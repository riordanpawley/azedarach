package main

import (
	"bytes"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
)

func TestRunLearnCommandHelpArgsReturnUsage(t *testing.T) {
	for _, args := range [][]string{
		{"--help"},
		{"add", "--help"},
		{"recall", "-h"},
		{"show", "help"},
		{"review", "--help"},
		{"promote", "--help"},
		{"retire", "--help"},
		{"relate", "--help"},
	} {
		t.Run(strings.Join(args, "_"), func(t *testing.T) {
			output := captureLearnStdout(t, func() {
				if err := runLearnCommand(nil, args); err != nil {
					t.Fatalf("runLearnCommand() error = %v", err)
				}
			})
			if !strings.Contains(output, "Usage: az learn") {
				t.Fatalf("help output missing learn usage:\n%s", output)
			}
		})
	}
}

func TestParseLearnAddArgs(t *testing.T) {
	cases := []struct {
		name    string
		args    []string
		ok      bool
		errFrag string
	}{
		{name: "minimum required", args: []string{"--evidence", "Use daemon projection before local cache."}, ok: true},
		{name: "private evidence flag", args: []string{"--evidence", "Sensitive local detail.", "--private"}, ok: true},
		{name: "scoped with repeated metadata", args: []string{"--issue", "csk", "--req", "req-1", "--evidence", "Evidence", "--tag", "daemon", "--tag", "daemon", "--file", "internal/foo.go"}, ok: true},
		{name: "missing evidence", args: []string{"--summary", "No evidence"}, ok: false, errFrag: "missing required flag: --evidence"},
		{name: "status removed from capture", args: []string{"--evidence", "Evidence", "--status", "accepted"}, ok: false, errFrag: "flag provided but not defined"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parseLearnAddArgs(tc.args)
			assertParseOutcome(t, err, tc.ok, tc.errFrag)
		})
	}
}

func TestParseLearnRecallArgs(t *testing.T) {
	cases := []struct {
		name    string
		args    []string
		ok      bool
		errFrag string
	}{
		{name: "default recall", args: []string{"--query", "daemon"}, ok: true},
		{name: "explicit private diagnostic recall", args: []string{"--query", "daemon", "--include-private", "--include-evidence"}, ok: true},
		{name: "negative limit", args: []string{"--limit", "-1"}, ok: false, errFrag: "limit must be non-negative"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parseLearnRecallArgs(tc.args)
			assertParseOutcome(t, err, tc.ok, tc.errFrag)
		})
	}
}

func TestLearningStatusLabelMarksPrivateEvidence(t *testing.T) {
	got := learningStatusLabel(protocol.Learning{
		Status:          protocol.LearningStatusAccepted,
		EvidencePrivate: true,
	})
	if got != "accepted, private" {
		t.Fatalf("learningStatusLabel() = %q, want private marker", got)
	}
}

func captureLearnStdout(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe stdout: %v", err)
	}
	os.Stdout = w
	defer func() {
		os.Stdout = old
	}()

	fn()
	_ = w.Close()
	var buf bytes.Buffer
	if _, err := io.Copy(&buf, r); err != nil {
		t.Fatalf("read stdout: %v", err)
	}
	return buf.String()
}

func TestParseLearnReviewArgs(t *testing.T) {
	cases := []struct {
		name    string
		args    []string
		ok      bool
		errFrag string
	}{
		{name: "list candidates", args: []string{"--limit", "10"}, ok: true},
		{name: "accept with note", args: []string{"--id", "learn-1", "--status", "accepted", "--note", "Verified durable enough."}, ok: true},
		{name: "status without id", args: []string{"--status", "accepted", "--note", "Reviewed."}, ok: false, errFrag: "--id is required"},
		{name: "missing status", args: []string{"--id", "learn-1", "--note", "Reviewed."}, ok: false, errFrag: "--status is required"},
		{name: "candidate is not a review outcome", args: []string{"--id", "learn-1", "--status", "candidate"}, ok: false, errFrag: "invalid review status"},
		{name: "missing note", args: []string{"--id", "learn-1", "--status", "accepted"}, ok: false, errFrag: "--note is required"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parseLearnReviewArgs(tc.args)
			assertParseOutcome(t, err, tc.ok, tc.errFrag)
		})
	}
}

func TestParseLearnPromoteArgs(t *testing.T) {
	cases := []struct {
		name    string
		args    []string
		ok      bool
		errFrag string
	}{
		{name: "decision target", args: []string{"--target", "decision", "--target-id", "dec-1", "learn-1"}, ok: true},
		{name: "create decision target", args: []string{"--target", "decision", "--create-target", "--target-title", "Decision title", "--decision-rationale", "Rationale.", "learn-1"}, ok: true},
		{name: "create spec target", args: []string{"--target", "spec", "--target-id", "req-1", "--create-target", "--target-title", "Requirement title", "--target-description", "Requirement body.", "learn-1"}, ok: true},
		{name: "target state metadata", args: []string{"--target", "agents", "--target-id", "AGENTS.md", "--target-hash", "sha256:target", "--target-meta", "path=AGENTS.md", "learn-1"}, ok: true},
		{name: "missing target", args: []string{"--target-id", "dec-1", "learn-1"}, ok: false, errFrag: "--target"},
		{name: "missing target id", args: []string{"--target", "decision", "learn-1"}, ok: false, errFrag: "--target-id"},
		{name: "create decision missing rationale", args: []string{"--target", "decision", "--create-target", "--target-title", "Decision title", "learn-1"}, ok: false, errFrag: "--decision-rationale"},
		{name: "create spec without title defers existence check", args: []string{"--target", "spec", "--target-id", "req-1", "--create-target", "learn-1"}, ok: true},
		{name: "missing learning id", args: []string{"--target", "decision", "--target-id", "dec-1"}, ok: false, errFrag: "usage: az learn promote"},
		{name: "bad target metadata", args: []string{"--target", "agents", "--target-id", "AGENTS.md", "--target-meta", "path", "learn-1"}, ok: false, errFrag: "target-meta must be key=value"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parseLearnPromoteArgs(tc.args)
			assertParseOutcome(t, err, tc.ok, tc.errFrag)
		})
	}
}

func TestParseLearnRetireArgs(t *testing.T) {
	cases := []struct {
		name    string
		args    []string
		ok      bool
		errFrag string
	}{
		{name: "learning id", args: []string{"learn-1"}, ok: true},
		{name: "json", args: []string{"--json", "learn-1"}, ok: true},
		{name: "missing learning id", args: nil, ok: false, errFrag: "usage: az learn retire"},
		{name: "too many learning ids", args: []string{"learn-1", "learn-2"}, ok: false, errFrag: "usage: az learn retire"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			opts, err := parseLearnRetireArgs(tc.args)
			assertParseOutcome(t, err, tc.ok, tc.errFrag)
			if tc.ok && opts.ID != "learn-1" {
				t.Fatalf("id = %q, want learn-1", opts.ID)
			}
		})
	}
}

func TestParseLearnRelateArgs(t *testing.T) {
	cases := []struct {
		name    string
		args    []string
		ok      bool
		errFrag string
	}{
		{name: "supersedes with scope", args: []string{"--type", "supersedes", "--note", "Newer guidance wins.", "--scope-issue", "csp", "--scope-req", "req-1", "--scope-tag", "daemon", "--scope-tag", "daemon", "--scope-file", "internal/config/config.go", "learn-2", "learn-1"}, ok: true},
		{name: "conflicts", args: []string{"--type", "conflicts", "--note", "Needs review.", "learn-2", "learn-1"}, ok: true},
		{name: "missing type", args: []string{"--note", "Needs review.", "learn-2", "learn-1"}, ok: false, errFrag: "--type"},
		{name: "invalid type", args: []string{"--type", "replaces", "--note", "Needs review.", "learn-2", "learn-1"}, ok: false, errFrag: "invalid relation type"},
		{name: "missing note", args: []string{"--type", "supersedes", "learn-2", "learn-1"}, ok: false, errFrag: "--note"},
		{name: "missing learning ids", args: []string{"--type", "supersedes", "--note", "Needs review.", "learn-2"}, ok: false, errFrag: "usage: az learn relate"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			opts, err := parseLearnRelateArgs(tc.args)
			assertParseOutcome(t, err, tc.ok, tc.errFrag)
			if tc.ok && opts.Type == "supersedes" {
				if opts.SourceLearningID != "learn-2" || opts.TargetLearningID != "learn-1" {
					t.Fatalf("relation ids = %q -> %q", opts.SourceLearningID, opts.TargetLearningID)
				}
				if len(opts.ScopeTags) != 1 {
					t.Fatalf("scope tags = %+v, want deduped single tag", opts.ScopeTags)
				}
			}
		})
	}
}

func assertParseOutcome(t *testing.T, err error, ok bool, errFrag string) {
	t.Helper()
	if ok {
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		return
	}
	if err == nil {
		t.Fatalf("expected error containing %q", errFrag)
	}
	if errFrag != "" && !strings.Contains(err.Error(), errFrag) {
		t.Fatalf("error %q does not contain %q", err.Error(), errFrag)
	}
}
