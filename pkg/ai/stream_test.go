package ai

import (
	"context"
	"testing"
	"time"
)

func TestStreamSimpleDispatchesRegisteredProvider(t *testing.T) {
	ClearAPIProviders()
	defer ClearAPIProviders()
	RegisterAPIProvider(Api("test-api"), func(ctx context.Context, model Model, llmContext Context, options SimpleStreamOptions) *AssistantMessageEventStream {
		stream := NewAssistantMessageEventStream()
		go func() {
			message := Message{Role: "assistant", Content: []ContentBlock{{Type: "text", Text: "ok"}}, API: model.API, Provider: model.Provider, Model: model.ID, StopReason: StopReasonStop}
			stream.Push(AssistantMessageEvent{Type: "done", Reason: StopReasonStop, Message: &message})
		}()
		return stream
	})
	result := CompleteSimple(context.Background(), Model{ID: "m", API: "test-api", Provider: "p"}, Context{}, SimpleStreamOptions{})
	if result.Role != "assistant" || result.StopReason != StopReasonStop {
		t.Fatalf("unexpected result %#v", result)
	}
}

func TestStreamDispatchesFullProviderWithStreamOptions(t *testing.T) {
	ClearAPIProviders()
	defer ClearAPIProviders()
	reasoning := ThinkingLevelHigh
	RegisterAPIProviderWithSource(APIProvider{
		API: "test-api",
		Stream: func(ctx context.Context, model Model, llmContext Context, options StreamOptions) *AssistantMessageEventStream {
			if options.MaxTokens == nil || *options.MaxTokens != 123 {
				t.Fatalf("expected stream options, got %#v", options)
			}
			stream := NewAssistantMessageEventStream()
			go func() {
				message := Message{Role: "assistant", Content: []ContentBlock{{Type: "text", Text: "full"}}, API: model.API, Provider: model.Provider, Model: model.ID, StopReason: StopReasonStop}
				stream.Push(AssistantMessageEvent{Type: "done", Reason: StopReasonStop, Message: &message})
			}()
			return stream
		},
		StreamSimple: func(ctx context.Context, model Model, llmContext Context, options SimpleStreamOptions) *AssistantMessageEventStream {
			if options.Reasoning == nil || *options.Reasoning != reasoning {
				t.Fatalf("expected simple reasoning, got %#v", options.Reasoning)
			}
			stream := NewAssistantMessageEventStream()
			go func() {
				message := Message{Role: "assistant", Content: []ContentBlock{{Type: "text", Text: "simple"}}, API: model.API, Provider: model.Provider, Model: model.ID, StopReason: StopReasonStop}
				stream.Push(AssistantMessageEvent{Type: "done", Reason: StopReasonStop, Message: &message})
			}()
			return stream
		},
	}, "test")
	maxTokens := 123
	full := Complete(context.Background(), Model{ID: "m", API: "test-api", Provider: "p"}, Context{}, StreamOptions{MaxTokens: &maxTokens})
	if full.Content.([]ContentBlock)[0].Text != "full" {
		t.Fatalf("unexpected full stream result %#v", full)
	}
	simple := CompleteSimple(context.Background(), Model{ID: "m", API: "test-api", Provider: "p"}, Context{}, SimpleStreamOptions{Reasoning: &reasoning})
	if simple.Content.([]ContentBlock)[0].Text != "simple" {
		t.Fatalf("unexpected simple stream result %#v", simple)
	}
}

func TestStreamSimpleUnregisteredProviderPanics(t *testing.T) {
	ClearAPIProviders()
	assertPanicMessage(t, "No API provider registered for api: missing", func() {
		CompleteSimple(context.Background(), Model{ID: "m", API: "missing", Provider: "p"}, Context{}, SimpleStreamOptions{})
	})
}

func TestCreateAssistantMessageEventStreamFactory(t *testing.T) {
	stream := CreateAssistantMessageEventStream()
	message := Message{Role: "assistant", StopReason: StopReasonStop}
	stream.Push(AssistantMessageEvent{Type: "done", Reason: StopReasonStop, Message: &message})
	if result := stream.Result(); result.Role != "assistant" || result.StopReason != StopReasonStop {
		t.Fatalf("unexpected result %#v", result)
	}
}

func TestAssistantMessageEventStreamResultDrainsUndeliveredEvents(t *testing.T) {
	stream := NewAssistantMessageEventStream()
	go func() {
		for i := 0; i < 128; i++ {
			stream.Push(AssistantMessageEvent{Type: "text_delta", ContentIndex: 0, Delta: "x"})
		}
		message := Message{Role: "assistant", StopReason: StopReasonStop}
		stream.Push(AssistantMessageEvent{Type: "done", Reason: StopReasonStop, Message: &message})
	}()

	resultCh := make(chan Message, 1)
	go func() {
		resultCh <- stream.Result()
	}()
	select {
	case result := <-resultCh:
		if result.Role != "assistant" || result.StopReason != StopReasonStop {
			t.Fatalf("unexpected result %#v", result)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Result deadlocked without an event consumer")
	}
}

func TestAssistantMessageEventStreamIgnoresPushAfterEnd(t *testing.T) {
	stream := NewAssistantMessageEventStream()
	stream.End()
	message := Message{Role: "assistant", StopReason: StopReasonStop}
	stream.Push(AssistantMessageEvent{Type: "done", Reason: StopReasonStop, Message: &message})
	if result := stream.Result(); result.Role != "" {
		t.Fatalf("expected empty result after manual end, got %#v", result)
	}
}

func TestAPIProviderRegistryTracksSourceAndMismatchedAPI(t *testing.T) {
	ClearAPIProviders()
	defer ClearAPIProviders()
	RegisterAPIProviderWithSource(APIProvider{
		API: "source-api",
		StreamSimple: func(ctx context.Context, model Model, llmContext Context, options SimpleStreamOptions) *AssistantMessageEventStream {
			stream := NewAssistantMessageEventStream()
			go func() {
				message := Message{Role: "assistant", API: model.API, Provider: model.Provider, Model: model.ID, StopReason: StopReasonStop}
				stream.Push(AssistantMessageEvent{Type: "done", Reason: StopReasonStop, Message: &message})
			}()
			return stream
		},
	}, "plugin-1")
	if providers := GetAPIProviders(); len(providers) != 1 || providers[0].API != "source-api" {
		t.Fatalf("expected registered provider list, got %#v", providers)
	}
	provider, ok := GetAPIProvider("source-api")
	if !ok {
		t.Fatal("expected provider")
	}
	assertPanicMessage(t, "Mismatched api: wrong-api expected source-api", func() {
		provider.StreamSimple(context.Background(), Model{ID: "m", API: "wrong-api", Provider: "p"}, Context{}, SimpleStreamOptions{})
	})
	UnregisterAPIProviders("plugin-1")
	if _, ok := GetAPIProvider("source-api"); ok {
		t.Fatal("expected source provider to be unregistered")
	}
}

func assertPanicMessage(t *testing.T, want string, fn func()) {
	t.Helper()
	defer func() {
		recovered := recover()
		if recovered != want {
			t.Fatalf("expected panic %q, got %#v", want, recovered)
		}
	}()
	fn()
}
