package chattools

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestGetSessionSchemaUsesArrayRequired(t *testing.T) {
	tool := GetSessionTool(SessionInfo{})
	raw, err := json.Marshal(tool.Tool.Parameters)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), `"required":null`) {
		t.Fatalf("required must be an array, got %s", string(raw))
	}
	required, ok := tool.Tool.Parameters["required"].([]string)
	if !ok || len(required) != 0 {
		t.Fatalf("expected empty required array, got %#v", tool.Tool.Parameters["required"])
	}
}

func TestGetSessionReturnsFreshMetadata(t *testing.T) {
	tool := GetSessionTool(SessionInfo{
		RoomTitle:       "Room",
		SessionID:       "session-1",
		LoginID:         "login-1",
		ProviderID:      "beeper",
		ModelID:         "gpt-5",
		ReasoningLevel:  "low",
		DisabledTools:   []string{"web_search"},
		AttachmentCount: 1,
	})
	result, err := tool.Execute(context.Background(), "call", map[string]any{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	var info SessionInfo
	if err := json.Unmarshal([]byte(result.Content[0].Text), &info); err != nil {
		t.Fatal(err)
	}
	if info.Timestamp == "" || info.Timezone == "" || info.ThreadID != "session-1" {
		t.Fatalf("expected fresh session metadata, got %#v", info)
	}
}

func TestToolsOmitsDisabledSearch(t *testing.T) {
	tools := Tools(SessionInfo{}, FetchOptions{}, SearchOptions{Enabled: false})
	for _, tool := range tools {
		if tool.Tool.Name == "web_search" {
			t.Fatalf("web_search should not be exposed when disabled")
		}
	}
}

func TestFetch(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte("<html><title>Hello</title><body><script>drop</script><p>Visible text</p></body></html>"))
	}))
	defer server.Close()

	result, err := Fetch(context.Background(), server.URL, FetchOptions{Timeout: time.Second, MaxBytes: 1024, MaxChars: 100})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != 200 || result.Title != "Hello" || !strings.Contains(result.Text, "Visible text") || strings.Contains(result.Text, "drop") {
		t.Fatalf("unexpected fetch result %#v", result)
	}
}

func TestFetchRejectsUnsupportedScheme(t *testing.T) {
	if _, err := Fetch(context.Background(), "file:///etc/passwd", FetchOptions{}); err == nil {
		t.Fatalf("expected unsupported scheme error")
	}
}

func TestSearchUsesConfiguredEndpoint(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.Header.Get("Authorization") != "Bearer key" {
			t.Fatalf("unexpected request method/header")
		}
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		if payload["query"] != "query" || payload["numResults"] != float64(5) || payload["useAutoprompt"] != false {
			t.Fatalf("unexpected search payload %#v", payload)
		}
		_, _ = w.Write([]byte(`{"results":[{"title":"One","url":"https://example.com","text":"ok","publishedDate":"2026-01-01","siteName":"Example","author":"A","image":"https://example.com/image.png","favicon":"https://example.com/favicon.ico"}]}`))
	}))
	defer server.Close()

	result, err := Search(context.Background(), "query", 5, SearchOptions{Enabled: true, Endpoint: server.URL, APIKey: "key", Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if result.Query != "query" || len(result.Results) != 1 || result.Results[0].Title != "One" || result.Results[0].Snippet != "ok" {
		t.Fatalf("unexpected search result %#v", result)
	}
	if result.Results[0].Published != "2026-01-01" || result.Results[0].SiteName != "Example" || result.Results[0].Author != "A" {
		t.Fatalf("missing Exa metadata: %#v", result.Results[0])
	}
}

func TestSearchRequiresConfiguration(t *testing.T) {
	if _, err := Search(context.Background(), "query", 5, SearchOptions{}); err == nil {
		t.Fatalf("expected configuration error")
	}
}
