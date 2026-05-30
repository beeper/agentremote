package sessiontitle

import (
	"context"
	"strings"
	"testing"

	agent "github.com/beeper/ai-bridge/pkg/agent"
	ai "github.com/beeper/ai-bridge/pkg/ai"
)

func TestGenerateCleansAndLimitsTitle(t *testing.T) {
	messages := []agent.AgentMessage{
		{Role: "assistant", Content: "Conversation title generation task"},
		{Role: "user", Content: "build a Matrix AI bridge"},
		{Role: "assistant", Content: "I'll implement it"},
	}
	title, err := Generate(context.Background(), messages, Options{
		Model: ai.Model{ID: "gpt-5-mini"},
		StreamFn: func(ctx context.Context, model ai.Model, llmContext ai.Context, options ai.SimpleStreamOptions) *ai.AssistantMessageEventStream {
			if model.ID != "gpt-5-mini" {
				t.Fatalf("unexpected title model %q", model.ID)
			}
			if llmContext.SystemPrompt != SystemPrompt {
				t.Fatalf("unexpected system prompt %q", llmContext.SystemPrompt)
			}
			if len(llmContext.Messages) != 1 {
				t.Fatalf("expected one title prompt message, got %d", len(llmContext.Messages))
			}
			prompt := assistantText(llmContext.Messages[0])
			if prompt != "build a Matrix AI bridge" {
				t.Fatalf("unexpected title prompt %q", prompt)
			}
			if strings.Contains(prompt, "Conversation title generation task") {
				t.Fatalf("title prompt should be based only on the first user message")
			}
			if options.MaxTokens == nil || *options.MaxTokens != 64 {
				t.Fatalf("unexpected max tokens %#v", options.MaxTokens)
			}
			stream := ai.NewAssistantMessageEventStream()
			message := ai.Message{Role: "assistant", Content: []ai.ContentBlock{{Type: "text", Text: `Title: "Matrix AI Bridge Implementation."`}}, StopReason: ai.StopReasonStop}
			stream.Push(ai.AssistantMessageEvent{Type: "done", Message: &message})
			stream.End()
			return stream
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if title != "Matrix AI Bridge Implementation" {
		t.Fatalf("unexpected title %q", title)
	}
}

func TestCleanTitleSanitizesMarkdownLabelsAndLength(t *testing.T) {
	title := cleanTitle("```text\nSession Name: \"Matrix AI Bridge Implementation With Too Many Details For A Single Short Session Title\".\n```")
	if strings.HasPrefix(title, "Session Name:") || strings.Contains(title, "```") || strings.HasSuffix(title, ".") {
		t.Fatalf("title was not sanitized: %q", title)
	}
	if len([]rune(title)) > maxTitleChars {
		t.Fatalf("title was not truncated: %q", title)
	}
}
