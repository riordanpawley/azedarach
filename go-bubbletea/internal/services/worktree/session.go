package worktree

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"sync"
	"time"

	"github.com/riordanpawley/azedarach/internal/config"
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/services/git"
	"github.com/riordanpawley/azedarach/internal/services/tmux"
)

// SessionStatus represents the current status of a worktree session
type SessionStatus string

const (
	SessionIdle    SessionStatus = "idle"
	SessionActive  SessionStatus = "active"
	SessionWaiting SessionStatus = "waiting"
	SessionDone    SessionStatus = "done"
	SessionError   SessionStatus = "error"
)

// WorktreeSession represents an active Claude session in a git worktree
type WorktreeSession struct {
	BeadID       string
	WorktreePath string
	TmuxSession  string
	Branch       string
	Status       SessionStatus
	CreatedAt    time.Time
}

// WorktreeSessionService manages Claude sessions in git worktrees
type WorktreeSessionService struct {
	tmux        *tmux.Client
	git         *git.Client
	worktree    *git.WorktreeManager
	projectRoot string
	projectName string
	config      *config.Config
	sessions    map[string]*WorktreeSession
	mu          sync.RWMutex
	logger      *slog.Logger
}

// NewWorktreeSessionService creates a new worktree session service
func NewWorktreeSessionService(
	tmuxClient *tmux.Client,
	gitClient *git.Client,
	worktreeManager *git.WorktreeManager,
	projectRoot string,
	cfg *config.Config,
	logger *slog.Logger,
) *WorktreeSessionService {
	if logger == nil {
		logger = slog.Default()
	}

	projectName := filepath.Base(projectRoot)

	return &WorktreeSessionService{
		tmux:        tmuxClient,
		git:         gitClient,
		worktree:    worktreeManager,
		projectRoot: projectRoot,
		projectName: projectName,
		config:      cfg,
		sessions:    make(map[string]*WorktreeSession),
		logger:      logger,
	}
}

// Create creates a new worktree and tmux session for the given bead ID
func (s *WorktreeSessionService) Create(ctx context.Context, beadID, branch string) (*WorktreeSession, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.logger.Info("creating worktree session", "beadID", beadID, "branch", branch)

	// Check if session already exists
	if existing, ok := s.sessions[beadID]; ok {
		s.logger.Warn("session already exists", "beadID", beadID)
		return existing, nil
	}

	// Create worktree using git worktree manager
	worktree, err := s.worktree.Create(ctx, beadID, branch)
	if err != nil {
		return nil, fmt.Errorf("failed to create worktree: %w", err)
	}

	s.logger.Debug("worktree created", "path", worktree.Path, "branch", worktree.Branch)

	// Create tmux session with the bead ID as the session name
	tmuxSessionName := beadID
	if err := s.tmux.NewSession(ctx, tmuxSessionName, worktree.Path); err != nil {
		// Clean up worktree on tmux session creation failure
		if delErr := s.worktree.Delete(ctx, beadID); delErr != nil {
			s.logger.Error("failed to clean up worktree after tmux error", "beadID", beadID, "error", delErr)
		}
		return nil, fmt.Errorf("failed to create tmux session: %w", err)
	}

	s.logger.Debug("tmux session created", "name", tmuxSessionName)

	// Create session record
	session := &WorktreeSession{
		BeadID:       beadID,
		WorktreePath: worktree.Path,
		TmuxSession:  tmuxSessionName,
		Branch:       worktree.Branch,
		Status:       SessionIdle,
		CreatedAt:    time.Now(),
	}

	s.sessions[beadID] = session

	s.logger.Info("worktree session created successfully", "beadID", beadID)

	return session, nil
}

// Start starts Claude in the tmux session
// If yolo is true, Claude will run in "YOLO mode" with auto-approvals
func (s *WorktreeSessionService) Start(ctx context.Context, beadID string, yolo bool) error {
	s.mu.RLock()
	session, exists := s.sessions[beadID]
	s.mu.RUnlock()

	if !exists {
		return fmt.Errorf("session not found: %s", beadID)
	}

	s.logger.Info("starting Claude session", "beadID", beadID, "yolo", yolo)

	// Build Claude command
	claudeCmd := s.buildClaudeCommand(yolo)

	// Send command to tmux session
	// We need to wrap it in an interactive shell to ensure direnv loads
	shell := s.config.Session.Shell
	wrappedCmd := fmt.Sprintf("%s -i -c '%s; exec %s'", shell, claudeCmd, shell)

	if err := s.tmux.SendKeys(ctx, session.TmuxSession, wrappedCmd); err != nil {
		return fmt.Errorf("failed to send keys to tmux session: %w", err)
	}

	// Update session status
	s.mu.Lock()
	session.Status = SessionActive
	s.mu.Unlock()

	s.logger.Info("Claude session started", "beadID", beadID)

	return nil
}

// Attach attaches to the tmux session for the given bead ID
// This is a blocking operation that takes over the terminal
func (s *WorktreeSessionService) Attach(ctx context.Context, beadID string) error {
	s.mu.RLock()
	session, exists := s.sessions[beadID]
	s.mu.RUnlock()

	if !exists {
		return fmt.Errorf("session not found: %s", beadID)
	}

	s.logger.Info("attaching to tmux session", "beadID", beadID)

	// Attach to the tmux session (blocking operation)
	if err := s.tmux.AttachSession(ctx, session.TmuxSession); err != nil {
		return fmt.Errorf("failed to attach to tmux session: %w", err)
	}

	return nil
}

// Stop stops the Claude session by sending Ctrl+C to the tmux session
func (s *WorktreeSessionService) Stop(ctx context.Context, beadID string) error {
	s.mu.RLock()
	session, exists := s.sessions[beadID]
	s.mu.RUnlock()

	if !exists {
		return fmt.Errorf("session not found: %s", beadID)
	}

	s.logger.Info("stopping Claude session", "beadID", beadID)

	// Send Ctrl+C to interrupt the session
	if err := s.tmux.SendKeys(ctx, session.TmuxSession, "C-c"); err != nil {
		return fmt.Errorf("failed to send interrupt to tmux session: %w", err)
	}

	// Update session status
	s.mu.Lock()
	session.Status = SessionIdle
	s.mu.Unlock()

	s.logger.Info("Claude session stopped", "beadID", beadID)

	return nil
}

// Delete removes the worktree and kills the tmux session
func (s *WorktreeSessionService) Delete(ctx context.Context, beadID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.logger.Info("deleting worktree session", "beadID", beadID)

	session, exists := s.sessions[beadID]
	if !exists {
		return fmt.Errorf("session not found: %s", beadID)
	}

	// Kill tmux session
	if err := s.tmux.KillSession(ctx, session.TmuxSession); err != nil {
		s.logger.Warn("failed to kill tmux session", "beadID", beadID, "error", err)
		// Continue with cleanup even if tmux session kill fails
	}

	// Remove worktree
	if err := s.worktree.Delete(ctx, beadID); err != nil {
		return fmt.Errorf("failed to delete worktree: %w", err)
	}

	// Remove session from map
	delete(s.sessions, beadID)

	s.logger.Info("worktree session deleted successfully", "beadID", beadID)

	return nil
}

// GetStatus returns the current status of the session
func (s *WorktreeSessionService) GetStatus(ctx context.Context, beadID string) (SessionStatus, error) {
	s.mu.RLock()
	session, exists := s.sessions[beadID]
	s.mu.RUnlock()

	if !exists {
		return "", fmt.Errorf("session not found: %s", beadID)
	}

	// Check if tmux session still exists
	hasSession, err := s.tmux.HasSession(ctx, session.TmuxSession)
	if err != nil {
		return "", fmt.Errorf("failed to check tmux session: %w", err)
	}

	if !hasSession {
		// Session was killed externally, update status
		s.mu.Lock()
		session.Status = SessionIdle
		s.mu.Unlock()
	}

	return session.Status, nil
}

// List returns all active worktree sessions
func (s *WorktreeSessionService) List() []*WorktreeSession {
	s.mu.RLock()
	defer s.mu.RUnlock()

	sessions := make([]*WorktreeSession, 0, len(s.sessions))
	for _, session := range s.sessions {
		// Create a copy to avoid returning internal pointers
		sessionCopy := *session
		sessions = append(sessions, &sessionCopy)
	}

	return sessions
}

// UpdateStatus updates the status of a session
// This is typically called by the monitor when it detects state changes
func (s *WorktreeSessionService) UpdateStatus(beadID string, status SessionStatus) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if session, exists := s.sessions[beadID]; exists {
		session.Status = status
		s.logger.Debug("session status updated", "beadID", beadID, "status", status)
	}
}

// buildClaudeCommand constructs the Claude CLI command
func (s *WorktreeSessionService) buildClaudeCommand(yolo bool) string {
	cliTool := s.config.CLITool
	if cliTool == "" {
		cliTool = "claude"
	}

	cmd := cliTool

	// Add yolo flag if enabled
	if yolo {
		cmd += " --yolo"
	}

	return cmd
}

// ConvertStatus converts SessionStatus to domain.SessionState
func ConvertStatus(status SessionStatus) domain.SessionState {
	switch status {
	case SessionIdle:
		return domain.SessionIdle
	case SessionActive:
		return domain.SessionBusy
	case SessionWaiting:
		return domain.SessionWaiting
	case SessionDone:
		return domain.SessionDone
	case SessionError:
		return domain.SessionError
	default:
		return domain.SessionIdle
	}
}

// ConvertFromDomainState converts domain.SessionState to SessionStatus
func ConvertFromDomainState(state domain.SessionState) SessionStatus {
	switch state {
	case domain.SessionIdle:
		return SessionIdle
	case domain.SessionBusy:
		return SessionActive
	case domain.SessionWaiting:
		return SessionWaiting
	case domain.SessionDone:
		return SessionDone
	case domain.SessionError:
		return SessionError
	default:
		return SessionIdle
	}
}
