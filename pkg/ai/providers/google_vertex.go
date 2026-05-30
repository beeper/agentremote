package providers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"

	"golang.org/x/oauth2/google"

	ai "github.com/beeper/ai-bridge/pkg/ai"
	aiutils "github.com/beeper/ai-bridge/pkg/ai/utils"
)

const gcpVertexCredentialsMarker = "gcp-vertex-credentials"

type GoogleVertexOptions struct {
	ai.StreamOptions
	ToolChoice string
	Thinking   *GoogleThinkingOptions
	Project    string
	Location   string
}

type GoogleThinkingOptions struct {
	Enabled      bool
	BudgetTokens *int
	Level        googleThinkingLevel
}

func StreamSimpleGoogleVertex(ctx context.Context, model ai.Model, llmContext ai.Context, options ai.SimpleStreamOptions) *ai.AssistantMessageEventStream {
	if options.APIKey == "" {
		options.APIKey = getEnvAPIKey(model.Provider)
	}
	base := BuildBaseOptions(model, &options, "")
	base.APIKey = options.APIKey
	if options.Reasoning == nil {
		return StreamGoogleVertex(ctx, model, llmContext, GoogleVertexOptions{
			StreamOptions: base,
			Thinking:      &GoogleThinkingOptions{Enabled: false},
		})
	}
	clamped := ai.ClampThinkingLevel(model, ai.ModelThinkingLevel(*options.Reasoning))
	effort := ai.ThinkingLevel(clamped)
	if clamped == ai.ModelThinkingLevelOff {
		effort = ai.ThinkingLevelHigh
	}
	geminiModel := model
	if isGoogleGemini3ProModel(geminiModel) || isGoogleGemini3FlashModel(geminiModel) {
		return StreamGoogleVertex(ctx, model, llmContext, GoogleVertexOptions{
			StreamOptions: base,
			Thinking:      &GoogleThinkingOptions{Enabled: true, Level: getGoogleVertexGemini3ThinkingLevel(effort, geminiModel)},
		})
	}
	budget := getGoogleVertexBudget(geminiModel, effort, options.ThinkingBudgets)
	return StreamGoogleVertex(ctx, model, llmContext, GoogleVertexOptions{
		StreamOptions: base,
		Thinking:      &GoogleThinkingOptions{Enabled: true, BudgetTokens: &budget},
	})
}

func StreamGoogleVertex(ctx context.Context, model ai.Model, llmContext ai.Context, options GoogleVertexOptions) *ai.AssistantMessageEventStream {
	stream := ai.NewAssistantMessageEventStream()
	go func() {
		output := newAssistant(model)
		params := BuildGoogleVertexParams(model, llmContext, options)
		if options.OnPayload != nil {
			if next, ok, err := options.OnPayload(params, model); err != nil {
				pushFinalError(stream, &output, err.Error())
				return
			} else if ok {
				nextParams, ok := next.(map[string]any)
				if !ok {
					pushFinalError(stream, &output, "onPayload returned unsupported Google Vertex request body")
					return
				}
				params = nextParams
			}
		}
		response, err := doGoogleVertexRequest(ctx, model, options, params)
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
		state := newGoogleVertexStreamState()
		err = iterateSSE(response.Body, func(sse serverSentEvent) error {
			if strings.TrimSpace(sse.Data) == "" || strings.TrimSpace(sse.Data) == "[DONE]" {
				return nil
			}
			var chunk map[string]any
			if err := json.Unmarshal([]byte(sse.Data), &chunk); err != nil {
				return fmt.Errorf("could not parse Google Vertex SSE event: %w; data=%s; raw=%s", err, sse.Data, strings.Join(sse.Raw, "\n"))
			}
			state.apply(stream, &output, model, chunk)
			return nil
		})
		if err != nil {
			pushFinalError(stream, &output, err.Error())
			return
		}
		finishGoogleCurrentBlock(stream, &output, &state.currentBlockIndex)
		stream.Push(ai.AssistantMessageEvent{Type: "done", Reason: output.StopReason, Message: &output})
	}()
	return stream
}

func BuildGoogleVertexParams(model ai.Model, llmContext ai.Context, options GoogleVertexOptions) map[string]any {
	params := map[string]any{"contents": ConvertGoogleMessages(model, llmContext)}
	config := map[string]any{}
	if options.Temperature != nil {
		config["temperature"] = *options.Temperature
	}
	if options.MaxTokens != nil {
		config["maxOutputTokens"] = *options.MaxTokens
	}
	if options.Thinking != nil && model.Reasoning {
		if options.Thinking.Enabled {
			thinking := map[string]any{"includeThoughts": true}
			if options.Thinking.Level != "" {
				thinking["thinkingLevel"] = options.Thinking.Level
			} else if options.Thinking.BudgetTokens != nil {
				thinking["thinkingBudget"] = *options.Thinking.BudgetTokens
			}
			config["thinkingConfig"] = thinking
		} else {
			config["thinkingConfig"] = disabledGoogleVertexThinkingConfig(model)
		}
	}
	if len(config) > 0 {
		params["generationConfig"] = config
	}
	if llmContext.SystemPrompt != "" {
		params["systemInstruction"] = map[string]any{"parts": []map[string]any{{"text": aiutils.SanitizeSurrogates(llmContext.SystemPrompt)}}}
	}
	if len(llmContext.Tools) > 0 {
		params["tools"] = ConvertGoogleTools(llmContext.Tools, false)
		if options.ToolChoice != "" {
			params["toolConfig"] = map[string]any{"functionCallingConfig": map[string]any{"mode": mapGoogleToolChoice(options.ToolChoice)}}
		}
	}
	return params
}

func doGoogleVertexRequest(ctx context.Context, model ai.Model, options GoogleVertexOptions, params map[string]any) (*http.Response, error) {
	body, err := json.Marshal(params)
	if err != nil {
		return nil, err
	}
	apiKey := resolveGoogleVertexAPIKey(options)
	beeperProxy := isBeeperAIProxyBaseURL(model.BaseURL)
	endpoint, err := googleVertexEndpoint(model, options, apiKey != "", beeperProxy)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if beeperProxy {
		if options.APIKey == "" {
			return nil, fmt.Errorf("missing API key for Beeper AI proxy")
		}
		req.Header.Set("Authorization", "Bearer "+options.APIKey)
	} else if apiKey != "" {
		req.Header.Set("X-Goog-Api-Key", apiKey)
	} else {
		tokenSource, err := google.DefaultTokenSource(ctx, "https://www.googleapis.com/auth/cloud-platform")
		if err != nil {
			return nil, err
		}
		token, err := tokenSource.Token()
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+token.AccessToken)
	}
	for key, value := range model.Headers {
		req.Header.Set(key, value)
	}
	for key, value := range options.Headers {
		req.Header.Set(key, value)
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
		return nil, fmt.Errorf("Google Vertex API error (%d): %s", resp.StatusCode, strings.TrimSpace(string(payload)))
	}
	return resp, nil
}

func googleVertexEndpoint(model ai.Model, options GoogleVertexOptions, usingAPIKey bool, beeperProxy bool) (string, error) {
	baseURL := resolveGoogleVertexCustomBaseURL(model.BaseURL)
	modelPath, err := googleVertexModelPath(model.ID)
	if err != nil {
		return "", err
	}
	if beeperProxy {
		if baseURL == "" {
			return "", fmt.Errorf("Beeper AI proxy base URL is required for Vertex")
		}
		return strings.TrimRight(baseURL, "/") + "/v1/" + modelPath + ":streamGenerateContent?alt=sse", nil
	}
	if usingAPIKey {
		if baseURL == "" {
			baseURL = "https://aiplatform.googleapis.com"
		}
		return strings.TrimRight(baseURL, "/") + "/v1/" + modelPath + ":streamGenerateContent?alt=sse", nil
	}
	project := resolveGoogleVertexProject(options)
	location := resolveGoogleVertexLocation(options)
	if project == "" {
		return "", fmt.Errorf("Vertex AI requires a project ID. Set GOOGLE_CLOUD_PROJECT/GCLOUD_PROJECT or pass project in options")
	}
	if location == "" {
		return "", fmt.Errorf("Vertex AI requires a location. Set GOOGLE_CLOUD_LOCATION or pass location in options")
	}
	if baseURL == "" {
		baseURL = fmt.Sprintf("https://%s-aiplatform.googleapis.com", location)
	}
	path := modelPath
	if !strings.HasPrefix(path, "projects/") {
		path = fmt.Sprintf("projects/%s/locations/%s/%s", url.PathEscape(project), url.PathEscape(location), path)
	}
	return strings.TrimRight(baseURL, "/") + "/v1/" + path + ":streamGenerateContent?alt=sse", nil
}

func googleVertexModelPath(modelID string) (string, error) {
	if modelID == "" || strings.Contains(modelID, "..") || strings.ContainsAny(modelID, "?&") {
		return "", fmt.Errorf("invalid model parameter")
	}
	if strings.HasPrefix(modelID, "publishers/") || strings.HasPrefix(modelID, "projects/") || strings.HasPrefix(modelID, "models/") {
		return modelID, nil
	}
	if strings.Contains(modelID, "/") {
		parts := strings.SplitN(modelID, "/", 2)
		return "publishers/" + parts[0] + "/models/" + parts[1], nil
	}
	return "publishers/google/models/" + modelID, nil
}

func resolveGoogleVertexCustomBaseURL(baseURL string) string {
	trimmed := strings.TrimSpace(baseURL)
	if trimmed == "" || strings.Contains(trimmed, "{location}") {
		return ""
	}
	return trimmed
}

func resolveGoogleVertexAPIKey(options GoogleVertexOptions) string {
	apiKey := strings.TrimSpace(options.APIKey)
	if apiKey == "" || apiKey == gcpVertexCredentialsMarker || apiKey == "<authenticated>" || regexp.MustCompile(`^<[^>]+>$`).MatchString(apiKey) {
		return ""
	}
	return apiKey
}

func resolveGoogleVertexProject(options GoogleVertexOptions) string {
	if options.Project != "" {
		return options.Project
	}
	if value := os.Getenv("GOOGLE_CLOUD_PROJECT"); value != "" {
		return value
	}
	return os.Getenv("GCLOUD_PROJECT")
}

func resolveGoogleVertexLocation(options GoogleVertexOptions) string {
	if options.Location != "" {
		return options.Location
	}
	return os.Getenv("GOOGLE_CLOUD_LOCATION")
}

func disabledGoogleVertexThinkingConfig(model ai.Model) map[string]any {
	if isGoogleGemini3ProModel(model) {
		return map[string]any{"thinkingLevel": googleThinkingLow}
	}
	if isGoogleGemini3FlashModel(model) {
		return map[string]any{"thinkingLevel": googleThinkingMinimal}
	}
	return map[string]any{"thinkingBudget": 0}
}

func isGoogleGemini3ProModel(model ai.Model) bool {
	return regexp.MustCompile(`(?i)gemini-3(?:\.\d+)?-pro`).MatchString(model.ID)
}

func isGoogleGemini3FlashModel(model ai.Model) bool {
	return regexp.MustCompile(`(?i)gemini-3(?:\.\d+)?-flash`).MatchString(model.ID)
}

func getGoogleVertexGemini3ThinkingLevel(effort ai.ThinkingLevel, model ai.Model) googleThinkingLevel {
	if isGoogleGemini3ProModel(model) {
		switch effort {
		case ai.ThinkingLevelMinimal, ai.ThinkingLevelLow:
			return googleThinkingLow
		case ai.ThinkingLevelMedium, ai.ThinkingLevelHigh:
			return googleThinkingHigh
		}
	}
	switch effort {
	case ai.ThinkingLevelMinimal:
		return googleThinkingMinimal
	case ai.ThinkingLevelLow:
		return googleThinkingLow
	case ai.ThinkingLevelMedium:
		return googleThinkingMedium
	case ai.ThinkingLevelHigh:
		return googleThinkingHigh
	default:
		return googleThinkingHigh
	}
}

func getGoogleVertexBudget(model ai.Model, effort ai.ThinkingLevel, customBudgets *ai.ThinkingBudgets) int {
	if customBudgets != nil {
		switch effort {
		case ai.ThinkingLevelMinimal:
			if customBudgets.Minimal != nil {
				return *customBudgets.Minimal
			}
		case ai.ThinkingLevelLow:
			if customBudgets.Low != nil {
				return *customBudgets.Low
			}
		case ai.ThinkingLevelMedium:
			if customBudgets.Medium != nil {
				return *customBudgets.Medium
			}
		case ai.ThinkingLevelHigh:
			if customBudgets.High != nil {
				return *customBudgets.High
			}
		}
	}
	if strings.Contains(model.ID, "2.5-pro") {
		return map[ai.ThinkingLevel]int{ai.ThinkingLevelMinimal: 128, ai.ThinkingLevelLow: 2048, ai.ThinkingLevelMedium: 8192, ai.ThinkingLevelHigh: 32768}[effort]
	}
	if strings.Contains(model.ID, "2.5-flash") {
		return map[ai.ThinkingLevel]int{ai.ThinkingLevelMinimal: 128, ai.ThinkingLevelLow: 2048, ai.ThinkingLevelMedium: 8192, ai.ThinkingLevelHigh: 24576}[effort]
	}
	return -1
}

type googleVertexStreamState struct {
	currentBlockIndex int
	toolCallCounter   int
}

func newGoogleVertexStreamState() *googleVertexStreamState {
	return &googleVertexStreamState{currentBlockIndex: -1}
}

func (s *googleVertexStreamState) apply(stream *ai.AssistantMessageEventStream, output *ai.Message, model ai.Model, chunk map[string]any) {
	if responseID := stringFromAny(chunk["responseId"]); responseID != "" && output.ResponseID == "" {
		output.ResponseID = responseID
	}
	candidates, _ := chunk["candidates"].([]any)
	if len(candidates) > 0 {
		candidate, _ := candidates[0].(map[string]any)
		if candidate != nil {
			if content, _ := candidate["content"].(map[string]any); content != nil {
				if parts, _ := content["parts"].([]any); len(parts) > 0 {
					for _, rawPart := range parts {
						part, _ := rawPart.(map[string]any)
						s.applyPart(stream, output, part)
					}
				}
			}
			if reason := stringFromAny(candidate["finishReason"]); reason != "" {
				output.StopReason = mapGoogleStopReason(reason)
				for _, block := range output.Content.([]ai.ContentBlock) {
					if block.Type == "toolCall" {
						output.StopReason = ai.StopReasonToolUse
						break
					}
				}
			}
		}
	}
	if usage, _ := chunk["usageMetadata"].(map[string]any); usage != nil {
		output.Usage = parseGoogleUsage(usage, model)
	}
}

func (s *googleVertexStreamState) applyPart(stream *ai.AssistantMessageEventStream, output *ai.Message, part map[string]any) {
	if part == nil {
		return
	}
	if text, ok := part["text"].(string); ok {
		isThinking := isGoogleThinkingPart(part)
		blocks := output.Content.([]ai.ContentBlock)
		if s.currentBlockIndex < 0 || (isThinking && blocks[s.currentBlockIndex].Type != "thinking") || (!isThinking && blocks[s.currentBlockIndex].Type != "text") {
			finishGoogleCurrentBlock(stream, output, &s.currentBlockIndex)
			block := ai.ContentBlock{Type: "text"}
			eventType := "text_start"
			if isThinking {
				block = ai.ContentBlock{Type: "thinking"}
				eventType = "thinking_start"
			}
			appendContentBlock(output, block)
			s.currentBlockIndex = len(output.Content.([]ai.ContentBlock)) - 1
			stream.Push(ai.AssistantMessageEvent{Type: eventType, ContentIndex: s.currentBlockIndex, Partial: output})
		}
		blocks = output.Content.([]ai.ContentBlock)
		if blocks[s.currentBlockIndex].Type == "thinking" {
			blocks[s.currentBlockIndex].Thinking += text
			blocks[s.currentBlockIndex].ThinkingSignature = retainThoughtSignature(blocks[s.currentBlockIndex].ThinkingSignature, part["thoughtSignature"])
			output.Content = blocks
			stream.Push(ai.AssistantMessageEvent{Type: "thinking_delta", ContentIndex: s.currentBlockIndex, Delta: text, Partial: output})
		} else {
			blocks[s.currentBlockIndex].Text += text
			blocks[s.currentBlockIndex].TextSignature = retainThoughtSignature(blocks[s.currentBlockIndex].TextSignature, part["thoughtSignature"])
			output.Content = blocks
			stream.Push(ai.AssistantMessageEvent{Type: "text_delta", ContentIndex: s.currentBlockIndex, Delta: text, Partial: output})
		}
	}
	if functionCall, _ := part["functionCall"].(map[string]any); functionCall != nil {
		finishGoogleCurrentBlock(stream, output, &s.currentBlockIndex)
		name := stringFromAny(functionCall["name"])
		id := nextGoogleToolCallID(output, name, stringFromAny(functionCall["id"]), &s.toolCallCounter)
		args, _ := functionCall["args"].(map[string]any)
		if args == nil {
			args = map[string]any{}
		}
		block := ai.ContentBlock{Type: "toolCall", ID: id, Name: name, Arguments: args, ThoughtSignature: stringFromAny(part["thoughtSignature"])}
		appendContentBlock(output, block)
		contentIndex := len(output.Content.([]ai.ContentBlock)) - 1
		toolCall := ai.ToolCall{Type: "toolCall", ID: id, Name: name, Arguments: args, ThoughtSignature: block.ThoughtSignature}
		stream.Push(ai.AssistantMessageEvent{Type: "toolcall_start", ContentIndex: contentIndex, Partial: output})
		stream.Push(ai.AssistantMessageEvent{Type: "toolcall_delta", ContentIndex: contentIndex, Delta: mustJSON(args), Partial: output})
		stream.Push(ai.AssistantMessageEvent{Type: "toolcall_end", ContentIndex: contentIndex, ToolCall: &toolCall, Partial: output})
	}
}
