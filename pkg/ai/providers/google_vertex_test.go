package providers

import (
	"testing"

	ai "github.com/beeper/ai-bridge/pkg/ai"
)

func TestGoogleVertexBeeperProxyEndpointDoesNotRequireGCPProject(t *testing.T) {
	model := ai.Model{
		ID:       "gemini-2.5-flash-lite",
		API:      ai.ApiGoogleVertex,
		Provider: ai.ProviderGoogleVertex,
		BaseURL:  "https://ai-services.beeper.localtest.me/proxy/vertex",
	}
	endpoint, err := googleVertexEndpoint(model, GoogleVertexOptions{}, false, true)
	if err != nil {
		t.Fatal(err)
	}
	want := "https://ai-services.beeper.localtest.me/proxy/vertex/v1/publishers/google/models/gemini-2.5-flash-lite:streamGenerateContent?alt=sse"
	if endpoint != want {
		t.Fatalf("endpoint = %q, want %q", endpoint, want)
	}
}
