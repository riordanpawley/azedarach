package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"time"
)

const (
	depCommandName       = "dep"
	depAddCommandName    = "dep.add"
	depRemoveCommandName = "dep.remove"
	depListCommandName   = "dep.list"
	depTreeCommandName   = "dep.tree"
	depCyclesCommandName = "dep.cycles"

	depUsage = "Usage:\n  az dep add <source-id> <target-id> --type <relation-type> [--json]\n  az dep remove <source-id> <target-id> --type <relation-type> [--json]\n  az dep list <issue-id> [--json]\n  az dep tree <issue-id> [--json]\n  az dep cycles [--json]"

	depAddUsage    = "Usage: az dep add <source-id> <target-id> --type <relation-type> [--json]"
	depRemoveUsage = "Usage: az dep remove <source-id> <target-id> --type <relation-type> [--json]"
	depListUsage   = "Usage: az dep list <issue-id> [--json]"
	depTreeUsage   = "Usage: az dep tree <issue-id> [--json]"
	depCyclesUsage = "Usage: az dep cycles [--json]"

	depInvalidArgumentCode = "invalid_argument"
	depBackendErrorCode    = "backend_error"
	depInternalErrorCode   = "internal_error"

	depInvalidArgRemediation       = "Run az dep <add|remove|list|tree|cycles> [args] [--json]"
	depAddInvalidArgRemediation    = "Run az dep add <source-id> <target-id> --type <relation-type> [--json]"
	depRemoveInvalidArgRemediation = "Run az dep remove <source-id> <target-id> --type <relation-type> [--json]"
	depListInvalidArgRemediation   = "Run az dep list <issue-id> [--json]"
	depTreeInvalidArgRemediation   = "Run az dep tree <issue-id> [--json]"
	depCyclesInvalidArgRemediation = "Run az dep cycles [--json]"

	depBackendRemediation  = "Retry the command and inspect backend stderr diagnostics"
	depInternalRemediation = "Retry the command and inspect stderr diagnostics"

	depExitCodeInvalidUsage   = 2
	depExitCodeBackendFailure = 3

	depSubcommandAdd    = "add"
	depSubcommandRemove = "remove"
	depSubcommandList   = "list"
	depSubcommandTree   = "tree"
	depSubcommandCycles = "cycles"
)

var (
	depCommandPath       = []string{"az", "dep"}
	depAddCommandPath    = []string{"az", "dep", "add"}
	depRemoveCommandPath = []string{"az", "dep", "remove"}
	depListCommandPath   = []string{"az", "dep", "list"}
	depTreeCommandPath   = []string{"az", "dep", "tree"}
	depCyclesCommandPath = []string{"az", "dep", "cycles"}

	depSpec = depCommandSpec{
		Name:                  depCommandName,
		Path:                  depCommandPath,
		Usage:                 depUsage,
		InvalidArgRemediation: depInvalidArgRemediation,
	}
	depAddSpec = depCommandSpec{
		Name:                  depAddCommandName,
		Path:                  depAddCommandPath,
		Usage:                 depAddUsage,
		InvalidArgRemediation: depAddInvalidArgRemediation,
	}
	depRemoveSpec = depCommandSpec{
		Name:                  depRemoveCommandName,
		Path:                  depRemoveCommandPath,
		Usage:                 depRemoveUsage,
		InvalidArgRemediation: depRemoveInvalidArgRemediation,
	}
	depListSpec = depCommandSpec{
		Name:                  depListCommandName,
		Path:                  depListCommandPath,
		Usage:                 depListUsage,
		InvalidArgRemediation: depListInvalidArgRemediation,
	}
	depTreeSpec = depCommandSpec{
		Name:                  depTreeCommandName,
		Path:                  depTreeCommandPath,
		Usage:                 depTreeUsage,
		InvalidArgRemediation: depTreeInvalidArgRemediation,
	}
	depCyclesSpec = depCommandSpec{
		Name:                  depCyclesCommandName,
		Path:                  depCyclesCommandPath,
		Usage:                 depCyclesUsage,
		InvalidArgRemediation: depCyclesInvalidArgRemediation,
	}
)

type depCommandSpec struct {
	Name                  string
	Path                  []string
	Usage                 string
	InvalidArgRemediation string
}

type depAddRemoveParsedArgs struct {
	SourceID     string
	TargetID     string
	RelationType string
	JSONMode     bool
}

type depIssueParsedArgs struct {
	IssueID  string
	JSONMode bool
}

type depCyclesParsedArgs struct {
	JSONMode bool
}

type depSuccessResult struct {
	Data json.RawMessage `json:"data"`
}

type depBackendExecutor func(args []string) ([]byte, error)

// depBackendExecutorHook exists so tests can stub backend invocations.
var depBackendExecutorHook depBackendExecutor = defaultDepBackendExecutor

type depBackendCommandError struct {
	ExitCode int
	Stderr   string
	Message  string
}

func (err depBackendCommandError) Error() string {
	if err.Message != "" {
		return err.Message
	}
	if err.ExitCode > 0 {
		if err.Stderr != "" {
			return fmt.Sprintf("backend command failed (exit %d): %s", err.ExitCode, err.Stderr)
		}
		return fmt.Sprintf("backend command failed (exit %d)", err.ExitCode)
	}
	if err.Stderr != "" {
		return fmt.Sprintf("backend command failed: %s", err.Stderr)
	}
	return "backend command failed"
}

func handleDepCommand(args []string, stdout, stderr io.Writer) int {
	startedAt := time.Now()

	if len(args) == 0 {
		return handleDepInvalidUsage(
			depSpec,
			fmt.Errorf("missing required subcommand"),
			false,
			stdout,
			stderr,
			showExecutionMeta(startedAt),
			showProjectContext(),
		)
	}

	subcommand := args[0]
	subcommandArgs := args[1:]

	switch subcommand {
	case depSubcommandAdd:
		parsedArgs, parseErr := parseDepAddRemoveArgs(subcommandArgs)
		if parseErr != nil {
			return handleDepInvalidUsage(
				depAddSpec,
				parseErr,
				parsedArgs.JSONMode,
				stdout,
				stderr,
				showExecutionMeta(startedAt),
				showProjectContext(),
			)
		}
		return runDepAdd(parsedArgs, stdout, stderr, showExecutionMeta(startedAt), showProjectContext())

	case depSubcommandRemove:
		parsedArgs, parseErr := parseDepAddRemoveArgs(subcommandArgs)
		if parseErr != nil {
			return handleDepInvalidUsage(
				depRemoveSpec,
				parseErr,
				parsedArgs.JSONMode,
				stdout,
				stderr,
				showExecutionMeta(startedAt),
				showProjectContext(),
			)
		}
		return runDepRemove(parsedArgs, stdout, stderr, showExecutionMeta(startedAt), showProjectContext())

	case depSubcommandList:
		parsedArgs, parseErr := parseDepIssueArgs(subcommandArgs)
		if parseErr != nil {
			return handleDepInvalidUsage(
				depListSpec,
				parseErr,
				parsedArgs.JSONMode,
				stdout,
				stderr,
				showExecutionMeta(startedAt),
				showProjectContext(),
			)
		}
		return runDepList(parsedArgs, stdout, stderr, showExecutionMeta(startedAt), showProjectContext())

	case depSubcommandTree:
		parsedArgs, parseErr := parseDepIssueArgs(subcommandArgs)
		if parseErr != nil {
			return handleDepInvalidUsage(
				depTreeSpec,
				parseErr,
				parsedArgs.JSONMode,
				stdout,
				stderr,
				showExecutionMeta(startedAt),
				showProjectContext(),
			)
		}
		return runDepTree(parsedArgs, stdout, stderr, showExecutionMeta(startedAt), showProjectContext())

	case depSubcommandCycles:
		parsedArgs, parseErr := parseDepCyclesArgs(subcommandArgs)
		if parseErr != nil {
			return handleDepInvalidUsage(
				depCyclesSpec,
				parseErr,
				parsedArgs.JSONMode,
				stdout,
				stderr,
				showExecutionMeta(startedAt),
				showProjectContext(),
			)
		}
		return runDepCycles(parsedArgs, stdout, stderr, showExecutionMeta(startedAt), showProjectContext())

	default:
		return handleDepInvalidUsage(
			depSpec,
			fmt.Errorf("unsupported subcommand: %s", subcommand),
			depHasJSONFlag(args),
			stdout,
			stderr,
			showExecutionMeta(startedAt),
			showProjectContext(),
		)
	}
}

func parseDepAddRemoveArgs(args []string) (depAddRemoveParsedArgs, error) {
	parsed := depAddRemoveParsedArgs{
		JSONMode: depHasJSONFlag(args),
	}

	if len(args) < 4 {
		return parsed, fmt.Errorf("expected <source-id> <target-id> --type <relation-type>")
	}
	if len(args) > 5 {
		return parsed, fmt.Errorf("too many arguments")
	}
	if len(args) == 5 && args[4] != "--json" {
		return parsed, fmt.Errorf("unexpected argument: %s", args[4])
	}
	if args[2] != "--type" {
		return parsed, fmt.Errorf("expected --type flag")
	}

	sourceID := strings.TrimSpace(args[0])
	targetID := strings.TrimSpace(args[1])
	relationType := strings.TrimSpace(args[3])

	if sourceID == "" || strings.HasPrefix(sourceID, "-") {
		return parsed, fmt.Errorf("missing required source-id")
	}
	if targetID == "" || strings.HasPrefix(targetID, "-") {
		return parsed, fmt.Errorf("missing required target-id")
	}
	if relationType == "" || strings.HasPrefix(relationType, "-") {
		return parsed, fmt.Errorf("missing value for --type")
	}

	parsed.SourceID = sourceID
	parsed.TargetID = targetID
	parsed.RelationType = relationType
	parsed.JSONMode = len(args) == 5
	return parsed, nil
}

func parseDepIssueArgs(args []string) (depIssueParsedArgs, error) {
	parsed := depIssueParsedArgs{
		JSONMode: depHasJSONFlag(args),
	}

	if len(args) == 0 {
		return parsed, fmt.Errorf("missing required issue-id")
	}
	if len(args) > 2 {
		return parsed, fmt.Errorf("too many arguments")
	}
	if len(args) == 2 && args[1] != "--json" {
		return parsed, fmt.Errorf("unexpected argument: %s", args[1])
	}

	issueID := strings.TrimSpace(args[0])
	if issueID == "" || strings.HasPrefix(issueID, "-") {
		return parsed, fmt.Errorf("missing required issue-id")
	}

	parsed.IssueID = issueID
	parsed.JSONMode = len(args) == 2
	return parsed, nil
}

func parseDepCyclesArgs(args []string) (depCyclesParsedArgs, error) {
	parsed := depCyclesParsedArgs{
		JSONMode: depHasJSONFlag(args),
	}

	if len(args) == 0 {
		return parsed, nil
	}
	if len(args) == 1 && args[0] == "--json" {
		parsed.JSONMode = true
		return parsed, nil
	}
	if len(args) == 1 && strings.HasPrefix(args[0], "-") {
		return parsed, fmt.Errorf("unexpected flag: %s", args[0])
	}
	return parsed, fmt.Errorf("cycles does not accept positional arguments")
}

func runDepAdd(
	args depAddRemoveParsedArgs,
	stdout io.Writer,
	stderr io.Writer,
	meta H2Meta,
	project H2ProjectContext,
) int {
	backendArgs := []string{
		depSubcommandAdd,
		args.SourceID,
		args.TargetID,
		"--type",
		args.RelationType,
	}
	humanOutput := fmt.Sprintf("added %s %s %s", args.SourceID, args.TargetID, args.RelationType)
	return runDepOperation(depAddSpec, backendArgs, args.JSONMode, humanOutput, stdout, stderr, meta, project)
}

func runDepRemove(
	args depAddRemoveParsedArgs,
	stdout io.Writer,
	stderr io.Writer,
	meta H2Meta,
	project H2ProjectContext,
) int {
	backendArgs := []string{
		depSubcommandRemove,
		args.SourceID,
		args.TargetID,
		"--type",
		args.RelationType,
	}
	humanOutput := fmt.Sprintf("removed %s %s %s", args.SourceID, args.TargetID, args.RelationType)
	return runDepOperation(depRemoveSpec, backendArgs, args.JSONMode, humanOutput, stdout, stderr, meta, project)
}

func runDepList(
	args depIssueParsedArgs,
	stdout io.Writer,
	stderr io.Writer,
	meta H2Meta,
	project H2ProjectContext,
) int {
	backendArgs := []string{
		depSubcommandList,
		args.IssueID,
	}
	return runDepOperation(depListSpec, backendArgs, args.JSONMode, "", stdout, stderr, meta, project)
}

func runDepTree(
	args depIssueParsedArgs,
	stdout io.Writer,
	stderr io.Writer,
	meta H2Meta,
	project H2ProjectContext,
) int {
	backendArgs := []string{
		depSubcommandTree,
		args.IssueID,
	}
	return runDepOperation(depTreeSpec, backendArgs, args.JSONMode, "", stdout, stderr, meta, project)
}

func runDepCycles(
	args depCyclesParsedArgs,
	stdout io.Writer,
	stderr io.Writer,
	meta H2Meta,
	project H2ProjectContext,
) int {
	backendArgs := []string{depSubcommandCycles}
	return runDepOperation(depCyclesSpec, backendArgs, args.JSONMode, "", stdout, stderr, meta, project)
}

func runDepOperation(
	spec depCommandSpec,
	backendArgs []string,
	jsonMode bool,
	humanOutput string,
	stdout io.Writer,
	stderr io.Writer,
	meta H2Meta,
	project H2ProjectContext,
) int {
	if depBackendExecutorHook == nil {
		return handleDepInternalError(
			spec,
			fmt.Errorf("dep backend executor not configured"),
			jsonMode,
			stdout,
			stderr,
			meta,
			project,
		)
	}

	payload, err := depBackendExecutorHook(cloneStrings(backendArgs))
	if err != nil {
		return handleDepBackendError(spec, err, backendArgs, jsonMode, stdout, stderr, meta, project)
	}

	normalizedPayload, err := normalizeDepBackendPayload(payload)
	if err != nil {
		return handleDepBackendError(spec, err, backendArgs, jsonMode, stdout, stderr, meta, project)
	}

	if jsonMode {
		result := depSuccessResult{
			Data: normalizedPayload,
		}
		envelope := NewH2SuccessEnvelope(spec.Name, spec.Path, project, meta, result)
		if err := writeJSON(stdout, envelope); err != nil {
			fmt.Fprintf(stderr, "Error: %v\n", err)
			return 1
		}
		return 0
	}

	if humanOutput != "" {
		fmt.Fprintln(stdout, humanOutput)
		return 0
	}

	fmt.Fprintln(stdout, string(normalizedPayload))
	return 0
}

func normalizeDepBackendPayload(payload []byte) (json.RawMessage, error) {
	trimmed := bytes.TrimSpace(payload)
	if len(trimmed) == 0 {
		return nil, fmt.Errorf("backend returned empty JSON payload")
	}

	var decoded any
	if err := json.Unmarshal(trimmed, &decoded); err != nil {
		return nil, fmt.Errorf("backend returned invalid JSON payload: %w", err)
	}

	normalized, err := json.Marshal(decoded)
	if err != nil {
		return nil, fmt.Errorf("failed to normalize backend JSON payload: %w", err)
	}

	return json.RawMessage(normalized), nil
}

func defaultDepBackendExecutor(args []string) ([]byte, error) {
	commandArgs := depBackendCommandArgs(args)
	cmd := exec.Command("bd", commandArgs...)

	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	output, err := cmd.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return nil, depBackendCommandError{
				ExitCode: exitErr.ExitCode(),
				Stderr:   strings.TrimSpace(stderr.String()),
			}
		}
		return nil, depBackendCommandError{
			Message: fmt.Sprintf("failed to execute backend command: %v", err),
			Stderr:  strings.TrimSpace(stderr.String()),
		}
	}

	return output, nil
}

func depBackendCommandArgs(args []string) []string {
	commandArgs := make([]string, 0, len(args)+2)
	commandArgs = append(commandArgs, "dep")
	commandArgs = append(commandArgs, cloneStrings(args)...)
	commandArgs = append(commandArgs, "--json")
	return commandArgs
}

func depBackendDisplayCommand(args []string) []string {
	display := make([]string, 0, len(args)+3)
	display = append(display, "bd")
	display = append(display, depBackendCommandArgs(args)...)
	return display
}

func depHasJSONFlag(args []string) bool {
	for _, arg := range args {
		if arg == "--json" {
			return true
		}
	}
	return false
}

func handleDepInvalidUsage(
	spec depCommandSpec,
	parseErr error,
	jsonMode bool,
	stdout io.Writer,
	stderr io.Writer,
	meta H2Meta,
	project H2ProjectContext,
) int {
	if jsonMode {
		errorPayload := NewH1Error(
			depInvalidArgumentCode,
			parseErr.Error(),
			spec.InvalidArgRemediation,
			nil,
		)
		envelope := NewH2FailureEnvelope(spec.Name, spec.Path, project, meta, errorPayload)
		if err := writeJSON(stdout, envelope); err != nil {
			fmt.Fprintf(stderr, "Error: %v\n", err)
			return 1
		}
		return depExitCodeInvalidUsage
	}

	fmt.Fprintln(stderr, spec.Usage)
	return depExitCodeInvalidUsage
}

func handleDepBackendError(
	spec depCommandSpec,
	commandErr error,
	backendArgs []string,
	jsonMode bool,
	stdout io.Writer,
	stderr io.Writer,
	meta H2Meta,
	project H2ProjectContext,
) int {
	errorMessage := commandErr.Error()
	errorDetails := map[string]any{
		"command": depBackendDisplayCommand(backendArgs),
	}

	var backendErr depBackendCommandError
	if errors.As(commandErr, &backendErr) {
		if backendErr.Message != "" {
			errorMessage = backendErr.Message
		}
		if backendErr.ExitCode != 0 {
			errorDetails["exitCode"] = backendErr.ExitCode
		}
		if backendErr.Stderr != "" {
			errorDetails["stderr"] = backendErr.Stderr
		}
	}

	if jsonMode {
		errorPayload := NewH1Error(
			depBackendErrorCode,
			errorMessage,
			depBackendRemediation,
			errorDetails,
		)
		envelope := NewH2FailureEnvelope(spec.Name, spec.Path, project, meta, errorPayload)
		if err := writeJSON(stdout, envelope); err != nil {
			fmt.Fprintf(stderr, "Error: %v\n", err)
			return 1
		}
		return depExitCodeBackendFailure
	}

	fmt.Fprintf(stderr, "Error: %s\n", errorMessage)
	return depExitCodeBackendFailure
}

func handleDepInternalError(
	spec depCommandSpec,
	commandErr error,
	jsonMode bool,
	stdout io.Writer,
	stderr io.Writer,
	meta H2Meta,
	project H2ProjectContext,
) int {
	if jsonMode {
		errorPayload := NewH1Error(
			depInternalErrorCode,
			commandErr.Error(),
			depInternalRemediation,
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
