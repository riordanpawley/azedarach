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
	Project     string
	RootIssueID string
	IssueID     string
	Type        string
	Body        string
	JSON        bool
}

func issueCloseCommand(issueID string) string {
	return fmt.Sprintf("az issue close --id %s", issueID)
}

type orchestrateStatusResult struct {
	RootIssueID    string                     `json:"root_issue_id"`
	Runnable       []string                   `json:"runnable"`
	Active         []string                   `json:"active,omitempty"`
	ActiveSessions []orchestrateActiveSession `json:"active_sessions,omitempty"`
	Blocked        map[string]string          `json:"blocked"`
	MailboxEvents  []protocol.MailEvent       `json:"mailbox_events"`
	Advice         map[string]interface{}     `json:"advice,omitempty"`
}

type orchestrateStartResult struct {
	RootIssueID string                   `json:"root_issue_id"`
	Limit       int                      `json:"limit"`
	Requested   []string                 `json:"requested"`
	Started     []string                 `json:"started"`
	Launched    []orchestrateStartLaunch `json:"launched,omitempty"`
	Skipped     map[string]string        `json:"skipped"`
	Failed      map[string]string        `json:"failed"`
	Warnings    []string                 `json:"warnings,omitempty"`
	Advice      orchestrateStartAdvice   `json:"advice,omitempty"`
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

type orchestrateStartAdvice struct {
	WatchCommand     string `json:"watch_command,omitempty"`
	WatchInstruction string `json:"watch_instruction,omitempty"`
}

type orchestrateWatchFrame struct {
	RootIssueID    string                     `json:"root_issue_id"`
	SinceSeq       int64                      `json:"since_seq"`
	NextSince      int64                      `json:"next_since"`
	Runnable       []string                   `json:"runnable"`
	Active         []string                   `json:"active,omitempty"`
	ActiveSessions []orchestrateActiveSession `json:"active_sessions,omitempty"`
	Blocked        map[string]string          `json:"blocked"`
	Events         []mailEvent                `json:"events"`
}

type orchestrateActiveSession struct {
	IssueID           string `json:"issue_id"`
	Activity          string `json:"activity"`
	ActivitySource    string `json:"activity_source"`
	State             string `json:"state,omitempty"`
	TmuxAttachedCount int    `json:"tmux_attached_count,omitempty"`
	Advice            string `json:"advice,omitempty"`
}

type orchestrateCompleteCheckResult struct {
	RootIssueID string   `json:"root_issue_id"`
	Pass        bool     `json:"pass"`
	Reasons     []string `json:"reasons,omitempty"`
	Advice      []string `json:"advice,omitempty"`
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
	IssueID      string                     `json:"issue_id"`
	WorktreePath string                     `json:"worktree_path,omitempty"`
	Branch       string                     `json:"branch,omitempty"`
	MergeReady   bool                       `json:"merge_ready"`
	Apply        bool                       `json:"apply"`
	Applied      bool                       `json:"applied"`
	Reasons      []string                   `json:"reasons,omitempty"`
	Commands     []string                   `json:"commands"`
	Steps        []orchestrateIntegrateStep `json:"steps,omitempty"`
	Recovery     []string                   `json:"recovery,omitempty"`
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

	tasks, err := deps.DaemonClient.ListTasks(ctx)
	if err != nil {
		return fmt.Errorf("list tasks: %w", err)
	}
	ready, err := computeRunnableLeaves(opts.RootIssueID, tasks)
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
		RootIssueID:    ready.RootIssueID,
		Runnable:       ready.Runnable,
		Active:         ready.Active,
		ActiveSessions: orchestrateActiveSessions(ready.Active, tasks),
		Blocked:        ready.Blocked,
		MailboxEvents:  events,
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
	if len(result.Active) > 0 {
		fmt.Println("Active leaves:")
		for _, active := range result.ActiveSessions {
			fmt.Printf("- %s activity=%s source=%s\n", active.IssueID, active.Activity, active.ActivitySource)
			if active.Advice != "" {
				fmt.Printf("  %s\n", active.Advice)
			}
		}
	}
	fmt.Printf("Mailbox events (latest %d, since seq>%d): %d\n", opts.Limit, opts.SinceSeq, len(result.MailboxEvents))
	for _, evt := range result.MailboxEvents {
		fmt.Printf("- seq=%d issue=%s type=%s from=%s to=%s\n", evt.Seq, strings.TrimSpace(evt.IssueID.String()), evt.Type, strings.TrimSpace(evt.From), strings.TrimSpace(evt.To))
	}
	fmt.Println("Next watch command (leave running while workers are active; do not add --once):")
	fmt.Printf("- %s\n", result.Advice["watch"])
	fmt.Printf("- %s\n", result.Advice["watch_instruction"])
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
		return printJSON(result)
	}
	printOrchestrateStartResult(result)
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
	tasks, err := deps.DaemonClient.ListTasks(ctx)
	if err != nil {
		return orchestrateStartResult{}, fmt.Errorf("list tasks: %w", err)
	}
	ready, err := computeRunnableLeaves(opts.RootIssueID, tasks)
	if err != nil {
		return orchestrateStartResult{}, err
	}

	byID := make(map[string]domain.Task, len(tasks))
	for _, task := range tasks {
		byID[task.ID.String()] = task
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
		Warnings:    orchestrateStartWarnings(ctx, deps, len(requested) > 0),
		Advice: orchestrateStartAdvice{
			WatchCommand:     fmt.Sprintf("az orchestrate watch --root %s --since 0 --jsonl", opts.RootIssueID),
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
		task, ok := byID[issueID]
		if !ok {
			result.Failed[issueID] = "task-not-found"
			continue
		}
		if task.HasTmuxSession {
			result.Skipped[issueID] = "session-already-running"
			continue
		}
		emitOrchestrateStartProgress(opts, "preparing", issueID)
		launch, err := submitSessionStartForIssueWithBaseBranch(deps, issueID, opts.BaseBranchOverride)
		if err != nil {
			result.Failed[issueID] = err.Error()
			continue
		}
		emitOrchestrateStartProgress(opts, "submitted", issueID)
		pendingLaunches = append(pendingLaunches, launch)
		count++
	}

	for _, launch := range pendingLaunches {
		issueID := launch.IssueID
		emitOrchestrateStartProgress(opts, "waiting", issueID)
		completedLaunch, err := waitForSubmittedSessionStart(deps, launch)
		if err != nil {
			result.Failed[issueID] = err.Error()
			continue
		}
		if sendErr := sendOrchestrateMailEvent(deps, opts.RootIssueID, issueID, "session-started", "session launched by az orchestrate start"); sendErr != nil {
			result.Failed[issueID] = fmt.Sprintf("session started but failed to emit event: %v", sendErr)
			continue
		}
		result.Started = append(result.Started, issueID)
		if strings.TrimSpace(completedLaunch.Warning) != "" {
			result.Warnings = append(result.Warnings, fmt.Sprintf("%s: %s", issueID, completedLaunch.Warning))
		}
		completedLaunch.WatchCommand = result.Advice.WatchCommand
		completedLaunch.IntegrateHint = issueCloseCommand(issueID)
		completedLaunch.CloseHint = fmt.Sprintf("az orchestrate close-session --issue %s", issueID)
		result.Launched = append(result.Launched, completedLaunch)
		emitOrchestrateStartProgress(opts, "started", issueID)
	}

	return result, nil
}

func emitOrchestrateStartProgress(opts OrchestrateStartOptions, stage, issueID string) {
	if !opts.JSON {
		return
	}
	fmt.Fprintf(os.Stderr, "orchestrate start: %s %s\n", stage, issueID)
}

func orchestrateActiveSessions(activeIDs []string, tasks []domain.Task) []orchestrateActiveSession {
	if len(activeIDs) == 0 {
		return nil
	}
	byID := make(map[string]domain.Task, len(tasks))
	for _, task := range tasks {
		byID[task.ID.String()] = task
	}
	out := make([]orchestrateActiveSession, 0, len(activeIDs))
	for _, issueID := range activeIDs {
		task, ok := byID[issueID]
		active := orchestrateActiveSession{
			IssueID:        issueID,
			Activity:       "unknown",
			ActivitySource: "none",
			Advice:         fmt.Sprintf("activity unknown: check hooks with az ai status --target=auto; install/update with az ai install --target=auto; use sparse pane capture only if status/watch looks stale, failed, or contradictory for %s", issueID),
		}
		if ok && task.Session != nil {
			active.State = string(task.Session.State)
			active.TmuxAttachedCount = task.Session.TmuxAttachedCount
			if activity := strings.TrimSpace(task.Session.Activity); activity != "" {
				active.Activity = activity
			}
			if source := strings.TrimSpace(task.Session.ActivitySource); source != "" {
				active.ActivitySource = source
			}
			if active.Activity != "unknown" {
				active.Advice = ""
			}
		}
		out = append(out, active)
	}
	return out
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
	if len(frame.Events) > 0 || opts.Once {
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
		if len(events) == 0 {
			continue
		}
		watchEvents := make([]mailEvent, 0, len(events))
		for _, event := range events {
			watchEvents = append(watchEvents, protocolToLocalMailEvent(event))
		}
		tasks, err := watchDaemonCommand(deps, func(ctx context.Context) ([]domain.Task, error) {
			return deps.DaemonClient.ListTasks(ctx)
		})
		if err != nil {
			if shouldContinueOrchestrateWatchAfterError(err) {
				continue
			}
			return fmt.Errorf("list tasks: %w", err)
		}
		ready, err := computeRunnableLeaves(opts.RootIssueID, tasks)
		if err != nil {
			return err
		}
		nextSince := nextMailboxSeq(events, lastSeq)
		frame := orchestrateWatchFrame{
			RootIssueID:    ready.RootIssueID,
			SinceSeq:       lastSeq,
			NextSince:      nextSince,
			Runnable:       ready.Runnable,
			Active:         ready.Active,
			ActiveSessions: orchestrateActiveSessions(ready.Active, tasks),
			Blocked:        ready.Blocked,
			Events:         watchEvents,
		}
		if err := emitOrchestrateWatchFrame(frame, opts.JSONL); err != nil {
			return err
		}
		lastSeq = nextSince
	}
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
	tasks, err := deps.DaemonClient.ListTasks(ctx)
	if err != nil {
		return fmt.Errorf("list tasks: %w", err)
	}
	result, err := evaluateOrchestrateCompleteCheck(opts.RootIssueID, tasks)
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
	tasks, err := deps.DaemonClient.ListTasks(ctx)
	if err != nil {
		return fmt.Errorf("list tasks: %w", err)
	}
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
	mergeReady, reasons, err := orchestrateIntegrationMergeReadiness(ctx, deps, opts.IssueID)
	if err != nil {
		return err
	}
	commands := orchestrateIntegrationCommands(opts.IssueID, wt, found, mergeReady)
	result := orchestrateIntegrateResult{
		IssueID:    opts.IssueID,
		MergeReady: mergeReady,
		Apply:      opts.Apply,
		Reasons:    reasons,
		Commands:   commands,
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
		for _, reason := range reasons {
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

	mergeResult, err := runBranchMergeToBase(deps, BranchMergeToBaseOptions{IssueID: issueID})
	if err != nil {
		result.Steps = append(result.Steps, orchestrateIntegrateStep{Name: "merge", Status: "failed", Error: err.Error()})
		result.Recovery = orchestrateIntegrationRecovery(issueID, "merge_failed")
		return fmt.Errorf("apply integration merge for %s: %w", issueID, err)
	}
	result.Steps = append(result.Steps, orchestrateIntegrateStep{
		Name:   "merge",
		Status: "success",
		Output: fmt.Sprintf("Merged %s into %s (%s)", mergeResult.SourceBranch, mergeResult.BaseBranch, mergeResult.IssueID),
	})
	result.Applied = true

	cleanupCtx, cancel := context.WithTimeout(context.Background(), daemonCommandTimeout)
	defer cancel()
	var failures []string
	if _, err := deps.DaemonClient.ValidateTaskCloseWithOptions(cleanupCtx, issueID, daemonclient.CloseGuardOptions{
		AllowTargetSession:  true,
		AllowTargetWorktree: true,
	}); err != nil {
		result.Steps = append(result.Steps, orchestrateIntegrateStep{Name: "close_preflight", Status: "failed", Error: err.Error()})
		result.Recovery = orchestrateIntegrationRecovery(issueID, "post_merge_failed")
		return fmt.Errorf("integration applied for %s but close preflight failed: %w", issueID, err)
	}
	result.Steps = append(result.Steps, orchestrateIntegrateStep{Name: "close_preflight", Status: "success"})

	if _, err := deps.DaemonClient.StopSession(cleanupCtx, issueID); err != nil {
		result.Steps = append(result.Steps, orchestrateIntegrateStep{Name: "stop_session", Status: "failed", Error: err.Error()})
		failures = append(failures, fmt.Sprintf("stop session: %v", err))
	} else {
		result.Steps = append(result.Steps, orchestrateIntegrateStep{Name: "stop_session", Status: "success"})
	}

	if err := deps.DaemonClient.RemoveWorktreeWithOptions(cleanupCtx, issueID, false); err != nil {
		result.Steps = append(result.Steps, orchestrateIntegrateStep{Name: "remove_worktree", Status: "failed", Error: err.Error()})
		failures = append(failures, fmt.Sprintf("remove worktree: %v", err))
	} else {
		result.Steps = append(result.Steps, orchestrateIntegrateStep{Name: "remove_worktree", Status: "success"})
	}

	if err := deps.DaemonClient.UpdateTaskStatus(cleanupCtx, issueID, domain.StatusDone); err != nil {
		result.Steps = append(result.Steps, orchestrateIntegrateStep{Name: "close_issue", Status: "failed", Error: err.Error()})
		failures = append(failures, fmt.Sprintf("close issue: %v", err))
	} else {
		result.Steps = append(result.Steps, orchestrateIntegrateStep{Name: "close_issue", Status: "success"})
	}

	note := fmt.Sprintf("Integrated by `az orchestrate integrate --issue %s --apply`: merge applied, session stop attempted, worktree removal attempted, issue close attempted.", issueID)
	if err := deps.DaemonClient.AppendTaskNotes(cleanupCtx, issueID, note); err != nil {
		result.Steps = append(result.Steps, orchestrateIntegrateStep{Name: "append_evidence", Status: "failed", Error: err.Error()})
		failures = append(failures, fmt.Sprintf("append evidence: %v", err))
	} else {
		result.Steps = append(result.Steps, orchestrateIntegrateStep{Name: "append_evidence", Status: "success"})
	}

	if len(failures) > 0 {
		result.Recovery = orchestrateIntegrationRecovery(issueID, "post_merge_failed")
		return fmt.Errorf("integration applied for %s but cleanup had failures: %s", issueID, strings.Join(failures, "; "))
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
	case "merge_failed":
		return []string{
			fmt.Sprintf("inspect merge failure and retry existing merge path: az branch merge %s", issueID),
			fmt.Sprintf("after merge succeeds, retry cleanup: az orchestrate integrate --issue %s --apply", issueID),
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

func formatOrchestratorSessionMessage(opts OrchestrateMessageOptions) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Orchestrator message for issue %s under root %s:\n\n", strings.TrimSpace(opts.IssueID), strings.TrimSpace(opts.RootIssueID))
	b.WriteString(strings.TrimSpace(opts.Body))
	b.WriteString("\n\nContinue from the current state. If this changes your status, update the issue notes/status and send worker-progress, worker-blocked, or worker-integration-ready evidence as appropriate.")
	return b.String()
}

func buildOrchestrateWatchFrame(deps *Dependencies, rootIssueID string, since int64) (orchestrateWatchFrame, error) {
	tasks, err := deps.DaemonClient.ListTasks(context.Background())
	if err != nil {
		return orchestrateWatchFrame{}, fmt.Errorf("list tasks: %w", err)
	}
	ready, err := computeRunnableLeaves(rootIssueID, tasks)
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
	return orchestrateWatchFrame{
		RootIssueID:    ready.RootIssueID,
		SinceSeq:       since,
		NextSince:      nextMailboxSeq(events, since),
		Runnable:       ready.Runnable,
		Active:         ready.Active,
		ActiveSessions: orchestrateActiveSessions(ready.Active, tasks),
		Blocked:        ready.Blocked,
		Events:         watchEvents,
	}, nil
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
	if len(frame.Active) > 0 {
		fmt.Println("active:")
		for _, active := range frame.ActiveSessions {
			fmt.Printf("- %s activity=%s source=%s\n", active.IssueID, active.Activity, active.ActivitySource)
			if active.Advice != "" {
				fmt.Printf("  %s\n", active.Advice)
			}
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
			fmt.Sprintf("az mail send --parent %s --issue %s --type worker-integration-ready --body \"<evidence>\"", parentIssueID, issueID),
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
	fmt.Fprintf(&b, "- Before behavior changes, inspect linked requirements with `az spec read --issue %s`; if none are linked, find a nearby requirement with `az spec req list`/`az spec read --req <req-id>` or create/update one before implementation. For contract-preserving refactors, tests, tooling, docs, internal cleanup, or fixes that restore existing behavior, record explicit `Spec impact: none (...)` evidence instead of creating implementation-only requirements.\n", issueID)
	fmt.Fprintf(&b, "- Keep `%s` status and notes current with terse evidence: final commands run, key outputs/assertions, files changed, AC pass/fail, blockers, and remaining scope only.\n", issueID)
	fmt.Fprintf(&b, "- Status semantics: use `in_progress` while actively working, `in_review` when your implementation is complete and ready for orchestrator review/integration, and `closed` only after the orchestrator has integrated/accepted the work.\n")
	fmt.Fprintf(&b, "- Blocked work is represented by dependency edges or `worker-blocked` mailbox events, not by setting issue status to `in_review`.\n")
	fmt.Fprintf(&b, "- Do not append raw logs, exploratory transcripts, routine progress narration, duplicate prompt context, or speculative scratch work to notes.\n")
	if coordination == "mailbox" {
		fmt.Fprintf(&b, "- Use mailbox events for hybrid coordination: `worker-progress`, `worker-blocked`, and `worker-integration-ready`; `worker-ready` and `worker-complete` are accepted only as legacy aliases for `worker-integration-ready`.\n")
		fmt.Fprintf(&b, "- Check inbound orchestrator messages with `az mail list --parent %s --since 0 --json` before declaring yourself blocked or idle; apply events for `%s` and continue without waiting for a separate user prompt.\n", parentIssueID, issueID)
		fmt.Fprintf(&b, "- When ready for integration, set/leave the issue `in_review` and send `worker-integration-ready` to parent `%s` with concise evidence; leave integration/merge/close to the orchestrator.\n", parentIssueID)
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

func evaluateOrchestrateCompleteCheck(rootIssueID string, tasks []domain.Task) (orchestrateCompleteCheckResult, error) {
	rootID, err := naming.ParseIssueID(strings.TrimSpace(rootIssueID))
	if err != nil {
		return orchestrateCompleteCheckResult{}, fmt.Errorf("invalid root issue id %q: %w", rootIssueID, err)
	}
	byID := make(map[naming.IssueID]domain.Task, len(tasks))
	children := make(map[naming.IssueID][]naming.IssueID, len(tasks))
	for _, t := range tasks {
		byID[t.ID] = t
		if t.ParentID != nil && !t.ParentID.IsZero() {
			children[*t.ParentID] = append(children[*t.ParentID], t.ID)
		}
	}
	if _, ok := byID[rootID]; !ok {
		return orchestrateCompleteCheckResult{}, fmt.Errorf("root issue not found: %s", rootIssueID)
	}
	ready, err := computeRunnableLeaves(rootIssueID, tasks)
	if err != nil {
		return orchestrateCompleteCheckResult{}, err
	}

	reasons := make([]string, 0, 8)
	if len(ready.Runnable) > 0 {
		reasons = append(reasons, fmt.Sprintf("runnable leaves remain: %s", strings.Join(ready.Runnable, ",")))
	}
	desc := collectDescendants(rootID, children)
	openDescendants := make([]string, 0, len(desc))
	activeSessions := make([]string, 0, len(desc))
	for _, id := range desc {
		task := byID[id]
		if task.Status != domain.StatusDone {
			openDescendants = append(openDescendants, id.String())
		}
		if task.HasTmuxSession {
			activeSessions = append(activeSessions, id.String())
		}
	}
	sort.Strings(openDescendants)
	sort.Strings(activeSessions)
	if len(openDescendants) > 0 {
		reasons = append(reasons, fmt.Sprintf("required descendants not closed: %s", strings.Join(openDescendants, ",")))
	}
	if len(activeSessions) > 0 {
		reasons = append(reasons, fmt.Sprintf("active child sessions remain: %s", strings.Join(activeSessions, ",")))
	}

	return orchestrateCompleteCheckResult{
		RootIssueID: rootIssueID,
		Pass:        len(reasons) == 0,
		Reasons:     reasons,
		Advice:      orchestrateCompletionAdvice(ready.Runnable, openDescendants, activeSessions),
	}, nil
}

func startSessionForIssue(deps *Dependencies, issueID string) error {
	ctx, cancel := context.WithTimeout(context.Background(), sessionStartCommandTimeout)
	defer cancel()
	task, err := validateSessionIssueID(ctx, deps, issueID)
	if err != nil {
		return err
	}
	baseBranch, err := resolveSessionStartBaseBranch(ctx, deps, task)
	if err != nil {
		return err
	}
	resp, err := deps.DaemonClient.Command(ctx, newSessionRequest(commandSessionStart, deps.ProjectID, issueID, baseBranch))
	if err != nil {
		return fmt.Errorf("failed to start session: %w", err)
	}
	if err := responseError(resp, "failed to start session"); err != nil {
		return err
	}
	return nil
}

func submitSessionStartForIssue(deps *Dependencies, issueID string) (orchestrateStartLaunch, error) {
	return submitSessionStartForIssueWithBaseBranch(deps, issueID, "")
}

func waitForSubmittedSessionStart(deps *Dependencies, launch orchestrateStartLaunch) (orchestrateStartLaunch, error) {
	if launch.OperationID == "" {
		return launch, fmt.Errorf("session start operation missing operation id")
	}
	ctx, cancel := context.WithTimeout(context.Background(), sessionStartCommandTimeout)
	defer cancel()
	record, err := deps.DaemonClient.WaitForOperation(ctx, launch.OperationID, 0)
	if err != nil {
		return launch, fmt.Errorf("wait for session start operation %s: %w", launch.OperationID, err)
	}
	launch.OperationState = string(record.State)
	if record.State != protocol.OperationStateDone {
		var message string
		if record.Error != nil {
			message = strings.TrimSpace(record.Error.Message)
		}
		if message == "" {
			message = fmt.Sprintf("session start operation ended in state %s", record.State)
		}
		return launch, fmt.Errorf("%s", message)
	}
	if wt, found, wtErr := worktreeForIssue(ctx, deps, launch.IssueID); wtErr == nil && found {
		launch.WorktreePath = wt.Path
	}
	launch.Warning = sessionStartWarningFromOperationResult(record.Result)
	return launch, nil
}

func sessionStartWarningFromOperationResult(result json.RawMessage) string {
	if len(result) == 0 {
		return ""
	}
	var payload struct {
		Output string `json:"output"`
	}
	if err := json.Unmarshal(result, &payload); err != nil {
		return ""
	}
	for _, line := range strings.Split(payload.Output, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(strings.ToLower(line), "worktree setup warning:") {
			return line
		}
	}
	return ""
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
	req := newSessionRequest(commandSessionStart, deps.ProjectID, issueID, baseBranch)
	body, err := json.Marshal(protocol.OperationSubmitRequestBody{
		ProjectID:    naming.ProjectID(deps.ProjectID),
		Kind:         commandSessionStart,
		IssueID:      parsedIssueID,
		DedupeKey:    "session.start:" + issueID,
		ResourceKeys: []string{"session:" + sessionID, "worktree:" + issueID},
		Payload:      append(json.RawMessage(nil), req.Body...),
	})
	if err != nil {
		return orchestrateStartLaunch{}, fmt.Errorf("marshal operation submit: %w", err)
	}
	resp, err := deps.DaemonClient.Command(ctx, protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       makeRequestID(protocol.CommandOperationSubmit),
		Kind:            protocol.EnvelopeKindCommand,
		Command:         protocol.CommandOperationSubmit,
		Meta: protocol.Metadata{
			ProjectID: naming.ProjectID(deps.ProjectID),
		},
		SentAt: time.Now().UTC(),
		Body:   body,
	})
	if err != nil {
		return orchestrateStartLaunch{}, fmt.Errorf("submit session start operation: %w", err)
	}
	if err := responseError(resp, "submit session start operation"); err != nil {
		return orchestrateStartLaunch{}, err
	}
	var out protocol.OperationSubmitResponseBody
	if err := json.Unmarshal(resp.Body, &out); err != nil {
		return orchestrateStartLaunch{}, fmt.Errorf("decode session start operation: %w", err)
	}
	launch := orchestrateStartLaunch{
		IssueID:        issueID,
		SessionID:      sessionID,
		OperationID:    out.Operation.OperationID.String(),
		OperationState: string(out.Operation.State),
	}
	return launch, nil
}

func orchestrateStartWarnings(ctx context.Context, deps *Dependencies, willStart bool) []string {
	if !willStart || deps == nil || deps.DaemonClient == nil {
		return nil
	}
	cwd, err := os.Getwd()
	if err != nil {
		return []string{fmt.Sprintf("could not inspect parent worktree dirtiness: %v", err)}
	}
	status, err := deps.DaemonClient.GitStatus(ctx, cwd)
	if err != nil {
		return []string{fmt.Sprintf("could not inspect parent worktree dirtiness: %v", err)}
	}
	dirty := dirtyFilesFromGitStatus(status)
	if len(dirty) == 0 {
		return nil
	}
	return []string{
		fmt.Sprintf("parent worktree has uncommitted tracked changes (%s); worker worktrees are created from committed branch state and will not see these files: %s", summarizeGitStatusCounts(status), strings.Join(dirty, ", ")),
	}
}

func orchestrateCompletionAdvice(runnable, openDescendants, activeSessions []string) []string {
	advice := make([]string, 0, len(runnable)+len(openDescendants)+len(activeSessions))
	for _, id := range activeSessions {
		advice = append(advice, fmt.Sprintf("if intentionally abandoning active worker session, repair-stop it: az orchestrate close-session --issue %s", id))
	}
	for _, id := range openDescendants {
		advice = append(advice, fmt.Sprintf("after integration/evidence, close required child issue: %s", issueCloseCommand(id)))
	}
	for _, id := range runnable {
		advice = append(advice, fmt.Sprintf("start or resolve runnable leaf: az orchestrate start --root <root> --issue %s --json", id))
	}
	return uniqueTrimmedStrings(advice)
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

func orchestrateIntegrationMergeReadiness(ctx context.Context, deps *Dependencies, issueID string) (bool, []string, error) {
	tasks, err := deps.DaemonClient.ListTasks(ctx)
	if err != nil {
		return false, nil, fmt.Errorf("list tasks: %w", err)
	}
	task, ok := findTaskByID(tasks, issueID)
	if !ok {
		return false, []string{fmt.Sprintf("issue %s not found in daemon task projection", issueID)}, nil
	}
	if task.Status == domain.StatusDone {
		return true, nil, nil
	}
	parentIssueID := issueID
	if task.ParentID != nil && strings.TrimSpace(task.ParentID.String()) != "" {
		parentIssueID = strings.TrimSpace(task.ParentID.String())
	}
	events, err := deps.DaemonClient.MailList(ctx, protocol.MailListCommandBody{
		RepoDir:     deps.RepoDir,
		ParentIssue: parentIssueID,
		SinceSeq:    0,
		Limit:       500,
	})
	if err != nil {
		return false, nil, fmt.Errorf("list mailbox events for %s: %w", parentIssueID, err)
	}
	for _, evt := range events {
		if naming.IssueIDsEqual(evt.IssueID.String(), issueID) && isWorkerIntegrationReadyMailType(evt.Type) {
			return true, nil, nil
		}
	}
	reasons := []string{
		fmt.Sprintf("issue %s is not closed", issueID),
		fmt.Sprintf("no worker-integration-ready mailbox event found under parent %s for %s", parentIssueID, issueID),
	}
	return false, reasons, nil
}

func isWorkerIntegrationReadyMailType(eventType string) bool {
	switch strings.ToLower(strings.TrimSpace(eventType)) {
	case "worker-integration-ready", "worker-ready", "worker-complete":
		return true
	default:
		return false
	}
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
