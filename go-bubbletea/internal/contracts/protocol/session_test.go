package protocol

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/vmihailenco/msgpack/v5"
)

func TestSessionProjectionContractConstants(t *testing.T) {
	if got, want := EventSessionUpdated, "session.updated"; got != want {
		t.Fatalf("EventSessionUpdated = %q, want %q", got, want)
	}
	if got, want := SessionLifecycleStateStarting, SessionLifecycleState("starting"); got != want {
		t.Fatalf("SessionLifecycleStateStarting = %q, want %q", got, want)
	}
	if got, want := SessionLifecycleStateAttached, SessionLifecycleState("attached"); got != want {
		t.Fatalf("SessionLifecycleStateAttached = %q, want %q", got, want)
	}
	if got, want := SessionLifecycleStatePaused, SessionLifecycleState("paused"); got != want {
		t.Fatalf("SessionLifecycleStatePaused = %q, want %q", got, want)
	}
	if got, want := SessionLifecycleStateStopped, SessionLifecycleState("stopped"); got != want {
		t.Fatalf("SessionLifecycleStateStopped = %q, want %q", got, want)
	}
}

func TestSessionProjectionEventBodyJSONShapeIsDeterministic(t *testing.T) {
	updatedAt := time.Date(2026, time.March, 31, 1, 2, 3, 456789000, time.UTC)
	payload := SessionProjectionEventBody{
		ProjectID: "proj-1",
		Revision:  7,
		Session: SessionProjection{
			SessionID: "proj-1-az-42",
			IssueID:   "az-42",
			State:     SessionLifecycleStateAttached,
			UpdatedAt: updatedAt,
		},
	}

	got := mustMarshalSessionJSON(t, payload)
	want := `{"project_id":"proj-1","revision":7,"session":{"session_id":"proj-1-az-42","issue_id":"az-42","state":"attached","updated_at":"2026-03-31T01:02:03.456789Z"}}`
	if got != want {
		t.Fatalf("json = %s, want %s", got, want)
	}
}

func TestSessionProjectionEventBodyMessagePackRoundTrip(t *testing.T) {
	payload := SessionProjectionEventBody{
		ProjectID: "proj-1",
		Revision:  7,
		Session: SessionProjection{
			SessionID: "proj-1-az-42",
			IssueID:   "az-42",
			State:     SessionLifecycleStatePaused,
			UpdatedAt: time.Date(2026, time.March, 31, 1, 2, 3, 0, time.UTC),
		},
	}

	data, err := msgpack.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal session projection event body: %v", err)
	}

	var got SessionProjectionEventBody
	if err := msgpack.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal session projection event body: %v", err)
	}
	if got.ProjectID != payload.ProjectID || got.Revision != payload.Revision {
		t.Fatalf("roundtrip = %+v, want %+v", got, payload)
	}
	if got.Session.SessionID != payload.Session.SessionID || got.Session.IssueID != payload.Session.IssueID || got.Session.State != payload.Session.State {
		t.Fatalf("roundtrip = %+v, want %+v", got, payload)
	}
	if !got.Session.UpdatedAt.Equal(payload.Session.UpdatedAt) {
		t.Fatalf("roundtrip updated_at = %s, want %s", got.Session.UpdatedAt, payload.Session.UpdatedAt)
	}
}

func mustMarshalSessionJSON(t *testing.T, payload SessionProjectionEventBody) string {
	t.Helper()

	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal session projection event body: %v", err)
	}
	return string(data)
}
