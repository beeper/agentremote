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
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/event"
)

func (cl *Client) GetContactList(ctx context.Context) ([]*bridgev2.ResolveIdentifierResponse, error) {
	return cl.modelContacts(ctx, ""), nil
}

func (cl *Client) SearchUsers(ctx context.Context, query string) ([]*bridgev2.ResolveIdentifierResponse, error) {
	return cl.modelContacts(ctx, strings.ToLower(strings.TrimSpace(query))), nil
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
	roomType := database.RoomTypeDM
	info := &bridgev2.ChatInfo{Name: &name, Type: &roomType, Members: aiChatMembers()}
	meta := portalMetadata(portal)
	meta.AutoTitlePending = true
	if portal.MXID == "" {
		if err = portal.CreateMatrixRoom(ctx, cl.UserLogin, info); err != nil {
			return nil, err
		}
	} else if err = portal.Save(ctx); err != nil {
		return nil, err
	}
	if _, err = cl.writeRoomModelState(ctx, portal, provider.ID+"/"+model.ID, ""); err != nil {
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
			},
		},
	}
}

func newAIChatPortalKey(loginID networkid.UserLoginID) networkid.PortalKey {
	return networkid.PortalKey{
		ID:       networkid.PortalID("chat:" + session.CreateSessionID()),
		Receiver: loginID,
	}
}

func (cl *Client) modelContacts(ctx context.Context, query string) []*bridgev2.ResolveIdentifierResponse {
	meta := cl.loginMetadata()
	if meta == nil {
		return nil
	}
	contacts := []*bridgev2.ResolveIdentifierResponse{}
	for _, provider := range meta.Providers {
		if !provider.Enabled {
			continue
		}
		provider = cl.providerWithCatalogModels(ctx, provider)
		contacts = append(contacts, providerModelContacts(ctx, cl.bridge(), provider, query)...)
	}
	return contacts
}

func (cl *Client) modelContact(ctx context.Context, provider aiid.ProviderConfig, model ai.Model) *bridgev2.ResolveIdentifierResponse {
	return modelContactWithGhost(ctx, cl.bridge(), provider, model)
}

func (cl *Client) providerWithCatalogModels(ctx context.Context, provider aiid.ProviderConfig) aiid.ProviderConfig {
	if models, err := cl.aiServicesCatalogModels(ctx, provider); err == nil && len(models) > 0 {
		provider.Models = models
	}
	return provider
}

func (cl *Client) bridge() *bridgev2.Bridge {
	if cl == nil || cl.Main == nil {
		return nil
	}
	return cl.Main.Bridge
}

func providerModelContacts(ctx context.Context, br *bridgev2.Bridge, provider aiid.ProviderConfig, query string) []*bridgev2.ResolveIdentifierResponse {
	contacts := []*bridgev2.ResolveIdentifierResponse{}
	for _, model := range contactModels(provider) {
		name := strings.ToLower(modelDisplayName(provider, model))
		if query != "" && !strings.Contains(name, query) && !strings.Contains(strings.ToLower(model.ID), query) && !strings.Contains(strings.ToLower(provider.ID), query) {
			continue
		}
		contacts = append(contacts, modelContactWithGhost(ctx, br, provider, model))
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
	var body struct {
		Data []struct {
			ID            string `json:"id"`
			Name          string `json:"name"`
			ContextLength int    `json:"context_length"`
			Architecture  *struct {
				InputModalities []string `json:"input_modalities"`
			} `json:"architecture"`
			TopProvider *struct {
				MaxCompletionTokens int `json:"max_completion_tokens"`
			} `json:"top_provider"`
		} `json:"data"`
	}
	if err = json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, err
	}
	models := make([]ai.Model, 0, len(body.Data))
	for _, item := range body.Data {
		modelID := strings.TrimSpace(item.ID)
		if modelID == "" {
			continue
		}
		input := []string{"text", "image"}
		if item.Architecture != nil && len(item.Architecture.InputModalities) > 0 {
			input = append([]string{}, item.Architecture.InputModalities...)
		}
		maxTokens := 0
		if item.TopProvider != nil {
			maxTokens = item.TopProvider.MaxCompletionTokens
		}
		model := ai.Model{
			ID:            modelID,
			Name:          item.Name,
			API:           provider.API,
			Provider:      provider.Provider,
			BaseURL:       provider.BaseURL,
			Input:         input,
			ContextWindow: item.ContextLength,
			MaxTokens:     maxTokens,
		}
		models = append(models, normalizeProviderModel(model, provider))
	}
	return models, nil
}

func aiServicesModelsURL(proxyBaseURL string) (string, error) {
	parsed, err := url.Parse(strings.TrimRight(normalizeResponsesBaseURL(proxyBaseURL), "/"))
	if err != nil {
		return "", err
	}
	parsed.Path = strings.TrimSuffix(parsed.Path, "/proxy/_/v1")
	parsed.Path = strings.TrimSuffix(parsed.Path, "/proxy/_")
	parsed.Path = strings.TrimRight(parsed.Path, "/") + "/models"
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return parsed.String(), nil
}

func modelContact(provider aiid.ProviderConfig, model ai.Model) *bridgev2.ResolveIdentifierResponse {
	name := modelDisplayName(provider, model)
	isBot := true
	return &bridgev2.ResolveIdentifierResponse{
		UserID: aiid.ModelContactID(provider.ID, model.ID),
		UserInfo: &bridgev2.UserInfo{
			Name:        &name,
			IsBot:       &isBot,
			Identifiers: []string{provider.ID + "/" + model.ID, model.ID},
		},
	}
}

func modelContactWithGhost(ctx context.Context, br *bridgev2.Bridge, provider aiid.ProviderConfig, model ai.Model) *bridgev2.ResolveIdentifierResponse {
	resp := modelContact(provider, model)
	if br != nil {
		if ghost, err := br.GetGhostByID(ctx, resp.UserID); err == nil {
			resp.Ghost = ghost
		}
	}
	return resp
}

func resolveModelForProvider(provider aiid.ProviderConfig, identifier string) (ai.Model, bool) {
	if providerID, modelID, ok := aiid.ParseModelContactID(aiidNetworkID(identifier)); ok {
		identifier = providerID + "/" + modelID
	}
	for _, model := range contactModels(provider) {
		if identifier == string(aiid.ModelContactID(provider.ID, model.ID)) || identifier == provider.ID+"/"+model.ID || identifier == model.ID {
			return model, true
		}
	}
	return ai.Model{}, false
}

func (cl *Client) resolveModelIdentifier(ctx context.Context, identifier string) (aiid.ProviderConfig, ai.Model, bool) {
	meta := cl.loginMetadata()
	if meta == nil {
		return aiid.ProviderConfig{}, ai.Model{}, false
	}
	if providerID, modelID, ok := aiid.ParseModelContactID(aiidNetworkID(identifier)); ok {
		identifier = providerID + "/" + modelID
	}
	for _, provider := range meta.Providers {
		if !provider.Enabled {
			continue
		}
		provider = cl.providerWithCatalogModels(ctx, provider)
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
		ensureMetadataDefaults(meta, cl.Main.defaultProviderConfig())
	}
	return meta
}

func contactModels(provider aiid.ProviderConfig) []ai.Model {
	if len(provider.Models) > 0 {
		return provider.Models
	}
	if len(provider.AllowedModels) > 0 {
		models := make([]ai.Model, 0, len(provider.AllowedModels))
		for _, modelID := range provider.AllowedModels {
			if modelID == "" {
				continue
			}
			models = append(models, normalizeProviderModel(modelForProviderCatalog(provider, modelID), provider))
		}
		return models
	}
	if models := ai.GetModels(provider.Provider); len(models) > 0 {
		out := make([]ai.Model, 0, len(models))
		for _, model := range models {
			out = append(out, normalizeProviderModel(model, provider))
		}
		return out
	}
	if provider.DefaultModel == "" {
		return nil
	}
	return []ai.Model{normalizeProviderModel(modelForProviderCatalog(provider, provider.DefaultModel), provider)}
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
