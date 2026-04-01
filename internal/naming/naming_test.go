package naming

import "testing"

func TestCanonicalSessionID(t *testing.T) {
	got := CanonicalSessionID("/Users/me/prog/Chefy", "CHE-3002")
	if got != "ch-CHE-3002" {
		t.Fatalf("CanonicalSessionID() = %q, want %q", got, "ch-CHE-3002")
	}
}

func TestCanonicalSessionIDRouteProjectIDUsesReadablePrefix(t *testing.T) {
	got := CanonicalSessionID("b0f4d3c2a1e9-azedarach-bkf", "BKF-123")
	if got != "az-BKF-123" {
		t.Fatalf("CanonicalSessionID() = %q, want %q", got, "az-BKF-123")
	}
}

func TestParseIssueIDFromSessionName(t *testing.T) {
	projectPath := "/Users/me/prog/Chefy"
	issueID, ok := ParseIssueIDFromSessionName("ch-CHE-3002", projectPath)
	if !ok {
		t.Fatal("expected parse to succeed")
	}
	if issueID != "CHE-3002" {
		t.Fatalf("issueID = %q, want %q", issueID, "CHE-3002")
	}

	if _, ok := ParseIssueIDFromSessionName("az-CHE-3002", projectPath); ok {
		t.Fatal("expected mismatched project prefix parse to fail")
	}
}

func TestParseIssueIDFromSessionNameRouteProjectID(t *testing.T) {
	projectID := "b0f4d3c2a1e9-azedarach-bkf"
	issueID, ok := ParseIssueIDFromSessionName("az-BKF-777", projectID)
	if !ok {
		t.Fatal("expected parse to succeed for route project id prefix")
	}
	if issueID != "BKF-777" {
		t.Fatalf("issueID = %q, want %q", issueID, "BKF-777")
	}
}

func TestComposeIssueBranchName(t *testing.T) {
	got := ComposeIssueBranchName("Riordan Pawley", "CHE-3002", "Migrate prep lists to db", 24)
	want := "riordanpawley/che-3002/migrate-prep-lists-to-db"
	if got != want {
		t.Fatalf("ComposeIssueBranchName() = %q, want %q", got, want)
	}
}

func TestExtractIssueIDFromBranchName(t *testing.T) {
	issueID, ok := ExtractIssueIDFromBranchName("riordanpawley/che-3002/migrate-prep-lists-to-db")
	if !ok || issueID != "che-3002" {
		t.Fatalf("ExtractIssueIDFromBranchName(new-format) = %q, %v", issueID, ok)
	}

	issueID, ok = ExtractIssueIDFromBranchName("az/issue-123")
	if !ok || issueID != "issue-123" {
		t.Fatalf("ExtractIssueIDFromBranchName(legacy) = %q, %v", issueID, ok)
	}
}
