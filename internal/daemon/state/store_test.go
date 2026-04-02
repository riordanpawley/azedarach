package state

import (
	"errors"
	"reflect"
	"testing"
	"time"
)

func TestStoreRevisionMonotonicity(t *testing.T) {
	s := NewStore()
	ts := time.Date(2026, time.March, 24, 13, 0, 0, 0, time.UTC)
	s.nowFn = func() time.Time { return ts }

	e1, err := s.UpsertSession("proj", "s1", "aej", SessionStateStarting)
	if err != nil {
		t.Fatalf("UpsertSession #1: %v", err)
	}
	e2, err := s.UpsertSession("proj", "s1", "aej", SessionStateAttached)
	if err != nil {
		t.Fatalf("UpsertSession #2: %v", err)
	}
	e3, err := s.UpsertSession("proj", "s2", "aek", SessionStateStarting)
	if err != nil {
		t.Fatalf("UpsertSession #3: %v", err)
	}

	if e1.Revision != 1 || e2.Revision != 2 || e3.Revision != 3 {
		t.Fatalf("unexpected revisions: %d %d %d", e1.Revision, e2.Revision, e3.Revision)
	}
	if got := s.CurrentRevision("proj"); got != 3 {
		t.Fatalf("CurrentRevision = %d, want 3", got)
	}
}

func TestStoreSnapshotIsolation(t *testing.T) {
	s := NewStore()
	if _, err := s.UpsertSession("proj", "s1", "aej", SessionStateStarting); err != nil {
		t.Fatalf("UpsertSession: %v", err)
	}

	snap := s.ReadSnapshot("proj")
	if snap.Revision != 1 {
		t.Fatalf("snapshot revision = %d, want 1", snap.Revision)
	}
	snap.Sessions["s1"] = Session{ID: "s1", State: SessionStateStopped}

	session, err := s.Session("proj", "s1")
	if err != nil {
		t.Fatalf("Session: %v", err)
	}
	if session.State != SessionStateStarting {
		t.Fatalf("store mutated via snapshot copy: %s", session.State)
	}
}

func TestStoreInvalidTransitionDoesNotAdvanceRevision(t *testing.T) {
	s := NewStore()
	if _, err := s.UpsertSession("proj", "s1", "aej", SessionStateStarting); err != nil {
		t.Fatalf("UpsertSession start: %v", err)
	}
	if _, err := s.UpsertSession("proj", "s1", "aej", SessionStatePaused); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("expected ErrInvalidTransition, got %v", err)
	}
	if got := s.CurrentRevision("proj"); got != 1 {
		t.Fatalf("revision after rejected transition = %d, want 1", got)
	}
}

func TestStoreSessionByIssueIDReturnsMostRecent(t *testing.T) {
	s := NewStore()
	ts := time.Date(2026, time.April, 2, 10, 0, 0, 0, time.UTC)
	s.nowFn = func() time.Time {
		current := ts
		ts = ts.Add(1 * time.Minute)
		return current
	}

	if _, err := s.UpsertSession("proj", "s-older", "az-123", SessionStateStopped); err != nil {
		t.Fatalf("UpsertSession older: %v", err)
	}
	if _, err := s.UpsertSession("proj", "s-other", "az-999", SessionStateAttached); err != nil {
		t.Fatalf("UpsertSession other issue: %v", err)
	}
	if _, err := s.UpsertSession("proj", "s-newest", "az-123", SessionStateAttached); err != nil {
		t.Fatalf("UpsertSession newest: %v", err)
	}

	for i := 0; i < 32; i++ {
		got, ok := s.SessionByIssueID("proj", "az-123")
		if !ok {
			t.Fatalf("SessionByIssueID returned !ok on iteration %d", i)
		}
		if got.ID != "s-newest" {
			t.Fatalf("SessionByIssueID ID = %s, want s-newest (iteration %d)", got.ID, i)
		}
		if got.State != SessionStateAttached {
			t.Fatalf("SessionByIssueID state = %s, want %s", got.State, SessionStateAttached)
		}
	}
}

func TestStoreUpsertSession_PreservesStartTimestampAcrossLifecycle(t *testing.T) {
	s := NewStore()
	base := time.Date(2026, time.April, 2, 0, 0, 0, 0, time.UTC)
	current := base
	s.nowFn = func() time.Time { return current }

	if _, err := s.UpsertSession("proj", "s1", "blx", SessionStateStarting); err != nil {
		t.Fatalf("starting transition: %v", err)
	}
	current = current.Add(2 * time.Minute)
	if _, err := s.UpsertSession("proj", "s1", "blx", SessionStateAttached); err != nil {
		t.Fatalf("attached transition: %v", err)
	}
	current = current.Add(2 * time.Minute)
	if _, err := s.UpsertSession("proj", "s1", "blx", SessionStatePaused); err != nil {
		t.Fatalf("paused transition: %v", err)
	}

	session, err := s.Session("proj", "s1")
	if err != nil {
		t.Fatalf("session lookup: %v", err)
	}
	if !session.UpdatedAt.Equal(base) {
		t.Fatalf("updated_at = %v, want preserved start time %v", session.UpdatedAt, base)
	}
}

func TestStoreUpsertSession_ResetsStartTimestampAfterRestart(t *testing.T) {
	s := NewStore()
	base := time.Date(2026, time.April, 2, 0, 0, 0, 0, time.UTC)
	current := base
	s.nowFn = func() time.Time { return current }

	if _, err := s.UpsertSession("proj", "s1", "blx", SessionStateStarting); err != nil {
		t.Fatalf("starting transition: %v", err)
	}
	current = current.Add(2 * time.Minute)
	if _, err := s.UpsertSession("proj", "s1", "blx", SessionStateAttached); err != nil {
		t.Fatalf("attached transition: %v", err)
	}
	current = current.Add(1 * time.Minute)
	if _, err := s.UpsertSession("proj", "s1", "blx", SessionStateStopped); err != nil {
		t.Fatalf("stopped transition: %v", err)
	}

	restartAt := current.Add(5 * time.Minute)
	current = restartAt
	if _, err := s.UpsertSession("proj", "s1", "blx", SessionStateStarting); err != nil {
		t.Fatalf("restart starting transition: %v", err)
	}

	session, err := s.Session("proj", "s1")
	if err != nil {
		t.Fatalf("session lookup: %v", err)
	}
	if !session.UpdatedAt.Equal(restartAt) {
		t.Fatalf("updated_at = %v, want restart time %v", session.UpdatedAt, restartAt)
	}
}

func TestStoreProjectIDs(t *testing.T) {
	s := NewStore()
	if _, err := s.UpsertSession("proj-b", "s1", "az-1", SessionStateStarting); err != nil {
		t.Fatalf("UpsertSession proj-b: %v", err)
	}
	if _, err := s.UpsertSession(" proj-a ", "s2", "az-2", SessionStateStarting); err != nil {
		t.Fatalf("UpsertSession proj-a: %v", err)
	}
	if _, err := s.UpsertSession("", "s3", "az-3", SessionStateStarting); err != nil {
		t.Fatalf("UpsertSession default: %v", err)
	}

	got := s.ProjectIDs()
	want := []string{"default", "proj-a", "proj-b"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ProjectIDs() = %v, want %v", got, want)
	}
}
