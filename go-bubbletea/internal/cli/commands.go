package cli

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"text/tabwriter"
	"time"

	"github.com/riordanpawley/azedarach/internal/config"
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/services/beads"
	"github.com/riordanpawley/azedarach/internal/services/git"
	"github.com/riordanpawley/azedarach/internal/services/tmux"
)

// Dependencies holds all the services needed for CLI commands
type Dependencies struct {
	Config          *config.Config
	BeadsClient     *beads.Client
	TmuxClient      *tmux.Client
	WorktreeManager *git.WorktreeManager
	Logger          *slog.Logger
}

// NewDependencies creates a new Dependencies instance with all required services
func NewDependencies(cfg *config.Config) (*Dependencies, error) {
	logger := slog.Default()

	// Initialize beads client
	beadsRunner := &beads.ExecRunner{}
	beadsClient := beads.NewClient(beadsRunner, logger)

	// Initialize tmux client
	tmuxRunner := &tmux.ExecRunner{}
	tmuxClient := tmux.NewClient(tmuxRunner, logger)

	// Initialize git worktree manager
	repoDir, err := os.Getwd()
	if err != nil {
		return nil, fmt.Errorf("failed to get current directory: %w", err)
	}
	gitRunner := git.NewExecRunner(repoDir)
	worktreeManager := git.NewWorktreeManager(gitRunner, repoDir, logger)

	return &Dependencies{
		Config:          cfg,
		BeadsClient:     beadsClient,
		TmuxClient:      tmuxClient,
		WorktreeManager: worktreeManager,
		Logger:          logger,
	}, nil
}

// StartCommand starts a Claude session for the given bead ID
func StartCommand(deps *Dependencies, beadID string) error {
	ctx := context.Background()

	deps.Logger.Info("starting session", "bead_id", beadID)

	// Check if tmux session already exists
	exists, err := deps.TmuxClient.HasSession(ctx, beadID)
	if err != nil {
		return fmt.Errorf("failed to check session: %w", err)
	}
	if exists {
		return fmt.Errorf("session already exists: %s (use 'az attach %s' to connect)", beadID, beadID)
	}

	// Get bead info to verify it exists
	tasks, err := deps.BeadsClient.Search(ctx, beadID)
	if err != nil {
		return fmt.Errorf("failed to search for bead: %w", err)
	}
	if len(tasks) == 0 {
		return fmt.Errorf("bead not found: %s", beadID)
	}
	task := tasks[0]

	fmt.Printf("Starting session for: %s - %s\n", task.ID, task.Title)

	// Create worktree for the task
	baseBranch := "main" // TODO: Make configurable
	fmt.Printf("Creating worktree from branch: %s\n", baseBranch)
	worktree, err := deps.WorktreeManager.Create(ctx, beadID, baseBranch)
	if err != nil {
		return fmt.Errorf("failed to create worktree: %w", err)
	}
	fmt.Printf("Worktree created: %s\n", worktree.Path)

	// Create tmux session
	fmt.Printf("Creating tmux session: %s\n", beadID)
	err = deps.TmuxClient.NewSession(ctx, beadID, worktree.Path)
	if err != nil {
		return fmt.Errorf("failed to create tmux session: %w", err)
	}

	// Send Claude command to session
	claudeCmd := "claude" // TODO: Make configurable or add more context
	err = deps.TmuxClient.SendKeys(ctx, beadID, claudeCmd)
	if err != nil {
		return fmt.Errorf("failed to send keys: %w", err)
	}

	// Update bead status to in_progress
	err = deps.BeadsClient.Update(ctx, beadID, domain.StatusInProgress)
	if err != nil {
		deps.Logger.Warn("failed to update bead status", "error", err)
		// Don't fail the command if status update fails
	}

	fmt.Printf("\n✓ Session started successfully\n")
	fmt.Printf("  To attach: az attach %s\n", beadID)
	fmt.Printf("  Or run:    tmux attach-session -t %s\n", beadID)

	return nil
}

// AttachCommand attaches to an existing tmux session
func AttachCommand(deps *Dependencies, beadID string) error {
	ctx := context.Background()

	deps.Logger.Info("attaching to session", "bead_id", beadID)

	// Check if session exists
	exists, err := deps.TmuxClient.HasSession(ctx, beadID)
	if err != nil {
		return fmt.Errorf("failed to check session: %w", err)
	}
	if !exists {
		return fmt.Errorf("session not found: %s (use 'az start %s' to create)", beadID, beadID)
	}

	fmt.Printf("Attaching to session: %s\n", beadID)
	fmt.Printf("(Press Ctrl+B then D to detach)\n\n")

	// Note: AttachSession is blocking - it will transfer control to tmux
	err = deps.TmuxClient.AttachSession(ctx, beadID)
	if err != nil {
		return fmt.Errorf("failed to attach to session: %w", err)
	}

	return nil
}

// KillCommand kills a Claude session
func KillCommand(deps *Dependencies, beadID string) error {
	ctx := context.Background()

	deps.Logger.Info("killing session", "bead_id", beadID)

	// Check if session exists
	exists, err := deps.TmuxClient.HasSession(ctx, beadID)
	if err != nil {
		return fmt.Errorf("failed to check session: %w", err)
	}
	if !exists {
		return fmt.Errorf("session not found: %s", beadID)
	}

	fmt.Printf("Killing session: %s\n", beadID)

	// Kill tmux session
	err = deps.TmuxClient.KillSession(ctx, beadID)
	if err != nil {
		return fmt.Errorf("failed to kill session: %w", err)
	}

	fmt.Printf("✓ Session killed: %s\n", beadID)
	fmt.Printf("  Note: Worktree is preserved. Use 'git worktree remove' to clean up.\n")

	return nil
}

// StatusCommand shows the status of sessions
func StatusCommand(deps *Dependencies, beadID string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	deps.Logger.Info("checking session status", "bead_id", beadID)

	// Get all tmux sessions
	tmuxSessions, err := deps.TmuxClient.ListSessions(ctx)
	if err != nil {
		return fmt.Errorf("failed to list tmux sessions: %w", err)
	}

	// Get all beads
	tasks, err := deps.BeadsClient.List(ctx)
	if err != nil {
		return fmt.Errorf("failed to list beads: %w", err)
	}

	// Build a map of bead ID to task
	taskMap := make(map[string]domain.Task)
	for _, task := range tasks {
		taskMap[task.ID] = task
	}

	// Filter to specific bead if provided
	if beadID != "" {
		found := false
		for _, sessionName := range tmuxSessions {
			if sessionName == beadID {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("no active session found for bead: %s", beadID)
		}
		tmuxSessions = []string{beadID}
	}

	if len(tmuxSessions) == 0 {
		fmt.Println("No active sessions")
		return nil
	}

	// Display sessions
	fmt.Printf("Active Sessions (%d):\n\n", len(tmuxSessions))

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "BEAD ID\tSTATUS\tTITLE")
	fmt.Fprintln(w, "-------\t------\t-----")

	for _, sessionName := range tmuxSessions {
		task, ok := taskMap[sessionName]
		status := "unknown"
		title := "(not in beads)"

		if ok {
			status = string(task.Status)
			title = task.Title
			// Truncate title if too long
			if len(title) > 60 {
				title = title[:57] + "..."
			}
		}

		fmt.Fprintf(w, "%s\t%s\t%s\n", sessionName, status, title)
	}

	w.Flush()

	fmt.Printf("\nUse 'az attach <bead-id>' to attach to a session\n")

	return nil
}

// PrintUsage prints CLI usage information
func PrintUsage() {
	usage := `Usage: az [command] [arguments]

Commands:
  (no command)         Start the Azedarach TUI
  start <bead-id>      Start a Claude session for a bead
  attach <bead-id>     Attach to an existing session
  kill <bead-id>       Kill a session
  status [bead-id]     Show session status (all or specific bead)
  help                 Show this help message

Examples:
  az                   # Start TUI
  az start az-123      # Start session for bead az-123
  az attach az-123     # Attach to az-123's session
  az kill az-123       # Kill az-123's session
  az status            # Show all active sessions
  az status az-123     # Show status for az-123

For more information, see: https://github.com/riordanpawley/azedarach
`
	fmt.Print(usage)
}
