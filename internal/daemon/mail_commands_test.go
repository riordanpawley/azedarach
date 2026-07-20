package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/naming"
	"github.com/riordanpawley/azedarach/internal/services/issues"
)

func TestMailWatchRecoversReviewReadyResubmissionsExactlyOnce(t *testing.T) {
	ctx := context.Background()
	repoDir := t.TempDir()
	client := newMigratedIssueClientAtPath(t, filepath.Join(repoDir, "issues.db"), slog.Default())
	t.Cleanup(func() { _ = client.CloseDB() })
	root, err := client.Create(ctx, issues.CreateTaskParams{Title: "root", Type: domain.TypeEpic, Priority: domain.P1, Status: domain.StatusInProgress})
	if err != nil {
		t.Fatal(err)
	}
	child, err := client.Create(ctx, issues.CreateTaskParams{Title: "child", Type: domain.TypeBug, Priority: domain.P1, Status: domain.StatusInProgress, ParentID: &root})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.AppendIssueObservationEvent(ctx, child, issues.IssueObservationEventParams{Type: "worker-integration-ready", Source: "test", Payload: mustWorkerEvidencePayload(t)}); err != nil {
		t.Fatal(err)
	}
	if err := client.Update(ctx, child, domain.StatusInReview); err != nil {
		t.Fatal(err)
	}
	d := &Daemon{cfg: Config{RepoDir: repoDir, Logger: slog.Default()}, issueClientsByProject: map[string]*issues.Client{"project": client}}
	req := protocol.RequestEnvelope{Meta: protocol.Metadata{ProjectID: "project"}}

	first, err := d.readMailboxEventsWithReviewReadyRecovery(ctx, req, repoDir, root)
	if err != nil {
		t.Fatal(err)
	}
	second, err := d.readMailboxEventsWithReviewReadyRecovery(ctx, req, repoDir, root)
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 1 || len(second) != 1 || first[0].Seq != 1 || first[0].Type != "worker-integration-ready" {
		t.Fatalf("first=%+v second=%+v, want one stable recovered publication", first, second)
	}
	if first[0].Payload["source_event_type"] != "worker-integration-ready" {
		t.Fatalf("payload = %+v, want normalized alias source diagnostics", first[0].Payload)
	}
	if _, validation := domain.ParseWorkerEvidencePacketBody(first[0].Body); !validation.Complete {
		t.Fatalf("replayed body = %s, validation=%+v, want canonical complete evidence", first[0].Body, validation)
	}
	encoded, err := json.Marshal(mailEventToProtocol(first[0]).Payload["worker_evidence_validation"])
	if err != nil || !bytes.Contains(encoded, []byte(`"storage":"issue_event_payload_json_v1"`)) {
		t.Fatalf("replay validation = %s err=%v, want source issue-event diagnostics preserved", encoded, err)
	}
	if err := d.ensureLegacyMailboxObservationProjection(ctx, "project", repoDir); err != nil {
		t.Fatal(err)
	}
	cutover, err := client.MailboxObservationProjectionCutoverState(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if cutover.State != "complete" || cutover.ImportedCount != 1 {
		t.Fatalf("cutover = %+v, want derived replay publication imported once", cutover)
	}
	restarted := &Daemon{cfg: Config{RepoDir: repoDir, Logger: slog.Default()}, issueClientsByProject: map[string]*issues.Client{"project": client}}
	afterRestart, err := restarted.readMailboxEventsWithReviewReadyRecovery(ctx, req, repoDir, root)
	if err != nil || len(afterRestart) != 1 {
		t.Fatalf("restart replay = %+v err=%v, want no duplicate", afterRestart, err)
	}

	if err := client.Update(ctx, child, domain.StatusInProgress); err != nil {
		t.Fatal(err)
	}
	if _, err := client.AppendIssueObservationEvent(ctx, child, issues.IssueObservationEventParams{Type: "worker.integration_ready", Source: "test", Payload: mustWorkerEvidencePayload(t)}); err != nil {
		t.Fatal(err)
	}
	if err := client.Update(ctx, child, domain.StatusInReview); err != nil {
		t.Fatal(err)
	}
	replayed, err := d.readMailboxEventsWithReviewReadyRecovery(ctx, req, repoDir, root)
	if err != nil {
		t.Fatal(err)
	}
	if len(replayed) != 2 || replayed[1].Seq != 2 {
		t.Fatalf("replayed = %+v, want one publication for each resubmission", replayed)
	}
	if afterCursor := filterMailEvents(replayed, 2, 0); len(afterCursor) != 1 || afterCursor[0].Seq != 2 {
		t.Fatalf("cursor replay = %+v, want only second resubmission", afterCursor)
	}
	watchReq := req
	watchReq.Body = mustMarshal(t, protocol.MailWatchCommandBody{RepoDir: repoDir, ParentIssue: root, SinceSeq: 2})
	watchResp, err := d.handleMailWatch(ctx, watchReq)
	if err != nil || !watchResp.OK {
		t.Fatalf("mail watch resp=%+v err=%v", watchResp, err)
	}
	var watched []protocol.MailEvent
	if err := json.Unmarshal(watchResp.Body, &watched); err != nil {
		t.Fatal(err)
	}
	if len(watched) != 1 || watched[0].Seq != 2 || watched[0].IssueID.String() != child {
		t.Fatalf("watched = %+v, want watch-only consumer to receive resubmission", watched)
	}
}

func TestLegacyMailboxProjectionPreservesTopLevelWorkerEvidence(t *testing.T) {
	ctx := context.Background()
	repoDir := t.TempDir()
	client := newMigratedIssueClientAtPath(t, filepath.Join(repoDir, "issues.db"), slog.Default())
	t.Cleanup(func() { _ = client.CloseDB() })
	root, err := client.Create(ctx, issues.CreateTaskParams{Title: "root", Type: domain.TypeEpic, Priority: domain.P1, Status: domain.StatusInProgress})
	if err != nil {
		t.Fatal(err)
	}
	child, err := client.Create(ctx, issues.CreateTaskParams{Title: "child", Type: domain.TypeBug, Priority: domain.P1, Status: domain.StatusInReview, ParentID: &root})
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(mustWorkerEvidencePayload(t))
	if err != nil {
		t.Fatal(err)
	}
	if err := appendMailboxEvent(repoDir, daemonMailEvent{
		Seq: 1, ParentIssue: root, IssueID: child, Type: "worker-integration-ready",
		From: "worker", Body: string(body), CreatedAt: time.Now().UTC(),
		Payload: map[string]interface{}{
			"publication":     reviewReadyReplayPublication,
			"publication_key": "worker-controlled-payload-must-not-skip",
		},
	}); err != nil {
		t.Fatal(err)
	}
	d := &Daemon{cfg: Config{RepoDir: repoDir, Logger: slog.Default()}, issueClientsByProject: map[string]*issues.Client{"project": client}}
	if err := d.ensureLegacyMailboxObservationProjection(ctx, "project", repoDir); err != nil {
		t.Fatal(err)
	}
	durableEvents, err := client.ListIssueReviewReadyObservationEvents(ctx, child)
	if err != nil {
		t.Fatal(err)
	}
	durableReplay := domain.ReduceReviewReadyEvidence(durableEvents).LatestEvidence
	if durableReplay == nil || !durableReplay.Validation.Complete || durableReplay.SourceEvent.SourceCommand != "mailbox.cutover" {
		t.Fatalf("durable replay = %+v, want cutover event with complete top-level evidence", durableReplay)
	}
	if _, ok := durableReplay.SourceEvent.Payload["mail_event"]; !ok {
		t.Fatalf("durable payload = %+v, want nested mailbox identity", durableReplay.SourceEvent.Payload)
	}
}

func TestMailSendProjectsStewardshipEventsWithoutProjectMailboxReads(t *testing.T) {
	ctx := context.Background()
	repoDir := t.TempDir()
	client := newMigratedIssueClientAtPath(t, filepath.Join(repoDir, "issues.db"), slog.Default())
	t.Cleanup(func() { _ = client.CloseDB() })
	root, err := client.Create(ctx, issues.CreateTaskParams{Title: "root", Type: domain.TypeEpic, Priority: domain.P1, Status: domain.StatusInProgress})
	if err != nil {
		t.Fatal(err)
	}
	child, err := client.Create(ctx, issues.CreateTaskParams{Title: "child", Type: domain.TypeBug, Priority: domain.P1, Status: domain.StatusInProgress, ParentID: &root})
	if err != nil {
		t.Fatal(err)
	}
	d := &Daemon{cfg: Config{RepoDir: repoDir, Logger: slog.Default()}, issueClientsByProject: map[string]*issues.Client{"project": client}}
	evidenceBody, err := json.Marshal(mustWorkerEvidencePayload(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range []protocol.MailSendCommandBody{
		{RepoDir: repoDir, ParentIssue: root, IssueID: naming.IssueID(child), Type: "worker-progress", From: "worker", To: "orchestrator", Body: "progress"},
		{RepoDir: repoDir, ParentIssue: root, IssueID: naming.IssueID(child), Type: "worker-blocked", From: "worker", To: "orchestrator", Body: "blocked"},
		{RepoDir: repoDir, ParentIssue: root, IssueID: naming.IssueID(child), Type: "worker-integration-ready", From: "worker", To: "orchestrator", Body: string(evidenceBody)},
	} {
		resp, handleErr := d.handleMailSend(ctx, protocol.RequestEnvelope{Meta: protocol.Metadata{ProjectID: "project"}, Body: mustMarshal(t, event)})
		if handleErr != nil || !resp.OK {
			t.Fatalf("mail send %s: resp=%+v err=%v", event.Type, resp, handleErr)
		}
	}
	for i := range 60 {
		if _, err := client.AppendIssueObservationEvent(ctx, child, issues.IssueObservationEventParams{
			Type: domain.IssueEventIssueDetailsChanged, ObservedAt: time.Now().UTC().Add(time.Duration(i) * time.Millisecond), Source: "noise", Payload: map[string]any{"index": i},
		}); err != nil {
			t.Fatal(err)
		}
	}

	rootedScope, err := domain.RootedOrchestrationScope(root)
	if err != nil {
		t.Fatal(err)
	}
	rooted, err := (daemonOrchestrationAuthority{daemon: d}).Snapshot(ctx, "project", protocol.OrchestrationSnapshotRequest{Scope: rootedScope, ActorID: "orchestrator", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	mailboxReads := 0
	d.taskGraphMailboxRead = func(string, string) ([]daemonMailEvent, error) {
		mailboxReads++
		return nil, fmt.Errorf("project scope must not read mailbox files")
	}
	project, err := (daemonOrchestrationAuthority{daemon: d}).Snapshot(ctx, "project", protocol.OrchestrationSnapshotRequest{Scope: domain.ProjectOrchestrationScope(), ActorID: "orchestrator", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if mailboxReads != 0 {
		t.Fatalf("project mailbox reads = %d, want 0", mailboxReads)
	}
	if len(rooted.RecentEvents) != 3 || len(project.RecentEvents) != 3 {
		t.Fatalf("rooted=%+v project=%+v, want three stewardship events despite noise", rooted.RecentEvents, project.RecentEvents)
	}
	for i := range rooted.RecentEvents {
		rootedEvent, projectEvent := rooted.RecentEvents[i], project.RecentEvents[i]
		rootedPayloadJSON, rootedErr := json.Marshal(rootedEvent.Payload)
		projectPayloadJSON, projectErr := json.Marshal(projectEvent.Payload)
		var rootedPayload, projectPayload any
		if rootedErr == nil {
			rootedErr = json.Unmarshal(rootedPayloadJSON, &rootedPayload)
		}
		if projectErr == nil {
			projectErr = json.Unmarshal(projectPayloadJSON, &projectPayload)
		}
		rootedEvent.Payload, projectEvent.Payload = nil, nil
		if rootedErr != nil || projectErr != nil || !reflect.DeepEqual(projectEvent, rootedEvent) || !reflect.DeepEqual(projectPayload, rootedPayload) {
			t.Fatalf("event %d mismatch:\nrooted=%+v\nproject=%+v", i, rooted.RecentEvents[i], project.RecentEvents[i])
		}
	}
}

func TestMailSendReconcilesSQLiteFirstCrashWithoutSequenceReuse(t *testing.T) {
	ctx := context.Background()
	repoDir := t.TempDir()
	client := newMigratedIssueClientAtPath(t, filepath.Join(repoDir, "issues.db"), slog.Default())
	t.Cleanup(func() { _ = client.CloseDB() })
	root, err := client.Create(ctx, issues.CreateTaskParams{Title: "root", Type: domain.TypeEpic, Priority: domain.P1, Status: domain.StatusInProgress})
	if err != nil {
		t.Fatal(err)
	}
	child, err := client.Create(ctx, issues.CreateTaskParams{Title: "child", Type: domain.TypeBug, Priority: domain.P1, Status: domain.StatusInProgress, ParentID: &root})
	if err != nil {
		t.Fatal(err)
	}
	spoofed := daemonMailEvent{
		Seq: 1, ParentIssue: root, IssueID: child, Type: "worker-progress", From: "spoof", To: "orchestrator",
		Body: "must not replay", CreatedAt: time.Now().UTC(),
	}
	for _, sourceCommand := range []string{"manual.spoof", "mail.send"} {
		if _, err := client.AppendIssueObservationEvent(ctx, child, issues.IssueObservationEventParams{
			Type: domain.IssueObservationEventType(spoofed.Type), ObservedAt: spoofed.CreatedAt,
			Source: "spoof", SourceCommand: sourceCommand, Payload: projectedMailObservationPayload(spoofed),
		}); err != nil {
			t.Fatal(err)
		}
	}
	d := &Daemon{cfg: Config{RepoDir: repoDir, Logger: slog.Default()}, issueClientsByProject: map[string]*issues.Client{"project": client}}
	send := func(requestID, body string) protocol.ResponseEnvelope {
		t.Helper()
		resp, handleErr := d.handleMailSend(ctx, protocol.RequestEnvelope{
			RequestID: naming.RequestID(requestID),
			Meta:      protocol.Metadata{ProjectID: "project"},
			Body: mustMarshal(t, protocol.MailSendCommandBody{
				RepoDir: repoDir, ParentIssue: root, IssueID: naming.IssueID(child), Type: "worker-progress",
				From: "worker", To: "orchestrator", Body: body,
			}),
		})
		if handleErr != nil {
			t.Fatal(handleErr)
		}
		return resp
	}

	d.mailProjectedBeforeAppend = func(context.Context, daemonMailEvent) error { return errors.New("injected crash after SQLite commit") }
	if resp := send("mail-crash-1", "first"); resp.OK || !strings.Contains(resp.Error.Message, "injected crash") {
		t.Fatalf("first send = %+v, want injected SQLite-first crash", resp)
	}
	if events, err := readMailboxEvents(repoDir, root); err != nil || len(events) != 0 {
		t.Fatalf("mailbox after crash = %+v err=%v, want no JSONL append", events, err)
	}

	d.mailProjectedBeforeAppend = nil
	if resp := send("mail-crash-1", "first"); !resp.OK {
		t.Fatalf("same delivery retry = %+v, want durable outbox replay", resp)
	}
	if events, err := readMailboxEvents(repoDir, root); err != nil || len(events) != 1 || events[0].Seq != 1 || events[0].Body != "first" {
		t.Fatalf("mailbox after retry = %+v err=%v, want original sequence 1", events, err)
	}
	if resp := send("mail-crash-1", "changed payload"); resp.OK || resp.Error == nil || !strings.Contains(resp.Error.Message, "different message") {
		t.Fatalf("request ID payload mismatch = %+v, want canonical identity rejection", resp)
	}

	d.mailProjectedBeforeAppend = func(context.Context, daemonMailEvent) error { return errors.New("injected second crash") }
	if resp := send("mail-crash-2", "second"); resp.OK {
		t.Fatalf("second crash send = %+v, want failure", resp)
	}
	d.mailProjectedBeforeAppend = nil
	if resp := send("mail-after-crash", "third"); !resp.OK {
		t.Fatalf("distinct send after crash = %+v, want reconciliation", resp)
	}
	events, err := readMailboxEvents(repoDir, root)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 3 {
		t.Fatalf("mailbox = %+v, want three durable deliveries", events)
	}
	for i, wantBody := range []string{"first", "second", "third"} {
		if events[i].Seq != int64(i+1) || events[i].Body != wantBody {
			t.Fatalf("mailbox[%d] = %+v, want seq=%d body=%q", i, events[i], i+1, wantBody)
		}
	}
	projected, err := client.ListIssueObservationMailEvents(ctx, root)
	if err != nil || len(projected) != 3 {
		t.Fatalf("projected outbox = %+v err=%v, want one row per logical delivery", projected, err)
	}
}

func TestMailboxReplayTreatsParserDerivedPayloadDriftAsEquivalent(t *testing.T) {
	ctx := context.Background()
	repoDir := t.TempDir()
	client := newMigratedIssueClientAtPath(t, filepath.Join(repoDir, "issues.db"), slog.Default())
	t.Cleanup(func() { _ = client.CloseDB() })
	root, err := client.Create(ctx, issues.CreateTaskParams{Title: "root", Type: domain.TypeEpic, Priority: domain.P1, Status: domain.StatusInProgress})
	if err != nil {
		t.Fatal(err)
	}
	child, err := client.Create(ctx, issues.CreateTaskParams{Title: "child", Type: domain.TypeBug, Priority: domain.P1, Status: domain.StatusInReview, ParentID: &root})
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(mustWorkerEvidencePayload(t))
	if err != nil {
		t.Fatal(err)
	}
	createdAt := time.Date(2026, time.July, 15, 7, 24, 25, 711794000, time.UTC)
	filesystemEvent := daemonMailEvent{
		Seq: 8, ParentIssue: root, IssueID: child, Type: "worker-integration-ready",
		From: reviewReadyReplaySender, Body: string(body), CreatedAt: createdAt,
		Payload: map[string]interface{}{
			"publication":     reviewReadyReplayPublication,
			"publication_key": "project:7526",
			"source_event_id": float64(7526),
		},
	}
	// Migration 0052 persisted an older envelope shape before parser-derived
	// worker evidence was added to protocol payloads. The immutable top-level
	// observation still carries the producer-authored publication identity.
	durablePayload := map[string]any{
		"publication":     reviewReadyReplayPublication,
		"publication_key": "project:7526",
		"source_event_id": float64(7526),
		"worker_evidence": mustWorkerEvidencePayload(t),
		"worker_evidence_validation": map[string]any{
			"found": true, "complete": true, "storage": "issue_event_payload_json_v1",
		},
	}
	durableEnvelope := mailEventToProtocol(filesystemEvent)
	durableEnvelope.Payload = nil
	durablePayload["mail_event"] = durableEnvelope
	if _, err := client.AppendIssueObservationEvent(ctx, child, issues.IssueObservationEventParams{
		Type: domain.IssueObservationEventType("worker-integration-ready"), ObservedAt: createdAt,
		Source: reviewReadyReplaySender, SourceCommand: "mailbox.cutover", WorktreePath: repoDir,
		Payload: durablePayload,
	}); err != nil {
		t.Fatal(err)
	}
	if err := appendMailboxEvent(repoDir, filesystemEvent); err != nil {
		t.Fatal(err)
	}

	d := &Daemon{cfg: Config{RepoDir: repoDir, Logger: slog.Default()}, issueClientsByProject: map[string]*issues.Client{"project": client}}
	existing, err := readMailboxEvents(repoDir, root)
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := d.reconcileProjectedMailboxEvents(ctx, "project", repoDir, root, existing)
	if err != nil {
		t.Fatalf("equivalent parser-derived payload drift blocked replay: %v", err)
	}
	if !reflect.DeepEqual(replayed, existing) {
		t.Fatalf("replayed = %+v, want immutable filesystem event unchanged", replayed)
	}
}

func TestMailboxReplayConflictDoesNotBlockAnotherProjectMailWrite(t *testing.T) {
	ctx := context.Background()
	conflictedRepo := t.TempDir()
	conflicted := newMigratedIssueClientAtPath(t, filepath.Join(conflictedRepo, "issues.db"), slog.Default())
	t.Cleanup(func() { _ = conflicted.CloseDB() })
	conflictedRoot, err := conflicted.Create(ctx, issues.CreateTaskParams{Title: "conflicted root", Type: domain.TypeEpic, Status: domain.StatusInProgress})
	if err != nil {
		t.Fatal(err)
	}
	conflictedChild, err := conflicted.Create(ctx, issues.CreateTaskParams{Title: "conflicted child", Type: domain.TypeTask, Status: domain.StatusInProgress, ParentID: &conflictedRoot})
	if err != nil {
		t.Fatal(err)
	}
	createdAt := time.Date(2026, time.July, 15, 7, 24, 25, 0, time.UTC)
	durable := daemonMailEvent{Seq: 1, ParentIssue: conflictedRoot, IssueID: conflictedChild, Type: "worker-progress", Body: "durable", CreatedAt: createdAt}
	if _, err := conflicted.AppendIssueObservationEvent(ctx, conflictedChild, issues.IssueObservationEventParams{
		Type: "worker-progress", ObservedAt: createdAt, SourceCommand: "mailbox.cutover", Payload: projectedMailObservationPayload(durable),
	}); err != nil {
		t.Fatal(err)
	}
	filesystem := durable
	filesystem.Body = "different producer message"
	if err := appendMailboxEvent(conflictedRepo, filesystem); err != nil {
		t.Fatal(err)
	}

	healthyRepo := t.TempDir()
	healthy := newMigratedIssueClientAtPath(t, filepath.Join(healthyRepo, "issues.db"), slog.Default())
	t.Cleanup(func() { _ = healthy.CloseDB() })
	healthyRoot, err := healthy.Create(ctx, issues.CreateTaskParams{Title: "healthy root", Type: domain.TypeEpic, Status: domain.StatusInProgress})
	if err != nil {
		t.Fatal(err)
	}
	healthyChild, err := healthy.Create(ctx, issues.CreateTaskParams{Title: "healthy child", Type: domain.TypeTask, Status: domain.StatusInProgress, ParentID: &healthyRoot})
	if err != nil {
		t.Fatal(err)
	}
	d := &Daemon{
		cfg: Config{RepoDir: healthyRepo, Logger: slog.Default()},
		issueClientsByProject: map[string]*issues.Client{
			"conflicted": conflicted,
			"healthy":    healthy,
		},
	}
	existing, err := readMailboxEvents(conflictedRepo, conflictedRoot)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.reconcileProjectedMailboxEvents(ctx, "conflicted", conflictedRepo, conflictedRoot, existing); err == nil || !strings.Contains(err.Error(), "conflicts with durable observation") {
		t.Fatalf("conflicted replay error = %v, want exact project-local diagnosis", err)
	}

	resp, err := d.handleMailSend(ctx, protocol.RequestEnvelope{
		RequestID: "healthy-delivery",
		Meta:      protocol.Metadata{ProjectID: "healthy"},
		Body: mustMarshal(t, protocol.MailSendCommandBody{
			RepoDir: healthyRepo, ParentIssue: healthyRoot, IssueID: naming.IssueID(healthyChild), Type: "worker-progress", Body: "healthy",
		}),
	})
	if err != nil || !resp.OK {
		t.Fatalf("healthy project mail send = %+v err=%v, want unrelated write availability", resp, err)
	}
	if err := healthy.Update(ctx, healthyChild, domain.StatusInReview); err != nil {
		t.Fatalf("healthy project issue update blocked by other project conflict: %v", err)
	}
}

func TestValidateCanonicalMailOutboxObservationRejectsSpoofedIdentity(t *testing.T) {
	createdAt := time.Date(2026, 7, 15, 1, 2, 3, 0, time.UTC)
	canonical := domain.IssueObservationEvent{
		ID: 9, IssueID: naming.IssueID("child"), Type: "worker-progress", ObservedAt: createdAt,
		SourceCommand: "mail.send", Payload: map[string]any{"mail_delivery_id": "request-9"},
	}
	base := daemonMailEvent{Seq: 1, ParentIssue: "root", IssueID: "child", Type: "worker-progress", Body: "progress", CreatedAt: createdAt}
	for _, test := range []struct {
		name  string
		event daemonMailEvent
		want  string
	}{
		{name: "issue", event: func() daemonMailEvent { event := base; event.IssueID = "other"; return event }(), want: "does not match observation issue"},
		{name: "type", event: func() daemonMailEvent { event := base; event.Type = "worker-blocked"; return event }(), want: "does not match canonical observation type"},
		{name: "time", event: func() daemonMailEvent { event := base; event.CreatedAt = createdAt.Add(time.Second); return event }(), want: "does not match observation time"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := validateCanonicalMailOutboxObservation(canonical, test.event); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("validation error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestProjectSnapshotBackfillsPreUpgradeMailboxOnceWithRootedShape(t *testing.T) {
	ctx := context.Background()
	repoDir := t.TempDir()
	client := newMigratedIssueClientAtPath(t, filepath.Join(repoDir, "issues.db"), slog.Default())
	t.Cleanup(func() { _ = client.CloseDB() })
	root, err := client.Create(ctx, issues.CreateTaskParams{Title: "root", Type: domain.TypeEpic, Priority: domain.P1, Status: domain.StatusInProgress})
	if err != nil {
		t.Fatal(err)
	}
	child, err := client.Create(ctx, issues.CreateTaskParams{Title: "child", Type: domain.TypeBug, Priority: domain.P1, Status: domain.StatusInProgress, ParentID: &root})
	if err != nil {
		t.Fatal(err)
	}
	created := time.Date(2026, 7, 14, 3, 0, 0, 0, time.UTC)
	legacy := []daemonMailEvent{
		{Seq: 1, ParentIssue: root, IssueID: child, Type: "worker-progress", From: "worker", To: "orchestrator", Body: "legacy progress", CreatedAt: created},
		{Seq: 2, ParentIssue: root, IssueID: child, Type: "worker-blocked", From: "worker", To: "orchestrator", Body: "legacy blocker", CreatedAt: created.Add(time.Second), Payload: map[string]any{"reason": "dependency"}},
	}
	for _, event := range legacy {
		if err := appendMailboxEvent(repoDir, event); err != nil {
			t.Fatal(err)
		}
	}
	d := &Daemon{cfg: Config{RepoDir: repoDir, Logger: slog.Default()}, issueClientsByProject: map[string]*issues.Client{"project": client}}
	rootedScope, err := domain.RootedOrchestrationScope(root)
	if err != nil {
		t.Fatal(err)
	}
	rooted, err := (daemonOrchestrationAuthority{daemon: d}).Snapshot(ctx, "project", protocol.OrchestrationSnapshotRequest{Scope: rootedScope, ActorID: "orchestrator", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	project, err := (daemonOrchestrationAuthority{daemon: d}).Snapshot(ctx, "project", protocol.OrchestrationSnapshotRequest{Scope: domain.ProjectOrchestrationScope(), ActorID: "orchestrator", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(project.RecentEvents, rooted.RecentEvents) || len(project.RecentEvents) != 2 {
		t.Fatalf("rooted=%+v project=%+v, want exact pre-upgrade event shape and ordering", rooted.RecentEvents, project.RecentEvents)
	}

	mailboxDir := filepath.Join(repoDir, ".azedarach", "mailbox")
	if err := os.RemoveAll(mailboxDir); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(mailboxDir, []byte("cutover complete; reads must not recur"), 0o600); err != nil {
		t.Fatal(err)
	}
	restarted := &Daemon{cfg: Config{RepoDir: repoDir, Logger: slog.Default()}, issueClientsByProject: map[string]*issues.Client{"project": client}}
	afterRestart, err := (daemonOrchestrationAuthority{daemon: restarted}).Snapshot(ctx, "project", protocol.OrchestrationSnapshotRequest{Scope: domain.ProjectOrchestrationScope(), ActorID: "orchestrator", Limit: 10})
	if err != nil {
		t.Fatalf("completed cutover re-read filesystem mailbox: %v", err)
	}
	if !reflect.DeepEqual(afterRestart.RecentEvents, project.RecentEvents) {
		t.Fatalf("after restart=%+v project=%+v, want stable no-duplicate durable events", afterRestart.RecentEvents, project.RecentEvents)
	}
	events, err := client.ListIssueObservationEvents(ctx, child, issues.IssueObservationEventListOptions{Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	stewardshipCount := 0
	for _, event := range events {
		if _, visible := projectStewardshipEventType(event.Type); visible {
			stewardshipCount++
		}
	}
	if stewardshipCount != 2 {
		t.Fatalf("durable events = %+v, want exactly two stewardship rows without retry duplicates", events)
	}
}

func TestLegacyMailboxProjectionRefusesOversizedFileWithoutCompleting(t *testing.T) {
	repoDir := t.TempDir()
	mailboxDir := filepath.Join(repoDir, ".azedarach", "mailbox")
	if err := os.MkdirAll(mailboxDir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(mailboxDir, "root.jsonl")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(legacyMailboxCutoverMaxFileBytes + 1); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := readLegacyMailboxObservationProjection(repoDir); err == nil || !strings.Contains(err.Error(), "bounded file limit") {
		t.Fatalf("oversized legacy mailbox error = %v, want bounded refusal", err)
	}
}

func TestMailWatchRepairsInterruptedAppendBeforeReviewReadyReplay(t *testing.T) {
	ctx := context.Background()
	repoDir := t.TempDir()
	client := newMigratedIssueClientAtPath(t, filepath.Join(repoDir, "issues.db"), slog.Default())
	t.Cleanup(func() { _ = client.CloseDB() })
	root, err := client.Create(ctx, issues.CreateTaskParams{Title: "root", Type: domain.TypeEpic, Priority: domain.P1, Status: domain.StatusInProgress})
	if err != nil {
		t.Fatal(err)
	}
	child, err := client.Create(ctx, issues.CreateTaskParams{Title: "child", Type: domain.TypeBug, Priority: domain.P1, Status: domain.StatusInProgress, ParentID: &root})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.AppendIssueObservationEvent(ctx, child, issues.IssueObservationEventParams{Type: domain.IssueEventEvidenceSubmitted, Source: "test", Payload: mustWorkerEvidencePayload(t)}); err != nil {
		t.Fatal(err)
	}
	if err := client.Update(ctx, child, domain.StatusInReview); err != nil {
		t.Fatal(err)
	}
	path := mailboxPath(repoDir, root)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"seq":1,"parent_issue":"`+root+`"`), 0o644); err != nil {
		t.Fatal(err)
	}
	d := &Daemon{cfg: Config{RepoDir: repoDir, Logger: slog.Default()}, issueClientsByProject: map[string]*issues.Client{"project": client}}
	events, err := d.readMailboxEventsWithReviewReadyRecovery(ctx, protocol.RequestEnvelope{Meta: protocol.Metadata{ProjectID: "project"}}, repoDir, root)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Seq != 1 || events[0].IssueID != child {
		t.Fatalf("events = %+v, want durable observation replay after fragment repair", events)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasSuffix(data, []byte("\n")) {
		t.Fatalf("mailbox = %q, want committed newline", data)
	}
}

func TestMailWatchRecoveryFailureReturnsDurableMailboxEvents(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	repoDir := t.TempDir()
	client := newMigratedIssueClientAtPath(t, filepath.Join(repoDir, "issues.db"), slog.Default())
	t.Cleanup(func() { _ = client.CloseDB() })
	root, err := client.Create(ctx, issues.CreateTaskParams{Title: "root", Type: domain.TypeEpic, Priority: domain.P1, Status: domain.StatusInProgress})
	if err != nil {
		t.Fatal(err)
	}
	if err := appendMailboxEvent(repoDir, daemonMailEvent{Seq: 1, ParentIssue: root, IssueID: "child", Type: "worker-progress", Body: "durable", CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	cancel()
	d := &Daemon{cfg: Config{RepoDir: repoDir, Logger: slog.Default()}, issueClientsByProject: map[string]*issues.Client{"project": client}}
	events, err := d.readMailboxEventsWithReviewReadyRecovery(ctx, protocol.RequestEnvelope{Meta: protocol.Metadata{ProjectID: "project"}}, repoDir, root)
	if err != nil {
		t.Fatalf("recovery failure must not fail mailbox read: %v", err)
	}
	if len(events) != 1 || events[0].Seq != 1 || events[0].Body != "durable" {
		t.Fatalf("events = %+v, want already-durable mailbox event", events)
	}
}

func TestMailConcurrentSendDoesNotWaitForReviewReadyReplayLoad(t *testing.T) {
	ctx := context.Background()
	repoDir := t.TempDir()
	client := newMigratedIssueClientAtPath(t, filepath.Join(repoDir, "issues.db"), slog.Default())
	t.Cleanup(func() { _ = client.CloseDB() })
	root, err := client.Create(ctx, issues.CreateTaskParams{Title: "root", Type: domain.TypeEpic, Priority: domain.P1, Status: domain.StatusInProgress})
	if err != nil {
		t.Fatal(err)
	}
	loadStarted := make(chan struct{})
	releaseLoad := make(chan struct{})
	var once, releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(releaseLoad) })
	d := &Daemon{
		cfg:                   Config{RepoDir: repoDir, Logger: slog.Default()},
		issueClientsByProject: map[string]*issues.Client{"project": client},
		reviewReadyRecoveryBeforeLoad: func() {
			once.Do(func() { close(loadStarted) })
			<-releaseLoad
		},
	}
	readDone := make(chan error, 1)
	go func() {
		_, readErr := d.readMailboxEventsWithReviewReadyRecovery(ctx, protocol.RequestEnvelope{Meta: protocol.Metadata{ProjectID: "project"}}, repoDir, root)
		readDone <- readErr
	}()
	select {
	case <-loadStarted:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for review-ready replay load")
	}

	sendBody := mustMarshal(t, protocol.MailSendCommandBody{
		RepoDir: repoDir, ParentIssue: root, IssueID: "child", Type: "worker-progress", Body: "concurrent",
	})
	sendDone := make(chan protocol.ResponseEnvelope, 1)
	go func() {
		resp, _ := d.handleMailSend(ctx, protocol.RequestEnvelope{Meta: protocol.Metadata{ProjectID: "project"}, Body: sendBody})
		sendDone <- resp
	}()
	select {
	case resp := <-sendDone:
		if !resp.OK {
			t.Fatalf("concurrent send failed: %+v", resp.Error)
		}
	case <-time.After(time.Second):
		t.Fatal("concurrent mail.send blocked on slow observation replay")
	}
	releaseOnce.Do(func() { close(releaseLoad) })
	if err := <-readDone; err != nil {
		t.Fatal(err)
	}
	events, err := readMailboxEvents(repoDir, root)
	if err != nil || len(events) != 1 || events[0].Body != "concurrent" {
		t.Fatalf("events = %+v err=%v, want concurrent send retained", events, err)
	}
}

func TestMailWatchGenericCommandLogsAtDebugOnSuccess(t *testing.T) {
	repoDir := t.TempDir()
	body := mustMarshal(t, protocol.MailWatchCommandBody{
		RepoDir:     repoDir,
		ParentIssue: "az-parent",
	})
	req := protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-watch",
		Kind:            protocol.EnvelopeKindCommand,
		Meta:            protocol.Metadata{ProjectID: "proj-mail"},
		Command:         protocol.CommandMailWatch,
		Body:            body,
	}

	var infoLogs bytes.Buffer
	infoDaemon := New(Config{
		RepoDir: repoDir,
		Logger:  slog.New(slog.NewTextHandler(&infoLogs, &slog.HandlerOptions{Level: slog.LevelInfo})),
	})
	resp, err := infoDaemon.command(context.Background(), req)
	if err != nil {
		t.Fatalf("mail.watch command error: %v", err)
	}
	if !resp.OK {
		t.Fatalf("mail.watch response error: %+v", resp.Error)
	}
	if strings.Contains(infoLogs.String(), "daemon command received") || strings.Contains(infoLogs.String(), "daemon command completed") {
		t.Fatalf("mail.watch success should not emit generic info command logs, got %q", infoLogs.String())
	}

	var debugLogs bytes.Buffer
	debugDaemon := New(Config{
		RepoDir: repoDir,
		Logger:  slog.New(slog.NewTextHandler(&debugLogs, &slog.HandlerOptions{Level: slog.LevelDebug})),
	})
	resp, err = debugDaemon.command(context.Background(), req)
	if err != nil {
		t.Fatalf("debug mail.watch command error: %v", err)
	}
	if !resp.OK {
		t.Fatalf("debug mail.watch response error: %+v", resp.Error)
	}
	output := debugLogs.String()
	if !strings.Contains(output, "daemon command received") || !strings.Contains(output, "daemon command completed") {
		t.Fatalf("mail.watch success should emit generic command logs at debug, got %q", output)
	}
}

func TestMailWatchFailureStillWarnsAtInfo(t *testing.T) {
	repoDir := t.TempDir()
	var logs bytes.Buffer
	d := New(Config{
		RepoDir: repoDir,
		Logger:  slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelInfo})),
	})
	resp, err := d.command(context.Background(), protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-watch-invalid",
		Kind:            protocol.EnvelopeKindCommand,
		Meta:            protocol.Metadata{ProjectID: "proj-mail"},
		Command:         protocol.CommandMailWatch,
		Body:            mustMarshal(t, protocol.MailWatchCommandBody{RepoDir: repoDir}),
	})
	if err != nil {
		t.Fatalf("mail.watch command error: %v", err)
	}
	if resp.Error == nil {
		t.Fatalf("expected mail.watch error response, got ok=%v", resp.OK)
	}
	output := logs.String()
	if strings.Contains(output, "daemon command received") {
		t.Fatalf("mail.watch failure should not emit generic received info log, got %q", output)
	}
	if !strings.Contains(output, "daemon command failed") {
		t.Fatalf("mail.watch failure should still emit warn log, got %q", output)
	}
}

func TestMailListAnnotatesStructuredWorkerEvidencePayload(t *testing.T) {
	repoDir := t.TempDir()
	d := New(Config{RepoDir: repoDir, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	sendReq := protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-send",
		Kind:            protocol.EnvelopeKindCommand,
		Meta:            protocol.Metadata{ProjectID: "proj-mail"},
		Command:         protocol.CommandMailSend,
		Body: mustMarshal(t, protocol.MailSendCommandBody{
			RepoDir:     repoDir,
			ParentIssue: "az-parent",
			IssueID:     naming.IssueID("az-child"),
			Type:        "worker-integration-ready",
			Body: `{
				"schema": "worker_evidence.v1",
				"summary": "Ready for integration.",
				"commands_run": ["go test ./internal/daemon"],
				"key_assertions": ["mailbox payload exposes parsed evidence"],
				"files_changed": ["internal/daemon/mail_commands.go"],
				"review": {"status": "clean", "findings": []},
				"risks": ["none"]
			}`,
		}),
	}
	resp, err := d.command(context.Background(), sendReq)
	if err != nil {
		t.Fatalf("mail.send command error: %v", err)
	}
	if !resp.OK {
		t.Fatalf("mail.send response error: %+v", resp.Error)
	}

	listReq := protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-list",
		Kind:            protocol.EnvelopeKindCommand,
		Meta:            protocol.Metadata{ProjectID: "proj-mail"},
		Command:         protocol.CommandMailList,
		Body: mustMarshal(t, protocol.MailListCommandBody{
			RepoDir:     repoDir,
			ParentIssue: "az-parent",
		}),
	}
	resp, err = d.command(context.Background(), listReq)
	if err != nil {
		t.Fatalf("mail.list command error: %v", err)
	}
	if !resp.OK {
		t.Fatalf("mail.list response error: %+v", resp.Error)
	}
	var events []protocol.MailEvent
	if err := json.Unmarshal(resp.Body, &events); err != nil {
		t.Fatalf("decode mail.list body: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("events = %+v, want one", events)
	}
	validation, ok := events[0].Payload["worker_evidence_validation"].(map[string]interface{})
	if !ok {
		t.Fatalf("payload = %+v, want worker_evidence_validation", events[0].Payload)
	}
	if validation["complete"] != true || validation["storage"] != "mailbox_body_json_v1" {
		t.Fatalf("validation = %+v", validation)
	}
	if _, ok := events[0].Payload["worker_evidence"].(map[string]interface{}); !ok {
		t.Fatalf("payload = %+v, want parsed worker_evidence", events[0].Payload)
	}
}

func TestMailSendAdvancesOrchestrationProjectionRevision(t *testing.T) {
	repoDir := t.TempDir()
	const projectID = "proj-mail-revision"
	d := New(Config{RepoDir: repoDir, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	resp, err := d.handleMailSend(context.Background(), protocol.RequestEnvelope{
		Meta: protocol.Metadata{ProjectID: naming.ProjectID(projectID)},
		Body: mustMarshal(t, protocol.MailSendCommandBody{
			RepoDir:     repoDir,
			ParentIssue: "az-parent",
			IssueID:     naming.IssueID("az-child"),
			Type:        "worker-progress",
			Body:        "progress",
		}),
	})
	if err != nil || !resp.OK {
		t.Fatalf("mail send response=%+v err=%v", resp, err)
	}
	if resp.Revision == 0 || d.currentRevision(projectID) != resp.Revision {
		t.Fatalf("mail revision response=%d current=%d", resp.Revision, d.currentRevision(projectID))
	}
}

func TestMailSendRejectsInvalidWorkerEvidenceBeforePersisting(t *testing.T) {
	repoDir := t.TempDir()
	d := New(Config{RepoDir: repoDir, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	req := protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-send-invalid-evidence",
		Kind:            protocol.EnvelopeKindCommand,
		Meta:            protocol.Metadata{ProjectID: "proj-mail"},
		Command:         protocol.CommandMailSend,
		Body: mustMarshal(t, protocol.MailSendCommandBody{
			RepoDir:     repoDir,
			ParentIssue: "az-parent",
			IssueID:     naming.IssueID("az-child"),
			Type:        "worker-integration-ready",
			Body: `{
				"schema": "worker_evidence.v1",
				"summary": "Ready for integration.",
				"commands_run": ["go test ./internal/daemon"],
				"key_assertions": ["mailbox rejects invalid evidence"],
				"files_changed": ["internal/daemon/mail_commands.go"],
				"review": {"status": "clean", "findings": []},
				"risks": ["none"],
				"artifact_links": ["https://example.test/run/1"]
			}`,
		}),
	}

	resp, err := d.command(context.Background(), req)
	if err != nil {
		t.Fatalf("mail.send command error: %v", err)
	}
	if resp.OK || resp.Error == nil || resp.Error.Code != protocol.ErrorCodeInvalidRequest {
		t.Fatalf("response = %+v, want invalid_request", resp)
	}
	for _, want := range []string{"invalid worker_evidence.v1 packet", "artifact_links[0] must be an object", "Omit artifact_links unless links are needed", "not a string array"} {
		if !strings.Contains(resp.Error.Message, want) {
			t.Fatalf("error = %q, missing %q", resp.Error.Message, want)
		}
	}
	events, err := readMailboxEvents(repoDir, "az-parent")
	if err != nil {
		t.Fatalf("readMailboxEvents error: %v", err)
	}
	if len(events) != 0 {
		t.Fatalf("events = %+v, want none persisted", events)
	}
}

func TestMailSendSerializesSequenceNumbers(t *testing.T) {
	repoDir := t.TempDir()
	d := New(Config{
		RepoDir: repoDir,
		Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	parent := "az-parent"
	start := make(chan struct{})
	errs := make(chan error, 2)
	send := func(issue string) {
		<-start
		_, err := d.command(context.Background(), protocol.RequestEnvelope{
			ProtocolVersion: protocol.CurrentVersion,
			RequestID:       naming.RequestID(fmt.Sprintf("req-%s", issue)),
			Kind:            protocol.EnvelopeKindCommand,
			Meta:            protocol.Metadata{ProjectID: "proj-mail"},
			Command:         protocol.CommandMailSend,
			Body: mustMarshal(t, protocol.MailSendCommandBody{
				RepoDir:     repoDir,
				ParentIssue: parent,
				IssueID:     naming.IssueID(issue),
				Type:        "handoff",
				Body:        issue,
			}),
		})
		errs <- err
	}

	go send("az-1")
	go send("az-2")
	close(start)
	for i := 0; i < 2; i++ {
		if err := <-errs; err != nil {
			t.Fatalf("mail.send command error: %v", err)
		}
	}

	events, err := readMailboxEvents(repoDir, parent)
	if err != nil {
		t.Fatalf("readMailboxEvents error: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("events len = %d, want 2", len(events))
	}
	if events[0].Seq != 1 || events[1].Seq != 2 {
		t.Fatalf("seqs = [%d,%d], want [1,2]", events[0].Seq, events[1].Seq)
	}
}

func mustMarshal(t *testing.T, v any) []byte {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return data
}
