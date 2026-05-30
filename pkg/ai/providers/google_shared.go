package providers

import (
	"encoding/base64"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	ai "github.com/beeper/ai-bridge/pkg/ai"
	aiutils "github.com/beeper/ai-bridge/pkg/ai/utils"
)

type googleThinkingLevel string

const (
	googleThinkingUnspecified googleThinkingLevel = "THINKING_LEVEL_UNSPECIFIED"
	googleThinkingMinimal     googleThinkingLevel = "MINIMAL"
	googleThinkingLow         googleThinkingLevel = "LOW"
	googleThinkingMedium      googleThinkingLevel = "MEDIUM"
	googleThinkingHigh        googleThinkingLevel = "HIGH"
)

var base64SignaturePattern = regexp.MustCompile(`^[A-Za-z0-9+/]+={0,2}$`)

func isGoogleThinkingPart(part map[string]any) bool {
	value, _ := part["thought"].(bool)
	return value
}

func retainThoughtSignature(existing string, incoming any) string {
	text, _ := incoming.(string)
	if text != "" {
		return text
	}
	return existing
}

func requiresGoogleToolCallID(modelID string) bool {
	return strings.HasPrefix(modelID, "claude-") || strings.HasPrefix(modelID, "gpt-oss-")
}

func ConvertGoogleMessages(model ai.Model, llmContext ai.Context) []map[string]any {
	contents := []map[string]any{}
	transformed := transformMessages(llmContext.Messages, model, func(id string, _ ai.Model, _ ai.Message) string {
		if !requiresGoogleToolCallID(model.ID) {
			return id
		}
		return normalizeAnthropicToolCallID(id)
	})
	for _, msg := range transformed {
		switch msg.Role {
		case "user":
			if text, ok := msg.Content.(string); ok {
				contents = append(contents, map[string]any{"role": "user", "parts": []map[string]any{{"text": aiutils.SanitizeSurrogates(text)}}})
				continue
			}
			parts := []map[string]any{}
			for _, block := range contentBlocks(msg.Content) {
				if block.Type == "text" {
					parts = append(parts, map[string]any{"text": aiutils.SanitizeSurrogates(block.Text)})
				} else if block.Type == "image" {
					parts = append(parts, map[string]any{"inlineData": map[string]any{"mimeType": block.MimeType, "data": block.Data}})
				}
			}
			if len(parts) > 0 {
				contents = append(contents, map[string]any{"role": "user", "parts": parts})
			}
		case "assistant":
			parts := []map[string]any{}
			isSameProviderAndModel := msg.Provider == model.Provider && msg.Model == model.ID
			for _, block := range contentBlocks(msg.Content) {
				switch block.Type {
				case "text":
					if strings.TrimSpace(block.Text) == "" {
						continue
					}
					part := map[string]any{"text": aiutils.SanitizeSurrogates(block.Text)}
					if signature := resolveGoogleThoughtSignature(isSameProviderAndModel, block.TextSignature); signature != "" {
						part["thoughtSignature"] = signature
					}
					parts = append(parts, part)
				case "thinking":
					if strings.TrimSpace(block.Thinking) == "" {
						continue
					}
					if isSameProviderAndModel {
						part := map[string]any{"thought": true, "text": aiutils.SanitizeSurrogates(block.Thinking)}
						if signature := resolveGoogleThoughtSignature(isSameProviderAndModel, block.ThinkingSignature); signature != "" {
							part["thoughtSignature"] = signature
						}
						parts = append(parts, part)
					} else {
						parts = append(parts, map[string]any{"text": aiutils.SanitizeSurrogates(block.Thinking)})
					}
				case "toolCall":
					functionCall := map[string]any{"name": block.Name, "args": block.Arguments}
					if requiresGoogleToolCallID(model.ID) {
						functionCall["id"] = block.ID
					}
					part := map[string]any{"functionCall": functionCall}
					if signature := resolveGoogleThoughtSignature(isSameProviderAndModel, block.ThoughtSignature); signature != "" {
						part["thoughtSignature"] = signature
					}
					parts = append(parts, part)
				}
			}
			if len(parts) > 0 {
				contents = append(contents, map[string]any{"role": "model", "parts": parts})
			}
		case "toolResult":
			textBlocks := []string{}
			imageParts := []map[string]any{}
			for _, block := range contentBlocks(msg.Content) {
				if block.Type == "text" {
					textBlocks = append(textBlocks, block.Text)
				} else if block.Type == "image" && modelSupportsImage(model) {
					imageParts = append(imageParts, map[string]any{"inlineData": map[string]any{"mimeType": block.MimeType, "data": block.Data}})
				}
			}
			textResult := strings.Join(textBlocks, "\n")
			hasImages := len(imageParts) > 0
			responseValue := aiutils.SanitizeSurrogates(textResult)
			if responseValue == "" && hasImages {
				responseValue = "(see attached image)"
			}
			response := map[string]any{"output": responseValue}
			if msg.IsError {
				response = map[string]any{"error": responseValue}
			}
			functionResponse := map[string]any{"name": msg.ToolName, "response": response}
			if hasImages && supportsMultimodalGoogleFunctionResponse(model.ID) {
				functionResponse["parts"] = imageParts
			}
			if requiresGoogleToolCallID(model.ID) {
				functionResponse["id"] = msg.ToolCallID
			}
			part := map[string]any{"functionResponse": functionResponse}
			last := len(contents) - 1
			if last >= 0 && contents[last]["role"] == "user" && googlePartsContainFunctionResponse(contents[last]["parts"]) {
				contents[last]["parts"] = append(contents[last]["parts"].([]map[string]any), part)
			} else {
				contents = append(contents, map[string]any{"role": "user", "parts": []map[string]any{part}})
			}
			if hasImages && !supportsMultimodalGoogleFunctionResponse(model.ID) {
				contents = append(contents, map[string]any{"role": "user", "parts": append([]map[string]any{{"text": "Tool result image:"}}, imageParts...)})
			}
		}
	}
	return contents
}

func ConvertGoogleTools(tools []ai.Tool, useParameters bool) []map[string]any {
	if len(tools) == 0 {
		return nil
	}
	declarations := []map[string]any{}
	for _, tool := range tools {
		declaration := map[string]any{"name": tool.Name, "description": tool.Description}
		if useParameters {
			declaration["parameters"] = sanitizeGoogleOpenAPISchema(tool.Parameters)
		} else {
			declaration["parametersJsonSchema"] = tool.Parameters
		}
		declarations = append(declarations, declaration)
	}
	return []map[string]any{{"functionDeclarations": declarations}}
}

func mapGoogleToolChoice(choice string) string {
	switch choice {
	case "auto":
		return "AUTO"
	case "none":
		return "NONE"
	case "any":
		return "ANY"
	default:
		return "AUTO"
	}
}

func mapGoogleStopReason(reason string) ai.StopReason {
	switch reason {
	case "STOP":
		return ai.StopReasonStop
	case "MAX_TOKENS":
		return ai.StopReasonLength
	default:
		return ai.StopReasonError
	}
}

func parseGoogleUsage(raw map[string]any, model ai.Model) ai.Usage {
	input := intFromAny(raw["promptTokenCount"]) - intFromAny(raw["cachedContentTokenCount"])
	usage := ai.Usage{
		Input:           input,
		Output:          intFromAny(raw["candidatesTokenCount"]) + intFromAny(raw["thoughtsTokenCount"]),
		CacheRead:       intFromAny(raw["cachedContentTokenCount"]),
		CacheWrite:      0,
		ReasoningTokens: intFromAny(raw["thoughtsTokenCount"]),
		TotalTokens:     intFromAny(raw["totalTokenCount"]),
	}
	ai.CalculateCost(model, &usage)
	return usage
}

func googlePartsContainFunctionResponse(value any) bool {
	parts, ok := value.([]map[string]any)
	if !ok {
		return false
	}
	for _, part := range parts {
		if _, ok := part["functionResponse"]; ok {
			return true
		}
	}
	return false
}

func supportsMultimodalGoogleFunctionResponse(modelID string) bool {
	version := googleGeminiMajorVersion(modelID)
	if version > 0 {
		return version >= 3
	}
	return true
}

func googleGeminiMajorVersion(modelID string) int {
	match := regexp.MustCompile(`(?i)^gemini(?:-live)?-(\d+)`).FindStringSubmatch(modelID)
	if len(match) < 2 {
		return 0
	}
	version, _ := strconv.Atoi(match[1])
	return version
}

func resolveGoogleThoughtSignature(isSameProviderAndModel bool, signature string) string {
	if !isSameProviderAndModel || signature == "" || len(signature)%4 != 0 || !base64SignaturePattern.MatchString(signature) {
		return ""
	}
	if _, err := base64.StdEncoding.DecodeString(signature); err != nil {
		return ""
	}
	return signature
}

func sanitizeGoogleOpenAPISchema(schema any) any {
	switch value := schema.(type) {
	case map[string]any:
		out := map[string]any{}
		for key, item := range value {
			if isGoogleSchemaMetaKey(key) {
				continue
			}
			out[key] = sanitizeGoogleOpenAPISchema(item)
		}
		return out
	case []any:
		out := make([]any, len(value))
		for index, item := range value {
			out[index] = sanitizeGoogleOpenAPISchema(item)
		}
		return out
	default:
		return value
	}
}

func isGoogleSchemaMetaKey(key string) bool {
	switch key {
	case "$schema", "$id", "$anchor", "$dynamicAnchor", "$vocabulary", "$comment", "$defs", "definitions":
		return true
	default:
		return false
	}
}

func finishGoogleCurrentBlock(stream *ai.AssistantMessageEventStream, output *ai.Message, currentBlockIndex *int) {
	if *currentBlockIndex < 0 {
		return
	}
	blocks := output.Content.([]ai.ContentBlock)
	block := blocks[*currentBlockIndex]
	if block.Type == "text" {
		stream.Push(ai.AssistantMessageEvent{Type: "text_end", ContentIndex: *currentBlockIndex, Content: block.Text, Partial: output})
	} else if block.Type == "thinking" {
		stream.Push(ai.AssistantMessageEvent{Type: "thinking_end", ContentIndex: *currentBlockIndex, Content: block.Thinking, Partial: output})
	}
	*currentBlockIndex = -1
}

func nextGoogleToolCallID(output *ai.Message, name string, providedID string, counter *int) string {
	if providedID != "" {
		blocks := output.Content.([]ai.ContentBlock)
		duplicate := false
		for _, block := range blocks {
			if block.Type == "toolCall" && block.ID == providedID {
				duplicate = true
				break
			}
		}
		if !duplicate {
			return providedID
		}
	}
	*counter = *counter + 1
	return fmt.Sprintf("%s_%d_%d", name, time.Now().UnixMilli(), *counter)
}
