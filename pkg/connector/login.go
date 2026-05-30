package connector

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	ai "github.com/beeper/ai-bridge/pkg/ai"
	"github.com/beeper/ai-bridge/pkg/aiid"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/status"
)

const (
	loginFlowDefaultProvider      = "beeper"
	loginFlowOpenAIResponses      = "openai-responses"
	loginFlowOpenAICompletions    = "openai-completions"
	loginFlowOpenAICodexResponses = "openai-codex-responses"
	loginFlowChatGPTDevice        = "chatgpt-device"
	loginStepDefault              = "com.beeper.ai.login.default"
	loginStepProviderConfig       = "com.beeper.ai.login.provider.config"
	loginStepProviderDefault      = "com.beeper.ai.login.provider.default_model"
	loginStepComplete             = "com.beeper.ai.login.complete"
)

func (c *Connector) GetLoginFlows() []bridgev2.LoginFlow {
	return []bridgev2.LoginFlow{{
		Name:        "Beeper AI",
		Description: "Use the default Beeper AI provider",
		ID:          loginFlowDefaultProvider,
	}, {
		Name:        "OpenAI Responses",
		Description: "Add a provider using the OpenAI Responses API",
		ID:          loginFlowOpenAIResponses,
	}, {
		Name:        "OpenAI Chat Completions",
		Description: "Add a provider using the OpenAI chat completions API",
		ID:          loginFlowOpenAICompletions,
	}, {
		Name:        "OpenAI Codex Responses",
		Description: "Add a provider using the OpenAI Codex Responses API",
		ID:          loginFlowOpenAICodexResponses,
	}, {
		Name:        "ChatGPT",
		Description: "Log in with ChatGPT using a browser device code",
		ID:          loginFlowChatGPTDevice,
	}}
}

func (c *Connector) CreateLogin(ctx context.Context, user *bridgev2.User, flowID string) (bridgev2.LoginProcess, error) {
	switch flowID {
	case loginFlowDefaultProvider:
		return &DefaultProviderLogin{Main: c, User: user}, nil
	case loginFlowOpenAIResponses:
		return &CustomProviderLogin{Main: c, User: user, config: providerLoginConfig{API: ai.ApiOpenAIResponses}}, nil
	case loginFlowOpenAICompletions:
		return &CustomProviderLogin{Main: c, User: user, config: providerLoginConfig{API: ai.ApiOpenAICompletions}}, nil
	case loginFlowOpenAICodexResponses:
		return &CustomProviderLogin{Main: c, User: user, config: providerLoginConfig{API: ai.ApiOpenAICodexResponses}}, nil
	case loginFlowChatGPTDevice:
		return &ChatGPTDeviceLogin{Main: c, User: user}, nil
	default:
		return nil, fmt.Errorf("invalid login flow ID")
	}
}

type DefaultProviderLogin struct {
	Main *Connector
	User *bridgev2.User
}

var _ bridgev2.LoginProcess = (*DefaultProviderLogin)(nil)

func (l *DefaultProviderLogin) Start(ctx context.Context) (*bridgev2.LoginStep, error) {
	login, err := l.Main.EnsureDefaultLogin(ctx, l.User)
	if err != nil {
		return nil, err
	}
	return &bridgev2.LoginStep{
		Type:         bridgev2.LoginStepTypeComplete,
		StepID:       loginStepDefault,
		Instructions: "AI bridge login ready",
		CompleteParams: &bridgev2.LoginCompleteParams{
			UserLoginID: login.ID,
			UserLogin:   login,
		},
	}, nil
}

func (l *DefaultProviderLogin) Cancel() {
}

type CustomProviderLogin struct {
	Main   *Connector
	User   *bridgev2.User
	config providerLoginConfig
}

var _ bridgev2.LoginProcessUserInput = (*CustomProviderLogin)(nil)

func (l *CustomProviderLogin) Start(ctx context.Context) (*bridgev2.LoginStep, error) {
	return l.providerConfigStep(), nil
}

func (l *CustomProviderLogin) SubmitUserInput(ctx context.Context, input map[string]string) (*bridgev2.LoginStep, error) {
	if _, ok := input["default_model"]; ok {
		return l.submitDefaultModel(ctx, input)
	}
	return l.submitProviderConfig(ctx, input)
}

func (l *CustomProviderLogin) providerConfigStep() *bridgev2.LoginStep {
	return &bridgev2.LoginStep{
		Type:         bridgev2.LoginStepTypeUserInput,
		StepID:       loginStepProviderConfig,
		Instructions: "Enter provider connection details",
		UserInputParams: &bridgev2.LoginUserInputParams{Fields: []bridgev2.LoginInputDataField{
			{Type: bridgev2.LoginInputFieldTypeUsername, ID: "provider_id", Name: "ID", Description: "Stable provider ID"},
			{Type: bridgev2.LoginInputFieldTypeURL, ID: "base_url", Name: "Base URL", Description: "OpenAI-compatible API base URL", DefaultValue: defaultBaseURLForAPI(l.config.API)},
			{Type: bridgev2.LoginInputFieldTypeToken, ID: "api_key", Name: "API key", Description: "Provider API key"},
		}},
	}
}

func (l *CustomProviderLogin) submitProviderConfig(ctx context.Context, input map[string]string) (*bridgev2.LoginStep, error) {
	providerID := strings.TrimSpace(input["provider_id"])
	baseURL := normalizeResponsesBaseURL(strings.TrimSpace(input["base_url"]))
	apiKey := strings.TrimSpace(input["api_key"])
	if providerID == "" || baseURL == "" || apiKey == "" {
		return nil, fmt.Errorf("provider_id, base_url and api_key are required")
	}
	models, err := fetchProviderModels(ctx, l.config.API, providerID, baseURL, apiKey)
	if err != nil {
		return nil, err
	}
	l.config.ProviderID = providerID
	l.config.BaseURL = baseURL
	l.config.APIKey = apiKey
	l.config.Models = models
	options := make([]string, 0, len(models))
	for _, model := range models {
		options = append(options, model.ID)
	}
	return &bridgev2.LoginStep{
		Type:         bridgev2.LoginStepTypeUserInput,
		StepID:       loginStepProviderDefault,
		Instructions: "Choose the default model",
		UserInputParams: &bridgev2.LoginUserInputParams{Fields: []bridgev2.LoginInputDataField{
			{Type: bridgev2.LoginInputFieldTypeSelect, ID: "default_model", Name: "Default model", Description: "Model to use by default", DefaultValue: options[0], Options: options},
		}},
	}, nil
}

func (l *CustomProviderLogin) submitDefaultModel(ctx context.Context, input map[string]string) (*bridgev2.LoginStep, error) {
	modelID := strings.TrimSpace(input["default_model"])
	if modelID == "" {
		return nil, fmt.Errorf("default_model is required")
	}
	if !providerHasModel(aiid.ProviderConfig{Models: l.config.Models}, modelID) {
		return nil, fmt.Errorf("model %s was not returned by provider %s", modelID, l.config.ProviderID)
	}
	displayName := providerDisplayName(l.config.ProviderID)
	provider := aiid.ProviderConfig{
		ID:           l.config.ProviderID,
		DisplayName:  displayName,
		API:          l.config.API,
		Provider:     ai.Provider(l.config.ProviderID),
		BaseURL:      l.config.BaseURL,
		APIKey:       l.config.APIKey,
		DefaultModel: modelID,
		Models:       l.config.Models,
	}
	login, err := l.Main.UpsertProviderLogin(ctx, l.User, provider)
	if err != nil {
		return nil, fmt.Errorf("failed to create provider login: %w", err)
	}
	return &bridgev2.LoginStep{
		Type:         bridgev2.LoginStepTypeComplete,
		StepID:       loginStepComplete,
		Instructions: "Provider added",
		CompleteParams: &bridgev2.LoginCompleteParams{
			UserLoginID: login.ID,
			UserLogin:   login,
		},
	}, nil
}

func (l *CustomProviderLogin) Cancel() {}

type providerLoginConfig struct {
	API        ai.Api
	ProviderID string
	BaseURL    string
	APIKey     string
	Models     []ai.Model
}

func supportedProviderLoginAPI(api ai.Api) bool {
	switch api {
	case ai.ApiOpenAIResponses, ai.ApiOpenAICompletions, ai.ApiOpenAICodexResponses:
		return true
	default:
		return false
	}
}

func defaultBaseURLForAPI(api ai.Api) string {
	return "https://api.openai.com/v1"
}

func providerDisplayName(providerID string) string {
	providerID = strings.TrimSpace(providerID)
	if providerID == "" {
		return "Provider"
	}
	known := map[string]string{
		"openai":     "OpenAI",
		"openrouter": "OpenRouter",
		"lmstudio":   "LM Studio",
	}
	if name, ok := known[strings.ToLower(providerID)]; ok {
		return name
	}
	parts := strings.FieldsFunc(providerID, func(r rune) bool {
		return r == '-' || r == '_' || r == '.'
	})
	for i, part := range parts {
		if part == "" {
			continue
		}
		parts[i] = strings.ToUpper(part[:1]) + part[1:]
	}
	return strings.Join(parts, " ")
}

func fetchProviderModels(ctx context.Context, api ai.Api, providerID string, baseURL string, apiKey string) ([]ai.Model, error) {
	if !supportedProviderLoginAPI(api) {
		return nil, fmt.Errorf("unsupported API type %s", api)
	}
	modelURL, err := url.JoinPath(baseURL, "models")
	if err != nil {
		return nil, fmt.Errorf("invalid base URL: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, modelURL, nil)
	if err != nil {
		return nil, err
	}
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+resolveConfiguredAPIKey(apiKey))
	}
	client := &http.Client{Timeout: 20 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch models: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("failed to fetch models: provider returned HTTP %d", resp.StatusCode)
	}
	var body aiServicesModelListResponse
	if err = json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, fmt.Errorf("failed to parse models response: %w", err)
	}
	models := make([]ai.Model, 0, len(body.Data))
	seen := map[string]bool{}
	provider, inferredAPI := inferProviderRoute(providerID, baseURL)
	if api != "" {
		inferredAPI = api
	}
	providerConfig := aiid.ProviderConfig{
		ID:       providerID,
		API:      inferredAPI,
		Provider: configuredModelProvider(providerID, provider),
		BaseURL:  baseURL,
	}
	for _, item := range body.Data {
		modelID := strings.TrimSpace(item.ID)
		if modelID == "" || seen[modelID] {
			continue
		}
		seen[modelID] = true
		model := ai.Model{
			ID:            modelID,
			Name:          item.Name,
			API:           inferredAPI,
			Provider:      providerConfig.Provider,
			BaseURL:       baseURL,
			Reasoning:     item.reasoning(),
			Input:         item.inputModalities(),
			ContextWindow: item.contextWindow(),
			MaxTokens:     item.maxTokens(),
		}
		model = item.applyProviderRoute(model, providerConfig)
		models = append(models, normalizeProviderModel(model, providerConfig))
	}
	if len(models) == 0 {
		return nil, fmt.Errorf("provider returned no models")
	}
	return models, nil
}

func (c *Connector) UpsertProviderLogin(ctx context.Context, user *bridgev2.User, provider aiid.ProviderConfig) (*bridgev2.UserLogin, error) {
	mainLogin, err := c.EnsureDefaultLogin(ctx, user)
	if err != nil {
		return nil, err
	}
	if err = c.addProviderToLogin(ctx, mainLogin, provider); err != nil {
		return nil, err
	}
	loginID := aiid.ProviderLoginID(mainLogin.ID, provider.ID)
	if cached := c.Bridge.GetCachedUserLoginByID(loginID); cached != nil {
		cached.RemoteName = provider.DisplayName
		cached.RemoteProfile.Name = provider.DisplayName
		cached.Metadata = &aiid.UserLoginMetadata{}
		if err = cached.Save(ctx); err != nil {
			return nil, err
		}
		if err = c.connectProviderLogin(ctx, cached); err != nil {
			return nil, err
		}
		return cached, nil
	}
	login, err := user.NewLogin(ctx, &database.UserLogin{
		ID:         loginID,
		RemoteName: provider.DisplayName,
		RemoteProfile: status.RemoteProfile{
			Name: provider.DisplayName,
		},
		Metadata: &aiid.UserLoginMetadata{},
	}, &bridgev2.NewLoginParams{})
	if err != nil {
		return nil, err
	}
	if err = c.connectProviderLogin(ctx, login); err != nil {
		return nil, err
	}
	return login, nil
}

func (c *Connector) addProviderToLogin(ctx context.Context, login *bridgev2.UserLogin, provider aiid.ProviderConfig) error {
	if provider.ID == "" {
		return fmt.Errorf("provider id is required")
	}
	if provider.ID == aiid.DefaultProvider {
		return fmt.Errorf("provider id %q is reserved for the Beeper AI provider", aiid.DefaultProvider)
	}
	meta, ok := login.Metadata.(*aiid.UserLoginMetadata)
	if !ok {
		return fmt.Errorf("unexpected login metadata type %T", login.Metadata)
	}
	ensureMetadata(meta)
	meta.Providers[provider.ID] = provider
	return login.Save(ctx)
}

func (c *Connector) connectProviderLogin(ctx context.Context, login *bridgev2.UserLogin) error {
	if login == nil {
		return nil
	}
	if login.Client == nil {
		if err := c.LoadUserLogin(ctx, login); err != nil {
			return err
		}
	}
	if login.Client != nil {
		login.Client.Connect(ctx)
	}
	return nil
}

func customProviderConfig(providerID string, displayName string, baseURL string, apiKey string, defaultModel string, modelList string) aiid.ProviderConfig {
	provider, api := inferProviderRoute(providerID, baseURL)
	modelIDs := providerModelIDs(modelList, defaultModel)
	return aiid.ProviderConfig{
		ID:           providerID,
		DisplayName:  displayName,
		API:          api,
		Provider:     provider,
		BaseURL:      baseURL,
		APIKey:       apiKey,
		DefaultModel: defaultModel,
		Models:       providerModelsFromIDs(modelIDs, providerID, provider, api, baseURL),
	}
}

func inferProviderRoute(providerID string, baseURL string) (ai.Provider, ai.Api) {
	providerID = strings.ToLower(providerID)
	baseURL = strings.ToLower(baseURL)
	if providerID == string(ai.ProviderOpenRouter) || strings.Contains(baseURL, "openrouter.ai") {
		return ai.ProviderOpenRouter, ai.ApiOpenAICompletions
	}
	return ai.ProviderOpenAI, ai.ApiOpenAIResponses
}

func providerModels(modelList string, defaultModel string, providerID string, baseURL string) []ai.Model {
	provider, api := inferProviderRoute(providerID, baseURL)
	return providerModelsFromIDs(providerModelIDs(modelList, defaultModel), providerID, provider, api, baseURL)
}

func providerModelIDs(modelList string, defaultModel string) []string {
	seen := map[string]bool{}
	modelIDs := []string{defaultModel}
	for _, raw := range strings.FieldsFunc(modelList, func(r rune) bool {
		return r == ',' || r == '\n' || r == '\t' || r == ' '
	}) {
		modelID := strings.TrimSpace(raw)
		if modelID != "" {
			modelIDs = append(modelIDs, modelID)
		}
	}
	out := make([]string, 0, len(modelIDs))
	for _, modelID := range modelIDs {
		if seen[modelID] {
			continue
		}
		seen[modelID] = true
		out = append(out, modelID)
	}
	return out
}

func providerModelsFromIDs(modelIDs []string, providerID string, provider ai.Provider, api ai.Api, baseURL string) []ai.Model {
	models := make([]ai.Model, 0, len(modelIDs))
	for _, modelID := range modelIDs {
		models = append(models, ai.Model{
			ID:            modelID,
			Name:          modelID,
			API:           api,
			Provider:      configuredModelProvider(providerID, provider),
			BaseURL:       baseURL,
			Input:         []string{"text", "image"},
			ContextWindow: 128000,
			MaxTokens:     32000,
		})
	}
	return models
}

func configuredModelProvider(providerID string, provider ai.Provider) ai.Provider {
	if providerID != string(ai.ProviderOpenAI) && providerID != string(ai.ProviderOpenRouter) {
		return ai.Provider(providerID)
	}
	return provider
}
