package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	daemonhandlers "github.com/riordanpawley/azedarach/internal/daemon/handlers"
	"github.com/riordanpawley/azedarach/internal/daemon/publish"
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/naming"
	"github.com/riordanpawley/azedarach/internal/services/issues"
)

func TestDrainInFlightCommandsWaitsForCompletionAndRejectsNewIntake(t *testing.T) {
	d := &Daemon{
		cfg: Config{
			IdleTimeout: 100 * time.Millisecond,
		},
	}

	if err := d.beginCommand(); err != nil {
		t.Fatalf("beginCommand error: %v", err)
	}

	drained := make(chan struct{})
	go func() {
		d.drainInFlightCommands()
		close(drained)
	}()

	select {
	case <-drained:
		t.Fatal("drain returned before in-flight command finished")
	case <-time.After(20 * time.Millisecond):
	}

	if err := d.beginCommand(); err == nil {
		t.Fatal("expected beginCommand to reject new intake while draining")
	}

	d.endCommand()

	select {
	case <-drained:
	case <-time.After(time.Second):
		t.Fatal("drain did not finish after in-flight command completed")
	}
}

func TestCommandLogsFailure(t *testing.T) {
	var logs bytes.Buffer
	d := &Daemon{
		cfg: Config{
			Logger: slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelInfo})),
		},
	}

	req := protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-1",
		Kind:            protocol.EnvelopeKindCommand,
		Meta: protocol.Metadata{
			ProjectID: "demo",
		},
		Command: "task.unsupported",
		SentAt:  time.Now().UTC(),
	}

	resp, err := d.command(context.Background(), req)
	if err != nil {
		t.Fatalf("command returned error: %v", err)
	}
	if resp.Error == nil {
		t.Fatalf("expected command error response, got ok=%v", resp.OK)
	}

	output := logs.String()
	if !strings.Contains(output, "daemon command received") {
		t.Fatalf("expected start command log entry, got %q", output)
	}
	if !strings.Contains(output, "daemon command failed") {
		t.Fatalf("expected failure command log entry, got %q", output)
	}
	if !strings.Contains(output, "command=task.unsupported") {
		t.Fatalf("expected command field in logs, got %q", output)
	}
	if !strings.Contains(output, "request_id=req-1") {
		t.Fatalf("expected request_id field in logs, got %q", output)
	}
}

func TestCommandDaemonShutdownRequestsRuntimeStop(t *testing.T) {
	d := &Daemon{}
	req := protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-shutdown",
		Kind:            protocol.EnvelopeKindCommand,
		Command:         protocol.CommandDaemonShutdown,
		SentAt:          time.Now().UTC(),
	}

	resp, err := d.command(context.Background(), req)
	if err != nil {
		t.Fatalf("command returned error: %v", err)
	}
	if !resp.OK {
		t.Fatalf("shutdown response = %+v", resp)
	}

	select {
	case <-d.shutdownRequestChannel():
	case <-time.After(200 * time.Millisecond):
		t.Fatal("expected shutdown request channel to close")
	}
}

func TestPrepareRunShutdownStateResetsSignals(t *testing.T) {
	d := &Daemon{
		shuttingDown:  true,
		shutdownReqCh: make(chan struct{}),
	}
	d.requestShutdown()

	d.prepareRunShutdownState()

	if d.shuttingDown {
		t.Fatal("expected shuttingDown reset to false")
	}
	if err := d.beginCommand(); err != nil {
		t.Fatalf("beginCommand after reset: %v", err)
	}
	d.endCommand()

	select {
	case <-d.shutdownRequestChannel():
		t.Fatal("expected fresh shutdown request channel to remain open after reset")
	default:
	}
}

func TestValidateCommandPolicyConfigurationFailsForIncompleteDispatcher(t *testing.T) {
	d := &Daemon{
		router: daemonhandlers.NewDispatcher(nil),
	}

	if err := d.validateCommandPolicyConfiguration(); err == nil {
		t.Fatal("expected command policy validation to fail for incomplete dispatcher wiring")
	}
}

func TestCommandCanonicalizesProjectIDAcrossTaskCommands(t *testing.T) {
	ctx := context.Background()
	logger := slog.Default()
	dbPath := filepath.Join(t.TempDir(), "issues.db")
	issuesClient := issues.NewClientAtPath(dbPath, logger)
	t.Cleanup(func() {
		if err := issuesClient.CloseDB(); err != nil {
			t.Fatalf("close issues db: %v", err)
		}
	})

	d := &Daemon{
		cfg: Config{
			Logger: logger,
		},
		issues: issuesClient,
		hub:    publish.NewHub(32, 16, logger),
	}
	if err := d.bootstrapSyncOrchestrator(ctx); err != nil {
		t.Fatalf("bootstrap sync orchestrator: %v", err)
	}

	events, cancel := d.hub.Subscribe("bmd", 0)
	defer cancel()

	mkReq := func(command string, projectID string, body any) protocol.RequestEnvelope {
		payload, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal %s body: %v", command, err)
		}
		return protocol.RequestEnvelope{
			ProtocolVersion: protocol.CurrentVersion,
			RequestID:       naming.RequestID(command + "-req"),
			Kind:            protocol.EnvelopeKindCommand,
			Meta:            protocol.Metadata{ProjectID: naming.ProjectID(projectID)},
			Command:         command,
			SentAt:          time.Now().UTC(),
			Body:            payload,
		}
	}

	createResp, err := d.command(ctx, mkReq("task.create", " bmd ", struct {
		Title       string          `json:"title"`
		Description string          `json:"description"`
		Type        domain.TaskType `json:"type"`
		Priority    domain.Priority `json:"priority"`
	}{
		Title:       "Normalize project IDs",
		Description: "ensure canonical project routing",
		Type:        domain.TypeTask,
		Priority:    domain.P1,
	}))
	if err != nil {
		t.Fatalf("task.create command error: %v", err)
	}
	if !createResp.OK {
		t.Fatalf("task.create response = %+v", createResp.Error)
	}
	if got, want := createResp.Meta.ProjectID.String(), "bmd"; got != want {
		t.Fatalf("task.create response meta project_id = %q, want %q", got, want)
	}
	if got, want := createResp.Revision, uint64(1); got != want {
		t.Fatalf("task.create revision = %d, want %d", got, want)
	}
	var createBody struct {
		TaskID string `json:"task_id"`
	}
	if err := json.Unmarshal(createResp.Body, &createBody); err != nil {
		t.Fatalf("unmarshal task.create body: %v", err)
	}
	if createBody.TaskID == "" {
		t.Fatal("task.create returned empty task id")
	}

	createEvt := waitForDaemonEvent(t, events)
	if createEvt.ProjectID.String() != "bmd" {
		t.Fatalf("task.create event project_id = %q, want bmd", createEvt.ProjectID)
	}
	if createEvt.Meta.ProjectID.String() != "bmd" {
		t.Fatalf("task.create event meta project_id = %q, want bmd", createEvt.Meta.ProjectID)
	}
	var createEventBody protocol.TaskEventBody
	if err := json.Unmarshal(createEvt.Body, &createEventBody); err != nil {
		t.Fatalf("unmarshal task.create event body: %v", err)
	}
	if createEventBody.TaskID.String() != createBody.TaskID || createEventBody.Task == nil || createEventBody.Task.Title != "Normalize project IDs" {
		t.Fatalf("task.create event body = %+v", createEventBody)
	}

	updateResp, err := d.command(ctx, mkReq("task.update_status", "bmd", struct {
		TaskID string        `json:"task_id"`
		Status domain.Status `json:"status"`
	}{
		TaskID: createBody.TaskID,
		Status: domain.StatusInProgress,
	}))
	if err != nil {
		t.Fatalf("task.update_status command error: %v", err)
	}
	if !updateResp.OK {
		t.Fatalf("task.update_status response = %+v", updateResp.Error)
	}
	if got, want := updateResp.Meta.ProjectID.String(), "bmd"; got != want {
		t.Fatalf("task.update_status response meta project_id = %q, want %q", got, want)
	}
	if got, want := updateResp.Revision, uint64(2); got != want {
		t.Fatalf("task.update_status revision = %d, want %d", got, want)
	}

	updateEvt := waitForDaemonEvent(t, events)
	if updateEvt.ProjectID.String() != "bmd" {
		t.Fatalf("task.update_status event project_id = %q, want bmd", updateEvt.ProjectID)
	}
	if updateEvt.Meta.ProjectID.String() != "bmd" {
		t.Fatalf("task.update_status event meta project_id = %q, want bmd", updateEvt.Meta.ProjectID)
	}
	var updateEventBody protocol.TaskEventBody
	if err := json.Unmarshal(updateEvt.Body, &updateEventBody); err != nil {
		t.Fatalf("unmarshal task.update_status event body: %v", err)
	}
	if updateEventBody.TaskID.String() != createBody.TaskID || updateEventBody.Task == nil || updateEventBody.Task.Status != domain.StatusInProgress {
		t.Fatalf("task.update_status event body = %+v", updateEventBody)
	}
}

func TestCommandDefaultsBlankProjectID(t *testing.T) {
	ctx := context.Background()
	logger := slog.Default()
	dbPath := filepath.Join(t.TempDir(), "issues.db")
	issuesClient := issues.NewClientAtPath(dbPath, logger)
	t.Cleanup(func() {
		if err := issuesClient.CloseDB(); err != nil {
			t.Fatalf("close issues db: %v", err)
		}
	})

	d := &Daemon{
		cfg: Config{
			Logger: logger,
		},
		issues: issuesClient,
		hub:    publish.NewHub(16, 8, logger),
	}
	if err := d.bootstrapSyncOrchestrator(ctx); err != nil {
		t.Fatalf("bootstrap sync orchestrator: %v", err)
	}

	events, cancel := d.hub.Subscribe(protocol.DefaultProjectID, 0)
	defer cancel()

	resp, err := d.command(ctx, protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "task.create-default-req",
		Kind:            protocol.EnvelopeKindCommand,
		Meta:            protocol.Metadata{ProjectID: "   "},
		Command:         "task.create",
		SentAt:          time.Now().UTC(),
		Body: mustJSONBody(t, struct {
			Title       string          `json:"title"`
			Description string          `json:"description"`
			Type        domain.TaskType `json:"type"`
			Priority    domain.Priority `json:"priority"`
		}{
			Title:       "Default project",
			Description: "blank project should route to default",
			Type:        domain.TypeTask,
			Priority:    domain.P1,
		}),
	})
	if err != nil {
		t.Fatalf("task.create command error: %v", err)
	}
	if !resp.OK {
		t.Fatalf("task.create response = %+v", resp.Error)
	}
	if got, want := resp.Meta.ProjectID.String(), protocol.DefaultProjectID; got != want {
		t.Fatalf("task.create response meta project_id = %q, want %q", got, want)
	}
	if got, want := resp.Revision, uint64(1); got != want {
		t.Fatalf("task.create revision = %d, want %d", got, want)
	}
	evt := waitForDaemonEvent(t, events)
	if evt.ProjectID.String() != protocol.DefaultProjectID {
		t.Fatalf("event project_id = %q, want default", evt.ProjectID)
	}
	if evt.Meta.ProjectID.String() != protocol.DefaultProjectID {
		t.Fatalf("event meta project_id = %q, want default", evt.Meta.ProjectID)
	}
}

func waitForDaemonEvent(t *testing.T, ch <-chan protocol.EventEnvelope) protocol.EventEnvelope {
	t.Helper()
	select {
	case evt := <-ch:
		return evt
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for daemon event")
		return protocol.EventEnvelope{}
	}
}

func mustJSONBody(t *testing.T, v any) []byte {
	t.Helper()
	payload, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal JSON: %v", err)
	}
	return payload
}
