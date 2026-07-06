package cli

import (
	"context"
	"encoding/json"
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
}

type OrchestrateStartOptions struct {
	Project            string
	RootIssueID        string
	Limit              int
	IssueIDs           []string
	JSON               bool
	BaseBranchOverride string
}

type OrchestrateWatchOptions struct {
	Project      string
	RootIssueID  string
	SinceSeq     int64
	JSONL        bool
	Once         bool
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
	return fmt.Sprintf("az issue close --id %s", issueID)
}

var orchestrateObserveNow = func() time.Time {
	return time.Now().UTC()
}

type orchestrateStatusResult struct {
	RootIssueID            string                               `json:"root_issue_id"`
	Runnable               []string                             `json:"runnable"`
	Pending                []orchestratePendingStart            `json:"pending,omitempty"`
	Active                 []string                             `json:"active,omitempty"`
	ActiveSessions         []orchestrateActiveSession           `json:"active_sessions,omitempty"`
	SessionStartProgress   []orchestrateSessionStartProgress    `json:"session_start_progress,omitempty"`
	StaleCloseableChildren []orchestrateStaleCloseableCandidate `json:"stale_closeable_children,omitempty"`
	WorkerObservations     []orchestrateObservation             `json:"worker_observations,omitempty"`
	Blocked                map[string]string                    `json:"blocked"`
	MailboxEvents          []protocol.MailEvent                 `json:"mailbox_events"`
	Warnings               []string                             `json:"warnings,omitempty"`
	Advice                 map[string]interface{}               `json:"advice,omitempty"`
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
	Limit       int                       `json:"limit"`
	Requested   []string                  `json:"requested"`
	Started     []string                  `json:"started"`
	Launched    []orchestrateStartLaunch  `json:"launched,omitempty"`
	Pending     []orchestrateStartPending `json:"pending,omitempty"`
	Skipped     map[string]string         `json:"skipped"`
	Failed      map[string]string         `json:"failed"`
	Warnings    []string                  `json:"warnings,omitempty"`
	Advice      orchestrateStartAdvice    `json:"advice,omitempty"`
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

type orchestrateWatchFrame struct {
	RootIssueID            string                               `json:"root_issue_id"`
	SinceSeq               int64                                `json:"since_seq"`
	NextSince              int64                                `json:"next_since"`
	Runnable               []string                             `json:"runnable"`
	Pending                []orchestratePendingStart            `json:"pending,omitempty"`
	Active                 []string                             `json:"active,omitempty"`
	ActiveSessions         []orchestrateActiveSession           `json:"active_sessions,omitempty"`
	SessionStartProgress   []orchestrateSessionStartProgress    `json:"session_start_progress,omitempty"`
	StaleCloseableChildren []orchestrateStaleCloseableCandidate `json:"stale_closeable_children,omitempty"`
	Blocked                map[string]string                    `json:"blocked"`
	Events                 []mailEvent                          `json:"events"`
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
	if err := fs.Parse(args); err != nil {
		return OrchestrateStatusOptions{}, err
	}
	if fs.NArg() != 0 {
		return OrchestrateStatusOptions{}, fmt.Errorf("unexpected argument: %s", fs.Arg(0))
	}
	if strings.TrimSpace(opts.RootIssueID) == "" {
		return OrchestrateStatusOptions{}, fmt.Errorf("missing required flag: --root")
	}
	if opts.Limit < 1 {
		return OrchestrateStatusOptions{}, fmt.Errorf("limit must be >= 1")
	}
	opts.Project = normalizeIssueProject(opts.Project)
	return opts, nil
}

func ParseOrchestrateStartArgs(args []string) (OrchestrateStartOptions, error) {
	opts := OrchestrateStartOptions{Limit: 4}
	fs := flag.NewFlagSet("orchestrate start", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	addIssueProjectFlag(fs, &opts.Project)
	fs.StringVar(&opts.RootIssueID, "root", "", "root issue id")
	fs.IntVar(&opts.Limit, "limit", 4, "maximum runnable issues to start")
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
	if strings.TrimSpace(opts.RootIssueID) == "" {
		return OrchestrateStartOptions{}, fmt.Errorf("missing required flag: --root")
	}
	if opts.Limit < 1 {
		return OrchestrateStartOptions{}, fmt.Errorf("limit must be >= 1")
	}
	opts.Project = normalizeIssueProject(opts.Project)
	opts.IssueIDs = dedupeSortedStrings(opts.IssueIDs)
	return opts, nil
}

func ParseOrchestrateWatchArgs(args []string) (OrchestrateWatchOptions, error) {
	opts := OrchestrateWatchOptions{JSONL: true, PollInterval: 250 * time.Millisecond}
	fs := flag.NewFlagSet("orchestrate watch", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	addIssueProjectFlag(fs, &opts.Project)
	fs.StringVar(&opts.RootIssueID, "root", "", "root issue id")
	fs.Int64Var(&opts.SinceSeq, "since", 0, "mailbox sequence lower bound")
	fs.BoolVar(&opts.JSONL, "jsonl", true, "emit JSON lines")
	fs.BoolVar(&opts.Once, "once", false, "print one frame then exit")
	if err := fs.Parse(args); err != nil {
		return OrchestrateWatchOptions{}, err
	}
	if fs.NArg() != 0 {
		return OrchestrateWatchOptions{}, fmt.Errorf("unexpected argument: %s", fs.Arg(0))
	}
	if strings.TrimSpace(opts.RootIssueID) == "" {
		return OrchestrateWatchOptions{}, fmt.Errorf("missing required flag: --root")
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
	if strings.TrimSpace(opts.RootIssueID) == "" {
		return OrchestrateCompleteCheckOptions{}, fmt.Errorf("missing required flag: --root")
	}
	opts.Project = normalizeIssueProject(opts.Project)
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
	restoreProject := applyIssueProjectOverride(deps, opts.Project)
	defer restoreProject()

	ctx, cancel := context.WithTimeout(context.Background(), daemonCommandTimeout)
	defer cancel()
	if err := ensureDaemon(ctx, deps, "cli"); err != nil {
		return err
	}

	ready, err := deps.DaemonClient.TaskGraphReadiness(ctx, opts.RootIssueID)
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

	result := orchestrateStatusResult{
		RootIssueID:            ready.RootIssueID,
		Runnable:               ready.Runnable,
		Pending:                orchestratePendingStartsFromDaemon(ready.Pending),
		Active:                 ready.Active,
		ActiveSessions:         orchestrateActiveSessionsFromDaemon(ready.ActiveSessions),
		SessionStartProgress:   orchestrateSessionStartProgressFromDaemon(ready.SessionStartProgress),
		StaleCloseableChildren: orchestrateStaleCloseableFromDaemon(ready.StaleCloseableChildren),
		WorkerObservations:     orchestrateObservationsFromDaemon(ready.WorkerObservations, orchestrateObserveNow()),
		Blocked:                ready.Blocked,
		MailboxEvents:          events,
		Warnings:               orchestrateStatusWarnings(ctx, deps, ready, len(ready.Runnable)),
		Advice: map[string]interface{}{
			"watch":             fmt.Sprintf("az orchestrate watch --root %s --since %d --jsonl", ready.RootIssueID, nextMailboxSeq(events, opts.SinceSeq)),
			"watch_instruction": "Start this watch command in another pane/session and leave it running while workers are active; use active_sessions activity before considering pane capture. Do not add --once for orchestration monitoring.",
		},
	}
	if opts.JSON {
		return printJSON(result)
	}

	fmt.Printf("Root issue: %s\n", result.RootIssueID)
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
	if len(result.Blocked) > 0 {
		fmt.Println("Blocked leaves:")
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
	restoreProject := applyIssueProjectOverride(deps, opts.Project)
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
	restoreProject := applyIssueProjectOverride(deps, opts.Project)
	defer restoreProject()

	var (
		result orchestrateObserveResult
		err    error
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
	restoreProject := applyIssueProjectOverride(deps, opts.Project)
	defer restoreProject()

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

func orchestrateStartResultError(result orchestrateStartResult) error {
	if len(result.Failed) > 0 {
		return fmt.Errorf("orchestrate start completed with failures")
	}
	return nil
}

func orchestrateStart(deps *Dependencies, opts OrchestrateStartOptions) (orchestrateStartResult, error) {
	ctx, cancel := context.WithTimeout(context.Background(), daemonCommandTimeout)
	defer cancel()
	if err := ensureDaemon(ctx, deps, "cli"); err != nil {
		return orchestrateStartResult{}, err
	}
	ready, err := deps.DaemonClient.TaskGraphReadiness(ctx, opts.RootIssueID)
	if err != nil {
		return orchestrateStartResult{}, err
	}

	runnableSet := make(map[string]struct{}, len(ready.Runnable))
	for _, id := range ready.Runnable {
		runnableSet[id] = struct{}{}
	}
	activeSet := make(map[string]struct{}, len(ready.Active))
	for _, id := range ready.Active {
		activeSet[id] = struct{}{}
	}

	requested := make([]string, 0, len(ready.Runnable))
	skipped := map[string]string{}
	if len(opts.IssueIDs) == 0 {
		requested = append(requested, ready.Runnable...)
	} else {
		for _, id := range opts.IssueIDs {
			if _, ok := runnableSet[id]; !ok {
				if _, active := activeSet[id]; active {
					skipped[id] = "session-already-running"
					continue
				}
				skipped[id] = "not-runnable"
				continue
			}
			requested = append(requested, id)
		}
	}

	result := orchestrateStartResult{
		RootIssueID: opts.RootIssueID,
		Limit:       opts.Limit,
		Requested:   append([]string(nil), requested...),
		Started:     make([]string, 0, len(requested)),
		Launched:    make([]orchestrateStartLaunch, 0, len(requested)),
		Skipped:     skipped,
		Failed:      map[string]string{},
		Warnings:    orchestrateStartWarnings(ctx, deps, ready, plannedOrchestrateStartLaunchCount(len(requested), opts.Limit)),
		Advice: orchestrateStartAdvice{
			WatchCommand:     fmt.Sprintf("az orchestrate watch --root %s --since 0 --jsonl", opts.RootIssueID),
			StatusCommand:    fmt.Sprintf("az orchestrate status --root %s --json", opts.RootIssueID),
			WatchInstruction: "Start this watch command in another pane/session and leave it running while workers are active; use active_sessions activity before considering pane capture. Do not add --once for orchestration monitoring.",
		},
	}

	count := 0
	pendingLaunches := make([]orchestrateStartLaunch, 0, len(requested))
	for _, issueID := range requested {
		if count >= opts.Limit {
			result.Skipped[issueID] = "limit-reached"
			continue
		}
		emitOrchestrateStartProgress(opts, "preparing", issueID)
		launch, err := submitSessionStartForIssueWithBaseBranch(deps, issueID, opts.BaseBranchOverride)
		if err != nil {
			result.Failed[issueID] = err.Error()
			continue
		}
		emitOrchestrateStartProgressWithLaunch(opts, "submitted", launch)
		pendingLaunches = append(pendingLaunches, launch)
		count++
	}

	for _, launch := range pendingLaunches {
		issueID := launch.IssueID
		launch.WatchCommand = result.Advice.WatchCommand
		launch.IntegrateHint = issueCloseCommand(issueID)
		launch.CloseHint = fmt.Sprintf("az orchestrate close-session --issue %s", issueID)
		result.Started = append(result.Started, issueID)
		result.Launched = append(result.Launched, launch)
		emitOrchestrateStartProgressWithLaunch(opts, "launched", launch)
	}

	return result, nil
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
	restoreProject := applyIssueProjectOverride(deps, opts.Project)
	defer restoreProject()

	ctx, cancel := context.WithTimeout(context.Background(), daemonCommandTimeout)
	defer cancel()
	if err := ensureDaemon(ctx, deps, "cli"); err != nil {
		return err
	}

	lastSeq := opts.SinceSeq
	frame, err := buildOrchestrateWatchFrame(deps, opts.RootIssueID, lastSeq)
	if err != nil {
		return err
	}
	lastSnapshotKey := orchestrateWatchFrameSnapshotKey(frame)
	readinessCache := newOrchestrateWatchReadinessCache(frame, time.Now(), opts.PollInterval)
	if deps.Logger != nil {
		deps.Logger.Debug("orchestrate watch readiness cache initialized",
			"root_issue_id", opts.RootIssueID,
			"poll_interval_ms", opts.PollInterval.Milliseconds(),
			"readiness_refresh_interval_ms", readinessCache.refreshInterval.Milliseconds(),
		)
	}
	if len(frame.Events) > 0 || len(frame.Pending) > 0 || len(frame.SessionStartProgress) > 0 || len(frame.ActiveSessions) > 0 || opts.Once {
		if err := emitOrchestrateWatchFrame(frame, opts.JSONL); err != nil {
			return err
		}
	}
	lastSeq = frame.NextSince
	if opts.Once {
		return nil
	}

	for {
		time.Sleep(opts.PollInterval)
		events, err := watchDaemonCommand(deps, func(ctx context.Context) ([]protocol.MailEvent, error) {
			return deps.DaemonClient.MailWatch(ctx, protocol.MailWatchCommandBody{
				RepoDir:     deps.RepoDir,
				ParentIssue: opts.RootIssueID,
				SinceSeq:    lastSeq + 1,
			})
		})
		if err != nil {
			if shouldContinueOrchestrateWatchAfterError(err) {
				continue
			}
			return err
		}
		watchEvents := make([]mailEvent, 0, len(events))
		for _, event := range events {
			watchEvents = append(watchEvents, protocolToLocalMailEvent(event))
		}
		nextSince := nextMailboxSeq(events, lastSeq)
		now := time.Now()
		refreshReadiness, refreshReason := readinessCache.shouldRefresh(now, len(events))
		if deps.Logger != nil {
			deps.Logger.Debug("orchestrate watch tick",
				"root_issue_id", opts.RootIssueID,
				"mailbox_event_count", len(events),
				"since_seq", lastSeq,
				"next_since", nextSince,
				"poll_interval_ms", opts.PollInterval.Milliseconds(),
				"readiness_cache", readinessCache.decision(refreshReadiness),
				"readiness_refresh_reason", refreshReason,
				"readiness_refresh_interval_ms", readinessCache.refreshInterval.Milliseconds(),
			)
		}
		var frame orchestrateWatchFrame
		if refreshReadiness {
			ready, err := watchDaemonCommand(deps, func(ctx context.Context) (daemonclient.TaskGraphReadiness, error) {
				return deps.DaemonClient.TaskGraphReadiness(ctx, opts.RootIssueID)
			})
			if err != nil {
				return err
			}
			frame = orchestrateWatchFrameFromReadiness(ready, watchEvents, lastSeq, nextSince)
			readinessCache.store(frame, now)
		} else {
			frame = readinessCache.cachedReadinessFrame(watchEvents, lastSeq, nextSince)
		}
		snapshotKey := orchestrateWatchFrameSnapshotKey(frame)
		if len(events) == 0 && snapshotKey == lastSnapshotKey {
			continue
		}
		if err := emitOrchestrateWatchFrame(frame, opts.JSONL); err != nil {
			return err
		}
		lastSnapshotKey = snapshotKey
		lastSeq = nextSince
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
		RootIssueID:            ready.RootIssueID,
		SinceSeq:               since,
		NextSince:              nextSince,
		Runnable:               ready.Runnable,
		Pending:                orchestratePendingStartsFromDaemon(ready.Pending),
		Active:                 ready.Active,
		ActiveSessions:         orchestrateActiveSessionsFromDaemon(ready.ActiveSessions),
		SessionStartProgress:   orchestrateSessionStartProgressFromDaemon(ready.SessionStartProgress),
		StaleCloseableChildren: orchestrateStaleCloseableFromDaemon(ready.StaleCloseableChildren),
		Blocked:                ready.Blocked,
		Events:                 events,
	}
}

func orchestrateWatchFrameSnapshotKey(frame orchestrateWatchFrame) string {
	type snapshot struct {
		Runnable               []string                             `json:"runnable"`
		Pending                []orchestratePendingStart            `json:"pending,omitempty"`
		Active                 []string                             `json:"active,omitempty"`
		ActiveSessions         []orchestrateActiveSession           `json:"active_sessions,omitempty"`
		SessionStartProgress   []orchestrateSessionStartProgress    `json:"session_start_progress,omitempty"`
		StaleCloseableChildren []orchestrateStaleCloseableCandidate `json:"stale_closeable_children,omitempty"`
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
	sessionStartProgress := append([]orchestrateSessionStartProgress(nil), frame.SessionStartProgress...)
	for i := range sessionStartProgress {
		sessionStartProgress[i].ElapsedMS = 0
	}
	encoded, err := json.Marshal(snapshot{
		Runnable:               frame.Runnable,
		Pending:                frame.Pending,
		Active:                 frame.Active,
		ActiveSessions:         activeSessions,
		SessionStartProgress:   sessionStartProgress,
		StaleCloseableChildren: frame.StaleCloseableChildren,
		Blocked:                frame.Blocked,
	})
	if err != nil {
		return ""
	}
	return string(encoded)
}

func watchDaemonCommand[T any](deps *Dependencies, call func(context.Context) (T, error)) (T, error) {
	value, err := call(context.Background())
	if err == nil || !reconnect.IsTransientTransportError(err) {
		return value, err
	}
	return commandWithDaemonAutostartRetry(context.Background(), deps, call)
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
	restoreProject := applyIssueProjectOverride(deps, opts.Project)
	defer restoreProject()

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
	restoreProject := applyIssueProjectOverride(deps, opts.Project)
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
	restoreProject := applyIssueProjectOverride(deps, opts.Project)
	defer restoreProject()

	ctx, cancel := context.WithTimeout(context.Background(), daemonCommandTimeout)
	defer cancel()
	if err := ensureDaemon(ctx, deps, "cli"); err != nil {
		return err
	}
	wt, found, err := worktreeForIssue(ctx, deps, opts.IssueID)
	if err != nil {
		return err
	}
	readiness, err := deps.DaemonClient.TaskIntegrationReadiness(ctx, opts.IssueID, deps.RepoDir)
	if err != nil {
		return err
	}
	mergeReady := readiness.Ready
	contextRiskBlocked := readiness.ContextRisk != nil && domain.IssueContextRiskRequiresStructuredCloseout(*readiness.ContextRisk)
	closeoutReady := mergeReady && !contextRiskBlocked
	commands := orchestrateIntegrationCommands(opts.IssueID, wt, found, closeoutReady)
	result := orchestrateIntegrateResult{
		IssueID:       opts.IssueID,
		MergeReady:    mergeReady,
		CloseoutReady: closeoutReady,
		ContextRisk:   readiness.ContextRisk,
		Apply:         opts.Apply,
		Reasons:       readiness.Reasons,
		Commands:      commands,
	}
	if contextRiskBlocked {
		result.Reasons = append(result.Reasons, issueContextRiskCloseoutBlockMessage(opts.IssueID))
	}
	if found {
		result.WorktreePath = wt.Path
		result.Branch = wt.Branch
	}
	if opts.Apply {
		applyErr := applyOrchestrateIntegration(deps, opts.IssueID, mergeReady, &result)
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

func applyOrchestrateIntegration(deps *Dependencies, issueID string, mergeReady bool, result *orchestrateIntegrateResult) error {
	if !mergeReady {
		result.Steps = append(result.Steps, orchestrateIntegrateStep{
			Name:   "completion_evidence",
			Status: "failed",
			Error:  "missing completion evidence",
		})
		result.Recovery = orchestrateIntegrationRecovery(issueID, "missing_completion_evidence")
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
		result.Steps = append(result.Steps, orchestrateIntegrateStep{Name: "integrate_and_close", Status: "failed", Error: err.Error()})
		result.Recovery = orchestrateIntegrationRecovery(issueID, "integration_failed")
		return fmt.Errorf("apply integration for %s: %w", issueID, err)
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

	note := fmt.Sprintf("Integrated by `az orchestrate integrate --issue %s --apply`: daemon task.close integrated the branch, stopped session/worktree runtime if present, and closed the issue.", issueID)
	if err := deps.DaemonClient.AppendTaskNotes(cleanupCtx, issueID, note); err != nil {
		result.Steps = append(result.Steps, orchestrateIntegrateStep{Name: "append_evidence", Status: "failed", Error: err.Error()})
		result.Recovery = orchestrateIntegrationRecovery(issueID, "post_close_failed")
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

func orchestrateIntegrationRecovery(issueID, reason string) []string {
	switch reason {
	case "missing_completion_evidence":
		return []string{
			fmt.Sprintf("review worker output and send a worker-integration-ready mailbox event for %s, or close if ready to integrate and clean up: %s", issueID, issueCloseCommand(issueID)),
			fmt.Sprintf("retry: az orchestrate integrate --issue %s --apply", issueID),
		}
	case "merge_failed", "integration_failed":
		return []string{
			fmt.Sprintf("inspect merge failure and retry existing merge path: az branch merge %s", issueID),
			fmt.Sprintf("after repair, retry daemon-owned integration/cleanup: az orchestrate integrate --issue %s --apply", issueID),
		}
	case "post_close_failed":
		return []string{
			fmt.Sprintf("integration/close already completed; inspect issue state: az issue get %s", issueID),
			fmt.Sprintf("append evidence notes to %s with the merge and validation summary", issueID),
		}
	default:
		return []string{
			fmt.Sprintf("run cleanup steps manually: az orchestrate close-session --issue %s", issueID),
			fmt.Sprintf("close the worker issue if ready to integrate and clean up: %s", issueCloseCommand(issueID)),
			fmt.Sprintf("append evidence notes to %s with the merge and validation summary", issueID),
		}
	}
}

func OrchestrateCloseSessionCommand(deps *Dependencies, opts OrchestrateCloseSessionOptions) error {
	restoreProject := applyIssueProjectOverride(deps, opts.Project)
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
	restoreProject := applyIssueProjectOverride(deps, opts.Project)
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
	ready, err := deps.DaemonClient.TaskGraphReadiness(context.Background(), rootIssueID)
	if err != nil {
		return orchestrateWatchFrame{}, err
	}
	events, err := deps.DaemonClient.MailList(context.Background(), protocol.MailListCommandBody{
		RepoDir:     deps.RepoDir,
		ParentIssue: rootIssueID,
		SinceSeq:    since,
		Limit:       200,
	})
	if err != nil {
		return orchestrateWatchFrame{}, err
	}
	watchEvents := make([]mailEvent, 0, len(events))
	for _, event := range events {
		watchEvents = append(watchEvents, protocolToLocalMailEvent(event))
	}
	return orchestrateWatchFrameFromReadiness(ready, watchEvents, since, nextMailboxSeq(events, since)), nil
}

func emitOrchestrateWatchFrame(frame orchestrateWatchFrame, jsonl bool) error {
	if jsonl {
		encoded, err := json.Marshal(frame)
		if err != nil {
			return err
		}
		fmt.Println(string(encoded))
		return nil
	}
	fmt.Printf("root=%s since=%d next=%d\n", frame.RootIssueID, frame.SinceSeq, frame.NextSince)
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

func buildOrchestratePromptResult(rootIssueID, parentIssueID string, task domain.Task, coordination string) orchestratePromptResult {
	issueID := task.ID.String()
	commands := []string{
		"az prime",
		fmt.Sprintf("az issue get %s", issueID),
		fmt.Sprintf("az spec read --issue %s", issueID),
		fmt.Sprintf("az issue update %s --status in_progress", issueID),
	}
	if coordination == "mailbox" {
		commands = append(commands,
			fmt.Sprintf("az mail list --parent %s --since 0 --json", parentIssueID),
			fmt.Sprintf("az mail send --parent %s --issue %s --type worker-progress --body \"<progress>\"", parentIssueID, issueID),
			fmt.Sprintf("az mail send --parent %s --issue %s --type worker-integration-ready --body '{\"schema\":\"worker_evidence.v1\",...}'", parentIssueID, issueID),
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
	fmt.Fprintf(&b, "- Keep `%s` status and notes current with terse evidence: final commands run, key outputs/assertions, files changed, AC pass/fail, blockers, and remaining scope only.\n", issueID)
	fmt.Fprintf(&b, "- Status semantics: use `in_progress` while actively working, `in_review` when your implementation is complete and ready for orchestrator review/integration, and `closed` only after the orchestrator has integrated/accepted the work.\n")
	fmt.Fprintf(&b, "- Blocked work is represented by dependency edges or `worker-blocked` mailbox events, not by setting issue status to `in_review`.\n")
	fmt.Fprintf(&b, "- Do not append raw logs, exploratory transcripts, routine progress narration, duplicate prompt context, or speculative scratch work to notes.\n")
	if coordination == "mailbox" {
		fmt.Fprintf(&b, "- Use mailbox events for hybrid coordination: `worker-progress`, `worker-blocked`, and `worker-integration-ready`; `worker-ready` and `worker-complete` are accepted only as legacy aliases for `worker-integration-ready`.\n")
		fmt.Fprintf(&b, "- Check inbound orchestrator messages with `az mail list --parent %s --since 0 --json` before declaring yourself blocked or idle; apply events for `%s` and continue without waiting for a separate user prompt.\n", parentIssueID, issueID)
		fmt.Fprintf(&b, "- Report to parent `%s` with `az mail send --parent %s --issue %s --type <worker-progress|worker-blocked|worker-integration-ready> --body \"<evidence>\"`; do not use `az orchestrate message` for your own status because it is an orchestrator-to-worker live delivery command.\n", parentIssueID, parentIssueID, issueID)
		fmt.Fprintf(&b, "- Evidence bodies should be JSON `worker_evidence.v1` packets with `summary`, `commands_run`, `key_assertions`, `files_changed`, `review.status`, `review.findings`, `risks`, and optional `artifact_links`; use `\"none\"` entries when a required list has no findings or risks.\n")
		fmt.Fprintf(&b, "- When ready for integration, set/leave the issue `in_review` and send `worker-integration-ready` to parent `%s` with a complete `worker_evidence.v1` packet; leave integration/merge/close to the orchestrator.\n", parentIssueID)
	} else {
		fmt.Fprintf(&b, "- Return progress, blockers, and final results through the native subagent result channel.\n")
		fmt.Fprintf(&b, "- Do not use `az mail` unless the orchestrator explicitly asks for mailbox coordination.\n")
	}
	fmt.Fprintf(&b, "- Do not close root issue `%s`; close only your worker issue when the orchestrator has integrated or explicitly instructs you.\n", rootIssueID)
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

func submitSessionStartForIssue(deps *Dependencies, issueID string) (orchestrateStartLaunch, error) {
	return submitSessionStartForIssueWithBaseBranch(deps, issueID, "")
}

func submitSessionStartForIssueWithBaseBranch(deps *Dependencies, issueID, baseBranchOverride string) (orchestrateStartLaunch, error) {
	ctx, cancel := context.WithTimeout(context.Background(), daemonCommandTimeout)
	defer cancel()
	task, err := validateSessionIssueID(ctx, deps, issueID)
	if err != nil {
		return orchestrateStartLaunch{}, err
	}
	baseBranch := strings.TrimSpace(baseBranchOverride)
	if baseBranch == "" {
		resolvedBaseBranch, err := resolveSessionStartBaseBranch(ctx, deps, task)
		if err != nil {
			return orchestrateStartLaunch{}, err
		}
		baseBranch = resolvedBaseBranch
	}
	parsedIssueID, err := naming.ParseIssueID(issueID)
	if err != nil {
		return orchestrateStartLaunch{}, fmt.Errorf("invalid issue id %q: %w", issueID, err)
	}
	sessionID := naming.CanonicalSessionIDForIssue(deps.RepoDir, parsedIssueID).String()
	record, err := deps.DaemonClient.StartSessionOperation(ctx, daemonclient.StartSessionParams{
		IssueID:    issueID,
		RepoDir:    deps.RepoDir,
		BaseBranch: baseBranch,
	})
	if err != nil {
		return orchestrateStartLaunch{}, fmt.Errorf("submit session start operation: %w", err)
	}
	launch := orchestrateStartLaunch{
		IssueID:        issueID,
		SessionID:      sessionID,
		OperationID:    record.OperationID.String(),
		OperationState: string(record.State),
	}
	return launch, nil
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
	if len(ready.Runnable)+len(ready.Pending)+len(ready.Active)+len(ready.Blocked)+len(ready.ActiveSessions)+len(ready.SessionStartProgress) == 0 {
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

func orchestrateIntegrationCommands(issueID string, wt daemonclient.Worktree, found, mergeReady bool) []string {
	commands := make([]string, 0, 5)
	if found && strings.TrimSpace(wt.Path) != "" {
		commands = append(commands,
			fmt.Sprintf("git -C %s status --short", shellSingleQuote(wt.Path)),
			fmt.Sprintf("git -C %s log --oneline --max-count=10", shellSingleQuote(wt.Path)),
		)
	} else {
		commands = append(commands, fmt.Sprintf("az issue get %s", issueID), fmt.Sprintf("az session status %s", issueID))
	}
	if mergeReady {
		commands = append(commands, issueCloseCommand(issueID))
		commands = append(commands, fmt.Sprintf("repair merge only if close reports conflicts: az branch merge %s", issueID))
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
