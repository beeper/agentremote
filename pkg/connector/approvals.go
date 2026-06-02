package connector

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/id"

	aistream "github.com/beeper/ai-bridge/pkg/ai-stream"
	aibridgev2 "github.com/beeper/ai-bridge/pkg/ai-stream/bridgev2"
	aiutils "github.com/beeper/ai-bridge/pkg/ai/utils"
	"github.com/beeper/ai-bridge/pkg/aiid"
	"github.com/beeper/ai-bridge/pkg/chattools"
)

const (
	beeperProfileApprovalKey = "beeper_profile_access"
	approvalDecisionApprove  = "approved"
	approvalDecisionDeny     = "denied"
)

func (r *activeAIRun) requestApproval(ctx context.Context, cl *Client, publisher bridgev2.BeeperStreamPublisher, portal *bridgev2.Portal, request aistream.ApprovalRequest, beforePublish func(context.Context, *aistream.ToolApprovalResponse) error) (aistream.ToolApprovalResponse, error) {
	if r == nil || cl == nil || cl.UserLogin == nil || publisher == nil || portal == nil {
		return aistream.ToolApprovalResponse{ID: request.ID, Approved: false, Reason: "approval_unavailable"}, nil
	}
	stream := r.currentAssistantStream()
	if stream == nil {
		return aistream.ToolApprovalResponse{ID: request.ID, Approved: false, Reason: "approval_unavailable"}, nil
	}
	if request.ExpiresAt.IsZero() {
		request.ExpiresAt = time.Now().Add(activeStreamIdleTimeout)
	}
	return r.approvalCoordinator().Request(ctx, request, aistream.ApprovalCoordinatorHooks{
		PublishRequested: func(ctx context.Context, request aistream.ApprovalRequest) (aistream.ApprovalRequest, error) {
			ctxMeta := r.approvalContext(stream, request)
			if err := r.publishApprovalInterrupt(ctx, cl, publisher, portal.MXID, stream, request); err != nil {
				return aistream.ApprovalRequest{}, err
			}
			queued := cl.UserLogin.QueueRemoteEvent(aibridgev2.ApprovalPrompt(portal.PortalKey, aiid.AssistantUserID(), ctxMeta, time.Now()))
			if queued.Success && queued.EventID != "" {
				request.ApprovalEventID = string(queued.EventID)
				if err := r.publishApprovalEventLink(ctx, cl, publisher, portal.MXID, stream, request); err != nil {
					return aistream.ApprovalRequest{}, err
				}
			}
			return request, nil
		},
		BeforeResponded: beforePublish,
		PublishResponded: func(ctx context.Context, request aistream.ApprovalRequest, response aistream.ToolApprovalResponse) error {
			return r.publishApprovalResponse(ctx, cl, publisher, portal.MXID, stream, request, response)
		},
	})
}

func (r *activeAIRun) approvalContext(stream *assistantStreamState, request aistream.ApprovalRequest) aistream.ApprovalContext {
	ctxMeta := aistream.ApprovalContext{
		ID:          request.ID,
		ThreadID:    stream.run.ThreadID,
		RunID:       stream.run.RunID,
		MessageID:   stream.run.MessageID,
		Command:     aistream.ApprovalCommandForID(request.ID),
		ToolCallID:  request.ToolCallID,
		ToolName:    request.ToolName,
		Title:       request.Title,
		Description: request.Description,
		PlanText:    request.PlanText,
		ExpiresAt:   request.ExpiresAt.UTC().Format(time.RFC3339),
		Choices:     request.Choices,
		TargetEvent: string(stream.eventID),
		AgentID:     stream.run.AgentID,
		AgentName:   stream.run.AgentName,
		Model:       stream.run.Model,
		Metadata:    request.Metadata,
	}
	if stream.run.Preview.Truncated || stream.run.Preview.Text != "" {
		ctxMeta.PreviewText = stream.run.Preview.Text
		ctxMeta.PreviewTruncated = stream.run.Preview.Truncated
	}
	return ctxMeta
}

func (r *activeAIRun) publishApprovalInterrupt(ctx context.Context, cl *Client, publisher bridgev2.BeeperStreamPublisher, roomID id.RoomID, stream *assistantStreamState, request aistream.ApprovalRequest) error {
	stream.publish.mu.Lock()
	defer stream.publish.mu.Unlock()
	writer := aistream.NewWriter(stream.run, time.Now)
	writer.ToolApprovalRequestedWithRequest(request)
	writer.InterruptWithUsage(nil)
	return cl.publishNewStreamEvents(ctx, publisher, roomID, stream.eventID, stream.run, &stream.publish)
}

func (r *activeAIRun) publishApprovalEventLink(ctx context.Context, cl *Client, publisher bridgev2.BeeperStreamPublisher, roomID id.RoomID, stream *assistantStreamState, request aistream.ApprovalRequest) error {
	stream.publish.mu.Lock()
	defer stream.publish.mu.Unlock()
	writer := aistream.NewWriter(stream.run, time.Now)
	writer.ApprovalEventLinked(request.ID, request.ApprovalEventID)
	return cl.publishNewStreamEvents(ctx, publisher, roomID, stream.eventID, stream.run, &stream.publish)
}

func (r *activeAIRun) publishApprovalResponse(ctx context.Context, cl *Client, publisher bridgev2.BeeperStreamPublisher, roomID id.RoomID, stream *assistantStreamState, request aistream.ApprovalRequest, response aistream.ToolApprovalResponse) error {
	stream.publish.mu.Lock()
	defer stream.publish.mu.Unlock()
	writer := aistream.NewWriter(stream.run, time.Now)
	writer.ToolApprovalResponded(request.ToolCallID, request.ToolName, request.Input, response)
	return cl.publishNewStreamEvents(ctx, publisher, roomID, stream.eventID, stream.run, &stream.publish)
}

func (r *activeAIRun) resolveApprovalCommand(arg string) (aistream.ToolApprovalResponse, bool, error) {
	if r == nil {
		return aistream.ToolApprovalResponse{}, false, nil
	}
	r.mu.Lock()
	approvals := r.approvals
	r.mu.Unlock()
	if approvals == nil {
		return aistream.ToolApprovalResponse{}, false, nil
	}
	return approvals.ResolveCommand(arg, nil)
}

func (r *activeAIRun) approvalCoordinator() *aistream.ApprovalCoordinator {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.approvals == nil {
		r.approvals = &aistream.ApprovalCoordinator{}
	}
	return r.approvals
}

func (cl *Client) resolveBeeperProfileForSession(ctx context.Context, portal *bridgev2.Portal, publisher bridgev2.BeeperStreamPublisher, active *activeAIRun, toolCallID string) (*chattools.SessionProfile, error) {
	switch decision, ok := cl.approvalDecision(beeperProfileApprovalKey); {
	case ok && decision == approvalDecisionApprove:
		return cl.fetchBeeperProfile(ctx)
	case ok && decision == approvalDecisionDeny:
		return nil, nil
	}
	if active == nil {
		return nil, nil
	}
	request := beeperProfileApprovalRequest(toolCallID)
	response, err := active.requestApproval(ctx, cl, publisher, portal, request, func(ctx context.Context, response *aistream.ToolApprovalResponse) error {
		if response.Always {
			if err := cl.saveApprovalDecision(ctx, beeperProfileApprovalKey, approvalDecisionApprove); err != nil {
				return err
			}
			response.Persisted = true
		} else if !response.Approved && response.Reason == "denied" {
			if err := cl.saveApprovalDecision(ctx, beeperProfileApprovalKey, approvalDecisionDeny); err != nil {
				return err
			}
			response.Persisted = true
		}
		return nil
	})
	if err != nil || !response.Approved {
		return nil, err
	}
	return cl.fetchBeeperProfile(ctx)
}

func beeperProfileApprovalRequest(toolCallID string) aistream.ApprovalRequest {
	title := "Can I access your Beeper profile, like your email and username?"
	description := "This **will not** allow me to see any of your chats, messages or contacts."
	displayName := "Access Beeper profile"
	approvalID := "approval-" + strings.TrimSpace(toolCallID)
	if approvalID == "approval-" {
		approvalID = fmt.Sprintf("approval-%d", time.Now().UnixNano())
	}
	return aistream.ApprovalRequest{
		ID:          approvalID,
		ToolCallID:  toolCallID,
		ToolName:    "get_session",
		Title:       title,
		Description: description,
		PlanText:    title + "\n\n" + description,
		Input:       map[string]any{},
		Metadata: map[string]any{
			"title":       title,
			"displayName": displayName,
			"description": description,
		},
	}
}

func (cl *Client) approvalDecision(key string) (string, bool) {
	if cl == nil || cl.UserLogin == nil {
		return "", false
	}
	meta, _ := cl.UserLogin.Metadata.(*aiid.UserLoginMetadata)
	if meta == nil || meta.Approvals == nil {
		return "", false
	}
	decision := strings.TrimSpace(meta.Approvals[key].Decision)
	return decision, decision != ""
}

func (cl *Client) saveApprovalDecision(ctx context.Context, key, decision string) error {
	if cl == nil || cl.UserLogin == nil {
		return fmt.Errorf("missing login for approval decision")
	}
	meta, ok := cl.UserLogin.Metadata.(*aiid.UserLoginMetadata)
	if !ok || meta == nil {
		meta = &aiid.UserLoginMetadata{}
		cl.UserLogin.Metadata = meta
	}
	if meta.Approvals == nil {
		meta.Approvals = map[string]aiid.ApprovalDecision{}
	}
	meta.Approvals[key] = aiid.ApprovalDecision{
		Decision:  decision,
		UpdatedAt: time.Now().UTC().Format(time.RFC3339),
	}
	return cl.UserLogin.Save(ctx)
}

func (cl *Client) resetApprovalDecisions(ctx context.Context) error {
	if cl == nil || cl.UserLogin == nil {
		return fmt.Errorf("missing login for approval reset")
	}
	meta, ok := cl.UserLogin.Metadata.(*aiid.UserLoginMetadata)
	if !ok || meta == nil {
		meta = &aiid.UserLoginMetadata{}
		cl.UserLogin.Metadata = meta
	}
	meta.Approvals = nil
	return cl.UserLogin.Save(ctx)
}

type aiServicesWhoamiResponse struct {
	Email           string `json:"email"`
	FullName        string `json:"full_name"`
	MatrixProfile   any    `json:"matrix_profile"`
	GravatarProfile any    `json:"gravatar_profile"`
	Username        string `json:"username"`
}

func (cl *Client) fetchBeeperProfile(ctx context.Context) (*chattools.SessionProfile, error) {
	provider, err := cl.defaultAIProviderForLimits()
	if err != nil {
		return nil, err
	}
	whoamiURL, err := aiServicesWhoamiURL(provider.BaseURL)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, whoamiURL, nil)
	if err != nil {
		return nil, err
	}
	token, err := cl.defaultProviderBearerToken()
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	client := aiutils.WithAIServicesLogging(&http.Client{Timeout: 20 * time.Second})
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4*1024))
		return nil, fmt.Errorf("AI Services whoami returned HTTP %d", resp.StatusCode)
	}
	var body aiServicesWhoamiResponse
	if err = json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, err
	}
	return &chattools.SessionProfile{
		Email:           body.Email,
		Username:        body.Username,
		FullName:        body.FullName,
		MatrixProfile:   body.MatrixProfile,
		GravatarProfile: body.GravatarProfile,
	}, nil
}

func aiServicesWhoamiURL(baseURL string) (string, error) {
	parsed, err := url.Parse(strings.TrimRight(normalizeResponsesBaseURL(baseURL), "/"))
	if err != nil {
		return "", err
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/") + "/whoami"
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return parsed.String(), nil
}
