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

func TestParseOperationID(t *testing.T) {
	id, err := ParseOperationID("20260405163240.161281000")
	if err != nil {
		t.Fatalf("ParseOperationID() error = %v", err)
	}
	if got, want := id.String(), "20260405163240.161281000"; got != want {
		t.Fatalf("ParseOperationID() = %q, want %q", got, want)
	}
}

func TestParseOperationIDRejectsInvalid(t *testing.T) {
	if _, err := ParseOperationID("op/1"); err == nil {
		t.Fatal("ParseOperationID() expected error for slash-separated value")
	}
}

func TestParseRequestID(t *testing.T) {
	id, err := ParseRequestID("session.start-123")
	if err != nil {
		t.Fatalf("ParseRequestID() error = %v", err)
	}
	if got, want := id.String(), "session.start-123"; got != want {
		t.Fatalf("ParseRequestID() = %q, want %q", got, want)
	}
}

func TestParseRequestIDRejectsInvalid(t *testing.T) {
	if _, err := ParseRequestID("req/1"); err == nil {
		t.Fatal("ParseRequestID() expected error for slash-separated value")
	}
}

func TestParseProjectID(t *testing.T) {
	id, err := ParseProjectID("proj-1")
	if err != nil {
		t.Fatalf("ParseProjectID() error = %v", err)
	}
	if got, want := id.String(), "proj-1"; got != want {
		t.Fatalf("ParseProjectID() = %q, want %q", got, want)
	}
}

func TestParseRequirementID(t *testing.T) {
	id, err := ParseRequirementID("bfs-req-1")
	if err != nil {
		t.Fatalf("ParseRequirementID() error = %v", err)
	}
	if got, want := id.String(), "bfs-req-1"; got != want {
		t.Fatalf("ParseRequirementID() = %q, want %q", got, want)
	}
}

func TestParseSpecLinkIDAllowsColon(t *testing.T) {
	id, err := ParseSpecLinkID("brl:fr4216")
	if err != nil {
		t.Fatalf("ParseSpecLinkID() error = %v", err)
	}
	if got, want := id.String(), "brl:fr4216"; got != want {
		t.Fatalf("ParseSpecLinkID() = %q, want %q", got, want)
	}
}

func TestParseSpecLinkIDRejectsInvalid(t *testing.T) {
	if _, err := ParseSpecLinkID("brl/fr4216"); err == nil {
		t.Fatal("ParseSpecLinkID() expected error for slash-separated value")
	}
}
