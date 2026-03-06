package main

import (
	"fmt"
	"io"
	"sort"
	"time"

	"github.com/riordanpawley/azedarach/internal/domain"
)

const (
	statsCommandName = "stats"
	statsUsage       = "Usage: az stats [--json]"

	statsInvalidArgumentCode = "invalid_argument"
	statsInternalErrorCode   = "internal_error"

	statsInvalidArgRemediation = "Run az stats [--json]"
	statsInternalRemediation   = "Retry the command and inspect stderr diagnostics"

	statsExitCodeInvalidUsage = 2
)

var (
	statsCommandPath                   = []string{"az", "stats"}
	newStatsTaskQuery taskQueryFactory = func() taskQueryFunc {
		return newTaskQuery()
	}

	statsKnownStatusOrder = []string{
		string(domain.StatusOpen),
		string(domain.StatusInProgress),
		string(domain.StatusBlocked),
		string(domain.StatusDone),
	}
	statsKnownPriorityOrder = []string{
		domain.P0.String(),
		domain.P1.String(),
		domain.P2.String(),
		domain.P3.String(),
		domain.P4.String(),
	}
	statsKnownTypeOrder = []string{
		string(domain.TypeTask),
		string(domain.TypeBug),
		string(domain.TypeFeature),
		string(domain.TypeEpic),
		string(domain.TypeChore),
	}
)

type statsResult struct {
	Total      int            `json:"total"`
	ByStatus   map[string]int `json:"byStatus"`
	ByPriority map[string]int `json:"byPriority"`
	ByType     map[string]int `json:"byType"`
}

func handleStatsCommand(args []string, stdout, stderr io.Writer) int {
	startedAt := time.Now()

	jsonMode, parseErr := parseNoPositionalArgs(args)
	if parseErr != nil {
		return handleStatsInvalidUsage(
			parseErr,
			jsonMode,
			stdout,
			stderr,
			showExecutionMeta(startedAt),
			showProjectContext(),
		)
	}

	query := newStatsTaskQuery()
	if query == nil {
		return handleStatsInternalError(
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
		return handleStatsInternalError(
			err,
			jsonMode,
			stdout,
			stderr,
			showExecutionMeta(startedAt),
			showProjectContext(),
		)
	}

	result := aggregateStats(tasks)
	if jsonMode {
		envelope := NewH2SuccessEnvelope(
			statsCommandName,
			statsCommandPath,
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

	printStatsHuman(stdout, result)
	return 0
}

func aggregateStats(tasks []domain.Task) statsResult {
	statusCounts := make(map[string]int, len(statsKnownStatusOrder))
	priorityCounts := make(map[string]int, len(statsKnownPriorityOrder))
	typeCounts := make(map[string]int, len(statsKnownTypeOrder))

	for _, status := range statsKnownStatusOrder {
		statusCounts[status] = 0
	}
	for _, priority := range statsKnownPriorityOrder {
		priorityCounts[priority] = 0
	}
	for _, taskType := range statsKnownTypeOrder {
		typeCounts[taskType] = 0
	}

	for _, task := range tasks {
		statusKey := string(task.Status)
		statusCounts[statusKey]++

		priorityKey := listPriorityString(task.Priority)
		priorityCounts[priorityKey]++

		typeKey := string(task.Type)
		typeCounts[typeKey]++
	}

	return statsResult{
		Total:      len(tasks),
		ByStatus:   statusCounts,
		ByPriority: priorityCounts,
		ByType:     typeCounts,
	}
}

func printStatsHuman(stdout io.Writer, result statsResult) {
	fmt.Fprintf(stdout, "total %d\n", result.Total)
	writeStatsCountSection(stdout, "status", statsKnownStatusOrder, result.ByStatus)
	writeStatsCountSection(stdout, "priority", statsKnownPriorityOrder, result.ByPriority)
	writeStatsCountSection(stdout, "type", statsKnownTypeOrder, result.ByType)
}

func writeStatsCountSection(
	stdout io.Writer,
	section string,
	knownOrder []string,
	counts map[string]int,
) {
	for _, key := range knownOrder {
		fmt.Fprintf(stdout, "%s.%s %d\n", section, key, counts[key])
	}

	known := make(map[string]struct{}, len(knownOrder))
	for _, key := range knownOrder {
		known[key] = struct{}{}
	}

	extras := make([]string, 0)
	for key := range counts {
		if _, ok := known[key]; ok {
			continue
		}
		extras = append(extras, key)
	}
	sort.Strings(extras)

	for _, key := range extras {
		fmt.Fprintf(stdout, "%s.%s %d\n", section, key, counts[key])
	}
}

func handleStatsInvalidUsage(
	parseErr error,
	jsonMode bool,
	stdout io.Writer,
	stderr io.Writer,
	meta H2Meta,
	project H2ProjectContext,
) int {
	if jsonMode {
		errorPayload := NewH1Error(
			statsInvalidArgumentCode,
			parseErr.Error(),
			statsInvalidArgRemediation,
			nil,
		)
		envelope := NewH2FailureEnvelope(statsCommandName, statsCommandPath, project, meta, errorPayload)
		if err := writeJSON(stdout, envelope); err != nil {
			fmt.Fprintf(stderr, "Error: %v\n", err)
			return 1
		}
		return statsExitCodeInvalidUsage
	}

	fmt.Fprintln(stderr, statsUsage)
	return statsExitCodeInvalidUsage
}

func handleStatsInternalError(
	commandErr error,
	jsonMode bool,
	stdout io.Writer,
	stderr io.Writer,
	meta H2Meta,
	project H2ProjectContext,
) int {
	if jsonMode {
		errorPayload := NewH1Error(
			statsInternalErrorCode,
			commandErr.Error(),
			statsInternalRemediation,
			nil,
		)
		envelope := NewH2FailureEnvelope(statsCommandName, statsCommandPath, project, meta, errorPayload)
		if err := writeJSON(stdout, envelope); err != nil {
			fmt.Fprintf(stderr, "Error: %v\n", err)
			return 1
		}
		return 1
	}

	fmt.Fprintf(stderr, "Error: %v\n", commandErr)
	return 1
}
