package aiid

import (
	ai "github.com/beeper/ai-bridge/pkg/ai"
)

type ProviderConfig struct {
	ID           string            `json:"id" yaml:"id"`
	DisplayName  string            `json:"display_name" yaml:"display_name"`
	API          ai.Api            `json:"api" yaml:"api"`
	Provider     ai.Provider       `json:"provider" yaml:"provider"`
	BaseURL      string            `json:"base_url" yaml:"base_url"`
	APIKey       string            `json:"api_key,omitempty" yaml:"api_key,omitempty"`
	RefreshToken string            `json:"refresh_token,omitempty" yaml:"refresh_token,omitempty"`
	ExpiresAtMS  int64             `json:"expires_at_ms,omitempty" yaml:"expires_at_ms,omitempty"`
	Headers      map[string]string `json:"headers,omitempty" yaml:"headers,omitempty"`
	DefaultModel string            `json:"default_model,omitempty" yaml:"default_model,omitempty"`
	Models       []ai.Model        `json:"models,omitempty" yaml:"models,omitempty"`
}

type UserLoginMetadata struct {
	Providers map[string]ProviderConfig `json:"providers,omitempty"`
}

type PortalMetadata struct {
	SessionID        string `json:"session_id,omitempty"`
	AutoTitlePending bool   `json:"auto_title_pending,omitempty"`
	LastRunID        string `json:"last_run_id,omitempty"`
}

type MessageMetadata struct {
	SessionEntryID string   `json:"session_entry_id,omitempty"`
	Role           string   `json:"role,omitempty"`
	RunID          string   `json:"run_id,omitempty"`
	ProviderID     string   `json:"provider_id,omitempty"`
	ModelID        string   `json:"model_id,omitempty"`
	ResponseID     string   `json:"response_id,omitempty"`
	ContentIndex   int      `json:"content_index,omitempty"`
	Usage          ai.Usage `json:"usage,omitempty"`
	StopReason     string   `json:"stop_reason,omitempty"`
	ErrorMessage   string   `json:"error_message,omitempty"`
	StreamStatus   string   `json:"stream_status,omitempty"`
}

type ReactionMetadata struct{}

type MediaMetadata struct {
	LoginID      string `json:"login_id,omitempty"`
	ProviderID   string `json:"provider_id,omitempty"`
	SessionID    string `json:"session_id,omitempty"`
	EntryID      string `json:"entry_id,omitempty"`
	ContentIndex int    `json:"content_index,omitempty"`
	MimeType     string `json:"mime_type,omitempty"`
	Retrieval    any    `json:"retrieval,omitempty"`
}
