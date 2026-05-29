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
	client := &Client{UserLogin: &bridgev2.UserLogin{UserLogin: &database.UserLogin{
		ID: "login",
		Metadata: &aiid.UserLoginMetadata{Providers: map[string]aiid.ProviderConfig{
			"local": {
				ID:          "local",
				DisplayName: "Local",
				Provider:    "local",
				API:         ai.ApiOpenAIResponses,
				Models:      []ai.Model{{ID: "model-a"}, {ID: "model-b", Name: "Model Bee"}},
				Enabled:     true,
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
		gotAuth = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`{"data":[{"id":"gpt-5.5","name":"GPT-5.5","context_length":1050000,"architecture":{"input_modalities":["text","image"]},"top_provider":{"max_completion_tokens":128000}},{"id":"beeper/fast","name":"Beeper Fast"}]}`))
	}))
	defer server.Close()

	client := &Client{
		Main: &Connector{
			AppServiceToken:   "as-token",
			HomeserverAddress: "https://matrix.beeper-staging.com/_hungryserv/test",
		},
		UserLogin: &bridgev2.UserLogin{UserLogin: &database.UserLogin{
			UserMXID: "@alice:beeper-staging.com",
		}},
	}
	models, err := client.aiServicesCatalogModels(context.Background(), aiid.ProviderConfig{
		ID:       aiid.DefaultProvider,
		Provider: ai.ProviderOpenAI,
		API:      ai.ApiOpenAIResponses,
		BaseURL:  server.URL + "/proxy/_/v1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(gotAuth, "Bearer "+aiServicesAppserviceTokenPrefix) {
		t.Fatalf("unexpected auth header %q", gotAuth)
	}
	if len(models) != 2 || models[0].ID != "gpt-5.5" || models[0].BaseURL != server.URL+"/proxy/_/v1" {
		t.Fatalf("unexpected models %#v", models)
	}
	if models[0].ContextWindow != 1050000 || models[0].MaxTokens != 128000 {
		t.Fatalf("expected AI Services metadata, got %#v", models[0])
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
	}
}

func TestSearchUsersFiltersModelContacts(t *testing.T) {
	client := &Client{UserLogin: &bridgev2.UserLogin{UserLogin: &database.UserLogin{
		ID: "login",
		Metadata: &aiid.UserLoginMetadata{Providers: map[string]aiid.ProviderConfig{
			"local": {
				ID:      "local",
				Models:  []ai.Model{{ID: "small"}, {ID: "large"}},
				Enabled: true,
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

func TestContactListIncludesEnabledLoginProviders(t *testing.T) {
	conn := &Connector{HomeserverAddress: "https://matrix.beeper-staging.com/_hungryserv/test"}
	client := &Client{
		Main: conn,
		UserLogin: &bridgev2.UserLogin{UserLogin: &database.UserLogin{
			ID: "login",
			Metadata: &aiid.UserLoginMetadata{
				SyntheticDefault: true,
				Providers: map[string]aiid.ProviderConfig{
					"openrouter": {
						ID:            "openrouter",
						DisplayName:   "OpenRouter",
						Provider:      ai.ProviderOpenRouter,
						API:           ai.ApiOpenAICompletions,
						BaseURL:       "https://openrouter.ai/api/v1",
						DefaultModel:  "anthropic/claude-sonnet-4.5",
						AllowedModels: []string{"anthropic/claude-sonnet-4.5"},
						Enabled:       true,
					},
					"custom": {
						ID:            "custom",
						DisplayName:   "Custom",
						Provider:      "custom",
						API:           ai.ApiOpenAIResponses,
						BaseURL:       "https://custom.test/v1",
						DefaultModel:  "custom-model",
						AllowedModels: []string{"custom-model"},
						Enabled:       true,
					},
					"disabled": {
						ID:            "disabled",
						DisplayName:   "Disabled",
						Provider:      ai.ProviderOpenAI,
						API:           ai.ApiOpenAIResponses,
						DefaultModel:  "gpt-5",
						AllowedModels: []string{"gpt-5"},
						Enabled:       false,
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
		"beeper/gpt-5.5",
		"openrouter/anthropic/claude-sonnet-4.5",
		"custom/custom-model",
	} {
		if !got[want] {
			t.Fatalf("expected contact %s in %#v", want, got)
		}
	}
	if got["disabled/gpt-5"] {
		t.Fatalf("disabled provider leaked into contact list: %#v", got)
	}
}
