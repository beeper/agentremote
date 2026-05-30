package ai

import "context"

type Api string
type ImagesApi string
type Provider string
type ImagesProvider string
type ThinkingLevel string
type ModelThinkingLevel string
type CacheRetention string
type Transport string
type StopReason string

const (
	ApiOpenAICompletions      Api = "openai-completions"
	ApiMistralConversations   Api = "mistral-conversations"
	ApiOpenAIResponses        Api = "openai-responses"
	ApiAzureOpenAIResponses   Api = "azure-openai-responses"
	ApiOpenAICodexResponses   Api = "openai-codex-responses"
	ApiAnthropicMessages      Api = "anthropic-messages"
	ApiBedrockConverseStream  Api = "bedrock-converse-stream"
	ApiGoogleGenerativeAI     Api = "google-generative-ai"
	ApiGoogleVertex           Api = "google-vertex"
	ImagesApiOpenRouterImages Api = "openrouter-images"

	ImagesApiOpenRouter ImagesApi = "openrouter-images"

	ProviderAmazonBedrock        Provider = "amazon-bedrock"
	ProviderAnthropic            Provider = "anthropic"
	ProviderGoogle               Provider = "google"
	ProviderGoogleVertex         Provider = "google-vertex"
	ProviderOpenAI               Provider = "openai"
	ProviderAzureOpenAIResponses Provider = "azure-openai-responses"
	ProviderOpenAICodex          Provider = "openai-codex"
	ProviderDeepSeek             Provider = "deepseek"
	ProviderGitHubCopilot        Provider = "github-copilot"
	ProviderXAI                  Provider = "xai"
	ProviderGroq                 Provider = "groq"
	ProviderCerebras             Provider = "cerebras"
	ProviderOpenRouter           Provider = "openrouter"
	ProviderVercelAIGateway      Provider = "vercel-ai-gateway"
	ProviderZai                  Provider = "zai"
	ProviderMistral              Provider = "mistral"
	ProviderMinimax              Provider = "minimax"
	ProviderMinimaxCN            Provider = "minimax-cn"
	ProviderMoonshotAI           Provider = "moonshotai"
	ProviderMoonshotAICN         Provider = "moonshotai-cn"
	ProviderHuggingFace          Provider = "huggingface"
	ProviderFireworks            Provider = "fireworks"
	ProviderTogether             Provider = "together"
	ProviderOpencode             Provider = "opencode"
	ProviderOpencodeGo           Provider = "opencode-go"
	ProviderKimiCoding           Provider = "kimi-coding"
	ProviderCloudflareWorkersAI  Provider = "cloudflare-workers-ai"
	ProviderCloudflareAIGateway  Provider = "cloudflare-ai-gateway"
	ProviderXiaomi               Provider = "xiaomi"
	ProviderXiaomiTokenPlanCN    Provider = "xiaomi-token-plan-cn"
	ProviderXiaomiTokenPlanAMS   Provider = "xiaomi-token-plan-ams"
	ProviderXiaomiTokenPlanSGP   Provider = "xiaomi-token-plan-sgp"

	ImagesProviderOpenRouter ImagesProvider = "openrouter"

	ThinkingLevelMinimal ThinkingLevel = "minimal"
	ThinkingLevelLow     ThinkingLevel = "low"
	ThinkingLevelMedium  ThinkingLevel = "medium"
	ThinkingLevelHigh    ThinkingLevel = "high"
	ThinkingLevelXHigh   ThinkingLevel = "xhigh"

	ModelThinkingLevelOff ModelThinkingLevel = "off"

	CacheRetentionNone  CacheRetention = "none"
	CacheRetentionShort CacheRetention = "short"
	CacheRetentionLong  CacheRetention = "long"

	TransportSSE             Transport = "sse"
	TransportWebSocket       Transport = "websocket"
	TransportWebSocketCached Transport = "websocket-cached"
	TransportAuto            Transport = "auto"

	StopReasonStop    StopReason = "stop"
	StopReasonLength  StopReason = "length"
	StopReasonToolUse StopReason = "toolUse"
	StopReasonError   StopReason = "error"
	StopReasonAborted StopReason = "aborted"
)

type ThinkingBudgets struct {
	Minimal *int `json:"minimal,omitempty"`
	Low     *int `json:"low,omitempty"`
	Medium  *int `json:"medium,omitempty"`
	High    *int `json:"high,omitempty"`
}

type ProviderResponse struct {
	Status  int               `json:"status"`
	Headers map[string]string `json:"headers"`
}

type StreamOptions struct {
	Temperature     *float64
	MaxTokens       *int
	APIKey          string
	Transport       Transport
	CacheRetention  CacheRetention
	SessionID       string
	Headers         map[string]string
	TimeoutMs       *int
	MaxRetries      *int
	MaxRetryDelayMs *int
	Metadata        map[string]any
	OnPayload       func(payload any, model Model) (any, bool, error)
	OnResponse      func(response ProviderResponse, model Model) error
}

type SimpleStreamOptions struct {
	StreamOptions
	Reasoning       *ThinkingLevel
	ThinkingBudgets *ThinkingBudgets
}

type ImagesOptions struct {
	APIKey          string
	Headers         map[string]string
	TimeoutMs       *int
	MaxRetries      *int
	MaxRetryDelayMs *int
	Metadata        map[string]any
	OnPayload       func(payload any, model ImagesModel) (any, bool, error)
	OnResponse      func(response ProviderResponse, model ImagesModel) error
}

type Model struct {
	ID                   string                         `json:"id"`
	Name                 string                         `json:"name"`
	API                  Api                            `json:"api"`
	Provider             Provider                       `json:"provider"`
	BaseURL              string                         `json:"baseUrl"`
	Reasoning            bool                           `json:"reasoning"`
	ThinkingLevelMap     map[ModelThinkingLevel]*string `json:"thinkingLevelMap,omitempty"`
	DefaultThinkingLevel ModelThinkingLevel             `json:"defaultThinkingLevel,omitempty"`
	Input                []string                       `json:"input"`
	Cost                 ModelCost                      `json:"cost"`
	ContextWindow        int                            `json:"contextWindow"`
	MaxTokens            int                            `json:"maxTokens"`
	Headers              map[string]string              `json:"headers,omitempty"`
	Compat               map[string]any                 `json:"compat,omitempty"`
}

type ImagesModel struct {
	ID       string            `json:"id"`
	Name     string            `json:"name"`
	API      ImagesApi         `json:"api"`
	Provider ImagesProvider    `json:"provider"`
	BaseURL  string            `json:"baseUrl"`
	Input    []string          `json:"input"`
	Output   []string          `json:"output"`
	Cost     ModelCost         `json:"cost"`
	Headers  map[string]string `json:"headers,omitempty"`
}

type ModelCost struct {
	Input      float64 `json:"input"`
	Output     float64 `json:"output"`
	CacheRead  float64 `json:"cacheRead"`
	CacheWrite float64 `json:"cacheWrite"`
}

type Usage struct {
	Input           int       `json:"input"`
	Output          int       `json:"output"`
	CacheRead       int       `json:"cacheRead"`
	CacheWrite      int       `json:"cacheWrite"`
	ReasoningTokens int       `json:"reasoningTokens,omitempty"`
	TotalTokens     int       `json:"totalTokens"`
	Cost            UsageCost `json:"cost"`
}

type UsageCost struct {
	Input      float64 `json:"input"`
	Output     float64 `json:"output"`
	CacheRead  float64 `json:"cacheRead"`
	CacheWrite float64 `json:"cacheWrite"`
	Total      float64 `json:"total"`
}

type ContentBlock struct {
	Type              string         `json:"type"`
	Text              string         `json:"text,omitempty"`
	TextSignature     string         `json:"textSignature,omitempty"`
	Thinking          string         `json:"thinking,omitempty"`
	ThinkingSignature string         `json:"thinkingSignature,omitempty"`
	Redacted          bool           `json:"redacted,omitempty"`
	Data              string         `json:"data,omitempty"`
	MimeType          string         `json:"mimeType,omitempty"`
	ID                string         `json:"id,omitempty"`
	Name              string         `json:"name,omitempty"`
	Arguments         map[string]any `json:"arguments,omitempty"`
	ThoughtSignature  string         `json:"thoughtSignature,omitempty"`
}

type ImagesContext struct {
	Input []ContentBlock `json:"input"`
}

type ImagesStopReason string

const (
	ImagesStopReasonStop    ImagesStopReason = "stop"
	ImagesStopReasonError   ImagesStopReason = "error"
	ImagesStopReasonAborted ImagesStopReason = "aborted"
)

type AssistantImages struct {
	API          ImagesApi        `json:"api"`
	Provider     ImagesProvider   `json:"provider"`
	Model        string           `json:"model"`
	Output       []ContentBlock   `json:"output"`
	ResponseID   string           `json:"responseId,omitempty"`
	Usage        Usage            `json:"usage,omitempty"`
	StopReason   ImagesStopReason `json:"stopReason"`
	ErrorMessage string           `json:"errorMessage,omitempty"`
	Timestamp    int64            `json:"timestamp"`
}

type Message struct {
	Role          string     `json:"role"`
	Content       any        `json:"content"`
	Timestamp     int64      `json:"timestamp"`
	API           Api        `json:"api,omitempty"`
	Provider      Provider   `json:"provider,omitempty"`
	Model         string     `json:"model,omitempty"`
	ResponseModel string     `json:"responseModel,omitempty"`
	ResponseID    string     `json:"responseId,omitempty"`
	Diagnostics   []any      `json:"diagnostics,omitempty"`
	Usage         Usage      `json:"usage,omitempty"`
	StopReason    StopReason `json:"stopReason,omitempty"`
	ErrorMessage  string     `json:"errorMessage,omitempty"`
	ToolCallID    string     `json:"toolCallId,omitempty"`
	ToolName      string     `json:"toolName,omitempty"`
	Details       any        `json:"details,omitempty"`
	IsError       bool       `json:"isError,omitempty"`
	CustomType    string     `json:"customType,omitempty"`
	Display       bool       `json:"display,omitempty"`
	Summary       string     `json:"summary,omitempty"`
	FromID        string     `json:"fromId,omitempty"`
	TokensBefore  int        `json:"tokensBefore,omitempty"`
	Truncated     bool       `json:"truncated,omitempty"`
}

type Tool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"`
}

type Context struct {
	SystemPrompt string    `json:"systemPrompt,omitempty"`
	Messages     []Message `json:"messages"`
	Tools        []Tool    `json:"tools,omitempty"`
}

type ToolCall struct {
	Type             string         `json:"type"`
	ID               string         `json:"id"`
	Name             string         `json:"name"`
	Arguments        map[string]any `json:"arguments"`
	ThoughtSignature string         `json:"thoughtSignature,omitempty"`
}

type AssistantMessageEvent struct {
	Type         string     `json:"type"`
	ContentIndex int        `json:"contentIndex,omitempty"`
	Delta        string     `json:"delta,omitempty"`
	Content      string     `json:"content,omitempty"`
	Partial      *Message   `json:"partial,omitempty"`
	ToolCall     *ToolCall  `json:"toolCall,omitempty"`
	Reason       StopReason `json:"reason,omitempty"`
	Message      *Message   `json:"message,omitempty"`
	Error        *Message   `json:"error,omitempty"`
	RawEvent     any        `json:"rawEvent,omitempty"`
	RawSource    string     `json:"rawSource,omitempty"`
	CustomName   string     `json:"customName,omitempty"`
	CustomValue  any        `json:"customValue,omitempty"`
}

type APIStreamFunction func(context.Context, Model, Context, StreamOptions) *AssistantMessageEventStream
type APIStreamSimpleFunction func(context.Context, Model, Context, SimpleStreamOptions) *AssistantMessageEventStream
type StreamFunction = APIStreamSimpleFunction

type APIProvider struct {
	API          Api
	Stream       APIStreamFunction
	StreamSimple APIStreamSimpleFunction
}

type OpenAICompletionsCompat struct {
	SupportsStore                               *bool                 `json:"supportsStore,omitempty"`
	SupportsDeveloperRole                       *bool                 `json:"supportsDeveloperRole,omitempty"`
	SupportsReasoningEffort                     *bool                 `json:"supportsReasoningEffort,omitempty"`
	SupportsUsageInStreaming                    *bool                 `json:"supportsUsageInStreaming,omitempty"`
	MaxTokensField                              string                `json:"maxTokensField,omitempty"`
	RequiresToolResultName                      *bool                 `json:"requiresToolResultName,omitempty"`
	RequiresAssistantAfterToolResult            *bool                 `json:"requiresAssistantAfterToolResult,omitempty"`
	RequiresThinkingAsText                      *bool                 `json:"requiresThinkingAsText,omitempty"`
	RequiresReasoningContentOnAssistantMessages *bool                 `json:"requiresReasoningContentOnAssistantMessages,omitempty"`
	ThinkingFormat                              string                `json:"thinkingFormat,omitempty"`
	OpenRouterRouting                           *OpenRouterRouting    `json:"openRouterRouting,omitempty"`
	VercelGatewayRouting                        *VercelGatewayRouting `json:"vercelGatewayRouting,omitempty"`
	ZaiToolStream                               *bool                 `json:"zaiToolStream,omitempty"`
	SupportsStrictMode                          *bool                 `json:"supportsStrictMode,omitempty"`
	CacheControlFormat                          string                `json:"cacheControlFormat,omitempty"`
	SendSessionAffinityHeaders                  *bool                 `json:"sendSessionAffinityHeaders,omitempty"`
	SupportsLongCacheRetention                  *bool                 `json:"supportsLongCacheRetention,omitempty"`
}

type OpenAIResponsesCompat struct {
	SendSessionIDHeader        *bool `json:"sendSessionIdHeader,omitempty"`
	SupportsLongCacheRetention *bool `json:"supportsLongCacheRetention,omitempty"`
}

type AnthropicMessagesCompat struct {
	SupportsEagerToolInputStreaming *bool `json:"supportsEagerToolInputStreaming,omitempty"`
	SupportsLongCacheRetention      *bool `json:"supportsLongCacheRetention,omitempty"`
	SendSessionAffinityHeaders      *bool `json:"sendSessionAffinityHeaders,omitempty"`
	SupportsCacheControlOnTools     *bool `json:"supportsCacheControlOnTools,omitempty"`
	ForceAdaptiveThinking           *bool `json:"forceAdaptiveThinking,omitempty"`
	AllowEmptySignature             *bool `json:"allowEmptySignature,omitempty"`
}

type OpenRouterRouting struct {
	AllowFallbacks         *bool               `json:"allow_fallbacks,omitempty"`
	RequireParameters      *bool               `json:"require_parameters,omitempty"`
	DataCollection         string              `json:"data_collection,omitempty"`
	ZDR                    *bool               `json:"zdr,omitempty"`
	EnforceDistillableText *bool               `json:"enforce_distillable_text,omitempty"`
	Order                  []string            `json:"order,omitempty"`
	Only                   []string            `json:"only,omitempty"`
	Ignore                 []string            `json:"ignore,omitempty"`
	Quantizations          []string            `json:"quantizations,omitempty"`
	Sort                   any                 `json:"sort,omitempty"`
	MaxPrice               *OpenRouterMaxPrice `json:"max_price,omitempty"`
	PreferredMinThroughput any                 `json:"preferred_min_throughput,omitempty"`
	PreferredMaxLatency    any                 `json:"preferred_max_latency,omitempty"`
}

type OpenRouterMaxPrice struct {
	Prompt     any `json:"prompt,omitempty"`
	Completion any `json:"completion,omitempty"`
	Image      any `json:"image,omitempty"`
	Audio      any `json:"audio,omitempty"`
	Request    any `json:"request,omitempty"`
}

type VercelGatewayRouting struct {
	Only  []string `json:"only,omitempty"`
	Order []string `json:"order,omitempty"`
}
