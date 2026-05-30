package utils

import (
	"sync"

	ai "github.com/beeper/ai-bridge/pkg/ai"
)

type EventStream[T any, R any] struct {
	events chan T
	done   chan struct{}
	once   sync.Once
	mu     sync.Mutex
	result R
	closed bool
}

func NewEventStream[T any, R any]() *EventStream[T, R] {
	return &EventStream[T, R]{events: make(chan T, 64), done: make(chan struct{})}
}

func (s *EventStream[T, R]) Events() <-chan T {
	return s.events
}

func (s *EventStream[T, R]) Push(event T) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	s.events <- event
}

func (s *EventStream[T, R]) End(result R) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.once.Do(func() {
		s.result = result
		s.closed = true
		close(s.done)
		close(s.events)
	})
}

func (s *EventStream[T, R]) Result() R {
	for {
		select {
		case <-s.done:
			s.mu.Lock()
			defer s.mu.Unlock()
			return s.result
		case _, ok := <-s.events:
			if !ok {
				s.mu.Lock()
				defer s.mu.Unlock()
				return s.result
			}
		}
	}
}

type AssistantMessageEventStream struct {
	*EventStream[ai.AssistantMessageEvent, ai.Message]
}

func NewAssistantMessageEventStream() *AssistantMessageEventStream {
	return &AssistantMessageEventStream{EventStream: NewEventStream[ai.AssistantMessageEvent, ai.Message]()}
}

func CreateAssistantMessageEventStream() *AssistantMessageEventStream {
	return NewAssistantMessageEventStream()
}

func (s *AssistantMessageEventStream) Push(event ai.AssistantMessageEvent) {
	if event.Type == "done" && event.Message != nil {
		s.EventStream.Push(event)
		s.End(*event.Message)
		return
	}
	if event.Type == "error" && event.Error != nil {
		s.EventStream.Push(event)
		s.End(*event.Error)
		return
	}
	s.EventStream.Push(event)
}
