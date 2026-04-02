package protocol

import "github.com/riordanpawley/azedarach/internal/domain"

// TaskListSnapshotSchemaVersion identifies the joined task-list snapshot payload contract.
const TaskListSnapshotSchemaVersion uint16 = 1

// TaskListSnapshotPayload is the deterministic daemon/client contract for joined issue/session/worktree task snapshots.
//
// The field order is part of the contract: schema_version -> protocol_version -> snapshot_revision -> project_id -> tasks.
// Tasks already carry daemon-authored joined runtime state via domain.Task fields.
type TaskListSnapshotPayload struct {
	SchemaVersion    uint16        `json:"schema_version" msgpack:"schema_version"`
	ProtocolVersion  Version       `json:"protocol_version" msgpack:"protocol_version"`
	SnapshotRevision uint64        `json:"snapshot_revision" msgpack:"snapshot_revision"`
	ProjectID        string        `json:"project_id" msgpack:"project_id"`
	Tasks            []domain.Task `json:"tasks" msgpack:"tasks"`
}
