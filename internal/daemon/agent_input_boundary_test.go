package daemon

import (
	"os"
	"strings"
	"testing"
)

// Messaging paths must use agentInputDeliveryService. Raw tmux input remains
// confined to the low-level adapter and lifecycle migration paths owned by DKW.
func TestAgentMessagingPathsDoNotBypassInputDeliveryService(t *testing.T) {
	for _, path := range []string{"orchestration_lifecycle.go", "mail_commands.go", "orchestration_review.go"} {
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		text := string(body)
		for _, forbidden := range []string{".PasteTextAndSubmit(", ".SendKeys("} {
			if strings.Contains(text, forbidden) {
				t.Errorf("%s contains raw automated input call %s", path, forbidden)
			}
		}
	}
	body, err := os.ReadFile("session_commands.go")
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	start := strings.Index(text, "func (d *Daemon) handleSessionMessage")
	if start < 0 {
		t.Fatal("session message handler start not found")
	}
	end := strings.Index(text[start:], "func (d *Daemon) handleSessionCapture")
	if end < 0 {
		t.Fatal("session message handler boundaries not found")
	}
	handler := text[start : start+end]
	if strings.Contains(handler, ".PasteTextAndSubmit(") || strings.Contains(handler, ".SendKeys(") {
		t.Fatal("session message handler bypasses agent input delivery service")
	}
}
