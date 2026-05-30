package main

import (
	"bytes"
	"errors"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/riordanpawley/azedarach/internal/cli"
	"github.com/riordanpawley/azedarach/internal/logging"
)

func TestRedactCommandArgs(t *testing.T) {
	got := redactCommandArgs([]string{
		"config", "set", "github.token", "ghp_secret",
		"--password", "p4ss",
		"--api-key=abc123",
		"--project", "az",
	})
	want := []string{
		"config", "set", "github.token", "[REDACTED]",
		"--password", "[REDACTED]",
		"--api-key=[REDACTED]",
		"--project", "az",
	}
	if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("redactCommandArgs() = %#v, want %#v", got, want)
	}
}

func TestCommandAuditLogsStartAndFinishWithContext(t *testing.T) {
	var buf bytes.Buffer
	logger := logging.NewTextStreamLogger(&buf, slog.LevelInfo)
	t.Setenv("AZEDARACH_ISSUE_ID", "ckf")
	t.Setenv("PWD", "/env/pwd")
	oldArgs := os.Args
	os.Args = []string{"/tmp/az", "issue", "get", "ckf"}
	t.Cleanup(func() { os.Args = oldArgs })

	deps := &cli.Dependencies{
		ProjectID:      "proj",
		RepoDir:        "/repo",
		RuntimeRepoDir: "/runtime",
		DaemonSocket:   "/socket",
	}
	ctx := beginCommandAudit(logger, deps, "issue get", os.Args[1:])
	finishCommandAudit(logger, ctx, errors.New("boom"))

	output := buf.String()
	for _, want := range []string{
		"audit_event=az.command.start",
		"audit_event=az.command.finish",
		"status=error",
		"active_issue=ckf",
		"project_id=proj",
		"repo_dir=/repo",
		"runtime_repo_dir=/runtime",
		"daemon_socket=/socket",
		"argv=\"[issue get ckf]\"",
		"error=boom",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("audit output missing %q:\n%s", want, output)
		}
	}
}

func TestCommandAuditAttrsIncludeWorkingDirectory(t *testing.T) {
	attrs := commandAuditAttrs(nil, "issue list", []string{"issue", "list"}, time.Unix(0, 0), "/cwd", nil, "user", "501")
	if !auditAttrsContainKey(attrs, "cwd") {
		t.Fatalf("attrs missing cwd: %#v", attrs)
	}
	if !auditAttrsContainKey(attrs, "pwd_env") {
		t.Fatalf("attrs missing pwd_env: %#v", attrs)
	}
}

func TestCommandAuditSetsAndRestoresEnvForDaemonAttribution(t *testing.T) {
	t.Setenv("AZEDARACH_AUDIT_INVOCATION_ID", "previous")
	oldArgs := os.Args
	os.Args = []string{"/tmp/az", "session", "stop", "ckf", "--token", "secret"}
	t.Cleanup(func() { os.Args = oldArgs })

	ctx := beginCommandAudit(nil, nil, "session stop", os.Args[1:])
	if got := os.Getenv("AZEDARACH_AUDIT_INVOCATION_ID"); got != ctx.InvocationID {
		t.Fatalf("invocation env = %q, want %q", got, ctx.InvocationID)
	}
	if got := os.Getenv("AZEDARACH_AUDIT_COMMAND_SHAPE"); got != "session stop" {
		t.Fatalf("command shape env = %q", got)
	}
	if got := os.Getenv("AZEDARACH_AUDIT_ARGV_JSON"); !strings.Contains(got, "[REDACTED]") || strings.Contains(got, "secret") {
		t.Fatalf("argv env = %q, want redacted", got)
	}
	finishCommandAudit(nil, ctx, nil)
	if got := os.Getenv("AZEDARACH_AUDIT_INVOCATION_ID"); got != "previous" {
		t.Fatalf("restored invocation env = %q, want previous", got)
	}
}

func auditAttrsContainKey(attrs []any, key string) bool {
	for i := 0; i < len(attrs)-1; i += 2 {
		if attrs[i] == key {
			return true
		}
	}
	return false
}
