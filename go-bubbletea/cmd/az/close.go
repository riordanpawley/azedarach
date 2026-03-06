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
	closeCommandName  = "close"
	reopenCommandName = "reopen"

	closeUsage  = "Usage: az close <issue-id> [--json]"
	reopenUsage = "Usage: az reopen <issue-id> [--json]"

	issueStateInvalidArgumentCode = "invalid_argument"
	issueStateInternalErrorCode   = "internal_error"

	closeInvalidArgumentRemediation  = "Run az close <issue-id> [--json]"
	reopenInvalidArgumentRemediation = "Run az reopen <issue-id> [--json]"
	issueStateInternalRemediation    = "Retry the command and inspect stderr diagnostics"

	issueStateExitCodeInvalidUsage = 2
)

var (
	closeCommandPath  = []string{"az", "close"}
	reopenCommandPath = []string{"az", "reopen"}
)

type issueStateMutateFunc func(operation, issueID string) error
type issueStateMutatorFactory func() issueStateMutateFunc

var newIssueStateMutator issueStateMutatorFactory = defaultIssueStateMutatorFactory

type issueStateParsedArgs struct {
	IssueID  string
	JSONMode bool
}

type issueStateResult struct {
	ID        string `json:"id"`
	Operation string `json:"operation"`
}

type issueStateCommandSpec struct {
	Name                  string
	Path                  []string
	Usage                 string
	Operation             string
	InvalidArgRemediation string
}

func handleCloseCommand(args []string, stdout, stderr io.Writer) int {
	return handleIssueStateCommand(
		args,
		stdout,
		stderr,
		issueStateCommandSpec{
			Name:                  closeCommandName,
			Path:                  closeCommandPath,
			Usage:                 closeUsage,
			Operation:             closeCommandName,
			InvalidArgRemediation: closeInvalidArgumentRemediation,
		},
	)
}

func handleReopenCommand(args []string, stdout, stderr io.Writer) int {
	return handleIssueStateCommand(
		args,
		stdout,
		stderr,
		issueStateCommandSpec{
			Name:                  reopenCommandName,
			Path:                  reopenCommandPath,
			Usage:                 reopenUsage,
			Operation:             reopenCommandName,
			InvalidArgRemediation: reopenInvalidArgumentRemediation,
		},
	)
}

func handleIssueStateCommand(
	args []string,
	stdout io.Writer,
	stderr io.Writer,
	spec issueStateCommandSpec,
) int {
	startedAt := time.Now()

	parsedArgs, parseErr := parseIssueStateArgs(args)
	if parseErr != nil {
		return handleIssueStateInvalidUsage(
			spec,
			parseErr,
			parsedArgs.JSONMode,
			stdout,
			stderr,
			showExecutionMeta(startedAt),
			showProjectContext(),
		)
	}

	mutator := newIssueStateMutator()
	if mutator == nil {
		return handleIssueStateInternalError(
			spec,
			fmt.Errorf("issue state mutator not configured"),
			parsedArgs.JSONMode,
			stdout,
			stderr,
			showExecutionMeta(startedAt),
			showProjectContext(),
		)
	}

	if err := mutator(spec.Operation, parsedArgs.IssueID); err != nil {
		return handleIssueStateInternalError(
			spec,
			err,
			parsedArgs.JSONMode,
			stdout,
			stderr,
			showExecutionMeta(startedAt),
			showProjectContext(),
		)
	}

	result := issueStateResult{
		ID:        parsedArgs.IssueID,
		Operation: spec.Operation,
	}

	if parsedArgs.JSONMode {
		envelope := NewH2SuccessEnvelope(
			spec.Name,
			spec.Path,
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

func parseIssueStateArgs(args []string) (issueStateParsedArgs, error) {
	parsed := issueStateParsedArgs{}
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

func defaultIssueStateMutatorFactory() issueStateMutateFunc {
	cfg, err := loadConfig()
	if err != nil {
		return func(_, _ string) error {
			return fmt.Errorf("failed to load config: %w", err)
		}
	}

	deps, err := cli.NewDependencies(cfg)
	if err != nil {
		return func(_, _ string) error {
			return fmt.Errorf("failed to initialize dependencies: %w", err)
		}
	}

	return func(operation, issueID string) error {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		switch operation {
		case closeCommandName:
			return deps.IssueClient.Close(ctx, issueID, "")
		case reopenCommandName:
			return deps.IssueClient.Update(ctx, issueID, domain.StatusOpen)
		default:
			return fmt.Errorf("unsupported issue state operation: %s", operation)
		}
	}
}

func handleIssueStateInvalidUsage(
	spec issueStateCommandSpec,
	parseErr error,
	jsonMode bool,
	stdout io.Writer,
	stderr io.Writer,
	meta H2Meta,
	project H2ProjectContext,
) int {
	if jsonMode {
		errorPayload := NewH1Error(
			issueStateInvalidArgumentCode,
			parseErr.Error(),
			spec.InvalidArgRemediation,
			nil,
		)
		envelope := NewH2FailureEnvelope(spec.Name, spec.Path, project, meta, errorPayload)
		if err := writeJSON(stdout, envelope); err != nil {
			fmt.Fprintf(stderr, "Error: %v\n", err)
			return 1
		}
		return issueStateExitCodeInvalidUsage
	}

	fmt.Fprintln(stderr, spec.Usage)
	return issueStateExitCodeInvalidUsage
}

func handleIssueStateInternalError(
	spec issueStateCommandSpec,
	commandErr error,
	jsonMode bool,
	stdout io.Writer,
	stderr io.Writer,
	meta H2Meta,
	project H2ProjectContext,
) int {
	if jsonMode {
		errorPayload := NewH1Error(
			issueStateInternalErrorCode,
			commandErr.Error(),
			issueStateInternalRemediation,
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
