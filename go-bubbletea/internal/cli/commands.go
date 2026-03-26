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
	"strings"
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
	defaultIssueListLimit     = 200
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
	Deps bool
	Limit int
}

type IssueGetOptions struct {
	IssueID string
	JSON    bool
	Deps    bool
}

type IssueCheckOptions struct {
	IssueID string
	JSON    bool
	Deps    bool
}

type IssueCreateOptions struct {
	Implementation string
	Title          string
	Description    string
	Type           domain.TaskType
	Priority       domain.Priority
}

type IssueCloseOptions struct {
	Implementation string
	IssueID        string
}

type IssueUpdateOptions struct {
	Implementation string
	IssueID        string
	Title          string
	Description    string
	Type           *domain.TaskType
	Priority       *domain.Priority
}

type IssueStatusOptions struct {
	Implementation string
	IssueID        string
	Status         domain.Status
}

type IssueDoctorOptions struct {
	IssueID string
}

type IssueDeleteOptions struct {
	Implementation string
	IssueID        string
	Confirm        bool
}

type IssueDependencyAddOptions struct {
	Implementation string
	IssueID        string
	DependsOnID    string
	Type           string
}

type IssueDependencyRemoveOptions struct {
	Implementation string
	IssueID        string
	DependsOnID    string
	Type           string
	Confirm        bool
}

type IssueBulkCreateOptions struct {
	Implementation string
	InputPath      string
	DryRun         bool
}

type IssueBulkUpdateOptions struct {
	Implementation string
	InputPath      string
	DryRun         bool
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
	opts := IssueListOptions{Limit: defaultIssueListLimit}
	fs := flag.NewFlagSet("issue list", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.BoolVar(&opts.JSON, "json", false, "output issues as JSON")
	fs.BoolVar(&opts.Deps, "deps", false, "include dependency summary in table output")
	fs.IntVar(&opts.Limit, "limit", defaultIssueListLimit, "maximum issues to list in one window")
	if err := fs.Parse(args); err != nil {
		return IssueListOptions{}, err
	}
	if fs.NArg() != 0 {
		return IssueListOptions{}, fmt.Errorf("unexpected argument: %s", fs.Arg(0))
	}
	if opts.Limit < 1 {
		return IssueListOptions{}, fmt.Errorf("limit must be >= 1")
	}
	return opts, nil
}

func ParseIssueGetArgs(args []string) (IssueGetOptions, error) {
	opts := IssueGetOptions{}
	fs := flag.NewFlagSet("issue get", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.BoolVar(&opts.JSON, "json", false, "output issue as JSON")
	fs.BoolVar(&opts.Deps, "deps", false, "include dependency details")
	if err := fs.Parse(args); err != nil {
		return IssueGetOptions{}, err
	}
	if fs.NArg() != 1 {
		return IssueGetOptions{}, fmt.Errorf("usage: az issue get <issue-id> [--json] [--deps]")
	}
	opts.IssueID = fs.Arg(0)
	return opts, nil
}

func ParseIssueCreateArgs(args []string) (IssueCreateOptions, error) {
	opts := IssueCreateOptions{
		Type:     domain.TypeTask,
		Priority: domain.P2,
	}
	var priorityRaw string
	var typeRaw string
	fs := flag.NewFlagSet("issue create", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&opts.Implementation, "impl", "", "target implementation key")
	fs.StringVar(&opts.Description, "description", "", "issue description")
	fs.StringVar(&priorityRaw, "priority", "P2", "issue priority (P0-P4)")
	fs.StringVar(&typeRaw, "type", string(domain.TypeTask), "issue type (task|bug|feature|epic|chore)")
	if err := fs.Parse(args); err != nil {
		return IssueCreateOptions{}, err
	}
	if fs.NArg() != 1 {
		return IssueCreateOptions{}, fmt.Errorf("usage: az issue create <title> --impl <implementation> [--type task|bug|feature|epic|chore] [--priority P0|P1|P2|P3|P4] [--description text]")
	}
	opts.Title = fs.Arg(0)
	if opts.Implementation == "" {
		return IssueCreateOptions{}, fmt.Errorf("missing required flag: --impl")
	}

	taskType, err := parseTaskType(typeRaw)
	if err != nil {
		return IssueCreateOptions{}, err
	}
	priority, err := parsePriority(priorityRaw)
	if err != nil {
		return IssueCreateOptions{}, err
	}
	opts.Type = taskType
	opts.Priority = priority
	return opts, nil
}

func ParseIssueCheckArgs(args []string) (IssueCheckOptions, error) {
	getOpts, err := ParseIssueGetArgs(args)
	if err != nil {
		return IssueCheckOptions{}, fmt.Errorf("usage: az issue check <issue-id> [--json] [--deps]")
	}
	return IssueCheckOptions{
		IssueID: getOpts.IssueID,
		JSON:    getOpts.JSON,
		Deps:    getOpts.Deps,
	}, nil
}

func ParseIssueDoctorArgs(args []string) (IssueDoctorOptions, error) {
	fs := flag.NewFlagSet("issue doctor", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	if err := fs.Parse(args); err != nil {
		return IssueDoctorOptions{}, err
	}
	if fs.NArg() != 1 {
		return IssueDoctorOptions{}, fmt.Errorf("usage: az issue doctor <issue-id>")
	}
	return IssueDoctorOptions{IssueID: fs.Arg(0)}, nil
}

func ParseIssueCloseArgs(args []string) (IssueCloseOptions, error) {
	opts := IssueCloseOptions{}
	fs := flag.NewFlagSet("issue close", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&opts.Implementation, "impl", "", "target implementation key")
	if err := fs.Parse(args); err != nil {
		return IssueCloseOptions{}, err
	}
	if fs.NArg() != 1 {
		return IssueCloseOptions{}, fmt.Errorf("usage: az issue close <issue-id> --impl <implementation>")
	}
	if opts.Implementation == "" {
		return IssueCloseOptions{}, fmt.Errorf("missing required flag: --impl")
	}
	opts.IssueID = fs.Arg(0)
	return opts, nil
}

func ParseIssueDeleteArgs(args []string) (IssueDeleteOptions, error) {
	opts := IssueDeleteOptions{}
	fs := flag.NewFlagSet("issue delete", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&opts.Implementation, "impl", "", "target implementation key")
	fs.BoolVar(&opts.Confirm, "confirm", false, "confirm permanent issue deletion")
	if err := fs.Parse(args); err != nil {
		return IssueDeleteOptions{}, err
	}
	if fs.NArg() != 1 {
		return IssueDeleteOptions{}, fmt.Errorf("usage: az issue delete <issue-id> --impl <implementation> --confirm")
	}
	if opts.Implementation == "" {
		return IssueDeleteOptions{}, fmt.Errorf("missing required flag: --impl")
	}
	if !opts.Confirm {
		return IssueDeleteOptions{}, fmt.Errorf("missing required flag: --confirm")
	}
	opts.IssueID = fs.Arg(0)
	return opts, nil
}

func ParseIssueUpdateArgs(args []string) (IssueUpdateOptions, error) {
	opts := IssueUpdateOptions{}
	var typeRaw string
	var priorityRaw string
	fs := flag.NewFlagSet("issue update", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&opts.Implementation, "impl", "", "target implementation key")
	fs.StringVar(&opts.Title, "title", "", "updated issue title")
	fs.StringVar(&opts.Description, "description", "", "updated issue description")
	fs.StringVar(&typeRaw, "type", "", "updated issue type (task|bug|feature|epic|chore)")
	fs.StringVar(&priorityRaw, "priority", "", "updated priority (P0-P4)")
	if err := fs.Parse(args); err != nil {
		return IssueUpdateOptions{}, err
	}
	if fs.NArg() != 1 {
		return IssueUpdateOptions{}, fmt.Errorf("usage: az issue update <issue-id> --impl <implementation> [--title text] [--description text] [--type task|bug|feature|epic|chore] [--priority P0|P1|P2|P3|P4]")
	}
	if opts.Implementation == "" {
		return IssueUpdateOptions{}, fmt.Errorf("missing required flag: --impl")
	}
	opts.IssueID = fs.Arg(0)
	if typeRaw != "" {
		tt, err := parseTaskType(typeRaw)
		if err != nil {
			return IssueUpdateOptions{}, err
		}
		opts.Type = &tt
	}
	if priorityRaw != "" {
		p, err := parsePriority(priorityRaw)
		if err != nil {
			return IssueUpdateOptions{}, err
		}
		opts.Priority = &p
	}
	if opts.Title == "" && opts.Description == "" && opts.Type == nil && opts.Priority == nil {
		return IssueUpdateOptions{}, fmt.Errorf("no update fields provided")
	}
	return opts, nil
}

func ParseIssueStatusArgs(args []string) (IssueStatusOptions, error) {
	opts := IssueStatusOptions{}
	fs := flag.NewFlagSet("issue status", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&opts.Implementation, "impl", "", "target implementation key")
	if err := fs.Parse(args); err != nil {
		return IssueStatusOptions{}, err
	}
	if fs.NArg() != 2 {
		return IssueStatusOptions{}, fmt.Errorf("usage: az issue status <issue-id> <open|in_progress|blocked|closed> --impl <implementation>")
	}
	if opts.Implementation == "" {
		return IssueStatusOptions{}, fmt.Errorf("missing required flag: --impl")
	}
	status, err := parseStatus(fs.Arg(1))
	if err != nil {
		return IssueStatusOptions{}, err
	}
	return IssueStatusOptions{
		Implementation: opts.Implementation,
		IssueID:        fs.Arg(0),
		Status:         status,
	}, nil
}

func ParseIssueDependencyAddArgs(args []string) (IssueDependencyAddOptions, error) {
	opts := IssueDependencyAddOptions{Type: "blocks"}
	fs := flag.NewFlagSet("issue dep add", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&opts.Implementation, "impl", "", "target implementation key")
	fs.StringVar(&opts.Type, "type", "blocks", "dependency type (blocks|related|parent-child|discovered-from)")
	if err := fs.Parse(args); err != nil {
		return IssueDependencyAddOptions{}, err
	}
	if fs.NArg() != 2 {
		return IssueDependencyAddOptions{}, fmt.Errorf("usage: az issue dep add <issue-id> <depends-on-id> --impl <implementation> [--type blocks|related|parent-child|discovered-from]")
	}
	if opts.Implementation == "" {
		return IssueDependencyAddOptions{}, fmt.Errorf("missing required flag: --impl")
	}
	opts.IssueID = fs.Arg(0)
	opts.DependsOnID = fs.Arg(1)
	return opts, nil
}

func ParseIssueDependencyRemoveArgs(args []string) (IssueDependencyRemoveOptions, error) {
	opts := IssueDependencyRemoveOptions{Type: "blocks"}
	fs := flag.NewFlagSet("issue dep remove", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&opts.Implementation, "impl", "", "target implementation key")
	fs.StringVar(&opts.Type, "type", "blocks", "dependency type (blocks|related|parent-child|discovered-from)")
	fs.BoolVar(&opts.Confirm, "confirm", false, "confirm removal for guarded dependency types")
	if err := fs.Parse(args); err != nil {
		return IssueDependencyRemoveOptions{}, err
	}
	if fs.NArg() != 2 {
		return IssueDependencyRemoveOptions{}, fmt.Errorf("usage: az issue dep remove <issue-id> <depends-on-id> --impl <implementation> [--type blocks|related|parent-child|discovered-from] [--confirm]")
	}
	if opts.Implementation == "" {
		return IssueDependencyRemoveOptions{}, fmt.Errorf("missing required flag: --impl")
	}
	opts.IssueID = fs.Arg(0)
	opts.DependsOnID = fs.Arg(1)
	return opts, nil
}

func ParseIssueBulkCreateArgs(args []string) (IssueBulkCreateOptions, error) {
	opts := IssueBulkCreateOptions{}
	fs := flag.NewFlagSet("issue bulk-create", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&opts.Implementation, "impl", "", "target implementation key")
	fs.StringVar(&opts.InputPath, "input", "", "path to JSON array input")
	fs.BoolVar(&opts.DryRun, "dry-run", false, "validate and preview without mutating")
	if err := fs.Parse(args); err != nil {
		return IssueBulkCreateOptions{}, err
	}
	if fs.NArg() != 0 {
		return IssueBulkCreateOptions{}, fmt.Errorf("unexpected argument: %s", fs.Arg(0))
	}
	if opts.Implementation == "" {
		return IssueBulkCreateOptions{}, fmt.Errorf("missing required flag: --impl")
	}
	if strings.TrimSpace(opts.InputPath) == "" {
		return IssueBulkCreateOptions{}, fmt.Errorf("missing required flag: --input")
	}
	return opts, nil
}

func ParseIssueBulkUpdateArgs(args []string) (IssueBulkUpdateOptions, error) {
	opts := IssueBulkUpdateOptions{}
	fs := flag.NewFlagSet("issue bulk-update", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&opts.Implementation, "impl", "", "target implementation key")
	fs.StringVar(&opts.InputPath, "input", "", "path to JSON array input")
	fs.BoolVar(&opts.DryRun, "dry-run", false, "validate and preview without mutating")
	if err := fs.Parse(args); err != nil {
		return IssueBulkUpdateOptions{}, err
	}
	if fs.NArg() != 0 {
		return IssueBulkUpdateOptions{}, fmt.Errorf("unexpected argument: %s", fs.Arg(0))
	}
	if opts.Implementation == "" {
		return IssueBulkUpdateOptions{}, fmt.Errorf("missing required flag: --impl")
	}
	if strings.TrimSpace(opts.InputPath) == "" {
		return IssueBulkUpdateOptions{}, fmt.Errorf("missing required flag: --input")
	}
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
	total := len(tasks)
	limit := opts.Limit
	if limit < 1 {
		limit = defaultIssueListLimit
	}
	truncated := total > limit
	if truncated {
		tasks = tasks[:limit]
	}

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
	if opts.Deps {
		fmt.Fprintln(w, "ID\tSTATUS\tPRIORITY\tTYPE\tDEPS\tTITLE")
	} else {
		fmt.Fprintln(w, "ID\tSTATUS\tPRIORITY\tTYPE\tTITLE")
	}
	for _, task := range tasks {
		if opts.Deps {
			fmt.Fprintf(
				w,
				"%s\t%s\t%s\t%s\t%s\t%s\n",
				task.ID,
				task.Status,
				task.Priority.String(),
				task.Type,
				formatDependencySummary(task.Dependencies),
				task.Title,
			)
			continue
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", task.ID, task.Status, task.Priority.String(), task.Type, task.Title)
	}
	if err := w.Flush(); err != nil {
		return err
	}

	truncatedText := "no"
	if truncated {
		truncatedText = "yes"
	}
	fmt.Printf("\nList window: listed=%d limit=%d total=%d truncated=%s\n", len(tasks), limit, total, truncatedText)
	if opts.Deps {
		topLevel, links := buildListDependencyContext(tasks)
		if len(topLevel) == 0 {
			fmt.Println("Top-level issues: (none)")
		} else {
			fmt.Printf("Top-level issues: %s\n", strings.Join(topLevel, ", "))
		}
		if len(links) == 0 {
			fmt.Println("Dependency links (listed issues): (none)")
		} else {
			fmt.Println("Dependency links (listed issues):")
			for _, link := range links {
				fmt.Printf("- %s -> %s (%s)\n", link.From, link.To, link.Type)
			}
		}
	}
	return nil
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
	dependencies, dependents := buildDependencyProjection(task, snapshot.Tasks)
	fmt.Printf("Dependencies: %d\n", len(dependencies))
	if opts.Deps {
		printDependencies(dependencies)
		printDependents(dependents)
	}
	if task.Description != "" {
		fmt.Printf("Description: %s\n", task.Description)
	}
	fmt.Printf("Created: %s\n", task.CreatedAt.UTC().Format(time.RFC3339))
	fmt.Printf("Updated: %s\n", task.UpdatedAt.UTC().Format(time.RFC3339))
	return nil
}

func IssueCheckCommand(deps *Dependencies, opts IssueCheckOptions) error {
	return IssueGetCommand(deps, IssueGetOptions{
		IssueID: opts.IssueID,
		JSON:    opts.JSON,
		Deps:    opts.Deps,
	})
}

func IssueDoctorCommand(deps *Dependencies, opts IssueDoctorOptions) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := ensureDaemon(ctx, deps, "cli"); err != nil {
		return err
	}

	snapshot, err := deps.DaemonClient.ListTasksSnapshot(ctx)
	if err != nil {
		return fmt.Errorf("failed to inspect issue %s: %w", opts.IssueID, err)
	}
	task, ok := findTaskByID(snapshot.Tasks, opts.IssueID)
	if !ok {
		return fmt.Errorf("issue not found: %s", opts.IssueID)
	}

	diagnostics := make([]string, 0, 4)
	if strings.TrimSpace(task.Title) == "" {
		diagnostics = append(diagnostics, "missing title")
	}
	if strings.TrimSpace(task.Type.String()) == "" {
		diagnostics = append(diagnostics, "missing type")
	}
	if strings.TrimSpace(task.Status.String()) == "" {
		diagnostics = append(diagnostics, "missing status")
	}

	if len(diagnostics) == 0 {
		fmt.Printf("Doctor: OK %s\n", task.ID)
		fmt.Printf("Dependencies: %s\n", formatDependencySummary(task.Dependencies))
		return nil
	}
	fmt.Printf("Doctor: WARN %s\n", task.ID)
	for _, diagnostic := range diagnostics {
		fmt.Printf("- %s\n", diagnostic)
	}
	return nil
}

func IssueCreateCommand(deps *Dependencies, opts IssueCreateOptions) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := ensureDaemon(ctx, deps, "cli"); err != nil {
		return err
	}

	taskID, err := deps.DaemonClient.CreateTask(ctx, daemonclient.TaskCreateParams{
		Title:       opts.Title,
		Description: opts.Description,
		Type:        opts.Type,
		Priority:    opts.Priority,
	})
	if err != nil {
		return fmt.Errorf("failed to create issue: %w", err)
	}

	fmt.Printf("Created issue: %s\n", taskID)
	return nil
}

func IssueCloseCommand(deps *Dependencies, opts IssueCloseOptions) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := ensureDaemon(ctx, deps, "cli"); err != nil {
		return err
	}

	if err := deps.DaemonClient.UpdateTaskStatus(ctx, opts.IssueID, domain.StatusDone); err != nil {
		return fmt.Errorf("failed to close issue %s: %w", opts.IssueID, err)
	}
	fmt.Printf("Closed issue: %s\n", opts.IssueID)
	return nil
}

func IssueDeleteCommand(deps *Dependencies, opts IssueDeleteOptions) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := ensureDaemon(ctx, deps, "cli"); err != nil {
		return err
	}

	if err := deps.DaemonClient.DeleteTask(ctx, opts.IssueID); err != nil {
		return fmt.Errorf("failed to delete issue %s: %w", opts.IssueID, err)
	}
	fmt.Printf("Deleted issue: %s\n", opts.IssueID)
	return nil
}

func IssueUpdateCommand(deps *Dependencies, opts IssueUpdateOptions) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := ensureDaemon(ctx, deps, "cli"); err != nil {
		return err
	}

	snapshot, err := deps.DaemonClient.ListTasksSnapshot(ctx)
	if err != nil {
		return fmt.Errorf("failed to load issue %s for update: %w", opts.IssueID, err)
	}
	task, ok := findTaskByID(snapshot.Tasks, opts.IssueID)
	if !ok {
		return fmt.Errorf("issue not found: %s", opts.IssueID)
	}

	update := daemonclient.TaskUpdateParams{
		Title:       task.Title,
		Description: task.Description,
		Type:        task.Type,
		Priority:    task.Priority,
	}
	if opts.Title != "" {
		update.Title = opts.Title
	}
	if opts.Description != "" {
		update.Description = opts.Description
	}
	if opts.Type != nil {
		update.Type = *opts.Type
	}
	if opts.Priority != nil {
		update.Priority = *opts.Priority
	}

	if err := deps.DaemonClient.UpdateTaskDetails(ctx, opts.IssueID, update); err != nil {
		return fmt.Errorf("failed to update issue %s: %w", opts.IssueID, err)
	}
	fmt.Printf("Updated issue: %s\n", opts.IssueID)
	return nil
}

func IssueStatusCommand(deps *Dependencies, opts IssueStatusOptions) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := ensureDaemon(ctx, deps, "cli"); err != nil {
		return err
	}

	if err := deps.DaemonClient.UpdateTaskStatus(ctx, opts.IssueID, opts.Status); err != nil {
		return fmt.Errorf("failed to set status for issue %s: %w", opts.IssueID, err)
	}
	fmt.Printf("Updated status: %s -> %s\n", opts.IssueID, opts.Status)
	return nil
}

func IssueDependencyAddCommand(deps *Dependencies, opts IssueDependencyAddOptions) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := ensureDaemon(ctx, deps, "cli"); err != nil {
		return err
	}

	if err := deps.DaemonClient.AddTaskDependency(ctx, daemonclient.TaskDependencyParams{
		TaskID:      opts.IssueID,
		DependsOnID: opts.DependsOnID,
		Type:        opts.Type,
	}); err != nil {
		return fmt.Errorf("failed to add dependency %s -> %s: %w", opts.IssueID, opts.DependsOnID, err)
	}
	fmt.Printf("Added dependency: %s --(%s)--> %s\n", opts.IssueID, opts.Type, opts.DependsOnID)
	return nil
}

func IssueDependencyRemoveCommand(deps *Dependencies, opts IssueDependencyRemoveOptions) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := ensureDaemon(ctx, deps, "cli"); err != nil {
		return err
	}

	if err := deps.DaemonClient.RemoveTaskDependency(ctx, daemonclient.TaskDependencyRemoveParams{
		TaskID:      opts.IssueID,
		DependsOnID: opts.DependsOnID,
		Type:        opts.Type,
		Confirm:     opts.Confirm,
	}); err != nil {
		return fmt.Errorf("failed to remove dependency %s -> %s: %w", opts.IssueID, opts.DependsOnID, err)
	}
	fmt.Printf("Removed dependency: %s --(%s)--> %s\n", opts.IssueID, opts.Type, opts.DependsOnID)
	return nil
}

func IssueBulkCreateCommand(deps *Dependencies, opts IssueBulkCreateOptions) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := ensureDaemon(ctx, deps, "cli"); err != nil {
		return err
	}

	inputBytes, err := os.ReadFile(opts.InputPath)
	if err != nil {
		return fmt.Errorf("read bulk-create input %s: %w", opts.InputPath, err)
	}
	var input []struct {
		Title       string  `json:"title"`
		Description string  `json:"description"`
		Type        string  `json:"type"`
		Priority    string  `json:"priority"`
		ParentID    *string `json:"parent_id,omitempty"`
	}
	if err := json.Unmarshal(inputBytes, &input); err != nil {
		return fmt.Errorf("parse bulk-create input %s: %w", opts.InputPath, err)
	}
	if len(input) == 0 {
		return fmt.Errorf("bulk-create input must contain at least one item")
	}

	ops := make([]protocol.ApplyOperationBody, 0, len(input))
	for i, item := range input {
		taskType, err := parseTaskType(item.Type)
		if err != nil {
			return fmt.Errorf("bulk-create item %d: %w", i, err)
		}
		priority, err := parsePriority(item.Priority)
		if err != nil {
			return fmt.Errorf("bulk-create item %d: %w", i, err)
		}
		body, err := json.Marshal(daemonclient.TaskCreateParams{
			Title:       item.Title,
			Description: item.Description,
			Type:        taskType,
			Priority:    priority,
			ParentID:    item.ParentID,
		})
		if err != nil {
			return fmt.Errorf("marshal bulk-create item %d: %w", i, err)
		}
		ops = append(ops, protocol.ApplyOperationBody{
			Command: daemonclient.CommandTaskCreate,
			Body:    body,
		})
	}

	return executeBulkApply(deps, opts.DryRun, ops)
}

func IssueBulkUpdateCommand(deps *Dependencies, opts IssueBulkUpdateOptions) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := ensureDaemon(ctx, deps, "cli"); err != nil {
		return err
	}

	inputBytes, err := os.ReadFile(opts.InputPath)
	if err != nil {
		return fmt.Errorf("read bulk-update input %s: %w", opts.InputPath, err)
	}
	var input []struct {
		TaskID      string `json:"task_id,omitempty"`
		ID          string `json:"id,omitempty"`
		Title       string `json:"title,omitempty"`
		Description string `json:"description,omitempty"`
		Type        string `json:"type,omitempty"`
		Priority    string `json:"priority,omitempty"`
	}
	if err := json.Unmarshal(inputBytes, &input); err != nil {
		return fmt.Errorf("parse bulk-update input %s: %w", opts.InputPath, err)
	}
	if len(input) == 0 {
		return fmt.Errorf("bulk-update input must contain at least one item")
	}

	snapshot, err := deps.DaemonClient.ListTasksSnapshot(ctx)
	if err != nil {
		return fmt.Errorf("load issues for bulk-update: %w", err)
	}
	tasksByID := map[string]domain.Task{}
	for _, task := range snapshot.Tasks {
		tasksByID[task.ID] = task
	}

	ops := make([]protocol.ApplyOperationBody, 0, len(input))
	for i, item := range input {
		taskID := strings.TrimSpace(item.TaskID)
		if taskID == "" {
			taskID = strings.TrimSpace(item.ID)
		}
		if taskID == "" {
			return fmt.Errorf("bulk-update item %d: missing task_id", i)
		}
		current, ok := tasksByID[taskID]
		if !ok {
			return fmt.Errorf("bulk-update item %d: issue not found: %s", i, taskID)
		}

		update := daemonclient.TaskUpdateParams{
			Title:       current.Title,
			Description: current.Description,
			Type:        current.Type,
			Priority:    current.Priority,
		}
		if item.Title != "" {
			update.Title = item.Title
		}
		if item.Description != "" {
			update.Description = item.Description
		}
		if item.Type != "" {
			taskType, err := parseTaskType(item.Type)
			if err != nil {
				return fmt.Errorf("bulk-update item %d: %w", i, err)
			}
			update.Type = taskType
		}
		if item.Priority != "" {
			priority, err := parsePriority(item.Priority)
			if err != nil {
				return fmt.Errorf("bulk-update item %d: %w", i, err)
			}
			update.Priority = priority
		}

		body, err := json.Marshal(struct {
			TaskID string `json:"task_id"`
			daemonclient.TaskUpdateParams
		}{
			TaskID:           taskID,
			TaskUpdateParams: update,
		})
		if err != nil {
			return fmt.Errorf("marshal bulk-update item %d: %w", i, err)
		}
		ops = append(ops, protocol.ApplyOperationBody{
			Command: daemonclient.CommandTaskUpdate,
			Body:    body,
		})
	}

	return executeBulkApply(deps, opts.DryRun, ops)
}

func findTaskByID(tasks []domain.Task, id string) (domain.Task, bool) {
	for _, task := range tasks {
		if task.ID == id {
			return task, true
		}
	}
	return domain.Task{}, false
}

func formatDependencySummary(deps []domain.Dependency) string {
	if len(deps) == 0 {
		return "-"
	}
	counts := map[domain.DependencyType]int{}
	for _, dep := range deps {
		counts[dep.Type]++
	}
	ordered := []domain.DependencyType{
		domain.DependencyBlocks,
		domain.DependencyBlockedBy,
		domain.DependencyRelatedTo,
		domain.DependencyDiscovered,
		domain.DependencyParentChild,
	}
	parts := make([]string, 0, len(ordered))
	for _, depType := range ordered {
		if count := counts[depType]; count > 0 {
			parts = append(parts, fmt.Sprintf("%s:%d", depType, count))
		}
	}
	if len(parts) == 0 {
		return "-"
	}
	return strings.Join(parts, ",")
}

func printDependencies(deps []domain.Dependency) {
	if len(deps) == 0 {
		return
	}
	fmt.Println("Dependency edges:")
	for _, dep := range deps {
		fmt.Printf("- %s (%s)\n", dep.ID, dep.Type)
	}
}

func printDependents(deps []domain.Dependency) {
	if len(deps) == 0 {
		return
	}
	fmt.Println("Dependents:")
	for _, dep := range deps {
		fmt.Printf("- %s (%s)\n", dep.ID, dep.Type)
	}
}

type dependencyLink struct {
	From string
	To   string
	Type domain.DependencyType
}

func buildListDependencyContext(tasks []domain.Task) ([]string, []dependencyLink) {
	idSet := make(map[string]struct{}, len(tasks))
	for _, task := range tasks {
		idSet[task.ID] = struct{}{}
	}

	topLevel := make(map[string]struct{}, len(tasks))
	for _, task := range tasks {
		topLevel[task.ID] = struct{}{}
	}

	links := make([]dependencyLink, 0, len(tasks))
	seenLinks := map[string]struct{}{}
	addLink := func(from, to string, depType domain.DependencyType) {
		if strings.TrimSpace(from) == "" || strings.TrimSpace(to) == "" {
			return
		}
		key := from + "|" + to + "|" + string(depType)
		if _, ok := seenLinks[key]; ok {
			return
		}
		seenLinks[key] = struct{}{}
		links = append(links, dependencyLink{From: from, To: to, Type: depType})
		delete(topLevel, from)
	}

	for _, task := range tasks {
		if task.ParentID != nil {
			parentID := strings.TrimSpace(*task.ParentID)
			if _, ok := idSet[parentID]; ok {
				addLink(task.ID, parentID, domain.DependencyParentChild)
			}
		}
		for _, dep := range task.Dependencies {
			depID := strings.TrimSpace(dep.ID)
			if _, ok := idSet[depID]; ok {
				addLink(task.ID, depID, dep.Type)
			}
		}
	}

	topLevelIDs := make([]string, 0, len(topLevel))
	for issueID := range topLevel {
		topLevelIDs = append(topLevelIDs, issueID)
	}
	sort.Strings(topLevelIDs)
	sort.Slice(links, func(i, j int) bool {
		if links[i].From != links[j].From {
			return links[i].From < links[j].From
		}
		if links[i].To != links[j].To {
			return links[i].To < links[j].To
		}
		return links[i].Type < links[j].Type
	})
	return topLevelIDs, links
}

func buildDependencyProjection(task domain.Task, allTasks []domain.Task) ([]domain.Dependency, []domain.Dependency) {
	dependencies := make([]domain.Dependency, 0, len(task.Dependencies)+1)
	seenDependencies := make(map[string]struct{}, len(task.Dependencies)+1)

	addDependency := func(dep domain.Dependency) {
		id := strings.TrimSpace(dep.ID)
		if id == "" {
			return
		}
		key := id + "|" + string(dep.Type)
		if _, ok := seenDependencies[key]; ok {
			return
		}
		seenDependencies[key] = struct{}{}
		dependencies = append(dependencies, domain.Dependency{ID: id, Type: dep.Type})
	}

	for _, dep := range task.Dependencies {
		addDependency(dep)
	}
	if task.ParentID != nil && strings.TrimSpace(*task.ParentID) != "" {
		addDependency(domain.Dependency{
			ID:   strings.TrimSpace(*task.ParentID),
			Type: domain.DependencyParentChild,
		})
	}

	dependents := make([]domain.Dependency, 0, 8)
	seenDependents := map[string]struct{}{}
	addDependent := func(dep domain.Dependency) {
		id := strings.TrimSpace(dep.ID)
		if id == "" {
			return
		}
		key := id + "|" + string(dep.Type)
		if _, ok := seenDependents[key]; ok {
			return
		}
		seenDependents[key] = struct{}{}
		dependents = append(dependents, domain.Dependency{ID: id, Type: dep.Type})
	}

	for _, candidate := range allTasks {
		if candidate.ID == task.ID {
			continue
		}
		if candidate.ParentID != nil && strings.TrimSpace(*candidate.ParentID) == task.ID {
			addDependent(domain.Dependency{
				ID:   candidate.ID,
				Type: domain.DependencyParentChild,
			})
		}
		for _, dep := range candidate.Dependencies {
			if strings.TrimSpace(dep.ID) == task.ID {
				addDependent(domain.Dependency{
					ID:   candidate.ID,
					Type: dep.Type,
				})
			}
		}
	}

	return dependencies, dependents
}

func executeBulkApply(deps *Dependencies, dryRun bool, operations []protocol.ApplyOperationBody) error {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	snapshot, err := deps.DaemonClient.ListTasksSnapshot(ctx)
	if err != nil {
		return fmt.Errorf("load snapshot revision for bulk apply: %w", err)
	}
	snapshotRevision := snapshot.Revision
	if snapshotRevision == 0 {
		snapshotRevision = 1
	}

	body, err := json.Marshal(protocol.ApplyRequestBody{
		SchemaVersion:    protocol.ApplySchemaVersion,
		SnapshotRevision: snapshotRevision,
		DryRun:           dryRun,
		Operations:       operations,
	})
	if err != nil {
		return fmt.Errorf("marshal bulk apply request: %w", err)
	}

	resp, err := deps.DaemonClient.Command(ctx, protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       fmt.Sprintf("%s-%d", protocol.CommandTaskBulkApply, time.Now().UTC().UnixNano()),
		Kind:            protocol.EnvelopeKindCommand,
		Meta: protocol.Metadata{
			ProjectID: deps.ProjectID,
		},
		Command: protocol.CommandTaskBulkApply,
		SentAt:  time.Now().UTC(),
		Body:    body,
	})
	if err != nil {
		return fmt.Errorf("execute bulk apply: %w", err)
	}
	if err := responseError(resp, "execute bulk apply"); err != nil {
		return err
	}

	if len(resp.Body) == 0 {
		fmt.Println("Bulk apply completed.")
		return nil
	}
	var pretty any
	if err := json.Unmarshal(resp.Body, &pretty); err != nil {
		return fmt.Errorf("decode bulk apply response: %w", err)
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(pretty); err != nil {
		return fmt.Errorf("print bulk apply response: %w", err)
	}
	if applyResponseExitCode(resp) == exitCodePartialFailure {
		return fmt.Errorf("bulk apply completed with partial failures")
	}
	return nil
}

func parseTaskType(raw string) (domain.TaskType, error) {
	tt := domain.TaskType(raw)
	switch tt {
	case domain.TypeTask, domain.TypeBug, domain.TypeFeature, domain.TypeEpic, domain.TypeChore:
		return tt, nil
	default:
		return "", fmt.Errorf("invalid issue type: %s", raw)
	}
}

func parsePriority(raw string) (domain.Priority, error) {
	switch raw {
	case "P0":
		return domain.P0, nil
	case "P1":
		return domain.P1, nil
	case "P2":
		return domain.P2, nil
	case "P3":
		return domain.P3, nil
	case "P4":
		return domain.P4, nil
	default:
		return 0, fmt.Errorf("invalid priority: %s", raw)
	}
}

func parseStatus(raw string) (domain.Status, error) {
	switch raw {
	case "open":
		return domain.StatusOpen, nil
	case "in_progress":
		return domain.StatusInProgress, nil
	case "blocked":
		return domain.StatusBlocked, nil
	case "closed":
		return domain.StatusDone, nil
	default:
		return "", fmt.Errorf("invalid status: %s", raw)
	}
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
  session <subcommand>  Session commands (start|attach|kill|status)
  start <issue-id>      Alias for 'az session start <issue-id>'
  attach <issue-id>     Alias for 'az session attach <issue-id>'
  kill <issue-id>       Alias for 'az session kill <issue-id>'
  status [issue-id]     Alias for 'az session status [issue-id]'
  issue list [--json] [--deps] [--limit N]  List issues from daemon-backed store
  issue get <id> [--json] [--deps]  Show a single issue from daemon-backed store
  issue check <id> [--json] [--deps]  Alias for issue get
  issue doctor <id>  Run integrity checks for one issue
  issue create <title> --impl <implementation> [--type ...] [--priority ...] [--description ...]  Create an issue
  issue update <id> --impl <implementation> [--title ...] [--description ...] [--type ...] [--priority ...]  Update issue fields
  issue status <id> <open|in_progress|blocked|closed> --impl <implementation>  Set issue status
  issue close <id> --impl <implementation>      Close an issue (sets status=closed)
  issue delete <id> --impl <implementation> --confirm  Permanently delete an issue
  issue dep add <issue-id> <depends-on-id> --impl <implementation> [--type ...]  Add a dependency edge
  issue dep remove <issue-id> <depends-on-id> --impl <implementation> [--type ...] [--confirm]  Remove a dependency edge
  issue bulk-create --impl <implementation> --input <path> [--dry-run]  Execute bulk create operations
  issue bulk-update --impl <implementation> --input <path> [--dry-run]  Execute bulk update operations
  export               Export a snapshot (use --format json [--out <path>])
  daemon restart       Force-restart the daemon and verify re-attach
  help                 Show this help message

Examples:
  az                   # Start TUI
  az session start az-123   # Start session for issue az-123
  az session attach az-123  # Attach to issue az-123's session
  az session kill az-123    # Kill issue az-123's session
  az session status         # Show all active sessions
  az session status az-123  # Show status for az-123
  az issue list
  az issue list --deps
  az issue get az-123 --json
  az issue get az-123 --deps
  az issue check az-123 --deps
  az issue doctor az-123
  az issue create "New task" --impl go-bubbletea --type task --priority P2
  az issue update az-123 --impl go-bubbletea --title "Renamed task" --priority P1
  az issue status az-123 in_progress --impl go-bubbletea
  az issue close az-123 --impl go-bubbletea
  az issue delete az-123 --impl go-bubbletea --confirm
  az issue dep add az-456 az-123 --impl go-bubbletea --type blocks
  az issue dep remove az-456 az-123 --impl go-bubbletea --type blocks --confirm
  az issue bulk-create --impl go-bubbletea --input ./bulk-create.json
  az issue bulk-update --impl go-bubbletea --input ./bulk-update.json --dry-run
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
