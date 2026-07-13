package daemon

import (
	"context"
	"errors"

	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/naming"
)

type projectionDeltaStore interface {
	ListProjectionDeltas(context.Context, string, uint64, int) ([]domain.ProjectionDelta, uint64, error)
	WatchProjectionDeltas(context.Context, string, uint64, int) ([]domain.ProjectionDelta, uint64, error)
	ProjectionSnapshotAt(context.Context, string, uint64) (domain.ProjectionSnapshot, error)
}

// ProjectionDeltaAuthority is the daemon/domain boundary for restart-safe
// project delta replay. The store remains the cursor authority; this adapter
// performs no reconciliation or runtime observation on read paths.
type ProjectionDeltaAuthority struct{ store projectionDeltaStore }

func NewProjectionDeltaAuthority(store projectionDeltaStore) *ProjectionDeltaAuthority {
	return &ProjectionDeltaAuthority{store: store}
}

func (a *ProjectionDeltaAuthority) List(ctx context.Context, projectID string, after uint64, limit int) (protocol.ProjectionDeltaBatch, error) {
	deltas, head, err := a.store.ListProjectionDeltas(ctx, projectID, after, limit)
	if err != nil {
		return protocol.ProjectionDeltaBatch{}, err
	}
	return projectionDeltaBatch(projectID, after, head, deltas), nil
}

func (a *ProjectionDeltaAuthority) Watch(ctx context.Context, projectID string, after uint64, limit int) (protocol.ProjectionDeltaBatch, error) {
	deltas, head, err := a.store.WatchProjectionDeltas(ctx, projectID, after, limit)
	if err != nil {
		return protocol.ProjectionDeltaBatch{}, err
	}
	return projectionDeltaBatch(projectID, after, head, deltas), nil
}

func (a *ProjectionDeltaAuthority) Snapshot(ctx context.Context, projectID string, cursor uint64) (protocol.ProjectionSnapshot, error) {
	snapshot, err := a.store.ProjectionSnapshotAt(ctx, projectID, cursor)
	if err != nil {
		return protocol.ProjectionSnapshot{}, err
	}
	values := make([]protocol.ProjectionValue, 0, len(snapshot.Values))
	for _, value := range snapshot.Values {
		values = append(values, protocol.ProjectionValue{Kind: protocol.ProjectionKind(value.Kind), Key: value.Key, Payload: value.Payload})
	}
	return protocol.ProjectionSnapshot{SchemaVersion: protocol.ProjectionDeltaSchemaVersion, ProjectID: naming.ProjectID(snapshot.ProjectID), Cursor: snapshot.Cursor, HeadCursor: snapshot.Head, Values: values}, nil
}

func ProjectionDeltaErrorEnvelope(err error) *protocol.ErrorEnvelope {
	if err == nil {
		return nil
	}
	var gap *domain.ProjectionGapError
	switch {
	case errors.As(err, &gap):
		return &protocol.ErrorEnvelope{Code: protocol.ErrorCodeRevisionGap, Message: err.Error(), Retryable: true}
	case errors.Is(err, domain.ErrProjectionCanceled), errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return &protocol.ErrorEnvelope{Code: protocol.ErrorCodeTimeout, Message: err.Error(), Retryable: true}
	case errors.Is(err, domain.ErrProjectionRetryable):
		return &protocol.ErrorEnvelope{Code: protocol.ErrorCodeUnavailable, Message: err.Error(), Retryable: true}
	default:
		return &protocol.ErrorEnvelope{Code: protocol.ErrorCodeInternal, Message: err.Error(), Retryable: false}
	}
}

func projectionDeltaBatch(projectID string, after, head uint64, deltas []domain.ProjectionDelta) protocol.ProjectionDeltaBatch {
	out := protocol.ProjectionDeltaBatch{SchemaVersion: protocol.ProjectionDeltaSchemaVersion, ProjectID: naming.ProjectID(protocol.NormalizeProjectID(projectID)), AfterCursor: after, HeadCursor: head, Deltas: make([]protocol.ProjectionDelta, 0, len(deltas))}
	for _, delta := range deltas {
		out.ProjectID = naming.ProjectID(delta.ProjectID)
		out.Deltas = append(out.Deltas, protocol.ProjectionDelta{ProjectID: naming.ProjectID(delta.ProjectID), Cursor: delta.Cursor, Kind: protocol.ProjectionKind(delta.Kind), Key: delta.Key, Operation: protocol.ProjectionDeltaOperation(delta.Operation), IdempotencyKey: delta.IdempotencyKey, Payload: delta.Payload, CommittedAt: delta.CommittedAt})
	}
	return out
}
