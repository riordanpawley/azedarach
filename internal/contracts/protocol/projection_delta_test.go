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
	delta.SemanticChecksum = projectionDeltaChecksum(delta)
	batch := ProjectionDeltaBatch{DeliveryContract: ProjectionDeliveryContract, DeliveryCursorTransitional: true, Projector: projector, AfterCursor: 0, HeadCursor: 1, DeliveryToCursor: 1, Deltas: []ProjectionDelta{delta}, SourceVector: []ProjectionSourceRange{source}}
	batch.SemanticChecksum = projectionChecksumJSON([]string{delta.SemanticChecksum})
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
}
