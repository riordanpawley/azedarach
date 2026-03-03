package session

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/riordanpawley/azedarach/internal/adapters/tmux"
	"github.com/riordanpawley/azedarach/internal/core/ops"
)

type State string

const (
	StateBusy    State = "busy"
	StateWaiting State = "waiting"
	StateDone    State = "done"
	StateError   State = "error"
	StatePaused  State = "paused"
)

type SessionInfo struct {
	Name             string
	Workdir          string
	Port             int
	State            State
	DevServerEnabled bool
}

type Manager struct {
	mu sync.Mutex

	tmuxClient   tmux.Client
	orchestrator *ops.Orchestrator
	ports        DeterministicPortAllocator

	sessions map[string]SessionInfo
}

func NewManager(tmuxClient tmux.Client, orchestrator *ops.Orchestrator, ports DeterministicPortAllocator) *Manager {
	if orchestrator == nil {
		orchestrator = ops.NewOrchestrator()
	}

	if ports.basePort == 0 {
		ports = NewDeterministicPortAllocator(defaultBasePort, defaultPortSpan)
	}

	return &Manager{
		tmuxClient:   tmuxClient,
		orchestrator: orchestrator,
		ports:        ports,
		sessions:     map[string]SessionInfo{},
	}
}

func (m *Manager) Start(ctx context.Context, name, workdir, issueKey string) (SessionInfo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	op, _, err := m.orchestrator.Queue(ops.Request{IssueKey: issueKey, IdempotencyKey: "session:start:" + name})
	if err != nil {
		return SessionInfo{}, fmt.Errorf("queue start operation: %w", err)
	}

	hasSession, err := m.tmuxClient.HasSession(ctx, name)
	if err != nil {
		return SessionInfo{}, fmt.Errorf("check session: %w", err)
	}
	if !hasSession {
		if err := m.tmuxClient.NewSession(ctx, name, workdir); err != nil {
			return SessionInfo{}, fmt.Errorf("start tmux session: %w", err)
		}
	}

	port := m.ports.Allocate(name)
	current := SessionInfo{
		Name:             name,
		Workdir:          workdir,
		Port:             port,
		State:            StateBusy,
		DevServerEnabled: false,
	}

	if existing, ok := m.sessions[name]; ok {
		current.DevServerEnabled = existing.DevServerEnabled
	}

	m.sessions[name] = current

	if _, err := m.orchestrator.Succeed(op.ID); err != nil {
		return SessionInfo{}, fmt.Errorf("complete start operation: %w", err)
	}

	return current, nil
}

func (m *Manager) Attach(ctx context.Context, name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	hasSession, err := m.tmuxClient.HasSession(ctx, name)
	if err != nil {
		return fmt.Errorf("check session before attach: %w", err)
	}
	if !hasSession {
		return fmt.Errorf("session %q does not exist", name)
	}

	return nil
}

func (m *Manager) Pause(_ context.Context, name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	session, ok := m.sessions[name]
	if !ok {
		return fmt.Errorf("session %q is not managed", name)
	}

	session.State = StatePaused
	m.sessions[name] = session
	return nil
}

func (m *Manager) Resume(_ context.Context, name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	session, ok := m.sessions[name]
	if !ok {
		return fmt.Errorf("session %q is not managed", name)
	}

	session.State = StateBusy
	m.sessions[name] = session
	return nil
}

func (m *Manager) Stop(ctx context.Context, name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	hasSession, err := m.tmuxClient.HasSession(ctx, name)
	if err != nil {
		return fmt.Errorf("check session before stop: %w", err)
	}
	if hasSession {
		if err := m.tmuxClient.KillSession(ctx, name); err != nil {
			return fmt.Errorf("stop tmux session: %w", err)
		}
	}

	if existing, ok := m.sessions[name]; ok {
		existing.State = StateDone
		existing.DevServerEnabled = false
		m.sessions[name] = existing
	}

	return nil
}

func (m *Manager) DetectState(ctx context.Context, name string) (State, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	session, known := m.sessions[name]
	if known && session.State == StatePaused {
		return StatePaused, nil
	}

	hasSession, err := m.tmuxClient.HasSession(ctx, name)
	if err != nil {
		return StateError, fmt.Errorf("check session before state detect: %w", err)
	}
	if !hasSession {
		if known {
			session.State = StateDone
			m.sessions[name] = session
		}
		return StateDone, nil
	}

	output, err := m.tmuxClient.CapturePane(ctx, name, 80)
	if err != nil {
		return StateError, fmt.Errorf("capture pane: %w", err)
	}

	state := detectFromOutput(output)
	if known {
		session.State = state
		m.sessions[name] = session
	}

	return state, nil
}

func (m *Manager) ToggleDevServer(_ context.Context, name string) (bool, int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	session := m.sessionOrDefault(name)
	session.DevServerEnabled = !session.DevServerEnabled
	m.sessions[name] = session

	return session.DevServerEnabled, session.Port, nil
}

func (m *Manager) RestartDevServer(_ context.Context, name string) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	session := m.sessionOrDefault(name)
	session.DevServerEnabled = true
	m.sessions[name] = session

	return session.Port, nil
}

func ReconcileOrphanSessions(knownSessionNames, liveSessionNames []string) []string {
	known := make(map[string]struct{}, len(knownSessionNames))
	for _, name := range knownSessionNames {
		known[name] = struct{}{}
	}

	orphans := make([]string, 0, len(liveSessionNames))
	for _, name := range liveSessionNames {
		if _, ok := known[name]; ok {
			continue
		}
		orphans = append(orphans, name)
	}

	sort.Strings(orphans)
	return orphans
}

func detectFromOutput(output string) State {
	text := strings.ToLower(strings.TrimSpace(output))
	if text == "" {
		return StateBusy
	}

	waitingHints := []string{"waiting", "[y/n]", "continue?", "press enter"}
	for _, hint := range waitingHints {
		if strings.Contains(text, hint) {
			return StateWaiting
		}
	}

	errorHints := []string{"error", "panic", "failed", "exception", "traceback"}
	for _, hint := range errorHints {
		if strings.Contains(text, hint) {
			return StateError
		}
	}

	doneHints := []string{"all done", "completed", "done", "success", "passed"}
	for _, hint := range doneHints {
		if strings.Contains(text, hint) {
			return StateDone
		}
	}

	return StateBusy
}

func (m *Manager) sessionOrDefault(name string) SessionInfo {
	if existing, ok := m.sessions[name]; ok {
		return existing
	}

	return SessionInfo{
		Name:             name,
		Port:             m.ports.Allocate(name),
		State:            StateBusy,
		DevServerEnabled: false,
	}
}
