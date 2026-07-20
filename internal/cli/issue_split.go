package cli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/riordanpawley/azedarach/internal/domain"
)

type IssueSplitOptions struct {
	Project          string
	JSON             bool
	ParentIssueID    string
	Title            string
	Description      string
	Type             domain.TaskType
	Priority         domain.Priority
	PriorityExplicit bool
	Implementations  []string
	IntentKey        string
}

type issueSplitResult struct {
	ParentIssueID string                 `json:"parent_issue_id"`
	ChildIssueID  string                 `json:"child_issue_id"`
	IntentKey     string                 `json:"intent_key"`
	Created       bool                   `json:"created"`
	Start         orchestrateStartResult `json:"start"`
	Advice        issueSplitAdvice       `json:"advice"`
}

type issueSplitAdvice struct {
	StatusCommand    string `json:"status_command"`
	WatchCommand     string `json:"watch_command"`
	IntegrateCommand string `json:"integrate_command"`
	MergeCommand     string `json:"merge_command"`
	CloseCommand     string `json:"close_command"`
	Summary          string `json:"summary"`
}

func ParseIssueSplitArgs(args []string) (IssueSplitOptions, error) {
	opts := IssueSplitOptions{Type: domain.TypeTask, Priority: domain.P2}
	var priorityRaw string
	var typeRaw string
	impls := make([]string, 0, 2)
	fs := flag.NewFlagSet("issue split", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	addIssueProjectFlag(fs, &opts.Project)
	fs.StringVar(&opts.ParentIssueID, "parent", "", "parent issue id (defaults to AZEDARACH_ISSUE_ID)")
	fs.Func("impl", "target implementation key (repeatable)", func(v string) error {
		trimmed := strings.TrimSpace(v)
		if trimmed == "" {
			return fmt.Errorf("empty impl value")
		}
		impls = append(impls, trimmed)
		return nil
	})
	fs.StringVar(&opts.Description, "description", "", "child issue description")
	fs.StringVar(&opts.IntentKey, "intent-key", "", "stable logical split key (exact retries must reuse it)")
	fs.StringVar(&priorityRaw, "priority", "", "child issue priority (P0-P4)")
	fs.StringVar(&typeRaw, "type", string(domain.TypeTask), "child issue type (task|bug|feature|epic|chore|investigation)")
	fs.BoolVar(&opts.JSON, "json", false, "output JSON")
	if err := parseWithInterspersedFlags(fs, args); err != nil {
		return IssueSplitOptions{}, err
	}
	if fs.NArg() != 1 {
		return IssueSplitOptions{}, fmt.Errorf("usage: az ticket split [--project <project-id>] [--parent <ticket-id>] [--impl <implementation> ...] [--intent-key <stable-key>] [--type task|bug|feature|epic|chore|investigation] [--priority P0|P1|P2|P3|P4] [--description text] [--json] <title>")
	}
	opts.Title = fs.Arg(0)
	taskType, err := parseTaskType(typeRaw)
	if err != nil {
		return IssueSplitOptions{}, err
	}
	opts.Type = taskType
	if strings.TrimSpace(priorityRaw) != "" {
		priority, err := parsePriority(priorityRaw)
		if err != nil {
			return IssueSplitOptions{}, err
		}
		opts.Priority = priority
		opts.PriorityExplicit = true
	}
	opts.Implementations = dedupeOrderedIDs(impls)
	opts.ParentIssueID = strings.TrimSpace(opts.ParentIssueID)
	if opts.ParentIssueID == "" {
		opts.ParentIssueID = strings.TrimSpace(os.Getenv("AZEDARACH_ISSUE_ID"))
	}
	if opts.ParentIssueID == "" {
		return IssueSplitOptions{}, fmt.Errorf("missing parent issue: pass --parent or set AZEDARACH_ISSUE_ID")
	}
	opts.Project = normalizeIssueProject(opts.Project)
	opts.IntentKey = strings.TrimSpace(opts.IntentKey)
	if opts.IntentKey == "" {
		opts.IntentKey = deriveIssueSplitIntentKey(opts)
	}
	return opts, nil
}

func deriveIssueSplitIntentKey(opts IssueSplitOptions) string {
	parts := []string{normalizeIssueProject(opts.Project), strings.TrimSpace(opts.ParentIssueID), strings.TrimSpace(opts.Title), opts.Description, string(opts.Type), opts.Priority.String(), fmt.Sprint(opts.PriorityExplicit), strings.Join(dedupeOrderedIDs(opts.Implementations), "\x1f")}
	digest := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return "ticket-split:" + hex.EncodeToString(digest[:])
}

func IssueSplitCommand(deps *Dependencies, opts IssueSplitOptions) error {
	if strings.TrimSpace(opts.IntentKey) == "" {
		opts.IntentKey = deriveIssueSplitIntentKey(opts)
	}
	restoreProject, err := applyExplicitProjectOverride(deps, opts.Project)
	if err != nil {
		return err
	}
	defer restoreProject()

	parentIssueID := strings.TrimSpace(opts.ParentIssueID)
	createResult, err := createIssue(context.Background(), deps, IssueCreateOptions{
		IntentKey:              opts.IntentKey,
		Title:                  opts.Title,
		Description:            opts.Description,
		Type:                   opts.Type,
		Priority:               opts.Priority,
		PriorityExplicit:       opts.PriorityExplicit,
		Implementations:        opts.Implementations,
		AutoParentFromIssueID:  &parentIssueID,
		AutoCreatedFromIssueID: &parentIssueID,
	})
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), daemonCommandTimeout)
	defer cancel()
	baseBranch, err := resolveParentWorktreeBaseBranch(ctx, deps, resolveCLIBaseBranch(deps.Config), createResult.IssueID)
	if err != nil {
		return err
	}

	startResult, err := orchestrateStart(deps, OrchestrateStartOptions{
		RootIssueID:        parentIssueID,
		Limit:              1,
		IssueIDs:           []string{createResult.IssueID},
		BaseBranchOverride: baseBranch,
		IntentKey:          "ticket-split-start:" + opts.IntentKey,
	})
	if err != nil {
		return err
	}
	result := issueSplitResult{
		ParentIssueID: parentIssueID,
		ChildIssueID:  createResult.IssueID,
		IntentKey:     opts.IntentKey,
		Created:       createResult.Created,
		Start:         startResult,
		Advice: issueSplitAdvice{
			StatusCommand:    fmt.Sprintf("az orchestrate status --root %s", parentIssueID),
			WatchCommand:     fmt.Sprintf("az orchestrate watch --root %s --since 0 --jsonl", parentIssueID),
			IntegrateCommand: issueCloseCommand(createResult.IssueID),
			MergeCommand:     fmt.Sprintf("az branch merge --source %s --target %s", createResult.IssueID, parentIssueID),
			CloseCommand:     issueCloseCommand(createResult.IssueID),
			Summary:          "Child work runs in an isolated session/worktree. Keep the parent/orchestrator watching with az orchestrate watch in another pane/session while workers are active; do not use --once for orchestration monitoring. It is not merged at creation; the parent/orchestrator should review, then close the child issue to integrate, record stopped session state, clean up, and mark it closed. Use merge_command only for manual repair.",
		},
	}
	if opts.JSON {
		if err := printJSON(result); err != nil {
			return err
		}
		if len(startResult.Failed) > 0 {
			return fmt.Errorf("issue split created %s but session launch failed", createResult.IssueID)
		}
		return nil
	}

	childResult := "Created child issue"
	if !result.Created {
		childResult = "Reused canonical child issue"
	}
	fmt.Printf("%s: %s (parent: %s)\n", childResult, result.ChildIssueID, result.ParentIssueID)
	fmt.Println("Integration model:")
	fmt.Println("- Child work runs in its own az/tmux session/worktree.")
	fmt.Println("- It is not merged at creation; review it from the parent/orchestrator session, then close it to integrate and clean up.")
	fmt.Println("- `az ticket close` owns merge, stopped session cleanup, worktree cleanup, and ticket closure.")
	printOrchestrateStartResult(startResult)
	fmt.Println("When the child is ready:")
	fmt.Printf("- %s\n", result.Advice.CloseCommand)
	fmt.Println("Repair-only commands:")
	fmt.Printf("- az orchestrate integrate --issue %s\n", result.ChildIssueID)
	fmt.Printf("- %s\n", result.Advice.MergeCommand)
	if len(startResult.Failed) > 0 {
		return fmt.Errorf("issue split created %s but session launch failed", createResult.IssueID)
	}
	return nil
}
