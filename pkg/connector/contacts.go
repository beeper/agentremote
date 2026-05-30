package connector

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/beeper/ai-bridge/pkg/agent/harness/session"
	ai "github.com/beeper/ai-bridge/pkg/ai"
	"github.com/beeper/ai-bridge/pkg/aiid"
	"github.com/rs/zerolog"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/event"
)

func (cl *Client) GetContactList(ctx context.Context) ([]*bridgev2.ResolveIdentifierResponse, error) {
	return cl.modelContacts(ctx, ""), nil
}

func (cl *Client) SearchUsers(ctx context.Context, query string) ([]*bridgev2.ResolveIdentifierResponse, error) {
	return cl.modelContacts(ctx, strings.TrimSpace(query)), nil
}

func (cl *Client) ResolveIdentifier(ctx context.Context, identifier string, createChat bool) (*bridgev2.ResolveIdentifierResponse, error) {
	provider, model, ok := cl.resolveModelIdentifier(ctx, identifier)
	if !ok {
		return nil, fmt.Errorf("unknown AI model %s", identifier)
	}
	resp := cl.modelContact(ctx, provider, model)
	if createChat {
		chat, err := cl.createModelChat(ctx, provider, model)
		if err != nil {
			return nil, err
		}
		resp.Chat = chat
	}
	return resp, nil
}

func (cl *Client) createModelChat(ctx context.Context, provider aiid.ProviderConfig, model ai.Model) (*bridgev2.CreateChatResponse, error) {
	portalKey := newAIChatPortalKey(cl.UserLogin.ID)
	portal, err := cl.Main.Bridge.GetPortalByKey(ctx, portalKey)
	if err != nil {
		return nil, err
	}
	name := defaultConversationTitle(provider, model)
	topic := modelRoomDescription(provider, model)
	roomType := database.RoomTypeDM
	info := &bridgev2.ChatInfo{Name: &name, Topic: &topic, Avatar: modelAvatar(provider, model), Type: &roomType, Members: aiChatMembers()}
	meta := portalMetadata(portal)
	meta.AutoTitlePending = true
	if portal.MXID == "" {
		if err = portal.CreateMatrixRoom(ctx, cl.UserLogin, info); err != nil {
			return nil, err
		}
	} else if err = portal.Save(ctx); err != nil {
		return nil, err
	}
	if _, err = cl.writeRoomModelState(ctx, portal, provider, model, provider.ID+"/"+model.ID, ""); err != nil {
		return nil, err
	}
	return &bridgev2.CreateChatResponse{
		PortalKey:      portalKey,
		Portal:         portal,
		PortalInfo:     info,
		DMRedirectedTo: aiid.AssistantUserID(),
	}, nil
}

func aiChatMembers() *bridgev2.ChatMemberList {
	return &bridgev2.ChatMemberList{
		IsFull:      true,
		OtherUserID: aiid.AssistantUserID(),
		MemberMap: bridgev2.ChatMemberMap{
			"": {
				EventSender: bridgev2.EventSender{IsFromMe: true},
				Membership:  event.MembershipJoin,
			},
			aiid.AssistantUserID(): {
				EventSender: bridgev2.EventSender{Sender: aiid.AssistantUserID()},
				Membership:  event.MembershipJoin,
				UserInfo:    aiAssistantUserInfo(),
			},
		},
	}
}

func aiAssistantUserInfo() *bridgev2.UserInfo {
	isBot := true
	name := "AI"
	return &bridgev2.UserInfo{
		Name:   &name,
		IsBot:  &isBot,
		Avatar: defaultAIAssistantAvatar(),
	}
}

func newAIChatPortalKey(loginID networkid.UserLoginID) networkid.PortalKey {
	return networkid.PortalKey{
		ID:       networkid.PortalID("chat:" + session.CreateSessionID()),
		Receiver: loginID,
	}
}

func (cl *Client) modelContacts(ctx context.Context, query string) []*bridgev2.ResolveIdentifierResponse {
	providers := cl.providers()
	if len(providers) == 0 {
		return nil
	}
	contacts := []*bridgev2.ResolveIdentifierResponse{}
	for _, provider := range providers {
		var ok bool
		provider, ok = cl.providerForModelContacts(ctx, provider)
		if !ok {
			continue
		}
		contacts = append(contacts, providerModelContacts(ctx, cl.bridge(), provider, query)...)
	}
	return contacts
}

func (cl *Client) modelContact(ctx context.Context, provider aiid.ProviderConfig, model ai.Model) *bridgev2.ResolveIdentifierResponse {
	return modelContactWithGhost(ctx, cl.bridge(), provider, model)
}

func (cl *Client) providerWithCatalogModels(ctx context.Context, provider aiid.ProviderConfig) aiid.ProviderConfig {
	if refreshed, err := cl.providerWithCatalogModelsStrict(ctx, provider); err == nil {
		return refreshed
	}
	return provider
}

func (cl *Client) providerForModelContacts(ctx context.Context, provider aiid.ProviderConfig) (aiid.ProviderConfig, bool) {
	if provider.ID != aiid.DefaultProvider {
		return provider, true
	}
	refreshed, err := cl.providerWithCatalogModelsStrict(ctx, provider)
	if err != nil {
		zerolog.Ctx(ctx).Warn().Err(err).Str("provider_id", provider.ID).Msg("Skipping provider contacts after model catalog fetch failed")
		return refreshed, false
	}
	if len(refreshed.Models) == 0 {
		zerolog.Ctx(ctx).Warn().Str("provider_id", provider.ID).Msg("Skipping provider contacts because model catalog is empty")
		return refreshed, false
	}
	return refreshed, true
}

func (cl *Client) providerWithCatalogModelsStrict(ctx context.Context, provider aiid.ProviderConfig) (aiid.ProviderConfig, error) {
	if provider.ID != aiid.DefaultProvider {
		return provider, nil
	}
	if len(provider.Models) > 0 {
		return provider, nil
	}
	models, err := cl.aiServicesCatalogModels(ctx, provider)
	if err != nil {
		return provider, err
	}
	if len(models) > 0 {
		provider.Models = models
	}
	return provider, nil
}

func (cl *Client) bridge() *bridgev2.Bridge {
	if cl == nil || cl.Main == nil {
		return nil
	}
	return cl.Main.Bridge
}

func providerModelContacts(ctx context.Context, br *bridgev2.Bridge, provider aiid.ProviderConfig, query string) []*bridgev2.ResolveIdentifierResponse {
	contacts := []*bridgev2.ResolveIdentifierResponse{}
	query = strings.TrimSpace(query)
	lowerQuery := strings.ToLower(query)
	seen := map[networkid.UserID]bool{}
	for _, model := range contactModels(provider) {
		name := strings.ToLower(modelDisplayName(provider, model))
		if lowerQuery != "" && !strings.Contains(name, lowerQuery) && !strings.Contains(strings.ToLower(model.ID), lowerQuery) && !strings.Contains(strings.ToLower(provider.ID), lowerQuery) {
			continue
		}
		contact := modelContactWithGhost(ctx, br, provider, model)
		seen[contact.UserID] = true
		contacts = append(contacts, contact)
	}
	if providerAllowsArbitraryModels(provider) {
		model, ok := arbitraryModelForProvider(provider, query)
		if !ok {
			return contacts
		}
		contact := modelContactWithGhost(ctx, br, provider, model)
		if !seen[contact.UserID] {
			contacts = append(contacts, contact)
		}
	}
	return contacts
}

func (cl *Client) aiServicesCatalogModels(ctx context.Context, provider aiid.ProviderConfig) ([]ai.Model, error) {
	if cl == nil || cl.Main == nil || provider.ID != aiid.DefaultProvider || provider.BaseURL == "" || cl.Main.AppServiceToken == "" {
		return nil, nil
	}
	modelsURL, err := aiServicesModelsURL(provider.BaseURL)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, modelsURL, nil)
	if err != nil {
		return nil, err
	}
	token, err := cl.defaultProviderBearerToken()
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	client := &http.Client{Timeout: 20 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("AI Services models returned HTTP %d", resp.StatusCode)
	}
	var body aiServicesModelListResponse
	if err = json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, err
	}
	models := make([]ai.Model, 0, len(body.Data))
	for _, item := range body.Data {
		modelID := strings.TrimSpace(item.ID)
		if modelID == "" {
			continue
		}
		model := ai.Model{
			ID:                   modelID,
			Name:                 item.Name,
			API:                  provider.API,
			Provider:             provider.Provider,
			BaseURL:              provider.BaseURL,
			Reasoning:            item.reasoning(),
			ThinkingLevelMap:     item.thinkingLevelMap(),
			DefaultThinkingLevel: item.defaultThinkingLevel(),
			Input:                item.inputModalities(),
			ContextWindow:        item.contextWindow(),
			MaxTokens:            item.maxTokens(),
		}
		model = item.applyProviderRoute(model, provider)
		models = append(models, normalizeProviderModel(model, provider))
	}
	return models, nil
}

func aiServicesModelsURL(proxyBaseURL string) (string, error) {
	parsed, err := url.Parse(strings.TrimRight(normalizeResponsesBaseURL(proxyBaseURL), "/"))
	if err != nil {
		return "", err
	}
	parsed.Path = trimAIProxyProviderPath(parsed.Path)
	parsed.Path = strings.TrimRight(parsed.Path, "/") + "/models"
	parsed.RawQuery = url.Values{"feature": {"bridge:ai"}, "route": {"responses"}}.Encode()
	parsed.Fragment = ""
	return parsed.String(), nil
}

func trimAIProxyProviderPath(path string) string {
	for _, suffix := range []string{
		"/proxy/openai/v1",
		"/proxy/openai",
		"/proxy/openrouter/v1",
		"/proxy/openrouter",
		"/proxy/anthropic/v1",
		"/proxy/anthropic",
		"/proxy/vertex/v1",
		"/proxy/vertex",
		"/proxy/_/v1",
		"/proxy/_",
	} {
		path = strings.TrimSuffix(path, suffix)
	}
	return path
}

type aiServicesModelListResponse struct {
	Type string                 `json:"type"`
	Data []aiServicesModelEntry `json:"data"`
}

type aiServicesModelEntry struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	ContextLength int    `json:"context_length"`
	Architecture  *struct {
		InputModalities []string `json:"input_modalities"`
	} `json:"architecture"`
	TopProvider *struct {
		MaxCompletionTokens int `json:"max_completion_tokens"`
	} `json:"top_provider"`
	Provider *struct {
		ID      string `json:"id"`
		ModelID string `json:"model_id"`
		API     string `json:"api"`
	} `json:"provider"`
	Capabilities *struct {
		Input struct {
			Modalities []string `json:"modalities"`
		} `json:"input"`
		Reasoning *struct {
			Supported    bool               `json:"supported"`
			Levels       []string           `json:"levels"`
			LevelMap     map[string]*string `json:"level_map"`
			DefaultLevel string             `json:"default_level"`
		} `json:"reasoning"`
		Limits *struct {
			ContextTokens int `json:"context_tokens"`
			OutputTokens  int `json:"output_tokens"`
		} `json:"limits"`
	} `json:"capabilities"`
}

func (entry aiServicesModelEntry) applyProviderRoute(model ai.Model, provider aiid.ProviderConfig) ai.Model {
	if entry.Provider == nil || entry.Provider.ID == "" {
		return model
	}
	switch entry.Provider.ID {
	case "wpcom_anthropic":
		model.API = ai.ApiAnthropicMessages
		model.Provider = ai.ProviderAnthropic
		model.BaseURL = aiServicesProxyBaseURL(provider.BaseURL, "anthropic", false)
	case "wpcom_vertex":
		model.API = ai.ApiGoogleVertex
		model.Provider = ai.ProviderGoogleVertex
		model.BaseURL = aiServicesProxyBaseURL(provider.BaseURL, "vertex", false)
	case "wpcom_openai":
		model.API = ai.ApiOpenAIResponses
		model.Provider = ai.ProviderOpenAI
		model.BaseURL = aiServicesProxyBaseURL(provider.BaseURL, "openai", true)
	case "openrouter":
		model.API = ai.ApiOpenAIResponses
		model.Provider = ai.ProviderOpenRouter
		model.BaseURL = aiServicesProxyBaseURL(provider.BaseURL, "openrouter", true)
	}
	return model
}

func aiServicesProxyBaseURL(baseURL string, providerPath string, includeV1 bool) string {
	parsed, err := url.Parse(strings.TrimRight(normalizeResponsesBaseURL(baseURL), "/"))
	if err != nil {
		return baseURL
	}
	parsed.Path = strings.TrimRight(trimAIProxyProviderPath(parsed.Path), "/") + "/proxy/" + providerPath
	if includeV1 {
		parsed.Path += "/v1"
	}
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return parsed.String()
}

func (entry aiServicesModelEntry) inputModalities() []string {
	if entry.Capabilities != nil && len(entry.Capabilities.Input.Modalities) > 0 {
		return append([]string{}, entry.Capabilities.Input.Modalities...)
	}
	if entry.Architecture != nil && len(entry.Architecture.InputModalities) > 0 {
		return append([]string{}, entry.Architecture.InputModalities...)
	}
	return []string{"text"}
}

func (entry aiServicesModelEntry) contextWindow() int {
	if entry.Capabilities != nil && entry.Capabilities.Limits != nil && entry.Capabilities.Limits.ContextTokens > 0 {
		return entry.Capabilities.Limits.ContextTokens
	}
	return entry.ContextLength
}

func (entry aiServicesModelEntry) maxTokens() int {
	if entry.Capabilities != nil && entry.Capabilities.Limits != nil && entry.Capabilities.Limits.OutputTokens > 0 {
		return entry.Capabilities.Limits.OutputTokens
	}
	if entry.TopProvider != nil {
		return entry.TopProvider.MaxCompletionTokens
	}
	return 0
}

func (entry aiServicesModelEntry) reasoning() bool {
	return entry.Capabilities != nil && entry.Capabilities.Reasoning != nil && entry.Capabilities.Reasoning.Supported
}

func (entry aiServicesModelEntry) thinkingLevelMap() map[ai.ModelThinkingLevel]*string {
	if entry.Capabilities == nil || entry.Capabilities.Reasoning == nil || len(entry.Capabilities.Reasoning.Levels) == 0 {
		return nil
	}
	if len(entry.Capabilities.Reasoning.LevelMap) > 0 {
		out := map[ai.ModelThinkingLevel]*string{}
		for rawLevel, rawMapped := range entry.Capabilities.Reasoning.LevelMap {
			level := ai.ModelThinkingLevel(strings.ToLower(strings.TrimSpace(rawLevel)))
			if !modelThinkingLevelKnown(level) {
				continue
			}
			if rawMapped == nil {
				out[level] = nil
				continue
			}
			mapped := *rawMapped
			out[level] = &mapped
		}
		if len(out) > 0 {
			return out
		}
	}
	supportedLevels := map[ai.ModelThinkingLevel]bool{}
	for _, rawLevel := range entry.Capabilities.Reasoning.Levels {
		level := ai.ModelThinkingLevel(strings.ToLower(strings.TrimSpace(rawLevel)))
		if modelThinkingLevelKnown(level) {
			supportedLevels[level] = true
		}
	}
	if len(supportedLevels) == 0 {
		return nil
	}
	out := map[ai.ModelThinkingLevel]*string{}
	for _, level := range []ai.ModelThinkingLevel{
		ai.ModelThinkingLevelOff,
		ai.ModelThinkingLevelMinimal,
		ai.ModelThinkingLevelLow,
		ai.ModelThinkingLevelMedium,
		ai.ModelThinkingLevelHigh,
		ai.ModelThinkingLevelXHigh,
	} {
		if supportedLevels[level] {
			if level == ai.ModelThinkingLevelXHigh {
				mapped := string(level)
				out[level] = &mapped
			}
			continue
		}
		out[level] = nil
	}
	return out
}

func (entry aiServicesModelEntry) defaultThinkingLevel() ai.ModelThinkingLevel {
	if entry.Capabilities == nil || entry.Capabilities.Reasoning == nil {
		return ""
	}
	level := ai.ModelThinkingLevel(strings.ToLower(strings.TrimSpace(entry.Capabilities.Reasoning.DefaultLevel)))
	if !modelThinkingLevelKnown(level) {
		return ""
	}
	return level
}

func modelThinkingLevelKnown(level ai.ModelThinkingLevel) bool {
	switch level {
	case ai.ModelThinkingLevelOff, ai.ModelThinkingLevelMinimal, ai.ModelThinkingLevelLow, ai.ModelThinkingLevelMedium, ai.ModelThinkingLevelHigh, ai.ModelThinkingLevelXHigh:
		return true
	default:
		return false
	}
}

func modelContact(provider aiid.ProviderConfig, model ai.Model) *bridgev2.ResolveIdentifierResponse {
	info := modelUserInfo(provider, model)
	return &bridgev2.ResolveIdentifierResponse{
		UserID:   aiid.ModelContactID(provider.ID, model.ID),
		UserInfo: info,
	}
}

func modelUserInfo(provider aiid.ProviderConfig, model ai.Model) *bridgev2.UserInfo {
	name := modelDisplayName(provider, model)
	isBot := true
	return &bridgev2.UserInfo{
		Name:        &name,
		IsBot:       &isBot,
		Identifiers: []string{provider.ID + "/" + model.ID, model.ID},
		Avatar:      modelAvatar(provider, model),
	}
}

func modelContactWithGhost(ctx context.Context, br *bridgev2.Bridge, provider aiid.ProviderConfig, model ai.Model) *bridgev2.ResolveIdentifierResponse {
	resp := modelContact(provider, model)
	if ghost, err := updateModelGhostInfo(ctx, br, provider, model); err == nil {
		resp.Ghost = ghost
	}
	return resp
}

func updateModelGhostInfo(ctx context.Context, br *bridgev2.Bridge, provider aiid.ProviderConfig, model ai.Model) (*bridgev2.Ghost, error) {
	if br == nil {
		return nil, fmt.Errorf("missing bridge")
	}
	ghost, err := br.GetGhostByID(ctx, aiid.ModelContactID(provider.ID, model.ID))
	if err != nil {
		return nil, err
	}
	ghost.UpdateInfo(ctx, modelUserInfo(provider, model))
	return ghost, nil
}

func resolveModelForProvider(provider aiid.ProviderConfig, identifier string) (ai.Model, bool) {
	if providerID, modelID, ok := aiid.ParseModelContactID(aiidNetworkID(identifier)); ok {
		if providerID != provider.ID {
			return ai.Model{}, false
		}
		identifier = providerID + "/" + modelID
	}
	for _, model := range contactModels(provider) {
		if identifier == string(aiid.ModelContactID(provider.ID, model.ID)) || identifier == provider.ID+"/"+model.ID || identifier == model.ID {
			return model, true
		}
	}
	if modelID, ok := strings.CutPrefix(identifier, provider.ID+"/"); ok {
		if model, ok := arbitraryModelForProvider(provider, modelID); ok && providerAllowsArbitraryModels(provider) {
			return model, true
		}
	}
	if !strings.Contains(identifier, "/") && providerAllowsArbitraryModels(provider) {
		if model, ok := arbitraryModelForProvider(provider, identifier); ok {
			return model, true
		}
	}
	return ai.Model{}, false
}

func (cl *Client) resolveModelIdentifier(ctx context.Context, identifier string) (aiid.ProviderConfig, ai.Model, bool) {
	providers := cl.providers()
	if len(providers) == 0 {
		return aiid.ProviderConfig{}, ai.Model{}, false
	}
	if providerID, modelID, ok := aiid.ParseModelContactID(aiidNetworkID(identifier)); ok {
		identifier = providerID + "/" + modelID
	}
	for _, provider := range providers {
		var providerOK bool
		provider, providerOK = cl.providerForModelContacts(ctx, provider)
		if !providerOK {
			continue
		}
		if model, ok := resolveModelForProvider(provider, identifier); ok {
			return provider, model, true
		}
	}
	return aiid.ProviderConfig{}, ai.Model{}, false
}

func aiidNetworkID(identifier string) networkid.UserID {
	return networkid.UserID(identifier)
}

func (cl *Client) loginMetadata() *aiid.UserLoginMetadata {
	if cl == nil || cl.UserLogin == nil {
		return nil
	}
	meta, ok := cl.UserLogin.Metadata.(*aiid.UserLoginMetadata)
	if !ok {
		return nil
	}
	if cl.Main != nil {
		ensureMetadata(meta)
	}
	return meta
}

func (cl *Client) providers() map[string]aiid.ProviderConfig {
	if cl == nil || cl.Main == nil || cl.UserLogin == nil {
		return nil
	}
	return cl.Main.providersForLogin(cl.UserLogin)
}

func contactModels(provider aiid.ProviderConfig) []ai.Model {
	if len(provider.Models) > 0 {
		return provider.Models
	}
	if provider.DefaultModel == "" {
		return nil
	}
	return []ai.Model{normalizeProviderModel(modelForProviderConfig(provider, provider.DefaultModel), provider)}
}

func arbitraryModelForProvider(provider aiid.ProviderConfig, query string) (ai.Model, bool) {
	modelID := strings.TrimSpace(query)
	if modelID == "" {
		return ai.Model{}, false
	}
	if stripped, ok := strings.CutPrefix(modelID, provider.ID+"/"); ok {
		modelID = strings.TrimSpace(stripped)
	}
	if modelID == "" {
		return ai.Model{}, false
	}
	model := normalizeProviderModel(modelForProviderConfig(provider, modelID), provider)
	displayName := provider.DisplayName
	if displayName == "" {
		displayName = providerDisplayName(provider.ID)
	}
	model.Name = displayName + ": " + modelID
	return model, true
}

func providerAllowsArbitraryModels(provider aiid.ProviderConfig) bool {
	return provider.ID != aiid.DefaultProvider
}

func modelDisplayName(provider aiid.ProviderConfig, model ai.Model) string {
	if model.Name != "" && model.Name != model.ID {
		return model.Name
	}
	if provider.DisplayName != "" {
		return provider.DisplayName + " " + model.ID
	}
	return model.ID
}

func defaultConversationTitle(provider aiid.ProviderConfig, model ai.Model) string {
	return "New AI Chat with " + modelDisplayName(provider, model)
}
