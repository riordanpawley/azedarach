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
		"get [--id <issue-id>] [--json] [--deps] [<issue-id>]",
		"check [--id <issue-id>] [--json] [--deps] [<issue-id>]",
		"doctor [--id <issue-id>] [<issue-id>]",
		"Argument ordering: place flags/options before positional arguments for deterministic parsing.",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("issue_help missing %q in output: %q", want, output)
		}
	}
}
