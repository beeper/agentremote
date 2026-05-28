package aibridgev2

import (
	"strings"
	"testing"
	"time"

	"github.com/beeper/ai-bridge/pkg/ag-ui"
	aistream "github.com/beeper/ai-bridge/pkg/ai-stream"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

func TestBridgeV2AIEvents(t *testing.T) {
	now := time.Unix(10, 0)
	run := aistream.NewRun("run-1", "thread-1", "", "ai", "AI", now)
	run.Preview = aistream.Preview{Text: "visible preview"}

	anchor := Anchor(
		networkid.PortalKey{ID: "portal-1"},
		networkid.UserID("ai"),
		*run,
		now,
	)
	if anchor.Type != bridgev2.RemoteEventMessage {
		t.Fatalf("anchor type = %v", anchor.Type)
	}
	part := anchor.Data.Parts[0]
	if part.Type != event.EventMessage || part.Content.Body != "visible preview" {
		t.Fatalf("unexpected anchor part: %#v", part)
	}
	ai, ok := part.Extra[aistream.BeeperAIKey].(aistream.BeeperAI)
	if !ok || ai.Kind != aistream.AIKindAnchor || ai.Protocol != "ag-ui" {
		t.Fatalf("anchor missing AI metadata: %#v", part.Extra)
	}

	carrier := Carrier(
		networkid.PortalKey{ID: "portal-1"},
		networkid.UserID("ai"),
		*run,
		aistream.Carrier{Envelopes: []aistream.Envelope{{
			Seq: 1,
			Event: agui.NewEvent(map[string]any{
				"type":      "TEXT_MESSAGE_CONTENT",
				"messageId": run.MessageID,
				"delta":     "hello",
			}),
		}}},
		id.EventID("$anchor"),
		1,
		now,
	)
	carrierPart := carrier.Data.Parts[0]
	if carrierPart.Content.MsgType != event.MsgText || carrierPart.Content.Body != "" {
		t.Fatalf("carrier should be hidden text carrier: %#v", carrierPart.Content)
	}
	ai, ok = carrierPart.Extra[aistream.BeeperAIKey].(aistream.BeeperAI)
	if !ok || len(ai.Events) == 0 {
		t.Fatalf("carrier missing deltas: %#v", carrierPart.Extra)
	}

	approval := ApprovalPrompt(networkid.PortalKey{ID: "portal-1"}, networkid.UserID("ai"), aistream.ApprovalContext{
		ID:          "approval-1",
		ThreadID:    run.ThreadID,
		RunID:       run.RunID,
		MessageID:   run.MessageID,
		ToolCallID:  "tool-1",
		ToolName:    "dummy_echo",
		TargetEvent: "$anchor",
	}, now)
	approvalPart := approval.Data.Parts[0]
	approvalMetadata, ok := approvalPart.DBMetadata.(map[string]any)
	if !ok || approvalMetadata["com.beeper.ai.approval"] == nil {
		t.Fatalf("approval missing DB metadata: %#v", approvalPart.DBMetadata)
	}
}

func TestFinalMetadataEditUsesCompactAnchorContent(t *testing.T) {
	now := time.Unix(10, 0)
	run := aistream.NewRun("run-1", "thread-1", "", "ai", "AI", now)
	run.Preview = aistream.Preview{Text: strings.Repeat("a", aistream.PreviewBudgetBytes+1), Truncated: true}

	edit := FinalMetadataEdit(
		networkid.PortalKey{ID: "portal-1"},
		networkid.UserID("ai"),
		networkid.MessageID(run.MessageID),
		*run,
		now,
	)
	if edit.Type != bridgev2.RemoteEventEdit {
		t.Fatalf("final metadata event type = %v", edit.Type)
	}
	if edit.TargetMessage != networkid.MessageID(run.MessageID) {
		t.Fatalf("final metadata target = %q", edit.TargetMessage)
	}
	if edit.Data.Text() != "" {
		t.Fatalf("final metadata edit must not expose full accumulated text")
	}
	converted, err := edit.ConvertEditFunc(nil, nil, nil, []*database.Message{{}}, edit.Data)
	if err != nil {
		t.Fatal(err)
	}
	ai, ok := converted.ModifiedParts[0].Extra[aistream.BeeperAIKey].(aistream.BeeperAI)
	if !ok || ai.Kind != aistream.AIKindFinal {
		t.Fatalf("final metadata edit missing final AI payload: %#v", converted.ModifiedParts[0].Extra)
	}
}

func TestFinalSegmentsUseMonotonicStreamOrder(t *testing.T) {
	now := time.Unix(10, 0)
	run := aistream.NewRun("run-1", "thread-1", "", "ai", "AI", now)
	writer := aistream.NewWriter(run, func() time.Time { return now })
	writer.Text(strings.Repeat("a", 70*1024))
	writer.Finish(agui.FinishReasonStop)

	segments := FinalSegments(
		networkid.PortalKey{ID: "portal-1"},
		networkid.UserID("ai"),
		*run,
		id.EventID("$anchor"),
		now,
	)
	if len(segments) < 2 {
		t.Fatalf("expected multiple final segments, got %d", len(segments))
	}
	for i := 1; i < len(segments); i++ {
		if segments[i].StreamOrder <= segments[i-1].StreamOrder {
			t.Fatalf("segment stream order did not increase: %d <= %d", segments[i].StreamOrder, segments[i-1].StreamOrder)
		}
	}
}
