package main

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/riordanpawley/azedarach/internal/cli"
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/services/beads"
)

const (
	createCommandName       = "create"
	quickCaptureCommandName = "q"

	createUsage       = "Usage: az create <title> [--json]"
	quickCaptureUsage = "Usage: az q <text> [--json]"

	createInvalidArgumentCode = "invalid_argument"
	createInternalErrorCode   = "internal_error"

	createInvalidArgRemediation       = "Run az create <title> [--json]"
	quickCaptureInvalidArgRemediation = "Run az q <text> [--json]"
	createInternalRemediation         = "Retry the command and inspect stderr diagnostics"

	createExitCodeInvalidUsage = 2
)

var (
	createCommandPath       = []string{"az", "create"}
	quickCaptureCommandPath = []string{"az", "q"}
)

type issueCreateFunc func(title string) (string, error)
type issueCreatorFactory func() issueCreateFunc

var newIssueCreator issueCreatorFactory = defaultIssueCreatorFactory

type createParsedArgs struct {
	Title    string
	JSONMode bool
}

type createResult struct {
	ID    string `json:"id"`
	Title string `json:"title"`
}

type createCommandSpec struct {
	Name                  string
	Path                  []string
	Usage                 string
	RequiredArgumentName  string
	InvalidArgRemediation string
}

func handleCreateCommand(args []string, stdout, stderr io.Writer) int {
	return handleIssueCreationCommand(
		args,
		stdout,
		stderr,
		createCommandSpec{
			Name:                  createCommandName,
			Path:                  createCommandPath,
			Usage:                 createUsage,
			RequiredArgumentName:  "title",
			InvalidArgRemediation: createInvalidArgRemediation,
		},
	)
}

func handleQuickCaptureCommand(args []string, stdout, stderr io.Writer) int {
	return handleIssueCreationCommand(
		args,
		stdout,
		stderr,
		createCommandSpec{
			Name:                  quickCaptureCommandName,
			Path:                  quickCaptureCommandPath,
			Usage:                 quickCaptureUsage,
			RequiredArgumentName:  "text",
			InvalidArgRemediation: quickCaptureInvalidArgRemediation,
		},
	)
}

func handleIssueCreationCommand(
	args []string,
	stdout io.Writer,
	stderr io.Writer,
	spec createCommandSpec,
) int {
	startedAt := time.Now()

	parsedArgs, parseErr := parseCreateArgs(args, spec.RequiredArgumentName)
	if parseErr != nil {
		return handleCreateInvalidUsage(
			spec,
			parseErr,
			parsedArgs.JSONMode,
			stdout,
			stderr,
			showExecutionMeta(startedAt),
			showProjectContext(),
		)
	}

	creator := newIssueCreator()
	if creator == nil {
		return handleCreateInternalError(
			spec,
			fmt.Errorf("issue creator not configured"),
			parsedArgs.JSONMode,
			stdout,
			stderr,
			showExecutionMeta(startedAt),
			showProjectContext(),
		)
	}

	issueID, err := creator(parsedArgs.Title)
	if err != nil {
		return handleCreateInternalError(
			spec,
			err,
			parsedArgs.JSONMode,
			stdout,
			stderr,
			showExecutionMeta(startedAt),
			showProjectContext(),
		)
	}
	if strings.TrimSpace(issueID) == "" {
		return handleCreateInternalError(
			spec,
			fmt.Errorf("issue creator returned empty id"),
			parsedArgs.JSONMode,
			stdout,
			stderr,
			showExecutionMeta(startedAt),
			showProjectContext(),
		)
	}

	result := createResult{
		ID:    issueID,
		Title: parsedArgs.Title,
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

	fmt.Fprintf(stdout, "%s %s\n", result.ID, result.Title)
	return 0
}

func parseCreateArgs(args []string, requiredArgumentName string) (createParsedArgs, error) {
	parsed := createParsedArgs{}
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

	title := strings.TrimSpace(strings.Join(positionals, " "))
	if title == "" {
		return parsed, fmt.Errorf("missing required %s", requiredArgumentName)
	}

	parsed.Title = title
	return parsed, nil
}

func defaultIssueCreatorFactory() issueCreateFunc {
	cfg, err := loadConfig()
	if err != nil {
		return func(_ string) (string, error) {
			return "", fmt.Errorf("failed to load config: %w", err)
		}
	}

	deps, err := cli.NewDependencies(cfg)
	if err != nil {
		return func(_ string) (string, error) {
			return "", fmt.Errorf("failed to initialize dependencies: %w", err)
		}
	}

	return func(title string) (string, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		return deps.BeadsClient.Create(ctx, beads.CreateTaskParams{
			Title:    title,
			Type:     domain.TypeTask,
			Priority: domain.P2,
		})
	}
}

func handleCreateInvalidUsage(
	spec createCommandSpec,
	parseErr error,
	jsonMode bool,
	stdout io.Writer,
	stderr io.Writer,
	meta H2Meta,
	project H2ProjectContext,
) int {
	if jsonMode {
		errorPayload := NewH1Error(
			createInvalidArgumentCode,
			parseErr.Error(),
			spec.InvalidArgRemediation,
			nil,
		)
		envelope := NewH2FailureEnvelope(spec.Name, spec.Path, project, meta, errorPayload)
		if err := writeJSON(stdout, envelope); err != nil {
			fmt.Fprintf(stderr, "Error: %v\n", err)
			return 1
		}
		return createExitCodeInvalidUsage
	}

	fmt.Fprintln(stderr, spec.Usage)
	return createExitCodeInvalidUsage
}

func handleCreateInternalError(
	spec createCommandSpec,
	commandErr error,
	jsonMode bool,
	stdout io.Writer,
	stderr io.Writer,
	meta H2Meta,
	project H2ProjectContext,
) int {
	if jsonMode {
		errorPayload := NewH1Error(
			createInternalErrorCode,
			commandErr.Error(),
			createInternalRemediation,
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
