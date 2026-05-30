package ai

var extendedThinkingLevels = []ModelThinkingLevel{
	ModelThinkingLevelOff,
	ModelThinkingLevelMinimal,
	ModelThinkingLevelLow,
	ModelThinkingLevelMedium,
	ModelThinkingLevelHigh,
	ModelThinkingLevelXHigh,
}

const (
	ModelThinkingLevelMinimal ModelThinkingLevel = "minimal"
	ModelThinkingLevelLow     ModelThinkingLevel = "low"
	ModelThinkingLevelMedium  ModelThinkingLevel = "medium"
	ModelThinkingLevelHigh    ModelThinkingLevel = "high"
	ModelThinkingLevelXHigh   ModelThinkingLevel = "xhigh"
)

func GetModel(provider Provider, modelID string) (Model, bool) {
	models, ok := Models[provider]
	if !ok {
		return Model{}, false
	}
	model, ok := models[modelID]
	return model, ok
}

func GetProviders() []Provider {
	return append([]Provider{}, modelProviderOrder...)
}

func GetModels(provider Provider) []Model {
	models := Models[provider]
	order := modelIDOrder[provider]
	out := make([]Model, 0, len(order))
	for _, modelID := range order {
		if model, ok := models[modelID]; ok {
			out = append(out, model)
		}
	}
	return out
}

func GetSupportedThinkingLevels(model Model) []ModelThinkingLevel {
	if !model.Reasoning {
		return []ModelThinkingLevel{ModelThinkingLevelOff}
	}
	levels := make([]ModelThinkingLevel, 0, len(extendedThinkingLevels))
	for _, level := range extendedThinkingLevels {
		if level == ModelThinkingLevelOff {
			if mapped, ok := model.ThinkingLevelMap[level]; ok && mapped == nil {
				continue
			}
			levels = append(levels, level)
			continue
		}
		mapped, ok := model.ThinkingLevelMap[level]
		if ok && mapped == nil {
			continue
		}
		if level == ModelThinkingLevelXHigh && !ok {
			continue
		}
		levels = append(levels, level)
	}
	return levels
}

func ClampThinkingLevel(model Model, level ModelThinkingLevel) ModelThinkingLevel {
	available := GetSupportedThinkingLevels(model)
	if containsThinkingLevel(available, level) {
		return level
	}
	requestedIndex := thinkingLevelIndex(level)
	if requestedIndex < 0 {
		return firstThinkingLevelOrOff(available)
	}
	for i := requestedIndex; i < len(extendedThinkingLevels); i++ {
		candidate := extendedThinkingLevels[i]
		if containsThinkingLevel(available, candidate) {
			return candidate
		}
	}
	for i := requestedIndex - 1; i >= 0; i-- {
		candidate := extendedThinkingLevels[i]
		if containsThinkingLevel(available, candidate) {
			return candidate
		}
	}
	return firstThinkingLevelOrOff(available)
}

func ModelsAreEqual(a *Model, b *Model) bool {
	if a == nil || b == nil {
		return false
	}
	return a.ID == b.ID && a.Provider == b.Provider
}

func containsThinkingLevel(levels []ModelThinkingLevel, want ModelThinkingLevel) bool {
	for _, level := range levels {
		if level == want {
			return true
		}
	}
	return false
}

func thinkingLevelIndex(level ModelThinkingLevel) int {
	for index, candidate := range extendedThinkingLevels {
		if candidate == level {
			return index
		}
	}
	return -1
}

func firstThinkingLevelOrOff(levels []ModelThinkingLevel) ModelThinkingLevel {
	if len(levels) == 0 {
		return ModelThinkingLevelOff
	}
	return levels[0]
}
