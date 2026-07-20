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
		"(--query <text>|-q <text>|<query>)  Search title, description, notes, design, acceptance, labels, and implementations",
		"[--parent <id> ...] [--parents a,b,c] [--depends-on <id> ...] [--depends-on-ids a,b,c]",
		"get [--project <project-id>] [--id <ticket-id>] [--json] [--with-notes] [<ticket-id>]",
		"get-many [--project <project-id>] --id <ticket-id> [--id <ticket-id> ...] [--ids a,b,c] [--json] [--with-notes]",
		"check [--project <project-id>] [--id <ticket-id>] [--json] [<ticket-id>]",
		"doctor [--project <project-id>] [--id <ticket-id>] [--json] [<ticket-id>]",
		"create [--project <project-id>] [--parent <ticket-id>] [--impl <implementation> ...] [--deferred]",
		"split --intent-key <unique-invocation-key> [--project <project-id>] [--parent <ticket-id>]",
		"close [--project <project-id>] [--id <ticket-id>|-i <ticket-id>] [--json] [--force-worktree] [<ticket-id>]",
		"delete [--project <project-id>] [--id <ticket-id>] [--json] [<ticket-id>] --confirm [--cleanup|--stop-session] [--remove-worktree] [--force-worktree]",
		"unarchive [--project <project-id>] [--id <ticket-id>] [--json] [--with-parents] [--cascade-children] [<ticket-id>]  Restore archived tickets to active reads",
		"Agent progress, validation, review facts, and worker closeout belong in mail/observation evidence, not ticket notes.",
		"To create a child under the active ticket, run `az ticket create \"Child task\"` from a session with AZEDARACH_ISSUE_ID set.",
		"To attach to a different parent/root at creation time, pass `--parent <ticket-id>`.",
		"Child/root membership comes from AZEDARACH_ISSUE_ID auto-parenting, `--parent`, or `dep add --type parent-child`.",
		"Do not use `--impl <id>` when you mean \"parent this under <id>\".",
		"`--impl` assigns implementation/spec variant metadata only; it never attaches a ticket to a graph/root.",
		"Argument ordering: place flags/options before positional arguments for deterministic parsing.",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("issue_help missing %q in output: %q", want, output)
		}
	}
}

func TestRenderRootUsageUsesCanonicalTicketFlags(t *testing.T) {
	output, err := Render("root_usage", nil)
	if err != nil {
		t.Fatalf("Render(root_usage) error = %v", err)
	}

	for _, want := range []string{
		"ticket image add [--project <project-id>] [--ticket-id <ticket-id>]",
		"ticket dep add [--project <project-id>] --ticket-id <ticket-id>",
		"ticket fanout drift [--project <project-id>] --ticket <ticket-id>",
		"validation <subcommand>    Coordinate machine validation",
		"[--per-ticket-timeout duration]",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("root_usage missing %q in output: %q", want, output)
		}
	}
	for _, legacy := range []string{"--issue-id", "--issue <ticket-id>", "--per-issue-timeout"} {
		if strings.Contains(output, legacy) {
			t.Fatalf("root_usage contains legacy ticket flag %q: %q", legacy, output)
		}
	}
}
