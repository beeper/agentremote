package chattools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	agent "github.com/beeper/ai-bridge/pkg/agent"
	ai "github.com/beeper/ai-bridge/pkg/ai"
)

type SessionInfo struct {
	Timestamp       string         `json:"timestamp"`
	Timezone        string         `json:"timezone"`
	RoomTitle       string         `json:"room_title,omitempty"`
	RoomID          string         `json:"room_id,omitempty"`
	SessionID       string         `json:"session_id,omitempty"`
	ThreadID        string         `json:"thread_id,omitempty"`
	LoginID         string         `json:"login_id,omitempty"`
	ProviderID      string         `json:"provider_id,omitempty"`
	ModelID         string         `json:"model_id,omitempty"`
	ReasoningLevel  string         `json:"reasoning_level,omitempty"`
	DisabledTools   []string       `json:"disabled_tools,omitempty"`
	AttachmentCount int            `json:"attachment_count"`
	Attachments     []Attachment   `json:"attachments,omitempty"`
	Extra           map[string]any `json:"extra,omitempty"`
}

type Attachment struct {
	Type     string `json:"type,omitempty"`
	MimeType string `json:"mime_type,omitempty"`
}

type FetchOptions struct {
	Timeout  time.Duration
	MaxBytes int64
	MaxChars int
	Client   *http.Client
}

type SearchOptions struct {
	Enabled  bool
	Endpoint string
	APIKey   string
	Timeout  time.Duration
	Client   *http.Client
}

func Tools(info SessionInfo, fetch FetchOptions, search SearchOptions) []agent.AgentTool[any] {
	tools := []agent.AgentTool[any]{
		GetSessionTool(info),
		FetchTool(fetch),
	}
	if search.Enabled {
		tools = append(tools, WebSearchTool(search))
	}
	return tools
}

func GetSessionTool(info SessionInfo) agent.AgentTool[any] {
	return agent.AgentTool[any]{
		Tool: ai.Tool{
			Name:        "get_session",
			Description: "Get fresh metadata for this Beeper AI chat, including current timestamp, timezone, room, session, model, reasoning, search, and attachments.",
			Parameters:  objectSchema(nil, nil),
		},
		Execute: func(ctx context.Context, toolCallID string, params any, onUpdate agent.AgentToolUpdateCallback[any]) (agent.AgentToolResult[any], error) {
			now := time.Now()
			current := info
			current.Timestamp = now.Format(time.RFC3339)
			current.Timezone = now.Location().String()
			if current.ThreadID == "" {
				current.ThreadID = current.SessionID
			}
			return jsonResult(current)
		},
	}
}

func FetchTool(options FetchOptions) agent.AgentTool[any] {
	return agent.AgentTool[any]{
		Tool: ai.Tool{
			Name:        "fetch",
			Description: "Fetch an HTTP or HTTPS URL and return status, final URL, content type, page title, and extracted text.",
			Parameters: objectSchema(map[string]any{
				"url": map[string]any{"type": "string", "description": "HTTP or HTTPS URL to fetch."},
			}, []string{"url"}),
		},
		Execute: func(ctx context.Context, toolCallID string, params any, onUpdate agent.AgentToolUpdateCallback[any]) (agent.AgentToolResult[any], error) {
			urlValue, err := stringParam(params, "url")
			if err != nil {
				return agent.AgentToolResult[any]{}, err
			}
			result, err := Fetch(ctx, urlValue, options)
			if err != nil {
				return agent.AgentToolResult[any]{}, err
			}
			return jsonResult(result)
		},
	}
}

type FetchResult struct {
	URL         string `json:"url"`
	FinalURL    string `json:"final_url"`
	Status      int    `json:"status"`
	ContentType string `json:"content_type,omitempty"`
	Title       string `json:"title,omitempty"`
	Text        string `json:"text,omitempty"`
	Truncated   bool   `json:"truncated"`
}

func Fetch(ctx context.Context, rawURL string, options FetchOptions) (FetchResult, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return FetchResult{}, fmt.Errorf("invalid URL")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return FetchResult{}, fmt.Errorf("unsupported URL scheme %s", parsed.Scheme)
	}
	if options.Timeout == 0 {
		options.Timeout = 10 * time.Second
	}
	if options.MaxBytes == 0 {
		options.MaxBytes = 2 * 1024 * 1024
	}
	if options.MaxChars == 0 {
		options.MaxChars = 20000
	}
	client := options.Client
	if client == nil {
		client = &http.Client{Timeout: options.Timeout}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return FetchResult{}, err
	}
	req.Header.Set("User-Agent", "beeper-ai-bridge/1.0")
	resp, err := client.Do(req)
	if err != nil {
		return FetchResult{}, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, options.MaxBytes+1))
	if err != nil {
		return FetchResult{}, err
	}
	truncated := int64(len(body)) > options.MaxBytes
	if truncated {
		body = body[:options.MaxBytes]
	}
	text := extractText(body, resp.Header.Get("Content-Type"))
	if len([]rune(text)) > options.MaxChars {
		runes := []rune(text)
		text = string(runes[:options.MaxChars])
		truncated = true
	}
	return FetchResult{
		URL:         rawURL,
		FinalURL:    resp.Request.URL.String(),
		Status:      resp.StatusCode,
		ContentType: resp.Header.Get("Content-Type"),
		Title:       extractTitle(body),
		Text:        text,
		Truncated:   truncated,
	}, nil
}

func WebSearchTool(options SearchOptions) agent.AgentTool[any] {
	return agent.AgentTool[any]{
		Tool: ai.Tool{
			Name:        "web_search",
			Description: "Search the web and return relevant results with title, URL, snippet, and source metadata.",
			Parameters: objectSchema(map[string]any{
				"query": map[string]any{"type": "string", "description": "Search query."},
				"limit": map[string]any{"type": "integer", "description": "Maximum number of results."},
			}, []string{"query"}),
		},
		Execute: func(ctx context.Context, toolCallID string, params any, onUpdate agent.AgentToolUpdateCallback[any]) (agent.AgentToolResult[any], error) {
			query, err := stringParam(params, "query")
			if err != nil {
				return agent.AgentToolResult[any]{}, err
			}
			limit := intParam(params, "limit", 5)
			result, err := Search(ctx, query, limit, options)
			if err != nil {
				return agent.AgentToolResult[any]{}, err
			}
			return jsonResult(result)
		},
	}
}

type SearchResult struct {
	Query   string       `json:"query"`
	Results []SearchItem `json:"results"`
}

type SearchItem struct {
	Title       string         `json:"title"`
	URL         string         `json:"url"`
	Snippet     string         `json:"snippet,omitempty"`
	Description string         `json:"description,omitempty"`
	Published   string         `json:"published,omitempty"`
	SiteName    string         `json:"siteName,omitempty"`
	Author      string         `json:"author,omitempty"`
	Image       string         `json:"image,omitempty"`
	Favicon     string         `json:"favicon,omitempty"`
	Source      string         `json:"source,omitempty"`
	Metadata    map[string]any `json:"metadata,omitempty"`
}

func Search(ctx context.Context, query string, limit int, options SearchOptions) (SearchResult, error) {
	if !options.Enabled || options.Endpoint == "" {
		return SearchResult{}, errors.New("web_search is not configured")
	}
	if limit <= 0 || limit > 10 {
		limit = 5
	}
	if options.Timeout == 0 {
		options.Timeout = 10 * time.Second
	}
	client := options.Client
	if client == nil {
		client = &http.Client{Timeout: options.Timeout}
	}
	payload, _ := json.Marshal(map[string]any{"query": query, "numResults": limit, "useAutoprompt": false})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, options.Endpoint, bytes.NewReader(payload))
	if err != nil {
		return SearchResult{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	if options.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+options.APIKey)
	}
	resp, err := client.Do(req)
	if err != nil {
		return SearchResult{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return SearchResult{}, fmt.Errorf("search failed with HTTP %d", resp.StatusCode)
	}
	var body searchResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1024*1024)).Decode(&body); err != nil {
		return SearchResult{}, err
	}
	result := body.result()
	if result.Query == "" {
		result.Query = query
	}
	if len(result.Results) > limit {
		result.Results = result.Results[:limit]
	}
	return result, nil
}

type searchResponse struct {
	Query   string               `json:"query"`
	Results []searchResponseItem `json:"results"`
}

type searchResponseItem struct {
	Title         string         `json:"title"`
	URL           string         `json:"url"`
	Snippet       string         `json:"snippet"`
	Text          string         `json:"text"`
	Description   string         `json:"description"`
	Published     string         `json:"published"`
	PublishedDate string         `json:"publishedDate"`
	SiteName      string         `json:"siteName"`
	Author        string         `json:"author"`
	Image         string         `json:"image"`
	Favicon       string         `json:"favicon"`
	Source        string         `json:"source"`
	Metadata      map[string]any `json:"metadata"`
}

func (body searchResponse) result() SearchResult {
	result := SearchResult{Query: body.Query, Results: make([]SearchItem, 0, len(body.Results))}
	for _, item := range body.Results {
		snippet := item.Snippet
		if snippet == "" {
			snippet = item.Text
		}
		published := item.Published
		if published == "" {
			published = item.PublishedDate
		}
		result.Results = append(result.Results, SearchItem{
			Title:       item.Title,
			URL:         item.URL,
			Snippet:     snippet,
			Description: item.Description,
			Published:   published,
			SiteName:    item.SiteName,
			Author:      item.Author,
			Image:       item.Image,
			Favicon:     item.Favicon,
			Source:      item.Source,
			Metadata:    item.Metadata,
		})
	}
	return result
}

func jsonResult(value any) (agent.AgentToolResult[any], error) {
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return agent.AgentToolResult[any]{}, err
	}
	return agent.AgentToolResult[any]{
		Content: []ai.ContentBlock{{Type: "text", Text: string(raw)}},
		Details: value,
	}, nil
}

func objectSchema(properties map[string]any, required []string) map[string]any {
	if properties == nil {
		properties = map[string]any{}
	}
	if required == nil {
		required = []string{}
	}
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties":           properties,
		"required":             required,
	}
}

func stringParam(params any, key string) (string, error) {
	values, ok := params.(map[string]any)
	if !ok {
		return "", fmt.Errorf("expected object arguments")
	}
	value, ok := values[key].(string)
	if !ok || strings.TrimSpace(value) == "" {
		return "", fmt.Errorf("missing %s", key)
	}
	return value, nil
}

func intParam(params any, key string, fallback int) int {
	values, ok := params.(map[string]any)
	if !ok {
		return fallback
	}
	switch value := values[key].(type) {
	case int:
		return value
	case float64:
		return int(value)
	default:
		return fallback
	}
}

var titleRE = regexp.MustCompile(`(?is)<title[^>]*>(.*?)</title>`)
var scriptStyleRE = regexp.MustCompile(`(?is)<script[^>]*>.*?</script>|<style[^>]*>.*?</style>`)
var tagRE = regexp.MustCompile(`(?is)<[^>]+>`)
var whitespaceRE = regexp.MustCompile(`\s+`)

func extractTitle(body []byte) string {
	match := titleRE.FindSubmatch(body)
	if len(match) < 2 {
		return ""
	}
	return cleanText(string(match[1]))
}

func extractText(body []byte, contentType string) string {
	raw := string(body)
	if strings.Contains(strings.ToLower(contentType), "html") || strings.Contains(raw, "<html") {
		raw = scriptStyleRE.ReplaceAllString(raw, " ")
		raw = tagRE.ReplaceAllString(raw, " ")
	}
	return cleanText(raw)
}

func cleanText(value string) string {
	value = strings.ReplaceAll(value, "\x00", "")
	value = whitespaceRE.ReplaceAllString(value, " ")
	return strings.TrimSpace(value)
}
