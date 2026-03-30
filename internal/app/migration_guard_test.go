package app

import (
	"os"
	"strings"
	"testing"
)

func TestMigrationGuard_NoLocalDaemonTransportShim(t *testing.T) {
	if _, err := os.Stat("daemon_transport.go"); !os.IsNotExist(err) {
		t.Fatalf("daemon_transport.go should not exist on active path; err=%v", err)
	}

	content, err := os.ReadFile("model.go")
	if err != nil {
		t.Fatalf("read model.go: %v", err)
	}
	if strings.Contains(string(content), "newLocalDaemonTransport(") {
		t.Fatal("model.go references local daemon transport shim")
	}
}
