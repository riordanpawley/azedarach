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

func TestTicketSubcommandHelpIsCanonical(t *testing.T) {
	tests := []struct {
		path []string
		want string
	}{
		{path: []string{"ticket", "get", "--help"}, want: "<ticket-id>"},
		{path: []string{"ticket", "dep", "add", "--help"}, want: "--ticket-id"},
		{path: []string{"ticket", "image", "add", "--help"}, want: "--ticket-id"},
		{path: []string{"ticket", "document", "list", "--help"}, want: "--ticket-id"},
		{path: []string{"ticket", "fanout", "drift", "--help"}, want: "--ticket"},
	}
	for _, tt := range tests {
		t.Run(strings.Join(tt.path[1:len(tt.path)-1], "_"), func(t *testing.T) {
			output := captureHelpOutput(t, func() {
				if !maybePrintCommandHelp(tt.path) {
					t.Fatalf("ticket help path %v was not recognized", tt.path)
				}
			})
			if !strings.Contains(output, "Usage: az ticket ") || !strings.Contains(output, tt.want) {
				t.Fatalf("canonical ticket help missing %q: %q", tt.want, output)
			}
			for _, legacy := range []string{"<issue-id>", "--issue-id", "--issue "} {
				if strings.Contains(output, legacy) {
					t.Fatalf("canonical ticket help contains legacy term %q: %q", legacy, output)
				}
			}
		})
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
