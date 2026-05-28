package connector

import (
	"context"

	"github.com/beeper/ai-bridge/pkg/agent/autocompact"
	"github.com/beeper/ai-bridge/pkg/agent/harness"
	"github.com/beeper/ai-bridge/pkg/agent/harness/session"
	ai "github.com/beeper/ai-bridge/pkg/ai"
)

func (cl *Client) runAutoCompaction(ctx context.Context, agentHarness *harness.AgentHarness, agentSession *session.Session, model ai.Model, assistantMessage ai.Message) {
	runner := autocompact.Runner{Harness: agentHarness, Session: agentSession, Model: model}
	_, ok, err := runner.ShouldCompact(ctx, assistantMessage)
	if err != nil || !ok {
		return
	}
	_, _ = runner.CheckAndCompact(ctx, assistantMessage)
}
