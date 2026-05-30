package main

import (
	"encoding/json"
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
	restoreEnv   func()
}

func beginCommandAudit(logger *slog.Logger, deps *cli.Dependencies, commandShape string, args []string) commandAuditContext {
	startedAt := time.Now()
	redactedArgs := redactCommandArgs(args)
	username, uid := currentAuditActor()
	cwd, cwdErr := os.Getwd()
	if cwdErr != nil {
		cwd = ""
	}
	ctx := commandAuditContext{
		InvocationID: fmt.Sprintf("%d-%d", os.Getpid(), startedAt.UnixNano()),
		StartedAt:    startedAt,
	}
	ctx.Attrs = commandAuditAttrs(deps, commandShape, redactedArgs, startedAt, cwd, cwdErr, username, uid)
	ctx.restoreEnv = setCommandAuditEnv(ctx.InvocationID, commandShape, redactedArgs, cwd, username, uid)
	if logger != nil {
		attrs := append([]any{"audit_event", "az.command.start", "invocation_id", ctx.InvocationID}, ctx.Attrs...)
		logger.Info("az command audit start", attrs...)
	}
	return ctx
}

func finishCommandAudit(logger *slog.Logger, ctx commandAuditContext, err error) {
	defer func() {
		if ctx.restoreEnv != nil {
			ctx.restoreEnv()
		}
	}()
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

func commandAuditAttrs(deps *cli.Dependencies, commandShape string, args []string, startedAt time.Time, cwd string, cwdErr error, username string, uid string) []any {
	attrs := []any{
		"command_shape", commandShape,
		"argv", args,
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
	if username != "" || uid != "" {
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

func setCommandAuditEnv(invocationID string, commandShape string, args []string, cwd string, username string, uid string) func() {
	argvJSON, _ := json.Marshal(args)
	updates := map[string]string{
		"AZEDARACH_AUDIT_INVOCATION_ID": invocationID,
		"AZEDARACH_AUDIT_COMMAND_SHAPE": commandShape,
		"AZEDARACH_AUDIT_ARGV_JSON":     string(argvJSON),
		"AZEDARACH_AUDIT_EXECUTABLE":    filepath.Base(os.Args[0]),
		"AZEDARACH_AUDIT_PID":           strconv.Itoa(os.Getpid()),
		"AZEDARACH_AUDIT_PPID":          strconv.Itoa(os.Getppid()),
		"AZEDARACH_AUDIT_CWD":           cwd,
		"AZEDARACH_AUDIT_PWD":           os.Getenv("PWD"),
		"AZEDARACH_AUDIT_ACTOR":         username,
		"AZEDARACH_AUDIT_UID":           uid,
		"AZEDARACH_AUDIT_ACTIVE_ISSUE":  os.Getenv("AZEDARACH_ISSUE_ID"),
	}
	previous := make(map[string]*string, len(updates))
	for key, value := range updates {
		if old, ok := os.LookupEnv(key); ok {
			oldCopy := old
			previous[key] = &oldCopy
		} else {
			previous[key] = nil
		}
		_ = os.Setenv(key, value)
	}
	return func() {
		for key, value := range previous {
			if value == nil {
				_ = os.Unsetenv(key)
				continue
			}
			_ = os.Setenv(key, *value)
		}
	}
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
