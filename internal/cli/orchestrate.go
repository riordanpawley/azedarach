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
	Project     string
	RootIssueID string
	Limit       int
	IssueIDs    []string
	JSON        bool
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

type OrchestrateIntegrateOptions struct {
	Project string
	IssueID string
	JSON    bool
}

type OrchestrateCloseSessionOptions struct {
	Project string
	IssueID string
	JSON    bool
}

type orchestrateStatusResult struct {
	RootIssueID   string                 `json:"root_issue_id"`
	Runnable      []string               `json:"runnable"`
	Active        []string               `json:"active,omitempty"`
	Blocked       map[string]string      `json:"blocked"`
	MailboxEvents []protocol.MailEvent   `json:"mailbox_events"`
	Advice        map[string]interface{} `json:"advice,omitempty"`
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
	WatchCommand   string `json:"watch_command"`
	IntegrateHint  string `json:"integrate_hint"`
	CloseHint      string `json:"close_hint"`
}

type orchestrateStartAdvice struct {
	WatchCommand string `json:"watch_command,omitempty"`
}

type orchestrateWatchFrame struct {
	RootIssueID string            `json:"root_issue_id"`
	SinceSeq    int64             `json:"since_seq"`
	NextSince   int64             `json:"next_since"`
	Runnable    []string          `json:"runnable"`
	Active      []string          `json:"active,omitempty"`
	Blocked     map[string]string `json:"blocked"`
	Events      []mailEvent       `json:"events"`
}

type orchestrateCompleteCheckResult struct {
	RootIssueID string   `json:"root_issue_id"`
	Pass        bool     `json:"pass"`
	Reasons     []string `json:"reasons,omitempty"`
	Advice      []string `json:"advice,omitempty"`
}

type orchestrateIntegrateResult struct {
	IssueID      string   `json:"issue_id"`
	WorktreePath string   `json:"worktree_path,omitempty"`
	Branch       string   `json:"branch,omitempty"`
	Commands     []string `json:"commands"`
}

type orchestrateCloseSessionResult struct {
	IssueID string `json:"issue_id"`
	Output  string `json:"output,omitempty"`
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

func ParseOrchestrateIntegrateArgs(args []string) (OrchestrateIntegrateOptions, error) {
	opts := OrchestrateIntegrateOptions{}
	fs := flag.NewFlagSet("orchestrate integrate", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	addIssueProjectFlag(fs, &opts.Project)
	fs.StringVar(&opts.IssueID, "issue", "", "worker issue id to integrate")
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
		RootIssueID:   ready.RootIssueID,
		Runnable:      ready.Runnable,
		Active:        ready.Active,
		Blocked:       ready.Blocked,
		MailboxEvents: events,
		Advice: map[string]interface{}{
			"watch": fmt.Sprintf("az orchestrate watch --root %s --since %d --jsonl", ready.RootIssueID, nextMailboxSeq(events, opts.SinceSeq)),
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
		for _, id := range result.Active {
			fmt.Printf("- %s\n", id)
		}
	}
	fmt.Printf("Mailbox events (latest %d, since seq>%d): %d\n", opts.Limit, opts.SinceSeq, len(result.MailboxEvents))
	for _, evt := range result.MailboxEvents {
		fmt.Printf("- seq=%d issue=%s type=%s from=%s to=%s\n", evt.Seq, strings.TrimSpace(evt.IssueID.String()), evt.Type, strings.TrimSpace(evt.From), strings.TrimSpace(evt.To))
	}
	fmt.Println("Next watch command:")
	fmt.Printf("- %s\n", result.Advice["watch"])
	return nil
}

func OrchestrateStartCommand(deps *Dependencies, opts OrchestrateStartOptions) error {
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
			WatchCommand: fmt.Sprintf("az orchestrate watch --root %s --since 0 --jsonl", opts.RootIssueID),
		},
	}

	count := 0
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
		launch, err := submitSessionStartForIssue(deps, issueID)
		if err != nil {
			result.Failed[issueID] = err.Error()
			continue
		}
		if sendErr := sendOrchestrateMailEvent(deps, opts.RootIssueID, issueID, "session-started", "session launched by az orchestrate start"); sendErr != nil {
			result.Failed[issueID] = fmt.Sprintf("session started but failed to emit event: %v", sendErr)
			continue
		}
		result.Started = append(result.Started, issueID)
		launch.WatchCommand = result.Advice.WatchCommand
		launch.IntegrateHint = fmt.Sprintf("az orchestrate integrate --issue %s", issueID)
		launch.CloseHint = fmt.Sprintf("az orchestrate close-session --issue %s", issueID)
		result.Launched = append(result.Launched, launch)
		count++
	}

	if opts.JSON {
		return printJSON(result)
	}

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
			fmt.Printf("  integrate: %s\n", launch.IntegrateHint)
			fmt.Printf("  close session: %s\n", launch.CloseHint)
		}
	}
	if len(result.Warnings) > 0 {
		fmt.Println("Warnings:")
		for _, warning := range result.Warnings {
			fmt.Printf("- %s\n", warning)
		}
	}
	if result.Advice.WatchCommand != "" {
		fmt.Println("Next watch command:")
		fmt.Printf("- %s\n", result.Advice.WatchCommand)
	}
	if len(result.Failed) > 0 {
		fmt.Println("Failed:")
		for _, id := range sortedKeys(result.Failed) {
			fmt.Printf("- %s: %s\n", id, result.Failed[id])
		}
		return fmt.Errorf("orchestrate start completed with failures")
	}
	return nil
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
			return fmt.Errorf("list tasks: %w", err)
		}
		ready, err := computeRunnableLeaves(opts.RootIssueID, tasks)
		if err != nil {
			return err
		}
		nextSince := nextMailboxSeq(events, lastSeq)
		frame := orchestrateWatchFrame{
			RootIssueID: ready.RootIssueID,
			SinceSeq:    lastSeq,
			NextSince:   nextSince,
			Runnable:    ready.Runnable,
			Active:      ready.Active,
			Blocked:     ready.Blocked,
			Events:      watchEvents,
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
	commands := orchestrateIntegrationCommands(opts.IssueID, wt, found)
	result := orchestrateIntegrateResult{
		IssueID:  opts.IssueID,
		Commands: commands,
	}
	if found {
		result.WorktreePath = wt.Path
		result.Branch = wt.Branch
	}
	if opts.JSON {
		return printJSON(result)
	}
	fmt.Printf("Integration guidance for %s\n", opts.IssueID)
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
	fmt.Printf("Session closed for %s. If work is integrated, close the issue with `az issue close %s`.\n", opts.IssueID, opts.IssueID)
	return nil
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
		RootIssueID: ready.RootIssueID,
		SinceSeq:    since,
		NextSince:   nextMailboxSeq(events, since),
		Runnable:    ready.Runnable,
		Active:      ready.Active,
		Blocked:     ready.Blocked,
		Events:      watchEvents,
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
		for _, id := range frame.Active {
			fmt.Printf("- %s\n", id)
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
	ctx, cancel := context.WithTimeout(context.Background(), daemonCommandTimeout)
	defer cancel()
	task, err := validateSessionIssueID(ctx, deps, issueID)
	if err != nil {
		return orchestrateStartLaunch{}, err
	}
	baseBranch, err := resolveSessionStartBaseBranch(ctx, deps, task)
	if err != nil {
		return orchestrateStartLaunch{}, err
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
	if wt, found, wtErr := worktreeForIssue(ctx, deps, issueID); wtErr == nil && found {
		launch.WorktreePath = wt.Path
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
		advice = append(advice, fmt.Sprintf("stop active worker session: az orchestrate close-session --issue %s", id))
	}
	for _, id := range openDescendants {
		advice = append(advice, fmt.Sprintf("after integration/evidence, close required child issue: az issue close %s", id))
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

func orchestrateIntegrationCommands(issueID string, wt daemonclient.Worktree, found bool) []string {
	commands := make([]string, 0, 5)
	if found && strings.TrimSpace(wt.Path) != "" {
		commands = append(commands,
			fmt.Sprintf("git -C %s status --short", shellSingleQuote(wt.Path)),
			fmt.Sprintf("git -C %s log --oneline --max-count=10", shellSingleQuote(wt.Path)),
		)
	} else {
		commands = append(commands, fmt.Sprintf("az issue get %s", issueID), fmt.Sprintf("az session status %s", issueID))
	}
	commands = append(commands,
		fmt.Sprintf("az branch merge %s", issueID),
		fmt.Sprintf("az orchestrate close-session --issue %s", issueID),
	)
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
