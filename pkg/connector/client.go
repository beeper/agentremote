package connector

import (
	"bytes"
	"context"
	"database/sql"
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

	"github.com/beeper/ai-bridge/pkg/ag-ui"
	agent "github.com/beeper/ai-bridge/pkg/agent"
	"github.com/beeper/ai-bridge/pkg/agent/harness"
	"github.com/beeper/ai-bridge/pkg/agent/harness/session"
	"github.com/beeper/ai-bridge/pkg/agent/sessiontitle"
	ai "github.com/beeper/ai-bridge/pkg/ai"
	aistream "github.com/beeper/ai-bridge/pkg/ai-stream"
	aibridgev2 "github.com/beeper/ai-bridge/pkg/ai-stream/bridgev2"
	aimatrix "github.com/beeper/ai-bridge/pkg/ai-stream/matrix"
	"github.com/beeper/ai-bridge/pkg/aiid"
	"github.com/beeper/ai-bridge/pkg/msgconv"
	"github.com/rs/zerolog"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/bridgev2/simplevent"
	"maunium.net/go/mautrix/bridgev2/status"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

type Client struct {
	Main      *Connector
	UserLogin *bridgev2.UserLogin
	loggedIn  bool

	activeMu        sync.Mutex
	activeHarnesses map[networkid.PortalKey]*harness.AgentHarness
	activeRuns      map[networkid.PortalKey]*activeAIRun
	providerAuthMu  sync.Mutex
}

func aguiFinishReasonFromAI(reason ai.StopReason) string {
	switch reason {
	case "", ai.StopReasonStop:
		return agui.FinishReasonStop
	case ai.StopReasonLength:
		return agui.FinishReasonLength
	case ai.StopReasonToolUse:
		return agui.FinishReasonToolCalls
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

	mu       sync.Mutex
	pending  []*pendingAIMessage
	consumed []*pendingAIMessage
	streams  []*assistantStreamState
	last     *assistantStreamState
}

type assistantStreamState struct {
	messageID networkid.MessageID
	eventID   id.EventID
	runID     string
	run       *aistream.Run
	metadata  *aiid.MessageMetadata
	entryID   string
	tools     []toolOutputEvent
	publish   streamPublishCursor
}

type streamPublishCursor struct {
	mu        sync.Mutex
	published int
	nextSeq   int
	started   bool
}

var _ bridgev2.NetworkAPI = (*Client)(nil)
var _ bridgev2.NetworkAPIWithUserID = (*Client)(nil)
var _ bridgev2.RedactionHandlingNetworkAPI = (*Client)(nil)
var _ bridgev2.RoomNameHandlingNetworkAPI = (*Client)(nil)
var _ bridgev2.RoomTopicHandlingNetworkAPI = (*Client)(nil)
var _ bridgev2.DisappearTimerChangingNetworkAPI = (*Client)(nil)
var _ bridgev2.GroupCreatingNetworkAPI = (*Client)(nil)
var _ bridgev2.IdentifierResolvingNetworkAPI = (*Client)(nil)
var _ bridgev2.ContactListingNetworkAPI = (*Client)(nil)
var _ bridgev2.UserSearchingNetworkAPI = (*Client)(nil)

func (cl *Client) Connect(ctx context.Context) {
	cl.loggedIn = true
	cl.sendBridgeState(status.StateConnected)
}

func (cl *Client) Disconnect() {
	cl.loggedIn = false
}

func (cl *Client) IsLoggedIn() bool {
	return cl.loggedIn
}

func (cl *Client) LogoutRemote(ctx context.Context) {
	cl.loggedIn = false
	cl.sendBridgeState(status.StateLoggedOut)
}

func (cl *Client) sendBridgeState(state status.BridgeStateEvent) {
	if cl != nil && cl.UserLogin != nil && cl.UserLogin.BridgeState != nil {
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
	return &bridgev2.ChatInfo{Name: &name, Topic: topic, Type: &roomType, Members: aiChatMembers(), Disappear: disappear}, nil
}

func (cl *Client) GetUserInfo(ctx context.Context, ghost *bridgev2.Ghost) (*bridgev2.UserInfo, error) {
	return aiAssistantUserInfo(), nil
}

func (cl *Client) CreateGroup(ctx context.Context, params *bridgev2.GroupCreateParams) (*bridgev2.CreateChatResponse, error) {
	if params == nil || params.RoomID == "" {
		return nil, fmt.Errorf("AI sessions must be created from an existing Matrix room")
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
	roomType := database.RoomTypeDM
	return &bridgev2.CreateChatResponse{
		PortalKey: aiid.PortalKey(params.RoomID, cl.UserLogin.ID),
		PortalInfo: &bridgev2.ChatInfo{
			Name:    &name,
			Topic:   topic,
			Type:    &roomType,
			Members: aiChatMembers(),
		},
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
	streamFn := cl.assistantStreamPublisher(streamPublisher, msg.Portal, portalMeta, provider, model, func() {
		cl.queueAssistantTyping(msg.Portal.PortalKey, 0)
	})
	options := harness.AgentHarnessOptions{
		Session:             agentSession,
		Model:               model,
		ThinkingLevel:       agent.ThinkingLevel(cl.reasoningLevelForModel(model, roomConfig)),
		SystemPrompt:        cl.systemPrompt(roomConfig),
		Tools:               cl.chatTools(msg, portalMeta, roomConfig, provider, model, prompt),
		StreamFn:            streamFn,
		GetAPIKeyAndHeaders: cl.authForProvider(provider),
	}
	agentHarness, err := harness.NewAgentHarness(options)
	if err != nil {
		cl.markPendingFailed(ctx, pending, err)
		return
	}
	active := &activeAIRun{portalKey: msg.Portal.PortalKey, harness: agentHarness, provider: provider, model: model, runID: runID}
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
		return
	}
	titleModel := cl.titleGenerationModel(provider, model)
	auth, err := cl.authForProvider(provider)(ctx, titleModel)
	if err != nil {
		return
	}
	title, err := sessiontitle.Generate(ctx, contextView.Messages, sessiontitle.Options{
		Model:   titleModel,
		APIKey:  auth.APIKey,
		Headers: auth.Headers,
	})
	if err != nil || title == "" {
		return
	}
	meta.AutoTitlePending = false
	if err = portal.Save(ctx); err != nil {
		return
	}
	cl.queueRoomTitleUpdate(portal.PortalKey, title)
}

func (cl *Client) titleGenerationModel(provider aiid.ProviderConfig, fallback ai.Model) ai.Model {
	modelID := titleGenerationModelID(provider)
	if modelID == "" || !providerAllowsModel(provider, modelID) {
		return fallback
	}
	if cl != nil && cl.Main != nil {
		return cl.Main.ModelForProvider(provider, modelID)
	}
	return normalizeProviderModel(modelForProviderConfig(provider, modelID), provider)
}

func titleGenerationModelID(provider aiid.ProviderConfig) string {
	switch provider.Provider {
	case ai.ProviderOpenAI:
		return defaultTitleGenerationModel
	case ai.ProviderOpenRouter:
		return openRouterTitleGenerationModel
	default:
		return ""
	}
}

func (cl *Client) queueRoomTitleUpdate(portalKey networkid.PortalKey, title string) {
	if cl == nil || cl.UserLogin == nil || title == "" {
		return
	}
	cl.UserLogin.QueueRemoteEvent(&simplevent.ChatInfoChange{
		EventMeta: simplevent.EventMeta{
			Type:      bridgev2.RemoteEventChatInfoChange,
			PortalKey: portalKey,
			Sender: bridgev2.EventSender{
				Sender: aiid.AssistantUserID(),
			},
			Timestamp: time.Now(),
		},
		ChatInfoChange: &bridgev2.ChatInfoChange{
			ChatInfo: &bridgev2.ChatInfo{Name: &title},
		},
	})
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
			active.markConsumed(ctx, cl, event.SessionEntryID, time.Now())
			return nil
		}
		if event.Type == "message_end" && event.Message != nil && event.Message.Role == "assistant" && event.SessionEntryID != "" {
			active.setAssistantEntryID(event.SessionEntryID)
			return nil
		}
		if event.Type != "tool_execution_end" || event.AgentEvent == nil || event.AgentEvent.ToolCallID == "" {
			if event.Type == "turn_end" && event.Message != nil && event.Message.Role == "assistant" {
				active.finalizeAssistant(ctx, cl, provider.ID, model.ID, *event.Message)
				if event.Message.StopReason != ai.StopReasonError && event.Message.StopReason != ai.StopReasonAborted && !assistantMessageHasToolCalls(*event.Message) {
					cl.generateSessionTitle(ctx, msg.Portal, portalMeta, agentSession, provider, model)
				}
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
	assistantMessage := promptResult.Message
	if promptResult.AssistantEntryID == "" {
		err := fmt.Errorf("prompt did not create expected assistant session entry")
		active.failAll(ctx, cl, err)
		active.failOpenAssistant(ctx, cl, provider.ID, model.ID, err)
		return
	}
	if assistantMessage.StopReason == ai.StopReasonError {
		err := errors.New(assistantMessage.ErrorMessage)
		if assistantMessage.ErrorMessage == "" {
			err = errors.New("AI failed to respond")
		}
		active.failConsumed(ctx, cl, err)
	}
	if assistantMessage.StopReason != ai.StopReasonError && assistantMessage.StopReason != ai.StopReasonAborted {
		if last := active.lastAssistant(); last != nil {
			cl.runAutoCompaction(ctx, streamPublisher, msg.Portal.MXID, last.eventID, agentHarness, agentSession, model, assistantMessage)
		}
	}
	portalMeta.LastRunID = active.lastRunID()
	_ = msg.Portal.Save(ctx)
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

func (cl *Client) queueConsumedUserEcho(ctx context.Context, pending *pendingAIMessage, userEntryID string, consumedAt time.Time) {
	if pending == nil || pending.msg == nil {
		return
	}
	pending.metadata.SessionEntryID = userEntryID
	pending.metadata.Role = "user"
	pending.metadata.StreamStatus = "done"
	messageID := aiid.UserMessageID(userEntryID)
	cl.UserLogin.QueueRemoteEvent(&simplevent.PreConvertedMessage{
		EventMeta: simplevent.EventMeta{
			Type:      bridgev2.RemoteEventMessage,
			PortalKey: pending.msg.Portal.PortalKey,
			Sender: bridgev2.EventSender{
				Sender: cl.GetUserID(),
			},
			Timestamp: consumedAt,
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
}

func (cl *Client) queueAssistantRunError(portalKey networkid.PortalKey, messageID networkid.MessageID, providerID string, modelID string, runID string, run aistream.Run, metadata *aiid.MessageMetadata, err error) {
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
	cl.UserLogin.QueueRemoteEvent(cl.assistantFinalEdit(portalKey, messageID, providerID, modelID, runID, run, message, metadata))
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
	active := cl.getActiveRun(msg.Portal.PortalKey)
	h := cl.getActiveHarness(msg.Portal.PortalKey)
	if h == nil {
		return nil
	}
	_, err := h.Abort(ctx)
	if active != nil {
		active.failAll(ctx, cl, fmt.Errorf("AI run aborted"))
	}
	return err
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
		agentSession, err := cl.Main.Store.OpenSession(ctx, session.SQLiteSessionMetadata{
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
		agentSession, err := cl.Main.Store.OpenSession(ctx, session.SQLiteSessionMetadata{
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

func (cl *Client) assistantEvent(ctx context.Context, portalKey networkid.PortalKey, messageID networkid.MessageID, providerID string, modelID string, runID string, descriptor *event.BeeperStreamInfo, run aistream.Run) (*simplevent.PreConvertedMessage, *aiid.MessageMetadata) {
	metadata := &aiid.MessageMetadata{
		Role:         "assistant",
		ProviderID:   providerID,
		ModelID:      modelID,
		RunID:        runID,
		StreamStatus: "streaming",
	}
	msg := aibridgev2.Anchor(portalKey, aiid.AssistantUserID(), run, time.Now())
	if len(msg.Data.Parts) > 0 {
		msg.Data.Parts[0].ID = aiid.PartID("text")
		msg.Data.Parts[0].Content.BeeperStream = descriptor
		cl.applyModelProfile(ctx, msg.Data.Parts[0].Content, providerID, modelID)
		msg.Data.Parts[0].DBMetadata = metadata
	}
	return msg, metadata
}

func finalizedAssistantRun(run aistream.Run, message ai.Message) aistream.Run {
	if message.StopReason == ai.StopReasonError {
		run.Status = aistream.Status{State: "error", Error: map[string]any{"message": message.ErrorMessage}}
	} else if run.Status.State == "streaming" {
		run.Status = aistream.Status{State: "complete", FinishReason: aguiFinishReasonFromAI(message.StopReason)}
	}
	run.Usage = aguiUsage(message.Usage)
	if run.Preview.Text == "" {
		run.Preview = aistream.PreviewFromText(msgconv.AssistantText(message), aistream.PreviewBudgetBytes)
	}
	return run
}

func (cl *Client) assistantFinalEdit(portalKey networkid.PortalKey, messageID networkid.MessageID, providerID string, modelID string, runID string, run aistream.Run, message ai.Message, metadata *aiid.MessageMetadata) *simplevent.Message[*aistream.Run] {
	run = finalizedAssistantRun(run, message)
	projection := aimatrix.ProjectFinal(run)
	return cl.assistantFinalEditWithProjection(portalKey, messageID, providerID, modelID, runID, run, projection, metadata)
}

func (cl *Client) assistantFinalEditWithProjection(portalKey networkid.PortalKey, messageID networkid.MessageID, providerID string, modelID string, runID string, run aistream.Run, projection aimatrix.FinalProjection, metadata *aiid.MessageMetadata) *simplevent.Message[*aistream.Run] {
	edit := aibridgev2.FinalMetadataEditWithContent(portalKey, aiid.AssistantUserID(), messageID, run, projection.Content, projection.Extra, time.Now())
	originalConvert := edit.ConvertEditFunc
	edit.ConvertEditFunc = func(ctx context.Context, portal *bridgev2.Portal, intent bridgev2.MatrixAPI, existing []*database.Message, data *aistream.Run) (*bridgev2.ConvertedEdit, error) {
		converted, err := originalConvert(ctx, portal, intent, existing, data)
		if err != nil || converted == nil {
			return converted, err
		}
		if len(existing) > 0 {
			existing[0].Metadata = metadata
		}
		if len(converted.ModifiedParts) > 0 && converted.ModifiedParts[0].Content != nil {
			cl.applyModelProfile(ctx, converted.ModifiedParts[0].Content, providerID, modelID)
		}
		return converted, nil
	}
	return edit
}

func (cl *Client) queueAssistantFinal(portalKey networkid.PortalKey, messageID networkid.MessageID, targetEventID id.EventID, providerID string, modelID string, runID string, run aistream.Run, message ai.Message, metadata *aiid.MessageMetadata) {
	if cl == nil || cl.UserLogin == nil {
		return
	}
	run = finalizedAssistantRun(run, message)
	projection := aimatrix.ProjectFinal(run)
	if targetEventID != "" {
		for _, segment := range aibridgev2.FinalSegmentMessages(portalKey, aiid.AssistantUserID(), run, projection.Segments, targetEventID, time.Now()) {
			cl.UserLogin.QueueRemoteEvent(segment)
		}
	}
	cl.UserLogin.QueueRemoteEvent(cl.assistantFinalEditWithProjection(portalKey, messageID, providerID, modelID, runID, run, projection, metadata))
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
		}
		runID := session.CreateSessionID()
		messageID := aiid.AssistantMessageID(runID)
		run := aistream.NewRun(runID, meta.SessionID, provider.ID+"/"+model.ID, string(aiid.AssistantUserID()), "AI", time.Now())
		run.MessageID = string(messageID)
		eventID := cl.Main.Bridge.Matrix.GenerateDeterministicEventID(
			portal.MXID,
			portal.PortalKey,
			messageID,
			aiid.PartID("text"),
		)
		descriptor, err := publisher.NewDescriptor(ctx, portal.MXID, aiid.StreamType)
		if err != nil {
			cl.logStreamError(err, portal.MXID, eventID, run, "Failed to create AI stream descriptor")
			return hookStreamError(err)
		}
		cl.logStreamDebug(ctx, portal.MXID, eventID, run, "Created AI stream descriptor", func(evt *zerolog.Event) {
			evt.Str("stream_type", descriptor.Type).Str("descriptor_user_id", string(descriptor.UserID))
		})
		assistantEvent, metadata := cl.assistantEvent(ctx, portal.PortalKey, messageID, provider.ID, model.ID, runID, descriptor, *run)
		cl.UserLogin.QueueRemoteEvent(assistantEvent)
		cl.logStreamDebug(ctx, portal.MXID, eventID, run, "Queued AI stream anchor")
		if err := publisher.Register(ctx, portal.MXID, eventID, descriptor); err != nil {
			cl.logStreamError(err, portal.MXID, eventID, run, "Failed to register AI stream publisher")
			cl.queueAssistantRunError(portal.PortalKey, messageID, provider.ID, model.ID, runID, *run, metadata, err)
			return hookStreamError(err)
		}
		cl.logStreamDebug(ctx, portal.MXID, eventID, run, "Registered AI stream publisher", func(evt *zerolog.Event) {
			evt.Str("stream_type", descriptor.Type)
		})
		if active := cl.getActiveRun(portal.PortalKey); active != nil {
			stream := &assistantStreamState{
				messageID: messageID,
				eventID:   eventID,
				runID:     runID,
				run:       run,
				metadata:  metadata,
				publish:   streamPublishCursor{nextSeq: 1},
			}
			active.addAssistantStream(stream)
			return cl.streamPublisherWithEndFrom(publisher, portal.MXID, eventID, run, &stream.publish, nil, onSecondVisibleChunk...)(ctx, model, llmContext, options)
		}
		return cl.streamPublisherWithEnd(publisher, portal.MXID, eventID, run, func() {
			publisher.Unregister(portal.MXID, eventID)
		}, onSecondVisibleChunk...)(ctx, model, llmContext, options)
	}
}

func (cl *Client) streamPublisher(publisher bridgev2.BeeperStreamPublisher, roomID id.RoomID, eventID id.EventID, run *aistream.Run, onSecondVisibleChunk ...func()) agent.StreamFn {
	return cl.streamPublisherWithEnd(publisher, roomID, eventID, run, nil, onSecondVisibleChunk...)
}

func (cl *Client) streamPublisherWithEnd(publisher bridgev2.BeeperStreamPublisher, roomID id.RoomID, eventID id.EventID, run *aistream.Run, onEnd func(), onSecondVisibleChunk ...func()) agent.StreamFn {
	return cl.streamPublisherWithEndFrom(publisher, roomID, eventID, run, &streamPublishCursor{nextSeq: 1}, onEnd, onSecondVisibleChunk...)
}

func (cl *Client) streamPublisherWithEndFrom(publisher bridgev2.BeeperStreamPublisher, roomID id.RoomID, eventID id.EventID, run *aistream.Run, cursor *streamPublishCursor, onEnd func(), onSecondVisibleChunk ...func()) agent.StreamFn {
	return func(ctx context.Context, model ai.Model, llmContext ai.Context, options ai.SimpleStreamOptions) *ai.AssistantMessageEventStream {
		upstream := ai.StreamSimple(ctx, model, llmContext, options)
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
		cursor.mu.Lock()
		if !cursor.started {
			writer.Start()
			cursor.started = true
		}
		startErr := cl.publishNewStreamEvents(ctx, publisher, roomID, eventID, run, cursor)
		cursor.mu.Unlock()
		if startErr != nil {
			cl.logStreamError(startErr, roomID, eventID, run, "Failed to publish AI stream start carrier")
			stream := hookStreamError(startErr)
			downstream.End()
			return stream
		}
		go func() {
			if onEnd != nil {
				defer onEnd()
			}
			defer downstream.End()
			seenFirstDelta := false
			for evt := range upstream.Events() {
				cursor.mu.Lock()
				beforeEvents := len(run.Events)
				applyAIStreamEvent(writer, evt)
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
			cursor.mu.Lock()
			publishedEvents := cursor.published
			nextSeq := cursor.nextSeq
			cursor.mu.Unlock()
			cl.logStreamDebug(ctx, roomID, eventID, run, "Finished AI stream publishing", func(logEvt *zerolog.Event) {
				logEvt.Int("published_agui_events", publishedEvents).
					Int("next_seq", nextSeq)
			})
		}()
		return downstream
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
		if err := publisher.Publish(ctx, roomID, eventID, aistream.CarrierContent(partial, carrier.Envelopes)); err != nil {
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
	return nil
}

func (cl *Client) logMatrixMessageError(msg *bridgev2.MatrixMessage, err error, message string) {
	if cl == nil || cl.Main == nil || cl.Main.Bridge == nil {
		return
	}
	event := cl.Main.Bridge.Log.Error().Err(err)
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

func (cl *Client) queueAssistantMediaMessages(portalKey networkid.PortalKey, providerID string, modelID string, runID string, message ai.Message) {
	if cl == nil || cl.UserLogin == nil {
		return
	}
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
				return assistantImageConvertedMessage(ctx, portal, intent, data, partID, metadata)
			},
		})
		mediaIndex++
	}
}

type matrixMediaUploader interface {
	UploadMedia(ctx context.Context, roomID id.RoomID, data []byte, fileName, mimeType string) (url id.ContentURIString, file *event.EncryptedFileInfo, err error)
}

func assistantImageConvertedMessage(ctx context.Context, portal *bridgev2.Portal, intent matrixMediaUploader, block ai.ContentBlock, partID networkid.PartID, metadata *aiid.MessageMetadata) (*bridgev2.ConvertedMessage, error) {
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
	return &bridgev2.ConvertedMessage{Parts: []*bridgev2.ConvertedMessagePart{{
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

func applyAIStreamEvent(writer *aistream.Writer, evt ai.AssistantMessageEvent) {
	if writer == nil {
		return
	}
	annotateLast := func() {
		if evt.RawEvent == nil || writer.Run == nil || len(writer.Run.Events) == 0 {
			return
		}
		writer.Run.Events[len(writer.Run.Events)-1].Set("rawEvent", evt.RawEvent)
		if evt.RawSource != "" {
			writer.Run.Events[len(writer.Run.Events)-1].Set("rawSource", evt.RawSource)
		}
	}
	annotateIfAdded := func(before int) {
		if writer.Run != nil && len(writer.Run.Events) > before {
			annotateLast()
		}
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
		return &ai.ToolCall{Type: "toolCall", ID: block.ID, Name: block.Name, Arguments: block.Arguments}
	}
	switch evt.Type {
	case "text_start":
		before := len(writer.Run.Events)
		writer.TextStart(evt.ContentIndex)
		annotateIfAdded(before)
	case "text_delta":
		before := len(writer.Run.Events)
		writer.TextDelta(evt.ContentIndex, evt.Delta)
		annotateIfAdded(before)
	case "text_end":
		before := len(writer.Run.Events)
		writer.TextEnd(evt.ContentIndex)
		annotateIfAdded(before)
	case "thinking_start":
		before := len(writer.Run.Events)
		writer.ReasoningMessageStart(evt.ContentIndex)
		annotateIfAdded(before)
	case "thinking_delta":
		before := len(writer.Run.Events)
		writer.ReasoningDelta(evt.ContentIndex, evt.Delta)
		annotateIfAdded(before)
	case "thinking_end":
		before := len(writer.Run.Events)
		writer.ReasoningMessageEnd(evt.ContentIndex)
		annotateIfAdded(before)
	case "toolcall_start":
		if toolCall := toolCallFromEvent(); toolCall != nil {
			writer.ToolStart(toolCall.ID, toolCall.Name, evt.ContentIndex, nil)
			annotateLast()
		}
	case "toolcall_delta":
		if toolCall := toolCallFromEvent(); toolCall != nil {
			writer.ToolArgs(toolCall.ID, evt.Delta, toolCall.Arguments)
			annotateLast()
		}
	case "toolcall_end":
		if toolCall := toolCallFromEvent(); toolCall != nil {
			writer.ToolEnd(toolCall.ID, toolCall.Name, toolCall.Arguments, nil)
			annotateLast()
		}
	case "raw":
		writer.Raw(evt.RawEvent, evt.RawSource)
	case "custom":
		if evt.CustomName != "" {
			writer.Custom(evt.CustomName, evt.CustomValue)
			annotateLast()
		}
	case "done":
		if evt.Message != nil {
			usage := aguiUsage(evt.Message.Usage)
			writer.FinishWithUsage(aguiFinishReasonFromAI(evt.Reason), &usage)
		} else {
			writer.Finish(aguiFinishReasonFromAI(evt.Reason))
		}
		annotateLast()
	case "error":
		message := "stream error"
		if evt.Error != nil && evt.Error.ErrorMessage != "" {
			message = evt.Error.ErrorMessage
		}
		writer.Error(message)
		annotateLast()
	}
}

type toolOutputEvent struct {
	ID      string
	Name    string
	Input   any
	Result  agent.AgentToolResult[any]
	IsError bool
}

func appendToolOutputs(run *aistream.Run, outputs []toolOutputEvent) {
	if run == nil || len(outputs) == 0 {
		return
	}
	writer := aistream.NewWriter(run, time.Now)
	for _, output := range outputs {
		structuredOutput := toolOutput(output.Result, output.IsError)
		writer.ToolEnd(output.ID, output.Name, output.Input, structuredOutput)
		for _, source := range webSearchSourceParts(output.Name, structuredOutput, output.IsError) {
			writer.Custom("com.beeper.source", source)
		}
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

func aguiUsage(usage ai.Usage) agui.Usage {
	total := usage.TotalTokens
	if total == 0 {
		total = usage.Input + usage.Output
	}
	return agui.Usage{
		PromptTokens:     usage.Input,
		CompletionTokens: usage.Output,
		ReasoningTokens:  usage.ReasoningTokens,
		TotalTokens:      total,
	}
}

func (cl *Client) sessionForPortal(ctx context.Context, portal *bridgev2.Portal, meta *aiid.PortalMetadata) (*session.Session, error) {
	if meta == nil {
		meta = &aiid.PortalMetadata{}
		portal.Metadata = meta
	}
	if meta.SessionID != "" {
		agentSession, err := cl.Main.Store.OpenSession(ctx, session.SQLiteSessionMetadata{
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
	agentSession, err := cl.Main.Store.CreateSession(ctx, session.SQLiteSessionCreateOptions{})
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

func (r *activeAIRun) addPending(pending *pendingAIMessage) {
	r.mu.Lock()
	defer r.mu.Unlock()
	pending.metadata.ProviderID = r.provider.ID
	pending.metadata.ModelID = r.model.ID
	pending.metadata.RunID = r.runID
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

func (r *activeAIRun) markConsumed(ctx context.Context, cl *Client, entryID string, consumedAt time.Time) {
	r.mu.Lock()
	if len(r.pending) == 0 {
		r.mu.Unlock()
		return
	}
	pending := r.pending[0]
	r.pending = r.pending[1:]
	r.consumed = append(r.consumed, pending)
	r.mu.Unlock()
	cl.queueConsumedUserEcho(ctx, pending, entryID, consumedAt)
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

func (r *activeAIRun) currentAssistantStream() *assistantStreamState {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.streams) == 0 {
		return nil
	}
	return r.streams[0]
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
	r.mu.Unlock()

	stream.publish.mu.Lock()
	defer stream.publish.mu.Unlock()
	writer := aistream.NewWriter(stream.run, time.Now)
	structuredOutput := toolOutput(output.Result, output.IsError)
	writer.ToolEnd(output.ID, output.Name, output.Input, structuredOutput)
	for _, source := range webSearchSourceParts(output.Name, structuredOutput, output.IsError) {
		writer.Custom("com.beeper.source", source)
	}
	return cl.publishNewStreamEvents(ctx, publisher, roomID, stream.eventID, stream.run, &stream.publish)
}

func (r *activeAIRun) finalizeAssistant(ctx context.Context, cl *Client, providerID string, modelID string, message ai.Message) {
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

	fillAssistantMetadata(stream.metadata, stream.entryID, providerID, modelID, stream.runID, message)
	appendToolOutputs(stream.run, stream.tools)
	cl.queueAssistantFinal(r.portalKey, stream.messageID, stream.eventID, providerID, modelID, stream.runID, *stream.run, message, stream.metadata)
	cl.queueAssistantMediaMessages(r.portalKey, providerID, modelID, stream.runID, message)
}

func (r *activeAIRun) failOpenAssistant(ctx context.Context, cl *Client, providerID string, modelID string, err error) {
	r.mu.Lock()
	streams := append([]*assistantStreamState(nil), r.streams...)
	r.streams = nil
	r.mu.Unlock()
	for _, stream := range streams {
		cl.queueAssistantRunError(r.portalKey, stream.messageID, providerID, modelID, stream.runID, *stream.run, stream.metadata, err)
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
