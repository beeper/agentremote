package connector

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/event"

	agui "github.com/beeper/ai-bridge/pkg/ag-ui"
	ai "github.com/beeper/ai-bridge/pkg/ai"
	aistream "github.com/beeper/ai-bridge/pkg/ai-stream"
	"github.com/beeper/ai-bridge/pkg/aiid"
)

func TestParseAISlashCommand(t *testing.T) {
	tests := []struct {
		body string
		name string
		arg  string
		ok   bool
	}{
		{body: "/model gpt-5", name: "model", arg: "gpt-5", ok: true},
		{body: "/model", name: "model", ok: true},
		{body: " /reasoning high ", name: "reasoning", arg: "high", ok: true},
		{body: "/reasoning", name: "reasoning", ok: true},
		{body: "/reasoning-mode adaptive", name: "reasoning-mode", arg: "adaptive", ok: true},
		{body: "/reasoning-mode", name: "reasoning-mode", ok: true},
		{body: "/reasoniing low", ok: false},
		{body: "/system-prompt be terse", name: "system-prompt", arg: "be terse", ok: true},
		{body: "/system-prompt", name: "system-prompt", ok: true},
		{body: "/help model", name: "help", arg: "model", ok: true},
		{body: "/compact focus on decisions", name: "compact", arg: "focus on decisions", ok: true},
		{body: "/abort", name: "abort", ok: true},
		{body: "/stop", name: "abort", ok: true},
		{body: "/session", name: "session", ok: true},
		{body: "/limits", name: "limits", ok: true},
		{body: "/approve approval-1 always", name: "approve", arg: "approval-1 always", ok: true},
		{body: "/reset-approvals", name: "reset-approvals", ok: true},
		{body: "/unknown nope", ok: false},
		{body: "hello /model gpt-5", ok: false},
	}
	for _, tt := range tests {
		got, ok := parseAISlashCommand(tt.body)
		if ok != tt.ok {
			t.Fatalf("%q ok=%v, want %v", tt.body, ok, tt.ok)
		}
		if !ok {
			continue
		}
		if got.Name != tt.name || got.Arg != tt.arg {
			t.Fatalf("%q parsed as %#v, want name=%q arg=%q", tt.body, got, tt.name, tt.arg)
		}
	}
}

func TestParseAICommandMessage(t *testing.T) {
	tests := []struct {
		name    string
		content *event.MessageEventContent
		want    aiSlashCommand
		ok      bool
	}{
		{
			name:    "visible slash command",
			content: &event.MessageEventContent{MsgType: event.MsgText, Body: "/model gpt-5"},
			want:    aiSlashCommand{Name: "model", Arg: "gpt-5"},
			ok:      true,
		},
		{
			name:    "hidden slash command",
			content: &event.MessageEventContent{MsgType: matrixCommandMsgType, Body: "/abort"},
			want:    aiSlashCommand{Name: "abort"},
			ok:      true,
		},
		{
			name:    "hidden bridge-prefixed command",
			content: &event.MessageEventContent{MsgType: matrixCommandMsgType, Body: "!ai stop"},
			want:    aiSlashCommand{Name: "abort"},
			ok:      true,
		},
		{
			name:    "hidden bridge-prefixed command with args",
			content: &event.MessageEventContent{MsgType: matrixCommandMsgType, Body: "!ai model gpt-5"},
			want:    aiSlashCommand{Name: "model", Arg: "gpt-5"},
			ok:      true,
		},
		{
			name:    "hidden bare command ignored",
			content: &event.MessageEventContent{MsgType: matrixCommandMsgType, Body: "abort"},
			ok:      false,
		},
		{
			name:    "visible bridge-prefixed command ignored",
			content: &event.MessageEventContent{MsgType: event.MsgText, Body: "!ai stop"},
			ok:      false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := parseAICommandMessage(tt.content)
			if ok != tt.ok {
				t.Fatalf("ok=%v, want %v", ok, tt.ok)
			}
			if ok && got != tt.want {
				t.Fatalf("parsed %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestCanonicalAICommandNameAliases(t *testing.T) {
	tests := map[string]string{
		"abort":   "abort",
		" stop ":  "abort",
		"ai-help": "help",
		"MODEL":   "model",
	}
	for input, want := range tests {
		if got := canonicalAICommandName(input); got != want {
			t.Fatalf("canonicalAICommandName(%q)=%q, want %q", input, got, want)
		}
	}
}

func TestParseApprovalCommand(t *testing.T) {
	response, err := aistream.ParseApprovalCommand("approval-1 always", aistream.DefaultApprovalChoices(), func() time.Time {
		return time.Unix(10, 0)
	})
	if err != nil || !response.Approved || !response.Always || response.Choice != aistream.ApprovalChoiceAlwaysApprove {
		t.Fatalf("always approval response = %#v err=%v", response, err)
	}
	response, err = aistream.ParseApprovalCommand("approval-1 deny", aistream.DefaultApprovalChoices(), func() time.Time {
		return time.Unix(10, 0)
	})
	if err != nil || response.Approved || response.Reason != "denied" {
		t.Fatalf("deny approval response = %#v err=%v", response, err)
	}
}

func TestAISlashCommandHelpCatalogUsesDefinitions(t *testing.T) {
	help := aiSlashCommandHelp("")
	seen := map[string]bool{}
	for _, def := range aiSlashCommandDefinitions() {
		if def.name == "" {
			t.Fatal("registered command has empty name")
		}
		if seen[def.name] {
			t.Fatalf("registered command %q more than once", def.name)
		}
		seen[def.name] = true
		if def.run == nil {
			t.Fatalf("registered command %q has no handler", def.name)
		}
		if !strings.Contains(help, "`"+def.usage+"`") {
			t.Fatalf("help catalog is missing usage %q:\n%s", def.usage, help)
		}
		if !strings.Contains(help, def.description) {
			t.Fatalf("help catalog is missing description %q:\n%s", def.description, help)
		}
		if _, ok := parseAISlashCommand(def.usage); !ok {
			t.Fatalf("registered command usage %q is not parseable", def.usage)
		}
	}
}

func TestAISlashCommandHelpForSpecificCommand(t *testing.T) {
	help := aiSlashCommandHelp("/model")
	if !strings.Contains(help, "Usage: /model [model]") {
		t.Fatalf("specific help is missing model usage:\n%s", help)
	}
	if strings.Contains(help, "/reasoning") {
		t.Fatalf("specific help included the full catalog:\n%s", help)
	}

	help = aiSlashCommandHelp("/limits")
	if strings.Contains(strings.ToLower(help), "full") {
		t.Fatalf("limits help advertised full debugging mode:\n%s", help)
	}
}

func TestCurrentCommandResponseText(t *testing.T) {
	if got := displayReasoningLevel(""); got != "off" {
		t.Fatalf("empty reasoning level = %q, want off", got)
	}
	model := ai.Model{ID: "anthropic/claude-opus-4.5", Reasoning: true}
	status := reasoningStatusText("", "beeper/anthropic/claude-opus-4.5", model)
	if !strings.Contains(status, "beeper/anthropic/claude-opus-4.5's reasoning is set to `off`.") {
		t.Fatalf("reasoning status is missing current value:\n%s", status)
	}
	if !strings.Contains(status, "Available settings: `off`, `minimal`, `low`, `medium`, `high`.") {
		t.Fatalf("reasoning status is missing supported options:\n%s", status)
	}
	geminiStatus := reasoningStatusText("off", "beeper/google/gemini-3-pro-image-preview", ai.Model{
		ID:        "google/gemini-3-pro-image-preview",
		Name:      "Gemini 3 Pro Image Preview",
		Reasoning: true,
	})
	if geminiStatus != "Gemini 3 Pro Image Preview's reasoning is set to `off`. Available settings: `off`, `minimal`, `low`, `medium`, `high`." {
		t.Fatalf("unexpected Gemini reasoning status:\n%s", geminiStatus)
	}
	unsupportedStatus := reasoningStatusText("off", "beeper/meta-llama/llama-3.3-70b-instruct", ai.Model{ID: "meta-llama/llama-3.3-70b-instruct", Name: "Llama 3.3 70B"})
	if unsupportedStatus != "Llama 3.3 70B doesn't support reasoning." {
		t.Fatalf("unexpected unsupported reasoning status:\n%s", unsupportedStatus)
	}
	fixedStatus := reasoningStatusText("low", "beeper/minimax/minimax-m2.7", ai.Model{
		ID:        "minimax/minimax-m2.7",
		Name:      "MiniMax M2.7",
		Reasoning: true,
		ThinkingLevelMap: map[ai.ModelThinkingLevel]*string{
			ai.ModelThinkingLevelOff:     nil,
			ai.ModelThinkingLevelMinimal: nil,
			ai.ModelThinkingLevelMedium:  nil,
			ai.ModelThinkingLevelHigh:    nil,
		},
	})
	if fixedStatus != "MiniMax M2.7's reasoning is set to `low` and it doesn't support changing reasoning settings." {
		t.Fatalf("unexpected fixed reasoning status:\n%s", fixedStatus)
	}
	modeStatus := reasoningModeStatusText("adaptive", "beeper/anthropic/claude-opus-4.8", ai.Model{ID: "anthropic/claude-opus-4.8", ReasoningMode: ai.ModelReasoningModeAdaptive})
	if !strings.Contains(modeStatus, "beeper/anthropic/claude-opus-4.8's reasoning mode is set to `adaptive`.") {
		t.Fatalf("reasoning mode status is missing current value:\n%s", modeStatus)
	}
	if !strings.Contains(modeStatus, "Available modes: `default`, `adaptive`.") {
		t.Fatalf("reasoning mode status is missing supported options:\n%s", modeStatus)
	}
	modelStatus := canonicalTestClient().modelStatusText("beeper/gpt-5.5", "off", "", aiid.ProviderConfig{
		ID:     "beeper",
		Models: []ai.Model{{ID: "gpt-5.5"}, {ID: "openai/gpt-5.5"}},
	})
	if !strings.Contains(modelStatus, "Current model: `beeper/gpt-5.5`. Reasoning: `off`.") {
		t.Fatalf("model status is missing current value:\n%s", modelStatus)
	}
	if !strings.Contains(modelStatus, "Available models: `beeper/gpt-5.5`, `beeper/openai/gpt-5.5`.") {
		t.Fatalf("model status is missing available options:\n%s", modelStatus)
	}
	if got := currentSystemPromptText(RoomConfig{}); got != "No additional system prompt is set." {
		t.Fatalf("empty system prompt text = %q", got)
	}
	promptStatus := systemPromptStatusText(RoomConfig{})
	if !strings.Contains(promptStatus, "Options: `/system-prompt <prompt>`, `/system-prompt clear`.") {
		t.Fatalf("system prompt status is missing options:\n%s", promptStatus)
	}
	prompt := currentSystemPromptText(RoomConfig{AdditionalPrompt: "be terse"})
	if !strings.Contains(prompt, "Current system prompt:") || !strings.Contains(prompt, "```\nbe terse\n```") {
		t.Fatalf("unexpected current prompt text:\n%s", prompt)
	}
}

func TestCommandResponseContentIsVisibleText(t *testing.T) {
	content := commandResponseContent(aiSlashCommandHelp(""))
	if content.MsgType != event.MsgText {
		t.Fatalf("command response msgtype=%s, want %s", content.MsgType, event.MsgText)
	}
	if content.Format != event.FormatHTML {
		t.Fatalf("command response format=%s, want %s", content.Format, event.FormatHTML)
	}
	if !strings.Contains(content.Body, "AI Bridge commands:") {
		t.Fatalf("command response body did not include help catalog:\n%s", content.Body)
	}
	if !strings.Contains(content.FormattedBody, "<code>/help [command]</code>") {
		t.Fatalf("command response formatted body did not render command usage as HTML:\n%s", content.FormattedBody)
	}
}

func TestCommandResponseContentRendersMarkdownTables(t *testing.T) {
	content := commandResponseContent("| One | Two |\n| --- | ---: |\n| A | `1` |\n")
	if content.Format != event.FormatHTML || !strings.Contains(content.FormattedBody, "<table>") {
		t.Fatalf("command response did not render markdown table as Matrix HTML: %#v", content)
	}
	if !strings.Contains(content.FormattedBody, "<code>1</code>") {
		t.Fatalf("command response table did not preserve markdown cell formatting:\n%s", content.FormattedBody)
	}
}

func TestCommandFinalAICarriesMarkdownAsFinalAssistantMessage(t *testing.T) {
	text := "AI limits\n\n## Models\n\n| Window | Left |\n| --- | ---: |\n| Daily | `75%` |\n"
	payload := commandFinalAI(text, "message-1", "thread-1", "beeper/gpt-5", "ai", "AI", time.Unix(10, 0))
	if payload.Schema != aistream.BeeperAISchema || payload.Protocol != "ag-ui" || payload.Kind != aistream.AIKindFinal {
		t.Fatalf("unexpected command AI envelope: %#v", payload)
	}
	if payload.Message == nil || payload.Message.Role != agui.RoleAssistant || payload.Message.ID != "message-1" {
		t.Fatalf("unexpected command AI message: %#v", payload.Message)
	}
	if len(payload.Message.Parts) != 1 || payload.Message.Parts[0]["type"] != "text" || payload.Message.Parts[0]["content"] != text {
		t.Fatalf("command AI message did not preserve markdown text part: %#v", payload.Message.Parts)
	}
	if len(payload.Events) != 1 || payload.Events[0].Event.Type() != agui.EventRunFinished {
		t.Fatalf("command AI final payload missing terminal event: %#v", payload.Events)
	}
}

func TestProviderCommandTextDoesNotExposeSecrets(t *testing.T) {
	provider := aiid.ProviderConfig{
		ID:           "custom",
		DisplayName:  "Custom",
		API:          ai.ApiOpenAIResponses,
		Provider:     "custom",
		BaseURL:      "https://example.test/v1",
		APIKey:       "secret-key",
		RefreshToken: "refresh-secret",
		Headers:      map[string]string{"Authorization": "Bearer header-secret"},
		DefaultModel: "model-a",
		Models:       []ai.Model{{ID: "model-a"}},
	}
	text := providerText(providerResponse(provider))
	for _, secret := range []string{"secret-key", "refresh-secret", "header-secret"} {
		if strings.Contains(text, secret) {
			t.Fatalf("provider command text leaked %q:\n%s", secret, text)
		}
	}
	if !strings.Contains(text, "Provider `custom`") || !strings.Contains(text, "Default model: `model-a`") {
		t.Fatalf("provider command text lost public fields:\n%s", text)
	}
}

func TestParseBridgeProviderArgsAllowsQuotedTokens(t *testing.T) {
	fields, err := parseBridgeProviderArgs(`add custom openai-responses https://example.test/v1 "secret key" "model with spaces"`)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"add", "custom", "openai-responses", "https://example.test/v1", "secret key", "model with spaces"}
	if len(fields) != len(want) {
		t.Fatalf("field count=%d, want %d: %#v", len(fields), len(want), fields)
	}
	for i := range want {
		if fields[i] != want[i] {
			t.Fatalf("field %d=%q, want %q in %#v", i, fields[i], want[i], fields)
		}
	}
}

func TestProviderCommandMayContainSecret(t *testing.T) {
	if !providerCommandMayContainSecret(`update custom openai-responses "unterminated`) {
		t.Fatal("expected update command to be treated as possibly sensitive")
	}
	if providerCommandMayContainSecret(`show custom`) {
		t.Fatal("show command should not be treated as sensitive")
	}
}

func TestCommandRejectedErrorSendsFailedStatusNoticeWithExactMessage(t *testing.T) {
	text := `AI room settings rejected: reasoning level "invalidvalue" is invalid`
	err := commandRejectedError(text)
	var status bridgev2.MessageStatus
	if !errors.As(err, &status) {
		t.Fatalf("commandRejectedError did not return message status: %T", err)
	}
	if status.Status != event.MessageStatusFail {
		t.Fatalf("status=%s, want %s", status.Status, event.MessageStatusFail)
	}
	if status.ErrorReason != event.MessageStatusUnsupported {
		t.Fatalf("reason=%s, want %s", status.ErrorReason, event.MessageStatusUnsupported)
	}
	if !status.SendNotice || !status.IsCertain || !status.ErrorAsMessage {
		t.Fatalf("status flags not set for visible exact notice: %#v", status)
	}
	info := &bridgev2.MessageStatusEventInfo{
		RoomID:        "!room:example.com",
		SourceEventID: "$event",
		EventType:     event.EventMessage,
	}
	mss := status.ToMSSEvent(info)
	if mss.Message != text || mss.InternalError != text {
		t.Fatalf("message status did not expose exact error: message=%q internal=%q", mss.Message, mss.InternalError)
	}
	notice := status.ToNoticeEvent(info)
	if notice.MsgType != event.MsgNotice || !strings.Contains(notice.Body, text) {
		t.Fatalf("notice did not expose exact error: %#v", notice)
	}
}

func TestSessionCommandStatsFromEntries(t *testing.T) {
	stats, err := sessionCommandStatsFromEntries([]json.RawMessage{
		rawSessionEntry(t, map[string]any{"type": "message", "message": map[string]any{"role": "user"}}),
		rawSessionEntry(t, map[string]any{"type": "message", "message": map[string]any{"role": "assistant"}}),
		rawSessionEntry(t, map[string]any{"type": "message", "message": map[string]any{"role": "toolResult"}}),
		rawSessionEntry(t, map[string]any{"type": "compaction"}),
		rawSessionEntry(t, map[string]any{"type": "model_change"}),
	})
	if err != nil {
		t.Fatal(err)
	}
	if stats.TotalEntries != 5 || stats.Messages != 3 || stats.UserMessages != 1 || stats.AssistantMessages != 1 || stats.ToolResultMessages != 1 || stats.Compactions != 1 {
		t.Fatalf("unexpected session stats: %#v", stats)
	}
}

func TestFormatSessionCommandInfo(t *testing.T) {
	text := formatSessionCommandInfo(sessionCommandInfo{
		SessionID:         "session-1",
		CreatedAt:         "2026-05-30T00:00:00Z",
		RoomProvider:      "beeper",
		RoomModel:         "beeper/gpt-5.5",
		RoomReasoning:     "off",
		SystemPrompt:      true,
		Responding:        true,
		LastKnownTimezone: "Europe/Amsterdam",
		Stats: sessionCommandStats{
			TotalEntries:       4,
			Messages:           3,
			UserMessages:       1,
			AssistantMessages:  1,
			ToolResultMessages: 1,
			Compactions:        1,
		},
	})
	for _, want := range []string{
		"Status: `responding`",
		"ID: `session-1`",
		"Room model: `beeper/gpt-5.5`",
		"Last known timezone: `Europe/Amsterdam`",
		"System prompt: `yes, 0 chars`",
		"Messages: `3` total, `1` user, `1` assistant, `1` tool results",
		"Compactions: `1`",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("session info missing %q:\n%s", want, text)
		}
	}

	text = formatSessionCommandInfo(sessionCommandInfo{RoomProvider: "beeper", RoomModel: "beeper/gpt-5.5", RoomReasoning: "off"})
	if !strings.Contains(text, "No AI session has been started in this room yet.") {
		t.Fatalf("empty session info missing no-session text:\n%s", text)
	}
}

func TestAIServicesLimitsURLUsesBaseURL(t *testing.T) {
	tests := map[string]string{
		"https://ai-services.beeper.com":      "https://ai-services.beeper.com/limits",
		"https://ai-services.beeper.com/":     "https://ai-services.beeper.com/limits",
		"https://ai-services.beeper.com/dev":  "https://ai-services.beeper.com/dev/limits",
		"https://ai-services.beeper.com/dev/": "https://ai-services.beeper.com/dev/limits",
	}
	for input, want := range tests {
		got, err := aiServicesLimitsURL(input)
		if err != nil {
			t.Fatalf("aiServicesLimitsURL(%q) returned error: %v", input, err)
		}
		if got != want {
			t.Fatalf("aiServicesLimitsURL(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestFetchAIServicesLimitsUsesAppserviceBearerToken(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/limits" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		auth := r.Header.Get("Authorization")
		payload, ok := strings.CutPrefix(auth, "Bearer "+aiServicesAppserviceTokenPrefix)
		if !ok {
			t.Fatalf("expected appservice bearer token, got %q", auth)
		}
		decoded, err := base64.RawURLEncoding.DecodeString(payload)
		if err != nil {
			t.Fatal(err)
		}
		var token aiServicesAppserviceToken
		if err = json.Unmarshal(decoded, &token); err != nil {
			t.Fatal(err)
		}
		if token.ASToken != "as-token" || token.Username != "alice" {
			t.Fatalf("unexpected appservice token %#v", token)
		}
		_, _ = w.Write([]byte(`{"windows":{"llm":{"day":{"percentage_left":90,"limit":1000,"used":100,"remaining":900,"reset_at":1893456000000},"week":{"percentage_left":100,"limit":7000,"used":0,"remaining":7000,"reset_at":1893974400000},"month":{"percentage_left":100,"limit":30000,"used":0,"remaining":30000,"reset_at":1896134400000}}}}`))
	}))
	defer server.Close()

	provider := aiid.ProviderConfig{ID: aiid.DefaultProvider, BaseURL: server.URL}
	client := &Client{
		Main: &Connector{AppServiceToken: "as-token"},
		UserLogin: &bridgev2.UserLogin{UserLogin: &database.UserLogin{
			UserMXID: "@alice:beeper.test",
			Metadata: &aiid.UserLoginMetadata{Providers: map[string]aiid.ProviderConfig{
				provider.ID: provider,
			}},
		}},
	}
	limits, err := client.fetchAIServicesLimits(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if limits.Windows.LLM.Day.Limit != 1000 || limits.Windows.LLM.Day.Used != 100 {
		t.Fatalf("unexpected limits %#v", limits.Windows.LLM.Day)
	}
}

func TestRunLimitsCommandFullUsesAIResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/limits" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"windows":{"llm":{"day":{"percentage_left":75,"limit":1000,"used":250,"remaining":750,"reset_at":1893456000000}}}}`))
	}))
	defer server.Close()

	provider := aiid.ProviderConfig{ID: aiid.DefaultProvider, BaseURL: server.URL}
	client := &Client{
		Main: &Connector{AppServiceToken: "as-token"},
		UserLogin: &bridgev2.UserLogin{UserLogin: &database.UserLogin{
			UserMXID: "@alice:beeper.test",
			Metadata: &aiid.UserLoginMetadata{Providers: map[string]aiid.ProviderConfig{
				provider.ID: provider,
			}},
		}},
	}
	responder := &recordingCommandResponder{}
	if err := runLimitsCommand(client, context.Background(), nil, RoomConfig{}, "full", responder); err != nil {
		t.Fatal(err)
	}
	if responder.text != "" {
		t.Fatalf("full limits should use AI response, got plain text %q", responder.text)
	}
	if !strings.Contains(responder.aiText, "## LLM tokens") || !strings.Contains(responder.aiText, "| Day | `75%` | `250` | `1,000` | `750` |") {
		t.Fatalf("full limits AI response missing table:\n%s", responder.aiText)
	}
}

func TestRunLimitsCommandRejectsRawArgument(t *testing.T) {
	err := runLimitsCommand(nil, context.Background(), nil, RoomConfig{}, "raw", &recordingCommandResponder{})
	if err == nil || err.Error() != "Usage: /limits" {
		t.Fatalf("raw argument error = %v, want Usage: /limits", err)
	}
}

type recordingCommandResponder struct {
	text   string
	aiText string
}

func (r *recordingCommandResponder) Reply(_ context.Context, text string) error {
	r.text = text
	return nil
}

func (r *recordingCommandResponder) ReplyAI(_ context.Context, text string) error {
	r.aiText = text
	return nil
}

func TestBeeperUsageLimitErrorUsesPlanResetMessage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/limits" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"windows":{"llm":{"day":{"percentage_left":0,"limit":1000,"used":1000,"remaining":0,"reset_at":1893457800000},"week":{"percentage_left":0,"limit":7000,"used":7000,"remaining":0,"reset_at":1893978000000},"month":{"percentage_left":75,"limit":30000,"used":7500,"remaining":22500,"reset_at":1896134400000}}}}`))
	}))
	defer server.Close()

	provider := aiid.ProviderConfig{ID: aiid.DefaultProvider, BaseURL: server.URL}
	client := &Client{
		Main: &Connector{AppServiceToken: "as-token"},
		UserLogin: &bridgev2.UserLogin{UserLogin: &database.UserLogin{
			UserMXID: "@alice:beeper.test",
			Metadata: &aiid.UserLoginMetadata{Providers: map[string]aiid.ProviderConfig{
				provider.ID: provider,
			}},
		}},
	}

	message := client.visibleProviderErrorMessage(
		context.Background(),
		provider,
		"OpenAI API error (429): AI token limit exceeded. Check /limits",
	)
	for _, want := range []string{
		"This message exceeds the AI usage limits in your plan.",
		"Your limits will reset in ",
		"You can see details by typing `/limits`",
	} {
		if !strings.Contains(message, want) {
			t.Fatalf("message = %q, want to contain %q", message, want)
		}
	}
	if strings.Contains(message, "OpenAI") {
		t.Fatalf("message leaked provider name: %q", message)
	}
}

func TestNonBeeperUsageLimitErrorKeepsProviderMessage(t *testing.T) {
	client := &Client{}
	provider := aiid.ProviderConfig{ID: "openai"}
	message := "OpenAI API error (429): AI token limit exceeded. Check /limits"
	if got := client.visibleProviderErrorMessage(context.Background(), provider, message); got != message {
		t.Fatalf("message = %q, want provider message %q", got, message)
	}
}

func TestFormatLimitsCommandInfo(t *testing.T) {
	now := time.Date(2029, 12, 31, 0, 0, 0, 0, time.UTC)
	reset := now.Add(26*time.Hour + 3*time.Minute).UnixMilli()
	text := formatLimitsCommandInfo(aiServicesLimitsResponse{Windows: aiServicesLimitCategories{
		LLM: aiServicesLimitWindows{
			Day:   aiServicesLimitWindow{PercentageLeft: 75, Limit: 1000, Used: 250, Remaining: 750, ResetAtMS: reset},
			Week:  aiServicesLimitWindow{PercentageLeft: 100, Limit: -1, Used: 1234, Remaining: -1, ResetAtMS: reset},
			Month: aiServicesLimitWindow{PercentageLeft: 0, Limit: 30000, Used: 30500, Remaining: -500, ResetAtMS: reset},
		},
		WebTools: aiServicesLimitWindows{
			Day:   aiServicesLimitWindow{PercentageLeft: 99, Limit: 200000, Used: 1, Remaining: 199999, ResetAtMS: reset},
			Week:  aiServicesLimitWindow{PercentageLeft: 100, Limit: 1000000, Used: 0, Remaining: 1000000, ResetAtMS: reset},
			Month: aiServicesLimitWindow{PercentageLeft: 100, Limit: 4000000, Used: 0, Remaining: 4000000, ResetAtMS: reset},
		},
		AudioTranscriptions: aiServicesLimitWindows{
			Day:   aiServicesLimitWindow{PercentageLeft: 99, Limit: 86400, Used: 43, Remaining: 86357, ResetAtMS: reset},
			Week:  aiServicesLimitWindow{PercentageLeft: 100, Limit: 302400, Used: 43, Remaining: 302357, ResetAtMS: reset},
			Month: aiServicesLimitWindow{PercentageLeft: 100, Limit: 600000, Used: 43, Remaining: 599957, ResetAtMS: reset},
		},
		AudioGeneration: aiServicesLimitWindows{
			Day:   aiServicesLimitWindow{PercentageLeft: 99, Limit: 50000, Used: 1234, Remaining: 48766, ResetAtMS: reset},
			Week:  aiServicesLimitWindow{PercentageLeft: 100, Limit: 200000, Used: 1234, Remaining: 198766, ResetAtMS: reset},
			Month: aiServicesLimitWindow{PercentageLeft: 100, Limit: 500000, Used: 1234, Remaining: 498766, ResetAtMS: reset},
		},
	}}, now)
	for _, want := range []string{
		"# AI limits",
		"| Window | Left | Reset |",
		"| Daily | `75%` | in 1 day 2 hours 3 minutes |",
		"| Weekly | Unlimited | in 1 day 2 hours 3 minutes |",
		"| Monthly | **Out** | in 1 day 2 hours 3 minutes |",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("limits info missing %q:\n%s", want, text)
		}
	}
	for _, notWant := range []string{"## Models", "## Web Search", "## Transcription", "## Audio Generation", "Used", "250 / 1,000", "2030-01-01T00:00:00Z"} {
		if strings.Contains(text, notWant) {
			t.Fatalf("limits info exposed non-summary value %q:\n%s", notWant, text)
		}
	}
}

func TestFormatLimitsCommandInfoShowsPerWindowResetsWhenDifferent(t *testing.T) {
	now := time.Date(2029, 12, 31, 0, 0, 0, 0, time.UTC)
	text := formatLimitsCommandInfo(aiServicesLimitsResponse{Windows: aiServicesLimitCategories{
		LLM: aiServicesLimitWindows{
			Day:   aiServicesLimitWindow{PercentageLeft: 75, ResetAtMS: now.Add(25*time.Hour + 3*time.Minute).UnixMilli()},
			Week:  aiServicesLimitWindow{PercentageLeft: 100, ResetAtMS: now.Add(7 * 24 * time.Hour).UnixMilli()},
			Month: aiServicesLimitWindow{PercentageLeft: 0, ResetAtMS: now.Add(31 * 24 * time.Hour).UnixMilli()},
		},
	}}, now)
	for _, want := range []string{
		"# AI limits",
		"| Daily | `75%` | in 1 day 1 hour 3 minutes |",
		"| Weekly | `100%` | in 7 days |",
		"| Monthly | **Out** | in 31 days |",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("limits info missing %q:\n%s", want, text)
		}
	}
	for _, notWant := range []string{"## Models", "## Web Search", "No limits reported.", "Everything resets", "Not reported"} {
		if strings.Contains(text, notWant) {
			t.Fatalf("limits info exposed %q:\n%s", notWant, text)
		}
	}
}

func TestFormatResetInDoesNotRoundUp(t *testing.T) {
	now := time.Date(2029, 12, 31, 0, 0, 0, 0, time.UTC)
	tests := map[time.Duration]string{
		45 * time.Second: "less than 1 minute",
		23*time.Hour + 59*time.Minute + 59*time.Second: "23 hours 59 minutes",
		24*time.Hour + 59*time.Minute:                  "1 day 59 minutes",
		26*time.Hour + 3*time.Minute:                   "1 day 2 hours 3 minutes",
	}
	for duration, want := range tests {
		if got := formatResetIn(now.Add(duration), now); got != want {
			t.Fatalf("formatResetIn(%s) = %q, want %q", duration, got, want)
		}
	}
}

func TestFormatFullLimitsCommandInfoShowsExactUsage(t *testing.T) {
	resetAt := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	text := formatFullLimitsCommandInfo(aiServicesLimitsResponse{Windows: aiServicesLimitCategories{
		LLM: aiServicesLimitWindows{
			Day:   aiServicesLimitWindow{PercentageLeft: 75, Limit: 1000, Used: 250, Remaining: 750, ResetAtMS: resetAt.UnixMilli()},
			Week:  aiServicesLimitWindow{PercentageLeft: 100, Limit: -1, Used: 1234, Remaining: -1, ResetAtMS: resetAt.UnixMilli()},
			Month: aiServicesLimitWindow{PercentageLeft: 0, Limit: 30000, Used: 30000, Remaining: 0, ResetAtMS: resetAt.UnixMilli()},
		},
		AudioTranscriptions: aiServicesLimitWindows{
			Day:   aiServicesLimitWindow{PercentageLeft: 99, Limit: 86400, Used: 43, Remaining: 86357, ResetAtMS: resetAt.UnixMilli()},
			Week:  aiServicesLimitWindow{PercentageLeft: 100, Limit: 302400, Used: 43, Remaining: 302357, ResetAtMS: resetAt.UnixMilli()},
			Month: aiServicesLimitWindow{PercentageLeft: 100, Limit: 600000, Used: 43, Remaining: 599957, ResetAtMS: resetAt.UnixMilli()},
		},
	}}, time.Date(2029, 12, 31, 0, 0, 0, 0, time.UTC))
	for _, want := range []string{
		"## LLM tokens",
		"| Window | Left | Used | Limit | Remaining | Reset |",
		"| Day | `75%` | `250` | `1,000` | `750` | `1893456000000` (`2030-01-01T00:00:00Z`, in 1 day) |",
		"| Week | `100%` | `1,234` | `-1` | `-1` | `1893456000000` (`2030-01-01T00:00:00Z`, in 1 day) |",
		"## Web tools",
		"| Day | `0%` | `0` | `0` | `0` | unknown |",
		"## Audio transcription seconds",
		"| Day | `99%` | `43` | `86,400` | `86,357` | `1893456000000` (`2030-01-01T00:00:00Z`, in 1 day) |",
		"## Audio generation characters",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("full limits info missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "AI limits full:") {
		t.Fatalf("full limits info should render as tables without the old header:\n%s", text)
	}
}

func TestResolveCanonicalRoomModelUsesDefaultProviderForBareModel(t *testing.T) {
	client := canonicalTestClient()
	_, model, canonical, err := client.resolveCanonicalRoomModel(context.Background(), RoomConfig{ModelID: "gpt-5.5"})
	if err != nil {
		t.Fatal(err)
	}
	if model.ID != "gpt-5.5" || canonical != "beeper/gpt-5.5" {
		t.Fatalf("unexpected canonical model %q %#v", canonical, model)
	}
}

func TestResolveCanonicalRoomModelMatchesBareModelByCatalogSuffixOrder(t *testing.T) {
	client := canonicalTestClient()
	meta := client.UserLogin.Metadata.(*aiid.UserLoginMetadata)
	provider := meta.Providers[aiid.DefaultProvider]
	provider.Models = []ai.Model{
		{ID: "anthropic/gpt-5.5", Name: "First GPT 5.5", Provider: ai.ProviderOpenRouter, API: ai.ApiOpenAIResponses},
		{ID: "openai/gpt-5.5", Name: "OpenAI GPT 5.5", Provider: ai.ProviderOpenAI, API: ai.ApiOpenAIResponses},
	}
	meta.Providers[provider.ID] = provider

	_, model, canonical, err := client.resolveCanonicalRoomModel(context.Background(), RoomConfig{ModelID: "gpt-5.5"})
	if err != nil {
		t.Fatal(err)
	}
	if model.ID != "anthropic/gpt-5.5" || canonical != "beeper/anthropic/gpt-5.5" {
		t.Fatalf("unexpected canonical model %q %#v", canonical, model)
	}
}

func TestResolveCanonicalRoomModelPreservesDefaultOpenAICatalogModel(t *testing.T) {
	client := canonicalTestClient()
	_, model, canonical, err := client.resolveCanonicalRoomModel(context.Background(), RoomConfig{ProviderID: "beeper", ModelID: "openai/gpt-5.5"})
	if err != nil {
		t.Fatal(err)
	}
	if model.ID != "openai/gpt-5.5" || canonical != "beeper/openai/gpt-5.5" {
		t.Fatalf("unexpected canonical model %q %#v", canonical, model)
	}
}

func TestResolveCanonicalRoomModelPreservesFullProviderModel(t *testing.T) {
	client := canonicalTestClient()
	openrouter := aiid.ProviderConfig{
		ID:           "openrouter",
		Provider:     ai.ProviderOpenRouter,
		API:          ai.ApiOpenAICompletions,
		DefaultModel: "openai/gpt-5",
		Models:       []ai.Model{{ID: "openai/gpt-5", Provider: ai.ProviderOpenRouter, API: ai.ApiOpenAICompletions}},
	}
	client.UserLogin.Metadata = &aiid.UserLoginMetadata{Providers: map[string]aiid.ProviderConfig{openrouter.ID: openrouter}}
	_, model, canonical, err := client.resolveCanonicalRoomModel(context.Background(), RoomConfig{ProviderID: "openrouter", ModelID: "openai/gpt-5"})
	if err != nil {
		t.Fatal(err)
	}
	if model.ID != "openai/gpt-5" || canonical != "openrouter/openai/gpt-5" {
		t.Fatalf("unexpected canonical model %q %#v", canonical, model)
	}
}

func TestRoomReasoningValidationSyntax(t *testing.T) {
	for _, level := range []string{"", "off", "minimal", "low", "medium", "high", "xhigh"} {
		if !validRoomReasoningLevel(level) {
			t.Fatalf("expected %q to be valid", level)
		}
	}
	for _, level := range []string{"xlow", "banana"} {
		if validRoomReasoningLevel(level) {
			t.Fatalf("expected %q to be invalid", level)
		}
	}
}

func canonicalTestClient() *Client {
	conn := &Connector{}
	conn.Config.ApplyDefaults()
	provider := aiid.ProviderConfig{
		ID:           "beeper",
		Provider:     ai.ProviderOpenAI,
		API:          ai.ApiOpenAIResponses,
		DefaultModel: "gpt-5.5",
		Models:       []ai.Model{{ID: "gpt-5.5", Provider: ai.ProviderOpenAI, API: ai.ApiOpenAIResponses}, {ID: "openai/gpt-5.5", Provider: ai.ProviderOpenAI, API: ai.ApiOpenAIResponses}},
	}
	login := &bridgev2.UserLogin{UserLogin: &database.UserLogin{
		ID:       "login",
		Metadata: &aiid.UserLoginMetadata{Providers: map[string]aiid.ProviderConfig{provider.ID: provider}},
	}}
	return &Client{Main: conn, UserLogin: login}
}

func rawSessionEntry(t *testing.T, entry map[string]any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(entry)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
