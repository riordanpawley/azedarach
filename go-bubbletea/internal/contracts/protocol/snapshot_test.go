package protocol

import (
	"encoding/json"
	"testing"

	"github.com/vmihailenco/msgpack/v5"
)

func TestSnapshotPayloadContractConstants(t *testing.T) {
	if got, want := SnapshotSchemaVersion, uint16(1); got != want {
		t.Fatalf("SnapshotSchemaVersion = %d, want %d", got, want)
	}
	if got, want := SnapshotProtocolVersion, CurrentVersion; got != want {
		t.Fatalf("SnapshotProtocolVersion = %d, want %d", got, want)
	}
}

func TestSnapshotPayloadJSONShapeIsDeterministic(t *testing.T) {
	payload := SnapshotPayload{
		SchemaVersion:    SnapshotSchemaVersion,
		ProtocolVersion:  SnapshotProtocolVersion,
		SnapshotRevision: 42,
	}

	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal snapshot payload: %v", err)
	}

	const want = `{"schema_version":1,"protocol_version":1,"snapshot_revision":42}`
	if got := string(data); got != want {
		t.Fatalf("json = %s, want %s", got, want)
	}
}

func TestSnapshotPayloadMessagePackRoundTrip(t *testing.T) {
	payload := SnapshotPayload{
		SchemaVersion:    SnapshotSchemaVersion,
		ProtocolVersion:  SnapshotProtocolVersion,
		SnapshotRevision: 42,
	}

	data, err := msgpack.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal snapshot payload: %v", err)
	}

	var got SnapshotPayload
	if err := msgpack.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal snapshot payload: %v", err)
	}
	if got != payload {
		t.Fatalf("roundtrip = %+v, want %+v", got, payload)
	}
}
