package protocol

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
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
	cases := []struct {
		name    string
		payload SnapshotPayload
		fixture string
	}{
		{
			name: "current-revision",
			payload: SnapshotPayload{
				SchemaVersion:    SnapshotSchemaVersion,
				ProtocolVersion:  SnapshotProtocolVersion,
				SnapshotRevision: 42,
			},
			fixture: "snapshot_payload_current_revision.json.golden",
		},
		{
			name: "default-revision",
			payload: SnapshotPayload{
				SchemaVersion:   SnapshotSchemaVersion,
				ProtocolVersion: SnapshotProtocolVersion,
			},
			fixture: "snapshot_payload_default_revision.json.golden",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := mustMarshalJSON(t, tc.payload)
			want := readGoldenText(t, tc.fixture)
			if got != want {
				t.Fatalf("json = %s, want %s", got, want)
			}
		})
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

func mustMarshalJSON(t *testing.T, payload SnapshotPayload) string {
	t.Helper()

	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal snapshot payload: %v", err)
	}
	return string(data)
}

func readGoldenText(t *testing.T, filename string) string {
	t.Helper()

	path := filepath.Join("testdata", filename)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden fixture %q: %v", path, err)
	}
	return string(bytes.TrimSpace(data))
}
