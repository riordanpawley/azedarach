package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	daemonhandlers "github.com/riordanpawley/azedarach/internal/daemon/handlers"
	"github.com/riordanpawley/azedarach/internal/daemon/publish"
	daemonstate "github.com/riordanpawley/azedarach/internal/daemon/state"
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/naming"
	"github.com/riordanpawley/azedarach/internal/services/git"
	"github.com/riordanpawley/azedarach/internal/services/issues"
	"github.com/riordanpawley/azedarach/internal/services/tmux"
)

type testTmuxRunner struct {
	mu              sync.Mutex
	sessions        map[string]bool
	newSessionCalls int
	killEntered     chan struct{}
	killRelease     chan struct{}
}

func newTestTmuxRunner(initialSession string) *testTmuxRunner {
	return &testTmuxRunner{
		sessions: map[string]bool{
			initialSession: true,
		},
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
		names := make([]string, 0, len(r.sessions))
		for name := range r.sessions {
			names = append(names, name)
		}
		return strings.Join(names, "\n"), nil
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

type testGitRunner struct {
	worktreePath string
	branchName   string
}

func (r *testGitRunner) Run(_ context.Context, args ...string) (string, error) {
	if len(args) >= 3 && args[0] == "worktree" && args[1] == "list" && args[2] == "--porcelain" {
		return "worktree " + r.worktreePath + "\nbranch refs/heads/" + r.branchName + "\n\n", nil
	}
	return "", nil
}

type recoveringWorktreeRunner struct {
	worktreePath string
	branchName   string
	listCalls    int
}

func (r *recoveringWorktreeRunner) Run(_ context.Context, args ...string) (string, error) {
	if len(args) >= 2 && args[0] == "config" && args[1] == "user.name" {
		return "testuser\n", nil
	}
	if len(args) >= 3 && args[0] == "worktree" && args[1] == "list" && args[2] == "--porcelain" {
		r.listCalls++
		if r.listCalls == 1 {
			return "", nil
		}
		return "worktree " + r.worktreePath + "\nbranch refs/heads/" + r.branchName + "\n\n", nil
	}
	if len(args) >= 3 && args[0] == "worktree" && args[1] == "add" && args[2] == "-b" {
		return "", fmt.Errorf("git worktree add -b failed: exit status 1: hook failed")
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

func TestSessionProjectionByIssueKeyPrefersActiveStateOverStopped(t *testing.T) {
	now := time.Now().UTC()
	sessions := []daemonstate.Session{
		{
			ID:        "az-bra",
			IssueID:   "az-bra",
			State:     daemonstate.SessionStateStopped,
			UpdatedAt: now,
		},
		{
			ID:        "plain",
			IssueID:   "bra",
			State:     daemonstate.SessionStateAttached,
			UpdatedAt: now.Add(-1 * time.Minute),
		},
	}

	byIssue := sessionProjectionByIssueKey(sessions, "azedarach")
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
}

func TestSourceForSessionInvariant(t *testing.T) {
	d := &Daemon{}
	if got := d.sourceForSessionInvariant(sessionInvariantSessionStartConflict); got != daemonInvariantSourceTmux {
		t.Fatalf("start conflict source = %s, want %s", got, daemonInvariantSourceTmux)
	}
	if got := d.sourceForSessionInvariant(sessionInvariantSessionAttachTarget); got != daemonInvariantSourceTmux {
		t.Fatalf("attach target source = %s, want %s", got, daemonInvariantSourceTmux)
	}
	if got := d.sourceForSessionInvariant(sessionInvariantSessionStopTargets); got != daemonInvariantSourceTmux {
		t.Fatalf("stop targets source = %s, want %s", got, daemonInvariantSourceTmux)
	}
	if got := d.sourceForSessionInvariant(sessionInvariantSessionReconcile); got != daemonInvariantSourceHybrid {
		t.Fatalf("reconcile source = %s, want %s", got, daemonInvariantSourceHybrid)
	}
}

type sessionStartTmuxRunner struct {
	sessions      map[string]bool
	sendKeysCalls int
}

func newSessionStartTmuxRunner() *sessionStartTmuxRunner {
	return &sessionStartTmuxRunner{sessions: map[string]bool{}}
}

func (r *sessionStartTmuxRunner) Run(_ context.Context, args ...string) (string, error) {
	if len(args) == 0 {
		return "", errors.New("missing tmux args")
	}
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
		r.sessions[args[3]] = true
		return "", nil
	case "send-keys":
		r.sendKeysCalls++
		return "", nil
	case "list-sessions":
		names := make([]string, 0, len(r.sessions))
		for name := range r.sessions {
			names = append(names, name)
		}
		return strings.Join(names, "\n"), nil
	default:
		return "", nil
	}
}

func TestSessionStartRecoversFromPartialWorktreeCreate(t *testing.T) {
	ctx := context.Background()
	repoDir := t.TempDir()
	projectID := "proj"
	if err := os.MkdirAll(filepath.Join(repoDir, ".azedarach"), 0o755); err != nil {
		t.Fatalf("mkdir .azedarach: %v", err)
	}

	issuesClient := issues.NewClient(repoDir, slog.Default())
	t.Cleanup(func() {
		_ = issuesClient.CloseDB()
	})
	issueID, err := issuesClient.Create(ctx, issues.CreateTaskParams{
		Title: "Recover from partial worktree create",
		Type:  domain.TypeTask,
	})
	if err != nil {
		t.Fatalf("create issue: %v", err)
	}

	branch := "testuser/" + issueID + "/recover-from-partial-work"
	worktreePath := filepath.Join(filepath.Dir(repoDir), filepath.Base(repoDir)+"-"+issueID)
	worktreeRunner := &recoveringWorktreeRunner{
		worktreePath: worktreePath,
		branchName:   branch,
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

	req := protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-start-recover",
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

	var payload struct {
		Output string `json:"output"`
	}
	if err := json.Unmarshal(resp.Body, &payload); err != nil {
		t.Fatalf("unmarshal response body: %v", err)
	}
	if !strings.Contains(payload.Output, "Worktree reused: "+worktreePath) {
		t.Fatalf("output = %q, want reused worktree path", payload.Output)
	}
	if tmuxRunner.sendKeysCalls != 1 {
		t.Fatalf("send-keys calls = %d, want 1", tmuxRunner.sendKeysCalls)
	}
}

func TestSessionStartIgnoresStaleProjectionWhenTmuxHasNoSession(t *testing.T) {
	ctx := context.Background()
	repoDir := t.TempDir()
	projectID := "proj"
	if err := os.MkdirAll(filepath.Join(repoDir, ".azedarach"), 0o755); err != nil {
		t.Fatalf("mkdir .azedarach: %v", err)
	}

	issuesClient := issues.NewClient(repoDir, slog.Default())
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
	worktreeRunner := &recoveringWorktreeRunner{
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

func TestSessionStartWithStartWorkFalseSkipsLaunchCommand(t *testing.T) {
	ctx := context.Background()
	repoDir := t.TempDir()
	projectID := "proj"
	if err := os.MkdirAll(filepath.Join(repoDir, ".azedarach"), 0o755); err != nil {
		t.Fatalf("mkdir .azedarach: %v", err)
	}

	issuesClient := issues.NewClient(repoDir, slog.Default())
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
	worktreeRunner := &recoveringWorktreeRunner{
		worktreePath: worktreePath,
		branchName:   branch,
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
	if tmuxRunner.sendKeysCalls != 0 {
		t.Fatalf("send-keys calls = %d, want 0 when start_work=false", tmuxRunner.sendKeysCalls)
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

	done := make(chan protocol.ResponseEnvelope, 1)
	stopErr := make(chan error, 1)
	go func() {
		resp, runErr := daemon.handleSessionStopDirect(context.Background(), req)
		if runErr != nil {
			stopErr <- runErr
			return
		}
		done <- resp
	}()

	killObserved := false
	stopCompleted := false
	var stopResp protocol.ResponseEnvelope
	select {
	case <-tmuxRunner.killEntered:
		killObserved = true
	case runErr := <-stopErr:
		stopCompleted = true
		t.Fatalf("stop command failed before reconcile: %v", runErr)
	case stopResp = <-done:
		stopCompleted = true
		if !stopResp.OK {
			t.Fatalf("stop response before reconcile = %+v", stopResp)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for stop command to enter kill or complete")
	}

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

	if killObserved {
		close(tmuxRunner.killRelease)
	}

	if !stopCompleted {
		select {
		case runErr := <-stopErr:
			t.Fatalf("stop command failed: %v", runErr)
		case resp := <-done:
			if !resp.OK {
				t.Fatalf("stop response = %+v", resp)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("timed out waiting for stop command completion")
		}
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

func TestHandleSessionStopDirectDoesNotRequireFreshnessGate(t *testing.T) {
	const (
		projectID = "proj"
		issueID   = "az-1"
	)
	sessionID := naming.CanonicalSessionID(projectID, issueID)

	store := daemonstate.NewStore()
	if _, err := store.UpsertSession(projectID, sessionID, issueID, daemonstate.SessionStateAttached); err != nil {
		t.Fatalf("seed session store: %v", err)
	}

	tmuxRunner := newTestTmuxRunner(sessionID)
	close(tmuxRunner.killRelease)
	recorder := &runtimeReconcileRecorder{waitForCancel: true}

	daemon := &Daemon{
		cfg: Config{
			RepoDir:                ".",
			Logger:                 slog.Default(),
			RuntimeReconcileTimeout: 20 * time.Millisecond,
		},
		tmux:              tmux.NewClient(tmuxRunner, slog.Default()),
		session:           daemonhandlers.NewSessionHandler(store),
		sessionStore:      store,
		runtimeReconciler: recorder,
	}

	req := protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-stop-no-freshness-gate",
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
	if !resp.OK {
		t.Fatalf("stop response not OK: %+v", resp)
	}

	calls, projectIDs := recorder.snapshot()
	if calls != 0 {
		t.Fatalf("runtime reconcile calls = %d, want 0", calls)
	}
	if len(projectIDs) != 0 {
		t.Fatalf("runtime reconcile project ids = %v, want empty", projectIDs)
	}

	snapshot := store.ReadSnapshot(projectID)
	got, ok := snapshot.Sessions[sessionID]
	if !ok {
		t.Fatalf("missing session %q in snapshot", sessionID)
	}
	if got.State != daemonstate.SessionStateStopped {
		t.Fatalf("session state = %s, want %s", got.State, daemonstate.SessionStateStopped)
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

func TestPersistTmuxSessionProjectionSnapshotRemovesMissingSessions(t *testing.T) {
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
	if err := runtimeStateStore.UpsertSessionState(ctx, projectID, daemonstate.Session{
		ID:        staleSessionID,
		IssueID:   staleIssueID,
		State:     daemonstate.SessionStateAttached,
		StartedAt: &staleStartedAt,
		UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("seed stale projection session: %v", err)
	}
	if err := runtimeStateStore.UpsertSessionState(ctx, projectID, daemonstate.Session{
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

	if err := daemon.persistTmuxSessionRuntimeState(ctx, projectID, []tmux.SessionInfo{{Name: liveSessionID}}); err != nil {
		t.Fatalf("persistTmuxSessionRuntimeState: %v", err)
	}

	rows, err := runtimeStateStore.ListSessionStates(ctx, projectID)
	if err != nil {
		t.Fatalf("list projection sessions: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("projection sessions count = %d, want 1", len(rows))
	}
	if rows[0].ID != liveSessionID {
		t.Fatalf("remaining projection session = %s, want %s", rows[0].ID, liveSessionID)
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

	if err := runtimeStateStore.UpsertSessionState(context.Background(), projectID, daemonstate.Session{
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
	if err := runtimeStateStore.UpsertSessionState(context.Background(), projectID, daemonstate.Session{
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

func TestSessionAttachRefreshesRuntimeBeforeMutation(t *testing.T) {
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
	if calls != 1 {
		t.Fatalf("runtime reconcile calls = %d, want 1", calls)
	}
	if len(projectIDs) != 1 || projectIDs[0] != projectID {
		t.Fatalf("runtime reconcile project ids = %v, want [%s]", projectIDs, projectID)
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
}

func TestApplySessionLifecycleTransitionRefreshesDurableCacheBeforeTransition(t *testing.T) {
	const (
		projectID = "proj-refresh-transition"
		issueID   = "az-1"
	)
	sessionID := naming.CanonicalSessionID(projectID, issueID)

	store := daemonstate.NewStore()
	if _, err := store.UpsertSession(projectID, sessionID, issueID, daemonstate.SessionStateStarting); err != nil {
		t.Fatalf("seed starting session: %v", err)
	}
	if _, err := store.UpsertSession(projectID, sessionID, issueID, daemonstate.SessionStateAttached); err != nil {
		t.Fatalf("seed attached session: %v", err)
	}

	runtimeStateStore := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(t.TempDir(), "runtime.db"), slog.Default())
	t.Cleanup(func() {
		_ = runtimeStateStore.Close()
	})
	if err := runtimeStateStore.UpsertSessionState(context.Background(), projectID, daemonstate.Session{
		ID:        sessionID,
		IssueID:   issueID,
		State:     daemonstate.SessionStateStopped,
		UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("seed durable stopped session: %v", err)
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
}

func TestReconcilePublishesSessionProjectionEventsForRecovery(t *testing.T) {
	const (
		projectID = "proj"
		issueID   = "az-1"
	)

	store := daemonstate.NewStore()
	runtimeStateStore := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(t.TempDir(), "azedarach.db"), slog.Default())
	t.Cleanup(func() {
		_ = runtimeStateStore.Close()
	})
	sessionID := naming.CanonicalSessionID(projectID, issueID)
	if err := runtimeStateStore.UpsertSessionState(context.Background(), projectID, daemonstate.Session{
		ID:        sessionID,
		IssueID:   issueID,
		State:     daemonstate.SessionStateStopped,
		UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("seed durable stopped session: %v", err)
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
		hub:          publish.NewHub(32, 8, slog.Default()),
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

func TestReconcileRecoversFromDurableSessionProjection(t *testing.T) {
	const issueID = "az-1"

	repoDir := t.TempDir()
	projectID := filepath.Base(repoDir)
	sessionID := naming.CanonicalSessionID(repoDir, issueID)
	runtimeStateStore := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(t.TempDir(), "runtime.db"), slog.Default())
	t.Cleanup(func() {
		_ = runtimeStateStore.Close()
	})
	if err := runtimeStateStore.UpsertSessionState(context.Background(), projectID, daemonstate.Session{
		ID:        sessionID,
		IssueID:   issueID,
		State:     daemonstate.SessionStateAttached,
		UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("seed durable session projection: %v", err)
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
			repoDir: git.NewWorktreeManager(&testGitRunner{worktreePath: filepath.Join(filepath.Dir(repoDir), filepath.Base(repoDir)+"-"+issueID), branchName: "riordan/az-1/test"}, repoDir, slog.Default()),
		},
		worktreeManagersByProject: map[string]*git.WorktreeManager{
			projectID: git.NewWorktreeManager(&testGitRunner{worktreePath: filepath.Join(filepath.Dir(repoDir), filepath.Base(repoDir)+"-"+issueID), branchName: "riordan/az-1/test"}, repoDir, slog.Default()),
		},
		runtimeStoresByRoot: map[string]*daemonstate.RuntimeStateStore{
			repoDir: runtimeStateStore,
		},
	}

	result, err := daemon.reconcileTmuxAndDaemonSessions(context.Background(), projectID, issueID)
	if err != nil {
		t.Fatalf("reconcile with durable session projection: %v", err)
	}
	if result.RecreatedTmuxSessions != 1 {
		t.Fatalf("recreated tmux sessions = %d, want 1", result.RecreatedTmuxSessions)
	}

	tmuxRunner.mu.Lock()
	created := tmuxRunner.newSessionCalls
	sessionExists := tmuxRunner.sessions[sessionID]
	tmuxRunner.mu.Unlock()
	if created != 1 {
		t.Fatalf("new-session calls = %d, want 1", created)
	}
	if !sessionExists {
		t.Fatalf("expected tmux session %q to exist after reconcile", sessionID)
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
	if err := runtimeStateStore.UpsertSessionState(context.Background(), projectID, daemonstate.Session{
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

func collectSessionProjectionEvents(t *testing.T, ch <-chan protocol.EventEnvelope, count int) []protocol.EventEnvelope {
	t.Helper()
	events := make([]protocol.EventEnvelope, 0, count)
	deadline := time.After(2 * time.Second)
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
			SessionInitCommands: []string{
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
	if !strings.Contains(command, `"work on issue axt-123 (task): Verify startup behavior"`) {
		t.Fatalf("command = %q, want initial prompt argument", command)
	}
}

func TestBuildSessionLaunchCommandIncludesCodexHookOverrides(t *testing.T) {
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
	if !strings.Contains(command, "hooks.SessionStart=[{hooks=[{command=") {
		t.Fatalf("command = %q, want codex SessionStart hook override", command)
	}
	if !strings.Contains(command, "hooks.UserPromptSubmit=[{hooks=[{command=") {
		t.Fatalf("command = %q, want codex UserPromptSubmit hook override", command)
	}
	if !strings.Contains(command, "hooks.PreToolUse=[{hooks=[{command=") {
		t.Fatalf("command = %q, want codex PreToolUse hook override", command)
	}
	if !strings.Contains(command, "hooks.PostToolUse=[{hooks=[{command=") {
		t.Fatalf("command = %q, want codex PostToolUse hook override", command)
	}
	if !strings.Contains(command, "hooks.Stop=[{hooks=[{command=") {
		t.Fatalf("command = %q, want codex Stop hook override", command)
	}
	if !strings.Contains(command, `az notify --json session_start axt-123`) {
		t.Fatalf("command = %q, want codex session_start notify command with issue id", command)
	}
	if !strings.Contains(command, `az notify --json user_prompt_submit axt-123`) {
		t.Fatalf("command = %q, want codex user_prompt_submit notify command with issue id", command)
	}
	if !strings.Contains(command, `az notify --json pre_tool_use axt-123`) {
		t.Fatalf("command = %q, want codex pre_tool_use notify command with issue id", command)
	}
	if !strings.Contains(command, `az notify --json post_tool_use axt-123`) {
		t.Fatalf("command = %q, want codex post_tool_use notify command with issue id", command)
	}
	if !strings.Contains(command, `az notify --json stop axt-123`) {
		t.Fatalf("command = %q, want codex stop notify command with issue id", command)
	}
	if !strings.Contains(command, `--image "/tmp/a.png"`) {
		t.Fatalf("command = %q, want codex image argument for /tmp/a.png", command)
	}
	if !strings.Contains(command, `--image "/tmp/with space/image.png"`) {
		t.Fatalf("command = %q, want codex image argument for spaced path", command)
	}
	if !strings.Contains(command, `-- "work on issue axt-123 (task): Verify startup behavior"`) {
		t.Fatalf("command = %q, want codex prompt with option terminator", command)
	}
}

func TestBuildStartWorkPromptMatchesPrimeBootFormat(t *testing.T) {
	prompt := buildStartWorkPrompt("az-42", "task", "Fix startup shell")
	if !strings.Contains(prompt, "work on issue az-42 (task): Fix startup shell") {
		t.Fatalf("prompt = %q, want issue summary header", prompt)
	}
	if !strings.Contains(prompt, "Start by running `az prime`. Then continue the task using the context it prints without waiting for further instruction.") {
		t.Fatalf("prompt = %q, want az prime boot instructions", prompt)
	}
}

func TestBuildStartWorkPromptSanitizesControlCharsAndAngleBrackets(t *testing.T) {
	prompt := buildStartWorkPrompt("az-42", "task\n", "Fix <shell>\tselection")
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

func TestResolveSessionIssueFallsBackToFirstResult(t *testing.T) {
	tasks := []domain.Task{
		{ID: "bgr", Title: "first result"},
		{ID: "bfs", Title: "second result"},
	}

	got, ok := resolveSessionIssue(tasks, "missing")
	if !ok {
		t.Fatal("resolveSessionIssue returned not found, want fallback to first result")
	}
	if got.ID != "bgr" {
		t.Fatalf("resolved issue = %s, want bgr", got.ID)
	}
}

func TestBuildSessionLaunchCommandAddsDangerousSkipPermissionsInYoloMode(t *testing.T) {
	d := &Daemon{
		cfg: Config{
			CLITool:      "codex",
			SessionShell: "zsh",
		},
	}

	command := d.buildSessionLaunchCommand(protocol.DefaultProjectID, "axt-123", "codex-axt-123", true, nil, "")
	if !strings.Contains(command, "--dangerously-skip-permissions") {
		t.Fatalf("command = %q, want yolo skip-permissions flag", command)
	}
}

func TestRunWorktreeInitCommandsExecutesInWorktreeDirectory(t *testing.T) {
	worktree := t.TempDir()
	d := &Daemon{
		cfg: Config{
			SessionShell: "sh",
			WorktreeInitCommands: []string{
				"printf seeded > .worktree-init-test",
			},
		},
	}

	if err := d.runWorktreeInitCommands(context.Background(), protocol.DefaultProjectID, worktree); err != nil {
		t.Fatalf("runWorktreeInitCommands error: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(worktree, ".worktree-init-test"))
	if err != nil {
		t.Fatalf("read init marker: %v", err)
	}
	if strings.TrimSpace(string(data)) != "seeded" {
		t.Fatalf("init marker content = %q, want seeded", string(data))
	}
}

func TestRunWorktreeInitCommandsReturnsCommandFailure(t *testing.T) {
	d := &Daemon{
		cfg: Config{
			SessionShell: "sh",
			WorktreeInitCommands: []string{
				"exit 7",
			},
		},
	}

	err := d.runWorktreeInitCommands(context.Background(), protocol.DefaultProjectID, t.TempDir())
	if err == nil {
		t.Fatal("runWorktreeInitCommands error = nil, want failure")
	}
	if !strings.Contains(err.Error(), "exit 7") {
		t.Fatalf("error = %q, want failed command context", err.Error())
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
	if err := runtimeStateStore.UpsertSessionState(context.Background(), projectID, daemonstate.Session{
		ID:        sessionID,
		IssueID:   issueID,
		State:     daemonstate.SessionStateAttached,
		UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("seed projection session: %v", err)
	}

	tasks := []domain.Task{
		{ID: issueID, Title: "session elapsed should render", Type: domain.TypeTask},
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
	if err := runtimeStateStore.UpsertSessionState(context.Background(), projectID, daemonstate.Session{
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
	if err := runtimeStateStore.UpsertSessionState(context.Background(), projectID, daemonstate.Session{
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

	if err := d.persistTmuxSessionRuntimeState(context.Background(), projectID, []tmux.SessionInfo{{Name: sessionID}}); err != nil {
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
	}}); err != nil {
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
	}}); err != nil {
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

func TestEnrichTasksWithSessionStatePrefersProjectionStartedAtOverSnapshot(t *testing.T) {
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

	runtimeStateStore := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(t.TempDir(), "azedarach.db"), slog.Default())
	t.Cleanup(func() {
		_ = runtimeStateStore.Close()
	})

	startedAt := time.Date(2026, time.April, 1, 10, 0, 0, 0, time.UTC)
	if err := runtimeStateStore.UpsertSessionState(context.Background(), projectID, daemonstate.Session{
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
	if !enriched[0].Session.StartedAt.Equal(startedAt) {
		t.Fatalf("started_at = %v, want %v", enriched[0].Session.StartedAt, startedAt)
	}
}
