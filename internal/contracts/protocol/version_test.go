package protocol

import "testing"

func TestDaemonExecutablePreflightAcceptsOnlyDeclaredRange(t *testing.T) {
	report := CurrentDaemonExecutablePreflight("test")
	if !report.Accepts(CurrentVersion) {
		t.Fatal("current daemon preflight rejected current protocol")
	}
	if (DaemonExecutablePreflight{MinProtocolVersion: CurrentVersion + 1, MaxProtocolVersion: CurrentVersion + 1}).Accepts(CurrentVersion) {
		t.Fatal("future-only daemon preflight accepted current protocol")
	}
	if (DaemonExecutablePreflight{MinProtocolVersion: CurrentVersion, MaxProtocolVersion: CurrentVersion - 1}).Accepts(CurrentVersion) {
		t.Fatal("invalid daemon preflight range accepted current protocol")
	}
}
