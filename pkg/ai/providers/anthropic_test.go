package providers

import (
	"testing"

	ai "github.com/beeper/ai-bridge/pkg/ai"
)

func TestAnthropicStreamStatePreservesToolInputFromContentBlockStart(t *testing.T) {
	stream := ai.NewAssistantMessageEventStream()
	model := ai.Model{ID: "claude-test", API: ai.ApiAnthropicMessages, Provider: ai.ProviderAnthropic}
	output := newAssistant(model)
	state := newAnthropicStreamState()

	state.apply(stream, &output, model, ai.Context{}, false, map[string]any{
		"type":  "content_block_start",
		"index": float64(0),
		"content_block": map[string]any{
			"type":  "tool_use",
			"id":    "toolu_1",
			"name":  "read",
			"input": map[string]any{"path": "README.md"},
		},
	})
	state.apply(stream, &output, model, ai.Context{}, false, map[string]any{
		"type":  "content_block_stop",
		"index": float64(0),
	})

	blocks := output.Content.([]ai.ContentBlock)
	if len(blocks) != 1 || blocks[0].Type != "toolCall" {
		t.Fatalf("expected one tool call block, got %#v", blocks)
	}
	if blocks[0].Arguments["path"] != "README.md" {
		t.Fatalf("expected tool input from content block start, got %#v", blocks[0].Arguments)
	}
}

func TestAnthropicBeeperProxyUsesBearerAuth(t *testing.T) {
	model := ai.Model{ID: "claude-test", API: ai.ApiAnthropicMessages, Provider: ai.ProviderAnthropic, BaseURL: "https://ai-services.beeper.localtest.me/proxy/anthropic"}
	headers := anthropicHeaders(model, ai.Context{}, AnthropicOptions{StreamOptions: ai.StreamOptions{APIKey: "matrix-token"}}, false)
	if headers["Authorization"] != "Bearer matrix-token" {
		t.Fatalf("expected bearer auth, got %#v", headers)
	}
	if _, ok := headers["X-Api-Key"]; ok {
		t.Fatalf("did not expect upstream Anthropic API key header for Beeper proxy: %#v", headers)
	}
}
