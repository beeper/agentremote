package ai

import "sync"

type AssistantMessageEventStream struct {
	events chan AssistantMessageEvent
	done   chan struct{}
	once   sync.Once
	mu     sync.Mutex
	result Message
	closed bool
}

func NewAssistantMessageEventStream() *AssistantMessageEventStream {
	return &AssistantMessageEventStream{events: make(chan AssistantMessageEvent, 64), done: make(chan struct{})}
}

func CreateAssistantMessageEventStream() *AssistantMessageEventStream {
	return NewAssistantMessageEventStream()
}

func (s *AssistantMessageEventStream) Events() <-chan AssistantMessageEvent {
	return s.events
}

func (s *AssistantMessageEventStream) Push(event AssistantMessageEvent) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	if event.Type == "done" && event.Message != nil {
		s.result = *event.Message
		s.events <- event
		s.closeLocked()
		s.mu.Unlock()
		return
	}
	if event.Type == "error" && event.Error != nil {
		s.result = *event.Error
		s.events <- event
		s.closeLocked()
		s.mu.Unlock()
		return
	}
	s.events <- event
	s.mu.Unlock()
}

func (s *AssistantMessageEventStream) End() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closeLocked()
}

func (s *AssistantMessageEventStream) closeLocked() {
	s.once.Do(func() {
		s.closed = true
		close(s.done)
		close(s.events)
	})
}

func (s *AssistantMessageEventStream) Result() Message {
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

func EmptyUsage() Usage {
	return Usage{Cost: UsageCost{}}
}

func CalculateCost(model Model, usage *Usage) {
	usage.Cost.Input = float64(usage.Input) * model.Cost.Input / 1_000_000
	usage.Cost.Output = float64(usage.Output) * model.Cost.Output / 1_000_000
	usage.Cost.CacheRead = float64(usage.CacheRead) * model.Cost.CacheRead / 1_000_000
	usage.Cost.CacheWrite = float64(usage.CacheWrite) * model.Cost.CacheWrite / 1_000_000
	usage.Cost.Total = usage.Cost.Input + usage.Cost.Output + usage.Cost.CacheRead + usage.Cost.CacheWrite
}
