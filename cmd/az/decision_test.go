package main

import (
	"strings"
	"testing"

	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
)

func TestParseDecisionRecordArgs(t *testing.T) {
	cases := []struct {
		name    string
		args    []string
		ok      bool
		errFrag string
	}{
		{
			name: "minimum required",
			args: []string{"--title", "Use SQLite", "--rationale", "Existing schema fits"},
			ok:   true,
		},
		{
			name: "with linked issues and reqs",
			args: []string{"--title", "x", "--rationale", "y", "--issue", "az-1", "--issue", "az-2", "--req", "req-1"},
			ok:   true,
		},
		{
			name:    "missing title",
			args:    []string{"--rationale", "why"},
			ok:      false,
			errFrag: "missing required flag: --title",
		},
		{
			name:    "missing rationale",
			args:    []string{"--title", "what"},
			ok:      false,
			errFrag: "missing required flag: --rationale",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parseDecisionRecordArgs(tc.args)
			if tc.ok {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected error containing %q", tc.errFrag)
			}
			if tc.errFrag != "" && !strings.Contains(err.Error(), tc.errFrag) {
				t.Fatalf("error %q does not contain %q", err.Error(), tc.errFrag)
			}
		})
	}
}

func TestParseDecisionLinkAddArgs(t *testing.T) {
	cases := []struct {
		name    string
		args    []string
		ok      bool
		errFrag string
	}{
		{name: "missing decision id", args: []string{"--issue", "x"}, ok: false, errFrag: "--id"},
		{name: "valid w/ issue", args: []string{"--id", "dec-1", "--issue", "az-1"}, ok: true},
		{name: "valid w/ req + relation", args: []string{"--id", "dec-1", "--req", "r1", "--relation", "applies-to"}, ok: true},
		{name: "valid w/ other decision + revises", args: []string{"--id", "dec-2", "--decision", "dec-1", "--relation", "revises"}, ok: true},
		{name: "invalid relation", args: []string{"--id", "dec-1", "--req", "r1", "--relation", "garbage"}, ok: false, errFrag: "invalid relation"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parseDecisionLinkAddArgs(tc.args)
			if tc.ok {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected error containing %q", tc.errFrag)
			}
			if tc.errFrag != "" && !strings.Contains(err.Error(), tc.errFrag) {
				t.Fatalf("error %q does not contain %q", err.Error(), tc.errFrag)
			}
		})
	}
}

func TestParseDecisionRevisitArgs(t *testing.T) {
	cases := []struct {
		name    string
		args    []string
		ok      bool
		errFrag string
	}{
		{name: "link only", args: []string{"--id", "dec-1", "--new", "dec-2"}, ok: true},
		{name: "record-and-link", args: []string{"--id", "dec-1", "--title", "Switch to Postgres", "--rationale", "scale"}, ok: true},
		{name: "missing old", args: []string{"--new", "dec-2"}, ok: false, errFrag: "--id"},
		{name: "missing replacement", args: []string{"--id", "dec-1", "--title", "no rationale"}, ok: false, errFrag: "--new"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parseDecisionRevisitArgs(tc.args)
			if tc.ok {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected error containing %q", tc.errFrag)
			}
			if tc.errFrag != "" && !strings.Contains(err.Error(), tc.errFrag) {
				t.Fatalf("error %q does not contain %q", err.Error(), tc.errFrag)
			}
		})
	}
}

func TestResolveDecisionLinkTarget(t *testing.T) {
	cases := []struct {
		name     string
		issue    string
		req      string
		otherDec string
		ok       bool
		wantKind protocol.DecisionTargetKind
		wantID   string
		errFrag  string
	}{
		{name: "issue only", issue: "az-1", ok: true, wantKind: protocol.DecisionTargetIssue, wantID: "az-1"},
		{name: "req only", req: "r-1", ok: true, wantKind: protocol.DecisionTargetRequirement, wantID: "r-1"},
		{name: "decision only", otherDec: "dec-1", ok: true, wantKind: protocol.DecisionTargetDecision, wantID: "dec-1"},
		{name: "two provided", issue: "az-1", req: "r-1", ok: false, errFrag: "only one of"},
		{name: "all three", issue: "az-1", req: "r-1", otherDec: "dec-1", ok: false, errFrag: "only one of"},
		{name: "none", ok: false, errFrag: "one of"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			kind, id, err := resolveDecisionLinkTarget(tc.issue, tc.req, tc.otherDec)
			if tc.ok {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if kind != tc.wantKind || id != tc.wantID {
					t.Fatalf("got (%s, %s) want (%s, %s)", kind, id, tc.wantKind, tc.wantID)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected error containing %q", tc.errFrag)
			}
			if tc.errFrag != "" && !strings.Contains(err.Error(), tc.errFrag) {
				t.Fatalf("error %q does not contain %q", err.Error(), tc.errFrag)
			}
		})
	}
}

func TestParseDecisionSyncArgs(t *testing.T) {
	cases := []struct {
		name       string
		args       []string
		ok         bool
		projectDir string
	}{
		{name: "no flags", args: nil, ok: true},
		{name: "with --check", args: []string{"--check"}, ok: true},
		{name: "with --json --check", args: []string{"--json", "--check"}, ok: true},
		{name: "with --project-dir", args: []string{"--project-dir", "/repo/worktree"}, ok: true, projectDir: "/repo/worktree"},
		{name: "positional rejected", args: []string{"--check", "extra"}, ok: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			opts, err := parseDecisionSyncArgs(tc.args)
			if tc.ok && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !tc.ok && err == nil {
				t.Fatalf("expected error")
			}
			if opts.ProjectDir != tc.projectDir {
				t.Fatalf("ProjectDir = %q, want %q", opts.ProjectDir, tc.projectDir)
			}
		})
	}
}

func TestParseDecisionImportArgs(t *testing.T) {
	cases := []struct {
		name       string
		args       []string
		ok         bool
		projectDir string
	}{
		{name: "no flags", args: nil, ok: true},
		{name: "with --check", args: []string{"--check"}, ok: true},
		{name: "with --force", args: []string{"--force"}, ok: true},
		{name: "with all flags", args: []string{"--check", "--force", "--json"}, ok: true},
		{name: "with --project-dir", args: []string{"--project-dir", "/repo/worktree"}, ok: true, projectDir: "/repo/worktree"},
		{name: "positional rejected", args: []string{"--check", "extra"}, ok: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			opts, err := parseDecisionImportArgs(tc.args)
			if tc.ok && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !tc.ok && err == nil {
				t.Fatalf("expected error")
			}
			if opts.ProjectDir != tc.projectDir {
				t.Fatalf("ProjectDir = %q, want %q", opts.ProjectDir, tc.projectDir)
			}
		})
	}
}

func TestPrintDecisionUsageRunsCleanly(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("printDecisionUsage panicked: %v", r)
		}
	}()
	printDecisionUsage()
	printDecisionLinkUsage()
}
