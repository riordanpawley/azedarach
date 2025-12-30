package worktree

import (
	"context"
	"testing"
	"time"

	"github.com/riordanpawley/azedarach/internal/config"
	"github.com/riordanpawley/azedarach/internal/domain"
)

// mockTmuxClient is a mock implementation of the tmux client
type mockTmuxClient struct {
	sessions map[string]bool
}

func newMockTmuxClient() *mockTmuxClient {
	return &mockTmuxClient{
		sessions: make(map[string]bool),
	}
}

func (m *mockTmuxClient) NewSession(ctx context.Context, name string, workdir string) error {
	m.sessions[name] = true
	return nil
}

func (m *mockTmuxClient) HasSession(ctx context.Context, name string) (bool, error) {
	return m.sessions[name], nil
}

func (m *mockTmuxClient) KillSession(ctx context.Context, name string) error {
	delete(m.sessions, name)
	return nil
}

func (m *mockTmuxClient) SendKeys(ctx context.Context, name string, keys string) error {
	return nil
}

func (m *mockTmuxClient) CapturePane(ctx context.Context, name string, lines int) (string, error) {
	return "", nil
}

// mockGitClient is a mock implementation of the git client (placeholder)
type mockGitClient struct{}

// mockWorktreeManager is a mock implementation of the worktree manager
type mockWorktreeManager struct {
	worktrees map[string]*mockWorktree
}

type mockWorktree struct {
	Path   string
	Branch string
	BeadID string
}

func newMockWorktreeManager() *mockWorktreeManager {
	return &mockWorktreeManager{
		worktrees: make(map[string]*mockWorktree),
	}
}

func (m *mockWorktreeManager) Create(ctx context.Context, beadID string, baseBranch string) (*mockWorktree, error) {
	wt := &mockWorktree{
		Path:   "/tmp/test-" + beadID,
		Branch: "az/" + beadID,
		BeadID: beadID,
	}
	m.worktrees[beadID] = wt
	return wt, nil
}

func (m *mockWorktreeManager) Delete(ctx context.Context, beadID string) error {
	delete(m.worktrees, beadID)
	return nil
}

func TestSessionStatusConversion(t *testing.T) {
	tests := []struct {
		status   SessionStatus
		expected domain.SessionState
	}{
		{SessionIdle, domain.SessionIdle},
		{SessionActive, domain.SessionBusy},
		{SessionWaiting, domain.SessionWaiting},
		{SessionDone, domain.SessionDone},
		{SessionError, domain.SessionError},
	}

	for _, tt := range tests {
		t.Run(string(tt.status), func(t *testing.T) {
			result := ConvertStatus(tt.status)
			if result != tt.expected {
				t.Errorf("ConvertStatus(%v) = %v, want %v", tt.status, result, tt.expected)
			}
		})
	}
}

func TestDomainStateConversion(t *testing.T) {
	tests := []struct {
		state    domain.SessionState
		expected SessionStatus
	}{
		{domain.SessionIdle, SessionIdle},
		{domain.SessionBusy, SessionActive},
		{domain.SessionWaiting, SessionWaiting},
		{domain.SessionDone, SessionDone},
		{domain.SessionError, SessionError},
	}

	for _, tt := range tests {
		t.Run(string(tt.state), func(t *testing.T) {
			result := ConvertFromDomainState(tt.state)
			if result != tt.expected {
				t.Errorf("ConvertFromDomainState(%v) = %v, want %v", tt.state, result, tt.expected)
			}
		})
	}
}

func TestUpdateStatus(t *testing.T) {
	cfg := config.DefaultConfig()
	service := NewWorktreeSessionService(
		nil, // tmux
		nil, // git
		nil, // worktree
		"/tmp/test-project",
		cfg,
		nil, // logger (will use default)
	)

	// Create a test session
	beadID := "test-123"
	service.sessions[beadID] = &WorktreeSession{
		BeadID:       beadID,
		WorktreePath: "/tmp/test-123",
		TmuxSession:  beadID,
		Branch:       "az/test-123",
		Status:       SessionIdle,
		CreatedAt:    time.Now(),
	}

	// Update status
	service.UpdateStatus(beadID, SessionActive)

	// Verify status was updated
	session := service.sessions[beadID]
	if session.Status != SessionActive {
		t.Errorf("Expected status to be %v, got %v", SessionActive, session.Status)
	}
}

func TestList(t *testing.T) {
	cfg := config.DefaultConfig()
	service := NewWorktreeSessionService(
		nil, // tmux
		nil, // git
		nil, // worktree
		"/tmp/test-project",
		cfg,
		nil, // logger (will use default)
	)

	// Create test sessions
	sessions := []*WorktreeSession{
		{
			BeadID:       "test-1",
			WorktreePath: "/tmp/test-1",
			TmuxSession:  "test-1",
			Branch:       "az/test-1",
			Status:       SessionIdle,
			CreatedAt:    time.Now(),
		},
		{
			BeadID:       "test-2",
			WorktreePath: "/tmp/test-2",
			TmuxSession:  "test-2",
			Branch:       "az/test-2",
			Status:       SessionActive,
			CreatedAt:    time.Now(),
		},
	}

	for _, s := range sessions {
		service.sessions[s.BeadID] = s
	}

	// List sessions
	result := service.List()

	// Verify count
	if len(result) != 2 {
		t.Errorf("Expected 2 sessions, got %d", len(result))
	}

	// Verify sessions are copies (not references)
	for _, s := range result {
		if s == service.sessions[s.BeadID] {
			t.Errorf("Expected session copy, got reference")
		}
	}
}

func TestBuildClaudeCommand(t *testing.T) {
	tests := []struct {
		name     string
		cliTool  string
		yolo     bool
		expected string
	}{
		{
			name:     "default command without yolo",
			cliTool:  "claude",
			yolo:     false,
			expected: "claude",
		},
		{
			name:     "default command with yolo",
			cliTool:  "claude",
			yolo:     true,
			expected: "claude --yolo",
		},
		{
			name:     "custom CLI tool",
			cliTool:  "my-claude",
			yolo:     false,
			expected: "my-claude",
		},
		{
			name:     "empty CLI tool defaults to claude",
			cliTool:  "",
			yolo:     false,
			expected: "claude",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := config.DefaultConfig()
			cfg.CLITool = tt.cliTool

			service := NewWorktreeSessionService(
				nil, // tmux
				nil, // git
				nil, // worktree
				"/tmp/test-project",
				cfg,
				nil, // logger (will use default)
			)

			result := service.buildClaudeCommand(tt.yolo)
			if result != tt.expected {
				t.Errorf("buildClaudeCommand() = %q, want %q", result, tt.expected)
			}
		})
	}
}
