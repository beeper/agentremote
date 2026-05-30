package connector

import (
	"context"

	ai "github.com/beeper/ai-bridge/pkg/ai"
	"github.com/beeper/ai-bridge/pkg/aiid"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/event"
)

var roomCaps = &event.RoomFeatures{
	Formatting: event.FormattingFeatureMap{
		event.FmtBold:          event.CapLevelPartialSupport,
		event.FmtItalic:        event.CapLevelPartialSupport,
		event.FmtStrikethrough: event.CapLevelPartialSupport,
		event.FmtInlineCode:    event.CapLevelPartialSupport,
		event.FmtCodeBlock:     event.CapLevelPartialSupport,
		event.FmtBlockquote:    event.CapLevelPartialSupport,
		event.FmtInlineLink:    event.CapLevelPartialSupport,
		event.FmtUnorderedList: event.CapLevelPartialSupport,
		event.FmtOrderedList:   event.CapLevelPartialSupport,
	},
	File: event.FileFeatureMap{
		event.MsgFile: textFileFeatures(),
	},
	State: event.StateFeatureMap{
		aiid.RoomToolsType:                      {Level: event.CapLevelFullySupported},
		aiid.RoomModelType:                      {Level: event.CapLevelFullySupported},
		aiid.RoomPromptType:                     {Level: event.CapLevelFullySupported},
		event.StateRoomName.Type:                {Level: event.CapLevelFullySupported},
		event.StateTopic.Type:                   {Level: event.CapLevelFullySupported},
		event.StateBeeperDisappearingTimer.Type: {Level: event.CapLevelFullySupported},
	},
	MaxTextLength: 20000,
	Reply:         event.CapLevelFullySupported,
	Edit:          event.CapLevelRejected,
	Delete:        event.CapLevelPartialSupport,
	DisappearingTimer: &event.DisappearingTimerCapability{
		Types: []event.DisappearingType{
			event.DisappearingTypeAfterSend,
			event.DisappearingTypeAfterRead,
		},
	},
	Reaction:            event.CapLevelUnsupported,
	ReadReceipts:        false,
	TypingNotifications: true,
}

func roomFeaturesForModel(model ai.Model, supportsAIState bool) *event.RoomFeatures {
	caps := roomCaps.Clone()
	if !supportsAIState {
		delete(caps.State, aiid.RoomToolsType)
		delete(caps.State, aiid.RoomModelType)
		delete(caps.State, aiid.RoomPromptType)
	}
	if isImageModel(model) {
		caps.File[event.MsgImage] = imageFileFeatures()
	}
	if isAudioModel(model) {
		audioFeatures := audioFileFeatures()
		caps.File[event.MsgAudio] = audioFeatures
		caps.File[event.CapMsgVoice] = audioFeatures.Clone()
	}
	return caps
}

func imageFileFeatures() *event.FileFeatures {
	return &event.FileFeatures{
		MimeTypes: map[string]event.CapabilitySupportLevel{
			"image/png":  event.CapLevelFullySupported,
			"image/jpeg": event.CapLevelFullySupported,
			"image/webp": event.CapLevelFullySupported,
			"image/gif":  event.CapLevelPartialSupport,
		},
		MaxSize:          20 * 1024 * 1024,
		Caption:          event.CapLevelFullySupported,
		MaxCaptionLength: 20000,
	}
}

func audioFileFeatures() *event.FileFeatures {
	return &event.FileFeatures{
		MimeTypes: map[string]event.CapabilitySupportLevel{
			"audio/wav":   event.CapLevelFullySupported,
			"audio/x-wav": event.CapLevelFullySupported,
			"audio/mpeg":  event.CapLevelFullySupported,
			"audio/mp3":   event.CapLevelFullySupported,
		},
		MaxSize:          25 * 1024 * 1024,
		Caption:          event.CapLevelFullySupported,
		MaxCaptionLength: 20000,
	}
}

func textFileFeatures() *event.FileFeatures {
	return &event.FileFeatures{
		MimeTypes: map[string]event.CapabilitySupportLevel{
			"text/*":                    event.CapLevelFullySupported,
			"application/json":          event.CapLevelFullySupported,
			"application/ld+json":       event.CapLevelFullySupported,
			"application/manifest+json": event.CapLevelFullySupported,
			"application/x-ndjson":      event.CapLevelFullySupported,
			"application/xml":           event.CapLevelFullySupported,
			"application/xhtml+xml":     event.CapLevelFullySupported,
			"application/yaml":          event.CapLevelFullySupported,
			"application/x-yaml":        event.CapLevelFullySupported,
			"application/toml":          event.CapLevelFullySupported,
			"application/javascript":    event.CapLevelFullySupported,
			"application/ecmascript":    event.CapLevelFullySupported,
			"application/sql":           event.CapLevelFullySupported,
		},
		MaxSize:          512 * 1024,
		Caption:          event.CapLevelFullySupported,
		MaxCaptionLength: 20000,
	}
}

func (c *Connector) GetCapabilities() *bridgev2.NetworkGeneralCapabilities {
	return &bridgev2.NetworkGeneralCapabilities{
		Provisioning: bridgev2.ProvisioningCapabilities{
			ResolveIdentifier: bridgev2.ResolveIdentifierCapabilities{
				CreateDM:       true,
				LookupUsername: true,
				ContactList:    true,
				Search:         true,
			},
			GroupCreation: map[string]bridgev2.GroupTypeCapabilities{
				"ai": {
					TypeDescription: "AI session",
					Name:            bridgev2.GroupFieldCapability{Allowed: true},
					Topic:           bridgev2.GroupFieldCapability{Allowed: true},
					Participants:    bridgev2.GroupFieldCapability{Allowed: false},
					Avatar:          bridgev2.GroupFieldCapability{Allowed: false},
					Username:        bridgev2.GroupFieldCapability{Allowed: false},
					Parent:          bridgev2.GroupFieldCapability{Allowed: false},
					Disappear:       bridgev2.GroupFieldCapability{Allowed: false},
				},
			},
		},
	}
}

func (cl *Client) GetCapabilities(ctx context.Context, portal *bridgev2.Portal) *event.RoomFeatures {
	supportsAIState := cl != nil && cl.Main != nil && cl.Main.aiRoomStateStore().canRead()
	if cl == nil || cl.Main == nil || cl.UserLogin == nil || portal == nil || portal.Portal == nil || portal.MXID == "" {
		return roomFeaturesForModel(ai.Model{}, supportsAIState)
	}
	roomConfig, _, err := cl.Main.ReadRoomConfig(ctx, portal.MXID)
	if err != nil {
		return roomFeaturesForModel(ai.Model{}, supportsAIState)
	}
	provider, modelID, err := cl.resolveProvider(ctx, roomConfig)
	if err != nil {
		return roomFeaturesForModel(ai.Model{}, supportsAIState)
	}
	return roomFeaturesForModel(cl.Main.ModelForProvider(provider, modelID), supportsAIState)
}
