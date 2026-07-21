package daemon

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	appconfig "github.com/riordanpawley/azedarach/internal/config"
	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/naming"
	"github.com/riordanpawley/azedarach/internal/services/issues"
)

type projectionFirstRequestServer struct {
	daemon    *Daemon
	cancel    context.CancelFunc
	result    chan protocol.ResponseEnvelope
	projectID string
}

func (*projectionFirstRequestServer) Bind() error  { return nil }
func (*projectionFirstRequestServer) Close() error { return nil }

type projectionStartupIsolationResult struct {
	corrupt protocol.ResponseEnvelope
	healthy protocol.ResponseEnvelope
}

type projectionStartupIsolationServer struct {
	daemon           *Daemon
	cancel           context.CancelFunc
	started          chan struct{}
	result           chan projectionStartupIsolationResult
	corruptProjectID string
	healthyProjectID string
}

func (*projectionStartupIsolationServer) Bind() error  { return nil }
func (*projectionStartupIsolationServer) Close() error { return nil }

func (s *projectionStartupIsolationServer) Serve(ctx context.Context) error {
	close(s.started)
	corrupt, _ := s.daemon.command(ctx, protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		Command:         "task.list",
		RequestID:       "corrupt-project-first-request",
		Meta:            protocol.Metadata{ProjectID: naming.ProjectID(s.corruptProjectID)},
	})
	body, _ := json.Marshal(protocol.ProjectionDeltaReadRequest{ProjectID: naming.ProjectID(s.healthyProjectID), Limit: 10})
	healthy, _ := s.daemon.command(ctx, protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		Command:         protocol.CommandProjectionDeltaList,
		RequestID:       "healthy-project-first-request",
		Meta:            protocol.Metadata{ProjectID: naming.ProjectID(s.healthyProjectID)},
		Body:            body,
	})
	s.result <- projectionStartupIsolationResult{corrupt: corrupt, healthy: healthy}
	s.cancel()
	<-ctx.Done()
	return nil
}

func (s *projectionFirstRequestServer) Serve(ctx context.Context) error {
	projectID := s.projectID
	if projectID == "" {
		projectID = "projection-startup"
	}
	body, _ := json.Marshal(protocol.ProjectionDeltaReadRequest{ProjectID: naming.ProjectID(projectID), Limit: 10})
	response, _ := s.daemon.command(ctx, protocol.RequestEnvelope{ProtocolVersion: protocol.CurrentVersion, Command: protocol.CommandProjectionDeltaList, RequestID: "first-request", Meta: protocol.Metadata{ProjectID: naming.ProjectID(projectID)}, Body: body})
	s.result <- response
	s.cancel()
	<-ctx.Done()
	return nil
}

func TestProjectionDeltaRegisteredProjectOpensBeforeFirstRequest(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	baseRepo := t.TempDir()
	registeredRepo := t.TempDir()
	const projectID = "registered-projection"
	if err := appconfig.SaveProjectsRegistry(&appconfig.ProjectsRegistry{Projects: []appconfig.Project{{ID: projectID, Name: "Registered projection", Path: registeredRepo}}}); err != nil {
		t.Fatal(err)
	}
	registeredDB := filepath.Join(registeredRepo, ".azedarach", "azedarach.db")
	canonicalProjectID, err := appconfig.ProjectIDForRoot(registeredRepo)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(registeredDB); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("registered project was mutated before daemon startup: %v", err)
	}

	primary := issues.NewClient(baseRepo, slog.Default())
	if _, err := primary.Create(context.Background(), issues.CreateTaskParams{Title: "primary routing sentinel", Type: domain.TypeTask}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	server := &projectionFirstRequestServer{cancel: cancel, result: make(chan protocol.ResponseEnvelope, 1), projectID: projectID}
	d := &Daemon{
		cfg:                   Config{RepoDir: baseRepo, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))},
		lock:                  bootstrapRecordingLock{},
		issues:                primary,
		issueClientsByProject: map[string]*issues.Client{},
		issueClientsByRoot:    map[string]*issues.Client{daemonStoreRootKey(baseRepo): primary},
		serve:                 server,
	}
	server.daemon = d
	errCh := make(chan error, 1)
	go func() { errCh <- d.Run(ctx) }()
	select {
	case response := <-server.result:
		if !response.OK {
			t.Fatalf("registered-project first projection read failed: %+v", response.Error)
		}
		var batch protocol.ProjectionDeltaBatch
		if err := json.Unmarshal(response.Body, &batch); err != nil {
			t.Fatal(err)
		}
		if batch.ProjectID.String() != protocol.NormalizeProjectID(canonicalProjectID) || batch.HeadCursor != 0 || len(batch.Deltas) != 0 {
			t.Fatalf("registered-project first response routed to wrong store: %+v", batch)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("daemon did not serve registered-project first projection request")
	}
	if err := <-errCh; err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(registeredDB); err != nil {
		t.Fatalf("startup did not open registered projection store: %v", err)
	}
}

func TestProjectionDeltaStartupQuarantinesOnlyCorruptRegisteredProject(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	baseRepo := t.TempDir()
	corruptRepo := t.TempDir()
	healthyRepo := t.TempDir()
	const corruptProjectID = "registered-corrupt"
	const healthyProjectID = "registered-healthy"
	if err := appconfig.SaveProjectsRegistry(&appconfig.ProjectsRegistry{Projects: []appconfig.Project{
		{ID: corruptProjectID, Name: "Corrupt", Path: corruptRepo},
		{ID: healthyProjectID, Name: "Healthy", Path: healthyRepo},
	}}); err != nil {
		t.Fatal(err)
	}

	corruptClient := issues.NewClient(corruptRepo, slog.Default())
	if _, err := corruptClient.Create(context.Background(), issues.CreateTaskParams{Title: "corrupt", Type: domain.TypeTask}); err != nil {
		t.Fatal(err)
	}
	if err := corruptClient.CloseDB(); err != nil {
		t.Fatal(err)
	}
	corruptDaemonSQLiteRootPage(t, filepath.Join(corruptRepo, ".azedarach", "azedarach.db"), "issue_observation_events")
	healthyClient := issues.NewClient(healthyRepo, slog.Default())
	if _, err := healthyClient.Create(context.Background(), issues.CreateTaskParams{Title: "healthy", Type: domain.TypeTask}); err != nil {
		t.Fatal(err)
	}
	if err := healthyClient.CloseDB(); err != nil {
		t.Fatal(err)
	}
	primary := issues.NewClient(baseRepo, slog.Default())
	if _, err := primary.Create(context.Background(), issues.CreateTaskParams{Title: "primary", Type: domain.TypeTask}); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	server := &projectionStartupIsolationServer{
		cancel: cancel, started: make(chan struct{}), result: make(chan projectionStartupIsolationResult, 1),
		corruptProjectID: corruptProjectID, healthyProjectID: healthyProjectID,
	}
	d := &Daemon{
		cfg:                              Config{RepoDir: baseRepo, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))},
		lock:                             bootstrapRecordingLock{},
		issues:                           primary,
		issueClientsByProject:            map[string]*issues.Client{},
		issueClientsByRoot:               map[string]*issues.Client{daemonStoreRootKey(baseRepo): primary},
		projectIssueStoreHealthByProject: map[string]projectIssueStoreHealthState{},
		serve:                            server,
	}
	server.daemon = d
	errCh := make(chan error, 1)
	go func() { errCh <- d.Run(ctx) }()
	select {
	case <-server.started:
	case err := <-errCh:
		t.Fatalf("daemon exited before IPC serve: %v", err)
	}

	result := <-server.result
	if result.corrupt.OK || result.corrupt.Error == nil || result.corrupt.Error.Code != protocol.ErrorCodeUnavailable || !strings.Contains(result.corrupt.Error.Message, "project issue store unhealthy (cached)") {
		t.Fatalf("corrupt project response=%+v, want cached unavailable", result.corrupt)
	}
	if !result.healthy.OK {
		t.Fatalf("healthy project response=%+v, want success", result.healthy.Error)
	}
	var batch protocol.ProjectionDeltaBatch
	if err := json.Unmarshal(result.healthy.Body, &batch); err != nil {
		t.Fatal(err)
	}
	if batch.HeadCursor != 1 || len(batch.Deltas) != 1 {
		t.Fatalf("healthy project batch=%+v, want one delta", batch)
	}
	if err := <-errCh; err != nil {
		t.Fatal(err)
	}
	corruptCanonical, _ := appconfig.ProjectIDForRoot(corruptRepo)
	healthyCanonical, _ := appconfig.ProjectIDForRoot(healthyRepo)
	if _, unhealthy := d.projectIssueStoreHealthError(corruptCanonical); !unhealthy {
		t.Fatal("corrupt registered project did not retain cached health")
	}
	if healthErr, unhealthy := d.projectIssueStoreHealthError(healthyCanonical); unhealthy {
		t.Fatalf("healthy registered project marked unhealthy: %v", healthErr)
	}
}

func TestProjectReadMaterializerStartupSkipsQuarantinedDefaultStore(t *testing.T) {
	d := &Daemon{
		cfg:                              Config{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))},
		issues:                           issues.NewClientAtPath(filepath.Join(t.TempDir(), "issues.db"), slog.Default()),
		projectIssueStoreHealthByProject: map[string]projectIssueStoreHealthState{},
	}
	d.recordProjectIssueStoreFailure(protocol.DefaultProjectID, &domain.TaskStoreError{Op: "open-db", Err: corruptSQLiteDaemonTestError{}})
	if err := d.startProjectReadMaterializers(context.Background()); err != nil {
		t.Fatalf("quarantined materializer startup returned global error: %v", err)
	}
	if materializer := d.activeProjectReadMaterializer(protocol.DefaultProjectID); materializer != nil {
		t.Fatalf("quarantined default store started materializer: %+v", materializer)
	}
}

func TestProjectionDeltaErrorEnvelopePreservesTypedRetrySemantics(t *testing.T) {
	gap := ProjectionDeltaErrorEnvelope(&domain.ProjectionGapError{ProjectID: "p", Expected: 1, Actual: 3})
	if gap.Code != protocol.ErrorCodeRevisionGap || !gap.Retryable {
		t.Fatalf("gap envelope=%+v", gap)
	}
	canceled := ProjectionDeltaErrorEnvelope(&domain.ProjectionCanceledError{Cause: context.Canceled})
	if canceled.Code != protocol.ErrorCodeTimeout || !canceled.Retryable {
		t.Fatalf("canceled envelope=%+v", canceled)
	}
	internal := ProjectionDeltaErrorEnvelope(errors.New("broken"))
	if internal.Code != protocol.ErrorCodeInternal || internal.Retryable {
		t.Fatalf("internal envelope=%+v", internal)
	}
	retryable := ProjectionDeltaErrorEnvelope(&domain.ProjectionRetryableError{Cause: errors.New("busy")})
	if retryable.Code != protocol.ErrorCodeUnavailable || !retryable.Retryable {
		t.Fatalf("retryable envelope=%+v", retryable)
	}
}

func TestProjectionDeltaCommandsReadActiveIssueMutation(t *testing.T) {
	ctx := context.Background()
	projectID := "projection-command-project"
	client := issues.NewClientAtPath(filepath.Join(t.TempDir(), "issues.db"), slog.Default())
	t.Cleanup(func() { _ = client.CloseDB() })
	issueID, err := client.Create(ctx, issues.CreateTaskParams{Title: "active command delta", Type: domain.TypeTask})
	if err != nil {
		t.Fatal(err)
	}
	d := &Daemon{
		cfg:                   Config{Logger: slog.Default()},
		issueClientsByProject: map[string]*issues.Client{projectID: client},
	}
	body, _ := json.Marshal(protocol.ProjectionDeltaReadRequest{ProjectID: naming.ProjectID(projectID), Limit: 10})
	resp, err := d.command(ctx, protocol.RequestEnvelope{ProtocolVersion: protocol.CurrentVersion, Command: protocol.CommandProjectionDeltaList, RequestID: "delta-list", Meta: protocol.Metadata{ProjectID: naming.ProjectID(projectID)}, Body: body})
	if err != nil || !resp.OK {
		t.Fatalf("list response=%+v transport=%v", resp.Error, err)
	}
	var batch protocol.ProjectionDeltaBatch
	if err := json.Unmarshal(resp.Body, &batch); err != nil {
		t.Fatal(err)
	}
	if batch.ProjectID.String() != projectID || batch.HeadCursor != 1 || len(batch.Deltas) != 1 || batch.Deltas[0].Key != issueID || batch.Deltas[0].ProjectID.String() != projectID {
		t.Fatalf("active-path batch=%+v", batch)
	}
	if batch.DeliveryContract != domain.ProjectionDeliveryContract || !batch.DeliveryCursorTransitional || batch.Projector.ID != domain.IssueProjectorID || batch.SemanticChecksum == "" || len(batch.SourceVector) != 1 || batch.SourceVector[0].Authority != "legacy_issue_observation" {
		t.Fatalf("projection bridge metadata=%+v", batch)
	}
	if err := protocol.VerifyProjectionDeltaBatch(batch, 0, issueProjectionProjector()); err != nil {
		t.Fatalf("verify projection batch: %v", err)
	}
	snapshotBody, _ := json.Marshal(protocol.ProjectionSnapshotRequest{ProjectID: naming.ProjectID(projectID), Cursor: 1})
	snapshotResp, err := d.command(ctx, protocol.RequestEnvelope{ProtocolVersion: protocol.CurrentVersion, Command: protocol.CommandProjectionSnapshot, RequestID: "delta-snapshot", Meta: protocol.Metadata{ProjectID: naming.ProjectID(projectID)}, Body: snapshotBody})
	if err != nil || !snapshotResp.OK {
		t.Fatalf("snapshot response=%+v transport=%v", snapshotResp.Error, err)
	}
	var snapshot protocol.ProjectionSnapshot
	if err := json.Unmarshal(snapshotResp.Body, &snapshot); err != nil {
		t.Fatal(err)
	}
	if snapshot.ProjectID.String() != projectID || snapshot.Cursor != 1 || len(snapshot.Values) != 1 || snapshot.Values[0].Key != issueID {
		t.Fatalf("active-path snapshot=%+v", snapshot)
	}
	if err := protocol.VerifyProjectionSnapshot(snapshot, issueProjectionProjector()); err != nil {
		t.Fatalf("verify projection snapshot: %v", err)
	}
}

func TestProjectionDeltaCommandsRejectOldAndNewEnvelopeVersions(t *testing.T) {
	d := &Daemon{cfg: Config{Logger: slog.Default()}}
	for _, tc := range []struct {
		name    string
		version protocol.Version
		code    protocol.ErrorCode
	}{
		{name: "old", version: protocol.ProjectionDeltaProtocolVersion - 1, code: protocol.ErrorCodeUpgradeRequired},
		{name: "new", version: protocol.CurrentVersion + 1, code: protocol.ErrorCodeIncompatible},
	} {
		t.Run(tc.name, func(t *testing.T) {
			response, err := d.handleProjectionDeltaRead(context.Background(), protocol.RequestEnvelope{ProtocolVersion: tc.version, Command: protocol.CommandProjectionDeltaList})
			if err != nil || response.Error == nil || response.Error.Code != tc.code {
				t.Fatalf("response=%+v err=%v, want %s", response, err, tc.code)
			}
		})
	}
}

func TestProjectionDeltaEmptyAdvanceCarriesSourceWithoutMaterializedChange(t *testing.T) {
	client := issues.NewClientAtPath(filepath.Join(t.TempDir(), "issues.db"), slog.Default())
	if err := client.OpenProjectionDeltaStore(); err != nil {
		t.Fatal(err)
	}
	defer client.CloseDB()
	_, err := client.CommitProjectionEmptyAdvance(context.Background(), issues.ProjectionSourceAdvance{ProjectID: "default", SourceAuthority: "legacy_mailbox", SourcePosition: "42", SourceHash: "source-hash-42"})
	if err != nil {
		t.Fatal(err)
	}
	batch, err := NewProjectionDeltaAuthority(client).List(context.Background(), "default", 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if batch.HeadCursor != 1 || batch.DeliveryToCursor != 1 || len(batch.Deltas) != 0 || len(batch.EmptyAdvances) != 1 || batch.EmptyAdvances[0].DeliveryCursor != 1 || len(batch.SourceVector) != 1 {
		t.Fatalf("empty advance batch=%+v", batch)
	}
	if err := protocol.VerifyProjectionDeltaBatch(batch, 0, issueProjectionProjector()); err != nil {
		t.Fatalf("verify empty advance: %v", err)
	}
	source := batch.SourceVector[0]
	if source.Authority != "legacy_mailbox" || source.SourceFrom != "42" || source.SourceTo != "42" || source.TerminalHash != "source-hash-42" || !source.Transitional {
		t.Fatalf("empty advance source=%+v", source)
	}
	snapshot, err := NewProjectionDeltaAuthority(client).Snapshot(context.Background(), "default", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Values) != 0 || snapshot.Cursor != 1 || snapshot.SemanticChecksum == "" || !snapshot.DeliveryCursorTransitional {
		t.Fatalf("empty advance snapshot=%+v", snapshot)
	}
}

func TestProjectionDeltaStoreOpensBeforeDaemonServesFirstRequest(t *testing.T) {
	repoDir := t.TempDir()
	path := filepath.Join(repoDir, ".azedarach", "azedarach.db")
	client := issues.NewClientAtPath(path, slog.Default())
	ctx, cancel := context.WithCancel(context.Background())
	server := &projectionFirstRequestServer{cancel: cancel, result: make(chan protocol.ResponseEnvelope, 1)}
	d := &Daemon{
		cfg:  Config{RepoDir: repoDir, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))},
		lock: bootstrapRecordingLock{}, issues: client,
		issueClientsByProject: map[string]*issues.Client{"projection-startup": client},
		issueClientsByRoot:    map[string]*issues.Client{daemonStoreRootKey(repoDir): client},
		serve:                 server,
	}
	server.daemon = d
	errCh := make(chan error, 1)
	go func() { errCh <- d.Run(ctx) }()
	select {
	case response := <-server.result:
		if !response.OK {
			t.Fatalf("fresh first projection read failed: %+v", response.Error)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("daemon did not serve first projection request")
	}
	if err := <-errCh; err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("startup did not open projection store: %v", err)
	}
}

func TestProjectionDeltaDaemonCanceledBeforeStartupDoesNotOpenStore(t *testing.T) {
	path := filepath.Join(t.TempDir(), "issues.db")
	client := issues.NewClientAtPath(path, slog.Default())
	d := &Daemon{cfg: Config{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}, lock: bootstrapRecordingLock{}, issues: client, serve: &bootstrapRecordingServer{recorder: &bootstrapRecorder{}}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := d.Run(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("canceled startup mutated store: %v", err)
	}
}

func TestProjectionDeltaDaemonStartupValidationFailurePreventsServe(t *testing.T) {
	path := filepath.Join(t.TempDir(), "issues.db")
	seed := issues.NewClientAtPath(path, slog.Default())
	if err := seed.OpenProjectionDeltaStore(); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path)+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DROP INDEX idx_projection_deltas_key_history; CREATE INDEX idx_projection_deltas_key_history ON projection_deltas(project_id,kind,key,cursor ASC)`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if err := seed.CloseDB(); err != nil {
		t.Fatal(err)
	}
	recorder := &bootstrapRecorder{}
	d := &Daemon{cfg: Config{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}, lock: bootstrapRecordingLock{}, issues: issues.NewClientAtPath(path, slog.Default()), serve: &bootstrapRecordingServer{recorder: recorder}}
	err = d.Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "projection delta") {
		t.Fatalf("startup error=%v", err)
	}
	if events := recorder.snapshot(); len(events) != 0 {
		t.Fatalf("IPC served before schema validation: %v", events)
	}
}

func TestProjectionDeltaRegisteredProjectValidationFailurePreventsServe(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	baseRepo := t.TempDir()
	registeredRepo := t.TempDir()
	const projectID = "registered-drift"
	if err := appconfig.SaveProjectsRegistry(&appconfig.ProjectsRegistry{Projects: []appconfig.Project{{ID: projectID, Name: "Registered drift", Path: registeredRepo}}}); err != nil {
		t.Fatal(err)
	}
	registeredPath := filepath.Join(registeredRepo, ".azedarach", "azedarach.db")
	seed := issues.NewClientAtPath(registeredPath, slog.Default())
	if err := seed.OpenProjectionDeltaStore(); err != nil {
		t.Fatal(err)
	}
	if err := seed.CloseDB(); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(registeredPath)+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DROP INDEX idx_projection_deltas_key_history; CREATE INDEX idx_projection_deltas_key_history ON projection_deltas(project_id,kind,key,cursor ASC)`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	recorder := &bootstrapRecorder{}
	primary := issues.NewClient(baseRepo, slog.Default())
	d := &Daemon{
		cfg:                   Config{RepoDir: baseRepo, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))},
		lock:                  bootstrapRecordingLock{},
		issues:                primary,
		issueClientsByProject: map[string]*issues.Client{},
		issueClientsByRoot:    map[string]*issues.Client{daemonStoreRootKey(baseRepo): primary},
		serve:                 &bootstrapRecordingServer{recorder: recorder},
	}
	err = d.Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), projectID) || !strings.Contains(err.Error(), "projection delta") {
		t.Fatalf("startup error=%v, want registered project projection validation failure", err)
	}
	if events := recorder.snapshot(); len(events) != 0 {
		t.Fatalf("IPC served before registered schema validation: %v", events)
	}
}

func TestProjectionDeltaIndependentDaemonProtocolWritersAreGapFree(t *testing.T) {
	repoDir := t.TempDir()
	seed := issues.NewClient(repoDir, slog.Default())
	if err := seed.OpenProjectionDeltaStore(); err != nil {
		t.Fatal(err)
	}
	if err := seed.CloseDB(); err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	commands := make([]*exec.Cmd, 2)
	outputs := make([]bytes.Buffer, 2)
	for worker := range commands {
		commands[worker] = exec.Command(executable, "-test.run=TestProjectionDeltaDaemonProtocolSubprocessWriter$")
		commands[worker].Env = append(os.Environ(), "AZEDARACH_DAEMON_DELTA_SUBPROCESS=1", "AZEDARACH_DAEMON_DELTA_REPO="+repoDir, fmt.Sprintf("AZEDARACH_DAEMON_DELTA_WORKER=%d", worker))
		commands[worker].Stdout = &outputs[worker]
		commands[worker].Stderr = &outputs[worker]
		if err := commands[worker].Start(); err != nil {
			t.Fatal(err)
		}
	}
	for index, command := range commands {
		if err := command.Wait(); err != nil {
			t.Fatalf("daemon protocol subprocess: %v\n%s", err, outputs[index].String())
		}
	}
	reader := issues.NewClient(repoDir, slog.Default())
	if err := reader.OpenProjectionDeltaStore(); err != nil {
		t.Fatal(err)
	}
	deltas, head, err := reader.ListProjectionDeltas(context.Background(), "default", 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if head != 20 || len(deltas) != 20 {
		t.Fatalf("head=%d deltas=%d", head, len(deltas))
	}
	for index, delta := range deltas {
		if delta.Cursor != uint64(index+1) {
			t.Fatalf("cursor[%d]=%d", index, delta.Cursor)
		}
		var payload domain.IssueProjectionDeltaPayload
		if err := json.Unmarshal(delta.Payload, &payload); err != nil || payload.Issue == nil || payload.Issue.ID.String() != delta.Key {
			t.Fatalf("semantic delta[%d]=%+v decode=%v", index, delta, err)
		}
	}
	if err := reader.CloseDB(); err != nil {
		t.Fatal(err)
	}
	restarted := issues.NewClient(repoDir, slog.Default())
	if err := restarted.OpenProjectionDeltaStore(); err != nil {
		t.Fatal(err)
	}
	replayed, replayHead, err := restarted.ListProjectionDeltas(context.Background(), "default", 0, 100)
	if err != nil || replayHead != head || len(replayed) != len(deltas) {
		t.Fatalf("restart replay head=%d count=%d err=%v", replayHead, len(replayed), err)
	}
	_ = restarted.CloseDB()
}

func TestProjectionDeltaDaemonProtocolSubprocessWriter(t *testing.T) {
	if os.Getenv("AZEDARACH_DAEMON_DELTA_SUBPROCESS") != "1" {
		t.Skip("subprocess helper")
	}
	repoDir, worker := os.Getenv("AZEDARACH_DAEMON_DELTA_REPO"), os.Getenv("AZEDARACH_DAEMON_DELTA_WORKER")
	d := New(Config{RepoDir: repoDir, SocketPath: filepath.Join(repoDir, worker+".sock"), LockPath: filepath.Join(repoDir, worker+".lock"), Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	defer d.closeIssueClients()
	for index := 0; index < 10; index++ {
		body, _ := json.Marshal(map[string]any{"title": fmt.Sprintf("worker-%s-%02d", worker, index), "type": domain.TypeTask})
		response, err := d.command(context.Background(), protocol.RequestEnvelope{ProtocolVersion: protocol.CurrentVersion, Kind: protocol.EnvelopeKindCommand, Command: "task.create", RequestID: naming.RequestID(fmt.Sprintf("%s-%d", worker, index)), Meta: protocol.Metadata{ProjectID: naming.ProjectID(d.canonicalProjectID(repoDir))}, Body: body})
		if err != nil || !response.OK {
			t.Fatalf("create %d response=%+v err=%v", index, response.Error, err)
		}
	}
}
