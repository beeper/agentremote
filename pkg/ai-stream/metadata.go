package aistream

import (
	"fmt"

	"github.com/beeper/ai-bridge/pkg/ag-ui"
)

func (t Run) Metadata() map[string]any {
	return t.RunMetadata().Map()
}

func (t Run) AI(kind string) BeeperAI {
	metadata := t.RunMetadata()
	terminal := metadata.BuildTerminal()
	final := metadata.Final
	return BeeperAI{
		Schema:       BeeperAISchema,
		Protocol:     "ag-ui",
		Kind:         kind,
		ThreadID:     t.ThreadID,
		RunID:        t.RunID,
		MessageID:    t.MessageID,
		Agent:        metadata.Agent,
		Model:        metadata.Model,
		Usage:        metadata.Usage,
		UsageDetails: metadata.UsageDetails,
		Status:       metadata.Status,
		Approvals:    metadata.Approvals,
		Interrupts:   metadata.Interrupts,
		Artifacts:    metadata.Artifacts,
		Data:         metadata.Data,
		Preview:      metadata.Preview,
		Terminal:     &terminal,
		Final:        &final,
	}
}

func (t Run) AIWithMessage(kind string, message UIMessage) BeeperAI {
	payload := t.AI(kind)
	payload.Message = &message
	return payload
}

func (t Run) AIStream(envelopes []Envelope) BeeperAI {
	payload := t.AI(AIKindStream)
	payload.Events = envelopes
	return payload
}

func (t Run) AISegment(message UIMessage, segment FinalSegmentMetadata) BeeperAI {
	payload := t.AIWithMessage(AIKindSegment, message)
	payload.Segment = &segment
	return payload
}

func (t Run) RunMetadata() RunMetadata {
	metadata := RunMetadata{
		Schema:    BeeperAISchema,
		Protocol:  "ag-ui",
		ThreadID:  t.ThreadID,
		RunID:     t.RunID,
		MessageID: t.MessageID,
		Agent:     AgentMetadata{ID: t.AgentID, DisplayName: t.AgentName},
		Model:     t.Model,
		Usage:     t.Usage,
		UsageDetails: map[string]any{
			"reasoningTokens": t.Usage.ReasoningTokens,
		},
		Status:     t.Status,
		Approvals:  t.Approvals,
		Interrupts: t.Interrupts,
		Artifacts:  t.Artifacts,
		Data:       t.Data,
		Preview:    t.Preview,
		Final:      t.finalDelivery(),
	}
	metadata.Terminal = metadata.BuildTerminal()
	return metadata
}

func (t Run) finalDelivery() FinalDelivery {
	if t.Final.Delivery != "" {
		return t.Final
	}
	return FinalDelivery{Delivery: "inline", SegmentCount: 0}
}

func (t Run) StreamMetadata() StreamMetadata {
	return StreamMetadata{
		Schema:    BeeperAISchema,
		Protocol:  "ag-ui",
		ThreadID:  t.ThreadID,
		RunID:     t.RunID,
		MessageID: t.MessageID,
		AgentID:   t.AgentID,
	}
}

func (t Run) Validate() error {
	for i, evt := range t.Events {
		if err := agui.ValidateEvent(evt); err != nil {
			return fmt.Errorf("event %d: %w", i+1, err)
		}
	}
	return nil
}
