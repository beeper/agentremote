package connector

import (
	"context"
	"strings"
	"testing"

	ai "github.com/beeper/ai-bridge/pkg/ai"
	"github.com/beeper/ai-bridge/pkg/aiid"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/event"
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
		{body: "/reasoniing low", ok: false},
		{body: "/system-prompt be terse", name: "system-prompt", arg: "be terse", ok: true},
		{body: "/system-prompt", name: "system-prompt", ok: true},
		{body: "/help model", name: "help", arg: "model", ok: true},
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
		if got.name != tt.name || got.arg != tt.arg {
			t.Fatalf("%q parsed as %#v, want name=%q arg=%q", tt.body, got, tt.name, tt.arg)
		}
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
}

func TestCurrentCommandResponseText(t *testing.T) {
	if got := displayReasoningLevel(""); got != "off" {
		t.Fatalf("empty reasoning level = %q, want off", got)
	}
	if got := currentSystemPromptText(RoomConfig{}); got != "No additional system prompt is set." {
		t.Fatalf("empty system prompt text = %q", got)
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
	login := &bridgev2.UserLogin{UserLogin: &database.UserLogin{
		ID: "login",
		Metadata: &aiid.UserLoginMetadata{
			Providers: map[string]aiid.ProviderConfig{
				"beeper": {
					ID:           "beeper",
					Provider:     ai.ProviderOpenAI,
					API:          ai.ApiOpenAIResponses,
					DefaultModel: "gpt-5.5",
					Models:       []ai.Model{{ID: "gpt-5.5", Provider: ai.ProviderOpenAI, API: ai.ApiOpenAIResponses}, {ID: "openai/gpt-5.5", Provider: ai.ProviderOpenAI, API: ai.ApiOpenAIResponses}},
				},
				"openrouter": {
					ID:           "openrouter",
					Provider:     ai.ProviderOpenRouter,
					API:          ai.ApiOpenAICompletions,
					DefaultModel: "openai/gpt-5",
					Models:       []ai.Model{{ID: "openai/gpt-5", Provider: ai.ProviderOpenRouter, API: ai.ApiOpenAICompletions}},
				},
			},
		},
	}}
	return &Client{Main: conn, UserLogin: login}
}
