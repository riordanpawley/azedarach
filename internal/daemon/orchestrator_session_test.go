package daemon

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	daemonstate "github.com/riordanpawley/azedarach/internal/daemon/state"
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/naming"
	"github.com/riordanpawley/azedarach/internal/services/tmux"
)

func TestProjectOrchestratorSessionStartAttachesExactScopeSingleton(t *testing.T) {
	ctx := context.Background()
	repoDir := t.TempDir()
	projectID := "project-session"
	store := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(repoDir, "runtime.db"), slog.Default())
	t.Cleanup(func() { _ = store.Close() })
	runner := newSessionStartTmuxRunner()
	d := &Daemon{
		cfg:                    Config{RepoDir: repoDir, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))},
		runtimeStoresByProject: map[string]*daemonstate.RuntimeStateStore{projectID: store},
		tmux:                   tmux.NewClient(runner, slog.New(slog.NewTextHandler(io.Discard, nil))),
	}
	body, err := json.Marshal(protocol.OrchestratorSessionRequest{Scope: domain.ProjectOrchestrationScope()})
	if err != nil {
		t.Fatal(err)
	}
	request := protocol.RequestEnvelope{Command: protocol.CommandOrchestratorSessionStart, Meta: protocol.Metadata{ProjectID: naming.ProjectID(projectID)}, Body: body}
	statusRequest := request
	statusRequest.Command = protocol.CommandOrchestratorSessionStatus
	missingResponse, err := d.handleOrchestratorSession(ctx, statusRequest)
	if err != nil || missingResponse.Error != nil {
		t.Fatalf("missing status: response=%+v err=%v", missingResponse.Error, err)
	}
	var missing protocol.OrchestratorSessionResult
	if err := json.Unmarshal(missingResponse.Body, &missing); err != nil {
		t.Fatal(err)
	}
	if missing.Disposition != "not-found" || missing.Live {
		t.Fatalf("missing status = %+v", missing)
	}

	firstResponse, err := d.handleOrchestratorSession(ctx, request)
	if err != nil || firstResponse.Error != nil {
		t.Fatalf("first start: response=%+v err=%v", firstResponse.Error, err)
	}
	var first protocol.OrchestratorSessionResult
	if err := json.Unmarshal(firstResponse.Body, &first); err != nil {
		t.Fatal(err)
	}
	if first.Disposition != string(daemonstate.OrchestratorLeaseAcquired) || !first.Live {
		t.Fatalf("first result = %+v", first)
	}
	staleProjection, found, err := store.GetSessionState(ctx, projectID, first.SessionID)
	if err != nil || !found {
		t.Fatalf("load stale projection: found=%t err=%v", found, err)
	}
	staleProjection.State, staleProjection.ObservedState = daemonstate.SessionStateStopped, daemonstate.SessionStateStopped
	if err := store.UpsertSessionState(ctx, projectID, staleProjection); err != nil {
		t.Fatal(err)
	}

	secondResponse, err := d.handleOrchestratorSession(ctx, request)
	if err != nil || secondResponse.Error != nil {
		t.Fatalf("second start: response=%+v err=%v", secondResponse.Error, err)
	}
	var second protocol.OrchestratorSessionResult
	if err := json.Unmarshal(secondResponse.Body, &second); err != nil {
		t.Fatal(err)
	}
	if second.Disposition != string(daemonstate.OrchestratorLeaseAttached) || second.SessionID != first.SessionID {
		t.Fatalf("second result = %+v, first = %+v", second, first)
	}

	newSessions := 0
	for _, command := range runner.commands {
		if len(command) > 0 && command[0] == "new-session" {
			newSessions++
		}
	}
	if newSessions != 1 {
		t.Fatalf("new-session calls = %d, want 1", newSessions)
	}
	runner.sessions[first.SessionID] = false
	recoveredResponse, err := d.handleOrchestratorSession(ctx, request)
	if err != nil || recoveredResponse.Error != nil {
		t.Fatalf("recovered start: response=%+v err=%v", recoveredResponse.Error, err)
	}
	var recovered protocol.OrchestratorSessionResult
	if err := json.Unmarshal(recoveredResponse.Body, &recovered); err != nil {
		t.Fatal(err)
	}
	if recovered.Disposition != string(daemonstate.OrchestratorLeaseRecoveredStale) || !recovered.Live {
		t.Fatalf("recovered = %+v", recovered)
	}
	newSessions = 0
	for _, command := range runner.commands {
		if len(command) > 0 && command[0] == "new-session" {
			newSessions++
		}
	}
	if newSessions != 2 {
		t.Fatalf("new-session calls after recovery = %d, want 2", newSessions)
	}
	projection, found, err := store.GetSessionState(ctx, projectID, first.SessionID)
	if err != nil || !found {
		t.Fatalf("projection found=%t err=%v", found, err)
	}
	if projection.Role != daemonstate.SessionRoleOrchestrator || projection.ScopeKind != daemonstate.SessionScopeOrchestration || projection.ScopeID != "project" {
		t.Fatalf("projection = %+v", projection)
	}
	if projection.State != daemonstate.SessionStateRunning || projection.ObservedState != daemonstate.SessionStateRunning {
		t.Fatalf("projection lifecycle = %+v", projection)
	}
}
