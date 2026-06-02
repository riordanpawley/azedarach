package text

import (
	"strings"
	"testing"
)

func TestRenderIssueHelpPrefersNamedFlagForms(t *testing.T) {
	output, err := Render("issue_help", nil)
	if err != nil {
		t.Fatalf("Render(issue_help) error = %v", err)
	}

	for _, want := range []string{
		"get [--project <project-id>] [--id <issue-id>] [--json] [--with-notes] [<issue-id>]",
		"get-many [--project <project-id>] --id <issue-id> [--id <issue-id> ...] [--ids a,b,c] [--json] [--with-notes]",
		"check [--project <project-id>] [--id <issue-id>] [--json] [<issue-id>]",
		"doctor [--project <project-id>] [--id <issue-id>] [--json] [<issue-id>]",
		"create [--project <project-id>] [--impl <implementation> ...] [--deferred]",
		"split [--project <project-id>] [--parent <issue-id>]",
		"close [--project <project-id>] [--id <issue-id>|-i <issue-id>] [--json] [--yes] [--force-worktree] [<issue-id>]",
		"delete [--project <project-id>] [--id <issue-id>] [--json] [<issue-id>] --confirm [--cleanup|--stop-session] [--remove-worktree] [--force-worktree]",
		"Argument ordering: place flags/options before positional arguments for deterministic parsing.",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("issue_help missing %q in output: %q", want, output)
		}
	}
}
