package daemon

import (
	"context"
	"encoding/json"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/naming"
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
	client := newMigratedIssueClientAtPath(t, filepath.Join(repoDir, "issues.db"), slog.Default())
	t.Cleanup(func() { _ = client.CloseDB() })
	issueID, err := client.Create(ctx, issues.CreateTaskParams{Title: "portable consumer change", Type: domain.TypeFeature, Priority: domain.P1, Status: domain.StatusInProgress})
	require.NoError(t, err)
	runtime := newOperationRuntime(operationRuntimeConfig{repoDir: repoDir})
	t.Cleanup(func() { _ = runtime.Close() })
	d := &Daemon{operationRuntime: runtime, revision: map[string]uint64{"project": 1}, issueClientsByProject: map[string]*issues.Client{"project": client}}

	recordBody, err := json.Marshal(protocol.PublicationEvidenceRecordRequest{Evidence: domain.PublicationEvidence{
		EvidenceID: "review-1", ProjectID: "spoofed", IssueID: issueID, Layer: domain.PublicationEvidencePatchReview,
		PatchDigest: "patch-a", SourceRevision: "source-a", BaseRevision: "base-a", Producer: "reviewer",
		PolicyVersion: "consumer-policy-v1", EnvironmentFingerprint: "node-22", Coverage: domain.PublicationEvidenceCoverage{Paths: []string{"src/api.ts"}},
	}})
	require.NoError(t, err)
	recordResp, err := d.handleValidationCommand(ctx, protocol.RequestEnvelope{ProtocolVersion: protocol.CurrentVersion, RequestID: "record", Kind: protocol.EnvelopeKindCommand, Command: protocol.CommandPublicationEvidenceRecord, Meta: protocol.Metadata{ProjectID: "project"}, Body: recordBody})
	require.NoError(t, err)
	require.True(t, recordResp.OK, recordResp.Error)
	var recorded protocol.PublicationEvidenceRecordResponse
	require.NoError(t, json.Unmarshal(recordResp.Body, &recorded))
	assert.Equal(t, "project", recorded.Evidence.ProjectID)
	assert.False(t, recorded.Evidence.CreatedAt.IsZero())

	evaluateBody, err := json.Marshal(protocol.PublicationEvidenceEvaluateRequest{
		IssueID:   issueID,
		Candidate: domain.PublicationEvidenceCandidate{PatchDigest: "patch-a", SourceRevision: "source-a", BaseRevision: "base-b", PolicyVersion: "consumer-policy-v1", EnvironmentFingerprint: "node-22", ImpactKnown: true, CapabilityAvailable: true, ChangedPaths: []string{"docs/readme.md"}},
		Policy:    domain.PublicationEvidencePolicy{Version: "consumer-policy-v1", ExactBaseSurfaces: []string{"wire"}, InvalidatePathOverlap: true, FailClosedUnknownImpact: true, RequireCapability: true},
	})
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
