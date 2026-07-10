package main

import (
	"strings"
	"testing"
)

func TestMaybePrintCommandHelpUsesSpecificUsage(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "root help", args: []string{"--help"}, want: "Usage: az [command] [arguments]"},
		{name: "help command help", args: []string{"help", "--help"}, want: "Usage: az [command] [arguments]"},
		{name: "help root syntax", args: []string{"help", "issue", "get"}, want: issueGetUsage},
		{name: "version help", args: []string{"version", "--help"}, want: "Usage: az version"},
		{name: "session leaf", args: []string{"session", "start", "--help"}, want: "usage: az session start [--project <project-id>] <issue-id> [--wait]"},
		{name: "alias leaf", args: []string{"start", "--help"}, want: "usage: az start <issue-id> [--wait]"},
		{name: "branch leaf", args: []string{"branch", "agent-merge", "--help"}, want: "usage: az branch agent-merge [--project <project-id>] <issue-id> [--target base|<issue-id>]"},
		{name: "branch merge alias", args: []string{"branch", "merge-to-base", "--help"}, want: "usage: az branch merge [--project <project-id>] --source <issue-id> --target base|<issue-id>"},
		{name: "operation leaf", args: []string{"operation", "cancel", "--help"}, want: "Usage: az operation cancel --id <operation-id> [--reason <reason>]"},
		{name: "operation queue leaf", args: []string{"operation", "queue", "--help"}, want: "Usage: az operation queue [--issue <issue-id>]"},
		{name: "config leaf", args: []string{"config", "set", "--help"}, want: "Usage: az config set <key> <value> [--project-dir <dir>]"},
		{name: "spec nested leaf", args: []string{"spec", "req", "get", "--help"}, want: "Usage: az spec req get --id <req-id> [--json]"},
		{name: "decision nested leaf", args: []string{"decision", "link", "add", "--help"}, want: "Usage: az decision link add --id <decision-id>"},
		{name: "learn root", args: []string{"learn", "--help"}, want: "Usage: az learn <add|recall|show|review|stale|demote|promote|retire|relate|supersede|doctor|gc>"},
		{name: "learn nested leaf", args: []string{"learn", "doctor", "--help"}, want: "Usage: az learn doctor"},
		{name: "interaction resolve", args: []string{"interaction", "resolve", "--help"}, want: "--significance is always required for a human final answer"},
		{name: "githooks leaf", args: []string{"githooks", "hook", "--help"}, want: "Usage: az githooks hook --hook <name>"},
		{name: "dev leaf", args: []string{"dev", "start", "--help"}, want: "Usage: az dev start <issue-id> [--project-dir <dir>] [--json] [--verbose]"},
		{name: "project nested leaf", args: []string{"project", "scripts", "status", "--help"}, want: "Usage: az project scripts status [--project-dir <dir>] [--json] [<name> ...]"},
		{name: "ai nested leaf", args: []string{"ai", "hook", "run", "--help"}, want: "Usage: az ai hook run --agent=<claude|codex|opencode> [--json] <event>"},
		{name: "ai account leaf", args: []string{"ai", "account", "backup", "--help"}, want: "Usage: az ai account backup [--force] [--json] <provider> <profile>"},
		{name: "tmux leaf", args: []string{"tmux", "install-selector", "--help"}, want: "Usage: az tmux install-selector [--config <path>] [--project-dir <dir>] [--key <key>] [--az-command <command>] [--verbose]"},
		{name: "prime leaf", args: []string{"prime", "--help"}, want: "Usage: az prime"},
		{name: "issue root includes document", args: []string{"issue", "--help"}, want: "document add [--project <project-id>]"},
		{name: "issue events leaf", args: []string{"issue", "events", "--help"}, want: issueEventsUsage},
		{name: "issue record leaf", args: []string{"issue", "record", "--help"}, want: issueRecordUsage},
		{name: "issue context risk leaf", args: []string{"issue", "context-risk", "--help"}, want: issueContextRiskUsage},
		{name: "issue leaf", args: []string{"issue", "get", "--help"}, want: issueGetUsage},
		{name: "issue unarchive leaf", args: []string{"issue", "unarchive", "--help"}, want: issueUnarchiveUsage},
		{name: "issue parent", args: []string{"issue", "document", "--help"}, want: "Usage: az issue document <add|list|remove> [arguments]"},
		{name: "issue nested leaf", args: []string{"issue", "dep", "bulk", "apply", "--help"}, want: issueDepBulkApplyUsage},
		{name: "mail leaf", args: []string{"mail", "watch", "--help"}, want: mailWatchUsage},
		{name: "observe leaf", args: []string{"observe", "--help"}, want: observeUsage},
		{name: "orchestrate leaf", args: []string{"orchestrate", "complete-check", "--help"}, want: orchestrateCompleteCheckUsage},
		{name: "orchestrate group leaf", args: []string{"orchestrate", "group", "--help"}, want: orchestrateGroupUsage},
		{name: "orchestrate observe leaf", args: []string{"orchestrate", "observe", "--help"}, want: orchestrateObserveUsage},
		{name: "daemon leaf", args: []string{"daemon", "restart", "--help"}, want: "Usage: az daemon restart"},
		{name: "daemon watch clients leaf", args: []string{"daemon", "watch-clients", "--help"}, want: "Usage: az daemon watch-clients [--json] [--all]"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			output := captureMainStdout(t, func() error {
				if !maybePrintCommandHelp(tt.args) {
					t.Fatalf("maybePrintCommandHelp(%v) = false, want true", tt.args)
				}
				return nil
			})
			if !strings.Contains(output, tt.want) {
				t.Fatalf("help output missing %q in %q", tt.want, output)
			}
		})
	}
}

func TestMaybePrintCommandHelpIgnoresNonHelpArgs(t *testing.T) {
	if maybePrintCommandHelp([]string{"issue", "get", "csi"}) {
		t.Fatal("maybePrintCommandHelp returned true for non-help command")
	}
	if maybePrintCommandHelp([]string{"definitely-not-a-command", "--help"}) {
		t.Fatal("maybePrintCommandHelp returned true for unknown help path")
	}
}

func TestMaybePrintCommandHelpCoversRoutedCommandSurface(t *testing.T) {
	paths := [][]string{
		{},
		{"help"},
		{"version"},
		{"session"},
		{"session", "start"},
		{"session", "attach"},
		{"session", "stop"},
		{"session", "kill"},
		{"session", "status"},
		{"session", "capture"},
		{"session", "restart-all"},
		{"session", "resolve-conflict"},
		{"start"},
		{"attach"},
		{"stop"},
		{"kill"},
		{"status"},
		{"branch"},
		{"branch", "merge"},
		{"branch", "merge-to-base"},
		{"branch", "agent-merge"},
		{"worktree"},
		{"worktree", "create"},
		{"operation"},
		{"operation", "get"},
		{"operation", "list"},
		{"operation", "queue"},
		{"operation", "logs"},
		{"operation", "cancel"},
		{"export"},
		{"log"},
		{"config"},
		{"config", "set"},
		{"spec"},
		{"spec", "req"},
		{"spec", "req", "list"},
		{"spec", "req", "get"},
		{"spec", "req", "create"},
		{"spec", "req", "update"},
		{"spec", "req", "delete"},
		{"spec", "link"},
		{"spec", "link", "list"},
		{"spec", "link", "add"},
		{"spec", "link", "remove"},
		{"spec", "read"},
		{"spec", "pack"},
		{"spec", "graph"},
		{"spec", "slice"},
		{"spec", "slice", "gate"},
		{"spec", "lint"},
		{"spec", "parity"},
		{"decision"},
		{"decision", "list"},
		{"decision", "get"},
		{"decision", "record"},
		{"decision", "update"},
		{"decision", "delete"},
		{"decision", "revisit"},
		{"decision", "sync"},
		{"decision", "import"},
		{"decision", "link"},
		{"decision", "link", "list"},
		{"decision", "link", "add"},
		{"decision", "link", "remove"},
		{"learn"},
		{"learn", "add"},
		{"learn", "recall"},
		{"learn", "show"},
		{"learn", "review"},
		{"learn", "stale"},
		{"learn", "demote"},
		{"learn", "promote"},
		{"learn", "retire"},
		{"learn", "relate"},
		{"learn", "supersede"},
		{"learn", "doctor"},
		{"learn", "gc"},
		{"interaction"},
		{"interaction", "list"},
		{"interaction", "get"},
		{"interaction", "discuss"},
		{"interaction", "answer"},
		{"interaction", "resolve"},
		{"interaction", "withdraw"},
		{"sync"},
		{"githooks"},
		{"githooks", "install"},
		{"githooks", "update"},
		{"githooks", "run"},
		{"githooks", "notify"},
		{"githooks", "hook"},
		{"gate"},
		{"dev"},
		{"dev", "gate"},
		{"dev", "start"},
		{"dev", "stop"},
		{"dev", "restart"},
		{"dev", "status"},
		{"dev", "list"},
		{"project"},
		{"project", "list"},
		{"project", "add"},
		{"project", "remove"},
		{"project", "scripts"},
		{"project", "scripts", "status"},
		{"impl"},
		{"impl", "list"},
		{"impl", "delete"},
		{"impl", "migrate"},
		{"ai"},
		{"ai", "install"},
		{"ai", "status"},
		{"ai", "uninstall"},
		{"ai", "migrate"},
		{"ai", "hook"},
		{"ai", "hook", "run"},
		{"tmux"},
		{"tmux", "selector"},
		{"tmux", "install-selector"},
		{"tmux", "uninstall-selector"},
		{"prime"},
		{"issue"},
		{"issue", "list"},
		{"issue", "search"},
		{"issue", "get"},
		{"issue", "events"},
		{"issue", "record"},
		{"issue", "context-risk"},
		{"issue", "get-many"},
		{"issue", "check"},
		{"issue", "doctor"},
		{"issue", "create"},
		{"issue", "split"},
		{"issue", "update"},
		{"issue", "close"},
		{"issue", "cleanup"},
		{"issue", "delete"},
		{"issue", "image"},
		{"issue", "image", "add"},
		{"issue", "image", "remove"},
		{"issue", "document"},
		{"issue", "document", "add"},
		{"issue", "document", "list"},
		{"issue", "document", "remove"},
		{"issue", "dep"},
		{"issue", "dep", "add"},
		{"issue", "dep", "remove"},
		{"issue", "dep", "bulk"},
		{"issue", "dep", "bulk", "apply"},
		{"issue", "bulk-create"},
		{"issue", "bulk-update"},
		{"issue", "fanout"},
		{"issue", "fanout", "ready"},
		{"issue", "fanout", "drift"},
		{"mail"},
		{"mail", "send"},
		{"mail", "list"},
		{"mail", "watch"},
		{"observe"},
		{"orchestrate"},
		{"orchestrate", "status"},
		{"orchestrate", "start"},
		{"orchestrate", "group"},
		{"orchestrate", "watch"},
		{"orchestrate", "observe"},
		{"orchestrate", "prompt"},
		{"orchestrate", "message"},
		{"orchestrate", "capture"},
		{"orchestrate", "complete-check"},
		{"orchestrate", "integrate"},
		{"orchestrate", "close-session"},
		{"orchestrator-session"},
		{"orchestrator-session", "start"},
		{"orchestrator-session", "attach"},
		{"orchestrator-session", "status"},
		{"daemon"},
		{"daemon", "start"},
		{"daemon", "stop"},
		{"daemon", "restart"},
		{"daemon", "watch-clients"},
	}

	for _, path := range paths {
		name := strings.Join(path, " ")
		if name == "" {
			name = "root"
		}
		t.Run(name, func(t *testing.T) {
			args := append([]string(nil), path...)
			args = append(args, "--help")
			output := captureMainStdout(t, func() error {
				if !maybePrintCommandHelp(args) {
					t.Fatalf("maybePrintCommandHelp(%v) = false, want true", args)
				}
				return nil
			})
			if !strings.Contains(strings.ToLower(output), "usage:") {
				t.Fatalf("help output missing usage line: %q", output)
			}
		})
	}
}
