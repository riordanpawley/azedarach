package appdeps

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/riordanpawley/azedarach/internal/config"
	"github.com/riordanpawley/azedarach/internal/services/attachment"
	"github.com/riordanpawley/azedarach/internal/services/devserver"
	"github.com/riordanpawley/azedarach/internal/services/diagnostics"
	"github.com/riordanpawley/azedarach/internal/services/git"
	"github.com/riordanpawley/azedarach/internal/services/monitor"
	"github.com/riordanpawley/azedarach/internal/services/network"
	"github.com/riordanpawley/azedarach/internal/services/tmux"
	"github.com/riordanpawley/azedarach/internal/ui/diff"
	"github.com/riordanpawley/azedarach/internal/ui/overlay"
)

const defaultPaneCaptureLines = 100
const defaultDevserverBasePort = 3000

type SessionMonitorService interface {
	Stop(issueID string)
	StopAll()
}

type GitSyncService interface {
	FetchAndCheck() tea.Cmd
	Pull() tea.Cmd
	ShouldNotify(count int) bool
}

type Deps struct {
	SessionMonitor     SessionMonitorService
	GitSyncService     GitSyncService
	GitDiffClient      diff.DiffClient
	AttachmentService  overlay.ImageAttachmentService
	DiagnosticsService overlay.DiagnosticsCollector
	ProjectRegistry    *config.ProjectsRegistry
	IsOnline           bool
	TmuxAvailable      bool
}

type tmuxAdapter struct {
	client *tmux.Client
}

func (a *tmuxAdapter) CapturePane(ctx context.Context, sessionName string) (string, error) {
	return a.client.CapturePane(ctx, sessionName, defaultPaneCaptureLines)
}

func Build(cfg *config.Config, repoDir string, logger *slog.Logger) Deps {
	tmuxRunner := &tmux.ExecRunner{}
	tmuxClient := tmux.NewClient(tmuxRunner, logger)

	adapter := &tmuxAdapter{client: tmuxClient}
	sessionMonitor := monitor.NewSessionMonitor(adapter)

	portAllocator := devserver.NewPortAllocator(defaultDevserverBasePort)
	networkChecker := network.NewStatusChecker()

	gitRunner := git.NewExecRunner(repoDir)
	gitClient := git.NewClient(gitRunner, logger)
	gitSync := git.NewGitSyncService(gitClient, networkChecker, cfg, repoDir, logger)

	registry, err := config.LoadProjectsRegistry()
	if err != nil {
		logger.Error("failed to load project registry", "error", err)
		registry = &config.ProjectsRegistry{
			Projects:       []config.Project{},
			DefaultProject: "",
		}
	}

	issuesPath := filepath.Join(repoDir, ".azedarach")
	attachmentSvc := attachment.NewService(issuesPath, logger)
	diagService := diagnostics.NewService(tmuxClient, portAllocator, networkChecker)

	return Deps{
		SessionMonitor:     sessionMonitor,
		GitSyncService:     gitSync,
		GitDiffClient:      gitClient,
		AttachmentService:  attachmentSvc,
		DiagnosticsService: diagService,
		ProjectRegistry:    registry,
		IsOnline:           true,
		TmuxAvailable:      os.Getenv("TMUX") != "",
	}
}
