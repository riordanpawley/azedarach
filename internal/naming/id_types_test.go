package naming

import "testing"

func TestParseIssueID(t *testing.T) {
	id, err := ParseIssueID("bra")
	if err != nil {
		t.Fatalf("ParseIssueID() error = %v", err)
	}
	if got, want := id.String(), "bra"; got != want {
		t.Fatalf("ParseIssueID() = %q, want %q", got, want)
	}
}

func TestParseIssueIDRejectsInvalid(t *testing.T) {
	if _, err := ParseIssueID("bra/session"); err == nil {
		t.Fatal("ParseIssueID() expected error for slash-separated value")
	}
}

func TestParseSessionID(t *testing.T) {
	id, err := ParseSessionID("az-bra", "azedarach")
	if err != nil {
		t.Fatalf("ParseSessionID() error = %v", err)
	}
	if got, want := id.String(), "az-bra"; got != want {
		t.Fatalf("ParseSessionID() = %q, want %q", got, want)
	}
}

func TestCanonicalSessionIDForIssue(t *testing.T) {
	issueID, err := ParseIssueID("bra")
	if err != nil {
		t.Fatalf("ParseIssueID() error = %v", err)
	}
	sessionID := CanonicalSessionIDForIssue("azedarach", issueID)
	if got, want := sessionID.String(), "az-bra"; got != want {
		t.Fatalf("CanonicalSessionIDForIssue() = %q, want %q", got, want)
	}
}

