package main

import (
	"fmt"
	"log/slog"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/riordanpawley/azedarach/internal/cli"
)

type commandAuditContext struct {
	InvocationID string
	StartedAt    time.Time
	Attrs        []any
}

func beginCommandAudit(logger *slog.Logger, deps *cli.Dependencies, commandShape string, args []string) commandAuditContext {
	startedAt := time.Now()
	ctx := commandAuditContext{
		InvocationID: fmt.Sprintf("%d-%d", os.Getpid(), startedAt.UnixNano()),
		StartedAt:    startedAt,
		Attrs:        commandAuditAttrs(deps, commandShape, args, startedAt),
	}
	if logger != nil {
		attrs := append([]any{"audit_event", "az.command.start", "invocation_id", ctx.InvocationID}, ctx.Attrs...)
		logger.Info("az command audit start", attrs...)
	}
	return ctx
}

func finishCommandAudit(logger *slog.Logger, ctx commandAuditContext, err error) {
	if logger == nil {
		return
	}
	status := "success"
	attrs := append([]any{
		"audit_event", "az.command.finish",
		"invocation_id", ctx.InvocationID,
		"status", status,
		"duration_ms", time.Since(ctx.StartedAt).Milliseconds(),
	}, ctx.Attrs...)
	if err != nil {
		status = "error"
		attrs[5] = status
		attrs = append(attrs, "error", err)
	}
	logger.Info("az command audit finish", attrs...)
}

func commandAuditAttrs(deps *cli.Dependencies, commandShape string, args []string, startedAt time.Time) []any {
	cwd, cwdErr := os.Getwd()
	if cwdErr != nil {
		cwd = ""
	}
	attrs := []any{
		"command_shape", commandShape,
		"argv", redactCommandArgs(args),
		"executable", filepath.Base(os.Args[0]),
		"pid", os.Getpid(),
		"ppid", os.Getppid(),
		"started_at", startedAt.UTC().Format(time.RFC3339Nano),
		"cwd", cwd,
		"pwd_env", os.Getenv("PWD"),
		"active_issue", os.Getenv("AZEDARACH_ISSUE_ID"),
	}
	if cwdErr != nil {
		attrs = append(attrs, "cwd_error", cwdErr)
	}
	if username, uid := currentAuditActor(); username != "" || uid != "" {
		attrs = append(attrs, "actor", username, "uid", uid)
	}
	if deps != nil {
		attrs = append(attrs,
			"project_id", deps.ProjectID,
			"repo_dir", deps.RepoDir,
			"runtime_repo_dir", deps.RuntimeRepoDir,
			"daemon_socket", deps.DaemonSocket,
		)
	}
	return attrs
}

func currentAuditActor() (string, string) {
	if current, err := user.Current(); err == nil && current != nil {
		return current.Username, current.Uid
	}
	return os.Getenv("USER"), strconv.Itoa(os.Getuid())
}

func redactCommandArgs(args []string) []string {
	if len(args) == 0 {
		return nil
	}
	redacted := append([]string(nil), args...)
	redactNext := false
	for i, arg := range redacted {
		if redactNext {
			redacted[i] = "[REDACTED]"
			redactNext = false
			continue
		}
		if !strings.HasPrefix(arg, "-") {
			continue
		}
		name, _, hasValue := strings.Cut(arg, "=")
		if !auditArgNameSensitive(name) {
			continue
		}
		if hasValue {
			redacted[i] = name + "=[REDACTED]"
			continue
		}
		redactNext = true
	}
	if len(redacted) >= 4 && redacted[0] == "config" && redacted[1] == "set" && auditArgNameSensitive(redacted[2]) {
		redacted[3] = "[REDACTED]"
	}
	return redacted
}

func auditArgNameSensitive(name string) bool {
	normalized := strings.ToLower(strings.TrimLeft(name, "-"))
	for _, marker := range []string{"token", "secret", "password", "passwd", "apikey", "api-key", "api_key", "access-key", "access_key"} {
		if strings.Contains(normalized, marker) {
			return true
		}
	}
	return false
}
