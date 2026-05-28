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

	segments := make([]FinalSegment, 0)
	for _, part := range message.Parts {
		for _, splitPart := range splitFinalPart(part, budget) {
			segmentMessage := UIMessage{
				ID:    message.ID,
				Role:  message.Role,
				Parts: []MessagePart{splitPart},
			}
			segments = append(segments, FinalSegment{Message: segmentMessage})
		}
	}
	if len(segments) == 0 {
		segments = append(segments, FinalSegment{
			Message: UIMessage{ID: message.ID, Role: message.Role, Parts: []MessagePart{}},
		})
	}
	for index := range segments {
		segments[index].Metadata = FinalSegmentMetadata{
			RunID:     run.RunID,
			MessageID: run.MessageID,
			Index:     index,
			Count:     len(segments),
		}
	}
	return UIMessage{ID: message.ID, Role: message.Role, Parts: []MessagePart{}}, segments
}

func FinalRunMetadata(run Run, segmentCount int) map[string]any {
	if segmentCount > 0 {
		run.Final = FinalDelivery{Delivery: "segmented", SegmentCount: segmentCount}
	} else {
		run.Final = FinalDelivery{Delivery: "inline", SegmentCount: 0}
	}
	return run.Metadata()
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
