package aistream

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	agui "github.com/beeper/ai-bridge/pkg/ag-ui"
)

func TestPackRunDoesNotSplitOrTruncateBySize(t *testing.T) {
	run := NewRun("run-1", "thread-1", DefaultModel, "ai", "AI", time.Unix(10, 0))
	writer := NewWriter(run, func() time.Time { return time.Unix(10, 0) })
	writer.Start()
	writer.Text(strings.Repeat("a", 70*1024))
	writer.Finish(agui.FinishReasonStop)

	carriers, err := PackRun(*run)
	if err != nil {
		t.Fatal(err)
	}
	if len(carriers) != 1 {
		t.Fatalf("stream packing should not split by size, got %d carriers", len(carriers))
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

	carriers, err := PackRun(*run)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(CarrierContent(*run, carriers[0].Envelopes))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "seqTotal") {
		t.Fatalf("stream envelopes must not contain finalization totals: %s", raw)
	}
}

func TestPackRunByTimeFromSeqSplitsOnlyByCadence(t *testing.T) {
	now := time.Unix(10, 0)
	run := NewRun("run-1", "thread-1", DefaultModel, "ai", "AI", now)
	writer := NewWriter(run, func() time.Time { return now })
	writer.Start()
	now = now.Add(250 * time.Millisecond)
	writer.Text("a")
	now = now.Add(250 * time.Millisecond)
	writer.Text("b")
	now = now.Add(2 * time.Second)
	writer.Text("c")
	writer.Finish(agui.FinishReasonStop)

	carriers, err := PackRunByTimeFromSeq(*run, 7, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if len(carriers) != 2 {
		t.Fatalf("expected cadence split only, got %d carriers", len(carriers))
	}
	if carriers[0].Envelopes[0].Seq != 7 || carriers[1].Envelopes[0].Seq != 11 {
		t.Fatalf("bad sequence continuity: %#v", carriers)
	}
	if got := ReconstructText(carriers); got != "abc" {
		t.Fatalf("bad reconstructed text %q", got)
	}
}

func TestStreamSnapshotUsesCanonicalMessagesWithoutSizeCompaction(t *testing.T) {
	run := NewRun("run-1", "thread-1", DefaultModel, "ai", "AI", time.Unix(10, 0))
	writer := NewWriter(run, func() time.Time { return time.Unix(10, 0) })
	writer.Start()
	writer.Thinking(strings.Repeat("t", 12*1024))
	writer.Text(strings.Repeat("a", 70*1024))
	writer.ToolStart("tool-1", "fetch", 0, nil)
	writer.ToolArgs("tool-1", `{"url":"https://example.com"}`, `{"url":"https://example.com"}`)
	writer.ToolEnd("tool-1", "fetch", `{"url":"https://example.com"}`, map[string]any{"ok": true})
	writer.Finish(agui.FinishReasonStop)

	carriers, err := PackRun(*run)
	if err != nil {
		t.Fatal(err)
	}
	var snapshots int
	var sawAssistant bool
	var sawReasoning bool
	var sawToolResult bool
	for _, carrier := range carriers {
		for _, env := range carrier.Envelopes {
			switch env.Event.Type() {
			case agui.EventMessagesSnapshot:
				snapshots++
				messages, ok := env.Event.Get("messages").([]agui.Message)
				if !ok || len(messages) == 0 {
					t.Fatalf("bad final snapshot: %#v", env.Event.Get("messages"))
				}
				for _, message := range messages {
					switch message.Role {
					case agui.RoleAssistant:
						sawAssistant = true
						content, _ := message.Content.(string)
						if len(content) != 70*1024 {
							t.Fatalf("stream snapshot assistant content was compacted: %d bytes", len(content))
						}
					case "reasoning":
						sawReasoning = true
						content, _ := message.Content.(string)
						if len(content) != 12*1024 {
							t.Fatalf("stream snapshot reasoning content was compacted: %d bytes", len(content))
						}
					case agui.RoleTool:
						sawToolResult = true
					}
				}
			}
		}
	}
	if snapshots != 1 || !sawAssistant || !sawReasoning || !sawToolResult {
		t.Fatalf("expected canonical snapshot with assistant/reasoning/tool messages, snapshots=%d assistant=%v reasoning=%v tool=%v", snapshots, sawAssistant, sawReasoning, sawToolResult)
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

	carriers, err := PackRun(*run)
	if err != nil {
		t.Fatal(err)
	}
	if len(carriers) != 1 {
		t.Fatalf("untimed stream packing should produce one carrier, got %d", len(carriers))
	}
	var deltas []string
	for _, carrier := range carriers {
		for _, env := range carrier.Envelopes {
			if env.Event.Type() == agui.EventTextMessageContent {
				deltas = append(deltas, env.Event.Get("delta").(string))
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

	uiMessage := run.FinalBeeperAIMessage(0, true)
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

func TestWriterAnchorsContinuationReasoningBeforeTextToFreshAssistantMessage(t *testing.T) {
	run := NewRun("run-1", "thread-1", DefaultModel, "ai", "AI", time.Unix(10, 0))
	writer := NewWriter(run, func() time.Time { return time.Unix(10, 0) })
	writer.Start()
	writer.Text("before ")
	writer.ToolStart("tool-1", "ask_approval", 1, nil)
	writer.AwaitToolUseWithUsage(nil)

	continuation := NewWriter(run, func() time.Time { return time.Unix(11, 0) })
	continuation.ReasoningMessageStart(0)
	continuation.ReasoningDelta(0, "checking approval")
	continuation.Text("after")

	var ordered []string
	for _, evt := range run.Events {
		switch evt.Type() {
		case agui.EventTextMessageStart:
			ordered = append(ordered, "text-start:"+asString(evt.Get("messageId")))
		case agui.EventReasoningMsgStart:
			ordered = append(ordered, "thinking-start:"+asString(evt.Get("messageId")))
		case agui.EventReasoningMsgCont:
			ordered = append(ordered, "thinking:"+asString(evt.Get("delta")))
		case agui.EventTextMessageContent:
			if delta := asString(evt.Get("delta")); delta != "" {
				ordered = append(ordered, "text:"+asString(evt.Get("messageId"))+":"+delta)
			}
		}
	}
	got := strings.Join(ordered, "|")
	if !strings.Contains(got, "text-start:msg-run-1-text-1|thinking-start:msg-run-1-reasoning-0|thinking:checking approval|text:msg-run-1-text-1:after") {
		t.Fatalf("continuation reasoning was not anchored to the fresh assistant message:\n%s", got)
	}

	uiMessage := run.FinalBeeperAIMessage(0, true)
	gotParts := uiPartSummary(uiMessage.Parts)
	wantParts := []string{"text:before ", "tool-call:tool-1", "thinking:checking approval", "text:after"}
	if !reflect.DeepEqual(gotParts, wantParts) {
		t.Fatalf("continued reasoning UI parts mismatch\ngot:  %#v\nwant: %#v\nparts: %#v", gotParts, wantParts, uiMessage.Parts)
	}
}

func TestWriterParentsContinuationToolsToCurrentAssistantMessage(t *testing.T) {
	run := NewRun("run-1", "thread-1", DefaultModel, "ai", "AI", time.Unix(10, 0))
	writer := NewWriter(run, func() time.Time { return time.Unix(10, 0) })
	writer.Start()
	writer.Text("before ")
	writer.ToolStart("tool-1", "ask_approval", 1, nil)
	writer.AwaitToolUseWithUsage(nil)

	continuation := NewWriter(run, func() time.Time { return time.Unix(11, 0) })
	continuation.Text("after ")
	continuation.ToolStart("tool-2", "second_tool", 1, nil)

	parentByTool := map[string]string{}
	for _, evt := range run.Events {
		if evt.Type() != agui.EventToolCallStart {
			continue
		}
		parentByTool[asString(evt.Get("toolCallId"))] = asString(evt.Get("parentMessageId"))
	}
	if parentByTool["tool-2"] != "msg-run-1-text-1" {
		t.Fatalf("second continuation tool used wrong parent: %#v", parentByTool)
	}

	uiMessage := run.FinalBeeperAIMessage(0, true)
	gotParts := uiPartSummary(uiMessage.Parts)
	wantParts := []string{"text:before ", "tool-call:tool-1", "text:after ", "tool-call:tool-2"}
	if !reflect.DeepEqual(gotParts, wantParts) {
		t.Fatalf("continued tool UI parts mismatch\ngot:  %#v\nwant: %#v\nparts: %#v", gotParts, wantParts, uiMessage.Parts)
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

	uiMessage := run.FinalBeeperAIMessage(0, true)
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

func TestFinalBeeperAIMessagePreservesInterleavedTextAndToolOrder(t *testing.T) {
	run := NewRun("run-1", "thread-1", DefaultModel, "ai", "AI", time.Unix(10, 0))
	builder := agui.NewEventBuilder(DefaultModel, func() time.Time { return time.Unix(10, 0) })
	run.Status = Status{State: "complete", FinishReason: agui.FinishReasonStop}
	run.Events = append(run.Events,
		builder.RunStarted("thread-1", "run-1"),
		builder.TextMessageContent(run.MessageID, "first text"),
		builder.ToolCallStart(run.MessageID, "tool-1", "fetch", nil),
		builder.ToolCallEnd("tool-1", "fetch", map[string]any{"query": "events"}, agui.ToolStateInputComplete),
		builder.ToolCallResult("tool-tool-1", "tool-1", `{"ok":true}`, agui.ToolResultStateComplete, agui.RoleTool),
		builder.TextMessageContent(run.MessageID, "second text"),
		builder.ReasoningMessageContent(run.MessageID+"-reasoning", "checked another thing"),
		builder.TextMessageContent(run.MessageID, "third text"),
		builder.RunFinished("thread-1", "run-1", agui.FinishReasonStop, agui.Usage{}),
	)
	if err := run.Validate(); err != nil {
		t.Fatal(err)
	}

	uiMessage := run.FinalBeeperAIMessage(0, true)
	got := make([]string, 0, len(uiMessage.Parts))
	for _, part := range uiMessage.Parts {
		switch part["type"] {
		case "text", "thinking":
			got = append(got, fmt.Sprintf("%s:%s", part["type"], part["content"]))
		case "tool-call":
			got = append(got, fmt.Sprintf("tool-call:%s", part["toolCallId"]))
		}
	}
	want := []string{
		"text:first text",
		"tool-call:tool-1",
		"text:second text",
		"thinking:checked another thing",
		"text:third text",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("final UIMessage order mismatch\ngot:  %#v\nwant: %#v\nparts: %#v", got, want, uiMessage.Parts)
	}
}

func TestFinalBeeperAIMessagePreservesTextChunksAfterToolCalls(t *testing.T) {
	run := NewRun("run-1", "thread-1", DefaultModel, "ai", "AI", time.Unix(10, 0))
	builder := agui.NewEventBuilder(DefaultModel, func() time.Time { return time.Unix(10, 0) })
	run.Status = Status{State: "complete", FinishReason: agui.FinishReasonStop}
	run.Events = append(run.Events,
		builder.RunStarted("thread-1", "run-1"),
		builder.TextMessageChunk(run.MessageID, agui.RoleAssistant, "first "),
		builder.TextMessageChunk(run.MessageID, agui.RoleAssistant, "text"),
		builder.ToolCallStart(run.MessageID, "tool-1", "fetch", nil),
		builder.ToolCallEnd("tool-1", "fetch", map[string]any{"query": "events"}, agui.ToolStateInputComplete),
		builder.ToolCallResult("tool-tool-1", "tool-1", `{"ok":true}`, agui.ToolResultStateComplete, agui.RoleTool),
		builder.TextMessageChunk(run.MessageID, agui.RoleAssistant, "second "),
		builder.TextMessageChunk(run.MessageID, agui.RoleAssistant, "text"),
		builder.RunFinished("thread-1", "run-1", agui.FinishReasonStop, agui.Usage{}),
	)
	if err := run.Validate(); err != nil {
		t.Fatal(err)
	}

	messages := run.Messages(false)
	if len(messages) == 0 || messages[0].Content != "first textsecond text" {
		t.Fatalf("final snapshot messages dropped text chunks: %#v", messages)
	}

	uiMessage := run.FinalBeeperAIMessage(0, true)
	got := make([]string, 0, len(uiMessage.Parts))
	for _, part := range uiMessage.Parts {
		switch part["type"] {
		case "text":
			got = append(got, fmt.Sprintf("text:%s", part["content"]))
		case "tool-call":
			got = append(got, fmt.Sprintf("tool-call:%s", part["toolCallId"]))
		}
	}
	want := []string{
		"text:first text",
		"tool-call:tool-1",
		"text:second text",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("final UIMessage text chunk order mismatch\ngot:  %#v\nwant: %#v\nparts: %#v", got, want, uiMessage.Parts)
	}
}

func TestFinalBeeperAIMessageSuppressesEmptyHiddenReasoningInOrder(t *testing.T) {
	run := NewRun("run-1", "thread-1", DefaultModel, "ai", "AI", time.Unix(10, 0))
	builder := agui.NewEventBuilder(DefaultModel, func() time.Time { return time.Unix(10, 0) })
	run.Status = Status{State: "complete", FinishReason: agui.FinishReasonStop}
	run.Events = append(run.Events,
		builder.RunStarted("thread-1", "run-1"),
		builder.TextMessageContent(run.MessageID, "first text"),
		builder.ToolCallStart(run.MessageID, "tool-1", "fetch", nil),
		builder.ReasoningMessageStart(run.MessageID+"-reasoning-0"),
		builder.ReasoningMessageEnd(run.MessageID+"-reasoning-0"),
		builder.TextMessageContent(run.MessageID, "second text"),
		builder.RunFinished("thread-1", "run-1", agui.FinishReasonStop, agui.Usage{}),
	)
	if err := run.Validate(); err != nil {
		t.Fatal(err)
	}

	uiMessage := run.FinalBeeperAIMessage(0, true)
	got := make([]string, 0, len(uiMessage.Parts))
	for _, part := range uiMessage.Parts {
		switch part["type"] {
		case "text", "thinking":
			got = append(got, fmt.Sprintf("%s:%s", part["type"], part["content"]))
		case "tool-call":
			got = append(got, fmt.Sprintf("tool-call:%s", part["toolCallId"]))
		}
	}
	want := []string{
		"text:first text",
		"tool-call:tool-1",
		"text:second text",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("final hidden reasoning order mismatch\ngot:  %#v\nwant: %#v\nparts: %#v", got, want, uiMessage.Parts)
	}
}

func TestFinalBeeperAIMessageKeepsStreamingThinkingPartEmptyUntilTokensArrive(t *testing.T) {
	run := NewRun("run-1", "thread-1", DefaultModel, "ai", "AI", time.Unix(10, 0))
	writer := NewWriter(run, func() time.Time { return time.Unix(10, 0) })
	writer.Start()
	writer.ReasoningMessageStart(0)

	streamingEmpty := run.FinalBeeperAIMessage(0, true)
	if len(streamingEmpty.Parts) != 1 || streamingEmpty.Parts[0]["type"] != "thinking" || streamingEmpty.Parts[0]["content"] != "" || streamingEmpty.Parts[0]["state"] != agui.PartStateStreaming {
		t.Fatalf("streaming empty reasoning should render as an empty in-progress part, got %#v", streamingEmpty.Parts)
	}

	writer.ReasoningDelta(0, "reading sources")
	streamingContent := run.FinalBeeperAIMessage(0, true)
	if len(streamingContent.Parts) != 1 || streamingContent.Parts[0]["type"] != "thinking" || streamingContent.Parts[0]["content"] != "reading sources" || streamingContent.Parts[0]["state"] != agui.PartStateStreaming {
		t.Fatalf("streaming reasoning tokens should update the same thinking part, got %#v", streamingContent.Parts)
	}

	writer.ReasoningMessageEnd(0)
	writer.Finish(agui.FinishReasonStop)
	finalContent := run.FinalBeeperAIMessage(0, true)
	if len(finalContent.Parts) != 1 || finalContent.Parts[0]["type"] != "thinking" || finalContent.Parts[0]["content"] != "reading sources" || finalContent.Parts[0]["state"] != agui.PartStateDone {
		t.Fatalf("final reasoning tokens should remain in order as a done thinking part, got %#v", finalContent.Parts)
	}
}

func TestFinalBeeperAIMessageDedupesNativeWebSearchRows(t *testing.T) {
	for _, provider := range []string{"openai", "openrouter"} {
		t.Run(provider, func(t *testing.T) {
			run := NewRun("run-1", "thread-1", DefaultModel, "ai", "AI", time.Unix(10, 0))
			writer := NewWriter(run, func() time.Time { return time.Unix(10, 0) })
			query := "pop culture latest Oscars Grammys Wikipedia"
			writer.ToolStart("ws_aggregate", "web_search", 0, nil)
			writer.ToolEnd("ws_aggregate", "web_search", map[string]any{
				"query":   query,
				"queries": []any{query, "latest entertainment news film music television"},
			}, map[string]any{
				"state":    "complete",
				"status":   "success",
				"provider": provider,
				"native":   true,
				"query":    query,
				"queries":  []any{query, "latest entertainment news film music television"},
			})
			writer.ToolStart("ws_single", "web_search", 0, nil)
			writer.ToolEnd("ws_single", "web_search", map[string]any{"query": query}, map[string]any{
				"state":    "complete",
				"status":   "success",
				"provider": provider,
				"native":   true,
				"query":    query,
			})
			writer.Finish(agui.FinishReasonStop)

			uiMessage := run.FinalBeeperAIMessage(0, true)
			var webSearchIDs []string
			for _, part := range uiMessage.Parts {
				if part["type"] == "tool-call" && part["name"] == "web_search" {
					webSearchIDs = append(webSearchIDs, firstString(part["toolCallId"]))
				}
			}
			if !reflect.DeepEqual(webSearchIDs, []string{"ws_single"}) {
				t.Fatalf("expected only the final native web_search row, got IDs=%#v parts=%#v", webSearchIDs, uiMessage.Parts)
			}
		})
	}
}

func TestFinalBeeperAIMessagePreservesStepsAsThinking(t *testing.T) {
	run := NewRun("run-1", "thread-1", DefaultModel, "ai", "AI", time.Unix(10, 0))
	builder := agui.NewEventBuilder(DefaultModel, func() time.Time { return time.Unix(10, 0) })
	run.Status = Status{State: "complete", FinishReason: agui.FinishReasonStop}
	run.Events = append(run.Events,
		builder.RunStarted("thread-1", "run-1"),
		builder.StepStarted(run.MessageID, "Search docs"),
		builder.StepFinished(run.MessageID, "Search docs"),
		builder.RunFinished("thread-1", "run-1", agui.FinishReasonStop, agui.Usage{}),
	)

	uiMessage := run.FinalBeeperAIMessage(0, true)
	got := make([]string, 0, len(uiMessage.Parts))
	for _, part := range uiMessage.Parts {
		if part["type"] == "thinking" {
			got = append(got, fmt.Sprintf("thinking:%s:%s:%s", part["stepId"], part["content"], part["state"]))
		}
	}
	want := []string{"thinking:Search docs:Search docs:done"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("final UIMessage step mismatch\ngot:  %#v\nwant: %#v\nparts: %#v", got, want, uiMessage.Parts)
	}
}

func TestFinalBeeperAIMessagePreservesActivitySnapshots(t *testing.T) {
	run := NewRun("run-1", "thread-1", DefaultModel, "ai", "AI", time.Unix(10, 0))
	builder := agui.NewEventBuilder(DefaultModel, func() time.Time { return time.Unix(10, 0) })
	run.Events = append(run.Events,
		builder.ActivitySnapshot(run.MessageID, "web_search", map[string]any{"title": "Searching web", "text": "Looking up docs"}, nil),
		builder.ActivityDelta(run.MessageID, "web_search", []any{map[string]any{"op": "replace", "path": "/text", "value": "Found docs"}}),
		builder.ActivityDelta(run.MessageID, "web_search", []any{map[string]any{"op": "replace", "path": "/status", "value": "completed"}}),
	)

	uiMessage := run.FinalBeeperAIMessage(0, true)
	got := make([]string, 0, len(uiMessage.Parts))
	for _, part := range uiMessage.Parts {
		if part["type"] == "thinking" {
			got = append(got, fmt.Sprintf("thinking:%s:%s:%s", part["title"], part["content"], part["state"]))
		}
	}
	want := []string{"thinking:Searching web:Found docs:done"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("final UIMessage activity mismatch\ngot:  %#v\nwant: %#v\nparts: %#v", got, want, uiMessage.Parts)
	}
}

func TestPackRunPreservesLargeCustomEvent(t *testing.T) {
	run := NewRun("run-1", "thread-1", DefaultModel, "ai", "AI", time.Unix(10, 0))
	builder := agui.NewEventBuilder(DefaultModel, func() time.Time { return time.Unix(10, 0) })
	run.Events = append(run.Events, builder.Custom("com.beeper.large", map[string]any{
		"value": strings.Repeat("x", FinalMessageBudgetBytes),
	}))

	carriers, err := PackRun(*run)
	if err != nil {
		t.Fatal(err)
	}
	part := carriers[0].Envelopes[0].Event
	value, ok := part.Get("value").(map[string]any)
	if !ok {
		t.Fatalf("missing custom value: %#v", part)
	}
	if got, _ := value["value"].(string); len(got) != FinalMessageBudgetBytes {
		t.Fatalf("custom value length = %d", len(got))
	}
}

func TestValidateRejectsLegacyOrInvalidToolResultShape(t *testing.T) {
	run := NewRun("run-1", "thread-1", DefaultModel, "ai", "AI", time.Unix(10, 0))
	builder := agui.NewEventBuilder(DefaultModel, func() time.Time { return time.Unix(10, 0) })
	run.Events = append(run.Events,
		builder.RunStarted("thread-1", "run-1"),
		builder.ToolCallStart("msg-run-1", "tool-1", "fetch", nil),
		agui.NewEvent(map[string]any{"type": agui.EventToolCallEnd, "toolCallId": "tool-1", "result": `{"ok":true}`, "state": agui.ToolStateInputComplete}),
	)
	if err := run.Validate(); err == nil {
		t.Fatal("expected validation error for legacy TOOL_CALL_END.result")
	}
}

func TestFinalBeeperAIMessageCarriesToolCallMetadata(t *testing.T) {
	run := NewRun("run-1", "thread-1", DefaultModel, "ai", "AI", time.Unix(10, 0))
	writer := NewWriter(run, func() time.Time { return time.Unix(10, 0) })
	writer.ToolStartWithMetadata("tool-1", "calendar.get_events", 0, nil, map[string]any{
		"displayName": "List Calendar Events",
		"iconUrl":     "mxc://beeper.com/calendar",
	})

	message := run.FinalBeeperAIMessage(0, true)
	if len(message.Parts) != 1 {
		t.Fatalf("expected one part, got %#v", message.Parts)
	}
	metadata, ok := message.Parts[0]["metadata"].(map[string]any)
	if !ok || metadata["displayName"] != "List Calendar Events" || metadata["iconUrl"] != "mxc://beeper.com/calendar" {
		t.Fatalf("bad tool metadata: %#v", message.Parts[0])
	}
}

func TestFinalBeeperAIMessageCarriesParsedToolOutputs(t *testing.T) {
	run := NewRun("run-1", "thread-1", DefaultModel, "ai", "AI", time.Unix(10, 0))
	writer := NewWriter(run, func() time.Time { return time.Unix(10, 0) })
	writer.ToolStart("tool-1", "fetch", 0, nil)
	writer.ToolArgs("tool-1", `{"url":"https://example.com"}`, `{"url":"https://example.com"}`)
	writer.ToolEnd("tool-1", "fetch", map[string]any{"url": "https://example.com"}, nil)
	writer.ToolStart("tool-2", "files", 1, nil)
	writer.ToolError("tool-2", "files", map[string]any{"path": "/tmp/nope"}, "missing")

	message := run.FinalBeeperAIMessage(0, true)
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

func TestFinalBeeperAIMessageCollapsesToolResultIntoToolCall(t *testing.T) {
	run := NewRun("run-1", "thread-1", DefaultModel, "ai", "AI", time.Unix(10, 0))
	writer := NewWriter(run, func() time.Time { return time.Unix(10, 0) })
	writer.ToolStart("tool-1", "fetch", 0, nil)
	writer.ToolResult("tool-1", `{"ok":true}`, agui.ToolResultStateComplete)

	message := run.FinalBeeperAIMessage(0, true)
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

func TestFinalBeeperAIMessageFailsOpenToolsWhenRunFinalized(t *testing.T) {
	run := NewRun("run-1", "thread-1", DefaultModel, "ai", "AI", time.Unix(10, 0))
	writer := NewWriter(run, func() time.Time { return time.Unix(10, 0) })
	writer.ToolStart("tool-1", "summarize", 0, nil)
	writer.ToolStart("tool-2", "calendar", 1, nil)
	writer.Finish(agui.FinishReasonStop)

	message := run.FinalBeeperAIMessage(0, true)
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

func TestFinalBeeperAIMessageFailsPendingApprovalWhenRunFails(t *testing.T) {
	run := NewRun("run-1", "thread-1", DefaultModel, "ai", "AI", time.Unix(10, 0))
	writer := NewWriter(run, func() time.Time { return time.Unix(10, 0) })
	writer.ToolApprovalRequested("tool-1", "get_session", map[string]any{}, ToolApproval{ID: "approval-1", NeedsApproval: true})
	writer.InterruptWithUsage(nil)
	run.Status = Status{State: "error", Error: map[string]any{"message": "AI stream was interrupted before completion"}}

	message := run.FinalBeeperAIMessage(0, true)
	if len(message.Parts) != 1 {
		t.Fatalf("expected one tool part, got %#v", message.Parts)
	}
	if message.Parts[0]["state"] != agui.ToolStateInputComplete {
		t.Fatalf("pending approval should be finalized as input-complete on error: %#v", message.Parts[0])
	}
	output, ok := message.Parts[0]["output"].(map[string]any)
	if !ok || output["status"] != "failed" || output["reason"] != "run failed before tool completed" {
		t.Fatalf("pending approval should carry failed output, got %#v", message.Parts[0])
	}
}

func TestFinalBeeperAIMessageKeepsPendingApprovalWhenInterrupted(t *testing.T) {
	run := NewRun("run-1", "thread-1", DefaultModel, "ai", "AI", time.Unix(10, 0))
	writer := NewWriter(run, func() time.Time { return time.Unix(10, 0) })
	writer.ToolApprovalRequested("tool-1", "get_session", map[string]any{}, ToolApproval{ID: "approval-1", NeedsApproval: true})
	writer.InterruptWithUsage(nil)

	message := run.FinalBeeperAIMessage(0, true)
	if len(message.Parts) != 1 || message.Parts[0]["state"] != ToolStateApprovalRequested {
		t.Fatalf("interrupted run should keep approval pending: %#v", message.Parts)
	}
	if _, hasOutput := message.Parts[0]["output"]; hasOutput {
		t.Fatalf("interrupted approval should not synthesize failed output: %#v", message.Parts[0])
	}
}

func TestFinalLifecycleEventsPreferTerminalErrorOverPreviousInterrupt(t *testing.T) {
	run := NewRun("run-1", "thread-1", DefaultModel, "ai", "AI", time.Unix(10, 0))
	writer := NewWriter(run, func() time.Time { return time.Unix(10, 0) })
	writer.ToolApprovalRequested("tool-1", "get_session", map[string]any{}, ToolApproval{ID: "approval-1", NeedsApproval: true})
	writer.InterruptWithUsage(nil)
	run.Status = Status{State: "error", Error: map[string]any{"message": "AI stream was interrupted before completion"}}

	payload := run.AI(AIKindFinal)
	if len(payload.Events) != 1 {
		t.Fatalf("expected one final lifecycle event, got %#v", payload.Events)
	}
	event := payload.Events[0].Event
	if event.Type() != agui.EventRunError {
		t.Fatalf("final lifecycle should be RUN_ERROR, got %#v", event)
	}
	if event.Get("message") != "AI stream was interrupted before completion" {
		t.Fatalf("final RUN_ERROR carried wrong message: %#v", event)
	}
}

func TestFinalBeeperAIMessageCarriesTopLevelArtifactsWithStableIDs(t *testing.T) {
	run := NewRun("run-1", "thread-1", DefaultModel, "ai", "AI", time.Unix(10, 0))
	writer := NewWriter(run, func() time.Time { return time.Unix(10, 0) })
	writer.Custom("com.beeper.source", map[string]any{
		"sourceId":    "source-1",
		"url":         "https://example.com/source",
		"title":       "Example Source",
		"description": "Source from example.com",
		"siteName":    "example.com",
		"imageUrl":    "https://example.com/favicon.ico",
		"appearances": []map[string]any{{"kind": "provider", "cited": true}},
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

	message := run.FinalBeeperAIMessage(0, true)
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

func TestApprovalResolverMatchesCommandChoices(t *testing.T) {
	choices := DefaultApprovalChoices()
	choice, ok := ResolveApprovalChoice(choices, "approve")
	response := ApprovalResponseForChoice("approval-1", choice)
	if !ok || !response.Approved || response.Always {
		t.Fatalf("expected approve, got %#v ok=%v", choice, ok)
	}
	choice, ok = ResolveApprovalChoice(choices, "always")
	response = ApprovalResponseForChoice("approval-1", choice)
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

func TestBeeperAIOwnsMatrixPayloadShape(t *testing.T) {
	run := NewRun("run-1", "thread-1", DefaultModel, "agent-1", "Agent", time.Unix(10, 0))
	run.MessageID = "msg-run-1"
	run.Status = Status{State: "complete", FinishReason: agui.FinishReasonStop}
	run.Usage = agui.Usage{PromptTokens: 1, CompletionTokens: 2, ReasoningTokens: 4, TotalTokens: 7}
	run.Preview = Preview{Text: "hello", Truncated: false}

	payload := run.AI(AIKindFinal)

	if payload.Schema != BeeperAISchema || payload.Protocol != "ag-ui" || payload.Kind != AIKindFinal {
		t.Fatalf("bad AI payload protocol: %#v", payload)
	}
	if payload.ThreadID != "thread-1" || payload.RunID != "run-1" || payload.MessageID != "msg-run-1" {
		t.Fatalf("bad run identifiers: %#v", payload)
	}
	if payload.Agent.ID != "agent-1" || payload.Agent.DisplayName != "Agent" {
		t.Fatalf("bad agent metadata: %#v", payload.Agent)
	}
	if len(payload.Events) != 1 || payload.Events[0].Event.Type() != agui.EventRunFinished {
		t.Fatalf("missing final AG-UI lifecycle event: %#v", payload.Events)
	}
	terminal := payload.Events[0].Event
	if terminal.Get("finishReason") != agui.FinishReasonStop {
		t.Fatalf("bad final finish reason: %#v", terminal)
	}
	if terminal.Get("usage") != run.Usage {
		t.Fatalf("bad final usage: %#v", terminal.Get("usage"))
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), `"metadata"`) || strings.Contains(string(encoded), `"status"`) || strings.Contains(string(encoded), `"usageDetails"`) || strings.Contains(string(encoded), `"terminal"`) {
		t.Fatalf("payload includes removed sidecar fields: %s", encoded)
	}
}

func TestMessagesSnapshotSurvivesJSONRoundTrip(t *testing.T) {
	run := NewRun("run-1", "thread-1", DefaultModel, "ai", "AI", time.Unix(10, 0))
	writer := NewWriter(run, func() time.Time { return time.Unix(10, 0) })
	writer.MessagesSnapshot([]agui.Message{
		{ID: "reasoning-1", Role: "reasoning", Content: "thought"},
		{ID: run.MessageID, Role: agui.RoleAssistant, Content: "answer"},
	})
	raw, err := json.Marshal(run.Events[0])
	if err != nil {
		t.Fatal(err)
	}
	var roundTripped agui.Event
	if err := json.Unmarshal(raw, &roundTripped); err != nil {
		t.Fatal(err)
	}
	run.Events = []agui.Event{roundTripped}

	if got := run.Text(); got != "answer" {
		t.Fatalf("round-tripped snapshot was not used for text: %q", got)
	}
	if messages := run.Messages(true); len(messages) != 2 || messages[0].Role != "reasoning" || messages[1].Content != "answer" {
		t.Fatalf("bad round-tripped snapshot messages: %#v", messages)
	}
	if messages := run.Messages(false); len(messages) != 1 || messages[0].Role != agui.RoleAssistant {
		t.Fatalf("reasoning filter failed for round-tripped snapshot: %#v", messages)
	}
}

func TestApprovalResponseClearsResolvedInterrupt(t *testing.T) {
	run := NewRun("run-1", "thread-1", DefaultModel, "ai", "AI", time.Unix(10, 0))
	writer := NewWriter(run, func() time.Time { return time.Unix(10, 0) })
	approval := ToolApproval{ID: "approval-1", NeedsApproval: true}
	writer.ToolApprovalRequested("tool-1", "shell", map[string]any{}, approval)
	if len(run.Interrupts) != 1 {
		t.Fatalf("expected pending interrupt: %#v", run.Interrupts)
	}
	writer.ToolApprovalResponded("tool-1", "shell", map[string]any{}, ToolApprovalResponse{ID: approval.ID, Approved: true})
	if len(run.Interrupts) != 0 {
		t.Fatalf("approval response left stale interrupts: %#v", run.Interrupts)
	}
	if run.Status.State != "streaming" {
		t.Fatalf("approval response should reopen the stream, got state %q", run.Status.State)
	}
	writer.ToolApprovalRequested("tool-2", "fetch", map[string]any{}, ToolApproval{ID: "approval-2", NeedsApproval: true})
	writer.ToolDenied("tool-2", "fetch", map[string]any{}, "approval-2", "denied")
	if len(run.Interrupts) != 0 {
		t.Fatalf("approval denial left stale interrupts: %#v", run.Interrupts)
	}
	if run.Status.State != "streaming" {
		t.Fatalf("approval denial should reopen the stream, got state %q", run.Status.State)
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
	var finishedUsage agui.Usage
	for _, evt := range run.Events {
		switch evt.Type() {
		case agui.EventRunFinished:
			finishedUsage = evt.Get("usage").(agui.Usage)
		}
	}
	if finishedUsage != usage {
		t.Fatalf("terminal event lost usage: finished=%#v", finishedUsage)
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
	if !ok || first["key"] != ApprovalChoiceApprove || first["label"] != "Allow once" || first["shortcut"] != "enter" {
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

func TestApprovalQueueKeepsOneActiveInterruptAndTimeouts(t *testing.T) {
	queue := NewApprovalQueue(ApprovalTimeout{After: time.Minute})
	queue.AddAll([]ApprovalPrompt{
		{ID: "approval-1", ToolCallID: "tool-1", ToolName: "shell"},
		{ID: "approval-2", ToolCallID: "tool-2", ToolName: "fetch"},
	})

	active, ok := queue.Active()
	if !ok || active.ID != "approval-1" {
		t.Fatalf("bad active approval: %#v ok=%v", active, ok)
	}
	if pending := queue.Pending(); len(pending) != 1 || pending[0].ID != "approval-2" {
		t.Fatalf("bad queued approvals: %#v", pending)
	}
	resolved, response, ok := queue.TimeoutActive()
	if !ok || resolved.ID != "approval-1" || response.ID != "approval-1" || response.Approved || response.Reason != "timed_out" {
		t.Fatalf("bad timeout resolution: prompt=%#v response=%#v ok=%v", resolved, response, ok)
	}
	active, ok = queue.Active()
	if !ok || active.ID != "approval-2" {
		t.Fatalf("queue did not promote next approval: %#v ok=%v", active, ok)
	}
	result := TimedOutApprovalToolResult("approval-1")
	if result.Status != "timed_out" || result.State != agui.ToolResultStateError || result.Approved {
		t.Fatalf("bad timed-out tool result: %#v", result)
	}
}

func uiPartSummary(parts []MessagePart) []string {
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		switch part["type"] {
		case "text", "thinking":
			out = append(out, fmt.Sprintf("%s:%s", part["type"], part["content"]))
		case "tool-call":
			out = append(out, fmt.Sprintf("tool-call:%s", firstString(part["toolCallId"], part["id"])))
		}
	}
	return out
}
