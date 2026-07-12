package main

import (
	"io"
	"os"
	"strings"
	"testing"
)

func TestTicketHelpIsCanonical(t *testing.T) {
	output := captureHelpOutput(t, func() {
		if !maybePrintCommandHelp([]string{"ticket", "--help"}) {
			t.Fatal("ticket help was not recognized")
		}
	})
	if !strings.Contains(output, "Usage: az ticket ") {
		t.Fatalf("ticket help did not advertise canonical command: %q", output)
	}
}

func TestLegacyIssueHelpDocumentsDeprecation(t *testing.T) {
	output := captureHelpOutput(t, func() {
		if !maybePrintCommandHelp([]string{"issue", "--help"}) {
			t.Fatal("legacy issue help was not recognized")
		}
	})
	if !strings.Contains(output, "`az issue` is deprecated; use `az ticket`") {
		t.Fatalf("legacy help omitted deprecation guidance: %q", output)
	}
}

func TestTicketSubcommandHelpUsesExistingCommandSurface(t *testing.T) {
	output := captureHelpOutput(t, func() {
		if !maybePrintCommandHelp([]string{"ticket", "get", "--help"}) {
			t.Fatal("ticket get help was not recognized")
		}
	})
	if !strings.Contains(output, " get ") && !strings.Contains(output, "issue get") {
		t.Fatalf("ticket get help did not render command usage: %q", output)
	}
}

func TestNormalizeTicketEnvironmentSupportsBothNames(t *testing.T) {
	t.Run("canonical populates legacy", func(t *testing.T) {
		t.Setenv("AZEDARACH_TICKET_ID", "az-ticket")
		t.Setenv("AZEDARACH_ISSUE_ID", "")
		normalizeTicketEnvironment()
		if got := os.Getenv("AZEDARACH_ISSUE_ID"); got != "az-ticket" {
			t.Fatalf("legacy environment = %q, want az-ticket", got)
		}
	})
	t.Run("legacy populates canonical", func(t *testing.T) {
		t.Setenv("AZEDARACH_TICKET_ID", "")
		t.Setenv("AZEDARACH_ISSUE_ID", "az-legacy")
		normalizeTicketEnvironment()
		if got := os.Getenv("AZEDARACH_TICKET_ID"); got != "az-legacy" {
			t.Fatalf("canonical environment = %q, want az-legacy", got)
		}
	})
	t.Run("legacy wins conflicts", func(t *testing.T) {
		t.Setenv("AZEDARACH_TICKET_ID", "az-new")
		t.Setenv("AZEDARACH_ISSUE_ID", "az-existing")
		normalizeTicketEnvironment()
		if got := os.Getenv("AZEDARACH_TICKET_ID"); got != "az-existing" {
			t.Fatalf("canonical environment = %q, want az-existing", got)
		}
	})
}

func captureHelpOutput(t *testing.T, fn func()) string {
	t.Helper()
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	original := os.Stdout
	os.Stdout = write
	defer func() { os.Stdout = original }()
	fn()
	if err := write.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(read)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
