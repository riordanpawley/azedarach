package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	daemonhandlers "github.com/riordanpawley/azedarach/internal/daemon/handlers"
	"github.com/riordanpawley/azedarach/internal/daemon/publish"
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
		"axt-123",
		"codex-axt-123",
		false,
		[]string{"/tmp/a.png", "/tmp/with space/image.png", "   "},
		`work on issue axt-123 (task): Verify startup behavior`,
	)
	if !strings.Contains(command, "hooks.SessionStart=[{hooks=[{command=") {
		t.Fatalf("command = %q, want codex SessionStart hook override", command)
	}
	if !strings.Contains(command, "hooks.Stop=[{hooks=[{command=") {
		t.Fatalf("command = %q, want codex Stop hook override", command)
	}
	if !strings.Contains(command, "az notify user_prompt axt-123 codex-axt-123") {
		t.Fatalf("command = %q, want codex user_prompt notify command", command)
	}
	if !strings.Contains(command, "az notify session_end axt-123 codex-axt-123") {
		t.Fatalf("command = %q, want codex session_end notify command", command)
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

func TestBuildSessionLaunchCommandAddsDangerousSkipPermissionsInYoloMode(t *testing.T) {
	d := &Daemon{
		cfg: Config{
			CLITool:      "codex",
			SessionShell: "zsh",
		},
	}

	command := d.buildSessionLaunchCommand("axt-123", "codex-axt-123", true, nil, "")
	if !strings.Contains(command, "--dangerously-skip-permissions") {
		t.Fatalf("command = %q, want yolo skip-permissions flag", command)
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
	daemon := &Daemon{
		cfg: Config{
			Logger: slog.Default(),
		},
		hub:          hub,
		session:      daemonhandlers.NewSessionHandler(store),
		sessionStore: store,
		revision:     map[string]uint64{},
	}

	ch, cancel := hub.Subscribe(projectID, 0)
	defer cancel()

	startReq := protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-session.start",
		Kind:            protocol.EnvelopeKindCommand,
		Meta: protocol.Metadata{
			ProjectID: projectID,
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
	}
}

func TestReconcilePublishesSessionProjectionEventsForRecovery(t *testing.T) {
	const (
		projectID = "proj"
		issueID   = "az-2"
	)

	sessionID := naming.CanonicalSessionID(projectID, issueID)
	tmuxRunner := newTestTmuxRunner(sessionID)
	store := daemonstate.NewStore()
	hub := publish.NewHub(32, 8, slog.Default())
	daemon := &Daemon{
		cfg: Config{
			RepoDir: ".",
			Logger:  slog.Default(),
		},
		hub:          hub,
		tmux:         tmux.NewClient(tmuxRunner, slog.Default()),
		worktree:     git.NewWorktreeManager(&testGitRunner{worktreePath: "/tmp/proj-az-2", branchName: "riordan/az-2/test"}, ".", slog.Default()),
		sessionStore: store,
		revision:     map[string]uint64{},
	}

	ch, cancel := hub.Subscribe(projectID, 0)
	defer cancel()

	result, err := daemon.reconcileTmuxAndDaemonSessions(context.Background(), projectID, issueID)
	if err != nil {
		t.Fatalf("reconcile sessions: %v", err)
	}
	if result.AlignedDaemonSessions != 1 {
		t.Fatalf("aligned daemon sessions = %d, want 1", result.AlignedDaemonSessions)
	}

	events := collectSessionProjectionEvents(t, ch, 2)
	wantStates := []protocol.SessionLifecycleState{
		protocol.SessionLifecycleStateStarting,
		protocol.SessionLifecycleStateAttached,
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
		if body.Session.SessionID != sessionID {
			t.Fatalf("body session id = %s, want %s", body.Session.SessionID, sessionID)
		}
		if body.Session.IssueID != issueID {
			t.Fatalf("body issue id = %s, want %s", body.Session.IssueID, issueID)
		}
		if body.Session.State != wantStates[i] {
			t.Fatalf("body session state = %s, want %s", body.Session.State, wantStates[i])
		}
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
