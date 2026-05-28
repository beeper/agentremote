package aistream

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/beeper/ai-bridge/pkg/ag-ui"
)

func TestPackRunSplitsOver64KBAndReconstructs(t *testing.T) {
	run := NewRun("run-1", "thread-1", DefaultModel, "ai", "AI", time.Unix(10, 0))
	writer := NewWriter(run, func() time.Time { return time.Unix(10, 0) })
	writer.Start()
	writer.Text(strings.Repeat("a", 70*1024))
	writer.Finish(agui.FinishReasonStop)

	carriers, err := PackRun(*run, "$anchor", CarrierBudgetBytes)
	if err != nil {
		t.Fatal(err)
	}
	if len(carriers) < 2 {
		t.Fatalf("expected multiple carriers for over-64KB output, got %d", len(carriers))
	}
	for i, carrier := range carriers {
		if size := JSONSize(CarrierContent(carrier.Envelopes)); size > CarrierBudgetBytes {
			t.Fatalf("carrier %d is %d bytes, budget %d", i, size, CarrierBudgetBytes)
		}
	}
	if got := ReconstructText(carriers); got != strings.Repeat("a", 70*1024) {
		t.Fatalf("reconstructed text length = %d", len(got))
	}
}

func TestPackRunDoesNotPutFinalizationTotalsOnStreamEnvelopes(t *testing.T) {
	run := NewRun("run-1", "thread-1", DefaultModel, "ai", "AI", time.Unix(10, 0))
	writer := NewWriter(run, func() time.Time { return time.Unix(10, 0) })
	writer.Start()
	writer.Text("hello")
	writer.Finish(agui.FinishReasonStop)

	carriers, err := PackRun(*run, "$anchor", CarrierBudgetBytes)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(CarrierContent(carriers[0].Envelopes))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "seqTotal") {
		t.Fatalf("stream envelopes must not contain finalization totals: %s", raw)
	}
}

func TestFinalSnapshotUsesCanonicalMessagesAndCompactsLargeContent(t *testing.T) {
	run := NewRun("run-1", "thread-1", DefaultModel, "ai", "AI", time.Unix(10, 0))
	writer := NewWriter(run, func() time.Time { return time.Unix(10, 0) })
	writer.Start()
	writer.Thinking(strings.Repeat("t", 12*1024))
	writer.Text(strings.Repeat("a", 70*1024))
	writer.ToolStart("tool-1", "fetch", 0, nil)
	writer.ToolArgs("tool-1", `{"url":"https://example.com"}`, `{"url":"https://example.com"}`)
	writer.ToolEnd("tool-1", "fetch", `{"url":"https://example.com"}`, map[string]any{"ok": true})
	writer.Finish(agui.FinishReasonStop)

	carriers, err := PackRun(*run, "$anchor", CarrierBudgetBytes)
	if err != nil {
		t.Fatal(err)
	}
	var snapshots int
	var sawMetadata bool
	var sawAssistant bool
	var sawReasoning bool
	var sawToolResult bool
	for i, carrier := range carriers {
		if size := JSONSize(CarrierContent(carrier.Envelopes)); size > CarrierBudgetBytes {
			t.Fatalf("carrier %d is %d bytes, budget %d", i, size, CarrierBudgetBytes)
		}
		for _, env := range carrier.Envelopes {
			switch env.Part["type"] {
			case agui.EventMessagesSnapshot:
				snapshots++
				messages, ok := env.Part["messages"].([]any)
				if !ok || len(messages) == 0 {
					t.Fatalf("bad final snapshot: %#v", env.Part["messages"])
				}
				for _, rawMessage := range messages {
					message, ok := rawMessage.(map[string]any)
					if !ok {
						t.Fatalf("bad final snapshot message: %#v", rawMessage)
					}
					switch message["role"] {
					case agui.RoleAssistant:
						sawAssistant = true
						metadata, ok := message["metadata"].(map[string]any)
						if ok && metadata["runId"] == "run-1" {
							sawMetadata = true
						}
						if metadata["contentTruncated"] != true {
							t.Fatalf("large assistant snapshot should be compacted: %#v", message)
						}
					case "reasoning":
						sawReasoning = true
						metadata, _ := message["metadata"].(map[string]any)
						if metadata["contentTruncated"] != true {
							t.Fatalf("large reasoning snapshot should be compacted: %#v", message)
						}
					case agui.RoleTool:
						sawToolResult = true
					}
				}
			}
		}
	}
	if snapshots != 1 || !sawMetadata || !sawAssistant || !sawReasoning || !sawToolResult {
		t.Fatalf("expected canonical compact snapshot with assistant/reasoning/tool messages, snapshots=%d metadata=%v assistant=%v reasoning=%v tool=%v", snapshots, sawMetadata, sawAssistant, sawReasoning, sawToolResult)
	}
}

func TestPackRunUsesDeltaEventsInsteadOfAccumulatedText(t *testing.T) {
	run := NewRun("run-1", "thread-1", DefaultModel, "ai", "AI", time.Unix(10, 0))
	tick := int64(10)
	writer := NewWriter(run, func() time.Time {
		tick++
		return time.Unix(tick, 0)
	})
	writer.Start()
	writer.Text("abc")
	writer.Text("def")
	writer.Finish(agui.FinishReasonStop)

	carriers, err := PackRun(*run, "$anchor", CarrierBudgetBytes)
	if err != nil {
		t.Fatal(err)
	}
	if len(carriers) != 1 {
		t.Fatalf("under-budget run should be packed into one carrier, got %d", len(carriers))
	}
	var deltas []string
	for _, carrier := range carriers {
		for _, env := range carrier.Envelopes {
			if env.Part["type"] == agui.EventTextMessageContent {
				deltas = append(deltas, env.Part["delta"].(string))
			}
		}
	}
	if strings.Join(deltas, "|") != "abc|def" {
		t.Fatalf("expected original deltas only, got %#v", deltas)
	}
}

func TestWriterKeepsReasoningMessagesSeparate(t *testing.T) {
	run := NewRun("run-1", "thread-1", DefaultModel, "ai", "AI", time.Unix(10, 0))
	writer := NewWriter(run, func() time.Time { return time.Unix(10, 0) })
	writer.Start()
	writer.Thinking("first thought")
	writer.Thinking("second thought")
	writer.Text("answer")
	writer.Finish(agui.FinishReasonStop)

	var reasoning []string
	for _, message := range run.Messages(true) {
		if message.Role == "reasoning" {
			reasoning = append(reasoning, message.Content.(string))
		}
	}
	if strings.Join(reasoning, "|") != "first thought|second thought" {
		t.Fatalf("reasoning messages were not preserved individually: %#v", reasoning)
	}

	uiMessage := run.FinalBeeperUIMessage(0, true)
	var thinkingParts []string
	for _, part := range uiMessage.Parts {
		if part["type"] == "thinking" {
			thinkingParts = append(thinkingParts, part["content"].(string))
		}
	}
	if strings.Join(thinkingParts, "|") != "first thought|second thought" {
		t.Fatalf("thinking parts were not preserved individually: %#v", uiMessage.Parts)
	}
}

func TestInterleavedReasoningContentStaysSeparateInFinalProjections(t *testing.T) {
	run := NewRun("run-1", "thread-1", DefaultModel, "ai", "AI", time.Unix(10, 0))
	builder := agui.NewEventBuilder(DefaultModel, func() time.Time { return time.Unix(10, 0) })
	run.Status = Status{State: "complete", FinishReason: agui.FinishReasonStop}
	run.Events = append(run.Events,
		builder.RunStarted("thread-1", "run-1"),
		builder.ReasoningMessageStart("reasoning-1"),
		builder.ReasoningMessageContent("reasoning-1", "checked calendar"),
		builder.ToolCallStart("msg-run-1", "tool-1", "fetch", nil),
		builder.ToolCallEnd("tool-1", "fetch", map[string]any{"query": "events"}, agui.ToolStateInputComplete),
		builder.ToolCallResult("tool-tool-1", "tool-1", `{"ok":true}`, agui.ToolResultStateComplete, agui.RoleTool),
		builder.ReasoningMessageContent("reasoning-1", "checked issues"),
		builder.ReasoningMessageEnd("reasoning-1"),
		builder.RunFinished("thread-1", "run-1", agui.FinishReasonStop, agui.Usage{}),
	)
	if err := run.Validate(); err != nil {
		t.Fatal(err)
	}

	var reasoning []string
	for _, message := range run.Messages(true) {
		if message.Role == "reasoning" {
			reasoning = append(reasoning, message.Content.(string))
		}
	}
	if strings.Join(reasoning, "|") != "checked calendar|checked issues" {
		t.Fatalf("interleaved reasoning messages were not preserved individually: %#v", reasoning)
	}

	uiMessage := run.FinalBeeperUIMessage(0, true)
	var thinkingParts []string
	for _, part := range uiMessage.Parts {
		if part["type"] == "thinking" {
			thinkingParts = append(thinkingParts, part["content"].(string))
			if part["state"] != agui.PartStateDone {
				t.Fatalf("terminal thinking part should be done, got %#v", part)
			}
		}
	}
	if strings.Join(thinkingParts, "|") != "checked calendar|checked issues" {
		t.Fatalf("interleaved thinking parts were not preserved individually: %#v", uiMessage.Parts)
	}
}

func TestRawEventIsTruncatedBeforePacking(t *testing.T) {
	run := NewRun("run-1", "thread-1", DefaultModel, "ai", "AI", time.Unix(10, 0))
	builder := agui.NewEventBuilder(DefaultModel, func() time.Time { return time.Unix(10, 0) })
	run.Events = append(run.Events, builder.Custom("com.beeper.debug", map[string]any{"ok": true}))
	run.Events[0]["rawEvent"] = strings.Repeat("x", CarrierBudgetBytes)

	carriers, err := PackRun(*run, "$anchor", CarrierBudgetBytes)
	if err != nil {
		t.Fatal(err)
	}
	part := carriers[0].Envelopes[0].Part
	if part["rawEventTruncated"] != true {
		t.Fatalf("expected rawEventTruncated marker, got %#v", part)
	}
	if size := JSONSize(CarrierContent(carriers[0].Envelopes)); size > CarrierBudgetBytes {
		t.Fatalf("carrier size = %d, budget %d", size, CarrierBudgetBytes)
	}
}

func TestRawAGUIEventIsTruncatedBeforePacking(t *testing.T) {
	run := NewRun("run-1", "thread-1", DefaultModel, "ai", "AI", time.Unix(10, 0))
	builder := agui.NewEventBuilder(DefaultModel, func() time.Time { return time.Unix(10, 0) })
	run.Events = append(run.Events, builder.Raw(map[string]any{
		"type": "response.large",
		"data": strings.Repeat("x", CarrierBudgetBytes),
	}, "openai"))

	carriers, err := PackRun(*run, "$anchor", CarrierBudgetBytes)
	if err != nil {
		t.Fatal(err)
	}
	part := carriers[0].Envelopes[0].Part
	if part["rawEventTruncated"] != true {
		t.Fatalf("expected raw event truncation marker, got %#v", part)
	}
	if size := JSONSize(CarrierContent(carriers[0].Envelopes)); size > CarrierBudgetBytes {
		t.Fatalf("carrier size = %d, budget %d", size, CarrierBudgetBytes)
	}
}

func TestPackRunRejectsOversizedNonTextEvent(t *testing.T) {
	run := NewRun("run-1", "thread-1", DefaultModel, "ai", "AI", time.Unix(10, 0))
	builder := agui.NewEventBuilder(DefaultModel, func() time.Time { return time.Unix(10, 0) })
	run.Events = append(run.Events, builder.Custom("com.beeper.large", map[string]any{
		"value": strings.Repeat("x", CarrierBudgetBytes),
	}))

	_, err := PackRun(*run, "$anchor", CarrierBudgetBytes)
	if err == nil {
		t.Fatal("expected oversized non-text event to fail packing")
	}
}

func TestValidateRejectsLegacyOrInvalidToolResultShape(t *testing.T) {
	run := NewRun("run-1", "thread-1", DefaultModel, "ai", "AI", time.Unix(10, 0))
	builder := agui.NewEventBuilder(DefaultModel, func() time.Time { return time.Unix(10, 0) })
	run.Events = append(run.Events,
		builder.RunStarted("thread-1", "run-1"),
		builder.ToolCallStart("msg-run-1", "tool-1", "fetch", nil),
		agui.Event{"type": agui.EventToolCallEnd, "toolCallId": "tool-1", "result": `{"ok":true}`, "state": agui.ToolStateInputComplete},
	)
	if err := run.Validate(); err == nil {
		t.Fatal("expected validation error for legacy TOOL_CALL_END.result")
	}
}

func TestFinalBeeperUIMessageCarriesToolCallMetadata(t *testing.T) {
	run := NewRun("run-1", "thread-1", DefaultModel, "ai", "AI", time.Unix(10, 0))
	writer := NewWriter(run, func() time.Time { return time.Unix(10, 0) })
	writer.ToolStartWithMetadata("tool-1", "calendar.get_events", 0, nil, map[string]any{
		"displayName": "List Calendar Events",
		"iconUrl":     "mxc://beeper.com/calendar",
	})

	message := run.FinalBeeperUIMessage(0, true)
	if len(message.Parts) != 1 {
		t.Fatalf("expected one part, got %#v", message.Parts)
	}
	metadata, ok := message.Parts[0]["metadata"].(map[string]any)
	if !ok || metadata["displayName"] != "List Calendar Events" || metadata["iconUrl"] != "mxc://beeper.com/calendar" {
		t.Fatalf("bad tool metadata: %#v", message.Parts[0])
	}
}

func TestFinalBeeperUIMessageCarriesParsedToolOutputs(t *testing.T) {
	run := NewRun("run-1", "thread-1", DefaultModel, "ai", "AI", time.Unix(10, 0))
	writer := NewWriter(run, func() time.Time { return time.Unix(10, 0) })
	writer.ToolStart("tool-1", "fetch", 0, nil)
	writer.ToolArgs("tool-1", `{"url":"https://example.com"}`, `{"url":"https://example.com"}`)
	writer.ToolEnd("tool-1", "fetch", map[string]any{"url": "https://example.com"}, nil)
	writer.ToolStart("tool-2", "files", 1, nil)
	writer.ToolError("tool-2", "files", map[string]any{"path": "/tmp/nope"}, "missing")

	message := run.FinalBeeperUIMessage(0, true)
	if len(message.Parts) != 2 {
		t.Fatalf("expected two tool parts, got %#v", message.Parts)
	}
	success, ok := message.Parts[0]["output"].(map[string]any)
	if !ok || success["state"] != agui.ToolResultStateComplete || success["status"] != "success" {
		t.Fatalf("success tool without result should emit terminal success output: %#v", message.Parts[0])
	}
	failure, ok := message.Parts[1]["output"].(map[string]any)
	if !ok || failure["state"] != agui.ToolResultStateError || failure["status"] != "failed" || failure["reason"] != "missing" {
		t.Fatalf("failed tool output should be parsed and terminal: %#v", message.Parts[1])
	}
}

func TestFinalBeeperUIMessageCollapsesToolResultIntoToolCall(t *testing.T) {
	run := NewRun("run-1", "thread-1", DefaultModel, "ai", "AI", time.Unix(10, 0))
	writer := NewWriter(run, func() time.Time { return time.Unix(10, 0) })
	writer.ToolStart("tool-1", "fetch", 0, nil)
	writer.ToolResult("tool-1", `{"ok":true}`, agui.ToolResultStateComplete)

	message := run.FinalBeeperUIMessage(0, true)
	if len(message.Parts) != 1 {
		t.Fatalf("expected tool result to be folded into one tool-call part, got %#v", message.Parts)
	}
	if message.Parts[0]["type"] == "tool-result" {
		t.Fatalf("final UI message must not persist standalone tool-result parts: %#v", message.Parts)
	}
	output, ok := message.Parts[0]["output"].(map[string]any)
	if !ok || output["ok"] != true || output["state"] != agui.ToolResultStateComplete || output["status"] != "success" {
		t.Fatalf("tool result was not folded into tool output: %#v", message.Parts[0])
	}
}

func TestFinalBeeperUIMessageFailsOpenToolsWhenRunFinalized(t *testing.T) {
	run := NewRun("run-1", "thread-1", DefaultModel, "ai", "AI", time.Unix(10, 0))
	writer := NewWriter(run, func() time.Time { return time.Unix(10, 0) })
	writer.ToolStart("tool-1", "summarize", 0, nil)
	writer.ToolStart("tool-2", "calendar", 1, nil)
	writer.Finish(agui.FinishReasonStop)

	message := run.FinalBeeperUIMessage(0, true)
	if len(message.Parts) != 2 {
		t.Fatalf("expected two tool parts, got %#v", message.Parts)
	}
	for _, part := range message.Parts {
		if part["state"] != agui.ToolStateInputComplete {
			t.Fatalf("open tool should be finalized as input-complete: %#v", part)
		}
		output, ok := part["output"].(map[string]any)
		if !ok || output["state"] != agui.ToolResultStateError || output["status"] != "failed" {
			t.Fatalf("open tool should get terminal failed output: %#v", part)
		}
	}
}

func TestFinalBeeperUIMessageCarriesTopLevelArtifactsWithStableIDs(t *testing.T) {
	run := NewRun("run-1", "thread-1", DefaultModel, "ai", "AI", time.Unix(10, 0))
	writer := NewWriter(run, func() time.Time { return time.Unix(10, 0) })
	writer.Custom("com.beeper.source", map[string]any{
		"sourceId": "source-1",
		"url":      "https://example.com/source",
		"title":    "Example Source",
	})
	writer.Custom("com.beeper.document", map[string]any{
		"id":        "doc-1",
		"title":     "Example Doc",
		"mediaType": "text/plain",
	})
	writer.Custom("com.beeper.file", map[string]any{
		"url":       "mxc://example/file",
		"mediaType": "application/octet-stream",
	})

	message := run.FinalBeeperUIMessage(0, true)
	if len(message.Parts) != 3 {
		t.Fatalf("expected artifact parts, got %#v", message.Parts)
	}
	if message.Parts[0]["type"] != "source-url" || message.Parts[0]["sourceId"] != "source-1" || message.Parts[0]["url"] != "https://example.com/source" {
		t.Fatalf("bad source part shape: %#v", message.Parts[0])
	}
	if _, hasNestedSource := message.Parts[0]["source"]; hasNestedSource {
		t.Fatalf("source part should not nest payload: %#v", message.Parts[0])
	}
	if message.Parts[1]["type"] != "source-document" || message.Parts[1]["sourceId"] != "doc-1" || message.Parts[1]["id"] != "doc-1" {
		t.Fatalf("bad document part shape: %#v", message.Parts[1])
	}
	if message.Parts[2]["type"] != "file" || message.Parts[2]["url"] != "mxc://example/file" {
		t.Fatalf("bad file part shape: %#v", message.Parts[2])
	}
	if _, hasNestedFile := message.Parts[2]["file"]; hasNestedFile {
		t.Fatalf("file part should not nest payload: %#v", message.Parts[2])
	}
}

func TestApprovalResolverMatchesEmojiKeysAndAliases(t *testing.T) {
	choices := DefaultApprovalChoices()
	for _, key := range []string{"✅", "approve"} {
		choice, ok := ResolveApprovalChoice(choices, key)
		response := ApprovalResponseForChoice("approval-1", choice)
		if !ok || !response.Approved || response.Always {
			t.Fatalf("expected approve for %q, got %#v ok=%v", key, choice, ok)
		}
	}
	choice, ok := ResolveApprovalChoice(choices, "☑️")
	response := ApprovalResponseForChoice("approval-1", choice)
	if !ok || !response.Approved || !response.Always {
		t.Fatalf("expected always-approve, got %#v ok=%v", choice, ok)
	}
	choice, ok = ResolveApprovalChoice(choices, "deny")
	response = ApprovalResponseForChoice("approval-1", choice)
	if !ok || response.Approved || response.Reason != "denied" {
		t.Fatalf("expected denial, got %#v ok=%v", choice, ok)
	}
}

func TestApprovalInterruptOwnsStreamPayloadShape(t *testing.T) {
	run := NewRun("run-1", "thread-1", DefaultModel, "ai", "AI", time.Unix(10, 0))
	run.MessageID = "msg-run-1"
	approval := ToolApproval{ID: "approval-1", NeedsApproval: true}

	interrupt := NewApprovalInterrupt(*run, "tool-1", "fetch", map[string]any{"url": "https://example.com"}, approval, map[string]any{"displayName": "Fetch"})

	if interrupt.ID != "approval-1" || interrupt.Reason != agui.InterruptReasonToolCall || interrupt.ToolCallID != "tool-1" {
		t.Fatalf("bad interrupt identifiers: %#v", interrupt)
	}
	if interrupt.Message == "" || interrupt.ResponseSchema["type"] != "object" {
		t.Fatalf("bad interrupt schema/message: %#v", interrupt)
	}
	if interrupt.Metadata["threadId"] != "thread-1" || interrupt.Metadata["runId"] != "run-1" || interrupt.Metadata["messageId"] != "msg-run-1" {
		t.Fatalf("bad run metadata: %#v", interrupt.Metadata)
	}
	if interrupt.Metadata["toolName"] != "fetch" || interrupt.Metadata["approvalMessageId"] != "approval-1" {
		t.Fatalf("bad tool metadata: %#v", interrupt.Metadata)
	}
	choices, ok := interrupt.Metadata["choices"].([]ApprovalChoice)
	if !ok || len(choices) != len(DefaultApprovalChoices()) || choices[0].Key != ApprovalChoiceApprove {
		t.Fatalf("bad approval choices: %#v", interrupt.Metadata["choices"])
	}
	if nested, ok := interrupt.Metadata["metadata"].(map[string]any); !ok || nested["displayName"] != "Fetch" {
		t.Fatalf("bad nested metadata: %#v", interrupt.Metadata["metadata"])
	}
	if _, ok := interrupt.Metadata["approvalEventId"]; ok {
		t.Fatalf("approval event id should only be added after Matrix send: %#v", interrupt.Metadata)
	}
	if !SetApprovalInterruptEventID(&interrupt, "$approval") || interrupt.Metadata["approvalEventId"] != "$approval" {
		t.Fatalf("failed to annotate approval event id: %#v", interrupt.Metadata)
	}
}

func TestApprovalResponseSchemaMatchesPayloadType(t *testing.T) {
	typedSchema := NewApprovalResponseJSONSchema()
	if typedSchema.Type != agui.JSONSchemaTypeObject || typedSchema.Properties.Approved["type"] != agui.JSONSchemaTypeBoolean {
		t.Fatalf("bad typed approval response schema: %#v", typedSchema)
	}
	schema := ApprovalResponseSchema()
	props := jsonSchemaProperties(t, schema["properties"])
	if props == nil {
		t.Fatalf("approval schema properties = %#v, want object", schema["properties"])
	}
	payloadFields := jsonTaggedFieldNames(t, ApprovalResponsePayload{})
	if len(props) != len(payloadFields) {
		t.Fatalf("schema properties = %#v, want fields %#v", props, payloadFields)
	}
	for field := range payloadFields {
		if _, ok := props[field]; !ok {
			t.Fatalf("schema missing payload field %q: %#v", field, props)
		}
	}
	if _, ok := props["fields"]; ok {
		t.Fatalf("approval response schema should use editedArgs, not legacy fields: %#v", props)
	}
	required, ok := schema["required"].([]string)
	if !ok || len(required) != 1 || required[0] != "approved" {
		t.Fatalf("approval schema required = %#v, want [approved]", schema["required"])
	}
}

func jsonSchemaProperties(t *testing.T, value any) map[string]any {
	t.Helper()
	switch props := value.(type) {
	case agui.JSONSchemaProperties:
		out := make(map[string]any, len(props))
		for key, schema := range props {
			out[key] = schema
		}
		return out
	case map[string]any:
		return props
	default:
		return nil
	}
}

func TestApprovalHelpersOwnResumeAndToolResultShapes(t *testing.T) {
	response := ToolApprovalResponse{
		ID:         "approval-1",
		Approved:   true,
		Always:     true,
		EditedArgs: map[string]any{"command": "pwd"},
		Metadata:   map[string]any{"source": "test"},
	}

	resume := NewApprovalResumeEntry("approval-1", response)
	if resume.InterruptID != "approval-1" || resume.Status != agui.ResumeStatusResolved {
		t.Fatalf("bad resume entry: %#v", resume)
	}
	payload, ok := resume.Payload.(ApprovalResponsePayload)
	if !ok || !payload.Approved || !payload.Always || payload.EditedArgs["command"] != "pwd" {
		t.Fatalf("bad resume payload: %#v", resume.Payload)
	}
	roundTrip, ok := ApprovalResponseFromPayload("approval-1", payload)
	if !ok || !roundTrip.Approved || !roundTrip.Always || roundTrip.EditedArgs["command"] != "pwd" {
		t.Fatalf("bad resume response round trip: %#v ok=%v", roundTrip, ok)
	}

	result := ApprovalToolResultFromResponse(response)
	if result.ApprovalID != "approval-1" || !result.Approved || result.State != agui.ToolResultStateComplete || result.Status != "success" {
		t.Fatalf("bad approval tool result: %#v", result)
	}
	parsed, ok := ParseApprovalToolResult(asString(jsonString(result)))
	if !ok || parsed.ApprovalID != "approval-1" || parsed.EditedArgs["command"] != "pwd" {
		t.Fatalf("bad parsed approval tool result: %#v ok=%v", parsed, ok)
	}

	denied := DeniedApprovalToolResult("approval-2", "")
	if denied.ApprovalID != "approval-2" || denied.Approved || denied.State != agui.ToolResultStateError || denied.Reason != "denied" {
		t.Fatalf("bad denied approval result: %#v", denied)
	}
}

func jsonTaggedFieldNames(t *testing.T, value any) map[string]struct{} {
	t.Helper()
	typ := reflect.TypeOf(value)
	if typ.Kind() == reflect.Pointer {
		typ = typ.Elem()
	}
	if typ.Kind() != reflect.Struct {
		t.Fatalf("expected struct type, got %s", typ.Kind())
	}
	fields := make(map[string]struct{}, typ.NumField())
	for i := range typ.NumField() {
		tag := typ.Field(i).Tag.Get("json")
		name, _, _ := strings.Cut(tag, ",")
		if name == "" || name == "-" {
			continue
		}
		fields[name] = struct{}{}
	}
	return fields
}

func TestRunMetadataOwnsMatrixPayloadShape(t *testing.T) {
	run := NewRun("run-1", "thread-1", DefaultModel, "agent-1", "Agent", time.Unix(10, 0))
	run.MessageID = "msg-run-1"
	run.Usage = agui.Usage{PromptTokens: 1, CompletionTokens: 2, ReasoningTokens: 4, TotalTokens: 7}
	run.Preview = Preview{Text: "hello", Truncated: false}

	metadata := run.Metadata()

	if metadata["schema"] != "com.beeper.ai.run.v1" || metadata["protocol"] != "ag-ui" {
		t.Fatalf("bad protocol metadata: %#v", metadata)
	}
	if metadata["threadId"] != "thread-1" || metadata["runId"] != "run-1" || metadata["messageId"] != "msg-run-1" {
		t.Fatalf("bad run identifiers: %#v", metadata)
	}
	agent, ok := metadata["agent"].(map[string]any)
	if !ok || agent["id"] != "agent-1" || agent["displayName"] != "Agent" {
		t.Fatalf("bad agent metadata: %#v", metadata["agent"])
	}
	usage, ok := metadata["usage"].(map[string]any)
	if !ok || usage["promptTokens"] != 1 || usage["completionTokens"] != 2 || usage["reasoningTokens"] != 4 || usage["totalTokens"] != 7 {
		t.Fatalf("bad usage metadata: %#v", metadata["usage"])
	}
	usageDetails, ok := metadata["usageDetails"].(map[string]any)
	if !ok || usageDetails["reasoningTokens"] != 4 {
		t.Fatalf("usage details should always be present: %#v", metadata)
	}
}

func TestFinishWithUsageCarriesProviderUsageToTerminalEvents(t *testing.T) {
	run := NewRun("run-1", "thread-1", DefaultModel, "ai", "AI", time.Unix(10, 0))
	writer := NewWriter(run, func() time.Time { return time.Unix(10, 0) })
	writer.Start()
	writer.Text("hello")
	usage := agui.Usage{PromptTokens: 10, CompletionTokens: 5, ReasoningTokens: 4, TotalTokens: 15}
	writer.FinishWithUsage(agui.FinishReasonStop, &usage)

	if run.Usage != usage {
		t.Fatalf("run usage was not preserved: %#v", run.Usage)
	}
	var snapshotUsage, finishedUsage agui.Usage
	for _, evt := range run.Events {
		switch evt["type"] {
		case agui.EventMessagesSnapshot:
			messages := evt["messages"].([]agui.Message)
			for _, message := range messages {
				if message.ID == run.MessageID {
					snapshotUsage = message.Metadata["usage"].(agui.Usage)
				}
			}
		case agui.EventRunFinished:
			finishedUsage = evt["usage"].(agui.Usage)
		}
	}
	if snapshotUsage != usage || finishedUsage != usage {
		t.Fatalf("terminal events lost usage: snapshot=%#v finished=%#v", snapshotUsage, finishedUsage)
	}
}

func TestApprovalNoticeOwnsHiddenMessagePayloadShape(t *testing.T) {
	notice := NewApprovalNotice(ApprovalContext{
		ID:         "approval-1",
		MessageID:  "msg-run-1",
		ToolCallID: "tool-1",
		ToolName:   "fetch",
	}, DefaultApprovalChoices()).Map()

	if notice["schema"] != "com.beeper.ai.approval.v1" || notice["state"] != "requested" {
		t.Fatalf("bad approval notice metadata: %#v", notice)
	}
	if notice["id"] != "approval-1" || notice["messageId"] != "msg-run-1" || notice["toolCallId"] != "tool-1" || notice["toolName"] != "fetch" {
		t.Fatalf("bad approval notice identifiers: %#v", notice)
	}
	choices, ok := notice["choices"].([]any)
	if !ok || len(choices) != 3 {
		t.Fatalf("bad approval notice choices: %#v", notice["choices"])
	}
	first, ok := choices[0].(map[string]any)
	if !ok || first["key"] != ApprovalChoiceApprove || first["label"] != "Allow once" || first["alias"] != "✅" {
		t.Fatalf("bad first approval choice: %#v", choices[0])
	}
	if _, ok := first["style"]; ok {
		t.Fatalf("empty style should be omitted from approval choices: %#v", first)
	}
	deny, ok := choices[2].(map[string]any)
	if !ok || deny["style"] != "danger" {
		t.Fatalf("deny choice should keep danger style: %#v", choices[2])
	}
}

func TestCleanupKeepsSelectedUserReactionAndRemovesBridgeOptions(t *testing.T) {
	choices := DefaultApprovalChoices()
	cleanup := CleanupApprovalReactions(choices, "✅", []ReactionEvent{
		{EventID: "$bridge-allow", Sender: "ai", Key: "✅", Bridge: true},
		{EventID: "$bridge-deny", Sender: "ai", Key: "❌", Bridge: true},
		{EventID: "$user-allow", Sender: "@user:example", Key: "✅"},
		{EventID: "$user-deny", Sender: "@user:example", Key: "❌"},
	}, "ai")
	if !cleanup.Matched || cleanup.SelectedReactionEvent != "$user-allow" {
		t.Fatalf("bad selected reaction: %#v", cleanup)
	}
	got := strings.Join(cleanup.RedactReactionEvents, ",")
	if !strings.Contains(got, "$bridge-allow") || !strings.Contains(got, "$bridge-deny") || !strings.Contains(got, "$user-deny") {
		t.Fatalf("bad cleanup redactions: %#v", cleanup.RedactReactionEvents)
	}
}
