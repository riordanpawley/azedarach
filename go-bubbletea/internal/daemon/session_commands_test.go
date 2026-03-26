package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	daemonhandlers "github.com/riordanpawley/azedarach/internal/daemon/handlers"
	daemonstate "github.com/riordanpawley/azedarach/internal/daemon/state"
	"github.com/riordanpawley/azedarach/internal/naming"
	"github.com/riordanpawley/azedarach/internal/services/git"
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

func TestReconcileSkipsRecreateWhileStopInProgress(t *testing.T) {
	const (
		projectID = "proj"
		issueID   = "az-1"
	)

	store := daemonstate.NewStore()
	sessionID := naming.CanonicalSessionID(projectID, issueID)
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
		worktree:     git.NewWorktreeManager(&testGitRunner{worktreePath: "/tmp/proj-az-1", branchName: "riordan/az-1/test"}, ".", slog.Default()),
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
		RequestID:       "req-stop",
		Kind:            protocol.EnvelopeKindCommand,
		Command:         "session.stop",
		Body:            body,
		Meta: protocol.Metadata{
			ProjectID: projectID,
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

	<-tmuxRunner.killEntered

	result, err := daemon.reconcileTmuxAndDaemonSessions(context.Background(), projectID, issueID)
	if err != nil {
		t.Fatalf("reconcile during stop: %v", err)
	}
	if result.RecreatedTmuxSessions != 0 {
		t.Fatalf("recreated tmux sessions = %d, want 0", result.RecreatedTmuxSessions)
	}
	if tmuxRunner.newSessionCalls != 0 {
		t.Fatalf("new-session calls = %d, want 0", tmuxRunner.newSessionCalls)
	}

	close(tmuxRunner.killRelease)

	select {
	case runErr := <-stopErr:
		t.Fatalf("stop command failed: %v", runErr)
	case resp := <-done:
		if !resp.OK {
			t.Fatalf("stop response = %+v", resp)
		}
	}

	snapshot := store.ReadSnapshot(projectID)
	if got := snapshot.Sessions[sessionID].State; got != daemonstate.SessionStateStopped {
		t.Fatalf("session state after stop = %s, want %s", got, daemonstate.SessionStateStopped)
	}
}
