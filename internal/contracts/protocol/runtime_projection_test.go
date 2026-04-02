package protocol

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/vmihailenco/msgpack/v5"
)

func TestRuntimeProjectionContractConstants(t *testing.T) {
	if got, want := RuntimeProjectionSchemaVersion, uint16(1); got != want {
		t.Fatalf("RuntimeProjectionSchemaVersion = %d, want %d", got, want)
	}
}

func TestRuntimeProjectionSnapshotPayloadJSONShapeIsDeterministic(t *testing.T) {
	startedAt := time.Date(2026, time.April, 1, 8, 0, 0, 123456000, time.UTC)
	updatedAt := time.Date(2026, time.April, 1, 8, 5, 0, 654321000, time.UTC)
	payload := RuntimeProjectionSnapshotPayload{
		SchemaVersion:    RuntimeProjectionSchemaVersion,
		ProtocolVersion:  CurrentVersion,
		SnapshotRevision: 11,
		ProjectID:        "proj-1",
		Projections: []RuntimeProjection{
			{
				ProjectID: "proj-1",
				IssueID:   "az-42",
				Worktree: RuntimeWorktreeProjection{
					Exists:             true,
					Path:               "/tmp/repo-az-42",
					Branch:             "riordan/az-42/task",
					Healthy:            true,
					GitStatusUpdatedAt: &updatedAt,
				},
				Git: RuntimeGitProjection{
					HasUncommittedChanges: true,
					GitAdditions:          7,
					GitDeletions:          3,
					GitAheadCount:         2,
					GitBehindCount:        1,
					ActiveOperation: &RuntimeOperationProjection{
						OperationID:     "op-123",
						State:           OperationStateRunning,
						ProgressPercent: 45,
						Message:         "syncing runtime projection",
					},
				},
				Session: RuntimeSessionProjection{
					HasSession: true,
					SessionID:  "sess-42",
					State:      SessionLifecycleStateAttached,
					StartedAt:  &startedAt,
					UpdatedAt:  &updatedAt,
					Worktree:   "/tmp/repo-az-42",
				},
				Agent: RuntimeAgentProjection{
					Status:    "attached",
					SessionID: "sess-42",
					UpdatedAt: &updatedAt,
				},
			},
		},
	}

	got, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal runtime projection snapshot: %v", err)
	}
	want := `{"schema_version":1,"protocol_version":2,"snapshot_revision":11,"project_id":"proj-1","projections":[{"project_id":"proj-1","issue_id":"az-42","worktree":{"exists":true,"path":"/tmp/repo-az-42","branch":"riordan/az-42/task","healthy":true,"git_status_updated_at":"2026-04-01T08:05:00.654321Z"},"git":{"has_uncommitted_changes":true,"git_additions":7,"git_deletions":3,"git_ahead_count":2,"git_behind_count":1,"active_operation":{"operation_id":"op-123","state":"running","progress_percent":45,"message":"syncing runtime projection"}},"session":{"has_session":true,"session_id":"sess-42","state":"attached","started_at":"2026-04-01T08:00:00.123456Z","updated_at":"2026-04-01T08:05:00.654321Z","worktree":"/tmp/repo-az-42"},"agent":{"status":"attached","session_id":"sess-42","updated_at":"2026-04-01T08:05:00.654321Z"}}]}`
	if string(got) != want {
		t.Fatalf("json = %s, want %s", string(got), want)
	}
}

func TestRuntimeProjectionSnapshotPayloadMessagePackRoundTrip(t *testing.T) {
	now := time.Date(2026, time.April, 1, 9, 0, 0, 0, time.UTC)
	payload := RuntimeProjectionSnapshotPayload{
		SchemaVersion:    RuntimeProjectionSchemaVersion,
		ProtocolVersion:  CurrentVersion,
		SnapshotRevision: 99,
		ProjectID:        "proj-2",
		Projections: []RuntimeProjection{
			{
				ProjectID: "proj-2",
				IssueID:   "az-7",
				Worktree: RuntimeWorktreeProjection{
					Exists:             true,
					Path:               "/tmp/repo-az-7",
					Branch:             "riordan/az-7/task",
					Healthy:            false,
					GitStatusUpdatedAt: &now,
				},
				Session: RuntimeSessionProjection{
					HasSession: true,
					SessionID:  "sess-7",
					State:      SessionLifecycleStatePaused,
					UpdatedAt:  &now,
				},
			},
		},
	}

	data, err := msgpack.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal runtime projection snapshot: %v", err)
	}

	var got RuntimeProjectionSnapshotPayload
	if err := msgpack.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal runtime projection snapshot: %v", err)
	}
	if got.SchemaVersion != payload.SchemaVersion || got.ProtocolVersion != payload.ProtocolVersion || got.SnapshotRevision != payload.SnapshotRevision || got.ProjectID != payload.ProjectID {
		t.Fatalf("roundtrip header = %+v, want %+v", got, payload)
	}
	if len(got.Projections) != 1 {
		t.Fatalf("roundtrip projections = %d, want 1", len(got.Projections))
	}
	if got.Projections[0].IssueID != "az-7" || got.Projections[0].Worktree.Path != "/tmp/repo-az-7" {
		t.Fatalf("roundtrip projection = %+v, want %+v", got.Projections[0], payload.Projections[0])
	}
	if got.Projections[0].Session.SessionID != "sess-7" || got.Projections[0].Session.State != SessionLifecycleStatePaused {
		t.Fatalf("roundtrip session = %+v, want %+v", got.Projections[0].Session, payload.Projections[0].Session)
	}
}

func TestRuntimeProjectionEventBodyMessagePackRoundTrip(t *testing.T) {
	now := time.Date(2026, time.April, 1, 10, 0, 0, 0, time.UTC)
	payload := RuntimeProjectionEventBody{
		ProjectID: "proj-3",
		Revision:  14,
		Projection: RuntimeProjection{
			ProjectID: "proj-3",
			IssueID:   "az-99",
			Git: RuntimeGitProjection{
				HasUncommittedChanges: false,
				GitAdditions:          0,
				GitDeletions:          0,
				GitAheadCount:         4,
				GitBehindCount:        2,
			},
			Agent: RuntimeAgentProjection{
				Status:    "idle",
				SessionID: "sess-99",
				UpdatedAt: &now,
			},
		},
	}

	data, err := msgpack.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal runtime projection event body: %v", err)
	}

	var got RuntimeProjectionEventBody
	if err := msgpack.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal runtime projection event body: %v", err)
	}
	if got.ProjectID != payload.ProjectID || got.Revision != payload.Revision {
		t.Fatalf("roundtrip header = %+v, want %+v", got, payload)
	}
	if got.Projection.IssueID != payload.Projection.IssueID {
		t.Fatalf("roundtrip projection issue = %q, want %q", got.Projection.IssueID, payload.Projection.IssueID)
	}
	if got.Projection.Git.GitAheadCount != 4 || got.Projection.Git.GitBehindCount != 2 {
		t.Fatalf("roundtrip git = %+v, want %+v", got.Projection.Git, payload.Projection.Git)
	}
}

func TestRuntimeProjectionZeroValuesAreStable(t *testing.T) {
	payload := RuntimeProjectionSnapshotPayload{
		SchemaVersion:   RuntimeProjectionSchemaVersion,
		ProtocolVersion: CurrentVersion,
	}

	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal zero runtime projection snapshot: %v", err)
	}

	got := string(data)
	want := `{"schema_version":1,"protocol_version":2,"snapshot_revision":0,"project_id":"","projections":null}`
	if got != want {
		t.Fatalf("json = %s, want %s", got, want)
	}
}
