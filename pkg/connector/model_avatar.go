package connector

import (
	"context"
	"strings"

	ai "github.com/beeper/ai-bridge/pkg/ai"
	"github.com/beeper/ai-bridge/pkg/aiid"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/id"
)

const defaultAIAssistantAvatarMXC = "mxc://beeper.com/51a668657dd9e0132cc823ad9402c6c2d0fc3321"

func defaultAIAssistantAvatar() *bridgev2.Avatar {
	return &bridgev2.Avatar{
		ID:  networkid.AvatarID(defaultAIAssistantAvatarMXC),
		MXC: id.ContentURIString(defaultAIAssistantAvatarMXC),
	}
}

func modelAvatar(provider aiid.ProviderConfig, model ai.Model) *bridgev2.Avatar {
	key, svg := modelAvatarAsset(provider, model)
	if svg == "" {
		return nil
	}
	return &bridgev2.Avatar{
		ID: networkid.AvatarID("ai-model-provider:" + key),
		Get: func(ctx context.Context) ([]byte, error) {
			return []byte(svg), nil
		},
	}
}

func modelAvatarAsset(provider aiid.ProviderConfig, model ai.Model) (string, string) {
	switch modelAvatarProviderKey(provider, model) {
	case "anthropic":
		return "anthropic", anthropicAvatarSVG
	case "vertex":
		return "vertex", vertexAvatarSVG
	case "openrouter":
		return "openrouter", openRouterAvatarSVG
	case "openai":
		return "openai", openAIAvatarSVG
	default:
		return "beeper-ai", beeperAIAvatarSVG
	}
}

func modelAvatarProviderKey(provider aiid.ProviderConfig, model ai.Model) string {
	switch model.Provider {
	case ai.ProviderAnthropic:
		return "anthropic"
	case ai.ProviderGoogle, ai.ProviderGoogleVertex:
		return "vertex"
	case ai.ProviderOpenRouter:
		return "openrouter"
	case ai.ProviderOpenAI, ai.ProviderOpenAICodex:
		return "openai"
	}
	modelID := strings.ToLower(model.ID)
	switch {
	case strings.HasPrefix(modelID, "claude-"), strings.HasPrefix(modelID, "anthropic/"):
		return "anthropic"
	case strings.HasPrefix(modelID, "gemini-"), strings.HasPrefix(modelID, "google/"):
		return "vertex"
	case strings.HasPrefix(modelID, "openrouter/"):
		return "openrouter"
	case strings.HasPrefix(modelID, "openai/"), strings.HasPrefix(modelID, "gpt-"), strings.HasPrefix(modelID, "o1"), strings.HasPrefix(modelID, "o3"), strings.HasPrefix(modelID, "o4"):
		return "openai"
	}
	providerID := strings.ToLower(provider.ID)
	switch {
	case strings.Contains(providerID, "anthropic"):
		return "anthropic"
	case strings.Contains(providerID, "vertex"), strings.Contains(providerID, "google"):
		return "vertex"
	case strings.Contains(providerID, "openrouter"):
		return "openrouter"
	case strings.Contains(providerID, "openai"):
		return "openai"
	default:
		return "beeper-ai"
	}
}

const beeperAIAvatarSVG = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 96 96"><rect width="96" height="96" rx="22" fill="#111827"/><path d="M24 29h31c13 0 22 8 22 19s-9 19-22 19H41L25 80V67h-1c-8 0-14-6-14-14V43c0-8 6-14 14-14Z" fill="#29D391"/><circle cx="37" cy="48" r="5" fill="#111827"/><circle cx="55" cy="48" r="5" fill="#111827"/></svg>`

const anthropicAvatarSVG = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 96 96"><rect width="96" height="96" rx="22" fill="#D9CBBF"/><path d="M48 18 76 78H62l-6-14H39l-6 14H20L48 18Zm-4 34h8l-4-10-4 10Z" fill="#191919"/></svg>`

const vertexAvatarSVG = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 96 96"><rect width="96" height="96" rx="22" fill="#FFFFFF"/><path d="M48 18 74 33v30L48 78 22 63V33l26-15Z" fill="#4285F4"/><path d="M48 18v30L22 33l26-15Z" fill="#34A853"/><path d="M48 48 74 33v30L48 78V48Z" fill="#FBBC04"/><path d="M48 48v30L22 63V33l26 15Z" fill="#EA4335"/></svg>`

const openAIAvatarSVG = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 96 96"><rect width="96" height="96" rx="22" fill="#0FA47F"/><path d="M48 20c7 0 13 4 16 10 7 1 12 7 12 14 0 3-1 6-3 9 2 7-1 14-7 18-3 2-7 3-11 2-5 5-13 5-19 2-3-2-6-5-7-9-6-3-9-10-7-17 1-4 3-7 7-9 0-7 5-13 12-15 2-3 5-5 7-5Zm-8 17 17 10v-8L43 31c-2 1-3 3-3 6Zm24 15L47 42l-7 4 17 10c3 2 6 0 7-4ZM35 59V39l-7 4v16c2 2 4 2 7 0Zm21 4L39 53v8l14 8c2-1 3-3 3-6Z" fill="#FFFFFF"/></svg>`

const openRouterAvatarSVG = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 96 96"><rect width="96" height="96" rx="22" fill="#101010"/><path d="M18 50h42l-13 13 7 7 25-25-25-25-7 7 13 13H18v10Z" fill="#FFFFFF"/><path d="M18 70h28v8H18v-8Z" fill="#8B5CF6"/></svg>`
