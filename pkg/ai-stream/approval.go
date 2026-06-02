package aistream

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	agui "github.com/beeper/ai-bridge/pkg/ag-ui"
)

const (
	ApprovalChoiceApprove       = "approve"
	ApprovalChoiceAlwaysApprove = "always_approve"
	ApprovalChoiceDeny          = "deny"
)

type ApprovalChoice struct {
	Key      string `json:"key"`
	Label    string `json:"label"`
	Style    string `json:"style,omitempty"`
	Shortcut string `json:"shortcut,omitempty"`
}

type ApprovalTimeout struct {
	After  time.Duration
	Reason string
}

type ApprovalQueue struct {
	mu      sync.Mutex
	active  *ApprovalPrompt
	pending []ApprovalPrompt
	timeout ApprovalTimeout
}

type ApprovalContext struct {
	ID               string           `json:"id"`
	ThreadID         string           `json:"threadId"`
	RunID            string           `json:"runId"`
	MessageID        string           `json:"messageId"`
	Command          string           `json:"command"`
	ToolCallID       string           `json:"toolCallId"`
	ToolName         string           `json:"toolName"`
	Title            string           `json:"title,omitempty"`
	Description      string           `json:"description,omitempty"`
	PlanText         string           `json:"planText,omitempty"`
	ExpiresAt        string           `json:"expiresAt,omitempty"`
	Choices          []ApprovalChoice `json:"choices,omitempty"`
	TargetEvent      string           `json:"targetEvent"`
	AgentID          string           `json:"agentId,omitempty"`
	AgentName        string           `json:"agentName,omitempty"`
	Model            string           `json:"model,omitempty"`
	SeqStart         int              `json:"seqStart,omitempty"`
	PreviewText      string           `json:"previewText,omitempty"`
	PreviewTruncated bool             `json:"previewTruncated,omitempty"`
	Metadata         map[string]any   `json:"metadata,omitempty"`
}

type ApprovalNotice struct {
	Schema      string           `json:"schema"`
	ID          string           `json:"id"`
	MessageID   string           `json:"messageId"`
	ToolCallID  string           `json:"toolCallId"`
	ToolName    string           `json:"toolName"`
	Title       string           `json:"title,omitempty"`
	Description string           `json:"description,omitempty"`
	PlanText    string           `json:"planText,omitempty"`
	ExpiresAt   string           `json:"expiresAt,omitempty"`
	State       string           `json:"state"`
	Choices     []ApprovalChoice `json:"choices"`
	Metadata    map[string]any   `json:"metadata,omitempty"`
}

type ToolApproval struct {
	ID            string         `json:"id"`
	NeedsApproval bool           `json:"needsApproval"`
	EditedArgs    map[string]any `json:"editedArgs,omitempty"`
}

type ToolApprovalResponse struct {
	ID          string         `json:"id"`
	Approved    bool           `json:"approved"`
	Always      bool           `json:"always,omitempty"`
	Choice      string         `json:"choice,omitempty"`
	Persisted   bool           `json:"persisted,omitempty"`
	RespondedAt string         `json:"respondedAt,omitempty"`
	Reason      string         `json:"reason,omitempty"`
	EditedArgs  map[string]any `json:"editedArgs,omitempty"`
	Metadata    map[string]any `json:"metadata,omitempty"`
}

type ApprovalInterruptMetadata struct {
	ThreadID          string           `json:"threadId"`
	RunID             string           `json:"runId"`
	MessageID         string           `json:"messageId"`
	ToolName          string           `json:"toolName"`
	Title             string           `json:"title,omitempty"`
	Description       string           `json:"description,omitempty"`
	PlanText          string           `json:"planText,omitempty"`
	Input             any              `json:"input"`
	Approval          ToolApproval     `json:"approval"`
	ApprovalMessageID string           `json:"approvalMessageId"`
	ApprovalEventID   string           `json:"approvalEventId,omitempty"`
	ExpiresAt         string           `json:"expiresAt,omitempty"`
	Choices           []ApprovalChoice `json:"choices"`
	Metadata          map[string]any   `json:"metadata,omitempty"`
}

type ApprovalResponsePayload struct {
	Approved    bool           `json:"approved"`
	Always      bool           `json:"always,omitempty"`
	Choice      string         `json:"choice,omitempty"`
	Persisted   bool           `json:"persisted,omitempty"`
	RespondedAt string         `json:"respondedAt,omitempty"`
	Reason      string         `json:"reason,omitempty"`
	EditedArgs  map[string]any `json:"editedArgs,omitempty"`
	Metadata    map[string]any `json:"metadata,omitempty"`
}

type ApprovalResponseJSONSchema struct {
	Type       string                           `json:"type"`
	Properties ApprovalResponseSchemaProperties `json:"properties"`
	Required   []string                         `json:"required"`
}

type ApprovalResponseSchemaProperties struct {
	Approved    agui.JSONSchema `json:"approved"`
	Always      agui.JSONSchema `json:"always"`
	Choice      agui.JSONSchema `json:"choice"`
	Persisted   agui.JSONSchema `json:"persisted"`
	RespondedAt agui.JSONSchema `json:"respondedAt"`
	Reason      agui.JSONSchema `json:"reason"`
	EditedArgs  agui.JSONSchema `json:"editedArgs"`
	Metadata    agui.JSONSchema `json:"metadata"`
}

type ApprovalToolResult struct {
	ApprovalID  string         `json:"approvalId"`
	Approved    bool           `json:"approved"`
	Always      bool           `json:"always,omitempty"`
	Choice      string         `json:"choice,omitempty"`
	Persisted   bool           `json:"persisted,omitempty"`
	RespondedAt string         `json:"respondedAt,omitempty"`
	State       string         `json:"state"`
	Status      string         `json:"status"`
	Reason      string         `json:"reason,omitempty"`
	EditedArgs  map[string]any `json:"editedArgs,omitempty"`
	Metadata    map[string]any `json:"metadata,omitempty"`
}

type ApprovalRequest struct {
	ID              string
	ToolCallID      string
	ToolName        string
	Title           string
	Description     string
	PlanText        string
	Input           any
	Approval        ToolApproval
	ApprovalEventID string
	Choices         []ApprovalChoice
	Metadata        map[string]any
	ExpiresAt       time.Time
}

func NewApprovalInterrupt(run Run, toolCallID, toolName string, input any, approval ToolApproval, metadata map[string]any) agui.Interrupt {
	return NewApprovalInterruptFromRequest(run, ApprovalRequest{
		ID:         approval.ID,
		ToolCallID: toolCallID,
		ToolName:   toolName,
		Input:      input,
		Approval:   approval,
		Metadata:   metadata,
	})
}

func NewApprovalInterruptFromRequest(run Run, request ApprovalRequest) agui.Interrupt {
	approval := request.Approval
	if approval.ID == "" {
		approval.ID = request.ID
	}
	if approval.ID == "" {
		approval.ID = request.ToolCallID
	}
	approval.NeedsApproval = true
	choices := request.Choices
	if len(choices) == 0 {
		choices = DefaultApprovalChoices()
	}
	expiresAt := ""
	if !request.ExpiresAt.IsZero() {
		expiresAt = request.ExpiresAt.UTC().Format(time.RFC3339)
	}
	interruptMetadata := ApprovalInterruptMetadata{
		ThreadID:          run.ThreadID,
		RunID:             run.RunID,
		MessageID:         run.MessageID,
		ToolName:          request.ToolName,
		Title:             request.Title,
		Description:       request.Description,
		PlanText:          request.PlanText,
		Input:             request.Input,
		Approval:          approval,
		ApprovalMessageID: approval.ID,
		ApprovalEventID:   request.ApprovalEventID,
		ExpiresAt:         expiresAt,
		Choices:           choices,
		Metadata:          request.Metadata,
	}
	message := strings.TrimSpace(request.Title)
	if message == "" && request.ToolName != "" {
		message = fmt.Sprintf("Approve %s?", request.ToolName)
	}
	if message == "" {
		message = "Approve tool call?"
	}
	return agui.Interrupt{
		ID:             approval.ID,
		Reason:         agui.InterruptReasonToolCall,
		Message:        message,
		ToolCallID:     request.ToolCallID,
		ResponseSchema: ApprovalResponseSchema(),
		ExpiresAt:      expiresAt,
		Metadata:       interruptMetadata.Map(),
	}
}

func ApprovalResponseSchema() agui.JSONSchema {
	return NewApprovalResponseJSONSchema().Map()
}

func NewApprovalResponseJSONSchema() ApprovalResponseJSONSchema {
	return ApprovalResponseJSONSchema{
		Type: agui.JSONSchemaTypeObject,
		Properties: ApprovalResponseSchemaProperties{
			Approved:    agui.BooleanSchema(),
			Always:      agui.BooleanSchema(),
			Choice:      agui.StringSchema(),
			Persisted:   agui.BooleanSchema(),
			RespondedAt: agui.StringSchema(),
			Reason:      agui.StringSchema(),
			EditedArgs:  agui.ObjectSchema(nil),
			Metadata:    agui.ObjectSchema(nil),
		},
		Required: []string{"approved"},
	}
}

func (schema ApprovalResponseJSONSchema) Map() agui.JSONSchema {
	return agui.ObjectSchema(schema.Properties.Map(), schema.Required...)
}

func (properties ApprovalResponseSchemaProperties) Map() agui.JSONSchemaProperties {
	return agui.JSONSchemaProperties{
		"approved":    properties.Approved,
		"always":      properties.Always,
		"choice":      properties.Choice,
		"persisted":   properties.Persisted,
		"respondedAt": properties.RespondedAt,
		"reason":      properties.Reason,
		"editedArgs":  properties.EditedArgs,
		"metadata":    properties.Metadata,
	}
}

func ApprovalResponsePayloadFromResponse(response ToolApprovalResponse) ApprovalResponsePayload {
	return ApprovalResponsePayload{
		Approved:    response.Approved,
		Always:      response.Always,
		Choice:      response.Choice,
		Persisted:   response.Persisted,
		RespondedAt: response.RespondedAt,
		Reason:      response.Reason,
		EditedArgs:  response.EditedArgs,
		Metadata:    response.Metadata,
	}
}

func ApprovalResponseFromPayload(approvalID string, payload any) (ToolApprovalResponse, bool) {
	switch typed := payload.(type) {
	case ApprovalResponsePayload:
		return approvalResponseFromPayloadValue(approvalID, typed), true
	case *ApprovalResponsePayload:
		if typed == nil {
			return ToolApprovalResponse{}, false
		}
		return approvalResponseFromPayloadValue(approvalID, *typed), true
	case map[string]any:
		approved, ok := typed["approved"].(bool)
		if !ok {
			return ToolApprovalResponse{}, false
		}
		response := ToolApprovalResponse{ID: approvalID, Approved: approved}
		response.Always, _ = typed["always"].(bool)
		response.Choice, _ = typed["choice"].(string)
		response.Persisted, _ = typed["persisted"].(bool)
		response.RespondedAt, _ = typed["respondedAt"].(string)
		response.Reason, _ = typed["reason"].(string)
		response.EditedArgs, _ = typed["editedArgs"].(map[string]any)
		response.Metadata, _ = typed["metadata"].(map[string]any)
		return response, true
	case []byte:
		var value map[string]any
		if err := json.Unmarshal(typed, &value); err != nil {
			return ToolApprovalResponse{}, false
		}
		return ApprovalResponseFromPayload(approvalID, value)
	case string:
		return ApprovalResponseFromPayload(approvalID, []byte(typed))
	default:
		return ToolApprovalResponse{}, false
	}
}

func approvalResponseFromPayloadValue(approvalID string, payload ApprovalResponsePayload) ToolApprovalResponse {
	return ToolApprovalResponse{
		ID:          approvalID,
		Approved:    payload.Approved,
		Always:      payload.Always,
		Choice:      payload.Choice,
		Persisted:   payload.Persisted,
		RespondedAt: payload.RespondedAt,
		Reason:      payload.Reason,
		EditedArgs:  payload.EditedArgs,
		Metadata:    payload.Metadata,
	}
}

func NewApprovalResumeEntry(interruptID string, response ToolApprovalResponse) agui.ResumeEntry {
	return agui.ResumeEntry{
		InterruptID: interruptID,
		Status:      agui.ResumeStatusResolved,
		Payload:     ApprovalResponsePayloadFromResponse(response),
	}
}

func ApprovalToolResultFromResponse(response ToolApprovalResponse) ApprovalToolResult {
	result := ApprovalToolResult{
		ApprovalID:  response.ID,
		Always:      response.Always,
		Choice:      response.Choice,
		Persisted:   response.Persisted,
		RespondedAt: response.RespondedAt,
		EditedArgs:  response.EditedArgs,
		Metadata:    response.Metadata,
	}
	if response.Approved {
		result.Approved = true
		result.State = agui.ToolResultStateComplete
		result.Status = "success"
		return result
	}
	reason := response.Reason
	if reason == "" {
		reason = "denied"
	}
	result.State = agui.ToolResultStateError
	result.Status = "denied"
	result.Reason = reason
	return result
}

func DeniedApprovalToolResult(approvalID, reason string) ApprovalToolResult {
	if reason == "" {
		reason = "denied"
	}
	return ApprovalToolResult{
		ApprovalID: approvalID,
		Approved:   false,
		State:      agui.ToolResultStateError,
		Status:     "denied",
		Reason:     reason,
	}
}

func TimedOutApprovalResponse(approvalID string) ToolApprovalResponse {
	return ToolApprovalResponse{ID: approvalID, Approved: false, Reason: "timed_out"}
}

func TimedOutApprovalToolResult(approvalID string) ApprovalToolResult {
	result := DeniedApprovalToolResult(approvalID, "timed_out")
	result.Status = "timed_out"
	return result
}

func ParseApprovalToolResult(value any) (ApprovalToolResult, bool) {
	switch typed := value.(type) {
	case ApprovalToolResult:
		return typed, typed.ApprovalID != ""
	case *ApprovalToolResult:
		if typed == nil {
			return ApprovalToolResult{}, false
		}
		return *typed, typed.ApprovalID != ""
	case map[string]any:
		approvalID, _ := typed["approvalId"].(string)
		approved, ok := typed["approved"].(bool)
		state, _ := typed["state"].(string)
		status, _ := typed["status"].(string)
		if approvalID == "" || !ok || state == "" || status == "" {
			return ApprovalToolResult{}, false
		}
		result := ApprovalToolResult{
			ApprovalID: approvalID,
			Approved:   approved,
			State:      state,
			Status:     status,
		}
		result.Always, _ = typed["always"].(bool)
		result.Choice, _ = typed["choice"].(string)
		result.Persisted, _ = typed["persisted"].(bool)
		result.RespondedAt, _ = typed["respondedAt"].(string)
		result.Reason, _ = typed["reason"].(string)
		result.EditedArgs, _ = typed["editedArgs"].(map[string]any)
		result.Metadata, _ = typed["metadata"].(map[string]any)
		return result, true
	case []byte:
		var result ApprovalToolResult
		if err := json.Unmarshal(typed, &result); err != nil {
			return ApprovalToolResult{}, false
		}
		return result, result.ApprovalID != ""
	case string:
		return ParseApprovalToolResult([]byte(typed))
	default:
		return ApprovalToolResult{}, false
	}
}

func NewApprovalNotice(ctx ApprovalContext, choices []ApprovalChoice) ApprovalNotice {
	return ApprovalNotice{
		Schema:      "com.beeper.ai.approval.v1",
		ID:          ctx.ID,
		MessageID:   ctx.MessageID,
		ToolCallID:  ctx.ToolCallID,
		ToolName:    ctx.ToolName,
		Title:       ctx.Title,
		Description: ctx.Description,
		PlanText:    ctx.PlanText,
		ExpiresAt:   ctx.ExpiresAt,
		State:       "requested",
		Choices:     choices,
		Metadata:    ctx.Metadata,
	}
}

func (n ApprovalNotice) Map() map[string]any {
	out := map[string]any{
		"schema":     n.Schema,
		"id":         n.ID,
		"messageId":  n.MessageID,
		"toolCallId": n.ToolCallID,
		"toolName":   n.ToolName,
		"state":      n.State,
		"choices":    ApprovalChoicesAsAny(n.Choices),
	}
	if n.Title != "" {
		out["title"] = n.Title
	}
	if n.Description != "" {
		out["description"] = n.Description
	}
	if n.PlanText != "" {
		out["planText"] = n.PlanText
	}
	if n.ExpiresAt != "" {
		out["expiresAt"] = n.ExpiresAt
	}
	if len(n.Metadata) > 0 {
		out["metadata"] = n.Metadata
	}
	return out
}

func (m ApprovalInterruptMetadata) Map() map[string]any {
	out := map[string]any{
		"threadId":          m.ThreadID,
		"runId":             m.RunID,
		"messageId":         m.MessageID,
		"toolName":          m.ToolName,
		"input":             m.Input,
		"approval":          m.Approval,
		"approvalMessageId": m.ApprovalMessageID,
		"choices":           m.Choices,
	}
	if m.Title != "" {
		out["title"] = m.Title
	}
	if m.Description != "" {
		out["description"] = m.Description
	}
	if m.PlanText != "" {
		out["planText"] = m.PlanText
	}
	if m.ApprovalEventID != "" {
		out["approvalEventId"] = m.ApprovalEventID
	}
	if m.ExpiresAt != "" {
		out["expiresAt"] = m.ExpiresAt
	}
	if len(m.Metadata) > 0 {
		out["metadata"] = m.Metadata
	}
	return out
}

func ApprovalChoicesAsAny(choices []ApprovalChoice) []any {
	out := make([]any, 0, len(choices))
	for _, choice := range choices {
		item := map[string]any{
			"key":   choice.Key,
			"label": choice.Label,
		}
		if choice.Style != "" {
			item["style"] = choice.Style
		}
		if choice.Shortcut != "" {
			item["shortcut"] = choice.Shortcut
		}
		out = append(out, item)
	}
	return out
}

func SetApprovalInterruptEventID(interrupt *agui.Interrupt, eventID string) bool {
	if interrupt == nil || interrupt.ID == "" || eventID == "" {
		return false
	}
	if interrupt.Metadata == nil {
		interrupt.Metadata = map[string]any{}
	}
	interrupt.Metadata["approvalMessageId"] = interrupt.ID
	interrupt.Metadata["approvalEventId"] = eventID
	return true
}

func DefaultApprovalChoices() []ApprovalChoice {
	return []ApprovalChoice{
		{
			Key:      ApprovalChoiceApprove,
			Label:    "Allow once",
			Shortcut: "enter",
		},
		{
			Key:   ApprovalChoiceAlwaysApprove,
			Label: "Always allow",
		},
		{
			Key:   ApprovalChoiceDeny,
			Label: "Deny",
			Style: "danger",
		},
	}
}

func ResolveApprovalChoice(choices []ApprovalChoice, raw string) (ApprovalChoice, bool) {
	key := normalizeApprovalChoice(raw)
	for _, choice := range choices {
		if normalizeApprovalChoice(choice.Key) == key || (choice.Key == ApprovalChoiceAlwaysApprove && key == "always") {
			return choice, true
		}
	}
	var zero ApprovalChoice
	return zero, false
}

func ApprovalResponseForChoice(approvalID string, choice ApprovalChoice) ToolApprovalResponse {
	switch choice.Key {
	case ApprovalChoiceApprove:
		return ToolApprovalResponse{ID: approvalID, Approved: true, Choice: choice.Key}
	case ApprovalChoiceAlwaysApprove:
		return ToolApprovalResponse{ID: approvalID, Approved: true, Always: true, Choice: choice.Key}
	case ApprovalChoiceDeny:
		return ToolApprovalResponse{ID: approvalID, Approved: false, Choice: choice.Key, Reason: "denied"}
	default:
		return ToolApprovalResponse{ID: approvalID, Approved: false, Choice: choice.Key, Reason: "invalid approval choice"}
	}
}

func NewApprovalQueue(timeout ApprovalTimeout) *ApprovalQueue {
	if timeout.Reason == "" {
		timeout.Reason = "timed_out"
	}
	return &ApprovalQueue{timeout: timeout}
}

func (q *ApprovalQueue) Add(prompt ApprovalPrompt) {
	if q == nil || prompt.ID == "" {
		return
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.active == nil {
		cp := prompt
		q.active = &cp
		return
	}
	if q.active.ID == prompt.ID {
		return
	}
	for _, existing := range q.pending {
		if existing.ID == prompt.ID {
			return
		}
	}
	q.pending = append(q.pending, prompt)
}

func (q *ApprovalQueue) AddAll(prompts []ApprovalPrompt) {
	if q == nil {
		return
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	for _, prompt := range prompts {
		q.addLocked(prompt)
	}
}

func (q *ApprovalQueue) Active() (ApprovalPrompt, bool) {
	if q == nil {
		return ApprovalPrompt{}, false
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.active == nil {
		return ApprovalPrompt{}, false
	}
	return *q.active, true
}

func (q *ApprovalQueue) Pending() []ApprovalPrompt {
	if q == nil {
		return nil
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.pending) == 0 {
		return nil
	}
	return append([]ApprovalPrompt(nil), q.pending...)
}

func (q *ApprovalQueue) Timeout() ApprovalTimeout {
	if q == nil {
		return ApprovalTimeout{Reason: "timed_out"}
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	timeout := q.timeout
	if timeout.Reason == "" {
		timeout.Reason = "timed_out"
	}
	return timeout
}

func (q *ApprovalQueue) Resolve(approvalID string) (ApprovalPrompt, bool) {
	if q == nil {
		return ApprovalPrompt{}, false
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.resolveLocked(approvalID)
}

func (q *ApprovalQueue) TimeoutActive() (ApprovalPrompt, ToolApprovalResponse, bool) {
	if q == nil {
		return ApprovalPrompt{}, ToolApprovalResponse{}, false
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.active == nil {
		return ApprovalPrompt{}, ToolApprovalResponse{}, false
	}
	active := *q.active
	response := TimedOutApprovalResponse(active.ID)
	timeout := q.timeout
	if timeout.Reason == "" {
		timeout.Reason = "timed_out"
	}
	if timeout.Reason != "" {
		response.Reason = timeout.Reason
	}
	q.resolveLocked(active.ID)
	return active, response, true
}

func (q *ApprovalQueue) addLocked(prompt ApprovalPrompt) {
	if prompt.ID == "" {
		return
	}
	if q.active == nil {
		cp := prompt
		q.active = &cp
		return
	}
	if q.active.ID == prompt.ID {
		return
	}
	for _, existing := range q.pending {
		if existing.ID == prompt.ID {
			return
		}
	}
	q.pending = append(q.pending, prompt)
}

func (q *ApprovalQueue) resolveLocked(approvalID string) (ApprovalPrompt, bool) {
	if q.active == nil || q.active.ID != approvalID {
		return ApprovalPrompt{}, false
	}
	resolved := *q.active
	if len(q.pending) == 0 {
		q.active = nil
		return resolved, true
	}
	next := q.pending[0]
	q.pending = append([]ApprovalPrompt(nil), q.pending[1:]...)
	q.active = &next
	return resolved, true
}

func normalizeApprovalChoice(choice string) string {
	return strings.ToLower(strings.TrimSpace(choice))
}

func approvalSummaryState(response ToolApprovalResponse) string {
	if response.Approved {
		if response.Always {
			return "approved-always"
		}
		return "approved"
	}
	return "denied"
}
