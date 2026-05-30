package matrix

import (
	"fmt"

	"github.com/beeper/ai-bridge/pkg/ai-stream"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/format"
	"maunium.net/go/mautrix/id"
)

const ApprovalRelationType = event.RelationType("com.beeper.ai.approval")

type FinalProjection struct {
	Content  *event.MessageEventContent
	Extra    map[string]any
	Segments []aistream.FinalSegment
}

func AnchorContent(run aistream.Run) (*event.MessageEventContent, map[string]any) {
	content := previewContent(run)
	extra := map[string]any{
		aistream.BeeperAIKey: run.AIWithMessage(aistream.AIKindAnchor, run.InitialBeeperAIMessage()),
	}
	return content, extra
}

func ProjectFinal(run aistream.Run) FinalProjection {
	uiMessage, segments := aistream.FinalUIMessageContent(run, aistream.FinalMessageBudgetBytes)
	content := previewContent(run)
	extra := map[string]any{
		aistream.BeeperAIKey: finalAIContent(run, uiMessage, len(segments)),
	}
	return FinalProjection{Content: content, Extra: extra, Segments: segments}
}

func FinalContent(run aistream.Run) (*event.MessageEventContent, map[string]any) {
	projection := ProjectFinal(run)
	return projection.Content, projection.Extra
}

func finalAIContent(run aistream.Run, message aistream.UIMessage, segmentCount int) aistream.BeeperAI {
	if segmentCount > 0 {
		run.Final = aistream.FinalDelivery{Delivery: "segmented", SegmentCount: segmentCount}
	} else {
		run.Final = aistream.FinalDelivery{Delivery: "inline", SegmentCount: 0}
	}
	return run.AIWithMessage(aistream.AIKindFinal, message)
}

func FinalSegments(run aistream.Run) []aistream.FinalSegment {
	return ProjectFinal(run).Segments
}

func previewContent(run aistream.Run) *event.MessageEventContent {
	body := run.Preview.Text
	if body == "" {
		body = "..."
	}
	rendered := format.RenderMarkdown(body, true, false)
	content := &rendered
	content.EnsureHasHTML()
	content.BeeperPerMessageProfile = &event.BeeperPerMessageProfile{
		ID:          run.AgentID,
		Displayname: run.AgentName,
	}
	return content
}

func CarrierContent(run aistream.Run, carrier aistream.Carrier, targetEventID id.EventID) (*event.MessageEventContent, map[string]any) {
	content := format.TextToContent("")
	content.SetRelatesTo(&event.RelatesTo{Type: event.RelReference, EventID: targetEventID})
	return &content, aistream.CarrierContent(run, carrier.Envelopes)
}

func FinalSegmentContent(run aistream.Run, segment aistream.FinalSegment, targetEventID id.EventID) (*event.MessageEventContent, map[string]any) {
	content := format.TextToContent("")
	content.SetRelatesTo(&event.RelatesTo{Type: event.RelReference, EventID: targetEventID})
	return &content, map[string]any{
		aistream.BeeperAIKey: run.AISegment(segment.Message, segment.Metadata),
	}
}

func ApprovalContent(ctx aistream.ApprovalContext, choices []aistream.ApprovalChoice) (*event.MessageEventContent, map[string]any) {
	toolName := ctx.ToolName
	body := fmt.Sprintf("Approval required for %s", toolName)
	if len(choices) > 0 {
		body += "\nReact with one of the listed choices."
	}
	content := format.TextToContent(body)
	if ctx.TargetEvent != "" {
		content.SetRelatesTo(&event.RelatesTo{Type: ApprovalRelationType, EventID: id.EventID(ctx.TargetEvent)})
	}
	extra := map[string]any{
		aistream.BeeperAIApprovalKey: aistream.NewApprovalNotice(ctx, choices).Map(),
	}
	return &content, extra
}

func ApprovalChoicesAsAny(choices []aistream.ApprovalChoice) []any {
	return aistream.ApprovalChoicesAsAny(choices)
}
