package aiid

import (
	"encoding/base64"
	"encoding/json"
	"strings"

	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/id"
)

const (
	NetworkID        = "ai"
	BeeperBridgeType = "ai"
	DefaultLoginName = "beeper"
	DefaultProvider  = "beeper"
	RoomToolsType    = "com.beeper.ai.tools"
	RoomModelType    = "com.beeper.ai.model"
	RoomPromptType   = "com.beeper.ai.additional_prompt"
	StreamType       = "com.beeper.stream"
)

func DefaultLoginID(mxid id.UserID) networkid.UserLoginID {
	return networkid.UserLoginID("default:" + encode(string(mxid)))
}

func ProviderLoginID(parent networkid.UserLoginID, providerID string) networkid.UserLoginID {
	return networkid.UserLoginID("provider:" + encode(string(parent)) + ":" + sanitizeID(providerID))
}

func ParseProviderLoginID(loginID networkid.UserLoginID) (parent networkid.UserLoginID, providerID string, ok bool) {
	rest, ok := strings.CutPrefix(string(loginID), "provider:")
	if !ok {
		return "", "", false
	}
	encodedParent, providerID, ok := strings.Cut(rest, ":")
	if !ok || encodedParent == "" || providerID == "" {
		return "", "", false
	}
	decodedParent, err := decode(encodedParent)
	if err != nil || decodedParent == "" {
		return "", "", false
	}
	return networkid.UserLoginID(decodedParent), providerID, true
}

func PortalID(roomID id.RoomID) networkid.PortalID {
	return networkid.PortalID("mxroom:" + encode(string(roomID)))
}

func PortalKey(roomID id.RoomID, loginID networkid.UserLoginID) networkid.PortalKey {
	return networkid.PortalKey{ID: PortalID(roomID), Receiver: loginID}
}

func AssistantUserID() networkid.UserID {
	return networkid.UserID("assistant:ai")
}

func ModelContactID(providerID string, modelID string) networkid.UserID {
	return networkid.UserID("model:" + encode(providerID) + ":" + encode(modelID))
}

func ParseModelContactID(userID networkid.UserID) (providerID string, modelID string, ok bool) {
	rest, ok := strings.CutPrefix(string(userID), "model:")
	if !ok {
		return "", "", false
	}
	providerID, modelID, ok = strings.Cut(rest, ":")
	if !ok || providerID == "" || modelID == "" {
		return "", "", false
	}
	decodedProvider, err := decode(providerID)
	if err != nil {
		return "", "", false
	}
	decodedModel, err := decode(modelID)
	if err != nil {
		return "", "", false
	}
	return decodedProvider, decodedModel, true
}

func UserMessageID(entryID string) networkid.MessageID {
	return networkid.MessageID("user:" + entryID)
}

func AssistantMessageID(entryID string) networkid.MessageID {
	return networkid.MessageID("assistant:" + entryID)
}

func PartID(name string) networkid.PartID {
	return networkid.PartID(sanitizeID(name))
}

func MediaID(parts ...string) networkid.MediaID {
	cleaned := make([]string, 0, len(parts))
	for _, part := range parts {
		cleaned = append(cleaned, sanitizeID(part))
	}
	return networkid.MediaID(strings.Join(cleaned, ":"))
}

func MediaIDFor(metadata MediaMetadata) (networkid.MediaID, error) {
	raw, err := json.Marshal(metadata)
	if err != nil {
		return networkid.MediaID(""), err
	}
	return networkid.MediaID("ai:" + base64.RawURLEncoding.EncodeToString(raw)), nil
}

func ParseMediaID(mediaID networkid.MediaID) (MediaMetadata, error) {
	encoded, ok := strings.CutPrefix(string(mediaID), "ai:")
	if !ok {
		return MediaMetadata{}, json.Unmarshal([]byte(mediaID), &MediaMetadata{})
	}
	raw, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return MediaMetadata{}, err
	}
	var metadata MediaMetadata
	err = json.Unmarshal(raw, &metadata)
	return metadata, err
}

func encode(value string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(value))
}

func decode(value string) (string, error) {
	raw, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

func sanitizeID(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "unknown"
	}
	replacer := strings.NewReplacer(" ", "_", "/", "_", "\\", "_", ":", "_", "\n", "_", "\r", "_", "\t", "_")
	return replacer.Replace(value)
}
