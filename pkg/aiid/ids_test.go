package aiid

import (
	"encoding/json"
	"strings"
	"testing"

	ai "github.com/beeper/ai-bridge/pkg/ai"
	"maunium.net/go/mautrix/id"
)

func TestPortalAndAssistantIDsAreStable(t *testing.T) {
	loginID := DefaultLoginID(id.UserID("@alice:example.com"))
	portalKey := PortalKey(id.RoomID("!room:example.com"), loginID)
	if !strings.HasPrefix(string(loginID), "default:") {
		t.Fatalf("expected default login prefix, got %q", loginID)
	}
	if !strings.HasPrefix(string(portalKey.ID), "mxroom:") {
		t.Fatalf("expected mxroom portal prefix, got %q", portalKey.ID)
	}
	if portalKey.Receiver != loginID {
		t.Fatalf("expected receiver %q, got %q", loginID, portalKey.Receiver)
	}
	if got := AssistantUserID(); got != "assistant:ai" {
		t.Fatalf("unexpected assistant ID %q", got)
	}
	providerID, modelID, ok := ParseModelContactID(ModelContactID("beeper/openai", "gpt-5:latest"))
	if !ok || providerID != "beeper/openai" || modelID != "gpt-5:latest" {
		t.Fatalf("model contact ID did not parse: %q %q %v", providerID, modelID, ok)
	}
	providerID, modelID, ok = ParseModelContactID(ModelContactID("beeper", "gpt-5"))
	if !ok || providerID != "beeper" || modelID != "gpt-5" {
		t.Fatalf("model contact ID did not parse: %q %q %v", providerID, modelID, ok)
	}
	providerLogin := ProviderLoginID(loginID, "openai")
	parent, providerID, ok := ParseProviderLoginID(providerLogin)
	if !ok || parent != loginID || providerID != "openai" {
		t.Fatalf("provider login ID did not parse: %q %q %v", parent, providerID, ok)
	}
}

func TestMetadataJSONRoundTrip(t *testing.T) {
	meta := &UserLoginMetadata{
		Providers: map[string]ProviderConfig{
			"custom": {
				ID:           "custom",
				DisplayName:  "Custom",
				API:          ai.ApiOpenAIResponses,
				Provider:     "custom",
				BaseURL:      "https://example.com/v1/responses",
				DefaultModel: "gpt-5",
			},
		},
	}
	raw, err := json.Marshal(meta)
	if err != nil {
		t.Fatal(err)
	}
	var decoded UserLoginMetadata
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Providers["custom"].BaseURL != "https://example.com/v1/responses" {
		t.Fatalf("provider metadata did not round trip: %#v", decoded.Providers["custom"])
	}
	if decoded.Providers["custom"].DefaultModel != "gpt-5" {
		t.Fatalf("metadata did not round trip: %#v", decoded)
	}
}

func TestMediaIDForEncodesMetadata(t *testing.T) {
	metadata := MediaMetadata{
		LoginID:      "login",
		ProviderID:   "provider",
		SessionID:    "session",
		EntryID:      "entry",
		ContentIndex: 1,
		MimeType:     "image/png",
	}
	mediaID, err := MediaIDFor(metadata)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(mediaID), "ai:") {
		t.Fatalf("expected encoded AI media ID, got %q", mediaID)
	}
	decoded, err := ParseMediaID(mediaID)
	if err != nil {
		t.Fatal(err)
	}
	if decoded != metadata {
		t.Fatalf("metadata did not round trip: %#v", decoded)
	}
}
