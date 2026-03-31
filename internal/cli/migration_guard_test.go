package cli

import (
	"os"
	"strings"
	"testing"
)

func TestMigrationGuard_NoLocalDaemonTransportShim(t *testing.T) {
	if _, err := os.Stat("daemon_transport.go"); !os.IsNotExist(err) {
		t.Fatalf("daemon_transport.go should not exist on active path; err=%v", err)
	}

	content, err := os.ReadFile("commands.go")
	if err != nil {
		t.Fatalf("read commands.go: %v", err)
	}
	if strings.Contains(string(content), "newLocalDaemonTransport(") {
		t.Fatal("commands.go references local daemon transport shim")
	}
}
