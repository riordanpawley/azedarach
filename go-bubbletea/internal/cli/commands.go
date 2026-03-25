package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"text/tabwriter"
	"time"

	autoclient "github.com/riordanpawley/azedarach/internal/client"
	"github.com/riordanpawley/azedarach/internal/client/daemonclient"
	"github.com/riordanpawley/azedarach/internal/client/daemonprocess"
	"github.com/riordanpawley/azedarach/internal/config"
	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/ipc/transport"
)

var newLauncher = func(repoDir, socketPath string) daemonStarter {
	return daemonprocess.NewLauncher(repoDir, socketPath)
}

const (
	commandSessionStart       = "session.start"
	commandSessionAttach      = "session.attach"
	commandSessionStop        = "session.stop"
	commandSessionStatus      = "session.status"
	commandTaskSnapshotExport = "task.snapshot.export"
	defaultExportFormat       = "json"
	exitCodeHardFailure       = 1
	exitCodePartialFailure    = 2
)

type Dependencies struct {
	Config       *config.Config
	DaemonClient *daemonclient.Client
	Logger       *slog.Logger
	ProjectID    string
	RepoDir      string
}

type daemonStarter interface {
	Start(ctx context.Context) error
	Replace(ctx context.Context) error
}

type ExportOptions struct {
	Format string
	Out    string
}

type IssueListOptions struct {
	JSON bool
}

type IssueGetOptions struct {
	IssueID string
	JSON    bool
}

func NewDependencies(cfg *config.Config) (*Dependencies, error) {
	logger := slog.Default()

	repoDir, err := os.Getwd()
	if err != nil {
		return nil, fmt.Errorf("failed to get current directory: %w", err)
	}

	projectID := filepath.Base(repoDir)
	socketPath := config.GlobalDaemonSocketPath()
	daemonTransport := transport.NewClient(socketPath)

	return &Dependencies{
		Config:       cfg,
		DaemonClient: daemonclient.New(daemonTransport).WithProjectID(projectID),
		Logger:       logger,
		ProjectID:    projectID,
		RepoDir:      repoDir,
	}, nil
}

func StartCommand(deps *Dependencies, issueID string) error {
	ctx := context.Background()
	if err := ensureDaemon(ctx, deps, "cli"); err != nil {
		return err
	}

	deps.Logger.Info("starting session", "issue_id", issueID)

	resp, err := deps.DaemonClient.Command(ctx, newSessionRequest(commandSessionStart, deps.ProjectID, issueID, "main"))
	if err != nil {
		return fmt.Errorf("failed to start session: %w", err)
	}
	if err := responseError(resp, "failed to start session"); err != nil {
		return err
	}

	return printCommandOutput(resp)
}

func AttachCommand(deps *Dependencies, issueID string) error {
	ctx := context.Background()
	if err := ensureDaemon(ctx, deps, "cli"); err != nil {
		return err
	}

	deps.Logger.Info("attaching to session", "issue_id", issueID)

	resp, err := deps.DaemonClient.Command(ctx, newSessionRequest(commandSessionAttach, deps.ProjectID, issueID, ""))
	if err != nil {
		return fmt.Errorf("failed to attach to session: %w", err)
	}
	if err := responseError(resp, "failed to attach to session"); err != nil {
		return err
	}

	return printCommandOutput(resp)
}

func KillCommand(deps *Dependencies, issueID string) error {
	ctx := context.Background()
	if err := ensureDaemon(ctx, deps, "cli"); err != nil {
		return err
	}

	deps.Logger.Info("killing session", "issue_id", issueID)

	resp, err := deps.DaemonClient.Command(ctx, newSessionRequest(commandSessionStop, deps.ProjectID, issueID, ""))
	if err != nil {
		return fmt.Errorf("failed to kill session: %w", err)
	}
	if err := responseError(resp, "failed to kill session"); err != nil {
		return err
	}

	return printCommandOutput(resp)
}

func StatusCommand(deps *Dependencies, issueID string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := ensureDaemon(ctx, deps, "cli"); err != nil {
		return err
	}

	deps.Logger.Info("checking session status", "issue_id", issueID)

	resp, err := deps.DaemonClient.Command(ctx, newSessionRequest(commandSessionStatus, deps.ProjectID, issueID, ""))
	if err != nil {
		return fmt.Errorf("failed to list tmux sessions: %w", err)
	}
	if err := responseError(resp, "failed to list tmux sessions"); err != nil {
		return err
	}

	return printCommandOutput(resp)
}

func ParseExportArgs(args []string) (ExportOptions, error) {
	opts := ExportOptions{Format: defaultExportFormat}

	fs := flag.NewFlagSet("export", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&opts.Format, "format", defaultExportFormat, "export format")
	fs.StringVar(&opts.Out, "out", "", "write export output to a file")

	if err := fs.Parse(args); err != nil {
		return ExportOptions{}, err
	}
	if fs.NArg() != 0 {
		return ExportOptions{}, fmt.Errorf("unexpected argument: %s", fs.Arg(0))
	}
	if opts.Format != defaultExportFormat {
		return ExportOptions{}, fmt.Errorf("unsupported export format: %s", opts.Format)
	}

	return opts, nil
}

func ParseIssueListArgs(args []string) (IssueListOptions, error) {
	opts := IssueListOptions{}
	fs := flag.NewFlagSet("issue list", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.BoolVar(&opts.JSON, "json", false, "output issues as JSON")
	if err := fs.Parse(args); err != nil {
		return IssueListOptions{}, err
	}
	if fs.NArg() != 0 {
		return IssueListOptions{}, fmt.Errorf("unexpected argument: %s", fs.Arg(0))
	}
	return opts, nil
}

func ParseIssueGetArgs(args []string) (IssueGetOptions, error) {
	opts := IssueGetOptions{}
	fs := flag.NewFlagSet("issue get", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.BoolVar(&opts.JSON, "json", false, "output issue as JSON")
	if err := fs.Parse(args); err != nil {
		return IssueGetOptions{}, err
	}
	if fs.NArg() != 1 {
		return IssueGetOptions{}, fmt.Errorf("usage: az issue get <issue-id> [--json]")
	}
	opts.IssueID = fs.Arg(0)
	return opts, nil
}

func ExportCommand(deps *Dependencies, opts ExportOptions) error {
	ctx := context.Background()
	if err := ensureDaemon(ctx, deps, "cli"); err != nil {
		return err
	}

	resp, err := deps.DaemonClient.Command(ctx, protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       fmt.Sprintf("%s-%d", commandTaskSnapshotExport, time.Now().UTC().UnixNano()),
		Kind:            protocol.EnvelopeKindCommand,
		Meta: protocol.Metadata{
			ProjectID: deps.ProjectID,
		},
		Command: commandTaskSnapshotExport,
		SentAt:  time.Now().UTC(),
	})
	if err != nil {
		return fmt.Errorf("failed to export snapshot: %w", err)
	}
	if err := responseError(resp, "failed to export snapshot"); err != nil {
		return err
	}

	if opts.Out == "" {
		if _, err := os.Stdout.Write(resp.Body); err != nil {
			return fmt.Errorf("write export output to stdout: %w", err)
		}
		return nil
	}

	if err := os.WriteFile(opts.Out, resp.Body, 0644); err != nil {
		return fmt.Errorf("write export output to %s: %w", opts.Out, err)
	}
	return nil
}

func IssueListCommand(deps *Dependencies, opts IssueListOptions) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := ensureDaemon(ctx, deps, "cli"); err != nil {
		return err
	}

	snapshot, err := deps.DaemonClient.ListTasksSnapshot(ctx)
	if err != nil {
		return fmt.Errorf("failed to list issues: %w", err)
	}
	tasks := snapshot.Tasks
	sort.SliceStable(tasks, func(i, j int) bool {
		if tasks[i].UpdatedAt.Equal(tasks[j].UpdatedAt) {
			return tasks[i].ID < tasks[j].ID
		}
		return tasks[i].UpdatedAt.After(tasks[j].UpdatedAt)
	})

	if opts.JSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(tasks)
	}

	if len(tasks) == 0 {
		fmt.Println("No issues found.")
		return nil
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 8, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tSTATUS\tPRIORITY\tTYPE\tTITLE")
	for _, task := range tasks {
		fmt.Fprintf(
			w,
			"%s\t%s\t%s\t%s\t%s\n",
			task.ID,
			task.Status,
			task.Priority.String(),
			task.Type,
			task.Title,
		)
	}
	return w.Flush()
}

func IssueGetCommand(deps *Dependencies, opts IssueGetOptions) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := ensureDaemon(ctx, deps, "cli"); err != nil {
		return err
	}

	snapshot, err := deps.DaemonClient.ListTasksSnapshot(ctx)
	if err != nil {
		return fmt.Errorf("failed to get issue %s: %w", opts.IssueID, err)
	}

	task, ok := findTaskByID(snapshot.Tasks, opts.IssueID)
	if !ok {
		return fmt.Errorf("issue not found: %s", opts.IssueID)
	}

	if opts.JSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(task)
	}

	fmt.Printf("ID: %s\n", task.ID)
	fmt.Printf("Title: %s\n", task.Title)
	fmt.Printf("Status: %s\n", task.Status)
	fmt.Printf("Priority: %s\n", task.Priority.String())
	fmt.Printf("Type: %s\n", task.Type)
	if task.ParentID != nil {
		fmt.Printf("Parent: %s\n", *task.ParentID)
	}
	fmt.Printf("Dependencies: %d\n", len(task.Dependencies))
	if task.Description != "" {
		fmt.Printf("Description: %s\n", task.Description)
	}
	fmt.Printf("Created: %s\n", task.CreatedAt.UTC().Format(time.RFC3339))
	fmt.Printf("Updated: %s\n", task.UpdatedAt.UTC().Format(time.RFC3339))
	return nil
}

func findTaskByID(tasks []domain.Task, id string) (domain.Task, bool) {
	for _, task := range tasks {
		if task.ID == id {
			return task, true
		}
	}
	return domain.Task{}, false
}

// RestartDaemonCommand forces daemon replacement and verifies client re-attach.
func RestartDaemonCommand(deps *Dependencies) error {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	launcher := newLauncher(deps.RepoDir, config.GlobalDaemonSocketPath())
	if err := launcher.Replace(ctx); err != nil {
		return fmt.Errorf("restart daemon: %w", err)
	}
	if err := ensureDaemon(ctx, deps, "cli"); err != nil {
		return fmt.Errorf("daemon health check after restart failed: %w", err)
	}

	fmt.Println("Daemon restarted successfully.")
	return nil
}

func PrintUsage() {
	usage := `Usage: az [command] [arguments]

Commands:
  (no command)         Start the Azedarach TUI
  start <issue-id>      Start a Claude session for an issue
  attach <issue-id>     Attach to an existing session
  kill <issue-id>       Kill a session
  status [issue-id]     Show session status (all or specific issue)
  issue list [--json]  List issues from daemon-backed store
  issue get <id> [--json]  Show a single issue from daemon-backed store
  export               Export a snapshot (use --format json [--out <path>])
  daemon restart       Force-restart the daemon and verify re-attach
  help                 Show this help message

Examples:
  az                   # Start TUI
  az start az-123      # Start session for issue az-123
  az attach az-123     # Attach to issue az-123's session
  az kill az-123       # Kill issue az-123's session
  az status            # Show all active sessions
  az status az-123     # Show status for az-123
  az issue list
  az issue get az-123 --json
  az export --format json
  az export --format json --out snapshot.json
  az daemon restart

For more information, see: https://github.com/riordanpawley/azedarach
`
	fmt.Print(usage)
}

type sessionRequestBody struct {
	ProjectID  string `json:"project_id"`
	SessionID  string `json:"session_id"`
	BaseBranch string `json:"base_branch,omitempty"`
}

type commandOutputBody struct {
	Output string `json:"output"`
}

type applyExecutionResultBody struct {
	Summary applyExecutionSummaryBody `json:"summary"`
}

type applyExecutionSummaryBody struct {
	Total     int `json:"total"`
	Succeeded int `json:"succeeded"`
	Failed    int `json:"failed"`
}

func newSessionRequest(command, projectID, sessionID, baseBranch string) protocol.RequestEnvelope {
	body, _ := json.Marshal(sessionRequestBody{
		ProjectID:  projectID,
		SessionID:  sessionID,
		BaseBranch: baseBranch,
	})

	return protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       fmt.Sprintf("%s-%d", command, time.Now().UTC().UnixNano()),
		Kind:            protocol.EnvelopeKindCommand,
		Command:         command,
		Meta: protocol.Metadata{
			ProjectID: projectID,
			SessionID: sessionID,
		},
		SentAt: time.Now().UTC(),
		Body:   body,
	}
}

func responseError(resp protocol.ResponseEnvelope, prefix string) error {
	if resp.OK {
		return nil
	}
	if resp.Error == nil {
		return fmt.Errorf("%s: daemon command failed", prefix)
	}
	return errors.New(resp.Error.Message)
}

func printCommandOutput(resp protocol.ResponseEnvelope) error {
	if len(resp.Body) == 0 {
		return nil
	}

	var out commandOutputBody
	if err := json.Unmarshal(resp.Body, &out); err != nil {
		return fmt.Errorf("failed to decode daemon response: %w", err)
	}

	if out.Output != "" {
		fmt.Print(out.Output)
	}

	return nil
}

func applyResponseExitCode(resp protocol.ResponseEnvelope) int {
	if !resp.OK {
		return exitCodeHardFailure
	}
	if len(resp.Body) == 0 {
		return 0
	}

	var applyResult applyExecutionResultBody
	if err := json.Unmarshal(resp.Body, &applyResult); err != nil {
		return exitCodeHardFailure
	}
	if applyResult.Summary.Failed > 0 {
		return exitCodePartialFailure
	}

	return 0
}

func ensureDaemon(ctx context.Context, deps *Dependencies, clientName string) error {
	launcher := newLauncher(deps.RepoDir, config.GlobalDaemonSocketPath())
	orch := autoclient.NewAutostartOrchestrator(autoclient.NewDaemonHandshaker(deps.DaemonClient), launcher)
	ack, err := orch.EnsureAttached(ctx, protocol.Hello{
		ProtocolVersion: protocol.CurrentVersion,
		ClientName:      clientName,
		ClientVersion:   "dev",
		Capabilities:    []string{"snapshot", "subscribe"},
	})
	if err != nil {
		return fmt.Errorf("daemon attach failed: %w", err)
	}
	if !ack.Accepted {
		return fmt.Errorf("daemon handshake rejected: %s", ack.Reason)
	}
	return nil
}
