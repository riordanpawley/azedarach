package testharness

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/riordanpawley/azedarach/internal/cli"
	"github.com/riordanpawley/azedarach/internal/client/daemonclient"
	"github.com/riordanpawley/azedarach/internal/config"
	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	daemonhandlers "github.com/riordanpawley/azedarach/internal/daemon/handlers"
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/services/issues"
)

type roundtripExportTransport struct {
	exportRequest protocol.RequestEnvelope
	snapshotBody  []byte
}

func (t *roundtripExportTransport) Handshake(context.Context, protocol.Hello) (protocol.HelloAck, error) {
	return protocol.HelloAck{Accepted: true}, nil
}

func (t *roundtripExportTransport) Command(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
	t.exportRequest = req
	if req.Command != "task.snapshot.export" {
		return protocol.ResponseEnvelope{}, fmt.Errorf("unexpected command: %s", req.Command)
	}
	return protocol.ResponseEnvelope{
		ProtocolVersion: req.ProtocolVersion,
		RequestID:       req.RequestID,
		Kind:            protocol.EnvelopeKindResponse,
		Meta:            req.Meta,
		CompletedAt:     req.SentAt,
		OK:              true,
		Body:            t.snapshotBody,
	}, nil
}

func (t *roundtripExportTransport) Subscribe(context.Context, string, uint64) (<-chan protocol.EventEnvelope, error) {
	return nil, errors.New("not implemented")
}

type roundtripApplyService struct {
	createIDs []string
	calls     []string
}

func (s *roundtripApplyService) Create(_ context.Context, params issues.CreateTaskParams) (string, error) {
	if len(s.createIDs) == 0 {
		return "", errors.New("no task ids left")
	}
	taskID := s.createIDs[0]
	s.createIDs = s.createIDs[1:]
	s.calls = append(s.calls, fmt.Sprintf("create:%s:%s:%s", params.Title, params.Priority.String(), params.Type))
	return taskID, nil
}

func (s *roundtripApplyService) Update(_ context.Context, id string, status domain.Status) error {
	s.calls = append(s.calls, fmt.Sprintf("status:%s:%s", id, status))
	return nil
}

func (s *roundtripApplyService) UpdateDetails(_ context.Context, id string, params issues.UpdateTaskParams) error {
	s.calls = append(s.calls, fmt.Sprintf("update:%s:%s:%s:%s", id, params.Title, params.Priority.String(), params.Type))
	return nil
}

func (s *roundtripApplyService) Delete(_ context.Context, id string) error {
	s.calls = append(s.calls, "delete:"+id)
	return nil
}

func (s *roundtripApplyService) Archive(_ context.Context, id string) error {
	s.calls = append(s.calls, "archive:"+id)
	return nil
}

type roundtripApplyRevisions struct {
	current   uint64
	published []string
}

func (r *roundtripApplyRevisions) CurrentRevision(string) uint64 {
	return r.current
}

func (r *roundtripApplyRevisions) NextRevision(string) uint64 {
	r.current++
	return r.current
}

func (r *roundtripApplyRevisions) PublishTaskEvent(_ protocol.RequestEnvelope, eventName string, rev uint64) {
	r.published = append(r.published, fmt.Sprintf("%s:%d", eventName, rev))
}

func TestRoundtripHappyPathExportEditDryRunApplySuccess(t *testing.T) {
	baseDir := t.TempDir()
	h := New(Config{
		BaseDir:   baseDir,
		ProjectID: "proj-roundtrip",
	})
	if err := h.Boot(); err != nil {
		t.Fatalf("Boot: %v", err)
	}

	logFile := h.LogFilePath()
	if logFile == "" {
		t.Fatal("expected log file path after boot")
	}

	exportBody := mustRoundtripJSON(t, protocol.SnapshotPayload{
		SchemaVersion:    protocol.SnapshotSchemaVersion,
		ProtocolVersion:  protocol.SnapshotProtocolVersion,
		SnapshotRevision: 7,
	})
	transport := &roundtripExportTransport{snapshotBody: exportBody}
	deps := &cli.Dependencies{
		Config:       config.DefaultConfig(),
		DaemonClient: daemonclient.New(transport).WithProjectID("proj-roundtrip"),
		Logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID:    "proj-roundtrip",
		RepoDir:      baseDir,
	}

	exportPath := filepath.Join(baseDir, "snapshot.json")
	if err := cli.ExportCommand(deps, cli.ExportOptions{Format: "json", Out: exportPath}); err != nil {
		t.Fatalf("ExportCommand: %v", err)
	}
	if transport.exportRequest.Command != "task.snapshot.export" {
		t.Fatalf("export command = %q, want task.snapshot.export", transport.exportRequest.Command)
	}
	if transport.exportRequest.Meta.ProjectID != "proj-roundtrip" {
		t.Fatalf("export project_id = %q, want proj-roundtrip", transport.exportRequest.Meta.ProjectID)
	}

	exported, err := os.ReadFile(exportPath)
	if err != nil {
		t.Fatalf("ReadFile(export): %v", err)
	}
	if string(exported) != string(exportBody) {
		t.Fatalf("exported body = %s, want %s", string(exported), string(exportBody))
	}

	var snapshot protocol.SnapshotPayload
	if err := json.Unmarshal(exported, &snapshot); err != nil {
		t.Fatalf("unmarshal snapshot export: %v", err)
	}
	if snapshot.SnapshotRevision != 7 {
		t.Fatalf("snapshot revision = %d, want 7", snapshot.SnapshotRevision)
	}

	applyReq := protocol.ApplyRequestBody{
		SchemaVersion:    protocol.ApplySchemaVersion,
		SnapshotRevision: snapshot.SnapshotRevision,
		DryRun:           true,
		Operations: []protocol.ApplyOperationBody{
			{
				Command: "task.create",
				Body: mustRoundtripJSON(t, roundtripCreateBody{
					Title:       "First task",
					Description: "Draft one",
					Type:        "task",
					Priority:    "high",
				}),
			},
			{
				Command: "task.create",
				Body: mustRoundtripJSON(t, roundtripCreateBody{
					Title:       "Second task",
					Description: "Draft two",
					Type:        "bug",
					Priority:    "medium",
				}),
			},
		},
	}

	applyPath := filepath.Join(baseDir, "apply.json")
	applyDryRunBody := mustRoundtripJSON(t, applyReq)
	if err := os.WriteFile(applyPath, applyDryRunBody, 0o644); err != nil {
		t.Fatalf("WriteFile(apply dry-run): %v", err)
	}

	service := &roundtripApplyService{
		createIDs: []string{"az-101", "az-102"},
	}
	revisions := &roundtripApplyRevisions{current: snapshot.SnapshotRevision}
	applyHandler := daemonhandlers.NewApplyHandler(service, revisions)

	dryRunResp := applyHandler.Handle(context.Background(), protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-dry-run",
		Kind:            protocol.EnvelopeKindCommand,
		Command:         protocol.CommandTaskBulkApply,
		Meta: protocol.Metadata{
			ProjectID: "proj-roundtrip",
		},
		Body: applyDryRunBody,
	})
	if !dryRunResp.OK {
		t.Fatalf("dry-run response error = %+v", dryRunResp.Error)
	}

	var preview daemonhandlers.ApplyDryRunPreview
	if err := json.Unmarshal(dryRunResp.Body, &preview); err != nil {
		t.Fatalf("unmarshal dry-run preview: %v", err)
	}
	if preview.SchemaVersion != protocol.ApplySchemaVersion {
		t.Fatalf("preview schema version = %d, want %d", preview.SchemaVersion, protocol.ApplySchemaVersion)
	}
	if preview.SnapshotRevision != snapshot.SnapshotRevision {
		t.Fatalf("preview snapshot revision = %d, want %d", preview.SnapshotRevision, snapshot.SnapshotRevision)
	}
	if !preview.DryRun {
		t.Fatal("preview dry_run = false, want true")
	}
	if got, want := len(preview.Operations), 2; got != want {
		t.Fatalf("preview operations = %d, want %d", got, want)
	}
	if preview.Operations[0].Index != 0 || preview.Operations[1].Index != 1 {
		t.Fatalf("preview indexes = %+v", preview.Operations)
	}
	if string(preview.Operations[0].Body) != string(applyReq.Operations[0].Body) {
		t.Fatalf("preview op0 body = %s, want %s", string(preview.Operations[0].Body), string(applyReq.Operations[0].Body))
	}
	if string(preview.Operations[1].Body) != string(applyReq.Operations[1].Body) {
		t.Fatalf("preview op1 body = %s, want %s", string(preview.Operations[1].Body), string(applyReq.Operations[1].Body))
	}

	applyReq.DryRun = false
	applyCommittedBody := mustRoundtripJSON(t, applyReq)
	if err := os.WriteFile(applyPath, applyCommittedBody, 0o644); err != nil {
		t.Fatalf("WriteFile(apply committed): %v", err)
	}

	applyResp := applyHandler.Handle(context.Background(), protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-apply",
		Kind:            protocol.EnvelopeKindCommand,
		Command:         protocol.CommandTaskBulkApply,
		Meta: protocol.Metadata{
			ProjectID: "proj-roundtrip",
		},
		Body: applyCommittedBody,
	})
	if !applyResp.OK {
		t.Fatalf("apply response error = %+v", applyResp.Error)
	}
	if applyResp.Revision != 9 {
		t.Fatalf("apply revision = %d, want 9", applyResp.Revision)
	}

	var result daemonhandlers.ApplyExecutionResult
	if err := json.Unmarshal(applyResp.Body, &result); err != nil {
		t.Fatalf("unmarshal apply result: %v", err)
	}
	if got, want := result.Summary, (daemonhandlers.ApplyExecutionSummary{Total: 2, Succeeded: 2, Failed: 0}); got != want {
		t.Fatalf("result summary = %+v, want %+v", got, want)
	}
	if got, want := len(result.Operations), 2; got != want {
		t.Fatalf("result operations = %d, want %d", got, want)
	}
	if got, want := len(result.Outcomes), 2; got != want {
		t.Fatalf("result outcomes = %d, want %d", got, want)
	}
	if result.Outcomes[0].Status != "success" || result.Outcomes[1].Status != "success" {
		t.Fatalf("unexpected outcomes = %+v", result.Outcomes)
	}
	if result.Outcomes[0].TaskID != "az-101" || result.Outcomes[1].TaskID != "az-102" {
		t.Fatalf("task IDs = %+v", result.Outcomes)
	}
	if result.Outcomes[0].Revision != 8 || result.Outcomes[1].Revision != 9 {
		t.Fatalf("outcome revisions = %+v", result.Outcomes)
	}
	if got, want := revisions.published, []string{"task.created:8", "task.created:9"}; !equalStrings(got, want) {
		t.Fatalf("published revisions = %v, want %v", got, want)
	}
	if got, want := service.calls, []string{
		"create:First task:P1:task",
		"create:Second task:P2:bug",
	}; !equalStrings(got, want) {
		t.Fatalf("service calls = %v, want %v", got, want)
	}

	if err := h.Shutdown(); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}

	logs, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatalf("ReadFile(log): %v", err)
	}
	logContent := string(logs)
	if !strings.Contains(logContent, "\"event\":\"daemon.harness.boot\"") {
		t.Fatalf("missing boot event in log: %s", logContent)
	}
	if !strings.Contains(logContent, "\"event\":\"daemon.harness.shutdown\"") {
		t.Fatalf("missing shutdown event in log: %s", logContent)
	}
}

type roundtripCreateBody struct {
	Title       string `json:"title"`
	Description string `json:"description"`
	Type        string `json:"type"`
	Priority    string `json:"priority"`
}

func mustRoundtripJSON(t *testing.T, v any) []byte {
	t.Helper()

	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal json: %v", err)
	}
	return b
}

func equalStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}
