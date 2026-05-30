package providers

import "strings"

func isBeeperAIProxyBaseURL(baseURL string) bool {
	return strings.Contains(baseURL, "ai-services.") ||
		strings.Contains(baseURL, "/proxy/anthropic") ||
		strings.Contains(baseURL, "/proxy/vertex")
}
