package connector

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/beeper/ai-bridge/pkg/agent/harness/session"
	ai "github.com/beeper/ai-bridge/pkg/ai"
	"github.com/beeper/ai-bridge/pkg/aiid"
	"github.com/beeper/ai-bridge/pkg/msgconv"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/bridgev2/simplevent"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/format"
)

type aiSlashCommand struct {
	name string
	arg  string
}

type aiSlashCommandDefinition struct {
	name            string
	usage           string
	description     string
	argRequired     bool
	needsRoomConfig bool
	noticeErrors    bool
	run             func(*Client, context.Context, *bridgev2.Portal, RoomConfig, string, aiCommandResponder) error
}

type aiCommandResponder interface {
	Reply(ctx context.Context, text string) error
}

type aiCommandResponderFunc func(ctx context.Context, text string) error

func (fn aiCommandResponderFunc) Reply(ctx context.Context, text string) error {
	return fn(ctx, text)
}

func aiSlashCommandDefinitions() []aiSlashCommandDefinition {
	return []aiSlashCommandDefinition{
		{
			name:        "help",
			usage:       "/help [command]",
			description: "Show available AI Bridge commands.",
			run:         runHelpCommand,
		},
		{
			name:            "model",
			usage:           "/model [model]",
			description:     "Show or set the AI model for this room. Use provider/model for a specific provider.",
			needsRoomConfig: true,
			noticeErrors:    true,
			run:             runModelCommand,
		},
		{
			name:            "reasoning",
			usage:           "/reasoning [off|minimal|low|medium|high|xhigh]",
			description:     "Show or set the reasoning level for this room when the selected model supports it.",
			needsRoomConfig: true,
			noticeErrors:    true,
			run:             runReasoningCommand,
		},
		{
			name:            "system-prompt",
			usage:           "/system-prompt [prompt|clear]",
			description:     "Show, set, or clear this room's additional system prompt.",
			needsRoomConfig: true,
			run:             runSystemPromptCommand,
		},
	}
}

func parseAISlashCommand(body string) (aiSlashCommand, bool) {
	body = strings.TrimSpace(body)
	if !strings.HasPrefix(body, "/") {
		return aiSlashCommand{}, false
	}
	name, arg, _ := strings.Cut(strings.TrimPrefix(body, "/"), " ")
	name = strings.ToLower(strings.TrimSpace(name))
	arg = strings.TrimSpace(arg)
	if _, ok := aiSlashCommandByName(name); ok {
		return aiSlashCommand{name: name, arg: arg}, true
	}
	return aiSlashCommand{}, false
}

func (cl *Client) handleAISlashCommand(ctx context.Context, msg *bridgev2.MatrixMessage) (*bridgev2.MatrixMessageResponse, bool, error) {
	if msg == nil || msg.Content == nil {
		return nil, false, nil
	}
	cmd, ok := parseAISlashCommand(msg.Content.Body)
	if !ok {
		return nil, false, nil
	}
	if msg.Portal == nil {
		return nil, true, fmt.Errorf("missing portal for AI command")
	}
	def, _ := aiSlashCommandByName(cmd.name)
	responder := aiCommandResponderFunc(func(ctx context.Context, text string) error {
		return cl.sendCommandNotice(ctx, msg.Portal, text)
	})
	if def.argRequired && cmd.arg == "" {
		if err := responder.Reply(ctx, aiSlashCommandUsage(def)); err != nil {
			return nil, true, err
		}
		return cl.commandHandledResponse(msg, "usage"), true, nil
	}
	var roomConfig RoomConfig
	if def.needsRoomConfig {
		var err error
		roomConfig, _, err = cl.Main.ReadRoomConfig(ctx, msg.Portal.MXID)
		if err != nil {
			return nil, true, err
		}
	}
	if err := def.run(cl, ctx, msg.Portal, roomConfig, cmd.arg, responder); err != nil {
		if def.noticeErrors {
			if noticeErr := responder.Reply(ctx, err.Error()); noticeErr != nil {
				return nil, true, noticeErr
			}
			return cl.commandHandledResponse(msg, "rejected"), true, nil
		}
		return nil, true, err
	}
	return cl.commandHandledResponse(msg, cmd.name), true, nil
}

func aiSlashCommandByName(name string) (aiSlashCommandDefinition, bool) {
	for _, def := range aiSlashCommandDefinitions() {
		if def.name == name {
			return def, true
		}
	}
	return aiSlashCommandDefinition{}, false
}

func aiSlashCommandUsage(def aiSlashCommandDefinition) string {
	return "Usage: " + def.usage
}

func aiSlashCommandHelp(topic string) string {
	topic = strings.TrimPrefix(strings.ToLower(strings.TrimSpace(topic)), "/")
	if topic != "" {
		if def, ok := aiSlashCommandByName(topic); ok {
			return fmt.Sprintf("%s\n\n%s", aiSlashCommandUsage(def), def.description)
		}
	}
	var text strings.Builder
	if topic != "" {
		fmt.Fprintf(&text, "Unknown command `/%s`.\n\n", topic)
	}
	text.WriteString("AI Bridge commands:")
	for _, def := range aiSlashCommandDefinitions() {
		fmt.Fprintf(&text, "\n- `%s` - %s", def.usage, def.description)
	}
	return text.String()
}

func runHelpCommand(cl *Client, ctx context.Context, portal *bridgev2.Portal, _ RoomConfig, arg string, responder aiCommandResponder) error {
	return responder.Reply(ctx, aiSlashCommandHelp(arg))
}

func runModelCommand(cl *Client, ctx context.Context, portal *bridgev2.Portal, roomConfig RoomConfig, arg string, responder aiCommandResponder) error {
	return cl.applyModelCommand(ctx, portal, roomConfig, arg, responder)
}

func runReasoningCommand(cl *Client, ctx context.Context, portal *bridgev2.Portal, roomConfig RoomConfig, arg string, responder aiCommandResponder) error {
	return cl.applyReasoningCommand(ctx, portal, roomConfig, arg, responder)
}

func runSystemPromptCommand(cl *Client, ctx context.Context, portal *bridgev2.Portal, roomConfig RoomConfig, arg string, responder aiCommandResponder) error {
	if strings.TrimSpace(arg) == "" {
		return responder.Reply(ctx, currentSystemPromptText(roomConfig))
	}
	prompt := arg
	if strings.EqualFold(prompt, "clear") || strings.EqualFold(prompt, "reset") {
		prompt = ""
	}
	if _, err := cl.writeRoomPromptState(ctx, portal, prompt); err != nil {
		return err
	}
	if prompt == "" {
		return responder.Reply(ctx, "System prompt cleared.")
	}
	return responder.Reply(ctx, "System prompt updated.")
}

func (cl *Client) applyModelCommand(ctx context.Context, portal *bridgev2.Portal, current RoomConfig, requested string, responder aiCommandResponder) error {
	if strings.TrimSpace(requested) == "" {
		_, model, canonical, err := cl.resolveCanonicalRoomModel(ctx, current)
		if err != nil {
			return fmt.Errorf("AI room settings rejected: %v", err)
		}
		return responder.Reply(ctx, fmt.Sprintf("Current model is `%s`. Current reasoning is `%s`.", canonical, cl.reasoningLevelForModel(model, current)))
	}
	target := current
	providerID, modelID := splitModelRef(requested)
	if modelID == "" {
		return fmt.Errorf("AI room settings rejected: model is required")
	}
	target.ProviderID = providerID
	target.ModelID = modelID
	provider, model, canonical, err := cl.resolveCanonicalRoomModel(ctx, target)
	if err != nil {
		return fmt.Errorf("AI room settings rejected: %v", err)
	}
	if err = cl.validateReasoningLevel(model, target); err != nil {
		return fmt.Errorf("AI room settings rejected: %v", err)
	}
	target.ThinkingLevel = cl.reasoningLevelForModel(model, target)
	if _, err = cl.writeRoomModelState(ctx, portal, provider, model, canonical, target.ThinkingLevel); err != nil {
		return err
	}
	cl.refreshRoomCapabilities(ctx, portal)
	return responder.Reply(ctx, fmt.Sprintf("Model set to `%s`. Current reasoning is `%s`.", canonical, target.ThinkingLevel))
}

func (cl *Client) applyReasoningCommand(ctx context.Context, portal *bridgev2.Portal, current RoomConfig, requested string, responder aiCommandResponder) error {
	if strings.TrimSpace(requested) == "" {
		_, model, canonical, err := cl.resolveCanonicalRoomModel(ctx, current)
		if err != nil {
			return fmt.Errorf("AI room settings rejected: %v", err)
		}
		return responder.Reply(ctx, fmt.Sprintf("Current reasoning is `%s` for `%s`.", cl.reasoningLevelForModel(model, current), canonical))
	}
	reasoning := strings.ToLower(strings.TrimSpace(requested))
	if !validRoomReasoningLevel(reasoning) {
		return fmt.Errorf("AI room settings rejected: reasoning level %q is invalid", requested)
	}
	target := current
	target.ThinkingLevel = reasoning
	provider, model, canonical, err := cl.resolveCanonicalRoomModel(ctx, target)
	if err != nil {
		return fmt.Errorf("AI room settings rejected: %v", err)
	}
	if err = cl.validateReasoningLevel(model, target); err != nil {
		return fmt.Errorf("AI room settings rejected: %v", err)
	}
	if _, err = cl.writeRoomModelState(ctx, portal, provider, model, canonical, reasoning); err != nil {
		return err
	}
	cl.refreshRoomCapabilities(ctx, portal)
	return responder.Reply(ctx, fmt.Sprintf("Reasoning set to `%s` for `%s`.", reasoning, canonical))
}

func displayReasoningLevel(level string) string {
	if level == "" {
		return string(ai.ModelThinkingLevelOff)
	}
	return level
}

func currentSystemPromptText(config RoomConfig) string {
	prompt := strings.TrimSpace(config.AdditionalPrompt)
	if prompt == "" {
		return "No additional system prompt is set."
	}
	return "Current system prompt:\n\n" + markdownCodeBlock(prompt)
}

func markdownCodeBlock(text string) string {
	fence := "```"
	for strings.Contains(text, fence) {
		fence += "`"
	}
	return fence + "\n" + text + "\n" + fence
}

func (cl *Client) normalizeRoomStateForPrompt(ctx context.Context, msg *bridgev2.MatrixMessage, config RoomConfig) (RoomConfig, *bridgev2.MatrixMessageResponse, bool, error) {
	if msg == nil || msg.Portal == nil {
		return config, nil, false, nil
	}
	provider, model, canonical, err := cl.resolveCanonicalRoomModel(ctx, config)
	if err != nil {
		if !config.modelStatePresent {
			return config, nil, false, err
		}
		if noticeErr := cl.sendCommandNotice(ctx, msg.Portal, fmt.Sprintf("AI room settings rejected: %v.", err)); noticeErr != nil {
			return config, nil, false, noticeErr
		}
		return config, cl.commandHandledResponse(msg, "invalid-settings"), true, nil
	}
	if config.ThinkingLevel != "" && !validRoomReasoningLevel(config.ThinkingLevel) {
		if !config.modelStatePresent {
			return config, nil, false, fmt.Errorf("reasoning level %q is invalid", config.ThinkingLevel)
		}
		if noticeErr := cl.sendCommandNotice(ctx, msg.Portal, fmt.Sprintf("AI room settings rejected: reasoning level %q is invalid.", config.ThinkingLevel)); noticeErr != nil {
			return config, nil, false, noticeErr
		}
		return config, cl.commandHandledResponse(msg, "invalid-settings"), true, nil
	}
	if err = cl.validateReasoningLevel(model, config); err != nil {
		if !config.modelStatePresent {
			return config, nil, false, err
		}
		if noticeErr := cl.sendCommandNotice(ctx, msg.Portal, fmt.Sprintf("AI room settings rejected: %v.", err)); noticeErr != nil {
			return config, nil, false, noticeErr
		}
		return config, cl.commandHandledResponse(msg, "invalid-settings"), true, nil
	}
	config.ThinkingLevel = cl.reasoningLevelForModel(model, config)
	normalized := config.modelStatePresent && (config.modelStateModel != canonical || config.modelStateReason != config.ThinkingLevel)
	if normalized {
		if _, err = cl.writeRoomModelState(ctx, msg.Portal, provider, model, canonical, config.ThinkingLevel); err != nil {
			return config, nil, false, err
		}
		cl.refreshRoomCapabilities(ctx, msg.Portal)
		if noticeErr := cl.sendCommandNotice(ctx, msg.Portal, fmt.Sprintf("AI room settings normalized to `%s`.", canonical)); noticeErr != nil {
			return config, nil, false, noticeErr
		}
	}
	config.ProviderID = provider.ID
	config.ModelID = model.ID
	return config, nil, false, nil
}

func (cl *Client) refreshRoomCapabilities(ctx context.Context, portal *bridgev2.Portal) {
	if cl == nil || cl.UserLogin == nil || portal == nil {
		return
	}
	portal.UpdateCapabilities(ctx, cl.UserLogin, true)
}

func (cl *Client) resolveCanonicalRoomModel(ctx context.Context, config RoomConfig) (aiid.ProviderConfig, ai.Model, string, error) {
	provider, modelID, err := cl.resolveProvider(ctx, config)
	if err != nil {
		return aiid.ProviderConfig{}, ai.Model{}, "", err
	}
	model := cl.Main.ModelForProvider(provider, modelID)
	return provider, model, provider.ID + "/" + model.ID, nil
}

func validRoomReasoningLevel(level string) bool {
	switch level {
	case "", string(ai.ModelThinkingLevelOff), string(ai.ModelThinkingLevelMinimal), string(ai.ModelThinkingLevelLow), string(ai.ModelThinkingLevelMedium), string(ai.ModelThinkingLevelHigh), string(ai.ModelThinkingLevelXHigh):
		return true
	default:
		return false
	}
}

func (cl *Client) writeRoomModelState(ctx context.Context, portal *bridgev2.Portal, provider aiid.ProviderConfig, model ai.Model, canonicalModel string, reasoning string) (string, error) {
	content := map[string]any{"model": canonicalModel}
	if reasoning != "" {
		content["reasoning"] = reasoning
	}
	eventID, err := cl.writeAIRoomState(ctx, portal, aiid.RoomModelType, content)
	if err != nil {
		return eventID, err
	}
	cl.updateRoomModelProfile(ctx, portal, provider, model)
	return eventID, nil
}

func (cl *Client) updateRoomModelProfile(ctx context.Context, portal *bridgev2.Portal, provider aiid.ProviderConfig, model ai.Model) {
	if cl == nil || cl.UserLogin == nil || portal == nil {
		return
	}
	topic := modelRoomDescription(provider, model)
	portal.UpdateInfo(ctx, &bridgev2.ChatInfo{
		Topic:  &topic,
		Avatar: modelAvatar(provider, model),
	}, cl.UserLogin, nil, time.Now())
}

func modelRoomDescription(provider aiid.ProviderConfig, model ai.Model) string {
	return "AI Chat with " + modelDisplayName(provider, model)
}

func (cl *Client) writeRoomPromptState(ctx context.Context, portal *bridgev2.Portal, prompt string) (string, error) {
	return cl.writeAIRoomState(ctx, portal, aiid.RoomPromptType, map[string]any{"prompt": strings.TrimSpace(prompt)})
}

func (cl *Client) writeAIRoomState(ctx context.Context, portal *bridgev2.Portal, stateType string, content map[string]any) (string, error) {
	return cl.Main.aiRoomStateStore().Write(ctx, portal, stateType, content)
}

func (cl *Client) sendCommandNotice(ctx context.Context, portal *bridgev2.Portal, text string) error {
	if cl == nil || cl.UserLogin == nil || portal == nil || portal.MXID == "" {
		return fmt.Errorf("portal room is not available to send command notice")
	}
	content := commandResponseContent(text)
	if roomConfig, _, err := cl.Main.ReadRoomConfig(ctx, portal.MXID); err == nil {
		if provider, modelID, err := cl.Main.ResolveProvider(ctx, cl.UserLogin, roomConfig); err == nil {
			cl.applyModelProfile(ctx, content, provider.ID, modelID)
		}
	}
	if content.BeeperPerMessageProfile == nil {
		content.BeeperPerMessageProfile = &event.BeeperPerMessageProfile{
			ID:          string(aiid.AssistantUserID()),
			Displayname: "AI",
			HasFallback: true,
		}
	}
	cl.UserLogin.QueueRemoteEvent(&simplevent.PreConvertedMessage{
		EventMeta: simplevent.EventMeta{
			Type:      bridgev2.RemoteEventMessage,
			PortalKey: portal.PortalKey,
			Sender: bridgev2.EventSender{
				Sender: aiid.AssistantUserID(),
			},
			Timestamp: time.Now(),
		},
		ID: networkid.MessageID("command-notice:" + session.CreateSessionID()),
		Data: &bridgev2.ConvertedMessage{Parts: []*bridgev2.ConvertedMessagePart{{
			ID:      aiid.PartID("command"),
			Type:    event.EventMessage,
			Content: content,
			DBMetadata: &aiid.MessageMetadata{
				Role:         "command",
				StreamStatus: "notice",
			},
		}}},
	})
	return nil
}

func commandResponseContent(text string) *event.MessageEventContent {
	if strings.TrimSpace(text) == "" {
		return msgconv.TextContent(text)
	}
	content := format.RenderMarkdown(text, true, false)
	content.EnsureHasHTML()
	return &content
}

func (cl *Client) commandHandledResponse(msg *bridgev2.MatrixMessage, status string) *bridgev2.MatrixMessageResponse {
	return &bridgev2.MatrixMessageResponse{DB: &database.Message{
		ID:        networkid.MessageID("command:" + session.CreateSessionID()),
		PartID:    aiid.PartID("command"),
		Room:      msg.Portal.PortalKey,
		SenderID:  cl.GetUserID(),
		Timestamp: matrixEventTime(msg.Event),
		Metadata: &aiid.MessageMetadata{
			Role:         "command",
			StreamStatus: status,
		},
	}}
}
