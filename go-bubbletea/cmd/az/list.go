package main

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/riordanpawley/azedarach/internal/cli"
	"github.com/riordanpawley/azedarach/internal/domain"
)

const (
	listCommandName = "list"
	listUsage       = "Usage: az list [--json]"

	listInvalidArgumentCode = "invalid_argument"
	listInternalErrorCode   = "internal_error"

	listInvalidArgRemediation = "Run az list [--json] with no positional arguments"
	listInternalRemediation   = "Retry the command and inspect stderr diagnostics"

	listExitCodeInvalidUsage = 2
)

var listCommandPath = []string{"az", "list"}

type listFetchFunc func() ([]domain.Task, error)
type listFetcherFactory func() listFetchFunc

var newListFetcher listFetcherFactory = defaultListFetcherFactory

type listParsedArgs struct {
	JSONMode bool
}

type listResult struct {
	Items []listItem `json:"items"`
}

type listItem struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Status   string `json:"status"`
	Priority string `json:"priority"`
	Type     string `json:"type"`
}

func handleListCommand(args []string, stdout, stderr io.Writer) int {
	startedAt := time.Now()

	parsedArgs, parseErr := parseListArgs(args)
	if parseErr != nil {
		return handleListInvalidUsage(
			parseErr,
			parsedArgs.JSONMode,
			stdout,
			stderr,
			showExecutionMeta(startedAt),
			showProjectContext(),
		)
	}

	fetcher := newListFetcher()
	if fetcher == nil {
		return handleListInternalError(
			fmt.Errorf("list fetcher not configured"),
			parsedArgs.JSONMode,
			stdout,
			stderr,
			showExecutionMeta(startedAt),
			showProjectContext(),
		)
	}

	tasks, err := fetcher()
	if err != nil {
		return handleListInternalError(
			err,
			parsedArgs.JSONMode,
			stdout,
			stderr,
			showExecutionMeta(startedAt),
			showProjectContext(),
		)
	}

	result := listTasksToResult(tasks)
	if parsedArgs.JSONMode {
		envelope := NewH2SuccessEnvelope(
			listCommandName,
			listCommandPath,
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

	for _, item := range result.Items {
		fmt.Fprintf(
			stdout,
			"%s %s %s %s %s\n",
			item.ID,
			item.Status,
			item.Priority,
			item.Type,
			item.Title,
		)
	}
	return 0
}

func parseListArgs(args []string) (listParsedArgs, error) {
	parsed := listParsedArgs{}
	positionals := make([]string, 0)
	unknownFlags := make([]string, 0)

	for _, arg := range args {
		switch {
		case arg == "--json":
			parsed.JSONMode = true
		case strings.HasPrefix(arg, "-"):
			unknownFlags = append(unknownFlags, arg)
		default:
			positionals = append(positionals, arg)
		}
	}

	if len(unknownFlags) > 0 {
		return parsed, fmt.Errorf("unknown flag(s): %s", strings.Join(unknownFlags, " "))
	}
	if len(positionals) > 0 {
		return parsed, fmt.Errorf("unsupported positional args: %s", strings.Join(positionals, " "))
	}

	return parsed, nil
}

func defaultListFetcherFactory() listFetchFunc {
	cfg, err := loadConfig()
	if err != nil {
		return func() ([]domain.Task, error) {
			return nil, fmt.Errorf("failed to load config: %w", err)
		}
	}

	deps, err := cli.NewDependencies(cfg)
	if err != nil {
		return func() ([]domain.Task, error) {
			return nil, fmt.Errorf("failed to initialize dependencies: %w", err)
		}
	}

	return func() ([]domain.Task, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return deps.IssueClient.List(ctx)
	}
}

func listTasksToResult(tasks []domain.Task) listResult {
	items := make([]listItem, 0, len(tasks))
	for _, task := range tasks {
		items = append(items, listTaskToItem(task))
	}
	return listResult{Items: items}
}

func listTaskToItem(task domain.Task) listItem {
	return listItem{
		ID:       task.ID,
		Title:    task.Title,
		Status:   string(task.Status),
		Priority: listPriorityString(task.Priority),
		Type:     string(task.Type),
	}
}

func listPriorityString(priority domain.Priority) string {
	priorityValue := int(priority)
	if priorityValue < int(domain.P0) || priorityValue > int(domain.P4) {
		return fmt.Sprintf("P%d", priorityValue)
	}
	return priority.String()
}

func handleListInvalidUsage(
	parseErr error,
	jsonMode bool,
	stdout io.Writer,
	stderr io.Writer,
	meta H2Meta,
	project H2ProjectContext,
) int {
	if jsonMode {
		errorPayload := NewH1Error(
			listInvalidArgumentCode,
			parseErr.Error(),
			listInvalidArgRemediation,
			nil,
		)
		envelope := NewH2FailureEnvelope(listCommandName, listCommandPath, project, meta, errorPayload)
		if err := writeJSON(stdout, envelope); err != nil {
			fmt.Fprintf(stderr, "Error: %v\n", err)
			return 1
		}
		return listExitCodeInvalidUsage
	}

	fmt.Fprintln(stderr, listUsage)
	return listExitCodeInvalidUsage
}

func handleListInternalError(
	commandErr error,
	jsonMode bool,
	stdout io.Writer,
	stderr io.Writer,
	meta H2Meta,
	project H2ProjectContext,
) int {
	if jsonMode {
		errorPayload := NewH1Error(
			listInternalErrorCode,
			commandErr.Error(),
			listInternalRemediation,
			nil,
		)
		envelope := NewH2FailureEnvelope(listCommandName, listCommandPath, project, meta, errorPayload)
		if err := writeJSON(stdout, envelope); err != nil {
			fmt.Fprintf(stderr, "Error: %v\n", err)
			return 1
		}
		return 1
	}

	fmt.Fprintf(stderr, "Error: %v\n", commandErr)
	return 1
}
