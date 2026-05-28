package aistream

import (
	"fmt"

	"github.com/beeper/ai-bridge/pkg/ag-ui"
)

func (t Run) Metadata() map[string]any {
	return t.RunMetadata().Map()
}

func (t Run) RunMetadata() RunMetadata {
	return RunMetadata{
		Schema:     "com.beeper.ai.run.v1",
		Protocol:   "ag-ui",
		ThreadID:   t.ThreadID,
		RunID:      t.RunID,
		MessageID:  t.MessageID,
		AgentID:    t.AgentID,
		AgentName:  t.AgentName,
		Model:      t.Model,
		Usage:      t.Usage,
		Status:     t.Status,
		Approvals:  t.Approvals,
		Interrupts: t.Interrupts,
		Artifacts:  t.Artifacts,
		Data:       t.Data,
		Preview:    t.Preview,
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
