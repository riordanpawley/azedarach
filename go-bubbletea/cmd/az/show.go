package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/riordanpawley/azedarach/internal/cli"
	"github.com/riordanpawley/azedarach/internal/domain"
)

const (
	showCommandName = "show"
	showUsage       = "Usage: az show <issue-id> [--json]"

	showInvalidArgumentCode = "invalid_argument"
	showIssueNotFoundCode   = "issue_not_found"
	showInternalErrorCode   = "internal_error"

	showInvalidArgRemediation = "Run az show <issue-id> [--json]"
	showNotFoundRemediation   = "Run az list --json to inspect available issues"
	showInternalRemediation   = "Retry the command and inspect stderr diagnostics"

	showIssueNotFoundMessageTemplate = "Issue not found internally nor externally: %s"

	showExitCodeInvalidUsage = 2
	showExitCodeNotFound     = 3
)

var showCommandPath = []string{"az", "show"}

type showSearchFunc func(issueID string) ([]domain.Task, error)
type showSearcherFactory func() showSearchFunc

var newShowSearcher showSearcherFactory = defaultShowSearcherFactory

type showParsedArgs struct {
	IssueID  string
	JSONMode bool
}

type showResult struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Status   string `json:"status"`
	Priority string `json:"priority"`
	Type     string `json:"type"`
}

func handleShowCommand(args []string, stdout, stderr io.Writer) int {
	startedAt := time.Now()

	parsedArgs, parseErr := parseShowArgs(args)
	if parseErr != nil {
		return handleShowInvalidUsage(
			parseErr,
			parsedArgs.JSONMode,
			stdout,
			stderr,
			showExecutionMeta(startedAt),
			showProjectContext(),
		)
	}

	searcher := newShowSearcher()
	if searcher == nil {
		return handleShowInternalError(
			fmt.Errorf("show searcher not configured"),
			parsedArgs.JSONMode,
			stdout,
			stderr,
			showExecutionMeta(startedAt),
			showProjectContext(),
		)
	}

	tasks, err := searcher(parsedArgs.IssueID)
	if err != nil {
		return handleShowInternalError(
			err,
			parsedArgs.JSONMode,
			stdout,
			stderr,
			showExecutionMeta(startedAt),
			showProjectContext(),
		)
	}

	task, found := findShowTaskByID(tasks, parsedArgs.IssueID)
	if !found {
		notFoundMessage := fmt.Sprintf(showIssueNotFoundMessageTemplate, parsedArgs.IssueID)
		notFoundError := NewH1Error(
			showIssueNotFoundCode,
			notFoundMessage,
			showNotFoundRemediation,
			map[string]any{"issueId": parsedArgs.IssueID},
		)

		if parsedArgs.JSONMode {
			envelope := NewH2FailureEnvelope(
				showCommandName,
				showCommandPath,
				showProjectContext(),
				showExecutionMeta(startedAt),
				notFoundError,
			)
			if err := writeJSON(stdout, envelope); err != nil {
				fmt.Fprintf(stderr, "Error: %v\n", err)
				return 1
			}
			return showExitCodeNotFound
		}

		fmt.Fprintln(stderr, notFoundMessage)
		return showExitCodeNotFound
	}

	result := taskToShowResult(task)
	if parsedArgs.JSONMode {
		envelope := NewH2SuccessEnvelope(
			showCommandName,
			showCommandPath,
			showProjectContext(),
			showExecutionMeta(startedAt),
			result,
		)
		if err := writeJSON(stdout, envelope); err != nil {
			fmt.Fprintf(stderr, "Error: %v\n", err)
			return 1
		}
		return 0
	}

	fmt.Fprintf(
		stdout,
		"%s %s %s %s %s\n",
		result.ID,
		result.Status,
		result.Priority,
		result.Type,
		result.Title,
	)
	return 0
}

func parseShowArgs(args []string) (showParsedArgs, error) {
	parsed := showParsedArgs{}
	issueIDs := make([]string, 0, 1)
	unknownFlags := make([]string, 0)

	for _, arg := range args {
		switch {
		case arg == "--json":
			parsed.JSONMode = true
		case strings.HasPrefix(arg, "-"):
			unknownFlags = append(unknownFlags, arg)
		default:
			issueIDs = append(issueIDs, arg)
		}
	}

	if len(unknownFlags) > 0 {
		return parsed, fmt.Errorf("unknown flag(s): %s", strings.Join(unknownFlags, " "))
	}
	if len(issueIDs) == 0 {
		return parsed, fmt.Errorf("missing required issue-id")
	}
	if len(issueIDs) > 1 {
		return parsed, fmt.Errorf("expected exactly one issue-id, got %d", len(issueIDs))
	}

	parsed.IssueID = issueIDs[0]
	return parsed, nil
}

func defaultShowSearcherFactory() showSearchFunc {
	cfg, err := loadConfig()
	if err != nil {
		return func(_ string) ([]domain.Task, error) {
			return nil, fmt.Errorf("failed to load config: %w", err)
		}
	}

	deps, err := cli.NewDependencies(cfg)
	if err != nil {
		return func(_ string) ([]domain.Task, error) {
			return nil, fmt.Errorf("failed to initialize dependencies: %w", err)
		}
	}

	return func(issueID string) ([]domain.Task, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return deps.BeadsClient.Search(ctx, issueID)
	}
}

func findShowTaskByID(tasks []domain.Task, issueID string) (domain.Task, bool) {
	for _, task := range tasks {
		if strings.EqualFold(task.ID, issueID) {
			return task, true
		}
	}
	return domain.Task{}, false
}

func taskToShowResult(task domain.Task) showResult {
	return showResult{
		ID:       task.ID,
		Title:    task.Title,
		Status:   string(task.Status),
		Priority: showPriorityString(task.Priority),
		Type:     string(task.Type),
	}
}

func showPriorityString(priority domain.Priority) string {
	priorityValue := int(priority)
	if priorityValue < int(domain.P0) || priorityValue > int(domain.P4) {
		return fmt.Sprintf("P%d", priorityValue)
	}
	return priority.String()
}

func handleShowInvalidUsage(
	parseErr error,
	jsonMode bool,
	stdout io.Writer,
	stderr io.Writer,
	meta H2Meta,
	project H2ProjectContext,
) int {
	if jsonMode {
		errorPayload := NewH1Error(
			showInvalidArgumentCode,
			parseErr.Error(),
			showInvalidArgRemediation,
			nil,
		)
		envelope := NewH2FailureEnvelope(showCommandName, showCommandPath, project, meta, errorPayload)
		if err := writeJSON(stdout, envelope); err != nil {
			fmt.Fprintf(stderr, "Error: %v\n", err)
			return 1
		}
		return showExitCodeInvalidUsage
	}

	fmt.Fprintln(stderr, showUsage)
	return showExitCodeInvalidUsage
}

func handleShowInternalError(
	commandErr error,
	jsonMode bool,
	stdout io.Writer,
	stderr io.Writer,
	meta H2Meta,
	project H2ProjectContext,
) int {
	if jsonMode {
		errorPayload := NewH1Error(
			showInternalErrorCode,
			commandErr.Error(),
			showInternalRemediation,
			nil,
		)
		envelope := NewH2FailureEnvelope(showCommandName, showCommandPath, project, meta, errorPayload)
		if err := writeJSON(stdout, envelope); err != nil {
			fmt.Fprintf(stderr, "Error: %v\n", err)
			return 1
		}
		return 1
	}

	fmt.Fprintf(stderr, "Error: %v\n", commandErr)
	return 1
}

func showProjectContext() H2ProjectContext {
	cwd, err := os.Getwd()
	if err != nil {
		return H2ProjectContext{}
	}

	projectID := filepath.Base(cwd)
	return H2ProjectContext{
		ID:              projectID,
		Path:            cwd,
		CanonicalDBPath: filepath.Join(cwd, ".azedarach", "azedarach.db"),
	}
}

func showExecutionMeta(startedAt time.Time) H2Meta {
	return H2Meta{
		DurationMs: time.Since(startedAt).Milliseconds(),
		At:         time.Now().UTC().Format(time.RFC3339),
	}
}
