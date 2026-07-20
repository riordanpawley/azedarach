package daemon

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	operationstore "github.com/riordanpawley/azedarach/internal/daemon/operations/store"
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

func TestValidationFinishReturnsBoundedPortableFailureSummary(t *testing.T) {
	repoDir := t.TempDir()
	runtime := newOperationRuntime(operationRuntimeConfig{repoDir: repoDir})
	t.Cleanup(func() { _ = runtime.Close() })
	d := &Daemon{cfg: Config{WorkflowArtifactDir: filepath.Join(repoDir, "retained-artifacts")}, operationRuntime: runtime, revision: map[string]uint64{"consumer-project": 1}}
	ctx := context.Background()
	acquireBody, err := json.Marshal(protocol.ValidationAcquireRequest{
		RequestID: "npm-check", LeaseToken: "secret", Class: domain.ValidationClassAggregate,
		Scope: domain.ValidationScopeRepository, Purpose: domain.ValidationPurposePushGate,
		IsolationMode: "consumer-runner", EnvironmentFingerprint: "node-22", Override: domain.ValidationOverrideNone,
		Profile: "consumer-check", Command: "npm test", SourceRevision: "candidate-123", TTLSeconds: 30,
	})
	require.NoError(t, err)
	acquired, err := d.handleValidationCommand(ctx, protocol.RequestEnvelope{ProtocolVersion: protocol.CurrentVersion, RequestID: "acquire", Kind: protocol.EnvelopeKindCommand, Command: protocol.CommandValidationAcquire, Meta: protocol.Metadata{ProjectID: "consumer-project"}, Body: acquireBody})
	require.NoError(t, err)
	require.True(t, acquired.OK, "response=%+v", acquired)

	retainedOutput := filepath.Join(repoDir, "full-output.log")
	require.NoError(t, os.WriteFile(retainedOutput, []byte("complete npm output\n"), 0o600))
	finishBody, err := json.Marshal(protocol.ValidationFinishRequest{
		RequestID: "npm-check", LeaseToken: "secret", State: domain.ValidationRequestFailed, Outcome: "exit 1",
		Evidence: domain.ValidationEvidence{
			Held: true, RequestID: "npm-check", Class: domain.ValidationClassAggregate,
			Scope: domain.ValidationScopeRepository, Purpose: domain.ValidationPurposePushGate,
			Profile: "consumer-check", SourceRevision: "candidate-123", Present: true,
			FailureSummary: strings.Repeat("npm assertion failed ", 2000), ReportPaths: []string{retainedOutput},
		},
	})
	require.NoError(t, err)
	finished, err := d.handleValidationCommand(ctx, protocol.RequestEnvelope{ProtocolVersion: protocol.CurrentVersion, RequestID: "finish", Kind: protocol.EnvelopeKindCommand, Command: protocol.CommandValidationFinish, Meta: protocol.Metadata{ProjectID: "consumer-project"}, Body: finishBody})
	require.NoError(t, err)
	require.True(t, finished.OK, "response=%+v error=%+v", finished, finished.Error)
	var result protocol.ValidationRequestResponse
	require.NoError(t, json.Unmarshal(finished.Body, &result))
	assert.LessOrEqual(t, len(finished.Body), domain.WorkflowResultSummaryMaxBytes)
	assert.NotContains(t, string(finished.Body), retainedOutput)
	assert.Empty(t, result.Request.Command)
	assert.Empty(t, result.Request.Evidence.ReportPaths)
	summary, err := json.Marshal(result.Summary)
	require.NoError(t, err)
	assert.LessOrEqual(t, len(summary), domain.WorkflowResultSummaryMaxBytes)
	assert.Equal(t, domain.WorkflowRoleValidator, result.Summary.Role)
	assert.Equal(t, domain.WorkflowRoleValidator, result.Context.Role)
	assert.Equal(t, "repository:consumer-project", result.Context.Provenance.ScopeID)
	assert.Contains(t, result.Context.Requirements, "command: npm test")
	assert.Equal(t, "repository:consumer-project", result.Summary.Provenance.ScopeID)
	assert.Empty(t, result.Summary.Provenance.IssueID, "repository validation must not fabricate a ticket")
	assert.Equal(t, "candidate-123", result.Summary.Provenance.SourceRevision)
	assert.Equal(t, "retained", result.Summary.OutputRetention)
	assert.NotContains(t, string(summary), retainedOutput)
	reference := result.Summary.ArtifactLinks[0]
	assert.Contains(t, reference.Reference, "artifact:sha256/")
	assert.Contains(t, reference.Digest, "sha256:")
	retainedFile, _, _, err := d.openValidationArtifact("consumer-project", reference.Reference)
	require.NoError(t, err)
	require.NoError(t, retainedFile.Close())
	retainedPath := filepath.Join(d.validationArtifactRoot("consumer-project"), strings.TrimPrefix(reference.Reference, "artifact:sha256/"))
	require.NoError(t, os.WriteFile(retainedOutput, []byte("mutated caller output\n"), 0o600))
	retained, err := os.ReadFile(retainedPath)
	require.NoError(t, err)
	assert.Equal(t, "complete npm output\n", string(retained), "retained artifact must be independent of source mutation")
	require.NoError(t, os.Remove(retainedOutput))
	retained, err = os.ReadFile(retainedPath)
	require.NoError(t, err)
	assert.Equal(t, "complete npm output\n", string(retained), "retained artifact must survive source deletion")
	require.NoError(t, os.Chmod(retainedPath, 0o600))
	require.NoError(t, os.WriteFile(retainedPath, []byte("corrupt"), 0o600))
	_, _, _, err = d.openValidationArtifact("consumer-project", reference.Reference)
	assert.ErrorContains(t, err, "digest mismatch")
}

func TestOpenValidationArtifactPinsVerifiedFileAcrossPathReplacement(t *testing.T) {
	root := t.TempDir()
	d := &Daemon{cfg: Config{WorkflowArtifactDir: filepath.Join(root, "artifacts")}}
	source := filepath.Join(root, "output.log")
	require.NoError(t, os.WriteFile(source, []byte("verified output\n"), 0o600))
	reference, err := d.retainValidationArtifact("project-a", source)
	require.NoError(t, err)

	file, _, _, err := d.openValidationArtifact("project-a", reference.Reference)
	require.NoError(t, err)
	defer file.Close()
	retainedPath := filepath.Join(d.validationArtifactRoot("project-a"), strings.TrimPrefix(reference.Reference, "artifact:sha256/"))
	require.NoError(t, os.Remove(retainedPath))
	require.NoError(t, os.WriteFile(retainedPath, []byte("replacement output\n"), 0o600))

	content, err := io.ReadAll(file)
	require.NoError(t, err)
	assert.Equal(t, "verified output\n", string(content), "serving must use the descriptor whose content was verified")
}

func TestOpenValidationArtifactRejectsSymlinkTraversal(t *testing.T) {
	root := t.TempDir()
	content := []byte("outside artifact\n")
	digest := fmt.Sprintf("%x", sha256.Sum256(content))
	reference := "artifact:sha256/" + digest

	t.Run("digest path", func(t *testing.T) {
		artifactRoot := filepath.Join(root, "digest-link-artifacts")
		d := &Daemon{cfg: Config{WorkflowArtifactDir: artifactRoot}}
		projectRoot := d.validationArtifactRoot("project-a")
		require.NoError(t, os.MkdirAll(projectRoot, 0o700))
		outside := filepath.Join(root, "outside-digest")
		require.NoError(t, os.WriteFile(outside, content, 0o600))
		require.NoError(t, os.Symlink(outside, filepath.Join(projectRoot, digest)))

		_, _, _, err := d.openValidationArtifact("project-a", reference)
		require.ErrorContains(t, err, "unavailable")
	})

	t.Run("artifact root component", func(t *testing.T) {
		outsideRoot := filepath.Join(root, "outside-root")
		directRoot := filepath.Join(root, "direct-root")
		require.NoError(t, os.MkdirAll(filepath.Join(outsideRoot, "project-a", "sha256"), 0o700))
		require.NoError(t, os.WriteFile(filepath.Join(outsideRoot, "project-a", "sha256", digest), content, 0o400))
		require.NoError(t, os.Symlink(outsideRoot, directRoot))
		d := &Daemon{cfg: Config{WorkflowArtifactDir: directRoot}}

		_, _, _, err := d.openValidationArtifact("project-a", reference)
		require.ErrorContains(t, err, "unavailable")
	})

	t.Run("artifact root parent component", func(t *testing.T) {
		outsideParent := filepath.Join(root, "outside-parent")
		require.NoError(t, os.MkdirAll(filepath.Join(outsideParent, "artifacts", "validation"), 0o700))
		linkedParent := filepath.Join(root, "linked-parent")
		require.NoError(t, os.Symlink(outsideParent, linkedParent))
		d := &Daemon{cfg: Config{WorkflowArtifactDir: filepath.Join(linkedParent, "artifacts")}}

		_, _, _, err := d.openValidationArtifact("project-a", reference)
		require.ErrorContains(t, err, "unavailable")
	})
}

func TestOpenValidationArtifactRootRejectsDotSegmentsBeforeNormalization(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(root, "outside")
	require.NoError(t, os.MkdirAll(filepath.Join(outside, "artifacts"), 0o700))
	require.NoError(t, os.Symlink(outside, filepath.Join(root, "link")))

	unsafePath := root + string(filepath.Separator) + "link" + string(filepath.Separator) + ".." + string(filepath.Separator) + "outside" + string(filepath.Separator) + "artifacts"
	opened, err := openValidationArtifactRoot(unsafePath, nil)
	if opened != nil {
		_ = opened.Close()
	}
	require.ErrorContains(t, err, "invalid validation artifact root component")
}

func TestOpenValidationArtifactRootSupportsPlatformCanonicalTempPrefix(t *testing.T) {
	root := t.TempDir()
	opened, err := openValidationArtifactRoot(root, nil)
	require.NoError(t, err)
	require.NoError(t, opened.Close())
}

func TestValidationArtifactReadCommandIsProjectScopedAndDigestVerified(t *testing.T) {
	root := t.TempDir()
	d := &Daemon{cfg: Config{WorkflowArtifactDir: filepath.Join(root, "artifacts")}}
	source := filepath.Join(root, "output.log")
	require.NoError(t, os.WriteFile(source, []byte("complete output\n"), 0o600))
	reference, err := d.retainValidationArtifact("project-a", source)
	require.NoError(t, err)
	body, err := json.Marshal(protocol.ValidationArtifactReadRequest{Reference: reference.Reference})
	require.NoError(t, err)

	read, err := d.handleValidationCommand(context.Background(), protocol.RequestEnvelope{ProtocolVersion: protocol.CurrentVersion, RequestID: "read", Kind: protocol.EnvelopeKindCommand, Command: protocol.CommandValidationArtifactRead, Meta: protocol.Metadata{ProjectID: "project-a"}, Body: body})
	require.NoError(t, err)
	require.True(t, read.OK, "response=%+v", read)
	var result protocol.ValidationArtifactReadResponse
	require.NoError(t, json.Unmarshal(read.Body, &result))
	assert.Equal(t, "complete output\n", string(result.Content))
	assert.Equal(t, reference.Digest, result.Digest)
	assert.True(t, result.Complete)
	assert.Equal(t, int64(len(result.Content)), result.TotalSize)
	chunkBody, err := json.Marshal(protocol.ValidationArtifactReadRequest{Reference: reference.Reference, Limit: 4})
	require.NoError(t, err)
	chunk, err := d.handleValidationCommand(context.Background(), protocol.RequestEnvelope{ProtocolVersion: protocol.CurrentVersion, RequestID: "read-chunk", Kind: protocol.EnvelopeKindCommand, Command: protocol.CommandValidationArtifactRead, Meta: protocol.Metadata{ProjectID: "project-a"}, Body: chunkBody})
	require.NoError(t, err)
	require.True(t, chunk.OK, "response=%+v", chunk)
	var firstChunk protocol.ValidationArtifactReadResponse
	require.NoError(t, json.Unmarshal(chunk.Body, &firstChunk))
	assert.Equal(t, "comp", string(firstChunk.Content))
	assert.Equal(t, int64(4), firstChunk.NextOffset)
	assert.False(t, firstChunk.Complete)
	nextChunkBody, err := json.Marshal(protocol.ValidationArtifactReadRequest{Reference: reference.Reference, Offset: firstChunk.NextOffset, Limit: 4})
	require.NoError(t, err)
	nextChunk, err := d.handleValidationCommand(context.Background(), protocol.RequestEnvelope{ProtocolVersion: protocol.CurrentVersion, RequestID: "read-next-chunk", Kind: protocol.EnvelopeKindCommand, Command: protocol.CommandValidationArtifactRead, Meta: protocol.Metadata{ProjectID: "project-a"}, Body: nextChunkBody})
	require.NoError(t, err)
	require.True(t, nextChunk.OK, "response=%+v", nextChunk)
	var secondChunk protocol.ValidationArtifactReadResponse
	require.NoError(t, json.Unmarshal(nextChunk.Body, &secondChunk))
	assert.Equal(t, "lete", string(secondChunk.Content))
	assert.Equal(t, firstChunk.NextOffset, secondChunk.Offset)

	unauthorized, err := d.handleValidationCommand(context.Background(), protocol.RequestEnvelope{ProtocolVersion: protocol.CurrentVersion, RequestID: "read-other", Kind: protocol.EnvelopeKindCommand, Command: protocol.CommandValidationArtifactRead, Meta: protocol.Metadata{ProjectID: "project-b"}, Body: body})
	require.NoError(t, err)
	assert.False(t, unauthorized.OK)
	assert.Contains(t, unauthorized.Error.Message, "unavailable")

	badBody, err := json.Marshal(protocol.ValidationArtifactReadRequest{Reference: "artifact:sha256/../../secret"})
	require.NoError(t, err)
	bad, err := d.handleValidationCommand(context.Background(), protocol.RequestEnvelope{ProtocolVersion: protocol.CurrentVersion, RequestID: "read-bad", Kind: protocol.EnvelopeKindCommand, Command: protocol.CommandValidationArtifactRead, Meta: protocol.Metadata{ProjectID: "project-a"}, Body: badBody})
	require.NoError(t, err)
	assert.False(t, bad.OK)
	assert.Contains(t, bad.Error.Message, "invalid validation artifact")

	tooLargeBody, err := json.Marshal(protocol.ValidationArtifactReadRequest{Reference: reference.Reference, Limit: protocol.ValidationArtifactReadMaxBytes + 1})
	require.NoError(t, err)
	tooLarge, err := d.handleValidationCommand(context.Background(), protocol.RequestEnvelope{ProtocolVersion: protocol.CurrentVersion, RequestID: "read-large", Kind: protocol.EnvelopeKindCommand, Command: protocol.CommandValidationArtifactRead, Meta: protocol.Metadata{ProjectID: "project-a"}, Body: tooLargeBody})
	require.NoError(t, err)
	assert.False(t, tooLarge.OK)
	assert.Contains(t, tooLarge.Error.Message, "limit exceeds")
}

func TestRetainValidationArtifactFailsClosedWhenStorageUnavailable(t *testing.T) {
	root := t.TempDir()
	blocked := filepath.Join(root, "not-a-directory")
	require.NoError(t, os.WriteFile(blocked, []byte("block"), 0o600))
	source := filepath.Join(root, "output.log")
	require.NoError(t, os.WriteFile(source, []byte("output"), 0o600))
	d := &Daemon{cfg: Config{WorkflowArtifactDir: blocked}}
	_, err := d.retainValidationArtifact("project", source)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not a directory")
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
	if _, err := client.AppendIssueObservationEvent(ctx, issueID, issues.IssueObservationEventParams{Type: domain.IssueEventEvidenceSubmitted, Source: "worker", Payload: mustWorkerEvidencePayload(t)}); err != nil {
		t.Fatal(err)
	}
	admission, err := client.CaptureReviewAdmissionPin(ctx, issueID)
	if err != nil || admission.Evidence == nil {
		t.Fatalf("capture review admission = %+v err=%v", admission, err)
	}
	if _, err := client.ClaimOwnershipWithRuntime(ctx, "project", issueID, issues.OwnershipClaimParams{OwnerID: "assigned-reviewer", OwnerKind: "orchestrator", Purpose: domain.CoordinationLeaseReview, ExpectedReviewAdmission: &admission, ReviewSourceOID: "candidate-a"}); err != nil {
		t.Fatal(err)
	}
	publication := domain.PublicationOperation{
		OperationID: "publication-validation-authority", ProjectID: "project", IssueID: issueID, IntentKey: "accepted-validation",
		RequestFingerprint: "fingerprint", ActorID: "assigned-reviewer", ReviewerKind: domain.ReviewerOwnerKindOrchestrator,
		ReviewEpochEventID: admission.ReviewEpochEventID, TargetID: "base", TargetBranch: "main", SourceRevision: "candidate-a",
		BaseRevision: "base", PolicyVersion: "policy", EnvironmentFingerprint: "toolchain", ValidationCommand: "just test", State: domain.PublicationOperationQueued, CreatedAt: time.Now().UTC(),
	}
	publication.PatchEvidenceID = publication.OperationID
	patchEvidence := domain.PublicationEvidence{EvidenceID: publication.PatchEvidenceID, ProjectID: publication.ProjectID, IssueID: publication.IssueID, Layer: domain.PublicationEvidencePatchReview, PatchDigest: "patch", SourceRevision: publication.SourceRevision, BaseRevision: publication.BaseRevision, Producer: "reviewer:" + publication.ActorID, PolicyVersion: publication.PolicyVersion, EnvironmentFingerprint: publication.EnvironmentFingerprint, CreatedAt: publication.CreatedAt}
	queueStore := operationstore.NewAtPath(filepath.Join(repoDir, "issues.db"), nil)
	t.Cleanup(func() { _ = queueStore.Close() })
	if _, err := queueStore.PublicationOperations(ctx, "project", "", false); err != nil {
		t.Fatal(err)
	}
	receipt, err := client.AppendAcceptedReviewAndPublicationWithReviewAdmission(ctx, issueID, issues.IssueObservationEventParams{
		Type: domain.IssueEventReviewCompleted, Source: "daemon-orchestration", SourceCommand: "review-accept", Payload: map[string]any{
			"outcome": string(domain.ReviewOutcomeAccepted), "intent_key": publication.IntentKey, "request_fingerprint": publication.RequestFingerprint,
			"reviewed_evidence_source": admission.Evidence.Source, "reviewed_evidence_event_id": admission.Evidence.EventID,
			"reviewed_evidence_seq": admission.Evidence.Seq, "reviewed_evidence_digest": admission.Evidence.Digest,
		},
	}, publication, patchEvidence, "candidate-a", admission, "", "assigned-reviewer")
	if err != nil {
		t.Fatal(err)
	}
	runtime := newOperationRuntime(operationRuntimeConfig{repoDir: repoDir})
	t.Cleanup(func() { _ = runtime.Close() })
	d := &Daemon{operationRuntime: runtime, revision: map[string]uint64{"project": 1}, issueClientsByProject: map[string]*issues.Client{"project": client}}

	acquire := func(requestID, reviewer string) protocol.ResponseEnvelope {
		t.Helper()
		body, err := json.Marshal(protocol.ValidationAcquireRequest{RequestID: requestID, LeaseToken: "secret", IssueID: issueID, Class: domain.ValidationClassAggregate, Scope: domain.ValidationScopeTicket, Purpose: domain.ValidationPurposeReviewEvidence, IsolationMode: "worktree", EnvironmentFingerprint: "toolchain-a", Override: domain.ValidationOverrideNone, Profile: "cold", Command: "just test", SourceRevision: "candidate-a", ReviewerID: reviewer, ReviewerKind: domain.ReviewerOwnerKindOrchestrator, ReviewEpochEventID: admission.ReviewEpochEventID, PublicationOperationID: receipt.PublicationOperationID, AcceptedReviewEventID: receipt.EventID, AcceptedPublicationOperationID: receipt.PublicationOperationID, TTLSeconds: 30})
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
	if wrong.OK || !strings.Contains(wrong.Error.Message, "does not own typed review lease") {
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
	if result.Request.ReviewerID != "assigned-reviewer" || result.Request.ReviewerKind != domain.ReviewerOwnerKindOrchestrator || result.Request.ReviewEpochEventID != latestReviewEpochEventID(t, ctx, client, issueID) || result.Request.PublicationOperationID != receipt.PublicationOperationID || result.Request.AcceptedReviewEventID != receipt.EventID {
		t.Fatalf("durable assignment = %+v", result.Request)
	}
}

func TestValidationAcquireCapturesDaemonAuthoritativeIssuePriority(t *testing.T) {
	ctx := context.Background()
	repoDir := t.TempDir()
	client := newMigratedIssueClientAtPath(t, filepath.Join(repoDir, "issues.db"), slog.Default())
	t.Cleanup(func() { _ = client.CloseDB() })
	issueID, err := client.Create(ctx, issues.CreateTaskParams{Title: "Urgent focused retry", Type: domain.TypeBug, Priority: domain.P0, Status: domain.StatusInProgress})
	require.NoError(t, err)
	runtime := newOperationRuntime(operationRuntimeConfig{repoDir: repoDir})
	t.Cleanup(func() { _ = runtime.Close() })
	d := &Daemon{operationRuntime: runtime, revision: map[string]uint64{"project": 1}, issueClientsByProject: map[string]*issues.Client{"project": client}}

	body, err := json.Marshal(protocol.ValidationAcquireRequest{RequestID: "urgent", LeaseToken: "secret", IssueID: issueID, Class: domain.ValidationClassShared, Scope: domain.ValidationScopeTicket, Purpose: domain.ValidationPurposeCapacity, IsolationMode: "worktree", EnvironmentFingerprint: "toolchain-a", Override: domain.ValidationOverrideNone, Profile: "focused", Command: "go test ./internal/daemon", SourceRevision: "candidate-a", TTLSeconds: 30})
	require.NoError(t, err)
	resp, err := d.handleValidationCommand(ctx, protocol.RequestEnvelope{ProtocolVersion: protocol.CurrentVersion, RequestID: "rpc-urgent", Kind: protocol.EnvelopeKindCommand, Command: protocol.CommandValidationAcquire, Meta: protocol.Metadata{ProjectID: "project"}, Body: body})
	require.NoError(t, err)
	require.True(t, resp.OK, "response=%+v", resp)
	var result protocol.ValidationRequestResponse
	require.NoError(t, json.Unmarshal(resp.Body, &result))
	assert.Equal(t, domain.P0, result.Request.IssuePriority)
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
		SourceRevision: sourceRevision, ReviewerID: "reviewer", ReviewerKind: domain.ReviewerOwnerKindOrchestrator, ReviewEpochEventID: 1, PublicationOperationID: "publication", AcceptedReviewEventID: 2, AcceptedPublicationOperationID: "publication", TTL: time.Minute,
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

func TestAcceptedIndependentReviewRecordsPatchEvidenceIdempotently(t *testing.T) {
	ctx := context.Background()
	repoDir := t.TempDir()
	runPublicationGit(t, repoDir, "init", "-b", "main")
	runPublicationGit(t, repoDir, "config", "user.email", "test@example.com")
	runPublicationGit(t, repoDir, "config", "user.name", "Test")
	require.NoError(t, os.MkdirAll(filepath.Join(repoDir, ".azedarach"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(repoDir, ".azedarach", "config.json"), []byte(`{
  "publicationEvidence": {"policyVersion":"portable-v1","activePathProfiles":[],"exactBaseSurfaces":{},"dependencies":{}}
}`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(repoDir, ".gitignore"), []byte("issues.db*\n.azedarach/*\n!.azedarach/config.json\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(repoDir, "base.txt"), []byte("base\n"), 0o644))
	runPublicationGit(t, repoDir, "add", ".")
	runPublicationGit(t, repoDir, "commit", "-m", "base")
	base := runPublicationGit(t, repoDir, "rev-parse", "HEAD")
	runPublicationGit(t, repoDir, "checkout", "-b", "feature")
	require.NoError(t, os.WriteFile(filepath.Join(repoDir, "feature.txt"), []byte("portable\n"), 0o644))
	runPublicationGit(t, repoDir, "add", "feature.txt")
	runPublicationGit(t, repoDir, "commit", "-m", "feature")
	head := runPublicationGit(t, repoDir, "rev-parse", "HEAD")

	client := newMigratedIssueClientAtPath(t, filepath.Join(repoDir, "issues.db"), slog.Default())
	t.Cleanup(func() { _ = client.CloseDB() })
	issueID, err := client.Create(ctx, issues.CreateTaskParams{Title: "portable review", Type: domain.TypeFeature, Priority: domain.P1, Status: domain.StatusInReview})
	require.NoError(t, err)
	runtime := newOperationRuntime(operationRuntimeConfig{repoDir: repoDir})
	t.Cleanup(func() { _ = runtime.Close() })
	d := &Daemon{
		cfg: Config{RepoDir: repoDir, BaseBranch: "main", Logger: slog.Default()}, operationRuntime: runtime,
		issueClientsByProject: map[string]*issues.Client{"project": client}, publicationEvidenceCache: map[string]domain.PublicationEvidenceSnapshot{},
		git: gitservice.NewClient(gitservice.NewExecRunner(repoDir), slog.Default()),
	}
	inspection := protocol.OrchestrationReview{IssueID: issueID, ReviewEpochEventID: 17, WorktreePath: repoDir, SourceOID: head, DiffBaseRevision: base}
	require.NoError(t, d.recordAcceptedPatchReviewEvidence(ctx, "project", "independent-reviewer", inspection))
	require.NoError(t, d.recordAcceptedPatchReviewEvidence(ctx, "project", "independent-reviewer", inspection))
	snapshot, err := runtime.store.PublicationEvidenceSnapshot(ctx, "project", issueID)
	require.NoError(t, err)
	require.Len(t, snapshot.Evidence, 1)
	assert.Equal(t, domain.PublicationEvidencePatchReview, snapshot.Evidence[0].Layer)
	assert.Equal(t, "reviewer:independent-reviewer", snapshot.Evidence[0].Producer)
	assert.Equal(t, []string{"feature.txt"}, snapshot.Evidence[0].Coverage.Paths)
}

func TestTaskCloseRetryRecoversReceiptAndRecordsExactSyntheticMergeEvidence(t *testing.T) {
	ctx := context.Background()
	repoDir := t.TempDir()
	runPublicationGit(t, repoDir, "init", "-b", "main")
	runPublicationGit(t, repoDir, "config", "user.email", "test@example.com")
	runPublicationGit(t, repoDir, "config", "user.name", "Test")
	require.NoError(t, os.MkdirAll(filepath.Join(repoDir, ".azedarach"), 0o755))
	configJSON := "{\n  \"gate\": {\"command\": \"npm run verify-publication\", \"environmentFingerprint\": \"node-consumer\"},\n  \"publicationEvidence\": {\n    \"policyVersion\": \"portable-v1\",\n    \"activePathProfiles\": [\"consumer-integration\"],\n    \"exactBaseSurfaces\": {\"wire\": [\"src/wire\"]},\n    \"dependencies\": {\"api\": [\"src/api.ts\"]}\n  }\n}"
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
	projectID := protocol.DefaultProjectID
	issueClient := newMigratedIssueClientAtPath(t, filepath.Join(repoDir, "issues.db"), slog.Default())
	t.Cleanup(func() { _ = issueClient.CloseDB() })
	issueID, err := issueClient.Create(ctx, issues.CreateTaskParams{Title: "portable merge", Type: domain.TypeFeature, Priority: domain.P1, Status: domain.StatusInProgress})
	require.NoError(t, err)
	started := time.Now().UTC()
	_, err = runtime.store.AcquireValidation(ctx, domain.ValidationAcquire{
		RequestID: "merge-review", LeaseToken: "secret", ProjectID: projectID, IssueID: issueID,
		Class: domain.ValidationClassAggregate, Scope: domain.ValidationScopeTicket, Purpose: domain.ValidationPurposeReviewEvidence,
		IsolationMode: "worktree", EnvironmentFingerprint: "node-consumer", Profile: "consumer-integration", Command: "npm test",
		SourceRevision: sourceOID, ReviewerID: "reviewer", ReviewerKind: domain.ReviewerOwnerKindOrchestrator, ReviewEpochEventID: 1,
		PublicationOperationID: "publication-close-exact", AcceptedReviewEventID: 2, AcceptedPublicationOperationID: "publication-close-exact", TTL: time.Minute,
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
		publicationEvidenceCache: map[string]domain.PublicationEvidenceSnapshot{}, issues: issueClient,
		issueClientsByProject: map[string]*issues.Client{projectID: issueClient, runtime.canonicalProject: issueClient},
		issueClientsByRoot:    map[string]*issues.Client{repoDir: issueClient, daemonStoreRootKey(repoDir): issueClient},
	}
	integration := taskCloseIntegrationResult{
		Requested: true, Integrated: true, ConfiguredBaseTarget: true, TargetID: "base", SourceBranch: "feature", TargetBranch: "main",
		BaseOID: baseOID, SourceOID: sourceOID, TargetOID: targetOID, ValidationAttempts: merge.ValidationAttempts,
	}
	_, err = runtime.store.AcquireValidation(ctx, domain.ValidationAcquire{
		RequestID: "candidate-authority", LeaseToken: "candidate-authority-secret", ProjectID: projectID,
		Class: domain.ValidationClassAggregate, Scope: domain.ValidationScopeRepository, Purpose: domain.ValidationPurposePushGate,
		IsolationMode: "synthetic-worktree", EnvironmentFingerprint: "node-consumer", Override: domain.ValidationOverrideNone,
		Profile: "publication:portable-v1", Command: "npm run verify-publication", SourceRevision: targetOID,
		TTL: time.Minute,
	}, started.Add(2*time.Second))
	require.NoError(t, err)
	_, err = runtime.store.FinishValidation(ctx, "candidate-authority", "candidate-authority-secret", domain.ValidationRequestCompleted, "passed", domain.ValidationEvidence{
		Held: true, RequestID: "candidate-authority", Class: domain.ValidationClassAggregate, Scope: domain.ValidationScopeRepository,
		Purpose: domain.ValidationPurposePushGate, Execution: domain.ValidationExecutionExecuted, Profile: "publication:portable-v1",
		SourceRevision: targetOID, Present: true,
	}, started.Add(3*time.Second), time.Minute)
	require.NoError(t, err)
	push, err := runtime.store.AcquireValidation(ctx, domain.ValidationAcquire{
		RequestID: "candidate-push", LeaseToken: "candidate-push-secret", ProjectID: projectID,
		Class: domain.ValidationClassAggregate, Scope: domain.ValidationScopeRepository, Purpose: domain.ValidationPurposePushGate,
		IsolationMode: "synthetic-worktree", EnvironmentFingerprint: "node-consumer", Override: domain.ValidationOverrideNone,
		Profile: "publication:portable-v1", Command: "npm run verify-publication", SourceRevision: targetOID, TTL: time.Minute,
	}, started.Add(4*time.Second))
	require.NoError(t, err)
	require.Equal(t, domain.ValidationExecutionReused, push.Execution)
	publication := domain.PublicationOperation{
		OperationID: "publication-close-exact", ProjectID: projectID, IssueID: issueID, IntentKey: "review-accept",
		RequestFingerprint: "fingerprint", ActorID: "reviewer", ReviewerKind: domain.ReviewerOwnerKindOrchestrator,
		ReviewEpochEventID: 1, AcceptedReviewEventID: 2, PatchEvidenceID: "publication-close-exact", TargetID: "base", TargetBranch: "main",
		SourceRevision: sourceOID, BaseRevision: baseOID, PolicyVersion: "portable-v1", EnvironmentFingerprint: "node-consumer",
		ValidationCommand: "npm run verify-publication", State: domain.PublicationOperationQueued, CreatedAt: started,
	}
	mergedA := publication
	mergedA.OperationID = "publication-merged-a"
	mergedA.PatchEvidenceID = mergedA.OperationID
	mergedA.IntentKey = "review-accept-a"
	storedMergedA, _, err := runtime.store.EnqueuePublication(ctx, mergedA, "publication-merged-a")
	require.NoError(t, err)
	claimedMergedA, acquired, err := runtime.store.ClaimPublicationOperation(ctx, storedMergedA.OperationID, operationstore.PublicationOperationClaim{
		Owner: "daemon", Token: "publication-claim-a", Now: started.Add(5 * time.Second), TTL: time.Minute,
	})
	require.NoError(t, err)
	require.True(t, acquired)
	passedMergedA, err := d.transitionPublicationOperation(ctx, claimedMergedA, "publication-claim-a", domain.PublicationOperationPassed, func(update *operationstore.PublicationOperationUpdate) {
		update.CandidateRevision = targetOID
		update.ValidationRequestID = push.RequestID
		update.ReusedEvidenceID = push.AuthoritativeRequestID
	})
	require.NoError(t, err)
	mergedAFinished := started.Add(6 * time.Second)
	_, err = d.transitionPublicationOperation(ctx, passedMergedA, "publication-claim-a", domain.PublicationOperationMerged, func(update *operationstore.PublicationOperationUpdate) {
		update.ReleaseClaim = true
		update.FinishedAt = &mergedAFinished
	})
	require.NoError(t, err)
	storedPublication, _, err := runtime.store.EnqueuePublication(ctx, publication, publicationCoalesceKey(publication))
	require.NoError(t, err)
	claimedPublication, acquired, err := runtime.store.ClaimPublicationOperation(ctx, storedPublication.OperationID, operationstore.PublicationOperationClaim{
		Owner: "daemon", Token: "publication-claim", Now: started.Add(7 * time.Second), TTL: time.Minute,
	})
	require.NoError(t, err)
	require.True(t, acquired)
	passedPublication, err := d.transitionPublicationOperation(ctx, claimedPublication, "publication-claim", domain.PublicationOperationPassed, func(update *operationstore.PublicationOperationUpdate) {
		update.CandidateRevision = targetOID
		update.ValidationRequestID = push.RequestID
		update.ReusedEvidenceID = push.AuthoritativeRequestID
	})
	require.NoError(t, err)
	_, _, err = d.taskClosePublicationProvenance(ctx, projectID, issueID, integration, "portable-v1", "npm run verify-publication", "node-consumer")
	require.Error(t, err, "an unbound close must not adopt another in-flight publication")
	for name, binding := range map[string]taskClosePublicationBinding{
		"foreign operation": {operationID: "publication-foreign", claimToken: "publication-claim"},
		"foreign claim":     {operationID: passedPublication.OperationID, claimToken: "foreign-claim"},
	} {
		t.Run(name, func(t *testing.T) {
			foreignCtx := context.WithValue(ctx, taskClosePublicationBindingContextKey{}, binding)
			_, _, bindingErr := d.taskClosePublicationProvenance(foreignCtx, projectID, issueID, integration, "portable-v1", "npm run verify-publication", "node-consumer")
			require.ErrorContains(t, bindingErr, "requires its completed non-emergency publication operation")
		})
	}
	boundCtx := withTaskClosePublicationBinding(ctx, passedPublication.OperationID, "publication-claim")
	boundOperation, boundValidation, err := d.taskClosePublicationProvenance(boundCtx, projectID, issueID, integration, "portable-v1", "npm run verify-publication", "node-consumer")
	require.NoError(t, err)
	assert.Equal(t, publication.OperationID, boundOperation.OperationID)
	assert.Equal(t, push.RequestID, boundValidation.RequestID)
	foreignRecovery := integration
	foreignRecovery.PublicationOperationID = mergedA.OperationID
	_, _, err = d.taskClosePublicationProvenance(boundCtx, projectID, issueID, foreignRecovery, "portable-v1", "npm run verify-publication", "node-consumer")
	require.ErrorContains(t, err, "does not match active publication binding")
	sameOperationRecovery := integration
	sameOperationRecovery.PublicationOperationID = passedPublication.OperationID
	boundOperation, boundValidation, err = d.taskClosePublicationProvenance(boundCtx, projectID, issueID, sameOperationRecovery, "portable-v1", "npm run verify-publication", "node-consumer")
	require.NoError(t, err)
	assert.Equal(t, publication.OperationID, boundOperation.OperationID)
	assert.Equal(t, push.RequestID, boundValidation.RequestID)
	finished := started.Add(8 * time.Second)
	_, err = d.transitionPublicationOperation(ctx, passedPublication, "publication-claim", domain.PublicationOperationMerged, func(update *operationstore.PublicationOperationUpdate) {
		update.ReleaseClaim = true
		update.FinishedAt = &finished
	})
	require.NoError(t, err)
	_, err = issueClient.AppendIssueObservationEvent(ctx, issueID, issues.IssueObservationEventParams{
		Type: domain.IssueEventReviewCompleted, Source: "daemon-orchestration", SourceCommand: string(protocol.OrchestrationIntentReviewAccept),
		Payload: map[string]any{"outcome": string(domain.ReviewOutcomeAccepted), "actor_id": "reviewer", "actor_kind": domain.ReviewerOwnerKindOrchestrator, "review_epoch_event_id": publication.ReviewEpochEventID, "intent_key": publication.IntentKey, "request_fingerprint": publication.RequestFingerprint, "reviewed_source_oid": sourceOID, "publication_operation_id": publication.OperationID},
	})
	require.NoError(t, err)
	resolvedOperation, resolvedValidation, err := d.taskClosePublicationProvenance(ctx, projectID, issueID, integration, "portable-v1", "npm run verify-publication", "node-consumer")
	require.NoError(t, err, "a missing receipt identity must recover from the authoritative accepted publication binding")
	assert.Equal(t, publication.OperationID, resolvedOperation.OperationID)
	assert.Equal(t, push.RequestID, resolvedValidation.RequestID)
	for name, mutate := range map[string]func(*taskCloseIntegrationResult) (string, string, string){
		"wrong candidate": func(candidate *taskCloseIntegrationResult) (string, string, string) {
			candidate.TargetOID = "wrong-target"
			return "portable-v1", "npm run verify-publication", "node-consumer"
		},
		"wrong policy": func(*taskCloseIntegrationResult) (string, string, string) {
			return "portable-v2", "npm run verify-publication", "node-consumer"
		},
		"wrong command": func(*taskCloseIntegrationResult) (string, string, string) {
			return "portable-v1", "npm run stale-publication", "node-consumer"
		},
		"wrong environment": func(*taskCloseIntegrationResult) (string, string, string) {
			return "portable-v1", "npm run verify-publication", "node-other"
		},
	} {
		t.Run(name, func(t *testing.T) {
			candidate := integration
			policy, command, environment := mutate(&candidate)
			_, _, provenanceErr := d.taskClosePublicationProvenance(ctx, projectID, issueID, candidate, policy, command, environment)
			require.Error(t, provenanceErr, "mismatched exact publication identity must fail closed")
		})
	}
	assert.Equal(t, domain.ValidationExecutionReused, resolvedValidation.Execution)
	_, _, fallbackErr := d.taskClosePublicationProvenance(ctx, "unrouted-project", issueID, integration, "portable-v1", "npm run verify-publication", "node-consumer")
	require.Error(t, fallbackErr, "unrouted project fallback must not authorize publication provenance")
	// Model the durable state after integration receipt succeeds but publication
	// identity/evidence persistence fails. Concurrent retries must append one
	// corrected binding and one merge-result evidence record without reapplying.
	err = d.persistTaskCloseIntegrationReceipt(ctx, projectID, issueID, repoDir, integration)
	require.NoError(t, err)
	recoveredIntegration := integration
	recoveredIntegration.ReceiptRecovered = true
	var retryWG sync.WaitGroup
	retryErrs := make(chan error, 2)
	for range 2 {
		retryWG.Add(1)
		go func() {
			defer retryWG.Done()
			retryErrs <- d.persistTaskCloseIntegrationPublication(ctx, projectID, issueID, repoDir, recoveredIntegration)
		}()
	}
	retryWG.Wait()
	close(retryErrs)
	for retryErr := range retryErrs {
		require.NoError(t, retryErr)
	}
	typedResult, err := d.mergeTypedConfiguredBaseThroughPublication(ctx, projectID, issueID, gitservice.Worktree{IssueID: issueID, Path: repoDir, Branch: "feature"}, repoDir, "main", sourceOID, targetOID)
	require.NoError(t, err, "merge=%+v target=%s", merge, targetOID)
	require.True(t, typedResult.ReceiptRecorded)
	require.Equal(t, baseOID, typedResult.BaseOID)
	require.Equal(t, targetOID, typedResult.TargetOID)
	require.NotEmpty(t, typedResult.Result.ValidationAttempts)
	receipts, err := issueClient.ListIssueObservationEvents(ctx, issueID, issues.IssueObservationEventListOptions{Types: []domain.IssueObservationEventType{domain.IssueEventTaskIntegrationCompleted}, Limit: 10})
	require.NoError(t, err)
	require.Len(t, receipts, 2, "concurrent retries must append exactly one corrected publication binding")
	assert.Empty(t, observationPayloadString(receipts[0].Payload, "publication_operation_id"))
	assert.Equal(t, publication.OperationID, observationPayloadString(receipts[1].Payload, "publication_operation_id"))
	snapshot, err := runtime.store.PublicationEvidenceSnapshot(ctx, projectID, issueID)
	require.NoError(t, err)
	require.Len(t, snapshot.Evidence, 1)
	assert.Equal(t, domain.PublicationEvidenceMergeResult, snapshot.Evidence[0].Layer)
	assert.Equal(t, baseOID, snapshot.Evidence[0].BaseRevision)
	assert.Equal(t, sourceOID, snapshot.Evidence[0].SourceRevision)
	assert.Equal(t, targetOID, snapshot.Evidence[0].ResultRevision)
	assert.Equal(t, []string{"src/api.ts"}, snapshot.Evidence[0].Coverage.Paths)
	_, assessments, err := d.evaluateCurrentPublicationEvidence(ctx, projectID, issueID, time.Now().UTC())
	require.NoError(t, err)
	require.Len(t, assessments, 1)
	assert.True(t, assessments[0].Retained, "exact synthetic merge must remain active after the issue worktree is gone: %+v", assessments[0])
	for _, name := range []string{"later-main-a.txt", "later-main-b.txt"} {
		require.NoError(t, os.WriteFile(filepath.Join(repoDir, name), []byte("unrelated\n"), 0o644))
		runPublicationGit(t, repoDir, "add", name)
		runPublicationGit(t, repoDir, "commit", "-m", "unrelated main advancement")
	}
	advancedTargetOID := runPublicationGit(t, repoDir, "rev-parse", "main")
	var advancedWG sync.WaitGroup
	advancedErrs := make(chan error, 2)
	for range 2 {
		advancedWG.Add(1)
		go func() {
			defer advancedWG.Done()
			recovered, found, recoverErr := d.recoverPublishedTaskCloseIntegration(ctx, projectID, issueID, repoDir, "base", "feature", "main", sourceOID, advancedTargetOID)
			if recoverErr != nil {
				advancedErrs <- recoverErr
				return
			}
			if !found || !recovered.ReceiptRecovered || recovered.TargetOID != targetOID || recovered.PublicationOperationID != publication.OperationID {
				advancedErrs <- fmt.Errorf("advanced recovery = (%+v, %t), want original exact publication", recovered, found)
				return
			}
			advancedErrs <- d.persistTaskCloseIntegrationPublication(ctx, projectID, issueID, repoDir, recovered)
		}()
	}
	advancedWG.Wait()
	close(advancedErrs)
	for advancedErr := range advancedErrs {
		require.NoError(t, advancedErr)
	}
	afterAdvance, err := runtime.store.PublicationEvidenceSnapshot(ctx, projectID, issueID)
	require.NoError(t, err)
	require.Len(t, afterAdvance.Evidence, 1, "cleanup retries after base advancement must not fabricate publication evidence")
	recoveredBeforeRewrite, found, err := d.recoverPublishedTaskCloseIntegration(ctx, projectID, issueID, repoDir, "base", "feature", "main", sourceOID, advancedTargetOID)
	require.NoError(t, err)
	require.True(t, found)
	runPublicationGit(t, repoDir, "checkout", "--orphan", "rewritten-main")
	runPublicationGit(t, repoDir, "read-tree", "--empty")
	require.NoError(t, os.WriteFile(filepath.Join(repoDir, "rewritten.txt"), []byte("divergent\n"), 0o644))
	runPublicationGit(t, repoDir, "add", "rewritten.txt")
	runPublicationGit(t, repoDir, "commit", "-m", "rewrite configured base")
	rewrittenOID := runPublicationGit(t, repoDir, "rev-parse", "HEAD")
	runPublicationGit(t, repoDir, "branch", "-f", "main", rewrittenOID)
	_, found, err = d.recoverPublishedTaskCloseIntegration(ctx, projectID, issueID, repoDir, "base", "feature", "main", sourceOID, rewrittenOID)
	require.ErrorContains(t, err, "exact integrated receipt is not valid")
	require.False(t, found, "divergent configured-base history must not reuse the stale receipt")
	require.ErrorContains(t, d.persistTaskCloseIntegrationPublication(ctx, projectID, issueID, repoDir, recoveredBeforeRewrite), "target ancestry", "pre-cleanup retry must recheck target history after recovery")
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
