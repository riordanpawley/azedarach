package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	daemonops "github.com/riordanpawley/azedarach/internal/daemon/operations"
	daemonstate "github.com/riordanpawley/azedarach/internal/daemon/state"
	"github.com/riordanpawley/azedarach/internal/services/tmux"
)

type exactRestartRunner struct {
	mu               sync.Mutex
	store            *daemonstate.RuntimeStateStore
	project, session string
	pid              int
	respawns         int
	respawnDelay     time.Duration
	respawnErr       error
	launchBody       string
	respawnArgs      []string
	extraPanes       string
	blockListPanes   bool
}

func (r *exactRestartRunner) Run(ctx context.Context, args ...string) (string, error) {
	if len(args) == 0 {
		return "", nil
	}
	switch args[0] {
	case "list-panes":
		if r.blockListPanes {
			<-ctx.Done()
			return "", ctx.Err()
		}
		r.mu.Lock()
		defer r.mu.Unlock()
		return r.session + "\t%12\t" + fmt.Sprint(r.pid) + r.extraPanes, nil
	case "respawn-pane":
		if r.respawnDelay > 0 {
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			case <-time.After(r.respawnDelay):
			}
		}
		if r.respawnErr != nil {
			return "", r.respawnErr
		}
		r.mu.Lock()
		defer r.mu.Unlock()
		r.respawns++
		r.respawnArgs = append([]string(nil), args...)
		r.pid++
		command := args[len(args)-1]
		for _, matches := range regexp.MustCompile(`'([^']+)'`).FindAllStringSubmatch(command, -1) {
			if len(matches) == 2 {
				if body, err := os.ReadFile(matches[1]); err == nil {
					r.launchBody = string(body)
					inc := regexp.MustCompile(`AZEDARACH_AGENT_INCARNATION='([^']+)'`).FindStringSubmatch(string(body))
					if len(inc) == 2 {
						_ = r.store.UpsertManagedAgentIdentity(ctx, daemonstate.ManagedAgentIdentity{ProjectID: r.project, SessionID: r.session, LogicalPaneID: "agent", TmuxPaneID: "12", PanePID: r.pid, AgentIncarnation: inc[1], ObservedAt: time.Now().Add(time.Second)})
						break
					}
				}
			}
		}
	}
	return "", nil
}

func (r *exactRestartRunner) snapshot() (int, string, []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.respawns, r.launchBody, append([]string(nil), r.respawnArgs...)
}

func TestRestartManagedAgentPaneRequiresForceAndAcknowledgesReplacement(t *testing.T) {
	ctx := context.Background()
	project, session := "project", "az-1"
	store := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(t.TempDir(), "runtime.db"), slog.Default())
	t.Cleanup(func() { _ = store.Close() })
	old := daemonstate.ManagedAgentIdentity{ProjectID: project, SessionID: session, LogicalPaneID: "agent", TmuxPaneID: "12", PanePID: 100, AgentIncarnation: "old", ObservedAt: time.Now()}
	if err := store.UpsertManagedAgentIdentity(ctx, old); err != nil {
		t.Fatal(err)
	}
	runner := &exactRestartRunner{store: store, project: project, session: session, pid: 100}
	d := &Daemon{cfg: Config{RepoDir: t.TempDir(), CLITool: "codex", SessionShell: "zsh", Logger: slog.Default()}, tmux: tmux.NewClient(runner, slog.Default()), runtimeStoresByProject: map[string]*daemonstate.RuntimeStateStore{project: store}}
	target := sessionRestartAllTarget{ProjectID: project, SessionID: session, IssueID: "one", Activity: "busy", TmuxReady: true, ActiveIntent: true}
	refused := d.restartManagedAgentPane(ctx, target, protocol.SessionRestartAllRequestBody{}, protocol.SessionRestartAllItem{})
	if refused.Outcome != "busy" || !refused.Skipped || runner.respawns != 0 {
		t.Fatalf("refused=%+v respawns=%d", refused, runner.respawns)
	}
	restarted := d.restartManagedAgentPane(ctx, target, protocol.SessionRestartAllRequestBody{ForceBusy: true}, protocol.SessionRestartAllItem{})
	if !restarted.Restarted || restarted.Outcome != "busy_forced" || restarted.OldIdentity.PanePID == restarted.NewIdentity.PanePID || runner.respawns != 1 {
		t.Fatalf("restarted=%+v respawns=%d", restarted, runner.respawns)
	}
	if got := strings.Join([]string{restarted.Stages[0].Name, restarted.Stages[len(restarted.Stages)-1].Name}, ","); got != "preflight,publish" {
		t.Fatalf("stage bounds=%s", got)
	}
}

func newExactRestartDaemon(t *testing.T, project, session, issue, activity string) (*Daemon, *daemonstate.RuntimeStateStore, *exactRestartRunner, sessionRestartAllTarget) {
	t.Helper()
	store := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(t.TempDir(), "runtime.db"), slog.Default())
	t.Cleanup(func() { _ = store.Close() })
	old := daemonstate.ManagedAgentIdentity{ProjectID: project, SessionID: session, LogicalPaneID: "agent", TmuxPaneID: "12", PanePID: 100, AgentIncarnation: "old", ObservedAt: time.Now()}
	if err := store.UpsertManagedAgentIdentity(context.Background(), old); err != nil {
		t.Fatal(err)
	}
	runner := &exactRestartRunner{store: store, project: project, session: session, pid: 100}
	d := &Daemon{cfg: Config{RepoDir: t.TempDir(), CLITool: "codex", SessionShell: "zsh", Logger: slog.Default()}, tmux: tmux.NewClient(runner, slog.Default()), runtimeStoresByProject: map[string]*daemonstate.RuntimeStateStore{project: store}, runtimeStoresByRoot: map[string]*daemonstate.RuntimeStateStore{}}
	return d, store, runner, sessionRestartAllTarget{ProjectID: project, SessionID: session, IssueID: issue, Activity: activity, TmuxReady: true, ActiveIntent: activity == "busy"}
}

func TestRestartManagedAgentPaneConcurrentDuplicateRespawnsOnce(t *testing.T) {
	d, _, runner, target := newExactRestartDaemon(t, "project", "az-1", "one", "idle")
	runner.respawnDelay = 75 * time.Millisecond
	start := make(chan struct{})
	results := make(chan protocol.SessionRestartAllItem, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			results <- d.restartManagedAgentPane(context.Background(), target, protocol.SessionRestartAllRequestBody{}, protocol.SessionRestartAllItem{})
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	for result := range results {
		if !result.Restarted {
			t.Fatalf("result=%+v", result)
		}
	}
	respawns, _, _ := runner.snapshot()
	if respawns != 1 {
		t.Fatalf("respawns=%d, want 1", respawns)
	}
}

func TestRestartManagedAgentPaneArtifactFlagsWorktreeAndUnrelatedPanes(t *testing.T) {
	d, store, runner, target := newExactRestartDaemon(t, "project", "az-1", "one", "waiting")
	d.cfg.DangerouslySkipPermissions = true
	runner.extraPanes = "\nother-session\t%99\t999\naz-1\t%13\t113"
	worktree := filepath.Join(t.TempDir(), "issue-worktree")
	if err := store.UpsertWorktreeState(context.Background(), daemonstate.WorktreeState{ProjectID: "project", IssueID: "one", Path: worktree, Branch: "issue/one", UpdatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	result := d.restartManagedAgentPane(context.Background(), target, protocol.SessionRestartAllRequestBody{Yolo: true}, protocol.SessionRestartAllItem{})
	if !result.Restarted || result.Outcome != "waiting" {
		t.Fatalf("result=%+v", result)
	}
	respawns, body, args := runner.snapshot()
	if respawns != 1 || len(args) < 7 || args[3] != "%12" || args[5] != worktree {
		t.Fatalf("respawns=%d args=%v", respawns, args)
	}
	if !strings.Contains(body, "codex resume") || !strings.Contains(body, "--dangerously-bypass-approvals-and-sandbox") || strings.Contains(body, "Continue your prior task") {
		t.Fatalf("launch body=%q", body)
	}
}

func TestRestartManagedAgentPanePartialFailureAndBoundedTimeout(t *testing.T) {
	t.Run("respawn failure", func(t *testing.T) {
		d, _, runner, target := newExactRestartDaemon(t, "project", "az-1", "one", "idle")
		runner.respawnErr = errors.New("respawn failed")
		result := d.restartManagedAgentPane(context.Background(), target, protocol.SessionRestartAllRequestBody{}, protocol.SessionRestartAllItem{})
		if result.Outcome != "partial_failure" || !strings.Contains(result.Error, "respawn failed") || result.Stages[len(result.Stages)-1].Name != "replace" {
			t.Fatalf("result=%+v", result)
		}
	})
	t.Run("preflight timeout", func(t *testing.T) {
		d, _, runner, target := newExactRestartDaemon(t, "project", "az-1", "one", "idle")
		runner.blockListPanes = true
		result := d.restartManagedAgentPane(context.Background(), target, protocol.SessionRestartAllRequestBody{}, protocol.SessionRestartAllItem{})
		stage := result.Stages[len(result.Stages)-1]
		if stage.Status != "timeout" || stage.TimeoutMS != sessionRestartPreflightTimeout.Milliseconds() {
			t.Fatalf("result=%+v", result)
		}
	})
	t.Run("durable stage write fails closed", func(t *testing.T) {
		d, _, runner, target := newExactRestartDaemon(t, "project", "az-1", "one", "idle")
		ctx := daemonops.WithProgressReporter(context.Background(), func(context.Context, daemonops.Progress) error { return errors.New("progress store unavailable") })
		result := d.restartManagedAgentPane(ctx, target, protocol.SessionRestartAllRequestBody{}, protocol.SessionRestartAllItem{})
		respawns, _, _ := runner.snapshot()
		if result.Outcome != "partial_failure" || respawns != 0 || result.Stages[len(result.Stages)-1].Name != "persist_prepare" {
			t.Fatalf("result=%+v respawns=%d", result, respawns)
		}
	})
}

func TestRecoverInterruptedSessionRestartConvergesWithoutRespawn(t *testing.T) {
	d, store, runner, target := newExactRestartDaemon(t, "project", "az-1", "one", "idle")
	plan := sessionRestartRecoveryPlan{ProjectID: target.ProjectID, SessionID: target.SessionID, IssueID: target.IssueID, Activity: target.Activity, Old: daemonstate.ManagedAgentIdentity{ProjectID: "project", SessionID: "az-1", LogicalPaneID: "agent", TmuxPaneID: "12", PanePID: 100, AgentIncarnation: "old"}, PlannedIncarnation: "planned", Stage: "observe"}
	body, _ := json.Marshal(plan)
	record := daemonops.Record{Kind: protocol.CommandSessionRestartAll, Progress: &daemonops.Progress{Phase: "session.restart_all.observe", Message: string(body)}}
	runner.mu.Lock()
	runner.pid = 101
	runner.mu.Unlock()
	if err := store.UpsertManagedAgentIdentity(context.Background(), daemonstate.ManagedAgentIdentity{ProjectID: "project", SessionID: "az-1", LogicalPaneID: "agent", TmuxPaneID: "12", PanePID: 101, AgentIncarnation: "planned", ObservedAt: time.Now().Add(time.Second)}); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		recovery, ok := d.recoverInterruptedSessionRestart(context.Background(), record)
		if !ok || recovery.State != daemonops.StateDone {
			t.Fatalf("recovery=%+v ok=%v", recovery, ok)
		}
		var result protocol.SessionRestartAllResponseBody
		if json.Unmarshal(recovery.ResultPayload, &result) != nil || result.Restarted != 1 {
			t.Fatalf("result=%+v", result)
		}
	}
	respawns, _, _ := runner.snapshot()
	if respawns != 0 {
		t.Fatalf("respawns=%d, want replay convergence without respawn", respawns)
	}
}

func TestRestartStateOutcomeClassification(t *testing.T) {
	for activity, want := range map[string]string{"idle": "idle", "waiting_human": "waiting", "busy": "busy_forced", "unknown": "unknown"} {
		if got := restartSuccessOutcome(activity); got != want {
			t.Errorf("activity %s outcome=%s want=%s", activity, got, want)
		}
	}
}
