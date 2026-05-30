package aibridgev2

import (
	"context"
	"time"

	aistream "github.com/beeper/ai-bridge/pkg/ai-stream"
	aimatrix "github.com/beeper/ai-bridge/pkg/ai-stream/matrix"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/bridgev2/simplevent"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

func eventMeta(eventType bridgev2.RemoteEventType, portalKey networkid.PortalKey, sender networkid.UserID, timestamp time.Time) simplevent.EventMeta {
	return simplevent.EventMeta{
		Type:        eventType,
		PortalKey:   portalKey,
		Sender:      bridgev2.EventSender{Sender: sender},
		Timestamp:   timestamp,
		StreamOrder: timestamp.UnixNano(),
	}
}

func messagePart(content *event.MessageEventContent, extra map[string]any, dbMetadata map[string]any) *bridgev2.ConvertedMessagePart {
	return &bridgev2.ConvertedMessagePart{
		ID:         networkid.PartID("0"),
		Type:       event.EventMessage,
		Content:    content,
		Extra:      extra,
		DBMetadata: dbMetadata,
	}
}

func Anchor(portalKey networkid.PortalKey, sender networkid.UserID, run aistream.Run, timestamp time.Time) *simplevent.PreConvertedMessage {
	content, extra := aimatrix.AnchorContent(run)
	return &simplevent.PreConvertedMessage{
		EventMeta: eventMeta(bridgev2.RemoteEventMessage, portalKey, sender, timestamp),
		Data:      &bridgev2.ConvertedMessage{Parts: []*bridgev2.ConvertedMessagePart{messagePart(content, extra, nil)}},
		ID:        networkid.MessageID(run.MessageID),
	}
}

func Carrier(portalKey networkid.PortalKey, sender networkid.UserID, run aistream.Run, carrier aistream.Carrier, targetEventID id.EventID, index int, timestamp time.Time) *simplevent.PreConvertedMessage {
	content, extra := aimatrix.CarrierContent(run, carrier, targetEventID)
	return &simplevent.PreConvertedMessage{
		EventMeta: eventMeta(bridgev2.RemoteEventMessage, portalKey, sender, timestamp),
		Data:      &bridgev2.ConvertedMessage{Parts: []*bridgev2.ConvertedMessagePart{messagePart(content, extra, nil)}},
		ID:        networkid.MessageID(aistream.StreamTxnID(run.RunID, index)),
	}
}

func FinalSegments(portalKey networkid.PortalKey, sender networkid.UserID, run aistream.Run, targetEventID id.EventID, timestamp time.Time) []*simplevent.PreConvertedMessage {
	return FinalSegmentMessages(portalKey, sender, run, aimatrix.FinalSegments(run), targetEventID, timestamp)
}

func FinalSegmentMessages(portalKey networkid.PortalKey, sender networkid.UserID, run aistream.Run, segments []aistream.FinalSegment, targetEventID id.EventID, timestamp time.Time) []*simplevent.PreConvertedMessage {
	out := make([]*simplevent.PreConvertedMessage, 0, len(segments))
	for i, segment := range segments {
		content, extra := aimatrix.FinalSegmentContent(run, segment, targetEventID)
		segmentTimestamp := timestamp.Add(time.Duration(i) * time.Nanosecond)
		out = append(out, &simplevent.PreConvertedMessage{
			EventMeta: eventMeta(bridgev2.RemoteEventMessage, portalKey, sender, segmentTimestamp),
			Data:      &bridgev2.ConvertedMessage{Parts: []*bridgev2.ConvertedMessagePart{messagePart(content, extra, nil)}},
			ID:        networkid.MessageID(aistream.FinalSegmentTxnID(run.RunID, segment.Metadata.Index)),
		})
	}
	return out
}

func ApprovalPrompt(portalKey networkid.PortalKey, sender networkid.UserID, ctx aistream.ApprovalContext, timestamp time.Time) *simplevent.PreConvertedMessage {
	content, extra := aimatrix.ApprovalContent(ctx, aistream.DefaultApprovalChoices())
	return &simplevent.PreConvertedMessage{
		EventMeta: eventMeta(bridgev2.RemoteEventMessage, portalKey, sender, timestamp),
		Data: &bridgev2.ConvertedMessage{Parts: []*bridgev2.ConvertedMessagePart{
			messagePart(content, extra, map[string]any{"com.beeper.ai.approval": ctx}),
		}},
		ID: networkid.MessageID(ctx.ID),
	}
}

func FinalMetadataEdit(portalKey networkid.PortalKey, sender networkid.UserID, messageID networkid.MessageID, run aistream.Run, timestamp time.Time) *simplevent.Message[*aistream.Run] {
	finalContent, finalExtra := aimatrix.FinalContent(run)
	return FinalMetadataEditWithContent(portalKey, sender, messageID, run, finalContent, finalExtra, timestamp)
}

func FinalMetadataEditWithContent(portalKey networkid.PortalKey, sender networkid.UserID, messageID networkid.MessageID, run aistream.Run, finalContent *event.MessageEventContent, finalExtra map[string]any, timestamp time.Time) *simplevent.Message[*aistream.Run] {
	return &simplevent.Message[*aistream.Run]{
		EventMeta:     eventMeta(bridgev2.RemoteEventEdit, portalKey, sender, timestamp),
		Data:          &run,
		ID:            messageID,
		TargetMessage: messageID,
		ConvertEditFunc: func(ctx context.Context, portal *bridgev2.Portal, intent bridgev2.MatrixAPI, existing []*database.Message, data *aistream.Run) (*bridgev2.ConvertedEdit, error) {
			if len(existing) == 0 {
				return nil, nil
			}
			return &bridgev2.ConvertedEdit{
				ModifiedParts: []*bridgev2.ConvertedEditPart{{
					Part:    existing[0],
					Type:    event.EventMessage,
					Content: finalContent,
					Extra:   finalExtra,
					TopLevelExtra: map[string]any{
						"com.beeper.dont_render_edited": true,
					},
				}},
			}, nil
		},
	}
}
