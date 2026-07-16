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
)

func TestValidationCommandRoutesDurableAggregateQueue(t *testing.T) {
	runtime := newOperationRuntime(operationRuntimeConfig{repoDir: t.TempDir()})
	t.Cleanup(func() { _ = runtime.Close() })
	d := &Daemon{operationRuntime: runtime, revision: map[string]uint64{"project": 1}}

	acquire := func(requestID, issueID string) domain.ValidationRequest {
		t.Helper()
		body, err := json.Marshal(protocol.ValidationAcquireRequest{RequestID: requestID, LeaseToken: "test-secret", IssueID: issueID, Class: domain.ValidationClassAggregate, Profile: "cold", Command: "just test", SourceRevision: "abc123", TTLSeconds: 30})
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

	if got := acquire("owner", "dkg"); got.State != domain.ValidationRequestActive {
		t.Fatalf("owner state = %s, want active", got.State)
	}
	if got := acquire("waiter", "dkd"); got.State != domain.ValidationRequestQueued {
		t.Fatalf("waiter state = %s, want queued", got.State)
	}
	resp, err := d.handleValidationCommand(context.Background(), protocol.RequestEnvelope{ProtocolVersion: protocol.CurrentVersion, RequestID: "rpc-status", Kind: protocol.EnvelopeKindCommand, Command: protocol.CommandValidationStatus, Meta: protocol.Metadata{ProjectID: "project"}, Body: json.RawMessage(`{}`)})
	if err != nil || !resp.OK {
		t.Fatalf("status: response=%+v err=%v", resp, err)
	}
	var status protocol.ValidationStatusResponse
	if err := json.Unmarshal(resp.Body, &status); err != nil {
		t.Fatal(err)
	}
	if len(status.Snapshot.Active) != 1 || len(status.Snapshot.Queued) != 1 {
		t.Fatalf("snapshot = %+v, want one owner and one waiter", status.Snapshot)
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
		body, err := json.Marshal(protocol.ValidationAcquireRequest{RequestID: requestID, LeaseToken: "secret", IssueID: issueID, Class: domain.ValidationClassAggregate, Profile: "cold", Command: "just test", SourceRevision: "candidate-a", ReviewerID: reviewer, TTLSeconds: 30})
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

func TestValidationCommandRejectsSpoofedNestedAuthorization(t *testing.T) {
	runtime := newOperationRuntime(operationRuntimeConfig{repoDir: t.TempDir()})
	t.Cleanup(func() { _ = runtime.Close() })
	d := &Daemon{operationRuntime: runtime, revision: map[string]uint64{"project": 1}}
	ctx := context.Background()
	_, err := runtime.store.AcquireValidation(ctx, domain.ValidationAcquire{RequestID: "owner", LeaseToken: "secret", ProjectID: "project", IssueID: "dkg", Class: domain.ValidationClassAggregate, Profile: "cold", Command: "just test", SourceRevision: "abc123", TTL: time.Minute}, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(protocol.ValidationAuthorizeNestedRequest{RequestID: "owner", LeaseToken: "spoof", Class: domain.ValidationClassShared})
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
