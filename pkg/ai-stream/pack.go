package aistream

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/beeper/ai-bridge/pkg/ag-ui"
)

type Envelope struct {
	Seq   int        `json:"seq"`
	Event agui.Event `json:"event"`
}

type Carrier struct {
	Envelopes []Envelope
}

func BuildEnvelope(run Run, seq int, part agui.Event) (Envelope, error) {
	if seq <= 0 {
		return Envelope{}, fmt.Errorf("stream envelope: seq must be > 0")
	}
	if err := agui.ValidateEvent(part); err != nil {
		return Envelope{}, err
	}
	return Envelope{Seq: seq, Event: part}, nil
}

func PackRun(run Run) ([]Carrier, error) {
	return PackRunFromSeq(run, 1)
}

func PackRunFromSeq(run Run, startSeq int) ([]Carrier, error) {
	return packRunFromSeq(run, startSeq, 0)
}

func PackRunByTimeFromSeq(run Run, startSeq int, maxSpan time.Duration) ([]Carrier, error) {
	return packRunFromSeq(run, startSeq, maxSpan)
}

func packRunFromSeq(run Run, startSeq int, maxSpan time.Duration) ([]Carrier, error) {
	if startSeq <= 0 {
		startSeq = 1
	}
	if err := run.Validate(); err != nil {
		return nil, err
	}
	var carriers []Carrier
	var current Carrier
	var currentStart time.Time
	seq := startSeq
	for _, event := range run.Events {
		env, err := BuildEnvelope(run, seq, event)
		if err != nil {
			return nil, err
		}
		eventTime := EventTimestamp(event)
		overSpan := maxSpan > 0 && len(current.Envelopes) > 0 && !currentStart.IsZero() && !eventTime.IsZero() && eventTime.Sub(currentStart) > maxSpan
		if overSpan {
			carriers = append(carriers, current)
			current = Carrier{}
			currentStart = time.Time{}
		}
		if currentStart.IsZero() && !eventTime.IsZero() {
			currentStart = eventTime
		}
		current.Envelopes = append(current.Envelopes, env)
		seq++
	}
	if len(current.Envelopes) > 0 {
		carriers = append(carriers, current)
	}
	return carriers, nil
}

func EventTimestamp(evt agui.Event) time.Time {
	if !evt.Has("timestamp") {
		return time.Time{}
	}
	switch value := evt.Get("timestamp").(type) {
	case int64:
		return time.UnixMilli(value)
	case int:
		return time.UnixMilli(int64(value))
	case int32:
		return time.UnixMilli(int64(value))
	case float64:
		return time.UnixMilli(int64(value))
	case json.Number:
		millis, err := value.Int64()
		if err != nil {
			return time.Time{}
		}
		return time.UnixMilli(millis)
	default:
		return time.Time{}
	}
}

func CarrierTimestamp(run Run, carrier Carrier, streamStart time.Time) time.Time {
	base := runStartTimestamp(run)
	if base.IsZero() {
		return time.Time{}
	}
	var latest time.Time
	for _, env := range carrier.Envelopes {
		eventTime := EventTimestamp(env.Event)
		if eventTime.IsZero() {
			continue
		}
		if latest.IsZero() || eventTime.After(latest) {
			latest = eventTime
		}
	}
	if latest.IsZero() {
		return time.Time{}
	}
	return streamStart.Add(latest.Sub(base))
}

func runStartTimestamp(run Run) time.Time {
	for _, evt := range run.Events {
		if ts := EventTimestamp(evt); !ts.IsZero() {
			return ts
		}
	}
	return time.Time{}
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

func CarrierContent(run Run, envelopes []Envelope) map[string]any {
	return map[string]any{
		BeeperAIKey: run.AIStream(envelopes),
	}
}

func ReconstructText(carriers []Carrier) string {
	var out strings.Builder
	for _, carrier := range carriers {
		for _, env := range carrier.Envelopes {
			if env.Event.Type() == agui.EventTextMessageContent || env.Event.Type() == agui.EventTextMessageChunk {
				delta, _ := env.Event.Get("delta").(string)
				out.WriteString(delta)
			}
		}
	}
	return out.String()
}

func StreamTxnID(runID string, seq int) string {
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return fmt.Sprintf("ai_stream_%d", seq)
	}
	return fmt.Sprintf("ai_stream_%s_%d", runID, seq)
}
