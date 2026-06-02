package aistream

import (
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	agui "github.com/beeper/ai-bridge/pkg/ag-ui"
)

const (
	BeeperAIKey         = "com.beeper.ai"
	BeeperAISchema      = "com.beeper.ai.v1"
	BeeperAIApprovalKey = "com.beeper.ai.approval"
	DefaultModel        = "dummybridge/ag-ui"
	ErrorFallbackText   = "Something went wrong"
	PreviewBudgetBytes  = 4096
)

const (
	AIKindAnchor = "anchor"
	AIKindStream = "stream"
	AIKindFinal  = "final"
)

type Run struct {
	ThreadID   string
	RunID      string
	MessageID  string
	Model      string
	AgentID    string
	AgentName  string
	Events     []agui.Event
	Approvals  []ApprovalSummary
	Interrupts []agui.Interrupt
	Artifacts  ArtifactSummary
	Data       map[string]any
	Status     Status
	Final      FinalDelivery
	Usage      agui.Usage
	Preview    Preview
	ToolCallID string
	ApprovalID string
	Prompts    []ApprovalPrompt
}

type Status struct {
	State        string `json:"state"`
	FinishReason string `json:"finishReason,omitempty"`
	Error        any    `json:"error"`
}

type Preview struct {
	Text      string `json:"text"`
	Truncated bool   `json:"truncated"`
}

type BeeperAI struct {
	Schema    string         `json:"schema"`
	Protocol  string         `json:"protocol"`
	Kind      string         `json:"kind"`
	ThreadID  string         `json:"threadId"`
	RunID     string         `json:"runId"`
	MessageID string         `json:"messageId"`
	Agent     AgentMetadata  `json:"agent,omitempty"`
	Model     string         `json:"model,omitempty"`
	Message   *UIMessage     `json:"message,omitempty"`
	Events    []Envelope     `json:"events,omitempty"`
	Data      map[string]any `json:"data,omitempty"`
	Final     *FinalDelivery `json:"final,omitempty"`
}

type AgentMetadata struct {
	ID          string `json:"id"`
	DisplayName string `json:"displayName"`
}

type RunError struct {
	Message string `json:"message"`
	Code    string `json:"code,omitempty"`
}

type FinalDelivery struct {
	Delivery      string         `json:"delivery"`
	TextComplete  *bool          `json:"textComplete,omitempty"`
	PartsComplete bool           `json:"partsComplete"`
	PartsRef      *FinalPartsRef `json:"partsRef,omitempty"`
}

type FinalPartsRef struct {
	Schema     string `json:"schema"`
	MediaType  string `json:"mediaType"`
	URL        string `json:"url,omitempty"`
	File       any    `json:"file,omitempty"`
	ByteSize   int    `json:"byteSize"`
	SHA256     string `json:"sha256"`
	PartsCount int    `json:"partsCount"`
}

type FinalPartsPayload struct {
	Schema    string    `json:"schema"`
	ThreadID  string    `json:"threadId"`
	RunID     string    `json:"runId"`
	MessageID string    `json:"messageId"`
	Message   UIMessage `json:"message"`
}

func terminalOutcome(status Status, interrupts []agui.Interrupt) any {
	if status.State == "interrupted" {
		return agui.RunFinishedOutcome{Type: agui.OutcomeInterrupt, Interrupts: interrupts}
	}
	if status.State == "complete" {
		return agui.RunFinishedOutcome{Type: agui.OutcomeSuccess}
	}
	return nil
}

func terminalError(value any) *RunError {
	switch typed := value.(type) {
	case nil:
		return nil
	case RunError:
		return &typed
	case *RunError:
		return typed
	case map[string]any:
		message, _ := typed["message"].(string)
		code, _ := typed["code"].(string)
		if message == "" && code == "" {
			return nil
		}
		return &RunError{Message: message, Code: code}
	case string:
		if typed == "" {
			return nil
		}
		return &RunError{Message: typed}
	default:
		return &RunError{Message: fmt.Sprint(typed)}
	}
}

type ApprovalSummary struct {
	ID          string         `json:"id"`
	ToolCallID  string         `json:"toolCallId"`
	ToolName    string         `json:"toolName,omitempty"`
	Title       string         `json:"title,omitempty"`
	Description string         `json:"description,omitempty"`
	ExpiresAt   string         `json:"expiresAt,omitempty"`
	State       string         `json:"state"`
	Always      bool           `json:"always"`
	Choice      string         `json:"choice,omitempty"`
	Persisted   bool           `json:"persisted,omitempty"`
	RespondedAt string         `json:"respondedAt,omitempty"`
	Reason      string         `json:"reason,omitempty"`
	EditedArgs  map[string]any `json:"editedArgs,omitempty"`
	Metadata    map[string]any `json:"metadata,omitempty"`
}

type ApprovalPrompt struct {
	ID          string
	ToolCallID  string
	ToolName    string
	Title       string
	Description string
	PlanText    string
	ExpiresAt   string
	SeqStart    int
	Metadata    map[string]any
}

type ArtifactSummary struct {
	Sources   []map[string]any `json:"sources"`
	Documents []map[string]any `json:"documents"`
	Files     []map[string]any `json:"files"`
}

type Writer struct {
	Run                       *Run
	builder                   agui.EventBuilder
	textMessages              map[int]string
	textOpen                  map[int]bool
	textContentWritten        bool
	textIndexOffset           int
	currentAssistantMessageID string
	reasoningMessages         map[int]string
	reasoningOpen             map[int]bool
	reasoningContent          map[int]string
	reasoningIndexOffset      int
	reasoningPhaseID          string
	reasoningPhaseOpen        bool
	nextSyntheticReasoningIdx int
	lastAccountedChars        int
	previewText               string
}

func NewRun(runID, threadID, model, agentID, agentName string, now time.Time) *Run {
	runID = strings.TrimSpace(runID)
	if runID == "" {
		runID = fmt.Sprintf("run-%d", now.UnixNano())
	}
	threadID = strings.TrimSpace(threadID)
	if threadID == "" {
		threadID = runID
	}
	model = strings.TrimSpace(model)
	if model == "" {
		model = DefaultModel
	}
	if agentID == "" {
		agentID = "ai"
	}
	if agentName == "" {
		agentName = "AI"
	}
	run := &Run{
		ThreadID:  threadID,
		RunID:     runID,
		MessageID: "msg-" + runID,
		Model:     model,
		AgentID:   agentID,
		AgentName: agentName,
		Data:      map[string]any{},
		Status:    Status{State: "streaming"},
	}
	run.Preview = Preview{Text: BoundedPreview("", PreviewBudgetBytes)}
	return run
}

func NewWriter(run *Run, now func() time.Time) *Writer {
	textIndexOffset := nextMessageOrdinal(run, messageOrdinalText)
	reasoningIndexOffset := nextMessageOrdinal(run, messageOrdinalReasoning)
	writer := &Writer{
		Run:                  run,
		builder:              agui.NewEventBuilder(run.Model, now),
		textMessages:         map[int]string{},
		textOpen:             map[int]bool{},
		textIndexOffset:      textIndexOffset,
		reasoningMessages:    map[int]string{},
		reasoningOpen:        map[int]bool{},
		reasoningContent:     map[int]string{},
		reasoningIndexOffset: reasoningIndexOffset,
		reasoningPhaseID:     "reasoning-" + run.RunID,
		lastAccountedChars:   utf8.RuneCountInString(run.Text()),
		previewText:          run.Text(),
	}
	writer.currentAssistantMessageID = writer.textMessageID(0)
	return writer
}

func (w *Writer) Add(evt agui.Event) {
	if w == nil || w.Run == nil || evt.Len() == 0 {
		return
	}
	w.Run.Events = append(w.Run.Events, evt)
	w.applySummary(evt)
}

func (w *Writer) Start() {
	w.Add(w.builder.RunStarted(w.Run.ThreadID, w.Run.RunID))
}

func (w *Writer) Text(delta string) {
	w.TextDelta(0, delta)
}

func (w *Writer) TextStart(index int) string {
	return w.ensureTextMessage(index)
}

func (w *Writer) TextDelta(index int, delta string) {
	if delta == "" {
		return
	}
	messageID := w.ensureTextMessage(index)
	w.textContentWritten = true
	w.Add(w.builder.TextMessageContent(messageID, delta))
}

func (w *Writer) HasTextContent() bool {
	return w != nil && w.textContentWritten
}

func (w *Writer) TextEnd(index int) {
	w.initState()
	messageID := w.textMessages[index]
	if messageID == "" || !w.textOpen[index] {
		return
	}
	w.Add(w.builder.TextMessageEnd(messageID))
	w.textOpen[index] = false
}

func (w *Writer) Thinking(delta string) {
	if delta == "" {
		return
	}
	index := w.nextSyntheticReasoningIdx
	w.nextSyntheticReasoningIdx++
	w.ReasoningDelta(index, delta)
	w.ReasoningMessageEnd(index)
}

func (w *Writer) ReasoningMessageStart(index int) string {
	w.ensureCurrentAssistantMessage()
	w.ensureReasoningPhase()
	return w.ensureReasoningMessage(index)
}

func (w *Writer) ReasoningDelta(index int, delta string) {
	if delta == "" {
		return
	}
	messageID := w.ReasoningMessageStart(index)
	w.reasoningContent[index] += delta
	w.Add(w.builder.ReasoningMessageContent(messageID, delta))
}

func (w *Writer) ReasoningContentSnapshot(index int, content string) {
	if content == "" {
		return
	}
	w.initState()
	previous := w.reasoningContent[index]
	if content == previous {
		return
	}
	if previous == "" {
		w.ReasoningDelta(index, content)
		return
	}
	if strings.HasPrefix(content, previous) {
		w.ReasoningDelta(index, content[len(previous):])
	}
}

func (w *Writer) ReasoningMessageEnd(index int) {
	w.initState()
	messageID := w.reasoningMessages[index]
	if messageID == "" || !w.reasoningOpen[index] {
		return
	}
	w.Add(w.builder.ReasoningMessageEnd(messageID))
	w.reasoningOpen[index] = false
}

func (w *Writer) StepStart(stepID string) {
	messageID := w.ensureCurrentAssistantMessage()
	w.Add(w.builder.StepStarted(messageID, stepID))
}

func (w *Writer) StepFinish(stepID string) {
	messageID := w.ensureCurrentAssistantMessage()
	w.Add(w.builder.StepFinished(messageID, stepID))
}

func (w *Writer) ToolStart(toolCallID, name string, index int, approval *ToolApproval) {
	w.ToolStartWithMetadata(toolCallID, name, index, approval, nil)
}

func (w *Writer) ToolStartWithMetadata(toolCallID, name string, index int, approval *ToolApproval, metadata map[string]any) {
	idx := index
	w.Add(w.builder.ToolCallStartWithMetadata(w.currentAssistantMessage(), toolCallID, name, &idx, metadata))
	if approval != nil {
		w.recordApprovalRequest(toolCallID, name, approval)
	}
}

func (w *Writer) ToolApprovalRequested(toolCallID, name string, input any, approval ToolApproval) {
	w.ToolApprovalRequestedWithMetadata(toolCallID, name, input, approval, nil)
}

func (w *Writer) ToolApprovalRequestedWithMetadata(toolCallID, name string, input any, approval ToolApproval, metadata map[string]any) {
	w.recordApprovalRequest(toolCallID, name, &approval)
	w.recordInterrupt(NewApprovalInterrupt(*w.Run, toolCallID, name, input, approval, metadata))
}

func (w *Writer) ToolApprovalRequestedWithRequest(request ApprovalRequest) {
	approval := request.Approval
	if approval.ID == "" {
		approval.ID = request.ID
	}
	approval.NeedsApproval = true
	request.Approval = approval
	w.recordApprovalRequestFromRequest(request)
	w.recordInterrupt(NewApprovalInterruptFromRequest(*w.Run, request))
}

func (w *Writer) recordApprovalRequest(toolCallID, name string, approval *ToolApproval) {
	if approval == nil || approval.ID == "" {
		return
	}
	w.Run.ToolCallID = toolCallID
	w.Run.ApprovalID = approval.ID
	for _, existing := range w.Run.Approvals {
		if existing.ID == approval.ID {
			return
		}
	}
	w.Run.Approvals = append(w.Run.Approvals, ApprovalSummary{
		ID:         approval.ID,
		ToolCallID: toolCallID,
		ToolName:   name,
		State:      "requested",
	})
	w.Run.Prompts = append(w.Run.Prompts, ApprovalPrompt{ID: approval.ID, ToolCallID: toolCallID, ToolName: name})
}

func (w *Writer) recordApprovalRequestFromRequest(request ApprovalRequest) {
	approval := request.Approval
	if approval.ID == "" {
		approval.ID = request.ID
	}
	if approval.ID == "" {
		return
	}
	w.Run.ToolCallID = request.ToolCallID
	w.Run.ApprovalID = approval.ID
	expiresAt := ""
	if !request.ExpiresAt.IsZero() {
		expiresAt = request.ExpiresAt.UTC().Format(time.RFC3339)
	}
	for _, existing := range w.Run.Approvals {
		if existing.ID == approval.ID {
			return
		}
	}
	w.Run.Approvals = append(w.Run.Approvals, ApprovalSummary{
		ID:          approval.ID,
		ToolCallID:  request.ToolCallID,
		ToolName:    request.ToolName,
		Title:       request.Title,
		Description: request.Description,
		ExpiresAt:   expiresAt,
		State:       "requested",
		Metadata:    request.Metadata,
	})
	w.Run.Prompts = append(w.Run.Prompts, ApprovalPrompt{
		ID:          approval.ID,
		ToolCallID:  request.ToolCallID,
		ToolName:    request.ToolName,
		Title:       request.Title,
		Description: request.Description,
		PlanText:    request.PlanText,
		ExpiresAt:   expiresAt,
		Metadata:    request.Metadata,
	})
}

func (w *Writer) ToolArgs(toolCallID, delta string, args any) {
	if delta == "" {
		return
	}
	w.Add(w.builder.ToolCallArgs(toolCallID, delta, args))
}

func (w *Writer) ToolEnd(toolCallID, name string, input, result any) {
	if result == nil {
		result = map[string]any{
			"state":  agui.ToolResultStateComplete,
			"status": "success",
		}
	}
	w.Add(w.builder.ToolCallEnd(toolCallID, name, input, agui.ToolStateInputComplete))
	w.ToolResult(toolCallID, asString(jsonString(result)), toolResultState(result))
}

func (w *Writer) ToolApprovalInputComplete(toolCallID, name string, input any) {
	w.Add(w.builder.ToolCallEnd(toolCallID, name, input, agui.ToolStateInputComplete))
}

func (w *Writer) ToolInputComplete(toolCallID, name string, input any) {
	w.Add(w.builder.ToolCallEnd(toolCallID, name, input, agui.ToolStateInputComplete))
}

func (w *Writer) ToolApprovalResponded(toolCallID, name string, input any, response ToolApprovalResponse) {
	for i := range w.Run.Approvals {
		if w.Run.Approvals[i].ID == response.ID {
			w.Run.Approvals[i].State = approvalSummaryState(response)
			w.Run.Approvals[i].Always = response.Always
			w.Run.Approvals[i].Choice = response.Choice
			w.Run.Approvals[i].Persisted = response.Persisted
			w.Run.Approvals[i].RespondedAt = response.RespondedAt
			w.Run.Approvals[i].Reason = response.Reason
			w.Run.Approvals[i].EditedArgs = response.EditedArgs
			if response.Metadata != nil {
				w.Run.Approvals[i].Metadata = response.Metadata
			}
		}
	}
	result := ApprovalToolResultFromResponse(response)
	state := agui.ToolResultStateComplete
	if !response.Approved {
		state = agui.ToolResultStateError
	}
	w.resolveInterrupt(response.ID, toolCallID)
	w.Run.Status = Status{State: "streaming"}
	w.ToolResult(toolCallID, asString(jsonString(result)), state)
}

func (w *Writer) ToolResult(toolCallID, content, state string) {
	w.Add(w.builder.ToolCallResult(w.toolResultMessageID(toolCallID), toolCallID, content, state, agui.RoleTool))
}

func (w *Writer) ToolError(toolCallID, name string, input any, reason string) {
	w.Add(w.builder.ToolCallEnd(toolCallID, name, input, agui.ToolStateInputComplete))
	w.ToolResult(toolCallID, asString(jsonString(map[string]any{
		"state":  agui.ToolResultStateError,
		"status": "failed",
		"reason": reason,
	})), agui.ToolResultStateError)
}

func (w *Writer) ToolDenied(toolCallID, name string, input any, approvalID, reason string) {
	if reason == "" {
		reason = "denied"
	}
	for i := range w.Run.Approvals {
		if w.Run.Approvals[i].ID == approvalID {
			w.Run.Approvals[i].State = "denied"
			w.Run.Approvals[i].Reason = reason
		}
	}
	w.resolveInterrupt(approvalID, toolCallID)
	w.Run.Status = Status{State: "streaming"}
	w.Add(w.builder.ToolCallEnd(toolCallID, name, input, agui.ToolStateInputComplete))
	w.ToolResult(toolCallID, asString(jsonString(DeniedApprovalToolResult(approvalID, reason))), agui.ToolResultStateError)
}

func (w *Writer) MessagesSnapshot(messages []agui.Message) {
	w.Add(w.builder.MessagesSnapshot(messages))
}

func (w *Writer) Custom(name string, value any) {
	w.Add(w.builder.Custom(name, value))
}

func (w *Writer) ApprovalEventLinked(approvalID, eventID string) {
	if approvalID == "" || eventID == "" {
		return
	}
	for i := range w.Run.Interrupts {
		if w.Run.Interrupts[i].ID == approvalID {
			SetApprovalInterruptEventID(&w.Run.Interrupts[i], eventID)
		}
	}
	w.Custom(BeeperAIApprovalKey, map[string]any{
		"schema":          "com.beeper.ai.approval.v1",
		"id":              approvalID,
		"state":           "event-linked",
		"approvalEventId": eventID,
	})
}

func (w *Writer) StateDelta(delta any) {
	w.Add(w.builder.StateDelta(delta))
}

func (w *Writer) Finish(reason string) {
	w.FinishWithUsage(reason, nil)
}

func (w *Writer) FinishWithUsage(reason string, usage *agui.Usage) {
	reason = agui.NormalizeFinishReason(reason)
	w.finishReasoning()
	w.finishText()
	w.addUsage(usage)
	w.Run.Status = Status{State: "complete", FinishReason: reason}
	w.addFinalSnapshot()
	w.Add(w.builder.RunFinished(w.Run.ThreadID, w.Run.RunID, reason, w.Run.Usage))
}

func (w *Writer) AwaitToolUseWithUsage(usage *agui.Usage) {
	w.finishReasoning()
	w.finishText()
	w.addUsage(usage)
	w.Run.Status = Status{State: "streaming"}
}

func (w *Writer) Interrupt() {
	w.InterruptWithUsage(nil)
}

func (w *Writer) InterruptWithUsage(usage *agui.Usage) {
	if len(w.Run.Interrupts) == 0 {
		w.FinishWithUsage(agui.FinishReasonStop, usage)
		return
	}
	w.finishReasoning()
	w.finishText()
	w.addUsage(usage)
	w.Run.Status = Status{State: "interrupted", FinishReason: agui.FinishReasonToolCalls}
	w.addFinalSnapshot()
	w.Add(w.builder.RunFinishedWithOutcome(
		w.Run.ThreadID,
		w.Run.RunID,
		agui.FinishReasonToolCalls,
		w.Run.Usage,
		agui.RunFinishedOutcome{Type: agui.OutcomeInterrupt, Interrupts: append([]agui.Interrupt(nil), w.Run.Interrupts...)},
	))
}

func (w *Writer) addUsage(usage *agui.Usage) {
	if usage == nil {
		text := w.Run.Text()
		currentChars := utf8.RuneCountInString(text)
		completionChars := currentChars - w.lastAccountedChars
		if completionChars < 0 {
			completionChars = 0
		}
		promptTokens := 0
		if w.Run.Usage.PromptTokens == 0 {
			promptTokens = 1
		}
		usage = &agui.Usage{
			PromptTokens:     promptTokens,
			CompletionTokens: completionChars,
			TotalTokens:      completionChars + promptTokens,
		}
	}
	w.Run.Usage.PromptTokens += usage.PromptTokens
	w.Run.Usage.CompletionTokens += usage.CompletionTokens
	w.Run.Usage.ReasoningTokens += usage.ReasoningTokens
	w.Run.Usage.TotalTokens += usage.TotalTokens
	if usage.ContextLimit > w.Run.Usage.ContextLimit {
		w.Run.Usage.ContextLimit = usage.ContextLimit
	}
	w.lastAccountedChars = utf8.RuneCountInString(w.Run.Text())
}

func (w *Writer) Error(message string) {
	w.finishReasoning()
	w.finishText()
	w.Run.Status = Status{State: "error", Error: map[string]any{"message": message}}
	if w.Run.Text() == "" {
		w.Run.Preview = PreviewFromText(ErrorFallbackPlaintext(message), PreviewBudgetBytes)
		w.previewText = w.Run.Preview.Text
	}
	w.addFinalSnapshot()
	w.Add(w.builder.RunError(w.Run.ThreadID, w.Run.RunID, message))
}

func (w *Writer) Abort(message string) {
	w.finishReasoning()
	w.finishText()
	w.Run.Status = Status{State: "aborted", Error: map[string]any{"message": message}}
	w.addFinalSnapshot()
	w.Add(w.builder.RunErrorWithCode(w.Run.ThreadID, w.Run.RunID, message, agui.FinishReasonCancelled))
}

func (w *Writer) addFinalSnapshot() {
	if w == nil || w.Run == nil {
		return
	}
	messages := w.Run.Messages(true)
	w.MessagesSnapshot(messages)
}

func (w *Writer) finishReasoning() {
	w.initState()
	if len(w.reasoningOpen) == 0 && !w.reasoningPhaseOpen {
		return
	}
	for _, index := range sortedOpenIndexes(w.reasoningOpen) {
		w.ReasoningMessageEnd(index)
	}
	if w.reasoningPhaseOpen {
		w.Add(w.builder.ReasoningEnd(w.reasoningPhaseID))
		w.reasoningPhaseOpen = false
	}
}

func (w *Writer) finishText() {
	w.initState()
	for _, index := range sortedOpenIndexes(w.textOpen) {
		w.TextEnd(index)
	}
}

func (w *Writer) ensureTextMessage(index int) string {
	w.initState()
	if messageID := w.textMessages[index]; messageID != "" {
		w.currentAssistantMessageID = messageID
		if !w.textOpen[index] {
			w.Add(w.builder.TextMessageStart(messageID, agui.RoleAssistant))
			w.textOpen[index] = true
		}
		return messageID
	}
	messageID := w.textMessageID(index)
	w.textMessages[index] = messageID
	w.currentAssistantMessageID = messageID
	w.Add(w.builder.TextMessageStart(messageID, agui.RoleAssistant))
	w.textOpen[index] = true
	return messageID
}

func (w *Writer) currentAssistantMessage() string {
	w.initState()
	if w.currentAssistantMessageID == "" {
		w.currentAssistantMessageID = w.textMessageID(0)
	}
	return w.currentAssistantMessageID
}

func (w *Writer) ensureCurrentAssistantMessage() string {
	messageID := w.currentAssistantMessage()
	for index, existingID := range w.textMessages {
		if existingID != messageID {
			continue
		}
		if !w.textOpen[index] {
			w.Add(w.builder.TextMessageStart(messageID, agui.RoleAssistant))
			w.textOpen[index] = true
		}
		return messageID
	}
	index := w.textIndexForMessageID(messageID)
	w.textMessages[index] = messageID
	w.Add(w.builder.TextMessageStart(messageID, agui.RoleAssistant))
	w.textOpen[index] = true
	return messageID
}

func (w *Writer) ensureReasoningPhase() {
	w.initState()
	if w.reasoningPhaseID == "" {
		w.reasoningPhaseID = "reasoning-" + w.Run.RunID
	}
	if !w.reasoningPhaseOpen {
		w.Add(w.builder.ReasoningStart(w.reasoningPhaseID))
		w.reasoningPhaseOpen = true
	}
}

func (w *Writer) ensureReasoningMessage(index int) string {
	w.initState()
	if messageID := w.reasoningMessages[index]; messageID != "" {
		if !w.reasoningOpen[index] {
			w.Add(w.builder.ReasoningMessageStart(messageID))
			w.reasoningOpen[index] = true
		}
		return messageID
	}
	messageID := w.reasoningMessageID(index)
	w.reasoningMessages[index] = messageID
	w.Add(w.builder.ReasoningMessageStart(messageID))
	w.reasoningOpen[index] = true
	return messageID
}

func (w *Writer) textMessageID(index int) string {
	ordinal := w.textIndexOffset + max(index, 0)
	if ordinal == 0 {
		return w.Run.MessageID
	}
	return fmt.Sprintf("%s-text-%d", w.Run.MessageID, ordinal)
}

func (w *Writer) reasoningMessageID(index int) string {
	if index < 0 {
		index = w.nextSyntheticReasoningIdx
		w.nextSyntheticReasoningIdx++
	}
	return fmt.Sprintf("%s-reasoning-%d", w.Run.MessageID, w.reasoningIndexOffset+max(index, 0))
}

func (w *Writer) textIndexForMessageID(messageID string) int {
	ordinal, ok := messageOrdinal(w.Run.MessageID, messageID, messageOrdinalText)
	if !ok {
		return 0
	}
	index := ordinal - w.textIndexOffset
	if index < 0 {
		return 0
	}
	return index
}

func (w *Writer) toolResultMessageID(toolCallID string) string {
	toolCallID = strings.TrimSpace(toolCallID)
	if toolCallID == "" {
		toolCallID = "result"
	}
	return w.Run.MessageID + "-tool-" + toolCallID
}

func (w *Writer) recordInterrupt(interrupt agui.Interrupt) {
	if interrupt.ID == "" {
		return
	}
	for i := range w.Run.Interrupts {
		if w.Run.Interrupts[i].ID == interrupt.ID {
			w.Run.Interrupts[i] = interrupt
			return
		}
	}
	w.Run.Interrupts = append(w.Run.Interrupts, interrupt)
}

func (w *Writer) resolveInterrupt(interruptID, toolCallID string) {
	if w == nil || w.Run == nil || len(w.Run.Interrupts) == 0 {
		return
	}
	filtered := w.Run.Interrupts[:0]
	for _, interrupt := range w.Run.Interrupts {
		if interrupt.ID == interruptID || (toolCallID != "" && interrupt.ToolCallID == toolCallID) {
			continue
		}
		filtered = append(filtered, interrupt)
	}
	w.Run.Interrupts = filtered
}

func (w *Writer) initState() {
	if w.textMessages == nil {
		w.textMessages = map[int]string{}
	}
	if w.textOpen == nil {
		w.textOpen = map[int]bool{}
	}
	if w.reasoningMessages == nil {
		w.reasoningMessages = map[int]string{}
	}
	if w.reasoningOpen == nil {
		w.reasoningOpen = map[int]bool{}
	}
	if w.reasoningContent == nil {
		w.reasoningContent = map[int]string{}
	}
	if w.reasoningPhaseID == "" && w.Run != nil {
		w.reasoningPhaseID = "reasoning-" + w.Run.RunID
	}
}

func sortedOpenIndexes(open map[int]bool) []int {
	indexes := make(map[int]struct{}, len(open))
	for index, isOpen := range open {
		if isOpen {
			indexes[index] = struct{}{}
		}
	}
	return slices.Sorted(maps.Keys(indexes))
}

type messageOrdinalKind int

const (
	messageOrdinalText messageOrdinalKind = iota
	messageOrdinalReasoning
)

func nextMessageOrdinal(run *Run, kind messageOrdinalKind) int {
	if run == nil {
		return 0
	}
	maxOrdinal := -1
	for _, evt := range run.Events {
		if !messageOrdinalEvent(evt.Type(), kind) {
			continue
		}
		messageID, _ := evt.Get("messageId").(string)
		if messageID == "" {
			continue
		}
		ordinal, ok := messageOrdinal(run.MessageID, messageID, kind)
		if ok && ordinal > maxOrdinal {
			maxOrdinal = ordinal
		}
	}
	if maxOrdinal < 0 {
		return 0
	}
	return maxOrdinal + 1
}

func messageOrdinalEvent(eventType string, kind messageOrdinalKind) bool {
	switch kind {
	case messageOrdinalText:
		switch eventType {
		case agui.EventTextMessageStart, agui.EventTextMessageContent, agui.EventTextMessageChunk, agui.EventTextMessageEnd:
			return true
		default:
			return false
		}
	case messageOrdinalReasoning:
		switch eventType {
		case agui.EventReasoningMsgStart, agui.EventReasoningMsgCont, agui.EventReasoningMsgChunk, agui.EventReasoningMsgEnd:
			return true
		default:
			return false
		}
	default:
		return false
	}
}

func messageOrdinal(baseID, messageID string, kind messageOrdinalKind) (int, bool) {
	switch kind {
	case messageOrdinalText:
		if messageID == baseID {
			return 0, true
		}
		prefix := baseID + "-text-"
		if !strings.HasPrefix(messageID, prefix) {
			return 0, false
		}
		ordinal, err := strconv.Atoi(strings.TrimPrefix(messageID, prefix))
		return ordinal, err == nil && ordinal > 0
	case messageOrdinalReasoning:
		prefix := baseID + "-reasoning-"
		if !strings.HasPrefix(messageID, prefix) {
			return 0, false
		}
		ordinal, err := strconv.Atoi(strings.TrimPrefix(messageID, prefix))
		return ordinal, err == nil && ordinal >= 0
	default:
		return 0, false
	}
}

func toolResultState(result any) string {
	value := result
	if raw, ok := result.(string); ok {
		value = jsonValue(raw)
	}
	if resultMap, ok := value.(map[string]any); ok {
		if state, _ := resultMap["state"].(string); state == agui.ToolResultStateError {
			return agui.ToolResultStateError
		}
		if status, _ := resultMap["status"].(string); status == "failed" || status == "denied" {
			return agui.ToolResultStateError
		}
	}
	return agui.ToolResultStateComplete
}

func (w *Writer) applySummary(evt agui.Event) {
	switch evt.Type() {
	case agui.EventTextMessageContent, agui.EventTextMessageChunk:
		if delta, _ := evt.Get("delta").(string); delta != "" {
			w.appendPreviewText(delta)
		}
	case agui.EventCustom:
		name, _ := evt.Get("name").(string)
		value, _ := evt.Get("value").(map[string]any)
		switch name {
		case "com.beeper.source":
			w.Run.Artifacts.Sources = upsertArtifact(w.Run.Artifacts.Sources, value)
		case "com.beeper.document":
			w.Run.Artifacts.Documents = append(w.Run.Artifacts.Documents, value)
		case "com.beeper.file":
			w.Run.Artifacts.Files = append(w.Run.Artifacts.Files, value)
		case "com.beeper.data":
			if key, _ := value["name"].(string); key != "" {
				w.Run.Data[key] = value["value"]
			}
		}
	}
}

func upsertArtifact(items []map[string]any, value map[string]any) []map[string]any {
	id := firstArtifactString(value["sourceId"], value["id"], value["url"])
	if id == "" {
		return append(items, value)
	}
	for index, item := range items {
		if firstArtifactString(item["sourceId"], item["id"], item["url"]) == id {
			items[index] = value
			return items
		}
	}
	return append(items, value)
}

func firstArtifactString(values ...any) string {
	for _, value := range values {
		if text, ok := value.(string); ok && text != "" {
			return text
		}
	}
	return ""
}

func (w *Writer) appendPreviewText(delta string) {
	if w == nil || w.Run == nil || delta == "" || w.Run.Preview.Truncated {
		return
	}
	w.previewText += delta
	w.Run.Preview = PreviewFromText(w.previewText, PreviewBudgetBytes)
	if w.Run.Preview.Truncated {
		w.previewText = w.Run.Preview.Text
	}
}
