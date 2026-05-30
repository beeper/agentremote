package providers

import (
	"testing"

	ai "github.com/beeper/ai-bridge/pkg/ai"
)

func TestCompletionsStreamStateKeepsContentIndexesSeparate(t *testing.T) {
	stream := ai.NewAssistantMessageEventStream()
	output := newAssistant(testStreamModel())
	state := newCompletionsStreamState()

	state.apply(stream, &output, testStreamModel(), map[string]any{
		"id": "chatcmpl_1",
		"choices": []any{map[string]any{"delta": map[string]any{
			"content": "hello",
		}}},
	})
	state.apply(stream, &output, testStreamModel(), map[string]any{
		"choices": []any{map[string]any{"delta": map[string]any{
			"tool_calls": []any{map[string]any{"index": float64(0), "id": "call_1", "function": map[string]any{"name": "read", "arguments": `{"path"`}}},
		}}},
	})
	state.apply(stream, &output, testStreamModel(), map[string]any{
		"choices": []any{map[string]any{"finish_reason": "tool_calls", "delta": map[string]any{
			"tool_calls": []any{map[string]any{"index": float64(0), "function": map[string]any{"arguments": `:"x"}`}}},
		}}},
	})

	if !state.hasFinishReason {
		t.Fatal("expected finish reason")
	}
	if len(state.blocks) != 2 {
		t.Fatalf("expected text and tool blocks, got %#v", state.blocks)
	}
	if state.blocks[0].Type != "text" || state.blocks[0].Text != "hello" {
		t.Fatalf("expected text block first, got %#v", state.blocks[0])
	}
	if state.blocks[1].Type != "toolCall" || state.blocks[1].ID != "call_1" || state.blocks[1].Name != "read" {
		t.Fatalf("expected tool block second, got %#v", state.blocks[1])
	}
	if state.blocks[1].Arguments["path"] != "x" {
		t.Fatalf("expected parsed tool args, got %#v", state.blocks[1].Arguments)
	}
	if output.StopReason != ai.StopReasonToolUse {
		t.Fatalf("expected toolUse stop reason, got %q", output.StopReason)
	}
}

func TestCompletionsStreamStateParsesUsageAndReasoning(t *testing.T) {
	stream := ai.NewAssistantMessageEventStream()
	model := testStreamModel()
	model.Cost = ai.ModelCost{Input: 1, Output: 2, CacheRead: 0.5, CacheWrite: 3}
	output := newAssistant(model)
	state := newCompletionsStreamState()

	state.apply(stream, &output, model, map[string]any{
		"usage": map[string]any{
			"prompt_tokens":     float64(10),
			"completion_tokens": float64(5),
			"prompt_tokens_details": map[string]any{
				"cached_tokens":      float64(3),
				"cache_write_tokens": float64(2),
			},
		},
		"choices": []any{map[string]any{"finish_reason": "stop", "delta": map[string]any{
			"reasoning_content": "thinking",
		}}},
	})
	if output.Usage.Input != 5 || output.Usage.Output != 5 || output.Usage.CacheRead != 3 || output.Usage.CacheWrite != 2 {
		t.Fatalf("unexpected usage: %#v", output.Usage)
	}
	if len(state.blocks) != 1 || state.blocks[0].Type != "thinking" || state.blocks[0].ThinkingSignature != "reasoning_content" {
		t.Fatalf("expected reasoning block, got %#v", state.blocks)
	}
}

func TestResponsesStreamStateFinalizesReasoningTextToolAndUsage(t *testing.T) {
	stream := ai.NewAssistantMessageEventStream()
	model := testStreamModel()
	model.API = ai.ApiOpenAIResponses
	model.Cost = ai.ModelCost{Input: 1, Output: 2, CacheRead: 0.5}
	output := newAssistant(model)
	state := newResponsesStreamState()

	state.apply(stream, &output, model, OpenAIResponsesOptions{ServiceTier: "priority"}, map[string]any{
		"type": "response.output_item.added",
		"item": map[string]any{"type": "reasoning", "id": "rs_1"},
	})
	state.apply(stream, &output, model, OpenAIResponsesOptions{}, map[string]any{"type": "response.reasoning_text.delta", "delta": "think"})
	state.apply(stream, &output, model, OpenAIResponsesOptions{}, map[string]any{
		"type": "response.output_item.done",
		"item": map[string]any{"type": "reasoning", "id": "rs_1", "content": []any{map[string]any{"text": "final think"}}},
	})
	state.apply(stream, &output, model, OpenAIResponsesOptions{}, map[string]any{
		"type": "response.output_item.added",
		"item": map[string]any{"type": "message", "id": "msg_1"},
	})
	state.apply(stream, &output, model, OpenAIResponsesOptions{}, map[string]any{
		"type": "response.content_part.added",
		"part": map[string]any{"type": "output_text"},
	})
	state.apply(stream, &output, model, OpenAIResponsesOptions{}, map[string]any{"type": "response.output_text.delta", "delta": "hello"})
	state.apply(stream, &output, model, OpenAIResponsesOptions{}, map[string]any{
		"type": "response.output_item.done",
		"item": map[string]any{"type": "message", "id": "msg_1", "content": []any{map[string]any{"type": "output_text", "text": "hello"}}},
	})
	state.apply(stream, &output, model, OpenAIResponsesOptions{}, map[string]any{
		"type": "response.output_item.added",
		"item": map[string]any{"type": "function_call", "id": "fc_1", "call_id": "call_1", "name": "read", "arguments": ""},
	})
	state.apply(stream, &output, model, OpenAIResponsesOptions{}, map[string]any{"type": "response.function_call_arguments.delta", "delta": `{"path":"x"`})
	state.apply(stream, &output, model, OpenAIResponsesOptions{}, map[string]any{"type": "response.function_call_arguments.done", "arguments": `{"path":"x"}`})
	state.apply(stream, &output, model, OpenAIResponsesOptions{}, map[string]any{
		"type": "response.output_item.done",
		"item": map[string]any{"type": "function_call", "id": "fc_1", "call_id": "call_1", "name": "read", "arguments": `{"path":"x"}`},
	})
	state.apply(stream, &output, model, OpenAIResponsesOptions{ServiceTier: "priority"}, map[string]any{
		"type": "response.completed",
		"response": map[string]any{
			"id":           "resp_1",
			"status":       "completed",
			"service_tier": "flex",
			"usage": map[string]any{
				"input_tokens":  float64(10),
				"output_tokens": float64(5),
				"total_tokens":  float64(15),
				"input_tokens_details": map[string]any{
					"cached_tokens": float64(3),
				},
				"output_tokens_details": map[string]any{
					"reasoning_tokens": float64(4),
				},
			},
		},
	})

	if len(state.blocks) != 3 {
		t.Fatalf("expected three blocks, got %#v", state.blocks)
	}
	if state.blocks[0].Thinking != "final think" || state.blocks[0].ThinkingSignature == "" {
		t.Fatalf("expected finalized reasoning block, got %#v", state.blocks[0])
	}
	if state.blocks[1].Text != "hello" || state.blocks[1].TextSignature == "" {
		t.Fatalf("expected finalized text block, got %#v", state.blocks[1])
	}
	if state.blocks[2].Arguments["path"] != "x" {
		t.Fatalf("expected finalized tool args, got %#v", state.blocks[2])
	}
	if output.StopReason != ai.StopReasonToolUse {
		t.Fatalf("expected toolUse because response has tool call, got %q", output.StopReason)
	}
	if output.Usage.Input != 7 || output.Usage.CacheRead != 3 || output.Usage.Output != 5 || output.Usage.ReasoningTokens != 4 {
		t.Fatalf("unexpected usage: %#v", output.Usage)
	}
	if output.Usage.Cost.Total != 0.00000925 {
		t.Fatalf("expected response service-tier adjusted cost, got %#v", output.Usage.Cost)
	}
	if state.blocks[1].TextSignature != `{"id":"msg_1","v":1}` && state.blocks[1].TextSignature != `{"v":1,"id":"msg_1"}` {
		t.Fatalf("expected text signature without null phase, got %q", state.blocks[1].TextSignature)
	}
}

func TestResponsesStreamStateStreamsTextDeltasWithoutContentPartPrelude(t *testing.T) {
	stream := ai.NewAssistantMessageEventStream()
	model := testStreamModel()
	model.API = ai.ApiOpenAIResponses
	output := newAssistant(model)
	state := newResponsesStreamState()

	state.apply(stream, &output, model, OpenAIResponsesOptions{}, map[string]any{
		"type": "response.output_item.added",
		"item": map[string]any{"type": "message", "id": "msg_1"},
	})
	state.apply(stream, &output, model, OpenAIResponsesOptions{}, map[string]any{"type": "response.output_text.delta", "delta": "direct"})
	if state.blocks[0].Text != "direct" {
		t.Fatalf("expected text delta without content part prelude to stream, got %#v", state.blocks[0])
	}
	state.apply(stream, &output, model, OpenAIResponsesOptions{}, map[string]any{
		"type": "response.output_item.done",
		"item": map[string]any{"type": "message", "id": "msg_1", "content": []any{map[string]any{"type": "output_text", "text": "direct"}}},
	})

	state.apply(stream, &output, model, OpenAIResponsesOptions{}, map[string]any{
		"type": "response.output_item.added",
		"item": map[string]any{"type": "message", "id": "msg_2"},
	})
	state.apply(stream, &output, model, OpenAIResponsesOptions{}, map[string]any{
		"type": "response.content_part.added",
		"part": map[string]any{"type": "refusal"},
	})
	state.apply(stream, &output, model, OpenAIResponsesOptions{}, map[string]any{"type": "response.output_text.delta", "delta": "still ignored"})
	state.apply(stream, &output, model, OpenAIResponsesOptions{}, map[string]any{"type": "response.refusal.delta", "delta": "refused"})
	if state.blocks[1].Text != "refused" {
		t.Fatalf("expected only matching refusal delta, got %#v", state.blocks[1])
	}

	state.apply(stream, &output, model, OpenAIResponsesOptions{}, map[string]any{
		"type": "response.output_item.added",
		"item": map[string]any{"type": "reasoning", "id": "rs_1"},
	})
	state.apply(stream, &output, model, OpenAIResponsesOptions{}, map[string]any{"type": "response.reasoning_summary_text.delta", "delta": "ignored"})
	if state.blocks[2].Thinking != "" {
		t.Fatalf("expected summary delta before summary part to be ignored, got %#v", state.blocks[2])
	}
	state.apply(stream, &output, model, OpenAIResponsesOptions{}, map[string]any{
		"type": "response.reasoning_summary_part.added",
		"part": map[string]any{"type": "summary_text", "text": ""},
	})
	state.apply(stream, &output, model, OpenAIResponsesOptions{}, map[string]any{"type": "response.reasoning_summary_text.delta", "delta": "summary"})
	if state.blocks[2].Thinking != "summary" {
		t.Fatalf("expected summary delta after summary part, got %#v", state.blocks[2])
	}
}

func TestResponsesStreamStateSeedsFunctionCallArgumentsFromAddedItem(t *testing.T) {
	stream := ai.NewAssistantMessageEventStream()
	model := testStreamModel()
	model.API = ai.ApiOpenAIResponses
	output := newAssistant(model)
	state := newResponsesStreamState()

	state.apply(stream, &output, model, OpenAIResponsesOptions{}, map[string]any{
		"type": "response.output_item.added",
		"item": map[string]any{"type": "function_call", "id": "fc_1", "call_id": "call_1", "name": "read", "arguments": `{"path"`},
	})
	state.apply(stream, &output, model, OpenAIResponsesOptions{}, map[string]any{
		"type":  "response.function_call_arguments.delta",
		"delta": `:"x"}`,
	})

	if len(state.blocks) != 1 || state.blocks[0].Arguments["path"] != "x" {
		t.Fatalf("expected initial item arguments and delta to combine, got %#v", state.blocks)
	}
}

func TestFinishResponsesStreamEmitsErrorForFailedResponse(t *testing.T) {
	stream := ai.NewAssistantMessageEventStream()
	model := testStreamModel()
	model.API = ai.ApiOpenAIResponses
	output := newAssistant(model)
	state := newResponsesStreamState()
	state.apply(stream, &output, model, OpenAIResponsesOptions{}, map[string]any{
		"type": "response.failed",
		"response": map[string]any{
			"error": map[string]any{"code": "bad_request", "message": "nope"},
		},
	})

	finishResponsesStream(stream, &output, state)
	event := <-stream.Events()
	for event.Type != "error" {
		event = <-stream.Events()
	}
	if event.Error == nil || event.Error.StopReason != ai.StopReasonError || event.Error.ErrorMessage != "bad_request: nope" {
		t.Fatalf("expected failed response error event, got %#v", event)
	}
}

func testStreamModel() ai.Model {
	return ai.Model{ID: "gpt-test", API: ai.ApiOpenAICompletions, Provider: "openai", Input: []string{"text"}}
}
