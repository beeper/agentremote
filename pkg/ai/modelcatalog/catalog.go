package modelcatalog

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strings"

	"github.com/beeper/ai-bridge/pkg/ai"
)

const (
	ModelsDevURL        = "https://models.dev/api.json"
	OpenRouterModelsURL = "https://openrouter.ai/api/v1/models"
	VertexBaseURL       = "https://{location}-aiplatform.googleapis.com"
)

var (
	DefaultProviders = []ai.Provider{
		ai.ProviderOpenAI,
		ai.ProviderOpenRouter,
		ai.ProviderAnthropic,
		ai.ProviderGoogleVertex,
	}

	DefaultRegisteredAPIs = []ai.Api{
		ai.ApiAnthropicMessages,
		ai.ApiOpenAICompletions,
		ai.ApiOpenAIResponses,
		ai.ApiOpenAICodexResponses,
		ai.ApiGoogleVertex,
	}

	gemini3ProPattern   = regexp.MustCompile(`(?i)gemini-3(?:\.\d+)?-pro`)
	gemini3FlashPattern = regexp.MustCompile(`(?i)gemini-3(?:\.\d+)?-flash`)
)

type Options struct {
	HTTPClient          *http.Client
	ModelsDevURL        string
	OpenRouterModelsURL string
	Providers           []ai.Provider
	RegisteredAPIs      []ai.Api
	IncludeUnregistered bool
}

type Catalog struct {
	Models        map[ai.Provider]map[string]ai.Model
	ProviderOrder []ai.Provider
	ModelIDOrder  map[ai.Provider][]string
	Skipped       []ai.Provider
}

func Build(ctx context.Context, opts Options) (Catalog, error) {
	opts = opts.withDefaults()
	modelsDev, openRouter, err := fetchSources(ctx, opts)
	if err != nil {
		return Catalog{}, err
	}

	models := make([]ai.Model, 0)
	for _, provider := range []ai.Provider{ai.ProviderOpenAI, ai.ProviderAnthropic, ai.ProviderGoogleVertex} {
		sourceModels := modelsDev[string(provider)].Models
		for _, sourceModel := range sourceModels {
			if !isToolCapableModelsDev(sourceModel) {
				continue
			}
			models = append(models, applyModelMetadata(normalizeModelsDevModel(provider, sourceModel)))
		}
	}
	for _, sourceModel := range openRouter.Data {
		if !isToolCapableOpenRouter(sourceModel) {
			continue
		}
		models = append(models, applyModelMetadata(normalizeOpenRouterModel(sourceModel)))
	}
	return BuildFromModels(models, opts), nil
}

func BuildFromModels(models []ai.Model, opts Options) Catalog {
	opts = opts.withDefaults()
	targetProviders := providerSet(opts.Providers)
	registeredAPIs := apiSet(opts.RegisteredAPIs)

	allProviders := make(map[ai.Provider]bool)
	out := make(map[ai.Provider]map[string]ai.Model)
	for _, model := range models {
		if !targetProviders[model.Provider] {
			continue
		}
		allProviders[model.Provider] = true
		if !opts.IncludeUnregistered && !registeredAPIs[model.API] {
			continue
		}
		if out[model.Provider] == nil {
			out[model.Provider] = make(map[string]ai.Model)
		}
		out[model.Provider][model.ID] = model
	}

	providerOrder := sortedProviders(out)
	modelIDOrder := make(map[ai.Provider][]string, len(out))
	for provider, providerModels := range out {
		modelIDOrder[provider] = sortedModelIDs(providerModels)
	}

	skipped := make([]ai.Provider, 0)
	emittedProviders := providerSet(providerOrder)
	for provider := range allProviders {
		if !emittedProviders[provider] {
			skipped = append(skipped, provider)
		}
	}
	sort.Slice(skipped, func(i, j int) bool { return skipped[i] < skipped[j] })

	return Catalog{
		Models:        out,
		ProviderOrder: providerOrder,
		ModelIDOrder:  modelIDOrder,
		Skipped:       skipped,
	}
}

func (catalog Catalog) Count() int {
	count := 0
	for _, providerModels := range catalog.Models {
		count += len(providerModels)
	}
	return count
}

func GenerateGoSource(catalog Catalog, packageName string) ([]byte, error) {
	if packageName == "" {
		packageName = "ai"
	}
	modelsJSON, err := json.Marshal(catalog.Models)
	if err != nil {
		return nil, fmt.Errorf("marshal models: %w", err)
	}
	modelIDOrderJSON, err := json.Marshal(catalog.ModelIDOrder)
	if err != nil {
		return nil, fmt.Errorf("marshal model id order: %w", err)
	}

	var buf bytes.Buffer
	fmt.Fprintf(&buf, "package %s\n\n", packageName)
	buf.WriteString("import \"encoding/json\"\n\n")
	fmt.Fprintf(&buf, "var modelsJSON = `%s`\n\n", escapeRawString(modelsJSON))
	buf.WriteString("var modelProviderOrder = []Provider{\n")
	for _, provider := range catalog.ProviderOrder {
		encoded, _ := json.Marshal(provider)
		fmt.Fprintf(&buf, "\t%s,\n", encoded)
	}
	buf.WriteString("}\n\n")
	fmt.Fprintf(&buf, "var modelIDOrderJSON = `%s`\n\n", escapeRawString(modelIDOrderJSON))
	buf.WriteString(`var Models = mustLoadModels()
var modelIDOrder = mustLoadModelIDOrder()

func mustLoadModels() map[Provider]map[string]Model {
	var raw map[Provider]map[string]Model
	if err := json.Unmarshal([]byte(modelsJSON), &raw); err != nil {
		panic(err)
	}
	return raw
}

func mustLoadModelIDOrder() map[Provider][]string {
	var raw map[Provider][]string
	if err := json.Unmarshal([]byte(modelIDOrderJSON), &raw); err != nil {
		panic(err)
	}
	return raw
}
`)
	return buf.Bytes(), nil
}

func fetchSources(ctx context.Context, opts Options) (modelsDevResponse, openRouterResponse, error) {
	var modelsDev modelsDevResponse
	var openRouter openRouterResponse
	if err := fetchJSON(ctx, opts.HTTPClient, opts.ModelsDevURL, &modelsDev); err != nil {
		return nil, openRouterResponse{}, fmt.Errorf("fetch models.dev catalog: %w", err)
	}
	if err := fetchJSON(ctx, opts.HTTPClient, opts.OpenRouterModelsURL, &openRouter); err != nil {
		return nil, openRouterResponse{}, fmt.Errorf("fetch OpenRouter catalog: %w", err)
	}
	return modelsDev, openRouter, nil
}

func fetchJSON(ctx context.Context, client *http.Client, url string, target any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("%s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	return json.NewDecoder(resp.Body).Decode(target)
}

func (opts Options) withDefaults() Options {
	if opts.HTTPClient == nil {
		opts.HTTPClient = http.DefaultClient
	}
	if opts.ModelsDevURL == "" {
		opts.ModelsDevURL = ModelsDevURL
	}
	if opts.OpenRouterModelsURL == "" {
		opts.OpenRouterModelsURL = OpenRouterModelsURL
	}
	if len(opts.Providers) == 0 {
		opts.Providers = append([]ai.Provider{}, DefaultProviders...)
	}
	if len(opts.RegisteredAPIs) == 0 {
		opts.RegisteredAPIs = append([]ai.Api{}, DefaultRegisteredAPIs...)
	}
	return opts
}

func normalizeModelsDevModel(provider ai.Provider, model modelsDevModel) ai.Model {
	api := ai.ApiGoogleVertex
	baseURL := VertexBaseURL
	switch provider {
	case ai.ProviderOpenAI:
		api = openAIAPI(model.ID)
		baseURL = "https://api.openai.com/v1"
	case ai.ProviderAnthropic:
		api = ai.ApiAnthropicMessages
		baseURL = "https://api.anthropic.com"
	}
	name := model.Name
	if name == "" {
		name = model.ID
	}
	if provider == ai.ProviderGoogleVertex {
		name += " (Vertex)"
	}
	return ai.Model{
		ID:            model.ID,
		Name:          name,
		API:           api,
		Provider:      provider,
		BaseURL:       baseURL,
		Reasoning:     model.Reasoning,
		Input:         textImageInput(model.Modalities.Input),
		Cost:          costFromModelsDev(model.Cost),
		ContextWindow: intOrDefault(model.Limit.Context, 128000),
		MaxTokens:     intOrDefault(model.Limit.Output, 16384),
	}
}

func normalizeOpenRouterModel(model openRouterModel) ai.Model {
	return ai.Model{
		ID:            model.ID,
		Name:          stringOrDefault(model.Name, model.ID),
		API:           ai.ApiOpenAICompletions,
		Provider:      ai.ProviderOpenRouter,
		BaseURL:       "https://openrouter.ai/api/v1",
		Reasoning:     contains(model.SupportedParameters, "reasoning") || contains(model.SupportedParameters, "include_reasoning"),
		Input:         textImageInput(model.Architecture.InputModalities),
		Cost:          costFromOpenRouter(model.Pricing),
		ContextWindow: intOrDefault(firstNonZero(model.ContextLength, model.TopProvider.ContextLength), 128000),
		MaxTokens:     intOrDefault(model.TopProvider.MaxCompletionTokens, 16384),
	}
}

func applyModelMetadata(model ai.Model) ai.Model {
	if (model.API == ai.ApiOpenAIResponses || model.API == ai.ApiOpenAICodexResponses) && strings.HasPrefix(model.ID, "gpt-5") {
		model.ThinkingLevelMap = mergeThinkingLevelMap(model.ThinkingLevelMap, map[ai.ModelThinkingLevel]*string{
			ai.ModelThinkingLevelOff: nil,
		})
	}
	if strings.Contains(model.ID, "gpt-5.2") || strings.Contains(model.ID, "gpt-5.3") || strings.Contains(model.ID, "gpt-5.4") || strings.Contains(model.ID, "gpt-5.5") {
		xhigh := string(ai.ModelThinkingLevelXHigh)
		model.ThinkingLevelMap = mergeThinkingLevelMap(model.ThinkingLevelMap, map[ai.ModelThinkingLevel]*string{
			ai.ModelThinkingLevelXHigh: &xhigh,
		})
	}
	if model.Provider == ai.ProviderOpenRouter && strings.HasPrefix(model.ID, "anthropic/") {
		model.Compat = mergeCompat(model.Compat, map[string]any{"cacheControlFormat": "anthropic"})
	}
	if model.Provider == ai.ProviderGoogleVertex && gemini3ProPattern.MatchString(model.ID) {
		low := "LOW"
		high := "HIGH"
		model.ThinkingLevelMap = mergeThinkingLevelMap(model.ThinkingLevelMap, map[ai.ModelThinkingLevel]*string{
			ai.ModelThinkingLevelOff:     nil,
			ai.ModelThinkingLevelMinimal: nil,
			ai.ModelThinkingLevelLow:     &low,
			ai.ModelThinkingLevelMedium:  nil,
			ai.ModelThinkingLevelHigh:    &high,
		})
	}
	if model.Provider == ai.ProviderGoogleVertex && gemini3FlashPattern.MatchString(model.ID) {
		model.ThinkingLevelMap = mergeThinkingLevelMap(model.ThinkingLevelMap, map[ai.ModelThinkingLevel]*string{
			ai.ModelThinkingLevelOff: nil,
		})
	}
	return model
}

func openAIAPI(modelID string) ai.Api {
	if strings.HasPrefix(modelID, "gpt-5") || strings.HasPrefix(modelID, "o") {
		return ai.ApiOpenAIResponses
	}
	return ai.ApiOpenAICompletions
}

func costFromModelsDev(cost modelsDevCost) ai.ModelCost {
	return ai.ModelCost{
		Input:      cost.Input,
		Output:     cost.Output,
		CacheRead:  cost.CacheRead,
		CacheWrite: cost.CacheWrite,
	}
}

func costFromOpenRouter(pricing openRouterPricing) ai.ModelCost {
	return ai.ModelCost{
		Input:      pricing.Prompt * 1_000_000,
		Output:     pricing.Completion * 1_000_000,
		CacheRead:  pricing.InputCacheRead * 1_000_000,
		CacheWrite: pricing.InputCacheWrite * 1_000_000,
	}
}

func textImageInput(modalities []string) []string {
	input := make([]string, 0, 2)
	if len(modalities) == 0 || contains(modalities, "text") {
		input = append(input, "text")
	}
	if contains(modalities, "image") {
		input = append(input, "image")
	}
	return input
}

func isToolCapableModelsDev(model modelsDevModel) bool {
	return model.ToolCall && contains(model.Modalities.Output, "text")
}

func isToolCapableOpenRouter(model openRouterModel) bool {
	return contains(model.SupportedParameters, "tools")
}

func providerSet(providers []ai.Provider) map[ai.Provider]bool {
	out := make(map[ai.Provider]bool, len(providers))
	for _, provider := range providers {
		out[provider] = true
	}
	return out
}

func apiSet(apis []ai.Api) map[ai.Api]bool {
	out := make(map[ai.Api]bool, len(apis))
	for _, api := range apis {
		out[api] = true
	}
	return out
}

func sortedProviders(models map[ai.Provider]map[string]ai.Model) []ai.Provider {
	providers := make([]ai.Provider, 0, len(models))
	for provider := range models {
		providers = append(providers, provider)
	}
	sort.Slice(providers, func(i, j int) bool { return providers[i] < providers[j] })
	return providers
}

func sortedModelIDs(models map[string]ai.Model) []string {
	ids := make([]string, 0, len(models))
	for id := range models {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

func mergeThinkingLevelMap(base, overlay map[ai.ModelThinkingLevel]*string) map[ai.ModelThinkingLevel]*string {
	out := make(map[ai.ModelThinkingLevel]*string, len(base)+len(overlay))
	for key, value := range base {
		out[key] = value
	}
	for key, value := range overlay {
		out[key] = value
	}
	return out
}

func mergeCompat(base, overlay map[string]any) map[string]any {
	out := make(map[string]any, len(base)+len(overlay))
	for key, value := range base {
		out[key] = value
	}
	for key, value := range overlay {
		out[key] = value
	}
	return out
}

func escapeRawString(data []byte) []byte {
	return bytes.ReplaceAll(data, []byte("`"), []byte("`+\"`\"+`"))
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func firstNonZero(values ...int) int {
	for _, value := range values {
		if value != 0 {
			return value
		}
	}
	return 0
}

func intOrDefault(value, fallback int) int {
	if value != 0 {
		return value
	}
	return fallback
}

func stringOrDefault(value, fallback string) string {
	if value != "" {
		return value
	}
	return fallback
}
