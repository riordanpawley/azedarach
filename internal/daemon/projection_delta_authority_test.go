package daemon

import (
	"context"
	"errors"
	"testing"

	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	"github.com/riordanpawley/azedarach/internal/domain"
)

func TestProjectionDeltaErrorEnvelopePreservesTypedRetrySemantics(t *testing.T) {
	gap := ProjectionDeltaErrorEnvelope(&domain.ProjectionGapError{ProjectID: "p", Expected: 1, Actual: 3})
	if gap.Code != protocol.ErrorCodeRevisionGap || !gap.Retryable {
		t.Fatalf("gap envelope=%+v", gap)
	}
	canceled := ProjectionDeltaErrorEnvelope(&domain.ProjectionCanceledError{Cause: context.Canceled})
	if canceled.Code != protocol.ErrorCodeTimeout || !canceled.Retryable {
		t.Fatalf("canceled envelope=%+v", canceled)
	}
	internal := ProjectionDeltaErrorEnvelope(errors.New("broken"))
	if internal.Code != protocol.ErrorCodeInternal || internal.Retryable {
		t.Fatalf("internal envelope=%+v", internal)
	}
	retryable := ProjectionDeltaErrorEnvelope(&domain.ProjectionRetryableError{Cause: errors.New("busy")})
	if retryable.Code != protocol.ErrorCodeUnavailable || !retryable.Retryable {
		t.Fatalf("retryable envelope=%+v", retryable)
	}
}
