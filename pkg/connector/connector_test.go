package connector

import (
	"testing"

	"github.com/beeper/ai-bridge/pkg/aiid"
)

func TestConnectorGetNameMatchesDesktopMetadata(t *testing.T) {
	name := (&Connector{}).GetName()

	if name.DisplayName != "AI Chats" {
		t.Fatalf("unexpected display name %q", name.DisplayName)
	}
	if name.NetworkURL != "https://www.beeper.com/ai" {
		t.Fatalf("unexpected network URL %q", name.NetworkURL)
	}
	if string(name.NetworkIcon) != "mxc://beeper.com/51a668657dd9e0132cc823ad9402c6c2d0fc3321" {
		t.Fatalf("unexpected network icon %q", name.NetworkIcon)
	}
	if name.NetworkID != aiid.NetworkID || name.BeeperBridgeType != aiid.BeeperBridgeType {
		t.Fatalf("unexpected bridge identity: %#v", name)
	}
	if name.DefaultPort != 29344 {
		t.Fatalf("unexpected default port %d", name.DefaultPort)
	}
	if name.DefaultCommandPrefix != "!ai" {
		t.Fatalf("unexpected default command prefix %q", name.DefaultCommandPrefix)
	}
}
