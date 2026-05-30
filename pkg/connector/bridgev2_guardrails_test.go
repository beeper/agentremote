package connector

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBridgeV2EscapeHatchGuardrails(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	portalInternalUses := 0
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		raw, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		source := string(raw)
		for _, forbidden := range []string{
			"Bridge.DB.User.GetDB().Query",
			"bridgev2.ErrNoPortal =",
			"Bridge.Bot.SendMessage",
			"Bridge.DB.Message.Update",
			"Bridge.DB.Message.Get",
			"GetPartByID",
		} {
			if strings.Contains(source, forbidden) {
				t.Fatalf("%s contains unscoped bridgev2 escape hatch %q", file, forbidden)
			}
		}
		portalInternalUses += strings.Count(source, "portal.Internal()")
	}
	if portalInternalUses != 1 {
		t.Fatalf("expected exactly one isolated portal.Internal() use, got %d", portalInternalUses)
	}
}
