package agui

import (
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

type Event map[string]any

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
	evt := Event{
		"type":      eventType,
		"timestamp": b.now().UnixMilli(),
	}
	if b.model != "" {
		evt["model"] = b.model
	}
	return evt
}

func (b EventBuilder) RunStarted(threadID, runID string) Event {
	evt := b.base(EventRunStarted)
	evt["threadId"] = threadID
	evt["runId"] = runID
	return evt
}

func (b EventBuilder) RunFinished(threadID, runID, finishReason string, usage Usage) Event {
	return b.RunFinishedWithOutcome(threadID, runID, finishReason, usage, RunFinishedOutcome{Type: OutcomeSuccess})
}

func (b EventBuilder) RunFinishedWithOutcome(threadID, runID, finishReason string, usage Usage, outcome RunFinishedOutcome) Event {
	evt := b.base(EventRunFinished)
	evt["threadId"] = threadID
	evt["runId"] = runID
	if finishReason != "" {
		evt["finishReason"] = NormalizeFinishReason(finishReason)
	}
	evt["usage"] = usage
	if outcome.Type != "" {
		evt["outcome"] = outcome
	}
	return evt
}

func (b EventBuilder) RunError(threadID, runID, message string) Event {
	evt := b.base(EventRunError)
	evt["threadId"] = threadID
	if strings.TrimSpace(runID) != "" {
		evt["runId"] = runID
	}
	evt["message"] = message
	evt["error"] = map[string]any{"message": message}
	return evt
}

func (b EventBuilder) TextMessageStart(messageID, role string) Event {
	if role == "" {
		role = RoleAssistant
	}
	evt := b.base(EventTextMessageStart)
	evt["messageId"] = messageID
	evt["role"] = role
	return evt
}

func (b EventBuilder) TextMessageContent(messageID, delta string) Event {
	evt := b.base(EventTextMessageContent)
	evt["messageId"] = messageID
	evt["delta"] = delta
	return evt
}

func (b EventBuilder) TextMessageEnd(messageID string) Event {
	evt := b.base(EventTextMessageEnd)
	evt["messageId"] = messageID
	return evt
}

func (b EventBuilder) TextMessageChunk(messageID, role, delta string) Event {
	evt := b.base(EventTextMessageChunk)
	if messageID != "" {
		evt["messageId"] = messageID
	}
	if role != "" {
		evt["role"] = role
	}
	if delta != "" {
		evt["delta"] = delta
	}
	return evt
}

func (b EventBuilder) ReasoningStart(messageID string) Event {
	evt := b.base(EventReasoningStart)
	evt["messageId"] = messageID
	return evt
}

func (b EventBuilder) ReasoningEnd(messageID string) Event {
	evt := b.base(EventReasoningEnd)
	evt["messageId"] = messageID
	return evt
}

func (b EventBuilder) ReasoningMessageStart(messageID string) Event {
	evt := b.base(EventReasoningMsgStart)
	evt["messageId"] = messageID
	evt["role"] = "reasoning"
	return evt
}

func (b EventBuilder) ReasoningMessageContent(messageID, delta string) Event {
	evt := b.base(EventReasoningMsgCont)
	evt["messageId"] = messageID
	evt["delta"] = delta
	return evt
}

func (b EventBuilder) ReasoningMessageEnd(messageID string) Event {
	evt := b.base(EventReasoningMsgEnd)
	evt["messageId"] = messageID
	return evt
}

func (b EventBuilder) ReasoningMessageChunk(messageID, delta string) Event {
	evt := b.base(EventReasoningMsgChunk)
	if messageID != "" {
		evt["messageId"] = messageID
	}
	if delta != "" {
		evt["delta"] = delta
	}
	return evt
}

func (b EventBuilder) ReasoningEncryptedValue(subtype, entityID, encryptedValue string) Event {
	evt := b.base(EventReasoningEncrypted)
	evt["subtype"] = subtype
	evt["entityId"] = entityID
	evt["encryptedValue"] = encryptedValue
	return evt
}

func (b EventBuilder) ToolCallStart(messageID, toolCallID, name string, index *int) Event {
	return b.ToolCallStartWithMetadata(messageID, toolCallID, name, index, nil)
}

func (b EventBuilder) ToolCallStartWithMetadata(messageID, toolCallID, name string, index *int, metadata map[string]any) Event {
	evt := b.base(EventToolCallStart)
	if messageID != "" {
		evt["parentMessageId"] = messageID
	}
	evt["toolCallId"] = toolCallID
	evt["toolCallName"] = name
	evt["toolName"] = name
	if len(metadata) > 0 {
		evt["metadata"] = metadata
	}
	if index != nil {
		evt["index"] = *index
	}
	evt["state"] = ToolStateAwaitingInput
	return evt
}

func (b EventBuilder) ToolCallArgs(toolCallID, delta string, args any) Event {
	evt := b.base(EventToolCallArgs)
	evt["toolCallId"] = toolCallID
	evt["delta"] = delta
	evt["state"] = ToolStateInputStreaming
	if args != nil {
		evt["args"] = args
	}
	return evt
}

func (b EventBuilder) ToolCallEnd(toolCallID, name string, input any, state string) Event {
	evt := b.base(EventToolCallEnd)
	evt["toolCallId"] = toolCallID
	evt["toolCallName"] = name
	evt["toolName"] = name
	if input != nil {
		evt["input"] = input
	}
	if state == "" {
		state = ToolStateInputComplete
	}
	evt["state"] = state
	return evt
}

func (b EventBuilder) ToolCallChunk(toolCallID, toolCallName, parentMessageID, delta string) Event {
	evt := b.base(EventToolCallChunk)
	if toolCallID != "" {
		evt["toolCallId"] = toolCallID
	}
	if toolCallName != "" {
		evt["toolCallName"] = toolCallName
	}
	if parentMessageID != "" {
		evt["parentMessageId"] = parentMessageID
	}
	if delta != "" {
		evt["delta"] = delta
	}
	return evt
}

func (b EventBuilder) ToolCallResult(messageID, toolCallID, content, state, role string) Event {
	if role == "" {
		role = RoleTool
	}
	if state == "" {
		state = ToolResultStateComplete
	}
	evt := b.base(EventToolCallResult)
	evt["messageId"] = messageID
	evt["toolCallId"] = toolCallID
	evt["content"] = content
	evt["state"] = state
	evt["role"] = role
	return evt
}

func (b EventBuilder) StepStarted(messageID, stepName string) Event {
	if stepName == "" {
		panic("ag-ui: stepName is required for STEP_STARTED")
	}
	evt := b.base(EventStepStarted)
	if messageID != "" {
		evt["messageId"] = messageID
	}
	evt["stepName"] = stepName
	return evt
}

func (b EventBuilder) StepFinished(messageID, stepName string) Event {
	if stepName == "" {
		panic("ag-ui: stepName is required for STEP_FINISHED")
	}
	evt := b.base(EventStepFinished)
	if messageID != "" {
		evt["messageId"] = messageID
	}
	evt["stepName"] = stepName
	return evt
}

func (b EventBuilder) StateSnapshot(state map[string]any) Event {
	evt := b.base(EventStateSnapshot)
	evt["snapshot"] = state
	return evt
}

func (b EventBuilder) StateDelta(delta any) Event {
	evt := b.base(EventStateDelta)
	evt["delta"] = delta
	return evt
}

func (b EventBuilder) MessagesSnapshot(messages []Message) Event {
	evt := b.base(EventMessagesSnapshot)
	evt["messages"] = messages
	return evt
}

func (b EventBuilder) ActivitySnapshot(messageID, activityType string, content map[string]any, replace *bool) Event {
	evt := b.base(EventActivitySnapshot)
	evt["messageId"] = messageID
	evt["activityType"] = activityType
	evt["content"] = content
	if replace != nil {
		evt["replace"] = *replace
	}
	return evt
}

func (b EventBuilder) ActivityDelta(messageID, activityType string, patch []any) Event {
	evt := b.base(EventActivityDelta)
	evt["messageId"] = messageID
	evt["activityType"] = activityType
	evt["patch"] = patch
	return evt
}

func (b EventBuilder) Raw(event any, source string) Event {
	evt := b.base(EventRaw)
	evt["event"] = event
	if source != "" {
		evt["source"] = source
	}
	return evt
}

func (b EventBuilder) Custom(name string, value any) Event {
	evt := b.base(EventCustom)
	evt["name"] = name
	evt["value"] = value
	return evt
}
