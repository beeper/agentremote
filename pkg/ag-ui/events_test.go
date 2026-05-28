package agui

import (
	"encoding/json"
	"testing"
	"time"
)

func TestBuildersCoverLifecycleEventsWithTimestamps(t *testing.T) {
	now := func() time.Time { return time.Unix(10, 0) }
	builder := NewEventBuilder("dummy/model", now)
	idx := 1
	events := []Event{
		builder.RunStarted("thread", "run"),
		builder.RunFinished("thread", "run", FinishReasonToolCalls, Usage{PromptTokens: 1, CompletionTokens: 2, TotalTokens: 3}),
		builder.RunError("thread", "run", "failed"),
		builder.TextMessageStart("msg", RoleAssistant),
		builder.TextMessageContent("msg", "hello"),
		builder.TextMessageEnd("msg"),
		builder.TextMessageChunk("msg", RoleAssistant, "hello"),
		builder.ReasoningStart("msg"),
		builder.ReasoningMessageStart("msg"),
		builder.ReasoningMessageContent("msg", "thinking"),
		builder.ReasoningMessageEnd("msg"),
		builder.ReasoningEnd("msg"),
		builder.ReasoningMessageChunk("msg", "thinking"),
		builder.ReasoningEncryptedValue("message", "msg", "encrypted"),
		builder.ToolCallStart("msg", "tool", "search", &idx),
		builder.ToolCallArgs("tool", `{"q":"he`, nil),
		builder.ToolCallEnd("tool", "search", map[string]any{"q": "hello"}, ToolStateInputComplete),
		builder.ToolCallChunk("tool", "search", "msg", `{"q":"he`),
		builder.ToolCallResult("msg", "tool", `{"ok":true}`, ToolResultStateComplete, RoleTool),
		builder.StepStarted("msg", "step"),
		builder.StepFinished("msg", "step"),
		builder.StateSnapshot(map[string]any{"open": true}),
		builder.StateDelta(map[string]any{"path": "/open", "value": false}),
		builder.MessagesSnapshot([]Message{{ID: "msg", Role: RoleAssistant, Content: "hello"}}),
		builder.ActivitySnapshot("msg", "thinking", map[string]any{"text": "working"}, nil),
		builder.ActivityDelta("msg", "thinking", []any{map[string]any{"op": "add", "path": "/text", "value": "working"}}),
		builder.Raw(map[string]any{"type": "provider.event"}, "openai"),
		builder.Custom("com.beeper.test", map[string]any{"ok": true}),
	}
	for _, evt := range events {
		if err := ValidateEvent(evt); err != nil {
			t.Fatalf("ValidateEvent(%s) returned error: %v", evt.Type(), err)
		}
		if evt.Get("timestamp") == nil {
			t.Fatalf("event missing timestamp: %#v", evt)
		}
	}
	if got := events[1].Get("finishReason"); got != FinishReasonToolCalls {
		t.Fatalf("finish reason = %q, want %q", got, FinishReasonToolCalls)
	}
	if outcome, ok := events[1].Get("outcome").(RunFinishedOutcome); !ok || outcome.Type != OutcomeSuccess {
		t.Fatalf("run finished outcome = %#v, want success", events[1].Get("outcome"))
	}
	if got := events[2].Get("message"); got != "failed" {
		t.Fatalf("run error message = %#v, want failed", got)
	}
	toolStart := events[14]
	if got := toolStart.Get("index"); got != 1 {
		t.Fatalf("tool index = %#v, want 1", got)
	}
	if got := toolStart.Get("parentMessageId"); got != "msg" {
		t.Fatalf("tool parentMessageId = %#v, want msg", got)
	}
	if toolStart.Has("messageId") {
		t.Fatalf("tool start should not emit deprecated messageId: %#v", toolStart)
	}
	if !events[21].Has("snapshot") {
		t.Fatalf("state snapshot should emit snapshot field: %#v", events[21])
	}
}

func TestZeroValueEventSetMutatesOriginal(t *testing.T) {
	var evt Event
	evt.Set("type", EventCustom)
	if evt.Type() != EventCustom {
		t.Fatalf("Set on zero-value Event did not mutate original: %#v", evt)
	}
}

func TestObjectSchemaCopiesProperties(t *testing.T) {
	properties := JSONSchemaProperties{"ok": BooleanSchema()}
	schema := ObjectSchema(properties)
	properties["ok"] = StringSchema()

	got := schema["properties"].(JSONSchemaProperties)["ok"]["type"]
	if got != JSONSchemaTypeBoolean {
		t.Fatalf("schema properties were externally mutated: %#v", schema)
	}
}

func TestValidateRejectsBadEvents(t *testing.T) {
	tests := []Event{
		NewEvent(nil),
		NewEvent(map[string]any{"type": EventRunStarted, "threadId": "thread"}),
		NewEvent(map[string]any{"type": EventRunError, "threadId": "thread", "error": map[string]any{"message": "failed"}}),
		NewEvent(map[string]any{"type": EventTextMessageContent, "messageId": "msg"}),
		NewEvent(map[string]any{"type": "REASONING_MESSAGE_CONTENT"}),
		NewEvent(map[string]any{"type": EventReasoningEncrypted, "subtype": "bad", "entityId": "msg", "encryptedValue": "x"}),
		NewEvent(map[string]any{"type": EventToolCallStart, "toolCallId": "tool", "toolCallName": "search", "state": "output-available"}),
		NewEvent(map[string]any{"type": EventToolCallArgs, "toolCallId": "tool", "delta": 12}),
		NewEvent(map[string]any{"type": EventToolCallEnd, "toolCallId": "tool", "result": `{"bad":true}`, "state": ToolStateInputComplete}),
		NewEvent(map[string]any{"type": EventToolCallResult, "messageId": "msg", "toolCallId": "tool", "content": "{}", "state": "output-error"}),
		NewEvent(map[string]any{"type": EventStepStarted, "stepId": "deprecated-only"}),
		NewEvent(map[string]any{"type": EventStateSnapshot, "state": map[string]any{}}),
		NewEvent(map[string]any{"type": EventActivitySnapshot, "messageId": "msg", "activityType": "thinking"}),
		NewEvent(map[string]any{"type": EventActivityDelta, "messageId": "msg", "activityType": "thinking"}),
		NewEvent(map[string]any{"type": EventRaw}),
	}
	for _, evt := range tests {
		if err := ValidateEvent(evt); err == nil {
			t.Fatalf("expected validation error for %#v", evt)
		}
	}
}

func TestValidateEventSequenceRejectsBadOrdering(t *testing.T) {
	now := func() time.Time { return time.Unix(10, 0) }
	builder := NewEventBuilder("dummy/model", now)

	valid := []Event{
		builder.RunStarted("thread", "run"),
		builder.TextMessageStart("msg", RoleAssistant),
		builder.TextMessageContent("msg", "hello"),
		builder.TextMessageEnd("msg"),
		builder.ToolCallStart("msg", "tool", "search", nil),
		builder.ToolCallArgs("tool", `{"q":"hello"}`, `{"q":"hello"}`),
		builder.ToolCallEnd("tool", "search", map[string]any{"q": "hello"}, ToolStateInputComplete),
		builder.RunFinished("thread", "run", FinishReasonStop, Usage{}),
	}
	if err := ValidateEventSequence(valid); err != nil {
		t.Fatalf("ValidateEventSequence(valid) returned error: %v", err)
	}

	tests := [][]Event{
		{builder.TextMessageContent("msg", "hello")},
		{builder.ReasoningMessageContent("msg", "thinking")},
		{builder.ToolCallArgs("tool", "{}", "{}")},
		{builder.ToolCallResult("msg", "tool", "{}", ToolResultStateComplete, RoleTool)},
		{
			builder.RunStarted("thread", "run"),
			builder.RunFinished("thread", "run", FinishReasonStop, Usage{}),
			builder.TextMessageStart("msg", RoleAssistant),
		},
	}
	for _, events := range tests {
		if err := ValidateEventSequence(events); err == nil {
			t.Fatalf("expected ordering error for %#v", events)
		}
	}
}

func TestRunAgentInputModelsBidirectionalShape(t *testing.T) {
	input := RunAgentInput{
		ThreadID: "thread",
		RunID:    "run",
		State:    map[string]any{"open": true},
		Messages: []Message{{
			ID:      "msg",
			Role:    RoleUser,
			Content: "hello",
		}},
		Resume: []ResumeEntry{{
			InterruptID: "approval-1",
			Status:      ResumeStatusResolved,
			Payload:     map[string]any{"approved": true},
		}},
		Tools: []Tool{{Name: "send_email", NeedsApproval: true}},
		Context: []ContextItem{{
			Type:  "beeper-room",
			Value: "room",
		}},
		ForwardedProps: map[string]any{"trace": "abc"},
		Data:           map[string]any{"legacy": true},
	}
	if input.ThreadID != "thread" || !input.Tools[0].NeedsApproval || input.Resume[0].Status != ResumeStatusResolved || input.ForwardedProps["trace"] != "abc" {
		t.Fatalf("bad RunAgentInput shape: %#v", input)
	}
}

func TestAgentCapabilitiesCanAdvertiseHITLInterruptsAndExplicitFalse(t *testing.T) {
	caps := AgentCapabilities{
		Transport: &TransportCapabilities{
			Streaming: Bool(true),
		},
		Reasoning: &ReasoningCapabilities{
			Supported: Bool(false),
		},
		HumanInTheLoop: &HumanInTheLoopCapabilities{
			Supported:        Bool(true),
			Approvals:        Bool(true),
			Interrupts:       Bool(true),
			ApproveWithEdits: Bool(true),
		},
	}

	raw, err := json.Marshal(caps)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	reasoning := decoded["reasoning"].(map[string]any)
	if reasoning["supported"] != false {
		t.Fatalf("reasoning supported should preserve explicit false: %s", raw)
	}
	hitl := decoded["humanInTheLoop"].(map[string]any)
	if hitl["interrupts"] != true || hitl["approveWithEdits"] != true {
		t.Fatalf("HITL capabilities missing interrupt/edit support: %s", raw)
	}
}

func TestRunFinishedInterruptOutcomeValidatesToolCallInterrupts(t *testing.T) {
	builder := NewEventBuilder("dummy/model", func() time.Time { return time.Unix(10, 0) })
	evt := builder.RunFinishedWithOutcome("thread", "run", FinishReasonToolCalls, Usage{}, RunFinishedOutcome{
		Type: OutcomeInterrupt,
		Interrupts: []Interrupt{{
			ID:         "approval-1",
			Reason:     InterruptReasonToolCall,
			ToolCallID: "tool-1",
			ResponseSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"approved": map[string]any{"type": "boolean"},
				},
				"required": []string{"approved"},
			},
		}},
	})
	if err := ValidateEvent(evt); err != nil {
		t.Fatalf("valid interrupt outcome failed validation: %v", err)
	}
	bad := builder.RunFinishedWithOutcome("thread", "run", FinishReasonToolCalls, Usage{}, RunFinishedOutcome{
		Type:       OutcomeInterrupt,
		Interrupts: []Interrupt{{ID: "approval-1", Reason: InterruptReasonToolCall}},
	})
	if err := ValidateEvent(bad); err == nil {
		t.Fatal("expected tool_call interrupt without toolCallId to fail validation")
	}
}
