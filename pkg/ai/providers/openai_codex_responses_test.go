package providers

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/coder/websocket"

	ai "github.com/beeper/ai-bridge/pkg/ai"
)

func TestBuildCodexRequestBodyMatchesBackendShape(t *testing.T) {
	temp := 0.7
	reasoning := ai.ThinkingLevelHigh
	summary := "concise"
	verbosity := "medium"
	model := ai.Model{
		ID:        "gpt-5-codex",
		API:       ai.ApiOpenAICodexResponses,
		Provider:  ai.ProviderOpenAICodex,
		Reasoning: true,
		Input:     []string{"text"},
	}
	body := BuildCodexRequestBody(model, ai.Context{
		SystemPrompt: "system",
		Messages:     []ai.Message{{Role: "user", Content: "hi"}},
		Tools:        []ai.Tool{{Name: "read", Description: "read", Parameters: map[string]any{"type": "object"}}},
	}, OpenAICodexResponsesOptions{
		OpenAIResponsesOptions: OpenAIResponsesOptions{
			StreamOptions:   ai.StreamOptions{Temperature: &temp, SessionID: strings.Repeat("x", 80)},
			ReasoningEffort: &reasoning,
		},
		ReasoningSummary: &summary,
		ServiceTier:      "priority",
		TextVerbosity:    &verbosity,
	})

	if body["instructions"] != "system" {
		t.Fatalf("expected system prompt as instructions, got %#v", body["instructions"])
	}
	if body["prompt_cache_key"] != strings.Repeat("x", 64) {
		t.Fatalf("expected clamped prompt cache key, got %#v", body["prompt_cache_key"])
	}
	if input := body["input"].([]map[string]any); len(input) != 1 || input[0]["role"] != "user" {
		t.Fatalf("expected input without system prompt, got %#v", body["input"])
	}
	if text := body["text"].(map[string]any); text["verbosity"] != "medium" {
		t.Fatalf("expected text verbosity medium, got %#v", text)
	}
	if reasoningPayload := body["reasoning"].(map[string]any); reasoningPayload["effort"] != "high" || reasoningPayload["summary"] != "concise" {
		t.Fatalf("unexpected reasoning payload %#v", reasoningPayload)
	}
	if tools := body["tools"].([]map[string]any); len(tools) != 1 || tools[0]["strict"] != nil {
		t.Fatalf("expected Codex tool strict null, got %#v", body["tools"])
	}
	if body["temperature"] != temp || body["service_tier"] != "priority" {
		t.Fatalf("expected temperature and service tier, got %#v", body)
	}
}

func TestBuildCodexRequestBodyDefaults(t *testing.T) {
	body := BuildCodexRequestBody(ai.Model{ID: "gpt-5-codex", API: ai.ApiOpenAICodexResponses, Provider: ai.ProviderOpenAICodex}, ai.Context{}, OpenAICodexResponsesOptions{})
	if body["instructions"] != "You are a helpful assistant." {
		t.Fatalf("expected default instructions, got %#v", body["instructions"])
	}
	if text := body["text"].(map[string]any); text["verbosity"] != "low" {
		t.Fatalf("expected low default verbosity, got %#v", text)
	}
	if _, ok := body["prompt_cache_key"]; ok {
		t.Fatalf("expected no empty prompt cache key, got %#v", body["prompt_cache_key"])
	}
}

func TestCodexURLResolution(t *testing.T) {
	if got := ResolveCodexURL(""); got != "https://chatgpt.com/backend-api/codex/responses" {
		t.Fatalf("unexpected default url %q", got)
	}
	if got := ResolveCodexURL("https://example.com/backend-api/codex"); got != "https://example.com/backend-api/codex/responses" {
		t.Fatalf("unexpected codex url %q", got)
	}
	if got := ResolveCodexWebSocketURL("https://example.com/backend-api"); got != "wss://example.com/backend-api/codex/responses" {
		t.Fatalf("unexpected websocket url %q", got)
	}
}

func TestCodexAccountAndHeaders(t *testing.T) {
	claims := `{"https://api.openai.com/auth":{"chatgpt_account_id":"acct_123"}}`
	token := "header." + base64.RawURLEncoding.EncodeToString([]byte(claims)) + ".sig"
	accountID, err := ExtractCodexAccountID(token)
	if err != nil {
		t.Fatal(err)
	}
	if accountID != "acct_123" {
		t.Fatalf("expected account id, got %q", accountID)
	}
	headers := BuildCodexSSEHeaders(map[string]string{"x-base": "1"}, map[string]string{"x-extra": "2"}, accountID, token, "session-1")
	if headers["Authorization"] != "Bearer "+token || headers["chatgpt-account-id"] != "acct_123" || headers["OpenAI-Beta"] != "responses=experimental" {
		t.Fatalf("unexpected sse headers %#v", headers)
	}
	if headers["session_id"] != "session-1" || headers["x-client-request-id"] != "session-1" {
		t.Fatalf("expected session headers, got %#v", headers)
	}
	wsHeaders := BuildCodexWebSocketHeaders(headers, nil, accountID, token, "request-1")
	if wsHeaders["OpenAI-Beta"] != openAIBetaResponsesWebSockets || wsHeaders["session_id"] != "request-1" {
		t.Fatalf("unexpected websocket headers %#v", wsHeaders)
	}
	if _, ok := wsHeaders["accept"]; ok {
		t.Fatalf("websocket headers should delete accept, got %#v", wsHeaders)
	}
}

func TestCodexServiceTierPricing(t *testing.T) {
	usage := ai.Usage{Cost: ai.UsageCost{Input: 1, Output: 2, CacheRead: 3, CacheWrite: 4, Total: 10}}
	ApplyCodexServiceTierPricing(&usage, "priority", ai.Model{ID: "gpt-5-codex"})
	if usage.Cost.Total != 20 || usage.Cost.Input != 2 || usage.Cost.Output != 4 {
		t.Fatalf("expected priority multiplier, got %#v", usage.Cost)
	}
	if got := ResolveCodexServiceTier("default", "flex"); got != "flex" {
		t.Fatalf("expected flex override, got %q", got)
	}
}

func TestStreamOpenAICodexResponsesUsesCodexSSEBackend(t *testing.T) {
	token := codexTestToken()
	var requestPath string
	var requestBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestPath = r.URL.Path
		if r.Header.Get("Authorization") != "Bearer "+token {
			t.Fatalf("unexpected auth header %q", r.Header.Get("Authorization"))
		}
		if r.Header.Get("chatgpt-account-id") != "acct_123" {
			t.Fatalf("unexpected account header %q", r.Header.Get("chatgpt-account-id"))
		}
		if r.Header.Get("OpenAI-Beta") != "responses=experimental" {
			t.Fatalf("unexpected beta header %q", r.Header.Get("OpenAI-Beta"))
		}
		if err := json.NewDecoder(r.Body).Decode(&requestBody); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("content-type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"response.output_item.added\",\"item\":{\"type\":\"message\",\"id\":\"msg_1\"}}\n\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"response.output_text.delta\",\"delta\":\"hello\"}\n\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"response.output_item.done\",\"item\":{\"type\":\"message\",\"id\":\"msg_1\",\"content\":[{\"type\":\"output_text\",\"text\":\"hello\"}]}}\n\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"response.done\",\"response\":{\"id\":\"resp_1\",\"status\":\"completed\",\"usage\":{\"input_tokens\":2,\"output_tokens\":3,\"total_tokens\":5}}}\n\n"))
	}))
	defer server.Close()

	stream := StreamOpenAICodexResponses(t.Context(), ai.Model{
		ID:       "gpt-5-codex",
		API:      ai.ApiOpenAICodexResponses,
		Provider: ai.ProviderOpenAICodex,
		BaseURL:  server.URL,
		Input:    []string{"text"},
	}, ai.Context{SystemPrompt: "system", Messages: []ai.Message{{Role: "user", Content: "hi"}}}, OpenAICodexResponsesOptions{
		OpenAIResponsesOptions: OpenAIResponsesOptions{StreamOptions: ai.StreamOptions{APIKey: token, SessionID: "session-1", Transport: ai.TransportSSE}},
	})

	var deltas []string
	for event := range stream.Events() {
		if event.Type == "text_delta" {
			deltas = append(deltas, event.Delta)
		}
	}
	result := stream.Result()
	if result.StopReason != ai.StopReasonStop {
		t.Fatalf("expected stop, got %#v", result)
	}
	if strings.Join(deltas, "") != "hello" {
		t.Fatalf("expected streamed text delta, got %#v", deltas)
	}
	if requestPath != "/codex/responses" {
		t.Fatalf("expected codex backend path, got %q", requestPath)
	}
	if requestBody["instructions"] != "system" {
		t.Fatalf("expected instructions in request, got %#v", requestBody)
	}
	if input := requestBody["input"].([]any); len(input) != 1 {
		t.Fatalf("expected system prompt excluded from input, got %#v", requestBody["input"])
	}
	blocks := contentBlocks(result.Content)
	if len(blocks) != 1 || blocks[0].Text != "hello" {
		t.Fatalf("expected streamed text block, got %#v", blocks)
	}
	if result.ResponseID != "resp_1" || result.Usage.Input != 2 || result.Usage.Output != 3 {
		t.Fatalf("expected response id and usage, got %#v", result)
	}
}

func TestStreamOpenAICodexResponsesEmitsErrorForFailedSSE(t *testing.T) {
	token := codexTestToken()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("content-type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"response.failed\",\"response\":{\"error\":{\"code\":\"bad_request\",\"message\":\"nope\"}}}\n\n"))
	}))
	defer server.Close()

	stream := StreamOpenAICodexResponses(t.Context(), ai.Model{
		ID:       "gpt-5-codex",
		API:      ai.ApiOpenAICodexResponses,
		Provider: ai.ProviderOpenAICodex,
		BaseURL:  server.URL,
		Input:    []string{"text"},
	}, ai.Context{}, OpenAICodexResponsesOptions{
		OpenAIResponsesOptions: OpenAIResponsesOptions{StreamOptions: ai.StreamOptions{APIKey: token, Transport: ai.TransportSSE}},
	})

	var errorEvent *ai.AssistantMessageEvent
	for event := range stream.Events() {
		if event.Type == "error" {
			copied := event
			errorEvent = &copied
		}
	}
	result := stream.Result()
	if errorEvent == nil || errorEvent.Error == nil {
		t.Fatalf("expected error event, got result %#v", result)
	}
	if result.StopReason != ai.StopReasonError || result.ErrorMessage != "bad_request: nope" {
		t.Fatalf("expected failed response result, got %#v", result)
	}
}

func TestOpenAICodexWebSocketDebugStats(t *testing.T) {
	ResetOpenAICodexWebSocketDebugStats()
	if _, ok := GetOpenAICodexWebSocketDebugStats("session-1"); ok {
		t.Fatal("expected no stats after reset")
	}
	recordCodexWebSocketFailure("session-1", errors.New("boom"))
	recordCodexSSEFallback("session-1")
	stats, ok := GetOpenAICodexWebSocketDebugStats("session-1")
	if !ok {
		t.Fatal("expected stats")
	}
	if stats.WebSocketFailures != 1 || stats.SSEFallbacks != 1 || stats.WebSocketFallbackActive == nil || !*stats.WebSocketFallbackActive {
		t.Fatalf("unexpected stats %#v", stats)
	}
	if stats.LastWebSocketError != "boom" {
		t.Fatalf("expected last error, got %q", stats.LastWebSocketError)
	}
	ResetOpenAICodexWebSocketDebugStats("session-1")
	if _, ok := GetOpenAICodexWebSocketDebugStats("session-1"); ok {
		t.Fatal("expected session stats reset")
	}
}

func TestStreamOpenAICodexResponsesRecordsAutoTransportFallback(t *testing.T) {
	token := codexTestToken()
	ResetOpenAICodexWebSocketDebugStats()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("content-type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"response.done\",\"response\":{\"id\":\"resp_1\",\"status\":\"completed\",\"usage\":{\"input_tokens\":0,\"output_tokens\":0,\"total_tokens\":0}}}\n\n"))
	}))
	defer server.Close()

	stream := StreamOpenAICodexResponses(t.Context(), ai.Model{
		ID:       "gpt-5-codex",
		API:      ai.ApiOpenAICodexResponses,
		Provider: ai.ProviderOpenAICodex,
		BaseURL:  server.URL,
		Input:    []string{"text"},
	}, ai.Context{}, OpenAICodexResponsesOptions{
		OpenAIResponsesOptions: OpenAIResponsesOptions{StreamOptions: ai.StreamOptions{APIKey: token, SessionID: "session-1", Transport: ai.TransportAuto}},
	})
	result := stream.Result()
	if result.StopReason != ai.StopReasonStop {
		t.Fatalf("expected stop, got %#v", result)
	}
	stats, ok := GetOpenAICodexWebSocketDebugStats("session-1")
	if !ok {
		t.Fatal("expected fallback stats")
	}
	if stats.WebSocketFailures != 1 || stats.SSEFallbacks != 1 || stats.WebSocketFallbackActive == nil || !*stats.WebSocketFallbackActive {
		t.Fatalf("unexpected fallback stats %#v", stats)
	}

	second := StreamOpenAICodexResponses(t.Context(), ai.Model{
		ID:       "gpt-5-codex",
		API:      ai.ApiOpenAICodexResponses,
		Provider: ai.ProviderOpenAICodex,
		BaseURL:  server.URL,
		Input:    []string{"text"},
	}, ai.Context{}, OpenAICodexResponsesOptions{
		OpenAIResponsesOptions: OpenAIResponsesOptions{StreamOptions: ai.StreamOptions{APIKey: token, SessionID: "session-1", Transport: ai.TransportAuto}},
	}).Result()
	if second.StopReason != ai.StopReasonStop {
		t.Fatalf("expected second stop, got %#v", second)
	}
	stats, ok = GetOpenAICodexWebSocketDebugStats("session-1")
	if !ok {
		t.Fatal("expected fallback stats after second request")
	}
	if stats.WebSocketFailures != 1 || stats.SSEFallbacks != 2 {
		t.Fatalf("expected active fallback to increment sse fallback count without another websocket failure, got %#v", stats)
	}
}

func TestStreamOpenAICodexResponsesDoesNotFallbackForWebSocketAPIError(t *testing.T) {
	token := codexTestToken()
	ResetOpenAICodexWebSocketDebugStats()
	CloseOpenAICodexWebSocketSessions()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Errorf("accept websocket: %v", err)
			return
		}
		defer conn.Close(websocket.StatusNormalClosure, "done")
		_, _, err = conn.Read(r.Context())
		if err != nil {
			t.Errorf("read websocket: %v", err)
			return
		}
		writeCodexWSEvent(t, r.Context(), conn, map[string]any{"type": "response.failed", "response": map[string]any{"error": map[string]any{"code": "bad_request", "message": "nope"}}})
	}))
	defer server.Close()
	defer CloseOpenAICodexWebSocketSessions("session-api-error")

	result := StreamOpenAICodexResponses(t.Context(), ai.Model{
		ID:       "gpt-5-codex",
		API:      ai.ApiOpenAICodexResponses,
		Provider: ai.ProviderOpenAICodex,
		BaseURL:  server.URL,
		Input:    []string{"text"},
	}, ai.Context{}, OpenAICodexResponsesOptions{
		OpenAIResponsesOptions: OpenAIResponsesOptions{StreamOptions: ai.StreamOptions{APIKey: token, SessionID: "session-api-error", Transport: ai.TransportAuto}},
	}).Result()
	if result.StopReason != ai.StopReasonError || result.ErrorMessage != "bad_request: nope" {
		t.Fatalf("expected websocket api error, got %#v", result)
	}
	stats, ok := GetOpenAICodexWebSocketDebugStats("session-api-error")
	if !ok {
		t.Fatal("expected websocket request stats")
	}
	if stats.WebSocketFailures != 0 || stats.SSEFallbacks != 0 || stats.WebSocketFallbackActive != nil {
		t.Fatalf("expected no transport fallback for api error, got %#v", stats)
	}
}

func TestStreamOpenAICodexResponsesUsesCachedWebSocketDelta(t *testing.T) {
	token := codexTestToken()
	ResetOpenAICodexWebSocketDebugStats()
	CloseOpenAICodexWebSocketSessions()
	requests := make(chan map[string]any, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Errorf("accept websocket: %v", err)
			return
		}
		defer conn.Close(websocket.StatusNormalClosure, "done")
		for i := 0; i < 2; i++ {
			_, raw, err := conn.Read(r.Context())
			if err != nil {
				t.Errorf("read websocket: %v", err)
				return
			}
			var request map[string]any
			if err := json.Unmarshal(raw, &request); err != nil {
				t.Errorf("decode request: %v", err)
				return
			}
			requests <- request
			msgID := "msg_1"
			respID := "resp_1"
			text := "hello"
			if i == 1 {
				msgID = "msg_2"
				respID = "resp_2"
				text = "again"
			}
			writeCodexWSEvent(t, r.Context(), conn, map[string]any{"type": "response.output_item.added", "item": map[string]any{"type": "message", "id": msgID}})
			writeCodexWSEvent(t, r.Context(), conn, map[string]any{"type": "response.output_text.delta", "delta": text})
			writeCodexWSEvent(t, r.Context(), conn, map[string]any{"type": "response.output_item.done", "item": map[string]any{"type": "message", "id": msgID, "content": []map[string]any{{"type": "output_text", "text": text}}}})
			writeCodexWSEvent(t, r.Context(), conn, map[string]any{"type": "response.done", "response": map[string]any{"id": respID, "status": "completed", "usage": map[string]any{"input_tokens": 1, "output_tokens": 1, "total_tokens": 2}}})
		}
	}))
	defer server.Close()
	defer CloseOpenAICodexWebSocketSessions("session-1")

	model := ai.Model{ID: "gpt-5-codex", API: ai.ApiOpenAICodexResponses, Provider: ai.ProviderOpenAICodex, BaseURL: server.URL, Input: []string{"text"}}
	options := OpenAICodexResponsesOptions{OpenAIResponsesOptions: OpenAIResponsesOptions{StreamOptions: ai.StreamOptions{APIKey: token, SessionID: "session-1", Transport: ai.TransportAuto}}}
	first := StreamOpenAICodexResponses(t.Context(), model, ai.Context{Messages: []ai.Message{{Role: "user", Content: "one"}}}, options).Result()
	if first.StopReason != ai.StopReasonStop {
		t.Fatalf("expected first stop, got %#v", first)
	}
	second := StreamOpenAICodexResponses(t.Context(), model, ai.Context{Messages: []ai.Message{{Role: "user", Content: "one"}, first, {Role: "user", Content: "two"}}}, options).Result()
	if second.StopReason != ai.StopReasonStop {
		t.Fatalf("expected second stop, got %#v", second)
	}

	firstRequest := <-requests
	secondRequest := <-requests
	if firstRequest["type"] != "response.create" {
		t.Fatalf("expected response.create, got %#v", firstRequest)
	}
	if _, ok := firstRequest["previous_response_id"]; ok {
		t.Fatalf("first request should not have previous_response_id, got %#v", firstRequest)
	}
	if secondRequest["previous_response_id"] != "resp_1" {
		t.Fatalf("expected cached previous response id, got %#v", secondRequest)
	}
	if input := secondRequest["input"].([]any); len(input) != 1 {
		t.Fatalf("expected one delta input item, got %#v", secondRequest["input"])
	}
	stats, ok := GetOpenAICodexWebSocketDebugStats("session-1")
	if !ok {
		t.Fatal("expected websocket stats")
	}
	if stats.ConnectionsCreated != 1 || stats.ConnectionsReused != 1 || stats.DeltaRequests != 1 || stats.FullContextRequests != 1 {
		t.Fatalf("unexpected websocket stats %#v", stats)
	}
}

func TestCleanupSessionResourcesClosesCodexWebSocketSession(t *testing.T) {
	token := codexTestToken()
	ResetOpenAICodexWebSocketDebugStats()
	CloseOpenAICodexWebSocketSessions()
	requests := make(chan map[string]any, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Errorf("accept websocket: %v", err)
			return
		}
		defer conn.Close(websocket.StatusNormalClosure, "done")
		_, raw, err := conn.Read(r.Context())
		if err != nil {
			t.Errorf("read websocket: %v", err)
			return
		}
		var request map[string]any
		if err := json.Unmarshal(raw, &request); err != nil {
			t.Errorf("decode request: %v", err)
			return
		}
		requests <- request
		writeCodexWSEvent(t, r.Context(), conn, map[string]any{"type": "response.done", "response": map[string]any{"id": "resp", "status": "completed", "usage": map[string]any{"input_tokens": 0, "output_tokens": 0, "total_tokens": 0}}})
	}))
	defer server.Close()
	defer CloseOpenAICodexWebSocketSessions("session-cleanup")

	model := ai.Model{ID: "gpt-5-codex", API: ai.ApiOpenAICodexResponses, Provider: ai.ProviderOpenAICodex, BaseURL: server.URL, Input: []string{"text"}}
	options := OpenAICodexResponsesOptions{OpenAIResponsesOptions: OpenAIResponsesOptions{StreamOptions: ai.StreamOptions{APIKey: token, SessionID: "session-cleanup", Transport: ai.TransportAuto}}}
	if result := StreamOpenAICodexResponses(t.Context(), model, ai.Context{}, options).Result(); result.StopReason != ai.StopReasonStop {
		t.Fatalf("expected first stop, got %#v", result)
	}
	if err := ai.CleanupSessionResources("session-cleanup"); err != nil {
		t.Fatal(err)
	}
	if result := StreamOpenAICodexResponses(t.Context(), model, ai.Context{}, options).Result(); result.StopReason != ai.StopReasonStop {
		t.Fatalf("expected second stop, got %#v", result)
	}
	<-requests
	<-requests
	stats, ok := GetOpenAICodexWebSocketDebugStats("session-cleanup")
	if !ok {
		t.Fatal("expected stats")
	}
	if stats.ConnectionsCreated != 2 || stats.ConnectionsReused != 0 {
		t.Fatalf("expected cleanup to force a new connection, got %#v", stats)
	}
}

func TestBuildCachedCodexWebSocketRequestBodyDelta(t *testing.T) {
	firstUser := map[string]any{"role": "user", "content": []map[string]any{{"type": "input_text", "text": "one"}}}
	assistant := map[string]any{"type": "message", "role": "assistant", "id": "msg_1"}
	secondUser := map[string]any{"role": "user", "content": []map[string]any{{"type": "input_text", "text": "two"}}}
	base := map[string]any{
		"model": "gpt-5-codex",
		"store": false,
		"input": []map[string]any{firstUser},
	}
	current := map[string]any{
		"model": "gpt-5-codex",
		"store": false,
		"input": []map[string]any{firstUser, assistant, secondUser},
	}
	entry := &CachedCodexWebSocketConnection{Continuation: &CachedCodexWebSocketContinuationState{
		LastRequestBody:   base,
		LastResponseID:    "resp_1",
		LastResponseItems: []map[string]any{assistant},
	}}

	next := BuildCachedCodexWebSocketRequestBody(entry, current)
	if next["previous_response_id"] != "resp_1" {
		t.Fatalf("expected previous response id, got %#v", next)
	}
	delta := next["input"].([]map[string]any)
	if len(delta) != 1 || delta[0]["role"] != "user" {
		t.Fatalf("expected one delta user item, got %#v", delta)
	}
	if _, ok := current["previous_response_id"]; ok {
		t.Fatalf("expected original request body not to be mutated, got %#v", current)
	}
}

func TestBuildCachedCodexWebSocketRequestBodyInvalidatesOnMismatch(t *testing.T) {
	entry := &CachedCodexWebSocketConnection{Continuation: &CachedCodexWebSocketContinuationState{
		LastRequestBody: map[string]any{"model": "gpt-5-codex", "input": []map[string]any{{"role": "user", "content": "one"}}},
		LastResponseID:  "resp_1",
	}}
	current := map[string]any{"model": "gpt-other", "input": []map[string]any{{"role": "user", "content": "one"}}}
	next := BuildCachedCodexWebSocketRequestBody(entry, current)
	next["sentinel"] = "mutated"
	if current["sentinel"] != "mutated" {
		t.Fatalf("expected original body on mismatch")
	}
	if entry.Continuation != nil {
		t.Fatalf("expected continuation invalidated")
	}
}

func TestCodexWebSocketErrorClearsContinuation(t *testing.T) {
	token := codexTestToken()
	entry := &CachedCodexWebSocketConnection{Continuation: &CachedCodexWebSocketContinuationState{
		LastRequestBody: map[string]any{"model": "gpt-5-codex"},
		LastResponseID:  "resp_1",
	}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close(websocket.StatusNormalClosure, "done")
		_, _, err = conn.Read(r.Context())
		if err != nil {
			t.Fatal(err)
		}
		if err := conn.Write(r.Context(), websocket.MessageText, []byte(`{"type":"response.failed","response":{"error":{"code":"bad_request","message":"nope"}}}`)); err != nil {
			t.Fatal(err)
		}
	}))
	defer server.Close()

	endpoint := "ws" + strings.TrimPrefix(server.URL, "http")
	conn, _, err := websocket.Dial(t.Context(), endpoint, nil)
	if err != nil {
		t.Fatal(err)
	}
	entry.Conn = conn
	model := ai.Model{ID: "gpt-5-codex", API: ai.ApiOpenAICodexResponses, Provider: ai.ProviderOpenAICodex, Input: []string{"text"}}
	stream := ai.NewAssistantMessageEventStream()
	output := newAssistant(model)
	codexWebSocketDebugMu.Lock()
	codexWebSocketSessionCache["session-clear"] = entry
	codexWebSocketDebugMu.Unlock()
	defer CloseOpenAICodexWebSocketSessions("session-clear")

	err = processCodexWebSocketStream(t.Context(), endpoint, map[string]any{"model": "gpt-5-codex", "stream": true}, BuildCodexWebSocketHeaders(nil, nil, "acct_123", token, "session-clear"), &output, stream, model, func() {}, OpenAICodexResponsesOptions{
		OpenAIResponsesOptions: OpenAIResponsesOptions{StreamOptions: ai.StreamOptions{APIKey: token, SessionID: "session-clear", Transport: ai.TransportWebSocketCached}},
	})
	if err == nil || err.Error() != "bad_request: nope" {
		t.Fatalf("expected websocket processing error, got %v", err)
	}
	if entry.Continuation != nil {
		t.Fatalf("expected continuation to be cleared on websocket error")
	}
}

func writeCodexWSEvent(t *testing.T, ctx context.Context, conn *websocket.Conn, event map[string]any) {
	t.Helper()
	raw, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	if err := conn.Write(ctx, websocket.MessageText, raw); err != nil {
		t.Fatal(err)
	}
}

func codexTestToken() string {
	claims := `{"https://api.openai.com/auth":{"chatgpt_account_id":"acct_123"}}`
	return "header." + base64.RawURLEncoding.EncodeToString([]byte(claims)) + ".sig"
}
