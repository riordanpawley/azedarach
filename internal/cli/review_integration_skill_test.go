package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReviewIntegrationSkillRoutesFindingsToLiveSessions(t *testing.T) {
	path := filepath.Join("..", "..", ".codex", "skills", "review-integration", "SKILL.md")
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read review-integration skill: %v", err)
	}
	content := string(body)

	for _, want := range []string{
		"name: review-integration",
		"Review and integrate Azedarach issues that are in_review",
		"az issue list --status in_review",
		"az session status <issue-id>",
		"do not patch non-trivial actionable findings",
		"az issue update <issue-id> --status in_progress",
		"az orchestrate message --root <root-issue> --issue <worker-issue> --type review-finding",
		"\"type\":\"review-finding\"",
		"\"suggested_fix\"",
		"\"validation\"",
		"az issue record <issue-id> --type review.recorded",
		"az issue close --id <issue-id>",
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("review-integration skill missing %q", want)
		}
	}
}
