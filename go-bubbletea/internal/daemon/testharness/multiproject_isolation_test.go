package testharness

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	daemonclient "github.com/riordanpawley/azedarach/internal/client/daemonclient"
	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	daemonpublish "github.com/riordanpawley/azedarach/internal/daemon/publish"
	daemonstate "github.com/riordanpawley/azedarach/internal/daemon/state"
)

const (
	multiprojectUpsertCommand   = "project.session.upsert"
	multiprojectSnapshotCommand = "project.snapshot"
)

type multiprojectDaemon struct {
	harness             *Harness
	store               *daemonstate.Store
	hub                 *daemonpublish.Hub
	fallbackProjectID   string
	fallbackActivatedMu sync.Mutex
	fallbackActivated   map[string]bool
}

type multiprojectSessionRequest struct {
	SessionID string                   `json:"session_id"`
	IssueID   string                   `json:"issue_id"`
	State     daemonstate.SessionState `json:"state"`
}

func newMultiprojectDaemon(h *Harness) *multiprojectDaemon {
	return &multiprojectDaemon{
		harness: h,
		store:   daemonstate.NewStore(),
		hub:     daemonpublish.NewHub(16, 4, nil),
	}
}

func (d *multiprojectDaemon) Handshake(ctx context.Context, hello protocol.Hello) (protocol.HelloAck, error) {
	_ = ctx
	return protocol.NegotiateHello(hello, "daemon-test"), nil
}

func (d *multiprojectDaemon) Command(ctx context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
	_ = ctx

	if req.Meta.ProjectID == "" {
		return protocol.ResponseEnvelope{}, fmt.Errorf("missing project_id in request metadata")
	}

	switch req.Command {
	case multiprojectUpsertCommand:
		if d.fallbackProjectID != "" && req.Meta.ProjectID == d.fallbackProjectID {
			d.fallbackActivatedMu.Lock()
			activated := d.fallbackActivated[req.Meta.ProjectID]
			if d.fallbackActivated == nil {
				d.fallbackActivated = map[string]bool{}
			}
			if !activated {
				d.fallbackActivated[req.Meta.ProjectID] = true
				d.fallbackActivatedMu.Unlock()
				if err := d.harness.appendEvent("daemon.multiproject.fallback.activated", map[string]any{
					"project_id": req.Meta.ProjectID,
					"command":    req.Command,
				}); err != nil {
					return protocol.ResponseEnvelope{}, err
				}
				return protocol.ResponseEnvelope{}, fmt.Errorf("project %s switched to fallback", req.Meta.ProjectID)
			}
			d.fallbackActivatedMu.Unlock()
		}

		var cmd multiprojectSessionRequest
		if err := json.Unmarshal(req.Body, &cmd); err != nil {
			return protocol.ResponseEnvelope{}, fmt.Errorf("decode upsert request: %w", err)
		}

		evt, err := d.store.UpsertSession(req.Meta.ProjectID, cmd.SessionID, cmd.IssueID, cmd.State)
		if err != nil {
			return protocol.ResponseEnvelope{}, err
		}

		sessionBody, err := json.Marshal(evt.Session)
		if err != nil {
			return protocol.ResponseEnvelope{}, fmt.Errorf("marshal session event: %w", err)
		}
		d.hub.Publish(protocol.EventEnvelope{
			ProtocolVersion: req.ProtocolVersion,
			ProjectID:       req.Meta.ProjectID,
			Meta:            req.Meta,
			Revision:        evt.Revision,
			Event:           evt.Type,
			Kind:            protocol.EnvelopeKindEvent,
			EmittedAt:       time.Now().UTC(),
			Body:            sessionBody,
		})

		snapshot := d.store.ReadSnapshot(req.Meta.ProjectID)
		body, err := json.Marshal(snapshot)
		if err != nil {
			return protocol.ResponseEnvelope{}, fmt.Errorf("marshal snapshot response: %w", err)
		}

		if err := d.harness.appendEvent("daemon.multiproject.command", map[string]any{
			"command":    req.Command,
			"project_id": req.Meta.ProjectID,
			"session_id": cmd.SessionID,
			"revision":   snapshot.Revision,
		}); err != nil {
			return protocol.ResponseEnvelope{}, err
		}

		return protocol.ResponseEnvelope{
			ProtocolVersion: req.ProtocolVersion,
			RequestID:       req.RequestID,
			Kind:            protocol.EnvelopeKindResponse,
			Meta:            req.Meta,
			Revision:        snapshot.Revision,
			CompletedAt:     time.Now().UTC(),
			OK:              true,
			Body:            body,
		}, nil

	case multiprojectSnapshotCommand:
		snapshot := d.store.ReadSnapshot(req.Meta.ProjectID)
		body, err := json.Marshal(snapshot)
		if err != nil {
			return protocol.ResponseEnvelope{}, fmt.Errorf("marshal snapshot response: %w", err)
		}
		return protocol.ResponseEnvelope{
			ProtocolVersion: req.ProtocolVersion,
			RequestID:       req.RequestID,
			Kind:            protocol.EnvelopeKindResponse,
			Meta:            req.Meta,
			Revision:        snapshot.Revision,
			CompletedAt:     time.Now().UTC(),
			OK:              true,
			Body:            body,
		}, nil

	default:
		return protocol.ResponseEnvelope{}, fmt.Errorf("unsupported command: %s", req.Command)
	}
}

func (d *multiprojectDaemon) Subscribe(ctx context.Context, projectID string, fromRevision uint64) (<-chan protocol.EventEnvelope, error) {
	if projectID == "" {
		return nil, fmt.Errorf("missing project_id for subscribe")
	}

	ch, cancel := d.hub.Subscribe(projectID, fromRevision)
	go func() {
		<-ctx.Done()
		cancel()
	}()
	if err := d.harness.appendEvent("client.multiproject.subscribe", map[string]any{
		"project_id":    projectID,
		"from_revision": fromRevision,
	}); err != nil {
		cancel()
		return nil, err
	}
	return ch, nil
}

func TestMultiprojectIsolationConcurrentClients(t *testing.T) {
	h := New(Config{
		BaseDir:   t.TempDir(),
		ProjectID: "proj-root",
	})
	if err := h.Boot(); err != nil {
		t.Fatalf("Boot: %v", err)
	}
	defer func() {
		if err := h.Shutdown(); err != nil {
			t.Fatalf("Shutdown: %v", err)
		}
	}()

	daemon := newMultiprojectDaemon(h)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	clientA := daemonclient.New(daemon).WithProjectID("proj-a")
	clientB := daemonclient.New(daemon).WithProjectID("proj-b")

	type clientCase struct {
		projectID string
		sessionID string
		client    *daemonclient.Client
	}

	errCh := make(chan error, 2)
	readyCh := make(chan struct{}, 2)
	startCh := make(chan struct{})
	var wg sync.WaitGroup

	runClient := func(tc clientCase) {
		defer wg.Done()

		sub, err := tc.client.Subscribe(ctx, tc.projectID, 0)
		if err != nil {
			errCh <- fmt.Errorf("subscribe %s: %w", tc.projectID, err)
			return
		}

		readyCh <- struct{}{}
		<-startCh

		reqBody, err := json.Marshal(multiprojectSessionRequest{
			SessionID: tc.sessionID,
			IssueID:   tc.sessionID,
			State:     daemonstate.SessionStateAttached,
		})
		if err != nil {
			errCh <- fmt.Errorf("marshal request %s: %w", tc.projectID, err)
			return
		}

		resp, err := tc.client.Command(ctx, protocol.RequestEnvelope{
			ProtocolVersion: protocol.CurrentVersion,
			RequestID:       tc.projectID + "-upsert",
			Kind:            protocol.EnvelopeKindCommand,
			Command:         multiprojectUpsertCommand,
			SentAt:          time.Now().UTC(),
			Body:            reqBody,
		})
		if err != nil {
			errCh <- fmt.Errorf("command %s: %w", tc.projectID, err)
			return
		}

		var snapshot daemonstate.Snapshot
		if err := json.Unmarshal(resp.Body, &snapshot); err != nil {
			errCh <- fmt.Errorf("decode snapshot %s: %w", tc.projectID, err)
			return
		}
		if snapshot.ProjectID != tc.projectID {
			errCh <- fmt.Errorf("snapshot project_id = %q, want %q", snapshot.ProjectID, tc.projectID)
			return
		}
		if snapshot.Revision != 1 {
			errCh <- fmt.Errorf("snapshot revision = %d, want 1", snapshot.Revision)
			return
		}
		if len(snapshot.Sessions) != 1 {
			errCh <- fmt.Errorf("snapshot sessions = %d, want 1", len(snapshot.Sessions))
			return
		}
		if _, ok := snapshot.Sessions[tc.sessionID]; !ok {
			errCh <- fmt.Errorf("snapshot missing session %q: %+v", tc.sessionID, snapshot.Sessions)
			return
		}

		select {
		case evt, ok := <-sub:
			if !ok {
				errCh <- fmt.Errorf("subscription %s closed early", tc.projectID)
				return
			}
			if evt.ProjectID != tc.projectID {
				errCh <- fmt.Errorf("event project_id = %q, want %q", evt.ProjectID, tc.projectID)
				return
			}
			if evt.Revision != 1 {
				errCh <- fmt.Errorf("event revision = %d, want 1", evt.Revision)
				return
			}
		case <-ctx.Done():
			errCh <- fmt.Errorf("wait for event %s: %w", tc.projectID, ctx.Err())
			return
		}

		select {
		case extra := <-sub:
			errCh <- fmt.Errorf("unexpected extra event for %s: %+v", tc.projectID, extra)
			return
		default:
		}
	}

	waitForReady := func() {
		select {
		case <-readyCh:
		case err := <-errCh:
			if err != nil {
				t.Fatal(err)
			}
		case <-ctx.Done():
			t.Fatalf("waiting for client readiness: %v", ctx.Err())
		}
	}

	wg.Add(2)
	go runClient(clientCase{projectID: "proj-a", sessionID: "sess-a", client: clientA})
	go runClient(clientCase{projectID: "proj-b", sessionID: "sess-b", client: clientB})

	waitForReady()
	waitForReady()
	close(startCh)

	wg.Wait()
	close(errCh)
	for err := range errCh {
		if err != nil {
			t.Fatal(err)
		}
	}

	events := readHarnessEvents(t, h.LogFilePath())
	if countEvent(events, "client.multiproject.subscribe") != 2 {
		t.Fatalf("subscribe events = %d, want 2", countEvent(events, "client.multiproject.subscribe"))
	}
	if countEvent(events, "daemon.multiproject.command") != 2 {
		t.Fatalf("command events = %d, want 2", countEvent(events, "daemon.multiproject.command"))
	}
}

func TestMultiprojectIsolationScopedSnapshots(t *testing.T) {
	h := New(Config{
		BaseDir:   t.TempDir(),
		ProjectID: "proj-root",
	})
	if err := h.Boot(); err != nil {
		t.Fatalf("Boot: %v", err)
	}
	defer func() {
		if err := h.Shutdown(); err != nil {
			t.Fatalf("Shutdown: %v", err)
		}
	}()

	daemon := newMultiprojectDaemon(h)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	clientA := daemonclient.New(daemon).WithProjectID("proj-a")
	clientB := daemonclient.New(daemon).WithProjectID("proj-b")
	const sharedSessionID = "sess-shared"

	upsert := func(client *daemonclient.Client, projectID, issueID string) daemonstate.Snapshot {
		t.Helper()

		reqBody, err := json.Marshal(multiprojectSessionRequest{
			SessionID: sharedSessionID,
			IssueID:   issueID,
			State:     daemonstate.SessionStateAttached,
		})
		if err != nil {
			t.Fatalf("marshal request %s: %v", projectID, err)
		}

		resp, err := client.Command(ctx, protocol.RequestEnvelope{
			ProtocolVersion: protocol.CurrentVersion,
			RequestID:       projectID + "-upsert",
			Kind:            protocol.EnvelopeKindCommand,
			Command:         multiprojectUpsertCommand,
			SentAt:          time.Now().UTC(),
			Body:            reqBody,
		})
		if err != nil {
			t.Fatalf("command %s: %v", projectID, err)
		}

		var snapshot daemonstate.Snapshot
		if err := json.Unmarshal(resp.Body, &snapshot); err != nil {
			t.Fatalf("decode snapshot %s: %v", projectID, err)
		}
		return snapshot
	}

	readSnapshot := func(client *daemonclient.Client, projectID string) daemonstate.Snapshot {
		t.Helper()

		resp, err := client.Command(ctx, protocol.RequestEnvelope{
			ProtocolVersion: protocol.CurrentVersion,
			RequestID:       projectID + "-snapshot",
			Kind:            protocol.EnvelopeKindCommand,
			Command:         multiprojectSnapshotCommand,
			SentAt:          time.Now().UTC(),
		})
		if err != nil {
			t.Fatalf("snapshot %s: %v", projectID, err)
		}

		var snapshot daemonstate.Snapshot
		if err := json.Unmarshal(resp.Body, &snapshot); err != nil {
			t.Fatalf("decode snapshot %s: %v", projectID, err)
		}
		return snapshot
	}

	snapA := upsert(clientA, "proj-a", "issue-a")
	if snapA.ProjectID != "proj-a" {
		t.Fatalf("proj-a snapshot project_id = %q, want %q", snapA.ProjectID, "proj-a")
	}
	if snapA.Revision != 1 {
		t.Fatalf("proj-a revision = %d, want 1", snapA.Revision)
	}
	if len(snapA.Sessions) != 1 {
		t.Fatalf("proj-a sessions = %d, want 1", len(snapA.Sessions))
	}
	if got := snapA.Sessions[sharedSessionID]; got.IssueID != "issue-a" {
		t.Fatalf("proj-a session issue_id = %q, want %q", got.IssueID, "issue-a")
	}

	snapB := readSnapshot(clientB, "proj-b")
	if snapB.ProjectID != "proj-b" {
		t.Fatalf("proj-b snapshot project_id = %q, want %q", snapB.ProjectID, "proj-b")
	}
	if snapB.Revision != 0 {
		t.Fatalf("proj-b revision = %d, want 0 before any writes", snapB.Revision)
	}
	if len(snapB.Sessions) != 0 {
		t.Fatalf("proj-b sessions = %d, want 0 before any writes", len(snapB.Sessions))
	}

	snapB = upsert(clientB, "proj-b", "issue-b")
	if snapB.ProjectID != "proj-b" {
		t.Fatalf("proj-b snapshot project_id = %q, want %q", snapB.ProjectID, "proj-b")
	}
	if snapB.Revision != 1 {
		t.Fatalf("proj-b revision = %d, want 1", snapB.Revision)
	}
	if len(snapB.Sessions) != 1 {
		t.Fatalf("proj-b sessions = %d, want 1", len(snapB.Sessions))
	}
	if got := snapB.Sessions[sharedSessionID]; got.IssueID != "issue-b" {
		t.Fatalf("proj-b session issue_id = %q, want %q", got.IssueID, "issue-b")
	}

	snapA = readSnapshot(clientA, "proj-a")
	if snapA.Revision != 1 {
		t.Fatalf("proj-a revision after proj-b write = %d, want 1", snapA.Revision)
	}
	if len(snapA.Sessions) != 1 {
		t.Fatalf("proj-a sessions after proj-b write = %d, want 1", len(snapA.Sessions))
	}
	if got := snapA.Sessions[sharedSessionID]; got.IssueID != "issue-a" {
		t.Fatalf("proj-a session issue_id after proj-b write = %q, want %q", got.IssueID, "issue-a")
	}
}

func TestMultiprojectFallbackDoesNotBlockHealthyProject(t *testing.T) {
	h := New(Config{
		BaseDir:   t.TempDir(),
		ProjectID: "proj-root",
	})
	if err := h.Boot(); err != nil {
		t.Fatalf("Boot: %v", err)
	}
	defer func() {
		if err := h.Shutdown(); err != nil {
			t.Fatalf("Shutdown: %v", err)
		}
	}()

	daemon := newMultiprojectDaemon(h)
	daemon.fallbackProjectID = "proj-b"
	daemon.fallbackActivated = make(map[string]bool)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	clientA := daemonclient.New(daemon).WithProjectID("proj-a")
	clientB := daemonclient.New(daemon).WithProjectID("proj-b")

	upsert := func(client *daemonclient.Client, projectID, issueID string) (daemonstate.Snapshot, error) {
		reqBody, err := json.Marshal(multiprojectSessionRequest{
			SessionID: projectID + "-sess",
			IssueID:   issueID,
			State:     daemonstate.SessionStateAttached,
		})
		if err != nil {
			return daemonstate.Snapshot{}, fmt.Errorf("marshal request %s: %w", projectID, err)
		}

		resp, err := client.Command(ctx, protocol.RequestEnvelope{
			ProtocolVersion: protocol.CurrentVersion,
			RequestID:       projectID + "-upsert",
			Kind:            protocol.EnvelopeKindCommand,
			Command:         multiprojectUpsertCommand,
			SentAt:          time.Now().UTC(),
			Body:            reqBody,
		})
		if err != nil {
			return daemonstate.Snapshot{}, err
		}

		var snapshot daemonstate.Snapshot
		if err := json.Unmarshal(resp.Body, &snapshot); err != nil {
			return daemonstate.Snapshot{}, fmt.Errorf("decode snapshot %s: %w", projectID, err)
		}
		return snapshot, nil
	}

	errCh := make(chan error, 2)
	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		snap, err := upsert(clientA, "proj-a", "issue-a")
		if err != nil {
			errCh <- fmt.Errorf("proj-a upsert: %w", err)
			return
		}
		if snap.ProjectID != "proj-a" {
			errCh <- fmt.Errorf("proj-a snapshot project_id = %q, want %q", snap.ProjectID, "proj-a")
			return
		}
		if snap.Revision != 1 {
			errCh <- fmt.Errorf("proj-a snapshot revision = %d, want 1", snap.Revision)
			return
		}
		if len(snap.Sessions) != 1 {
			errCh <- fmt.Errorf("proj-a snapshot sessions = %d, want 1", len(snap.Sessions))
			return
		}
		errCh <- nil
	}()

	go func() {
		defer wg.Done()
		_, err := upsert(clientB, "proj-b", "issue-b")
		if err == nil {
			errCh <- fmt.Errorf("proj-b first upsert unexpectedly succeeded")
			return
		}
		if got := err.Error(); !strings.Contains(got, "switched to fallback") {
			errCh <- fmt.Errorf("proj-b first upsert error = %q, want fallback activation", got)
			return
		}

		snap, err := upsert(clientB, "proj-b", "issue-b")
		if err != nil {
			errCh <- fmt.Errorf("proj-b retry upsert: %w", err)
			return
		}
		if snap.ProjectID != "proj-b" {
			errCh <- fmt.Errorf("proj-b retry snapshot project_id = %q, want %q", snap.ProjectID, "proj-b")
			return
		}
		if snap.Revision != 1 {
			errCh <- fmt.Errorf("proj-b retry snapshot revision = %d, want 1", snap.Revision)
			return
		}
		if len(snap.Sessions) != 1 {
			errCh <- fmt.Errorf("proj-b retry snapshot sessions = %d, want 1", len(snap.Sessions))
			return
		}
		errCh <- nil
	}()

	wg.Wait()
	close(errCh)
	for err := range errCh {
		if err != nil {
			t.Fatal(err)
		}
	}

	events := readHarnessEvents(t, h.LogFilePath())
	if countEvent(events, "daemon.multiproject.fallback.activated") != 1 {
		t.Fatalf("fallback activated events = %d, want 1", countEvent(events, "daemon.multiproject.fallback.activated"))
	}
	if countEvent(events, "daemon.multiproject.command") != 2 {
		t.Fatalf("command events = %d, want 2", countEvent(events, "daemon.multiproject.command"))
	}
}
