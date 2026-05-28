package matrix

import (
	"strings"
	"testing"
	"time"

	"github.com/beeper/ai-bridge/pkg/ag-ui"
	"github.com/beeper/ai-bridge/pkg/ai-stream"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

func TestAnchorContentUsesVisibleTextAndAIProfile(t *testing.T) {
	run := aistream.NewRun("run-1", "thread-1", aistream.DefaultModel, "ai", "AI", time.Unix(10, 0))
	run.Preview = aistream.Preview{Text: "visible preview"}

	content, extra := AnchorContent(*run)
	if content.MsgType != event.MsgText || content.Body != "visible preview" {
		t.Fatalf("bad anchor content: %#v", content)
	}
	if content.Format != event.FormatHTML || content.FormattedBody == "" {
		t.Fatalf("anchor preview should include Matrix HTML: %#v", content)
	}
	if content.BeeperPerMessageProfile == nil || content.BeeperPerMessageProfile.ID != "ai" || content.BeeperPerMessageProfile.Displayname != "AI" {
		t.Fatalf("missing AI per-message profile: %#v", content.BeeperPerMessageProfile)
	}
	uiMessage, ok := extra[aistream.BeeperAIKey].(aistream.UIMessage)
	if !ok || uiMessage.ID == "" || uiMessage.Metadata == nil || len(uiMessage.Parts) != 1 {
		t.Fatalf("bad compact AI message: %#v", extra[aistream.BeeperAIKey])
	}
	if uiMessage.Parts[0]["type"] != "text" || uiMessage.Parts[0]["content"] != "visible preview" {
		t.Fatalf("anchor AI message should include preview text part: %#v", uiMessage.Parts)
	}
	if extra[aistream.BeeperAIMetadataKey] == nil {
		t.Fatalf("missing AI metadata: %#v", extra)
	}
	stream, ok := extra["com.beeper.stream"].(map[string]any)
	if !ok || stream["user_id"] != nil || stream["type"] != aistream.BeeperAIStreamDeltas {
		t.Fatalf("missing stream descriptor: %#v", extra["com.beeper.stream"])
	}
}

func TestAnchorContentKeepsLongRunsCompact(t *testing.T) {
	run := aistream.NewRun("run-1", "thread-1", aistream.DefaultModel, "ai", "AI", time.Unix(10, 0))
	writer := aistream.NewWriter(run, func() time.Time { return time.Unix(10, 0) })
	writer.Start()
	writer.Text(strings.Repeat("a", 70*1024))
	writer.Finish(agui.FinishReasonStop)

	content, extra := AnchorContent(*run)
	if len(content.Body) > aistream.PreviewBudgetBytes {
		t.Fatalf("anchor body length = %d, want <= %d", len(content.Body), aistream.PreviewBudgetBytes)
	}
	metadata := extra[aistream.BeeperAIMetadataKey].(map[string]any)
	if _, hasParts := metadata["parts"]; hasParts {
		t.Fatalf("metadata must not contain streamed parts: %#v", metadata)
	}
	if _, hasChunks := metadata["chunks"]; hasChunks {
		t.Fatalf("metadata must not contain streamed chunks: %#v", metadata)
	}
	preview := metadata["preview"].(aistream.Preview)
	if !preview.Truncated || len(preview.Text) > aistream.PreviewBudgetBytes {
		t.Fatalf("bad bounded preview: %#v", preview)
	}
}

func TestStreamingAnchorDoesNotIncludePreviewPart(t *testing.T) {
	run := aistream.NewRun("run-1", "thread-1", aistream.DefaultModel, "ai", "AI", time.Unix(10, 0))
	run.Preview = aistream.Preview{}

	content, extra := AnchorContent(*run)
	if content.Body != "..." {
		t.Fatalf("empty streaming anchor should use placeholder body, got %q", content.Body)
	}
	uiMessage, ok := extra[aistream.BeeperAIKey].(aistream.UIMessage)
	if !ok || len(uiMessage.Parts) != 0 {
		t.Fatalf("streaming anchor should not include an initial text snapshot: %#v", extra[aistream.BeeperAIKey])
	}
}

func TestAnchorContentRendersFinalPreviewAsMatrixHTML(t *testing.T) {
	run := aistream.NewRun("run-1", "thread-1", aistream.DefaultModel, "ai", "AI", time.Unix(10, 0))
	run.Preview = aistream.Preview{Text: "Use **bold** and `code`"}

	content, _ := AnchorContent(*run)
	if content.Format != event.FormatHTML {
		t.Fatalf("format = %q, want Matrix HTML", content.Format)
	}
	if !strings.Contains(content.FormattedBody, "<strong>bold</strong>") || !strings.Contains(content.FormattedBody, "<code>code</code>") {
		t.Fatalf("formatted body did not render markdown: %q", content.FormattedBody)
	}
}

func TestFinalContentIncludesFinalUIParts(t *testing.T) {
	run := aistream.NewRun("run-1", "thread-1", aistream.DefaultModel, "ai", "AI", time.Unix(10, 0))
	writer := aistream.NewWriter(run, func() time.Time { return time.Unix(10, 0) })
	writer.Start()
	writer.Thinking("hidden reasoning")
	writer.Text("final **preview**")
	writer.Finish(agui.FinishReasonStop)

	content, extra := FinalContent(*run)
	if content.Body != "final **preview**" || content.Format != event.FormatHTML {
		t.Fatalf("bad final preview content: %#v", content)
	}
	uiMessage, ok := extra[aistream.BeeperAIKey].(aistream.UIMessage)
	if !ok || len(uiMessage.Parts) != 2 || uiMessage.Parts[0]["type"] != "thinking" || uiMessage.Parts[1]["type"] != "text" {
		t.Fatalf("final edit must include concrete UI parts: %#v", extra[aistream.BeeperAIKey])
	}
	if uiMessage.Parts[0]["content"] != "hidden reasoning" || uiMessage.Parts[1]["content"] == "" {
		t.Fatalf("final edit must preserve reasoning and text parts: %#v", uiMessage.Parts)
	}
	if extra[aistream.BeeperAIMetadataKey] == nil {
		t.Fatalf("missing final metadata: %#v", extra)
	}
	if stream, ok := extra["com.beeper.stream"]; !ok || stream != nil {
		t.Fatalf("final edit must clear stream descriptor: %#v", extra["com.beeper.stream"])
	}
}

func TestFinalContentDoesNotTruncateUIParts(t *testing.T) {
	run := aistream.NewRun("run-1", "thread-1", aistream.DefaultModel, "ai", "AI", time.Unix(10, 0))
	writer := aistream.NewWriter(run, func() time.Time { return time.Unix(10, 0) })
	writer.Start()
	full := strings.Repeat("| Artifact | State | Latency |\n| --- | --- | --- |\n| renderer | active | accepts markdown |\n\n", 100)
	writer.Text(full)
	writer.Finish(agui.FinishReasonStop)
	expected := run.Text()

	_, extra := FinalContent(*run)
	uiMessage, ok := extra[aistream.BeeperAIKey].(aistream.UIMessage)
	if !ok || len(uiMessage.Parts) == 0 {
		t.Fatalf("missing final UI message: %#v", extra[aistream.BeeperAIKey])
	}
	textPart := uiMessage.Parts[len(uiMessage.Parts)-1]
	if textPart["content"] != expected {
		t.Fatalf("final UI text was truncated: got %d bytes want %d", len(textPart["content"].(string)), len(expected))
	}
	if metadata, ok := textPart["providerMetadata"]; ok {
		t.Fatalf("final UI text should not be marked truncated: %#v", metadata)
	}
}

func TestCarrierContentIsHiddenTextCarrierWithDeltas(t *testing.T) {
	carrier := aistream.Carrier{Envelopes: []aistream.Envelope{{
		ThreadID:    "thread-1",
		RunID:       "run-1",
		MessageID:   "msg-run-1",
		Seq:         1,
		TargetEvent: "$anchor",
	}}}

	content, extra := CarrierContent(carrier, id.EventID("$anchor"))
	if content.MsgType != event.MsgText || content.Body != "" {
		t.Fatalf("carrier should be empty m.text, got %#v", content)
	}
	if content.RelatesTo == nil || content.RelatesTo.EventID != "$anchor" {
		t.Fatalf("carrier should reference anchor, got %#v", content.RelatesTo)
	}
	deltas, ok := extra[aistream.BeeperAIStreamDeltas].([]aistream.Envelope)
	if !ok || len(deltas) != 1 || deltas[0].Seq != 1 {
		t.Fatalf("missing deltas: %#v", extra)
	}
}

func TestApprovalContentIncludesContextAndChoices(t *testing.T) {
	ctx := aistream.ApprovalContext{
		ID:          "approval-1",
		ThreadID:    "thread-1",
		RunID:       "run-1",
		MessageID:   "msg-run-1",
		ToolCallID:  "tool-1",
		ToolName:    "fetch",
		TargetEvent: "$anchor",
	}
	choices := aistream.DefaultApprovalChoices()

	content, extra := ApprovalContent(ctx, choices)
	if content.MsgType != event.MsgText || content.RelatesTo == nil || content.RelatesTo.EventID != "$anchor" || content.RelatesTo.Type != ApprovalRelationType {
		t.Fatalf("bad approval content: %#v", content)
	}
	meta, ok := extra["com.beeper.ai.approval"].(map[string]any)
	if !ok {
		t.Fatalf("missing approval metadata: %#v", extra)
	}
	if meta["schema"] != "com.beeper.ai.approval.v1" || meta["id"] != ctx.ID || meta["messageId"] != ctx.MessageID || meta["toolCallId"] != ctx.ToolCallID || meta["state"] != "requested" {
		t.Fatalf("bad approval metadata: %#v", meta)
	}
	if _, ok := meta["runId"]; ok {
		t.Fatalf("approval event should not duplicate run metadata: %#v", meta)
	}
	approvalChoices, ok := meta["choices"].([]any)
	if !ok || len(approvalChoices) != len(choices) {
		t.Fatalf("bad approval choices: %#v", meta["choices"])
	}
	first := approvalChoices[0].(map[string]any)
	if first["key"] != aistream.ApprovalChoiceApprove || first["alias"] != "✅" {
		t.Fatalf("bad first approval choice: %#v", first)
	}
}
