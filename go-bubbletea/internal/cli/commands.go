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
	"github.com/riordanpawley/azedarach/internal/services/git"
	"github.com/riordanpawley/azedarach/internal/services/linear"
	"github.com/riordanpawley/azedarach/internal/services/tmux"
)

// Dependencies holds all the services needed for CLI commands
type Dependencies struct {
	Config          *config.Config
	IssueClient     *linear.Client
	TmuxClient      *tmux.Client
	WorktreeManager *git.WorktreeManager
	Logger          *slog.Logger
}

// NewDependencies creates a new Dependencies instance with all required services
func NewDependencies(cfg *config.Config) (*Dependencies, error) {
	logger := slog.Default()

	// Initialize issue client adapter
	issueRunner := &linear.ExecRunner{}
	issueClient := linear.NewClient(issueRunner, logger)

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
		IssueClient:     issueClient,
		TmuxClient:      tmuxClient,
		WorktreeManager: worktreeManager,
		Logger:          logger,
	}, nil
}

// StartCommand starts a Claude session for the given issue ID
func StartCommand(deps *Dependencies, issueID string) error {
	ctx := context.Background()

	deps.Logger.Info("starting session", "issue_id", issueID)

	// Check if tmux session already exists
	exists, err := deps.TmuxClient.HasSession(ctx, issueID)
	if err != nil {
		return fmt.Errorf("failed to check session: %w", err)
	}
	if exists {
		return fmt.Errorf("session already exists: %s (use 'az attach %s' to connect)", issueID, issueID)
	}

	// Get issue info to verify it exists
	tasks, err := deps.IssueClient.Search(ctx, issueID)
	if err != nil {
		return fmt.Errorf("failed to search for issue: %w", err)
	}
	if len(tasks) == 0 {
		return fmt.Errorf("issue not found: %s", issueID)
	}
	task := tasks[0]

	fmt.Printf("Starting session for: %s - %s\n", task.ID, task.Title)

	// Create worktree for the task
	baseBranch := "main" // TODO: Make configurable
	fmt.Printf("Creating worktree from branch: %s\n", baseBranch)
	worktree, err := deps.WorktreeManager.Create(ctx, issueID, baseBranch)
	if err != nil {
		return fmt.Errorf("failed to create worktree: %w", err)
	}
	fmt.Printf("Worktree created: %s\n", worktree.Path)

	// Create tmux session
	fmt.Printf("Creating tmux session: %s\n", issueID)
	err = deps.TmuxClient.NewSession(ctx, issueID, worktree.Path)
	if err != nil {
		return fmt.Errorf("failed to create tmux session: %w", err)
	}

	// Send Claude command to session
	claudeCmd := "claude" // TODO: Make configurable or add more context
	err = deps.TmuxClient.SendKeys(ctx, issueID, claudeCmd)
	if err != nil {
		return fmt.Errorf("failed to send keys: %w", err)
	}

	// Update issue status to in_progress
	err = deps.IssueClient.Update(ctx, issueID, domain.StatusInProgress)
	if err != nil {
		deps.Logger.Warn("failed to update issue status", "error", err)
		// Don't fail the command if status update fails
	}

	fmt.Printf("\n✓ Session started successfully\n")
	fmt.Printf("  To attach: az attach %s\n", issueID)
	fmt.Printf("  Or run:    tmux attach-session -t %s\n", issueID)

	return nil
}

// AttachCommand attaches to an existing tmux session
func AttachCommand(deps *Dependencies, issueID string) error {
	ctx := context.Background()

	deps.Logger.Info("attaching to session", "issue_id", issueID)

	// Check if session exists
	exists, err := deps.TmuxClient.HasSession(ctx, issueID)
	if err != nil {
		return fmt.Errorf("failed to check session: %w", err)
	}
	if !exists {
		return fmt.Errorf("session not found: %s (use 'az start %s' to create)", issueID, issueID)
	}

	fmt.Printf("Attaching to session: %s\n", issueID)
	fmt.Printf("(Press Ctrl+B then D to detach)\n\n")

	// Note: AttachSession is blocking - it will transfer control to tmux
	err = deps.TmuxClient.AttachSession(ctx, issueID)
	if err != nil {
		return fmt.Errorf("failed to attach to session: %w", err)
	}

	return nil
}

// KillCommand kills a Claude session
func KillCommand(deps *Dependencies, issueID string) error {
	ctx := context.Background()

	deps.Logger.Info("killing session", "issue_id", issueID)

	// Check if session exists
	exists, err := deps.TmuxClient.HasSession(ctx, issueID)
	if err != nil {
		return fmt.Errorf("failed to check session: %w", err)
	}
	if !exists {
		return fmt.Errorf("session not found: %s", issueID)
	}

	fmt.Printf("Killing session: %s\n", issueID)

	// Kill tmux session
	err = deps.TmuxClient.KillSession(ctx, issueID)
	if err != nil {
		return fmt.Errorf("failed to kill session: %w", err)
	}

	fmt.Printf("✓ Session killed: %s\n", issueID)
	fmt.Printf("  Note: Worktree is preserved. Use 'git worktree remove' to clean up.\n")

	return nil
}

// StatusCommand shows the status of sessions
func StatusCommand(deps *Dependencies, issueID string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	deps.Logger.Info("checking session status", "issue_id", issueID)

	// Get all tmux sessions
	tmuxSessions, err := deps.TmuxClient.ListSessions(ctx)
	if err != nil {
		return fmt.Errorf("failed to list tmux sessions: %w", err)
	}

	// Get all issues
	tasks, err := deps.IssueClient.List(ctx)
	if err != nil {
		return fmt.Errorf("failed to list issues: %w", err)
	}

	// Build a map of issue ID to task
	taskMap := make(map[string]domain.Task)
	for _, task := range tasks {
		taskMap[task.ID] = task
	}

	// Filter to specific issue if provided
	if issueID != "" {
		found := false
		for _, sessionName := range tmuxSessions {
			if sessionName == issueID {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("no active session found for issue: %s", issueID)
		}
		tmuxSessions = []string{issueID}
	}

	if len(tmuxSessions) == 0 {
		fmt.Println("No active sessions")
		return nil
	}

	// Display sessions
	fmt.Printf("Active Sessions (%d):\n\n", len(tmuxSessions))

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "ISSUE ID\tSTATUS\tTITLE")
	fmt.Fprintln(w, "-------\t------\t-----")

	for _, sessionName := range tmuxSessions {
		task, ok := taskMap[sessionName]
		status := "unknown"
		title := "(not in issue store)"

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

	fmt.Printf("\nUse 'az attach <issue-id>' to attach to a session\n")

	return nil
}

// PrintUsage prints CLI usage information
func PrintUsage() {
	usage := `Usage: az [command] [arguments]

Commands:
  (no command)         Start the Azedarach TUI
  start <issue-id>     Start a Claude session for an issue
  attach <issue-id>    Attach to an existing session
  kill <issue-id>      Kill a session
  status [issue-id]    Show session status (all or specific issue)
  help                 Show this help message

Examples:
  az                   # Start TUI
  az start az-123      # Start session for issue az-123
  az attach az-123     # Attach to az-123's session
  az kill az-123       # Kill az-123's session
  az status            # Show all active sessions
  az status az-123     # Show status for az-123

For more information, see: https://github.com/riordanpawley/azedarach
`
	fmt.Print(usage)
}
