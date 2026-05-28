package aistream

import "fmt"

const FinalMessageBudgetBytes = 64 * 1024

type FinalSegmentMetadata struct {
	RunID     string `json:"runId"`
	MessageID string `json:"messageId"`
	Index     int    `json:"index"`
	Count     int    `json:"count"`
}

type FinalSegment struct {
	Message  UIMessage            `json:"message"`
	Metadata FinalSegmentMetadata `json:"metadata"`
}

func FinalUIMessageContent(run Run, budget int) (UIMessage, []FinalSegment) {
	if budget <= 0 {
		budget = FinalMessageBudgetBytes
	}
	message := run.FinalBeeperAIMessage(0, true)
	inlineRun := run
	inlineRun.Final = FinalDelivery{Delivery: "inline", SegmentCount: 0}
	if JSONSize(map[string]any{BeeperAIKey: inlineRun.AIWithMessage(AIKindFinal, message)}) <= budget {
		return message, nil
	}

	parts := make([]MessagePart, 0, len(message.Parts))
	for _, part := range message.Parts {
		parts = append(parts, splitFinalPart(part, budget)...)
	}
	segments := packFinalSegments(run, message, parts, budget)
	assignFinalSegmentMetadata(run, segments)
	return UIMessage{ID: message.ID, Role: message.Role, Parts: []MessagePart{}}, segments
}

func packFinalSegments(run Run, message UIMessage, parts []MessagePart, budget int) []FinalSegment {
	if len(parts) == 0 {
		return []FinalSegment{{Message: UIMessage{ID: message.ID, Role: message.Role, Parts: []MessagePart{}}}}
	}
	maxMetadata := FinalSegmentMetadata{RunID: run.RunID, MessageID: run.MessageID, Index: len(parts), Count: len(parts)}
	segments := make([]FinalSegment, 0)
	current := make([]MessagePart, 0)
	for _, part := range splitFinalPartsForPayload(run, message, parts, budget, maxMetadata) {
		candidate := append(append([]MessagePart(nil), current...), part)
		if len(current) > 0 && finalSegmentPayloadSize(run, message, candidate, maxMetadata) > budget {
			segments = append(segments, FinalSegment{Message: finalSegmentMessage(message, current)})
			current = []MessagePart{part}
			continue
		}
		current = candidate
	}
	if len(current) > 0 {
		segments = append(segments, FinalSegment{Message: finalSegmentMessage(message, current)})
	}
	return segments
}

func splitFinalPartsForPayload(run Run, message UIMessage, parts []MessagePart, budget int, metadata FinalSegmentMetadata) []MessagePart {
	out := make([]MessagePart, 0, len(parts))
	for _, part := range parts {
		out = append(out, splitFinalPartForPayload(run, message, part, budget, metadata)...)
	}
	return out
}

func splitFinalPartForPayload(run Run, message UIMessage, part MessagePart, budget int, metadata FinalSegmentMetadata) []MessagePart {
	if finalSegmentPayloadSize(run, message, []MessagePart{part}, metadata) <= budget {
		return []MessagePart{part}
	}
	content, _ := part["content"].(string)
	if content == "" {
		return []MessagePart{part}
	}
	maxContentBytes := len(content) / 2
	if maxContentBytes > budget/2 {
		maxContentBytes = budget / 2
	}
	if maxContentBytes < 1 {
		maxContentBytes = 1
	}
	for maxContentBytes >= 1 {
		chunks := SplitTextUTF8(content, maxContentBytes)
		out := make([]MessagePart, 0, len(chunks))
		allFit := true
		for _, chunk := range chunks {
			split := cloneValueMap(part)
			split["content"] = chunk
			if finalSegmentPayloadSize(run, message, []MessagePart{split}, metadata) > budget {
				allFit = false
				break
			}
			out = append(out, split)
		}
		if allFit {
			return out
		}
		maxContentBytes /= 2
	}
	return []MessagePart{part}
}

func assignFinalSegmentMetadata(run Run, segments []FinalSegment) {
	for index := range segments {
		segments[index].Metadata = FinalSegmentMetadata{
			RunID:     run.RunID,
			MessageID: run.MessageID,
			Index:     index,
			Count:     len(segments),
		}
	}
}

func finalSegmentPayloadSize(run Run, message UIMessage, parts []MessagePart, metadata FinalSegmentMetadata) int {
	segmentMessage := finalSegmentMessage(message, parts)
	return JSONSize(map[string]any{BeeperAIKey: run.AISegment(segmentMessage, metadata)})
}

func finalSegmentMessage(message UIMessage, parts []MessagePart) UIMessage {
	return UIMessage{
		ID:    message.ID,
		Role:  message.Role,
		Parts: append([]MessagePart(nil), parts...),
	}
}

func FinalSegmentTxnID(runID string, index int) string {
	if runID == "" {
		return fmt.Sprintf("ai_final_segment_%d", index)
	}
	return fmt.Sprintf("ai_final_segment_%s_%d", runID, index)
}

func splitFinalPart(part MessagePart, budget int) []MessagePart {
	if JSONSize(part) <= budget {
		return []MessagePart{part}
	}
	content, _ := part["content"].(string)
	if content == "" {
		return []MessagePart{part}
	}
	maxContentBytes := budget / 2
	if maxContentBytes < 1024 {
		maxContentBytes = 1024
	}
	chunks := SplitTextUTF8(content, maxContentBytes)
	out := make([]MessagePart, 0, len(chunks))
	for _, chunk := range chunks {
		split := cloneValueMap(part)
		split["content"] = chunk
		out = append(out, split)
	}
	return out
}
