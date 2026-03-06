package main

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/riordanpawley/azedarach/internal/cli"
	"github.com/riordanpawley/azedarach/internal/domain"
)

const (
	readyCommandName   = "ready"
	blockedCommandName = "blocked"
	searchCommandName  = "search"
	staleCommandName   = "stale"
	countCommandName   = "count"

	readyUsage   = "Usage: az ready [--json]"
	blockedUsage = "Usage: az blocked [--json]"
	searchUsage  = "Usage: az search <query> [--json]"
	staleUsage   = "Usage: az stale [--json]"
	countUsage   = "Usage: az count [--json]"

	queryInvalidArgumentCode = "invalid_argument"
	queryInternalErrorCode   = "internal_error"

	readyInvalidArgRemediation   = "Run az ready [--json]"
	blockedInvalidArgRemediation = "Run az blocked [--json]"
	searchInvalidArgRemediation  = "Run az search <query> [--json]"
	staleInvalidArgRemediation   = "Run az stale [--json]"
	countInvalidArgRemediation   = "Run az count [--json]"
	queryInternalRemediation     = "Retry the command and inspect stderr diagnostics"

	queryExitCodeInvalidUsage = 2
)

var (
	readyCommandPath   = []string{"az", "ready"}
	blockedCommandPath = []string{"az", "blocked"}
	searchCommandPath  = []string{"az", "search"}
	staleCommandPath   = []string{"az", "stale"}
	countCommandPath   = []string{"az", "count"}
)

type taskQueryFunc func() ([]domain.Task, error)
type taskQueryFactory func() taskQueryFunc

type searchQueryFunc func(query string) ([]domain.Task, error)
type searchQueryFactory func() searchQueryFunc

var newTaskQuery taskQueryFactory = defaultTaskQueryFactory
var newSearchQuery searchQueryFactory = defaultSearchQueryFactory

type queryCommandSpec struct {
	Name                  string
	Path                  []string
	Usage                 string
	InvalidArgRemediation string
}

type countResult struct {
	Count int `json:"count"`
}

func handleReadyCommand(args []string, stdout, stderr io.Writer) int {
	return handleTaskListQueryCommand(
		args,
		stdout,
		stderr,
		queryCommandSpec{
			Name:                  readyCommandName,
			Path:                  readyCommandPath,
			Usage:                 readyUsage,
			InvalidArgRemediation: readyInvalidArgRemediation,
		},
		func(tasks []domain.Task) []domain.Task {
			filtered := make([]domain.Task, 0, len(tasks))
			for _, task := range tasks {
				if task.Status == domain.StatusOpen {
					filtered = append(filtered, task)
				}
			}
			return filtered
		},
	)
}

func handleBlockedCommand(args []string, stdout, stderr io.Writer) int {
	return handleTaskListQueryCommand(
		args,
		stdout,
		stderr,
		queryCommandSpec{
			Name:                  blockedCommandName,
			Path:                  blockedCommandPath,
			Usage:                 blockedUsage,
			InvalidArgRemediation: blockedInvalidArgRemediation,
		},
		func(tasks []domain.Task) []domain.Task {
			filtered := make([]domain.Task, 0, len(tasks))
			for _, task := range tasks {
				if task.Status == domain.StatusBlocked {
					filtered = append(filtered, task)
				}
			}
			return filtered
		},
	)
}

func handleStaleCommand(args []string, stdout, stderr io.Writer) int {
	return handleTaskListQueryCommand(
		args,
		stdout,
		stderr,
		queryCommandSpec{
			Name:                  staleCommandName,
			Path:                  staleCommandPath,
			Usage:                 staleUsage,
			InvalidArgRemediation: staleInvalidArgRemediation,
		},
		func(tasks []domain.Task) []domain.Task {
			filtered := make([]domain.Task, 0, len(tasks))
			for _, task := range tasks {
				if task.Status != domain.StatusDone {
					filtered = append(filtered, task)
				}
			}
			sort.SliceStable(filtered, func(i, j int) bool {
				return filtered[i].UpdatedAt.Before(filtered[j].UpdatedAt)
			})
			return filtered
		},
	)
}

func handleSearchCommand(args []string, stdout, stderr io.Writer) int {
	startedAt := time.Now()
	query, jsonMode, parseErr := parseSearchArgs(args)
	if parseErr != nil {
		return handleQueryInvalidUsage(
			queryCommandSpec{
				Name:                  searchCommandName,
				Path:                  searchCommandPath,
				Usage:                 searchUsage,
				InvalidArgRemediation: searchInvalidArgRemediation,
			},
			parseErr,
			jsonMode,
			stdout,
			stderr,
			showExecutionMeta(startedAt),
			showProjectContext(),
		)
	}

	searcher := newSearchQuery()
	if searcher == nil {
		return handleQueryInternalError(
			queryCommandSpec{Name: searchCommandName, Path: searchCommandPath},
			fmt.Errorf("search query dependency not configured"),
			jsonMode,
			stdout,
			stderr,
			showExecutionMeta(startedAt),
			showProjectContext(),
		)
	}

	tasks, err := searcher(query)
	if err != nil {
		return handleQueryInternalError(
			queryCommandSpec{Name: searchCommandName, Path: searchCommandPath},
			err,
			jsonMode,
			stdout,
			stderr,
			showExecutionMeta(startedAt),
			showProjectContext(),
		)
	}

	return writeTaskListQueryResult(
		queryCommandSpec{Name: searchCommandName, Path: searchCommandPath},
		tasks,
		jsonMode,
		stdout,
		stderr,
		showExecutionMeta(startedAt),
		showProjectContext(),
	)
}

func handleCountCommand(args []string, stdout, stderr io.Writer) int {
	startedAt := time.Now()
	jsonMode, parseErr := parseNoPositionalArgs(args)
	if parseErr != nil {
		return handleQueryInvalidUsage(
			queryCommandSpec{
				Name:                  countCommandName,
				Path:                  countCommandPath,
				Usage:                 countUsage,
				InvalidArgRemediation: countInvalidArgRemediation,
			},
			parseErr,
			jsonMode,
			stdout,
			stderr,
			showExecutionMeta(startedAt),
			showProjectContext(),
		)
	}

	query := newTaskQuery()
	if query == nil {
		return handleQueryInternalError(
			queryCommandSpec{Name: countCommandName, Path: countCommandPath},
			fmt.Errorf("task query dependency not configured"),
			jsonMode,
			stdout,
			stderr,
			showExecutionMeta(startedAt),
			showProjectContext(),
		)
	}

	tasks, err := query()
	if err != nil {
		return handleQueryInternalError(
			queryCommandSpec{Name: countCommandName, Path: countCommandPath},
			err,
			jsonMode,
			stdout,
			stderr,
			showExecutionMeta(startedAt),
			showProjectContext(),
		)
	}

	result := countResult{Count: len(tasks)}
	if jsonMode {
		envelope := NewH2SuccessEnvelope(
			countCommandName,
			countCommandPath,
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

	fmt.Fprintf(stdout, "%d\n", result.Count)
	return 0
}

func handleTaskListQueryCommand(
	args []string,
	stdout io.Writer,
	stderr io.Writer,
	spec queryCommandSpec,
	filter func([]domain.Task) []domain.Task,
) int {
	startedAt := time.Now()
	jsonMode, parseErr := parseNoPositionalArgs(args)
	if parseErr != nil {
		return handleQueryInvalidUsage(
			spec,
			parseErr,
			jsonMode,
			stdout,
			stderr,
			showExecutionMeta(startedAt),
			showProjectContext(),
		)
	}

	query := newTaskQuery()
	if query == nil {
		return handleQueryInternalError(
			spec,
			fmt.Errorf("task query dependency not configured"),
			jsonMode,
			stdout,
			stderr,
			showExecutionMeta(startedAt),
			showProjectContext(),
		)
	}

	tasks, err := query()
	if err != nil {
		return handleQueryInternalError(
			spec,
			err,
			jsonMode,
			stdout,
			stderr,
			showExecutionMeta(startedAt),
			showProjectContext(),
		)
	}

	filtered := filter(tasks)
	return writeTaskListQueryResult(
		spec,
		filtered,
		jsonMode,
		stdout,
		stderr,
		showExecutionMeta(startedAt),
		showProjectContext(),
	)
}

func writeTaskListQueryResult(
	spec queryCommandSpec,
	tasks []domain.Task,
	jsonMode bool,
	stdout io.Writer,
	stderr io.Writer,
	meta H2Meta,
	project H2ProjectContext,
) int {
	result := listTasksToResult(tasks)
	if jsonMode {
		envelope := NewH2SuccessEnvelope(spec.Name, spec.Path, project, meta, result)
		if err := writeJSON(stdout, envelope); err != nil {
			fmt.Fprintf(stderr, "Error: %v\n", err)
			return 1
		}
		return 0
	}

	for _, item := range result.Items {
		fmt.Fprintf(stdout, "%s %s %s %s %s\n", item.ID, item.Status, item.Priority, item.Type, item.Title)
	}
	return 0
}

func parseNoPositionalArgs(args []string) (bool, error) {
	jsonMode := false
	positionals := make([]string, 0)
	unknownFlags := make([]string, 0)
	for _, arg := range args {
		switch {
		case arg == "--json":
			jsonMode = true
		case strings.HasPrefix(arg, "-"):
			unknownFlags = append(unknownFlags, arg)
		default:
			positionals = append(positionals, arg)
		}
	}
	if len(unknownFlags) > 0 {
		return jsonMode, fmt.Errorf("unknown flag(s): %s", strings.Join(unknownFlags, " "))
	}
	if len(positionals) > 0 {
		return jsonMode, fmt.Errorf("unsupported positional args: %s", strings.Join(positionals, " "))
	}
	return jsonMode, nil
}

func parseSearchArgs(args []string) (string, bool, error) {
	jsonMode := false
	positionals := make([]string, 0)
	unknownFlags := make([]string, 0)
	for _, arg := range args {
		switch {
		case arg == "--json":
			jsonMode = true
		case strings.HasPrefix(arg, "-"):
			unknownFlags = append(unknownFlags, arg)
		default:
			positionals = append(positionals, arg)
		}
	}
	if len(unknownFlags) > 0 {
		return "", jsonMode, fmt.Errorf("unknown flag(s): %s", strings.Join(unknownFlags, " "))
	}
	if len(positionals) == 0 {
		return "", jsonMode, fmt.Errorf("missing required query")
	}
	query := strings.TrimSpace(strings.Join(positionals, " "))
	if query == "" {
		return "", jsonMode, fmt.Errorf("missing required query")
	}
	return query, jsonMode, nil
}

func defaultTaskQueryFactory() taskQueryFunc {
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

func defaultSearchQueryFactory() searchQueryFunc {
	cfg, err := loadConfig()
	if err != nil {
		return func(string) ([]domain.Task, error) {
			return nil, fmt.Errorf("failed to load config: %w", err)
		}
	}

	deps, err := cli.NewDependencies(cfg)
	if err != nil {
		return func(string) ([]domain.Task, error) {
			return nil, fmt.Errorf("failed to initialize dependencies: %w", err)
		}
	}

	return func(query string) ([]domain.Task, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return deps.IssueClient.Search(ctx, query)
	}
}

func handleQueryInvalidUsage(
	spec queryCommandSpec,
	parseErr error,
	jsonMode bool,
	stdout io.Writer,
	stderr io.Writer,
	meta H2Meta,
	project H2ProjectContext,
) int {
	if jsonMode {
		errorPayload := NewH1Error(
			queryInvalidArgumentCode,
			parseErr.Error(),
			spec.InvalidArgRemediation,
			nil,
		)
		envelope := NewH2FailureEnvelope(spec.Name, spec.Path, project, meta, errorPayload)
		if err := writeJSON(stdout, envelope); err != nil {
			fmt.Fprintf(stderr, "Error: %v\n", err)
			return 1
		}
		return queryExitCodeInvalidUsage
	}

	fmt.Fprintln(stderr, spec.Usage)
	return queryExitCodeInvalidUsage
}

func handleQueryInternalError(
	spec queryCommandSpec,
	commandErr error,
	jsonMode bool,
	stdout io.Writer,
	stderr io.Writer,
	meta H2Meta,
	project H2ProjectContext,
) int {
	if jsonMode {
		errorPayload := NewH1Error(
			queryInternalErrorCode,
			commandErr.Error(),
			queryInternalRemediation,
			nil,
		)
		envelope := NewH2FailureEnvelope(spec.Name, spec.Path, project, meta, errorPayload)
		if err := writeJSON(stdout, envelope); err != nil {
			fmt.Fprintf(stderr, "Error: %v\n", err)
			return 1
		}
		return 1
	}

	fmt.Fprintf(stderr, "Error: %v\n", commandErr)
	return 1
}
