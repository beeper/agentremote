package aistream

import (
	"fmt"

	"github.com/beeper/ai-bridge/pkg/ag-ui"
)

func (t Run) AI(kind string) BeeperAI {
	terminal := t.runTerminal()
	final := t.finalDelivery()
	return BeeperAI{
		Schema:     BeeperAISchema,
		Protocol:   "ag-ui",
		Kind:       kind,
		ThreadID:   t.ThreadID,
		RunID:      t.RunID,
		MessageID:  t.MessageID,
		Agent:      AgentMetadata{ID: t.AgentID, DisplayName: t.AgentName},
		Model:      t.Model,
		Approvals:  t.Approvals,
		Interrupts: t.Interrupts,
		Artifacts:  t.Artifacts,
		Data:       t.Data,
		Preview:    t.Preview,
		Terminal:   &terminal,
		Final:      &final,
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

func (t Run) finalDelivery() FinalDelivery {
	if t.Final.Delivery != "" {
		return t.Final
	}
	return FinalDelivery{Delivery: "inline", SegmentCount: 0}
}

func (t Run) runTerminal() RunTerminal {
	return RunTerminal{
		State:        t.Status.State,
		FinishReason: t.Status.FinishReason,
		Usage:        t.Usage,
		Outcome:      terminalOutcome(t.Status, t.Interrupts),
		Error:        terminalError(t.Status.Error),
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
