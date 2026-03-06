package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	initCommandName = "init"
	initUsage       = "Usage: az init [--json]"

	initInvalidArgumentCode = "invalid_argument"
	initInternalErrorCode   = "internal_error"

	initInvalidArgRemediation = "Run az init [--json] with no positional arguments or unknown flags"
	initInternalRemediation   = "Retry the command and inspect stderr diagnostics"

	initExitCodeInvalidUsage = 2

	initArtifactDirectory = ".azedarach"
	initArtifactDatabase  = ".azedarach/azedarach.db"
	initArtifactGitkeep   = ".azedarach/.gitkeep"

	initArtifactStatusCreated  = "created"
	initArtifactStatusExisting = "existing"
)

var initCommandPath = []string{"az", "init"}

type initStatFunc func(name string) (os.FileInfo, error)
type initMkdirAllFunc func(path string, perm os.FileMode) error
type initOpenFileFunc func(name string, flag int, perm os.FileMode) (*os.File, error)

var (
	initGetwd                     = os.Getwd
	initStat     initStatFunc     = os.Stat
	initMkdirAll initMkdirAllFunc = os.MkdirAll
	initOpenFile initOpenFileFunc = os.OpenFile
)

type initParsedArgs struct {
	JSONMode bool
}

type initArtifactResult struct {
	Path   string `json:"path"`
	Status string `json:"status"`
}

type initResult struct {
	Artifacts     []initArtifactResult `json:"artifacts"`
	CreatedPaths  []string             `json:"createdPaths"`
	ExistingPaths []string             `json:"existingPaths"`
}

func handleInitCommand(args []string, stdout, stderr io.Writer) int {
	startedAt := time.Now()

	parsedArgs, parseErr := parseInitArgs(args)
	if parseErr != nil {
		return handleInitInvalidUsage(
			parseErr,
			parsedArgs.JSONMode,
			stdout,
			stderr,
			showExecutionMeta(startedAt),
			showProjectContext(),
		)
	}

	cwd, err := initGetwd()
	if err != nil {
		return handleInitInternalError(
			fmt.Errorf("failed to resolve current working directory: %w", err),
			parsedArgs.JSONMode,
			stdout,
			stderr,
			showExecutionMeta(startedAt),
			showProjectContext(),
		)
	}

	result, err := initializeWorkspaceArtifacts(cwd)
	if err != nil {
		return handleInitInternalError(
			err,
			parsedArgs.JSONMode,
			stdout,
			stderr,
			showExecutionMeta(startedAt),
			showProjectContext(),
		)
	}

	if parsedArgs.JSONMode {
		envelope := NewH2SuccessEnvelope(
			initCommandName,
			initCommandPath,
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

	printInitHumanSummary(stdout, result)
	return 0
}

func parseInitArgs(args []string) (initParsedArgs, error) {
	parsed := initParsedArgs{}
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

func initializeWorkspaceArtifacts(cwd string) (initResult, error) {
	result := initResult{
		Artifacts:     make([]initArtifactResult, 0, 3),
		CreatedPaths:  make([]string, 0, 3),
		ExistingPaths: make([]string, 0, 3),
	}

	directoryPath := filepath.Join(cwd, initArtifactDirectory)
	directoryStatus, err := ensureInitDirectory(directoryPath)
	if err != nil {
		return initResult{}, err
	}
	appendInitArtifact(&result, initArtifactDirectory, directoryStatus)

	databasePath := filepath.Join(cwd, initArtifactDatabase)
	databaseStatus, err := ensureInitFile(databasePath)
	if err != nil {
		return initResult{}, err
	}
	appendInitArtifact(&result, initArtifactDatabase, databaseStatus)

	gitkeepPath := filepath.Join(cwd, initArtifactGitkeep)
	gitkeepStatus, err := ensureInitFile(gitkeepPath)
	if err != nil {
		return initResult{}, err
	}
	appendInitArtifact(&result, initArtifactGitkeep, gitkeepStatus)

	return result, nil
}

func ensureInitDirectory(path string) (string, error) {
	exists, isDir, err := initPathInfo(path)
	if err != nil {
		return "", fmt.Errorf("failed to inspect %s: %w", path, err)
	}
	if exists {
		if !isDir {
			return "", fmt.Errorf("path exists and is not a directory: %s", path)
		}
		return initArtifactStatusExisting, nil
	}

	if err := initMkdirAll(path, 0o755); err != nil {
		return "", fmt.Errorf("failed to create directory %s: %w", path, err)
	}
	return initArtifactStatusCreated, nil
}

func ensureInitFile(path string) (string, error) {
	exists, isDir, err := initPathInfo(path)
	if err != nil {
		return "", fmt.Errorf("failed to inspect %s: %w", path, err)
	}
	if exists {
		if isDir {
			return "", fmt.Errorf("path exists and is a directory: %s", path)
		}
		return initArtifactStatusExisting, nil
	}

	file, err := initOpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return initArtifactStatusExisting, nil
		}
		return "", fmt.Errorf("failed to create file %s: %w", path, err)
	}

	if err := file.Close(); err != nil {
		return "", fmt.Errorf("failed to close file %s: %w", path, err)
	}

	return initArtifactStatusCreated, nil
}

func initPathInfo(path string) (exists bool, isDirectory bool, err error) {
	info, statErr := initStat(path)
	if statErr == nil {
		return true, info.IsDir(), nil
	}
	if errors.Is(statErr, os.ErrNotExist) {
		return false, false, nil
	}
	return false, false, statErr
}

func appendInitArtifact(result *initResult, path, status string) {
	result.Artifacts = append(result.Artifacts, initArtifactResult{
		Path:   path,
		Status: status,
	})

	if status == initArtifactStatusCreated {
		result.CreatedPaths = append(result.CreatedPaths, path)
		return
	}

	result.ExistingPaths = append(result.ExistingPaths, path)
}

func printInitHumanSummary(stdout io.Writer, result initResult) {
	fmt.Fprintln(stdout, "Workspace artifacts:")
	for _, artifact := range result.Artifacts {
		fmt.Fprintf(stdout, "%s %s\n", artifact.Status, artifact.Path)
	}
}

func handleInitInvalidUsage(
	parseErr error,
	jsonMode bool,
	stdout io.Writer,
	stderr io.Writer,
	meta H2Meta,
	project H2ProjectContext,
) int {
	if jsonMode {
		errorPayload := NewH1Error(
			initInvalidArgumentCode,
			parseErr.Error(),
			initInvalidArgRemediation,
			nil,
		)
		envelope := NewH2FailureEnvelope(initCommandName, initCommandPath, project, meta, errorPayload)
		if err := writeJSON(stdout, envelope); err != nil {
			fmt.Fprintf(stderr, "Error: %v\n", err)
			return 1
		}
		return initExitCodeInvalidUsage
	}

	fmt.Fprintln(stderr, initUsage)
	return initExitCodeInvalidUsage
}

func handleInitInternalError(
	commandErr error,
	jsonMode bool,
	stdout io.Writer,
	stderr io.Writer,
	meta H2Meta,
	project H2ProjectContext,
) int {
	if jsonMode {
		errorPayload := NewH1Error(
			initInternalErrorCode,
			commandErr.Error(),
			initInternalRemediation,
			nil,
		)
		envelope := NewH2FailureEnvelope(initCommandName, initCommandPath, project, meta, errorPayload)
		if err := writeJSON(stdout, envelope); err != nil {
			fmt.Fprintf(stderr, "Error: %v\n", err)
			return 1
		}
		return 1
	}

	fmt.Fprintf(stderr, "Error: %v\n", commandErr)
	return 1
}
