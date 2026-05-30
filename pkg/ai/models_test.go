package ai

import "testing"

func TestModelRegistryGeneratedSurface(t *testing.T) {
	providers := GetProviders()
	if len(providers) != 2 || providers[0] != ProviderOpenAI || providers[1] != ProviderOpenRouter {
		t.Fatalf("expected generated provider order, got %#v", providers)
	}
	models := GetModels(ProviderOpenAI)
	if len(models) == 0 {
		t.Fatalf("expected generated OpenAI models")
	}
	model, ok := GetModel(ProviderOpenAI, models[0].ID)
	if !ok || model.ID != models[0].ID || model.Provider != ProviderOpenAI {
		t.Fatalf("expected model lookup to round trip, got %#v ok=%v", model, ok)
	}
	if _, ok := GetModel("missing", "missing"); ok {
		t.Fatalf("expected missing model lookup to fail")
	}
}

func TestGetSupportedThinkingLevels(t *testing.T) {
	if got := GetSupportedThinkingLevels(Model{}); len(got) != 1 || got[0] != ModelThinkingLevelOff {
		t.Fatalf("expected non-reasoning model to support off only, got %#v", got)
	}
	disabled := ""
	model := Model{Reasoning: true, ThinkingLevelMap: map[ModelThinkingLevel]*string{
		ModelThinkingLevelLow:   nil,
		ModelThinkingLevelXHigh: &disabled,
	}}
	got := GetSupportedThinkingLevels(model)
	want := []ModelThinkingLevel{
		ModelThinkingLevelOff,
		ModelThinkingLevelMinimal,
		ModelThinkingLevelMedium,
		ModelThinkingLevelHigh,
		ModelThinkingLevelXHigh,
	}
	if len(got) != len(want) {
		t.Fatalf("expected levels %#v, got %#v", want, got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("expected levels %#v, got %#v", want, got)
		}
	}

	mandatory := Model{Reasoning: true, ThinkingLevelMap: map[ModelThinkingLevel]*string{
		ModelThinkingLevelOff: nil,
	}}
	got = GetSupportedThinkingLevels(mandatory)
	if len(got) == 0 || got[0] == ModelThinkingLevelOff {
		t.Fatalf("expected off to be omitted for mandatory reasoning model, got %#v", got)
	}
}

func TestClampThinkingLevel(t *testing.T) {
	xhigh := ""
	model := Model{Reasoning: true, ThinkingLevelMap: map[ModelThinkingLevel]*string{
		ModelThinkingLevelMedium: nil,
		ModelThinkingLevelHigh:   nil,
		ModelThinkingLevelXHigh:  &xhigh,
	}}
	if got := ClampThinkingLevel(model, ModelThinkingLevelMedium); got != ModelThinkingLevelXHigh {
		t.Fatalf("expected medium to clamp upward to xhigh, got %q", got)
	}
	if got := ClampThinkingLevel(Model{}, ModelThinkingLevelHigh); got != ModelThinkingLevelOff {
		t.Fatalf("expected non-reasoning model to clamp to off, got %q", got)
	}
}

func TestModelsAreEqual(t *testing.T) {
	a := &Model{ID: "gpt", Provider: ProviderOpenAI}
	b := &Model{ID: "gpt", Provider: ProviderOpenAI}
	c := &Model{ID: "gpt", Provider: ProviderOpenRouter}
	if !ModelsAreEqual(a, b) {
		t.Fatal("expected models to be equal")
	}
	if ModelsAreEqual(a, c) || ModelsAreEqual(a, nil) {
		t.Fatal("expected different or nil models not to be equal")
	}
}
