package protocol

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/riordanpawley/azedarach/internal/domain"
)

// TaskListSnapshotSchemaVersion identifies the joined task-list snapshot payload contract.
const TaskListSnapshotSchemaVersion uint16 = 2

// TaskListFreshness describes whether the daemon's joined runtime projection is current enough for UI display.
type TaskListFreshness string

const (
	TaskListFreshnessFresh TaskListFreshness = "fresh"
	TaskListFreshnessStale TaskListFreshness = "stale"
)

func (f TaskListFreshness) Valid() bool {
	switch f {
	case TaskListFreshnessFresh, TaskListFreshnessStale:
		return true
	default:
		return false
	}
}

// TaskListSnapshotPayload is the deterministic daemon/client contract for joined issue/session/worktree task snapshots.
//
// The field order is part of the contract:
// schema_version -> protocol_version -> snapshot_revision -> project_id -> last_checked_at -> freshness -> tasks.
// Tasks already carry daemon-authored joined runtime state via domain.Task fields.
type TaskListSnapshotPayload struct {
	SchemaVersion    uint16            `json:"schema_version" msgpack:"schema_version"`
	ProtocolVersion  Version           `json:"protocol_version" msgpack:"protocol_version"`
	SnapshotRevision uint64            `json:"snapshot_revision" msgpack:"snapshot_revision"`
	ProjectID        string            `json:"project_id" msgpack:"project_id"`
	LastCheckedAt    time.Time         `json:"last_checked_at" msgpack:"last_checked_at"`
	Freshness        TaskListFreshness `json:"freshness" msgpack:"freshness"`
	Tasks            []domain.Task     `json:"tasks" msgpack:"tasks"`
}

// TaskListSnapshotVersionMismatchError indicates schema/protocol contract mismatch.
type TaskListSnapshotVersionMismatchError struct {
	Field    string
	Expected int
	Actual   int
}

func (e *TaskListSnapshotVersionMismatchError) Error() string {
	if e == nil {
		return ""
	}
	return fmt.Sprintf("task list snapshot %s mismatch: expected %d, actual %d", e.Field, e.Expected, e.Actual)
}

// IsTaskListSnapshotVersionMismatch reports whether err is a schema/protocol version mismatch.
func IsTaskListSnapshotVersionMismatch(err error) bool {
	var mismatch *TaskListSnapshotVersionMismatchError
	return errors.As(err, &mismatch)
}

// DecodeTaskListSnapshotPayload strictly decodes the daemon-authored task list snapshot contract.
func DecodeTaskListSnapshotPayload(data []byte) (TaskListSnapshotPayload, error) {
	var payload TaskListSnapshotPayload
	if err := json.Unmarshal(data, &payload); err != nil {
		return TaskListSnapshotPayload{}, err
	}
	if payload.SchemaVersion != TaskListSnapshotSchemaVersion {
		return TaskListSnapshotPayload{}, &TaskListSnapshotVersionMismatchError{
			Field:    "schema_version",
			Expected: int(TaskListSnapshotSchemaVersion),
			Actual:   int(payload.SchemaVersion),
		}
	}
	if payload.ProtocolVersion != CurrentVersion {
		return TaskListSnapshotPayload{}, &TaskListSnapshotVersionMismatchError{
			Field:    "protocol_version",
			Expected: int(CurrentVersion),
			Actual:   int(payload.ProtocolVersion),
		}
	}
	if payload.LastCheckedAt.IsZero() {
		return TaskListSnapshotPayload{}, fmt.Errorf("task list snapshot missing last_checked_at")
	}
	if !payload.Freshness.Valid() {
		return TaskListSnapshotPayload{}, fmt.Errorf("task list snapshot freshness mismatch: expected one of [%s %s], actual %q", TaskListFreshnessFresh, TaskListFreshnessStale, payload.Freshness)
	}
	return payload, nil
}
