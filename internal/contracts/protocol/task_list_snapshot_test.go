package protocol

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/vmihailenco/msgpack/v5"
)

func TestTaskListSnapshotPayloadContractConstants(t *testing.T) {
	if got, want := TaskListSnapshotSchemaVersion, uint16(2); got != want {
		t.Fatalf("TaskListSnapshotSchemaVersion = %d, want %d", got, want)
	}
}

func TestTaskListSnapshotPayloadJSONShapeIsDeterministic(t *testing.T) {
	now := time.Date(2026, time.April, 2, 10, 30, 0, 0, time.UTC)
	checkedAt := time.Date(2026, time.April, 2, 10, 31, 45, 0, time.UTC)
	payload := TaskListSnapshotPayload{
		SchemaVersion:    TaskListSnapshotSchemaVersion,
		ProtocolVersion:  CurrentVersion,
		SnapshotRevision: 17,
		ProjectID:        "proj-joined",
		LastCheckedAt:    checkedAt,
		Freshness:        TaskListFreshnessFresh,
		Tasks: []domain.Task{
			{
				ID:          "az-1",
				Title:       "Joined snapshot",
				Description: "daemon-authored issue/session/worktree payload",
				Status:      domain.StatusInProgress,
				Priority:    domain.P1,
				Type:        domain.TypeTask,
				Session: &domain.Session{
					IssueID:   "az-1",
					State:     domain.SessionBusy,
					StartedAt: &now,
					Worktree:  "/tmp/repo-az-1",
				},
				HasWorktree:           true,
				HasUncommittedChanges: true,
				GitAheadCount:         2,
				GitBehindCount:        1,
				GitAdditions:          7,
				GitDeletions:          3,
			},
		},
	}

	got, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal task list snapshot: %v", err)
	}
	want := `{"schema_version":2,"protocol_version":3,"snapshot_revision":17,"project_id":"proj-joined","last_checked_at":"2026-04-02T10:31:45Z","freshness":"fresh","tasks":[{"id":"az-1","title":"Joined snapshot","description":"daemon-authored issue/session/worktree payload","status":"in_progress","priority":1,"issue_type":"task","session":{"issue_id":"az-1","state":"busy","started_at":"2026-04-02T10:30:00Z","worktree":"/tmp/repo-az-1"},"has_worktree":true,"git_ahead_count":2,"git_behind_count":1,"has_uncommitted_changes":true,"git_additions":7,"git_deletions":3,"created_at":"0001-01-01T00:00:00Z","updated_at":"0001-01-01T00:00:00Z"}]}`
	if string(got) != want {
		t.Fatalf("json = %s, want %s", string(got), want)
	}
}

func TestTaskListSnapshotPayloadMessagePackRoundTrip(t *testing.T) {
	checkedAt := time.Date(2026, time.April, 2, 14, 0, 0, 0, time.UTC)
	payload := TaskListSnapshotPayload{
		SchemaVersion:    TaskListSnapshotSchemaVersion,
		ProtocolVersion:  CurrentVersion,
		SnapshotRevision: 5,
		ProjectID:        "proj-roundtrip",
		LastCheckedAt:    checkedAt,
		Freshness:        TaskListFreshnessStale,
		Tasks: []domain.Task{{
			ID:       "az-7",
			Title:    "Roundtrip",
			Status:   domain.StatusBlocked,
			Priority: domain.P2,
			Type:     domain.TypeBug,
		}},
	}

	data, err := msgpack.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal task list snapshot: %v", err)
	}

	var got TaskListSnapshotPayload
	if err := msgpack.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal task list snapshot: %v", err)
	}
	if got.SchemaVersion != payload.SchemaVersion || got.ProtocolVersion != payload.ProtocolVersion || got.SnapshotRevision != payload.SnapshotRevision || got.ProjectID != payload.ProjectID || !got.LastCheckedAt.Equal(payload.LastCheckedAt) || got.Freshness != payload.Freshness {
		t.Fatalf("roundtrip header = %+v, want %+v", got, payload)
	}
	if len(got.Tasks) != 1 || got.Tasks[0].ID != "az-7" || got.Tasks[0].Type != domain.TypeBug {
		t.Fatalf("roundtrip tasks = %+v, want %+v", got.Tasks, payload.Tasks)
	}
}

func TestDecodeTaskListSnapshotPayloadRejectsVersionMismatch(t *testing.T) {
	t.Run("schema version", func(t *testing.T) {
		body := []byte(`{"schema_version":99,"protocol_version":3,"snapshot_revision":1,"project_id":"proj","last_checked_at":"2026-04-02T10:31:45Z","freshness":"fresh","tasks":[]}`)

		_, err := DecodeTaskListSnapshotPayload(body)
		if err == nil {
			t.Fatal("expected schema version mismatch error")
		}
		if got := err.Error(); !strings.Contains(got, "schema_version mismatch: expected 2, actual 99") {
			t.Fatalf("error = %q, want expected/actual schema mismatch", got)
		}
	})

	t.Run("protocol version", func(t *testing.T) {
		body := []byte(`{"schema_version":2,"protocol_version":99,"snapshot_revision":1,"project_id":"proj","last_checked_at":"2026-04-02T10:31:45Z","freshness":"fresh","tasks":[]}`)

		_, err := DecodeTaskListSnapshotPayload(body)
		if err == nil {
			t.Fatal("expected protocol version mismatch error")
		}
		if got := err.Error(); !strings.Contains(got, "protocol_version mismatch: expected 3, actual 99") {
			t.Fatalf("error = %q, want expected/actual protocol mismatch", got)
		}
	})

	t.Run("missing freshness metadata", func(t *testing.T) {
		body := []byte(`{"schema_version":2,"protocol_version":3,"snapshot_revision":1,"project_id":"proj","tasks":[]}`)

		_, err := DecodeTaskListSnapshotPayload(body)
		if err == nil {
			t.Fatal("expected freshness metadata error")
		}
		if got := err.Error(); !strings.Contains(got, "missing last_checked_at") {
			t.Fatalf("error = %q, want missing last_checked_at", got)
		}
	})

	t.Run("invalid freshness", func(t *testing.T) {
		body := []byte(`{"schema_version":2,"protocol_version":3,"snapshot_revision":1,"project_id":"proj","last_checked_at":"2026-04-02T10:31:45Z","freshness":"unknown","tasks":[]}`)

		_, err := DecodeTaskListSnapshotPayload(body)
		if err == nil {
			t.Fatal("expected freshness mismatch error")
		}
		if got := err.Error(); !strings.Contains(got, `freshness mismatch: expected one of [fresh stale], actual "unknown"`) {
			t.Fatalf("error = %q, want freshness mismatch", got)
		}
	})
}
