package connector

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/beeper/ai-bridge/pkg/aiid"
	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

type AIRoomStateStore struct {
	connector *Connector
}

type RoomConfig struct {
	ProviderID       string
	ModelID          string
	AdditionalPrompt string
	ThinkingLevel    string
	DisabledTools    []string

	modelStatePresent  bool
	modelStateModel    string
	modelStateReason   string
	modelStateEventID  string
	promptStateEventID string
}

func (c *Connector) aiRoomStateStore() AIRoomStateStore {
	return AIRoomStateStore{connector: c}
}

func (s AIRoomStateStore) canRead() bool {
	return s.connector != nil && s.connector.Bridge != nil && implementsArbitraryRoomState(s.connector.Bridge.Matrix)
}

func (c *Connector) ReadRoomConfig(ctx context.Context, roomID id.RoomID) (RoomConfig, string, error) {
	return c.aiRoomStateStore().ReadConfig(ctx, roomID)
}

func (s AIRoomStateStore) ReadConfig(ctx context.Context, roomID id.RoomID) (RoomConfig, string, error) {
	config := RoomConfig{}
	reader, ok := s.connector.Bridge.Matrix.(bridgev2.MatrixConnectorWithArbitraryRoomState)
	if !ok {
		return config, "", nil
	}
	stateEventIDs := []string{}
	if raw, eventID, err := s.readRoomState(ctx, reader, roomID, aiid.RoomModelType); err != nil {
		return RoomConfig{}, "", err
	} else if raw != nil {
		config.modelStatePresent = true
		config.modelStateModel = firstString(raw, "model")
		config.modelStateReason = firstString(raw, "reasoning")
		config.modelStateEventID = eventID
		applyRoomModelConfig(&config, raw)
		if _, ok := raw["reasoning"]; !ok {
			config.ThinkingLevel = ""
		}
		stateEventIDs = append(stateEventIDs, eventID)
	}
	if raw, eventID, err := s.readRoomState(ctx, reader, roomID, aiid.RoomPromptType); err != nil {
		return RoomConfig{}, "", err
	} else if raw != nil {
		config.AdditionalPrompt = firstString(raw, "prompt")
		config.promptStateEventID = eventID
		stateEventIDs = append(stateEventIDs, eventID)
	}
	if raw, eventID, err := s.readRoomState(ctx, reader, roomID, aiid.RoomToolsType); err != nil {
		return RoomConfig{}, "", err
	} else if raw != nil {
		config.DisabledTools = stringSlice(raw["disabled"])
		stateEventIDs = append(stateEventIDs, eventID)
	}
	return config, strings.Join(stateEventIDs, ","), nil
}

func (s AIRoomStateStore) Write(ctx context.Context, portal *bridgev2.Portal, stateType string, content map[string]any) (string, error) {
	if portal == nil || portal.MXID == "" {
		return "", fmt.Errorf("portal room is not available to write room state")
	}
	// bridgev2 exposes arbitrary room-state reads through MatrixConnectorWithArbitraryRoomState,
	// but does not expose an equivalent public write API. Keep this local-only escape hatch
	// isolated here until upstream has a connector-facing arbitrary state writer.
	resp, err := portal.Internal().SendStateWithIntentOrBot(ctx, nil, event.Type{Type: stateType, Class: event.StateEventType}, "", &event.Content{Raw: content}, time.Now())
	if err != nil {
		return "", err
	}
	if resp == nil {
		return "", nil
	}
	return string(resp.EventID), nil
}

func (c *Connector) ResolveProvider(ctx context.Context, login *bridgev2.UserLogin, roomConfig RoomConfig) (aiid.ProviderConfig, string, error) {
	providerID := roomConfig.ProviderID
	if providerID == "" {
		providerID = aiid.DefaultProvider
	}
	provider, ok := c.providersForLogin(login)[providerID]
	if !ok {
		return aiid.ProviderConfig{}, "", fmt.Errorf("provider %s is not available for login %s", providerID, login.ID)
	}
	modelID := roomConfig.ModelID
	if modelID == "" {
		modelID = provider.DefaultModel
	}
	if modelID == "" {
		return aiid.ProviderConfig{}, "", fmt.Errorf("provider %s has no selected model", providerID)
	}
	if !providerAllowsModel(provider, modelID) {
		return aiid.ProviderConfig{}, "", fmt.Errorf("model %s is not available for provider %s", modelID, providerID)
	}
	return provider, modelID, nil
}

func providerAllowsModel(provider aiid.ProviderConfig, modelID string) bool {
	if len(provider.Models) > 0 {
		return providerHasModel(provider, modelID)
	}
	return strings.TrimSpace(modelID) != ""
}

func providerHasModel(provider aiid.ProviderConfig, modelID string) bool {
	for _, model := range provider.Models {
		if model.ID == modelID {
			return true
		}
	}
	return false
}

func (s AIRoomStateStore) readRoomState(ctx context.Context, reader bridgev2.MatrixConnectorWithArbitraryRoomState, roomID id.RoomID, stateType string) (map[string]any, string, error) {
	evt, err := reader.GetStateEvent(ctx, roomID, event.Type{Type: stateType, Class: event.StateEventType}, "")
	if errors.Is(err, mautrix.MNotFound) {
		return nil, "", nil
	} else if err != nil {
		return nil, "", fmt.Errorf("failed to read %s state: %w", stateType, err)
	}
	if evt == nil {
		return nil, "", nil
	}
	raw := evt.Content.Raw
	if raw == nil && evt.Content.Parsed != nil {
		encoded, err := json.Marshal(evt.Content.Parsed)
		if err != nil {
			return nil, "", err
		}
		if err = json.Unmarshal(encoded, &raw); err != nil {
			return nil, "", err
		}
	}
	return raw, string(evt.ID), nil
}

func implementsArbitraryRoomState(matrix bridgev2.MatrixConnector) bool {
	_, ok := matrix.(bridgev2.MatrixConnectorWithArbitraryRoomState)
	return ok
}

func applyRoomModelConfig(config *RoomConfig, raw map[string]any) {
	providerID, modelID := splitModelRef(firstString(raw, "model"))
	if providerID != "" {
		config.ProviderID = providerID
	}
	if modelID != "" {
		config.ModelID = modelID
	}
	if reasoning := firstString(raw, "reasoning"); reasoning != "" {
		config.ThinkingLevel = reasoning
	}
}

func splitModelRef(model string) (providerID string, modelID string) {
	providerID, modelID, _ = strings.Cut(strings.TrimSpace(model), "/")
	if modelID == "" {
		return "", providerID
	}
	return providerID, modelID
}

func firstString(raw map[string]any, keys ...string) string {
	for _, key := range keys {
		if value, ok := raw[key].(string); ok {
			if value = strings.TrimSpace(value); value != "" {
				return value
			}
		}
	}
	return ""
}

func stringSlice(value any) []string {
	items, ok := value.([]any)
	if !ok {
		return nil
	}
	out := []string{}
	for _, item := range items {
		if text, ok := item.(string); ok {
			text = strings.TrimSpace(text)
			if text != "" && !slices.Contains(out, text) {
				out = append(out, text)
			}
		}
	}
	return out
}
