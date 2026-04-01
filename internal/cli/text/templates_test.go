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
		"get [--project <project-id>] [--id <issue-id>] [--json] [<issue-id>]",
		"check [--project <project-id>] [--id <issue-id>] [--json] [<issue-id>]",
		"doctor [--project <project-id>] [--id <issue-id>] [<issue-id>]",
		"create [--project <project-id>] [--impl <implementation> ...] [--deferred]",
		"Argument ordering: place flags/options before positional arguments for deterministic parsing.",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("issue_help missing %q in output: %q", want, output)
		}
	}
}
