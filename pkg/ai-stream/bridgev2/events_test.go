package aibridgev2

import (
	"strings"
	"testing"
	"time"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	agui "github.com/beeper/ai-bridge/pkg/ag-ui"
	aistream "github.com/beeper/ai-bridge/pkg/ai-stream"
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
	part := converted.ModifiedParts[0]
	ai, ok := part.Extra[aistream.BeeperAIKey].(aistream.BeeperAI)
	if !ok || ai.Kind != aistream.AIKindFinal {
		t.Fatalf("final metadata edit missing final AI payload: %#v", part.Extra)
	}
	if stream, ok := part.Extra["com.beeper.stream"]; !ok || stream != nil {
		t.Fatalf("final metadata edit must clear stream in m.new_content: %#v", part.Extra)
	}
	if stream, ok := part.TopLevelExtra["com.beeper.stream"]; !ok || stream != nil {
		t.Fatalf("final metadata edit must clear stream at top level: %#v", part.TopLevelExtra)
	}
	if part.TopLevelExtra["com.beeper.dont_render_edited"] != true {
		t.Fatalf("final metadata edit missing no-render-edited marker: %#v", part.TopLevelExtra)
	}
}

func TestCarriersUseMonotonicStreamOrder(t *testing.T) {
	now := time.Unix(10, 0)
	run := aistream.NewRun("run-1", "thread-1", "", "ai", "AI", now)
	carrier := aistream.Carrier{Envelopes: []aistream.Envelope{{
		Seq: 1,
		Event: agui.NewEvent(map[string]any{
			"type":      "TEXT_MESSAGE_CONTENT",
			"messageId": run.MessageID,
			"delta":     "hello",
		}),
	}}}

	first := Carrier(
		networkid.PortalKey{ID: "portal-1"},
		networkid.UserID("ai"),
		*run,
		carrier,
		id.EventID("$anchor"),
		1,
		now,
	)
	second := Carrier(
		networkid.PortalKey{ID: "portal-1"},
		networkid.UserID("ai"),
		*run,
		carrier,
		id.EventID("$anchor"),
		2,
		now,
	)
	if second.StreamOrder <= first.StreamOrder {
		t.Fatalf("carrier stream order did not increase: %d <= %d", second.StreamOrder, first.StreamOrder)
	}
}
