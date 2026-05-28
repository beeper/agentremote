package agui

import (
	"encoding/json"
	"reflect"
	"strings"
)

type EventEnvelope struct {
	Type      string `json:"type"`
	Timestamp int64  `json:"timestamp,omitempty"`
	Model     string `json:"model,omitempty"`
}

type RunStartedEvent struct {
	EventEnvelope
	ThreadID string `json:"threadId"`
	RunID    string `json:"runId"`
}

type RunFinishedEvent struct {
	EventEnvelope
	ThreadID     string             `json:"threadId"`
	RunID        string             `json:"runId"`
	FinishReason string             `json:"finishReason,omitempty"`
	Usage        Usage              `json:"usage,omitempty"`
	Outcome      RunFinishedOutcome `json:"outcome,omitempty"`
}

type RunErrorEvent struct {
	EventEnvelope
	ThreadID string   `json:"threadId,omitempty"`
	RunID    string   `json:"runId,omitempty"`
	Message  string   `json:"message"`
	Error    RunError `json:"error"`
}

type RunError struct {
	Message string `json:"message"`
	Code    string `json:"code,omitempty"`
}

type TextMessageStartEvent struct {
	EventEnvelope
	MessageID string `json:"messageId"`
	Role      string `json:"role"`
}

type TextMessageContentEvent struct {
	EventEnvelope
	MessageID string `json:"messageId"`
	Delta     string `json:"delta"`
}

type TextMessageEndEvent struct {
	EventEnvelope
	MessageID string `json:"messageId"`
}

type TextMessageChunkEvent struct {
	EventEnvelope
	MessageID string `json:"messageId,omitempty"`
	Role      string `json:"role,omitempty"`
	Delta     string `json:"delta,omitempty"`
}

type ReasoningMessageStartEvent struct {
	EventEnvelope
	MessageID string `json:"messageId"`
	Role      string `json:"role,omitempty"`
}

type ReasoningMessageContentEvent struct {
	EventEnvelope
	MessageID string `json:"messageId"`
	Delta     string `json:"delta"`
}

type ReasoningMessageEndEvent struct {
	EventEnvelope
	MessageID string `json:"messageId"`
}

type ReasoningMessageChunkEvent struct {
	EventEnvelope
	MessageID string `json:"messageId,omitempty"`
	Delta     string `json:"delta,omitempty"`
}

type ReasoningEncryptedValueEvent struct {
	EventEnvelope
	Subtype        string `json:"subtype"`
	EntityID       string `json:"entityId"`
	EncryptedValue string `json:"encryptedValue"`
}

type ToolCallStartEvent struct {
	EventEnvelope
	ParentMessageID string         `json:"parentMessageId,omitempty"`
	ToolCallID      string         `json:"toolCallId"`
	ToolCallName    string         `json:"toolCallName"`
	ToolName        string         `json:"toolName"`
	Metadata        map[string]any `json:"metadata,omitempty"`
	Index           *int           `json:"index,omitempty"`
	State           string         `json:"state"`
}

type ToolCallArgsEvent struct {
	EventEnvelope
	ToolCallID string `json:"toolCallId"`
	Delta      string `json:"delta"`
	State      string `json:"state,omitempty"`
	Args       any    `json:"args,omitempty"`
}

type ToolCallEndEvent struct {
	EventEnvelope
	ToolCallID   string `json:"toolCallId"`
	ToolCallName string `json:"toolCallName,omitempty"`
	ToolName     string `json:"toolName,omitempty"`
	Input        any    `json:"input,omitempty"`
	State        string `json:"state"`
}

type ToolCallChunkEvent struct {
	EventEnvelope
	ToolCallID      string `json:"toolCallId,omitempty"`
	ToolCallName    string `json:"toolCallName,omitempty"`
	ParentMessageID string `json:"parentMessageId,omitempty"`
	Delta           string `json:"delta,omitempty"`
}

type ToolCallResultEvent struct {
	EventEnvelope
	MessageID  string `json:"messageId"`
	ToolCallID string `json:"toolCallId"`
	Content    string `json:"content"`
	State      string `json:"state,omitempty"`
	Role       string `json:"role,omitempty"`
	Error      string `json:"error,omitempty"`
}

type StepStartedEvent struct {
	EventEnvelope
	MessageID string `json:"messageId,omitempty"`
	StepName  string `json:"stepName"`
}

type StepFinishedEvent struct {
	EventEnvelope
	MessageID string `json:"messageId,omitempty"`
	StepName  string `json:"stepName"`
}

type StateSnapshotEvent struct {
	EventEnvelope
	Snapshot map[string]any `json:"snapshot"`
}

type StateDeltaEvent struct {
	EventEnvelope
	Delta any `json:"delta"`
}

type MessagesSnapshotEvent struct {
	EventEnvelope
	Messages []Message `json:"messages"`
}

type ActivitySnapshotEvent struct {
	EventEnvelope
	MessageID    string         `json:"messageId"`
	ActivityType string         `json:"activityType"`
	Content      map[string]any `json:"content"`
	Replace      *bool          `json:"replace,omitempty"`
}

type ActivityDeltaEvent struct {
	EventEnvelope
	MessageID    string `json:"messageId"`
	ActivityType string `json:"activityType"`
	Patch        []any  `json:"patch"`
}

type RawEvent struct {
	EventEnvelope
	Event  any    `json:"event"`
	Source string `json:"source,omitempty"`
}

type CustomEvent struct {
	EventEnvelope
	Name  string `json:"name"`
	Value any    `json:"value,omitempty"`
}

func EventFromTyped(value any) (Event, error) {
	evt := NewEvent(nil)
	if err := appendJSONFields(&evt, reflect.ValueOf(value)); err != nil {
		return Event{}, err
	}
	return evt, nil
}

func mustEvent(value any) Event {
	evt, err := EventFromTyped(value)
	if err != nil {
		panic(err)
	}
	return evt
}

func appendJSONFields(out *Event, value reflect.Value) error {
	if !value.IsValid() {
		return nil
	}
	if value.Kind() == reflect.Pointer {
		if value.IsNil() {
			return nil
		}
		value = value.Elem()
	}
	if value.Kind() != reflect.Struct {
		raw, err := json.Marshal(value.Interface())
		if err != nil {
			return err
		}
		return json.Unmarshal(raw, out)
	}
	typ := value.Type()
	for i := 0; i < value.NumField(); i++ {
		field := typ.Field(i)
		if field.PkgPath != "" {
			continue
		}
		fieldValue := value.Field(i)
		if field.Anonymous {
			if err := appendJSONFields(out, fieldValue); err != nil {
				return err
			}
			continue
		}
		name, omitEmpty, skip := jsonField(field)
		if skip || (omitEmpty && fieldValue.IsZero()) {
			continue
		}
		out.Set(name, fieldValue.Interface())
	}
	return nil
}

func jsonField(field reflect.StructField) (name string, omitEmpty bool, skip bool) {
	tag := field.Tag.Get("json")
	if tag == "-" {
		return "", false, true
	}
	parts := strings.Split(tag, ",")
	name = parts[0]
	if name == "" {
		name = field.Name
	}
	for _, part := range parts[1:] {
		if part == "omitempty" {
			omitEmpty = true
		}
	}
	return name, omitEmpty, false
}
