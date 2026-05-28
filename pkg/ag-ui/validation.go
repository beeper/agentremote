package agui

import (
	"fmt"
	"strings"
)

func ValidateEvent(evt Event) error {
	eventType := evt.Type()
	if eventType == "" {
		return fmt.Errorf("ag-ui event missing type")
	}
	switch eventType {
	case EventRunStarted:
		return require(evt, "threadId", "runId")
	case EventRunFinished:
		if err := require(evt, "threadId", "runId"); err != nil {
			return err
		}
		if err := validateFinishReason(evt); err != nil {
			return err
		}
		return validateRunFinishedOutcome(evt.Get("outcome"))
	case EventRunError:
		return require(evt, "message")
	case EventTextMessageStart:
		return require(evt, "messageId", "role")
	case EventTextMessageContent:
		if err := require(evt, "messageId"); err != nil {
			return err
		}
		return requireStringField(evt, "delta")
	case EventTextMessageEnd:
		return require(evt, "messageId")
	case EventTextMessageChunk:
		if messageID, _ := evt.Get("messageId").(string); messageID == "" {
			if delta, _ := evt.Get("delta").(string); delta == "" {
				return fmt.Errorf("%s requires messageId or delta", eventType)
			}
		}
		return nil
	case EventReasoningStart, EventReasoningEnd, EventReasoningMsgStart, EventReasoningMsgEnd:
		return require(evt, "messageId")
	case EventReasoningMsgCont:
		if err := require(evt, "messageId"); err != nil {
			return err
		}
		return requireStringField(evt, "delta")
	case EventReasoningMsgChunk:
		if messageID, _ := evt.Get("messageId").(string); messageID == "" {
			if delta, _ := evt.Get("delta").(string); delta == "" {
				return fmt.Errorf("%s requires messageId or delta", eventType)
			}
		}
		return nil
	case EventReasoningEncrypted:
		if err := require(evt, "subtype", "entityId", "encryptedValue"); err != nil {
			return err
		}
		return validateStringSet(evt, "subtype", true, map[string]bool{"message": true, "tool-call": true})
	case EventToolCallStart:
		if err := require(evt, "toolCallId", "toolCallName"); err != nil {
			return err
		}
		return validateStringSet(evt, "state", true, validToolStates)
	case EventToolCallArgs:
		if err := require(evt, "toolCallId"); err != nil {
			return err
		}
		if err := requireStringField(evt, "delta"); err != nil {
			return err
		}
		if err := validateStringSet(evt, "state", false, validToolStates); err != nil {
			return err
		}
		return nil
	case EventToolCallEnd:
		if err := require(evt, "toolCallId"); err != nil {
			return err
		}
		if evt.Has("result") {
			return fmt.Errorf("%s must not include result; emit TOOL_CALL_RESULT instead", evt.Type())
		}
		return validateStringSet(evt, "state", true, validToolStates)
	case EventToolCallChunk:
		if toolCallID, _ := evt.Get("toolCallId").(string); toolCallID == "" {
			if delta, _ := evt.Get("delta").(string); delta == "" {
				return fmt.Errorf("%s requires toolCallId or delta", eventType)
			}
		}
		return nil
	case EventToolCallResult:
		if err := require(evt, "messageId", "toolCallId", "content"); err != nil {
			return err
		}
		return validateStringSet(evt, "state", false, validToolResultStates)
	case EventStepStarted, EventStepFinished:
		return require(evt, "stepName")
	case EventStateSnapshot:
		return require(evt, "snapshot")
	case EventStateDelta:
		return require(evt, "delta")
	case EventMessagesSnapshot:
		return require(evt, "messages")
	case EventActivitySnapshot:
		return require(evt, "messageId", "activityType", "content")
	case EventActivityDelta:
		return require(evt, "messageId", "activityType", "patch")
	case EventRaw:
		return require(evt, "event")
	case EventCustom:
		return require(evt, "name")
	default:
		return fmt.Errorf("unsupported ag-ui event type %q", eventType)
	}
}

func validateFinishReason(evt Event) error {
	raw := evt.Get("finishReason")
	if !evt.Has("finishReason") {
		return nil
	}
	value, ok := raw.(string)
	if !ok {
		return fmt.Errorf("RUN_FINISHED has invalid finishReason %T", raw)
	}
	if !ValidFinishReason(value) {
		return fmt.Errorf("RUN_FINISHED has invalid finishReason %q", value)
	}
	return nil
}

func validateRunFinishedOutcome(value any) error {
	if value == nil {
		return nil
	}
	switch outcome := value.(type) {
	case RunFinishedOutcome:
		return validateOutcomeFields(outcome.Type, interruptsToAny(outcome.Interrupts))
	case *RunFinishedOutcome:
		if outcome == nil {
			return nil
		}
		return validateRunFinishedOutcome(*outcome)
	case map[string]any:
		outcomeType, _ := outcome["type"].(string)
		return validateOutcomeFields(outcomeType, outcome["interrupts"])
	default:
		return fmt.Errorf("RUN_FINISHED has invalid outcome %T", value)
	}
}

func interruptsToAny(interrupts []Interrupt) []any {
	out := make([]any, 0, len(interrupts))
	for _, interrupt := range interrupts {
		out = append(out, interrupt)
	}
	return out
}

func validateOutcomeFields(outcomeType string, interrupts any) error {
	switch outcomeType {
	case "":
		return fmt.Errorf("RUN_FINISHED outcome missing type")
	case OutcomeSuccess:
		return nil
	case OutcomeInterrupt:
		return validateInterrupts(interrupts)
	default:
		return fmt.Errorf("RUN_FINISHED has invalid outcome type %q", outcomeType)
	}
}

func validateInterrupts(value any) error {
	switch interrupts := value.(type) {
	case []Interrupt:
		if len(interrupts) == 0 {
			return fmt.Errorf("interrupt outcome requires interrupts")
		}
		for i, interrupt := range interrupts {
			if err := validateInterrupt(interrupt.ID, interrupt.Reason, interrupt.ToolCallID); err != nil {
				return fmt.Errorf("interrupt %d: %w", i+1, err)
			}
		}
		return nil
	case []any:
		if len(interrupts) == 0 {
			return fmt.Errorf("interrupt outcome requires interrupts")
		}
		for i, raw := range interrupts {
			switch interrupt := raw.(type) {
			case Interrupt:
				if err := validateInterrupt(interrupt.ID, interrupt.Reason, interrupt.ToolCallID); err != nil {
					return fmt.Errorf("interrupt %d: %w", i+1, err)
				}
			case map[string]any:
				id, _ := interrupt["id"].(string)
				reason, _ := interrupt["reason"].(string)
				toolCallID, _ := interrupt["toolCallId"].(string)
				if err := validateInterrupt(id, reason, toolCallID); err != nil {
					return fmt.Errorf("interrupt %d: %w", i+1, err)
				}
			default:
				return fmt.Errorf("interrupt %d has invalid type %T", i+1, raw)
			}
		}
		return nil
	default:
		return fmt.Errorf("interrupt outcome requires interrupts")
	}
}

func validateInterrupt(id, reason, toolCallID string) error {
	if strings.TrimSpace(id) == "" {
		return fmt.Errorf("missing id")
	}
	if strings.TrimSpace(reason) == "" {
		return fmt.Errorf("missing reason")
	}
	if reason == InterruptReasonToolCall && strings.TrimSpace(toolCallID) == "" {
		return fmt.Errorf("tool_call interrupt missing toolCallId")
	}
	return nil
}

func ValidateEventSequence(events []Event) error {
	seenRunStart := false
	terminal := false
	textOpen := map[string]bool{}
	reasoningPhaseOpen := map[string]bool{}
	reasoningOpen := map[string]bool{}
	toolStarted := map[string]bool{}
	toolEnded := map[string]bool{}

	for i, evt := range events {
		if err := ValidateEvent(evt); err != nil {
			return fmt.Errorf("event %d: %w", i+1, err)
		}
		eventType := evt.Type()
		if terminal {
			return fmt.Errorf("event %d: %s after terminal run event", i+1, eventType)
		}

		switch eventType {
		case EventRunStarted:
			if seenRunStart {
				return fmt.Errorf("event %d: duplicate RUN_STARTED", i+1)
			}
			seenRunStart = true
		case EventRunFinished:
			if !seenRunStart {
				return fmt.Errorf("event %d: RUN_FINISHED before RUN_STARTED", i+1)
			}
			if err := rejectOpenSequences(i+1, textOpen, reasoningOpen, reasoningPhaseOpen); err != nil {
				return err
			}
			terminal = true
		case EventRunError:
			if err := rejectOpenSequences(i+1, textOpen, reasoningOpen, reasoningPhaseOpen); err != nil {
				return err
			}
			terminal = true
		case EventTextMessageStart:
			messageID := stringField(evt, "messageId")
			if textOpen[messageID] {
				return fmt.Errorf("event %d: duplicate TEXT_MESSAGE_START for %s", i+1, messageID)
			}
			textOpen[messageID] = true
		case EventTextMessageContent:
			messageID := stringField(evt, "messageId")
			if !textOpen[messageID] {
				return fmt.Errorf("event %d: TEXT_MESSAGE_CONTENT before TEXT_MESSAGE_START for %s", i+1, messageID)
			}
		case EventTextMessageChunk:
			messageID := stringField(evt, "messageId")
			if messageID != "" {
				if !textOpen[messageID] {
					textOpen[messageID] = true
				}
				if stringField(evt, "delta") == "" {
					delete(textOpen, messageID)
				}
			}
		case EventTextMessageEnd:
			messageID := stringField(evt, "messageId")
			if !textOpen[messageID] {
				return fmt.Errorf("event %d: TEXT_MESSAGE_END before TEXT_MESSAGE_START for %s", i+1, messageID)
			}
			delete(textOpen, messageID)
		case EventReasoningStart:
			messageID := stringField(evt, "messageId")
			if reasoningPhaseOpen[messageID] {
				return fmt.Errorf("event %d: duplicate REASONING_START for %s", i+1, messageID)
			}
			reasoningPhaseOpen[messageID] = true
		case EventReasoningEnd:
			messageID := stringField(evt, "messageId")
			if !reasoningPhaseOpen[messageID] {
				return fmt.Errorf("event %d: REASONING_END before REASONING_START for %s", i+1, messageID)
			}
			delete(reasoningPhaseOpen, messageID)
		case EventReasoningMsgStart:
			messageID := stringField(evt, "messageId")
			if reasoningOpen[messageID] {
				return fmt.Errorf("event %d: duplicate REASONING_MESSAGE_START for %s", i+1, messageID)
			}
			reasoningOpen[messageID] = true
		case EventReasoningMsgCont:
			messageID := stringField(evt, "messageId")
			if !reasoningOpen[messageID] {
				return fmt.Errorf("event %d: REASONING_MESSAGE_CONTENT before REASONING_MESSAGE_START for %s", i+1, messageID)
			}
		case EventReasoningMsgChunk:
			messageID := stringField(evt, "messageId")
			if messageID != "" {
				if !reasoningOpen[messageID] {
					reasoningOpen[messageID] = true
				}
				if stringField(evt, "delta") == "" {
					delete(reasoningOpen, messageID)
				}
			}
		case EventReasoningMsgEnd:
			messageID := stringField(evt, "messageId")
			if !reasoningOpen[messageID] {
				return fmt.Errorf("event %d: REASONING_MESSAGE_END before REASONING_MESSAGE_START for %s", i+1, messageID)
			}
			delete(reasoningOpen, messageID)
		case EventToolCallStart:
			toolCallID := stringField(evt, "toolCallId")
			if toolStarted[toolCallID] {
				return fmt.Errorf("event %d: duplicate TOOL_CALL_START for %s", i+1, toolCallID)
			}
			toolStarted[toolCallID] = true
		case EventToolCallArgs:
			toolCallID := stringField(evt, "toolCallId")
			if !toolStarted[toolCallID] {
				return fmt.Errorf("event %d: TOOL_CALL_ARGS before TOOL_CALL_START for %s", i+1, toolCallID)
			}
		case EventToolCallEnd:
			toolCallID := stringField(evt, "toolCallId")
			if !toolStarted[toolCallID] {
				return fmt.Errorf("event %d: TOOL_CALL_END before TOOL_CALL_START for %s", i+1, toolCallID)
			}
			if toolEnded[toolCallID] {
				return fmt.Errorf("event %d: duplicate TOOL_CALL_END for %s", i+1, toolCallID)
			}
			toolEnded[toolCallID] = true
		case EventToolCallResult:
			toolCallID := stringField(evt, "toolCallId")
			if !toolStarted[toolCallID] {
				return fmt.Errorf("event %d: TOOL_CALL_RESULT before TOOL_CALL_START for %s", i+1, toolCallID)
			}
		}
	}
	return nil
}

func rejectOpenSequences(eventNumber int, textOpen, reasoningOpen, reasoningPhaseOpen map[string]bool) error {
	for messageID, open := range textOpen {
		if open {
			return fmt.Errorf("event %d: terminal run event while TEXT_MESSAGE %s is open", eventNumber, messageID)
		}
	}
	for messageID, open := range reasoningOpen {
		if open {
			return fmt.Errorf("event %d: terminal run event while REASONING_MESSAGE %s is open", eventNumber, messageID)
		}
	}
	for messageID, open := range reasoningPhaseOpen {
		if open {
			return fmt.Errorf("event %d: terminal run event while REASONING %s is open", eventNumber, messageID)
		}
	}
	return nil
}

var validToolStates = map[string]bool{
	ToolStateAwaitingInput:  true,
	ToolStateInputStreaming: true,
	ToolStateInputComplete:  true,
}

func stringField(evt Event, key string) string {
	value, _ := evt.Get(key).(string)
	return value
}

var validToolResultStates = map[string]bool{
	ToolResultStateStreaming: true,
	ToolResultStateComplete:  true,
	ToolResultStateError:     true,
}

func validateStringSet(evt Event, key string, required bool, allowed map[string]bool) error {
	value := evt.Get(key)
	if !evt.Has(key) || value == nil {
		if required {
			return fmt.Errorf("%s missing %s", evt.Type(), key)
		}
		return nil
	}
	stringValue, ok := value.(string)
	if !ok || !allowed[stringValue] {
		return fmt.Errorf("%s has invalid %s %q", evt.Type(), key, value)
	}
	return nil
}
