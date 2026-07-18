package protocol

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/riordanpawley/azedarach/internal/naming"
)

func TestVerifyProjectionDeltaBatchDetectsContinuityHashAndProjectorFailures(t *testing.T) {
	projector := ProjectionProjector{ID: "issue-complete-value", SchemaVersion: 1, Build: "dgp-v1", Checksum: "projector-checksum"}
	source := ProjectionSourceRange{Authority: "legacy_issue_observation", SourceFrom: "1", SourceTo: "1", Transitional: true}
	delta := ProjectionDelta{ProjectID: naming.ProjectID("p"), Cursor: 1, Kind: "issue", Key: "a", QualifiedKey: "p/issue/a", Operation: ProjectionDeltaUpsert, Payload: json.RawMessage(`{"id":"a"}`), Source: source}
	batch := ProjectionDeltaBatch{DeliveryContract: ProjectionDeliveryContract, DeliveryCursorTransitional: true, Projector: projector, AfterCursor: 0, HeadCursor: 1, DeliveryToCursor: 1, Deltas: []ProjectionDelta{delta}, SourceVector: []ProjectionSourceRange{source}}
	FinalizeProjectionDeltaBatch(&batch)
	if err := VerifyProjectionDeltaBatch(batch, 0, projector); err != nil {
		t.Fatal(err)
	}
	assertKind := func(name string, changed ProjectionDeltaBatch, expectedAfter uint64, expectedKind ProjectionVerificationErrorKind) {
		t.Helper()
		t.Run(name, func(t *testing.T) {
			err := VerifyProjectionDeltaBatch(changed, expectedAfter, projector)
			var verification *ProjectionVerificationError
			if !errors.As(err, &verification) || verification.Kind != expectedKind {
				t.Fatalf("error=%v kind=%v", err, verification)
			}
		})
	}
	assertKind("overlap", batch, 1, ProjectionVerificationOverlap)
	gap := batch
	gap.AfterCursor = 2
	assertKind("explicit_gap", gap, 0, ProjectionVerificationGap)
	incompatible := batch
	incompatible.Projector.Build = "other"
	assertKind("incompatible", incompatible, 0, ProjectionVerificationIncompatible)
	corrupt := batch
	corrupt.Deltas = append([]ProjectionDelta(nil), batch.Deltas...)
	corrupt.Deltas[0].Payload = json.RawMessage(`{"id":"changed"}`)
	assertKind("hash", corrupt, 0, ProjectionVerificationHashMismatch)
	corruptSource := batch
	corruptSource.SourceVector = append([]ProjectionSourceRange(nil), batch.SourceVector...)
	corruptSource.SourceVector[0].TerminalHash = "forged"
	assertKind("source_hash", corruptSource, 0, ProjectionVerificationHashMismatch)
	pairedForgery := batch
	pairedForgery.Deltas = append([]ProjectionDelta(nil), batch.Deltas...)
	pairedForgery.SourceVector = append([]ProjectionSourceRange(nil), batch.SourceVector...)
	pairedForgery.Deltas[0].Source.SourceTo = "forged"
	pairedForgery.SourceVector[0].SourceTo = "forged"
	assertKind("paired_delta_and_vector_forgery", pairedForgery, 0, ProjectionVerificationHashMismatch)
}

func TestVerifyProjectionSnapshotBindsCanonicalSourceVector(t *testing.T) {
	projector := ProjectionProjector{ID: "issue-complete-value", SchemaVersion: 1, Build: "dgp-v1", Checksum: "projector-checksum"}
	snapshot := ProjectionSnapshot{
		ProjectID: "p", Cursor: 1, HeadCursor: 1,
		Values:                     []ProjectionValue{{Kind: "issue", Key: "a", Payload: json.RawMessage(`{"id":"a"}`)}},
		DeliveryContract:           ProjectionDeliveryContract,
		DeliveryCursorTransitional: true,
		Projector:                  projector,
		SourceVector:               []ProjectionSourceRange{{Authority: "legacy_issue_observation", SourceFrom: "1", SourceTo: "1", Transitional: true}},
	}
	FinalizeProjectionSnapshot(&snapshot)
	if err := VerifyProjectionSnapshot(snapshot, projector); err != nil {
		t.Fatalf("valid snapshot: %v", err)
	}
	forged := snapshot
	forged.SourceVector = append([]ProjectionSourceRange(nil), snapshot.SourceVector...)
	forged.SourceVector[0].SourceTo = "forged"
	if err := VerifyProjectionSnapshot(forged, projector); err == nil {
		t.Fatal("paired snapshot source-vector forgery passed verification")
	}
}

func TestProjectionDeltaProtocolVersionClaimAndCompatibility(t *testing.T) {
	if ProjectionDeltaProtocolVersion != 48 {
		t.Fatalf("projection protocol first version=%d, want 48", ProjectionDeltaProtocolVersion)
	}
	if CurrentVersion != 56 {
		t.Fatalf("current protocol version=%d, want 56", CurrentVersion)
	}
	if SupportsProjectionDeltaCommands(47) || !SupportsProjectionDeltaCommands(48) || !SupportsProjectionDeltaCommands(49) || !SupportsProjectionDeltaCommands(50) || !SupportsProjectionDeltaCommands(51) || !SupportsProjectionDeltaCommands(52) || !SupportsProjectionDeltaCommands(53) || !SupportsProjectionDeltaCommands(54) || !SupportsProjectionDeltaCommands(55) || !SupportsProjectionDeltaCommands(56) || SupportsProjectionDeltaCommands(57) {
		t.Fatal("projection command support window does not span v48-v56")
	}
	if ack := NegotiateHello(Hello{ProtocolVersion: 48}, "daemon-v56"); ack.Accepted || ack.DaemonProtocolVersion != 56 || ack.ErrorCode != ErrorCodeUpgradeRequired {
		t.Fatalf("v48 stale-client handshake=%+v", ack)
	}
	if ack := NegotiateHello(Hello{ProtocolVersion: 55}, "daemon-v56"); ack.Accepted || ack.DaemonProtocolVersion != 56 || ack.ErrorCode != ErrorCodeUpgradeRequired {
		t.Fatalf("v54 stale-generation handshake=%+v", ack)
	}
	if ack := NegotiateHello(Hello{ProtocolVersion: 56}, "daemon-v56"); !ack.Accepted || ack.DaemonProtocolVersion != 56 {
		t.Fatalf("v56 compatibility handshake=%+v", ack)
	}
	if ack := NegotiateHello(Hello{ProtocolVersion: 57}, "daemon-v56"); ack.Accepted || ack.ErrorCode != ErrorCodeIncompatible {
		t.Fatalf("v56 compatibility handshake=%+v", ack)
	}
}
