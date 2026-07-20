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
		"fresh ephemeral review subagent",
		"Keep delegates read-only",
		"verdict` (`clean` or `findings`)",
		"inspect the complete assigned revision and return one consolidated actionable finding batch before yielding",
		"Confirming the requested repair or finding the first defect is not a stop condition",
		"covered matrix cells",
		"deliberately skipped cells with reasons",
		"Fall back to the complete affected invariant",
		"whenever the local delta cannot establish completeness",
		"remains solely responsible for durable review-return, review-accept, integration, and close operations",
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
