package main

import (
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"time"

	"github.com/riordanpawley/azedarach/internal/config"
)

const (
	projectCommandName       = "project"
	projectAddCommandName    = "project.add"
	projectListCommandName   = "project.list"
	projectRemoveCommandName = "project.remove"
	projectSwitchCommandName = "project.switch"

	projectUsage = "Usage:\n  az project add <path> [--name <name>] [--json]\n  az project list [--json]\n  az project remove <name> [--json]\n  az project switch <name> [--json]"

	projectAddUsage    = "Usage: az project add <path> [--name <name>] [--json]"
	projectListUsage   = "Usage: az project list [--json]"
	projectRemoveUsage = "Usage: az project remove <name> [--json]"
	projectSwitchUsage = "Usage: az project switch <name> [--json]"

	projectInvalidArgumentCode = "invalid_argument"
	projectInternalErrorCode   = "internal_error"
	projectNotFoundCode        = "project_not_found"
	projectDuplicateCode       = "duplicate_project"
	projectInvalidPathCode     = "invalid_project_path"
	projectInvalidNameCode     = "invalid_project_name"

	projectInvalidArgRemediation       = "Run az project <add|list|remove|switch> [args] [--json]"
	projectAddInvalidArgRemediation    = "Run az project add <path> [--name <name>] [--json]"
	projectListInvalidArgRemediation   = "Run az project list [--json]"
	projectRemoveInvalidArgRemediation = "Run az project remove <name> [--json]"
	projectSwitchInvalidArgRemediation = "Run az project switch <name> [--json]"

	projectInternalRemediation    = "Retry the command and inspect stderr diagnostics"
	projectNotFoundRemediation    = "Run az project list [--json] to inspect registered projects"
	projectDuplicateRemediation   = "Use a different project name or remove the existing project first"
	projectInvalidPathRemediation = "Use a path to an existing git repository"
	projectInvalidNameRemediation = "Use a non-empty project name"

	projectExitCodeInvalidUsage   = 2
	projectExitCodeDomainFailure  = 3
	projectSubcommandAdd          = "add"
	projectSubcommandList         = "list"
	projectSubcommandRemove       = "remove"
	projectSubcommandSwitch       = "switch"
	projectRemoveNameArgumentName = "name"
	projectSwitchNameArgumentName = "name"
)

var (
	projectCommandPath       = []string{"az", "project"}
	projectAddCommandPath    = []string{"az", "project", "add"}
	projectListCommandPath   = []string{"az", "project", "list"}
	projectRemoveCommandPath = []string{"az", "project", "remove"}
	projectSwitchCommandPath = []string{"az", "project", "switch"}

	projectSpec = projectCommandSpec{
		Name:                  projectCommandName,
		Path:                  projectCommandPath,
		Usage:                 projectUsage,
		InvalidArgRemediation: projectInvalidArgRemediation,
	}
	projectAddSpec = projectCommandSpec{
		Name:                  projectAddCommandName,
		Path:                  projectAddCommandPath,
		Usage:                 projectAddUsage,
		InvalidArgRemediation: projectAddInvalidArgRemediation,
	}
	projectListSpec = projectCommandSpec{
		Name:                  projectListCommandName,
		Path:                  projectListCommandPath,
		Usage:                 projectListUsage,
		InvalidArgRemediation: projectListInvalidArgRemediation,
	}
	projectRemoveSpec = projectCommandSpec{
		Name:                  projectRemoveCommandName,
		Path:                  projectRemoveCommandPath,
		Usage:                 projectRemoveUsage,
		InvalidArgRemediation: projectRemoveInvalidArgRemediation,
	}
	projectSwitchSpec = projectCommandSpec{
		Name:                  projectSwitchCommandName,
		Path:                  projectSwitchCommandPath,
		Usage:                 projectSwitchUsage,
		InvalidArgRemediation: projectSwitchInvalidArgRemediation,
	}
)

type projectCommandSpec struct {
	Name                  string
	Path                  []string
	Usage                 string
	InvalidArgRemediation string
}

type projectAddParsedArgs struct {
	Path     string
	Name     string
	JSONMode bool
}

type projectListParsedArgs struct {
	JSONMode bool
}

type projectNameParsedArgs struct {
	Name     string
	JSONMode bool
}

type projectRecord struct {
	Name    string `json:"name"`
	Path    string `json:"path"`
	Default bool   `json:"default"`
}

type projectAddResult struct {
	Project projectRecord `json:"project"`
}

type projectListResult struct {
	Projects       []projectRecord `json:"projects"`
	DefaultProject string          `json:"defaultProject"`
}

type projectRemoveResult struct {
	Name           string `json:"name"`
	DefaultProject string `json:"defaultProject"`
}

type projectSwitchResult struct {
	Project projectRecord `json:"project"`
}

func handleProjectCommand(args []string, stdout, stderr io.Writer) int {
	startedAt := time.Now()

	if len(args) == 0 {
		return handleProjectInvalidUsage(
			projectSpec,
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
	case projectSubcommandAdd:
		parsedArgs, parseErr := parseProjectAddArgs(subcommandArgs)
		if parseErr != nil {
			return handleProjectInvalidUsage(
				projectAddSpec,
				parseErr,
				parsedArgs.JSONMode,
				stdout,
				stderr,
				showExecutionMeta(startedAt),
				showProjectContext(),
			)
		}
		return runProjectAdd(
			parsedArgs,
			stdout,
			stderr,
			showExecutionMeta(startedAt),
			showProjectContext(),
		)

	case projectSubcommandList:
		parsedArgs, parseErr := parseProjectListArgs(subcommandArgs)
		if parseErr != nil {
			return handleProjectInvalidUsage(
				projectListSpec,
				parseErr,
				parsedArgs.JSONMode,
				stdout,
				stderr,
				showExecutionMeta(startedAt),
				showProjectContext(),
			)
		}
		return runProjectList(
			parsedArgs,
			stdout,
			stderr,
			showExecutionMeta(startedAt),
			showProjectContext(),
		)

	case projectSubcommandRemove:
		parsedArgs, parseErr := parseProjectNameArgs(subcommandArgs, projectRemoveNameArgumentName)
		if parseErr != nil {
			return handleProjectInvalidUsage(
				projectRemoveSpec,
				parseErr,
				parsedArgs.JSONMode,
				stdout,
				stderr,
				showExecutionMeta(startedAt),
				showProjectContext(),
			)
		}
		return runProjectRemove(
			parsedArgs,
			stdout,
			stderr,
			showExecutionMeta(startedAt),
			showProjectContext(),
		)

	case projectSubcommandSwitch:
		parsedArgs, parseErr := parseProjectNameArgs(subcommandArgs, projectSwitchNameArgumentName)
		if parseErr != nil {
			return handleProjectInvalidUsage(
				projectSwitchSpec,
				parseErr,
				parsedArgs.JSONMode,
				stdout,
				stderr,
				showExecutionMeta(startedAt),
				showProjectContext(),
			)
		}
		return runProjectSwitch(
			parsedArgs,
			stdout,
			stderr,
			showExecutionMeta(startedAt),
			showProjectContext(),
		)

	default:
		return handleProjectInvalidUsage(
			projectSpec,
			fmt.Errorf("unsupported subcommand: %s", subcommand),
			projectHasJSONFlag(args),
			stdout,
			stderr,
			showExecutionMeta(startedAt),
			showProjectContext(),
		)
	}
}

func parseProjectAddArgs(args []string) (projectAddParsedArgs, error) {
	parsed := projectAddParsedArgs{}
	positionals := make([]string, 0, 1)
	unknownFlags := make([]string, 0)
	nameProvided := false

	for index := 0; index < len(args); index++ {
		arg := args[index]

		switch {
		case arg == "--json":
			parsed.JSONMode = true
		case arg == "--name":
			if nameProvided {
				return parsed, fmt.Errorf("duplicate flag: --name")
			}
			if index+1 >= len(args) {
				return parsed, fmt.Errorf("missing value for --name")
			}
			next := strings.TrimSpace(args[index+1])
			if next == "" || strings.HasPrefix(next, "-") {
				return parsed, fmt.Errorf("missing value for --name")
			}
			parsed.Name = next
			nameProvided = true
			index++
		case strings.HasPrefix(arg, "-"):
			unknownFlags = append(unknownFlags, arg)
		default:
			positionals = append(positionals, arg)
		}
	}

	if len(unknownFlags) > 0 {
		return parsed, fmt.Errorf("unknown flag(s): %s", strings.Join(unknownFlags, " "))
	}
	if len(positionals) == 0 {
		return parsed, fmt.Errorf("missing required path")
	}
	if len(positionals) > 1 {
		return parsed, fmt.Errorf("expected exactly one path, got %d", len(positionals))
	}

	parsed.Path = positionals[0]
	if !nameProvided {
		parsed.Name = filepath.Base(filepath.Clean(parsed.Path))
	}

	if strings.TrimSpace(parsed.Name) == "" {
		return parsed, fmt.Errorf("missing required name")
	}

	return parsed, nil
}

func parseProjectListArgs(args []string) (projectListParsedArgs, error) {
	parsed := projectListParsedArgs{}
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

func parseProjectNameArgs(args []string, argumentName string) (projectNameParsedArgs, error) {
	parsed := projectNameParsedArgs{}
	positionals := make([]string, 0, 1)
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
	if len(positionals) == 0 {
		return parsed, fmt.Errorf("missing required %s", argumentName)
	}
	if len(positionals) > 1 {
		return parsed, fmt.Errorf("expected exactly one %s, got %d", argumentName, len(positionals))
	}

	parsed.Name = positionals[0]
	return parsed, nil
}

func runProjectAdd(
	args projectAddParsedArgs,
	stdout io.Writer,
	stderr io.Writer,
	meta H2Meta,
	project H2ProjectContext,
) int {
	registry, err := config.LoadProjectsRegistry()
	if err != nil {
		return handleProjectInternalError(
			projectAddSpec,
			fmt.Errorf("failed to load projects registry: %w", err),
			args.JSONMode,
			stdout,
			stderr,
			meta,
			project,
		)
	}

	if err := registry.Add(args.Name, args.Path); err != nil {
		return handleProjectDomainError(
			projectAddSpec,
			err,
			map[string]any{
				"name": args.Name,
				"path": args.Path,
			},
			args.JSONMode,
			stdout,
			stderr,
			meta,
			project,
		)
	}

	if err := config.SaveProjectsRegistry(registry); err != nil {
		return handleProjectInternalError(
			projectAddSpec,
			fmt.Errorf("failed to save projects registry: %w", err),
			args.JSONMode,
			stdout,
			stderr,
			meta,
			project,
		)
	}

	defaultProject := registry.GetDefault()
	isDefault := defaultProject != nil && defaultProject.Name == args.Name
	result := projectAddResult{
		Project: projectRecord{
			Name:    args.Name,
			Path:    args.Path,
			Default: isDefault,
		},
	}

	if args.JSONMode {
		envelope := NewH2SuccessEnvelope(projectAddSpec.Name, projectAddSpec.Path, project, meta, result)
		if err := writeJSON(stdout, envelope); err != nil {
			fmt.Fprintf(stderr, "Error: %v\n", err)
			return 1
		}
		return 0
	}

	fmt.Fprintf(stdout, "added %s %s\n", result.Project.Name, result.Project.Path)
	return 0
}

func runProjectList(
	args projectListParsedArgs,
	stdout io.Writer,
	stderr io.Writer,
	meta H2Meta,
	project H2ProjectContext,
) int {
	registry, err := config.LoadProjectsRegistry()
	if err != nil {
		return handleProjectInternalError(
			projectListSpec,
			fmt.Errorf("failed to load projects registry: %w", err),
			args.JSONMode,
			stdout,
			stderr,
			meta,
			project,
		)
	}

	result := projectRegistryToListResult(registry)
	if args.JSONMode {
		envelope := NewH2SuccessEnvelope(projectListSpec.Name, projectListSpec.Path, project, meta, result)
		if err := writeJSON(stdout, envelope); err != nil {
			fmt.Fprintf(stderr, "Error: %v\n", err)
			return 1
		}
		return 0
	}

	if len(result.Projects) == 0 {
		fmt.Fprintln(stdout, "no projects")
		return 0
	}

	for _, item := range result.Projects {
		marker := "-"
		if item.Default {
			marker = "*"
		}
		fmt.Fprintf(stdout, "%s %s %s\n", marker, item.Name, item.Path)
	}

	return 0
}

func runProjectRemove(
	args projectNameParsedArgs,
	stdout io.Writer,
	stderr io.Writer,
	meta H2Meta,
	project H2ProjectContext,
) int {
	registry, err := config.LoadProjectsRegistry()
	if err != nil {
		return handleProjectInternalError(
			projectRemoveSpec,
			fmt.Errorf("failed to load projects registry: %w", err),
			args.JSONMode,
			stdout,
			stderr,
			meta,
			project,
		)
	}

	if err := registry.Remove(args.Name); err != nil {
		return handleProjectDomainError(
			projectRemoveSpec,
			err,
			map[string]any{
				"name": args.Name,
			},
			args.JSONMode,
			stdout,
			stderr,
			meta,
			project,
		)
	}

	if err := config.SaveProjectsRegistry(registry); err != nil {
		return handleProjectInternalError(
			projectRemoveSpec,
			fmt.Errorf("failed to save projects registry: %w", err),
			args.JSONMode,
			stdout,
			stderr,
			meta,
			project,
		)
	}

	defaultProject := registry.GetDefault()
	defaultProjectName := ""
	if defaultProject != nil {
		defaultProjectName = defaultProject.Name
	}
	result := projectRemoveResult{
		Name:           args.Name,
		DefaultProject: defaultProjectName,
	}

	if args.JSONMode {
		envelope := NewH2SuccessEnvelope(projectRemoveSpec.Name, projectRemoveSpec.Path, project, meta, result)
		if err := writeJSON(stdout, envelope); err != nil {
			fmt.Fprintf(stderr, "Error: %v\n", err)
			return 1
		}
		return 0
	}

	if result.DefaultProject == "" {
		fmt.Fprintf(stdout, "removed %s\n", result.Name)
		return 0
	}

	fmt.Fprintf(stdout, "removed %s default %s\n", result.Name, result.DefaultProject)
	return 0
}

func runProjectSwitch(
	args projectNameParsedArgs,
	stdout io.Writer,
	stderr io.Writer,
	meta H2Meta,
	project H2ProjectContext,
) int {
	registry, err := config.LoadProjectsRegistry()
	if err != nil {
		return handleProjectInternalError(
			projectSwitchSpec,
			fmt.Errorf("failed to load projects registry: %w", err),
			args.JSONMode,
			stdout,
			stderr,
			meta,
			project,
		)
	}

	if err := registry.SetDefault(args.Name); err != nil {
		return handleProjectDomainError(
			projectSwitchSpec,
			err,
			map[string]any{
				"name": args.Name,
			},
			args.JSONMode,
			stdout,
			stderr,
			meta,
			project,
		)
	}

	if err := config.SaveProjectsRegistry(registry); err != nil {
		return handleProjectInternalError(
			projectSwitchSpec,
			fmt.Errorf("failed to save projects registry: %w", err),
			args.JSONMode,
			stdout,
			stderr,
			meta,
			project,
		)
	}

	defaultProject := registry.GetDefault()
	defaultProjectPath := ""
	if defaultProject != nil {
		defaultProjectPath = defaultProject.Path
	}
	result := projectSwitchResult{
		Project: projectRecord{
			Name:    args.Name,
			Path:    defaultProjectPath,
			Default: true,
		},
	}

	if args.JSONMode {
		envelope := NewH2SuccessEnvelope(projectSwitchSpec.Name, projectSwitchSpec.Path, project, meta, result)
		if err := writeJSON(stdout, envelope); err != nil {
			fmt.Fprintf(stderr, "Error: %v\n", err)
			return 1
		}
		return 0
	}

	fmt.Fprintf(stdout, "switched %s\n", result.Project.Name)
	return 0
}

func projectRegistryToListResult(registry *config.ProjectsRegistry) projectListResult {
	defaultProject := registry.GetDefault()
	defaultProjectName := ""
	if defaultProject != nil {
		defaultProjectName = defaultProject.Name
	}

	projects := make([]projectRecord, 0, len(registry.Projects))
	for _, project := range registry.Projects {
		projects = append(projects, projectRecord{
			Name:    project.Name,
			Path:    project.Path,
			Default: project.Name == defaultProjectName,
		})
	}

	return projectListResult{
		Projects:       projects,
		DefaultProject: defaultProjectName,
	}
}

func projectHasJSONFlag(args []string) bool {
	for _, arg := range args {
		if arg == "--json" {
			return true
		}
	}
	return false
}

func handleProjectInvalidUsage(
	spec projectCommandSpec,
	parseErr error,
	jsonMode bool,
	stdout io.Writer,
	stderr io.Writer,
	meta H2Meta,
	project H2ProjectContext,
) int {
	if jsonMode {
		errorPayload := NewH1Error(
			projectInvalidArgumentCode,
			parseErr.Error(),
			spec.InvalidArgRemediation,
			nil,
		)
		envelope := NewH2FailureEnvelope(spec.Name, spec.Path, project, meta, errorPayload)
		if err := writeJSON(stdout, envelope); err != nil {
			fmt.Fprintf(stderr, "Error: %v\n", err)
			return 1
		}
		return projectExitCodeInvalidUsage
	}

	fmt.Fprintln(stderr, spec.Usage)
	return projectExitCodeInvalidUsage
}

func handleProjectDomainError(
	spec projectCommandSpec,
	commandErr error,
	details map[string]any,
	jsonMode bool,
	stdout io.Writer,
	stderr io.Writer,
	meta H2Meta,
	project H2ProjectContext,
) int {
	errorPayload, ok := mapProjectDomainError(commandErr, details)
	if !ok {
		return handleProjectInternalError(spec, commandErr, jsonMode, stdout, stderr, meta, project)
	}

	if jsonMode {
		envelope := NewH2FailureEnvelope(spec.Name, spec.Path, project, meta, errorPayload)
		if err := writeJSON(stdout, envelope); err != nil {
			fmt.Fprintf(stderr, "Error: %v\n", err)
			return 1
		}
		return projectExitCodeDomainFailure
	}

	fmt.Fprintln(stderr, errorPayload.Message)
	return projectExitCodeDomainFailure
}

func mapProjectDomainError(commandErr error, details map[string]any) (H1Error, bool) {
	switch {
	case errors.Is(commandErr, config.ErrProjectNotFound):
		return NewH1Error(
			projectNotFoundCode,
			config.ErrProjectNotFound.Error(),
			projectNotFoundRemediation,
			cloneDetails(details),
		), true
	case errors.Is(commandErr, config.ErrDuplicateProject):
		return NewH1Error(
			projectDuplicateCode,
			config.ErrDuplicateProject.Error(),
			projectDuplicateRemediation,
			cloneDetails(details),
		), true
	case errors.Is(commandErr, config.ErrNotGitRepo), errors.Is(commandErr, config.ErrEmptyPath):
		return NewH1Error(
			projectInvalidPathCode,
			config.ErrNotGitRepo.Error(),
			projectInvalidPathRemediation,
			cloneDetails(details),
		), true
	case errors.Is(commandErr, config.ErrEmptyName):
		return NewH1Error(
			projectInvalidNameCode,
			config.ErrEmptyName.Error(),
			projectInvalidNameRemediation,
			cloneDetails(details),
		), true
	default:
		return H1Error{}, false
	}
}

func handleProjectInternalError(
	spec projectCommandSpec,
	commandErr error,
	jsonMode bool,
	stdout io.Writer,
	stderr io.Writer,
	meta H2Meta,
	project H2ProjectContext,
) int {
	if jsonMode {
		errorPayload := NewH1Error(
			projectInternalErrorCode,
			commandErr.Error(),
			projectInternalRemediation,
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
