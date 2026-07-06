package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/riordanpawley/azedarach/internal/buildinfo"
	clitext "github.com/riordanpawley/azedarach/internal/cli/text"
	autoclient "github.com/riordanpawley/azedarach/internal/client"
	"github.com/riordanpawley/azedarach/internal/client/daemonclient"
	"github.com/riordanpawley/azedarach/internal/client/daemonprocess"
	"github.com/riordanpawley/azedarach/internal/client/reconnect"
	"github.com/riordanpawley/azedarach/internal/config"
	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/ipc/transport"
	"github.com/riordanpawley/azedarach/internal/latencytrace"
	"github.com/riordanpawley/azedarach/internal/logging"
	"github.com/riordanpawley/azedarach/internal/logstream"
	"github.com/riordanpawley/azedarach/internal/naming"
	"github.com/riordanpawley/azedarach/internal/services/attachment"
)

var newLauncher = func(repoDir, socketPath string) daemonStarter {
	return daemonprocess.NewLauncher(repoDir, socketPath)
}

var currentExecutable = os.Executable

var tmuxPaneSessionName = defaultTmuxPaneSessionName

const (
	commandSessionStart         = "session.start"
	commandSessionAttach        = "session.attach"
	commandSessionPause         = "session.pause"
	commandSessionResume        = "session.resume"
	commandSessionStop          = "session.stop"
	commandSessionStatus        = "session.status"
	commandTaskSnapshotExport   = "task.snapshot.export"
	defaultExportFormat         = "json"
	defaultIssueListLimit       = 200
	defaultOperationListLimit   = 50
	sessionStartCommandTimeout  = 5 * time.Minute
	branchMergeToBaseTimeout    = 2 * time.Minute
	daemonCommandTimeout        = 15 * time.Second
	issueCloseCleanupTimeout    = 10 * time.Minute
	issueCreateCommandTimeout   = 10 * time.Second
	issueCreateAutostartTimeout = 12 * time.Second
	exitCodeHardFailure         = 1
	exitCodePartialFailure      = 2
)

var primeLookPath = exec.LookPath

type Dependencies struct {
	Config         *config.Config
	DaemonClient   *daemonclient.Client
	DaemonSocket   string
	Logger         *slog.Logger
	ProjectID      string
	RepoDir        string
	RuntimeRepoDir string
}

type repeatedStringFlag []string

func (f *repeatedStringFlag) String() string {
	if f == nil {
		return ""
	}
	return strings.Join(*f, ",")
}

func (f *repeatedStringFlag) Set(value string) error {
	*f = append(*f, value)
	return nil
}

type daemonStarter interface {
	Start(ctx context.Context) error
	Stop(ctx context.Context) error
	Replace(ctx context.Context) error
}

type ExportOptions struct {
	Format string
	Out    string
}

type ConfigSetOptions struct {
	Key        string
	Value      string
	ProjectDir string
}

type SyncOptions struct {
	All        bool
	ProjectDir string
	Conflicts  bool
	JSON       bool
}

type LogOptions struct {
	Sources []string
	Lines   int
	Follow  bool
}

type ImplDeleteOptions struct {
	Implementation string
	Confirm        bool
}

type ImplListOptions struct{}

type ImplMigrateOptions struct {
	FromImplementation string
	ToImplementation   string
}

type IssueListOptions struct {
	Project       string
	JSON          bool
	Deps          bool
	Limit         int
	Query         string
	IDs           []string
	States        []domain.Status
	ParentIDs     []string
	DependsOnIDs  []string
	CreatedAfter  *time.Time
	CreatedBefore *time.Time
	UpdatedAfter  *time.Time
	UpdatedBefore *time.Time
}

type IssueGetOptions struct {
	Project      string
	IssueID      string
	JSON         bool
	IncludeNotes bool
}

type IssueEventsOptions struct {
	Project    string
	IssueID    string
	JSON       bool
	EventTypes []string
	Limit      int
}

type IssueGetManyOptions struct {
	Project      string
	IssueIDs     []string
	JSON         bool
	IncludeNotes bool
}

type IssueCheckOptions struct {
	Project string
	IssueID string
	JSON    bool
}

type IssueCreateOptions struct {
	Project                string
	JSON                   bool
	Title                  string
	Description            string
	Type                   domain.TaskType
	Priority               domain.Priority
	PriorityExplicit       bool
	Deferred               bool
	Implementations        []string
	AutoParentFromIssueID  *string
	AutoCreatedFromIssueID *string
	ProjectQualifiedOutput bool
}

type issueCreateResult struct {
	IssueID       string
	ProjectID     string
	ParentID      string
	CreatedFromID string
	Deferred      bool
	Message       string
}

type issueCreatePartialError struct {
	Result issueCreateResult
	Err    error
}

func (e issueCreatePartialError) Error() string {
	return fmt.Sprintf("issue creation partially succeeded: created %s, but post-create graph update failed: %v", formatProjectIssueRef(e.Result.ProjectID, e.Result.IssueID), e.Err)
}

func (e issueCreatePartialError) Unwrap() error {
	return e.Err
}

type IssueCloseOptions struct {
	Project            string
	IssueID            string
	JSON               bool
	ForceWorktree      bool
	CloseCleanChildren bool
}

type IssueUpdateOptions struct {
	Project         string
	IssueID         string
	JSON            bool
	Title           string
	Description     string
	DescriptionSet  bool
	Notes           *string
	AppendNotes     string
	Type            *domain.TaskType
	Priority        *domain.Priority
	Status          *domain.Status
	ForceWorktree   bool
	CascadeChildren bool
	UpdateImpls     []string
}

type IssueDoctorOptions struct {
	Project string
	IssueID string
	JSON    bool
}

type ProjectScriptsStatusOptions struct {
	ProjectDir string
	JSON       bool
	Names      []string
}

type IssueDeleteOptions struct {
	Project        string
	IssueID        string
	Confirm        bool
	JSON           bool
	Cleanup        bool
	StopSession    bool
	RemoveWorktree bool
	ForceWorktree  bool
}

type IssueDependencyAddOptions struct {
	Project            string
	IssueID            string
	DependsOnID        string
	IssueProjectID     string
	DependsOnProjectID string
	Type               string
	ForceParentChange  bool
	JSON               bool
}

type IssueDependencyRemoveOptions struct {
	Project             string
	IssueID             string
	DependsOnID         string
	Type                string
	Confirm             bool
	ConfirmParentOrphan bool
	JSON                bool
}

type IssueDependencyBulkApplyOptions struct {
	Project   string
	InputPath string
	DryRun    bool
	JSON      bool
}

type IssueImageAddOptions struct {
	Project    string
	IssueID    string
	SourcePath string
	JSON       bool
}

type IssueImageRemoveOptions struct {
	Project      string
	IssueID      string
	AttachmentID string
	JSON         bool
}

type IssueDocumentAddOptions struct {
	Project    string
	IssueID    string
	SourcePath string
	JSON       bool
}

type IssueDocumentListOptions struct {
	Project string
	IssueID string
	JSON    bool
}

type IssueDocumentRemoveOptions struct {
	Project      string
	IssueID      string
	AttachmentID string
	JSON         bool
}

type IssueBulkCreateOptions struct {
	Project        string
	Implementation string
	InputPath      string
	DryRun         bool
	JSON           bool
}

type issueBulkCreateInputItem struct {
	Title       string                     `json:"title"`
	Description string                     `json:"description"`
	Type        string                     `json:"type"`
	Priority    string                     `json:"priority"`
	ParentID    *string                    `json:"parent_id,omitempty"`
	Ref         string                     `json:"ref,omitempty"`
	Children    []issueBulkCreateInputItem `json:"children,omitempty"`
}

type IssueBulkUpdateOptions struct {
	Project        string
	Implementation string
	InputPath      string
	DryRun         bool
	JSON           bool
}

type SessionCommandOptions struct {
	Wait         bool
	Project      string
	PollInterval time.Duration
	WaitTimeout  time.Duration
}

type SessionRestartAllOptions struct {
	ForceBusy bool
	Yolo      bool
	JSON      bool
}

type WorktreeCreateOptions struct {
	Project    string
	IssueID    string
	BaseBranch string
	JSON       bool
}

func ParseWorktreeCreateArgs(args []string) (WorktreeCreateOptions, error) {
	opts := WorktreeCreateOptions{}
	fs := flag.NewFlagSet("worktree create", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	addIssueProjectFlag(fs, &opts.Project)
	fs.StringVar(&opts.BaseBranch, "base", "", "base branch/ref override")
	fs.StringVar(&opts.BaseBranch, "base-branch", "", "base branch/ref override")
	fs.BoolVar(&opts.JSON, "json", false, "print JSON output")
	if err := parseWithInterspersedFlags(fs, args); err != nil {
		return WorktreeCreateOptions{}, err
	}
	if fs.NArg() != 1 {
		return WorktreeCreateOptions{}, fmt.Errorf("issue id is required")
	}
	opts.IssueID = strings.TrimSpace(fs.Arg(0))
	return opts, nil
}

func ParseSessionStartArgs(args []string, allowProject bool, usage string) (string, SessionCommandOptions, error) {
	opts := SessionCommandOptions{}
	if strings.TrimSpace(usage) == "" {
		usage = "usage: az session start [--project <project-id>] <issue-id> [--wait]"
	}

	fs := flag.NewFlagSet("session start", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.BoolVar(&opts.Wait, "wait", false, "wait for any queued operation to complete")
	if allowProject {
		fs.StringVar(&opts.Project, "project", "", "target project id or registered alias")
	}
	if err := parseWithInterspersedFlags(fs, args); err != nil {
		return "", SessionCommandOptions{}, fmt.Errorf("%s", usage)
	}
	if fs.NArg() != 1 {
		return "", SessionCommandOptions{}, fmt.Errorf("%s", usage)
	}
	opts.Project = strings.TrimSpace(opts.Project)
	return fs.Arg(0), opts, nil
}

func ParseSessionRestartAllArgs(args []string) (SessionRestartAllOptions, error) {
	opts := SessionRestartAllOptions{}
	fs := flag.NewFlagSet("session restart-all", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.BoolVar(&opts.ForceBusy, "force-busy", false, "restart busy sessions too")
	fs.BoolVar(&opts.Yolo, "yolo", false, "resume sessions with dangerous bypass enabled")
	fs.BoolVar(&opts.JSON, "json", false, "print JSON output")
	if err := parseWithInterspersedFlags(fs, args); err != nil {
		return SessionRestartAllOptions{}, err
	}
	if fs.NArg() != 0 {
		return SessionRestartAllOptions{}, fmt.Errorf("usage: az session restart-all [--force-busy] [--yolo] [--json]")
	}
	return opts, nil
}

type SessionResolveConflictOptions struct {
	IssueID       string
	Worktree      string
	ConflictFiles []string
	Prompt        string
}

type sessionIssueTarget struct {
	ProjectID   string
	RepoDir     string
	IssueID     string
	Task        domain.Task
	TaskContext []domain.Task
}

type sessionProjectCandidate struct {
	Route   string
	Name    string
	Path    string
	Aliases []string
	Scopes  []string
}

type BranchAgentMergeOptions struct {
	IssueID string
	Target  string
}

type BranchMergeToBaseOptions struct {
	IssueID           string
	AllowBaseForChild bool
}

type OperationGetOptions struct {
	OperationID  string
	JSON         bool
	Wait         bool
	PollInterval time.Duration
}

type OperationLogsOptions struct {
	OperationID string
	JSON        bool
}

type OperationListOptions struct {
	IssueID string
	Kind    string
	States  []protocol.OperationState
	JSON    bool
	Limit   int
}

type OperationCancelOptions struct {
	OperationID  string
	Reason       string
	JSON         bool
	Wait         bool
	PollInterval time.Duration
}

func NewDependencies(cfg *config.Config) (*Dependencies, error) {
	repoDir, err := os.Getwd()
	if err != nil {
		return nil, fmt.Errorf("failed to get current directory: %w", err)
	}
	return NewDependenciesAt(cfg, repoDir)
}

func NewDependenciesAt(cfg *config.Config, repoDir string) (*Dependencies, error) {
	if strings.TrimSpace(repoDir) == "" {
		return nil, fmt.Errorf("failed to get current directory: empty repo dir")
	}
	absRepoDir, err := filepath.Abs(repoDir)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve repo directory %q: %w", repoDir, err)
	}
	logPath := filepath.Join(resolveSessionLogDirFor(cfg, absRepoDir), logging.CLILogFileName)
	logger := logging.NewTextFileLogger(logPath, slog.LevelInfo)
	slog.SetDefault(logger)

	rootRepoDir, err := config.ResolveProjectRoot(absRepoDir)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve project root from %q: %w", absRepoDir, err)
	}
	runtimeRepoDir := rootRepoDir
	if config.UseScopedDaemonRuntimeFor(absRepoDir) {
		if worktreeRoot, err := config.ResolveWorktreeRoot(absRepoDir); err == nil && strings.TrimSpace(worktreeRoot) != "" {
			runtimeRepoDir = worktreeRoot
		}
	}

	projectID, err := config.ProjectIDForRoot(rootRepoDir)
	if err != nil {
		return nil, fmt.Errorf("failed to derive project id from %q: %w", rootRepoDir, err)
	}
	socketPath := config.DaemonSocketPathFor(absRepoDir)
	if err := validateSharedDaemonFence(absRepoDir, socketPath); err != nil {
		return nil, err
	}
	daemonTransport := transport.NewClient(socketPath)

	return &Dependencies{
		Config:         cfg,
		DaemonClient:   daemonclient.New(daemonTransport).WithProjectID(projectID),
		DaemonSocket:   socketPath,
		Logger:         logger,
		ProjectID:      projectID,
		RepoDir:        rootRepoDir,
		RuntimeRepoDir: runtimeRepoDir,
	}, nil
}

func validateSharedDaemonFence(startPath, socketPath string) error {
	if config.UseScopedDaemonRuntimeFor(startPath) {
		return nil
	}
	executable, err := currentExecutable()
	if err != nil || strings.TrimSpace(executable) == "" {
		return nil
	}
	return config.ValidateSharedDaemonExecutable(socketPath, executable)
}

func StartCommand(deps *Dependencies, issueID string) error {
	return StartCommandWithOptions(deps, issueID, SessionCommandOptions{})
}

func StartCommandWithOptions(deps *Dependencies, issueID string, opts SessionCommandOptions) error {
	ctx, cancel := context.WithTimeout(context.Background(), daemonCommandTimeout)
	defer cancel()

	restoreExplicitProject := applyExplicitSessionProjectOverride(deps, opts.Project)
	defer restoreExplicitProject()

	if err := ensureDaemon(ctx, deps, "cli"); err != nil {
		return err
	}
	target, err := resolveSessionStartIssueTarget(ctx, deps, issueID, opts.Project)
	if err != nil {
		return err
	}
	restoreProject := applyIssueProjectOverride(deps, target.ProjectID)
	defer restoreProject()

	baseBranch, err := resolveSessionStartBaseBranch(ctx, deps, target.Task)
	if err != nil {
		return err
	}

	deps.Logger.Info("submitting session start operation", "project_id", target.ProjectID, "issue_id", target.IssueID)
	record, err := deps.DaemonClient.StartSessionOperation(ctx, daemonclient.StartSessionParams{
		IssueID:    target.IssueID,
		RepoDir:    target.RepoDir,
		BaseBranch: baseBranch,
	})
	if err != nil {
		return fmt.Errorf("failed to submit session start: %w", err)
	}

	if opts.Wait && !operationStateTerminal(record.State) {
		waitTimeout := opts.WaitTimeout
		if waitTimeout <= 0 {
			waitTimeout = sessionStartCommandTimeout
		}
		waitCtx, waitCancel := context.WithTimeout(context.Background(), waitTimeout)
		defer waitCancel()
		waited, waitErr := deps.DaemonClient.WaitForOperation(waitCtx, record.OperationID.String(), opts.PollInterval)
		if waitErr != nil {
			if errors.Is(waitErr, context.DeadlineExceeded) || errors.Is(waitErr, context.Canceled) {
				if waited.OperationID == "" {
					waited = record
				}
				return printPendingSessionStartOperation(waited, target.IssueID)
			}
			return fmt.Errorf("wait for operation %s: %w", record.OperationID, waitErr)
		}
		return printOperationOutcome(waited)
	}

	if operationStateTerminal(record.State) {
		return printOperationOutcome(record)
	}

	return printPendingSessionStartOperation(record, target.IssueID)
}

func printPendingSessionStartOperation(record protocol.OperationRecord, issueID string) error {
	if record.OperationID == "" || operationStateTerminal(record.State) {
		return printOperationOutcome(record)
	}

	fmt.Printf("Session start is still %s for %s.\n", record.State, issueID)
	fmt.Printf("Operation: %s (%s)\n", record.OperationID, record.State)
	if progress := operationProgressSummary(record); progress != "" {
		fmt.Printf("Progress: %s\n", progress)
	}
	fmt.Printf("Follow up: az operation get --id %s --wait\n", record.OperationID)
	fmt.Printf("Follow up: az session status %s\n", issueID)
	return nil
}

func WorktreeCreateCommand(deps *Dependencies, opts WorktreeCreateOptions) error {
	ctx, cancel := context.WithTimeout(context.Background(), sessionStartCommandTimeout)
	defer cancel()
	if err := ensureDaemon(ctx, deps, "cli"); err != nil {
		return err
	}

	target, err := resolveSessionStartIssueTarget(ctx, deps, opts.IssueID, opts.Project)
	if err != nil {
		return err
	}
	restoreProject := applyIssueProjectOverride(deps, target.ProjectID)
	defer restoreProject()

	baseBranch := strings.TrimSpace(opts.BaseBranch)
	if baseBranch == "" {
		baseBranch, err = resolveSessionStartBaseBranch(ctx, deps, target.Task)
		if err != nil {
			return err
		}
	}

	deps.Logger.Info("creating worktree", "project_id", target.ProjectID, "issue_id", target.IssueID, "base_branch", baseBranch)
	createResult, err := deps.DaemonClient.CreateWorktreeResult(ctx, target.IssueID, baseBranch)
	if err != nil {
		wrappedErr := fmt.Errorf("failed to create worktree for %s: %w", target.IssueID, err)
		if opts.JSON {
			if printErr := printJSON(map[string]any{
				"ok":          false,
				"project_id":  target.ProjectID,
				"issue_id":    target.IssueID,
				"base_branch": baseBranch,
				"error":       wrappedErr.Error(),
			}); printErr != nil {
				return fmt.Errorf("%w (also failed to write JSON error response: %v)", wrappedErr, printErr)
			}
		}
		return wrappedErr
	}
	worktree := createResult.Worktree
	baseBranch = strings.TrimSpace(createResult.BaseBranch)

	if opts.JSON {
		return printJSON(map[string]any{
			"project_id":  target.ProjectID,
			"issue_id":    target.IssueID,
			"base_branch": baseBranch,
			"worktree": map[string]any{
				"path":     worktree.Path,
				"branch":   worktree.Branch,
				"issue_id": worktree.IssueID,
			},
		})
	}

	fmt.Printf("Worktree ready: %s\n", worktree.Path)
	fmt.Printf("Branch: %s\n", worktree.Branch)
	fmt.Printf("Base: %s\n", baseBranch)
	return nil
}

func resolveSessionStartIssueTarget(ctx context.Context, deps *Dependencies, issueID, project string) (sessionIssueTarget, error) {
	trimmedProject := strings.TrimSpace(project)
	if trimmedProject == "" {
		return resolveSessionIssueTarget(ctx, deps, issueID)
	}
	trimmedIssueID := strings.TrimSpace(issueID)
	if trimmedIssueID == "" {
		return sessionIssueTarget{}, fmt.Errorf("issue id is required")
	}
	return resolveExplicitSessionIssueTarget(ctx, deps, trimmedIssueID, trimmedProject, trimmedIssueID)
}

func AttachCommand(deps *Dependencies, issueID string) error {
	ctx := context.Background()
	if err := ensureDaemon(ctx, deps, "cli"); err != nil {
		return err
	}
	target, err := resolveSessionIssueTarget(ctx, deps, issueID)
	if err != nil {
		return err
	}
	restoreProject := applyIssueProjectOverride(deps, target.ProjectID)
	defer restoreProject()

	deps.Logger.Info("attaching to session", "project_id", target.ProjectID, "issue_id", target.IssueID)

	resp, err := deps.DaemonClient.Command(ctx, newSessionRequest(commandSessionAttach, target.ProjectID, target.IssueID, ""))
	if err != nil {
		return fmt.Errorf("failed to attach to session: %w", err)
	}
	if err := responseError(resp, "failed to attach to session"); err != nil {
		return err
	}

	return printCommandOutput(resp)
}

func KillCommand(deps *Dependencies, issueID string) error {
	return KillCommandWithOptions(deps, issueID, SessionCommandOptions{})
}

func KillCommandWithOptions(deps *Dependencies, issueID string, opts SessionCommandOptions) error {
	ctx := context.Background()
	if err := ensureDaemon(ctx, deps, "cli"); err != nil {
		return err
	}
	target, err := resolveSessionIssueTarget(ctx, deps, issueID)
	if err != nil {
		return err
	}
	restoreProject := applyIssueProjectOverride(deps, target.ProjectID)
	defer restoreProject()

	deps.Logger.Info("killing session", "project_id", target.ProjectID, "issue_id", target.IssueID)

	resp, err := deps.DaemonClient.Command(ctx, newSessionRequest(commandSessionStop, target.ProjectID, target.IssueID, ""))
	if err != nil {
		return fmt.Errorf("failed to kill session: %w", err)
	}
	if err := responseError(resp, "failed to kill session"); err != nil {
		return err
	}

	return printCommandOutputWithWait(ctx, deps, resp, opts)
}

func StatusCommand(deps *Dependencies, issueID string) error {
	ctx, cancel := context.WithTimeout(context.Background(), daemonCommandTimeout)
	defer cancel()
	if err := ensureDaemon(ctx, deps, "cli"); err != nil {
		return err
	}

	targetProjectID := deps.ProjectID
	targetIssueID := strings.TrimSpace(issueID)
	if targetIssueID != "" {
		target, err := resolveSessionStatusTarget(deps, targetIssueID)
		if err != nil {
			return err
		}
		targetProjectID = target.ProjectID
		targetIssueID = target.IssueID
		restoreProject := applyIssueProjectOverride(deps, targetProjectID)
		defer restoreProject()
	}

	deps.Logger.Info("checking session status", "project_id", targetProjectID, "issue_id", targetIssueID)

	resp, err := deps.DaemonClient.Command(ctx, newSessionRequest(commandSessionStatus, targetProjectID, targetIssueID, ""))
	if err != nil {
		return fmt.Errorf("failed to list tmux sessions: %w", err)
	}
	if err := responseError(resp, "failed to list tmux sessions"); err != nil {
		return err
	}

	return printCommandOutput(resp)
}

func SessionRestartAllCommand(deps *Dependencies, opts SessionRestartAllOptions) error {
	ctx, cancel := context.WithTimeout(context.Background(), daemonCommandTimeout)
	defer cancel()
	if err := ensureDaemon(ctx, deps, "cli"); err != nil {
		return err
	}

	result, err := deps.DaemonClient.RestartAllSessions(ctx, daemonclient.RestartAllSessionsParams{
		ForceBusy: opts.ForceBusy,
		Yolo:      opts.Yolo,
	})
	if err != nil {
		return fmt.Errorf("failed to restart sessions: %w", err)
	}
	if opts.JSON {
		if err := printJSON(result); err != nil {
			return err
		}
		if result.Failed > 0 {
			return fmt.Errorf("failed to restart %d session(s)", result.Failed)
		}
		return nil
	}
	printSessionRestartAllResult(result)
	if result.Failed > 0 {
		return fmt.Errorf("failed to restart %d session(s)", result.Failed)
	}
	return nil
}

func printSessionRestartAllResult(result protocol.SessionRestartAllResponseBody) {
	fmt.Printf("Restarted %d session(s)", result.Restarted)
	if result.Skipped > 0 || result.Failed > 0 {
		fmt.Printf(" (%d skipped, %d failed)", result.Skipped, result.Failed)
	}
	fmt.Println()
	for _, session := range result.Sessions {
		issueID := strings.TrimSpace(session.IssueID.String())
		if issueID == "" {
			issueID = "(unknown issue)"
		}
		status := "restarted"
		if session.Skipped {
			status = "skipped: " + strings.TrimSpace(session.Reason)
		}
		if strings.TrimSpace(session.Error) != "" {
			status = "failed: " + strings.TrimSpace(session.Error)
		}
		fmt.Printf("- %s (%s, activity=%s): %s\n", issueID, session.SessionID, session.Activity, status)
	}
}

func resolveSessionStatusTarget(deps *Dependencies, issueID string) (sessionIssueTarget, error) {
	trimmed := strings.TrimSpace(issueID)
	if trimmed == "" {
		return sessionIssueTarget{}, fmt.Errorf("issue id is required")
	}
	if projectPart, issuePart, ok := splitExplicitSessionIssueTarget(trimmed); ok {
		return resolveExplicitSessionStatusTarget(deps, trimmed, projectPart, issuePart)
	}
	parsedIssueID, err := naming.ParseIssueID(trimmed)
	if err != nil {
		return sessionIssueTarget{}, fmt.Errorf("invalid issue id %q: %w", issueID, err)
	}
	return sessionIssueTarget{ProjectID: deps.ProjectID, RepoDir: deps.RepoDir, IssueID: parsedIssueID.String()}, nil
}

func resolveExplicitSessionStatusTarget(deps *Dependencies, raw, projectPart, issuePart string) (sessionIssueTarget, error) {
	issueID, err := naming.ParseIssueID(issuePart)
	if err != nil {
		return sessionIssueTarget{}, fmt.Errorf("invalid project-prefixed issue id %q: %w", raw, err)
	}
	project, ok := findSessionProjectCandidate(deps, projectPart)
	if !ok {
		return sessionIssueTarget{}, fmt.Errorf("unknown project in project-prefixed issue id %q: %s", raw, projectPart)
	}
	return sessionIssueTarget{ProjectID: project.Route, RepoDir: project.Path, IssueID: issueID.String()}, nil
}

func resolveSessionIssueTarget(ctx context.Context, deps *Dependencies, issueID string) (sessionIssueTarget, error) {
	trimmed := strings.TrimSpace(issueID)
	if trimmed == "" {
		return sessionIssueTarget{}, fmt.Errorf("issue id is required")
	}
	if projectPart, issuePart, ok := splitExplicitSessionIssueTarget(trimmed); ok {
		return resolveExplicitSessionIssueTarget(ctx, deps, trimmed, projectPart, issuePart)
	}
	if _, err := naming.ParseIssueID(trimmed); err != nil {
		return sessionIssueTarget{}, fmt.Errorf("invalid issue id %q: %w", issueID, err)
	}

	if task, taskContext, ok, err := lookupSessionTaskInProject(ctx, deps, deps.ProjectID, trimmed); err != nil {
		return sessionIssueTarget{}, err
	} else if ok {
		return sessionIssueTarget{ProjectID: deps.ProjectID, RepoDir: deps.RepoDir, IssueID: trimmed, Task: task, TaskContext: taskContext}, nil
	}

	matches, err := resolveCanonicalSessionIssueTargets(ctx, deps, trimmed)
	if err != nil {
		return sessionIssueTarget{}, err
	}
	switch len(matches) {
	case 0:
		return sessionIssueTarget{}, fmt.Errorf("issue not found: %s", trimmed)
	case 1:
		return matches[0], nil
	default:
		projects := make([]string, 0, len(matches))
		for _, match := range matches {
			projects = append(projects, match.ProjectID)
		}
		sort.Strings(projects)
		return sessionIssueTarget{}, fmt.Errorf("ambiguous tmux session issue id %q; matched projects: %s", trimmed, strings.Join(projects, ", "))
	}
}

func splitExplicitSessionIssueTarget(raw string) (projectID, issueID string, ok bool) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", "", false
	}
	for _, sep := range []string{":", "/"} {
		if before, after, found := strings.Cut(trimmed, sep); found {
			before = strings.TrimSpace(before)
			after = strings.TrimSpace(after)
			if before != "" && after != "" {
				return before, after, true
			}
		}
	}
	return "", "", false
}

func resolveExplicitSessionIssueTarget(ctx context.Context, deps *Dependencies, raw, projectPart, issuePart string) (sessionIssueTarget, error) {
	issueID, err := naming.ParseIssueID(issuePart)
	if err != nil {
		return sessionIssueTarget{}, fmt.Errorf("invalid project-prefixed issue id %q: %w", raw, err)
	}
	project, ok := findSessionProjectCandidate(deps, projectPart)
	if !ok {
		return sessionIssueTarget{}, fmt.Errorf("unknown project in project-prefixed issue id %q: %s", raw, projectPart)
	}
	task, taskContext, found, err := lookupSessionTaskInProject(ctx, deps, project.Route, issueID.String())
	if err != nil {
		return sessionIssueTarget{}, err
	}
	if !found {
		return sessionIssueTarget{}, fmt.Errorf("issue not found in project %s: %s", project.Route, issueID.String())
	}
	return sessionIssueTarget{ProjectID: project.Route, RepoDir: project.Path, IssueID: issueID.String(), Task: task, TaskContext: taskContext}, nil
}

func resolveCanonicalSessionIssueTargets(ctx context.Context, deps *Dependencies, raw string) ([]sessionIssueTarget, error) {
	candidates := knownSessionProjectCandidates(deps)
	matchesByKey := map[string]sessionIssueTarget{}
	for _, candidate := range candidates {
		for _, scope := range candidate.Scopes {
			parsedIssueID, ok := naming.ParseIssueIDFromSessionName(raw, scope)
			if !ok || strings.EqualFold(parsedIssueID, raw) {
				continue
			}
			task, taskContext, found, err := lookupSessionTaskInProject(ctx, deps, candidate.Route, parsedIssueID)
			if err != nil {
				return nil, err
			}
			if !found {
				continue
			}
			key := candidate.Route + "\x00" + strings.ToLower(parsedIssueID)
			matchesByKey[key] = sessionIssueTarget{ProjectID: candidate.Route, RepoDir: candidate.Path, IssueID: parsedIssueID, Task: task, TaskContext: taskContext}
		}
	}
	matches := make([]sessionIssueTarget, 0, len(matchesByKey))
	for _, match := range matchesByKey {
		matches = append(matches, match)
	}
	sort.Slice(matches, func(i, j int) bool {
		if matches[i].ProjectID == matches[j].ProjectID {
			return matches[i].IssueID < matches[j].IssueID
		}
		return matches[i].ProjectID < matches[j].ProjectID
	})
	return matches, nil
}

func lookupSessionTaskInProject(ctx context.Context, deps *Dependencies, projectID, issueID string) (domain.Task, []domain.Task, bool, error) {
	restoreProject := applyIssueProjectOverride(deps, projectID)
	defer restoreProject()

	task, taskContext, ok, err := loadIssueMetadataTask(ctx, deps, issueID)
	if err != nil {
		return domain.Task{}, nil, false, fmt.Errorf("failed to validate issue %s in project %s: %w", issueID, projectID, err)
	}
	return task, taskContext, ok, nil
}

func findSessionProjectCandidate(deps *Dependencies, projectID string) (sessionProjectCandidate, bool) {
	normalized := protocol.NormalizeProjectID(projectID)
	for _, candidate := range knownSessionProjectCandidates(deps) {
		for _, alias := range candidate.Aliases {
			if protocol.NormalizeProjectID(alias) == normalized {
				return candidate, true
			}
		}
	}
	return sessionProjectCandidate{}, false
}

func knownSessionProjectCandidates(deps *Dependencies) []sessionProjectCandidate {
	candidates := make([]sessionProjectCandidate, 0, 4)
	if deps != nil {
		candidates = appendSessionProjectCandidate(candidates, "", deps.ProjectID, deps.RepoDir)
	}
	if registry, err := config.LoadProjectsRegistry(); err == nil && registry != nil {
		for _, project := range registry.Projects {
			candidates = appendSessionProjectCandidate(candidates, project.Name, "", project.Path)
		}
	}
	return dedupeSessionProjectCandidates(candidates)
}

func appendSessionProjectCandidate(candidates []sessionProjectCandidate, name, route, path string) []sessionProjectCandidate {
	name = strings.TrimSpace(name)
	route = strings.TrimSpace(route)
	path = strings.TrimSpace(path)
	if path != "" {
		if hashProjectID, err := config.ProjectIDForRoot(path); err == nil && strings.TrimSpace(hashProjectID) != "" {
			route = hashProjectID
		}
	}
	if route == "" {
		route = firstNonEmptyString(name, filepath.Base(path))
	}
	route = protocol.NormalizeProjectID(route)
	if route == "" {
		return candidates
	}

	aliases := uniqueTrimmedStrings([]string{route, name, filepath.Base(path)})
	scopes := uniqueTrimmedStrings([]string{route, path})
	return append(candidates, sessionProjectCandidate{
		Route:   route,
		Name:    name,
		Path:    path,
		Aliases: aliases,
		Scopes:  scopes,
	})
}

func dedupeSessionProjectCandidates(candidates []sessionProjectCandidate) []sessionProjectCandidate {
	byRoute := make(map[string]sessionProjectCandidate, len(candidates))
	order := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		route := protocol.NormalizeProjectID(candidate.Route)
		if route == "" {
			continue
		}
		candidate.Route = route
		existing, ok := byRoute[route]
		if !ok {
			byRoute[route] = candidate
			order = append(order, route)
			continue
		}
		existing.Aliases = uniqueTrimmedStrings(append(existing.Aliases, candidate.Aliases...))
		existing.Scopes = uniqueTrimmedStrings(append(existing.Scopes, candidate.Scopes...))
		if existing.Name == "" {
			existing.Name = candidate.Name
		}
		if existing.Path == "" {
			existing.Path = candidate.Path
		}
		byRoute[route] = existing
	}
	sort.Strings(order)
	out := make([]sessionProjectCandidate, 0, len(order))
	for _, route := range order {
		out = append(out, byRoute[route])
	}
	return out
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func SessionResolveConflictCommand(deps *Dependencies, opts SessionResolveConflictOptions) error {
	ctx, cancel := context.WithTimeout(context.Background(), daemonCommandTimeout)
	defer cancel()

	trimmedIssueID := strings.TrimSpace(opts.IssueID)
	if trimmedIssueID == "" {
		return fmt.Errorf("issue id is required")
	}
	issueID, err := naming.ParseIssueID(trimmedIssueID)
	if err != nil {
		return fmt.Errorf("invalid issue id %q: %w", opts.IssueID, err)
	}

	if err := ensureDaemon(ctx, deps, "cli"); err != nil {
		return err
	}

	out, err := deps.DaemonClient.ResolveConflict(ctx, daemonclient.ResolveConflictParams{
		IssueID:       issueID.String(),
		Worktree:      opts.Worktree,
		ConflictFiles: append([]string(nil), opts.ConflictFiles...),
		Prompt:        opts.Prompt,
	})
	if err != nil {
		return fmt.Errorf("failed to resolve conflicts for %s: %w", issueID.String(), err)
	}

	fmt.Printf("Conflict resolution agent launched for %s\n", issueID.String())
	fmt.Printf("Worktree: %s\n", out.Worktree)
	fmt.Printf("Window: %s\n", out.WindowName)
	return nil
}

func validateSessionIssueID(ctx context.Context, deps *Dependencies, issueID string) (domain.Task, error) {
	trimmed := strings.TrimSpace(issueID)
	if trimmed == "" {
		return domain.Task{}, fmt.Errorf("issue id is required")
	}
	if _, err := naming.ParseIssueID(trimmed); err != nil {
		return domain.Task{}, fmt.Errorf("invalid issue id %q: %w", issueID, err)
	}

	task, _, ok, err := loadIssueMetadataTask(ctx, deps, trimmed)
	if err != nil {
		return domain.Task{}, fmt.Errorf("failed to validate issue %s: %w", trimmed, err)
	}
	if !ok {
		return domain.Task{}, fmt.Errorf("issue not found: %s", trimmed)
	}
	return task, nil
}

func resolveSessionStartBaseBranch(ctx context.Context, deps *Dependencies, task domain.Task) (string, error) {
	baseBranch := resolveCLIBaseBranch(deps.Config)
	return resolveParentWorktreeBaseBranch(ctx, deps, baseBranch, task.ID.String())
}

func loadIssueMetadataTask(ctx context.Context, deps *Dependencies, issueID string) (domain.Task, []domain.Task, bool, error) {
	snapshot, err := deps.DaemonClient.GetManyTaskSnapshotWithAncestorsNoDependentsMetadataOnly(ctx, []string{issueID})
	if err != nil {
		return domain.Task{}, nil, false, err
	}
	task, ok := findTaskByID(snapshot.Tasks, issueID)
	return task, snapshot.Tasks, ok, nil
}

func loadIssueMetadataTaskWithDaemonAutostartRetry(ctx context.Context, deps *Dependencies, issueID string) (domain.Task, []domain.Task, bool, error) {
	snapshot, err := commandWithDaemonAutostartRetry(ctx, deps, func(callCtx context.Context) (daemonclient.TaskSnapshot, error) {
		return deps.DaemonClient.GetManyTaskSnapshotWithAncestorsNoDependentsMetadataOnly(callCtx, []string{issueID})
	})
	if err != nil {
		return domain.Task{}, nil, false, err
	}
	task, ok := findTaskByID(snapshot.Tasks, issueID)
	return task, snapshot.Tasks, ok, nil
}

func resolveParentWorktreeBaseBranch(ctx context.Context, deps *Dependencies, baseBranch, issueIDForError string) (string, error) {
	target, err := deps.DaemonClient.TaskMergeBaseTarget(ctx, issueIDForError, baseBranch, true)
	if err != nil {
		return "", fmt.Errorf("resolve parent worktree branch for %s: %w", issueIDForError, err)
	}
	if branch := strings.TrimSpace(target.Branch); branch != "" {
		return branch, nil
	}
	return baseBranch, nil
}

// BranchMergeToBaseCommand merges one issue worktree branch into its resolved target branch using daemon git commands.
func BranchMergeToBaseCommand(deps *Dependencies, issueID string) error {
	return BranchMergeToBaseCommandWithOptions(deps, BranchMergeToBaseOptions{
		IssueID:           issueID,
		AllowBaseForChild: false,
	})
}

func BranchMergeToBaseCommandWithOptions(deps *Dependencies, opts BranchMergeToBaseOptions) error {
	result, err := runBranchMergeToBase(deps, opts)
	if err != nil {
		return err
	}
	printBranchMergeToBaseResult(result)
	return nil
}

type branchMergeToBaseCommandResult struct {
	IssueID      string
	SourceBranch string
	BaseBranch   string
	Message      string
	Phases       []commandPhaseTiming
}

type mergeHookFailureFixer struct {
	ID string
}

type commandPhaseTiming struct {
	Name    string
	Elapsed time.Duration
}

func runBranchMergeToBase(deps *Dependencies, opts BranchMergeToBaseOptions) (branchMergeToBaseCommandResult, error) {
	ctx, cancel := context.WithTimeout(context.Background(), branchMergeToBaseTimeout)
	defer cancel()
	var phases []commandPhaseTiming
	recordPhase := func(name string, startedAt time.Time) {
		phases = append(phases, commandPhaseTiming{Name: name, Elapsed: time.Since(startedAt)})
		latencytrace.LogPhase(deps.Logger, "cli", "branch.merge."+name, startedAt, "issue_id", opts.IssueID)
	}
	wrapPhaseErr := func(name string, err error) error {
		if err == nil {
			return nil
		}
		if errors.Is(err, context.DeadlineExceeded) {
			return fmt.Errorf("phase %s timed out after %s while running az branch merge: %w", name, branchMergeToBaseTimeout, err)
		}
		return fmt.Errorf("phase %s failed while running az branch merge: %w", name, err)
	}

	phaseStartedAt := time.Now()
	if err := ensureDaemon(ctx, deps, "cli"); err != nil {
		recordPhase("ensure_daemon", phaseStartedAt)
		return branchMergeToBaseCommandResult{}, wrapPhaseErr("ensure_daemon", err)
	}
	recordPhase("ensure_daemon", phaseStartedAt)

	phaseStartedAt = time.Now()
	source, err := resolveMergeToBaseSourceWorktree(ctx, deps, opts.IssueID)
	if err != nil {
		recordPhase("resolve_source", phaseStartedAt)
		return branchMergeToBaseCommandResult{}, wrapPhaseErr("resolve_source", err)
	}
	recordPhase("resolve_source", phaseStartedAt)

	phaseStartedAt = time.Now()
	target, decision, err := resolveMergeToBaseTarget(ctx, deps, source.IssueID, opts.AllowBaseForChild)
	if err != nil {
		recordPhase("resolve_target", phaseStartedAt)
		return branchMergeToBaseCommandResult{}, wrapPhaseErr("resolve_target", err)
	}
	recordPhase("resolve_target", phaseStartedAt)
	baseBranch := target.Branch
	baseWorktree := strings.TrimSpace(target.WorktreePath)
	branchAttached := target.BranchAttached
	if baseWorktree == "" {
		phaseStartedAt = time.Now()
		if attached, err := deps.DaemonClient.GitWorktreeForBranch(ctx, baseBranch); err == nil && attached.Found && strings.TrimSpace(attached.Worktree) != "" {
			baseWorktree = strings.TrimSpace(attached.Worktree)
			branchAttached = true
		}
		recordPhase("resolve_target_worktree", phaseStartedAt)
	}
	if baseWorktree == "" {
		baseWorktree = strings.TrimSpace(deps.RepoDir)
		if baseWorktree == "" {
			baseWorktree = "."
		}
	}
	deps.Logger.Info("resolved branch merge target",
		"issue_id", source.IssueID,
		"selected_target", target.TargetID,
		"selected_branch", target.Branch,
		"branch_attached", branchAttached,
		"decision_reason", decision.Reason,
		"ancestor_chain", strings.Join(decision.AncestorChain, " -> "),
		"allow_base_for_child", opts.AllowBaseForChild,
	)

	phaseStartedAt = time.Now()
	if err := checkMergeToBasePreflight(ctx, deps, source, baseWorktree); err != nil {
		recordPhase("preflight", phaseStartedAt)
		return branchMergeToBaseCommandResult{}, wrapPhaseErr("preflight", err)
	}
	recordPhase("preflight", phaseStartedAt)

	deps.Logger.Info("merging issue branch into target branch",
		"issue_id", source.IssueID,
		"source_worktree", source.Path,
		"source_branch", source.Branch,
		"target_worktree", baseWorktree,
		"base_branch", baseBranch,
	)

	if !branchAttached {
		phaseStartedAt = time.Now()
		if _, err := deps.DaemonClient.GitCheckout(ctx, baseWorktree, baseBranch); err != nil {
			recordPhase("checkout", phaseStartedAt)
			return branchMergeToBaseCommandResult{}, wrapPhaseErr("checkout", wrapPendingGitOperation("checkout", err))
		}
		recordPhase("checkout", phaseStartedAt)
	}
	phaseStartedAt = time.Now()
	result, err := deps.DaemonClient.GitMerge(ctx, baseWorktree, source.Branch)
	if err != nil {
		recordPhase("merge", phaseStartedAt)
		return branchMergeToBaseCommandResult{}, wrapPhaseErr("merge", wrapPendingGitOperation("merge", err))
	}
	recordPhase("merge", phaseStartedAt)
	if !result.Result.Success {
		details := strings.TrimSpace(result.Result.Message)
		if len(result.Result.ConflictFiles) > 0 {
			details = strings.TrimSpace(details + "\nconflicts: " + strings.Join(result.Result.ConflictFiles, ", "))
		}
		if details == "" {
			details = "merge did not complete successfully"
		}
		if result.Result.HasConflicts || len(result.Result.ConflictFiles) > 0 {
			return branchMergeToBaseCommandResult{}, fmt.Errorf("merge %s into %s failed: %s", source.Branch, baseBranch, details)
		}
		fixer, fixerErr := createMergeHookFailureFixer(ctx, deps, mergeHookFailureFixerParams{
			SourceIssueID:  source.IssueID,
			SourceBranch:   source.Branch,
			SourceWorktree: source.Path,
			TargetID:       target.TargetID,
			TargetBranch:   baseBranch,
			TargetWorktree: baseWorktree,
			MergeOutput:    details,
		})
		message := fmt.Sprintf("merge %s into %s failed: %s", source.Branch, baseBranch, details)
		if fixerErr != nil {
			return branchMergeToBaseCommandResult{}, fmt.Errorf("%s\nfixer issue creation failed: %w", message, fixerErr)
		}
		if fixer.ID != "" {
			message = fmt.Sprintf("%s\ncreated fixer issue %s", message, fixer.ID)
		}
		return branchMergeToBaseCommandResult{}, fmt.Errorf("%s", message)
	}
	return branchMergeToBaseCommandResult{
		IssueID:      source.IssueID,
		SourceBranch: source.Branch,
		BaseBranch:   baseBranch,
		Message:      result.Result.Message,
		Phases:       phases,
	}, nil
}

func printBranchMergeToBaseResult(result branchMergeToBaseCommandResult) {
	if strings.TrimSpace(result.Message) != "" {
		if strings.HasSuffix(result.Message, "\n") {
			fmt.Print(result.Message)
		} else {
			fmt.Println(result.Message)
		}
	}
	fmt.Printf("Merged %s into %s (%s)\n", result.SourceBranch, result.BaseBranch, result.IssueID)
	printCommandPhases(result.Phases)
}

func printCommandPhases(phases []commandPhaseTiming) {
	if len(phases) == 0 {
		return
	}
	fmt.Println("- Phase timings:")
	for _, phase := range phases {
		name := strings.TrimSpace(phase.Name)
		if name == "" {
			continue
		}
		fmt.Printf("  - %s: %s\n", name, phase.Elapsed.Round(time.Millisecond))
	}
}

type mergeHookFailureFixerParams struct {
	SourceIssueID  string
	SourceBranch   string
	SourceWorktree string
	TargetID       string
	TargetBranch   string
	TargetWorktree string
	MergeOutput    string
}

func createMergeHookFailureFixer(ctx context.Context, deps *Dependencies, params mergeHookFailureFixerParams) (mergeHookFailureFixer, error) {
	if deps == nil || deps.DaemonClient == nil {
		return mergeHookFailureFixer{}, fmt.Errorf("daemon client unavailable")
	}
	if strings.TrimSpace(params.SourceIssueID) == "" {
		return mergeHookFailureFixer{}, fmt.Errorf("source issue id required")
	}
	notes := buildMergeHookFailureFixerNotes(params)
	fixerID, err := deps.DaemonClient.CreateTask(ctx, daemonclient.TaskCreateParams{
		Title:       fmt.Sprintf("Fix merge hook/check failure for %s", params.SourceIssueID),
		Description: "Repair the hook/check failure that prevented issue integration, then retry the recorded merge command.",
		Type:        domain.TypeTask,
		Priority:    domain.P4,
		Status:      domain.StatusOpen,
		Notes:       notes,
	})
	if err != nil {
		return mergeHookFailureFixer{}, fmt.Errorf("create fixer issue: %w", err)
	}
	if strings.TrimSpace(fixerID) == "" {
		return mergeHookFailureFixer{}, fmt.Errorf("create fixer issue returned empty id")
	}
	sourceID, err := naming.ParseIssueID(params.SourceIssueID)
	if err != nil {
		return mergeHookFailureFixer{}, fmt.Errorf("parse source issue id: %w", err)
	}
	parsedFixerID, err := naming.ParseIssueID(fixerID)
	if err != nil {
		return mergeHookFailureFixer{}, fmt.Errorf("parse fixer issue id: %w", err)
	}
	if err := deps.DaemonClient.AddTaskDependency(ctx, daemonclient.TaskDependencyParams{
		TaskID:      sourceID,
		DependsOnID: parsedFixerID,
		Type:        string(domain.DependencyBlocks),
	}); err != nil {
		return mergeHookFailureFixer{}, fmt.Errorf("add fixer blocker dependency: %w", err)
	}
	return mergeHookFailureFixer{ID: fixerID}, nil
}

func buildMergeHookFailureFixerNotes(params mergeHookFailureFixerParams) string {
	lines := []string{
		"Auto-created after daemon-backed merge failed because hooks/checks rejected the merge commit.",
		fmt.Sprintf("Source issue: %s", strings.TrimSpace(params.SourceIssueID)),
		fmt.Sprintf("Source branch: %s", strings.TrimSpace(params.SourceBranch)),
		fmt.Sprintf("Source worktree: %s", strings.TrimSpace(params.SourceWorktree)),
		fmt.Sprintf("Target: %s", strings.TrimSpace(params.TargetID)),
		fmt.Sprintf("Target branch: %s", strings.TrimSpace(params.TargetBranch)),
		fmt.Sprintf("Target worktree: %s", strings.TrimSpace(params.TargetWorktree)),
		fmt.Sprintf("Retry: az issue close --id %s", strings.TrimSpace(params.SourceIssueID)),
		"Hook/check output:",
	}
	output := strings.TrimSpace(params.MergeOutput)
	if output == "" {
		output = "merge did not complete successfully"
	}
	lines = append(lines, output)
	return strings.Join(lines, "\n")
}

type mergeBaseTarget struct {
	TargetID       string
	Branch         string
	WorktreePath   string
	BranchAttached bool
}

type mergeTargetDecision struct {
	Reason        string
	AncestorChain []string
}

func resolveMergeToBaseTarget(ctx context.Context, deps *Dependencies, issueID string, allowBaseForChild bool) (mergeBaseTarget, mergeTargetDecision, error) {
	defaultBase := resolveCLIBaseBranch(deps.Config)
	target, err := deps.DaemonClient.TaskMergeBaseTarget(ctx, issueID, defaultBase, allowBaseForChild)
	if err != nil {
		return mergeBaseTarget{}, mergeTargetDecision{}, err
	}
	return mergeBaseTarget{
			TargetID:       target.TargetID,
			Branch:         target.Branch,
			WorktreePath:   target.WorktreePath,
			BranchAttached: target.BranchAttached,
		}, mergeTargetDecision{
			Reason:        target.Reason,
			AncestorChain: append([]string(nil), target.AncestorChain...),
		}, nil
}

func BranchAgentMergeCommand(deps *Dependencies, opts BranchAgentMergeOptions) error {
	ctx, cancel := context.WithTimeout(context.Background(), branchMergeToBaseTimeout)
	defer cancel()
	if err := ensureDaemon(ctx, deps, "cli"); err != nil {
		return err
	}

	source, err := resolveMergeToBaseSourceWorktree(ctx, deps, opts.IssueID)
	if err != nil {
		return err
	}

	targetID := strings.TrimSpace(opts.Target)
	if targetID == "" {
		targetID = "base"
	}
	requestedBaseTarget := isBaseMergeTarget(targetID)
	displayTargetID := targetID

	targetWorktree := strings.TrimSpace(deps.RepoDir)
	targetRef := resolveCLIBaseBranch(deps.Config)
	agentIssueID := source.IssueID
	agentWorktree := source.Path
	if requestedBaseTarget {
		baseTarget, _, err := resolveMergeToBaseTarget(ctx, deps, source.IssueID, false)
		if err != nil {
			return err
		}
		targetID = baseTarget.TargetID
		targetRef = baseTarget.Branch
		if strings.TrimSpace(baseTarget.WorktreePath) != "" {
			targetWorktree = baseTarget.WorktreePath
		} else if attached, err := deps.DaemonClient.GitWorktreeForBranch(ctx, targetRef); err == nil && attached.Found && strings.TrimSpace(attached.Worktree) != "" {
			targetWorktree = strings.TrimSpace(attached.Worktree)
		}
	} else {
		target, err := resolveWorktreeForIssue(ctx, deps, targetID)
		if err != nil {
			return err
		}
		targetID = target.IssueID
		targetWorktree = target.Path
		targetRef = "HEAD"
		agentIssueID = target.IssueID
		agentWorktree = target.Path
	}
	if strings.TrimSpace(targetWorktree) == "" {
		return fmt.Errorf("target worktree unavailable")
	}

	preflight, err := deps.DaemonClient.GitMergePreflight(ctx, source.IssueID, source.Path, targetID, targetWorktree, targetRef, source.Branch)
	if err != nil {
		return fmt.Errorf("merge preflight failed: %w", err)
	}
	if preflight.Clean {
		fmt.Printf("Merge preflight clean for %s -> %s; no agent needed.\n", source.IssueID, displayTargetID)
		if requestedBaseTarget {
			fmt.Printf("Run: az branch merge %s\n", source.IssueID)
		}
		return nil
	}

	conflictFiles := preflight.ConflictFiles
	if len(conflictFiles) == 0 {
		conflictFiles = append(conflictFiles, preflight.SourceFiles...)
		conflictFiles = append(conflictFiles, preflight.TargetFiles...)
	}
	promptTargetID := targetID
	if requestedBaseTarget {
		promptTargetID = "base"
	}
	prompt := buildBranchAgentMergePrompt(source, promptTargetID, targetWorktree, targetRef, conflictFiles)
	out, err := deps.DaemonClient.ResolveConflict(ctx, daemonclient.ResolveConflictParams{
		IssueID:       agentIssueID,
		Worktree:      agentWorktree,
		ConflictFiles: append([]string(nil), conflictFiles...),
		Prompt:        prompt,
	})
	if err != nil {
		return fmt.Errorf("launch merge agent for %s -> %s: %w", source.IssueID, displayTargetID, err)
	}

	fmt.Printf("Agent merge launched for %s -> %s\n", source.IssueID, displayTargetID)
	fmt.Printf("Worktree: %s\n", out.Worktree)
	fmt.Printf("Window: %s\n", out.WindowName)
	if len(conflictFiles) > 0 {
		fmt.Printf("Predicted conflicts: %s\n", strings.Join(uniqueTrimmedStrings(conflictFiles), ", "))
	}
	return nil
}

func isBaseMergeTarget(targetID string) bool {
	normalized := strings.ToLower(strings.TrimSpace(targetID))
	return normalized == "" || normalized == "base" || normalized == "main"
}

func resolveWorktreeForIssue(ctx context.Context, deps *Dependencies, issueID string) (daemonclient.Worktree, error) {
	issueID = strings.TrimSpace(issueID)
	if issueID == "" {
		return daemonclient.Worktree{}, fmt.Errorf("target issue id is required")
	}
	worktrees, err := deps.DaemonClient.ListWorktrees(ctx)
	if err != nil {
		return daemonclient.Worktree{}, fmt.Errorf("list daemon worktrees: %w", err)
	}
	for _, wt := range worktrees {
		if naming.IssueIDsEqual(wt.IssueID, issueID) {
			return wt, nil
		}
	}
	return daemonclient.Worktree{}, fmt.Errorf("worktree not found for issue %s", issueID)
}

func buildBranchAgentMergePrompt(source daemonclient.Worktree, targetID, targetWorktree, targetRef string, conflictFiles []string) string {
	targetID = strings.TrimSpace(targetID)
	targetRef = strings.TrimSpace(targetRef)
	if targetRef == "" {
		targetRef = "target branch"
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Auto-merge the blocked preflight for %s -> %s.\n\n", source.IssueID, targetID)
	b.WriteString("Start by running `az prime`. Inspect the predicted conflict files, perform the merge in the appropriate worktree, resolve conflicts, commit the resolution, and run focused validation.\n\n")
	if isBaseMergeTarget(targetID) {
		fmt.Fprintf(&b, "This preflight blocked merging source branch %s into base ref %s. Work in source issue %s at %s: merge %s into %s, resolve conflicts there, and leave the source branch clean so `az branch merge %s` can be retried.\n", source.Branch, targetRef, source.IssueID, source.Path, targetRef, source.Branch, source.IssueID)
	} else {
		fmt.Fprintf(&b, "This preflight blocked merging source branch %s from %s into target issue %s at %s. Work in the target worktree, merge %s, resolve conflicts there, and leave the target branch clean.\n", source.Branch, source.Path, targetID, targetWorktree, source.Branch)
	}
	if files := uniqueTrimmedStrings(conflictFiles); len(files) > 0 {
		b.WriteString("\nPredicted conflict files:\n")
		for _, file := range files {
			fmt.Fprintf(&b, "- %s\n", file)
		}
	}
	b.WriteString("\nDo not push or create a PR unless explicitly asked. Leave a concise summary of resolved files and validation results.")
	return b.String()
}

func resolveMergeToBaseSourceWorktree(ctx context.Context, deps *Dependencies, issueID string) (daemonclient.Worktree, error) {
	worktrees, err := deps.DaemonClient.ListWorktrees(ctx)
	if err != nil {
		return daemonclient.Worktree{}, fmt.Errorf("list daemon worktrees: %w", err)
	}
	if len(worktrees) == 0 {
		return daemonclient.Worktree{}, fmt.Errorf("no daemon worktrees found; start the issue session first")
	}

	trimmedIssueID := strings.TrimSpace(issueID)
	if trimmedIssueID == "" {
		trimmedIssueID = strings.TrimSpace(os.Getenv("AZEDARACH_ISSUE_ID"))
	}
	if trimmedIssueID != "" {
		for _, wt := range worktrees {
			if naming.IssueIDsEqual(wt.IssueID, trimmedIssueID) {
				return wt, nil
			}
		}
		return daemonclient.Worktree{}, fmt.Errorf("worktree not found for issue %s", trimmedIssueID)
	}

	cwd, err := os.Getwd()
	if err != nil {
		return daemonclient.Worktree{}, fmt.Errorf("resolve working directory: %w", err)
	}
	absCWD, err := filepath.Abs(cwd)
	if err != nil {
		return daemonclient.Worktree{}, fmt.Errorf("resolve working directory path: %w", err)
	}

	for _, wt := range worktrees {
		if samePath(wt.Path, absCWD) {
			return wt, nil
		}
	}
	return daemonclient.Worktree{}, fmt.Errorf("could not infer issue from current worktree %q; pass issue ID: az branch merge <issue-id>", absCWD)
}

func samePath(left, right string) bool {
	return filepath.Clean(left) == filepath.Clean(right)
}

func resolveCLIBaseBranch(cfg *config.Config) string {
	if cfg == nil {
		return "main"
	}
	base := strings.TrimSpace(cfg.Git.BaseBranch)
	if base == "" {
		return "main"
	}
	return base
}

func checkMergeToBasePreflight(ctx context.Context, deps *Dependencies, source daemonclient.Worktree, targetWorktree string) error {
	sourceStatus, err := deps.DaemonClient.GitStatus(ctx, source.Path)
	if err != nil {
		return fmt.Errorf("read source status for %s: %w", source.IssueID, err)
	}
	targetStatus, err := deps.DaemonClient.GitStatus(ctx, targetWorktree)
	if err != nil {
		return fmt.Errorf("read target branch status: %w", err)
	}
	sourceDirtyFiles := dirtyFilesFromGitStatus(sourceStatus)
	targetDirtyFiles := dirtyFilesFromGitStatus(targetStatus)

	reasons := make([]string, 0, 2)
	if len(sourceDirtyFiles) > 0 {
		reasons = append(reasons, fmt.Sprintf("source %s is not clean: %s", source.IssueID, summarizeGitStatusCounts(sourceStatus)))
	}
	if len(targetDirtyFiles) > 0 {
		reasons = append(reasons, fmt.Sprintf("target branch is not clean: %s", summarizeGitStatusCounts(targetStatus)))
	}
	if len(reasons) == 0 {
		return nil
	}

	lines := []string{"merge preflight failed:"}
	for _, reason := range reasons {
		lines = append(lines, "- "+reason)
	}
	if len(sourceDirtyFiles) > 0 {
		lines = append(lines, fmt.Sprintf("- source dirty files: %s", strings.Join(sourceDirtyFiles, ", ")))
	}
	if len(targetDirtyFiles) > 0 {
		lines = append(lines, fmt.Sprintf("- target dirty files: %s", strings.Join(targetDirtyFiles, ", ")))
	}
	return errors.New(strings.Join(lines, "\n"))
}

func summarizeGitStatusCounts(status daemonclient.GitStatus) string {
	parts := make([]string, 0, 5)
	if n := len(status.Staged); n > 0 {
		parts = append(parts, fmt.Sprintf("%d staged", n))
	}
	if n := len(status.Modified); n > 0 {
		parts = append(parts, fmt.Sprintf("%d modified", n))
	}
	if n := len(status.Added); n > 0 {
		parts = append(parts, fmt.Sprintf("%d added", n))
	}
	if n := len(status.Deleted); n > 0 {
		parts = append(parts, fmt.Sprintf("%d deleted", n))
	}
	if len(parts) == 0 {
		return "clean"
	}
	return strings.Join(parts, ", ")
}

func dirtyFilesFromGitStatus(status daemonclient.GitStatus) []string {
	seen := make(map[string]struct{}, len(status.Staged)+len(status.Modified)+len(status.Added)+len(status.Deleted))
	out := make([]string, 0, len(seen))

	appendUnique := func(files []string) {
		for _, file := range files {
			file = strings.TrimSpace(file)
			if file == "" {
				continue
			}
			if _, ok := seen[file]; ok {
				continue
			}
			seen[file] = struct{}{}
			out = append(out, file)
		}
	}

	appendUnique(status.Staged)
	appendUnique(status.Modified)
	appendUnique(status.Added)
	appendUnique(status.Deleted)

	sort.Strings(out)
	return out
}

func uniqueTrimmedStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

func wrapPendingGitOperation(stage string, err error) error {
	var pending *daemonclient.OperationPendingError
	if !errors.As(err, &pending) {
		return err
	}
	if strings.TrimSpace(pending.OperationID) == "" {
		return fmt.Errorf("%s pending: %w", stage, err)
	}
	return fmt.Errorf("%s queued as operation %s (%s); run `az operation get --id %s --wait` and rerun `az branch merge`", stage, pending.OperationID, pending.State, pending.OperationID)
}

func OperationGetCommand(deps *Dependencies, opts OperationGetOptions) error {
	ctx := context.Background()
	if err := ensureDaemon(ctx, deps, "cli"); err != nil {
		return err
	}

	record, err := deps.DaemonClient.GetOperation(ctx, opts.OperationID)
	if err != nil {
		return fmt.Errorf("failed to get operation: %w", err)
	}
	if opts.Wait && !operationStateTerminal(record.State) {
		record, err = deps.DaemonClient.WaitForOperation(ctx, opts.OperationID, opts.PollInterval)
		if err != nil {
			return fmt.Errorf("failed while waiting for operation %s: %w", opts.OperationID, err)
		}
	}
	return printOperationRecord(record, opts.JSON)
}

func OperationLogsCommand(deps *Dependencies, opts OperationLogsOptions) error {
	ctx := context.Background()
	if err := ensureDaemon(ctx, deps, "cli"); err != nil {
		return err
	}

	record, err := deps.DaemonClient.GetOperation(ctx, opts.OperationID)
	if err != nil {
		return fmt.Errorf("failed to get operation: %w", err)
	}
	return printOperationLogs(record, opts.JSON)
}

func OperationListCommand(deps *Dependencies, opts OperationListOptions) error {
	ctx := context.Background()
	if err := ensureDaemon(ctx, deps, "cli"); err != nil {
		return err
	}

	var issueID string
	if trimmed := strings.TrimSpace(opts.IssueID); trimmed != "" {
		typedIssueID, err := naming.ParseIssueID(trimmed)
		if err != nil {
			return fmt.Errorf("invalid issue id: %w", err)
		}
		issueID = typedIssueID.String()
	}
	records, err := deps.DaemonClient.ListOperations(ctx, daemonclient.OperationListOptions{
		IssueID: issueID,
		Kind:    opts.Kind,
		States:  opts.States,
		Limit:   opts.Limit,
	})
	if err != nil {
		return fmt.Errorf("failed to list operations: %w", err)
	}
	return printOperationList(records, opts.JSON)
}

func OperationCancelCommand(deps *Dependencies, opts OperationCancelOptions) error {
	ctx := context.Background()
	if err := ensureDaemon(ctx, deps, "cli"); err != nil {
		return err
	}

	record, err := deps.DaemonClient.CancelOperation(ctx, opts.OperationID, opts.Reason)
	if err != nil {
		return fmt.Errorf("failed to cancel operation: %w", err)
	}
	if opts.Wait && !operationStateTerminal(record.State) {
		record, err = deps.DaemonClient.WaitForOperation(ctx, record.OperationID.String(), opts.PollInterval)
		if err != nil {
			return fmt.Errorf("failed while waiting for operation %s: %w", record.OperationID, err)
		}
	}
	return printOperationRecord(record, opts.JSON)
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

func ParseConfigSetArgs(args []string) (ConfigSetOptions, error) {
	opts := ConfigSetOptions{}
	fs := flag.NewFlagSet("config set", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&opts.ProjectDir, "project-dir", "", "project directory")
	if err := fs.Parse(args); err != nil {
		return ConfigSetOptions{}, err
	}
	if fs.NArg() != 2 {
		return ConfigSetOptions{}, fmt.Errorf("usage: az config set <key> <value> [--project-dir <dir>]")
	}
	opts.Key = strings.TrimSpace(fs.Arg(0))
	opts.Value = strings.TrimSpace(fs.Arg(1))
	if opts.Key == "" {
		return ConfigSetOptions{}, fmt.Errorf("config key is required")
	}
	return opts, nil
}

func ParseSyncArgs(args []string) (SyncOptions, error) {
	opts := SyncOptions{}
	if len(args) > 0 && strings.TrimSpace(args[0]) == "conflicts" {
		opts.Conflicts = true
		args = args[1:]
	}
	fs := flag.NewFlagSet("sync", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.BoolVar(&opts.All, "all", false, "sync all worktrees")
	fs.StringVar(&opts.ProjectDir, "project-dir", "", "project directory")
	fs.BoolVar(&opts.JSON, "json", false, "print JSON")
	if err := fs.Parse(args); err != nil {
		return SyncOptions{}, err
	}
	switch fs.NArg() {
	case 0:
	case 1:
		if strings.TrimSpace(opts.ProjectDir) != "" {
			return SyncOptions{}, fmt.Errorf("usage: az sync [conflicts] [--all] [<directory>] [--project-dir <dir>] [--json]")
		}
		opts.ProjectDir = strings.TrimSpace(fs.Arg(0))
	default:
		return SyncOptions{}, fmt.Errorf("usage: az sync [conflicts] [--all] [<directory>] [--project-dir <dir>] [--json]")
	}
	return opts, nil
}

func ParseLogArgs(args []string) (LogOptions, error) {
	opts := LogOptions{
		Lines:  200,
		Follow: true,
	}
	fs := flag.NewFlagSet("log", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var sourceList string
	var noFollow bool
	fs.StringVar(&sourceList, "source", "", "comma-separated log sources: daemon,tui,cli")
	fs.IntVar(&opts.Lines, "lines", 200, "number of lines to show before streaming")
	fs.BoolVar(&opts.Follow, "follow", true, "keep streaming logs")
	fs.BoolVar(&noFollow, "no-follow", false, "print and exit without following")
	if err := fs.Parse(args); err != nil {
		return LogOptions{}, err
	}
	if noFollow {
		opts.Follow = false
	}
	if opts.Lines < 1 {
		return LogOptions{}, fmt.Errorf("--lines must be greater than 0")
	}

	requestedSources := make([]string, 0, len(fs.Args())+3)
	requestedSources = append(requestedSources, splitLogSourceList(sourceList)...)
	for _, arg := range fs.Args() {
		trimmed := strings.TrimSpace(arg)
		if strings.HasPrefix(trimmed, "-") {
			return LogOptions{}, fmt.Errorf("flags must come before positional sources (got %q after sources)", trimmed)
		}
		requestedSources = append(requestedSources, splitLogSourceList(trimmed)...)
	}

	if len(requestedSources) == 0 {
		opts.Sources = []string{"daemon", "tui", "cli"}
		return opts, nil
	}

	seen := make(map[string]struct{}, len(requestedSources))
	opts.Sources = make([]string, 0, len(requestedSources))
	for _, source := range requestedSources {
		normalized := strings.ToLower(strings.TrimSpace(source))
		switch normalized {
		case "daemon", "tui", "cli":
		default:
			return LogOptions{}, fmt.Errorf("unknown log source %q (expected daemon, tui, or cli)", source)
		}
		if _, ok := seen[normalized]; ok {
			continue
		}
		seen[normalized] = struct{}{}
		opts.Sources = append(opts.Sources, normalized)
	}
	return opts, nil
}

func splitLogSourceList(value string) []string {
	raw := strings.Split(strings.TrimSpace(value), ",")
	out := make([]string, 0, len(raw))
	for _, source := range raw {
		trimmed := strings.TrimSpace(source)
		if trimmed == "" {
			continue
		}
		out = append(out, trimmed)
	}
	return out
}

func parseWithInterspersedFlags(fs *flag.FlagSet, args []string) error {
	normalized, err := normalizeInterspersedFlags(fs, args)
	if err != nil {
		return err
	}
	return fs.Parse(normalized)
}

func normalizeInterspersedFlags(fs *flag.FlagSet, args []string) ([]string, error) {
	if len(args) == 0 {
		return args, nil
	}

	flagTokens := make([]string, 0, len(args))
	positionals := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		token := args[i]
		if token == "--" {
			positionals = append(positionals, args[i+1:]...)
			break
		}
		if !isFlagToken(token) {
			positionals = append(positionals, token)
			continue
		}
		if isSingleDashLongFlagToken(token) {
			flagName := strings.TrimPrefix(token, "-")
			if idx := strings.Index(flagName, "="); idx >= 0 {
				flagName = flagName[:idx]
			}
			return nil, fmt.Errorf("invalid flag %q: use --%s (single dash is reserved for one-letter aliases)", token, flagName)
		}

		flagTokens = append(flagTokens, token)
		if consumesFlagValue(fs, token) && i+1 < len(args) {
			i++
			flagTokens = append(flagTokens, args[i])
		}
	}

	if len(flagTokens) == 0 || len(positionals) == 0 {
		return args, nil
	}

	normalized := make([]string, 0, len(flagTokens)+len(positionals)+1)
	normalized = append(normalized, flagTokens...)
	normalized = append(normalized, "--")
	normalized = append(normalized, positionals...)
	return normalized, nil
}

func isFlagToken(token string) bool {
	return strings.HasPrefix(token, "-") && token != "-"
}

func isSingleDashLongFlagToken(token string) bool {
	return strings.HasPrefix(token, "-") && !strings.HasPrefix(token, "--") && len(strings.TrimPrefix(token, "-")) > 1
}

func consumesFlagValue(fs *flag.FlagSet, token string) bool {
	name, hasInlineValue := parseFlagName(token)
	if name == "" || hasInlineValue {
		return false
	}
	defined := fs.Lookup(name)
	if defined == nil {
		return false
	}
	if bf, ok := defined.Value.(interface{ IsBoolFlag() bool }); ok && bf.IsBoolFlag() {
		return false
	}
	return true
}

func parseFlagName(token string) (string, bool) {
	switch {
	case strings.HasPrefix(token, "--"):
		trimmed := strings.TrimPrefix(token, "--")
		if trimmed == "" {
			return "", false
		}
		if idx := strings.Index(trimmed, "="); idx >= 0 {
			return trimmed[:idx], true
		}
		return trimmed, false
	case strings.HasPrefix(token, "-"):
		trimmed := strings.TrimPrefix(token, "-")
		if trimmed == "" {
			return "", false
		}
		if idx := strings.Index(trimmed, "="); idx >= 0 {
			return trimmed[:idx], true
		}
		if len(trimmed) != 1 {
			return "", false
		}
		return trimmed, false
	default:
		return "", false
	}
}

func ParseImplDeleteArgs(args []string) (ImplDeleteOptions, error) {
	opts := ImplDeleteOptions{}
	fs := flag.NewFlagSet("impl delete", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.BoolVar(&opts.Confirm, "confirm", false, "confirm deletion of implementation assignments")
	if err := fs.Parse(args); err != nil {
		return ImplDeleteOptions{}, err
	}
	if fs.NArg() != 1 {
		return ImplDeleteOptions{}, fmt.Errorf("usage: az impl delete --confirm <implementation>")
	}
	opts.Implementation = strings.TrimSpace(fs.Arg(0))
	if opts.Implementation == "" {
		return ImplDeleteOptions{}, fmt.Errorf("usage: az impl delete --confirm <implementation>")
	}
	if !opts.Confirm {
		return ImplDeleteOptions{}, fmt.Errorf("missing required flag: --confirm")
	}
	return opts, nil
}

func ParseImplListArgs(args []string) (ImplListOptions, error) {
	fs := flag.NewFlagSet("impl list", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	if err := fs.Parse(args); err != nil {
		return ImplListOptions{}, err
	}
	if fs.NArg() != 0 {
		return ImplListOptions{}, fmt.Errorf("usage: az impl list")
	}
	return ImplListOptions{}, nil
}

func ParseImplMigrateArgs(args []string) (ImplMigrateOptions, error) {
	opts := ImplMigrateOptions{}
	fs := flag.NewFlagSet("impl migrate", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	if err := fs.Parse(args); err != nil {
		return ImplMigrateOptions{}, err
	}
	if fs.NArg() != 2 {
		return ImplMigrateOptions{}, fmt.Errorf("usage: az impl migrate <from-implementation> <to-implementation>")
	}
	opts.FromImplementation = strings.TrimSpace(fs.Arg(0))
	opts.ToImplementation = strings.TrimSpace(fs.Arg(1))
	if opts.FromImplementation == "" || opts.ToImplementation == "" {
		return ImplMigrateOptions{}, fmt.Errorf("usage: az impl migrate <from-implementation> <to-implementation>")
	}
	if opts.FromImplementation == opts.ToImplementation {
		return ImplMigrateOptions{}, fmt.Errorf("source and destination implementations must differ")
	}
	return opts, nil
}

func ParseOperationGetArgs(args []string) (OperationGetOptions, error) {
	opts := OperationGetOptions{}
	fs := flag.NewFlagSet("operation get", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.BoolVar(&opts.JSON, "json", false, "output operation as JSON")
	fs.BoolVar(&opts.Wait, "wait", false, "wait for the operation to reach a terminal state")
	fs.DurationVar(&opts.PollInterval, "poll-interval", 250*time.Millisecond, "poll interval while waiting")
	fs.StringVar(&opts.OperationID, "id", "", "operation id")
	if err := fs.Parse(args); err != nil {
		return OperationGetOptions{}, err
	}
	if opts.OperationID == "" {
		switch fs.NArg() {
		case 0:
			return OperationGetOptions{}, fmt.Errorf("operation id is required")
		case 1:
			opts.OperationID = fs.Arg(0)
		default:
			return OperationGetOptions{}, fmt.Errorf("unexpected argument: %s", fs.Arg(1))
		}
	} else if fs.NArg() != 0 {
		return OperationGetOptions{}, fmt.Errorf("unexpected argument: %s", fs.Arg(0))
	}
	return opts, nil
}

func ParseOperationLogsArgs(args []string) (OperationLogsOptions, error) {
	opts := OperationLogsOptions{}
	fs := flag.NewFlagSet("operation logs", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.BoolVar(&opts.JSON, "json", false, "output operation logs as JSON")
	fs.StringVar(&opts.OperationID, "id", "", "operation id")
	if err := fs.Parse(args); err != nil {
		return OperationLogsOptions{}, err
	}
	if opts.OperationID == "" {
		switch fs.NArg() {
		case 0:
			return OperationLogsOptions{}, fmt.Errorf("operation id is required")
		case 1:
			opts.OperationID = fs.Arg(0)
		default:
			return OperationLogsOptions{}, fmt.Errorf("unexpected argument: %s", fs.Arg(1))
		}
	} else if fs.NArg() != 0 {
		return OperationLogsOptions{}, fmt.Errorf("unexpected argument: %s", fs.Arg(0))
	}
	return opts, nil
}

func ParseOperationListArgs(args []string) (OperationListOptions, error) {
	opts := OperationListOptions{Limit: defaultOperationListLimit}
	stateInputs := make([]string, 0, 4)
	statesCSV := ""
	fs := flag.NewFlagSet("operation list", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.BoolVar(&opts.JSON, "json", false, "output operations as JSON")
	fs.StringVar(&opts.IssueID, "issue", "", "filter by issue id")
	fs.StringVar(&opts.Kind, "kind", "", "filter by operation kind")
	fs.IntVar(&opts.Limit, "limit", defaultOperationListLimit, "maximum operations to return")
	fs.Func("state", "restrict to a specific operation state (repeatable)", func(v string) error {
		stateInputs = append(stateInputs, v)
		return nil
	})
	fs.StringVar(&statesCSV, "states", "", "comma-separated operation states")
	if err := fs.Parse(args); err != nil {
		return OperationListOptions{}, err
	}
	if fs.NArg() != 0 {
		return OperationListOptions{}, fmt.Errorf("unexpected argument: %s", fs.Arg(0))
	}
	if opts.Limit < 1 {
		return OperationListOptions{}, fmt.Errorf("limit must be >= 1")
	}
	if strings.TrimSpace(statesCSV) != "" {
		stateInputs = append(stateInputs, strings.Split(statesCSV, ",")...)
	}
	states, err := parseOperationStates(stateInputs)
	if err != nil {
		return OperationListOptions{}, err
	}
	opts.States = states
	return opts, nil
}

func ParseOperationCancelArgs(args []string) (OperationCancelOptions, error) {
	opts := OperationCancelOptions{}
	fs := flag.NewFlagSet("operation cancel", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.BoolVar(&opts.JSON, "json", false, "output operation as JSON")
	fs.BoolVar(&opts.Wait, "wait", false, "wait for the operation to reach a terminal state")
	fs.DurationVar(&opts.PollInterval, "poll-interval", 250*time.Millisecond, "poll interval while waiting")
	fs.StringVar(&opts.OperationID, "id", "", "operation id")
	fs.StringVar(&opts.Reason, "reason", "", "cancellation reason")
	if err := fs.Parse(args); err != nil {
		return OperationCancelOptions{}, err
	}
	if opts.OperationID == "" {
		switch fs.NArg() {
		case 0:
			return OperationCancelOptions{}, fmt.Errorf("operation id is required")
		case 1:
			opts.OperationID = fs.Arg(0)
		default:
			return OperationCancelOptions{}, fmt.Errorf("unexpected argument: %s", fs.Arg(1))
		}
	} else if fs.NArg() != 0 {
		return OperationCancelOptions{}, fmt.Errorf("unexpected argument: %s", fs.Arg(0))
	}
	return opts, nil
}

func addIssueProjectFlag(fs *flag.FlagSet, project *string) {
	fs.StringVar(project, "project", "", "explicit project id override")
}

func normalizeIssueProject(project string) string {
	return strings.TrimSpace(project)
}

func applyIssueProjectOverride(deps *Dependencies, project string) func() {
	project = normalizeIssueProject(project)
	if project == "" {
		return func() {}
	}
	previousProject := deps.ProjectID
	deps.ProjectID = project
	deps.DaemonClient.WithProjectID(project)
	return func() {
		deps.ProjectID = previousProject
		deps.DaemonClient.WithProjectID(previousProject)
	}
}

func applyExplicitSessionProjectOverride(deps *Dependencies, project string) func() {
	project = normalizeIssueProject(project)
	if deps == nil || project == "" {
		return func() {}
	}
	candidate, ok := findSessionProjectCandidate(deps, project)
	if !ok {
		return func() {}
	}
	previousProject := deps.ProjectID
	previousRepoDir := deps.RepoDir
	previousRuntimeRepoDir := deps.RuntimeRepoDir

	deps.ProjectID = candidate.Route
	if deps.DaemonClient != nil {
		deps.DaemonClient.WithProjectID(candidate.Route)
	}
	if repoDir := sessionProjectCandidateRepoDir(candidate); repoDir != "" {
		deps.RepoDir = repoDir
		deps.RuntimeRepoDir = resolveRuntimeRepoDir(repoDir)
	}

	return func() {
		deps.ProjectID = previousProject
		deps.RepoDir = previousRepoDir
		deps.RuntimeRepoDir = previousRuntimeRepoDir
		if deps.DaemonClient != nil {
			deps.DaemonClient.WithProjectID(previousProject)
		}
	}
}

func sessionProjectCandidateRepoDir(candidate sessionProjectCandidate) string {
	path := strings.TrimSpace(candidate.Path)
	if path == "" {
		return ""
	}
	if repoDir, err := config.ResolveProjectRoot(path); err == nil && strings.TrimSpace(repoDir) != "" {
		return repoDir
	}
	return path
}

func isDifferentExplicitIssueProject(currentProject, explicitProject string) bool {
	explicitProject = normalizeIssueProject(explicitProject)
	if explicitProject == "" {
		return false
	}
	return protocol.NormalizeProjectID(currentProject) != protocol.NormalizeProjectID(explicitProject)
}

func formatProjectIssueRef(projectID, issueID string) string {
	projectID = strings.TrimSpace(projectID)
	issueID = strings.TrimSpace(issueID)
	if projectID == "" {
		return issueID
	}
	return projectID + ":" + issueID
}

func ParseIssueListArgs(args []string) (IssueListOptions, error) {
	return parseIssueListArgs(args, false)
}

func ParseIssueSearchArgs(args []string) (IssueListOptions, error) {
	return parseIssueListArgs(args, true)
}

func parseIssueListArgs(args []string, allowQueryArgs bool) (IssueListOptions, error) {
	opts := IssueListOptions{Limit: defaultIssueListLimit}
	ids := make([]string, 0, 4)
	idsCSV := ""
	parentIDs := make([]string, 0, 2)
	parentsCSV := ""
	depIDs := make([]string, 0, 2)
	depIDsCSV := ""
	stateInputs := make([]string, 0, 4)
	statesCSV := ""
	createdAfterRaw := ""
	createdBeforeRaw := ""
	updatedAfterRaw := ""
	updatedBeforeRaw := ""
	fs := flag.NewFlagSet("issue list", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	addIssueProjectFlag(fs, &opts.Project)
	fs.BoolVar(&opts.JSON, "json", false, "output issues as JSON")
	fs.BoolVar(&opts.Deps, "deps", false, "include dependency summary in table output")
	fs.IntVar(&opts.Limit, "limit", defaultIssueListLimit, "maximum issues to list in one window")
	fs.StringVar(&opts.Query, "query", "", "case-insensitive content query")
	fs.StringVar(&opts.Query, "q", "", "case-insensitive content query")
	fs.StringVar(&createdAfterRaw, "created-after", "", "include issues created at or after date/time")
	fs.StringVar(&createdBeforeRaw, "created-before", "", "include issues created at or before date/time")
	fs.StringVar(&updatedAfterRaw, "updated-after", "", "include issues updated at or after date/time")
	fs.StringVar(&updatedBeforeRaw, "updated-before", "", "include issues updated at or before date/time")
	addStatusInput := func(v string) error {
		stateInputs = append(stateInputs, v)
		return nil
	}
	fs.Func("status", "restrict to a specific issue status (repeatable)", addStatusInput)
	fs.Func("state", "deprecated alias for --status", addStatusInput)
	fs.StringVar(&statesCSV, "statuses", "", "comma-separated issue statuses")
	fs.StringVar(&statesCSV, "states", "", "deprecated alias for --statuses")
	fs.Func("id", "restrict list to specific issue ids (repeatable)", func(v string) error {
		trimmed := strings.TrimSpace(v)
		if trimmed == "" {
			return fmt.Errorf("empty issue id")
		}
		ids = append(ids, trimmed)
		return nil
	})
	fs.StringVar(&idsCSV, "ids", "", "comma-separated issue ids to include")
	fs.Func("parent", "restrict list to issues with one of the provided parent issue ids (repeatable)", func(v string) error {
		trimmed := strings.TrimSpace(v)
		if trimmed == "" {
			return fmt.Errorf("empty parent issue id")
		}
		parentIDs = append(parentIDs, trimmed)
		return nil
	})
	fs.StringVar(&parentsCSV, "parents", "", "comma-separated parent issue ids")
	fs.Func("depends-on", "restrict list to issues depending on one of the provided issue ids (repeatable)", func(v string) error {
		trimmed := strings.TrimSpace(v)
		if trimmed == "" {
			return fmt.Errorf("empty dependency issue id")
		}
		depIDs = append(depIDs, trimmed)
		return nil
	})
	fs.StringVar(&depIDsCSV, "depends-on-ids", "", "comma-separated dependency issue ids")
	if err := fs.Parse(args); err != nil {
		return IssueListOptions{}, err
	}
	if allowQueryArgs {
		if fs.NArg() > 0 {
			if strings.TrimSpace(opts.Query) != "" {
				return IssueListOptions{}, fmt.Errorf("provide query either as --query or as positional text, not both")
			}
			for _, arg := range fs.Args()[1:] {
				if strings.HasPrefix(arg, "-") {
					return IssueListOptions{}, fmt.Errorf("flags/options must appear before positional query text")
				}
			}
			opts.Query = strings.Join(fs.Args(), " ")
		}
	} else if fs.NArg() != 0 {
		return IssueListOptions{}, fmt.Errorf("unexpected argument: %s", fs.Arg(0))
	}
	if opts.Limit < 1 {
		return IssueListOptions{}, fmt.Errorf("limit must be >= 1")
	}
	opts.Project = normalizeIssueProject(opts.Project)
	opts.Query = strings.TrimSpace(opts.Query)
	if allowQueryArgs && opts.Query == "" {
		return IssueListOptions{}, fmt.Errorf("usage: az issue search [--project <project-id>] [--json] [--deps] [--created-after YYYY-MM-DD] [--created-before YYYY-MM-DD] [--updated-after YYYY-MM-DD] [--updated-before YYYY-MM-DD] [--status <status> ...] [--statuses a,b,c] [--limit N] [--id <id> ...] [--ids a,b,c] [--parent <id> ...] [--parents a,b,c] [--depends-on <id> ...] [--depends-on-ids a,b,c] (--query <text>|-q <text>|<query>)")
	}
	var parseErr error
	if opts.CreatedAfter, parseErr = parseIssueListDateFilter(createdAfterRaw, false); parseErr != nil {
		return IssueListOptions{}, fmt.Errorf("invalid --created-after: %w", parseErr)
	}
	if opts.CreatedBefore, parseErr = parseIssueListDateFilter(createdBeforeRaw, true); parseErr != nil {
		return IssueListOptions{}, fmt.Errorf("invalid --created-before: %w", parseErr)
	}
	if opts.UpdatedAfter, parseErr = parseIssueListDateFilter(updatedAfterRaw, false); parseErr != nil {
		return IssueListOptions{}, fmt.Errorf("invalid --updated-after: %w", parseErr)
	}
	if opts.UpdatedBefore, parseErr = parseIssueListDateFilter(updatedBeforeRaw, true); parseErr != nil {
		return IssueListOptions{}, fmt.Errorf("invalid --updated-before: %w", parseErr)
	}
	if strings.TrimSpace(idsCSV) != "" {
		for _, raw := range strings.Split(idsCSV, ",") {
			trimmed := strings.TrimSpace(raw)
			if trimmed == "" {
				continue
			}
			ids = append(ids, trimmed)
		}
	}
	opts.IDs = dedupeOrderedIDs(ids)
	if strings.TrimSpace(parentsCSV) != "" {
		for _, raw := range strings.Split(parentsCSV, ",") {
			trimmed := strings.TrimSpace(raw)
			if trimmed == "" {
				continue
			}
			parentIDs = append(parentIDs, trimmed)
		}
	}
	opts.ParentIDs = dedupeOrderedIDs(parentIDs)
	if strings.TrimSpace(depIDsCSV) != "" {
		for _, raw := range strings.Split(depIDsCSV, ",") {
			trimmed := strings.TrimSpace(raw)
			if trimmed == "" {
				continue
			}
			depIDs = append(depIDs, trimmed)
		}
	}
	opts.DependsOnIDs = dedupeOrderedIDs(depIDs)
	if strings.TrimSpace(statesCSV) != "" {
		stateInputs = append(stateInputs, strings.Split(statesCSV, ",")...)
	}
	states, err := parseIssueStatuses(stateInputs)
	if err != nil {
		return IssueListOptions{}, err
	}
	opts.States = states
	return opts, nil
}

func ParseIssueGetArgs(args []string) (IssueGetOptions, error) {
	opts := IssueGetOptions{}
	issueIDFlag := ""
	fs := flag.NewFlagSet("issue get", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	addIssueProjectFlag(fs, &opts.Project)
	fs.BoolVar(&opts.JSON, "json", false, "output issue as JSON")
	fs.BoolVar(&opts.IncludeNotes, "with-notes", false, "include full notes in text output")
	fs.StringVar(&issueIDFlag, "id", "", "issue id (named alternative to positional)")
	if err := parseWithInterspersedFlags(fs, args); err != nil {
		return IssueGetOptions{}, err
	}
	if fs.NArg() > 1 {
		return IssueGetOptions{}, fmt.Errorf("usage: az issue get [--project <project-id>] [--id <issue-id>] [--json] [--with-notes] [<issue-id>]")
	}
	if fs.NArg() == 1 {
		opts.IssueID = fs.Arg(0)
	}
	if strings.TrimSpace(issueIDFlag) != "" {
		opts.IssueID = strings.TrimSpace(issueIDFlag)
	}
	if strings.TrimSpace(opts.IssueID) == "" {
		return IssueGetOptions{}, fmt.Errorf("usage: az issue get [--project <project-id>] [--id <issue-id>] [--json] [--with-notes] [<issue-id>]")
	}
	opts.Project = normalizeIssueProject(opts.Project)
	return opts, nil
}

func ParseIssueEventsArgs(args []string) (IssueEventsOptions, error) {
	opts := IssueEventsOptions{}
	issueIDFlag := ""
	typesCSV := ""
	var typeFlags repeatedStringFlag
	fs := flag.NewFlagSet("issue events", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	addIssueProjectFlag(fs, &opts.Project)
	fs.BoolVar(&opts.JSON, "json", false, "output issue events as JSON")
	fs.StringVar(&issueIDFlag, "id", "", "issue id (named alternative to positional)")
	fs.Var(&typeFlags, "type", "filter by event type; may be repeated")
	fs.StringVar(&typesCSV, "types", "", "comma-separated event types")
	fs.IntVar(&opts.Limit, "limit", 0, "maximum events to return")
	if err := parseWithInterspersedFlags(fs, args); err != nil {
		return IssueEventsOptions{}, err
	}
	if fs.NArg() > 1 {
		return IssueEventsOptions{}, fmt.Errorf("usage: az issue events [--project <project-id>] [--id <issue-id>] [--json] [--type <event-type> ...] [--types a,b] [--limit N] [<issue-id>]")
	}
	if fs.NArg() == 1 {
		opts.IssueID = fs.Arg(0)
	}
	if strings.TrimSpace(issueIDFlag) != "" {
		opts.IssueID = strings.TrimSpace(issueIDFlag)
	}
	if strings.TrimSpace(opts.IssueID) == "" {
		return IssueEventsOptions{}, fmt.Errorf("usage: az issue events [--project <project-id>] [--id <issue-id>] [--json] [--type <event-type> ...] [--types a,b] [--limit N] [<issue-id>]")
	}
	if opts.Limit < 0 {
		return IssueEventsOptions{}, fmt.Errorf("--limit must be non-negative")
	}
	types := append([]string(nil), typeFlags...)
	if strings.TrimSpace(typesCSV) != "" {
		types = append(types, strings.Split(typesCSV, ",")...)
	}
	opts.EventTypes = dedupeOrderedIDs(types)
	opts.Project = normalizeIssueProject(opts.Project)
	return opts, nil
}

func ParseIssueGetManyArgs(args []string) (IssueGetManyOptions, error) {
	opts := IssueGetManyOptions{}
	ids := make([]string, 0, 4)
	idsCSV := ""
	fs := flag.NewFlagSet("issue get-many", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	addIssueProjectFlag(fs, &opts.Project)
	fs.BoolVar(&opts.JSON, "json", false, "output issue lookup results as JSON")
	fs.BoolVar(&opts.IncludeNotes, "with-notes", false, "include full notes in output")
	fs.Func("id", "issue id to fetch (repeatable)", func(v string) error {
		trimmed := strings.TrimSpace(v)
		if trimmed == "" {
			return fmt.Errorf("empty issue id")
		}
		ids = append(ids, trimmed)
		return nil
	})
	fs.StringVar(&idsCSV, "ids", "", "comma-separated issue ids to fetch")
	if err := fs.Parse(args); err != nil {
		return IssueGetManyOptions{}, err
	}
	if fs.NArg() != 0 {
		return IssueGetManyOptions{}, fmt.Errorf("unexpected argument: %s", fs.Arg(0))
	}
	if strings.TrimSpace(idsCSV) != "" {
		for _, raw := range strings.Split(idsCSV, ",") {
			trimmed := strings.TrimSpace(raw)
			if trimmed == "" {
				continue
			}
			ids = append(ids, trimmed)
		}
	}
	ids = dedupeOrderedIDs(ids)
	if len(ids) == 0 {
		return IssueGetManyOptions{}, fmt.Errorf("usage: az issue get-many [--project <project-id>] --id <issue-id> [--id <issue-id> ...] [--ids a,b,c] [--json] [--with-notes]")
	}
	opts.IssueIDs = ids
	opts.Project = normalizeIssueProject(opts.Project)
	return opts, nil
}

func ParseIssueCreateArgs(args []string) (IssueCreateOptions, error) {
	opts := IssueCreateOptions{Type: domain.TypeTask}
	var priorityRaw string
	var typeRaw string
	var titleFlag string
	impls := make([]string, 0, 2)
	fs := flag.NewFlagSet("issue create", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	addIssueProjectFlag(fs, &opts.Project)
	fs.Func("impl", "target implementation key (repeatable)", func(v string) error {
		trimmed := strings.TrimSpace(v)
		if trimmed == "" {
			return fmt.Errorf("empty impl value")
		}
		impls = append(impls, trimmed)
		return nil
	})
	fs.StringVar(&titleFlag, "title", "", "issue title")
	fs.StringVar(&opts.Description, "description", "", "issue description")
	fs.StringVar(&priorityRaw, "priority", "", "issue priority (P0-P4)")
	fs.BoolVar(&opts.Deferred, "deferred", false, "create standalone later/backlog work; skips AZEDARACH_ISSUE_ID auto-parenting and defaults priority to P4 unless --priority is provided")
	fs.BoolVar(&opts.JSON, "json", false, "output issue create result as JSON")
	fs.StringVar(&typeRaw, "type", string(domain.TypeTask), "issue type (task|bug|feature|epic|chore)")
	if err := parseWithInterspersedFlags(fs, args); err != nil {
		return IssueCreateOptions{}, err
	}
	titleFlag = strings.TrimSpace(titleFlag)
	switch {
	case fs.NArg() == 0 && titleFlag != "":
		opts.Title = titleFlag
	case fs.NArg() == 1 && titleFlag == "":
		opts.Title = fs.Arg(0)
	case fs.NArg() == 1 && titleFlag != "":
		return IssueCreateOptions{}, fmt.Errorf("provide title either as --title or as a positional argument, not both")
	default:
		return IssueCreateOptions{}, fmt.Errorf("usage: az issue create [--project <project-id>] [--impl <implementation> ...] [--deferred] [--type task|bug|feature|epic|chore] [--priority P0|P1|P2|P3|P4] [--title text] [--description text] [--json] [<title>]")
	}

	taskType, err := parseTaskType(typeRaw)
	if err != nil {
		return IssueCreateOptions{}, err
	}
	if strings.TrimSpace(priorityRaw) != "" {
		priority, err := parsePriority(priorityRaw)
		if err != nil {
			return IssueCreateOptions{}, err
		}
		opts.Priority = priority
		opts.PriorityExplicit = true
	} else if opts.Deferred {
		opts.Priority = domain.P4
	} else {
		opts.Priority = domain.P2
	}
	opts.Type = taskType
	opts.Implementations = dedupeOrderedIDs(impls)
	if issueID := strings.TrimSpace(os.Getenv("AZEDARACH_ISSUE_ID")); issueID != "" {
		opts.AutoCreatedFromIssueID = &issueID
		if !opts.Deferred {
			opts.AutoParentFromIssueID = &issueID
		}
	}
	opts.Project = normalizeIssueProject(opts.Project)
	return opts, nil
}

func ParseIssueCheckArgs(args []string) (IssueCheckOptions, error) {
	getOpts, err := ParseIssueGetArgs(args)
	if err != nil {
		return IssueCheckOptions{}, fmt.Errorf("usage: az issue check [--project <project-id>] [--id <issue-id>] [--json] [<issue-id>]")
	}
	return IssueCheckOptions{
		Project: getOpts.Project,
		IssueID: getOpts.IssueID,
		JSON:    getOpts.JSON,
	}, nil
}

func ParseIssueDoctorArgs(args []string) (IssueDoctorOptions, error) {
	opts := IssueDoctorOptions{}
	issueIDFlag := ""
	fs := flag.NewFlagSet("issue doctor", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	addIssueProjectFlag(fs, &opts.Project)
	fs.StringVar(&issueIDFlag, "id", "", "issue id (named alternative to positional)")
	fs.BoolVar(&opts.JSON, "json", false, "output issue doctor result as JSON")
	if err := parseWithInterspersedFlags(fs, args); err != nil {
		return IssueDoctorOptions{}, err
	}
	if fs.NArg() > 1 {
		return IssueDoctorOptions{}, fmt.Errorf("usage: az issue doctor [--project <project-id>] [--id <issue-id>] [--json] [<issue-id>]")
	}
	issueID := ""
	if fs.NArg() == 1 {
		issueID = fs.Arg(0)
	}
	if strings.TrimSpace(issueIDFlag) != "" {
		issueID = strings.TrimSpace(issueIDFlag)
	}
	if strings.TrimSpace(issueID) == "" {
		return IssueDoctorOptions{}, fmt.Errorf("usage: az issue doctor [--project <project-id>] [--id <issue-id>] [--json] [<issue-id>]")
	}
	opts.Project = normalizeIssueProject(opts.Project)
	opts.IssueID = issueID
	return opts, nil
}

func ParseIssueCloseArgs(args []string) (IssueCloseOptions, error) {
	opts := IssueCloseOptions{}
	issueIDFlag := ""
	implFlag := ""
	fs := flag.NewFlagSet("issue close", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	addIssueProjectFlag(fs, &opts.Project)
	fs.StringVar(&implFlag, "impl", "", "forbidden for existing-issue commands")
	fs.StringVar(&issueIDFlag, "id", "", "issue id (named alternative to positional)")
	fs.StringVar(&issueIDFlag, "i", "", "issue id (short alternative to --id)")
	fs.BoolVar(&opts.JSON, "json", false, "output issue close result as JSON")
	fs.BoolVar(&opts.ForceWorktree, "force-worktree", false, "force worktree removal after integration")
	fs.BoolVar(&opts.CloseCleanChildren, "close-clean-children", false, "also close clean unresolved child issues after confirmation")
	if err := parseWithInterspersedFlags(fs, args); err != nil {
		return IssueCloseOptions{}, err
	}
	if fs.NArg() > 1 {
		return IssueCloseOptions{}, fmt.Errorf("usage: az issue close [--project <project-id>] [--id <issue-id>|-i <issue-id>] [--json] [--force-worktree] [--close-clean-children] [<issue-id>]")
	}
	if strings.TrimSpace(implFlag) != "" {
		return IssueCloseOptions{}, fmt.Errorf("--impl is not supported for issue close; issue implementations are already assigned")
	}
	if fs.NArg() == 1 {
		opts.IssueID = fs.Arg(0)
	}
	if strings.TrimSpace(issueIDFlag) != "" {
		opts.IssueID = strings.TrimSpace(issueIDFlag)
	}
	if strings.TrimSpace(opts.IssueID) == "" {
		return IssueCloseOptions{}, fmt.Errorf("usage: az issue close [--project <project-id>] [--id <issue-id>|-i <issue-id>] [--json] [--force-worktree] [--close-clean-children] [<issue-id>]")
	}
	opts.Project = normalizeIssueProject(opts.Project)
	return opts, nil
}

func ParseIssueDeleteArgs(args []string) (IssueDeleteOptions, error) {
	opts := IssueDeleteOptions{}
	issueIDFlag := ""
	implFlag := ""
	fs := flag.NewFlagSet("issue delete", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	addIssueProjectFlag(fs, &opts.Project)
	fs.StringVar(&implFlag, "impl", "", "forbidden for existing-issue commands")
	fs.StringVar(&issueIDFlag, "id", "", "issue id (named alternative to positional)")
	fs.BoolVar(&opts.Confirm, "confirm", false, "confirm permanent issue deletion")
	fs.BoolVar(&opts.JSON, "json", false, "output issue delete result as JSON")
	fs.BoolVar(&opts.Cleanup, "cleanup", false, "stop active session and remove worktree before deleting")
	fs.BoolVar(&opts.StopSession, "stop-session", false, "stop active session before deleting")
	fs.BoolVar(&opts.RemoveWorktree, "remove-worktree", false, "remove issue worktree before deleting")
	fs.BoolVar(&opts.ForceWorktree, "force-worktree", false, "force worktree removal when removing worktree")
	if err := parseWithInterspersedFlags(fs, args); err != nil {
		return IssueDeleteOptions{}, err
	}
	if fs.NArg() > 1 {
		return IssueDeleteOptions{}, fmt.Errorf("usage: az issue delete [--project <project-id>] --confirm [--id <issue-id>] [--json] [--cleanup|--stop-session] [--remove-worktree] [--force-worktree] [<issue-id>]")
	}
	if strings.TrimSpace(implFlag) != "" {
		return IssueDeleteOptions{}, fmt.Errorf("--impl is not supported for issue delete; issue implementations are already assigned")
	}
	if !opts.Confirm {
		return IssueDeleteOptions{}, fmt.Errorf("missing required flag: --confirm")
	}
	if opts.Cleanup {
		opts.StopSession = true
		opts.RemoveWorktree = true
	}
	if opts.ForceWorktree && !opts.RemoveWorktree {
		return IssueDeleteOptions{}, fmt.Errorf("--force-worktree requires --remove-worktree or --cleanup")
	}
	if fs.NArg() == 1 {
		opts.IssueID = fs.Arg(0)
	}
	if strings.TrimSpace(issueIDFlag) != "" {
		opts.IssueID = strings.TrimSpace(issueIDFlag)
	}
	if strings.TrimSpace(opts.IssueID) == "" {
		return IssueDeleteOptions{}, fmt.Errorf("usage: az issue delete [--project <project-id>] --confirm [--id <issue-id>] [--json] [--cleanup|--stop-session] [--remove-worktree] [--force-worktree] [<issue-id>]")
	}
	opts.Project = normalizeIssueProject(opts.Project)
	return opts, nil
}

func ParseIssueUpdateArgs(args []string) (IssueUpdateOptions, error) {
	opts := IssueUpdateOptions{}
	issueIDFlag := ""
	implFlag := ""
	statusRaw := ""
	var typeRaw string
	var priorityRaw string
	updateImpls := make([]string, 0, 2)
	fs := flag.NewFlagSet("issue update", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	addIssueProjectFlag(fs, &opts.Project)
	fs.StringVar(&implFlag, "impl", "", "forbidden for existing-issue commands")
	fs.StringVar(&opts.Title, "title", "", "updated issue title")
	fs.StringVar(&opts.Description, "description", "", "updated issue description")
	fs.Func("notes", "replace issue notes", func(v string) error {
		opts.Notes = &v
		return nil
	})
	fs.StringVar(&opts.AppendNotes, "append-notes", "", "append a note line to issue notes")
	fs.StringVar(&issueIDFlag, "id", "", "issue id (named alternative to positional)")
	fs.BoolVar(&opts.JSON, "json", false, "output issue update result as JSON")
	fs.BoolVar(&opts.ForceWorktree, "force-worktree", false, "force worktree removal when setting closed")
	fs.BoolVar(&opts.CascadeChildren, "cascade-children", false, "when setting in_review, move open/in_progress descendants to in_review first")
	fs.StringVar(&statusRaw, "status", "", "updated status (open|in_progress|in_review|closed)")
	fs.Func("update-impl", "set implementation assignment (repeatable)", func(v string) error {
		trimmed := strings.TrimSpace(v)
		if trimmed == "" {
			return fmt.Errorf("empty update-impl value")
		}
		updateImpls = append(updateImpls, trimmed)
		return nil
	})
	fs.StringVar(&typeRaw, "type", "", "updated issue type (task|bug|feature|epic|chore)")
	fs.StringVar(&priorityRaw, "priority", "", "updated priority (P0-P4)")
	if err := parseWithInterspersedFlags(fs, args); err != nil {
		return IssueUpdateOptions{}, err
	}
	if fs.NArg() > 1 {
		return IssueUpdateOptions{}, fmt.Errorf("usage: az issue update [--project <project-id>] [--id <issue-id>] [--json] [<issue-id>] [--title text] [--description text] [--notes text] [--append-notes text] [--status open|in_progress|in_review|closed] [--cascade-children] [--force-worktree] [--type task|bug|feature|epic|chore] [--priority P0|P1|P2|P3|P4] [--update-impl <impl> ...]")
	}
	if strings.TrimSpace(implFlag) != "" {
		return IssueUpdateOptions{}, fmt.Errorf("--impl is not supported for issue update (it is create-only); normal field updates do not need --update-impl, and --update-impl is only for changing issue implementations")
	}
	if fs.NArg() == 1 {
		opts.IssueID = fs.Arg(0)
	}
	if strings.TrimSpace(issueIDFlag) != "" {
		opts.IssueID = strings.TrimSpace(issueIDFlag)
	}
	if strings.TrimSpace(opts.IssueID) == "" {
		return IssueUpdateOptions{}, fmt.Errorf("usage: az issue update [--project <project-id>] [--id <issue-id>] [--json] [<issue-id>] [--title text] [--description text] [--notes text] [--append-notes text] [--status open|in_progress|in_review|closed] [--cascade-children] [--force-worktree] [--type task|bug|feature|epic|chore] [--priority P0|P1|P2|P3|P4] [--update-impl <impl> ...]")
	}
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
	if strings.TrimSpace(statusRaw) != "" {
		status, err := parseStatus(statusRaw)
		if err != nil {
			return IssueUpdateOptions{}, err
		}
		opts.Status = &status
	}
	if opts.ForceWorktree && (opts.Status == nil || *opts.Status != domain.StatusDone) {
		return IssueUpdateOptions{}, fmt.Errorf("--force-worktree is only supported with --status closed")
	}
	if opts.CascadeChildren && (opts.Status == nil || *opts.Status != domain.StatusInReview) {
		return IssueUpdateOptions{}, fmt.Errorf("--cascade-children is only supported with --status in_review")
	}
	if strings.TrimSpace(opts.AppendNotes) == "" {
		opts.AppendNotes = ""
	}
	fs.Visit(func(flag *flag.Flag) {
		if flag.Name == "description" {
			opts.DescriptionSet = true
		}
	})
	opts.UpdateImpls = dedupeOrderedIDs(updateImpls)
	if opts.Title == "" && !opts.DescriptionSet && opts.Notes == nil && opts.AppendNotes == "" && opts.Type == nil && opts.Priority == nil && opts.Status == nil && len(opts.UpdateImpls) == 0 {
		return IssueUpdateOptions{}, fmt.Errorf("no update fields provided")
	}
	opts.Project = normalizeIssueProject(opts.Project)
	return opts, nil
}

func ParseIssueDependencyAddArgs(args []string) (IssueDependencyAddOptions, error) {
	opts := IssueDependencyAddOptions{Type: "blocks"}
	issueIDFlag := ""
	dependsOnIDFlag := ""
	implFlag := ""
	fs := flag.NewFlagSet("issue dep add", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	addIssueProjectFlag(fs, &opts.Project)
	fs.StringVar(&implFlag, "impl", "", "forbidden for existing-issue commands")
	fs.StringVar(&issueIDFlag, "issue-id", "", "source issue id (named alternative to positional)")
	fs.StringVar(&dependsOnIDFlag, "depends-on-id", "", "dependency target issue id (named alternative to positional)")
	fs.StringVar(&opts.Type, "type", "blocks", "dependency type (blocks|related|parent-child|discovered-from|created-in)")
	fs.BoolVar(&opts.ForceParentChange, "force-parent-change", false, "replace an existing parent-child edge")
	fs.BoolVar(&opts.JSON, "json", false, "output dependency add result as JSON")
	if err := parseWithInterspersedFlags(fs, args); err != nil {
		return IssueDependencyAddOptions{}, err
	}
	if fs.NArg() > 2 {
		return IssueDependencyAddOptions{}, fmt.Errorf("usage: az issue dep add [--project <project-id>] [--issue-id <issue-id>] [--depends-on-id <depends-on-id>] [<issue-id> <depends-on-id>] [--type blocks|related|parent-child|discovered-from|created-in] [--force-parent-change] [--json]")
	}
	if strings.TrimSpace(implFlag) != "" {
		return IssueDependencyAddOptions{}, fmt.Errorf("--impl is not supported for issue dep add; dependencies target existing issues")
	}
	if fs.NArg() >= 1 {
		opts.IssueID = fs.Arg(0)
	}
	if fs.NArg() >= 2 {
		opts.DependsOnID = fs.Arg(1)
	}
	if strings.TrimSpace(issueIDFlag) != "" {
		opts.IssueID = strings.TrimSpace(issueIDFlag)
	}
	if strings.TrimSpace(dependsOnIDFlag) != "" {
		opts.DependsOnID = strings.TrimSpace(dependsOnIDFlag)
	}
	if strings.TrimSpace(opts.IssueID) == "" || strings.TrimSpace(opts.DependsOnID) == "" {
		return IssueDependencyAddOptions{}, fmt.Errorf("usage: az issue dep add [--project <project-id>] [--issue-id <issue-id>] [--depends-on-id <depends-on-id>] [<issue-id> <depends-on-id>] [--type blocks|related|parent-child|discovered-from|created-in] [--force-parent-change] [--json]")
	}
	opts.Project = normalizeIssueProject(opts.Project)
	issueProject, issueID, err := parseDependencyEndpointProject(opts.IssueID, "issue_id")
	if err != nil {
		return IssueDependencyAddOptions{}, err
	}
	dependsOnProject, dependsOnID, err := parseDependencyEndpointProject(opts.DependsOnID, "depends_on_id")
	if err != nil {
		return IssueDependencyAddOptions{}, err
	}
	opts.IssueID = issueID
	opts.DependsOnID = dependsOnID
	opts.IssueProjectID = issueProject
	opts.DependsOnProjectID = dependsOnProject
	if err := reconcileDependencyEndpointProjects(&opts.Project, opts.IssueProjectID, opts.DependsOnProjectID); err != nil {
		return IssueDependencyAddOptions{}, err
	}
	return opts, nil
}

func parseDependencyEndpointProject(raw, field string) (projectID, issueID string, err error) {
	trimmed := strings.TrimSpace(raw)
	if projectPart, issuePart, ok := splitExplicitSessionIssueTarget(trimmed); ok {
		if _, parseErr := naming.ParseIssueID(issuePart); parseErr != nil {
			return "", "", fmt.Errorf("invalid %s %q: %w", field, raw, parseErr)
		}
		return normalizeIssueProject(projectPart), strings.TrimSpace(issuePart), nil
	}
	return "", trimmed, nil
}

func reconcileDependencyEndpointProjects(requestProject *string, issueProject, dependsOnProject string) error {
	issueProject = normalizeIssueProject(issueProject)
	dependsOnProject = normalizeIssueProject(dependsOnProject)
	if issueProject != "" && dependsOnProject != "" && protocol.NormalizeProjectID(issueProject) != protocol.NormalizeProjectID(dependsOnProject) {
		return fmt.Errorf("dependency endpoints must be in the same project: issue_id project %q, depends_on_id project %q", issueProject, dependsOnProject)
	}
	endpointProject := issueProject
	if endpointProject == "" {
		endpointProject = dependsOnProject
	}
	if endpointProject == "" {
		return nil
	}
	if requestProject == nil {
		return nil
	}
	if strings.TrimSpace(*requestProject) == "" {
		*requestProject = endpointProject
		return nil
	}
	if protocol.NormalizeProjectID(*requestProject) != protocol.NormalizeProjectID(endpointProject) {
		return fmt.Errorf("dependency endpoint project %q does not match --project %q", endpointProject, *requestProject)
	}
	return nil
}

func ParseIssueDependencyRemoveArgs(args []string) (IssueDependencyRemoveOptions, error) {
	opts := IssueDependencyRemoveOptions{Type: "blocks"}
	issueIDFlag := ""
	dependsOnIDFlag := ""
	implFlag := ""
	fs := flag.NewFlagSet("issue dep remove", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	addIssueProjectFlag(fs, &opts.Project)
	fs.StringVar(&implFlag, "impl", "", "forbidden for existing-issue commands")
	fs.StringVar(&issueIDFlag, "issue-id", "", "source issue id (named alternative to positional)")
	fs.StringVar(&dependsOnIDFlag, "depends-on-id", "", "dependency target issue id (named alternative to positional)")
	fs.StringVar(&opts.Type, "type", "blocks", "dependency type (blocks|related|parent-child|discovered-from|created-in)")
	fs.BoolVar(&opts.Confirm, "confirm", false, "confirm removal for guarded dependency types")
	fs.BoolVar(&opts.ConfirmParentOrphan, "confirm-parent-orphan", false, "confirm parent-child removal that can orphan active child work onto the root board")
	fs.BoolVar(&opts.JSON, "json", false, "output dependency remove result as JSON")
	if err := parseWithInterspersedFlags(fs, args); err != nil {
		return IssueDependencyRemoveOptions{}, err
	}
	if fs.NArg() > 2 {
		return IssueDependencyRemoveOptions{}, fmt.Errorf("usage: az issue dep remove [--project <project-id>] [--issue-id <issue-id>] [--depends-on-id <depends-on-id>] [<issue-id> <depends-on-id>] [--type blocks|related|parent-child|discovered-from|created-in] [--confirm] [--confirm-parent-orphan] [--json]")
	}
	if strings.TrimSpace(implFlag) != "" {
		return IssueDependencyRemoveOptions{}, fmt.Errorf("--impl is not supported for issue dep remove; dependencies target existing issues")
	}
	if fs.NArg() >= 1 {
		opts.IssueID = fs.Arg(0)
	}
	if fs.NArg() >= 2 {
		opts.DependsOnID = fs.Arg(1)
	}
	if strings.TrimSpace(issueIDFlag) != "" {
		opts.IssueID = strings.TrimSpace(issueIDFlag)
	}
	if strings.TrimSpace(dependsOnIDFlag) != "" {
		opts.DependsOnID = strings.TrimSpace(dependsOnIDFlag)
	}
	if strings.TrimSpace(opts.IssueID) == "" || strings.TrimSpace(opts.DependsOnID) == "" {
		return IssueDependencyRemoveOptions{}, fmt.Errorf("usage: az issue dep remove [--project <project-id>] [--issue-id <issue-id>] [--depends-on-id <depends-on-id>] [<issue-id> <depends-on-id>] [--type blocks|related|parent-child|discovered-from|created-in] [--confirm] [--confirm-parent-orphan] [--json]")
	}
	opts.Project = normalizeIssueProject(opts.Project)
	return opts, nil
}

func ParseIssueDependencyBulkApplyArgs(args []string) (IssueDependencyBulkApplyOptions, error) {
	opts := IssueDependencyBulkApplyOptions{}
	implFlag := ""
	fs := flag.NewFlagSet("issue dep bulk apply", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	addIssueProjectFlag(fs, &opts.Project)
	fs.StringVar(&implFlag, "impl", "", "forbidden for existing-issue commands")
	fs.StringVar(&opts.InputPath, "input", "", "path to JSON payload")
	fs.BoolVar(&opts.DryRun, "dry-run", false, "validate and preview without mutating")
	fs.BoolVar(&opts.JSON, "json", false, "output dependency mutation results as JSON")
	if err := fs.Parse(args); err != nil {
		return IssueDependencyBulkApplyOptions{}, err
	}
	if fs.NArg() != 0 {
		return IssueDependencyBulkApplyOptions{}, fmt.Errorf("unexpected argument: %s", fs.Arg(0))
	}
	if strings.TrimSpace(implFlag) != "" {
		return IssueDependencyBulkApplyOptions{}, fmt.Errorf("--impl is not supported for issue dep bulk apply; dependencies target existing issues")
	}
	if strings.TrimSpace(opts.InputPath) == "" {
		return IssueDependencyBulkApplyOptions{}, fmt.Errorf("missing required flag: --input")
	}
	opts.Project = normalizeIssueProject(opts.Project)
	return opts, nil
}

func ParseIssueImageAddArgs(args []string) (IssueImageAddOptions, error) {
	opts := IssueImageAddOptions{}
	issueIDFlag := ""
	pathFlag := ""
	implFlag := ""
	fs := flag.NewFlagSet("issue image add", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	addIssueProjectFlag(fs, &opts.Project)
	fs.StringVar(&implFlag, "impl", "", "forbidden for existing-issue commands")
	fs.StringVar(&issueIDFlag, "issue-id", "", "issue id (named alternative to positional)")
	fs.StringVar(&pathFlag, "path", "", "source image path (named alternative to positional)")
	fs.BoolVar(&opts.JSON, "json", false, "output image add result as JSON")
	if err := parseWithInterspersedFlags(fs, args); err != nil {
		return IssueImageAddOptions{}, err
	}
	if fs.NArg() > 2 {
		return IssueImageAddOptions{}, fmt.Errorf("usage: az issue image add [--project <project-id>] [--issue-id <issue-id>] [--path <file>] [<issue-id> <file>] [--json]")
	}
	if strings.TrimSpace(implFlag) != "" {
		return IssueImageAddOptions{}, fmt.Errorf("--impl is not supported for issue image add; issue implementations are already assigned")
	}
	if fs.NArg() >= 1 {
		opts.IssueID = fs.Arg(0)
	}
	if fs.NArg() >= 2 {
		opts.SourcePath = fs.Arg(1)
	}
	if strings.TrimSpace(issueIDFlag) != "" {
		opts.IssueID = strings.TrimSpace(issueIDFlag)
	}
	if strings.TrimSpace(pathFlag) != "" {
		opts.SourcePath = strings.TrimSpace(pathFlag)
	}
	if strings.TrimSpace(opts.IssueID) == "" || strings.TrimSpace(opts.SourcePath) == "" {
		return IssueImageAddOptions{}, fmt.Errorf("usage: az issue image add [--project <project-id>] [--issue-id <issue-id>] [--path <file>] [<issue-id> <file>] [--json]")
	}
	opts.Project = normalizeIssueProject(opts.Project)
	return opts, nil
}

func ParseIssueImageRemoveArgs(args []string) (IssueImageRemoveOptions, error) {
	opts := IssueImageRemoveOptions{}
	issueIDFlag := ""
	attachmentIDFlag := ""
	implFlag := ""
	fs := flag.NewFlagSet("issue image remove", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	addIssueProjectFlag(fs, &opts.Project)
	fs.StringVar(&implFlag, "impl", "", "forbidden for existing-issue commands")
	fs.StringVar(&issueIDFlag, "issue-id", "", "issue id (named alternative to positional)")
	fs.StringVar(&attachmentIDFlag, "attachment-id", "", "attachment id (named alternative to positional)")
	fs.BoolVar(&opts.JSON, "json", false, "output image remove result as JSON")
	if err := parseWithInterspersedFlags(fs, args); err != nil {
		return IssueImageRemoveOptions{}, err
	}
	if fs.NArg() > 2 {
		return IssueImageRemoveOptions{}, fmt.Errorf("usage: az issue image remove [--project <project-id>] [--issue-id <issue-id>] [--attachment-id <attachment-id>] [<issue-id> <attachment-id>] [--json]")
	}
	if strings.TrimSpace(implFlag) != "" {
		return IssueImageRemoveOptions{}, fmt.Errorf("--impl is not supported for issue image remove; issue implementations are already assigned")
	}
	if fs.NArg() >= 1 {
		opts.IssueID = fs.Arg(0)
	}
	if fs.NArg() >= 2 {
		opts.AttachmentID = fs.Arg(1)
	}
	if strings.TrimSpace(issueIDFlag) != "" {
		opts.IssueID = strings.TrimSpace(issueIDFlag)
	}
	if strings.TrimSpace(attachmentIDFlag) != "" {
		opts.AttachmentID = strings.TrimSpace(attachmentIDFlag)
	}
	if strings.TrimSpace(opts.IssueID) == "" || strings.TrimSpace(opts.AttachmentID) == "" {
		return IssueImageRemoveOptions{}, fmt.Errorf("usage: az issue image remove [--project <project-id>] [--issue-id <issue-id>] [--attachment-id <attachment-id>] [<issue-id> <attachment-id>] [--json]")
	}
	opts.Project = normalizeIssueProject(opts.Project)
	return opts, nil
}

func ParseIssueDocumentAddArgs(args []string) (IssueDocumentAddOptions, error) {
	opts := IssueDocumentAddOptions{}
	issueIDFlag := ""
	pathFlag := ""
	implFlag := ""
	fs := flag.NewFlagSet("issue document add", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	addIssueProjectFlag(fs, &opts.Project)
	fs.StringVar(&implFlag, "impl", "", "forbidden for existing-issue commands")
	fs.StringVar(&issueIDFlag, "issue-id", "", "issue id (named alternative to positional)")
	fs.StringVar(&pathFlag, "path", "", "source document path (named alternative to positional)")
	fs.BoolVar(&opts.JSON, "json", false, "output document add result as JSON")
	if err := parseWithInterspersedFlags(fs, args); err != nil {
		return IssueDocumentAddOptions{}, err
	}
	if fs.NArg() > 2 {
		return IssueDocumentAddOptions{}, fmt.Errorf("usage: az issue document add [--project <project-id>] [--issue-id <issue-id>] [--path <file>] [<issue-id> <file>] [--json]")
	}
	if strings.TrimSpace(implFlag) != "" {
		return IssueDocumentAddOptions{}, fmt.Errorf("--impl is not supported for issue document add; issue implementations are already assigned")
	}
	if fs.NArg() >= 1 {
		opts.IssueID = fs.Arg(0)
	}
	if fs.NArg() >= 2 {
		opts.SourcePath = fs.Arg(1)
	}
	if strings.TrimSpace(issueIDFlag) != "" {
		opts.IssueID = strings.TrimSpace(issueIDFlag)
	}
	if strings.TrimSpace(pathFlag) != "" {
		opts.SourcePath = strings.TrimSpace(pathFlag)
	}
	if strings.TrimSpace(opts.IssueID) == "" || strings.TrimSpace(opts.SourcePath) == "" {
		return IssueDocumentAddOptions{}, fmt.Errorf("usage: az issue document add [--project <project-id>] [--issue-id <issue-id>] [--path <file>] [<issue-id> <file>] [--json]")
	}
	opts.Project = normalizeIssueProject(opts.Project)
	return opts, nil
}

func ParseIssueDocumentListArgs(args []string) (IssueDocumentListOptions, error) {
	opts := IssueDocumentListOptions{}
	issueIDFlag := ""
	implFlag := ""
	fs := flag.NewFlagSet("issue document list", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	addIssueProjectFlag(fs, &opts.Project)
	fs.StringVar(&implFlag, "impl", "", "forbidden for existing-issue commands")
	fs.StringVar(&issueIDFlag, "issue-id", "", "issue id (named alternative to positional)")
	fs.BoolVar(&opts.JSON, "json", false, "output document list result as JSON")
	if err := parseWithInterspersedFlags(fs, args); err != nil {
		return IssueDocumentListOptions{}, err
	}
	if fs.NArg() > 1 {
		return IssueDocumentListOptions{}, fmt.Errorf("usage: az issue document list [--project <project-id>] [--issue-id <issue-id>] [<issue-id>] [--json]")
	}
	if strings.TrimSpace(implFlag) != "" {
		return IssueDocumentListOptions{}, fmt.Errorf("--impl is not supported for issue document list; issue implementations are already assigned")
	}
	if fs.NArg() >= 1 {
		opts.IssueID = fs.Arg(0)
	}
	if strings.TrimSpace(issueIDFlag) != "" {
		opts.IssueID = strings.TrimSpace(issueIDFlag)
	}
	if strings.TrimSpace(opts.IssueID) == "" {
		return IssueDocumentListOptions{}, fmt.Errorf("usage: az issue document list [--project <project-id>] [--issue-id <issue-id>] [<issue-id>] [--json]")
	}
	opts.Project = normalizeIssueProject(opts.Project)
	return opts, nil
}

func ParseIssueDocumentRemoveArgs(args []string) (IssueDocumentRemoveOptions, error) {
	opts := IssueDocumentRemoveOptions{}
	issueIDFlag := ""
	attachmentIDFlag := ""
	implFlag := ""
	fs := flag.NewFlagSet("issue document remove", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	addIssueProjectFlag(fs, &opts.Project)
	fs.StringVar(&implFlag, "impl", "", "forbidden for existing-issue commands")
	fs.StringVar(&issueIDFlag, "issue-id", "", "issue id (named alternative to positional)")
	fs.StringVar(&attachmentIDFlag, "attachment-id", "", "attachment id (named alternative to positional)")
	fs.BoolVar(&opts.JSON, "json", false, "output document remove result as JSON")
	if err := parseWithInterspersedFlags(fs, args); err != nil {
		return IssueDocumentRemoveOptions{}, err
	}
	if fs.NArg() > 2 {
		return IssueDocumentRemoveOptions{}, fmt.Errorf("usage: az issue document remove [--project <project-id>] [--issue-id <issue-id>] [--attachment-id <attachment-id>] [<issue-id> <attachment-id>] [--json]")
	}
	if strings.TrimSpace(implFlag) != "" {
		return IssueDocumentRemoveOptions{}, fmt.Errorf("--impl is not supported for issue document remove; issue implementations are already assigned")
	}
	if fs.NArg() >= 1 {
		opts.IssueID = fs.Arg(0)
	}
	if fs.NArg() >= 2 {
		opts.AttachmentID = fs.Arg(1)
	}
	if strings.TrimSpace(issueIDFlag) != "" {
		opts.IssueID = strings.TrimSpace(issueIDFlag)
	}
	if strings.TrimSpace(attachmentIDFlag) != "" {
		opts.AttachmentID = strings.TrimSpace(attachmentIDFlag)
	}
	if strings.TrimSpace(opts.IssueID) == "" || strings.TrimSpace(opts.AttachmentID) == "" {
		return IssueDocumentRemoveOptions{}, fmt.Errorf("usage: az issue document remove [--project <project-id>] [--issue-id <issue-id>] [--attachment-id <attachment-id>] [<issue-id> <attachment-id>] [--json]")
	}
	opts.Project = normalizeIssueProject(opts.Project)
	return opts, nil
}

func ParseIssueBulkCreateArgs(args []string) (IssueBulkCreateOptions, error) {
	opts := IssueBulkCreateOptions{}
	fs := flag.NewFlagSet("issue bulk-create", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	addIssueProjectFlag(fs, &opts.Project)
	fs.StringVar(&opts.Implementation, "impl", "", "target implementation key")
	fs.StringVar(&opts.InputPath, "input", "", "path to JSON array input")
	fs.BoolVar(&opts.DryRun, "dry-run", false, "validate and preview without mutating")
	fs.BoolVar(&opts.JSON, "json", false, "output bulk-create result as JSON")
	if err := fs.Parse(args); err != nil {
		return IssueBulkCreateOptions{}, err
	}
	if fs.NArg() != 0 {
		return IssueBulkCreateOptions{}, fmt.Errorf("unexpected argument: %s", fs.Arg(0))
	}
	if strings.TrimSpace(opts.InputPath) == "" {
		return IssueBulkCreateOptions{}, fmt.Errorf("missing required flag: --input")
	}
	opts.Project = normalizeIssueProject(opts.Project)
	return opts, nil
}

func ParseIssueBulkUpdateArgs(args []string) (IssueBulkUpdateOptions, error) {
	opts := IssueBulkUpdateOptions{}
	fs := flag.NewFlagSet("issue bulk-update", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	addIssueProjectFlag(fs, &opts.Project)
	fs.StringVar(&opts.Implementation, "impl", "", "target implementation key")
	fs.StringVar(&opts.InputPath, "input", "", "path to JSON array input")
	fs.BoolVar(&opts.DryRun, "dry-run", false, "validate and preview without mutating")
	fs.BoolVar(&opts.JSON, "json", false, "output bulk-update result as JSON")
	if err := fs.Parse(args); err != nil {
		return IssueBulkUpdateOptions{}, err
	}
	if fs.NArg() != 0 {
		return IssueBulkUpdateOptions{}, fmt.Errorf("unexpected argument: %s", fs.Arg(0))
	}
	if strings.TrimSpace(opts.InputPath) == "" {
		return IssueBulkUpdateOptions{}, fmt.Errorf("missing required flag: --input")
	}
	opts.Project = normalizeIssueProject(opts.Project)
	return opts, nil
}

func configuredIssueImplementations(tasks []domain.Task) []string {
	seen := make(map[string]struct{})
	impls := make([]string, 0, 4)
	for _, task := range tasks {
		for _, impl := range task.Implementations {
			trimmed := strings.TrimSpace(impl)
			if trimmed == "" {
				continue
			}
			if _, ok := seen[trimmed]; ok {
				continue
			}
			seen[trimmed] = struct{}{}
			impls = append(impls, trimmed)
		}
	}
	sort.Strings(impls)
	return impls
}

func listTasksSnapshotForCLI(ctx context.Context, deps *Dependencies) (daemonclient.TaskSnapshot, error) {
	if deps == nil || deps.DaemonClient == nil {
		return daemonclient.TaskSnapshot{}, fmt.Errorf("daemon client unavailable")
	}
	return deps.DaemonClient.ListTasksSnapshot(ctx)
}

func listTasksSnapshotWithDependenciesForCLI(ctx context.Context, deps *Dependencies) (daemonclient.TaskSnapshot, error) {
	if deps == nil || deps.DaemonClient == nil {
		return daemonclient.TaskSnapshot{}, fmt.Errorf("daemon client unavailable")
	}
	return deps.DaemonClient.ListTasksSnapshotWithDependencies(ctx)
}

func resolveIssueWriteImplementation(ctx context.Context, deps *Dependencies, provided string) (string, error) {
	trimmed := strings.TrimSpace(provided)
	impls, err := issueWriteImplementationOptions(ctx, deps)
	if trimmed != "" {
		if err != nil {
			return "", fmt.Errorf("unable to validate implementation %q: %w", trimmed, err)
		}
		if !implementationOptionExists(impls, trimmed) {
			return "", unknownIssueWriteImplementationError(trimmed, impls)
		}
		return trimmed, nil
	}

	if err != nil {
		return "", fmt.Errorf("missing required flag: --impl (unable to infer implementation automatically: %v). Specify --impl <implementation>", err)
	}
	switch len(impls) {
	case 1:
		return impls[0], nil
	default:
		return "", fmt.Errorf("missing required flag: --impl (multiple implementations configured: %s)", strings.Join(impls, ", "))
	}
}

func resolveIssueWriteImplementations(ctx context.Context, deps *Dependencies, provided []string) ([]string, error) {
	normalized := dedupeTrimmed(provided)
	if len(normalized) == 0 {
		impl, err := resolveIssueWriteImplementation(ctx, deps, "")
		if err != nil {
			return nil, err
		}
		return []string{impl}, nil
	}
	impls, err := issueWriteImplementationOptions(ctx, deps)
	if err != nil {
		return nil, fmt.Errorf("unable to validate implementations: %w", err)
	}
	for _, impl := range normalized {
		if !implementationOptionExists(impls, impl) {
			return nil, unknownIssueWriteImplementationError(impl, impls)
		}
	}
	return normalized, nil
}

func issueWriteImplementationOptions(ctx context.Context, deps *Dependencies) ([]string, error) {
	if deps == nil || deps.DaemonClient == nil {
		return nil, fmt.Errorf("daemon client is required to validate implementations")
	}
	snapshot, err := commandWithDaemonAutostartRetry(ctx, deps, func(callCtx context.Context) (daemonclient.TaskSnapshot, error) {
		return listTasksSnapshotForCLI(callCtx, deps)
	})
	if err != nil {
		return nil, err
	}
	impls := configuredIssueImplementations(snapshot.Tasks)
	if len(impls) == 0 {
		return []string{"default"}, nil
	}
	return impls, nil
}

func implementationOptionExists(options []string, impl string) bool {
	for _, option := range options {
		if option == impl {
			return true
		}
	}
	return false
}

func unknownIssueWriteImplementationError(impl string, known []string) error {
	return fmt.Errorf("unknown implementation %q (known implementations: %s). Run `az impl list` to inspect implementation assignments. If you meant to parent work under %q, omit --impl in the correct AZEDARACH_ISSUE_ID context or add a parent-child edge instead", impl, strings.Join(known, ", "), impl)
}

func ConfigSetCommand(deps *Dependencies, opts ConfigSetOptions) error {
	if deps == nil {
		return fmt.Errorf("config set: missing dependencies")
	}

	projectDir := strings.TrimSpace(opts.ProjectDir)
	if projectDir == "" {
		projectDir = deps.RepoDir
	}

	configPath, err := resolveWritableConfigPath(projectDir)
	if err != nil {
		return fmt.Errorf("resolve config path: %w", err)
	}

	cfg, err := config.LoadConfig(projectDir)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	renderedValue, err := setConfigValue(cfg, opts.Key, opts.Value)
	if err != nil {
		return err
	}

	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		return fmt.Errorf("create config directory: %w", err)
	}
	if err := config.SaveConfig(cfg, configPath); err != nil {
		return fmt.Errorf("save config: %w", err)
	}

	fmt.Printf("Updated %s: %s=%s\n", configPath, opts.Key, renderedValue)
	switch opts.Key {
	case "spec.enabled":
		if renderedValue == "true" {
			fmt.Println("Spec workflows are enabled.")
		} else {
			fmt.Println("Spec workflows are disabled. `az prime` will stop mentioning spec and `az spec` commands will fail until re-enabled.")
		}
	case "diagnostics.latencyTrace":
		if renderedValue == "true" {
			latencytrace.SetConfigEnabled(true)
			fmt.Println("Latency trace logging is enabled. Restart the daemon for daemon-side trace logs to use the persisted setting.")
		} else {
			latencytrace.SetConfigEnabled(false)
			fmt.Println("Latency trace logging is disabled.")
		}
	}

	return nil
}

func SyncCommand(deps *Dependencies, opts SyncOptions) error {
	if deps == nil {
		return fmt.Errorf("sync: missing dependencies")
	}

	ctx := context.Background()
	if err := ensureDaemon(ctx, deps, "cli"); err != nil {
		return err
	}
	if opts.Conflicts {
		conflicts, err := deps.DaemonClient.ListIssueSyncConflicts(ctx, false)
		if err != nil {
			return fmt.Errorf("list sync conflicts: %w", err)
		}
		if opts.JSON {
			data, err := json.MarshalIndent(conflicts, "", "  ")
			if err != nil {
				return fmt.Errorf("marshal sync conflicts: %w", err)
			}
			fmt.Println(string(data))
			return nil
		}
		if len(conflicts.Conflicts) == 0 {
			fmt.Println("No unresolved sync conflicts.")
			return nil
		}
		fmt.Printf("Unresolved sync conflicts: %d\n", len(conflicts.Conflicts))
		for _, conflict := range conflicts.Conflicts {
			fmt.Printf("- %s %s: local=%q remote=%q\n", conflict.IssueID, conflict.Field, conflict.LocalValue, conflict.RemoteValue)
		}
		return nil
	}

	projectDir := strings.TrimSpace(opts.ProjectDir)
	if projectDir == "" {
		projectDir = deps.RepoDir
	}

	targetPaths := []string{projectDir}
	if opts.All {
		worktrees, err := deps.DaemonClient.ListWorktrees(ctx)
		if err != nil {
			return fmt.Errorf("list daemon worktrees: %w", err)
		}
		targetPaths = targetPaths[:0]
		seen := make(map[string]struct{}, len(worktrees))
		for _, worktree := range worktrees {
			path := strings.TrimSpace(worktree.Path)
			if path == "" {
				continue
			}
			if _, ok := seen[path]; ok {
				continue
			}
			seen[path] = struct{}{}
			targetPaths = append(targetPaths, path)
		}
		if len(targetPaths) == 0 {
			targetPaths = []string{projectDir}
		}
	}

	if !opts.JSON {
		fmt.Println("Syncing issue tracker state...")
		fmt.Printf("Project: %s\n", projectDir)
		if opts.All {
			fmt.Printf("Targets: %d worktree(s)\n", len(targetPaths))
			for _, targetPath := range targetPaths {
				fmt.Printf("  %s\n", targetPath)
			}
		}
	}

	summary, err := deps.DaemonClient.RunIssueSync(ctx)
	if err != nil {
		return fmt.Errorf("run issue tracker sync: %w", err)
	}
	if opts.JSON {
		data, err := json.MarshalIndent(summary, "", "  ")
		if err != nil {
			return fmt.Errorf("marshal sync summary: %w", err)
		}
		fmt.Println(string(data))
		return nil
	}
	fmt.Println("")
	if summary.Skipped {
		fmt.Printf("Sync skipped: %s\n", summary.Reason)
		return nil
	}
	fmt.Printf("Linear: remote=%d local=%d imported=%d updated_local=%d pushed_remote=%d conflicts=%d\n", summary.RemoteIssues, summary.LocalIssues, summary.Imported, summary.UpdatedLocal, summary.PushedRemote, summary.Conflicts)
	if summary.Incremental {
		fmt.Printf("Incremental: cursor=%s", summary.Cursor)
		if summary.RemoteScopeIssues > 0 {
			fmt.Printf(" remote_scope=%d", summary.RemoteScopeIssues)
		}
		fmt.Println("")
	}
	fmt.Printf("Efficiency: api_requests=%d skipped_unchanged=%d pending_pushes=%d skipped_push_out_of_scope=%d out_of_scope_refs=%d retried_requests=%d\n", summary.APIRequests, summary.SkippedUnchanged, summary.PendingPushes, summary.SkippedPushOutOfScope, summary.OutOfScopeRefs, summary.RetriedRequests)
	if summary.PushBudgetExhausted {
		fmt.Println("Push budget exhausted; remaining local changes will be retried in a later sync.")
	}
	if summary.RateLimitLimit > 0 || summary.RateLimitRemaining > 0 || summary.RateLimitReset != "" {
		fmt.Printf("Rate limit: remaining=%d limit=%d reset=%s\n", summary.RateLimitRemaining, summary.RateLimitLimit, summary.RateLimitReset)
	}
	fmt.Printf("Sync summary: targets=%d, provider=%s\n", len(targetPaths), summary.Provider)
	return nil
}

func resolveWritableConfigPath(projectDir string) (string, error) {
	baseDir, err := config.ResolveConfigBase(projectDir)
	if err != nil {
		return "", err
	}
	return filepath.Join(baseDir, config.ConfigDirName, config.ConfigFileName), nil
}

func setConfigValue(cfg *config.Config, key, value string) (string, error) {
	switch strings.TrimSpace(key) {
	case "spec.enabled":
		parsed, ok := parseBooleanConfigValue(value)
		if !ok {
			return "", fmt.Errorf("Invalid boolean value '%s' for spec.enabled. Use true/false, on/off, yes/no, or 1/0.", value)
		}
		cfg.Spec.Enabled = parsed
		return fmt.Sprintf("%t", parsed), nil
	case "diagnostics.latencyTrace":
		parsed, ok := parseBooleanConfigValue(value)
		if !ok {
			return "", fmt.Errorf("Invalid boolean value '%s' for diagnostics.latencyTrace. Use true/false, on/off, yes/no, or 1/0.", value)
		}
		cfg.Diagnostics.LatencyTrace = parsed
		return fmt.Sprintf("%t", parsed), nil
	default:
		return "", fmt.Errorf("Unsupported config key '%s'. Supported keys: spec.enabled, diagnostics.latencyTrace", key)
	}
}

func parseBooleanConfigValue(value string) (bool, bool) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "true", "1", "yes", "on":
		return true, true
	case "false", "0", "no", "off":
		return false, true
	default:
		return false, false
	}
}

func ExportCommand(deps *Dependencies, opts ExportOptions) error {
	ctx := context.Background()
	if err := ensureDaemon(ctx, deps, "cli"); err != nil {
		return err
	}

	resp, err := deps.DaemonClient.Command(ctx, protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       makeRequestID(commandTaskSnapshotExport),
		Kind:            protocol.EnvelopeKindCommand,
		Meta: protocol.Metadata{
			ProjectID: naming.ProjectID(deps.ProjectID),
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

func ImplDeleteCommand(deps *Dependencies, opts ImplDeleteOptions) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := ensureDaemon(ctx, deps, "cli"); err != nil {
		return err
	}

	impl := strings.TrimSpace(opts.Implementation)
	if impl == "" {
		return fmt.Errorf("implementation is required")
	}

	snapshot, err := listTasksSnapshotForCLI(ctx, deps)
	if err != nil {
		return fmt.Errorf("failed to list issues for implementation delete: %w", err)
	}

	updated := make([]string, 0, 8)
	for _, task := range snapshot.Tasks {
		if len(task.Implementations) == 0 {
			continue
		}
		nextImpls := make([]string, 0, len(task.Implementations))
		removed := false
		for _, existing := range task.Implementations {
			if strings.TrimSpace(existing) == impl {
				removed = true
				continue
			}
			nextImpls = append(nextImpls, existing)
		}
		if !removed {
			continue
		}

		fullTask, err := loadIssueDetailTask(ctx, deps, task.ID.String())
		if err != nil {
			return fmt.Errorf("failed to load issue %s for implementation delete: %w", task.ID, err)
		}
		update := daemonclient.TaskUpdateParams{
			Title:           fullTask.Title,
			Description:     fullTask.Description,
			Type:            fullTask.Type,
			Priority:        fullTask.Priority,
			Implementations: nextImpls,
		}
		if err := deps.DaemonClient.UpdateTaskDetails(ctx, task.ID.String(), update); err != nil {
			return fmt.Errorf("failed to remove implementation %s from issue %s: %w", impl, task.ID, err)
		}
		updated = append(updated, task.ID.String())
	}

	if len(updated) == 0 {
		fmt.Printf("No issues reference implementation: %s\n", impl)
		return nil
	}
	fmt.Printf("Deleted implementation assignment: %s\n", impl)
	fmt.Printf("Updated issues: %d\n", len(updated))
	return nil
}

func ImplListCommand(deps *Dependencies, _ ImplListOptions) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := ensureDaemon(ctx, deps, "cli"); err != nil {
		return err
	}

	snapshot, err := listTasksSnapshotForCLI(ctx, deps)
	if err != nil {
		return fmt.Errorf("failed to list issues for implementation list: %w", err)
	}

	impls := collectImplementations(snapshot.Tasks)
	if len(impls) == 0 {
		fmt.Println("No implementations found in issue assignments.")
		return nil
	}
	fmt.Printf("Implementations: %d\n", len(impls))
	for _, impl := range impls {
		fmt.Println(impl)
	}
	return nil
}

func ImplMigrateCommand(deps *Dependencies, opts ImplMigrateOptions) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := ensureDaemon(ctx, deps, "cli"); err != nil {
		return err
	}

	from := strings.TrimSpace(opts.FromImplementation)
	to := strings.TrimSpace(opts.ToImplementation)
	if from == "" || to == "" {
		return fmt.Errorf("both source and destination implementations are required")
	}
	if from == to {
		return fmt.Errorf("source and destination implementations must differ")
	}

	snapshot, err := listTasksSnapshotForCLI(ctx, deps)
	if err != nil {
		return fmt.Errorf("failed to list issues for implementation migrate: %w", err)
	}

	updated := make([]string, 0, 8)
	for _, task := range snapshot.Tasks {
		if len(task.Implementations) == 0 {
			continue
		}
		nextImpls := make([]string, 0, len(task.Implementations))
		changed := false
		for _, existing := range task.Implementations {
			if strings.TrimSpace(existing) == from {
				nextImpls = append(nextImpls, to)
				changed = true
				continue
			}
			nextImpls = append(nextImpls, existing)
		}
		if !changed {
			continue
		}

		nextImpls = dedupeTrimmed(nextImpls)
		fullTask, err := loadIssueDetailTask(ctx, deps, task.ID.String())
		if err != nil {
			return fmt.Errorf("failed to load issue %s for implementation migrate: %w", task.ID, err)
		}
		update := daemonclient.TaskUpdateParams{
			Title:           fullTask.Title,
			Description:     fullTask.Description,
			Type:            fullTask.Type,
			Priority:        fullTask.Priority,
			Implementations: nextImpls,
		}
		if err := deps.DaemonClient.UpdateTaskDetails(ctx, task.ID.String(), update); err != nil {
			return fmt.Errorf("failed to migrate implementation %s -> %s for issue %s: %w", from, to, task.ID, err)
		}
		updated = append(updated, task.ID.String())
	}

	if len(updated) == 0 {
		fmt.Printf("No issues reference implementation: %s\n", from)
		return nil
	}
	fmt.Printf("Migrated implementation assignment: %s -> %s\n", from, to)
	fmt.Printf("Updated issues: %d\n", len(updated))
	return nil
}

func IssueListCommand(deps *Dependencies, opts IssueListOptions) error {
	restoreProject := applyIssueProjectOverride(deps, opts.Project)
	defer restoreProject()

	ctx, cancel := context.WithTimeout(context.Background(), daemonCommandTimeout)
	defer cancel()
	if err := ensureDaemon(ctx, deps, "cli"); err != nil {
		return err
	}

	var (
		snapshot daemonclient.TaskSnapshot
		err      error
	)
	if strings.TrimSpace(opts.Query) != "" {
		snapshot, err = deps.DaemonClient.ListTasksSnapshotWithQuery(ctx, opts.Query)
	} else if opts.Deps || len(opts.DependsOnIDs) > 0 {
		snapshot, err = listTasksSnapshotWithDependenciesForCLI(ctx, deps)
	} else {
		snapshot, err = listTasksSnapshotForCLI(ctx, deps)
	}
	if err != nil {
		return fmt.Errorf("failed to list issues: %w", err)
	}
	tasks := snapshot.Tasks
	if len(opts.IDs) > 0 {
		tasks = filterTasksByIDs(tasks, opts.IDs)
	}
	if len(opts.States) > 0 {
		tasks = filterTasksByStatus(tasks, opts.States)
	}
	if len(opts.ParentIDs) > 0 {
		tasks = filterTasksByParentIDs(tasks, opts.ParentIDs)
	}
	if len(opts.DependsOnIDs) > 0 {
		tasks = filterTasksByDependencyIDs(tasks, opts.DependsOnIDs)
	}
	tasks = filterTasksByTimeRange(tasks, opts.CreatedAfter, opts.CreatedBefore, opts.UpdatedAfter, opts.UpdatedBefore)
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

	var topLevel []string
	var links []dependencyLink
	if opts.Deps {
		topLevel, links = buildListDependencyContext(tasks)
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
		fmt.Println()
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 8, 2, ' ', 0)
	if opts.Deps {
		fmt.Fprintln(w, "ID\tSTATUS\tPRIORITY\tTYPE\tASSIGNEE\tEST\tIMPL\tDEPS\tTITLE")
	} else {
		fmt.Fprintln(w, "ID\tSTATUS\tPRIORITY\tTYPE\tASSIGNEE\tEST\tIMPL\tTITLE")
	}
	for _, task := range tasks {
		assigneeSummary := "-"
		if strings.TrimSpace(task.Assignee) != "" {
			assigneeSummary = strings.TrimSpace(task.Assignee)
		}
		estimateSummary := "-"
		if task.Estimate != nil {
			estimateSummary = strconv.Itoa(*task.Estimate)
		}
		implSummary := formatIssueImplementationSummary(task.Implementations)
		if opts.Deps {
			fmt.Fprintf(
				w,
				"%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
				task.ID,
				task.Status,
				task.Priority.String(),
				task.Type,
				assigneeSummary,
				estimateSummary,
				implSummary,
				formatDependencySummary(task.Dependencies),
				task.Title,
			)
			continue
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", task.ID, task.Status, task.Priority.String(), task.Type, assigneeSummary, estimateSummary, implSummary, task.Title)
	}
	if err := w.Flush(); err != nil {
		return err
	}

	truncatedText := "no"
	if truncated {
		truncatedText = "yes"
	}
	fmt.Printf("\nList window: listed=%d limit=%d total=%d truncated=%s\n", len(tasks), limit, total, truncatedText)
	if truncated {
		fmt.Println("Window note: additional matching issues may exist beyond current limit.")
	} else {
		fmt.Println("Window note: all matching issues are shown.")
	}
	return nil
}

type issueGetManyItem struct {
	ID           string              `json:"id"`
	Status       string              `json:"status"`
	Issue        *domain.Task        `json:"issue,omitempty"`
	Dependencies []dependencyDetails `json:"dependencies,omitempty"`
	Dependents   []dependencyDetails `json:"dependents,omitempty"`
	Error        string              `json:"error,omitempty"`
}

type issueGetManyResult struct {
	Requested int                `json:"requested"`
	Found     int                `json:"found"`
	Missing   int                `json:"missing"`
	Results   []issueGetManyItem `json:"results"`
}

func IssueGetManyCommand(deps *Dependencies, opts IssueGetManyOptions) error {
	restoreProject := applyIssueProjectOverride(deps, opts.Project)
	defer restoreProject()

	ctx, cancel := context.WithTimeout(context.Background(), daemonCommandTimeout)
	defer cancel()
	if err := ensureDaemon(ctx, deps, "cli"); err != nil {
		return err
	}

	snapshot, err := deps.DaemonClient.GetManyTaskSnapshot(ctx, opts.IssueIDs)
	if err != nil {
		return fmt.Errorf("failed to get issues: %w", err)
	}
	tasksByID := make(map[naming.IssueID]domain.Task, len(snapshot.Tasks))
	for _, task := range snapshot.Tasks {
		tasksByID[task.ID] = task
	}

	result := issueGetManyResult{
		Requested: len(opts.IssueIDs),
		Results:   make([]issueGetManyItem, 0, len(opts.IssueIDs)),
	}
	for _, issueID := range opts.IssueIDs {
		typedIssueID, parseErr := naming.ParseIssueID(issueID)
		task, ok := tasksByID[typedIssueID]
		if parseErr != nil {
			ok = false
		}
		if !ok {
			result.Missing++
			result.Results = append(result.Results, issueGetManyItem{
				ID:     issueID,
				Status: "not_found",
				Error:  "issue not found",
			})
			continue
		}
		result.Found++
		taskCopy := task
		if !opts.IncludeNotes {
			taskCopy.Notes = ""
		}
		dependencies, dependents := buildDependencyProjection(task, snapshot.Tasks)
		result.Results = append(result.Results, issueGetManyItem{
			ID:           issueID,
			Status:       "found",
			Issue:        &taskCopy,
			Dependencies: dependencies,
			Dependents:   dependents,
		})
	}

	if opts.JSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(result)
	}

	for _, item := range result.Results {
		switch item.Status {
		case "found":
			fmt.Printf("%s found: %s [%s]\n", item.ID, item.Issue.Title, item.Issue.Status)
			if opts.IncludeNotes && strings.TrimSpace(item.Issue.Notes) != "" {
				fmt.Printf("Notes:\n%s\n", item.Issue.Notes)
			}
		default:
			fmt.Printf("%s not_found: %s\n", item.ID, item.Error)
		}
	}
	fmt.Printf("Summary: requested=%d found=%d missing=%d\n", result.Requested, result.Found, result.Missing)
	return nil
}

func IssueGetCommand(deps *Dependencies, opts IssueGetOptions) error {
	restoreProject := applyIssueProjectOverride(deps, opts.Project)
	defer restoreProject()

	startedAt := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), daemonCommandTimeout)
	defer cancel()
	if err := ensureDaemon(ctx, deps, "cli"); err != nil {
		if deps.Logger != nil {
			deps.Logger.Warn("issue get failed during daemon ensure", "issue_id", opts.IssueID, "elapsed_ms", time.Since(startedAt).Milliseconds(), "error", err)
		}
		return err
	}

	snapshot, err := deps.DaemonClient.GetTaskSnapshot(ctx, opts.IssueID)
	if err != nil {
		if deps.Logger != nil {
			deps.Logger.Warn("issue get failed during daemon read", "issue_id", opts.IssueID, "elapsed_ms", time.Since(startedAt).Milliseconds(), "error", err)
		}
		if strings.Contains(err.Error(), fmt.Sprintf("issue not found: %s", opts.IssueID)) {
			return fmt.Errorf("issue not found: %s", opts.IssueID)
		}
		return fmt.Errorf("failed to get issue %s: %w", opts.IssueID, err)
	}
	if err := snapshot.RequireFullDetails("issue get"); err != nil {
		return fmt.Errorf("failed to get issue %s: %w", opts.IssueID, err)
	}

	task, ok := findTaskByID(snapshot.Tasks, opts.IssueID)
	if !ok {
		if deps.Logger != nil {
			deps.Logger.Info("issue get not found", "issue_id", opts.IssueID, "elapsed_ms", time.Since(startedAt).Milliseconds())
		}
		return fmt.Errorf("issue not found: %s", opts.IssueID)
	}
	if deps.Logger != nil {
		deps.Logger.Info("issue get completed", "issue_id", opts.IssueID, "context_task_count", len(snapshot.Tasks), "elapsed_ms", time.Since(startedAt).Milliseconds())
	}

	decisions := fetchIssueDecisions(ctx, deps, opts.IssueID)

	if opts.JSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(issueGetJSONEnvelope{
			Task:      &task,
			Decisions: decisions,
		})
	}

	fmt.Printf("ID: %s\n", task.ID)
	fmt.Printf("Title: %s\n", task.Title)
	fmt.Printf("Status: %s\n", task.Status)
	fmt.Printf("Priority: %s\n", task.Priority.String())
	fmt.Printf("Type: %s\n", task.Type)
	if task.ParentID != nil {
		fmt.Printf("Parent: %s\n", *task.ParentID)
	}
	if len(task.Implementations) > 0 {
		fmt.Printf("Implementations: %s\n", strings.Join(task.Implementations, ", "))
	}
	if strings.TrimSpace(task.Assignee) != "" {
		fmt.Printf("Assignee: %s\n", strings.TrimSpace(task.Assignee))
	}
	if len(task.Labels) > 0 {
		fmt.Printf("Labels: %s\n", strings.Join(task.Labels, ", "))
	}
	if task.Estimate != nil {
		fmt.Printf("Estimate: %d\n", *task.Estimate)
	}
	if runtimeSummary := renderIssueRuntimeSummary(task); runtimeSummary != "" {
		fmt.Printf("Runtime: %s\n", runtimeSummary)
	}
	if gitSummary := renderIssueGitSummary(task); gitSummary != "" {
		fmt.Printf("Git: %s\n", gitSummary)
	}
	dependencies, dependents := buildDependencyProjection(task, snapshot.Tasks)
	fmt.Printf("Dependencies: %d\n", len(dependencies))
	printDependencies(dependencies)
	printDependents(dependents)
	printIssueDecisions(decisions)
	if task.Description != "" {
		fmt.Printf("Description: %s\n", task.Description)
	}
	if strings.TrimSpace(task.Notes) != "" {
		if opts.IncludeNotes {
			fmt.Printf("Notes:\n%s\n", task.Notes)
		} else {
			fmt.Printf("Notes: present (hidden in text output; use `az issue get %s --with-notes` for full notes or `--json` for structured context)\n", task.ID)
		}
	}
	if strings.TrimSpace(task.Design) != "" {
		fmt.Printf("Design:\n%s\n", task.Design)
	}
	if strings.TrimSpace(task.Acceptance) != "" {
		fmt.Printf("Acceptance:\n%s\n", task.Acceptance)
	}
	fmt.Printf("Created: %s\n", task.CreatedAt.UTC().Format(time.RFC3339))
	fmt.Printf("Updated: %s\n", task.UpdatedAt.UTC().Format(time.RFC3339))
	return nil
}

func IssueEventsCommand(deps *Dependencies, opts IssueEventsOptions) error {
	restoreProject := applyIssueProjectOverride(deps, opts.Project)
	defer restoreProject()

	ctx, cancel := context.WithTimeout(context.Background(), daemonCommandTimeout)
	defer cancel()
	if err := ensureDaemon(ctx, deps, "cli"); err != nil {
		return err
	}

	events, err := deps.DaemonClient.ListTaskEvents(ctx, opts.IssueID, opts.EventTypes, opts.Limit)
	if err != nil {
		if strings.Contains(err.Error(), fmt.Sprintf("issue not found: %s", opts.IssueID)) {
			return fmt.Errorf("issue not found: %s", opts.IssueID)
		}
		return fmt.Errorf("failed to list issue events for %s: %w", opts.IssueID, err)
	}

	if opts.JSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(struct {
			IssueID string                         `json:"issue_id"`
			Events  []domain.IssueObservationEvent `json:"events"`
		}{
			IssueID: opts.IssueID,
			Events:  events,
		})
	}

	if len(events) == 0 {
		fmt.Printf("No observation events for issue %s\n", opts.IssueID)
		return nil
	}
	for _, event := range events {
		fmt.Printf("%d %s %s", event.ID, event.ObservedAt.Format(time.RFC3339Nano), event.Type)
		if event.Source != "" {
			fmt.Printf(" source=%s", event.Source)
		}
		if event.SourceCommand != "" {
			fmt.Printf(" command=%s", event.SourceCommand)
		}
		if event.OperationID != "" {
			fmt.Printf(" operation=%s", event.OperationID)
		}
		if event.SessionID != "" {
			fmt.Printf(" session=%s", event.SessionID)
		}
		if event.WorktreePath != "" {
			fmt.Printf(" worktree=%s", event.WorktreePath)
		}
		if len(event.Payload) > 0 {
			payload, err := json.Marshal(event.Payload)
			if err != nil {
				return fmt.Errorf("marshal event payload: %w", err)
			}
			fmt.Printf(" payload=%s", payload)
		}
		fmt.Println()
	}
	return nil
}

// issueDecisionSummary is the per-decision payload embedded in `az issue get`
// output (text + JSON). It merges link-level fields (relation, note) with the
// linked decision's identity (slug, title) so a single row is self-describing.
type issueDecisionSummary struct {
	ID       string `json:"id"`
	Title    string `json:"title,omitempty"`
	Relation string `json:"relation"`
	Note     string `json:"note,omitempty"`
}

// issueGetJSONEnvelope is the JSON shape produced by `az issue get --json`.
// The Task is embedded so existing top-level keys (id, title, status, ...) keep
// their position in the output; consumers that unmarshal into domain.Task
// continue to work. The new Decisions key is omitted when empty so issues
// without linked decisions render the same as before.
type issueGetJSONEnvelope struct {
	*domain.Task
	Decisions []issueDecisionSummary `json:"decisions,omitempty"`
}

// fetchIssueDecisions queries the daemon for decisions linked to the given
// issue. Errors are treated as non-fatal: callers see an empty slice and the
// rest of the issue render proceeds. This is symmetric with the TUI's
// graceful-degradation path and keeps `az issue get` reliable when the daemon
// is older than the decisions feature.
func fetchIssueDecisions(ctx context.Context, deps *Dependencies, issueID string) []issueDecisionSummary {
	if deps == nil || deps.DaemonClient == nil || strings.TrimSpace(issueID) == "" {
		return nil
	}
	result, err := deps.DaemonClient.ListDecisionLinks(ctx, daemonclient.DecisionLinkListRequest{
		TargetKind:       daemonclient.DecisionTargetIssue,
		TargetID:         issueID,
		IncludeDecisions: true,
	})
	if err != nil {
		if deps.Logger != nil {
			deps.Logger.Debug("issue get decision link fetch failed", "issue_id", issueID, "error", err)
		}
		return nil
	}
	if len(result.Links) == 0 {
		return nil
	}
	byID := make(map[string]daemonclient.Decision, len(result.Decisions))
	for _, d := range result.Decisions {
		byID[d.ID] = d
	}
	out := make([]issueDecisionSummary, 0, len(result.Links))
	for _, link := range result.Links {
		summary := issueDecisionSummary{
			ID:       link.DecisionID,
			Relation: string(link.Relation),
			Note:     link.Note,
		}
		if d, ok := byID[link.DecisionID]; ok {
			summary.Title = d.Title
		}
		out = append(out, summary)
	}
	return out
}

// printIssueDecisions writes the Decisions section of `az issue get`. Output
// format mirrors the TUI's issue detail panel: a header line with the count,
// then one row per decision with relation, slug, status, and title/note when
// available. Emits nothing when there are no decisions.
func printIssueDecisions(decisions []issueDecisionSummary) {
	fmt.Printf("Decisions: %d\n", len(decisions))
	for _, d := range decisions {
		relation := strings.TrimSpace(d.Relation)
		if relation == "" {
			relation = "applies-to"
		}
		var b strings.Builder
		fmt.Fprintf(&b, "  %-12s %s", relation, d.ID)
		if title := strings.TrimSpace(d.Title); title != "" {
			b.WriteString("  ")
			b.WriteString(title)
		}
		if note := strings.TrimSpace(d.Note); note != "" {
			b.WriteString("  — ")
			b.WriteString(note)
		}
		fmt.Println(b.String())
	}
}

func IssueCheckCommand(deps *Dependencies, opts IssueCheckOptions) error {
	return IssueGetCommand(deps, IssueGetOptions{
		Project: opts.Project,
		IssueID: opts.IssueID,
		JSON:    opts.JSON,
	})
}

func ProjectScriptsStatusCommand(deps *Dependencies, opts ProjectScriptsStatusOptions) error {
	ctx, cancel := context.WithTimeout(context.Background(), daemonCommandTimeout)
	defer cancel()
	if err := ensureDaemon(ctx, deps, "cli"); err != nil {
		return err
	}

	result, err := deps.DaemonClient.ScheduledScriptsStatus(ctx, opts.Names)
	if err != nil {
		return fmt.Errorf("failed to get scheduled script status: %w", err)
	}
	if opts.JSON {
		return printJSON(result)
	}
	if len(result.Scripts) == 0 {
		fmt.Println("No scheduled project scripts configured.")
		return nil
	}
	fmt.Printf("Scheduled project scripts for %s:\n", result.ProjectID)
	for _, script := range result.Scripts {
		state := "idle"
		if !script.Enabled {
			state = "disabled"
		} else if script.Running {
			state = "running"
		} else if script.LastError != "" {
			state = "failed"
		}
		fmt.Printf("- %s\t%s\tinterval=%s\truns=%d\tskips=%d\n", script.Name, state, script.Interval, script.RunCount, script.SkipCount)
		if script.NextRunAt != nil {
			fmt.Printf("  next: %s\n", script.NextRunAt.UTC().Format(time.RFC3339))
		}
		if script.LastFinishedAt != nil {
			fmt.Printf("  last: %s exit=%d duration=%dms\n", script.LastFinishedAt.UTC().Format(time.RFC3339), script.LastExitCode, script.LastDurationMs)
		}
		if script.LastError != "" {
			fmt.Printf("  error: %s\n", script.LastError)
		}
		if script.LastLogPath != "" {
			fmt.Printf("  log: %s\n", script.LastLogPath)
		}
	}
	return nil
}

func IssueDoctorCommand(deps *Dependencies, opts IssueDoctorOptions) error {
	restoreProject := applyIssueProjectOverride(deps, opts.Project)
	defer restoreProject()

	ctx, cancel := context.WithTimeout(context.Background(), daemonCommandTimeout)
	defer cancel()
	if err := ensureDaemon(ctx, deps, "cli"); err != nil {
		return err
	}

	task, _, ok, err := loadIssueMetadataTask(ctx, deps, opts.IssueID)
	if err != nil {
		return fmt.Errorf("failed to inspect issue %s: %w", opts.IssueID, err)
	}
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
	if mixedIssueResourceLifecycleHooksConfigured(deps) {
		diagnostics = append(diagnostics, "issueResources config mixes reconcileCommand with one-shot prepare/cleanup hooks; verify lifecycle ownership is intentional")
	}

	if len(diagnostics) == 0 {
		if opts.JSON {
			return printJSON(map[string]any{
				"issue_id":      task.ID,
				"status":        "ok",
				"dependencies":  formatDependencySummary(task.Dependencies),
				"diagnostics":   []string{},
				"diagnostic_ct": 0,
			})
		}
		fmt.Printf("Doctor: OK %s\n", task.ID)
		fmt.Printf("Dependencies: %s\n", formatDependencySummary(task.Dependencies))
		return nil
	}
	if opts.JSON {
		return printJSON(map[string]any{
			"issue_id":      task.ID,
			"status":        "warn",
			"dependencies":  formatDependencySummary(task.Dependencies),
			"diagnostics":   diagnostics,
			"diagnostic_ct": len(diagnostics),
		})
	}
	fmt.Printf("Doctor: WARN %s\n", task.ID)
	for _, diagnostic := range diagnostics {
		fmt.Printf("- %s\n", diagnostic)
	}
	return nil
}

func mixedIssueResourceLifecycleHooksConfigured(deps *Dependencies) bool {
	if deps == nil || deps.Config == nil {
		return false
	}
	resources := deps.Config.IssueResources
	if strings.TrimSpace(resources.ReconcileCommand) == "" {
		return false
	}
	return len(resources.PrepareCommands) > 0 ||
		len(resources.FailedStartCleanupCommands) > 0 ||
		len(resources.CleanupCommands) > 0
}

func IssueCreateCommand(deps *Dependencies, opts IssueCreateOptions) error {
	if isDifferentExplicitIssueProject(deps.ProjectID, opts.Project) {
		opts.AutoParentFromIssueID = nil
		opts.AutoCreatedFromIssueID = nil
	}
	if !isDifferentExplicitIssueProject(deps.ProjectID, opts.Project) {
		if !opts.Deferred && opts.AutoParentFromIssueID == nil {
			if issueID, ok := activeIssueIDFromTmuxPaneIfKnown(context.Background(), deps); ok {
				opts.AutoParentFromIssueID = &issueID
				opts.AutoCreatedFromIssueID = &issueID
			}
		}
		if opts.Deferred && opts.AutoCreatedFromIssueID == nil {
			if issueID, ok := activeIssueIDFromTmuxPaneIfKnown(context.Background(), deps); ok {
				opts.AutoCreatedFromIssueID = &issueID
			}
		}
	}
	if normalizeIssueProject(opts.Project) != "" {
		opts.ProjectQualifiedOutput = true
	}
	restoreProject := applyIssueProjectOverride(deps, opts.Project)
	defer restoreProject()

	result, err := createIssue(context.Background(), deps, opts)
	if err != nil {
		var partial issueCreatePartialError
		if errors.As(err, &partial) && opts.JSON {
			if printErr := printJSON(map[string]any{
				"issue_id":        partial.Result.IssueID,
				"project_id":      partial.Result.ProjectID,
				"parent_id":       partial.Result.ParentID,
				"created_from_id": partial.Result.CreatedFromID,
				"deferred":        partial.Result.Deferred,
				"created":         true,
				"partial_success": true,
				"error":           partial.Err.Error(),
				"message":         partial.Error(),
			}); printErr != nil {
				return printErr
			}
		}
		return err
	}
	if opts.JSON {
		return printJSON(map[string]any{
			"issue_id":        result.IssueID,
			"parent_id":       result.ParentID,
			"created_from_id": result.CreatedFromID,
			"deferred":        result.Deferred,
			"message":         result.Message,
			"created":         true,
			"project_id":      strings.TrimSpace(deps.ProjectID),
		})
	}
	fmt.Println(result.Message)
	return nil
}

func createIssue(parentCtx context.Context, deps *Dependencies, opts IssueCreateOptions) (issueCreateResult, error) {
	if parentCtx == nil {
		parentCtx = context.Background()
	}
	ctx, cancel := context.WithTimeout(parentCtx, issueCreateCommandTimeout)
	defer cancel()

	var err error
	var parentID *naming.IssueID
	implementations := append([]string{}, opts.Implementations...)
	if !opts.Deferred && opts.AutoParentFromIssueID != nil && strings.TrimSpace(*opts.AutoParentFromIssueID) != "" {
		parentIssueID := strings.TrimSpace(*opts.AutoParentFromIssueID)
		parentTask, _, ok, err := loadIssueMetadataTaskWithDaemonAutostartRetry(ctx, deps, parentIssueID)
		if err != nil {
			return issueCreateResult{}, fmt.Errorf("failed to resolve active parent issue %s: %w", parentIssueID, err)
		}
		if !ok {
			return issueCreateResult{}, fmt.Errorf("active issue not found for auto-parenting: %s", parentIssueID)
		}
		parentID = &parentTask.ID
		if len(implementations) == 0 {
			implementations = append([]string{}, parentTask.Implementations...)
		}
	}
	implementations, err = resolveIssueWriteImplementations(ctx, deps, implementations)
	if err != nil {
		return issueCreateResult{}, err
	}

	taskID, err := commandWithDaemonAutostartRetry(ctx, deps, func(callCtx context.Context) (string, error) {
		return deps.DaemonClient.CreateTask(callCtx, daemonclient.TaskCreateParams{
			Title:           opts.Title,
			Description:     opts.Description,
			Type:            opts.Type,
			Priority:        opts.Priority,
			ParentID:        parentID,
			Implementations: dedupeOrderedIDs(implementations),
		})
	})
	if err != nil {
		return issueCreateResult{}, fmt.Errorf("failed to create issue: %w", err)
	}

	createdFromIDValue := ""
	projectIDValue := strings.TrimSpace(deps.ProjectID)
	if opts.AutoCreatedFromIssueID != nil && strings.TrimSpace(*opts.AutoCreatedFromIssueID) != "" {
		createdFromIDValue = strings.TrimSpace(*opts.AutoCreatedFromIssueID)
		createdIssueID, parseErr := naming.ParseIssueID(taskID)
		if parseErr != nil {
			return issueCreateResult{}, fmt.Errorf("failed to parse created issue id %s: %w", formatProjectIssueRef(projectIDValue, taskID), parseErr)
		}
		createdFromID, parseErr := naming.ParseIssueID(createdFromIDValue)
		if parseErr != nil {
			return issueCreateResult{}, fmt.Errorf("failed to parse active issue id for created-from edge %s: %w", formatProjectIssueRef(projectIDValue, createdFromIDValue), parseErr)
		}
		if createdIssueID != createdFromID {
			if _, err := commandWithDaemonAutostartRetry(ctx, deps, func(callCtx context.Context) (struct{}, error) {
				err := deps.DaemonClient.AddTaskDependency(callCtx, daemonclient.TaskDependencyParams{
					TaskID:      createdIssueID,
					DependsOnID: createdFromID,
					Type:        string(domain.DependencyCreatedIn),
				})
				return struct{}{}, err
			}); err != nil {
				partial := issueCreateResult{
					IssueID:       taskID,
					ProjectID:     projectIDValue,
					CreatedFromID: createdFromIDValue,
					Deferred:      opts.Deferred,
				}
				if parentID != nil && strings.TrimSpace(parentID.String()) != "" {
					partial.ParentID = strings.TrimSpace(parentID.String())
				}
				return issueCreateResult{}, issueCreatePartialError{
					Result: partial,
					Err: fmt.Errorf("failed to add created-from issue graph edge %s -> %s: %w",
						formatProjectIssueRef(projectIDValue, taskID),
						formatProjectIssueRef(projectIDValue, createdFromIDValue),
						err,
					),
				}
			}
		}
	}

	displayIssueID := taskID
	if opts.ProjectQualifiedOutput {
		displayIssueID = formatProjectIssueRef(projectIDValue, taskID)
	}
	message := fmt.Sprintf("Created issue: %s", displayIssueID)
	parentIDValue := ""
	if parentID != nil && strings.TrimSpace(parentID.String()) != "" {
		parentIDValue = strings.TrimSpace(parentID.String())
		message = fmt.Sprintf("%s (parent: %s, auto-parent from AZEDARACH_ISSUE_ID)", message, parentIDValue)
	}
	if createdFromIDValue != "" {
		message = fmt.Sprintf("%s [created-from: %s]", message, createdFromIDValue)
	}
	if opts.Deferred {
		message = fmt.Sprintf("%s [deferred: standalone later work, not auto-parented]", message)
	}
	return issueCreateResult{
		IssueID:       taskID,
		ProjectID:     projectIDValue,
		ParentID:      parentIDValue,
		CreatedFromID: createdFromIDValue,
		Deferred:      opts.Deferred,
		Message:       message,
	}, nil
}

func IssueCloseCommand(deps *Dependencies, opts IssueCloseOptions) error {
	restoreProject := applyIssueProjectOverride(deps, opts.Project)
	defer restoreProject()

	ctx, cancel := context.WithTimeout(context.Background(), issueCloseCleanupTimeout)
	defer cancel()
	if err := ensureDaemon(ctx, deps, "cli"); err != nil {
		return err
	}

	result, err := deps.DaemonClient.CloseTask(ctx, opts.IssueID, cleanupCloseTaskStatusOptions(opts.ForceWorktree, opts.CloseCleanChildren))
	if err != nil {
		return fmt.Errorf("failed to close issue %s: %w", opts.IssueID, err)
	}

	if opts.JSON {
		return printJSON(map[string]any{
			"issue_id":                  opts.IssueID,
			"status":                    "closed",
			"updated":                   true,
			"integration_requested":     result.IntegrationRequested,
			"integrated":                result.Integrated,
			"cleanup_performed":         true,
			"worktree_forced":           opts.ForceWorktree,
			"auto_closed_children":      result.AutoClosedChildren,
			"worktree_cleanup_deferred": result.WorktreeCleanupDeferred,
			"phases":                    taskClosePhaseJSON(result.Phases),
		})
	}
	fmt.Printf("Closed issue: %s\n", opts.IssueID)
	if result.IntegrationRequested {
		fmt.Println("- Integration requested")
	}
	if result.Integrated {
		fmt.Printf("- Integrated %s into %s\n", result.IntegratedSourceBranch, result.IntegratedTargetBranch)
	}
	fmt.Println("- Cleanup performed")
	if opts.ForceWorktree {
		fmt.Println("- Worktree removal forced")
	}
	if len(result.AutoClosedChildren) > 0 {
		fmt.Printf("- Closed clean child issues: %s\n", strings.Join(result.AutoClosedChildren, ", "))
	}
	if result.WorktreeCleanupDeferred {
		fmt.Println("- Worktree cleanup deferred")
	}
	printTaskClosePhases(result.Phases)
	return nil
}

func taskClosePhaseJSON(phases []daemonclient.TaskClosePhaseTiming) []map[string]any {
	out := make([]map[string]any, 0, len(phases))
	for _, phase := range phases {
		name := strings.TrimSpace(phase.Name)
		if name == "" {
			continue
		}
		item := map[string]any{
			"name":       name,
			"elapsed_ms": phase.ElapsedMS,
		}
		if phase.Skipped {
			item["skipped"] = true
		}
		out = append(out, item)
	}
	return out
}

func printTaskClosePhases(phases []daemonclient.TaskClosePhaseTiming) {
	if len(phases) == 0 {
		return
	}
	fmt.Println("- Phase timings:")
	for _, phase := range phases {
		name := strings.TrimSpace(phase.Name)
		if name == "" {
			continue
		}
		suffix := ""
		if phase.Skipped {
			suffix = " (skipped)"
		}
		fmt.Printf("  - %s: %s%s\n", name, phase.Elapsed().Round(time.Millisecond), suffix)
	}
}

func IssueDeleteCommand(deps *Dependencies, opts IssueDeleteOptions) error {
	restoreProject := applyIssueProjectOverride(deps, opts.Project)
	defer restoreProject()

	ctx, cancel := context.WithTimeout(context.Background(), daemonCommandTimeout)
	defer cancel()
	if err := ensureDaemon(ctx, deps, "cli"); err != nil {
		return err
	}

	cleanup, err := deleteIssueWithRuntimeCleanup(ctx, deps, opts)
	if err != nil {
		return err
	}
	return printIssueDeleteResult(opts, cleanup)
}

type issueDeleteCleanupResult struct {
	SessionStopped  bool `json:"session_stopped"`
	WorktreeRemoved bool `json:"worktree_removed"`
	WorktreeForced  bool `json:"worktree_forced"`
}

func deleteIssueWithRuntimeCleanup(ctx context.Context, deps *Dependencies, opts IssueDeleteOptions) (issueDeleteCleanupResult, error) {
	result, err := deps.DaemonClient.DeleteTaskWithOptions(ctx, opts.IssueID, daemonclient.TaskDeleteOptions{
		Cleanup:        opts.Cleanup,
		StopSession:    opts.StopSession,
		RemoveWorktree: opts.RemoveWorktree,
		ForceWorktree:  opts.ForceWorktree,
	})
	if err != nil {
		return issueDeleteCleanupResult{}, fmt.Errorf("failed to delete issue %s after cleanup: %w", opts.IssueID, err)
	}
	return issueDeleteCleanupResult{
		SessionStopped:  result.SessionStopped,
		WorktreeRemoved: result.WorktreeRemoved,
		WorktreeForced:  result.WorktreeForced,
	}, nil
}

func printIssueDeleteResult(opts IssueDeleteOptions, cleanup issueDeleteCleanupResult) error {
	if opts.JSON {
		return printJSON(map[string]any{
			"issue_id":         opts.IssueID,
			"deleted":          true,
			"session_stopped":  cleanup.SessionStopped,
			"worktree_removed": cleanup.WorktreeRemoved,
			"worktree_forced":  cleanup.WorktreeForced,
		})
	}
	fmt.Printf("Deleted issue: %s\n", opts.IssueID)
	if cleanup.SessionStopped {
		fmt.Println("- Session stopped")
	}
	if cleanup.WorktreeRemoved {
		fmt.Println("- Worktree removed")
		if cleanup.WorktreeForced {
			fmt.Println("- Worktree removal forced")
		}
	}
	return nil
}

func IssueUpdateCommand(deps *Dependencies, opts IssueUpdateOptions) error {
	restoreProject := applyIssueProjectOverride(deps, opts.Project)
	defer restoreProject()

	ctx, cancel := context.WithTimeout(context.Background(), daemonCommandTimeout)
	defer cancel()
	if err := ensureDaemon(ctx, deps, "cli"); err != nil {
		return err
	}

	task, err := loadIssueDetailTask(ctx, deps, opts.IssueID)
	if err != nil {
		return fmt.Errorf("failed to load issue %s for update: %w", opts.IssueID, err)
	}

	update := daemonclient.TaskUpdateParams{
		Title:       task.Title,
		Description: task.Description,
		Notes:       opts.Notes,
		Type:        task.Type,
		Priority:    task.Priority,
	}
	if opts.Title != "" {
		update.Title = opts.Title
	}
	if opts.DescriptionSet {
		update.Description = opts.Description
	}
	if opts.Type != nil {
		update.Type = *opts.Type
	}
	if opts.Priority != nil {
		update.Priority = *opts.Priority
	}
	if len(opts.UpdateImpls) > 0 {
		impls, err := resolveIssueWriteImplementations(ctx, deps, opts.UpdateImpls)
		if err != nil {
			return fmt.Errorf("invalid implementation update: %w", err)
		}
		update.Implementations = impls
	}

	if err := deps.DaemonClient.UpdateTaskDetails(ctx, opts.IssueID, update); err != nil {
		return fmt.Errorf("failed to update issue %s: %w", opts.IssueID, err)
	}
	if opts.Status != nil {
		statusOptions := daemonclient.TaskStatusOptions{}
		if *opts.Status == domain.StatusDone {
			statusOptions = cleanupCloseTaskStatusOptions(opts.ForceWorktree)
		}
		if *opts.Status == domain.StatusInReview {
			statusOptions.CascadeChildren = opts.CascadeChildren
		}
		if err := deps.DaemonClient.UpdateTaskStatusWithOptions(ctx, opts.IssueID, *opts.Status, statusOptions); err != nil {
			return fmt.Errorf("failed to set status for issue %s: %w", opts.IssueID, err)
		}
	}
	if opts.AppendNotes != "" {
		if err := deps.DaemonClient.AppendTaskNotes(ctx, opts.IssueID, opts.AppendNotes); err != nil {
			return fmt.Errorf("failed to append notes for issue %s: %w", opts.IssueID, err)
		}
	}
	if opts.JSON {
		return printJSON(map[string]any{
			"issue_id":       opts.IssueID,
			"updated":        true,
			"status_set":     opts.Status != nil,
			"notes_replaced": opts.Notes != nil,
			"notes_appended": opts.AppendNotes != "",
		})
	}
	fmt.Printf("Updated issue: %s\n", opts.IssueID)
	return nil
}

func cleanupCloseTaskStatusOptions(forceWorktree bool, closeCleanChildren ...bool) daemonclient.TaskStatusOptions {
	autoCloseChildren := false
	if len(closeCleanChildren) > 0 {
		autoCloseChildren = closeCleanChildren[0]
	}
	return daemonclient.TaskStatusOptions{
		ForceWorktree:        forceWorktree,
		IntegrateBeforeClose: true,
		CloseCleanChildren:   autoCloseChildren,
	}
}

func IssueDependencyAddCommand(deps *Dependencies, opts IssueDependencyAddOptions) error {
	restoreProject := applyIssueProjectOverride(deps, opts.Project)
	defer restoreProject()

	ctx, cancel := context.WithTimeout(context.Background(), daemonCommandTimeout)
	defer cancel()
	if err := ensureDaemon(ctx, deps, "cli"); err != nil {
		return err
	}

	typedIssueID, err := naming.ParseIssueID(strings.TrimSpace(opts.IssueID))
	if err != nil {
		return fmt.Errorf("invalid issue_id %q: %w", opts.IssueID, err)
	}
	typedDependsOnID, err := naming.ParseIssueID(strings.TrimSpace(opts.DependsOnID))
	if err != nil {
		return fmt.Errorf("invalid depends_on_id %q: %w", opts.DependsOnID, err)
	}
	if err := deps.DaemonClient.AddTaskDependency(ctx, daemonclient.TaskDependencyParams{
		TaskID:             typedIssueID,
		DependsOnID:        typedDependsOnID,
		Type:               opts.Type,
		ForceParentChange:  opts.ForceParentChange,
		IssueProjectID:     opts.IssueProjectID,
		DependsOnProjectID: opts.DependsOnProjectID,
	}); err != nil {
		return fmt.Errorf("%s: %w", parentChangeGuidance(opts), err)
	}
	if opts.JSON {
		return printJSON(map[string]any{
			"action":              "add",
			"issue_id":            opts.IssueID,
			"depends_on_id":       opts.DependsOnID,
			"dependency_type":     opts.Type,
			"force_parent_change": opts.ForceParentChange,
			"updated":             true,
		})
	}
	fmt.Printf("Added dependency: %s --(%s)--> %s\n", opts.IssueID, opts.Type, opts.DependsOnID)
	if opts.Type == string(domain.DependencyParentChild) {
		fmt.Printf("This makes %s a child of %s.\n", opts.IssueID, opts.DependsOnID)
	}
	return nil
}

func parentChangeGuidance(opts IssueDependencyAddOptions) string {
	if opts.Type != string(domain.DependencyParentChild) {
		return fmt.Sprintf("failed to add dependency %s -> %s", opts.IssueID, opts.DependsOnID)
	}
	return fmt.Sprintf("failed to add parent-child edge. This would make %s a child of %s. If you meant to make %s a child of %s, run: az issue dep add %s %s --type parent-child. Use --force-parent-change to intentionally move %s to %s", opts.IssueID, opts.DependsOnID, opts.DependsOnID, opts.IssueID, opts.DependsOnID, opts.IssueID, opts.IssueID, opts.DependsOnID)
}

func IssueDependencyRemoveCommand(deps *Dependencies, opts IssueDependencyRemoveOptions) error {
	restoreProject := applyIssueProjectOverride(deps, opts.Project)
	defer restoreProject()

	ctx, cancel := context.WithTimeout(context.Background(), daemonCommandTimeout)
	defer cancel()
	if err := ensureDaemon(ctx, deps, "cli"); err != nil {
		return err
	}

	typedIssueID, err := naming.ParseIssueID(strings.TrimSpace(opts.IssueID))
	if err != nil {
		return fmt.Errorf("invalid issue_id %q: %w", opts.IssueID, err)
	}
	typedDependsOnID, err := naming.ParseIssueID(strings.TrimSpace(opts.DependsOnID))
	if err != nil {
		return fmt.Errorf("invalid depends_on_id %q: %w", opts.DependsOnID, err)
	}
	if err := deps.DaemonClient.RemoveTaskDependency(ctx, daemonclient.TaskDependencyRemoveParams{
		TaskID:              typedIssueID,
		DependsOnID:         typedDependsOnID,
		Type:                opts.Type,
		Confirm:             opts.Confirm,
		ConfirmParentOrphan: opts.ConfirmParentOrphan,
	}); err != nil {
		return fmt.Errorf("failed to remove dependency %s -> %s: %w", opts.IssueID, opts.DependsOnID, err)
	}
	if opts.JSON {
		return printJSON(map[string]any{
			"action":          "remove",
			"issue_id":        opts.IssueID,
			"depends_on_id":   opts.DependsOnID,
			"dependency_type": opts.Type,
			"updated":         true,
		})
	}
	fmt.Printf("Removed dependency: %s --(%s)--> %s\n", opts.IssueID, opts.Type, opts.DependsOnID)
	return nil
}

type dependencyBulkMutationPayload struct {
	Mutations []dependencyBulkMutation `json:"mutations"`
}

type dependencyBulkMutation struct {
	Action      string `json:"action"`
	IssueID     string `json:"issue_id,omitempty"`
	ID          string `json:"id,omitempty"`
	DependsOnID string `json:"depends_on_id,omitempty"`
	FromID      string `json:"from_id,omitempty"`
	ToID        string `json:"to_id,omitempty"`
	Type        string `json:"type,omitempty"`
}

type dependencyBulkOutcome struct {
	Index      int    `json:"index"`
	Action     string `json:"action"`
	IssueID    string `json:"issue_id"`
	Dependency string `json:"dependency,omitempty"`
	FromID     string `json:"from_id,omitempty"`
	ToID       string `json:"to_id,omitempty"`
	Type       string `json:"type"`
	Status     string `json:"status"`
	Reason     string `json:"reason,omitempty"`
}

type dependencyBulkSummary struct {
	Requested int `json:"requested"`
	Planned   int `json:"planned"`
	NoOp      int `json:"no_op"`
	Invalid   int `json:"invalid"`
	Applied   int `json:"applied"`
}

type dependencyBulkResult struct {
	DryRun   bool                    `json:"dry_run"`
	Summary  dependencyBulkSummary   `json:"summary"`
	Outcomes []dependencyBulkOutcome `json:"outcomes"`
}

func IssueDependencyBulkApplyCommand(deps *Dependencies, opts IssueDependencyBulkApplyOptions) error {
	restoreProject := applyIssueProjectOverride(deps, opts.Project)
	defer restoreProject()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := ensureDaemon(ctx, deps, "cli"); err != nil {
		return err
	}

	inputBytes, err := os.ReadFile(opts.InputPath)
	if err != nil {
		return fmt.Errorf("read dependency bulk input %s: %w", opts.InputPath, err)
	}
	var payload dependencyBulkMutationPayload
	if err := json.Unmarshal(inputBytes, &payload); err != nil {
		return fmt.Errorf("parse dependency bulk input %s: %w", opts.InputPath, err)
	}
	if len(payload.Mutations) == 0 {
		return fmt.Errorf("dependency bulk input must contain at least one mutation")
	}

	snapshot, err := listTasksSnapshotForCLI(ctx, deps)
	if err != nil {
		return fmt.Errorf("load issues for dependency bulk apply: %w", err)
	}
	taskEdges := buildTaskDependencyEdgeSet(snapshot.Tasks)
	taskIDs := make(map[naming.IssueID]struct{}, len(snapshot.Tasks))
	for _, task := range snapshot.Tasks {
		taskIDs[task.ID] = struct{}{}
	}

	ops := make([]protocol.ApplyOperationBody, 0, len(payload.Mutations)*2)
	outcomes := make([]dependencyBulkOutcome, 0, len(payload.Mutations))
	summary := dependencyBulkSummary{Requested: len(payload.Mutations)}

	for i, mutation := range payload.Mutations {
		outcome, plannedOps, err := planDependencyMutation(i, mutation, taskIDs, taskEdges)
		if err != nil {
			outcome.Status = "invalid"
			outcome.Reason = err.Error()
			summary.Invalid++
			outcomes = append(outcomes, outcome)
			continue
		}
		switch outcome.Status {
		case "planned":
			summary.Planned++
			ops = append(ops, plannedOps...)
		case "no-op":
			summary.NoOp++
		}
		outcomes = append(outcomes, outcome)
	}

	if !opts.DryRun && len(ops) > 0 {
		if err := executeBulkApply(deps, false, opts.JSON, ops); err != nil {
			return err
		}
		summary.Applied = summary.Planned
	}

	result := dependencyBulkResult{
		DryRun:   opts.DryRun,
		Summary:  summary,
		Outcomes: outcomes,
	}
	if opts.JSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(result)
	}

	for _, outcome := range outcomes {
		switch outcome.Status {
		case "planned":
			fmt.Printf("[%d] planned %s %s (%s)\n", outcome.Index, outcome.Action, outcome.IssueID, outcome.Type)
		case "no-op":
			fmt.Printf("[%d] no-op %s %s (%s): %s\n", outcome.Index, outcome.Action, outcome.IssueID, outcome.Type, outcome.Reason)
		default:
			fmt.Printf("[%d] invalid %s %s (%s): %s\n", outcome.Index, outcome.Action, outcome.IssueID, outcome.Type, outcome.Reason)
		}
	}
	fmt.Printf("Summary: requested=%d planned=%d no-op=%d invalid=%d applied=%d\n",
		summary.Requested,
		summary.Planned,
		summary.NoOp,
		summary.Invalid,
		summary.Applied,
	)
	return nil
}

func IssueImageAddCommand(deps *Dependencies, opts IssueImageAddOptions) error {
	restoreProject := applyIssueProjectOverride(deps, opts.Project)
	defer restoreProject()

	ctx, cancel := context.WithTimeout(context.Background(), daemonCommandTimeout)
	defer cancel()
	if err := ensureDaemon(ctx, deps, "cli"); err != nil {
		return err
	}

	_, _, ok, err := loadIssueMetadataTask(ctx, deps, opts.IssueID)
	if err != nil {
		return fmt.Errorf("failed to load issue %s for image attach: %w", opts.IssueID, err)
	}
	if !ok {
		return fmt.Errorf("issue not found: %s", opts.IssueID)
	}

	logger := deps.Logger
	if logger == nil {
		logger = slog.Default()
	}
	service := attachment.NewService(filepath.Join(deps.RepoDir, ".azedarach"), logger)
	attached, err := service.Attach(ctx, opts.IssueID, opts.SourcePath)
	if err != nil {
		return fmt.Errorf("failed to attach image %q to issue %s: %w", opts.SourcePath, opts.IssueID, err)
	}

	notesAppended := false
	if line := formatIssueAttachmentNoteLine(attached); strings.TrimSpace(line) != "" {
		if err := deps.DaemonClient.AppendTaskNotes(ctx, opts.IssueID, line); err != nil {
			return fmt.Errorf("image attached but failed to append notes for issue %s: %w", opts.IssueID, err)
		}
		notesAppended = true
	}

	if opts.JSON {
		return printJSON(map[string]any{
			"issue_id":       opts.IssueID,
			"attachment_id":  attached.ID,
			"filename":       attached.Filename,
			"path":           attached.Path,
			"notes_appended": notesAppended,
			"updated":        true,
		})
	}
	fmt.Printf("Attached image to issue %s: %s (attachment_id=%s)\n", opts.IssueID, attached.Filename, attached.ID)
	return nil
}

func IssueImageRemoveCommand(deps *Dependencies, opts IssueImageRemoveOptions) error {
	restoreProject := applyIssueProjectOverride(deps, opts.Project)
	defer restoreProject()

	ctx, cancel := context.WithTimeout(context.Background(), daemonCommandTimeout)
	defer cancel()
	if err := ensureDaemon(ctx, deps, "cli"); err != nil {
		return err
	}

	_, _, ok, err := loadIssueMetadataTask(ctx, deps, opts.IssueID)
	if err != nil {
		return fmt.Errorf("failed to load issue %s for image removal: %w", opts.IssueID, err)
	}
	if !ok {
		return fmt.Errorf("issue not found: %s", opts.IssueID)
	}

	logger := deps.Logger
	if logger == nil {
		logger = slog.Default()
	}
	service := attachment.NewService(filepath.Join(deps.RepoDir, ".azedarach"), logger)
	if err := service.Delete(ctx, opts.IssueID, opts.AttachmentID); err != nil {
		return fmt.Errorf("failed to remove attachment %s from issue %s: %w", opts.AttachmentID, opts.IssueID, err)
	}

	if opts.JSON {
		return printJSON(map[string]any{
			"issue_id":      opts.IssueID,
			"attachment_id": opts.AttachmentID,
			"removed":       true,
		})
	}
	fmt.Printf("Removed image attachment %s from issue %s\n", opts.AttachmentID, opts.IssueID)
	return nil
}

func IssueDocumentAddCommand(deps *Dependencies, opts IssueDocumentAddOptions) error {
	restoreProject := applyIssueProjectOverride(deps, opts.Project)
	defer restoreProject()

	ctx, cancel := context.WithTimeout(context.Background(), daemonCommandTimeout)
	defer cancel()
	if err := ensureDaemon(ctx, deps, "cli"); err != nil {
		return err
	}

	_, _, ok, err := loadIssueMetadataTask(ctx, deps, opts.IssueID)
	if err != nil {
		return fmt.Errorf("failed to load issue %s for document attach: %w", opts.IssueID, err)
	}
	if !ok {
		return fmt.Errorf("issue not found: %s", opts.IssueID)
	}

	logger := deps.Logger
	if logger == nil {
		logger = slog.Default()
	}
	service := attachment.NewDocumentService(filepath.Join(deps.RepoDir, ".azedarach"), logger)
	attached, err := service.Attach(ctx, opts.IssueID, opts.SourcePath)
	if err != nil {
		return fmt.Errorf("failed to attach document %q to issue %s: %w", opts.SourcePath, opts.IssueID, err)
	}

	notesAppended := false
	if line := formatIssueAttachmentNoteLine(attached); strings.TrimSpace(line) != "" {
		if err := deps.DaemonClient.AppendTaskNotes(ctx, opts.IssueID, line); err != nil {
			return fmt.Errorf("document attached but failed to append notes for issue %s: %w", opts.IssueID, err)
		}
		notesAppended = true
	}

	if opts.JSON {
		return printJSON(map[string]any{
			"issue_id":       opts.IssueID,
			"attachment_id":  attached.ID,
			"filename":       attached.Filename,
			"path":           attached.Path,
			"notes_appended": notesAppended,
			"updated":        true,
		})
	}
	fmt.Printf("Attached document to issue %s: %s (attachment_id=%s)\n", opts.IssueID, attached.Filename, attached.ID)
	return nil
}

func IssueDocumentListCommand(deps *Dependencies, opts IssueDocumentListOptions) error {
	restoreProject := applyIssueProjectOverride(deps, opts.Project)
	defer restoreProject()

	ctx, cancel := context.WithTimeout(context.Background(), daemonCommandTimeout)
	defer cancel()
	if err := ensureDaemon(ctx, deps, "cli"); err != nil {
		return err
	}

	_, _, ok, err := loadIssueMetadataTask(ctx, deps, opts.IssueID)
	if err != nil {
		return fmt.Errorf("failed to load issue %s for document list: %w", opts.IssueID, err)
	}
	if !ok {
		return fmt.Errorf("issue not found: %s", opts.IssueID)
	}

	logger := deps.Logger
	if logger == nil {
		logger = slog.Default()
	}
	service := attachment.NewDocumentService(filepath.Join(deps.RepoDir, ".azedarach"), logger)
	attachments, err := service.List(ctx, opts.IssueID)
	if err != nil {
		return fmt.Errorf("failed to list document attachments for issue %s: %w", opts.IssueID, err)
	}

	if opts.JSON {
		return printJSON(map[string]any{
			"issue_id":    opts.IssueID,
			"attachments": attachments,
			"count":       len(attachments),
		})
	}
	if len(attachments) == 0 {
		fmt.Printf("No document attachments for issue %s\n", opts.IssueID)
		return nil
	}
	fmt.Printf("Document attachments for issue %s:\n", opts.IssueID)
	for _, att := range attachments {
		relativePath := strings.TrimSpace(att.Relative)
		if relativePath == "" {
			relativePath = filepath.ToSlash(filepath.Join(".azedarach", "attachments", att.Filename))
		}
		mimeType := strings.TrimSpace(att.MimeType)
		if mimeType == "" {
			mimeType = "application/octet-stream"
		}
		fmt.Printf("- %s  %s  %s  %d bytes  %s\n", att.ID, att.Filename, mimeType, att.Size, relativePath)
	}
	return nil
}

func IssueDocumentRemoveCommand(deps *Dependencies, opts IssueDocumentRemoveOptions) error {
	restoreProject := applyIssueProjectOverride(deps, opts.Project)
	defer restoreProject()

	ctx, cancel := context.WithTimeout(context.Background(), daemonCommandTimeout)
	defer cancel()
	if err := ensureDaemon(ctx, deps, "cli"); err != nil {
		return err
	}

	_, _, ok, err := loadIssueMetadataTask(ctx, deps, opts.IssueID)
	if err != nil {
		return fmt.Errorf("failed to load issue %s for document removal: %w", opts.IssueID, err)
	}
	if !ok {
		return fmt.Errorf("issue not found: %s", opts.IssueID)
	}

	logger := deps.Logger
	if logger == nil {
		logger = slog.Default()
	}
	service := attachment.NewDocumentService(filepath.Join(deps.RepoDir, ".azedarach"), logger)
	if err := service.Delete(ctx, opts.IssueID, opts.AttachmentID); err != nil {
		return fmt.Errorf("failed to remove attachment %s from issue %s: %w", opts.AttachmentID, opts.IssueID, err)
	}

	if opts.JSON {
		return printJSON(map[string]any{
			"issue_id":      opts.IssueID,
			"attachment_id": opts.AttachmentID,
			"removed":       true,
		})
	}
	fmt.Printf("Removed document attachment %s from issue %s\n", opts.AttachmentID, opts.IssueID)
	return nil
}

func formatIssueAttachmentNoteLine(att *attachment.Attachment) string {
	if att == nil {
		return ""
	}
	issueID := strings.TrimSpace(att.IssueID)
	filename := strings.TrimSpace(att.Filename)
	if issueID == "" || filename == "" {
		return ""
	}
	relativePath := strings.TrimSpace(att.Relative)
	if relativePath == "" {
		relativePath = filepath.ToSlash(filepath.Join(".azedarach", "attachments", filename))
	}
	source := "file"
	if strings.HasPrefix(strings.ToLower(filename), "clipboard-") {
		source = "clipboard"
	}
	timestamp := att.Created.Local().Format("2006-01-02 15:04:05")
	return fmt.Sprintf("📎 [%s](%s) (%s, %s)", filename, relativePath, source, timestamp)
}

func IssueBulkCreateCommand(deps *Dependencies, opts IssueBulkCreateOptions) error {
	restoreProject := applyIssueProjectOverride(deps, opts.Project)
	defer restoreProject()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := ensureDaemon(ctx, deps, "cli"); err != nil {
		return err
	}
	impl, err := resolveIssueWriteImplementation(ctx, deps, opts.Implementation)
	if err != nil {
		return err
	}

	inputBytes, err := os.ReadFile(opts.InputPath)
	if err != nil {
		return fmt.Errorf("read bulk-create input %s: %w", opts.InputPath, err)
	}
	var input []issueBulkCreateInputItem
	if err := json.Unmarshal(inputBytes, &input); err != nil {
		return fmt.Errorf("parse bulk-create input %s: %w", opts.InputPath, err)
	}
	if len(input) == 0 {
		return fmt.Errorf("bulk-create input must contain at least one item")
	}

	ops := make([]protocol.ApplyOperationBody, 0, countBulkCreateItems(input))
	seenRefs := map[string]string{}
	for i, item := range input {
		compiled, err := compileBulkCreateItem(item, fmt.Sprintf("bulk-create item %d", i), impl, "", seenRefs)
		if err != nil {
			return err
		}
		ops = append(ops, compiled...)
	}

	return executeBulkApply(deps, opts.DryRun, opts.JSON, ops)
}

func countBulkCreateItems(items []issueBulkCreateInputItem) int {
	count := 0
	for _, item := range items {
		count++
		count += countBulkCreateItems(item.Children)
	}
	return count
}

func compileBulkCreateItem(item issueBulkCreateInputItem, path, impl, parentRef string, seenRefs map[string]string) ([]protocol.ApplyOperationBody, error) {
	taskType, err := parseTaskType(item.Type)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	priority, err := parsePriority(item.Priority)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	ref := strings.TrimSpace(item.Ref)
	if len(item.Children) > 0 && ref == "" {
		ref = strings.ReplaceAll(path, ".", "_")
		ref = strings.ReplaceAll(ref, "[", "_")
		ref = strings.ReplaceAll(ref, "]", "")
		ref = strings.ReplaceAll(ref, " ", "_")
	}
	if ref != "" {
		if previousPath, ok := seenRefs[ref]; ok {
			return nil, fmt.Errorf("%s: duplicate ref %q already used by %s", path, ref, previousPath)
		}
		seenRefs[ref] = path
	}
	effectiveParentRef := strings.TrimSpace(parentRef)
	parsedParentID, err := parseBulkCreateParentID(item.ParentID)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if item.ParentID != nil {
		effectiveParentRef = ""
	}
	body, err := json.Marshal(struct {
		Title           string          `json:"title"`
		Description     string          `json:"description"`
		Type            domain.TaskType `json:"type"`
		Priority        string          `json:"priority"`
		Implementations []string        `json:"implementations,omitempty"`
		ParentID        *naming.IssueID `json:"parent_id,omitempty"`
		Ref             string          `json:"ref,omitempty"`
		ParentRef       string          `json:"parent_ref,omitempty"`
	}{
		Title:           item.Title,
		Description:     item.Description,
		Type:            taskType,
		Priority:        priority.String(),
		Implementations: []string{impl},
		ParentID:        parsedParentID,
		Ref:             ref,
		ParentRef:       effectiveParentRef,
	})
	if err != nil {
		return nil, fmt.Errorf("marshal %s: %w", path, err)
	}
	ops := []protocol.ApplyOperationBody{{
		Command: daemonclient.CommandTaskCreate,
		Body:    body,
	}}
	childParentRef := ref
	if childParentRef == "" && effectiveParentRef != "" {
		childParentRef = effectiveParentRef
	}
	for i, child := range item.Children {
		childOps, err := compileBulkCreateItem(child, fmt.Sprintf("%s.children[%d]", path, i), impl, childParentRef, seenRefs)
		if err != nil {
			return nil, err
		}
		ops = append(ops, childOps...)
	}
	return ops, nil
}

func parseBulkCreateParentID(value *string) (*naming.IssueID, error) {
	if value == nil {
		return nil, nil
	}
	trimmed := strings.TrimSpace(*value)
	if trimmed == "" {
		return nil, fmt.Errorf("parent_id cannot be empty")
	}
	typed, err := naming.ParseIssueID(trimmed)
	if err != nil {
		return nil, fmt.Errorf("invalid parent_id %q: %w", trimmed, err)
	}
	return &typed, nil
}

func IssueBulkUpdateCommand(deps *Dependencies, opts IssueBulkUpdateOptions) error {
	restoreProject := applyIssueProjectOverride(deps, opts.Project)
	defer restoreProject()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := ensureDaemon(ctx, deps, "cli"); err != nil {
		return err
	}
	if _, err := resolveIssueWriteImplementation(ctx, deps, opts.Implementation); err != nil {
		return err
	}

	inputBytes, err := os.ReadFile(opts.InputPath)
	if err != nil {
		return fmt.Errorf("read bulk-update input %s: %w", opts.InputPath, err)
	}
	var input []struct {
		TaskID              string `json:"task_id,omitempty"`
		ID                  string `json:"id,omitempty"`
		Title               string `json:"title,omitempty"`
		Description         string `json:"description,omitempty"`
		Type                string `json:"type,omitempty"`
		Priority            string `json:"priority,omitempty"`
		DependencyRetargets []struct {
			FromID string `json:"from_id"`
			ToID   string `json:"to_id"`
			Type   string `json:"type"`
		} `json:"dependency_retargets,omitempty"`
	}
	if err := json.Unmarshal(inputBytes, &input); err != nil {
		return fmt.Errorf("parse bulk-update input %s: %w", opts.InputPath, err)
	}
	if len(input) == 0 {
		return fmt.Errorf("bulk-update input must contain at least one item")
	}

	snapshot, err := listTasksSnapshotForCLI(ctx, deps)
	if err != nil {
		return fmt.Errorf("load issues for bulk-update: %w", err)
	}
	tasksByID := map[naming.IssueID]domain.Task{}
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
		typedTaskID, parseErr := naming.ParseIssueID(taskID)
		if parseErr != nil {
			return fmt.Errorf("bulk-update item %d: invalid task_id %q: %w", i, taskID, parseErr)
		}
		if _, ok := tasksByID[typedTaskID]; !ok {
			return fmt.Errorf("bulk-update item %d: issue not found: %s", i, taskID)
		}

		var update daemonclient.TaskUpdateParams
		needsUpdate := false
		if item.Title != "" {
			needsUpdate = true
		}
		if item.Description != "" {
			needsUpdate = true
		}
		if item.Type != "" {
			if _, err := parseTaskType(item.Type); err != nil {
				return fmt.Errorf("bulk-update item %d: %w", i, err)
			}
			needsUpdate = true
		}
		if item.Priority != "" {
			if _, err := parsePriority(item.Priority); err != nil {
				return fmt.Errorf("bulk-update item %d: %w", i, err)
			}
			needsUpdate = true
		}
		if needsUpdate {
			fullTask, err := loadIssueDetailTask(ctx, deps, typedTaskID.String())
			if err != nil {
				return fmt.Errorf("bulk-update item %d: load full issue detail: %w", i, err)
			}
			update = daemonclient.TaskUpdateParams{
				Title:       fullTask.Title,
				Description: fullTask.Description,
				Type:        fullTask.Type,
				Priority:    fullTask.Priority,
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
				TaskID      naming.IssueID  `json:"task_id"`
				Title       string          `json:"title"`
				Description string          `json:"description"`
				Notes       *string         `json:"notes,omitempty"`
				Type        domain.TaskType `json:"type"`
				Priority    string          `json:"priority"`
			}{
				TaskID:      typedTaskID,
				Title:       update.Title,
				Description: update.Description,
				Notes:       update.Notes,
				Type:        update.Type,
				Priority:    update.Priority.String(),
			})
			if err != nil {
				return fmt.Errorf("marshal bulk-update item %d: %w", i, err)
			}
			ops = append(ops, protocol.ApplyOperationBody{
				Command: daemonclient.CommandTaskUpdate,
				Body:    body,
			})
		}

		for _, retarget := range item.DependencyRetargets {
			depType := strings.TrimSpace(retarget.Type)
			if depType == "" {
				depType = string(domain.DependencyBlocks)
			}
			fromID := strings.TrimSpace(retarget.FromID)
			toID := strings.TrimSpace(retarget.ToID)
			if fromID == "" || toID == "" {
				return fmt.Errorf("bulk-update item %d: dependency_retargets requires from_id and to_id", i)
			}
			typedFromID, fromErr := naming.ParseIssueID(fromID)
			if fromErr != nil {
				return fmt.Errorf("bulk-update item %d: invalid from_id %q: %w", i, fromID, fromErr)
			}
			typedToID, toErr := naming.ParseIssueID(toID)
			if toErr != nil {
				return fmt.Errorf("bulk-update item %d: invalid to_id %q: %w", i, toID, toErr)
			}
			removeBody, err := json.Marshal(daemonclient.TaskDependencyRemoveParams{
				TaskID:      typedTaskID,
				DependsOnID: typedFromID,
				Type:        depType,
				Confirm:     true,
			})
			if err != nil {
				return fmt.Errorf("marshal bulk-update item %d dependency retarget remove: %w", i, err)
			}
			ops = append(ops, protocol.ApplyOperationBody{
				Command: daemonclient.CommandTaskDependencyRemove,
				Body:    removeBody,
			})

			addBody, err := json.Marshal(daemonclient.TaskDependencyParams{
				TaskID:      typedTaskID,
				DependsOnID: typedToID,
				Type:        depType,
			})
			if err != nil {
				return fmt.Errorf("marshal bulk-update item %d dependency retarget add: %w", i, err)
			}
			ops = append(ops, protocol.ApplyOperationBody{
				Command: daemonclient.CommandTaskDependencyAdd,
				Body:    addBody,
			})
		}
	}
	if len(ops) == 0 {
		return fmt.Errorf("bulk-update input produced no operations")
	}

	return executeBulkApply(deps, opts.DryRun, opts.JSON, ops)
}

func findTaskByID(tasks []domain.Task, id string) (domain.Task, bool) {
	for _, task := range tasks {
		if task.ID.String() == id {
			return task, true
		}
	}
	return domain.Task{}, false
}

func filterTasksByIDs(tasks []domain.Task, ids []string) []domain.Task {
	if len(ids) == 0 {
		return tasks
	}
	idSet := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		idSet[id] = struct{}{}
	}
	filtered := make([]domain.Task, 0, len(ids))
	for _, task := range tasks {
		if _, ok := idSet[task.ID.String()]; ok {
			filtered = append(filtered, task)
		}
	}
	return filtered
}

func filterTasksByStatus(tasks []domain.Task, statuses []domain.Status) []domain.Task {
	if len(statuses) == 0 {
		return tasks
	}
	statusSet := make(map[domain.Status]struct{}, len(statuses))
	for _, status := range statuses {
		statusSet[status] = struct{}{}
	}
	filtered := make([]domain.Task, 0, len(tasks))
	for _, task := range tasks {
		if _, ok := statusSet[task.Status]; ok {
			filtered = append(filtered, task)
		}
	}
	return filtered
}

func filterTasksByParentIDs(tasks []domain.Task, parentIDs []string) []domain.Task {
	if len(parentIDs) == 0 {
		return tasks
	}
	allow := make(map[string]struct{}, len(parentIDs))
	for _, id := range parentIDs {
		allow[strings.TrimSpace(id)] = struct{}{}
	}
	filtered := make([]domain.Task, 0, len(tasks))
	for _, task := range tasks {
		if task.ParentID == nil {
			continue
		}
		if _, ok := allow[strings.TrimSpace(task.ParentID.String())]; ok {
			filtered = append(filtered, task)
		}
	}
	return filtered
}

func filterTasksByDependencyIDs(tasks []domain.Task, dependencyIDs []string) []domain.Task {
	if len(dependencyIDs) == 0 {
		return tasks
	}
	allow := make(map[string]struct{}, len(dependencyIDs))
	for _, id := range dependencyIDs {
		allow[strings.TrimSpace(id)] = struct{}{}
	}
	filtered := make([]domain.Task, 0, len(tasks))
	for _, task := range tasks {
		matched := false
		for _, dep := range task.Dependencies {
			if _, ok := allow[strings.TrimSpace(dep.ID.String())]; ok {
				matched = true
				break
			}
		}
		if matched {
			filtered = append(filtered, task)
		}
	}
	return filtered
}

func filterTasksByTimeRange(tasks []domain.Task, createdAfter, createdBefore, updatedAfter, updatedBefore *time.Time) []domain.Task {
	if createdAfter == nil && createdBefore == nil && updatedAfter == nil && updatedBefore == nil {
		return tasks
	}
	filtered := make([]domain.Task, 0, len(tasks))
	for _, task := range tasks {
		if timeBeforeFilter(task.CreatedAt, createdAfter) || timeAfterFilter(task.CreatedAt, createdBefore) {
			continue
		}
		if timeBeforeFilter(task.UpdatedAt, updatedAfter) || timeAfterFilter(task.UpdatedAt, updatedBefore) {
			continue
		}
		filtered = append(filtered, task)
	}
	return filtered
}

func timeBeforeFilter(value time.Time, boundary *time.Time) bool {
	return boundary != nil && value.Before(*boundary)
}

func timeAfterFilter(value time.Time, boundary *time.Time) bool {
	return boundary != nil && value.After(*boundary)
}

func dedupeOrderedIDs(ids []string) []string {
	if len(ids) == 0 {
		return nil
	}
	deduped := make([]string, 0, len(ids))
	seen := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		deduped = append(deduped, id)
	}
	return deduped
}

func dedupeTrimmed(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	deduped := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			continue
		}
		if _, ok := seen[trimmed]; ok {
			continue
		}
		seen[trimmed] = struct{}{}
		deduped = append(deduped, trimmed)
	}
	return deduped
}

func collectImplementations(tasks []domain.Task) []string {
	if len(tasks) == 0 {
		return nil
	}
	seen := make(map[string]struct{})
	impls := make([]string, 0, 4)
	for _, task := range tasks {
		for _, impl := range task.Implementations {
			trimmed := strings.TrimSpace(impl)
			if trimmed == "" {
				continue
			}
			if _, ok := seen[trimmed]; ok {
				continue
			}
			seen[trimmed] = struct{}{}
			impls = append(impls, trimmed)
		}
	}
	sort.Strings(impls)
	return impls
}

func parseOptionalIssueID(value *string) *naming.IssueID {
	if value == nil {
		return nil
	}
	trimmed := strings.TrimSpace(*value)
	if trimmed == "" {
		return nil
	}
	typed, err := naming.ParseIssueID(trimmed)
	if err != nil {
		return nil
	}
	return &typed
}

type dependencyEdgeKey struct {
	depID   naming.IssueID
	depType domain.DependencyType
}

func buildTaskDependencyEdgeSet(tasks []domain.Task) map[naming.IssueID]map[dependencyEdgeKey]struct{} {
	edges := make(map[naming.IssueID]map[dependencyEdgeKey]struct{}, len(tasks))
	for _, task := range tasks {
		taskID := task.ID
		if _, ok := edges[taskID]; !ok {
			edges[taskID] = map[dependencyEdgeKey]struct{}{}
		}
		for _, dep := range task.Dependencies {
			edges[taskID][dependencyEdgeKey{depID: dep.ID, depType: dep.Type}] = struct{}{}
		}
		if task.ParentID != nil && !task.ParentID.IsZero() {
			edges[taskID][dependencyEdgeKey{depID: *task.ParentID, depType: domain.DependencyParentChild}] = struct{}{}
		}
	}
	return edges
}

func dependencyTypeOrDefault(raw string) (domain.DependencyType, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return domain.DependencyBlocks, nil
	}
	switch domain.DependencyType(trimmed) {
	case domain.DependencyBlocks, domain.DependencyRelatedTo, domain.DependencyParentChild, domain.DependencyDiscovered, domain.DependencyCreatedIn:
		return domain.DependencyType(trimmed), nil
	default:
		return "", fmt.Errorf("invalid dependency type: %s", raw)
	}
}

func planDependencyMutation(
	index int,
	mutation dependencyBulkMutation,
	taskIDs map[naming.IssueID]struct{},
	taskEdges map[naming.IssueID]map[dependencyEdgeKey]struct{},
) (dependencyBulkOutcome, []protocol.ApplyOperationBody, error) {
	issueID := strings.TrimSpace(mutation.IssueID)
	if issueID == "" {
		issueID = strings.TrimSpace(mutation.ID)
	}
	action := strings.TrimSpace(strings.ToLower(mutation.Action))
	depType, err := dependencyTypeOrDefault(mutation.Type)
	if err != nil {
		return dependencyBulkOutcome{}, nil, err
	}
	outcome := dependencyBulkOutcome{
		Index:   index,
		Action:  action,
		IssueID: issueID,
		Type:    string(depType),
	}
	if issueID == "" {
		return outcome, nil, fmt.Errorf("missing issue_id")
	}
	typedIssueID, err := naming.ParseIssueID(issueID)
	if err != nil {
		return outcome, nil, fmt.Errorf("invalid issue_id: %w", err)
	}
	if _, ok := taskIDs[typedIssueID]; !ok {
		return outcome, nil, fmt.Errorf("issue not found: %s", issueID)
	}
	edges := taskEdges[typedIssueID]
	if edges == nil {
		edges = map[dependencyEdgeKey]struct{}{}
	}

	switch action {
	case "add":
		depID := strings.TrimSpace(mutation.DependsOnID)
		outcome.Dependency = depID
		if depID == "" {
			return outcome, nil, fmt.Errorf("missing depends_on_id")
		}
		typedDepID, err := naming.ParseIssueID(depID)
		if err != nil {
			return outcome, nil, fmt.Errorf("invalid depends_on_id: %w", err)
		}
		if _, ok := taskIDs[typedDepID]; !ok {
			return outcome, nil, fmt.Errorf("depends_on issue not found: %s", depID)
		}
		key := dependencyEdgeKey{depID: typedDepID, depType: depType}
		if _, exists := edges[key]; exists {
			outcome.Status = "no-op"
			outcome.Reason = "edge already exists"
			return outcome, nil, nil
		}
		edges[key] = struct{}{}
		outcome.Status = "planned"
		body, err := json.Marshal(daemonclient.TaskDependencyParams{
			TaskID:      typedIssueID,
			DependsOnID: typedDepID,
			Type:        string(depType),
		})
		if err != nil {
			return outcome, nil, fmt.Errorf("marshal add payload: %w", err)
		}
		return outcome, []protocol.ApplyOperationBody{{
			Command: daemonclient.CommandTaskDependencyAdd,
			Body:    body,
		}}, nil
	case "remove":
		depID := strings.TrimSpace(mutation.DependsOnID)
		outcome.Dependency = depID
		if depID == "" {
			return outcome, nil, fmt.Errorf("missing depends_on_id")
		}
		typedDepID, err := naming.ParseIssueID(depID)
		if err != nil {
			return outcome, nil, fmt.Errorf("invalid depends_on_id: %w", err)
		}
		key := dependencyEdgeKey{depID: typedDepID, depType: depType}
		if _, exists := edges[key]; !exists {
			outcome.Status = "no-op"
			outcome.Reason = "edge already absent"
			return outcome, nil, nil
		}
		delete(edges, key)
		outcome.Status = "planned"
		body, err := json.Marshal(daemonclient.TaskDependencyRemoveParams{
			TaskID:      typedIssueID,
			DependsOnID: typedDepID,
			Type:        string(depType),
			Confirm:     true,
		})
		if err != nil {
			return outcome, nil, fmt.Errorf("marshal remove payload: %w", err)
		}
		return outcome, []protocol.ApplyOperationBody{{
			Command: daemonclient.CommandTaskDependencyRemove,
			Body:    body,
		}}, nil
	case "retarget":
		fromID := strings.TrimSpace(mutation.FromID)
		toID := strings.TrimSpace(mutation.ToID)
		outcome.FromID = fromID
		outcome.ToID = toID
		if fromID == "" || toID == "" {
			return outcome, nil, fmt.Errorf("retarget requires from_id and to_id")
		}
		typedFromID, err := naming.ParseIssueID(fromID)
		if err != nil {
			return outcome, nil, fmt.Errorf("invalid from_id: %w", err)
		}
		typedToID, err := naming.ParseIssueID(toID)
		if err != nil {
			return outcome, nil, fmt.Errorf("invalid to_id: %w", err)
		}
		if _, ok := taskIDs[typedToID]; !ok {
			return outcome, nil, fmt.Errorf("to issue not found: %s", toID)
		}
		removeKey := dependencyEdgeKey{depID: typedFromID, depType: depType}
		addKey := dependencyEdgeKey{depID: typedToID, depType: depType}
		needsRemove := false
		needsAdd := false
		if _, exists := edges[removeKey]; exists {
			needsRemove = true
		}
		if _, exists := edges[addKey]; !exists {
			needsAdd = true
		}
		if !needsRemove && !needsAdd {
			outcome.Status = "no-op"
			outcome.Reason = "retarget already applied"
			return outcome, nil, nil
		}
		plannedOps := make([]protocol.ApplyOperationBody, 0, 2)
		if needsRemove {
			delete(edges, removeKey)
			body, err := json.Marshal(daemonclient.TaskDependencyRemoveParams{
				TaskID:      typedIssueID,
				DependsOnID: typedFromID,
				Type:        string(depType),
				Confirm:     true,
			})
			if err != nil {
				return outcome, nil, fmt.Errorf("marshal retarget remove payload: %w", err)
			}
			plannedOps = append(plannedOps, protocol.ApplyOperationBody{
				Command: daemonclient.CommandTaskDependencyRemove,
				Body:    body,
			})
		}
		if needsAdd {
			edges[addKey] = struct{}{}
			body, err := json.Marshal(daemonclient.TaskDependencyParams{
				TaskID:      typedIssueID,
				DependsOnID: typedToID,
				Type:        string(depType),
			})
			if err != nil {
				return outcome, nil, fmt.Errorf("marshal retarget add payload: %w", err)
			}
			plannedOps = append(plannedOps, protocol.ApplyOperationBody{
				Command: daemonclient.CommandTaskDependencyAdd,
				Body:    body,
			})
		}
		outcome.Status = "planned"
		return outcome, plannedOps, nil
	default:
		return outcome, nil, fmt.Errorf("unsupported action: %s", action)
	}
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
		domain.DependencyCreatedIn,
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

func formatIssueImplementationSummary(impls []string) string {
	if len(impls) == 0 {
		return "-"
	}
	filtered := make([]string, 0, len(impls))
	for _, impl := range impls {
		if trimmed := strings.TrimSpace(impl); trimmed != "" {
			filtered = append(filtered, trimmed)
		}
	}
	if len(filtered) == 0 {
		return "-"
	}
	return strings.Join(filtered, ",")
}

func renderIssueRuntimeSummary(task domain.Task) string {
	parts := make([]string, 0, 3)
	if task.Session != nil {
		sessionSummary := fmt.Sprintf("session=%s", task.Session.State)
		if task.Session.TmuxAttachedCount > 1 {
			sessionSummary = fmt.Sprintf("%s tmux_attached=%d", sessionSummary, task.Session.TmuxAttachedCount)
		} else if task.Session.TmuxAttached || task.Session.TmuxAttachedCount == 1 {
			sessionSummary = fmt.Sprintf("%s tmux_attached=yes", sessionSummary)
		}
		if task.Session.StartedAt != nil {
			sessionSummary = fmt.Sprintf("%s since %s", sessionSummary, task.Session.StartedAt.UTC().Format(time.RFC3339))
		}
		parts = append(parts, sessionSummary)
	} else if task.HasTmuxSession {
		parts = append(parts, "session=present")
	}
	if task.HasWorktree {
		parts = append(parts, "worktree=yes")
	}
	return strings.Join(parts, ", ")
}

func renderIssueGitSummary(task domain.Task) string {
	parts := make([]string, 0, 4)
	if task.HasUncommittedChanges {
		parts = append(parts, "dirty")
	}
	if task.GitAdditions > 0 || task.GitDeletions > 0 {
		parts = append(parts, fmt.Sprintf("+%d/-%d", task.GitAdditions, task.GitDeletions))
	}
	if task.GitAheadCount != 0 || task.GitBehindCount != 0 {
		parts = append(parts, fmt.Sprintf("ahead=%d behind=%d", task.GitAheadCount, task.GitBehindCount))
	}
	return strings.Join(parts, ", ")
}

type dependencyDetails struct {
	ID     string                `json:"id"`
	Type   domain.DependencyType `json:"type"`
	Status string                `json:"status"`
}

func printDependencies(deps []dependencyDetails) {
	if len(deps) == 0 {
		return
	}
	fmt.Println("Dependency edges:")
	for _, dep := range deps {
		fmt.Printf("- %s (%s, status=%s)\n", dep.ID, dep.Type, dep.Status)
	}
}

func printDependents(deps []dependencyDetails) {
	if len(deps) == 0 {
		return
	}
	fmt.Println("Dependents:")
	for _, dep := range deps {
		fmt.Printf("- %s (%s, status=%s)\n", dep.ID, dep.Type, dep.Status)
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
		idSet[task.ID.String()] = struct{}{}
	}

	topLevel := make(map[string]struct{}, len(tasks))
	for _, task := range tasks {
		topLevel[task.ID.String()] = struct{}{}
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
			parentID := strings.TrimSpace(task.ParentID.String())
			if _, ok := idSet[parentID]; ok {
				addLink(task.ID.String(), parentID, domain.DependencyParentChild)
			}
		}
		for _, dep := range task.Dependencies {
			depID := strings.TrimSpace(dep.ID.String())
			if _, ok := idSet[depID]; ok {
				addLink(task.ID.String(), depID, dep.Type)
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

func buildDependencyProjection(task domain.Task, allTasks []domain.Task) ([]dependencyDetails, []dependencyDetails) {
	statusByID := make(map[string]string, len(allTasks))
	for _, candidate := range allTasks {
		statusByID[candidate.ID.String()] = candidate.Status.String()
	}

	dependencies := make([]dependencyDetails, 0, len(task.Dependencies)+1)
	seenDependencies := make(map[string]struct{}, len(task.Dependencies)+1)

	addDependency := func(dep domain.Dependency) {
		id := strings.TrimSpace(dep.ID.String())
		if id == "" {
			return
		}
		key := id + "|" + string(dep.Type)
		if _, ok := seenDependencies[key]; ok {
			return
		}
		seenDependencies[key] = struct{}{}
		status := statusByID[id]
		if status == "" {
			status = "unknown"
		}
		dependencies = append(dependencies, dependencyDetails{
			ID:     id,
			Type:   dep.Type,
			Status: status,
		})
	}

	for _, dep := range task.Dependencies {
		addDependency(dep)
	}
	if task.ParentID != nil && strings.TrimSpace(task.ParentID.String()) != "" {
		addDependency(domain.Dependency{
			ID:   *task.ParentID,
			Type: domain.DependencyParentChild,
		})
	}

	dependents := make([]dependencyDetails, 0, 8)
	seenDependents := map[string]struct{}{}
	addDependent := func(dep domain.Dependency) {
		id := strings.TrimSpace(dep.ID.String())
		if id == "" {
			return
		}
		key := id + "|" + string(dep.Type)
		if _, ok := seenDependents[key]; ok {
			return
		}
		seenDependents[key] = struct{}{}
		status := statusByID[id]
		if status == "" {
			status = "unknown"
		}
		dependents = append(dependents, dependencyDetails{
			ID:     id,
			Type:   dep.Type,
			Status: status,
		})
	}

	for _, candidate := range allTasks {
		if candidate.ID == task.ID {
			continue
		}
		if candidate.ParentID != nil && strings.TrimSpace(candidate.ParentID.String()) == task.ID.String() {
			addDependent(domain.Dependency{
				ID:   candidate.ID,
				Type: domain.DependencyParentChild,
			})
		}
		for _, dep := range candidate.Dependencies {
			if strings.TrimSpace(dep.ID.String()) == task.ID.String() {
				addDependent(domain.Dependency{
					ID:   candidate.ID,
					Type: dep.Type,
				})
			}
		}
	}

	return dependencies, dependents
}

func executeBulkApply(deps *Dependencies, dryRun bool, asJSON bool, operations []protocol.ApplyOperationBody) error {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	snapshot, err := listTasksSnapshotForCLI(ctx, deps)
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
		RequestID:       makeRequestID(protocol.CommandTaskBulkApply),
		Kind:            protocol.EnvelopeKindCommand,
		Meta: protocol.Metadata{
			ProjectID: naming.ProjectID(deps.ProjectID),
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
	if asJSON {
		if _, err := os.Stdout.Write(resp.Body); err != nil {
			return fmt.Errorf("write bulk apply response: %w", err)
		}
		if !bytes.HasSuffix(resp.Body, []byte("\n")) {
			fmt.Println()
		}
		if applyResponseExitCode(resp) == exitCodePartialFailure {
			return fmt.Errorf("bulk apply completed with partial failures")
		}
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
	case "in_review":
		return domain.StatusInReview, nil
	case "closed":
		return domain.StatusDone, nil
	default:
		return "", fmt.Errorf("invalid status: %s", raw)
	}
}

// StartDaemonCommand starts daemon process and verifies client attach.
func StartDaemonCommand(deps *Dependencies) error {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	launcher := newLauncher(runtimeRepoDirForDeps(deps), deps.DaemonSocket)
	if err := launcher.Start(ctx); err != nil {
		return fmt.Errorf("start daemon: %w", err)
	}
	if err := ensureDaemon(ctx, deps, "cli"); err != nil {
		return fmt.Errorf("daemon health check after start failed: %w", err)
	}

	fmt.Println("Daemon started successfully.")
	return nil
}

// RestartDaemonCommand forces daemon replacement and verifies client re-attach.
func RestartDaemonCommand(deps *Dependencies) error {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	launcher := newLauncher(runtimeRepoDirForDeps(deps), deps.DaemonSocket)
	if concreteLauncher, ok := launcher.(*daemonprocess.Launcher); ok {
		concreteLauncher.WithReplaceReason("manual-restart")
	}
	if err := launcher.Replace(ctx); err != nil {
		return fmt.Errorf("restart daemon: %w", err)
	}
	if err := ensureDaemon(ctx, deps, "cli"); err != nil {
		return fmt.Errorf("daemon health check after restart failed: %w", err)
	}

	fmt.Println("Daemon restarted successfully.")
	return nil
}

// StopDaemonCommand stops daemon lock-owner process for current daemon scope.
func StopDaemonCommand(deps *Dependencies) error {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	launcher := newLauncher(runtimeRepoDirForDeps(deps), deps.DaemonSocket)
	if err := launcher.Stop(ctx); err != nil {
		return fmt.Errorf("stop daemon: %w", err)
	}

	fmt.Println("Daemon stopped successfully.")
	return nil
}

type primeTemplateData struct {
	ActiveIssueID            string
	SpecEnabled              bool
	PrimeEvidenceKey         string
	OrchestrationVia         string
	OrchestrationViaAz       bool
	OrchestrationViaNative   bool
	TmuxAvailable            bool
	OrchestratorExitContract string
	IssueSection             string
	ActiveIssueClosedWarning string
	ContextGuardrail         string
	QuestionFirstGuardrails  string
	ImplementationSection    string
	ImplementationGuardrails string
	LearningSection          string
	SpecGuardrails           string
}

func PrimeCommand(deps *Dependencies) error {
	issueID := strings.TrimSpace(os.Getenv("AZEDARACH_ISSUE_ID"))
	issueIDSource := ""
	if issueID != "" {
		issueIDSource = "env"
	}
	primeMode := strings.TrimSpace(os.Getenv("AZEDARACH_PRIME_MODE"))
	guardrail := "- No active issue is preselected. When work starts, set `AZEDARACH_ISSUE_ID` or run `az issue get <issue-id>`."
	issueSection := ""
	activeIssueClosedWarning := ""
	specGuardrails := ""
	questionFirstGuardrails := ""
	orchestratorExitContract := ""
	implementationSection := ""
	implementationGuardrails := "- Implementation guardrails: `--impl` assigns implementation/spec variants only; it is not graph/root membership or parent selection. To parent new work under the active issue, run `az issue create \"Child task\"` from the correct `AZEDARACH_ISSUE_ID` context; for another parent/root, add an explicit `parent-child` edge. In multi-implementation repos, include explicit `--impl <impl>` only on implementation-specific new issue writes and use repeated `--impl` only for intentional shared work. For `az issue update`, `--update-impl` is only for changing implementation assignments; status/title/notes updates do not require it."
	learningSection := ""
	specEnabled := deps != nil && deps.Config != nil && deps.Config.Spec.Enabled
	orchestrationVia := primeOrchestrationVia(deps)
	orchestrationViaAz := strings.EqualFold(orchestrationVia, "az")
	orchestrationViaNative := strings.EqualFold(orchestrationVia, "native")
	tmuxAvailable := primeTmuxAvailable()

	if primeMode == "question-first" {
		questionFirstGuardrails = `- Question-first execution rules (Space+Q mode):
  - MUST ask follow-up questions immediately when the issue is underspecified or ambiguous.
  - MUST improve the current issue title and description before implementation work begins.
  - MUST record unknowns/open questions in the issue description so scope is explicit.`
	}
	if specEnabled {
		specGuardrails = `  - In this repo, when guidance says ` + "`spec`" + `, it means records managed by ` + "`az spec req ...`" + ` and ` + "`az spec link ...`" + `, not README.md, AGENTS.md, or other internal docs.
  - ALWAYS run ` + "`az spec read --issue <issue-id>`" + ` before starting behavior work; use ` + "`az spec link list --issue <issue-id>`" + ` when you need link-only detail.
  - To choose spec traceability, first inspect linked requirements, then use ` + "`az spec req list --query \"<issue title and feature terms>\" --match any --limit 10`" + ` and ` + "`az spec read --req <req-id>`" + ` to find nearby requirements across naming variants; avoid unbounded requirement lists during session startup.
  - Link an existing requirement when it already defines the intended behavior; create or update a requirement before implementation when work adds behavior, changes user-visible behavior, changes a CLI/API/TUI contract, alters persistence/daemon semantics, or reveals an underspecified contract.
  - Contract-preserving work usually does not need a new requirement: refactors, tests, formatting, tooling, observability, dependency/internal cleanup, docs/process-only edits, or fixes that restore already-specified behavior.
  - For contract-preserving work, record explicit issue-note evidence such as ` + "`Spec impact: none (contract-preserving refactor)`" + `, ` + "`Spec impact: none (tests/tooling only)`" + `, or ` + "`Spec impact: none (fix restores existing behavior)`" + `.
  - If behavior work has no linked requirements after that check, do not treat missing links as permission to skip spec alignment.
  - If implementation is not aligned with spec, update spec first, then implement.
  - Ensure implementation issue(s) are linked to relevant spec requirement(s) before execution.
  - Treat ` + "`az spec link`" + ` records as required traceability for behavior work.
  - Before implementing behavior changes, inspect relevant ` + "`az spec read --issue <issue-id>`" + ` output and align the plan.
  - If this project should not use spec workflows, disable them with ` + "`az config set spec.enabled false`" + ` (or set ` + "`spec.enabled`" + ` to false in ` + "`.azedarach/config.json`" + `).`
	}

	var snapshot daemonclient.TaskSnapshot
	snapshotLoaded := false
	if deps != nil && deps.DaemonClient != nil {
		loaded, err := deps.DaemonClient.ListTasksSnapshotWithDependencies(context.Background())
		if err == nil {
			snapshot = loaded
			snapshotLoaded = true
			implementationSection = renderPrimeImplementationSection(configuredIssueImplementations(snapshot.Tasks))
		}
	}
	if issueID == "" && snapshotLoaded {
		if tmuxIssueID, ok := activeIssueIDFromTmuxPaneInSnapshot(context.Background(), deps, snapshot); ok {
			issueID = tmuxIssueID
			issueIDSource = "tmux"
		}
	}

	if issueID != "" {
		if issueIDSource == "tmux" {
			guardrail = fmt.Sprintf("- `AZEDARACH_ISSUE_ID` is absent, but the current tmux session resolves to issue `%s`; use it as the default issue scope and refresh stale context with `az issue get %s`.", issueID, issueID)
		} else {
			guardrail = fmt.Sprintf("- `AZEDARACH_ISSUE_ID` is set to `%s`; use it as the default issue scope and refresh stale context with `az issue get %s`.", issueID, issueID)
		}
		readiness, hasReadiness := loadPrimeTaskGraphReadiness(context.Background(), deps, issueID)
		if orchestrationViaAz && hasReadiness && primeTaskGraphReadinessHasGraphState(issueID, readiness) {
			orchestratorExitContract = renderPrimeOrchestratorExitContract(issueID)
		}
		if !snapshotLoaded {
			issueSection = fmt.Sprintf("Active issue context (AZEDARACH_ISSUE_ID=%s):\nCould not load issue details automatically; run `az issue get %s`.\n", issueID, issueID)
		} else if task, ok := findTaskByID(snapshot.Tasks, issueID); ok {
			if detailTask, err := loadIssueDetailTask(context.Background(), deps, issueID); err == nil {
				task = detailTask
			}
			observations := readiness.WorkerObservations
			if orchestratorExitContract == "" && primeIssueIsAzOrchestrationRoot(task, snapshot.Tasks, orchestrationViaAz) {
				orchestratorExitContract = renderPrimeOrchestratorExitContract(issueID)
			}
			issueSection = renderPrimeIssueSection(issueID, task, snapshot.Tasks, observations, tmuxAvailable)
			if task.Status == domain.StatusDone {
				activeIssueClosedWarning = fmt.Sprintf("- Active issue `%s` is currently `closed`; start by picking/opening actionable work (for example `az issue list --limit 20` or `az issue create \"Next task\"`). Use `--deferred` only for standalone backlog work.", task.ID)
			}
		} else {
			issueSection = fmt.Sprintf("Active issue context (AZEDARACH_ISSUE_ID=%s):\nIssue not found in current project snapshot; run `az issue get %s`.\n", issueID, issueID)
		}
	}
	if issueID != "" {
		learningSection = renderPrimeLearningSection(context.Background(), deps, issueID)
	}

	output, err := clitext.Render("prime_output", primeTemplateData{
		ActiveIssueID:            issueID,
		SpecEnabled:              specEnabled,
		PrimeEvidenceKey:         primeEvidenceKey,
		OrchestrationVia:         orchestrationVia,
		OrchestrationViaAz:       orchestrationViaAz,
		OrchestrationViaNative:   orchestrationViaNative,
		TmuxAvailable:            tmuxAvailable,
		OrchestratorExitContract: orchestratorExitContract,
		IssueSection:             issueSection,
		ActiveIssueClosedWarning: activeIssueClosedWarning,
		ContextGuardrail:         guardrail,
		QuestionFirstGuardrails:  questionFirstGuardrails,
		ImplementationSection:    implementationSection,
		ImplementationGuardrails: implementationGuardrails,
		LearningSection:          learningSection,
		SpecGuardrails:           specGuardrails,
	})
	if err != nil {
		return fmt.Errorf("render prime output: %w", err)
	}
	fmt.Print(output)
	return nil
}

func renderPrimeLearningSection(ctx context.Context, deps *Dependencies, issueID string) string {
	if deps == nil || deps.DaemonClient == nil || strings.TrimSpace(issueID) == "" {
		return ""
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	payload, err := json.Marshal(protocol.LearnRecallRequestBody{
		ContextIssueID: naming.IssueID(issueID),
		Statuses:       []protocol.LearningStatus{protocol.LearningStatusAccepted, protocol.LearningStatusPromoted},
		Limit:          3,
	})
	if err != nil {
		return ""
	}
	resp, err := deps.DaemonClient.Command(ctx, protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       naming.RequestID(fmt.Sprintf("prime-learn-%d", time.Now().UnixNano())),
		Kind:            protocol.EnvelopeKindCommand,
		Command:         protocol.CommandLearnRecall,
		SentAt:          time.Now().UTC(),
		Body:            payload,
		Meta:            protocol.Metadata{ProjectID: naming.ProjectID(deps.ProjectID)},
	})
	if err != nil || !resp.OK || len(resp.Body) == 0 {
		return ""
	}
	var out protocol.LearnRecallResponseBody
	if err := json.Unmarshal(resp.Body, &out); err != nil || len(out.Learnings) == 0 {
		return ""
	}
	lines := []string{"Relevant accepted/promoted learnings:"}
	for _, learning := range out.Learnings {
		reason := strings.TrimSpace(learning.RecallReason)
		if reason != "" {
			reason = " (why: " + reason + ")"
		}
		lines = append(lines, fmt.Sprintf("- %s [%s]: %s%s", learning.ID, learning.Status, learning.Summary, reason))
	}
	lines = append(lines, "Use `az learn show <learning-id>` for evidence; long evidence is not injected by default.")
	return strings.Join(lines, "\n")
}

func activeIssueIDFromTmuxPane(ctx context.Context, deps *Dependencies) (string, bool) {
	if deps == nil || deps.DaemonClient == nil {
		return "", false
	}
	sessionName, err := tmuxPaneSessionName(ctx)
	if err != nil || strings.TrimSpace(sessionName) == "" {
		return "", false
	}
	for _, candidate := range knownSessionProjectCandidates(deps) {
		for _, scope := range candidate.Scopes {
			issueID, ok := naming.ParseIssueIDFromSessionName(sessionName, scope)
			if !ok || strings.TrimSpace(issueID) == "" {
				continue
			}
			if _, err := naming.ParseIssueID(issueID); err != nil {
				continue
			}
			return issueID, true
		}
	}
	return "", false
}

func activeIssueIDFromTmuxPaneIfKnown(ctx context.Context, deps *Dependencies) (string, bool) {
	issueID, ok := activeIssueIDFromTmuxPane(ctx, deps)
	if !ok {
		return "", false
	}
	_, _, found, err := loadIssueMetadataTask(ctx, deps, issueID)
	if err != nil || !found {
		return "", false
	}
	return issueID, true
}

func activeIssueIDFromTmuxPaneInSnapshot(ctx context.Context, deps *Dependencies, snapshot daemonclient.TaskSnapshot) (string, bool) {
	issueID, ok := activeIssueIDFromTmuxPane(ctx, deps)
	if !ok {
		return "", false
	}
	if _, found := findTaskByID(snapshot.Tasks, issueID); !found {
		return "", false
	}
	return issueID, true
}

func defaultTmuxPaneSessionName(ctx context.Context) (string, error) {
	paneID := strings.TrimSpace(os.Getenv("TMUX_PANE"))
	if paneID == "" {
		return "", nil
	}
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
	}
	cmd := exec.CommandContext(ctx, "tmux", "display-message", "-p", "-t", paneID, "#{session_name}")
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func loadIssueDetailTask(ctx context.Context, deps *Dependencies, issueID string) (domain.Task, error) {
	if deps == nil || deps.DaemonClient == nil {
		return domain.Task{}, fmt.Errorf("daemon client is required")
	}
	snapshot, err := deps.DaemonClient.GetTaskSnapshot(ctx, issueID)
	if err != nil {
		return domain.Task{}, err
	}
	if err := snapshot.RequireFullDetails("issue detail read"); err != nil {
		return domain.Task{}, err
	}
	task, ok := findTaskByID(snapshot.Tasks, issueID)
	if !ok {
		return domain.Task{}, fmt.Errorf("issue not found: %s", issueID)
	}
	return task, nil
}

func loadPrimeTaskGraphReadiness(ctx context.Context, deps *Dependencies, issueID string) (daemonclient.TaskGraphReadiness, bool) {
	if deps == nil || deps.DaemonClient == nil || strings.TrimSpace(issueID) == "" {
		return daemonclient.TaskGraphReadiness{}, false
	}
	ready, err := deps.DaemonClient.TaskGraphReadiness(ctx, issueID)
	if err != nil {
		return daemonclient.TaskGraphReadiness{}, false
	}
	return ready, true
}

func primeTmuxAvailable() bool {
	_, err := primeLookPath("tmux")
	return err == nil
}

func primeOrchestrationVia(deps *Dependencies) string {
	if deps == nil || deps.Config == nil {
		return "az"
	}
	via := strings.TrimSpace(deps.Config.Orchestration.Via)
	if via == "" {
		return "az"
	}
	return strings.ToLower(via)
}

func renderPrimeImplementationSection(implementations []string) string {
	if len(implementations) <= 1 {
		return ""
	}
	quoted := make([]string, 0, len(implementations))
	for _, impl := range implementations {
		quoted = append(quoted, fmt.Sprintf("`%s`", impl))
	}
	exampleImpl := implementations[0]
	return fmt.Sprintf("- Implementation selection (multi-implementation project):\n"+
		"  - Available implementations: %s\n"+
		"  - Use `az impl list` to refresh the available options.\n"+
		"  - If you mean \"make this a child of the active issue\", run `az issue create \"Child task\"`; auto-parenting uses `AZEDARACH_ISSUE_ID`, not `--impl`.\n"+
		"  - If you mean \"attach this to another parent/root\", create the issue and add `az issue dep add <child-id> <parent-id> --type parent-child`.\n"+
		"  - `--impl` selects implementation/spec variant assignment only; it does not attach an issue to a parent/root graph.\n"+
		"  - New issue writes for a specific implementation must choose one, for example `az issue create --impl %s \"Implementation-specific task\"`; this still relies on auto-parenting or parent-child edges for graph membership.\n"+
		"  - Repeat `--impl` only for intentionally shared implementation work. Existing issue updates do not use `--impl`; use `--update-impl` only when changing assignments.\n",
		strings.Join(quoted, ", "), exampleImpl)
}

func renderPrimeIssueSection(issueID string, task domain.Task, tasks []domain.Task, observations []domain.WorkerObservation, tmuxAvailable bool) string {
	structuredContext := renderPrimeStructuredIssueContext(issueID, task)
	implementations := formatPrimeImplementations(task.Implementations)
	parent := ""
	mailbox := ""
	if task.ParentID != nil && strings.TrimSpace(task.ParentID.String()) != "" {
		parentID := strings.TrimSpace(task.ParentID.String())
		parent = fmt.Sprintf("\nParent: %s", parentID)
		mailbox = fmt.Sprintf("- Worker mailbox: receive orchestrator messages with `az mail list --parent %s --since 0 --json`; use `az mail watch --parent %s --since <seq> --jsonl` only when explicitly asked to monitor continuously. Send `worker-integration-ready` with a complete JSON `worker_evidence.v1` body containing `summary`, `commands_run`, `key_assertions`, `files_changed`, `review`, `risks`, and optional `artifact_links`.\n", parentID, parentID)
	}
	childWorkRecommendation := renderPrimeChildWorkRecommendation(task, tasks, tmuxAvailable)
	observationSection := renderPrimeWorkerObservationSection(observations)
	return fmt.Sprintf(
		"Active issue context (AZEDARACH_ISSUE_ID=%s):\nRefresh with `az issue get %s` if this looks stale.\n```\n%s: %s [status=%s priority=%s type=%s impl=%s]%s%s\nDependencies:\n%s\n```\n%s%s%s",
		issueID,
		issueID,
		task.ID,
		task.Title,
		task.Status,
		task.Priority.String(),
		task.Type,
		implementations,
		parent,
		structuredContext,
		formatPrimeDependencyLines(task.Dependencies),
		observationSection,
		childWorkRecommendation,
		mailbox,
	)
}

func renderPrimeOrchestratorExitContract(rootIssueID string) string {
	return fmt.Sprintf(`Orchestrator Exit Contract (root %s):
- Keep running the status/start/watch/integrate/close loop until `+"`az orchestrate complete-check --root %s`"+` passes, then run final validation and close the root.
- Treat `+"`worker-integration-ready`"+` and `+"`in_review`"+` as evidence to inspect and validate; if accepted, run `+"`az issue close --id <worker>`"+` instead of handing off to the user.
- Send the final assistant response only after root completion, a named hard blocker, or an explicit user pause.
`, rootIssueID, rootIssueID)
}

func primeIssueIsAzOrchestrationRoot(task domain.Task, tasks []domain.Task, orchestrationViaAz bool) bool {
	if !orchestrationViaAz {
		return false
	}
	return task.Type == domain.TypeEpic || issueHasChildren(task.ID, tasks)
}

func primeTaskGraphReadinessHasGraphState(rootIssueID string, ready daemonclient.TaskGraphReadiness) bool {
	if primeAnyIssueOtherThanRoot(rootIssueID, ready.Runnable) || primeAnyIssueOtherThanRoot(rootIssueID, ready.Active) {
		return true
	}
	for _, pending := range ready.Pending {
		if primeIssueDiffersFromRoot(rootIssueID, pending.IssueID) {
			return true
		}
	}
	for _, session := range ready.ActiveSessions {
		if primeIssueDiffersFromRoot(rootIssueID, session.IssueID) {
			return true
		}
	}
	for _, progress := range ready.SessionStartProgress {
		if primeIssueDiffersFromRoot(rootIssueID, progress.IssueID) {
			return true
		}
	}
	for _, candidate := range ready.StaleCloseableChildren {
		if primeIssueDiffersFromRoot(rootIssueID, candidate.IssueID) {
			return true
		}
	}
	for issueID := range ready.Blocked {
		if primeIssueDiffersFromRoot(rootIssueID, issueID) {
			return true
		}
	}
	return false
}

func primeAnyIssueOtherThanRoot(rootIssueID string, issueIDs []string) bool {
	for _, issueID := range issueIDs {
		if primeIssueDiffersFromRoot(rootIssueID, issueID) {
			return true
		}
	}
	return false
}

func primeIssueDiffersFromRoot(rootIssueID, issueID string) bool {
	rootIssueID = strings.TrimSpace(rootIssueID)
	issueID = strings.TrimSpace(issueID)
	return issueID != "" && !strings.EqualFold(issueID, rootIssueID)
}

func renderPrimeStructuredIssueContext(issueID string, task domain.Task) string {
	var b strings.Builder
	if strings.TrimSpace(task.Description) != "" {
		fmt.Fprintf(&b, "\nDescription: %s", summarizePrimeDescription(issueID, task.Description))
	}
	if strings.TrimSpace(task.Acceptance) != "" {
		fmt.Fprintf(&b, "\nAcceptance: %s", summarizePrimeDescription(issueID, task.Acceptance))
	}
	if strings.TrimSpace(task.Design) != "" {
		fmt.Fprintf(&b, "\nDesign: %s", summarizePrimeDescription(issueID, task.Design))
	}
	return b.String()
}

func renderPrimeWorkerObservationSection(observations []domain.WorkerObservation) string {
	if len(observations) == 0 {
		return ""
	}
	sorted := append([]domain.WorkerObservation(nil), observations...)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].IssueID < sorted[j].IssueID
	})
	if len(sorted) > 5 {
		sorted = sorted[:5]
	}
	var b strings.Builder
	b.WriteString("- Observation/evidence projection:\n")
	for _, observation := range sorted {
		issueID := strings.TrimSpace(observation.IssueID)
		if issueID == "" {
			issueID = "(unknown)"
		}
		reason := strings.TrimSpace(observation.Reason)
		if reason == "" {
			reason = "no daemon reason reported"
		}
		fmt.Fprintf(&b, "  - %s: %s - %s\n", issueID, observation.State, reason)
		if observation.LastEvent != nil {
			event := observation.LastEvent
			eventType := strings.TrimSpace(event.Type)
			if eventType == "" {
				eventType = strings.TrimSpace(event.Kind)
			}
			if eventType != "" {
				fmt.Fprintf(&b, "    Last event: %s", eventType)
				if strings.TrimSpace(event.Summary) != "" {
					fmt.Fprintf(&b, " - %s", strings.TrimSpace(event.Summary))
				}
				b.WriteString("\n")
			}
		}
		writePrimeObservationList(&b, "Evidence", observation.EvidenceSummary, 2)
		writePrimeObservationList(&b, "Risks", observation.Risks, 2)
		writePrimeObservationList(&b, "Next", observation.NextActions, 2)
	}
	if len(observations) > len(sorted) {
		fmt.Fprintf(&b, "  - ... %d more observations omitted; run `az orchestrate observe --root <root>` for full projection.\n", len(observations)-len(sorted))
	}
	return b.String()
}

func writePrimeObservationList(b *strings.Builder, label string, values []string, limit int) {
	values = nonEmptyStrings(values)
	if len(values) == 0 {
		return
	}
	if limit > 0 && len(values) > limit {
		values = values[:limit]
	}
	fmt.Fprintf(b, "    %s: %s\n", label, strings.Join(values, "; "))
}

func nonEmptyStrings(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

func renderPrimeChildWorkRecommendation(task domain.Task, tasks []domain.Task, tmuxAvailable bool) string {
	if task.Type != domain.TypeEpic && !issueHasChildren(task.ID, tasks) {
		return ""
	}
	commands := "`az issue create \"Child task\"` for tracking-only child work"
	sessionCommands := "`az session start <child-issue>`"
	if tmuxAvailable {
		commands += "; use `az issue split \"Child task\"` only when that child should launch immediately in its own session"
		sessionCommands += " or `az issue split \"Child task\"`"
	}
	return fmt.Sprintf("- Parent-context recommendation: `%s` is an epic or already has child issues; keep implementation/subtask work in child issues with %s instead of accumulating detailed work on the parent. Do the child implementation from the child issue execution context: preferably a child session (%s) and at minimum the child worktree (`az worktree create <child-issue>`).\n", task.ID, commands, sessionCommands)
}

func issueHasChildren(issueID naming.IssueID, tasks []domain.Task) bool {
	for _, candidate := range tasks {
		if candidate.ParentID == nil {
			continue
		}
		if strings.TrimSpace(candidate.ParentID.String()) == issueID.String() {
			return true
		}
	}
	return false
}

func summarizePrimeDescription(issueID, description string) string {
	const (
		maxLines = 8
		maxRunes = 800
	)

	trimmed := strings.TrimSpace(description)
	if trimmed == "" {
		return ""
	}

	truncated := false
	runes := []rune(trimmed)
	if len(runes) > maxRunes {
		trimmed = string(runes[:maxRunes])
		truncated = true
	}

	lines := strings.Split(trimmed, "\n")
	if len(lines) > maxLines {
		lines = lines[:maxLines]
		truncated = true
	}

	snippet := strings.TrimSpace(strings.Join(lines, "\n"))
	if !truncated {
		return snippet
	}
	return fmt.Sprintf("%s\n… (truncated; run `az issue get %s` for full context)", snippet, issueID)
}

func formatPrimeImplementations(implementations []string) string {
	if len(implementations) == 0 {
		return "(none)"
	}
	normalized := make([]string, 0, len(implementations))
	for _, impl := range implementations {
		trimmed := strings.TrimSpace(impl)
		if trimmed == "" {
			continue
		}
		normalized = append(normalized, trimmed)
	}
	if len(normalized) == 0 {
		return "(none)"
	}
	sort.Strings(normalized)
	return strings.Join(normalized, ",")
}

func formatPrimeDependencyLines(deps []domain.Dependency) string {
	if len(deps) == 0 {
		return "(none)"
	}
	grouped := map[domain.DependencyType][]string{}
	for _, dep := range deps {
		grouped[dep.Type] = append(grouped[dep.Type], dep.ID.String())
	}
	lines := make([]string, 0, len(grouped))
	order := []domain.DependencyType{
		domain.DependencyBlocks,
		domain.DependencyParentChild,
		domain.DependencyRelatedTo,
		domain.DependencyDiscovered,
		domain.DependencyCreatedIn,
		domain.DependencyBlockedBy,
	}
	for _, typ := range order {
		ids := grouped[typ]
		if len(ids) == 0 {
			continue
		}
		sort.Strings(ids)
		lines = append(lines, fmt.Sprintf("- %s: %s", typ, strings.Join(ids, ", ")))
	}
	if len(lines) == 0 {
		return "(none)"
	}
	return strings.Join(lines, "\n")
}

func PrintUsage() {
	fmt.Println("Argument ordering: place flags/options before positional arguments for deterministic parsing.")
	usage, err := clitext.Render("root_usage", nil)
	if err != nil {
		fmt.Printf("Usage unavailable: %v\n", err)
		return
	}
	fmt.Print(usage)
}

type sessionRequestBody struct {
	ProjectID  string `json:"project_id"`
	SessionID  string `json:"session_id"`
	IssueID    string `json:"issue_id,omitempty"`
	BaseBranch string `json:"base_branch,omitempty"`
}

type commandOutputBody struct {
	Output string `json:"output"`
}

type commandOutputEnvelope struct {
	OperationID *string         `json:"operation_id,omitempty"`
	State       *string         `json:"state,omitempty"`
	Output      *string         `json:"output,omitempty"`
	Result      json.RawMessage `json:"result,omitempty"`
}

type decodedCommandOutput struct {
	Output      string
	OperationID string
	State       protocol.OperationState
}

type applyExecutionResultBody struct {
	Summary applyExecutionSummaryBody `json:"summary"`
}

type applyExecutionSummaryBody struct {
	Total     int `json:"total"`
	Succeeded int `json:"succeeded"`
	Failed    int `json:"failed"`
}

func LogCommand(deps *Dependencies, opts LogOptions) error {
	if deps == nil {
		return fmt.Errorf("dependencies are required")
	}
	if len(opts.Sources) == 0 {
		opts.Sources = []string{"daemon", "tui", "cli"}
	}
	if opts.Lines < 1 {
		return fmt.Errorf("--lines must be greater than 0")
	}

	repoDir := strings.TrimSpace(deps.RepoDir)
	if repoDir == "" {
		repoDir = "."
	}
	runtimeRepoDir := strings.TrimSpace(deps.RuntimeRepoDir)
	if runtimeRepoDir == "" {
		runtimeRepoDir = resolveRuntimeRepoDir(repoDir)
	}
	sessionLogDir := resolveSessionLogDirFor(deps.Config, runtimeRepoDir)
	logSources := make([]logstream.SourceSpec, 0, len(opts.Sources))
	seen := make(map[string]struct{}, len(opts.Sources))
	for _, source := range opts.Sources {
		var logPath string
		normalizedSource := strings.ToLower(strings.TrimSpace(source))
		switch strings.ToLower(strings.TrimSpace(source)) {
		case "daemon":
			logPath = filepath.Join(runtimeRepoDir, ".azedarach", logging.DaemonLogFileName)
		case "tui":
			logPath = filepath.Join(sessionLogDir, logging.TUILogFileName)
		case "cli":
			logPath = filepath.Join(sessionLogDir, logging.CLILogFileName)
		default:
			return fmt.Errorf("unknown log source %q", source)
		}
		if _, ok := seen[normalizedSource]; ok {
			continue
		}
		seen[normalizedSource] = struct{}{}
		logSources = append(logSources, logstream.SourceSpec{
			Name: normalizedSource,
			Path: logPath,
		})
	}
	if len(logSources) == 0 {
		return fmt.Errorf("no log files selected")
	}
	availableLogSources := make([]logstream.SourceSpec, 0, len(logSources))
	for _, source := range logSources {
		if _, err := os.Stat(source.Path); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				fmt.Fprintf(os.Stderr, "warning: log file not found, skipping: %s\n", source.Path)
				continue
			}
			return fmt.Errorf("inspect log file %s: %w", source.Path, err)
		}
		availableLogSources = append(availableLogSources, source)
	}
	if len(availableLogSources) == 0 {
		return fmt.Errorf("none of the selected log files exist yet")
	}

	entries, err := logstream.ReadLastMerged(availableLogSources, opts.Lines)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		fmt.Fprintln(os.Stdout, logstream.FormatLine(entry.Source, entry.RawLine, time.Local))
	}
	if !opts.Follow {
		return nil
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	return logstream.Follow(ctx, availableLogSources, 250*time.Millisecond, func(entry logstream.Entry) error {
		if _, err := fmt.Fprintln(os.Stdout, logstream.FormatLine(entry.Source, entry.RawLine, time.Local)); err != nil {
			return fmt.Errorf("write log output: %w", err)
		}
		return nil
	})
}

func resolveSessionLogDir(cfg *config.Config) string {
	return resolveSessionLogDirFor(cfg, "")
}

func resolveSessionLogDirFor(cfg *config.Config, startPath string) string {
	return config.SessionLogDirFor(cfg, startPath)
}

func resolveRuntimeRepoDir(repoDir string) string {
	if !config.UseScopedDaemonRuntimeFor(repoDir) {
		return repoDir
	}
	if worktreeRoot, ok := resolveScopedWorktreeRoot(repoDir); ok {
		return worktreeRoot
	}
	return repoDir
}

func resolveScopedWorktreeRoot(startPath string) (string, bool) {
	candidates := make([]string, 0, 2)
	if cwd, err := os.Getwd(); err == nil && strings.TrimSpace(cwd) != "" {
		candidates = append(candidates, cwd)
	}
	if candidate := strings.TrimSpace(startPath); candidate != "" {
		candidates = append(candidates, candidate)
	}
	for _, candidate := range candidates {
		worktreeRoot, err := config.ResolveWorktreeRoot(candidate)
		if err != nil || strings.TrimSpace(worktreeRoot) == "" {
			continue
		}
		return worktreeRoot, true
	}
	return "", false
}

func makeRequestID(prefix string) naming.RequestID {
	id, _ := naming.ParseRequestID(fmt.Sprintf("%s-%d", prefix, time.Now().UTC().UnixNano()))
	return id
}

func newSessionRequest(command, projectID, sessionID, baseBranch string) protocol.RequestEnvelope {
	body, _ := json.Marshal(sessionRequestBody{
		ProjectID:  projectID,
		SessionID:  sessionID,
		BaseBranch: baseBranch,
	})
	parsedSessionID, _ := naming.ParseSessionIDLoose(sessionID)

	return protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       makeRequestID(command),
		Kind:            protocol.EnvelopeKindCommand,
		Command:         command,
		Meta: protocol.Metadata{
			ProjectID: naming.ProjectID(projectID),
			SessionID: parsedSessionID,
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
	return printCommandOutputWithWait(context.Background(), nil, resp, SessionCommandOptions{})
}

func printCommandOutputWithWait(ctx context.Context, deps *Dependencies, resp protocol.ResponseEnvelope, opts SessionCommandOptions) error {
	if len(resp.Body) == 0 {
		return nil
	}

	out, err := decodeCommandOutput(resp.Body)
	if err != nil {
		return fmt.Errorf("failed to decode daemon response: %w", err)
	}
	if opts.Wait && deps != nil && out.OperationID != "" && !operationStateTerminal(out.State) {
		record, err := deps.DaemonClient.WaitForOperation(ctx, out.OperationID, opts.PollInterval)
		if err != nil {
			return fmt.Errorf("wait for operation %s: %w", out.OperationID, err)
		}
		if err := printOperationOutcome(record); err != nil {
			return err
		}
		return nil
	}
	return printDecodedCommandOutput(out)
}

func decodeCommandOutput(body []byte) (decodedCommandOutput, error) {
	var envelope commandOutputEnvelope
	if err := json.Unmarshal(body, &envelope); err == nil {
		if envelope.Output != nil {
			return decodedCommandOutput{Output: *envelope.Output}, nil
		}
		if len(envelope.Result) > 0 {
			var nested commandOutputBody
			if err := json.Unmarshal(envelope.Result, &nested); err != nil {
				return decodedCommandOutput{}, err
			}
			return decodedCommandOutput{Output: nested.Output}, nil
		}
		state := operationStateFromString(stringValue(envelope.State))
		if envelope.OperationID != nil && state != "" && !operationStateTerminal(state) {
			return decodedCommandOutput{
				OperationID: stringValue(envelope.OperationID),
				State:       state,
			}, nil
		}
	}

	var out commandOutputBody
	if err := json.Unmarshal(body, &out); err != nil {
		return decodedCommandOutput{}, err
	}
	return decodedCommandOutput{Output: out.Output}, nil
}

func printDecodedCommandOutput(out decodedCommandOutput) error {
	if out.Output != "" {
		fmt.Print(out.Output)
		return nil
	}
	if out.OperationID != "" && out.State != "" {
		fmt.Printf("Operation %s: %s\n", out.State, out.OperationID)
	}
	return nil
}

func printOperationOutcome(record protocol.OperationRecord) error {
	if err := operationRecordError(record); err != nil {
		return err
	}
	if len(record.Result) > 0 {
		out, err := decodeCommandOutput(record.Result)
		if err != nil {
			return fmt.Errorf("decode operation result: %w", err)
		}
		return printDecodedCommandOutput(out)
	}
	fmt.Printf("Operation %s: %s\n", record.State, record.OperationID)
	return nil
}

func printOperationRecord(record protocol.OperationRecord, asJSON bool) error {
	if asJSON {
		return printJSON(record)
	}
	return printOperationList([]protocol.OperationRecord{record}, false)
}

func printOperationLogs(record protocol.OperationRecord, asJSON bool) error {
	if asJSON {
		return printJSON(record)
	}
	if err := printOperationRecord(record, false); err != nil {
		return err
	}
	if payload := formatJSONBlock(record.Payload); payload != "" {
		fmt.Printf("\nPayload:\n%s\n", payload)
	}
	if result := formatJSONBlock(record.Result); result != "" {
		fmt.Printf("\nResult (raw JSON):\n%s\n", result)
	}
	return nil
}

func printOperationList(records []protocol.OperationRecord, asJSON bool) error {
	if asJSON {
		return printJSON(records)
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tSTATE\tKIND\tISSUE\tENQUEUED")
	for _, record := range records {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n",
			record.OperationID,
			record.State,
			record.Kind,
			record.IssueID,
			record.EnqueuedAt.Format(time.RFC3339),
		)
	}
	if err := w.Flush(); err != nil {
		return err
	}
	if len(records) == 1 {
		if progress := operationProgressSummary(records[0]); progress != "" {
			fmt.Printf("\nProgress: %s\n", progress)
		}
		if errMsg := operationRecordMessage(records[0]); errMsg != "" {
			fmt.Printf("\nError: %s\n", errMsg)
		}
		if len(records[0].Result) > 0 {
			if out, err := decodeCommandOutput(records[0].Result); err == nil && out.Output != "" {
				if !strings.HasSuffix(out.Output, "\n") {
					out.Output += "\n"
				}
				fmt.Printf("\n%s", out.Output)
			}
		}
	}
	return nil
}

func printJSON(v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	fmt.Println(string(data))
	return nil
}

func formatJSONBlock(raw json.RawMessage) string {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return ""
	}
	var out bytes.Buffer
	if err := json.Indent(&out, trimmed, "", "  "); err != nil {
		return string(trimmed)
	}
	return out.String()
}

func parseOperationStates(values []string) ([]protocol.OperationState, error) {
	if len(values) == 0 {
		return nil, nil
	}
	states := make([]protocol.OperationState, 0, len(values))
	seen := make(map[protocol.OperationState]struct{}, len(values))
	for _, raw := range values {
		for _, part := range strings.Split(raw, ",") {
			state := operationStateFromString(strings.TrimSpace(part))
			if state == "" {
				continue
			}
			if !state.Valid() {
				return nil, fmt.Errorf("invalid operation state: %s", part)
			}
			if _, ok := seen[state]; ok {
				continue
			}
			seen[state] = struct{}{}
			states = append(states, state)
		}
	}
	return states, nil
}

func parseIssueListDateFilter(raw string, endOfDay bool) (*time.Time, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	if parsed, err := time.Parse(time.RFC3339Nano, raw); err == nil {
		parsed = parsed.UTC()
		return &parsed, nil
	}
	if parsed, err := time.Parse(time.RFC3339, raw); err == nil {
		parsed = parsed.UTC()
		return &parsed, nil
	}
	parsed, err := time.ParseInLocation("2006-01-02", raw, time.Local)
	if err != nil {
		return nil, fmt.Errorf("expected YYYY-MM-DD or RFC3339 timestamp")
	}
	if endOfDay {
		parsed = parsed.AddDate(0, 0, 1).Add(-time.Nanosecond)
	}
	parsed = parsed.UTC()
	return &parsed, nil
}

func parseIssueStatuses(values []string) ([]domain.Status, error) {
	if len(values) == 0 {
		return nil, nil
	}
	statuses := make([]domain.Status, 0, len(values))
	seen := make(map[domain.Status]struct{}, len(values))
	for _, value := range values {
		for _, part := range strings.Split(value, ",") {
			status, err := parseStatus(strings.TrimSpace(part))
			if err != nil {
				return nil, err
			}
			if _, ok := seen[status]; ok {
				continue
			}
			seen[status] = struct{}{}
			statuses = append(statuses, status)
		}
	}
	return statuses, nil
}

func operationStateFromString(raw string) protocol.OperationState {
	state := protocol.OperationState(strings.TrimSpace(raw))
	if !state.Valid() {
		return ""
	}
	return state
}

func operationStateTerminal(state protocol.OperationState) bool {
	switch state {
	case protocol.OperationStateDone,
		protocol.OperationStateFailed,
		protocol.OperationStateCancelled:
		return true
	default:
		return false
	}
}

func operationRecordError(record protocol.OperationRecord) error {
	switch record.State {
	case protocol.OperationStateFailed, protocol.OperationStateCancelled:
		if record.Error != nil && strings.TrimSpace(record.Error.Message) != "" {
			return errors.New(record.Error.Message)
		}
		return fmt.Errorf("operation %s %s", record.OperationID, record.State)
	default:
		return nil
	}
}

func operationRecordMessage(record protocol.OperationRecord) string {
	if record.Error == nil {
		return ""
	}
	return strings.TrimSpace(record.Error.Message)
}

func operationProgressSummary(record protocol.OperationRecord) string {
	if record.Progress == nil {
		return ""
	}
	if record.Progress.Percent > 0 && record.Progress.Message != "" {
		return fmt.Sprintf("%s (%d%%)", record.Progress.Message, record.Progress.Percent)
	}
	if record.Progress.Message != "" {
		return record.Progress.Message
	}
	if record.Progress.Total > 0 {
		return fmt.Sprintf("%d/%d %s", record.Progress.Current, record.Progress.Total, record.Progress.Unit)
	}
	return ""
}

func stringValue(v *string) string {
	if v == nil {
		return ""
	}
	return *v
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
	startedAt := time.Now()
	launcher := newLauncher(runtimeRepoDirForDeps(deps), deps.DaemonSocket)
	if concreteLauncher, ok := launcher.(*daemonprocess.Launcher); ok {
		concreteLauncher.WithLogger(deps.Logger)
	}
	orch := autoclient.NewAutostartOrchestrator(autoclient.NewDaemonHandshaker(deps.DaemonClient), launcher)
	ack, err := orch.EnsureAttached(ctx, protocol.Hello{
		ProtocolVersion: protocol.CurrentVersion,
		ClientName:      clientName,
		ClientVersion:   buildinfo.VersionString(),
		Capabilities:    []string{"snapshot", "subscribe"},
	})
	if err != nil {
		latencytrace.LogPhase(deps.Logger, "cli", "daemon_attach", startedAt, "client_name", clientName, "error", err)
		return fmt.Errorf("daemon attach failed: %w", err)
	}
	if !ack.Accepted {
		latencytrace.LogPhase(deps.Logger, "cli", "daemon_attach", startedAt, "client_name", clientName, "accepted", false, "reason", ack.Reason)
		return fmt.Errorf("daemon handshake rejected: %s", ack.Reason)
	}
	latencytrace.LogPhase(deps.Logger, "cli", "daemon_attach", startedAt, "client_name", clientName, "accepted", true, "daemon_version", ack.DaemonVersion)
	return nil
}

func commandWithDaemonAutostartRetry[T any](ctx context.Context, deps *Dependencies, call func(context.Context) (T, error)) (T, error) {
	value, err := call(ctx)
	if err == nil {
		return value, nil
	}
	if !shouldAutostartAfterDaemonReadError(err) {
		return value, err
	}
	startCtx, cancel := context.WithTimeout(context.Background(), issueCreateAutostartTimeout)
	defer cancel()
	var startErr error
	if deps == nil || deps.DaemonClient == nil {
		startErr = startDaemonLauncher(startCtx, deps)
	} else {
		startErr = ensureDaemon(startCtx, deps, "cli")
	}
	if startErr != nil {
		return value, fmt.Errorf("autostart daemon: %w", startErr)
	}
	return call(ctx)
}

func commandWithDaemonAutostartRetryWithinContext[T any](ctx context.Context, deps *Dependencies, call func(context.Context) (T, error)) (T, error) {
	value, err := call(ctx)
	if err == nil {
		return value, nil
	}
	if !shouldAutostartAfterDaemonReadError(err) {
		return value, err
	}
	if deps == nil || deps.DaemonClient == nil {
		if startErr := startDaemonLauncher(ctx, deps); startErr != nil {
			return value, fmt.Errorf("autostart daemon: %w", startErr)
		}
	} else if startErr := ensureDaemon(ctx, deps, "cli"); startErr != nil {
		return value, fmt.Errorf("autostart daemon: %w", startErr)
	}
	return call(ctx)
}

func startDaemonLauncher(ctx context.Context, deps *Dependencies) error {
	socketPath := ""
	if deps != nil {
		socketPath = deps.DaemonSocket
	}
	launcher := newLauncher(runtimeRepoDirForDeps(deps), socketPath)
	if concreteLauncher, ok := launcher.(*daemonprocess.Launcher); ok && deps != nil {
		concreteLauncher.WithLogger(deps.Logger)
	}
	return launcher.Start(ctx)
}

func shouldAutostartAfterDaemonReadError(err error) bool {
	if reconnect.IsTransientTransportError(err) {
		return true
	}
	var commandErr *daemonclient.CommandError
	if errors.As(err, &commandErr) {
		return commandErr.Code == protocol.ErrorCodeUnavailable && commandErr.Retryable
	}
	return false
}

func runtimeRepoDirForDeps(deps *Dependencies) string {
	if deps == nil {
		return "."
	}
	if repoDir := strings.TrimSpace(deps.RuntimeRepoDir); repoDir != "" {
		return repoDir
	}
	if repoDir := strings.TrimSpace(deps.RepoDir); repoDir != "" {
		return repoDir
	}
	return "."
}
