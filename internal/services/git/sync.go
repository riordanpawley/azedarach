package git

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/riordanpawley/azedarach/internal/config"
	"github.com/riordanpawley/azedarach/internal/services/network"
)

type GitSyncService struct {
	gitClient      *Client
	networkChecker *network.StatusChecker
	config         *config.Config
	logger         *slog.Logger
	projectPath    string

	mu            sync.Mutex
	commitsBehind int
	isFetching    bool
	pendingFetch  bool
	lastNotified  int
}

type GitSyncMsg struct {
	CommitsBehind int
	IsFetching    bool
	Err           error
}

func NewGitSyncService(gitClient *Client, networkChecker *network.StatusChecker, cfg *config.Config, projectPath string, logger *slog.Logger) *GitSyncService {
	return &GitSyncService{
		gitClient:      gitClient,
		networkChecker: networkChecker,
		config:         cfg,
		projectPath:    projectPath,
		logger:         logger,
	}
}

func (s *GitSyncService) FetchAndCheck() tea.Cmd {
	return func() tea.Msg {
		for {
			if !s.beginFetch() {
				return nil
			}

			msg := s.fetchAndCheckOnce()

			if !s.finishFetch() {
				return msg
			}
		}
	}
}

func (s *GitSyncService) beginFetch() bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.isFetching {
		s.pendingFetch = true
		return false
	}

	s.isFetching = true
	return true
}

func (s *GitSyncService) finishFetch() bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.isFetching = false

	if s.pendingFetch {
		s.pendingFetch = false
		return true
	}

	return false
}

func (s *GitSyncService) fetchAndCheckOnce() tea.Msg {
	if s.config.Git.WorkflowMode != "origin" {
		return nil
	}

	if !s.networkChecker.IsOnline() {
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	remote := "origin"
	baseBranch := s.config.Git.BaseBranch

	err := s.gitClient.Fetch(ctx, s.projectPath, remote)
	if err != nil {
		s.logger.Warn("git sync fetch failed", "error", err)
	}

	revRange := fmt.Sprintf("%s..%s/%s", baseBranch, remote, baseBranch)
	count, err := s.gitClient.RevListCount(ctx, s.projectPath, revRange)
	if err != nil {
		s.mu.Lock()
		lastKnown := s.commitsBehind
		s.mu.Unlock()
		s.logger.Warn("git sync rev-list failed", "error", err, "lastKnownCommitsBehind", lastKnown)
		return GitSyncMsg{
			CommitsBehind: lastKnown,
			IsFetching:    false,
			Err:           err,
		}
	}

	s.mu.Lock()
	s.commitsBehind = count
	s.mu.Unlock()

	return GitSyncMsg{
		CommitsBehind: count,
		IsFetching:    false,
	}
}

func (s *GitSyncService) Pull() tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		remote := "origin"
		baseBranch := s.config.Git.BaseBranch

		currentBranch, err := s.gitClient.CurrentBranch(ctx, s.projectPath)
		if err != nil {
			return fmt.Errorf("failed to get current branch: %w", err)
		}

		if currentBranch == baseBranch {
			err = s.gitClient.Pull(ctx, s.projectPath, remote, baseBranch)
		} else {
			refSpec := fmt.Sprintf("%s:%s", baseBranch, baseBranch)
			err = s.gitClient.FetchRef(ctx, s.projectPath, remote, refSpec)
		}

		if err != nil {
			return GitSyncMsg{Err: err}
		}

		s.mu.Lock()
		s.commitsBehind = 0
		s.lastNotified = 0
		s.mu.Unlock()
		return GitSyncMsg{
			CommitsBehind: 0,
			IsFetching:    false,
		}
	}
}

func (s *GitSyncService) ShouldNotify(count int) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.config.Git.WorkflowMode != "origin" {
		return false
	}
	if count <= 0 {
		return false
	}
	if count <= s.lastNotified {
		return false
	}
	s.lastNotified = count
	return true
}
