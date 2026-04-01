package main

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/riordanpawley/azedarach/internal/cli"
	"github.com/riordanpawley/azedarach/internal/config"
)

func TestParseSpecReqListArgsDedupesIDsInCallerOrder(t *testing.T) {
	opts, err := parseSpecReqListArgs([]string{
		"--issue", "bgh",
		"--status", "accepted",
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
		{name: "link add invalid role", args: []string{"link", "add", "--issue", "bgh", "--req", "req-1", "--role", "owns"}, errContains: "invalid link role \"owns\""},
		{name: "read positional rejected", args: []string{"read", "bgh"}, errContains: "usage: az spec read [--json] [--issue <issue-id>] [--req <req-id>]"},
		{name: "sync target required", args: []string{"sync", "--check"}, errContains: "usage: az spec sync --target md [--check] [--json]"},
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
		"az spec req create --id <req-id> --title <text>",
		"az spec link add --issue <issue-id> --req <req-id>",
		"az spec parity [--json] [--fail-on-out]",
		"az spec sync --target md [--check] [--json]",
	} {
		if !strings.Contains(helpOut, want) {
			t.Fatalf("help output missing %q: %q", want, helpOut)
		}
	}
}

func TestRunSpecSyncCommandWritesAndChecksMarkdown(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Spec.Enabled = true
	projectDir := t.TempDir()

	withWorkingDir(t, projectDir, func() {
		out, err := captureMainStdoutWithError(t, func() error {
			return runSpecCommand(cfg, []string{"sync", "--target", "md"})
		})
		if err != nil {
			t.Fatalf("initial sync error = %v", err)
		}
		if !strings.Contains(out, "Changed: true") {
			t.Fatalf("initial sync output = %q", out)
		}
		if _, err := os.Stat(filepath.Join(projectDir, "docs", "spec", "README.md")); err != nil {
			t.Fatalf("expected docs/spec/README.md: %v", err)
		}

		checkOut, err := captureMainStdoutWithError(t, func() error {
			return runSpecCommand(cfg, []string{"sync", "--target", "md", "--check"})
		})
		if err != nil {
			t.Fatalf("clean check error = %v", err)
		}
		if !strings.Contains(checkOut, "Changed: false") {
			t.Fatalf("clean check output = %q", checkOut)
		}

		readmePath := filepath.Join(projectDir, "docs", "spec", "README.md")
		if err := os.WriteFile(readmePath, []byte("drift\n"), 0o644); err != nil {
			t.Fatalf("write drifted README: %v", err)
		}
		driftOut, err := captureMainStdoutWithError(t, func() error {
			return runSpecCommand(cfg, []string{"sync", "--target", "md", "--check"})
		})
		if !errors.Is(err, cli.ErrSpecMarkdownDrift) {
			t.Fatalf("drift check error = %v, want ErrSpecMarkdownDrift", err)
		}
		if !strings.Contains(driftOut, "docs/spec/README.md: drift") {
			t.Fatalf("drift check output = %q", driftOut)
		}
	})
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
