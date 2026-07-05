package main

import (
	"bytes"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/riordanpawley/azedarach/internal/config"
	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
)

func TestParseSpecReqListArgsDedupesIDsInCallerOrder(t *testing.T) {
	opts, err := parseSpecReqListArgs([]string{
		"--issue", "bgh",
		"--status", "accepted",
		"--query", "storage lifecycle",
		"--match", "any",
		"--limit", "5",
		"--id", "req-2",
		"--ids", "req-1, req-2, , req-3",
		"--id", "req-1",
	})
	if err != nil {
		t.Fatalf("parseSpecReqListArgs error = %v", err)
	}
	if opts.Issue != "bgh" {
		t.Fatalf("issue = %q, want bgh", opts.Issue)
	}
	if opts.Status != "accepted" {
		t.Fatalf("status = %q, want accepted", opts.Status)
	}
	if opts.Query != "storage lifecycle" {
		t.Fatalf("query = %q, want storage lifecycle", opts.Query)
	}
	if opts.Match != "any" {
		t.Fatalf("match = %q, want any", opts.Match)
	}
	if opts.Limit != 5 {
		t.Fatalf("limit = %d, want 5", opts.Limit)
	}
	want := []string{"req-2", "req-1", "req-3"}
	if len(opts.IDs) != len(want) {
		t.Fatalf("len(ids) = %d, want %d (%v)", len(opts.IDs), len(want), opts.IDs)
	}
	for i := range want {
		if opts.IDs[i] != want[i] {
			t.Fatalf("ids[%d] = %q, want %q (full=%v)", i, opts.IDs[i], want[i], opts.IDs)
		}
	}
}

func TestRunSpecCommandValidationDeterminism(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Spec.Enabled = true

	tests := []struct {
		name        string
		args        []string
		errContains string
	}{
		{name: "req get missing id", args: []string{"req", "get"}, errContains: "missing required flag: --id"},
		{name: "req update invalid status", args: []string{"req", "update", "--id", "req-1", "--status", "done"}, errContains: "invalid requirement status \"done\""},
		{name: "req list invalid limit", args: []string{"req", "list", "--limit", "-1"}, errContains: "limit must be non-negative"},
		{name: "req list invalid match", args: []string{"req", "list", "--match", "near"}, errContains: "invalid match \"near\""},
		{name: "link add invalid role", args: []string{"link", "add", "--issue", "bgh", "--req", "req-1", "--role", "owns"}, errContains: "invalid link role \"owns\""},
		{name: "read positional rejected", args: []string{"read", "bgh"}, errContains: "usage: az spec read [--json] [--issue <issue-id>] [--req <req-id>]"},
		{name: "pack requires scope", args: []string{"pack", "--stage", "brownfield"}, errContains: "missing required flag: --issue or --req"},
		{name: "pack invalid stage", args: []string{"pack", "--issue", "bgh", "--stage", "audit"}, errContains: "invalid stage \"audit\""},
		{name: "graph missing issue", args: []string{"graph"}, errContains: "missing required flag: --issue"},
		{name: "graph invalid format", args: []string{"graph", "--issue", "bgh", "--format", "json"}, errContains: "invalid format \"json\""},
		{name: "slice unknown command", args: []string{"slice", "inspect"}, errContains: "unknown spec slice command: inspect"},
		{name: "slice gate requires slice", args: []string{"slice", "gate"}, errContains: "missing required flag: --slice"},
		{name: "slice gate rejects positional", args: []string{"slice", "gate", "--slice", "bgh", "extra"}, errContains: "usage: az spec slice gate --slice <slice-id>"},
		{name: "sync disabled", args: []string{"sync", "--check"}, errContains: "unknown spec command: sync"},
		{name: "unknown req command", args: []string{"req", "inspect"}, errContains: "unknown spec req command: inspect"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := runSpecCommand(cfg, tt.args)
			if err == nil || !strings.Contains(err.Error(), tt.errContains) {
				t.Fatalf("runSpecCommand(%v) error = %v, want substring %q", tt.args, err, tt.errContains)
			}
		})
	}
}

func TestRunSpecCommandHelpIncludesRestoredGrammar(t *testing.T) {
	helpOut := captureMainStdout(t, func() error {
		return runSpecCommand(config.DefaultConfig(), []string{"--help"})
	})
	for _, want := range []string{
		"az spec req get --id <req-id> [--json]",
		"az spec req list [--json] [--issue <issue-id>] [--status <open|accepted|superseded>] [--query <text>] [--match <all|any>] [--limit <n>]",
		"az spec req create --id <req-id> --title <text>",
		"az spec link add --issue <issue-id> --req <req-id>",
		"az spec pack [--json] (--issue <issue-id> | --req <req-id>)",
		"az spec graph [--json] --issue <issue-id> [--meta <path>] [--format <text|dot>]",
		"az spec slice gate --slice <slice-id>",
		"az spec parity [--json] [--fail-on-out]",
	} {
		if !strings.Contains(helpOut, want) {
			t.Fatalf("help output missing %q: %q", want, helpOut)
		}
	}
	if strings.Contains(helpOut, "az spec sync") {
		t.Fatalf("help output should not mention disabled sync command: %q", helpOut)
	}
}

func TestBuildSpecSliceGraphTopologicalAndCriticalPath(t *testing.T) {
	reqs := []protocol.SpecRequirement{
		{ID: "req-a"},
		{ID: "req-b"},
		{ID: "req-c"},
	}
	meta := specSliceMetaFile{
		Requirements: map[string]specSliceRequirementMeta{
			"req-a": {Slice: "s1"},
			"req-b": {Slice: "s2", DependsOn: []string{"s1"}},
			"req-c": {Slice: "s3", DependsOn: []string{"s2"}},
		},
	}
	graph, err := buildSpecSliceGraph(reqs, meta)
	if err != nil {
		t.Fatalf("buildSpecSliceGraph error = %v", err)
	}
	if got := strings.Join(graph.TopologicalOrder, ","); got != "s1,s2,s3" {
		t.Fatalf("topological order = %q, want s1,s2,s3", got)
	}
	if got := strings.Join(graph.CriticalPath, ","); got != "s1,s2,s3" {
		t.Fatalf("critical path = %q, want s1,s2,s3", got)
	}
	if graph.CriticalPathDepth != 3 {
		t.Fatalf("critical path depth = %d, want 3", graph.CriticalPathDepth)
	}
}

func captureMainStdoutWithError(t *testing.T, fn func() error) (string, error) {
	t.Helper()

	oldStdout := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stdout = w

	resultCh := make(chan error, 1)
	go func() {
		resultCh <- fn()
		_ = w.Close()
	}()

	var buf bytes.Buffer
	_, copyErr := io.Copy(&buf, r)
	os.Stdout = oldStdout
	runErr := <-resultCh
	if copyErr != nil {
		t.Fatalf("copy stdout: %v", copyErr)
	}
	return buf.String(), runErr
}
