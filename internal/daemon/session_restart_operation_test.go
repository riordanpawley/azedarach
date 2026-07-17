package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	daemonops "github.com/riordanpawley/azedarach/internal/daemon/operations"
	daemonstate "github.com/riordanpawley/azedarach/internal/daemon/state"
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/services/tmux"
	"golang.org/x/sys/unix"
)

type exactRestartRunner struct {
	mu               sync.Mutex
	store            *daemonstate.RuntimeStateStore
	project, session string
	pid              int
	respawns         int
	respawnEntered   chan struct{}
	respawnRelease   <-chan struct{}
	respawnErr       error
	launchBody       string
	respawnArgs      []string
	extraPanes       string
	blockListPanes   bool
	paneMissing      bool
	listPanesEntered chan struct{}
	listPanesRelease <-chan struct{}
	environment      map[string]string
}

type realRestartRunner struct {
	tmuxPath           string
	socketName         string
	store              *daemonstate.RuntimeStateStore
	projectID, session string
}

func (r *realRestartRunner) Run(ctx context.Context, args ...string) (string, error) {
	if strings.TrimSpace(r.socketName) == "" {
		return "", errors.New("real restart fixture requires a private tmux socket name")
	}
	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
	}
	var replacementIncarnation string
	if len(args) > 0 && args[0] == "respawn-pane" {
		command := args[len(args)-1]
		scriptMatch := regexp.MustCompile(`-i '([^']+)'`).FindStringSubmatch(command)
		if len(scriptMatch) != 2 {
			return "", fmt.Errorf("restart artifact path missing from %q", command)
		}
		script, readErr := os.ReadFile(scriptMatch[1])
		if readErr != nil {
			return "", readErr
		}
		incarnationMatch := regexp.MustCompile(`AZEDARACH_AGENT_INCARNATION='([^']+)'`).FindSubmatch(script)
		if len(incarnationMatch) != 2 {
			return "", fmt.Errorf("restart incarnation missing from artifact")
		}
		replacementIncarnation = string(incarnationMatch[1])
	}
	outputBytes, err := exec.CommandContext(ctx, r.tmuxPath, append([]string{"-L", r.socketName}, args...)...).CombinedOutput()
	output := string(outputBytes)
	if err != nil || replacementIncarnation == "" {
		return output, err
	}
	metadata, metadataErr := r.Run(ctx, "display-message", "-p", "-t", r.session, "#{pane_id}\t#{pane_pid}")
	if metadataErr != nil {
		return output, metadataErr
	}
	fields := strings.Split(strings.TrimSpace(metadata), "\t")
	if len(fields) != 2 {
		return output, fmt.Errorf("replacement metadata = %q", metadata)
	}
	pid, parseErr := strconv.Atoi(fields[1])
	if parseErr != nil {
		return output, parseErr
	}
	identity := daemonstate.ManagedAgentIdentity{
		ProjectID: r.projectID, SessionID: r.session, LogicalPaneID: "agent",
		TmuxPaneID: strings.TrimPrefix(fields[0], "%"), PanePID: pid,
		AgentIncarnation: replacementIncarnation, ObservedAt: time.Now().UTC(),
	}
	if storeErr := r.store.UpsertManagedAgentIdentity(ctx, identity); storeErr != nil {
		return output, storeErr
	}
	return output, nil
}

func (r *exactRestartRunner) Run(ctx context.Context, args ...string) (string, error) {
	if len(args) == 0 {
		return "", nil
	}
	switch args[0] {
	case "set-environment":
		r.mu.Lock()
		defer r.mu.Unlock()
		if r.environment == nil {
			r.environment = make(map[string]string)
		}
		if len(args) >= 5 {
			r.environment[args[3]] = args[4]
		}
		return "", nil
	case "show-environment":
		r.mu.Lock()
		defer r.mu.Unlock()
		var lines []string
		for key, value := range r.environment {
			lines = append(lines, key+"="+value)
		}
		return strings.Join(lines, "\n"), nil
	case "list-panes":
		if r.listPanesEntered != nil {
			select {
			case r.listPanesEntered <- struct{}{}:
			default:
			}
		}
		if r.listPanesRelease != nil {
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			case <-r.listPanesRelease:
			}
		}
		if r.blockListPanes {
			<-ctx.Done()
			return "", ctx.Err()
		}
		r.mu.Lock()
		defer r.mu.Unlock()
		if r.paneMissing {
			return r.extraPanes, nil
		}
		return r.session + "\t%12\t" + fmt.Sprint(r.pid) + r.extraPanes, nil
	case "respawn-pane":
		if r.respawnEntered != nil {
			select {
			case r.respawnEntered <- struct{}{}:
			default:
			}
		}
		if r.respawnRelease != nil {
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			case <-r.respawnRelease:
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
	d.sessionRestartPromptHandoffWait = func(context.Context, sessionPromptHandoff) error {
		t.Fatal("Codex resume must not create or wait on a prompt handoff")
		return nil
	}
	target := sessionRestartAllTarget{ProjectID: project, SessionID: session, IssueID: "one", Activity: "busy", TmuxReady: true, ActiveIntent: true}
	refused := d.restartManagedAgentPane(ctx, target, protocol.SessionRestartAllRequestBody{}, protocol.SessionRestartAllItem{}, nil)
	if refused.Outcome != "busy" || !refused.Skipped || runner.respawns != 0 {
		t.Fatalf("refused=%+v respawns=%d", refused, runner.respawns)
	}
	restarted := d.restartManagedAgentPane(ctx, target, protocol.SessionRestartAllRequestBody{ForceBusy: true}, protocol.SessionRestartAllItem{}, nil)
	if !restarted.Restarted || restarted.Outcome != "busy_forced" || restarted.OldIdentity.PanePID == restarted.NewIdentity.PanePID || runner.respawns != 1 {
		t.Fatalf("restarted=%+v respawns=%d", restarted, runner.respawns)
	}
	if got := strings.Join([]string{restarted.Stages[0].Name, restarted.Stages[len(restarted.Stages)-1].Name}, ","); got != "preflight,persist_complete" {
		t.Fatalf("stage bounds=%s", got)
	}
}

func TestRestartManagedAgentPaneRequiresHybridInvariantSource(t *testing.T) {
	previous := daemonInvariantSourceMatrix[daemonInvariantManagedAgentRestart]
	daemonInvariantSourceMatrix[daemonInvariantManagedAgentRestart] = daemonInvariantSourceProjection
	t.Cleanup(func() { daemonInvariantSourceMatrix[daemonInvariantManagedAgentRestart] = previous })

	d, _, _, target := newExactRestartDaemon(t, "project", "az-1", "one", "idle")
	result := d.restartManagedAgentPane(context.Background(), target, protocol.SessionRestartAllRequestBody{}, protocol.SessionRestartAllItem{}, nil)
	if result.Outcome != "partial_failure" || !strings.Contains(result.Error, "requires hybrid invariant source") {
		t.Fatalf("restart result = %+v, want source-policy refusal", result)
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
	respawnEntered := make(chan struct{}, 1)
	respawnRelease := make(chan struct{})
	runner.respawnEntered = respawnEntered
	runner.respawnRelease = respawnRelease
	start := make(chan struct{})
	results := make(chan protocol.SessionRestartAllItem, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			results <- d.restartManagedAgentPane(context.Background(), target, protocol.SessionRestartAllRequestBody{}, protocol.SessionRestartAllItem{}, nil)
		}()
	}
	close(start)
	<-respawnEntered
	close(respawnRelease)
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

func TestRestartManagedAgentPaneCrossDaemonStaleIdentityRespawnsOnce(t *testing.T) {
	ctx := context.Background()
	const project = "project"
	const session = "az-1"
	dbPath := filepath.Join(t.TempDir(), "runtime.db")
	storeA := daemonstate.NewRuntimeStateStoreAtPath(dbPath, slog.Default())
	storeB := daemonstate.NewRuntimeStateStoreAtPath(dbPath, slog.Default())
	t.Cleanup(func() { _ = storeB.Close() })
	t.Cleanup(func() { _ = storeA.Close() })
	old := daemonstate.ManagedAgentIdentity{
		ProjectID: project, SessionID: session, LogicalPaneID: "agent", TmuxPaneID: "12",
		PanePID: 100, AgentIncarnation: "old", ObservedAt: time.Now(),
	}
	if err := storeA.UpsertManagedAgentIdentity(ctx, old); err != nil {
		t.Fatal(err)
	}
	runner := &exactRestartRunner{store: storeA, project: project, session: session, pid: old.PanePID}
	newDaemon := func(store *daemonstate.RuntimeStateStore) *Daemon {
		return &Daemon{
			cfg:  Config{RepoDir: t.TempDir(), CLITool: "codex", SessionShell: "zsh", Logger: slog.Default()},
			tmux: tmux.NewClient(runner, slog.Default()), runtimeStoresByProject: map[string]*daemonstate.RuntimeStateStore{project: store},
		}
	}
	daemonA, daemonB := newDaemon(storeA), newDaemon(storeB)
	target := sessionRestartAllTarget{ProjectID: project, SessionID: session, IssueID: "one", Activity: "idle", TmuxReady: true}
	start := make(chan struct{})
	results := make(chan protocol.SessionRestartAllItem, 2)
	var wg sync.WaitGroup
	for _, daemon := range []*Daemon{daemonA, daemonB} {
		wg.Add(1)
		go func(d *Daemon) {
			defer wg.Done()
			<-start
			results <- d.restartManagedAgentPaneWithIdentity(ctx, d.runtimeStoresByProject[project], old, target, protocol.SessionRestartAllRequestBody{}, protocol.SessionRestartAllItem{}, nil)
		}(daemon)
	}
	close(start)
	wg.Wait()
	close(results)

	restarted, superseded := 0, 0
	for result := range results {
		switch {
		case result.Restarted:
			restarted++
		case result.Skipped && result.Outcome == "superseded" && result.Reason == "managed_agent_identity_changed":
			superseded++
		default:
			t.Fatalf("unexpected result: %+v", result)
		}
	}
	respawns, _, _ := runner.snapshot()
	if restarted != 1 || superseded != 1 || respawns != 1 {
		t.Fatalf("restarted=%d superseded=%d respawns=%d, want 1/1/1", restarted, superseded, respawns)
	}
	current, found, err := storeB.GetManagedAgentIdentity(ctx, project, session, old.LogicalPaneID)
	if err != nil || !found {
		t.Fatalf("load replacement identity: found=%t err=%v", found, err)
	}
	third := daemonA.restartManagedAgentPaneWithIdentity(ctx, storeA, current, target, protocol.SessionRestartAllRequestBody{}, protocol.SessionRestartAllItem{}, nil)
	if !third.Restarted {
		t.Fatalf("next incarnation restart = %+v", third)
	}
	lockFiles, err := filepath.Glob(dbPath + ".managed-agent-restart-*.lock")
	if err != nil || len(lockFiles) != 1 {
		t.Fatalf("stable restart lock files=%v err=%v, want exactly one across incarnations", lockFiles, err)
	}
}

func TestRestartManagedAgentPaneParentCancellationAfterDispatchRemainsFencedAcrossDaemons(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	const project = "project"
	const session = "az-1"
	dbPath := filepath.Join(t.TempDir(), "runtime.db")
	storeA := daemonstate.NewRuntimeStateStoreAtPath(dbPath, slog.Default())
	storeB := daemonstate.NewRuntimeStateStoreAtPath(dbPath, slog.Default())
	t.Cleanup(func() { _ = storeB.Close() })
	t.Cleanup(func() { _ = storeA.Close() })
	old := daemonstate.ManagedAgentIdentity{ProjectID: project, SessionID: session, LogicalPaneID: "agent", TmuxPaneID: "12", PanePID: 100, AgentIncarnation: "old", ObservedAt: time.Now()}
	if err := storeA.UpsertManagedAgentIdentity(ctx, old); err != nil {
		t.Fatal(err)
	}
	runner := &exactRestartRunner{store: storeA, project: project, session: session, pid: old.PanePID}
	newDaemon := func(store *daemonstate.RuntimeStateStore) *Daemon {
		return &Daemon{cfg: Config{RepoDir: t.TempDir(), CLITool: "codex", SessionShell: "zsh", Logger: slog.Default()}, tmux: tmux.NewClient(runner, slog.Default()), runtimeStoresByProject: map[string]*daemonstate.RuntimeStateStore{project: store}}
	}
	daemonA, daemonB := newDaemon(storeA), newDaemon(storeB)
	observationEntered := make(chan struct{}, 1)
	observationRelease := make(chan struct{})
	accepted := make(chan string, 1)
	respawnCalls := 0
	daemonA.sessionRestartRespawn = func(_ context.Context, _, _, command string) (error, bool) {
		incarnation := regexp.MustCompile(`AZEDARACH_AGENT_INCARNATION='([^']+)'`).FindStringSubmatch(readLaunchScriptFromCommand(t, command))
		if len(incarnation) != 2 {
			t.Fatalf("planned incarnation missing from %q", command)
		}
		runner.mu.Lock()
		respawnCalls++
		runner.listPanesEntered = observationEntered
		runner.listPanesRelease = observationRelease
		runner.mu.Unlock()
		accepted <- incarnation[1]
		cancel()
		return context.Canceled, true
	}
	daemonB.sessionRestartRespawn = func(context.Context, string, string, string) (error, bool) {
		runner.mu.Lock()
		respawnCalls++
		runner.mu.Unlock()
		return errors.New("second daemon reached respawn"), false
	}
	target := sessionRestartAllTarget{ProjectID: project, SessionID: session, IssueID: "one", Activity: "idle", TmuxReady: true}
	resultA := make(chan protocol.SessionRestartAllItem, 1)
	go func() {
		resultA <- daemonA.restartManagedAgentPaneWithIdentity(ctx, storeA, old, target, protocol.SessionRestartAllRequestBody{}, protocol.SessionRestartAllItem{}, nil)
	}()
	planned := <-accepted
	<-observationEntered
	resultB := make(chan protocol.SessionRestartAllItem, 1)
	go func() {
		resultB <- daemonB.restartManagedAgentPaneWithIdentity(context.Background(), storeB, old, target, protocol.SessionRestartAllRequestBody{}, protocol.SessionRestartAllItem{}, nil)
	}()

	// Model tmux accepting the first command despite its lost/timed-out reply,
	// followed by the replacement hook projection. The first daemon still owns
	// the stable transition lock while this convergence becomes visible.
	runner.mu.Lock()
	runner.pid = 101
	runner.mu.Unlock()
	if err := storeA.UpsertManagedAgentIdentity(context.Background(), daemonstate.ManagedAgentIdentity{ProjectID: project, SessionID: session, LogicalPaneID: "agent", TmuxPaneID: "12", PanePID: 101, AgentIncarnation: planned, ObservedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	close(observationRelease)
	first, second := <-resultA, <-resultB
	runner.mu.Lock()
	gotRespawnCalls := respawnCalls
	runner.mu.Unlock()
	if !first.Restarted || second.Outcome != "superseded" || !second.Skipped || gotRespawnCalls != 1 {
		t.Fatalf("first=%+v second=%+v respawn_calls=%d", first, second, gotRespawnCalls)
	}
}

func TestRunRestartRespawnStageTreatsResultContextRaceAsAmbiguous(t *testing.T) {
	for range 100 {
		ctx, cancel := context.WithCancel(context.Background())
		entered := make(chan struct{})
		release := make(chan struct{})
		result := make(chan struct {
			err       error
			ambiguous bool
		}, 1)
		go func() {
			err, ambiguous := runRestartRespawnStage(ctx, time.Hour, func(stageCtx context.Context) error {
				close(entered)
				<-release
				return stageCtx.Err()
			})
			result <- struct {
				err       error
				ambiguous bool
			}{err: err, ambiguous: ambiguous}
		}()
		<-entered
		cancel()
		close(release)
		got := <-result
		if !errors.Is(got.err, context.Canceled) || !got.ambiguous {
			t.Fatalf("result-context race err=%v ambiguous=%t", got.err, got.ambiguous)
		}
	}
}

func TestRestartManagedAgentPaneObserveProgressFailureRemainsFencedAcrossDaemons(t *testing.T) {
	ctx := daemonops.WithProgressReporter(context.Background(), func(_ context.Context, progress daemonops.Progress) error {
		if progress.Phase == "session.restart_all.observe" {
			return errors.New("observe checkpoint unavailable")
		}
		return nil
	})
	const project, session = "project", "az-1"
	dbPath := filepath.Join(t.TempDir(), "runtime.db")
	storeA := daemonstate.NewRuntimeStateStoreAtPath(dbPath, slog.Default())
	storeB := daemonstate.NewRuntimeStateStoreAtPath(dbPath, slog.Default())
	t.Cleanup(func() { _ = storeB.Close() })
	t.Cleanup(func() { _ = storeA.Close() })
	old := daemonstate.ManagedAgentIdentity{ProjectID: project, SessionID: session, LogicalPaneID: "agent", TmuxPaneID: "12", PanePID: 100, AgentIncarnation: "old", ObservedAt: time.Now()}
	if err := storeA.UpsertManagedAgentIdentity(ctx, old); err != nil {
		t.Fatal(err)
	}
	runner := &exactRestartRunner{store: storeA, project: project, session: session, pid: 100}
	newDaemon := func(store *daemonstate.RuntimeStateStore) *Daemon {
		return &Daemon{cfg: Config{RepoDir: t.TempDir(), CLITool: "codex", SessionShell: "zsh", Logger: slog.Default()}, tmux: tmux.NewClient(runner, slog.Default()), runtimeStoresByProject: map[string]*daemonstate.RuntimeStateStore{project: store}}
	}
	daemonA, daemonB := newDaemon(storeA), newDaemon(storeB)
	observationEntered := make(chan struct{}, 1)
	observationRelease := make(chan struct{})
	accepted := make(chan string, 1)
	respawnCalls := 0
	daemonA.sessionRestartRespawn = func(_ context.Context, _, _, command string) (error, bool) {
		incarnation := regexp.MustCompile(`AZEDARACH_AGENT_INCARNATION='([^']+)'`).FindStringSubmatch(readLaunchScriptFromCommand(t, command))
		if len(incarnation) != 2 {
			t.Fatalf("planned incarnation missing from %q", command)
		}
		runner.mu.Lock()
		respawnCalls++
		runner.listPanesEntered = observationEntered
		runner.listPanesRelease = observationRelease
		runner.mu.Unlock()
		accepted <- incarnation[1]
		return context.DeadlineExceeded, true
	}
	daemonB.sessionRestartRespawn = func(context.Context, string, string, string) (error, bool) {
		runner.mu.Lock()
		respawnCalls++
		runner.mu.Unlock()
		return errors.New("second daemon reached respawn"), false
	}
	target := sessionRestartAllTarget{ProjectID: project, SessionID: session, IssueID: "one", Activity: "idle", TmuxReady: true}
	resultA := make(chan protocol.SessionRestartAllItem, 1)
	go func() {
		resultA <- daemonA.restartManagedAgentPaneWithIdentity(ctx, storeA, old, target, protocol.SessionRestartAllRequestBody{}, protocol.SessionRestartAllItem{}, nil)
	}()
	planned := <-accepted
	<-observationEntered
	resultB := make(chan protocol.SessionRestartAllItem, 1)
	go func() {
		resultB <- daemonB.restartManagedAgentPaneWithIdentity(ctx, storeB, old, target, protocol.SessionRestartAllRequestBody{}, protocol.SessionRestartAllItem{}, nil)
	}()
	runner.mu.Lock()
	runner.pid = 101
	runner.mu.Unlock()
	if err := storeA.UpsertManagedAgentIdentity(ctx, daemonstate.ManagedAgentIdentity{ProjectID: project, SessionID: session, LogicalPaneID: "agent", TmuxPaneID: "12", PanePID: 101, AgentIncarnation: planned, ObservedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	close(observationRelease)
	first, second := <-resultA, <-resultB
	runner.mu.Lock()
	gotRespawnCalls := respawnCalls
	runner.mu.Unlock()
	foundProgressFailure := false
	for _, stage := range first.Stages {
		if stage.Name == "persist_observe" && stage.Status == "failed" && strings.Contains(stage.Message, "checkpoint unavailable") {
			foundProgressFailure = true
		}
	}
	if !first.Restarted || !foundProgressFailure || second.Outcome != "superseded" || !second.Skipped || gotRespawnCalls != 1 {
		t.Fatalf("first=%+v second=%+v respawn_calls=%d", first, second, gotRespawnCalls)
	}
}

func readLaunchScriptFromCommand(t *testing.T, command string) string {
	t.Helper()
	for _, match := range regexp.MustCompile(`'([^']+)'`).FindAllStringSubmatch(command, -1) {
		if len(match) == 2 {
			if body, err := os.ReadFile(match[1]); err == nil {
				return string(body)
			}
		}
	}
	t.Fatalf("launch script missing from %q", command)
	return ""
}

func TestRestartManagedAgentPaneArtifactFlagsWorktreeAndUnrelatedPanes(t *testing.T) {
	d, store, runner, target := newExactRestartDaemon(t, "project", "az-1", "one", "waiting")
	d.cfg.DangerouslySkipPermissions = true
	runner.extraPanes = "\nother-session\t%99\t999\naz-1\t%13\t113"
	worktree := filepath.Join(t.TempDir(), "issue-worktree")
	if err := store.UpsertWorktreeState(context.Background(), daemonstate.WorktreeState{ProjectID: "project", IssueID: "one", Path: worktree, Branch: "issue/one", UpdatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	result := d.restartManagedAgentPane(context.Background(), target, protocol.SessionRestartAllRequestBody{Yolo: true}, protocol.SessionRestartAllItem{}, nil)
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
		result := d.restartManagedAgentPane(context.Background(), target, protocol.SessionRestartAllRequestBody{}, protocol.SessionRestartAllItem{}, nil)
		if result.Outcome != "partial_failure" || !strings.Contains(result.Error, "respawn failed") || result.Stages[len(result.Stages)-1].Name != "replace" {
			t.Fatalf("result=%+v", result)
		}
	})
	t.Run("preflight cancellation is bounded", func(t *testing.T) {
		d, _, runner, target := newExactRestartDaemon(t, "project", "az-1", "one", "idle")
		listPanesEntered := make(chan struct{}, 1)
		runner.listPanesEntered = listPanesEntered
		runner.blockListPanes = true
		ctx, cancel := context.WithCancel(context.Background())
		resultCh := make(chan protocol.SessionRestartAllItem, 1)
		go func() {
			resultCh <- d.restartManagedAgentPane(ctx, target, protocol.SessionRestartAllRequestBody{}, protocol.SessionRestartAllItem{}, nil)
		}()
		<-listPanesEntered
		cancel()
		result := <-resultCh
		stage := result.Stages[len(result.Stages)-1]
		if stage.Status != "failed" || !strings.Contains(stage.Message, context.Canceled.Error()) || stage.TimeoutMS != sessionRestartPreflightTimeout.Milliseconds() {
			t.Fatalf("result=%+v", result)
		}
	})
	t.Run("durable stage write fails closed", func(t *testing.T) {
		d, _, runner, target := newExactRestartDaemon(t, "project", "az-1", "one", "idle")
		ctx := daemonops.WithProgressReporter(context.Background(), func(context.Context, daemonops.Progress) error { return errors.New("progress store unavailable") })
		result := d.restartManagedAgentPane(ctx, target, protocol.SessionRestartAllRequestBody{}, protocol.SessionRestartAllItem{}, nil)
		respawns, _, _ := runner.snapshot()
		if result.Outcome != "partial_failure" || respawns != 0 || result.Stages[len(result.Stages)-1].Name != "persist_prepare" {
			t.Fatalf("result=%+v respawns=%d", result, respawns)
		}
	})
	t.Run("completion checkpoint failure is typed and returned", func(t *testing.T) {
		d, _, runner, target := newExactRestartDaemon(t, "project", "az-1", "one", "idle")
		ctx := daemonops.WithProgressReporter(context.Background(), func(_ context.Context, progress daemonops.Progress) error {
			if progress.Phase == "session.restart_all.complete" {
				return errors.New("completion checkpoint unavailable")
			}
			return nil
		})
		result := d.restartManagedAgentPane(ctx, target, protocol.SessionRestartAllRequestBody{}, protocol.SessionRestartAllItem{}, nil)
		respawns, _, _ := runner.snapshot()
		stage := result.Stages[len(result.Stages)-1]
		if result.Outcome != "partial_failure" || !strings.Contains(result.Error, "completion checkpoint unavailable") || stage.Name != "persist_complete" || respawns != 1 {
			t.Fatalf("result=%+v respawns=%d", result, respawns)
		}
	})
	t.Run("unconsumed continuation handoff is partial failure", func(t *testing.T) {
		d, _, runner, target := newExactRestartDaemon(t, "project", "az-1", "one", "idle")
		d.cfg.CLITool = "custom-agent"
		var promptPath string
		d.sessionRestartPromptHandoffWait = func(_ context.Context, handoff sessionPromptHandoff) error {
			promptPath = handoff.PromptPath
			return errors.New("continuation handoff was not consumed")
		}
		result := d.restartManagedAgentPane(context.Background(), target, protocol.SessionRestartAllRequestBody{}, protocol.SessionRestartAllItem{}, nil)
		respawns, _, _ := runner.snapshot()
		stage := result.Stages[len(result.Stages)-1]
		if result.Restarted || result.Outcome != "partial_failure" || !strings.Contains(result.Error, "not consumed") || stage.Name != "prompt_handoff" || respawns != 1 || promptPath == "" {
			t.Fatalf("result=%+v respawns=%d prompt_path=%q", result, respawns, promptPath)
		}
		if _, err := os.Stat(promptPath); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("prompt handoff remains after partial failure: %v", err)
		}
	})
}

func TestRecoverInterruptedSessionRestartConvergesWithoutRespawn(t *testing.T) {
	d, store, runner, target := newExactRestartDaemon(t, "project", "az-1", "one", "idle")
	plan := sessionRestartRecoveryPlan{ProjectID: target.ProjectID, SessionID: target.SessionID, IssueID: target.IssueID, Activity: target.Activity, Old: daemonstate.ManagedAgentIdentity{ProjectID: "project", SessionID: "az-1", LogicalPaneID: "agent", TmuxPaneID: "12", PanePID: 100, AgentIncarnation: "old"}, PlannedIncarnation: "planned", PromptHandoffType: sessionRestartPromptHandoffTypeNone, Stage: "observe"}
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

func TestRecoverInterruptedSessionRestartMatrix(t *testing.T) {
	t.Run("replacement with unconsumed handoff remains partial", func(t *testing.T) {
		d, store, runner, target := newExactRestartDaemon(t, "project", "az-1", "one", "idle")
		handoff, err := prepareSessionPromptHandoff(d.sessionLaunchArtifactDir(), "continue")
		if err != nil {
			t.Fatal(err)
		}
		promptPath := handoff.PromptPath
		d.sessionRestartPromptHandoffWait = func(_ context.Context, handoff sessionPromptHandoff) error {
			if handoff.PromptPath != promptPath {
				t.Fatalf("handoff path=%q want %q", handoff.PromptPath, promptPath)
			}
			return errors.New("recovered continuation handoff was not consumed")
		}
		runner.mu.Lock()
		runner.pid = 101
		runner.mu.Unlock()
		if err := store.UpsertManagedAgentIdentity(context.Background(), daemonstate.ManagedAgentIdentity{ProjectID: "project", SessionID: "az-1", LogicalPaneID: "agent", TmuxPaneID: "12", PanePID: 101, AgentIncarnation: "planned", ObservedAt: time.Now()}); err != nil {
			t.Fatal(err)
		}
		plan := sessionRestartRecoveryPlan{ProjectID: target.ProjectID, SessionID: target.SessionID, IssueID: target.IssueID, Activity: target.Activity, Old: daemonstate.ManagedAgentIdentity{ProjectID: "project", SessionID: "az-1", LogicalPaneID: "agent", TmuxPaneID: "12", PanePID: 100, AgentIncarnation: "old"}, PlannedIncarnation: "planned", PromptHandoffRequired: true, PromptHandoffType: sessionRestartPromptHandoffTypeOwnerOnlyArtifact, PromptPath: promptPath, Stage: "observe"}
		body, err := json.Marshal(plan)
		if err != nil {
			t.Fatal(err)
		}
		recovery, ok := d.recoverInterruptedSessionRestart(context.Background(), daemonops.Record{Kind: protocol.CommandSessionRestartAll, Progress: &daemonops.Progress{Phase: "session.restart_all.observe", Message: string(body)}})
		result := decodeRestartRecoveryResult(t, recovery)
		if !ok || result.Failed != 1 || result.Sessions[0].Restarted || result.Sessions[0].Stages[0].Name != "recover_prompt_handoff" {
			t.Fatalf("recovery=%+v result=%+v ok=%v", recovery, result, ok)
		}
	})
	t.Run("prepare before respawn", func(t *testing.T) {
		d, _, runner, target := newExactRestartDaemon(t, "project", "az-1", "one", "idle")
		recovery, ok := d.recoverInterruptedSessionRestart(context.Background(), restartRecoveryRecord(t, target, "prepare"))
		result := decodeRestartRecoveryResult(t, recovery)
		respawns, _, _ := runner.snapshot()
		if !ok || result.Failed != 1 || result.Sessions[0].Outcome != "partial_failure" || result.Sessions[0].Stages[0].Name != "recover_prepare" || respawns != 0 {
			t.Fatalf("recovery=%+v result=%+v ok=%v respawns=%d", recovery, result, ok, respawns)
		}
	})
	t.Run("replace ready old pane waits for delayed replacement and hook", func(t *testing.T) {
		d, store, runner, target := newExactRestartDaemon(t, "project", "az-1", "one", "idle")
		listPanesEntered := make(chan struct{}, 1)
		listPanesRelease := make(chan struct{})
		runner.listPanesEntered = listPanesEntered
		runner.listPanesRelease = listPanesRelease
		type recoveryResult struct {
			recovery interruptedOperationRecovery
			ok       bool
		}
		resultCh := make(chan recoveryResult, 1)
		record := restartRecoveryRecord(t, target, "replace_ready")
		recoveryCtx, cancelRecovery := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancelRecovery()
		go func() {
			recovery, ok := d.recoverInterruptedSessionRestart(recoveryCtx, record)
			resultCh <- recoveryResult{recovery: recovery, ok: ok}
		}()
		<-listPanesEntered
		runner.mu.Lock()
		runner.pid = 101
		runner.mu.Unlock()
		if err := store.UpsertManagedAgentIdentity(context.Background(), daemonstate.ManagedAgentIdentity{ProjectID: "project", SessionID: "az-1", LogicalPaneID: "agent", TmuxPaneID: "12", PanePID: 101, AgentIncarnation: "planned", ObservedAt: time.Now()}); err != nil {
			t.Fatal(err)
		}
		close(listPanesRelease)
		got := <-resultCh
		result := decodeRestartRecoveryResult(t, got.recovery)
		respawns, _, _ := runner.snapshot()
		if !got.ok || result.Restarted != 1 || result.Sessions[0].Outcome != restartSuccessOutcome(target.Activity) || respawns != 0 {
			t.Fatalf("recovery=%+v result=%+v ok=%v respawns=%d", got.recovery, result, got.ok, respawns)
		}
	})
	t.Run("live replacement waits for delayed hook", func(t *testing.T) {
		d, store, runner, target := newExactRestartDaemon(t, "project", "az-1", "one", "idle")
		listPanesEntered := make(chan struct{}, 1)
		listPanesRelease := make(chan struct{})
		runner.listPanesEntered = listPanesEntered
		runner.listPanesRelease = listPanesRelease
		runner.mu.Lock()
		runner.pid = 101
		runner.mu.Unlock()
		type recoveryResult struct {
			recovery interruptedOperationRecovery
			ok       bool
		}
		resultCh := make(chan recoveryResult, 1)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		go func() {
			recovery, ok := d.recoverInterruptedSessionRestart(ctx, restartRecoveryRecord(t, target, "observe"))
			resultCh <- recoveryResult{recovery: recovery, ok: ok}
		}()
		<-listPanesEntered
		if err := store.UpsertManagedAgentIdentity(context.Background(), daemonstate.ManagedAgentIdentity{ProjectID: "project", SessionID: "az-1", LogicalPaneID: "agent", TmuxPaneID: "12", PanePID: 101, AgentIncarnation: "planned", ObservedAt: time.Now()}); err != nil {
			t.Fatal(err)
		}
		close(listPanesRelease)
		got := <-resultCh
		result := decodeRestartRecoveryResult(t, got.recovery)
		if !got.ok || result.Restarted != 1 || result.Sessions[0].Stages[0].Status != "complete" {
			t.Fatalf("recovery=%+v result=%+v ok=%v", got.recovery, result, got.ok)
		}
	})
	t.Run("missing pane is typed crashed", func(t *testing.T) {
		d, _, runner, target := newExactRestartDaemon(t, "project", "az-1", "one", "idle")
		runner.mu.Lock()
		runner.paneMissing = true
		runner.mu.Unlock()
		recovery, ok := d.recoverInterruptedSessionRestart(context.Background(), restartRecoveryRecord(t, target, "observe"))
		result := decodeRestartRecoveryResult(t, recovery)
		if !ok || result.Skipped != 1 || result.Sessions[0].Outcome != "crashed" || result.Sessions[0].Stages[0].Status != "failed" {
			t.Fatalf("recovery=%+v result=%+v ok=%v", recovery, result, ok)
		}
	})
	t.Run("live replacement without hook times out typed partial", func(t *testing.T) {
		d, _, runner, target := newExactRestartDaemon(t, "project", "az-1", "one", "idle")
		listPanesEntered := make(chan struct{}, 1)
		runner.listPanesEntered = listPanesEntered
		runner.mu.Lock()
		runner.pid = 101
		runner.mu.Unlock()
		ctx, cancel := context.WithCancel(context.Background())
		type recoveryResult struct {
			recovery interruptedOperationRecovery
			ok       bool
		}
		resultCh := make(chan recoveryResult, 1)
		go func() {
			recovery, ok := d.recoverInterruptedSessionRestart(ctx, restartRecoveryRecord(t, target, "observe"))
			resultCh <- recoveryResult{recovery: recovery, ok: ok}
		}()
		<-listPanesEntered
		cancel()
		got := <-resultCh
		result := decodeRestartRecoveryResult(t, got.recovery)
		stage := result.Sessions[0].Stages[0]
		if !got.ok || result.Failed != 1 || result.Sessions[0].Outcome != "partial_failure" || stage.Name != "recover_observe" || stage.Status != "failed" || !strings.Contains(stage.Message, "canceled") || stage.TimeoutMS == 0 {
			t.Fatalf("recovery=%+v result=%+v ok=%v", got.recovery, result, got.ok)
		}
	})
}

func TestRecoverInterruptedSessionRestartValidatesPromptHandoffBeforeWaitOrRemoval(t *testing.T) {
	t.Run("rejects path outside launch directory without touching it", func(t *testing.T) {
		d, store, runner, target := newExactRestartDaemon(t, "project", "az-1", "one", "idle")
		if err := ensureSessionLaunchArtifactDir(d.sessionLaunchArtifactDir()); err != nil {
			t.Fatal(err)
		}
		outside := filepath.Join(t.TempDir(), sessionLaunchArtifactPrefix+"outside.prompt")
		if err := os.WriteFile(outside, []byte("secret"), 0o600); err != nil {
			t.Fatal(err)
		}
		seedRecoveredReplacement(t, store, runner, "planned")
		plan := recoveryPlanForTarget(target, "observe")
		plan.PromptHandoffRequired = true
		plan.PromptHandoffType = sessionRestartPromptHandoffTypeOwnerOnlyArtifact
		plan.PromptPath = outside
		called := false
		d.sessionRestartPromptHandoffWait = func(context.Context, sessionPromptHandoff) error {
			called = true
			return nil
		}

		result := decodeRestartRecoveryResult(t, mustRecoverRestartPlan(t, d, plan))
		if result.Failed != 1 || !strings.Contains(result.Sessions[0].Error, "outside session launch artifact directory") || called {
			t.Fatalf("result=%+v waiter_called=%t", result, called)
		}
		if body, err := os.ReadFile(outside); err != nil || string(body) != "secret" {
			t.Fatalf("outside artifact changed: body=%q err=%v", body, err)
		}
	})

	t.Run("validated owner-only artifact is removed after terminal wait failure", func(t *testing.T) {
		d, store, runner, target := newExactRestartDaemon(t, "project", "az-1", "one", "idle")
		if err := ensureSessionLaunchArtifactDir(d.sessionLaunchArtifactDir()); err != nil {
			t.Fatal(err)
		}
		handoff, err := prepareSessionPromptHandoff(d.sessionLaunchArtifactDir(), "continue")
		if err != nil {
			t.Fatal(err)
		}
		seedRecoveredReplacement(t, store, runner, "planned")
		plan := recoveryPlanForTarget(target, "observe")
		plan.PromptHandoffRequired = true
		plan.PromptHandoffType = sessionRestartPromptHandoffTypeOwnerOnlyArtifact
		plan.PromptPath = handoff.PromptPath
		d.sessionRestartPromptHandoffWait = func(_ context.Context, got sessionPromptHandoff) error {
			if got.PromptPath != handoff.PromptPath {
				t.Fatalf("handoff=%q want %q", got.PromptPath, handoff.PromptPath)
			}
			return errors.New("not consumed")
		}

		result := decodeRestartRecoveryResult(t, mustRecoverRestartPlan(t, d, plan))
		if result.Failed != 1 || !strings.Contains(result.Sessions[0].Error, "not consumed") {
			t.Fatalf("result=%+v", result)
		}
		if _, err := os.Lstat(handoff.PromptPath); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("validated terminal artifact remains: %v", err)
		}
	})

	t.Run("rejects permissive artifact without removing it", func(t *testing.T) {
		d, store, runner, target := newExactRestartDaemon(t, "project", "az-1", "one", "idle")
		if err := ensureSessionLaunchArtifactDir(d.sessionLaunchArtifactDir()); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(d.sessionLaunchArtifactDir(), sessionLaunchArtifactPrefix+"permissive.prompt")
		if err := os.WriteFile(path, []byte("secret"), 0o644); err != nil {
			t.Fatal(err)
		}
		seedRecoveredReplacement(t, store, runner, "planned")
		plan := recoveryPlanForTarget(target, "observe")
		plan.PromptHandoffRequired = true
		plan.PromptHandoffType = sessionRestartPromptHandoffTypeOwnerOnlyArtifact
		plan.PromptPath = path

		result := decodeRestartRecoveryResult(t, mustRecoverRestartPlan(t, d, plan))
		if result.Failed != 1 || !strings.Contains(result.Sessions[0].Error, "owner-only") {
			t.Fatalf("result=%+v", result)
		}
		if _, err := os.Lstat(path); err != nil {
			t.Fatalf("invalid artifact was removed: %v", err)
		}
	})
}

func TestRecoverInterruptedRootedRestartRepairsBootstrapAcrossCrashWindows(t *testing.T) {
	for _, tt := range []struct {
		name        string
		stage       string
		replacement bool
		wantRestart bool
	}{
		{name: "after invalidation before replacement", stage: sessionRestartStageRootedInvalidateReady},
		{name: "after replacement", stage: "observe", replacement: true, wantRestart: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			d, store, runner, target := newExactRestartDaemon(t, "project", "az-1", "one", "idle")
			scope, err := domain.RootedOrchestrationScope(target.IssueID)
			if err != nil {
				t.Fatal(err)
			}
			identity, err := domain.NewOrchestratorIdentity(target.ProjectID, scope)
			if err != nil {
				t.Fatal(err)
			}
			if tt.replacement {
				seedRecoveredReplacement(t, store, runner, "planned")
			}
			plan := recoveryPlanForTarget(target, tt.stage)
			plan.RootedIdentity = &identity
			called := 0
			d.sessionRestartRootedBootstrapRepair = func(ctx context.Context, got domain.OrchestratorIdentity, sessionID string) error {
				called++
				if got != identity || sessionID != target.SessionID {
					t.Fatalf("repair target identity=%+v session=%q", got, sessionID)
				}
				now := time.Now().UTC()
				if err := d.tmux.SetEnvironment(ctx, sessionID, rootedOrchestratorBootstrapNonceEnvironment, "nonce"); err != nil {
					return err
				}
				return daemonstate.NewRootedBootstrapAcknowledgementAuthority(store).Acknowledge(ctx, daemonstate.RootedBootstrapAcknowledgement{
					Identity: got, SessionID: sessionID, PromptHash: "prompt", RuntimeNonce: "nonce", AcknowledgedAt: now, UpdatedAt: now,
				})
			}

			result := decodeRestartRecoveryResult(t, mustRecoverRestartPlan(t, d, plan))
			if called != 1 || (result.Restarted == 1) != tt.wantRestart {
				t.Fatalf("result=%+v repair_calls=%d", result, called)
			}
			if tt.wantRestart {
				if result.Failed != 0 {
					t.Fatalf("replacement recovery=%+v", result)
				}
			} else if result.Failed != 1 || !strings.Contains(result.Sessions[0].Error, "before exact-pane replacement") {
				t.Fatalf("pre-replacement recovery=%+v", result)
			}
			ack, found, err := daemonstate.NewRootedBootstrapAcknowledgementAuthority(store).Get(context.Background(), identity)
			if err != nil || !found || ack.SessionID != target.SessionID || ack.RuntimeNonce == "" {
				t.Fatalf("repaired acknowledgement=%+v found=%t err=%v", ack, found, err)
			}
		})
	}
}

func TestRecoverInterruptedRootedRestartHoldsRootedThenPaneLocksAgainstLiveRestart(t *testing.T) {
	base := t.TempDir()
	dbPath := filepath.Join(base, "runtime.db")
	store := daemonstate.NewRuntimeStateStoreAtPath(dbPath, slog.Default())
	t.Cleanup(func() { _ = store.Close() })
	old := daemonstate.ManagedAgentIdentity{ProjectID: "project", SessionID: "az-1", LogicalPaneID: "agent", TmuxPaneID: "12", PanePID: 100, AgentIncarnation: "old", ObservedAt: time.Now()}
	if err := store.UpsertManagedAgentIdentity(context.Background(), old); err != nil {
		t.Fatal(err)
	}
	runner := &exactRestartRunner{store: store, project: "project", session: "az-1", pid: 100}
	d := &Daemon{cfg: Config{RepoDir: base, CLITool: "codex", SessionShell: "zsh", Logger: slog.Default()}, tmux: tmux.NewClient(runner, slog.Default()), runtimeStoresByProject: map[string]*daemonstate.RuntimeStateStore{"project": store}}
	target := sessionRestartAllTarget{ProjectID: "project", SessionID: "az-1", IssueID: "one", Activity: "idle", TmuxReady: true}
	scope, err := domain.RootedOrchestrationScope(target.IssueID)
	if err != nil {
		t.Fatal(err)
	}
	identity, err := domain.NewOrchestratorIdentity(target.ProjectID, scope)
	if err != nil {
		t.Fatal(err)
	}
	plan := recoveryPlanForTarget(target, sessionRestartStageRootedInvalidateReady)
	plan.RootedIdentity = &identity
	repairEntered := make(chan struct{})
	repairRelease := make(chan struct{})
	d.sessionRestartRootedBootstrapRepair = func(ctx context.Context, got domain.OrchestratorIdentity, sessionID string) error {
		close(repairEntered)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-repairRelease:
		}
		if err := d.tmux.SetEnvironment(ctx, sessionID, rootedOrchestratorBootstrapNonceEnvironment, "nonce"); err != nil {
			return err
		}
		now := time.Now().UTC()
		return daemonstate.NewRootedBootstrapAcknowledgementAuthority(store).Acknowledge(ctx, daemonstate.RootedBootstrapAcknowledgement{Identity: got, SessionID: sessionID, PromptHash: "prompt", RuntimeNonce: "nonce", AcknowledgedAt: now, UpdatedAt: now})
	}
	body, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	record := daemonops.Record{Kind: protocol.CommandSessionRestartAll, Progress: &daemonops.Progress{Phase: "session.restart_all." + plan.Stage, Message: string(body)}}
	type recovered struct {
		value interruptedOperationRecovery
		ok    bool
	}
	recoveryResult := make(chan recovered, 1)
	go func() {
		value, ok := d.recoverInterruptedSessionRestart(context.Background(), record)
		recoveryResult <- recovered{value: value, ok: ok}
	}()
	<-repairEntered
	assertRestartTransitionLocksHeld(t, dbPath)
	close(repairRelease)
	recoveredResult := <-recoveryResult
	if !recoveredResult.ok {
		t.Fatal("restart plan was not recognized")
	}
	result := decodeRestartRecoveryResult(t, recoveredResult.value)
	if result.Failed != 1 || !strings.Contains(result.Sessions[0].Error, "before exact-pane replacement") {
		t.Fatalf("recovery result=%+v", result)
	}
	if got := d.restartManagedAgentPane(context.Background(), target, protocol.SessionRestartAllRequestBody{}, protocol.SessionRestartAllItem{}, nil); !got.Restarted {
		t.Fatalf("live restart after recovered lock release=%+v", got)
	}
	respawns, _, _ := runner.snapshot()
	if respawns != 1 {
		t.Fatalf("respawns=%d, want one live replacement after recovery", respawns)
	}
}

func TestRecoverInterruptedRootedRestartRejectsDurableRuntimeNonceMismatch(t *testing.T) {
	d, store, runner, target := newExactRestartDaemon(t, "project", "az-1", "one", "idle")
	scope, err := domain.RootedOrchestrationScope(target.IssueID)
	if err != nil {
		t.Fatal(err)
	}
	identity, err := domain.NewOrchestratorIdentity(target.ProjectID, scope)
	if err != nil {
		t.Fatal(err)
	}
	seedRecoveredReplacement(t, store, runner, "planned")
	plan := recoveryPlanForTarget(target, "observe")
	plan.RootedIdentity = &identity
	d.sessionRestartRootedBootstrapRepair = func(ctx context.Context, got domain.OrchestratorIdentity, sessionID string) error {
		if err := d.tmux.SetEnvironment(ctx, sessionID, rootedOrchestratorBootstrapNonceEnvironment, "live-nonce"); err != nil {
			return err
		}
		now := time.Now().UTC()
		return daemonstate.NewRootedBootstrapAcknowledgementAuthority(store).Acknowledge(ctx, daemonstate.RootedBootstrapAcknowledgement{Identity: got, SessionID: sessionID, PromptHash: "prompt", RuntimeNonce: "durable-nonce", AcknowledgedAt: now, UpdatedAt: now})
	}
	result := decodeRestartRecoveryResult(t, mustRecoverRestartPlan(t, d, plan))
	if result.Failed != 1 || !strings.Contains(result.Sessions[0].Error, "does not match live runtime nonce") {
		t.Fatalf("result=%+v", result)
	}
}

func assertRestartTransitionLocksHeld(t *testing.T, dbPath string) {
	t.Helper()
	for _, pattern := range []string{dbPath + ".orchestrator-*.lock", dbPath + ".managed-agent-restart-*.lock"} {
		paths, err := filepath.Glob(pattern)
		if err != nil || len(paths) != 1 {
			t.Fatalf("transition locks for %q: paths=%v err=%v", pattern, paths, err)
		}
		file, err := os.OpenFile(paths[0], os.O_RDWR, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		err = unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		_ = file.Close()
		if !errors.Is(err, unix.EWOULDBLOCK) && !errors.Is(err, unix.EAGAIN) {
			t.Fatalf("lock %s was not held by recovery: %v", paths[0], err)
		}
	}
}

func seedRecoveredReplacement(t *testing.T, store *daemonstate.RuntimeStateStore, runner *exactRestartRunner, incarnation string) {
	t.Helper()
	runner.mu.Lock()
	runner.pid = 101
	runner.mu.Unlock()
	if err := store.UpsertManagedAgentIdentity(context.Background(), daemonstate.ManagedAgentIdentity{
		ProjectID: "project", SessionID: "az-1", LogicalPaneID: "agent", TmuxPaneID: "12", PanePID: 101,
		AgentIncarnation: incarnation, ObservedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
}

func recoveryPlanForTarget(target sessionRestartAllTarget, stage string) sessionRestartRecoveryPlan {
	return sessionRestartRecoveryPlan{
		ProjectID: target.ProjectID, SessionID: target.SessionID, IssueID: target.IssueID, Activity: target.Activity,
		Old:                daemonstate.ManagedAgentIdentity{ProjectID: target.ProjectID, SessionID: target.SessionID, LogicalPaneID: "agent", TmuxPaneID: "12", PanePID: 100, AgentIncarnation: "old"},
		PlannedIncarnation: "planned", PromptHandoffType: sessionRestartPromptHandoffTypeNone, Stage: stage,
	}
}

func mustRecoverRestartPlan(t *testing.T, d *Daemon, plan sessionRestartRecoveryPlan) interruptedOperationRecovery {
	t.Helper()
	body, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	recovery, ok := d.recoverInterruptedSessionRestart(context.Background(), daemonops.Record{
		Kind: protocol.CommandSessionRestartAll, Progress: &daemonops.Progress{Phase: "session.restart_all." + plan.Stage, Message: string(body)},
	})
	if !ok {
		t.Fatal("restart plan was not recognized")
	}
	return recovery
}

func restartRecoveryRecord(t *testing.T, target sessionRestartAllTarget, stage string) daemonops.Record {
	t.Helper()
	plan := recoveryPlanForTarget(target, stage)
	body, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	return daemonops.Record{Kind: protocol.CommandSessionRestartAll, Progress: &daemonops.Progress{Phase: "session.restart_all." + stage, Message: string(body)}}
}

func TestDecodeSessionRestartRecoveryPlanBindsDuplicatedAuthority(t *testing.T) {
	target := sessionRestartAllTarget{ProjectID: "project", SessionID: "az-1", IssueID: "one", Activity: "idle"}
	rootedScope, err := domain.RootedOrchestrationScope(target.IssueID)
	if err != nil {
		t.Fatal(err)
	}
	rootedIdentity, err := domain.NewOrchestratorIdentity(target.ProjectID, rootedScope)
	if err != nil {
		t.Fatal(err)
	}
	base := recoveryPlanForTarget(target, "observe")
	base.RootedIdentity = &rootedIdentity
	recordFor := func(plan sessionRestartRecoveryPlan, phase, projectID string) daemonops.Record {
		body, marshalErr := json.Marshal(plan)
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		return daemonops.Record{ProjectID: projectID, Kind: protocol.CommandSessionRestartAll, Progress: &daemonops.Progress{Phase: phase, Message: string(body)}}
	}
	if _, ok := decodeSessionRestartRecoveryPlan(recordFor(base, "session.restart_all.observe", target.ProjectID)); !ok {
		t.Fatal("exact duplicated authority was rejected")
	}

	wrongStage := base
	wrongStage.Stage = "replace_ready"
	wrongRoot := base
	wrongRoot.IssueID = "two"
	wrongIdentityProject := base
	identity := rootedIdentity
	identity.ProjectID = "other"
	wrongIdentityProject.RootedIdentity = &identity
	for name, record := range map[string]daemonops.Record{
		"phase stage mismatch":      recordFor(wrongStage, "session.restart_all.observe", target.ProjectID),
		"record project mismatch":   recordFor(base, "session.restart_all.observe", "other"),
		"root issue mismatch":       recordFor(wrongRoot, "session.restart_all.observe", target.ProjectID),
		"identity project mismatch": recordFor(wrongIdentityProject, "session.restart_all.observe", target.ProjectID),
	} {
		t.Run(name, func(t *testing.T) {
			if _, ok := decodeSessionRestartRecoveryPlan(record); ok {
				t.Fatalf("mismatched recovery authority was accepted: %+v", record)
			}
		})
	}
}

func decodeRestartRecoveryResult(t *testing.T, recovery interruptedOperationRecovery) protocol.SessionRestartAllResponseBody {
	t.Helper()
	var result protocol.SessionRestartAllResponseBody
	if err := json.Unmarshal(recovery.ResultPayload, &result); err != nil || len(result.Sessions) != 1 {
		t.Fatalf("decode recovery result: result=%+v err=%v", result, err)
	}
	return result
}

func TestRestartStateOutcomeClassification(t *testing.T) {
	for activity, want := range map[string]string{"idle": "idle", "waiting_human": "waiting", "busy": "busy_forced", "unknown": "unknown"} {
		if got := restartSuccessOutcome(activity); got != want {
			t.Errorf("activity %s outcome=%s want=%s", activity, got, want)
		}
	}
}

func TestRealTmuxSupervisedRestartNeverSubmitsLifecycleTextAndPreservesOtherPane(t *testing.T) {
	tmuxPath, err := exec.LookPath("tmux")
	if err != nil {
		t.Skip("tmux is not installed")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	base := t.TempDir()
	repoDir := filepath.Join(base, "project")
	if err := os.MkdirAll(repoDir, 0o755); err != nil {
		t.Fatal(err)
	}
	inputTrace := filepath.Join(base, "agent-input")
	argsTrace := filepath.Join(base, "agent-args")
	managedReady := "managed-ready-" + strconv.FormatInt(time.Now().UnixNano(), 10)
	auxReady := "aux-ready-" + strconv.FormatInt(time.Now().UnixNano(), 10)
	t.Setenv("AZ_TEST_INPUT_TRACE", inputTrace)
	t.Setenv("AZ_TEST_ARGS_TRACE", argsTrace)
	t.Setenv("AZ_TEST_MANAGED_READY", managedReady)
	t.Setenv("AZ_TEST_AUX_READY", auxReady)
	fakeAgent := filepath.Join(base, "fakeagent")
	fakeAgentScript := `#!/bin/sh
for arg in "$@"; do
  case "$arg" in
    "Read and follow the complete worker instructions in "*)
      prompt_path=${arg#Read and follow the complete worker instructions in }
      prompt_path=${prompt_path%. Delete that file immediately after reading it.}
      rm -f -- "$prompt_path"
      ;;
  esac
done
if [ "$1" = "--aux" ]; then
  label=auxiliary
  channel="$AZ_TEST_AUX_READY"
else
  label=managed
  channel="$AZ_TEST_MANAGED_READY"
fi
printf '%s|%s\n' "$label" "$*" >>"$AZ_TEST_ARGS_TRACE"
printf 'assistant: existing conversation restored for %s\n' "$label"
tmux wait-for -S "$channel"
cat >>"$AZ_TEST_INPUT_TRACE"
`
	if err := os.WriteFile(fakeAgent, []byte(fakeAgentScript), 0o755); err != nil {
		t.Fatal(err)
	}

	store := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(base, "runtime.db"), slog.Default())
	t.Cleanup(func() { _ = store.Close() })
	const projectID = "project-a"
	const sessionID = "project-a-az-1"
	socketName := "az-dla-" + strconv.FormatInt(time.Now().UnixNano(), 10)
	if _, err := (&realRestartRunner{tmuxPath: tmuxPath}).Run(ctx, "list-sessions"); err == nil || !strings.Contains(err.Error(), "private tmux socket") {
		t.Fatalf("default-socket guard error = %v", err)
	}
	runner := &realRestartRunner{tmuxPath: tmuxPath, socketName: socketName, store: store, projectID: projectID, session: sessionID}
	if output, err := runner.Run(ctx, "-f", "/dev/null", "new-session", "-d", "-s", sessionID, "-c", repoDir, fakeAgent); err != nil {
		t.Fatalf("start managed pane: %v (%s)", err, output)
	}
	t.Cleanup(func() { _, _ = runner.Run(context.Background(), "kill-server") })
	if output, err := runner.Run(ctx, "new-window", "-d", "-t", sessionID, "-n", "unrelated", fakeAgent, "--aux"); err != nil {
		t.Fatalf("start unrelated pane: %v (%s)", err, output)
	}
	if output, err := runner.Run(ctx, "wait-for", managedReady); err != nil {
		t.Fatalf("wait for managed fixture: %v (%s)", err, output)
	}
	if output, err := runner.Run(ctx, "wait-for", auxReady); err != nil {
		t.Fatalf("wait for unrelated fixture: %v (%s)", err, output)
	}

	metadata := func(target string) (string, int) {
		t.Helper()
		output, err := runner.Run(ctx, "display-message", "-p", "-t", target, "#{pane_id}\t#{pane_pid}")
		if err != nil {
			t.Fatalf("read pane metadata for %s: %v (%s)", target, err, output)
		}
		fields := strings.Split(strings.TrimSpace(output), "\t")
		if len(fields) != 2 {
			t.Fatalf("pane metadata for %s = %q", target, output)
		}
		pid, err := strconv.Atoi(fields[1])
		if err != nil {
			t.Fatal(err)
		}
		return strings.TrimPrefix(fields[0], "%"), pid
	}
	oldPane, oldPID := metadata(sessionID + ":0")
	_, unrelatedPID := metadata(sessionID + ":unrelated")
	if err := store.UpsertManagedAgentIdentity(ctx, daemonstate.ManagedAgentIdentity{
		ProjectID: projectID, SessionID: sessionID, LogicalPaneID: "agent",
		TmuxPaneID: oldPane, PanePID: oldPID, AgentIncarnation: "original",
		ObservedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	if output, err := runner.Run(ctx, "send-keys", "-l", "-t", "%"+oldPane, "user: unfinished active conversation"); err != nil {
		t.Fatalf("seed active composer: %v (%s)", err, output)
	}
	before, err := runner.Run(ctx, "capture-pane", "-p", "-t", "%"+oldPane)
	if err != nil || !strings.Contains(before, "unfinished active conversation") {
		t.Fatalf("active conversation fixture missing: err=%v output=%q", err, before)
	}

	d := &Daemon{
		cfg:                    Config{RepoDir: repoDir, CLITool: fakeAgent, SessionShell: "/bin/sh", Logger: slog.Default()},
		tmux:                   tmux.NewClient(runner, slog.Default()),
		runtimeStoresByProject: map[string]*daemonstate.RuntimeStateStore{projectID: store},
	}
	target := sessionRestartAllTarget{ProjectID: projectID, SessionID: sessionID, IssueID: "az-1", Activity: "busy", ActivitySource: "hooks", TmuxReady: true, ActiveIntent: true}
	result := d.restartManagedAgentPane(ctx, target, protocol.SessionRestartAllRequestBody{ForceBusy: true}, sessionRestartAllItem(target), nil)
	if !result.Restarted || result.Outcome != "busy_forced" || result.OldIdentity == nil || result.NewIdentity == nil {
		t.Fatalf("restart result = %+v", result)
	}
	if result.OldIdentity.PanePID == result.NewIdentity.PanePID || result.OldIdentity.AgentIncarnation == result.NewIdentity.AgentIncarnation {
		t.Fatalf("restart did not prove a distinct process incarnation: old=%+v new=%+v", result.OldIdentity, result.NewIdentity)
	}
	if output, err := runner.Run(ctx, "wait-for", managedReady); err != nil {
		t.Fatalf("wait for replacement fixture: %v (%s)", err, output)
	}
	_, gotUnrelatedPID := metadata(sessionID + ":unrelated")
	if gotUnrelatedPID != unrelatedPID {
		t.Fatalf("unrelated pane pid changed: got %d want %d", gotUnrelatedPID, unrelatedPID)
	}
	if input, err := os.ReadFile(inputTrace); err == nil && len(input) > 0 {
		t.Fatalf("lifecycle bytes reached agent stdin: %q", input)
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	args, err := os.ReadFile(argsTrace)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"export PATH=", "codex resume", "send-keys"} {
		if strings.Contains(string(args), forbidden) {
			t.Fatalf("legacy lifecycle payload %q reached agent launch input: %s", forbidden, args)
		}
	}
}
