package connector

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/beeper/ai-bridge/pkg/agent/harness"
	ai "github.com/beeper/ai-bridge/pkg/ai"
	"github.com/beeper/ai-bridge/pkg/aiid"
)

const aiServicesAppserviceTokenPrefix = "as::"

type aiServicesAppserviceToken struct {
	ASToken  string `json:"as_token"`
	Username string `json:"username"`
}

func (c *Connector) ModelForProvider(provider aiid.ProviderConfig, modelID string) ai.Model {
	for _, model := range provider.Models {
		if model.ID == modelID {
			return normalizeProviderModel(model, provider)
		}
	}
	return normalizeProviderModel(modelForProviderCatalog(provider, modelID), provider)
}

func modelForProviderCatalog(provider aiid.ProviderConfig, modelID string) ai.Model {
	if model, ok := ai.GetModel(provider.Provider, modelID); ok {
		return model
	}
	if strings.HasPrefix(modelID, "openai/") {
		if model, ok := ai.GetModel(ai.ProviderOpenAI, strings.TrimPrefix(modelID, "openai/")); ok {
			model.ID = modelID
			return model
		}
	}
	return ai.Model{
		ID:            modelID,
		Name:          modelID,
		API:           provider.API,
		Provider:      provider.Provider,
		BaseURL:       provider.BaseURL,
		Input:         []string{"text", "image"},
		ContextWindow: 128000,
		MaxTokens:     32000,
	}
}

func normalizeProviderModel(model ai.Model, provider aiid.ProviderConfig) ai.Model {
	keepCatalogRoute := provider.ID == aiid.DefaultProvider && model.Provider != "" && model.Provider != provider.Provider
	if provider.API != "" && !keepCatalogRoute {
		model.API = provider.API
	} else if model.API == "" {
		model.API = provider.API
	}
	if model.Provider == "" {
		model.Provider = provider.Provider
	}
	if provider.BaseURL != "" && !keepCatalogRoute {
		model.BaseURL = provider.BaseURL
	} else if model.BaseURL == "" {
		model.BaseURL = provider.BaseURL
	}
	if model.Name == "" {
		model.Name = model.ID
	}
	if len(model.Input) == 0 {
		model.Input = []string{"text"}
	}
	if override, ok := provider.ModelOverrides[model.ID]; ok {
		model = applyModelOverride(model, override)
	}
	model.BaseURL = normalizeResponsesBaseURL(model.BaseURL)
	return model
}

func applyModelOverride(model ai.Model, override aiid.ModelOverride) ai.Model {
	if override.Name != "" {
		model.Name = override.Name
	}
	if override.API != "" {
		model.API = override.API
	}
	if override.BaseURL != "" {
		model.BaseURL = override.BaseURL
	}
	if override.Reasoning != nil {
		model.Reasoning = *override.Reasoning
	}
	if len(override.Input) > 0 {
		model.Input = override.Input
	}
	if override.ContextWindow > 0 {
		model.ContextWindow = override.ContextWindow
	}
	if override.MaxTokens > 0 {
		model.MaxTokens = override.MaxTokens
	}
	if len(override.Headers) > 0 {
		model.Headers = mergeStringMaps(model.Headers, override.Headers)
	}
	if len(override.Compat) > 0 {
		model.Compat = mergeAnyMaps(model.Compat, override.Compat)
	}
	return model
}

func mergeStringMaps(base map[string]string, override map[string]string) map[string]string {
	out := map[string]string{}
	for key, value := range base {
		out[key] = value
	}
	for key, value := range override {
		out[key] = value
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func mergeAnyMaps(base map[string]any, override map[string]any) map[string]any {
	out := map[string]any{}
	for key, value := range base {
		out[key] = value
	}
	for key, value := range override {
		out[key] = value
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func (cl *Client) authForProvider(provider aiid.ProviderConfig) func(context.Context, ai.Model) (*harness.AgentHarnessAuth, error) {
	return func(ctx context.Context, model ai.Model) (*harness.AgentHarnessAuth, error) {
		var err error
		provider, err = cl.refreshProviderIfNeeded(ctx, provider)
		if err != nil {
			return nil, err
		}
		apiKey := resolveConfiguredAPIKey(provider.APIKey)
		if provider.ID == aiid.DefaultProvider {
			apiKey, err = cl.defaultProviderBearerToken()
			if err != nil {
				return nil, err
			}
		}
		if apiKey == "" {
			return nil, fmt.Errorf("missing API key for provider %s", provider.ID)
		}
		return &harness.AgentHarnessAuth{
			APIKey:  apiKey,
			Headers: provider.Headers,
		}, nil
	}
}

func (cl *Client) defaultProviderBearerToken() (string, error) {
	if cl == nil || cl.Main == nil {
		return "", fmt.Errorf("missing connector for default provider")
	}
	if cl.Main.AppServiceToken == "" {
		return "", fmt.Errorf("missing appservice token for default provider")
	}
	username := usernameFromHomeserverAddress(cl.Main.HomeserverAddress)
	if username == "" {
		return "", fmt.Errorf("missing hungryserv username in homeserver address for default provider")
	}
	payload, err := json.Marshal(aiServicesAppserviceToken{
		ASToken:  cl.Main.AppServiceToken,
		Username: username,
	})
	if err != nil {
		return "", err
	}
	return aiServicesAppserviceTokenPrefix + base64.RawURLEncoding.EncodeToString(payload), nil
}

func usernameFromHomeserverAddress(address string) string {
	_, username, ok := strings.Cut(strings.TrimSpace(address), "/_hungryserv/")
	if !ok {
		return ""
	}
	username, _, _ = strings.Cut(username, "/")
	return username
}

func (cl *Client) refreshProviderIfNeeded(ctx context.Context, provider aiid.ProviderConfig) (aiid.ProviderConfig, error) {
	if provider.API != ai.ApiOpenAICodexResponses || provider.RefreshToken == "" || provider.ExpiresAtMS == 0 {
		return provider, nil
	}
	if time.Now().Add(2 * time.Minute).Before(time.UnixMilli(provider.ExpiresAtMS)) {
		return provider, nil
	}
	credentials, err := refreshChatGPTCredentials(ctx, provider.RefreshToken)
	if err != nil {
		return provider, err
	}
	provider.APIKey = credentials.AccessToken
	provider.RefreshToken = credentials.RefreshToken
	provider.ExpiresAtMS = credentials.ExpiresAtMS
	cl.saveProviderConfig(ctx, provider)
	return provider, nil
}

func (cl *Client) saveProviderConfig(ctx context.Context, provider aiid.ProviderConfig) {
	meta := cl.loginMetadata()
	if meta == nil || meta.Providers == nil || provider.ID == "" {
		return
	}
	if _, ok := meta.Providers[provider.ID]; !ok {
		return
	}
	meta.Providers[provider.ID] = provider
	if cl.UserLogin != nil {
		_ = cl.UserLogin.Save(ctx)
	}
}

func resolveConfiguredAPIKey(apiKey string) string {
	if envName, ok := strings.CutPrefix(apiKey, "env:"); ok {
		return os.Getenv(strings.TrimSpace(envName))
	}
	return apiKey
}

func isImageModel(model ai.Model) bool {
	return modelHasInput(model, "image")
}

func isAudioModel(model ai.Model) bool {
	return modelHasInput(model, "audio")
}

func modelHasInput(model ai.Model, inputType string) bool {
	for _, input := range model.Input {
		if input == inputType {
			return true
		}
	}
	return false
}
