package daemon

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
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
// project delta replay. The store owns only a transitional delivery offset;
// this adapter neither creates semantic history nor observes runtime on reads.
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
		values = append(values, protocol.ProjectionValue{Kind: protocol.ProjectionKind(value.Kind), Key: value.Key, QualifiedKey: projectionQualifiedKey(snapshot.ProjectID, value.Kind, value.Key), Payload: value.Payload})
	}
	sources := make([]protocol.ProjectionSourceRange, 0, len(snapshot.Sources))
	for _, source := range snapshot.Sources {
		sources = append(sources, protocolProjectionSource(source))
	}
	return protocol.ProjectionSnapshot{
		SchemaVersion: protocol.ProjectionDeltaSchemaVersion, ProjectID: naming.ProjectID(snapshot.ProjectID), Cursor: snapshot.Cursor, HeadCursor: snapshot.Head, Values: values,
		DeliveryContract: domain.ProjectionDeliveryContract, DeliveryCursorTransitional: true, Projector: issueProjectionProjector(), SourceVector: sources,
		SemanticChecksum: projectionValuesChecksum(values), Health: "healthy", AvailableDeliveryFrom: 0, AvailableDeliveryTo: snapshot.Head, LastGoodDeliveryCursor: snapshot.Cursor,
	}, nil
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
	out := protocol.ProjectionDeltaBatch{
		SchemaVersion: protocol.ProjectionDeltaSchemaVersion, ProjectID: naming.ProjectID(protocol.NormalizeProjectID(projectID)), AfterCursor: after, HeadCursor: head,
		DeliveryToCursor: after, Deltas: make([]protocol.ProjectionDelta, 0, len(deltas)), EmptyAdvances: []protocol.ProjectionEmptyAdvance{},
		DeliveryContract: domain.ProjectionDeliveryContract, DeliveryCursorTransitional: true, Projector: issueProjectionProjector(),
		Health: "healthy", AvailableDeliveryFrom: 0, AvailableDeliveryTo: head, LastGoodDeliveryCursor: after,
	}
	for _, delta := range deltas {
		out.ProjectID = naming.ProjectID(delta.ProjectID)
		out.DeliveryToCursor = delta.Cursor
		out.LastGoodDeliveryCursor = delta.Cursor
		if delta.Kind == domain.ProjectionKindSourceAdvance {
			out.EmptyAdvances = append(out.EmptyAdvances, protocol.ProjectionEmptyAdvance{DeliveryCursor: delta.Cursor, Source: protocolProjectionSource(delta.Source)})
			continue
		}
		source := protocolProjectionSource(delta.Source)
		out.Deltas = append(out.Deltas, protocol.ProjectionDelta{ProjectID: naming.ProjectID(delta.ProjectID), Cursor: delta.Cursor, Kind: protocol.ProjectionKind(delta.Kind), Key: delta.Key, QualifiedKey: projectionQualifiedKey(delta.ProjectID, delta.Kind, delta.Key), Operation: protocol.ProjectionDeltaOperation(delta.Operation), IdempotencyKey: delta.IdempotencyKey, Payload: delta.Payload, CommittedAt: delta.CommittedAt, Source: source, SemanticChecksum: projectionDeltaSemanticChecksum(delta)})
	}
	for _, source := range domain.MergeProjectionSourceRanges(deltas) {
		out.SourceVector = append(out.SourceVector, protocolProjectionSource(source))
	}
	out.SemanticChecksum = projectionProtocolDeltasChecksum(out.Deltas)
	return out
}

func issueProjectionProjector() protocol.ProjectionProjector {
	return protocol.ProjectionProjector{ID: domain.IssueProjectorID, SchemaVersion: domain.IssueProjectionDeltaSchemaVersion, Build: domain.IssueProjectorBuild, Checksum: domain.IssueProjectorChecksum}
}

func projectionQualifiedKey(projectID string, kind domain.ProjectionKind, key string) string {
	return protocol.NormalizeProjectID(projectID) + "/" + string(kind) + "/" + key
}

func protocolProjectionSource(source domain.ProjectionSourceRange) protocol.ProjectionSourceRange {
	return protocol.ProjectionSourceRange{Authority: source.Authority, SourceFrom: source.SourceFrom, SourceTo: source.SourceTo, TerminalHash: source.TerminalHash, Transitional: source.Transitional}
}

func projectionDeltaSemanticChecksum(delta domain.ProjectionDelta) string {
	return checksumJSON(struct {
		ProjectID string                          `json:"project_id"`
		Kind      domain.ProjectionKind           `json:"kind"`
		Key       string                          `json:"key"`
		Operation domain.ProjectionDeltaOperation `json:"operation"`
		Payload   json.RawMessage                 `json:"payload"`
	}{delta.ProjectID, delta.Kind, delta.Key, delta.Operation, delta.Payload})
}

func projectionProtocolDeltasChecksum(deltas []protocol.ProjectionDelta) string {
	checksums := make([]string, 0, len(deltas))
	for _, delta := range deltas {
		checksums = append(checksums, delta.SemanticChecksum)
	}
	return checksumJSON(checksums)
}

func projectionValuesChecksum(values []protocol.ProjectionValue) string { return checksumJSON(values) }

func checksumJSON(value any) string {
	raw, _ := json.Marshal(value)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
