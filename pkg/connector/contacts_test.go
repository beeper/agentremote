package connector

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	ai "github.com/beeper/ai-bridge/pkg/ai"
	"github.com/beeper/ai-bridge/pkg/aiid"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"
)

func TestModelContactsExposeConfiguredModels(t *testing.T) {
	client := &Client{Main: &Connector{}, UserLogin: &bridgev2.UserLogin{UserLogin: &database.UserLogin{
		ID: "login",
		Metadata: &aiid.UserLoginMetadata{Providers: map[string]aiid.ProviderConfig{
			"local": {
				ID:          "local",
				DisplayName: "Local",
				Provider:    "local",
				API:         ai.ApiOpenAIResponses,
				Models:      []ai.Model{{ID: "model-a"}, {ID: "model-b", Name: "Model Bee"}},
			},
		}},
	}}}
	contacts, err := client.GetContactList(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(contacts) != 2 {
		t.Fatalf("expected two contacts, got %#v", contacts)
	}
	if contacts[1].UserInfo == nil || contacts[1].UserInfo.Name == nil || *contacts[1].UserInfo.Name != "Model Bee" {
		t.Fatalf("unexpected contact info %#v", contacts[1])
	}
	providerID, modelID, ok := aiid.ParseModelContactID(contacts[0].UserID)
	if !ok || providerID != "local" || modelID != "model-a" {
		t.Fatalf("unexpected model contact ID %q parsed as %q %q", contacts[0].UserID, providerID, modelID)
	}
}

func TestAIServicesCatalogModelsFetchesVisibleModels(t *testing.T) {
	var gotAuth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		if r.URL.Query().Get("feature") != "bridge:ai" || r.URL.Query().Get("route") != "responses" {
			t.Fatalf("unexpected query %s", r.URL.RawQuery)
		}
		gotAuth = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`{"type":"com.beeper.ai.model_list","data":[{"id":"openai/gpt-5.5","name":"GPT-5.5","capabilities":{"input":{"modalities":["text","image"]},"output":{"modalities":["text"]},"reasoning":{"supported":true,"levels":["off","minimal","low","medium","high","xhigh"],"level_map":{"xhigh":"xhigh"},"default_level":"off"},"limits":{"context_tokens":1050000,"output_tokens":128000}}},{"id":"minimax/minimax-m2.7","name":"MiniMax M2.7","provider":{"id":"openrouter","model_id":"minimax/minimax-m2.7","api":"openai-responses"},"capabilities":{"input":{"modalities":["text"]},"output":{"modalities":["text"]},"reasoning":{"supported":true,"levels":["low","medium","high"],"level_map":{"off":null,"minimal":null},"default_level":"low"}}},{"id":"beeper/fast","name":"Beeper Fast","capabilities":{"input":{"modalities":["text"]},"output":{"modalities":["text"]}}}]}`))
	}))
	defer server.Close()

	client := &Client{
		Main: &Connector{
			AppServiceToken: "as-token",
		},
		UserLogin: &bridgev2.UserLogin{UserLogin: &database.UserLogin{
			UserMXID: "@test:beeper-staging.com",
		}},
	}
	models, err := client.aiServicesCatalogModels(context.Background(), aiid.ProviderConfig{
		ID:       aiid.DefaultProvider,
		Provider: ai.ProviderOpenAI,
		API:      ai.ApiOpenAIResponses,
		BaseURL:  server.URL + "/proxy/openai/v1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(gotAuth, "Bearer "+aiServicesAppserviceTokenPrefix) {
		t.Fatalf("unexpected auth header %q", gotAuth)
	}
	if len(models) != 3 || models[0].ID != "openai/gpt-5.5" || models[0].BaseURL != server.URL+"/proxy/openai/v1" {
		t.Fatalf("unexpected models %#v", models)
	}
	if models[0].ContextWindow != 1050000 || models[0].MaxTokens != 128000 {
		t.Fatalf("expected AI Services metadata, got %#v", models[0])
	}
	if !models[0].Reasoning {
		t.Fatalf("expected AI Services reasoning metadata, got %#v", models[0])
	}
	if models[0].DefaultThinkingLevel != ai.ModelThinkingLevelOff || !roomThinkingLevelSupported(models[0], ai.ModelThinkingLevelOff) {
		t.Fatalf("expected AI Services reasoning defaults, got %#v", models[0])
	}
	if got := models[0].ThinkingLevelMap[ai.ModelThinkingLevelXHigh]; got == nil || *got != "xhigh" {
		t.Fatalf("expected AI Services xhigh map, got %#v", models[0].ThinkingLevelMap)
	}
	if models[1].DefaultThinkingLevel != ai.ModelThinkingLevelLow || roomThinkingLevelSupported(models[1], ai.ModelThinkingLevelOff) {
		t.Fatalf("expected MiniMax reasoning to default to low and reject off, got %#v", models[1])
	}
	if roomThinkingLevelSupported(models[1], ai.ModelThinkingLevelMinimal) {
		t.Fatalf("expected MiniMax reasoning to reject minimal, got %#v", models[1])
	}
}

func TestAIServicesModelsURLStripsProviderProxyPaths(t *testing.T) {
	tests := map[string]string{
		"https://ai-services.beeper.com/proxy/openai/v1":          "https://ai-services.beeper.com/models?feature=bridge%3Aai&route=responses",
		"https://ai-services.beeper.com/proxy/openrouter/v1":      "https://ai-services.beeper.com/models?feature=bridge%3Aai&route=responses",
		"https://ai-services.beeper.com/proxy/anthropic":          "https://ai-services.beeper.com/models?feature=bridge%3Aai&route=responses",
		"https://ai-services.beeper.com/proxy/vertex":             "https://ai-services.beeper.com/models?feature=bridge%3Aai&route=responses",
		"https://ai-services.beeper.com/proxy/_/v1/responses":     "https://ai-services.beeper.com/models?feature=bridge%3Aai&route=responses",
		"https://ai-services.beeper.com/dev/proxy/openai/v1":      "https://ai-services.beeper.com/dev/models?feature=bridge%3Aai&route=responses",
		"https://ai-services.beeper.com/dev/proxy/openrouter/v1/": "https://ai-services.beeper.com/dev/models?feature=bridge%3Aai&route=responses",
	}
	for input, want := range tests {
		got, err := aiServicesModelsURL(input)
		if err != nil {
			t.Fatalf("aiServicesModelsURL(%q) returned error: %v", input, err)
		}
		if got != want {
			t.Fatalf("aiServicesModelsURL(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestAIServicesCatalogModelsUsesPublishedProviderRoutes(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":[
			{"id":"claude-sonnet-4-5","name":"Claude Sonnet 4.5","provider":{"id":"wpcom_anthropic","model_id":"claude-sonnet-4-5","api":"openai-responses"}},
			{"id":"gemini-2.5-flash-lite","name":"Gemini 2.5 Flash Lite","provider":{"id":"wpcom_vertex","model_id":"gemini-2.5-flash-lite","api":"openai-responses"}},
			{"id":"anthropic/claude-sonnet-4.5","name":"Claude via OpenRouter","provider":{"id":"openrouter","model_id":"anthropic/claude-sonnet-4.5","api":"openai-responses"}}
		]}`))
	}))
	defer server.Close()

	client := &Client{
		Main: &Connector{
			AppServiceToken: "as-token",
		},
		UserLogin: &bridgev2.UserLogin{UserLogin: &database.UserLogin{UserMXID: "@test:beeper-staging.com"}},
	}
	models, err := client.aiServicesCatalogModels(context.Background(), aiid.ProviderConfig{
		ID:       aiid.DefaultProvider,
		Provider: ai.ProviderOpenAI,
		API:      ai.ApiOpenAIResponses,
		BaseURL:  server.URL + "/proxy/openai/v1",
	})
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]ai.Model{}
	for _, model := range models {
		byID[model.ID] = model
	}
	if got := byID["claude-sonnet-4-5"]; got.API != ai.ApiAnthropicMessages || got.Provider != ai.ProviderAnthropic || got.BaseURL != server.URL+"/proxy/anthropic" {
		t.Fatalf("unexpected Anthropic route %#v", got)
	}
	if got := byID["gemini-2.5-flash-lite"]; got.API != ai.ApiGoogleVertex || got.Provider != ai.ProviderGoogleVertex || got.BaseURL != server.URL+"/proxy/vertex" {
		t.Fatalf("unexpected Vertex route %#v", got)
	}
	if got := byID["anthropic/claude-sonnet-4.5"]; got.API != ai.ApiOpenAIResponses || got.Provider != ai.ProviderOpenRouter || got.BaseURL != server.URL+"/proxy/openrouter/v1" {
		t.Fatalf("unexpected OpenRouter route %#v", got)
	}
}

func TestResolveModelForProviderPreservesOpenAICatalogModelID(t *testing.T) {
	provider := aiid.ProviderConfig{
		ID:       aiid.DefaultProvider,
		Provider: ai.ProviderOpenAI,
		API:      ai.ApiOpenAIResponses,
		Models:   []ai.Model{{ID: "openai/gpt-5.5", Provider: ai.ProviderOpenAI, API: ai.ApiOpenAIResponses}},
	}
	model, ok := resolveModelForProvider(provider, "beeper/openai/gpt-5.5")
	if !ok || model.ID != "openai/gpt-5.5" {
		t.Fatalf("expected OpenAI catalog model to resolve, got ok=%v model=%#v", ok, model)
	}
	model, ok = resolveModelForProvider(provider, string(aiid.ModelContactID(aiid.DefaultProvider, "openai/gpt-5.5")))
	if !ok || model.ID != "openai/gpt-5.5" {
		t.Fatalf("expected OpenAI model contact to resolve, got ok=%v model=%#v", ok, model)
	}
}

func TestResolveModelForProviderAcceptsArbitraryCustomModelID(t *testing.T) {
	provider := aiid.ProviderConfig{
		ID:       "custom-openai",
		Provider: ai.ProviderOpenAI,
		API:      ai.ApiOpenAIResponses,
		Models:   []ai.Model{{ID: "gpt-5.5", Provider: ai.ProviderOpenAI, API: ai.ApiOpenAIResponses}},
	}
	model, ok := resolveModelForProvider(provider, "custom-openai/openai/gpt-5.5")
	if !ok || model.ID != "openai/gpt-5.5" {
		t.Fatalf("expected arbitrary model ID to resolve, got ok=%v model=%#v", ok, model)
	}
	model, ok = resolveModelForProvider(provider, "whateveristyped")
	if !ok || model.ID != "whateveristyped" {
		t.Fatalf("expected bare arbitrary model ID to resolve, got ok=%v model=%#v", ok, model)
	}
}

func TestResolveModelForProviderRejectsArbitraryDefaultProviderModelID(t *testing.T) {
	provider := aiid.ProviderConfig{
		ID:       aiid.DefaultProvider,
		Provider: ai.ProviderOpenRouter,
		API:      ai.ApiOpenAICompletions,
		Models:   []ai.Model{{ID: "gpt-5", Provider: ai.ProviderOpenRouter, API: ai.ApiOpenAICompletions}},
	}
	if model, ok := resolveModelForProvider(provider, "beeper/openai/gpt-5"); ok {
		t.Fatalf("expected arbitrary default provider model ID to be rejected, got %#v", model)
	}
}

func TestNewAIChatPortalKeyCreatesFreshChatPortal(t *testing.T) {
	first := newAIChatPortalKey("login")
	second := newAIChatPortalKey("login")
	if first.Receiver != "login" || second.Receiver != "login" {
		t.Fatalf("unexpected receiver: %#v %#v", first, second)
	}
	if first.ID == second.ID {
		t.Fatalf("expected fresh portal IDs, got %q twice", first.ID)
	}
	if !strings.HasPrefix(string(first.ID), "chat:") || !strings.HasPrefix(string(second.ID), "chat:") {
		t.Fatalf("expected chat portal IDs, got %#v %#v", first, second)
	}
}

func TestAIChatMembersUseGlobalAssistantGhost(t *testing.T) {
	members := aiChatMembers()
	if members == nil || !members.IsFull || members.OtherUserID != aiid.AssistantUserID() {
		t.Fatalf("unexpected AI chat members %#v", members)
	}
	if member, ok := members.MemberMap[networkid.UserID("")]; !ok || !member.IsFromMe {
		t.Fatalf("expected Matrix user member, got %#v", members.MemberMap)
	}
	if member, ok := members.MemberMap[aiid.AssistantUserID()]; !ok || member.Sender != aiid.AssistantUserID() {
		t.Fatalf("expected assistant ghost member, got %#v", members.MemberMap)
	} else if member.UserInfo == nil || member.UserInfo.Avatar == nil || string(member.UserInfo.Avatar.MXC) != defaultAIAssistantAvatarMXC {
		t.Fatalf("expected assistant ghost avatar %q, got %#v", defaultAIAssistantAvatarMXC, member.UserInfo)
	}
}

func TestModelRoomDescriptionUsesDisplayName(t *testing.T) {
	provider := aiid.ProviderConfig{ID: "local", DisplayName: "Local"}
	model := ai.Model{ID: "small", Name: "Small Chat"}
	if got := modelRoomDescription(provider, model); got != "AI Chat with Small Chat" {
		t.Fatalf("unexpected model room description %q", got)
	}
}

func TestSearchUsersFiltersModelContacts(t *testing.T) {
	client := &Client{Main: &Connector{}, UserLogin: &bridgev2.UserLogin{UserLogin: &database.UserLogin{
		ID: "login",
		Metadata: &aiid.UserLoginMetadata{Providers: map[string]aiid.ProviderConfig{
			"local": {
				ID:     "local",
				Models: []ai.Model{{ID: "small"}, {ID: "large"}},
			},
		}},
	}}}
	results, err := client.SearchUsers(context.Background(), "large")
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 {
		t.Fatalf("expected one result, got %#v", results)
	}
}

func TestSearchUsersAddsArbitraryModelContact(t *testing.T) {
	client := &Client{Main: &Connector{}, UserLogin: &bridgev2.UserLogin{UserLogin: &database.UserLogin{
		ID: "login",
		Metadata: &aiid.UserLoginMetadata{Providers: map[string]aiid.ProviderConfig{
			"local": {
				ID:          "local",
				DisplayName: "Local",
				Provider:    "local",
				API:         ai.ApiOpenAIResponses,
				Models:      []ai.Model{{ID: "listed"}},
			},
		}},
	}}}
	results, err := client.SearchUsers(context.Background(), "whateveristyped")
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 {
		t.Fatalf("expected arbitrary model contact, got %#v", results)
	}
	name := results[0].UserInfo.Name
	if name == nil || *name != "Local: whateveristyped" {
		t.Fatalf("unexpected arbitrary model contact name %#v", results[0].UserInfo)
	}
}

func TestContactListIncludesLoginProviders(t *testing.T) {
	conn := &Connector{}
	client := &Client{
		Main: conn,
		UserLogin: &bridgev2.UserLogin{UserLogin: &database.UserLogin{
			ID: "login",
			Metadata: &aiid.UserLoginMetadata{
				Providers: map[string]aiid.ProviderConfig{
					"openrouter": {
						ID:           "openrouter",
						DisplayName:  "OpenRouter",
						Provider:     ai.ProviderOpenRouter,
						API:          ai.ApiOpenAICompletions,
						BaseURL:      "https://openrouter.ai/api/v1",
						DefaultModel: "anthropic/claude-sonnet-4.5",
						Models:       []ai.Model{{ID: "anthropic/claude-sonnet-4.5"}},
					},
					"custom": {
						ID:           "custom",
						DisplayName:  "Custom",
						Provider:     "custom",
						API:          ai.ApiOpenAIResponses,
						BaseURL:      "https://custom.test/v1",
						DefaultModel: "custom-model",
						Models:       []ai.Model{{ID: "custom-model"}},
					},
				},
			},
		}},
	}

	contacts, err := client.GetContactList(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, contact := range contacts {
		for _, identifier := range contact.UserInfo.Identifiers {
			got[identifier] = true
		}
	}
	for _, want := range []string{
		"openrouter/anthropic/claude-sonnet-4.5",
		"custom/custom-model",
	} {
		if !got[want] {
			t.Fatalf("expected contact %s in %#v", want, got)
		}
	}
	if got["disabled/gpt-5"] {
		t.Fatalf("unexpected provider leaked into contact list: %#v", got)
	}
}
