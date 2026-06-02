package connector

import (
	"context"
	"fmt"
	"strings"

	"maunium.net/go/mautrix/bridgev2"

	aistream "github.com/beeper/ai-bridge/pkg/ai-stream"
)

func runApproveCommand(cl *Client, ctx context.Context, portal *bridgev2.Portal, _ RoomConfig, arg string, responder aiCommandResponder) error {
	active := cl.getActiveRun(portal.PortalKey)
	if active == nil {
		return fmt.Errorf("approval is not pending")
	}
	response, ok, err := active.resolveApprovalCommand(arg)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("approval %s is not pending", response.ID)
	}
	return responder.Reply(ctx, aistream.ApprovalCommandReply(response))
}

func runResetApprovalsCommand(cl *Client, ctx context.Context, _ *bridgev2.Portal, _ RoomConfig, arg string, responder aiCommandResponder) error {
	if strings.TrimSpace(arg) != "" {
		return fmt.Errorf("Usage: /reset-approvals")
	}
	if err := cl.resetApprovalDecisions(ctx); err != nil {
		return err
	}
	return responder.Reply(ctx, "Saved AI approvals reset.")
}
