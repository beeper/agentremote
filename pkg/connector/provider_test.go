package connector

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	ai "github.com/beeper/ai-bridge/pkg/ai"
	"github.com/beeper/ai-bridge/pkg/aiid"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
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

func TestModelForProviderBuildsDefaultProviderModelFromConfig(t *testing.T) {
	conn := &Connector{}
	provider := aiid.ProviderConfig{
		ID:       aiid.DefaultProvider,
		API:      ai.ApiOpenAIResponses,
		Provider: ai.ProviderOpenAI,
		BaseURL:  "https://ai-services.beeper-staging.com/proxy/openai/v1",
	}
	model := conn.ModelForProvider(provider, "openai/gpt-5.5")
	if model.ID != "openai/gpt-5.5" || model.Provider != ai.ProviderOpenAI || model.API != ai.ApiOpenAIResponses {
		t.Fatalf("unexpected model %#v", model)
	}
	if model.Reasoning || model.ContextWindow != 128000 || model.MaxTokens != 32000 {
		t.Fatalf("expected config-derived model metadata, got %#v", model)
	}
}

func TestModelForProviderPassesCustomOpenAIProviderModelIDDirectly(t *testing.T) {
	conn := &Connector{}
	provider := aiid.ProviderConfig{
		ID:       "custom-openai",
		API:      ai.ApiOpenAIResponses,
		Provider: ai.ProviderOpenAI,
		BaseURL:  "https://custom.test/v1",
	}
	model := conn.ModelForProvider(provider, "openai/gpt-5.5")
	if model.ID != "openai/gpt-5.5" {
		t.Fatalf("expected custom provider model ID to pass through, got %#v", model)
	}
}

func TestModelForProviderPreservesNonOpenAIBeeperProviderModelID(t *testing.T) {
	conn := &Connector{}
	provider := aiid.ProviderConfig{
		ID:       aiid.DefaultProvider,
		API:      ai.ApiOpenAICompletions,
		Provider: ai.ProviderOpenRouter,
		BaseURL:  "https://openrouter.ai/api/v1",
	}
	model := conn.ModelForProvider(provider, "openai/gpt-5")
	if model.ID != "openai/gpt-5" || model.Provider != ai.ProviderOpenRouter {
		t.Fatalf("expected non-OpenAI Beeper provider model ID to stay qualified, got %#v", model)
	}
}

func TestTitleGenerationModelUsesDefaultGPTMini(t *testing.T) {
	conn := &Connector{}
	client := &Client{Main: conn}
	provider := conn.defaultProviderConfig("@alice:beeper-staging.com")
	model := client.titleGenerationModel(provider, ai.Model{ID: defaultBeeperAIModel})
	if model.ID != defaultTitleGenerationModel || model.Provider != ai.ProviderOpenAI || model.API != ai.ApiOpenAIResponses {
		t.Fatalf("unexpected title model %#v", model)
	}
}

func TestTitleGenerationModelUsesOpenRouterGPTMini(t *testing.T) {
	client := &Client{Main: &Connector{}}
	provider := aiid.ProviderConfig{
		ID:       "openrouter",
		API:      ai.ApiOpenAICompletions,
		Provider: ai.ProviderOpenRouter,
		BaseURL:  "https://openrouter.ai/api/v1",
		Models:   []ai.Model{{ID: openRouterTitleGenerationModel}},
	}
	model := client.titleGenerationModel(provider, ai.Model{ID: "anthropic/claude-sonnet-4.5"})
	if model.ID != openRouterTitleGenerationModel || model.Provider != ai.ProviderOpenRouter || model.API != ai.ApiOpenAICompletions {
		t.Fatalf("unexpected title model %#v", model)
	}
}

func TestTitleGenerationModelAllowsProviderConfiguredModels(t *testing.T) {
	client := &Client{Main: &Connector{}}
	fallback := ai.Model{ID: "local-model", Provider: ai.Provider("local")}
	provider := aiid.ProviderConfig{
		ID:       "local",
		API:      ai.ApiOpenAIResponses,
		Provider: ai.ProviderOpenAI,
		Models:   []ai.Model{{ID: defaultTitleGenerationModel}, fallback},
	}
	model := client.titleGenerationModel(provider, fallback)
	if model.ID != defaultTitleGenerationModel || model.Provider != ai.ProviderOpenAI {
		t.Fatalf("expected config-derived title model, got %#v", model)
	}
}

func TestDefaultProviderAuthUsesAppServiceToken(t *testing.T) {
	client := &Client{
		Main: &Connector{
			AppServiceToken: "as-token",
		},
		UserLogin: &bridgev2.UserLogin{UserLogin: &database.UserLogin{
			UserMXID: "@qatest9033045029:beeper-staging.com",
		}},
	}
	auth, err := client.authForProvider(aiid.ProviderConfig{ID: aiid.DefaultProvider})(context.Background(), ai.Model{})
	if err != nil {
		t.Fatal(err)
	}
	payload, ok := strings.CutPrefix(auth.APIKey, aiServicesAppserviceTokenPrefix)
	if !ok {
		t.Fatalf("expected appservice bearer token, got %q", auth.APIKey)
	}
	decoded, err := base64.RawURLEncoding.DecodeString(payload)
	if err != nil {
		t.Fatal(err)
	}
	var token aiServicesAppserviceToken
	if err = json.Unmarshal(decoded, &token); err != nil {
		t.Fatal(err)
	}
	if token.ASToken != "as-token" || token.Username != "qatest9033045029" {
		t.Fatalf("unexpected appservice token %#v", token)
	}
	if len(auth.Headers) != 0 {
		t.Fatalf("expected no extra headers, got %#v", auth.Headers)
	}
}

func TestDefaultProviderAuthRequiresBeeperUsername(t *testing.T) {
	client := &Client{
		Main: &Connector{
			AppServiceToken: "as-token",
		},
	}
	_, err := client.authForProvider(aiid.ProviderConfig{ID: aiid.DefaultProvider})(context.Background(), ai.Model{})
	if err == nil || !strings.Contains(err.Error(), "Beeper username") {
		t.Fatalf("expected Beeper username error, got %v", err)
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

func TestDefaultAIServicesOpenAIProxyBaseURLUsesUserHomeserver(t *testing.T) {
	conn := &Connector{}
	tests := map[string]string{
		"@alice:beeper.localtest.me": "https://ai-services.beeper.localtest.me/proxy/openai/v1",
		"@alice:beeper-dev.com":      "https://ai-services.beeper-dev.com/proxy/openai/v1",
		"@alice:beeper-staging.com":  "https://ai-services.beeper-staging.com/proxy/openai/v1",
		"@alice:beeper.com":          "https://ai-services.beeper.com/proxy/openai/v1",
	}
	for userMXID, want := range tests {
		if got := conn.defaultAIServicesOpenAIProxyBaseURL(id.UserID(userMXID)); got != want {
			t.Fatalf("defaultAIServicesOpenAIProxyBaseURL(%q) = %q, want %q", userMXID, got, want)
		}
	}
}

func TestDefaultProviderBaseURLUsesUserHomeserver(t *testing.T) {
	conn := &Connector{}
	provider := conn.defaultProviderConfig("@alice:beeper-staging.com")
	if provider.BaseURL != "https://ai-services.beeper-staging.com/proxy/openai/v1" {
		t.Fatalf("unexpected provider base URL %q", provider.BaseURL)
	}
}

func TestDefaultProviderBaseURLUsesInternalLocalServiceForLocalUser(t *testing.T) {
	conn := &Connector{HomeserverURL: "http://megahungry-proxy.megahungry/api/proxy/bridge-user"}
	provider := conn.defaultProviderConfig("@alice:beeper.localtest.me")
	if provider.BaseURL != "http://ai-services.beeper/proxy/openai/v1" {
		t.Fatalf("unexpected provider base URL %q", provider.BaseURL)
	}
}

func TestDefaultProviderBaseURLUsesExternalLocaltestServiceForSelfHosted(t *testing.T) {
	conn := &Connector{HomeserverURL: "https://matrix.beeper.localtest.me/_hungryserv/bridge-user"}
	provider := conn.defaultProviderConfig("@alice:beeper.localtest.me")
	if provider.BaseURL != "https://ai-services.beeper.localtest.me/proxy/openai/v1" {
		t.Fatalf("unexpected provider base URL %q", provider.BaseURL)
	}
}

func TestDefaultProviderBaseURLRejectsUserHomeserverMismatch(t *testing.T) {
	conn := &Connector{HomeserverURL: "https://matrix.beeper.com/_hungryserv/bridge-user"}
	provider := conn.defaultProviderConfig("@alice:evil.example")
	if provider.BaseURL != "" {
		t.Fatalf("expected no default provider route for mismatched user homeserver, got %q", provider.BaseURL)
	}
}

func TestDefaultProviderIgnoresPersistedMetadata(t *testing.T) {
	conn := &Connector{}
	login := &bridgev2.UserLogin{UserLogin: &database.UserLogin{
		UserMXID: "@alice:beeper.localtest.me",
		Metadata: &aiid.UserLoginMetadata{Providers: map[string]aiid.ProviderConfig{
			aiid.DefaultProvider: {
				ID:      aiid.DefaultProvider,
				BaseURL: "https://ai-services.megahungry-proxy.megahungry/proxy/openai/v1",
			},
		}},
	}}
	ensureMetadata(login.Metadata.(*aiid.UserLoginMetadata))
	provider := conn.providersForLogin(login)[aiid.DefaultProvider]
	if provider.BaseURL != "https://ai-services.beeper.localtest.me/proxy/openai/v1" {
		t.Fatalf("expected computed default provider, got %#v", provider)
	}
}

func TestDefaultProviderHasNoRouteWithoutUserHomeserver(t *testing.T) {
	conn := &Connector{}
	provider := conn.defaultProviderConfig("")
	if provider.BaseURL != "" {
		t.Fatalf("expected provider without user homeserver to have no route, got %q", provider.BaseURL)
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

func TestNoAIChatStatusIsLocalAndPermanent(t *testing.T) {
	status := errNoAIChat()
	if status.Status != event.MessageStatusFail || status.ErrorReason != event.MessageStatusUnsupported {
		t.Fatalf("expected permanent unsupported no-portal status, got %#v", status)
	}
	if status.Message == "" || status.ErrorReason == event.MessageStatusGenericError {
		t.Fatalf("expected specific no-portal message status, got %#v", status)
	}
	if status.InternalError == nil || status.InternalError.Error() != "room is not an AI chat" {
		t.Fatalf("expected local no-AI-chat internal error, got %#v", status.InternalError)
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
		if caps.State[stateType] != nil {
			t.Fatalf("did not expect %s state support without arbitrary state support, got %#v", stateType, caps.State[stateType])
		}
	}
	withState := roomFeaturesForModel(ai.Model{}, true)
	for _, stateType := range []string{aiid.RoomToolsType, aiid.RoomModelType, aiid.RoomPromptType} {
		if withState.State[stateType] == nil || withState.State[stateType].Level != event.CapLevelFullySupported {
			t.Fatalf("expected %s state support with arbitrary state support, got %#v", stateType, withState.State[stateType])
		}
	}
}

func TestRoomFeaturesFollowModelInputModalities(t *testing.T) {
	textCaps := roomFeaturesForModel(ai.Model{Input: []string{"text"}}, true)
	if textCaps.File[event.MsgImage] != nil || textCaps.File[event.MsgAudio] != nil || textCaps.File[event.CapMsgVoice] != nil {
		t.Fatalf("text-only model should not advertise media input, got %#v", textCaps.File)
	}
	if textCaps.File[event.MsgFile] == nil {
		t.Fatalf("text-like files should be available through prompt text conversion")
	}

	visionCaps := roomFeaturesForModel(ai.Model{Input: []string{"text", "image"}}, true)
	if visionCaps.File[event.MsgImage] == nil {
		t.Fatalf("vision model should advertise image input")
	}
	if visionCaps.File[event.MsgAudio] != nil {
		t.Fatalf("vision-only model should not advertise audio input")
	}

	audioCaps := roomFeaturesForModel(ai.Model{Input: []string{"text", "audio"}}, true)
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

func TestFetchProviderModelsRespectsPublishedProviderRoutes(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":[
			{"id":"claude-sonnet-4-5","name":"Claude Sonnet 4.5","provider":{"id":"wpcom_anthropic","model_id":"claude-sonnet-4-5","api":"openai-responses"}},
			{"id":"gemini-2.5-flash-lite","name":"Gemini 2.5 Flash Lite","provider":{"id":"wpcom_vertex","model_id":"gemini-2.5-flash-lite","api":"openai-responses"}}
		]}`))
	}))
	defer server.Close()

	models, err := fetchProviderModels(context.Background(), ai.ApiOpenAIResponses, "local", server.URL+"/proxy/openai/v1", "key")
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
		ID:       aiid.DefaultProvider,
		API:      ai.ApiOpenAIResponses,
		Provider: ai.ProviderOpenAI,
		BaseURL:  "https://ai-services.beeper.com/proxy/openai/v1/responses",
	}
	model := conn.ModelForProvider(provider, "gpt-5.5")
	if model.Provider != ai.ProviderOpenAI || model.ID != "gpt-5.5" {
		t.Fatalf("expected AI Services model, got %#v", model)
	}
	if model.BaseURL != "https://ai-services.beeper.com/proxy/openai/v1" {
		t.Fatalf("expected route base URL override, got %q", model.BaseURL)
	}
	if model.ContextWindow != 128000 || model.MaxTokens != 32000 {
		t.Fatalf("expected config-derived model metadata, got %#v", model)
	}
}

func TestResolveProviderRequiresListedModelWhenModelListExists(t *testing.T) {
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
				},
			},
		},
	}}
	_, _, err := conn.ResolveProvider(context.Background(), login, RoomConfig{ProviderID: "custom", ModelID: "missing"})
	if err == nil {
		t.Fatal("expected missing model to be rejected")
	}
	_, modelID, err := conn.ResolveProvider(context.Background(), login, RoomConfig{ProviderID: "custom", ModelID: "allowed"})
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

func TestDefaultReasoningLevelClampsForMandatoryReasoningModel(t *testing.T) {
	client := &Client{Main: &Connector{Config: Config{DefaultReasoningLevel: "off"}}}
	model := ai.Model{
		ID:                   "minimax/minimax-m2.7",
		Provider:             ai.ProviderOpenRouter,
		Reasoning:            true,
		DefaultThinkingLevel: ai.ModelThinkingLevelLow,
		ThinkingLevelMap:     map[ai.ModelThinkingLevel]*string{ai.ModelThinkingLevelOff: nil},
	}
	if got := client.reasoningLevelForModel(model, RoomConfig{}); got != "low" {
		t.Fatalf("expected default off to clamp to low, got %q", got)
	}
	if err := client.validateReasoningLevel(model, RoomConfig{}); err != nil {
		t.Fatalf("expected clamped default reasoning to be accepted: %v", err)
	}
	if err := client.validateReasoningLevel(model, RoomConfig{ThinkingLevel: "off"}); err == nil {
		t.Fatalf("expected explicit off reasoning to be rejected")
	}
}

func TestNormalizeProviderModelInheritsCatalogReasoningMetadata(t *testing.T) {
	model := normalizeProviderModel(ai.Model{
		ID:                   "minimax/minimax-m2.7",
		Provider:             ai.ProviderOpenRouter,
		Reasoning:            true,
		DefaultThinkingLevel: ai.ModelThinkingLevelLow,
		ThinkingLevelMap:     map[ai.ModelThinkingLevel]*string{ai.ModelThinkingLevelOff: nil},
	}, aiid.ProviderConfig{
		ID:       aiid.DefaultProvider,
		Provider: ai.ProviderOpenAI,
		API:      ai.ApiOpenAIResponses,
		BaseURL:  "https://ai-services.test/proxy/openrouter/v1",
	})
	if roomThinkingLevelSupported(model, ai.ModelThinkingLevelOff) {
		t.Fatalf("expected normalized MiniMax M2.7 catalog model to reject off reasoning")
	}
}
