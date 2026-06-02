package connector

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/bridgev2/simplevent"
	"maunium.net/go/mautrix/bridgev2/status"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	agui "github.com/beeper/ai-bridge/pkg/ag-ui"
	agent "github.com/beeper/ai-bridge/pkg/agent"
	"github.com/beeper/ai-bridge/pkg/agent/autocompact"
	"github.com/beeper/ai-bridge/pkg/agent/harness"
	"github.com/beeper/ai-bridge/pkg/agent/harness/session"
	"github.com/beeper/ai-bridge/pkg/agent/sessiontitle"
	ai "github.com/beeper/ai-bridge/pkg/ai"
	aistream "github.com/beeper/ai-bridge/pkg/ai-stream"
	aibridgev2 "github.com/beeper/ai-bridge/pkg/ai-stream/bridgev2"
	aimatrix "github.com/beeper/ai-bridge/pkg/ai-stream/matrix"
	aiutils "github.com/beeper/ai-bridge/pkg/ai/utils"
	"github.com/beeper/ai-bridge/pkg/aidb"
	"github.com/beeper/ai-bridge/pkg/aiid"
	"github.com/beeper/ai-bridge/pkg/msgconv"
)

type Client struct {
	Main      *Connector
	UserLogin *bridgev2.UserLogin
	loggedIn  bool

	activeMu                sync.Mutex
	activeHarnesses         map[networkid.PortalKey]*harness.AgentHarness
	activeRuns              map[networkid.PortalKey]*activeAIRun
	activeStreamJanitorMu   sync.Mutex
	activeStreamJanitorStop context.CancelFunc
	providerAuthMu          sync.Mutex
	contactCacheMu          sync.Mutex
	contactCache            modelContactsCache
	contactRefreshMu        sync.Mutex
	catalogCacheMu          sync.Mutex
	catalogCache            providerCatalogCache
}

var activeStreamIdleTimeout = 5 * time.Minute

func aguiFinishReasonFromAI(reason ai.StopReason) string {
	switch reason {
	case "", ai.StopReasonStop:
		return agui.FinishReasonStop
	case ai.StopReasonLength:
		return agui.FinishReasonLength
	case ai.StopReasonToolUse:
		return agui.FinishReasonToolCalls
	case ai.StopReasonAborted:
		return agui.FinishReasonCancelled
	default:
		return agui.FinishReasonOther
	}
}

type pendingAIMessage struct {
	msg         *bridgev2.MatrixMessage
	txnID       networkid.TransactionID
	metadata    *aiid.MessageMetadata
	replyTo     *networkid.MessageOptionalPartID
	text        string
	attachments []ai.ContentBlock
}

type activeAIRun struct {
	portalKey networkid.PortalKey
	harness   *harness.AgentHarness
	provider  aiid.ProviderConfig
	model     ai.Model
	runID     string

	mu        sync.Mutex
	pending   []*pendingAIMessage
	consumed  []*pendingAIMessage
	streams   []*assistantStreamState
	last      *assistantStreamState
	status    *bridgev2.MessageStatusEventInfo
	approvals *aistream.ApprovalCoordinator
	echoWaits []*consumedEchoWait

	lastConsumedTimestamp time.Time
}

type consumedEchoWait struct {
	done chan struct{}
	err  error
}

type assistantStreamState struct {
	messageID       networkid.MessageID
	eventID         id.EventID
	runID           string
	run             *aistream.Run
	portal          *bridgev2.Portal
	metadata        *aiid.MessageMetadata
	entryID         string
	anchorTimestamp time.Time
	tools           []toolOutputEvent
	sources         *sourceCollector
	publish         streamPublishCursor
}

type streamPublishCursor struct {
	mu              sync.Mutex
	published       int
	nextSeq         int
	started         bool
	lastPersisted   int
	lastPersistedAt time.Time
	persist         func(context.Context, *aistream.Run) error
}

var _ bridgev2.NetworkAPI = (*Client)(nil)
var _ bridgev2.NetworkAPIWithUserID = (*Client)(nil)
var _ bridgev2.RedactionHandlingNetworkAPI = (*Client)(nil)
var _ bridgev2.RoomNameHandlingNetworkAPI = (*Client)(nil)
var _ bridgev2.RoomTopicHandlingNetworkAPI = (*Client)(nil)
var _ bridgev2.DisappearTimerChangingNetworkAPI = (*Client)(nil)
var _ bridgev2.DeleteChatHandlingNetworkAPI = (*Client)(nil)
var _ bridgev2.GroupCreatingNetworkAPI = (*Client)(nil)
var _ bridgev2.IdentifierResolvingNetworkAPI = (*Client)(nil)
var _ bridgev2.ContactListingNetworkAPI = (*Client)(nil)
var _ bridgev2.UserSearchingNetworkAPI = (*Client)(nil)

func (cl *Client) Connect(ctx context.Context) {
	cl.loggedIn = true
	cl.sendBridgeState(status.StateConnected)
	cl.refreshModelContactCacheAsync(ctx)
	cl.failPersistedActiveStreams(ctx)
	cl.startActiveStreamJanitor(ctx)
}

func (cl *Client) Disconnect() {
	cl.loggedIn = false
	cl.stopActiveStreamJanitor()
}

func (cl *Client) IsLoggedIn() bool {
	return cl.loggedIn
}

func (cl *Client) LogoutRemote(ctx context.Context) {
	cl.loggedIn = false
	cl.stopActiveStreamJanitor()
	cl.sendBridgeState(status.StateLoggedOut)
}

func (cl *Client) sendBridgeState(state status.BridgeStateEvent) {
	if cl != nil && cl.UserLogin != nil && cl.UserLogin.BridgeState != nil {
		cl.UserLogin.Log.Debug().
			Str("action", "ai_bridge_state").
			Str("login_id", string(cl.UserLogin.ID)).
			Str("state_event", string(state)).
			Msg("Sending AI bridge state")
		cl.UserLogin.BridgeState.Send(status.BridgeState{StateEvent: state})
	}
}

func (cl *Client) GetUserID() networkid.UserID {
	return networkid.UserID("login:" + string(cl.UserLogin.ID))
}

func (cl *Client) IsThisUser(ctx context.Context, userID networkid.UserID) bool {
	return userID == cl.GetUserID()
}

func (cl *Client) GetChatInfo(ctx context.Context, portal *bridgev2.Portal) (*bridgev2.ChatInfo, error) {
	name := cl.defaultConversationTitle(ctx, portal)
	if portal != nil && portal.NameSet {
		name = portal.Name
	}
	var topic *string
	if portal != nil && portal.TopicSet {
		topic = &portal.Topic
	}
	var disappear *database.DisappearingSetting
	if portal != nil && portal.Disappear.Type != "" {
		disappearSetting := portal.Disappear
		disappear = &disappearSetting
	}
	roomType := database.RoomTypeDM
	return &bridgev2.ChatInfo{Name: &name, Topic: topic, Avatar: defaultAIAssistantAvatar(), Type: &roomType, Members: aiChatMembers(), Disappear: disappear, ExcludeChangesFromTimeline: true}, nil
}

func (cl *Client) GetUserInfo(ctx context.Context, ghost *bridgev2.Ghost) (*bridgev2.UserInfo, error) {
	return aiAssistantUserInfo(), nil
}

func (cl *Client) CreateGroup(ctx context.Context, params *bridgev2.GroupCreateParams) (*bridgev2.CreateChatResponse, error) {
	if params == nil || params.RoomID == "" {
		return nil, fmt.Errorf("AI sessions must be created from an existing Matrix room")
	}
	if cl == nil || cl.Main == nil || cl.Main.Bridge == nil || cl.UserLogin == nil {
		return nil, fmt.Errorf("AI session creation requires a loaded bridge login")
	}
	if params.Type != "" && params.Type != "ai" {
		return nil, fmt.Errorf("unsupported AI group type %s", params.Type)
	}
	name := "AI"
	if params.Name != nil && params.Name.Name != "" {
		name = params.Name.Name
	}
	var topic *string
	if params.Topic != nil {
		topic = &params.Topic.Topic
	}
	var disappear *database.DisappearingSetting
	if params.Disappear != nil {
		disappearSetting := database.DisappearingSettingFromEvent(params.Disappear)
		disappear = &disappearSetting
	}
	roomType := database.RoomTypeDM
	portalKey := aiid.PortalKey(params.RoomID, cl.UserLogin.ID)
	info := &bridgev2.ChatInfo{
		Name:                       &name,
		Topic:                      topic,
		Avatar:                     defaultAIAssistantAvatar(),
		Type:                       &roomType,
		Members:                    aiChatMembers(),
		Disappear:                  disappear,
		ExcludeChangesFromTimeline: true,
	}
	portal, err := cl.Main.Bridge.GetPortalByKey(ctx, portalKey)
	if err != nil {
		return nil, err
	}
	err = portal.UpdateMatrixRoomID(ctx, params.RoomID, bridgev2.UpdateMatrixRoomIDParams{
		ChatInfoSource: cl.UserLogin,
		SyncDBMetadata: func() {
			portal.Name = name
			portal.NameSet = name != ""
			if topic != nil {
				portal.Topic = *topic
				portal.TopicSet = *topic != ""
			}
			portal.RoomType = roomType
			if disappear != nil {
				portal.Disappear = *disappear
			}
		},
	})
	if err != nil {
		return nil, err
	}
	return &bridgev2.CreateChatResponse{
		PortalKey:  portalKey,
		Portal:     portal,
		PortalInfo: info,
	}, nil
}

func (cl *Client) HandleMatrixMessage(ctx context.Context, msg *bridgev2.MatrixMessage) (*bridgev2.MatrixMessageResponse, error) {
	resp, err := cl.handleMatrixMessage(ctx, msg)
	if err != nil && msg != nil && msg.Portal != nil {
		cl.logMatrixMessageError(msg, err, "AI prompt failed")
		err = matrixMessageStatusForAIError(err)
	}
	return resp, err
}

func (cl *Client) handleMatrixMessage(ctx context.Context, msg *bridgev2.MatrixMessage) (*bridgev2.MatrixMessageResponse, error) {
	if err := cl.ensureUsablePortal(msg.Portal); err != nil {
		return nil, err
	}
	cl.updateLastKnownTimezoneFromMessage(ctx, msg)
	if resp, handled, err := cl.handleAISlashCommand(ctx, msg); handled {
		return resp, err
	}
	roomConfig, _, err := cl.Main.ReadRoomConfig(ctx, msg.Portal.MXID)
	if err != nil {
		return nil, err
	}
	var resp *bridgev2.MatrixMessageResponse
	var handled bool
	if roomConfig, resp, handled, err = cl.normalizeRoomStateForPrompt(ctx, msg, roomConfig); handled || err != nil {
		return resp, err
	}
	return cl.handleMatrixMessageWithConfig(ctx, msg, roomConfig)
}

func (cl *Client) handleMatrixMessageWithConfig(ctx context.Context, msg *bridgev2.MatrixMessage, roomConfig RoomConfig) (*bridgev2.MatrixMessageResponse, error) {
	portalMeta := portalMetadata(msg.Portal)
	provider, modelID, err := cl.resolveProvider(ctx, roomConfig)
	if err != nil {
		return nil, err
	}
	model := cl.Main.ModelForProvider(provider, modelID)
	if err := cl.validateReasoningLevel(model, roomConfig); err != nil {
		return nil, err
	}
	if err := cl.validateReasoningMode(model, roomConfig); err != nil {
		return nil, err
	}
	prompt, err := msgconv.FromMatrix(ctx, cl.Main.Bridge.Matrix.BotIntent(), msg)
	if err != nil {
		return nil, err
	}
	if hasPromptAttachmentType(prompt.Attachments, "image") && !isImageModel(model) {
		return nil, fmt.Errorf("model %s does not support image input", model.ID)
	}
	if hasPromptAttachmentType(prompt.Attachments, "audio") && !isAudioModel(model) {
		return nil, fmt.Errorf("model %s does not support audio input", model.ID)
	}
	if unsupported := unsupportedPromptAudioAttachment(prompt.Attachments); unsupported != "" {
		return nil, fmt.Errorf("unsupported audio MIME type %s", unsupported)
	}
	agentSession, err := cl.sessionForPortal(ctx, msg.Portal, portalMeta)
	if err != nil {
		return nil, err
	}
	if active := cl.getActiveRun(msg.Portal.PortalKey); active != nil && active.harness != nil {
		pending := cl.preparePendingAIMessage(ctx, msg, prompt, provider.ID, model.ID, "", active.replyTarget())
		active.addPending(pending)
		if err := active.harness.Steer(context.WithoutCancel(ctx), prompt.Text, prompt.Attachments...); err == nil {
			return &bridgev2.MatrixMessageResponse{DB: pending.dbMessage(), Pending: true}, nil
		}
		active.removePending(pending)
		pending.msg.RemovePending(pending.txnID)
		cl.clearActiveRun(msg.Portal.PortalKey, active)
	}
	pending := cl.preparePendingAIMessage(ctx, msg, prompt, provider.ID, model.ID, "", nil)
	cl.startAsyncPrompt(context.WithoutCancel(ctx), msg, portalMeta, roomConfig, provider, model, agentSession, prompt, pending)
	return &bridgev2.MatrixMessageResponse{DB: pending.dbMessage(), Pending: true}, nil
}

func hasPromptAttachmentType(blocks []ai.ContentBlock, blockType string) bool {
	for _, block := range blocks {
		if block.Type == blockType {
			return true
		}
	}
	return false
}

func unsupportedPromptAudioAttachment(blocks []ai.ContentBlock) string {
	for _, block := range blocks {
		if block.Type == "audio" && !nativeAudioMimeSupported(block.MimeType) {
			return block.MimeType
		}
	}
	return ""
}

func nativeAudioMimeSupported(mimeType string) bool {
	switch strings.ToLower(strings.TrimSpace(strings.Split(mimeType, ";")[0])) {
	case "audio/wav", "audio/x-wav", "audio/mpeg", "audio/mp3":
		return true
	default:
		return false
	}
}

func (cl *Client) startAsyncPrompt(ctx context.Context, msg *bridgev2.MatrixMessage, portalMeta *aiid.PortalMetadata, roomConfig RoomConfig, provider aiid.ProviderConfig, model ai.Model, agentSession *session.Session, prompt msgconv.MatrixPrompt, pending *pendingAIMessage) {
	streamPublisher := cl.Main.Bridge.GetBeeperStreamPublisher()
	runID := session.CreateSessionID()
	active := &activeAIRun{portalKey: msg.Portal.PortalKey, provider: provider, model: model, runID: runID}
	streamFn := cl.assistantStreamPublisher(streamPublisher, msg.Portal, portalMeta, provider, model, func() {
		cl.queueAssistantTyping(msg.Portal.PortalKey, 0)
	})
	chatFirstMessageAt := ""
	if sessionMeta, err := agentSession.GetMetadata(ctx); err == nil {
		chatFirstMessageAt = sessionMeta.CreatedAt
	}
	options := harness.AgentHarnessOptions{
		Session:             agentSession,
		Model:               model,
		ThinkingLevel:       agent.ThinkingLevel(cl.reasoningLevelForModel(model, roomConfig)),
		SystemPrompt:        cl.systemPrompt(roomConfig),
		Tools:               cl.chatTools(msg, portalMeta, roomConfig, provider, model, chatFirstMessageAt, chatToolsApprovalContext{publisher: streamPublisher, active: active}),
		StreamFn:            streamFn,
		GetAPIKeyAndHeaders: cl.authForProvider(provider),
		CompactionSettings:  cl.Main.Config.Compaction.Settings(),
	}
	agentHarness, err := harness.NewAgentHarness(options)
	if err != nil {
		cl.markPendingFailed(ctx, pending, err)
		return
	}
	cl.registerProviderBuiltInToolHooks(agentHarness, roomConfig)
	active.harness = agentHarness
	active.addPending(pending)
	cl.setActiveHarness(msg.Portal.PortalKey, agentHarness)
	cl.setActiveRun(msg.Portal.PortalKey, active)
	go cl.runAsyncPrompt(ctx, msg, portalMeta, provider, model, agentSession, streamPublisher, agentHarness, active, prompt)
}

func (cl *Client) generateSessionTitle(ctx context.Context, portal *bridgev2.Portal, meta *aiid.PortalMetadata, agentSession *session.Session, provider aiid.ProviderConfig, model ai.Model) {
	if portal == nil || meta == nil || !meta.AutoTitlePending {
		return
	}
	contextView, err := agentSession.BuildContext(ctx)
	if err != nil || len(contextView.Messages) < 2 {
		if err != nil {
			zerolog.Ctx(ctx).Debug().Err(err).Str("action", "session_title").Msg("Skipping session title generation")
		} else {
			zerolog.Ctx(ctx).Debug().Str("action", "session_title").Int("message_count", len(contextView.Messages)).Msg("Skipping session title generation")
		}
		return
	}
	titleModel := cl.titleGenerationModel(provider, model)
	titleLog := zerolog.Ctx(ctx).With().
		Bool("session_title", true).
		Str("model_id", titleModel.ID).
		Str("provider", string(titleModel.Provider)).
		Logger()
	titleCtx := titleLog.WithContext(ctx)
	auth, err := cl.authForProvider(provider)(titleCtx, titleModel)
	if err != nil {
		titleLog.Debug().Err(err).Str("action", "session_title").Msg("Skipping session title generation")
		return
	}
	title, err := sessiontitle.Generate(titleCtx, contextView.Messages, sessiontitle.Options{
		Model:   titleModel,
		APIKey:  auth.APIKey,
		Headers: auth.Headers,
	})
	if err != nil || title == "" {
		if err != nil {
			titleLog.Debug().Err(err).Str("action", "session_title").Msg("Skipping session title generation")
		} else {
			titleLog.Debug().Str("action", "session_title").Msg("Skipping empty session title")
		}
		return
	}
	if _, err = agentSession.AppendSessionName(ctx, title); err != nil {
		titleLog.Debug().Err(err).Str("action", "session_title").Msg("Skipping session title update")
		return
	}
	meta.AutoTitlePending = false
	if err = portal.Save(ctx); err != nil {
		titleLog.Debug().Err(err).Str("action", "session_title").Msg("Skipping session title update")
		return
	}
	if cl != nil && cl.UserLogin != nil {
		titleLog.Debug().Str("action", "session_title").Str("title", title).Msg("Updating room title")
		portal.UpdateInfo(ctx, &bridgev2.ChatInfo{Name: &title}, cl.UserLogin, nil, time.Now())
	}
}

func (cl *Client) titleGenerationModel(provider aiid.ProviderConfig, fallback ai.Model) ai.Model {
	for _, modelID := range titleGenerationModelIDs(provider) {
		if !providerAllowsModel(provider, modelID) {
			continue
		}
		if cl != nil && cl.Main != nil {
			return cl.Main.ModelForProvider(provider, modelID)
		}
		return normalizeProviderModel(modelForProviderConfig(provider, modelID), provider)
	}
	return fallback
}

func titleGenerationModelIDs(provider aiid.ProviderConfig) []string {
	switch provider.Provider {
	case ai.ProviderOpenAI:
		return []string{defaultTitleGenerationModel, fallbackTitleGenerationModel}
	case ai.ProviderOpenRouter:
		return []string{openRouterTitleGenerationModel, openRouterFallbackTitleGenerationModel}
	default:
		return nil
	}
}

func (cl *Client) queueAssistantTyping(portalKey networkid.PortalKey, timeout time.Duration) {
	cl.UserLogin.QueueRemoteEvent(&simplevent.Typing{
		EventMeta: simplevent.EventMeta{
			Type:      bridgev2.RemoteEventTyping,
			PortalKey: portalKey,
			Sender: bridgev2.EventSender{
				Sender: aiid.AssistantUserID(),
			},
			Timestamp: time.Now(),
		},
		Timeout: timeout,
		Type:    bridgev2.TypingTypeText,
	})
}

func fillAssistantMetadata(metadata *aiid.MessageMetadata, entryID string, providerID string, modelID string, runID string, assistantMessage ai.Message) {
	metadata.SessionEntryID = entryID
	metadata.Role = "assistant"
	metadata.ProviderID = providerID
	metadata.ModelID = modelID
	metadata.ResponseID = assistantMessage.ResponseID
	metadata.RunID = runID
	metadata.Usage = assistantMessage.Usage
	metadata.StopReason = string(assistantMessage.StopReason)
	metadata.StreamStatus = "done"
}

func (cl *Client) runAsyncPrompt(ctx context.Context, msg *bridgev2.MatrixMessage, portalMeta *aiid.PortalMetadata, provider aiid.ProviderConfig, model ai.Model, agentSession *session.Session, streamPublisher bridgev2.BeeperStreamPublisher, agentHarness *harness.AgentHarness, active *activeAIRun, prompt msgconv.MatrixPrompt) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err := fmt.Errorf("AI run panicked: %v", recovered)
			active.failConsumed(ctx, cl, err)
			active.failAll(ctx, cl, err)
			active.failOpenAssistant(ctx, cl, provider.ID, model.ID, err)
		}
	}()
	defer cl.clearActiveHarness(msg.Portal.PortalKey, agentHarness)
	defer cl.clearActiveRun(msg.Portal.PortalKey, active)
	defer cl.queueAssistantTyping(msg.Portal.PortalKey, 0)
	defer active.failAll(ctx, cl, fmt.Errorf("AI run ended before queued message was consumed"))

	cl.queueAssistantTyping(msg.Portal.PortalKey, 30*time.Second)
	unsubscribeToolOutputs := agentHarness.Subscribe(func(ctx context.Context, event harness.AgentHarnessEvent) error {
		if event.Type == "message_end" && event.Message != nil && event.Message.Role == "user" && event.SessionEntryID != "" {
			active.markConsumed(ctx, cl, event.SessionEntryID)
			return nil
		}
		if event.Type == "message_end" && event.Message != nil && event.Message.Role == "assistant" && event.SessionEntryID != "" {
			active.setAssistantEntryID(event.SessionEntryID)
			return nil
		}
		if event.Type != "tool_execution_end" || event.AgentEvent == nil || event.AgentEvent.ToolCallID == "" {
			if event.Type == "turn_end" && event.Message != nil && event.Message.Role == "assistant" {
				active.finalizeAssistant(ctx, cl, provider.ID, model, cl.withProviderVisibleError(ctx, provider, *event.Message))
			}
			return nil
		}
		result, ok := event.AgentEvent.Result.(agent.AgentToolResult[any])
		if !ok {
			return nil
		}
		output := toolOutputEvent{
			ID:      event.AgentEvent.ToolCallID,
			Name:    event.AgentEvent.ToolName,
			Input:   event.AgentEvent.Args,
			Result:  result,
			IsError: event.AgentEvent.IsError,
		}
		if err := active.publishToolOutput(ctx, cl, streamPublisher, msg.Portal.MXID, output); err != nil {
			cl.logStreamError(err, msg.Portal.MXID, "", nil, "Failed to publish AI tool output stream carrier")
		}
		return nil
	})
	defer unsubscribeToolOutputs()

	promptResult, err := agentHarness.PromptWithResult(ctx, prompt.Text, prompt.Attachments...)
	if err != nil {
		cl.logMatrixMessageError(msg, err, "AI harness prompt failed")
		active.failConsumed(ctx, cl, err)
		active.failAll(ctx, cl, err)
		active.failOpenAssistant(ctx, cl, provider.ID, model.ID, err)
		return
	}
	overflowRecoveryAttempted := false
	for {
		assistantMessage := promptResult.Message
		if promptResult.AssistantEntryID == "" {
			err := fmt.Errorf("prompt did not create expected assistant session entry")
			active.failAll(ctx, cl, err)
			active.failOpenAssistant(ctx, cl, provider.ID, model.ID, err)
			return
		}
		if assistantMessage.StopReason == ai.StopReasonError && aiutils.IsContextOverflow(assistantMessage, model.ContextWindow) {
			if overflowRecoveryAttempted {
				err := errors.New("Context overflow recovery failed after one compact-and-retry attempt. Try reducing context or switching to a larger-context model.")
				active.failConsumed(ctx, cl, err)
				assistantMessage.StopReason = ai.StopReasonError
				assistantMessage.ErrorMessage = err.Error()
			} else {
				overflowRecoveryAttempted = true
				if last := active.lastAssistant(); last != nil {
					result, compacted := cl.runAutoCompaction(ctx, streamPublisher, msg.Portal.MXID, last.eventID, agentHarness, agentSession, model, assistantMessage)
					if compacted && result.Reason == autocompact.ReasonOverflow {
						promptResult, err = agentHarness.ContinueWithResult(ctx, harness.ContinueOptions{DropTrailingAssistantError: true})
						if err != nil {
							cl.logMatrixMessageError(msg, err, "AI harness continuation failed")
							active.failConsumed(ctx, cl, err)
							active.failAll(ctx, cl, err)
							active.failOpenAssistant(ctx, cl, provider.ID, model.ID, err)
							return
						}
						continue
					}
				}
			}
		}
		if assistantMessage.StopReason == ai.StopReasonError {
			assistantMessage = cl.withProviderVisibleError(ctx, provider, assistantMessage)
			err := errors.New(assistantMessage.ErrorMessage)
			if assistantMessage.ErrorMessage == "" {
				err = errors.New("AI failed to respond")
			}
			active.failConsumed(ctx, cl, err)
			active.failOpenAssistant(ctx, cl, provider.ID, model.ID, err)
		}
		if assistantMessage.StopReason != ai.StopReasonError && assistantMessage.StopReason != ai.StopReasonAborted {
			if last := active.lastAssistant(); last != nil {
				cl.runAutoCompaction(ctx, streamPublisher, msg.Portal.MXID, last.eventID, agentHarness, agentSession, model, assistantMessage)
			}
		}
		portalMeta.LastRunID = active.lastRunID()
		_ = msg.Portal.Save(ctx)
		if assistantMessage.StopReason != ai.StopReasonError && assistantMessage.StopReason != ai.StopReasonAborted && !assistantMessageHasToolCalls(assistantMessage) {
			cl.generateSessionTitle(ctx, msg.Portal, portalMeta, agentSession, provider, model)
		}
		break
	}
}

func (cl *Client) preparePendingAIMessage(ctx context.Context, msg *bridgev2.MatrixMessage, prompt msgconv.MatrixPrompt, providerID string, modelID string, runID string, replyTo *networkid.MessageOptionalPartID) *pendingAIMessage {
	txnID := aiPendingTransactionID(msg)
	metadata := &aiid.MessageMetadata{
		Role:         "user",
		ProviderID:   providerID,
		ModelID:      modelID,
		RunID:        runID,
		StreamStatus: "pending",
	}
	pending := &pendingAIMessage{
		msg:         msg,
		txnID:       txnID,
		metadata:    metadata,
		replyTo:     replyTo,
		text:        prompt.Text,
		attachments: append([]ai.ContentBlock(nil), prompt.Attachments...),
	}
	msg.AddPendingToSave(pending.dbMessage(), txnID, nil)
	cl.sendPendingStatus(ctx, msg, "Queued for AI")
	return pending
}

func aiPendingTransactionID(msg *bridgev2.MatrixMessage) networkid.TransactionID {
	if msg != nil && msg.Event != nil && msg.Event.ID != "" {
		return networkid.TransactionID("ai:" + string(msg.Event.ID))
	}
	return networkid.TransactionID("ai:" + session.CreateSessionID())
}

func (p *pendingAIMessage) dbMessage() *database.Message {
	dbMessage := &database.Message{
		ID:        networkid.MessageID("pending:" + string(p.txnID)),
		PartID:    aiid.PartID("text"),
		Room:      p.msg.Portal.PortalKey,
		SenderID:  networkid.UserID(""),
		Timestamp: matrixEventTime(p.msg.Event),
		Metadata:  p.metadata,
	}
	if p.replyTo != nil {
		dbMessage.ReplyTo = *p.replyTo
	}
	return dbMessage
}

func (cl *Client) sendPendingStatus(ctx context.Context, msg *bridgev2.MatrixMessage, text string) {
	cl.Main.Bridge.Matrix.SendMessageStatus(ctx, &bridgev2.MessageStatus{
		Status:  event.MessageStatusPending,
		Message: text,
	}, bridgev2.StatusEventInfoFromEvent(msg.Event))
}

func (cl *Client) markPendingFailed(ctx context.Context, pending *pendingAIMessage, err error) {
	if pending == nil || pending.msg == nil {
		return
	}
	pending.msg.RemovePending(pending.txnID)
	cl.markConsumedFailed(ctx, pending, err)
}

func (cl *Client) markConsumedFailed(ctx context.Context, pending *pendingAIMessage, err error) {
	if pending == nil || pending.msg == nil {
		return
	}
	status := matrixMessageStatusForAIError(err)
	cl.Main.Bridge.Matrix.SendMessageStatus(ctx, &status, bridgev2.StatusEventInfoFromEvent(pending.msg.Event))
}

func (cl *Client) queueConsumedUserEcho(ctx context.Context, pending *pendingAIMessage, userEntryID string, echoTimestamp time.Time) error {
	if pending == nil || pending.msg == nil {
		return nil
	}
	pending.metadata.SessionEntryID = userEntryID
	pending.metadata.Role = "user"
	pending.metadata.StreamStatus = "done"
	messageID := aiid.UserMessageID(userEntryID)
	result := cl.UserLogin.QueueRemoteEvent(&simplevent.PreConvertedMessage{
		EventMeta: simplevent.EventMeta{
			Type:      bridgev2.RemoteEventMessage,
			PortalKey: pending.msg.Portal.PortalKey,
			Sender: bridgev2.EventSender{
				Sender: cl.GetUserID(),
			},
			Timestamp:   echoTimestamp,
			StreamOrder: echoTimestamp.UnixNano(),
		},
		ID:            messageID,
		TransactionID: pending.txnID,
		Data: &bridgev2.ConvertedMessage{Parts: []*bridgev2.ConvertedMessagePart{{
			ID:         aiid.PartID("text"),
			Type:       event.EventMessage,
			Content:    msgconv.TextContent(pending.text),
			DBMetadata: pending.metadata,
		}}},
	})
	if !result.Success {
		if result.Error != nil {
			return result.Error
		}
		return fmt.Errorf("failed to queue consumed user echo")
	}
	if result.EventID != "" {
		return nil
	}
	_, err := cl.waitForMessageEventID(ctx, pending.msg.Portal, messageID, aiid.PartID("text"), 30*time.Second)
	return err
}

func (cl *Client) queueAssistantRunError(portalKey networkid.PortalKey, messageID networkid.MessageID, providerID string, modelID string, runID string, run aistream.Run, metadata *aiid.MessageMetadata, timestamp time.Time, err error) {
	message := ai.Message{
		Role:         "assistant",
		StopReason:   ai.StopReasonError,
		ErrorMessage: err.Error(),
	}
	if metadata != nil {
		metadata.ErrorMessage = err.Error()
		metadata.StopReason = string(ai.StopReasonError)
		metadata.StreamStatus = "error"
	}
	run = finalizedAssistantRun(run, message, 0)
	cl.UserLogin.QueueRemoteEvent(cl.assistantFinalEditWithProjection(portalKey, messageID, providerID, modelID, run, metadata, timestamp))
}

func hookStreamError(err error) *ai.AssistantMessageEventStream {
	stream := ai.NewAssistantMessageEventStream()
	go func() {
		defer stream.End()
		stream.Push(ai.AssistantMessageEvent{
			Type: "error",
			Error: &ai.Message{
				Role:         "assistant",
				ErrorMessage: err.Error(),
				StopReason:   ai.StopReasonError,
			},
		})
	}()
	return stream
}

func (cl *Client) HandleMatrixMessageRemove(ctx context.Context, msg *bridgev2.MatrixMessageRemove) error {
	if msg == nil || msg.Portal == nil {
		return nil
	}
	if err := cl.appendDeletedSessionPlaceholder(ctx, msg.Portal, msg.TargetMessage); err != nil {
		return err
	}
	active := cl.getActiveRun(msg.Portal.PortalKey)
	if active == nil || !active.matchesRedactionTarget(msg.TargetMessage) {
		return nil
	}
	h := cl.getActiveHarness(msg.Portal.PortalKey)
	if h == nil {
		return nil
	}
	_, err := h.Abort(ctx)
	active.failAll(ctx, cl, fmt.Errorf("AI run aborted"))
	return err
}

func (cl *Client) HandleMatrixDeleteChat(ctx context.Context, msg *bridgev2.MatrixDeleteChat) error {
	if msg == nil || msg.Portal == nil {
		return nil
	}
	active := cl.getActiveRun(msg.Portal.PortalKey)
	if active != nil {
		if h := cl.getActiveHarness(msg.Portal.PortalKey); h != nil {
			_, _ = h.Abort(ctx)
		}
		active.failAll(ctx, cl, fmt.Errorf("AI run aborted"))
	}
	meta := portalMetadata(msg.Portal)
	if meta.SessionID != "" {
		if err := ai.CleanupSessionResources(meta.SessionID); err != nil {
			return err
		}
		if cl != nil && cl.Main != nil && cl.Main.Store != nil {
			if err := cl.Main.Store.DeleteSession(ctx, cl.UserLogin.ID, meta.SessionID); err != nil {
				return err
			}
		}
	}
	return nil
}

func (cl *Client) appendDeletedSessionPlaceholder(ctx context.Context, portal *bridgev2.Portal, target *database.Message) error {
	if cl == nil || cl.Main == nil || cl.Main.Store == nil || portal == nil || target == nil {
		return nil
	}
	metadata, ok := target.Metadata.(*aiid.MessageMetadata)
	if !ok || metadata.SessionEntryID == "" {
		return nil
	}
	meta := portalMetadata(portal)
	if meta.SessionID == "" {
		return nil
	}
	agentSession, err := cl.Main.Store.OpenSession(ctx, cl.UserLogin.ID, session.SQLiteSessionMetadata{
		SessionMetadata: session.SessionMetadata{ID: meta.SessionID},
	})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		return err
	}
	if _, err = agentSession.AppendMessageDeletion(ctx, metadata.SessionEntryID); err != nil {
		if isSessionNotFound(err) {
			return nil
		}
		return err
	}
	return nil
}

func isSessionNotFound(err error) bool {
	if errors.Is(err, session.ErrSessionEntryNotFound) {
		return true
	}
	var sessionErr *session.SessionError
	return errors.As(err, &sessionErr) && sessionErr.Code == session.SessionErrorNotFound
}

func (cl *Client) HandleMatrixRoomTopic(ctx context.Context, msg *bridgev2.MatrixRoomTopic) (bool, error) {
	if msg == nil || msg.Portal == nil || msg.Content == nil {
		return false, nil
	}
	topic := msg.Content.Topic
	msg.Portal.Topic = topic
	msg.Portal.TopicSet = topic != ""
	meta := portalMetadata(msg.Portal)
	if meta.SessionID != "" {
		agentSession, err := cl.Main.Store.OpenSession(ctx, cl.UserLogin.ID, session.SQLiteSessionMetadata{
			SessionMetadata: session.SessionMetadata{ID: meta.SessionID},
		})
		if err == nil {
			_, _ = agentSession.AppendCustomMessageEntry(ctx, "room_topic", map[string]any{"topic": topic}, false, nil)
		} else if !errors.Is(err, sql.ErrNoRows) {
			return false, err
		}
	}
	return true, nil
}

func (cl *Client) HandleMatrixRoomName(ctx context.Context, msg *bridgev2.MatrixRoomName) (bool, error) {
	if msg == nil || msg.Portal == nil || msg.Content == nil {
		return false, nil
	}
	name := msg.Content.Name
	msg.Portal.Name = name
	msg.Portal.NameSet = name != ""
	meta := portalMetadata(msg.Portal)
	meta.AutoTitlePending = false
	if meta.SessionID != "" {
		agentSession, err := cl.Main.Store.OpenSession(ctx, cl.UserLogin.ID, session.SQLiteSessionMetadata{
			SessionMetadata: session.SessionMetadata{ID: meta.SessionID},
		})
		if err == nil {
			_, _ = agentSession.AppendSessionName(ctx, name)
		} else if !errors.Is(err, sql.ErrNoRows) {
			return false, err
		}
	}
	return true, nil
}

func (cl *Client) HandleMatrixDisappearingTimer(ctx context.Context, msg *bridgev2.MatrixDisappearingTimer) (bool, error) {
	if msg == nil || msg.Portal == nil {
		return false, nil
	}
	msg.Portal.Disappear = database.DisappearingSettingFromEvent(msg.Content)
	return true, nil
}

func (cl *Client) ensureUsablePortal(portal *bridgev2.Portal) error {
	if portal == nil {
		return wrapNoAIChatError("missing portal")
	}
	if portal.Receiver != "" && portal.Receiver != cl.UserLogin.ID {
		return wrapNoAIChatError("portal receiver %s does not match login %s", portal.Receiver, cl.UserLogin.ID)
	}
	return nil
}

func (cl *Client) defaultConversationTitle(ctx context.Context, portal *bridgev2.Portal) string {
	if cl != nil && cl.Main != nil && portal != nil && portal.MXID != "" {
		if roomConfig, _, err := cl.Main.ReadRoomConfig(ctx, portal.MXID); err == nil {
			if provider, modelID, err := cl.resolveProvider(ctx, roomConfig); err == nil && roomConfig.modelStatePresent {
				model := cl.Main.ModelForProvider(provider, modelID)
				if resolved, ok := resolveModelForProvider(provider, modelID); ok {
					model = resolved
				}
				return defaultConversationTitle(provider, model)
			}
		}
	}
	return "New AI Chat"
}

func (cl *Client) assistantEvent(ctx context.Context, portalKey networkid.PortalKey, messageID networkid.MessageID, providerID string, modelID string, runID string, descriptor *event.BeeperStreamInfo, run aistream.Run, timestamp time.Time) (*simplevent.PreConvertedMessage, *aiid.MessageMetadata) {
	metadata := &aiid.MessageMetadata{
		Role:         "assistant",
		ProviderID:   providerID,
		ModelID:      modelID,
		RunID:        runID,
		StreamStatus: "streaming",
	}
	msg := aibridgev2.Anchor(portalKey, aiid.AssistantUserID(), run, timestamp)
	if len(msg.Data.Parts) > 0 {
		msg.Data.Parts[0].ID = aiid.PartID("text")
		msg.Data.Parts[0].Content.BeeperStream = descriptor
		cl.applyModelProfile(ctx, msg.Data.Parts[0].Content, providerID, modelID)
		msg.Data.Parts[0].DBMetadata = metadata
	}
	return msg, metadata
}

func finalizedAssistantRun(run aistream.Run, message ai.Message, contextLimit int) aistream.Run {
	if message.StopReason == ai.StopReasonError {
		run.Status = aistream.Status{State: "error", Error: map[string]any{"message": message.ErrorMessage}}
		if run.Text() == "" {
			run.Preview = aistream.PreviewFromText(aistream.ErrorFallbackPlaintext(message.ErrorMessage), aistream.PreviewBudgetBytes)
		}
	} else if run.Status.State != "aborted" && run.Status.State != "error" {
		run.Status = aistream.Status{State: "complete", FinishReason: aguiFinishReasonFromAI(message.StopReason)}
	}
	if isZeroAGUIUsage(run.Usage) {
		run.Usage = aguiUsage(message.Usage, contextLimit)
	} else if run.Usage.ContextLimit == 0 && contextLimit > 0 {
		run.Usage.ContextLimit = contextLimit
	}
	if run.Preview.Text == "" {
		run.Preview = aistream.PreviewFromText(msgconv.AssistantText(message), aistream.PreviewBudgetBytes)
	}
	return run
}

func isZeroAGUIUsage(usage agui.Usage) bool {
	return usage.PromptTokens == 0 &&
		usage.CompletionTokens == 0 &&
		usage.ReasoningTokens == 0 &&
		usage.TotalTokens == 0 &&
		usage.ContextLimit == 0
}

func (cl *Client) assistantFinalEditWithProjection(portalKey networkid.PortalKey, messageID networkid.MessageID, providerID string, modelID string, run aistream.Run, metadata *aiid.MessageMetadata, timestamp time.Time) *simplevent.Message[*aistream.Run] {
	initialProjection := aimatrix.ProjectFinal(run, nil)
	edit := aibridgev2.FinalMetadataEditWithContent(portalKey, aiid.AssistantUserID(), messageID, run, initialProjection.Content, initialProjection.Extra, timestamp)
	edit.ConvertEditFunc = func(ctx context.Context, portal *bridgev2.Portal, intent bridgev2.MatrixAPI, existing []*database.Message, data *aistream.Run) (*bridgev2.ConvertedEdit, error) {
		if len(existing) == 0 {
			return nil, nil
		}
		projection := aimatrix.ProjectFinal(*data, nil)
		if projection.NeedsAttachment {
			partsRef, err := uploadFinalPartsRef(ctx, portal, intent, *data, projection.Message)
			if err != nil {
				return nil, err
			}
			projection = aimatrix.ProjectFinal(*data, partsRef)
		}
		existing[0].Metadata = metadata
		if projection.Content != nil {
			cl.applyModelProfile(ctx, projection.Content, providerID, modelID)
		}
		return &bridgev2.ConvertedEdit{
			ModifiedParts: []*bridgev2.ConvertedEditPart{{
				Part:    existing[0],
				Type:    event.EventMessage,
				Content: projection.Content,
				Extra:   projection.Extra,
				TopLevelExtra: map[string]any{
					"com.beeper.dont_render_edited": true,
				},
			}},
		}, nil
	}
	return edit
}

func (cl *Client) queueAssistantFinal(portalKey networkid.PortalKey, messageID networkid.MessageID, providerID string, model ai.Model, run aistream.Run, message ai.Message, metadata *aiid.MessageMetadata, timestamp time.Time) {
	if cl == nil || cl.UserLogin == nil {
		return
	}
	run = finalizedAssistantRun(run, message, model.ContextWindow)
	cl.UserLogin.QueueRemoteEvent(cl.assistantFinalEditWithProjection(portalKey, messageID, providerID, model.ID, run, metadata, timestamp))
}

func uploadFinalPartsRef(ctx context.Context, portal *bridgev2.Portal, intent bridgev2.MatrixAPI, run aistream.Run, message aistream.UIMessage) (*aistream.FinalPartsRef, error) {
	if portal == nil || portal.Portal == nil {
		return nil, fmt.Errorf("missing portal for AI final parts upload")
	}
	payload, err := json.Marshal(run.FinalPartsPayload(message))
	if err != nil {
		return nil, fmt.Errorf("failed to encode AI final parts: %w", err)
	}
	hash := sha256.Sum256(payload)
	url, file, err := intent.UploadMedia(ctx, portal.MXID, payload, fmt.Sprintf("ai-final-parts-%s.json", run.RunID), aistream.FinalPartsMediaType)
	if err != nil {
		return nil, fmt.Errorf("failed to upload AI final parts: %w", err)
	}
	ref := &aistream.FinalPartsRef{
		Schema:     aistream.FinalPartsRefSchema,
		MediaType:  aistream.FinalPartsMediaType,
		ByteSize:   len(payload),
		SHA256:     base64.RawURLEncoding.EncodeToString(hash[:]),
		PartsCount: len(message.Parts),
	}
	if file != nil {
		ref.File = file
	} else {
		ref.URL = string(url)
	}
	return ref, nil
}

func (cl *Client) applyModelProfile(ctx context.Context, content *event.MessageEventContent, providerID string, modelID string) {
	if content == nil {
		return
	}
	displayName := modelID
	profileID := string(aiid.ModelContactID(providerID, modelID))
	var avatarURL *id.ContentURIString
	if provider, ok := cl.providers()[providerID]; ok {
		if refreshed, err := cl.providerWithCatalogModelsStrict(ctx, provider); err == nil {
			provider = refreshed
		}
		model := ai.Model{ID: modelID, Name: modelID}
		if resolved, ok := resolveModelForProvider(provider, modelID); ok {
			model = resolved
		} else if cl.Main != nil {
			model = cl.Main.ModelForProvider(provider, modelID)
		}
		displayName = modelDisplayName(provider, model)
		profileID = string(aiid.ModelContactID(provider.ID, model.ID))
		if ghost, err := updateModelGhostInfo(ctx, cl.bridge(), provider, model); err == nil {
			profileID = string(ghost.ID)
			if ghost.Name != "" {
				displayName = ghost.Name
			}
			if ghost.AvatarMXC != "" {
				avatarURL = &ghost.AvatarMXC
			}
		}
	}
	content.BeeperPerMessageProfile = &event.BeeperPerMessageProfile{
		ID:          profileID,
		Displayname: displayName,
		AvatarURL:   avatarURL,
		HasFallback: true,
	}
}

func (cl *Client) assistantStreamPublisher(publisher bridgev2.BeeperStreamPublisher, portal *bridgev2.Portal, meta *aiid.PortalMetadata, provider aiid.ProviderConfig, model ai.Model, onSecondVisibleChunk ...func()) agent.StreamFn {
	return func(ctx context.Context, model ai.Model, llmContext ai.Context, options ai.SimpleStreamOptions) *ai.AssistantMessageEventStream {
		if active := cl.getActiveRun(portal.PortalKey); active != nil {
			if stream := active.currentAssistantStream(); stream != nil {
				return cl.streamPublisherWithEndFrom(publisher, portal.MXID, stream.eventID, stream.run, &stream.publish, nil, onSecondVisibleChunk...)(ctx, model, llmContext, options)
			}
			if message, ok := active.terminalAssistantError(); ok {
				if message == "" {
					message = "AI failed to respond"
				}
				return hookStreamError(errors.New(message))
			}
		}
		runID := session.CreateSessionID()
		messageID := aiid.AssistantMessageID(runID)
		anchorTimestamp := time.Now()
		if active := cl.getActiveRun(portal.PortalKey); active != nil {
			anchorTimestamp = active.nextAssistantAnchorTimestamp()
		}
		run := aistream.NewRun(runID, meta.SessionID, provider.ID+"/"+model.ID, string(aiid.AssistantUserID()), "AI", anchorTimestamp)
		run.MessageID = string(messageID)
		enrichAIRunMetadata(run, model, options)
		descriptor, err := publisher.NewDescriptor(ctx, portal.MXID, aiid.StreamType)
		if err != nil {
			cl.logStreamError(err, portal.MXID, "", run, "Failed to create AI stream descriptor")
			return hookStreamError(err)
		}
		cl.logStreamDebug(ctx, portal.MXID, "", run, "Created AI stream descriptor", func(evt *zerolog.Event) {
			evt.Str("stream_type", descriptor.Type).Str("descriptor_user_id", string(descriptor.UserID))
		})
		assistantEvent, metadata := cl.assistantEvent(ctx, portal.PortalKey, messageID, provider.ID, model.ID, runID, descriptor, *run, anchorTimestamp)
		if active := cl.getActiveRun(portal.PortalKey); active != nil {
			stream := &assistantStreamState{
				messageID:       messageID,
				runID:           runID,
				run:             run,
				portal:          portal,
				metadata:        metadata,
				anchorTimestamp: anchorTimestamp,
				sources:         newSourceCollector(),
				publish:         streamPublishCursor{nextSeq: 1},
			}
			stream.publish.persist = func(ctx context.Context, run *aistream.Run) error {
				return cl.persistActiveStream(ctx, portal, provider.ID, model.ID, active, stream, run)
			}
			return cl.streamPublisherWithAnchorSetup(ctx, model, llmContext, options, publisher, portal.MXID, run, &stream.publish, func(setupCtx context.Context) (id.EventID, func(), error) {
				if err := active.waitForConsumedEchoes(setupCtx); err != nil {
					cl.logStreamError(err, portal.MXID, "", run, "Timed out waiting for consumed user echo before AI stream anchor")
					return "", nil, err
				}
				eventID, err := cl.queueAssistantStreamAnchor(setupCtx, portal, assistantEvent, messageID)
				if err != nil {
					cl.logStreamError(err, portal.MXID, "", run, "Failed to queue AI stream anchor")
					return "", nil, err
				}
				cl.logStreamDebug(setupCtx, portal.MXID, eventID, run, "Queued AI stream anchor")
				if err := publisher.Register(setupCtx, portal.MXID, eventID, descriptor); err != nil {
					cl.logStreamError(err, portal.MXID, eventID, run, "Failed to register AI stream publisher")
					cl.queueAssistantRunError(portal.PortalKey, messageID, provider.ID, model.ID, runID, *run, metadata, afterMatrixTimestamp(anchorTimestamp), err)
					return eventID, nil, err
				}
				cl.logStreamDebug(setupCtx, portal.MXID, eventID, run, "Registered AI stream publisher", func(evt *zerolog.Event) {
					evt.Str("stream_type", descriptor.Type)
				})
				stream.eventID = eventID
				active.addAssistantStream(stream)
				return eventID, nil, nil
			}, onSecondVisibleChunk...)
		}
		return cl.streamPublisherWithAnchorSetup(ctx, model, llmContext, options, publisher, portal.MXID, run, &streamPublishCursor{nextSeq: 1}, func(setupCtx context.Context) (id.EventID, func(), error) {
			eventID, err := cl.queueAssistantStreamAnchor(setupCtx, portal, assistantEvent, messageID)
			if err != nil {
				cl.logStreamError(err, portal.MXID, "", run, "Failed to queue AI stream anchor")
				return "", nil, err
			}
			cl.logStreamDebug(setupCtx, portal.MXID, eventID, run, "Queued AI stream anchor")
			if err := publisher.Register(setupCtx, portal.MXID, eventID, descriptor); err != nil {
				cl.logStreamError(err, portal.MXID, eventID, run, "Failed to register AI stream publisher")
				cl.queueAssistantRunError(portal.PortalKey, messageID, provider.ID, model.ID, runID, *run, metadata, afterMatrixTimestamp(anchorTimestamp), err)
				return eventID, nil, err
			}
			cl.logStreamDebug(setupCtx, portal.MXID, eventID, run, "Registered AI stream publisher", func(evt *zerolog.Event) {
				evt.Str("stream_type", descriptor.Type)
			})
			return eventID, func() {
				publisher.Unregister(portal.MXID, eventID)
			}, nil
		}, onSecondVisibleChunk...)
	}
}

func (cl *Client) queueAssistantStreamAnchor(ctx context.Context, portal *bridgev2.Portal, assistantEvent *simplevent.PreConvertedMessage, messageID networkid.MessageID) (id.EventID, error) {
	if cl == nil || cl.UserLogin == nil || portal == nil || assistantEvent == nil {
		return "", fmt.Errorf("missing stream anchor context")
	}
	result := cl.UserLogin.QueueRemoteEvent(assistantEvent)
	if !result.Success {
		if result.Error != nil {
			return "", result.Error
		}
		return "", fmt.Errorf("failed to queue stream anchor")
	}
	if result.EventID != "" {
		return result.EventID, nil
	}
	eventID, err := cl.waitForMessageEventID(ctx, portal, messageID, aiid.PartID("text"), 30*time.Second)
	if err == nil && eventID != "" {
		return eventID, nil
	}
	if cl.Main != nil && cl.Main.Bridge != nil && cl.Main.Bridge.Matrix != nil {
		deterministicEventID := cl.Main.Bridge.Matrix.GenerateDeterministicEventID(portal.MXID, portal.PortalKey, messageID, aiid.PartID("text"))
		zerolog.Ctx(ctx).Warn().
			Err(err).
			Str("event_id", string(deterministicEventID)).
			Msg("Falling back to deterministic AI stream anchor event ID")
		return deterministicEventID, nil
	}
	return "", err
}

func (cl *Client) waitForMessageEventID(ctx context.Context, portal *bridgev2.Portal, messageID networkid.MessageID, partID networkid.PartID, timeout time.Duration) (id.EventID, error) {
	if cl == nil || cl.Main == nil || cl.Main.Bridge == nil || portal == nil {
		return "", fmt.Errorf("missing message store for stream anchor")
	}
	return aibridgev2.WaitForMessageEventID(ctx, cl.Main.Bridge, portal.Receiver, messageID, partID, timeout)
}

func (cl *Client) streamPublisher(publisher bridgev2.BeeperStreamPublisher, roomID id.RoomID, eventID id.EventID, run *aistream.Run, onSecondVisibleChunk ...func()) agent.StreamFn {
	return cl.streamPublisherWithEnd(publisher, roomID, eventID, run, nil, onSecondVisibleChunk...)
}

func (cl *Client) streamPublisherWithAnchorSetup(ctx context.Context, model ai.Model, llmContext ai.Context, options ai.SimpleStreamOptions, publisher bridgev2.BeeperStreamPublisher, roomID id.RoomID, run *aistream.Run, cursor *streamPublishCursor, setup func(context.Context) (id.EventID, func(), error), onSecondVisibleChunk ...func()) *ai.AssistantMessageEventStream {
	streamCtx, cancelStream := context.WithCancel(ctx)
	upstream := ai.StreamSimple(streamCtx, model, llmContext, options)
	downstream := ai.NewAssistantMessageEventStream()
	writer := aistream.NewWriter(run, time.Now)
	if cursor == nil {
		cursor = &streamPublishCursor{nextSeq: 1}
	}
	visibleChunks := 0
	secondVisibleSent := false
	maybeSecondVisibleChunk := func(evt ai.AssistantMessageEvent) {
		if evt.Type != "text_delta" || evt.Delta == "" || secondVisibleSent {
			return
		}
		visibleChunks++
		if visibleChunks < 2 {
			return
		}
		secondVisibleSent = true
		for _, cb := range onSecondVisibleChunk {
			if cb != nil {
				cb()
			}
		}
	}
	go func() {
		defer cancelStream()
		defer downstream.End()
		eventID, onEnd, setupErr := setup(ctx)
		if onEnd != nil {
			defer onEnd()
		}
		if setupErr != nil {
			downstream.Push(ai.AssistantMessageEvent{
				Type: "error",
				Error: &ai.Message{
					Role:         "assistant",
					ErrorMessage: setupErr.Error(),
					StopReason:   ai.StopReasonError,
				},
			})
			return
		}
		streamSources := newSourceCollector()
		cursor.mu.Lock()
		if !cursor.started {
			enrichAIRunMetadata(run, model, options)
			writer.Start()
			cursor.started = true
		}
		startErr := cl.publishNewStreamEvents(ctx, publisher, roomID, eventID, run, cursor)
		cursor.mu.Unlock()
		if startErr != nil {
			cl.logStreamError(startErr, roomID, eventID, run, "Failed to publish AI stream start carrier")
			downstream.Push(ai.AssistantMessageEvent{
				Type: "error",
				Error: &ai.Message{
					Role:         "assistant",
					ErrorMessage: startErr.Error(),
					StopReason:   ai.StopReasonError,
				},
			})
			return
		}
		seenFirstDelta := false
		idleTimer := time.NewTimer(activeStreamIdleTimeout)
		defer idleTimer.Stop()
		for {
			select {
			case <-idleTimer.C:
				err := fmt.Errorf("AI stream timed out after %s without updates", activeStreamIdleTimeout)
				cursor.mu.Lock()
				writer.Error(err.Error())
				if publishErr := cl.publishNewStreamEvents(ctx, publisher, roomID, eventID, run, cursor); publishErr != nil {
					cl.logStreamError(publishErr, roomID, eventID, run, "Failed to publish AI stream timeout carrier")
				}
				cursor.mu.Unlock()
				downstream.Push(ai.AssistantMessageEvent{
					Type: "error",
					Error: &ai.Message{
						Role:         "assistant",
						ErrorMessage: err.Error(),
						StopReason:   ai.StopReasonError,
					},
				})
				cl.logStreamError(err, roomID, eventID, run, "AI stream timed out")
				return
			case evt, ok := <-upstream.Events():
				if !ok {
					cursor.mu.Lock()
					publishedEvents := cursor.published
					nextSeq := cursor.nextSeq
					cursor.mu.Unlock()
					cl.logStreamDebug(ctx, roomID, eventID, run, "Finished AI stream publishing", func(logEvt *zerolog.Event) {
						logEvt.Int("published_agui_events", publishedEvents).
							Int("next_seq", nextSeq)
					})
					return
				}
				if !idleTimer.Stop() {
					select {
					case <-idleTimer.C:
					default:
					}
				}
				idleTimer.Reset(activeStreamIdleTimeout)

				cursor.mu.Lock()
				beforeEvents := len(run.Events)
				applyAIStreamEvent(writer, evt, model.ContextWindow)
				if evt.Partial != nil {
					for _, source := range streamSources.addProviderSources(*evt.Partial) {
						writer.Custom("com.beeper.source", source)
					}
				}
				if evt.Type == "toolresult" && evt.ToolCall != nil && !hasPriorToolResult(run.Events, beforeEvents, evt.ToolCall.ID) {
					output := toolOutputEvent{ID: evt.ToolCall.ID, Name: evt.ToolCall.Name, Input: evt.ToolCall.Arguments}
					for _, source := range streamSources.addToolOutput(output, evt.CustomValue) {
						writer.Custom("com.beeper.source", source)
					}
				}
				afterEvents := len(run.Events)
				maybeSecondVisibleChunk(evt)
				if !seenFirstDelta && isVisibleAIStreamDelta(evt) {
					seenFirstDelta = true
					cl.logStreamDebug(ctx, roomID, eventID, run, "Received first AI stream delta", func(logEvt *zerolog.Event) {
						logEvt.Str("upstream_event_type", evt.Type).
							Int("content_index", evt.ContentIndex).
							Int("delta_bytes", len(evt.Delta)).
							Int("agui_events_added", afterEvents-beforeEvents)
					})
				}
				if afterEvents > beforeEvents {
					cl.logStreamDebug(ctx, roomID, eventID, run, "Transformed AI stream event to AG-UI", func(logEvt *zerolog.Event) {
						logEvt.Str("upstream_event_type", evt.Type).
							Int("content_index", evt.ContentIndex).
							Int("agui_events_added", afterEvents-beforeEvents).
							Int("pending_agui_events", afterEvents-cursor.published)
					})
				}
				if err := cl.publishNewStreamEvents(ctx, publisher, roomID, eventID, run, cursor); err != nil {
					cursor.mu.Unlock()
					downstream.Push(ai.AssistantMessageEvent{
						Type: "error",
						Error: &ai.Message{
							Role:         "assistant",
							ErrorMessage: err.Error(),
							StopReason:   ai.StopReasonError,
						},
					})
					cl.logStreamError(err, roomID, eventID, run, "Failed to publish AI stream carrier")
					return
				}
				cursor.mu.Unlock()
				downstream.Push(evt)
			}
		}
	}()
	return downstream
}

func (cl *Client) streamPublisherWithEnd(publisher bridgev2.BeeperStreamPublisher, roomID id.RoomID, eventID id.EventID, run *aistream.Run, onEnd func(), onSecondVisibleChunk ...func()) agent.StreamFn {
	return cl.streamPublisherWithEndFrom(publisher, roomID, eventID, run, &streamPublishCursor{nextSeq: 1}, onEnd, onSecondVisibleChunk...)
}

func (cl *Client) streamPublisherWithEndFrom(publisher bridgev2.BeeperStreamPublisher, roomID id.RoomID, eventID id.EventID, run *aistream.Run, cursor *streamPublishCursor, onEnd func(), onSecondVisibleChunk ...func()) agent.StreamFn {
	return func(ctx context.Context, model ai.Model, llmContext ai.Context, options ai.SimpleStreamOptions) *ai.AssistantMessageEventStream {
		return cl.streamPublisherWithAnchorSetup(ctx, model, llmContext, options, publisher, roomID, run, cursor, func(context.Context) (id.EventID, func(), error) {
			return eventID, onEnd, nil
		}, onSecondVisibleChunk...)
	}
}

func (cl *Client) publishNewStreamEvents(ctx context.Context, publisher bridgev2.BeeperStreamPublisher, roomID id.RoomID, eventID id.EventID, run *aistream.Run, cursor *streamPublishCursor) error {
	if run == nil || cursor == nil || cursor.published >= len(run.Events) {
		return nil
	}
	if cursor.nextSeq <= 0 {
		cursor.nextSeq = 1
	}
	partial := *run
	partial.Events = append([]agui.Event(nil), run.Events[cursor.published:]...)
	carriers, err := aistream.PackRunFromSeq(partial, cursor.nextSeq)
	if err != nil {
		return err
	}
	for _, carrier := range carriers {
		if err := publisher.Publish(suppressStreamCarrierRequestLogs(ctx), roomID, eventID, aistream.CarrierContent(partial, carrier.Envelopes)); err != nil {
			return err
		}
		cl.logStreamDebug(ctx, roomID, eventID, run, "Published AI stream carrier", func(logEvt *zerolog.Event) {
			logEvt.Int("envelope_count", len(carrier.Envelopes)).
				Int("seq_start", firstCarrierSeq(carrier)).
				Int("seq_end", lastCarrierSeq(carrier)).
				Strs("agui_event_types", carrierEventTypes(carrier)).
				Str("payload_key", aistream.BeeperAIKey)
		})
	}
	cursor.nextSeq = aistream.NextSeq(carriers)
	cursor.published = len(run.Events)
	if cursor.persist != nil && shouldPersistStreamRun(run, cursor) {
		cursor.lastPersisted = len(run.Events)
		cursor.lastPersistedAt = time.Now()
		return cursor.persist(ctx, run)
	}
	return nil
}

func shouldPersistStreamRun(run *aistream.Run, cursor *streamPublishCursor) bool {
	if run == nil || cursor == nil {
		return false
	}
	switch run.Status.State {
	case "complete", "aborted", "error":
		return true
	case "interrupted":
		return true
	}
	if cursor.lastPersisted == 0 {
		return true
	}
	if len(run.Events)-cursor.lastPersisted >= 25 {
		return true
	}
	return time.Since(cursor.lastPersistedAt) >= 2*time.Second
}

func (cl *Client) persistActiveStream(ctx context.Context, portal *bridgev2.Portal, providerID string, modelID string, active *activeAIRun, stream *assistantStreamState, run *aistream.Run) error {
	if cl == nil || cl.Main == nil || cl.Main.Store == nil || cl.UserLogin == nil || portal == nil || stream == nil || run == nil {
		return nil
	}
	active.mu.Lock()
	entryID := stream.entryID
	var metadata aiid.MessageMetadata
	if stream.metadata != nil {
		metadata = *stream.metadata
	}
	var statusInfo bridgev2.MessageStatusEventInfo
	if active.status != nil {
		statusInfo = *active.status
	}
	active.mu.Unlock()
	return cl.Main.Store.UpsertActiveStream(ctx, aidb.ActiveStreamRecord{
		RunID:      stream.runID,
		LoginID:    cl.UserLogin.ID,
		PortalKey:  portal.PortalKey,
		RoomID:     portal.MXID,
		EventID:    stream.eventID,
		MessageID:  stream.messageID,
		ProviderID: providerID,
		ModelID:    modelID,
		EntryID:    entryID,
		Run:        *run,
		Metadata:   metadata,
		StatusInfo: statusInfo,
		CreatedAt:  stream.anchorTimestamp,
		UpdatedAt:  time.Now(),
	})
}

func (cl *Client) deleteActiveStream(ctx context.Context, runID string) {
	if cl == nil || cl.Main == nil || cl.Main.Store == nil || cl.UserLogin == nil || runID == "" {
		return
	}
	if err := cl.Main.Store.DeleteActiveStream(ctx, cl.UserLogin.ID, runID); err != nil && cl.Main.Bridge != nil {
		cl.Main.Bridge.Log.Warn().Err(err).Str("run_id", runID).Msg("Failed to delete AI active stream")
	}
}

func suppressStreamCarrierRequestLogs(ctx context.Context) context.Context {
	log := zerolog.Ctx(ctx)
	level := log.GetLevel()
	if level >= zerolog.FatalLevel && level != zerolog.Disabled {
		return ctx
	}
	return log.Level(zerolog.FatalLevel).WithContext(ctx)
}

func (cl *Client) logMatrixMessageError(msg *bridgev2.MatrixMessage, err error, message string) {
	if cl == nil || cl.Main == nil || cl.Main.Bridge == nil {
		return
	}
	event := cl.Main.Bridge.Log.Error().
		Err(err).
		Str("action", "ai_matrix_message")
	if msg != nil {
		if msg.Portal != nil {
			event = event.
				Str("portal_id", string(msg.Portal.ID)).
				Str("portal_receiver", string(msg.Portal.Receiver)).
				Str("portal_mxid", string(msg.Portal.MXID))
		}
		if msg.Event != nil {
			event = event.
				Str("event_id", string(msg.Event.ID)).
				Str("event_type", string(msg.Event.Type.Type)).
				Str("sender", string(msg.Event.Sender))
		}
	}
	if cl.UserLogin != nil {
		event = event.Str("login_id", string(cl.UserLogin.ID))
	}
	event.Msg(message)
}

func (cl *Client) queueAssistantMediaMessages(portalKey networkid.PortalKey, anchorMessageID networkid.MessageID, providerID string, modelID string, runID string, message ai.Message) {
	if cl == nil || cl.UserLogin == nil {
		return
	}
	replyTo := assistantAnchorReplyTarget(anchorMessageID)
	mediaIndex := 0
	for _, block := range aiContentBlocks(message.Content) {
		if block.Type != "image" || block.Data == "" {
			continue
		}
		if runID == "" {
			runID = session.CreateSessionID()
		}
		messageID := networkid.MessageID(fmt.Sprintf("assistant:%s:image:%d", runID, mediaIndex))
		partID := aiid.PartID(fmt.Sprintf("image-%d", mediaIndex))
		metadata := &aiid.MessageMetadata{
			Role:         "assistant",
			ProviderID:   providerID,
			ModelID:      modelID,
			RunID:        runID,
			StreamStatus: "done",
			StopReason:   string(message.StopReason),
		}
		block := block
		cl.UserLogin.QueueRemoteEvent(&simplevent.Message[ai.ContentBlock]{
			EventMeta: simplevent.EventMeta{
				Type:      bridgev2.RemoteEventMessage,
				PortalKey: portalKey,
				Sender: bridgev2.EventSender{
					Sender: aiid.AssistantUserID(),
				},
				Timestamp: time.Now(),
			},
			ID:   messageID,
			Data: block,
			ConvertMessageFunc: func(ctx context.Context, portal *bridgev2.Portal, intent bridgev2.MatrixAPI, data ai.ContentBlock) (*bridgev2.ConvertedMessage, error) {
				return assistantImageConvertedMessage(ctx, portal, intent, data, partID, metadata, replyTo)
			},
		})
		mediaIndex++
	}
}

func assistantAnchorReplyTarget(messageID networkid.MessageID) *networkid.MessageOptionalPartID {
	if messageID == "" {
		return nil
	}
	partID := aiid.PartID("text")
	return &networkid.MessageOptionalPartID{MessageID: messageID, PartID: &partID}
}

type matrixMediaUploader interface {
	UploadMedia(ctx context.Context, roomID id.RoomID, data []byte, fileName, mimeType string) (url id.ContentURIString, file *event.EncryptedFileInfo, err error)
}

func assistantImageConvertedMessage(ctx context.Context, portal *bridgev2.Portal, intent matrixMediaUploader, block ai.ContentBlock, partID networkid.PartID, metadata *aiid.MessageMetadata, replyTo *networkid.MessageOptionalPartID) (*bridgev2.ConvertedMessage, error) {
	if portal == nil || portal.Portal == nil {
		return nil, fmt.Errorf("missing portal for assistant image")
	}
	data, mimeType, err := decodeContentBlockDataWithMIME(block)
	if err != nil {
		return nil, err
	}
	if mimeType == "" {
		mimeType = http.DetectContentType(data)
	}
	fileName := strings.TrimSpace(block.Name)
	if fileName == "" {
		fileName = fileNameForImageMIME(mimeType)
	}
	uri, file, err := intent.UploadMedia(ctx, portal.MXID, data, fileName, mimeType)
	if err != nil {
		return nil, fmt.Errorf("failed to upload assistant image: %w", err)
	}
	info := &event.FileInfo{
		MimeType: mimeType,
		Size:     len(data),
	}
	if width, height := imageSize(data); width > 0 && height > 0 {
		info.Width = width
		info.Height = height
	}
	content := &event.MessageEventContent{
		MsgType:  event.MsgImage,
		Body:     fileName,
		FileName: fileName,
		Info:     info,
		Mentions: &event.Mentions{},
	}
	if file != nil {
		content.File = file
	} else {
		content.URL = uri
	}
	return &bridgev2.ConvertedMessage{ReplyTo: replyTo, Parts: []*bridgev2.ConvertedMessagePart{{
		ID:         partID,
		Type:       event.EventMessage,
		Content:    content,
		DBMetadata: metadata,
	}}}, nil
}

func imageSize(data []byte) (int, int) {
	config, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return 0, 0
	}
	return config.Width, config.Height
}

func fileNameForImageMIME(mimeType string) string {
	switch strings.ToLower(strings.TrimSpace(strings.Split(mimeType, ";")[0])) {
	case "image/jpeg":
		return "image.jpg"
	case "image/gif":
		return "image.gif"
	case "image/webp":
		return "image.webp"
	default:
		return "image.png"
	}
}

func (cl *Client) logStreamError(err error, roomID id.RoomID, eventID id.EventID, run *aistream.Run, message string) {
	if cl == nil || cl.Main == nil || cl.Main.Bridge == nil {
		return
	}
	event := cl.Main.Bridge.Log.Error().
		Err(err).
		Str("action", "ai_stream").
		Str("room_id", string(roomID)).
		Str("event_id", string(eventID))
	if run != nil {
		event = event.
			Str("run_id", run.RunID).
			Str("thread_id", run.ThreadID).
			Str("message_id", run.MessageID).
			Str("model", run.Model)
	}
	if cl.UserLogin != nil {
		event = event.Str("login_id", string(cl.UserLogin.ID))
	}
	event.Msg(message)
}

func (cl *Client) logStreamDebug(ctx context.Context, roomID id.RoomID, eventID id.EventID, run *aistream.Run, message string, fields ...func(*zerolog.Event)) {
	if cl == nil || cl.Main == nil || cl.Main.Bridge == nil {
		return
	}
	event := cl.Main.Bridge.Log.Debug().
		Str("action", "ai_stream").
		Str("room_id", string(roomID)).
		Str("event_id", string(eventID))
	if run != nil {
		event = event.
			Str("run_id", run.RunID).
			Str("thread_id", run.ThreadID).
			Str("message_id", run.MessageID).
			Str("model", run.Model)
	}
	if cl.UserLogin != nil {
		event = event.Str("login_id", string(cl.UserLogin.ID))
	}
	for _, field := range fields {
		if field != nil {
			field(event)
		}
	}
	event.Ctx(ctx).Msg(message)
}

func isVisibleAIStreamDelta(evt ai.AssistantMessageEvent) bool {
	switch evt.Type {
	case "text_delta", "thinking_delta", "toolcall_delta":
		return evt.Delta != ""
	default:
		return false
	}
}

func firstCarrierSeq(carrier aistream.Carrier) int {
	if len(carrier.Envelopes) == 0 {
		return 0
	}
	return carrier.Envelopes[0].Seq
}

func lastCarrierSeq(carrier aistream.Carrier) int {
	if len(carrier.Envelopes) == 0 {
		return 0
	}
	return carrier.Envelopes[len(carrier.Envelopes)-1].Seq
}

func carrierEventTypes(carrier aistream.Carrier) []string {
	types := make([]string, 0, len(carrier.Envelopes))
	for _, envelope := range carrier.Envelopes {
		types = append(types, string(envelope.Event.Type()))
	}
	return types
}

func applyAIStreamEvent(writer *aistream.Writer, evt ai.AssistantMessageEvent, contextLimit ...int) {
	if writer == nil {
		return
	}
	toolCallFromEvent := func() *ai.ToolCall {
		if evt.ToolCall != nil {
			return evt.ToolCall
		}
		if evt.Partial == nil || evt.ContentIndex < 0 {
			return nil
		}
		blocks := aiContentBlocks(evt.Partial.Content)
		if evt.ContentIndex >= len(blocks) {
			return nil
		}
		block := blocks[evt.ContentIndex]
		if block.Type != "toolCall" || block.ID == "" {
			return nil
		}
		return &ai.ToolCall{Type: "toolCall", ID: block.ID, Name: block.Name, Arguments: block.Arguments, ThoughtSignature: block.ThoughtSignature}
	}
	switch evt.Type {
	case "text_start":
		writer.TextStart(evt.ContentIndex)
	case "text_delta":
		writer.TextDelta(evt.ContentIndex, evt.Delta)
	case "text_end":
		writer.TextEnd(evt.ContentIndex)
	case "thinking_start":
		writer.ReasoningMessageStart(evt.ContentIndex)
	case "thinking_delta":
		writer.ReasoningDelta(evt.ContentIndex, evt.Delta)
	case "thinking_end":
		writer.ReasoningContentSnapshot(evt.ContentIndex, evt.Content)
		writer.ReasoningMessageEnd(evt.ContentIndex)
	case "toolcall_start":
		if toolCall := toolCallFromEvent(); toolCall != nil {
			writer.ToolStart(toolCall.ID, toolCall.Name, evt.ContentIndex, nil)
		}
	case "toolcall_delta":
		if toolCall := toolCallFromEvent(); toolCall != nil {
			writer.ToolArgs(toolCall.ID, evt.Delta, toolCall.Arguments)
		}
	case "toolcall_end":
		if toolCall := toolCallFromEvent(); toolCall != nil {
			writer.ToolInputComplete(toolCall.ID, toolCall.Name, toolCall.Arguments)
		}
	case "toolresult":
		if toolCall := toolCallFromEvent(); toolCall != nil {
			result := evt.CustomValue
			if result == nil {
				result = map[string]any{"state": agui.ToolResultStateComplete, "status": "success"}
			}
			writer.ToolEnd(toolCall.ID, toolCall.Name, toolCall.Arguments, result)
		}
	case "custom":
		if evt.CustomName != "" {
			writer.Custom(evt.CustomName, evt.CustomValue)
		}
	case "done":
		if evt.Message != nil {
			writeFinalTextFallback(writer, *evt.Message)
			usage := aguiUsage(evt.Message.Usage, contextLimit...)
			if evt.Reason == ai.StopReasonToolUse {
				writer.AwaitToolUseWithUsage(&usage)
			} else {
				writer.FinishWithUsage(aguiFinishReasonFromAI(evt.Reason), &usage)
			}
		} else if evt.Reason == ai.StopReasonToolUse {
			writer.AwaitToolUseWithUsage(nil)
		} else {
			writer.Finish(aguiFinishReasonFromAI(evt.Reason))
		}
	case "error":
		message := "stream error"
		if evt.Error != nil && evt.Error.ErrorMessage != "" {
			message = evt.Error.ErrorMessage
		}
		writer.Error(message)
	}
}

func writeFinalTextFallback(writer *aistream.Writer, message ai.Message) {
	if writer == nil || writer.Run == nil || writer.HasTextContent() {
		return
	}
	if text := msgconv.AssistantText(message); text != "" {
		writer.Text(text)
		writer.TextEnd(0)
	}
}

type toolOutputEvent struct {
	ID      string
	Name    string
	Input   any
	Result  agent.AgentToolResult[any]
	IsError bool
}

func hasPriorToolResult(events []agui.Event, before int, toolCallID string) bool {
	if toolCallID == "" {
		return false
	}
	if before > len(events) {
		before = len(events)
	}
	for _, evt := range events[:before] {
		if evt.Type() == agui.EventToolCallResult && evt.Get("toolCallId") == toolCallID {
			return true
		}
	}
	return false
}

func appendToolOutputs(run *aistream.Run, outputs []toolOutputEvent, messages ...ai.Message) {
	if run == nil {
		return
	}
	writer := aistream.NewWriter(run, time.Now)
	sources := newSourceCollector()
	for _, output := range outputs {
		structuredOutput := toolOutput(output.Result, output.IsError)
		writer.ToolEnd(output.ID, output.Name, output.Input, structuredOutput)
		sources.addToolOutput(output, structuredOutput)
	}
	for _, message := range messages {
		sources.addProviderSources(message)
		sources.addAnswerURLSources(message)
	}
	for _, source := range sources.sources() {
		writer.Custom("com.beeper.source", source)
	}
}

func toolOutput(result agent.AgentToolResult[any], isError bool) any {
	output := mapFromAny(result.Details)
	if output == nil {
		if text := textFromBlocks(result.Content); text != "" {
			output = map[string]any{"content": text}
		} else {
			output = map[string]any{}
		}
	}
	if isError {
		output["state"] = agui.ToolResultStateError
		output["status"] = "failed"
	} else {
		output["state"] = agui.ToolResultStateComplete
		output["status"] = "success"
	}
	return output
}

func assistantMessageHasToolCalls(message ai.Message) bool {
	if message.StopReason == ai.StopReasonToolUse {
		return true
	}
	for _, block := range aiContentBlocks(message.Content) {
		if block.Type == "toolCall" {
			return true
		}
	}
	return false
}

func aiContentBlocks(content any) []ai.ContentBlock {
	switch value := content.(type) {
	case []ai.ContentBlock:
		return value
	case []any:
		blocks := make([]ai.ContentBlock, 0, len(value))
		for _, item := range value {
			raw, _ := json.Marshal(item)
			var block ai.ContentBlock
			if json.Unmarshal(raw, &block) == nil {
				blocks = append(blocks, block)
			}
		}
		return blocks
	default:
		raw, _ := json.Marshal(value)
		var blocks []ai.ContentBlock
		_ = json.Unmarshal(raw, &blocks)
		return blocks
	}
}

func mapFromAny(value any) map[string]any {
	if value == nil {
		return nil
	}
	if source, ok := value.(map[string]any); ok {
		clone := map[string]any{}
		for key, item := range source {
			clone[key] = item
		}
		return clone
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return map[string]any{"result": value}
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err == nil && out != nil {
		return out
	}
	var decoded any
	if err := json.Unmarshal(raw, &decoded); err == nil {
		return map[string]any{"result": decoded}
	}
	return map[string]any{"result": value}
}

func textFromBlocks(blocks []ai.ContentBlock) string {
	var out string
	for _, block := range blocks {
		if block.Type == "text" {
			out += block.Text
		}
	}
	return out
}

func aguiUsage(usage ai.Usage, contextLimit ...int) agui.Usage {
	total := usage.TotalTokens
	if total == 0 {
		total = usage.Input + usage.Output
	}
	limit := 0
	for _, value := range contextLimit {
		if value > limit {
			limit = value
		}
	}
	return agui.Usage{
		PromptTokens:     usage.Input,
		CompletionTokens: usage.Output,
		ReasoningTokens:  usage.ReasoningTokens,
		TotalTokens:      total,
		ContextLimit:     limit,
	}
}

func enrichAIRunMetadata(run *aistream.Run, model ai.Model, options ai.SimpleStreamOptions) {
	if run == nil {
		return
	}
	if run.Data == nil {
		run.Data = map[string]any{}
	}
	if options.Reasoning != nil && *options.Reasoning != "" {
		run.Data["reasoning"] = string(*options.Reasoning)
	}
	if model.ContextWindow > 0 {
		run.Data["contextLimit"] = model.ContextWindow
	}
}

func (cl *Client) sessionForPortal(ctx context.Context, portal *bridgev2.Portal, meta *aiid.PortalMetadata) (*session.Session, error) {
	if meta == nil {
		meta = &aiid.PortalMetadata{}
		portal.Metadata = meta
	}
	if meta.SessionID != "" {
		agentSession, err := cl.Main.Store.OpenSession(ctx, cl.UserLogin.ID, session.SQLiteSessionMetadata{
			SessionMetadata: session.SessionMetadata{ID: meta.SessionID},
		})
		if err == nil {
			_ = portal.Save(ctx)
			return agentSession, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
	}
	agentSession, err := cl.Main.Store.CreateSession(ctx, cl.UserLogin.ID, session.SQLiteSessionCreateOptions{})
	if err != nil {
		return nil, err
	}
	sessionMeta, err := agentSession.GetMetadata(ctx)
	if err != nil {
		return nil, err
	}
	meta.SessionID = sessionMeta.ID
	if err := portal.Save(ctx); err != nil {
		return nil, err
	}
	return agentSession, nil
}

func portalMetadata(portal *bridgev2.Portal) *aiid.PortalMetadata {
	if portal == nil {
		return &aiid.PortalMetadata{}
	}
	if meta, ok := portal.Metadata.(*aiid.PortalMetadata); ok && meta != nil {
		return meta
	}
	meta := &aiid.PortalMetadata{}
	portal.Metadata = meta
	return meta
}

func matrixEventTime(evt *event.Event) time.Time {
	if evt == nil || evt.Timestamp == 0 {
		return time.Now()
	}
	return time.UnixMilli(evt.Timestamp)
}

func afterMatrixTimestamp(base time.Time) time.Time {
	if base.IsZero() {
		return time.Now()
	}
	min := base.Add(time.Millisecond)
	if now := time.Now(); now.After(min) {
		return now
	}
	return min
}

func (cl *Client) setActiveHarness(key networkid.PortalKey, h *harness.AgentHarness) {
	cl.activeMu.Lock()
	defer cl.activeMu.Unlock()
	if cl.activeHarnesses == nil {
		cl.activeHarnesses = map[networkid.PortalKey]*harness.AgentHarness{}
	}
	cl.activeHarnesses[key] = h
}

func (cl *Client) getActiveHarness(key networkid.PortalKey) *harness.AgentHarness {
	cl.activeMu.Lock()
	defer cl.activeMu.Unlock()
	if cl.activeHarnesses == nil {
		return nil
	}
	return cl.activeHarnesses[key]
}

func (cl *Client) clearActiveHarness(key networkid.PortalKey, h *harness.AgentHarness) {
	cl.activeMu.Lock()
	defer cl.activeMu.Unlock()
	if cl.activeHarnesses != nil && cl.activeHarnesses[key] == h {
		delete(cl.activeHarnesses, key)
	}
}

func (cl *Client) setActiveRun(key networkid.PortalKey, run *activeAIRun) {
	cl.activeMu.Lock()
	defer cl.activeMu.Unlock()
	if cl.activeRuns == nil {
		cl.activeRuns = map[networkid.PortalKey]*activeAIRun{}
	}
	cl.activeRuns[key] = run
}

func (cl *Client) getActiveRun(key networkid.PortalKey) *activeAIRun {
	cl.activeMu.Lock()
	defer cl.activeMu.Unlock()
	if cl.activeRuns == nil {
		return nil
	}
	return cl.activeRuns[key]
}

func (cl *Client) clearActiveRun(key networkid.PortalKey, run *activeAIRun) {
	cl.activeMu.Lock()
	defer cl.activeMu.Unlock()
	if cl.activeRuns != nil && cl.activeRuns[key] == run {
		delete(cl.activeRuns, key)
	}
}

func (cl *Client) startActiveStreamJanitor(ctx context.Context) {
	if cl == nil || cl.Main == nil || cl.Main.Store == nil || cl.UserLogin == nil {
		return
	}
	cl.activeStreamJanitorMu.Lock()
	defer cl.activeStreamJanitorMu.Unlock()
	if cl.activeStreamJanitorStop != nil {
		return
	}
	janitorCtx, stop := context.WithCancel(ctx)
	cl.activeStreamJanitorStop = stop
	go cl.runActiveStreamJanitor(janitorCtx)
}

func (cl *Client) stopActiveStreamJanitor() {
	cl.activeStreamJanitorMu.Lock()
	stop := cl.activeStreamJanitorStop
	cl.activeStreamJanitorStop = nil
	cl.activeStreamJanitorMu.Unlock()
	if stop != nil {
		stop()
	}
}

func (cl *Client) runActiveStreamJanitor(ctx context.Context) {
	cl.failStaleActiveStreams(ctx)
	ticker := time.NewTicker(activeStreamJanitorInterval())
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			cl.failStaleActiveStreams(ctx)
		}
	}
}

func activeStreamJanitorInterval() time.Duration {
	if activeStreamIdleTimeout <= 0 {
		return time.Minute
	}
	interval := activeStreamIdleTimeout / 5
	if interval <= 0 {
		return activeStreamIdleTimeout
	}
	if interval < time.Second {
		return time.Second
	}
	return interval
}

func (cl *Client) failStaleActiveStreams(ctx context.Context) {
	if cl == nil || cl.Main == nil || cl.Main.Store == nil || cl.UserLogin == nil {
		return
	}
	cutoff := time.Now().Add(-activeStreamIdleTimeout)
	records, err := cl.Main.Store.ListStaleActiveStreams(ctx, cl.UserLogin.ID, cutoff)
	if err != nil {
		if cl.Main.Bridge != nil {
			cl.Main.Bridge.Log.Warn().Err(err).Msg("Failed to load stale AI streams")
		}
		return
	}
	for _, record := range records {
		if active := cl.getActiveRun(record.PortalKey); active != nil && active.hasAssistantRun(record.RunID) {
			continue
		}
		cl.finishActiveStreamRecord(ctx, record, fmt.Errorf("AI stream timed out after %s without updates", activeStreamIdleTimeout))
	}
}

func (cl *Client) failPersistedActiveStreams(ctx context.Context) {
	if cl == nil || cl.Main == nil || cl.Main.Store == nil || cl.UserLogin == nil {
		return
	}
	records, err := cl.Main.Store.ListActiveStreams(ctx, cl.UserLogin.ID)
	if err != nil {
		if cl.Main.Bridge != nil {
			cl.Main.Bridge.Log.Warn().Err(err).Msg("Failed to load persisted AI streams")
		}
		return
	}
	for _, record := range records {
		if active := cl.getActiveRun(record.PortalKey); active != nil && active.hasAssistantRun(record.RunID) {
			continue
		}
		cl.finishActiveStreamRecord(ctx, record, fmt.Errorf("AI stream was interrupted before completion"))
	}
}

func (cl *Client) finishActiveStreamRecord(ctx context.Context, record aidb.ActiveStreamRecord, err error) {
	if cl == nil || cl.UserLogin == nil || cl.Main == nil || cl.Main.Store == nil {
		return
	}
	if err := cl.Main.Store.DeleteActiveStream(ctx, cl.UserLogin.ID, record.RunID); err != nil {
		if cl.Main.Bridge != nil {
			cl.Main.Bridge.Log.Warn().Err(err).Str("run_id", record.RunID).Msg("Failed to delete stale AI stream")
		}
		return
	}
	switch record.Run.Status.State {
	case "complete", "aborted", "error":
		cl.queueTerminalActiveStreamFinal(ctx, record)
	default:
		cl.finalizeInterruptedSessionTurn(ctx, record, err)
		cl.queueAssistantRunError(record.PortalKey, record.MessageID, record.ProviderID, record.ModelID, record.RunID, record.Run, &record.Metadata, afterMatrixTimestamp(record.CreatedAt), err)
		cl.sendActiveStreamRetriableStatus(ctx, record, err)
	}
}

func (cl *Client) queueTerminalActiveStreamFinal(ctx context.Context, record aidb.ActiveStreamRecord) {
	metadata := record.Metadata
	metadata.Role = "assistant"
	metadata.RunID = record.RunID
	metadata.ProviderID = record.ProviderID
	metadata.ModelID = record.ModelID
	metadata.StreamStatus = "done"
	if metadata.StopReason == "" {
		metadata.StopReason = record.Run.Status.FinishReason
	}
	if record.Run.Status.State == "error" || record.Run.Status.State == "aborted" {
		metadata.ErrorMessage = aistream.ErrorVisibleText(record.Run.Status.Error)
		if metadata.StopReason == "" {
			metadata.StopReason = string(ai.StopReasonError)
		}
	}
	cl.UserLogin.QueueRemoteEvent(cl.assistantFinalEditWithProjection(record.PortalKey, record.MessageID, record.ProviderID, record.ModelID, record.Run, &metadata, afterMatrixTimestamp(record.CreatedAt)))
}

func (cl *Client) finalizeInterruptedSessionTurn(ctx context.Context, record aidb.ActiveStreamRecord, cause error) {
	if cl == nil || cl.Main == nil || cl.Main.Store == nil || cl.UserLogin == nil || record.Run.ThreadID == "" || cause == nil {
		return
	}
	agentSession, err := cl.Main.Store.OpenSession(ctx, cl.UserLogin.ID, session.SQLiteSessionMetadata{
		SessionMetadata: session.SessionMetadata{ID: record.Run.ThreadID},
	})
	if err != nil {
		if cl.Main.Bridge != nil {
			cl.Main.Bridge.Log.Warn().Err(err).Str("session_id", record.Run.ThreadID).Str("run_id", record.RunID).Msg("Failed to open interrupted AI session")
		}
		return
	}
	defer agentSession.Close()

	existingToolResults, ok := sessionBranchCanAppendRecovery(ctx, agentSession, record.EntryID, cause.Error())
	if !ok {
		if cl.Main.Bridge != nil {
			cl.Main.Bridge.Log.Warn().Str("session_id", record.Run.ThreadID).Str("run_id", record.RunID).Str("entry_id", record.EntryID).Msg("Skipping interrupted AI session recovery because the branch moved")
		}
		return
	}

	now := time.Now().UnixMilli()
	for _, message := range interruptedToolResultMessages(record.Run, cause, existingToolResults, now) {
		if _, err := agentSession.AppendMessage(ctx, message); err != nil {
			if cl.Main.Bridge != nil {
				cl.Main.Bridge.Log.Warn().Err(err).Str("session_id", record.Run.ThreadID).Str("run_id", record.RunID).Msg("Failed to append interrupted AI tool result")
			}
			return
		}
	}
	assistantMessage := ai.Message{
		Role:         "assistant",
		Content:      []ai.ContentBlock{{Type: "text", Text: cause.Error()}},
		Provider:     ai.Provider(record.ProviderID),
		Model:        record.ModelID,
		StopReason:   ai.StopReasonError,
		ErrorMessage: cause.Error(),
		Timestamp:    now,
	}
	if _, err := agentSession.AppendMessage(ctx, assistantMessage); err != nil && cl.Main.Bridge != nil {
		cl.Main.Bridge.Log.Warn().Err(err).Str("session_id", record.Run.ThreadID).Str("run_id", record.RunID).Msg("Failed to append interrupted AI assistant error")
	}
}

func sessionBranchCanAppendRecovery(ctx context.Context, agentSession *session.Session, assistantEntryID string, recoveryMessage string) (map[string]bool, bool) {
	existingToolResults := map[string]bool{}
	branch, err := agentSession.GetBranch(ctx, nil)
	if err != nil {
		return existingToolResults, false
	}
	found := assistantEntryID == ""
	for _, raw := range branch {
		var entry map[string]any
		if err := json.Unmarshal(raw, &entry); err != nil {
			return existingToolResults, false
		}
		if !found {
			if entry["id"] == assistantEntryID {
				found = true
			}
			continue
		}
		if entry["type"] != "message" {
			continue
		}
		message, ok := entry["message"].(map[string]any)
		if !ok {
			return existingToolResults, false
		}
		role, _ := message["role"].(string)
		if role == "assistant" && recoveryMessage != "" && stringFromAny(message["errorMessage"]) == recoveryMessage {
			return existingToolResults, false
		}
		if role != "toolResult" {
			if assistantEntryID == "" {
				continue
			}
			return existingToolResults, false
		}
		if toolCallID, _ := message["toolCallId"].(string); toolCallID != "" {
			existingToolResults[toolCallID] = true
		}
	}
	return existingToolResults, found
}

func interruptedToolResultMessages(run aistream.Run, cause error, existingToolResults map[string]bool, timestamp int64) []ai.Message {
	final := finalizedAssistantRun(run, ai.Message{
		Role:         "assistant",
		StopReason:   ai.StopReasonError,
		ErrorMessage: cause.Error(),
	}, 0)
	message := final.FinalBeeperAIMessage(0, true)
	results := make([]ai.Message, 0)
	for _, part := range message.Parts {
		if stringFromAny(part["type"]) != "tool-call" {
			continue
		}
		toolCallID := stringFromAny(part["toolCallId"])
		if toolCallID == "" || existingToolResults[toolCallID] {
			continue
		}
		output := mapFromAny(part["output"])
		if output == nil {
			output = map[string]any{
				"state":  agui.ToolResultStateError,
				"status": "failed",
				"reason": cause.Error(),
			}
		}
		reason := stringFromAny(output["reason"])
		if reason == "" {
			reason = cause.Error()
		}
		results = append(results, ai.Message{
			Role:       "toolResult",
			ToolCallID: toolCallID,
			ToolName:   stringFromAny(part["name"]),
			Content:    []ai.ContentBlock{{Type: "text", Text: reason}},
			Details:    output,
			IsError:    true,
			Timestamp:  timestamp,
		})
	}
	return results
}

func (cl *Client) sendActiveStreamRetriableStatus(ctx context.Context, record aidb.ActiveStreamRecord, err error) {
	if cl == nil || cl.Main == nil || cl.Main.Bridge == nil || cl.Main.Bridge.Matrix == nil {
		return
	}
	info := record.StatusInfo
	if info.RoomID == "" {
		info.RoomID = record.RoomID
	}
	if info.SourceEventID == "" && info.TransactionID == "" {
		return
	}
	status := bridgev2.MessageStatus{
		InternalError: err,
		Status:        event.MessageStatusRetriable,
		ErrorReason:   event.MessageStatusGenericError,
		Message:       "AI response was interrupted. Please retry.",
	}
	cl.Main.Bridge.Matrix.SendMessageStatus(ctx, &status, &info)
}

func (r *activeAIRun) addPending(pending *pendingAIMessage) {
	r.mu.Lock()
	defer r.mu.Unlock()
	pending.metadata.ProviderID = r.provider.ID
	pending.metadata.ModelID = r.model.ID
	pending.metadata.RunID = r.runID
	pendingTimestamp := time.Now()
	if pending != nil && pending.msg != nil {
		pendingTimestamp = matrixEventTime(pending.msg.Event)
	}
	if pendingTimestamp.After(r.lastConsumedTimestamp) {
		r.lastConsumedTimestamp = pendingTimestamp
	}
	r.pending = append(r.pending, pending)
}

func (r *activeAIRun) removePending(pending *pendingAIMessage) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i, candidate := range r.pending {
		if candidate == pending {
			r.pending = append(r.pending[:i], r.pending[i+1:]...)
			return
		}
	}
}

func (r *activeAIRun) matchesRedactionTarget(target *database.Message) bool {
	if r == nil || target == nil {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, pending := range r.pending {
		if pending != nil && networkid.MessageID("pending:"+string(pending.txnID)) == target.ID {
			return true
		}
	}
	for _, consumed := range r.consumed {
		if consumed != nil && consumed.metadata != nil && consumed.metadata.SessionEntryID != "" && aiid.UserMessageID(consumed.metadata.SessionEntryID) == target.ID {
			return true
		}
	}
	for _, stream := range r.streams {
		if stream != nil && stream.messageID == target.ID {
			return true
		}
	}
	return false
}

func (r *activeAIRun) replyTarget() *networkid.MessageOptionalPartID {
	r.mu.Lock()
	defer r.mu.Unlock()
	var messageID networkid.MessageID
	if r.last != nil {
		messageID = r.last.messageID
	} else if len(r.streams) > 0 {
		messageID = r.streams[len(r.streams)-1].messageID
	}
	if messageID == "" {
		return nil
	}
	partID := aiid.PartID("text")
	return &networkid.MessageOptionalPartID{
		MessageID: messageID,
		PartID:    &partID,
	}
}

func (w *consumedEchoWait) wait(ctx context.Context) error {
	if w == nil {
		return nil
	}
	select {
	case <-w.done:
		return w.err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (r *activeAIRun) markConsumed(ctx context.Context, cl *Client, entryID string) {
	wait := &consumedEchoWait{done: make(chan struct{})}
	r.mu.Lock()
	if len(r.pending) == 0 {
		r.mu.Unlock()
		return
	}
	pending := r.pending[0]
	r.pending = r.pending[1:]
	r.consumed = append(r.consumed, pending)
	r.echoWaits = append(r.echoWaits, wait)
	echoTimestamp := time.Now()
	if pending != nil && pending.msg != nil {
		echoTimestamp = matrixEventTime(pending.msg.Event)
	}
	if echoTimestamp.After(r.lastConsumedTimestamp) {
		r.lastConsumedTimestamp = echoTimestamp
	}
	if pending != nil && pending.msg != nil && pending.msg.Event != nil {
		r.status = bridgev2.StatusEventInfoFromEvent(pending.msg.Event)
	}
	r.mu.Unlock()
	go func() {
		wait.err = cl.queueConsumedUserEcho(ctx, pending, entryID, echoTimestamp)
		close(wait.done)
	}()
}

func (r *activeAIRun) waitForConsumedEchoes(ctx context.Context) error {
	r.mu.Lock()
	waits := append([]*consumedEchoWait(nil), r.echoWaits...)
	r.mu.Unlock()
	for _, wait := range waits {
		if err := wait.wait(ctx); err != nil {
			return err
		}
	}
	return nil
}

func (r *activeAIRun) failAll(ctx context.Context, cl *Client, err error) {
	r.mu.Lock()
	pending := append([]*pendingAIMessage(nil), r.pending...)
	r.pending = nil
	r.mu.Unlock()
	for _, msg := range pending {
		cl.markPendingFailed(ctx, msg, err)
	}
}

func (r *activeAIRun) failConsumed(ctx context.Context, cl *Client, err error) {
	r.mu.Lock()
	consumed := append([]*pendingAIMessage(nil), r.consumed...)
	r.consumed = nil
	r.mu.Unlock()
	for _, msg := range consumed {
		cl.markConsumedFailed(ctx, msg, err)
	}
}

func (r *activeAIRun) addAssistantStream(stream *assistantStreamState) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.streams = append(r.streams, stream)
}

func (r *activeAIRun) nextAssistantAnchorTimestamp() time.Time {
	r.mu.Lock()
	defer r.mu.Unlock()
	return afterMatrixTimestamp(r.lastConsumedTimestamp)
}

func (r *activeAIRun) hasAssistantRun(runID string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, stream := range r.streams {
		if stream != nil && stream.runID == runID {
			return true
		}
	}
	return r.last != nil && r.last.runID == runID
}

func (r *activeAIRun) currentAssistantStream() *assistantStreamState {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.streams) == 0 {
		return nil
	}
	return r.streams[0]
}

func (r *activeAIRun) terminalAssistantError() (string, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.last == nil || r.last.run == nil {
		return "", false
	}
	switch r.last.run.Status.State {
	case "error", "aborted":
		return aistream.ErrorVisibleText(r.last.run.Status.Error), true
	default:
		return "", false
	}
}

func (r *activeAIRun) setAssistantEntryID(entryID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, stream := range r.streams {
		stream.entryID = entryID
		return
	}
}

func (r *activeAIRun) addToolOutput(output toolOutputEvent) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.streams) == 0 {
		return
	}
	r.streams[0].tools = append(r.streams[0].tools, output)
}

func (r *activeAIRun) publishToolOutput(ctx context.Context, cl *Client, publisher bridgev2.BeeperStreamPublisher, roomID id.RoomID, output toolOutputEvent) error {
	r.mu.Lock()
	if len(r.streams) == 0 {
		r.mu.Unlock()
		return fmt.Errorf("no active assistant stream for tool output")
	}
	stream := r.streams[0]
	stream.tools = append(stream.tools, output)
	r.mu.Unlock()

	stream.publish.mu.Lock()
	defer stream.publish.mu.Unlock()
	writer := aistream.NewWriter(stream.run, time.Now)
	structuredOutput := toolOutput(output.Result, output.IsError)
	writer.ToolEnd(output.ID, output.Name, output.Input, structuredOutput)
	if stream.sources == nil {
		stream.sources = newSourceCollector()
	}
	for _, source := range stream.sources.addToolOutput(output, structuredOutput) {
		writer.Custom("com.beeper.source", source)
	}
	return cl.publishNewStreamEvents(ctx, publisher, roomID, stream.eventID, stream.run, &stream.publish)
}

func (cl *Client) waitForAssistantAnchorMessage(ctx context.Context, stream *assistantStreamState) error {
	if stream == nil {
		return fmt.Errorf("missing assistant stream")
	}
	if stream.portal == nil {
		return fmt.Errorf("missing assistant stream portal")
	}
	eventID, err := cl.waitForMessageEventID(ctx, stream.portal, stream.messageID, aiid.PartID("text"), 30*time.Second)
	if err != nil {
		return err
	}
	if stream.eventID == "" {
		stream.eventID = eventID
	}
	return nil
}

func (r *activeAIRun) finalizeAssistant(ctx context.Context, cl *Client, providerID string, model ai.Model, message ai.Message) {
	r.mu.Lock()
	if len(r.streams) == 0 {
		r.mu.Unlock()
		return
	}
	stream := r.streams[0]
	if assistantMessageHasToolCalls(message) {
		r.mu.Unlock()
		return
	}
	r.streams = r.streams[1:]
	r.last = stream
	r.mu.Unlock()

	if err := cl.waitForAssistantAnchorMessage(ctx, stream); err != nil {
		cl.logStreamError(err, "", stream.eventID, stream.run, "Failed to confirm AI stream anchor before final edit")
		cl.deleteActiveStream(ctx, stream.runID)
		return
	}
	fillAssistantMetadata(stream.metadata, stream.entryID, providerID, model.ID, stream.runID, message)
	appendToolOutputs(stream.run, stream.tools, message)
	cl.queueAssistantFinal(r.portalKey, stream.messageID, providerID, model, *stream.run, message, stream.metadata, afterMatrixTimestamp(stream.anchorTimestamp))
	cl.deleteActiveStream(ctx, stream.runID)
	cl.queueAssistantMediaMessages(r.portalKey, stream.messageID, providerID, model.ID, stream.runID, message)
}

func (r *activeAIRun) failOpenAssistant(ctx context.Context, cl *Client, providerID string, modelID string, err error) {
	r.mu.Lock()
	streams := append([]*assistantStreamState(nil), r.streams...)
	r.streams = nil
	r.mu.Unlock()
	for _, stream := range streams {
		if err := cl.waitForAssistantAnchorMessage(ctx, stream); err != nil {
			cl.logStreamError(err, "", stream.eventID, stream.run, "Failed to confirm AI stream anchor before error edit")
			cl.deleteActiveStream(ctx, stream.runID)
			continue
		}
		cl.queueAssistantRunError(r.portalKey, stream.messageID, providerID, modelID, stream.runID, *stream.run, stream.metadata, afterMatrixTimestamp(stream.anchorTimestamp), err)
		cl.deleteActiveStream(ctx, stream.runID)
	}
}

func (r *activeAIRun) lastAssistant() *assistantStreamState {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.last
}

func (r *activeAIRun) lastRunID() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.last != nil {
		return r.last.runID
	}
	return r.runID
}
