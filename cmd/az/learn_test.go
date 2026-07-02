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
		{name: "missing target", args: []string{"--target-id", "dec-1", "learn-1"}, ok: false, errFrag: "--target"},
		{name: "missing target id", args: []string{"--target", "decision", "learn-1"}, ok: false, errFrag: "--target-id"},
		{name: "missing learning id", args: []string{"--target", "decision", "--target-id", "dec-1"}, ok: false, errFrag: "usage: az learn promote"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parseLearnPromoteArgs(tc.args)
			assertParseOutcome(t, err, tc.ok, tc.errFrag)
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
