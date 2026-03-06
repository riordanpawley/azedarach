package main

import (
	"encoding/json"
	"errors"
	"io"
	"testing"

	"github.com/riordanpawley/azedarach/internal/config"
)

type depJSONEnvelope struct {
	Command string `json:"command"`
	OK      bool   `json:"ok"`
	Error   struct {
		Code        string         `json:"code"`
		Message     string         `json:"message"`
		Remediation string         `json:"remediation"`
		Details     map[string]any `json:"details"`
	} `json:"error"`
}

func TestRunCLIDepAddJSONSuccessIncludesCommandAndOK(t *testing.T) {
	stubDepDependencies(t, func(_ []string) ([]byte, error) {
		return []byte(`{"ok":true}`), nil
	})

	assertDepJSONSuccessCommand(
		t,
		[]string{"dep", "add", "AZE-101", "AZE-102", "--type", "blocks", "--json"},
		"dep.add",
	)
}

func TestRunCLIDepRemoveJSONSuccessIncludesCommandAndOK(t *testing.T) {
	stubDepDependencies(t, func(_ []string) ([]byte, error) {
		return []byte(`{"ok":true}`), nil
	})

	assertDepJSONSuccessCommand(
		t,
		[]string{"dep", "remove", "AZE-101", "AZE-102", "--type", "blocks", "--json"},
		"dep.remove",
	)
}

func TestRunCLIDepListJSONSuccessIncludesCommandAndOK(t *testing.T) {
	stubDepDependencies(t, func(_ []string) ([]byte, error) {
		return []byte(`{"items":[]}`), nil
	})

	assertDepJSONSuccessCommand(t, []string{"dep", "list", "AZE-101", "--json"}, "dep.list")
}

func TestRunCLIDepTreeJSONSuccessIncludesCommandAndOK(t *testing.T) {
	stubDepDependencies(t, func(_ []string) ([]byte, error) {
		return []byte(`{"nodes":[]}`), nil
	})

	assertDepJSONSuccessCommand(t, []string{"dep", "tree", "AZE-101", "--json"}, "dep.tree")
}

func TestRunCLIDepCyclesJSONSuccessIncludesCommandAndOK(t *testing.T) {
	stubDepDependencies(t, func(_ []string) ([]byte, error) {
		return []byte(`{"cycles":[]}`), nil
	})

	assertDepJSONSuccessCommand(t, []string{"dep", "cycles", "--json"}, "dep.cycles")
}

func TestRunCLIDepInvalidUsageJSONReturnsDeterministicInvalidArgument(t *testing.T) {
	stubDepDependencies(t, func(_ []string) ([]byte, error) {
		t.Fatalf("dep runner should not be called on invalid usage")
		return nil, nil
	})

	testCases := []struct {
		name string
		args []string
	}{
		{
			name: "missing required args",
			args: []string{"dep", "list", "--json"},
		},
		{
			name: "unknown subcommand",
			args: []string{"dep", "unexpected", "--json"},
		},
		{
			name: "missing type for add",
			args: []string{"dep", "add", "AZE-101", "AZE-102", "--json"},
		},
		{
			name: "missing type for remove",
			args: []string{"dep", "remove", "AZE-101", "AZE-102", "--json"},
		},
		{
			name: "unknown flags",
			args: []string{"dep", "cycles", "--nope", "--json"},
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
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

			envelope1 := decodeDepJSONEnvelope(t, stdout1)
			envelope2 := decodeDepJSONEnvelope(t, stdout2)

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

func TestRunCLIDepRunnerFailureJSONReturnsDeterministicErrorCode(t *testing.T) {
	stubDepDependencies(t, func(_ []string) ([]byte, error) {
		return nil, errors.New("dep backend unavailable")
	})

	args := []string{"dep", "tree", "AZE-777", "--json"}
	exitCode1, stdout1, stderr1 := runCLIForTest(args)
	exitCode2, stdout2, stderr2 := runCLIForTest(args)

	if exitCode1 == 0 || exitCode2 == 0 {
		t.Fatalf("expected non-zero exit code for dep runner failure, got %d then %d", exitCode1, exitCode2)
	}
	if exitCode1 != exitCode2 {
		t.Fatalf("expected deterministic non-zero exit code, got %d then %d", exitCode1, exitCode2)
	}
	if stderr1 != "" || stderr2 != "" {
		t.Fatalf("expected empty stderr in JSON mode, got %q and %q", stderr1, stderr2)
	}

	envelope1 := decodeDepJSONEnvelope(t, stdout1)
	envelope2 := decodeDepJSONEnvelope(t, stdout2)

	if envelope1.Command != "dep.tree" {
		t.Fatalf("expected first command=dep.tree, got %q", envelope1.Command)
	}
	if envelope2.Command != "dep.tree" {
		t.Fatalf("expected second command=dep.tree, got %q", envelope2.Command)
	}
	if envelope1.OK || envelope2.OK {
		t.Fatalf("expected ok=false for dep runner failure path")
	}
	if envelope1.Error.Code == "" {
		t.Fatalf("expected deterministic non-empty error code for dep runner failure")
	}
	if envelope1.Error.Code != "backend_error" {
		t.Fatalf("expected first error.code=backend_error, got %q", envelope1.Error.Code)
	}
	if envelope2.Error.Code != envelope1.Error.Code {
		t.Fatalf(
			"expected deterministic dep runner failure code, got %q then %q",
			envelope1.Error.Code,
			envelope2.Error.Code,
		)
	}
}

func TestRunCLIDepAddCycleRejectedJSONReturnsDeterministicErrorCode(t *testing.T) {
	stubDepDependencies(t, func(_ []string) ([]byte, error) {
		return nil, depBackendCommandError{
			ExitCode: 1,
			Stderr:   "dependency cycle detected while adding edge",
		}
	})

	args := []string{"dep", "add", "AZE-101", "AZE-102", "--type", "blocking", "--json"}
	exitCode, stdout, stderr := runCLIForTest(args)

	if exitCode != depExitCodeBackendFailure {
		t.Fatalf("expected deterministic cycle rejection exit code %d, got %d", depExitCodeBackendFailure, exitCode)
	}
	if stderr != "" {
		t.Fatalf("expected empty stderr in JSON mode, got %q", stderr)
	}

	envelope := decodeDepJSONEnvelope(t, stdout)
	if envelope.Command != "dep.add" {
		t.Fatalf("expected command=dep.add, got %q", envelope.Command)
	}
	if envelope.OK {
		t.Fatalf("expected ok=false for cycle rejection path")
	}
	if envelope.Error.Code != "cycle_rejected" {
		t.Fatalf("expected error.code=cycle_rejected, got %q", envelope.Error.Code)
	}
	if envelope.Error.Message != "Dependency edge would introduce disallowed cycle" {
		t.Fatalf("unexpected message: %q", envelope.Error.Message)
	}
	if envelope.Error.Remediation != "Run az dep cycles --json and choose a non-cyclic relation target" {
		t.Fatalf("unexpected remediation: %q", envelope.Error.Remediation)
	}
	if got := envelope.Error.Details["sourceIssueId"]; got != "AZE-101" {
		t.Fatalf("expected details.sourceIssueId=AZE-101, got %#v", got)
	}
	if got := envelope.Error.Details["targetIssueId"]; got != "AZE-102" {
		t.Fatalf("expected details.targetIssueId=AZE-102, got %#v", got)
	}
}

func TestRunCLIDepAddRemoveSuccessTriggersBackupOpenAndMutation(t *testing.T) {
	testCases := []struct {
		name string
		args []string
	}{
		{
			name: "add",
			args: []string{"dep", "add", "AZE-101", "AZE-102", "--type", "blocks", "--json"},
		},
		{
			name: "remove",
			args: []string{"dep", "remove", "AZE-101", "AZE-102", "--type", "blocks", "--json"},
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			stubDepDependencies(t, func(_ []string) ([]byte, error) {
				return []byte(`{"ok":true}`), nil
			})
			backupRunner := &depBackupRunnerSpy{}
			stubDepBackupRunner(t, backupRunner)

			exitCode, _, stderr := runCLIForTest(testCase.args)
			if exitCode != 0 {
				t.Fatalf("expected success exit code, got %d (stderr=%q)", exitCode, stderr)
			}
			if backupRunner.openCalls != 1 {
				t.Fatalf("expected OnOpen once, got %d", backupRunner.openCalls)
			}
			if backupRunner.mutationCalls != 1 {
				t.Fatalf("expected OnMutationSuccess once, got %d", backupRunner.mutationCalls)
			}
		})
	}
}

func TestRunCLIDepReadCommandsTriggerBackupOpenOnly(t *testing.T) {
	testCases := []struct {
		name string
		args []string
	}{
		{
			name: "list",
			args: []string{"dep", "list", "AZE-101", "--json"},
		},
		{
			name: "tree",
			args: []string{"dep", "tree", "AZE-101", "--json"},
		},
		{
			name: "cycles",
			args: []string{"dep", "cycles", "--json"},
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			stubDepDependencies(t, func(_ []string) ([]byte, error) {
				return []byte(`{"ok":true}`), nil
			})
			backupRunner := &depBackupRunnerSpy{}
			stubDepBackupRunner(t, backupRunner)

			exitCode, _, stderr := runCLIForTest(testCase.args)
			if exitCode != 0 {
				t.Fatalf("expected success exit code, got %d (stderr=%q)", exitCode, stderr)
			}
			if backupRunner.openCalls != 1 {
				t.Fatalf("expected OnOpen once, got %d", backupRunner.openCalls)
			}
			if backupRunner.mutationCalls != 0 {
				t.Fatalf("expected OnMutationSuccess never, got %d", backupRunner.mutationCalls)
			}
		})
	}
}

func TestRunCLIDepAddFailureDoesNotTriggerBackupMutation(t *testing.T) {
	stubDepDependencies(t, func(_ []string) ([]byte, error) {
		return nil, errors.New("backend failed")
	})
	backupRunner := &depBackupRunnerSpy{}
	stubDepBackupRunner(t, backupRunner)

	exitCode, _, _ := runCLIForTest([]string{"dep", "add", "AZE-101", "AZE-102", "--type", "blocks", "--json"})
	if exitCode == 0 {
		t.Fatalf("expected non-zero exit code when backend fails")
	}
	if backupRunner.openCalls != 1 {
		t.Fatalf("expected OnOpen once on failing mutation path, got %d", backupRunner.openCalls)
	}
	if backupRunner.mutationCalls != 0 {
		t.Fatalf("expected OnMutationSuccess never on failed mutation, got %d", backupRunner.mutationCalls)
	}
}

func assertDepJSONSuccessCommand(t *testing.T, args []string, expectedCommand string) {
	t.Helper()

	exitCode, stdout, stderr := runCLIForTest(args)
	if exitCode != 0 {
		t.Fatalf("expected exit code 0, got %d (stderr: %q)", exitCode, stderr)
	}
	if stderr != "" {
		t.Fatalf("expected empty stderr, got %q", stderr)
	}

	envelope := decodeDepJSONEnvelope(t, stdout)
	if envelope.Command != expectedCommand {
		t.Fatalf("expected command=%s, got %q", expectedCommand, envelope.Command)
	}
	if !envelope.OK {
		t.Fatalf("expected ok=true, got false")
	}
}

func decodeDepJSONEnvelope(t *testing.T, stdout string) depJSONEnvelope {
	t.Helper()

	var envelope depJSONEnvelope
	if err := json.Unmarshal([]byte(stdout), &envelope); err != nil {
		t.Fatalf("expected valid JSON output, got parse error: %v\noutput:\n%s", err, stdout)
	}
	return envelope
}

func stubDepDependencies(
	t *testing.T,
	executor depBackendExecutor,
) {
	t.Helper()

	originalExecutor := depBackendExecutorHook
	depBackendExecutorHook = executor

	t.Cleanup(func() {
		depBackendExecutorHook = originalExecutor
	})
}

type depBackupRunnerSpy struct {
	openCalls     int
	mutationCalls int
}

func (spy *depBackupRunnerSpy) OnOpen() {
	spy.openCalls++
}

func (spy *depBackupRunnerSpy) OnMutationSuccess() {
	spy.mutationCalls++
}

func stubDepBackupRunner(t *testing.T, runner issueCommandBackupRunner) {
	t.Helper()

	originalLoadConfig := loadConfig
	originalBackupRunnerFactory := newIssueCommandBackupRunner

	loadConfig = func() (*config.Config, error) {
		return config.DefaultConfig(), nil
	}
	newIssueCommandBackupRunner = func(
		_ *config.Config,
		_ H2ProjectContext,
		_ io.Writer,
	) issueCommandBackupRunner {
		return runner
	}

	t.Cleanup(func() {
		loadConfig = originalLoadConfig
		newIssueCommandBackupRunner = originalBackupRunnerFactory
	})
}
