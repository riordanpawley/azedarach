package protocol

import (
	"encoding/json"
	"testing"

	"github.com/vmihailenco/msgpack/v5"
)

func TestCleanupOrphanedRequestBodySerialization(t *testing.T) {
	body := CleanupOrphanedRequestBody{ProjectID: "proj-123"}

	t.Run("json", func(t *testing.T) {
		data, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal request body: %v", err)
		}

		var got CleanupOrphanedRequestBody
		if err := json.Unmarshal(data, &got); err != nil {
			t.Fatalf("unmarshal request body: %v", err)
		}
		if got != body {
			t.Fatalf("roundtrip = %+v, want %+v", got, body)
		}
	})

	t.Run("msgpack", func(t *testing.T) {
		data, err := msgpack.Marshal(body)
		if err != nil {
			t.Fatalf("marshal request body: %v", err)
		}

		var got CleanupOrphanedRequestBody
		if err := msgpack.Unmarshal(data, &got); err != nil {
			t.Fatalf("unmarshal request body: %v", err)
		}
		if got != body {
			t.Fatalf("roundtrip = %+v, want %+v", got, body)
		}
	})
}

func TestCleanupOrphanedResponseBodySerialization(t *testing.T) {
	body := CleanupOrphanedResponseBody{
		ProjectID:        "proj-123",
		WorktreesRemoved: 3,
	}

	t.Run("json", func(t *testing.T) {
		data, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal response body: %v", err)
		}

		var got CleanupOrphanedResponseBody
		if err := json.Unmarshal(data, &got); err != nil {
			t.Fatalf("unmarshal response body: %v", err)
		}
		if got != body {
			t.Fatalf("roundtrip = %+v, want %+v", got, body)
		}
	})

	t.Run("msgpack", func(t *testing.T) {
		data, err := msgpack.Marshal(body)
		if err != nil {
			t.Fatalf("marshal response body: %v", err)
		}

		var got CleanupOrphanedResponseBody
		if err := msgpack.Unmarshal(data, &got); err != nil {
			t.Fatalf("unmarshal response body: %v", err)
		}
		if got != body {
			t.Fatalf("roundtrip = %+v, want %+v", got, body)
		}
	})
}
