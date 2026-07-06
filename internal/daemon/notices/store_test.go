package notices

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	"github.com/riordanpawley/azedarach/internal/daemon/publish"
	"github.com/riordanpawley/azedarach/internal/naming"
)

func TestSQLiteStoreUpsertActiveDedupesWithinProject(t *testing.T) {
	ctx := context.Background()
	store := NewAtPath(filepath.Join(t.TempDir(), "azedarach.db"), nil)
	defer store.Close()
	now := time.Unix(1_700_000_000, 0).UTC()

	first, created, err := store.UpsertActive(ctx, noticeCandidate("notice-1", "proj-1", "az-1", now))
	if err != nil {
		t.Fatalf("first upsert: %v", err)
	}
	if !created {
		t.Fatal("first upsert created = false, want true")
	}

	secondCandidate := noticeCandidate("notice-2", "proj-1", "az-1", now.Add(time.Minute))
	secondCandidate.Summary = "second failure"
	second, created, err := store.UpsertActive(ctx, secondCandidate)
	if err != nil {
		t.Fatalf("second upsert: %v", err)
	}
	if created {
		t.Fatal("second upsert created = true, want dedupe update")
	}
	if second.NoticeID != first.NoticeID {
		t.Fatalf("dedupe notice id = %s, want %s", second.NoticeID, first.NoticeID)
	}
	if second.OccurrenceCount != 2 || second.Summary != "second failure" || second.Read {
		t.Fatalf("dedupe record = %+v, want occurrence_count=2 unread updated summary", second)
	}

	otherProject, created, err := store.UpsertActive(ctx, noticeCandidate("notice-3", "proj-2", "az-1", now.Add(2*time.Minute)))
	if err != nil {
		t.Fatalf("other project upsert: %v", err)
	}
	if !created || otherProject.NoticeID == first.NoticeID {
		t.Fatalf("cross-project upsert = created %v id %s, want isolated record", created, otherProject.NoticeID)
	}

	records, err := store.List(ctx, Query{ProjectID: "proj-1", States: []State{StateActive}})
	if err != nil {
		t.Fatalf("list proj-1: %v", err)
	}
	if len(records) != 1 || records[0].NoticeID != first.NoticeID {
		t.Fatalf("proj-1 records = %+v, want one deduped record", records)
	}

	records, err = store.List(ctx, Query{ProjectID: "proj-1", DedupeKey: first.DedupeKey})
	if err != nil {
		t.Fatalf("list by dedupe key: %v", err)
	}
	if len(records) != 1 || records[0].NoticeID != first.NoticeID {
		t.Fatalf("dedupe-key records = %+v, want first notice", records)
	}
}

func TestSQLiteStoreUpdateLifecycleAndRejectsInvalidTransition(t *testing.T) {
	ctx := context.Background()
	store := NewAtPath(filepath.Join(t.TempDir(), "azedarach.db"), nil)
	defer store.Close()
	now := time.Unix(1_700_000_000, 0).UTC()
	record, _, err := store.UpsertActive(ctx, noticeCandidate("notice-1", "proj-1", "az-1", now))
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}

	read := true
	updated, changed, err := store.Update(ctx, UpdateParams{
		ProjectID: "proj-1",
		NoticeID:  record.NoticeID,
		Read:      &read,
		Now:       now.Add(time.Minute),
	})
	if err != nil {
		t.Fatalf("mark read: %v", err)
	}
	if !changed || !updated.Read {
		t.Fatalf("mark read changed=%v read=%v, want changed read", changed, updated.Read)
	}

	updated, changed, err = store.Update(ctx, UpdateParams{
		ProjectID: "proj-1",
		NoticeID:  record.NoticeID,
		Read:      &read,
		Now:       now.Add(2 * time.Minute),
	})
	if err != nil {
		t.Fatalf("idempotent mark read: %v", err)
	}
	if changed {
		t.Fatal("idempotent mark read changed = true, want false")
	}

	resolved, changed, err := store.Update(ctx, UpdateParams{
		ProjectID: "proj-1",
		NoticeID:  record.NoticeID,
		State:     StateResolved,
		Now:       now.Add(3 * time.Minute),
	})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if !changed || resolved.State != StateResolved || resolved.ResolvedAt == nil {
		t.Fatalf("resolve = %+v changed=%v, want resolved timestamp", resolved, changed)
	}

	_, _, err = store.Update(ctx, UpdateParams{
		ProjectID: "proj-1",
		NoticeID:  record.NoticeID,
		State:     StateActive,
		Now:       now.Add(4 * time.Minute),
	})
	if !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("resolved->active err = %v, want ErrInvalidTransition", err)
	}

	_, _, err = store.Update(ctx, UpdateParams{
		ProjectID: "proj-1",
		NoticeID:  record.NoticeID,
		State:     StateExpired,
		Now:       now.Add(5 * time.Minute),
	})
	if !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("client expiry err = %v, want ErrInvalidTransition", err)
	}
}

func TestSQLiteStoreRetentionExpiresThenDeletesTerminalNotices(t *testing.T) {
	ctx := context.Background()
	store := NewAtPath(filepath.Join(t.TempDir(), "azedarach.db"), nil)
	defer store.Close()
	now := time.Unix(1_700_000_000, 0).UTC()
	expiresAt := now.Add(time.Hour)
	candidate := noticeCandidate("notice-1", "proj-1", "az-1", now)
	candidate.ExpiresAt = &expiresAt
	record, _, err := store.UpsertActive(ctx, candidate)
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if _, _, err := store.Update(ctx, UpdateParams{
		ProjectID: "proj-1",
		NoticeID:  record.NoticeID,
		State:     StateDismissed,
		Now:       now.Add(time.Minute),
	}); err != nil {
		t.Fatalf("dismiss: %v", err)
	}

	expired, err := store.ExpireDue(ctx, ExpireQuery{ProjectID: "proj-1", Now: expiresAt.Add(time.Second)})
	if err != nil {
		t.Fatalf("expire due: %v", err)
	}
	if len(expired) != 1 || expired[0].State != StateExpired {
		t.Fatalf("expired = %+v, want one expired record", expired)
	}
	deleted, err := store.DeleteExpired(ctx, ExpireQuery{ProjectID: "proj-1", Now: expiresAt.Add(time.Second)})
	if err != nil {
		t.Fatalf("delete expired: %v", err)
	}
	if len(deleted) != 1 || deleted[0].NoticeID != record.NoticeID {
		t.Fatalf("deleted = %+v, want expired notice", deleted)
	}
	if _, err := store.Get(ctx, "proj-1", record.NoticeID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("get deleted err = %v, want ErrNotFound", err)
	}
}

func TestServicePublishesNoticeEvents(t *testing.T) {
	ctx := context.Background()
	hub := publish.NewHub(16, 8, nil)
	store := NewAtPath(filepath.Join(t.TempDir(), "azedarach.db"), nil)
	service := NewService(ServiceConfig{
		Repository:   store,
		Hub:          hub,
		NextRevision: sequentialNoticeRevision(),
	})
	defer service.Close()
	ch, cancel := hub.Subscribe("proj-1", 0)
	defer cancel()
	now := time.Unix(1_700_000_000, 0).UTC()

	record, created, rev, err := service.Upsert(ctx, noticeCandidate("notice-1", "proj-1", "az-1", now))
	if err != nil {
		t.Fatalf("service upsert: %v", err)
	}
	if !created || rev != 1 {
		t.Fatalf("upsert created=%v rev=%d, want created rev 1", created, rev)
	}
	createdEvent := receiveNoticeEvent(t, ch)
	if createdEvent.Event != protocol.EventNoticeCreated || createdEvent.Revision != 1 {
		t.Fatalf("created event = %s rev %d, want notice.created rev 1", createdEvent.Event, createdEvent.Revision)
	}
	var createdBody protocol.NoticeEventBody
	if err := json.Unmarshal(createdEvent.Body, &createdBody); err != nil {
		t.Fatalf("unmarshal created body: %v", err)
	}
	if createdBody.Notice == nil || createdBody.Notice.NoticeID != record.NoticeID {
		t.Fatalf("created body = %+v, want notice payload", createdBody)
	}

	_, changed, rev, err := service.ExecuteAction(ctx, "proj-1", record.NoticeID, "dismiss", now.Add(time.Minute))
	if err != nil {
		t.Fatalf("dismiss action: %v", err)
	}
	if !changed || rev != 2 {
		t.Fatalf("dismiss changed=%v rev=%d, want changed rev 2", changed, rev)
	}
	updatedEvent := receiveNoticeEvent(t, ch)
	if updatedEvent.Event != protocol.EventNoticeUpdated || updatedEvent.Revision != 2 {
		t.Fatalf("updated event = %s rev %d, want notice.updated rev 2", updatedEvent.Event, updatedEvent.Revision)
	}
}

func TestServiceDurableLifecycleSurvivesRestartSharedClientsAndFloodDedupe(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "azedarach.db")
	now := time.Unix(1_700_000_000, 0).UTC()
	nextRevision := sequentialNoticeRevision()
	newService := func(t *testing.T) *Service {
		t.Helper()
		service := NewService(ServiceConfig{
			Repository:   NewAtPath(dbPath, nil),
			NextRevision: nextRevision,
		})
		t.Cleanup(func() {
			_ = service.Close()
		})
		return service
	}

	firstDaemon := newService(t)
	var firstID string
	const repeatedFailures = 25
	for i := 0; i < repeatedFailures; i++ {
		candidate := noticeCandidate(
			"notice-flood-"+strconv.Itoa(i),
			"proj-1",
			"az-1",
			now.Add(time.Duration(i)*time.Second),
		)
		candidate.Summary = fmt.Sprintf("deduped failure %02d", i+1)
		record, created, _, err := firstDaemon.Upsert(ctx, candidate)
		if err != nil {
			t.Fatalf("upsert flood notice %d: %v", i, err)
		}
		if i == 0 {
			if !created {
				t.Fatal("first flood notice created = false, want true")
			}
			firstID = record.NoticeID
			continue
		}
		if created {
			t.Fatalf("flood notice %d created = true, want dedupe update", i)
		}
		if record.NoticeID != firstID {
			t.Fatalf("flood notice %d id = %q, want original id %q", i, record.NoticeID, firstID)
		}
	}
	if err := firstDaemon.Close(); err != nil {
		t.Fatalf("close first daemon service: %v", err)
	}

	restartedDaemon := newService(t)
	active, err := restartedDaemon.List(ctx, Query{
		ProjectID: "proj-1",
		States:    []State{StateActive},
	})
	if err != nil {
		t.Fatalf("list active notices after restart: %v", err)
	}
	if len(active) != 1 {
		t.Fatalf("active notices after restart = %+v, want one deduped notice", active)
	}
	if active[0].NoticeID != firstID ||
		active[0].OccurrenceCount != repeatedFailures ||
		active[0].Summary != "deduped failure 25" ||
		active[0].Read {
		t.Fatalf("deduped notice after restart = %+v, want original id, occurrence count %d, latest summary, unread", active[0], repeatedFailures)
	}

	secondClient := newService(t)
	read := true
	updated, changed, _, err := secondClient.Update(ctx, UpdateParams{
		ProjectID: "proj-1",
		NoticeID:  firstID,
		Read:      &read,
		Now:       now.Add(time.Hour),
	})
	if err != nil {
		t.Fatalf("mark read from second client: %v", err)
	}
	if !changed || !updated.Read {
		t.Fatalf("mark read changed=%v read=%v, want changed read", changed, updated.Read)
	}

	visibleToRestarted, err := restartedDaemon.Get(ctx, "proj-1", firstID)
	if err != nil {
		t.Fatalf("get notice from restarted daemon after second client update: %v", err)
	}
	if !visibleToRestarted.Read {
		t.Fatalf("shared notice read = false, want second client lifecycle update visible")
	}

	dismissed, changed, _, err := restartedDaemon.Update(ctx, UpdateParams{
		ProjectID: "proj-1",
		NoticeID:  firstID,
		State:     StateDismissed,
		Now:       now.Add(2 * time.Hour),
	})
	if err != nil {
		t.Fatalf("dismiss from restarted daemon: %v", err)
	}
	if !changed || dismissed.State != StateDismissed || dismissed.DismissedAt == nil {
		t.Fatalf("dismissed notice = %+v changed=%v, want dismissed timestamp", dismissed, changed)
	}
	if err := restartedDaemon.Close(); err != nil {
		t.Fatalf("close restarted daemon service: %v", err)
	}
	if err := secondClient.Close(); err != nil {
		t.Fatalf("close second client service: %v", err)
	}

	finalDaemon := newService(t)
	active, err = finalDaemon.List(ctx, Query{
		ProjectID: "proj-1",
		States:    []State{StateActive},
	})
	if err != nil {
		t.Fatalf("list active notices after final restart: %v", err)
	}
	if len(active) != 0 {
		t.Fatalf("active notices after dismiss restart = %+v, want none", active)
	}
	terminal, err := finalDaemon.List(ctx, Query{
		ProjectID: "proj-1",
		States:    []State{StateDismissed},
	})
	if err != nil {
		t.Fatalf("list dismissed notices after final restart: %v", err)
	}
	if len(terminal) != 1 || terminal[0].NoticeID != firstID || !terminal[0].Read || terminal[0].DismissedAt == nil {
		t.Fatalf("dismissed notices after final restart = %+v, want persisted read dismissed notice", terminal)
	}
}

func noticeCandidate(noticeID, projectID, issueID string, now time.Time) Candidate {
	return Candidate{
		NoticeID:  noticeID,
		ProjectID: projectID,
		Scope: Scope{
			Type: "task",
			ID:   issueID,
		},
		Source: &Source{
			OperationID:    naming.OperationID("op-" + issueID),
			OperationKind:  "task.update_status",
			OperationState: protocol.OperationStateFailed,
			Producer:       "test",
		},
		Severity:       SeverityError,
		Category:       "mutation_rejected",
		Title:          "Mutation failed",
		Summary:        "first failure",
		Detail:         "daemon rejected the mutation",
		Cause:          &Cause{Code: "invalid_transition", Message: "invalid status transition", Retryable: true},
		Actions:        []Action{{ActionID: "dismiss", Kind: "dismiss", Label: "Dismiss", Enabled: true}},
		DedupeKey:      projectID + ":task:" + issueID + ":mutation_rejected",
		OccurredAt:     now,
		RetentionClass: RetentionError,
	}
}

func sequentialNoticeRevision() func(string) uint64 {
	var rev uint64
	return func(string) uint64 {
		rev++
		return rev
	}
}

func receiveNoticeEvent(t *testing.T, ch <-chan protocol.EventEnvelope) protocol.EventEnvelope {
	t.Helper()
	select {
	case event := <-ch:
		return event
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for notice event")
		return protocol.EventEnvelope{}
	}
}
