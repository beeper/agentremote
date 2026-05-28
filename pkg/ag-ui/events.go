package agui

import (
	"encoding/json"
	"strings"
	"time"
)

const (
	EventRunStarted         = "RUN_STARTED"
	EventRunFinished        = "RUN_FINISHED"
	EventRunError           = "RUN_ERROR"
	EventTextMessageStart   = "TEXT_MESSAGE_START"
	EventTextMessageContent = "TEXT_MESSAGE_CONTENT"
	EventTextMessageEnd     = "TEXT_MESSAGE_END"
	EventTextMessageChunk   = "TEXT_MESSAGE_CHUNK"
	EventToolCallStart      = "TOOL_CALL_START"
	EventToolCallArgs       = "TOOL_CALL_ARGS"
	EventToolCallEnd        = "TOOL_CALL_END"
	EventToolCallChunk      = "TOOL_CALL_CHUNK"
	EventToolCallResult     = "TOOL_CALL_RESULT"
	EventStepStarted        = "STEP_STARTED"
	EventStepFinished       = "STEP_FINISHED"
	EventStateSnapshot      = "STATE_SNAPSHOT"
	EventStateDelta         = "STATE_DELTA"
	EventMessagesSnapshot   = "MESSAGES_SNAPSHOT"
	EventActivitySnapshot   = "ACTIVITY_SNAPSHOT"
	EventActivityDelta      = "ACTIVITY_DELTA"
	EventRaw                = "RAW"
	EventCustom             = "CUSTOM"
	EventReasoningStart     = "REASONING_START"
	EventReasoningEnd       = "REASONING_END"
	EventReasoningMsgStart  = "REASONING_MESSAGE_START"
	EventReasoningMsgCont   = "REASONING_MESSAGE_CONTENT"
	EventReasoningMsgEnd    = "REASONING_MESSAGE_END"
	EventReasoningMsgChunk  = "REASONING_MESSAGE_CHUNK"
	EventReasoningEncrypted = "REASONING_ENCRYPTED_VALUE"
)

const (
	RoleAssistant = "assistant"
	RoleUser      = "user"
	RoleSystem    = "system"
	RoleTool      = "tool"
)

const (
	ToolStateAwaitingInput    = "awaiting-input"
	ToolStateInputStreaming   = "input-streaming"
	ToolStateInputComplete    = "input-complete"
	ToolResultStateStreaming  = "streaming"
	ToolResultStateComplete   = "complete"
	ToolResultStateError      = "error"
	PartStateStreaming        = "streaming"
	PartStateDone             = "done"
	FinishReasonStop          = "stop"
	FinishReasonLength        = "length"
	FinishReasonContentFilter = "content_filter"
	FinishReasonToolCalls     = "tool_calls"
	FinishReasonOther         = "other"
	OutcomeSuccess            = "success"
	OutcomeInterrupt          = "interrupt"
	InterruptReasonToolCall   = "tool_call"
	InterruptReasonInput      = "input_required"
	InterruptReasonConfirm    = "confirmation"
	ResumeStatusResolved      = "resolved"
	ResumeStatusCancelled     = "cancelled"
)

type Event struct {
	fields map[string]any
}

func NewEvent(fields map[string]any) Event {
	evt := Event{fields: map[string]any{}}
	for key, value := range fields {
		evt.fields[key] = value
	}
	return evt
}

func (e Event) Type() string {
	return e.String("type")
}

func (e Event) String(key string) string {
	value, _ := e.Get(key).(string)
	return value
}

func (e Event) Get(key string) any {
	if e.fields == nil {
		return nil
	}
	return e.fields[key]
}

func (e Event) Has(key string) bool {
	if e.fields == nil {
		return false
	}
	_, ok := e.fields[key]
	return ok
}

func (e Event) Len() int {
	return len(e.fields)
}

func (e *Event) Set(key string, value any) {
	if e.fields == nil {
		e.fields = map[string]any{}
	}
	e.fields[key] = value
}

func (e *Event) Delete(key string) {
	delete(e.fields, key)
}

func (e Event) Map() map[string]any {
	out := make(map[string]any, len(e.fields))
	for key, value := range e.fields {
		out[key] = value
	}
	return out
}

func (e Event) MarshalJSON() ([]byte, error) {
	return json.Marshal(e.fields)
}

func (e *Event) UnmarshalJSON(data []byte) error {
	var fields map[string]any
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	e.fields = fields
	return nil
}

type JSONSchema map[string]any

type Message struct {
	ID             string            `json:"id"`
	Role           string            `json:"role"`
	Content        any               `json:"content,omitempty"`
	Name           string            `json:"name,omitempty"`
	ToolCalls      []MessageToolCall `json:"toolCalls,omitempty"`
	ToolCallID     string            `json:"toolCallId,omitempty"`
	Error          string            `json:"error,omitempty"`
	ActivityType   string            `json:"activityType,omitempty"`
	EncryptedValue string            `json:"encryptedValue,omitempty"`
	Metadata       map[string]any    `json:"metadata,omitempty"`
}

type MessageToolCall struct {
	ID             string           `json:"id"`
	Type           string           `json:"type"`
	Function       ToolCallFunction `json:"function"`
	EncryptedValue string           `json:"encryptedValue,omitempty"`
}

type ToolCallFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type RunFinishedOutcome struct {
	Type       string      `json:"type"`
	Interrupts []Interrupt `json:"interrupts,omitempty"`
}

type Interrupt struct {
	ID             string         `json:"id"`
	Reason         string         `json:"reason"`
	Message        string         `json:"message,omitempty"`
	ToolCallID     string         `json:"toolCallId,omitempty"`
	ResponseSchema JSONSchema     `json:"responseSchema,omitempty"`
	ExpiresAt      string         `json:"expiresAt,omitempty"`
	Metadata       map[string]any `json:"metadata,omitempty"`
}

type ResumeEntry struct {
	InterruptID string `json:"interruptId"`
	Status      string `json:"status"`
	Payload     any    `json:"payload,omitempty"`
}

type RunAgentInput struct {
	ThreadID       string         `json:"threadId,omitempty"`
	RunID          string         `json:"runId,omitempty"`
	State          map[string]any `json:"state,omitempty"`
	Messages       []Message      `json:"messages,omitempty"`
	Resume         []ResumeEntry  `json:"resume,omitempty"`
	Tools          []Tool         `json:"tools,omitempty"`
	Context        []ContextItem  `json:"context,omitempty"`
	ForwardedProps map[string]any `json:"forwardedProps,omitempty"`
	Data           map[string]any `json:"data,omitempty"`
}

type Tool struct {
	Name          string     `json:"name"`
	Description   string     `json:"description,omitempty"`
	InputSchema   JSONSchema `json:"inputSchema,omitempty"`
	OutputSchema  JSONSchema `json:"outputSchema,omitempty"`
	NeedsApproval bool       `json:"needsApproval,omitempty"`
}

type ContextItem struct {
	Type  string         `json:"type"`
	Value any            `json:"value,omitempty"`
	Meta  map[string]any `json:"meta,omitempty"`
}

type Usage struct {
	PromptTokens     int `json:"promptTokens,omitempty"`
	CompletionTokens int `json:"completionTokens,omitempty"`
	ReasoningTokens  int `json:"reasoningTokens,omitempty"`
	TotalTokens      int `json:"totalTokens,omitempty"`
}

type EventBuilder struct {
	now   func() time.Time
	model string
}

func NewEventBuilder(model string, now func() time.Time) EventBuilder {
	if now == nil {
		now = time.Now
	}
	return EventBuilder{now: now, model: strings.TrimSpace(model)}
}

func (b EventBuilder) base(eventType string) Event {
	return mustEvent(b.envelope(eventType))
}

func (b EventBuilder) envelope(eventType string) EventEnvelope {
	return EventEnvelope{Type: eventType, Timestamp: b.now().UnixMilli(), Model: b.model}
}

func (b EventBuilder) RunStarted(threadID, runID string) Event {
	return mustEvent(RunStartedEvent{
		EventEnvelope: b.envelope(EventRunStarted),
		ThreadID:      threadID,
		RunID:         runID,
	})
}

func (b EventBuilder) RunFinished(threadID, runID, finishReason string, usage Usage) Event {
	return b.RunFinishedWithOutcome(threadID, runID, finishReason, usage, RunFinishedOutcome{Type: OutcomeSuccess})
}

func (b EventBuilder) RunFinishedWithOutcome(threadID, runID, finishReason string, usage Usage, outcome RunFinishedOutcome) Event {
	if finishReason != "" {
		finishReason = NormalizeFinishReason(finishReason)
	}
	evt := mustEvent(RunFinishedEvent{
		EventEnvelope: b.envelope(EventRunFinished),
		ThreadID:      threadID,
		RunID:         runID,
		FinishReason:  finishReason,
		Usage:         usage,
		Outcome:       outcome,
	})
	return evt
}

func (b EventBuilder) RunError(threadID, runID, message string) Event {
	if strings.TrimSpace(runID) == "" {
		runID = ""
	}
	return mustEvent(RunErrorEvent{
		EventEnvelope: b.envelope(EventRunError),
		ThreadID:      threadID,
		RunID:         runID,
		Message:       message,
		Error:         RunError{Message: message},
	})
}

func (b EventBuilder) TextMessageStart(messageID, role string) Event {
	if role == "" {
		role = RoleAssistant
	}
	return mustEvent(TextMessageStartEvent{EventEnvelope: b.envelope(EventTextMessageStart), MessageID: messageID, Role: role})
}

func (b EventBuilder) TextMessageContent(messageID, delta string) Event {
	return mustEvent(TextMessageContentEvent{EventEnvelope: b.envelope(EventTextMessageContent), MessageID: messageID, Delta: delta})
}

func (b EventBuilder) TextMessageEnd(messageID string) Event {
	return mustEvent(TextMessageEndEvent{EventEnvelope: b.envelope(EventTextMessageEnd), MessageID: messageID})
}

func (b EventBuilder) TextMessageChunk(messageID, role, delta string) Event {
	return mustEvent(TextMessageChunkEvent{EventEnvelope: b.envelope(EventTextMessageChunk), MessageID: messageID, Role: role, Delta: delta})
}

func (b EventBuilder) ReasoningStart(messageID string) Event {
	return mustEvent(ReasoningMessageStartEvent{EventEnvelope: b.envelope(EventReasoningStart), MessageID: messageID})
}

func (b EventBuilder) ReasoningEnd(messageID string) Event {
	return mustEvent(ReasoningMessageEndEvent{EventEnvelope: b.envelope(EventReasoningEnd), MessageID: messageID})
}

func (b EventBuilder) ReasoningMessageStart(messageID string) Event {
	return mustEvent(ReasoningMessageStartEvent{EventEnvelope: b.envelope(EventReasoningMsgStart), MessageID: messageID, Role: "reasoning"})
}

func (b EventBuilder) ReasoningMessageContent(messageID, delta string) Event {
	return mustEvent(ReasoningMessageContentEvent{EventEnvelope: b.envelope(EventReasoningMsgCont), MessageID: messageID, Delta: delta})
}

func (b EventBuilder) ReasoningMessageEnd(messageID string) Event {
	return mustEvent(ReasoningMessageEndEvent{EventEnvelope: b.envelope(EventReasoningMsgEnd), MessageID: messageID})
}

func (b EventBuilder) ReasoningMessageChunk(messageID, delta string) Event {
	return mustEvent(ReasoningMessageChunkEvent{EventEnvelope: b.envelope(EventReasoningMsgChunk), MessageID: messageID, Delta: delta})
}

func (b EventBuilder) ReasoningEncryptedValue(subtype, entityID, encryptedValue string) Event {
	return mustEvent(ReasoningEncryptedValueEvent{EventEnvelope: b.envelope(EventReasoningEncrypted), Subtype: subtype, EntityID: entityID, EncryptedValue: encryptedValue})
}

func (b EventBuilder) ToolCallStart(messageID, toolCallID, name string, index *int) Event {
	return b.ToolCallStartWithMetadata(messageID, toolCallID, name, index, nil)
}

func (b EventBuilder) ToolCallStartWithMetadata(messageID, toolCallID, name string, index *int, metadata map[string]any) Event {
	evt := mustEvent(ToolCallStartEvent{
		EventEnvelope:   b.envelope(EventToolCallStart),
		ParentMessageID: messageID,
		ToolCallID:      toolCallID,
		ToolCallName:    name,
		ToolName:        name,
		Metadata:        metadata,
		Index:           index,
		State:           ToolStateAwaitingInput,
	})
	if index != nil {
		evt.Set("index", *index)
	}
	return evt
}

func (b EventBuilder) ToolCallArgs(toolCallID, delta string, args any) Event {
	return mustEvent(ToolCallArgsEvent{EventEnvelope: b.envelope(EventToolCallArgs), ToolCallID: toolCallID, Delta: delta, State: ToolStateInputStreaming, Args: args})
}

func (b EventBuilder) ToolCallEnd(toolCallID, name string, input any, state string) Event {
	if state == "" {
		state = ToolStateInputComplete
	}
	return mustEvent(ToolCallEndEvent{EventEnvelope: b.envelope(EventToolCallEnd), ToolCallID: toolCallID, ToolCallName: name, ToolName: name, Input: input, State: state})
}

func (b EventBuilder) ToolCallChunk(toolCallID, toolCallName, parentMessageID, delta string) Event {
	return mustEvent(ToolCallChunkEvent{EventEnvelope: b.envelope(EventToolCallChunk), ToolCallID: toolCallID, ToolCallName: toolCallName, ParentMessageID: parentMessageID, Delta: delta})
}

func (b EventBuilder) ToolCallResult(messageID, toolCallID, content, state, role string) Event {
	if role == "" {
		role = RoleTool
	}
	if state == "" {
		state = ToolResultStateComplete
	}
	return mustEvent(ToolCallResultEvent{EventEnvelope: b.envelope(EventToolCallResult), MessageID: messageID, ToolCallID: toolCallID, Content: content, State: state, Role: role})
}

func (b EventBuilder) StepStarted(messageID, stepName string) Event {
	if stepName == "" {
		panic("ag-ui: stepName is required for STEP_STARTED")
	}
	return mustEvent(StepStartedEvent{EventEnvelope: b.envelope(EventStepStarted), MessageID: messageID, StepName: stepName})
}

func (b EventBuilder) StepFinished(messageID, stepName string) Event {
	if stepName == "" {
		panic("ag-ui: stepName is required for STEP_FINISHED")
	}
	return mustEvent(StepFinishedEvent{EventEnvelope: b.envelope(EventStepFinished), MessageID: messageID, StepName: stepName})
}

func (b EventBuilder) StateSnapshot(state map[string]any) Event {
	return mustEvent(StateSnapshotEvent{EventEnvelope: b.envelope(EventStateSnapshot), Snapshot: state})
}

func (b EventBuilder) StateDelta(delta any) Event {
	return mustEvent(StateDeltaEvent{EventEnvelope: b.envelope(EventStateDelta), Delta: delta})
}

func (b EventBuilder) MessagesSnapshot(messages []Message) Event {
	return mustEvent(MessagesSnapshotEvent{EventEnvelope: b.envelope(EventMessagesSnapshot), Messages: messages})
}

func (b EventBuilder) ActivitySnapshot(messageID, activityType string, content map[string]any, replace *bool) Event {
	return mustEvent(ActivitySnapshotEvent{EventEnvelope: b.envelope(EventActivitySnapshot), MessageID: messageID, ActivityType: activityType, Content: content, Replace: replace})
}

func (b EventBuilder) ActivityDelta(messageID, activityType string, patch []any) Event {
	return mustEvent(ActivityDeltaEvent{EventEnvelope: b.envelope(EventActivityDelta), MessageID: messageID, ActivityType: activityType, Patch: patch})
}

func (b EventBuilder) Raw(event any, source string) Event {
	return mustEvent(RawEvent{EventEnvelope: b.envelope(EventRaw), Event: event, Source: source})
}

func (b EventBuilder) Custom(name string, value any) Event {
	return mustEvent(CustomEvent{EventEnvelope: b.envelope(EventCustom), Name: name, Value: value})
}
