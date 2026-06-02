package connector

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"go.mau.fi/util/dbutil"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	agui "github.com/beeper/ai-bridge/pkg/ag-ui"
	agent "github.com/beeper/ai-bridge/pkg/agent"
	"github.com/beeper/ai-bridge/pkg/agent/harness/session"
	ai "github.com/beeper/ai-bridge/pkg/ai"
	aistream "github.com/beeper/ai-bridge/pkg/ai-stream"
	"github.com/beeper/ai-bridge/pkg/aidb"
	"github.com/beeper/ai-bridge/pkg/aiid"
)

func TestStreamPublisherUsesFakeProviderAndPublishesDeltas(t *testing.T) {
	ctx := context.Background()
	testAPI := ai.Api("test-stream")
	ai.RegisterAPIProvider(testAPI, func(ctx context.Context, model ai.Model, llmContext ai.Context, options ai.SimpleStreamOptions) *ai.AssistantMessageEventStream {
		stream := ai.NewAssistantMessageEventStream()
		go func() {
			message := ai.Message{Role: "assistant", Content: []ai.ContentBlock{{Type: "text", Text: "hello"}}, StopReason: ai.StopReasonStop}
			stream.Push(ai.AssistantMessageEvent{Type: "text_delta", ContentIndex: 0, Delta: "hel"})
			stream.Push(ai.AssistantMessageEvent{Type: "text_delta", ContentIndex: 0, Delta: "lo"})
			stream.Push(ai.AssistantMessageEvent{Type: "done", Reason: ai.StopReasonStop, Message: &message})
		}()
		return stream
	})
	defer ai.UnregisterAPIProvider(testAPI)

	publisher := &recordingStreamPublisher{}
	client := &Client{}
	run := aistream.NewRun("run", "thread", "beeper/fake", "assistant:run", "Fake", timeNow())
	run.MessageID = "assistant:run"
	secondVisibleChunks := 0
	streamFn := client.streamPublisher(publisher, "!room:example.com", "$event", run, func() {
		secondVisibleChunks++
	})
	result := streamFn(ctx, ai.Model{ID: "fake", API: testAPI}, ai.Context{}, ai.SimpleStreamOptions{}).Result()
	if result.StopReason != ai.StopReasonStop {
		t.Fatalf("unexpected stream result %#v", result)
	}
	if secondVisibleChunks != 1 {
		t.Fatalf("expected one second-visible-chunk callback, got %d", secondVisibleChunks)
	}
	if len(publisher.updates) != 4 {
		t.Fatalf("expected stream updates, got %#v", publisher.updates)
	}
	aiPayload, ok := publisher.updates[1][aistream.BeeperAIKey].(aistream.BeeperAI)
	if !ok || len(aiPayload.Events) != 2 {
		t.Fatalf("unexpected first delta %#v", publisher.updates[1])
	}
	part := aiPayload.Events[1].Event
	if part.Type() != agui.EventTextMessageContent || part.Get("delta") != "hel" {
		t.Fatalf("unexpected first text part %#v", part)
	}
}

func TestStreamPublisherWithAnchorSetupStartsProviderBeforeAnchor(t *testing.T) {
	ctx := context.Background()
	testAPI := ai.Api("test-stream-delayed-anchor")
	providerStarted := make(chan struct{})
	ai.RegisterAPIProvider(testAPI, func(ctx context.Context, model ai.Model, llmContext ai.Context, options ai.SimpleStreamOptions) *ai.AssistantMessageEventStream {
		close(providerStarted)
		stream := ai.NewAssistantMessageEventStream()
		go func() {
			message := ai.Message{Role: "assistant", Content: []ai.ContentBlock{{Type: "text", Text: "hello"}}, StopReason: ai.StopReasonStop}
			stream.Push(ai.AssistantMessageEvent{Type: "text_delta", ContentIndex: 0, Delta: "hello"})
			stream.Push(ai.AssistantMessageEvent{Type: "done", Reason: ai.StopReasonStop, Message: &message})
		}()
		return stream
	})
	defer ai.UnregisterAPIProvider(testAPI)

	publisher := &recordingStreamPublisher{}
	client := &Client{}
	run := aistream.NewRun("run", "thread", "beeper/fake", "assistant:run", "Fake", timeNow())
	run.MessageID = "assistant:run"
	setupStarted := make(chan struct{})
	releaseAnchor := make(chan struct{})
	stream := client.streamPublisherWithAnchorSetup(ctx, ai.Model{ID: "fake", API: testAPI}, ai.Context{}, ai.SimpleStreamOptions{}, publisher, "!room:example.com", run, &streamPublishCursor{nextSeq: 1}, func(context.Context) (id.EventID, func(), error) {
		close(setupStarted)
		<-releaseAnchor
		return "$event", nil, nil
	})

	select {
	case <-setupStarted:
	case <-time.After(time.Second):
		t.Fatal("anchor setup did not start")
	}
	select {
	case <-providerStarted:
	case <-time.After(time.Second):
		t.Fatal("provider did not start before anchor setup completed")
	}
	if len(publisher.updates) != 0 {
		t.Fatalf("published before anchor setup completed: %#v", publisher.updates)
	}
	resultDone := make(chan ai.Message, 1)
	resultStarted := make(chan struct{})
	go func() {
		close(resultStarted)
		resultDone <- stream.Result()
	}()
	<-resultStarted
	select {
	case result := <-resultDone:
		t.Fatalf("stream result completed before anchor setup: %#v", result)
	case <-time.After(50 * time.Millisecond):
	}
	close(releaseAnchor)
	result := <-resultDone
	if result.StopReason != ai.StopReasonStop {
		t.Fatalf("unexpected stream result %#v", result)
	}
	if len(publisher.updates) == 0 {
		t.Fatal("expected stream updates after anchor setup completed")
	}
}

func TestStreamPublisherPublishesThinkingDeltasLiveAfterThinkingStart(t *testing.T) {
	ctx := context.Background()
	testAPI := ai.Api("test-stream-thinking")
	ai.RegisterAPIProvider(testAPI, func(ctx context.Context, model ai.Model, llmContext ai.Context, options ai.SimpleStreamOptions) *ai.AssistantMessageEventStream {
		stream := ai.NewAssistantMessageEventStream()
		go func() {
			message := ai.Message{Role: "assistant", Content: []ai.ContentBlock{{Type: "thinking", Thinking: "checking context"}, {Type: "text", Text: "answer"}}, StopReason: ai.StopReasonStop}
			stream.Push(ai.AssistantMessageEvent{Type: "thinking_start", ContentIndex: 0, Partial: &message})
			stream.Push(ai.AssistantMessageEvent{Type: "thinking_delta", ContentIndex: 0, Delta: "checking context", Partial: &message})
			stream.Push(ai.AssistantMessageEvent{Type: "text_delta", ContentIndex: 1, Delta: "answer", Partial: &message})
			stream.Push(ai.AssistantMessageEvent{Type: "thinking_end", ContentIndex: 0, Content: "checking context", Partial: &message})
			stream.Push(ai.AssistantMessageEvent{Type: "done", Reason: ai.StopReasonStop, Message: &message})
		}()
		return stream
	})
	defer ai.UnregisterAPIProvider(testAPI)

	publisher := &recordingStreamPublisher{}
	client := &Client{}
	run := aistream.NewRun("run", "thread", "beeper/fake", "assistant:run", "Fake", timeNow())
	run.MessageID = "assistant:run"
	result := client.streamPublisher(publisher, "!room:example.com", "$event", run)(ctx, ai.Model{ID: "fake", API: testAPI}, ai.Context{}, ai.SimpleStreamOptions{}).Result()
	if result.StopReason != ai.StopReasonStop {
		t.Fatalf("unexpected stream result %#v", result)
	}
	var sawThinkingStart, sawThinkingDelta, sawTextDelta bool
	for _, update := range publisher.updates {
		payload, _ := update[aistream.BeeperAIKey].(aistream.BeeperAI)
		for _, envelope := range payload.Events {
			switch envelope.Event.Type() {
			case agui.EventReasoningMsgStart:
				sawThinkingStart = true
			case agui.EventReasoningMsgCont:
				sawThinkingDelta = envelope.Event.Get("delta") == "checking context"
			case agui.EventTextMessageContent:
				sawTextDelta = envelope.Event.Get("delta") == "answer"
			}
		}
	}
	if !sawThinkingStart || !sawThinkingDelta || !sawTextDelta {
		t.Fatalf("expected live thinking and text carriers, start=%v thinking=%v text=%v updates=%#v", sawThinkingStart, sawThinkingDelta, sawTextDelta, publisher.updates)
	}
}

func TestStreamPublisherFailsAfterIdleTimeout(t *testing.T) {
	ctx := context.Background()
	oldTimeout := activeStreamIdleTimeout
	activeStreamIdleTimeout = 20 * time.Millisecond
	defer func() {
		activeStreamIdleTimeout = oldTimeout
	}()
	testAPI := ai.Api("test-stream-timeout")
	ai.RegisterAPIProvider(testAPI, func(ctx context.Context, model ai.Model, llmContext ai.Context, options ai.SimpleStreamOptions) *ai.AssistantMessageEventStream {
		stream := ai.NewAssistantMessageEventStream()
		go func() {
			<-ctx.Done()
			stream.End()
		}()
		return stream
	})
	defer ai.UnregisterAPIProvider(testAPI)

	publisher := &recordingStreamPublisher{}
	client := &Client{}
	run := aistream.NewRun("run", "thread", "beeper/fake", "assistant:run", "Fake", timeNow())
	run.MessageID = "assistant:run"
	result := client.streamPublisher(publisher, "!room:example.com", "$event", run)(ctx, ai.Model{ID: "fake", API: testAPI}, ai.Context{}, ai.SimpleStreamOptions{}).Result()

	if result.StopReason != ai.StopReasonError || !strings.Contains(result.ErrorMessage, "timed out") {
		t.Fatalf("expected timeout error result, got %#v", result)
	}
	if run.Status.State != "error" {
		t.Fatalf("expected run to be marked error, got %#v", run.Status)
	}
	if len(publisher.updates) < 2 {
		t.Fatalf("expected start and timeout stream updates, got %#v", publisher.updates)
	}
}

func TestStreamPublisherReusesRunAcrossToolContinuation(t *testing.T) {
	ctx := context.Background()
	toolAPI := ai.Api("test-stream-tool")
	answerAPI := ai.Api("test-stream-answer")
	ai.RegisterAPIProvider(toolAPI, func(ctx context.Context, model ai.Model, llmContext ai.Context, options ai.SimpleStreamOptions) *ai.AssistantMessageEventStream {
		stream := ai.NewAssistantMessageEventStream()
		go func() {
			toolCall := &ai.ToolCall{ID: "call-session", Name: "get_session", Arguments: map[string]any{}}
			message := ai.Message{
				Role:       "assistant",
				Content:    []ai.ContentBlock{{Type: "toolCall", ID: toolCall.ID, Name: toolCall.Name, Arguments: toolCall.Arguments}},
				StopReason: ai.StopReasonToolUse,
				Usage:      ai.Usage{Input: 10, Output: 1, ReasoningTokens: 2, TotalTokens: 11},
			}
			stream.Push(ai.AssistantMessageEvent{Type: "toolcall_start", ToolCall: toolCall})
			stream.Push(ai.AssistantMessageEvent{Type: "toolcall_end", ToolCall: toolCall})
			stream.Push(ai.AssistantMessageEvent{Type: "done", Reason: ai.StopReasonToolUse, Message: &message})
		}()
		return stream
	})
	ai.RegisterAPIProvider(answerAPI, func(ctx context.Context, model ai.Model, llmContext ai.Context, options ai.SimpleStreamOptions) *ai.AssistantMessageEventStream {
		stream := ai.NewAssistantMessageEventStream()
		go func() {
			message := ai.Message{
				Role:       "assistant",
				Content:    []ai.ContentBlock{{Type: "text", Text: "hello"}},
				StopReason: ai.StopReasonStop,
				Usage:      ai.Usage{Input: 3, Output: 2, ReasoningTokens: 1, TotalTokens: 5},
			}
			stream.Push(ai.AssistantMessageEvent{Type: "text_delta", ContentIndex: 0, Delta: "hello"})
			stream.Push(ai.AssistantMessageEvent{Type: "done", Reason: ai.StopReasonStop, Message: &message})
		}()
		return stream
	})
	defer ai.UnregisterAPIProvider(toolAPI)
	defer ai.UnregisterAPIProvider(answerAPI)

	publisher := &recordingStreamPublisher{}
	client := &Client{}
	run := aistream.NewRun("run", "thread", "beeper/fake", "assistant:run", "Fake", timeNow())
	run.MessageID = "assistant:run"
	cursor := &streamPublishCursor{nextSeq: 1}

	toolResult := client.streamPublisherWithEndFrom(publisher, "!room:example.com", "$event", run, cursor, nil)(ctx, ai.Model{ID: "fake", API: toolAPI}, ai.Context{}, ai.SimpleStreamOptions{}).Result()
	if toolResult.StopReason != ai.StopReasonToolUse {
		t.Fatalf("unexpected tool stream result %#v", toolResult)
	}
	for _, evt := range run.Events {
		if evt.Type() == agui.EventRunFinished {
			t.Fatalf("tool-use provider stop must not finish a continued AG-UI run: %#v", run.Events)
		}
		if evt.Type() == agui.EventToolCallResult {
			t.Fatalf("provider toolcall_end must not emit a tool result before the tool runs: %#v", run.Events)
		}
	}
	answerResult := client.streamPublisherWithEndFrom(publisher, "!room:example.com", "$event", run, cursor, nil)(ctx, ai.Model{ID: "fake", API: answerAPI}, ai.Context{}, ai.SimpleStreamOptions{}).Result()
	if answerResult.StopReason != ai.StopReasonStop {
		t.Fatalf("unexpected answer stream result %#v", answerResult)
	}

	runStarted := 0
	for _, evt := range run.Events {
		if evt.Type() == agui.EventRunStarted {
			runStarted++
		}
	}
	if runStarted != 1 {
		t.Fatalf("expected one run start event, got %d in %#v", runStarted, run.Events)
	}
	runFinished := 0
	for _, evt := range run.Events {
		if evt.Type() == agui.EventRunFinished {
			runFinished++
		}
	}
	if runFinished != 1 {
		t.Fatalf("expected one terminal run finish after continuation, got %d in %#v", runFinished, run.Events)
	}
	if run.Usage != (agui.Usage{PromptTokens: 13, CompletionTokens: 3, ReasoningTokens: 3, TotalTokens: 16}) {
		t.Fatalf("continued run usage was not accumulated: %#v", run.Usage)
	}
	message := run.FinalBeeperAIMessage(0, true)
	if message.ID != "assistant:run" || len(message.Parts) != 2 {
		t.Fatalf("expected one assistant UI message with text and tool parts, got %#v", message)
	}
	if message.Parts[0]["type"] != "tool-call" || message.Parts[0]["toolCallId"] != "call-session" {
		t.Fatalf("expected folded tool-call part first, got %#v", message.Parts)
	}
	if message.Parts[1]["type"] != "text" || message.Parts[1]["content"] != "hello" {
		t.Fatalf("expected final answer text after tool call, got %#v", message.Parts)
	}
}

func TestStreamPublisherUsesFreshTextMessageAfterToolContinuationWithPriorText(t *testing.T) {
	ctx := context.Background()
	toolAPI := ai.Api("test-stream-tool-prior-text")
	answerAPI := ai.Api("test-stream-answer-after-prior-text")
	ai.RegisterAPIProvider(toolAPI, func(ctx context.Context, model ai.Model, llmContext ai.Context, options ai.SimpleStreamOptions) *ai.AssistantMessageEventStream {
		stream := ai.NewAssistantMessageEventStream()
		go func() {
			toolCall := &ai.ToolCall{ID: "call-session", Name: "get_session", Arguments: map[string]any{}}
			message := ai.Message{
				Role: "assistant",
				Content: []ai.ContentBlock{
					{Type: "text", Text: "before "},
					{Type: "toolCall", ID: toolCall.ID, Name: toolCall.Name, Arguments: toolCall.Arguments},
				},
				StopReason: ai.StopReasonToolUse,
			}
			stream.Push(ai.AssistantMessageEvent{Type: "text_delta", ContentIndex: 0, Delta: "before "})
			stream.Push(ai.AssistantMessageEvent{Type: "toolcall_start", ContentIndex: 1, ToolCall: toolCall})
			stream.Push(ai.AssistantMessageEvent{Type: "toolcall_end", ContentIndex: 1, ToolCall: toolCall})
			stream.Push(ai.AssistantMessageEvent{Type: "done", Reason: ai.StopReasonToolUse, Message: &message})
		}()
		return stream
	})
	ai.RegisterAPIProvider(answerAPI, func(ctx context.Context, model ai.Model, llmContext ai.Context, options ai.SimpleStreamOptions) *ai.AssistantMessageEventStream {
		stream := ai.NewAssistantMessageEventStream()
		go func() {
			message := ai.Message{
				Role:       "assistant",
				Content:    []ai.ContentBlock{{Type: "text", Text: "after"}},
				StopReason: ai.StopReasonStop,
			}
			stream.Push(ai.AssistantMessageEvent{Type: "text_delta", ContentIndex: 0, Delta: "after"})
			stream.Push(ai.AssistantMessageEvent{Type: "done", Reason: ai.StopReasonStop, Message: &message})
		}()
		return stream
	})
	defer ai.UnregisterAPIProvider(toolAPI)
	defer ai.UnregisterAPIProvider(answerAPI)

	publisher := &recordingStreamPublisher{}
	client := &Client{}
	run := aistream.NewRun("run", "thread", "beeper/fake", "assistant:run", "Fake", timeNow())
	run.MessageID = "assistant:run"
	cursor := &streamPublishCursor{nextSeq: 1}

	toolResult := client.streamPublisherWithEndFrom(publisher, "!room:example.com", "$event", run, cursor, nil)(ctx, ai.Model{ID: "fake", API: toolAPI}, ai.Context{}, ai.SimpleStreamOptions{}).Result()
	if toolResult.StopReason != ai.StopReasonToolUse {
		t.Fatalf("unexpected tool stream result %#v", toolResult)
	}
	writer := aistream.NewWriter(run, timeNow)
	writer.ToolEnd("call-session", "get_session", map[string]any{}, map[string]any{"state": agui.ToolResultStateComplete, "status": "success"})

	answerResult := client.streamPublisherWithEndFrom(publisher, "!room:example.com", "$event", run, cursor, nil)(ctx, ai.Model{ID: "fake", API: answerAPI}, ai.Context{}, ai.SimpleStreamOptions{}).Result()
	if answerResult.StopReason != ai.StopReasonStop {
		t.Fatalf("unexpected answer stream result %#v", answerResult)
	}

	textMessageIDs := textContentMessageIDs(run.Events)
	if strings.Join(textMessageIDs, "|") != "assistant:run|assistant:run-text-1" {
		t.Fatalf("resumed answer reused a prior text message id: %#v", textMessageIDs)
	}
	message := run.FinalBeeperAIMessage(0, true)
	got := uiPartSummary(message.Parts)
	want := []string{"text:before ", "tool-call:call-session", "text:after"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("continued UI parts mismatch\ngot:  %#v\nwant: %#v\nparts: %#v", got, want, message.Parts)
	}
}

func TestStreamPublisherAnchorsContinuationThinkingAndSecondToolAfterApproval(t *testing.T) {
	ctx := context.Background()
	toolAPI := ai.Api("test-stream-tool-prior-text-second-tool")
	answerAPI := ai.Api("test-stream-answer-thinking-second-tool")
	ai.RegisterAPIProvider(toolAPI, func(ctx context.Context, model ai.Model, llmContext ai.Context, options ai.SimpleStreamOptions) *ai.AssistantMessageEventStream {
		stream := ai.NewAssistantMessageEventStream()
		go func() {
			toolCall := &ai.ToolCall{ID: "call-session", Name: "get_session", Arguments: map[string]any{}}
			message := ai.Message{
				Role: "assistant",
				Content: []ai.ContentBlock{
					{Type: "text", Text: "before "},
					{Type: "toolCall", ID: toolCall.ID, Name: toolCall.Name, Arguments: toolCall.Arguments},
				},
				StopReason: ai.StopReasonToolUse,
			}
			stream.Push(ai.AssistantMessageEvent{Type: "text_delta", ContentIndex: 0, Delta: "before "})
			stream.Push(ai.AssistantMessageEvent{Type: "toolcall_start", ContentIndex: 1, ToolCall: toolCall})
			stream.Push(ai.AssistantMessageEvent{Type: "toolcall_end", ContentIndex: 1, ToolCall: toolCall})
			stream.Push(ai.AssistantMessageEvent{Type: "done", Reason: ai.StopReasonToolUse, Message: &message})
		}()
		return stream
	})
	ai.RegisterAPIProvider(answerAPI, func(ctx context.Context, model ai.Model, llmContext ai.Context, options ai.SimpleStreamOptions) *ai.AssistantMessageEventStream {
		stream := ai.NewAssistantMessageEventStream()
		go func() {
			secondTool := &ai.ToolCall{ID: "call-second", Name: "second_tool", Arguments: map[string]any{"ok": true}}
			message := ai.Message{
				Role: "assistant",
				Content: []ai.ContentBlock{
					{Type: "thinking", Thinking: "checking approval"},
					{Type: "text", Text: "after "},
					{Type: "toolCall", ID: secondTool.ID, Name: secondTool.Name, Arguments: secondTool.Arguments},
				},
				StopReason: ai.StopReasonToolUse,
			}
			stream.Push(ai.AssistantMessageEvent{Type: "thinking_start", ContentIndex: 0})
			stream.Push(ai.AssistantMessageEvent{Type: "thinking_delta", ContentIndex: 0, Delta: "checking approval"})
			stream.Push(ai.AssistantMessageEvent{Type: "text_delta", ContentIndex: 1, Delta: "after "})
			stream.Push(ai.AssistantMessageEvent{Type: "toolcall_start", ContentIndex: 2, ToolCall: secondTool})
			stream.Push(ai.AssistantMessageEvent{Type: "toolcall_end", ContentIndex: 2, ToolCall: secondTool})
			stream.Push(ai.AssistantMessageEvent{Type: "done", Reason: ai.StopReasonToolUse, Message: &message})
		}()
		return stream
	})
	defer ai.UnregisterAPIProvider(toolAPI)
	defer ai.UnregisterAPIProvider(answerAPI)

	publisher := &recordingStreamPublisher{}
	client := &Client{}
	run := aistream.NewRun("run", "thread", "beeper/fake", "assistant:run", "Fake", timeNow())
	run.MessageID = "assistant:run"
	cursor := &streamPublishCursor{nextSeq: 1}

	toolResult := client.streamPublisherWithEndFrom(publisher, "!room:example.com", "$event", run, cursor, nil)(ctx, ai.Model{ID: "fake", API: toolAPI}, ai.Context{}, ai.SimpleStreamOptions{}).Result()
	if toolResult.StopReason != ai.StopReasonToolUse {
		t.Fatalf("unexpected first tool stream result %#v", toolResult)
	}
	writer := aistream.NewWriter(run, timeNow)
	writer.ToolEnd("call-session", "get_session", map[string]any{}, map[string]any{"state": agui.ToolResultStateComplete, "status": "success"})

	answerResult := client.streamPublisherWithEndFrom(publisher, "!room:example.com", "$event", run, cursor, nil)(ctx, ai.Model{ID: "fake", API: answerAPI}, ai.Context{}, ai.SimpleStreamOptions{}).Result()
	if answerResult.StopReason != ai.StopReasonToolUse {
		t.Fatalf("unexpected answer stream result %#v", answerResult)
	}

	parents := toolParentMessageIDs(run.Events)
	if parents["call-second"] != "assistant:run-text-2" {
		t.Fatalf("second continuation tool used wrong parent: %#v", parents)
	}

	message := run.FinalBeeperAIMessage(0, true)
	got := uiPartSummary(message.Parts)
	want := []string{"text:before ", "tool-call:call-session", "thinking:checking approval", "text:after ", "tool-call:call-second"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("continued UI parts mismatch\ngot:  %#v\nwant: %#v\nparts: %#v", got, want, message.Parts)
	}
}

func TestDoneFallbackAfterPriorTextUsesCurrentWriterOnly(t *testing.T) {
	run := aistream.NewRun("run", "thread", "beeper/fake", "assistant:run", "Fake", timeNow())
	run.MessageID = "assistant:run"
	firstWriter := aistream.NewWriter(run, timeNow)
	firstWriter.Start()
	firstWriter.TextDelta(0, "before ")
	firstWriter.ToolStart("call-session", "get_session", 1, nil)
	firstWriter.ToolInputComplete("call-session", "get_session", map[string]any{})
	firstWriter.AwaitToolUseWithUsage(nil)

	message := ai.Message{
		Role:       "assistant",
		Content:    []ai.ContentBlock{{Type: "text", Text: "after"}},
		StopReason: ai.StopReasonStop,
	}
	secondWriter := aistream.NewWriter(run, timeNow)
	applyAIStreamEvent(secondWriter, ai.AssistantMessageEvent{Type: "done", Reason: ai.StopReasonStop, Message: &message})

	textMessageIDs := textContentMessageIDs(run.Events)
	if strings.Join(textMessageIDs, "|") != "assistant:run|assistant:run-text-1" {
		t.Fatalf("done fallback reused a prior text message id: %#v", textMessageIDs)
	}
	uiMessage := run.FinalBeeperAIMessage(0, true)
	got := uiPartSummary(uiMessage.Parts)
	want := []string{"text:before ", "tool-call:call-session", "text:after"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("fallback UI parts mismatch\ngot:  %#v\nwant: %#v\nparts: %#v", got, want, uiMessage.Parts)
	}
}

func TestFinalizedAssistantRunPreservesAccumulatedStreamUsage(t *testing.T) {
	run := *aistream.NewRun("run", "thread", "beeper/fake", "assistant:run", "Fake", timeNow())
	run.Usage = agui.Usage{PromptTokens: 13, CompletionTokens: 3, ReasoningTokens: 3, TotalTokens: 16}
	message := ai.Message{
		Role:       "assistant",
		Content:    []ai.ContentBlock{{Type: "text", Text: "hello"}},
		StopReason: ai.StopReasonStop,
		Usage:      ai.Usage{Input: 3, Output: 2, ReasoningTokens: 1, TotalTokens: 5},
	}

	final := finalizedAssistantRun(run, message, 400000)
	want := run.Usage
	want.ContextLimit = 400000
	if final.Usage != want {
		t.Fatalf("finalization overwrote accumulated usage: got %#v want %#v", final.Usage, want)
	}
}

func TestFinalizedAssistantRunUsesModelContextLimit(t *testing.T) {
	run := *aistream.NewRun("run", "thread", "beeper/fake", "assistant:run", "Fake", timeNow())
	message := ai.Message{
		Role:       "assistant",
		Content:    []ai.ContentBlock{{Type: "text", Text: "hello"}},
		StopReason: ai.StopReasonStop,
		Usage:      ai.Usage{Input: 80, Output: 10, TotalTokens: 90},
	}

	final := finalizedAssistantRun(run, message, 100)
	want := agui.Usage{PromptTokens: 80, CompletionTokens: 10, TotalTokens: 90, ContextLimit: 100}
	if final.Usage != want {
		t.Fatalf("final usage = %#v, want %#v", final.Usage, want)
	}
	payload := final.AI(aistream.AIKindFinal)
	if len(payload.Events) != 1 || payload.Events[0].Event.Get("usage") != want {
		t.Fatalf("final RUN_FINISHED usage = %#v, want %#v", payload.Events, want)
	}
}

func TestFinalizedAssistantRunCompletesAfterApprovalInterrupt(t *testing.T) {
	run := *aistream.NewRun("run", "thread", "beeper/fake", "assistant:run", "Fake", timeNow())
	run.MessageID = "assistant:run"
	writer := aistream.NewWriter(&run, timeNow)
	writer.Start()
	writer.ToolApprovalRequested("call-session", "get_session", map[string]any{}, aistream.ToolApproval{ID: "approval-1", NeedsApproval: true})
	writer.InterruptWithUsage(nil)
	writer.ToolApprovalResponded("call-session", "get_session", map[string]any{}, aistream.ToolApprovalResponse{ID: "approval-1", Approved: true})
	message := ai.Message{
		Role:       "assistant",
		Content:    []ai.ContentBlock{{Type: "text", Text: "done"}},
		StopReason: ai.StopReasonStop,
	}

	final := finalizedAssistantRun(run, message, 0)
	if final.Status.State != "complete" {
		t.Fatalf("finalized interrupted run state = %q, want complete", final.Status.State)
	}
	payload := final.AI(aistream.AIKindFinal)
	if len(payload.Events) != 1 || payload.Events[0].Event.Type() != agui.EventRunFinished {
		t.Fatalf("missing final RUN_FINISHED event: %#v", payload.Events)
	}
	terminal := payload.Events[0].Event
	if terminal.Get("finishReason") != agui.FinishReasonStop {
		t.Fatalf("final finish reason = %#v, want stop", terminal.Get("finishReason"))
	}
	if outcome, ok := terminal.Get("outcome").(agui.RunFinishedOutcome); !ok || outcome.Type != agui.OutcomeSuccess {
		t.Fatalf("final outcome should be success, got %#v", terminal.Get("outcome"))
	}
}

func TestDoneEventAfterApprovalDoesNotDuplicateStreamedText(t *testing.T) {
	run := aistream.NewRun("run", "thread", "beeper/fake", "assistant:run", "Fake", timeNow())
	run.MessageID = "assistant:run"
	writer := aistream.NewWriter(run, timeNow)
	writer.Start()
	writer.ToolApprovalRequested("call-session", "get_session", map[string]any{}, aistream.ToolApproval{ID: "approval-1", NeedsApproval: true})
	writer.InterruptWithUsage(nil)
	writer.ToolApprovalResponded("call-session", "get_session", map[string]any{}, aistream.ToolApprovalResponse{ID: "approval-1", Approved: true})

	text := "First tool call done. Now I'm sending text in between."
	message := ai.Message{
		Role:       "assistant",
		Content:    []ai.ContentBlock{{Type: "text", Text: text}},
		StopReason: ai.StopReasonStop,
	}
	applyAIStreamEvent(writer, ai.AssistantMessageEvent{Type: "text_delta", ContentIndex: 0, Delta: text})
	applyAIStreamEvent(writer, ai.AssistantMessageEvent{Type: "done", Reason: ai.StopReasonStop, Message: &message})

	uiMessage := run.FinalBeeperAIMessage(0, true)
	textParts := 0
	for _, part := range uiMessage.Parts {
		if part["type"] != "text" {
			continue
		}
		textParts++
		if part["content"] != text {
			t.Fatalf("unexpected text part content: %#v", part)
		}
	}
	if textParts != 1 {
		t.Fatalf("expected one text part after approval resume, got %d parts: %#v", textParts, uiMessage.Parts)
	}
	if strings.Count(run.Preview.Text, text) != 1 {
		t.Fatalf("preview duplicated streamed text: %q", run.Preview.Text)
	}
}

func TestInterruptedStreamRunsRemainRecoverable(t *testing.T) {
	run := aistream.NewRun("run", "thread", "beeper/fake", "assistant:run", "Fake", timeNow())
	run.Status.State = "interrupted"
	cursor := &streamPublishCursor{lastPersisted: 1, lastPersistedAt: time.Now()}

	if !shouldPersistStreamRun(run, cursor) {
		t.Fatal("interrupted stream should stay persisted while waiting for approval")
	}
}

func TestTerminalStreamRunsRemainRecoverableUntilFinalEdit(t *testing.T) {
	for _, state := range []string{"complete", "aborted", "error"} {
		run := aistream.NewRun("run", "thread", "beeper/fake", "assistant:run", "Fake", timeNow())
		run.Status.State = state
		cursor := &streamPublishCursor{lastPersisted: len(run.Events), lastPersistedAt: time.Now()}

		if !shouldPersistStreamRun(run, cursor) {
			t.Fatalf("%s stream should stay persisted until final edit deletes it", state)
		}
	}
}

func TestPublishNewStreamEventsPersistsTerminalRun(t *testing.T) {
	ctx := context.Background()
	publisher := &recordingStreamPublisher{}
	run := aistream.NewRun("run", "thread", "beeper/fake", "assistant:run", "Fake", timeNow())
	run.MessageID = "assistant:run"
	writer := aistream.NewWriter(run, timeNow)
	writer.Start()
	persistedStates := []string{}
	cursor := &streamPublishCursor{
		nextSeq: 1,
		persist: func(ctx context.Context, run *aistream.Run) error {
			persistedStates = append(persistedStates, run.Status.State)
			return nil
		},
	}
	client := &Client{}
	if err := client.publishNewStreamEvents(ctx, publisher, "!room:example.com", "$event", run, cursor); err != nil {
		t.Fatal(err)
	}
	writer.Finish(agui.FinishReasonStop)
	if err := client.publishNewStreamEvents(ctx, publisher, "!room:example.com", "$event", run, cursor); err != nil {
		t.Fatal(err)
	}
	if len(persistedStates) != 2 || persistedStates[0] != "streaming" || persistedStates[1] != "complete" {
		t.Fatalf("expected streaming and terminal states to persist, got %#v", persistedStates)
	}
}

func TestAssistantStreamPublisherDoesNotCreateAnchorAfterTerminalError(t *testing.T) {
	ctx := context.Background()
	portalKey := aiid.PortalKey(id.RoomID("!room:example.com"), "login")
	run := aistream.NewRun("run", "thread", "beeper/fake", "assistant:run", "Fake", timeNow())
	run.MessageID = "assistant:run"
	writer := aistream.NewWriter(run, timeNow)
	writer.Start()
	writer.Error("usage limit exceeded")
	active := &activeAIRun{
		portalKey: portalKey,
		last: &assistantStreamState{
			messageID: "assistant:run",
			runID:     run.RunID,
			run:       run,
		},
	}
	client := &Client{}
	client.setActiveRun(portalKey, active)
	publisher := &recordingStreamPublisher{}
	portal := &bridgev2.Portal{Portal: &database.Portal{
		PortalKey: portalKey,
		MXID:      id.RoomID("!room:example.com"),
	}}

	result := client.assistantStreamPublisher(
		publisher,
		portal,
		&aiid.PortalMetadata{SessionID: "thread"},
		aiid.ProviderConfig{ID: "beeper"},
		ai.Model{ID: "fake"},
	)(ctx, ai.Model{ID: "fake"}, ai.Context{}, ai.SimpleStreamOptions{}).Result()

	if result.StopReason != ai.StopReasonError || !strings.Contains(result.ErrorMessage, "usage limit exceeded") {
		t.Fatalf("expected existing terminal error result, got %#v", result)
	}
	if publisher.descriptorCalls != 0 || len(publisher.updates) != 0 {
		t.Fatalf("terminal error should not create a new stream descriptor or publish carriers, descriptorCalls=%d updates=%#v", publisher.descriptorCalls, publisher.updates)
	}
}

func TestFinalizeInterruptedSessionTurnClosesToolUseTurn(t *testing.T) {
	ctx := context.Background()
	store := testAIStore(t)
	loginID := networkid.UserLoginID("login")
	agentSession, err := store.CreateSession(ctx, loginID, session.SQLiteSessionCreateOptions{ID: "session-1"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = agentSession.AppendMessage(ctx, ai.Message{Role: "user", Content: "hi", Timestamp: 1}); err != nil {
		t.Fatal(err)
	}
	assistantEntryID, err := agentSession.AppendMessage(ctx, ai.Message{
		Role: "assistant",
		Content: []ai.ContentBlock{{
			Type:      "toolCall",
			ID:        "call-session",
			Name:      "get_session",
			Arguments: map[string]any{},
		}},
		StopReason: ai.StopReasonToolUse,
		Timestamp:  2,
	})
	if err != nil {
		t.Fatal(err)
	}

	run := aistream.NewRun("run-1", "session-1", "beeper/fake", "assistant:run", "Fake", timeNow())
	run.MessageID = "assistant:run"
	writer := aistream.NewWriter(run, timeNow)
	writer.Start()
	writer.ToolStart("call-session", "get_session", 0, nil)
	writer.ToolApprovalRequested("call-session", "get_session", map[string]any{}, aistream.ToolApproval{ID: "approval-1", NeedsApproval: true})
	writer.InterruptWithUsage(nil)

	client := &Client{
		Main: &Connector{Store: store},
		UserLogin: &bridgev2.UserLogin{UserLogin: &database.UserLogin{
			ID: loginID,
		}},
	}
	client.finalizeInterruptedSessionTurn(ctx, aidb.ActiveStreamRecord{
		RunID:      run.RunID,
		LoginID:    loginID,
		EntryID:    assistantEntryID,
		Run:        *run,
		ProviderID: "beeper",
		ModelID:    "fake",
	}, errors.New("AI stream was interrupted before completion"))

	context, err := agentSession.BuildContext(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(context.Messages) != 4 {
		t.Fatalf("expected recovered turn to have 4 messages, got %#v", context.Messages)
	}
	toolResult := context.Messages[2]
	if toolResult.Role != "toolResult" || toolResult.ToolCallID != "call-session" || !toolResult.IsError {
		t.Fatalf("expected failed tool result after interrupted tool call, got %#v", toolResult)
	}
	final := context.Messages[3]
	if final.Role != "assistant" || final.StopReason != ai.StopReasonError || !strings.Contains(final.ErrorMessage, "interrupted") {
		t.Fatalf("expected assistant error to close interrupted turn, got %#v", final)
	}
}

func TestFinalizeInterruptedSessionTurnWithEmptyEntryIDIsIdempotent(t *testing.T) {
	ctx := context.Background()
	store := testAIStore(t)
	loginID := networkid.UserLoginID("login")
	agentSession, err := store.CreateSession(ctx, loginID, session.SQLiteSessionCreateOptions{ID: "session-1"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = agentSession.AppendMessage(ctx, ai.Message{Role: "user", Content: "hi", Timestamp: 1}); err != nil {
		t.Fatal(err)
	}

	run := aistream.NewRun("run-1", "session-1", "beeper/fake", "assistant:run", "Fake", timeNow())
	run.MessageID = "assistant:run"
	writer := aistream.NewWriter(run, timeNow)
	writer.Start()

	client := &Client{
		Main: &Connector{Store: store},
		UserLogin: &bridgev2.UserLogin{UserLogin: &database.UserLogin{
			ID: loginID,
		}},
	}
	record := aidb.ActiveStreamRecord{
		RunID:      run.RunID,
		LoginID:    loginID,
		Run:        *run,
		ProviderID: "beeper",
		ModelID:    "fake",
	}
	cause := errors.New("AI stream was interrupted before completion")
	client.finalizeInterruptedSessionTurn(ctx, record, cause)
	client.finalizeInterruptedSessionTurn(ctx, record, cause)

	context, err := agentSession.BuildContext(ctx)
	if err != nil {
		t.Fatal(err)
	}
	recoveryCount := 0
	for _, message := range context.Messages {
		if message.Role == "assistant" && message.ErrorMessage == cause.Error() {
			recoveryCount++
		}
	}
	if recoveryCount != 1 {
		t.Fatalf("expected one recovered assistant error, got %d in %#v", recoveryCount, context.Messages)
	}
}

func TestFinalizedAssistantRunErrorUsesGenericPreviewAndTerminalReason(t *testing.T) {
	run := *aistream.NewRun("run", "thread", "beeper/fake", "assistant:run", "Fake", timeNow())
	message := ai.Message{
		Role:         "assistant",
		StopReason:   ai.StopReasonError,
		ErrorMessage: "OpenAI API error (403): This model is not available",
	}

	final := finalizedAssistantRun(run, message, 0)
	wantVisible := message.ErrorMessage
	if final.Preview.Text != wantVisible {
		t.Fatalf("error preview = %q, want %q", final.Preview.Text, wantVisible)
	}
	payload := final.AI(aistream.AIKindFinal)
	if len(payload.Events) != 1 || payload.Events[0].Event.Type() != agui.EventRunError {
		t.Fatalf("missing final RUN_ERROR event: %#v", payload.Events)
	}
	if payload.Events[0].Event.Get("message") != message.ErrorMessage {
		t.Fatalf("missing RUN_ERROR message: %#v", payload.Events[0].Event)
	}
}

func TestDoneEventAddsFinalTextWhenProviderDidNotStreamDeltas(t *testing.T) {
	run := aistream.NewRun("run", "thread", "beeper/fake", "assistant:run", "Fake", timeNow())
	run.MessageID = "assistant:run"
	writer := aistream.NewWriter(run, timeNow)
	message := ai.Message{
		Role:       "assistant",
		Content:    []ai.ContentBlock{{Type: "text", Text: "final only"}},
		StopReason: ai.StopReasonStop,
	}

	applyAIStreamEvent(writer, ai.AssistantMessageEvent{Type: "done", Reason: ai.StopReasonStop, Message: &message})

	uiMessage := run.FinalBeeperAIMessage(0, true)
	if got := len(uiMessage.Parts); got != 1 {
		t.Fatalf("expected final text part, got %d parts: %#v", got, uiMessage.Parts)
	}
	if uiMessage.Parts[0]["type"] != "text" || uiMessage.Parts[0]["content"] != "final only" {
		t.Fatalf("final-only provider text was not preserved as a UI part: %#v", uiMessage.Parts)
	}
}

func TestAppendToolOutputsPreservesStructuredResult(t *testing.T) {
	run := aistream.NewRun("run", "thread", "beeper/gpt-5", "assistant:run", "GPT-5", timeNow())
	run.MessageID = "assistant:run"
	writer := aistream.NewWriter(run, timeNow)
	writer.ToolStart("call-session", "get_session", 0, nil)
	writer.ToolEnd("call-session", "get_session", map[string]any{}, nil)

	appendToolOutputs(run, []toolOutputEvent{{
		ID:    "call-session",
		Name:  "get_session",
		Input: map[string]any{},
		Result: agent.AgentToolResult[any]{
			Content: []ai.ContentBlock{{Type: "text", Text: `{"session_id":"session-1"}`}},
			Details: struct {
				SessionID string `json:"session_id"`
				ModelID   string `json:"model_id"`
			}{SessionID: "session-1", ModelID: "gpt-5"},
		},
	}})

	message := run.FinalBeeperAIMessage(0, true)
	if len(message.Parts) != 1 {
		t.Fatalf("expected one tool part, got %#v", message.Parts)
	}
	output, ok := message.Parts[0]["output"].(map[string]any)
	if !ok {
		t.Fatalf("expected object output, got %#v", message.Parts[0]["output"])
	}
	if output["session_id"] != "session-1" || output["model_id"] != "gpt-5" || output["state"] != agui.ToolResultStateComplete || output["status"] != "success" {
		encoded, _ := json.Marshal(output)
		t.Fatalf("tool output lost structured result: %s", encoded)
	}
}

func TestAppendToolOutputsAddsFetchSources(t *testing.T) {
	run := aistream.NewRun("run", "thread", "beeper/gpt-5", "assistant:run", "GPT-5", timeNow())
	run.MessageID = "assistant:run"
	writer := aistream.NewWriter(run, timeNow)
	writer.ToolStart("call-fetch", "fetch", 0, nil)
	writer.ToolEnd("call-fetch", "fetch", map[string]any{"url": "https://example.com/one"}, nil)

	appendToolOutputs(run, []toolOutputEvent{{
		ID:    "call-fetch",
		Name:  "fetch",
		Input: map[string]any{"url": "https://example.com/one"},
		Result: agent.AgentToolResult[any]{
			Details: map[string]any{
				"title":        "One",
				"url":          "https://example.com/one",
				"final_url":    "https://example.com/one",
				"description":  "desc",
				"published_at": "2026-01-01",
				"site_name":    "Example",
			},
		},
	}})

	message := run.FinalBeeperAIMessage(0, true)
	var source map[string]any
	for _, part := range message.Parts {
		if part["type"] == "source-url" {
			source = part
			break
		}
	}
	if source == nil {
		t.Fatalf("expected source-url part, got %#v", message.Parts)
	}
	if source["url"] != "https://example.com/one" || source["title"] != "One" || source["sourceId"] != "https://example.com/one" {
		t.Fatalf("unexpected source part %#v", source)
	}
	if source["description"] != "desc" || source["publishedAt"] != "2026-01-01" || source["siteName"] != "Example" || source["imageUrl"] == "" {
		t.Fatalf("missing canonical source metadata: %#v", source)
	}
	appearances, ok := source["appearances"].([]map[string]any)
	if !ok || len(appearances) != 1 || appearances[0]["kind"] != "fetch" || appearances[0]["toolCallId"] != "call-fetch" {
		t.Fatalf("missing source appearances: %#v", source["appearances"])
	}
}

func TestAppendToolOutputsKeepsWebSearchResultSources(t *testing.T) {
	run := aistream.NewRun("run", "thread", "beeper/gpt-5", "assistant:run", "GPT-5", timeNow())
	run.MessageID = "assistant:run"
	writer := aistream.NewWriter(run, timeNow)
	writer.ToolStart("call-search", "web_search", 0, nil)
	writer.ToolEnd("call-search", "web_search", map[string]any{"query": "latest news"}, nil)

	appendToolOutputs(run, []toolOutputEvent{{
		ID:    "call-search",
		Name:  "web_search",
		Input: map[string]any{"query": "latest news"},
		Result: agent.AgentToolResult[any]{
			Details: map[string]any{
				"results": []any{
					map[string]any{
						"title":       "Headline",
						"url":         "https://example.com/headline",
						"description": "desc",
					},
				},
			},
		},
	}})

	message := run.FinalBeeperAIMessage(0, true)
	var source map[string]any
	for _, part := range message.Parts {
		if part["type"] == "source-url" {
			source = part
			break
		}
	}
	if source == nil {
		t.Fatalf("expected web_search source-url part for tool result list, got %#v", message.Parts)
	}
	appearances, ok := source["appearances"].([]map[string]any)
	if !ok || len(appearances) != 1 || appearances[0]["kind"] != "web_search" || appearances[0]["toolCallId"] != "call-search" || appearances[0]["rank"] != 1 {
		t.Fatalf("missing web_search source appearance: %#v", source["appearances"])
	}
	if appearances[0]["cited"] == true {
		t.Fatalf("raw web_search result should not be marked cited: %#v", appearances[0])
	}
}

func TestAssistantEventMetadataCanBeFinalizedBeforeInsert(t *testing.T) {
	client := &Client{}
	run := aistream.NewRun("run", "thread", "beeper/gpt-5", "assistant:run", "GPT-5", timeNow())
	run.MessageID = "assistant:run"
	assistantEvent, metadata := client.assistantEvent(
		context.Background(),
		aiid.PortalKey(id.RoomID("!room:example.com"), "login"),
		"assistant:run",
		"beeper",
		"gpt-5",
		"run",
		&event.BeeperStreamInfo{Type: "com.beeper.ai.response"},
		*run,
		timeNow(),
	)
	if metadata.StreamStatus != "streaming" {
		t.Fatalf("expected streaming metadata, got %#v", metadata)
	}
	if assistantEvent.Sender.Sender != aiid.AssistantUserID() {
		t.Fatalf("assistant event used sender %q", assistantEvent.Sender.Sender)
	}
	profile := assistantEvent.Data.Parts[0].Content.BeeperPerMessageProfile
	if profile == nil || profile.ID != string(aiid.ModelContactID("beeper", "gpt-5")) || profile.Displayname != "gpt-5" || !profile.HasFallback {
		t.Fatalf("assistant event missing model profile: %#v", profile)
	}
	fillAssistantMetadata(metadata, "entry", "beeper", "gpt-5", "run", ai.Message{
		Role:       "assistant",
		ResponseID: "resp",
		Usage:      ai.Usage{Input: 1, Output: 2},
		StopReason: ai.StopReasonStop,
	})
	partMetadata, ok := assistantEvent.Data.Parts[0].DBMetadata.(*aiid.MessageMetadata)
	if !ok {
		t.Fatalf("unexpected metadata type %T", assistantEvent.Data.Parts[0].DBMetadata)
	}
	if partMetadata.SessionEntryID != "entry" || partMetadata.StreamStatus != "done" || partMetadata.ResponseID != "resp" {
		t.Fatalf("metadata was not finalized through shared pointer: %#v", partMetadata)
	}

	finalRun := finalizedAssistantRun(*run, ai.Message{
		Role:       "assistant",
		StopReason: ai.StopReasonStop,
	}, 0)
	edit := client.assistantFinalEditWithProjection(aiid.PortalKey(id.RoomID("!room:example.com"), "login"), "assistant:run", "beeper", "gpt-5", finalRun, metadata, timeNow().Add(time.Millisecond))
	if edit.Sender.Sender != aiid.AssistantUserID() {
		t.Fatalf("assistant final edit used sender %q", edit.Sender.Sender)
	}
	converted, err := edit.ConvertEditFunc(context.Background(), nil, nil, []*database.Message{{}}, edit.Data)
	if err != nil {
		t.Fatal(err)
	}
	profile = converted.ModifiedParts[0].Content.BeeperPerMessageProfile
	if profile == nil || profile.ID != string(aiid.ModelContactID("beeper", "gpt-5")) || profile.Displayname != "gpt-5" || !profile.HasFallback {
		t.Fatalf("assistant final edit missing model profile: %#v", profile)
	}
}

func TestAssistantFallbackEventsUseStrictMatrixOrder(t *testing.T) {
	client := &Client{}
	userTimestamp := time.Now().Add(10 * time.Second).Truncate(time.Millisecond)
	active := &activeAIRun{lastConsumedTimestamp: userTimestamp}
	anchorTimestamp := active.nextAssistantAnchorTimestamp()
	if !anchorTimestamp.Equal(userTimestamp.Add(time.Millisecond)) {
		t.Fatalf("anchor timestamp = %s, want %s", anchorTimestamp, userTimestamp.Add(time.Millisecond))
	}
	run := aistream.NewRun("run", "thread", "beeper/gpt-5", "assistant:run", "GPT-5", anchorTimestamp)
	run.MessageID = "assistant:run"
	assistantEvent, metadata := client.assistantEvent(
		context.Background(),
		aiid.PortalKey(id.RoomID("!room:example.com"), "login"),
		"assistant:run",
		"beeper",
		"gpt-5",
		"run",
		&event.BeeperStreamInfo{Type: "com.beeper.ai.response"},
		*run,
		anchorTimestamp,
	)
	if !assistantEvent.Timestamp.After(userTimestamp) || assistantEvent.StreamOrder <= userTimestamp.UnixNano() {
		t.Fatalf("anchor event not after user echo: timestamp=%s stream_order=%d user=%s", assistantEvent.Timestamp, assistantEvent.StreamOrder, userTimestamp)
	}

	finalTimestamp := afterMatrixTimestamp(anchorTimestamp)
	if !finalTimestamp.Equal(anchorTimestamp.Add(time.Millisecond)) {
		t.Fatalf("final timestamp = %s, want %s", finalTimestamp, anchorTimestamp.Add(time.Millisecond))
	}
	finalRun := finalizedAssistantRun(*run, ai.Message{Role: "assistant", StopReason: ai.StopReasonStop}, 0)
	edit := client.assistantFinalEditWithProjection(
		aiid.PortalKey(id.RoomID("!room:example.com"), "login"),
		"assistant:run",
		"beeper",
		"gpt-5",
		finalRun,
		metadata,
		finalTimestamp,
	)
	if !edit.Timestamp.After(assistantEvent.Timestamp) || edit.StreamOrder <= assistantEvent.StreamOrder {
		t.Fatalf("final edit not after anchor: timestamp=%s stream_order=%d anchor=%s/%d", edit.Timestamp, edit.StreamOrder, assistantEvent.Timestamp, assistantEvent.StreamOrder)
	}
}

func TestPendingUserMessageSeedsAssistantAnchorOrder(t *testing.T) {
	userTimestamp := time.Now().Add(10 * time.Second).Truncate(time.Millisecond)
	active := &activeAIRun{
		provider: aiid.ProviderConfig{ID: "beeper"},
		model:    ai.Model{ID: "gpt-5"},
		runID:    "run",
	}
	active.addPending(&pendingAIMessage{
		msg: &bridgev2.MatrixMessage{
			MatrixEventBase: bridgev2.MatrixEventBase[*event.MessageEventContent]{
				Event: &event.Event{Timestamp: userTimestamp.UnixMilli()},
			},
		},
		metadata: &aiid.MessageMetadata{},
	})
	anchorTimestamp := active.nextAssistantAnchorTimestamp()
	if !anchorTimestamp.Equal(userTimestamp.Add(time.Millisecond)) {
		t.Fatalf("anchor timestamp = %s, want %s", anchorTimestamp, userTimestamp.Add(time.Millisecond))
	}
}

func TestAssistantModelProfileUsesConfiguredModelDisplayName(t *testing.T) {
	provider := aiid.ProviderConfig{
		ID:     "custom",
		Models: []ai.Model{{ID: "gpt-5.5", Name: "GPT 5.5"}},
	}
	client := &Client{Main: &Connector{}, UserLogin: &bridgev2.UserLogin{UserLogin: &database.UserLogin{
		ID:       "login",
		Metadata: &aiid.UserLoginMetadata{Providers: map[string]aiid.ProviderConfig{provider.ID: provider}},
	}}}
	content := &event.MessageEventContent{}
	client.applyModelProfile(context.Background(), content, "custom", "gpt-5.5")
	profile := content.BeeperPerMessageProfile
	if profile == nil || profile.ID != string(aiid.ModelContactID("custom", "gpt-5.5")) || profile.Displayname != "GPT 5.5" || !profile.HasFallback {
		t.Fatalf("assistant model profile lost configured display name: %#v", profile)
	}
}

func TestAssistantModelProfileUsesCatalogDisplayName(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"type":"com.beeper.ai.model_list","data":[{"id":"openai/gpt-5.5","name":"GPT 5.5 Catalog","runtime":{"provider":"openai","model":"gpt-5.5","api":"openai-responses","base_url":"/proxy/openai/v1"}}]}`))
	}))
	defer server.Close()

	client := &Client{
		Main: &Connector{AppServiceToken: "as-token"},
		UserLogin: &bridgev2.UserLogin{UserLogin: &database.UserLogin{
			UserMXID: "@test:beeper.com",
			Metadata: &aiid.UserLoginMetadata{Providers: map[string]aiid.ProviderConfig{aiid.DefaultProvider: {
				ID:           aiid.DefaultProvider,
				DisplayName:  "Beeper AI",
				API:          ai.ApiOpenAIResponses,
				Provider:     ai.ProviderOpenAI,
				BaseURL:      server.URL,
				DefaultModel: "beeper/default",
			}}},
		}},
	}
	content := &event.MessageEventContent{}
	client.applyModelProfile(context.Background(), content, "beeper", "openai/gpt-5.5")
	profile := content.BeeperPerMessageProfile
	if profile == nil || profile.ID != string(aiid.ModelContactID("beeper", "openai/gpt-5.5")) || profile.Displayname != "GPT 5.5 Catalog" || !profile.HasFallback {
		t.Fatalf("assistant model profile ignored catalog display name: %#v", profile)
	}
}

func TestApplyAIStreamDonePublishesProviderUsageInFinalAGUIEvents(t *testing.T) {
	run := aistream.NewRun("run", "thread", "beeper/gpt-5.5", "assistant:run", "GPT-5.5", timeNow())
	writer := aistream.NewWriter(run, timeNow)

	writer.Start()
	writer.Text("hello")
	applyAIStreamEvent(writer, ai.AssistantMessageEvent{
		Type:   "done",
		Reason: ai.StopReasonStop,
		Message: &ai.Message{
			Usage: ai.Usage{Input: 10, Output: 5, ReasoningTokens: 4, TotalTokens: 15},
		},
	}, 400000)

	want := agui.Usage{PromptTokens: 10, CompletionTokens: 5, ReasoningTokens: 4, TotalTokens: 15, ContextLimit: 400000}
	if run.Usage != want {
		t.Fatalf("run usage = %#v, want %#v", run.Usage, want)
	}
	var finished agui.Usage
	for _, evt := range run.Events {
		if evt.Type() == agui.EventRunFinished {
			finished = evt.Get("usage").(agui.Usage)
		}
	}
	if finished != want {
		t.Fatalf("RUN_FINISHED usage = %#v, want %#v", finished, want)
	}
}

func TestAGUIFinishReasonFromAIMapsAbortedToCancelled(t *testing.T) {
	if got := aguiFinishReasonFromAI(ai.StopReasonAborted); got != agui.FinishReasonCancelled {
		t.Fatalf("aborted finish reason = %q, want %q", got, agui.FinishReasonCancelled)
	}
}

func TestApplyAIStreamEventStreamsToolCallsFromPartialContent(t *testing.T) {
	run := aistream.NewRun("run", "thread", "beeper/gpt-5.5", "assistant:run", "GPT-5.5", timeNow())
	run.MessageID = "assistant:run"
	writer := aistream.NewWriter(run, timeNow)
	partial := &ai.Message{
		Role: "assistant",
		Content: []ai.ContentBlock{{
			Type:      "toolCall",
			ID:        "call-1",
			Name:      "read_file",
			Arguments: map[string]any{"path": "README.md"},
		}},
	}

	applyAIStreamEvent(writer, ai.AssistantMessageEvent{Type: "toolcall_start", ContentIndex: 0, Partial: partial})
	applyAIStreamEvent(writer, ai.AssistantMessageEvent{Type: "toolcall_delta", ContentIndex: 0, Delta: `{"path":"README.md"}`, Partial: partial})

	var sawStart, sawArgs bool
	for _, evt := range run.Events {
		switch evt.Type() {
		case agui.EventToolCallStart:
			sawStart = evt.Get("toolCallId") == "call-1" && evt.Get("toolName") == "read_file"
		case agui.EventToolCallArgs:
			sawArgs = evt.Get("toolCallId") == "call-1" && evt.Get("delta") == `{"path":"README.md"}`
			if args, ok := evt.Get("args").(map[string]any); !ok || args["path"] != "README.md" {
				t.Fatalf("expected streamed tool args, got %#v", evt.Get("args"))
			}
		}
	}
	if !sawStart || !sawArgs {
		t.Fatalf("missing streamed tool lifecycle events: %#v", run.Events)
	}
}

func TestApplyAIStreamEventStreamsNativeToolResult(t *testing.T) {
	run := aistream.NewRun("run", "thread", "beeper/gpt-5.5", "assistant:run", "GPT-5.5", timeNow())
	run.MessageID = "assistant:run"
	writer := aistream.NewWriter(run, timeNow)
	toolCall := &ai.ToolCall{
		ID:        "ws_1",
		Name:      "web_search",
		Arguments: map[string]any{"query": "latest headlines Amsterdam news today"},
	}

	applyAIStreamEvent(writer, ai.AssistantMessageEvent{Type: "toolcall_start", ToolCall: toolCall})
	applyAIStreamEvent(writer, ai.AssistantMessageEvent{
		Type:     "toolresult",
		ToolCall: toolCall,
		CustomValue: map[string]any{
			"state":    agui.ToolResultStateComplete,
			"status":   "success",
			"provider": "openai",
			"native":   true,
		},
	})

	var sawEnd, sawResult bool
	for _, evt := range run.Events {
		switch evt.Type() {
		case agui.EventToolCallEnd:
			sawEnd = evt.Get("toolCallId") == "ws_1" && evt.Get("toolName") == "web_search"
		case agui.EventToolCallResult:
			sawResult = evt.Get("toolCallId") == "ws_1"
			var output map[string]any
			if err := json.Unmarshal([]byte(evt.Get("content").(string)), &output); err != nil {
				t.Fatalf("native tool result content is not JSON: %v", err)
			}
			if output["status"] != "success" || output["native"] != true {
				t.Fatalf("unexpected native tool result output: %#v", output)
			}
		}
	}
	if !sawEnd || !sawResult {
		t.Fatalf("missing native tool result lifecycle events: %#v", run.Events)
	}
}

func TestApplyAIStreamEventSkipsEmptyToolCallDelta(t *testing.T) {
	run := aistream.NewRun("run", "thread", "beeper/gpt-5.5", "assistant:run", "GPT-5.5", timeNow())
	run.MessageID = "assistant:run"
	writer := aistream.NewWriter(run, timeNow)
	partial := &ai.Message{
		Role: "assistant",
		Content: []ai.ContentBlock{{
			Type:      "toolCall",
			ID:        "call-1",
			Name:      "web_search",
			Arguments: map[string]any{"query": "what is god"},
		}},
	}

	applyAIStreamEvent(writer, ai.AssistantMessageEvent{Type: "toolcall_start", ContentIndex: 0, Partial: partial})
	applyAIStreamEvent(writer, ai.AssistantMessageEvent{Type: "toolcall_delta", ContentIndex: 0, Partial: partial})
	applyAIStreamEvent(writer, ai.AssistantMessageEvent{Type: "toolcall_end", ContentIndex: 0, Partial: partial})

	for _, evt := range run.Events {
		if evt.Type() == agui.EventToolCallArgs {
			t.Fatalf("empty tool delta emitted TOOL_CALL_ARGS: %#v", evt)
		}
	}
	if _, err := aistream.PackRun(*run); err != nil {
		t.Fatalf("expected stream to remain packable: %v", err)
	}
}

func TestApplyAIStreamEventDoesNotPublishReasoningSignaturesToAGUI(t *testing.T) {
	run := aistream.NewRun("run", "thread", "beeper/gpt-5.5", "assistant:run", "GPT-5.5", timeNow())
	writer := aistream.NewWriter(run, timeNow)
	partial := &ai.Message{
		Role: "assistant",
		Content: []ai.ContentBlock{{
			Type:              "thinking",
			Thinking:          "hidden continuity",
			ThinkingSignature: "opaque-reasoning-state",
		}},
	}

	applyAIStreamEvent(writer, ai.AssistantMessageEvent{Type: "thinking_start", ContentIndex: 0, Partial: partial})
	applyAIStreamEvent(writer, ai.AssistantMessageEvent{Type: "thinking_delta", ContentIndex: 0, Delta: "hidden continuity", Partial: partial})
	applyAIStreamEvent(writer, ai.AssistantMessageEvent{Type: "thinking_end", ContentIndex: 0, Content: "hidden continuity", Partial: partial})

	for _, evt := range run.Events {
		if evt.Type() == agui.EventReasoningEncrypted {
			t.Fatalf("reasoning signatures must not be published as AG-UI events: %#v", run.Events)
		}
	}
}

func TestApplyAIStreamEventPublishesReasoningContentFromEndSnapshot(t *testing.T) {
	run := aistream.NewRun("run", "thread", "beeper/gpt-5.5", "assistant:run", "GPT-5.5", timeNow())
	writer := aistream.NewWriter(run, timeNow)
	partial := &ai.Message{
		Role: "assistant",
		Content: []ai.ContentBlock{{
			Type:     "thinking",
			Thinking: "final summary",
		}},
	}

	applyAIStreamEvent(writer, ai.AssistantMessageEvent{Type: "thinking_start", ContentIndex: 0, Partial: partial})
	applyAIStreamEvent(writer, ai.AssistantMessageEvent{Type: "thinking_end", ContentIndex: 0, Content: "final summary", Partial: partial})

	var reasoningContent []string
	for _, evt := range run.Events {
		if evt.Type() == agui.EventReasoningMsgCont {
			reasoningContent = append(reasoningContent, fmt.Sprint(evt.Get("delta")))
		}
	}
	if strings.Join(reasoningContent, "") != "final summary" {
		t.Fatalf("expected thinking_end content to be emitted before end, got events=%#v", run.Events)
	}
}

func TestApplyAIStreamEventDoesNotDuplicateReasoningEndSnapshot(t *testing.T) {
	run := aistream.NewRun("run", "thread", "beeper/gpt-5.5", "assistant:run", "GPT-5.5", timeNow())
	writer := aistream.NewWriter(run, timeNow)
	partial := &ai.Message{
		Role: "assistant",
		Content: []ai.ContentBlock{{
			Type:     "thinking",
			Thinking: "hidden continuity",
		}},
	}

	applyAIStreamEvent(writer, ai.AssistantMessageEvent{Type: "thinking_start", ContentIndex: 0, Partial: partial})
	applyAIStreamEvent(writer, ai.AssistantMessageEvent{Type: "thinking_delta", ContentIndex: 0, Delta: "hidden continuity", Partial: partial})
	applyAIStreamEvent(writer, ai.AssistantMessageEvent{Type: "thinking_end", ContentIndex: 0, Content: "hidden continuity", Partial: partial})

	var reasoningContent []string
	for _, evt := range run.Events {
		if evt.Type() == agui.EventReasoningMsgCont {
			reasoningContent = append(reasoningContent, fmt.Sprint(evt.Get("delta")))
		}
	}
	if strings.Join(reasoningContent, "") != "hidden continuity" || len(reasoningContent) != 1 {
		t.Fatalf("expected reasoning content once, got %#v events=%#v", reasoningContent, run.Events)
	}
}

func TestApplyAIStreamEventIgnoresRawProviderEvent(t *testing.T) {
	run := aistream.NewRun("run", "thread", "beeper/gpt-5.5", "assistant:run", "GPT-5.5", timeNow())
	writer := aistream.NewWriter(run, timeNow)
	raw := map[string]any{"type": "response.created", "response": map[string]any{"id": "resp-1"}}

	applyAIStreamEvent(writer, ai.AssistantMessageEvent{Type: "raw", RawEvent: raw, RawSource: "openai"})

	if len(run.Events) != 0 {
		t.Fatalf("raw provider events must not be published as AG-UI events: %#v", run.Events)
	}
}

func TestPublishToolOutputStreamsLiveResult(t *testing.T) {
	ctx := context.Background()
	publisher := &recordingStreamPublisher{}
	run := aistream.NewRun("run", "thread", "beeper/gpt-5.5", "assistant:run", "GPT-5.5", timeNow())
	run.MessageID = "assistant:run"
	writer := aistream.NewWriter(run, timeNow)
	writer.ToolStart("call-1", "get_session", 0, nil)
	active := &activeAIRun{streams: []*assistantStreamState{{
		eventID: "$event",
		run:     run,
		publish: streamPublishCursor{nextSeq: 1, published: len(run.Events)},
	}}}

	err := active.publishToolOutput(ctx, &Client{}, publisher, "!room:example.com", toolOutputEvent{
		ID:    "call-1",
		Name:  "get_session",
		Input: map[string]any{},
		Result: agent.AgentToolResult[any]{
			Content: []ai.ContentBlock{{Type: "text", Text: `{"session_id":"session-1"}`}},
			Details: map[string]any{
				"session_id": "session-1",
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(publisher.updates) != 1 {
		t.Fatalf("expected one live stream update, got %#v", publisher.updates)
	}
	if len(active.streams) != 1 || len(active.streams[0].tools) != 1 || active.streams[0].tools[0].ID != "call-1" {
		t.Fatalf("live tool output was not retained for finalization: %#v", active.streams)
	}
	aiPayload, ok := publisher.updates[0][aistream.BeeperAIKey].(aistream.BeeperAI)
	if !ok || len(aiPayload.Events) != 2 {
		t.Fatalf("unexpected live stream carrier %#v", publisher.updates[0])
	}
	part := aiPayload.Events[1].Event
	if part.Type() != agui.EventToolCallResult || part.Get("toolCallId") != "call-1" {
		t.Fatalf("expected live tool result event, got %#v", part)
	}
	result, ok := part.Get("content").(string)
	if !ok || result == "" {
		t.Fatalf("expected encoded tool result, got %#v", part.Get("content"))
	}
}

func TestStreamPublisherSkipsDuplicateToolResultSourcesAfterLiveToolOutput(t *testing.T) {
	ctx := context.Background()
	testAPI := ai.Api("test-duplicate-toolresult-sources")
	toolCall := &ai.ToolCall{
		ID:        "call-fetch",
		Name:      "fetch",
		Arguments: map[string]any{"url": "https://example.com"},
	}
	result := map[string]any{
		"url":         "https://example.com",
		"title":       "Example",
		"description": "Example source",
	}
	ai.RegisterAPIProvider(testAPI, func(ctx context.Context, model ai.Model, llmContext ai.Context, options ai.SimpleStreamOptions) *ai.AssistantMessageEventStream {
		stream := ai.NewAssistantMessageEventStream()
		go func() {
			stream.Push(ai.AssistantMessageEvent{Type: "toolresult", ToolCall: toolCall, CustomValue: result})
			stream.Push(ai.AssistantMessageEvent{Type: "done", Reason: ai.StopReasonStop, Message: &ai.Message{Role: "assistant", StopReason: ai.StopReasonStop}})
		}()
		return stream
	})
	defer ai.UnregisterAPIProvider(testAPI)

	publisher := &recordingStreamPublisher{}
	run := aistream.NewRun("run", "thread", "beeper/gpt-5.5", "assistant:run", "GPT-5.5", timeNow())
	run.MessageID = "assistant:run"
	writer := aistream.NewWriter(run, timeNow)
	writer.Start()
	writer.ToolStart(toolCall.ID, toolCall.Name, 0, nil)
	active := &activeAIRun{streams: []*assistantStreamState{{
		eventID: "$event",
		run:     run,
		sources: newSourceCollector(),
		publish: streamPublishCursor{
			nextSeq:   1,
			published: len(run.Events),
			started:   true,
		},
	}}}

	err := active.publishToolOutput(ctx, &Client{}, publisher, "!room:example.com", toolOutputEvent{
		ID:    toolCall.ID,
		Name:  toolCall.Name,
		Input: toolCall.Arguments,
		Result: agent.AgentToolResult[any]{
			Details: result,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	sourceEventsBefore := sourceCustomEventCount(run.Events)
	if sourceEventsBefore != 1 {
		t.Fatalf("expected live tool output to publish one source, got %d events=%#v", sourceEventsBefore, run.Events)
	}

	stream := active.streams[0]
	response := (&Client{}).streamPublisherWithEndFrom(publisher, "!room:example.com", "$event", run, &stream.publish, nil)(ctx, ai.Model{ID: "fake", API: testAPI}, ai.Context{}, ai.SimpleStreamOptions{}).Result()
	if response.StopReason != ai.StopReasonStop {
		t.Fatalf("unexpected stream response %#v", response)
	}
	if sourceEventsAfter := sourceCustomEventCount(run.Events); sourceEventsAfter != sourceEventsBefore {
		t.Fatalf("duplicate toolresult re-emitted sources: before=%d after=%d events=%#v", sourceEventsBefore, sourceEventsAfter, run.Events)
	}
}

func TestPublishNewStreamEventsSuppressesMautrixRequestBodyLogs(t *testing.T) {
	logger := zerolog.New(io.Discard).Level(zerolog.DebugLevel)
	ctx := logger.WithContext(context.Background())
	publisher := &recordingStreamPublisher{}
	run := aistream.NewRun("run", "thread", "beeper/gpt-5.5", "assistant:run", "GPT-5.5", timeNow())
	run.MessageID = "assistant:run"
	writer := aistream.NewWriter(run, timeNow)
	writer.Start()
	writer.Text("hello")

	err := (&Client{}).publishNewStreamEvents(ctx, publisher, "!room:example.com", "$event", run, &streamPublishCursor{})
	if err != nil {
		t.Fatal(err)
	}
	if len(publisher.publishLogLevels) != 1 {
		t.Fatalf("expected one stream publish, got %d", len(publisher.publishLogLevels))
	}
	if publisher.publishLogLevels[0] < zerolog.FatalLevel {
		t.Fatalf("stream carrier publish should suppress mautrix request body logs, got level %s", publisher.publishLogLevels[0])
	}
}

func textContentMessageIDs(events []agui.Event) []string {
	var ids []string
	for _, evt := range events {
		if evt.Type() != agui.EventTextMessageContent && evt.Type() != agui.EventTextMessageChunk {
			continue
		}
		delta, _ := evt.Get("delta").(string)
		if delta == "" {
			continue
		}
		messageID, _ := evt.Get("messageId").(string)
		ids = append(ids, messageID)
	}
	return ids
}

func toolParentMessageIDs(events []agui.Event) map[string]string {
	parents := map[string]string{}
	for _, evt := range events {
		if evt.Type() != agui.EventToolCallStart {
			continue
		}
		parents[firstNonEmptyString(evt.Get("toolCallId"))] = firstNonEmptyString(evt.Get("parentMessageId"))
	}
	return parents
}

func sourceCustomEventCount(events []agui.Event) int {
	count := 0
	for _, evt := range events {
		if evt.Type() == agui.EventCustom && evt.Get("name") == "com.beeper.source" {
			count++
		}
	}
	return count
}

func uiPartSummary(parts []aistream.MessagePart) []string {
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		switch part["type"] {
		case "text", "thinking":
			out = append(out, fmt.Sprintf("%s:%s", part["type"], part["content"]))
		case "tool-call":
			out = append(out, fmt.Sprintf("tool-call:%s", firstNonEmptyString(part["toolCallId"], part["id"])))
		}
	}
	return out
}

func firstNonEmptyString(values ...any) string {
	for _, value := range values {
		if text, _ := value.(string); text != "" {
			return text
		}
	}
	return ""
}

func testAIStore(t *testing.T) *aidb.Store {
	t.Helper()
	rawDB, err := sql.Open("sqlite3", filepath.Join(t.TempDir(), "bridge.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = rawDB.Close()
	})
	db, err := dbutil.NewWithDB(rawDB, "sqlite3")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = db.Close()
	})
	store := aidb.NewStore(db, networkid.BridgeID("bridge"), dbutil.ZeroLogger(zerolog.Nop()))
	if err := store.Upgrade(context.Background()); err != nil {
		t.Fatal(err)
	}
	return store
}

type recordingStreamPublisher struct {
	updates          []map[string]any
	publishLogLevels []zerolog.Level
	descriptorCalls  int
}

func (p *recordingStreamPublisher) NewDescriptor(ctx context.Context, roomID id.RoomID, streamType string) (*event.BeeperStreamInfo, error) {
	p.descriptorCalls++
	return &event.BeeperStreamInfo{UserID: "@bot:example.com", Type: streamType}, nil
}

func (p *recordingStreamPublisher) Register(ctx context.Context, roomID id.RoomID, eventID id.EventID, descriptor *event.BeeperStreamInfo) error {
	return nil
}

func (p *recordingStreamPublisher) Publish(ctx context.Context, roomID id.RoomID, eventID id.EventID, delta map[string]any) error {
	p.updates = append(p.updates, delta)
	p.publishLogLevels = append(p.publishLogLevels, zerolog.Ctx(ctx).GetLevel())
	return nil
}

func (p *recordingStreamPublisher) Unregister(roomID id.RoomID, eventID id.EventID) {
}

func timeNow() time.Time {
	return time.Unix(1, 0)
}
