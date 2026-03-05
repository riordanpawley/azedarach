package main

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/riordanpawley/azedarach/internal/cli"
)

const (
	deleteCommandName = "delete"
	deleteUsage       = "Usage: az delete <issue-id> [--json]"

	deleteInvalidArgumentCode = "invalid_argument"
	deleteInternalErrorCode   = "internal_error"

	deleteInvalidArgRemediation = "Run az delete <issue-id> [--json]"
	deleteInternalRemediation   = "Retry the command and inspect stderr diagnostics"

	deleteExitCodeInvalidUsage = 2
)

var deleteCommandPath = []string{"az", "delete"}

type issueDeleteFunc func(issueID string) error
type issueDeleterFactory func() issueDeleteFunc

var newIssueDeleter issueDeleterFactory = defaultIssueDeleterFactory

type deleteParsedArgs struct {
	IssueID  string
	JSONMode bool
}

type deleteResult struct {
	ID        string `json:"id"`
	Operation string `json:"operation"`
}

func handleDeleteCommand(args []string, stdout, stderr io.Writer) int {
	startedAt := time.Now()

	parsedArgs, parseErr := parseDeleteArgs(args)
	if parseErr != nil {
		return handleDeleteInvalidUsage(
			parseErr,
			parsedArgs.JSONMode,
			stdout,
			stderr,
			showExecutionMeta(startedAt),
			showProjectContext(),
		)
	}

	deleter := newIssueDeleter()
	if deleter == nil {
		return handleDeleteInternalError(
			fmt.Errorf("issue deleter not configured"),
			parsedArgs.JSONMode,
			stdout,
			stderr,
			showExecutionMeta(startedAt),
			showProjectContext(),
		)
	}

	if err := deleter(parsedArgs.IssueID); err != nil {
		return handleDeleteInternalError(
			err,
			parsedArgs.JSONMode,
			stdout,
			stderr,
			showExecutionMeta(startedAt),
			showProjectContext(),
		)
	}

	result := deleteResult{
		ID:        parsedArgs.IssueID,
		Operation: deleteCommandName,
	}

	if parsedArgs.JSONMode {
		envelope := NewH2SuccessEnvelope(
			deleteCommandName,
			deleteCommandPath,
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

	fmt.Fprintf(stdout, "%s %s\n", result.Operation, result.ID)
	return 0
}

func parseDeleteArgs(args []string) (deleteParsedArgs, error) {
	parsed := deleteParsedArgs{}
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

func defaultIssueDeleterFactory() issueDeleteFunc {
	cfg, err := loadConfig()
	if err != nil {
		return func(_ string) error {
			return fmt.Errorf("failed to load config: %w", err)
		}
	}

	deps, err := cli.NewDependencies(cfg)
	if err != nil {
		return func(_ string) error {
			return fmt.Errorf("failed to initialize dependencies: %w", err)
		}
	}

	return func(issueID string) error {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return deps.BeadsClient.Delete(ctx, issueID)
	}
}

func handleDeleteInvalidUsage(
	parseErr error,
	jsonMode bool,
	stdout io.Writer,
	stderr io.Writer,
	meta H2Meta,
	project H2ProjectContext,
) int {
	if jsonMode {
		errorPayload := NewH1Error(
			deleteInvalidArgumentCode,
			parseErr.Error(),
			deleteInvalidArgRemediation,
			nil,
		)
		envelope := NewH2FailureEnvelope(deleteCommandName, deleteCommandPath, project, meta, errorPayload)
		if err := writeJSON(stdout, envelope); err != nil {
			fmt.Fprintf(stderr, "Error: %v\n", err)
			return 1
		}
		return deleteExitCodeInvalidUsage
	}

	fmt.Fprintln(stderr, deleteUsage)
	return deleteExitCodeInvalidUsage
}

func handleDeleteInternalError(
	commandErr error,
	jsonMode bool,
	stdout io.Writer,
	stderr io.Writer,
	meta H2Meta,
	project H2ProjectContext,
) int {
	if jsonMode {
		errorPayload := NewH1Error(
			deleteInternalErrorCode,
			commandErr.Error(),
			deleteInternalRemediation,
			nil,
		)
		envelope := NewH2FailureEnvelope(deleteCommandName, deleteCommandPath, project, meta, errorPayload)
		if err := writeJSON(stdout, envelope); err != nil {
			fmt.Fprintf(stderr, "Error: %v\n", err)
			return 1
		}
		return 1
	}

	fmt.Fprintf(stderr, "Error: %v\n", commandErr)
	return 1
}
