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
	updateCommandName = "update"
	updateUsage       = "Usage: az update <issue-id> --status <open|in_progress|blocked|closed> [--json]"

	updateInvalidArgumentCode = "invalid_argument"
	updateInternalErrorCode   = "internal_error"

	updateInvalidArgRemediation = "Run az update <issue-id> --status <open|in_progress|blocked|closed> [--json]"
	updateInternalRemediation   = "Retry the command and inspect stderr diagnostics"

	updateExitCodeInvalidUsage = 2
)

var updateCommandPath = []string{"az", "update"}

type issueUpdateFunc func(issueID string, status domain.Status) error
type issueUpdaterFactory func() issueUpdateFunc

var newIssueUpdater issueUpdaterFactory = defaultIssueUpdaterFactory

type updateParsedArgs struct {
	IssueID  string
	Status   domain.Status
	JSONMode bool
}

type updateResult struct {
	ID     string `json:"id"`
	Status string `json:"status"`
}

func handleUpdateCommand(args []string, stdout, stderr io.Writer) int {
	startedAt := time.Now()

	parsedArgs, parseErr := parseUpdateArgs(args)
	if parseErr != nil {
		return handleUpdateInvalidUsage(
			parseErr,
			parsedArgs.JSONMode,
			stdout,
			stderr,
			showExecutionMeta(startedAt),
			showProjectContext(),
		)
	}

	updater := newIssueUpdater()
	if updater == nil {
		return handleUpdateInternalError(
			fmt.Errorf("issue updater not configured"),
			parsedArgs.JSONMode,
			stdout,
			stderr,
			showExecutionMeta(startedAt),
			showProjectContext(),
		)
	}

	if err := updater(parsedArgs.IssueID, parsedArgs.Status); err != nil {
		return handleUpdateInternalError(
			err,
			parsedArgs.JSONMode,
			stdout,
			stderr,
			showExecutionMeta(startedAt),
			showProjectContext(),
		)
	}

	result := updateResult{
		ID:     parsedArgs.IssueID,
		Status: string(parsedArgs.Status),
	}

	if parsedArgs.JSONMode {
		envelope := NewH2SuccessEnvelope(
			updateCommandName,
			updateCommandPath,
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

	fmt.Fprintf(stdout, "%s %s\n", result.ID, result.Status)
	return 0
}

func parseUpdateArgs(args []string) (updateParsedArgs, error) {
	parsed := updateParsedArgs{}
	issueIDs := make([]string, 0, 1)
	unknownFlags := make([]string, 0)
	statusSet := false

	// Pre-scan JSON mode so validation errors can still emit JSON envelopes.
	for _, arg := range args {
		if arg == "--json" {
			parsed.JSONMode = true
		}
	}

	for index := 0; index < len(args); index++ {
		arg := args[index]

		switch {
		case arg == "--json":
			continue

		case arg == "--status":
			if statusSet {
				return parsed, fmt.Errorf("duplicate --status flag")
			}
			if index+1 >= len(args) {
				return parsed, fmt.Errorf("missing value for --status")
			}

			value := args[index+1]
			if strings.HasPrefix(value, "-") {
				return parsed, fmt.Errorf("missing value for --status")
			}

			status, ok := parseUpdateStatus(value)
			if !ok {
				return parsed, fmt.Errorf("invalid --status value: %s", value)
			}

			parsed.Status = status
			statusSet = true
			index++

		case strings.HasPrefix(arg, "--status="):
			if statusSet {
				return parsed, fmt.Errorf("duplicate --status flag")
			}

			value := strings.TrimPrefix(arg, "--status=")
			if value == "" {
				return parsed, fmt.Errorf("missing value for --status")
			}

			status, ok := parseUpdateStatus(value)
			if !ok {
				return parsed, fmt.Errorf("invalid --status value: %s", value)
			}

			parsed.Status = status
			statusSet = true

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
	if !statusSet {
		return parsed, fmt.Errorf("missing required --status")
	}

	parsed.IssueID = issueIDs[0]
	return parsed, nil
}

func parseUpdateStatus(value string) (domain.Status, bool) {
	switch value {
	case string(domain.StatusOpen):
		return domain.StatusOpen, true
	case string(domain.StatusInProgress):
		return domain.StatusInProgress, true
	case string(domain.StatusBlocked):
		return domain.StatusBlocked, true
	case string(domain.StatusDone):
		return domain.StatusDone, true
	default:
		return "", false
	}
}

func defaultIssueUpdaterFactory() issueUpdateFunc {
	cfg, err := loadConfig()
	if err != nil {
		return func(_ string, _ domain.Status) error {
			return fmt.Errorf("failed to load config: %w", err)
		}
	}

	deps, err := cli.NewDependencies(cfg)
	if err != nil {
		return func(_ string, _ domain.Status) error {
			return fmt.Errorf("failed to initialize dependencies: %w", err)
		}
	}

	return func(issueID string, status domain.Status) error {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return deps.BeadsClient.Update(ctx, issueID, status)
	}
}

func handleUpdateInvalidUsage(
	parseErr error,
	jsonMode bool,
	stdout io.Writer,
	stderr io.Writer,
	meta H2Meta,
	project H2ProjectContext,
) int {
	if jsonMode {
		errorPayload := NewH1Error(
			updateInvalidArgumentCode,
			parseErr.Error(),
			updateInvalidArgRemediation,
			nil,
		)
		envelope := NewH2FailureEnvelope(updateCommandName, updateCommandPath, project, meta, errorPayload)
		if err := writeJSON(stdout, envelope); err != nil {
			fmt.Fprintf(stderr, "Error: %v\n", err)
			return 1
		}
		return updateExitCodeInvalidUsage
	}

	fmt.Fprintln(stderr, updateUsage)
	return updateExitCodeInvalidUsage
}

func handleUpdateInternalError(
	commandErr error,
	jsonMode bool,
	stdout io.Writer,
	stderr io.Writer,
	meta H2Meta,
	project H2ProjectContext,
) int {
	if jsonMode {
		errorPayload := NewH1Error(
			updateInternalErrorCode,
			commandErr.Error(),
			updateInternalRemediation,
			nil,
		)
		envelope := NewH2FailureEnvelope(updateCommandName, updateCommandPath, project, meta, errorPayload)
		if err := writeJSON(stdout, envelope); err != nil {
			fmt.Fprintf(stderr, "Error: %v\n", err)
			return 1
		}
		return 1
	}

	fmt.Fprintf(stderr, "Error: %v\n", commandErr)
	return 1
}
