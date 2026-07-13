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
	commandSessionCapture       = daemonclient.CommandSessionCapture
	commandTaskSnapshotExport   = "task.snapshot.export"
	defaultExportFormat         = "json"
	defaultIssueListLimit       = 200
	defaultIssueContextRiskDays = 14
	defaultOperationListLimit   = 50
	sessionStartCommandTimeout  = 5 * time.Minute
	branchMergeToBaseTimeout    = 2 * time.Minute
	daemonCommandTimeout        = 15 * time.Second
	prCommandTimeout            = 2 * time.Minute
	issueCloseCleanupTimeout    = 10 * time.Minute
	issueCreateCommandTimeout   = 10 * time.Second
	issueCreateAutostartTimeout = 12 * time.Second
	exitCodeHardFailure         = 1
	exitCodePartialFailure      = 2
)

var primeLookPath = exec.LookPath

var primeDaemonReadTimeout = 8 * time.Second

type Dependencies struct {
	Config         *config.Config
	DaemonClient   *daemonclient.Client
	DaemonSocket   string
	Logger         *slog.Logger
	ProjectID      string
	RepoDir        string
	RuntimeRepoDir string
	TraceContext   context.Context
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
	Archived      string
	IDs           []string
	States        []domain.IssueDisplayPhase
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
	Archived     string
}

type IssueOwnershipOptions struct {
	Project   string
	IssueID   string
	OwnerID   string
	OwnerKind string
	TTL       string
	Force     bool
	JSON      bool
}

type IssueEventsOptions struct {
	Project    string
	IssueID    string
	JSON       bool
	JQHelp     bool
	EventTypes []string
	Limit      int
}

type IssueRecordOptions struct {
	Project          string
	IssueID          string
	EventType        string
	Summary          string
	Body             string
	DataJSON         string
	Source           string
	OperationID      string
	SessionID        string
	WorktreePath     string
	FollowUpIssueIDs []string
	JSON             bool
}

type IssueContextRiskOptions struct {
	Project string
	IssueID string
	JSON    bool
	Summary bool
	Full    bool
	Since   time.Time
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
	Lifecycle              domain.IssueWorkflow
	Deferred               bool
	Implementations        []string
	AutoParentFromIssueID  *string
	AutoCreatedFromIssueID *string
	ExplicitParent         bool
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

type IssueCleanupOptions struct {
	Project         string
	IDs             []string
	Statuses        []string
	Query           string
	UpdatedBefore   *time.Time
	Limit           int
	Action          string
	DryRun          bool
	JSON            bool
	PerIssueTimeout time.Duration
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
	Lifecycle       *domain.IssueWorkflow
	ForceWorktree   bool
	CascadeChildren bool
	UpdateImpls     []string
}

type IssueDoctorOptions struct {
	Project       string
	IssueID       string
	JSON          bool
	CheckpointWAL bool
	TruncateWAL   bool
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

type IssueUnarchiveOptions struct {
	Project         string
	IssueID         string
	JSON            bool
	WithParents     bool
	CascadeChildren bool
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

type SessionCaptureOptions struct {
	Project string
	IssueID string
	Lines   int
	JSON    bool
	Raw     bool
}

type WorktreeCreateOptions struct {
	Project    string
	IssueID    string
	BaseBranch string
	JSON       bool
}

type WorktreeDeleteOptions struct {
	Project      string
	IssueID      string
	Force        bool
	DeleteBranch bool
	JSON         bool
}

type PROptions struct {
	Command    string
	Project    string
	IssueID    string
	Branch     string
	Number     int
	Title      string
	Body       string
	BaseBranch string
	Strategy   string
	State      string
	Limit      int
	Draft      bool
	Confirm    bool
	JSON       bool
}

func ParsePRArgs(args []string) (PROptions, error) {
	if len(args) == 0 {
		return PROptions{}, fmt.Errorf("usage: az pr <list|status|checks|open|create|merge> [arguments]")
	}
	opts := PROptions{Command: strings.TrimSpace(args[0]), Strategy: "squash", State: "open", Limit: 30, Draft: true}
	fs := flag.NewFlagSet("pr "+opts.Command, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&opts.Project, "project", "", "project id")
	fs.StringVar(&opts.IssueID, "issue", "", "issue id")
	fs.StringVar(&opts.IssueID, "id", "", "issue id")
	fs.StringVar(&opts.Branch, "branch", "", "head branch")
	fs.IntVar(&opts.Number, "number", 0, "pull request number")
	fs.IntVar(&opts.Number, "pr", 0, "pull request number")
	fs.StringVar(&opts.Title, "title", "", "pull request title")
	fs.StringVar(&opts.Body, "body", "", "pull request body")
	fs.StringVar(&opts.BaseBranch, "base", "", "base branch")
	fs.StringVar(&opts.BaseBranch, "base-branch", "", "base branch")
	fs.StringVar(&opts.Strategy, "strategy", opts.Strategy, "merge strategy: squash, rebase, or merge")
	fs.StringVar(&opts.State, "state", opts.State, "PR state for list: open, closed, merged, or all")
	fs.IntVar(&opts.Limit, "limit", opts.Limit, "maximum PRs to list")
	fs.BoolVar(&opts.Draft, "draft", opts.Draft, "create as draft")
	fs.BoolVar(&opts.Confirm, "confirm", false, "confirm merge")
	fs.BoolVar(&opts.JSON, "json", false, "emit JSON")
	if err := fs.Parse(args[1:]); err != nil {
		return PROptions{}, err
	}
	switch opts.Command {
	case "list", "status", "checks", "open", "create", "merge":
	default:
		return PROptions{}, fmt.Errorf("unknown pr command: %s", opts.Command)
	}
	if opts.Command == "list" {
		if unsupported := unsupportedPRListFlags(fs); len(unsupported) > 0 {
			return PROptions{}, fmt.Errorf("az pr list does not accept %s", strings.Join(unsupported, ", "))
		}
		if fs.NArg() > 0 {
			return PROptions{}, fmt.Errorf("az pr list does not accept issue or pull request selectors")
		}
		return opts, nil
	}
	if fs.NArg() > 0 && opts.IssueID == "" && opts.Branch == "" && opts.Number == 0 {
		opts.IssueID = fs.Arg(0)
	}
	return opts, nil
}

func unsupportedPRListFlags(fs *flag.FlagSet) []string {
	allowed := map[string]bool{
		"json":    true,
		"limit":   true,
		"project": true,
		"state":   true,
	}
	var unsupported []string
	fs.Visit(func(f *flag.Flag) {
		if !allowed[f.Name] {
			unsupported = append(unsupported, "--"+f.Name)
		}
	})
	sort.Strings(unsupported)
	return unsupported
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

func ParseWorktreeDeleteArgs(args []string) (WorktreeDeleteOptions, error) {
	opts := WorktreeDeleteOptions{}
	fs := flag.NewFlagSet("worktree delete", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	addIssueProjectFlag(fs, &opts.Project)
	fs.BoolVar(&opts.Force, "force", false, "force worktree removal")
	fs.BoolVar(&opts.DeleteBranch, "delete-branch", false, "delete the associated local branch after removing the worktree")
	fs.BoolVar(&opts.JSON, "json", false, "print JSON output")
	if err := parseWithInterspersedFlags(fs, args); err != nil {
		return WorktreeDeleteOptions{}, err
	}
	if fs.NArg() != 1 {
		return WorktreeDeleteOptions{}, fmt.Errorf("issue id is required")
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
	fs.BoolVar(&opts.ForceBusy, "force-busy", false, "accepted for compatibility; busy sessions restart by default")
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

func ParseSessionCaptureArgs(args []string) (SessionCaptureOptions, error) {
	return parseSessionCaptureArgs(args, false)
}

func ParseOrchestrateCaptureArgs(args []string) (SessionCaptureOptions, error) {
	return parseSessionCaptureArgs(args, true)
}

func parseSessionCaptureArgs(args []string, allowRaw bool) (SessionCaptureOptions, error) {
	opts := SessionCaptureOptions{Lines: 120}
	fs := flag.NewFlagSet("session capture", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	addIssueProjectFlag(fs, &opts.Project)
	fs.StringVar(&opts.IssueID, "issue", "", "issue id")
	fs.StringVar(&opts.IssueID, "id", "", "issue id")
	fs.IntVar(&opts.Lines, "lines", opts.Lines, "number of recent pane lines to capture")
	fs.BoolVar(&opts.JSON, "json", false, "print JSON output")
	if allowRaw {
		fs.BoolVar(&opts.Raw, "raw", false, "print the unparsed pane capture")
	}
	if err := parseWithInterspersedFlags(fs, args); err != nil {
		return SessionCaptureOptions{}, err
	}
	if fs.NArg() > 1 {
		return SessionCaptureOptions{}, fmt.Errorf("usage: az session capture [--project <project-id>] [--lines N] [--json] <issue-id>")
	}
	if fs.NArg() == 1 {
		if strings.TrimSpace(opts.IssueID) != "" {
			return SessionCaptureOptions{}, fmt.Errorf("usage: az session capture [--project <project-id>] [--lines N] [--json] <issue-id>")
		}
		opts.IssueID = strings.TrimSpace(fs.Arg(0))
	}
	if strings.TrimSpace(opts.IssueID) == "" {
		return SessionCaptureOptions{}, fmt.Errorf("usage: az session capture [--project <project-id>] [--lines N] [--json] <issue-id>")
	}
	if opts.Lines < 0 {
		return SessionCaptureOptions{}, fmt.Errorf("--lines must be greater than or equal to zero")
	}
	opts.Project = strings.TrimSpace(opts.Project)
	opts.IssueID = strings.TrimSpace(opts.IssueID)
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
	Project string
	Target  string
}

type BranchMergeToBaseOptions struct {
	IssueID           string
	Project           string
	Target            string
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

type OperationQueueOptions struct {
	OperationListOptions
	Tree bool
}

type OperationCancelOptions struct {
	OperationID  string
	Reason       string
	JSON         bool
	Wait         bool
	PollInterval time.Duration
}

func NewDependencies(cfg *config.Config) (*Dependencies, error) {
	return NewDependenciesWithContext(context.Background(), cfg)
}

func NewDependenciesWithContext(ctx context.Context, cfg *config.Config) (*Dependencies, error) {
	repoDir, err := os.Getwd()
	if err != nil {
		return nil, fmt.Errorf("failed to get current directory: %w", err)
	}
	return NewDependenciesAtWithContext(ctx, cfg, repoDir)
}

func NewDependenciesAt(cfg *config.Config, repoDir string) (*Dependencies, error) {
	return NewDependenciesAtWithContext(context.Background(), cfg, repoDir)
}

func NewDependenciesAtWithContext(ctx context.Context, cfg *config.Config, repoDir string) (*Dependencies, error) {
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

	rootRepoDir, err := config.ResolveProjectRootContext(ctx, absRepoDir)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve project root from %q: %w", absRepoDir, err)
	}
	runtimeRepoDir := rootRepoDir
	if config.UseScopedDaemonRuntimeFor(absRepoDir) {
		if worktreeRoot, err := config.ResolveWorktreeRootContext(ctx, absRepoDir); err == nil && strings.TrimSpace(worktreeRoot) != "" {
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

func WorktreeDeleteCommand(deps *Dependencies, opts WorktreeDeleteOptions) error {
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

	result, err := deps.DaemonClient.RemoveWorktreeWithOptions(ctx, target.IssueID, daemonclient.WorktreeRemoveOptions{
		Force:        opts.Force,
		DeleteBranch: opts.DeleteBranch,
	})
	if err != nil {
		wrappedErr := fmt.Errorf("failed to delete worktree for %s: %w", target.IssueID, err)
		if opts.JSON {
			if printErr := printJSON(map[string]any{
				"ok":            false,
				"project_id":    target.ProjectID,
				"issue_id":      target.IssueID,
				"delete_branch": opts.DeleteBranch,
				"error":         wrappedErr.Error(),
			}); printErr != nil {
				return fmt.Errorf("%w (also failed to write JSON error response: %v)", wrappedErr, printErr)
			}
		}
		return wrappedErr
	}

	if opts.JSON {
		return printJSON(map[string]any{
			"project_id":       target.ProjectID,
			"issue_id":         target.IssueID,
			"worktree_deleted": true,
			"branch":           result.Branch,
			"branch_deleted":   result.BranchDeleted,
			"forced":           opts.Force,
		})
	}

	fmt.Printf("Worktree deleted for %s\n", target.IssueID)
	if result.BranchDeleted {
		fmt.Printf("Branch deleted: %s\n", result.Branch)
	} else if opts.DeleteBranch {
		fmt.Println("Branch delete requested, but no branch was reported.")
	} else if strings.TrimSpace(result.Branch) != "" {
		fmt.Printf("Branch preserved: %s\n", result.Branch)
	}
	return nil
}

func PRCommand(deps *Dependencies, opts PROptions) error {
	ctx, cancel := context.WithTimeout(context.Background(), prCommandTimeout)
	defer cancel()
	if err := ensureDaemon(ctx, deps, "cli"); err != nil {
		return err
	}
	if project := strings.TrimSpace(opts.Project); project != "" {
		restoreProject := applyIssueProjectOverride(deps, project)
		defer restoreProject()
	}

	switch opts.Command {
	case "list":
		result, err := deps.DaemonClient.ListPullRequests(ctx, daemonclient.PullRequestListParams{
			State: strings.TrimSpace(opts.State),
			Limit: opts.Limit,
		})
		if err != nil {
			return fmt.Errorf("list pull requests: %w", err)
		}
		if opts.JSON {
			return printJSON(result)
		}
		printPullRequestList(result)
		return nil
	case "status":
		ref, err := resolvePRRef(ctx, deps, opts)
		if err != nil {
			return err
		}
		status, err := deps.DaemonClient.GetPullRequest(ctx, daemonclient.PullRequestBranchParams{Branch: ref})
		if err != nil {
			return fmt.Errorf("get pull request: %w", err)
		}
		checks, checksErr := deps.DaemonClient.GetPullRequestChecks(ctx, daemonclient.PullRequestChecksParams{Ref: ref})
		if opts.JSON {
			payload := map[string]any{"pull_request": status.PullRequest}
			if checksErr == nil {
				payload["checks_status"] = checks.ChecksStatus
				payload["checks"] = checks.Checks
			}
			return printJSON(payload)
		}
		printPullRequestStatus(status, checks.ChecksStatus, checksErr)
		return nil
	case "checks":
		ref, err := resolvePRRef(ctx, deps, opts)
		if err != nil {
			return err
		}
		checks, err := deps.DaemonClient.GetPullRequestChecks(ctx, daemonclient.PullRequestChecksParams{Ref: ref})
		if err != nil {
			return fmt.Errorf("get pull request checks: %w", err)
		}
		if opts.JSON {
			return printJSON(checks)
		}
		printPullRequestChecks(checks)
		return nil
	case "open":
		ref, err := resolvePRRef(ctx, deps, opts)
		if err != nil {
			return err
		}
		if err := deps.DaemonClient.OpenPullRequest(ctx, daemonclient.PullRequestBranchParams{Branch: ref}); err != nil {
			return fmt.Errorf("open pull request: %w", err)
		}
		if opts.JSON {
			return printJSON(map[string]string{"ref": ref, "status": "opened"})
		}
		fmt.Printf("Opened PR for %s\n", ref)
		return nil
	case "create":
		return prCreateCommand(ctx, deps, opts)
	case "merge":
		if !opts.Confirm {
			return fmt.Errorf("refusing to merge PR without --confirm")
		}
		branch := strings.TrimSpace(opts.Branch)
		var err error
		if branch == "" && opts.Number == 0 {
			branch, err = resolvePRBranch(ctx, deps, opts)
			if err != nil {
				return err
			}
		}
		result, err := deps.DaemonClient.MergePullRequest(ctx, daemonclient.PullRequestMergeParams{
			Branch:   branch,
			Number:   opts.Number,
			Strategy: opts.Strategy,
		})
		if err != nil {
			return fmt.Errorf("merge pull request: %w", err)
		}
		if opts.JSON {
			return printJSON(result)
		}
		fmt.Printf("Merged PR #%d with %s\n", result.Number, result.Strategy)
		return nil
	default:
		return fmt.Errorf("unknown pr command: %s", opts.Command)
	}
}

func prCreateCommand(ctx context.Context, deps *Dependencies, opts PROptions) error {
	if opts.Number > 0 {
		return fmt.Errorf("pr create does not accept pull request number selectors")
	}
	branch, err := resolvePRBranch(ctx, deps, opts)
	if err != nil {
		return err
	}
	issueID := strings.TrimSpace(opts.IssueID)
	if issueID == "" {
		return fmt.Errorf("issue id is required for pr create")
	}
	title := strings.TrimSpace(opts.Title)
	if title == "" {
		title = issueID
		if task, _, ok, err := loadIssueMetadataTask(ctx, deps, issueID); err == nil && ok && strings.TrimSpace(task.Title) != "" {
			title = strings.TrimSpace(task.Title)
		}
	}
	body := strings.TrimSpace(opts.Body)
	if body == "" {
		body = fmt.Sprintf("Created by az for %s.", issueID)
	}
	baseBranch := strings.TrimSpace(opts.BaseBranch)
	if baseBranch == "" && deps.Config != nil {
		baseBranch = strings.TrimSpace(deps.Config.Git.BaseBranch)
	}
	if baseBranch == "" {
		baseBranch = "main"
	}
	result, err := deps.DaemonClient.CreatePullRequest(ctx, daemonclient.CreatePullRequestParams{
		Title:      title,
		Body:       body,
		Branch:     branch,
		BaseBranch: baseBranch,
		Draft:      opts.Draft,
		IssueID:    issueID,
	})
	if err != nil {
		return fmt.Errorf("create pull request: %w", err)
	}
	if opts.JSON {
		return printJSON(result)
	}
	fmt.Printf("Created PR #%d: %s\n", result.PullRequest.Number, result.PullRequest.URL)
	return nil
}

func resolvePRRef(ctx context.Context, deps *Dependencies, opts PROptions) (string, error) {
	if opts.Number > 0 {
		return strconv.Itoa(opts.Number), nil
	}
	return resolvePRBranch(ctx, deps, opts)
}

func resolvePRBranch(ctx context.Context, deps *Dependencies, opts PROptions) (string, error) {
	if branch := strings.TrimSpace(opts.Branch); branch != "" {
		return branch, nil
	}
	issueID := strings.TrimSpace(opts.IssueID)
	if issueID == "" {
		return "", fmt.Errorf("issue id or --branch is required")
	}
	worktree, err := resolveMergeToBaseSourceWorktree(ctx, deps, issueID)
	if err != nil {
		return "", err
	}
	if branch := strings.TrimSpace(worktree.Branch); branch != "" {
		return branch, nil
	}
	return "", fmt.Errorf("worktree for %s has no branch", issueID)
}

func printPullRequestStatus(result daemonclient.PullRequestGetResult, checksStatus string, checksErr error) {
	pr := result.PullRequest
	fmt.Printf("PR #%d: %s\n", pr.Number, pr.Title)
	fmt.Printf("State: %s", strings.ToLower(strings.TrimSpace(pr.State)))
	if pr.Draft {
		fmt.Print(" (draft)")
	}
	fmt.Println()
	fmt.Printf("Branch: %s -> %s\n", pr.Branch, pr.BaseRef)
	if pr.MergeStateStatus != "" || pr.ReviewDecision != "" {
		fmt.Printf("Merge: %s  Review: %s\n", pr.MergeStateStatus, pr.ReviewDecision)
	}
	if checksErr != nil {
		fmt.Printf("Checks: unavailable (%v)\n", checksErr)
	} else {
		fmt.Printf("Checks: %s\n", checksStatus)
	}
	fmt.Printf("URL: %s\n", pr.URL)
}

func printPullRequestChecks(result daemonclient.PullRequestChecksResult) {
	fmt.Printf("Checks for %s: %s\n", result.Ref, result.ChecksStatus)
	for _, check := range result.Checks {
		name := strings.TrimSpace(check.Name)
		if name == "" {
			name = "(unnamed)"
		}
		state := strings.TrimSpace(check.Bucket)
		if state == "" {
			state = strings.TrimSpace(check.State)
		}
		fmt.Printf("- %s: %s\n", name, state)
	}
}

func printPullRequestList(result daemonclient.PullRequestListResult) {
	if len(result.PullRequests) == 0 {
		fmt.Printf("No %s pull requests found.\n", strings.TrimSpace(result.State))
		return
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "NUMBER\tSTATE\tDRAFT\tBRANCH\tBASE\tTITLE")
	for _, pr := range result.PullRequests {
		draft := ""
		if pr.Draft {
			draft = "yes"
		}
		fmt.Fprintf(w, "#%d\t%s\t%s\t%s\t%s\t%s\n",
			pr.Number,
			strings.ToLower(strings.TrimSpace(pr.State)),
			draft,
			strings.TrimSpace(pr.Branch),
			strings.TrimSpace(pr.BaseRef),
			strings.TrimSpace(pr.Title),
		)
	}
	_ = w.Flush()
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

func SessionCaptureCommand(deps *Dependencies, opts SessionCaptureOptions) error {
	result, err := captureSessionPane(deps, opts, "capturing session pane")
	if err != nil {
		return err
	}
	if opts.JSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(result)
	}
	fmt.Print(result.Output)
	if result.Output != "" && !strings.HasSuffix(result.Output, "\n") {
		fmt.Println()
	}
	return nil
}

func captureSessionPane(deps *Dependencies, opts SessionCaptureOptions, logMessage string) (protocol.SessionCaptureResponseBody, error) {
	ctx, cancel := context.WithTimeout(context.Background(), daemonCommandTimeout)
	defer cancel()
	if err := ensureDaemon(ctx, deps, "cli"); err != nil {
		return protocol.SessionCaptureResponseBody{}, err
	}

	var target sessionIssueTarget
	var err error
	if strings.TrimSpace(opts.Project) != "" {
		target, err = resolveExplicitSessionStatusTarget(deps, opts.IssueID, opts.Project, opts.IssueID)
	} else {
		target, err = resolveSessionStatusTarget(deps, opts.IssueID)
	}
	if err != nil {
		return protocol.SessionCaptureResponseBody{}, err
	}
	restoreProject := applyIssueProjectOverride(deps, target.ProjectID)
	defer restoreProject()

	deps.Logger.Info(logMessage, "project_id", target.ProjectID, "issue_id", target.IssueID, "lines", opts.Lines, "raw", opts.Raw)
	result, err := deps.DaemonClient.CaptureSession(ctx, target.IssueID, daemonclient.CaptureSessionOptions{Lines: opts.Lines})
	if err != nil {
		return protocol.SessionCaptureResponseBody{}, fmt.Errorf("failed to capture session pane: %w", err)
	}
	return result, nil
}

func OrchestrateCaptureCommand(deps *Dependencies, opts SessionCaptureOptions) error {
	return orchestrateCaptureCommand(deps, opts)
}

func SessionDiagnoseCommand(deps *Dependencies, issueID string) error {
	ctx, cancel := context.WithTimeout(context.Background(), daemonCommandTimeout)
	defer cancel()
	if err := ensureDaemon(ctx, deps, "cli"); err != nil {
		return err
	}

	target, err := resolveSessionStatusTarget(deps, issueID)
	if err != nil {
		return err
	}
	restoreProject := applyIssueProjectOverride(deps, target.ProjectID)
	defer restoreProject()

	fmt.Printf("Session diagnose: %s\n", target.IssueID)
	fmt.Printf("Project: %s\n", target.ProjectID)
	fmt.Printf("Repo: %s\n\n", target.RepoDir)

	fmt.Println("Session status:")
	statusResp, statusErr := deps.DaemonClient.Command(ctx, newSessionRequest(commandSessionStatus, target.ProjectID, target.IssueID, ""))
	if statusErr != nil {
		fmt.Printf("  unavailable: %v\n", statusErr)
	} else if statusResp.Error != nil {
		fmt.Printf("  %s\n", statusResp.Error.Message)
	} else if err := printCommandOutput(statusResp); err != nil {
		fmt.Printf("  decode failed: %v\n", err)
	}

	fmt.Println("\nWorktree:")
	printSessionDiagnoseWorktree(ctx, deps, target.IssueID)

	fmt.Println("\nRecent session.start operations:")
	printSessionDiagnoseOperations(ctx, deps, target.IssueID)

	fmt.Println("\nAI hook status:")
	printSessionDiagnoseAIStatus(deps, target.RepoDir)

	fmt.Println("\nLogs:")
	logDir := resolveSessionLogDirFor(deps.Config, target.RepoDir)
	fmt.Printf("  daemon: %s\n", filepath.Join(logDir, logging.DaemonLogFileName))
	fmt.Printf("  cli: %s\n", filepath.Join(logDir, logging.CLILogFileName))
	return nil
}

func printSessionDiagnoseWorktree(ctx context.Context, deps *Dependencies, issueID string) {
	worktrees, err := deps.DaemonClient.ListWorktrees(ctx)
	if err != nil {
		fmt.Printf("  unavailable: %v\n", err)
		return
	}
	for _, worktree := range worktrees {
		if naming.IssueIDsEqual(worktree.IssueID, issueID) {
			fmt.Printf("  path: %s\n", worktree.Path)
			fmt.Printf("  branch: %s\n", worktree.Branch)
			return
		}
	}
	fmt.Println("  not found")
}

func printSessionDiagnoseOperations(ctx context.Context, deps *Dependencies, issueID string) {
	records, err := deps.DaemonClient.ListOperations(ctx, daemonclient.OperationListOptions{
		IssueID: issueID,
		Kind:    commandSessionStart,
		Limit:   5,
	})
	if err != nil {
		fmt.Printf("  unavailable: %v\n", err)
		return
	}
	if len(records) == 0 {
		fmt.Println("  none")
		return
	}
	for _, record := range records {
		line := fmt.Sprintf("  %s %s", record.OperationID, record.State)
		if record.Progress != nil {
			if phase := strings.TrimSpace(record.Progress.Phase); phase != "" {
				line += " phase=" + phase
			}
			if message := strings.TrimSpace(record.Progress.Message); message != "" {
				line += " progress=" + message
			}
		}
		fmt.Println(line)
		if record.Error != nil && strings.TrimSpace(record.Error.Message) != "" {
			fmt.Printf("    error: %s\n", compactSingleLine(record.Error.Message, 500))
		}
	}
}

func printSessionDiagnoseAIStatus(deps *Dependencies, repoDir string) {
	result, err := AIStatus(deps, AIStatusOptions{
		Targets:    []AgentInstallTarget{AgentInstallTargetAuto},
		ProjectDir: repoDir,
	})
	if err != nil {
		fmt.Printf("  unavailable: %v\n", err)
		return
	}
	if len(result.Targets) == 0 {
		fmt.Println("  no hook targets detected")
		return
	}
	for _, target := range result.Targets {
		state := "missing"
		if target.Installed {
			state = "installed"
		}
		fmt.Printf("  %s: %s", target.Target, state)
		if target.Path != "" {
			fmt.Printf(" (%s)", target.Path)
		}
		fmt.Println()
		if target.Reason != "" {
			fmt.Printf("    reason: %s\n", target.Reason)
		}
	}
}

func compactSingleLine(value string, limit int) string {
	value = strings.Join(strings.Fields(strings.TrimSpace(value)), " ")
	if limit <= 0 || len(value) <= limit {
		return value
	}
	if limit <= 3 {
		return value[:limit]
	}
	return value[:limit-3] + "..."
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
	if projectCount := sessionRestartAllProjectCount(result); projectCount > 0 {
		fmt.Printf(" across %d project(s)", projectCount)
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
		projectID := strings.TrimSpace(session.ProjectID.String())
		if projectID == "" {
			projectID = strings.TrimSpace(result.ProjectID.String())
		}
		if projectID != "" {
			fmt.Printf("- %s (project=%s, session=%s, activity=%s, source=%s, tmux_ready=%t, active_intent=%t): %s\n", issueID, projectID, session.SessionID, session.Activity, session.ActivitySource, session.TmuxReady, session.ActiveIntent, status)
			continue
		}
		fmt.Printf("- %s (session=%s, activity=%s, source=%s, tmux_ready=%t, active_intent=%t): %s\n", issueID, session.SessionID, session.Activity, session.ActivitySource, session.TmuxReady, session.ActiveIntent, status)
	}
}

func sessionRestartAllProjectCount(result protocol.SessionRestartAllResponseBody) int {
	seen := map[string]struct{}{}
	for _, projectID := range result.ProjectIDs {
		projectID := strings.TrimSpace(projectID.String())
		if projectID == "" {
			continue
		}
		seen[projectID] = struct{}{}
	}
	for _, session := range result.Sessions {
		projectID := strings.TrimSpace(session.ProjectID.String())
		if projectID == "" {
			continue
		}
		seen[projectID] = struct{}{}
	}
	if len(seen) > 0 {
		return len(seen)
	}
	if strings.TrimSpace(result.ProjectID.String()) != "" {
		return 1
	}
	return 0
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
	restoreProject := applyIssueProjectOverride(deps, opts.Project)
	defer restoreProject()

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
		latencytrace.LogPhaseContext(ctx, deps.Logger, "cli", "branch.merge."+name, startedAt, "issue_id", opts.IssueID)
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
	target, decision, err := resolveExplicitBranchMergeTarget(ctx, deps, source, opts)
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
	fmt.Printf("Merge plan: source %s (%s) -> target %s (%s)\n", source.IssueID, source.Branch, target.TargetID, baseBranch)

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

func resolveExplicitBranchMergeTarget(ctx context.Context, deps *Dependencies, source daemonclient.Worktree, opts BranchMergeToBaseOptions) (mergeBaseTarget, mergeTargetDecision, error) {
	targetID := strings.TrimSpace(opts.Target)
	if targetID == "" {
		return resolveMergeToBaseTarget(ctx, deps, source.IssueID, opts.AllowBaseForChild)
	}
	if isBaseMergeTarget(targetID) {
		defaultBase := resolveCLIBaseBranch(deps.Config)
		targetResult, err := deps.DaemonClient.TaskMergeBaseTarget(ctx, source.IssueID, defaultBase, opts.AllowBaseForChild, true)
		if err != nil {
			return mergeBaseTarget{}, mergeTargetDecision{}, err
		}
		target := mergeBaseTarget{TargetID: targetResult.TargetID, Branch: targetResult.Branch, WorktreePath: targetResult.WorktreePath, BranchAttached: targetResult.BranchAttached}
		decision := mergeTargetDecision{Reason: targetResult.Reason, AncestorChain: append([]string(nil), targetResult.AncestorChain...)}
		if target.TargetID != "base" {
			return mergeBaseTarget{}, mergeTargetDecision{}, fmt.Errorf("refusing target base for %s: daemon selected active ancestor %s; use --target %s", source.IssueID, target.TargetID, target.TargetID)
		}
		return target, decision, nil
	}
	targetWorktree, err := resolveWorktreeForIssue(ctx, deps, targetID)
	if err != nil {
		return mergeBaseTarget{}, mergeTargetDecision{}, err
	}
	if naming.IssueIDsEqual(source.IssueID, targetWorktree.IssueID) {
		return mergeBaseTarget{}, mergeTargetDecision{}, fmt.Errorf("source and target issue must be different: %s", source.IssueID)
	}
	if strings.TrimSpace(targetWorktree.Branch) == "" {
		return mergeBaseTarget{}, mergeTargetDecision{}, fmt.Errorf("target issue %s has no branch", targetWorktree.IssueID)
	}
	return mergeBaseTarget{
		TargetID:       targetWorktree.IssueID,
		Branch:         targetWorktree.Branch,
		WorktreePath:   targetWorktree.Path,
		BranchAttached: true,
	}, mergeTargetDecision{Reason: "selected explicit issue target"}, nil
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
	restoreProject := applyIssueProjectOverride(deps, opts.Project)
	defer restoreProject()

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
			fmt.Printf("Run: az branch merge --source %s --target %s\n", source.IssueID, targetID)
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
		fmt.Fprintf(&b, "This preflight blocked merging source branch %s into base ref %s. Work in source issue %s at %s: merge %s into %s, resolve conflicts there, and leave the source branch clean so `az branch merge --source %s --target base` can be retried after durable human acceptance.\n", source.Branch, targetRef, source.IssueID, source.Path, targetRef, source.Branch, source.IssueID)
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
	return daemonclient.Worktree{}, fmt.Errorf("could not infer issue from current worktree %q; pass an explicit source: az branch merge --source <issue-id> --target <issue-id|base>", absCWD)
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

func OperationQueueCommand(deps *Dependencies, opts OperationQueueOptions) error {
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
	snapshot, err := deps.DaemonClient.OperationQueue(ctx, daemonclient.OperationListOptions{
		IssueID: issueID,
		Kind:    opts.Kind,
		States:  opts.States,
		Limit:   opts.Limit,
	})
	if err != nil {
		return fmt.Errorf("failed to inspect operation queue: %w", err)
	}
	return printOperationQueue(snapshot, opts.JSON, opts.Tree)
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

func ParseOperationQueueArgs(args []string) (OperationQueueOptions, error) {
	opts := OperationQueueOptions{
		OperationListOptions: OperationListOptions{Limit: defaultOperationListLimit},
	}
	stateInputs := make([]string, 0, 4)
	statesCSV := ""
	fs := flag.NewFlagSet("operation queue", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.BoolVar(&opts.JSON, "json", false, "output operation queue as JSON")
	fs.BoolVar(&opts.Tree, "tree", false, "render queued operations under their blocking operations")
	fs.StringVar(&opts.IssueID, "issue", "", "filter by issue id")
	fs.StringVar(&opts.Kind, "kind", "", "filter by operation kind")
	fs.IntVar(&opts.Limit, "limit", defaultOperationListLimit, "maximum operations to return")
	fs.Func("state", "restrict to a specific operation state (repeatable)", func(v string) error {
		stateInputs = append(stateInputs, v)
		return nil
	})
	fs.StringVar(&statesCSV, "states", "", "comma-separated operation states")
	if err := fs.Parse(args); err != nil {
		return OperationQueueOptions{}, err
	}
	if fs.NArg() != 0 {
		return OperationQueueOptions{}, fmt.Errorf("unexpected argument: %s", fs.Arg(0))
	}
	if opts.Limit < 1 {
		return OperationQueueOptions{}, fmt.Errorf("limit must be >= 1")
	}
	if strings.TrimSpace(statesCSV) != "" {
		stateInputs = append(stateInputs, strings.Split(statesCSV, ",")...)
	}
	states, err := parseOperationStates(stateInputs)
	if err != nil {
		return OperationQueueOptions{}, err
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
	if repoDir := sessionProjectCandidateRepoDir(deps.TraceContext, candidate); repoDir != "" {
		deps.RepoDir = repoDir
		deps.RuntimeRepoDir = resolveRuntimeRepoDir(deps.TraceContext, repoDir)
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

func sessionProjectCandidateRepoDir(ctx context.Context, candidate sessionProjectCandidate) string {
	path := strings.TrimSpace(candidate.Path)
	if path == "" {
		return ""
	}
	if repoDir, err := config.ResolveProjectRootContext(ctx, path); err == nil && strings.TrimSpace(repoDir) != "" {
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
	fs.StringVar(&opts.Archived, "archived", "", "archived issue visibility: exclude, include, or only")
	fs.StringVar(&createdAfterRaw, "created-after", "", "include issues created at or after date/time")
	fs.StringVar(&createdBeforeRaw, "created-before", "", "include issues created at or before date/time")
	fs.StringVar(&updatedAfterRaw, "updated-after", "", "include issues updated at or after date/time")
	fs.StringVar(&updatedBeforeRaw, "updated-before", "", "include issues updated at or before date/time")
	addStatusInput := func(v string) error {
		stateInputs = append(stateInputs, v)
		return nil
	}
	fs.Func("status", "restrict to a specific issue display status or phase (repeatable)", addStatusInput)
	fs.Func("state", "deprecated alias for --status", addStatusInput)
	fs.StringVar(&statesCSV, "statuses", "", "comma-separated issue display statuses or phases")
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
	if strings.TrimSpace(opts.Archived) != "" {
		archiveMode, archiveErr := protocol.NormalizeArchiveMode(opts.Archived)
		if archiveErr != nil {
			return IssueListOptions{}, archiveErr
		}
		opts.Archived = string(archiveMode)
	}
	if allowQueryArgs && opts.Query == "" {
		return IssueListOptions{}, fmt.Errorf("usage: az ticket search [--project <project-id>] [--json] [--deps] [--archived exclude|include|only] [--created-after YYYY-MM-DD] [--created-before YYYY-MM-DD] [--updated-after YYYY-MM-DD] [--updated-before YYYY-MM-DD] [--status <status> ...] [--statuses a,b,c] [--limit N] [--id <id> ...] [--ids a,b,c] [--parent <id> ...] [--parents a,b,c] [--depends-on <id> ...] [--depends-on-ids a,b,c] (--query <text>|-q <text>|<query>)")
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
	fs.StringVar(&opts.Archived, "archived", "", "archived issue visibility: exclude, include, or only")
	fs.StringVar(&issueIDFlag, "id", "", "issue id (named alternative to positional)")
	if err := parseWithInterspersedFlags(fs, args); err != nil {
		return IssueGetOptions{}, err
	}
	if fs.NArg() > 1 {
		return IssueGetOptions{}, fmt.Errorf("usage: az ticket get [--project <project-id>] [--id <ticket-id>] [--json] [--with-notes] [<ticket-id>]")
	}
	if fs.NArg() == 1 {
		opts.IssueID = fs.Arg(0)
	}
	if strings.TrimSpace(issueIDFlag) != "" {
		opts.IssueID = strings.TrimSpace(issueIDFlag)
	}
	if strings.TrimSpace(opts.IssueID) == "" {
		return IssueGetOptions{}, fmt.Errorf("usage: az ticket get [--project <project-id>] [--id <ticket-id>] [--json] [--with-notes] [--archived exclude|include|only] [<ticket-id>]")
	}
	if strings.TrimSpace(opts.Archived) != "" {
		archiveMode, archiveErr := protocol.NormalizeArchiveMode(opts.Archived)
		if archiveErr != nil {
			return IssueGetOptions{}, archiveErr
		}
		opts.Archived = string(archiveMode)
	}
	opts.Project = normalizeIssueProject(opts.Project)
	return opts, nil
}

func ParseIssueOwnershipArgs(args []string, command string) (IssueOwnershipOptions, error) {
	opts := IssueOwnershipOptions{}
	issueIDFlag := ""
	fs := flag.NewFlagSet("issue "+command, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	addIssueProjectFlag(fs, &opts.Project)
	fs.BoolVar(&opts.JSON, "json", false, "output updated issue as JSON")
	fs.StringVar(&issueIDFlag, "id", "", "issue id (named alternative to positional)")
	fs.StringVar(&opts.OwnerID, "owner", "", "owner id; defaults to current actor")
	fs.StringVar(&opts.OwnerKind, "kind", "agent", "owner kind: human, agent, or orchestrator")
	fs.StringVar(&opts.TTL, "ttl", "", "optional lease duration, for example 2h")
	fs.BoolVar(&opts.Force, "force", false, "take over or release another active owner")
	if err := parseWithInterspersedFlags(fs, args); err != nil {
		return IssueOwnershipOptions{}, err
	}
	if fs.NArg() > 1 {
		return IssueOwnershipOptions{}, fmt.Errorf("usage: az ticket %s [--project <project-id>] [--id <ticket-id>] [--owner <owner-id>] [--kind human|agent|orchestrator] [--ttl 2h] [--force] [--json] [<ticket-id>]", command)
	}
	if fs.NArg() == 1 {
		opts.IssueID = fs.Arg(0)
	}
	if strings.TrimSpace(issueIDFlag) != "" {
		opts.IssueID = strings.TrimSpace(issueIDFlag)
	}
	if strings.TrimSpace(opts.IssueID) == "" {
		return IssueOwnershipOptions{}, fmt.Errorf("usage: az ticket %s [--project <project-id>] [--id <ticket-id>] [--owner <owner-id>] [--kind human|agent|orchestrator] [--ttl 2h] [--force] [--json] [<ticket-id>]", command)
	}
	opts.Project = normalizeIssueProject(opts.Project)
	opts.OwnerID = strings.TrimSpace(opts.OwnerID)
	opts.OwnerKind = strings.TrimSpace(opts.OwnerKind)
	opts.TTL = strings.TrimSpace(opts.TTL)
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
	fs.BoolVar(&opts.JQHelp, "jq-help", false, "print jq examples for issue event JSON")
	fs.StringVar(&issueIDFlag, "id", "", "issue id (named alternative to positional)")
	fs.Var(&typeFlags, "type", "filter by event type; may be repeated")
	fs.StringVar(&typesCSV, "types", "", "comma-separated event types")
	fs.IntVar(&opts.Limit, "limit", 0, "maximum events to return")
	if err := parseWithInterspersedFlags(fs, args); err != nil {
		return IssueEventsOptions{}, err
	}
	if fs.NArg() > 1 {
		return IssueEventsOptions{}, fmt.Errorf("usage: az ticket events [--project <project-id>] [--id <ticket-id>] [--json] [--jq-help] [--type <event-type> ...] [--types a,b] [--limit N] [<ticket-id>]")
	}
	if fs.NArg() == 1 {
		opts.IssueID = fs.Arg(0)
	}
	if strings.TrimSpace(issueIDFlag) != "" {
		opts.IssueID = strings.TrimSpace(issueIDFlag)
	}
	if strings.TrimSpace(opts.IssueID) == "" && !opts.JQHelp {
		return IssueEventsOptions{}, fmt.Errorf("usage: az ticket events [--project <project-id>] [--id <ticket-id>] [--json] [--jq-help] [--type <event-type> ...] [--types a,b] [--limit N] [<ticket-id>]")
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

func ParseIssueRecordArgs(args []string) (IssueRecordOptions, error) {
	opts := IssueRecordOptions{EventType: string(domain.IssueEventProgressRecorded)}
	issueIDFlag := ""
	var followUpFlags repeatedStringFlag
	fs := flag.NewFlagSet("issue record", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	addIssueProjectFlag(fs, &opts.Project)
	fs.StringVar(&issueIDFlag, "id", "", "issue id (named alternative to positional)")
	fs.StringVar(&opts.EventType, "type", opts.EventType, "observation event type")
	fs.StringVar(&opts.Summary, "summary", "", "short human-readable summary")
	fs.StringVar(&opts.Body, "body", "", "longer evidence body")
	fs.StringVar(&opts.DataJSON, "data", "", "JSON object payload to merge into the event")
	fs.StringVar(&opts.Source, "source", "agent", "event source")
	fs.StringVar(&opts.OperationID, "operation", "", "operation id")
	fs.StringVar(&opts.SessionID, "session", "", "session id")
	fs.StringVar(&opts.WorktreePath, "worktree", "", "worktree path")
	fs.Var(&followUpFlags, "follow-up", "follow-up issue id; may be repeated")
	fs.BoolVar(&opts.JSON, "json", false, "output recorded event as JSON")
	if err := parseWithInterspersedFlags(fs, args); err != nil {
		return IssueRecordOptions{}, err
	}
	if fs.NArg() > 1 {
		return IssueRecordOptions{}, fmt.Errorf("usage: az ticket record [--project <project-id>] [--id <ticket-id>] [--type <event-type>] [--summary <text>] [--body <text>] [--data <json-object>] [--follow-up <ticket-id> ...] [--json] [<ticket-id>]")
	}
	if fs.NArg() == 1 {
		opts.IssueID = fs.Arg(0)
	}
	if strings.TrimSpace(issueIDFlag) != "" {
		opts.IssueID = strings.TrimSpace(issueIDFlag)
	}
	typeExplicit := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "type" {
			typeExplicit = true
		}
	})
	opts.FollowUpIssueIDs = dedupeOrderedIDs([]string(followUpFlags))
	if len(opts.FollowUpIssueIDs) > 0 && !typeExplicit {
		opts.EventType = string(domain.IssueEventFollowupCreated)
	}
	opts.Project = normalizeIssueProject(opts.Project)
	opts.EventType = strings.TrimSpace(opts.EventType)
	opts.Summary = strings.TrimSpace(opts.Summary)
	opts.Body = strings.TrimSpace(opts.Body)
	opts.DataJSON = strings.TrimSpace(opts.DataJSON)
	opts.Source = strings.TrimSpace(opts.Source)
	opts.OperationID = strings.TrimSpace(opts.OperationID)
	opts.SessionID = strings.TrimSpace(opts.SessionID)
	opts.WorktreePath = strings.TrimSpace(opts.WorktreePath)
	if opts.EventType == "" {
		return IssueRecordOptions{}, fmt.Errorf("--type is required")
	}
	if opts.Summary == "" && opts.Body == "" && opts.DataJSON == "" && len(opts.FollowUpIssueIDs) == 0 {
		return IssueRecordOptions{}, fmt.Errorf("at least one of --summary, --body, --data, or --follow-up is required")
	}
	return opts, nil
}

func ParseIssueContextRiskArgs(args []string) (IssueContextRiskOptions, error) {
	opts := IssueContextRiskOptions{}
	issueIDFlag := ""
	sinceRaw := fmt.Sprintf("%dd", defaultIssueContextRiskDays)
	fs := flag.NewFlagSet("issue context-risk", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	addIssueProjectFlag(fs, &opts.Project)
	fs.BoolVar(&opts.JSON, "json", false, "output context risk packet as JSON")
	fs.BoolVar(&opts.Summary, "summary", false, "output bounded closeout summary")
	fs.BoolVar(&opts.Full, "full", false, "output full context risk packet including all evidence")
	fs.StringVar(&issueIDFlag, "id", "", "issue id (named alternative to positional)")
	fs.StringVar(&sinceRaw, "since", sinceRaw, "recent window (for example 14d, 2w, 72h)")
	if err := parseWithInterspersedFlags(fs, args); err != nil {
		return IssueContextRiskOptions{}, err
	}
	if fs.NArg() > 1 {
		return IssueContextRiskOptions{}, fmt.Errorf("usage: az ticket context-risk [--project <project-id>] [--id <ticket-id>] [--since 14d] [--summary|--full] [--json] [<ticket-id>]")
	}
	if opts.Summary && opts.Full {
		return IssueContextRiskOptions{}, fmt.Errorf("--summary and --full are mutually exclusive")
	}
	if fs.NArg() == 1 {
		opts.IssueID = fs.Arg(0)
	}
	if strings.TrimSpace(issueIDFlag) != "" {
		opts.IssueID = strings.TrimSpace(issueIDFlag)
	}
	if strings.TrimSpace(opts.IssueID) == "" {
		return IssueContextRiskOptions{}, fmt.Errorf("usage: az ticket context-risk [--project <project-id>] [--id <ticket-id>] [--since 14d] [--summary|--full] [--json] [<ticket-id>]")
	}
	duration, err := parseIssueContextRiskWindow(sinceRaw)
	if err != nil {
		return IssueContextRiskOptions{}, fmt.Errorf("invalid --since: %w", err)
	}
	opts.Since = time.Now().UTC().Add(-duration)
	opts.Project = normalizeIssueProject(opts.Project)
	return opts, nil
}

func parseIssueContextRiskWindow(raw string) (time.Duration, error) {
	value := strings.TrimSpace(strings.ToLower(raw))
	if value == "" {
		return 0, fmt.Errorf("empty duration")
	}
	if strings.HasSuffix(value, "d") || strings.HasSuffix(value, "w") {
		multiplier := 24 * time.Hour
		if strings.HasSuffix(value, "w") {
			multiplier = 7 * 24 * time.Hour
		}
		number := strings.TrimSpace(value[:len(value)-1])
		n, err := strconv.Atoi(number)
		if err != nil || n <= 0 {
			return 0, fmt.Errorf("must be a positive duration")
		}
		return time.Duration(n) * multiplier, nil
	}
	duration, err := time.ParseDuration(value)
	if err != nil {
		return 0, err
	}
	if duration <= 0 {
		return 0, fmt.Errorf("must be positive")
	}
	return duration, nil
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
		return IssueGetManyOptions{}, fmt.Errorf("usage: az ticket get-many [--project <project-id>] --id <ticket-id> [--id <ticket-id> ...] [--ids a,b,c] [--json] [--with-notes]")
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
	var parentFlag string
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
	fs.StringVar(&parentFlag, "parent", "", "parent issue id for explicit parent-child creation")
	fs.StringVar(&titleFlag, "title", "", "issue title")
	fs.StringVar(&opts.Description, "description", "", "issue description")
	fs.StringVar(&priorityRaw, "priority", "", "issue priority (P0-P4)")
	fs.BoolVar(&opts.Deferred, "deferred", false, "create standalone later/backlog work; skips AZEDARACH_ISSUE_ID auto-parenting and defaults priority to P4 unless --priority is provided")
	fs.BoolVar(&opts.JSON, "json", false, "output issue create result as JSON")
	fs.StringVar(&typeRaw, "type", string(domain.TypeTask), "issue type (task|bug|feature|epic|chore|investigation)")
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
		return IssueCreateOptions{}, fmt.Errorf("usage: az ticket create [--project <project-id>] [--parent <ticket-id>] [--impl <implementation> ...] [--deferred] [--type task|bug|feature|epic|chore|investigation] [--priority P0|P1|P2|P3|P4] [--title text] [--description text] [--json] [<title>]")
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
	if opts.Deferred {
		opts.Lifecycle = domain.IssueWorkflowBacklog
	}
	opts.Type = taskType
	opts.Implementations = dedupeOrderedIDs(impls)
	parentFlag = strings.TrimSpace(parentFlag)
	if parentFlag != "" {
		if opts.Deferred {
			return IssueCreateOptions{}, fmt.Errorf("--parent cannot be used with --deferred; deferred issues are standalone backlog work")
		}
		if _, err := naming.ParseIssueID(parentFlag); err != nil {
			return IssueCreateOptions{}, fmt.Errorf("invalid parent issue id %q: %w", parentFlag, err)
		}
		opts.AutoParentFromIssueID = &parentFlag
		opts.AutoCreatedFromIssueID = &parentFlag
		opts.ExplicitParent = true
	} else if issueID := strings.TrimSpace(os.Getenv("AZEDARACH_ISSUE_ID")); issueID != "" {
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
		return IssueCheckOptions{}, fmt.Errorf("usage: az ticket check [--project <project-id>] [--id <ticket-id>] [--json] [<ticket-id>]")
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
	fs.BoolVar(&opts.CheckpointWAL, "checkpoint-wal", false, "run a safe passive SQLite WAL checkpoint")
	fs.BoolVar(&opts.TruncateWAL, "truncate-wal", false, "run an explicit SQLite WAL truncate checkpoint")
	if err := parseWithInterspersedFlags(fs, args); err != nil {
		return IssueDoctorOptions{}, err
	}
	if fs.NArg() > 1 {
		return IssueDoctorOptions{}, fmt.Errorf("usage: az ticket doctor [--project <project-id>] [--id <ticket-id>] [--checkpoint-wal] [--truncate-wal] [--json] [<ticket-id>]")
	}
	issueID := ""
	if fs.NArg() == 1 {
		issueID = fs.Arg(0)
	}
	if strings.TrimSpace(issueIDFlag) != "" {
		issueID = strings.TrimSpace(issueIDFlag)
	}
	if strings.TrimSpace(issueID) == "" {
		return IssueDoctorOptions{}, fmt.Errorf("usage: az ticket doctor [--project <project-id>] [--id <ticket-id>] [--checkpoint-wal] [--truncate-wal] [--json] [<ticket-id>]")
	}
	if opts.CheckpointWAL && opts.TruncateWAL {
		return IssueDoctorOptions{}, fmt.Errorf("--checkpoint-wal and --truncate-wal are mutually exclusive")
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
		return IssueCloseOptions{}, fmt.Errorf("usage: az ticket close [--project <project-id>] [--id <ticket-id>|-i <ticket-id>] [--json] [--force-worktree] [--close-clean-children] [<ticket-id>]")
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
		return IssueCloseOptions{}, fmt.Errorf("usage: az ticket close [--project <project-id>] [--id <ticket-id>|-i <ticket-id>] [--json] [--force-worktree] [--close-clean-children] [<ticket-id>]")
	}
	opts.Project = normalizeIssueProject(opts.Project)
	return opts, nil
}

func ParseIssueCleanupArgs(args []string) (IssueCleanupOptions, error) {
	opts := IssueCleanupOptions{Action: "closed", PerIssueTimeout: issueCloseCleanupTimeout}
	var idsCSV, statusesCSV, updatedBefore string
	fs := flag.NewFlagSet("issue cleanup", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	addIssueProjectFlag(fs, &opts.Project)
	fs.Func("id", "issue id (repeatable)", func(v string) error { opts.IDs = append(opts.IDs, v); return nil })
	fs.StringVar(&idsCSV, "ids", "", "comma-separated issue ids")
	fs.Func("status", "candidate status (repeatable)", func(v string) error { opts.Statuses = append(opts.Statuses, v); return nil })
	fs.StringVar(&statusesCSV, "statuses", "", "comma-separated candidate statuses")
	fs.StringVar(&opts.Query, "query", "", "case-insensitive candidate text query")
	fs.StringVar(&updatedBefore, "updated-before", "", "candidate update cutoff")
	fs.IntVar(&opts.Limit, "limit", 0, "maximum candidates (0 means unlimited)")
	fs.StringVar(&opts.Action, "action", "closed", "lifecycle action: closed or cancelled")
	fs.BoolVar(&opts.DryRun, "dry-run", false, "preview ordered actions without changing issues")
	fs.BoolVar(&opts.JSON, "json", false, "output structured per-issue results")
	fs.DurationVar(&opts.PerIssueTimeout, "per-ticket-timeout", issueCloseCleanupTimeout, "maximum time for each ticket cleanup")
	fs.DurationVar(&opts.PerIssueTimeout, "per-issue-timeout", issueCloseCleanupTimeout, "deprecated alias for --per-ticket-timeout")
	if err := parseWithInterspersedFlags(fs, args); err != nil {
		return IssueCleanupOptions{}, err
	}
	if fs.NArg() != 0 {
		return IssueCleanupOptions{}, fmt.Errorf("unexpected argument: %s", fs.Arg(0))
	}
	if idsCSV != "" {
		opts.IDs = append(opts.IDs, strings.Split(idsCSV, ",")...)
	}
	if statusesCSV != "" {
		opts.Statuses = append(opts.Statuses, strings.Split(statusesCSV, ",")...)
	}
	opts.IDs = dedupeOrderedIDs(opts.IDs)
	for i := range opts.Statuses {
		opts.Statuses[i] = strings.ToLower(strings.TrimSpace(opts.Statuses[i]))
	}
	opts.Statuses = dedupeOrderedIDs(opts.Statuses)
	opts.Action = strings.ToLower(strings.TrimSpace(opts.Action))
	if opts.Action != "closed" && opts.Action != "cancelled" {
		return IssueCleanupOptions{}, fmt.Errorf("action must be closed or cancelled")
	}
	if opts.Limit < 0 {
		return IssueCleanupOptions{}, fmt.Errorf("limit must be >= 0")
	}
	if opts.PerIssueTimeout <= 0 {
		return IssueCleanupOptions{}, fmt.Errorf("per-ticket-timeout must be positive")
	}
	if strings.TrimSpace(opts.Query) != "" && len(domain.ContentQueryTerms(opts.Query)) == 0 {
		return IssueCleanupOptions{}, fmt.Errorf("query must contain a searchable term")
	}
	var err error
	if opts.UpdatedBefore, err = parseIssueListDateFilter(updatedBefore, true); err != nil {
		return IssueCleanupOptions{}, fmt.Errorf("invalid --updated-before: %w", err)
	}
	if len(opts.IDs) == 0 && len(opts.Statuses) == 0 && strings.TrimSpace(opts.Query) == "" && opts.UpdatedBefore == nil {
		return IssueCleanupOptions{}, fmt.Errorf("at least one --id/--ids or candidate filter is required")
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
		return IssueDeleteOptions{}, fmt.Errorf("usage: az ticket delete [--project <project-id>] --confirm [--id <ticket-id>] [--json] [--cleanup|--stop-session] [--remove-worktree] [--force-worktree] [<ticket-id>]")
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
		return IssueDeleteOptions{}, fmt.Errorf("usage: az ticket delete [--project <project-id>] --confirm [--id <ticket-id>] [--json] [--cleanup|--stop-session] [--remove-worktree] [--force-worktree] [<ticket-id>]")
	}
	opts.Project = normalizeIssueProject(opts.Project)
	return opts, nil
}

func ParseIssueUnarchiveArgs(args []string) (IssueUnarchiveOptions, error) {
	opts := IssueUnarchiveOptions{}
	issueIDFlag := ""
	fs := flag.NewFlagSet("issue unarchive", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	addIssueProjectFlag(fs, &opts.Project)
	fs.StringVar(&issueIDFlag, "id", "", "issue id (named alternative to positional)")
	fs.BoolVar(&opts.JSON, "json", false, "output issue unarchive result as JSON")
	fs.BoolVar(&opts.WithParents, "with-parents", false, "also restore archived parent-child ancestors")
	fs.BoolVar(&opts.CascadeChildren, "cascade-children", false, "also restore archived parent-child descendants")
	if err := parseWithInterspersedFlags(fs, args); err != nil {
		return IssueUnarchiveOptions{}, err
	}
	if fs.NArg() > 1 {
		return IssueUnarchiveOptions{}, fmt.Errorf("usage: az ticket unarchive [--project <project-id>] [--id <ticket-id>] [--json] [--with-parents] [--cascade-children] [<ticket-id>]")
	}
	if fs.NArg() == 1 {
		opts.IssueID = fs.Arg(0)
	}
	if strings.TrimSpace(issueIDFlag) != "" {
		opts.IssueID = strings.TrimSpace(issueIDFlag)
	}
	if strings.TrimSpace(opts.IssueID) == "" {
		return IssueUnarchiveOptions{}, fmt.Errorf("usage: az ticket unarchive [--project <project-id>] [--id <ticket-id>] [--json] [--with-parents] [--cascade-children] [<ticket-id>]")
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
	fs.BoolVar(&opts.ForceWorktree, "force-worktree", false, "force worktree removal when setting closed or cancelled")
	fs.BoolVar(&opts.CascadeChildren, "cascade-children", false, "when requesting review, move open/in_progress descendants to in_review first")
	fs.StringVar(&statusRaw, "status", "", "durable lifecycle action (backlog|open|in_progress|in_review|closed|cancelled)")
	fs.Func("update-impl", "set implementation assignment (repeatable)", func(v string) error {
		trimmed := strings.TrimSpace(v)
		if trimmed == "" {
			return fmt.Errorf("empty update-impl value")
		}
		updateImpls = append(updateImpls, trimmed)
		return nil
	})
	fs.StringVar(&typeRaw, "type", "", "updated issue type (task|bug|feature|epic|chore|investigation)")
	fs.StringVar(&priorityRaw, "priority", "", "updated priority (P0-P4)")
	if err := parseWithInterspersedFlags(fs, args); err != nil {
		return IssueUpdateOptions{}, err
	}
	if fs.NArg() > 1 {
		return IssueUpdateOptions{}, fmt.Errorf("usage: az ticket update [--project <project-id>] [--id <ticket-id>] [--json] [<ticket-id>] [--title text] [--description text] [--notes text] [--append-notes text] [--status backlog|open|in_progress|in_review|closed|cancelled] [--cascade-children] [--force-worktree] [--type task|bug|feature|epic|chore|investigation] [--priority P0|P1|P2|P3|P4] [--update-impl <impl> ...]")
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
		return IssueUpdateOptions{}, fmt.Errorf("usage: az ticket update [--project <project-id>] [--id <ticket-id>] [--json] [<ticket-id>] [--title text] [--description text] [--notes text] [--append-notes text] [--status backlog|open|in_progress|in_review|closed|cancelled] [--cascade-children] [--force-worktree] [--type task|bug|feature|epic|chore|investigation] [--priority P0|P1|P2|P3|P4] [--update-impl <impl> ...]")
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
		status, lifecycle, err := parseIssueUpdateStatus(statusRaw)
		if err != nil {
			return IssueUpdateOptions{}, err
		}
		if lifecycle != nil {
			opts.Lifecycle = lifecycle
		} else {
			opts.Status = &status
		}
	}
	if opts.ForceWorktree && (opts.Status == nil || (*opts.Status != domain.StatusDone && *opts.Status != domain.StatusCancelled)) {
		return IssueUpdateOptions{}, fmt.Errorf("--force-worktree is only supported with --status closed or --status cancelled")
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
	if opts.Title == "" && !opts.DescriptionSet && opts.Notes == nil && opts.AppendNotes == "" && opts.Type == nil && opts.Priority == nil && opts.Status == nil && opts.Lifecycle == nil && len(opts.UpdateImpls) == 0 {
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
	fs.StringVar(&issueIDFlag, "ticket-id", "", "source ticket id (named alternative to positional)")
	fs.StringVar(&issueIDFlag, "issue-id", "", "deprecated alias for --ticket-id")
	fs.StringVar(&dependsOnIDFlag, "depends-on-id", "", "dependency target issue id (named alternative to positional)")
	fs.StringVar(&opts.Type, "type", "blocks", "dependency type (blocks|related|parent-child|discovered-from|created-in)")
	fs.BoolVar(&opts.ForceParentChange, "force-parent-change", false, "replace an existing parent-child edge")
	fs.BoolVar(&opts.JSON, "json", false, "output dependency add result as JSON")
	if err := parseWithInterspersedFlags(fs, args); err != nil {
		return IssueDependencyAddOptions{}, err
	}
	if fs.NArg() > 2 {
		return IssueDependencyAddOptions{}, fmt.Errorf("usage: az ticket dep add [--project <project-id>] [--ticket-id <ticket-id>] [--depends-on-id <depends-on-id>] [<ticket-id> <depends-on-id>] [--type blocks|related|parent-child|discovered-from|created-in] [--force-parent-change] [--json]")
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
		return IssueDependencyAddOptions{}, fmt.Errorf("usage: az ticket dep add [--project <project-id>] [--ticket-id <ticket-id>] [--depends-on-id <depends-on-id>] [<ticket-id> <depends-on-id>] [--type blocks|related|parent-child|discovered-from|created-in] [--force-parent-change] [--json]")
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
	fs.StringVar(&issueIDFlag, "ticket-id", "", "source ticket id (named alternative to positional)")
	fs.StringVar(&issueIDFlag, "issue-id", "", "deprecated alias for --ticket-id")
	fs.StringVar(&dependsOnIDFlag, "depends-on-id", "", "dependency target issue id (named alternative to positional)")
	fs.StringVar(&opts.Type, "type", "blocks", "dependency type (blocks|related|parent-child|discovered-from|created-in)")
	fs.BoolVar(&opts.Confirm, "confirm", false, "confirm removal for guarded dependency types")
	fs.BoolVar(&opts.ConfirmParentOrphan, "confirm-parent-orphan", false, "confirm parent-child removal that can orphan active child work onto the root board")
	fs.BoolVar(&opts.JSON, "json", false, "output dependency remove result as JSON")
	if err := parseWithInterspersedFlags(fs, args); err != nil {
		return IssueDependencyRemoveOptions{}, err
	}
	if fs.NArg() > 2 {
		return IssueDependencyRemoveOptions{}, fmt.Errorf("usage: az ticket dep remove [--project <project-id>] [--ticket-id <ticket-id>] [--depends-on-id <depends-on-id>] [<ticket-id> <depends-on-id>] [--type blocks|related|parent-child|discovered-from|created-in] [--confirm] [--confirm-parent-orphan] [--json]")
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
		return IssueDependencyRemoveOptions{}, fmt.Errorf("usage: az ticket dep remove [--project <project-id>] [--ticket-id <ticket-id>] [--depends-on-id <depends-on-id>] [<ticket-id> <depends-on-id>] [--type blocks|related|parent-child|discovered-from|created-in] [--confirm] [--confirm-parent-orphan] [--json]")
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
	fs.StringVar(&issueIDFlag, "ticket-id", "", "ticket id (named alternative to positional)")
	fs.StringVar(&issueIDFlag, "issue-id", "", "deprecated alias for --ticket-id")
	fs.StringVar(&pathFlag, "path", "", "source image path (named alternative to positional)")
	fs.BoolVar(&opts.JSON, "json", false, "output image add result as JSON")
	if err := parseWithInterspersedFlags(fs, args); err != nil {
		return IssueImageAddOptions{}, err
	}
	if fs.NArg() > 2 {
		return IssueImageAddOptions{}, fmt.Errorf("usage: az ticket image add [--project <project-id>] [--ticket-id <ticket-id>] [--path <file>] [<ticket-id> <file>] [--json]")
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
		return IssueImageAddOptions{}, fmt.Errorf("usage: az ticket image add [--project <project-id>] [--ticket-id <ticket-id>] [--path <file>] [<ticket-id> <file>] [--json]")
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
	fs.StringVar(&issueIDFlag, "ticket-id", "", "ticket id (named alternative to positional)")
	fs.StringVar(&issueIDFlag, "issue-id", "", "deprecated alias for --ticket-id")
	fs.StringVar(&attachmentIDFlag, "attachment-id", "", "attachment id (named alternative to positional)")
	fs.BoolVar(&opts.JSON, "json", false, "output image remove result as JSON")
	if err := parseWithInterspersedFlags(fs, args); err != nil {
		return IssueImageRemoveOptions{}, err
	}
	if fs.NArg() > 2 {
		return IssueImageRemoveOptions{}, fmt.Errorf("usage: az ticket image remove [--project <project-id>] [--ticket-id <ticket-id>] [--attachment-id <attachment-id>] [<ticket-id> <attachment-id>] [--json]")
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
		return IssueImageRemoveOptions{}, fmt.Errorf("usage: az ticket image remove [--project <project-id>] [--ticket-id <ticket-id>] [--attachment-id <attachment-id>] [<ticket-id> <attachment-id>] [--json]")
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
	fs.StringVar(&issueIDFlag, "ticket-id", "", "ticket id (named alternative to positional)")
	fs.StringVar(&issueIDFlag, "issue-id", "", "deprecated alias for --ticket-id")
	fs.StringVar(&pathFlag, "path", "", "source document path (named alternative to positional)")
	fs.BoolVar(&opts.JSON, "json", false, "output document add result as JSON")
	if err := parseWithInterspersedFlags(fs, args); err != nil {
		return IssueDocumentAddOptions{}, err
	}
	if fs.NArg() > 2 {
		return IssueDocumentAddOptions{}, fmt.Errorf("usage: az ticket document add [--project <project-id>] [--ticket-id <ticket-id>] [--path <file>] [<ticket-id> <file>] [--json]")
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
		return IssueDocumentAddOptions{}, fmt.Errorf("usage: az ticket document add [--project <project-id>] [--ticket-id <ticket-id>] [--path <file>] [<ticket-id> <file>] [--json]")
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
	fs.StringVar(&issueIDFlag, "ticket-id", "", "ticket id (named alternative to positional)")
	fs.StringVar(&issueIDFlag, "issue-id", "", "deprecated alias for --ticket-id")
	fs.BoolVar(&opts.JSON, "json", false, "output document list result as JSON")
	if err := parseWithInterspersedFlags(fs, args); err != nil {
		return IssueDocumentListOptions{}, err
	}
	if fs.NArg() > 1 {
		return IssueDocumentListOptions{}, fmt.Errorf("usage: az ticket document list [--project <project-id>] [--ticket-id <ticket-id>] [<ticket-id>] [--json]")
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
		return IssueDocumentListOptions{}, fmt.Errorf("usage: az ticket document list [--project <project-id>] [--ticket-id <ticket-id>] [<ticket-id>] [--json]")
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
	fs.StringVar(&issueIDFlag, "ticket-id", "", "ticket id (named alternative to positional)")
	fs.StringVar(&issueIDFlag, "issue-id", "", "deprecated alias for --ticket-id")
	fs.StringVar(&attachmentIDFlag, "attachment-id", "", "attachment id (named alternative to positional)")
	fs.BoolVar(&opts.JSON, "json", false, "output document remove result as JSON")
	if err := parseWithInterspersedFlags(fs, args); err != nil {
		return IssueDocumentRemoveOptions{}, err
	}
	if fs.NArg() > 2 {
		return IssueDocumentRemoveOptions{}, fmt.Errorf("usage: az ticket document remove [--project <project-id>] [--ticket-id <ticket-id>] [--attachment-id <attachment-id>] [<ticket-id> <attachment-id>] [--json]")
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
		return IssueDocumentRemoveOptions{}, fmt.Errorf("usage: az ticket document remove [--project <project-id>] [--ticket-id <ticket-id>] [--attachment-id <attachment-id>] [<ticket-id> <attachment-id>] [--json]")
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
		return "", fmt.Errorf("missing required flag: --impl (implementation inference unavailable: %w). Specify --impl <implementation>; --impl selects implementation metadata, not parent/root membership", err)
	}
	switch len(impls) {
	case 1:
		return impls[0], nil
	default:
		return "", fmt.Errorf("missing required flag: --impl (implementation is ambiguous; valid --impl values: %s)", strings.Join(impls, ", "))
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

func issueWriteImplementationValidationTimedOut(err error) bool {
	var timeoutErr *daemonclient.ReadWaitTimeoutError
	return errors.As(err, &timeoutErr)
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
	return fmt.Errorf("unknown implementation %q (valid --impl values: %s). Run `az impl list` to inspect implementation assignments. If you meant to parent work under %q, use `--parent %s` or run from the correct AZEDARACH_ISSUE_ID context without --impl", impl, strings.Join(known, ", "), impl, impl)
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
			fmt.Println("Latency trace logging and OpenTelemetry spans are enabled. Restart the daemon for daemon-side diagnostics to use the persisted setting.")
		} else {
			latencytrace.SetConfigEnabled(false)
			fmt.Println("Latency trace logging and OpenTelemetry spans are disabled.")
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
	case "issues.autoArchive.enabled":
		parsed, ok := parseBooleanConfigValue(value)
		if !ok {
			return "", fmt.Errorf("Invalid boolean value '%s' for issues.autoArchive.enabled. Use true/false, on/off, yes/no, or 1/0.", value)
		}
		cfg.Issues.AutoArchive.Enabled = parsed
		return fmt.Sprintf("%t", parsed), nil
	case "issues.autoArchive.closedAfterDays":
		parsed, err := strconv.Atoi(strings.TrimSpace(value))
		if err != nil || parsed < 1 {
			return "", fmt.Errorf("Invalid integer value '%s' for issues.autoArchive.closedAfterDays. Use a positive day count.", value)
		}
		cfg.Issues.AutoArchive.ClosedAfterDays = parsed
		return strconv.Itoa(parsed), nil
	case "issues.autoArchive.interval":
		parsed, err := time.ParseDuration(strings.TrimSpace(value))
		if err != nil || parsed <= 0 {
			return "", fmt.Errorf("Invalid duration value '%s' for issues.autoArchive.interval. Use a positive Go duration such as 24h.", value)
		}
		cfg.Issues.AutoArchive.Interval = strings.TrimSpace(value)
		return cfg.Issues.AutoArchive.Interval, nil
	default:
		return "", fmt.Errorf("Unsupported config key '%s'. Supported keys: spec.enabled, diagnostics.latencyTrace, issues.autoArchive.enabled, issues.autoArchive.closedAfterDays, issues.autoArchive.interval", key)
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
	if protocol.NormalizeProjectID(opts.Project) == "global" {
		return globalIssueListCommand(deps, opts)
	}
	restoreProject := applyIssueProjectOverride(deps, opts.Project)
	defer restoreProject()

	ctx, cancel := context.WithTimeout(context.Background(), daemonCommandTimeout)
	defer cancel()
	if err := ensureDaemon(ctx, deps, "cli"); err != nil {
		return err
	}

	var (
		snapshot    daemonclient.TaskSnapshot
		err         error
		archiveMode protocol.ArchiveMode
	)
	archiveMode, err = protocol.NormalizeArchiveMode(opts.Archived)
	if err != nil {
		return err
	}
	if strings.TrimSpace(opts.Query) != "" {
		snapshot, err = deps.DaemonClient.ListTasksSnapshotWithQueryArchiveMode(ctx, opts.Query, archiveMode)
	} else if opts.Deps || len(opts.DependsOnIDs) > 0 {
		if archiveMode == protocol.ArchiveModeExclude {
			snapshot, err = listTasksSnapshotWithDependenciesForCLI(ctx, deps)
		} else {
			snapshot, err = deps.DaemonClient.ListTasksSnapshotWithDependenciesArchiveMode(ctx, archiveMode)
		}
	} else {
		if archiveMode == protocol.ArchiveModeExclude {
			snapshot, err = listTasksSnapshotForCLI(ctx, deps)
		} else {
			snapshot, err = deps.DaemonClient.ListTasksSnapshotWithArchiveMode(ctx, archiveMode)
		}
	}
	if err != nil {
		return fmt.Errorf("failed to list issues: %w", err)
	}
	tasks := snapshot.Tasks
	if len(opts.IDs) > 0 {
		tasks = filterTasksByIDs(tasks, opts.IDs)
	}
	if len(opts.States) > 0 {
		tasks = filterTasksByIssueDisplayPhase(tasks, opts.States)
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
		fmt.Fprintln(w, "ID\tSTATUS\tPRIORITY\tTYPE\tASSIGNEE\tOWNER\tEST\tIMPL\tDEPS\tTITLE")
	} else {
		fmt.Fprintln(w, "ID\tSTATUS\tPRIORITY\tTYPE\tASSIGNEE\tOWNER\tEST\tIMPL\tTITLE")
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
		ownerSummary := renderIssueOwnershipListCell(task.Ownership)
		implSummary := formatIssueImplementationSummary(task.Implementations)
		if opts.Deps {
			fmt.Fprintf(
				w,
				"%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
				task.ID,
				task.IssueDisplayStatusText(),
				task.Priority.String(),
				task.Type,
				assigneeSummary,
				ownerSummary,
				estimateSummary,
				implSummary,
				formatDependencySummary(task.Dependencies),
				task.Title,
			)
			continue
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", task.ID, task.IssueDisplayStatusText(), task.Priority.String(), task.Type, assigneeSummary, ownerSummary, estimateSummary, implSummary, task.Title)
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

func globalIssueListCommand(deps *Dependencies, opts IssueListOptions) error {
	if deps == nil || deps.DaemonClient == nil {
		return fmt.Errorf("daemon client unavailable")
	}
	archiveMode, err := protocol.NormalizeArchiveMode(opts.Archived)
	if err != nil {
		return err
	}
	if opts.Deps || len(opts.DependsOnIDs) > 0 {
		return fmt.Errorf("global issue queries do not support --deps or --depends-on; query a specific project")
	}
	if archiveMode != protocol.ArchiveModeExclude {
		return fmt.Errorf("global issue queries support only --archived exclude")
	}
	ctx, cancel := context.WithTimeout(context.Background(), daemonCommandTimeout)
	defer cancel()
	consumer := protocol.GlobalViewConsumerSearch
	for _, state := range opts.States {
		if state == domain.IssueDisplayReview {
			consumer = protocol.GlobalViewConsumerReview
			break
		}
	}
	snapshot, err := deps.DaemonClient.GlobalViewSnapshot(ctx, protocol.GlobalSnapshotRequestBody{Query: strings.TrimSpace(opts.Query), Consumer: consumer})
	if err != nil {
		return fmt.Errorf("failed to query global issues: %w", err)
	}
	items := append([]protocol.GlobalViewProjectedItem(nil), snapshot.Projection.Items...)
	filtered := items[:0]
	for _, item := range items {
		task := item.Task
		if len(opts.IDs) > 0 && len(filterTasksByIDs([]domain.Task{task}, opts.IDs)) == 0 {
			continue
		}
		if len(opts.States) > 0 && len(filterTasksByIssueDisplayPhase([]domain.Task{task}, opts.States)) == 0 {
			continue
		}
		if len(opts.ParentIDs) > 0 && len(filterTasksByParentIDs([]domain.Task{task}, opts.ParentIDs)) == 0 {
			continue
		}
		if len(filterTasksByTimeRange([]domain.Task{task}, opts.CreatedAfter, opts.CreatedBefore, opts.UpdatedAfter, opts.UpdatedBefore)) == 0 {
			continue
		}
		filtered = append(filtered, item)
	}
	limit := opts.Limit
	if limit < 1 {
		limit = defaultIssueListLimit
	}
	truncated := len(filtered) > limit
	if truncated {
		filtered = filtered[:limit]
	}
	if opts.JSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(filtered)
	}
	if len(filtered) == 0 {
		fmt.Println("No issues found.")
		return nil
	}
	for _, item := range filtered {
		fmt.Printf("%-24s %-12s %s\n", formatProjectIssueRef(item.Identity.ProjectID.String(), item.Identity.IssueID.String()), item.Task.Status, item.Task.Title)
	}
	if truncated {
		fmt.Printf("Showing %d of %d issues; use --limit to expand.\n", len(filtered), len(items))
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

	archiveMode, err := protocol.NormalizeArchiveMode(opts.Archived)
	if err != nil {
		return err
	}
	snapshot, err := deps.DaemonClient.GetTaskSnapshotWithArchiveMode(ctx, opts.IssueID, archiveMode, daemonclient.ReadWaitModeDefault)
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
	fmt.Printf("Status: %s\n", task.IssueDisplayStatusText())
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
	if ownershipSummary := renderIssueOwnershipSummary(task.Ownership); ownershipSummary != "" {
		fmt.Printf("Owner: %s\n", ownershipSummary)
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

func IssueClaimCommand(deps *Dependencies, opts IssueOwnershipOptions) error {
	return issueOwnershipCommand(deps, opts, true)
}

func IssueReleaseCommand(deps *Dependencies, opts IssueOwnershipOptions) error {
	return issueOwnershipCommand(deps, opts, false)
}

func issueOwnershipCommand(deps *Dependencies, opts IssueOwnershipOptions, claim bool) error {
	restoreProject := applyIssueProjectOverride(deps, opts.Project)
	defer restoreProject()

	ownerID := strings.TrimSpace(opts.OwnerID)
	if ownerID == "" {
		ownerID = defaultIssueOwnerID()
	}
	if ownerID == "" {
		return fmt.Errorf("--owner is required when current actor cannot be inferred")
	}

	ctx, cancel := context.WithTimeout(context.Background(), daemonCommandTimeout)
	defer cancel()
	if err := ensureDaemon(ctx, deps, "cli"); err != nil {
		return err
	}

	req := daemonclient.TaskOwnershipRequest{
		OwnerID:   ownerID,
		OwnerKind: opts.OwnerKind,
		TTL:       opts.TTL,
		Force:     opts.Force,
	}
	var (
		task domain.Task
		err  error
	)
	if claim {
		task, err = deps.DaemonClient.ClaimTaskOwnership(ctx, opts.IssueID, req)
	} else {
		task, err = deps.DaemonClient.ReleaseTaskOwnership(ctx, opts.IssueID, req)
	}
	if err != nil {
		return err
	}
	if opts.JSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(task)
	}
	if claim {
		fmt.Printf("Claimed issue %s: %s\n", task.ID, renderIssueOwnershipSummary(task.Ownership))
		return nil
	}
	fmt.Printf("Released issue %s ownership\n", task.ID)
	return nil
}

func defaultIssueOwnerID() string {
	for _, name := range []string{"AZEDARACH_AUDIT_ACTOR", "USER", "LOGNAME"} {
		if value := strings.TrimSpace(os.Getenv(name)); value != "" {
			return value
		}
	}
	return ""
}

func IssueEventsCommand(deps *Dependencies, opts IssueEventsOptions) error {
	if opts.JQHelp {
		printIssueEventsJQHelp(os.Stdout)
		return nil
	}

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
		return enc.Encode(issueEventsJSONOutput{
			SchemaVersion: "issue_events.v1",
			IssueID:       opts.IssueID,
			Events:        issueEventsJSON(events),
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

func IssueRecordCommand(deps *Dependencies, opts IssueRecordOptions) error {
	restoreProject := applyIssueProjectOverride(deps, opts.Project)
	defer restoreProject()

	issueID := strings.TrimSpace(opts.IssueID)
	if issueID == "" {
		issueID = strings.TrimSpace(os.Getenv("AZEDARACH_ISSUE_ID"))
	}
	if issueID == "" {
		return fmt.Errorf("issue id is required; pass --id or run with AZEDARACH_ISSUE_ID")
	}
	payload, err := issueRecordPayload(opts)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), daemonCommandTimeout)
	defer cancel()
	if err := ensureDaemon(ctx, deps, "cli"); err != nil {
		return err
	}

	event, err := deps.DaemonClient.AppendTaskEvent(ctx, issueID, daemonclient.TaskEventAppendRequest{
		Type:          opts.EventType,
		Source:        opts.Source,
		SourceCommand: "az issue record",
		OperationID:   opts.OperationID,
		SessionID:     opts.SessionID,
		WorktreePath:  issueRecordWorktreePath(deps, opts.WorktreePath),
		Payload:       payload,
	})
	if err != nil {
		if strings.Contains(err.Error(), fmt.Sprintf("issue not found: %s", issueID)) {
			return fmt.Errorf("issue not found: %s", issueID)
		}
		return fmt.Errorf("failed to record issue event for %s: %w", issueID, err)
	}

	if opts.JSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(issueEventJSONFromDomain(event))
	}
	fmt.Printf("Recorded %s for %s as event %d\n", event.Type, event.IssueID, event.ID)
	if summary := issueObservationEventSummaryText(event.Payload); summary != "" {
		fmt.Printf("Summary: %s\n", summary)
	}
	return nil
}

func issueRecordPayload(opts IssueRecordOptions) (map[string]any, error) {
	payload := map[string]any{}
	if strings.TrimSpace(opts.DataJSON) != "" {
		if err := json.Unmarshal([]byte(opts.DataJSON), &payload); err != nil {
			return nil, fmt.Errorf("parse --data JSON object: %w", err)
		}
		if payload == nil {
			payload = map[string]any{}
		}
	}
	if strings.TrimSpace(opts.Summary) != "" {
		payload["summary"] = strings.TrimSpace(opts.Summary)
	}
	if strings.TrimSpace(opts.Body) != "" {
		payload["body"] = strings.TrimSpace(opts.Body)
	}
	if len(opts.FollowUpIssueIDs) > 0 {
		payload["follow_up_issue_ids"] = append([]string(nil), opts.FollowUpIssueIDs...)
	}
	if len(payload) == 0 {
		return nil, fmt.Errorf("record payload is empty")
	}
	return payload, nil
}

func issueRecordWorktreePath(deps *Dependencies, explicit string) string {
	if trimmed := strings.TrimSpace(explicit); trimmed != "" {
		return trimmed
	}
	if deps != nil && strings.TrimSpace(deps.RepoDir) != "" {
		return strings.TrimSpace(deps.RepoDir)
	}
	wd, err := os.Getwd()
	if err != nil {
		return ""
	}
	return wd
}

func issueObservationEventSummaryText(payload map[string]any) string {
	return firstStringPayloadValue(payload, "summary", "message", "body", "line", "evidence")
}

type issueEventsJSONOutput struct {
	SchemaVersion string           `json:"schema_version"`
	IssueID       string           `json:"issue_id"`
	Events        []issueEventJSON `json:"events"`
}

type issueEventJSON struct {
	ID            int64          `json:"id"`
	IssueID       string         `json:"issue_id"`
	Type          string         `json:"type"`
	EventType     string         `json:"event_type"`
	CreatedAt     time.Time      `json:"created_at"`
	ObservedAt    time.Time      `json:"observed_at"`
	Source        string         `json:"source"`
	SourceCommand string         `json:"source_command"`
	OperationID   string         `json:"operation_id"`
	SessionID     string         `json:"session_id"`
	WorktreePath  string         `json:"worktree_path"`
	Body          string         `json:"body"`
	Notes         string         `json:"notes"`
	Data          map[string]any `json:"data"`
	Payload       map[string]any `json:"payload"`
}

func issueEventsJSON(events []domain.IssueObservationEvent) []issueEventJSON {
	out := make([]issueEventJSON, 0, len(events))
	for _, event := range events {
		out = append(out, issueEventJSONFromDomain(event))
	}
	return out
}

func issueEventJSONFromDomain(event domain.IssueObservationEvent) issueEventJSON {
	data := map[string]any{}
	if event.Payload != nil {
		data = event.Payload
	}
	body := firstStringPayloadValue(data, "body", "message", "summary", "line", "evidence")
	notes := firstStringPayloadValue(data, "notes", "note", "line")
	eventType := string(event.Type)
	return issueEventJSON{
		ID:            event.ID,
		IssueID:       event.IssueID.String(),
		Type:          eventType,
		EventType:     eventType,
		CreatedAt:     event.ObservedAt,
		ObservedAt:    event.ObservedAt,
		Source:        event.Source,
		SourceCommand: event.SourceCommand,
		OperationID:   event.OperationID,
		SessionID:     event.SessionID,
		WorktreePath:  event.WorktreePath,
		Body:          body,
		Notes:         notes,
		Data:          data,
		Payload:       data,
	}
}

func firstStringPayloadValue(data map[string]any, keys ...string) string {
	for _, key := range keys {
		if value, ok := data[key].(string); ok && strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func printIssueEventsJQHelp(w io.Writer) {
	fmt.Fprintln(w, "Stable JSON schema: issue_events.v1")
	fmt.Fprintln(w, "Primary fields: id, issue_id, type, created_at, source, source_command, operation_id, session_id, worktree_path, body, notes, data")
	fmt.Fprintln(w, "Compatibility aliases: event_type == type, observed_at == created_at, payload == data")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Examples:")
	fmt.Fprintln(w, "  az issue events az-123 --json | jq '.events[0]'")
	fmt.Fprintln(w, "  az issue events az-123 --json | jq '[.events[] | {type, created_at, source, data}]'")
	fmt.Fprintln(w, "  az issue events az-123 --json | jq '.events[] | select(.type == \"issue.status_changed\")'")
	fmt.Fprintln(w, "  az issue events az-123 --type validation.passed --json | jq '.events[] | {created_at, body, data}'")
}

func IssueContextRiskCommand(deps *Dependencies, opts IssueContextRiskOptions) error {
	restoreProject := applyIssueProjectOverride(deps, opts.Project)
	defer restoreProject()

	ctx, cancel := context.WithTimeout(context.Background(), daemonCommandTimeout)
	defer cancel()
	if err := ensureDaemon(ctx, deps, "cli"); err != nil {
		return err
	}

	packet, err := deps.DaemonClient.TaskContextRisk(ctx, opts.IssueID, deps.RepoDir, opts.Since, daemonclient.TaskContextRiskOptions{Compact: !opts.Full})
	if err != nil {
		if strings.Contains(err.Error(), fmt.Sprintf("issue not found: %s", opts.IssueID)) {
			return fmt.Errorf("issue not found: %s", opts.IssueID)
		}
		if issueContextRiskTimedOut(err) {
			packet = issueContextRiskDegradedPacket(opts.IssueID, opts.Since, err)
		} else {
			return fmt.Errorf("failed to build context risk for %s: %w", opts.IssueID, err)
		}
	}
	if opts.JSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if opts.Full {
			return enc.Encode(packet)
		}
		return enc.Encode(domain.SummarizeIssueContextRisk(packet))
	}
	if opts.Full {
		renderIssueContextRiskFull(packet)
		return nil
	}
	renderIssueContextRisk(domain.SummarizeIssueContextRisk(packet))
	return nil
}

func issueContextRiskTimedOut(err error) bool {
	return errors.Is(err, context.DeadlineExceeded) || strings.Contains(err.Error(), context.DeadlineExceeded.Error())
}

func issueContextRiskDegradedPacket(issueID string, since time.Time, err error) domain.IssueContextRiskPacket {
	return domain.IssueContextRiskPacket{
		IssueID:        strings.TrimSpace(issueID),
		Level:          domain.IssueContextRiskNone,
		Since:          since,
		GeneratedAt:    time.Now().UTC(),
		Degraded:       true,
		Timeout:        true,
		DegradedReason: strings.TrimSpace(err.Error()),
		CloseoutPrompts: []string{
			"Context-risk scan timed out; retry with --summary or record explicit risk evidence before closeout if local overlap is suspected.",
		},
	}
}

func renderIssueContextRisk(summary domain.IssueContextRiskSummary) {
	fmt.Printf("Issue context risk: %s\n", summary.IssueID)
	if summary.ParentIssueID != "" {
		fmt.Printf("Parent/local scope: %s\n", summary.ParentIssueID)
	}
	fmt.Printf("Level: %s", summary.Level)
	if summary.Confidence > 0 {
		fmt.Printf(" confidence=%d", summary.Confidence)
	}
	if summary.Degraded {
		fmt.Print(" degraded=true")
	}
	if summary.Timeout {
		fmt.Print(" timeout=true")
	}
	fmt.Println()
	if strings.TrimSpace(summary.DegradedReason) != "" {
		fmt.Printf("Degraded reason: %s\n", summary.DegradedReason)
	}
	if !summary.Since.IsZero() {
		fmt.Printf("Window since: %s\n", summary.Since.UTC().Format(time.RFC3339))
	}
	fmt.Printf("Candidates checked: %d; overlapping issues: %d\n", summary.CandidateCount, summary.OverlapIssueCount)
	if len(summary.RelatedIssueIDs) > 0 {
		fmt.Printf("Related issues: %s\n", strings.Join(summary.RelatedIssueIDs, ", "))
	}
	if len(summary.Signals) > 0 {
		fmt.Println("Signals:")
		for _, signal := range summary.Signals {
			fmt.Printf("- %s\n", signal)
		}
	}
	if len(summary.EvidenceSnippets) > 0 {
		fmt.Println("Evidence snippets:")
		for _, snippet := range summary.EvidenceSnippets {
			label := snippet.IssueID
			if snippet.Relationship != "" {
				label += " " + snippet.Relationship
			}
			fmt.Printf("- %s: %s\n", label, strings.Join(snippet.Signals, "; "))
		}
	}
	if len(summary.CloseoutPrompts) > 0 {
		fmt.Println("Closeout prompts:")
		for _, prompt := range summary.CloseoutPrompts {
			fmt.Printf("- %s\n", prompt)
		}
	}
	if len(summary.HandoffFields.StructuredFields) > 0 {
		fmt.Printf("Structured handoff fields: %s\n", strings.Join(summary.HandoffFields.StructuredFields, ", "))
	}
}

func renderIssueContextRiskFull(packet domain.IssueContextRiskPacket) {
	renderIssueContextRisk(domain.SummarizeIssueContextRisk(packet))
	if len(packet.Clusters) > 0 {
		fmt.Println("Clusters:")
		for _, cluster := range packet.Clusters {
			fmt.Printf("- %s %s: %s\n", cluster.Kind, cluster.Value, strings.Join(cluster.Issues, ", "))
		}
	}
	if len(packet.Evidence) > 0 {
		fmt.Println("Evidence:")
		for _, evidence := range packet.Evidence {
			parts := []string{evidence.IssueID}
			if evidence.Relationship != "" {
				parts = append(parts, evidence.Relationship)
			}
			if len(evidence.Files) > 0 {
				parts = append(parts, "files="+strings.Join(evidence.Files, ","))
			}
			if len(evidence.Symbols) > 0 {
				parts = append(parts, "symbols="+strings.Join(evidence.Symbols, ","))
			}
			if len(evidence.Tests) > 0 {
				parts = append(parts, "tests="+strings.Join(evidence.Tests, ","))
			}
			if evidence.RootCause != "" {
				parts = append(parts, "root_cause="+evidence.RootCause)
			}
			if evidence.Invariant != "" {
				parts = append(parts, "invariant="+evidence.Invariant)
			}
			if evidence.Validation != "" {
				parts = append(parts, "validation="+evidence.Validation)
			}
			if len(evidence.RiskNotes) > 0 {
				parts = append(parts, "risk_notes="+strings.Join(evidence.RiskNotes, ";"))
			}
			if len(evidence.EvidenceKinds) > 0 {
				parts = append(parts, "evidence_kinds="+strings.Join(evidence.EvidenceKinds, ","))
			}
			fmt.Printf("- %s\n", strings.Join(parts, " "))
		}
	}
}

func issueContextRiskCloseoutSince() time.Time {
	return time.Now().UTC().Add(-time.Duration(defaultIssueContextRiskDays) * 24 * time.Hour)
}

func loadIssueContextRiskForCloseout(ctx context.Context, deps *Dependencies, issueID string) (*domain.IssueContextRiskPacket, error) {
	if deps == nil || deps.DaemonClient == nil {
		return nil, nil
	}
	since := issueContextRiskCloseoutSince()
	packet, err := deps.DaemonClient.TaskContextRisk(ctx, issueID, deps.RepoDir, since, daemonclient.TaskContextRiskOptions{Compact: true})
	if err != nil {
		if issueContextRiskTimedOut(err) {
			packet := issueContextRiskDegradedPacket(issueID, since, err)
			return &packet, nil
		}
		return nil, fmt.Errorf("inspect context risk before closeout: %w", err)
	}
	return &packet, nil
}

func issueContextRiskCloseoutBlockMessage(issueID string) string {
	return fmt.Sprintf("context risk is high for issue %s: record root_cause, invariant, regression_validation, or a structured risk note before closeout", issueID)
}

func printIssueContextRiskCloseout(packet *domain.IssueContextRiskPacket) {
	if packet == nil || packet.Level == domain.IssueContextRiskNone {
		return
	}
	summary := domain.SummarizeIssueContextRisk(*packet)
	fmt.Printf("- Context risk: %s", summary.Level)
	if summary.Confidence > 0 {
		fmt.Printf(" confidence=%d", summary.Confidence)
	}
	if summary.Degraded {
		fmt.Print(" degraded=true")
	}
	if summary.Timeout {
		fmt.Print(" timeout=true")
	}
	fmt.Println()
	for _, signal := range summary.Signals {
		fmt.Printf("  - %s\n", signal)
	}
	if len(summary.RelatedIssueIDs) > 0 {
		fmt.Printf("  - Related issues: %s\n", strings.Join(summary.RelatedIssueIDs, ", "))
	}
	if domain.IssueContextRiskRequiresStructuredCloseout(*packet) {
		fmt.Println("  - Required before closeout: record root_cause, invariant, regression_validation, or a structured risk note.")
		return
	}
	for _, prompt := range summary.CloseoutPrompts {
		fmt.Printf("  - %s\n", prompt)
	}
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

	ctx, cancel := context.WithTimeout(commandTraceContext(deps), daemonCommandTimeout)
	defer cancel()
	var commandErr error
	ctx, endCommandSpan := latencytrace.StartSpan(ctx, "cli", "issue_doctor", "issue_id", opts.IssueID)
	defer func() { endCommandSpan(commandErr) }()

	if err := ensureDaemon(ctx, deps, "cli"); err != nil {
		commandErr = err
		return err
	}

	loadCtx, endLoadSpan := latencytrace.StartSpanWithEndAttributes(ctx, "cli", "issue_doctor.load_issue", "issue_id", opts.IssueID)
	task, _, ok, err := loadIssueMetadataTask(loadCtx, deps, opts.IssueID)
	loadOutcome := "found"
	if err != nil {
		loadOutcome = "error"
	} else if !ok {
		loadOutcome = "not_found"
	}
	endLoadSpan(err, "outcome", loadOutcome)
	if err != nil {
		commandErr = fmt.Errorf("failed to inspect issue %s: %w", opts.IssueID, err)
		return commandErr
	}
	if !ok {
		commandErr = fmt.Errorf("issue not found: %s", opts.IssueID)
		return commandErr
	}

	_, endLocalSpan := latencytrace.StartSpanWithEndAttributes(ctx, "cli", "issue_doctor.local_checks", "issue_id", task.ID.String())
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
	mixedLifecycleHooks := mixedIssueResourceLifecycleHooksConfigured(deps)
	if mixedLifecycleHooks {
		diagnostics = append(diagnostics, "issueResources config mixes reconcileCommand with one-shot prepare/cleanup hooks; verify lifecycle ownership is intentional")
	}
	endLocalSpan(nil, "diagnostic_ct", len(diagnostics), "mixed_lifecycle_hooks", mixedLifecycleHooks)

	runtimeCtx, endRuntimeSpan := latencytrace.StartSpanWithEndAttributes(ctx, "cli", "issue_doctor.runtime_diagnostics", "issue_id", task.ID.String())
	runtimeDiagnostics := issueDoctorRuntimeDiagnostics(runtimeCtx, deps, task)
	endRuntimeSpan(nil, "diagnostic_ct", len(runtimeDiagnostics), "has_session_metadata", task.Session != nil || task.HasTmuxSession, "has_worktree", task.HasWorktree)
	diagnostics = append(diagnostics, runtimeDiagnostics...)

	walCtx, endWALSpan := latencytrace.StartSpanWithEndAttributes(ctx, "cli", "issue_doctor.sqlite_wal", "issue_id", task.ID.String(), "checkpoint_wal", opts.CheckpointWAL, "truncate_wal", opts.TruncateWAL)
	walSummary, walPayload, walDiagnostics := issueDoctorSQLiteWALDiagnostics(walCtx, deps, opts)
	var walSpanErr error
	walOutcome := "ok"
	if _, ok := walPayload["error"]; ok {
		walSpanErr = errors.New("sqlite wal diagnostics unavailable")
		walOutcome = "error"
	}
	endWALSpan(walSpanErr, "diagnostic_ct", len(walDiagnostics), "outcome", walOutcome)
	diagnostics = append(diagnostics, walDiagnostics...)

	dependencySummary := formatDependencySummary(task.Dependencies)

	_, endRenderSpan := latencytrace.StartSpanWithEndAttributes(ctx, "cli", "issue_doctor.render", "issue_id", task.ID.String(), "json", opts.JSON)
	if len(diagnostics) == 0 {
		if opts.JSON {
			commandErr = printJSON(map[string]any{
				"issue_id":      task.ID,
				"status":        "ok",
				"dependencies":  dependencySummary,
				"sqlite_wal":    walPayload,
				"diagnostics":   []string{},
				"diagnostic_ct": 0,
			})
			endRenderSpan(commandErr, "outcome", "ok")
			return commandErr
		}
		fmt.Printf("Doctor: OK %s\n", task.ID)
		fmt.Printf("Dependencies: %s\n", dependencySummary)
		if walSummary != "" {
			fmt.Printf("SQLite WAL: %s\n", walSummary)
		}
		endRenderSpan(nil, "outcome", "ok")
		return nil
	}
	if opts.JSON {
		commandErr = printJSON(map[string]any{
			"issue_id":      task.ID,
			"status":        "warn",
			"dependencies":  dependencySummary,
			"sqlite_wal":    walPayload,
			"diagnostics":   diagnostics,
			"diagnostic_ct": len(diagnostics),
		})
		endRenderSpan(commandErr, "outcome", "warn", "diagnostic_ct", len(diagnostics))
		return commandErr
	}
	fmt.Printf("Doctor: WARN %s\n", task.ID)
	if walSummary != "" {
		fmt.Printf("SQLite WAL: %s\n", walSummary)
	}
	for _, diagnostic := range diagnostics {
		fmt.Printf("- %s\n", diagnostic)
	}
	endRenderSpan(nil, "outcome", "warn", "diagnostic_ct", len(diagnostics))
	return nil
}

func commandTraceContext(deps *Dependencies) context.Context {
	if deps == nil || deps.TraceContext == nil {
		return context.Background()
	}
	return deps.TraceContext
}

func issueDoctorSQLiteWALDiagnostics(ctx context.Context, deps *Dependencies, opts IssueDoctorOptions) (string, map[string]any, []string) {
	if deps == nil || deps.DaemonClient == nil {
		return "", map[string]any{"skipped": "daemon_client_unavailable"}, nil
	}
	mode := ""
	if opts.TruncateWAL {
		mode = "TRUNCATE"
	} else if opts.CheckpointWAL {
		mode = "PASSIVE"
	}
	diag, err := deps.DaemonClient.TaskSQLiteWAL(ctx, mode)
	if err != nil {
		return "", map[string]any{"error": err.Error()}, []string{"sqlite wal diagnostics unavailable: " + err.Error()}
	}
	payload := map[string]any{
		"db_path":                    diag.DBPath,
		"wal_path":                   diag.WALPath,
		"wal_bytes":                  diag.WALBytes,
		"checkpoint_threshold_bytes": diag.CheckpointThreshold,
		"large_threshold_bytes":      diag.LargeThreshold,
		"large":                      diag.Large,
		"open_connections":           diag.OpenConnections,
		"in_use":                     diag.InUse,
		"idle":                       diag.Idle,
	}
	checkpoint := diag.Checkpoint
	if checkpoint != nil {
		payload["checkpoint"] = map[string]any{
			"mode":                checkpoint.Mode,
			"busy":                checkpoint.Busy,
			"log_frames":          checkpoint.LogFrames,
			"checkpointed_frames": checkpoint.CheckpointedFrames,
			"wal_bytes_before":    checkpoint.WALBytesBefore,
			"wal_bytes_after":     checkpoint.WALBytesAfter,
			"duration_ms":         checkpoint.DurationMillisecond,
		}
	}
	warnings := []string{}
	if diag.Large {
		warnings = append(warnings, fmt.Sprintf("sqlite wal is large: %d bytes at %s", diag.WALBytes, diag.WALPath))
	}
	if checkpoint != nil && checkpoint.Busy != 0 {
		warnings = append(warnings, fmt.Sprintf("sqlite wal checkpoint could not finish because readers are active: busy=%d log_frames=%d checkpointed_frames=%d", checkpoint.Busy, checkpoint.LogFrames, checkpoint.CheckpointedFrames))
	}
	return issueDoctorSQLiteWALSummary(diag, checkpoint), payload, warnings
}

func issueDoctorSQLiteWALSummary(diag protocol.TaskSQLiteWALResponse, checkpoint *protocol.TaskSQLiteWALCheckpointInfo) string {
	summary := fmt.Sprintf("%d bytes at %s", diag.WALBytes, diag.WALPath)
	if checkpoint != nil {
		summary += fmt.Sprintf("; checkpoint mode=%s busy=%d frames=%d/%d after=%d bytes",
			checkpoint.Mode,
			checkpoint.Busy,
			checkpoint.CheckpointedFrames,
			checkpoint.LogFrames,
			checkpoint.WALBytesAfter,
		)
	}
	return summary
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

func issueDoctorRuntimeDiagnostics(ctx context.Context, deps *Dependencies, task domain.Task) []string {
	diagnostics := make([]string, 0, 2)
	issueID := strings.TrimSpace(task.ID.String())
	if issueID == "" {
		issueID = "<issue-id>"
	}
	if task.Session != nil || task.HasTmuxSession {
		sessionStatusOutput := ""
		if deps != nil && deps.DaemonClient != nil && issueID != "<issue-id>" {
			if output, err := deps.DaemonClient.SessionStatus(ctx, issueID); err == nil {
				sessionStatusOutput = strings.TrimSpace(output)
			} else {
				sessionStatusOutput = strings.TrimSpace(err.Error())
			}
		}
		state := "unknown"
		activity := ""
		source := ""
		if task.Session != nil {
			if trimmed := strings.TrimSpace(string(task.Session.State)); trimmed != "" {
				state = trimmed
			}
			activity = strings.TrimSpace(task.Session.DisplayActivity())
			if activity == "" {
				activity = strings.TrimSpace(task.Session.Activity)
			}
			source = strings.TrimSpace(task.Session.ActivitySource)
		}
		detail := fmt.Sprintf("runtime session metadata remains: state=%s", state)
		if activity != "" {
			detail += " activity=" + activity
		}
		if source != "" {
			detail += " source=" + source
		}
		if strings.Contains(strings.ToLower(sessionStatusOutput), "no active session found") {
			detail = "stale " + detail + "; live session status reports no active session"
		}
		diagnostics = append(diagnostics, fmt.Sprintf("%s; verify live state with `az session status %s`; if no active session exists, repair with `az orchestrate close-session --issue %s`, then retry close", detail, issueID, issueID))
	}
	if task.HasWorktree {
		diagnostics = append(diagnostics, fmt.Sprintf("runtime worktree metadata remains; verify the worktree still exists, or retry close with cleanup/force options after confirming it is gone for %s", issueID))
	}
	return diagnostics
}

func IssueCreateCommand(deps *Dependencies, opts IssueCreateOptions) error {
	if isDifferentExplicitIssueProject(deps.ProjectID, opts.Project) && !opts.ExplicitParent {
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
	if opts.Deferred && opts.Lifecycle == "" {
		opts.Lifecycle = domain.IssueWorkflowBacklog
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
			implementations = dedupeTrimmed(parentTask.Implementations)
		}
	}
	candidateImplementations := dedupeTrimmed(implementations)
	implementations, err = resolveIssueWriteImplementations(ctx, deps, implementations)
	if err != nil {
		if issueWriteImplementationValidationTimedOut(err) {
			// A freshness timeout cannot safely prove implementation ambiguity.
			// Preserve explicit/inherited metadata when present; otherwise omit it
			// and let the daemon/default create path handle the issue.
			implementations = candidateImplementations
		} else {
			return issueCreateResult{}, err
		}
	}

	taskID, err := commandWithDaemonAutostartRetry(ctx, deps, func(callCtx context.Context) (string, error) {
		return deps.DaemonClient.CreateTask(callCtx, daemonclient.TaskCreateParams{
			Title:           opts.Title,
			Description:     opts.Description,
			Type:            opts.Type,
			Priority:        opts.Priority,
			Lifecycle:       opts.Lifecycle,
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
		parentSource := "auto-parent from AZEDARACH_ISSUE_ID"
		if opts.ExplicitParent {
			parentSource = "explicit --parent"
		}
		message = fmt.Sprintf("%s (parent: %s, %s)", message, parentIDValue, parentSource)
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
		if strings.Contains(err.Error(), "context risk is high") {
			if contextRisk, riskErr := loadIssueContextRiskForCloseout(ctx, deps, opts.IssueID); riskErr == nil && contextRisk != nil {
				if opts.JSON {
					_ = printJSON(map[string]any{
						"issue_id":     opts.IssueID,
						"closed":       false,
						"context_risk": contextRisk,
						"blocked":      true,
						"reason":       issueContextRiskCloseoutBlockMessage(opts.IssueID),
					})
				} else {
					printIssueContextRiskCloseout(contextRisk)
				}
			}
		}
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
			"context_risk":              result.ContextRisk,
		})
	}
	fmt.Printf("Closed issue: %s\n", opts.IssueID)
	printIssueContextRiskCloseout(result.ContextRisk)
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

func IssueCleanupCommand(deps *Dependencies, opts IssueCleanupOptions) error {
	restoreProject := applyIssueProjectOverride(deps, opts.Project)
	defer restoreProject()
	startupCtx, startupCancel := context.WithTimeout(context.Background(), issueCloseCleanupTimeout)
	if err := ensureDaemon(startupCtx, deps, "cli"); err != nil {
		startupCancel()
		return err
	}
	startupCancel()
	// The daemon applies an independent deadline to every selected issue. A
	// batch-wide deadline derived from that same duration would starve later
	// items in a sequential batch, so the transport parent remains cancellable
	// but intentionally has no deadline of its own.
	ctx, cancel := newIssueCleanupBatchContext()
	defer cancel()
	result, err := deps.DaemonClient.BulkCleanupTasks(ctx, daemonclient.TaskBulkCleanupRequest{
		TaskIDs: opts.IDs, Statuses: opts.Statuses, Query: opts.Query, UpdatedBefore: opts.UpdatedBefore,
		Limit: opts.Limit, DryRun: opts.DryRun, CloseOutcome: opts.Action, PerIssueTimeout: opts.PerIssueTimeout,
	})
	if err != nil {
		return fmt.Errorf("bulk issue cleanup failed: %w", err)
	}
	failures := 0
	for _, item := range result.Items {
		if item.Error != "" {
			failures++
		}
	}
	if opts.JSON {
		if err := printJSON(result); err != nil {
			return err
		}
		if failures > 0 {
			return fmt.Errorf("%d issue cleanup operation(s) failed", failures)
		}
		return nil
	}
	verb := "Cleanup"
	if opts.DryRun {
		verb = "Would clean up"
	}
	for _, item := range result.Items {
		state := "ok"
		if item.Skipped {
			state = "already " + item.Action
		}
		if item.Error != "" {
			state = "failed: " + item.Error
		}
		fmt.Printf("%s %s as %s: %s\n", verb, item.TaskID, item.Action, state)
	}
	if failures > 0 {
		return fmt.Errorf("%d issue cleanup operation(s) failed", failures)
	}
	return nil
}

func newIssueCleanupBatchContext() (context.Context, context.CancelFunc) {
	return context.WithCancel(context.Background())
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
		if hook := strings.TrimSpace(phase.Hook); hook != "" {
			item["hook"] = hook
		}
		if command := strings.TrimSpace(phase.Command); command != "" {
			item["command"] = command
		}
		if phase.ExitStatus != nil {
			item["exit_status"] = *phase.ExitStatus
		}
		if phase.Blocking != nil {
			item["blocking"] = *phase.Blocking
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
		details := []string{}
		if hook := strings.TrimSpace(phase.Hook); hook != "" {
			details = append(details, "hook="+hook)
		}
		if command := strings.TrimSpace(phase.Command); command != "" {
			details = append(details, "command="+command)
		}
		if phase.ExitStatus != nil {
			details = append(details, fmt.Sprintf("exit_status=%d", *phase.ExitStatus))
		}
		if phase.Blocking != nil {
			details = append(details, fmt.Sprintf("blocking=%t", *phase.Blocking))
		}
		if len(details) > 0 {
			suffix += " [" + strings.Join(details, " ") + "]"
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

func IssueUnarchiveCommand(deps *Dependencies, opts IssueUnarchiveOptions) error {
	restoreProject := applyIssueProjectOverride(deps, opts.Project)
	defer restoreProject()

	ctx, cancel := context.WithTimeout(context.Background(), daemonCommandTimeout)
	defer cancel()
	if err := ensureDaemon(ctx, deps, "cli"); err != nil {
		return err
	}
	if err := deps.DaemonClient.UnarchiveTaskWithOptions(ctx, opts.IssueID, daemonclient.TaskUnarchiveOptions{
		WithParents:     opts.WithParents,
		CascadeChildren: opts.CascadeChildren,
	}); err != nil {
		return fmt.Errorf("failed to unarchive issue: %w", err)
	}
	if opts.JSON {
		return printJSON(map[string]any{
			"issue_id":         opts.IssueID,
			"unarchived":       true,
			"with_parents":     opts.WithParents,
			"cascade_children": opts.CascadeChildren,
		})
	}
	details := []string{}
	if opts.WithParents {
		details = append(details, "with parents")
	}
	if opts.CascadeChildren {
		details = append(details, "with children")
	}
	if len(details) == 0 {
		fmt.Printf("Unarchived issue: %s\n", opts.IssueID)
		return nil
	}
	fmt.Printf("Unarchived issue: %s (%s)\n", opts.IssueID, strings.Join(details, ", "))
	return nil
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
		Lifecycle:   opts.Lifecycle,
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

	var updateContextRisk *domain.IssueContextRiskPacket
	if opts.Status != nil && *opts.Status == domain.StatusInReview {
		var err error
		updateContextRisk, err = loadIssueContextRiskForCloseout(ctx, deps, opts.IssueID)
		if err != nil {
			return err
		}
		if updateContextRisk != nil && domain.IssueContextRiskRequiresStructuredCloseout(*updateContextRisk) {
			if opts.JSON {
				_ = printJSON(map[string]any{
					"issue_id":     opts.IssueID,
					"updated":      false,
					"status_set":   false,
					"context_risk": updateContextRisk,
					"blocked":      true,
					"reason":       issueContextRiskCloseoutBlockMessage(opts.IssueID),
				})
			} else {
				printIssueContextRiskCloseout(updateContextRisk)
			}
			return fmt.Errorf("%s", issueContextRiskCloseoutBlockMessage(opts.IssueID))
		}
	}

	if err := deps.DaemonClient.UpdateTaskDetails(ctx, opts.IssueID, update); err != nil {
		return fmt.Errorf("failed to update issue %s: %w", opts.IssueID, err)
	}
	if opts.Status != nil {
		statusOptions := daemonclient.TaskStatusOptions{}
		if *opts.Status == domain.StatusDone {
			statusOptions = cleanupCloseTaskStatusOptions(opts.ForceWorktree)
		}
		if *opts.Status == domain.StatusCancelled {
			statusOptions.ForceWorktree = opts.ForceWorktree
			statusOptions.CloseOutcome = domain.IssueCloseCancelled
		}
		if *opts.Status == domain.StatusInReview {
			statusOptions.CascadeChildren = opts.CascadeChildren
		}
		if err := deps.DaemonClient.UpdateTaskStatusWithOptions(ctx, opts.IssueID, *opts.Status, statusOptions); err != nil {
			return fmt.Errorf("failed to apply lifecycle action for issue %s: %w", opts.IssueID, err)
		}
		if updateContextRisk != nil && !opts.JSON {
			printIssueContextRiskCloseout(updateContextRisk)
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
			"context_risk":   updateContextRisk,
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
		TaskID              string  `json:"task_id,omitempty"`
		ID                  string  `json:"id,omitempty"`
		Title               string  `json:"title,omitempty"`
		Description         *string `json:"description,omitempty"`
		Type                string  `json:"type,omitempty"`
		Priority            string  `json:"priority,omitempty"`
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
		if item.Description != nil {
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
			if item.Description != nil {
				update.Description = *item.Description
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

func filterTasksByIssueDisplayPhase(tasks []domain.Task, phases []domain.IssueDisplayPhase) []domain.Task {
	if len(phases) == 0 {
		return tasks
	}
	phaseSet := make(map[domain.IssueDisplayPhase]struct{}, len(phases))
	for _, phase := range phases {
		phaseSet[phase] = struct{}{}
	}
	filtered := make([]domain.Task, 0, len(tasks))
	for _, task := range tasks {
		if _, ok := phaseSet[task.IssueFacts().DisplayPhase]; ok {
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

func renderIssueOwnershipSummary(ownership *domain.IssueOwnership) string {
	if ownership == nil || strings.TrimSpace(ownership.OwnerID) == "" {
		return ""
	}
	kind := strings.TrimSpace(ownership.OwnerKind)
	if kind == "" {
		kind = "owner"
	}
	parts := []string{fmt.Sprintf("%s:%s", kind, strings.TrimSpace(ownership.OwnerID))}
	if !ownership.ClaimedAt.IsZero() {
		parts = append(parts, "claimed "+ownership.ClaimedAt.UTC().Format(time.RFC3339))
	}
	if ownership.ExpiresAt != nil && !ownership.ExpiresAt.IsZero() {
		parts = append(parts, "expires "+ownership.ExpiresAt.UTC().Format(time.RFC3339))
	}
	return strings.Join(parts, ", ")
}

func renderIssueOwnershipListCell(ownership *domain.IssueOwnership) string {
	if ownership == nil || strings.TrimSpace(ownership.OwnerID) == "" {
		return "-"
	}
	ownerID := strings.TrimSpace(ownership.OwnerID)
	kind := strings.TrimSpace(ownership.OwnerKind)
	if kind == "" {
		return ownerID
	}
	return kind + ":" + ownerID
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
	case domain.TypeTask, domain.TypeBug, domain.TypeFeature, domain.TypeEpic, domain.TypeChore, domain.TypeInvestigation:
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
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "open":
		return domain.StatusOpen, nil
	case "in_progress":
		return domain.StatusInProgress, nil
	case "in_review":
		return domain.StatusInReview, nil
	case "closed":
		return domain.StatusDone, nil
	case "cancelled", "canceled":
		return domain.StatusCancelled, nil
	default:
		return "", fmt.Errorf("invalid status: %s", raw)
	}
}

func parseIssueUpdateStatus(raw string) (domain.Status, *domain.IssueWorkflow, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "backlog":
		lifecycle := domain.IssueWorkflowBacklog
		return "", &lifecycle, nil
	case "open":
		lifecycle := domain.IssueWorkflowOpen
		return "", &lifecycle, nil
	default:
		status, err := parseStatus(raw)
		return status, nil, err
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
	OrchestrationSection     string
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
	return primeCommandTo(deps, os.Stdout, clitext.Render)
}

type primeRenderFunc func(string, any) (string, error)

func primeCommandTo(deps *Dependencies, stdout io.Writer, render primeRenderFunc) error {
	commandStartedAt := time.Now()
	issueID := strings.TrimSpace(os.Getenv("AZEDARACH_ISSUE_ID"))
	issueIDSource := ""
	if issueID != "" {
		issueIDSource = "env"
	}
	primeMode := strings.TrimSpace(os.Getenv("AZEDARACH_PRIME_MODE"))
	guardrail := "- No active ticket is preselected. When work starts, set `AZEDARACH_TICKET_ID` (or legacy `AZEDARACH_ISSUE_ID`) or run `az ticket get <ticket-id>`."
	issueSection := ""
	activeIssueClosedWarning := ""
	specGuardrails := ""
	questionFirstGuardrails := ""
	orchestratorExitContract := ""
	implementationSection := ""
	implementationGuardrails := "- `--impl` selects implementation/spec variants; it never establishes parentage or graph membership. Use the active ticket context or an explicit parent-child edge for hierarchy."
	learningSection := ""
	var learningConfirmation *protocol.LearnActivationConfirmRequestBody
	specEnabled := deps != nil && deps.Config != nil && deps.Config.Spec.Enabled
	orchestrationVia := primeOrchestrationVia(deps)
	orchestrationViaAz := strings.EqualFold(orchestrationVia, "az")
	orchestrationViaNative := strings.EqualFold(orchestrationVia, "native")
	tmuxAvailable := primeTmuxAvailable()
	primeOrchestrator := false
	orchestrationSection := ""
	if tmuxAvailable && deps != nil && deps.DaemonClient != nil {
		if sessionID, err := tmuxPaneSessionName(context.Background()); err == nil && strings.TrimSpace(sessionID) != "" {
			ctx, cancel := context.WithTimeout(context.Background(), primeDaemonReadTimeout)
			finish := primePhase(deps, "orchestration_snapshot", "loading daemon orchestration scope snapshot")
			orchestrationSnapshot, snapshotErr := deps.DaemonClient.OrchestrationSnapshot(ctx, protocol.OrchestrationSnapshotRequest{
				SessionID: strings.TrimSpace(sessionID), ActorID: defaultIssueOwnerID(),
			})
			cancel()
			finish(snapshotErr)
			if snapshotErr == nil && orchestrationSnapshot.Role == "orchestrator" {
				primeOrchestrator = true
				issueID = orchestrationSnapshot.Scope.RootIssueID.String()
				if issueID != "" {
					issueIDSource = "daemon"
					guardrail = fmt.Sprintf("- The daemon identifies this session as the rooted orchestrator for `%s`; its persisted scope is authority over environment-derived startup context.", issueID)
				} else {
					issueIDSource = ""
					guardrail = "- The daemon identifies this session as the project orchestrator; remain project-scoped and do not invent an active worker issue from environment context."
				}
				orchestrationSection = renderPrimeOrchestrationSection(orchestrationSnapshot)
				if issueID != "" {
					orchestratorExitContract = renderPrimeOrchestratorExitContract(issueID)
				}
			}
		}
	}

	if primeMode == "question-first" {
		questionFirstGuardrails = `- Question-first execution rules (Space+Q mode):
  - MUST ask follow-up questions immediately when the issue is underspecified or ambiguous.
  - MUST improve the current issue title and description before implementation work begins.
  - MUST record unknowns/open questions in the issue description so scope is explicit.`
	}
	if specEnabled {
		specGuardrails = `- Specs are daemon-backed ` + "`az spec req`" + `/` + "`az spec link`" + ` records, not repository prose.
- Read linked requirements before behavior work. Search nearby requirements when links are missing; link an existing contract or create/update one before implementation.
- Update the spec before code when behavior, persistence, CLI/API/TUI contracts, or invariants change.
- Contract-preserving refactors, tests, tooling, docs, and repairs normally need explicit ` + "`Spec impact: none (...)`" + ` evidence rather than a new requirement.
- Keep implementation issues linked to the requirements they implement or verify.`
	}

	var snapshot daemonclient.TaskSnapshot
	snapshotLoaded := false
	if !primeOrchestrator && deps != nil && deps.DaemonClient != nil {
		if issueID != "" {
			ctx, cancel := context.WithTimeout(context.Background(), primeDaemonReadTimeout)
			finish := primePhase(deps, "issue_snapshot", fmt.Sprintf("loading active issue snapshot for %s", issueID))
			loaded, err := deps.DaemonClient.GetTaskSnapshot(ctx, issueID)
			cancel()
			finish(err)
			if err == nil {
				snapshot = loaded
				snapshotLoaded = true
			}
		} else {
			ctx, cancel := context.WithTimeout(context.Background(), primeDaemonReadTimeout)
			finish := primePhase(deps, "task_snapshot", "loading daemon issue snapshot")
			loaded, err := deps.DaemonClient.ListTasksSnapshotWithDependencies(ctx)
			cancel()
			finish(err)
			if err == nil {
				snapshot = loaded
				snapshotLoaded = true
				implementationSection = renderPrimeImplementationSection(configuredIssueImplementations(snapshot.Tasks))
			}
		}
	}
	if issueID == "" && snapshotLoaded {
		if tmuxIssueID, ok := activeIssueIDFromTmuxPaneInSnapshot(context.Background(), deps, snapshot); ok {
			issueID = tmuxIssueID
			issueIDSource = "tmux"
		}
	}

	if !primeOrchestrator && issueID != "" {
		if issueIDSource == "tmux" {
			guardrail = fmt.Sprintf("- `AZEDARACH_TICKET_ID` is absent, but the current tmux session resolves to ticket `%s`; use it as the default ticket scope and refresh stale context with `az ticket get %s`.", issueID, issueID)
		} else {
			guardrail = fmt.Sprintf("- The active ticket scope is `%s`; refresh stale context with `az ticket get %s`. `AZEDARACH_ISSUE_ID` remains a legacy compatibility variable.", issueID, issueID)
		}
		ownerID := defaultIssueOwnerID()
		if !snapshotLoaded {
			issueSection = fmt.Sprintf("Active ticket context (ticket=%s):\nCould not load ticket details automatically; run `az ticket get %s`.\n", issueID, issueID)
		} else if task, ok := findTaskByID(snapshot.Tasks, issueID); ok {
			if err := snapshot.RequireFullDetails("prime active issue snapshot"); err != nil {
				if detailTask, err := loadPrimeIssueDetailTask(context.Background(), deps, issueID); err == nil {
					task = detailTask
				}
			}
			if !task.IssueClosed() {
				if err := claimPrimeActiveIssue(context.Background(), deps, issueID, ownerID, task.Ownership); err != nil {
					return err
				}
			}
			readiness, hasReadiness := loadPrimeTaskGraphReadiness(context.Background(), deps, issueID, ownerID)
			if orchestrationViaAz && hasReadiness && primeTaskGraphReadinessHasGraphState(issueID, readiness) {
				orchestratorExitContract = renderPrimeOrchestratorExitContract(issueID)
			}
			observations := readiness.WorkerObservations
			containmentRisks := readiness.ContainmentRisks
			if task.ParentID != nil && strings.TrimSpace(task.ParentID.String()) != "" {
				if parentReadiness, ok := loadPrimeTaskGraphReadiness(context.Background(), deps, strings.TrimSpace(task.ParentID.String()), ownerID); ok {
					containmentRisks = parentReadiness.ContainmentRisks
				}
			}
			if orchestratorExitContract == "" && primeIssueIsAzOrchestrationRoot(task, snapshot.Tasks, orchestrationViaAz) {
				orchestratorExitContract = renderPrimeOrchestratorExitContract(issueID)
			}
			issueSection = renderPrimeIssueSection(issueID, task, snapshot.Tasks, observations, containmentRisks, tmuxAvailable)
			if task.IssueClosed() {
				activeIssueClosedWarning = fmt.Sprintf("- Active ticket `%s` is currently `%s`; start by picking/opening actionable work (for example `az ticket list --limit 20` or `az ticket create \"Next task\"`). Use `--deferred` only for standalone backlog work.", task.ID, task.IssueDisplayStatusText())
			}
		} else {
			readiness, hasReadiness := loadPrimeTaskGraphReadiness(context.Background(), deps, issueID, ownerID)
			if orchestrationViaAz && hasReadiness && primeTaskGraphReadinessHasGraphState(issueID, readiness) {
				orchestratorExitContract = renderPrimeOrchestratorExitContract(issueID)
			}
			issueSection = fmt.Sprintf("Active ticket context (ticket=%s):\nTicket not found in current project snapshot; run `az ticket get %s`.\n", issueID, issueID)
		}
	}
	if !primeOrchestrator && issueID != "" {
		learning := renderPrimeLearningSection(context.Background(), deps, issueID)
		learningSection, learningConfirmation = learning.Text, learning.Confirmation
	}

	finishRender := primePhase(deps, "render", "rendering primer output")
	output, err := render("prime_output", primeTemplateData{
		ActiveIssueID:            issueID,
		SpecEnabled:              specEnabled,
		PrimeEvidenceKey:         primeEvidenceKey,
		OrchestrationVia:         orchestrationVia,
		OrchestrationViaAz:       orchestrationViaAz,
		OrchestrationViaNative:   orchestrationViaNative,
		TmuxAvailable:            tmuxAvailable,
		OrchestratorExitContract: orchestratorExitContract,
		OrchestrationSection:     orchestrationSection,
		IssueSection:             issueSection,
		ActiveIssueClosedWarning: activeIssueClosedWarning,
		ContextGuardrail:         guardrail,
		QuestionFirstGuardrails:  questionFirstGuardrails,
		ImplementationSection:    implementationSection,
		ImplementationGuardrails: implementationGuardrails,
		LearningSection:          learningSection,
		SpecGuardrails:           specGuardrails,
	})
	finishRender(err)
	if err != nil {
		return errors.Join(fmt.Errorf("render prime output: %w", err), abandonPrimeLearningActivation(deps, learningConfirmation, "render_failed"))
	}
	if _, err := io.WriteString(stdout, output); err != nil {
		return errors.Join(fmt.Errorf("write prime output: %w", err), abandonPrimeLearningActivation(deps, learningConfirmation, "write_failed"))
	}
	if learningConfirmation != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if _, err := deps.DaemonClient.ConfirmLearningActivation(ctx, *learningConfirmation); err != nil {
			return fmt.Errorf("confirm prime learning delivery: %w", err)
		}
	}
	latencytrace.LogPhaseContext(primeTraceContext(deps), primeLogger(deps), "cli", "prime.total", commandStartedAt, "issue_id", issueID)
	return nil
}

func abandonPrimeLearningActivation(deps *Dependencies, confirmation *protocol.LearnActivationConfirmRequestBody, reason string) error {
	if deps == nil || deps.DaemonClient == nil || confirmation == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, err := deps.DaemonClient.AbandonLearningActivation(ctx, protocol.LearnActivationAbandonRequestBody{ActivationID: confirmation.ActivationID, Reason: reason})
	return err
}

func primePhase(deps *Dependencies, phase, message string) func(error) {
	startedAt := time.Now()
	return func(err error) {
		attrs := []any{"phase", phase}
		if err != nil {
			attrs = append(attrs, "error", err)
		}
		latencytrace.LogPhaseContext(primeTraceContext(deps), primeLogger(deps), "cli", "prime."+phase, startedAt, attrs...)
	}
}

func primeLogger(deps *Dependencies) *slog.Logger {
	if deps == nil {
		return nil
	}
	return deps.Logger
}

func primeTraceContext(deps *Dependencies) context.Context {
	if deps == nil || deps.TraceContext == nil {
		return context.Background()
	}
	return deps.TraceContext
}

type primeLearningSection struct {
	Text         string
	Confirmation *protocol.LearnActivationConfirmRequestBody
}

func renderPrimeLearningSection(ctx context.Context, deps *Dependencies, issueID string) primeLearningSection {
	issueID = strings.TrimSpace(issueID)
	if issueID == "" {
		return primeLearningSection{}
	}
	lines := []string{
		"Learning capture:",
		fmt.Sprintf("- Before handoff, review, or context switch, capture reusable evidence-backed discoveries with `az learn add --issue %s --tag <tag> --evidence \"<observation + command/file evidence>\"`.", issueID),
		"- Use `--private` for sensitive local details; promote durable guidance later with `az learn review` and `az learn promote`.",
	}
	if deps == nil || deps.DaemonClient == nil {
		return primeLearningSection{Text: strings.Join(lines, "\n")}
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	projectID := strings.TrimSpace(deps.ProjectID)
	if projectID == "" {
		projectID = protocol.DefaultProjectID
	}
	sessionID := naming.CanonicalSessionID(projectID, issueID)
	finish := primePhase(deps, "learning_recall", fmt.Sprintf("loading learning recall for %s", issueID))
	resp, err := deps.DaemonClient.ActivateContextualLearnings(ctx, protocol.LearnContextualActivateRequestBody{Purpose: string(domain.LearningPurposeSessionStart), Surface: "prime", SessionID: naming.SessionID(sessionID), ContextIssueID: naming.IssueID(issueID), TokenBudget: 256})
	finish(err)
	if err != nil || resp.Proposal == nil || len(resp.Learnings) == 0 {
		return primeLearningSection{Text: strings.Join(lines, "\n")}
	}
	section := []string{"", fmt.Sprintf("Relevant accepted/promoted learnings [activation: %s]:", resp.Proposal.ActivationID)}
	for _, learning := range resp.Learnings {
		reason := strings.TrimSpace(learning.RecallReason)
		if reason != "" {
			reason = " (why: " + reason + ")"
		}
		section = append(section, fmt.Sprintf("- %s [%s]: %s%s", learning.ID, learning.Status, learning.Summary, reason))
	}
	section = append(section, "Use `az learn show <learning-id>` for evidence; long evidence is not injected by default.")
	section = append(section, fmt.Sprintf("Record the activation outcome with `az learn feedback --idempotency-key <key> --outcome helpful|followed|contradicted|unknown %s`.", resp.Proposal.ActivationID))
	rendered := strings.Join(section, "\n")
	lines = append(lines, section...)
	return primeLearningSection{Text: strings.Join(lines, "\n"), Confirmation: &protocol.LearnActivationConfirmRequestBody{ActivationID: resp.Proposal.ActivationID, TokenCost: domain.RenderedLearningTokenCost(rendered)}}
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
	ctx, endSpan := latencytrace.StartSpan(ctx, "dependency", "tmux",
		"dependency.name", "tmux",
		"dependency.operation", "display-message",
		"arg_count", 5,
	)
	var spanErr error
	defer func() { endSpan(spanErr) }()
	cmd := exec.CommandContext(ctx, "tmux", "display-message", "-p", "-t", paneID, "#{session_name}")
	out, err := cmd.Output()
	if err != nil {
		spanErr = err
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

func loadPrimeIssueDetailTask(ctx context.Context, deps *Dependencies, issueID string) (domain.Task, error) {
	ctx, cancel := context.WithTimeout(ctx, primeDaemonReadTimeout)
	finish := primePhase(deps, "issue_detail", fmt.Sprintf("loading issue detail for %s", issueID))
	task, err := loadIssueDetailTask(ctx, deps, issueID)
	cancel()
	finish(err)
	return task, err
}

func claimPrimeActiveIssue(ctx context.Context, deps *Dependencies, issueID, ownerID string, ownership *domain.IssueOwnership) error {
	if deps == nil || deps.DaemonClient == nil || strings.TrimSpace(issueID) == "" {
		return nil
	}
	ownerID = strings.TrimSpace(ownerID)
	if ownerID == "" {
		return fmt.Errorf("claim active issue %s: current actor cannot be inferred; set AZEDARACH_AUDIT_ACTOR or USER", issueID)
	}
	if ownership != nil && ownership.OwnedBy(ownerID, time.Now().UTC()) {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, primeDaemonReadTimeout)
	finish := primePhase(deps, "issue_claim", fmt.Sprintf("claiming active issue %s", issueID))
	_, err := deps.DaemonClient.ClaimTaskOwnership(ctx, issueID, daemonclient.TaskOwnershipRequest{
		OwnerID:   ownerID,
		OwnerKind: "agent",
	})
	cancel()
	finish(err)
	if err == nil {
		return nil
	}
	var commandErr *daemonclient.CommandError
	if errors.As(err, &commandErr) && commandErr.Code == protocol.ErrorCodeConflict {
		reason := strings.TrimSpace(commandErr.Message)
		if reason == "" {
			reason = "owned by another actor"
		}
		return fmt.Errorf("active issue %s is already claimed: %s", issueID, reason)
	}
	return fmt.Errorf("claim active issue %s: %w", issueID, err)
}

func loadPrimeTaskGraphReadiness(ctx context.Context, deps *Dependencies, issueID, actorID string) (daemonclient.TaskGraphReadiness, bool) {
	if deps == nil || deps.DaemonClient == nil || strings.TrimSpace(issueID) == "" {
		return daemonclient.TaskGraphReadiness{}, false
	}
	ctx, cancel := context.WithTimeout(ctx, primeDaemonReadTimeout)
	finish := primePhase(deps, "graph_readiness", fmt.Sprintf("loading graph readiness for %s", issueID))
	ready, err := deps.DaemonClient.TaskGraphReadinessForActor(ctx, issueID, actorID)
	cancel()
	finish(err)
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
	return fmt.Sprintf("- Implementation selection (multi-implementation project):\n"+
		"  - Available implementations: %s\n"+
		"  - Choose with `--impl` on creation; repeat it only for intentionally shared work. Use `--update-impl` only when changing an existing issue's assignments.\n",
		strings.Join(quoted, ", "))
}

func renderPrimeIssueSection(issueID string, task domain.Task, tasks []domain.Task, observations []domain.WorkerObservation, containmentRisks []daemonclient.TaskContainmentRisk, tmuxAvailable bool) string {
	structuredContext := renderPrimeStructuredIssueContext(issueID, task)
	implementations := formatPrimeImplementations(task.Implementations)
	parent := ""
	mailbox := ""
	if task.ParentID != nil && strings.TrimSpace(task.ParentID.String()) != "" {
		parentID := strings.TrimSpace(task.ParentID.String())
		parent = fmt.Sprintf("\nParent: %s", parentID)
		mailbox = fmt.Sprintf("- Worker coordination parent: `%s`. Read inbound events with `az mail list --parent %s --since 0 --json`; report validated `worker_evidence.v1` only when the parent watch is active.\n", parentID, parentID)
	}
	childWorkRecommendation := renderPrimeChildWorkRecommendation(task, tasks, tmuxAvailable)
	observationSection := renderPrimeWorkerObservationSection(observations)
	containmentSection := renderPrimeContainmentRiskSection(issueID, containmentRisks)
	return fmt.Sprintf(
		"Active ticket context (ticket=%s):\nRefresh with `az ticket get %s` if this looks stale.\n```\n%s: %s [status=%s priority=%s type=%s impl=%s]%s%s\nDependencies:\n%s\n```\n%s%s%s%s",
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
		containmentSection,
		childWorkRecommendation,
		mailbox,
	)
}

func renderPrimeOrchestratorExitContract(rootIssueID string) string {
	return fmt.Sprintf(`Orchestrator Exit Contract (root %s):
- Remain in the active orchestration turn/loop after starting workers, nested orchestrators, or a background watch; startup is not a completed handoff to the human.
- Continuously consume the root watch and react to worker/nested-orchestrator progress, blocked, and integration-ready evidence while graph work remains.
- Supervise nested epic/root orchestrators as direct children while they own their descendant workers; do not flatten or take over those descendants unless explicitly requested.
- Review and integrate accepted children/epics, advance newly unblocked work, and repeat status/start/watch/review until `+"`az orchestrate complete-check --root %s`"+` passes; then run root validation.
- Set the root `+"`in_review`"+` and report to the human without stopping its session or cleaning its worktree.
- Close/integrate the root only after explicit human acceptance.
`, rootIssueID, rootIssueID)
}

func renderPrimeOrchestrationSection(snapshot protocol.OrchestrationSnapshot) string {
	var b strings.Builder
	scope := "project"
	if snapshot.Scope.Kind == domain.OrchestrationScopeRooted {
		scope = "rooted:" + snapshot.Scope.RootIssueID.String()
	}
	fmt.Fprintf(&b, "Daemon orchestration context (role=orchestrator scope=%s lifecycle=%s revision=%d cursor=%d):\n", scope, snapshot.Lifecycle, snapshot.Revision, snapshot.Cursor)
	if snapshot.ContinuationRequired {
		fmt.Fprintf(&b, "- Runtime persistence guard: wake-required (%s). Durable continuation: %s\n", snapshot.ContinuationReason, snapshot.ContinuationContract)
	} else if snapshot.Scope.Kind == domain.OrchestrationScopeRooted {
		b.WriteString("- Runtime persistence guard: daemon-enforced; idle/turn completion wakes this parent while direct nested roots remain, except after complete-check passes or while explicit human acceptance is pending.\n")
	}
	fmt.Fprintf(&b, "- Capacity: active=%d runnable=%d total=%d/%d; wave limit=%d.\n", snapshot.Capacity.DirectActiveCount, snapshot.Capacity.DirectRunnableCount, snapshot.Capacity.TotalCountingCapacityCount, snapshot.Constraints.AgentCapacity, snapshot.Constraints.StartLimit)
	renderCandidates := func(label string, candidates []protocol.OrchestrationCandidate) {
		if len(candidates) == 0 {
			return
		}
		fmt.Fprintf(&b, "- %s:\n", label)
		for _, candidate := range candidates {
			fmt.Fprintf(&b, "  - %s: %s (%s)\n", candidate.IssueID, candidate.Classification, candidate.Reason)
		}
	}
	generalCandidates := make([]protocol.OrchestrationCandidate, 0, len(snapshot.Candidates))
	for _, candidate := range snapshot.Candidates {
		if candidate.Classification != string(domain.OrchestrationCandidateReviewReady) && candidate.Classification != string(domain.OrchestrationCandidateOwnedElsewhere) {
			generalCandidates = append(generalCandidates, candidate)
		}
	}
	renderCandidates("Candidates", generalCandidates)
	renderCandidates("Reviews", snapshot.Reviews)
	renderCandidates("Ownership conflicts", snapshot.OwnershipConflicts)
	if len(snapshot.Blocked) > 0 {
		ids := make([]string, 0, len(snapshot.Blocked))
		for id := range snapshot.Blocked {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		b.WriteString("- Blockers:\n")
		for _, id := range ids {
			fmt.Fprintf(&b, "  - %s: %s\n", id, snapshot.Blocked[id])
		}
	}
	if len(snapshot.Interactions) > 0 {
		b.WriteString("- Waiting-human interactions:\n")
		for _, interaction := range snapshot.Interactions {
			fmt.Fprintf(&b, "  - %s (%s): %s\n", interaction.ID, interaction.IssueID, interaction.Question)
		}
	}
	if len(snapshot.RecentEvents) > 0 {
		b.WriteString("- Recent coordination events:\n")
		for _, event := range snapshot.RecentEvents {
			fmt.Fprintf(&b, "  - #%d %s %s: %s\n", event.Seq, event.Type, event.IssueID, summarizePrimeDescription(event.IssueID.String(), event.Body))
		}
	}
	if len(snapshot.Constraints.Commands) > 0 {
		fmt.Fprintf(&b, "- Commands: %s.\n", strings.Join(snapshot.Constraints.Commands, ", "))
	}
	for _, guardrail := range snapshot.Constraints.RoleGuardrails {
		fmt.Fprintf(&b, "- Constraint: %s.\n", guardrail)
	}
	return strings.TrimSpace(b.String())
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

func renderPrimeContainmentRiskSection(issueID string, risks []daemonclient.TaskContainmentRisk) string {
	if len(risks) == 0 {
		return ""
	}
	issueID = strings.TrimSpace(issueID)
	filtered := make([]daemonclient.TaskContainmentRisk, 0, len(risks))
	for _, risk := range risks {
		if issueID != "" && !naming.IssueIDsEqual(risk.IssueID, issueID) {
			continue
		}
		filtered = append(filtered, risk)
	}
	if len(filtered) == 0 {
		return ""
	}
	sort.SliceStable(filtered, func(i, j int) bool {
		if filtered[i].ClosedChildIssueID != filtered[j].ClosedChildIssueID {
			return filtered[i].ClosedChildIssueID < filtered[j].ClosedChildIssueID
		}
		return filtered[i].EvidenceCommit < filtered[j].EvidenceCommit
	})
	filteredTotal := len(filtered)
	if len(filtered) > 5 {
		filtered = filtered[:5]
	}
	var b strings.Builder
	b.WriteString("- Containment risks:\n")
	for _, risk := range filtered {
		label := "Containment risk"
		if strings.TrimSpace(risk.Classification) == "stale_child_branch" {
			label = "Stale child branch"
		}
		message := strings.TrimSpace(risk.Message)
		if message == "" {
			message = fmt.Sprintf("closed child evidence %s from %s has root_contains=%t active_contains=%t", shortCLICommitHash(risk.EvidenceCommit), risk.ClosedChildIssueID, risk.RootContainsEvidence, risk.ActiveContainsEvidence)
		}
		fmt.Fprintf(&b, "  - %s: %s\n", label, message)
		fmt.Fprintf(&b, "    Evidence: child=%s commit=%s root_contains=%t active_contains=%t\n", risk.ClosedChildIssueID, shortCLICommitHash(risk.EvidenceCommit), risk.RootContainsEvidence, risk.ActiveContainsEvidence)
		if len(risk.OverlapFiles) > 0 {
			fmt.Fprintf(&b, "    Overlap: %s\n", strings.Join(risk.OverlapFiles, ", "))
		}
		if strings.TrimSpace(risk.SuggestedCommand) != "" {
			fmt.Fprintf(&b, "    Next: %s\n", strings.TrimSpace(risk.SuggestedCommand))
		}
	}
	if filteredTotal > len(filtered) {
		fmt.Fprintf(&b, "  - ... %d more containment risks omitted; run `az orchestrate status --root <root> --json` for full projection.\n", filteredTotal-len(filtered))
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
	commands := "`az ticket create \"Child task\"` for tracking-only work"
	if tmuxAvailable {
		commands += " or `az ticket split \"Child task\"` for an immediate isolated worker"
	}
	return fmt.Sprintf("- Parent context: `%s` is an epic or has children. Keep implementation-sized scope in child tickets using %s; execute each child from its own worktree/session and account for every child before root handoff.\n", task.ID, commands)
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
	return fmt.Sprintf("%s\n… (truncated; run `az ticket get %s` for full context)", snippet, issueID)
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
		runtimeRepoDir = resolveRuntimeRepoDir(deps.TraceContext, repoDir)
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

func resolveRuntimeRepoDir(ctx context.Context, repoDir string) string {
	if !config.UseScopedDaemonRuntimeFor(repoDir) {
		return repoDir
	}
	if worktreeRoot, ok := resolveScopedWorktreeRoot(ctx, repoDir); ok {
		return worktreeRoot
	}
	return repoDir
}

func resolveScopedWorktreeRoot(ctx context.Context, startPath string) (string, bool) {
	candidates := make([]string, 0, 2)
	if cwd, err := os.Getwd(); err == nil && strings.TrimSpace(cwd) != "" {
		candidates = append(candidates, cwd)
	}
	if candidate := strings.TrimSpace(startPath); candidate != "" {
		candidates = append(candidates, candidate)
	}
	for _, candidate := range candidates {
		worktreeRoot, err := config.ResolveWorktreeRootContext(ctx, candidate)
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

func printOperationQueue(snapshot protocol.OperationQueueResponseBody, asJSON, asTree bool) error {
	if asJSON {
		return printJSON(snapshot)
	}
	if asTree {
		return printOperationQueueTree(snapshot)
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tSTATE\tKIND\tISSUE\tBLOCKED_BY\tRESOURCES")
	for _, entry := range appendOperationQueueEntries(snapshot.Running, snapshot.Queued) {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n",
			entry.Operation.OperationID,
			entry.Operation.State,
			entry.Operation.Kind,
			entry.Operation.IssueID,
			joinOperationIDs(entry.BlockingOperationIDs),
			strings.Join(entry.BlockedResourceKeys, ","),
		)
	}
	return w.Flush()
}

func printOperationQueueTree(snapshot protocol.OperationQueueResponseBody) error {
	waitingByBlocker := make(map[string][]protocol.OperationQueueEntry)
	var unblockedQueued []protocol.OperationQueueEntry
	for _, entry := range snapshot.Queued {
		if len(entry.BlockingOperationIDs) == 0 {
			unblockedQueued = append(unblockedQueued, entry)
			continue
		}
		for _, blocker := range entry.BlockingOperationIDs {
			waitingByBlocker[blocker.String()] = append(waitingByBlocker[blocker.String()], entry)
		}
	}
	for _, entry := range snapshot.Running {
		fmt.Println(operationQueueLine(entry))
		children := waitingByBlocker[entry.Operation.OperationID.String()]
		for idx, child := range children {
			connector := "`- "
			if idx < len(children)-1 {
				connector = "|- "
			}
			fmt.Println(connector + operationQueueLine(child))
		}
		delete(waitingByBlocker, entry.Operation.OperationID.String())
	}
	for _, entry := range unblockedQueued {
		fmt.Println(operationQueueLine(entry))
	}
	blockerIDs := make([]string, 0, len(waitingByBlocker))
	for blockerID := range waitingByBlocker {
		blockerIDs = append(blockerIDs, blockerID)
	}
	sort.Strings(blockerIDs)
	for _, blockerID := range blockerIDs {
		entries := waitingByBlocker[blockerID]
		for _, entry := range entries {
			fmt.Println(operationQueueLine(entry))
		}
	}
	if len(snapshot.Running) == 0 && len(snapshot.Queued) == 0 {
		fmt.Println("No running or queued operations.")
	}
	return nil
}

func appendOperationQueueEntries(running, queued []protocol.OperationQueueEntry) []protocol.OperationQueueEntry {
	out := make([]protocol.OperationQueueEntry, 0, len(running)+len(queued))
	out = append(out, running...)
	out = append(out, queued...)
	return out
}

func operationQueueLine(entry protocol.OperationQueueEntry) string {
	parts := []string{
		entry.Operation.OperationID.String(),
		string(entry.Operation.State),
		entry.Operation.Kind,
	}
	if entry.Operation.IssueID != "" {
		parts = append(parts, entry.Operation.IssueID.String())
	}
	if len(entry.BlockedResourceKeys) > 0 {
		parts = append(parts, "blocked="+strings.Join(entry.BlockedResourceKeys, ","))
	}
	return strings.Join(parts, " ")
}

func joinOperationIDs(ids []naming.OperationID) string {
	values := make([]string, 0, len(ids))
	for _, id := range ids {
		if id == "" {
			continue
		}
		values = append(values, id.String())
	}
	return strings.Join(values, ",")
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

func parseIssueStatuses(values []string) ([]domain.IssueDisplayPhase, error) {
	if len(values) == 0 {
		return nil, nil
	}
	statuses := make([]domain.IssueDisplayPhase, 0, len(values))
	seen := make(map[domain.IssueDisplayPhase]struct{}, len(values))
	for _, value := range values {
		for _, part := range strings.Split(value, ",") {
			status, err := parseIssueDisplayPhase(strings.TrimSpace(part))
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

func parseIssueDisplayPhase(raw string) (domain.IssueDisplayPhase, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "backlog":
		return domain.IssueDisplayBacklog, nil
	case "open":
		return domain.IssueDisplayOpen, nil
	case "in_progress", "active", "working":
		return domain.IssueDisplayActive, nil
	case "in_review", "review", "review_ready", "review-ready":
		return domain.IssueDisplayReview, nil
	case "closed", "done", "completed", "complete":
		return domain.IssueDisplayDone, nil
	case "cancelled", "canceled":
		return domain.IssueDisplayCancelled, nil
	default:
		return "", fmt.Errorf("invalid status: %s", raw)
	}
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
		latencytrace.LogPhaseContext(ctx, deps.Logger, "cli", "daemon_attach", startedAt, "client_name", clientName, "error", err)
		return fmt.Errorf("daemon attach failed: %w", err)
	}
	if !ack.Accepted {
		latencytrace.LogPhaseContext(ctx, deps.Logger, "cli", "daemon_attach", startedAt, "client_name", clientName, "accepted", false, "reason", ack.Reason)
		return fmt.Errorf("daemon handshake rejected: %s", ack.Reason)
	}
	latencytrace.LogPhaseContext(ctx, deps.Logger, "cli", "daemon_attach", startedAt, "client_name", clientName, "accepted", true, "daemon_version", ack.DaemonVersion)
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
