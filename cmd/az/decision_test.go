package main

import (
	"strings"
	"testing"

	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
)

func TestParseDecisionCreateArgs(t *testing.T) {
	cases := []struct {
		name    string
		args    []string
		ok      bool
		want    decisionCreateOpts
		errFrag string
	}{
		{
			name: "minimum required",
			args: []string{"--id", "use-sqlite", "--title", "Use SQLite"},
			ok:   true,
			want: decisionCreateOpts{ID: "use-sqlite", Title: "Use SQLite"},
		},
		{
			name: "with linked issues and reqs",
			args: []string{"--id", "x", "--title", "y", "--issue", "az-1", "--issue", "az-2", "--req", "req-1"},
			ok:   true,
			want: decisionCreateOpts{ID: "x", Title: "y", IssueLinks: []string{"az-1", "az-2"}, ReqLinks: []string{"req-1"}},
		},
		{
			name: "valid status",
			args: []string{"--id", "x", "--title", "y", "--status", "accepted"},
			ok:   true,
			want: decisionCreateOpts{ID: "x", Title: "y", Status: "accepted"},
		},
		{
			name:    "missing id",
			args:    []string{"--title", "x"},
			ok:      false,
			errFrag: "missing required flag: --id",
		},
		{
			name:    "missing title",
			args:    []string{"--id", "x"},
			ok:      false,
			errFrag: "missing required flag: --title",
		},
		{
			name:    "invalid status",
			args:    []string{"--id", "x", "--title", "y", "--status", "garbage"},
			ok:      false,
			errFrag: "invalid status",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			opts, err := parseDecisionCreateArgs(tc.args)
			if tc.ok {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if opts.ID != tc.want.ID || opts.Title != tc.want.Title || opts.Status != tc.want.Status {
					t.Fatalf("opts mismatch: got %+v want %+v", opts, tc.want)
				}
				if len(opts.IssueLinks) != len(tc.want.IssueLinks) {
					t.Fatalf("IssueLinks mismatch: got %v want %v", opts.IssueLinks, tc.want.IssueLinks)
				}
				if len(opts.ReqLinks) != len(tc.want.ReqLinks) {
					t.Fatalf("ReqLinks mismatch: got %v want %v", opts.ReqLinks, tc.want.ReqLinks)
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
		{name: "valid w/ issue", args: []string{"--id", "d1", "--issue", "az-1"}, ok: true},
		{name: "valid w/ req + relation", args: []string{"--id", "d1", "--req", "r1", "--relation", "implements"}, ok: true},
		{name: "invalid relation", args: []string{"--id", "d1", "--req", "r1", "--relation", "garbage"}, ok: false, errFrag: "invalid relation"},
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

func TestResolveDecisionTarget(t *testing.T) {
	cases := []struct {
		name      string
		issue     string
		req       string
		ok        bool
		wantKind  protocol.DecisionTargetKind
		wantID    string
		errFrag   string
	}{
		{name: "issue only", issue: "az-1", ok: true, wantKind: protocol.DecisionTargetIssue, wantID: "az-1"},
		{name: "req only", req: "r-1", ok: true, wantKind: protocol.DecisionTargetRequirement, wantID: "r-1"},
		{name: "both provided", issue: "az-1", req: "r-1", ok: false, errFrag: "only one of"},
		{name: "neither", ok: false, errFrag: "either --issue or --req"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			kind, id, err := resolveDecisionTarget(tc.issue, tc.req)
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

func TestPrintDecisionUsageRunsCleanly(t *testing.T) {
	// Defensive: usage path should not panic and should be non-empty (Stdout capture not
	// strictly necessary; we just want to ensure no nil deref or formatting panic).
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("printDecisionUsage panicked: %v", r)
		}
	}()
	printDecisionUsage()
	printDecisionLinkUsage()
}
