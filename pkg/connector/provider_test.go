package connector

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	ai "github.com/beeper/ai-bridge/pkg/ai"
	"github.com/beeper/ai-bridge/pkg/aiid"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/event"
)

func TestModelForProviderConstructsCustomModel(t *testing.T) {
	conn := &Connector{}
	provider := aiid.ProviderConfig{
		ID:       "local",
		API:      ai.ApiOpenAIResponses,
		Provider: ai.Provider("local"),
		BaseURL:  "https://local.test/v1/responses",
	}
	model := conn.ModelForProvider(provider, "local-model")
	if model.ID != "local-model" || model.Provider != "local" || model.API != ai.ApiOpenAIResponses {
		t.Fatalf("unexpected model %#v", model)
	}
	if model.BaseURL != "https://local.test/v1" {
		t.Fatalf("unexpected model base URL %q", model.BaseURL)
	}
	if !isImageModel(model) {
		t.Fatalf("expected custom provider fallback model to support image input")
	}
}

func TestDefaultProviderAuthUsesAppServiceToken(t *testing.T) {
	client := &Client{Main: &Connector{AppServiceToken: "as-token"}}
	auth, err := client.authForProvider(aiid.ProviderConfig{ID: aiid.DefaultProvider})(context.Background(), ai.Model{})
	if err != nil {
		t.Fatal(err)
	}
	if auth.APIKey != "as-token" {
		t.Fatalf("expected appservice token, got %q", auth.APIKey)
	}
}

func TestCustomProviderAuthUsesProviderKeyAndHeaders(t *testing.T) {
	client := &Client{Main: &Connector{}}
	auth, err := client.authForProvider(aiid.ProviderConfig{
		ID:      "custom",
		APIKey:  "custom-key",
		Headers: map[string]string{"X-Test": "ok"},
	})(context.Background(), ai.Model{})
	if err != nil {
		t.Fatal(err)
	}
	if auth.APIKey != "custom-key" || auth.Headers["X-Test"] != "ok" {
		t.Fatalf("unexpected auth %#v", auth)
	}
}

func TestConfigDefaults(t *testing.T) {
	config := Config{}
	config.ApplyDefaults()
	if config.DefaultSystemPrompt == "" || config.DefaultReasoningLevel != "off" {
		t.Fatalf("expected chat defaults, got %#v", config)
	}
	if config.Fetch.TimeoutMS == 0 || config.Fetch.MaxBytes == 0 || config.Fetch.MaxChars == 0 {
		t.Fatalf("expected fetch defaults, got %#v", config.Fetch)
	}
}

func TestDefaultAIServicesProxyBaseURLFollowsHomeserverAddress(t *testing.T) {
	if got := defaultAIServicesProxyBaseURL("https://matrix.beeper-staging.com/_hungryserv/test"); got != "https://ai-services.beeper-staging.com/proxy/_/v1" {
		t.Fatalf("unexpected staging AI Services URL from homeserver address %q", got)
	}
}

func TestDefaultProviderBaseURLUsesConnectorHomeserverAddress(t *testing.T) {
	conn := &Connector{
		HomeserverAddress: "https://matrix.beeper-staging.com/_hungryserv/test",
	}
	provider := conn.defaultProviderConfig()
	if provider.BaseURL != "https://ai-services.beeper-staging.com/proxy/_/v1" {
		t.Fatalf("unexpected provider base URL %q", provider.BaseURL)
	}
}

func TestConnectorCapabilitiesAdvertiseAISessionCreation(t *testing.T) {
	conn := &Connector{}
	caps := conn.GetCapabilities()
	if _, ok := caps.Provisioning.GroupCreation["ai"]; !ok {
		t.Fatalf("expected AI group creation capability, got %#v", caps.Provisioning.GroupCreation)
	}
	if !caps.Provisioning.ResolveIdentifier.ContactList || !caps.Provisioning.ResolveIdentifier.Search || !caps.Provisioning.ResolveIdentifier.CreateDM {
		t.Fatalf("expected contact list/search/create DM capabilities, got %#v", caps.Provisioning.ResolveIdentifier)
	}
	_, capVersion := conn.GetBridgeInfoVersion()
	if capVersion < 3 {
		t.Fatalf("capability version must bump when provisioning capabilities change, got %d", capVersion)
	}
}

func TestConfigureBridgeV2MessageStatusesMapsNoPortal(t *testing.T) {
	original := bridgev2.ErrNoPortal
	t.Cleanup(func() {
		bridgev2.ErrNoPortal = original
	})
	configureBridgeV2MessageStatuses()

	status := bridgev2.WrapErrorInStatus(bridgev2.ErrNoPortal)
	if status.Status != event.MessageStatusFail || status.ErrorReason != event.MessageStatusUnsupported {
		t.Fatalf("expected permanent unsupported no-portal status, got %#v", status)
	}
	if status.Message == "" || status.ErrorReason == event.MessageStatusGenericError {
		t.Fatalf("expected specific no-portal message status, got %#v", status)
	}
	if !errors.Is(bridgev2.ErrNoPortal, status.InternalError) {
		t.Fatalf("expected wrapped no-portal error to remain unwrap-compatible")
	}
}

func TestRoomFeaturesDisableReactions(t *testing.T) {
	client := &Client{}
	caps := client.GetCapabilities(context.Background(), nil)
	if caps.ID != "" {
		t.Fatalf("expected room features to use content-derived ID, got %q", caps.ID)
	}
	if caps.GetID() == "" {
		t.Fatalf("expected room features to expose a dedupe ID")
	}
	if caps.Reaction != event.CapLevelUnsupported {
		t.Fatalf("expected reactions to be unsupported, got %d", caps.Reaction)
	}
	if !caps.TypingNotifications {
		t.Fatalf("expected assistant typing notifications")
	}
	if caps.File[event.MsgImage] != nil {
		t.Fatalf("did not expect image support without a vision model")
	}
	if caps.File[event.MsgAudio] != nil || caps.File[event.CapMsgVoice] != nil {
		t.Fatalf("did not expect audio support without a native audio model")
	}
	if caps.File[event.MsgFile] == nil || caps.File[event.MsgFile].MimeTypes["text/*"] != event.CapLevelFullySupported {
		t.Fatalf("expected text file support, got %#v", caps.File[event.MsgFile])
	}
	for _, stateType := range []string{aiid.RoomToolsType, aiid.RoomModelType, aiid.RoomPromptType} {
		if caps.State[stateType] == nil || caps.State[stateType].Level != event.CapLevelFullySupported {
			t.Fatalf("expected %s state support, got %#v", stateType, caps.State[stateType])
		}
	}
}

func TestRoomFeaturesFollowModelInputModalities(t *testing.T) {
	textCaps := roomFeaturesForModel(ai.Model{Input: []string{"text"}})
	if textCaps.File[event.MsgImage] != nil || textCaps.File[event.MsgAudio] != nil || textCaps.File[event.CapMsgVoice] != nil {
		t.Fatalf("text-only model should not advertise media input, got %#v", textCaps.File)
	}
	if textCaps.File[event.MsgFile] == nil {
		t.Fatalf("text-like files should be available through prompt text conversion")
	}

	visionCaps := roomFeaturesForModel(ai.Model{Input: []string{"text", "image"}})
	if visionCaps.File[event.MsgImage] == nil {
		t.Fatalf("vision model should advertise image input")
	}
	if visionCaps.File[event.MsgAudio] != nil {
		t.Fatalf("vision-only model should not advertise audio input")
	}

	audioCaps := roomFeaturesForModel(ai.Model{Input: []string{"text", "audio"}})
	if audioCaps.File[event.MsgAudio] == nil || audioCaps.File[event.CapMsgVoice] == nil {
		t.Fatalf("audio model should advertise audio and voice input")
	}
	if audioCaps.File[event.MsgAudio].MimeTypes["audio/wav"] != event.CapLevelFullySupported ||
		audioCaps.File[event.MsgAudio].MimeTypes["audio/mpeg"] != event.CapLevelFullySupported {
		t.Fatalf("audio model should advertise native wav/mp3 support, got %#v", audioCaps.File[event.MsgAudio])
	}
	if audioCaps.File[event.MsgAudio].Caption != event.CapLevelFullySupported {
		t.Fatalf("audio model should advertise caption support, got %#v", audioCaps.File[event.MsgAudio])
	}
}

func TestUnsupportedPromptAudioAttachment(t *testing.T) {
	if unsupported := unsupportedPromptAudioAttachment([]ai.ContentBlock{{Type: "audio", MimeType: "audio/mpeg"}}); unsupported != "" {
		t.Fatalf("expected mp3 audio to be accepted, got %q", unsupported)
	}
	if unsupported := unsupportedPromptAudioAttachment([]ai.ContentBlock{{Type: "audio", MimeType: "audio/ogg"}}); unsupported != "audio/ogg" {
		t.Fatalf("expected ogg audio to be rejected for native pass-through, got %q", unsupported)
	}
	if unsupported := unsupportedPromptAudioAttachment([]ai.ContentBlock{{Type: "image", MimeType: "audio/ogg"}}); unsupported != "" {
		t.Fatalf("expected non-audio block to be ignored, got %q", unsupported)
	}
}

func TestProviderModelsParsesOptionalModelList(t *testing.T) {
	models := providerModels("gpt-5-mini, gpt-5\nlocal", "gpt-5", "custom", "https://example.test/v1")
	if len(models) != 3 {
		t.Fatalf("expected default plus two unique models, got %#v", models)
	}
	if models[0].ID != "gpt-5" || models[1].ID != "gpt-5-mini" || models[2].ID != "local" {
		t.Fatalf("unexpected model order %#v", models)
	}
}

func TestFetchProviderModelsVerifiesAndBuildsModels(t *testing.T) {
	var gotAuth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		gotAuth = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`{"data":[{"id":"model-a"},{"id":"model-b"},{"id":"model-a"}]}`))
	}))
	defer server.Close()

	models, err := fetchProviderModels(context.Background(), ai.ApiOpenAIResponses, "local", server.URL+"/v1", "key")
	if err != nil {
		t.Fatal(err)
	}
	if gotAuth != "Bearer key" {
		t.Fatalf("unexpected auth header %q", gotAuth)
	}
	if len(models) != 2 || models[0].ID != "model-a" || models[1].ID != "model-b" {
		t.Fatalf("unexpected models %#v", models)
	}
	if models[0].API != ai.ApiOpenAIResponses || models[0].Provider != "local" || models[0].BaseURL != server.URL+"/v1" {
		t.Fatalf("unexpected model route %#v", models[0])
	}
}

func TestFetchProviderModelsRejectsFailedVerification(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "bad key", http.StatusUnauthorized)
	}))
	defer server.Close()

	if _, err := fetchProviderModels(context.Background(), ai.ApiOpenAICompletions, "local", server.URL, "bad"); err == nil {
		t.Fatalf("expected failed verification to return an error")
	}
}

func TestCustomProviderLoginStagesAPIConfigAndDefaultModel(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"id":"gpt-test"},{"id":"gpt-other"}]}`))
	}))
	defer server.Close()

	login := &CustomProviderLogin{config: providerLoginConfig{API: ai.ApiOpenAICompletions}}
	step := login.providerConfigStep()
	if step.StepID != loginStepProviderConfig || len(step.UserInputParams.Fields) != 3 {
		t.Fatalf("unexpected config step %#v", step)
	}

	step, err := login.submitProviderConfig(context.Background(), map[string]string{
		"provider_id": "local-ai",
		"base_url":    server.URL,
		"api_key":     "key",
	})
	if err != nil {
		t.Fatal(err)
	}
	if step.StepID != loginStepProviderDefault {
		t.Fatalf("expected default model step, got %#v", step)
	}
	field := step.UserInputParams.Fields[0]
	if field.Type != bridgev2.LoginInputFieldTypeSelect || field.DefaultValue != "gpt-test" || len(field.Options) != 2 {
		t.Fatalf("unexpected default model field %#v", field)
	}
	if login.config.ProviderID != "local-ai" || login.config.API != ai.ApiOpenAICompletions || len(login.config.Models) != 2 {
		t.Fatalf("login config was not retained: %#v", login.config)
	}
}

func TestModelForProviderAppliesRouteBaseURLToDefaultModel(t *testing.T) {
	conn := &Connector{}
	provider := aiid.ProviderConfig{
		ID:            aiid.DefaultProvider,
		API:           ai.ApiOpenAIResponses,
		Provider:      ai.ProviderOpenAI,
		BaseURL:       "https://ai-services.beeper.com/proxy/_/v1/responses",
		AllowedModels: []string{"gpt-5.5"},
	}
	model := conn.ModelForProvider(provider, "gpt-5.5")
	if model.Provider != ai.ProviderOpenAI || model.ID != "gpt-5.5" {
		t.Fatalf("expected AI Services model, got %#v", model)
	}
	if model.BaseURL != "https://ai-services.beeper.com/proxy/_/v1" {
		t.Fatalf("expected route base URL override, got %q", model.BaseURL)
	}
	if model.ContextWindow == 0 || model.MaxTokens == 0 {
		t.Fatalf("expected generated model metadata to be preserved, got %#v", model)
	}
}

func TestResolveProviderValidatesExplicitModelList(t *testing.T) {
	conn := &Connector{}
	login := &bridgev2.UserLogin{UserLogin: &database.UserLogin{
		ID: "login",
		Metadata: &aiid.UserLoginMetadata{
			Providers: map[string]aiid.ProviderConfig{
				"custom": {
					ID:           "custom",
					Provider:     "custom",
					API:          ai.ApiOpenAIResponses,
					DefaultModel: "allowed",
					Models:       []ai.Model{{ID: "allowed", Provider: "custom", API: ai.ApiOpenAIResponses}},
					Enabled:      true,
				},
			},
			DefaultProviderID: "custom",
		},
	}}
	if _, _, err := conn.ResolveProvider(context.Background(), login, RoomConfig{ModelID: "missing"}); err == nil {
		t.Fatalf("expected missing model to fail")
	}
	_, modelID, err := conn.ResolveProvider(context.Background(), login, RoomConfig{ModelID: "allowed"})
	if err != nil {
		t.Fatal(err)
	}
	if modelID != "allowed" {
		t.Fatalf("unexpected model ID %q", modelID)
	}
}

func TestValidateReasoningLevelRejectsUnsupportedPair(t *testing.T) {
	client := &Client{Main: &Connector{Config: Config{DefaultReasoningLevel: "off"}}}
	model := ai.Model{ID: "plain", Input: []string{"text"}}
	if err := client.validateReasoningLevel(model, RoomConfig{ThinkingLevel: "high"}); err == nil {
		t.Fatalf("expected unsupported reasoning level to fail")
	}
	if err := client.validateReasoningLevel(model, RoomConfig{ThinkingLevel: "off"}); err != nil {
		t.Fatalf("expected off reasoning to be accepted: %v", err)
	}
}

func TestValidateReasoningLevelAcceptsOffForReasoningModel(t *testing.T) {
	client := &Client{Main: &Connector{Config: Config{DefaultReasoningLevel: "off"}}}
	model, ok := ai.GetModel(ai.ProviderOpenAI, "gpt-5.4")
	if !ok {
		t.Fatal("expected generated gpt-5.4 model")
	}
	if err := client.validateReasoningLevel(model, RoomConfig{}); err != nil {
		t.Fatalf("expected default off reasoning to be accepted: %v", err)
	}
}
