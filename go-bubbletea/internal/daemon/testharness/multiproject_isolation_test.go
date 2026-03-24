package testharness

import (
	"context"
	"encoding/json"
	"fmt"
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
	harness *Harness
	store   *daemonstate.Store
	hub     *daemonpublish.Hub
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
