package git

import (
	"context"
	"fmt"
	"log/slog"
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

	commitsBehind int
	isFetching    bool
	lastNotified  int
	isLocked      bool
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
		if s.isLocked {
			return nil
		}
		s.isLocked = true
		defer func() { s.isLocked = false }()

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
			s.logger.Warn("git sync rev-list failed", "error", err)
			count = 0
		}

		s.commitsBehind = count
		return GitSyncMsg{
			CommitsBehind: count,
			IsFetching:    false,
		}
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

		s.commitsBehind = 0
		s.lastNotified = 0
		return GitSyncMsg{
			CommitsBehind: 0,
			IsFetching:    false,
		}
	}
}

func (s *GitSyncService) ShouldNotify(count int) bool {
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
