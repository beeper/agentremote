package modelcatalog

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/beeper/ai-bridge/pkg/ai"
)

func TestBuildFetchesAndFiltersRuntimeSafeCatalog(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/models.dev", func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`{
			"openai": {"models": {
				"gpt-5.5": {
					"id": "gpt-5.5",
					"name": "GPT-5.5",
					"reasoning": true,
					"tool_call": true,
					"modalities": {"input": ["text", "image"], "output": ["text"]},
					"cost": {"input": 5, "output": 30, "cache_read": 0.5},
					"limit": {"context": 1050000, "output": 128000}
				}
			}},
			"anthropic": {"models": {
				"claude-sonnet-4-5": {
					"id": "claude-sonnet-4-5",
					"name": "Claude Sonnet 4.5",
					"reasoning": true,
					"tool_call": true,
					"modalities": {"input": ["text", "image"], "output": ["text"]},
					"cost": {"input": 3, "output": 15},
					"limit": {"context": 200000, "output": 64000}
				}
			}},
			"google-vertex": {"models": {
				"gemini-3-pro-preview": {
					"id": "gemini-3-pro-preview",
					"name": "Gemini 3 Pro Preview",
					"reasoning": true,
					"tool_call": true,
					"modalities": {"input": ["text"], "output": ["text"]},
					"cost": {"input": 2, "output": 12},
					"limit": {"context": 1000000, "output": 64000}
				}
			}}
		}`))
	})
	mux.HandleFunc("/openrouter", func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`{"data": [{
			"id": "anthropic/claude-sonnet-4.5",
			"name": "Anthropic: Claude Sonnet 4.5",
			"supported_parameters": ["tools", "reasoning"],
			"architecture": {"input_modalities": ["text", "image"]},
			"pricing": {"prompt": "0.000003", "completion": "0.000015", "input_cache_read": "0.0000003"},
			"context_length": 200000,
			"top_provider": {"max_completion_tokens": 64000}
		}]}`))
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	catalog, err := Build(context.Background(), Options{
		HTTPClient:          server.Client(),
		ModelsDevURL:        server.URL + "/models.dev",
		OpenRouterModelsURL: server.URL + "/openrouter",
	})
	if err != nil {
		t.Fatal(err)
	}

	if catalog.Count() != 4 {
		t.Fatalf("expected runtime-safe catalog to emit four models, got %d", catalog.Count())
	}
	if _, ok := catalog.Models[ai.ProviderAnthropic]["claude-sonnet-4-5"]; !ok {
		t.Fatal("expected direct Anthropic model after provider registration")
	}
	if _, ok := catalog.Models[ai.ProviderGoogleVertex]["gemini-3-pro-preview"]; !ok {
		t.Fatal("expected Vertex model after provider registration")
	}
	if got := catalog.Models[ai.ProviderOpenAI]["gpt-5.5"].ThinkingLevelMap[ai.ModelThinkingLevelXHigh]; got == nil || *got != "xhigh" {
		t.Fatalf("expected gpt-5.5 xhigh metadata, got %#v", got)
	}
	if got := catalog.Models[ai.ProviderOpenRouter]["anthropic/claude-sonnet-4.5"].Compat["cacheControlFormat"]; got != "anthropic" {
		t.Fatalf("expected OpenRouter Anthropic cache control compat, got %#v", got)
	}
	if len(catalog.Skipped) != 0 {
		t.Fatalf("expected no skipped registered providers, got %#v", catalog.Skipped)
	}
}

func TestBuildCanIncludeUnregisteredProviders(t *testing.T) {
	catalog := BuildFromModels([]ai.Model{
		{ID: "claude-sonnet-4-5", Provider: ai.ProviderAnthropic, API: ai.ApiAnthropicMessages},
		{ID: "gemini-3-pro-preview", Provider: ai.ProviderGoogleVertex, API: ai.ApiGoogleVertex},
	}, Options{IncludeUnregistered: true})

	if catalog.Count() != 2 {
		t.Fatalf("expected two models, got %d", catalog.Count())
	}
	if _, ok := catalog.Models[ai.ProviderAnthropic]["claude-sonnet-4-5"]; !ok {
		t.Fatal("expected Anthropic model")
	}
	if _, ok := catalog.Models[ai.ProviderGoogleVertex]["gemini-3-pro-preview"]; !ok {
		t.Fatal("expected Vertex model")
	}
}

func TestGenerateGoSource(t *testing.T) {
	catalog := BuildFromModels([]ai.Model{{
		ID:       "gpt-5",
		Name:     "GPT-5",
		API:      ai.ApiOpenAIResponses,
		Provider: ai.ProviderOpenAI,
		BaseURL:  "https://api.openai.com/v1",
		Input:    []string{"text"},
	}}, Options{})

	source, err := GenerateGoSource(catalog, "ai")
	if err != nil {
		t.Fatal(err)
	}
	text := string(source)
	for _, want := range []string{"package ai", "var modelsJSON = `", `"gpt-5"`, "func mustLoadModels()"} {
		if !strings.Contains(text, want) {
			t.Fatalf("generated source missing %q:\n%s", want, text)
		}
	}
}
