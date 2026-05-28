package connector

import (
	"context"

	"github.com/beeper/ai-bridge/pkg/agent/autocompact"
	"github.com/beeper/ai-bridge/pkg/agent/harness"
	"github.com/beeper/ai-bridge/pkg/agent/harness/session"
	ai "github.com/beeper/ai-bridge/pkg/ai"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/id"
)

func (cl *Client) runAutoCompaction(ctx context.Context, publisher bridgev2.BeeperStreamPublisher, roomID id.RoomID, eventID id.EventID, agentHarness *harness.AgentHarness, agentSession *session.Session, model ai.Model, assistantMessage ai.Message) {
	runner := autocompact.Runner{Harness: agentHarness, Session: agentSession, Model: model}
	reason, ok, err := runner.ShouldCompact(ctx, assistantMessage)
	if err != nil || !ok {
		return
	}
	_ = publisher.Publish(ctx, roomID, eventID, map[string]any{"op": "compaction_start", "reason": reason})
	result, err := runner.CheckAndCompact(ctx, assistantMessage)
	if err != nil {
		_ = publisher.Publish(ctx, roomID, eventID, map[string]any{"op": "compaction_error", "reason": reason, "message": err.Error()})
		return
	}
	_ = publisher.Publish(ctx, roomID, eventID, map[string]any{
		"op":                  "compaction_done",
		"reason":              result.Reason,
		"summary":             result.Compaction.Summary,
		"first_kept_entry_id": result.Compaction.FirstKeptEntryID,
		"tokens_before":       result.Compaction.TokensBefore,
	})
}
