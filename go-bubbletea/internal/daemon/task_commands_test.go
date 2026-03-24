package daemon

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/riordanpawley/azedarach/internal/domain"
)

func TestBuildTaskSnapshotExportBodyIsDeterministic(t *testing.T) {
	originalNow := timeNow
	t.Cleanup(func() {
		timeNow = originalNow
	})
	timeNow = func() time.Time {
		return time.Date(2026, 3, 25, 12, 34, 56, 789000000, time.UTC)
	}
	wantCapturedAt := time.Date(2026, 3, 25, 12, 34, 56, 789000000, time.UTC).UnixMilli()

	parentID := "parent-1"
	body := buildTaskSnapshotExportBody(
		"proj",
		17,
		[]domain.Task{
			{
				ID:           "b-task",
				Title:        "Bravo",
				Status:       domain.StatusBlocked,
				Priority:     domain.P2,
				Type:         domain.TypeTask,
				UpdatedAt:    time.Now(),
				Dependencies: []domain.Dependency{{ID: "dep-1", Type: domain.DependencyBlocks}},
			},
			{
				ID:           "a-task",
				Title:        "Alpha",
				Status:       domain.StatusInProgress,
				Priority:     domain.P1,
				Type:         domain.TypeBug,
				ParentID:     &parentID,
				Dependencies: []domain.Dependency{{ID: "dep-2", Type: domain.DependencyRelatedTo}, {ID: "dep-3", Type: domain.DependencyBlocks}},
			},
		},
		[]string{"b-task", "a-task"},
	)

	if got, want := body.SchemaVersion, uint16(1); got != want {
		t.Fatalf("SchemaVersion = %d, want %d", got, want)
	}
	if got, want := body.SnapshotRevision, uint64(17); got != want {
		t.Fatalf("SnapshotRevision = %d, want %d", got, want)
	}
	if got, want := body.CapturedAtMs, wantCapturedAt; got != want {
		t.Fatalf("CapturedAtMs = %d, want %d", got, want)
	}
	if got, want := body.ProjectID, "proj"; got != want {
		t.Fatalf("ProjectID = %q, want %q", got, want)
	}
	if got, want := body.TaskCount, 2; got != want {
		t.Fatalf("TaskCount = %d, want %d", got, want)
	}
	if got, want := body.SessionCount, 2; got != want {
		t.Fatalf("SessionCount = %d, want %d", got, want)
	}

	if got, want := len(body.Tasks), 2; got != want {
		t.Fatalf("len(Tasks) = %d, want %d", got, want)
	}
	if got, want := body.Tasks[0].ID, "a-task"; got != want {
		t.Fatalf("Tasks[0].ID = %q, want %q", got, want)
	}
	if got, want := body.Tasks[0].SessionAttached, true; got != want {
		t.Fatalf("Tasks[0].SessionAttached = %v, want %v", got, want)
	}
	if got, want := body.Tasks[0].DependencyCount, 2; got != want {
		t.Fatalf("Tasks[0].DependencyCount = %d, want %d", got, want)
	}
	if got, want := body.Tasks[0].Critical, false; got != want {
		t.Fatalf("Tasks[0].Critical = %v, want %v", got, want)
	}
	if body.Tasks[0].ParentID == nil || *body.Tasks[0].ParentID != parentID {
		t.Fatalf("Tasks[0].ParentID = %+v, want %q", body.Tasks[0].ParentID, parentID)
	}
	if got, want := body.Tasks[1].ID, "b-task"; got != want {
		t.Fatalf("Tasks[1].ID = %q, want %q", got, want)
	}
	if got, want := body.Tasks[1].Critical, true; got != want {
		t.Fatalf("Tasks[1].Critical = %v, want %v", got, want)
	}

	if got, want := len(body.Sessions), 2; got != want {
		t.Fatalf("len(Sessions) = %d, want %d", got, want)
	}
	if got, want := body.Sessions[0].Name, "a-task"; got != want {
		t.Fatalf("Sessions[0].Name = %q, want %q", got, want)
	}
	if got, want := body.Sessions[1].Name, "b-task"; got != want {
		t.Fatalf("Sessions[1].Name = %q, want %q", got, want)
	}

	if _, err := json.Marshal(body); err != nil {
		t.Fatalf("marshal snapshot body: %v", err)
	}
}
