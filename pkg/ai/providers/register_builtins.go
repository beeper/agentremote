package providers

import (
	"context"

	ai "github.com/beeper/ai-bridge/pkg/ai"
)

func RegisterBuiltInAPIProviders() {
	ai.RegisterAPIProviderWithSource(ai.APIProvider{API: ai.ApiAnthropicMessages, Stream: func(ctx context.Context, model ai.Model, llmContext ai.Context, options ai.StreamOptions) *ai.AssistantMessageEventStream {
		return StreamAnthropic(ctx, model, llmContext, AnthropicOptions{StreamOptions: options})
	}, StreamSimple: StreamSimpleAnthropic}, "builtins")
	ai.RegisterAPIProviderWithSource(ai.APIProvider{API: ai.ApiOpenAICompletions, Stream: func(ctx context.Context, model ai.Model, llmContext ai.Context, options ai.StreamOptions) *ai.AssistantMessageEventStream {
		return StreamOpenAICompletions(ctx, model, llmContext, OpenAICompletionsOptions{StreamOptions: options})
	}, StreamSimple: StreamSimpleOpenAICompletions}, "builtins")
	ai.RegisterAPIProviderWithSource(ai.APIProvider{API: ai.ApiOpenAIResponses, Stream: func(ctx context.Context, model ai.Model, llmContext ai.Context, options ai.StreamOptions) *ai.AssistantMessageEventStream {
		return StreamOpenAIResponses(ctx, model, llmContext, OpenAIResponsesOptions{StreamOptions: options})
	}, StreamSimple: StreamSimpleOpenAIResponses}, "builtins")
	ai.RegisterAPIProviderWithSource(ai.APIProvider{API: ai.ApiOpenAICodexResponses, Stream: func(ctx context.Context, model ai.Model, llmContext ai.Context, options ai.StreamOptions) *ai.AssistantMessageEventStream {
		return StreamOpenAICodexResponses(ctx, model, llmContext, OpenAICodexResponsesOptions{OpenAIResponsesOptions: OpenAIResponsesOptions{StreamOptions: options}})
	}, StreamSimple: StreamSimpleOpenAICodexResponses}, "builtins")
	ai.RegisterAPIProviderWithSource(ai.APIProvider{API: ai.ApiGoogleVertex, Stream: func(ctx context.Context, model ai.Model, llmContext ai.Context, options ai.StreamOptions) *ai.AssistantMessageEventStream {
		return StreamGoogleVertex(ctx, model, llmContext, GoogleVertexOptions{StreamOptions: options})
	}, StreamSimple: StreamSimpleGoogleVertex}, "builtins")
}

func ResetAPIProviders() {
	ai.ClearAPIProviders()
	RegisterBuiltInAPIProviders()
}

func init() {
	RegisterBuiltInAPIProviders()
}
