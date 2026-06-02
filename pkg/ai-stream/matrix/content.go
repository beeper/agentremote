package matrix

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/format"
	"maunium.net/go/mautrix/id"

	aistream "github.com/beeper/ai-bridge/pkg/ai-stream"
)

const ApprovalRelationType = event.RelationType("com.beeper.ai.approval")

const seeMoreSupportedClients = "[See more on supported clients]"
const seeMoreSupportedClientsHTML = `<span data-beeper-ai-fallback="final-parts">` + seeMoreSupportedClients + `</span>`
const workingFallbackText = "Working..."
const finalEncryptedEventOverheadBytes = 2048

const finalEditSizeProbeEventID = id.EventID("$final-size-probe-final-size-probe-final-size-probe:beeper.local")

type FinalProjection struct {
	Content         *event.MessageEventContent
	Extra           map[string]any
	Message         aistream.UIMessage
	NeedsAttachment bool
}

func AnchorContent(run aistream.Run) (*event.MessageEventContent, map[string]any) {
	content := previewContent(run)
	return content, map[string]any{
		aistream.BeeperAIKey: run.AIWithMessage(aistream.AIKindAnchor, run.InitialBeeperAIMessage()),
	}
}

func ProjectFinal(run aistream.Run, partsRef *aistream.FinalPartsRef) FinalProjection {
	fullMessage := run.FinalBeeperAIMessage(0, true)
	message := fullMessage
	partsComplete := true
	needsAttachment := false
	delivery := "inline"
	if partsRef != nil {
		message = emptyFinalMessage(fullMessage)
		delivery = "attachment"
		partsComplete = false
	} else if !finalMetadataFits(run, fullMessage, nil, aistream.FinalMessageBudgetBytes) {
		message = emptyFinalMessage(fullMessage)
		delivery = "attachment"
		partsComplete = false
		needsAttachment = true
	}

	textComplete := true
	var content *event.MessageEventContent
	var extra map[string]any
	for attempt := 0; attempt < 3; attempt++ {
		run.Final = aistream.FinalDelivery{
			Delivery:      delivery,
			TextComplete:  &textComplete,
			PartsComplete: partsComplete,
			PartsRef:      partsRef,
		}
		extra = map[string]any{aistream.BeeperAIKey: run.AIWithMessage(aistream.AIKindFinal, message)}
		content, textComplete = fitFinalHTML(run, finalVisibleText(run), aistream.FinalMessageBudgetBytes, extra)
	}
	content = fitFinalPlaintextBody(content, finalVisibleText(run), aistream.FinalMessageBudgetBytes, extra)
	return FinalProjection{Content: content, Extra: extra, Message: fullMessage, NeedsAttachment: needsAttachment}
}

func FinalContent(run aistream.Run) (*event.MessageEventContent, map[string]any) {
	projection := ProjectFinal(run, nil)
	return projection.Content, projection.Extra
}

func finalMetadataFits(run aistream.Run, message aistream.UIMessage, partsRef *aistream.FinalPartsRef, budget int) bool {
	textComplete := true
	run.Final = aistream.FinalDelivery{
		Delivery:      "inline",
		TextComplete:  &textComplete,
		PartsComplete: true,
		PartsRef:      partsRef,
	}
	if partsRef != nil {
		run.Final.Delivery = "attachment"
		run.Final.PartsComplete = false
	}
	extra := map[string]any{aistream.BeeperAIKey: run.AIWithMessage(aistream.AIKindFinal, message)}
	return finalPayloadSize(finalTextContent(run, "", false), extra) <= budget
}

func emptyFinalMessage(message aistream.UIMessage) aistream.UIMessage {
	return aistream.UIMessage{ID: message.ID, Role: message.Role, Parts: []aistream.MessagePart{}}
}

func finalVisibleText(run aistream.Run) string {
	if text := run.Text(); text != "" {
		return text
	}
	if run.Status.State == "error" {
		return aistream.ErrorVisibleText(run.Status.Error)
	}
	if run.Preview.Text != "" {
		return run.Preview.Text
	}
	return workingFallbackText
}

func fitFinalHTML(run aistream.Run, markdown string, budget int, extra map[string]any) (*event.MessageEventContent, bool) {
	if markdown == "" {
		return finalTextContent(run, "", false), true
	}
	full := finalTextContent(run, markdown, false)
	if finalPayloadSize(full, extra) <= budget {
		return full, true
	}
	suffix := "\n\n" + seeMoreSupportedClients
	best := ""
	low, high := 0, len(markdown)
	for low <= high {
		mid := (low + high) / 2
		prefix := strings.TrimSpace(utf8Prefix(markdown, mid))
		text := strings.TrimSpace(prefix + suffix)
		if prefix == "" {
			text = seeMoreSupportedClients
		}
		content := finalTextContentWithSupportedClientsFallback(run, text, false)
		if finalPayloadSize(content, extra) <= budget {
			best = text
			low = mid + 1
		} else {
			high = mid - 1
		}
	}
	if best == "" {
		if fallback := finalTextContentWithSupportedClientsFallback(run, seeMoreSupportedClients, false); finalPayloadSize(fallback, extra) <= budget {
			return fallback, false
		}
		return finalTextContent(run, "", false), false
	}
	return finalTextContentWithSupportedClientsFallback(run, best, false), false
}

func fitFinalPlaintextBody(content *event.MessageEventContent, markdown string, budget int, extra map[string]any) *event.MessageEventContent {
	if markdown == "" {
		return content
	}
	best := ""
	low, high := 0, len(markdown)
	for low <= high {
		mid := (low + high) / 2
		body := utf8Prefix(markdown, mid)
		candidate := *content
		candidate.Body = body
		if finalPayloadSize(&candidate, extra) <= budget {
			best = body
			low = mid + 1
		} else {
			high = mid - 1
		}
	}
	out := *content
	out.Body = best
	return &out
}

func utf8Prefix(text string, maxBytes int) string {
	if maxBytes >= len(text) {
		return text
	}
	if maxBytes <= 0 {
		return ""
	}
	end := maxBytes
	for end > 0 && !utf8.RuneStart(text[end]) {
		end--
	}
	return text[:end]
}

func finalTextContent(run aistream.Run, text string, includeBody bool) *event.MessageEventContent {
	rendered := format.RenderMarkdown(text, true, false)
	content := &rendered
	if !includeBody {
		content.Body = ""
	}
	content.EnsureHasHTML()
	content.BeeperPerMessageProfile = &event.BeeperPerMessageProfile{
		ID:          run.AgentID,
		Displayname: run.AgentName,
	}
	return content
}

func finalTextContentWithSupportedClientsFallback(run aistream.Run, text string, includeBody bool) *event.MessageEventContent {
	content := finalTextContent(run, text, includeBody)
	content.FormattedBody = strings.ReplaceAll(content.FormattedBody, seeMoreSupportedClients, seeMoreSupportedClientsHTML)
	return content
}

func finalPayloadSize(content *event.MessageEventContent, extra map[string]any) int {
	clear := *content
	clear.SetEdit(finalEditSizeProbeEventID)
	raw := map[string]any{"com.beeper.dont_render_edited": true}
	if extra != nil {
		raw["m.new_content"] = extra
	}
	clearSize := aistream.JSONSize(&event.Content{Parsed: &clear, Raw: raw})
	return finalEncryptedEventOverheadBytes + ((clearSize+2)/3)*4
}

func previewContent(run aistream.Run) *event.MessageEventContent {
	body := run.Preview.Text
	if body == "" {
		body = workingFallbackText
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

func ApprovalContent(ctx aistream.ApprovalContext, choices []aistream.ApprovalChoice) (*event.MessageEventContent, map[string]any) {
	body := strings.TrimSpace(ctx.PlanText)
	if body == "" {
		title := strings.TrimSpace(ctx.Title)
		if title == "" {
			title = fmt.Sprintf("Approval required for %s", ctx.ToolName)
		}
		body = title
		if description := strings.TrimSpace(ctx.Description); description != "" {
			body += "\n\n" + description
		}
	}
	if len(choices) > 0 {
		body += "\n\nRespond with one of these commands:"
		for _, choice := range choices {
			body += fmt.Sprintf("\n- `%s`", aistream.ApprovalCommandForChoice(ctx.ID, choice))
		}
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
