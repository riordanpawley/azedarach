package protocol

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/vmihailenco/msgpack/v5"
)

func TestUICommandBodySerialization(t *testing.T) {
	createdAt := time.Date(2026, time.May, 5, 15, 50, 0, 0, time.UTC)
	body := UICommandEventBody{
		ProjectID: "proj-ui",
		IssueID:   "az-1",
		Command:   UICommandOpenTaskWorkspace,
		RequestID: "req-ui",
		CreatedAt: createdAt,
	}

	jsonData, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("json marshal: %v", err)
	}
	var jsonOut UICommandEventBody
	if err := json.Unmarshal(jsonData, &jsonOut); err != nil {
		t.Fatalf("json unmarshal: %v", err)
	}
	if jsonOut != body {
		t.Fatalf("json body = %+v, want %+v", jsonOut, body)
	}

	msgpackData, err := msgpack.Marshal(body)
	if err != nil {
		t.Fatalf("msgpack marshal: %v", err)
	}
	var msgpackOut UICommandEventBody
	if err := msgpack.Unmarshal(msgpackData, &msgpackOut); err != nil {
		t.Fatalf("msgpack unmarshal: %v", err)
	}
	if msgpackOut.ProjectID != body.ProjectID ||
		msgpackOut.IssueID != body.IssueID ||
		msgpackOut.Command != body.Command ||
		msgpackOut.RequestID != body.RequestID ||
		!msgpackOut.CreatedAt.Equal(body.CreatedAt) {
		t.Fatalf("msgpack body = %+v, want %+v", msgpackOut, body)
	}
}
