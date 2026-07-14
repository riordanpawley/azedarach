package daemon

import (
	"bytes"
	"context"
	"encoding/json"
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
