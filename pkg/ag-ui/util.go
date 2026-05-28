package agui

import (
	"encoding/json"
	"fmt"
	"strings"
)

func NormalizeFinishReason(value string) string {
	normalized := strings.TrimSpace(strings.ToLower(value))
	switch normalized {
	case "", FinishReasonStop:
		return FinishReasonStop
	case FinishReasonLength:
		return FinishReasonLength
	case FinishReasonContentFilter:
		return FinishReasonContentFilter
	case FinishReasonToolCalls:
		return FinishReasonToolCalls
	case FinishReasonOther:
		return FinishReasonOther
	default:
		return normalized
	}
}

func ValidFinishReason(value string) bool {
	switch value {
	case FinishReasonStop, FinishReasonLength, FinishReasonContentFilter, FinishReasonToolCalls, FinishReasonOther:
		return true
	default:
		return false
	}
}

func CloneEvent(evt Event) Event {
	raw, err := json.Marshal(evt)
	if err != nil {
		cp := make(Event, len(evt))
		for k, v := range evt {
			cp[k] = v
		}
		return cp
	}
	var cp Event
	if err := json.Unmarshal(raw, &cp); err != nil {
		cp = make(Event, len(evt))
		for k, v := range evt {
			cp[k] = v
		}
	}
	return cp
}

func require(evt Event, keys ...string) error {
	for _, key := range keys {
		value, ok := evt[key]
		if !ok || emptyValue(value) {
			return fmt.Errorf("%s missing %s", evt["type"], key)
		}
	}
	return nil
}

// requireStringField checks that the field is present and is a string.
// Unlike require, it accepts whitespace-only strings — streaming deltas can
// legitimately consist only of spaces or newlines between tokens.
func requireStringField(evt Event, key string) error {
	value, ok := evt[key]
	if !ok {
		return fmt.Errorf("%s missing %s", evt["type"], key)
	}
	str, ok := value.(string)
	if !ok {
		return fmt.Errorf("%s has invalid %s %T", evt["type"], key, value)
	}
	if str == "" {
		return fmt.Errorf("%s missing %s", evt["type"], key)
	}
	return nil
}

func emptyValue(value any) bool {
	switch v := value.(type) {
	case string:
		return strings.TrimSpace(v) == ""
	case nil:
		return true
	default:
		return false
	}
}
