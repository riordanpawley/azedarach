package protocol

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/riordanpawley/azedarach/internal/domain"
)

func TestTaskEventBodyJSONShape(t *testing.T) {
	updatedAt := time.Date(2026, time.April, 26, 10, 30, 0, 0, time.UTC)
	body := TaskEventBody{
		ProjectID: "proj-a",
		TaskID:    "az-1",
		Task: &domain.Task{
			ID:     "az-1",
			Title:  "Live task",
			Status: domain.StatusInProgress,
		},
		UpdatedAt: updatedAt,
	}

	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal task event body: %v", err)
	}
	want := `{"project_id":"proj-a","task_id":"az-1","task":{"id":"az-1","title":"Live task","status":"in_progress","priority":0,"issue_type":"","created_at":"0001-01-01T00:00:00Z","updated_at":"0001-01-01T00:00:00Z"},"updated_at":"2026-04-26T10:30:00Z"}`
	if string(raw) != want {
		t.Fatalf("task event json = %s, want %s", raw, want)
	}
}
