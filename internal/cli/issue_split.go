package cli

import (
	"context"
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
}

type issueSplitResult struct {
	ParentIssueID string                 `json:"parent_issue_id"`
	ChildIssueID  string                 `json:"child_issue_id"`
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
	fs.StringVar(&priorityRaw, "priority", "", "child issue priority (P0-P4)")
	fs.StringVar(&typeRaw, "type", string(domain.TypeTask), "child issue type (task|bug|feature|epic|chore)")
	fs.BoolVar(&opts.JSON, "json", false, "output JSON")
	if err := parseWithInterspersedFlags(fs, args); err != nil {
		return IssueSplitOptions{}, err
	}
	if fs.NArg() != 1 {
		return IssueSplitOptions{}, fmt.Errorf("usage: az issue split [--project <project-id>] [--parent <issue-id>] [--impl <implementation> ...] [--type task|bug|feature|epic|chore] [--priority P0|P1|P2|P3|P4] [--description text] [--json] <title>")
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
	return opts, nil
}

func IssueSplitCommand(deps *Dependencies, opts IssueSplitOptions) error {
	restoreProject := applyIssueProjectOverride(deps, opts.Project)
	defer restoreProject()

	parentIssueID := strings.TrimSpace(opts.ParentIssueID)
	createResult, err := createIssue(context.Background(), deps, IssueCreateOptions{
		Title:                 opts.Title,
		Description:           opts.Description,
		Type:                  opts.Type,
		Priority:              opts.Priority,
		PriorityExplicit:      opts.PriorityExplicit,
		Implementations:       opts.Implementations,
		AutoParentFromIssueID: &parentIssueID,
	})
	if err != nil {
		return err
	}

	startResult, err := orchestrateStart(deps, OrchestrateStartOptions{
		RootIssueID: parentIssueID,
		Limit:       1,
		IssueIDs:    []string{createResult.IssueID},
	})
	if err != nil {
		return err
	}
	result := issueSplitResult{
		ParentIssueID: parentIssueID,
		ChildIssueID:  createResult.IssueID,
		Created:       true,
		Start:         startResult,
		Advice: issueSplitAdvice{
			StatusCommand:    fmt.Sprintf("az orchestrate status --root %s", parentIssueID),
			WatchCommand:     fmt.Sprintf("az orchestrate watch --root %s --since 0 --jsonl", parentIssueID),
			IntegrateCommand: fmt.Sprintf("az orchestrate integrate --issue %s", createResult.IssueID),
			MergeCommand:     fmt.Sprintf("az branch merge %s", createResult.IssueID),
			CloseCommand:     fmt.Sprintf("az orchestrate close-session --issue %s", createResult.IssueID),
			Summary:          "Child work runs in an isolated session/worktree. Keep the parent/orchestrator watching with az orchestrate watch in another pane/session while workers are active; do not use --once for orchestration monitoring. It is not auto-merged; the parent/orchestrator should review, integrate, merge, close the child session, then close the child issue.",
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

	fmt.Printf("Created child issue: %s (parent: %s)\n", result.ChildIssueID, result.ParentIssueID)
	fmt.Println("Integration model:")
	fmt.Println("- Child work runs in its own az/tmux session/worktree.")
	fmt.Println("- It is not auto-merged; review and integrate it from the parent/orchestrator session when ready.")
	printOrchestrateStartResult(startResult)
	fmt.Println("When the child is ready:")
	fmt.Printf("- %s\n", result.Advice.IntegrateCommand)
	fmt.Printf("- %s\n", result.Advice.MergeCommand)
	fmt.Printf("- %s\n", result.Advice.CloseCommand)
	fmt.Printf("- az issue close %s\n", result.ChildIssueID)
	if len(startResult.Failed) > 0 {
		return fmt.Errorf("issue split created %s but session launch failed", createResult.IssueID)
	}
	return nil
}
