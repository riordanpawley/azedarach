package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/riordanpawley/azedarach/internal/client/daemonclient"
	"github.com/riordanpawley/azedarach/internal/client/reconnect"
	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/naming"
)

type OrchestrateStatusOptions struct {
	Project     string
	RootIssueID string
	SinceSeq    int64
	Limit       int
	JSON        bool
	Summary     bool
	Full        bool
}

type OrchestrateStartOptions struct {
	Project             string
	RootIssueID         string
	Limit               int
	IssueIDs            []string
	JSON                bool
	BaseBranchOverride  string
	OverrideBoardHealth bool
	IntentKey           string
}

type OrchestrateGroupOptions struct {
	Project       string
	RootIssueID   string
	NestedIssueID string
	IssueIDs      []string
	JSON          bool
}

type OrchestrateWatchOptions struct {
	Project      string
	RootIssueID  string
	SinceSeq     int64
	JSONL        bool
	Once         bool
	Compact      bool
	Full         bool
	Verbose      bool
	PollInterval time.Duration
}

type OrchestrateObserveOptions struct {
	Project     string
	RootIssueID string
	JSON        bool
}

type ObserveOptions struct {
	Project     string
	RootIssueID string
	JSON        bool
}

type OrchestrateCompleteCheckOptions struct {
	Project     string
	RootIssueID string
	JSON        bool
}

type OrchestrateReviewOptions struct {
	Project       string
	RootIssueID   string
	Action        string
	IntentKey     string
	IssueIDs      []string
	Severity      string
	Findings      []string
	RestartWorker bool
	JSON          bool
}

type OrchestratePromptOptions struct {
	Project      string
	RootIssueID  string
	IssueID      string
	Coordination string
	JSON         bool
}

type OrchestrateIntegrateOptions struct {
	Project string
	IssueID string
	Apply   bool
	JSON    bool
}

type OrchestrateCloseSessionOptions struct {
	Project string
	IssueID string
	JSON    bool
}

type OrchestrateMessageOptions struct {
	Project           string
	RootIssueID       string
	IssueID           string
	Type              string
	Body              string
	ForceSelfDelivery bool
	JSON              bool
}

func issueCloseCommand(issueID string) string {
	return fmt.Sprintf("az ticket close --id %s", issueID)
}

func issueCloseCommandForProject(issueID, projectID string) string {
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		return issueCloseCommand(issueID)
	}
	return fmt.Sprintf("az ticket close --project %s --id %s", projectID, issueID)
}

func issueGetCommandForProject(issueID, projectID string) string {
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		return fmt.Sprintf("az issue get %s", issueID)
	}
	return fmt.Sprintf("az issue get --project %s %s", projectID, issueID)
}

func branchMergeCommandForProject(issueID, projectID string) string {
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		return fmt.Sprintf("az branch merge --source %s --target <issue-id|base>", issueID)
	}
	return fmt.Sprintf("az branch merge --project %s --source %s --target <issue-id|base>", projectID, issueID)
}

func orchestrateIntegrateApplyCommandForProject(issueID, projectID string) string {
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		return fmt.Sprintf("az orchestrate integrate --issue %s --apply", issueID)
	}
	return fmt.Sprintf("az orchestrate integrate --project %s --issue %s --apply", projectID, issueID)
}

func orchestrateCloseSessionCommandForProject(issueID, projectID string) string {
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		return fmt.Sprintf("az orchestrate close-session --issue %s", issueID)
	}
	return fmt.Sprintf("az orchestrate close-session --project %s --issue %s", projectID, issueID)
}

var orchestrateObserveNow = func() time.Time {
	return time.Now().UTC()
}

var (
	orchestrateStartWaitTimeout      = sessionStartCommandTimeout
	orchestrateStartWaitPollInterval = 250 * time.Millisecond
)

type orchestrateStatusResult struct {
	RootIssueID            string                               `json:"root_issue_id"`
	Capacity               orchestrateCapacitySummary           `json:"capacity"`
	Runnable               []string                             `json:"runnable"`
	NestedRoots            []orchestrateNestedRoot              `json:"nested_roots,omitempty"`
	Pending                []orchestratePendingStart            `json:"pending,omitempty"`
	PublicationQueue       []domain.PublicationOperation        `json:"publication_queue,omitempty"`
	Active                 []string                             `json:"active,omitempty"`
	ActiveSessions         []orchestrateActiveSession           `json:"active_sessions,omitempty"`
	SessionStartProgress   []orchestrateSessionStartProgress    `json:"session_start_progress,omitempty"`
	StaleCloseableChildren []orchestrateStaleCloseableCandidate `json:"stale_closeable_children,omitempty"`
	ContainmentRisks       []orchestrateContainmentRisk         `json:"containment_risks,omitempty"`
	WorkerObservations     []orchestrateObservation             `json:"worker_observations,omitempty"`
	Blocked                map[string]string                    `json:"blocked"`
	MailboxEvents          []protocol.MailEvent                 `json:"mailbox_events"`
	Warnings               []string                             `json:"warnings,omitempty"`
	Advice                 map[string]interface{}               `json:"advice,omitempty"`
}

type orchestrateCapacitySummary struct {
	DirectRunnableCount        int `json:"direct_runnable_count"`
	DirectActiveCount          int `json:"direct_active_count"`
	NestedStartableCount       int `json:"nested_startable_count"`
	NestedActiveCount          int `json:"nested_active_count"`
	PendingStartsCount         int `json:"pending_starts_count"`
	BlockedNestedRootsCount    int `json:"blocked_nested_roots_count"`
	NotCountingCapacityCount   int `json:"not_counting_capacity_count"`
	TotalCountingCapacityCount int `json:"total_counting_capacity_count"`
}

type orchestrateObserveResult struct {
	Mode         string                        `json:"mode"`
	RootIssueID  string                        `json:"root_issue_id,omitempty"`
	GeneratedAt  time.Time                     `json:"generated_at"`
	Observations []orchestrateObservation      `json:"observations"`
	Groups       []orchestrateObservationGroup `json:"groups"`
	Warnings     []string                      `json:"warnings,omitempty"`
	Advice       map[string]interface{}        `json:"advice,omitempty"`
}

type orchestrateObservation struct {
	IssueID             string                                `json:"issue_id"`
	State               string                                `json:"state"`
	Group               string                                `json:"group"`
	Reason              string                                `json:"reason"`
	Age                 string                                `json:"age"`
	AgeSeconds          int64                                 `json:"age_seconds,omitempty"`
	EvidenceFlags       []string                              `json:"evidence_flags"`
	EvidenceSummary     []string                              `json:"evidence_summary,omitempty"`
	Risks               []string                              `json:"risks,omitempty"`
	NextActions         []string                              `json:"next_actions"`
	LastMeaningfulEvent *domain.WorkerObservationEventSummary `json:"last_meaningful_event,omitempty"`
	SourceTruthPolicy   domain.WorkerObservationSourcePolicy  `json:"source_truth_policy"`
}

type orchestrateObservationGroup struct {
	Name     string   `json:"name"`
	Title    string   `json:"title"`
	IssueIDs []string `json:"issue_ids"`
}

type orchestrateStartResult struct {
	RootIssueID string                    `json:"root_issue_id"`
	IntentKey   string                    `json:"intent_key"`
	Limit       int                       `json:"limit"`
	Requested   []string                  `json:"requested"`
	NestedRoots []string                  `json:"nested_roots,omitempty"`
	Started     []string                  `json:"started"`
	Launched    []orchestrateStartLaunch  `json:"launched,omitempty"`
	Pending     []orchestrateStartPending `json:"pending,omitempty"`
	Skipped     map[string]string         `json:"skipped"`
	Failed      map[string]string         `json:"failed"`
	Warnings    []string                  `json:"warnings,omitempty"`
	Advice      orchestrateStartAdvice    `json:"advice,omitempty"`
}

type orchestrateGroupResult struct {
	RootIssueID   string                 `json:"root_issue_id"`
	NestedIssueID string                 `json:"nested_issue_id"`
	Grouped       []orchestrateGroupItem `json:"grouped"`
	NestedRoot    *orchestrateNestedRoot `json:"nested_root,omitempty"`
	Advice        []string               `json:"advice,omitempty"`
}

type orchestrateGroupItem struct {
	IssueID          string `json:"issue_id"`
	PreviousParentID string `json:"previous_parent_id,omitempty"`
	NewParentID      string `json:"new_parent_id"`
	Changed          bool   `json:"changed"`
}

type orchestrateStartLaunch struct {
	IssueID        string `json:"issue_id"`
	SessionID      string `json:"session_id"`
	WorktreePath   string `json:"worktree_path,omitempty"`
	OperationID    string `json:"operation_id,omitempty"`
	OperationState string `json:"operation_state,omitempty"`
	Warning        string `json:"warning,omitempty"`
	WatchCommand   string `json:"watch_command"`
	IntegrateHint  string `json:"integrate_hint"`
	CloseHint      string `json:"close_hint"`
}

type orchestrateStartPending struct {
	IssueID          string   `json:"issue_id"`
	OperationID      string   `json:"operation_id"`
	OperationState   string   `json:"operation_state"`
	Reason           string   `json:"reason"`
	FollowUpCommands []string `json:"follow_up_commands,omitempty"`
}

type orchestrateStartAdvice struct {
	WatchCommand     string `json:"watch_command,omitempty"`
	StatusCommand    string `json:"status_command,omitempty"`
	WatchInstruction string `json:"watch_instruction,omitempty"`
}

type orchestrateStartLaunchError struct {
	IssueID string
	Field   string
}

func (e *orchestrateStartLaunchError) Error() string {
	issueID := strings.TrimSpace(e.IssueID)
	if issueID == "" {
		issueID = "<unknown>"
	}
	return fmt.Sprintf("invalid orchestration launch for %s: missing %s", issueID, e.Field)
}

type orchestrateWatchFrame struct {
	sourceRevision         uint64
	RootIssueID            string                               `json:"root_issue_id"`
	SinceSeq               int64                                `json:"since_seq"`
	NextSince              int64                                `json:"next_since"`
	Capacity               orchestrateCapacitySummary           `json:"capacity"`
	Runnable               []string                             `json:"runnable"`
	NestedRoots            []orchestrateNestedRoot              `json:"nested_roots,omitempty"`
	Pending                []orchestratePendingStart            `json:"pending,omitempty"`
	PublicationQueue       []domain.PublicationOperation        `json:"publication_queue,omitempty"`
	Active                 []string                             `json:"active,omitempty"`
	ActiveSessions         []orchestrateActiveSession           `json:"active_sessions,omitempty"`
	SessionStartProgress   []orchestrateSessionStartProgress    `json:"session_start_progress,omitempty"`
	StaleCloseableChildren []orchestrateStaleCloseableCandidate `json:"stale_closeable_children,omitempty"`
	ContainmentRisks       []orchestrateContainmentRisk         `json:"containment_risks,omitempty"`
	Blocked                map[string]string                    `json:"blocked"`
	Events                 []mailEvent                          `json:"events"`
	PersistenceGuard       string                               `json:"persistence_guard"`
}

type orchestrateCompactFrame struct {
	RootIssueID string                      `json:"root_issue_id"`
	SinceSeq    int64                       `json:"since_seq"`
	NextSince   int64                       `json:"next_since"`
	Capacity    orchestrateCompactCapacity  `json:"capacity"`
	Readiness   orchestrateCompactReadiness `json:"readiness"`
	Events      []orchestrateCompactEvent   `json:"events,omitempty"`
	EventCount  int                         `json:"event_count"`
	Warnings    []string                    `json:"warnings,omitempty"`
	Advice      map[string]interface{}      `json:"advice,omitempty"`
}

type orchestrateCompactCapacity struct {
	Runnable int            `json:"runnable"`
	Active   int            `json:"active"`
	Blocked  int            `json:"blocked"`
	Pending  int            `json:"pending"`
	Activity map[string]int `json:"activity,omitempty"`
}

type orchestrateCompactReadiness struct {
	Runnable               []string                             `json:"runnable,omitempty"`
	Active                 []orchestrateCompactActiveSession    `json:"active,omitempty"`
	Blocked                map[string]string                    `json:"blocked,omitempty"`
	Pending                []orchestratePendingStart            `json:"pending,omitempty"`
	PublicationQueue       []domain.PublicationOperation        `json:"publication_queue,omitempty"`
	NestedRoots            []orchestrateCompactNestedRoot       `json:"nested_roots,omitempty"`
	SessionStartProgress   []orchestrateCompactSessionProgress  `json:"session_start_progress,omitempty"`
	StaleCloseableChildren []orchestrateStaleCloseableCandidate `json:"stale_closeable_children,omitempty"`
}

type orchestrateCompactActiveSession struct {
	IssueID        string `json:"issue_id"`
	Status         string `json:"status,omitempty"`
	Activity       string `json:"activity"`
	ActivitySource string `json:"activity_source,omitempty"`
}

type orchestrateCompactNestedRoot struct {
	IssueID  string `json:"issue_id"`
	Status   string `json:"status"`
	Type     string `json:"type"`
	Children int    `json:"children"`
	Activity string `json:"activity,omitempty"`
}

type orchestrateCompactSessionProgress struct {
	IssueID        string `json:"issue_id"`
	OperationState string `json:"operation_state"`
	Phase          string `json:"phase,omitempty"`
	Percent        int    `json:"percent,omitempty"`
	Message        string `json:"message,omitempty"`
}

type orchestrateCompactEvent struct {
	Seq            int64                             `json:"seq"`
	IssueID        string                            `json:"issue_id,omitempty"`
	Type           string                            `json:"type"`
	From           string                            `json:"from,omitempty"`
	To             string                            `json:"to,omitempty"`
	CreatedAt      time.Time                         `json:"created_at,omitempty"`
	WorkerEvidence *orchestrateWorkerEvidenceSummary `json:"worker_evidence,omitempty"`
}

type orchestrateWorkerEvidenceSummary struct {
	ValidationStatus string   `json:"validation_status"`
	Summary          string   `json:"summary,omitempty"`
	ReviewStatus     string   `json:"review_status,omitempty"`
	Risks            []string `json:"risks,omitempty"`
	Problems         []string `json:"problems,omitempty"`
}

type orchestrateNestedRoot struct {
	IssueID          string                    `json:"issue_id"`
	Status           string                    `json:"status"`
	IssueStatus      string                    `json:"issue_status,omitempty"`
	Classification   string                    `json:"classification,omitempty"`
	ExclusionReasons []string                  `json:"exclusion_reasons,omitempty"`
	Type             string                    `json:"type"`
	ChildCount       int                       `json:"child_count"`
	ActiveSession    *orchestrateActiveSession `json:"active_session,omitempty"`
	StartFailure     *orchestrateStartFailure  `json:"start_failure,omitempty"`
	FallbackPolicy   string                    `json:"fallback_policy,omitempty"`
	Advice           string                    `json:"advice,omitempty"`
}

type orchestrateStartFailure struct {
	OperationID    string `json:"operation_id,omitempty"`
	OperationState string `json:"operation_state,omitempty"`
	Message        string `json:"message,omitempty"`
}

type orchestratePendingStart struct {
	IssueID        string `json:"issue_id"`
	OperationID    string `json:"operation_id,omitempty"`
	OperationState string `json:"operation_state,omitempty"`
}

type orchestrateActiveSession struct {
	IssueID           string                           `json:"issue_id"`
	Activity          string                           `json:"activity"`
	ActivitySource    string                           `json:"activity_source"`
	State             string                           `json:"state,omitempty"`
	Status            string                           `json:"status,omitempty"`
	TmuxAttachedCount int                              `json:"tmux_attached_count,omitempty"`
	StartProgress     *orchestrateSessionStartProgress `json:"start_progress,omitempty"`
	Advice            string                           `json:"advice,omitempty"`
}

type orchestrateSessionStartProgress struct {
	IssueID        string     `json:"issue_id"`
	OperationID    string     `json:"operation_id,omitempty"`
	OperationState string     `json:"operation_state"`
	Phase          string     `json:"phase,omitempty"`
	Message        string     `json:"message,omitempty"`
	Percent        int        `json:"percent,omitempty"`
	ElapsedMS      int64      `json:"elapsed_ms,omitempty"`
	EnqueuedAt     time.Time  `json:"enqueued_at,omitempty"`
	StartedAt      *time.Time `json:"started_at,omitempty"`
	FinishedAt     *time.Time `json:"finished_at,omitempty"`
}

type orchestrateStaleCloseableCandidate struct {
	IssueID          string   `json:"issue_id"`
	Status           string   `json:"status"`
	Evidence         []string `json:"evidence"`
	SuggestedCommand string   `json:"suggested_command"`
}

type orchestrateContainmentRisk struct {
	IssueID                string   `json:"issue_id"`
	ActiveBranch           string   `json:"active_branch,omitempty"`
	RootIssueID            string   `json:"root_issue_id"`
	RootBranch             string   `json:"root_branch,omitempty"`
	ClosedChildIssueID     string   `json:"closed_child_issue_id"`
	EvidenceCommit         string   `json:"evidence_commit"`
	EvidenceSubject        string   `json:"evidence_subject,omitempty"`
	RootContainsEvidence   bool     `json:"root_contains_evidence"`
	ActiveContainsEvidence bool     `json:"active_contains_evidence"`
	Classification         string   `json:"classification"`
	Message                string   `json:"message"`
	ChangedFiles           []string `json:"changed_files,omitempty"`
	OverlapFiles           []string `json:"overlap_files,omitempty"`
	SuggestedCommand       string   `json:"suggested_command,omitempty"`
}

type orchestratePromptResult struct {
	RootIssueID  string   `json:"root_issue_id"`
	IssueID      string   `json:"issue_id"`
	ParentIssue  string   `json:"parent_issue"`
	Coordination string   `json:"coordination"`
	Prompt       string   `json:"prompt"`
	Commands     []string `json:"commands"`
}

type orchestrateIntegrateResult struct {
	IssueID       string                         `json:"issue_id"`
	WorktreePath  string                         `json:"worktree_path,omitempty"`
	Branch        string                         `json:"branch,omitempty"`
	MergeReady    bool                           `json:"merge_ready"`
	CloseoutReady bool                           `json:"closeout_ready"`
	ContextRisk   *domain.IssueContextRiskPacket `json:"context_risk,omitempty"`
	Apply         bool                           `json:"apply"`
	Applied       bool                           `json:"applied"`
	Reasons       []string                       `json:"reasons,omitempty"`
	Commands      []string                       `json:"commands"`
	Steps         []orchestrateIntegrateStep     `json:"steps,omitempty"`
	Recovery      []string                       `json:"recovery,omitempty"`
}

type orchestrateIntegrateStep struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Output string `json:"output,omitempty"`
	Error  string `json:"error,omitempty"`
}

type orchestrateCloseSessionResult struct {
	IssueID string `json:"issue_id"`
	Output  string `json:"output,omitempty"`
}

type orchestrateMessageResult struct {
	RootIssueID string             `json:"root_issue_id"`
	IssueID     string             `json:"issue_id"`
	Type        string             `json:"type"`
	Mailbox     protocol.MailEvent `json:"mailbox"`
	Delivered   bool               `json:"delivered"`
	Output      string             `json:"output,omitempty"`
}

func ParseOrchestrateStatusArgs(args []string) (OrchestrateStatusOptions, error) {
	opts := OrchestrateStatusOptions{Limit: 50}
	fs := flag.NewFlagSet("orchestrate status", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	addIssueProjectFlag(fs, &opts.Project)
	fs.StringVar(&opts.RootIssueID, "root", "", "root issue id")
	fs.Int64Var(&opts.SinceSeq, "since", 0, "mailbox sequence lower bound")
	fs.IntVar(&opts.Limit, "limit", 50, "maximum mailbox events to include")
	fs.BoolVar(&opts.JSON, "json", false, "output JSON")
	fs.BoolVar(&opts.Summary, "summary", false, "emit concise readiness and event summaries")
	fs.BoolVar(&opts.Full, "full", false, "emit full readiness and mailbox event payloads")
	if err := fs.Parse(args); err != nil {
		return OrchestrateStatusOptions{}, err
	}
	if fs.NArg() != 0 {
		return OrchestrateStatusOptions{}, fmt.Errorf("unexpected argument: %s", fs.Arg(0))
	}
	if opts.Limit < 1 {
		return OrchestrateStatusOptions{}, fmt.Errorf("limit must be >= 1")
	}
	if opts.Summary && opts.Full {
		return OrchestrateStatusOptions{}, fmt.Errorf("--summary and --full cannot be combined")
	}
	opts.Project = normalizeIssueProject(opts.Project)
	return opts, nil
}

func ParseOrchestrateStartArgs(args []string) (OrchestrateStartOptions, error) {
	opts := OrchestrateStartOptions{Limit: 3}
	fs := flag.NewFlagSet("orchestrate start", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	addIssueProjectFlag(fs, &opts.Project)
	fs.StringVar(&opts.RootIssueID, "root", "", "root issue id")
	fs.IntVar(&opts.Limit, "limit", 3, "maximum runnable issues to start")
	fs.StringVar(&opts.IntentKey, "intent-key", "", "stable key to reuse when retrying the same start request")
	fs.BoolVar(&opts.OverrideBoardHealth, "override-board-health", false, "allow project-wide start despite board health refusal")
	fs.Func("issue", "specific runnable issue id (repeatable)", func(value string) error {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			return fmt.Errorf("issue id cannot be empty")
		}
		opts.IssueIDs = append(opts.IssueIDs, trimmed)
		return nil
	})
	fs.BoolVar(&opts.JSON, "json", false, "output JSON")
	if err := fs.Parse(args); err != nil {
		return OrchestrateStartOptions{}, err
	}
	if fs.NArg() != 0 {
		return OrchestrateStartOptions{}, fmt.Errorf("unexpected argument: %s", fs.Arg(0))
	}
	if opts.Limit < 1 {
		return OrchestrateStartOptions{}, fmt.Errorf("limit must be >= 1")
	}
	opts.Project = normalizeIssueProject(opts.Project)
	opts.IntentKey = strings.TrimSpace(opts.IntentKey)
	opts.IssueIDs = dedupeSortedStrings(opts.IssueIDs)
	return opts, nil
}

func ParseOrchestrateGroupArgs(args []string) (OrchestrateGroupOptions, error) {
	opts := OrchestrateGroupOptions{}
	fs := flag.NewFlagSet("orchestrate group", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	addIssueProjectFlag(fs, &opts.Project)
	fs.StringVar(&opts.RootIssueID, "root", "", "root issue id")
	fs.StringVar(&opts.NestedIssueID, "nested", "", "nested epic/root issue id")
	fs.Func("issue", "direct child issue id to move under the nested root (repeatable)", func(value string) error {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			return fmt.Errorf("issue id cannot be empty")
		}
		opts.IssueIDs = append(opts.IssueIDs, trimmed)
		return nil
	})
	fs.BoolVar(&opts.JSON, "json", false, "output JSON")
	if err := fs.Parse(args); err != nil {
		return OrchestrateGroupOptions{}, err
	}
	if fs.NArg() != 0 {
		return OrchestrateGroupOptions{}, fmt.Errorf("unexpected argument: %s", fs.Arg(0))
	}
	if strings.TrimSpace(opts.RootIssueID) == "" {
		return OrchestrateGroupOptions{}, fmt.Errorf("missing required flag: --root")
	}
	if strings.TrimSpace(opts.NestedIssueID) == "" {
		return OrchestrateGroupOptions{}, fmt.Errorf("missing required flag: --nested")
	}
	if len(opts.IssueIDs) == 0 {
		return OrchestrateGroupOptions{}, fmt.Errorf("at least one --issue is required")
	}
	opts.Project = normalizeIssueProject(opts.Project)
	opts.IssueIDs = dedupeSortedStrings(opts.IssueIDs)
	return opts, nil
}

func ParseOrchestrateWatchArgs(args []string) (OrchestrateWatchOptions, error) {
	opts := OrchestrateWatchOptions{JSONL: true, Compact: true, PollInterval: 250 * time.Millisecond}
	fs := flag.NewFlagSet("orchestrate watch", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	addIssueProjectFlag(fs, &opts.Project)
	fs.StringVar(&opts.RootIssueID, "root", "", "root issue id")
	fs.Int64Var(&opts.SinceSeq, "since", 0, "mailbox sequence lower bound")
	fs.BoolVar(&opts.JSONL, "jsonl", true, "emit JSON lines")
	fs.BoolVar(&opts.Once, "once", false, "print one frame then exit")
	fs.BoolVar(&opts.Compact, "compact", true, "emit concise readiness and event summaries")
	fs.BoolVar(&opts.Verbose, "verbose", false, "emit full readiness and mailbox event payloads")
	fs.BoolVar(&opts.Full, "full", false, "alias for --verbose")
	if err := fs.Parse(args); err != nil {
		return OrchestrateWatchOptions{}, err
	}
	explicitFlags := make(map[string]bool)
	fs.Visit(func(f *flag.Flag) {
		explicitFlags[f.Name] = true
	})
	if fs.NArg() != 0 {
		return OrchestrateWatchOptions{}, fmt.Errorf("unexpected argument: %s", fs.Arg(0))
	}
	if explicitFlags["compact"] && opts.Compact && (opts.Full || opts.Verbose) {
		return OrchestrateWatchOptions{}, fmt.Errorf("--compact cannot be combined with --verbose or --full")
	}
	if opts.Full || opts.Verbose {
		opts.Compact = false
		opts.Full = true
	}
	opts.Project = normalizeIssueProject(opts.Project)
	return opts, nil
}

func ParseOrchestrateObserveArgs(args []string) (OrchestrateObserveOptions, error) {
	opts := OrchestrateObserveOptions{}
	fs := flag.NewFlagSet("orchestrate observe", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	addIssueProjectFlag(fs, &opts.Project)
	fs.StringVar(&opts.RootIssueID, "root", "", "root issue id")
	fs.BoolVar(&opts.JSON, "json", false, "output JSON")
	if err := fs.Parse(args); err != nil {
		return OrchestrateObserveOptions{}, err
	}
	if fs.NArg() != 0 {
		return OrchestrateObserveOptions{}, fmt.Errorf("unexpected argument: %s", fs.Arg(0))
	}
	if strings.TrimSpace(opts.RootIssueID) == "" {
		return OrchestrateObserveOptions{}, fmt.Errorf("missing required flag: --root")
	}
	opts.Project = normalizeIssueProject(opts.Project)
	return opts, nil
}

func ParseObserveArgs(args []string) (ObserveOptions, error) {
	opts := ObserveOptions{}
	fs := flag.NewFlagSet("observe", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	addIssueProjectFlag(fs, &opts.Project)
	fs.StringVar(&opts.RootIssueID, "root", "", "optional root issue id")
	fs.BoolVar(&opts.JSON, "json", false, "output JSON")
	if err := fs.Parse(args); err != nil {
		return ObserveOptions{}, err
	}
	if fs.NArg() != 0 {
		return ObserveOptions{}, fmt.Errorf("unexpected argument: %s", fs.Arg(0))
	}
	opts.Project = normalizeIssueProject(opts.Project)
	opts.RootIssueID = strings.TrimSpace(opts.RootIssueID)
	return opts, nil
}

func ParseOrchestrateCompleteCheckArgs(args []string) (OrchestrateCompleteCheckOptions, error) {
	opts := OrchestrateCompleteCheckOptions{}
	fs := flag.NewFlagSet("orchestrate complete-check", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	addIssueProjectFlag(fs, &opts.Project)
	fs.StringVar(&opts.RootIssueID, "root", "", "root issue id")
	fs.BoolVar(&opts.JSON, "json", false, "output JSON")
	if err := fs.Parse(args); err != nil {
		return OrchestrateCompleteCheckOptions{}, err
	}
	if fs.NArg() != 0 {
		return OrchestrateCompleteCheckOptions{}, fmt.Errorf("unexpected argument: %s", fs.Arg(0))
	}
	opts.Project = normalizeIssueProject(opts.Project)
	return opts, nil
}

func ParseOrchestrateReviewArgs(action string, args []string) (OrchestrateReviewOptions, error) {
	opts := OrchestrateReviewOptions{Action: strings.ToLower(strings.TrimSpace(action)), Severity: "high"}
	if opts.Action != "accept" && opts.Action != "return" {
		return OrchestrateReviewOptions{}, fmt.Errorf("review action must be accept or return")
	}
	fs := flag.NewFlagSet("orchestrate review "+opts.Action, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	addIssueProjectFlag(fs, &opts.Project)
	fs.StringVar(&opts.RootIssueID, "root", "", "root issue id")
	fs.StringVar(&opts.IntentKey, "intent-key", "", "stable key to reuse when retrying the same review decision")
	fs.Func("issue", "review issue id (repeatable for accept)", func(value string) error {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			return fmt.Errorf("issue id cannot be empty")
		}
		opts.IssueIDs = append(opts.IssueIDs, trimmed)
		return nil
	})
	fs.StringVar(&opts.Severity, "severity", "high", "severity applied to returned findings")
	fs.Func("finding", "actionable finding text (repeatable for return)", func(value string) error {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			return fmt.Errorf("finding cannot be empty")
		}
		opts.Findings = append(opts.Findings, trimmed)
		return nil
	})
	fs.BoolVar(&opts.RestartWorker, "restart-worker", false, "restart an inactive owned worker after returning findings")
	fs.BoolVar(&opts.JSON, "json", false, "output JSON")
	if err := fs.Parse(args); err != nil {
		return OrchestrateReviewOptions{}, err
	}
	if fs.NArg() != 0 {
		return OrchestrateReviewOptions{}, fmt.Errorf("unexpected argument: %s", fs.Arg(0))
	}
	opts.Project = normalizeIssueProject(opts.Project)
	opts.RootIssueID = strings.TrimSpace(opts.RootIssueID)
	opts.IntentKey = strings.TrimSpace(opts.IntentKey)
	opts.IssueIDs = dedupeSortedStrings(opts.IssueIDs)
	opts.Severity = strings.ToLower(strings.TrimSpace(opts.Severity))
	if len(opts.IssueIDs) == 0 {
		return OrchestrateReviewOptions{}, fmt.Errorf("at least one --issue is required")
	}
	if opts.Action == "accept" {
		if len(opts.Findings) > 0 || opts.RestartWorker {
			return OrchestrateReviewOptions{}, fmt.Errorf("review accept cannot include --finding or --restart-worker")
		}
		return opts, nil
	}
	if len(opts.IssueIDs) != 1 {
		return OrchestrateReviewOptions{}, fmt.Errorf("review return requires exactly one --issue")
	}
	if len(opts.Findings) == 0 {
		return OrchestrateReviewOptions{}, fmt.Errorf("review return requires at least one --finding")
	}
	if opts.Severity == "" {
		return OrchestrateReviewOptions{}, fmt.Errorf("severity cannot be empty")
	}
	return opts, nil
}

func ParseOrchestratePromptArgs(args []string) (OrchestratePromptOptions, error) {
	opts := OrchestratePromptOptions{Coordination: "native"}
	fs := flag.NewFlagSet("orchestrate prompt", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	addIssueProjectFlag(fs, &opts.Project)
	fs.StringVar(&opts.RootIssueID, "root", "", "root issue id")
	fs.StringVar(&opts.IssueID, "issue", "", "worker issue id")
	fs.StringVar(&opts.Coordination, "coordination", "native", "coordination mode: native or mailbox")
	fs.BoolVar(&opts.JSON, "json", false, "output JSON")
	if err := fs.Parse(args); err != nil {
		return OrchestratePromptOptions{}, err
	}
	if fs.NArg() != 0 {
		return OrchestratePromptOptions{}, fmt.Errorf("unexpected argument: %s", fs.Arg(0))
	}
	if strings.TrimSpace(opts.IssueID) == "" {
		return OrchestratePromptOptions{}, fmt.Errorf("missing required flag: --issue")
	}
	opts.Coordination = strings.ToLower(strings.TrimSpace(opts.Coordination))
	if opts.Coordination == "" {
		opts.Coordination = "native"
	}
	switch opts.Coordination {
	case "native", "mailbox":
	default:
		return OrchestratePromptOptions{}, fmt.Errorf("coordination must be native or mailbox")
	}
	opts.Project = normalizeIssueProject(opts.Project)
	return opts, nil
}

func ParseOrchestrateIntegrateArgs(args []string) (OrchestrateIntegrateOptions, error) {
	opts := OrchestrateIntegrateOptions{}
	fs := flag.NewFlagSet("orchestrate integrate", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	addIssueProjectFlag(fs, &opts.Project)
	fs.StringVar(&opts.IssueID, "issue", "", "worker issue id to integrate")
	fs.BoolVar(&opts.Apply, "apply", false, "apply integration instead of printing advisory guidance")
	fs.BoolVar(&opts.JSON, "json", false, "output JSON")
	if err := fs.Parse(args); err != nil {
		return OrchestrateIntegrateOptions{}, err
	}
	if fs.NArg() != 0 {
		return OrchestrateIntegrateOptions{}, fmt.Errorf("unexpected argument: %s", fs.Arg(0))
	}
	if strings.TrimSpace(opts.IssueID) == "" {
		return OrchestrateIntegrateOptions{}, fmt.Errorf("missing required flag: --issue")
	}
	opts.Project = normalizeIssueProject(opts.Project)
	return opts, nil
}

func ParseOrchestrateCloseSessionArgs(args []string) (OrchestrateCloseSessionOptions, error) {
	opts := OrchestrateCloseSessionOptions{}
	fs := flag.NewFlagSet("orchestrate close-session", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	addIssueProjectFlag(fs, &opts.Project)
	fs.StringVar(&opts.IssueID, "issue", "", "worker issue id whose active session should be stopped")
	fs.BoolVar(&opts.JSON, "json", false, "output JSON")
	if err := fs.Parse(args); err != nil {
		return OrchestrateCloseSessionOptions{}, err
	}
	if fs.NArg() != 0 {
		return OrchestrateCloseSessionOptions{}, fmt.Errorf("unexpected argument: %s", fs.Arg(0))
	}
	if strings.TrimSpace(opts.IssueID) == "" {
		return OrchestrateCloseSessionOptions{}, fmt.Errorf("missing required flag: --issue")
	}
	opts.Project = normalizeIssueProject(opts.Project)
	return opts, nil
}

func ParseOrchestrateMessageArgs(args []string) (OrchestrateMessageOptions, error) {
	opts := OrchestrateMessageOptions{Type: "orchestrator-message"}
	fs := flag.NewFlagSet("orchestrate message", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	addIssueProjectFlag(fs, &opts.Project)
	fs.StringVar(&opts.RootIssueID, "root", "", "root/parent issue id for mailbox persistence")
	fs.StringVar(&opts.IssueID, "issue", "", "worker issue id to message")
	fs.StringVar(&opts.Type, "type", "orchestrator-message", "mailbox event type")
	fs.StringVar(&opts.Body, "body", "", "message body")
	fs.BoolVar(&opts.ForceSelfDelivery, "force-self-delivery", false, "allow delivery when --issue matches AZEDARACH_ISSUE_ID")
	fs.BoolVar(&opts.JSON, "json", false, "output JSON")
	if err := fs.Parse(args); err != nil {
		return OrchestrateMessageOptions{}, err
	}
	if fs.NArg() != 0 {
		return OrchestrateMessageOptions{}, fmt.Errorf("unexpected argument: %s", fs.Arg(0))
	}
	if strings.TrimSpace(opts.RootIssueID) == "" {
		return OrchestrateMessageOptions{}, fmt.Errorf("missing required flag: --root")
	}
	if strings.TrimSpace(opts.IssueID) == "" {
		return OrchestrateMessageOptions{}, fmt.Errorf("missing required flag: --issue")
	}
	if strings.TrimSpace(opts.Type) == "" {
		return OrchestrateMessageOptions{}, fmt.Errorf("missing required flag: --type")
	}
	if strings.TrimSpace(opts.Body) == "" {
		return OrchestrateMessageOptions{}, fmt.Errorf("missing required flag: --body")
	}
	opts.Project = normalizeIssueProject(opts.Project)
	opts.Type = strings.TrimSpace(opts.Type)
	opts.Body = strings.TrimSpace(opts.Body)
	return opts, nil
}

func OrchestrateStatusCommand(deps *Dependencies, opts OrchestrateStatusOptions) error {
	restoreProject, err := applyExplicitProjectOverride(deps, opts.Project)
	if err != nil {
		return err
	}
	defer restoreProject()
	scope, err := resolveCLIOrchestrationScope(opts.RootIssueID)
	if err != nil {
		return err
	}
	if scope.Kind == domain.OrchestrationScopeProject {
		return projectOrchestrateStatusCommand(deps, opts, scope)
	}
	opts.RootIssueID = scope.RootIssueID.String()

	ctx, cancel := context.WithTimeout(context.Background(), daemonCommandTimeout)
	defer cancel()
	if err := ensureDaemon(ctx, deps, "cli"); err != nil {
		return err
	}

	ready, err := deps.DaemonClient.TaskGraphReadinessForActor(ctx, opts.RootIssueID, orchestrateOwnerID())
	if err != nil {
		return err
	}

	events, err := deps.DaemonClient.MailList(ctx, protocol.MailListCommandBody{
		RepoDir:     deps.RepoDir,
		ParentIssue: opts.RootIssueID,
		SinceSeq:    opts.SinceSeq,
		Limit:       opts.Limit,
	})
	if err != nil {
		return err
	}
	if next := nextMailboxSeq(events, opts.SinceSeq); next > opts.SinceSeq {
		checkpointRootOrchestratorCursor(ctx, deps, opts.RootIssueID, next)
	}

	result := orchestrateStatusResult{
		RootIssueID:            ready.RootIssueID,
		Capacity:               orchestrateCapacityFromDaemon(ready.Capacity),
		Runnable:               ready.Runnable,
		NestedRoots:            orchestrateNestedRootsFromDaemon(ready.NestedRoots),
		Pending:                orchestratePendingStartsFromDaemon(ready.Pending),
		PublicationQueue:       append([]domain.PublicationOperation(nil), ready.PublicationQueue...),
		Active:                 ready.Active,
		ActiveSessions:         orchestrateActiveSessionsFromDaemon(ready.ActiveSessions),
		SessionStartProgress:   orchestrateSessionStartProgressFromDaemon(ready.SessionStartProgress),
		StaleCloseableChildren: orchestrateStaleCloseableFromDaemon(ready.StaleCloseableChildren),
		ContainmentRisks:       orchestrateContainmentRisksFromDaemon(ready.ContainmentRisks),
		WorkerObservations:     orchestrateObservationsFromDaemon(ready.WorkerObservations, orchestrateObserveNow()),
		Blocked:                ready.Blocked,
		MailboxEvents:          events,
		Warnings:               orchestrateStatusWarnings(ctx, deps, ready, len(ready.Runnable)),
		Advice: map[string]interface{}{
			"watch":             fmt.Sprintf("az orchestrate watch --root %s --since %d --jsonl", ready.RootIssueID, nextMailboxSeq(events, opts.SinceSeq)),
			"watch_instruction": "Start this watch command in another pane/session and leave it running while workers are active; use active_sessions activity before considering pane capture. Do not add --once for orchestration monitoring.",
			"persistence_guard": "Daemon-enforced: parent idle/turn completion is wake-required while direct nested roots remain and complete-check has not passed; the durable cursor is resumed after restart or session replacement.",
		},
	}
	if opts.Summary {
		compact := compactFrameFromStatusResult(result, opts.SinceSeq, nextMailboxSeq(events, opts.SinceSeq))
		if opts.JSON {
			return printJSON(compact)
		}
		printCompactOrchestrateFrame(compact)
		return nil
	}
	if opts.JSON {
		return printJSON(result)
	}

	fmt.Printf("Root issue: %s\n", result.RootIssueID)
	fmt.Println("Capacity:")
	fmt.Printf("- direct runnable=%d active=%d pending_starts=%d\n", result.Capacity.DirectRunnableCount, result.Capacity.DirectActiveCount, result.Capacity.PendingStartsCount)
	fmt.Printf("- nested startable=%d active=%d blocked_start_failed=%d not_counting=%d total_counting=%d\n", result.Capacity.NestedStartableCount, result.Capacity.NestedActiveCount, result.Capacity.BlockedNestedRootsCount, result.Capacity.NotCountingCapacityCount, result.Capacity.TotalCountingCapacityCount)
	fmt.Println("Runnable leaves:")
	if len(result.Runnable) == 0 {
		fmt.Println("- (none)")
	} else {
		for _, id := range result.Runnable {
			fmt.Printf("- %s\n", id)
		}
	}
	if len(result.Pending) > 0 {
		fmt.Println("Pending starts:")
		for _, pending := range result.Pending {
			fmt.Printf("- %s operation=%s state=%s\n", pending.IssueID, pending.OperationID, pending.OperationState)
		}
	}
	if len(result.PublicationQueue) > 0 {
		fmt.Println("Publication queue:")
		for _, publication := range result.PublicationQueue {
			fmt.Printf("- %s issue=%s intent=%s state=%s position=%d source=%s base=%s candidate=%s lease=%s evidence=%s validation=%s reused=%s",
				publication.OperationID, publication.IssueID, publication.IntentKey, publication.State, publication.QueuePosition,
				publication.SourceRevision, publication.BaseRevision, publication.CandidateRevision, publication.LeaseOwner,
				publication.EvidenceSource, publication.ValidationRequestID, publication.ReusedEvidenceID)
			if publication.FailureKind != "" {
				fmt.Printf(" failure=%s", publication.FailureKind)
			}
			if publication.FailureArtifact != "" {
				fmt.Printf(" artifact=%s", publication.FailureArtifact)
			}
			fmt.Println()
		}
	}
	if len(result.NestedRoots) > 0 {
		fmt.Println("Nested roots:")
		for _, nested := range result.NestedRoots {
			fmt.Printf("- %s status=%s issue_status=%s type=%s children=%d", nested.IssueID, nested.Status, nested.IssueStatus, nested.Type, nested.ChildCount)
			if nested.Classification != "" {
				fmt.Printf(" classification=%s", nested.Classification)
			}
			if len(nested.ExclusionReasons) > 0 {
				fmt.Printf(" exclusions=%s", strings.Join(nested.ExclusionReasons, ","))
			}
			if nested.FallbackPolicy != "" {
				fmt.Printf(" fallback=%s", nested.FallbackPolicy)
			}
			fmt.Println()
			if nested.ActiveSession != nil {
				active := nested.ActiveSession
				status := strings.TrimSpace(active.Status)
				if status == "" {
					status = "active"
				}
				fmt.Printf("  session: status=%s activity=%s source=%s\n", status, active.Activity, active.ActivitySource)
			}
			if nested.StartFailure != nil {
				fmt.Printf("  start failure: operation=%s state=%s", nested.StartFailure.OperationID, nested.StartFailure.OperationState)
				if nested.StartFailure.Message != "" {
					fmt.Printf(" message=%s", nested.StartFailure.Message)
				}
				fmt.Println()
			}
			if nested.Advice != "" {
				fmt.Printf("  next: %s\n", nested.Advice)
			}
		}
	}
	if len(result.Blocked) > 0 {
		fmt.Println("Blocked leaves/roots:")
		ids := make([]string, 0, len(result.Blocked))
		for id := range result.Blocked {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		for _, id := range ids {
			reason := result.Blocked[id]
			fmt.Printf("- %s: %s\n", id, reason)
		}
	}
	if len(result.ActiveSessions) > 0 {
		fmt.Println("Active sessions:")
		for _, active := range result.ActiveSessions {
			status := strings.TrimSpace(active.Status)
			if status == "" {
				status = "active"
			}
			fmt.Printf("- %s status=%s activity=%s source=%s\n", active.IssueID, status, active.Activity, active.ActivitySource)
			if active.StartProgress != nil {
				fmt.Printf("  start: %s\n", formatSessionStartProgress(*active.StartProgress))
			}
			if active.Advice != "" {
				fmt.Printf("  %s\n", active.Advice)
			}
		}
	}
	if len(result.SessionStartProgress) > 0 {
		fmt.Println("Session start progress:")
		for _, progress := range result.SessionStartProgress {
			fmt.Printf("- %s: %s\n", progress.IssueID, formatSessionStartProgress(progress))
		}
	}
	if len(result.StaleCloseableChildren) > 0 {
		fmt.Println("Stale-closeable children:")
		for _, candidate := range result.StaleCloseableChildren {
			fmt.Printf("- %s status=%s\n", candidate.IssueID, candidate.Status)
			if len(candidate.Evidence) > 0 {
				fmt.Printf("  evidence: %s\n", strings.Join(candidate.Evidence, "; "))
			}
			if candidate.SuggestedCommand != "" {
				fmt.Printf("  next: %s\n", candidate.SuggestedCommand)
			}
		}
	}
	if len(result.ContainmentRisks) > 0 {
		fmt.Println("Containment risks:")
		for _, risk := range result.ContainmentRisks {
			fmt.Printf("- %s: %s\n", risk.IssueID, risk.Message)
			fmt.Printf("  evidence: child=%s commit=%s root_contains=%t active_contains=%t\n", risk.ClosedChildIssueID, shortCLICommitHash(risk.EvidenceCommit), risk.RootContainsEvidence, risk.ActiveContainsEvidence)
			if len(risk.OverlapFiles) > 0 {
				fmt.Printf("  overlap: %s\n", strings.Join(risk.OverlapFiles, ", "))
			} else if len(risk.ChangedFiles) > 0 {
				fmt.Printf("  changed files: %s\n", strings.Join(risk.ChangedFiles, ", "))
			}
			if risk.SuggestedCommand != "" {
				fmt.Printf("  next: %s\n", risk.SuggestedCommand)
			}
		}
	}
	if len(result.WorkerObservations) > 0 {
		fmt.Println("Worker observations:")
		printOrchestrateObservationGroups(result.WorkerObservations)
	}
	fmt.Printf("Mailbox events (latest %d, since seq>%d): %d\n", opts.Limit, opts.SinceSeq, len(result.MailboxEvents))
	for _, evt := range result.MailboxEvents {
		fmt.Printf("- seq=%d issue=%s type=%s from=%s to=%s\n", evt.Seq, strings.TrimSpace(evt.IssueID.String()), evt.Type, strings.TrimSpace(evt.From), strings.TrimSpace(evt.To))
	}
	if len(result.Warnings) > 0 {
		fmt.Println("Warnings:")
		for _, warning := range result.Warnings {
			fmt.Printf("- %s\n", warning)
		}
	}
	fmt.Println("Next watch command (leave running while workers are active; do not add --once):")
	fmt.Printf("- %s\n", result.Advice["watch"])
	fmt.Printf("- %s\n", result.Advice["watch_instruction"])
	return nil
}

func OrchestrateObserveCommand(deps *Dependencies, opts OrchestrateObserveOptions) error {
	restoreProject, err := applyExplicitProjectOverride(deps, opts.Project)
	if err != nil {
		return err
	}
	defer restoreProject()

	result, err := buildOrchestrateObserveForRoot(deps, opts.RootIssueID)
	if err != nil {
		return err
	}
	if opts.JSON {
		return printJSON(result)
	}
	printOrchestrateObserveResult(result)
	return nil
}

func ObserveCommand(deps *Dependencies, opts ObserveOptions) error {
	restoreProject, err := applyExplicitProjectOverride(deps, opts.Project)
	if err != nil {
		return err
	}
	defer restoreProject()

	var (
		result orchestrateObserveResult
	)
	if strings.TrimSpace(opts.RootIssueID) != "" {
		result, err = buildOrchestrateObserveForRoot(deps, opts.RootIssueID)
	} else {
		result, err = buildObserveForActiveWork(deps)
	}
	if err != nil {
		return err
	}
	if opts.JSON {
		return printJSON(result)
	}
	printOrchestrateObserveResult(result)
	return nil
}

func OrchestrateStartCommand(deps *Dependencies, opts OrchestrateStartOptions) error {
	restoreProject, err := applyExplicitProjectOverride(deps, opts.Project)
	if err != nil {
		return err
	}
	defer restoreProject()
	scope, err := resolveCLIOrchestrationScope(opts.RootIssueID)
	if err != nil {
		return err
	}
	if scope.Kind == domain.OrchestrationScopeProject {
		return projectOrchestrateStartCommand(deps, opts, scope)
	}
	opts.RootIssueID = scope.RootIssueID.String()

	result, err := orchestrateStart(deps, opts)
	if err != nil {
		return err
	}
	if opts.JSON {
		if err := printJSON(result); err != nil {
			return err
		}
		return orchestrateStartResultError(result)
	}
	printOrchestrateStartResult(result)
	return orchestrateStartResultError(result)
}

func OrchestrateGroupCommand(deps *Dependencies, opts OrchestrateGroupOptions) error {
	restoreProject, err := applyExplicitProjectOverride(deps, opts.Project)
	if err != nil {
		return err
	}
	defer restoreProject()

	result, err := orchestrateGroup(deps, opts)
	if err != nil {
		return err
	}
	if opts.JSON {
		return printJSON(result)
	}
	fmt.Printf("Grouped %d issue(s) under nested root %s for root %s\n", len(result.Grouped), result.NestedIssueID, result.RootIssueID)
	for _, item := range result.Grouped {
		changed := "unchanged"
		if item.Changed {
			changed = "moved"
		}
		if item.PreviousParentID != "" {
			fmt.Printf("- %s: %s parent %s -> %s\n", item.IssueID, changed, item.PreviousParentID, item.NewParentID)
		} else {
			fmt.Printf("- %s: %s parent -> %s\n", item.IssueID, changed, item.NewParentID)
		}
	}
	if result.NestedRoot != nil {
		nested := result.NestedRoot
		fmt.Printf("Nested root: %s status=%s issue_status=%s classification=%s exclusions=%s children=%d fallback=%s\n", nested.IssueID, nested.Status, nested.IssueStatus, nested.Classification, strings.Join(nested.ExclusionReasons, ","), nested.ChildCount, nested.FallbackPolicy)
		if nested.Advice != "" {
			fmt.Printf("Next: %s\n", nested.Advice)
		}
	}
	for _, advice := range result.Advice {
		fmt.Printf("Next: %s\n", advice)
	}
	return nil
}

func orchestrateStartResultError(result orchestrateStartResult) error {
	if len(result.Failed) > 0 {
		return fmt.Errorf("orchestrate start completed with failures")
	}
	return nil
}

func orchestrateGroup(deps *Dependencies, opts OrchestrateGroupOptions) (orchestrateGroupResult, error) {
	ctx, cancel := context.WithTimeout(context.Background(), daemonCommandTimeout)
	defer cancel()
	if err := ensureDaemon(ctx, deps, "cli"); err != nil {
		return orchestrateGroupResult{}, err
	}
	rootID, err := naming.ParseIssueID(strings.TrimSpace(opts.RootIssueID))
	if err != nil {
		return orchestrateGroupResult{}, fmt.Errorf("invalid root issue id: %w", err)
	}
	nestedID, err := naming.ParseIssueID(strings.TrimSpace(opts.NestedIssueID))
	if err != nil {
		return orchestrateGroupResult{}, fmt.Errorf("invalid nested issue id: %w", err)
	}
	if rootID == nestedID {
		return orchestrateGroupResult{}, fmt.Errorf("nested issue must differ from root issue")
	}

	before, err := deps.DaemonClient.TaskGraphReadiness(ctx, rootID.String())
	if err != nil {
		return orchestrateGroupResult{}, err
	}
	nestedBefore, nestedKnown := findOrchestrateDaemonNestedRoot(before.NestedRoots, nestedID.String())
	if !nestedKnown {
		return orchestrateGroupResult{}, fmt.Errorf("nested issue %s is not a nested root under %s; create/link it under the root first", nestedID, rootID)
	}

	grouped := make([]orchestrateGroupItem, 0, len(opts.IssueIDs))
	toMove := make([]naming.IssueID, 0, len(opts.IssueIDs))
	for _, rawIssueID := range opts.IssueIDs {
		issueID, err := naming.ParseIssueID(strings.TrimSpace(rawIssueID))
		if err != nil {
			return orchestrateGroupResult{}, fmt.Errorf("invalid issue id %q: %w", rawIssueID, err)
		}
		if issueID == rootID || issueID == nestedID {
			return orchestrateGroupResult{}, fmt.Errorf("cannot group root or nested root issue %s under %s", issueID, nestedID)
		}
		snapshot, err := deps.DaemonClient.GetTaskSnapshot(ctx, issueID.String())
		if err != nil {
			return orchestrateGroupResult{}, fmt.Errorf("inspect issue %s before grouping: %w", issueID, err)
		}
		if len(snapshot.Tasks) == 0 {
			return orchestrateGroupResult{}, fmt.Errorf("issue not found before grouping: %s", issueID)
		}
		task, ok := findTaskByID(snapshot.Tasks, issueID.String())
		if !ok {
			return orchestrateGroupResult{}, fmt.Errorf("issue not found before grouping: %s", issueID)
		}
		previousParent := ""
		if task.ParentID != nil {
			previousParent = strings.TrimSpace(task.ParentID.String())
		}
		if previousParent == "" {
			return orchestrateGroupResult{}, fmt.Errorf("issue %s is not parented under root %s; attach it to the root before nested grouping", issueID, rootID)
		}
		if !naming.IssueIDsEqual(previousParent, rootID.String()) && !naming.IssueIDsEqual(previousParent, nestedID.String()) {
			return orchestrateGroupResult{}, fmt.Errorf("issue %s is parented under %s, not root %s; refusing nested grouping from another parent", issueID, previousParent, rootID)
		}
		changed := !naming.IssueIDsEqual(previousParent, nestedID.String())
		if changed && (task.HasTmuxSession || task.HasWorktree) {
			return orchestrateGroupResult{}, fmt.Errorf("issue %s has runtime/worktree state; stop or supersede it before grouping under nested root %s", issueID, nestedID)
		}
		if changed {
			toMove = append(toMove, issueID)
		}
		grouped = append(grouped, orchestrateGroupItem{
			IssueID:          issueID.String(),
			PreviousParentID: previousParent,
			NewParentID:      nestedID.String(),
			Changed:          changed,
		})
	}
	for _, issueID := range toMove {
		if err := deps.DaemonClient.AddTaskDependency(ctx, daemonclient.TaskDependencyParams{
			TaskID:            issueID,
			DependsOnID:       nestedID,
			Type:              string(domain.DependencyParentChild),
			ForceParentChange: true,
		}); err != nil {
			return orchestrateGroupResult{}, fmt.Errorf("group issue %s under nested root %s: %w", issueID, nestedID, err)
		}
	}

	after, err := deps.DaemonClient.TaskGraphReadiness(ctx, rootID.String())
	if err != nil {
		return orchestrateGroupResult{}, err
	}
	nestedAfter, _ := findOrchestrateDaemonNestedRoot(after.NestedRoots, nestedID.String())
	nested := orchestrateNestedRootsFromDaemon([]daemonclient.TaskNestedRoot{nestedAfter})
	result := orchestrateGroupResult{
		RootIssueID:   rootID.String(),
		NestedIssueID: nestedID.String(),
		Grouped:       grouped,
		Advice: []string{
			fmt.Sprintf("inspect updated root: az orchestrate status --root %s --json", rootID.String()),
			fmt.Sprintf("start nested root orchestrator when ready: az orchestrator-session start --root %s", nestedID.String()),
		},
	}
	if len(nested) > 0 && nestedAfter.IssueID != "" {
		result.NestedRoot = &nested[0]
	}
	if result.NestedRoot == nil {
		converted := orchestrateNestedRootsFromDaemon([]daemonclient.TaskNestedRoot{nestedBefore})
		if len(converted) > 0 {
			result.NestedRoot = &converted[0]
		}
	}
	return result, nil
}

func findOrchestrateDaemonNestedRoot(nested []daemonclient.TaskNestedRoot, issueID string) (daemonclient.TaskNestedRoot, bool) {
	for _, item := range nested {
		if naming.IssueIDsEqual(item.IssueID, issueID) {
			return item, true
		}
	}
	return daemonclient.TaskNestedRoot{}, false
}

func orchestrateStart(deps *Dependencies, opts OrchestrateStartOptions) (orchestrateStartResult, error) {
	ctx, cancel := context.WithTimeout(context.Background(), daemonCommandTimeout)
	defer cancel()
	if err := ensureDaemon(ctx, deps, "cli"); err != nil {
		return orchestrateStartResult{}, err
	}
	ownerID := orchestrateOwnerID()
	scope, err := domain.RootedOrchestrationScope(opts.RootIssueID)
	if err != nil {
		return orchestrateStartResult{}, err
	}
	intentKey := strings.TrimSpace(opts.IntentKey)
	if intentKey == "" {
		intentKey, err = newCLIOrchestrationStartIntentKey()
		if err != nil {
			return orchestrateStartResult{}, err
		}
	}
	applied, err := deps.DaemonClient.ApplyOrchestrationIntent(ctx, protocol.OrchestrationIntentRequest{Scope: scope, Kind: protocol.OrchestrationIntentStart, IntentKey: intentKey, ActorID: ownerID, IssueIDs: opts.IssueIDs, Limit: opts.Limit, RepoDir: deps.RepoDir, BaseBranch: opts.BaseBranchOverride, OverrideBoardHealth: opts.OverrideBoardHealth})
	if err != nil {
		return orchestrateStartResult{}, err
	}
	ready, readinessErr := deps.DaemonClient.TaskGraphReadinessForActor(ctx, opts.RootIssueID, ownerID)
	if readinessErr != nil && len(applied.Pending) == 0 {
		return orchestrateStartResult{}, readinessErr
	}
	nestedRootIDs := make([]string, 0, len(ready.NestedRoots))
	for _, nested := range ready.NestedRoots {
		nestedRootIDs = append(nestedRootIDs, nested.IssueID)
	}

	result := orchestrateStartResult{
		RootIssueID: opts.RootIssueID,
		IntentKey:   intentKey,
		Limit:       opts.Limit,
		Requested:   append([]string(nil), applied.Requested...),
		NestedRoots: nestedRootIDs,
		Started:     make([]string, 0, len(applied.Launched)),
		Launched:    make([]orchestrateStartLaunch, 0, len(applied.Launched)),
		Skipped:     cloneOrchestrateStartDetails(applied.Skipped),
		Failed:      cloneOrchestrateStartDetails(applied.Failed),
		Advice: orchestrateStartAdvice{
			WatchCommand:     fmt.Sprintf("az orchestrate watch --root %s --since 0 --jsonl", opts.RootIssueID),
			StatusCommand:    fmt.Sprintf("az orchestrate status --root %s --json", opts.RootIssueID),
			WatchInstruction: "Start this watch command in another pane/session and leave it running while workers are active; use active_sessions activity before considering pane capture. Do not add --once for orchestration monitoring.",
		},
	}
	if readinessErr == nil {
		result.Warnings = orchestrateStartWarnings(ctx, deps, ready, len(applied.Launched))
	} else {
		result.Warnings = append(result.Warnings, "durable start intent is queued; readiness projection remains unavailable: "+readinessErr.Error())
	}
	for _, pending := range applied.Pending {
		reason := strings.TrimSpace(string(pending.Phase))
		if pending.Message != "" {
			if reason != "" {
				reason += ": "
			}
			reason += pending.Message
		}
		retry := orchestrateStartRetryCommand(opts, intentKey)
		result.Pending = append(result.Pending, orchestrateStartPending{IssueID: pending.IssueID, OperationID: pending.OperationID, OperationState: pending.OperationState, Reason: reason, FollowUpCommands: []string{retry}})
	}

	for _, daemonLaunch := range applied.Launched {
		launch := orchestrateStartLaunch{IssueID: daemonLaunch.IssueID, SessionID: daemonLaunch.SessionID, OperationID: daemonLaunch.OperationID, OperationState: daemonLaunch.OperationState}
		emitOrchestrateStartProgressWithLaunch(opts, "submitted", launch)
		issueID := launch.IssueID
		if strings.TrimSpace(issueID) == "" {
			return orchestrateStartResult{}, &orchestrateStartLaunchError{Field: "issue_id"}
		}
		launch, pending, err := waitForOrchestrateStartLaunch(deps, opts, launch)
		if err != nil {
			result.Failed[issueID] = err.Error()
			continue
		}
		if pending != nil {
			result.Pending = append(result.Pending, *pending)
			emitOrchestrateStartProgressWithLaunch(opts, "pending", launch)
			continue
		}
		launch.WatchCommand = result.Advice.WatchCommand
		launch.IntegrateHint = issueCloseCommand(issueID)
		launch.CloseHint = fmt.Sprintf("az orchestrate close-session --issue %s", issueID)
		result.Started = append(result.Started, issueID)
		result.Launched = append(result.Launched, launch)
		emitOrchestrateStartProgressWithLaunch(opts, "launched", launch)
	}

	return result, nil
}

func orchestrateStartRetryCommand(opts OrchestrateStartOptions, intentKey string) string {
	parts := []string{"az", "orchestrate", "start"}
	if opts.Project != "" {
		parts = append(parts, "--project", shellSingleQuote(opts.Project))
	}
	if opts.RootIssueID != "" {
		parts = append(parts, "--root", shellSingleQuote(opts.RootIssueID))
	}
	parts = append(parts, "--limit", fmt.Sprintf("%d", opts.Limit), "--intent-key", shellSingleQuote(intentKey))
	for _, issueID := range opts.IssueIDs {
		parts = append(parts, "--issue", shellSingleQuote(issueID))
	}
	if opts.OverrideBoardHealth {
		parts = append(parts, "--override-board-health")
	}
	return strings.Join(parts, " ")
}

func cloneOrchestrateStartDetails(in map[string]string) map[string]string {
	out := make(map[string]string, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

func orchestrateOwnerID() string {
	if ownerID := defaultIssueOwnerID(); ownerID != "" {
		return ownerID
	}
	return "orchestrate"
}

func waitForOrchestrateStartLaunch(deps *Dependencies, opts OrchestrateStartOptions, launch orchestrateStartLaunch) (orchestrateStartLaunch, *orchestrateStartPending, error) {
	operationID := strings.TrimSpace(launch.OperationID)
	if operationID == "" {
		return launch, nil, &orchestrateStartLaunchError{IssueID: launch.IssueID, Field: "operation_id"}
	}

	timeout := orchestrateStartWaitTimeout
	if timeout <= 0 {
		timeout = sessionStartCommandTimeout
	}
	waitCtx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	record, err := deps.DaemonClient.WaitForOperation(waitCtx, operationID, orchestrateStartWaitPollInterval)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
			if record.OperationID == "" {
				record.OperationID = naming.OperationID(operationID)
				record.State = operationStateFromString(launch.OperationState)
			}
			state := string(record.State)
			if state == "" {
				state = strings.TrimSpace(launch.OperationState)
			}
			if state == "" {
				state = "unknown"
			}
			launch.OperationState = state
			pending := orchestrateStartPending{
				IssueID:        launch.IssueID,
				OperationID:    operationID,
				OperationState: state,
				Reason:         fmt.Sprintf("timed out after %s waiting for daemon operation to finish; operation is still %s", timeout, state),
				FollowUpCommands: []string{
					fmt.Sprintf("az operation get --id %s --wait", operationID),
					fmt.Sprintf("az orchestrate status --root %s --json", opts.RootIssueID),
				},
			}
			return launch, &pending, nil
		}
		return launch, nil, fmt.Errorf("wait for operation %s: %w", operationID, err)
	}

	launch.OperationID = record.OperationID.String()
	launch.OperationState = string(record.State)
	if err := operationRecordError(record); err != nil {
		return launch, nil, err
	}
	return launch, nil, nil
}

func emitOrchestrateStartProgress(opts OrchestrateStartOptions, stage, issueID string) {
	if !opts.JSON {
		return
	}
	fmt.Fprintf(os.Stderr, "orchestrate start: %s %s\n", stage, issueID)
}

func emitOrchestrateStartProgressWithLaunch(opts OrchestrateStartOptions, stage string, launch orchestrateStartLaunch) {
	if !opts.JSON {
		return
	}
	details := strings.TrimSpace(launch.IssueID)
	if launch.OperationID != "" {
		details += " operation=" + launch.OperationID
	}
	if launch.OperationState != "" {
		details += " state=" + launch.OperationState
	}
	fmt.Fprintf(os.Stderr, "orchestrate start: %s %s\n", stage, details)
}

func orchestratePendingStartsFromDaemon(pending []daemonclient.TaskPendingStart) []orchestratePendingStart {
	if len(pending) == 0 {
		return nil
	}
	out := make([]orchestratePendingStart, 0, len(pending))
	for _, start := range pending {
		out = append(out, orchestratePendingStart{
			IssueID:        start.IssueID,
			OperationID:    start.OperationID,
			OperationState: start.OperationState,
		})
	}
	return out
}

func orchestrateCapacityFromDaemon(capacity daemonclient.TaskCapacitySummary) orchestrateCapacitySummary {
	return orchestrateCapacitySummary{
		DirectRunnableCount:        capacity.DirectRunnableCount,
		DirectActiveCount:          capacity.DirectActiveCount,
		NestedStartableCount:       capacity.NestedStartableCount,
		NestedActiveCount:          capacity.NestedActiveCount,
		PendingStartsCount:         capacity.PendingStartsCount,
		BlockedNestedRootsCount:    capacity.BlockedNestedRootsCount,
		NotCountingCapacityCount:   capacity.NotCountingCapacityCount,
		TotalCountingCapacityCount: capacity.TotalCountingCapacityCount,
	}
}

func orchestrateNestedRootsFromDaemon(nested []daemonclient.TaskNestedRoot) []orchestrateNestedRoot {
	if len(nested) == 0 {
		return nil
	}
	out := make([]orchestrateNestedRoot, 0, len(nested))
	for _, item := range nested {
		var active *orchestrateActiveSession
		if item.ActiveSession != nil {
			converted := orchestrateActiveSessionsFromDaemon([]daemonclient.TaskActiveSession{*item.ActiveSession})
			if len(converted) > 0 {
				active = &converted[0]
			}
		}
		var failure *orchestrateStartFailure
		if item.StartFailure != nil {
			failure = &orchestrateStartFailure{
				OperationID:    item.StartFailure.OperationID,
				OperationState: item.StartFailure.OperationState,
				Message:        item.StartFailure.Message,
			}
		}
		out = append(out, orchestrateNestedRoot{
			IssueID:          item.IssueID,
			Status:           item.Status,
			IssueStatus:      item.IssueStatus,
			Classification:   item.Classification,
			ExclusionReasons: append([]string(nil), item.ExclusionReasons...),
			Type:             item.Type,
			ChildCount:       item.ChildCount,
			ActiveSession:    active,
			StartFailure:     failure,
			FallbackPolicy:   item.FallbackPolicy,
			Advice:           item.Advice,
		})
	}
	return out
}

func orchestrateActiveSessionsFromDaemon(active []daemonclient.TaskActiveSession) []orchestrateActiveSession {
	if len(active) == 0 {
		return nil
	}
	out := make([]orchestrateActiveSession, 0, len(active))
	for _, session := range active {
		var startProgress *orchestrateSessionStartProgress
		if session.StartProgress != nil {
			converted := orchestrateSessionStartProgressFromDaemonOne(*session.StartProgress)
			startProgress = &converted
		}
		out = append(out, orchestrateActiveSession{
			IssueID:           session.IssueID,
			Activity:          session.Activity,
			ActivitySource:    session.ActivitySource,
			State:             session.State,
			Status:            session.Status,
			TmuxAttachedCount: session.TmuxAttachedCount,
			StartProgress:     startProgress,
			Advice:            session.Advice,
		})
	}
	return out
}

func orchestrateSessionStartProgressFromDaemon(progress []daemonclient.TaskSessionStartProgress) []orchestrateSessionStartProgress {
	if len(progress) == 0 {
		return nil
	}
	out := make([]orchestrateSessionStartProgress, 0, len(progress))
	for _, item := range progress {
		out = append(out, orchestrateSessionStartProgressFromDaemonOne(item))
	}
	return out
}

func orchestrateSessionStartProgressFromDaemonOne(progress daemonclient.TaskSessionStartProgress) orchestrateSessionStartProgress {
	return orchestrateSessionStartProgress{
		IssueID:        progress.IssueID,
		OperationID:    progress.OperationID,
		OperationState: progress.OperationState,
		Phase:          progress.Phase,
		Message:        progress.Message,
		Percent:        progress.Percent,
		ElapsedMS:      progress.ElapsedMS,
		EnqueuedAt:     progress.EnqueuedAt,
		StartedAt:      progress.StartedAt,
		FinishedAt:     progress.FinishedAt,
	}
}

func orchestrateStaleCloseableFromDaemon(candidates []daemonclient.TaskStaleCloseableCandidate) []orchestrateStaleCloseableCandidate {
	if len(candidates) == 0 {
		return nil
	}
	out := make([]orchestrateStaleCloseableCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		out = append(out, orchestrateStaleCloseableCandidate{
			IssueID:          candidate.IssueID,
			Status:           candidate.Status,
			Evidence:         append([]string(nil), candidate.Evidence...),
			SuggestedCommand: candidate.SuggestedCommand,
		})
	}
	return out
}

func orchestrateContainmentRisksFromDaemon(risks []daemonclient.TaskContainmentRisk) []orchestrateContainmentRisk {
	if len(risks) == 0 {
		return nil
	}
	out := make([]orchestrateContainmentRisk, 0, len(risks))
	for _, risk := range risks {
		out = append(out, orchestrateContainmentRisk{
			IssueID:                risk.IssueID,
			ActiveBranch:           risk.ActiveBranch,
			RootIssueID:            risk.RootIssueID,
			RootBranch:             risk.RootBranch,
			ClosedChildIssueID:     risk.ClosedChildIssueID,
			EvidenceCommit:         risk.EvidenceCommit,
			EvidenceSubject:        risk.EvidenceSubject,
			RootContainsEvidence:   risk.RootContainsEvidence,
			ActiveContainsEvidence: risk.ActiveContainsEvidence,
			Classification:         risk.Classification,
			Message:                risk.Message,
			ChangedFiles:           append([]string(nil), risk.ChangedFiles...),
			OverlapFiles:           append([]string(nil), risk.OverlapFiles...),
			SuggestedCommand:       risk.SuggestedCommand,
		})
	}
	return out
}

func shortCLICommitHash(hash string) string {
	hash = strings.TrimSpace(hash)
	if len(hash) > 12 {
		return hash[:12]
	}
	return hash
}

func buildOrchestrateObserveForRoot(deps *Dependencies, rootIssueID string) (orchestrateObserveResult, error) {
	ctx, cancel := context.WithTimeout(context.Background(), daemonCommandTimeout)
	defer cancel()
	if err := ensureDaemon(ctx, deps, "cli"); err != nil {
		return orchestrateObserveResult{}, err
	}
	ready, err := deps.DaemonClient.TaskGraphReadiness(ctx, rootIssueID)
	if err != nil {
		return orchestrateObserveResult{}, err
	}
	now := orchestrateObserveNow()
	observations := orchestrateObservationsFromDaemon(ready.WorkerObservations, now)
	return orchestrateObserveResult{
		Mode:         "graph",
		RootIssueID:  ready.RootIssueID,
		GeneratedAt:  now,
		Observations: observations,
		Groups:       orchestrateObservationGroups(observations),
		Advice: map[string]interface{}{
			"status": fmt.Sprintf("az orchestrate status --root %s --json", ready.RootIssueID),
			"watch":  fmt.Sprintf("az orchestrate watch --root %s --since 0 --jsonl", ready.RootIssueID),
		},
	}, nil
}

func buildObserveForActiveWork(deps *Dependencies) (orchestrateObserveResult, error) {
	ctx, cancel := context.WithTimeout(context.Background(), daemonCommandTimeout)
	defer cancel()
	if err := ensureDaemon(ctx, deps, "cli"); err != nil {
		return orchestrateObserveResult{}, err
	}
	snapshot, err := deps.DaemonClient.ListTasksSnapshot(ctx)
	if err != nil {
		return orchestrateObserveResult{}, fmt.Errorf("list active issue candidates: %w", err)
	}
	candidates := observeActiveIssueIDs(snapshot.Tasks)
	now := orchestrateObserveNow()
	byIssue := make(map[string]orchestrateObservation)
	warnings := make([]string, 0)
	for _, issueID := range candidates {
		ready, err := deps.DaemonClient.TaskGraphReadiness(ctx, issueID)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("%s: %v", issueID, err))
			continue
		}
		for _, observation := range orchestrateObservationsFromDaemon(ready.WorkerObservations, now) {
			if _, exists := byIssue[observation.IssueID]; exists {
				continue
			}
			byIssue[observation.IssueID] = observation
		}
	}
	observations := make([]orchestrateObservation, 0, len(byIssue))
	for _, observation := range byIssue {
		observations = append(observations, observation)
	}
	sortOrchestrateObservations(observations)
	return orchestrateObserveResult{
		Mode:         "active",
		GeneratedAt:  now,
		Observations: observations,
		Groups:       orchestrateObservationGroups(observations),
		Warnings:     warnings,
		Advice: map[string]interface{}{
			"root_filter": "az observe --root <issue-id>",
		},
	}, nil
}

func observeActiveIssueIDs(tasks []domain.Task) []string {
	ids := make([]string, 0, len(tasks))
	seen := map[string]struct{}{}
	for _, task := range tasks {
		if task.ID.IsZero() {
			continue
		}
		if !observeActiveTask(task) {
			continue
		}
		id := task.ID.String()
		if _, exists := seen[id]; exists {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

func observeActiveTask(task domain.Task) bool {
	if task.HasTmuxSession || task.HasWorktree {
		return true
	}
	switch task.Status {
	case domain.StatusInProgress, domain.StatusInReview:
		return true
	default:
		return false
	}
}

func orchestrateObservationsFromDaemon(observations []domain.WorkerObservation, now time.Time) []orchestrateObservation {
	out := make([]orchestrateObservation, 0, len(observations))
	for _, observation := range observations {
		out = append(out, orchestrateObservationFromDaemon(observation, now))
	}
	sortOrchestrateObservations(out)
	return out
}

func orchestrateObservationFromDaemon(observation domain.WorkerObservation, now time.Time) orchestrateObservation {
	group := orchestrateObservationActionabilityGroup(observation.State)
	out := orchestrateObservation{
		IssueID:             strings.TrimSpace(observation.IssueID),
		State:               string(observation.State),
		Group:               group,
		Reason:              strings.TrimSpace(observation.Reason),
		Age:                 "unknown",
		EvidenceFlags:       orchestrateObservationEvidenceFlags(observation),
		EvidenceSummary:     cloneObservationStrings(observation.EvidenceSummary),
		Risks:               cloneObservationStrings(observation.Risks),
		NextActions:         cloneObservationStrings(observation.NextActions),
		LastMeaningfulEvent: observation.LastEvent,
		SourceTruthPolicy:   observation.SourceTruthPolicy,
	}
	if observation.LastEvent != nil && !observation.LastEvent.At.IsZero() {
		age := now.Sub(observation.LastEvent.At)
		if age < 0 {
			age = 0
		}
		out.AgeSeconds = int64(age.Round(time.Second).Seconds())
		out.Age = formatObservationAge(age)
	}
	return out
}

func cloneObservationStrings(values []string) []string {
	if len(values) == 0 {
		return []string{}
	}
	return append([]string(nil), values...)
}

func orchestrateObservationEvidenceFlags(observation domain.WorkerObservation) []string {
	flags := make([]string, 0, 6)
	if observation.LastEvent != nil {
		flags = append(flags, "last_event")
		switch strings.TrimSpace(observation.LastEvent.Kind) {
		case "mailbox":
			flags = append(flags, "mailbox_event")
		case "issue_event":
			flags = append(flags, "issue_event")
		}
	}
	if len(observation.EvidenceSummary) > 0 {
		flags = append(flags, "evidence_summary")
	}
	if len(observation.Risks) > 0 {
		flags = append(flags, "risks")
	}
	if len(observation.NextActions) > 0 {
		flags = append(flags, "next_actions")
	}
	return flags
}

func orchestrateObservationActionabilityGroup(state domain.WorkerObservationState) string {
	switch state {
	case domain.WorkerObservationWaitingHuman:
		return "needs_human"
	case domain.WorkerObservationReviewReady:
		return "review_ready"
	case domain.WorkerObservationBlocked, domain.WorkerObservationFailed, domain.WorkerObservationStale:
		return "blocked_failed_stale"
	case domain.WorkerObservationCleanupPending, domain.WorkerObservationDone:
		return "cleanup"
	default:
		return "working"
	}
}

func sortOrchestrateObservations(observations []orchestrateObservation) {
	sort.SliceStable(observations, func(i, j int) bool {
		left := orchestrateObservationGroupRank(observations[i].Group)
		right := orchestrateObservationGroupRank(observations[j].Group)
		if left != right {
			return left < right
		}
		if observations[i].AgeSeconds != observations[j].AgeSeconds {
			return observations[i].AgeSeconds > observations[j].AgeSeconds
		}
		return observations[i].IssueID < observations[j].IssueID
	})
}

func orchestrateObservationGroups(observations []orchestrateObservation) []orchestrateObservationGroup {
	groups := make([]orchestrateObservationGroup, 0, len(orchestrateObservationGroupOrder))
	byName := map[string][]string{}
	for _, observation := range observations {
		byName[observation.Group] = append(byName[observation.Group], observation.IssueID)
	}
	for _, spec := range orchestrateObservationGroupOrder {
		ids := byName[spec.name]
		if len(ids) == 0 {
			continue
		}
		groups = append(groups, orchestrateObservationGroup{
			Name:     spec.name,
			Title:    spec.title,
			IssueIDs: append([]string(nil), ids...),
		})
	}
	return groups
}

var orchestrateObservationGroupOrder = []struct {
	name  string
	title string
}{
	{name: "needs_human", title: "Needs human"},
	{name: "review_ready", title: "Review-ready"},
	{name: "blocked_failed_stale", title: "Blocked/failed/stale"},
	{name: "working", title: "Working"},
	{name: "cleanup", title: "Cleanup"},
}

func orchestrateObservationGroupRank(group string) int {
	for i, spec := range orchestrateObservationGroupOrder {
		if spec.name == group {
			return i
		}
	}
	return len(orchestrateObservationGroupOrder)
}

func formatObservationAge(age time.Duration) string {
	if age < 0 {
		age = 0
	}
	return age.Round(time.Second).String()
}

func printOrchestrateObserveResult(result orchestrateObserveResult) {
	if strings.TrimSpace(result.RootIssueID) != "" {
		fmt.Printf("Root issue: %s\n", result.RootIssueID)
	} else {
		fmt.Println("Scope: active issues")
	}
	printOrchestrateObservationGroups(result.Observations)
	if len(result.Warnings) > 0 {
		fmt.Println("Warnings:")
		for _, warning := range result.Warnings {
			fmt.Printf("- %s\n", warning)
		}
	}
}

func printOrchestrateObservationGroups(observations []orchestrateObservation) {
	if len(observations) == 0 {
		fmt.Println("- (no observations)")
		return
	}
	byGroup := map[string][]orchestrateObservation{}
	for _, observation := range observations {
		byGroup[observation.Group] = append(byGroup[observation.Group], observation)
	}
	for _, group := range orchestrateObservationGroupOrder {
		items := byGroup[group.name]
		if len(items) == 0 {
			continue
		}
		fmt.Printf("%s:\n", group.title)
		for _, observation := range items {
			age := observation.Age
			if age == "" {
				age = "unknown"
			}
			fmt.Printf("- %s state=%s age=%s\n", observation.IssueID, observation.State, age)
			if observation.Reason != "" {
				fmt.Printf("  reason: %s\n", observation.Reason)
			}
			if len(observation.EvidenceFlags) > 0 {
				fmt.Printf("  evidence flags: %s\n", strings.Join(observation.EvidenceFlags, ", "))
			}
			if len(observation.NextActions) > 0 {
				fmt.Printf("  next: %s\n", strings.Join(observation.NextActions, "; "))
			}
		}
	}
}

func formatSessionStartProgress(progress orchestrateSessionStartProgress) string {
	parts := make([]string, 0, 6)
	if state := strings.TrimSpace(progress.OperationState); state != "" {
		parts = append(parts, "state="+state)
	}
	if phase := strings.TrimSpace(progress.Phase); phase != "" {
		parts = append(parts, "phase="+phase)
	}
	if progress.OperationID != "" {
		parts = append(parts, "operation="+progress.OperationID)
	}
	if progress.ElapsedMS > 0 {
		parts = append(parts, fmt.Sprintf("elapsed=%s", formatMillisDuration(progress.ElapsedMS)))
	}
	if progress.Percent > 0 {
		parts = append(parts, fmt.Sprintf("progress=%d%%", progress.Percent))
	}
	if message := strings.TrimSpace(progress.Message); message != "" {
		parts = append(parts, message)
	}
	if len(parts) == 0 {
		return "pending"
	}
	return strings.Join(parts, " ")
}

func formatMillisDuration(ms int64) string {
	if ms < 0 {
		ms = 0
	}
	return (time.Duration(ms) * time.Millisecond).Round(time.Second).String()
}

func printOrchestrateStartResult(result orchestrateStartResult) {
	fmt.Printf("Root issue: %s\n", result.RootIssueID)
	fmt.Printf("Intent key: %s\n", result.IntentKey)
	fmt.Printf("Start limit: %d\n", result.Limit)
	fmt.Println("Started sessions:")
	if len(result.Started) == 0 {
		fmt.Println("- (none)")
	} else {
		for _, id := range result.Started {
			fmt.Printf("- %s\n", id)
		}
	}
	if len(result.Skipped) > 0 {
		fmt.Println("Skipped:")
		for _, id := range sortedKeys(result.Skipped) {
			fmt.Printf("- %s: %s\n", id, result.Skipped[id])
		}
	}
	if len(result.NestedRoots) > 0 {
		fmt.Println("Nested roots:")
		for _, id := range result.NestedRoots {
			fmt.Printf("- %s: start its orchestrator session with `az orchestrator-session start --root %s`\n", id, id)
		}
	}
	if len(result.Launched) > 0 {
		fmt.Println("Launch details:")
		for _, launch := range result.Launched {
			fmt.Printf("- %s: session=%s operation=%s state=%s\n", launch.IssueID, launch.SessionID, launch.OperationID, launch.OperationState)
			if launch.WorktreePath != "" {
				fmt.Printf("  worktree=%s\n", launch.WorktreePath)
			}
			fmt.Printf("  complete accepted worker: %s\n", launch.IntegrateHint)
			fmt.Printf("  repair stop only: %s\n", launch.CloseHint)
		}
	}
	if len(result.Warnings) > 0 {
		fmt.Println("Warnings:")
		for _, warning := range result.Warnings {
			fmt.Printf("- %s\n", warning)
		}
	}
	if result.Advice.WatchCommand != "" {
		fmt.Println("Next watch command (leave running while workers are active; do not add --once):")
		fmt.Printf("- %s\n", result.Advice.WatchCommand)
		if result.Advice.WatchInstruction != "" {
			fmt.Printf("- %s\n", result.Advice.WatchInstruction)
		}
	}
	if len(result.Pending) > 0 {
		fmt.Println("Pending starts:")
		for _, pending := range result.Pending {
			fmt.Printf("- %s: operation=%s state=%s reason=%s\n", pending.IssueID, pending.OperationID, pending.OperationState, pending.Reason)
			for _, command := range pending.FollowUpCommands {
				fmt.Printf("  follow up: %s\n", command)
			}
		}
	}
	if len(result.Failed) > 0 {
		fmt.Println("Failed:")
		for _, id := range sortedKeys(result.Failed) {
			fmt.Printf("- %s: %s\n", id, result.Failed[id])
		}
	}
}

func OrchestrateWatchCommand(deps *Dependencies, opts OrchestrateWatchOptions) error {
	restoreProject, err := applyExplicitProjectOverride(deps, opts.Project)
	if err != nil {
		return err
	}
	defer restoreProject()
	scope, err := resolveCLIOrchestrationScope(opts.RootIssueID)
	if err != nil {
		return err
	}
	if scope.Kind == domain.OrchestrationScopeProject {
		return projectOrchestrateWatchCommand(deps, opts, scope)
	}
	opts.RootIssueID = scope.RootIssueID.String()

	watchCtx, stopWatch := newWatchCommandContext("orchestrate watch")
	defer stopWatch()
	if watchCtx.Err() != nil {
		return nil
	}

	ctx, cancel := context.WithTimeout(watchCtx, daemonCommandTimeout)
	defer cancel()
	if err := ensureDaemon(ctx, deps, "cli"); err != nil {
		return err
	}

	lastSeq := opts.SinceSeq
	frame, err := buildOrchestrateWatchFrameContext(watchCtx, deps, opts.RootIssueID, lastSeq)
	if err != nil {
		if isWatchContextDone(watchCtx, err) {
			return nil
		}
		return err
	}
	lastSnapshotKey := orchestrateWatchFrameSnapshotKey(frame)
	if len(frame.Events) > 0 || len(frame.Pending) > 0 || len(frame.SessionStartProgress) > 0 || len(frame.ActiveSessions) > 0 || opts.Once {
		if err := emitOrchestrateWatchFrame(frame, opts.JSONL, opts.Compact); err != nil {
			return err
		}
	}
	lastSeq = frame.NextSince
	if opts.Once {
		return nil
	}

	for {
		events, err := deps.DaemonClient.Subscribe(watchCtx, opts.Project, frame.sourceRevision)
		if err != nil {
			if isWatchContextDone(watchCtx, err) {
				return nil
			}
			return err
		}
		select {
		case <-watchCtx.Done():
			return nil
		case _, ok := <-events:
			if !ok {
				continue
			}
		}
		frame, err = buildOrchestrateWatchFrameContext(watchCtx, deps, opts.RootIssueID, lastSeq)
		if err != nil {
			if isWatchContextDone(watchCtx, err) {
				return nil
			}
			return err
		}
		snapshotKey := orchestrateWatchFrameSnapshotKey(frame)
		if len(frame.Events) == 0 && snapshotKey == lastSnapshotKey {
			continue
		}
		if err := emitOrchestrateWatchFrame(frame, opts.JSONL, opts.Compact); err != nil {
			return err
		}
		lastSnapshotKey = snapshotKey
		lastSeq = frame.NextSince
	}
}

type orchestrateWatchReadinessCache struct {
	cachedFrame     orchestrateWatchFrame
	refreshedAt     time.Time
	refreshInterval time.Duration
}

func newOrchestrateWatchReadinessCache(frame orchestrateWatchFrame, refreshedAt time.Time, pollInterval time.Duration) orchestrateWatchReadinessCache {
	return orchestrateWatchReadinessCache{
		cachedFrame:     frame,
		refreshedAt:     refreshedAt,
		refreshInterval: orchestrateWatchReadinessRefreshInterval(pollInterval),
	}
}

func orchestrateWatchReadinessRefreshInterval(pollInterval time.Duration) time.Duration {
	const (
		minInterval = 2 * time.Second
		maxInterval = 10 * time.Second
	)
	if pollInterval <= 0 {
		return minInterval
	}
	interval := pollInterval * 8
	if interval < minInterval {
		return minInterval
	}
	if interval > maxInterval {
		return maxInterval
	}
	return interval
}

func (cache orchestrateWatchReadinessCache) shouldRefresh(now time.Time, eventCount int) (bool, string) {
	if eventCount > 0 {
		return true, "mailbox_events"
	}
	if cache.refreshedAt.IsZero() {
		return true, "cache_empty"
	}
	if !now.Before(cache.refreshedAt.Add(cache.refreshInterval)) {
		return true, "refresh_interval_elapsed"
	}
	return false, "unchanged_mailbox"
}

func (cache orchestrateWatchReadinessCache) decision(refresh bool) string {
	if refresh {
		return "miss"
	}
	return "hit"
}

func (cache *orchestrateWatchReadinessCache) store(frame orchestrateWatchFrame, refreshedAt time.Time) {
	cache.cachedFrame = frame
	cache.refreshedAt = refreshedAt
}

func (cache orchestrateWatchReadinessCache) cachedReadinessFrame(events []mailEvent, since, nextSince int64) orchestrateWatchFrame {
	frame := cache.cachedFrame
	frame.SinceSeq = since
	frame.NextSince = nextSince
	frame.Events = events
	return frame
}

func orchestrateWatchFrameFromReadiness(ready daemonclient.TaskGraphReadiness, events []mailEvent, since, nextSince int64) orchestrateWatchFrame {
	return orchestrateWatchFrame{
		sourceRevision:         ready.Revision,
		RootIssueID:            ready.RootIssueID,
		SinceSeq:               since,
		NextSince:              nextSince,
		Capacity:               orchestrateCapacityFromDaemon(ready.Capacity),
		Runnable:               ready.Runnable,
		NestedRoots:            orchestrateNestedRootsFromDaemon(ready.NestedRoots),
		Pending:                orchestratePendingStartsFromDaemon(ready.Pending),
		PublicationQueue:       append([]domain.PublicationOperation(nil), ready.PublicationQueue...),
		Active:                 ready.Active,
		ActiveSessions:         orchestrateActiveSessionsFromDaemon(ready.ActiveSessions),
		SessionStartProgress:   orchestrateSessionStartProgressFromDaemon(ready.SessionStartProgress),
		StaleCloseableChildren: orchestrateStaleCloseableFromDaemon(ready.StaleCloseableChildren),
		ContainmentRisks:       orchestrateContainmentRisksFromDaemon(ready.ContainmentRisks),
		Blocked:                ready.Blocked,
		Events:                 events,
		PersistenceGuard:       "Daemon-enforced: parent idle/turn completion is wake-required while direct nested roots remain and complete-check has not passed; the durable cursor is resumed after restart or session replacement.",
	}
}

func orchestrateWatchFrameSnapshotKey(frame orchestrateWatchFrame) string {
	type snapshot struct {
		Capacity               orchestrateCapacitySummary           `json:"capacity"`
		Runnable               []string                             `json:"runnable"`
		NestedRoots            []orchestrateNestedRoot              `json:"nested_roots,omitempty"`
		Pending                []orchestratePendingStart            `json:"pending,omitempty"`
		PublicationQueue       []domain.PublicationOperation        `json:"publication_queue,omitempty"`
		Active                 []string                             `json:"active,omitempty"`
		ActiveSessions         []orchestrateActiveSession           `json:"active_sessions,omitempty"`
		SessionStartProgress   []orchestrateSessionStartProgress    `json:"session_start_progress,omitempty"`
		StaleCloseableChildren []orchestrateStaleCloseableCandidate `json:"stale_closeable_children,omitempty"`
		ContainmentRisks       []orchestrateContainmentRisk         `json:"containment_risks,omitempty"`
		Blocked                map[string]string                    `json:"blocked"`
	}
	activeSessions := append([]orchestrateActiveSession(nil), frame.ActiveSessions...)
	for i := range activeSessions {
		if activeSessions[i].StartProgress != nil {
			progress := *activeSessions[i].StartProgress
			progress.ElapsedMS = 0
			activeSessions[i].StartProgress = &progress
		}
	}
	nestedRoots := normalizeOrchestrateNestedRootsForSnapshot(frame.NestedRoots)
	sessionStartProgress := append([]orchestrateSessionStartProgress(nil), frame.SessionStartProgress...)
	for i := range sessionStartProgress {
		sessionStartProgress[i].ElapsedMS = 0
	}
	encoded, err := json.Marshal(snapshot{
		Capacity:               frame.Capacity,
		Runnable:               frame.Runnable,
		NestedRoots:            nestedRoots,
		Pending:                frame.Pending,
		PublicationQueue:       frame.PublicationQueue,
		Active:                 frame.Active,
		ActiveSessions:         activeSessions,
		SessionStartProgress:   sessionStartProgress,
		StaleCloseableChildren: frame.StaleCloseableChildren,
		ContainmentRisks:       frame.ContainmentRisks,
		Blocked:                frame.Blocked,
	})
	if err != nil {
		return ""
	}
	return string(encoded)
}

func normalizeOrchestrateNestedRootsForSnapshot(nested []orchestrateNestedRoot) []orchestrateNestedRoot {
	if len(nested) == 0 {
		return nil
	}
	out := append([]orchestrateNestedRoot(nil), nested...)
	for i := range out {
		if out[i].ActiveSession == nil {
			continue
		}
		active := *out[i].ActiveSession
		if active.StartProgress != nil {
			progress := *active.StartProgress
			progress.ElapsedMS = 0
			active.StartProgress = &progress
		}
		out[i].ActiveSession = &active
	}
	return out
}

func watchDaemonCommand[T any](deps *Dependencies, call func(context.Context) (T, error)) (T, error) {
	return watchDaemonCommandContext(context.Background(), deps, call)
}

func watchDaemonCommandContext[T any](ctx context.Context, deps *Dependencies, call func(context.Context) (T, error)) (T, error) {
	linkCtx := context.Context(nil)
	if deps != nil {
		linkCtx = deps.TraceContext
	}
	segmentCtx, endSegment := newWatchTraceSegment(ctx, linkCtx, "daemon_command")
	value, err := call(segmentCtx)
	if err == nil || !reconnect.IsTransientTransportError(err) {
		endSegment(err)
		return value, err
	}
	value, err = commandWithDaemonAutostartRetry(segmentCtx, deps, call)
	endSegment(err)
	return value, err
}

func shouldContinueOrchestrateWatchAfterError(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	if strings.Contains(message, "short_frame") && strings.Contains(message, "i/o timeout") {
		return true
	}
	return strings.Contains(message, "context deadline exceeded")
}

func OrchestrateCompleteCheckCommand(deps *Dependencies, opts OrchestrateCompleteCheckOptions) error {
	restoreProject, err := applyExplicitProjectOverride(deps, opts.Project)
	if err != nil {
		return err
	}
	defer restoreProject()
	scope, err := resolveCLIOrchestrationScope(opts.RootIssueID)
	if err != nil {
		return err
	}
	if scope.Kind == domain.OrchestrationScopeProject {
		return projectOrchestrateCompleteCheckCommand(deps, opts, scope)
	}
	opts.RootIssueID = scope.RootIssueID.String()

	ctx, cancel := context.WithTimeout(context.Background(), daemonCommandTimeout)
	defer cancel()
	if err := ensureDaemon(ctx, deps, "cli"); err != nil {
		return err
	}
	result, err := deps.DaemonClient.TaskCompleteCheck(ctx, opts.RootIssueID)
	if err != nil {
		return err
	}
	if opts.JSON {
		if err := printJSON(result); err != nil {
			return err
		}
		if !result.Pass {
			return fmt.Errorf("orchestration completion gate failed")
		}
		return nil
	}

	if result.Pass {
		fmt.Printf("Root issue %s is safe to close.\n", result.RootIssueID)
		return nil
	}
	fmt.Printf("Root issue %s is NOT ready to close:\n", result.RootIssueID)
	for _, reason := range result.Reasons {
		fmt.Printf("- %s\n", reason)
	}
	if len(result.StaleCloseableChildren) > 0 {
		fmt.Println("Stale-closeable children:")
		for _, candidate := range result.StaleCloseableChildren {
			fmt.Printf("- %s status=%s\n", candidate.IssueID, candidate.Status)
			if len(candidate.Evidence) > 0 {
				fmt.Printf("  evidence: %s\n", strings.Join(candidate.Evidence, "; "))
			}
			if candidate.SuggestedCommand != "" {
				fmt.Printf("  next: %s\n", candidate.SuggestedCommand)
			}
		}
	}
	if len(result.Advice) > 0 {
		fmt.Println("Suggested next steps:")
		for _, advice := range result.Advice {
			fmt.Printf("- %s\n", advice)
		}
	}
	return fmt.Errorf("orchestration completion gate failed")
}

func OrchestratePromptCommand(deps *Dependencies, opts OrchestratePromptOptions) error {
	restoreProject, err := applyExplicitProjectOverride(deps, opts.Project)
	if err != nil {
		return err
	}
	defer restoreProject()

	ctx, cancel := context.WithTimeout(context.Background(), daemonCommandTimeout)
	defer cancel()
	if err := ensureDaemon(ctx, deps, "cli"); err != nil {
		return err
	}
	snapshot, err := listTasksSnapshotForCLI(ctx, deps)
	if err != nil {
		return fmt.Errorf("list tasks: %w", err)
	}
	tasks := snapshot.Tasks
	task, ok := findTaskByID(tasks, strings.TrimSpace(opts.IssueID))
	if !ok {
		return fmt.Errorf("issue not found: %s", opts.IssueID)
	}
	rootIssueID := strings.TrimSpace(opts.RootIssueID)
	parentIssueID := rootIssueID
	if task.ParentID != nil && strings.TrimSpace(task.ParentID.String()) != "" {
		parentIssueID = strings.TrimSpace(task.ParentID.String())
	}
	if parentIssueID == "" {
		parentIssueID = strings.TrimSpace(task.ID.String())
	}
	if rootIssueID == "" {
		rootIssueID = parentIssueID
	}
	coordination := strings.ToLower(strings.TrimSpace(opts.Coordination))
	if coordination == "" {
		coordination = "native"
	}
	result := buildOrchestratePromptResult(rootIssueID, parentIssueID, task, coordination)
	if opts.JSON {
		return printJSON(result)
	}
	fmt.Print(result.Prompt)
	if !strings.HasSuffix(result.Prompt, "\n") {
		fmt.Println()
	}
	return nil
}

func OrchestrateIntegrateCommand(deps *Dependencies, opts OrchestrateIntegrateOptions) error {
	restoreProject, err := applyExplicitProjectOverride(deps, opts.Project)
	if err != nil {
		return err
	}
	defer restoreProject()

	ctx, cancel := context.WithTimeout(context.Background(), orchestrateIntegrateCommandTimeout(opts.Apply))
	defer cancel()
	result := orchestrateIntegrateResult{
		IssueID: opts.IssueID,
		Apply:   opts.Apply,
	}
	if err := ensureDaemon(ctx, deps, "cli"); err != nil {
		return finishOrchestrateIntegratePreflightFailure(opts, &result, err)
	}
	wt, found, err := worktreeForIssue(ctx, deps, opts.IssueID)
	if err != nil {
		return finishOrchestrateIntegratePreflightFailure(opts, &result, err)
	}
	readiness, err := deps.DaemonClient.TaskIntegrationReadiness(ctx, opts.IssueID, deps.RepoDir)
	if err != nil {
		return finishOrchestrateIntegratePreflightFailure(opts, &result, err)
	}
	mergeReady := readiness.Ready
	contextRiskBlocked := readiness.ContextRisk != nil && domain.IssueContextRiskRequiresStructuredCloseout(*readiness.ContextRisk)
	closeoutReady := mergeReady && !contextRiskBlocked
	commands := orchestrateIntegrationCommands(opts.IssueID, opts.Project, wt, found, closeoutReady)
	result.MergeReady = mergeReady
	result.CloseoutReady = closeoutReady
	result.ContextRisk = readiness.ContextRisk
	result.Reasons = readiness.Reasons
	result.Commands = commands
	if contextRiskBlocked {
		result.Reasons = append(result.Reasons, issueContextRiskCloseoutBlockMessage(opts.IssueID))
	}
	if found {
		result.WorktreePath = wt.Path
		result.Branch = wt.Branch
	}
	if opts.Apply {
		applyErr := applyOrchestrateIntegration(deps, opts.IssueID, opts.Project, mergeReady, &result)
		if opts.JSON {
			if err := printJSON(result); err != nil {
				return err
			}
			if applyErr != nil {
				return applyErr
			}
			return nil
		}
		printOrchestrateIntegrateApplyResult(result)
		if applyErr != nil {
			return applyErr
		}
		return nil
	}
	if opts.JSON {
		return printJSON(result)
	}
	fmt.Printf("Integration guidance for %s\n", opts.IssueID)
	if !mergeReady {
		fmt.Println("Merge guidance: BLOCKED (insufficient completion evidence)")
		for _, reason := range result.Reasons {
			fmt.Printf("- %s\n", reason)
		}
	}
	printIssueContextRiskCloseout(result.ContextRisk)
	if mergeReady && !closeoutReady {
		fmt.Println("Closeout guidance: BLOCKED (high context risk needs structured closeout evidence)")
		for _, reason := range result.Reasons {
			fmt.Printf("- %s\n", reason)
		}
	}
	if found {
		fmt.Printf("Worktree: %s\n", wt.Path)
		fmt.Printf("Branch: %s\n", wt.Branch)
	} else {
		fmt.Println("Worktree: not found in daemon projection")
	}
	fmt.Println("Commands:")
	for _, command := range commands {
		fmt.Printf("- %s\n", command)
	}
	return nil
}

func orchestrateIntegrateCommandTimeout(apply bool) time.Duration {
	if apply {
		return issueCloseCleanupTimeout
	}
	return daemonCommandTimeout
}

func finishOrchestrateIntegratePreflightFailure(opts OrchestrateIntegrateOptions, result *orchestrateIntegrateResult, err error) error {
	wrapped := fmt.Errorf("phase preflight for issue %s: %w", opts.IssueID, err)
	if !opts.Apply {
		return wrapped
	}
	result.Steps = append(result.Steps, orchestrateIntegrateStep{
		Name:   "preflight",
		Status: "failed",
		Error:  err.Error(),
	})
	result.Recovery = orchestrateIntegrationRecovery(opts.IssueID, opts.Project, "preflight_failed")
	if opts.JSON {
		if printErr := printJSON(*result); printErr != nil {
			return printErr
		}
		return wrapped
	}
	printOrchestrateIntegrateApplyResult(*result)
	return wrapped
}

func applyOrchestrateIntegration(deps *Dependencies, issueID, projectID string, mergeReady bool, result *orchestrateIntegrateResult) error {
	if !mergeReady {
		result.Steps = append(result.Steps, orchestrateIntegrateStep{
			Name:   "completion_evidence",
			Status: "failed",
			Error:  "missing completion evidence",
		})
		result.Recovery = orchestrateIntegrationRecovery(issueID, projectID, "missing_completion_evidence")
		return fmt.Errorf("cannot apply integration for %s: missing completion evidence", issueID)
	}
	result.Steps = append(result.Steps, orchestrateIntegrateStep{Name: "completion_evidence", Status: "success"})
	if result.ContextRisk != nil && domain.IssueContextRiskRequiresStructuredCloseout(*result.ContextRisk) {
		result.Steps = append(result.Steps, orchestrateIntegrateStep{
			Name:   "context_risk",
			Status: "failed",
			Error:  issueContextRiskCloseoutBlockMessage(issueID),
		})
		result.Recovery = append(result.Recovery, "record root_cause, invariant, regression_validation, or a structured risk note, then retry integration")
		return fmt.Errorf("cannot apply integration for %s: %s", issueID, issueContextRiskCloseoutBlockMessage(issueID))
	}
	result.Steps = append(result.Steps, orchestrateIntegrateStep{Name: "context_risk", Status: "success"})

	cleanupCtx, cancel := context.WithTimeout(context.Background(), issueCloseCleanupTimeout)
	defer cancel()
	closeResult, err := deps.DaemonClient.CloseTask(cleanupCtx, issueID, daemonclient.TaskStatusOptions{
		IntegrateBeforeClose: true,
	})
	if err != nil {
		wrapped := fmt.Errorf("phase integrate_and_close for issue %s: %w", issueID, err)
		result.Steps = append(result.Steps, orchestrateIntegrateStep{Name: "integrate_and_close", Status: "failed", Error: wrapped.Error()})
		result.Recovery = orchestrateIntegrationRecovery(issueID, projectID, "integration_failed")
		return fmt.Errorf("apply integration for %s: %w", issueID, wrapped)
	}
	result.Applied = true
	closeOutput := fmt.Sprintf("Closed %s (session_stopped=%t, worktree_removed=%t)", closeResult.TaskID, closeResult.SessionStopped, closeResult.WorktreeRemoved)
	if closeResult.Integrated {
		closeOutput = fmt.Sprintf("Merged %s into %s; %s", closeResult.IntegratedSourceBranch, closeResult.IntegratedTargetBranch, closeOutput)
	} else if closeResult.IntegrationRequested {
		closeOutput = fmt.Sprintf("Integration requested; %s", closeOutput)
	}
	result.Steps = append(result.Steps, orchestrateIntegrateStep{
		Name:   "integrate_and_close",
		Status: "success",
		Output: closeOutput,
	})

	note := fmt.Sprintf("Integrated by `%s`: daemon task.close integrated the branch, stopped session/worktree runtime if present, and closed the issue.", orchestrateIntegrateApplyCommandForProject(issueID, projectID))
	if err := deps.DaemonClient.AppendTaskNotes(cleanupCtx, issueID, note); err != nil {
		result.Steps = append(result.Steps, orchestrateIntegrateStep{Name: "append_evidence", Status: "failed", Error: err.Error()})
		result.Recovery = orchestrateIntegrationRecovery(issueID, projectID, "post_close_failed")
		return fmt.Errorf("integration applied for %s but append evidence failed: %w", issueID, err)
	} else {
		result.Steps = append(result.Steps, orchestrateIntegrateStep{Name: "append_evidence", Status: "success"})
	}
	return nil
}

func printOrchestrateIntegrateApplyResult(result orchestrateIntegrateResult) {
	fmt.Printf("Integration apply result for %s\n", result.IssueID)
	for _, step := range result.Steps {
		if step.Error != "" {
			fmt.Printf("- %s: %s (%s)\n", step.Name, step.Status, step.Error)
			continue
		}
		if step.Output != "" {
			fmt.Printf("- %s: %s (%s)\n", step.Name, step.Status, step.Output)
			continue
		}
		fmt.Printf("- %s: %s\n", step.Name, step.Status)
	}
	if len(result.Recovery) > 0 {
		fmt.Println("Recovery:")
		for _, item := range result.Recovery {
			fmt.Printf("- %s\n", item)
		}
	}
}

func orchestrateIntegrationRecovery(issueID, projectID, reason string) []string {
	switch reason {
	case "preflight_failed":
		return []string{
			fmt.Sprintf("no integration/cleanup/status mutation started; retry from preflight: %s", orchestrateIntegrateApplyCommandForProject(issueID, projectID)),
			fmt.Sprintf("if this repeats, inspect daemon/runtime health before retrying: %s", issueGetCommandForProject(issueID, projectID)),
		}
	case "missing_completion_evidence":
		return []string{
			fmt.Sprintf("review worker output and send a worker-integration-ready mailbox event for %s, or close if ready to integrate and clean up: %s", issueID, issueCloseCommandForProject(issueID, projectID)),
			fmt.Sprintf("retry: %s", orchestrateIntegrateApplyCommandForProject(issueID, projectID)),
		}
	case "merge_failed", "integration_failed":
		return []string{
			fmt.Sprintf("inspect merge failure and retry existing merge path: %s", branchMergeCommandForProject(issueID, projectID)),
			fmt.Sprintf("after repair, retry daemon-owned integration/cleanup: %s", orchestrateIntegrateApplyCommandForProject(issueID, projectID)),
		}
	case "post_close_failed":
		return []string{
			fmt.Sprintf("integration/close already completed; inspect issue state: %s", issueGetCommandForProject(issueID, projectID)),
			fmt.Sprintf("append evidence notes to %s with the merge and validation summary", issueID),
		}
	default:
		return []string{
			fmt.Sprintf("run cleanup steps manually: %s", orchestrateCloseSessionCommandForProject(issueID, projectID)),
			fmt.Sprintf("close the worker issue if ready to integrate and clean up: %s", issueCloseCommandForProject(issueID, projectID)),
			fmt.Sprintf("append evidence notes to %s with the merge and validation summary", issueID),
		}
	}
}

func OrchestrateCloseSessionCommand(deps *Dependencies, opts OrchestrateCloseSessionOptions) error {
	restoreProject, err := applyExplicitProjectOverride(deps, opts.Project)
	if err != nil {
		return err
	}
	defer restoreProject()

	ctx, cancel := context.WithTimeout(context.Background(), daemonCommandTimeout)
	defer cancel()
	if err := ensureDaemon(ctx, deps, "cli"); err != nil {
		return err
	}
	output, err := deps.DaemonClient.StopSession(ctx, opts.IssueID)
	if err != nil {
		return fmt.Errorf("close orchestrate session: %w", err)
	}
	result := orchestrateCloseSessionResult{IssueID: opts.IssueID, Output: output}
	if opts.JSON {
		return printJSON(result)
	}
	if strings.TrimSpace(output) != "" {
		fmt.Print(output)
	}
	fmt.Printf("Session closed for %s. If work is ready to integrate and clean up, close the issue with `%s`.\n", opts.IssueID, issueCloseCommand(opts.IssueID))
	return nil
}

func OrchestrateMessageCommand(deps *Dependencies, opts OrchestrateMessageOptions) error {
	restoreProject, err := applyExplicitProjectOverride(deps, opts.Project)
	if err != nil {
		return err
	}
	defer restoreProject()

	ctx, cancel := context.WithTimeout(context.Background(), daemonCommandTimeout)
	defer cancel()
	if err := ensureDaemon(ctx, deps, "cli"); err != nil {
		return err
	}
	if err := rejectAccidentalSelfDelivery(opts); err != nil {
		return err
	}
	event, err := deps.DaemonClient.MailSend(ctx, protocol.MailSendCommandBody{
		RepoDir:     deps.RepoDir,
		ParentIssue: opts.RootIssueID,
		IssueID:     naming.IssueID(strings.TrimSpace(opts.IssueID)),
		Type:        strings.TrimSpace(opts.Type),
		From:        "orchestrator",
		To:          strings.TrimSpace(opts.IssueID),
		Body:        opts.Body,
	})
	if err != nil {
		return fmt.Errorf("record orchestrator message: %w", err)
	}
	output, err := deps.DaemonClient.SendSessionMessage(ctx, opts.IssueID, formatOrchestratorSessionMessage(opts))
	if err != nil {
		return fmt.Errorf("mail event seq=%d recorded but active delivery failed: %w", event.Seq, err)
	}
	result := orchestrateMessageResult{
		RootIssueID: opts.RootIssueID,
		IssueID:     opts.IssueID,
		Type:        opts.Type,
		Mailbox:     event,
		Delivered:   true,
		Output:      output,
	}
	if opts.JSON {
		return printJSON(result)
	}
	fmt.Printf("message delivered to %s; mailbox seq=%d parent=%s type=%s\n", opts.IssueID, event.Seq, event.ParentIssue, event.Type)
	return nil
}

func rejectAccidentalSelfDelivery(opts OrchestrateMessageOptions) error {
	if opts.ForceSelfDelivery {
		return nil
	}
	activeIssueID := strings.TrimSpace(os.Getenv("AZEDARACH_ISSUE_ID"))
	targetIssueID := strings.TrimSpace(opts.IssueID)
	if activeIssueID == "" || targetIssueID == "" || !naming.IssueIDsEqual(activeIssueID, targetIssueID) {
		return nil
	}
	return fmt.Errorf("refusing to deliver orchestrate message to the active issue %s; worker-to-parent reporting should use `az mail send --parent %s --issue %s --type %s --body ...` so the event is recorded without injecting it back into this session (use --force-self-delivery only for intentional self-delivery)", targetIssueID, strings.TrimSpace(opts.RootIssueID), targetIssueID, strings.TrimSpace(opts.Type))
}

func formatOrchestratorSessionMessage(opts OrchestrateMessageOptions) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Orchestrator message for issue %s under root %s:\n\n", strings.TrimSpace(opts.IssueID), strings.TrimSpace(opts.RootIssueID))
	b.WriteString(strings.TrimSpace(opts.Body))
	b.WriteString("\n\nContinue from the current state. If this changes your status, update the issue notes/status and send worker-progress, worker-blocked, or worker-integration-ready evidence as appropriate.")
	return b.String()
}

func buildOrchestrateWatchFrame(deps *Dependencies, rootIssueID string, since int64) (orchestrateWatchFrame, error) {
	return buildOrchestrateWatchFrameContext(context.Background(), deps, rootIssueID, since)
}

func buildOrchestrateWatchFrameContext(ctx context.Context, deps *Dependencies, rootIssueID string, since int64) (orchestrateWatchFrame, error) {
	ready, err := deps.DaemonClient.TaskGraphReadiness(ctx, rootIssueID)
	if err != nil {
		return orchestrateWatchFrame{}, err
	}
	events, err := deps.DaemonClient.MailList(ctx, protocol.MailListCommandBody{
		RepoDir:     deps.RepoDir,
		ParentIssue: rootIssueID,
		SinceSeq:    since + 1,
		Limit:       200,
	})
	if err != nil {
		return orchestrateWatchFrame{}, err
	}
	nextSince := nextMailboxSeq(events, since)
	if nextSince > since {
		checkpointRootOrchestratorCursor(ctx, deps, rootIssueID, nextSince)
	}
	watchEvents := make([]mailEvent, 0, len(events))
	for _, event := range events {
		watchEvents = append(watchEvents, protocolToLocalMailEvent(event))
	}
	return orchestrateWatchFrameFromReadiness(ready, watchEvents, since, nextSince), nil
}

func checkpointRootOrchestratorCursor(ctx context.Context, deps *Dependencies, rootIssueID string, cursor int64) {
	if cursor <= 0 || strings.TrimSpace(os.Getenv("TMUX_PANE")) == "" {
		return
	}
	sessionID, err := tmuxPaneSessionName(ctx)
	if err != nil || strings.TrimSpace(sessionID) == "" {
		return
	}
	scope, err := domain.RootedOrchestrationScope(rootIssueID)
	if err != nil {
		return
	}
	_, _ = deps.DaemonClient.OrchestrationSnapshot(ctx, protocol.OrchestrationSnapshotRequest{Scope: scope, SessionID: sessionID, ObservedCursor: cursor})
}

func compactFrameFromStatusResult(result orchestrateStatusResult, since, nextSince int64) orchestrateCompactFrame {
	events := make([]mailEvent, 0, len(result.MailboxEvents))
	for _, event := range result.MailboxEvents {
		events = append(events, protocolToLocalMailEvent(event))
	}
	frame := orchestrateWatchFrame{
		RootIssueID:            result.RootIssueID,
		SinceSeq:               since,
		NextSince:              nextSince,
		Runnable:               result.Runnable,
		NestedRoots:            result.NestedRoots,
		Pending:                result.Pending,
		PublicationQueue:       result.PublicationQueue,
		Active:                 result.Active,
		ActiveSessions:         result.ActiveSessions,
		SessionStartProgress:   result.SessionStartProgress,
		StaleCloseableChildren: result.StaleCloseableChildren,
		Blocked:                result.Blocked,
		Events:                 events,
	}
	compact := compactFrameFromWatchFrame(frame)
	compact.Warnings = result.Warnings
	compact.Advice = copyAdvice(result.Advice)
	return compact
}

func copyAdvice(advice map[string]interface{}) map[string]interface{} {
	if len(advice) == 0 {
		return nil
	}
	out := make(map[string]interface{}, len(advice))
	for key, value := range advice {
		out[key] = value
	}
	return out
}

func compactFrameFromWatchFrame(frame orchestrateWatchFrame) orchestrateCompactFrame {
	compact := orchestrateCompactFrame{
		RootIssueID: frame.RootIssueID,
		SinceSeq:    frame.SinceSeq,
		NextSince:   frame.NextSince,
		Capacity: orchestrateCompactCapacity{
			Runnable: len(frame.Runnable),
			Active:   len(frame.ActiveSessions),
			Blocked:  len(frame.Blocked),
			Pending:  len(frame.Pending),
			Activity: compactActivityCounts(frame.ActiveSessions),
		},
		Readiness: orchestrateCompactReadiness{
			Runnable:               append([]string(nil), frame.Runnable...),
			Active:                 compactActiveSessions(frame.ActiveSessions),
			Blocked:                compactBlocked(frame.Blocked),
			Pending:                append([]orchestratePendingStart(nil), frame.Pending...),
			PublicationQueue:       append([]domain.PublicationOperation(nil), frame.PublicationQueue...),
			NestedRoots:            compactNestedRoots(frame.NestedRoots),
			SessionStartProgress:   compactSessionStartProgress(frame.SessionStartProgress),
			StaleCloseableChildren: append([]orchestrateStaleCloseableCandidate(nil), frame.StaleCloseableChildren...),
		},
		Events:     compactEvents(frame.Events),
		EventCount: len(frame.Events),
	}
	if frame.PersistenceGuard != "" {
		compact.Advice = map[string]interface{}{"persistence_guard": frame.PersistenceGuard}
	}
	return compact
}

func compactActivityCounts(activeSessions []orchestrateActiveSession) map[string]int {
	counts := make(map[string]int)
	for _, active := range activeSessions {
		activity := strings.TrimSpace(active.Activity)
		if activity == "" {
			activity = "unknown"
		}
		counts[activity]++
	}
	if len(counts) == 0 {
		return nil
	}
	return counts
}

func compactActiveSessions(activeSessions []orchestrateActiveSession) []orchestrateCompactActiveSession {
	out := make([]orchestrateCompactActiveSession, 0, len(activeSessions))
	for _, active := range activeSessions {
		out = append(out, orchestrateCompactActiveSession{
			IssueID:        active.IssueID,
			Status:         active.Status,
			Activity:       active.Activity,
			ActivitySource: active.ActivitySource,
		})
	}
	return out
}

func compactBlocked(blocked map[string]string) map[string]string {
	if len(blocked) == 0 {
		return nil
	}
	out := make(map[string]string, len(blocked))
	for id, reason := range blocked {
		out[id] = reason
	}
	return out
}

func compactNestedRoots(nestedRoots []orchestrateNestedRoot) []orchestrateCompactNestedRoot {
	out := make([]orchestrateCompactNestedRoot, 0, len(nestedRoots))
	for _, nested := range nestedRoots {
		compact := orchestrateCompactNestedRoot{
			IssueID:  nested.IssueID,
			Status:   nested.Status,
			Type:     nested.Type,
			Children: nested.ChildCount,
		}
		if nested.ActiveSession != nil {
			compact.Activity = nested.ActiveSession.Activity
		}
		out = append(out, compact)
	}
	return out
}

func compactSessionStartProgress(progress []orchestrateSessionStartProgress) []orchestrateCompactSessionProgress {
	out := make([]orchestrateCompactSessionProgress, 0, len(progress))
	for _, item := range progress {
		out = append(out, orchestrateCompactSessionProgress{
			IssueID:        item.IssueID,
			OperationState: item.OperationState,
			Phase:          item.Phase,
			Percent:        item.Percent,
			Message:        item.Message,
		})
	}
	return out
}

func compactEvents(events []mailEvent) []orchestrateCompactEvent {
	out := make([]orchestrateCompactEvent, 0, len(events))
	for _, event := range events {
		out = append(out, orchestrateCompactEvent{
			Seq:            event.Seq,
			IssueID:        event.IssueID,
			Type:           event.Type,
			From:           event.From,
			To:             event.To,
			CreatedAt:      event.CreatedAt,
			WorkerEvidence: compactWorkerEvidenceSummary(event),
		})
	}
	return out
}

func compactWorkerEvidenceSummary(event mailEvent) *orchestrateWorkerEvidenceSummary {
	if payloadSummary := compactWorkerEvidenceSummaryFromPayload(event.Payload); payloadSummary != nil {
		return payloadSummary
	}
	packet, validation := domain.ParseWorkerEvidencePacketBody(event.Body)
	if !validation.Found {
		return nil
	}
	status := "incomplete"
	if validation.Complete {
		status = "complete"
	}
	return &orchestrateWorkerEvidenceSummary{
		ValidationStatus: status,
		Summary:          packet.Summary,
		ReviewStatus:     packet.Review.Status,
		Risks:            nonEmptyStringCopy(packet.Risks),
		Problems:         validation.Problems(),
	}
}

func compactWorkerEvidenceSummaryFromPayload(payload map[string]interface{}) *orchestrateWorkerEvidenceSummary {
	validationMap, _ := payload["worker_evidence_validation"].(map[string]interface{})
	if len(validationMap) == 0 {
		return nil
	}
	status := "incomplete"
	if compactBoolValue(validationMap["complete"]) {
		status = "complete"
	}
	if !compactBoolValue(validationMap["found"]) {
		status = "missing"
	}
	packetMap, _ := payload["worker_evidence"].(map[string]interface{})
	return &orchestrateWorkerEvidenceSummary{
		ValidationStatus: status,
		Summary:          compactStringValue(packetMap["summary"]),
		ReviewStatus:     workerEvidenceReviewStatus(packetMap),
		Risks:            compactStringSliceValue(packetMap["risks"]),
		Problems:         workerEvidenceValidationProblems(validationMap),
	}
}

func workerEvidenceReviewStatus(packet map[string]interface{}) string {
	review, _ := packet["review"].(map[string]interface{})
	return compactStringValue(review["status"])
}

func workerEvidenceValidationProblems(validation map[string]interface{}) []string {
	var problems []string
	for _, field := range compactStringSliceValue(validation["missing"]) {
		problems = append(problems, "missing "+field)
	}
	problems = append(problems, compactStringSliceValue(validation["invalid"])...)
	return problems
}

func compactBoolValue(value interface{}) bool {
	got, _ := value.(bool)
	return got
}

func compactStringValue(value interface{}) string {
	got, _ := value.(string)
	return strings.TrimSpace(got)
}

func compactStringSliceValue(value interface{}) []string {
	raw, ok := value.([]interface{})
	if !ok {
		if stringsValue, ok := value.([]string); ok {
			return nonEmptyStringCopy(stringsValue)
		}
		return nil
	}
	out := make([]string, 0, len(raw))
	for _, item := range raw {
		if s := compactStringValue(item); s != "" {
			out = append(out, s)
		}
	}
	return out
}

func nonEmptyStringCopy(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

func formatCompactActivity(counts map[string]int) string {
	activities := sortedKeys(counts)
	parts := make([]string, 0, len(activities))
	for _, activity := range activities {
		parts = append(parts, fmt.Sprintf("%s=%d", activity, counts[activity]))
	}
	return strings.Join(parts, " ")
}

func emitOrchestrateWatchFrame(frame orchestrateWatchFrame, jsonl bool, compact bool) error {
	if compact {
		return emitCompactOrchestrateFrame(compactFrameFromWatchFrame(frame), jsonl)
	}
	if jsonl {
		encoded, err := json.Marshal(frame)
		if err != nil {
			return err
		}
		fmt.Println(string(encoded))
		return nil
	}
	fmt.Printf("root=%s since=%d next=%d\n", frame.RootIssueID, frame.SinceSeq, frame.NextSince)
	fmt.Printf("persistence_guard: %s\n", frame.PersistenceGuard)
	fmt.Printf("capacity: direct_runnable=%d direct_active=%d pending_starts=%d nested_startable=%d nested_active=%d blocked_nested_roots=%d not_counting=%d total_counting=%d\n",
		frame.Capacity.DirectRunnableCount,
		frame.Capacity.DirectActiveCount,
		frame.Capacity.PendingStartsCount,
		frame.Capacity.NestedStartableCount,
		frame.Capacity.NestedActiveCount,
		frame.Capacity.BlockedNestedRootsCount,
		frame.Capacity.NotCountingCapacityCount,
		frame.Capacity.TotalCountingCapacityCount,
	)
	fmt.Println("runnable:")
	if len(frame.Runnable) == 0 {
		fmt.Println("- (none)")
	} else {
		for _, id := range frame.Runnable {
			fmt.Printf("- %s\n", id)
		}
	}
	if len(frame.Blocked) > 0 {
		fmt.Println("blocked:")
		for _, id := range sortedKeys(frame.Blocked) {
			fmt.Printf("- %s: %s\n", id, frame.Blocked[id])
		}
	}
	if len(frame.NestedRoots) > 0 {
		fmt.Println("nested roots:")
		for _, nested := range frame.NestedRoots {
			fmt.Printf("- %s status=%s issue_status=%s type=%s children=%d", nested.IssueID, nested.Status, nested.IssueStatus, nested.Type, nested.ChildCount)
			if nested.Classification != "" {
				fmt.Printf(" classification=%s", nested.Classification)
			}
			if len(nested.ExclusionReasons) > 0 {
				fmt.Printf(" exclusions=%s", strings.Join(nested.ExclusionReasons, ","))
			}
			if nested.FallbackPolicy != "" {
				fmt.Printf(" fallback=%s", nested.FallbackPolicy)
			}
			fmt.Println()
			if nested.StartFailure != nil {
				fmt.Printf("  start failure: operation=%s state=%s\n", nested.StartFailure.OperationID, nested.StartFailure.OperationState)
			}
			if nested.Advice != "" {
				fmt.Printf("  next: %s\n", nested.Advice)
			}
		}
	}
	if len(frame.Pending) > 0 {
		fmt.Println("pending:")
		for _, pending := range frame.Pending {
			fmt.Printf("- %s operation=%s state=%s\n", pending.IssueID, pending.OperationID, pending.OperationState)
		}
	}
	if len(frame.ActiveSessions) > 0 {
		fmt.Println("active:")
		for _, active := range frame.ActiveSessions {
			status := strings.TrimSpace(active.Status)
			if status == "" {
				status = "active"
			}
			fmt.Printf("- %s status=%s activity=%s source=%s\n", active.IssueID, status, active.Activity, active.ActivitySource)
			if active.StartProgress != nil {
				fmt.Printf("  start: %s\n", formatSessionStartProgress(*active.StartProgress))
			}
			if active.Advice != "" {
				fmt.Printf("  %s\n", active.Advice)
			}
		}
	}
	if len(frame.SessionStartProgress) > 0 {
		fmt.Println("session start progress:")
		for _, progress := range frame.SessionStartProgress {
			fmt.Printf("- %s: %s\n", progress.IssueID, formatSessionStartProgress(progress))
		}
	}
	fmt.Println("events:")
	if len(frame.Events) == 0 {
		fmt.Println("- (none)")
	} else {
		for _, evt := range frame.Events {
			fmt.Printf("- seq=%d issue=%s type=%s\n", evt.Seq, evt.IssueID, evt.Type)
		}
	}
	return nil
}

func emitCompactOrchestrateFrame(frame orchestrateCompactFrame, jsonl bool) error {
	if jsonl {
		encoded, err := json.Marshal(frame)
		if err != nil {
			return err
		}
		fmt.Println(string(encoded))
		return nil
	}
	printCompactOrchestrateFrame(frame)
	return nil
}

func printCompactOrchestrateFrame(frame orchestrateCompactFrame) {
	fmt.Printf("root=%s since=%d next=%d events=%d runnable=%d active=%d blocked=%d pending=%d\n",
		frame.RootIssueID,
		frame.SinceSeq,
		frame.NextSince,
		frame.EventCount,
		frame.Capacity.Runnable,
		frame.Capacity.Active,
		frame.Capacity.Blocked,
		frame.Capacity.Pending,
	)
	if len(frame.Capacity.Activity) > 0 {
		fmt.Printf("activity: %s\n", formatCompactActivity(frame.Capacity.Activity))
	}
	if len(frame.Readiness.Runnable) > 0 {
		fmt.Printf("runnable: %s\n", strings.Join(frame.Readiness.Runnable, ", "))
	}
	if len(frame.Readiness.Blocked) > 0 {
		fmt.Println("blocked:")
		for _, id := range sortedKeys(frame.Readiness.Blocked) {
			fmt.Printf("- %s: %s\n", id, frame.Readiness.Blocked[id])
		}
	}
	if len(frame.Readiness.Active) > 0 {
		fmt.Println("active:")
		for _, active := range frame.Readiness.Active {
			status := strings.TrimSpace(active.Status)
			if status == "" {
				status = "active"
			}
			fmt.Printf("- %s status=%s activity=%s\n", active.IssueID, status, active.Activity)
		}
	}
	if len(frame.Readiness.Pending) > 0 {
		fmt.Println("pending:")
		for _, pending := range frame.Readiness.Pending {
			fmt.Printf("- %s state=%s operation=%s\n", pending.IssueID, pending.OperationState, pending.OperationID)
		}
	}
	if len(frame.Readiness.NestedRoots) > 0 {
		fmt.Println("nested roots:")
		for _, nested := range frame.Readiness.NestedRoots {
			if nested.Activity != "" {
				fmt.Printf("- %s status=%s type=%s children=%d activity=%s\n", nested.IssueID, nested.Status, nested.Type, nested.Children, nested.Activity)
			} else {
				fmt.Printf("- %s status=%s type=%s children=%d\n", nested.IssueID, nested.Status, nested.Type, nested.Children)
			}
		}
	}
	if len(frame.Readiness.SessionStartProgress) > 0 {
		fmt.Println("session start progress:")
		for _, progress := range frame.Readiness.SessionStartProgress {
			fmt.Printf("- %s state=%s phase=%s progress=%d%% %s\n", progress.IssueID, progress.OperationState, progress.Phase, progress.Percent, progress.Message)
		}
	}
	if len(frame.Events) > 0 {
		fmt.Println("events:")
		for _, evt := range frame.Events {
			if evt.WorkerEvidence != nil {
				evidence := evt.WorkerEvidence
				extra := strings.TrimSpace(evidence.Summary)
				if len(evidence.Risks) > 0 {
					extra = strings.TrimSpace(extra + " risks=" + strings.Join(evidence.Risks, ", "))
				}
				if len(evidence.Problems) > 0 {
					extra = strings.TrimSpace(extra + " problems=" + strings.Join(evidence.Problems, "; "))
				}
				if extra != "" {
					fmt.Printf("- seq=%d issue=%s type=%s evidence=%s review=%s %s\n", evt.Seq, evt.IssueID, evt.Type, evidence.ValidationStatus, evidence.ReviewStatus, extra)
				} else {
					fmt.Printf("- seq=%d issue=%s type=%s evidence=%s review=%s\n", evt.Seq, evt.IssueID, evt.Type, evidence.ValidationStatus, evidence.ReviewStatus)
				}
				continue
			}
			fmt.Printf("- seq=%d issue=%s type=%s\n", evt.Seq, evt.IssueID, evt.Type)
		}
	}
	if len(frame.Warnings) > 0 {
		fmt.Println("warnings:")
		for _, warning := range frame.Warnings {
			fmt.Printf("- %s\n", warning)
		}
	}
	if frame.Advice != nil {
		if watch, ok := frame.Advice["watch"].(string); ok && watch != "" {
			fmt.Printf("watch: %s\n", watch)
		}
	}
}

func buildOrchestratePromptResult(rootIssueID, parentIssueID string, task domain.Task, coordination string) orchestratePromptResult {
	issueID := task.ID.String()
	commands := []string{
		"az prime",
		fmt.Sprintf("az issue get %s", issueID),
		fmt.Sprintf("az spec read --issue %s", issueID),
		fmt.Sprintf("az issue record %s --summary \"<progress>\"", issueID),
		fmt.Sprintf("az issue update %s --status in_progress", issueID),
	}
	if coordination == "mailbox" {
		commands = append(commands,
			fmt.Sprintf("az mail list --parent %s --since 0 --json", parentIssueID),
			fmt.Sprintf("az mail send --parent %s --issue %s --type worker-progress --body \"<progress>\"", parentIssueID, issueID),
			"az evidence validate --template",
			`az evidence validate --body '{"schema":"worker_evidence.v1","summary":"Ready for integration.","commands_run":["<project validation command>"],"key_assertions":["validation passed"],"files_changed":["path/to/changed-file"],"review":{"status":"clean","findings":[]},"risks":["none"]}'`,
			fmt.Sprintf("az issue record %s --type evidence.submitted --data '{\"schema\":\"worker_evidence.v1\",\"summary\":\"Ready for integration.\",\"commands_run\":[\"<project validation command>\"],\"key_assertions\":[\"validation passed\"],\"files_changed\":[\"path/to/changed-file\"],\"review\":{\"status\":\"clean\",\"findings\":[]},\"risks\":[\"none\"]}'", issueID),
			fmt.Sprintf("az mail send --parent %s --issue %s --type worker-integration-ready --body '{\"schema\":\"worker_evidence.v1\",\"summary\":\"Ready for integration.\",\"commands_run\":[\"<project validation command>\"],\"key_assertions\":[\"validation passed\"],\"files_changed\":[\"path/to/changed-file\"],\"review\":{\"status\":\"clean\",\"findings\":[]},\"risks\":[\"none\"]}'", parentIssueID, issueID),
		)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Work on issue %s: %s\n\n", issueID, task.Title)
	fmt.Fprintf(&b, "Start by running `az prime`, then continue this worker task using the issue context without waiting for further instruction.\n\n")
	fmt.Fprintf(&b, "Root issue: %s\n", rootIssueID)
	fmt.Fprintf(&b, "Coordination mode: %s\n", coordination)
	if coordination == "mailbox" {
		fmt.Fprintf(&b, "Coordination mailbox parent: %s\n", parentIssueID)
	}
	fmt.Fprintf(&b, "Worker issue: %s\n", issueID)
	fmt.Fprintf(&b, "Status: %s\n", task.Status)
	fmt.Fprintf(&b, "Priority: %s\n", task.Priority)
	fmt.Fprintf(&b, "Type: %s\n", task.Type)
	if len(task.Implementations) > 0 {
		fmt.Fprintf(&b, "Implementations: %s\n", strings.Join(task.Implementations, ", "))
	}
	if strings.TrimSpace(task.Description) != "" {
		fmt.Fprintf(&b, "\nDescription:\n%s\n", strings.TrimSpace(task.Description))
	}
	if strings.TrimSpace(task.Design) != "" {
		fmt.Fprintf(&b, "\nDesign:\n%s\n", strings.TrimSpace(task.Design))
	}
	if strings.TrimSpace(task.Acceptance) != "" {
		fmt.Fprintf(&b, "\nAcceptance:\n%s\n", strings.TrimSpace(task.Acceptance))
	}
	if strings.TrimSpace(task.Notes) != "" {
		fmt.Fprintf(&b, "\nCurrent notes: present but omitted from worker prompt. Run `az issue get %s --with-notes` only if full note history is necessary.\n", issueID)
	}
	fmt.Fprintf(&b, "\nRole: worker\n")
	fmt.Fprintf(&b, "- Focus only on issue %s unless the orchestrator explicitly expands scope.\n", issueID)
	fmt.Fprintf(&b, "- Before behavior changes, inspect linked requirements with `az spec read --issue %s`; if none are linked, find nearby requirements with `az spec req list --query \"<issue title and feature terms>\" --match any --limit 10`/`az spec read --req <req-id>` or create/update one before implementation. For contract-preserving refactors, tests, tooling, docs, internal cleanup, or fixes that restore existing behavior, record explicit `Spec impact: none (...)` evidence instead of creating implementation-only requirements.\n", issueID)
	fmt.Fprintf(&b, "- Keep `%s` status current; record progress, follow-ups, validation, review facts, risks, blockers, and closeout evidence with `az issue record` instead of routine notes.\n", issueID)
	fmt.Fprintf(&b, "- Status semantics: use `in_progress` while actively working and `in_review` when your implementation is complete and ready for orchestrator review/integration. Review handoff is non-terminal: preserve the worker session/worktree; the orchestrator closes accepted work.\n")
	fmt.Fprintf(&b, "- Blocked work is represented by dependency edges, issue record evidence, or active-coordination `worker-blocked` mailbox events, not by setting issue status to `in_review`.\n")
	fmt.Fprintf(&b, "- Do not append raw logs, exploratory transcripts, routine progress narration, duplicate prompt context, or speculative scratch work to notes.\n")
	if coordination == "mailbox" {
		fmt.Fprintf(&b, "- Use mailbox events for active hybrid coordination only: `worker-progress`, `worker-blocked`, and `worker-integration-ready`; `worker-ready` and `worker-complete` are accepted only as legacy aliases for `worker-integration-ready`. For non-orchestrated durable facts, use `az issue record`.\n")
		fmt.Fprintf(&b, "- Check inbound orchestrator messages with `az mail list --parent %s --since 0 --json` before declaring yourself blocked or idle; apply events for `%s` and continue without waiting for a separate user prompt.\n", parentIssueID, issueID)
		fmt.Fprintf(&b, "- Report to parent `%s` with `az mail send --parent %s --issue %s --type <worker-progress|worker-blocked|worker-integration-ready> --body \"<evidence>\"`; do not use `az orchestrate message` for your own status because it is an orchestrator-to-worker live delivery command.\n", parentIssueID, parentIssueID, issueID)
		fmt.Fprintf(&b, "- Evidence bodies should be JSON `worker_evidence.v1` packets with `summary`, `commands_run`, `key_assertions`, `files_changed`, `review.status`, `review.findings`, and `risks`; use `\"none\"` entries when a required list has no findings or risks. Omit `artifact_links` unless links are needed; when present, encode it as objects like `[{\"label\":\"CI\",\"url\":\"https://example.test/run\"}]`, not a string array. Preflight with `az evidence validate --body '<json>'`; use `az issue record --type evidence.submitted --data '<json>'` when mailbox delivery is irrelevant.\n")
		fmt.Fprintf(&b, "- Before handing off, run the relevant validation/review checks, build the final `worker_evidence.v1` packet from actual results, run `az evidence validate --body '<json>'`, record or send that exact JSON packet, then set/leave `%s` `in_review`. Preserve the worker session/worktree for feedback; do not stop or close them as part of review handoff. Do not rely on a prose-only final response as the handoff.\n", issueID)
		fmt.Fprintf(&b, "- When ready for integration, provide the validated `worker_evidence.v1` packet. Send `worker-integration-ready` to parent `%s` only when this active mailbox coordination path is being watched; otherwise record it with `az issue record %s --type evidence.submitted --data '<json>'`. Leave integration/merge/close to the orchestrator.\n", parentIssueID, issueID)
	} else {
		fmt.Fprintf(&b, "- Return progress, blockers, and final results through the native subagent result channel.\n")
		fmt.Fprintf(&b, "- Do not use `az mail` unless the orchestrator explicitly asks for mailbox coordination; use `az issue record` for durable issue activity/evidence.\n")
		fmt.Fprintf(&b, "- Before handing off, run the relevant validation/review checks and include the same facts expected in `worker_evidence.v1`: summary, commands run, key assertions, files changed, review status/findings, and risks. Preserve any az-managed worker session/worktree for feedback; do not stop or close them as part of review handoff. Do not rely on a prose-only status update.\n")
	}
	fmt.Fprintf(&b, "- Do not close root issue `%s` or your worker issue as part of handoff; leave integration and terminal close to the orchestrator unless the human explicitly instructs otherwise.\n", rootIssueID)
	fmt.Fprintf(&b, "\nUseful commands:\n")
	for _, command := range commands {
		fmt.Fprintf(&b, "- `%s`\n", command)
	}
	return orchestratePromptResult{
		RootIssueID:  rootIssueID,
		IssueID:      issueID,
		ParentIssue:  parentIssueID,
		Coordination: coordination,
		Prompt:       b.String(),
		Commands:     commands,
	}
}

func orchestrateStartWarnings(ctx context.Context, deps *Dependencies, ready daemonclient.TaskGraphReadiness, launchCount int) []string {
	if deps == nil || deps.DaemonClient == nil {
		return nil
	}
	warnings := orchestrateRootWorktreeWarningsFromReadiness(ctx, deps, ready)
	warnings = append(warnings, sessionInitCommandFanoutWarnings(deps, ready.RootIssueID, launchCount)...)
	if launchCount < 1 {
		return warnings
	}
	cwd, err := os.Getwd()
	if err != nil {
		return append(warnings, fmt.Sprintf("could not inspect parent worktree dirtiness: %v", err))
	}
	status, err := deps.DaemonClient.GitStatus(ctx, cwd)
	if err != nil {
		return append(warnings, fmt.Sprintf("could not inspect parent worktree dirtiness: %v", err))
	}
	dirty := dirtyFilesFromGitStatus(status)
	if len(dirty) == 0 {
		return warnings
	}
	return append(warnings,
		fmt.Sprintf("parent worktree has uncommitted tracked changes (%s); worker worktrees are created from committed branch state and will not see these files: %s", summarizeGitStatusCounts(status), strings.Join(dirty, ", ")),
	)
}

func orchestrateStatusWarnings(ctx context.Context, deps *Dependencies, ready daemonclient.TaskGraphReadiness, runnableCount int) []string {
	if deps == nil || deps.DaemonClient == nil {
		return nil
	}
	warnings := orchestrateRootWorktreeWarningsFromReadiness(ctx, deps, ready)
	warnings = append(warnings, sessionInitCommandFanoutWarnings(deps, ready.RootIssueID, runnableCount)...)
	return warnings
}

func plannedOrchestrateStartLaunchCount(requestedCount, limit int) int {
	if requestedCount < 1 || limit < 1 {
		return 0
	}
	if requestedCount < limit {
		return requestedCount
	}
	return limit
}

func sessionInitCommandFanoutWarnings(deps *Dependencies, rootIssueID string, fanoutCount int) []string {
	if fanoutCount < 2 || deps == nil || deps.Config == nil {
		return nil
	}
	commands := expensiveSessionSyncInitCommands(deps.Config.Session.SyncInitCommands)
	if len(commands) == 0 {
		return nil
	}
	warnings := make([]string, 0, len(commands))
	rootIssueID = strings.TrimSpace(rootIssueID)
	if rootIssueID == "" {
		rootIssueID = "this root"
	}
	for _, command := range commands {
		warnings = append(warnings, fmt.Sprintf("session.syncInitCommands contains expensive command %q; fanout count %d for same-project sessions under root %s can run it %d times concurrently. Consider lowering --limit, moving the command to explicit verification, or running one parent preflight before fanout.", command, fanoutCount, rootIssueID, fanoutCount))
	}
	return warnings
}

func expensiveSessionSyncInitCommands(commands []string) []string {
	out := make([]string, 0, len(commands))
	seen := map[string]struct{}{}
	for _, command := range commands {
		command = strings.TrimSpace(command)
		if command == "" || !isExpensiveSessionInitCommand(command) {
			continue
		}
		if _, ok := seen[command]; ok {
			continue
		}
		seen[command] = struct{}{}
		out = append(out, command)
	}
	return out
}

func isExpensiveSessionInitCommand(command string) bool {
	tokens := sessionInitCommandTokens(command)
	for _, token := range tokens {
		switch token {
		case "tsc", "tsgo", "nx", "test", "install", "build":
			return true
		}
		if strings.Contains(token, "type-check") || strings.Contains(token, "typecheck") || strings.Contains(token, "check:types") || strings.Contains(token, "types:check") {
			return true
		}
	}
	return false
}

func sessionInitCommandTokens(command string) []string {
	command = strings.ToLower(command)
	replacer := strings.NewReplacer(
		"&&", " ",
		"||", " ",
		";", " ",
		"(", " ",
		")", " ",
		"\"", " ",
		"'", " ",
		"`", " ",
	)
	fields := strings.Fields(replacer.Replace(command))
	tokens := make([]string, 0, len(fields))
	for _, field := range fields {
		field = strings.Trim(field, " \t\r\n,")
		if field == "" || strings.Contains(field, "=") {
			continue
		}
		for _, part := range strings.Split(field, "/") {
			part = strings.Trim(part, " \t\r\n,.")
			if part != "" {
				tokens = append(tokens, part)
			}
		}
	}
	return tokens
}

func orchestrateRootWorktreeWarningsFromReadiness(ctx context.Context, deps *Dependencies, ready daemonclient.TaskGraphReadiness) []string {
	rootIssueID := strings.TrimSpace(ready.RootIssueID)
	if rootIssueID == "" || deps == nil || deps.DaemonClient == nil {
		return nil
	}
	if len(ready.Runnable)+len(ready.NestedRoots)+len(ready.Pending)+len(ready.Active)+len(ready.Blocked)+len(ready.ActiveSessions)+len(ready.SessionStartProgress) == 0 {
		return nil
	}
	worktrees, err := deps.DaemonClient.ListWorktrees(ctx)
	if err != nil {
		return []string{fmt.Sprintf("could not inspect root worktree ownership for %s: list worktrees: %v", rootIssueID, err)}
	}
	for _, wt := range worktrees {
		if naming.IssueIDsEqual(wt.IssueID, rootIssueID) && strings.TrimSpace(wt.Path) != "" {
			return nil
		}
	}
	currentPath := strings.TrimSpace(deps.RuntimeRepoDir)
	if currentPath == "" {
		currentPath = strings.TrimSpace(deps.RepoDir)
	}
	if currentPath == "" {
		cwd, err := os.Getwd()
		if err != nil {
			cwd = ""
		}
		currentPath = strings.TrimSpace(cwd)
	}
	return []string{
		fmt.Sprintf("root issue %s has child orchestration but no dedicated worktree; current worktree/path is %s. Recommended: az worktree create %s, then run orchestration from the issue worktree.", rootIssueID, currentPath, rootIssueID),
	}
}

func worktreeForIssue(ctx context.Context, deps *Dependencies, issueID string) (daemonclient.Worktree, bool, error) {
	worktrees, err := deps.DaemonClient.ListWorktrees(ctx)
	if err != nil {
		return daemonclient.Worktree{}, false, fmt.Errorf("list worktrees: %w", err)
	}
	for _, wt := range worktrees {
		if naming.IssueIDsEqual(wt.IssueID, issueID) {
			return wt, true, nil
		}
	}
	return daemonclient.Worktree{}, false, nil
}

func orchestrateIntegrationCommands(issueID, projectID string, wt daemonclient.Worktree, found, mergeReady bool) []string {
	commands := make([]string, 0, 5)
	if found && strings.TrimSpace(wt.Path) != "" {
		commands = append(commands,
			fmt.Sprintf("git -C %s status --short", shellSingleQuote(wt.Path)),
			fmt.Sprintf("git -C %s log --oneline --max-count=10", shellSingleQuote(wt.Path)),
		)
	} else {
		commands = append(commands, issueGetCommandForProject(issueID, projectID))
		if strings.TrimSpace(projectID) == "" {
			commands = append(commands, fmt.Sprintf("az session status %s", issueID))
		}
	}
	if mergeReady {
		commands = append(commands, issueCloseCommandForProject(issueID, projectID))
		commands = append(commands, fmt.Sprintf("repair merge only if close reports conflicts: %s", branchMergeCommandForProject(issueID, projectID)))
	}
	return commands
}

func sendOrchestrateMailEvent(deps *Dependencies, parentIssueID, issueID, eventType, body string) error {
	parsedIssueID, err := naming.ParseIssueID(issueID)
	if err != nil {
		return fmt.Errorf("invalid issue id %q: %w", issueID, err)
	}
	_, err = deps.DaemonClient.MailSend(context.Background(), protocol.MailSendCommandBody{
		RepoDir:     deps.RepoDir,
		ParentIssue: parentIssueID,
		IssueID:     parsedIssueID,
		Type:        eventType,
		From:        "orchestrator",
		Body:        body,
	})
	return err
}

func nextMailboxSeq(events []protocol.MailEvent, since int64) int64 {
	maxSeq := since
	for _, evt := range events {
		if evt.Seq > maxSeq {
			maxSeq = evt.Seq
		}
	}
	return maxSeq
}

func dedupeSortedStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			continue
		}
		if _, ok := seen[trimmed]; ok {
			continue
		}
		seen[trimmed] = struct{}{}
		out = append(out, trimmed)
	}
	sort.Strings(out)
	return out
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
