package aistream

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/beeper/ai-bridge/pkg/ag-ui"
)

func truncateUTF8(s string, maxBytes int) string {
	if maxBytes <= 0 || len(s) <= maxBytes {
		return s
	}
	end := maxBytes
	for end > 0 && !utf8.RuneStart(s[end]) {
		end--
	}
	return s[:end]
}

type Envelope struct {
	ThreadID    string     `json:"threadId"`
	RunID       string     `json:"runId"`
	MessageID   string     `json:"messageId"`
	Seq         int        `json:"seq"`
	Part        agui.Event `json:"part"`
	TargetEvent string     `json:"target_event,omitempty"`
	RelatesTo   Relation   `json:"m.relates_to,omitempty"`
	AgentID     string     `json:"agent_id,omitempty"`
}

type Relation struct {
	Type    string `json:"rel_type"`
	EventID string `json:"event_id"`
}

type Carrier struct {
	Envelopes []Envelope
}

func BuildEnvelope(run Run, seq int, part agui.Event, targetEventID string) (Envelope, error) {
	if seq <= 0 {
		return Envelope{}, fmt.Errorf("stream envelope: seq must be > 0")
	}
	if err := agui.ValidateEvent(part); err != nil {
		return Envelope{}, err
	}
	targetEventID = strings.TrimSpace(targetEventID)
	if targetEventID == "" {
		return Envelope{}, fmt.Errorf("stream envelope: missing target event id")
	}
	return Envelope{
		ThreadID:    run.ThreadID,
		RunID:       run.RunID,
		MessageID:   run.MessageID,
		Seq:         seq,
		Part:        part,
		TargetEvent: targetEventID,
		RelatesTo:   Relation{Type: "m.reference", EventID: targetEventID},
		AgentID:     run.AgentID,
	}, nil
}

func PackRun(run Run, targetEventID string, budget int) ([]Carrier, error) {
	return PackRunFromSeq(run, targetEventID, budget, 1)
}

func PackRunFromSeq(run Run, targetEventID string, budget int, startSeq int) ([]Carrier, error) {
	if budget <= 0 {
		budget = CarrierBudgetBytes
	}
	if startSeq <= 0 {
		startSeq = 1
	}
	if err := run.Validate(); err != nil {
		return nil, err
	}
	var carriers []Carrier
	var current Carrier
	currentSize := 0
	emptyCarrierOverhead := JSONSize(CarrierContent([]Envelope{}))
	seq := startSeq
	for _, original := range run.Events {
		for _, part := range splitEventForBudget(original, budget) {
			env, err := BuildEnvelope(run, seq, part, targetEventID)
			if err != nil {
				return nil, err
			}
			envSize := JSONSize(env)
			if emptyCarrierOverhead+envSize > budget {
				return nil, fmt.Errorf("stream envelope %d exceeds %d byte budget", seq, budget)
			}
			// +1 for the comma separator between envelopes in the JSON array.
			addedSize := envSize
			if len(current.Envelopes) > 0 {
				addedSize++
			}
			if len(current.Envelopes) > 0 && currentSize+addedSize > budget {
				carriers = append(carriers, current)
				current = Carrier{}
				currentSize = 0
				addedSize = envSize
			}
			if len(current.Envelopes) == 0 {
				currentSize = emptyCarrierOverhead + envSize
			} else {
				currentSize += addedSize
			}
			current.Envelopes = append(current.Envelopes, env)
			seq++
		}
	}
	if len(current.Envelopes) > 0 {
		carriers = append(carriers, current)
	}
	return carriers, nil
}

func NextSeq(carriers []Carrier) int {
	next := 1
	for _, carrier := range carriers {
		for _, env := range carrier.Envelopes {
			if env.Seq >= next {
				next = env.Seq + 1
			}
		}
	}
	return next
}

func CarrierContent(envelopes []Envelope) map[string]any {
	return map[string]any{BeeperAIStreamDeltas: envelopes}
}

func ReconstructText(carriers []Carrier) string {
	var out strings.Builder
	for _, carrier := range carriers {
		for _, env := range carrier.Envelopes {
			if env.Part["type"] == agui.EventTextMessageContent || env.Part["type"] == agui.EventTextMessageChunk {
				delta, _ := env.Part["delta"].(string)
				out.WriteString(delta)
			}
		}
	}
	return out.String()
}

func splitEventForBudget(evt agui.Event, budget int) []agui.Event {
	if evt["type"] == agui.EventMessagesSnapshot {
		return splitMessagesSnapshotForBudget(evt, budget)
	}
	if JSONSize(evt) <= budget {
		return []agui.Event{sanitizeRawEvent(evt, budget)}
	}
	if split := splitStringFieldEventForBudget(evt, budget, "delta"); len(split) > 0 {
		return split
	}
	if split := splitStringFieldEventForBudget(evt, budget, "content"); len(split) > 0 {
		return split
	}
	if evt["type"] != agui.EventTextMessageContent {
		return []agui.Event{sanitizeRawEvent(evt, budget)}
	}
	return []agui.Event{sanitizeRawEvent(evt, budget)}
}

func splitStringFieldEventForBudget(evt agui.Event, budget int, field string) []agui.Event {
	value, _ := evt[field].(string)
	if value == "" {
		return nil
	}
	maxDelta := budget / 2
	if maxDelta < 1024 {
		maxDelta = 1024
	}
	var out []agui.Event
	for _, chunk := range SplitTextUTF8(value, maxDelta) {
		cp := agui.CloneEvent(evt)
		cp[field] = chunk
		out = append(out, sanitizeRawEvent(cp, budget))
	}
	return out
}

func splitMessagesSnapshotForBudget(evt agui.Event, budget int) []agui.Event {
	rawMessages, ok := evt["messages"].([]agui.Message)
	if !ok || len(rawMessages) == 0 {
		return []agui.Event{sanitizeRawEvent(evt, budget)}
	}
	if JSONSize(evt) <= budget {
		return []agui.Event{sanitizeRawEvent(evt, budget)}
	}
	cp := agui.CloneEvent(evt)
	messages := append([]agui.Message{}, rawMessages...)
	for i := range messages {
		content, _ := messages[i].Content.(string)
		if content == "" {
			continue
		}
		preview := BoundedPreview(content, SnapshotTextBytes)
		messages[i].Content = preview
		if messages[i].Metadata == nil {
			messages[i].Metadata = map[string]any{}
		}
		if len(preview) < len(content) {
			messages[i].Metadata["contentTruncated"] = true
		}
	}
	cp["messages"] = messages
	if JSONSize(cp) <= budget {
		return []agui.Event{sanitizeRawEvent(cp, budget)}
	}
	for i := range messages {
		if _, ok := messages[i].Content.(string); ok {
			messages[i].Content = ""
			if messages[i].Metadata == nil {
				messages[i].Metadata = map[string]any{}
			}
			messages[i].Metadata["contentTruncated"] = true
		}
	}
	cp["messages"] = messages
	return []agui.Event{sanitizeRawEvent(cp, budget)}
}

func sanitizeRawEvent(evt agui.Event, budget int) agui.Event {
	cp := agui.CloneEvent(evt)
	rawKey := ""
	switch {
	case cp["rawEvent"] != nil:
		rawKey = "rawEvent"
	case cp["type"] == agui.EventRaw && cp["event"] != nil:
		rawKey = "event"
	}
	if rawKey == "" {
		return cp
	}
	if JSONSize(cp) <= budget {
		return cp
	}
	raw, err := json.Marshal(cp[rawKey])
	if err != nil {
		delete(cp, rawKey)
		cp["rawEventTruncated"] = true
	} else if len(raw) > 2048 {
		cp[rawKey] = truncateUTF8(string(raw), 2048)
		cp["rawEventTruncated"] = true
	}
	if JSONSize(cp) > budget {
		delete(cp, rawKey)
		cp["rawEventTruncated"] = true
	}
	return cp
}

func StreamTxnID(runID string, seq int) string {
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return fmt.Sprintf("ai_stream_%d", seq)
	}
	return fmt.Sprintf("ai_stream_%s_%d", runID, seq)
}
