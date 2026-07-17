package daemon

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	daemonstate "github.com/riordanpawley/azedarach/internal/daemon/state"
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/naming"
	gitservice "github.com/riordanpawley/azedarach/internal/services/git"
	"github.com/riordanpawley/azedarach/internal/services/issues"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidationCommandStartsPublicationWithoutAggregateQueueing(t *testing.T) {
	runtime := newOperationRuntime(operationRuntimeConfig{repoDir: t.TempDir()})
	t.Cleanup(func() { _ = runtime.Close() })
	d := &Daemon{operationRuntime: runtime, revision: map[string]uint64{"project": 1}}

	acquire := func(requestID string) domain.ValidationRequest {
		t.Helper()
		body, err := json.Marshal(protocol.ValidationAcquireRequest{RequestID: requestID, LeaseToken: "test-secret", Class: domain.ValidationClassAggregate, Scope: domain.ValidationScopeRepository, Purpose: domain.ValidationPurposePushGate, IsolationMode: "repository-family", EnvironmentFingerprint: "toolchain-a", Override: domain.ValidationOverrideNone, Profile: "cold", Command: "just test", SourceRevision: "abc123", TTLSeconds: 30})
		if err != nil {
			t.Fatal(err)
		}
		resp, err := d.handleValidationCommand(context.Background(), protocol.RequestEnvelope{ProtocolVersion: protocol.CurrentVersion, RequestID: naming.RequestID("rpc-" + requestID), Kind: protocol.EnvelopeKindCommand, Command: protocol.CommandValidationAcquire, Meta: protocol.Metadata{ProjectID: "project"}, Body: body})
		if err != nil || !resp.OK {
			t.Fatalf("acquire %s: response=%+v err=%v", requestID, resp, err)
		}
		var result protocol.ValidationRequestResponse
		if err := json.Unmarshal(resp.Body, &result); err != nil {
			t.Fatal(err)
		}
		return result.Request
	}

	if got := acquire("owner"); got.State != domain.ValidationRequestActive {
		t.Fatalf("owner state = %s, want active", got.State)
	}
	if got := acquire("second-publication"); got.State != domain.ValidationRequestActive {
		t.Fatalf("second publication state = %s, want active", got.State)
	}
	resp, err := d.handleValidationCommand(context.Background(), protocol.RequestEnvelope{ProtocolVersion: protocol.CurrentVersion, RequestID: "rpc-status", Kind: protocol.EnvelopeKindCommand, Command: protocol.CommandValidationStatus, Meta: protocol.Metadata{ProjectID: "project"}, Body: json.RawMessage(`{}`)})
	if err != nil || !resp.OK {
		t.Fatalf("status: response=%+v err=%v", resp, err)
	}
	var status protocol.ValidationStatusResponse
	if err := json.Unmarshal(resp.Body, &status); err != nil {
		t.Fatal(err)
	}
	if len(status.Snapshot.Active) != 2 || len(status.Snapshot.Queued) != 0 {
		t.Fatalf("snapshot = %+v, want both publication requests active", status.Snapshot)
	}
}

func TestPublicationCoverageForPathsRejectsNonRepoRelativeGitPaths(t *testing.T) {
	for _, candidate := range []string{"../escape.txt", "src/../../escape.txt", "/absolute.txt", `C:/absolute.txt`, `back\\slash.txt`} {
		if coverage, err := publicationCoverageForPaths([]string{candidate}, publicationEvidenceCapability{}); err == nil {
			t.Fatalf("publicationCoverageForPaths(%q) = %+v, want traversal or absolute path rejection", candidate, coverage)
		}
	}
}

func TestValidationAcquireBindsReviewAssignmentToDurableLease(t *testing.T) {
	ctx := context.Background()
	repoDir := t.TempDir()
	client := newMigratedIssueClientAtPath(t, filepath.Join(repoDir, "issues.db"), slog.Default())
	t.Cleanup(func() { _ = client.CloseDB() })
	issueID := createReviewTask(t, ctx, client, domain.P1, "worker-a")
	if _, err := client.ClaimOwnershipWithRuntime(ctx, "project", issueID, issues.OwnershipClaimParams{OwnerID: "assigned-reviewer", OwnerKind: "orchestrator", Purpose: domain.CoordinationLeaseReview}); err != nil {
		t.Fatal(err)
	}
	runtime := newOperationRuntime(operationRuntimeConfig{repoDir: repoDir})
	t.Cleanup(func() { _ = runtime.Close() })
	d := &Daemon{operationRuntime: runtime, revision: map[string]uint64{"project": 1}, issueClientsByProject: map[string]*issues.Client{"project": client}}

	acquire := func(requestID, reviewer string) protocol.ResponseEnvelope {
		t.Helper()
		body, err := json.Marshal(protocol.ValidationAcquireRequest{RequestID: requestID, LeaseToken: "secret", IssueID: issueID, Class: domain.ValidationClassAggregate, Scope: domain.ValidationScopeTicket, Purpose: domain.ValidationPurposeReviewEvidence, IsolationMode: "worktree", EnvironmentFingerprint: "toolchain-a", Override: domain.ValidationOverrideNone, Profile: "cold", Command: "just test", SourceRevision: "candidate-a", ReviewerID: reviewer, TTLSeconds: 30})
		if err != nil {
			t.Fatal(err)
		}
		resp, err := d.handleValidationCommand(ctx, protocol.RequestEnvelope{ProtocolVersion: protocol.CurrentVersion, RequestID: naming.RequestID("rpc-" + requestID), Kind: protocol.EnvelopeKindCommand, Command: protocol.CommandValidationAcquire, Meta: protocol.Metadata{ProjectID: "project"}, Body: body})
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}

	wrong := acquire("wrong-reviewer", "other-reviewer")
	if wrong.OK || !strings.Contains(wrong.Error.Message, "does not own review lease") {
		t.Fatalf("wrong reviewer response = %+v", wrong)
	}
	correct := acquire("assigned-reviewer", "assigned-reviewer")
	if !correct.OK {
		t.Fatalf("assigned reviewer response = %+v", correct)
	}
	var result protocol.ValidationRequestResponse
	if err := json.Unmarshal(correct.Body, &result); err != nil {
		t.Fatal(err)
	}
	if result.Request.ReviewerID != "assigned-reviewer" || result.Request.ReviewEpochEventID != latestReviewEpochEventID(t, ctx, client, issueID) {
		t.Fatalf("durable assignment = %+v", result.Request)
	}
}

func TestValidationAcquireTicketScopeFailsClosedForMissingTicket(t *testing.T) {
	ctx := context.Background()
	repoDir := t.TempDir()
	client := newMigratedIssueClientAtPath(t, filepath.Join(repoDir, "issues.db"), slog.Default())
	t.Cleanup(func() { _ = client.CloseDB() })
	runtime := newOperationRuntime(operationRuntimeConfig{repoDir: repoDir})
	t.Cleanup(func() { _ = runtime.Close() })
	d := &Daemon{operationRuntime: runtime, revision: map[string]uint64{"project": 1}, issueClientsByProject: map[string]*issues.Client{"project": client}}
	body, err := json.Marshal(protocol.ValidationAcquireRequest{RequestID: "missing", LeaseToken: "secret", IssueID: "missing-ticket", Class: domain.ValidationClassShared, Scope: domain.ValidationScopeTicket, Purpose: domain.ValidationPurposeDevelopment, IsolationMode: "worktree", EnvironmentFingerprint: "toolchain-a", Override: domain.ValidationOverrideNone, Profile: "focused", Command: "go test ./internal/domain", SourceRevision: "candidate-a", TTLSeconds: 30})
	require.NoError(t, err)
	resp, err := d.handleValidationCommand(ctx, protocol.RequestEnvelope{ProtocolVersion: protocol.CurrentVersion, RequestID: "rpc-missing", Kind: protocol.EnvelopeKindCommand, Command: protocol.CommandValidationAcquire, Meta: protocol.Metadata{ProjectID: "project"}, Body: body})
	require.NoError(t, err)
	require.False(t, resp.OK)
	assert.Contains(t, resp.Error.Message, "resolve ticket-scoped validation")
}

func TestValidationCommandRejectsSpoofedNestedAuthorization(t *testing.T) {
	runtime := newOperationRuntime(operationRuntimeConfig{repoDir: t.TempDir()})
	t.Cleanup(func() { _ = runtime.Close() })
	d := &Daemon{operationRuntime: runtime, revision: map[string]uint64{"project": 1}}
	ctx := context.Background()
	_, err := runtime.store.AcquireValidation(ctx, domain.ValidationAcquire{RequestID: "owner", LeaseToken: "secret", ProjectID: "project", IssueID: "dkg", Class: domain.ValidationClassAggregate, Profile: "cold", Command: "just test", SourceRevision: "abc123", TTL: time.Minute}, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(protocol.ValidationAuthorizeNestedRequest{RequestID: "owner", LeaseToken: "spoof", Class: domain.ValidationClassShared, Scope: domain.ValidationScopeTicket, Purpose: domain.ValidationPurposeDevelopment})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := d.handleValidationCommand(ctx, protocol.RequestEnvelope{ProtocolVersion: protocol.CurrentVersion, RequestID: "rpc-nested", Kind: protocol.EnvelopeKindCommand, Command: protocol.CommandValidationNested, Meta: protocol.Metadata{ProjectID: "project"}, Body: body})
	if err != nil {
		t.Fatal(err)
	}
	if resp.OK || !strings.Contains(resp.Error.Message, "lease token rejected") {
		t.Fatalf("response = %+v, want rejected spoofed nested authorization", resp)
	}
}

func TestPublicationEvidenceCommandsRetainPatchAcrossUnrelatedBaseMovement(t *testing.T) {
	ctx := context.Background()
	repoDir := t.TempDir()
	runPublicationGit(t, repoDir, "init", "-b", "main")
	runPublicationGit(t, repoDir, "config", "user.email", "test@example.com")
	runPublicationGit(t, repoDir, "config", "user.name", "Test")
	require.NoError(t, os.MkdirAll(filepath.Join(repoDir, ".azedarach"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(repoDir, ".azedarach", "config.json"), []byte(`{
  "publicationEvidence": {
    "policyVersion": "consumer-policy-v1",
    "activePathProfiles": ["consumer-integration"],
    "exactBaseSurfaces": {"wire": ["src/wire"]},
    "dependencies": {"api": ["src/api.ts"]}
  }
}`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(repoDir, ".gitignore"), []byte("issues.db*\nruntime.db*\n.azedarach/*\n!.azedarach/config.json\n"), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(repoDir, "src"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(repoDir, "src", "api.ts"), []byte("export const api = 1;\n"), 0o644))
	runPublicationGit(t, repoDir, "add", ".")
	runPublicationGit(t, repoDir, "commit", "-m", "base")
	runPublicationGit(t, repoDir, "checkout", "-b", "consumer-change")
	require.NoError(t, os.WriteFile(filepath.Join(repoDir, "src", "api.ts"), []byte("export const api = 2;\n"), 0o644))
	runPublicationGit(t, repoDir, "add", "src/api.ts")
	runPublicationGit(t, repoDir, "commit", "-m", "consumer change")
	sourceRevision := runPublicationGit(t, repoDir, "rev-parse", "HEAD")

	client := newMigratedIssueClientAtPath(t, filepath.Join(repoDir, "issues.db"), slog.Default())
	t.Cleanup(func() { _ = client.CloseDB() })
	issueID, err := client.Create(ctx, issues.CreateTaskParams{Title: "portable consumer change", Type: domain.TypeFeature, Priority: domain.P1, Status: domain.StatusInProgress})
	require.NoError(t, err)
	runtime := newOperationRuntime(operationRuntimeConfig{repoDir: repoDir})
	t.Cleanup(func() { _ = runtime.Close() })
	runtimeStore := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(repoDir, "runtime.db"), slog.Default())
	t.Cleanup(func() { _ = runtimeStore.Close() })
	require.NoError(t, runtimeStore.UpsertWorktreeState(ctx, daemonstate.WorktreeState{ProjectID: "project", IssueID: issueID, Path: repoDir, Branch: "consumer-change", UpdatedAt: time.Now().UTC()}))
	d := &Daemon{
		cfg: Config{RepoDir: repoDir, BaseBranch: "main", Logger: slog.Default()}, operationRuntime: runtime,
		revision: map[string]uint64{"project": 1}, issueClientsByProject: map[string]*issues.Client{"project": client},
		runtimeStoresByProject:   map[string]*daemonstate.RuntimeStateStore{"project": runtimeStore},
		publicationEvidenceCache: map[string]domain.PublicationEvidenceSnapshot{},
		git:                      gitservice.NewClient(gitservice.NewExecRunner(repoDir), slog.Default()),
	}

	started := time.Now().UTC()
	_, err = runtime.store.AcquireValidation(ctx, domain.ValidationAcquire{
		RequestID: "review-validation", LeaseToken: "secret", ProjectID: "project", IssueID: issueID,
		Class: domain.ValidationClassAggregate, Scope: domain.ValidationScopeTicket, Purpose: domain.ValidationPurposeReviewEvidence,
		IsolationMode: "worktree", EnvironmentFingerprint: "node-22", Profile: "consumer-integration", Command: "npm test",
		SourceRevision: sourceRevision, ReviewerID: "reviewer", ReviewEpochEventID: 1, TTL: time.Minute,
	}, started)
	require.NoError(t, err)
	_, err = runtime.store.FinishValidation(ctx, "review-validation", "secret", domain.ValidationRequestCompleted, "passed", domain.ValidationEvidence{
		Held: true, RequestID: "review-validation", Class: domain.ValidationClassAggregate, Scope: domain.ValidationScopeTicket,
		Purpose: domain.ValidationPurposeReviewEvidence, Profile: "consumer-integration", SourceRevision: sourceRevision, Present: true,
	}, started.Add(time.Second), time.Minute)
	require.NoError(t, err)
	require.Empty(t, runPublicationGit(t, repoDir, "status", "--porcelain=v1", "--untracked-files=all"))

	recordBody, err := json.Marshal(protocol.PublicationEvidenceRecordRequest{EvidenceID: "review-1", IssueID: issueID, Layer: domain.PublicationEvidencePatchReview, ValidationRequestID: "review-validation"})
	require.NoError(t, err)
	recordResp, err := d.handleValidationCommand(ctx, protocol.RequestEnvelope{ProtocolVersion: protocol.CurrentVersion, RequestID: "record", Kind: protocol.EnvelopeKindCommand, Command: protocol.CommandPublicationEvidenceRecord, Meta: protocol.Metadata{ProjectID: "project"}, Body: recordBody})
	require.NoError(t, err)
	require.True(t, recordResp.OK, recordResp.Error)
	var recorded protocol.PublicationEvidenceRecordResponse
	require.NoError(t, json.Unmarshal(recordResp.Body, &recorded))
	assert.Equal(t, "project", recorded.Evidence.ProjectID)
	assert.False(t, recorded.Evidence.CreatedAt.IsZero())
	assert.Equal(t, sourceRevision, recorded.Evidence.SourceRevision)
	assert.Equal(t, []string{"src/api.ts"}, recorded.Evidence.Coverage.Paths)

	baseWorktree := t.TempDir()
	runPublicationGit(t, repoDir, "worktree", "add", baseWorktree, "main")
	t.Cleanup(func() { runPublicationGit(t, repoDir, "worktree", "remove", "--force", baseWorktree) })
	require.NoError(t, os.MkdirAll(filepath.Join(baseWorktree, "docs"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(baseWorktree, "docs", "readme.md"), []byte("unrelated\n"), 0o644))
	runPublicationGit(t, baseWorktree, "add", "docs/readme.md")
	runPublicationGit(t, baseWorktree, "commit", "-m", "unrelated base movement")

	evaluateBody, err := json.Marshal(protocol.PublicationEvidenceEvaluateRequest{IssueID: issueID})
	require.NoError(t, err)
	evaluateResp, err := d.handleValidationCommand(ctx, protocol.RequestEnvelope{ProtocolVersion: protocol.CurrentVersion, RequestID: "evaluate", Kind: protocol.EnvelopeKindCommand, Command: protocol.CommandPublicationEvidenceEvaluate, Meta: protocol.Metadata{ProjectID: "project"}, Body: evaluateBody})
	require.NoError(t, err)
	require.True(t, evaluateResp.OK, evaluateResp.Error)
	var evaluated protocol.PublicationEvidenceEvaluateResponse
	require.NoError(t, json.Unmarshal(evaluateResp.Body, &evaluated))
	require.Len(t, evaluated.Assessments, 1)
	assert.True(t, evaluated.Assessments[0].Retained)
	assert.True(t, evaluated.Assessments[0].BaseMovementOnly)
}

func TestTaskCloseRetryRecoversReceiptAndRecordsExactSyntheticMergeEvidence(t *testing.T) {
	ctx := context.Background()
	repoDir := t.TempDir()
	runPublicationGit(t, repoDir, "init", "-b", "main")
	runPublicationGit(t, repoDir, "config", "user.email", "test@example.com")
	runPublicationGit(t, repoDir, "config", "user.name", "Test")
	require.NoError(t, os.MkdirAll(filepath.Join(repoDir, ".azedarach"), 0o755))
	configJSON := "{\n  \"publicationEvidence\": {\n    \"policyVersion\": \"portable-v1\",\n    \"activePathProfiles\": [\"consumer-integration\"],\n    \"exactBaseSurfaces\": {\"wire\": [\"src/wire\"]},\n    \"dependencies\": {\"api\": [\"src/api.ts\"]}\n  }\n}"
	require.NoError(t, os.WriteFile(filepath.Join(repoDir, ".azedarach", "config.json"), []byte(configJSON), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(repoDir, ".gitignore"), []byte(".azedarach/*\n!.azedarach/config.json\nissues.db*\n"), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(repoDir, "src"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(repoDir, "scripts"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(repoDir, "scripts", "git-merge-rebase-gate.sh"), []byte("#!/bin/sh\nexit 0\n"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(repoDir, "src", "api.ts"), []byte("export const api = 1;\n"), 0o644))
	runPublicationGit(t, repoDir, "add", ".")
	runPublicationGit(t, repoDir, "commit", "-m", "base")
	runPublicationGit(t, repoDir, "checkout", "-b", "feature")
	require.NoError(t, os.WriteFile(filepath.Join(repoDir, "src", "api.ts"), []byte("export const api = 2;\n"), 0o644))
	runPublicationGit(t, repoDir, "add", "src/api.ts")
	runPublicationGit(t, repoDir, "commit", "-m", "feature")
	sourceOID := runPublicationGit(t, repoDir, "rev-parse", "HEAD")
	runPublicationGit(t, repoDir, "checkout", "main")
	require.NoError(t, os.WriteFile(filepath.Join(repoDir, "main.txt"), []byte("divergent target\n"), 0o644))
	runPublicationGit(t, repoDir, "add", "main.txt")
	runPublicationGit(t, repoDir, "commit", "-m", "advance target")
	baseOID := runPublicationGit(t, repoDir, "rev-parse", "HEAD")

	runtime := newOperationRuntime(operationRuntimeConfig{repoDir: repoDir})
	t.Cleanup(func() { _ = runtime.Close() })
	issueClient := newMigratedIssueClientAtPath(t, filepath.Join(repoDir, "issues.db"), slog.Default())
	t.Cleanup(func() { _ = issueClient.CloseDB() })
	issueID, err := issueClient.Create(ctx, issues.CreateTaskParams{Title: "portable merge", Type: domain.TypeFeature, Priority: domain.P1, Status: domain.StatusInProgress})
	require.NoError(t, err)
	started := time.Now().UTC()
	_, err = runtime.store.AcquireValidation(ctx, domain.ValidationAcquire{
		RequestID: "merge-review", LeaseToken: "secret", ProjectID: "project", IssueID: issueID,
		Class: domain.ValidationClassAggregate, Scope: domain.ValidationScopeTicket, Purpose: domain.ValidationPurposeReviewEvidence,
		IsolationMode: "worktree", EnvironmentFingerprint: "go-portable", Profile: "consumer-integration", Command: "go test ./...",
		SourceRevision: sourceOID, ReviewerID: "reviewer", ReviewEpochEventID: 1, TTL: time.Minute,
	}, started)
	require.NoError(t, err)
	_, err = runtime.store.FinishValidation(ctx, "merge-review", "secret", domain.ValidationRequestCompleted, "passed", domain.ValidationEvidence{
		Held: true, RequestID: "merge-review", Class: domain.ValidationClassAggregate, Scope: domain.ValidationScopeTicket,
		Purpose: domain.ValidationPurposeReviewEvidence, Profile: "consumer-integration", SourceRevision: sourceOID, Present: true,
	}, started.Add(time.Second), time.Minute)
	require.NoError(t, err)
	gitClient := gitservice.NewClient(gitservice.NewExecRunner(repoDir), slog.Default())
	merge, err := gitClient.MergeCleanlyTransactional(ctx, repoDir, "feature")
	require.NoError(t, err)
	require.True(t, merge.Success)
	targetOID := runPublicationGit(t, repoDir, "rev-parse", "HEAD")
	d := &Daemon{
		cfg: Config{RepoDir: repoDir, BaseBranch: "main", Logger: slog.Default()}, operationRuntime: runtime, git: gitClient,
		publicationEvidenceCache: map[string]domain.PublicationEvidenceSnapshot{}, issueClientsByProject: map[string]*issues.Client{"project": issueClient},
	}
	integration := taskCloseIntegrationResult{
		Requested: true, Integrated: true, ConfiguredBaseTarget: true, TargetID: "base", SourceBranch: "feature", TargetBranch: "main",
		BaseOID: baseOID, SourceOID: sourceOID, TargetOID: targetOID, ValidationAttempts: merge.ValidationAttempts,
	}
	// Model the durable state after integration receipt succeeds but publication
	// evidence fails. The retry sees source already contained by target.
	err = d.persistTaskCloseIntegrationReceipt(ctx, "project", issueID, repoDir, integration)
	require.NoError(t, err)
	recovered, found, err := d.recoverPublishedTaskCloseIntegration(ctx, "project", issueID, repoDir, "base", "feature", "main", sourceOID, targetOID)
	require.NoError(t, err)
	require.True(t, found)
	require.True(t, recovered.NoChanges)
	require.True(t, recovered.ReceiptRecovered)
	require.False(t, recovered.Integrated)
	require.Equal(t, baseOID, recovered.BaseOID)
	require.NotEmpty(t, recovered.ValidationAttempts)
	err = d.persistTaskCloseIntegrationPublication(ctx, "project", issueID, repoDir, recovered)
	require.NoError(t, err, "merge=%+v target=%s", merge, targetOID)
	receipts, err := issueClient.ListIssueObservationEvents(ctx, issueID, issues.IssueObservationEventListOptions{Types: []domain.IssueObservationEventType{domain.IssueEventTaskIntegrationCompleted}, Limit: 10})
	require.NoError(t, err)
	require.Len(t, receipts, 1, "retry must reuse the exact receipt rather than append a generic no-change receipt")
	snapshot, err := runtime.store.PublicationEvidenceSnapshot(ctx, "project", issueID)
	require.NoError(t, err)
	require.Len(t, snapshot.Evidence, 1)
	assert.Equal(t, domain.PublicationEvidenceMergeResult, snapshot.Evidence[0].Layer)
	assert.Equal(t, baseOID, snapshot.Evidence[0].BaseRevision)
	assert.Equal(t, sourceOID, snapshot.Evidence[0].SourceRevision)
	assert.Equal(t, targetOID, snapshot.Evidence[0].ResultRevision)
	assert.Equal(t, []string{"src/api.ts"}, snapshot.Evidence[0].Coverage.Paths)
	_, assessments, err := d.evaluateCurrentPublicationEvidence(ctx, "project", issueID, time.Now().UTC())
	require.NoError(t, err)
	require.Len(t, assessments, 1)
	assert.True(t, assessments[0].Retained, "exact synthetic merge must remain active after the issue worktree is gone: %+v", assessments[0])
}

func TestTaskClosePublicationDistinguishesTypedBaseFromNonBaseComposition(t *testing.T) {
	ctx := context.Background()
	repoDir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(repoDir, ".azedarach"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(repoDir, ".azedarach", "config.json"), []byte(`{
  "publicationEvidence": {
    "policyVersion": "consumer-policy-v1",
    "activePathProfiles": ["consumer-integration"]
  }
}`), 0o644))
	issueClient := newMigratedIssueClientAtPath(t, filepath.Join(t.TempDir(), "issues.db"), slog.Default())
	t.Cleanup(func() { _ = issueClient.CloseDB() })
	issueID, err := issueClient.Create(ctx, issues.CreateTaskParams{Title: "child composition", Type: domain.TypeTask, Status: domain.StatusInReview})
	require.NoError(t, err)
	d := &Daemon{
		cfg:                   Config{RepoDir: repoDir, BaseBranch: "main", Logger: slog.Default()},
		issueClientsByProject: map[string]*issues.Client{"project": issueClient},
	}
	nonBase := taskCloseIntegrationResult{
		Requested: true, Integrated: true, ConfiguredBaseTarget: false,
		TargetID: "parent-issue", SourceBranch: "child", TargetBranch: "parent", BaseOID: "base", SourceOID: "source", TargetOID: "result",
	}
	require.NoError(t, d.persistTaskCloseIntegrationPublication(ctx, "project", issueID, repoDir, nonBase))
	events, err := issueClient.ListIssueObservationEvents(ctx, issueID, issues.IssueObservationEventListOptions{Types: []domain.IssueObservationEventType{domain.IssueEventTaskIntegrationCompleted}, Limit: 10})
	require.NoError(t, err)
	require.Len(t, events, 1)
	assert.Equal(t, false, events[0].Payload["configured_base_target"])

	base := nonBase
	base.ConfiguredBaseTarget = true
	base.TargetID = "base"
	base.SourceBranch = "root"
	base.TargetBranch = "main"
	err = d.persistTaskCloseIntegrationPublication(ctx, "project", issueID, repoDir, base)
	require.Error(t, err, "typed configured-base integration must enter exact publication validation")
	assert.Contains(t, err.Error(), "passed canonical validation")
}

func runPublicationGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", dir}, args...)...)
	output, err := command.CombinedOutput()
	require.NoError(t, err, "git %s: %s", strings.Join(args, " "), output)
	return strings.TrimSpace(string(output))
}

func TestPublicationEvidenceProjectionRefreshesAcrossDaemons(t *testing.T) {
	ctx := context.Background()
	repoDir := t.TempDir()
	firstRuntime := newOperationRuntime(operationRuntimeConfig{repoDir: repoDir})
	secondRuntime := newOperationRuntime(operationRuntimeConfig{repoDir: repoDir})
	t.Cleanup(func() { _ = firstRuntime.Close() })
	t.Cleanup(func() { _ = secondRuntime.Close() })
	secondDaemon := &Daemon{operationRuntime: secondRuntime, publicationEvidenceCache: map[string]domain.PublicationEvidenceSnapshot{}}

	evidence := domain.PublicationEvidence{
		EvidenceID: "review-1", ProjectID: "project", IssueID: "consumer-1", Layer: domain.PublicationEvidencePatchReview,
		PatchDigest: "patch-a", SourceRevision: "source-a", Producer: "reviewer", PolicyVersion: "policy-v1",
		EnvironmentFingerprint: "node-22", CreatedAt: time.Now().UTC(),
	}
	_, err := firstRuntime.store.RecordPublicationEvidence(ctx, evidence)
	require.NoError(t, err)
	initial, err := secondDaemon.publicationEvidenceSnapshot(ctx, "project", evidence.IssueID)
	require.NoError(t, err)
	require.Len(t, initial.Evidence, 1)

	evidence.EvidenceID = "active-path-1"
	evidence.Layer = domain.PublicationEvidenceActivePath
	_, err = firstRuntime.store.RecordPublicationEvidence(ctx, evidence)
	require.NoError(t, err)
	refreshed, err := secondDaemon.publicationEvidenceSnapshot(ctx, "project", evidence.IssueID)
	require.NoError(t, err)
	assert.Len(t, refreshed.Evidence, 2, "second daemon must refresh durable projection before reading its cache")
}

func TestPublicationEvidenceProjectionRejectsStaleConcurrentRefresh(t *testing.T) {
	ctx := context.Background()
	repoDir := t.TempDir()
	writer := newOperationRuntime(operationRuntimeConfig{repoDir: repoDir})
	reader := newOperationRuntime(operationRuntimeConfig{repoDir: repoDir})
	t.Cleanup(func() { _ = writer.Close() })
	t.Cleanup(func() { _ = reader.Close() })
	d := &Daemon{operationRuntime: reader, publicationEvidenceCache: map[string]domain.PublicationEvidenceSnapshot{}}
	evidence := domain.PublicationEvidence{
		EvidenceID: "review-1", ProjectID: "project", IssueID: "consumer-1", Layer: domain.PublicationEvidencePatchReview,
		PatchDigest: "patch-a", SourceRevision: "source-a", Producer: "reviewer", PolicyVersion: "policy-v1",
		EnvironmentFingerprint: "node-22", CreatedAt: time.Now().UTC(),
	}
	_, err := writer.store.RecordPublicationEvidence(ctx, evidence)
	require.NoError(t, err)

	firstRead := make(chan struct{})
	releaseFirst := make(chan struct{})
	var refreshes atomic.Int32
	d.publicationEvidenceAfterRefresh = func(snapshot domain.PublicationEvidenceSnapshot) {
		if refreshes.Add(1) == 1 {
			close(firstRead)
			<-releaseFirst
		}
	}
	firstResult := make(chan domain.PublicationEvidenceSnapshot, 1)
	firstError := make(chan error, 1)
	go func() {
		snapshot, readErr := d.publicationEvidenceSnapshot(ctx, "project", evidence.IssueID)
		firstResult <- snapshot
		firstError <- readErr
	}()
	<-firstRead
	evidence.EvidenceID = "active-path-1"
	evidence.Layer = domain.PublicationEvidenceActivePath
	_, err = writer.store.RecordPublicationEvidence(ctx, evidence)
	require.NoError(t, err)
	newer, err := d.publicationEvidenceSnapshot(ctx, "project", evidence.IssueID)
	require.NoError(t, err)
	require.Equal(t, int64(2), newer.Revision)
	require.Len(t, newer.Evidence, 2)
	close(releaseFirst)
	require.NoError(t, <-firstError)
	staleCallerResult := <-firstResult
	assert.Equal(t, int64(2), staleCallerResult.Revision)
	assert.Len(t, staleCallerResult.Evidence, 2, "an N refresh must return the selected N+1 cache snapshot")
}
