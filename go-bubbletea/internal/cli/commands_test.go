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
	"reflect"
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
	if output != "Starting session for: issue-1 - Example\nCreating worktree from branch: main\nWorktree created: /tmp/repo-issue-1\nCreating tmux session: issue-1\n\n✓ Session started successfully\n  To attach: az attach issue-1\n  Or run:    tmux attach-session -t issue-1\n" {
		t.Fatalf("output = %q", output)
	}
}

func TestAttachKillAndStatusCommandsUseDaemonEnvelope(t *testing.T) {
	tests := []struct {
		name        string
		command     func(*Dependencies, string) error
		sessionID   string
		wantCommand string
		response    string
	}{
		{
			name:        "attach",
			command:     AttachCommand,
			sessionID:   "issue-2",
			wantCommand: commandSessionAttach,
			response:    "Attaching to session: issue-2\n(Press Ctrl+B then D to detach)\n",
		},
		{
			name:        "kill",
			command:     KillCommand,
			sessionID:   "issue-3",
			wantCommand: commandSessionStop,
			response:    "Killing session: issue-3\n✓ Session killed: issue-3\n  Note: Worktree is preserved. Use 'git worktree remove' to clean up.\n",
		},
		{
			name:        "status",
			command:     StatusCommand,
			sessionID:   "",
			wantCommand: commandSessionStatus,
			response:    "Active Sessions (1):\n\nISSUE ID  STATUS   TITLE\n-------  ------   -----\nbead-4   active   Example task\n\nUse 'az attach <issue-id>' to attach to a session\n",
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
			if output != tt.response {
				t.Fatalf("output = %q, want %q", output, tt.response)
			}
		})
	}
}

func TestStartCommandPrintsNestedOperationOutput(t *testing.T) {
	deps := &Dependencies{
		Config: config.DefaultConfig(),
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				nested, err := json.Marshal(commandOutputBody{Output: "wrapped output\n"})
				if err != nil {
					t.Fatalf("marshal nested response: %v", err)
				}
				body, err := json.Marshal(map[string]any{
					"operation_id": "op-start",
					"state":        "done",
					"result":       json.RawMessage(nested),
				})
				if err != nil {
					t.Fatalf("marshal wrapped response: %v", err)
				}
				return protocol.ResponseEnvelope{
					ProtocolVersion: req.ProtocolVersion,
					RequestID:       req.RequestID,
					Kind:            protocol.EnvelopeKindResponse,
					OK:              true,
					Body:            body,
				}, nil
			},
		}),
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID: "proj",
	}

	output := captureStdout(t, func() error {
		return StartCommand(deps, "issue-1")
	})

	if output != "wrapped output\n" {
		t.Fatalf("output = %q, want wrapped output", output)
	}
}

func TestStartCommandPrintsPendingOperationState(t *testing.T) {
	deps := &Dependencies{
		Config: config.DefaultConfig(),
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				body, err := json.Marshal(map[string]any{
					"operation_id": "op-start",
					"state":        string(protocol.OperationStateQueued),
				})
				if err != nil {
					t.Fatalf("marshal wrapped response: %v", err)
				}
				return protocol.ResponseEnvelope{
					ProtocolVersion: req.ProtocolVersion,
					RequestID:       req.RequestID,
					Kind:            protocol.EnvelopeKindResponse,
					OK:              true,
					Body:            body,
				}, nil
			},
		}),
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID: "proj",
	}

	output := captureStdout(t, func() error {
		return StartCommand(deps, "issue-1")
	})

	if output != "Operation queued: op-start\n" {
		t.Fatalf("output = %q, want queued operation line", output)
	}
}

func TestStartCommandWithWaitPrintsTerminalOperationOutput(t *testing.T) {
	var calls int
	deps := &Dependencies{
		Config: config.DefaultConfig(),
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				calls++
				switch req.Command {
				case commandSessionStart:
					body, err := json.Marshal(map[string]any{
						"operation_id": "op-start",
						"state":        string(protocol.OperationStateQueued),
					})
					if err != nil {
						t.Fatalf("marshal pending response: %v", err)
					}
					return protocol.ResponseEnvelope{
						ProtocolVersion: req.ProtocolVersion,
						RequestID:       req.RequestID,
						Kind:            protocol.EnvelopeKindResponse,
						OK:              true,
						Body:            body,
					}, nil
				case protocol.CommandOperationGet:
					body, err := json.Marshal(protocol.OperationGetResponseBody{
						Operation: protocol.OperationRecord{
							OperationID: "op-start",
							ProjectID:   "proj",
							Kind:        commandSessionStart,
							State:       protocol.OperationStateDone,
							Result:      mustJSON(t, commandOutputBody{Output: "started\n"}),
						},
					})
					if err != nil {
						t.Fatalf("marshal get response: %v", err)
					}
					return protocol.ResponseEnvelope{
						ProtocolVersion: req.ProtocolVersion,
						RequestID:       req.RequestID,
						Kind:            protocol.EnvelopeKindResponse,
						OK:              true,
						Body:            body,
					}, nil
				default:
					t.Fatalf("unexpected command: %s", req.Command)
				}
				return protocol.ResponseEnvelope{}, nil
			},
		}),
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID: "proj",
	}

	output := captureStdout(t, func() error {
		return StartCommandWithOptions(deps, "issue-1", SessionCommandOptions{
			Wait:         true,
			PollInterval: time.Millisecond,
		})
	})

	if output != "started\n" {
		t.Fatalf("output = %q, want terminal operation output", output)
	}
	if calls != 2 {
		t.Fatalf("calls = %d, want 2", calls)
	}
}

func TestOperationCommandsParseAndRender(t *testing.T) {
	getOpts, err := ParseOperationGetArgs([]string{"--wait", "--poll-interval", "500ms", "op-1"})
	if err != nil {
		t.Fatalf("ParseOperationGetArgs error: %v", err)
	}
	if getOpts.OperationID != "op-1" || !getOpts.Wait || getOpts.PollInterval != 500*time.Millisecond {
		t.Fatalf("get opts = %+v", getOpts)
	}

	listOpts, err := ParseOperationListArgs([]string{"--issue", "az-1", "--kind", "session.start", "--state", "queued", "--states", "running", "--limit", "3"})
	if err != nil {
		t.Fatalf("ParseOperationListArgs error: %v", err)
	}
	if listOpts.IssueID != "az-1" || listOpts.Kind != "session.start" || listOpts.Limit != 3 || len(listOpts.States) != 2 {
		t.Fatalf("list opts = %+v", listOpts)
	}

	cancelOpts, err := ParseOperationCancelArgs([]string{"--reason", "user request", "op-1"})
	if err != nil {
		t.Fatalf("ParseOperationCancelArgs error: %v", err)
	}
	if cancelOpts.OperationID != "op-1" || cancelOpts.Reason != "user request" {
		t.Fatalf("cancel opts = %+v", cancelOpts)
	}
}

func TestOperationCommandsUseDaemonClient(t *testing.T) {
	deps := &Dependencies{
		Config: config.DefaultConfig(),
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				switch req.Command {
				case protocol.CommandOperationGet:
					return responseWithJSON(req, protocol.OperationGetResponseBody{
						Operation: protocol.OperationRecord{
							OperationID: "op-1",
							ProjectID:   "proj",
							Kind:        "session.start",
							State:       protocol.OperationStateRunning,
						},
					}), nil
				case protocol.CommandOperationList:
					return responseWithJSON(req, protocol.OperationListResponseBody{
						ProjectID: "proj",
						Operations: []protocol.OperationRecord{
							{
								OperationID: "op-1",
								ProjectID:   "proj",
								Kind:        "session.start",
								State:       protocol.OperationStateQueued,
							},
						},
					}), nil
				case protocol.CommandOperationCancel:
					return responseWithJSON(req, protocol.OperationCancelResponseBody{
						Cancelled: true,
						Operation: protocol.OperationRecord{
							OperationID: "op-1",
							ProjectID:   "proj",
							Kind:        "session.start",
							State:       protocol.OperationStateCancelled,
						},
					}), nil
				default:
					t.Fatalf("unexpected command: %s", req.Command)
				}
				return protocol.ResponseEnvelope{}, nil
			},
		}),
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID: "proj",
	}

	output := captureStdout(t, func() error {
		if err := OperationGetCommand(deps, OperationGetOptions{OperationID: "op-1"}); err != nil {
			return err
		}
		if err := OperationListCommand(deps, OperationListOptions{IssueID: "az-1", Limit: 5}); err != nil {
			return err
		}
		return OperationCancelCommand(deps, OperationCancelOptions{OperationID: "op-1"})
	})

	for _, needle := range []string{"ID", "STATE", "KIND", "op-1", "running", "queued", "cancelled"} {
		if !strings.Contains(output, needle) {
			t.Fatalf("output = %q, want %q", output, needle)
		}
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
			want: IssueListOptions{JSON: false, Deps: false, Limit: defaultIssueListLimit},
		},
		{
			name: "json output",
			args: []string{"--json"},
			want: IssueListOptions{JSON: true, Deps: false, Limit: defaultIssueListLimit},
		},
		{
			name: "deps projection",
			args: []string{"--deps"},
			want: IssueListOptions{JSON: false, Deps: true, Limit: defaultIssueListLimit},
		},
		{
			name: "limit override",
			args: []string{"--limit", "25"},
			want: IssueListOptions{JSON: false, Deps: false, Limit: 25},
		},
		{
			name: "id filters",
			args: []string{"--id", "az-1", "--id", "az-2", "--ids", "az-3,az-4"},
			want: IssueListOptions{JSON: false, Deps: false, Limit: defaultIssueListLimit, IDs: []string{"az-1", "az-2", "az-3", "az-4"}},
		},
		{
			name:        "invalid limit",
			args:        []string{"--limit", "0"},
			errContains: "limit must be >= 1",
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
			if !reflect.DeepEqual(got, tt.want) {
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
			want: IssueGetOptions{IssueID: "az-1", JSON: false, Deps: false},
		},
		{
			name: "json output",
			args: []string{"--json", "az-2"},
			want: IssueGetOptions{IssueID: "az-2", JSON: true, Deps: false},
		},
		{
			name: "deps projection",
			args: []string{"--deps", "az-3"},
			want: IssueGetOptions{IssueID: "az-3", JSON: false, Deps: true},
		},
		{
			name:        "missing issue id",
			args:        []string{},
			errContains: "usage: az issue get [--id <issue-id>] [--json] [--deps] [<issue-id>]",
		},
		{
			name:        "too many args",
			args:        []string{"az-1", "extra"},
			errContains: "usage: az issue get [--id <issue-id>] [--json] [--deps] [<issue-id>]",
		},
		{
			name: "named id",
			args: []string{"--id", "az-4"},
			want: IssueGetOptions{IssueID: "az-4", JSON: false, Deps: false},
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

func TestParseIssueCheckAndDoctorArgs(t *testing.T) {
	check, err := ParseIssueCheckArgs([]string{"--deps", "az-1"})
	if err != nil {
		t.Fatalf("ParseIssueCheckArgs() error = %v", err)
	}
	if check.IssueID != "az-1" || !check.Deps || check.JSON {
		t.Fatalf("ParseIssueCheckArgs() = %+v", check)
	}
	_, err = ParseIssueCheckArgs([]string{})
	if err == nil || !strings.Contains(err.Error(), "usage: az issue check [--id <issue-id>] [--json] [--deps] [<issue-id>]") {
		t.Fatalf("expected check usage error, got %v", err)
	}

	doctor, err := ParseIssueDoctorArgs([]string{"az-2"})
	if err != nil {
		t.Fatalf("ParseIssueDoctorArgs() error = %v", err)
	}
	if doctor.IssueID != "az-2" {
		t.Fatalf("ParseIssueDoctorArgs() = %+v", doctor)
	}
	doctor, err = ParseIssueDoctorArgs([]string{"--id", "az-3"})
	if err != nil {
		t.Fatalf("ParseIssueDoctorArgs() named id error = %v", err)
	}
	if doctor.IssueID != "az-3" {
		t.Fatalf("ParseIssueDoctorArgs() named id = %+v", doctor)
	}
	_, err = ParseIssueDoctorArgs([]string{})
	if err == nil || !strings.Contains(err.Error(), "usage: az issue doctor [--id <issue-id>] [<issue-id>]") {
		t.Fatalf("expected doctor usage error, got %v", err)
	}
}

func TestParseIssueGetManyArgs(t *testing.T) {
	got, err := ParseIssueGetManyArgs([]string{"--id", "az-1", "--id", "az-2", "--ids", "az-3,az-4", "--json"})
	if err != nil {
		t.Fatalf("ParseIssueGetManyArgs() error = %v", err)
	}
	if !got.JSON {
		t.Fatalf("expected json output flag to be set")
	}
	if !reflect.DeepEqual(got.IssueIDs, []string{"az-1", "az-2", "az-3", "az-4"}) {
		t.Fatalf("ParseIssueGetManyArgs() ids = %+v", got.IssueIDs)
	}

	_, err = ParseIssueGetManyArgs([]string{"az-1"})
	if err == nil || !strings.Contains(err.Error(), "unexpected argument: az-1") {
		t.Fatalf("expected positional arg rejection, got %v", err)
	}

	_, err = ParseIssueGetManyArgs([]string{"--json"})
	if err == nil || !strings.Contains(err.Error(), "usage: az issue get-many --id <issue-id>") {
		t.Fatalf("expected usage error for missing ids, got %v", err)
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
			args: []string{"--impl", "go-bubbletea", "Title"},
			want: IssueCreateOptions{
				Implementation: "go-bubbletea",
				Title:          "Title",
				Type:           domain.TypeTask,
				Priority:       domain.P2,
			},
		},
		{
			name: "explicit options",
			args: []string{"--impl", "go-bubbletea", "--type", "bug", "--priority", "P0", "--description", "details", "Title"},
			want: IssueCreateOptions{
				Implementation: "go-bubbletea",
				Title:          "Title",
				Description:    "details",
				Type:           domain.TypeBug,
				Priority:       domain.P0,
			},
		},
		{
			name:        "missing impl",
			args:        []string{"Title"},
			errContains: "missing required flag: --impl",
		},
		{
			name:        "invalid priority",
			args:        []string{"--impl", "go-bubbletea", "--priority", "high", "Title"},
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
			name:        "forbid impl",
			args:        []string{"--impl", "go-bubbletea", "az-1"},
			errContains: "--impl is not supported for issue close",
		},
		{
			name:        "missing id",
			args:        []string{},
			errContains: "usage: az issue close [--id <issue-id>] [<issue-id>]",
		},
		{
			name:        "extra args",
			args:        []string{"az-1", "extra"},
			errContains: "usage: az issue close [--id <issue-id>] [<issue-id>]",
		},
		{
			name: "named id",
			args: []string{"--id", "az-2"},
			want: IssueCloseOptions{IssueID: "az-2"},
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

func TestParseIssueDeleteArgs(t *testing.T) {
	got, err := ParseIssueDeleteArgs([]string{"--confirm", "az-1"})
	if err != nil {
		t.Fatalf("ParseIssueDeleteArgs() error = %v", err)
	}
	if got.IssueID != "az-1" || !got.Confirm {
		t.Fatalf("ParseIssueDeleteArgs() = %+v", got)
	}
	_, err = ParseIssueDeleteArgs([]string{"az-1"})
	if err == nil || !strings.Contains(err.Error(), "missing required flag: --confirm") {
		t.Fatalf("expected missing confirm error, got %v", err)
	}
	_, err = ParseIssueDeleteArgs([]string{"--impl", "go-bubbletea", "--confirm", "az-1"})
	if err == nil || !strings.Contains(err.Error(), "--impl is not supported for issue delete") {
		t.Fatalf("expected impl forbidden error, got %v", err)
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
			name:        "forbid impl",
			args:        []string{"--impl", "go-bubbletea", "--title", "Renamed", "az-1"},
			errContains: "--impl is not supported for issue update",
		},
		{
			name:        "no update fields",
			args:        []string{"az-1"},
			errContains: "no update fields provided",
		},
		{
			name:        "invalid status arg count",
			args:        []string{},
			errContains: "usage: az issue update [--id <issue-id>]",
		},
		{
			name: "named id",
			args: []string{"--id", "az-9", "--title", "Renamed"},
			want: IssueUpdateOptions{
				IssueID: "az-9",
				Title:   "Renamed",
			},
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

func TestParseIssueDependencyArgs(t *testing.T) {
	add, err := ParseIssueDependencyAddArgs([]string{"--type", "related", "az-1", "az-2"})
	if err != nil {
		t.Fatalf("ParseIssueDependencyAddArgs() error = %v", err)
	}
	if add.IssueID != "az-1" || add.DependsOnID != "az-2" || add.Type != "related" {
		t.Fatalf("ParseIssueDependencyAddArgs() = %+v", add)
	}
	_, err = ParseIssueDependencyAddArgs([]string{"--impl", "go-bubbletea", "az-1", "az-2"})
	if err == nil || !strings.Contains(err.Error(), "--impl is not supported for issue dep add") {
		t.Fatalf("expected impl forbidden error for add, got %v", err)
	}
	add, err = ParseIssueDependencyAddArgs([]string{"--issue-id", "az-1", "--depends-on-id", "az-2"})
	if err != nil {
		t.Fatalf("ParseIssueDependencyAddArgs() named flags error = %v", err)
	}
	if add.IssueID != "az-1" || add.DependsOnID != "az-2" {
		t.Fatalf("ParseIssueDependencyAddArgs() named flags = %+v", add)
	}

	remove, err := ParseIssueDependencyRemoveArgs([]string{"--type", "blocks", "--confirm", "az-3", "az-4"})
	if err != nil {
		t.Fatalf("ParseIssueDependencyRemoveArgs() error = %v", err)
	}
	if remove.IssueID != "az-3" || remove.DependsOnID != "az-4" || remove.Type != "blocks" || !remove.Confirm {
		t.Fatalf("ParseIssueDependencyRemoveArgs() = %+v", remove)
	}
	_, err = ParseIssueDependencyRemoveArgs([]string{"--impl", "go-bubbletea", "az-3"})
	if err == nil || !strings.Contains(err.Error(), "--impl is not supported for issue dep remove") {
		t.Fatalf("expected impl forbidden error for remove, got %v", err)
	}
	remove, err = ParseIssueDependencyRemoveArgs([]string{"--issue-id", "az-3", "--depends-on-id", "az-4"})
	if err != nil {
		t.Fatalf("ParseIssueDependencyRemoveArgs() named flags error = %v", err)
	}
	if remove.IssueID != "az-3" || remove.DependsOnID != "az-4" {
		t.Fatalf("ParseIssueDependencyRemoveArgs() named flags = %+v", remove)
	}
}

func TestParseIssueBulkArgs(t *testing.T) {
	create, err := ParseIssueBulkCreateArgs([]string{"--impl", "go-bubbletea", "--input", "bulk-create.json", "--dry-run"})
	if err != nil {
		t.Fatalf("ParseIssueBulkCreateArgs() error = %v", err)
	}
	if create.Implementation != "go-bubbletea" || create.InputPath != "bulk-create.json" || !create.DryRun {
		t.Fatalf("ParseIssueBulkCreateArgs() = %+v", create)
	}
	_, err = ParseIssueBulkCreateArgs([]string{"--input", "bulk-create.json"})
	if err == nil || !strings.Contains(err.Error(), "missing required flag: --impl") {
		t.Fatalf("expected missing impl error for bulk-create, got %v", err)
	}

	update, err := ParseIssueBulkUpdateArgs([]string{"--impl", "go-bubbletea", "--input", "bulk-update.json"})
	if err != nil {
		t.Fatalf("ParseIssueBulkUpdateArgs() error = %v", err)
	}
	if update.Implementation != "go-bubbletea" || update.InputPath != "bulk-update.json" || update.DryRun {
		t.Fatalf("ParseIssueBulkUpdateArgs() = %+v", update)
	}
	_, err = ParseIssueBulkUpdateArgs([]string{"--impl", "go-bubbletea"})
	if err == nil || !strings.Contains(err.Error(), "missing required flag: --input") {
		t.Fatalf("expected missing input error for bulk-update, got %v", err)
	}

	depBulk, err := ParseIssueDependencyBulkApplyArgs([]string{"--input", "dep-bulk.json", "--dry-run", "--json"})
	if err != nil {
		t.Fatalf("ParseIssueDependencyBulkApplyArgs() error = %v", err)
	}
	if depBulk.InputPath != "dep-bulk.json" || !depBulk.DryRun || !depBulk.JSON {
		t.Fatalf("ParseIssueDependencyBulkApplyArgs() = %+v", depBulk)
	}
	_, err = ParseIssueDependencyBulkApplyArgs([]string{"--impl", "go-bubbletea", "--input", "dep-bulk.json"})
	if err == nil || !strings.Contains(err.Error(), "--impl is not supported for issue dep bulk apply") {
		t.Fatalf("expected impl forbidden error for dep bulk apply, got %v", err)
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

func TestIssueListCommandDepsProjection(t *testing.T) {
	now := time.Date(2026, 3, 25, 10, 0, 0, 0, time.UTC)
	tasks := []domain.Task{
		{
			ID:       "az-1",
			Title:    "Dependent issue",
			Status:   domain.StatusInProgress,
			Priority: domain.P1,
			Type:     domain.TypeFeature,
			Dependencies: []domain.Dependency{
				{ID: "az-2", Type: domain.DependencyBlocks},
				{ID: "az-3", Type: domain.DependencyBlockedBy},
				{ID: "az-4", Type: domain.DependencyBlockedBy},
			},
			CreatedAt: now,
			UpdatedAt: now,
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
					Revision:        3,
					Body:            body,
				}, nil
			},
		}),
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID: "proj",
	}

	output := captureStdout(t, func() error {
		return IssueListCommand(deps, IssueListOptions{Deps: true})
	})
	if !strings.Contains(output, "DEPS") {
		t.Fatalf("deps output missing DEPS column: %q", output)
	}
	if !strings.Contains(output, "blocks:1,blocked-by:2") {
		t.Fatalf("deps output missing summary: %q", output)
	}
}

func TestIssueListCommand_IncludesListWindowMetadata(t *testing.T) {
	now := time.Date(2026, 3, 26, 2, 0, 0, 0, time.UTC)
	tasks := []domain.Task{
		{
			ID:        "az-1",
			Title:     "Newest issue",
			Status:    domain.StatusInProgress,
			Priority:  domain.P1,
			Type:      domain.TypeFeature,
			CreatedAt: now,
			UpdatedAt: now.Add(1 * time.Hour),
		},
		{
			ID:        "az-2",
			Title:     "Older issue",
			Status:    domain.StatusOpen,
			Priority:  domain.P2,
			Type:      domain.TypeTask,
			CreatedAt: now,
			UpdatedAt: now,
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
					Revision:        3,
					Body:            body,
				}, nil
			},
		}),
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID: "proj",
	}

	output := captureStdout(t, func() error {
		return IssueListCommand(deps, IssueListOptions{Limit: 1})
	})
	if !strings.Contains(output, "List window: listed=1 limit=1 total=2 truncated=yes") {
		t.Fatalf("list metadata missing expected window summary: %q", output)
	}
	if !strings.Contains(output, "Window note: additional matching issues may exist beyond current limit.") {
		t.Fatalf("list metadata missing truncated window note: %q", output)
	}
}

func TestIssueListCommand_IDSetFilter(t *testing.T) {
	now := time.Date(2026, 3, 26, 2, 0, 0, 0, time.UTC)
	tasks := []domain.Task{
		{ID: "az-1", Title: "One", Status: domain.StatusOpen, Priority: domain.P2, Type: domain.TypeTask, CreatedAt: now, UpdatedAt: now.Add(3 * time.Hour)},
		{ID: "az-2", Title: "Two", Status: domain.StatusOpen, Priority: domain.P2, Type: domain.TypeTask, CreatedAt: now, UpdatedAt: now.Add(2 * time.Hour)},
		{ID: "az-3", Title: "Three", Status: domain.StatusOpen, Priority: domain.P2, Type: domain.TypeTask, CreatedAt: now, UpdatedAt: now.Add(1 * time.Hour)},
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
					Revision:        2,
					Body:            body,
				}, nil
			},
		}),
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID: "proj",
	}

	output := captureStdout(t, func() error {
		return IssueListCommand(deps, IssueListOptions{IDs: []string{"az-3", "az-1"}})
	})
	if strings.Contains(output, "az-2") {
		t.Fatalf("id-set filter should exclude az-2: %q", output)
	}
	if !strings.Contains(output, "az-1") || !strings.Contains(output, "az-3") {
		t.Fatalf("id-set filter should include requested issues: %q", output)
	}
}

func TestIssueListCommandDepsProjection_IncludesTopLevelGraphContext(t *testing.T) {
	now := time.Date(2026, 3, 26, 2, 10, 0, 0, time.UTC)
	parentID := "az-parent"
	tasks := []domain.Task{
		{
			ID:        parentID,
			Title:     "Parent issue",
			Status:    domain.StatusOpen,
			Priority:  domain.P2,
			Type:      domain.TypeEpic,
			CreatedAt: now,
			UpdatedAt: now,
		},
		{
			ID:        "az-child",
			Title:     "Child issue",
			Status:    domain.StatusOpen,
			Priority:  domain.P2,
			Type:      domain.TypeTask,
			ParentID:  &parentID,
			CreatedAt: now,
			UpdatedAt: now,
		},
		{
			ID:       "az-dependent",
			Title:    "Depends on parent via blocks",
			Status:   domain.StatusOpen,
			Priority: domain.P2,
			Type:     domain.TypeTask,
			Dependencies: []domain.Dependency{
				{ID: parentID, Type: domain.DependencyBlocks},
			},
			CreatedAt: now,
			UpdatedAt: now,
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
					Revision:        3,
					Body:            body,
				}, nil
			},
		}),
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID: "proj",
	}

	output := captureStdout(t, func() error {
		return IssueListCommand(deps, IssueListOptions{Deps: true, Limit: 10})
	})
	lines := strings.Split(strings.TrimSpace(output), "\n")
	if len(lines) == 0 || !strings.HasPrefix(lines[0], "Top-level issues:") {
		t.Fatalf("deps output should start with top-level context, got: %q", output)
	}
	for _, want := range []string{
		"Top-level issues:",
		"Dependency links (listed issues):",
		"- az-child -> az-parent (parent-child)",
		"- az-dependent -> az-parent (blocks)",
		"List window: listed=3 limit=10 total=3 truncated=no",
		"Window note: all matching issues are shown.",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("deps graph context missing %q: %q", want, output)
		}
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

func TestIssueGetCommandDepsProjection(t *testing.T) {
	now := time.Date(2026, 3, 25, 11, 0, 0, 0, time.UTC)
	tasks := []domain.Task{
		{
			ID:          "az-8",
			Title:       "Dependency detail",
			Description: "Detail context",
			Status:      domain.StatusOpen,
			Priority:    domain.P2,
			Type:        domain.TypeTask,
			Dependencies: []domain.Dependency{
				{ID: "az-2", Type: domain.DependencyBlocks},
				{ID: "az-5", Type: domain.DependencyRelatedTo},
			},
			CreatedAt: now,
			UpdatedAt: now,
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
		return IssueGetCommand(deps, IssueGetOptions{IssueID: "az-8", Deps: true})
	})
	if !strings.Contains(output, "Dependency edges:") {
		t.Fatalf("deps output missing dependency section: %q", output)
	}
	if !strings.Contains(output, "- az-2 (blocks)") || !strings.Contains(output, "- az-5 (related)") {
		t.Fatalf("deps output missing dependency rows: %q", output)
	}
}

func TestIssueGetCommandDepsProjectionCanonicalTypes(t *testing.T) {
	now := time.Date(2026, 3, 25, 11, 30, 0, 0, time.UTC)
	tasks := []domain.Task{
		{
			ID:          "az-9",
			Title:       "Dependency matrix",
			Description: "Ensure deps output labels are canonical",
			Status:      domain.StatusOpen,
			Priority:    domain.P2,
			Type:        domain.TypeTask,
			Dependencies: []domain.Dependency{
				{ID: "az-a", Type: domain.DependencyBlocks},
				{ID: "az-b", Type: domain.DependencyParentChild},
				{ID: "az-c", Type: domain.DependencyRelatedTo},
				{ID: "az-d", Type: domain.DependencyDiscovered},
			},
			CreatedAt: now,
			UpdatedAt: now,
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
					Revision:        5,
					Body:            body,
				}, nil
			},
		}),
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID: "proj",
	}

	output := captureStdout(t, func() error {
		return IssueGetCommand(deps, IssueGetOptions{IssueID: "az-9", Deps: true})
	})
	if !strings.Contains(output, "Dependency edges:") {
		t.Fatalf("deps output missing dependency section: %q", output)
	}
	for _, want := range []string{
		"- az-a (blocks)",
		"- az-b (parent-child)",
		"- az-c (related)",
		"- az-d (discovered-from)",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("deps output missing %q: %q", want, output)
		}
	}
}

func TestIssueGetCommandDepsProjectionIncludesDependentsAndParentEdge(t *testing.T) {
	now := time.Date(2026, 3, 26, 1, 15, 0, 0, time.UTC)
	parentID := "az-parent"
	targetID := "az-target"
	childParentID := targetID
	tasks := []domain.Task{
		{
			ID:        parentID,
			Title:     "Parent issue",
			Status:    domain.StatusOpen,
			Priority:  domain.P2,
			Type:      domain.TypeEpic,
			CreatedAt: now,
			UpdatedAt: now,
		},
		{
			ID:        targetID,
			Title:     "Target issue",
			Status:    domain.StatusOpen,
			Priority:  domain.P2,
			Type:      domain.TypeTask,
			ParentID:  &parentID,
			CreatedAt: now,
			UpdatedAt: now,
		},
		{
			ID:        "az-child",
			Title:     "Child issue",
			Status:    domain.StatusOpen,
			Priority:  domain.P2,
			Type:      domain.TypeTask,
			ParentID:  &childParentID,
			CreatedAt: now,
			UpdatedAt: now,
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
					Revision:        6,
					Body:            body,
				}, nil
			},
		}),
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID: "proj",
	}

	output := captureStdout(t, func() error {
		return IssueGetCommand(deps, IssueGetOptions{IssueID: targetID, Deps: true})
	})
	for _, want := range []string{
		"Dependency edges:",
		"- az-parent (parent-child)",
		"Dependents:",
		"- az-child (parent-child)",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("deps projection missing %q: %q", want, output)
		}
	}
}

func TestIssueGetManyCommand_JSONStableOrderWithPartialMisses(t *testing.T) {
	now := time.Date(2026, 3, 26, 3, 15, 0, 0, time.UTC)
	tasks := []domain.Task{
		{ID: "az-1", Title: "First", Status: domain.StatusOpen, Priority: domain.P2, Type: domain.TypeTask, CreatedAt: now, UpdatedAt: now},
		{ID: "az-2", Title: "Second", Status: domain.StatusInProgress, Priority: domain.P1, Type: domain.TypeFeature, CreatedAt: now, UpdatedAt: now},
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
					Revision:        3,
					Body:            body,
				}, nil
			},
		}),
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID: "proj",
	}

	output := captureStdout(t, func() error {
		return IssueGetManyCommand(deps, IssueGetManyOptions{
			IssueIDs: []string{"az-2", "az-missing", "az-1"},
			JSON:     true,
		})
	})
	var got issueGetManyResult
	if err := json.Unmarshal([]byte(output), &got); err != nil {
		t.Fatalf("unmarshal get-many output: %v", err)
	}
	if got.Requested != 3 || got.Found != 2 || got.Missing != 1 {
		t.Fatalf("unexpected summary: %+v", got)
	}
	if len(got.Results) != 3 {
		t.Fatalf("results length = %d, want 3", len(got.Results))
	}
	if got.Results[0].ID != "az-2" || got.Results[0].Status != "found" {
		t.Fatalf("result[0] = %+v", got.Results[0])
	}
	if got.Results[1].ID != "az-missing" || got.Results[1].Status != "not_found" {
		t.Fatalf("result[1] = %+v", got.Results[1])
	}
	if got.Results[2].ID != "az-1" || got.Results[2].Status != "found" {
		t.Fatalf("result[2] = %+v", got.Results[2])
	}
}

func TestIssueDependencyBulkApplyCommand_DryRunOutcomes(t *testing.T) {
	now := time.Date(2026, 3, 26, 4, 0, 0, 0, time.UTC)
	tasks := []domain.Task{
		{
			ID:        "az-a",
			Title:     "A",
			Status:    domain.StatusOpen,
			Priority:  domain.P2,
			Type:      domain.TypeTask,
			CreatedAt: now,
			UpdatedAt: now,
			Dependencies: []domain.Dependency{
				{ID: "az-b", Type: domain.DependencyBlocks},
			},
		},
		{ID: "az-b", Title: "B", Status: domain.StatusOpen, Priority: domain.P2, Type: domain.TypeTask, CreatedAt: now, UpdatedAt: now},
		{ID: "az-c", Title: "C", Status: domain.StatusOpen, Priority: domain.P2, Type: domain.TypeTask, CreatedAt: now, UpdatedAt: now},
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

	tempDir := t.TempDir()
	inputPath := filepath.Join(tempDir, "dep-bulk.json")
	payload := `{
  "mutations": [
    {"action":"add","issue_id":"az-a","depends_on_id":"az-b","type":"blocks"},
    {"action":"add","issue_id":"az-a","depends_on_id":"az-c","type":"blocks"},
    {"action":"remove","issue_id":"az-a","depends_on_id":"az-z","type":"blocks"},
    {"action":"retarget","issue_id":"az-a","from_id":"az-b","to_id":"az-c","type":"blocks"},
    {"action":"add","issue_id":"az-missing","depends_on_id":"az-b","type":"blocks"}
  ]
}`
	if err := os.WriteFile(inputPath, []byte(payload), 0644); err != nil {
		t.Fatalf("write dep bulk file: %v", err)
	}

	output := captureStdout(t, func() error {
		return IssueDependencyBulkApplyCommand(deps, IssueDependencyBulkApplyOptions{
			InputPath: inputPath,
			DryRun:    true,
			JSON:      true,
		})
	})
	var result dependencyBulkResult
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		t.Fatalf("unmarshal dry-run output: %v", err)
	}
	if result.Summary.Requested != 5 || result.Summary.Planned != 2 || result.Summary.NoOp != 2 || result.Summary.Invalid != 1 {
		t.Fatalf("unexpected dry-run summary: %+v", result.Summary)
	}
	statuses := make([]string, 0, len(result.Outcomes))
	for _, outcome := range result.Outcomes {
		statuses = append(statuses, outcome.Status)
	}
	if !reflect.DeepEqual(statuses, []string{"no-op", "planned", "no-op", "planned", "invalid"}) {
		t.Fatalf("unexpected dry-run statuses: %+v", statuses)
	}
}

func TestIssueDependencyBulkApplyCommand_IdempotentReplayNoOp(t *testing.T) {
	now := time.Date(2026, 3, 26, 4, 45, 0, 0, time.UTC)
	tasks := []domain.Task{
		{
			ID:        "az-a",
			Title:     "A",
			Status:    domain.StatusOpen,
			Priority:  domain.P2,
			Type:      domain.TypeTask,
			CreatedAt: now,
			UpdatedAt: now,
		},
		{ID: "az-b", Title: "B", Status: domain.StatusOpen, Priority: domain.P2, Type: domain.TypeTask, CreatedAt: now, UpdatedAt: now},
	}
	applyCalls := 0
	deps := &Dependencies{
		Config: config.DefaultConfig(),
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				switch req.Command {
				case daemonclient.CommandTaskList:
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
						Revision:        5,
						Body:            body,
					}, nil
				case protocol.CommandTaskBulkApply:
					applyCalls++
					tasks[0].Dependencies = append(tasks[0].Dependencies, domain.Dependency{
						ID:   "az-b",
						Type: domain.DependencyBlocks,
					})
					return protocol.ResponseEnvelope{
						ProtocolVersion: req.ProtocolVersion,
						RequestID:       req.RequestID,
						Kind:            protocol.EnvelopeKindResponse,
						Meta:            req.Meta,
						OK:              true,
						CompletedAt:     req.SentAt,
						Revision:        6,
						Body:            []byte(`{}`),
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

	tempDir := t.TempDir()
	inputPath := filepath.Join(tempDir, "dep-bulk-apply.json")
	payload := `{"mutations":[{"action":"add","issue_id":"az-a","depends_on_id":"az-b","type":"blocks"}]}`
	if err := os.WriteFile(inputPath, []byte(payload), 0644); err != nil {
		t.Fatalf("write dep bulk file: %v", err)
	}

	first := captureStdout(t, func() error {
		return IssueDependencyBulkApplyCommand(deps, IssueDependencyBulkApplyOptions{
			InputPath: inputPath,
			JSON:      true,
		})
	})
	var firstResult dependencyBulkResult
	firstJSON := extractTrailingJSONResult(first)
	if err := json.Unmarshal([]byte(firstJSON), &firstResult); err != nil {
		t.Fatalf("unmarshal first result: %v", err)
	}
	if firstResult.Summary.Planned != 1 || firstResult.Summary.Applied != 1 || applyCalls != 1 {
		t.Fatalf("unexpected first apply result=%+v calls=%d", firstResult.Summary, applyCalls)
	}

	second := captureStdout(t, func() error {
		return IssueDependencyBulkApplyCommand(deps, IssueDependencyBulkApplyOptions{
			InputPath: inputPath,
			JSON:      true,
		})
	})
	var secondResult dependencyBulkResult
	secondJSON := extractTrailingJSONResult(second)
	if err := json.Unmarshal([]byte(secondJSON), &secondResult); err != nil {
		t.Fatalf("unmarshal second result: %v", err)
	}
	if secondResult.Summary.NoOp != 1 || secondResult.Summary.Applied != 0 || applyCalls != 1 {
		t.Fatalf("unexpected second apply result=%+v calls=%d", secondResult.Summary, applyCalls)
	}
}

func extractTrailingJSONResult(output string) string {
	needle := "{\n  \"dry_run\""
	idx := strings.LastIndex(output, needle)
	if idx < 0 {
		return output
	}
	return output[idx:]
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
					Implementation: "go-bubbletea",
					Title:          "New issue",
					Description:    "Context",
					Type:           domain.TypeFeature,
					Priority:       domain.P1,
				})
			},
			wantCommand: daemonclient.CommandTaskCreate,
			wantText:    "Created issue: az-42",
		},
		{
			name: "close",
			run: func(deps *Dependencies) error {
				return IssueCloseCommand(deps, IssueCloseOptions{
					IssueID: "az-9",
				})
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
			switch tt.name {
			case "create":
				var createReq daemonclient.TaskCreateParams
				if err := json.Unmarshal(gotReq.Body, &createReq); err != nil {
					t.Fatalf("unmarshal create body: %v", err)
				}
				if createReq.Title != "New issue" || createReq.Description != "Context" || createReq.Type != domain.TypeFeature || createReq.Priority != domain.P1 {
					t.Fatalf("create body = %+v", createReq)
				}
			case "close":
				var statusReq daemonclient.TaskStatusRequest
				if err := json.Unmarshal(gotReq.Body, &statusReq); err != nil {
					t.Fatalf("unmarshal request body: %v", err)
				}
				if statusReq.TaskID != "az-9" || statusReq.Status != domain.StatusDone {
					t.Fatalf("close body = %+v, want task_id=az-9 status=closed", statusReq)
				}
			}
			if !strings.Contains(output, tt.wantText) {
				t.Fatalf("output missing %q: %q", tt.wantText, output)
			}
		})
	}
}

func TestIssueCheckDoctorAndDeleteCommandsUseDaemonTaskCommands(t *testing.T) {
	now := time.Date(2026, 3, 25, 10, 0, 0, 0, time.UTC)
	var gotDeleteReq protocol.RequestEnvelope
	deps := &Dependencies{
		Config: config.DefaultConfig(),
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				switch req.Command {
				case daemonclient.CommandTaskList:
					body, err := json.Marshal([]domain.Task{
						{
							ID:          "az-1",
							Title:       "Check target",
							Description: "Desc",
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
				case daemonclient.CommandTaskDelete:
					gotDeleteReq = req
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

	checkOut := captureStdout(t, func() error {
		return IssueCheckCommand(deps, IssueCheckOptions{IssueID: "az-1", Deps: true})
	})
	if !strings.Contains(checkOut, "ID: az-1") {
		t.Fatalf("check output = %q", checkOut)
	}

	doctorOut := captureStdout(t, func() error {
		return IssueDoctorCommand(deps, IssueDoctorOptions{IssueID: "az-1"})
	})
	if !strings.Contains(doctorOut, "Doctor: OK az-1") {
		t.Fatalf("doctor output = %q", doctorOut)
	}

	deleteOut := captureStdout(t, func() error {
		return IssueDeleteCommand(deps, IssueDeleteOptions{
			IssueID: "az-1",
			Confirm: true,
		})
	})
	if gotDeleteReq.Command != daemonclient.CommandTaskDelete {
		t.Fatalf("delete command = %q, want %q", gotDeleteReq.Command, daemonclient.CommandTaskDelete)
	}
	if !strings.Contains(deleteOut, "Deleted issue: az-1") {
		t.Fatalf("delete output = %q", deleteOut)
	}
}

func TestIssueUpdateCommandUsesDaemonTaskUpdateCommand(t *testing.T) {
	now := time.Date(2026, 3, 25, 10, 0, 0, 0, time.UTC)
	var gotUpdateReq protocol.RequestEnvelope
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
}

func TestIssueDependencyCommandsUseDaemonTaskCommands(t *testing.T) {
	var gotAddReq protocol.RequestEnvelope
	var gotRemoveReq protocol.RequestEnvelope
	deps := &Dependencies{
		Config: config.DefaultConfig(),
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				switch req.Command {
				case daemonclient.CommandTaskDependencyAdd:
					gotAddReq = req
				case daemonclient.CommandTaskDependencyRemove:
					gotRemoveReq = req
				}
				return protocol.ResponseEnvelope{
					ProtocolVersion: req.ProtocolVersion,
					RequestID:       req.RequestID,
					Kind:            protocol.EnvelopeKindResponse,
					Meta:            req.Meta,
					OK:              true,
					CompletedAt:     req.SentAt,
				}, nil
			},
		}),
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID: "proj",
	}

	addOut := captureStdout(t, func() error {
		return IssueDependencyAddCommand(deps, IssueDependencyAddOptions{
			IssueID:     "az-5",
			DependsOnID: "az-2",
			Type:        "blocks",
		})
	})
	if gotAddReq.Command != daemonclient.CommandTaskDependencyAdd {
		t.Fatalf("add command = %q, want %q", gotAddReq.Command, daemonclient.CommandTaskDependencyAdd)
	}
	var addBody daemonclient.TaskDependencyParams
	if err := json.Unmarshal(gotAddReq.Body, &addBody); err != nil {
		t.Fatalf("unmarshal add body: %v", err)
	}
	if addBody.TaskID != "az-5" || addBody.DependsOnID != "az-2" || addBody.Type != "blocks" {
		t.Fatalf("add body = %+v", addBody)
	}
	if !strings.Contains(addOut, "Added dependency: az-5 --(blocks)--> az-2") {
		t.Fatalf("add output = %q", addOut)
	}

	removeOut := captureStdout(t, func() error {
		return IssueDependencyRemoveCommand(deps, IssueDependencyRemoveOptions{
			IssueID:     "az-5",
			DependsOnID: "az-2",
			Type:        "blocks",
			Confirm:     true,
		})
	})
	if gotRemoveReq.Command != daemonclient.CommandTaskDependencyRemove {
		t.Fatalf("remove command = %q, want %q", gotRemoveReq.Command, daemonclient.CommandTaskDependencyRemove)
	}
	var removeBody daemonclient.TaskDependencyRemoveParams
	if err := json.Unmarshal(gotRemoveReq.Body, &removeBody); err != nil {
		t.Fatalf("unmarshal remove body: %v", err)
	}
	if removeBody.TaskID != "az-5" || removeBody.DependsOnID != "az-2" || removeBody.Type != "blocks" || !removeBody.Confirm {
		t.Fatalf("remove body = %+v", removeBody)
	}
	if !strings.Contains(removeOut, "Removed dependency: az-5 --(blocks)--> az-2") {
		t.Fatalf("remove output = %q", removeOut)
	}
}

func TestIssueBulkCommandsUseApplyCommand(t *testing.T) {
	tempDir := t.TempDir()
	bulkCreatePath := filepath.Join(tempDir, "bulk-create.json")
	bulkUpdatePath := filepath.Join(tempDir, "bulk-update.json")
	if err := os.WriteFile(bulkCreatePath, []byte(`[{"title":"Bulk one","description":"Desc","type":"task","priority":"P2"}]`), 0o644); err != nil {
		t.Fatalf("write bulk-create file: %v", err)
	}
	if err := os.WriteFile(bulkUpdatePath, []byte(`[{"task_id":"az-1","title":"Renamed"}]`), 0o644); err != nil {
		t.Fatalf("write bulk-update file: %v", err)
	}

	var commands []protocol.RequestEnvelope
	deps := &Dependencies{
		Config: config.DefaultConfig(),
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			handshakeFn: func(context.Context, protocol.Hello) (protocol.HelloAck, error) {
				return protocol.HelloAck{Accepted: true}, nil
			},
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				commands = append(commands, req)
				switch req.Command {
				case daemonclient.CommandTaskList:
					body, err := json.Marshal([]domain.Task{{
						ID:          "az-1",
						Title:       "Old",
						Description: "Desc",
						Type:        domain.TypeTask,
						Priority:    domain.P2,
						Status:      domain.StatusOpen,
					}})
					if err != nil {
						t.Fatalf("marshal list response: %v", err)
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
				case protocol.CommandTaskBulkApply:
					body, err := json.Marshal(applyExecutionResultBody{
						Summary: applyExecutionSummaryBody{Total: 1, Succeeded: 1, Failed: 0},
					})
					if err != nil {
						t.Fatalf("marshal apply response: %v", err)
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

	_ = captureStdout(t, func() error {
		return IssueBulkCreateCommand(deps, IssueBulkCreateOptions{
			Implementation: "go-bubbletea",
			InputPath:      bulkCreatePath,
			DryRun:         false,
		})
	})
	_ = captureStdout(t, func() error {
		return IssueBulkUpdateCommand(deps, IssueBulkUpdateOptions{
			Implementation: "go-bubbletea",
			InputPath:      bulkUpdatePath,
			DryRun:         true,
		})
	})

	applyReqs := make([]protocol.RequestEnvelope, 0, 2)
	for _, req := range commands {
		if req.Command == protocol.CommandTaskBulkApply {
			applyReqs = append(applyReqs, req)
		}
	}
	if len(applyReqs) != 2 {
		t.Fatalf("bulk apply command count = %d, want 2", len(applyReqs))
	}
	var createBody protocol.ApplyRequestBody
	if err := json.Unmarshal(applyReqs[0].Body, &createBody); err != nil {
		t.Fatalf("unmarshal create apply body: %v", err)
	}
	if createBody.DryRun {
		t.Fatalf("create body dry_run = true, want false")
	}
	if len(createBody.Operations) != 1 || createBody.Operations[0].Command != daemonclient.CommandTaskCreate {
		t.Fatalf("create operations = %+v", createBody.Operations)
	}
	var updateBody protocol.ApplyRequestBody
	if err := json.Unmarshal(applyReqs[1].Body, &updateBody); err != nil {
		t.Fatalf("unmarshal update apply body: %v", err)
	}
	if !updateBody.DryRun {
		t.Fatalf("update body dry_run = false, want true")
	}
	if len(updateBody.Operations) != 1 || updateBody.Operations[0].Command != daemonclient.CommandTaskUpdate {
		t.Fatalf("update operations = %+v", updateBody.Operations)
	}
}

func TestIssueBulkUpdateCommand_DependencyRetargetBuildsApplyOps(t *testing.T) {
	tempDir := t.TempDir()
	bulkUpdatePath := filepath.Join(tempDir, "bulk-update-retarget.json")
	if err := os.WriteFile(
		bulkUpdatePath,
		[]byte(`[{"task_id":"az-1","dependency_retargets":[{"from_id":"az-old","to_id":"az-new","type":"blocks"}]}]`),
		0o644,
	); err != nil {
		t.Fatalf("write bulk-update file: %v", err)
	}

	var commands []protocol.RequestEnvelope
	deps := &Dependencies{
		Config: config.DefaultConfig(),
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				commands = append(commands, req)
				switch req.Command {
				case daemonclient.CommandTaskList:
					body, err := json.Marshal([]domain.Task{{
						ID:          "az-1",
						Title:       "Existing",
						Description: "Desc",
						Type:        domain.TypeTask,
						Priority:    domain.P2,
						Status:      domain.StatusOpen,
					}})
					if err != nil {
						t.Fatalf("marshal list response: %v", err)
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
				case protocol.CommandTaskBulkApply:
					body, err := json.Marshal(applyExecutionResultBody{
						Summary: applyExecutionSummaryBody{Total: 2, Succeeded: 2, Failed: 0},
					})
					if err != nil {
						t.Fatalf("marshal apply response: %v", err)
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

	_ = captureStdout(t, func() error {
		return IssueBulkUpdateCommand(deps, IssueBulkUpdateOptions{
			Implementation: "go-bubbletea",
			InputPath:      bulkUpdatePath,
			DryRun:         true,
		})
	})

	var applyReq *protocol.RequestEnvelope
	for i := range commands {
		if commands[i].Command == protocol.CommandTaskBulkApply {
			applyReq = &commands[i]
			break
		}
	}
	if applyReq == nil {
		t.Fatalf("expected bulk apply command")
	}
	var body protocol.ApplyRequestBody
	if err := json.Unmarshal(applyReq.Body, &body); err != nil {
		t.Fatalf("unmarshal apply body: %v", err)
	}
	if !body.DryRun {
		t.Fatalf("dry_run = false, want true")
	}
	if len(body.Operations) != 2 {
		t.Fatalf("operation count = %d, want 2", len(body.Operations))
	}
	if body.Operations[0].Command != daemonclient.CommandTaskDependencyRemove {
		t.Fatalf("operation[0] command = %q, want %q", body.Operations[0].Command, daemonclient.CommandTaskDependencyRemove)
	}
	if body.Operations[1].Command != daemonclient.CommandTaskDependencyAdd {
		t.Fatalf("operation[1] command = %q, want %q", body.Operations[1].Command, daemonclient.CommandTaskDependencyAdd)
	}

	var removeBody daemonclient.TaskDependencyRemoveParams
	if err := json.Unmarshal(body.Operations[0].Body, &removeBody); err != nil {
		t.Fatalf("unmarshal remove body: %v", err)
	}
	if removeBody.TaskID != "az-1" || removeBody.DependsOnID != "az-old" || removeBody.Type != "blocks" || !removeBody.Confirm {
		t.Fatalf("remove body = %+v", removeBody)
	}

	var addBody daemonclient.TaskDependencyParams
	if err := json.Unmarshal(body.Operations[1].Body, &addBody); err != nil {
		t.Fatalf("unmarshal add body: %v", err)
	}
	if addBody.TaskID != "az-1" || addBody.DependsOnID != "az-new" || addBody.Type != "blocks" {
		t.Fatalf("add body = %+v", addBody)
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
	if !strings.Contains(output, "issue list [--json] [--deps]") {
		t.Fatalf("usage missing issue list command: %q", output)
	}
	if !strings.Contains(output, "issue get [--id <id>] [--json] [--deps] [<id>]") {
		t.Fatalf("usage missing issue get command: %q", output)
	}
	if !strings.Contains(output, "issue get-many --id <id>") {
		t.Fatalf("usage missing issue get-many command: %q", output)
	}
	if !strings.Contains(output, "issue check [--id <id>] [--json] [--deps] [<id>]") {
		t.Fatalf("usage missing issue check command: %q", output)
	}
	if !strings.Contains(output, "issue doctor [--id <id>] [<id>]") {
		t.Fatalf("usage missing issue doctor command: %q", output)
	}
	if !strings.Contains(output, "issue create <title>") {
		t.Fatalf("usage missing issue create command: %q", output)
	}
	if !strings.Contains(output, "issue update [--id <id>] [<id>]") {
		t.Fatalf("usage missing issue update command: %q", output)
	}
	if strings.Contains(output, "issue status --impl <implementation>") {
		t.Fatalf("usage should not include issue status command: %q", output)
	}
	if !strings.Contains(output, "issue close [--id <id>] [<id>]") {
		t.Fatalf("usage missing issue close command: %q", output)
	}
	if !strings.Contains(output, "issue delete [--id <id>] [<id>] --confirm") {
		t.Fatalf("usage missing issue delete command: %q", output)
	}
	if !strings.Contains(output, "issue dep add --issue-id <issue-id> --depends-on-id <depends-on-id>") {
		t.Fatalf("usage missing issue dep add command: %q", output)
	}
	if !strings.Contains(output, "issue dep remove --issue-id <issue-id> --depends-on-id <depends-on-id>") {
		t.Fatalf("usage missing issue dep remove command: %q", output)
	}
	if !strings.Contains(output, "issue dep bulk apply --input <path>") {
		t.Fatalf("usage missing issue dep bulk apply command: %q", output)
	}
	if !strings.Contains(output, "issue bulk-create --impl <implementation> --input <path>") {
		t.Fatalf("usage missing issue bulk-create command: %q", output)
	}
	if !strings.Contains(output, "issue bulk-update --impl <implementation> --input <path>") {
		t.Fatalf("usage missing issue bulk-update command: %q", output)
	}
	if !strings.Contains(output, "prime") {
		t.Fatalf("usage missing prime command: %q", output)
	}
	if strings.Contains(output, "issue close --impl") || strings.Contains(output, "issue delete --impl") || strings.Contains(output, "issue dep add --impl") {
		t.Fatalf("usage should not include --impl for existing-issue commands: %q", output)
	}
	if !strings.Contains(output, "Argument ordering: place flags/options before positional arguments for deterministic parsing.") {
		t.Fatalf("usage missing canonical argument ordering hint: %q", output)
	}
}

func TestPrimeCommandWithoutIssueContext(t *testing.T) {
	t.Setenv("AZEDARACH_ISSUE_ID", "")

	output := captureStdout(t, func() error {
		return PrimeCommand(&Dependencies{})
	})

	if !strings.Contains(output, "Azedarach Session Primer") {
		t.Fatalf("prime output missing header: %q", output)
	}
	if !strings.Contains(output, "No active issue is preselected") {
		t.Fatalf("prime output missing no-issue guardrail: %q", output)
	}
	if !strings.Contains(output, "single-window fanout") {
		t.Fatalf("prime output missing orchestration shorthand: %q", output)
	}
	if !strings.Contains(output, "split work until each child issue is independently actionable and fits within a single subagent context window") {
		t.Fatalf("prime output missing subagent sizing guardrail: %q", output)
	}
	if !strings.Contains(output, "Daemon execution guardrails:") {
		t.Fatalf("prime output missing daemon execution guardrails section: %q", output)
	}
	if !strings.Contains(output, "The daemon runs outside your repo working directory") {
		t.Fatalf("prime output missing daemon cwd guardrail: %q", output)
	}
	if !strings.Contains(output, "Treat `az issue fanout`, `az issue fanout drift`, and `az mail send|list|watch` as daemon-authoritative flows.") {
		t.Fatalf("prime output missing daemon-authoritative fanout/mail guardrail: %q", output)
	}
}

func TestPrimeCommandWithActiveIssueContext(t *testing.T) {
	t.Setenv("AZEDARACH_ISSUE_ID", "az-1")
	now := time.Date(2026, 3, 26, 11, 0, 0, 0, time.UTC)

	deps := &Dependencies{
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				if req.Command != daemonclient.CommandTaskList {
					return protocol.ResponseEnvelope{
						ProtocolVersion: req.ProtocolVersion,
						RequestID:       req.RequestID,
						Kind:            protocol.EnvelopeKindResponse,
						Meta:            req.Meta,
						OK:              true,
						CompletedAt:     req.SentAt,
					}, nil
				}
				body, err := json.Marshal([]domain.Task{{
					ID:              "az-1",
					Title:           "Prime issue",
					Status:          domain.StatusOpen,
					Priority:        domain.P2,
					Type:            domain.TypeTask,
					Implementations: []string{"go-bubbletea"},
					CreatedAt:       now,
					UpdatedAt:       now,
					Dependencies: []domain.Dependency{
						{ID: "az-2", Type: domain.DependencyBlocks},
					},
				}})
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
					Body:            body,
				}, nil
			},
		}),
		ProjectID: "proj",
	}

	output := captureStdout(t, func() error {
		return PrimeCommand(deps)
	})

	if !strings.Contains(output, "Active issue context (AZEDARACH_ISSUE_ID=az-1)") {
		t.Fatalf("prime output missing active issue section: %q", output)
	}
	if !strings.Contains(output, "az-1: Prime issue [status=open priority=P2 type=task impl=go-bubbletea]") {
		t.Fatalf("prime output missing issue summary: %q", output)
	}
	if !strings.Contains(output, "Dependencies:\n- blocks: az-2") {
		t.Fatalf("prime output missing dependency summary: %q", output)
	}
}

func TestPrimeCommandQuestionFirstAndSpecBlock(t *testing.T) {
	t.Setenv("AZEDARACH_ISSUE_ID", "")
	t.Setenv("AZEDARACH_PRIME_MODE", "question-first")

	output := captureStdout(t, func() error {
		return PrimeCommand(&Dependencies{Config: &config.Config{Spec: config.SpecConfig{Enabled: true}}})
	})

	if !strings.Contains(output, "Question-first execution rules") {
		t.Fatalf("prime output missing question-first block: %q", output)
	}
	if !strings.Contains(output, "- Spec workflow:") {
		t.Fatalf("prime output missing spec workflow block: %q", output)
	}
	if !strings.Contains(output, "ALWAYS check `az spec` requirements/links before starting behavior work.") {
		t.Fatalf("prime output missing mandatory pre-work spec check guardrail: %q", output)
	}
	if !strings.Contains(output, "If implementation is not aligned with spec, update spec first, then implement.") {
		t.Fatalf("prime output missing spec-first update guardrail: %q", output)
	}
	if !strings.Contains(output, "Ensure implementation issue(s) are linked to relevant spec requirement(s) before execution.") {
		t.Fatalf("prime output missing issue/spec linking guardrail: %q", output)
	}
}

func TestPrimeCommandSpecBlockDisabled(t *testing.T) {
	t.Setenv("AZEDARACH_ISSUE_ID", "")

	output := captureStdout(t, func() error {
		return PrimeCommand(&Dependencies{Config: &config.Config{Spec: config.SpecConfig{Enabled: false}}})
	})

	if strings.Contains(output, "- Spec workflow:") {
		t.Fatalf("prime output should not include spec workflow block when disabled: %q", output)
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

func responseWithJSON(req protocol.RequestEnvelope, body any) protocol.ResponseEnvelope {
	payload, err := json.Marshal(body)
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

func mustJSON(t *testing.T, body any) json.RawMessage {
	t.Helper()

	payload, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal json body: %v", err)
	}
	return payload
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
