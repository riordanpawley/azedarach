package daemon

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	appconfig "github.com/riordanpawley/azedarach/internal/config"
	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	daemonhandlers "github.com/riordanpawley/azedarach/internal/daemon/handlers"
	daemonops "github.com/riordanpawley/azedarach/internal/daemon/operations"
	"github.com/riordanpawley/azedarach/internal/daemon/publish"
	daemonstate "github.com/riordanpawley/azedarach/internal/daemon/state"
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/naming"
	"github.com/riordanpawley/azedarach/internal/services/attachment"
	"github.com/riordanpawley/azedarach/internal/services/git"
	"github.com/riordanpawley/azedarach/internal/services/issues"
	"github.com/riordanpawley/azedarach/internal/services/tmux"
)

type testTmuxRunner struct {
	mu                  sync.Mutex
	sessions            map[string]bool
	panes               map[string][]string
	commands            [][]string
	newSessionCalls     int
	listSessionsCalls   int
	listPanesCalls      int
	listSessionsEntered chan struct{}
	listSessionsRelease chan struct{}
	killEntered         chan struct{}
	killRelease         chan struct{}
}

type sessionStartCompensationTmuxRunner struct {
	live    bool
	killErr error
}

func (r *sessionStartCompensationTmuxRunner) Run(_ context.Context, args ...string) (string, error) {
	if len(args) == 0 {
		return "", errors.New("missing tmux args")
	}
	switch args[0] {
	case "kill-session":
		if r.killErr != nil {
			return "", r.killErr
		}
		r.live = false
		return "", nil
	case "has-session":
		if r.live {
			return "", nil
		}
		return "", errors.New("missing session")
	default:
		return "", nil
	}
}

func TestSessionStartFailureCompensationMatchesActualRuntime(t *testing.T) {
	for _, tc := range []struct {
		name, failure string
		seedPhysical  bool
		winnerAhead   bool
		durableFail   bool
		noEvent       bool
		killErr       error
		want          daemonstate.SessionState
		wantDesired   daemonstate.SessionState
	}{
		{name: "physical write failure cleanup succeeds", failure: "physical-write", want: daemonstate.SessionStateStopped},
		{name: "post-observation lease failure cleanup fails", failure: "lease", seedPhysical: true, killErr: errors.New("kill failed"), want: daemonstate.SessionStateRunning},
		{name: "newer physical winner defeats stopped compensation", failure: "higher-version-race", seedPhysical: true, winnerAhead: true, want: daemonstate.SessionStateRunning, wantDesired: daemonstate.SessionStateStopped},
		{name: "durable transaction failure reloads existing desired", failure: "transaction-failure", seedPhysical: true, durableFail: true, noEvent: true, want: daemonstate.SessionStateStarting},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			const projectID, issueID, sessionID = "p", "root", "az-root"
			runtimeDBPath := filepath.Join(t.TempDir(), "runtime.db")
			runtimeStore := daemonstate.NewRuntimeStateStoreAtPath(runtimeDBPath, slog.Default())
			t.Cleanup(func() { _ = runtimeStore.Close() })
			seedAt := time.Date(2000, time.January, 1, 0, 0, 0, 0, time.UTC)
			seed := daemonstate.Session{ID: sessionID, IssueID: issueID, State: daemonstate.SessionStateStarting, UpdatedAt: seedAt}
			if err := runtimeStore.UpsertSessionState(ctx, projectID, seed); err != nil {
				t.Fatal(err)
			}
			if tc.seedPhysical {
				physicalAt := seedAt.Add(time.Second)
				if tc.winnerAhead {
					physicalAt = time.Date(2200, time.January, 1, 0, 0, 0, 0, time.UTC)
				}
				if _, _, err := runtimeStore.ApplyPhysicalSessionObservation(ctx, daemonstate.PhysicalSessionObservation{ProjectID: projectID, SessionID: sessionID, ObservedState: daemonstate.SessionStateRunning, Activity: "busy", ActivitySource: "session", UpdatedAt: physicalAt}); err != nil {
					t.Fatal(err)
				}
			}
			if tc.durableFail {
				db, err := sql.Open("sqlite", runtimeDBPath)
				if err != nil {
					t.Fatal(err)
				}
				defer db.Close()
				if _, err := db.Exec(`CREATE TRIGGER inject_compensation_failure BEFORE UPDATE ON daemon_session_projections BEGIN SELECT RAISE(ABORT,'injected compensation failure'); END`); err != nil {
					t.Fatal(err)
				}
			}
			transient := daemonstate.NewStore()
			_, _ = transient.ForceUpsertSession(projectID, sessionID, issueID, daemonstate.SessionStateStarting)
			runner := &sessionStartCompensationTmuxRunner{live: true, killErr: tc.killErr}
			d := &Daemon{cfg: Config{RepoDir: ".", Logger: slog.Default()}, tmux: tmux.NewClient(runner, slog.Default()), sessionStore: transient,
				runtimeStoresByProject: map[string]*daemonstate.RuntimeStateStore{projectID: runtimeStore}, runtimeStoresByRoot: map[string]*daemonstate.RuntimeStateStore{".": runtimeStore}, hub: publish.NewHub(8, 8, slog.Default())}
			ch, cancel := d.hub.Subscribe(projectID, 0)
			defer cancel()
			d.compensateSessionStartFailure(ctx, protocol.RequestEnvelope{Meta: protocol.Metadata{ProjectID: projectID}}, projectID, sessionID, issueID, issueResourceLifecycleContext{}, "busy", "session")
			intent, found, err := runtimeStore.GetSessionIntent(ctx, projectID, daemonstate.SessionRoleWorker, daemonstate.SessionScopeIssue, issueID)
			wantDesired := tc.wantDesired
			if wantDesired == "" {
				wantDesired = tc.want
			}
			wantObserved := tc.want
			if tc.durableFail {
				wantObserved = daemonstate.SessionStateRunning
			}
			if err != nil || !found || intent.State != wantDesired || intent.ObservedState != wantObserved {
				t.Fatalf("%s durable intent=%+v found=%v err=%v", tc.failure, intent, found, err)
			}
			physical, found, err := runtimeStore.GetPhysicalSessionObservation(ctx, projectID, sessionID)
			if err != nil || !found || physical.ObservedState != wantObserved {
				t.Fatalf("%s physical=%+v found=%v err=%v", tc.failure, physical, found, err)
			}
			got, err := transient.Session(projectID, sessionID)
			if err != nil || got.State != tc.want {
				t.Fatalf("%s transient=%+v err=%v", tc.failure, got, err)
			}
			if tc.noEvent {
				select {
				case event := <-ch:
					t.Fatalf("%s unexpected event=%+v", tc.failure, event)
				case <-time.After(50 * time.Millisecond):
				}
				return
			}
			events := collectSessionProjectionEvents(t, ch, 1)
			var body protocol.SessionProjectionEventBody
			if len(events) != 1 || json.Unmarshal(events[0].Body, &body) != nil || daemonstate.SessionState(body.Session.State) != wantDesired {
				t.Fatalf("%s final events=%+v", tc.failure, events)
			}
		})
	}
}

func TestSessionStartFailureCompensationPurgesManagedIdentityOnlyForStoppedWinner(t *testing.T) {
	for _, tc := range []struct {
		name              string
		seedIntent        bool
		seedNewerRunning  bool
		injectStoreError  bool
		wantObserved      daemonstate.SessionState
		wantIdentityFound bool
	}{
		{name: "stopped with intent", seedIntent: true, wantObserved: daemonstate.SessionStateStopped},
		{name: "stopped without intent is idempotent", wantObserved: daemonstate.SessionStateStopped},
		{name: "newer running winner", seedIntent: true, seedNewerRunning: true, wantObserved: daemonstate.SessionStateRunning, wantIdentityFound: true},
		{name: "store error", seedIntent: true, injectStoreError: true, wantIdentityFound: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			const projectID, issueID, sessionID = "compensation-project", "compensation-issue", "az-compensation"
			runtimeDBPath := filepath.Join(t.TempDir(), "runtime.db")
			runtimeStore := daemonstate.NewRuntimeStateStoreAtPath(runtimeDBPath, slog.Default())
			t.Cleanup(func() { _ = runtimeStore.Close() })
			if tc.seedIntent {
				if err := runtimeStore.UpsertSessionState(ctx, projectID, daemonstate.Session{
					ID: sessionID, IssueID: issueID, State: daemonstate.SessionStateStarting,
					UpdatedAt: time.Date(2000, time.January, 1, 0, 0, 0, 0, time.UTC),
				}); err != nil {
					t.Fatal(err)
				}
			}
			if tc.seedNewerRunning {
				if _, _, err := runtimeStore.ApplyPhysicalSessionObservation(ctx, daemonstate.PhysicalSessionObservation{
					ProjectID: projectID, SessionID: sessionID, ObservedState: daemonstate.SessionStateRunning,
					Activity: "busy", ActivitySource: "session",
					UpdatedAt: time.Date(2200, time.December, 31, 23, 59, 59, 0, time.UTC),
				}); err != nil {
					t.Fatal(err)
				}
			}
			if tc.injectStoreError {
				db, err := sql.Open("sqlite", runtimeDBPath)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = db.Close() })
				if _, err := db.Exec(`CREATE TRIGGER inject_identity_compensation_failure BEFORE UPDATE ON daemon_session_projections BEGIN SELECT RAISE(ABORT,'injected compensation failure'); END`); err != nil {
					t.Fatal(err)
				}
			}
			d := &Daemon{
				cfg:                    Config{RepoDir: ".", Logger: slog.Default()},
				tmux:                   tmux.NewClient(&sessionStartCompensationTmuxRunner{live: true}, slog.Default()),
				runtimeStoresByProject: map[string]*daemonstate.RuntimeStateStore{projectID: runtimeStore},
				runtimeStoresByRoot:    map[string]*daemonstate.RuntimeStateStore{".": runtimeStore},
				hub:                    publish.NewHub(8, 8, slog.Default()),
			}
			d.recordManagedAgentIdentityProjection(daemonstate.ManagedAgentIdentity{
				ProjectID: projectID, SessionID: sessionID, LogicalPaneID: "agent", TmuxPaneID: "7",
				PanePID: 123, AgentIncarnation: "compensation-incarnation",
				ObservedAt: time.Date(2000, time.January, 1, 0, 0, 0, 0, time.UTC),
			}, true)

			note := d.compensateSessionStartFailure(ctx, protocol.RequestEnvelope{Meta: protocol.Metadata{ProjectID: projectID}},
				projectID, sessionID, issueID, issueResourceLifecycleContext{}, "busy", "session")
			if tc.injectStoreError {
				if !strings.Contains(note, "failed-start durable session compensation also failed") {
					t.Fatalf("compensation note = %q, want durable failure", note)
				}
			} else {
				observation, found, err := runtimeStore.GetPhysicalSessionObservation(ctx, projectID, sessionID)
				if err != nil || !found || observation.ObservedState != tc.wantObserved {
					t.Fatalf("physical winner = %+v found=%t err=%v, want %s", observation, found, err, tc.wantObserved)
				}
			}

			_, found := d.projectedManagedAgentIdentity(projectID, sessionID, "agent")
			if found != tc.wantIdentityFound {
				t.Fatalf("managed identity found = %t, want %t", found, tc.wantIdentityFound)
			}
		})
	}
}

func TestSessionStartFailureCompensationGivesLaterStagesIndependentContexts(t *testing.T) {
	runner := &sessionStartCompensationTmuxRunner{live: true}
	contextCalls := 0
	cleanupEntered := false
	d := &Daemon{
		cfg: Config{
			RepoDir: t.TempDir(),
			IssueResources: appconfig.IssueResourcesConfig{
				FailedStartCleanupCommands: []string{"blocked cleanup"},
			},
			Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		},
		tmux: tmux.NewClient(runner, slog.Default()),
		sessionStartCompensationContext: func(parent context.Context) (context.Context, context.CancelFunc) {
			contextCalls++
			stageCtx, cancel := context.WithCancel(context.WithoutCancel(parent))
			if contextCalls == 1 {
				cancel()
			}
			return stageCtx, cancel
		},
		sessionShellRun: func(ctx context.Context, _, _, command string, _ []string) ([]byte, error) {
			if command != "blocked cleanup" {
				t.Fatalf("cleanup command = %q", command)
			}
			cleanupEntered = true
			<-ctx.Done()
			return nil, ctx.Err()
		},
	}

	note := d.compensateSessionStartFailure(
		context.Background(),
		protocol.RequestEnvelope{Meta: protocol.Metadata{ProjectID: protocol.DefaultProjectID}},
		protocol.DefaultProjectID, "az-a", "a", issueResourceLifecycleContext{}, "busy", "hooks",
	)
	if !cleanupEntered || !strings.Contains(note, "context canceled") {
		t.Fatalf("early cleanup note = %q entered=%t", note, cleanupEntered)
	}
	if runner.live {
		t.Fatal("later tmux cleanup did not execute after the early stage exhausted its context")
	}
	if contextCalls < 2 {
		t.Fatalf("compensation context calls = %d, want independent contexts for later stages", contextCalls)
	}
}

func TestRunSessionShellCancellationDrainsDescendantOutputPipes(t *testing.T) {
	ready := make(chan os.Signal, 1)
	signal.Notify(ready, syscall.SIGUSR1)
	t.Cleanup(func() { signal.Stop(ready) })

	pidFile := strings.TrimSpace(os.Getenv("AZEDARACH_DTV_TEST_PID_FILE"))
	if pidFile == "" {
		pidFile = filepath.Join(t.TempDir(), "cleanup-child.pid")
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	d := &Daemon{}
	type runResult struct {
		output []byte
		err    error
	}
	done := make(chan runResult, 1)
	workdir := t.TempDir()
	command := `/bin/sh -c 'trap "" TERM; printf "%s" "$$" > "$AZEDARACH_TEST_CHILD_PID_FILE"; kill -USR1 "$AZEDARACH_TEST_PARENT_PID"; while :; do sleep 60; done' & wait`
	go func() {
		output, err := d.runSessionShell(ctx, "/bin/sh", workdir, command, append(os.Environ(),
			"AZEDARACH_TEST_PARENT_PID="+strconv.Itoa(os.Getpid()),
			"AZEDARACH_TEST_CHILD_PID_FILE="+pidFile,
		))
		done <- runResult{output: output, err: err}
	}()

	<-ready
	cancel()
	result := <-done
	if !errors.Is(result.err, context.Canceled) {
		t.Fatalf("runSessionShell error = %v, want context cancellation; output=%q", result.err, result.output)
	}
	childPID, err := os.ReadFile(pidFile)
	if err != nil || strings.TrimSpace(string(childPID)) == "" {
		t.Fatalf("managed cleanup descendant pid = %q err=%v", childPID, err)
	}
}

func TestRunSessionShellNormalExitCleansRetainedDescendantGroup(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "cleanup-worker.pid")
	cleanupNeeded := true
	t.Cleanup(func() {
		if !cleanupNeeded {
			return
		}
		pidBytes, err := os.ReadFile(pidFile)
		if err != nil {
			return
		}
		for _, field := range strings.Fields(string(pidBytes)) {
			pid, err := strconv.Atoi(field)
			if err == nil {
				_ = syscall.Kill(pid, syscall.SIGKILL)
			}
		}
	})
	d := &Daemon{}
	workdir := t.TempDir()
	readyFIFO := filepath.Join(workdir, "retained-descendant-ready")
	if err := syscall.Mkfifo(readyFIFO, 0o600); err != nil {
		t.Fatalf("create retained descendant readiness fifo: %v", err)
	}
	supervisor := filepath.Join(workdir, "retained-descendant.sh")
	if err := os.WriteFile(supervisor, []byte(`#!/bin/sh
trap 'kill -TERM "$worker" 2>/dev/null; wait "$worker"; exit 0' TERM
/bin/sh -c 'trap "exit 0" TERM; while :; do sleep 60; done' &
worker=$!
printf '%s %s' "$$" "$worker" > "$AZEDARACH_TEST_CHILD_PID_FILE"
printf 'ready\n' > "$AZEDARACH_TEST_READY_FIFO"
wait "$worker"
`), 0o700); err != nil {
		t.Fatalf("write retained descendant supervisor: %v", err)
	}
	command := `/bin/sh "$AZEDARACH_TEST_SUPERVISOR" & read ready < "$AZEDARACH_TEST_READY_FIFO"`
	output, err := d.runSessionShell(context.Background(), "/bin/sh", workdir, command, append(os.Environ(),
		"AZEDARACH_TEST_CHILD_PID_FILE="+pidFile,
		"AZEDARACH_TEST_READY_FIFO="+readyFIFO,
		"AZEDARACH_TEST_SUPERVISOR="+supervisor,
	))
	if err != nil {
		t.Fatalf("runSessionShell error = %v, want successful direct-shell exit; output=%q", err, output)
	}
	pidBytes, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatalf("read retained descendant pid: %v", err)
	}
	pidFields := strings.Fields(string(pidBytes))
	if len(pidFields) != 2 {
		t.Fatalf("retained descendant pid file = %q, want supervisor and worker pids", pidBytes)
	}
	pid, err := strconv.Atoi(pidFields[1])
	if err != nil {
		t.Fatalf("parse retained descendant pid %q: %v", pidBytes, err)
	}
	if err := syscall.Kill(pid, 0); !errors.Is(err, syscall.ESRCH) {
		t.Fatalf("retained descendant %d still exists after direct shell exit: %v", pid, err)
	}
	cleanupNeeded = false
}

func TestCodexAppServerLaunchUsesStockRemoteTUIAndSupervisedResume(t *testing.T) {
	d := &Daemon{cfg: Config{
		CLITool:                    "codex",
		CodexAppServer:             true,
		DangerouslySkipPermissions: true,
		SessionShell:               "sh",
	}}
	command := d.buildSessionLaunchCommand(protocol.DefaultProjectID, "dbc", "az-dbc", true, nil, "start here")
	for _, want := range []string{
		"codex " + codexFloopFailOpenConfigExpansion + " app-server daemon start",
		"codex " + codexFloopFailOpenConfigExpansion + " --remote unix:// --dangerously-bypass-approvals-and-sandbox",
		"codex " + codexFloopFailOpenConfigExpansion + " resume --remote unix:// --dangerously-bypass-approvals-and-sandbox --last",
		"codex mcp get --json floop",
		"codex " + codexFloopFailOpenConfigExpansion + " --remote unix:// --dangerously-bypass-approvals-and-sandbox --",
		codexFloopFailOpenConfig,
		"__az_codex_remote_failures",
	} {
		if !strings.Contains(command, want) {
			t.Fatalf("launch command missing %q: %s", want, command)
		}
	}
	if strings.Contains(command, "native-codex-client") {
		t.Fatalf("launch command still uses removed custom client: %s", command)
	}

	stableDir := filepath.Join(t.TempDir(), "stable daemon cwd")
	if err := os.MkdirAll(stableDir, 0o700); err != nil {
		t.Fatal(err)
	}
	supervisor := codexAppServerSupervisedCommand("codex", stableDir, "codex --remote unix://", "codex resume --remote unix:// --last")
	if out, err := exec.Command("sh", "-n", "-c", supervisor).CombinedOutput(); err != nil {
		t.Fatalf("supervisor shell syntax: %v\n%s\n%s", err, out, supervisor)
	}
	trace := filepath.Join(t.TempDir(), "trace")
	fakeCodex := `codex() { printf '%s|%s\n' "$*" "$PWD" >> "$TRACE"; case "$*" in "mcp get --json floop") return 0 ;; "-c mcp_servers.floop.required=false app-server daemon start") return 0 ;; "-c mcp_servers.floop.required=false app-server daemon version") return 1 ;; "-c mcp_servers.floop.required=false app-server daemon restart") return 0 ;; "--remote unix://") return 1 ;; "resume --remote unix:// --last") return 0 ;; *) return 2 ;; esac; }; `
	cmd := exec.Command("sh", "-c", fakeCodex+supervisor)
	cmd.Env = append(os.Environ(), "TRACE="+trace, "AZEDARACH_DAEMON_SCOPE=global")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("supervisor execution: %v\n%s", err, out)
	}
	data, err := os.ReadFile(trace)
	if err != nil {
		t.Fatal(err)
	}
	got := string(data)
	if !strings.Contains(got, "--remote unix://") || !strings.Contains(got, "resume --remote unix:// --last") {
		t.Fatalf("supervisor trace = %q", got)
	}
	for _, action := range []string{"app-server daemon start", "app-server daemon restart"} {
		if !strings.Contains(got, action+"|"+stableDir) {
			t.Fatalf("supervisor trace = %q, want %s from stable cwd %q", got, action, stableDir)
		}
	}
	for _, signal := range []string{"kill -TERM", "kill -KILL", "kill -HUP", "kill -INT"} {
		if strings.Contains(supervisor, signal) {
			t.Fatalf("supervisor directly terminates processes instead of preserving native daemon ownership: %s", supervisor)
		}
	}
}

func TestCodexAppServerRecoveryCoordinatesConcurrentSupervisors(t *testing.T) {
	stableDir := t.TempDir()
	supervisor := codexAppServerSupervisedCommand("codex", stableDir, "codex first", "codex resume")
	trace := filepath.Join(t.TempDir(), "trace")
	healthy := filepath.Join(t.TempDir(), "healthy")
	fakeCodex := `codex() {
  case "$*" in
    "mcp get --json floop"|"-c mcp_servers.floop.required=false app-server daemon start") return 0 ;;
    "-c mcp_servers.floop.required=false app-server daemon version") [ -f "$HEALTHY" ] ;;
    "-c mcp_servers.floop.required=false app-server daemon restart") printf 'restart\n' >> "$TRACE"; touch "$HEALTHY" ;;
    "first") return 1 ;;
    "resume") return 0 ;;
    *) return 2 ;;
  esac
}; `
	commands := []*exec.Cmd{exec.Command("sh", "-c", fakeCodex+supervisor), exec.Command("sh", "-c", fakeCodex+supervisor)}
	outputs := make([]bytes.Buffer, len(commands))
	for i, cmd := range commands {
		cmd.Env = append(os.Environ(), "TRACE="+trace, "HEALTHY="+healthy, "AZEDARACH_DAEMON_SCOPE=global")
		cmd.Stdout = &outputs[i]
		cmd.Stderr = &outputs[i]
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
	}
	for i, cmd := range commands {
		if err := cmd.Wait(); err != nil {
			t.Fatalf("concurrent supervisor: %v\n%s", err, outputs[i].Bytes())
		}
	}
	data, err := os.ReadFile(trace)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(string(data), "restart\n"); got != 1 {
		t.Fatalf("restart count = %d, want one globally coordinated recovery; trace=%q", got, data)
	}
}

func TestCodexAppServerRecoveryReclaimsDeadOwnerLock(t *testing.T) {
	stableDir := t.TempDir()
	lockPath := filepath.Join(stableDir, "codex-app-server-recovery.lock")
	if err := os.WriteFile(lockPath, []byte("999999999\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	supervisor := codexAppServerSupervisedCommand("codex", stableDir, "codex first", "codex resume")
	fakeCodex := `codex() { case "$*" in "mcp get --json floop"|"-c mcp_servers.floop.required=false app-server daemon start"|"-c mcp_servers.floop.required=false app-server daemon restart"|"resume") return 0 ;; "-c mcp_servers.floop.required=false app-server daemon version"|"first") return 1 ;; *) return 2 ;; esac; }; `
	cmd := exec.Command("sh", "-c", fakeCodex+supervisor)
	cmd.Env = append(os.Environ(), "AZEDARACH_DAEMON_SCOPE=global")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("recover dead owner lock: %v\n%s", err, output)
	}
	if _, err := os.Stat(lockPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("recovery lock remains after success: %v", err)
	}
}

func TestCodexAppServerSupervisorRefusesGlobalControlFromWorktreeScopedDaemon(t *testing.T) {
	stableDir := t.TempDir()
	trace := filepath.Join(t.TempDir(), "trace")
	supervisor := codexAppServerSupervisedCommand("codex", stableDir, "codex first", "codex resume")
	cmd := exec.Command("sh", "-c", `codex() { printf '%s\n' "$*" >> "$TRACE"; }; `+supervisor)
	cmd.Env = append(os.Environ(), "TRACE="+trace, "AZEDARACH_DAEMON_SCOPE=worktree")
	output, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("worktree-scoped supervisor unexpectedly succeeded: %s", output)
	}
	if !strings.Contains(string(output), "refusing to control the user-global Codex app-server") {
		t.Fatalf("worktree-scoped supervisor output = %q, want honest refusal", output)
	}
	if data, readErr := os.ReadFile(trace); readErr == nil && len(data) != 0 {
		t.Fatalf("worktree-scoped supervisor invoked Codex: %q", data)
	} else if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
		t.Fatal(readErr)
	}
}

func TestCodexAppServerSupervisorRequiresExplicitGlobalDaemonScope(t *testing.T) {
	stableDir := t.TempDir()
	trace := filepath.Join(t.TempDir(), "trace")
	supervisor := codexAppServerSupervisedCommand("codex", stableDir, "codex first", "codex resume")
	cmd := exec.Command("sh", "-c", `codex() { printf '%s\n' "$*" >> "$TRACE"; }; `+supervisor)
	cmd.Env = append(os.Environ(), "TRACE="+trace, "AZEDARACH_DAEMON_SCOPE=")
	output, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("unscoped supervisor unexpectedly succeeded: %s", output)
	}
	if data, readErr := os.ReadFile(trace); readErr == nil && len(data) != 0 {
		t.Fatalf("unscoped supervisor invoked Codex: %q", data)
	} else if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
		t.Fatal(readErr)
	}
}

func TestDaemonScopeTmuxEnvironmentUsesAuthoritativeConfig(t *testing.T) {
	for _, test := range []struct {
		name          string
		ambientScope  string
		scopedRuntime bool
		want          string
	}{
		{name: "global config ignores worktree ambient", ambientScope: "worktree", want: "global"},
		{name: "scoped config ignores global ambient", ambientScope: "global", scopedRuntime: true, want: "worktree"},
		{name: "scoped config ignores absent ambient", scopedRuntime: true, want: "worktree"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("AZEDARACH_DAEMON_SCOPE", test.ambientScope)
			d := &Daemon{cfg: Config{ScopedRuntime: test.scopedRuntime}}
			if got := d.daemonScopeTmuxEnvironment()["AZEDARACH_DAEMON_SCOPE"]; got != test.want {
				t.Fatalf("scope marker = %q, want %q", got, test.want)
			}
		})
	}
}

func TestCodexFloopPolicyFailsOpenForAbsentAndBrokenServers(t *testing.T) {
	for _, mode := range []string{"absent", "disabled", "unavailable", "crash", "timeout", "mid-handshake-close"} {
		t.Run(mode, func(t *testing.T) {
			trace := filepath.Join(t.TempDir(), "trace")
			script := `codex() {
  printf '%s\n' "$*" >> "$TRACE"
  if [ "$*" = "mcp get --json floop" ]; then
    [ "$MODE" != absent ]
    return
  fi
  if [ "$MODE" = absent ]; then
    [ "$*" = launch ]
    return
  fi
  [ "$*" = "-c mcp_servers.floop.required=false launch" ] || return 91
  [ "$MODE" = disabled ] && return
  printf 'optional MCP server floop %s; continuing\n' "$MODE" >&2
}
` + codexFloopFailOpenProbe("codex") + `; codex ` + codexFloopFailOpenConfigExpansion + ` launch`
			cmd := exec.Command("sh", "-c", script)
			cmd.Env = append(os.Environ(), "TRACE="+trace, "MODE="+mode)
			output, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("fail-open simulation: %v (%s)", err, output)
			}
			got, err := os.ReadFile(trace)
			if err != nil {
				t.Fatal(err)
			}
			if mode == "absent" {
				if strings.Contains(string(got), codexFloopFailOpenConfig) || !strings.Contains(string(got), "\nlaunch\n") {
					t.Fatalf("absent Floop trace = %q, want launch without synthesized server config", got)
				}
				return
			}
			if !strings.Contains(string(got), "-c "+codexFloopFailOpenConfig+" launch") {
				t.Fatalf("%s trace = %q, want optional Floop override", mode, got)
			}
			if mode == "disabled" {
				if len(output) != 0 {
					t.Fatalf("disabled Floop output = %q, want no failure diagnostic", output)
				}
				return
			}
			if !strings.Contains(string(output), "optional MCP server floop "+mode+"; continuing") {
				t.Fatalf("%s output = %q, want concise mode-specific diagnostic", mode, output)
			}
		})
	}
}

func TestManagedCodexSessionRolesCarryFloopFailOpenPolicy(t *testing.T) {
	d := &Daemon{cfg: Config{CLITool: "codex", SessionShell: "sh"}}
	prompts := map[string]string{
		"ordinary-contributor": buildStartWorkPrompt("dsp", string(domain.TypeBug), "fix", false, ""),
		"orchestrated-worker":  buildStartWorkPrompt("dsp", string(domain.TypeBug), "fix", true, "root"),
		"root-orchestrator":    buildRootedOrchestratorPrompt("root", string(domain.TypeEpic), "coordinate"),
	}
	for role, prompt := range prompts {
		t.Run(role, func(t *testing.T) {
			command := d.buildCLIToolCommand(protocol.DefaultProjectID, "dsp", "az-dsp", false, nil, prompt)
			for _, want := range []string{
				"codex mcp get --json floop",
				codexFloopFailOpenConfigVariable + "='-c " + codexFloopFailOpenConfig + "'",
				"codex " + codexFloopFailOpenConfigExpansion,
			} {
				if !strings.Contains(command, want) {
					t.Fatalf("%s launch missing %q: %s", role, want, command)
				}
			}
		})
	}
}

func TestCodexAppServerDisabledPreservesStandaloneLaunch(t *testing.T) {
	d := &Daemon{cfg: Config{CLITool: "codex", SessionShell: "sh"}}
	command := d.buildSessionLaunchCommand(protocol.DefaultProjectID, "dbc", "az-dbc", false, nil, "start here")
	if strings.Contains(command, "app-server") || strings.Contains(command, "--remote") {
		t.Fatalf("standalone launch unexpectedly uses app-server: %s", command)
	}
}

func newTestTmuxRunner(initialSession string) *testTmuxRunner {
	return &testTmuxRunner{
		sessions: map[string]bool{
			initialSession: true,
		},
		panes:       map[string][]string{},
		killEntered: make(chan struct{}),
		killRelease: make(chan struct{}),
	}
}

func (r *testTmuxRunner) Run(_ context.Context, args ...string) (string, error) {
	if len(args) == 0 {
		return "", errors.New("missing tmux args")
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	r.commands = append(r.commands, append([]string(nil), args...))

	switch args[0] {
	case "has-session":
		session := args[2]
		if r.sessions[session] {
			return "", nil
		}
		return "", errors.New("missing session")
	case "kill-session":
		session := args[2]
		delete(r.sessions, session)
		select {
		case <-r.killEntered:
		default:
			close(r.killEntered)
		}
		r.mu.Unlock()
		<-r.killRelease
		r.mu.Lock()
		return "", nil
	case "list-sessions":
		r.listSessionsCalls++
		names := make([]string, 0, len(r.sessions))
		for name := range r.sessions {
			names = append(names, name)
		}
		entered, release := r.listSessionsEntered, r.listSessionsRelease
		if entered != nil {
			select {
			case <-entered:
			default:
				close(entered)
			}
		}
		if release != nil {
			r.mu.Unlock()
			<-release
			r.mu.Lock()
		}
		return strings.Join(names, "\n"), nil
	case "list-panes":
		r.listPanesCalls++
		lines := make([]string, 0, len(r.sessions))
		for name := range r.sessions {
			panes := r.panes[name]
			if len(panes) == 0 {
				panes = []string{"%1"}
			}
			for _, pane := range panes {
				lines = append(lines, name+"\t"+pane)
			}
		}
		return strings.Join(lines, "\n"), nil
	case "new-session":
		session := args[3]
		r.newSessionCalls++
		r.sessions[session] = true
		return "", nil
	case "send-keys":
		return "", nil
	default:
		return "", nil
	}
}

func (r *testTmuxRunner) hasSession(name string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.sessions[name]
}

func (r *testTmuxRunner) listSessionCallCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.listSessionsCalls
}

func (r *testTmuxRunner) listPaneCallCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.listPanesCalls
}

type failingListSessionsTmuxRunner struct {
	listSessionsCalls int
}

func (r *failingListSessionsTmuxRunner) Run(_ context.Context, args ...string) (string, error) {
	if len(args) > 0 && args[0] == "list-sessions" {
		r.listSessionsCalls++
		return "", context.Canceled
	}
	return "", nil
}

type testGitRunner struct {
	worktreePath string
	branchName   string
}

type timeoutRuntimeReconciler struct {
	mu         sync.Mutex
	calls      int
	projectIDs []string
	issueIDs   [][]string
}

func (r *timeoutRuntimeReconciler) Reconcile(ctx context.Context, projectID string) (protocol.RuntimeReconcileResponseBody, error) {
	r.mu.Lock()
	r.calls++
	r.projectIDs = append(r.projectIDs, projectID)
	r.mu.Unlock()
	<-ctx.Done()
	return protocol.RuntimeReconcileResponseBody{ProjectID: naming.ProjectID(projectID)}, ctx.Err()
}

func (r *timeoutRuntimeReconciler) ReconcileIssues(ctx context.Context, projectID string, issueIDs []string) (protocol.RuntimeReconcileResponseBody, error) {
	r.mu.Lock()
	r.issueIDs = append(r.issueIDs, append([]string(nil), issueIDs...))
	r.mu.Unlock()
	return r.Reconcile(ctx, projectID)
}

func (r *timeoutRuntimeReconciler) snapshot() (calls int, projectIDs []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls, append([]string(nil), r.projectIDs...)
}

func (r *timeoutRuntimeReconciler) issueSnapshot() [][]string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([][]string, 0, len(r.issueIDs))
	for _, issueIDs := range r.issueIDs {
		out = append(out, append([]string(nil), issueIDs...))
	}
	return out
}

func (r *testGitRunner) Run(_ context.Context, args ...string) (string, error) {
	if len(args) >= 3 && args[0] == "worktree" && args[1] == "list" && args[2] == "--porcelain" {
		return "worktree " + r.worktreePath + "\nbranch refs/heads/" + r.branchName + "\n\n", nil
	}
	return "", nil
}

type worktreeCreateRunner struct {
	worktreePath     string
	branchName       string
	preexisting      bool
	failCreate       bool
	deletedFiles     []string
	porcelainStatus  string
	listCalls        int
	lsFilesDeleted   int
	statusCalls      int
	worktreeAddCalls int
	worktreeRemoved  bool
	branchDeleted    bool
	onWorktreeRemove func()
}

func (r *worktreeCreateRunner) Run(_ context.Context, args ...string) (string, error) {
	if len(args) >= 2 && args[0] == "config" && args[1] == "user.name" {
		return "testuser\n", nil
	}
	if len(args) >= 3 && args[0] == "worktree" && args[1] == "list" && args[2] == "--porcelain" {
		r.listCalls++
		if r.worktreeRemoved {
			return "", nil
		}
		if !r.preexisting && r.worktreeAddCalls == 0 {
			return "", nil
		}
		return "worktree " + r.worktreePath + "\nbranch refs/heads/" + r.branchName + "\n\n", nil
	}
	if len(args) >= 4 && args[0] == "-C" && args[1] == r.worktreePath && args[2] == "ls-files" && args[3] == "-d" {
		r.lsFilesDeleted++
		return strings.Join(r.deletedFiles, "\n"), nil
	}
	if len(args) >= 4 && args[0] == "-C" && args[1] == r.worktreePath && args[2] == "status" && args[3] == "--porcelain" {
		r.statusCalls++
		return r.porcelainStatus, nil
	}
	if len(args) >= 3 && args[0] == "worktree" && args[1] == "add" && args[2] == "-b" {
		r.worktreeAddCalls++
		r.worktreeRemoved = false
		_ = os.MkdirAll(r.worktreePath, 0o755)
		if r.failCreate {
			return "", fmt.Errorf("git worktree add -b failed: exit status 1: hook failed")
		}
		return "", nil
	}
	if len(args) >= 3 && args[0] == "worktree" && args[1] == "remove" {
		r.worktreeRemoved = true
		_ = os.RemoveAll(r.worktreePath)
		if r.onWorktreeRemove != nil {
			r.onWorktreeRemove()
		}
		return "", nil
	}
	if len(args) >= 3 && args[0] == "branch" && args[1] == "-D" {
		r.branchDeleted = true
		return "", nil
	}
	return "", nil
}

type ancestorBaseWorktreeRunner struct {
	repoDir           string
	parentWorktree    string
	parentBranch      string
	childWorktreePath string
	createBaseBranch  string
}

func (r *ancestorBaseWorktreeRunner) Run(_ context.Context, args ...string) (string, error) {
	if len(args) >= 2 && args[0] == "config" && args[1] == "user.name" {
		return "testuser\n", nil
	}
	if len(args) >= 3 && args[0] == "worktree" && args[1] == "list" && args[2] == "--porcelain" {
		return "worktree " + r.repoDir + "\nbranch refs/heads/main\n\n" +
			"worktree " + r.parentWorktree + "\nbranch refs/heads/" + r.parentBranch + "\n\n", nil
	}
	if len(args) >= 6 && args[0] == "worktree" && args[1] == "add" && args[2] == "-b" {
		r.childWorktreePath = args[4]
		r.createBaseBranch = args[5]
		return "", nil
	}
	return "", nil
}

type initFailureCleanupWorktreeRunner struct {
	repoDir        string
	worktreePath   string
	branchName     string
	worktreeExists bool
	removeForced   bool
	deletedFiles   []string
	porcelain      string
	statusCalls    int
}

func (r *initFailureCleanupWorktreeRunner) Run(_ context.Context, args ...string) (string, error) {
	if len(args) >= 2 && args[0] == "config" && args[1] == "user.name" {
		return "testuser\n", nil
	}
	if len(args) >= 3 && args[0] == "worktree" && args[1] == "list" && args[2] == "--porcelain" {
		var b strings.Builder
		b.WriteString("worktree " + r.repoDir + "\nbranch refs/heads/main\n\n")
		if r.worktreeExists {
			b.WriteString("worktree " + r.worktreePath + "\nbranch refs/heads/" + r.branchName + "\n\n")
		}
		return b.String(), nil
	}
	if len(args) >= 6 && args[0] == "worktree" && args[1] == "add" && args[2] == "-b" {
		r.worktreeExists = true
		return "", nil
	}
	if len(args) >= 4 && args[0] == "-C" && args[1] == r.worktreePath && args[2] == "ls-files" && args[3] == "-d" {
		r.statusCalls++
		return strings.Join(r.deletedFiles, "\n"), nil
	}
	if len(args) >= 4 && args[0] == "-C" && args[1] == r.worktreePath && args[2] == "status" && args[3] == "--porcelain" {
		r.statusCalls++
		return r.porcelain, nil
	}
	if len(args) >= 3 && args[0] == "worktree" && args[1] == "remove" {
		for _, arg := range args {
			if arg == "--force" {
				r.removeForced = true
				break
			}
		}
		r.worktreeExists = false
		return "", nil
	}
	if len(args) >= 3 && args[0] == "branch" && args[1] == "-D" {
		return "", nil
	}
	return "", nil
}

func TestSessionProjectionIssueIDPrefersSessionNameParsing(t *testing.T) {
	session := daemonstate.Session{
		ID:      "az-bra",
		IssueID: "az-bra",
	}
	if got, want := sessionProjectionIssueID(session, "azedarach"), "bra"; got != want {
		t.Fatalf("sessionProjectionIssueID() = %q, want %q", got, want)
	}
}

func TestSessionProjectionByIssueKeyPrefersMostRecentState(t *testing.T) {
	now := time.Now().UTC()
	t.Run("newer attached wins over older stopped", func(t *testing.T) {
		sessions := []daemonstate.Session{
			{
				ID:        "az-bra",
				IssueID:   "az-bra",
				State:     daemonstate.SessionStateStopped,
				UpdatedAt: now.Add(-1 * time.Minute),
			},
			{
				ID:        "plain",
				IssueID:   "bra",
				State:     daemonstate.SessionStateAttached,
				UpdatedAt: now,
			},
		}

		byIssue := sessionProjectionLatestByIssueKey(sessions, "azedarach")
		entry, ok := byIssue["bra"]
		if !ok {
			t.Fatalf("missing projection for issue key bra: %+v", byIssue)
		}
		if entry.State != daemonstate.SessionStateAttached {
			t.Fatalf("projection state = %s, want %s", entry.State, daemonstate.SessionStateAttached)
		}
		if entry.ID != "plain" {
			t.Fatalf("projection session id = %s, want plain", entry.ID)
		}
	})

	t.Run("newer stopped wins over older attached", func(t *testing.T) {
		sessions := []daemonstate.Session{
			{
				ID:        "plain",
				IssueID:   "bra",
				State:     daemonstate.SessionStateAttached,
				UpdatedAt: now.Add(-1 * time.Minute),
			},
			{
				ID:        "az-bra",
				IssueID:   "az-bra",
				State:     daemonstate.SessionStateStopped,
				UpdatedAt: now,
			},
		}

		byIssue := sessionProjectionLatestByIssueKey(sessions, "azedarach")
		entry, ok := byIssue["bra"]
		if !ok {
			t.Fatalf("missing projection for issue key bra: %+v", byIssue)
		}
		if entry.State != daemonstate.SessionStateStopped {
			t.Fatalf("projection state = %s, want %s", entry.State, daemonstate.SessionStateStopped)
		}
		if entry.ID != "az-bra" {
			t.Fatalf("projection session id = %s, want az-bra", entry.ID)
		}
	})
}

func TestLifecycleSessionProjectionByIssueKeyIgnoresAgentScopedRows(t *testing.T) {
	now := time.Now().UTC()
	sessions := []daemonstate.Session{
		{
			ID:        "az-bra",
			IssueID:   "bra",
			State:     daemonstate.SessionStateAttached,
			UpdatedAt: now,
		},
		{
			ID:        "az-bra.pane-190",
			IssueID:   "bra",
			State:     daemonstate.SessionStatePaused,
			UpdatedAt: now.Add(time.Minute),
		},
	}

	for name, byIssue := range map[string]map[string]daemonstate.Session{
		"reconcile":      sessionProjectionForReconcileByIssueKey(sessions, "azedarach"),
		"tmux hydration": sessionProjectionForTmuxHydrationByIssueKey(sessions, "azedarach"),
	} {
		entry, ok := byIssue["bra"]
		if !ok {
			t.Fatalf("%s projection missing issue key bra: %+v", name, byIssue)
		}
		if entry.ID != "az-bra" || entry.State != daemonstate.SessionStateAttached {
			t.Fatalf("%s projection = %+v, want parent attached row", name, entry)
		}
	}
}

func TestAgentScopedProjectionCannotRecreateTmuxSession(t *testing.T) {
	if sessionProjectionCanRecreateTmuxSession(daemonstate.Session{
		ID:            "az-bra.pane-190",
		IssueID:       "bra",
		State:         daemonstate.SessionStateAttached,
		ObservedState: daemonstate.SessionStateAttached,
	}) {
		t.Fatal("agent-scoped projection authorized tmux recovery")
	}
	if !sessionProjectionCanRecreateTmuxSession(daemonstate.Session{
		ID:            "az-bra",
		IssueID:       "bra",
		State:         daemonstate.SessionStateAttached,
		ObservedState: daemonstate.SessionStateStopped,
	}) {
		t.Fatal("parent desired-active projection did not authorize tmux recovery")
	}
	if sessionProjectionCanRecreateTmuxSession(daemonstate.Session{
		ID:      "az-bra",
		IssueID: "bra",
		State:   daemonstate.SessionStateStopped,
	}) {
		t.Fatal("stopped parent projection authorized tmux recovery")
	}
	if sessionProjectionCanRecreateTmuxSession(daemonstate.Session{
		ID:      "az-bra",
		IssueID: "bra",
		State:   daemonstate.SessionStateStopping,
	}) {
		t.Fatal("stopping parent projection authorized tmux recovery")
	}
	if sessionProjectionCanRecreateTmuxSession(daemonstate.Session{
		ID:      "az-bra",
		IssueID: "bra",
	}) {
		t.Fatal("empty-state parent projection authorized tmux recovery")
	}
}

func TestSourceForSessionInvariant(t *testing.T) {
	d := &Daemon{}
	if got := d.sourceForSessionInvariant(sessionInvariantSessionStartConflict); got != daemonInvariantSourceTmux {
		t.Fatalf("start conflict source = %s, want %s", got, daemonInvariantSourceTmux)
	}
	if got := d.sourceForSessionInvariant(sessionInvariantSessionAttachTarget); got != daemonInvariantSourceTmux {
		t.Fatalf("attach target source = %s, want %s", got, daemonInvariantSourceTmux)
	}
	if got := d.sourceForSessionInvariant(sessionInvariantSessionLifecycleTarget); got != daemonInvariantSourceTmux {
		t.Fatalf("lifecycle target source = %s, want %s", got, daemonInvariantSourceTmux)
	}
	if got := d.sourceForSessionInvariant(sessionInvariantSessionStopTargets); got != daemonInvariantSourceTmux {
		t.Fatalf("stop targets source = %s, want %s", got, daemonInvariantSourceTmux)
	}
	if got := d.sourceForSessionInvariant(sessionInvariantSessionReconcile); got != daemonInvariantSourceHybrid {
		t.Fatalf("reconcile source = %s, want %s", got, daemonInvariantSourceHybrid)
	}
}

func TestReconcileSessionValidationIssueIDsDefersOnlyQuiescentStopped(t *testing.T) {
	namingScope := "/repo"
	tmuxSet := map[string]struct{}{
		"cpa": {},
		"cpb": {},
	}
	snapshotSessions := []daemonstate.Session{
		{ID: naming.CanonicalSessionID(namingScope, "cpc"), IssueID: "cpc", State: daemonstate.SessionStateRunning},
		{ID: naming.CanonicalSessionID(namingScope, "cpc") + ".stopped-pane", IssueID: "cpc", State: daemonstate.SessionStateStopped, ObservedState: daemonstate.SessionStateStopped},
		{ID: naming.CanonicalSessionID(namingScope, "cpa"), IssueID: "cpa", State: daemonstate.SessionStateStopped, ObservedState: daemonstate.SessionStateStopped},
		{ID: naming.CanonicalSessionID(namingScope, "stopping"), IssueID: "stopping", State: daemonstate.SessionStateStopping, ObservedState: daemonstate.SessionStateRunning},
		{ID: naming.CanonicalSessionID(namingScope, "observed-live"), IssueID: "observed-live", State: daemonstate.SessionStateStopped, ObservedState: daemonstate.SessionStateRunning},
		{ID: naming.CanonicalSessionID(namingScope, "invalid-state"), IssueID: "invalid-state", State: daemonstate.SessionState("invalid"), ObservedState: daemonstate.SessionStateStopped},
		{ID: naming.CanonicalSessionID(namingScope, "agent-scoped") + ".pane-1", IssueID: "agent-scoped", State: daemonstate.SessionStateRunning, ObservedState: daemonstate.SessionStateRunning},
		{ID: "###", IssueID: ""},
	}
	for i := 0; i < 1300; i++ {
		issueID := fmt.Sprintf("stopped-%04d", i)
		snapshotSessions = append(snapshotSessions, daemonstate.Session{
			ID:            naming.CanonicalSessionID(namingScope, issueID),
			IssueID:       issueID,
			State:         daemonstate.SessionStateStopped,
			ObservedState: daemonstate.SessionStateStopped,
		})
	}

	ids, deferred := reconcileSessionValidationIssueIDs(nil, tmuxSet, snapshotSessions, namingScope)
	got := map[string]struct{}{}
	for _, id := range ids {
		got[id] = struct{}{}
	}
	for _, want := range []string{"cpa", "cpb", "cpc", "stopping", "observed-live", "invalid-state", "agent-scoped"} {
		if _, ok := got[want]; !ok {
			t.Fatalf("validation ids = %v, missing %s", ids, want)
		}
	}
	if len(got) != 7 {
		t.Fatalf("validation ids = %v, want only observed candidates", ids)
	}
	if deferred != 1300 {
		t.Fatalf("deferred validation issue count = %d, want 1300 quiescent stopped issues", deferred)
	}
}

func TestReconcileSessionValidationIssueIDsHonorsTargetIssue(t *testing.T) {
	namingScope := "/repo"
	tmuxSet := map[string]struct{}{
		"cpa": {},
		"cpb": {},
	}
	snapshotSessions := []daemonstate.Session{
		{ID: naming.CanonicalSessionID(namingScope, "cpa"), IssueID: "cpa"},
		{ID: naming.CanonicalSessionID(namingScope, "cpb"), IssueID: "cpb"},
	}

	ids, deferred := reconcileSessionValidationIssueIDs(map[string]struct{}{"cpa": {}}, tmuxSet, snapshotSessions, namingScope)
	if len(ids) != 1 || ids[0] != "cpa" {
		t.Fatalf("validation ids = %v, want [cpa]", ids)
	}
	if deferred != 0 {
		t.Fatalf("deferred validation issue count = %d, want 0 outside explicit target scope", deferred)
	}
}

func TestReconcileSessionValidationIssueIDsHonorsMultipleTargetIssues(t *testing.T) {
	namingScope := "/repo"
	tmuxSet := map[string]struct{}{
		"cpa": {},
		"cpb": {},
		"cpc": {},
	}
	snapshotSessions := []daemonstate.Session{
		{ID: naming.CanonicalSessionID(namingScope, "cpa"), IssueID: "cpa"},
		{ID: naming.CanonicalSessionID(namingScope, "cpb"), IssueID: "cpb"},
		{ID: naming.CanonicalSessionID(namingScope, "cpc"), IssueID: "cpc"},
	}

	ids, deferred := reconcileSessionValidationIssueIDs(map[string]struct{}{"cpa": {}, "cpc": {}}, tmuxSet, snapshotSessions, namingScope)
	got := map[string]struct{}{}
	for _, id := range ids {
		got[id] = struct{}{}
	}
	for _, want := range []string{"cpa", "cpc"} {
		if _, ok := got[want]; !ok {
			t.Fatalf("validation ids = %v, missing %s", ids, want)
		}
	}
	if _, ok := got["cpb"]; ok || len(got) != 2 {
		t.Fatalf("validation ids = %v, want only cpa/cpc", ids)
	}
	if deferred != 0 {
		t.Fatalf("deferred validation issue count = %d, want 0 outside explicit target scope", deferred)
	}
}

func TestRuntimeReconcileIssuesReusesSharedTmuxSessionSnapshotForTargets(t *testing.T) {
	ctx := context.Background()
	repoDir := t.TempDir()
	projectID, err := appconfig.ProjectIDForRoot(repoDir)
	if err != nil {
		t.Fatalf("ProjectIDForRoot: %v", err)
	}
	runtimeStateStore := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(t.TempDir(), "runtime.db"), slog.Default())
	t.Cleanup(func() { _ = runtimeStateStore.Close() })

	issueIDs := []string{"cpa", "cpb", "cpc", "cpd"}
	tmuxRunner := &testTmuxRunner{
		sessions:    map[string]bool{},
		killEntered: make(chan struct{}),
		killRelease: make(chan struct{}),
	}
	for _, issueID := range issueIDs {
		sessionID := naming.CanonicalSessionID(repoDir, issueID)
		tmuxRunner.sessions[sessionID] = true
		if err := upsertSessionStateFixture(runtimeStateStore, ctx, projectID, daemonstate.Session{
			ID:        sessionID,
			IssueID:   issueID,
			State:     daemonstate.SessionStateAttached,
			UpdatedAt: time.Now().UTC(),
		}); err != nil {
			t.Fatalf("seed runtime session %s: %v", issueID, err)
		}
	}

	store := daemonstate.NewStore()
	daemon := &Daemon{
		cfg: Config{
			RepoDir: repoDir,
			CLITool: "claude",
			Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
		},
		tmux:         tmux.NewClient(tmuxRunner, slog.Default()),
		session:      daemonhandlers.NewSessionHandler(store),
		sessionStore: store,
		worktreeManagersByRoot: map[string]*git.WorktreeManager{
			repoDir: git.NewWorktreeManager(&testGitRunner{worktreePath: filepath.Join(filepath.Dir(repoDir), filepath.Base(repoDir)+"-cpa"), branchName: "riordan/cpa/test"}, repoDir, slog.Default()),
		},
		worktreeManagersByProject: map[string]*git.WorktreeManager{
			projectID: git.NewWorktreeManager(&testGitRunner{worktreePath: filepath.Join(filepath.Dir(repoDir), filepath.Base(repoDir)+"-cpa"), branchName: "riordan/cpa/test"}, repoDir, slog.Default()),
		},
		runtimeStoresByRoot: map[string]*daemonstate.RuntimeStateStore{
			repoDir: runtimeStateStore,
		},
	}
	t.Cleanup(func() { daemon.closeIssueClients() })

	if _, err := daemon.ensureRuntimeReconciler().ReconcileIssues(ctx, projectID, issueIDs); err != nil {
		t.Fatalf("ReconcileIssues: %v", err)
	}
	if got := tmuxRunner.listSessionCallCount(); got != 1 {
		t.Fatalf("tmux list-sessions calls = %d, want one shared target snapshot", got)
	}
}

func TestRuntimeReconcileIssuesSummarizesSharedTmuxSnapshotFailure(t *testing.T) {
	ctx := context.Background()
	repoDir := t.TempDir()
	projectID, err := appconfig.ProjectIDForRoot(repoDir)
	if err != nil {
		t.Fatalf("ProjectIDForRoot: %v", err)
	}
	runtimeStateStore := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(t.TempDir(), "runtime.db"), slog.Default())
	t.Cleanup(func() { _ = runtimeStateStore.Close() })

	issueIDs := []string{"cpa", "cpb", "cpc", "cpd"}
	for _, issueID := range issueIDs {
		if err := upsertSessionStateFixture(runtimeStateStore, ctx, projectID, daemonstate.Session{
			ID:        naming.CanonicalSessionID(repoDir, issueID),
			IssueID:   issueID,
			State:     daemonstate.SessionStateAttached,
			UpdatedAt: time.Now().UTC(),
		}); err != nil {
			t.Fatalf("seed runtime session %s: %v", issueID, err)
		}
	}

	tmuxRunner := &failingListSessionsTmuxRunner{}
	store := daemonstate.NewStore()
	daemon := &Daemon{
		cfg: Config{
			RepoDir: repoDir,
			CLITool: "claude",
			Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
		},
		tmux:         tmux.NewClient(tmuxRunner, slog.Default()),
		session:      daemonhandlers.NewSessionHandler(store),
		sessionStore: store,
		worktreeManagersByRoot: map[string]*git.WorktreeManager{
			repoDir: git.NewWorktreeManager(&testGitRunner{worktreePath: filepath.Join(filepath.Dir(repoDir), filepath.Base(repoDir)+"-cpa"), branchName: "riordan/cpa/test"}, repoDir, slog.Default()),
		},
		worktreeManagersByProject: map[string]*git.WorktreeManager{
			projectID: git.NewWorktreeManager(&testGitRunner{worktreePath: filepath.Join(filepath.Dir(repoDir), filepath.Base(repoDir)+"-cpa"), branchName: "riordan/cpa/test"}, repoDir, slog.Default()),
		},
		runtimeStoresByRoot: map[string]*daemonstate.RuntimeStateStore{
			repoDir: runtimeStateStore,
		},
	}
	t.Cleanup(func() { daemon.closeIssueClients() })

	_, err = daemon.ensureRuntimeReconciler().ReconcileIssues(ctx, projectID, issueIDs)
	if err == nil {
		t.Fatal("ReconcileIssues error = nil, want tmux snapshot failure")
	}
	errText := err.Error()
	if !strings.Contains(errText, "reconcile issue sessions (4 targets)") {
		t.Fatalf("error = %q, want target-count summary", errText)
	}
	if strings.Contains(errText, "reconcile issue session cpa") || strings.Contains(errText, "reconcile issue session cpb") {
		t.Fatalf("error = %q, want no repeated per-issue reconcile fragments", errText)
	}
	if got := tmuxRunner.listSessionsCalls; got != 1 {
		t.Fatalf("tmux list-sessions calls = %d, want one failed shared snapshot", got)
	}
}

func TestSessionRestartAllRefusesSessionsWithoutManagedIdentity(t *testing.T) {
	ctx := context.Background()
	repoDir := t.TempDir()
	managedDir := filepath.Join(t.TempDir(), ".azedarach-generations", "generation.current")
	t.Setenv("PATH", filepath.Join(repoDir, "bin")+string(os.PathListSeparator)+"/usr/bin:/bin")
	projectID, err := appconfig.ProjectIDForRoot(repoDir)
	if err != nil {
		t.Fatalf("ProjectIDForRoot: %v", err)
	}
	issuesClient := newMigratedIssueClient(t, repoDir, slog.Default())
	runtimeStateStore := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(t.TempDir(), "runtime.db"), slog.Default())
	t.Cleanup(func() { _ = runtimeStateStore.Close() })

	idleIssue := "cph"
	busyIssue := "cpi"
	idleSession := naming.CanonicalSessionID(repoDir, idleIssue)
	busySession := naming.CanonicalSessionID(repoDir, busyIssue)
	for _, row := range []daemonstate.Session{
		{ID: idleSession, IssueID: idleIssue, State: daemonstate.SessionStateRunning, ObservedState: daemonstate.SessionStateRunning, Activity: "idle", ActivitySource: "hooks", UpdatedAt: time.Now().UTC()},
		{ID: busySession, IssueID: busyIssue, State: daemonstate.SessionStateRunning, ObservedState: daemonstate.SessionStateRunning, Activity: "busy", ActivitySource: "hooks", UpdatedAt: time.Now().UTC()},
	} {
		if err := upsertSessionStateFixture(runtimeStateStore, ctx, projectID, row); err != nil {
			t.Fatalf("seed session state: %v", err)
		}
	}

	tmuxRunner := newSessionStartTmuxRunner()
	tmuxRunner.sessions[idleSession] = true
	tmuxRunner.sessions[busySession] = true
	store := daemonstate.NewStore()
	daemon := &Daemon{
		cfg:          Config{RepoDir: repoDir, CLITool: "codex", SessionShell: "zsh", ManagedGenerationBinDir: managedDir, Logger: slog.Default()},
		tmux:         tmux.NewClient(tmuxRunner, slog.Default()),
		issues:       issuesClient,
		session:      daemonhandlers.NewSessionHandler(store),
		sessionStore: store,
		runtimeStoresByProject: map[string]*daemonstate.RuntimeStateStore{
			projectID: runtimeStateStore,
		},
		issueClientsByProject: map[string]*issues.Client{
			projectID: issuesClient,
		},
	}

	resp, err := daemon.handleSessionRestartAll(ctx, protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-restart-all",
		Kind:            protocol.EnvelopeKindCommand,
		Meta:            protocol.Metadata{ProjectID: naming.ProjectID(projectID)},
		Body:            marshalJSON(protocol.SessionRestartAllRequestBody{ProjectID: naming.ProjectID(projectID)}),
	})
	if err != nil {
		t.Fatalf("handleSessionRestartAll error: %v", err)
	}
	if !resp.OK {
		t.Fatalf("restart-all response error: %+v", resp.Error)
	}
	var result protocol.SessionRestartAllResponseBody
	if err := json.Unmarshal(resp.Body, &result); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if result.Restarted != 0 || result.Skipped != 2 || result.Failed != 0 || result.Sessions[0].Outcome != "no_agent" {
		t.Fatalf("result = %+v, want typed no-agent refusals", result)
	}
	if tmuxRunner.sendKeysCalls != 0 {
		t.Fatalf("send-keys calls = %d, want exact restart to fail closed before terminal input", tmuxRunner.sendKeysCalls)
	}
}

func TestSessionRestartAllCompletionCheckpointFailureReturnsProtocolError(t *testing.T) {
	ctx := context.Background()
	repoDir := t.TempDir()
	projectID, err := appconfig.ProjectIDForRoot(repoDir)
	if err != nil {
		t.Fatal(err)
	}
	sessionID := naming.CanonicalSessionID(repoDir, "cph")
	runner := newSessionStartTmuxRunner()
	runner.sessions[sessionID] = true
	runtimeStore := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(t.TempDir(), "runtime.db"), slog.Default())
	t.Cleanup(func() { _ = runtimeStore.Close() })
	if err := upsertSessionStateFixture(runtimeStore, ctx, projectID, daemonstate.Session{
		ID: sessionID, IssueID: "cph", State: daemonstate.SessionStateRunning,
	}); err != nil {
		t.Fatal(err)
	}
	d := &Daemon{
		cfg:                    Config{RepoDir: repoDir, CLITool: "codex", SessionShell: "zsh", Logger: slog.Default()},
		tmux:                   tmux.NewClient(runner, slog.Default()),
		runtimeStoresByProject: map[string]*daemonstate.RuntimeStateStore{projectID: runtimeStore},
	}
	writes := 0
	ctx = daemonops.WithProgressReporter(ctx, func(context.Context, daemonops.Progress) error {
		writes++
		if writes == 3 {
			return errors.New("completion checkpoint unavailable")
		}
		return nil
	})
	resp, err := d.handleSessionRestartAllDirect(ctx, protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion, RequestID: "req-checkpoint-failure", Kind: protocol.EnvelopeKindCommand,
		Meta: protocol.Metadata{ProjectID: naming.ProjectID(projectID)},
		Body: marshalJSON(protocol.SessionRestartAllRequestBody{ProjectID: naming.ProjectID(projectID)}),
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.OK || resp.Error == nil || !strings.Contains(resp.Error.Message, "persist restart target completion") || !strings.Contains(resp.Error.Message, "checkpoint unavailable") {
		t.Fatalf("response=%+v writes=%d", resp, writes)
	}
}

func TestSessionRestartAllForceBusyStillRequiresManagedIdentity(t *testing.T) {
	ctx := context.Background()
	repoDir := t.TempDir()
	projectID, err := appconfig.ProjectIDForRoot(repoDir)
	if err != nil {
		t.Fatalf("ProjectIDForRoot: %v", err)
	}
	issuesClient := newMigratedIssueClient(t, repoDir, slog.Default())
	runtimeStateStore := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(t.TempDir(), "runtime.db"), slog.Default())
	t.Cleanup(func() { _ = runtimeStateStore.Close() })

	issueIDs := []string{"cpj", "cpk"}
	tmuxRunner := newSessionStartTmuxRunner()
	for _, issueID := range issueIDs {
		sessionID := naming.CanonicalSessionID(repoDir, issueID)
		tmuxRunner.sessions[sessionID] = true
		if err := upsertSessionStateFixture(runtimeStateStore, ctx, projectID, daemonstate.Session{
			ID:             sessionID,
			IssueID:        issueID,
			State:          daemonstate.SessionStateRunning,
			ObservedState:  daemonstate.SessionStateRunning,
			Activity:       "busy",
			ActivitySource: "hooks",
			UpdatedAt:      time.Now().UTC(),
		}); err != nil {
			t.Fatalf("seed session state: %v", err)
		}
	}
	store := daemonstate.NewStore()
	daemon := &Daemon{
		cfg: Config{
			RepoDir:                    repoDir,
			CLITool:                    "codex",
			DangerouslySkipPermissions: true,
			SessionShell:               "zsh",
			Logger:                     slog.Default(),
		},
		tmux:         tmux.NewClient(tmuxRunner, slog.Default()),
		issues:       issuesClient,
		session:      daemonhandlers.NewSessionHandler(store),
		sessionStore: store,
		runtimeStoresByProject: map[string]*daemonstate.RuntimeStateStore{
			projectID: runtimeStateStore,
		},
		issueClientsByProject: map[string]*issues.Client{
			projectID: issuesClient,
		},
	}

	resp, err := daemon.handleSessionRestartAll(ctx, protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-restart-all-force",
		Kind:            protocol.EnvelopeKindCommand,
		Meta:            protocol.Metadata{ProjectID: naming.ProjectID(projectID)},
		Body: marshalJSON(protocol.SessionRestartAllRequestBody{
			ProjectID: naming.ProjectID(projectID),
			ForceBusy: true,
		}),
	})
	if err != nil {
		t.Fatalf("handleSessionRestartAll error: %v", err)
	}
	if !resp.OK {
		t.Fatalf("restart-all response error: %+v", resp.Error)
	}
	var result protocol.SessionRestartAllResponseBody
	if err := json.Unmarshal(resp.Body, &result); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if result.Restarted != 0 || result.Skipped != 2 || result.Failed != 0 || result.Sessions[0].Outcome != "no_agent" {
		t.Fatalf("result = %+v, want typed no-agent refusals", result)
	}
	if tmuxRunner.sendKeysCalls != 0 {
		t.Fatalf("send-keys calls = %d, want no legacy terminal input", tmuxRunner.sendKeysCalls)
	}
}

func TestSessionRestartAllUsesRefreshedHookActivityForBusyGate(t *testing.T) {
	ctx := context.Background()
	repoDir := t.TempDir()
	projectID, err := appconfig.ProjectIDForRoot(repoDir)
	if err != nil {
		t.Fatal(err)
	}
	store := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(t.TempDir(), "runtime.db"), slog.Default())
	t.Cleanup(func() { _ = store.Close() })
	issueID := "busy-hook"
	sessionID := naming.CanonicalSessionID(repoDir, issueID)
	base := time.Now().UTC()
	if err := upsertSessionStateFixture(store, ctx, projectID, daemonstate.Session{
		ID: sessionID, IssueID: issueID, State: daemonstate.SessionStateRunning, ObservedState: daemonstate.SessionStateRunning,
		Activity: "idle", ActivitySource: "session", UpdatedAt: base,
	}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.ApplyPhysicalSessionObservation(ctx, daemonstate.PhysicalSessionObservation{
		ProjectID: projectID, SessionID: sessionID, ObservedState: daemonstate.SessionStateRunning,
		Activity: "busy", ActivitySource: "hooks", UpdatedAt: base.Add(time.Second), ObservedVersion: base.Add(time.Second).UnixNano(),
	}); err != nil {
		t.Fatal(err)
	}
	runner := newSessionStartTmuxRunner()
	runner.sessions[sessionID] = true
	d := &Daemon{
		cfg:                    Config{RepoDir: repoDir, CLITool: "codex", SessionShell: "zsh", Logger: slog.Default()},
		tmux:                   tmux.NewClient(runner, slog.Default()),
		runtimeStoresByProject: map[string]*daemonstate.RuntimeStateStore{projectID: store},
	}
	runner.onRespawnPane = seedManagedRestartIdentity(t, d, runner, projectID, sessionID)
	resp, err := d.handleSessionRestartAll(ctx, protocol.RequestEnvelope{
		Command: protocol.CommandSessionRestartAll,
		Meta:    protocol.Metadata{ProjectID: naming.ProjectID(projectID)},
		Body:    marshalJSON(protocol.SessionRestartAllRequestBody{ProjectID: naming.ProjectID(projectID)}),
	})
	if err != nil || resp.Error != nil {
		t.Fatalf("restart-all response=%+v err=%v", resp.Error, err)
	}
	var result protocol.SessionRestartAllResponseBody
	if err := json.Unmarshal(resp.Body, &result); err != nil {
		t.Fatal(err)
	}
	if result.Restarted != 0 || result.Skipped != 1 || len(result.Sessions) != 1 || result.Sessions[0].Reason != "busy_requires_force" || result.Sessions[0].Activity != "busy" || result.Sessions[0].ActivitySource != "hooks" {
		t.Fatalf("hook-busy restart result = %+v", result)
	}
	for _, command := range runner.commands {
		if len(command) > 0 && command[0] == "respawn-pane" {
			t.Fatalf("hook-busy session bypassed force gate: %v", command)
		}
	}
}

func TestSessionRestartAllDiscoversKnownProjectSessionsAndReportsNoAgent(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	repoA := filepath.Join(root, "qaalpha")
	repoB := filepath.Join(root, "qabeta")
	if err := os.MkdirAll(repoA, 0o755); err != nil {
		t.Fatalf("mkdir repoA: %v", err)
	}
	if err := os.MkdirAll(repoB, 0o755); err != nil {
		t.Fatalf("mkdir repoB: %v", err)
	}
	projectA, err := appconfig.ProjectIDForRoot(repoA)
	if err != nil {
		t.Fatalf("ProjectIDForRoot(repoA): %v", err)
	}
	projectB, err := appconfig.ProjectIDForRoot(repoB)
	if err != nil {
		t.Fatalf("ProjectIDForRoot(repoB): %v", err)
	}
	storeA := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(t.TempDir(), "runtime-a.db"), slog.Default())
	storeB := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(t.TempDir(), "runtime-b.db"), slog.Default())
	t.Cleanup(func() {
		_ = storeA.Close()
		_ = storeB.Close()
	})

	sessionA := naming.CanonicalSessionID(repoA, "cpl")
	sessionB := naming.CanonicalSessionID(repoB, "cpm")
	for _, seed := range []struct {
		store     *daemonstate.RuntimeStateStore
		projectID string
		sessionID string
		issueID   string
	}{
		{store: storeA, projectID: projectA, sessionID: sessionA, issueID: "cpl"},
		{store: storeB, projectID: projectB, sessionID: sessionB, issueID: "cpm"},
	} {
		if err := upsertSessionStateFixture(seed.store, ctx, seed.projectID, daemonstate.Session{
			ID:             seed.sessionID,
			IssueID:        seed.issueID,
			State:          daemonstate.SessionStateRunning,
			ObservedState:  daemonstate.SessionStateRunning,
			Activity:       "busy",
			ActivitySource: "hooks",
			UpdatedAt:      time.Now().UTC(),
		}); err != nil {
			t.Fatalf("seed session state: %v", err)
		}
	}

	tmuxRunner := newSessionStartTmuxRunner()
	tmuxRunner.sessions[sessionA] = true
	tmuxRunner.sessions[sessionB] = true
	tmuxRunner.sendKeysErr = errors.New("send-keys failed")
	tmuxRunner.sendKeysErrOnCall = 4
	stateStore := daemonstate.NewStore()
	daemon := &Daemon{
		cfg:          Config{RepoDir: repoA, CLITool: "codex", SessionShell: "zsh", Logger: slog.Default()},
		tmux:         tmux.NewClient(tmuxRunner, slog.Default()),
		issues:       newMigratedIssueClient(t, repoA, slog.Default()),
		session:      daemonhandlers.NewSessionHandler(stateStore),
		sessionStore: stateStore,
		runtimeStoresByProject: map[string]*daemonstate.RuntimeStateStore{
			projectA: storeA,
			projectB: storeB,
		},
		issueClientsByProject: map[string]*issues.Client{
			projectA: newMigratedIssueClient(t, repoA, slog.Default()),
			projectB: newMigratedIssueClient(t, repoB, slog.Default()),
		},
	}

	resp, err := daemon.handleSessionRestartAll(ctx, protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-restart-all-known-projects",
		Kind:            protocol.EnvelopeKindCommand,
		Meta:            protocol.Metadata{ProjectID: naming.ProjectID(projectA)},
		Body: marshalJSON(protocol.SessionRestartAllRequestBody{
			ProjectID: naming.ProjectID(projectA),
			ForceBusy: true,
		}),
	})
	if err != nil {
		t.Fatalf("handleSessionRestartAll error: %v", err)
	}
	if !resp.OK {
		t.Fatalf("restart-all response error: %+v", resp.Error)
	}
	var result protocol.SessionRestartAllResponseBody
	if err := json.Unmarshal(resp.Body, &result); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if result.Restarted != 0 || result.Skipped != 2 || result.Failed != 0 {
		t.Fatalf("result = %+v, want two typed no-agent refusals", result)
	}
	seenProjects := map[string]bool{}
	for _, session := range result.Sessions {
		seenProjects[session.ProjectID.String()] = true
	}
	if !seenProjects[projectA] || !seenProjects[projectB] {
		t.Fatalf("session projects = %+v, want %s and %s", seenProjects, projectA, projectB)
	}
}

func TestSessionRestartAllSkipsTmuxSessionWithoutLivePane(t *testing.T) {
	ctx := context.Background()
	repoDir := t.TempDir()
	projectID, err := appconfig.ProjectIDForRoot(repoDir)
	if err != nil {
		t.Fatalf("ProjectIDForRoot: %v", err)
	}
	runtimeStateStore := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(t.TempDir(), "runtime.db"), slog.Default())
	t.Cleanup(func() { _ = runtimeStateStore.Close() })

	issueID := "cpn"
	sessionID := naming.CanonicalSessionID(repoDir, issueID)
	if err := upsertSessionStateFixture(runtimeStateStore, ctx, projectID, daemonstate.Session{
		ID:             sessionID,
		IssueID:        issueID,
		State:          daemonstate.SessionStateRunning,
		ObservedState:  daemonstate.SessionStateRunning,
		Activity:       "idle",
		ActivitySource: "hooks",
		UpdatedAt:      time.Now().UTC(),
	}); err != nil {
		t.Fatalf("seed session state: %v", err)
	}

	tmuxRunner := newSessionStartTmuxRunner()
	tmuxRunner.sessions[sessionID] = true
	tmuxRunner.sessionsWithoutPanes[sessionID] = true
	store := daemonstate.NewStore()
	daemon := &Daemon{
		cfg:          Config{RepoDir: repoDir, CLITool: "codex", SessionShell: "zsh", Logger: slog.Default()},
		tmux:         tmux.NewClient(tmuxRunner, slog.Default()),
		session:      daemonhandlers.NewSessionHandler(store),
		sessionStore: store,
		runtimeStoresByProject: map[string]*daemonstate.RuntimeStateStore{
			projectID: runtimeStateStore,
		},
	}

	resp, err := daemon.handleSessionRestartAll(ctx, protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-restart-all-no-pane",
		Kind:            protocol.EnvelopeKindCommand,
		Meta:            protocol.Metadata{ProjectID: naming.ProjectID(projectID)},
		Body: marshalJSON(protocol.SessionRestartAllRequestBody{
			ProjectID: naming.ProjectID(projectID),
			ForceBusy: true,
		}),
	})
	if err != nil {
		t.Fatalf("handleSessionRestartAll error: %v", err)
	}
	if !resp.OK {
		t.Fatalf("restart-all response error: %+v", resp.Error)
	}
	var result protocol.SessionRestartAllResponseBody
	if err := json.Unmarshal(resp.Body, &result); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if result.Restarted != 0 || result.Skipped != 1 || result.Failed != 0 {
		t.Fatalf("result = %+v, want restarted=0 skipped=1 failed=0", result)
	}
	if len(result.Sessions) != 1 || result.Sessions[0].Reason != "no_tmux_pane" || result.Sessions[0].TmuxReady {
		t.Fatalf("session result = %+v, want no_tmux_pane with tmux_ready=false", result.Sessions)
	}
	if tmuxRunner.sendKeysCalls != 0 {
		t.Fatalf("send-keys calls = %d, want no tmux dispatch for no-pane session", tmuxRunner.sendKeysCalls)
	}
}

func TestSessionRestartAllClassifiesShellOnlyWithoutTerminalInput(t *testing.T) {
	ctx := context.Background()
	repoDir := t.TempDir()
	projectID, err := appconfig.ProjectIDForRoot(repoDir)
	if err != nil {
		t.Fatalf("ProjectIDForRoot: %v", err)
	}
	runtimeStateStore := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(t.TempDir(), "runtime.db"), slog.Default())
	t.Cleanup(func() { _ = runtimeStateStore.Close() })

	issueID := "cpo"
	sessionID := naming.CanonicalSessionID(repoDir, issueID)
	if err := upsertSessionStateFixture(runtimeStateStore, ctx, projectID, daemonstate.Session{
		ID:             sessionID,
		IssueID:        issueID,
		State:          daemonstate.SessionStateRunning,
		ObservedState:  daemonstate.SessionStateRunning,
		Activity:       "no-agent",
		ActivitySource: "session",
		UpdatedAt:      time.Now().UTC(),
	}); err != nil {
		t.Fatalf("seed session state: %v", err)
	}

	tmuxRunner := newSessionStartTmuxRunner()
	tmuxRunner.sessions[sessionID] = true
	store := daemonstate.NewStore()
	daemon := &Daemon{
		cfg:          Config{RepoDir: repoDir, CLITool: "codex", SessionShell: "zsh", Logger: slog.Default()},
		tmux:         tmux.NewClient(tmuxRunner, slog.Default()),
		session:      daemonhandlers.NewSessionHandler(store),
		sessionStore: store,
		runtimeStoresByProject: map[string]*daemonstate.RuntimeStateStore{
			projectID: runtimeStateStore,
		},
	}

	resp, err := daemon.handleSessionRestartAll(ctx, protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-restart-all-no-agent",
		Kind:            protocol.EnvelopeKindCommand,
		Meta:            protocol.Metadata{ProjectID: naming.ProjectID(projectID)},
		Body: marshalJSON(protocol.SessionRestartAllRequestBody{
			ProjectID: naming.ProjectID(projectID),
			ForceBusy: true,
		}),
	})
	if err != nil {
		t.Fatalf("handleSessionRestartAll error: %v", err)
	}
	if !resp.OK {
		t.Fatalf("restart-all response error: %+v", resp.Error)
	}
	var result protocol.SessionRestartAllResponseBody
	if err := json.Unmarshal(resp.Body, &result); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if result.Restarted != 0 || result.Skipped != 1 || result.Failed != 0 || result.Sessions[0].Outcome != "shell_only" {
		t.Fatalf("result = %+v, want typed shell-only refusal", result)
	}
	if len(result.Sessions) != 1 || !result.Sessions[0].TmuxReady || result.Sessions[0].ActiveIntent {
		t.Fatalf("session result = %+v, want tmux_ready=true and active_intent=false", result.Sessions)
	}
	if result.Sessions[0].ActivitySource != "session" {
		t.Fatalf("activity source = %q, want session", result.Sessions[0].ActivitySource)
	}
	if tmuxRunner.sendKeysCalls != 0 {
		t.Fatalf("send-keys calls = %d, want no legacy terminal input", tmuxRunner.sendKeysCalls)
	}
	for _, payload := range tmuxRunner.inputPayloads {
		if strings.Contains(payload, "Continue your prior task") {
			t.Fatalf("unexpected continuation prompt paste for no-agent session: %+v", tmuxRunner.commands)
		}
	}
}

func TestSessionRestartAllClassifiesSessionSourcedBusyWithoutIdentity(t *testing.T) {
	ctx := context.Background()
	repoDir := t.TempDir()
	projectID, err := appconfig.ProjectIDForRoot(repoDir)
	if err != nil {
		t.Fatalf("ProjectIDForRoot: %v", err)
	}
	runtimeStateStore := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(t.TempDir(), "runtime.db"), slog.Default())
	t.Cleanup(func() { _ = runtimeStateStore.Close() })

	issueID := "cpp"
	sessionID := naming.CanonicalSessionID(repoDir, issueID)
	if err := upsertSessionStateFixture(runtimeStateStore, ctx, projectID, daemonstate.Session{
		ID:             sessionID,
		IssueID:        issueID,
		State:          daemonstate.SessionStateRunning,
		ObservedState:  daemonstate.SessionStateRunning,
		Activity:       "busy",
		ActivitySource: "session",
		UpdatedAt:      time.Now().UTC(),
	}); err != nil {
		t.Fatalf("seed session state: %v", err)
	}

	tmuxRunner := newSessionStartTmuxRunner()
	tmuxRunner.sessions[sessionID] = true
	store := daemonstate.NewStore()
	daemon := &Daemon{
		cfg:          Config{RepoDir: repoDir, CLITool: "codex", SessionShell: "zsh", Logger: slog.Default()},
		tmux:         tmux.NewClient(tmuxRunner, slog.Default()),
		session:      daemonhandlers.NewSessionHandler(store),
		sessionStore: store,
		runtimeStoresByProject: map[string]*daemonstate.RuntimeStateStore{
			projectID: runtimeStateStore,
		},
	}

	resp, err := daemon.handleSessionRestartAll(ctx, protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-restart-all-session-agent",
		Kind:            protocol.EnvelopeKindCommand,
		Meta:            protocol.Metadata{ProjectID: naming.ProjectID(projectID)},
		Body: marshalJSON(protocol.SessionRestartAllRequestBody{
			ProjectID: naming.ProjectID(projectID),
			ForceBusy: true,
		}),
	})
	if err != nil {
		t.Fatalf("handleSessionRestartAll error: %v", err)
	}
	if !resp.OK {
		t.Fatalf("restart-all response error: %+v", resp.Error)
	}
	var result protocol.SessionRestartAllResponseBody
	if err := json.Unmarshal(resp.Body, &result); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if result.Restarted != 0 || result.Skipped != 1 || result.Failed != 0 || result.Sessions[0].Outcome != "no_agent" {
		t.Fatalf("result = %+v, want typed no-agent refusal", result)
	}
	if len(result.Sessions) != 1 || !result.Sessions[0].TmuxReady || !result.Sessions[0].ActiveIntent || result.Sessions[0].ActivitySource != "session" {
		t.Fatalf("session result = %+v, want session-sourced active intent with tmux pane", result.Sessions)
	}
	if tmuxRunner.sendKeysCalls != 0 {
		t.Fatalf("send-keys calls = %d, want no legacy terminal input", tmuxRunner.sendKeysCalls)
	}
}

func TestSessionRestartActiveIntentOnlyForRunningWork(t *testing.T) {
	for _, tt := range []struct {
		activity string
		want     bool
	}{
		{activity: "busy", want: true},
		{activity: "starting", want: true},
		{activity: "working", want: true},
		{activity: "idle", want: false},
		{activity: "waiting", want: false},
		{activity: "paused", want: false},
		{activity: "no-agent", want: false},
		{activity: "unknown", want: false},
		{activity: "ended", want: false},
		{activity: "error", want: false},
	} {
		t.Run(tt.activity, func(t *testing.T) {
			if got := sessionRestartActiveIntent(tt.activity); got != tt.want {
				t.Fatalf("sessionRestartActiveIntent(%q) = %t, want %t", tt.activity, got, tt.want)
			}
		})
	}
}

type sessionStartTmuxRunner struct {
	sessions                    map[string]bool
	sessionsWithoutPanes        map[string]bool
	panes                       map[string][]string
	panePIDs                    map[string]int
	windows                     map[string]map[string]bool
	commands                    [][]string
	inputPayloads               []string
	handoffPromptContents       []string
	env                         map[string]map[string]string
	sendKeysCalls               int
	sendKeysTargets             []string
	sendKeysPayloads            []string
	newSessionErr               error
	createBeforeNewSessionError bool
	onNewSession                func(string)
	onNewSessionCommand         func(context.Context, string) error
	onNewWindow                 func(string, string)
	newWindowErr                error
	createBeforeNewWindowError  bool
	respawnWindowErr            error
	respawnBeforeWindowError    bool
	maxNewSessionCommand        int
	launchScriptPaths           map[string]string
	launchScriptContents        map[string]string
	launchScriptModes           map[string]os.FileMode
	launchPromptPaths           map[string]string
	launchPromptContents        map[string]string
	launchPromptModes           map[string]os.FileMode
	launchArtifactModes         map[string]os.FileMode
	sendKeysErr                 error
	sendKeysErrOnCall           int
	captureOutput               string
	currentCommand              string
	listPanesCalls              int
	onListPanes                 func(int)
	onSendKeys                  func(string, string)
	onRunWithInput              func(context.Context, string, []string) (string, error)
	onRespawnPane               func(context.Context, []string) error
	respawnErr                  error
}

func newSessionStartTmuxRunner() *sessionStartTmuxRunner {
	return &sessionStartTmuxRunner{
		sessions:             map[string]bool{},
		sessionsWithoutPanes: map[string]bool{},
		panes:                map[string][]string{},
		panePIDs:             map[string]int{},
		windows:              map[string]map[string]bool{},
		env:                  map[string]map[string]string{},
		launchScriptPaths:    map[string]string{},
		launchScriptContents: map[string]string{},
		launchScriptModes:    map[string]os.FileMode{},
		launchPromptPaths:    map[string]string{},
		launchPromptContents: map[string]string{},
		launchPromptModes:    map[string]os.FileMode{},
		launchArtifactModes:  map[string]os.FileMode{},
	}
}

func attachIsolatedRuntimeStore(t *testing.T, d *Daemon, projectID string) {
	t.Helper()
	store := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(t.TempDir(), "runtime.db"), slog.Default())
	t.Cleanup(func() { _ = store.Close() })
	d.runtimeStoresByRoot = map[string]*daemonstate.RuntimeStateStore{d.cfg.RepoDir: store}
	d.runtimeStoresByProject = map[string]*daemonstate.RuntimeStateStore{projectID: store}
}

func seedReadyAgentInput(t *testing.T, d *Daemon, runner *sessionStartTmuxRunner, projectID, sessionID string) {
	t.Helper()
	projectID = d.canonicalProjectID(projectID)
	runner.currentCommand = strings.TrimSpace(d.runtimeConfigForProject(projectID).CLITool)
	if runner.currentCommand == "" {
		runner.currentCommand = "claude"
	}
	store := d.sessionRuntimeStateStore(projectID)
	now := time.Now().UTC()
	if err := store.UpsertManagedAgentIdentity(context.Background(), daemonstate.ManagedAgentIdentity{ProjectID: projectID, SessionID: sessionID, LogicalPaneID: "agent", TmuxPaneID: "1", PanePID: 123, AgentIncarnation: "test-incarnation", ObservedAt: now}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.ApplyPhysicalSessionObservation(context.Background(), daemonstate.PhysicalSessionObservation{ProjectID: projectID, SessionID: sessionID, ObservedState: daemonstate.SessionStateRunning, Activity: "idle", ActivitySource: "hooks", UpdatedAt: now, ObservedVersion: now.UnixNano()}); err != nil {
		t.Fatal(err)
	}
	client := d.issueClientForProject(projectID)
	if client == nil {
		client = issues.NewClientAtPath(filepath.Join(t.TempDir(), "issues.db"), d.cfg.Logger)
		t.Cleanup(func() { _ = client.CloseDB() })
		d.issues = client
		if d.issueClientsByProject == nil {
			d.issueClientsByProject = map[string]*issues.Client{}
		}
		d.issueClientsByProject[projectID] = client
	}
	receiver := &recordingAuthoritativeReceiver{accepted: map[string]string{}, sink: func(payload string) {
		runner.inputPayloads = append(runner.inputPayloads, payload)
	}}
	d.agentInput = newAgentInputDeliveryService(d.sessionRuntimeStateStoreIfConfigured, d.issueClientForProject, receiver, "test-daemon")
}

func seedManagedRestartIdentity(t *testing.T, d *Daemon, runner *sessionStartTmuxRunner, projectID, sessionID string) func(context.Context, []string) error {
	t.Helper()
	projectID = d.canonicalProjectID(projectID)
	store := d.sessionRuntimeStateStore(projectID)
	runner.panePIDs[sessionID] = 123
	if err := store.UpsertManagedAgentIdentity(context.Background(), daemonstate.ManagedAgentIdentity{
		ProjectID: projectID, SessionID: sessionID, LogicalPaneID: "agent", TmuxPaneID: "1",
		PanePID: 123, AgentIncarnation: "pre-restart", ObservedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	return func(ctx context.Context, args []string) error {
		command := args[len(args)-1]
		for _, matches := range regexp.MustCompile(`'([^']+)'`).FindAllStringSubmatch(command, -1) {
			if len(matches) != 2 {
				continue
			}
			body, err := os.ReadFile(matches[1])
			if err != nil {
				continue
			}
			runner.launchScriptPaths[sessionID] = matches[1]
			runner.launchScriptContents[sessionID] = string(body)
			incarnation := regexp.MustCompile(`AZEDARACH_AGENT_INCARNATION='([^']+)'`).FindStringSubmatch(string(body))
			if len(incarnation) != 2 {
				continue
			}
			runner.panePIDs[sessionID] = 124
			return store.UpsertManagedAgentIdentity(ctx, daemonstate.ManagedAgentIdentity{
				ProjectID: projectID, SessionID: sessionID, LogicalPaneID: "agent", TmuxPaneID: "1",
				PanePID: 124, AgentIncarnation: incarnation[1], ObservedAt: time.Now().UTC().Add(time.Second),
			})
		}
		return errors.New("restart artifact did not contain an agent incarnation")
	}
}

func acknowledgeManagedAgentOnInitialLaunch(t *testing.T, d *Daemon, runner *sessionStartTmuxRunner, projectID string) {
	t.Helper()
	previous := runner.onNewSession
	runner.onNewSession = func(sessionID string) {
		if previous != nil {
			previous(sessionID)
		}
		script := runner.launchScriptContents[sessionID]
		incarnation := regexp.MustCompile(`AZEDARACH_AGENT_INCARNATION='([^']+)'`).FindStringSubmatch(script)
		if len(incarnation) != 2 {
			t.Errorf("initial launch artifact for %s has no planned incarnation", sessionID)
			return
		}
		store := d.sessionRuntimeStateStore(projectID)
		observedAt := time.Now().UTC()
		if current, found, err := store.GetManagedAgentIdentity(context.Background(), d.canonicalProjectID(projectID), sessionID, "agent"); err != nil {
			t.Errorf("load prior managed agent identity: %v", err)
			return
		} else if found && !observedAt.After(current.ObservedAt) {
			// Restart fixtures deliberately seed future observations. Advance by a
			// full second so SQLite's persisted RFC3339 timestamps retain ordering.
			observedAt = current.ObservedAt.Add(time.Second)
		}
		panePID := runner.panePIDs[sessionID]
		if panePID == 0 {
			panePID = 123
		}
		if bundle := regexp.MustCompile(`'([^']*advisor-bootstrap-[^'/]+)/(?:claude-settings\.json|codex-home|work)'`).FindStringSubmatch(script); len(bundle) == 2 {
			runner.currentCommand = strings.ToLower(strings.TrimSpace(d.runtimeConfigForProject(projectID).CLITool))
			if runner.currentCommand == "" {
				runner.currentCommand = "claude"
			}
			paneID := "%1"
			if panes := runner.panes[sessionID]; len(panes) > 0 {
				paneID = panes[0]
			}
			signalPath := filepath.Join(bundle[1], "ready.signal")
			if err := os.WriteFile(signalPath, []byte(fmt.Sprintf("%s\t%s\t%d\n", incarnation[1], paneID, panePID)), 0o600); err != nil {
				t.Errorf("write advisor bootstrap signal: %v", err)
			}
			return
		}
		if err := store.UpsertManagedAgentIdentity(context.Background(), daemonstate.ManagedAgentIdentity{
			ProjectID: d.canonicalProjectID(projectID), SessionID: sessionID, LogicalPaneID: "agent",
			TmuxPaneID: "1", PanePID: panePID, AgentIncarnation: incarnation[1], ObservedAt: observedAt,
		}); err != nil {
			t.Errorf("acknowledge initial managed agent launch: %v", err)
		}
	}
}

func requireNewSessionLaunchCommand(t *testing.T, runner *sessionStartTmuxRunner, sessionID string) string {
	t.Helper()
	for _, command := range runner.commands {
		if len(command) < 7 || command[0] != "new-session" {
			continue
		}
		for i := 0; i+1 < len(command); i++ {
			if command[i] == "-s" && command[i+1] == sessionID {
				return command[len(command)-1]
			}
		}
	}
	t.Fatalf("new-session launch command for %q not found in commands: %+v", sessionID, runner.commands)
	return ""
}

func requireNewSessionLaunchScript(t *testing.T, runner *sessionStartTmuxRunner, sessionID string) string {
	t.Helper()
	contents, ok := runner.launchScriptContents[sessionID]
	if !ok {
		t.Fatalf("new-session launch script for %q not found; paths: %+v", sessionID, runner.launchScriptPaths)
	}
	return contents
}

func tmuxCommandEnvironmentValue(command []string, key string) (string, bool) {
	for i := 0; i+1 < len(command); i++ {
		if command[i] != "-e" {
			continue
		}
		name, value, ok := strings.Cut(command[i+1], "=")
		if ok && name == key {
			return value, true
		}
	}
	return "", false
}

func (r *sessionStartTmuxRunner) RunWithInput(ctx context.Context, input string, args ...string) (string, error) {
	r.commands = append(r.commands, append([]string(nil), args...))
	r.inputPayloads = append(r.inputPayloads, input)
	const promptPrefix = "Read and follow the complete worker instructions in "
	const promptSuffix = ". Delete that file immediately after reading it."
	if strings.HasPrefix(input, promptPrefix) && strings.HasSuffix(input, promptSuffix) {
		path := strings.TrimSuffix(strings.TrimPrefix(input, promptPrefix), promptSuffix)
		if prompt, err := os.ReadFile(filepath.FromSlash(path)); err == nil {
			r.handoffPromptContents = append(r.handoffPromptContents, string(prompt))
		}
		_ = os.Remove(filepath.FromSlash(path))
	}
	if r.onRunWithInput != nil {
		return r.onRunWithInput(ctx, input, args)
	}
	return "", nil
}

func TestWaitForSessionPromptHandoffConsumedRejectsPartialDelivery(t *testing.T) {
	path := filepath.Join(t.TempDir(), "launch.prompt")
	if err := os.WriteFile(path, []byte("complete rooted role"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := waitForSessionPromptHandoffConsumed(ctx, sessionPromptHandoff{PromptPath: path})
	if err == nil || !strings.Contains(err.Error(), "was not consumed") {
		t.Fatalf("wait error = %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("partial handoff was removed: %v", err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	consumedCtx, consumedCancel := context.WithCancel(context.Background())
	consumedCancel()
	if err := waitForSessionPromptHandoffConsumed(consumedCtx, sessionPromptHandoff{PromptPath: path}); err != nil {
		t.Fatalf("consumed handoff: %v", err)
	}
}

func (r *sessionStartTmuxRunner) Run(ctx context.Context, args ...string) (string, error) {
	if len(args) == 0 {
		return "", errors.New("missing tmux args")
	}
	r.commands = append(r.commands, append([]string(nil), args...))
	switch args[0] {
	case "has-session":
		if len(args) < 3 {
			return "", errors.New("missing session name")
		}
		if r.sessions[args[2]] {
			return "", nil
		}
		return "", errors.New("missing session")
	case "new-session":
		if len(args) < 4 {
			return "", errors.New("missing session name")
		}
		if r.maxNewSessionCommand > 0 && len(args) > 4 && len(args[len(args)-1]) > r.maxNewSessionCommand {
			return "", errors.New("command too long")
		}
		if r.onNewSessionCommand != nil {
			if err := r.onNewSessionCommand(ctx, args[3]); err != nil {
				return "", err
			}
		}
		if len(args) > 4 {
			command := args[len(args)-1]
			const scriptArg = " -i '"
			if start := strings.LastIndex(command, scriptArg); strings.HasPrefix(command, "exec ") && strings.HasSuffix(command, "'") {
				path := ""
				if start >= 0 {
					path = strings.TrimSuffix(command[start+len(scriptArg):], "'")
				} else if match := regexp.MustCompile(`'([^']+)'$`).FindStringSubmatch(command); len(match) == 2 {
					path = match[1]
				}
				if path != "" {
					if contents, readErr := os.ReadFile(path); readErr == nil {
						r.launchScriptPaths[args[3]] = path
						r.launchScriptContents[args[3]] = string(contents)
						if info, statErr := os.Stat(path); statErr == nil {
							r.launchScriptModes[args[3]] = info.Mode()
						}
						if info, statErr := os.Stat(filepath.Dir(path)); statErr == nil {
							r.launchArtifactModes[args[3]] = info.Mode()
						}
						promptPath := ""
						if entries, entriesErr := os.ReadDir(filepath.Dir(path)); entriesErr == nil {
							for _, entry := range entries {
								if strings.HasPrefix(entry.Name(), sessionLaunchArtifactPrefix) && strings.HasSuffix(entry.Name(), ".prompt") {
									promptPath = filepath.Join(filepath.Dir(path), entry.Name())
									break
								}
							}
						}
						if promptPath != "" {
							r.launchPromptPaths[args[3]] = promptPath
							if prompt, promptErr := os.ReadFile(promptPath); promptErr == nil {
								r.launchPromptContents[args[3]] = string(prompt)
							}
							if info, statErr := os.Stat(promptPath); statErr == nil {
								r.launchPromptModes[args[3]] = info.Mode()
							}
							_ = os.Remove(promptPath)
						}
					}
				}
			}
		}
		if r.newSessionErr != nil && !r.createBeforeNewSessionError {
			return "", r.newSessionErr
		}
		if path := r.launchScriptPaths[args[3]]; path != "" {
			_ = os.Remove(path)
		}
		if r.onNewSession != nil {
			r.onNewSession(args[3])
		}
		r.sessions[args[3]] = true
		if r.windows[args[3]] == nil {
			r.windows[args[3]] = map[string]bool{"shell": true}
		}
		if r.newSessionErr != nil {
			return "", r.newSessionErr
		}
		return "", nil
	case "kill-session":
		if len(args) < 3 {
			return "", errors.New("missing session name")
		}
		delete(r.sessions, args[2])
		delete(r.windows, args[2])
		delete(r.env, args[2])
		return "", nil
	case "list-windows":
		if len(args) < 3 {
			return "", errors.New("missing session name")
		}
		windows := r.windows[args[2]]
		names := make([]string, 0, len(windows))
		for name := range windows {
			names = append(names, name)
		}
		return strings.Join(names, "\n"), nil
	case "new-window":
		if len(args) < 6 {
			return "", errors.New("missing window args")
		}
		session := args[3]
		window := args[5]
		if r.windows[session] == nil {
			r.windows[session] = map[string]bool{}
		}
		if r.newWindowErr == nil || r.createBeforeNewWindowError {
			r.windows[session][window] = true
		}
		if r.onNewWindow != nil {
			r.onNewWindow(session, args[len(args)-1])
		}
		if r.newWindowErr != nil {
			return "", r.newWindowErr
		}
		return "", nil
	case "respawn-window":
		if len(args) < 4 {
			return "", errors.New("missing respawn window target")
		}
		parts := strings.SplitN(args[3], ":", 2)
		if len(parts) == 2 && (r.respawnWindowErr == nil || r.respawnBeforeWindowError) {
			if r.windows[parts[0]] == nil {
				r.windows[parts[0]] = map[string]bool{}
			}
			r.windows[parts[0]][parts[1]] = true
			if r.onNewWindow != nil {
				r.onNewWindow(parts[0], args[len(args)-1])
			}
		}
		if r.respawnWindowErr != nil {
			return "", r.respawnWindowErr
		}
		return "", nil
	case "kill-window":
		if len(args) < 3 {
			return "", errors.New("missing window target")
		}
		parts := strings.SplitN(args[2], ":", 2)
		if len(parts) == 2 && r.windows[parts[0]] != nil {
			delete(r.windows[parts[0]], parts[1])
		}
		return "", nil
	case "send-keys":
		r.sendKeysCalls++
		if len(args) >= 4 {
			r.sendKeysTargets = append(r.sendKeysTargets, args[2])
			r.sendKeysPayloads = append(r.sendKeysPayloads, args[3])
			if r.onSendKeys != nil {
				r.onSendKeys(args[2], args[3])
			}
		}
		if r.sendKeysErr != nil && (r.sendKeysErrOnCall == 0 || r.sendKeysCalls == r.sendKeysErrOnCall) {
			return "", r.sendKeysErr
		}
		return "", nil
	case "respawn-pane":
		if r.respawnErr != nil {
			return "", r.respawnErr
		}
		if r.onRespawnPane != nil {
			return "", r.onRespawnPane(ctx, append([]string(nil), args...))
		}
		return "", nil
	case "set-environment":
		if len(args) < 5 {
			return "", errors.New("missing set-environment args")
		}
		session := args[2]
		key := args[3]
		value := args[4]
		if r.env[session] == nil {
			r.env[session] = map[string]string{}
		}
		r.env[session][key] = value
		return "", nil
	case "show-environment":
		if len(args) < 3 {
			return "", errors.New("missing show-environment args")
		}
		lines := make([]string, 0, len(r.env[args[2]]))
		for key, value := range r.env[args[2]] {
			lines = append(lines, key+"="+value)
		}
		return strings.Join(lines, "\n"), nil
	case "list-sessions":
		names := make([]string, 0, len(r.sessions))
		for name := range r.sessions {
			names = append(names, name)
		}
		return strings.Join(names, "\n"), nil
	case "list-panes":
		r.listPanesCalls++
		if r.onListPanes != nil {
			r.onListPanes(r.listPanesCalls)
		}
		lines := make([]string, 0, len(r.sessions))
		for name := range r.sessions {
			if r.sessionsWithoutPanes[name] {
				continue
			}
			panes := r.panes[name]
			if len(panes) == 0 {
				panes = []string{"%1"}
			}
			for _, pane := range panes {
				panePID := r.panePIDs[name]
				if panePID == 0 {
					panePID = 123
				}
				if slices.Contains(args, "#{session_name}\t#{pane_id}\t#{pane_pid}\t#{pane_current_command}\t#{session_attached}") {
					currentCommand := runnerCommand(r.currentCommand)
					lines = append(lines, fmt.Sprintf("%s\t%s\t%d\t%s\t0", name, pane, panePID, currentCommand))
				} else if slices.Contains(args, "#{session_name}\t#{pane_id}\t#{pane_pid}\t#{pane_current_command}") {
					currentCommand := runnerCommand(r.currentCommand)
					lines = append(lines, fmt.Sprintf("%s\t%s\t%d\t%s", name, pane, panePID, currentCommand))
				} else {
					lines = append(lines, fmt.Sprintf("%s\t%s\t%d", name, pane, panePID))
				}
			}
		}
		return strings.Join(lines, "\n"), nil
	case "capture-pane":
		return r.captureOutput, nil
	default:
		return "", nil
	}
}

func runnerCommand(command string) string {
	if strings.TrimSpace(command) == "" {
		return "codex"
	}
	return strings.TrimSpace(command)
}

type failingRuntimeProjectionWriter struct {
	recordingRuntimeProjectionWriter
	failWorktreePersist error
	failSessionPersist  error
}

func (w *failingRuntimeProjectionWriter) PersistWorktreeProjection(ctx context.Context, projectID, issueID, path, branch string) error {
	w.record("worktree.persist")
	if w.failWorktreePersist != nil {
		return w.failWorktreePersist
	}
	return w.recordingRuntimeProjectionWriter.PersistWorktreeProjection(ctx, projectID, issueID, path, branch)
}

func (w *failingRuntimeProjectionWriter) PersistSessionProjection(ctx context.Context, projectID string, session daemonstate.Session) error {
	w.record("session.persist")
	if w.failSessionPersist != nil {
		return w.failSessionPersist
	}
	return w.recordingRuntimeProjectionWriter.PersistSessionProjection(ctx, projectID, session)
}

func TestSessionStartRollsBackWorktreeWhenCreateFailsAfterMaterializing(t *testing.T) {
	ctx := context.Background()
	repoDir := t.TempDir()
	projectID := "proj"
	if err := os.MkdirAll(filepath.Join(repoDir, ".azedarach"), 0o755); err != nil {
		t.Fatalf("mkdir .azedarach: %v", err)
	}

	issuesClient := newMigratedIssueClient(t, repoDir, slog.Default())
	t.Cleanup(func() {
		_ = issuesClient.CloseDB()
	})
	issueID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title: "Rollback failed worktree create",
		Type:  domain.TypeTask,
	})
	if err != nil {
		t.Fatalf("create issue: %v", err)
	}

	branch := "testuser/" + issueID + "/rollback-failed-create"
	worktreePath := filepath.Join(filepath.Dir(repoDir), filepath.Base(repoDir)+"-"+issueID)
	if err := os.MkdirAll(worktreePath, 0o755); err != nil {
		t.Fatalf("mkdir materialized worktree: %v", err)
	}
	initMarker := filepath.Join(worktreePath, "init-ran")
	worktreeRunner := &worktreeCreateRunner{
		worktreePath: worktreePath,
		branchName:   branch,
		failCreate:   true,
	}
	tmuxRunner := newSessionStartTmuxRunner()
	store := daemonstate.NewStore()
	runtimeStateStore := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(t.TempDir(), "runtime.db"), slog.Default())
	t.Cleanup(func() { _ = runtimeStateStore.Close() })

	d := &Daemon{
		cfg: Config{
			RepoDir:      repoDir,
			BaseBranch:   "main",
			CLITool:      "codex",
			SessionShell: "zsh",
			WorktreeInitCommands: []string{
				"touch init-ran",
			},
			Logger: slog.Default(),
		},
		tmux:         tmux.NewClient(tmuxRunner, slog.Default()),
		issues:       issuesClient,
		session:      daemonhandlers.NewSessionHandler(store),
		sessionStore: store,
		revision:     map[string]uint64{},
		runtimeStoresByProject: map[string]*daemonstate.RuntimeStateStore{
			projectID: runtimeStateStore,
		},
		worktreeManagersByRoot: map[string]*git.WorktreeManager{
			repoDir: git.NewWorktreeManager(worktreeRunner, repoDir, slog.Default()),
		},
		worktreeManagersByProject: map[string]*git.WorktreeManager{
			projectID: git.NewWorktreeManager(worktreeRunner, repoDir, slog.Default()),
		},
	}

	req := protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-start-rollback-create",
		Kind:            protocol.EnvelopeKindCommand,
		Command:         "session.start",
		Meta: protocol.Metadata{
			ProjectID: naming.ProjectID(projectID),
		},
		Body: marshalJSON(map[string]string{
			"project_id": projectID,
			"session_id": issueID,
		}),
	}

	resp, err := d.handleSessionStartDirect(ctx, req)
	if err != nil {
		t.Fatalf("handleSessionStartDirect returned error: %v", err)
	}
	if resp.OK || resp.Error == nil {
		t.Fatalf("session start response = %+v, want worktree create failure", resp)
	}
	for _, want := range []string{
		"worktree create failed for " + issueID,
		"git worktree add -b",
		worktreePath,
		"hook failed",
		"rolled back worktree " + worktreePath,
	} {
		if !strings.Contains(resp.Error.Message, want) {
			t.Fatalf("error message = %q, want %q", resp.Error.Message, want)
		}
	}
	if worktreeRunner.lsFilesDeleted != 0 {
		t.Fatalf("ls-files -d calls = %d, want 0 when create fails", worktreeRunner.lsFilesDeleted)
	}
	if worktreeRunner.statusCalls != 0 {
		t.Fatalf("status --porcelain calls = %d, want 0 when create fails", worktreeRunner.statusCalls)
	}
	if _, err := os.Stat(initMarker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("init marker stat error = %v, want not exist", err)
	}
	if !worktreeRunner.worktreeRemoved {
		t.Fatal("expected failed create to roll back materialized worktree")
	}
	if !worktreeRunner.branchDeleted {
		t.Fatal("expected failed create rollback to delete issue branch")
	}
	if len(tmuxRunner.sessions) != 0 {
		t.Fatalf("tmux sessions = %v, want none when create fails", tmuxRunner.sessions)
	}
	if tmuxRunner.sendKeysCalls != 0 {
		t.Fatalf("send-keys calls = %d, want none when create fails", tmuxRunner.sendKeysCalls)
	}
	task, err := issuesClient.GetWithRuntime(ctx, projectID, issueID)
	if err != nil {
		t.Fatalf("get issue after failure: %v", err)
	}
	if task.Status != domain.StatusOpen {
		t.Fatalf("issue status = %s, want rollback to %s", task.Status, domain.StatusOpen)
	}
}

func TestSessionStartDoesNotInspectDirtyWorktreeAfterCreateFailure(t *testing.T) {
	ctx := context.Background()
	repoDir := t.TempDir()
	projectID := "proj"
	if err := os.MkdirAll(filepath.Join(repoDir, ".azedarach"), 0o755); err != nil {
		t.Fatalf("mkdir .azedarach: %v", err)
	}

	issuesClient := newMigratedIssueClient(t, repoDir, slog.Default())
	t.Cleanup(func() {
		_ = issuesClient.CloseDB()
	})
	issueID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title: "Rollback dirty materialized worktree",
		Type:  domain.TypeTask,
	})
	if err != nil {
		t.Fatalf("create issue: %v", err)
	}

	branch := "testuser/" + issueID + "/rollback-dirty-create"
	worktreePath := filepath.Join(filepath.Dir(repoDir), filepath.Base(repoDir)+"-"+issueID)
	if err := os.MkdirAll(worktreePath, 0o755); err != nil {
		t.Fatalf("mkdir worktree: %v", err)
	}
	initMarker := filepath.Join(worktreePath, "init-ran")
	worktreeRunner := &worktreeCreateRunner{
		worktreePath:    worktreePath,
		branchName:      branch,
		failCreate:      true,
		deletedFiles:    []string{"internal-docs/setup.md", "ui/package.json"},
		porcelainStatus: " D internal-docs/setup.md\n D ui/package.json\n?? generated.js\n",
	}
	tmuxRunner := newSessionStartTmuxRunner()
	store := daemonstate.NewStore()

	d := &Daemon{
		cfg: Config{
			RepoDir:              repoDir,
			BaseBranch:           "main",
			CLITool:              "codex",
			SessionShell:         "zsh",
			WorktreeInitCommands: []string{"touch init-ran"},
			Logger:               slog.Default(),
		},
		tmux:         tmux.NewClient(tmuxRunner, slog.Default()),
		issues:       issuesClient,
		session:      daemonhandlers.NewSessionHandler(store),
		sessionStore: store,
		revision:     map[string]uint64{},
		worktreeManagersByRoot: map[string]*git.WorktreeManager{
			repoDir: git.NewWorktreeManager(worktreeRunner, repoDir, slog.Default()),
		},
		worktreeManagersByProject: map[string]*git.WorktreeManager{
			projectID: git.NewWorktreeManager(worktreeRunner, repoDir, slog.Default()),
		},
	}

	resp, err := d.handleSessionStartDirect(ctx, protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-start-rollback-dirty-materialized",
		Kind:            protocol.EnvelopeKindCommand,
		Command:         "session.start",
		Meta:            protocol.Metadata{ProjectID: naming.ProjectID(projectID)},
		Body: marshalJSON(map[string]any{
			"project_id": projectID,
			"session_id": issueID,
			"start_work": false,
		}),
	})
	if err != nil {
		t.Fatalf("handleSessionStartDirect returned error: %v", err)
	}
	if resp.OK || resp.Error == nil {
		t.Fatalf("session start response = %+v, want materialized worktree create failure", resp)
	}
	if !strings.Contains(resp.Error.Message, "worktree create failed for "+issueID) {
		t.Fatalf("error message = %q, want create failure", resp.Error.Message)
	}
	if !strings.Contains(resp.Error.Message, "rolled back worktree "+worktreePath) {
		t.Fatalf("error message = %q, want rollback note", resp.Error.Message)
	}
	if worktreeRunner.lsFilesDeleted != 0 {
		t.Fatalf("ls-files -d calls = %d, want 0 when create fails", worktreeRunner.lsFilesDeleted)
	}
	if worktreeRunner.statusCalls != 0 {
		t.Fatalf("status --porcelain calls = %d, want 0 when create fails", worktreeRunner.statusCalls)
	}
	if _, err := os.Stat(initMarker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("init marker stat error = %v, want not exist", err)
	}
	for _, payload := range tmuxRunner.sendKeysPayloads {
		if strings.Contains(payload, "codex") {
			t.Fatalf("send-keys payload = %q, want no AI launch when start_work=false", payload)
		}
	}
	if tmuxRunner.sessions[naming.CanonicalSessionID(projectID, issueID)] {
		t.Fatalf("tmux session %q was created", naming.CanonicalSessionID(projectID, issueID))
	}
	if !worktreeRunner.worktreeRemoved {
		t.Fatal("expected failed create to roll back materialized worktree")
	}
}

func TestSessionStartReusesPreexistingDirtyWorktreeWithoutInit(t *testing.T) {
	ctx := context.Background()
	repoDir := t.TempDir()
	projectID := "proj"
	if err := os.MkdirAll(filepath.Join(repoDir, ".azedarach"), 0o755); err != nil {
		t.Fatalf("mkdir .azedarach: %v", err)
	}

	issuesClient := newMigratedIssueClient(t, repoDir, slog.Default())
	t.Cleanup(func() {
		_ = issuesClient.CloseDB()
	})
	issueID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title: "Reuse existing dirty worktree",
		Type:  domain.TypeTask,
	})
	if err != nil {
		t.Fatalf("create issue: %v", err)
	}

	branch := "testuser/" + issueID + "/reuse-existing-dirty-wor"
	worktreePath := filepath.Join(filepath.Dir(repoDir), filepath.Base(repoDir)+"-"+issueID)
	if err := os.MkdirAll(worktreePath, 0o755); err != nil {
		t.Fatalf("mkdir worktree: %v", err)
	}
	initMarker := filepath.Join(worktreePath, "init-ran")
	worktreeRunner := &worktreeCreateRunner{
		worktreePath:    worktreePath,
		branchName:      branch,
		preexisting:     true,
		deletedFiles:    []string{"internal-docs/setup.md"},
		porcelainStatus: " D internal-docs/setup.md\n?? generated.js\n",
	}
	tmuxRunner := newSessionStartTmuxRunner()
	store := daemonstate.NewStore()

	d := &Daemon{
		cfg: Config{
			RepoDir:              repoDir,
			BaseBranch:           "main",
			CLITool:              "codex",
			SessionShell:         "zsh",
			WorktreeInitCommands: []string{"touch init-ran"},
			Logger:               slog.Default(),
		},
		tmux:         tmux.NewClient(tmuxRunner, slog.Default()),
		issues:       issuesClient,
		session:      daemonhandlers.NewSessionHandler(store),
		sessionStore: store,
		revision:     map[string]uint64{},
		worktreeManagersByRoot: map[string]*git.WorktreeManager{
			repoDir: git.NewWorktreeManager(worktreeRunner, repoDir, slog.Default()),
		},
		worktreeManagersByProject: map[string]*git.WorktreeManager{
			projectID: git.NewWorktreeManager(worktreeRunner, repoDir, slog.Default()),
		},
	}

	resp, err := d.handleSessionStartDirect(ctx, protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-start-reuse-preexisting-dirty",
		Kind:            protocol.EnvelopeKindCommand,
		Command:         "session.start",
		Meta:            protocol.Metadata{ProjectID: naming.ProjectID(projectID)},
		Body: marshalJSON(map[string]any{
			"project_id": projectID,
			"session_id": issueID,
			"start_work": false,
		}),
	})
	if err != nil {
		t.Fatalf("handleSessionStartDirect returned error: %v", err)
	}
	if !resp.OK || resp.Error != nil {
		t.Fatalf("session start response = %+v, want pre-existing worktree reuse success", resp)
	}
	var payload struct {
		Output string `json:"output"`
	}
	if err := json.Unmarshal(resp.Body, &payload); err != nil {
		t.Fatalf("unmarshal response body: %v", err)
	}
	for _, want := range []string{
		"Worktree reused: " + worktreePath,
		"without running worktree init commands",
		"Skipping AI launch (tmux session only)",
		"Session started successfully",
	} {
		if !strings.Contains(payload.Output, want) {
			t.Fatalf("output = %q, want %q", payload.Output, want)
		}
	}
	if worktreeRunner.lsFilesDeleted != 0 {
		t.Fatalf("ls-files -d calls = %d, want 0 for reused pre-existing worktree", worktreeRunner.lsFilesDeleted)
	}
	if worktreeRunner.statusCalls != 0 {
		t.Fatalf("status --porcelain calls = %d, want 0 for reused pre-existing worktree", worktreeRunner.statusCalls)
	}
	if _, err := os.Stat(initMarker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("init marker stat error = %v, want not exist", err)
	}
	if !tmuxRunner.sessions[naming.CanonicalSessionID(projectID, issueID)] {
		t.Fatalf("tmux session %q was not created", naming.CanonicalSessionID(projectID, issueID))
	}
}

func TestSessionStartAllowsNewWorktreeWithPreInitStatusOutput(t *testing.T) {
	ctx := context.Background()
	repoDir := t.TempDir()
	projectID := "proj"
	if err := os.MkdirAll(filepath.Join(repoDir, ".azedarach"), 0o755); err != nil {
		t.Fatalf("mkdir .azedarach: %v", err)
	}

	issuesClient := newMigratedIssueClient(t, repoDir, slog.Default())
	t.Cleanup(func() {
		_ = issuesClient.CloseDB()
	})
	issueID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title: "Chefy preinit status should start",
		Type:  domain.TypeTask,
	})
	if err != nil {
		t.Fatalf("create issue: %v", err)
	}

	worktreePath := filepath.Join(filepath.Dir(repoDir), filepath.Base(repoDir)+"-"+issueID)
	branchName := "testuser/" + issueID + "/chefy-preinit-status-shou"
	worktreeRunner := &initFailureCleanupWorktreeRunner{
		repoDir:      repoDir,
		worktreePath: worktreePath,
		branchName:   branchName,
		deletedFiles: []string{"internal-docs/setup.md"},
		porcelain:    " D internal-docs/setup.md\n?? generated.js\n",
	}
	tmuxRunner := newSessionStartTmuxRunner()
	store := daemonstate.NewStore()

	d := &Daemon{
		cfg: Config{
			RepoDir:      repoDir,
			BaseBranch:   "main",
			CLITool:      "codex",
			SessionShell: "zsh",
			Logger:       slog.Default(),
		},
		tmux:         tmux.NewClient(tmuxRunner, slog.Default()),
		issues:       issuesClient,
		session:      daemonhandlers.NewSessionHandler(store),
		sessionStore: store,
		revision:     map[string]uint64{},
		worktreeManagersByRoot: map[string]*git.WorktreeManager{
			repoDir: git.NewWorktreeManager(worktreeRunner, repoDir, slog.Default()),
		},
		worktreeManagersByProject: map[string]*git.WorktreeManager{
			projectID: git.NewWorktreeManager(worktreeRunner, repoDir, slog.Default()),
		},
	}

	resp, err := d.handleSessionStartDirect(ctx, protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-start-preinit-status-worktree",
		Kind:            protocol.EnvelopeKindCommand,
		Command:         "session.start",
		Meta:            protocol.Metadata{ProjectID: naming.ProjectID(projectID)},
		Body: marshalJSON(map[string]any{
			"project_id": projectID,
			"session_id": issueID,
			"start_work": false,
		}),
	})
	if err != nil {
		t.Fatalf("handleSessionStartDirect returned error: %v", err)
	}
	if !resp.OK || resp.Error != nil {
		t.Fatalf("session start response = %+v, want success", resp)
	}
	if worktreeRunner.statusCalls != 0 {
		t.Fatalf("pre-init clean status calls = %d, want 0", worktreeRunner.statusCalls)
	}
	if worktreeRunner.removeForced {
		t.Fatal("new worktree should not be removed during successful session start")
	}
	if !worktreeRunner.worktreeExists {
		t.Fatal("new worktree should remain after successful session start")
	}
	if !tmuxRunner.sessions[naming.CanonicalSessionID(projectID, issueID)] {
		t.Fatalf("tmux session %q was not created", naming.CanonicalSessionID(projectID, issueID))
	}
}

func TestSessionStartWithoutAgentExportsContextToLiveShell(t *testing.T) {
	ctx := context.Background()
	repoDir := t.TempDir()
	projectID := "proj"
	if err := os.MkdirAll(filepath.Join(repoDir, ".azedarach"), 0o755); err != nil {
		t.Fatalf("mkdir .azedarach: %v", err)
	}

	issuesClient := newMigratedIssueClient(t, repoDir, slog.Default())
	t.Cleanup(func() {
		_ = issuesClient.CloseDB()
	})
	issueID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title: "Shell only session",
		Type:  domain.TypeTask,
	})
	if err != nil {
		t.Fatalf("create issue: %v", err)
	}

	worktreeRunner := &worktreeCreateRunner{
		worktreePath: filepath.Join(filepath.Dir(repoDir), filepath.Base(repoDir)+"-"+issueID),
		branchName:   "testuser/" + issueID + "/shell-only-session",
	}
	tmuxRunner := newSessionStartTmuxRunner()
	store := daemonstate.NewStore()
	d := &Daemon{
		cfg: Config{
			RepoDir:      repoDir,
			BaseBranch:   "main",
			CLITool:      "codex",
			SessionShell: "zsh",
			Logger:       slog.Default(),
		},
		tmux:         tmux.NewClient(tmuxRunner, slog.Default()),
		issues:       issuesClient,
		session:      daemonhandlers.NewSessionHandler(store),
		sessionStore: store,
		revision:     map[string]uint64{},
		worktreeManagersByRoot: map[string]*git.WorktreeManager{
			repoDir: git.NewWorktreeManager(worktreeRunner, repoDir, slog.Default()),
		},
		worktreeManagersByProject: map[string]*git.WorktreeManager{
			projectID: git.NewWorktreeManager(worktreeRunner, repoDir, slog.Default()),
		},
	}

	resp, err := d.handleSessionStartDirect(ctx, protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-start-shell-only-context",
		Kind:            protocol.EnvelopeKindCommand,
		Command:         "session.start",
		Meta:            protocol.Metadata{ProjectID: naming.ProjectID(projectID)},
		Body: marshalJSON(map[string]any{
			"project_id": projectID,
			"session_id": issueID,
			"start_work": false,
		}),
	})
	if err != nil {
		t.Fatalf("handleSessionStartDirect returned error: %v", err)
	}
	if !resp.OK {
		t.Fatalf("session start response not OK: %+v", resp)
	}

	sessionID := naming.CanonicalSessionID(projectID, issueID)
	if tmuxRunner.sendKeysCalls != 1 {
		t.Fatalf("send-keys calls = %d, want one live-shell context export", tmuxRunner.sendKeysCalls)
	}
	if got := tmuxRunner.sendKeysTargets[0]; got != sessionID {
		t.Fatalf("send-keys target = %q, want %q", got, sessionID)
	}
	contextExport := tmuxRunner.sendKeysPayloads[0]
	for _, want := range []string{
		"export ",
		"AZEDARACH_PROJECT_ID='" + projectID + "'",
		"AZEDARACH_ISSUE_ID='" + issueID + "'",
		"AZEDARACH_SESSION_ID='" + sessionID + "'",
	} {
		if !strings.Contains(contextExport, want) {
			t.Fatalf("context export = %q, want %q", contextExport, want)
		}
	}
	if strings.Contains(contextExport, " codex") {
		t.Fatalf("context export = %q, shell-only start must not launch codex", contextExport)
	}
	for key, want := range map[string]string{
		"AZEDARACH_PROJECT_ID": projectID,
		"AZEDARACH_ISSUE_ID":   issueID,
		"AZEDARACH_SESSION_ID": sessionID,
	} {
		if got := tmuxRunner.env[sessionID][key]; got != want {
			t.Fatalf("tmux %s = %q, want %q", key, got, want)
		}
	}

	snapshot := store.ReadSnapshot(projectID)
	if _, ok := snapshot.Sessions[sessionID]; !ok {
		t.Fatalf("missing session %q in snapshot: %+v", sessionID, snapshot.Sessions)
	}
}

func TestSessionStartIgnoresStaleProjectionWhenTmuxHasNoSession(t *testing.T) {
	ctx := context.Background()
	repoDir := t.TempDir()
	projectID := "proj"
	if err := os.MkdirAll(filepath.Join(repoDir, ".azedarach"), 0o755); err != nil {
		t.Fatalf("mkdir .azedarach: %v", err)
	}

	issuesClient := newMigratedIssueClient(t, repoDir, slog.Default())
	t.Cleanup(func() {
		_ = issuesClient.CloseDB()
	})
	issueID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title: "Start should use tmux truth",
		Type:  domain.TypeTask,
	})
	if err != nil {
		t.Fatalf("create issue: %v", err)
	}

	branch := "testuser/" + issueID + "/tmux-truth"
	worktreePath := filepath.Join(filepath.Dir(repoDir), filepath.Base(repoDir)+"-"+issueID)
	worktreeRunner := &worktreeCreateRunner{
		worktreePath: worktreePath,
		branchName:   branch,
	}
	tmuxRunner := newSessionStartTmuxRunner()
	store := daemonstate.NewStore()
	sessionID := naming.CanonicalSessionID(projectID, issueID)
	if _, err := store.UpsertSession(projectID, sessionID, issueID, daemonstate.SessionStateStarting); err != nil {
		t.Fatalf("seed stale starting session: %v", err)
	}
	if _, err := store.UpsertSession(projectID, sessionID, issueID, daemonstate.SessionStateAttached); err != nil {
		t.Fatalf("seed stale attached session: %v", err)
	}

	d := &Daemon{
		cfg: Config{
			RepoDir:      repoDir,
			BaseBranch:   "main",
			CLITool:      "codex",
			SessionShell: "zsh",
			Logger:       slog.Default(),
		},
		tmux:         tmux.NewClient(tmuxRunner, slog.Default()),
		issues:       issuesClient,
		session:      daemonhandlers.NewSessionHandler(store),
		sessionStore: store,
		revision:     map[string]uint64{},
		worktreeManagersByRoot: map[string]*git.WorktreeManager{
			repoDir: git.NewWorktreeManager(worktreeRunner, repoDir, slog.Default()),
		},
		worktreeManagersByProject: map[string]*git.WorktreeManager{
			projectID: git.NewWorktreeManager(worktreeRunner, repoDir, slog.Default()),
		},
	}

	acknowledgeManagedAgentOnInitialLaunch(t, d, tmuxRunner, projectID)
	req := protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-start-tmux-truth",
		Kind:            protocol.EnvelopeKindCommand,
		Command:         "session.start",
		Meta: protocol.Metadata{
			ProjectID: naming.ProjectID(projectID),
		},
		Body: marshalJSON(map[string]string{
			"project_id": projectID,
			"session_id": issueID,
		}),
	}

	resp, err := d.handleSessionStartDirect(ctx, req)
	if err != nil {
		t.Fatalf("handleSessionStartDirect returned error: %v", err)
	}
	if !resp.OK {
		t.Fatalf("session start response not OK: %+v", resp)
	}
	if !tmuxRunner.sessions[sessionID] {
		t.Fatalf("expected tmux session %q to be created", sessionID)
	}
}

func TestSessionStartReactivatesClosedIssueBeforeRuntimeProjection(t *testing.T) {
	ctx := context.Background()
	repoDir := t.TempDir()
	projectID := "proj"
	if err := os.MkdirAll(filepath.Join(repoDir, ".azedarach"), 0o755); err != nil {
		t.Fatalf("mkdir .azedarach: %v", err)
	}

	issuesClient := newMigratedIssueClient(t, repoDir, slog.Default())
	t.Cleanup(func() { _ = issuesClient.CloseDB() })
	issueID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title: "Closed task should reactivate",
		Type:  domain.TypeTask,
	})
	if err != nil {
		t.Fatalf("create issue: %v", err)
	}
	if err := issuesClient.Update(ctx, issueID, domain.StatusDone); err != nil {
		t.Fatalf("close issue: %v", err)
	}

	worktreePath := filepath.Join(filepath.Dir(repoDir), filepath.Base(repoDir)+"-"+issueID)
	worktreeRunner := &worktreeCreateRunner{
		worktreePath: worktreePath,
		branchName:   "testuser/" + issueID + "/closed-task-should-react",
	}
	tmuxRunner := newSessionStartTmuxRunner()
	store := daemonstate.NewStore()
	runtimeStateStore := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(t.TempDir(), "runtime.db"), slog.Default())
	t.Cleanup(func() { _ = runtimeStateStore.Close() })

	d := &Daemon{
		cfg: Config{
			RepoDir:      repoDir,
			BaseBranch:   "main",
			CLITool:      "codex",
			SessionShell: "zsh",
			Logger:       slog.Default(),
		},
		tmux:         tmux.NewClient(tmuxRunner, slog.Default()),
		issues:       issuesClient,
		session:      daemonhandlers.NewSessionHandler(store),
		sessionStore: store,
		revision:     map[string]uint64{},
		runtimeStoresByProject: map[string]*daemonstate.RuntimeStateStore{
			projectID: runtimeStateStore,
		},
		worktreeManagersByRoot: map[string]*git.WorktreeManager{
			repoDir: git.NewWorktreeManager(worktreeRunner, repoDir, slog.Default()),
		},
		worktreeManagersByProject: map[string]*git.WorktreeManager{
			projectID: git.NewWorktreeManager(worktreeRunner, repoDir, slog.Default()),
		},
	}

	resp, err := d.handleSessionStartDirect(ctx, protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-start-closed-reactivate",
		Kind:            protocol.EnvelopeKindCommand,
		Command:         daemonhandlers.CommandSessionStart,
		Meta:            protocol.Metadata{ProjectID: naming.ProjectID(projectID)},
		Body: marshalJSON(map[string]any{
			"project_id": projectID,
			"session_id": issueID,
			"start_work": false,
		}),
	})
	if err != nil {
		t.Fatalf("handleSessionStartDirect returned error: %v", err)
	}
	if !resp.OK || resp.Error != nil {
		t.Fatalf("session start response = %+v, want success", resp)
	}
	task, err := issuesClient.GetWithRuntime(ctx, projectID, issueID)
	if err != nil {
		t.Fatalf("get issue: %v", err)
	}
	if task.Status != domain.StatusInProgress {
		t.Fatalf("issue status = %s, want %s", task.Status, domain.StatusInProgress)
	}
	if _, found, err := runtimeStateStore.GetWorktreeStateByIssueID(ctx, projectID, issueID); err != nil || !found {
		t.Fatalf("worktree projection found=%t err=%v, want persisted", found, err)
	}
	sessionID := naming.CanonicalSessionID(projectID, issueID)
	sessionProjection, found, err := runtimeStateStore.GetSessionState(ctx, projectID, sessionID)
	if err != nil || !found {
		t.Fatalf("session projection found=%t err=%v, want persisted", found, err)
	}
	if sessionProjection.State == daemonstate.SessionStateStopped {
		t.Fatalf("session projection = %+v, want active", sessionProjection)
	}
}

func TestSessionStartRetriesTransientWorktreeProjectionWriterContention(t *testing.T) {
	ctx := context.Background()
	repoDir := t.TempDir()
	projectID := "proj"
	if err := os.MkdirAll(filepath.Join(repoDir, ".azedarach"), 0o755); err != nil {
		t.Fatalf("mkdir .azedarach: %v", err)
	}

	issuesClient := newMigratedIssueClient(t, repoDir, slog.Default())
	t.Cleanup(func() { _ = issuesClient.CloseDB() })
	issueID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title: "Transient projection contention should retry",
		Type:  domain.TypeTask,
	})
	if err != nil {
		t.Fatalf("create issue: %v", err)
	}

	worktreePath := filepath.Join(filepath.Dir(repoDir), filepath.Base(repoDir)+"-"+issueID)
	worktreeRunner := &worktreeCreateRunner{
		worktreePath: worktreePath,
		branchName:   "testuser/" + issueID + "/transient-projection-retry",
	}
	tmuxRunner := newSessionStartTmuxRunner()
	sessionStore := daemonstate.NewStore()
	runtimeDBPath := filepath.Join(t.TempDir(), "runtime.db")
	runtimeStateStore := daemonstate.NewRuntimeStateStoreAtPath(runtimeDBPath, slog.Default())
	t.Cleanup(func() { _ = runtimeStateStore.Close() })
	if _, err := runtimeStateStore.ListWorktreeStates(ctx, projectID); err != nil {
		t.Fatalf("warm runtime projection store: %v", err)
	}

	projectionAttempts := 0
	ctx = daemonstate.WithSQLiteWriteAttemptHookForTest(ctx, func(operation string, attempt int) error {
		if operation != "upsert_worktree_state" {
			return nil
		}
		projectionAttempts = attempt
		if attempt == 1 {
			return codedSQLiteDaemonTestError{code: 517}
		}
		return nil
	})

	d := &Daemon{
		cfg: Config{
			RepoDir:      repoDir,
			BaseBranch:   "main",
			CLITool:      "codex",
			SessionShell: "zsh",
			Logger:       slog.Default(),
		},
		tmux:         tmux.NewClient(tmuxRunner, slog.Default()),
		issues:       issuesClient,
		session:      daemonhandlers.NewSessionHandler(sessionStore),
		sessionStore: sessionStore,
		revision:     map[string]uint64{},
		runtimeStoresByProject: map[string]*daemonstate.RuntimeStateStore{
			projectID: runtimeStateStore,
		},
		worktreeManagersByRoot: map[string]*git.WorktreeManager{
			repoDir: git.NewWorktreeManager(worktreeRunner, repoDir, slog.Default()),
		},
		worktreeManagersByProject: map[string]*git.WorktreeManager{
			projectID: git.NewWorktreeManager(worktreeRunner, repoDir, slog.Default()),
		},
	}

	resp, err := d.handleSessionStartDirect(ctx, protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-start-projection-contention",
		Kind:            protocol.EnvelopeKindCommand,
		Command:         daemonhandlers.CommandSessionStart,
		Meta:            protocol.Metadata{ProjectID: naming.ProjectID(projectID)},
		Body: marshalJSON(map[string]any{
			"project_id": projectID,
			"session_id": issueID,
			"start_work": false,
		}),
	})
	if err != nil {
		t.Fatalf("handleSessionStartDirect returned error: %v", err)
	}
	if !resp.OK || resp.Error != nil {
		t.Fatalf("session start response = %+v, want success after transient contention", resp)
	}
	if projectionAttempts != 2 {
		t.Fatalf("worktree projection attempts = %d, want 2 after injected transient contention", projectionAttempts)
	}
	if worktreeRunner.worktreeRemoved {
		t.Fatal("worktree cleanup ran despite successful projection retry")
	}
	task, err := issuesClient.GetWithRuntime(ctx, projectID, issueID)
	if err != nil {
		t.Fatalf("get issue after successful projection retry: %v", err)
	}
	if task.Status != domain.StatusInProgress {
		t.Fatalf("issue status = %s, want %s after coherent start", task.Status, domain.StatusInProgress)
	}
	if _, found, err := runtimeStateStore.GetWorktreeStateByIssueID(ctx, projectID, issueID); err != nil || !found {
		t.Fatalf("worktree projection found=%t err=%v, want coherent persisted projection", found, err)
	}
	sessionID := naming.CanonicalSessionID(projectID, issueID)
	if projection, found, err := runtimeStateStore.GetSessionState(ctx, projectID, sessionID); err != nil || !found || projection.State == daemonstate.SessionStateStopped {
		t.Fatalf("session projection found=%t err=%v projection=%+v, want active persisted projection", found, err, projection)
	}
	if !tmuxRunner.sessions[sessionID] {
		t.Fatalf("tmux session %q missing after successful projection retry", sessionID)
	}
}

func TestSessionStartFailsAndRollsBackWhenSessionProjectionPersistFails(t *testing.T) {
	ctx := context.Background()
	repoDir := t.TempDir()
	projectID := "proj"
	if err := os.MkdirAll(filepath.Join(repoDir, ".azedarach"), 0o755); err != nil {
		t.Fatalf("mkdir .azedarach: %v", err)
	}

	issuesClient := newMigratedIssueClient(t, repoDir, slog.Default())
	t.Cleanup(func() { _ = issuesClient.CloseDB() })
	issueID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title: "Projection failure should fail",
		Type:  domain.TypeTask,
	})
	if err != nil {
		t.Fatalf("create issue: %v", err)
	}

	worktreePath := filepath.Join(filepath.Dir(repoDir), filepath.Base(repoDir)+"-"+issueID)
	worktreeRunner := &worktreeCreateRunner{
		worktreePath: worktreePath,
		branchName:   "testuser/" + issueID + "/projection-failure-shou",
	}
	tmuxRunner := newSessionStartTmuxRunner()
	store := daemonstate.NewStore()
	sessionID := naming.CanonicalSessionID(projectID, issueID)
	projectionErr := errors.New("runtime projection unavailable")

	d := &Daemon{
		cfg: Config{
			RepoDir:      repoDir,
			BaseBranch:   "main",
			CLITool:      "codex",
			SessionShell: "zsh",
			Logger:       slog.Default(),
		},
		tmux:                    tmux.NewClient(tmuxRunner, slog.Default()),
		issues:                  issuesClient,
		session:                 daemonhandlers.NewSessionHandler(store),
		sessionStore:            store,
		revision:                map[string]uint64{},
		runtimeProjectionWriter: &failingRuntimeProjectionWriter{failSessionPersist: projectionErr},
		worktreeManagersByRoot: map[string]*git.WorktreeManager{
			repoDir: git.NewWorktreeManager(worktreeRunner, repoDir, slog.Default()),
		},
		worktreeManagersByProject: map[string]*git.WorktreeManager{
			projectID: git.NewWorktreeManager(worktreeRunner, repoDir, slog.Default()),
		},
	}

	resp, err := d.handleSessionStartDirect(ctx, protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-start-session-projection-fails",
		Kind:            protocol.EnvelopeKindCommand,
		Command:         daemonhandlers.CommandSessionStart,
		Meta:            protocol.Metadata{ProjectID: naming.ProjectID(projectID)},
		Body: marshalJSON(map[string]any{
			"project_id": projectID,
			"session_id": issueID,
			"start_work": false,
		}),
	})
	if err != nil {
		t.Fatalf("handleSessionStartDirect returned error: %v", err)
	}
	if resp.OK || resp.Error == nil {
		t.Fatalf("session start response = %+v, want projection failure", resp)
	}
	if !strings.Contains(resp.Error.Message, projectionErr.Error()) {
		t.Fatalf("error message = %q, want projection failure", resp.Error.Message)
	}
	if tmuxRunner.sessions[sessionID] {
		t.Fatalf("tmux session %q still exists after projection failure", sessionID)
	}
	snapshot := store.ReadSnapshot(projectID)
	if got := snapshot.Sessions[sessionID]; got.State != daemonstate.SessionStateStarting {
		t.Fatalf("session store state = %+v, want unchanged starting state when durable compensation has no winner", got)
	}
	if !worktreeRunner.worktreeRemoved {
		t.Fatal("expected new worktree cleanup after session projection failure")
	}
	task, err := issuesClient.GetWithRuntime(ctx, projectID, issueID)
	if err != nil {
		t.Fatalf("get issue after failure: %v", err)
	}
	if task.Status != domain.StatusOpen {
		t.Fatalf("issue status = %s, want rollback to %s", task.Status, domain.StatusOpen)
	}
}

func TestSessionStartFailsBeforeTmuxWhenWorktreeProjectionPersistFails(t *testing.T) {
	ctx := context.Background()
	repoDir := t.TempDir()
	projectID := "proj"
	if err := os.MkdirAll(filepath.Join(repoDir, ".azedarach"), 0o755); err != nil {
		t.Fatalf("mkdir .azedarach: %v", err)
	}

	issuesClient := newMigratedIssueClient(t, repoDir, slog.Default())
	t.Cleanup(func() { _ = issuesClient.CloseDB() })
	issueID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title: "Worktree projection failure should fail",
		Type:  domain.TypeTask,
	})
	if err != nil {
		t.Fatalf("create issue: %v", err)
	}

	worktreePath := filepath.Join(filepath.Dir(repoDir), filepath.Base(repoDir)+"-"+issueID)
	worktreeRunner := &worktreeCreateRunner{
		worktreePath: worktreePath,
		branchName:   "testuser/" + issueID + "/worktree-projection-fai",
	}
	tmuxRunner := newSessionStartTmuxRunner()
	store := daemonstate.NewStore()
	projectionErr := errors.New("worktree projection unavailable")

	d := &Daemon{
		cfg: Config{
			RepoDir:      repoDir,
			BaseBranch:   "main",
			CLITool:      "codex",
			SessionShell: "zsh",
			Logger:       slog.Default(),
		},
		tmux:                    tmux.NewClient(tmuxRunner, slog.Default()),
		issues:                  issuesClient,
		session:                 daemonhandlers.NewSessionHandler(store),
		sessionStore:            store,
		revision:                map[string]uint64{},
		runtimeProjectionWriter: &failingRuntimeProjectionWriter{failWorktreePersist: projectionErr},
		worktreeManagersByRoot: map[string]*git.WorktreeManager{
			repoDir: git.NewWorktreeManager(worktreeRunner, repoDir, slog.Default()),
		},
		worktreeManagersByProject: map[string]*git.WorktreeManager{
			projectID: git.NewWorktreeManager(worktreeRunner, repoDir, slog.Default()),
		},
	}

	resp, err := d.handleSessionStartDirect(ctx, protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-start-worktree-projection-fails",
		Kind:            protocol.EnvelopeKindCommand,
		Command:         daemonhandlers.CommandSessionStart,
		Meta:            protocol.Metadata{ProjectID: naming.ProjectID(projectID)},
		Body: marshalJSON(map[string]any{
			"project_id": projectID,
			"session_id": issueID,
			"start_work": false,
		}),
	})
	if err != nil {
		t.Fatalf("handleSessionStartDirect returned error: %v", err)
	}
	if resp.OK || resp.Error == nil {
		t.Fatalf("session start response = %+v, want projection failure", resp)
	}
	if !strings.Contains(resp.Error.Message, projectionErr.Error()) {
		t.Fatalf("error message = %q, want projection failure", resp.Error.Message)
	}
	if len(tmuxRunner.sessions) != 0 {
		t.Fatalf("tmux sessions = %v, want none before worktree projection succeeds", tmuxRunner.sessions)
	}
	if !worktreeRunner.worktreeRemoved {
		t.Fatal("expected new worktree cleanup after worktree projection failure")
	}
	task, err := issuesClient.GetWithRuntime(ctx, projectID, issueID)
	if err != nil {
		t.Fatalf("get issue after failure: %v", err)
	}
	if task.Status != domain.StatusOpen {
		t.Fatalf("issue status = %s, want rollback to %s", task.Status, domain.StatusOpen)
	}
}

func TestSessionStartRejectsForeignPrefixedLiveIssueSession(t *testing.T) {
	ctx := context.Background()
	const (
		projectID = "proj"
		issueID   = "frp"
		liveName  = "ch-frp"
	)

	tmuxRunner := newSessionStartTmuxRunner()
	tmuxRunner.sessions[liveName] = true
	d := &Daemon{
		cfg: Config{
			RepoDir: ".",
			Logger:  slog.Default(),
		},
		tmux: tmux.NewClient(tmuxRunner, slog.Default()),
	}

	resp, err := d.handleSessionStartDirect(ctx, protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-start-foreign-prefixed-live",
		Kind:            protocol.EnvelopeKindCommand,
		Command:         daemonhandlers.CommandSessionStart,
		Meta:            protocol.Metadata{ProjectID: naming.ProjectID(projectID)},
		Body: marshalJSON(map[string]any{
			"project_id": projectID,
			"session_id": issueID,
		}),
	})
	if err != nil {
		t.Fatalf("handleSessionStartDirect returned error: %v", err)
	}
	if resp.OK || resp.Error == nil || resp.Error.Code != protocol.ErrorCodeConflict {
		t.Fatalf("session start response = %+v, want conflict", resp)
	}
	if tmuxRunner.sessions[naming.CanonicalSessionID(projectID, issueID)] {
		t.Fatalf("canonical session %q was created despite foreign-prefixed live session", naming.CanonicalSessionID(projectID, issueID))
	}
}

func TestSessionStartReportsFailedStartCleanupFailures(t *testing.T) {
	tests := []struct {
		name              string
		startWork         bool
		newSessionErr     error
		sendKeysErr       error
		sendKeysErrOnCall int
		wantPrimary       string
	}{
		{
			name:          "tmux create failure",
			startWork:     false,
			newSessionErr: errors.New("tmux create failed"),
			wantPrimary:   "tmux create failed",
		},
		{
			name:              "env export failure",
			startWork:         false,
			sendKeysErr:       errors.New("env export failed"),
			sendKeysErrOnCall: 1,
			wantPrimary:       "export session context env",
		},
		{
			name:              "issue resource env export failure",
			startWork:         false,
			sendKeysErr:       errors.New("env export failed"),
			sendKeysErrOnCall: 2,
			wantPrimary:       "export issue resource env",
		},
		{
			name:          "launch failure",
			startWork:     true,
			newSessionErr: errors.New("launch failed"),
			wantPrimary:   "launch failed",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			repoDir := t.TempDir()
			projectID := "proj"
			if err := os.MkdirAll(filepath.Join(repoDir, ".azedarach"), 0o755); err != nil {
				t.Fatalf("mkdir .azedarach: %v", err)
			}
			issuesClient := newMigratedIssueClient(t, repoDir, slog.Default())
			t.Cleanup(func() {
				_ = issuesClient.CloseDB()
			})
			issueID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
				Title: "Report cleanup failure",
				Type:  domain.TypeTask,
			})
			if err != nil {
				t.Fatalf("create issue: %v", err)
			}

			worktreeRunner := &worktreeCreateRunner{
				worktreePath: filepath.Join(filepath.Dir(repoDir), filepath.Base(repoDir)+"-"+issueID),
				branchName:   "testuser/" + issueID + "/cleanup-report",
			}
			tmuxRunner := newSessionStartTmuxRunner()
			tmuxRunner.newSessionErr = tt.newSessionErr
			tmuxRunner.sendKeysErr = tt.sendKeysErr
			tmuxRunner.sendKeysErrOnCall = tt.sendKeysErrOnCall
			store := daemonstate.NewStore()
			d := &Daemon{
				cfg: Config{
					RepoDir:      repoDir,
					BaseBranch:   "main",
					CLITool:      "codex",
					SessionShell: "sh",
					IssueResources: appconfig.IssueResourcesConfig{
						FailedStartCleanupCommands: []string{"printf cleanup; exit 7"},
					},
					Logger: slog.Default(),
				},
				tmux:         tmux.NewClient(tmuxRunner, slog.Default()),
				issues:       issuesClient,
				session:      daemonhandlers.NewSessionHandler(store),
				sessionStore: store,
				revision:     map[string]uint64{},
				worktreeManagersByRoot: map[string]*git.WorktreeManager{
					repoDir: git.NewWorktreeManager(worktreeRunner, repoDir, slog.Default()),
				},
				worktreeManagersByProject: map[string]*git.WorktreeManager{
					projectID: git.NewWorktreeManager(worktreeRunner, repoDir, slog.Default()),
				},
			}
			resp, err := d.handleSessionStartDirect(ctx, protocol.RequestEnvelope{
				ProtocolVersion: protocol.CurrentVersion,
				RequestID:       "req-start-cleanup-report",
				Kind:            protocol.EnvelopeKindCommand,
				Command:         "session.start",
				Meta:            protocol.Metadata{ProjectID: naming.ProjectID(projectID)},
				Body: marshalJSON(map[string]any{
					"project_id": projectID,
					"session_id": issueID,
					"start_work": tt.startWork,
				}),
			})
			if err != nil {
				t.Fatalf("handleSessionStartDirect returned error: %v", err)
			}
			if resp.OK || resp.Error == nil {
				t.Fatalf("session start response = %+v, want error", resp)
			}
			for _, want := range []string{tt.wantPrimary, "failed-start cleanup also failed", "printf cleanup; exit 7", "cleanup"} {
				if !strings.Contains(resp.Error.Message, want) {
					t.Fatalf("error message = %q, want %q", resp.Error.Message, want)
				}
			}
			if tmuxRunner.sessions[naming.CanonicalSessionID(projectID, issueID)] {
				t.Fatalf("tmux session %q left running after failed start", naming.CanonicalSessionID(projectID, issueID))
			}
			task, loadErr := issuesClient.GetWithRuntime(ctx, projectID, issueID)
			if loadErr != nil {
				t.Fatal(loadErr)
			}
			if task.Status != domain.StatusOpen {
				t.Fatalf("issue status after failed start = %s, want %s", task.Status, domain.StatusOpen)
			}
		})
	}
}

func TestSessionStartContinuesWhenFreshnessTimesOut(t *testing.T) {
	ctx := context.Background()
	repoDir := t.TempDir()
	projectID := "proj"
	if err := os.MkdirAll(filepath.Join(repoDir, ".azedarach"), 0o755); err != nil {
		t.Fatalf("mkdir .azedarach: %v", err)
	}

	issuesClient := newMigratedIssueClient(t, repoDir, slog.Default())
	t.Cleanup(func() {
		_ = issuesClient.CloseDB()
	})
	issueID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title: "Start should continue after reconcile timeout",
		Type:  domain.TypeTask,
	})
	if err != nil {
		t.Fatalf("create issue: %v", err)
	}

	worktreeRunner := &worktreeCreateRunner{
		worktreePath: filepath.Join(filepath.Dir(repoDir), filepath.Base(repoDir)+"-"+issueID),
		branchName:   "testuser/" + issueID + "/freshness-timeout-continue",
	}
	tmuxRunner := newSessionStartTmuxRunner()
	store := daemonstate.NewStore()
	recorder := &timeoutRuntimeReconciler{}

	d := &Daemon{
		cfg: Config{
			RepoDir:                 repoDir,
			BaseBranch:              "main",
			CLITool:                 "codex",
			SessionShell:            "zsh",
			Logger:                  slog.Default(),
			RuntimeReconcileTimeout: 20 * time.Millisecond,
		},
		tmux:              tmux.NewClient(tmuxRunner, slog.Default()),
		issues:            issuesClient,
		session:           daemonhandlers.NewSessionHandler(store),
		sessionStore:      store,
		runtimeReconciler: recorder,
		revision:          map[string]uint64{},
		worktreeManagersByRoot: map[string]*git.WorktreeManager{
			repoDir: git.NewWorktreeManager(worktreeRunner, repoDir, slog.Default()),
		},
		worktreeManagersByProject: map[string]*git.WorktreeManager{
			projectID: git.NewWorktreeManager(worktreeRunner, repoDir, slog.Default()),
		},
	}
	t.Cleanup(func() {
		if d.runtimeReconcileQueue != nil {
			_ = d.runtimeReconcileQueue.Close()
		}
	})

	acknowledgeManagedAgentOnInitialLaunch(t, d, tmuxRunner, projectID)
	req := protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-start-timeout-continue",
		Kind:            protocol.EnvelopeKindCommand,
		Command:         "session.start",
		Meta: protocol.Metadata{
			ProjectID: naming.ProjectID(projectID),
		},
		Body: marshalJSON(map[string]string{
			"project_id": projectID,
			"session_id": issueID,
		}),
	}

	resp, err := d.handleSessionStartDirect(ctx, req)
	if err != nil {
		t.Fatalf("handleSessionStartDirect returned error: %v", err)
	}
	if !resp.OK {
		t.Fatalf("session start response not OK: %+v", resp)
	}

	sessionID := naming.CanonicalSessionID(projectID, issueID)
	if !tmuxRunner.sessions[sessionID] {
		t.Fatalf("expected tmux session %q to be created", sessionID)
	}

	calls, projectIDs := recorder.snapshot()
	if calls < 1 {
		t.Fatalf("runtime reconcile calls = %d, want at least 1", calls)
	}
	for _, id := range projectIDs {
		if id != projectID {
			t.Fatalf("runtime reconcile project ids = %v, want only %s", projectIDs, projectID)
		}
	}
	issueCalls := recorder.issueSnapshot()
	if len(issueCalls) != 1 || len(issueCalls[0]) != 1 || issueCalls[0][0] != issueID {
		t.Fatalf("runtime reconcile issue ids = %v, want [[%s]]", issueCalls, issueID)
	}
}

func waitForDirectoryOrCompletion[T any](t *testing.T, path string, done <-chan T) {
	t.Helper()
	deadline := time.After(2 * time.Second)
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		if info, err := os.Stat(path); err == nil && info.IsDir() {
			return
		}
		select {
		case result := <-done:
			t.Fatalf("operation completed before directory %s existed: %+v", path, result)
		case <-deadline:
			t.Fatalf("timed out waiting for directory %s", path)
		case <-ticker.C:
		}
	}
}

func TestSessionStartWaitsForInitReadyMarkerBeforeCompleting(t *testing.T) {
	ctx := context.Background()
	repoDir := t.TempDir()
	projectID := "proj"
	if err := os.MkdirAll(filepath.Join(repoDir, ".azedarach"), 0o755); err != nil {
		t.Fatalf("mkdir .azedarach: %v", err)
	}

	issuesClient := newMigratedIssueClient(t, repoDir, slog.Default())
	t.Cleanup(func() {
		_ = issuesClient.CloseDB()
	})
	issueID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title: "Start waits for init marker",
		Type:  domain.TypeTask,
	})
	if err != nil {
		t.Fatalf("create issue: %v", err)
	}

	worktreePath := filepath.Join(filepath.Dir(repoDir), filepath.Base(repoDir)+"-"+issueID)
	worktreeRunner := &initFailureCleanupWorktreeRunner{
		repoDir:      repoDir,
		worktreePath: worktreePath,
		branchName:   "testuser/" + issueID + "/init-marker",
	}
	tmuxRunner := newSessionStartTmuxRunner()
	store := daemonstate.NewStore()
	d := &Daemon{
		cfg: Config{
			RepoDir:                 repoDir,
			BaseBranch:              "main",
			CLITool:                 "codex",
			SessionShell:            "zsh",
			SessionSyncInitCommands: []string{"direnv allow"},
			Logger:                  slog.Default(),
		},
		tmux:         tmux.NewClient(tmuxRunner, slog.Default()),
		issues:       issuesClient,
		session:      daemonhandlers.NewSessionHandler(store),
		sessionStore: store,
		revision:     map[string]uint64{},
		worktreeManagersByRoot: map[string]*git.WorktreeManager{
			repoDir: git.NewWorktreeManager(worktreeRunner, repoDir, slog.Default()),
		},
		worktreeManagersByProject: map[string]*git.WorktreeManager{
			projectID: git.NewWorktreeManager(worktreeRunner, repoDir, slog.Default()),
		},
	}

	acknowledgeManagedAgentOnInitialLaunch(t, d, tmuxRunner, projectID)
	type startResult struct {
		resp protocol.ResponseEnvelope
		err  error
	}
	done := make(chan startResult, 1)
	go func() {
		resp, err := d.handleSessionStartDirect(ctx, protocol.RequestEnvelope{
			ProtocolVersion: protocol.CurrentVersion,
			RequestID:       "req-start-init-marker",
			Kind:            protocol.EnvelopeKindCommand,
			Command:         "session.start",
			Meta:            protocol.Metadata{ProjectID: naming.ProjectID(projectID)},
			Body: marshalJSON(map[string]string{
				"project_id": projectID,
				"session_id": issueID,
			}),
		})
		done <- startResult{resp: resp, err: err}
	}()

	sessionID := naming.CanonicalSessionID(projectID, issueID)
	markerPath := filepath.Join(worktreePath, sessionInitReadyMarkerPath(issueID, sessionID))
	waitForDirectoryOrCompletion(t, filepath.Dir(markerPath), done)

	select {
	case result := <-done:
		t.Fatalf("session start completed before init marker existed: resp=%+v err=%v", result.resp, result.err)
	case <-time.After(50 * time.Millisecond):
	}

	if err := os.WriteFile(markerPath, []byte("ready"), 0o644); err != nil {
		t.Fatalf("write marker: %v", err)
	}

	var result startResult
	select {
	case result = <-done:
	case <-time.After(time.Second):
		t.Fatal("session start did not complete after init marker was written")
	}
	if result.err != nil {
		t.Fatalf("handleSessionStartDirect returned error: %v", result.err)
	}
	if !result.resp.OK {
		t.Fatalf("session start response not OK: %+v", result.resp)
	}
	var payload struct {
		Output string `json:"output"`
	}
	if err := json.Unmarshal(result.resp.Body, &payload); err != nil {
		t.Fatalf("unmarshal response body: %v", err)
	}
	if !strings.Contains(payload.Output, "Session init commands finished: 1 command(s)") {
		t.Fatalf("output = %q, want init command completion line", payload.Output)
	}
	launchScript := requireNewSessionLaunchScript(t, tmuxRunner, sessionID)
	if !strings.Contains(launchScript, filepath.ToSlash(sessionInitReadyMarkerPath(issueID, sessionID))) {
		t.Fatalf("launch script = %q, want init marker path", launchScript)
	}
}

func TestSessionStartWithStartWorkFalseSkipsLaunchCommand(t *testing.T) {
	ctx := context.Background()
	repoDir := t.TempDir()
	projectID := "proj"
	if err := os.MkdirAll(filepath.Join(repoDir, ".azedarach"), 0o755); err != nil {
		t.Fatalf("mkdir .azedarach: %v", err)
	}

	issuesClient := newMigratedIssueClient(t, repoDir, slog.Default())
	t.Cleanup(func() {
		_ = issuesClient.CloseDB()
	})
	issueID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title: "Start tmux only",
		Type:  domain.TypeTask,
	})
	if err != nil {
		t.Fatalf("create issue: %v", err)
	}

	branch := "testuser/" + issueID + "/start-tmux-only"
	worktreePath := filepath.Join(filepath.Dir(repoDir), filepath.Base(repoDir)+"-"+issueID)
	worktreeRunner := &worktreeCreateRunner{
		worktreePath: worktreePath,
		branchName:   branch,
	}
	tmuxRunner := newSessionStartTmuxRunner()
	store := daemonstate.NewStore()
	runtimeStateStore := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(t.TempDir(), "runtime.db"), slog.Default())
	t.Cleanup(func() { _ = runtimeStateStore.Close() })

	d := &Daemon{
		cfg: Config{
			RepoDir:      repoDir,
			BaseBranch:   "main",
			CLITool:      "codex",
			SessionShell: "zsh",
			Logger:       slog.Default(),
		},
		tmux:         tmux.NewClient(tmuxRunner, slog.Default()),
		issues:       issuesClient,
		session:      daemonhandlers.NewSessionHandler(store),
		sessionStore: store,
		revision:     map[string]uint64{},
		runtimeStoresByProject: map[string]*daemonstate.RuntimeStateStore{
			projectID: runtimeStateStore,
		},
		worktreeManagersByRoot: map[string]*git.WorktreeManager{
			repoDir: git.NewWorktreeManager(worktreeRunner, repoDir, slog.Default()),
		},
		worktreeManagersByProject: map[string]*git.WorktreeManager{
			projectID: git.NewWorktreeManager(worktreeRunner, repoDir, slog.Default()),
		},
	}

	req := protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-start-tmux-only",
		Kind:            protocol.EnvelopeKindCommand,
		Command:         "session.start",
		Meta: protocol.Metadata{
			ProjectID: naming.ProjectID(projectID),
		},
		Body: marshalJSON(map[string]any{
			"project_id": projectID,
			"session_id": issueID,
			"start_work": false,
		}),
	}

	resp, err := d.handleSessionStartDirect(ctx, req)
	if err != nil {
		t.Fatalf("handleSessionStartDirect returned error: %v", err)
	}
	if !resp.OK {
		t.Fatalf("session start response not OK: %+v", resp)
	}
	if tmuxRunner.sendKeysCalls != 1 {
		t.Fatalf("send-keys calls = %d, want context export only when start_work=false", tmuxRunner.sendKeysCalls)
	}
	contextExport := tmuxRunner.sendKeysPayloads[0]
	for _, want := range []string{
		"export ",
		"AZEDARACH_PROJECT_ID='" + projectID + "'",
		"AZEDARACH_ISSUE_ID='" + issueID + "'",
		"AZEDARACH_SESSION_ID='" + naming.CanonicalSessionID(d.sessionNamingScope(projectID), issueID) + "'",
	} {
		if !strings.Contains(contextExport, want) {
			t.Fatalf("context export = %q, want %q", contextExport, want)
		}
	}
	if strings.Contains(contextExport, " codex") {
		t.Fatalf("context export = %q, start_work=false must not launch codex", contextExport)
	}

	var payload struct {
		Output string `json:"output"`
	}
	if err := json.Unmarshal(resp.Body, &payload); err != nil {
		t.Fatalf("unmarshal response body: %v", err)
	}
	if !strings.Contains(payload.Output, "Skipping AI launch (tmux session only)") {
		t.Fatalf("output = %q, want tmux-only launch line", payload.Output)
	}
	sessionID := naming.CanonicalSessionID(d.sessionNamingScope(projectID), issueID)
	session, found, err := runtimeStateStore.GetSessionState(ctx, projectID, sessionID)
	if err != nil {
		t.Fatalf("GetSessionState: %v", err)
	}
	if !found {
		t.Fatalf("missing runtime session projection %q", sessionID)
	}
	if session.Activity != "no-agent" || session.ActivitySource != "session" {
		t.Fatalf("session activity = %s/%s, want no-agent/session", session.Activity, session.ActivitySource)
	}
}

func TestSessionStartDefaultsAgentActivityToBusy(t *testing.T) {
	ctx := context.Background()
	repoDir := t.TempDir()
	projectID := "proj-agent-start"
	if err := os.MkdirAll(filepath.Join(repoDir, ".azedarach"), 0o755); err != nil {
		t.Fatalf("mkdir .azedarach: %v", err)
	}

	issuesClient := newMigratedIssueClient(t, repoDir, slog.Default())
	t.Cleanup(func() { _ = issuesClient.CloseDB() })
	issueID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title: "Start AI worker",
		Type:  domain.TypeTask,
	})
	if err != nil {
		t.Fatalf("create issue: %v", err)
	}

	branch := "testuser/" + issueID + "/start-ai-worker"
	worktreePath := filepath.Join(filepath.Dir(repoDir), filepath.Base(repoDir)+"-"+issueID)
	worktreeRunner := &worktreeCreateRunner{
		worktreePath: worktreePath,
		branchName:   branch,
	}
	tmuxRunner := newSessionStartTmuxRunner()
	tmuxRunner.maxNewSessionCommand = 12 * 1024
	store := daemonstate.NewStore()
	runtimeStateStore := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(t.TempDir(), "runtime.db"), slog.Default())
	t.Cleanup(func() { _ = runtimeStateStore.Close() })

	d := &Daemon{
		cfg: Config{
			RepoDir:      repoDir,
			BaseBranch:   "main",
			CLITool:      "codex",
			SessionShell: "zsh",
			Logger:       slog.Default(),
		},
		tmux:         tmux.NewClient(tmuxRunner, slog.Default()),
		issues:       issuesClient,
		session:      daemonhandlers.NewSessionHandler(store),
		sessionStore: store,
		revision:     map[string]uint64{},
		runtimeStoresByProject: map[string]*daemonstate.RuntimeStateStore{
			projectID: runtimeStateStore,
		},
		worktreeManagersByRoot: map[string]*git.WorktreeManager{
			repoDir: git.NewWorktreeManager(worktreeRunner, repoDir, slog.Default()),
		},
		worktreeManagersByProject: map[string]*git.WorktreeManager{
			projectID: git.NewWorktreeManager(worktreeRunner, repoDir, slog.Default()),
		},
	}

	acknowledgeManagedAgentOnInitialLaunch(t, d, tmuxRunner, projectID)
	resp, err := d.handleSessionStartDirect(ctx, protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-start-ai-worker",
		Kind:            protocol.EnvelopeKindCommand,
		Command:         "session.start",
		Meta: protocol.Metadata{
			ProjectID: naming.ProjectID(projectID),
		},
		Body: marshalJSON(map[string]any{
			"project_id":     projectID,
			"session_id":     issueID,
			"initial_prompt": strings.Repeat("context-rich worker prompt\n", 100),
		}),
	})
	if err != nil {
		t.Fatalf("handleSessionStartDirect returned error: %v", err)
	}
	if !resp.OK {
		t.Fatalf("session start response not OK: %+v", resp)
	}
	sessionID := naming.CanonicalSessionID(d.sessionNamingScope(projectID), issueID)
	launchCommand := requireNewSessionLaunchCommand(t, tmuxRunner, sessionID)
	launchScript := requireNewSessionLaunchScript(t, tmuxRunner, sessionID)
	if len(launchCommand) > tmuxRunner.maxNewSessionCommand {
		t.Fatalf("launch command length = %d, want <= %d", len(launchCommand), tmuxRunner.maxNewSessionCommand)
	}
	if strings.Contains(launchCommand, "context-rich worker prompt") || strings.Contains(launchCommand, initialPromptShellVariable) {
		t.Fatalf("launch command contains initial prompt payload: %q", launchCommand)
	}
	if !strings.Contains(launchScript, `AZEDARACH_ISSUE_ID="`+issueID+`" codex`) || !strings.Contains(launchScript, initialPromptShellVariable) || strings.Contains(launchScript, "context-rich worker prompt") {
		t.Fatalf("launch script = %q, want bounded file-bootstrap codex launch", launchScript)
	}
	if path := tmuxRunner.launchScriptPaths[sessionID]; path == "" {
		t.Fatal("missing transient launch script path")
	} else if _, statErr := os.Stat(path); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("transient launch script still exists after start: %v", statErr)
	}
	if mode := tmuxRunner.launchScriptModes[sessionID].Perm(); mode != 0o600 {
		t.Fatalf("transient launch script mode = %o, want 600", mode)
	}
	if got, want := tmuxRunner.launchPromptContents[sessionID], strings.TrimSpace(strings.Repeat("context-rich worker prompt\n", 100)); got != want {
		t.Fatalf("queued launch prompt = %q, want exact prompt %q", got, want)
	}
	if mode := tmuxRunner.launchPromptModes[sessionID].Perm(); mode != 0o600 {
		t.Fatalf("transient prompt mode = %o, want 600", mode)
	}
	if mode := tmuxRunner.launchArtifactModes[sessionID].Perm(); mode != 0o700 {
		t.Fatalf("launch artifact dir mode = %o, want 700", mode)
	}

	session, found, err := runtimeStateStore.GetSessionState(ctx, projectID, sessionID)
	if err != nil {
		t.Fatalf("GetSessionState: %v", err)
	}
	if !found {
		t.Fatalf("missing runtime session projection %q", sessionID)
	}
	if session.Activity != "busy" || session.ActivitySource != "session" {
		t.Fatalf("session activity = %s/%s, want busy/session", session.Activity, session.ActivitySource)
	}
}

func TestSessionStartInjectsIssueImageAttachments(t *testing.T) {
	ctx := context.Background()
	repoDir := t.TempDir()
	projectID, err := appconfig.ProjectIDForRoot(repoDir)
	if err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(repoDir, ".azedarach", "azedarach.db")
	t.Setenv("AZEDARACH_DB_PATH", dbPath)
	if err := os.MkdirAll(filepath.Join(repoDir, ".azedarach"), 0o755); err != nil {
		t.Fatalf("mkdir .azedarach: %v", err)
	}

	issuesClient := newMigratedIssueClient(t, repoDir, slog.Default())
	t.Cleanup(func() { _ = issuesClient.CloseDB() })
	issueID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title: "Use screenshot",
		Type:  domain.TypeTask,
	})
	if err != nil {
		t.Fatalf("create issue: %v", err)
	}

	sourceImage := filepath.Join(t.TempDir(), "screen shot.png")
	if err := os.WriteFile(sourceImage, []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a}, 0o644); err != nil {
		t.Fatalf("write source image: %v", err)
	}
	attached, err := attachment.NewService(filepath.Join(repoDir, ".azedarach"), slog.Default()).Attach(ctx, issueID, sourceImage)
	if err != nil {
		t.Fatalf("attach image: %v", err)
	}

	branch := "testuser/" + issueID + "/use-screenshot"
	worktreePath := filepath.Join(filepath.Dir(repoDir), filepath.Base(repoDir)+"-"+issueID)
	worktreeRunner := &worktreeCreateRunner{
		worktreePath: worktreePath,
		branchName:   branch,
	}
	tmuxRunner := newSessionStartTmuxRunner()
	store := daemonstate.NewStore()
	runtimeStateStore := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(t.TempDir(), "runtime.db"), slog.Default())
	t.Cleanup(func() { _ = runtimeStateStore.Close() })

	d := &Daemon{
		cfg: Config{
			RepoDir:      repoDir,
			BaseBranch:   "main",
			CLITool:      "codex",
			SessionShell: "zsh",
			Logger:       slog.Default(),
		},
		tmux:         tmux.NewClient(tmuxRunner, slog.Default()),
		issues:       issuesClient,
		session:      daemonhandlers.NewSessionHandler(store),
		sessionStore: store,
		revision:     map[string]uint64{},
		runtimeStoresByProject: map[string]*daemonstate.RuntimeStateStore{
			projectID: runtimeStateStore,
		},
		worktreeManagersByRoot: map[string]*git.WorktreeManager{
			repoDir: git.NewWorktreeManager(worktreeRunner, repoDir, slog.Default()),
		},
		worktreeManagersByProject: map[string]*git.WorktreeManager{
			projectID: git.NewWorktreeManager(worktreeRunner, repoDir, slog.Default()),
		},
	}

	acknowledgeManagedAgentOnInitialLaunch(t, d, tmuxRunner, projectID)
	resp, err := d.handleSessionStartDirect(ctx, protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-start-image-worker",
		Kind:            protocol.EnvelopeKindCommand,
		Command:         "session.start",
		Meta: protocol.Metadata{
			ProjectID: naming.ProjectID(projectID),
		},
		Body: marshalJSON(map[string]any{
			"project_id":  projectID,
			"session_id":  issueID,
			"image_paths": []string{attached.Path},
		}),
	})
	if err != nil {
		t.Fatalf("handleSessionStartDirect returned error: %v", err)
	}
	if !resp.OK {
		t.Fatalf("session start response not OK: %+v", resp)
	}

	worktreeImagePath := filepath.Join(worktreePath, filepath.FromSlash(attached.Relative))
	if _, err := os.Stat(worktreeImagePath); err != nil {
		t.Fatalf("expected worktree-local image attachment at %s: %v", worktreeImagePath, err)
	}
	launchScript := requireNewSessionLaunchScript(t, tmuxRunner, naming.CanonicalSessionID(d.sessionNamingScope(projectID), issueID))
	if !strings.Contains(launchScript, `--image "`+worktreeImagePath+`"`) {
		t.Fatalf("launch script = %q, want worktree-local codex image argument %q", launchScript, worktreeImagePath)
	}
	if strings.Contains(launchScript, attached.Path) {
		t.Fatalf("launch script = %q, must not use shared attachment store path %q", launchScript, attached.Path)
	}
	if _, err := os.Stat(filepath.Join(worktreePath, ".azedarach", "azedarach.db")); !os.IsNotExist(err) {
		t.Fatalf("session attachment read created worktree-local database: %v", err)
	}
	if !strings.Contains(launchScript, `codex `+codexFloopFailOpenConfigExpansion+` --image "`) || !strings.Contains(launchScript, initialPromptShellVariable) {
		t.Fatalf("launch script = %q, want image args with bounded file bootstrap", launchScript)
	}
}

func TestSessionStartLargeCodexPromptUsesFileBootstrap(t *testing.T) {
	t.Setenv("AZEDARACH_DAEMON_SCOPE", "worktree")
	ctx := context.Background()
	repoDir := t.TempDir()
	projectID := "proj-large-codex-prompt"
	if err := os.MkdirAll(filepath.Join(repoDir, ".azedarach"), 0o755); err != nil {
		t.Fatalf("mkdir .azedarach: %v", err)
	}

	issuesClient := newMigratedIssueClient(t, repoDir, slog.Default())
	t.Cleanup(func() { _ = issuesClient.CloseDB() })
	issueID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title: "Start AI worker with large prompt",
		Type:  domain.TypeTask,
	})
	if err != nil {
		t.Fatalf("create issue: %v", err)
	}

	branch := "testuser/" + issueID + "/start-ai-worker-with-large-prompt"
	worktreePath := filepath.Join(filepath.Dir(repoDir), filepath.Base(repoDir)+"-"+issueID)
	worktreeRunner := &worktreeCreateRunner{
		worktreePath: worktreePath,
		branchName:   branch,
	}
	tmuxRunner := newSessionStartTmuxRunner()
	store := daemonstate.NewStore()

	d := &Daemon{
		cfg: Config{
			RepoDir:        repoDir,
			BaseBranch:     "main",
			CLITool:        "codex",
			CodexAppServer: true,
			SessionShell:   "zsh",
			Logger:         slog.Default(),
			ScopedRuntime:  true,
		},
		tmux:         tmux.NewClient(tmuxRunner, slog.Default()),
		issues:       issuesClient,
		session:      daemonhandlers.NewSessionHandler(store),
		sessionStore: store,
		revision:     map[string]uint64{},
		worktreeManagersByRoot: map[string]*git.WorktreeManager{
			repoDir: git.NewWorktreeManager(worktreeRunner, repoDir, slog.Default()),
		},
		worktreeManagersByProject: map[string]*git.WorktreeManager{
			projectID: git.NewWorktreeManager(worktreeRunner, repoDir, slog.Default()),
		},
	}

	largePrompt := strings.Repeat("large prompt line\n", 32*1024)
	acknowledgeManagedAgentOnInitialLaunch(t, d, tmuxRunner, projectID)
	resp, err := d.handleSessionStartDirect(ctx, protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-start-large-codex-prompt",
		Kind:            protocol.EnvelopeKindCommand,
		Command:         "session.start",
		Meta: protocol.Metadata{
			ProjectID: naming.ProjectID(projectID),
		},
		Body: marshalJSON(map[string]any{
			"project_id":     projectID,
			"session_id":     issueID,
			"initial_prompt": largePrompt,
		}),
	})
	if err != nil {
		t.Fatalf("handleSessionStartDirect returned error: %v", err)
	}
	if !resp.OK {
		t.Fatalf("session start response not OK: %+v", resp)
	}

	sessionID := naming.CanonicalSessionID(d.sessionNamingScope(projectID), issueID)
	launchCommand := requireNewSessionLaunchCommand(t, tmuxRunner, sessionID)
	var launchArgs []string
	for _, command := range tmuxRunner.commands {
		if len(command) > 0 && command[0] == "new-session" {
			launchArgs = command
			break
		}
	}
	if got, ok := tmuxCommandEnvironmentValue(launchArgs, "AZEDARACH_DAEMON_SCOPE"); !ok || got != "worktree" {
		t.Fatalf("active tmux launch scope = %q, present=%t; command=%v", got, ok, launchArgs)
	}
	if strings.Contains(launchCommand, "large prompt line") || strings.Contains(launchCommand, initialPromptShellVariable) {
		t.Fatalf("launch command contains large prompt payload or prompt variable")
	}
	launchScript := requireNewSessionLaunchScript(t, tmuxRunner, sessionID)
	if strings.Contains(launchScript, shellPrintfPercentBBytes("large prompt line\n")) || !strings.Contains(launchScript, initialPromptShellVariable) {
		t.Fatal("launch script contains the encoded large prompt")
	}
	if got := tmuxRunner.launchPromptContents[sessionID]; got != strings.TrimSpace(largePrompt) {
		t.Fatalf("queued launch prompt bytes = %d, want %d", len(got), len(strings.TrimSpace(largePrompt)))
	}
}

func TestSessionStartCodexPromptWithLargeEncodedLaunchUsesFileBootstrap(t *testing.T) {
	ctx := context.Background()
	repoDir := t.TempDir()
	projectID := "proj-large-encoded-codex-prompt"
	if err := os.MkdirAll(filepath.Join(repoDir, ".azedarach"), 0o755); err != nil {
		t.Fatalf("mkdir .azedarach: %v", err)
	}

	issuesClient := newMigratedIssueClient(t, repoDir, slog.Default())
	t.Cleanup(func() { _ = issuesClient.CloseDB() })
	issueID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title: "Start AI worker with encoded prompt near tmux limit",
		Type:  domain.TypeTask,
	})
	if err != nil {
		t.Fatalf("create issue: %v", err)
	}

	branch := "testuser/" + issueID + "/start-ai-worker-encoded-prompt"
	worktreePath := filepath.Join(filepath.Dir(repoDir), filepath.Base(repoDir)+"-"+issueID)
	worktreeRunner := &worktreeCreateRunner{
		worktreePath: worktreePath,
		branchName:   branch,
	}
	tmuxRunner := newSessionStartTmuxRunner()
	tmuxRunner.maxNewSessionCommand = codexLaunchCommandArgMaxBytes
	store := daemonstate.NewStore()

	d := &Daemon{
		cfg: Config{
			RepoDir:      repoDir,
			BaseBranch:   "main",
			CLITool:      "codex",
			SessionShell: "zsh",
			Logger:       slog.Default(),
		},
		tmux:         tmux.NewClient(tmuxRunner, slog.Default()),
		issues:       issuesClient,
		session:      daemonhandlers.NewSessionHandler(store),
		sessionStore: store,
		revision:     map[string]uint64{},
		worktreeManagersByRoot: map[string]*git.WorktreeManager{
			repoDir: git.NewWorktreeManager(worktreeRunner, repoDir, slog.Default()),
		},
		worktreeManagersByProject: map[string]*git.WorktreeManager{
			projectID: git.NewWorktreeManager(worktreeRunner, repoDir, slog.Default()),
		},
	}
	acknowledgeManagedAgentOnInitialLaunch(t, d, tmuxRunner, projectID)

	promptLine := "Codex orchestration bootstrap prompt.\n"
	initialPrompt := strings.Repeat(promptLine, 180)
	if len(initialPrompt) >= codexLaunchPromptArgMaxBytes {
		t.Fatalf("test prompt length = %d, want below raw prompt arg limit %d", len(initialPrompt), codexLaunchPromptArgMaxBytes)
	}
	encodedLaunchCommand := d.buildSessionLaunchCommand(projectID, issueID, naming.CanonicalSessionID(d.sessionNamingScope(projectID), issueID), false, nil, initialPrompt)
	if len(encodedLaunchCommand) <= codexLaunchCommandArgMaxBytes {
		t.Fatalf("encoded launch command length = %d, want above tmux-safe bound %d", len(encodedLaunchCommand), codexLaunchCommandArgMaxBytes)
	}

	resp, err := d.handleSessionStartDirect(ctx, protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-start-large-encoded-codex-prompt",
		Kind:            protocol.EnvelopeKindCommand,
		Command:         "session.start",
		Meta: protocol.Metadata{
			ProjectID: naming.ProjectID(projectID),
		},
		Body: marshalJSON(map[string]any{
			"project_id":     projectID,
			"session_id":     issueID,
			"initial_prompt": initialPrompt,
		}),
	})
	if err != nil {
		t.Fatalf("handleSessionStartDirect returned error: %v", err)
	}
	if !resp.OK {
		t.Fatalf("session start response not OK: %+v", resp)
	}

	sessionID := naming.CanonicalSessionID(d.sessionNamingScope(projectID), issueID)
	launchCommand := requireNewSessionLaunchCommand(t, tmuxRunner, sessionID)
	if len(launchCommand) > codexLaunchCommandArgMaxBytes {
		t.Fatalf("launch command length = %d, want <= %d", len(launchCommand), codexLaunchCommandArgMaxBytes)
	}
	for _, mustNotContain := range []string{promptLine, initialPromptShellVariable} {
		if strings.Contains(launchCommand, mustNotContain) {
			t.Fatalf("launch command = %q, must not contain prompt fragment %q", launchCommand, mustNotContain)
		}
	}
	launchScript := requireNewSessionLaunchScript(t, tmuxRunner, sessionID)
	if strings.Contains(launchScript, shellPrintfPercentBBytes(promptLine)) || !strings.Contains(launchScript, initialPromptShellVariable) {
		t.Fatal("launch script contains encoded orchestration prompt")
	}
	if got := tmuxRunner.launchPromptContents[sessionID]; got != strings.TrimSpace(initialPrompt) {
		t.Fatalf("queued orchestration prompt = %q, want exact prompt", got)
	}
}

func TestSessionStartUsesClosestAncestorWorktreeBranchAsBase(t *testing.T) {
	ctx := context.Background()
	repoDir := t.TempDir()
	projectID := "proj"
	if err := os.MkdirAll(filepath.Join(repoDir, ".azedarach"), 0o755); err != nil {
		t.Fatalf("mkdir .azedarach: %v", err)
	}

	issuesClient := newMigratedIssueClient(t, repoDir, slog.Default())
	t.Cleanup(func() {
		_ = issuesClient.CloseDB()
	})
	parentID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title:  "Parent issue",
		Type:   domain.TypeTask,
		Status: domain.StatusDone,
	})
	if err != nil {
		t.Fatalf("create parent issue: %v", err)
	}
	childID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title:    "Child issue",
		Type:     domain.TypeTask,
		ParentID: &parentID,
	})
	if err != nil {
		t.Fatalf("create child issue: %v", err)
	}

	parentBranch := "testuser/" + parentID + "/parent-issue"
	parentWorktree := filepath.Join(filepath.Dir(repoDir), filepath.Base(repoDir)+"-"+parentID)
	worktreeRunner := &ancestorBaseWorktreeRunner{
		repoDir:        repoDir,
		parentWorktree: parentWorktree,
		parentBranch:   parentBranch,
	}
	tmuxRunner := newSessionStartTmuxRunner()
	store := daemonstate.NewStore()

	d := &Daemon{
		cfg: Config{
			RepoDir:      repoDir,
			BaseBranch:   "main",
			CLITool:      "codex",
			SessionShell: "zsh",
			Logger:       slog.Default(),
		},
		tmux:         tmux.NewClient(tmuxRunner, slog.Default()),
		issues:       issuesClient,
		session:      daemonhandlers.NewSessionHandler(store),
		sessionStore: store,
		revision:     map[string]uint64{},
		worktreeManagersByRoot: map[string]*git.WorktreeManager{
			repoDir: git.NewWorktreeManager(worktreeRunner, repoDir, slog.Default()),
		},
		worktreeManagersByProject: map[string]*git.WorktreeManager{
			projectID: git.NewWorktreeManager(worktreeRunner, repoDir, slog.Default()),
		},
	}

	acknowledgeManagedAgentOnInitialLaunch(t, d, tmuxRunner, projectID)
	req := protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-start-ancestor-base",
		Kind:            protocol.EnvelopeKindCommand,
		Command:         "session.start",
		Meta: protocol.Metadata{
			ProjectID: naming.ProjectID(projectID),
		},
		Body: marshalJSON(map[string]string{
			"project_id": projectID,
			"session_id": childID,
		}),
	}

	resp, err := d.handleSessionStartDirect(ctx, req)
	if err != nil {
		t.Fatalf("handleSessionStartDirect returned error: %v", err)
	}
	if !resp.OK {
		t.Fatalf("session start response not OK: %+v", resp)
	}
	if got, want := worktreeRunner.createBaseBranch, parentBranch; got != want {
		t.Fatalf("worktree create base branch = %q, want %q", got, want)
	}
}

func TestSessionStartMaterializesMissingParentWorktreeBranchAsBase(t *testing.T) {
	ctx := context.Background()
	repoDir := filepath.Join(t.TempDir(), "repo")
	projectID := "proj"
	if err := os.MkdirAll(filepath.Join(repoDir, ".azedarach"), 0o755); err != nil {
		t.Fatalf("mkdir .azedarach: %v", err)
	}

	issuesClient := newMigratedIssueClient(t, repoDir, slog.Default())
	t.Cleanup(func() {
		_ = issuesClient.CloseDB()
	})
	parentID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title:  "Parent issue",
		Type:   domain.TypeTask,
		Status: domain.StatusOpen,
	})
	if err != nil {
		t.Fatalf("create parent issue: %v", err)
	}
	childID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title:    "Child issue",
		Type:     domain.TypeTask,
		ParentID: &parentID,
	})
	if err != nil {
		t.Fatalf("create child issue: %v", err)
	}

	eventsFile := filepath.Join(repoDir, "worktree-init-events")
	worktreeRunner := &recordingWorktreeCreateRunner{repoDir: repoDir, eventsFile: eventsFile}
	tmuxRunner := newSessionStartTmuxRunner()
	store := daemonstate.NewStore()

	d := &Daemon{
		cfg: Config{
			RepoDir:                   repoDir,
			BaseBranch:                "main",
			CLITool:                   "codex",
			SessionShell:              "sh",
			WorktreeInitCommands:      []string{`printf 'sync:%s:parent=%s\n' "$AZEDARACH_ISSUE_ID" "$AZEDARACH_PARENT_ISSUE_ID" >> "$AZEDARACH_PROJECT_ROOT/worktree-init-events"`},
			WorktreeAsyncInitCommands: []string{`printf 'async:%s\n' "$AZEDARACH_ISSUE_ID" >> "$AZEDARACH_PROJECT_ROOT/worktree-init-events"`},
			Logger:                    slog.Default(),
		},
		tmux:         tmux.NewClient(tmuxRunner, slog.Default()),
		issues:       issuesClient,
		session:      daemonhandlers.NewSessionHandler(store),
		sessionStore: store,
		revision:     map[string]uint64{},
		worktreeManagersByRoot: map[string]*git.WorktreeManager{
			repoDir: git.NewWorktreeManager(worktreeRunner, repoDir, slog.Default()),
		},
		worktreeManagersByProject: map[string]*git.WorktreeManager{
			projectID: git.NewWorktreeManager(worktreeRunner, repoDir, slog.Default()),
		},
	}

	acknowledgeManagedAgentOnInitialLaunch(t, d, tmuxRunner, projectID)
	req := protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-start-create-parent-base",
		Kind:            protocol.EnvelopeKindCommand,
		Command:         "session.start",
		Meta:            protocol.Metadata{ProjectID: naming.ProjectID(projectID)},
		Body: marshalJSON(map[string]string{
			"project_id": projectID,
			"session_id": childID,
		}),
	}

	resp, err := d.handleSessionStartDirect(ctx, req)
	if err != nil {
		t.Fatalf("handleSessionStartDirect returned error: %v", err)
	}
	if !resp.OK {
		t.Fatalf("session start response not OK: %+v", resp)
	}
	if len(worktreeRunner.adds) != 2 {
		t.Fatalf("adds = %+v, want parent then child", worktreeRunner.adds)
	}
	if worktreeRunner.adds[0].IssueID != parentID || worktreeRunner.adds[0].Base != "main" {
		t.Fatalf("parent add = %+v, want base main", worktreeRunner.adds[0])
	}
	if worktreeRunner.adds[1].IssueID != childID || worktreeRunner.adds[1].Base != worktreeRunner.adds[0].Branch {
		t.Fatalf("child add = %+v, want base %q", worktreeRunner.adds[1], worktreeRunner.adds[0].Branch)
	}
	deadline := time.Now().Add(2 * time.Second)
	var eventLines []string
	for {
		data, err := os.ReadFile(eventsFile)
		if err == nil {
			eventLines = nonEmptyLines(string(data))
			if containsLine(eventLines, "async:"+parentID) && containsLine(eventLines, "async:"+childID) {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("events = %#v, want async init for parent and child", eventLines)
		}
		time.Sleep(20 * time.Millisecond)
	}
	wantPrefix := []string{
		"add:" + parentID + ":base=main",
		"sync:" + parentID + ":parent=",
		"add:" + childID + ":base=" + worktreeRunner.adds[0].Branch,
		"sync:" + childID + ":parent=" + parentID,
	}
	blockingLines := filterOutPrefix(eventLines, "async:")
	if len(blockingLines) < len(wantPrefix) || strings.Join(blockingLines[:len(wantPrefix)], "\n") != strings.Join(wantPrefix, "\n") {
		t.Fatalf("blocking event prefix = %#v, want %#v (all events %#v)", blockingLines[:min(len(blockingLines), len(wantPrefix))], wantPrefix, eventLines)
	}
}

func nonEmptyLines(value string) []string {
	rawLines := strings.Split(strings.TrimSpace(value), "\n")
	lines := make([]string, 0, len(rawLines))
	for _, line := range rawLines {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			lines = append(lines, trimmed)
		}
	}
	return lines
}

func containsLine(lines []string, want string) bool {
	for _, line := range lines {
		if line == want {
			return true
		}
	}
	return false
}

func filterOutPrefix(lines []string, prefix string) []string {
	filtered := make([]string, 0, len(lines))
	for _, line := range lines {
		if !strings.HasPrefix(line, prefix) {
			filtered = append(filtered, line)
		}
	}
	return filtered
}

func TestSessionStartDoesNotPersistTransitionWhenTmuxCreateFails(t *testing.T) {
	ctx := context.Background()
	repoDir := t.TempDir()
	projectID := "proj"
	if err := os.MkdirAll(filepath.Join(repoDir, ".azedarach"), 0o755); err != nil {
		t.Fatalf("mkdir .azedarach: %v", err)
	}

	issuesClient := newMigratedIssueClient(t, repoDir, slog.Default())
	t.Cleanup(func() {
		_ = issuesClient.CloseDB()
	})
	issueID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title: "Fail tmux create",
		Type:  domain.TypeTask,
	})
	if err != nil {
		t.Fatalf("create issue: %v", err)
	}

	worktreeRunner := &worktreeCreateRunner{
		worktreePath: filepath.Join(filepath.Dir(repoDir), filepath.Base(repoDir)+"-"+issueID),
		branchName:   "testuser/" + issueID + "/fail-tmux-create",
	}
	tmuxRunner := newSessionStartTmuxRunner()
	tmuxRunner.newSessionErr = errors.New("tmux new-session failed")
	store := daemonstate.NewStore()

	d := &Daemon{
		cfg: Config{
			RepoDir:      repoDir,
			BaseBranch:   "main",
			CLITool:      "codex",
			SessionShell: "zsh",
			Logger:       slog.Default(),
		},
		tmux:         tmux.NewClient(tmuxRunner, slog.Default()),
		issues:       issuesClient,
		session:      daemonhandlers.NewSessionHandler(store),
		sessionStore: store,
		revision:     map[string]uint64{},
		worktreeManagersByRoot: map[string]*git.WorktreeManager{
			repoDir: git.NewWorktreeManager(worktreeRunner, repoDir, slog.Default()),
		},
		worktreeManagersByProject: map[string]*git.WorktreeManager{
			projectID: git.NewWorktreeManager(worktreeRunner, repoDir, slog.Default()),
		},
	}

	req := protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-start-tmux-fail",
		Kind:            protocol.EnvelopeKindCommand,
		Command:         "session.start",
		Meta: protocol.Metadata{
			ProjectID: naming.ProjectID(projectID),
		},
		Body: marshalJSON(map[string]string{
			"project_id": projectID,
			"session_id": issueID,
		}),
	}

	resp, err := d.handleSessionStartDirect(ctx, req)
	if err != nil {
		t.Fatalf("handleSessionStartDirect returned error: %v", err)
	}
	if resp.OK {
		t.Fatalf("expected start response to fail")
	}
	if tmuxRunner.sendKeysCalls != 0 {
		t.Fatalf("send-keys calls = %d, want 0 on tmux create failure", tmuxRunner.sendKeysCalls)
	}

	snapshot := store.ReadSnapshot(projectID)
	if len(snapshot.Sessions) != 0 {
		t.Fatalf("session snapshot = %+v, want empty after failed start", snapshot.Sessions)
	}
	sessionID := naming.CanonicalSessionID(d.sessionNamingScope(projectID), issueID)
	failedScriptPath := tmuxRunner.launchScriptPaths[sessionID]
	if failedScriptPath == "" {
		t.Fatal("failed launch did not create a transient launch script")
	}
	if _, statErr := os.Stat(failedScriptPath); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("failed launch script still exists: %v", statErr)
	}

	tmuxRunner.newSessionErr = nil
	acknowledgeManagedAgentOnInitialLaunch(t, d, tmuxRunner, projectID)
	retryResp, retryErr := d.handleSessionStartDirect(ctx, req)
	if retryErr != nil || !retryResp.OK {
		t.Fatalf("retry after compensated launch failure: resp=%+v err=%v", retryResp, retryErr)
	}
}

func TestSessionStartCreateThenErrorCompensatesAndRetries(t *testing.T) {
	for _, startWork := range []bool{false, true} {
		t.Run(fmt.Sprintf("start_work_%t", startWork), func(t *testing.T) {
			ctx := context.Background()
			repoDir := t.TempDir()
			projectID := "proj"
			if err := os.MkdirAll(filepath.Join(repoDir, ".azedarach"), 0o755); err != nil {
				t.Fatal(err)
			}
			issuesClient := newMigratedIssueClient(t, repoDir, slog.Default())
			t.Cleanup(func() { _ = issuesClient.CloseDB() })
			issueID, err := issuesClient.Create(ctx, issues.CreateTaskParams{Title: "Create then error", Type: domain.TypeTask})
			if err != nil {
				t.Fatal(err)
			}
			worktreeRunner := &worktreeCreateRunner{
				worktreePath: filepath.Join(filepath.Dir(repoDir), filepath.Base(repoDir)+"-"+issueID),
				branchName:   "testuser/" + issueID + "/create-then-error",
			}
			tmuxRunner := newSessionStartTmuxRunner()
			tmuxRunner.newSessionErr = errors.New("tmux create returned ambiguous error")
			tmuxRunner.createBeforeNewSessionError = true
			memoryStore := daemonstate.NewStore()
			d := &Daemon{
				cfg:  Config{RepoDir: repoDir, BaseBranch: "main", CLITool: "codex", SessionShell: "zsh", Logger: slog.Default()},
				tmux: tmux.NewClient(tmuxRunner, slog.Default()), issues: issuesClient,
				session: daemonhandlers.NewSessionHandler(memoryStore), sessionStore: memoryStore, revision: map[string]uint64{},
				worktreeManagersByRoot: map[string]*git.WorktreeManager{
					repoDir: git.NewWorktreeManager(worktreeRunner, repoDir, slog.Default()),
				},
				worktreeManagersByProject: map[string]*git.WorktreeManager{
					projectID: git.NewWorktreeManager(worktreeRunner, repoDir, slog.Default()),
				},
			}
			attachIsolatedRuntimeStore(t, d, projectID)
			req := protocol.RequestEnvelope{
				ProtocolVersion: protocol.CurrentVersion, RequestID: "req-create-then-error", Kind: protocol.EnvelopeKindCommand,
				Command: daemonhandlers.CommandSessionStart, Meta: protocol.Metadata{ProjectID: naming.ProjectID(projectID)},
				Body: marshalJSON(map[string]any{"project_id": projectID, "session_id": issueID, "start_work": startWork}),
			}
			sessionID := naming.CanonicalSessionID(d.sessionNamingScope(projectID), issueID)
			failed, err := d.handleSessionStartDirect(ctx, req)
			if err != nil || failed.OK || failed.Error == nil || !strings.Contains(failed.Error.Message, "ambiguous error") {
				t.Fatalf("create-then-error response=%+v err=%v", failed, err)
			}
			if tmuxRunner.sessions[sessionID] {
				t.Fatalf("ambiguous create left live session %s", sessionID)
			}
			projection, found, err := d.sessionRuntimeStateStore(projectID).GetWorkerSessionStateByIssueID(ctx, projectID, issueID, sessionID)
			if err != nil || !found || projection.State != daemonstate.SessionStateStopped || projection.ObservedState != daemonstate.SessionStateStopped {
				t.Fatalf("compensated projection=%+v found=%t err=%v", projection, found, err)
			}

			tmuxRunner.newSessionErr = nil
			tmuxRunner.createBeforeNewSessionError = false
			if startWork {
				acknowledgeManagedAgentOnInitialLaunch(t, d, tmuxRunner, projectID)
			}
			retried, err := d.handleSessionStartDirect(ctx, req)
			if err != nil || !retried.OK || !tmuxRunner.sessions[sessionID] {
				t.Fatalf("retry response=%+v err=%v live=%t", retried, err, tmuxRunner.sessions[sessionID])
			}
		})
	}
}

func TestSessionStartImmediateAgentExitCompensatesAndRetries(t *testing.T) {
	ctx := context.Background()
	repoDir := t.TempDir()
	projectID := "proj"
	if err := os.MkdirAll(filepath.Join(repoDir, ".azedarach"), 0o755); err != nil {
		t.Fatalf("mkdir .azedarach: %v", err)
	}

	issuesClient := newMigratedIssueClient(t, repoDir, slog.Default())
	t.Cleanup(func() { _ = issuesClient.CloseDB() })
	issueID, err := issuesClient.Create(ctx, issues.CreateTaskParams{Title: "Retry failed bootstrap", Type: domain.TypeTask})
	if err != nil {
		t.Fatalf("create issue: %v", err)
	}

	worktreeRunner := &worktreeCreateRunner{
		worktreePath: filepath.Join(filepath.Dir(repoDir), filepath.Base(repoDir)+"-"+issueID),
		branchName:   "testuser/" + issueID + "/retry-bootstrap",
	}
	tmuxRunner := newSessionStartTmuxRunner()
	store := daemonstate.NewStore()
	runtimeStore := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(t.TempDir(), "runtime.db"), slog.Default())
	t.Cleanup(func() { _ = runtimeStore.Close() })
	d := &Daemon{
		cfg:  Config{RepoDir: repoDir, BaseBranch: "main", CLITool: "codex", SessionShell: "zsh", Logger: slog.Default()},
		tmux: tmux.NewClient(tmuxRunner, slog.Default()), issues: issuesClient,
		session: daemonhandlers.NewSessionHandler(store), sessionStore: store, revision: map[string]uint64{},
		runtimeStoresByProject: map[string]*daemonstate.RuntimeStateStore{projectID: runtimeStore},
		runtimeStoresByRoot:    map[string]*daemonstate.RuntimeStateStore{repoDir: runtimeStore},
		worktreeManagersByRoot: map[string]*git.WorktreeManager{
			repoDir: git.NewWorktreeManager(worktreeRunner, repoDir, slog.Default()),
		},
		worktreeManagersByProject: map[string]*git.WorktreeManager{
			projectID: git.NewWorktreeManager(worktreeRunner, repoDir, slog.Default()),
		},
	}
	req := protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion, RequestID: "req-bootstrap-exit", Kind: protocol.EnvelopeKindCommand,
		Command: daemonhandlers.CommandSessionStart, Meta: protocol.Metadata{ProjectID: naming.ProjectID(projectID)},
		Body: marshalJSON(map[string]string{"project_id": projectID, "session_id": issueID}),
	}
	sessionID := naming.CanonicalSessionID(d.sessionNamingScope(projectID), issueID)
	removePaneOnListCall := 0
	tmuxRunner.onNewSession = func(startedSessionID string) {
		delete(tmuxRunner.sessionsWithoutPanes, startedSessionID)
		tmuxRunner.panes[startedSessionID] = []string{"%1"}
		tmuxRunner.panePIDs[startedSessionID] = 123
		removePaneOnListCall = tmuxRunner.listPanesCalls + 2
		promptPath := tmuxRunner.launchPromptPaths[startedSessionID]
		prompt := tmuxRunner.launchPromptContents[startedSessionID]
		if promptPath == "" || prompt == "" {
			t.Errorf("launch prompt artifact missing for %s", startedSessionID)
		} else if writeErr := os.WriteFile(promptPath, []byte(prompt), 0o600); writeErr != nil {
			t.Errorf("restore pending launch prompt: %v", writeErr)
		}
		now := time.Now().UTC()
		if projectionErr := runtimeStore.UpsertSessionState(ctx, projectID, daemonstate.Session{
			ID: startedSessionID, IssueID: issueID, State: daemonstate.SessionStateStarting, UpdatedAt: now,
		}); projectionErr != nil {
			t.Errorf("seed concurrent starting projection: %v", projectionErr)
		}
		if _, _, observationErr := runtimeStore.ApplyPhysicalSessionObservation(ctx, daemonstate.PhysicalSessionObservation{
			ProjectID: projectID, SessionID: startedSessionID, ObservedState: daemonstate.SessionStateRunning,
			Activity: "busy", ActivitySource: "tmux-observer", UpdatedAt: now, ObservedVersion: now.UnixNano(),
		}); observationErr != nil {
			t.Errorf("seed concurrent tmux observation: %v", observationErr)
		}
	}
	tmuxRunner.onListPanes = func(call int) {
		if removePaneOnListCall > 0 && call == removePaneOnListCall {
			tmuxRunner.sessionsWithoutPanes[sessionID] = true
		}
	}
	acknowledgeManagedAgentOnInitialLaunch(t, d, tmuxRunner, projectID)

	failed, err := d.handleSessionStartDirect(ctx, req)
	if err != nil {
		t.Fatalf("failed bootstrap returned transport error: %v", err)
	}
	if failed.OK || !strings.Contains(failed.Error.Message, string(sessionStartBootstrapAgentExited)) {
		t.Fatalf("failed bootstrap response = %+v, want typed agent exit", failed)
	}
	if tmuxRunner.sessions[sessionID] {
		t.Fatalf("failed bootstrap session %s survived compensation", sessionID)
	}
	if projection, found, projectionErr := runtimeStore.GetSessionState(ctx, projectID, sessionID); projectionErr != nil || !found || projection.State != daemonstate.SessionStateStopped || projection.ObservedState != daemonstate.SessionStateStopped {
		t.Fatalf("failed bootstrap projection = %+v, found=%t err=%v; want durable stopped compensation", projection, found, projectionErr)
	}

	tmuxRunner.onNewSession = nil
	tmuxRunner.onListPanes = nil
	delete(tmuxRunner.sessionsWithoutPanes, sessionID)
	acknowledgeManagedAgentOnInitialLaunch(t, d, tmuxRunner, projectID)
	retried, retryErr := d.handleSessionStartDirect(ctx, req)
	if retryErr != nil || !retried.OK {
		t.Fatalf("retry after failed bootstrap: resp=%+v err=%v", retried, retryErr)
	}
	if !tmuxRunner.sessions[sessionID] {
		t.Fatalf("retry did not leave one live session %s", sessionID)
	}
}

func TestSessionStartTmuxCreateFailureIncludesDiagnostics(t *testing.T) {
	ctx := context.Background()
	repoDir := t.TempDir()
	projectID := "proj"
	if err := os.MkdirAll(filepath.Join(repoDir, ".azedarach"), 0o755); err != nil {
		t.Fatalf("mkdir .azedarach: %v", err)
	}

	issuesClient := newMigratedIssueClient(t, repoDir, slog.Default())
	t.Cleanup(func() { _ = issuesClient.CloseDB() })
	issueID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title: "Diagnose tmux create failure",
		Type:  domain.TypeTask,
	})
	if err != nil {
		t.Fatalf("create issue: %v", err)
	}

	worktreePath := filepath.Join(filepath.Dir(repoDir), filepath.Base(repoDir)+"-"+issueID)
	worktreeRunner := &worktreeCreateRunner{
		worktreePath: worktreePath,
		branchName:   "testuser/" + issueID + "/diagnose-tmux-create",
	}
	tmuxRunner := newSessionStartTmuxRunner()
	tmuxRunner.newSessionErr = errors.New("server exited unexpectedly: /tmp/tmux-501/default")
	store := daemonstate.NewStore()

	d := &Daemon{
		cfg: Config{
			RepoDir:      repoDir,
			BaseBranch:   "main",
			CLITool:      "codex",
			SessionShell: "zsh",
			Logger:       slog.Default(),
		},
		tmux:         tmux.NewClient(tmuxRunner, slog.Default()),
		issues:       issuesClient,
		session:      daemonhandlers.NewSessionHandler(store),
		sessionStore: store,
		revision:     map[string]uint64{},
		worktreeManagersByRoot: map[string]*git.WorktreeManager{
			repoDir: git.NewWorktreeManager(worktreeRunner, repoDir, slog.Default()),
		},
		worktreeManagersByProject: map[string]*git.WorktreeManager{
			projectID: git.NewWorktreeManager(worktreeRunner, repoDir, slog.Default()),
		},
	}

	req := protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-start-tmux-diagnostics",
		Kind:            protocol.EnvelopeKindCommand,
		Command:         "session.start",
		Meta: protocol.Metadata{
			ProjectID: naming.ProjectID(projectID),
		},
		Body: marshalJSON(map[string]string{
			"project_id": projectID,
			"session_id": issueID,
		}),
	}

	resp, err := d.handleSessionStartDirect(ctx, req)
	if err != nil {
		t.Fatalf("handleSessionStartDirect returned error: %v", err)
	}
	if resp.OK || resp.Error == nil {
		t.Fatalf("expected failed response with diagnostics, got %+v", resp)
	}
	message := resp.Error.Message
	sessionID := naming.CanonicalSessionIDForIssue(projectID, naming.IssueID(issueID)).String()
	if len(message) > 2000 {
		t.Fatalf("error message length = %d, want bounded diagnostic under 2000 bytes: %q", len(message), message)
	}
	for _, want := range []string{
		"session start failed during tmux_launch",
		"issue_id=" + issueID,
		"session_id=" + sessionID,
		"cwd=" + worktreePath,
		"tmux new-session -d -s " + sessionID + " -c " + worktreePath,
		"session-launch/launch-",
		"server exited unexpectedly",
		"Worktree exists but tmux session is not active; retry with `az session start " + issueID + "`",
		"Diagnostics: az session diagnose " + issueID,
	} {
		if !strings.Contains(message, want) {
			t.Fatalf("error message = %q, missing %q", message, want)
		}
	}
	if strings.Contains(message, initialPromptShellVariable) {
		t.Fatalf("error message leaked initial prompt launch content: %q", message)
	}
}

func TestSessionResolveConflictCreatesDedicatedWindowAndLaunchesAgent(t *testing.T) {
	ctx := context.Background()
	t.Setenv("AZEDARACH_DAEMON_SCOPE", "global")
	repoDir := t.TempDir()
	managedDir := filepath.Join(t.TempDir(), ".azedarach-generations", "generation.current")
	t.Setenv("PATH", filepath.Join(repoDir, "bin")+string(os.PathListSeparator)+"/usr/bin:/bin")
	projectID := "proj"
	if err := os.MkdirAll(filepath.Join(repoDir, ".azedarach"), 0o755); err != nil {
		t.Fatalf("mkdir .azedarach: %v", err)
	}

	issuesClient := newMigratedIssueClient(t, repoDir, slog.Default())
	t.Cleanup(func() {
		_ = issuesClient.CloseDB()
	})
	issueID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title: "Resolve merge conflicts",
		Type:  domain.TypeTask,
	})
	if err != nil {
		t.Fatalf("create issue: %v", err)
	}

	tmuxRunner := newSessionStartTmuxRunner()
	store := daemonstate.NewStore()
	daemon := &Daemon{
		cfg: Config{
			RepoDir:                 repoDir,
			BaseBranch:              "main",
			CLITool:                 "codex",
			SessionShell:            "zsh",
			ManagedGenerationBinDir: managedDir,
			Logger:                  slog.Default(),
			ScopedRuntime:           true,
		},
		tmux:         tmux.NewClient(tmuxRunner, slog.Default()),
		issues:       issuesClient,
		session:      daemonhandlers.NewSessionHandler(store),
		sessionStore: store,
		revision:     map[string]uint64{},
	}
	attachIsolatedRuntimeStore(t, daemon, projectID)
	tmuxRunner.onNewWindow = func(sessionID, command string) {
		pathMatch := regexp.MustCompile(` -i '([^']+)'$`).FindStringSubmatch(command)
		if len(pathMatch) != 2 {
			t.Errorf("conflict launch command has no artifact path: %s", command)
			return
		}
		script, err := os.ReadFile(pathMatch[1])
		if err != nil {
			t.Errorf("read conflict launch artifact: %v", err)
			return
		}
		incarnation := regexp.MustCompile(`AZEDARACH_AGENT_INCARNATION='([^']+)'`).FindStringSubmatch(string(script))
		if len(incarnation) != 2 {
			t.Errorf("conflict launch artifact has no incarnation")
			return
		}
		entries, _ := os.ReadDir(filepath.Dir(pathMatch[1]))
		for _, entry := range entries {
			if strings.HasSuffix(entry.Name(), ".prompt") {
				_ = os.Remove(filepath.Join(filepath.Dir(pathMatch[1]), entry.Name()))
			}
		}
		tmuxRunner.panes[sessionID] = []string{"%1", "%2"}
		if err := daemon.sessionRuntimeStateStore(projectID).UpsertManagedAgentIdentity(ctx, daemonstate.ManagedAgentIdentity{
			ProjectID: projectID, SessionID: sessionID, LogicalPaneID: "conflict-resolver", TmuxPaneID: "%2",
			PanePID: 123, AgentIncarnation: incarnation[1], ObservedAt: time.Now().UTC(),
		}); err != nil {
			t.Errorf("acknowledge conflict resolver: %v", err)
		}
	}
	worktreePath := filepath.Join(t.TempDir(), "project-'quoted'\nand-newline-"+issueID)
	largePrompt := "Resolve 'quoted' conflict\n" + strings.Repeat("inspect this conflict carefully\n", 1024) + "finish"
	req := protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-resolve-conflict",
		Kind:            protocol.EnvelopeKindCommand,
		Command:         protocol.CommandSessionResolveConflict,
		Meta: protocol.Metadata{
			ProjectID: naming.ProjectID(projectID),
		},
		Body: marshalJSON(protocol.SessionResolveConflictRequestBody{
			ProjectID:     naming.ProjectID(projectID),
			IssueID:       naming.IssueID(issueID),
			Worktree:      worktreePath,
			ConflictFiles: []string{"README.md", " README.md ", "sub/../main.go"},
			Yolo:          true,
			Prompt:        largePrompt,
		}),
	}

	resp, err := daemon.handleSessionResolveConflictDirect(ctx, req)
	if err != nil {
		t.Fatalf("handleSessionResolveConflictDirect returned error: %v", err)
	}
	if !resp.OK {
		t.Fatalf("resolve conflict response not OK: %+v", resp)
	}

	var out protocol.SessionResolveConflictResponseBody
	if err := json.Unmarshal(resp.Body, &out); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	sessionID := naming.CanonicalSessionID(projectID, issueID)
	if out.SessionID.String() != sessionID || out.Worktree != worktreePath || out.WindowName != sessionConflictWindowName {
		t.Fatalf("response = %+v, want session=%s worktree=%s window=%s", out, sessionID, worktreePath, sessionConflictWindowName)
	}
	if out.ReusedSession || out.ReusedWindow {
		t.Fatalf("response reused flags = session:%v window:%v, want false/false", out.ReusedSession, out.ReusedWindow)
	}
	if len(out.ConflictFiles) != 2 || out.ConflictFiles[0] != "README.md" || out.ConflictFiles[1] != "main.go" {
		t.Fatalf("conflict files = %+v, want [README.md main.go]", out.ConflictFiles)
	}
	if !tmuxRunner.sessions[sessionID] {
		t.Fatalf("expected tmux session %q to be created", sessionID)
	}
	if !tmuxRunner.windows[sessionID][sessionConflictWindowName] {
		t.Fatalf("expected conflict window to be created in session %q", sessionID)
	}
	for _, commandName := range []string{"new-session", "new-window"} {
		var commandArgs []string
		for _, command := range tmuxRunner.commands {
			if len(command) > 0 && command[0] == commandName {
				commandArgs = command
				break
			}
		}
		if got, ok := tmuxCommandEnvironmentValue(commandArgs, "AZEDARACH_DAEMON_SCOPE"); !ok || got != "worktree" {
			t.Fatalf("%s scope = %q, present=%t; command=%v", commandName, got, ok, commandArgs)
		}
	}
	if tmuxRunner.sendKeysCalls != 0 || len(tmuxRunner.inputPayloads) != 0 {
		t.Fatalf("conflict launch bypassed artifact transport: send-keys=%d inputs=%d", tmuxRunner.sendKeysCalls, len(tmuxRunner.inputPayloads))
	}
	var launchCommand string
	for _, command := range tmuxRunner.commands {
		if len(command) > 0 && command[0] == "new-window" {
			launchCommand = command[len(command)-1]
			break
		}
	}
	if launchCommand == "" {
		t.Fatalf("missing atomic launch command in tmux commands: %+v", tmuxRunner.commands)
	}
	if strings.Contains(launchCommand, "Resolve merge conflicts for issue "+issueID) ||
		strings.Contains(launchCommand, "README.md") ||
		strings.Contains(launchCommand, "main.go") {
		t.Fatalf("launch command contains raw conflict prompt text: %s", launchCommand)
	}
	quoted := strings.Split(launchCommand, "'")
	if len(quoted) < 4 {
		t.Fatalf("conflict launch does not use bounded artifact: %s", launchCommand)
	}
	artifactBody, err := os.ReadFile(quoted[len(quoted)-2])
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(artifactBody), `AZEDARACH_ISSUE_ID="`+issueID+`" codex`) || !strings.Contains(string(artifactBody), "--dangerously-bypass-approvals-and-sandbox") {
		t.Fatalf("artifact missing codex launch shape or yolo flag: %s", artifactBody)
	}
	if strings.Contains(launchCommand, "export PATH=") || strings.Contains(launchCommand, managedDir) {
		t.Fatalf("conflict launch command injects managed PATH: %s", launchCommand)
	}
	if got := tmuxRunner.env[sessionID]["PATH"]; got != "" {
		t.Fatalf("conflict session injected PATH = %q", got)
	}
	for _, command := range tmuxRunner.commands {
		if len(command) == 0 || command[0] != "new-window" {
			continue
		}
		if got, ok := tmuxCommandEnvironmentValue(command, "PATH"); ok || got != "" {
			t.Fatalf("conflict window injected PATH = %q, %t; command=%v", got, ok, command)
		}
	}

	snapshot := store.ReadSnapshot(projectID)
	session, ok := snapshot.Sessions[sessionID]
	if !ok {
		t.Fatalf("missing session %q in snapshot", sessionID)
	}
	if session.State != daemonstate.SessionStateStarting {
		t.Fatalf("session state = %s, want %s", session.State, daemonstate.SessionStateStarting)
	}
}

func TestSessionResolveConflictRejectsUnacknowledgedInitialWindow(t *testing.T) {
	baseCtx := context.Background()
	ctx, cancel := context.WithCancel(baseCtx)
	repoDir := t.TempDir()
	projectID := "proj-conflict-exit"
	issuesClient := newMigratedIssueClient(t, repoDir, slog.Default())
	t.Cleanup(func() { _ = issuesClient.CloseDB() })
	issueID, err := issuesClient.Create(baseCtx, issues.CreateTaskParams{Title: "Conflict agent exits", Type: domain.TypeTask})
	if err != nil {
		t.Fatal(err)
	}
	runner := newSessionStartTmuxRunner()
	runner.onNewWindow = func(string, string) { cancel() }
	memoryStore := daemonstate.NewStore()
	d := &Daemon{
		cfg:  Config{RepoDir: repoDir, BaseBranch: "main", CLITool: "codex", SessionShell: "zsh", Logger: slog.Default()},
		tmux: tmux.NewClient(runner, slog.Default()), issues: issuesClient,
		session: daemonhandlers.NewSessionHandler(memoryStore), sessionStore: memoryStore, revision: map[string]uint64{},
	}
	attachIsolatedRuntimeStore(t, d, projectID)
	request := protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion, RequestID: "conflict-exit", Kind: protocol.EnvelopeKindCommand,
		Command: protocol.CommandSessionResolveConflict, Meta: protocol.Metadata{ProjectID: naming.ProjectID(projectID)},
		Body: marshalJSON(protocol.SessionResolveConflictRequestBody{
			ProjectID: naming.ProjectID(projectID), IssueID: naming.IssueID(issueID), Worktree: t.TempDir(), Prompt: "resolve",
		}),
	}
	response, err := d.handleSessionResolveConflictDirect(ctx, request)
	if err != nil || response.Error == nil || response.Error.Code != protocol.ErrorCodeUnavailable {
		t.Fatalf("unacknowledged conflict response=%+v err=%v", response.Error, err)
	}
	sessionID := naming.CanonicalSessionID(projectID, issueID)
	if runner.windows[sessionID][sessionConflictWindowName] {
		t.Fatalf("unacknowledged conflict window survived: %+v", runner.windows[sessionID])
	}
}

func TestSessionResolveConflictRetiresWindowAfterAmbiguousCreateErrors(t *testing.T) {
	for _, tc := range []struct {
		name    string
		respawn bool
	}{
		{name: "new window create then error"},
		{name: "respawn window then error", respawn: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.respawn {
				t.Setenv("AZEDARACH_DAEMON_SCOPE", "global")
			}
			ctx := context.Background()
			repoDir := t.TempDir()
			projectID := "proj-conflict-ambiguous"
			issuesClient := newMigratedIssueClient(t, repoDir, slog.Default())
			t.Cleanup(func() { _ = issuesClient.CloseDB() })
			issueID, err := issuesClient.Create(ctx, issues.CreateTaskParams{Title: "Ambiguous conflict launch", Type: domain.TypeTask})
			if err != nil {
				t.Fatal(err)
			}
			runner := newSessionStartTmuxRunner()
			sessionID := naming.CanonicalSessionID(projectID, issueID)
			if tc.respawn {
				runner.sessions[sessionID] = true
				runner.windows[sessionID] = map[string]bool{"shell": true, sessionConflictWindowName: true}
				runner.respawnBeforeWindowError = true
				runner.respawnWindowErr = errors.New("respawn returned after replacement")
			} else {
				runner.createBeforeNewWindowError = true
				runner.newWindowErr = errors.New("create returned after window appeared")
			}
			memoryStore := daemonstate.NewStore()
			d := &Daemon{
				cfg:  Config{RepoDir: repoDir, BaseBranch: "main", CLITool: "codex", SessionShell: "zsh", Logger: slog.Default(), ScopedRuntime: tc.respawn},
				tmux: tmux.NewClient(runner, slog.Default()), issues: issuesClient,
				session: daemonhandlers.NewSessionHandler(memoryStore), sessionStore: memoryStore, revision: map[string]uint64{},
			}
			attachIsolatedRuntimeStore(t, d, projectID)
			request := protocol.RequestEnvelope{
				ProtocolVersion: protocol.CurrentVersion, RequestID: "conflict-ambiguous", Kind: protocol.EnvelopeKindCommand,
				Command: protocol.CommandSessionResolveConflict, Meta: protocol.Metadata{ProjectID: naming.ProjectID(projectID)},
				Body: marshalJSON(protocol.SessionResolveConflictRequestBody{
					ProjectID: naming.ProjectID(projectID), IssueID: naming.IssueID(issueID), Worktree: t.TempDir(), Prompt: "resolve",
				}),
			}
			response, err := d.handleSessionResolveConflictDirect(ctx, request)
			if err != nil || response.OK || response.Error == nil || response.Error.Code != protocol.ErrorCodeInternal {
				t.Fatalf("ambiguous conflict response=%+v err=%v", response, err)
			}
			if runner.windows[sessionID][sessionConflictWindowName] {
				t.Fatalf("ambiguous conflict window survived: %+v", runner.windows[sessionID])
			}
			if !runner.sessions[sessionID] {
				t.Fatalf("cleanup removed the containing session %s", sessionID)
			}
			if tc.respawn {
				var respawnCommand []string
				for _, command := range runner.commands {
					if len(command) > 0 && command[0] == "respawn-window" {
						respawnCommand = command
						break
					}
				}
				if got, ok := tmuxCommandEnvironmentValue(respawnCommand, "AZEDARACH_DAEMON_SCOPE"); !ok || got != "worktree" {
					t.Fatalf("respawn window scope = %q, present=%t; command=%v", got, ok, respawnCommand)
				}
			}
		})
	}
}

func TestRuntimeReconcileRejectsImmediateExitDuringRecreation(t *testing.T) {
	baseCtx := context.Background()
	ctx, cancel := context.WithCancel(baseCtx)
	repoDir := t.TempDir()
	projectID, err := appconfig.ProjectIDForRoot(repoDir)
	if err != nil {
		t.Fatal(err)
	}
	issuesClient := newMigratedIssueClient(t, repoDir, slog.Default())
	t.Cleanup(func() { _ = issuesClient.CloseDB() })
	issueID, err := issuesClient.Create(baseCtx, issues.CreateTaskParams{Title: "Reconcile agent exits", Type: domain.TypeTask})
	if err != nil {
		t.Fatal(err)
	}
	sessionID := naming.CanonicalSessionID(repoDir, issueID)
	runtimeStore := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(t.TempDir(), "runtime.db"), slog.Default())
	t.Cleanup(func() { _ = runtimeStore.Close() })
	if err := upsertSessionStateFixture(runtimeStore, baseCtx, projectID, daemonstate.Session{
		ID: sessionID, IssueID: issueID, State: daemonstate.SessionStateAttached, ObservedState: daemonstate.SessionStateStopped, UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	runner := newSessionStartTmuxRunner()
	runner.onNewSession = func(started string) {
		runner.sessionsWithoutPanes[started] = true
		cancel()
	}
	memoryStore := daemonstate.NewStore()
	manager := git.NewWorktreeManager(&testGitRunner{worktreePath: filepath.Join(filepath.Dir(repoDir), filepath.Base(repoDir)+"-"+issueID), branchName: "riordan/" + issueID + "/reconcile-exit"}, repoDir, slog.Default())
	d := &Daemon{
		cfg: Config{RepoDir: repoDir, CLITool: "codex", Logger: slog.Default()}, issues: issuesClient,
		tmux: tmux.NewClient(runner, slog.Default()), session: daemonhandlers.NewSessionHandler(memoryStore), sessionStore: memoryStore,
		runtimeStoresByRoot: map[string]*daemonstate.RuntimeStateStore{repoDir: runtimeStore}, runtimeStoresByProject: map[string]*daemonstate.RuntimeStateStore{projectID: runtimeStore},
		worktreeManagersByRoot: map[string]*git.WorktreeManager{repoDir: manager}, worktreeManagersByProject: map[string]*git.WorktreeManager{projectID: manager},
	}
	result, err := d.reconcileTmuxAndDaemonSessions(ctx, projectID, issueID)
	if err != nil {
		t.Fatal(err)
	}
	if result.RecreatedTmuxSessions != 0 || runner.sessions[sessionID] {
		t.Fatalf("immediate-exit reconcile result=%+v live=%t", result, runner.sessions[sessionID])
	}
	projection, found, err := runtimeStore.GetWorkerSessionStateByIssueID(baseCtx, projectID, issueID, sessionID)
	if err != nil || !found || projection.State != daemonstate.SessionStateAttached || projection.ObservedState != daemonstate.SessionStateStopped {
		t.Fatalf("reconcile projection=%+v found=%t err=%v", projection, found, err)
	}
}

func TestReconcileSkipsRecreateWhileStopInProgress(t *testing.T) {
	const (
		projectID = "proj"
		issueID   = "az-1"
	)

	store := daemonstate.NewStore()
	runtimeStateStore := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(t.TempDir(), "azedarach.db"), slog.Default())
	t.Cleanup(func() {
		_ = runtimeStateStore.Close()
	})
	sessionID := naming.CanonicalSessionID(".", issueID)
	if _, err := store.UpsertSession(projectID, sessionID, issueID, daemonstate.SessionStateStarting); err != nil {
		t.Fatalf("seed starting session: %v", err)
	}
	if _, err := store.UpsertSession(projectID, sessionID, issueID, daemonstate.SessionStateAttached); err != nil {
		t.Fatalf("seed attached session: %v", err)
	}

	tmuxRunner := newTestTmuxRunner(sessionID)
	daemon := &Daemon{
		cfg: Config{
			RepoDir: ".",
			CLITool: "claude",
			Logger:  slog.Default(),
		},
		tmux:         tmux.NewClient(tmuxRunner, slog.Default()),
		session:      daemonhandlers.NewSessionHandler(store),
		sessionStore: store,
		runtimeStoresByRoot: map[string]*daemonstate.RuntimeStateStore{
			".": runtimeStateStore,
		},
		runtimeStoresByProject: map[string]*daemonstate.RuntimeStateStore{
			projectID: runtimeStateStore,
		},
		worktreeManagersByRoot: map[string]*git.WorktreeManager{
			".": git.NewWorktreeManager(&testGitRunner{worktreePath: "/tmp/proj-az-1", branchName: "riordan/az-1/test"}, ".", slog.Default()),
		},
		worktreeManagersByProject: map[string]*git.WorktreeManager{
			projectID: git.NewWorktreeManager(&testGitRunner{worktreePath: "/tmp/proj-az-1", branchName: "riordan/az-1/test"}, ".", slog.Default()),
		},
	}

	body, err := json.Marshal(map[string]string{
		"project_id": projectID,
		"session_id": issueID,
	})
	if err != nil {
		t.Fatalf("marshal request body: %v", err)
	}
	req := protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-stop",
		Kind:            protocol.EnvelopeKindCommand,
		Command:         "session.stop",
		Body:            body,
		Meta: protocol.Metadata{
			ProjectID: naming.ProjectID(projectID),
		},
	}

	type stopResult struct {
		response protocol.ResponseEnvelope
		err      error
	}
	stopped := make(chan stopResult, 1)
	go func() {
		resp, runErr := daemon.handleSessionStopDirect(context.Background(), req)
		stopped <- stopResult{response: resp, err: runErr}
	}()

	<-tmuxRunner.killEntered

	newSessionCallsBeforeReconcile := tmuxRunner.newSessionCalls
	result, err := daemon.reconcileTmuxAndDaemonSessions(context.Background(), projectID, issueID)
	if err != nil {
		t.Fatalf("reconcile during stop: %v", err)
	}
	if result.RecreatedTmuxSessions != 0 {
		t.Fatalf("recreated tmux sessions = %d, want 0", result.RecreatedTmuxSessions)
	}
	if tmuxRunner.newSessionCalls != newSessionCallsBeforeReconcile {
		t.Fatalf("new-session calls increased from %d to %d during reconcile", newSessionCallsBeforeReconcile, tmuxRunner.newSessionCalls)
	}

	close(tmuxRunner.killRelease)
	stopOutcome := <-stopped
	if stopOutcome.err != nil {
		t.Fatalf("stop command failed: %v", stopOutcome.err)
	}
	if !stopOutcome.response.OK {
		t.Fatalf("stop response = %+v", stopOutcome.response)
	}

}

func TestHandleSessionStopDirectMarksStoppedWhenTmuxSessionMissing(t *testing.T) {
	const (
		projectID = "proj"
		issueID   = "az-1"
	)

	store := daemonstate.NewStore()
	tmuxRunner := &testTmuxRunner{
		sessions:    map[string]bool{},
		killEntered: make(chan struct{}),
		killRelease: make(chan struct{}),
	}
	daemon := &Daemon{
		cfg: Config{
			RepoDir: ".",
			Logger:  slog.Default(),
		},
		tmux:         tmux.NewClient(tmuxRunner, slog.Default()),
		session:      daemonhandlers.NewSessionHandler(store),
		sessionStore: store,
	}

	body, err := json.Marshal(map[string]string{
		"project_id": projectID,
		"session_id": issueID,
	})
	if err != nil {
		t.Fatalf("marshal request body: %v", err)
	}
	attachIsolatedRuntimeStore(t, daemon, projectID)
	req := protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-stop-missing",
		Kind:            protocol.EnvelopeKindCommand,
		Command:         "session.stop",
		Body:            body,
		Meta: protocol.Metadata{
			ProjectID: naming.ProjectID(projectID),
		},
	}

	resp, err := daemon.handleSessionStopDirect(context.Background(), req)
	if err != nil {
		t.Fatalf("handleSessionStopDirect returned error: %v", err)
	}
	if !resp.OK {
		t.Fatalf("stop response not OK: %+v", resp)
	}

	snapshot := store.ReadSnapshot(projectID)
	sessionID := naming.CanonicalSessionID(daemon.sessionNamingScope(projectID), issueID)
	got, ok := snapshot.Sessions[sessionID]
	if !ok {
		t.Fatalf("missing session %q in snapshot", sessionID)
	}
	if got.State != daemonstate.SessionStateStopped {
		t.Fatalf("session state = %s, want %s", got.State, daemonstate.SessionStateStopped)
	}
}

func TestHandleSessionStopDirectCleanupFailureDoesNotMarkStoppedOrKillTmux(t *testing.T) {
	const (
		projectID = "proj"
		issueID   = "az-1"
	)

	root := t.TempDir()
	sessionID := naming.CanonicalSessionID(projectID, issueID)
	store := daemonstate.NewStore()
	if _, err := store.ForceUpsertSession(projectID, sessionID, issueID, daemonstate.SessionStateAttached); err != nil {
		t.Fatalf("seed attached session: %v", err)
	}
	tmuxRunner := newTestTmuxRunner(sessionID)
	daemon := &Daemon{
		cfg: Config{
			RepoDir:      root,
			SessionShell: "sh",
			IssueResources: appconfig.IssueResourcesConfig{
				CleanupCommands: []string{"exit 9"},
			},
			Logger: slog.Default(),
		},
		tmux:         tmux.NewClient(tmuxRunner, slog.Default()),
		session:      daemonhandlers.NewSessionHandler(store),
		sessionStore: store,
	}

	body, err := json.Marshal(map[string]string{
		"project_id": projectID,
		"session_id": issueID,
	})
	if err != nil {
		t.Fatalf("marshal request body: %v", err)
	}
	resp, err := daemon.handleSessionStopDirect(context.Background(), protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-stop-cleanup-fail",
		Kind:            protocol.EnvelopeKindCommand,
		Command:         "session.stop",
		Body:            body,
		Meta:            protocol.Metadata{ProjectID: naming.ProjectID(projectID)},
	})
	if err != nil {
		t.Fatalf("handleSessionStopDirect returned error: %v", err)
	}
	if resp.OK || resp.Error == nil {
		t.Fatalf("stop response = %+v, want cleanup failure", resp)
	}
	if !strings.Contains(resp.Error.Message, "issue resource cleanup failed") {
		t.Fatalf("stop error = %q, want cleanup failure context", resp.Error.Message)
	}

	snapshot := store.ReadSnapshot(projectID)
	got := snapshot.Sessions[sessionID]
	if got.State != daemonstate.SessionStateAttached {
		t.Fatalf("session state = %s, want still attached after cleanup failure", got.State)
	}
	if !tmuxRunner.hasSession(sessionID) {
		t.Fatalf("expected tmux session %q to remain running after cleanup failure", sessionID)
	}
}

func TestHandleSessionStopDirectIgnoresCleanupSkipRequestField(t *testing.T) {
	const (
		projectID = "proj"
		issueID   = "az-1"
	)

	root := t.TempDir()
	sessionID := naming.CanonicalSessionID(projectID, issueID)
	store := daemonstate.NewStore()
	if _, err := store.ForceUpsertSession(projectID, sessionID, issueID, daemonstate.SessionStateAttached); err != nil {
		t.Fatalf("seed attached session: %v", err)
	}
	tmuxRunner := newTestTmuxRunner(sessionID)
	daemon := &Daemon{
		cfg: Config{
			RepoDir:      root,
			SessionShell: "sh",
			IssueResources: appconfig.IssueResourcesConfig{
				CleanupCommands: []string{"exit 9"},
			},
			Logger: slog.Default(),
		},
		tmux:         tmux.NewClient(tmuxRunner, slog.Default()),
		session:      daemonhandlers.NewSessionHandler(store),
		sessionStore: store,
	}

	body, err := json.Marshal(map[string]any{
		"project_id":                  projectID,
		"session_id":                  issueID,
		"skip_issue_resource_cleanup": true,
	})
	if err != nil {
		t.Fatalf("marshal request body: %v", err)
	}
	resp, err := daemon.handleSessionStopDirect(context.Background(), protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-stop-cleanup-skip-ignored",
		Kind:            protocol.EnvelopeKindCommand,
		Command:         "session.stop",
		Body:            body,
		Meta:            protocol.Metadata{ProjectID: naming.ProjectID(projectID)},
	})
	if err != nil {
		t.Fatalf("handleSessionStopDirect returned error: %v", err)
	}
	if resp.OK || resp.Error == nil {
		t.Fatalf("stop response = %+v, want cleanup failure", resp)
	}
	if !strings.Contains(resp.Error.Message, "issue resource cleanup failed") {
		t.Fatalf("stop error = %q, want cleanup failure context", resp.Error.Message)
	}
	if !tmuxRunner.hasSession(sessionID) {
		t.Fatalf("expected tmux session %q to remain running after cleanup failure", sessionID)
	}
}

func TestHandleSessionStopDirectKillsLegacyIssueNamedSession(t *testing.T) {
	const (
		projectID = "proj"
		issueID   = "bpm"
	)

	store := daemonstate.NewStore()
	tmuxRunner := &testTmuxRunner{
		sessions: map[string]bool{
			issueID: true,
		},
		killEntered: make(chan struct{}),
		killRelease: make(chan struct{}),
	}
	close(tmuxRunner.killRelease)
	daemon := &Daemon{
		cfg: Config{
			RepoDir: ".",
			Logger:  slog.Default(),
		},
		tmux:         tmux.NewClient(tmuxRunner, slog.Default()),
		session:      daemonhandlers.NewSessionHandler(store),
		sessionStore: store,
	}

	body, err := json.Marshal(map[string]string{
		"project_id": projectID,
		"session_id": issueID,
	})
	if err != nil {
		t.Fatalf("marshal request body: %v", err)
	}
	attachIsolatedRuntimeStore(t, daemon, projectID)
	req := protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-stop-legacy-name",
		Kind:            protocol.EnvelopeKindCommand,
		Command:         "session.stop",
		Body:            body,
		Meta: protocol.Metadata{
			ProjectID: naming.ProjectID(projectID),
		},
	}

	resp, err := daemon.handleSessionStopDirect(context.Background(), req)
	if err != nil {
		t.Fatalf("handleSessionStopDirect returned error: %v", err)
	}
	if !resp.OK {
		t.Fatalf("stop response not OK: %+v", resp)
	}

	tmuxRunner.mu.Lock()
	_, sessionStillRunning := tmuxRunner.sessions[issueID]
	tmuxRunner.mu.Unlock()
	if sessionStillRunning {
		t.Fatalf("expected legacy tmux session %q to be killed", issueID)
	}

	snapshot := store.ReadSnapshot(projectID)
	sessionID := naming.CanonicalSessionID(projectID, issueID)
	got, ok := snapshot.Sessions[sessionID]
	if !ok {
		t.Fatalf("missing session %q in snapshot", sessionID)
	}
	if got.State != daemonstate.SessionStateStopped {
		t.Fatalf("session state = %s, want %s", got.State, daemonstate.SessionStateStopped)
	}
}

func TestHandleSessionStopDirectKillsForeignPrefixedIssueSession(t *testing.T) {
	const (
		projectID = "proj"
		issueID   = "dsj"
		liveName  = "ch-dsj"
	)

	store := daemonstate.NewStore()
	runtimeStateStore := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(t.TempDir(), "runtime.db"), slog.Default())
	t.Cleanup(func() { _ = runtimeStateStore.Close() })
	if err := upsertSessionStateFixture(runtimeStateStore, context.Background(), projectID, daemonstate.Session{
		ID:            liveName,
		IssueID:       issueID,
		State:         daemonstate.SessionStateAttached,
		ObservedState: daemonstate.SessionStateAttached,
		UpdatedAt:     time.Now().UTC(),
	}); err != nil {
		t.Fatalf("seed runtime session state: %v", err)
	}
	tmuxRunner := &testTmuxRunner{
		sessions: map[string]bool{
			liveName: true,
		},
		killEntered: make(chan struct{}),
		killRelease: make(chan struct{}),
	}
	close(tmuxRunner.killRelease)
	daemon := &Daemon{
		cfg: Config{
			RepoDir: ".",
			Logger:  slog.Default(),
		},
		tmux:         tmux.NewClient(tmuxRunner, slog.Default()),
		session:      daemonhandlers.NewSessionHandler(store),
		sessionStore: store,
		runtimeStoresByProject: map[string]*daemonstate.RuntimeStateStore{
			projectID: runtimeStateStore,
		},
	}

	body, err := json.Marshal(map[string]string{
		"project_id": projectID,
		"session_id": issueID,
	})
	if err != nil {
		t.Fatalf("marshal request body: %v", err)
	}
	req := protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-stop-foreign-prefixed-name",
		Kind:            protocol.EnvelopeKindCommand,
		Command:         "session.stop",
		Body:            body,
		Meta: protocol.Metadata{
			ProjectID: naming.ProjectID(projectID),
		},
	}

	resp, err := daemon.handleSessionStopDirect(context.Background(), req)
	if err != nil {
		t.Fatalf("handleSessionStopDirect returned error: %v", err)
	}
	if !resp.OK {
		t.Fatalf("stop response not OK: %+v", resp)
	}
	var payload struct {
		Output string `json:"output"`
	}
	if err := json.Unmarshal(resp.Body, &payload); err != nil {
		t.Fatalf("unmarshal response body: %v", err)
	}
	if !strings.Contains(payload.Output, "Killing session: "+liveName) {
		t.Fatalf("output = %q, want resolved live session name %q", payload.Output, liveName)
	}

	tmuxRunner.mu.Lock()
	_, sessionStillRunning := tmuxRunner.sessions[liveName]
	tmuxRunner.mu.Unlock()
	if sessionStillRunning {
		t.Fatalf("expected foreign-prefixed tmux session %q to be killed", liveName)
	}
	row, found, err := runtimeStateStore.GetSessionState(context.Background(), projectID, liveName)
	if err != nil {
		t.Fatalf("get runtime session state: %v", err)
	}
	if !found {
		t.Fatalf("runtime session state %q missing", liveName)
	}
	if row.State != daemonstate.SessionStateStopped || row.ObservedState != daemonstate.SessionStateStopped {
		t.Fatalf("runtime session state = desired %s observed %s, want stopped/stopped", row.State, row.ObservedState)
	}
}

func TestTmuxSessionNamesForIssueMatchesForeignPrefixedSession(t *testing.T) {
	const (
		projectID = "proj"
		issueID   = "frp"
		liveName  = "ch-frp"
	)

	tmuxRunner := newTestTmuxRunner(liveName)
	close(tmuxRunner.killRelease)
	daemon := &Daemon{
		cfg:  Config{RepoDir: ".", Logger: slog.Default()},
		tmux: tmux.NewClient(tmuxRunner, slog.Default()),
	}

	names, err := daemon.tmuxSessionNamesForIssue(context.Background(), projectID, issueID, naming.CanonicalSessionID(projectID, issueID), daemonInvariantSourceTmux)
	if err != nil {
		t.Fatalf("tmuxSessionNamesForIssue error: %v", err)
	}
	if len(names) != 1 || names[0] != liveName {
		t.Fatalf("tmux session names = %+v, want [%s]", names, liveName)
	}
}

func TestHandleSessionStopDirectRecordsDesiredStateBeforeTmuxKillCompletes(t *testing.T) {
	const (
		projectID = "proj"
		issueID   = "az-1"
	)
	sessionID := naming.CanonicalSessionID(projectID, issueID)

	sessionStore := daemonstate.NewStore()
	if _, err := sessionStore.UpsertSession(projectID, sessionID, issueID, daemonstate.SessionStateAttached); err != nil {
		t.Fatalf("seed session store: %v", err)
	}

	runtimeStateStore := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(t.TempDir(), "runtime.db"), slog.Default())
	t.Cleanup(func() {
		_ = runtimeStateStore.Close()
	})
	if err := upsertSessionStateFixture(runtimeStateStore, context.Background(), projectID, daemonstate.Session{
		ID:        sessionID,
		IssueID:   issueID,
		State:     daemonstate.SessionStateAttached,
		UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("seed runtime state: %v", err)
	}

	tmuxRunner := &testTmuxRunner{
		sessions: map[string]bool{
			sessionID: true,
		},
		killEntered: make(chan struct{}),
		killRelease: make(chan struct{}),
	}
	daemon := &Daemon{
		cfg:          Config{RepoDir: ".", Logger: slog.Default()},
		tmux:         tmux.NewClient(tmuxRunner, slog.Default()),
		session:      daemonhandlers.NewSessionHandler(sessionStore),
		sessionStore: sessionStore,
		runtimeStoresByRoot: map[string]*daemonstate.RuntimeStateStore{
			".": runtimeStateStore,
		},
		worktreeManagersByRoot: map[string]*git.WorktreeManager{
			".": git.NewWorktreeManager(&testGitRunner{worktreePath: "/tmp/proj-az-1", branchName: "riordan/az-1/test"}, ".", slog.Default()),
		},
		worktreeManagersByProject: map[string]*git.WorktreeManager{
			projectID: git.NewWorktreeManager(&testGitRunner{worktreePath: "/tmp/proj-az-1", branchName: "riordan/az-1/test"}, ".", slog.Default()),
		},
	}

	body, err := json.Marshal(map[string]string{
		"project_id": projectID,
		"session_id": issueID,
	})
	if err != nil {
		t.Fatalf("marshal request body: %v", err)
	}
	req := protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-stop-intent",
		Kind:            protocol.EnvelopeKindCommand,
		Command:         "session.stop",
		Body:            body,
		Meta: protocol.Metadata{
			ProjectID: naming.ProjectID(projectID),
		},
	}

	type stopResult struct {
		response protocol.ResponseEnvelope
		err      error
	}
	stopped := make(chan stopResult, 1)
	go func() {
		resp, runErr := daemon.handleSessionStopDirect(context.Background(), req)
		stopped <- stopResult{response: resp, err: runErr}
	}()

	<-tmuxRunner.killEntered

	rows, err := runtimeStateStore.ListSessionStates(context.Background(), projectID)
	if err != nil {
		t.Fatalf("list runtime state rows: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("runtime rows = %d, want 1", len(rows))
	}
	if rows[0].State != daemonstate.SessionStateStopped {
		t.Fatalf("desired session state = %s, want %s", rows[0].State, daemonstate.SessionStateStopped)
	}
	if rows[0].ObservedState != daemonstate.SessionStateStopped {
		t.Fatalf("observed session state = %s, want %s", rows[0].ObservedState, daemonstate.SessionStateStopped)
	}
	close(tmuxRunner.killRelease)

	result := <-stopped
	if result.err != nil {
		t.Fatalf("stop command failed: %v", result.err)
	}
	if !result.response.OK {
		t.Fatalf("stop response = %+v", result.response)
	}

	rows, err = runtimeStateStore.ListSessionStates(context.Background(), projectID)
	if err != nil {
		t.Fatalf("list runtime state rows after stop: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("runtime rows after stop = %d, want 1", len(rows))
	}
	if rows[0].State != daemonstate.SessionStateStopped {
		t.Fatalf("desired session state after stop = %s, want %s", rows[0].State, daemonstate.SessionStateStopped)
	}
	if rows[0].ObservedState != daemonstate.SessionStateStopped {
		t.Fatalf("observed session state after stop = %s, want %s", rows[0].ObservedState, daemonstate.SessionStateStopped)
	}
}

func TestPersistTmuxSessionProjectionSnapshotMarksMissingSessionsStopped(t *testing.T) {
	const projectID = "proj"

	sessionStore := daemonstate.NewStore()
	runtimeStateStore := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(t.TempDir(), "projections.db"), slog.Default())
	defer func() {
		if err := runtimeStateStore.Close(); err != nil {
			t.Fatalf("close projection store: %v", err)
		}
	}()

	staleIssueID := "az-stale"
	liveIssueID := "az-live"
	staleSessionID := naming.CanonicalSessionID(projectID, staleIssueID)
	liveSessionID := naming.CanonicalSessionID(projectID, liveIssueID)
	staleStartedAt := time.Now().UTC().Add(-2 * time.Hour)
	liveStartedAt := time.Now().UTC().Add(-1 * time.Hour)

	ctx := context.Background()
	if err := upsertSessionStateFixture(runtimeStateStore, ctx, projectID, daemonstate.Session{
		ID:        staleSessionID,
		IssueID:   staleIssueID,
		State:     daemonstate.SessionStateAttached,
		StartedAt: &staleStartedAt,
		UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("seed stale projection session: %v", err)
	}
	if err := upsertSessionStateFixture(runtimeStateStore, ctx, projectID, daemonstate.Session{
		ID:        liveSessionID,
		IssueID:   liveIssueID,
		State:     daemonstate.SessionStateAttached,
		StartedAt: &liveStartedAt,
		UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("seed live projection session: %v", err)
	}

	if _, err := sessionStore.UpsertSession(projectID, liveSessionID, liveIssueID, daemonstate.SessionStateAttached); err != nil {
		t.Fatalf("seed session store live session: %v", err)
	}

	daemon := &Daemon{
		cfg: Config{
			RepoDir: ".",
			Logger:  slog.Default(),
		},
		sessionStore: sessionStore,
		runtimeStoresByRoot: map[string]*daemonstate.RuntimeStateStore{
			".": runtimeStateStore,
		},
	}

	if err := daemon.persistTmuxSessionRuntimeState(ctx, projectID, []tmux.SessionInfo{{Name: liveSessionID}}, nil); err != nil {
		t.Fatalf("persistTmuxSessionRuntimeState: %v", err)
	}

	rows, err := runtimeStateStore.ListSessionStates(ctx, projectID)
	if err != nil {
		t.Fatalf("list projection sessions: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("projection sessions count = %d, want 2", len(rows))
	}
	rowsByID := map[string]daemonstate.Session{}
	for _, row := range rows {
		rowsByID[row.ID] = row
	}
	liveRow, ok := rowsByID[liveSessionID]
	if !ok {
		t.Fatalf("missing live projection session %s", liveSessionID)
	}
	if liveRow.State != daemonstate.SessionStateAttached {
		t.Fatalf("live desired state = %s, want %s", liveRow.State, daemonstate.SessionStateAttached)
	}
	if liveRow.ObservedState != daemonstate.SessionStateAttached {
		t.Fatalf("live observed state = %s, want %s", liveRow.ObservedState, daemonstate.SessionStateAttached)
	}
	staleRow, ok := rowsByID[staleSessionID]
	if !ok {
		t.Fatalf("missing stale projection session %s", staleSessionID)
	}
	if staleRow.State != daemonstate.SessionStateAttached {
		t.Fatalf("stale desired state = %s, want %s", staleRow.State, daemonstate.SessionStateAttached)
	}
	if staleRow.ObservedState != daemonstate.SessionStateStopped {
		t.Fatalf("stale observed state = %s, want %s", staleRow.ObservedState, daemonstate.SessionStateStopped)
	}
}

func TestHandleSessionStopDirectWritesStoppedProjectionBeforeKillCompletes(t *testing.T) {
	const (
		projectID = "proj"
		issueID   = "az-1"
	)

	store := daemonstate.NewStore()
	runtimeStateStore := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(t.TempDir(), "projections.db"), slog.Default())
	defer func() {
		if err := runtimeStateStore.Close(); err != nil {
			t.Fatalf("close projection store: %v", err)
		}
	}()

	sessionID := naming.CanonicalSessionID(projectID, issueID)
	tmuxRunner := newTestTmuxRunner(sessionID)
	close(tmuxRunner.killRelease)
	daemon := &Daemon{
		cfg: Config{
			RepoDir: ".",
			Logger:  slog.Default(),
		},
		tmux:         tmux.NewClient(tmuxRunner, slog.Default()),
		session:      daemonhandlers.NewSessionHandler(store),
		sessionStore: store,
		runtimeStoresByRoot: map[string]*daemonstate.RuntimeStateStore{
			".": runtimeStateStore,
		},
	}

	if err := upsertSessionStateFixture(runtimeStateStore, context.Background(), projectID, daemonstate.Session{
		ID:        sessionID,
		IssueID:   issueID,
		State:     daemonstate.SessionStateAttached,
		UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("seed projection session: %v", err)
	}
	if _, err := store.UpsertSession(projectID, sessionID, issueID, daemonstate.SessionStateAttached); err != nil {
		t.Fatalf("seed cache session: %v", err)
	}

	req := protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-stop-write-through",
		Kind:            protocol.EnvelopeKindCommand,
		Command:         "session.stop",
		Body: marshalJSON(map[string]string{
			"project_id": projectID,
			"session_id": issueID,
		}),
		Meta: protocol.Metadata{
			ProjectID: naming.ProjectID(projectID),
		},
	}

	if _, err := daemon.handleSessionStopDirect(context.Background(), req); err != nil {
		t.Fatalf("handleSessionStopDirect returned error: %v", err)
	}

	rows, err := runtimeStateStore.ListSessionStates(context.Background(), projectID)
	if err != nil {
		t.Fatalf("list projection sessions: %v", err)
	}
	foundStopped := false
	for _, row := range rows {
		if row.ID == sessionID && row.State == daemonstate.SessionStateStopped {
			foundStopped = true
			break
		}
	}
	if !foundStopped {
		t.Fatalf("expected write-through stopped projection for %s before kill completes; rows=%+v", sessionID, rows)
	}
	snapshot := store.ReadSnapshot(projectID)
	if got := snapshot.Sessions[sessionID].State; got != daemonstate.SessionStateStopped {
		t.Fatalf("expected cached session state stopped for %s before kill completes, got %s", sessionID, got)
	}

	tmuxRunner.mu.Lock()
	_, sessionStillRunning := tmuxRunner.sessions[sessionID]
	tmuxRunner.mu.Unlock()
	if sessionStillRunning {
		t.Fatalf("expected tmux session %q to be killed", sessionID)
	}

}

func TestHandleSessionStopDirectFailsWhenStopIntentProjectionWriteFails(t *testing.T) {
	const (
		projectID = "proj"
		issueID   = "az-1"
	)

	store := daemonstate.NewStore()
	badStorePath := filepath.Join(t.TempDir(), "runtime-store-dir")
	if err := os.MkdirAll(badStorePath, 0o755); err != nil {
		t.Fatalf("create bad runtime store path: %v", err)
	}
	runtimeStateStore := daemonstate.NewRuntimeStateStoreAtPath(badStorePath, slog.Default())
	t.Cleanup(func() {
		_ = runtimeStateStore.Close()
	})
	sessionID := naming.CanonicalSessionID(projectID, issueID)
	tmuxRunner := newTestTmuxRunner(sessionID)
	close(tmuxRunner.killRelease)
	daemon := &Daemon{
		cfg: Config{
			RepoDir: ".",
			Logger:  slog.Default(),
		},
		tmux:         tmux.NewClient(tmuxRunner, slog.Default()),
		session:      daemonhandlers.NewSessionHandler(store),
		sessionStore: store,
		runtimeStoresByRoot: map[string]*daemonstate.RuntimeStateStore{
			".": runtimeStateStore,
		},
	}

	if _, err := store.UpsertSession(projectID, sessionID, issueID, daemonstate.SessionStateAttached); err != nil {
		t.Fatalf("seed cache session: %v", err)
	}

	req := protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-stop-intent-write-fail",
		Kind:            protocol.EnvelopeKindCommand,
		Command:         "session.stop",
		Body: marshalJSON(map[string]string{
			"project_id": projectID,
			"session_id": issueID,
		}),
		Meta: protocol.Metadata{
			ProjectID: naming.ProjectID(projectID),
		},
	}

	resp, err := daemon.handleSessionStopDirect(context.Background(), req)
	if err != nil {
		t.Fatalf("handleSessionStopDirect returned error: %v", err)
	}
	if resp.OK {
		t.Fatalf("expected stop response to fail when stop intent write fails: %+v", resp)
	}
	if resp.Error == nil || resp.Error.Code != protocol.ErrorCodeInternal {
		t.Fatalf("stop error = %+v, want internal error", resp.Error)
	}
	if !strings.Contains(strings.ToLower(resp.Error.Message), "resolve rooted orchestrator session stop") {
		t.Fatalf("stop error message = %q, want rooted-orchestrator preflight failure", resp.Error.Message)
	}

	snapshot := store.ReadSnapshot(projectID)
	if got := snapshot.Sessions[sessionID].State; got != daemonstate.SessionStateAttached {
		t.Fatalf("cached session state = %s, want %s when stop intent write fails", got, daemonstate.SessionStateAttached)
	}
	tmuxRunner.mu.Lock()
	_, sessionStillRunning := tmuxRunner.sessions[sessionID]
	tmuxRunner.mu.Unlock()
	if !sessionStillRunning {
		t.Fatalf("expected tmux session %q to remain running when stop intent write fails", sessionID)
	}
}

func TestListTmuxSessionsCacheFirstSkipsStopPendingCachedSession(t *testing.T) {
	const (
		projectID = "proj"
		issueID   = "az-1"
	)

	runtimeStateStore := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(t.TempDir(), "projections.db"), slog.Default())
	defer func() {
		if err := runtimeStateStore.Close(); err != nil {
			t.Fatalf("close projection store: %v", err)
		}
	}()

	sessionID := naming.CanonicalSessionID(projectID, issueID)
	if err := upsertSessionStateFixture(runtimeStateStore, context.Background(), projectID, daemonstate.Session{
		ID:        sessionID,
		IssueID:   issueID,
		State:     daemonstate.SessionStateAttached,
		UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("seed projection session: %v", err)
	}

	tmuxRunner := &testTmuxRunner{
		sessions:    map[string]bool{},
		killEntered: make(chan struct{}),
		killRelease: make(chan struct{}),
	}
	daemon := &Daemon{
		cfg:  Config{RepoDir: ".", Logger: slog.Default()},
		tmux: tmux.NewClient(tmuxRunner, slog.Default()),
		runtimeStoresByRoot: map[string]*daemonstate.RuntimeStateStore{
			".": runtimeStateStore,
		},
	}

	clearStopPending := daemon.markSessionStopPending(projectID, issueID)
	defer clearStopPending()

	sessions, err := daemon.listTmuxSessionsCacheFirst(context.Background(), projectID)
	if err != nil {
		t.Fatalf("listTmuxSessionsCacheFirst: %v", err)
	}
	if len(sessions) != 0 {
		t.Fatalf("sessions = %v, want empty while session stop is pending", sessions)
	}
}

func TestSessionAttachDoesNotRequireRuntimeReconcile(t *testing.T) {
	const (
		projectID = "proj"
		issueID   = "az-1"
	)
	sessionID := naming.CanonicalSessionID(projectID, issueID)
	tmuxRunner := newTestTmuxRunner(sessionID)
	store := daemonstate.NewStore()
	if _, err := store.UpsertSession(projectID, sessionID, issueID, daemonstate.SessionStateStarting); err != nil {
		t.Fatalf("seed session store: %v", err)
	}
	recorder := &runtimeReconcileRecorder{}

	daemon := &Daemon{
		cfg: Config{
			RepoDir: ".",
			Logger:  slog.Default(),
		},
		tmux:              tmux.NewClient(tmuxRunner, slog.Default()),
		session:           daemonhandlers.NewSessionHandler(store),
		sessionStore:      store,
		runtimeReconciler: recorder,
	}

	body, err := json.Marshal(sessionCommandBody{
		ProjectID: projectID,
		SessionID: sessionID,
	})
	if err != nil {
		t.Fatalf("marshal request body: %v", err)
	}
	attachIsolatedRuntimeStore(t, daemon, projectID)
	resp, err := daemon.handleSessionAttach(context.Background(), protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-attach-refresh",
		Kind:            protocol.EnvelopeKindCommand,
		Command:         daemonhandlers.CommandSessionAttach,
		Meta: protocol.Metadata{
			ProjectID: naming.ProjectID(projectID),
		},
		Body: body,
	})
	if err != nil {
		t.Fatalf("handleSessionAttach returned error: %v", err)
	}
	if !resp.OK {
		t.Fatalf("response not ok: %+v", resp.Error)
	}
	calls, projectIDs := recorder.snapshot()
	if calls != 0 {
		t.Fatalf("runtime reconcile calls = %d, want 0", calls)
	}
	if len(projectIDs) != 0 {
		t.Fatalf("runtime reconcile project ids = %v, want none", projectIDs)
	}
}

func TestSessionPauseResumeUseIssueScopedRuntimeReconcile(t *testing.T) {
	const (
		projectID = "proj"
		issueID   = "az-1"
	)
	sessionID := naming.CanonicalSessionID(projectID, issueID)
	store := daemonstate.NewStore()
	if _, err := store.UpsertSession(projectID, sessionID, issueID, daemonstate.SessionStateAttached); err != nil {
		t.Fatalf("seed attached session: %v", err)
	}
	recorder := &runtimeReconcileRecorder{}
	daemon := &Daemon{
		cfg: Config{
			RepoDir: ".",
			Logger:  slog.Default(),
		},
		session:           daemonhandlers.NewSessionHandler(store),
		sessionStore:      store,
		runtimeReconciler: recorder,
	}

	attachIsolatedRuntimeStore(t, daemon, projectID)
	req := protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-pause",
		Kind:            protocol.EnvelopeKindCommand,
		Command:         daemonhandlers.CommandSessionPause,
		Meta: protocol.Metadata{
			ProjectID: naming.ProjectID(projectID),
		},
		Body: marshalJSON(map[string]string{
			"project_id": projectID,
			"session_id": issueID,
		}),
	}
	resp, err := daemon.handleSessionPause(context.Background(), req)
	if err != nil {
		t.Fatalf("handleSessionPause returned error: %v", err)
	}
	if !resp.OK {
		t.Fatalf("pause response not ok: %+v", resp.Error)
	}
	waitForRuntimeReconcileCalls(t, recorder, 1)

	req.RequestID = "req-resume"
	req.Command = daemonhandlers.CommandSessionResume
	resp, err = daemon.handleSessionResume(context.Background(), req)
	if err != nil {
		t.Fatalf("handleSessionResume returned error: %v", err)
	}
	if !resp.OK {
		t.Fatalf("resume response not ok: %+v", resp.Error)
	}
	waitForRuntimeReconcileCalls(t, recorder, 2)
	if daemon.runtimeReconcileQueue != nil {
		t.Cleanup(func() { _ = daemon.runtimeReconcileQueue.Close() })
	}

	calls, projectIDs := recorder.snapshot()
	if calls != 2 {
		t.Fatalf("runtime reconcile calls = %d, want 2", calls)
	}
	for _, gotProjectID := range projectIDs {
		if gotProjectID != projectID {
			t.Fatalf("runtime reconcile project ids = %v, want only %s", projectIDs, projectID)
		}
	}
	issueCalls := recorder.issueSnapshot()
	if len(issueCalls) != 2 {
		t.Fatalf("runtime reconcile issue calls = %v, want two calls", issueCalls)
	}
	for _, gotIssueIDs := range issueCalls {
		if len(gotIssueIDs) != 1 || gotIssueIDs[0] != issueID {
			t.Fatalf("runtime reconcile issue ids = %v, want only %s", issueCalls, issueID)
		}
	}
}

func waitForRuntimeReconcileCalls(t *testing.T, recorder *runtimeReconcileRecorder, want int) {
	t.Helper()
	deadline := time.After(time.Second)
	for {
		calls, _ := recorder.snapshot()
		if calls >= want {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("timed out waiting for runtime reconcile calls >= %d; got %d", want, calls)
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func TestSessionPauseResumeRejectMissingExplicitRuntimeTarget(t *testing.T) {
	const (
		projectID = "proj"
		issueID   = "az-1"
	)
	canonicalSessionID := naming.CanonicalSessionID(projectID, issueID)
	staleSessionID := issueID + ".pane-190"
	tests := []struct {
		name      string
		command   string
		initial   daemonstate.SessionState
		wantState daemonstate.SessionState
		handle    func(*Daemon, context.Context, protocol.RequestEnvelope) (protocol.ResponseEnvelope, error)
	}{
		{
			name:      "pause",
			command:   daemonhandlers.CommandSessionPause,
			initial:   daemonstate.SessionStateAttached,
			wantState: daemonstate.SessionStateAttached,
			handle:    (*Daemon).handleSessionPause,
		},
		{
			name:      "resume",
			command:   daemonhandlers.CommandSessionResume,
			initial:   daemonstate.SessionStatePaused,
			wantState: daemonstate.SessionStatePaused,
			handle:    (*Daemon).handleSessionResume,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmuxRunner := newTestTmuxRunner(canonicalSessionID)
			close(tmuxRunner.killRelease)
			store := daemonstate.NewStore()
			if _, err := store.UpsertSession(projectID, staleSessionID, issueID, tt.initial); err != nil {
				t.Fatalf("seed stale session: %v", err)
			}
			recorder := &runtimeReconcileRecorder{}
			daemon := &Daemon{
				cfg: Config{
					RepoDir: ".",
					Logger:  slog.Default(),
				},
				tmux:              tmux.NewClient(tmuxRunner, slog.Default()),
				session:           daemonhandlers.NewSessionHandler(store),
				sessionStore:      store,
				runtimeReconciler: recorder,
			}

			resp, err := tt.handle(daemon, context.Background(), protocol.RequestEnvelope{
				ProtocolVersion: protocol.CurrentVersion,
				RequestID:       "req-stale-lifecycle-target",
				Kind:            protocol.EnvelopeKindCommand,
				Command:         tt.command,
				Meta: protocol.Metadata{
					ProjectID: naming.ProjectID(projectID),
				},
				Body: marshalJSON(map[string]string{
					"project_id": projectID,
					"issue_id":   issueID,
					"session_id": staleSessionID,
				}),
			})
			if err != nil {
				t.Fatalf("handler returned error: %v", err)
			}
			if resp.OK {
				t.Fatalf("response ok, want missing target error")
			}
			if resp.Error == nil || resp.Error.Code != protocol.ErrorCodeInvalidRequest {
				t.Fatalf("response error = %+v, want invalid request", resp.Error)
			}
			snapshot := store.ReadSnapshot(projectID)
			got := snapshot.Sessions[staleSessionID]
			if got.State != tt.wantState {
				t.Fatalf("stale session state = %s, want %s", got.State, tt.wantState)
			}
			if got := store.CurrentRevision(projectID); got != 1 {
				t.Fatalf("store revision = %d, want unchanged revision 1", got)
			}
			calls, _ := recorder.snapshot()
			if calls != 0 {
				t.Fatalf("runtime reconcile calls = %d, want 0", calls)
			}
		})
	}
}

func TestSessionPauseResumeAgentScopedTargetWritesHookActivity(t *testing.T) {
	const (
		projectID = "proj"
		issueID   = "az-1"
	)
	canonicalSessionID := naming.CanonicalSessionID(projectID, issueID)
	agentSessionID := canonicalSessionID + ".pane-190"
	tmuxRunner := newTestTmuxRunner(canonicalSessionID)
	close(tmuxRunner.killRelease)
	store := daemonstate.NewStore()
	if _, err := store.UpsertSession(projectID, canonicalSessionID, issueID, daemonstate.SessionStateAttached); err != nil {
		t.Fatalf("seed canonical session: %v", err)
	}
	runtimeStateStore := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(t.TempDir(), "runtime.db"), slog.Default())
	t.Cleanup(func() {
		_ = runtimeStateStore.Close()
	})
	daemon := &Daemon{
		cfg: Config{
			RepoDir: ".",
			Logger:  slog.Default(),
		},
		tmux:         tmux.NewClient(tmuxRunner, slog.Default()),
		session:      daemonhandlers.NewSessionHandler(store),
		sessionStore: store,
		runtimeStoresByRoot: map[string]*daemonstate.RuntimeStateStore{
			".": runtimeStateStore,
		},
	}

	resp, err := daemon.handleSessionPause(context.Background(), protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-agent-pause",
		Kind:            protocol.EnvelopeKindCommand,
		Command:         daemonhandlers.CommandSessionPause,
		Meta: protocol.Metadata{
			ProjectID: naming.ProjectID(projectID),
		},
		Body: marshalJSON(map[string]string{
			"project_id": projectID,
			"issue_id":   issueID,
			"session_id": agentSessionID,
		}),
	})
	if err != nil {
		t.Fatalf("handleSessionPause returned error: %v", err)
	}
	if !resp.OK {
		t.Fatalf("pause response not ok: %+v", resp.Error)
	}
	row, found, err := runtimeStateStore.GetSessionState(context.Background(), projectID, agentSessionID)
	if err != nil {
		t.Fatalf("get agent session runtime state: %v", err)
	}
	if !found {
		t.Fatal("agent session runtime state not found")
	}
	if row.State != daemonstate.SessionStatePaused {
		t.Fatalf("agent session state = %s, want %s", row.State, daemonstate.SessionStatePaused)
	}
	if row.Activity != "idle" || row.ActivitySource != "hooks" {
		t.Fatalf("agent session activity = %s/%s, want idle/hooks", row.Activity, row.ActivitySource)
	}

	resp, err = daemon.handleSessionResume(context.Background(), protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-agent-resume",
		Kind:            protocol.EnvelopeKindCommand,
		Command:         daemonhandlers.CommandSessionResume,
		Meta: protocol.Metadata{
			ProjectID: naming.ProjectID(projectID),
		},
		Body: marshalJSON(map[string]string{
			"project_id": projectID,
			"issue_id":   issueID,
			"session_id": agentSessionID,
		}),
	})
	if err != nil {
		t.Fatalf("handleSessionResume returned error: %v", err)
	}
	if !resp.OK {
		t.Fatalf("resume response not ok: %+v", resp.Error)
	}
	row, found, err = runtimeStateStore.GetSessionState(context.Background(), projectID, agentSessionID)
	if err != nil {
		t.Fatalf("get resumed agent session runtime state: %v", err)
	}
	if !found {
		t.Fatal("resumed agent session runtime state not found")
	}
	if row.State != daemonstate.SessionStateRunning {
		t.Fatalf("agent session state = %s, want %s", row.State, daemonstate.SessionStateRunning)
	}
	if row.Activity != "busy" || row.ActivitySource != "hooks" {
		t.Fatalf("agent session activity = %s/%s, want busy/hooks", row.Activity, row.ActivitySource)
	}
}

func TestSessionPauseResumeAgentScopedTargetRefreshesHookActivityWhenLifecycleUnchanged(t *testing.T) {
	const (
		projectID = "proj"
		issueID   = "az-1"
	)
	tests := []struct {
		name          string
		command       string
		state         daemonstate.SessionState
		staleActivity string
		staleSource   string
		wantActivity  string
		handle        func(*Daemon, context.Context, protocol.RequestEnvelope) (protocol.ResponseEnvelope, error)
	}{
		{
			name:          "pause clears stale busy",
			command:       daemonhandlers.CommandSessionPause,
			state:         daemonstate.SessionStatePaused,
			staleActivity: "busy",
			staleSource:   "hooks",
			wantActivity:  "idle",
			handle:        (*Daemon).handleSessionPause,
		},
		{
			name:          "resume clears stale idle",
			command:       daemonhandlers.CommandSessionResume,
			state:         daemonstate.SessionStateRunning,
			staleActivity: "idle",
			staleSource:   "hooks",
			wantActivity:  "busy",
			handle:        (*Daemon).handleSessionResume,
		},
		{
			name:          "pause restores missing hook source",
			command:       daemonhandlers.CommandSessionPause,
			state:         daemonstate.SessionStatePaused,
			staleActivity: "idle",
			wantActivity:  "idle",
			handle:        (*Daemon).handleSessionPause,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			canonicalSessionID := naming.CanonicalSessionID(projectID, issueID)
			agentSessionID := canonicalSessionID + ".pane-190"
			tmuxRunner := newTestTmuxRunner(canonicalSessionID)
			close(tmuxRunner.killRelease)
			store := daemonstate.NewStore()
			now := time.Now().UTC()
			store.ReplaceProjectSessions(projectID, []daemonstate.Session{
				{
					ID:            canonicalSessionID,
					IssueID:       issueID,
					State:         daemonstate.SessionStateRunning,
					ObservedState: daemonstate.SessionStateRunning,
					Activity:      "busy",
					StartedAt:     &now,
					UpdatedAt:     now,
				},
				{
					ID:             agentSessionID,
					IssueID:        issueID,
					State:          tt.state,
					ObservedState:  tt.state,
					Activity:       tt.staleActivity,
					ActivitySource: tt.staleSource,
					StartedAt:      &now,
					UpdatedAt:      now,
				},
			})
			runtimeStateStore := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(t.TempDir(), "runtime.db"), slog.Default())
			t.Cleanup(func() {
				_ = runtimeStateStore.Close()
			})
			if err := upsertSessionStateFixture(runtimeStateStore, context.Background(), projectID, daemonstate.Session{
				ID:             agentSessionID,
				IssueID:        issueID,
				State:          tt.state,
				ObservedState:  tt.state,
				Activity:       tt.staleActivity,
				ActivitySource: tt.staleSource,
				StartedAt:      &now,
				UpdatedAt:      now,
			}); err != nil {
				t.Fatalf("seed stale agent runtime state: %v", err)
			}
			daemon := &Daemon{
				cfg: Config{
					RepoDir: ".",
					Logger:  slog.Default(),
				},
				tmux:         tmux.NewClient(tmuxRunner, slog.Default()),
				session:      daemonhandlers.NewSessionHandler(store),
				sessionStore: store,
				runtimeStoresByRoot: map[string]*daemonstate.RuntimeStateStore{
					".": runtimeStateStore,
				},
			}

			resp, err := tt.handle(daemon, context.Background(), protocol.RequestEnvelope{
				ProtocolVersion: protocol.CurrentVersion,
				RequestID:       "req-agent-lifecycle-unchanged",
				Kind:            protocol.EnvelopeKindCommand,
				Command:         tt.command,
				Meta: protocol.Metadata{
					ProjectID: naming.ProjectID(projectID),
				},
				Body: marshalJSON(map[string]string{
					"project_id": projectID,
					"issue_id":   issueID,
					"session_id": agentSessionID,
				}),
			})
			if err != nil {
				t.Fatalf("handler returned error: %v", err)
			}
			if !resp.OK {
				t.Fatalf("response not ok: %+v", resp.Error)
			}
			row, found, err := runtimeStateStore.GetSessionState(context.Background(), projectID, agentSessionID)
			if err != nil {
				t.Fatalf("get agent session runtime state: %v", err)
			}
			if !found {
				t.Fatal("agent session runtime state not found")
			}
			if row.Activity != tt.wantActivity || row.ActivitySource != "hooks" {
				t.Fatalf("agent session activity = %s/%s, want %s/hooks", row.Activity, row.ActivitySource, tt.wantActivity)
			}
		})
	}
}

func waitForSessionState(t *testing.T, store *daemonstate.Store, projectID, sessionID string, want daemonstate.SessionState) {
	t.Helper()
	deadline := time.After(time.Second)
	for {
		session, err := store.Session(projectID, sessionID)
		if err == nil && session.State == want {
			return
		}
		select {
		case <-deadline:
			if err != nil {
				t.Fatalf("timed out waiting for session %s state %s: %v", sessionID, want, err)
			}
			t.Fatalf("timed out waiting for session %s state %s; got %s", sessionID, want, session.State)
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func TestSessionPauseResumeSkipRuntimeReconcileWhenLifecycleUnchanged(t *testing.T) {
	const (
		projectID = "proj"
		issueID   = "az-1"
	)
	sessionID := naming.CanonicalSessionID(projectID, issueID)
	tests := []struct {
		name    string
		state   daemonstate.SessionState
		command string
		handle  func(*Daemon, context.Context, protocol.RequestEnvelope) (protocol.ResponseEnvelope, error)
	}{
		{
			name:    "pause already paused",
			state:   daemonstate.SessionStatePaused,
			command: daemonhandlers.CommandSessionPause,
			handle:  (*Daemon).handleSessionPause,
		},
		{
			name:    "resume already attached",
			state:   daemonstate.SessionStateAttached,
			command: daemonhandlers.CommandSessionResume,
			handle:  (*Daemon).handleSessionResume,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := daemonstate.NewStore()
			if _, err := store.UpsertSession(projectID, sessionID, issueID, tt.state); err != nil {
				t.Fatalf("seed session: %v", err)
			}
			recorder := &runtimeReconcileRecorder{}
			daemon := &Daemon{
				cfg: Config{
					RepoDir: ".",
					Logger:  slog.Default(),
				},
				session:           daemonhandlers.NewSessionHandler(store),
				sessionStore:      store,
				runtimeReconciler: recorder,
			}

			resp, err := tt.handle(daemon, context.Background(), protocol.RequestEnvelope{
				ProtocolVersion: protocol.CurrentVersion,
				RequestID:       "req-noop-lifecycle",
				Kind:            protocol.EnvelopeKindCommand,
				Command:         tt.command,
				Meta: protocol.Metadata{
					ProjectID: naming.ProjectID(projectID),
				},
				Body: marshalJSON(map[string]string{
					"project_id": projectID,
					"session_id": issueID,
				}),
			})
			if err != nil {
				t.Fatalf("handler returned error: %v", err)
			}
			if !resp.OK {
				t.Fatalf("response not ok: %+v", resp.Error)
			}
			if got := store.CurrentRevision(projectID); got != 1 {
				t.Fatalf("store revision = %d, want unchanged revision 1", got)
			}
			calls, projectIDs := recorder.snapshot()
			if calls != 0 {
				t.Fatalf("runtime reconcile calls = %d, want 0", calls)
			}
			if len(projectIDs) != 0 {
				t.Fatalf("runtime reconcile project ids = %v, want none", projectIDs)
			}
		})
	}
}

func TestHandleSessionStopDirectDoesNotWaitForRuntimeFreshness(t *testing.T) {
	const (
		projectID = "proj"
		issueID   = "az-1"
	)
	sessionID := naming.CanonicalSessionID(projectID, issueID)
	tmuxRunner := newTestTmuxRunner(sessionID)
	close(tmuxRunner.killRelease)
	store := daemonstate.NewStore()
	if _, err := store.UpsertSession(projectID, sessionID, issueID, daemonstate.SessionStateAttached); err != nil {
		t.Fatalf("seed session store: %v", err)
	}
	recorder := &timeoutRuntimeReconciler{}

	daemon := &Daemon{
		cfg: Config{
			RepoDir:                 ".",
			Logger:                  slog.Default(),
			RuntimeReconcileTimeout: time.Hour,
		},
		tmux:              tmux.NewClient(tmuxRunner, slog.Default()),
		session:           daemonhandlers.NewSessionHandler(store),
		sessionStore:      store,
		runtimeReconciler: recorder,
	}
	t.Cleanup(func() {
		if daemon.runtimeReconcileQueue != nil {
			_ = daemon.runtimeReconcileQueue.Close()
		}
	})

	attachIsolatedRuntimeStore(t, daemon, projectID)
	resp, err := daemon.handleSessionStopDirect(context.Background(), protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-stop-fallback",
		Kind:            protocol.EnvelopeKindCommand,
		Command:         daemonhandlers.CommandSessionStop,
		Meta: protocol.Metadata{
			ProjectID: naming.ProjectID(projectID),
		},
		Body: marshalJSON(map[string]string{
			"project_id": projectID,
			"session_id": issueID,
		}),
	})
	if err != nil {
		t.Fatalf("handleSessionStopDirect returned error: %v", err)
	}
	if !resp.OK {
		t.Fatalf("response not ok: %+v", resp.Error)
	}

	calls, projectIDs := recorder.snapshot()
	if calls != 0 {
		t.Fatalf("runtime reconcile calls = %d (%v), want 0 for lightweight stop", calls, projectIDs)
	}
}

func TestHandleSessionStopDirectStopsTmuxWhenRuntimeFreshnessWouldTimeout(t *testing.T) {
	const (
		projectID = "proj"
		issueID   = "az-1"
	)
	sessionID := naming.CanonicalSessionID(projectID, issueID)
	tmuxRunner := newTestTmuxRunner(sessionID)
	close(tmuxRunner.killRelease)
	store := daemonstate.NewStore()
	if _, err := store.UpsertSession(projectID, sessionID, issueID, daemonstate.SessionStateAttached); err != nil {
		t.Fatalf("seed session store: %v", err)
	}
	recorder := &timeoutRuntimeReconciler{}

	daemon := &Daemon{
		cfg: Config{
			RepoDir:                 ".",
			Logger:                  slog.Default(),
			RuntimeReconcileTimeout: 20 * time.Millisecond,
		},
		tmux:              tmux.NewClient(tmuxRunner, slog.Default()),
		session:           daemonhandlers.NewSessionHandler(store),
		sessionStore:      store,
		runtimeReconciler: recorder,
	}
	t.Cleanup(func() {
		if daemon.runtimeReconcileQueue != nil {
			_ = daemon.runtimeReconcileQueue.Close()
		}
	})

	attachIsolatedRuntimeStore(t, daemon, projectID)
	resp, err := daemon.handleSessionStopDirect(context.Background(), protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-stop-timeout-continue",
		Kind:            protocol.EnvelopeKindCommand,
		Command:         daemonhandlers.CommandSessionStop,
		Meta: protocol.Metadata{
			ProjectID: naming.ProjectID(projectID),
		},
		Body: marshalJSON(map[string]string{
			"project_id": projectID,
			"session_id": issueID,
		}),
	})
	if err != nil {
		t.Fatalf("handleSessionStopDirect returned error: %v", err)
	}
	if !resp.OK {
		t.Fatalf("response not ok: %+v", resp.Error)
	}

	if tmuxRunner.hasSession(sessionID) {
		t.Fatalf("expected tmux session %q to be killed", sessionID)
	}
	snapshot := store.ReadSnapshot(projectID)
	if got := snapshot.Sessions[sessionID].State; got != daemonstate.SessionStateStopped {
		t.Fatalf("session state = %s, want %s", got, daemonstate.SessionStateStopped)
	}

	calls, projectIDs := recorder.snapshot()
	if calls != 0 {
		t.Fatalf("runtime reconcile calls = %d (%v), want 0 for lightweight stop", calls, projectIDs)
	}
}

func TestHandleSessionStopDirectPostKillRefreshIsIssueScoped(t *testing.T) {
	const (
		projectID    = "proj"
		issueID      = "az-1"
		otherIssueID = "az-2"
	)
	sessionID := naming.CanonicalSessionID(projectID, issueID)
	otherSessionID := naming.CanonicalSessionID(projectID, otherIssueID)
	tmuxRunner := newTestTmuxRunner(sessionID)
	close(tmuxRunner.killRelease)
	store := daemonstate.NewStore()
	if _, err := store.UpsertSession(projectID, sessionID, issueID, daemonstate.SessionStateAttached); err != nil {
		t.Fatalf("seed target session store: %v", err)
	}
	if _, err := store.UpsertSession(projectID, otherSessionID, otherIssueID, daemonstate.SessionStateAttached); err != nil {
		t.Fatalf("seed other session store: %v", err)
	}
	runtimeStateStore := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(t.TempDir(), "runtime.db"), slog.Default())
	t.Cleanup(func() {
		_ = runtimeStateStore.Close()
	})
	for _, seed := range []daemonstate.Session{
		{ID: sessionID, IssueID: issueID, State: daemonstate.SessionStateAttached, ObservedState: daemonstate.SessionStateAttached, UpdatedAt: time.Now().UTC()},
		{ID: otherSessionID, IssueID: otherIssueID, State: daemonstate.SessionStateAttached, ObservedState: daemonstate.SessionStateAttached, UpdatedAt: time.Now().UTC()},
	} {
		if err := upsertSessionStateFixture(runtimeStateStore, context.Background(), projectID, seed); err != nil {
			t.Fatalf("seed runtime state %s: %v", seed.IssueID, err)
		}
	}

	daemon := &Daemon{
		cfg: Config{
			RepoDir: ".",
			Logger:  slog.Default(),
		},
		tmux:         tmux.NewClient(tmuxRunner, slog.Default()),
		session:      daemonhandlers.NewSessionHandler(store),
		sessionStore: store,
		runtimeStoresByRoot: map[string]*daemonstate.RuntimeStateStore{
			".": runtimeStateStore,
		},
	}

	resp, err := daemon.handleSessionStopDirect(context.Background(), protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-stop-issue-scoped-refresh",
		Kind:            protocol.EnvelopeKindCommand,
		Command:         daemonhandlers.CommandSessionStop,
		Meta: protocol.Metadata{
			ProjectID: naming.ProjectID(projectID),
		},
		Body: marshalJSON(map[string]string{
			"project_id": projectID,
			"session_id": issueID,
		}),
	})
	if err != nil {
		t.Fatalf("handleSessionStopDirect returned error: %v", err)
	}
	if !resp.OK {
		t.Fatalf("response not ok: %+v", resp.Error)
	}

	target, found, err := runtimeStateStore.GetSessionStateByIssueID(context.Background(), projectID, issueID)
	if err != nil {
		t.Fatalf("get target session state: %v", err)
	}
	if !found {
		t.Fatal("target runtime session state not found")
	}
	if target.State != daemonstate.SessionStateStopped || target.ObservedState != daemonstate.SessionStateStopped {
		t.Fatalf("target runtime state = desired %s observed %s, want stopped/stopped", target.State, target.ObservedState)
	}
	other, found, err := runtimeStateStore.GetSessionStateByIssueID(context.Background(), projectID, otherIssueID)
	if err != nil {
		t.Fatalf("get other session state: %v", err)
	}
	if !found {
		t.Fatal("other runtime session state not found")
	}
	if other.State != daemonstate.SessionStateAttached || other.ObservedState != daemonstate.SessionStateAttached {
		t.Fatalf("other runtime state = desired %s observed %s, want attached/attached", other.State, other.ObservedState)
	}
}

func TestHandleSessionStopDirectKillsLiveProjectedIssueSessions(t *testing.T) {
	const (
		projectID = "proj"
		issueID   = "ciw"
	)
	sessionID := naming.CanonicalSessionID(projectID, issueID)
	paneSessionID := issueID + ".pane-190"
	otherSessionID := naming.CanonicalSessionID(projectID, "cix")

	tmuxRunner := &testTmuxRunner{
		sessions: map[string]bool{
			sessionID:      true,
			paneSessionID:  true,
			otherSessionID: true,
		},
		killEntered: make(chan struct{}),
		killRelease: make(chan struct{}),
	}
	close(tmuxRunner.killRelease)

	store := daemonstate.NewStore()
	if _, err := store.UpsertSession(projectID, sessionID, issueID, daemonstate.SessionStateAttached); err != nil {
		t.Fatalf("seed session store: %v", err)
	}
	runtimeStateStore := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(t.TempDir(), "runtime.db"), slog.Default())
	t.Cleanup(func() {
		_ = runtimeStateStore.Close()
	})
	for _, seed := range []daemonstate.Session{
		{ID: sessionID, IssueID: issueID, State: daemonstate.SessionStateAttached, ObservedState: daemonstate.SessionStateAttached, UpdatedAt: time.Now().UTC()},
		{ID: paneSessionID, IssueID: issueID, State: daemonstate.SessionStateAttached, ObservedState: daemonstate.SessionStateAttached, UpdatedAt: time.Now().UTC()},
		{ID: otherSessionID, IssueID: "cix", State: daemonstate.SessionStateAttached, ObservedState: daemonstate.SessionStateAttached, UpdatedAt: time.Now().UTC()},
	} {
		if err := upsertSessionStateFixture(runtimeStateStore, context.Background(), projectID, seed); err != nil {
			t.Fatalf("seed runtime state %s: %v", seed.ID, err)
		}
	}

	daemon := &Daemon{
		cfg: Config{
			RepoDir: ".",
			Logger:  slog.Default(),
		},
		tmux:         tmux.NewClient(tmuxRunner, slog.Default()),
		session:      daemonhandlers.NewSessionHandler(store),
		sessionStore: store,
		runtimeStoresByRoot: map[string]*daemonstate.RuntimeStateStore{
			".": runtimeStateStore,
		},
	}

	resp, err := daemon.handleSessionStopDirect(context.Background(), protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-stop-live-projected-sessions",
		Kind:            protocol.EnvelopeKindCommand,
		Command:         daemonhandlers.CommandSessionStop,
		Meta: protocol.Metadata{
			ProjectID: naming.ProjectID(projectID),
		},
		Body: marshalJSON(map[string]string{
			"project_id": projectID,
			"session_id": issueID,
		}),
	})
	if err != nil {
		t.Fatalf("handleSessionStopDirect returned error: %v", err)
	}
	if !resp.OK {
		t.Fatalf("response not ok: %+v", resp.Error)
	}

	for _, killed := range []string{sessionID, paneSessionID} {
		if tmuxRunner.hasSession(killed) {
			t.Fatalf("expected tmux session %q to be killed", killed)
		}
	}
	if !tmuxRunner.hasSession(otherSessionID) {
		t.Fatalf("expected unrelated tmux session %q to remain", otherSessionID)
	}

	rows, err := runtimeStateStore.ListSessionStates(context.Background(), projectID)
	if err != nil {
		t.Fatalf("list session states: %v", err)
	}
	stoppedByID := map[string]bool{}
	for _, row := range rows {
		if row.IssueID == issueID {
			if row.State != daemonstate.SessionStateStopped || row.ObservedState != daemonstate.SessionStateStopped {
				t.Fatalf("runtime row %s = desired %s observed %s, want stopped/stopped", row.ID, row.State, row.ObservedState)
			}
			stoppedByID[row.ID] = true
		}
		if row.ID == otherSessionID && (row.State != daemonstate.SessionStateAttached || row.ObservedState != daemonstate.SessionStateAttached) {
			t.Fatalf("unrelated runtime row = desired %s observed %s, want attached/attached", row.State, row.ObservedState)
		}
	}
	for _, want := range []string{sessionID, paneSessionID} {
		if !stoppedByID[want] {
			t.Fatalf("missing stopped runtime row for %s; rows=%+v", want, rows)
		}
	}
}

func TestRefreshStoppedSessionRuntimeStateWritesCanonicalParentForPaneOnlyMatch(t *testing.T) {
	const (
		projectID = "proj"
		issueID   = "ciw"
	)
	sessionID := naming.CanonicalSessionID(projectID, issueID)
	paneSessionID := sessionID + ".pane-190"

	store := daemonstate.NewStore()
	runtimeStateStore := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(t.TempDir(), "runtime.db"), slog.Default())
	t.Cleanup(func() {
		_ = runtimeStateStore.Close()
	})
	if err := upsertSessionStateFixture(runtimeStateStore, context.Background(), projectID, daemonstate.Session{
		ID:            paneSessionID,
		IssueID:       issueID,
		State:         daemonstate.SessionStateAttached,
		ObservedState: daemonstate.SessionStateStopped,
		UpdatedAt:     time.Now().UTC(),
	}); err != nil {
		t.Fatalf("seed pane runtime state: %v", err)
	}

	daemon := &Daemon{
		cfg: Config{
			RepoDir: ".",
			Logger:  slog.Default(),
		},
		session:      daemonhandlers.NewSessionHandler(store),
		sessionStore: store,
		runtimeStoresByRoot: map[string]*daemonstate.RuntimeStateStore{
			".": runtimeStateStore,
		},
	}

	if err := daemon.refreshStoppedSessionRuntimeState(context.Background(), projectID, issueID, []string{paneSessionID}); err != nil {
		t.Fatalf("refresh stopped runtime state: %v", err)
	}

	rows, err := runtimeStateStore.ListSessionStates(context.Background(), projectID)
	if err != nil {
		t.Fatalf("list session states: %v", err)
	}
	stoppedByID := map[string]bool{}
	for _, row := range rows {
		if row.State != daemonstate.SessionStateStopped || row.ObservedState != daemonstate.SessionStateStopped {
			t.Fatalf("runtime row %s = desired %s observed %s, want stopped/stopped", row.ID, row.State, row.ObservedState)
		}
		stoppedByID[row.ID] = true
	}
	for _, want := range []string{sessionID, paneSessionID} {
		if !stoppedByID[want] {
			t.Fatalf("missing stopped runtime row for %s; rows=%+v", want, rows)
		}
	}
}

func TestHandleSessionStopDirectCanceledContextDoesNotContinueAfterFreshnessFailure(t *testing.T) {
	const (
		projectID = "proj"
		issueID   = "az-1"
	)
	sessionID := naming.CanonicalSessionID(projectID, issueID)
	tmuxRunner := newTestTmuxRunner(sessionID)
	close(tmuxRunner.killRelease)
	store := daemonstate.NewStore()
	if _, err := store.UpsertSession(projectID, sessionID, issueID, daemonstate.SessionStateAttached); err != nil {
		t.Fatalf("seed session store: %v", err)
	}
	recorder := &timeoutRuntimeReconciler{}

	daemon := &Daemon{
		cfg: Config{
			RepoDir:                 ".",
			Logger:                  slog.Default(),
			RuntimeReconcileTimeout: 20 * time.Millisecond,
		},
		tmux:              tmux.NewClient(tmuxRunner, slog.Default()),
		session:           daemonhandlers.NewSessionHandler(store),
		sessionStore:      store,
		runtimeReconciler: recorder,
	}
	t.Cleanup(func() {
		if daemon.runtimeReconcileQueue != nil {
			_ = daemon.runtimeReconcileQueue.Close()
		}
	})

	attachIsolatedRuntimeStore(t, daemon, projectID)
	canceledCtx, cancel := context.WithCancel(context.Background())
	cancel()
	resp, err := daemon.handleSessionStopDirect(canceledCtx, protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-stop-canceled-no-continue",
		Kind:            protocol.EnvelopeKindCommand,
		Command:         daemonhandlers.CommandSessionStop,
		Meta: protocol.Metadata{
			ProjectID: naming.ProjectID(projectID),
		},
		Body: marshalJSON(map[string]string{
			"project_id": projectID,
			"session_id": issueID,
		}),
	})
	if err != nil {
		t.Fatalf("handleSessionStopDirect returned error: %v", err)
	}
	if resp.OK {
		t.Fatalf("response unexpectedly ok")
	}

	if !tmuxRunner.hasSession(sessionID) {
		t.Fatalf("expected tmux session %q to remain", sessionID)
	}
	snapshot := store.ReadSnapshot(projectID)
	if got := snapshot.Sessions[sessionID].State; got != daemonstate.SessionStateRunning {
		t.Fatalf("session state = %s, want %s", got, daemonstate.SessionStateRunning)
	}

	calls, projectIDs := recorder.snapshot()
	if calls != 0 {
		t.Fatalf("runtime reconcile calls = %d (%v), want 0 when request is already canceled", calls, projectIDs)
	}
}

func TestApplySessionLifecycleTransitionPublishesProjectionEvent(t *testing.T) {
	const (
		projectID = "proj"
		sessionID = "sess-1"
		issueID   = "az-1"
	)

	store := daemonstate.NewStore()
	runtimeStateStore := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(t.TempDir(), "projection.db"), slog.Default())
	t.Cleanup(func() {
		_ = runtimeStateStore.Close()
	})
	if err := upsertSessionStateFixture(runtimeStateStore, context.Background(), projectID, daemonstate.Session{
		ID:            sessionID,
		IssueID:       issueID,
		State:         daemonstate.SessionStateStopped,
		ObservedState: daemonstate.SessionStateStopped,
		UpdatedAt:     time.Now().UTC(),
	}); err != nil {
		t.Fatalf("seed runtime session projection: %v", err)
	}
	daemon := &Daemon{
		cfg:          Config{RepoDir: ".", Logger: slog.Default()},
		session:      daemonhandlers.NewSessionHandler(store),
		sessionStore: store,
		hub:          publish.NewHub(32, 8, slog.Default()),
		runtimeStoresByRoot: map[string]*daemonstate.RuntimeStateStore{
			".": runtimeStateStore,
		},
	}

	ch, cancel := daemon.hub.Subscribe(projectID, 0)
	defer cancel()

	req := protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-start",
		Kind:            protocol.EnvelopeKindCommand,
		Meta: protocol.Metadata{
			ProjectID: naming.ProjectID(projectID),
		},
	}
	if err := daemon.applySessionLifecycleTransition(context.Background(), req, projectID, sessionID, issueID, daemonhandlers.CommandSessionStart); err != nil {
		t.Fatalf("apply session lifecycle transition: %v", err)
	}

	evt := collectSessionProjectionEvents(t, ch, 1)[0]
	if evt.Event != protocol.EventSessionUpdated {
		t.Fatalf("event = %s, want %s", evt.Event, protocol.EventSessionUpdated)
	}
	if evt.Revision != 1 {
		t.Fatalf("revision = %d, want 1", evt.Revision)
	}
	var body protocol.SessionProjectionEventBody
	if err := json.Unmarshal(evt.Body, &body); err != nil {
		t.Fatalf("unmarshal session event body: %v", err)
	}
	if body.ProjectID != projectID || body.Revision != 1 {
		t.Fatalf("body = %+v, want project/revision %s/1", body, projectID)
	}
	if body.Session.SessionID != sessionID || body.Session.IssueID != issueID {
		t.Fatalf("session body = %+v, want session/issue %s/%s", body.Session, sessionID, issueID)
	}
	if body.Session.State != protocol.SessionLifecycleStateStarting {
		t.Fatalf("session state = %s, want %s", body.Session.State, protocol.SessionLifecycleStateStarting)
	}
	if body.Runtime == nil {
		t.Fatal("expected runtime projection delta")
	}
	if body.Runtime.ProjectID != projectID || body.Runtime.Revision != evt.Revision {
		t.Fatalf("runtime envelope = %+v, want project/revision %s/%d", body.Runtime, projectID, evt.Revision)
	}
	if body.Runtime.Projection.IssueID != issueID || body.Runtime.Projection.Session.SessionID != sessionID {
		t.Fatalf("runtime projection = %+v, want issue/session %s/%s", body.Runtime.Projection, issueID, sessionID)
	}
	if body.Runtime.Projection.Session.State != protocol.SessionLifecycleStateStopped {
		t.Fatalf("runtime session state = %s, want observed %s", body.Runtime.Projection.Session.State, protocol.SessionLifecycleStateStopped)
	}
	if body.Runtime.Projection.Session.HasSession {
		t.Fatalf("runtime session = %+v, want stopped session inactive", body.Runtime.Projection.Session)
	}
}

func TestApplySessionLifecycleTransitionPreservesTypedIdentity(t *testing.T) {
	for _, tc := range []struct {
		name, sessionID, issueHint string
		projection                 daemonstate.Session
	}{
		{name: "advisor", sessionID: "advisor-request-1", issueHint: "request-1", projection: daemonstate.Session{ID: "advisor-request-1", IssueID: "issue-1", Role: daemonstate.SessionRoleAdvisor, ScopeKind: daemonstate.SessionScopeInteraction, ScopeID: "request-1", State: daemonstate.SessionStateStopped}},
		{name: "rooted orchestrator", sessionID: "az-root", issueHint: "root", projection: daemonstate.Session{ID: "az-root", IssueID: "root", Role: daemonstate.SessionRoleOrchestrator, ScopeKind: daemonstate.SessionScopeOrchestration, ScopeID: "root", State: daemonstate.SessionStateStopped}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			transient := daemonstate.NewStore()
			runtimeStore := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(t.TempDir(), "runtime.db"), slog.Default())
			t.Cleanup(func() { _ = runtimeStore.Close() })
			tc.projection.UpdatedAt = time.Now().UTC()
			if err := upsertSessionStateFixture(runtimeStore, ctx, "p", tc.projection); err != nil {
				t.Fatal(err)
			}
			d := &Daemon{cfg: Config{RepoDir: ".", Logger: slog.Default()}, sessionStore: transient, session: daemonhandlers.NewSessionHandler(transient), hub: publish.NewHub(8, 8, slog.Default()), runtimeStoresByRoot: map[string]*daemonstate.RuntimeStateStore{".": runtimeStore}}
			ch, cancel := d.hub.Subscribe("p", 0)
			defer cancel()
			req := protocol.RequestEnvelope{ProtocolVersion: protocol.CurrentVersion, RequestID: "typed", Kind: protocol.EnvelopeKindCommand, Meta: protocol.Metadata{ProjectID: "p"}}
			selector := sessionIntentSelector{Role: tc.projection.Role, ScopeKind: tc.projection.ScopeKind, ScopeID: tc.projection.ScopeID}
			if err := d.applyTypedSessionLifecycleTransition(ctx, req, "p", tc.sessionID, tc.issueHint, daemonhandlers.CommandSessionStart, "", "", selector); err != nil {
				t.Fatal(err)
			}
			rows, err := runtimeStore.ListSessionIntentStates(ctx, "p")
			if err != nil || len(rows) != 1 {
				t.Fatalf("rows=%+v err=%v", rows, err)
			}
			got := rows[0]
			if got.IssueID != tc.projection.IssueID || got.Role != tc.projection.Role || got.ScopeKind != tc.projection.ScopeKind || got.ScopeID != tc.projection.ScopeID {
				t.Fatalf("typed identity lost: %+v want %+v", got, tc.projection)
			}
			events := collectSessionProjectionEvents(t, ch, 1)
			if len(events) != 1 || events[0].Event != protocol.EventSessionUpdated {
				t.Fatalf("publication lost: %+v", events)
			}
		})
	}
}

func TestTypedLifecycleTransitionTargetsExclusivePhysicalRuntimeIntent(t *testing.T) {
	ctx := context.Background()
	sharedID, issueID := "az-root", "root"
	worker := daemonstate.Session{ID: sharedID, IssueID: issueID, Role: daemonstate.SessionRoleWorker, ScopeKind: daemonstate.SessionScopeIssue, ScopeID: issueID, State: daemonstate.SessionStateStopped}
	rooted := daemonstate.Session{ID: sharedID, IssueID: issueID, Role: daemonstate.SessionRoleOrchestrator, ScopeKind: daemonstate.SessionScopeOrchestration, ScopeID: issueID, State: daemonstate.SessionStateStopped}
	for _, target := range []daemonstate.Session{worker, rooted} {
		t.Run(string(target.Role), func(t *testing.T) {
			transient := daemonstate.NewStore()
			runtimeStore := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(t.TempDir(), "runtime.db"), slog.Default())
			t.Cleanup(func() { _ = runtimeStore.Close() })
			target.UpdatedAt = time.Now().UTC()
			if err := upsertSessionStateFixture(runtimeStore, ctx, "p", target); err != nil {
				t.Fatal(err)
			}
			d := &Daemon{cfg: Config{RepoDir: ".", Logger: slog.Default()}, sessionStore: transient, session: daemonhandlers.NewSessionHandler(transient), runtimeStoresByRoot: map[string]*daemonstate.RuntimeStateStore{".": runtimeStore}}
			sel := sessionIntentSelector{Role: target.Role, ScopeKind: target.ScopeKind, ScopeID: target.ScopeID}
			if err := d.applyTypedSessionLifecycleTransition(ctx, protocol.RequestEnvelope{}, "p", sharedID, issueID, daemonhandlers.CommandSessionStart, "", "", sel); err != nil {
				t.Fatal(err)
			}
			got, found, err := runtimeStore.GetSessionIntent(ctx, "p", target.Role, target.ScopeKind, target.ScopeID)
			if err != nil || !found {
				t.Fatalf("load %s: %v", target.Role, err)
			}
			if got.State != daemonstate.SessionStateStarting {
				t.Fatalf("%s state=%s want %s", target.Role, got.State, daemonstate.SessionStateStarting)
			}
		})
	}
}

func TestTmuxObservationAuthoritySupersedesOldHookAndLaterDesiredWrite(t *testing.T) {
	ctx := context.Background()
	projectID, issueID := "p", "root"
	sessionID := naming.CanonicalSessionID(projectID, issueID)
	store := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(t.TempDir(), "runtime.db"), slog.Default())
	t.Cleanup(func() { _ = store.Close() })
	seed := daemonstate.Session{ID: sessionID, IssueID: issueID, State: daemonstate.SessionStateRunning, UpdatedAt: time.Now().UTC().Add(-3 * time.Second)}
	if err := upsertSessionStateFixture(store, ctx, projectID, seed); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.ApplyPhysicalSessionObservation(ctx, daemonstate.PhysicalSessionObservation{
		ProjectID: projectID, SessionID: sessionID, ObservedState: daemonstate.SessionStateRunning,
		Activity: "busy", ActivitySource: "hooks", UpdatedAt: time.Now().UTC().Add(-2 * time.Second),
	}); err != nil {
		t.Fatal(err)
	}
	d := &Daemon{
		cfg: Config{RepoDir: ".", Logger: slog.Default()}, sessionStore: daemonstate.NewStore(),
		runtimeStoresByProject: map[string]*daemonstate.RuntimeStateStore{projectID: store},
		runtimeStoresByRoot:    map[string]*daemonstate.RuntimeStateStore{".": store},
	}
	if err := d.persistTmuxSessionRuntimeState(ctx, projectID, nil, nil); err != nil {
		t.Fatal(err)
	}
	physical, found, err := store.GetPhysicalSessionObservation(ctx, projectID, sessionID)
	if err != nil || !found || physical.ObservedState != daemonstate.SessionStateStopped {
		t.Fatalf("tmux stopped observation=%+v found=%v err=%v", physical, found, err)
	}
	if err := store.UpsertSessionState(ctx, projectID, daemonstate.Session{
		ID: sessionID, IssueID: issueID, State: daemonstate.SessionStatePaused,
		ObservedState: daemonstate.SessionStateRunning, Activity: "stale", UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	intent, found, err := store.GetSessionIntent(ctx, projectID, daemonstate.SessionRoleWorker, daemonstate.SessionScopeIssue, issueID)
	if err != nil || !found || intent.ObservedState != daemonstate.SessionStateStopped || intent.Activity != "" {
		t.Fatalf("desired write overrode tmux fact: %+v found=%v err=%v", intent, found, err)
	}
	newerHookAt := time.Now().UTC().Add(time.Second)
	if _, _, err := store.ApplyPhysicalSessionObservation(ctx, daemonstate.PhysicalSessionObservation{
		ProjectID: projectID, SessionID: sessionID, ObservedState: daemonstate.SessionStateRunning,
		Activity: "busy", ActivitySource: "hooks", UpdatedAt: newerHookAt,
	}); err != nil {
		t.Fatal(err)
	}
	intent, found, err = store.GetSessionIntent(ctx, projectID, daemonstate.SessionRoleWorker, daemonstate.SessionScopeIssue, issueID)
	if err != nil || !found || intent.ObservedState != daemonstate.SessionStateRunning || intent.Activity != "busy" {
		t.Fatalf("newer hook did not win: %+v found=%v err=%v", intent, found, err)
	}
}

func TestApplySessionLifecycleTransitionPreservesObservedRuntimeState(t *testing.T) {
	const (
		projectID = "proj-refresh-transition"
		issueID   = "az-1"
	)
	sessionID := naming.CanonicalSessionID(projectID, issueID)

	store := daemonstate.NewStore()
	if _, err := store.UpsertSession(projectID, sessionID, issueID, daemonstate.SessionStateStopped); err != nil {
		t.Fatalf("seed stopped session: %v", err)
	}

	runtimeStateStore := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(t.TempDir(), "runtime.db"), slog.Default())
	t.Cleanup(func() {
		_ = runtimeStateStore.Close()
	})
	if err := upsertSessionStateFixture(runtimeStateStore, context.Background(), projectID, daemonstate.Session{
		ID:            sessionID,
		IssueID:       issueID,
		State:         daemonstate.SessionStateAttached,
		ObservedState: daemonstate.SessionStateAttached,
		UpdatedAt:     time.Now().UTC(),
	}); err != nil {
		t.Fatalf("seed durable attached session: %v", err)
	}

	d := &Daemon{
		cfg:          Config{RepoDir: ".", Logger: slog.Default()},
		session:      daemonhandlers.NewSessionHandler(store),
		sessionStore: store,
		runtimeStoresByRoot: map[string]*daemonstate.RuntimeStateStore{
			".": runtimeStateStore,
		},
	}

	req := protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-transition-refresh",
		Kind:            protocol.EnvelopeKindCommand,
		Command:         daemonhandlers.CommandSessionStart,
		Meta: protocol.Metadata{
			ProjectID: naming.ProjectID(projectID),
		},
	}

	if err := d.applySessionLifecycleTransition(context.Background(), req, projectID, sessionID, issueID, daemonhandlers.CommandSessionStart); err != nil {
		t.Fatalf("applySessionLifecycleTransition: %v", err)
	}

	got, err := store.Session(projectID, sessionID)
	if err != nil {
		t.Fatalf("load session: %v", err)
	}
	if got.State != daemonstate.SessionStateStarting {
		t.Fatalf("session state = %s, want %s", got.State, daemonstate.SessionStateStarting)
	}
	runtimeRows, err := runtimeStateStore.ListSessionStates(context.Background(), projectID)
	if err != nil {
		t.Fatalf("load runtime rows: %v", err)
	}
	if len(runtimeRows) != 1 || runtimeRows[0].ObservedState != daemonstate.SessionStateAttached {
		t.Fatalf("runtime rows = %+v, want observed attached state preserved", runtimeRows)
	}
}

func TestApplySessionLifecycleTransitionRejectsInvalidTransition(t *testing.T) {
	const (
		projectID = "proj-invalid-transition"
		issueID   = "az-1"
	)
	sessionID := naming.CanonicalSessionID(projectID, issueID)

	store := daemonstate.NewStore()
	if _, err := store.UpsertSession(projectID, sessionID, issueID, daemonstate.SessionStateStarting); err != nil {
		t.Fatalf("seed starting session: %v", err)
	}

	d := &Daemon{
		cfg:          Config{RepoDir: ".", Logger: slog.Default()},
		sessionStore: store,
	}

	req := protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-invalid-transition",
		Kind:            protocol.EnvelopeKindCommand,
		Command:         daemonhandlers.CommandSessionPause,
		Meta: protocol.Metadata{
			ProjectID: naming.ProjectID(projectID),
		},
	}
	if err := d.applySessionLifecycleTransition(context.Background(), req, projectID, sessionID, issueID, daemonhandlers.CommandSessionPause); !errors.Is(err, daemonstate.ErrInvalidTransition) {
		t.Fatalf("applySessionLifecycleTransition err = %v, want %v", err, daemonstate.ErrInvalidTransition)
	}
}

func TestReconcilePublishesSessionProjectionEventsForRecovery(t *testing.T) {
	const (
		projectID = "proj"
		issueID   = "az-1"
	)

	store := daemonstate.NewStore()
	sessionID := naming.CanonicalSessionID(projectID, issueID)

	tmuxRunner := newTestTmuxRunner(sessionID)
	daemon := &Daemon{
		cfg: Config{
			RepoDir: ".",
			CLITool: "claude",
			Logger:  slog.Default(),
		},
		tmux:         tmux.NewClient(tmuxRunner, slog.Default()),
		session:      daemonhandlers.NewSessionHandler(store),
		sessionStore: store,
		hub:          publish.NewHub(32, 8, slog.Default()),
		worktreeManagersByRoot: map[string]*git.WorktreeManager{
			".": git.NewWorktreeManager(&testGitRunner{worktreePath: "/tmp/proj-az-1", branchName: "riordan/az-1/test"}, ".", slog.Default()),
		},
		worktreeManagersByProject: map[string]*git.WorktreeManager{
			projectID: git.NewWorktreeManager(&testGitRunner{worktreePath: "/tmp/proj-az-1", branchName: "riordan/az-1/test"}, ".", slog.Default()),
		},
	}

	attachIsolatedRuntimeStore(t, daemon, projectID)
	ch, cancel := daemon.hub.Subscribe(projectID, 0)
	defer cancel()

	result, err := daemon.reconcileTmuxAndDaemonSessions(context.Background(), projectID, issueID)
	if err != nil {
		t.Fatalf("reconcile with recovery: %v", err)
	}
	if result.AlignedDaemonSessions != 1 {
		t.Fatalf("aligned daemon sessions = %d, want 1", result.AlignedDaemonSessions)
	}

	events := collectSessionProjectionEvents(t, ch, 2)
	wantStates := []protocol.SessionLifecycleState{
		protocol.SessionLifecycleStateStarting,
		protocol.SessionLifecycleStateAttached,
	}
	for i, evt := range events {
		if evt.Event != protocol.EventSessionUpdated {
			t.Fatalf("event[%d] = %s, want %s", i, evt.Event, protocol.EventSessionUpdated)
		}
		var body protocol.SessionProjectionEventBody
		if err := json.Unmarshal(evt.Body, &body); err != nil {
			t.Fatalf("unmarshal session event body: %v", err)
		}
		if body.ProjectID != projectID {
			t.Fatalf("event[%d] project = %s, want %s", i, body.ProjectID, projectID)
		}
		if body.Session.SessionID.String() != sessionID {
			t.Fatalf("event[%d] session = %s, want %s", i, body.Session.SessionID, sessionID)
		}
		if body.Session.State != wantStates[i] {
			t.Fatalf("event[%d] state = %s, want %s", i, body.Session.State, wantStates[i])
		}
		if body.Revision != evt.Revision {
			t.Fatalf("event[%d] body revision = %d, want envelope revision %d", i, body.Revision, evt.Revision)
		}
		if body.Runtime == nil {
			t.Fatalf("event[%d] expected runtime projection delta", i)
		}
		if body.Runtime.ProjectID != projectID || body.Runtime.Revision != evt.Revision {
			t.Fatalf("event[%d] runtime envelope = %+v, want project/revision %s/%d", i, body.Runtime, projectID, evt.Revision)
		}
		if body.Runtime.Projection.IssueID != issueID {
			t.Fatalf("event[%d] runtime issue = %s, want %s", i, body.Runtime.Projection.IssueID, issueID)
		}
	}
}

func TestReconcileRepairsReadyIdleIssueWithLiveManagedRuntime(t *testing.T) {
	repoDir := t.TempDir()
	projectID, err := appconfig.ProjectIDForRoot(repoDir)
	if err != nil {
		t.Fatal(err)
	}
	issueClient := newMigratedIssueClient(t, repoDir, slog.Default())
	t.Cleanup(func() { _ = issueClient.CloseDB() })
	issueID, err := issueClient.Create(context.Background(), issues.CreateTaskParams{Title: "live ready issue", Type: domain.TypeBug, Status: domain.StatusOpen})
	if err != nil {
		t.Fatal(err)
	}
	sessionID := naming.CanonicalSessionID(repoDir, issueID)
	tmuxRunner := newTestTmuxRunner(sessionID)
	store := daemonstate.NewStore()
	d := &Daemon{
		cfg:  Config{RepoDir: repoDir, CLITool: "claude", Logger: slog.Default()},
		tmux: tmux.NewClient(tmuxRunner, slog.Default()), session: daemonhandlers.NewSessionHandler(store), sessionStore: store,
		worktreeManagersByRoot:    map[string]*git.WorktreeManager{repoDir: git.NewWorktreeManager(&testGitRunner{worktreePath: filepath.Join(filepath.Dir(repoDir), filepath.Base(repoDir)+"-"+issueID)}, repoDir, slog.Default())},
		worktreeManagersByProject: map[string]*git.WorktreeManager{projectID: git.NewWorktreeManager(&testGitRunner{worktreePath: filepath.Join(filepath.Dir(repoDir), filepath.Base(repoDir)+"-"+issueID)}, repoDir, slog.Default())},
	}
	result, err := d.reconcileTmuxAndDaemonSessions(context.Background(), projectID, issueID)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if result.RepairedIssueLifecycle != 1 {
		t.Fatalf("repaired lifecycle = %d, want 1", result.RepairedIssueLifecycle)
	}
	task, err := issueClient.GetWithRuntime(context.Background(), projectID, issueID)
	if err != nil {
		t.Fatal(err)
	}
	if task.State.Disposition != domain.IssueDispositionReady || task.State.Engagement != domain.IssueEngagementWorking {
		t.Fatalf("state = %+v, want ready/working", task.State)
	}
}

func TestReconcileRejectsBacklogLiveRuntimeWithoutDestroyingIt(t *testing.T) {
	repoDir := t.TempDir()
	projectID, err := appconfig.ProjectIDForRoot(repoDir)
	if err != nil {
		t.Fatal(err)
	}
	issueClient := newMigratedIssueClient(t, repoDir, slog.Default())
	t.Cleanup(func() { _ = issueClient.CloseDB() })
	issueID, err := issueClient.Create(context.Background(), issues.CreateTaskParams{Title: "live backlog issue", Type: domain.TypeBug, Status: domain.StatusOpen, Priority: domain.P4})
	if err != nil {
		t.Fatal(err)
	}
	backlog := domain.IssueWorkflowBacklog
	if err := issueClient.UpdateDetails(context.Background(), issueID, issues.UpdateTaskParams{Title: "live backlog issue", Type: domain.TypeBug, Priority: domain.P4, Lifecycle: &backlog}); err != nil {
		t.Fatal(err)
	}
	sessionID := naming.CanonicalSessionID(repoDir, issueID)
	tmuxRunner := newTestTmuxRunner(sessionID)
	store := daemonstate.NewStore()
	d := &Daemon{
		cfg:  Config{RepoDir: repoDir, CLITool: "claude", Logger: slog.Default()},
		tmux: tmux.NewClient(tmuxRunner, slog.Default()), session: daemonhandlers.NewSessionHandler(store), sessionStore: store,
		worktreeManagersByRoot:    map[string]*git.WorktreeManager{repoDir: git.NewWorktreeManager(&testGitRunner{}, repoDir, slog.Default())},
		worktreeManagersByProject: map[string]*git.WorktreeManager{projectID: git.NewWorktreeManager(&testGitRunner{}, repoDir, slog.Default())},
	}
	_, err = d.reconcileTmuxAndDaemonSessions(context.Background(), projectID, issueID)
	if err == nil || !strings.Contains(err.Error(), "backlog issue") {
		t.Fatalf("reconcile err = %v, want backlog invariant", err)
	}
	tmuxRunner.mu.Lock()
	live := tmuxRunner.sessions[sessionID]
	tmuxRunner.mu.Unlock()
	if !live {
		t.Fatal("live runtime was destroyed during invariant rejection")
	}
}

func TestReconcileResolvesLifecycleDivergenceAfterStoppedRuntime(t *testing.T) {
	repoDir := t.TempDir()
	projectID, err := appconfig.ProjectIDForRoot(repoDir)
	if err != nil {
		t.Fatal(err)
	}
	issueClient := newMigratedIssueClient(t, repoDir, slog.Default())
	t.Cleanup(func() { _ = issueClient.CloseDB() })
	issueID, err := issueClient.Create(context.Background(), issues.CreateTaskParams{Title: "stopped divergence", Type: domain.TypeBug, Status: domain.StatusOpen})
	if err != nil {
		t.Fatal(err)
	}
	if err := issueClient.RecordRuntimeDivergence(context.Background(), issueID, "prior live runtime rejected"); err != nil {
		t.Fatal(err)
	}
	store := daemonstate.NewStore()
	sessionID := naming.CanonicalSessionID(repoDir, issueID)
	if _, err := store.UpsertSession(projectID, sessionID, issueID, daemonstate.SessionStateStopped); err != nil {
		t.Fatal(err)
	}
	tmuxRunner := newTestTmuxRunner("")
	delete(tmuxRunner.sessions, "")
	d := &Daemon{cfg: Config{RepoDir: repoDir, Logger: slog.Default()}, tmux: tmux.NewClient(tmuxRunner, slog.Default()), session: daemonhandlers.NewSessionHandler(store), sessionStore: store,
		worktreeManagersByRoot: map[string]*git.WorktreeManager{repoDir: git.NewWorktreeManager(&testGitRunner{}, repoDir, slog.Default())}, worktreeManagersByProject: map[string]*git.WorktreeManager{projectID: git.NewWorktreeManager(&testGitRunner{}, repoDir, slog.Default())}}
	if _, err := d.reconcileTmuxAndDaemonSessions(context.Background(), projectID, issueID); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", filepath.Join(repoDir, ".azedarach", "azedarach.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var active int
	if err := db.QueryRow(`SELECT COUNT(*) FROM issue_runtime_divergences WHERE issue_id=? AND resolved_at IS NULL`, issueID).Scan(&active); err != nil {
		t.Fatal(err)
	}
	if active != 0 {
		t.Fatalf("active divergence=%d, want resolved", active)
	}
}

func TestUntargetedReconcileDefersQuiescentStoppedProjectionWithoutPruning(t *testing.T) {
	repoDir := t.TempDir()
	projectID, err := appconfig.ProjectIDForRoot(repoDir)
	if err != nil {
		t.Fatal(err)
	}
	issueClient := newMigratedIssueClient(t, repoDir, slog.Default())
	t.Cleanup(func() { _ = issueClient.CloseDB() })
	issueID, err := issueClient.Create(context.Background(), issues.CreateTaskParams{Title: "quiescent stopped issue", Type: domain.TypeBug, Status: domain.StatusOpen})
	if err != nil {
		t.Fatal(err)
	}

	runtimeStore := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(t.TempDir(), "runtime.db"), slog.Default())
	t.Cleanup(func() { _ = runtimeStore.Close() })
	sessionID := naming.CanonicalSessionID(repoDir, issueID)
	if err := upsertSessionStateFixture(runtimeStore, context.Background(), projectID, daemonstate.Session{
		ID:            sessionID,
		IssueID:       issueID,
		State:         daemonstate.SessionStateStopped,
		ObservedState: daemonstate.SessionStateStopped,
		UpdatedAt:     time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}

	tmuxRunner := newTestTmuxRunner("")
	delete(tmuxRunner.sessions, "")
	store := daemonstate.NewStore()
	d := &Daemon{
		cfg:          Config{RepoDir: repoDir, Logger: slog.Default()},
		tmux:         tmux.NewClient(tmuxRunner, slog.Default()),
		issues:       issueClient,
		session:      daemonhandlers.NewSessionHandler(store),
		sessionStore: store,
		worktreeManagersByRoot: map[string]*git.WorktreeManager{
			repoDir: git.NewWorktreeManager(&testGitRunner{}, repoDir, slog.Default()),
		},
		worktreeManagersByProject: map[string]*git.WorktreeManager{
			projectID: git.NewWorktreeManager(&testGitRunner{}, repoDir, slog.Default()),
		},
		runtimeStoresByRoot: map[string]*daemonstate.RuntimeStateStore{
			repoDir: runtimeStore,
		},
		runtimeStoresByProject: map[string]*daemonstate.RuntimeStateStore{
			projectID: runtimeStore,
		},
		issueClientsByRoot: map[string]*issues.Client{
			repoDir: issueClient,
		},
		issueClientsByProject: map[string]*issues.Client{
			projectID: issueClient,
		},
	}
	if _, err := d.reconcileTmuxAndDaemonSessions(context.Background(), projectID, ""); err != nil {
		t.Fatal(err)
	}
	rows, err := runtimeStore.ListSessionIntentStates(context.Background(), projectID)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].IssueID != issueID || !daemonSessionProjectionStopped(rows[0]) {
		t.Fatalf("quiescent stopped projection = %+v, want retained stopped intent for %s", rows, issueID)
	}
}

func TestUntargetedReconcilePrunesActionableStaleProjectionWithoutIssue(t *testing.T) {
	for _, tt := range []struct {
		name      string
		sessionID func(string, string) string
		state     daemonstate.SessionState
		observed  daemonstate.SessionState
	}{
		{
			name:      "stopping",
			sessionID: naming.CanonicalSessionID,
			state:     daemonstate.SessionStateStopping,
			observed:  daemonstate.SessionStateRunning,
		},
		{
			name:      "stopped but observed running",
			sessionID: naming.CanonicalSessionID,
			state:     daemonstate.SessionStateStopped,
			observed:  daemonstate.SessionStateRunning,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			repoDir := t.TempDir()
			projectID, err := appconfig.ProjectIDForRoot(repoDir)
			if err != nil {
				t.Fatal(err)
			}
			issueClient := newMigratedIssueClient(t, repoDir, slog.Default())
			t.Cleanup(func() { _ = issueClient.CloseDB() })
			if _, err := issueClient.Create(context.Background(), issues.CreateTaskParams{Title: "valid issue", Type: domain.TypeBug, Status: domain.StatusOpen}); err != nil {
				t.Fatal(err)
			}

			const missingIssueID = "missing-actionable-stale"
			runtimeStore := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(t.TempDir(), "runtime.db"), slog.Default())
			t.Cleanup(func() { _ = runtimeStore.Close() })
			if err := upsertSessionStateFixture(runtimeStore, context.Background(), projectID, daemonstate.Session{
				ID:            tt.sessionID(repoDir, missingIssueID),
				IssueID:       missingIssueID,
				State:         tt.state,
				ObservedState: tt.observed,
				UpdatedAt:     time.Now().UTC(),
			}); err != nil {
				t.Fatal(err)
			}

			tmuxRunner := newTestTmuxRunner("")
			delete(tmuxRunner.sessions, "")
			store := daemonstate.NewStore()
			d := &Daemon{
				cfg:          Config{RepoDir: repoDir, Logger: slog.Default()},
				tmux:         tmux.NewClient(tmuxRunner, slog.Default()),
				issues:       issueClient,
				session:      daemonhandlers.NewSessionHandler(store),
				sessionStore: store,
				worktreeManagersByRoot: map[string]*git.WorktreeManager{
					repoDir: git.NewWorktreeManager(&testGitRunner{}, repoDir, slog.Default()),
				},
				worktreeManagersByProject: map[string]*git.WorktreeManager{
					projectID: git.NewWorktreeManager(&testGitRunner{}, repoDir, slog.Default()),
				},
				runtimeStoresByRoot: map[string]*daemonstate.RuntimeStateStore{
					repoDir: runtimeStore,
				},
				runtimeStoresByProject: map[string]*daemonstate.RuntimeStateStore{
					projectID: runtimeStore,
				},
				issueClientsByRoot: map[string]*issues.Client{
					repoDir: issueClient,
				},
				issueClientsByProject: map[string]*issues.Client{
					projectID: issueClient,
				},
			}
			if _, err := d.reconcileTmuxAndDaemonSessions(context.Background(), projectID, ""); err != nil {
				t.Fatal(err)
			}
			rows, err := runtimeStore.ListSessionIntentStates(context.Background(), projectID)
			if err != nil {
				t.Fatal(err)
			}
			if len(rows) != 0 {
				t.Fatalf("actionable stale projection retained after issue validation: %+v", rows)
			}
		})
	}
}

func TestUntargetedReconcileResolvesDivergenceWithoutRuntimeProjection(t *testing.T) {
	repoDir := t.TempDir()
	projectID, err := appconfig.ProjectIDForRoot(repoDir)
	if err != nil {
		t.Fatal(err)
	}
	issueClient := newMigratedIssueClient(t, repoDir, slog.Default())
	t.Cleanup(func() { _ = issueClient.CloseDB() })
	issueID, err := issueClient.Create(context.Background(), issues.CreateTaskParams{Title: "removed divergence", Type: domain.TypeBug, Status: domain.StatusOpen})
	if err != nil {
		t.Fatal(err)
	}
	if err := issueClient.RecordRuntimeDivergence(context.Background(), issueID, "runtime removed"); err != nil {
		t.Fatal(err)
	}
	store := daemonstate.NewStore()
	tmuxRunner := newTestTmuxRunner("")
	delete(tmuxRunner.sessions, "")
	d := &Daemon{cfg: Config{RepoDir: repoDir, Logger: slog.Default()}, tmux: tmux.NewClient(tmuxRunner, slog.Default()), session: daemonhandlers.NewSessionHandler(store), sessionStore: store,
		worktreeManagersByRoot: map[string]*git.WorktreeManager{repoDir: git.NewWorktreeManager(&testGitRunner{}, repoDir, slog.Default())}, worktreeManagersByProject: map[string]*git.WorktreeManager{projectID: git.NewWorktreeManager(&testGitRunner{}, repoDir, slog.Default())}}
	if _, err := d.reconcileTmuxAndDaemonSessions(context.Background(), projectID, ""); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", filepath.Join(repoDir, ".azedarach", "azedarach.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var active int
	if err := db.QueryRow(`SELECT COUNT(*) FROM issue_runtime_divergences WHERE issue_id=? AND resolved_at IS NULL`, issueID).Scan(&active); err != nil {
		t.Fatal(err)
	}
	if active != 0 {
		t.Fatalf("active divergence=%d, want resolved", active)
	}
	exported, err := issueClient.ExportProjection(context.Background(), projectID)
	if err != nil {
		t.Fatal(err)
	}
	if len(exported.Tasks) != 1 {
		t.Fatalf("projection tasks=%d, want unquarantined issue", len(exported.Tasks))
	}
}

func TestUntargetedReconcilePreservesProjectOrchestratorRuntime(t *testing.T) {
	repoDir := t.TempDir()
	projectID, err := appconfig.ProjectIDForRoot(repoDir)
	if err != nil {
		t.Fatal(err)
	}
	issueClient := newMigratedIssueClient(t, repoDir, slog.Default())
	t.Cleanup(func() { _ = issueClient.CloseDB() })
	sessionID := naming.CanonicalSessionID(repoDir, "orchestrator-project")
	store := daemonstate.NewStore()
	store.ReplaceProjectSessions(projectID, []daemonstate.Session{{ID: sessionID, IssueID: "", Role: daemonstate.SessionRoleOrchestrator, ScopeKind: daemonstate.SessionScopeOrchestration, ScopeID: string(domain.OrchestrationScopeProject), State: daemonstate.SessionStateRunning, ObservedState: daemonstate.SessionStateRunning}})
	tmuxRunner := newTestTmuxRunner(sessionID)
	d := &Daemon{cfg: Config{RepoDir: repoDir, Logger: slog.Default()}, tmux: tmux.NewClient(tmuxRunner, slog.Default()), session: daemonhandlers.NewSessionHandler(store), sessionStore: store,
		worktreeManagersByRoot: map[string]*git.WorktreeManager{repoDir: git.NewWorktreeManager(&testGitRunner{}, repoDir, slog.Default())}, worktreeManagersByProject: map[string]*git.WorktreeManager{projectID: git.NewWorktreeManager(&testGitRunner{}, repoDir, slog.Default())}}
	if _, err := d.reconcileTmuxAndDaemonSessions(context.Background(), projectID, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Session(projectID, sessionID); err != nil {
		t.Fatalf("project orchestrator projection was pruned: %v", err)
	}
	tmuxRunner.mu.Lock()
	live := tmuxRunner.sessions[sessionID]
	tmuxRunner.mu.Unlock()
	if !live {
		t.Fatal("project orchestrator tmux runtime was removed")
	}
}

func TestReconcileRecoversRootedOrchestratorThroughOrchestratorAuthority(t *testing.T) {
	t.Setenv("AZEDARACH_DAEMON_SCOPE", "worktree")
	for _, target := range []string{"targeted", "full", "batched"} {
		t.Run(target, func(t *testing.T) {
			repoDir := t.TempDir()
			projectID, err := appconfig.ProjectIDForRoot(repoDir)
			if err != nil {
				t.Fatal(err)
			}
			issueClient := newMigratedIssueClient(t, repoDir, slog.Default())
			t.Cleanup(func() { _ = issueClient.CloseDB() })
			issueID, err := issueClient.Create(context.Background(), issues.CreateTaskParams{Title: "root", Type: domain.TypeEpic})
			if err != nil {
				t.Fatal(err)
			}
			sessionID := naming.CanonicalSessionID(repoDir, issueID)
			runtimeStore := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(t.TempDir(), "runtime.db"), slog.Default())
			t.Cleanup(func() { _ = runtimeStore.Close() })
			if err := upsertSessionStateFixture(runtimeStore, context.Background(), projectID, daemonstate.Session{ID: sessionID, IssueID: issueID, Role: daemonstate.SessionRoleOrchestrator, ScopeKind: daemonstate.SessionScopeOrchestration, ScopeID: issueID, State: daemonstate.SessionStateRunning, ObservedState: daemonstate.SessionStateRunning}); err != nil {
				t.Fatal(err)
			}
			runner := newSessionStartTmuxRunner()
			store := daemonstate.NewStore()
			manager := git.NewWorktreeManager(&testGitRunner{worktreePath: filepath.Join(filepath.Dir(repoDir), filepath.Base(repoDir)+"-"+issueID), branchName: "riordan/" + issueID + "/root"}, repoDir, slog.Default())
			d := &Daemon{cfg: Config{RepoDir: repoDir, CLITool: "codex", CodexAppServer: true, Logger: slog.Default()}, tmux: tmux.NewClient(runner, slog.Default()), session: daemonhandlers.NewSessionHandler(store), sessionStore: store, runtimeStoresByRoot: map[string]*daemonstate.RuntimeStateStore{repoDir: runtimeStore}, runtimeStoresByProject: map[string]*daemonstate.RuntimeStateStore{projectID: runtimeStore}, worktreeManagersByRoot: map[string]*git.WorktreeManager{repoDir: manager}, worktreeManagersByProject: map[string]*git.WorktreeManager{projectID: manager}}
			acknowledgeManagedAgentOnInitialLaunch(t, d, runner, projectID)
			targetIssue := issueID
			if target == "full" {
				targetIssue = ""
			}
			if target == "batched" {
				ids := make([]string, runtimeReconcileIssueRepairLimit)
				for i := range ids {
					ids[i] = fmt.Sprintf("missing-%d", i)
				}
				ids = append(ids, issueID)
				if _, err := newRuntimeReconcileService(d).ReconcileIssues(context.Background(), projectID, ids); err != nil {
					t.Fatal(err)
				}
			} else {
				result, err := d.reconcileTmuxAndDaemonSessions(context.Background(), projectID, targetIssue)
				if err != nil {
					t.Fatal(err)
				}
				if result.RecreatedTmuxSessions != 1 {
					t.Fatalf("result=%+v", result)
				}
			}
			if !runner.sessions[sessionID] {
				t.Fatalf("rooted orchestrator runtime %s was not recovered", sessionID)
			}
			projection, found, err := runtimeStore.GetSessionState(context.Background(), projectID, sessionID)
			if err != nil || !found || projection.Role != daemonstate.SessionRoleOrchestrator || projection.ScopeKind != daemonstate.SessionScopeOrchestration || projection.ScopeID != issueID {
				t.Fatalf("projection=%+v found=%v err=%v", projection, found, err)
			}
			contract := requireNewSessionLaunchCommand(t, runner, sessionID) + "\n" + requireNewSessionLaunchScript(t, runner, sessionID) + "\n" + runner.launchPromptContents[sessionID] + "\n" + strings.Join(runner.inputPayloads, "\n") + "\n" + strings.Join(runner.sendKeysPayloads, "\n")
			if !strings.Contains(contract, "Role: orchestrator") || strings.Contains(contract, "Role: worker") {
				t.Fatalf("rooted orchestrator launch contract=%q", contract)
			}
			var launchArgs []string
			for _, command := range runner.commands {
				if len(command) > 0 && command[0] == "new-session" {
					launchArgs = command
					break
				}
			}
			if got, ok := tmuxCommandEnvironmentValue(launchArgs, "AZEDARACH_DAEMON_SCOPE"); !ok || got != "global" {
				t.Fatalf("rooted orchestrator scope = %q, present=%t; command=%v", got, ok, launchArgs)
			}
		})
	}
}

func TestReconcileRefreshesLifecycleBeforeRepairAcrossDaemons(t *testing.T) {
	repoDir := t.TempDir()
	projectID, err := appconfig.ProjectIDForRoot(repoDir)
	if err != nil {
		t.Fatal(err)
	}
	clientA := newMigratedIssueClient(t, repoDir, slog.Default())
	clientB := newMigratedIssueClient(t, repoDir, slog.Default())
	t.Cleanup(func() { _ = clientA.CloseDB(); _ = clientB.CloseDB() })
	issueID, err := clientA.Create(context.Background(), issues.CreateTaskParams{Title: "cross daemon", Type: domain.TypeBug, Status: domain.StatusInProgress})
	if err != nil {
		t.Fatal(err)
	}
	// Simulate another daemon writing ready+idle after daemon A has already
	// observed the issue. Reconcile must refresh the durable projection.
	if _, err := clientA.GetWithRuntime(context.Background(), projectID, issueID); err != nil {
		t.Fatal(err)
	}
	if err := clientB.Update(context.Background(), issueID, domain.StatusOpen); err != nil {
		t.Fatal(err)
	}
	sessionID := naming.CanonicalSessionID(repoDir, issueID)
	tmuxRunner := newTestTmuxRunner(sessionID)
	store := daemonstate.NewStore()
	d := &Daemon{cfg: Config{RepoDir: repoDir, Logger: slog.Default()}, tmux: tmux.NewClient(tmuxRunner, slog.Default()), session: daemonhandlers.NewSessionHandler(store), sessionStore: store,
		worktreeManagersByRoot: map[string]*git.WorktreeManager{repoDir: git.NewWorktreeManager(&testGitRunner{}, repoDir, slog.Default())}, worktreeManagersByProject: map[string]*git.WorktreeManager{projectID: git.NewWorktreeManager(&testGitRunner{}, repoDir, slog.Default())}}
	result, err := d.reconcileTmuxAndDaemonSessions(context.Background(), projectID, issueID)
	if err != nil {
		t.Fatal(err)
	}
	if result.RepairedIssueLifecycle != 1 {
		t.Fatalf("repairs=%d, want 1", result.RepairedIssueLifecycle)
	}
	task, err := clientB.GetWithRuntime(context.Background(), projectID, issueID)
	if err != nil {
		t.Fatal(err)
	}
	if task.State.Engagement != domain.IssueEngagementWorking {
		t.Fatalf("engagement=%s, want working", task.State.Engagement)
	}
}

func TestReconcileIssueProjectionFailureFailsClosedForConfiguredProject(t *testing.T) {
	repoDir := t.TempDir()
	projectID, err := appconfig.ProjectIDForRoot(repoDir)
	if err != nil {
		t.Fatal(err)
	}
	client := newMigratedIssueClient(t, repoDir, slog.Default())
	if _, err := client.Create(context.Background(), issues.CreateTaskParams{Title: "configured", Type: domain.TypeTask}); err != nil {
		t.Fatal(err)
	}
	if err := client.CloseDB(); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AZEDARACH_REFUSE_DB_PATH", filepath.Join(repoDir, ".azedarach", "azedarach.db"))
	d := &Daemon{cfg: Config{RepoDir: repoDir, Logger: slog.Default()}, issueClientsByProject: map[string]*issues.Client{projectID: client}}
	_, enabled, err := d.reconcileIssueKeyIndex(context.Background(), projectID, []string{"a"})
	if !enabled || err == nil {
		t.Fatalf("enabled=%t err=%v, want configured fail-closed error", enabled, err)
	}
}

func TestManagedRuntimeLifecycleEvaluationConsultsInvariantPolicy(t *testing.T) {
	old := daemonInvariantSourceMatrix[daemonInvariantSessionIssueLifecycle]
	daemonInvariantSourceMatrix[daemonInvariantSessionIssueLifecycle] = daemonInvariantSourceProjection
	t.Cleanup(func() { daemonInvariantSourceMatrix[daemonInvariantSessionIssueLifecycle] = old })
	store := daemonstate.NewStore()
	d := &Daemon{cfg: Config{RepoDir: ".", Logger: slog.Default()}, tmux: tmux.NewClient(newTestTmuxRunner("proj-a"), slog.Default()), sessionStore: store,
		worktreeManagersByRoot: map[string]*git.WorktreeManager{".": git.NewWorktreeManager(&testGitRunner{}, ".", slog.Default())}}
	_, err := d.reconcileTmuxAndDaemonSessions(context.Background(), "proj", "a")
	if err == nil || !strings.Contains(err.Error(), "requires hybrid source") {
		t.Fatalf("err=%v", err)
	}
}

func TestReconcileRecoversFromDurableSessionProjection(t *testing.T) {
	priorScope, hadPriorScope := os.LookupEnv("AZEDARACH_DAEMON_SCOPE")
	if err := os.Unsetenv("AZEDARACH_DAEMON_SCOPE"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if hadPriorScope {
			_ = os.Setenv("AZEDARACH_DAEMON_SCOPE", priorScope)
		} else {
			_ = os.Unsetenv("AZEDARACH_DAEMON_SCOPE")
		}
	})
	repoDir := t.TempDir()
	managedDir := filepath.Join(t.TempDir(), ".azedarach-generations", "generation.current")
	t.Setenv("PATH", filepath.Join(repoDir, "bin")+string(os.PathListSeparator)+"/usr/bin:/bin")
	projectID, err := appconfig.ProjectIDForRoot(repoDir)
	if err != nil {
		t.Fatalf("ProjectIDForRoot: %v", err)
	}
	issuesClient := newMigratedIssueClient(t, repoDir, slog.Default())
	t.Cleanup(func() {
		_ = issuesClient.CloseDB()
	})
	issueID, err := issuesClient.Create(context.Background(), issues.CreateTaskParams{
		Title: "Recoverable durable session issue",
		Type:  domain.TypeTask,
	})
	if err != nil {
		t.Fatalf("create local issue: %v", err)
	}
	sessionID := naming.CanonicalSessionID(repoDir, issueID)
	runtimeStateStore := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(t.TempDir(), "runtime.db"), slog.Default())
	t.Cleanup(func() {
		_ = runtimeStateStore.Close()
	})
	if err := upsertSessionStateFixture(runtimeStateStore, context.Background(), projectID, daemonstate.Session{
		ID:        sessionID,
		IssueID:   issueID,
		State:     daemonstate.SessionStateAttached,
		UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("seed durable session projection: %v", err)
	}

	tmuxRunner := newSessionStartTmuxRunner()
	store := daemonstate.NewStore()
	daemon := &Daemon{
		cfg: Config{
			RepoDir:                 repoDir,
			CLITool:                 "codex",
			CodexAppServer:          true,
			ManagedGenerationBinDir: managedDir,
			Logger:                  slog.Default(),
		},
		tmux:         tmux.NewClient(tmuxRunner, slog.Default()),
		session:      daemonhandlers.NewSessionHandler(store),
		sessionStore: store,
		worktreeManagersByRoot: map[string]*git.WorktreeManager{
			repoDir: git.NewWorktreeManager(&testGitRunner{worktreePath: filepath.Join(filepath.Dir(repoDir), filepath.Base(repoDir)+"-"+issueID), branchName: "riordan/" + issueID + "/test"}, repoDir, slog.Default()),
		},
		worktreeManagersByProject: map[string]*git.WorktreeManager{
			projectID: git.NewWorktreeManager(&testGitRunner{worktreePath: filepath.Join(filepath.Dir(repoDir), filepath.Base(repoDir)+"-"+issueID), branchName: "riordan/" + issueID + "/test"}, repoDir, slog.Default()),
		},
		runtimeStoresByRoot: map[string]*daemonstate.RuntimeStateStore{
			repoDir: runtimeStateStore,
		},
	}
	acknowledgeManagedAgentOnInitialLaunch(t, daemon, tmuxRunner, projectID)

	result, err := daemon.reconcileTmuxAndDaemonSessions(context.Background(), projectID, issueID)
	if err != nil {
		t.Fatalf("reconcile with durable session projection: %v", err)
	}
	if result.RecreatedTmuxSessions != 1 {
		t.Fatalf("recreated tmux sessions = %d, want 1", result.RecreatedTmuxSessions)
	}

	created := 0
	for _, command := range tmuxRunner.commands {
		if len(command) > 0 && command[0] == "new-session" {
			created++
		}
	}
	sessionExists := tmuxRunner.sessions[sessionID]
	if created != 1 {
		t.Fatalf("new-session calls = %d, want 1", created)
	}
	if !sessionExists {
		t.Fatalf("expected tmux session %q to exist after reconcile", sessionID)
	}
	var recreatedCommand []string
	for _, command := range tmuxRunner.commands {
		if len(command) > 0 && command[0] == "new-session" {
			recreatedCommand = command
		}
	}
	if got, ok := tmuxCommandEnvironmentValue(recreatedCommand, "PATH"); ok || got != "" {
		t.Fatalf("recreated session injected PATH = %q, %t; command=%v", got, ok, recreatedCommand)
	}
	if got, ok := tmuxCommandEnvironmentValue(recreatedCommand, "AZEDARACH_DAEMON_SCOPE"); !ok || got != "global" {
		t.Fatalf("recreated session scope = %q, present=%t; command=%v", got, ok, recreatedCommand)
	}
	launchCommand := recreatedCommand[len(recreatedCommand)-1]
	if !strings.Contains(launchCommand, "session-launch") || strings.Contains(launchCommand, "export PATH=") || strings.Contains(launchCommand, managedDir) {
		t.Fatalf("reconciled launch does not use canonical artifact: %v", recreatedCommand)
	}
}

func TestReconcileRecreatesObservedStoppedDesiredActiveSession(t *testing.T) {
	repoDir := t.TempDir()
	projectID, err := appconfig.ProjectIDForRoot(repoDir)
	if err != nil {
		t.Fatalf("ProjectIDForRoot: %v", err)
	}
	issuesClient := newMigratedIssueClient(t, repoDir, slog.Default())
	t.Cleanup(func() {
		_ = issuesClient.CloseDB()
	})
	issueID, err := issuesClient.Create(context.Background(), issues.CreateTaskParams{
		Title: "Manually stopped durable session issue",
		Type:  domain.TypeTask,
	})
	if err != nil {
		t.Fatalf("create local issue: %v", err)
	}
	sessionID := naming.CanonicalSessionID(repoDir, issueID)
	runtimeStateStore := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(t.TempDir(), "runtime.db"), slog.Default())
	t.Cleanup(func() {
		_ = runtimeStateStore.Close()
	})
	if err := upsertSessionStateFixture(runtimeStateStore, context.Background(), projectID, daemonstate.Session{
		ID:            sessionID,
		IssueID:       issueID,
		State:         daemonstate.SessionStateAttached,
		ObservedState: daemonstate.SessionStateStopped,
		UpdatedAt:     time.Now().UTC(),
	}); err != nil {
		t.Fatalf("seed observed stopped session projection: %v", err)
	}

	tmuxRunner := newSessionStartTmuxRunner()
	store := daemonstate.NewStore()
	daemon := &Daemon{
		cfg: Config{
			RepoDir: repoDir,
			CLITool: "claude",
			Logger:  slog.Default(),
		},
		tmux:         tmux.NewClient(tmuxRunner, slog.Default()),
		session:      daemonhandlers.NewSessionHandler(store),
		sessionStore: store,
		worktreeManagersByRoot: map[string]*git.WorktreeManager{
			repoDir: git.NewWorktreeManager(&testGitRunner{worktreePath: filepath.Join(filepath.Dir(repoDir), filepath.Base(repoDir)+"-"+issueID), branchName: "riordan/" + issueID + "/test"}, repoDir, slog.Default()),
		},
		worktreeManagersByProject: map[string]*git.WorktreeManager{
			projectID: git.NewWorktreeManager(&testGitRunner{worktreePath: filepath.Join(filepath.Dir(repoDir), filepath.Base(repoDir)+"-"+issueID), branchName: "riordan/" + issueID + "/test"}, repoDir, slog.Default()),
		},
		runtimeStoresByRoot: map[string]*daemonstate.RuntimeStateStore{
			repoDir: runtimeStateStore,
		},
	}
	acknowledgeManagedAgentOnInitialLaunch(t, daemon, tmuxRunner, projectID)

	result, err := daemon.reconcileTmuxAndDaemonSessions(context.Background(), projectID, issueID)
	if err != nil {
		t.Fatalf("reconcile observed stopped projection: %v", err)
	}
	if result.RecreatedTmuxSessions != 1 {
		t.Fatalf("recreated tmux sessions = %d, want 1", result.RecreatedTmuxSessions)
	}
	created := 0
	for _, command := range tmuxRunner.commands {
		if len(command) > 0 && command[0] == "new-session" {
			created++
		}
	}
	sessionExists := tmuxRunner.sessions[sessionID]
	if created != 1 {
		t.Fatalf("new-session calls = %d, want 1", created)
	}
	if !sessionExists {
		t.Fatalf("session %q was not recreated", sessionID)
	}
	rows, err := runtimeStateStore.ListSessionStates(context.Background(), projectID)
	if err != nil {
		t.Fatalf("list runtime rows: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("runtime rows = %d, want 1", len(rows))
	}
	if rows[0].State != daemonstate.SessionStateAttached || rows[0].ObservedState != daemonstate.SessionStateAttached {
		t.Fatalf("runtime row = desired %s observed %s, want attached/attached", rows[0].State, rows[0].ObservedState)
	}
}

func TestReconcileDoesNotRecreateFromAgentScopedProjection(t *testing.T) {
	repoDir := t.TempDir()
	projectID, err := appconfig.ProjectIDForRoot(repoDir)
	if err != nil {
		t.Fatalf("ProjectIDForRoot: %v", err)
	}
	issuesClient := newMigratedIssueClient(t, repoDir, slog.Default())
	t.Cleanup(func() {
		_ = issuesClient.CloseDB()
	})
	issueID, err := issuesClient.Create(context.Background(), issues.CreateTaskParams{
		Title: "Pane projection only issue",
		Type:  domain.TypeTask,
	})
	if err != nil {
		t.Fatalf("create local issue: %v", err)
	}
	sessionID := naming.CanonicalSessionID(repoDir, issueID)
	paneSessionID := sessionID + ".pane-535"
	runtimeStateStore := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(t.TempDir(), "runtime.db"), slog.Default())
	t.Cleanup(func() {
		_ = runtimeStateStore.Close()
	})
	if err := upsertSessionStateFixture(runtimeStateStore, context.Background(), projectID, daemonstate.Session{
		ID:            paneSessionID,
		IssueID:       issueID,
		State:         daemonstate.SessionStateAttached,
		ObservedState: daemonstate.SessionStateStopped,
		UpdatedAt:     time.Now().UTC(),
	}); err != nil {
		t.Fatalf("seed pane projection: %v", err)
	}

	tmuxRunner := &testTmuxRunner{
		sessions:    map[string]bool{},
		killEntered: make(chan struct{}),
		killRelease: make(chan struct{}),
	}
	store := daemonstate.NewStore()
	daemon := &Daemon{
		cfg: Config{
			RepoDir: repoDir,
			CLITool: "claude",
			Logger:  slog.Default(),
		},
		tmux:         tmux.NewClient(tmuxRunner, slog.Default()),
		session:      daemonhandlers.NewSessionHandler(store),
		sessionStore: store,
		worktreeManagersByRoot: map[string]*git.WorktreeManager{
			repoDir: git.NewWorktreeManager(&testGitRunner{worktreePath: filepath.Join(filepath.Dir(repoDir), filepath.Base(repoDir)+"-"+issueID), branchName: "riordan/" + issueID + "/test"}, repoDir, slog.Default()),
		},
		worktreeManagersByProject: map[string]*git.WorktreeManager{
			projectID: git.NewWorktreeManager(&testGitRunner{worktreePath: filepath.Join(filepath.Dir(repoDir), filepath.Base(repoDir)+"-"+issueID), branchName: "riordan/" + issueID + "/test"}, repoDir, slog.Default()),
		},
		runtimeStoresByRoot: map[string]*daemonstate.RuntimeStateStore{
			repoDir: runtimeStateStore,
		},
	}

	result, err := daemon.reconcileTmuxAndDaemonSessions(context.Background(), projectID, issueID)
	if err != nil {
		t.Fatalf("reconcile pane projection: %v", err)
	}
	if result.RecreatedTmuxSessions != 0 {
		t.Fatalf("recreated tmux sessions = %d, want 0", result.RecreatedTmuxSessions)
	}
	tmuxRunner.mu.Lock()
	created := tmuxRunner.newSessionCalls
	sessionExists := tmuxRunner.sessions[sessionID]
	tmuxRunner.mu.Unlock()
	if created != 0 {
		t.Fatalf("new-session calls = %d, want 0", created)
	}
	if sessionExists {
		t.Fatalf("agent-scoped projection recreated parent tmux session %q", sessionID)
	}
}

func TestReconcileStoppedParentWinsOverAgentScopedProjection(t *testing.T) {
	repoDir := t.TempDir()
	projectID, err := appconfig.ProjectIDForRoot(repoDir)
	if err != nil {
		t.Fatalf("ProjectIDForRoot: %v", err)
	}
	issuesClient := newMigratedIssueClient(t, repoDir, slog.Default())
	t.Cleanup(func() {
		_ = issuesClient.CloseDB()
	})
	issueID, err := issuesClient.Create(context.Background(), issues.CreateTaskParams{
		Title: "Stopped parent with stale pane",
		Type:  domain.TypeTask,
	})
	if err != nil {
		t.Fatalf("create local issue: %v", err)
	}
	sessionID := naming.CanonicalSessionID(repoDir, issueID)
	paneSessionID := sessionID + ".pane-535"
	runtimeStateStore := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(t.TempDir(), "runtime.db"), slog.Default())
	t.Cleanup(func() {
		_ = runtimeStateStore.Close()
	})
	for _, seed := range []daemonstate.Session{
		{ID: sessionID, IssueID: issueID, State: daemonstate.SessionStateStopped, ObservedState: daemonstate.SessionStateStopped, UpdatedAt: time.Now().UTC()},
		{ID: paneSessionID, IssueID: issueID, State: daemonstate.SessionStateAttached, ObservedState: daemonstate.SessionStateStopped, UpdatedAt: time.Now().UTC().Add(time.Second)},
	} {
		if err := upsertSessionStateFixture(runtimeStateStore, context.Background(), projectID, seed); err != nil {
			t.Fatalf("seed runtime state %s: %v", seed.ID, err)
		}
	}

	tmuxRunner := &testTmuxRunner{
		sessions:    map[string]bool{},
		killEntered: make(chan struct{}),
		killRelease: make(chan struct{}),
	}
	store := daemonstate.NewStore()
	daemon := &Daemon{
		cfg: Config{
			RepoDir: repoDir,
			CLITool: "claude",
			Logger:  slog.Default(),
		},
		tmux:         tmux.NewClient(tmuxRunner, slog.Default()),
		session:      daemonhandlers.NewSessionHandler(store),
		sessionStore: store,
		worktreeManagersByRoot: map[string]*git.WorktreeManager{
			repoDir: git.NewWorktreeManager(&testGitRunner{worktreePath: filepath.Join(filepath.Dir(repoDir), filepath.Base(repoDir)+"-"+issueID), branchName: "riordan/" + issueID + "/test"}, repoDir, slog.Default()),
		},
		worktreeManagersByProject: map[string]*git.WorktreeManager{
			projectID: git.NewWorktreeManager(&testGitRunner{worktreePath: filepath.Join(filepath.Dir(repoDir), filepath.Base(repoDir)+"-"+issueID), branchName: "riordan/" + issueID + "/test"}, repoDir, slog.Default()),
		},
		runtimeStoresByRoot: map[string]*daemonstate.RuntimeStateStore{
			repoDir: runtimeStateStore,
		},
	}

	result, err := daemon.reconcileTmuxAndDaemonSessions(context.Background(), projectID, issueID)
	if err != nil {
		t.Fatalf("reconcile stopped parent with stale pane: %v", err)
	}
	if result.RecreatedTmuxSessions != 0 {
		t.Fatalf("recreated tmux sessions = %d, want 0", result.RecreatedTmuxSessions)
	}
	tmuxRunner.mu.Lock()
	created := tmuxRunner.newSessionCalls
	sessionExists := tmuxRunner.sessions[sessionID]
	tmuxRunner.mu.Unlock()
	if created != 0 {
		t.Fatalf("new-session calls = %d, want 0", created)
	}
	if sessionExists {
		t.Fatalf("stale pane projection recreated stopped parent tmux session %q", sessionID)
	}
}

func TestReconcilePrunesInvalidDesiredSessionAndDoesNotRecreate(t *testing.T) {
	const invalidIssueID = "ch-it"

	repoDir := t.TempDir()
	projectID, err := appconfig.ProjectIDForRoot(repoDir)
	if err != nil {
		t.Fatalf("ProjectIDForRoot: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(repoDir, ".azedarach"), 0o755); err != nil {
		t.Fatalf("mkdir .azedarach: %v", err)
	}
	issuesClient := newMigratedIssueClient(t, repoDir, slog.Default())
	t.Cleanup(func() {
		_ = issuesClient.CloseDB()
	})
	if _, err := issuesClient.Create(context.Background(), issues.CreateTaskParams{
		Title: "Valid local issue",
		Type:  domain.TypeTask,
	}); err != nil {
		t.Fatalf("create local issue: %v", err)
	}

	sessionID := naming.CanonicalSessionID(repoDir, invalidIssueID)
	runtimeStateStore := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(t.TempDir(), "runtime.db"), slog.Default())
	t.Cleanup(func() {
		_ = runtimeStateStore.Close()
	})
	if err := upsertSessionStateFixture(runtimeStateStore, context.Background(), projectID, daemonstate.Session{
		ID:        sessionID,
		IssueID:   invalidIssueID,
		State:     daemonstate.SessionStateAttached,
		UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("seed invalid durable session projection: %v", err)
	}

	tmuxRunner := &testTmuxRunner{
		sessions:    map[string]bool{},
		killEntered: make(chan struct{}),
		killRelease: make(chan struct{}),
	}
	store := daemonstate.NewStore()
	daemon := &Daemon{
		cfg: Config{
			RepoDir: repoDir,
			CLITool: "claude",
			Logger:  slog.Default(),
		},
		tmux:         tmux.NewClient(tmuxRunner, slog.Default()),
		issues:       issuesClient,
		session:      daemonhandlers.NewSessionHandler(store),
		sessionStore: store,
		worktreeManagersByRoot: map[string]*git.WorktreeManager{
			repoDir: git.NewWorktreeManager(&testGitRunner{
				worktreePath: filepath.Join(filepath.Dir(repoDir), filepath.Base(repoDir)+"-"+invalidIssueID),
				branchName:   "riordan/" + invalidIssueID + "/test",
			}, repoDir, slog.Default()),
		},
		worktreeManagersByProject: map[string]*git.WorktreeManager{
			projectID: git.NewWorktreeManager(&testGitRunner{
				worktreePath: filepath.Join(filepath.Dir(repoDir), filepath.Base(repoDir)+"-"+invalidIssueID),
				branchName:   "riordan/" + invalidIssueID + "/test",
			}, repoDir, slog.Default()),
		},
		runtimeStoresByRoot: map[string]*daemonstate.RuntimeStateStore{
			repoDir: runtimeStateStore,
		},
		issueClientsByRoot: map[string]*issues.Client{
			repoDir: issuesClient,
		},
		issueClientsByProject: map[string]*issues.Client{
			projectID: issuesClient,
		},
	}

	result, err := daemon.reconcileTmuxAndDaemonSessions(context.Background(), projectID, invalidIssueID)
	if err != nil {
		t.Fatalf("reconcile invalid desired session: %v", err)
	}
	if result.RecreatedTmuxSessions != 0 {
		t.Fatalf("recreated tmux sessions = %d, want 0", result.RecreatedTmuxSessions)
	}
	tmuxRunner.mu.Lock()
	newSessionCalls := tmuxRunner.newSessionCalls
	tmuxRunner.mu.Unlock()
	if newSessionCalls != 0 {
		t.Fatalf("new-session calls = %d, want 0", newSessionCalls)
	}
	rows, err := runtimeStateStore.ListSessionStates(context.Background(), projectID)
	if err != nil {
		t.Fatalf("list session states: %v", err)
	}
	for _, row := range rows {
		if strings.EqualFold(strings.TrimSpace(row.IssueID), invalidIssueID) {
			t.Fatalf("invalid desired session was not pruned: %+v", row)
		}
	}
}

func TestReconcileDoesNotAlignUnknownTmuxSessionWithoutIssueRecord(t *testing.T) {
	const invalidIssueID = "ch-hn"

	repoDir := t.TempDir()
	projectID, err := appconfig.ProjectIDForRoot(repoDir)
	if err != nil {
		t.Fatalf("ProjectIDForRoot: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(repoDir, ".azedarach"), 0o755); err != nil {
		t.Fatalf("mkdir .azedarach: %v", err)
	}
	issuesClient := newMigratedIssueClient(t, repoDir, slog.Default())
	t.Cleanup(func() {
		_ = issuesClient.CloseDB()
	})

	tmuxSessionID := naming.CanonicalSessionID(repoDir, invalidIssueID)
	tmuxRunner := newTestTmuxRunner(tmuxSessionID)
	runtimeStateStore := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(t.TempDir(), "runtime.db"), slog.Default())
	t.Cleanup(func() {
		_ = runtimeStateStore.Close()
	})
	store := daemonstate.NewStore()

	daemon := &Daemon{
		cfg: Config{
			RepoDir: repoDir,
			CLITool: "claude",
			Logger:  slog.Default(),
		},
		tmux:         tmux.NewClient(tmuxRunner, slog.Default()),
		issues:       issuesClient,
		session:      daemonhandlers.NewSessionHandler(store),
		sessionStore: store,
		worktreeManagersByRoot: map[string]*git.WorktreeManager{
			repoDir: git.NewWorktreeManager(&testGitRunner{
				worktreePath: filepath.Join(filepath.Dir(repoDir), filepath.Base(repoDir)+"-"+invalidIssueID),
				branchName:   "riordan/" + invalidIssueID + "/test",
			}, repoDir, slog.Default()),
		},
		worktreeManagersByProject: map[string]*git.WorktreeManager{
			projectID: git.NewWorktreeManager(&testGitRunner{
				worktreePath: filepath.Join(filepath.Dir(repoDir), filepath.Base(repoDir)+"-"+invalidIssueID),
				branchName:   "riordan/" + invalidIssueID + "/test",
			}, repoDir, slog.Default()),
		},
		runtimeStoresByRoot: map[string]*daemonstate.RuntimeStateStore{
			repoDir: runtimeStateStore,
		},
		issueClientsByRoot: map[string]*issues.Client{
			repoDir: issuesClient,
		},
		issueClientsByProject: map[string]*issues.Client{
			projectID: issuesClient,
		},
	}

	result, err := daemon.reconcileTmuxAndDaemonSessions(context.Background(), projectID, "")
	if err != nil {
		t.Fatalf("reconcile with unknown tmux session: %v", err)
	}
	if result.AlignedDaemonSessions != 0 {
		t.Fatalf("aligned daemon sessions = %d, want 0", result.AlignedDaemonSessions)
	}
	snapshot := store.ReadSnapshot(projectID)
	if len(snapshot.Sessions) != 0 {
		t.Fatalf("unexpected in-memory sessions aligned from unknown tmux: %+v", snapshot.Sessions)
	}
}

func TestReconcilePrunesMissingWorktreeSessionProjection(t *testing.T) {
	ctx := context.Background()
	repoDir := t.TempDir()
	projectID, err := appconfig.ProjectIDForRoot(repoDir)
	if err != nil {
		t.Fatalf("ProjectIDForRoot: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(repoDir, ".azedarach"), 0o755); err != nil {
		t.Fatalf("mkdir .azedarach: %v", err)
	}

	issuesClient := newMigratedIssueClient(t, repoDir, slog.Default())
	t.Cleanup(func() { _ = issuesClient.CloseDB() })
	issueID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title:  "Stale projected session",
		Type:   domain.TypeBug,
		Status: domain.StatusInProgress,
	})
	if err != nil {
		t.Fatalf("create issue: %v", err)
	}

	sessionID := naming.CanonicalSessionID(repoDir, issueID)
	runtimeStateStore := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(t.TempDir(), "runtime.db"), slog.Default())
	t.Cleanup(func() { _ = runtimeStateStore.Close() })
	if err := upsertSessionStateFixture(runtimeStateStore, ctx, projectID, daemonstate.Session{
		ID:            sessionID,
		IssueID:       issueID,
		State:         daemonstate.SessionStateAttached,
		ObservedState: daemonstate.SessionStateAttached,
		UpdatedAt:     time.Now().UTC(),
	}); err != nil {
		t.Fatalf("seed session projection: %v", err)
	}

	tmuxRunner := &testTmuxRunner{
		sessions:    map[string]bool{},
		killEntered: make(chan struct{}),
		killRelease: make(chan struct{}),
	}
	store := daemonstate.NewStore()
	daemon := &Daemon{
		cfg: Config{
			RepoDir: repoDir,
			CLITool: "claude",
			Logger:  slog.Default(),
		},
		tmux:         tmux.NewClient(tmuxRunner, slog.Default()),
		issues:       issuesClient,
		session:      daemonhandlers.NewSessionHandler(store),
		sessionStore: store,
		worktreeManagersByRoot: map[string]*git.WorktreeManager{
			repoDir: git.NewWorktreeManager(&testGitRunner{worktreePath: repoDir, branchName: "main"}, repoDir, slog.Default()),
		},
		worktreeManagersByProject: map[string]*git.WorktreeManager{
			projectID: git.NewWorktreeManager(&testGitRunner{worktreePath: repoDir, branchName: "main"}, repoDir, slog.Default()),
		},
		runtimeStoresByRoot: map[string]*daemonstate.RuntimeStateStore{
			repoDir: runtimeStateStore,
		},
		issueClientsByRoot: map[string]*issues.Client{
			repoDir: issuesClient,
		},
		issueClientsByProject: map[string]*issues.Client{
			projectID: issuesClient,
		},
	}

	result, err := daemon.reconcileTmuxAndDaemonSessions(ctx, projectID, "")
	if err != nil {
		t.Fatalf("reconcile sessions: %v", err)
	}
	if result.RecreatedTmuxSessions != 0 {
		t.Fatalf("recreated tmux sessions = %d, want 0", result.RecreatedTmuxSessions)
	}
	if tmuxRunner.newSessionCalls != 0 {
		t.Fatalf("new tmux sessions = %d, want 0", tmuxRunner.newSessionCalls)
	}
	if _, found, err := runtimeStateStore.GetSessionState(ctx, projectID, sessionID); err != nil {
		t.Fatalf("GetSessionState: %v", err)
	} else if found {
		t.Fatalf("stale session projection still present for %s", sessionID)
	}
}

func TestReconcileDoesNotResurrectStoppedSessionAcrossDaemons(t *testing.T) {
	const (
		projectID = "proj"
		issueID   = "az-1"
	)
	sessionID := naming.CanonicalSessionID(projectID, issueID)

	runtimeStateStore := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(t.TempDir(), "runtime.db"), slog.Default())
	t.Cleanup(func() {
		_ = runtimeStateStore.Close()
	})
	if err := upsertSessionStateFixture(runtimeStateStore, context.Background(), projectID, daemonstate.Session{
		ID:        sessionID,
		IssueID:   issueID,
		State:     daemonstate.SessionStateAttached,
		UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("seed durable attached session: %v", err)
	}

	tmuxRunner := newTestTmuxRunner(sessionID)
	close(tmuxRunner.killRelease)

	storeA := daemonstate.NewStore()
	if _, err := storeA.UpsertSession(projectID, sessionID, issueID, daemonstate.SessionStateStarting); err != nil {
		t.Fatalf("seed daemon A starting session: %v", err)
	}
	if _, err := storeA.UpsertSession(projectID, sessionID, issueID, daemonstate.SessionStateAttached); err != nil {
		t.Fatalf("seed daemon A attached session: %v", err)
	}
	daemonA := &Daemon{
		cfg: Config{
			RepoDir: ".",
			CLITool: "claude",
			Logger:  slog.Default(),
		},
		tmux:         tmux.NewClient(tmuxRunner, slog.Default()),
		session:      daemonhandlers.NewSessionHandler(storeA),
		sessionStore: storeA,
		worktreeManagersByRoot: map[string]*git.WorktreeManager{
			".": git.NewWorktreeManager(&testGitRunner{worktreePath: "/tmp/proj-az-1", branchName: "riordan/az-1/test"}, ".", slog.Default()),
		},
		worktreeManagersByProject: map[string]*git.WorktreeManager{
			projectID: git.NewWorktreeManager(&testGitRunner{worktreePath: "/tmp/proj-az-1", branchName: "riordan/az-1/test"}, ".", slog.Default()),
		},
		runtimeStoresByRoot: map[string]*daemonstate.RuntimeStateStore{
			".": runtimeStateStore,
		},
	}

	storeB := daemonstate.NewStore()
	daemonB := &Daemon{
		cfg: Config{
			RepoDir: ".",
			CLITool: "claude",
			Logger:  slog.Default(),
		},
		tmux:         tmux.NewClient(tmuxRunner, slog.Default()),
		session:      daemonhandlers.NewSessionHandler(storeB),
		sessionStore: storeB,
		runtimeStoresByRoot: map[string]*daemonstate.RuntimeStateStore{
			".": runtimeStateStore,
		},
	}

	stopBody, err := json.Marshal(map[string]string{
		"project_id": projectID,
		"session_id": issueID,
	})
	if err != nil {
		t.Fatalf("marshal stop body: %v", err)
	}
	stopResp, err := daemonB.handleSessionStopDirect(context.Background(), protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-stop-daemon-b",
		Kind:            protocol.EnvelopeKindCommand,
		Command:         "session.stop",
		Body:            stopBody,
		Meta:            protocol.Metadata{ProjectID: naming.ProjectID(projectID)},
	})
	if err != nil {
		t.Fatalf("daemon B stop returned error: %v", err)
	}
	if !stopResp.OK {
		t.Fatalf("daemon B stop response not OK: %+v", stopResp.Error)
	}

	tmuxRunner.mu.Lock()
	_, runningAfterStop := tmuxRunner.sessions[sessionID]
	newSessionCallsBeforeReconcile := tmuxRunner.newSessionCalls
	tmuxRunner.mu.Unlock()
	if runningAfterStop {
		t.Fatalf("expected tmux session %q to be stopped by daemon B", sessionID)
	}

	result, err := daemonA.reconcileTmuxAndDaemonSessions(context.Background(), projectID, issueID)
	if err != nil {
		t.Fatalf("daemon A reconcile: %v", err)
	}
	if result.RecreatedTmuxSessions != 0 {
		t.Fatalf("recreated tmux sessions = %d, want 0", result.RecreatedTmuxSessions)
	}

	tmuxRunner.mu.Lock()
	newSessionCallsAfterReconcile := tmuxRunner.newSessionCalls
	_, runningAfterReconcile := tmuxRunner.sessions[sessionID]
	tmuxRunner.mu.Unlock()
	if newSessionCallsAfterReconcile != newSessionCallsBeforeReconcile {
		t.Fatalf("new-session calls increased from %d to %d after reconcile", newSessionCallsBeforeReconcile, newSessionCallsAfterReconcile)
	}
	if runningAfterReconcile {
		t.Fatalf("session %q resurrected unexpectedly", sessionID)
	}
}

func TestListTmuxSessionsCacheFirstUsesProjectionOnlyWhenCacheEmpty(t *testing.T) {
	const projectID = "proj-no-write-query"

	runtimeStateStore := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(t.TempDir(), "projections.db"), slog.Default())
	t.Cleanup(func() {
		_ = runtimeStateStore.Close()
	})

	sessionID := naming.CanonicalSessionID(projectID, "az-1")
	tmuxRunner := &testTmuxRunner{
		sessions: map[string]bool{
			sessionID: true,
		},
		killEntered: make(chan struct{}),
		killRelease: make(chan struct{}),
	}
	d := &Daemon{
		cfg:  Config{RepoDir: ".", Logger: slog.Default()},
		tmux: tmux.NewClient(tmuxRunner, slog.Default()),
		runtimeStoresByRoot: map[string]*daemonstate.RuntimeStateStore{
			".": runtimeStateStore,
		},
	}

	sessions, err := d.listTmuxSessionsCacheFirst(context.Background(), projectID)
	if err != nil {
		t.Fatalf("listTmuxSessionsCacheFirst returned error: %v", err)
	}
	if len(sessions) != 0 {
		t.Fatalf("sessions = %v, want empty when projection cache has no session rows", sessions)
	}

	rows, err := runtimeStateStore.ListSessionStates(context.Background(), projectID)
	if err != nil {
		t.Fatalf("ListSessions returned error: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("projection rows = %d, want 0 (read path must not persist)", len(rows))
	}
}

func TestListTmuxSessionsCacheFirstUsesObservedStateForActiveSet(t *testing.T) {
	const projectID = "proj-observed-runtime"

	runtimeStateStore := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(t.TempDir(), "projections.db"), slog.Default())
	t.Cleanup(func() {
		_ = runtimeStateStore.Close()
	})

	stoppedObservedID := naming.CanonicalSessionID(projectID, "az-1")
	attachedObservedID := naming.CanonicalSessionID(projectID, "az-2")
	now := time.Now().UTC()
	if err := upsertSessionStateFixture(runtimeStateStore, context.Background(), projectID, daemonstate.Session{
		ID:            stoppedObservedID,
		IssueID:       "az-1",
		State:         daemonstate.SessionStateAttached,
		ObservedState: daemonstate.SessionStateStopped,
		UpdatedAt:     now,
	}); err != nil {
		t.Fatalf("seed stopped-observed session: %v", err)
	}
	if err := upsertSessionStateFixture(runtimeStateStore, context.Background(), projectID, daemonstate.Session{
		ID:            attachedObservedID,
		IssueID:       "az-2",
		State:         daemonstate.SessionStateAttached,
		ObservedState: daemonstate.SessionStateAttached,
		UpdatedAt:     now,
	}); err != nil {
		t.Fatalf("seed attached-observed session: %v", err)
	}

	d := &Daemon{
		cfg: Config{RepoDir: ".", Logger: slog.Default()},
		runtimeStoresByRoot: map[string]*daemonstate.RuntimeStateStore{
			".": runtimeStateStore,
		},
	}

	sessions, err := d.listTmuxSessionsCacheFirst(context.Background(), projectID)
	if err != nil {
		t.Fatalf("listTmuxSessionsCacheFirst returned error: %v", err)
	}
	if len(sessions) != 1 || sessions[0] != attachedObservedID {
		t.Fatalf("sessions = %v, want [%q]", sessions, attachedObservedID)
	}
}

func TestSessionStatusIgnoresStaleProjectionWhenTmuxHasNoSession(t *testing.T) {
	ctx := context.Background()
	repoDir := t.TempDir()
	projectID, err := appconfig.ProjectIDForRoot(repoDir)
	if err != nil {
		t.Fatalf("ProjectIDForRoot: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(repoDir, ".azedarach"), 0o755); err != nil {
		t.Fatalf("mkdir .azedarach: %v", err)
	}

	issuesClient := newMigratedIssueClient(t, repoDir, slog.Default())
	t.Cleanup(func() {
		_ = issuesClient.CloseDB()
	})
	issueID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title: "Stale projected session",
		Type:  domain.TypeBug,
	})
	if err != nil {
		t.Fatalf("create issue: %v", err)
	}

	runtimeStateStore := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(t.TempDir(), "runtime.db"), slog.Default())
	t.Cleanup(func() {
		_ = runtimeStateStore.Close()
	})
	sessionID := naming.CanonicalSessionID(repoDir, issueID)
	if err := upsertSessionStateFixture(runtimeStateStore, ctx, projectID, daemonstate.Session{
		ID:            sessionID,
		IssueID:       issueID,
		State:         daemonstate.SessionStateAttached,
		ObservedState: daemonstate.SessionStateAttached,
		UpdatedAt:     time.Now().UTC(),
	}); err != nil {
		t.Fatalf("seed stale session projection: %v", err)
	}

	tmuxRunner := &testTmuxRunner{
		sessions:    map[string]bool{},
		killEntered: make(chan struct{}),
		killRelease: make(chan struct{}),
	}
	store := daemonstate.NewStore()
	daemon := &Daemon{
		cfg:          Config{RepoDir: repoDir, Logger: slog.Default()},
		tmux:         tmux.NewClient(tmuxRunner, slog.Default()),
		issues:       issuesClient,
		session:      daemonhandlers.NewSessionHandler(store),
		sessionStore: store,
		runtimeStoresByRoot: map[string]*daemonstate.RuntimeStateStore{
			repoDir: runtimeStateStore,
		},
		issueClientsByRoot: map[string]*issues.Client{
			repoDir: issuesClient,
		},
		issueClientsByProject: map[string]*issues.Client{
			projectID: issuesClient,
		},
	}
	if err := daemon.observeTmuxProject(ctx, projectID, newTmuxRuntimeLiveness(nil, nil), domain.CurrentTmuxObservationProvenance(time.Now().UTC().Add(time.Second))); err != nil {
		t.Fatalf("apply asynchronous missing-session observation: %v", err)
	}

	resp, err := daemon.handleSessionStatus(ctx, protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-status-stale-projection",
		Kind:            protocol.EnvelopeKindCommand,
		Command:         "session.status",
		Meta:            protocol.Metadata{ProjectID: naming.ProjectID(projectID)},
		Body:            marshalJSON(map[string]string{"project_id": projectID}),
	})
	if err != nil {
		t.Fatalf("handleSessionStatus returned error: %v", err)
	}
	if !resp.OK {
		t.Fatalf("session status response not OK: %+v", resp.Error)
	}
	var payload struct {
		Output string `json:"output"`
	}
	if err := json.Unmarshal(resp.Body, &payload); err != nil {
		t.Fatalf("unmarshal status response: %v", err)
	}
	if strings.Contains(payload.Output, "Active Sessions") {
		t.Fatalf("status output reported stale projection: %q", payload.Output)
	}
	if !strings.Contains(payload.Output, "No active sessions") {
		t.Fatalf("status output = %q, want no active sessions", payload.Output)
	}
	if got := tmuxRunner.listSessionCallCount(); got != 0 {
		t.Fatalf("session.status tmux inventory calls = %d, want 0", got)
	}
}

func TestSessionStatusReportsHookBackedActivity(t *testing.T) {
	ctx := context.Background()
	repoDir := t.TempDir()
	projectID, err := appconfig.ProjectIDForRoot(repoDir)
	if err != nil {
		t.Fatalf("ProjectIDForRoot: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(repoDir, ".azedarach"), 0o755); err != nil {
		t.Fatalf("mkdir .azedarach: %v", err)
	}

	issuesClient := newMigratedIssueClient(t, repoDir, slog.Default())
	t.Cleanup(func() { _ = issuesClient.CloseDB() })
	busyIssueID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title:  "Busy worker",
		Type:   domain.TypeTask,
		Status: domain.StatusInProgress,
	})
	if err != nil {
		t.Fatalf("create busy issue: %v", err)
	}
	idleIssueID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title:  "Idle worker",
		Type:   domain.TypeTask,
		Status: domain.StatusInProgress,
	})
	if err != nil {
		t.Fatalf("create idle issue: %v", err)
	}

	busySessionID := naming.CanonicalSessionID(repoDir, busyIssueID)
	idleSessionID := naming.CanonicalSessionID(repoDir, idleIssueID)
	runtimeStateStore := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(t.TempDir(), "runtime.db"), slog.Default())
	t.Cleanup(func() { _ = runtimeStateStore.Close() })
	now := time.Now().UTC()
	if err := upsertSessionStateFixture(runtimeStateStore, ctx, projectID, daemonstate.Session{
		ID:             busySessionID + ".pane-%1",
		IssueID:        busyIssueID,
		State:          daemonstate.SessionStateRunning,
		ObservedState:  daemonstate.SessionStateRunning,
		Activity:       "busy",
		ActivitySource: "hooks",
		UpdatedAt:      now,
	}); err != nil {
		t.Fatalf("seed busy hook activity: %v", err)
	}
	if err := upsertSessionStateFixture(runtimeStateStore, ctx, projectID, daemonstate.Session{
		ID:             idleSessionID + ".pane-%2",
		IssueID:        idleIssueID,
		State:          daemonstate.SessionStatePaused,
		ObservedState:  daemonstate.SessionStatePaused,
		Activity:       "idle",
		ActivitySource: "hooks",
		UpdatedAt:      now,
	}); err != nil {
		t.Fatalf("seed idle hook activity: %v", err)
	}

	daemon := &Daemon{
		cfg:          Config{RepoDir: repoDir, Logger: slog.Default()},
		tmux:         tmux.NewClient(&testTmuxRunner{sessions: map[string]bool{busySessionID: true, idleSessionID: true}}, slog.Default()),
		issues:       issuesClient,
		session:      daemonhandlers.NewSessionHandler(daemonstate.NewStore()),
		sessionStore: daemonstate.NewStore(),
		runtimeStoresByRoot: map[string]*daemonstate.RuntimeStateStore{
			repoDir: runtimeStateStore,
		},
		issueClientsByProject: map[string]*issues.Client{
			projectID: issuesClient,
		},
	}

	resp, err := daemon.handleSessionStatus(ctx, protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-status-hook-activity",
		Kind:            protocol.EnvelopeKindCommand,
		Command:         "session.status",
		Meta:            protocol.Metadata{ProjectID: naming.ProjectID(projectID)},
		Body:            marshalJSON(map[string]string{"project_id": projectID}),
	})
	if err != nil {
		t.Fatalf("handleSessionStatus returned error: %v", err)
	}
	if !resp.OK {
		t.Fatalf("session status response not OK: %+v", resp.Error)
	}
	var payload struct {
		Output string `json:"output"`
	}
	if err := json.Unmarshal(resp.Body, &payload); err != nil {
		t.Fatalf("unmarshal status response: %v", err)
	}
	if !strings.Contains(payload.Output, "ISSUE ID\tSTATUS\tACTIVITY\tTITLE") {
		t.Fatalf("status output = %q, want activity header", payload.Output)
	}
	if !strings.Contains(payload.Output, busyIssueID+"\tin_progress\tbusy\tBusy worker") {
		t.Fatalf("status output = %q, want busy hook-backed activity", payload.Output)
	}
	if !strings.Contains(payload.Output, idleIssueID+"\tin_progress\tidle\tIdle worker") {
		t.Fatalf("status output = %q, want idle hook-backed activity", payload.Output)
	}
}

func TestSessionStatusReportsPendingSessionStartProgress(t *testing.T) {
	ctx := context.Background()
	repoDir := t.TempDir()
	projectID, err := appconfig.ProjectIDForRoot(repoDir)
	if err != nil {
		t.Fatalf("ProjectIDForRoot: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(repoDir, ".azedarach"), 0o755); err != nil {
		t.Fatalf("mkdir .azedarach: %v", err)
	}
	issuesClient := newMigratedIssueClient(t, repoDir, slog.Default())
	t.Cleanup(func() { _ = issuesClient.CloseDB() })
	issueID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title:  "Launching worker",
		Type:   domain.TypeTask,
		Status: domain.StatusOpen,
	})
	if err != nil {
		t.Fatalf("create issue: %v", err)
	}

	runtime := newOperationRuntime(operationRuntimeConfig{repoDir: repoDir, nextRevision: sequentialRevision()})
	started := make(chan struct{})
	release := make(chan struct{})
	defer close(release)
	if _, err := runtime.manager.Submit(ctx, daemonops.SubmitRequest{
		ID:           "op-session-start",
		ProjectID:    projectID,
		IssueID:      issueID,
		Kind:         daemonhandlers.CommandSessionStart,
		ResourceKeys: []string{"issue:" + projectID + ":" + issueID},
	}, func(ctx context.Context) ([]byte, error) {
		_ = daemonops.ReportProgress(ctx, daemonops.Progress{
			Phase:   "issue_resources",
			Message: "preparing issue resources",
			Current: 50,
			Total:   100,
			Unit:    "percent",
			Percent: 50,
		})
		close(started)
		<-release
		return nil, nil
	}); err != nil {
		t.Fatalf("submit session start operation: %v", err)
	}
	<-started
	waitForRuntimeProgress(t, runtime, "op-session-start", "issue_resources")

	daemon := &Daemon{
		cfg:              Config{RepoDir: repoDir, Logger: slog.Default()},
		tmux:             tmux.NewClient(&testTmuxRunner{sessions: map[string]bool{}}, slog.Default()),
		issues:           issuesClient,
		operationRuntime: runtime,
		issueClientsByProject: map[string]*issues.Client{
			projectID: issuesClient,
		},
	}
	resp, err := daemon.handleSessionStatus(ctx, protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-status-pending-start",
		Kind:            protocol.EnvelopeKindCommand,
		Command:         "session.status",
		Meta:            protocol.Metadata{ProjectID: naming.ProjectID(projectID)},
		Body:            marshalJSON(map[string]string{"project_id": projectID, "session_id": issueID}),
	})
	if err != nil {
		t.Fatalf("handleSessionStatus returned error: %v", err)
	}
	if !resp.OK {
		t.Fatalf("session status response not OK: %+v", resp.Error)
	}
	var payload struct {
		Output string `json:"output"`
	}
	if err := json.Unmarshal(resp.Body, &payload); err != nil {
		t.Fatalf("unmarshal status response: %v", err)
	}
	for _, want := range []string{
		"Session start progress:",
		issueID + ": state=running phase=issue_resources operation=op-session-start",
		"preparing issue resources",
	} {
		if !strings.Contains(payload.Output, want) {
			t.Fatalf("status output missing %q:\n%s", want, payload.Output)
		}
	}
}

func TestSessionStatusReportsUnknownActivityWithoutHookRows(t *testing.T) {
	ctx := context.Background()
	repoDir := t.TempDir()
	projectID, err := appconfig.ProjectIDForRoot(repoDir)
	if err != nil {
		t.Fatalf("ProjectIDForRoot: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(repoDir, ".azedarach"), 0o755); err != nil {
		t.Fatalf("mkdir .azedarach: %v", err)
	}

	issuesClient := newMigratedIssueClient(t, repoDir, slog.Default())
	t.Cleanup(func() { _ = issuesClient.CloseDB() })
	issueID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title:  "Worker without hooks",
		Type:   domain.TypeTask,
		Status: domain.StatusInProgress,
	})
	if err != nil {
		t.Fatalf("create issue: %v", err)
	}

	sessionID := naming.CanonicalSessionID(repoDir, issueID)
	runtimeStateStore := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(t.TempDir(), "runtime.db"), slog.Default())
	t.Cleanup(func() { _ = runtimeStateStore.Close() })
	if err := upsertSessionStateFixture(runtimeStateStore, ctx, projectID, daemonstate.Session{
		ID:            sessionID,
		IssueID:       issueID,
		State:         daemonstate.SessionStateRunning,
		ObservedState: daemonstate.SessionStateRunning,
		UpdatedAt:     time.Now().UTC(),
	}); err != nil {
		t.Fatalf("seed non-hook session projection: %v", err)
	}

	daemon := &Daemon{
		cfg:          Config{RepoDir: repoDir, Logger: slog.Default()},
		tmux:         tmux.NewClient(&testTmuxRunner{sessions: map[string]bool{sessionID: true}}, slog.Default()),
		issues:       issuesClient,
		session:      daemonhandlers.NewSessionHandler(daemonstate.NewStore()),
		sessionStore: daemonstate.NewStore(),
		runtimeStoresByRoot: map[string]*daemonstate.RuntimeStateStore{
			repoDir: runtimeStateStore,
		},
		issueClientsByProject: map[string]*issues.Client{
			projectID: issuesClient,
		},
	}

	resp, err := daemon.handleSessionStatus(ctx, protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-status-unknown-activity",
		Kind:            protocol.EnvelopeKindCommand,
		Command:         "session.status",
		Meta:            protocol.Metadata{ProjectID: naming.ProjectID(projectID)},
		Body:            marshalJSON(map[string]string{"project_id": projectID}),
	})
	if err != nil {
		t.Fatalf("handleSessionStatus returned error: %v", err)
	}
	if !resp.OK {
		t.Fatalf("session status response not OK: %+v", resp.Error)
	}
	var payload struct {
		Output string `json:"output"`
	}
	if err := json.Unmarshal(resp.Body, &payload); err != nil {
		t.Fatalf("unmarshal status response: %v", err)
	}
	if !strings.Contains(payload.Output, issueID+"\tin_progress\tunknown\tWorker without hooks") {
		t.Fatalf("status output = %q, want unknown activity without hook-scoped rows", payload.Output)
	}
}

func TestSessionStatusReportsNoAgentActivity(t *testing.T) {
	ctx := context.Background()
	repoDir := t.TempDir()
	projectID, err := appconfig.ProjectIDForRoot(repoDir)
	if err != nil {
		t.Fatalf("ProjectIDForRoot: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(repoDir, ".azedarach"), 0o755); err != nil {
		t.Fatalf("mkdir .azedarach: %v", err)
	}

	issuesClient := newMigratedIssueClient(t, repoDir, slog.Default())
	t.Cleanup(func() { _ = issuesClient.CloseDB() })
	issueID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title:  "Shell only session",
		Type:   domain.TypeTask,
		Status: domain.StatusInProgress,
	})
	if err != nil {
		t.Fatalf("create issue: %v", err)
	}

	sessionID := naming.CanonicalSessionID(repoDir, issueID)
	runtimeStateStore := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(t.TempDir(), "runtime.db"), slog.Default())
	t.Cleanup(func() { _ = runtimeStateStore.Close() })
	if err := upsertSessionStateFixture(runtimeStateStore, ctx, projectID, daemonstate.Session{
		ID:             sessionID,
		IssueID:        issueID,
		State:          daemonstate.SessionStateRunning,
		ObservedState:  daemonstate.SessionStateRunning,
		Activity:       "no-agent",
		ActivitySource: "session",
		UpdatedAt:      time.Now().UTC(),
	}); err != nil {
		t.Fatalf("seed no-agent session projection: %v", err)
	}

	daemon := &Daemon{
		cfg:          Config{RepoDir: repoDir, Logger: slog.Default()},
		tmux:         tmux.NewClient(&testTmuxRunner{sessions: map[string]bool{sessionID: true}}, slog.Default()),
		issues:       issuesClient,
		session:      daemonhandlers.NewSessionHandler(daemonstate.NewStore()),
		sessionStore: daemonstate.NewStore(),
		runtimeStoresByRoot: map[string]*daemonstate.RuntimeStateStore{
			repoDir: runtimeStateStore,
		},
		issueClientsByProject: map[string]*issues.Client{
			projectID: issuesClient,
		},
	}

	resp, err := daemon.handleSessionStatus(ctx, protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-status-no-agent-activity",
		Kind:            protocol.EnvelopeKindCommand,
		Command:         "session.status",
		Meta:            protocol.Metadata{ProjectID: naming.ProjectID(projectID)},
		Body:            marshalJSON(map[string]string{"project_id": projectID}),
	})
	if err != nil {
		t.Fatalf("handleSessionStatus returned error: %v", err)
	}
	if !resp.OK {
		t.Fatalf("session status response not OK: %+v", resp.Error)
	}
	var payload struct {
		Output string `json:"output"`
	}
	if err := json.Unmarshal(resp.Body, &payload); err != nil {
		t.Fatalf("unmarshal status response: %v", err)
	}
	if !strings.Contains(payload.Output, issueID+"\tin_progress\tno-agent\tShell only session") {
		t.Fatalf("status output = %q, want no-agent activity", payload.Output)
	}
}

func TestSessionStatusReportsStaleRuntimeForTargetIssue(t *testing.T) {
	ctx := context.Background()
	repoDir := t.TempDir()
	projectID, err := appconfig.ProjectIDForRoot(repoDir)
	if err != nil {
		t.Fatalf("ProjectIDForRoot: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(repoDir, ".azedarach"), 0o755); err != nil {
		t.Fatalf("mkdir .azedarach: %v", err)
	}

	issuesClient := newMigratedIssueClient(t, repoDir, slog.Default())
	t.Cleanup(func() { _ = issuesClient.CloseDB() })
	issueID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title:  "Target stale projected session",
		Type:   domain.TypeBug,
		Status: domain.StatusInProgress,
	})
	if err != nil {
		t.Fatalf("create issue: %v", err)
	}

	runtimeStateStore := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(t.TempDir(), "runtime.db"), slog.Default())
	t.Cleanup(func() { _ = runtimeStateStore.Close() })
	sessionID := naming.CanonicalSessionID(repoDir, issueID)
	paneSessionID := sessionID + ".pane-190"
	now := time.Now().UTC()
	if err := upsertSessionStateFixture(runtimeStateStore, ctx, projectID, daemonstate.Session{
		ID:            sessionID,
		IssueID:       issueID,
		State:         daemonstate.SessionStateAttached,
		ObservedState: daemonstate.SessionStateStopped,
		UpdatedAt:     now,
	}); err != nil {
		t.Fatalf("seed stale session projection: %v", err)
	}
	if err := upsertSessionStateFixture(runtimeStateStore, ctx, projectID, daemonstate.Session{
		ID:            paneSessionID,
		IssueID:       issueID,
		State:         daemonstate.SessionStatePaused,
		ObservedState: daemonstate.SessionStateStopped,
		UpdatedAt:     now.Add(time.Minute),
	}); err != nil {
		t.Fatalf("seed newer stale pane projection: %v", err)
	}

	daemon := &Daemon{
		cfg:          Config{RepoDir: repoDir, Logger: slog.Default()},
		tmux:         tmux.NewClient(&testTmuxRunner{sessions: map[string]bool{}}, slog.Default()),
		issues:       issuesClient,
		session:      daemonhandlers.NewSessionHandler(daemonstate.NewStore()),
		sessionStore: daemonstate.NewStore(),
		runtimeStoresByRoot: map[string]*daemonstate.RuntimeStateStore{
			repoDir: runtimeStateStore,
		},
		issueClientsByProject: map[string]*issues.Client{
			projectID: issuesClient,
		},
	}

	resp, err := daemon.handleSessionStatus(ctx, protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-status-target-stale-projection",
		Kind:            protocol.EnvelopeKindCommand,
		Command:         "session.status",
		Meta:            protocol.Metadata{ProjectID: naming.ProjectID(projectID)},
		Body:            marshalJSON(map[string]string{"project_id": projectID, "session_id": issueID}),
	})
	if err != nil {
		t.Fatalf("handleSessionStatus returned error: %v", err)
	}
	if !resp.OK {
		t.Fatalf("session status response not OK: %+v", resp.Error)
	}
	var payload struct {
		Output string `json:"output"`
	}
	if err := json.Unmarshal(resp.Body, &payload); err != nil {
		t.Fatalf("unmarshal status response: %v", err)
	}
	if !strings.Contains(payload.Output, "Stale runtime session for "+issueID) {
		t.Fatalf("status output = %q, want stale runtime diagnostic", payload.Output)
	}
	if !strings.Contains(payload.Output, fmt.Sprintf("session %q", sessionID)) {
		t.Fatalf("status output = %q, want parent session %q", payload.Output, sessionID)
	}
	if strings.Contains(payload.Output, paneSessionID) {
		t.Fatalf("status output = %q, should not report helper pane session %q", payload.Output, paneSessionID)
	}
	if !strings.Contains(payload.Output, "az orchestrate close-session --issue "+issueID) {
		t.Fatalf("status output = %q, want repair command", payload.Output)
	}
}

func TestSessionStatusDoesNotReportDesiredStoppedRuntimeAsStale(t *testing.T) {
	ctx := context.Background()
	repoDir := t.TempDir()
	projectID, err := appconfig.ProjectIDForRoot(repoDir)
	if err != nil {
		t.Fatalf("ProjectIDForRoot: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(repoDir, ".azedarach"), 0o755); err != nil {
		t.Fatalf("mkdir .azedarach: %v", err)
	}

	issuesClient := newMigratedIssueClient(t, repoDir, slog.Default())
	t.Cleanup(func() { _ = issuesClient.CloseDB() })
	issueID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title:  "Stopped projected session",
		Type:   domain.TypeBug,
		Status: domain.StatusInProgress,
	})
	if err != nil {
		t.Fatalf("create issue: %v", err)
	}

	runtimeStateStore := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(t.TempDir(), "runtime.db"), slog.Default())
	t.Cleanup(func() { _ = runtimeStateStore.Close() })
	sessionID := naming.CanonicalSessionID(repoDir, issueID)
	if err := upsertSessionStateFixture(runtimeStateStore, ctx, projectID, daemonstate.Session{
		ID:            sessionID,
		IssueID:       issueID,
		State:         daemonstate.SessionStateStopped,
		ObservedState: daemonstate.SessionStateStopped,
		UpdatedAt:     time.Now().UTC(),
	}); err != nil {
		t.Fatalf("seed stopped session projection: %v", err)
	}

	daemon := &Daemon{
		cfg:          Config{RepoDir: repoDir, Logger: slog.Default()},
		tmux:         tmux.NewClient(&testTmuxRunner{sessions: map[string]bool{}}, slog.Default()),
		issues:       issuesClient,
		session:      daemonhandlers.NewSessionHandler(daemonstate.NewStore()),
		sessionStore: daemonstate.NewStore(),
		runtimeStoresByRoot: map[string]*daemonstate.RuntimeStateStore{
			repoDir: runtimeStateStore,
		},
		issueClientsByProject: map[string]*issues.Client{
			projectID: issuesClient,
		},
	}

	resp, err := daemon.handleSessionStatus(ctx, protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-status-target-stopped-projection",
		Kind:            protocol.EnvelopeKindCommand,
		Command:         "session.status",
		Meta:            protocol.Metadata{ProjectID: naming.ProjectID(projectID)},
		Body:            marshalJSON(map[string]string{"project_id": projectID}),
	})
	if err != nil {
		t.Fatalf("handleSessionStatus returned error: %v", err)
	}
	if !resp.OK {
		t.Fatalf("session status response not OK: %+v", resp.Error)
	}
	var payload struct {
		Output string `json:"output"`
	}
	if err := json.Unmarshal(resp.Body, &payload); err != nil {
		t.Fatalf("unmarshal status response: %v", err)
	}
	if strings.Contains(payload.Output, "Stale runtime session for "+issueID) {
		t.Fatalf("status output = %q, should not report desired stopped runtime as stale", payload.Output)
	}
}

func TestRefreshSessionRuntimeStateIgnoresForeignProjectPrefixedTmuxSessions(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	repoDir := filepath.Join(root, "Chefy")
	if err := os.MkdirAll(filepath.Join(repoDir, ".azedarach"), 0o755); err != nil {
		t.Fatalf("mkdir repo: %v", err)
	}
	projectID, err := appconfig.ProjectIDForRoot(repoDir)
	if err != nil {
		t.Fatalf("ProjectIDForRoot: %v", err)
	}

	runtimeStateStore := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(t.TempDir(), "runtime.db"), slog.Default())
	t.Cleanup(func() {
		_ = runtimeStateStore.Close()
	})
	store := daemonstate.NewStore()
	tmuxRunner := newTestTmuxRunner("az-ciw")
	daemon := &Daemon{
		cfg:          Config{RepoDir: repoDir, Logger: slog.Default()},
		tmux:         tmux.NewClient(tmuxRunner, slog.Default()),
		session:      daemonhandlers.NewSessionHandler(store),
		sessionStore: store,
		runtimeStoresByRoot: map[string]*daemonstate.RuntimeStateStore{
			repoDir: runtimeStateStore,
		},
	}

	if err := daemon.refreshSessionRuntimeState(ctx, projectID); err != nil {
		t.Fatalf("refreshSessionRuntimeState: %v", err)
	}
	rows, err := runtimeStateStore.ListSessionStates(ctx, projectID)
	if err != nil {
		t.Fatalf("list session states: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("projection rows = %+v, want none for foreign az-ciw session in Chefy project", rows)
	}
}

func TestRefreshExistingSessionRuntimeStateStopsDeadPaneRows(t *testing.T) {
	const (
		projectID = "proj-pane-dead"
		issueID   = "az-1"
	)
	ctx := context.Background()
	parentSessionID := naming.CanonicalSessionID(projectID, issueID)
	deadPaneSessionID := parentSessionID + ".pane-1"
	runtimeStateStore := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(t.TempDir(), "runtime.db"), slog.Default())
	t.Cleanup(func() { _ = runtimeStateStore.Close() })
	if err := upsertSessionStateFixture(runtimeStateStore, ctx, projectID, daemonstate.Session{
		ID:            deadPaneSessionID,
		IssueID:       issueID,
		State:         daemonstate.SessionStateRunning,
		ObservedState: daemonstate.SessionStateRunning,
		UpdatedAt:     time.Now().UTC(),
	}); err != nil {
		t.Fatalf("seed dead pane projection: %v", err)
	}
	tmuxRunner := newTestTmuxRunner(parentSessionID)
	tmuxRunner.panes[parentSessionID] = []string{"%2"}
	d := &Daemon{
		cfg:          Config{RepoDir: ".", Logger: slog.Default()},
		tmux:         tmux.NewClient(tmuxRunner, slog.Default()),
		sessionStore: daemonstate.NewStore(),
		runtimeStoresByRoot: map[string]*daemonstate.RuntimeStateStore{
			".": runtimeStateStore,
		},
	}

	if err := d.refreshExistingSessionRuntimeState(ctx, projectID); err != nil {
		t.Fatalf("refreshExistingSessionRuntimeState: %v", err)
	}

	row, found, err := runtimeStateStore.GetSessionState(ctx, projectID, deadPaneSessionID)
	if err != nil {
		t.Fatalf("get dead pane row: %v", err)
	}
	if !found {
		t.Fatal("dead pane row not found")
	}
	if row.ObservedState != daemonstate.SessionStateStopped {
		t.Fatalf("dead pane observed state = %s, want %s", row.ObservedState, daemonstate.SessionStateStopped)
	}
	counts := d.sessionProjectionCountsForIssue(ctx, projectID, issueID)
	if counts.Total != 0 || counts.Active != 0 || counts.Paused != 0 {
		t.Fatalf("counts = %+v, want no live rows", counts)
	}
}

func TestRefreshExistingSessionRuntimeStateKeepsLivePaneRowsBusy(t *testing.T) {
	const (
		projectID = "proj-pane-live"
		issueID   = "az-1"
	)
	ctx := context.Background()
	parentSessionID := naming.CanonicalSessionID(projectID, issueID)
	paneSessionID := parentSessionID + ".pane-12"
	runtimeStateStore := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(t.TempDir(), "runtime.db"), slog.Default())
	t.Cleanup(func() { _ = runtimeStateStore.Close() })
	if err := upsertSessionStateFixture(runtimeStateStore, ctx, projectID, daemonstate.Session{
		ID:             paneSessionID,
		IssueID:        issueID,
		State:          daemonstate.SessionStateRunning,
		ObservedState:  daemonstate.SessionStateRunning,
		Activity:       "busy",
		ActivitySource: "hooks",
		UpdatedAt:      time.Now().UTC(),
	}); err != nil {
		t.Fatalf("seed live pane projection: %v", err)
	}
	tmuxRunner := newTestTmuxRunner(parentSessionID)
	tmuxRunner.panes[parentSessionID] = []string{"%12"}
	d := &Daemon{
		cfg:          Config{RepoDir: ".", Logger: slog.Default()},
		tmux:         tmux.NewClient(tmuxRunner, slog.Default()),
		sessionStore: daemonstate.NewStore(),
		runtimeStoresByRoot: map[string]*daemonstate.RuntimeStateStore{
			".": runtimeStateStore,
		},
	}

	if err := d.refreshExistingSessionRuntimeState(ctx, projectID); err != nil {
		t.Fatalf("refreshExistingSessionRuntimeState: %v", err)
	}

	row, found, err := runtimeStateStore.GetSessionState(ctx, projectID, paneSessionID)
	if err != nil {
		t.Fatalf("get live pane row: %v", err)
	}
	if !found {
		t.Fatal("live pane row not found")
	}
	if row.ObservedState != daemonstate.SessionStateRunning {
		t.Fatalf("live pane observed state = %s, want %s", row.ObservedState, daemonstate.SessionStateRunning)
	}
	counts := d.sessionProjectionCountsForIssue(ctx, projectID, issueID)
	if counts.Total != 1 || counts.Active != 1 || counts.Paused != 0 {
		t.Fatalf("counts = %+v, want one active live row", counts)
	}
}

func TestEnrichTasksWithSessionStateTreatsLegacyPaneRowsWithoutActivityAsIdle(t *testing.T) {
	const (
		projectID = "proj-legacy-pane-activity"
		issueID   = "az-1"
	)
	ctx := context.Background()
	parentSessionID := naming.CanonicalSessionID(projectID, issueID)
	runtimeStateStore := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(t.TempDir(), "runtime.db"), slog.Default())
	t.Cleanup(func() { _ = runtimeStateStore.Close() })
	if err := upsertSessionStateFixture(runtimeStateStore, ctx, projectID, daemonstate.Session{
		ID:             parentSessionID,
		IssueID:        issueID,
		State:          daemonstate.SessionStateRunning,
		ObservedState:  daemonstate.SessionStateRunning,
		Activity:       "busy",
		ActivitySource: "session",
		UpdatedAt:      time.Now().UTC(),
	}); err != nil {
		t.Fatalf("seed parent projection: %v", err)
	}
	if err := upsertSessionStateFixture(runtimeStateStore, ctx, projectID, daemonstate.Session{
		ID:            parentSessionID + ".pane-506",
		IssueID:       issueID,
		State:         daemonstate.SessionStateRunning,
		ObservedState: daemonstate.SessionStateRunning,
		UpdatedAt:     time.Now().UTC().Add(time.Second),
	}); err != nil {
		t.Fatalf("seed legacy pane projection: %v", err)
	}
	d := &Daemon{
		cfg:          Config{RepoDir: ".", Logger: slog.Default()},
		sessionStore: daemonstate.NewStore(),
		runtimeStoresByRoot: map[string]*daemonstate.RuntimeStateStore{
			".": runtimeStateStore,
		},
	}

	counts := d.sessionProjectionCountsForIssue(ctx, projectID, issueID)
	if counts.Total != 1 || counts.Active != 0 || counts.Paused != 1 {
		t.Fatalf("counts = %+v, want one idle legacy pane row", counts)
	}

	tasks := []domain.Task{{ID: issueID, Title: "legacy pane row", Type: domain.TypeTask}}
	enriched := d.enrichTasksWithSessionState(ctx, projectID, tasks)
	if len(enriched) != 1 || enriched[0].Session == nil {
		t.Fatalf("missing session in enriched task: %+v", enriched)
	}
	if enriched[0].Session.State != domain.SessionPaused {
		t.Fatalf("enriched session state = %s, want %s", enriched[0].Session.State, domain.SessionPaused)
	}
	if enriched[0].Session.Activity != "idle" || enriched[0].Session.ActivitySource != "hooks" {
		t.Fatalf("activity = %s/%s, want idle/hooks", enriched[0].Session.Activity, enriched[0].Session.ActivitySource)
	}
}

func TestRefreshExistingSessionRuntimeStateMatchesUnsanitizedPersistedPaneID(t *testing.T) {
	const (
		projectID = "proj-pane-unsanitized"
		issueID   = "az-1"
	)
	ctx := context.Background()
	parentSessionID := naming.CanonicalSessionID(projectID, issueID)
	paneSessionID := parentSessionID + ".pane-%12"
	runtimeStateStore := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(t.TempDir(), "runtime.db"), slog.Default())
	t.Cleanup(func() { _ = runtimeStateStore.Close() })
	if err := upsertSessionStateFixture(runtimeStateStore, ctx, projectID, daemonstate.Session{
		ID:             paneSessionID,
		IssueID:        issueID,
		State:          daemonstate.SessionStateRunning,
		ObservedState:  daemonstate.SessionStateRunning,
		Activity:       "busy",
		ActivitySource: "hooks",
		UpdatedAt:      time.Now().UTC(),
	}); err != nil {
		t.Fatalf("seed live pane projection: %v", err)
	}
	tmuxRunner := newTestTmuxRunner(parentSessionID)
	tmuxRunner.panes[parentSessionID] = []string{"%12"}
	d := &Daemon{
		cfg:          Config{RepoDir: ".", Logger: slog.Default()},
		tmux:         tmux.NewClient(tmuxRunner, slog.Default()),
		sessionStore: daemonstate.NewStore(),
		runtimeStoresByRoot: map[string]*daemonstate.RuntimeStateStore{
			".": runtimeStateStore,
		},
	}

	if err := d.refreshExistingSessionRuntimeState(ctx, projectID); err != nil {
		t.Fatalf("refreshExistingSessionRuntimeState: %v", err)
	}

	row, found, err := runtimeStateStore.GetSessionState(ctx, projectID, paneSessionID)
	if err != nil {
		t.Fatalf("get live pane row: %v", err)
	}
	if !found {
		t.Fatal("live pane row not found")
	}
	if row.ObservedState != daemonstate.SessionStateRunning {
		t.Fatalf("live pane observed state = %s, want %s", row.ObservedState, daemonstate.SessionStateRunning)
	}
	counts := d.sessionProjectionCountsForIssue(ctx, projectID, issueID)
	if counts.Total != 1 || counts.Active != 1 || counts.Paused != 0 {
		t.Fatalf("counts = %+v, want one active live row", counts)
	}
}

func TestRefreshExistingSessionRuntimeStateCountsMultipleLivePanes(t *testing.T) {
	const (
		projectID = "proj-pane-multi"
		issueID   = "az-1"
	)
	ctx := context.Background()
	parentSessionID := naming.CanonicalSessionID(projectID, issueID)
	runtimeStateStore := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(t.TempDir(), "runtime.db"), slog.Default())
	t.Cleanup(func() { _ = runtimeStateStore.Close() })
	now := time.Now().UTC()
	for _, paneSessionID := range []string{parentSessionID + ".pane-1", parentSessionID + ".pane-2"} {
		if err := upsertSessionStateFixture(runtimeStateStore, ctx, projectID, daemonstate.Session{
			ID:             paneSessionID,
			IssueID:        issueID,
			State:          daemonstate.SessionStateRunning,
			ObservedState:  daemonstate.SessionStateRunning,
			Activity:       "busy",
			ActivitySource: "hooks",
			UpdatedAt:      now,
		}); err != nil {
			t.Fatalf("seed live pane projection %s: %v", paneSessionID, err)
		}
	}
	tmuxRunner := newTestTmuxRunner(parentSessionID)
	tmuxRunner.panes[parentSessionID] = []string{"%1", "%2"}
	d := &Daemon{
		cfg:          Config{RepoDir: ".", Logger: slog.Default()},
		tmux:         tmux.NewClient(tmuxRunner, slog.Default()),
		sessionStore: daemonstate.NewStore(),
		runtimeStoresByRoot: map[string]*daemonstate.RuntimeStateStore{
			".": runtimeStateStore,
		},
	}

	if err := d.refreshExistingSessionRuntimeState(ctx, projectID); err != nil {
		t.Fatalf("refreshExistingSessionRuntimeState: %v", err)
	}

	counts := d.sessionProjectionCountsForIssue(ctx, projectID, issueID)
	if counts.Total != 2 || counts.Active != 2 || counts.Paused != 0 {
		t.Fatalf("counts = %+v, want two active live rows", counts)
	}
}

func TestRefreshExistingSessionRuntimeStatePausedLivePaneWithDeadRunningPaneIsIdle(t *testing.T) {
	const (
		projectID = "proj-pane-idle"
		issueID   = "az-1"
	)
	ctx := context.Background()
	parentSessionID := naming.CanonicalSessionID(projectID, issueID)
	oldPaneSessionID := parentSessionID + ".pane-1"
	currentPaneSessionID := parentSessionID + ".pane-2"
	runtimeStateStore := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(t.TempDir(), "runtime.db"), slog.Default())
	t.Cleanup(func() { _ = runtimeStateStore.Close() })
	now := time.Now().UTC()
	if err := upsertSessionStateFixture(runtimeStateStore, ctx, projectID, daemonstate.Session{
		ID:            oldPaneSessionID,
		IssueID:       issueID,
		State:         daemonstate.SessionStateRunning,
		ObservedState: daemonstate.SessionStateRunning,
		UpdatedAt:     now,
	}); err != nil {
		t.Fatalf("seed old running pane projection: %v", err)
	}
	if err := upsertSessionStateFixture(runtimeStateStore, ctx, projectID, daemonstate.Session{
		ID:            currentPaneSessionID,
		IssueID:       issueID,
		State:         daemonstate.SessionStatePaused,
		ObservedState: daemonstate.SessionStatePaused,
		UpdatedAt:     now,
	}); err != nil {
		t.Fatalf("seed current paused pane projection: %v", err)
	}
	tmuxRunner := newTestTmuxRunner(parentSessionID)
	tmuxRunner.panes[parentSessionID] = []string{"%2"}
	d := &Daemon{
		cfg:          Config{RepoDir: ".", Logger: slog.Default()},
		tmux:         tmux.NewClient(tmuxRunner, slog.Default()),
		sessionStore: daemonstate.NewStore(),
		runtimeStoresByRoot: map[string]*daemonstate.RuntimeStateStore{
			".": runtimeStateStore,
		},
	}

	if err := d.refreshExistingSessionRuntimeState(ctx, projectID); err != nil {
		t.Fatalf("refreshExistingSessionRuntimeState: %v", err)
	}

	oldRow, found, err := runtimeStateStore.GetSessionState(ctx, projectID, oldPaneSessionID)
	if err != nil {
		t.Fatalf("get old pane row: %v", err)
	}
	if !found {
		t.Fatal("old pane row not found")
	}
	if oldRow.ObservedState != daemonstate.SessionStateStopped {
		t.Fatalf("old pane observed state = %s, want %s", oldRow.ObservedState, daemonstate.SessionStateStopped)
	}
	counts := d.sessionProjectionCountsForIssue(ctx, projectID, issueID)
	if counts.Total != 1 || counts.Active != 0 || counts.Paused != 1 {
		t.Fatalf("counts = %+v, want one paused live row", counts)
	}
}

func collectSessionProjectionEvents(t *testing.T, ch <-chan protocol.EventEnvelope, count int) []protocol.EventEnvelope {
	t.Helper()
	events := make([]protocol.EventEnvelope, 0, count)
	deadline := time.After(500 * time.Millisecond)
	for len(events) < count {
		select {
		case evt := <-ch:
			events = append(events, evt)
		case <-deadline:
			t.Fatalf("timed out waiting for %d session events, got %d", count, len(events))
		}
	}
	return events
}

func TestBuildSessionLaunchCommandIncludesInitCommandsAndIssueEnv(t *testing.T) {
	d := &Daemon{
		cfg: Config{
			CLITool:      "claude",
			SessionShell: "zsh",
			SessionSyncInitCommands: []string{
				"direnv allow",
				"go test ./...",
			},
		},
	}

	command := d.buildSessionLaunchCommand(
		protocol.DefaultProjectID,
		"axt-123",
		"axt-123", false,
		nil,
		`work on issue axt-123 (task): Verify startup behavior`,
	)
	if !strings.Contains(command, "zsh -i -c") {
		t.Fatalf("command = %q, want interactive shell launch", command)
	}
	if !strings.Contains(command, "direnv allow; go test ./...;") {
		t.Fatalf("command = %q, want init command sequence", command)
	}
	if !strings.Contains(command, `AZEDARACH_ISSUE_ID="axt-123" claude`) {
		t.Fatalf("command = %q, want AZEDARACH_ISSUE_ID env injection", command)
	}
	if !strings.Contains(command, `__AZEDARACH_INITIAL_PROMPT=$(printf`) ||
		!strings.Contains(command, `claude "$__AZEDARACH_INITIAL_PROMPT"`) {
		t.Fatalf("command = %q, want encoded initial prompt argument", command)
	}
}

func TestGlobalSessionLaunchUsesInheritedPathForAgentAndChildren(t *testing.T) {
	base := t.TempDir()
	generationDir := filepath.Join(base, "install", ".azedarach-generations", "generation.fresh")
	staleBin := filepath.Join(base, "repo", "bin")
	toolBin := filepath.Join(base, "tools")
	for _, dir := range []string{generationDir, staleBin, toolBin} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	tracePath := filepath.Join(base, "trace")
	writeExecutable := func(path, body string) {
		t.Helper()
		if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeExecutable(filepath.Join(generationDir, "az"), `printf 'installed-az|%s|%s\n' "$0" "$*" >> "$TRACE"
`)
	writeExecutable(filepath.Join(generationDir, "azd"), `printf 'installed-azd|%s|%s\n' "$0" "$*" >> "$TRACE"
`)
	for _, binary := range []string{"az", "azd"} {
		writeExecutable(filepath.Join(staleBin, binary), `printf 'STALE|%s|%s\n' "$0" "$*" >> "$TRACE"
`)
	}
	writeExecutable(filepath.Join(toolBin, "codex"), `az prime
az ticket get djm
azd version
(az version) &
wait
`)

	inheritedPath := strings.Join([]string{generationDir, staleBin, toolBin, "/usr/bin", "/bin"}, string(os.PathListSeparator))
	d := &Daemon{cfg: Config{
		CLITool:                 "codex",
		SessionShell:            "/bin/sh",
		SessionSyncInitCommands: []string{"az sync-init"},
		ManagedGenerationBinDir: generationDir,
	}}
	command := d.buildSessionLaunchCommandWithInitReadyPathAndEnv(
		protocol.DefaultProjectID,
		"djm",
		"az-djm",
		false,
		nil,
		"",
		"",
		d.sessionLaunchStartupExportCommands(daemonProjectRuntimeConfig{}, issueResourceLifecycleContext{}),
	)
	cmd := exec.Command("/bin/sh", "-c", command)
	cmd.Env = append(os.Environ(), "PATH="+inheritedPath, "TRACE="+tracePath)
	cmd.Stdin = strings.NewReader("az fallback-shell\nazd fallback-shell\nexit\n")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("managed session launch failed: %v\n%s\ncommand=%s", err, output, command)
	}
	traceBytes, err := os.ReadFile(tracePath)
	if err != nil {
		t.Fatal(err)
	}
	trace := string(traceBytes)
	if strings.Contains(trace, "STALE|") {
		t.Fatalf("trace used stale repository binary:\n%s", trace)
	}
	for _, want := range []string{
		"installed-az|" + filepath.Join(generationDir, "az") + "|prime",
		"installed-az|" + filepath.Join(generationDir, "az") + "|ticket get djm",
		"installed-az|" + filepath.Join(generationDir, "az") + "|sync-init",
		"installed-azd|" + filepath.Join(generationDir, "azd") + "|version",
		"installed-az|" + filepath.Join(generationDir, "az") + "|version",
		"installed-az|" + filepath.Join(generationDir, "az") + "|ai hook run --agent=codex session_end",
		"installed-az|" + filepath.Join(generationDir, "az") + "|fallback-shell",
		"installed-azd|" + filepath.Join(generationDir, "azd") + "|fallback-shell",
	} {
		if !strings.Contains(trace, want) {
			t.Fatalf("trace missing %q:\n%s", want, trace)
		}
	}
	if strings.Contains(command, "export PATH=") {
		t.Fatalf("launch command injects PATH policy: %s", command)
	}
}

func TestSessionContextPreservesInheritedPath(t *testing.T) {
	managedDir := filepath.Join(t.TempDir(), ".azedarach-generations", "generation.test")
	staleDir := filepath.Join(t.TempDir(), "repo", "bin")
	t.Setenv("PATH", staleDir+string(os.PathListSeparator)+"/usr/bin:/bin")

	for _, scoped := range []bool{false, true} {
		t.Run(fmt.Sprintf("scoped=%t", scoped), func(t *testing.T) {
			runner := newSessionStartTmuxRunner()
			d := &Daemon{
				cfg:  Config{ManagedGenerationBinDir: managedDir, ScopedRuntime: scoped},
				tmux: tmux.NewClient(runner, slog.Default()),
			}
			if err := d.setSessionContextEnv(context.Background(), "project", "djm", "az-djm"); err != nil {
				t.Fatal(err)
			}
			gotPath := runner.env["az-djm"]["PATH"]
			if gotPath != "" {
				t.Fatalf("session context injected PATH = %q", gotPath)
			}
		})
	}
}

func TestSessionContextClearsInheritedIssueForProjectOrchestrator(t *testing.T) {
	runner := newSessionStartTmuxRunner()
	runner.env["az-orchestrator-project"] = map[string]string{
		"AZEDARACH_ISSUE_ID": "stale-parent-issue",
	}
	d := &Daemon{tmux: tmux.NewClient(runner, slog.Default())}

	if err := d.setSessionContextEnv(context.Background(), "project-id", "", "az-orchestrator-project"); err != nil {
		t.Fatal(err)
	}

	got := runner.env["az-orchestrator-project"]
	if got["AZEDARACH_PROJECT_ID"] != "project-id" || got["AZEDARACH_ISSUE_ID"] != "" || got["AZEDARACH_SESSION_ID"] != "az-orchestrator-project" {
		t.Fatalf("session context = %#v, want exact issue-less orchestrator identity", got)
	}
}

func TestBuildSessionLaunchCommandDoesNotSerializeAsyncInitCommandsBeforeToolLaunch(t *testing.T) {
	d := &Daemon{
		cfg: Config{
			CLITool:      "codex",
			SessionShell: "zsh",
			SessionSyncInitCommands: []string{
				"direnv allow",
			},
			SessionAsyncInitCommands: []string{
				"pnpm type-check",
				"echo $AZEDARACH_ISSUE_ID",
			},
		},
	}

	command := d.buildSessionLaunchCommand(
		protocol.DefaultProjectID,
		"cnb",
		"cnb", false,
		nil,
		`work on issue cnb (feature): Add non-blocking session async init commands`,
	)
	if !strings.Contains(command, "direnv allow;") {
		t.Fatalf("command = %q, want blocking init command before launch", command)
	}
	if strings.Contains(command, "pnpm type-check") {
		// Async init commands must not be serialized into the AI pane launch command.
		t.Fatalf("command = %q, want async init command excluded from AI launch shell", command)
	}
	if !strings.Contains(command, `AZEDARACH_ISSUE_ID="cnb" codex`) {
		t.Fatalf("command = %q, want foreground AI tool launch", command)
	}
}

func TestBuildSessionLaunchCommandReportsAgentProcessExitBeforeFallbackShell(t *testing.T) {
	d := &Daemon{cfg: Config{CLITool: "codex", SessionShell: "zsh"}}

	command := d.buildSessionLaunchCommand(protocol.DefaultProjectID, "az-42", "codex-az-42", false, nil, "")

	wrapper := sessionAgentProcessExitCommand("codex")
	if !strings.Contains(command, "__azedarach_agent_exit_status=$?") ||
		!strings.Contains(command, "az ai hook run --agent=codex session_end") ||
		!strings.Contains(command, "|| true; exec '") {
		t.Fatalf("launch command = %q, want process-exit wrapper %q before fallback shell", command, wrapper)
	}
}

func TestSessionAgentProcessExitCommandConstrainsAgentArgument(t *testing.T) {
	command := sessionAgentProcessExitCommand(`codex; touch /tmp/unsafe`)
	if strings.Contains(command, "touch") || !strings.Contains(command, "--agent=claude") {
		t.Fatalf("sessionAgentProcessExitCommand() = %q, want safe fallback agent", command)
	}
}

func TestBuildSessionLaunchCommandWritesInitReadyMarkerAfterInitCommands(t *testing.T) {
	d := &Daemon{
		cfg: Config{
			CLITool:      "codex",
			SessionShell: "zsh",
			SessionSyncInitCommands: []string{
				"direnv allow",
				"go test ./...",
			},
		},
	}

	markerPath := sessionInitReadyMarkerPath("axt-123", "session-axt-123")
	command := d.buildSessionLaunchCommandWithInitReadyPath(
		protocol.DefaultProjectID,
		"axt-123",
		"session-axt-123",
		false,
		nil,
		`work on issue axt-123 (task): Verify startup behavior`,
		markerPath,
	)

	if !strings.Contains(command, "__azedarach_session_init_ready=0") ||
		!strings.Contains(command, "trap") ||
		!strings.Contains(command, filepath.ToSlash(markerPath)) {
		t.Fatalf("command = %q, want init-ready trap and marker path", command)
	}
	initIndex := strings.Index(command, "go test ./...")
	markerIndex := strings.LastIndex(command, "printf %s ready")
	toolIndex := strings.Index(command, `AZEDARACH_ISSUE_ID="axt-123" codex`)
	if initIndex < 0 || markerIndex < 0 || toolIndex < 0 {
		t.Fatalf("command = %q, missing init, marker, or tool command", command)
	}
	if !(initIndex < markerIndex && markerIndex < toolIndex) {
		t.Fatalf("command = %q, want init commands before marker before tool launch", command)
	}
}

func TestStartSessionAsyncInitCommandsUsesSeparateTmuxWindow(t *testing.T) {
	tmuxRunner := newSessionStartTmuxRunner()
	tmuxRunner.sessions["cnb"] = true
	tmuxRunner.windows["cnb"] = map[string]bool{"shell": true}
	d := &Daemon{
		cfg: Config{
			Logger: slog.Default(),
			SessionAsyncInitCommands: []string{
				"pnpm type-check",
				"echo $AZEDARACH_ISSUE_ID",
			},
		},
		tmux: tmux.NewClient(tmuxRunner, slog.Default()),
	}

	d.startSessionAsyncInitCommands(context.Background(), protocol.DefaultProjectID, "cnb", "cnb", "/tmp/worktree")

	if !tmuxRunner.windows["cnb"][sessionAsyncInitWindowName] {
		t.Fatalf("windows = %+v, want %q window", tmuxRunner.windows["cnb"], sessionAsyncInitWindowName)
	}
	if len(tmuxRunner.sendKeysTargets) != 1 || tmuxRunner.sendKeysTargets[0] != "cnb:"+sessionAsyncInitWindowName {
		t.Fatalf("sendKeysTargets = %+v, want cnb:%s", tmuxRunner.sendKeysTargets, sessionAsyncInitWindowName)
	}
	payload := tmuxRunner.sendKeysPayloads[0]
	if !strings.Contains(payload, "export AZEDARACH_PROJECT_ID=") ||
		!strings.Contains(payload, "AZEDARACH_ISSUE_ID=") ||
		!strings.Contains(payload, "AZEDARACH_SESSION_ID=") {
		t.Fatalf("payload = %q, want exported AZEDARACH context before async init", payload)
	}
	if !strings.Contains(payload, "session async-init[1] log: .azedarach/session-async-init/cnb/cnb/001.log") {
		t.Fatalf("payload = %q, want discoverable async init log path", payload)
	}
	if !strings.Contains(payload, "pnpm type-check") {
		t.Fatalf("payload = %q, want async init command", payload)
	}
	if !strings.Contains(payload, "tee -a '.azedarach/session-async-init/cnb/cnb/001.log'") {
		t.Fatalf("payload = %q, want log tee for async init output", payload)
	}
}

func TestBuildSessionLaunchCommandDoesNotInjectCodexHookOverrides(t *testing.T) {
	d := &Daemon{
		cfg: Config{
			CLITool:      "codex",
			SessionShell: "zsh",
		},
	}

	command := d.buildSessionLaunchCommand(
		protocol.DefaultProjectID,
		"axt-123",
		"codex-axt-123",
		false,
		[]string{"/tmp/a.png", "/tmp/with space/image.png", "   "},
		`work on issue axt-123 (task): Verify startup behavior`,
	)

	// Negative space: codex hooks live in <repo>/.codex/hooks.json (managed by
	// `az ai install --target=codex`). The daemon must NOT inject duplicate
	// `-c hooks.*=...` overrides at launch — that caused double-firing per
	// event.
	for _, mustNotContain := range []string{
		`hooks.SessionStart=`,
		`hooks.UserPromptSubmit=`,
		`hooks.PreToolUse=`,
		`hooks.PostToolUse=`,
		`hooks.PermissionRequest=`,
		`hooks.Stop=`,
		`az notify`,
	} {
		if strings.Contains(command, mustNotContain) {
			t.Fatalf("command = %q, must NOT contain %q (hook injection removed; rely on .codex/hooks.json)", command, mustNotContain)
		}
	}
	if strings.Count(command, "az ai hook run") != 1 || !strings.Contains(command, "session_end") {
		t.Fatalf("command = %q, want only the process-exit lifecycle wrapper", command)
	}

	// Surrounding launch behaviour stays intact: env prefix, image flags, and
	// the encoded Codex positional prompt.
	if !strings.Contains(command, `AZEDARACH_ISSUE_ID="axt-123"`) {
		t.Fatalf("command = %q, want AZEDARACH_ISSUE_ID env exported for the launched codex", command)
	}
	if !strings.Contains(command, `--image "/tmp/a.png"`) {
		t.Fatalf("command = %q, want codex image argument for /tmp/a.png", command)
	}
	if !strings.Contains(command, `--image "/tmp/with space/image.png"`) {
		t.Fatalf("command = %q, want codex image argument for spaced path", command)
	}
	if !strings.Contains(command, initialPromptShellVariable+`=$(printf`) {
		t.Fatalf("command = %q, want encoded initial prompt assignment", command)
	}
	if !strings.Contains(command, `codex `+codexFloopFailOpenConfigExpansion+` --image "/tmp/a.png" --image "/tmp/with space/image.png" -- "$`+initialPromptShellVariable+`"`) {
		t.Fatalf("command = %q, want codex positional prompt after image args", command)
	}
	if strings.Contains(command, "Verify startup behavior") {
		t.Fatalf("command = %q, want raw prompt text encoded out of launch command", command)
	}
}

func TestBuildSessionLaunchCommandOmitsMultilinePromptForCodex(t *testing.T) {
	d := &Daemon{
		cfg: Config{
			CLITool:      "codex",
			SessionShell: "zsh",
		},
	}

	command := d.buildSessionLaunchCommand(
		protocol.DefaultProjectID,
		"az-42",
		"codex-az-42",
		false,
		nil,
		buildStartWorkPrompt("az-42", string(domain.TypeEpic), "Replace apps/server", false, ""),
	)

	if strings.Contains(command, "`az orchestrate status --root <issue-id>`") {
		t.Fatalf("command = %q, want backticks encoded in prompt to avoid shell command substitution", command)
	}
	if strings.Contains(command, "\n") {
		t.Fatalf("command = %q, want tmux-submitted launch command without raw newlines", command)
	}
	if strings.Contains(command, "Start by running") {
		t.Fatalf("command = %q, want multiline prompt text encoded out of the shell command", command)
	}
	if !strings.Contains(command, initialPromptShellVariable+`=$(printf`) {
		t.Fatalf("command = %q, want encoded initial prompt assignment", command)
	}
	if !strings.Contains(command, `codex `+codexFloopFailOpenConfigExpansion+` -- "$`+initialPromptShellVariable+`"`) {
		t.Fatalf("command = %q, want codex positional prompt", command)
	}
	if !strings.Contains(command, `AZEDARACH_ISSUE_ID="az-42" codex`) {
		t.Fatalf("command = %q, want small codex launch command", command)
	}
}

func TestBuildSessionLaunchCommandKeepsLargeCodexPromptOutOfArgv(t *testing.T) {
	d := &Daemon{
		cfg: Config{
			CLITool:      "codex",
			SessionShell: "zsh",
		},
	}
	prompt := strings.Repeat("large prompt line\n", 32*1024)

	command := d.buildSessionLaunchCommand(protocol.DefaultProjectID, "az-42", "codex-az-42", false, nil, prompt)

	if strings.Contains(command, "large prompt line") || strings.Contains(command, initialPromptShellVariable) {
		t.Fatalf("command contains prompt payload or prompt variable")
	}
	if len(command) > 512 {
		t.Fatalf("command length = %d, want bounded small codex launch", len(command))
	}
	if !strings.Contains(command, `AZEDARACH_ISSUE_ID="az-42" codex`) {
		t.Fatalf("command = %q, want codex launch", command)
	}
}

func TestSessionLaunchScriptKeepsVeryLargePayloadOutOfChildArgv(t *testing.T) {
	tempDir := t.TempDir()
	argvPath := filepath.Join(tempDir, "argv")
	agentArgvPath := filepath.Join(tempDir, "agent-argv")
	shellPath := filepath.Join(tempDir, "recording-shell")
	agentPath := filepath.Join(tempDir, "recording-agent")
	azPath := filepath.Join(tempDir, "az")
	shell := "#!/bin/sh\n" +
		"if [ \"$#\" -eq 0 ]; then exit 0; fi\n" +
		"printf '%s\\n' \"$@\" > " + singleQuoteForShell(argvPath) + "\n" +
		"if [ \"$1\" != '-i' ] || [ \"$#\" -ne 2 ]; then exit 64; fi\n" +
		"exec /bin/sh \"$2\"\n"
	if err := os.WriteFile(shellPath, []byte(shell), 0o700); err != nil {
		t.Fatal(err)
	}
	agent := "#!/bin/sh\nprintf '%s\\n' \"$#\" \"$@\" > " + singleQuoteForShell(agentArgvPath) + "\n"
	if err := os.WriteFile(agentPath, []byte(agent), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(azPath, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", tempDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	d := &Daemon{cfg: Config{CLITool: agentPath, SessionShell: shellPath}}
	largeStartup := "__AZ_CONTEXT=1\n" + strings.Repeat("# context\n", 256*1024) + ":"
	launchShell, launchPayload := d.buildSessionLaunchScriptPayloadWithInitReadyPathAndEnv(protocol.DefaultProjectID, "az-42", "az-42", false, nil, "", []string{largeStartup}, sessionPromptHandoff{})
	scriptPath, tmuxCommand, err := prepareSessionLaunchScript(tempDir, launchShell, launchPayload)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(scriptPath) })
	if len(tmuxCommand) > 1024 || strings.Contains(tmuxCommand, "context") || strings.Contains(tmuxCommand, initialPromptShellVariable) {
		t.Fatalf("tmux command is not bounded: bytes=%d command=%q", len(tmuxCommand), tmuxCommand)
	}

	output, err := exec.Command("/bin/sh", "-c", tmuxCommand).CombinedOutput()
	if err != nil {
		t.Fatalf("execute very large launch payload: %v (%s)", err, output)
	}
	argv, err := os.ReadFile(argvPath)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(argv), "-i\n"+scriptPath+"\n"; got != want {
		t.Fatalf("child argv = %q, want bounded script argv %q", got, want)
	}
	agentArgv, err := os.ReadFile(agentArgvPath)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(agentArgv), "0\n"; got != want {
		t.Fatalf("agent argv = %q, want no prompt/startup payload %q", got, want)
	}
}

func TestSessionLaunchAtomicallyBootstrapsSlowAgentsAcrossToolsAndStartModes(t *testing.T) {
	for _, tool := range []string{"codex", "claude", "opencode", "codex-app-server"} {
		for _, mode := range []string{"direct", "orchestrated"} {
			t.Run(tool+"/"+mode, func(t *testing.T) {
				t.Parallel()
				tempDir := t.TempDir()
				readPath := filepath.Join(tempDir, "read-prompt")
				argvPath := filepath.Join(tempDir, "agent-argv")
				shellPath := filepath.Join(tempDir, "recording-shell")
				shell := "#!/bin/sh\n" +
					"if [ \"$#\" -eq 0 ]; then exit 0; fi\n" +
					"if [ \"$1\" != '-i' ] || [ \"$#\" -ne 2 ]; then exit 64; fi\n" +
					"exec /bin/sh \"$2\"\n"
				if err := os.WriteFile(shellPath, []byte(shell), 0o700); err != nil {
					t.Fatal(err)
				}
				agentName := strings.TrimSuffix(tool, "-app-server")
				agent := "#!/bin/sh\n" +
					"printf '%s\\n' \"$@\" >> \"$AGENT_ARGV_PATH\"\n" +
					"if [ \"${1:-} ${2:-} ${3:-}\" = 'app-server daemon start' ]; then sleep 0.05; exit 0; fi\n" +
					"sleep 0.05\n" +
					"last=; for arg do last=$arg; done\n" +
					"prompt_path=${last#* in }; prompt_path=${prompt_path%. Delete*}\n" +
					"cat \"$prompt_path\" > \"$READ_PATH\" || exit 66\n" +
					"rm -f -- \"$prompt_path\"\n" +
					"exit 0\n"
				if err := os.WriteFile(filepath.Join(tempDir, agentName), []byte(agent), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(tempDir, "az"), []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
					t.Fatal(err)
				}
				prompt := "direct request"
				if mode == "orchestrated" {
					prompt = buildStartWorkPrompt("az-42", string(domain.TypeTask), "Slow worker", true, "az-root")
				}
				handoff, err := prepareSessionPromptHandoff(tempDir, prompt)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(handoff.remove)
				d := &Daemon{cfg: Config{CLITool: agentName, CodexAppServer: tool == "codex-app-server", SessionShell: shellPath}}
				startupEnv := []string{
					"export PATH=" + singleQuoteForShell(tempDir+string(os.PathListSeparator)+os.Getenv("PATH")),
					"export READ_PATH=" + singleQuoteForShell(readPath),
					"export AGENT_ARGV_PATH=" + singleQuoteForShell(argvPath),
				}
				launchShell, launchPayload := d.buildSessionLaunchScriptPayloadWithInitReadyPathAndEnv(protocol.DefaultProjectID, "az-42", "az-42", false, nil, "", startupEnv, handoff)
				scriptPath, tmuxCommand, err := prepareSessionLaunchScript(tempDir, launchShell, launchPayload)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = os.Remove(scriptPath) })
				command := exec.Command("/bin/sh", "-c", tmuxCommand)
				command.Env = append(os.Environ(), "AZEDARACH_DAEMON_SCOPE="+d.daemonScopeTmuxEnvironment()["AZEDARACH_DAEMON_SCOPE"])
				output, err := command.CombinedOutput()
				if err != nil {
					t.Fatalf("execute slow %s %s launch: %v (%s)", tool, mode, err, output)
				}
				readPrompt, err := os.ReadFile(readPath)
				if err != nil {
					t.Fatal(err)
				}
				if string(readPrompt) != prompt {
					t.Fatalf("file-backed prompt mismatch: got %d bytes want %d", len(readPrompt), len(prompt))
				}
				argv, err := os.ReadFile(argvPath)
				if err != nil {
					t.Fatal(err)
				}
				if strings.Contains(string(argv), prompt) || strings.Contains(string(argv), "Slow worker") {
					t.Fatalf("agent argv contains prompt: %q", argv)
				}
				if !strings.Contains(string(argv), handoff.PromptPath) {
					t.Fatalf("agent argv does not contain bounded prompt-file bootstrap path: %q", argv)
				}
				if _, err := os.Stat(handoff.PromptPath); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("prompt artifact was not deleted after agent read: %v", err)
				}
			})
		}
	}
}

func TestSessionLaunchArtifactCleanerIsPrivateCancellableAndBatchBounded(t *testing.T) {
	tempDir := t.TempDir()
	now := time.Now()
	old := now.Add(-sessionLaunchArtifactMaxAge - time.Minute)
	stalePaths := make([]string, sessionLaunchArtifactCleanupLimit*20)
	for i := range stalePaths {
		path := filepath.Join(tempDir, fmt.Sprintf("%s%05d.sh", sessionLaunchArtifactPrefix, i))
		if err := os.WriteFile(path, []byte("secret"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, old, old); err != nil {
			t.Fatal(err)
		}
		stalePaths[i] = path
	}
	recentPath := filepath.Join(tempDir, sessionLaunchArtifactPrefix+"recent.sh")
	if err := os.WriteFile(recentPath, []byte("active"), 0o600); err != nil {
		t.Fatal(err)
	}

	cleaner := &sessionLaunchArtifactCleaner{dir: tempDir}
	t.Cleanup(cleaner.close)
	inspected, removed, err := cleaner.cleanupBatch(context.Background(), now)
	if err != nil {
		t.Fatal(err)
	}
	if inspected != sessionLaunchArtifactCleanupLimit || removed != sessionLaunchArtifactCleanupLimit {
		t.Fatalf("cleanup batch inspected/removed = %d/%d, want %d/%d", inspected, removed, sessionLaunchArtifactCleanupLimit, sessionLaunchArtifactCleanupLimit)
	}
	totalInspected, totalRemoved := inspected, removed
	for cleaner.cursor != nil {
		batchInspected, batchRemoved, batchErr := cleaner.cleanupBatch(context.Background(), now)
		if batchErr != nil {
			t.Fatal(batchErr)
		}
		if batchInspected > sessionLaunchArtifactCleanupLimit {
			t.Fatalf("cleanup inspected %d entries in one batch, limit %d", batchInspected, sessionLaunchArtifactCleanupLimit)
		}
		totalInspected += batchInspected
		totalRemoved += batchRemoved
	}
	if totalInspected != len(stalePaths)+1 || totalRemoved != len(stalePaths) {
		t.Fatalf("cursor cleanup totals inspected/removed = %d/%d, want %d/%d", totalInspected, totalRemoved, len(stalePaths)+1, len(stalePaths))
	}
	if mode, err := os.Stat(tempDir); err != nil || mode.Mode().Perm() != 0o700 {
		t.Fatalf("artifact dir mode = %v err=%v, want 0700", mode, err)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if inspected, removed, err := cleaner.cleanupBatch(cancelled, now); !errors.Is(err, context.Canceled) || inspected != 0 || removed != 0 {
		t.Fatalf("cancelled cleanup = inspected %d removed %d err %v", inspected, removed, err)
	}
	if _, err := os.Stat(recentPath); err != nil {
		t.Fatalf("recent launch artifact was removed: %v", err)
	}
}

func TestSessionLaunchArtifactCleanerStopsPromptlyOnShutdown(t *testing.T) {
	dir := t.TempDir()
	cleaner := &sessionLaunchArtifactCleaner{dir: dir}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		cleaner.run(ctx, time.Hour, nil)
		close(done)
	}()
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("artifact cleaner did not stop after cancellation")
	}
}

func TestInitialPromptShellAssignmentPreservesSpecialPromptBytes(t *testing.T) {
	prompt := "work $ `\n' \" \\ ! \t"
	command := initialPromptShellAssignment(prompt) + `; printf '<%s>' "$__AZEDARACH_INITIAL_PROMPT"`

	out, err := exec.Command("sh", "-c", command).Output()
	if err != nil {
		t.Fatalf("run assignment command: %v", err)
	}
	if got, want := string(out), "<"+prompt+">"; got != want {
		t.Fatalf("decoded prompt = %q, want %q", got, want)
	}
	if strings.Contains(command, "\n") || strings.Contains(command, prompt) {
		t.Fatalf("command = %q, want encoded prompt without raw multiline text", command)
	}
}

func TestBuildStartWorkPromptMatchesPrimeBootFormatForOrchestratedWorker(t *testing.T) {
	prompt := buildStartWorkPrompt("az-42", "task", "Fix startup shell", true, "az-1")
	if !strings.Contains(prompt, "work on issue az-42 (task): Fix startup shell") {
		t.Fatalf("prompt = %q, want issue summary header", prompt)
	}
	if !strings.Contains(prompt, "Start by running `az prime`. Then continue the task using the context it prints without waiting for further instruction.") {
		t.Fatalf("prompt = %q, want az prime boot instructions", prompt)
	}
	if !strings.Contains(prompt, "Role: worker") {
		t.Fatalf("prompt = %q, want worker role primer", prompt)
	}
	if !strings.Contains(prompt, "Coordination mailbox parent: `az-1`") {
		t.Fatalf("prompt = %q, want concrete parent mailbox", prompt)
	}
	if !strings.Contains(prompt, "az mail list --parent az-1 --since 0 --json") {
		t.Fatalf("prompt = %q, want inbound mailbox read command", prompt)
	}
	if !strings.Contains(prompt, "before declaring yourself blocked or idle") {
		t.Fatalf("prompt = %q, want receive-before-idle guidance", prompt)
	}
	if !strings.Contains(prompt, "Report coordination state to an active parent orchestrator/watch with `az mail send --parent az-1 --issue az-42 --type worker-progress|worker-blocked|worker-integration-ready --body \"...\"`; do not use `az orchestrate message` for your own status") {
		t.Fatalf("prompt = %q, want safe worker reporting guidance", prompt)
	}
	for _, eventType := range []string{"worker-progress", "worker-blocked", "worker-integration-ready"} {
		if !strings.Contains(prompt, eventType) {
			t.Fatalf("prompt = %q, want mailbox event type %s", prompt, eventType)
		}
	}
	if !strings.Contains(prompt, "Evidence bodies should be JSON `worker_evidence.v1` packets with `summary`, `commands_run`, `key_assertions`, `files_changed`, `review.status`, `review.findings`, and `risks`") {
		t.Fatalf("prompt = %q, want structured worker evidence guidance", prompt)
	}
	if !strings.Contains(prompt, "use `az issue record az-42 --type evidence.submitted --data '<json>'` when mailbox delivery is irrelevant") {
		t.Fatalf("prompt = %q, want concrete issue evidence command", prompt)
	}
	if !strings.Contains(prompt, "Before handing off, run the relevant validation/review checks, build the final `worker_evidence.v1` packet from actual results, run `az evidence validate --body '<json>'`, record or send that exact JSON packet, then set/leave the issue `in_review`. Review handoff is non-terminal: preserve this tmux session and worktree for orchestrator feedback; do not stop or close them yourself.") {
		t.Fatalf("prompt = %q, want validated handoff sequence", prompt)
	}
	if !strings.Contains(prompt, "Omit `artifact_links` unless links are needed; when present, encode it as objects like `[{\"label\":\"CI\",\"url\":\"https://example.test/run\"}]`, not a string array") {
		t.Fatalf("prompt = %q, want artifact_links object-shape guidance", prompt)
	}
	if !strings.Contains(prompt, "worker-ready and worker-complete are accepted only as legacy aliases for worker-integration-ready") {
		t.Fatalf("prompt = %q, want legacy worker-ready/worker-complete alias guidance", prompt)
	}
	if !strings.Contains(prompt, "For non-orchestrated progress, follow-ups, validation, risks, blockers, or closeout evidence, use `az issue record` instead.") {
		t.Fatalf("prompt = %q, want issue record split guidance", prompt)
	}
	if !strings.Contains(prompt, "Keep issue status current; report progress, blockers, risks, and readiness through issue records or active-coordination mailbox evidence, with notes as terse human audit scratchpad only.") {
		t.Fatalf("prompt = %q, want worker evidence-first status guidance", prompt)
	}
	if !strings.Contains(prompt, "Use `in_progress` while actively working and `in_review` when complete and ready for orchestrator integration") {
		t.Fatalf("prompt = %q, want worker status semantics", prompt)
	}
	if !strings.Contains(prompt, "Report blockers via dependency edges, issue record evidence, or active worker-blocked mailbox events, not by setting `in_review`") {
		t.Fatalf("prompt = %q, want blocked-as-graph guidance", prompt)
	}
	if strings.Contains(prompt, "events: , , and .") {
		t.Fatalf("prompt = %q, contains blank mailbox event interpolation", prompt)
	}
}

func TestBuildStartWorkPromptOmitsMailboxGuidanceForStandaloneTask(t *testing.T) {
	prompt := buildStartWorkPrompt("az-42", "task", "Fix startup shell", false, "")
	if !strings.Contains(prompt, "Role: contributor") {
		t.Fatalf("prompt = %q, want contributor role primer", prompt)
	}
	if strings.Contains(prompt, "worker-progress") || strings.Contains(prompt, "worker-blocked") || strings.Contains(prompt, "worker-integration-ready") || strings.Contains(prompt, "worker-ready") || strings.Contains(prompt, "worker-complete") {
		t.Fatalf("prompt = %q, want mailbox worker event types omitted", prompt)
	}
	if !strings.Contains(prompt, "Keep issue status current; record progress, follow-ups, validation, blockers, review facts, risks, and closeout evidence with `az issue record`; keep notes as terse human audit scratchpad only.") {
		t.Fatalf("prompt = %q, want contributor evidence-first status guidance", prompt)
	}
	if !strings.Contains(prompt, "Use `in_progress` while actively working and `in_review` when complete and awaiting review/integration") {
		t.Fatalf("prompt = %q, want contributor status semantics", prompt)
	}
	if !strings.Contains(prompt, "Review handoff is non-terminal: preserve the tmux session and worktree; do not stop or close them while waiting for human feedback") {
		t.Fatalf("prompt = %q, want contributor review handoff resource preservation", prompt)
	}
	if !strings.Contains(prompt, "Use `closed` only after explicit acceptance/integration and `cancelled` only for a terminal non-integrated outcome") {
		t.Fatalf("prompt = %q, want contributor terminal lifecycle semantics", prompt)
	}
	if !strings.Contains(prompt, "Represent blocked work with dependency edges and issue record evidence, not by using `in_review`") {
		t.Fatalf("prompt = %q, want contributor blocked-as-graph guidance", prompt)
	}
}

func TestBuildStartWorkPromptSanitizesControlCharsAndAngleBrackets(t *testing.T) {
	prompt := buildStartWorkPrompt("az-42", "task\n", "Fix <shell>\tselection", false, "")
	if strings.Contains(prompt, "<shell>") {
		t.Fatalf("prompt = %q, want angle brackets sanitized", prompt)
	}
	if !strings.Contains(prompt, "Fix [shell] selection") {
		t.Fatalf("prompt = %q, want sanitized title", prompt)
	}
	if strings.Contains(prompt, "\n\n\n") {
		t.Fatalf("prompt = %q, want compact whitespace", prompt)
	}
}

func TestBuildStartWorkPromptIncludesOrchestratorPrimerForEpic(t *testing.T) {
	prompt := buildStartWorkPrompt("az-99", "epic", "Coordinate big tree", false, "")
	if !strings.Contains(prompt, "Role: orchestrator") {
		t.Fatalf("prompt = %q, want orchestrator role primer", prompt)
	}
	if !strings.Contains(prompt, "az orchestrate status --root <issue-id>") {
		t.Fatalf("prompt = %q, want orchestrate status instruction", prompt)
	}
	if !strings.Contains(prompt, "leave it running while workers are active") {
		t.Fatalf("prompt = %q, want continuous watch instruction", prompt)
	}
	if !strings.Contains(prompt, "Remain in this active turn/loop and continuously consume its events; starting sessions and a background watch is not a completed handoff to the human") {
		t.Fatalf("prompt = %q, want persistent watch-consumption duty", prompt)
	}
	if !strings.Contains(prompt, "Do not use `--once` for orchestration monitoring") {
		t.Fatalf("prompt = %q, want --once diagnostic warning", prompt)
	}
	if !strings.Contains(prompt, "az orchestrate complete-check --root <issue-id>") {
		t.Fatalf("prompt = %q, want complete-check instruction", prompt)
	}
	if !strings.Contains(prompt, "only then set the root `in_review` and hand it to the human while keeping its tmux session/worktree alive") {
		t.Fatalf("prompt = %q, want non-terminal root review handoff", prompt)
	}
	if !strings.Contains(prompt, "Close/integrate the root only after explicit human acceptance") {
		t.Fatalf("prompt = %q, want explicit root acceptance gate", prompt)
	}
	if strings.Contains(prompt, "Close only when `az orchestrate complete-check") {
		t.Fatalf("prompt = %q, retained stale complete-check-implies-close guidance", prompt)
	}
	if !strings.Contains(prompt, "Start this root's direct runnable leaf workers manually with `az orchestrate start --root <issue-id> --limit 4`") {
		t.Fatalf("prompt = %q, want direct leaf start guidance", prompt)
	}
	if !strings.Contains(prompt, "Queued reviews do not block unrelated starts when managed agent capacity remains") {
		t.Fatalf("prompt = %q, want non-blocking review scheduling guidance", prompt)
	}
	if !strings.Contains(prompt, "Chain of command is strict: orchestrate only this root's direct children") ||
		!strings.Contains(prompt, "Never launch, message, inspect for intervention, review, integrate, stop, or take over grandchildren or deeper descendants") {
		t.Fatalf("prompt = %q, want strict direct-child boundary", prompt)
	}
	if !strings.Contains(prompt, "Nested epic/root rule: start a direct child root's own orchestrator session with `az orchestrator-session start --root <child-root>`") {
		t.Fatalf("prompt = %q, want nested root session guidance", prompt)
	}
	if !strings.Contains(prompt, "supervise that orchestrator as a direct child while it exclusively owns its descendants") {
		t.Fatalf("prompt = %q, want no-flattening guidance", prompt)
	}
	if !strings.Contains(prompt, "React to progress, blocked, and integration-ready evidence; review and integrate accepted children/epics, advance newly unblocked work, and repeat status/start/watch/review while graph work remains") {
		t.Fatalf("prompt = %q, want active parent coordination loop", prompt)
	}
	if !strings.Contains(prompt, "az orchestrate message --root <issue-id> --issue <worker-issue> --body \"...\"") {
		t.Fatalf("prompt = %q, want active worker message instruction", prompt)
	}
	if !strings.Contains(prompt, "Bare `az mail send` is durable mailbox-only") {
		t.Fatalf("prompt = %q, want passive mailbox warning", prompt)
	}
	if !strings.Contains(prompt, "workers reporting to this active parent orchestrator/watch should use `az mail send --parent <issue-id> --issue <worker-issue> --type worker-progress|worker-blocked|worker-integration-ready --body \"...\"`") {
		t.Fatalf("prompt = %q, want safe worker reporting guidance", prompt)
	}
	if !strings.Contains(prompt, "Worker integration evidence should be a structured JSON `worker_evidence.v1` packet") {
		t.Fatalf("prompt = %q, want structured evidence guidance", prompt)
	}
	for _, want := range []string{
		"Review revision contract",
		"exact `diff_base_revision`, `head_revision`, stable `diff_scope`, and executable `diff_range`",
		"independent review inspects the complete assigned revision and returns one consolidated actionable finding batch before yielding",
		"finding the first defect is not a stop condition",
		"falls back to the complete affected invariant when the local delta cannot establish completeness",
		"covered and deliberately skipped typed risk-matrix cells",
		"stateful/concurrent work covers state, attempt/completion ordering",
		"subprocess work covers every lifecycle ending and portability",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt = %q, want revision-incremental review guidance %q", prompt, want)
		}
	}
	if !strings.Contains(prompt, "Trust hook-backed `activity=busy|idle|waiting` for worker idleness checks") {
		t.Fatalf("prompt = %q, want bounded tmux observation guidance", prompt)
	}
	if !strings.Contains(prompt, "treat `activity=no-agent` as an intentional session-only shell") {
		t.Fatalf("prompt = %q, want no-agent session-only guidance", prompt)
	}
	if !strings.Contains(prompt, "If activity is `unknown`, inspect hooks with `az ai status --target=auto`; run `az ai install --target=auto` only when hooks are missing, outdated, or not installed") {
		t.Fatalf("prompt = %q, want hook status/install fallback guidance", prompt)
	}
	if !strings.Contains(prompt, "az orchestrate capture --issue <worker-issue>") ||
		!strings.Contains(prompt, "Do not poll panes on a fixed interval") {
		t.Fatalf("prompt = %q, want daemon-backed capture guardrail", prompt)
	}
	if !strings.Contains(prompt, "az orchestrate integrate --issue <issue-id>") {
		t.Fatalf("prompt = %q, want integrate instruction", prompt)
	}
	if !strings.Contains(prompt, "az orchestrate close-session --issue <issue-id>") {
		t.Fatalf("prompt = %q, want close-session instruction", prompt)
	}
	if !strings.Contains(prompt, "Treat blocked work as graph state from unresolved `blocks` dependencies") {
		t.Fatalf("prompt = %q, want graph-derived blocked guidance", prompt)
	}
	if !strings.Contains(prompt, "Delegate every non-trivial diff inspection to a fresh ephemeral review subagent") ||
		!strings.Contains(prompt, "Delegates are read-only") ||
		!strings.Contains(prompt, "keep durable review-return, review-accept, integration, and close authority in this orchestrator") {
		t.Fatalf("prompt = %q, want ephemeral read-only review delegation guidance", prompt)
	}
	if !strings.Contains(prompt, "Treat `in_review` workers as ready for orchestrator validation") {
		t.Fatalf("prompt = %q, want in-review integration guidance", prompt)
	}
	if !strings.Contains(prompt, "close accepted worker issues with `az issue close --id <issue-id>`") {
		t.Fatalf("prompt = %q, want issue close completion guidance", prompt)
	}
	if !strings.Contains(prompt, "Keep orchestration authority centralized inside each root session while delegating read-only review inspection; delegate explicit nested epic/root issues to their own orchestrator sessions rather than flattening their children into this session.") {
		t.Fatalf("prompt = %q, want per-root centralization guidance", prompt)
	}
	if !strings.Contains(prompt, "Parent/tracker completion includes child lifecycle cleanup: close accepted completed children with `az issue close --id <child-issue>`") {
		t.Fatalf("prompt = %q, want child lifecycle cleanup guidance", prompt)
	}
	if !strings.Contains(prompt, "leave any child `open` or `in_progress` only with an explicit blocker, dependency, or remaining-scope rationale") {
		t.Fatalf("prompt = %q, want unresolved child rationale guidance", prompt)
	}
	if !strings.Contains(prompt, "Continue the parent loop until `az orchestrate complete-check --root <issue-id>` and final validation pass; only then set the root `in_review` and hand it to the human") {
		t.Fatalf("prompt = %q, want complete-check-gated human handoff", prompt)
	}
}

func TestBuildStartWorkPromptUsesRiskTieredWorkerReview(t *testing.T) {
	for _, prompt := range []string{
		buildStartWorkPrompt("az-standalone", "task", "Standalone", false, ""),
		buildStartWorkPrompt("az-worker", "task", "Worker", true, "az-root"),
	} {
		for _, want := range []string{
			"for a default non-migration change, run one complete revision-bound worker review pass",
			"Build/test/boundary gates do not count as review passes",
			"Extra worker passes require an explicitly named high-risk class and reason",
			"database migrations retain three clean post-final-edit passes",
			"`review.revision`",
			"`review.reused_layers`",
			"deliberately skipped cells with reasons",
			"authorization/bypass paths",
			"fall back to the complete affected invariant when a local delta cannot establish completeness",
		} {
			if !strings.Contains(prompt, want) {
				t.Fatalf("prompt missing risk-tiered review guidance %q:\n%s", want, prompt)
			}
		}
	}
}

func TestSessionMessageUsesAuthoritativeReceiverWithoutTmuxTextInput(t *testing.T) {
	projectID := protocol.DefaultProjectID
	issueID := naming.IssueID("az-42")
	repoDir := t.TempDir()
	sessionID := naming.CanonicalSessionIDForIssue(repoDir, issueID).String()
	tmuxRunner := newSessionStartTmuxRunner()
	tmuxRunner.sessions[sessionID] = true
	daemon := &Daemon{
		cfg:  Config{RepoDir: repoDir, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))},
		tmux: tmux.NewClient(tmuxRunner, slog.New(slog.NewTextHandler(io.Discard, nil))),
	}
	seedReadyAgentInput(t, daemon, tmuxRunner, projectID, sessionID)
	body, err := json.Marshal(sessionCommandBody{
		ProjectID: projectID,
		SessionID: issueID.String(),
		Message:   "Orchestrator says proceed now.\n\nKeep notes current.",
	})
	if err != nil {
		t.Fatalf("marshal body: %v", err)
	}

	resp, err := daemon.handleSessionMessage(context.Background(), protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-session-message",
		Kind:            protocol.EnvelopeKindCommand,
		Command:         daemonhandlers.CommandSessionMessage,
		Meta:            protocol.Metadata{ProjectID: naming.ProjectID(projectID)},
		Body:            body,
	})
	if err != nil {
		t.Fatalf("handleSessionMessage error = %v", err)
	}
	if !resp.OK {
		if resp.Error != nil {
			t.Fatalf("response not OK: %+v", *resp.Error)
		}
		t.Fatalf("response not OK: %+v", resp)
	}
	wantCommands := [][]string{{"has-session", "-t", sessionID}}
	if !reflect.DeepEqual(tmuxRunner.commands, wantCommands) {
		t.Fatalf("tmux commands = %#v, want %#v", tmuxRunner.commands, wantCommands)
	}
	if !reflect.DeepEqual(tmuxRunner.inputPayloads, []string{"Orchestrator says proceed now.\n\nKeep notes current."}) {
		t.Fatalf("authoritative receiver payloads = %#v", tmuxRunner.inputPayloads)
	}
}

func TestSessionCaptureCapturesPaneThroughDaemonTmuxClient(t *testing.T) {
	projectID := protocol.DefaultProjectID
	issueID := naming.IssueID("az-42")
	repoDir := "/repo"
	sessionID := naming.CanonicalSessionIDForIssue(repoDir, issueID).String()
	tmuxRunner := newSessionStartTmuxRunner()
	tmuxRunner.sessions[sessionID] = true
	tmuxRunner.captureOutput = "line one\nline two\nline three\nline four\n"
	daemon := &Daemon{
		cfg:  Config{RepoDir: repoDir, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))},
		tmux: tmux.NewClient(tmuxRunner, slog.New(slog.NewTextHandler(io.Discard, nil))),
	}
	body, err := json.Marshal(sessionCommandBody{
		ProjectID: projectID,
		SessionID: issueID.String(),
		Lines:     2,
	})
	if err != nil {
		t.Fatalf("marshal body: %v", err)
	}

	resp, err := daemon.handleSessionCapture(context.Background(), protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-session-capture",
		Kind:            protocol.EnvelopeKindCommand,
		Command:         protocol.CommandSessionCapture,
		Meta:            protocol.Metadata{ProjectID: naming.ProjectID(projectID)},
		Body:            body,
	})
	if err != nil {
		t.Fatalf("handleSessionCapture error = %v", err)
	}
	if !resp.OK {
		t.Fatalf("response not OK: %+v", resp)
	}
	wantCommands := [][]string{
		{"has-session", "-t", sessionID},
		{"capture-pane", "-t", sessionID, "-p", "-S", "-2"},
	}
	if !reflect.DeepEqual(tmuxRunner.commands, wantCommands) {
		t.Fatalf("tmux commands = %#v, want %#v", tmuxRunner.commands, wantCommands)
	}
	var got protocol.SessionCaptureResponseBody
	if err := json.Unmarshal(resp.Body, &got); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if got.ProjectID != naming.ProjectID(daemon.canonicalProjectID(projectID)) || got.IssueID != issueID || got.SessionID != naming.SessionID(sessionID) || got.Lines != 2 || got.Output != "line three\nline four\n" {
		t.Fatalf("response = %+v, want project issue session lines output", got)
	}
}

func TestResolveSessionIssuePrefersExactID(t *testing.T) {
	tasks := []domain.Task{
		{ID: "bgr", Title: "cant seem to start tmux session for issue bfs"},
		{ID: "bfs", Title: "actual target issue"},
	}

	got, ok := resolveSessionIssue(tasks, "bfs")
	if !ok {
		t.Fatal("resolveSessionIssue returned not found, want exact match")
	}
	if got.ID != "bfs" {
		t.Fatalf("resolved issue = %s, want bfs", got.ID)
	}
}

func TestResolveSessionIssueRejectsFuzzyOnlyResults(t *testing.T) {
	tasks := []domain.Task{
		{ID: "bgr", Title: "first result"},
		{ID: "bfs", Title: "second result"},
	}

	if got, ok := resolveSessionIssue(tasks, "missing"); ok {
		t.Fatalf("resolveSessionIssue = %+v, want not found for fuzzy-only result", got)
	}
}

func TestBuildSessionLaunchCommandAddsCodexDangerousBypassFlagInYoloMode(t *testing.T) {
	d := &Daemon{
		cfg: Config{
			CLITool:      "codex",
			SessionShell: "zsh",
		},
	}

	command := d.buildSessionLaunchCommand(protocol.DefaultProjectID, "axt-123", "codex-axt-123", true, nil, "")
	if !strings.Contains(command, "--dangerously-bypass-approvals-and-sandbox") {
		t.Fatalf("command = %q, want yolo codex bypass flag", command)
	}
}

func TestBuildSessionResumeCommandUsesCodexResumeLastWithOptionsBeforeSelector(t *testing.T) {
	d := &Daemon{
		cfg: Config{
			CLITool:                    "codex",
			DangerouslySkipPermissions: true,
			SessionShell:               "zsh",
		},
	}

	command := d.buildSessionResumeCommand(protocol.DefaultProjectID, "axt-123", "codex-axt-123", false, []string{"/tmp/screen.png"})
	wantOrder := []string{"codex", "resume", `--image "/tmp/screen.png"`, "--dangerously-bypass-approvals-and-sandbox", "--last"}
	last := -1
	for _, part := range wantOrder {
		idx := strings.Index(command, part)
		if idx <= last {
			t.Fatalf("command = %q, want %q after index %d", command, part, last)
		}
		last = idx
	}
	if strings.Contains(command, "Continue your prior task") {
		t.Fatalf("command = %q, want continuation prompt delivered after launch for codex", command)
	}
}

func TestBuildSessionResumeCommandUsesClaudeContinueWithPrompt(t *testing.T) {
	d := &Daemon{
		cfg: Config{
			CLITool:                    "claude",
			DangerouslySkipPermissions: true,
			SessionShell:               "zsh",
		},
	}

	command := d.buildSessionResumeCommand(protocol.DefaultProjectID, "axt-123", "claude-axt-123", false, nil)
	wantParts := []string{`AZEDARACH_ISSUE_ID="axt-123"`, "claude", "--continue", "--dangerously-skip-permissions", "Continue your prior task"}
	for _, part := range wantParts {
		if !strings.Contains(command, part) {
			t.Fatalf("command = %q, want part %q", command, part)
		}
	}
}

func TestBuildSessionLaunchCommandAddsDangerousSkipPermissionsFromConfigAcrossTools(t *testing.T) {
	tests := []struct {
		name string
		tool string
		want string
	}{
		{name: "claude", tool: "claude", want: "--dangerously-skip-permissions"},
		{name: "codex", tool: "codex", want: "--dangerously-bypass-approvals-and-sandbox"},
		{name: "opencode", tool: "opencode", want: "opencode"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := &Daemon{
				cfg: Config{
					CLITool:                    tt.tool,
					SessionShell:               "zsh",
					DangerouslySkipPermissions: true,
				},
			}

			command := d.buildSessionLaunchCommand(protocol.DefaultProjectID, "axt-123", "session-axt-123", false, nil, "")
			if !strings.Contains(command, tt.want) {
				t.Fatalf("tool %s command = %q, want config-driven permissions flag %q", tt.tool, command, tt.want)
			}
		})
	}
}

func TestSessionLaunchArtifactAdaptersCoverConfiguredTools(t *testing.T) {
	tests := []struct {
		tool string
		mode sessionLaunchMode
		want []string
	}{
		{tool: "claude", mode: sessionLaunchInitial, want: []string{"claude", "--dangerously-skip-permissions"}},
		{tool: "claude", mode: sessionLaunchResume, want: []string{"claude", "--continue"}},
		{tool: "codex", mode: sessionLaunchInitial, want: []string{"codex", "--dangerously-bypass-approvals-and-sandbox"}},
		{tool: "codex", mode: sessionLaunchResume, want: []string{"codex", "resume", "--last"}},
		{tool: "opencode", mode: sessionLaunchInitial, want: []string{"opencode", "--prompt"}},
		{tool: "opencode", mode: sessionLaunchResume, want: []string{"opencode", "--continue", "--prompt"}},
		{tool: "my-agent", mode: sessionLaunchInitial, want: []string{"my-agent"}},
		{tool: "my-agent", mode: sessionLaunchResume, want: []string{"my-agent", "--continue"}},
	}
	for _, tt := range tests {
		t.Run(tt.tool+"/"+string(tt.mode), func(t *testing.T) {
			d := &Daemon{cfg: Config{RepoDir: t.TempDir(), CLITool: tt.tool, SessionShell: "/bin/sh", DangerouslySkipPermissions: true}}
			artifact, err := d.prepareSessionLaunchArtifact(sessionLaunchSpec{Mode: tt.mode, ProjectID: protocol.DefaultProjectID, IssueID: "dky", SessionID: "az-dky", Prompt: "continue", Yolo: true})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(artifact.remove)
			body, err := os.ReadFile(artifact.ScriptPath)
			if err != nil {
				t.Fatal(err)
			}
			for _, want := range tt.want {
				if !strings.Contains(string(body), want) {
					t.Fatalf("%s %s artifact missing %q: %s", tt.tool, tt.mode, want, body)
				}
			}
			if strings.Contains(string(body), ".azedarach-generations") || strings.Contains(string(body), "export PATH=") {
				t.Fatalf("artifact injects PATH policy: %s", body)
			}
			if tt.tool == "my-agent" && strings.Contains(string(body), "dangerously-skip-permissions") {
				t.Fatalf("configured tool inherited Claude permission flag: %s", body)
			}
		})
	}
}

func TestSessionLaunchArtifactExportsManagedContextBeforeProjectOrchestratorAgentStarts(t *testing.T) {
	binDir := t.TempDir()
	trace := filepath.Join(t.TempDir(), "trace")
	tool := filepath.Join(binDir, "codex")
	toolScript := `#!/bin/sh
if [ "${1:-}" != mcp ]; then
  printf '%s|%s|%s|%s|%s|%s' "$AZEDARACH_PROJECT_ID" "$AZEDARACH_ISSUE_ID" "$AZEDARACH_SESSION_ID" "$AZEDARACH_LOGICAL_PANE_ID" "$AZEDARACH_AGENT_INCARNATION" "$AZEDARACH_PANE_PID" > "$TRACE"
fi
`
	if err := os.WriteFile(tool, []byte(toolScript), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(binDir, "az"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	d := &Daemon{cfg: Config{RepoDir: t.TempDir(), CLITool: "codex", SessionShell: "/bin/sh"}}
	artifact, err := d.prepareSessionLaunchArtifact(sessionLaunchSpec{
		Mode:             sessionLaunchInitial,
		ProjectID:        "project-id",
		SessionID:        "az-orchestrator-project",
		LogicalPaneID:    "agent",
		AgentIncarnation: "restart-exact",
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(artifact.remove)

	cmd := exec.Command("/bin/sh", "-c", artifact.Command)
	cmd.Env = append(os.Environ(),
		"PATH="+binDir+string(os.PathListSeparator)+"/usr/bin:/bin",
		"TRACE="+trace,
		"AZEDARACH_ISSUE_ID=stale-parent-issue",
	)
	cmd.Stdin = strings.NewReader("exit\n")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("execute orchestrator launch artifact: %v\n%s", err, out)
	}
	got, err := os.ReadFile(trace)
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(string(got), "|")
	if len(parts) != 6 {
		t.Fatalf("managed context = %q, want six fields", got)
	}
	if parts[0] != "project-id" || parts[1] != "" || parts[2] != "az-orchestrator-project" || parts[3] != "agent" || parts[4] != "restart-exact" {
		t.Fatalf("managed context = %q, want exact project orchestrator identity", got)
	}
	if panePID, err := strconv.Atoi(parts[5]); err != nil || panePID <= 0 {
		t.Fatalf("managed pane pid = %q, want positive launch-shell pid", parts[5])
	}
}

func TestSessionLaunchArtifactPicksUpStableLinkSwitchWithoutEnvironmentReload(t *testing.T) {
	binDir := t.TempDir()
	trace := filepath.Join(t.TempDir(), "trace")
	writeTool := func(name, value string) string {
		path := filepath.Join(t.TempDir(), name)
		if err := os.WriteFile(path, []byte("#!/bin/sh\nprintf '"+value+"\\n' >> \"$TRACE\"\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		return path
	}
	stable := filepath.Join(binDir, "codex")
	first := writeTool("codex-v1", "v1")
	second := writeTool("codex-v2", "v2")
	if err := os.Symlink(first, stable); err != nil {
		t.Fatal(err)
	}
	d := &Daemon{cfg: Config{RepoDir: t.TempDir(), CLITool: "codex", SessionShell: "/bin/sh"}}
	run := func() {
		artifact, err := d.prepareSessionLaunchArtifact(sessionLaunchSpec{Mode: sessionLaunchInitial, ProjectID: protocol.DefaultProjectID, IssueID: "dky", SessionID: "az-dky"})
		if err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command("/bin/sh", "-c", artifact.Command)
		cmd.Env = append(os.Environ(), "PATH="+binDir+string(os.PathListSeparator)+"/usr/bin:/bin", "TRACE="+trace)
		cmd.Stdin = strings.NewReader("exit\n")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("run artifact: %v\n%s", err, out)
		}
	}
	run()
	if err := os.Remove(stable); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(second, stable); err != nil {
		t.Fatal(err)
	}
	run()
	got, err := os.ReadFile(trace)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "v1\nv1\nv2\nv2\n" {
		t.Fatalf("trace = %q, want stable link switch without PATH reload", got)
	}
}

func TestSessionLaunchResumeAdaptersExecuteContinuationSemantics(t *testing.T) {
	tests := []struct {
		tool string
		want string
	}{
		{tool: "codex", want: "resume"},
		{tool: "claude", want: "--continue"},
		{tool: "opencode", want: "--continue --prompt"},
		{tool: "configured-agent", want: "--continue"},
	}
	for _, tt := range tests {
		t.Run(tt.tool, func(t *testing.T) {
			binDir := t.TempDir()
			trace := filepath.Join(t.TempDir(), "trace")
			toolPath := filepath.Join(binDir, tt.tool)
			if err := os.WriteFile(toolPath, []byte("#!/bin/sh\nprintf '%s' \"$*\" > \"$TRACE\"\n"), 0o755); err != nil {
				t.Fatal(err)
			}
			d := &Daemon{cfg: Config{RepoDir: t.TempDir(), CLITool: tt.tool, SessionShell: "/bin/sh", DangerouslySkipPermissions: true}}
			artifact, err := d.prepareSessionLaunchArtifact(sessionLaunchSpec{Mode: sessionLaunchResume, ProjectID: protocol.DefaultProjectID, IssueID: "dky", SessionID: "az-dky", Prompt: "continue"})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(artifact.remove)
			cmd := exec.Command("/bin/sh", "-c", artifact.Command)
			cmd.Env = append(os.Environ(), "PATH="+binDir+string(os.PathListSeparator)+"/usr/bin:/bin", "TRACE="+trace)
			cmd.Stdin = strings.NewReader("exit\n")
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("execute resume artifact: %v\n%s", err, out)
			}
			got, err := os.ReadFile(trace)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(got), tt.want) {
				t.Fatalf("executed args = %q, want continuation %q", got, tt.want)
			}
			if tt.tool == "configured-agent" && strings.Contains(string(got), "dangerously") {
				t.Fatalf("configured adapter inherited provider permission flag: %q", got)
			}
		})
	}
}

func TestSessionLaunchArtifactQuotesConfiguredShellEverywhere(t *testing.T) {
	base := t.TempDir()
	shellDir := filepath.Join(base, "shell dir")
	if err := os.MkdirAll(shellDir, 0o755); err != nil {
		t.Fatal(err)
	}
	shellPath := filepath.Join(shellDir, "sh;safe")
	if err := os.Symlink("/bin/sh", shellPath); err != nil {
		t.Fatal(err)
	}
	binDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(binDir, "configured-agent"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	d := &Daemon{cfg: Config{RepoDir: base, CLITool: "configured-agent", SessionShell: shellPath}}
	artifact, err := d.prepareSessionLaunchArtifact(sessionLaunchSpec{Mode: sessionLaunchInitial, ProjectID: protocol.DefaultProjectID, IssueID: "dky", SessionID: "az-dky"})
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("/bin/sh", "-c", artifact.Command)
	cmd.Env = append(os.Environ(), "PATH="+binDir+string(os.PathListSeparator)+"/usr/bin:/bin")
	cmd.Stdin = strings.NewReader("exit\n")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("execute artifact through metacharacter shell path: %v\n%s", err, out)
	}
}

func TestRealProcessProfileWorktreeInitCommandsPreserveDirectoryAndEnvironment(t *testing.T) {
	worktree := t.TempDir()
	parentWorktree := t.TempDir()
	repoDir := t.TempDir()
	d := &Daemon{
		cfg: Config{
			RepoDir:      repoDir,
			SessionShell: "sh",
			WorktreeInitCommands: []string{
				"printf seeded > .worktree-init-test",
				"printf '%s\\n%s\\n%s\\n%s\\n%s\\n%s\\n' \"$AZEDARACH_PROJECT_ID\" \"$AZEDARACH_PROJECT_ROOT\" \"$AZEDARACH_ISSUE_ID\" \"$AZEDARACH_WORKTREE_PATH\" \"$AZEDARACH_PARENT_WORKTREE_PATH\" \"$AZEDARACH_WORKTREE_INIT_PHASE\" > .worktree-init-env",
			},
		},
	}

	if err := d.runWorktreeSyncInitCommands(context.Background(), worktreeInitContext{
		ProjectID:          protocol.DefaultProjectID,
		IssueID:            "az-123",
		WorktreePath:       worktree,
		ParentIssueID:      "az-122",
		ParentWorktreePath: parentWorktree,
	}); err != nil {
		t.Fatalf("runWorktreeInitCommands error: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(worktree, ".worktree-init-test"))
	if err != nil {
		t.Fatalf("read init marker: %v", err)
	}
	if strings.TrimSpace(string(data)) != "seeded" {
		t.Fatalf("init marker content = %q, want seeded", string(data))
	}
	envData, err := os.ReadFile(filepath.Join(worktree, ".worktree-init-env"))
	if err != nil {
		t.Fatalf("read init env: %v", err)
	}
	wantEnv := strings.Join([]string{
		protocol.DefaultProjectID,
		repoDir,
		"az-123",
		worktree,
		parentWorktree,
		"sync",
	}, "\n")
	if strings.TrimSpace(string(envData)) != wantEnv {
		t.Fatalf("init env = %q, want %q", strings.TrimSpace(string(envData)), wantEnv)
	}
}

func TestManagedDaemonProcessPathCoversPreSessionInitAndResourceCommands(t *testing.T) {
	base := t.TempDir()
	generationDir := filepath.Join(base, "install", ".azedarach-generations", "generation.current")
	staleDir := filepath.Join(base, "repo", "bin")
	for _, dir := range []string{generationDir, staleDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	tracePath := filepath.Join(base, "trace")
	writeExecutable := func(path, label string) {
		t.Helper()
		body := "#!/bin/sh\nprintf '" + label + "|%s\\n' \"$*\" >> \"$TRACE\"\n"
		if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, binary := range []string{"az", "azd"} {
		writeExecutable(filepath.Join(generationDir, binary), "fresh-"+binary)
		writeExecutable(filepath.Join(staleDir, binary), "STALE-"+binary)
	}
	t.Setenv("TRACE", tracePath)
	t.Setenv("PATH", appconfig.PrependPathEntry(staleDir+string(os.PathListSeparator)+generationDir+string(os.PathListSeparator)+"/usr/bin:/bin", generationDir))

	root, worktree := filepath.Join(base, "root"), filepath.Join(base, "worktree")
	for _, dir := range []string{root, worktree} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	d := &Daemon{cfg: Config{
		RepoDir:                   root,
		SessionShell:              "sh",
		ManagedGenerationBinDir:   generationDir,
		WorktreeInitCommands:      []string{"az sync-init", "azd sync-init"},
		WorktreeAsyncInitCommands: []string{"az async-init", "azd async-init"},
		IssueResources: appconfig.IssueResourcesConfig{
			PrepareCommands:  []string{"az resource-prepare", "azd resource-prepare"},
			ReconcileCommand: "az resource-reconcile && azd resource-reconcile",
			CleanupCommands:  []string{"az resource-cleanup", "azd resource-cleanup"},
		},
	}}
	initCtx := worktreeInitContext{ProjectID: protocol.DefaultProjectID, IssueID: "djm", ProjectRoot: root, WorktreePath: worktree}
	if err := d.runWorktreeSyncInitCommands(context.Background(), initCtx); err != nil {
		t.Fatal(err)
	}
	if err := d.runWorktreeInitCommandList(context.Background(), initCtx, "async", d.cfg.WorktreeAsyncInitCommands); err != nil {
		t.Fatal(err)
	}
	resourceCtx := issueResourceLifecycleContext{ProjectID: protocol.DefaultProjectID, IssueID: "djm", SessionID: "az-djm", RootPath: root, WorktreePath: worktree}
	if _, err := d.runIssueResourcePrepareCommands(context.Background(), protocol.DefaultProjectID, resourceCtx); err != nil {
		t.Fatal(err)
	}
	if _, err := d.runIssueResourceReconcileCommand(context.Background(), protocol.DefaultProjectID, resourceCtx, "present"); err != nil {
		t.Fatal(err)
	}
	if _, err := d.runIssueResourceCleanupCommands(context.Background(), protocol.DefaultProjectID, resourceCtx); err != nil {
		t.Fatal(err)
	}

	traceBytes, err := os.ReadFile(tracePath)
	if err != nil {
		t.Fatal(err)
	}
	trace := string(traceBytes)
	if strings.Contains(trace, "STALE-") {
		t.Fatalf("pre-session daemon command used stale binary:\n%s", trace)
	}
	for _, phase := range []string{"sync-init", "async-init", "resource-prepare", "resource-reconcile", "resource-cleanup"} {
		for _, binary := range []string{"az", "azd"} {
			if !strings.Contains(trace, "fresh-"+binary+"|"+phase) {
				t.Fatalf("trace missing fresh %s %s invocation:\n%s", binary, phase, trace)
			}
		}
	}
}

func TestRunWorktreeInitCommandsReturnsCommandFailure(t *testing.T) {
	worktree := t.TempDir()
	var gotShell, gotDir, gotCommand string
	var gotEnv []string
	d := &Daemon{
		cfg: Config{
			SessionShell: "sh",
			WorktreeInitCommands: []string{
				"exit 7",
			},
		},
		sessionShellRun: func(_ context.Context, shell, dir, command string, env []string) ([]byte, error) {
			gotShell, gotDir, gotCommand = shell, dir, command
			gotEnv = append([]string(nil), env...)
			return []byte("deterministic failure"), errors.New("exit status 7")
		},
	}

	err := d.runWorktreeInitCommands(context.Background(), protocol.DefaultProjectID, worktree)
	if err == nil {
		t.Fatal("runWorktreeInitCommands error = nil, want failure")
	}
	if !strings.Contains(err.Error(), "exit 7") {
		t.Fatalf("error = %q, want failed command context", err.Error())
	}
	if gotShell != "sh" || gotDir != worktree || gotCommand != "exit 7" {
		t.Fatalf("shell run = shell %q dir %q command %q, want sh/%s/exit 7", gotShell, gotDir, gotCommand, worktree)
	}
	if joined := strings.Join(gotEnv, "\n"); !strings.Contains(joined, "AZEDARACH_WORKTREE_PATH="+worktree) || !strings.Contains(joined, "AZEDARACH_WORKTREE_INIT_PHASE=sync") {
		t.Fatalf("shell env missing worktree init contract: %v", gotEnv)
	}
}

func TestRunWorktreeInitCommandsMissingCommandReturnsFailure(t *testing.T) {
	d := &Daemon{
		cfg: Config{
			SessionShell: "sh",
			WorktreeInitCommands: []string{
				"definitely-missing-command-xyz",
			},
		},
		sessionShellRun: func(_ context.Context, _, _, command string, _ []string) ([]byte, error) {
			return []byte("sh: " + command + ": not found"), errors.New("exit status 127")
		},
	}

	err := d.runWorktreeInitCommands(context.Background(), protocol.DefaultProjectID, t.TempDir())
	if err == nil {
		t.Fatal("runWorktreeInitCommands error = nil, want failure")
	}
	if !strings.Contains(err.Error(), "definitely-missing-command-xyz") {
		t.Fatalf("error = %q, want missing command context", err.Error())
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Fatalf("error = %q, want shell not-found signal", err.Error())
	}
}

func TestStartWorktreeAsyncInitCommandsDoesNotBlock(t *testing.T) {
	worktree := t.TempDir()
	runnerStarted := make(chan struct{})
	releaseRunner := make(chan struct{})
	runnerDone := make(chan struct{})
	t.Cleanup(func() {
		select {
		case <-releaseRunner:
		default:
			close(releaseRunner)
		}
	})
	d := &Daemon{
		cfg: Config{
			RepoDir:                   t.TempDir(),
			SessionShell:              "sh",
			WorktreeAsyncInitCommands: []string{"async command"},
			Logger:                    slog.New(slog.NewTextHandler(io.Discard, nil)),
		},
		sessionShellRun: func(ctx context.Context, _, dir, command string, _ []string) ([]byte, error) {
			if dir != worktree || command != "async command" {
				return nil, fmt.Errorf("unexpected async shell run dir=%q command=%q", dir, command)
			}
			close(runnerStarted)
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-releaseRunner:
				close(runnerDone)
				return nil, nil
			}
		},
	}

	started := time.Now()
	d.startWorktreeAsyncInitCommands(worktreeInitContext{
		ProjectID:    protocol.DefaultProjectID,
		IssueID:      "az-async",
		WorktreePath: worktree,
	})
	if elapsed := time.Since(started); elapsed > 100*time.Millisecond {
		t.Fatalf("startWorktreeAsyncInitCommands blocked for %s", elapsed)
	}

	select {
	case <-runnerStarted:
	case <-time.After(time.Second):
		t.Fatal("async runner did not start")
	}
	close(releaseRunner)
	select {
	case <-runnerDone:
	case <-time.After(time.Second):
		t.Fatal("async runner did not finish")
	}
}

func TestRealProcessProfileIssueResourceCommandsReceiveContextAndConfiguredEnv(t *testing.T) {
	worktree := t.TempDir()
	d := &Daemon{
		cfg: Config{
			RepoDir:      "/repo/root",
			SessionShell: "sh",
			IssueResources: appconfig.IssueResourcesConfig{
				Env: map[string]string{
					"RESOURCE_DB":        "db_$AZEDARACH_ISSUE_ID",
					"RESOURCE_URL":       "postgres://localhost/$RESOURCE_DB",
					"PADDED":             "  keep me  ",
					"AZEDARACH_ISSUE_ID": "wrong",
				},
				PrepareCommands: []string{
					"printf '%s|%s|%s|%s|%s|%s|%s|<%s>' \"$AZEDARACH_PROJECT_ID\" \"$AZEDARACH_ISSUE_ID\" \"$AZEDARACH_SESSION_ID\" \"$AZEDARACH_WORKTREE_PATH\" \"$AZEDARACH_BRANCH\" \"$RESOURCE_DB\" \"$RESOURCE_URL\" \"$PADDED\" > resource-env",
				},
			},
		},
	}
	resourceCtx := issueResourceLifecycleContext{
		ProjectID:    "proj",
		IssueID:      "az-123",
		SessionID:    "proj-az-123",
		WorktreePath: worktree,
		RootPath:     "/repo/root",
		Branch:       "user/az-123/demo",
	}

	result, err := d.runIssueResourcePrepareCommands(context.Background(), protocol.DefaultProjectID, resourceCtx)
	if err != nil {
		t.Fatalf("runIssueResourcePrepareCommands error: %v", err)
	}
	if len(result.Ran) != 1 {
		t.Fatalf("commands ran = %+v, want one prepare command", result.Ran)
	}
	data, err := os.ReadFile(filepath.Join(worktree, "resource-env"))
	if err != nil {
		t.Fatalf("read resource env marker: %v", err)
	}
	want := "proj|az-123|proj-az-123|" + worktree + "|user/az-123/demo|db_az-123|postgres://localhost/db_az-123|<  keep me  >"
	if strings.TrimSpace(string(data)) != want {
		t.Fatalf("resource env marker = %q, want %q", strings.TrimSpace(string(data)), want)
	}
}

func TestRealProcessProfileIssueResourceReconcileCommandReceivesDesiredState(t *testing.T) {
	worktree := t.TempDir()
	root := t.TempDir()
	d := &Daemon{
		cfg: Config{
			RepoDir:      root,
			SessionShell: "sh",
			IssueResources: appconfig.IssueResourcesConfig{
				Env: map[string]string{
					"AZEDARACH_RESOURCE_DESIRED_STATE": "wrong",
				},
				ReconcileCommand: "printf '%s|%s|%s|%s' \"$AZEDARACH_ISSUE_ID\" \"$AZEDARACH_RESOURCE_DESIRED_STATE\" \"$AZEDARACH_WORKTREE_PATH\" \"$(pwd)\" > reconcile-env",
			},
		},
	}
	resourceCtx := issueResourceLifecycleContext{
		ProjectID:    "proj",
		IssueID:      "az-123",
		SessionID:    "proj-az-123",
		WorktreePath: worktree,
		RootPath:     root,
		Branch:       "user/az-123/demo",
	}

	result, err := d.runIssueResourceReconcileCommand(context.Background(), protocol.DefaultProjectID, resourceCtx, "present")
	if err != nil {
		t.Fatalf("runIssueResourceReconcileCommand error: %v", err)
	}
	if len(result.Ran) != 1 {
		t.Fatalf("commands ran = %+v, want one reconcile command", result.Ran)
	}
	data, err := os.ReadFile(filepath.Join(root, "reconcile-env"))
	if err != nil {
		t.Fatalf("read reconcile env marker: %v", err)
	}
	wantRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatalf("eval root symlink: %v", err)
	}
	want := "az-123|present|" + worktree + "|" + wantRoot
	if strings.TrimSpace(string(data)) != want {
		t.Fatalf("reconcile env marker = %q, want %q", strings.TrimSpace(string(data)), want)
	}
}

func TestRealProcessProfileIssueResourceCommandsUseRootPathWhenWorktreeMissing(t *testing.T) {
	root := t.TempDir()
	d := &Daemon{
		cfg: Config{
			RepoDir:      root,
			SessionShell: "sh",
			IssueResources: appconfig.IssueResourcesConfig{
				CleanupCommands: []string{
					"printf '%s|%s' \"$AZEDARACH_WORKTREE_PATH\" \"$(pwd)\" > cleanup-env",
				},
			},
		},
	}
	resourceCtx := issueResourceLifecycleContext{
		ProjectID: "proj",
		IssueID:   "az-123",
		SessionID: "proj-az-123",
		RootPath:  root,
	}

	result, err := d.runIssueResourceCleanupCommands(context.Background(), protocol.DefaultProjectID, resourceCtx)
	if err != nil {
		t.Fatalf("runIssueResourceCleanupCommands error: %v", err)
	}
	if len(result.Ran) != 1 {
		t.Fatalf("commands ran = %+v, want one cleanup command", result.Ran)
	}
	data, err := os.ReadFile(filepath.Join(root, "cleanup-env"))
	if err != nil {
		t.Fatalf("read cleanup env marker: %v", err)
	}
	wantRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatalf("eval root symlink: %v", err)
	}
	want := "|" + wantRoot
	if strings.TrimSpace(string(data)) != want {
		t.Fatalf("cleanup env marker = %q, want %q", strings.TrimSpace(string(data)), want)
	}
}

func TestIssueResourceSessionEnvExportUsesStableContext(t *testing.T) {
	tmuxRunner := newSessionStartTmuxRunner()
	tmuxRunner.sessions["proj-az-123"] = true
	d := &Daemon{
		cfg: Config{
			SessionShell: "sh",
			IssueResources: appconfig.IssueResourcesConfig{
				Env: map[string]string{
					"DATABASE_URL": "postgres://localhost/db_$AZEDARACH_ISSUE_ID",
					"PADDED":       "  keep me  ",
					"bad-name":     "ignored",
				},
			},
		},
		tmux: tmux.NewClient(tmuxRunner, slog.Default()),
	}
	resourceCtx := issueResourceLifecycleContext{
		ProjectID:    "proj",
		IssueID:      "az-123",
		SessionID:    "proj-az-123",
		WorktreePath: "/repo/worktree",
		RootPath:     "/repo",
		Branch:       "user/az-123/demo",
	}

	if err := d.exportIssueResourceSessionEnv(context.Background(), protocol.DefaultProjectID, resourceCtx); err != nil {
		t.Fatalf("exportIssueResourceSessionEnv error: %v", err)
	}
	if tmuxRunner.sendKeysCalls != 1 {
		t.Fatalf("send-keys calls = %d, want one env export", tmuxRunner.sendKeysCalls)
	}
	payload := tmuxRunner.sendKeysPayloads[0]
	for _, want := range []string{
		"export ",
		"AZEDARACH_PROJECT_ID='proj'",
		"AZEDARACH_ISSUE_ID='az-123'",
		"AZEDARACH_SESSION_ID='proj-az-123'",
		"AZEDARACH_WORKTREE_PATH='/repo/worktree'",
		"AZEDARACH_ROOT_PATH='/repo'",
		"AZEDARACH_BRANCH='user/az-123/demo'",
		"DATABASE_URL='postgres://localhost/db_az-123'",
		"PADDED='  keep me  '",
	} {
		if !strings.Contains(payload, want) {
			t.Fatalf("export payload = %q, want %q", payload, want)
		}
	}
	if strings.Contains(payload, "bad-name") {
		t.Fatalf("export payload = %q, want invalid env name ignored", payload)
	}
}

func TestSessionStartWaitsForWorktreeSyncInitBeforeCreatingTmuxSession(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	repoDir := t.TempDir()
	projectID := "proj"
	if err := os.MkdirAll(filepath.Join(repoDir, ".azedarach"), 0o755); err != nil {
		t.Fatalf("mkdir .azedarach: %v", err)
	}

	issuesClient := newMigratedIssueClient(t, repoDir, slog.Default())
	t.Cleanup(func() { _ = issuesClient.CloseDB() })
	issueID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title: "Wait for worktree sync init",
		Type:  domain.TypeTask,
	})
	if err != nil {
		t.Fatalf("create issue: %v", err)
	}

	worktreePath := filepath.Join(filepath.Dir(repoDir), filepath.Base(repoDir)+"-"+issueID)
	initStartedPath := filepath.Join(worktreePath, "init-started")
	initReleasePath := filepath.Join(worktreePath, "init-release")
	initStatePath := filepath.Join(worktreePath, "init-state")
	worktreeRunner := &worktreeCreateRunner{
		worktreePath: worktreePath,
		branchName:   "testuser/" + issueID + "/wait-for-sync-init",
	}
	tmuxRunner := newSessionStartTmuxRunner()
	tmuxCreated := make(chan struct{}, 1)
	tmuxRunner.onNewSession = func(string) {
		tmuxCreated <- struct{}{}
	}
	store := daemonstate.NewStore()

	d := &Daemon{
		cfg: Config{
			RepoDir:      repoDir,
			BaseBranch:   "main",
			CLITool:      "codex",
			SessionShell: "sh",
			WorktreeInitCommands: []string{
				"printf started > init-started; while [ ! -f init-release ]; do sleep 0.01; done; printf finished > init-state",
			},
			Logger: slog.Default(),
		},
		tmux:         tmux.NewClient(tmuxRunner, slog.Default()),
		issues:       issuesClient,
		session:      daemonhandlers.NewSessionHandler(store),
		sessionStore: store,
		revision:     map[string]uint64{},
		worktreeManagersByRoot: map[string]*git.WorktreeManager{
			repoDir: git.NewWorktreeManager(worktreeRunner, repoDir, slog.Default()),
		},
		worktreeManagersByProject: map[string]*git.WorktreeManager{
			projectID: git.NewWorktreeManager(worktreeRunner, repoDir, slog.Default()),
		},
	}

	type startResult struct {
		resp protocol.ResponseEnvelope
		err  error
	}
	resultCh := make(chan startResult, 1)
	go func() {
		resp, startErr := d.handleSessionStartDirect(ctx, protocol.RequestEnvelope{
			ProtocolVersion: protocol.CurrentVersion,
			RequestID:       "req-start-wait-worktree-init",
			Kind:            protocol.EnvelopeKindCommand,
			Command:         "session.start",
			Meta:            protocol.Metadata{ProjectID: naming.ProjectID(projectID)},
			Body: marshalJSON(map[string]any{
				"project_id": projectID,
				"session_id": issueID,
				"start_work": false,
			}),
		})
		resultCh <- startResult{resp: resp, err: startErr}
	}()

	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, statErr := os.Stat(initStartedPath); statErr == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("worktree sync init did not start before deadline: %s", initStartedPath)
		}
		time.Sleep(10 * time.Millisecond)
	}

	tmuxCreatedBeforeRelease := false
	select {
	case <-tmuxCreated:
		tmuxCreatedBeforeRelease = true
	case <-time.After(100 * time.Millisecond):
	}
	if err := os.WriteFile(initReleasePath, []byte("release"), 0o644); err != nil {
		t.Fatalf("release worktree sync init: %v", err)
	}

	var result startResult
	select {
	case result = <-resultCh:
	case <-time.After(2 * time.Second):
		t.Fatal("session start did not finish after worktree sync init was released")
	}
	if result.err != nil {
		t.Fatalf("handleSessionStartDirect returned error: %v", result.err)
	}
	if !result.resp.OK || result.resp.Error != nil {
		t.Fatalf("session start response = %+v, want success", result.resp)
	}
	if tmuxCreatedBeforeRelease {
		t.Fatal("tmux session was created while worktree sync init was still blocked")
	}
	select {
	case <-tmuxCreated:
	default:
		t.Fatal("tmux session was not created after worktree sync init finished")
	}
	state, err := os.ReadFile(initStatePath)
	if err != nil || strings.TrimSpace(string(state)) != "finished" {
		t.Fatalf("worktree sync init state = %q, err = %v, want finished", strings.TrimSpace(string(state)), err)
	}
}

func TestSessionStartInitFailureCleansUpNewWorktree(t *testing.T) {
	ctx := context.Background()
	repoDir := t.TempDir()
	projectID := "proj"
	if err := os.MkdirAll(filepath.Join(repoDir, ".azedarach"), 0o755); err != nil {
		t.Fatalf("mkdir .azedarach: %v", err)
	}

	issuesClient := newMigratedIssueClient(t, repoDir, slog.Default())
	t.Cleanup(func() { _ = issuesClient.CloseDB() })
	issueID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title: "Init failure should cleanup worktree",
		Type:  domain.TypeTask,
	})
	if err != nil {
		t.Fatalf("create issue: %v", err)
	}

	worktreePath := filepath.Join(filepath.Dir(repoDir), filepath.Base(repoDir)+"-"+issueID)
	branchName := "testuser/" + issueID + "/init-failure-cleanup"
	worktreeRunner := &initFailureCleanupWorktreeRunner{
		repoDir:      repoDir,
		worktreePath: worktreePath,
		branchName:   branchName,
	}
	tmuxRunner := newSessionStartTmuxRunner()
	store := daemonstate.NewStore()

	d := &Daemon{
		cfg: Config{
			RepoDir:      repoDir,
			BaseBranch:   "main",
			CLITool:      "codex",
			SessionShell: "sh",
			WorktreeInitCommands: []string{
				"definitely-missing-command-xyz",
			},
			Logger: slog.Default(),
		},
		tmux:         tmux.NewClient(tmuxRunner, slog.Default()),
		issues:       issuesClient,
		session:      daemonhandlers.NewSessionHandler(store),
		sessionStore: store,
		revision:     map[string]uint64{},
		worktreeManagersByRoot: map[string]*git.WorktreeManager{
			repoDir: git.NewWorktreeManager(worktreeRunner, repoDir, slog.Default()),
		},
		worktreeManagersByProject: map[string]*git.WorktreeManager{
			projectID: git.NewWorktreeManager(worktreeRunner, repoDir, slog.Default()),
		},
	}

	req := protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-start-init-fail",
		Kind:            protocol.EnvelopeKindCommand,
		Command:         "session.start",
		Meta: protocol.Metadata{
			ProjectID: naming.ProjectID(projectID),
		},
		Body: marshalJSON(map[string]string{
			"project_id": projectID,
			"session_id": issueID,
		}),
	}

	resp, err := d.handleSessionStartDirect(ctx, req)
	if err != nil {
		t.Fatalf("handleSessionStartDirect returned error: %v", err)
	}
	if resp.OK {
		t.Fatalf("session start response OK = true, want failure: %+v", resp)
	}
	if !worktreeRunner.removeForced {
		t.Fatal("expected cleanup to remove newly-created worktree with --force")
	}
	if worktreeRunner.worktreeExists {
		t.Fatal("expected worktree to be removed after init failure")
	}
	if len(tmuxRunner.sessions) != 0 {
		t.Fatalf("tmux sessions = %v, want none when init fails", tmuxRunner.sessions)
	}

	if resp.Error == nil {
		t.Fatalf("expected error envelope, got nil: %+v", resp)
	}
	message := resp.Error.Message
	if !strings.Contains(message, "worktree init failed") {
		t.Fatalf("error message = %q, want init failure context", message)
	}
	if !strings.Contains(message, "definitely-missing-command-xyz") || !strings.Contains(message, "no such file or directory") {
		t.Fatalf("error message = %q, want failing init command output", message)
	}
	if strings.Contains(message, "tmux new-session") {
		t.Fatalf("error message = %q, init failure should not be reported as tmux launch failure", message)
	}
	if !strings.Contains(message, "cleaned up worktree") {
		t.Fatalf("error message = %q, want cleanup context", message)
	}
}

func TestApplySessionLifecycleTransitionPublishesProjectionEvents(t *testing.T) {
	const (
		projectID = "proj"
		sessionID = "proj-az-1"
		issueID   = "az-1"
	)

	store := daemonstate.NewStore()
	hub := publish.NewHub(32, 8, slog.Default())
	runtimeStateStore := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(t.TempDir(), "projection.db"), slog.Default())
	t.Cleanup(func() {
		_ = runtimeStateStore.Close()
	})
	daemon := &Daemon{
		cfg: Config{
			RepoDir: ".",
			Logger:  slog.Default(),
		},
		hub:          hub,
		session:      daemonhandlers.NewSessionHandler(store),
		sessionStore: store,
		revision:     map[string]uint64{},
		runtimeStoresByRoot: map[string]*daemonstate.RuntimeStateStore{
			".": runtimeStateStore,
		},
	}

	ch, cancel := hub.Subscribe(projectID, 0)
	defer cancel()

	startReq := protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-session.start",
		Kind:            protocol.EnvelopeKindCommand,
		Meta: protocol.Metadata{
			ProjectID: naming.ProjectID(projectID),
		},
		Command: daemonhandlers.CommandSessionStart,
		Body: marshalJSON(map[string]string{
			"project_id": projectID,
			"session_id": sessionID,
			"issue_id":   issueID,
		}),
	}

	if err := daemon.applySessionLifecycleTransition(context.Background(), startReq, projectID, sessionID, issueID, daemonhandlers.CommandSessionStart); err != nil {
		t.Fatalf("apply start transition: %v", err)
	}

	startRows, err := runtimeStateStore.ListSessionStates(context.Background(), projectID)
	if err != nil {
		t.Fatalf("load runtime rows after start: %v", err)
	}
	if len(startRows) != 1 {
		t.Fatalf("runtime rows after start = %+v, want 1 row", startRows)
	}
	if startRows[0].State != daemonstate.SessionStateStarting || startRows[0].ObservedState != "" {
		t.Fatalf("runtime rows after start = %+v, want desired starting without fabricated observation", startRows)
	}

	stopReq := startReq
	stopReq.RequestID = "req-session.stop"
	stopReq.Command = daemonhandlers.CommandSessionStop
	if err := daemon.applySessionLifecycleTransition(context.Background(), stopReq, projectID, sessionID, issueID, daemonhandlers.CommandSessionStop); err != nil {
		t.Fatalf("apply stop transition: %v", err)
	}

	events := collectSessionProjectionEvents(t, ch, 2)
	wantStates := []protocol.SessionLifecycleState{
		protocol.SessionLifecycleStateStarting,
		protocol.SessionLifecycleStateStopped,
	}
	wantRuntimeStates := []protocol.SessionLifecycleState{
		protocol.SessionLifecycleStateStarting,
		protocol.SessionLifecycleStateStopped,
	}
	for i, event := range events {
		if event.Event != protocol.EventSessionUpdated {
			t.Fatalf("event[%d] = %s, want %s", i, event.Event, protocol.EventSessionUpdated)
		}
		if event.Revision != uint64(i+1) {
			t.Fatalf("event[%d] revision = %d, want %d", i, event.Revision, i+1)
		}
		var body protocol.SessionProjectionEventBody
		if err := json.Unmarshal(event.Body, &body); err != nil {
			t.Fatalf("unmarshal event body: %v", err)
		}
		if body.ProjectID != projectID {
			t.Fatalf("body project_id = %s, want %s", body.ProjectID, projectID)
		}
		if body.Revision != event.Revision {
			t.Fatalf("body revision = %d, want %d", body.Revision, event.Revision)
		}
		if body.Session.SessionID != sessionID {
			t.Fatalf("body session id = %s, want %s", body.Session.SessionID, sessionID)
		}
		if body.Session.IssueID != issueID {
			t.Fatalf("body issue id = %s, want %s", body.Session.IssueID, issueID)
		}
		if body.Session.State != wantStates[i] {
			t.Fatalf("body session state = %s, want %s", body.Session.State, wantStates[i])
		}
		if body.Session.UpdatedAt.IsZero() {
			t.Fatal("expected updated_at to be populated")
		}
		if body.Runtime == nil {
			t.Fatalf("event[%d] expected runtime projection delta", i)
		}
		if body.Runtime.ProjectID != projectID || body.Runtime.Revision != event.Revision {
			t.Fatalf("body runtime envelope = %+v, want project/revision %s/%d", body.Runtime, projectID, event.Revision)
		}
		if body.Runtime.Projection.IssueID != issueID || body.Runtime.Projection.Session.SessionID != sessionID {
			t.Fatalf("body runtime projection = %+v, want issue/session %s/%s", body.Runtime.Projection, issueID, sessionID)
		}
		if body.Runtime.Projection.Session.State != wantRuntimeStates[i] {
			runtimeRows, err := runtimeStateStore.ListSessionStates(context.Background(), projectID)
			if err != nil {
				t.Fatalf("body runtime session state = %s, want observed %s (load runtime rows failed: %v)", body.Runtime.Projection.Session.State, wantRuntimeStates[i], err)
			}
			t.Fatalf("body runtime session state = %s, want observed %s (runtime rows = %+v)", body.Runtime.Projection.Session.State, wantRuntimeStates[i], runtimeRows)
		}
	}
}

func TestEnrichTasksWithSessionStateSeedsStartedAtFromSnapshot(t *testing.T) {
	const (
		projectID = "proj-time"
		issueID   = "bia"
	)

	store := daemonstate.NewStore()
	runtimeStateStore := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(t.TempDir(), "azedarach.db"), slog.Default())
	t.Cleanup(func() {
		_ = runtimeStateStore.Close()
	})
	sessionID := naming.CanonicalSessionID(projectID, issueID)
	if _, err := store.UpsertSession(projectID, sessionID, issueID, daemonstate.SessionStateStarting); err != nil {
		t.Fatalf("seed starting session: %v", err)
	}
	if _, err := store.UpsertSession(projectID, sessionID, issueID, daemonstate.SessionStateAttached); err != nil {
		t.Fatalf("seed attached session: %v", err)
	}

	snapshot := store.ReadSnapshot(projectID)
	sessionSnapshot, ok := snapshot.Sessions[sessionID]
	if !ok {
		t.Fatalf("missing seeded session %q in snapshot", sessionID)
	}
	if sessionSnapshot.StartedAt == nil || sessionSnapshot.StartedAt.IsZero() {
		t.Fatal("expected seeded session started_at")
	}

	tmuxRunner := newSessionStartTmuxRunner()
	tmuxRunner.sessions[sessionID] = true

	d := &Daemon{
		cfg:          Config{RepoDir: ".", Logger: slog.Default()},
		tmux:         tmux.NewClient(tmuxRunner, slog.Default()),
		sessionStore: store,
		runtimeStoresByRoot: map[string]*daemonstate.RuntimeStateStore{
			".": runtimeStateStore,
		},
	}
	if err := upsertSessionStateFixture(runtimeStateStore, context.Background(), projectID, daemonstate.Session{
		ID:        sessionID,
		IssueID:   issueID,
		State:     daemonstate.SessionStateAttached,
		UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("seed projection session: %v", err)
	}

	runtimeUpdatedAt := time.Now().UTC().Add(time.Hour)
	tasks := []domain.Task{
		{ID: issueID, Title: "session elapsed should render", Type: domain.TypeTask, RuntimeUpdatedAt: runtimeUpdatedAt},
	}
	enriched := d.enrichTasksWithSessionState(context.Background(), projectID, tasks)
	if len(enriched) != 1 {
		t.Fatalf("len(enriched) = %d, want 1", len(enriched))
	}
	if enriched[0].Session == nil {
		t.Fatal("expected session projection to be attached")
	}
	if enriched[0].Session.StartedAt == nil {
		t.Fatal("expected session started_at to be seeded from daemon snapshot")
	}
	if !enriched[0].Session.StartedAt.Equal(sessionSnapshot.StartedAt.UTC()) {
		t.Fatalf("started_at = %v, want %v", enriched[0].Session.StartedAt, sessionSnapshot.StartedAt.UTC())
	}
	if !enriched[0].RuntimeUpdatedAt.Equal(runtimeUpdatedAt) {
		t.Fatalf("runtime_updated_at = %v, want preserved newer %v", enriched[0].RuntimeUpdatedAt, runtimeUpdatedAt)
	}
}

func TestEnrichTasksWithSessionStateFallsBackToProjectionCache(t *testing.T) {
	const (
		projectID = "proj-cache-fallback"
		issueID   = "bib"
	)

	store := daemonstate.NewStore()
	runtimeStateStore := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(t.TempDir(), "azedarach.db"), slog.Default())
	t.Cleanup(func() {
		_ = runtimeStateStore.Close()
	})

	sessionID := naming.CanonicalSessionID(projectID, issueID)
	cachedStartedAt := time.Date(2026, time.April, 1, 10, 0, 0, 0, time.UTC)
	if err := upsertSessionStateFixture(runtimeStateStore, context.Background(), projectID, daemonstate.Session{
		ID:        sessionID,
		IssueID:   issueID,
		State:     daemonstate.SessionStateAttached,
		StartedAt: &cachedStartedAt,
		UpdatedAt: cachedStartedAt,
	}); err != nil {
		t.Fatalf("seed projection session: %v", err)
	}

	tmuxRunner := newSessionStartTmuxRunner()
	tmuxRunner.sessions[sessionID] = true

	d := &Daemon{
		cfg:          Config{RepoDir: ".", Logger: slog.Default()},
		tmux:         tmux.NewClient(tmuxRunner, slog.Default()),
		sessionStore: store,
		runtimeStoresByRoot: map[string]*daemonstate.RuntimeStateStore{
			".": runtimeStateStore,
		},
	}

	tasks := []domain.Task{
		{ID: issueID, Title: "projection fallback", Type: domain.TypeTask},
	}
	enriched := d.enrichTasksWithSessionState(context.Background(), projectID, tasks)
	if len(enriched) != 1 {
		t.Fatalf("len(enriched) = %d, want 1", len(enriched))
	}
	if enriched[0].Session == nil {
		t.Fatal("expected session projection")
	}
	if enriched[0].Session.StartedAt == nil {
		t.Fatal("expected started_at from cached projection")
	}
	if !enriched[0].Session.StartedAt.Equal(cachedStartedAt) {
		t.Fatalf("started_at = %v, want %v", enriched[0].Session.StartedAt, cachedStartedAt)
	}
	if enriched[0].Session.Activity != "unknown" || enriched[0].Session.ActivitySource != "none" {
		t.Fatalf("activity = %s/%s, want unknown/none", enriched[0].Session.Activity, enriched[0].Session.ActivitySource)
	}
}

func TestActiveSessionIssueKeysIgnoreDesiredStoppedWithStaleObservedRunning(t *testing.T) {
	const issueID = "bix"
	sessionID := naming.CanonicalSessionID("proj-stale-observed", issueID)

	active := activeSessionIssueKeysFromProjection([]daemonstate.Session{
		{
			ID:            sessionID,
			IssueID:       issueID,
			State:         daemonstate.SessionStateStopped,
			ObservedState: daemonstate.SessionStateRunning,
			UpdatedAt:     time.Now().UTC(),
		},
	}, "proj-stale-observed")

	if _, ok := active[sessionKey(issueID)]; ok {
		t.Fatalf("desired-stopped session %s was treated as active: %+v", issueID, active)
	}
}

func TestImplementationSessionAggregatesIgnoreAdvisorSessions(t *testing.T) {
	const issueID = "biz"
	sessions := []daemonstate.Session{{
		ID: "advisor-request-1", IssueID: issueID, Role: daemonstate.SessionRoleAdvisor,
		ScopeKind: daemonstate.SessionScopeInteraction, ScopeID: "request-1",
		State: daemonstate.SessionStateRunning, ObservedState: daemonstate.SessionStateRunning,
	}}
	if counts := sessionProjectionCountsByIssueKey(sessions, "project")[sessionKey(issueID)]; counts.Total != 0 || counts.Active != 0 {
		t.Fatalf("advisor session leaked into implementation counts: %+v", counts)
	}
	if _, active := activeSessionIssueKeysFromProjection(sessions, "project")[sessionKey(issueID)]; active {
		t.Fatal("advisor session made its attached implementation issue active")
	}
}

func TestActiveSessionIDsFromProjectionIgnoreDesiredStoppedWithStaleObservedRunning(t *testing.T) {
	const issueID = "biy"
	sessionID := naming.CanonicalSessionID("proj-stale-observed", issueID)
	daemon := &Daemon{}

	active := daemon.activeSessionIDsFromProjection("proj-stale-observed", []daemonstate.Session{
		{
			ID:            sessionID,
			IssueID:       issueID,
			State:         daemonstate.SessionStateStopped,
			ObservedState: daemonstate.SessionStateRunning,
			UpdatedAt:     time.Now().UTC(),
		},
	})

	if len(active) != 0 {
		t.Fatalf("desired-stopped session %s was treated as active IDs: %+v", issueID, active)
	}
}

func TestEnrichTasksWithSessionStateReportsNoAgentActivity(t *testing.T) {
	const (
		projectID = "proj-no-agent"
		issueID   = "bna"
	)

	store := daemonstate.NewStore()
	runtimeStateStore := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(t.TempDir(), "azedarach.db"), slog.Default())
	t.Cleanup(func() { _ = runtimeStateStore.Close() })

	sessionID := naming.CanonicalSessionID(projectID, issueID)
	now := time.Date(2026, time.April, 1, 10, 30, 0, 0, time.UTC)
	if err := upsertSessionStateFixture(runtimeStateStore, context.Background(), projectID, daemonstate.Session{
		ID:             sessionID,
		IssueID:        issueID,
		State:          daemonstate.SessionStateRunning,
		ObservedState:  daemonstate.SessionStateRunning,
		Activity:       "no-agent",
		ActivitySource: "session",
		StartedAt:      &now,
		UpdatedAt:      now,
	}); err != nil {
		t.Fatalf("seed no-agent projection session: %v", err)
	}

	d := &Daemon{
		cfg:          Config{RepoDir: ".", Logger: slog.Default()},
		sessionStore: store,
		runtimeStoresByRoot: map[string]*daemonstate.RuntimeStateStore{
			".": runtimeStateStore,
		},
	}

	tasks := []domain.Task{{ID: issueID, Title: "shell only", Type: domain.TypeTask}}
	enriched := d.enrichTasksWithSessionState(context.Background(), projectID, tasks)
	if len(enriched) != 1 || enriched[0].Session == nil {
		t.Fatalf("missing session projection: %+v", enriched)
	}
	if enriched[0].Session.Activity != "no-agent" || enriched[0].Session.ActivitySource != "session" {
		t.Fatalf("activity = %s/%s, want no-agent/session", enriched[0].Session.Activity, enriched[0].Session.ActivitySource)
	}
}

func TestSessionDisplayActivityUsesLatestParentSessionActivity(t *testing.T) {
	const (
		projectID = "proj-parent-activity"
		issueID   = "bpa"
	)

	now := time.Date(2026, time.April, 1, 10, 45, 0, 0, time.UTC)
	sessions := []daemonstate.Session{
		{
			ID:             naming.CanonicalSessionID(projectID, issueID) + "-older",
			IssueID:        issueID,
			State:          daemonstate.SessionStateRunning,
			ObservedState:  daemonstate.SessionStateRunning,
			Activity:       "busy",
			ActivitySource: "runtime",
			UpdatedAt:      now,
		},
		{
			ID:             naming.CanonicalSessionID(projectID, issueID),
			IssueID:        issueID,
			State:          daemonstate.SessionStateRunning,
			ObservedState:  daemonstate.SessionStateRunning,
			Activity:       "no-agent",
			ActivitySource: "session",
			UpdatedAt:      now.Add(time.Minute),
		},
	}

	activityByKey := sessionDisplayActivityByIssueKeyFromSessions(sessions, projectID)
	display, found := activityByKey[sessionKey(issueID)]
	if !found {
		t.Fatal("expected session activity")
	}
	if display.Activity != "no-agent" || display.Source != "session" {
		t.Fatalf("activity = %s/%s, want no-agent/session", display.Activity, display.Source)
	}
}

func TestSessionDisplayActivityKeepsNewerCanonicalIdleOverStalePaneBusy(t *testing.T) {
	const (
		projectID = "proj-stale-pane-busy"
		issueID   = "bpb"
	)

	now := time.Date(2026, time.April, 1, 10, 45, 0, 0, time.UTC)
	sessionID := naming.CanonicalSessionID(projectID, issueID)
	sessions := []daemonstate.Session{
		{
			ID:             sessionID + ".pane-500",
			IssueID:        issueID,
			State:          daemonstate.SessionStateRunning,
			ObservedState:  daemonstate.SessionStateRunning,
			Activity:       "busy",
			ActivitySource: "hooks",
			UpdatedAt:      now,
		},
		{
			ID:             sessionID,
			IssueID:        issueID,
			State:          daemonstate.SessionStateRunning,
			ObservedState:  daemonstate.SessionStateRunning,
			Activity:       "idle",
			ActivitySource: "hooks",
			UpdatedAt:      now.Add(time.Minute),
		},
	}

	activityByKey := sessionDisplayActivityByIssueKeyFromSessions(sessions, projectID)
	display, found := activityByKey[sessionKey(issueID)]
	if !found {
		t.Fatal("expected session activity")
	}
	if display.Activity != "idle" || display.Source != "hooks" {
		t.Fatalf("activity = %s/%s, want newer canonical idle/hooks", display.Activity, display.Source)
	}
}

func TestSessionDisplayActivityDoesNotLetUntimedPaneBusyOverrideTimedCanonicalIdle(t *testing.T) {
	const (
		projectID = "proj-untimed-pane-busy"
		issueID   = "bpc"
	)

	sessionID := naming.CanonicalSessionID(projectID, issueID)
	sessions := []daemonstate.Session{
		{
			ID:             sessionID + ".pane-500",
			IssueID:        issueID,
			State:          daemonstate.SessionStateRunning,
			ObservedState:  daemonstate.SessionStateRunning,
			Activity:       "busy",
			ActivitySource: "hooks",
		},
		{
			ID:             sessionID,
			IssueID:        issueID,
			State:          daemonstate.SessionStateRunning,
			ObservedState:  daemonstate.SessionStateRunning,
			Activity:       "idle",
			ActivitySource: "hooks",
			UpdatedAt:      time.Date(2026, time.April, 1, 10, 45, 0, 0, time.UTC),
		},
	}

	activityByKey := sessionDisplayActivityByIssueKeyFromSessions(sessions, projectID)
	display, found := activityByKey[sessionKey(issueID)]
	if !found {
		t.Fatal("expected session activity")
	}
	if display.Activity != "idle" || display.Source != "hooks" {
		t.Fatalf("activity = %s/%s, want timed canonical idle/hooks", display.Activity, display.Source)
	}
}

func TestSessionHookActivityIgnoresDesiredStoppedWithStaleObservedRunning(t *testing.T) {
	const (
		projectID = "proj-stale-hook"
		issueID   = "bih"
	)
	sessionID := naming.CanonicalSessionID(projectID, issueID) + ".pane-1"

	activityByKey := sessionHookActivityByIssueKeyFromSessions([]daemonstate.Session{
		{
			ID:             sessionID,
			IssueID:        issueID,
			State:          daemonstate.SessionStateStopped,
			ObservedState:  daemonstate.SessionStateRunning,
			Activity:       "busy",
			ActivitySource: "hooks",
			UpdatedAt:      time.Now().UTC(),
		},
	}, projectID)

	if activity, ok := activityByKey[sessionKey(issueID)]; ok {
		t.Fatalf("desired-stopped hook activity was carried forward: %+v", activity)
	}
}

func TestPersistTmuxSessionRuntimeStateDefaultsRecoveredSessionActivityBusy(t *testing.T) {
	const (
		projectID = "proj-recovered-agent"
		issueID   = "brc"
	)

	store := daemonstate.NewStore()
	runtimeStateStore := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(t.TempDir(), "azedarach.db"), slog.Default())
	t.Cleanup(func() { _ = runtimeStateStore.Close() })

	sessionID := naming.CanonicalSessionID(projectID, issueID)
	d := &Daemon{
		cfg:          Config{RepoDir: ".", Logger: slog.Default()},
		sessionStore: store,
		runtimeStoresByRoot: map[string]*daemonstate.RuntimeStateStore{
			".": runtimeStateStore,
		},
	}

	if err := d.persistTmuxSessionRuntimeState(context.Background(), projectID, []tmux.SessionInfo{{Name: sessionID}}, nil); err != nil {
		t.Fatalf("persist tmux session runtime state: %v", err)
	}

	tasks := []domain.Task{{ID: issueID, Title: "recovered worker", Type: domain.TypeTask}}
	enriched := d.enrichTasksWithSessionState(context.Background(), projectID, tasks)
	if len(enriched) != 1 || enriched[0].Session == nil {
		t.Fatalf("missing recovered session projection: %+v", enriched)
	}
	if enriched[0].Session.Activity != "busy" || enriched[0].Session.ActivitySource != "runtime" {
		t.Fatalf("activity = %s/%s, want busy/runtime", enriched[0].Session.Activity, enriched[0].Session.ActivitySource)
	}
}

func TestPersistTmuxSessionRuntimeStateAdmitsOnlyExactProjectManagedSessionsAcrossRestart(t *testing.T) {
	ctx := context.Background()
	const (
		firstProjectID  = "wedding"
		firstIssueID    = "wed-17"
		secondProjectID = "effect-prisma-generator"
		secondIssueID   = "efp-29"
		externalSession = "az"
	)

	firstStore := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(t.TempDir(), "first.db"), slog.Default())
	secondStore := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(t.TempDir(), "second.db"), slog.Default())
	t.Cleanup(func() {
		_ = firstStore.Close()
		_ = secondStore.Close()
	})
	firstSessionID := naming.CanonicalSessionID(firstProjectID, firstIssueID)
	secondSessionID := naming.CanonicalSessionID(secondProjectID, secondIssueID)
	inventory := []tmux.SessionInfo{{Name: externalSession}, {Name: firstSessionID}, {Name: secondSessionID}}

	newDaemon := func() *Daemon {
		return &Daemon{
			cfg:          Config{RepoDir: ".", Logger: slog.Default()},
			sessionStore: daemonstate.NewStore(),
			runtimeStoresByProject: map[string]*daemonstate.RuntimeStateStore{
				firstProjectID: firstStore, secondProjectID: secondStore,
			},
		}
	}
	for cycle, d := range []*Daemon{newDaemon(), newDaemon()} {
		for _, projectID := range []string{firstProjectID, secondProjectID} {
			if err := d.persistTmuxSessionRuntimeState(ctx, projectID, inventory, nil); err != nil {
				t.Fatalf("cycle %d persist project %s: %v", cycle, projectID, err)
			}
		}
	}

	for _, tc := range []struct {
		projectID string
		store     *daemonstate.RuntimeStateStore
		wantID    string
	}{
		{projectID: firstProjectID, store: firstStore, wantID: firstSessionID},
		{projectID: secondProjectID, store: secondStore, wantID: secondSessionID},
	} {
		rows, err := tc.store.ListSessionStates(ctx, tc.projectID)
		if err != nil {
			t.Fatalf("list %s sessions: %v", tc.projectID, err)
		}
		if len(rows) != 1 || rows[0].ID != tc.wantID {
			t.Fatalf("%s sessions = %+v, want only %s", tc.projectID, rows, tc.wantID)
		}
		if observation, found, err := tc.store.GetPhysicalSessionObservation(ctx, tc.projectID, externalSession); err != nil || found {
			t.Fatalf("%s external observation = %+v found=%v err=%v", tc.projectID, observation, found, err)
		}
	}
}

func TestPersistTmuxSessionRuntimeStatePreservesNoAgentActivity(t *testing.T) {
	const (
		projectID = "proj-reconcile-no-agent"
		issueID   = "bnr"
	)

	store := daemonstate.NewStore()
	runtimeStateStore := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(t.TempDir(), "azedarach.db"), slog.Default())
	t.Cleanup(func() { _ = runtimeStateStore.Close() })

	sessionID := naming.CanonicalSessionID(projectID, issueID)
	now := time.Date(2026, time.April, 1, 10, 45, 0, 0, time.UTC)
	if err := upsertSessionStateFixture(runtimeStateStore, context.Background(), projectID, daemonstate.Session{
		ID:             sessionID,
		IssueID:        issueID,
		State:          daemonstate.SessionStateRunning,
		ObservedState:  daemonstate.SessionStateRunning,
		Activity:       "no-agent",
		ActivitySource: "session",
		StartedAt:      &now,
		UpdatedAt:      now,
	}); err != nil {
		t.Fatalf("seed no-agent projection session: %v", err)
	}
	d := &Daemon{
		cfg:          Config{RepoDir: ".", Logger: slog.Default()},
		sessionStore: store,
		runtimeStoresByRoot: map[string]*daemonstate.RuntimeStateStore{
			".": runtimeStateStore,
		},
	}

	if err := d.persistTmuxSessionRuntimeState(context.Background(), projectID, []tmux.SessionInfo{{Name: sessionID}}, nil); err != nil {
		t.Fatalf("persist tmux session runtime state: %v", err)
	}

	session, found, err := runtimeStateStore.GetSessionState(context.Background(), projectID, sessionID)
	if err != nil {
		t.Fatalf("GetSessionState: %v", err)
	}
	if !found {
		t.Fatalf("missing session projection %q", sessionID)
	}
	if session.Activity != "no-agent" || session.ActivitySource != "session" {
		t.Fatalf("activity = %s/%s, want no-agent/session", session.Activity, session.ActivitySource)
	}
}

func TestEnrichTasksWithSessionStateReportsHookBackedActivity(t *testing.T) {
	const (
		projectID = "proj-hook-activity"
		busyID    = "bja"
		idleID    = "bjb"
	)

	store := daemonstate.NewStore()
	runtimeStateStore := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(t.TempDir(), "azedarach.db"), slog.Default())
	t.Cleanup(func() {
		_ = runtimeStateStore.Close()
	})

	busySessionID := naming.CanonicalSessionID(projectID, busyID) + ".pane-%1"
	idleSessionID := naming.CanonicalSessionID(projectID, idleID) + ".pane-%2"
	now := time.Now().UTC()
	if err := upsertSessionStateFixture(runtimeStateStore, context.Background(), projectID, daemonstate.Session{
		ID:            naming.CanonicalSessionID(projectID, busyID),
		IssueID:       busyID,
		State:         daemonstate.SessionStateRunning,
		ObservedState: daemonstate.SessionStateRunning,
		UpdatedAt:     now,
	}); err != nil {
		t.Fatalf("seed busy session: %v", err)
	}
	if err := upsertSessionStateFixture(runtimeStateStore, context.Background(), projectID, daemonstate.Session{
		ID:            naming.CanonicalSessionID(projectID, idleID),
		IssueID:       idleID,
		State:         daemonstate.SessionStatePaused,
		ObservedState: daemonstate.SessionStatePaused,
		UpdatedAt:     now,
	}); err != nil {
		t.Fatalf("seed idle session: %v", err)
	}
	if err := upsertSessionStateFixture(runtimeStateStore, context.Background(), projectID, daemonstate.Session{
		ID:             busySessionID,
		IssueID:        busyID,
		State:          daemonstate.SessionStateRunning,
		ObservedState:  daemonstate.SessionStateRunning,
		Activity:       "busy",
		ActivitySource: "hooks",
		UpdatedAt:      now,
	}); err != nil {
		t.Fatalf("seed busy hook session: %v", err)
	}
	if err := upsertSessionStateFixture(runtimeStateStore, context.Background(), projectID, daemonstate.Session{
		ID:             idleSessionID,
		IssueID:        idleID,
		State:          daemonstate.SessionStatePaused,
		ObservedState:  daemonstate.SessionStatePaused,
		Activity:       "idle",
		ActivitySource: "hooks",
		UpdatedAt:      now,
	}); err != nil {
		t.Fatalf("seed idle hook session: %v", err)
	}

	d := &Daemon{
		cfg:          Config{RepoDir: ".", Logger: slog.Default()},
		sessionStore: store,
		runtimeStoresByRoot: map[string]*daemonstate.RuntimeStateStore{
			".": runtimeStateStore,
		},
	}

	tasks := []domain.Task{
		{ID: busyID, Title: "busy", Type: domain.TypeTask},
		{ID: idleID, Title: "idle", Type: domain.TypeTask},
	}
	enriched := d.enrichTasksWithSessionState(context.Background(), projectID, tasks)
	if len(enriched) != 2 {
		t.Fatalf("len(enriched) = %d, want 2", len(enriched))
	}
	byID := map[naming.IssueID]domain.Task{}
	for _, task := range enriched {
		byID[task.ID] = task
	}
	if byID[busyID].Session == nil || byID[busyID].Session.Activity != "busy" || byID[busyID].Session.ActivitySource != "hooks" {
		t.Fatalf("busy session = %+v", byID[busyID].Session)
	}
	if byID[idleID].Session == nil || byID[idleID].Session.Activity != "idle" || byID[idleID].Session.ActivitySource != "hooks" {
		t.Fatalf("idle session = %+v", byID[idleID].Session)
	}
}

func TestEnrichTasksWithSessionStatePrefersNewerProjectionStateOverSnapshot(t *testing.T) {
	const (
		projectID = "proj-task-merge"
		issueID   = "bix"
	)

	store := daemonstate.NewStore()
	runtimeStateStore := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(t.TempDir(), "azedarach.db"), slog.Default())
	t.Cleanup(func() {
		_ = runtimeStateStore.Close()
	})

	sessionID := naming.CanonicalSessionID(projectID, issueID)
	snapshotStartedAt := time.Date(2026, time.April, 1, 9, 0, 0, 0, time.UTC)
	if _, err := store.UpsertSession(projectID, sessionID, issueID, daemonstate.SessionStateAttached); err != nil {
		t.Fatalf("seed snapshot session: %v", err)
	}
	snapshot := store.ReadSnapshot(projectID)
	snapshotSession, ok := snapshot.Sessions[sessionID]
	if !ok {
		t.Fatalf("missing snapshot session %q", sessionID)
	}
	snapshotSession.StartedAt = &snapshotStartedAt
	snapshotSession.UpdatedAt = snapshotStartedAt
	store.ReplaceProjectSessions(projectID, []daemonstate.Session{snapshotSession})

	projectionStoppedAt := snapshotStartedAt.Add(1 * time.Minute)
	if err := upsertSessionStateFixture(runtimeStateStore, context.Background(), projectID, daemonstate.Session{
		ID:        sessionID,
		IssueID:   issueID,
		State:     daemonstate.SessionStateStopped,
		UpdatedAt: projectionStoppedAt,
	}); err != nil {
		t.Fatalf("seed projection stopped session: %v", err)
	}

	tmuxRunner := newSessionStartTmuxRunner()
	tmuxRunner.sessions[sessionID] = true

	d := &Daemon{
		cfg:          Config{RepoDir: ".", Logger: slog.Default()},
		tmux:         tmux.NewClient(tmuxRunner, slog.Default()),
		sessionStore: store,
		runtimeStoresByRoot: map[string]*daemonstate.RuntimeStateStore{
			".": runtimeStateStore,
		},
	}

	tasks := []domain.Task{{ID: issueID, Title: "projection should win", Type: domain.TypeTask}}
	enriched := d.enrichTasksWithSessionState(context.Background(), projectID, tasks)
	if len(enriched) != 1 {
		t.Fatalf("len(enriched) = %d, want 1", len(enriched))
	}
	if enriched[0].Session != nil {
		t.Fatalf("expected stopped projection to suppress task session, got %+v", enriched[0].Session)
	}
}

func TestPersistTmuxSessionProjectionSnapshotPreservesCachedStartedAt(t *testing.T) {
	const (
		projectID = "proj-cache-preserve"
		issueID   = "bic"
	)

	store := daemonstate.NewStore()
	runtimeStateStore := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(t.TempDir(), "azedarach.db"), slog.Default())
	t.Cleanup(func() {
		_ = runtimeStateStore.Close()
	})

	sessionID := naming.CanonicalSessionID(projectID, issueID)
	cachedStartedAt := time.Date(2026, time.April, 1, 11, 0, 0, 0, time.UTC)
	if err := upsertSessionStateFixture(runtimeStateStore, context.Background(), projectID, daemonstate.Session{
		ID:        sessionID,
		IssueID:   issueID,
		State:     daemonstate.SessionStateAttached,
		StartedAt: &cachedStartedAt,
		UpdatedAt: cachedStartedAt,
	}); err != nil {
		t.Fatalf("seed projection session: %v", err)
	}

	d := &Daemon{
		cfg:          Config{RepoDir: ".", Logger: slog.Default()},
		sessionStore: store,
		runtimeStoresByRoot: map[string]*daemonstate.RuntimeStateStore{
			".": runtimeStateStore,
		},
	}

	if err := d.persistTmuxSessionRuntimeState(context.Background(), projectID, []tmux.SessionInfo{{Name: sessionID}}, nil); err != nil {
		t.Fatalf("persist tmux session runtime state: %v", err)
	}

	sessions, err := runtimeStateStore.ListSessionStates(context.Background(), projectID)
	if err != nil {
		t.Fatalf("list projection sessions: %v", err)
	}
	if len(sessions) != 1 {
		t.Fatalf("len(sessions) = %d, want 1", len(sessions))
	}
	if sessions[0].StartedAt == nil || !sessions[0].StartedAt.Equal(cachedStartedAt) {
		t.Fatalf("started_at = %v, want %v", sessions[0].StartedAt, cachedStartedAt)
	}
}

func TestPersistTmuxSessionRuntimeStateSeedsStartedAtFromTmuxCreatedAt(t *testing.T) {
	const (
		projectID = "proj-created-at"
		issueID   = "bid"
	)

	store := daemonstate.NewStore()
	runtimeStateStore := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(t.TempDir(), "azedarach.db"), slog.Default())
	t.Cleanup(func() {
		_ = runtimeStateStore.Close()
	})

	sessionID := naming.CanonicalSessionID(projectID, issueID)
	createdAt := time.Date(2026, time.April, 3, 19, 0, 0, 0, time.UTC)

	d := &Daemon{
		cfg:          Config{RepoDir: ".", Logger: slog.Default()},
		sessionStore: store,
		runtimeStoresByRoot: map[string]*daemonstate.RuntimeStateStore{
			".": runtimeStateStore,
		},
	}

	if err := d.persistTmuxSessionRuntimeState(context.Background(), projectID, []tmux.SessionInfo{{
		Name:      sessionID,
		CreatedAt: &createdAt,
	}}, nil); err != nil {
		t.Fatalf("persist tmux session runtime state: %v", err)
	}

	sessions, err := runtimeStateStore.ListSessionStates(context.Background(), projectID)
	if err != nil {
		t.Fatalf("list projection sessions: %v", err)
	}
	if len(sessions) != 1 {
		t.Fatalf("len(sessions) = %d, want 1", len(sessions))
	}
	if sessions[0].StartedAt == nil || !sessions[0].StartedAt.Equal(createdAt) {
		t.Fatalf("started_at = %v, want %v", sessions[0].StartedAt, createdAt)
	}
}

func TestPersistTmuxSessionRuntimeStateKeepsAgentHookActivityWhenPaneLives(t *testing.T) {
	const (
		projectID = "proj-hook-parent-live"
		issueID   = "bih"
	)

	store := daemonstate.NewStore()
	runtimeStateStore := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(t.TempDir(), "azedarach.db"), slog.Default())
	t.Cleanup(func() {
		_ = runtimeStateStore.Close()
	})

	sessionID := naming.CanonicalSessionID(projectID, issueID)
	agentSessionID := sessionID + ".pane-2021"
	now := time.Date(2026, time.April, 3, 20, 0, 0, 0, time.UTC)
	if err := upsertSessionStateFixture(runtimeStateStore, context.Background(), projectID, daemonstate.Session{
		ID:            sessionID,
		IssueID:       issueID,
		State:         daemonstate.SessionStateRunning,
		ObservedState: daemonstate.SessionStateRunning,
		UpdatedAt:     now,
	}); err != nil {
		t.Fatalf("seed parent session: %v", err)
	}
	if err := upsertSessionStateFixture(runtimeStateStore, context.Background(), projectID, daemonstate.Session{
		ID:             agentSessionID,
		IssueID:        issueID,
		State:          daemonstate.SessionStateRunning,
		ObservedState:  daemonstate.SessionStateRunning,
		Activity:       "busy",
		ActivitySource: "hooks",
		UpdatedAt:      now,
	}); err != nil {
		t.Fatalf("seed agent session: %v", err)
	}

	d := &Daemon{
		cfg:          Config{RepoDir: ".", Logger: slog.Default()},
		sessionStore: store,
		runtimeStoresByRoot: map[string]*daemonstate.RuntimeStateStore{
			".": runtimeStateStore,
		},
	}

	if err := d.persistTmuxSessionRuntimeState(context.Background(), projectID, []tmux.SessionInfo{{Name: sessionID}}, []tmux.PaneInfo{{SessionName: sessionID, PaneID: "2021"}}); err != nil {
		t.Fatalf("persist tmux session runtime state: %v", err)
	}

	sessions, err := runtimeStateStore.ListSessionStates(context.Background(), projectID)
	if err != nil {
		t.Fatalf("list projection sessions: %v", err)
	}
	byID := make(map[string]daemonstate.Session, len(sessions))
	for _, session := range sessions {
		byID[session.ID] = session
	}
	agentSession, ok := byID[agentSessionID]
	if !ok {
		t.Fatalf("missing agent session %q", agentSessionID)
	}
	if agentSession.ObservedState != daemonstate.SessionStateRunning {
		t.Fatalf("agent observed state = %s, want %s", agentSession.ObservedState, daemonstate.SessionStateRunning)
	}
	activity := sessionHookActivityByIssueKeyFromSessions(sessions, d.sessionNamingScope(projectID))[sessionKey(issueID)]
	if got, _ := sessionActivityLabel(activity); got != "busy" {
		t.Fatalf("activity = %s from %+v, want busy", got, activity)
	}
}

func TestSessionActivityLabelForDisplayUsesStartupGraceOnlyForFreshUnknownActivity(t *testing.T) {
	now := time.Date(2026, time.June, 17, 8, 0, 0, 0, time.UTC)
	previousNow := timeNow
	timeNow = func() time.Time { return now }
	t.Cleanup(func() { timeNow = previousNow })

	freshStart := now.Add(-10 * time.Second)
	label, source := sessionActivityLabelForDisplay(sessionHookActivity{}, daemonstate.Session{
		ID:        "session-fresh",
		IssueID:   "bif",
		State:     daemonstate.SessionStateStarting,
		StartedAt: &freshStart,
	})
	if label != "starting" || source != "startup-grace" {
		t.Fatalf("fresh unknown activity = %s/%s, want starting/startup-grace", label, source)
	}

	oldStart := now.Add(-2 * time.Minute)
	label, source = sessionActivityLabelForDisplay(sessionHookActivity{}, daemonstate.Session{
		ID:        "session-old",
		IssueID:   "big",
		State:     daemonstate.SessionStateRunning,
		StartedAt: &oldStart,
	})
	if label != "unknown" || source != "none" {
		t.Fatalf("old unknown activity = %s/%s, want unknown/none", label, source)
	}

	label, source = sessionActivityLabelForDisplay(sessionHookActivity{Total: 1, Active: 1}, daemonstate.Session{
		ID:        "session-hook",
		IssueID:   "bih",
		State:     daemonstate.SessionStateRunning,
		StartedAt: &freshStart,
	})
	if label != "busy" || source != "hooks" {
		t.Fatalf("hook-backed activity = %s/%s, want busy/hooks", label, source)
	}
}

func TestEnrichTasksWithSessionStateDoesNotTreatLaunchBusyAsHookActivity(t *testing.T) {
	ctx := context.Background()
	const (
		projectID = "proj-launch-activity"
		issueID   = "bii"
	)

	now := time.Date(2026, time.June, 17, 8, 0, 0, 0, time.UTC)
	previousNow := timeNow
	timeNow = func() time.Time { return now }
	t.Cleanup(func() { timeNow = previousNow })

	oldStart := now.Add(-2 * time.Minute)
	sessionID := naming.CanonicalSessionID(projectID, issueID)
	runtimeStateStore := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(t.TempDir(), "runtime.db"), slog.Default())
	t.Cleanup(func() { _ = runtimeStateStore.Close() })
	if err := upsertSessionStateFixture(runtimeStateStore, ctx, projectID, daemonstate.Session{
		ID:             sessionID,
		IssueID:        issueID,
		State:          daemonstate.SessionStateRunning,
		ObservedState:  daemonstate.SessionStateRunning,
		Activity:       "busy",
		ActivitySource: "session",
		StartedAt:      &oldStart,
		UpdatedAt:      oldStart,
	}); err != nil {
		t.Fatalf("seed runtime state: %v", err)
	}

	d := &Daemon{
		cfg:          Config{RepoDir: ".", Logger: slog.Default()},
		sessionStore: daemonstate.NewStore(),
		runtimeStoresByProject: map[string]*daemonstate.RuntimeStateStore{
			projectID: runtimeStateStore,
		},
	}

	tasks := d.enrichTasksWithSessionState(ctx, projectID, []domain.Task{{
		ID:     naming.IssueID(issueID),
		Title:  "Launch-only worker",
		Status: domain.StatusInProgress,
	}})
	if len(tasks) != 1 || tasks[0].Session == nil {
		t.Fatalf("tasks = %+v, want enriched session", tasks)
	}
	if tasks[0].Session.Activity != "unknown" || tasks[0].Session.ActivitySource != "none" {
		t.Fatalf("session activity = %s/%s, want unknown/none", tasks[0].Session.Activity, tasks[0].Session.ActivitySource)
	}
}

func TestEnrichTasksWithSessionStateClearsStalePaneRuntimeBehindStoppedIntent(t *testing.T) {
	ctx := context.Background()
	const (
		projectID = "proj-stopped-pane"
		issueID   = "closed-ticket"
	)

	now := time.Date(2026, time.July, 17, 0, 18, 45, 0, time.UTC)
	sessionID := naming.CanonicalSessionID(projectID, issueID)
	runtimeStateStore := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(t.TempDir(), "runtime.db"), slog.Default())
	t.Cleanup(func() { _ = runtimeStateStore.Close() })
	for _, session := range []daemonstate.Session{
		{
			ID: sessionID, IssueID: issueID,
			Role: daemonstate.SessionRoleWorker, ScopeKind: daemonstate.SessionScopeIssue, ScopeID: issueID,
			State: daemonstate.SessionStateStopped, ObservedState: daemonstate.SessionStateStopped,
			UpdatedAt: now,
		},
		{
			ID: sessionID + ".pane-12", IssueID: issueID,
			Role: daemonstate.SessionRoleWorker, ScopeKind: daemonstate.SessionScopeIssue, ScopeID: issueID,
			State: daemonstate.SessionStatePaused, ObservedState: daemonstate.SessionStatePaused,
			Activity: "idle", ActivitySource: "hooks", UpdatedAt: now.Add(-time.Second),
		},
	} {
		if err := upsertSessionStateFixture(runtimeStateStore, ctx, projectID, session); err != nil {
			t.Fatalf("seed runtime state %+v: %v", session, err)
		}
	}

	sessionStore := daemonstate.NewStore()
	if _, err := sessionStore.UpsertSession(projectID, sessionID, issueID, daemonstate.SessionStateRunning); err != nil {
		t.Fatalf("seed contradictory exact live session: %v", err)
	}
	d := &Daemon{
		cfg:          Config{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))},
		sessionStore: sessionStore,
		runtimeStoresByProject: map[string]*daemonstate.RuntimeStateStore{
			projectID: runtimeStateStore,
		},
	}
	staleStartedAt := now.Add(-time.Hour)
	tasks := d.enrichTasksWithSessionState(ctx, projectID, []domain.Task{
		{
			ID:     naming.IssueID(issueID),
			Title:  "Already closed",
			Status: domain.StatusDone,
			Session: &domain.Session{
				IssueID: naming.IssueID(issueID), State: domain.SessionPaused,
				Activity: "idle", ActivitySource: "hooks", StartedAt: &staleStartedAt, UpdatedAt: now.Add(-time.Second),
			},
			HasTmuxSession: true,
		},
		{
			ID: naming.IssueID("projection-gap"), Title: "Projection temporarily unavailable", Status: domain.StatusInProgress,
			Session:        &domain.Session{IssueID: naming.IssueID("projection-gap"), State: domain.SessionBusy, Activity: "busy", ActivitySource: "hooks", UpdatedAt: now},
			HasTmuxSession: true,
		},
	})
	if len(tasks) != 2 {
		t.Fatalf("tasks = %+v, want two tasks", tasks)
	}
	if tasks[0].Session != nil || tasks[0].HasTmuxSession {
		t.Fatalf("stopped logical intent retained stale pane runtime: session=%+v has_tmux_session=%t", tasks[0].Session, tasks[0].HasTmuxSession)
	}
	if tasks[1].Session == nil || !tasks[1].HasTmuxSession {
		t.Fatalf("missing runtime authority erased pre-hydrated session: %+v", tasks[1])
	}
}

func TestEnrichTasksWithSessionStateKeepsNoAgentActivity(t *testing.T) {
	ctx := context.Background()
	const (
		projectID = "proj-no-agent-display"
		issueID   = "bij"
	)

	startedAt := time.Date(2026, time.June, 17, 8, 0, 0, 0, time.UTC)
	sessionID := naming.CanonicalSessionID(projectID, issueID)
	runtimeStateStore := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(t.TempDir(), "runtime.db"), slog.Default())
	t.Cleanup(func() { _ = runtimeStateStore.Close() })
	if err := upsertSessionStateFixture(runtimeStateStore, ctx, projectID, daemonstate.Session{
		ID:             sessionID,
		IssueID:        issueID,
		State:          daemonstate.SessionStateRunning,
		ObservedState:  daemonstate.SessionStateRunning,
		Activity:       "no-agent",
		ActivitySource: "session",
		StartedAt:      &startedAt,
		UpdatedAt:      startedAt,
	}); err != nil {
		t.Fatalf("seed runtime state: %v", err)
	}

	d := &Daemon{
		cfg:          Config{RepoDir: ".", Logger: slog.Default()},
		sessionStore: daemonstate.NewStore(),
		runtimeStoresByProject: map[string]*daemonstate.RuntimeStateStore{
			projectID: runtimeStateStore,
		},
	}

	tasks := d.enrichTasksWithSessionState(ctx, projectID, []domain.Task{{
		ID:     naming.IssueID(issueID),
		Title:  "Shell session",
		Status: domain.StatusInProgress,
	}})
	if len(tasks) != 1 || tasks[0].Session == nil {
		t.Fatalf("tasks = %+v, want enriched session", tasks)
	}
	if tasks[0].Session.Activity != "no-agent" || tasks[0].Session.ActivitySource != "session" {
		t.Fatalf("session activity = %s/%s, want no-agent/session", tasks[0].Session.Activity, tasks[0].Session.ActivitySource)
	}
}

func TestEnrichTasksWithSessionStateProjectsInvestigationHumanAuthority(t *testing.T) {
	ctx := context.Background()
	repoDir := t.TempDir()
	const projectID = "investigation-authority"
	client := issues.NewClient(repoDir, slog.Default())
	t.Cleanup(func() { _ = client.CloseDB() })
	secondClient := issues.NewClient(repoDir, slog.Default())
	t.Cleanup(func() { _ = secondClient.CloseDB() })

	create := func(title string) string {
		id, err := client.Create(ctx, issues.CreateTaskParams{Title: title, Type: domain.TypeInvestigation, Status: domain.StatusInReview})
		if err != nil {
			t.Fatalf("create %s: %v", title, err)
		}
		return id
	}
	unacceptedHuman := create("Human findings")
	acceptedHuman := create("Accepted human findings")
	acceptedInternal := create("Accepted internal review")
	// Write through a second store instance to prove enrichment refreshes durable
	// evidence instead of reusing process-local acceptance state.
	if _, err := secondClient.AppendIssueObservationEvent(ctx, acceptedHuman, issues.IssueObservationEventParams{Type: domain.IssueEventHumanInputProvided, Source: "human", Payload: map[string]any{"investigation_findings_accepted": true}}); err != nil {
		t.Fatal(err)
	}
	if _, err := client.AppendIssueObservationEvent(ctx, acceptedInternal, issues.IssueObservationEventParams{Type: domain.IssueEventInvestigationDisposition, Source: "daemon-orchestration", Payload: map[string]any{"disposition": "internal_review"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := client.AppendIssueObservationEvent(ctx, acceptedInternal, issues.IssueObservationEventParams{Type: domain.IssueEventReviewCompleted, Source: "daemon-orchestration", SourceCommand: "review-accept", Payload: map[string]any{"actor_id": "reviewer", "outcome": "accepted"}}); err != nil {
		t.Fatal(err)
	}
	tasks, err := client.ListWithRuntime(ctx, projectID)
	if err != nil {
		t.Fatal(err)
	}
	d := &Daemon{cfg: Config{RepoDir: repoDir, Logger: slog.Default()}, issues: client, issueClientsByProject: map[string]*issues.Client{projectID: client}}
	tasks = d.enrichTasksWithSessionState(ctx, projectID, tasks)
	byID := make(map[string]domain.Task, len(tasks))
	for _, task := range tasks {
		byID[task.ID.String()] = task
	}
	if facts := byID[unacceptedHuman].IssueFacts(); !facts.WaitingHuman || facts.WaitingHumanSource != domain.WaitingHumanSourceInvestigationAcceptance {
		t.Fatalf("unaccepted human findings facts = %+v", facts)
	}
	for _, id := range []string{acceptedHuman, acceptedInternal} {
		if facts := byID[id].IssueFacts(); facts.WaitingHuman {
			t.Fatalf("accepted investigation %s retained human authority: %+v", id, facts)
		}
	}
}

func TestPersistTmuxSessionRuntimeStatePrefersTmuxCreatedAtOverSnapshotStartedAt(t *testing.T) {
	const (
		projectID = "proj-created-at-priority"
		issueID   = "bie"
	)

	store := daemonstate.NewStore()
	sessionID := naming.CanonicalSessionID(projectID, issueID)
	if _, err := store.UpsertSession(projectID, sessionID, issueID, daemonstate.SessionStateStarting); err != nil {
		t.Fatalf("seed starting session: %v", err)
	}
	if _, err := store.UpsertSession(projectID, sessionID, issueID, daemonstate.SessionStateAttached); err != nil {
		t.Fatalf("seed attached session: %v", err)
	}

	runtimeStateStore := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(t.TempDir(), "azedarach.db"), slog.Default())
	t.Cleanup(func() {
		_ = runtimeStateStore.Close()
	})

	createdAt := time.Date(2026, time.April, 1, 9, 0, 0, 0, time.UTC)
	d := &Daemon{
		cfg:          Config{RepoDir: ".", Logger: slog.Default()},
		sessionStore: store,
		runtimeStoresByRoot: map[string]*daemonstate.RuntimeStateStore{
			".": runtimeStateStore,
		},
	}
	if err := d.persistTmuxSessionRuntimeState(context.Background(), projectID, []tmux.SessionInfo{{
		Name:      sessionID,
		CreatedAt: &createdAt,
	}}, nil); err != nil {
		t.Fatalf("persist tmux session runtime state: %v", err)
	}

	sessions, err := runtimeStateStore.ListSessionStates(context.Background(), projectID)
	if err != nil {
		t.Fatalf("list projection sessions: %v", err)
	}
	if len(sessions) != 1 {
		t.Fatalf("len(sessions) = %d, want 1", len(sessions))
	}
	if sessions[0].StartedAt == nil || !sessions[0].StartedAt.Equal(createdAt) {
		t.Fatalf("started_at = %v, want %v", sessions[0].StartedAt, createdAt)
	}
}

func TestEnrichTasksWithSessionStateKeepsEarlierStartedAtAcrossSnapshotAndProjection(t *testing.T) {
	const (
		projectID = "proj-started-at-merge"
		issueID   = "bif"
	)

	store := daemonstate.NewStore()
	sessionID := naming.CanonicalSessionID(projectID, issueID)
	if _, err := store.UpsertSession(projectID, sessionID, issueID, daemonstate.SessionStateStarting); err != nil {
		t.Fatalf("seed starting session: %v", err)
	}
	if _, err := store.UpsertSession(projectID, sessionID, issueID, daemonstate.SessionStateAttached); err != nil {
		t.Fatalf("seed attached session: %v", err)
	}
	if _, err := store.UpsertSession(projectID, sessionID, issueID, daemonstate.SessionStatePaused); err != nil {
		t.Fatalf("seed paused session: %v", err)
	}
	snapshot := store.ReadSnapshot(projectID)
	snapshotSession, ok := snapshot.Sessions[sessionID]
	if !ok {
		t.Fatalf("missing snapshot session %q", sessionID)
	}
	snapshotStartedAt := time.Date(2026, time.April, 1, 9, 0, 0, 0, time.UTC)
	snapshotSession.StartedAt = &snapshotStartedAt
	snapshotSession.UpdatedAt = snapshotStartedAt
	store.ReplaceProjectSessions(projectID, []daemonstate.Session{snapshotSession})

	runtimeStateStore := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(t.TempDir(), "azedarach.db"), slog.Default())
	t.Cleanup(func() {
		_ = runtimeStateStore.Close()
	})

	startedAt := time.Date(2026, time.April, 1, 10, 0, 0, 0, time.UTC)
	if err := upsertSessionStateFixture(runtimeStateStore, context.Background(), projectID, daemonstate.Session{
		ID:        sessionID,
		IssueID:   issueID,
		State:     daemonstate.SessionStateAttached,
		StartedAt: &startedAt,
		UpdatedAt: startedAt,
	}); err != nil {
		t.Fatalf("seed projection session: %v", err)
	}

	tmuxRunner := newSessionStartTmuxRunner()
	tmuxRunner.sessions[sessionID] = true

	d := &Daemon{
		cfg:          Config{RepoDir: ".", Logger: slog.Default()},
		tmux:         tmux.NewClient(tmuxRunner, slog.Default()),
		sessionStore: store,
		runtimeStoresByRoot: map[string]*daemonstate.RuntimeStateStore{
			".": runtimeStateStore,
		},
	}

	tasks := []domain.Task{{ID: issueID, Title: "projection started_at precedence", Type: domain.TypeTask}}
	enriched := d.enrichTasksWithSessionState(context.Background(), projectID, tasks)
	if len(enriched) != 1 || enriched[0].Session == nil || enriched[0].Session.StartedAt == nil {
		t.Fatalf("missing session started_at in enriched task: %+v", enriched)
	}
	if enriched[0].Session.State != domain.SessionBusy {
		t.Fatalf("session state = %v, want %v", enriched[0].Session.State, domain.SessionBusy)
	}
	if !enriched[0].Session.StartedAt.Equal(snapshotStartedAt) {
		t.Fatalf("started_at = %v, want %v", enriched[0].Session.StartedAt, snapshotStartedAt)
	}
}

func TestEnrichTasksWithSessionStateIgnoresStoppedAgentScopedRows(t *testing.T) {
	const (
		projectID = "proj-multi-agent"
		issueID   = "ciw"
	)

	store := daemonstate.NewStore()
	runtimeStateStore := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(t.TempDir(), "azedarach.db"), slog.Default())
	t.Cleanup(func() {
		_ = runtimeStateStore.Close()
	})
	d := &Daemon{
		cfg:          Config{RepoDir: ".", Logger: slog.Default()},
		sessionStore: store,
		runtimeStoresByRoot: map[string]*daemonstate.RuntimeStateStore{
			".": runtimeStateStore,
		},
	}

	startedAt := time.Date(2026, time.May, 25, 8, 0, 0, 0, time.UTC)
	secondStartedAt := startedAt.Add(time.Minute)
	if err := upsertSessionStateFixture(runtimeStateStore, context.Background(), projectID, daemonstate.Session{
		ID:            "ciw.pane-1",
		IssueID:       issueID,
		State:         daemonstate.SessionStatePaused,
		ObservedState: daemonstate.SessionStateStopped,
		StartedAt:     &startedAt,
		UpdatedAt:     startedAt,
	}); err != nil {
		t.Fatalf("seed paused agent session: %v", err)
	}
	if err := upsertSessionStateFixture(runtimeStateStore, context.Background(), projectID, daemonstate.Session{
		ID:                "ciw.pane-2",
		IssueID:           issueID,
		State:             daemonstate.SessionStateAttached,
		ObservedState:     daemonstate.SessionStateStopped,
		TmuxAttachedCount: 0,
		StartedAt:         &secondStartedAt,
		UpdatedAt:         secondStartedAt,
	}); err != nil {
		t.Fatalf("seed attached agent session: %v", err)
	}

	tasks := []domain.Task{{ID: issueID, Title: "multiple codex sessions", Type: domain.TypeTask}}
	enriched := d.enrichTasksWithSessionState(context.Background(), projectID, tasks)
	if len(enriched) != 1 {
		t.Fatalf("enriched task count = %d, want 1", len(enriched))
	}
	if enriched[0].Session != nil {
		t.Fatalf("session = %+v, want nil because stopped pane rows are not active lifecycle sessions", enriched[0].Session)
	}
}

func TestEnrichTasksWithSessionStateCountsLivePaneRowsWithoutParentProjection(t *testing.T) {
	const (
		projectID = "proj-agent-pane-only"
		issueID   = "cix"
	)

	runtimeStateStore := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(t.TempDir(), "azedarach.db"), slog.Default())
	t.Cleanup(func() {
		_ = runtimeStateStore.Close()
	})
	d := &Daemon{
		cfg:          Config{RepoDir: ".", Logger: slog.Default()},
		sessionStore: daemonstate.NewStore(),
		runtimeStoresByRoot: map[string]*daemonstate.RuntimeStateStore{
			".": runtimeStateStore,
		},
	}

	parentSessionID := naming.CanonicalSessionID(projectID, issueID)
	now := time.Date(2026, time.May, 25, 9, 0, 0, 0, time.UTC)
	if err := upsertSessionStateFixture(runtimeStateStore, context.Background(), projectID, daemonstate.Session{
		ID:             parentSessionID + ".pane-1",
		IssueID:        issueID,
		State:          daemonstate.SessionStateRunning,
		ObservedState:  daemonstate.SessionStateRunning,
		Activity:       "busy",
		ActivitySource: "hooks",
		StartedAt:      &now,
		UpdatedAt:      now,
	}); err != nil {
		t.Fatalf("seed live pane session: %v", err)
	}

	tasks := []domain.Task{{ID: issueID, Title: "single live codex pane", Type: domain.TypeTask}}
	enriched := d.enrichTasksWithSessionState(context.Background(), projectID, tasks)
	if len(enriched) != 1 || enriched[0].Session == nil {
		t.Fatalf("missing session in enriched task: %+v", enriched)
	}
	if enriched[0].Session.State != domain.SessionBusy {
		t.Fatalf("session state = %v, want %v", enriched[0].Session.State, domain.SessionBusy)
	}
	if enriched[0].Session.TotalCount != 1 || enriched[0].Session.ActiveCount != 1 || enriched[0].Session.PausedCount != 0 {
		t.Fatalf("session aggregate counts = total %d active %d paused %d, want 1/1/0", enriched[0].Session.TotalCount, enriched[0].Session.ActiveCount, enriched[0].Session.PausedCount)
	}
}

func TestEnrichTasksWithSessionStateUsesPaneActivityOverParentContainer(t *testing.T) {
	const (
		projectID = "proj-agent-container"
		issueID   = "cjz"
	)

	runtimeStateStore := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(t.TempDir(), "azedarach.db"), slog.Default())
	t.Cleanup(func() {
		_ = runtimeStateStore.Close()
	})
	d := &Daemon{
		cfg:          Config{RepoDir: ".", Logger: slog.Default()},
		sessionStore: daemonstate.NewStore(),
		runtimeStoresByRoot: map[string]*daemonstate.RuntimeStateStore{
			".": runtimeStateStore,
		},
	}

	startedAt := time.Date(2026, time.May, 28, 8, 0, 0, 0, time.UTC)
	containerSessionID := naming.CanonicalSessionID(projectID, issueID)
	if err := upsertSessionStateFixture(runtimeStateStore, context.Background(), projectID, daemonstate.Session{
		ID:            containerSessionID,
		IssueID:       issueID,
		State:         daemonstate.SessionStateAttached,
		ObservedState: daemonstate.SessionStateAttached,
		StartedAt:     &startedAt,
		UpdatedAt:     startedAt,
	}); err != nil {
		t.Fatalf("seed tmux container session: %v", err)
	}
	if err := upsertSessionStateFixture(runtimeStateStore, context.Background(), projectID, daemonstate.Session{
		ID:            containerSessionID + ".pane-190",
		IssueID:       issueID,
		State:         daemonstate.SessionStatePaused,
		ObservedState: daemonstate.SessionStatePaused,
		StartedAt:     &startedAt,
		UpdatedAt:     startedAt.Add(time.Minute),
	}); err != nil {
		t.Fatalf("seed paused agent session: %v", err)
	}

	tasks := []domain.Task{{ID: issueID, Title: "codex hook paused", Type: domain.TypeTask}}
	enriched := d.enrichTasksWithSessionState(context.Background(), projectID, tasks)
	if len(enriched) != 1 || enriched[0].Session == nil {
		t.Fatalf("missing session in enriched task: %+v", enriched)
	}
	if enriched[0].Session.State != domain.SessionPaused {
		t.Fatalf("session state = %v, want %v", enriched[0].Session.State, domain.SessionPaused)
	}
	if enriched[0].Session.TotalCount != 1 || enriched[0].Session.ActiveCount != 0 || enriched[0].Session.PausedCount != 1 {
		t.Fatalf("session aggregate counts = total %d active %d paused %d, want 1/0/1", enriched[0].Session.TotalCount, enriched[0].Session.ActiveCount, enriched[0].Session.PausedCount)
	}
}

func TestSessionInitAndCreateRollbackCleanCaches(t *testing.T) {
	for _, test := range []struct {
		name string
		run  func(*Daemon, context.Context, string, *git.WorktreeManager, string, string) string
	}{
		{
			name: "init rollback",
			run: func(d *Daemon, ctx context.Context, projectID string, manager *git.WorktreeManager, issueID, path string) string {
				return d.cleanupNewWorktreeAfterInitFailure(ctx, projectID, manager, issueID, path, false)
			},
		},
		{
			name: "create rollback",
			run: func(d *Daemon, ctx context.Context, projectID string, manager *git.WorktreeManager, issueID, path string) string {
				return d.cleanupWorktreeAfterCreateFailure(ctx, projectID, manager, issueID, path)
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			repoDir := t.TempDir()
			issueID := "az-rollback"
			worktreePath := filepath.Join(repoDir, "wt-"+issueID)
			runner := &recordingWorktreeCreateRunner{
				repoDir: repoDir,
				worktree: map[string]git.Worktree{
					issueID: {IssueID: issueID, Path: worktreePath, Branch: "tester/" + issueID + "/rollback"},
				},
			}
			manager := git.NewWorktreeManager(runner, repoDir, slog.Default())
			cacheRoot := filepath.Join(repoDir, ".azedarach", "go")
			for _, kind := range []string{"normal", "race", "coverage"} {
				entry := filepath.Join(cacheRoot, "caches", "v1", kind, "issue-"+issueID, "entry")
				if err := os.MkdirAll(filepath.Dir(entry), 0o755); err != nil {
					t.Fatalf("seed %s cache: %v", kind, err)
				}
				if err := os.WriteFile(entry, []byte("cache"), 0o644); err != nil {
					t.Fatalf("write %s cache: %v", kind, err)
				}
			}
			d := &Daemon{cfg: Config{RepoDir: repoDir, Logger: slog.Default()}}

			note := test.run(d, ctx, protocol.DefaultProjectID, manager, issueID, worktreePath)
			if !strings.Contains(note, "worktree") {
				t.Fatalf("rollback note = %q", note)
			}
			for _, kind := range []string{"normal", "race", "coverage"} {
				_, err := os.Stat(filepath.Join(cacheRoot, "caches", "v1", kind, "issue-"+issueID))
				if !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("%s cache remains after rollback: %v", kind, err)
				}
			}
		})
	}
}
