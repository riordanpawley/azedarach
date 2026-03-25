package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/riordanpawley/azedarach/internal/client/daemonclient"
	"github.com/riordanpawley/azedarach/internal/config"
	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	"github.com/riordanpawley/azedarach/internal/domain"
)

type fakeDaemonTransport struct {
	handshakeFn func(context.Context, protocol.Hello) (protocol.HelloAck, error)
	commandFn   func(context.Context, protocol.RequestEnvelope) (protocol.ResponseEnvelope, error)
}

func (f *fakeDaemonTransport) Handshake(ctx context.Context, hello protocol.Hello) (protocol.HelloAck, error) {
	if f.handshakeFn != nil {
		return f.handshakeFn(ctx, hello)
	}
	return protocol.HelloAck{Accepted: true}, nil
}

func (f *fakeDaemonTransport) Command(ctx context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
	if f.commandFn != nil {
		return f.commandFn(ctx, req)
	}
	return protocol.ResponseEnvelope{}, nil
}

func (f *fakeDaemonTransport) Subscribe(context.Context, string, uint64) (<-chan protocol.EventEnvelope, error) {
	return nil, errors.New("not implemented")
}

func TestStartCommandUsesDaemonEnvelope(t *testing.T) {
	var gotReq protocol.RequestEnvelope
	deps := &Dependencies{
		Config: config.DefaultConfig(),
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				gotReq = req
				return responseWithOutput(req, "Starting session for: issue-1 - Example\nCreating worktree from branch: main\nWorktree created: /tmp/repo-issue-1\nCreating tmux session: issue-1\n\n✓ Session started successfully\n  To attach: az attach issue-1\n  Or run:    tmux attach-session -t issue-1\n"), nil
			},
		}),
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID: "proj",
	}

	output := captureStdout(t, func() error {
		return StartCommand(deps, "issue-1")
	})

	if gotReq.Command != commandSessionStart {
		t.Fatalf("command = %q, want %q", gotReq.Command, commandSessionStart)
	}
	var body sessionRequestBody
	if err := json.Unmarshal(gotReq.Body, &body); err != nil {
		t.Fatalf("unmarshal body: %v", err)
	}
	if body.ProjectID != "proj" || body.SessionID != "issue-1" || body.BaseBranch != "main" {
		t.Fatalf("body = %+v", body)
	}
	if !strings.Contains(output, "Session started successfully") {
		t.Fatalf("output missing success text: %q", output)
	}
}

func TestAttachKillAndStatusCommandsUseDaemonEnvelope(t *testing.T) {
	tests := []struct {
		name         string
		command      func(*Dependencies, string) error
		sessionID    string
		wantCommand  string
		wantContains string
		response     string
	}{
		{
			name:         "attach",
			command:      AttachCommand,
			sessionID:    "issue-2",
			wantCommand:  commandSessionAttach,
			wantContains: "Attaching to session: issue-2",
			response:     "Attaching to session: issue-2\n(Press Ctrl+B then D to detach)\n",
		},
		{
			name:         "kill",
			command:      KillCommand,
			sessionID:    "issue-3",
			wantCommand:  commandSessionStop,
			wantContains: "Session killed: issue-3",
			response:     "Killing session: issue-3\n✓ Session killed: issue-3\n  Note: Worktree is preserved. Use 'git worktree remove' to clean up.\n",
		},
		{
			name:         "status",
			command:      StatusCommand,
			sessionID:    "",
			wantCommand:  commandSessionStatus,
			wantContains: "Active Sessions (1):",
			response:     "Active Sessions (1):\n\nISSUE ID  STATUS   TITLE\n-------  ------   -----\nbead-4   active   Example task\n\nUse 'az attach <issue-id>' to attach to a session\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var gotReq protocol.RequestEnvelope
			deps := &Dependencies{
				Config: config.DefaultConfig(),
				DaemonClient: daemonclient.New(&fakeDaemonTransport{
					commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
						gotReq = req
						return responseWithOutput(req, tt.response), nil
					},
				}),
				Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
				ProjectID: "proj",
			}

			output := captureStdout(t, func() error {
				return tt.command(deps, tt.sessionID)
			})

			if gotReq.Command != tt.wantCommand {
				t.Fatalf("command = %q, want %q", gotReq.Command, tt.wantCommand)
			}
			if gotReq.Meta.ProjectID != "proj" {
				t.Fatalf("meta project_id = %q, want proj", gotReq.Meta.ProjectID)
			}
			if !strings.Contains(output, tt.wantContains) {
				t.Fatalf("output missing %q: %q", tt.wantContains, output)
			}
		})
	}
}

func TestCommandErrorUsesTransportMessage(t *testing.T) {
	deps := &Dependencies{
		Config: config.DefaultConfig(),
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(context.Context, protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				return protocol.ResponseEnvelope{
					ProtocolVersion: protocol.CurrentVersion,
					RequestID:       "req",
					Kind:            protocol.EnvelopeKindResponse,
					OK:              false,
					Error: &protocol.ErrorEnvelope{
						Code:      protocol.ErrorCodeConflict,
						Message:   "session already exists: issue-1 (use 'az attach issue-1' to connect)",
						Retryable: false,
					},
				}, nil
			},
		}),
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID: "proj",
	}

	err := StartCommand(deps, "issue-1")
	if err == nil || err.Error() != "session already exists: issue-1 (use 'az attach issue-1' to connect)" {
		t.Fatalf("error = %v", err)
	}
}

func TestResponseExitCode(t *testing.T) {
	tests := []struct {
		name string
		resp protocol.ResponseEnvelope
		want int
	}{
		{
			name: "success response",
			resp: protocol.ResponseEnvelope{OK: true},
			want: 0,
		},
		{
			name: "dry-run preview response",
			resp: protocol.ResponseEnvelope{
				OK: true,
				Body: mustApplyDryRunPreviewBody(t, applyDryRunPreviewBody{
					SchemaVersion:    protocol.ApplySchemaVersion,
					SnapshotRevision: 7,
					DryRun:           true,
					Operations: []applyDryRunPreviewOperationBody{
						{
							Index:   0,
							Command: "task.create",
							Body:    json.RawMessage(`{"title":"First task","description":"Draft","type":"task","priority":"high"}`),
						},
					},
				}),
			},
			want: 0,
		},
		{
			name: "partial failure response",
			resp: protocol.ResponseEnvelope{
				OK: true,
				Body: mustApplyResultBody(t, applyExecutionSummaryBody{
					Total:     3,
					Succeeded: 2,
					Failed:    1,
				}),
			},
			want: 2,
		},
		{
			name: "contract failure",
			resp: protocol.ResponseEnvelope{
				OK:   true,
				Body: []byte(`{"summary":`),
			},
			want: 1,
		},
		{
			name: "transport failure",
			resp: protocol.ResponseEnvelope{OK: false},
			want: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := applyResponseExitCode(tt.resp); got != tt.want {
				t.Fatalf("applyResponseExitCode() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestParseExportArgs(t *testing.T) {
	tests := []struct {
		name        string
		args        []string
		want        ExportOptions
		errContains string
	}{
		{
			name: "defaults",
			want: ExportOptions{
				Format: "json",
				Out:    "",
			},
		},
		{
			name: "explicit out path",
			args: []string{"--format", "json", "--out", "snapshot.json"},
			want: ExportOptions{
				Format: "json",
				Out:    "snapshot.json",
			},
		},
		{
			name:        "rejects unsupported format",
			args:        []string{"--format", "yaml"},
			errContains: "unsupported export format: yaml",
		},
		{
			name:        "rejects extra arguments",
			args:        []string{"unexpected"},
			errContains: "unexpected argument: unexpected",
		},
		{
			name:        "rejects unknown flag",
			args:        []string{"--bogus"},
			errContains: "flag provided but not defined: -bogus",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseExportArgs(tt.args)
			if tt.errContains != "" {
				if err == nil || !strings.Contains(err.Error(), tt.errContains) {
					t.Fatalf("error = %v, want substring %q", err, tt.errContains)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseExportArgs() error = %v", err)
			}
			if got != tt.want {
				t.Fatalf("ParseExportArgs() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestExportCommandWritesStdoutByDefault(t *testing.T) {
	var gotReq protocol.RequestEnvelope
	payload := mustSnapshotPayloadJSON(t, protocol.SnapshotPayload{
		SchemaVersion:    protocol.SnapshotSchemaVersion,
		ProtocolVersion:  protocol.CurrentVersion,
		SnapshotRevision: 7,
	})

	deps := &Dependencies{
		Config: config.DefaultConfig(),
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				gotReq = req
				return protocol.ResponseEnvelope{
					ProtocolVersion: req.ProtocolVersion,
					RequestID:       req.RequestID,
					Kind:            protocol.EnvelopeKindResponse,
					Meta:            req.Meta,
					OK:              true,
					CompletedAt:     req.SentAt,
					Body:            payload,
				}, nil
			},
		}),
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID: "proj",
		RepoDir:   t.TempDir(),
	}

	output := captureStdout(t, func() error {
		return ExportCommand(deps, ExportOptions{Format: "json"})
	})

	if gotReq.Command != commandTaskSnapshotExport {
		t.Fatalf("command = %q, want %q", gotReq.Command, commandTaskSnapshotExport)
	}
	if gotReq.Meta.ProjectID != "proj" {
		t.Fatalf("meta project_id = %q, want proj", gotReq.Meta.ProjectID)
	}
	if output != string(payload) {
		t.Fatalf("stdout = %q, want %q", output, string(payload))
	}
}

func TestExportCommandWritesFileWhenOutIsSet(t *testing.T) {
	var gotReq protocol.RequestEnvelope
	payload := mustSnapshotPayloadJSON(t, protocol.SnapshotPayload{
		SchemaVersion:    protocol.SnapshotSchemaVersion,
		ProtocolVersion:  protocol.CurrentVersion,
		SnapshotRevision: 11,
	})
	outPath := filepath.Join(t.TempDir(), "snapshot.json")

	deps := &Dependencies{
		Config: config.DefaultConfig(),
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				gotReq = req
				return protocol.ResponseEnvelope{
					ProtocolVersion: req.ProtocolVersion,
					RequestID:       req.RequestID,
					Kind:            protocol.EnvelopeKindResponse,
					Meta:            req.Meta,
					OK:              true,
					CompletedAt:     req.SentAt,
					Body:            payload,
				}, nil
			},
		}),
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID: "proj",
		RepoDir:   t.TempDir(),
	}

	if err := ExportCommand(deps, ExportOptions{Format: "json", Out: outPath}); err != nil {
		t.Fatalf("ExportCommand() error = %v", err)
	}
	if gotReq.Command != commandTaskSnapshotExport {
		t.Fatalf("command = %q, want %q", gotReq.Command, commandTaskSnapshotExport)
	}

	written, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if string(written) != string(payload) {
		t.Fatalf("file = %q, want %q", string(written), string(payload))
	}
}

func TestExportCommandSurfacesFileWriteErrors(t *testing.T) {
	payload := mustSnapshotPayloadJSON(t, protocol.SnapshotPayload{
		SchemaVersion:    protocol.SnapshotSchemaVersion,
		ProtocolVersion:  protocol.CurrentVersion,
		SnapshotRevision: 23,
	})
	outPath := filepath.Join(t.TempDir(), "missing", "snapshot.json")

	deps := &Dependencies{
		Config: config.DefaultConfig(),
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				return protocol.ResponseEnvelope{
					ProtocolVersion: req.ProtocolVersion,
					RequestID:       req.RequestID,
					Kind:            protocol.EnvelopeKindResponse,
					Meta:            req.Meta,
					OK:              true,
					CompletedAt:     req.SentAt,
					Body:            payload,
				}, nil
			},
		}),
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID: "proj",
		RepoDir:   t.TempDir(),
	}

	err := ExportCommand(deps, ExportOptions{Format: "json", Out: outPath})
	if err == nil || !strings.Contains(err.Error(), "write export output to") {
		t.Fatalf("error = %v, want write failure", err)
	}
}

func TestParseIssueListArgs(t *testing.T) {
	tests := []struct {
		name        string
		args        []string
		want        IssueListOptions
		errContains string
	}{
		{
			name: "defaults",
			want: IssueListOptions{JSON: false},
		},
		{
			name: "json output",
			args: []string{"--json"},
			want: IssueListOptions{JSON: true},
		},
		{
			name:        "rejects extra args",
			args:        []string{"extra"},
			errContains: "unexpected argument: extra",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseIssueListArgs(tt.args)
			if tt.errContains != "" {
				if err == nil || !strings.Contains(err.Error(), tt.errContains) {
					t.Fatalf("error = %v, want substring %q", err, tt.errContains)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseIssueListArgs() error = %v", err)
			}
			if got != tt.want {
				t.Fatalf("ParseIssueListArgs() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestParseIssueGetArgs(t *testing.T) {
	tests := []struct {
		name        string
		args        []string
		want        IssueGetOptions
		errContains string
	}{
		{
			name: "defaults",
			args: []string{"az-1"},
			want: IssueGetOptions{IssueID: "az-1"},
		},
		{
			name: "json output",
			args: []string{"--json", "az-2"},
			want: IssueGetOptions{IssueID: "az-2", JSON: true},
		},
		{
			name:        "missing issue id",
			args:        []string{},
			errContains: "usage: az issue get <issue-id> [--json]",
		},
		{
			name:        "too many args",
			args:        []string{"az-1", "extra"},
			errContains: "usage: az issue get <issue-id> [--json]",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseIssueGetArgs(tt.args)
			if tt.errContains != "" {
				if err == nil || !strings.Contains(err.Error(), tt.errContains) {
					t.Fatalf("error = %v, want substring %q", err, tt.errContains)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseIssueGetArgs() error = %v", err)
			}
			if got != tt.want {
				t.Fatalf("ParseIssueGetArgs() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestParseIssueCreateArgs(t *testing.T) {
	tests := []struct {
		name        string
		args        []string
		want        IssueCreateOptions
		errContains string
	}{
		{
			name: "defaults",
			args: []string{"Title"},
			want: IssueCreateOptions{
				Title:    "Title",
				Type:     domain.TypeTask,
				Priority: domain.P2,
			},
		},
		{
			name: "explicit options",
			args: []string{"--type", "bug", "--priority", "P0", "--description", "details", "Title"},
			want: IssueCreateOptions{
				Title:       "Title",
				Description: "details",
				Type:        domain.TypeBug,
				Priority:    domain.P0,
			},
		},
		{
			name:        "invalid priority",
			args:        []string{"--priority", "high", "Title"},
			errContains: "invalid priority: high",
		},
		{
			name:        "missing title",
			args:        []string{},
			errContains: "usage: az issue create <title>",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseIssueCreateArgs(tt.args)
			if tt.errContains != "" {
				if err == nil || !strings.Contains(err.Error(), tt.errContains) {
					t.Fatalf("error = %v, want substring %q", err, tt.errContains)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseIssueCreateArgs() error = %v", err)
			}
			if got != tt.want {
				t.Fatalf("ParseIssueCreateArgs() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestParseIssueCloseArgs(t *testing.T) {
	tests := []struct {
		name        string
		args        []string
		want        IssueCloseOptions
		errContains string
	}{
		{
			name: "valid",
			args: []string{"az-1"},
			want: IssueCloseOptions{IssueID: "az-1"},
		},
		{
			name:        "missing id",
			args:        []string{},
			errContains: "usage: az issue close <issue-id>",
		},
		{
			name:        "extra args",
			args:        []string{"az-1", "extra"},
			errContains: "usage: az issue close <issue-id>",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseIssueCloseArgs(tt.args)
			if tt.errContains != "" {
				if err == nil || !strings.Contains(err.Error(), tt.errContains) {
					t.Fatalf("error = %v, want substring %q", err, tt.errContains)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseIssueCloseArgs() error = %v", err)
			}
			if got != tt.want {
				t.Fatalf("ParseIssueCloseArgs() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestParseIssueUpdateArgs(t *testing.T) {
	tests := []struct {
		name        string
		args        []string
		want        IssueUpdateOptions
		errContains string
	}{
		{
			name: "update title",
			args: []string{"--title", "Renamed", "az-1"},
			want: IssueUpdateOptions{
				IssueID: "az-1",
				Title:   "Renamed",
			},
		},
		{
			name: "update type and priority",
			args: []string{"--type", "epic", "--priority", "P0", "az-1"},
			want: func() IssueUpdateOptions {
				tt := domain.TypeEpic
				p := domain.P0
				return IssueUpdateOptions{
					IssueID:  "az-1",
					Type:     &tt,
					Priority: &p,
				}
			}(),
		},
		{
			name:        "no update fields",
			args:        []string{"az-1"},
			errContains: "no update fields provided",
		},
		{
			name:        "invalid status arg count",
			args:        []string{},
			errContains: "usage: az issue update <issue-id>",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseIssueUpdateArgs(tt.args)
			if tt.errContains != "" {
				if err == nil || !strings.Contains(err.Error(), tt.errContains) {
					t.Fatalf("error = %v, want substring %q", err, tt.errContains)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseIssueUpdateArgs() error = %v", err)
			}
			if got.IssueID != tt.want.IssueID || got.Title != tt.want.Title || got.Description != tt.want.Description {
				t.Fatalf("ParseIssueUpdateArgs() = %+v, want %+v", got, tt.want)
			}
			if (got.Type == nil) != (tt.want.Type == nil) {
				t.Fatalf("type presence mismatch: got=%v want=%v", got.Type, tt.want.Type)
			}
			if got.Type != nil && *got.Type != *tt.want.Type {
				t.Fatalf("type mismatch: got=%v want=%v", *got.Type, *tt.want.Type)
			}
			if (got.Priority == nil) != (tt.want.Priority == nil) {
				t.Fatalf("priority presence mismatch: got=%v want=%v", got.Priority, tt.want.Priority)
			}
			if got.Priority != nil && *got.Priority != *tt.want.Priority {
				t.Fatalf("priority mismatch: got=%v want=%v", *got.Priority, *tt.want.Priority)
			}
		})
	}
}

func TestParseIssueStatusArgs(t *testing.T) {
	got, err := ParseIssueStatusArgs([]string{"az-1", "blocked"})
	if err != nil {
		t.Fatalf("ParseIssueStatusArgs() error = %v", err)
	}
	if got.IssueID != "az-1" || got.Status != domain.StatusBlocked {
		t.Fatalf("ParseIssueStatusArgs() = %+v", got)
	}
	_, err = ParseIssueStatusArgs([]string{"az-1"})
	if err == nil || !strings.Contains(err.Error(), "usage: az issue status <issue-id> <open|in_progress|blocked|closed>") {
		t.Fatalf("expected usage error, got %v", err)
	}
	_, err = ParseIssueStatusArgs([]string{"az-1", "done"})
	if err == nil || !strings.Contains(err.Error(), "invalid status: done") {
		t.Fatalf("expected invalid status error, got %v", err)
	}
}

func TestIssueListCommandUsesDaemonTaskList(t *testing.T) {
	now := time.Date(2026, 3, 25, 10, 0, 0, 0, time.UTC)
	tasks := []domain.Task{
		{
			ID:        "az-2",
			Title:     "Older issue",
			Status:    domain.StatusOpen,
			Priority:  domain.P2,
			Type:      domain.TypeTask,
			CreatedAt: now,
			UpdatedAt: now,
		},
		{
			ID:        "az-1",
			Title:     "Newest issue",
			Status:    domain.StatusInProgress,
			Priority:  domain.P1,
			Type:      domain.TypeFeature,
			CreatedAt: now,
			UpdatedAt: now.Add(1 * time.Hour),
		},
	}

	var gotReq protocol.RequestEnvelope
	deps := &Dependencies{
		Config: config.DefaultConfig(),
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				gotReq = req
				body, err := json.Marshal(tasks)
				if err != nil {
					t.Fatalf("marshal tasks: %v", err)
				}
				return protocol.ResponseEnvelope{
					ProtocolVersion: req.ProtocolVersion,
					RequestID:       req.RequestID,
					Kind:            protocol.EnvelopeKindResponse,
					Meta:            req.Meta,
					OK:              true,
					CompletedAt:     req.SentAt,
					Revision:        3,
					Body:            body,
				}, nil
			},
		}),
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID: "proj",
	}

	output := captureStdout(t, func() error {
		return IssueListCommand(deps, IssueListOptions{})
	})

	if gotReq.Command != daemonclient.CommandTaskList {
		t.Fatalf("command = %q, want %q", gotReq.Command, daemonclient.CommandTaskList)
	}
	if !strings.Contains(output, "ID") || !strings.Contains(output, "STATUS") || !strings.Contains(output, "PRIORITY") || !strings.Contains(output, "TITLE") {
		t.Fatalf("output missing header: %q", output)
	}
	if first, second := strings.Index(output, "az-1"), strings.Index(output, "az-2"); !(first >= 0 && second > first) {
		t.Fatalf("expected newest issue first in output: %q", output)
	}
}

func TestIssueGetCommandJSON(t *testing.T) {
	now := time.Date(2026, 3, 25, 11, 0, 0, 0, time.UTC)
	tasks := []domain.Task{
		{
			ID:          "az-5",
			Title:       "Lookup issue",
			Description: "Detailed context",
			Status:      domain.StatusBlocked,
			Priority:    domain.P0,
			Type:        domain.TypeBug,
			CreatedAt:   now,
			UpdatedAt:   now,
		},
	}

	deps := &Dependencies{
		Config: config.DefaultConfig(),
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				body, err := json.Marshal(tasks)
				if err != nil {
					t.Fatalf("marshal tasks: %v", err)
				}
				return protocol.ResponseEnvelope{
					ProtocolVersion: req.ProtocolVersion,
					RequestID:       req.RequestID,
					Kind:            protocol.EnvelopeKindResponse,
					Meta:            req.Meta,
					OK:              true,
					CompletedAt:     req.SentAt,
					Revision:        4,
					Body:            body,
				}, nil
			},
		}),
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID: "proj",
	}

	output := captureStdout(t, func() error {
		return IssueGetCommand(deps, IssueGetOptions{IssueID: "az-5", JSON: true})
	})
	if !strings.Contains(output, "\"id\": \"az-5\"") || !strings.Contains(output, "\"title\": \"Lookup issue\"") {
		t.Fatalf("output missing issue json fields: %q", output)
	}
}

func TestIssueGetCommandNotFound(t *testing.T) {
	deps := &Dependencies{
		Config: config.DefaultConfig(),
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				return protocol.ResponseEnvelope{
					ProtocolVersion: req.ProtocolVersion,
					RequestID:       req.RequestID,
					Kind:            protocol.EnvelopeKindResponse,
					Meta:            req.Meta,
					OK:              true,
					CompletedAt:     req.SentAt,
					Revision:        1,
					Body:            []byte(`[]`),
				}, nil
			},
		}),
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID: "proj",
	}

	err := IssueGetCommand(deps, IssueGetOptions{IssueID: "az-missing"})
	if err == nil || !strings.Contains(err.Error(), "issue not found: az-missing") {
		t.Fatalf("error = %v, want not found", err)
	}
}

func TestIssueCreateAndCloseCommandsUseDaemonTaskCommands(t *testing.T) {
	tests := []struct {
		name        string
		run         func(*Dependencies) error
		wantCommand string
		wantText    string
	}{
		{
			name: "create",
			run: func(deps *Dependencies) error {
				return IssueCreateCommand(deps, IssueCreateOptions{
					Title:       "New issue",
					Description: "Context",
					Type:        domain.TypeFeature,
					Priority:    domain.P1,
				})
			},
			wantCommand: daemonclient.CommandTaskCreate,
			wantText:    "Created issue: az-42",
		},
		{
			name: "close",
			run: func(deps *Dependencies) error {
				return IssueCloseCommand(deps, IssueCloseOptions{IssueID: "az-9"})
			},
			wantCommand: daemonclient.CommandTaskUpdateStatus,
			wantText:    "Closed issue: az-9",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var gotReq protocol.RequestEnvelope
			deps := &Dependencies{
				Config: config.DefaultConfig(),
				DaemonClient: daemonclient.New(&fakeDaemonTransport{
					commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
						gotReq = req
						body := []byte{}
						if req.Command == daemonclient.CommandTaskCreate {
							payload, err := json.Marshal(map[string]string{"task_id": "az-42"})
							if err != nil {
								t.Fatalf("marshal task create response: %v", err)
							}
							body = payload
						}
						return protocol.ResponseEnvelope{
							ProtocolVersion: req.ProtocolVersion,
							RequestID:       req.RequestID,
							Kind:            protocol.EnvelopeKindResponse,
							Meta:            req.Meta,
							OK:              true,
							CompletedAt:     req.SentAt,
							Body:            body,
						}, nil
					},
				}),
				Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
				ProjectID: "proj",
			}

			output := captureStdout(t, func() error {
				return tt.run(deps)
			})
			if gotReq.Command != tt.wantCommand {
				t.Fatalf("command = %q, want %q", gotReq.Command, tt.wantCommand)
			}
			if !strings.Contains(output, tt.wantText) {
				t.Fatalf("output missing %q: %q", tt.wantText, output)
			}
		})
	}
}

func TestIssueUpdateAndStatusCommandsUseDaemonTaskCommands(t *testing.T) {
	now := time.Date(2026, 3, 25, 10, 0, 0, 0, time.UTC)
	var gotUpdateReq protocol.RequestEnvelope
	var gotStatusReq protocol.RequestEnvelope
	deps := &Dependencies{
		Config: config.DefaultConfig(),
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				switch req.Command {
				case daemonclient.CommandTaskList:
					body, err := json.Marshal([]domain.Task{
						{
							ID:          "az-1",
							Title:       "Old",
							Description: "OldDesc",
							Type:        domain.TypeTask,
							Priority:    domain.P2,
							Status:      domain.StatusOpen,
							CreatedAt:   now,
							UpdatedAt:   now,
						},
					})
					if err != nil {
						t.Fatalf("marshal task list: %v", err)
					}
					return protocol.ResponseEnvelope{
						ProtocolVersion: req.ProtocolVersion,
						RequestID:       req.RequestID,
						Kind:            protocol.EnvelopeKindResponse,
						Meta:            req.Meta,
						OK:              true,
						CompletedAt:     req.SentAt,
						Revision:        2,
						Body:            body,
					}, nil
				case daemonclient.CommandTaskUpdate:
					gotUpdateReq = req
					return protocol.ResponseEnvelope{
						ProtocolVersion: req.ProtocolVersion,
						RequestID:       req.RequestID,
						Kind:            protocol.EnvelopeKindResponse,
						Meta:            req.Meta,
						OK:              true,
						CompletedAt:     req.SentAt,
					}, nil
				case daemonclient.CommandTaskUpdateStatus:
					gotStatusReq = req
					return protocol.ResponseEnvelope{
						ProtocolVersion: req.ProtocolVersion,
						RequestID:       req.RequestID,
						Kind:            protocol.EnvelopeKindResponse,
						Meta:            req.Meta,
						OK:              true,
						CompletedAt:     req.SentAt,
					}, nil
				default:
					return protocol.ResponseEnvelope{
						ProtocolVersion: req.ProtocolVersion,
						RequestID:       req.RequestID,
						Kind:            protocol.EnvelopeKindResponse,
						Meta:            req.Meta,
						OK:              true,
						CompletedAt:     req.SentAt,
					}, nil
				}
			},
		}),
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID: "proj",
	}

	updateOut := captureStdout(t, func() error {
		return IssueUpdateCommand(deps, IssueUpdateOptions{
			IssueID: "az-1",
			Title:   "New",
		})
	})
	if gotUpdateReq.Command != daemonclient.CommandTaskUpdate {
		t.Fatalf("update command = %q, want %q", gotUpdateReq.Command, daemonclient.CommandTaskUpdate)
	}
	if !strings.Contains(updateOut, "Updated issue: az-1") {
		t.Fatalf("update output = %q", updateOut)
	}

	statusOut := captureStdout(t, func() error {
		return IssueStatusCommand(deps, IssueStatusOptions{
			IssueID: "az-1",
			Status:  domain.StatusBlocked,
		})
	})
	if gotStatusReq.Command != daemonclient.CommandTaskUpdateStatus {
		t.Fatalf("status command = %q, want %q", gotStatusReq.Command, daemonclient.CommandTaskUpdateStatus)
	}
	if !strings.Contains(statusOut, "Updated status: az-1 -> blocked") {
		t.Fatalf("status output = %q", statusOut)
	}
}

func TestPrintUsageIncludesExport(t *testing.T) {
	output := captureStdout(t, func() error {
		PrintUsage()
		return nil
	})

	if !strings.Contains(output, "export") {
		t.Fatalf("usage missing export command: %q", output)
	}
	if !strings.Contains(output, "az export --format json --out snapshot.json") {
		t.Fatalf("usage missing export example: %q", output)
	}
	if !strings.Contains(output, "issue list [--json]") {
		t.Fatalf("usage missing issue list command: %q", output)
	}
	if !strings.Contains(output, "issue get <id> [--json]") {
		t.Fatalf("usage missing issue get command: %q", output)
	}
	if !strings.Contains(output, "issue create <title>") {
		t.Fatalf("usage missing issue create command: %q", output)
	}
	if !strings.Contains(output, "issue update <id>") {
		t.Fatalf("usage missing issue update command: %q", output)
	}
	if !strings.Contains(output, "issue status <id>") {
		t.Fatalf("usage missing issue status command: %q", output)
	}
	if !strings.Contains(output, "issue close <id>") {
		t.Fatalf("usage missing issue close command: %q", output)
	}
}

type fakeLauncher struct {
	replaceErr    error
	replaceCalled bool
}

func (f *fakeLauncher) Start(context.Context) error { return nil }

func (f *fakeLauncher) Replace(context.Context) error {
	f.replaceCalled = true
	return f.replaceErr
}

func TestRestartDaemonCommand(t *testing.T) {
	oldLauncher := newLauncher
	t.Cleanup(func() { newLauncher = oldLauncher })

	fake := &fakeLauncher{}
	newLauncher = func(_, _ string) daemonStarter {
		return fake
	}

	deps := &Dependencies{
		Config: config.DefaultConfig(),
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			handshakeFn: func(context.Context, protocol.Hello) (protocol.HelloAck, error) {
				return protocol.HelloAck{Accepted: true}, nil
			},
		}),
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID: "proj",
		RepoDir:   t.TempDir(),
	}

	output := captureStdout(t, func() error {
		return RestartDaemonCommand(deps)
	})

	if !fake.replaceCalled {
		t.Fatalf("expected replace to be called")
	}
	if !strings.Contains(output, "Daemon restarted successfully.") {
		t.Fatalf("output missing restart success: %q", output)
	}
}

func TestRestartDaemonCommandReplaceFailure(t *testing.T) {
	oldLauncher := newLauncher
	t.Cleanup(func() { newLauncher = oldLauncher })

	fake := &fakeLauncher{replaceErr: errors.New("boom")}
	newLauncher = func(_, _ string) daemonStarter {
		return fake
	}

	deps := &Dependencies{
		Config:       config.DefaultConfig(),
		DaemonClient: daemonclient.New(&fakeDaemonTransport{}),
		Logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID:    "proj",
		RepoDir:      t.TempDir(),
	}

	err := RestartDaemonCommand(deps)
	if err == nil || !strings.Contains(err.Error(), "restart daemon: boom") {
		t.Fatalf("error = %v, want restart daemon boom", err)
	}
}

func responseWithOutput(req protocol.RequestEnvelope, output string) protocol.ResponseEnvelope {
	payload, err := json.Marshal(commandOutputBody{Output: output})
	if err != nil {
		panic(err)
	}
	return protocol.ResponseEnvelope{
		ProtocolVersion: req.ProtocolVersion,
		RequestID:       req.RequestID,
		Kind:            protocol.EnvelopeKindResponse,
		Meta:            req.Meta,
		CompletedAt:     req.SentAt,
		OK:              true,
		Body:            payload,
	}
}

func mustSnapshotPayloadJSON(t *testing.T, payload protocol.SnapshotPayload) []byte {
	t.Helper()

	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal snapshot payload: %v", err)
	}
	return data
}

func mustApplyResultBody(t *testing.T, summary applyExecutionSummaryBody) []byte {
	t.Helper()

	data, err := json.Marshal(applyExecutionResultBody{Summary: summary})
	if err != nil {
		t.Fatalf("marshal apply result body: %v", err)
	}
	return data
}

type applyDryRunPreviewBody struct {
	SchemaVersion    uint16                            `json:"schema_version"`
	SnapshotRevision uint64                            `json:"snapshot_revision"`
	DryRun           bool                              `json:"dry_run"`
	Operations       []applyDryRunPreviewOperationBody `json:"operations"`
}

type applyDryRunPreviewOperationBody struct {
	Index   int             `json:"index"`
	Command string          `json:"command"`
	Body    json.RawMessage `json:"body,omitempty"`
}

func mustApplyDryRunPreviewBody(t *testing.T, preview applyDryRunPreviewBody) []byte {
	t.Helper()

	data, err := json.Marshal(preview)
	if err != nil {
		t.Fatalf("marshal dry-run preview body: %v", err)
	}
	return data
}

func captureStdout(t *testing.T, fn func() error) string {
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
