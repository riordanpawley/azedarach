package app

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

type sessionMonitorService interface {
	Stop(issueID string)
	StopAll()
}

type gitSyncService interface {
	FetchAndCheck() tea.Cmd
	Pull() tea.Cmd
	ShouldNotify(count int) bool
}

type appServiceDeps struct {
	sessionMonitor     sessionMonitorService
	gitSyncService     gitSyncService
	gitDiffClient      diff.DiffClient
	attachmentService  overlay.ImageAttachmentService
	diagnosticsService overlay.DiagnosticsCollector
	projectRegistry    *config.ProjectsRegistry
	isOnline           bool
	tmuxAvailable      bool
}

// tmuxAdapter adapts tmux.Client to satisfy monitor.TmuxClient interface
type tmuxAdapter struct {
	client *tmux.Client
}

func (a *tmuxAdapter) CapturePane(ctx context.Context, sessionName string) (string, error) {
	return a.client.CapturePane(ctx, sessionName, defaultPaneCaptureLines)
}

func buildAppServiceDeps(cfg *config.Config, repoDir string, logger *slog.Logger) appServiceDeps {
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

	return appServiceDeps{
		sessionMonitor:     sessionMonitor,
		gitSyncService:     gitSync,
		gitDiffClient:      gitClient,
		attachmentService:  attachmentSvc,
		diagnosticsService: diagService,
		projectRegistry:    registry,
		isOnline:           true,
		tmuxAvailable:      os.Getenv("TMUX") != "",
	}
}
