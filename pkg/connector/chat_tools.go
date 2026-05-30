package connector

import (
	"fmt"
	"net/url"
	"strings"
	"time"

	agent "github.com/beeper/ai-bridge/pkg/agent"
	ai "github.com/beeper/ai-bridge/pkg/ai"
	"github.com/beeper/ai-bridge/pkg/aiid"
	"github.com/beeper/ai-bridge/pkg/chattools"
	"github.com/beeper/ai-bridge/pkg/msgconv"
	"maunium.net/go/mautrix/bridgev2"
)

func (cl *Client) chatTools(msg *bridgev2.MatrixMessage, meta *aiid.PortalMetadata, roomConfig RoomConfig, provider aiid.ProviderConfig, model ai.Model, prompt msgconv.MatrixPrompt) []agent.AgentTool[any] {
	roomID := ""
	roomTitle := ""
	if msg != nil && msg.Portal != nil {
		roomID = string(msg.Portal.MXID)
		if msg.Portal.NameSet {
			roomTitle = msg.Portal.Name
		}
	}
	info := chattools.SessionInfo{
		RoomTitle:       roomTitle,
		RoomID:          roomID,
		SessionID:       meta.SessionID,
		ThreadID:        meta.SessionID,
		LoginID:         string(cl.UserLogin.ID),
		ProviderID:      provider.ID,
		ModelID:         model.ID,
		ReasoningLevel:  cl.reasoningLevelForModel(model, roomConfig),
		DisabledTools:   roomConfig.DisabledTools,
		AttachmentCount: len(prompt.Attachments),
	}
	for _, attachment := range prompt.Attachments {
		info.Attachments = append(info.Attachments, chattools.Attachment{Type: attachment.Type, MimeType: attachment.MimeType})
	}
	search := cl.searchOptions(roomConfig, provider)
	return chattools.Tools(info, chattools.FetchOptions{
		Timeout:  time.Duration(cl.Main.Config.Fetch.TimeoutMS) * time.Millisecond,
		MaxBytes: cl.Main.Config.Fetch.MaxBytes,
		MaxChars: cl.Main.Config.Fetch.MaxChars,
	}, search)
}

func (cl *Client) searchOptions(roomConfig RoomConfig, provider aiid.ProviderConfig) chattools.SearchOptions {
	if toolDisabled(roomConfig.DisabledTools, "web_search") || provider.ID != aiid.DefaultProvider || provider.BaseURL == "" {
		return chattools.SearchOptions{}
	}
	token, err := cl.defaultProviderBearerToken()
	if err != nil {
		return chattools.SearchOptions{}
	}
	endpoint, err := aiServicesExaSearchURL(provider.BaseURL)
	if err != nil {
		return chattools.SearchOptions{}
	}
	return chattools.SearchOptions{
		Enabled:  true,
		Endpoint: endpoint,
		APIKey:   token,
		Timeout:  10 * time.Second,
	}
}

func aiServicesExaSearchURL(proxyBaseURL string) (string, error) {
	parsed, err := url.Parse(strings.TrimRight(normalizeResponsesBaseURL(proxyBaseURL), "/"))
	if err != nil {
		return "", err
	}
	parsed.Path = strings.TrimRight(trimAIProxyProviderPath(parsed.Path), "/") + "/proxy/exa/v1/search"
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return parsed.String(), nil
}

func (cl *Client) systemPrompt(roomConfig RoomConfig) string {
	base := strings.TrimSpace(cl.Main.Config.DefaultSystemPrompt)
	room := strings.TrimSpace(roomConfig.AdditionalPrompt)
	switch {
	case base == "":
		return room
	case room == "":
		return base
	default:
		return base + "\n\n" + room
	}
}

func (cl *Client) configuredReasoningLevel(model ai.Model, roomConfig RoomConfig) string {
	if roomConfig.ThinkingLevel != "" {
		return roomConfig.ThinkingLevel
	}
	if model.DefaultThinkingLevel != "" {
		return string(model.DefaultThinkingLevel)
	}
	return cl.Main.Config.DefaultReasoningLevel
}

func (cl *Client) reasoningLevelForModel(model ai.Model, roomConfig RoomConfig) string {
	level := ai.ModelThinkingLevel(cl.configuredReasoningLevel(model, roomConfig))
	if roomConfig.ThinkingLevel == "" {
		level = clampRoomReasoningLevel(model, level)
	}
	return string(level)
}

func (cl *Client) validateReasoningLevel(model ai.Model, roomConfig RoomConfig) error {
	rawLevel := cl.configuredReasoningLevel(model, roomConfig)
	if !validRoomReasoningLevel(rawLevel) {
		return fmt.Errorf("reasoning level %q is invalid", rawLevel)
	}
	level := ai.ModelThinkingLevel(rawLevel)
	if roomConfig.ThinkingLevel == "" {
		level = clampRoomReasoningLevel(model, level)
	}
	for _, supported := range ai.GetSupportedThinkingLevels(model) {
		if supported == level {
			return nil
		}
	}
	return fmt.Errorf("model %s does not support reasoning level %q", model.ID, level)
}

func clampRoomReasoningLevel(model ai.Model, level ai.ModelThinkingLevel) ai.ModelThinkingLevel {
	return ai.ClampThinkingLevel(model, level)
}

func roomThinkingLevelSupported(model ai.Model, want ai.ModelThinkingLevel) bool {
	for _, supported := range ai.GetSupportedThinkingLevels(model) {
		if supported == want {
			return true
		}
	}
	return false
}

func toolDisabled(disabled []string, name string) bool {
	for _, disabledName := range disabled {
		if disabledName == name {
			return true
		}
	}
	return false
}

func webSearchSourceParts(toolName string, result any, isError bool) []map[string]any {
	if isError || toolName != "web_search" {
		return nil
	}
	output := mapFromAny(result)
	if output == nil {
		return nil
	}
	rawResults, _ := output["results"].([]any)
	if rawResults == nil {
		return nil
	}
	parts := make([]map[string]any, 0, len(rawResults))
	seen := map[string]struct{}{}
	for _, raw := range rawResults {
		item, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		rawURL := strings.TrimSpace(stringFromAny(item["url"]))
		if rawURL == "" {
			continue
		}
		parsed, err := url.Parse(rawURL)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") {
			continue
		}
		if _, exists := seen[rawURL]; exists {
			continue
		}
		seen[rawURL] = struct{}{}

		part := map[string]any{
			"url": rawURL,
		}
		if title := strings.TrimSpace(stringFromAny(item["title"])); title != "" {
			part["title"] = title
		}
		if meta := webSearchProviderMetadata(item); len(meta) > 0 {
			part["providerMetadata"] = meta
		}
		parts = append(parts, part)
	}
	return parts
}

func webSearchProviderMetadata(item map[string]any) map[string]any {
	meta := map[string]any{}
	for _, key := range []string{"snippet", "description", "published", "siteName", "author", "image", "favicon", "source"} {
		if value := strings.TrimSpace(stringFromAny(item[key])); value != "" {
			meta[key] = value
		}
	}
	if nested, ok := item["metadata"].(map[string]any); ok {
		for key, value := range nested {
			if _, exists := meta[key]; !exists {
				meta[key] = value
			}
		}
	}
	return meta
}

func stringFromAny(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case fmt.Stringer:
		return typed.String()
	default:
		return ""
	}
}
