package matrix

import (
	"fmt"

	"github.com/beeper/ai-bridge/pkg/ai-stream"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/format"
	"maunium.net/go/mautrix/id"
)

const ApprovalRelationType = event.RelationType("com.beeper.ai.approval")

func AnchorContent(run aistream.Run) (*event.MessageEventContent, map[string]any) {
	content := previewContent(run)
	extra := map[string]any{
		aistream.BeeperAIKey:         run.InitialBeeperUIMessage(),
		aistream.BeeperAIMetadataKey: run.Metadata(),
		"com.beeper.stream": map[string]any{
			"type": aistream.BeeperAIStreamDeltas,
		},
	}
	return content, extra
}

func FinalContent(run aistream.Run) (*event.MessageEventContent, map[string]any) {
	content := previewContent(run)
	extra := map[string]any{
		aistream.BeeperAIKey:         run.FinalBeeperUIMessage(0, true),
		aistream.BeeperAIMetadataKey: run.Metadata(),
		"com.beeper.stream":          nil,
	}
	return content, extra
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

func CarrierContent(carrier aistream.Carrier, targetEventID id.EventID) (*event.MessageEventContent, map[string]any) {
	content := format.TextToContent("")
	content.SetRelatesTo(&event.RelatesTo{Type: event.RelReference, EventID: targetEventID})
	return &content, aistream.CarrierContent(carrier.Envelopes)
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
		"com.beeper.ai.approval": aistream.NewApprovalNotice(ctx, choices).Map(),
	}
	return &content, extra
}

func ApprovalChoicesAsAny(choices []aistream.ApprovalChoice) []any {
	return aistream.ApprovalChoicesAsAny(choices)
}
