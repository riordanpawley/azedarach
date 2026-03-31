package protocol

// SnapshotSchemaVersion identifies the snapshot payload schema contract.
const SnapshotSchemaVersion uint16 = 1

// SnapshotProtocolVersion pins the daemon/client protocol revision used by snapshot payloads.
const SnapshotProtocolVersion Version = CurrentVersion

// SnapshotPayload is the deterministic snapshot contract exchanged across the IPC boundary.
//
// The field order is part of the contract: schema_version -> protocol_version -> snapshot_revision.
type SnapshotPayload struct {
	SchemaVersion    uint16  `json:"schema_version" msgpack:"schema_version"`
	ProtocolVersion  Version `json:"protocol_version" msgpack:"protocol_version"`
	SnapshotRevision uint64  `json:"snapshot_revision" msgpack:"snapshot_revision"`
}
