package main

import (
	"bytes"
	"encoding/json"
	"regexp"
	"strings"
	"testing"
)

type primeJSONEnvelope struct {
	Command string `json:"command"`
	OK      bool   `json:"ok"`
	Data    struct {
		QuickReference []string        `json:"quickReference"`
		Policies       map[string]bool `json:"policies"`
		CommitTemplate string          `json:"commitTemplate"`
	} `json:"data"`
}

func TestRunCLIPrimeHumanIncludesQuickReferencePoliciesAndCommitTemplate(t *testing.T) {
	exitCode, stdout, stderr := runCLIForTest([]string{"prime"})
	if exitCode != 0 {
		t.Fatalf("expected exit code 0, got %d (stderr: %q)", exitCode, stderr)
	}
	if stderr != "" {
		t.Fatalf("expected empty stderr, got %q", stderr)
	}

	requiredCommands := []string{
		"az show <issue-id> --json",
		"az update <issue-id> ... --json",
		"az close <issue-id> --json",
		"az list --json",
	}
	for _, cmd := range requiredCommands {
		if !strings.Contains(stdout, cmd) {
			t.Fatalf("expected human prime output to include quick-reference command %q.\noutput:\n%s", cmd, stdout)
		}
	}

	normalized := strings.ToLower(stdout)
	if !strings.Contains(normalized, "close issues when done") {
		t.Fatalf("expected close-when-done policy in output.\noutput:\n%s", stdout)
	}
	if !strings.Contains(normalized, "commit before close") {
		t.Fatalf("expected commit-before-close policy in output.\noutput:\n%s", stdout)
	}
	if !strings.Contains(stdout, "git commit -m") {
		t.Fatalf("expected commit template command in output.\noutput:\n%s", stdout)
	}
	if !regexp.MustCompile(`AZE-\d+`).MatchString(stdout) {
		t.Fatalf("expected commit template to include an issue-id example (e.g. AZE-123).\noutput:\n%s", stdout)
	}
}

func TestRunCLIPrimeJSONReturnsDeterministicEnvelope(t *testing.T) {
	exitCode1, stdout1, stderr1 := runCLIForTest([]string{"prime", "--json"})
	if exitCode1 != 0 {
		t.Fatalf("expected first exit code 0, got %d (stderr: %q)", exitCode1, stderr1)
	}
	if stderr1 != "" {
		t.Fatalf("expected first stderr to be empty, got %q", stderr1)
	}

	exitCode2, stdout2, stderr2 := runCLIForTest([]string{"prime", "--json"})
	if exitCode2 != 0 {
		t.Fatalf("expected second exit code 0, got %d (stderr: %q)", exitCode2, stderr2)
	}
	if stderr2 != "" {
		t.Fatalf("expected second stderr to be empty, got %q", stderr2)
	}

	if stdout1 != stdout2 {
		t.Fatalf("expected deterministic JSON output across repeated invocations.\nfirst:\n%s\nsecond:\n%s", stdout1, stdout2)
	}

	var envelope primeJSONEnvelope
	if err := json.Unmarshal([]byte(stdout1), &envelope); err != nil {
		t.Fatalf("expected valid JSON output, got parse error: %v\noutput:\n%s", err, stdout1)
	}

	if envelope.Command != "prime" {
		t.Fatalf("expected command=prime, got %q", envelope.Command)
	}
	if !envelope.OK {
		t.Fatalf("expected ok=true, got false")
	}

	if len(envelope.Data.QuickReference) == 0 {
		t.Fatalf("expected data.quickReference to contain commands")
	}

	expectedQuickRef := []string{
		"az show <issue-id> --json",
		"az update <issue-id> ... --json",
		"az close <issue-id> --json",
		"az list --json",
	}
	for _, expected := range expectedQuickRef {
		if !sliceContains(envelope.Data.QuickReference, expected) {
			t.Fatalf("expected data.quickReference to include %q, got %#v", expected, envelope.Data.QuickReference)
		}
	}

	if envelope.Data.Policies == nil {
		t.Fatalf("expected data.policies object")
	}
	requiredPolicies := []string{
		"closeWhenDone",
		"commitBeforeClose",
		"requireIssueIdInCommitMessage",
	}
	for _, policy := range requiredPolicies {
		val, ok := envelope.Data.Policies[policy]
		if !ok {
			t.Fatalf("expected data.policies[%q] to exist; got %#v", policy, envelope.Data.Policies)
		}
		if !val {
			t.Fatalf("expected data.policies[%q]=true; got false", policy)
		}
	}

	if envelope.Data.CommitTemplate == "" {
		t.Fatalf("expected non-empty data.commitTemplate")
	}
	if !strings.Contains(envelope.Data.CommitTemplate, "git commit -m") {
		t.Fatalf("expected data.commitTemplate to include commit command, got %q", envelope.Data.CommitTemplate)
	}
	if !regexp.MustCompile(`AZE-\d+`).MatchString(envelope.Data.CommitTemplate) {
		t.Fatalf("expected data.commitTemplate to include issue-id example, got %q", envelope.Data.CommitTemplate)
	}
}

func TestRunCLIPrimeWithPositionalArgFailsDeterministically(t *testing.T) {
	exitCode1, stdout1, stderr1 := runCLIForTest([]string{"prime", "AZE-99999"})
	if exitCode1 == 0 {
		t.Fatalf("expected non-zero exit code when positional arg is provided")
	}
	if stdout1 != "" {
		t.Fatalf("expected empty stdout for invalid positional args, got %q", stdout1)
	}
	if stderr1 == "" {
		t.Fatalf("expected deterministic diagnostic in stderr")
	}

	exitCode2, stdout2, stderr2 := runCLIForTest([]string{"prime", "AZE-99999"})
	if exitCode2 == 0 {
		t.Fatalf("expected non-zero exit code on repeated invocation with positional args")
	}
	if stdout2 != "" {
		t.Fatalf("expected empty stdout on repeated invalid invocation, got %q", stdout2)
	}
	if stderr2 == "" {
		t.Fatalf("expected deterministic diagnostic in stderr on repeated invocation")
	}

	if stderr1 != stderr2 {
		t.Fatalf("expected deterministic diagnostics across repeated invocations.\nfirst: %q\nsecond: %q", stderr1, stderr2)
	}

	normalized := strings.ToLower(stderr1)
	if !strings.Contains(normalized, "unsupported positional args") {
		t.Fatalf("expected diagnostic to mention unsupported positional args, got %q", stderr1)
	}
	if !strings.Contains(stderr1, "AZE-99999") {
		t.Fatalf("expected diagnostic to include invalid argument, got %q", stderr1)
	}
}

func runCLIForTest(args []string) (int, string, string) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	exitCode := runCLI(args, &stdout, &stderr)
	return exitCode, stdout.String(), stderr.String()
}

func sliceContains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
