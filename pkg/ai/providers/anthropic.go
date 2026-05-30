package providers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	ai "github.com/beeper/ai-bridge/pkg/ai"
	aiutils "github.com/beeper/ai-bridge/pkg/ai/utils"
)

const (
	fineGrainedToolStreamingBeta = "fine-grained-tool-streaming-2025-05-14"
	interleavedThinkingBeta      = "interleaved-thinking-2025-05-14"
	claudeCodeVersion            = "2.1.75"
)

var claudeCodeToolNames = map[string]string{
	"read": "Read", "write": "Write", "edit": "Edit", "bash": "Bash", "grep": "Grep", "glob": "Glob",
	"askuserquestion": "AskUserQuestion", "enterplanmode": "EnterPlanMode", "exitplanmode": "ExitPlanMode",
	"killshell": "KillShell", "notebookedit": "NotebookEdit", "skill": "Skill", "task": "Task",
	"taskoutput": "TaskOutput", "todowrite": "TodoWrite", "webfetch": "WebFetch", "websearch": "WebSearch",
}

type AnthropicOptions struct {
	ai.StreamOptions
	ThinkingEnabled      *bool
	ThinkingBudgetTokens *int
	Effort               string
	ThinkingDisplay      string
	InterleavedThinking  *bool
	ToolChoice           any
}

type resolvedAnthropicCompat struct {
	SupportsEagerToolInputStreaming bool
	SupportsLongCacheRetention      bool
	SendSessionAffinityHeaders      bool
	SupportsCacheControlOnTools     bool
	ForceAdaptiveThinking           bool
	AllowEmptySignature             bool
}

func StreamSimpleAnthropic(ctx context.Context, model ai.Model, llmContext ai.Context, options ai.SimpleStreamOptions) *ai.AssistantMessageEventStream {
	if options.APIKey == "" {
		options.APIKey = getEnvAPIKey(model.Provider)
	}
	if options.APIKey == "" {
		stream := ai.NewAssistantMessageEventStream()
		go pushError(stream, model, "No API key for provider: "+string(model.Provider))
		return stream
	}
	base := BuildBaseOptions(model, &options, options.APIKey)
	if options.Reasoning == nil {
		disabled := false
		return StreamAnthropic(ctx, model, llmContext, AnthropicOptions{StreamOptions: base, ThinkingEnabled: &disabled})
	}
	enabled := true
	if getAnthropicCompat(model).ForceAdaptiveThinking {
		return StreamAnthropic(ctx, model, llmContext, AnthropicOptions{
			StreamOptions:   base,
			ThinkingEnabled: &enabled,
			Effort:          mapAnthropicThinkingLevelToEffort(model, *options.Reasoning),
		})
	}
	adjusted := AdjustMaxTokensForThinking(base.MaxTokens, model.MaxTokens, *options.Reasoning, options.ThinkingBudgets)
	base.MaxTokens = &adjusted.MaxTokens
	return StreamAnthropic(ctx, model, llmContext, AnthropicOptions{
		StreamOptions:        base,
		ThinkingEnabled:      &enabled,
		ThinkingBudgetTokens: &adjusted.ThinkingBudget,
	})
}

func StreamAnthropic(ctx context.Context, model ai.Model, llmContext ai.Context, options AnthropicOptions) *ai.AssistantMessageEventStream {
	stream := ai.NewAssistantMessageEventStream()
	go func() {
		output := newAssistant(model)
		isOAuth := isAnthropicOAuthToken(options.APIKey)
		params := BuildAnthropicParams(model, llmContext, isOAuth, options)
		if options.OnPayload != nil {
			if next, ok, err := options.OnPayload(params, model); err != nil {
				pushFinalError(stream, &output, err.Error())
				return
			} else if ok {
				nextParams, ok := next.(map[string]any)
				if !ok {
					pushFinalError(stream, &output, "onPayload returned unsupported Anthropic request body")
					return
				}
				params = nextParams
			}
		}
		response, err := doAnthropicRequest(ctx, model, llmContext, options, isOAuth, params)
		if err != nil {
			pushFinalError(stream, &output, err.Error())
			return
		}
		defer response.Body.Close()
		if options.OnResponse != nil {
			if err := options.OnResponse(providerResponse(response), model); err != nil {
				pushFinalError(stream, &output, err.Error())
				return
			}
		}
		stream.Push(ai.AssistantMessageEvent{Type: "start", Partial: &output})
		state := newAnthropicStreamState()
		err = iterateSSE(response.Body, func(sse serverSentEvent) error {
			if sse.Event == "error" {
				return errors.New(sse.Data)
			}
			if !isAnthropicMessageEvent(sse.Event) {
				return nil
			}
			var event map[string]any
			if err := json.Unmarshal([]byte(sse.Data), &event); err != nil {
				if repaired, repairErr := aiutils.ParseJSONWithRepair[map[string]any](sse.Data); repairErr == nil {
					event = repaired
				} else {
					return fmt.Errorf("could not parse Anthropic SSE event %s: %w; data=%s; raw=%s", sse.Event, err, sse.Data, strings.Join(sse.Raw, "\n"))
				}
			}
			state.apply(stream, &output, model, llmContext, isOAuth, event)
			return nil
		})
		if err != nil {
			pushFinalError(stream, &output, err.Error())
			return
		}
		stream.Push(ai.AssistantMessageEvent{Type: "done", Reason: output.StopReason, Message: &output})
	}()
	return stream
}

func BuildAnthropicParams(model ai.Model, llmContext ai.Context, isOAuth bool, options AnthropicOptions) map[string]any {
	cacheControl := anthropicCacheControl(model, options.CacheRetention)
	params := map[string]any{
		"model":      model.ID,
		"messages":   ConvertAnthropicMessages(model, llmContext, isOAuth, cacheControl),
		"max_tokens": model.MaxTokens,
		"stream":     true,
	}
	if options.MaxTokens != nil {
		params["max_tokens"] = *options.MaxTokens
	}
	if isOAuth {
		system := []map[string]any{{"type": "text", "text": "You are Claude Code, Anthropic's official CLI for Claude."}}
		if cacheControl != nil {
			system[0]["cache_control"] = cacheControl
		}
		if llmContext.SystemPrompt != "" {
			block := map[string]any{"type": "text", "text": aiutils.SanitizeSurrogates(llmContext.SystemPrompt)}
			if cacheControl != nil {
				block["cache_control"] = cacheControl
			}
			system = append(system, block)
		}
		params["system"] = system
	} else if llmContext.SystemPrompt != "" {
		block := map[string]any{"type": "text", "text": aiutils.SanitizeSurrogates(llmContext.SystemPrompt)}
		if cacheControl != nil {
			block["cache_control"] = cacheControl
		}
		params["system"] = []map[string]any{block}
	}
	if options.Temperature != nil && (options.ThinkingEnabled == nil || !*options.ThinkingEnabled) {
		params["temperature"] = *options.Temperature
	}
	if len(llmContext.Tools) > 0 {
		compat := getAnthropicCompat(model)
		var toolCache map[string]any
		if compat.SupportsCacheControlOnTools {
			toolCache = cacheControl
		}
		params["tools"] = ConvertAnthropicTools(llmContext.Tools, isOAuth, compat.SupportsEagerToolInputStreaming, toolCache)
	}
	if model.Reasoning {
		if options.ThinkingEnabled != nil && *options.ThinkingEnabled {
			display := options.ThinkingDisplay
			if display == "" {
				display = "summarized"
			}
			if getAnthropicCompat(model).ForceAdaptiveThinking {
				params["thinking"] = map[string]any{"type": "adaptive", "display": display}
				if options.Effort != "" {
					params["output_config"] = map[string]any{"effort": options.Effort}
				}
			} else {
				budget := 1024
				if options.ThinkingBudgetTokens != nil {
					budget = *options.ThinkingBudgetTokens
				}
				params["thinking"] = map[string]any{"type": "enabled", "budget_tokens": budget, "display": display}
			}
		} else if options.ThinkingEnabled != nil {
			params["thinking"] = map[string]any{"type": "disabled"}
		}
	}
	if options.Metadata != nil {
		if userID, ok := options.Metadata["user_id"].(string); ok {
			params["metadata"] = map[string]any{"user_id": userID}
		}
	}
	if options.ToolChoice != nil {
		if choice, ok := options.ToolChoice.(string); ok {
			params["tool_choice"] = map[string]any{"type": choice}
		} else {
			params["tool_choice"] = options.ToolChoice
		}
	}
	return params
}

func ConvertAnthropicMessages(model ai.Model, llmContext ai.Context, isOAuth bool, cacheControl map[string]any) []map[string]any {
	transformed := transformMessages(llmContext.Messages, model, func(id string, _ ai.Model, _ ai.Message) string {
		return normalizeAnthropicToolCallID(id)
	})
	params := []map[string]any{}
	for i := 0; i < len(transformed); i++ {
		msg := transformed[i]
		switch msg.Role {
		case "user":
			if text, ok := msg.Content.(string); ok {
				if strings.TrimSpace(text) != "" {
					params = append(params, map[string]any{"role": "user", "content": aiutils.SanitizeSurrogates(text)})
				}
				continue
			}
			blocks := []map[string]any{}
			for _, block := range contentBlocks(msg.Content) {
				if block.Type == "text" {
					if strings.TrimSpace(block.Text) != "" {
						blocks = append(blocks, map[string]any{"type": "text", "text": aiutils.SanitizeSurrogates(block.Text)})
					}
				} else if block.Type == "image" {
					blocks = append(blocks, anthropicImageBlock(block))
				}
			}
			if len(blocks) > 0 {
				params = append(params, map[string]any{"role": "user", "content": blocks})
			}
		case "assistant":
			blocks := []map[string]any{}
			for _, block := range contentBlocks(msg.Content) {
				switch block.Type {
				case "text":
					if strings.TrimSpace(block.Text) != "" {
						blocks = append(blocks, map[string]any{"type": "text", "text": aiutils.SanitizeSurrogates(block.Text)})
					}
				case "thinking":
					if block.Redacted {
						blocks = append(blocks, map[string]any{"type": "redacted_thinking", "data": block.ThinkingSignature})
					} else if strings.TrimSpace(block.Thinking) != "" {
						if block.ThinkingSignature == "" && !getAnthropicCompat(model).AllowEmptySignature {
							blocks = append(blocks, map[string]any{"type": "text", "text": aiutils.SanitizeSurrogates(block.Thinking)})
						} else {
							blocks = append(blocks, map[string]any{"type": "thinking", "thinking": aiutils.SanitizeSurrogates(block.Thinking), "signature": block.ThinkingSignature})
						}
					}
				case "toolCall":
					name := block.Name
					if isOAuth {
						name = toClaudeCodeName(name)
					}
					blocks = append(blocks, map[string]any{"type": "tool_use", "id": block.ID, "name": name, "input": block.Arguments})
				}
			}
			if len(blocks) > 0 {
				params = append(params, map[string]any{"role": "assistant", "content": blocks})
			}
		case "toolResult":
			toolResults := []map[string]any{}
			for ; i < len(transformed) && transformed[i].Role == "toolResult"; i++ {
				toolMsg := transformed[i]
				toolResults = append(toolResults, map[string]any{
					"type":        "tool_result",
					"tool_use_id": toolMsg.ToolCallID,
					"content":     anthropicToolResultContent(toolMsg.Content),
					"is_error":    toolMsg.IsError,
				})
			}
			i--
			params = append(params, map[string]any{"role": "user", "content": toolResults})
		}
	}
	if cacheControl != nil && len(params) > 0 {
		last := params[len(params)-1]
		if last["role"] == "user" {
			if blocks, ok := last["content"].([]map[string]any); ok && len(blocks) > 0 {
				blocks[len(blocks)-1]["cache_control"] = cacheControl
			} else if text, ok := last["content"].(string); ok {
				last["content"] = []map[string]any{{"type": "text", "text": text, "cache_control": cacheControl}}
			}
		}
	}
	return params
}

func ConvertAnthropicTools(tools []ai.Tool, isOAuth bool, supportsEagerToolInputStreaming bool, cacheControl map[string]any) []map[string]any {
	out := make([]map[string]any, 0, len(tools))
	for index, tool := range tools {
		name := tool.Name
		if isOAuth {
			name = toClaudeCodeName(name)
		}
		schema := map[string]any{"type": "object", "properties": map[string]any{}, "required": []any{}}
		if properties, ok := tool.Parameters["properties"]; ok {
			schema["properties"] = properties
		}
		if required, ok := tool.Parameters["required"]; ok {
			schema["required"] = required
		}
		item := map[string]any{"name": name, "description": tool.Description, "input_schema": schema}
		if supportsEagerToolInputStreaming {
			item["eager_input_streaming"] = true
		}
		if cacheControl != nil && index == len(tools)-1 {
			item["cache_control"] = cacheControl
		}
		out = append(out, item)
	}
	return out
}

func doAnthropicRequest(ctx context.Context, model ai.Model, llmContext ai.Context, options AnthropicOptions, isOAuth bool, params map[string]any) (*http.Response, error) {
	body, err := json.Marshal(params)
	if err != nil {
		return nil, err
	}
	url := strings.TrimRight(model.BaseURL, "/") + "/v1/messages"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Anthropic-Version", "2023-06-01")
	for key, value := range anthropicHeaders(model, llmContext, options, isOAuth) {
		if value == "" {
			req.Header.Del(key)
		} else {
			req.Header.Set(key, value)
		}
	}
	client := http.DefaultClient
	if options.TimeoutMs != nil && *options.TimeoutMs > 0 {
		client = &http.Client{Timeout: time.Duration(*options.TimeoutMs) * time.Millisecond}
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		defer resp.Body.Close()
		payload, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
		return nil, fmt.Errorf("Anthropic API error (%d): %s", resp.StatusCode, strings.TrimSpace(string(payload)))
	}
	return resp, nil
}

func anthropicHeaders(model ai.Model, llmContext ai.Context, options AnthropicOptions, isOAuth bool) map[string]string {
	headers := map[string]string{"Anthropic-Dangerous-Direct-Browser-Access": "true"}
	betas := []string{}
	if len(llmContext.Tools) > 0 && !getAnthropicCompat(model).SupportsEagerToolInputStreaming {
		betas = append(betas, fineGrainedToolStreamingBeta)
	}
	interleaved := true
	if options.InterleavedThinking != nil {
		interleaved = *options.InterleavedThinking
	}
	if interleaved && !getAnthropicCompat(model).ForceAdaptiveThinking {
		betas = append(betas, interleavedThinkingBeta)
	}
	if isBeeperAIProxyBaseURL(model.BaseURL) {
		headers["Authorization"] = "Bearer " + options.APIKey
		if len(betas) > 0 {
			headers["Anthropic-Beta"] = strings.Join(betas, ",")
		}
	} else if isOAuth {
		headers["Authorization"] = "Bearer " + options.APIKey
		headers["Anthropic-Beta"] = strings.Join(append([]string{"claude-code-20250219", "oauth-2025-04-20"}, betas...), ",")
		headers["User-Agent"] = "claude-cli/" + claudeCodeVersion
		headers["X-App"] = "cli"
	} else {
		headers["X-Api-Key"] = options.APIKey
		if len(betas) > 0 {
			headers["Anthropic-Beta"] = strings.Join(betas, ",")
		}
		if options.SessionID != "" && getAnthropicCompat(model).SendSessionAffinityHeaders && resolveAnthropicCacheRetention(options.CacheRetention) != ai.CacheRetentionNone {
			headers["X-Session-Affinity"] = options.SessionID
		}
	}
	for key, value := range model.Headers {
		headers[key] = value
	}
	for key, value := range options.Headers {
		headers[key] = value
	}
	return headers
}

func getAnthropicCompat(model ai.Model) resolvedAnthropicCompat {
	isFireworks := model.Provider == ai.ProviderFireworks
	isCloudflareGatewayAnthropic := model.Provider == ai.ProviderCloudflareAIGateway && strings.Contains(model.BaseURL, "anthropic")
	return resolvedAnthropicCompat{
		SupportsEagerToolInputStreaming: compatBool(model, "supportsEagerToolInputStreaming", !isFireworks),
		SupportsLongCacheRetention:      compatBool(model, "supportsLongCacheRetention", !isFireworks),
		SendSessionAffinityHeaders:      compatBool(model, "sendSessionAffinityHeaders", isFireworks || isCloudflareGatewayAnthropic),
		SupportsCacheControlOnTools:     compatBool(model, "supportsCacheControlOnTools", !isFireworks),
		ForceAdaptiveThinking:           compatBool(model, "forceAdaptiveThinking", false),
		AllowEmptySignature:             compatBool(model, "allowEmptySignature", false),
	}
}

func resolveAnthropicCacheRetention(cacheRetention ai.CacheRetention) ai.CacheRetention {
	if cacheRetention != "" {
		return cacheRetention
	}
	if os.Getenv("PI_CACHE_RETENTION") == "long" {
		return ai.CacheRetentionLong
	}
	return ai.CacheRetentionShort
}

func anthropicCacheControl(model ai.Model, cacheRetention ai.CacheRetention) map[string]any {
	retention := resolveAnthropicCacheRetention(cacheRetention)
	if retention == ai.CacheRetentionNone {
		return nil
	}
	cacheControl := map[string]any{"type": "ephemeral"}
	if retention == ai.CacheRetentionLong && getAnthropicCompat(model).SupportsLongCacheRetention {
		cacheControl["ttl"] = "1h"
	}
	return cacheControl
}

func anthropicImageBlock(block ai.ContentBlock) map[string]any {
	return map[string]any{
		"type": "image",
		"source": map[string]any{
			"type":       "base64",
			"media_type": block.MimeType,
			"data":       block.Data,
		},
	}
}

func anthropicToolResultContent(content any) any {
	blocks := contentBlocks(content)
	hasImage := false
	for _, block := range blocks {
		if block.Type == "image" {
			hasImage = true
			break
		}
	}
	if !hasImage {
		text := []string{}
		for _, block := range blocks {
			if block.Type == "text" {
				text = append(text, block.Text)
			}
		}
		return aiutils.SanitizeSurrogates(strings.Join(text, "\n"))
	}
	out := []map[string]any{}
	hasText := false
	for _, block := range blocks {
		if block.Type == "text" {
			hasText = true
			out = append(out, map[string]any{"type": "text", "text": aiutils.SanitizeSurrogates(block.Text)})
		} else if block.Type == "image" {
			out = append(out, anthropicImageBlock(block))
		}
	}
	if !hasText {
		out = append([]map[string]any{{"type": "text", "text": "(see attached image)"}}, out...)
	}
	return out
}

func normalizeAnthropicToolCallID(id string) string {
	return invalidToolCallIDChar.ReplaceAllString(id, "_")[:minInt(len(invalidToolCallIDChar.ReplaceAllString(id, "_")), 64)]
}

func toClaudeCodeName(name string) string {
	if canonical := claudeCodeToolNames[strings.ToLower(name)]; canonical != "" {
		return canonical
	}
	return name
}

func fromClaudeCodeName(name string, tools []ai.Tool) string {
	lower := strings.ToLower(name)
	for _, tool := range tools {
		if strings.ToLower(tool.Name) == lower {
			return tool.Name
		}
	}
	return name
}

func mapAnthropicThinkingLevelToEffort(model ai.Model, level ai.ThinkingLevel) string {
	if model.ThinkingLevelMap != nil {
		if mapped := model.ThinkingLevelMap[ai.ModelThinkingLevel(level)]; mapped != nil && *mapped != "" {
			return *mapped
		}
	}
	switch level {
	case ai.ThinkingLevelMinimal, ai.ThinkingLevelLow:
		return "low"
	case ai.ThinkingLevelMedium:
		return "medium"
	case ai.ThinkingLevelHigh:
		return "high"
	default:
		return "high"
	}
}

func isAnthropicOAuthToken(apiKey string) bool {
	return strings.Contains(apiKey, "sk-ant-oat")
}

func isAnthropicMessageEvent(event string) bool {
	switch event {
	case "message_start", "message_delta", "message_stop", "content_block_start", "content_block_delta", "content_block_stop":
		return true
	default:
		return false
	}
}

type anthropicStreamState struct {
	anthropicIndexes map[int]int
	partialJSON      map[int]string
}

func newAnthropicStreamState() *anthropicStreamState {
	return &anthropicStreamState{anthropicIndexes: map[int]int{}, partialJSON: map[int]string{}}
}

func (s *anthropicStreamState) apply(stream *ai.AssistantMessageEventStream, output *ai.Message, model ai.Model, llmContext ai.Context, isOAuth bool, event map[string]any) {
	switch event["type"] {
	case "message_start":
		if message, _ := event["message"].(map[string]any); message != nil {
			if id, _ := message["id"].(string); id != "" {
				output.ResponseID = id
			}
			if usage, _ := message["usage"].(map[string]any); usage != nil {
				applyAnthropicUsage(output, model, usage)
			}
		}
	case "content_block_start":
		index := intFromAny(event["index"])
		blockMap, _ := event["content_block"].(map[string]any)
		if blockMap == nil {
			return
		}
		contentIndex := len(output.Content.([]ai.ContentBlock))
		s.anthropicIndexes[index] = contentIndex
		switch blockMap["type"] {
		case "text":
			appendContentBlock(output, ai.ContentBlock{Type: "text"})
			stream.Push(ai.AssistantMessageEvent{Type: "text_start", ContentIndex: contentIndex, Partial: output})
		case "thinking":
			appendContentBlock(output, ai.ContentBlock{Type: "thinking"})
			stream.Push(ai.AssistantMessageEvent{Type: "thinking_start", ContentIndex: contentIndex, Partial: output})
		case "redacted_thinking":
			appendContentBlock(output, ai.ContentBlock{Type: "thinking", Thinking: "[Reasoning redacted]", ThinkingSignature: stringFromAny(blockMap["data"]), Redacted: true})
			stream.Push(ai.AssistantMessageEvent{Type: "thinking_start", ContentIndex: contentIndex, Partial: output})
		case "tool_use":
			name := stringFromAny(blockMap["name"])
			if isOAuth {
				name = fromClaudeCodeName(name, llmContext.Tools)
			}
			arguments := map[string]any{}
			if input, ok := blockMap["input"]; ok {
				arguments = parseJSONMap(mustJSON(input))
				if len(arguments) > 0 {
					s.partialJSON[contentIndex] = mustJSON(input)
				}
			}
			appendContentBlock(output, ai.ContentBlock{Type: "toolCall", ID: stringFromAny(blockMap["id"]), Name: name, Arguments: arguments})
			stream.Push(ai.AssistantMessageEvent{Type: "toolcall_start", ContentIndex: contentIndex, Partial: output})
		}
	case "content_block_delta":
		contentIndex, ok := s.anthropicIndexes[intFromAny(event["index"])]
		if !ok {
			return
		}
		delta, _ := event["delta"].(map[string]any)
		if delta == nil {
			return
		}
		blocks := output.Content.([]ai.ContentBlock)
		switch delta["type"] {
		case "text_delta":
			text := stringFromAny(delta["text"])
			blocks[contentIndex].Text += text
			output.Content = blocks
			stream.Push(ai.AssistantMessageEvent{Type: "text_delta", ContentIndex: contentIndex, Delta: text, Partial: output})
		case "thinking_delta":
			text := stringFromAny(delta["thinking"])
			blocks[contentIndex].Thinking += text
			output.Content = blocks
			stream.Push(ai.AssistantMessageEvent{Type: "thinking_delta", ContentIndex: contentIndex, Delta: text, Partial: output})
		case "signature_delta":
			blocks[contentIndex].ThinkingSignature += stringFromAny(delta["signature"])
			output.Content = blocks
		case "input_json_delta":
			part := stringFromAny(delta["partial_json"])
			s.partialJSON[contentIndex] += part
			blocks[contentIndex].Arguments = parseJSONMap(s.partialJSON[contentIndex])
			output.Content = blocks
			stream.Push(ai.AssistantMessageEvent{Type: "toolcall_delta", ContentIndex: contentIndex, Delta: part, Partial: output})
		}
	case "content_block_stop":
		contentIndex, ok := s.anthropicIndexes[intFromAny(event["index"])]
		if !ok {
			return
		}
		blocks := output.Content.([]ai.ContentBlock)
		block := blocks[contentIndex]
		switch block.Type {
		case "text":
			stream.Push(ai.AssistantMessageEvent{Type: "text_end", ContentIndex: contentIndex, Content: block.Text, Partial: output})
		case "thinking":
			stream.Push(ai.AssistantMessageEvent{Type: "thinking_end", ContentIndex: contentIndex, Content: block.Thinking, Partial: output})
		case "toolCall":
			block.Arguments = parseJSONMap(s.partialJSON[contentIndex])
			blocks[contentIndex] = block
			output.Content = blocks
			toolCall := ai.ToolCall{Type: "toolCall", ID: block.ID, Name: block.Name, Arguments: block.Arguments}
			stream.Push(ai.AssistantMessageEvent{Type: "toolcall_end", ContentIndex: contentIndex, ToolCall: &toolCall, Partial: output})
		}
	case "message_delta":
		if delta, _ := event["delta"].(map[string]any); delta != nil {
			if reason := stringFromAny(delta["stop_reason"]); reason != "" {
				output.StopReason = mapAnthropicStopReason(reason)
			}
		}
		if usage, _ := event["usage"].(map[string]any); usage != nil {
			applyAnthropicUsage(output, model, usage)
		}
	}
}

func appendContentBlock(output *ai.Message, block ai.ContentBlock) {
	blocks, _ := output.Content.([]ai.ContentBlock)
	blocks = append(blocks, block)
	output.Content = blocks
}

func applyAnthropicUsage(output *ai.Message, model ai.Model, usage map[string]any) {
	if value, ok := usage["input_tokens"]; ok && value != nil {
		output.Usage.Input = intFromAny(value)
	}
	if value, ok := usage["output_tokens"]; ok && value != nil {
		output.Usage.Output = intFromAny(value)
	}
	if value, ok := usage["cache_read_input_tokens"]; ok && value != nil {
		output.Usage.CacheRead = intFromAny(value)
	}
	if value, ok := usage["cache_creation_input_tokens"]; ok && value != nil {
		output.Usage.CacheWrite = intFromAny(value)
	}
	output.Usage.TotalTokens = output.Usage.Input + output.Usage.Output + output.Usage.CacheRead + output.Usage.CacheWrite
	ai.CalculateCost(model, &output.Usage)
}

func mapAnthropicStopReason(reason string) ai.StopReason {
	switch reason {
	case "end_turn", "pause_turn", "stop_sequence":
		return ai.StopReasonStop
	case "max_tokens":
		return ai.StopReasonLength
	case "tool_use":
		return ai.StopReasonToolUse
	case "refusal", "sensitive":
		return ai.StopReasonError
	default:
		return ai.StopReasonError
	}
}

func stringFromAny(value any) string {
	if text, ok := value.(string); ok {
		return text
	}
	return ""
}
