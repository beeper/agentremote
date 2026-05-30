package utils

import (
	"testing"
	"time"

	ai "github.com/beeper/ai-bridge/pkg/ai"
)

func TestCreateAssistantMessageEventStreamFactory(t *testing.T) {
	stream := CreateAssistantMessageEventStream()
	message := ai.Message{Role: "assistant", StopReason: ai.StopReasonStop}
	stream.Push(ai.AssistantMessageEvent{Type: "done", Reason: ai.StopReasonStop, Message: &message})
	if result := stream.Result(); result.Role != "assistant" || result.StopReason != ai.StopReasonStop {
		t.Fatalf("unexpected result %#v", result)
	}
}

func TestEventStreamResultDrainsUndeliveredEvents(t *testing.T) {
	stream := NewEventStream[int, string]()
	go func() {
		for i := 0; i < 128; i++ {
			stream.Push(i)
		}
		stream.End("done")
	}()

	resultCh := make(chan string, 1)
	go func() {
		resultCh <- stream.Result()
	}()
	select {
	case result := <-resultCh:
		if result != "done" {
			t.Fatalf("unexpected result %q", result)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Result deadlocked without an event consumer")
	}
}

func TestEventStreamIgnoresPushAfterEnd(t *testing.T) {
	stream := NewEventStream[int, string]()
	stream.End("done")
	stream.Push(1)
	if result := stream.Result(); result != "done" {
		t.Fatalf("unexpected result %q", result)
	}
}
