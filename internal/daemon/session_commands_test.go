package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	appconfig "github.com/riordanpawley/azedarach/internal/config"
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

func (r *testTmuxRunner) hasSession(name string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.sessions[name]
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

type sessionStartTmuxRunner struct {
	sessions          map[string]bool
	windows           map[string]map[string]bool
	commands          [][]string
	sendKeysCalls     int
	sendKeysTargets   []string
	sendKeysPayloads  []string
	newSessionErr     error
	sendKeysErr       error
	sendKeysErrOnCall int
}

func newSessionStartTmuxRunner() *sessionStartTmuxRunner {
	return &sessionStartTmuxRunner{
		sessions: map[string]bool{},
		windows:  map[string]map[string]bool{},
	}
}

func (r *sessionStartTmuxRunner) Run(_ context.Context, args ...string) (string, error) {
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
		if r.newSessionErr != nil {
			return "", r.newSessionErr
		}
		r.sessions[args[3]] = true
		if r.windows[args[3]] == nil {
			r.windows[args[3]] = map[string]bool{"shell": true}
		}
		return "", nil
	case "kill-session":
		if len(args) < 3 {
			return "", errors.New("missing session name")
		}
		delete(r.sessions, args[2])
		delete(r.windows, args[2])
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
		r.windows[session][window] = true
		return "", nil
	case "send-keys":
		r.sendKeysCalls++
		if len(args) >= 4 {
			r.sendKeysTargets = append(r.sendKeysTargets, args[2])
			r.sendKeysPayloads = append(r.sendKeysPayloads, args[3])
		}
		if r.sendKeysErr != nil && (r.sendKeysErrOnCall == 0 || r.sendKeysCalls == r.sendKeysErrOnCall) {
			return "", r.sendKeysErr
		}
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
	if !strings.Contains(payload.Output, "Worktree setup warning:") {
		t.Fatalf("output = %q, want recovered worktree warning", payload.Output)
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
			wantPrimary:       "export issue resource env",
		},
		{
			name:              "launch failure",
			startWork:         true,
			sendKeysErr:       errors.New("launch failed"),
			sendKeysErrOnCall: 2,
			wantPrimary:       "launch failed",
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
			issuesClient := issues.NewClient(repoDir, slog.Default())
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

			worktreeRunner := &recoveringWorktreeRunner{
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

	issuesClient := issues.NewClient(repoDir, slog.Default())
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

	worktreeRunner := &recoveringWorktreeRunner{
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

func TestSessionStartUsesClosestAncestorWorktreeBranchAsBase(t *testing.T) {
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

func TestSessionStartDoesNotPersistTransitionWhenTmuxCreateFails(t *testing.T) {
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
		Title: "Fail tmux create",
		Type:  domain.TypeTask,
	})
	if err != nil {
		t.Fatalf("create issue: %v", err)
	}

	worktreeRunner := &recoveringWorktreeRunner{
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
}

func TestSessionResolveConflictCreatesDedicatedWindowAndLaunchesAgent(t *testing.T) {
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
	}
	worktreePath := filepath.Join(t.TempDir(), "project-"+issueID)
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
	if tmuxRunner.sendKeysCalls != 1 {
		t.Fatalf("send-keys calls = %d, want 1", tmuxRunner.sendKeysCalls)
	}
	if gotTarget := tmuxRunner.sendKeysTargets[0]; gotTarget != sessionID+":"+sessionConflictWindowName {
		t.Fatalf("send-keys target = %q, want %q", gotTarget, sessionID+":"+sessionConflictWindowName)
	}
	launchCommand := tmuxRunner.sendKeysPayloads[0]
	if !strings.Contains(launchCommand, "Resolve merge conflicts for issue "+issueID) ||
		!strings.Contains(launchCommand, `AZEDARACH_ISSUE_ID="`+issueID+`" codex`) ||
		!strings.Contains(launchCommand, "README.md") ||
		!strings.Contains(launchCommand, "main.go") ||
		!strings.Contains(launchCommand, "--dangerously-bypass-approvals-and-sandbox") {
		t.Fatalf("launch command missing conflict prompt or yolo flag: %s", launchCommand)
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
	case <-time.After(500 * time.Millisecond):
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
		case <-time.After(500 * time.Millisecond):
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
	if err := runtimeStateStore.UpsertSessionState(context.Background(), projectID, daemonstate.Session{
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
	if err := runtimeStateStore.UpsertSessionState(context.Background(), projectID, daemonstate.Session{
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
		cfg: Config{
			RepoDir:                 ".",
			RuntimeReconcileTimeout: 20 * time.Millisecond,
			Logger:                  slog.Default(),
		},
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

	select {
	case <-tmuxRunner.killEntered:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("timed out waiting for tmux kill to begin")
	}

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

	select {
	case runErr := <-stopErr:
		t.Fatalf("stop command failed: %v", runErr)
	case resp := <-done:
		if !resp.OK {
			t.Fatalf("stop response = %+v", resp)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("timed out waiting for stop completion")
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
	if !strings.Contains(strings.ToLower(resp.Error.Message), "record session stop intent") {
		t.Fatalf("stop error message = %q, want record session stop intent", resp.Error.Message)
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

func TestSessionPauseAcceptsAgentScopedTargetWhenParentTmuxSessionExists(t *testing.T) {
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
		if err := runtimeStateStore.UpsertSessionState(context.Background(), projectID, seed); err != nil {
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
		if err := runtimeStateStore.UpsertSessionState(context.Background(), projectID, seed); err != nil {
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
	if got := snapshot.Sessions[sessionID].State; got != daemonstate.SessionStateStopped {
		t.Fatalf("session state = %s, want %s", got, daemonstate.SessionStateStopped)
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
	if err := runtimeStateStore.UpsertSessionState(context.Background(), projectID, daemonstate.Session{
		ID:        sessionID,
		IssueID:   issueID,
		State:     daemonstate.SessionStateStopped,
		UpdatedAt: time.Now().UTC(),
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
	if err := runtimeStateStore.UpsertSessionState(context.Background(), projectID, daemonstate.Session{
		ID:        sessionID,
		IssueID:   issueID,
		State:     daemonstate.SessionStateAttached,
		UpdatedAt: time.Now().UTC(),
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
	repoDir := t.TempDir()
	projectID, err := appconfig.ProjectIDForRoot(repoDir)
	if err != nil {
		t.Fatalf("ProjectIDForRoot: %v", err)
	}
	issuesClient := issues.NewClient(repoDir, slog.Default())
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

func TestReconcileRecreatesObservedStoppedDesiredActiveSession(t *testing.T) {
	repoDir := t.TempDir()
	projectID, err := appconfig.ProjectIDForRoot(repoDir)
	if err != nil {
		t.Fatalf("ProjectIDForRoot: %v", err)
	}
	issuesClient := issues.NewClient(repoDir, slog.Default())
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
	if err := runtimeStateStore.UpsertSessionState(context.Background(), projectID, daemonstate.Session{
		ID:            sessionID,
		IssueID:       issueID,
		State:         daemonstate.SessionStateAttached,
		ObservedState: daemonstate.SessionStateStopped,
		UpdatedAt:     time.Now().UTC(),
	}); err != nil {
		t.Fatalf("seed observed stopped session projection: %v", err)
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
		t.Fatalf("reconcile observed stopped projection: %v", err)
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
	issuesClient := issues.NewClient(repoDir, slog.Default())
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
	if err := runtimeStateStore.UpsertSessionState(context.Background(), projectID, daemonstate.Session{
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
	issuesClient := issues.NewClient(repoDir, slog.Default())
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

func TestListTmuxSessionsCacheFirstUsesObservedStateForActiveSet(t *testing.T) {
	const projectID = "proj-observed-runtime"

	runtimeStateStore := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(t.TempDir(), "projections.db"), slog.Default())
	t.Cleanup(func() {
		_ = runtimeStateStore.Close()
	})

	stoppedObservedID := naming.CanonicalSessionID(projectID, "az-1")
	attachedObservedID := naming.CanonicalSessionID(projectID, "az-2")
	now := time.Now().UTC()
	if err := runtimeStateStore.UpsertSessionState(context.Background(), projectID, daemonstate.Session{
		ID:            stoppedObservedID,
		IssueID:       "az-1",
		State:         daemonstate.SessionStateAttached,
		ObservedState: daemonstate.SessionStateStopped,
		UpdatedAt:     now,
	}); err != nil {
		t.Fatalf("seed stopped-observed session: %v", err)
	}
	if err := runtimeStateStore.UpsertSessionState(context.Background(), projectID, daemonstate.Session{
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

	issuesClient := issues.NewClient(repoDir, slog.Default())
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
	if err := runtimeStateStore.UpsertSessionState(ctx, projectID, daemonstate.Session{
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

	issuesClient := issues.NewClient(repoDir, slog.Default())
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
	if err := runtimeStateStore.UpsertSessionState(ctx, projectID, daemonstate.Session{
		ID:            busySessionID + ".pane-%1",
		IssueID:       busyIssueID,
		State:         daemonstate.SessionStateRunning,
		ObservedState: daemonstate.SessionStateRunning,
		UpdatedAt:     now,
	}); err != nil {
		t.Fatalf("seed busy hook activity: %v", err)
	}
	if err := runtimeStateStore.UpsertSessionState(ctx, projectID, daemonstate.Session{
		ID:            idleSessionID + ".pane-%2",
		IssueID:       idleIssueID,
		State:         daemonstate.SessionStatePaused,
		ObservedState: daemonstate.SessionStatePaused,
		UpdatedAt:     now,
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

	issuesClient := issues.NewClient(repoDir, slog.Default())
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
	if err := runtimeStateStore.UpsertSessionState(ctx, projectID, daemonstate.Session{
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

	issuesClient := issues.NewClient(repoDir, slog.Default())
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
	if err := runtimeStateStore.UpsertSessionState(ctx, projectID, daemonstate.Session{
		ID:            sessionID,
		IssueID:       issueID,
		State:         daemonstate.SessionStateAttached,
		ObservedState: daemonstate.SessionStateStopped,
		UpdatedAt:     now,
	}); err != nil {
		t.Fatalf("seed stale session projection: %v", err)
	}
	if err := runtimeStateStore.UpsertSessionState(ctx, projectID, daemonstate.Session{
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

	issuesClient := issues.NewClient(repoDir, slog.Default())
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
	if err := runtimeStateStore.UpsertSessionState(ctx, projectID, daemonstate.Session{
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

func TestBuildSessionLaunchCommandDoesNotSerializeSideEffectCommandsBeforeToolLaunch(t *testing.T) {
	d := &Daemon{
		cfg: Config{
			CLITool:      "codex",
			SessionShell: "zsh",
			SessionInitCommands: []string{
				"direnv allow",
			},
			SessionSideEffectCommands: []string{
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
		`work on issue cnb (feature): Add non-blocking session side-effect commands`,
	)
	if !strings.Contains(command, "direnv allow;") {
		t.Fatalf("command = %q, want blocking init command before launch", command)
	}
	if strings.Contains(command, "pnpm type-check") {
		// Side-effect commands must not be serialized into the AI pane launch command.
		t.Fatalf("command = %q, want side-effect command excluded from AI launch shell", command)
	}
	if !strings.Contains(command, `AZEDARACH_ISSUE_ID="cnb" codex`) {
		t.Fatalf("command = %q, want foreground AI tool launch", command)
	}
}

func TestStartSessionSideEffectCommandsUsesSeparateTmuxWindow(t *testing.T) {
	tmuxRunner := newSessionStartTmuxRunner()
	tmuxRunner.sessions["cnb"] = true
	tmuxRunner.windows["cnb"] = map[string]bool{"shell": true}
	d := &Daemon{
		cfg: Config{
			Logger: slog.Default(),
			SessionSideEffectCommands: []string{
				"pnpm type-check",
				"echo $AZEDARACH_ISSUE_ID",
			},
		},
		tmux: tmux.NewClient(tmuxRunner, slog.Default()),
	}

	d.startSessionSideEffectCommands(context.Background(), protocol.DefaultProjectID, "cnb", "cnb", "/tmp/worktree")

	if !tmuxRunner.windows["cnb"][sessionSideEffectWindowName] {
		t.Fatalf("windows = %+v, want %q window", tmuxRunner.windows["cnb"], sessionSideEffectWindowName)
	}
	if len(tmuxRunner.sendKeysTargets) != 1 || tmuxRunner.sendKeysTargets[0] != "cnb:"+sessionSideEffectWindowName {
		t.Fatalf("sendKeysTargets = %+v, want cnb:%s", tmuxRunner.sendKeysTargets, sessionSideEffectWindowName)
	}
	payload := tmuxRunner.sendKeysPayloads[0]
	if !strings.Contains(payload, "export AZEDARACH_PROJECT_ID=") ||
		!strings.Contains(payload, "AZEDARACH_ISSUE_ID=") ||
		!strings.Contains(payload, "AZEDARACH_SESSION_ID=") {
		t.Fatalf("payload = %q, want exported AZEDARACH context before side effects", payload)
	}
	if !strings.Contains(payload, "session side-effect[1] log: .azedarach/session-side-effects/cnb/cnb/001.log") {
		t.Fatalf("payload = %q, want discoverable side-effect log path", payload)
	}
	if !strings.Contains(payload, "pnpm type-check") {
		t.Fatalf("payload = %q, want side-effect command", payload)
	}
	if !strings.Contains(payload, "tee -a '.azedarach/session-side-effects/cnb/cnb/001.log'") {
		t.Fatalf("payload = %q, want log tee for side-effect output", payload)
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
		`az ai hook run`,
		`az notify`,
	} {
		if strings.Contains(command, mustNotContain) {
			t.Fatalf("command = %q, must NOT contain %q (hook injection removed; rely on .codex/hooks.json)", command, mustNotContain)
		}
	}

	// Surrounding launch behaviour stays intact: env prefix, image flags, and
	// prompt with option terminator.
	if !strings.Contains(command, `AZEDARACH_ISSUE_ID="axt-123"`) {
		t.Fatalf("command = %q, want AZEDARACH_ISSUE_ID env exported for the launched codex", command)
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

func TestBuildSessionLaunchCommandEscapesPromptCommandSubstitutionForCodex(t *testing.T) {
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
		t.Fatalf("command = %q, want backticks escaped in prompt to avoid shell command substitution", command)
	}
	if !strings.Contains(command, "\\`az orchestrate status --root <issue-id>\\`") {
		t.Fatalf("command = %q, want escaped backticks in prompt", command)
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
	if !strings.Contains(prompt, "Report coordination state with `az mail send --parent <parent-issue> --issue az-42 --type worker-progress|worker-blocked|worker-integration-ready --body \"...\"`; do not use `az orchestrate message` for your own status") {
		t.Fatalf("prompt = %q, want safe worker reporting guidance", prompt)
	}
	for _, eventType := range []string{"worker-progress", "worker-blocked", "worker-integration-ready"} {
		if !strings.Contains(prompt, eventType) {
			t.Fatalf("prompt = %q, want mailbox event type %s", prompt, eventType)
		}
	}
	if !strings.Contains(prompt, "worker-ready and worker-complete are accepted only as legacy aliases for worker-integration-ready") {
		t.Fatalf("prompt = %q, want legacy worker-ready/worker-complete alias guidance", prompt)
	}
	if !strings.Contains(prompt, "Use `in_progress` while actively working and `in_review` when complete and ready for orchestrator integration") {
		t.Fatalf("prompt = %q, want worker status semantics", prompt)
	}
	if !strings.Contains(prompt, "Report blockers via dependency edges or worker-blocked mailbox events, not by setting `in_review`") {
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
	if !strings.Contains(prompt, "Use `in_progress` while actively working, `in_review` when complete and awaiting review/integration") {
		t.Fatalf("prompt = %q, want contributor status semantics", prompt)
	}
	if !strings.Contains(prompt, "Represent blocked work with dependency edges and notes, not by using `in_review`") {
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
	if !strings.Contains(prompt, "Do not use `--once` for orchestration monitoring") {
		t.Fatalf("prompt = %q, want --once diagnostic warning", prompt)
	}
	if !strings.Contains(prompt, "az orchestrate complete-check --root <issue-id>") {
		t.Fatalf("prompt = %q, want complete-check instruction", prompt)
	}
	if !strings.Contains(prompt, "az orchestrate message --root <issue-id> --issue <worker-issue> --body \"...\"") {
		t.Fatalf("prompt = %q, want active worker message instruction", prompt)
	}
	if !strings.Contains(prompt, "bare `az mail send` is durable mailbox-only") {
		t.Fatalf("prompt = %q, want passive mailbox warning", prompt)
	}
	if !strings.Contains(prompt, "workers reporting their own status should use `az mail send --parent <issue-id> --issue <worker-issue> --type worker-progress|worker-blocked|worker-integration-ready --body \"...\"`") {
		t.Fatalf("prompt = %q, want safe worker reporting guidance", prompt)
	}
	if !strings.Contains(prompt, "Trust hook-backed `activity=busy|idle` for worker idleness checks") {
		t.Fatalf("prompt = %q, want bounded tmux observation guidance", prompt)
	}
	if !strings.Contains(prompt, "If activity is `unknown`, check hooks with `az ai status --target=auto` and install/update with `az ai install --target=auto`") {
		t.Fatalf("prompt = %q, want hook status/install fallback guidance", prompt)
	}
	if !strings.Contains(prompt, "Do not poll tmux panes on a fixed interval") {
		t.Fatalf("prompt = %q, want tmux polling guardrail", prompt)
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
	if !strings.Contains(prompt, "Treat `in_review` workers as ready for orchestrator validation") {
		t.Fatalf("prompt = %q, want in-review integration guidance", prompt)
	}
	if !strings.Contains(prompt, "close accepted worker issues with `az issue close --id <issue-id>`") {
		t.Fatalf("prompt = %q, want issue close completion guidance", prompt)
	}
}

func TestSessionMessagePastesTextAndSubmitsActiveIssueSession(t *testing.T) {
	projectID := protocol.DefaultProjectID
	issueID := naming.IssueID("az-42")
	repoDir := "/repo"
	sessionID := naming.CanonicalSessionIDForIssue(repoDir, issueID).String()
	tmuxRunner := newSessionStartTmuxRunner()
	tmuxRunner.sessions[sessionID] = true
	daemon := &Daemon{
		cfg:  Config{RepoDir: repoDir, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))},
		tmux: tmux.NewClient(tmuxRunner, slog.New(slog.NewTextHandler(io.Discard, nil))),
	}
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
		t.Fatalf("response not OK: %+v", resp)
	}
	wantCommands := [][]string{
		{"has-session", "-t", sessionID},
		{"set-buffer", "-b", "azedarach-message-" + sessionID, "Orchestrator says proceed now.\n\nKeep notes current."},
		{"paste-buffer", "-dp", "-b", "azedarach-message-" + sessionID, "-t", sessionID},
		{"send-keys", "-t", sessionID, "Enter"},
	}
	if !reflect.DeepEqual(tmuxRunner.commands, wantCommands) {
		t.Fatalf("tmux commands = %#v, want %#v", tmuxRunner.commands, wantCommands)
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

func TestBuildSessionLaunchCommandAddsDangerousSkipPermissionsFromConfigAcrossTools(t *testing.T) {
	tests := []struct {
		name string
		tool string
		want string
	}{
		{name: "claude", tool: "claude", want: "--dangerously-skip-permissions"},
		{name: "codex", tool: "codex", want: "--dangerously-bypass-approvals-and-sandbox"},
		{name: "opencode", tool: "opencode", want: "--dangerously-skip-permissions"},
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

func TestRunWorktreeInitCommandsMissingCommandReturnsFailure(t *testing.T) {
	d := &Daemon{
		cfg: Config{
			SessionShell: "sh",
			WorktreeInitCommands: []string{
				"definitely-missing-command-xyz",
			},
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

func TestIssueResourceCommandsReceiveContextAndConfiguredEnv(t *testing.T) {
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

func TestIssueResourceCommandsUseRootPathWhenWorktreeMissing(t *testing.T) {
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

func TestSessionStartInitFailureCleansUpNewWorktree(t *testing.T) {
	ctx := context.Background()
	repoDir := t.TempDir()
	projectID := "proj"
	if err := os.MkdirAll(filepath.Join(repoDir, ".azedarach"), 0o755); err != nil {
		t.Fatalf("mkdir .azedarach: %v", err)
	}

	issuesClient := issues.NewClient(repoDir, slog.Default())
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
	if startRows[0].State != daemonstate.SessionStateStarting || startRows[0].ObservedState != daemonstate.SessionStateStarting {
		t.Fatalf("runtime rows after start = %+v, want desired and observed starting", startRows)
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
		if body.Runtime.Projection.Session.State != protocol.SessionLifecycleStateStarting {
			runtimeRows, err := runtimeStateStore.ListSessionStates(context.Background(), projectID)
			if err != nil {
				t.Fatalf("body runtime session state = %s, want observed %s (load runtime rows failed: %v)", body.Runtime.Projection.Session.State, protocol.SessionLifecycleStateStarting, err)
			}
			t.Fatalf("body runtime session state = %s, want observed %s (runtime rows = %+v)", body.Runtime.Projection.Session.State, protocol.SessionLifecycleStateStarting, runtimeRows)
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
	if enriched[0].Session.Activity != "unknown" || enriched[0].Session.ActivitySource != "none" {
		t.Fatalf("activity = %s/%s, want unknown/none", enriched[0].Session.Activity, enriched[0].Session.ActivitySource)
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
	if err := runtimeStateStore.UpsertSessionState(context.Background(), projectID, daemonstate.Session{
		ID:            naming.CanonicalSessionID(projectID, busyID),
		IssueID:       busyID,
		State:         daemonstate.SessionStateRunning,
		ObservedState: daemonstate.SessionStateRunning,
		UpdatedAt:     now,
	}); err != nil {
		t.Fatalf("seed busy session: %v", err)
	}
	if err := runtimeStateStore.UpsertSessionState(context.Background(), projectID, daemonstate.Session{
		ID:            naming.CanonicalSessionID(projectID, idleID),
		IssueID:       idleID,
		State:         daemonstate.SessionStatePaused,
		ObservedState: daemonstate.SessionStatePaused,
		UpdatedAt:     now,
	}); err != nil {
		t.Fatalf("seed idle session: %v", err)
	}
	if err := runtimeStateStore.UpsertSessionState(context.Background(), projectID, daemonstate.Session{
		ID:            busySessionID,
		IssueID:       busyID,
		State:         daemonstate.SessionStateRunning,
		ObservedState: daemonstate.SessionStateRunning,
		UpdatedAt:     now,
	}); err != nil {
		t.Fatalf("seed busy hook session: %v", err)
	}
	if err := runtimeStateStore.UpsertSessionState(context.Background(), projectID, daemonstate.Session{
		ID:            idleSessionID,
		IssueID:       idleID,
		State:         daemonstate.SessionStatePaused,
		ObservedState: daemonstate.SessionStatePaused,
		UpdatedAt:     now,
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
	if err := runtimeStateStore.UpsertSessionState(context.Background(), projectID, daemonstate.Session{
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

func TestPersistTmuxSessionRuntimeStateKeepsAgentHookActivityWhenParentTmuxSessionLives(t *testing.T) {
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
	if err := runtimeStateStore.UpsertSessionState(context.Background(), projectID, daemonstate.Session{
		ID:            sessionID,
		IssueID:       issueID,
		State:         daemonstate.SessionStateRunning,
		ObservedState: daemonstate.SessionStateRunning,
		UpdatedAt:     now,
	}); err != nil {
		t.Fatalf("seed parent session: %v", err)
	}
	if err := runtimeStateStore.UpsertSessionState(context.Background(), projectID, daemonstate.Session{
		ID:            agentSessionID,
		IssueID:       issueID,
		State:         daemonstate.SessionStateRunning,
		ObservedState: daemonstate.SessionStateRunning,
		UpdatedAt:     now,
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

	if err := d.persistTmuxSessionRuntimeState(context.Background(), projectID, []tmux.SessionInfo{{Name: sessionID}}); err != nil {
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
	if enriched[0].Session.State != domain.SessionBusy {
		t.Fatalf("session state = %v, want %v", enriched[0].Session.State, domain.SessionBusy)
	}
	if !enriched[0].Session.StartedAt.Equal(snapshotStartedAt) {
		t.Fatalf("started_at = %v, want %v", enriched[0].Session.StartedAt, snapshotStartedAt)
	}
}

func TestEnrichTasksWithSessionStateIgnoresAgentScopedRowsWithoutParentSession(t *testing.T) {
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
	if err := runtimeStateStore.UpsertSessionState(context.Background(), projectID, daemonstate.Session{
		ID:            "ciw.pane-1",
		IssueID:       issueID,
		State:         daemonstate.SessionStatePaused,
		ObservedState: daemonstate.SessionStatePaused,
		StartedAt:     &startedAt,
		UpdatedAt:     startedAt,
	}); err != nil {
		t.Fatalf("seed paused agent session: %v", err)
	}
	if err := runtimeStateStore.UpsertSessionState(context.Background(), projectID, daemonstate.Session{
		ID:                "ciw.pane-2",
		IssueID:           issueID,
		State:             daemonstate.SessionStateAttached,
		ObservedState:     daemonstate.SessionStateAttached,
		TmuxAttachedCount: 1,
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
		t.Fatalf("session = %+v, want nil because agent-scoped rows are not task lifecycle sessions", enriched[0].Session)
	}
}

func TestEnrichTasksWithSessionStateIgnoresAgentScopedRowsInFavorOfParentContainer(t *testing.T) {
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
	if err := runtimeStateStore.UpsertSessionState(context.Background(), projectID, daemonstate.Session{
		ID:            containerSessionID,
		IssueID:       issueID,
		State:         daemonstate.SessionStateAttached,
		ObservedState: daemonstate.SessionStateAttached,
		StartedAt:     &startedAt,
		UpdatedAt:     startedAt,
	}); err != nil {
		t.Fatalf("seed tmux container session: %v", err)
	}
	if err := runtimeStateStore.UpsertSessionState(context.Background(), projectID, daemonstate.Session{
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
	if enriched[0].Session.State != domain.SessionBusy {
		t.Fatalf("session state = %v, want %v", enriched[0].Session.State, domain.SessionBusy)
	}
	if enriched[0].Session.TotalCount != 1 || enriched[0].Session.ActiveCount != 1 || enriched[0].Session.PausedCount != 0 {
		t.Fatalf("session aggregate counts = total %d active %d paused %d, want 1/1/0", enriched[0].Session.TotalCount, enriched[0].Session.ActiveCount, enriched[0].Session.PausedCount)
	}
}
