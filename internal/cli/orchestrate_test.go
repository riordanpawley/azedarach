package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/riordanpawley/azedarach/internal/client/daemonclient"
	"github.com/riordanpawley/azedarach/internal/config"
	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/naming"
	gitservice "github.com/riordanpawley/azedarach/internal/services/git"
)

const commandTaskClosePreflight = "task.close_preflight"

func TestParseOrchestrateStatusArgs(t *testing.T) {
	opts, err := ParseOrchestrateStatusArgs([]string{"--root", "az-123", "--since", "10", "--limit", "25", "--json", "--summary"})
	if err != nil {
		t.Fatalf("ParseOrchestrateStatusArgs error = %v", err)
	}
	if opts.RootIssueID != "az-123" {
		t.Fatalf("RootIssueID = %q, want az-123", opts.RootIssueID)
	}
	if opts.SinceSeq != 10 {
		t.Fatalf("SinceSeq = %d, want 10", opts.SinceSeq)
	}
	if opts.Limit != 25 {
		t.Fatalf("Limit = %d, want 25", opts.Limit)
	}
	if !opts.JSON {
		t.Fatal("JSON = false, want true")
	}
	if !opts.Summary {
		t.Fatal("Summary = false, want true")
	}
}

func TestParseOrchestrateStatusArgsRejectsSummaryFull(t *testing.T) {
	if _, err := ParseOrchestrateStatusArgs([]string{"--root", "az-123", "--summary", "--full"}); err == nil {
		t.Fatal("ParseOrchestrateStatusArgs succeeded, want --summary/--full conflict")
	}
}

func TestParseOrchestrateStatusArgs_RequiresRoot(t *testing.T) {
	if _, err := ParseOrchestrateStatusArgs([]string{"--since", "10"}); err == nil {
		t.Fatal("expected error for missing --root")
	}
}

func TestParseOrchestrateStartArgs_DefaultLimitAndIssues(t *testing.T) {
	opts, err := ParseOrchestrateStartArgs([]string{"--root", "az-123", "--issue", "az-3", "--issue", "az-2", "--issue", "az-3"})
	if err != nil {
		t.Fatalf("ParseOrchestrateStartArgs error = %v", err)
	}
	if opts.Limit != 3 {
		t.Fatalf("Limit = %d, want 3", opts.Limit)
	}
	if len(opts.IssueIDs) != 2 || opts.IssueIDs[0] != "az-2" || opts.IssueIDs[1] != "az-3" {
		t.Fatalf("IssueIDs = %+v, want [az-2 az-3]", opts.IssueIDs)
	}
}

func TestParseOrchestrateGroupArgs(t *testing.T) {
	opts, err := ParseOrchestrateGroupArgs([]string{"--root", "az-1", "--nested", "az-2", "--issue", "az-4", "--issue", "az-3", "--issue", "az-4", "--json"})
	if err != nil {
		t.Fatalf("ParseOrchestrateGroupArgs error = %v", err)
	}
	if opts.RootIssueID != "az-1" || opts.NestedIssueID != "az-2" || !opts.JSON {
		t.Fatalf("opts = %+v", opts)
	}
	if len(opts.IssueIDs) != 2 || opts.IssueIDs[0] != "az-3" || opts.IssueIDs[1] != "az-4" {
		t.Fatalf("IssueIDs = %+v, want [az-3 az-4]", opts.IssueIDs)
	}
}

func TestParseOrchestrateGroupArgs_RequiresNestedAndIssue(t *testing.T) {
	if _, err := ParseOrchestrateGroupArgs([]string{"--root", "az-1", "--issue", "az-3"}); err == nil {
		t.Fatal("expected error for missing --nested")
	}
	if _, err := ParseOrchestrateGroupArgs([]string{"--root", "az-1", "--nested", "az-2"}); err == nil {
		t.Fatal("expected error for missing --issue")
	}
}

func TestExpensiveSessionSyncInitCommandsDetectsKnownPatterns(t *testing.T) {
	commands := []string{
		"direnv allow",
		"pnpm type-check",
		"tsc --noEmit",
		"tsgo ./...",
		"nx affected:test",
		"go test ./...",
		"bun install",
		"npm run build",
		"pnpm run check:types",
	}
	got := expensiveSessionSyncInitCommands(commands)
	want := []string{
		"pnpm type-check",
		"tsc --noEmit",
		"tsgo ./...",
		"nx affected:test",
		"go test ./...",
		"bun install",
		"npm run build",
		"pnpm run check:types",
	}
	if len(got) != len(want) {
		t.Fatalf("expensive commands = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("expensive commands = %+v, want %+v", got, want)
		}
	}
}

func TestSessionInitCommandFanoutWarningsQuietForNonExpensiveOrSingleLaunch(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Session.SyncInitCommands = []string{"direnv allow", "az prime", "printf ready"}
	deps := &Dependencies{Config: cfg}
	if warnings := sessionInitCommandFanoutWarnings(deps, "az-1", 3); len(warnings) != 0 {
		t.Fatalf("warnings = %+v, want none for non-expensive init commands", warnings)
	}

	cfg.Session.SyncInitCommands = []string{"pnpm type-check"}
	if warnings := sessionInitCommandFanoutWarnings(deps, "az-1", 1); len(warnings) != 0 {
		t.Fatalf("warnings = %+v, want none for single-session start", warnings)
	}
}

func TestParseOrchestrateWatchArgs(t *testing.T) {
	opts, err := ParseOrchestrateWatchArgs([]string{"--root", "az-1", "--since", "12", "--once"})
	if err != nil {
		t.Fatalf("ParseOrchestrateWatchArgs error = %v", err)
	}
	if opts.RootIssueID != "az-1" || opts.SinceSeq != 12 || !opts.Once || !opts.Compact {
		t.Fatalf("opts = %+v", opts)
	}
	if !opts.JSONL {
		t.Fatalf("JSONL = false, want true")
	}
}

func TestParseOrchestrateWatchArgsVerboseSelectsFullFrame(t *testing.T) {
	opts, err := ParseOrchestrateWatchArgs([]string{"--root", "az-1", "--verbose"})
	if err != nil {
		t.Fatalf("ParseOrchestrateWatchArgs error = %v", err)
	}
	if opts.Compact || !opts.Full || !opts.Verbose {
		t.Fatalf("opts = %+v, want verbose full-frame output", opts)
	}
}

func TestParseOrchestrateWatchArgsRejectsExplicitCompactFull(t *testing.T) {
	if _, err := ParseOrchestrateWatchArgs([]string{"--root", "az-1", "--compact", "--full"}); err == nil {
		t.Fatal("ParseOrchestrateWatchArgs succeeded, want --compact/--full conflict")
	}
}

func TestParseOrchestrateMessageArgs(t *testing.T) {
	opts, err := ParseOrchestrateMessageArgs([]string{"--root", "az-1", "--issue", "az-2", "--body", "Proceed now", "--force-self-delivery", "--json"})
	if err != nil {
		t.Fatalf("ParseOrchestrateMessageArgs error = %v", err)
	}
	if opts.RootIssueID != "az-1" || opts.IssueID != "az-2" || opts.Body != "Proceed now" || opts.Type != "orchestrator-message" || !opts.ForceSelfDelivery || !opts.JSON {
		t.Fatalf("opts = %+v", opts)
	}
}

func TestWatchDaemonCommandRestartsAfterTransientSocketLoss(t *testing.T) {
	oldLauncher := newLauncher
	t.Cleanup(func() { newLauncher = oldLauncher })

	launcher := &fakeLauncher{}
	newLauncher = func(_, _ string) daemonStarter {
		return launcher
	}

	calls := 0
	got, err := watchDaemonCommand(&Dependencies{}, func(context.Context) (string, error) {
		calls++
		if !launcher.startCalled {
			return "", errors.New("dial unix /tmp/azedarach.sock: connect: connection refused")
		}
		return "reattached", nil
	})
	if err != nil {
		t.Fatalf("watchDaemonCommand error = %v", err)
	}
	if got != "reattached" {
		t.Fatalf("result = %q, want reattached", got)
	}
	if !launcher.startCalled {
		t.Fatal("launcher.Start was not called after transient transport loss")
	}
	if calls != 3 {
		t.Fatalf("calls = %d, want initial call, retry probe, and post-start call", calls)
	}
}

func TestOrchestrateWatchCommandExitsWhenParentReparentedBeforePolling(t *testing.T) {
	oldParentPID := currentParentPID
	oldParentPollInterval := watchParentPollInterval
	t.Cleanup(func() {
		currentParentPID = oldParentPID
		watchParentPollInterval = oldParentPollInterval
	})
	var parentChecks int32
	currentParentPID = func() int {
		if atomic.AddInt32(&parentChecks, 1) == 1 {
			return 42
		}
		return 1
	}
	watchParentPollInterval = time.Millisecond

	var mailWatchCalls int32
	deps := &Dependencies{
		RepoDir: "/repo",
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				switch req.Command {
				case daemonclient.CommandTaskGraphReadiness:
					return responseWithJSON(req, daemonclient.TaskGraphReadiness{
						RootIssueID: "az-root",
						Blocked:     map[string]string{},
					}), nil
				case protocol.CommandMailList:
					return responseWithJSON(req, []protocol.MailEvent{}), nil
				case protocol.CommandMailWatch:
					atomic.AddInt32(&mailWatchCalls, 1)
					return responseWithJSON(req, []protocol.MailEvent{}), nil
				default:
					t.Fatalf("unexpected daemon command: %s", req.Command)
					return protocol.ResponseEnvelope{}, nil
				}
			},
		}),
	}

	err := OrchestrateWatchCommand(deps, OrchestrateWatchOptions{
		RootIssueID:  "az-root",
		JSONL:        true,
		Compact:      true,
		PollInterval: 50 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("OrchestrateWatchCommand error = %v", err)
	}
	if got := atomic.LoadInt32(&mailWatchCalls); got != 0 {
		t.Fatalf("mail.watch calls after parent disappearance = %d, want 0", got)
	}
}

func TestOrchestrateWatchCommandCancelsInitialFrameWhenParentDisappears(t *testing.T) {
	oldParentPID := currentParentPID
	oldParentPollInterval := watchParentPollInterval
	t.Cleanup(func() {
		currentParentPID = oldParentPID
		watchParentPollInterval = oldParentPollInterval
	})
	var parentChecks int32
	currentParentPID = func() int {
		if atomic.AddInt32(&parentChecks, 1) == 1 {
			return 42
		}
		return 1
	}
	watchParentPollInterval = time.Millisecond

	taskGraphCalls := int32(0)
	deps := &Dependencies{
		RepoDir: "/repo",
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(ctx context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				if req.Command != daemonclient.CommandTaskGraphReadiness {
					t.Fatalf("unexpected daemon command: %s", req.Command)
				}
				atomic.AddInt32(&taskGraphCalls, 1)
				<-ctx.Done()
				return protocol.ResponseEnvelope{}, ctx.Err()
			},
		}),
	}

	err := OrchestrateWatchCommand(deps, OrchestrateWatchOptions{
		RootIssueID:  "az-root",
		JSONL:        true,
		Compact:      true,
		PollInterval: 50 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("OrchestrateWatchCommand error = %v", err)
	}
	if got := atomic.LoadInt32(&taskGraphCalls); got != 1 {
		t.Fatalf("task.graph_readiness calls = %d, want 1", got)
	}
}

func TestShouldContinueOrchestrateWatchAfterTimeout(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "short frame socket timeout",
			err:  errors.New("daemon command transport: short_frame: read unix ->/tmp/daemon.sock: i/o timeout"),
			want: true,
		},
		{
			name: "context deadline",
			err:  context.DeadlineExceeded,
			want: true,
		},
		{
			name: "ordinary failure",
			err:  errors.New("list tasks: invalid response"),
			want: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := shouldContinueOrchestrateWatchAfterError(tt.err); got != tt.want {
				t.Fatalf("shouldContinueOrchestrateWatchAfterError() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestBuildOrchestrateWatchFrameIncludesPendingAndCleanupMarkers(t *testing.T) {
	root := naming.IssueID("az-1")
	pending := naming.IssueID("az-2")
	closed := naming.IssueID("az-3")
	deps := &Dependencies{
		RepoDir:   "/repo",
		ProjectID: protocol.DefaultProjectID,
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				switch req.Command {
				case daemonclient.CommandTaskGraphReadiness:
					return responseWithJSON(req, daemonclient.TaskGraphReadiness{
						RootIssueID: root.String(),
						Blocked:     map[string]string{},
						Pending: []daemonclient.TaskPendingStart{
							{IssueID: pending.String(), OperationID: "op-start", OperationState: string(protocol.OperationStateRunning)},
						},
						ActiveSessions: []daemonclient.TaskActiveSession{
							{IssueID: closed.String(), Status: "cleanup-pending", Activity: "unknown", ActivitySource: "none", State: string(domain.SessionBusy), Advice: "closed issue az-3 still has session projection; cleanup is pending or stale"},
						},
					}), nil
				case protocol.CommandMailList:
					return responseWithJSON(req, []protocol.MailEvent{
						{Seq: 9, ParentIssue: root.String(), IssueID: pending, Type: "session-started"},
					}), nil
				case protocol.CommandOrchestrationSnapshot:
					return responseWithJSON(req, protocol.OrchestrationSnapshot{}), nil
				default:
					t.Fatalf("unexpected command: %s", req.Command)
				}
				return protocol.ResponseEnvelope{}, nil
			},
		}),
	}

	frame, err := buildOrchestrateWatchFrame(deps, root.String(), 7)
	if err != nil {
		t.Fatalf("buildOrchestrateWatchFrame error = %v", err)
	}
	if len(frame.Pending) != 1 || frame.Pending[0].IssueID != pending.String() || frame.Pending[0].OperationState != string(protocol.OperationStateRunning) {
		t.Fatalf("pending = %+v", frame.Pending)
	}
	if !strings.Contains(frame.PersistenceGuard, "Daemon-enforced") || !strings.Contains(frame.PersistenceGuard, "durable cursor") {
		t.Fatalf("persistence guard = %q", frame.PersistenceGuard)
	}
	if len(frame.ActiveSessions) != 1 || frame.ActiveSessions[0].Status != "cleanup-pending" {
		t.Fatalf("active_sessions = %+v", frame.ActiveSessions)
	}
	if frame.NextSince != 9 {
		t.Fatalf("next since = %d, want 9", frame.NextSince)
	}
}

func TestOrchestrateStatusCommandIncludesActiveSessionActivity(t *testing.T) {
	root := naming.IssueID("az-1")
	busy := naming.IssueID("az-2")
	unknown := naming.IssueID("az-3")
	noAgent := naming.IssueID("az-4")
	deps := &Dependencies{
		RepoDir:   "/repo",
		ProjectID: protocol.DefaultProjectID,
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				switch req.Command {
				case daemonclient.CommandTaskGraphReadiness:
					return responseWithJSON(req, daemonclient.TaskGraphReadiness{
						RootIssueID: root.String(),
						Blocked:     map[string]string{},
						Active:      []string{busy.String(), unknown.String(), noAgent.String()},
						ActiveSessions: []daemonclient.TaskActiveSession{
							{IssueID: busy.String(), Activity: "busy", ActivitySource: "hooks", State: string(domain.SessionBusy), TmuxAttachedCount: 1},
							{IssueID: unknown.String(), Activity: "unknown", ActivitySource: "none", State: string(domain.SessionBusy), TmuxAttachedCount: 1, Advice: "activity unknown: inspect hooks with az ai status --target=auto; run az ai install --target=auto only if hooks are missing, outdated, or not installed; use `az orchestrate capture --issue az-3` only if status/watch looks stale, failed, or contradictory"},
							{IssueID: noAgent.String(), Activity: "no-agent", ActivitySource: "session", State: string(domain.SessionBusy), TmuxAttachedCount: 1},
						},
					}), nil
				case daemonclient.CommandWorktreeList:
					return responseWithJSON(req, map[string]any{
						"project_id": protocol.DefaultProjectID,
						"worktrees":  []map[string]string{},
					}), nil
				case protocol.CommandMailList:
					return responseWithJSON(req, []protocol.MailEvent{}), nil
				default:
					t.Fatalf("unexpected command: %s", req.Command)
				}
				return protocol.ResponseEnvelope{}, nil
			},
		}),
	}

	output := captureStdout(t, func() error {
		return OrchestrateStatusCommand(deps, OrchestrateStatusOptions{RootIssueID: root.String(), JSON: true})
	})
	var result orchestrateStatusResult
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		t.Fatalf("decode output: %v\n%s", err, output)
	}
	if len(result.ActiveSessions) != 3 {
		t.Fatalf("active_sessions = %+v, want three entries", result.ActiveSessions)
	}
	byID := map[string]orchestrateActiveSession{}
	for _, active := range result.ActiveSessions {
		byID[active.IssueID] = active
	}
	if byID[busy.String()].Activity != "busy" || byID[busy.String()].ActivitySource != "hooks" || byID[busy.String()].Advice != "" {
		t.Fatalf("busy active session = %+v", byID[busy.String()])
	}
	if byID[unknown.String()].Activity != "unknown" || !strings.Contains(byID[unknown.String()].Advice, "az ai status --target=auto") || !strings.Contains(byID[unknown.String()].Advice, "az ai install --target=auto") {
		t.Fatalf("unknown active session = %+v", byID[unknown.String()])
	}
	if byID[noAgent.String()].Activity != "no-agent" || byID[noAgent.String()].ActivitySource != "session" || byID[noAgent.String()].Advice != "" {
		t.Fatalf("no-agent active session = %+v", byID[noAgent.String()])
	}
}

func TestOrchestrateStatusCommandPrintsContainmentRisks(t *testing.T) {
	root := naming.IssueID("fmd")
	active := naming.IssueID("fsy")
	deps := &Dependencies{
		RepoDir:   "/repo",
		ProjectID: protocol.DefaultProjectID,
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				switch req.Command {
				case daemonclient.CommandTaskGraphReadiness:
					return responseWithJSON(req, daemonclient.TaskGraphReadiness{
						RootIssueID: root.String(),
						Blocked:     map[string]string{},
						ContainmentRisks: []daemonclient.TaskContainmentRisk{{
							IssueID:                active.String(),
							RootIssueID:            root.String(),
							RootBranch:             "riordan/fmd/profile-and-worker-mater-cif-merge",
							ActiveBranch:           "riordan/fsy/reconcile",
							ClosedChildIssueID:     "frv",
							EvidenceCommit:         "67cc4c5cad123456",
							RootContainsEvidence:   true,
							ActiveContainsEvidence: false,
							Classification:         "stale_child_branch",
							Message:                "stale child branch: parent branch contains closed child evidence",
							OverlapFiles:           []string{"internal/rpc/materializer.go"},
							SuggestedCommand:       "merge or rebase parent before continuing",
						}},
					}), nil
				case daemonclient.CommandWorktreeList:
					return responseWithJSON(req, map[string]any{
						"project_id": protocol.DefaultProjectID,
						"worktrees":  []map[string]string{},
					}), nil
				case protocol.CommandMailList:
					return responseWithJSON(req, []protocol.MailEvent{}), nil
				default:
					t.Fatalf("unexpected command: %s", req.Command)
				}
				return protocol.ResponseEnvelope{}, nil
			},
		}),
	}

	output := captureStdout(t, func() error {
		return OrchestrateStatusCommand(deps, OrchestrateStatusOptions{RootIssueID: root.String()})
	})
	for _, want := range []string{
		"Containment risks:",
		"stale child branch",
		"child=frv commit=67cc4c5cad12 root_contains=true active_contains=false",
		"overlap: internal/rpc/materializer.go",
		"next: merge or rebase parent before continuing",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("output missing %q:\n%s", want, output)
		}
	}
}

func TestOrchestrateStatusCommandIncludesNestedRoots(t *testing.T) {
	root := naming.IssueID("az-1")
	nested := naming.IssueID("az-2")
	deps := &Dependencies{
		RepoDir:   "/repo",
		ProjectID: protocol.DefaultProjectID,
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				switch req.Command {
				case daemonclient.CommandTaskGraphReadiness:
					return responseWithJSON(req, daemonclient.TaskGraphReadiness{
						RootIssueID: root.String(),
						Capacity: daemonclient.TaskCapacitySummary{
							DirectRunnableCount:        1,
							NestedStartableCount:       1,
							TotalCountingCapacityCount: 0,
						},
						Runnable: []string{"az-3"},
						NestedRoots: []daemonclient.TaskNestedRoot{{
							IssueID:        nested.String(),
							Status:         "startable",
							IssueStatus:    string(domain.StatusOpen),
							Type:           string(domain.TypeTask),
							ChildCount:     2,
							FallbackPolicy: "start_nested_root",
							Advice:         "start nested root orchestrator: az session start az-2",
						}},
						Blocked: map[string]string{},
					}), nil
				case daemonclient.CommandWorktreeList:
					return responseWithJSON(req, map[string]any{
						"project_id": protocol.DefaultProjectID,
						"worktrees":  []map[string]string{},
					}), nil
				case protocol.CommandMailList:
					return responseWithJSON(req, []protocol.MailEvent{}), nil
				default:
					t.Fatalf("unexpected command: %s", req.Command)
				}
				return protocol.ResponseEnvelope{}, nil
			},
		}),
	}

	output := captureStdout(t, func() error {
		return OrchestrateStatusCommand(deps, OrchestrateStatusOptions{RootIssueID: root.String(), JSON: true})
	})
	var result orchestrateStatusResult
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		t.Fatalf("decode output: %v\n%s", err, output)
	}
	if len(result.NestedRoots) != 1 {
		t.Fatalf("nested_roots = %+v, want one nested root", result.NestedRoots)
	}
	got := result.NestedRoots[0]
	if got.IssueID != nested.String() || got.Status != "startable" || got.IssueStatus != string(domain.StatusOpen) || got.ChildCount != 2 || !strings.Contains(got.Advice, "az session start az-2") {
		t.Fatalf("nested root = %+v", got)
	}
	if result.Capacity.DirectRunnableCount != 1 || result.Capacity.NestedStartableCount != 1 {
		t.Fatalf("capacity = %+v", result.Capacity)
	}
}

func TestOrchestrateStatusCommandShowsNestedRoots(t *testing.T) {
	root := naming.IssueID("az-1")
	nested := naming.IssueID("az-2")
	deps := &Dependencies{
		RepoDir:   "/repo",
		ProjectID: protocol.DefaultProjectID,
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				switch req.Command {
				case daemonclient.CommandTaskGraphReadiness:
					return responseWithJSON(req, daemonclient.TaskGraphReadiness{
						RootIssueID: root.String(),
						Capacity: daemonclient.TaskCapacitySummary{
							NestedStartableCount: 1,
						},
						Runnable: []string{},
						NestedRoots: []daemonclient.TaskNestedRoot{{
							IssueID:        nested.String(),
							Status:         "startable",
							IssueStatus:    string(domain.StatusOpen),
							Type:           string(domain.TypeTask),
							ChildCount:     1,
							FallbackPolicy: "start_nested_root",
							Advice:         "start its orchestrator session with `az session start az-2`",
						}},
						Blocked: map[string]string{},
					}), nil
				case daemonclient.CommandWorktreeList:
					return responseWithJSON(req, map[string]any{
						"project_id": protocol.DefaultProjectID,
						"worktrees":  []map[string]string{},
					}), nil
				case protocol.CommandMailList:
					return responseWithJSON(req, []protocol.MailEvent{}), nil
				default:
					t.Fatalf("unexpected command: %s", req.Command)
				}
				return protocol.ResponseEnvelope{}, nil
			},
		}),
	}

	output := captureStdout(t, func() error {
		return OrchestrateStatusCommand(deps, OrchestrateStatusOptions{RootIssueID: root.String()})
	})
	for _, want := range []string{
		"Capacity:",
		"nested startable=1 active=0 blocked_start_failed=0 not_counting=0 total_counting=0",
		"Nested roots:",
		"- az-2 status=startable issue_status=open type=task children=1 fallback=start_nested_root",
		"next: start its orchestrator session with `az session start az-2`",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("output missing %q:\n%s", want, output)
		}
	}
}

func TestOrchestrateGroupCommandMovesChildUnderNestedRoot(t *testing.T) {
	root := naming.IssueID("az-1")
	nested := naming.IssueID("az-2")
	child := naming.IssueID("az-3")
	taskBody, err := marshalTaskListBody([]domain.Task{
		{ID: root, Title: "Root", Status: domain.StatusInProgress, Type: domain.TypeEpic},
		{ID: child, Title: "Worker", Status: domain.StatusOpen, Type: domain.TypeTask, ParentID: &root},
	})
	if err != nil {
		t.Fatalf("marshal task list: %v", err)
	}

	var requests []protocol.RequestEnvelope
	var addReq daemonclient.TaskDependencyParams
	readinessCalls := 0
	deps := &Dependencies{
		RepoDir:   "/repo",
		ProjectID: protocol.DefaultProjectID,
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				requests = append(requests, req)
				switch req.Command {
				case daemonclient.CommandTaskGraphReadiness:
					var body daemonclient.TaskIDRequest
					if err := json.Unmarshal(req.Body, &body); err != nil {
						t.Fatalf("decode graph readiness request: %v", err)
					}
					if body.TaskID != root {
						t.Fatalf("graph readiness task id = %s, want %s", body.TaskID, root)
					}
					readinessCalls++
					childCount := 1
					if readinessCalls > 1 {
						childCount = 2
					}
					return responseWithJSON(req, daemonclient.TaskGraphReadiness{
						RootIssueID: root.String(),
						NestedRoots: []daemonclient.TaskNestedRoot{{
							IssueID:        nested.String(),
							Status:         "startable",
							IssueStatus:    string(domain.StatusOpen),
							Type:           string(domain.TypeEpic),
							ChildCount:     childCount,
							FallbackPolicy: "start_nested_root",
							Advice:         "start nested root orchestrator: az session start az-2",
						}},
						Blocked: map[string]string{},
					}), nil
				case daemonclient.CommandTaskGet:
					var body daemonclient.TaskIDRequest
					if err := json.Unmarshal(req.Body, &body); err != nil {
						t.Fatalf("decode task.get request: %v", err)
					}
					if body.TaskID != child {
						t.Fatalf("task.get id = %s, want %s", body.TaskID, child)
					}
					return responseWithBody(req, taskBody), nil
				case daemonclient.CommandTaskDependencyAdd:
					if err := json.Unmarshal(req.Body, &addReq); err != nil {
						t.Fatalf("decode dependency add request: %v", err)
					}
					return responseWithJSON(req, map[string]any{}), nil
				default:
					t.Fatalf("unexpected command: %s", req.Command)
				}
				return protocol.ResponseEnvelope{}, nil
			},
		}),
	}

	output := captureStdout(t, func() error {
		return OrchestrateGroupCommand(deps, OrchestrateGroupOptions{
			RootIssueID:   root.String(),
			NestedIssueID: nested.String(),
			IssueIDs:      []string{child.String()},
			JSON:          true,
		})
	})
	if got, want := commandNames(requests), []string{
		daemonclient.CommandTaskGraphReadiness,
		daemonclient.CommandTaskGet,
		daemonclient.CommandTaskDependencyAdd,
		daemonclient.CommandTaskGraphReadiness,
	}; !slices.Equal(got, want) {
		t.Fatalf("commands = %+v, want %+v", got, want)
	}
	if addReq.TaskID != child || addReq.DependsOnID != nested || addReq.Type != string(domain.DependencyParentChild) || !addReq.ForceParentChange {
		t.Fatalf("dependency add request = %+v", addReq)
	}

	var result orchestrateGroupResult
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		t.Fatalf("decode output: %v\n%s", err, output)
	}
	if result.RootIssueID != root.String() || result.NestedIssueID != nested.String() || result.NestedRoot == nil {
		t.Fatalf("result = %+v", result)
	}
	if result.NestedRoot.ChildCount != 2 || result.NestedRoot.Status != "startable" {
		t.Fatalf("nested root = %+v", result.NestedRoot)
	}
	if len(result.Grouped) != 1 || result.Grouped[0].IssueID != child.String() || result.Grouped[0].PreviousParentID != root.String() || result.Grouped[0].NewParentID != nested.String() || !result.Grouped[0].Changed {
		t.Fatalf("grouped = %+v", result.Grouped)
	}
	if !slices.Contains(result.Advice, "inspect updated root: az orchestrate status --root az-1 --json") {
		t.Fatalf("advice = %+v", result.Advice)
	}
}

func TestOrchestrateGroupCommandRejectsRuntimeBackedChild(t *testing.T) {
	root := naming.IssueID("az-1")
	nested := naming.IssueID("az-2")
	child := naming.IssueID("az-3")
	taskBody, err := marshalTaskListBody([]domain.Task{
		{ID: child, Title: "Worker", Status: domain.StatusInProgress, Type: domain.TypeTask, ParentID: &root, HasTmuxSession: true},
	})
	if err != nil {
		t.Fatalf("marshal task list: %v", err)
	}

	var commands []string
	deps := &Dependencies{
		RepoDir:   "/repo",
		ProjectID: protocol.DefaultProjectID,
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				commands = append(commands, req.Command)
				switch req.Command {
				case daemonclient.CommandTaskGraphReadiness:
					return responseWithJSON(req, daemonclient.TaskGraphReadiness{
						RootIssueID: root.String(),
						NestedRoots: []daemonclient.TaskNestedRoot{{
							IssueID:        nested.String(),
							Status:         "startable",
							IssueStatus:    string(domain.StatusOpen),
							Type:           string(domain.TypeEpic),
							ChildCount:     1,
							FallbackPolicy: "start_nested_root",
						}},
						Blocked: map[string]string{},
					}), nil
				case daemonclient.CommandTaskGet:
					return responseWithBody(req, taskBody), nil
				case daemonclient.CommandTaskDependencyAdd:
					t.Fatalf("unexpected dependency add for runtime-backed child")
				default:
					t.Fatalf("unexpected command: %s", req.Command)
				}
				return protocol.ResponseEnvelope{}, nil
			},
		}),
	}

	_, err = captureStdoutAllowError(t, func() error {
		return OrchestrateGroupCommand(deps, OrchestrateGroupOptions{
			RootIssueID:   root.String(),
			NestedIssueID: nested.String(),
			IssueIDs:      []string{child.String()},
			JSON:          true,
		})
	})
	if err == nil || !strings.Contains(err.Error(), "has runtime/worktree state") {
		t.Fatalf("error = %v, want runtime/worktree rejection", err)
	}
	if !slices.Equal(commands, []string{daemonclient.CommandTaskGraphReadiness, daemonclient.CommandTaskGet}) {
		t.Fatalf("commands = %+v", commands)
	}
}

func TestOrchestrateGroupCommandRejectsUnparentedIssue(t *testing.T) {
	root := naming.IssueID("az-1")
	nested := naming.IssueID("az-2")
	child := naming.IssueID("az-3")
	taskBody, err := marshalTaskListBody([]domain.Task{
		{ID: child, Title: "Standalone", Status: domain.StatusOpen, Type: domain.TypeTask},
	})
	if err != nil {
		t.Fatalf("marshal task list: %v", err)
	}

	var commands []string
	deps := &Dependencies{
		RepoDir:   "/repo",
		ProjectID: protocol.DefaultProjectID,
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				commands = append(commands, req.Command)
				switch req.Command {
				case daemonclient.CommandTaskGraphReadiness:
					return responseWithJSON(req, daemonclient.TaskGraphReadiness{
						RootIssueID: root.String(),
						NestedRoots: []daemonclient.TaskNestedRoot{{
							IssueID:        nested.String(),
							Status:         "startable",
							IssueStatus:    string(domain.StatusOpen),
							Type:           string(domain.TypeEpic),
							ChildCount:     1,
							FallbackPolicy: "start_nested_root",
						}},
						Blocked: map[string]string{},
					}), nil
				case daemonclient.CommandTaskGet:
					return responseWithBody(req, taskBody), nil
				case daemonclient.CommandTaskDependencyAdd:
					t.Fatalf("unexpected dependency add for unparented issue")
				default:
					t.Fatalf("unexpected command: %s", req.Command)
				}
				return protocol.ResponseEnvelope{}, nil
			},
		}),
	}

	_, err = captureStdoutAllowError(t, func() error {
		return OrchestrateGroupCommand(deps, OrchestrateGroupOptions{
			RootIssueID:   root.String(),
			NestedIssueID: nested.String(),
			IssueIDs:      []string{child.String()},
			JSON:          true,
		})
	})
	if err == nil || !strings.Contains(err.Error(), "is not parented under root") {
		t.Fatalf("error = %v, want unparented rejection", err)
	}
	if !slices.Equal(commands, []string{daemonclient.CommandTaskGraphReadiness, daemonclient.CommandTaskGet}) {
		t.Fatalf("commands = %+v", commands)
	}
}

func TestOrchestrateStatusCommandIncludesWorkerObservations(t *testing.T) {
	root := naming.IssueID("az-1")
	review := naming.IssueID("az-2")
	observedAt := time.Date(2026, time.July, 6, 1, 30, 0, 0, time.UTC)
	originalNow := orchestrateObserveNow
	orchestrateObserveNow = func() time.Time { return observedAt.Add(90 * time.Minute) }
	t.Cleanup(func() { orchestrateObserveNow = originalNow })

	deps := &Dependencies{
		RepoDir:   "/repo",
		ProjectID: protocol.DefaultProjectID,
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				switch req.Command {
				case daemonclient.CommandTaskGraphReadiness:
					return responseWithJSON(req, daemonclient.TaskGraphReadiness{
						RootIssueID: root.String(),
						Blocked:     map[string]string{},
						WorkerObservations: []domain.WorkerObservation{{
							IssueID: review.String(),
							State:   domain.WorkerObservationReviewReady,
							Reason:  "issue is in_review",
							LastEvent: &domain.WorkerObservationEventSummary{
								Kind: "mailbox",
								Type: "worker-integration-ready",
								At:   observedAt,
								Seq:  12,
							},
							EvidenceSummary: []string{"mailbox worker-integration-ready: validation passed"},
							NextActions:     []string{"validate evidence, then close accepted worker: az issue close --id az-2"},
							SourceTruthPolicy: domain.WorkerObservationSourcePolicy{
								IssueGraph:      "projection",
								SessionRuntime:  "hybrid",
								MailboxEvidence: "projection",
							},
						}},
					}), nil
				case daemonclient.CommandWorktreeList:
					return responseWithJSON(req, map[string]any{
						"project_id": protocol.DefaultProjectID,
						"worktrees":  []map[string]string{},
					}), nil
				case protocol.CommandMailList:
					return responseWithJSON(req, []protocol.MailEvent{}), nil
				default:
					t.Fatalf("unexpected command: %s", req.Command)
				}
				return protocol.ResponseEnvelope{}, nil
			},
		}),
	}

	output := captureStdout(t, func() error {
		return OrchestrateStatusCommand(deps, OrchestrateStatusOptions{RootIssueID: root.String(), JSON: true})
	})
	var result orchestrateStatusResult
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		t.Fatalf("decode output: %v\n%s", err, output)
	}
	if len(result.WorkerObservations) != 1 {
		t.Fatalf("worker_observations = %+v", result.WorkerObservations)
	}
	observation := result.WorkerObservations[0]
	if observation.IssueID != review.String() || observation.State != string(domain.WorkerObservationReviewReady) || observation.Group != "review_ready" {
		t.Fatalf("observation = %+v", observation)
	}
	if observation.Age != "1h30m0s" || observation.AgeSeconds != 5400 {
		t.Fatalf("age = %q/%d, want 1h30m0s/5400", observation.Age, observation.AgeSeconds)
	}
	if !slices.Contains(observation.EvidenceFlags, "mailbox_event") || !slices.Contains(observation.EvidenceFlags, "next_actions") {
		t.Fatalf("evidence flags = %+v", observation.EvidenceFlags)
	}
}

func TestOrchestrateObserveCommandRendersGroupedText(t *testing.T) {
	root := naming.IssueID("az-1")
	waiting := naming.IssueID("az-2")
	review := naming.IssueID("az-3")
	observedAt := time.Date(2026, time.July, 6, 3, 0, 0, 0, time.UTC)
	originalNow := orchestrateObserveNow
	orchestrateObserveNow = func() time.Time { return observedAt.Add(2 * time.Hour) }
	t.Cleanup(func() { orchestrateObserveNow = originalNow })

	deps := &Dependencies{
		RepoDir:   "/repo",
		ProjectID: protocol.DefaultProjectID,
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				if req.Command != daemonclient.CommandTaskGraphReadiness {
					t.Fatalf("unexpected command: %s", req.Command)
				}
				return responseWithJSON(req, daemonclient.TaskGraphReadiness{
					RootIssueID: root.String(),
					WorkerObservations: []domain.WorkerObservation{
						{
							IssueID: waiting.String(),
							State:   domain.WorkerObservationWaitingHuman,
							Reason:  "active session is waiting for human input",
							LastEvent: &domain.WorkerObservationEventSummary{
								Kind: "issue_event",
								Type: "session.waiting",
								At:   observedAt,
							},
							NextActions:       []string{"inspect worker az-2 and answer the pending prompt"},
							SourceTruthPolicy: domain.WorkerObservationSourcePolicy{IssueGraph: "projection"},
						},
						{
							IssueID:           review.String(),
							State:             domain.WorkerObservationReviewReady,
							Reason:            "issue is in_review",
							EvidenceSummary:   []string{"status=in_review"},
							NextActions:       []string{"validate evidence, then close accepted worker: az issue close --id az-3"},
							SourceTruthPolicy: domain.WorkerObservationSourcePolicy{IssueGraph: "projection"},
						},
					},
					Blocked: map[string]string{},
				}), nil
			},
		}),
	}

	output := captureStdout(t, func() error {
		return OrchestrateObserveCommand(deps, OrchestrateObserveOptions{RootIssueID: root.String()})
	})
	needsHumanIdx := strings.Index(output, "Needs human:")
	reviewIdx := strings.Index(output, "Review-ready:")
	if needsHumanIdx < 0 || reviewIdx < 0 || needsHumanIdx > reviewIdx {
		t.Fatalf("output groups out of order:\n%s", output)
	}
	for _, want := range []string{
		"Root issue: az-1",
		"- az-2 state=waiting_human age=2h0m0s",
		"reason: active session is waiting for human input",
		"evidence flags: last_event, issue_event, next_actions",
		"next: inspect worker az-2 and answer the pending prompt",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("output missing %q:\n%s", want, output)
		}
	}
}

func TestOrchestrateObserveCommandJSONScopesToRoot(t *testing.T) {
	root := naming.IssueID("az-root")
	child := naming.IssueID("az-child")
	deps := &Dependencies{
		RepoDir:   "/repo",
		ProjectID: protocol.DefaultProjectID,
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				if req.Command != daemonclient.CommandTaskGraphReadiness {
					t.Fatalf("unexpected command: %s", req.Command)
				}
				if !strings.Contains(string(req.Body), root.String()) {
					t.Fatalf("task.graph_readiness body = %s, want root %s", string(req.Body), root.String())
				}
				return responseWithJSON(req, daemonclient.TaskGraphReadiness{
					RootIssueID: root.String(),
					WorkerObservations: []domain.WorkerObservation{{
						IssueID:           child.String(),
						State:             domain.WorkerObservationRunnable,
						Reason:            "leaf worker has no unresolved blockers or active runtime",
						NextActions:       []string{"az orchestrate start --root az-root --issue az-child --json"},
						SourceTruthPolicy: domain.WorkerObservationSourcePolicy{IssueGraph: "projection"},
					}},
					Blocked: map[string]string{},
				}), nil
			},
		}),
	}

	output := captureStdout(t, func() error {
		return OrchestrateObserveCommand(deps, OrchestrateObserveOptions{RootIssueID: root.String(), JSON: true})
	})
	var result orchestrateObserveResult
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		t.Fatalf("decode output: %v\n%s", err, output)
	}
	if result.Mode != "graph" || result.RootIssueID != root.String() {
		t.Fatalf("result scope = mode %q root %q", result.Mode, result.RootIssueID)
	}
	if len(result.Observations) != 1 || result.Observations[0].IssueID != child.String() || result.Observations[0].Group != "working" {
		t.Fatalf("observations = %+v", result.Observations)
	}
	if len(result.Groups) != 1 || result.Groups[0].Name != "working" || result.Groups[0].IssueIDs[0] != child.String() {
		t.Fatalf("groups = %+v", result.Groups)
	}
}

func TestObserveCommandWithoutRootCollectsActiveIssueObservations(t *testing.T) {
	active := naming.IssueID("az-active")
	worktree := naming.IssueID("az-worktree")
	openBacklog := naming.IssueID("az-backlog")
	taskListBody, err := marshalTaskListBody([]domain.Task{
		{ID: active, Title: "Active work", Status: domain.StatusInProgress, Type: domain.TypeTask},
		{ID: worktree, Title: "Open with worktree", Status: domain.StatusOpen, Type: domain.TypeTask, HasWorktree: true},
		{ID: openBacklog, Title: "Backlog", Status: domain.StatusOpen, Type: domain.TypeTask},
	})
	if err != nil {
		t.Fatalf("marshal task list: %v", err)
	}
	var graphRequests []string
	deps := &Dependencies{
		RepoDir:   "/repo",
		ProjectID: protocol.DefaultProjectID,
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				switch req.Command {
				case daemonclient.CommandTaskList:
					return protocol.ResponseEnvelope{ProtocolVersion: req.ProtocolVersion, RequestID: req.RequestID, Kind: protocol.EnvelopeKindResponse, Meta: req.Meta, OK: true, Body: taskListBody}, nil
				case daemonclient.CommandTaskGraphReadiness:
					body := string(req.Body)
					var issueID naming.IssueID
					switch {
					case strings.Contains(body, active.String()):
						issueID = active
					case strings.Contains(body, worktree.String()):
						issueID = worktree
					default:
						t.Fatalf("unexpected graph readiness body: %s", body)
					}
					graphRequests = append(graphRequests, issueID.String())
					return responseWithJSON(req, daemonclient.TaskGraphReadiness{
						RootIssueID: issueID.String(),
						WorkerObservations: []domain.WorkerObservation{{
							IssueID:           issueID.String(),
							State:             domain.WorkerObservationWorking,
							Reason:            "active worker session is present",
							EvidenceSummary:   []string{"status=in_progress"},
							NextActions:       []string{"watch worker activity for " + issueID.String()},
							SourceTruthPolicy: domain.WorkerObservationSourcePolicy{IssueGraph: "projection"},
						}},
						Blocked: map[string]string{},
					}), nil
				default:
					t.Fatalf("unexpected command: %s", req.Command)
				}
				return protocol.ResponseEnvelope{}, nil
			},
		}),
	}

	output := captureStdout(t, func() error {
		return ObserveCommand(deps, ObserveOptions{JSON: true})
	})
	var result orchestrateObserveResult
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		t.Fatalf("decode output: %v\n%s", err, output)
	}
	if result.Mode != "active" {
		t.Fatalf("mode = %q, want active", result.Mode)
	}
	if strings.Join(graphRequests, ",") != "az-active,az-worktree" {
		t.Fatalf("graph requests = %+v", graphRequests)
	}
	if len(result.Observations) != 2 {
		t.Fatalf("observations = %+v, want active and worktree", result.Observations)
	}
	for _, observation := range result.Observations {
		if observation.IssueID == openBacklog.String() {
			t.Fatalf("open backlog issue should not be observed: %+v", result.Observations)
		}
	}
}

func TestOrchestrateStatusCommandIncludesPendingAndCleanupMarkers(t *testing.T) {
	root := naming.IssueID("az-1")
	pending := naming.IssueID("az-2")
	closed := naming.IssueID("az-3")
	deps := &Dependencies{
		RepoDir:   "/repo",
		ProjectID: protocol.DefaultProjectID,
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				switch req.Command {
				case daemonclient.CommandTaskGraphReadiness:
					return responseWithJSON(req, daemonclient.TaskGraphReadiness{
						RootIssueID: root.String(),
						Blocked:     map[string]string{},
						Pending: []daemonclient.TaskPendingStart{
							{IssueID: pending.String(), OperationID: "op-start", OperationState: string(protocol.OperationStateQueued)},
						},
						ActiveSessions: []daemonclient.TaskActiveSession{
							{IssueID: closed.String(), Status: "cleanup-pending", Activity: "unknown", ActivitySource: "none", State: string(domain.SessionBusy), Advice: "closed issue az-3 still has session projection; cleanup is pending or stale"},
						},
					}), nil
				case daemonclient.CommandWorktreeList:
					return responseWithJSON(req, map[string]any{
						"project_id": protocol.DefaultProjectID,
						"worktrees":  []map[string]string{},
					}), nil
				case protocol.CommandMailList:
					return responseWithJSON(req, []protocol.MailEvent{}), nil
				default:
					t.Fatalf("unexpected command: %s", req.Command)
				}
				return protocol.ResponseEnvelope{}, nil
			},
		}),
	}

	output := captureStdout(t, func() error {
		return OrchestrateStatusCommand(deps, OrchestrateStatusOptions{RootIssueID: root.String(), JSON: true})
	})
	var result orchestrateStatusResult
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		t.Fatalf("decode output: %v\n%s", err, output)
	}
	if len(result.Pending) != 1 || result.Pending[0].IssueID != pending.String() || result.Pending[0].OperationID != "op-start" {
		t.Fatalf("pending = %+v", result.Pending)
	}
	if len(result.ActiveSessions) != 1 || result.ActiveSessions[0].Status != "cleanup-pending" || !strings.Contains(result.ActiveSessions[0].Advice, "cleanup is pending") {
		t.Fatalf("active_sessions = %+v", result.ActiveSessions)
	}
}

func TestOrchestrateStatusCommandWarnsOnExpensiveSessionInitFanout(t *testing.T) {
	root := naming.IssueID("az-1")
	childA := naming.IssueID("az-2")
	childB := naming.IssueID("az-3")
	taskListBody, err := marshalTaskListBody([]domain.Task{
		{ID: root, Title: "Root", Status: domain.StatusInProgress, Type: domain.TypeEpic},
		{ID: childA, Title: "Worker A", Status: domain.StatusOpen, Type: domain.TypeTask, ParentID: &root},
		{ID: childB, Title: "Worker B", Status: domain.StatusOpen, Type: domain.TypeTask, ParentID: &root},
	})
	if err != nil {
		t.Fatalf("marshal task list: %v", err)
	}
	cfg := config.DefaultConfig()
	cfg.Session.SyncInitCommands = []string{"pnpm type-check"}
	deps := &Dependencies{
		Config:    cfg,
		RepoDir:   "/repo",
		ProjectID: protocol.DefaultProjectID,
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				switch req.Command {
				case daemonclient.CommandTaskGraphReadiness:
					return responseWithJSON(req, daemonclient.TaskGraphReadiness{
						RootIssueID: root.String(),
						Runnable:    []string{childA.String(), childB.String()},
						Blocked:     map[string]string{},
					}), nil
				case daemonclient.CommandTaskList:
					return protocol.ResponseEnvelope{ProtocolVersion: req.ProtocolVersion, RequestID: req.RequestID, Kind: protocol.EnvelopeKindResponse, Meta: req.Meta, OK: true, Body: taskListBody}, nil
				case daemonclient.CommandWorktreeList:
					return responseWithJSON(req, map[string]any{
						"project_id": protocol.DefaultProjectID,
						"worktrees": []map[string]string{
							{"issue_id": root.String(), "path": "/repo-az-1", "branch": "user/az-1/root"},
						},
					}), nil
				case protocol.CommandMailList:
					return responseWithJSON(req, []protocol.MailEvent{}), nil
				default:
					t.Fatalf("unexpected command: %s", req.Command)
				}
				return protocol.ResponseEnvelope{}, nil
			},
		}),
	}

	output := captureStdout(t, func() error {
		return OrchestrateStatusCommand(deps, OrchestrateStatusOptions{RootIssueID: root.String(), JSON: true})
	})
	var result orchestrateStatusResult
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		t.Fatalf("decode output: %v\n%s", err, output)
	}
	warnings := strings.Join(result.Warnings, "\n")
	if !strings.Contains(warnings, "pnpm type-check") || !strings.Contains(warnings, "fanout count 2") {
		t.Fatalf("warnings = %+v, want expensive init command fanout guidance", result.Warnings)
	}
}

func TestOrchestrateStatusCommandIncludesSessionStartProgress(t *testing.T) {
	root := naming.IssueID("az-1")
	child := naming.IssueID("az-2")
	deps := &Dependencies{
		RepoDir:   "/repo",
		ProjectID: protocol.DefaultProjectID,
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				switch req.Command {
				case daemonclient.CommandTaskGraphReadiness:
					return responseWithJSON(req, daemonclient.TaskGraphReadiness{
						RootIssueID: root.String(),
						Runnable:    []string{},
						Blocked:     map[string]string{},
						SessionStartProgress: []daemonclient.TaskSessionStartProgress{
							{
								IssueID:        child.String(),
								OperationID:    "op-session-start",
								OperationState: "running",
								Phase:          "init_commands",
								Message:        "configured init commands likely running before agent hooks",
								Percent:        90,
								ElapsedMS:      12345,
							},
						},
					}), nil
				case daemonclient.CommandWorktreeList:
					return responseWithJSON(req, map[string]any{
						"project_id": protocol.DefaultProjectID,
						"worktrees":  []map[string]string{},
					}), nil
				case protocol.CommandMailList:
					return responseWithJSON(req, []protocol.MailEvent{}), nil
				default:
					t.Fatalf("unexpected command: %s", req.Command)
				}
				return protocol.ResponseEnvelope{}, nil
			},
		}),
	}

	output := captureStdout(t, func() error {
		return OrchestrateStatusCommand(deps, OrchestrateStatusOptions{RootIssueID: root.String(), JSON: true})
	})
	var result orchestrateStatusResult
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		t.Fatalf("decode output: %v\n%s", err, output)
	}
	if len(result.SessionStartProgress) != 1 {
		t.Fatalf("session_start_progress = %+v, want one entry", result.SessionStartProgress)
	}
	progress := result.SessionStartProgress[0]
	if progress.IssueID != child.String() || progress.OperationID != "op-session-start" || progress.Phase != "init_commands" || progress.ElapsedMS != 12345 {
		t.Fatalf("session start progress = %+v", progress)
	}
}

func TestOrchestrateStatusCommandWarnsWhenRootEpicLacksWorktree(t *testing.T) {
	root := naming.IssueID("az-1")
	child := naming.IssueID("az-2")
	body, err := marshalTaskListBody([]domain.Task{
		{ID: root, Title: "Root", Status: domain.StatusInProgress, Type: domain.TypeEpic},
		{ID: child, Title: "Worker", Status: domain.StatusOpen, Type: domain.TypeTask, ParentID: &root},
	})
	if err != nil {
		t.Fatalf("marshal task list: %v", err)
	}
	deps := &Dependencies{
		RepoDir:        "/repo",
		RuntimeRepoDir: "/repo-cif",
		ProjectID:      protocol.DefaultProjectID,
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				switch req.Command {
				case daemonclient.CommandTaskGraphReadiness:
					return responseWithJSON(req, daemonclient.TaskGraphReadiness{
						RootIssueID: root.String(),
						Runnable:    []string{child.String()},
						Blocked:     map[string]string{},
					}), nil
				case daemonclient.CommandTaskList:
					return protocol.ResponseEnvelope{ProtocolVersion: req.ProtocolVersion, RequestID: req.RequestID, Kind: protocol.EnvelopeKindResponse, Meta: req.Meta, OK: true, Body: body}, nil
				case daemonclient.CommandWorktreeList:
					return responseWithJSON(req, map[string]any{
						"project_id": protocol.DefaultProjectID,
						"worktrees": []map[string]string{
							{"issue_id": child.String(), "path": "/repo-az-2", "branch": "user/az-2/worker"},
						},
					}), nil
				case protocol.CommandMailList:
					return responseWithJSON(req, []protocol.MailEvent{}), nil
				default:
					t.Fatalf("unexpected command: %s", req.Command)
				}
				return protocol.ResponseEnvelope{}, nil
			},
		}),
	}

	output := captureStdout(t, func() error {
		return OrchestrateStatusCommand(deps, OrchestrateStatusOptions{RootIssueID: root.String(), JSON: true})
	})
	var result orchestrateStatusResult
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		t.Fatalf("decode output: %v\n%s", err, output)
	}
	if len(result.Warnings) != 1 ||
		!strings.Contains(result.Warnings[0], "root issue az-1 has child orchestration but no dedicated worktree") ||
		!strings.Contains(result.Warnings[0], "current worktree/path is /repo-cif") ||
		!strings.Contains(result.Warnings[0], "az worktree create az-1") {
		t.Fatalf("warnings = %+v", result.Warnings)
	}
}

func TestBuildOrchestrateWatchFrameIncludesActiveSessionActivity(t *testing.T) {
	root := naming.IssueID("az-1")
	idle := naming.IssueID("az-2")
	deps := &Dependencies{
		RepoDir:   "/repo",
		ProjectID: protocol.DefaultProjectID,
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				switch req.Command {
				case daemonclient.CommandTaskGraphReadiness:
					return responseWithJSON(req, daemonclient.TaskGraphReadiness{
						RootIssueID: root.String(),
						Blocked:     map[string]string{},
						Active:      []string{idle.String()},
						ActiveSessions: []daemonclient.TaskActiveSession{
							{IssueID: idle.String(), Activity: "idle", ActivitySource: "hooks", State: string(domain.SessionPaused), TmuxAttachedCount: 1},
						},
					}), nil
				case protocol.CommandMailList:
					return responseWithJSON(req, []protocol.MailEvent{{Seq: 7, ParentIssue: root.String(), IssueID: idle, Type: "worker-progress"}}), nil
				case protocol.CommandOrchestrationSnapshot:
					return responseWithJSON(req, protocol.OrchestrationSnapshot{}), nil
				default:
					t.Fatalf("unexpected command: %s", req.Command)
				}
				return protocol.ResponseEnvelope{}, nil
			},
		}),
	}

	frame, err := buildOrchestrateWatchFrame(deps, root.String(), 3)
	if err != nil {
		t.Fatalf("buildOrchestrateWatchFrame error = %v", err)
	}
	if len(frame.ActiveSessions) != 1 {
		t.Fatalf("active_sessions = %+v, want one entry", frame.ActiveSessions)
	}
	active := frame.ActiveSessions[0]
	if active.IssueID != idle.String() || active.Activity != "idle" || active.ActivitySource != "hooks" || active.Advice != "" {
		t.Fatalf("active session = %+v", active)
	}
}

func TestBuildOrchestrateWatchFrameIncludesSessionStartProgress(t *testing.T) {
	root := naming.IssueID("az-1")
	child := naming.IssueID("az-2")
	deps := &Dependencies{
		RepoDir:   "/repo",
		ProjectID: protocol.DefaultProjectID,
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				switch req.Command {
				case daemonclient.CommandTaskGraphReadiness:
					return responseWithJSON(req, daemonclient.TaskGraphReadiness{
						RootIssueID: root.String(),
						Blocked:     map[string]string{},
						SessionStartProgress: []daemonclient.TaskSessionStartProgress{
							{IssueID: child.String(), OperationID: "op-session-start", OperationState: "queued", Phase: "queued", Message: "queued session.start"},
						},
					}), nil
				case protocol.CommandMailList:
					return responseWithJSON(req, []protocol.MailEvent{}), nil
				default:
					t.Fatalf("unexpected command: %s", req.Command)
				}
				return protocol.ResponseEnvelope{}, nil
			},
		}),
	}

	frame, err := buildOrchestrateWatchFrame(deps, root.String(), 0)
	if err != nil {
		t.Fatalf("buildOrchestrateWatchFrame error = %v", err)
	}
	if len(frame.SessionStartProgress) != 1 {
		t.Fatalf("session_start_progress = %+v, want one entry", frame.SessionStartProgress)
	}
	progress := frame.SessionStartProgress[0]
	if progress.IssueID != child.String() || progress.OperationID != "op-session-start" || progress.Phase != "queued" {
		t.Fatalf("session start progress = %+v", progress)
	}
}

func TestEmitOrchestrateWatchFramePrintsSessionStartProgress(t *testing.T) {
	frame := orchestrateWatchFrame{
		RootIssueID: "az-1",
		Active:      []string{"az-2"},
		ActiveSessions: []orchestrateActiveSession{
			{
				IssueID:        "az-2",
				Activity:       "busy",
				ActivitySource: "session",
				StartProgress: &orchestrateSessionStartProgress{
					IssueID:        "az-2",
					OperationState: "running",
					Phase:          "init_commands",
					Message:        "configured init commands likely running before agent hooks",
					Percent:        90,
				},
			},
		},
		SessionStartProgress: []orchestrateSessionStartProgress{
			{
				IssueID:        "az-3",
				OperationID:    "op-session-start",
				OperationState: "queued",
				Phase:          "queued",
				Message:        "waiting for session.start resources",
			},
		},
		Blocked: map[string]string{},
	}

	output := captureStdout(t, func() error {
		return emitOrchestrateWatchFrame(frame, false, false)
	})
	for _, want := range []string{
		"start: state=running phase=init_commands progress=90% configured init commands likely running before agent hooks",
		"session start progress:",
		"az-3: state=queued phase=queued operation=op-session-start waiting for session.start resources",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("watch output missing %q:\n%s", want, output)
		}
	}
}

func TestEmitOrchestrateWatchFrameCompactSummarizesWorkerEvidence(t *testing.T) {
	frame := orchestrateWatchFrame{
		RootIssueID: "az-1",
		SinceSeq:    40,
		NextSince:   41,
		Runnable:    []string{"az-3"},
		ActiveSessions: []orchestrateActiveSession{
			{IssueID: "az-2", Status: "active", Activity: "busy", ActivitySource: "hooks"},
		},
		Blocked: map[string]string{"az-4": "blocked by az-5"},
		Events: []mailEvent{
			{
				Seq:     41,
				IssueID: "az-2",
				Type:    "worker-integration-ready",
				Body: `{
					"schema": "worker_evidence.v1",
					"summary": "Ready for integration after focused validation.",
					"commands_run": ["go test ./internal/cli"],
					"key_assertions": ["compact output omits full evidence body"],
					"files_changed": ["internal/cli/orchestrate.go"],
					"review": {"status": "clean", "findings": []},
					"risks": ["none"]
				}`,
			},
		},
	}

	output := captureStdout(t, func() error {
		return emitOrchestrateWatchFrame(frame, true, true)
	})
	if strings.Contains(output, "commands_run") || strings.Contains(output, "key_assertions") || strings.Contains(output, "files_changed") {
		t.Fatalf("compact output leaked full evidence body:\n%s", output)
	}
	var compact orchestrateCompactFrame
	if err := json.Unmarshal([]byte(output), &compact); err != nil {
		t.Fatalf("decode compact watch output: %v\n%s", err, output)
	}
	if compact.Capacity.Runnable != 1 || compact.Capacity.Active != 1 || compact.Capacity.Blocked != 1 {
		t.Fatalf("capacity = %+v", compact.Capacity)
	}
	if len(compact.Events) != 1 || compact.Events[0].WorkerEvidence == nil {
		t.Fatalf("events = %+v, want worker evidence summary", compact.Events)
	}
	evidence := compact.Events[0].WorkerEvidence
	if evidence.ValidationStatus != "complete" || evidence.Summary != "Ready for integration after focused validation." || evidence.ReviewStatus != "clean" || !slices.Contains(evidence.Risks, "none") {
		t.Fatalf("worker evidence = %+v", evidence)
	}
}

func TestCompactFrameFromStatusResultKeepsDefaultCompactWatchAdvice(t *testing.T) {
	result := orchestrateStatusResult{
		RootIssueID: "az-1",
		Blocked:     map[string]string{},
		Advice: map[string]interface{}{
			"watch": "az orchestrate watch --root az-1 --since 41 --jsonl",
		},
	}

	compact := compactFrameFromStatusResult(result, 40, 41)
	if got, _ := compact.Advice["watch"].(string); got != "az orchestrate watch --root az-1 --since 41 --jsonl" {
		t.Fatalf("watch advice = %q", got)
	}
}

func TestOrchestrateWatchSnapshotKeyIgnoresElapsedProgressTime(t *testing.T) {
	frame := orchestrateWatchFrame{
		Runnable: []string{"az-3"},
		Active:   []string{"az-2"},
		ActiveSessions: []orchestrateActiveSession{
			{
				IssueID:        "az-2",
				Activity:       "busy",
				ActivitySource: "session",
				StartProgress: &orchestrateSessionStartProgress{
					IssueID:        "az-2",
					OperationState: "running",
					Phase:          "init_commands",
					ElapsedMS:      1000,
				},
			},
		},
		SessionStartProgress: []orchestrateSessionStartProgress{
			{IssueID: "az-4", OperationID: "op-4", OperationState: "running", Phase: "worktree_preflight", ElapsedMS: 1000},
		},
		Blocked: map[string]string{},
	}
	first := orchestrateWatchFrameSnapshotKey(frame)
	frame.ActiveSessions[0].StartProgress.ElapsedMS = 5000
	frame.SessionStartProgress[0].ElapsedMS = 5000
	if second := orchestrateWatchFrameSnapshotKey(frame); second != first {
		t.Fatalf("snapshot key changed after elapsed-only update:\nfirst=%s\nsecond=%s", first, second)
	}
	frame.SessionStartProgress[0].Phase = "issue_resources"
	if second := orchestrateWatchFrameSnapshotKey(frame); second == first {
		t.Fatalf("snapshot key did not change after phase update")
	}
}

func TestOrchestrateWatchReadinessRefreshIntervalBounds(t *testing.T) {
	tests := []struct {
		name string
		poll time.Duration
		want time.Duration
	}{
		{name: "zero uses minimum", poll: 0, want: 2 * time.Second},
		{name: "default poll scales to minimum", poll: 250 * time.Millisecond, want: 2 * time.Second},
		{name: "larger poll scales", poll: time.Second, want: 8 * time.Second},
		{name: "large poll caps", poll: 2 * time.Second, want: 10 * time.Second},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := orchestrateWatchReadinessRefreshInterval(tt.poll); got != tt.want {
				t.Fatalf("refresh interval = %s, want %s", got, tt.want)
			}
		})
	}
}

func TestOrchestrateWatchReadinessCachePolicyDedupesQuietTicks(t *testing.T) {
	start := time.Date(2026, 6, 30, 10, 0, 0, 0, time.UTC)
	cache := newOrchestrateWatchReadinessCache(orchestrateWatchFrame{RootIssueID: "az-1"}, start, 250*time.Millisecond)

	refreshes := 1
	for i := 1; i <= 7; i++ {
		refresh, reason := cache.shouldRefresh(start.Add(time.Duration(i)*250*time.Millisecond), 0)
		if refresh {
			t.Fatalf("tick %d refreshed early with reason %q", i, reason)
		}
	}
	refresh, reason := cache.shouldRefresh(start.Add(2*time.Second), 0)
	if !refresh || reason != "refresh_interval_elapsed" {
		t.Fatalf("refresh at interval = %v reason=%q, want interval refresh", refresh, reason)
	}
	if refresh {
		refreshes++
	}
	if refreshes != 2 {
		t.Fatalf("refreshes across eight quiet ticks = %d, want initial plus one interval refresh", refreshes)
	}
	refresh, reason = cache.shouldRefresh(start.Add(250*time.Millisecond), 1)
	if !refresh || reason != "mailbox_events" {
		t.Fatalf("refresh with mailbox event = %v reason=%q, want mailbox refresh", refresh, reason)
	}
}

func TestOrchestrateWatchReadinessCacheBoundsChefyScaleQuietTicks(t *testing.T) {
	const tickCount = 2203
	pollInterval := 250 * time.Millisecond
	start := time.Date(2026, 6, 30, 10, 0, 0, 0, time.UTC)
	cache := newOrchestrateWatchReadinessCache(orchestrateWatchFrame{RootIssueID: "az-1"}, start, pollInterval)

	refreshes := 1
	lastRefresh := start
	for i := 1; i <= tickCount; i++ {
		now := start.Add(time.Duration(i) * pollInterval)
		cache.refreshedAt = lastRefresh
		refresh, _ := cache.shouldRefresh(now, 0)
		if refresh {
			refreshes++
			lastRefresh = now
		}
	}
	if refreshes >= tickCount/4 {
		t.Fatalf("readiness refreshes = %d across %d quiet ticks, want bounded coalescing", refreshes, tickCount)
	}
	wantMax := int((time.Duration(tickCount)*pollInterval)/cache.refreshInterval) + 2
	if refreshes > wantMax {
		t.Fatalf("readiness refreshes = %d, want <= %d for interval %s", refreshes, wantMax, cache.refreshInterval)
	}
}

func TestOrchestrateWatchReadinessCacheReusesSnapshotFields(t *testing.T) {
	cached := orchestrateWatchFrame{
		RootIssueID: "az-1",
		Runnable:    []string{"az-2"},
		Pending: []orchestratePendingStart{
			{IssueID: "az-3", OperationID: "op-1", OperationState: "queued"},
		},
		Active: []string{"az-4"},
		ActiveSessions: []orchestrateActiveSession{
			{IssueID: "az-4", Activity: "busy", ActivitySource: "hooks", State: "busy"},
		},
		NestedRoots: []orchestrateNestedRoot{{
			IssueID:    "az-nested",
			Status:     "open",
			Type:       "task",
			ChildCount: 1,
			ActiveSession: &orchestrateActiveSession{
				IssueID:        "az-nested",
				Activity:       "busy",
				ActivitySource: "hooks",
				StartProgress:  &orchestrateSessionStartProgress{IssueID: "az-nested", OperationID: "op-nested", OperationState: "running", ElapsedMS: 100},
			},
		}},
		SessionStartProgress: []orchestrateSessionStartProgress{
			{IssueID: "az-5", OperationID: "op-2", OperationState: "running", Phase: "worktree"},
		},
		StaleCloseableChildren: []orchestrateStaleCloseableCandidate{
			{IssueID: "az-6", Status: "in_review", Evidence: []string{"child closed with stale runtime"}},
		},
		Blocked: map[string]string{"az-7": "blocked by az-8"},
	}
	cache := newOrchestrateWatchReadinessCache(cached, time.Now(), 250*time.Millisecond)
	frame := cache.cachedReadinessFrame([]mailEvent{{Seq: 12, Type: "worker-progress"}}, 10, 12)

	if frame.RootIssueID != cached.RootIssueID || frame.SinceSeq != 10 || frame.NextSince != 12 {
		t.Fatalf("frame identifiers = %+v", frame)
	}
	if len(frame.Runnable) != 1 || frame.Runnable[0] != "az-2" {
		t.Fatalf("runnable = %+v", frame.Runnable)
	}
	if len(frame.Pending) != 1 || frame.Pending[0].IssueID != "az-3" {
		t.Fatalf("pending = %+v", frame.Pending)
	}
	if len(frame.ActiveSessions) != 1 || frame.ActiveSessions[0].IssueID != "az-4" {
		t.Fatalf("active sessions = %+v", frame.ActiveSessions)
	}
	if len(frame.NestedRoots) != 1 || frame.NestedRoots[0].IssueID != "az-nested" {
		t.Fatalf("nested roots = %+v", frame.NestedRoots)
	}
	if len(frame.SessionStartProgress) != 1 || frame.SessionStartProgress[0].IssueID != "az-5" {
		t.Fatalf("session start progress = %+v", frame.SessionStartProgress)
	}
	if len(frame.StaleCloseableChildren) != 1 || frame.StaleCloseableChildren[0].IssueID != "az-6" {
		t.Fatalf("stale closeable children = %+v", frame.StaleCloseableChildren)
	}
	if frame.Blocked["az-7"] != "blocked by az-8" {
		t.Fatalf("blocked = %+v", frame.Blocked)
	}
	if len(frame.Events) != 1 || frame.Events[0].Seq != 12 {
		t.Fatalf("events = %+v", frame.Events)
	}

	second := cached
	second.NestedRoots = append([]orchestrateNestedRoot(nil), cached.NestedRoots...)
	second.NestedRoots[0].ActiveSession = &orchestrateActiveSession{
		IssueID:        "az-nested",
		Activity:       "busy",
		ActivitySource: "hooks",
		StartProgress:  &orchestrateSessionStartProgress{IssueID: "az-nested", OperationID: "op-nested", OperationState: "running", ElapsedMS: 900},
	}
	if orchestrateWatchFrameSnapshotKey(cached) != orchestrateWatchFrameSnapshotKey(second) {
		t.Fatal("snapshot key changed when only nested root elapsed time changed")
	}
}

func TestParseOrchestrateCompleteCheckArgs(t *testing.T) {
	opts, err := ParseOrchestrateCompleteCheckArgs([]string{"--root", "az-1", "--json"})
	if err != nil {
		t.Fatalf("ParseOrchestrateCompleteCheckArgs error = %v", err)
	}
	if opts.RootIssueID != "az-1" || !opts.JSON {
		t.Fatalf("opts = %+v", opts)
	}
}

func TestParseOrchestratePromptArgs(t *testing.T) {
	opts, err := ParseOrchestratePromptArgs([]string{"--root", "az-1", "--issue", "az-2", "--coordination", "mailbox", "--json"})
	if err != nil {
		t.Fatalf("ParseOrchestratePromptArgs error = %v", err)
	}
	if opts.RootIssueID != "az-1" || opts.IssueID != "az-2" || opts.Coordination != "mailbox" || !opts.JSON {
		t.Fatalf("opts = %+v", opts)
	}
	defaults, err := ParseOrchestratePromptArgs([]string{"--issue", "az-2"})
	if err != nil {
		t.Fatalf("ParseOrchestratePromptArgs default error = %v", err)
	}
	if defaults.Coordination != "native" {
		t.Fatalf("default Coordination = %q, want native", defaults.Coordination)
	}
	if _, err := ParseOrchestratePromptArgs([]string{"--root", "az-1"}); err == nil {
		t.Fatal("expected prompt error for missing --issue")
	}
	if _, err := ParseOrchestratePromptArgs([]string{"--issue", "az-2", "--coordination", "banana"}); err == nil {
		t.Fatal("expected prompt error for invalid coordination")
	}
}

func TestParseOrchestrateIntegrateAndCloseSessionArgs(t *testing.T) {
	integrate, err := ParseOrchestrateIntegrateArgs([]string{"--issue", "az-2", "--apply", "--json"})
	if err != nil {
		t.Fatalf("ParseOrchestrateIntegrateArgs error = %v", err)
	}
	if integrate.IssueID != "az-2" || !integrate.Apply || !integrate.JSON {
		t.Fatalf("integrate opts = %+v", integrate)
	}

	closeSession, err := ParseOrchestrateCloseSessionArgs([]string{"--issue", "az-2", "--json"})
	if err != nil {
		t.Fatalf("ParseOrchestrateCloseSessionArgs error = %v", err)
	}
	if closeSession.IssueID != "az-2" || !closeSession.JSON {
		t.Fatalf("close-session opts = %+v", closeSession)
	}

	if _, err := ParseOrchestrateIntegrateArgs([]string{"--json"}); err == nil {
		t.Fatal("expected integrate error for missing --issue")
	}
	if _, err := ParseOrchestrateCloseSessionArgs([]string{"--json"}); err == nil {
		t.Fatal("expected close-session error for missing --issue")
	}
}

func TestOrchestrateStartSubmitsOperationAndWarnsOnDirtyParent(t *testing.T) {
	root := naming.IssueID("az-1")
	child := naming.IssueID("az-2")
	tasks := []domain.Task{
		{ID: root, Title: "Root", Status: domain.StatusInProgress, Type: domain.TypeEpic},
		{ID: child, Title: "Worker", Status: domain.StatusOpen, Type: domain.TypeTask, ParentID: &root},
	}
	taskListBody, err := marshalTaskListBody(tasks)
	if err != nil {
		t.Fatalf("marshal task list: %v", err)
	}

	commands := []string{}
	var submitted protocol.OperationSubmitRequestBody
	deps := &Dependencies{
		ProjectID: protocol.DefaultProjectID,
		RepoDir:   "/repo",
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				commands = append(commands, req.Command)
				switch req.Command {
				case daemonclient.CommandTaskGraphReadiness:
					return responseWithJSON(req, daemonclient.TaskGraphReadiness{
						RootIssueID: root.String(),
						Runnable:    []string{child.String()},
						Blocked:     map[string]string{},
					}), nil
				case daemonclient.CommandTaskList:
					return protocol.ResponseEnvelope{ProtocolVersion: req.ProtocolVersion, RequestID: req.RequestID, Kind: protocol.EnvelopeKindResponse, Meta: req.Meta, OK: true, Body: taskListBody}, nil
				case daemonclient.CommandTaskGetMany:
					assertMetadataOnlyTaskGetManyRequest(t, req, child.String())
					return protocol.ResponseEnvelope{ProtocolVersion: req.ProtocolVersion, RequestID: req.RequestID, Kind: protocol.EnvelopeKindResponse, Meta: req.Meta, OK: true, Body: taskListBody}, nil
				case daemonclient.CommandTaskMergeBaseTarget:
					return responseWithJSON(req, daemonclient.TaskMergeBaseTarget{IssueID: child.String(), TargetID: "base", Branch: "main"}), nil
				case daemonclient.CommandGitStatus:
					return responseWithJSON(req, map[string]any{"status": gitservice.GitStatus{Modified: []string{"parent.go"}}}), nil
				case daemonclient.CommandWorktreeList:
					return responseWithJSON(req, map[string]any{
						"project_id": protocol.DefaultProjectID,
						"worktrees": []map[string]string{
							{"issue_id": "az-2", "path": "/repo-az-2", "branch": "user/az-2/worker"},
						},
					}), nil
				case daemonclient.CommandTaskClaimOwnership:
					return responseWithTaskOwnershipMutation(t, req), nil
				case protocol.CommandOperationSubmit:
					if err := json.Unmarshal(req.Body, &submitted); err != nil {
						t.Fatalf("decode submit body: %v", err)
					}
					return responseWithJSON(req, protocol.OperationSubmitResponseBody{
						Created: true,
						Operation: protocol.OperationRecord{
							OperationID: "op-1",
							ProjectID:   protocol.DefaultProjectID,
							Kind:        commandSessionStart,
							IssueID:     child,
							State:       protocol.OperationStateQueued,
						},
					}), nil
				case protocol.CommandOperationGet:
					return responseWithJSON(req, protocol.OperationGetResponseBody{
						Operation: protocol.OperationRecord{
							OperationID: "op-1",
							ProjectID:   protocol.DefaultProjectID,
							Kind:        commandSessionStart,
							IssueID:     child,
							State:       protocol.OperationStateDone,
						},
					}), nil
				case protocol.CommandMailSend:
					t.Fatal("orchestrate start should not send session-started mail before operation completion is observed")
				default:
					t.Fatalf("unexpected command: %s", req.Command)
				}
				return protocol.ResponseEnvelope{}, nil
			},
		}),
	}

	var stderr string
	output := captureStdout(t, func() error {
		stderr = captureStderr(t, func() error {
			return OrchestrateStartCommand(deps, OrchestrateStartOptions{RootIssueID: "az-1", IssueIDs: []string{"az-2"}, Limit: 4, JSON: true})
		})
		return nil
	})
	for _, want := range []string{
		"orchestrate start: submitted az-2 operation=op-1 state=queued",
		"orchestrate start: launched az-2 operation=op-1 state=done",
	} {
		if !strings.Contains(stderr, want) {
			t.Fatalf("stderr = %q, want %q", stderr, want)
		}
	}

	var result orchestrateStartResult
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		t.Fatalf("decode output: %v\n%s", err, output)
	}
	if len(result.Launched) != 1 || result.Launched[0].OperationID != "op-1" || result.Launched[0].OperationState != string(protocol.OperationStateDone) {
		t.Fatalf("launched = %+v", result.Launched)
	}
	if !strings.Contains(result.Advice.WatchInstruction, "leave it running") || strings.Contains(result.Advice.WatchCommand, "--once") {
		t.Fatalf("watch advice = %+v, want continuous watch instruction without --once", result.Advice)
	}
	if len(result.Warnings) != 2 || !strings.Contains(strings.Join(result.Warnings, "\n"), "az worktree create az-1") || !strings.Contains(strings.Join(result.Warnings, "\n"), "uncommitted tracked changes") {
		t.Fatalf("warnings = %+v", result.Warnings)
	}
	if submitted.Kind != commandSessionStart || submitted.IssueID != child || len(submitted.Payload) == 0 {
		t.Fatalf("submitted = %+v", submitted)
	}
	if !containsString(submitted.ResourceKeys, "issue:"+protocol.DefaultProjectID+":"+child.String()) {
		t.Fatalf("submitted resource keys = %+v, want issue resource key", submitted.ResourceKeys)
	}
	if !containsString(submitted.ResourceKeys, "worktree:"+child.String()) {
		t.Fatalf("submitted resource keys = %+v, want worktree resource key", submitted.ResourceKeys)
	}
	if !containsString(submitted.ResourceKeys, "session:"+naming.CanonicalSessionID("/repo", child.String())) {
		t.Fatalf("submitted resource keys = %+v, want session resource key", submitted.ResourceKeys)
	}
	if strings.Join(commands, ",") == "" || !containsString(commands, protocol.CommandOperationSubmit) || !containsString(commands, protocol.CommandOperationGet) {
		t.Fatalf("commands = %+v, want operation submit and readiness wait", commands)
	}
}

func TestOrchestrateStartSkipsForeignOwnedIssue(t *testing.T) {
	root := naming.IssueID("az-1")
	child := naming.IssueID("az-2")
	var readinessBody struct {
		TaskID  naming.IssueID `json:"task_id"`
		ActorID string         `json:"actor_id,omitempty"`
	}
	deps := &Dependencies{
		ProjectID: protocol.DefaultProjectID,
		RepoDir:   "/repo",
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				switch req.Command {
				case daemonclient.CommandTaskGraphReadiness:
					if err := json.Unmarshal(req.Body, &readinessBody); err != nil {
						t.Fatalf("decode readiness request: %v", err)
					}
					return responseWithJSON(req, daemonclient.TaskGraphReadiness{
						RootIssueID: root.String(),
						Runnable:    []string{},
						Blocked:     map[string]string{child.String(): "owned by agent-a"},
					}), nil
				case daemonclient.CommandWorktreeList:
					return responseWithJSON(req, map[string]any{"project_id": protocol.DefaultProjectID, "worktrees": []map[string]string{}}), nil
				case protocol.CommandOperationSubmit:
					t.Fatal("orchestrate start should not submit a session for a foreign-owned issue")
				default:
					t.Fatalf("unexpected command: %s", req.Command)
				}
				return protocol.ResponseEnvelope{}, nil
			},
		}),
	}

	t.Setenv("AZEDARACH_AUDIT_ACTOR", "agent-b")
	result, err := orchestrateStart(deps, OrchestrateStartOptions{RootIssueID: root.String(), IssueIDs: []string{child.String()}, Limit: 4})
	if err != nil {
		t.Fatalf("orchestrateStart error = %v", err)
	}
	if readinessBody.ActorID != "agent-b" {
		t.Fatalf("readiness actor_id = %q, want agent-b", readinessBody.ActorID)
	}
	if got := result.Skipped[child.String()]; got != "owned by agent-a" {
		t.Fatalf("skipped[%s] = %q, want ownership blocker", child, got)
	}
	if len(result.Started) != 0 || len(result.Launched) != 0 {
		t.Fatalf("started=%+v launched=%+v, want no launches", result.Started, result.Launched)
	}
}

func TestOrchestrateStartSkipsWhenOwnershipClaimConflictsAfterReadiness(t *testing.T) {
	root := naming.IssueID("az-1")
	child := naming.IssueID("az-2")
	deps := &Dependencies{
		ProjectID: protocol.DefaultProjectID,
		RepoDir:   "/repo",
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				switch req.Command {
				case daemonclient.CommandTaskGraphReadiness:
					return responseWithJSON(req, daemonclient.TaskGraphReadiness{
						RootIssueID: root.String(),
						Runnable:    []string{child.String()},
						Blocked:     map[string]string{},
					}), nil
				case daemonclient.CommandWorktreeList:
					return responseWithJSON(req, map[string]any{"project_id": protocol.DefaultProjectID, "worktrees": []map[string]string{}}), nil
				case daemonclient.CommandGitStatus:
					return responseWithJSON(req, map[string]any{"status": gitservice.GitStatus{}}), nil
				case daemonclient.CommandTaskClaimOwnership:
					return protocol.ResponseEnvelope{
						ProtocolVersion: req.ProtocolVersion,
						RequestID:       req.RequestID,
						Kind:            protocol.EnvelopeKindResponse,
						OK:              false,
						Error:           &protocol.ErrorEnvelope{Code: protocol.ErrorCodeConflict, Message: "owned by agent-a"},
					}, nil
				case protocol.CommandOperationSubmit:
					t.Fatal("orchestrate start must not submit after ownership claim conflict")
				default:
					t.Fatalf("unexpected command: %s", req.Command)
				}
				return protocol.ResponseEnvelope{}, nil
			},
		}),
	}

	result, err := orchestrateStart(deps, OrchestrateStartOptions{RootIssueID: root.String(), Limit: 4})
	if err != nil {
		t.Fatalf("orchestrateStart error = %v", err)
	}
	if got := result.Skipped[child.String()]; got != "owned by agent-a" {
		t.Fatalf("skipped[%s] = %q, want ownership conflict", child, got)
	}
	if len(result.Started) != 0 || len(result.Launched) != 0 || len(result.Failed) != 0 {
		t.Fatalf("started=%+v launched=%+v failed=%+v, want no launch or failure", result.Started, result.Launched, result.Failed)
	}
}

func TestOrchestrateStartWarnsOnExpensiveSessionSyncInitCommandsDuringFanout(t *testing.T) {
	root := naming.IssueID("az-1")
	childA := naming.IssueID("az-2")
	childB := naming.IssueID("az-3")
	tasks := []domain.Task{
		{ID: root, Title: "Root", Status: domain.StatusInProgress, Type: domain.TypeEpic},
		{ID: childA, Title: "Worker A", Status: domain.StatusOpen, Type: domain.TypeTask, ParentID: &root},
		{ID: childB, Title: "Worker B", Status: domain.StatusOpen, Type: domain.TypeTask, ParentID: &root},
	}
	taskListBody, err := marshalTaskListBody(tasks)
	if err != nil {
		t.Fatalf("marshal task list: %v", err)
	}

	cfg := config.DefaultConfig()
	cfg.Session.SyncInitCommands = []string{"direnv allow", "pnpm type-check"}
	deps := &Dependencies{
		Config:    cfg,
		ProjectID: protocol.DefaultProjectID,
		RepoDir:   "/repo",
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				switch req.Command {
				case daemonclient.CommandTaskGraphReadiness:
					return responseWithJSON(req, daemonclient.TaskGraphReadiness{
						RootIssueID: root.String(),
						Runnable:    []string{childA.String(), childB.String()},
						Blocked:     map[string]string{},
					}), nil
				case daemonclient.CommandTaskList:
					return protocol.ResponseEnvelope{ProtocolVersion: req.ProtocolVersion, RequestID: req.RequestID, Kind: protocol.EnvelopeKindResponse, Meta: req.Meta, OK: true, Body: taskListBody}, nil
				case daemonclient.CommandTaskGetMany:
					return protocol.ResponseEnvelope{ProtocolVersion: req.ProtocolVersion, RequestID: req.RequestID, Kind: protocol.EnvelopeKindResponse, Meta: req.Meta, OK: true, Body: taskListBody}, nil
				case daemonclient.CommandTaskMergeBaseTarget:
					var body struct {
						TaskID string `json:"task_id"`
					}
					if err := json.Unmarshal(req.Body, &body); err != nil {
						t.Fatalf("decode merge base target body: %v", err)
					}
					return responseWithJSON(req, daemonclient.TaskMergeBaseTarget{IssueID: body.TaskID, TargetID: "base", Branch: "main"}), nil
				case daemonclient.CommandGitStatus:
					return responseWithJSON(req, map[string]any{"status": gitservice.GitStatus{}}), nil
				case daemonclient.CommandWorktreeList:
					return responseWithJSON(req, map[string]any{
						"project_id": protocol.DefaultProjectID,
						"worktrees": []map[string]string{
							{"issue_id": root.String(), "path": "/repo-az-1", "branch": "user/az-1/root"},
							{"issue_id": childA.String(), "path": "/repo-az-2", "branch": "user/az-2/worker-a"},
							{"issue_id": childB.String(), "path": "/repo-az-3", "branch": "user/az-3/worker-b"},
						},
					}), nil
				case daemonclient.CommandTaskClaimOwnership:
					return responseWithTaskOwnershipMutation(t, req), nil
				case protocol.CommandOperationSubmit:
					var body protocol.OperationSubmitRequestBody
					if err := json.Unmarshal(req.Body, &body); err != nil {
						t.Fatalf("decode submit body: %v", err)
					}
					return responseWithJSON(req, protocol.OperationSubmitResponseBody{
						Created: true,
						Operation: protocol.OperationRecord{
							OperationID: naming.OperationID("op-" + body.IssueID.String()),
							ProjectID:   protocol.DefaultProjectID,
							Kind:        commandSessionStart,
							IssueID:     body.IssueID,
							State:       protocol.OperationStateQueued,
						},
					}), nil
				case protocol.CommandOperationGet:
					var body protocol.OperationGetRequestBody
					if err := json.Unmarshal(req.Body, &body); err != nil {
						t.Fatalf("decode operation get body: %v", err)
					}
					issueID := strings.TrimPrefix(body.OperationID.String(), "op-")
					return responseWithJSON(req, protocol.OperationGetResponseBody{
						Operation: protocol.OperationRecord{
							OperationID: body.OperationID,
							ProjectID:   protocol.DefaultProjectID,
							Kind:        commandSessionStart,
							IssueID:     naming.IssueID(issueID),
							State:       protocol.OperationStateDone,
						},
					}), nil
				case protocol.CommandMailSend:
					var body protocol.MailSendCommandBody
					if err := json.Unmarshal(req.Body, &body); err != nil {
						t.Fatalf("decode mail body: %v", err)
					}
					return responseWithJSON(req, protocol.MailEvent{Seq: 1, ParentIssue: root.String(), IssueID: body.IssueID, Type: "session-started"}), nil
				default:
					t.Fatalf("unexpected command: %s", req.Command)
				}
				return protocol.ResponseEnvelope{}, nil
			},
		}),
	}

	output := captureStdout(t, func() error {
		return OrchestrateStartCommand(deps, OrchestrateStartOptions{RootIssueID: root.String(), Limit: 4, JSON: true})
	})
	var result orchestrateStartResult
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		t.Fatalf("decode output: %v\n%s", err, output)
	}
	if len(result.Started) != 2 || len(result.Failed) != 0 {
		t.Fatalf("started=%+v failed=%+v, want both workers started with no failures", result.Started, result.Failed)
	}
	warnings := strings.Join(result.Warnings, "\n")
	for _, want := range []string{"pnpm type-check", "fanout count 2", "root az-1", "lowering --limit", "explicit verification", "one parent preflight"} {
		if !strings.Contains(warnings, want) {
			t.Fatalf("warnings missing %q: %+v", want, result.Warnings)
		}
	}
}

func TestOrchestrateStartWaitsForAllOperationsBeforeReturning(t *testing.T) {
	root := naming.IssueID("az-1")
	childA := naming.IssueID("az-2")
	childB := naming.IssueID("az-3")
	tasks := []domain.Task{
		{ID: root, Title: "Root", Status: domain.StatusInProgress, Type: domain.TypeEpic},
		{ID: childA, Title: "Worker A", Status: domain.StatusOpen, Type: domain.TypeTask, ParentID: &root},
		{ID: childB, Title: "Worker B", Status: domain.StatusOpen, Type: domain.TypeTask, ParentID: &root},
	}
	taskListBody, err := marshalTaskListBody(tasks)
	if err != nil {
		t.Fatalf("marshal task list: %v", err)
	}

	submitted := map[string]bool{}
	deps := &Dependencies{
		ProjectID: protocol.DefaultProjectID,
		RepoDir:   "/repo",
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				switch req.Command {
				case daemonclient.CommandTaskGraphReadiness:
					return responseWithJSON(req, daemonclient.TaskGraphReadiness{
						RootIssueID: root.String(),
						Runnable:    []string{childA.String(), childB.String()},
						Blocked:     map[string]string{},
					}), nil
				case daemonclient.CommandTaskList:
					return protocol.ResponseEnvelope{ProtocolVersion: req.ProtocolVersion, RequestID: req.RequestID, Kind: protocol.EnvelopeKindResponse, Meta: req.Meta, OK: true, Body: taskListBody}, nil
				case daemonclient.CommandTaskGetMany:
					return protocol.ResponseEnvelope{ProtocolVersion: req.ProtocolVersion, RequestID: req.RequestID, Kind: protocol.EnvelopeKindResponse, Meta: req.Meta, OK: true, Body: taskListBody}, nil
				case daemonclient.CommandTaskMergeBaseTarget:
					var body struct {
						TaskID string `json:"task_id"`
					}
					if err := json.Unmarshal(req.Body, &body); err != nil {
						t.Fatalf("decode merge base target body: %v", err)
					}
					return responseWithJSON(req, daemonclient.TaskMergeBaseTarget{IssueID: body.TaskID, TargetID: "base", Branch: "main"}), nil
				case daemonclient.CommandGitStatus:
					return responseWithJSON(req, map[string]any{"status": gitservice.GitStatus{}}), nil
				case daemonclient.CommandTaskClaimOwnership:
					return responseWithTaskOwnershipMutation(t, req), nil
				case protocol.CommandOperationSubmit:
					var body protocol.OperationSubmitRequestBody
					if err := json.Unmarshal(req.Body, &body); err != nil {
						t.Fatalf("decode submit body: %v", err)
					}
					submitted[body.IssueID.String()] = true
					return responseWithJSON(req, protocol.OperationSubmitResponseBody{
						Created: true,
						Operation: protocol.OperationRecord{
							OperationID: naming.OperationID("op-" + body.IssueID.String()),
							ProjectID:   protocol.DefaultProjectID,
							Kind:        commandSessionStart,
							IssueID:     body.IssueID,
							State:       protocol.OperationStateQueued,
						},
					}), nil
				case protocol.CommandOperationGet:
					var body protocol.OperationGetRequestBody
					if err := json.Unmarshal(req.Body, &body); err != nil {
						t.Fatalf("decode operation get body: %v", err)
					}
					issueID := strings.TrimPrefix(body.OperationID.String(), "op-")
					return responseWithJSON(req, protocol.OperationGetResponseBody{
						Operation: protocol.OperationRecord{
							OperationID: body.OperationID,
							ProjectID:   protocol.DefaultProjectID,
							Kind:        commandSessionStart,
							IssueID:     naming.IssueID(issueID),
							State:       protocol.OperationStateDone,
						},
					}), nil
				case daemonclient.CommandWorktreeList:
					return responseWithJSON(req, map[string]any{"project_id": protocol.DefaultProjectID, "worktrees": []map[string]string{}}), nil
				case protocol.CommandMailSend:
					t.Fatal("orchestrate start should not send session-started mail before watch/status observe completion")
				default:
					t.Fatalf("unexpected command: %s", req.Command)
				}
				return protocol.ResponseEnvelope{}, nil
			},
		}),
	}

	result, err := orchestrateStart(deps, OrchestrateStartOptions{RootIssueID: "az-1", IssueIDs: []string{"az-2", "az-3"}, Limit: 4})
	if err != nil {
		t.Fatalf("orchestrateStart error = %v", err)
	}
	if len(result.Started) != 2 || len(result.Launched) != 2 || len(result.Failed) != 0 {
		t.Fatalf("result started=%+v launched=%+v failed=%+v, want both submitted with no failures", result.Started, result.Launched, result.Failed)
	}
	if !submitted[childA.String()] || !submitted[childB.String()] {
		t.Fatalf("submitted = %+v, want both children", submitted)
	}
	for _, launch := range result.Launched {
		if launch.OperationID == "" || launch.OperationState != string(protocol.OperationStateDone) {
			t.Fatalf("launch = %+v, want done operation id", launch)
		}
	}
}

func TestOrchestrateStartReportsPendingOperationWhenChildIsNotReadyBeforeDeadline(t *testing.T) {
	oldTimeout := orchestrateStartWaitTimeout
	oldPollInterval := orchestrateStartWaitPollInterval
	orchestrateStartWaitTimeout = time.Millisecond
	orchestrateStartWaitPollInterval = time.Hour
	defer func() {
		orchestrateStartWaitTimeout = oldTimeout
		orchestrateStartWaitPollInterval = oldPollInterval
	}()

	root := naming.IssueID("az-1")
	child := naming.IssueID("az-2")
	taskListBody, err := marshalTaskListBody([]domain.Task{
		{ID: root, Title: "Root", Status: domain.StatusInProgress, Type: domain.TypeEpic},
		{ID: child, Title: "Worker", Status: domain.StatusOpen, Type: domain.TypeTask, ParentID: &root},
	})
	if err != nil {
		t.Fatalf("marshal task list: %v", err)
	}

	deps := &Dependencies{
		ProjectID: protocol.DefaultProjectID,
		RepoDir:   "/repo",
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				switch req.Command {
				case daemonclient.CommandTaskGraphReadiness:
					return responseWithJSON(req, daemonclient.TaskGraphReadiness{
						RootIssueID: root.String(),
						Runnable:    []string{child.String()},
						Blocked:     map[string]string{},
					}), nil
				case daemonclient.CommandTaskList:
					return protocol.ResponseEnvelope{ProtocolVersion: req.ProtocolVersion, RequestID: req.RequestID, Kind: protocol.EnvelopeKindResponse, Meta: req.Meta, OK: true, Body: taskListBody}, nil
				case daemonclient.CommandTaskGetMany:
					assertMetadataOnlyTaskGetManyRequest(t, req, child.String())
					return protocol.ResponseEnvelope{ProtocolVersion: req.ProtocolVersion, RequestID: req.RequestID, Kind: protocol.EnvelopeKindResponse, Meta: req.Meta, OK: true, Body: taskListBody}, nil
				case daemonclient.CommandTaskMergeBaseTarget:
					return responseWithJSON(req, daemonclient.TaskMergeBaseTarget{IssueID: child.String(), TargetID: "base", Branch: "main"}), nil
				case daemonclient.CommandGitStatus:
					return responseWithJSON(req, map[string]any{"status": gitservice.GitStatus{}}), nil
				case daemonclient.CommandWorktreeList:
					return responseWithJSON(req, map[string]any{
						"project_id": protocol.DefaultProjectID,
						"worktrees": []map[string]string{
							{"issue_id": root.String(), "path": "/repo-az-1", "branch": "user/az-1/root"},
						},
					}), nil
				case daemonclient.CommandTaskClaimOwnership:
					return responseWithTaskOwnershipMutation(t, req), nil
				case protocol.CommandOperationSubmit:
					return responseWithJSON(req, protocol.OperationSubmitResponseBody{
						Created: true,
						Operation: protocol.OperationRecord{
							OperationID: "op-1",
							ProjectID:   protocol.DefaultProjectID,
							Kind:        commandSessionStart,
							IssueID:     child,
							State:       protocol.OperationStateQueued,
						},
					}), nil
				case protocol.CommandOperationGet:
					return responseWithJSON(req, protocol.OperationGetResponseBody{
						Operation: protocol.OperationRecord{
							OperationID: "op-1",
							ProjectID:   protocol.DefaultProjectID,
							Kind:        commandSessionStart,
							IssueID:     child,
							State:       protocol.OperationStateRunning,
						},
					}), nil
				case protocol.CommandMailSend:
					t.Fatal("orchestrate start should not send session-started mail before operation completion is observed")
				default:
					t.Fatalf("unexpected command: %s", req.Command)
				}
				return protocol.ResponseEnvelope{}, nil
			},
		}),
	}

	output, err := captureStdoutAllowError(t, func() error {
		return OrchestrateStartCommand(deps, OrchestrateStartOptions{RootIssueID: root.String(), IssueIDs: []string{child.String()}, Limit: 4, JSON: true})
	})
	if err != nil {
		t.Fatalf("OrchestrateStartCommand error = %v", err)
	}
	var result orchestrateStartResult
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		t.Fatalf("decode output: %v\n%s", err, output)
	}
	if len(result.Failed) != 0 || len(result.Pending) != 1 || len(result.Started) != 0 || len(result.Launched) != 0 {
		t.Fatalf("started=%+v launched=%+v pending=%+v failed=%+v, want one pending start", result.Started, result.Launched, result.Pending, result.Failed)
	}
	pending := result.Pending[0]
	if pending.IssueID != child.String() || pending.OperationID != "op-1" || pending.OperationState != string(protocol.OperationStateRunning) {
		t.Fatalf("pending = %+v", pending)
	}
	if !strings.Contains(pending.Reason, "timed out") || !containsString(pending.FollowUpCommands, "az operation get --id op-1 --wait") {
		t.Fatalf("pending = %+v, want timeout reason and operation follow-up", pending)
	}
}

func TestOrchestrateStartSkipsNestedRootWithSessionStartAdvice(t *testing.T) {
	root := naming.IssueID("az-1")
	nested := naming.IssueID("az-2")
	deps := &Dependencies{
		RepoDir:   "/repo",
		ProjectID: protocol.DefaultProjectID,
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				switch req.Command {
				case daemonclient.CommandTaskGraphReadiness:
					return responseWithJSON(req, daemonclient.TaskGraphReadiness{
						RootIssueID: root.String(),
						Runnable:    []string{},
						NestedRoots: []daemonclient.TaskNestedRoot{{
							IssueID:    nested.String(),
							Status:     string(domain.StatusOpen),
							Type:       string(domain.TypeTask),
							ChildCount: 1,
							Advice:     "start its orchestrator session with `az session start az-2`",
						}},
						Blocked: map[string]string{},
					}), nil
				case daemonclient.CommandWorktreeList:
					return responseWithJSON(req, map[string]any{
						"project_id": protocol.DefaultProjectID,
						"worktrees": []map[string]string{
							{"issue_id": root.String(), "path": "/repo-az-1", "branch": "user/az-1/root"},
						},
					}), nil
				case protocol.CommandOperationSubmit:
					t.Fatal("orchestrate start must not submit session starts for nested roots from the parent root")
				default:
					t.Fatalf("unexpected command: %s", req.Command)
				}
				return protocol.ResponseEnvelope{}, nil
			},
		}),
	}

	result, err := orchestrateStart(deps, OrchestrateStartOptions{RootIssueID: root.String(), IssueIDs: []string{nested.String()}, Limit: 4})
	if err != nil {
		t.Fatalf("orchestrateStart error = %v", err)
	}
	if len(result.Started) != 0 || len(result.Launched) != 0 {
		t.Fatalf("started=%+v launched=%+v, want no nested root launch", result.Started, result.Launched)
	}
	if !slices.Equal(result.NestedRoots, []string{nested.String()}) {
		t.Fatalf("nested roots = %+v, want %s", result.NestedRoots, nested.String())
	}
	if got := result.Skipped[nested.String()]; !strings.Contains(got, "nested-root-start-orchestrator-session") || !strings.Contains(got, "az session start "+nested.String()) {
		t.Fatalf("skipped[%s] = %q, want session start advice", nested.String(), got)
	}

	defaultResult, err := orchestrateStart(deps, OrchestrateStartOptions{RootIssueID: root.String(), Limit: 4})
	if err != nil {
		t.Fatalf("orchestrateStart default error = %v", err)
	}
	if len(defaultResult.Started) != 0 || !slices.Equal(defaultResult.NestedRoots, []string{nested.String()}) {
		t.Fatalf("default started=%+v nested=%+v, want nested root advice without launches", defaultResult.Started, defaultResult.NestedRoots)
	}
}

func TestOrchestrateStartPreservesSubmitFailure(t *testing.T) {
	root := naming.IssueID("az-1")
	child := naming.IssueID("az-2")
	taskListBody, err := marshalTaskListBody([]domain.Task{
		{ID: root, Title: "Root", Status: domain.StatusInProgress, Type: domain.TypeEpic},
		{ID: child, Title: "Worker", Status: domain.StatusOpen, Type: domain.TypeTask, ParentID: &root},
	})
	if err != nil {
		t.Fatalf("marshal task list: %v", err)
	}

	deps := &Dependencies{
		ProjectID: protocol.DefaultProjectID,
		RepoDir:   "/repo",
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				switch req.Command {
				case daemonclient.CommandTaskGraphReadiness:
					return responseWithJSON(req, daemonclient.TaskGraphReadiness{
						RootIssueID: root.String(),
						Runnable:    []string{child.String()},
						Blocked:     map[string]string{},
					}), nil
				case daemonclient.CommandTaskList:
					return protocol.ResponseEnvelope{ProtocolVersion: req.ProtocolVersion, RequestID: req.RequestID, Kind: protocol.EnvelopeKindResponse, Meta: req.Meta, OK: true, Body: taskListBody}, nil
				case daemonclient.CommandTaskGetMany:
					assertMetadataOnlyTaskGetManyRequest(t, req, child.String())
					return protocol.ResponseEnvelope{ProtocolVersion: req.ProtocolVersion, RequestID: req.RequestID, Kind: protocol.EnvelopeKindResponse, Meta: req.Meta, OK: true, Body: taskListBody}, nil
				case daemonclient.CommandTaskMergeBaseTarget:
					return responseWithJSON(req, daemonclient.TaskMergeBaseTarget{IssueID: child.String(), TargetID: "base", Branch: "main"}), nil
				case daemonclient.CommandGitStatus:
					return responseWithJSON(req, map[string]any{"status": gitservice.GitStatus{}}), nil
				case daemonclient.CommandWorktreeList:
					return responseWithJSON(req, map[string]any{
						"project_id": protocol.DefaultProjectID,
						"worktrees": []map[string]string{
							{"issue_id": root.String(), "path": "/repo-az-1", "branch": "user/az-1/root"},
						},
					}), nil
				case daemonclient.CommandTaskClaimOwnership:
					return responseWithTaskOwnershipMutation(t, req), nil
				case protocol.CommandOperationSubmit:
					return protocol.ResponseEnvelope{
						ProtocolVersion: req.ProtocolVersion,
						RequestID:       req.RequestID,
						Kind:            protocol.EnvelopeKindResponse,
						OK:              false,
						Error:           &protocol.ErrorEnvelope{Code: protocol.ErrorCodeInternal, Message: "tmux launch failed"},
					}, nil
				case daemonclient.CommandTaskReleaseOwnership:
					return responseWithTaskOwnershipMutation(t, req), nil
				case protocol.CommandOperationGet:
					t.Fatal("orchestrate start should not poll submitted operations")
				case protocol.CommandMailSend:
					t.Fatal("mail should not be sent for failed submission")
				default:
					t.Fatalf("unexpected command: %s", req.Command)
				}
				return protocol.ResponseEnvelope{}, nil
			},
		}),
	}

	output, err := captureStdoutAllowError(t, func() error {
		return OrchestrateStartCommand(deps, OrchestrateStartOptions{RootIssueID: root.String(), IssueIDs: []string{child.String()}, Limit: 4, JSON: true})
	})
	if err == nil || !strings.Contains(err.Error(), "completed with failures") {
		t.Fatalf("OrchestrateStartCommand error = %v, want failure error", err)
	}
	var result orchestrateStartResult
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		t.Fatalf("decode output: %v\n%s", err, output)
	}
	if len(result.Pending) != 0 || !strings.Contains(result.Failed[child.String()], "tmux launch failed") {
		t.Fatalf("pending=%+v failed=%+v, want submit failure only", result.Pending, result.Failed)
	}
}

func TestPrintOrchestrateStartResultIncludesPendingFollowUps(t *testing.T) {
	output := captureStdout(t, func() error {
		printOrchestrateStartResult(orchestrateStartResult{
			RootIssueID: "az-1",
			Limit:       4,
			Skipped:     map[string]string{},
			Failed:      map[string]string{},
			Pending: []orchestrateStartPending{
				{
					IssueID:        "az-2",
					OperationID:    "op-1",
					OperationState: string(protocol.OperationStateRunning),
					Reason:         "timed out after 5m0s waiting for daemon operation to finish; operation is still running",
					FollowUpCommands: []string{
						"az operation get --id op-1 --wait",
						"az orchestrate status --root az-1 --json",
					},
				},
			},
			Advice: orchestrateStartAdvice{
				WatchCommand:     "az orchestrate watch --root az-1 --since 0 --jsonl",
				WatchInstruction: "leave it running",
			},
		})
		return nil
	})
	for _, want := range []string{
		"Pending starts:",
		"az-2: operation=op-1 state=running",
		"follow up: az operation get --id op-1 --wait",
		"follow up: az orchestrate status --root az-1 --json",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("output missing %q:\n%s", want, output)
		}
	}
}

func TestOrchestrateStartWarnsWhenRootWorktreeMissingWithoutRunnableLaunches(t *testing.T) {
	root := naming.IssueID("az-1")
	child := naming.IssueID("az-2")
	taskListBody, err := marshalTaskListBody([]domain.Task{
		{ID: root, Title: "Root", Status: domain.StatusInProgress, Type: domain.TypeEpic},
		{ID: child, Title: "Blocked worker", Status: domain.StatusOpen, Type: domain.TypeTask, ParentID: &root},
	})
	if err != nil {
		t.Fatalf("marshal task list: %v", err)
	}
	deps := &Dependencies{
		RepoDir:        "/repo",
		RuntimeRepoDir: "/repo-cif",
		ProjectID:      protocol.DefaultProjectID,
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				switch req.Command {
				case daemonclient.CommandTaskGraphReadiness:
					return responseWithJSON(req, daemonclient.TaskGraphReadiness{
						RootIssueID: root.String(),
						Runnable:    []string{},
						Blocked:     map[string]string{child.String(): "blocked by az-3"},
					}), nil
				case daemonclient.CommandTaskList:
					return protocol.ResponseEnvelope{ProtocolVersion: req.ProtocolVersion, RequestID: req.RequestID, Kind: protocol.EnvelopeKindResponse, Meta: req.Meta, OK: true, Body: taskListBody}, nil
				case daemonclient.CommandWorktreeList:
					return responseWithJSON(req, map[string]any{"project_id": protocol.DefaultProjectID, "worktrees": []map[string]string{}}), nil
				default:
					t.Fatalf("unexpected command: %s", req.Command)
				}
				return protocol.ResponseEnvelope{}, nil
			},
		}),
	}

	result, err := orchestrateStart(deps, OrchestrateStartOptions{RootIssueID: root.String(), Limit: 4})
	if err != nil {
		t.Fatalf("orchestrateStart error = %v", err)
	}
	if len(result.Requested) != 0 || len(result.Started) != 0 {
		t.Fatalf("requested=%+v started=%+v, want no launches", result.Requested, result.Started)
	}
	if len(result.Warnings) != 1 ||
		!strings.Contains(result.Warnings[0], "root issue az-1 has child orchestration but no dedicated worktree") ||
		!strings.Contains(result.Warnings[0], "current worktree/path is /repo-cif") ||
		!strings.Contains(result.Warnings[0], "az worktree create az-1") {
		t.Fatalf("warnings = %+v", result.Warnings)
	}
}

func TestOrchestratePromptCommandPrintsNativeWorkerHandoff(t *testing.T) {
	deps, root, child := orchestratePromptTestDeps(t)

	output := captureStdout(t, func() error {
		return OrchestratePromptCommand(deps, OrchestratePromptOptions{RootIssueID: root.String(), IssueID: child.String(), Coordination: "native"})
	})
	for _, want := range []string{
		"Work on issue az-2: Worker task",
		"Coordination mode: native",
		"az spec read --issue az-2",
		"Current notes: present but omitted from worker prompt. Run `az issue get az-2 --with-notes` only if full note history is necessary.",
		"Status semantics: use `in_progress` while actively working and `in_review` when your implementation is complete and ready for orchestrator review/integration",
		"Keep `az-2` status current; record progress, follow-ups, validation, review facts, risks, blockers, and closeout evidence with `az issue record` instead of routine notes.",
		"Blocked work is represented by dependency edges, issue record evidence, or active-coordination `worker-blocked` mailbox events, not by setting issue status to `in_review`.",
		"Do not append raw logs, exploratory transcripts, routine progress narration, duplicate prompt context, or speculative scratch work to notes.",
		"Return progress, blockers, and final results through the native subagent result channel.",
		"Do not use `az mail` unless the orchestrator explicitly asks for mailbox coordination; use `az issue record` for durable issue activity/evidence.",
		"Before handing off, run the relevant validation/review checks and include the same facts expected in `worker_evidence.v1`: summary, commands run, key assertions, files changed, review status/findings, and risks.",
		"Preserve any az-managed worker session/worktree for feedback; do not stop or close them as part of review handoff.",
		"Do not rely on a prose-only status update.",
		"Do not close root issue `az-1` or your worker issue as part of handoff; leave integration and terminal close to the orchestrator unless the human explicitly instructs otherwise.",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("output missing %q:\n%s", want, output)
		}
	}
	for _, unexpected := range []string{"Coordination mailbox parent:", "az mail send", "worker-complete"} {
		if strings.Contains(output, unexpected) {
			t.Fatalf("output unexpectedly contains %q:\n%s", unexpected, output)
		}
	}
	if strings.Contains(output, "close only your worker issue") {
		t.Fatalf("output retained stale worker self-close guidance:\n%s", output)
	}
	if strings.Contains(output, "Spec impact TBD") {
		t.Fatalf("output should not include full historical notes by default:\n%s", output)
	}
}

func TestOrchestratePromptCommandMailboxCoordinationOptIn(t *testing.T) {
	deps, root, child := orchestratePromptTestDeps(t)

	output := captureStdout(t, func() error {
		return OrchestratePromptCommand(deps, OrchestratePromptOptions{RootIssueID: root.String(), IssueID: child.String(), Coordination: "mailbox"})
	})
	for _, want := range []string{
		"Coordination mode: mailbox",
		"Coordination mailbox parent: az-1",
		"Use mailbox events for active hybrid coordination only",
		"For non-orchestrated durable facts, use `az issue record`.",
		"Check inbound orchestrator messages with `az mail list --parent az-1 --since 0 --json` before declaring yourself blocked or idle",
		"Report to parent `az-1` with `az mail send --parent az-1 --issue az-2 --type <worker-progress|worker-blocked|worker-integration-ready> --body \"<evidence>\"`; do not use `az orchestrate message` for your own status",
		"Evidence bodies should be JSON `worker_evidence.v1` packets with `summary`, `commands_run`, `key_assertions`, `files_changed`, `review.status`, `review.findings`, and `risks`",
		"use `az issue record --type evidence.submitted --data '<json>'` when mailbox delivery is irrelevant",
		"Omit `artifact_links` unless links are needed; when present, encode it as objects like `[{\"label\":\"CI\",\"url\":\"https://example.test/run\"}]`, not a string array",
		"Before handing off, run the relevant validation/review checks, build the final `worker_evidence.v1` packet from actual results, run `az evidence validate --body '<json>'`, record or send that exact JSON packet, then set/leave `az-2` `in_review`",
		"Preserve the worker session/worktree for feedback; do not stop or close them as part of review handoff.",
		"Do not rely on a prose-only final response as the handoff.",
		"otherwise record it with `az issue record az-2 --type evidence.submitted --data '<json>'`",
		"az mail list --parent az-1 --since 0 --json",
		"az issue record az-2 --type evidence.submitted",
		"`worker-ready` and `worker-complete` are accepted only as legacy aliases for `worker-integration-ready`",
		"az mail send --parent az-1 --issue az-2 --type worker-integration-ready",
		`"schema":"worker_evidence.v1","summary":"Ready for integration."`,
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("output missing %q:\n%s", want, output)
		}
	}
}

func TestOrchestrateMessageCommandRecordsMailboxThenDeliversToSession(t *testing.T) {
	t.Setenv("AZEDARACH_ISSUE_ID", "az-9")
	commands := []string{}
	var mailBody protocol.MailSendCommandBody
	var sessionBody struct {
		SessionID naming.SessionID `json:"session_id"`
		Message   string           `json:"message"`
	}
	deps := &Dependencies{
		ProjectID: protocol.DefaultProjectID,
		RepoDir:   "/repo",
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				commands = append(commands, req.Command)
				switch req.Command {
				case protocol.CommandMailSend:
					if err := json.Unmarshal(req.Body, &mailBody); err != nil {
						t.Fatalf("decode mail body: %v", err)
					}
					return responseWithJSON(req, protocol.MailEvent{
						Seq:         7,
						ParentIssue: "az-1",
						IssueID:     naming.IssueID("az-2"),
						Type:        "orchestrator-message",
						From:        "orchestrator",
						To:          "az-2",
						Body:        "Proceed now",
					}), nil
				case daemonclient.CommandSessionMessage:
					if err := json.Unmarshal(req.Body, &sessionBody); err != nil {
						t.Fatalf("decode session message body: %v", err)
					}
					return responseWithJSON(req, map[string]string{"output": "Sent message to session: az-2\n"}), nil
				default:
					t.Fatalf("unexpected command: %s", req.Command)
				}
				return protocol.ResponseEnvelope{}, nil
			},
		}),
	}

	output := captureStdout(t, func() error {
		return OrchestrateMessageCommand(deps, OrchestrateMessageOptions{
			RootIssueID: "az-1",
			IssueID:     "az-2",
			Type:        "orchestrator-message",
			Body:        "Proceed now",
			JSON:        true,
		})
	})

	if len(commands) != 2 || commands[0] != protocol.CommandMailSend || commands[1] != daemonclient.CommandSessionMessage {
		t.Fatalf("commands = %+v, want mail send then session message", commands)
	}
	if mailBody.ParentIssue != "az-1" || mailBody.IssueID != "az-2" || mailBody.Type != "orchestrator-message" || mailBody.From != "orchestrator" || mailBody.To != "az-2" || mailBody.Body != "Proceed now" {
		t.Fatalf("mail body = %+v", mailBody)
	}
	if sessionBody.SessionID != "az-2" || !strings.Contains(sessionBody.Message, "Orchestrator message for issue az-2 under root az-1") || !strings.Contains(sessionBody.Message, "Proceed now") {
		t.Fatalf("session body = %+v", sessionBody)
	}
	var result orchestrateMessageResult
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		t.Fatalf("decode result: %v\n%s", err, output)
	}
	if !result.Delivered || result.Mailbox.Seq != 7 {
		t.Fatalf("result = %+v", result)
	}
}

func TestOrchestrateMessageCommandRejectsActiveWorkerSelfDelivery(t *testing.T) {
	t.Setenv("AZEDARACH_ISSUE_ID", "az-2")
	commands := []string{}
	deps := &Dependencies{
		ProjectID: protocol.DefaultProjectID,
		RepoDir:   "/repo",
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				commands = append(commands, req.Command)
				t.Fatalf("unexpected command after self-delivery guard: %s", req.Command)
				return protocol.ResponseEnvelope{}, nil
			},
		}),
	}

	err := OrchestrateMessageCommand(deps, OrchestrateMessageOptions{
		RootIssueID: "az-1",
		IssueID:     "az-2",
		Type:        "worker-integration-ready",
		Body:        "ready",
	})
	if err == nil {
		t.Fatal("expected self-delivery guard error")
	}
	if !strings.Contains(err.Error(), "refusing to deliver orchestrate message to the active issue az-2") ||
		!strings.Contains(err.Error(), "az mail send --parent az-1 --issue az-2 --type worker-integration-ready") {
		t.Fatalf("error = %v, want self-delivery guidance", err)
	}
	if len(commands) != 0 {
		t.Fatalf("commands = %+v, want no mailbox or session commands", commands)
	}
}

func TestOrchestrateMessageCommandForceSelfDelivery(t *testing.T) {
	t.Setenv("AZEDARACH_ISSUE_ID", "az-2")
	commands := []string{}
	deps := &Dependencies{
		ProjectID: protocol.DefaultProjectID,
		RepoDir:   "/repo",
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				commands = append(commands, req.Command)
				switch req.Command {
				case protocol.CommandMailSend:
					return responseWithJSON(req, protocol.MailEvent{Seq: 8, ParentIssue: "az-1", IssueID: naming.IssueID("az-2"), Type: "orchestrator-message"}), nil
				case daemonclient.CommandSessionMessage:
					return responseWithJSON(req, map[string]string{"output": "Sent message to session: az-2\n"}), nil
				default:
					t.Fatalf("unexpected command: %s", req.Command)
				}
				return protocol.ResponseEnvelope{}, nil
			},
		}),
	}

	output := captureStdout(t, func() error {
		return OrchestrateMessageCommand(deps, OrchestrateMessageOptions{
			RootIssueID:       "az-1",
			IssueID:           "az-2",
			Type:              "orchestrator-message",
			Body:              "intentional self message",
			ForceSelfDelivery: true,
			JSON:              true,
		})
	})

	if len(commands) != 2 || commands[0] != protocol.CommandMailSend || commands[1] != daemonclient.CommandSessionMessage {
		t.Fatalf("commands = %+v, want mail send then session message", commands)
	}
	var result orchestrateMessageResult
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		t.Fatalf("decode result: %v\n%s", err, output)
	}
	if !result.Delivered || result.Mailbox.Seq != 8 {
		t.Fatalf("result = %+v", result)
	}
}

func orchestratePromptTestDeps(t *testing.T) (*Dependencies, naming.IssueID, naming.IssueID) {
	t.Helper()
	root := naming.IssueID("az-1")
	child := naming.IssueID("az-2")
	tasks := []domain.Task{
		{ID: root, Title: "Root", Status: domain.StatusInProgress, Type: domain.TypeEpic},
		{
			ID:              child,
			Title:           "Worker task",
			Description:     "Do a focused slice",
			Design:          "Keep it small",
			Acceptance:      "Tests pass",
			Notes:           "Spec impact TBD",
			Status:          domain.StatusOpen,
			Priority:        domain.P2,
			Type:            domain.TypeTask,
			ParentID:        &root,
			Implementations: []string{"go-bubbletea"},
		},
	}
	body, err := marshalTaskListBody(tasks)
	if err != nil {
		t.Fatalf("marshal task list response: %v", err)
	}
	deps := &Dependencies{
		ProjectID: protocol.DefaultProjectID,
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				if req.Command != daemonclient.CommandTaskList {
					t.Fatalf("unexpected command: %s", req.Command)
				}
				return protocol.ResponseEnvelope{ProtocolVersion: req.ProtocolVersion, RequestID: req.RequestID, Kind: protocol.EnvelopeKindResponse, Meta: req.Meta, OK: true, Body: body}, nil
			},
		}),
	}
	return deps, root, child
}

func TestOrchestrateIntegrateCommandPrintsGuidance(t *testing.T) {
	child := naming.IssueID("az-2")
	parent := naming.IssueID("az-1")
	deps := &Dependencies{
		RepoDir:   "/repo",
		ProjectID: protocol.DefaultProjectID,
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				switch req.Command {
				case daemonclient.CommandWorktreeList:
					return responseWithJSON(req, map[string]any{
						"project_id": protocol.DefaultProjectID,
						"worktrees": []map[string]string{
							{"issue_id": child.String(), "path": "/repo-az-2", "branch": "user/az-2/worker"},
						},
					}), nil
				case daemonclient.CommandTaskIntegrationReady:
					return responseWithJSON(req, daemonclient.TaskIntegrationReadiness{
						IssueID:       child.String(),
						ParentIssueID: parent.String(),
						Ready:         true,
					}), nil
				default:
					t.Fatalf("unexpected command: %s", req.Command)
				}
				return protocol.ResponseEnvelope{}, nil
			},
		}),
	}
	output := captureStdout(t, func() error {
		return OrchestrateIntegrateCommand(deps, OrchestrateIntegrateOptions{IssueID: "az-2"})
	})
	for _, want := range []string{"Worktree: /repo-az-2", "az branch merge --source az-2 --target <issue-id|base>", "az issue close --id az-2"} {
		if !strings.Contains(output, want) {
			t.Fatalf("output missing %q:\n%s", want, output)
		}
	}
}

func TestOrchestrateIntegrateCommandBlocksCloseForHighContextRisk(t *testing.T) {
	child := naming.IssueID("az-2")
	parent := naming.IssueID("az-1")
	deps := &Dependencies{
		RepoDir:   "/repo",
		ProjectID: protocol.DefaultProjectID,
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				switch req.Command {
				case daemonclient.CommandWorktreeList:
					return responseWithJSON(req, map[string]any{
						"project_id": protocol.DefaultProjectID,
						"worktrees": []map[string]string{
							{"issue_id": child.String(), "path": "/repo-az-2", "branch": "user/az-2/worker"},
						},
					}), nil
				case daemonclient.CommandTaskIntegrationReady:
					return responseWithJSON(req, daemonclient.TaskIntegrationReadiness{
						IssueID:       child.String(),
						ParentIssueID: parent.String(),
						Ready:         true,
						ContextRisk: &domain.IssueContextRiskPacket{
							IssueID:    child.String(),
							Level:      domain.IssueContextRiskHigh,
							Confidence: 75,
							Signals:    []string{`file overlap "internal/daemon/task_commands.go" with az-3`},
							Evidence: []domain.IssueContextRiskEvidence{
								{IssueID: child.String(), Files: []string{"internal/daemon/task_commands.go"}},
								{IssueID: "az-3", Files: []string{"internal/daemon/task_commands.go"}, RiskNotes: []string{"same failure repeated"}},
							},
						},
					}), nil
				default:
					t.Fatalf("unexpected command: %s", req.Command)
				}
				return protocol.ResponseEnvelope{}, nil
			},
		}),
	}
	output := captureStdout(t, func() error {
		return OrchestrateIntegrateCommand(deps, OrchestrateIntegrateOptions{IssueID: child.String()})
	})
	if !strings.Contains(output, "Context risk: high confidence=75") || !strings.Contains(output, "Closeout guidance: BLOCKED") {
		t.Fatalf("output missing context-risk block:\n%s", output)
	}
	if strings.Contains(output, "az issue close --id az-2") {
		t.Fatalf("output unexpectedly suggests close under high context risk:\n%s", output)
	}
}

func TestOrchestrateIntegrateCommandBlocksMergeWithoutCompletionEvidence(t *testing.T) {
	child := naming.IssueID("az-2")
	parent := naming.IssueID("az-1")
	deps := &Dependencies{
		RepoDir:   "/repo",
		ProjectID: protocol.DefaultProjectID,
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				switch req.Command {
				case daemonclient.CommandWorktreeList:
					return responseWithJSON(req, map[string]any{
						"project_id": protocol.DefaultProjectID,
						"worktrees": []map[string]string{
							{"issue_id": child.String(), "path": "/repo-az-2", "branch": "user/az-2/worker"},
						},
					}), nil
				case daemonclient.CommandTaskIntegrationReady:
					return responseWithJSON(req, daemonclient.TaskIntegrationReadiness{
						IssueID:       child.String(),
						ParentIssueID: parent.String(),
						Ready:         false,
						Reasons: []string{
							"issue az-2 is not closed",
							"no worker-integration-ready mailbox event found under parent az-1 for az-2",
						},
					}), nil
				default:
					t.Fatalf("unexpected command: %s", req.Command)
				}
				return protocol.ResponseEnvelope{}, nil
			},
		}),
	}
	output := captureStdout(t, func() error {
		return OrchestrateIntegrateCommand(deps, OrchestrateIntegrateOptions{IssueID: child.String()})
	})
	if !strings.Contains(output, "Merge guidance: BLOCKED") {
		t.Fatalf("output missing blocked merge guidance:\n%s", output)
	}
	if strings.Contains(output, "az branch merge az-2") {
		t.Fatalf("output unexpectedly suggests branch merge:\n%s", output)
	}
	if strings.Contains(output, "az issue close --id az-2") || strings.Contains(output, "az orchestrate close-session --issue az-2") {
		t.Fatalf("output unexpectedly suggests close/cleanup while blocked:\n%s", output)
	}
}

func TestOrchestrateIntegrateApplySuccess(t *testing.T) {
	deps, commands, closeBudgets := orchestrateIntegrateApplyDeps(t, "")
	output := captureStdout(t, func() error {
		return OrchestrateIntegrateCommand(deps, OrchestrateIntegrateOptions{IssueID: "az-2", Apply: true})
	})
	for _, want := range []string{"Merged user/az-2/worker into user/az-1/parent", "integrate_and_close: success", "append_evidence: success"} {
		if !strings.Contains(output, want) {
			t.Fatalf("output missing %q:\n%s", want, output)
		}
	}
	for _, want := range []string{daemonclient.CommandTaskClose, daemonclient.CommandTaskAppendNotes} {
		if !containsString(*commands, want) {
			t.Fatalf("commands = %+v, want %s", *commands, want)
		}
	}
	for _, unexpected := range []string{daemonclient.CommandGitMerge, commandTaskClosePreflight, commandSessionStop, daemonclient.CommandWorktreeRemove, daemonclient.CommandTaskUpdateStatus} {
		if containsString(*commands, unexpected) {
			t.Fatalf("commands = %+v, did not expect client-side integration/cleanup command %s", *commands, unexpected)
		}
	}
	if len(*closeBudgets) != 1 {
		t.Fatalf("close budget count = %d, want 1", len(*closeBudgets))
	}
	if budget := (*closeBudgets)[0]; budget < issueCloseCleanupTimeout-10*time.Second {
		t.Fatalf("close budget = %s, want near %s", budget, issueCloseCleanupTimeout)
	}
}

func TestOrchestrateIntegrateApplyJSONReportsPreflightDeadlineWithRetry(t *testing.T) {
	child := naming.IssueID("az-2")
	parent := naming.IssueID("az-1")
	commands := make([]string, 0, 4)
	var readinessBudget time.Duration
	deps := &Dependencies{
		RepoDir:   "/repo-parent",
		ProjectID: protocol.DefaultProjectID,
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(ctx context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				commands = append(commands, req.Command)
				switch req.Command {
				case daemonclient.CommandWorktreeList:
					return responseWithJSON(req, map[string]any{
						"project_id": protocol.DefaultProjectID,
						"worktrees": []map[string]string{
							{"issue_id": parent.String(), "path": "/repo-parent", "branch": "user/az-1/parent"},
							{"issue_id": child.String(), "path": "/repo-az-2", "branch": "user/az-2/worker"},
						},
					}), nil
				case daemonclient.CommandTaskIntegrationReady:
					if deadline, ok := ctx.Deadline(); ok {
						readinessBudget = time.Until(deadline)
					}
					return protocol.ResponseEnvelope{}, context.DeadlineExceeded
				default:
					t.Fatalf("unexpected command: %s", req.Command)
				}
				return protocol.ResponseEnvelope{}, nil
			},
		}),
	}

	output, err := captureStdoutAllowError(t, func() error {
		return OrchestrateIntegrateCommand(deps, OrchestrateIntegrateOptions{IssueID: child.String(), Apply: true, JSON: true})
	})
	if err == nil {
		t.Fatal("expected preflight deadline error")
	}
	if !strings.Contains(err.Error(), "phase preflight for issue az-2") || !strings.Contains(err.Error(), "context deadline exceeded") {
		t.Fatalf("error = %v, want preflight phase attribution", err)
	}
	for _, want := range []string{`"apply": true`, `"applied": false`, `"name": "preflight"`, `"status": "failed"`, "context deadline exceeded", "no integration/cleanup/status mutation started", "az orchestrate integrate --issue az-2 --apply"} {
		if !strings.Contains(output, want) {
			t.Fatalf("output missing %q:\n%s", want, output)
		}
	}
	for _, unexpected := range []string{daemonclient.CommandTaskClose, daemonclient.CommandTaskAppendNotes, daemonclient.CommandGitMerge, commandSessionStop, daemonclient.CommandWorktreeRemove, daemonclient.CommandTaskUpdateStatus} {
		if containsString(commands, unexpected) {
			t.Fatalf("commands = %+v, did not expect mutation command %s", commands, unexpected)
		}
	}
	if readinessBudget < issueCloseCleanupTimeout-10*time.Second {
		t.Fatalf("readiness budget = %s, want near %s", readinessBudget, issueCloseCleanupTimeout)
	}
}

func TestOrchestrateIntegrateApplyRequiresCompletionEvidence(t *testing.T) {
	deps, commands, _ := orchestrateIntegrateApplyDeps(t, "missing_evidence")
	output, err := captureStdoutAllowError(t, func() error {
		return OrchestrateIntegrateCommand(deps, OrchestrateIntegrateOptions{IssueID: "az-2", Apply: true, JSON: true})
	})
	if err == nil {
		t.Fatal("expected missing completion evidence error")
	}
	if !strings.Contains(output, `"apply": true`) || !strings.Contains(output, `"applied": false`) || !strings.Contains(output, `"name": "completion_evidence"`) || !strings.Contains(output, `"recovery"`) {
		t.Fatalf("output missing structured failure:\n%s", output)
	}
	for _, unexpected := range []string{daemonclient.CommandGitMerge, daemonclient.CommandTaskClose, commandTaskClosePreflight, commandSessionStop, daemonclient.CommandWorktreeRemove, daemonclient.CommandTaskUpdateStatus} {
		if containsString(*commands, unexpected) {
			t.Fatalf("commands = %+v, did not expect %s", *commands, unexpected)
		}
	}
}

func TestOrchestrateIntegrateApplyDaemonIntegrationFailureStopsBeforeAppend(t *testing.T) {
	deps, commands, _ := orchestrateIntegrateApplyDeps(t, "merge_project")
	output, err := captureStdoutAllowError(t, func() error {
		return OrchestrateIntegrateCommand(deps, OrchestrateIntegrateOptions{Project: "azedarach", IssueID: "az-2", Apply: true})
	})
	if err == nil {
		t.Fatal("expected daemon integration failure")
	}
	if !strings.Contains(output, "integrate_and_close: failed") ||
		!strings.Contains(output, "az branch merge --project azedarach --source az-2 --target <issue-id|base>") ||
		!strings.Contains(output, "az orchestrate integrate --project azedarach --issue az-2 --apply") {
		t.Fatalf("output missing daemon integration failure recovery:\n%s", output)
	}
	if strings.Contains(output, "az orchestrate integrate --issue az-2 --apply") {
		t.Fatalf("output included unscoped project retry recovery:\n%s", output)
	}
	if containsString(*commands, daemonclient.CommandTaskAppendNotes) || containsString(*commands, daemonclient.CommandGitMerge) || containsString(*commands, commandTaskClosePreflight) || containsString(*commands, commandSessionStop) || containsString(*commands, daemonclient.CommandWorktreeRemove) || containsString(*commands, daemonclient.CommandTaskUpdateStatus) {
		t.Fatalf("commands = %+v, append/client integration cleanup should not run after daemon integration failure", *commands)
	}
}

func TestOrchestrateIntegrateApplySurfacesDaemonIntegrationConflict(t *testing.T) {
	deps, commands, _ := orchestrateIntegrateApplyDeps(t, "merge_conflict")
	output, err := captureStdoutAllowError(t, func() error {
		return OrchestrateIntegrateCommand(deps, OrchestrateIntegrateOptions{IssueID: "az-2", Apply: true, JSON: true})
	})
	if err == nil {
		t.Fatal("expected daemon integration conflict")
	}
	if !strings.Contains(output, `"applied": false`) || !strings.Contains(output, "README.md") || !strings.Contains(output, `"name": "integrate_and_close"`) {
		t.Fatalf("output missing daemon integration conflict:\n%s", output)
	}
	if containsString(*commands, daemonclient.CommandTaskAppendNotes) || containsString(*commands, daemonclient.CommandGitMerge) || containsString(*commands, commandTaskClosePreflight) || containsString(*commands, commandSessionStop) || containsString(*commands, daemonclient.CommandWorktreeRemove) || containsString(*commands, daemonclient.CommandTaskUpdateStatus) {
		t.Fatalf("commands = %+v, append/client integration cleanup should not run after daemon integration conflict", *commands)
	}
}

func TestOrchestrateIntegrateApplyReportsDaemonCloseFailure(t *testing.T) {
	deps, commands, _ := orchestrateIntegrateApplyDeps(t, "close_issue")
	output, err := captureStdoutAllowError(t, func() error {
		return OrchestrateIntegrateCommand(deps, OrchestrateIntegrateOptions{IssueID: "az-2", Apply: true})
	})
	if err == nil {
		t.Fatal("expected daemon close failure")
	}
	if !strings.Contains(output, "integrate_and_close: failed") || !strings.Contains(output, "Recovery:") {
		t.Fatalf("output missing daemon close failure:\n%s", output)
	}
	if !containsString(*commands, daemonclient.CommandTaskClose) || containsString(*commands, daemonclient.CommandTaskAppendNotes) {
		t.Fatalf("commands = %+v, want daemon close without append after close failure", *commands)
	}
	for _, unexpected := range []string{daemonclient.CommandGitMerge, commandTaskClosePreflight, commandSessionStop, daemonclient.CommandWorktreeRemove, daemonclient.CommandTaskUpdateStatus} {
		if containsString(*commands, unexpected) {
			t.Fatalf("commands = %+v, did not expect client-side integration/cleanup command %s", *commands, unexpected)
		}
	}
}

func orchestrateIntegrateApplyDeps(t *testing.T, failStep string) (*Dependencies, *[]string, *[]time.Duration) {
	t.Helper()
	child := naming.IssueID("az-2")
	parent := naming.IssueID("az-1")
	commands := make([]string, 0, 16)
	closeBudgets := make([]time.Duration, 0, 1)
	deps := &Dependencies{
		RepoDir:   "/repo-parent",
		ProjectID: protocol.DefaultProjectID,
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(ctx context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				commands = append(commands, req.Command)
				switch req.Command {
				case daemonclient.CommandWorktreeList:
					return responseWithJSON(req, map[string]any{
						"project_id": protocol.DefaultProjectID,
						"worktrees": []map[string]string{
							{"issue_id": parent.String(), "path": "/repo-parent", "branch": "user/az-1/parent"},
							{"issue_id": child.String(), "path": "/repo-az-2", "branch": "user/az-2/worker"},
						},
					}), nil
				case daemonclient.CommandTaskIntegrationReady:
					if failStep == "missing_evidence" {
						return responseWithJSON(req, daemonclient.TaskIntegrationReadiness{
							IssueID:       child.String(),
							ParentIssueID: parent.String(),
							Ready:         false,
							Reasons: []string{
								"issue az-2 is not closed",
								"no worker-integration-ready mailbox event found under parent az-1 for az-2",
							},
						}), nil
					}
					return responseWithJSON(req, daemonclient.TaskIntegrationReadiness{
						IssueID:       child.String(),
						ParentIssueID: parent.String(),
						Ready:         true,
					}), nil
				case daemonclient.CommandTaskMergeBaseTarget:
					return responseWithJSON(req, daemonclient.TaskMergeBaseTarget{
						IssueID:        child.String(),
						TargetID:       parent.String(),
						Branch:         "user/az-1/parent",
						WorktreePath:   "/repo-parent",
						BranchAttached: true,
						AncestorChain:  []string{parent.String()},
					}), nil
				case daemonclient.CommandTaskList:
					body, err := marshalTaskListBody([]domain.Task{
						{ID: parent, Status: domain.StatusInProgress, Type: domain.TypeTask},
						{ID: child, Status: domain.StatusDone, Type: domain.TypeTask, ParentID: &parent, HasTmuxSession: true, HasWorktree: true},
					})
					if err != nil {
						t.Fatalf("marshal task list response: %v", err)
					}
					return protocol.ResponseEnvelope{ProtocolVersion: req.ProtocolVersion, RequestID: req.RequestID, Kind: protocol.EnvelopeKindResponse, Meta: req.Meta, OK: true, Body: body}, nil
				case daemonclient.CommandGitStatus:
					return responseWithJSON(req, map[string]any{"status": gitservice.GitStatus{HasChanges: false}}), nil
				case daemonclient.CommandGitFetch:
					return responseWithJSON(req, daemonclient.GitCommandResponse{Worktree: "/repo-parent", Remote: "origin"}), nil
				case daemonclient.CommandGitCheckout:
					return responseWithJSON(req, daemonclient.GitCommandResponse{Worktree: "/repo-parent", Branch: "user/az-1/parent"}), nil
				case daemonclient.CommandGitMerge:
					if failStep == "merge" {
						return protocol.ResponseEnvelope{}, fmt.Errorf("merge failed")
					}
					if failStep == "merge_conflict" {
						return responseWithJSON(req, daemonclient.GitMergeCommandResponse{
							Worktree: "/repo-parent",
							Branch:   "user/az-2/worker",
							Result: gitservice.MergeResult{
								Success:       false,
								HasConflicts:  true,
								ConflictFiles: []string{"README.md"},
								Message:       "CONFLICT (content): Merge conflict in README.md",
							},
						}), nil
					}
					return responseWithJSON(req, daemonclient.GitMergeCommandResponse{
						Worktree: "/repo-parent",
						Branch:   "user/az-2/worker",
						Result:   gitservice.MergeResult{Success: true},
					}), nil
				case daemonclient.CommandTaskClose:
					if failStep == "merge_project" && req.Meta.ProjectID.String() != "azedarach" {
						t.Fatalf("task.close project = %q, want azedarach", req.Meta.ProjectID)
					}
					if deadline, ok := ctx.Deadline(); ok {
						closeBudgets = append(closeBudgets, time.Until(deadline))
					}
					var body struct {
						IntegrateBeforeClose bool `json:"integrate_before_close"`
					}
					if err := json.Unmarshal(req.Body, &body); err != nil {
						t.Fatalf("decode task close body: %v", err)
					}
					if !body.IntegrateBeforeClose {
						t.Fatalf("task.close integrate_before_close = false, want true")
					}
					if failStep == "merge" || failStep == "merge_project" {
						return protocol.ResponseEnvelope{}, fmt.Errorf("integrate before closing az-2: merge failed")
					}
					if failStep == "merge_conflict" {
						return protocol.ResponseEnvelope{}, fmt.Errorf("integrate before closing az-2: merge user/az-2/worker into user/az-1/parent failed: CONFLICT (content): Merge conflict in README.md")
					}
					if failStep == "close_issue" {
						return protocol.ResponseEnvelope{}, fmt.Errorf("close failed")
					}
					return responseWithJSON(req, daemonclient.TaskCloseResult{
						TaskID:                 child.String(),
						Status:                 string(domain.StatusDone),
						IntegrationRequested:   true,
						Integrated:             true,
						IntegratedSourceBranch: "user/az-2/worker",
						IntegratedTargetBranch: "user/az-1/parent",
						SessionStopped:         true,
						WorktreeRemoved:        true,
						Revision:               3,
					}), nil
				case daemonclient.CommandTaskAppendNotes:
					return responseWithJSON(req, map[string]any{}), nil
				default:
					t.Fatalf("unexpected command: %s", req.Command)
				}
				return protocol.ResponseEnvelope{}, nil
			},
		}),
	}
	return deps, &commands, &closeBudgets
}

func TestOrchestrateCloseSessionCommandStopsSession(t *testing.T) {
	var gotCommand string
	deps := &Dependencies{
		ProjectID: protocol.DefaultProjectID,
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				gotCommand = req.Command
				return responseWithOutput(req, "stopped\n"), nil
			},
		}),
	}
	output := captureStdout(t, func() error {
		return OrchestrateCloseSessionCommand(deps, OrchestrateCloseSessionOptions{IssueID: "az-2"})
	})
	if gotCommand != commandSessionStop {
		t.Fatalf("command = %q, want %q", gotCommand, commandSessionStop)
	}
	if !strings.Contains(output, "az issue close --id az-2") || strings.Contains(output, "--cleanup") {
		t.Fatalf("output = %q, want cleanup-close guidance", output)
	}
}

func TestNextMailboxSeq(t *testing.T) {
	events := []protocol.MailEvent{
		{Seq: 11},
		{Seq: 18},
		{Seq: 14},
	}
	if got := nextMailboxSeq(events, 10); got != 18 {
		t.Fatalf("nextMailboxSeq = %d, want 18", got)
	}
	if got := nextMailboxSeq(nil, 10); got != 10 {
		t.Fatalf("nextMailboxSeq(nil) = %d, want 10", got)
	}
}

func responseWithTaskOwnershipMutation(t *testing.T, req protocol.RequestEnvelope) protocol.ResponseEnvelope {
	t.Helper()
	var body daemonclient.TaskOwnershipRequest
	if err := json.Unmarshal(req.Body, &body); err != nil {
		t.Fatalf("decode ownership body: %v", err)
	}
	if body.TaskID == "" {
		t.Fatal("ownership body missing task_id")
	}
	if strings.TrimSpace(body.OwnerID) == "" {
		t.Fatal("ownership body missing owner_id")
	}
	task := domain.Task{
		ID: body.TaskID,
		Ownership: &domain.IssueOwnership{
			OwnerID:   body.OwnerID,
			OwnerKind: body.OwnerKind,
		},
	}
	return responseWithJSON(req, task)
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func captureStdoutAllowError(t *testing.T, fn func() error) (string, error) {
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

	var buf strings.Builder
	_, copyErr := io.Copy(&buf, r)
	os.Stdout = oldStdout
	runErr := <-resultCh
	if copyErr != nil {
		t.Fatalf("copy stdout: %v", copyErr)
	}
	return buf.String(), runErr
}
