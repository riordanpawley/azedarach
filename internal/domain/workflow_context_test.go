package domain

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestBuildWorkflowContextPacketIsBoundedDeterministicAndRevisionBound(t *testing.T) {
	values := make([]string, 80)
	for i := range values {
		values[i] = strings.Repeat(string(rune('a'+i%20)), 1100)
	}
	input := WorkflowContextInput{Role: WorkflowRoleReviewer, IssueID: "consumer-42", SourceRevision: strings.Repeat("a", 40), Summary: "review the candidate", Requirements: values, UnresolvedFindings: values, AffectedInvariants: values}
	first, err := BuildWorkflowContextPacket(input)
	if err != nil {
		t.Fatal(err)
	}
	second, err := BuildWorkflowContextPacket(input)
	if err != nil {
		t.Fatal(err)
	}
	a, _ := json.Marshal(first)
	b, _ := json.Marshal(second)
	if string(a) != string(b) {
		t.Fatal("packet compaction is not deterministic")
	}
	if len(a) > WorkflowContextPacketMaxBytes || len(first.Omitted) == 0 {
		t.Fatalf("packet bytes=%d omissions=%+v", len(a), first.Omitted)
	}
	input.SourceRevision = strings.Repeat("b", 40)
	changed, err := BuildWorkflowContextPacket(input)
	if err != nil {
		t.Fatal(err)
	}
	if changed.Provenance.SourceHash == first.Provenance.SourceHash {
		t.Fatal("source revision did not change provenance hash")
	}
}

func TestWorkflowContextExcludesSensitiveLocalAndIrrelevantSources(t *testing.T) {
	packet, err := BuildWorkflowContextPacket(WorkflowContextInput{
		Role: WorkflowRoleWorker, IssueID: "npm-7", SourceRevision: "deadbeef",
		Summary: "portable consumer", Requirements: []string{"npm test", "token=secret", "inspect /Users/alice/private", `inspect D:\private\output`},
		ArtifactLinks: []WorkflowArtifactReference{{Label: "CI", Reference: "https://ci.example.test/run/7"}, {Label: "local", Reference: "/tmp/output.log"}, {Label: "token=secret", Reference: "https://ci.example.test/private"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(packet)
	for _, forbidden := range []string{"secret", "/Users/", "/tmp/", `D:\private`} {
		if strings.Contains(string(body), forbidden) {
			t.Fatalf("packet leaked %q: %s", forbidden, body)
		}
	}
	if !strings.Contains(string(body), "npm test") || !strings.Contains(string(body), "ci.example.test") {
		t.Fatalf("non-Azedarach consumer context missing: %s", body)
	}
	if len(packet.Omitted) == 0 {
		t.Fatal("excluded fields were not explained")
	}
}

func TestWorkflowResultCompactionRetainsAnArtifactReference(t *testing.T) {
	artifacts := make([]WorkflowArtifactReference, 80)
	for i := range artifacts {
		artifacts[i] = WorkflowArtifactReference{Label: "complete output", Reference: "artifact:validation-output/" + strings.Repeat(string(rune('a'+i%20)), 1800)}
	}
	result, err := BuildWorkflowResultSummary(WorkflowResultInput{
		Role: WorkflowRoleValidator, ScopeID: "repository:consumer", SourceRevision: "abc123", Status: "failed",
		FailureSummary: strings.Repeat("actionable failure ", 1000), ArtifactLinks: artifacts, OutputPresent: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(result)
	if len(body) > WorkflowResultSummaryMaxBytes || result.OutputRetention != "retained" || len(result.ArtifactLinks) == 0 {
		t.Fatalf("bytes=%d retention=%q artifacts=%d", len(body), result.OutputRetention, len(result.ArtifactLinks))
	}
}

func TestWorkflowResultSummaryReportsUnavailableOutputRetention(t *testing.T) {
	result, err := BuildWorkflowResultSummary(WorkflowResultInput{Role: WorkflowRoleValidator, IssueID: "consumer-9", SourceRevision: "abc123", Status: "failed", FailureSummary: strings.Repeat("failure ", 4000), OutputPresent: true})
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(result)
	if len(body) > WorkflowResultSummaryMaxBytes {
		t.Fatalf("summary bytes=%d", len(body))
	}
	if result.OutputRetention != "unavailable" {
		t.Fatalf("output retention=%q", result.OutputRetention)
	}
}

func TestWorkflowContextIsProviderPortableAndRoleTyped(t *testing.T) {
	var baseline string
	for _, provider := range []string{"codex", "claude", "opencode"} {
		packet, err := BuildWorkflowContextPacket(WorkflowContextInput{
			Role: WorkflowRoleWorker, IssueID: "consumer-10", SourceRevision: "abc123",
			Summary: "provider-neutral task", Requirements: []string{"npm test passes"},
		})
		if err != nil {
			t.Fatalf("%s: %v", provider, err)
		}
		body, err := MarshalWorkflowContextPacket(packet)
		if err != nil {
			t.Fatalf("%s: %v", provider, err)
		}
		if strings.Contains(string(body), provider) {
			t.Fatalf("%s adapter detail leaked into packet: %s", provider, body)
		}
		if baseline == "" {
			baseline = string(body)
		} else if string(body) != baseline {
			t.Fatalf("provider changed canonical packet\nbase=%s\ngot=%s", baseline, body)
		}
	}
	for _, role := range []WorkflowRole{WorkflowRoleWorker, WorkflowRoleReviewer, WorkflowRoleIntegrator} {
		packet, err := BuildWorkflowContextPacket(WorkflowContextInput{Role: role, IssueID: "consumer-10", SourceRevision: "abc123"})
		if err != nil || packet.Role != role {
			t.Fatalf("role %s packet=%+v err=%v", role, packet, err)
		}
	}
	for _, role := range []WorkflowRole{WorkflowRoleWorker, WorkflowRoleReviewer, WorkflowRoleIntegrator} {
		result, err := BuildWorkflowResultSummary(WorkflowResultInput{Role: role, IssueID: "consumer-10", SourceRevision: "abc123", Status: "completed"})
		if err != nil || result.Role != role {
			t.Fatalf("role %s result=%+v err=%v", role, result, err)
		}
	}
	validator, err := BuildWorkflowContextPacket(WorkflowContextInput{Role: WorkflowRoleValidator, ScopeID: "repository:consumer", SourceRevision: "abc123"})
	if err != nil || validator.Provenance.IssueID != "" {
		t.Fatalf("repository validator packet=%+v err=%v", validator, err)
	}
	validatorResult, err := BuildWorkflowResultSummary(WorkflowResultInput{Role: WorkflowRoleValidator, ScopeID: "repository:consumer", SourceRevision: "abc123", Status: "completed"})
	if err != nil || validatorResult.Role != WorkflowRoleValidator {
		t.Fatalf("validator result=%+v err=%v", validatorResult, err)
	}
}

func TestWorkflowIssueContextRevisionIgnoresLifecycleChanges(t *testing.T) {
	task := Task{ID: "consumer-11", Title: "portable", Description: "npm package", Acceptance: "npm test passes", Type: TypeTask, Status: StatusOpen}
	want := WorkflowIssueContextRevision(task)
	task.Status = StatusInProgress
	task.Priority = P0
	if got := WorkflowIssueContextRevision(task); got != want {
		t.Fatalf("lifecycle-only change altered semantic revision: got %q want %q", got, want)
	}
	task.Acceptance = "npm run test:ci passes"
	if got := WorkflowIssueContextRevision(task); got == want {
		t.Fatal("requirement change did not alter semantic revision")
	}
}

func TestWorkflowIssueRequirementsRemainLabeledAfterCanonicalSort(t *testing.T) {
	got := WorkflowIssueRequirements(Task{Description: "what", Design: "how", Acceptance: "proof"})
	packet, err := BuildWorkflowContextPacket(WorkflowContextInput{Role: WorkflowRoleWorker, IssueID: "consumer-13", SourceRevision: "abc123", Requirements: got})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"acceptance: proof", "description: what", "design: how"}
	if strings.Join(packet.Requirements, "|") != strings.Join(want, "|") {
		t.Fatalf("requirements=%v want=%v", packet.Requirements, want)
	}
}

func TestWorkflowContextRejectsOversizedExactProvenance(t *testing.T) {
	_, err := BuildWorkflowContextPacket(WorkflowContextInput{Role: WorkflowRoleWorker, IssueID: "consumer-12", SourceRevision: strings.Repeat("a", 201)})
	if err == nil {
		t.Fatal("oversized source revision was silently truncated")
	}
}
