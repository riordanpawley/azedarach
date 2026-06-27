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
		"list [--project <project-id>] [--json] [--deps] [--query <text>|-q <text>]",
		"search [--project <project-id>] [--json] [--deps]",
		"[--parent <id> ...] [--parents a,b,c] [--depends-on <id> ...] [--depends-on-ids a,b,c]",
		"get [--project <project-id>] [--id <issue-id>] [--json] [--with-notes] [<issue-id>]",
		"get-many [--project <project-id>] --id <issue-id> [--id <issue-id> ...] [--ids a,b,c] [--json] [--with-notes]",
		"check [--project <project-id>] [--id <issue-id>] [--json] [<issue-id>]",
		"doctor [--project <project-id>] [--id <issue-id>] [--json] [<issue-id>]",
		"create [--project <project-id>] [--impl <implementation> ...] [--deferred]",
		"split [--project <project-id>] [--parent <issue-id>]",
		"close [--project <project-id>] [--id <issue-id>|-i <issue-id>] [--json] [--force-worktree] [<issue-id>]",
		"delete [--project <project-id>] [--id <issue-id>] [--json] [<issue-id>] --confirm [--cleanup|--stop-session] [--remove-worktree] [--force-worktree]",
		"To create a child under the active issue, run `az issue create \"Child task\"` from a session with AZEDARACH_ISSUE_ID set.",
		"To attach to a different parent/root, create the issue and then run `az issue dep add <child-id> <parent-id> --type parent-child`.",
		"Child/root membership comes from AZEDARACH_ISSUE_ID auto-parenting or `dep add --type parent-child`.",
		"Do not use `--impl <id>` when you mean \"parent this under <id>\".",
		"`--impl` assigns implementation/spec variant metadata only; it never attaches an issue to a graph/root.",
		"Argument ordering: place flags/options before positional arguments for deterministic parsing.",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("issue_help missing %q in output: %q", want, output)
		}
	}
}
