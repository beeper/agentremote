package aistream

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"
)

const ApprovalCommandUsage = "/approve <approval-id> <approve|always|deny>"

type ApprovalCoordinator struct {
	mu      sync.Mutex
	pending map[string]*pendingApproval
}

type ApprovalCoordinatorHooks struct {
	PublishRequested func(context.Context, ApprovalRequest) (ApprovalRequest, error)
	BeforeResponded  func(context.Context, *ToolApprovalResponse) error
	PublishResponded func(context.Context, ApprovalRequest, ToolApprovalResponse) error
}

type pendingApproval struct {
	request  ApprovalRequest
	response chan ToolApprovalResponse
}

func NormalizeApprovalRequest(request ApprovalRequest, now func() time.Time) ApprovalRequest {
	if now == nil {
		now = time.Now
	}
	if request.ID == "" {
		request.ID = "approval-" + request.ToolCallID
	}
	if request.Approval.ID == "" {
		request.Approval.ID = request.ID
	}
	request.Approval.NeedsApproval = true
	if len(request.Choices) == 0 {
		request.Choices = DefaultApprovalChoices()
	}
	if request.ExpiresAt.IsZero() {
		request.ExpiresAt = now().Add(time.Minute)
	}
	return request
}

func ApprovalCommandForChoice(approvalID string, choice ApprovalChoice) string {
	return "/approve " + strings.TrimSpace(approvalID) + " " + choice.Key
}

func ApprovalCommandForID(approvalID string) string {
	return "/approve " + strings.TrimSpace(approvalID)
}

func ParseApprovalCommand(arg string, choices []ApprovalChoice, now func() time.Time) (ToolApprovalResponse, error) {
	approvalID, rawChoice, ok := strings.Cut(strings.TrimSpace(arg), " ")
	if !ok || strings.TrimSpace(approvalID) == "" || strings.TrimSpace(rawChoice) == "" {
		return ToolApprovalResponse{}, fmt.Errorf("Usage: %s", ApprovalCommandUsage)
	}
	choice, ok := ResolveApprovalChoice(choices, rawChoice)
	if !ok {
		return ToolApprovalResponse{}, fmt.Errorf("unknown approval choice %q", strings.TrimSpace(rawChoice))
	}
	response := ApprovalResponseForChoice(strings.TrimSpace(approvalID), choice)
	return StampApprovalResponse(response, now), nil
}

func StampApprovalResponse(response ToolApprovalResponse, now func() time.Time) ToolApprovalResponse {
	if response.RespondedAt != "" {
		return response
	}
	if now == nil {
		now = time.Now
	}
	response.RespondedAt = now().UTC().Format(time.RFC3339)
	return response
}

func ApprovalCommandReply(response ToolApprovalResponse) string {
	switch {
	case response.Always:
		return "Approval saved. Continuing."
	case response.Approved:
		return "Approved. Continuing."
	default:
		return "Denied. Continuing without the requested access."
	}
}

func (c *ApprovalCoordinator) Request(ctx context.Context, request ApprovalRequest, hooks ApprovalCoordinatorHooks) (ToolApprovalResponse, error) {
	if c == nil {
		return ToolApprovalResponse{ID: request.ID, Approved: false, Reason: "approval_unavailable"}, nil
	}
	request = NormalizeApprovalRequest(request, time.Now)
	pending := &pendingApproval{request: request, response: make(chan ToolApprovalResponse, 1)}
	if err := c.add(pending); err != nil {
		return ToolApprovalResponse{}, err
	}
	deletePending := true
	defer func() {
		if deletePending {
			c.Delete(request.ID)
		}
	}()

	if hooks.PublishRequested != nil {
		var err error
		request, err = hooks.PublishRequested(ctx, request)
		if err != nil {
			return ToolApprovalResponse{}, err
		}
		pending.request = request
	}

	response := c.wait(ctx, request, pending)
	c.Delete(request.ID)
	deletePending = false
	if response.ID == "" {
		response.ID = request.ID
	}
	response = StampApprovalResponse(response, time.Now)
	if hooks.BeforeResponded != nil {
		if err := hooks.BeforeResponded(ctx, &response); err != nil {
			return ToolApprovalResponse{}, err
		}
	}
	if hooks.PublishResponded != nil {
		if err := hooks.PublishResponded(ctx, request, response); err != nil {
			return ToolApprovalResponse{}, err
		}
	}
	return response, nil
}

func (c *ApprovalCoordinator) ResolveCommand(arg string, now func() time.Time) (ToolApprovalResponse, bool, error) {
	if c == nil {
		return ToolApprovalResponse{}, false, nil
	}
	approvalID, rawChoice, ok := strings.Cut(strings.TrimSpace(arg), " ")
	if strings.TrimSpace(approvalID) == "" {
		response, err := ParseApprovalCommand(arg, DefaultApprovalChoices(), now)
		return response, false, err
	}
	approvalID = strings.TrimSpace(approvalID)
	c.mu.Lock()
	pending := c.pending[approvalID]
	c.mu.Unlock()
	if pending == nil {
		return ToolApprovalResponse{ID: approvalID}, false, nil
	}
	if !ok || strings.TrimSpace(rawChoice) == "" {
		if len(pending.request.Choices) == 0 {
			return ToolApprovalResponse{}, false, fmt.Errorf("approval %s has no choices", approvalID)
		}
		arg = approvalID + " " + pending.request.Choices[0].Key
	}
	response, err := ParseApprovalCommand(arg, pending.request.Choices, now)
	if err != nil {
		return ToolApprovalResponse{}, false, err
	}
	return response, c.Resolve(response), nil
}

func (c *ApprovalCoordinator) Resolve(response ToolApprovalResponse) bool {
	if c == nil || response.ID == "" {
		return false
	}
	c.mu.Lock()
	pending := c.pending[response.ID]
	c.mu.Unlock()
	if pending == nil {
		return false
	}
	select {
	case pending.response <- response:
		return true
	default:
		return false
	}
}

func (c *ApprovalCoordinator) Delete(approvalID string) {
	if c == nil || approvalID == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.pending, approvalID)
}

func (c *ApprovalCoordinator) add(pending *pendingApproval) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.pending == nil {
		c.pending = map[string]*pendingApproval{}
	}
	if _, exists := c.pending[pending.request.ID]; exists {
		return fmt.Errorf("approval %s is already pending", pending.request.ID)
	}
	c.pending[pending.request.ID] = pending
	return nil
}

func (c *ApprovalCoordinator) wait(ctx context.Context, request ApprovalRequest, pending *pendingApproval) ToolApprovalResponse {
	wait := time.Until(request.ExpiresAt)
	if wait <= 0 {
		return TimedOutApprovalResponse(request.ID)
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case response := <-pending.response:
		return response
	case <-timer.C:
		return TimedOutApprovalResponse(request.ID)
	case <-ctx.Done():
		return ToolApprovalResponse{ID: request.ID, Approved: false, Reason: "aborted"}
	}
}
