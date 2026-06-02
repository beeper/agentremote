package aistream

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestParseApprovalCommandBuildsResponse(t *testing.T) {
	now := func() time.Time { return time.Unix(10, 0) }
	response, err := ParseApprovalCommand("approval-1 always", DefaultApprovalChoices(), now)
	if err != nil {
		t.Fatal(err)
	}
	if response.ID != "approval-1" || !response.Approved || !response.Always || response.Choice != ApprovalChoiceAlwaysApprove {
		t.Fatalf("bad approval response: %#v", response)
	}
	if response.RespondedAt != "1970-01-01T00:00:10Z" {
		t.Fatalf("bad response timestamp: %#v", response)
	}

	if _, err := ParseApprovalCommand("approval-1 nope", DefaultApprovalChoices(), now); err == nil || !strings.Contains(err.Error(), "unknown approval choice") {
		t.Fatalf("expected unknown choice error, got %v", err)
	}
}

func TestApprovalCoordinatorWaitsForResolveAndPublishesLifecycle(t *testing.T) {
	coordinator := &ApprovalCoordinator{}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	request := ApprovalRequest{
		ID:         "approval-1",
		ToolCallID: "tool-1",
		ToolName:   "fetch",
		ExpiresAt:  time.Now().Add(5 * time.Second),
	}
	requested := make(chan struct{}, 1)
	responded := make(chan struct{}, 1)
	done := make(chan ToolApprovalResponse, 1)
	go func() {
		response, err := coordinator.Request(ctx, request, ApprovalCoordinatorHooks{
			PublishRequested: func(ctx context.Context, request ApprovalRequest) (ApprovalRequest, error) {
				requested <- struct{}{}
				return request, nil
			},
			PublishResponded: func(ctx context.Context, request ApprovalRequest, response ToolApprovalResponse) error {
				responded <- struct{}{}
				return nil
			},
		})
		if err != nil {
			t.Errorf("request failed: %v", err)
		}
		done <- response
	}()

	select {
	case <-requested:
	case <-time.After(time.Second):
		t.Fatal("approval request was not published")
	}
	if !coordinator.Resolve(ToolApprovalResponse{ID: "approval-1", Approved: true}) {
		t.Fatal("approval did not resolve")
	}
	var response ToolApprovalResponse
	select {
	case response = <-done:
	case <-time.After(time.Second):
		t.Fatal("approval request did not complete")
	}
	select {
	case <-responded:
	case <-time.After(time.Second):
		t.Fatal("approval response was not published")
	}
	if !response.Approved || response.ID != "approval-1" || response.RespondedAt == "" {
		t.Fatalf("bad resolved response=%#v", response)
	}
}

func TestApprovalCoordinatorResolvesCommandWithPendingChoices(t *testing.T) {
	coordinator := &ApprovalCoordinator{}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	request := ApprovalRequest{
		ID:        "approval-1",
		Choices:   []ApprovalChoice{{Key: ApprovalChoiceApprove, Label: "Allow"}},
		ExpiresAt: time.Now().Add(5 * time.Second),
	}
	requested := make(chan struct{}, 1)
	done := make(chan ToolApprovalResponse, 1)
	go func() {
		response, err := coordinator.Request(ctx, request, ApprovalCoordinatorHooks{
			PublishRequested: func(ctx context.Context, request ApprovalRequest) (ApprovalRequest, error) {
				requested <- struct{}{}
				return request, nil
			},
		})
		if err != nil {
			t.Errorf("request failed: %v", err)
		}
		done <- response
	}()

	select {
	case <-requested:
	case <-time.After(time.Second):
		t.Fatal("approval request was not published")
	}
	if response, ok, err := coordinator.ResolveCommand("approval-1 deny", nil); err == nil || ok || response.ID != "" {
		t.Fatalf("unexpected denied choice resolution: response=%#v ok=%v err=%v", response, ok, err)
	}
	response, ok, err := coordinator.ResolveCommand("approval-1 approve", nil)
	if err != nil || !ok || !response.Approved {
		t.Fatalf("approval command did not resolve: response=%#v ok=%v err=%v", response, ok, err)
	}
	select {
	case response = <-done:
	case <-time.After(time.Second):
		t.Fatal("approval request did not complete")
	}
	if !response.Approved || response.ID != "approval-1" {
		t.Fatalf("bad resolved response: %#v", response)
	}
}

func TestApprovalCoordinatorBareCommandUsesFirstPendingChoice(t *testing.T) {
	coordinator := &ApprovalCoordinator{}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	request := ApprovalRequest{
		ID:        "approval-1",
		Choices:   DefaultApprovalChoices(),
		ExpiresAt: time.Now().Add(5 * time.Second),
	}
	requested := make(chan struct{}, 1)
	done := make(chan ToolApprovalResponse, 1)
	go func() {
		response, err := coordinator.Request(ctx, request, ApprovalCoordinatorHooks{
			PublishRequested: func(ctx context.Context, request ApprovalRequest) (ApprovalRequest, error) {
				requested <- struct{}{}
				return request, nil
			},
		})
		if err != nil {
			t.Errorf("request failed: %v", err)
		}
		done <- response
	}()

	select {
	case <-requested:
	case <-time.After(time.Second):
		t.Fatal("approval request was not published")
	}
	response, ok, err := coordinator.ResolveCommand("approval-1", nil)
	if err != nil || !ok || !response.Approved || response.Choice != ApprovalChoiceApprove {
		t.Fatalf("bare approval command did not resolve to first choice: response=%#v ok=%v err=%v", response, ok, err)
	}
	select {
	case response = <-done:
	case <-time.After(time.Second):
		t.Fatal("approval request did not complete")
	}
	if !response.Approved || response.Choice != ApprovalChoiceApprove {
		t.Fatalf("bad resolved response: %#v", response)
	}
}

func TestApprovalCoordinatorTimesOut(t *testing.T) {
	coordinator := &ApprovalCoordinator{}
	response, err := coordinator.Request(context.Background(), ApprovalRequest{
		ID:        "approval-1",
		ExpiresAt: time.Now().Add(-time.Second),
	}, ApprovalCoordinatorHooks{})
	if err != nil {
		t.Fatal(err)
	}
	if response.Approved || response.Reason != "timed_out" {
		t.Fatalf("bad timeout response: %#v", response)
	}
}
