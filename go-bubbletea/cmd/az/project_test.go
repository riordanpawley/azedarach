package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/riordanpawley/azedarach/internal/config"
)

type projectJSONEnvelope struct {
	Command string          `json:"command"`
	OK      bool            `json:"ok"`
	Result  json.RawMessage `json:"result"`
	Error   struct {
		Code string `json:"code"`
	} `json:"error"`
}

func TestRunCLIProjectAddJSONSuccessIncludesAddedProjectNameAndPath(t *testing.T) {
	workspaceRoot := setupProjectCommandSandbox(t)
	projectPath := createProjectGitRepoAt(t, workspaceRoot, "alpha-repo")

	exitCode, stdout, stderr := runCLIForTest([]string{
		"project",
		"add",
		projectPath,
		"--name",
		"alpha",
		"--json",
	})
	if exitCode != 0 {
		t.Fatalf("expected exit code 0, got %d (stderr: %q)", exitCode, stderr)
	}
	if stderr != "" {
		t.Fatalf("expected empty stderr, got %q", stderr)
	}

	envelope := decodeProjectJSONEnvelope(t, stdout)
	if envelope.Command != "project.add" {
		t.Fatalf("expected command=project.add, got %q", envelope.Command)
	}
	if !envelope.OK {
		t.Fatalf("expected ok=true, got false")
	}
	if !projectJSONContainsStringField(envelope.Result, "name", "alpha") {
		t.Fatalf("expected result to include project name alpha.\nresult:\n%s", string(envelope.Result))
	}
	if !projectJSONContainsPathField(envelope.Result, projectPath) {
		t.Fatalf("expected result to include project path %q.\nresult:\n%s", projectPath, string(envelope.Result))
	}
}

func TestRunCLIProjectListJSONSuccessDeterministicOrdering(t *testing.T) {
	workspaceRoot := setupProjectCommandSandbox(t)
	betaPath := createProjectGitRepoAt(t, workspaceRoot, "beta-repo")
	alphaPath := createProjectGitRepoAt(t, workspaceRoot, "alpha-repo")

	writeProjectsRegistryForTest(t, []config.Project{
		{Name: "beta", Path: betaPath},
		{Name: "alpha", Path: alphaPath},
	}, "beta")

	exitCode1, stdout1, stderr1 := runCLIForTest([]string{"project", "list", "--json"})
	if exitCode1 != 0 {
		t.Fatalf("expected first exit code 0, got %d (stderr: %q)", exitCode1, stderr1)
	}
	if stderr1 != "" {
		t.Fatalf("expected first stderr to be empty, got %q", stderr1)
	}

	envelope1 := decodeProjectJSONEnvelope(t, stdout1)
	if envelope1.Command != "project.list" {
		t.Fatalf("expected first command=project.list, got %q", envelope1.Command)
	}
	if !envelope1.OK {
		t.Fatalf("expected first ok=true, got false")
	}

	result1 := decodeProjectListResult(t, envelope1.Result)
	if len(result1.Projects) != 2 {
		t.Fatalf("expected first result.projects length 2, got %d", len(result1.Projects))
	}
	if result1.DefaultProject != "beta" {
		t.Fatalf("expected first result.defaultProject=beta, got %q", result1.DefaultProject)
	}
	if !projectListContains(result1.Projects, "beta", betaPath) {
		t.Fatalf("expected first result.projects to include beta=%q, got %#v", betaPath, result1.Projects)
	}
	if !projectListContains(result1.Projects, "alpha", alphaPath) {
		t.Fatalf("expected first result.projects to include alpha=%q, got %#v", alphaPath, result1.Projects)
	}

	exitCode2, stdout2, stderr2 := runCLIForTest([]string{"project", "list", "--json"})
	if exitCode2 != 0 {
		t.Fatalf("expected second exit code 0, got %d (stderr: %q)", exitCode2, stderr2)
	}
	if stderr2 != "" {
		t.Fatalf("expected second stderr to be empty, got %q", stderr2)
	}

	envelope2 := decodeProjectJSONEnvelope(t, stdout2)
	if envelope2.Command != "project.list" {
		t.Fatalf("expected second command=project.list, got %q", envelope2.Command)
	}
	if !envelope2.OK {
		t.Fatalf("expected second ok=true, got false")
	}

	result2 := decodeProjectListResult(t, envelope2.Result)
	if !reflect.DeepEqual(result1.Projects, result2.Projects) {
		t.Fatalf(
			"expected deterministic project ordering across repeated invocations.\nfirst: %#v\nsecond: %#v",
			result1.Projects,
			result2.Projects,
		)
	}
	if result2.DefaultProject != result1.DefaultProject {
		t.Fatalf(
			"expected deterministic default project across repeated invocations, got %q then %q",
			result1.DefaultProject,
			result2.DefaultProject,
		)
	}
}

func TestRunCLIProjectRemoveJSONSuccess(t *testing.T) {
	workspaceRoot := setupProjectCommandSandbox(t)
	alphaPath := createProjectGitRepoAt(t, workspaceRoot, "alpha-repo")
	betaPath := createProjectGitRepoAt(t, workspaceRoot, "beta-repo")

	writeProjectsRegistryForTest(t, []config.Project{
		{Name: "alpha", Path: alphaPath},
		{Name: "beta", Path: betaPath},
	}, "alpha")

	exitCode, stdout, stderr := runCLIForTest([]string{"project", "remove", "beta", "--json"})
	if exitCode != 0 {
		t.Fatalf("expected exit code 0, got %d (stderr: %q)", exitCode, stderr)
	}
	if stderr != "" {
		t.Fatalf("expected empty stderr, got %q", stderr)
	}

	envelope := decodeProjectJSONEnvelope(t, stdout)
	if envelope.Command != "project.remove" {
		t.Fatalf("expected command=project.remove, got %q", envelope.Command)
	}
	if !envelope.OK {
		t.Fatalf("expected ok=true, got false")
	}
}

func TestRunCLIProjectSwitchJSONSuccessUpdatesDefaultProject(t *testing.T) {
	workspaceRoot := setupProjectCommandSandbox(t)
	alphaPath := createProjectGitRepoAt(t, workspaceRoot, "alpha-repo")
	betaPath := createProjectGitRepoAt(t, workspaceRoot, "beta-repo")

	writeProjectsRegistryForTest(t, []config.Project{
		{Name: "alpha", Path: alphaPath},
		{Name: "beta", Path: betaPath},
	}, "alpha")

	exitCode, stdout, stderr := runCLIForTest([]string{"project", "switch", "beta", "--json"})
	if exitCode != 0 {
		t.Fatalf("expected exit code 0, got %d (stderr: %q)", exitCode, stderr)
	}
	if stderr != "" {
		t.Fatalf("expected empty stderr, got %q", stderr)
	}

	envelope := decodeProjectJSONEnvelope(t, stdout)
	if envelope.Command != "project.switch" {
		t.Fatalf("expected command=project.switch, got %q", envelope.Command)
	}
	if !envelope.OK {
		t.Fatalf("expected ok=true, got false")
	}

	var switchResult projectSwitchResult
	if err := json.Unmarshal(envelope.Result, &switchResult); err != nil {
		t.Fatalf(
			"expected switch result to be valid JSON, got parse error: %v\nresult:\n%s",
			err,
			string(envelope.Result),
		)
	}
	if switchResult.Project.Name != "beta" {
		t.Fatalf("expected switch result.project.name=beta, got %q", switchResult.Project.Name)
	}
	if !switchResult.Project.Default {
		t.Fatalf("expected switch result.project.default=true")
	}
	if !samePathForTest(switchResult.Project.Path, betaPath) {
		t.Fatalf("expected switch result.project.path=%q, got %q", betaPath, switchResult.Project.Path)
	}

	listExitCode, listStdout, listStderr := runCLIForTest([]string{"project", "list", "--json"})
	if listExitCode != 0 {
		t.Fatalf("expected list exit code 0 after switch, got %d (stderr: %q)", listExitCode, listStderr)
	}
	if listStderr != "" {
		t.Fatalf("expected empty list stderr after switch, got %q", listStderr)
	}

	listEnvelope := decodeProjectJSONEnvelope(t, listStdout)
	if !listEnvelope.OK {
		t.Fatalf("expected list ok=true after switch, got false")
	}
	listResult := decodeProjectListResult(t, listEnvelope.Result)
	if listResult.DefaultProject != "beta" {
		t.Fatalf("expected list result.defaultProject=beta after switch, got %q", listResult.DefaultProject)
	}
}

func TestRunCLIProjectInvalidUsageJSONReturnsDeterministicInvalidArgument(t *testing.T) {
	testCases := []struct {
		name string
		args []string
	}{
		{
			name: "missing required arg for add",
			args: []string{"project", "add", "--json"},
		},
		{
			name: "missing required arg for remove",
			args: []string{"project", "remove", "--json"},
		},
		{
			name: "unknown subcommand",
			args: []string{"project", "unexpected", "--json"},
		},
		{
			name: "unknown flag",
			args: []string{"project", "list", "--nope", "--json"},
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			setupProjectCommandSandbox(t)

			exitCode1, stdout1, stderr1 := runCLIForTest(testCase.args)
			exitCode2, stdout2, stderr2 := runCLIForTest(testCase.args)

			if exitCode1 == 0 || exitCode2 == 0 {
				t.Fatalf("expected non-zero exit code for invalid usage, got %d then %d", exitCode1, exitCode2)
			}
			if exitCode1 != exitCode2 {
				t.Fatalf("expected deterministic non-zero exit code, got %d then %d", exitCode1, exitCode2)
			}
			if stderr1 != "" || stderr2 != "" {
				t.Fatalf("expected empty stderr in JSON mode, got %q and %q", stderr1, stderr2)
			}

			envelope1 := decodeProjectJSONEnvelope(t, stdout1)
			envelope2 := decodeProjectJSONEnvelope(t, stdout2)

			if envelope1.OK || envelope2.OK {
				t.Fatalf("expected ok=false for invalid usage JSON path")
			}
			if envelope1.Error.Code != "invalid_argument" {
				t.Fatalf("expected first error.code=invalid_argument, got %q", envelope1.Error.Code)
			}
			if envelope2.Error.Code != envelope1.Error.Code {
				t.Fatalf(
					"expected deterministic invalid usage error code, got %q then %q",
					envelope1.Error.Code,
					envelope2.Error.Code,
				)
			}
		})
	}
}

func TestRunCLIProjectUnknownProjectJSONDeterministicFailureCode(t *testing.T) {
	testCases := []struct {
		name            string
		args            []string
		expectedCommand string
	}{
		{
			name:            "remove unknown project",
			args:            []string{"project", "remove", "missing", "--json"},
			expectedCommand: "project.remove",
		},
		{
			name:            "switch unknown project",
			args:            []string{"project", "switch", "missing", "--json"},
			expectedCommand: "project.switch",
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			workspaceRoot := setupProjectCommandSandbox(t)
			knownPath := createProjectGitRepoAt(t, workspaceRoot, "known-repo")
			writeProjectsRegistryForTest(t, []config.Project{
				{Name: "known", Path: knownPath},
			}, "known")

			exitCode1, stdout1, stderr1 := runCLIForTest(testCase.args)
			exitCode2, stdout2, stderr2 := runCLIForTest(testCase.args)

			if exitCode1 == 0 || exitCode2 == 0 {
				t.Fatalf("expected non-zero exit code for unknown project, got %d then %d", exitCode1, exitCode2)
			}
			if exitCode1 != exitCode2 {
				t.Fatalf("expected deterministic non-zero exit code, got %d then %d", exitCode1, exitCode2)
			}
			if stderr1 != "" || stderr2 != "" {
				t.Fatalf("expected empty stderr in JSON mode, got %q and %q", stderr1, stderr2)
			}

			envelope1 := decodeProjectJSONEnvelope(t, stdout1)
			envelope2 := decodeProjectJSONEnvelope(t, stdout2)

			if envelope1.Command != testCase.expectedCommand {
				t.Fatalf("expected first command=%s, got %q", testCase.expectedCommand, envelope1.Command)
			}
			if envelope2.Command != testCase.expectedCommand {
				t.Fatalf("expected second command=%s, got %q", testCase.expectedCommand, envelope2.Command)
			}
			if envelope1.OK || envelope2.OK {
				t.Fatalf("expected ok=false for unknown project failure path")
			}
			if envelope1.Error.Code == "" {
				t.Fatalf("expected deterministic non-empty error code for unknown project")
			}
			if envelope2.Error.Code != envelope1.Error.Code {
				t.Fatalf(
					"expected deterministic unknown-project error code, got %q then %q",
					envelope1.Error.Code,
					envelope2.Error.Code,
				)
			}
			if !strings.Contains(envelope1.Error.Code, "not_found") {
				t.Fatalf(
					"expected project_not_found or equivalent deterministic not-found code, got %q",
					envelope1.Error.Code,
				)
			}
		})
	}
}

func setupProjectCommandSandbox(t *testing.T) string {
	t.Helper()

	originalLoadConfig := loadConfig
	loadConfig = func() (*config.Config, error) {
		return config.DefaultConfig(), nil
	}
	t.Cleanup(func() {
		loadConfig = originalLoadConfig
	})

	homeDir := t.TempDir()
	t.Setenv("HOME", homeDir)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(homeDir, ".config"))

	workspaceRoot := filepath.Join(homeDir, "workspace")
	if err := os.MkdirAll(workspaceRoot, 0755); err != nil {
		t.Fatalf("failed to create workspace root %q: %v", workspaceRoot, err)
	}

	originalWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("failed to get working directory: %v", err)
	}
	if err := os.Chdir(workspaceRoot); err != nil {
		t.Fatalf("failed to change directory to workspace root %q: %v", workspaceRoot, err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(originalWD); err != nil {
			t.Fatalf("failed to restore working directory %q: %v", originalWD, err)
		}
	})

	return workspaceRoot
}

func createProjectGitRepoAt(t *testing.T, root, name string) string {
	t.Helper()

	projectPath := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Join(projectPath, ".git"), 0755); err != nil {
		t.Fatalf("failed to create git repository at %q: %v", projectPath, err)
	}

	return projectPath
}

func writeProjectsRegistryForTest(t *testing.T, projects []config.Project, defaultProject string) {
	t.Helper()

	copiedProjects := make([]config.Project, len(projects))
	copy(copiedProjects, projects)

	registry := &config.ProjectsRegistry{
		Projects:       copiedProjects,
		DefaultProject: defaultProject,
	}
	if err := config.SaveProjectsRegistry(registry); err != nil {
		t.Fatalf("failed to save projects registry: %v", err)
	}
}

func decodeProjectJSONEnvelope(t *testing.T, stdout string) projectJSONEnvelope {
	t.Helper()

	var envelope projectJSONEnvelope
	if err := json.Unmarshal([]byte(stdout), &envelope); err != nil {
		t.Fatalf("expected valid JSON output, got parse error: %v\noutput:\n%s", err, stdout)
	}
	return envelope
}

func decodeProjectListResult(t *testing.T, rawResult json.RawMessage) projectListResult {
	t.Helper()

	var result projectListResult
	if err := json.Unmarshal(rawResult, &result); err != nil {
		t.Fatalf("expected valid project list result JSON, got parse error: %v\nresult:\n%s", err, string(rawResult))
	}
	return result
}

func projectListContains(items []projectRecord, name, expectedPath string) bool {
	for _, item := range items {
		if item.Name != name {
			continue
		}
		if samePathForTest(item.Path, expectedPath) {
			return true
		}
	}
	return false
}

func samePathForTest(actualPath, expectedPath string) bool {
	cleanActual := filepath.Clean(actualPath)
	cleanExpected := filepath.Clean(expectedPath)
	if cleanActual == cleanExpected {
		return true
	}

	evalActual, errActual := filepath.EvalSymlinks(cleanActual)
	evalExpected, errExpected := filepath.EvalSymlinks(cleanExpected)
	if errActual == nil && errExpected == nil && evalActual == evalExpected {
		return true
	}

	return false
}

func projectJSONContainsPathField(raw json.RawMessage, expectedPath string) bool {
	pathVariants := []string{filepath.Clean(expectedPath)}
	if evalPath, err := filepath.EvalSymlinks(expectedPath); err == nil {
		cleanEvalPath := filepath.Clean(evalPath)
		if cleanEvalPath != pathVariants[0] {
			pathVariants = append(pathVariants, cleanEvalPath)
		}
	}

	for _, expectedVariant := range pathVariants {
		if projectJSONContainsStringField(raw, "path", expectedVariant) {
			return true
		}
	}

	return false
}

func projectJSONContainsStringField(raw json.RawMessage, key, expected string) bool {
	var parsed any
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return false
	}
	return projectJSONNestedStringFieldEquals(parsed, key, expected)
}

func projectJSONNestedStringFieldEquals(value any, key, expected string) bool {
	switch typed := value.(type) {
	case map[string]any:
		if rawField, ok := typed[key]; ok {
			if actual, ok := rawField.(string); ok && actual == expected {
				return true
			}
		}
		for _, nestedValue := range typed {
			if projectJSONNestedStringFieldEquals(nestedValue, key, expected) {
				return true
			}
		}
	case []any:
		for _, nestedValue := range typed {
			if projectJSONNestedStringFieldEquals(nestedValue, key, expected) {
				return true
			}
		}
	}

	return false
}
