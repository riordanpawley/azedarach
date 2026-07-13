package protocol

import (
	"encoding/json"
	"time"

	"github.com/riordanpawley/azedarach/internal/naming"
)

const ProjectionDeltaSchemaVersion = 1

type ProjectionDeltaReadRequest struct {
	ProjectID   naming.ProjectID `json:"project_id" msgpack:"project_id"`
	AfterCursor uint64           `json:"after_cursor" msgpack:"after_cursor"`
	Limit       int              `json:"limit,omitempty" msgpack:"limit,omitempty"`
}

type ProjectionDeltaOperation string
type ProjectionKind string

const (
	ProjectionDeltaUpsert ProjectionDeltaOperation = "upsert"
	ProjectionDeltaDelete ProjectionDeltaOperation = "delete"
)

func (o ProjectionDeltaOperation) Valid() bool {
	return o == ProjectionDeltaUpsert || o == ProjectionDeltaDelete
}

type ProjectionSnapshotRequest struct {
	ProjectID naming.ProjectID `json:"project_id" msgpack:"project_id"`
	Cursor    uint64           `json:"cursor" msgpack:"cursor"`
}

type ProjectionDelta struct {
	ProjectID      naming.ProjectID         `json:"project_id" msgpack:"project_id"`
	Cursor         uint64                   `json:"cursor" msgpack:"cursor"`
	Kind           ProjectionKind           `json:"kind" msgpack:"kind"`
	Key            string                   `json:"key" msgpack:"key"`
	Operation      ProjectionDeltaOperation `json:"operation" msgpack:"operation"`
	IdempotencyKey string                   `json:"idempotency_key" msgpack:"idempotency_key"`
	Payload        json.RawMessage          `json:"payload,omitempty" msgpack:"payload,omitempty"`
	CommittedAt    time.Time                `json:"committed_at" msgpack:"committed_at"`
}

type ProjectionValue struct {
	Kind    ProjectionKind  `json:"kind" msgpack:"kind"`
	Key     string          `json:"key" msgpack:"key"`
	Payload json.RawMessage `json:"payload" msgpack:"payload"`
}

type ProjectionSnapshot struct {
	SchemaVersion int               `json:"schema_version" msgpack:"schema_version"`
	ProjectID     naming.ProjectID  `json:"project_id" msgpack:"project_id"`
	Cursor        uint64            `json:"cursor" msgpack:"cursor"`
	HeadCursor    uint64            `json:"head_cursor" msgpack:"head_cursor"`
	Values        []ProjectionValue `json:"values" msgpack:"values"`
}

type ProjectionDeltaBatch struct {
	SchemaVersion int               `json:"schema_version" msgpack:"schema_version"`
	ProjectID     naming.ProjectID  `json:"project_id" msgpack:"project_id"`
	AfterCursor   uint64            `json:"after_cursor" msgpack:"after_cursor"`
	HeadCursor    uint64            `json:"head_cursor" msgpack:"head_cursor"`
	Deltas        []ProjectionDelta `json:"deltas" msgpack:"deltas"`
}
