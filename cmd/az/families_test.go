package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/riordanpawley/azedarach/internal/cli"
	"github.com/riordanpawley/azedarach/internal/client/daemonclient"
	"github.com/riordanpawley/azedarach/internal/config"
	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/services/devserver"
)

func TestRunHooksCommandHelpAndDispatch(t *testing.T) {
	output := captureMainStdout(t, func() error {
		return runHooksCommand(config.DefaultConfig(), []string{"--help"})
	})
	if !strings.Contains(output, "Usage: az hooks install <issue-id>") {
		t.Fatalf("help output = %q", output)
	}

	projectDir := t.TempDir()
	output = captureMainStdout(t, func() error {
		return runHooksCommand(config.DefaultConfig(), []string{"install", "--project-dir", projectDir, "az-123"})
	})
	if !strings.Contains(output, "Installed hooks for issue az-123") {
		t.Fatalf("dispatch output = %q", output)
	}
	if _, err := os.Stat(filepath.Join(projectDir, ".claude", "settings.local.json")); err != nil {
		t.Fatalf("expected hooks file: %v", err)
	}
}

func TestRunGitHooksCommandHelpAndDispatch(t *testing.T) {
	output := captureMainStdout(t, func() error {
		return runGitHooksCommand(config.DefaultConfig(), []string{"--help"})
	})
	if !strings.Contains(output, "Usage: az githooks <install|run>") {
		t.Fatalf("help output = %q", output)
	}

	projectDir := t.TempDir()
	if err := exec.Command("git", "-C", projectDir, "init").Run(); err != nil {
		t.Fatalf("git init: %v", err)
	}

	output = captureMainStdout(t, func() error {
		return runGitHooksCommand(config.DefaultConfig(), []string{"install", "--project-dir", projectDir})
	})
	if !strings.Contains(output, "Installed git hooks in") {
		t.Fatalf("dispatch output = %q", output)
	}
	if _, err := os.Stat(filepath.Join(projectDir, ".githooks", "pre-commit")); err != nil {
		t.Fatalf("expected pre-commit hook: %v", err)
	}
}

func TestRunDevHelpAndGateRegression(t *testing.T) {
	helpOut := captureMainStdout(t, func() error {
		return runDevCommand(config.DefaultConfig(), []string{"--help"})
	})
	if !strings.Contains(helpOut, "Usage: az dev <gate|start|stop|restart|status|list>") {
		t.Fatalf("help output = %q", helpOut)
	}
	if !strings.Contains(helpOut, "  list [--project-dir <dir>] [--json] [--verbose]") {
		t.Fatalf("help output missing dev list = %q", helpOut)
	}

	gateOut := captureMainStdout(t, func() error {
		return runDevCommand(config.DefaultConfig(), []string{"gate", "--verbose", "--project-dir", "/tmp/dev", "az-123"})
	})
	if !strings.Contains(gateOut, "Running quality gates for: az-123") {
		t.Fatalf("gate output = %q", gateOut)
	}
}

func TestRunDevCommandsAgainstDaemonClient(t *testing.T) {
	projectDir := t.TempDir()
	transport := &devServerTransport{
		replyFn: func(req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
			switch req.Command {
			case daemonclient.CommandTaskList:
				return jsonResponse(req, []domain.Task{
					{ID: "az-123", Title: "Server 123"},
					{ID: "az-999", Title: "Server 999"},
				})
			case daemonclient.CommandDevServerStart:
				return devServerResponse(req, "az-123", devserver.Server{
					ID:        "az-123",
					Name:      "default",
					Port:      4321,
					Status:    "running",
					IssueID:   "az-123",
					StartedAt: timeNow(t),
				})
			case daemonclient.CommandDevServerStop:
				return devServerResponse(req, "az-123", devserver.Server{
					ID:        "az-123",
					Name:      "default",
					Port:      4321,
					Status:    "stopped",
					IssueID:   "az-123",
					StartedAt: timeNow(t).Add(-30 * time.Second),
					Uptime:    30 * time.Second,
				})
			case daemonclient.CommandDevServerStatus:
				var body struct {
					IssueID string `json:"issue_id"`
				}
				if err := json.Unmarshal(req.Body, &body); err != nil {
					return protocol.ResponseEnvelope{}, err
				}
				if body.IssueID == "az-123" {
					return devServerResponse(req, "az-123", devserver.Server{
						ID:        "az-123",
						Name:      "default",
						Port:      4321,
						Status:    "running",
						IssueID:   "az-123",
						StartedAt: timeNow(t),
					})
				}
				return devServerResponse(req, "az-999", devserver.Server{
					ID:      "az-999",
					Name:    "default",
					Port:    9999,
					Status:  "stopped",
					IssueID: "az-999",
				})
			default:
				return protocol.ResponseEnvelope{}, errors.New("unexpected command: " + req.Command)
			}
		},
	}
	deps := newDevDependencies(t, projectDir, transport)

	notifyOut := captureMainStdout(t, func() error {
		return runNotifyCommand(config.DefaultConfig(), []string{"--verbose", "stop", "az-123"})
	})
	if !strings.Contains(notifyOut, "Hook notification: stop for az-123 -> stopped") {
		t.Fatalf("notify output = %q", notifyOut)
	}
	startOut := captureMainStdout(t, func() error {
		return runDevStartCommand(deps, devIssueOptions{IssueID: "az-123"})
	})
	if !strings.Contains(startOut, "Started dev server for az-123") {
		t.Fatalf("start output = %q", startOut)
	}
	if !strings.Contains(startOut, "Port: 4321") {
		t.Fatalf("start output missing port = %q", startOut)
	}

	stopOut := captureMainStdout(t, func() error {
		return runDevStopCommand(deps, devIssueOptions{IssueID: "az-123", Verbose: true})
	})
	if !strings.Contains(stopOut, "Stopped dev server for az-123") {
		t.Fatalf("stop output = %q", stopOut)
	}
	if !strings.Contains(stopOut, "Status: stopped") {
		t.Fatalf("stop output missing status = %q", stopOut)
	}

	restartOut := captureMainStdout(t, func() error {
		return runDevRestartCommand(deps, devIssueOptions{IssueID: "az-123"})
	})
	if !strings.Contains(restartOut, "Restarted dev server for az-123") {
		t.Fatalf("restart output = %q", restartOut)
	}

	statusOut := captureMainStdout(t, func() error {
		return runDevStatusCommand(deps, devIssueOptions{IssueID: "az-123", Verbose: true})
	})
	if !strings.Contains(statusOut, "Dev server for az-123:") {
		t.Fatalf("status output = %q", statusOut)
	}
	if !strings.Contains(statusOut, "Status: running") {
		t.Fatalf("status output missing running state = %q", statusOut)
	}

	listOut := captureMainStdout(t, func() error {
		return runDevListCommand(deps, devListOptions{})
	})
	if !strings.Contains(listOut, "Running dev servers:") {
		t.Fatalf("list output = %q", listOut)
	}
	if !strings.Contains(listOut, "az-123") || strings.Contains(listOut, "az-999") {
		t.Fatalf("list output = %q", listOut)
	}
}

func TestRunProjectCommands(t *testing.T) {
	homeDir := t.TempDir()
	t.Setenv("HOME", homeDir)

	projectDir := filepath.Join(t.TempDir(), "azedarach")
	if err := os.MkdirAll(filepath.Join(projectDir, ".git"), 0o755); err != nil {
		t.Fatalf("create project git dir: %v", err)
	}

	addOut := captureMainStdout(t, func() error {
		return runProjectCommand(config.DefaultConfig(), []string{"add", "--name", "azedarach", projectDir})
	})
	if !strings.Contains(addOut, "Added project azedarach") {
		t.Fatalf("add output = %q", addOut)
	}

	listOut := captureMainStdout(t, func() error {
		return runProjectCommand(config.DefaultConfig(), []string{"list"})
	})
	if !strings.Contains(listOut, "Registered projects:") || !strings.Contains(listOut, "azedarach") {
		t.Fatalf("list output = %q", listOut)
	}
	if !strings.Contains(listOut, "[default]") {
		t.Fatalf("list output missing default marker = %q", listOut)
	}

	err := runProjectCommand(config.DefaultConfig(), []string{"switch", "azedarach"})
	if err == nil {
		t.Fatal("expected switch subcommand to be rejected")
	}
	if !strings.Contains(err.Error(), "unknown project command: switch") {
		t.Fatalf("switch error = %v", err)
	}

	removeOut := captureMainStdout(t, func() error {
		return runProjectCommand(config.DefaultConfig(), []string{"remove", "azedarach"})
	})
	if !strings.Contains(removeOut, "Removed project azedarach") {
		t.Fatalf("remove output = %q", removeOut)
	}
}

func captureMainStdout(t *testing.T, fn func() error) string {
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
	if runErr != nil {
		t.Fatalf("command error: %v", runErr)
	}

	return buf.String()
}

func withWorkingDir(t *testing.T, dir string, fn func()) {
	t.Helper()

	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir to temp dir: %v", err)
	}
	defer func() {
		if err := os.Chdir(wd); err != nil {
			t.Fatalf("restore cwd: %v", err)
		}
	}()

	fn()
}

type devServerTransport struct {
	replyFn  func(protocol.RequestEnvelope) (protocol.ResponseEnvelope, error)
	commands []string
}

func (t *devServerTransport) Handshake(context.Context, protocol.Hello) (protocol.HelloAck, error) {
	return protocol.HelloAck{Accepted: true}, nil
}

func (t *devServerTransport) Command(ctx context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
	_ = ctx
	t.commands = append(t.commands, req.Command)
	if t.replyFn != nil {
		return t.replyFn(req)
	}
	return protocol.ResponseEnvelope{OK: true, ProtocolVersion: req.ProtocolVersion, RequestID: req.RequestID, Kind: protocol.EnvelopeKindResponse}, nil
}

func (t *devServerTransport) Subscribe(context.Context, string, uint64) (<-chan protocol.EventEnvelope, error) {
	return nil, errors.New("not implemented")
}

func newDevDependencies(t *testing.T, repoDir string, transport *devServerTransport) *cli.Dependencies {
	t.Helper()
	client := daemonclient.New(transport).WithProjectID("proj-dev")
	return &cli.Dependencies{
		DaemonClient: client,
		ProjectID:    "proj-dev",
		RepoDir:      repoDir,
	}
}

func jsonResponse(req protocol.RequestEnvelope, value any) (protocol.ResponseEnvelope, error) {
	body, err := json.Marshal(value)
	if err != nil {
		return protocol.ResponseEnvelope{}, err
	}
	return protocol.ResponseEnvelope{
		ProtocolVersion: req.ProtocolVersion,
		RequestID:       req.RequestID,
		Kind:            protocol.EnvelopeKindResponse,
		OK:              true,
		Body:            body,
	}, nil
}

func devServerResponse(req protocol.RequestEnvelope, issueID string, srv devserver.Server) (protocol.ResponseEnvelope, error) {
	return jsonResponse(req, map[string]any{
		"issue_id": issueID,
		"server":   srv,
	})
}

func timeNow(t *testing.T) time.Time {
	t.Helper()
	return time.Date(2026, 3, 27, 12, 0, 0, 0, time.UTC)
}
