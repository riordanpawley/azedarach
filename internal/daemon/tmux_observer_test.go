package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	"github.com/riordanpawley/azedarach/internal/daemon/publish"
	daemonstate "github.com/riordanpawley/azedarach/internal/daemon/state"
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/services/tmux"
)

func TestTmuxObserverPublishesChangedCurrentProjectionWithProvenance(t *testing.T) {
	ctx := context.Background()
	const projectID, issueID, sessionID = "observer-project", "obs", "observer-session"
	store := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(t.TempDir(), "runtime.db"), slog.Default())
	t.Cleanup(func() { _ = store.Close() })
	estimatedStartedAt := time.Unix(2, 0).UTC()
	if err := upsertSessionStateFixture(store, ctx, projectID, daemonstate.Session{
		ID: sessionID, IssueID: issueID, State: daemonstate.SessionStateRunning,
		ObservedState: daemonstate.SessionStateStopped, StartedAt: &estimatedStartedAt, UpdatedAt: time.Unix(10, 0).UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	d := &Daemon{
		cfg: Config{Logger: slog.Default()}, hub: publish.NewHub(8, 8, slog.Default()),
		runtimeStoresByProject: map[string]*daemonstate.RuntimeStateStore{projectID: store},
		revision:               map[string]uint64{},
	}
	events, unsubscribe := d.hub.Subscribe(projectID, 0)
	defer unsubscribe()
	observedAt := time.Unix(20, 0).UTC()
	createdAt := time.Unix(5, 0).UTC()
	live := newTmuxRuntimeLiveness([]tmux.SessionInfo{{Name: sessionID, AttachedCount: 2, CreatedAt: &createdAt}}, nil)
	if err := d.observeTmuxProject(ctx, projectID, live, domainCurrentTmuxProvenance(observedAt)); err != nil {
		t.Fatalf("observe tmux project: %v", err)
	}

	select {
	case event := <-events:
		if event.Event != protocol.EventSessionUpdated {
			t.Fatalf("event = %q, want %q", event.Event, protocol.EventSessionUpdated)
		}
		var body protocol.SessionProjectionEventBody
		if err := json.Unmarshal(event.Body, &body); err != nil {
			t.Fatal(err)
		}
		if body.Observation == nil || body.Observation.Authority != "tmux" || body.Observation.Product != "session_runtime" || body.Observation.Disposition != "current" {
			t.Fatalf("observation provenance = %+v", body.Observation)
		}
		if body.Observation.CanonicalEventAdmitted || body.Observation.SemanticSequenceAdvanced || !body.Observation.ObservedAt.Equal(observedAt) {
			t.Fatalf("observation admission = %+v", body.Observation)
		}
		if body.Runtime == nil || body.Runtime.Projection.Session.TmuxAttachedCount != 2 {
			t.Fatalf("runtime attachment projection = %+v, want 2", body.Runtime)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for observation projection event")
	}
	if got := d.currentRevision(projectID); got != 1 {
		t.Fatalf("current projection revision = %d, want 1", got)
	}
	row, found, err := store.GetSessionState(ctx, projectID, sessionID)
	if err != nil || !found || row.StartedAt == nil || !row.StartedAt.Equal(createdAt) {
		t.Fatalf("persisted started_at = %+v found=%v err=%v", row.StartedAt, found, err)
	}
	if err := d.observeTmuxProject(ctx, projectID, live, domainCurrentTmuxProvenance(observedAt.Add(time.Second))); err != nil {
		t.Fatal(err)
	}
	if got := d.currentRevision(projectID); got != 1 {
		t.Fatalf("unchanged routine poll advanced projection revision to %d", got)
	}
}

func TestTmuxObserverRotatesProjectSweepAfterTimeout(t *testing.T) {
	d := &Daemon{}
	projects := []string{"project-a", "project-b", "project-c"}
	var observed []string

	firstCtx, cancelFirst := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancelFirst()
	d.observeTmuxProjects(firstCtx, projects, func(ctx context.Context, projectID string) {
		observed = append(observed, projectID)
		if projectID == "project-a" {
			<-ctx.Done()
		}
	})
	if got, want := observed, []string{"project-a"}; !slices.Equal(got, want) {
		t.Fatalf("first timed-out sweep = %v, want %v", got, want)
	}

	secondCtx, cancelSecond := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancelSecond()
	d.observeTmuxProjects(secondCtx, projects, func(ctx context.Context, projectID string) {
		observed = append(observed, projectID)
		if projectID == "project-a" {
			<-ctx.Done()
		}
	})
	if got, want := observed, []string{"project-a", "project-b", "project-c", "project-a"}; !slices.Equal(got, want) {
		t.Fatalf("rotated sweep after timeout = %v, want %v", got, want)
	}
}

func domainCurrentTmuxProvenance(observedAt time.Time) domain.ExternalObservationProvenance {
	return domain.CurrentTmuxObservationProvenance(observedAt)
}

type cancelAwareTmuxObservationRunner struct{ entered chan struct{} }

func (r *cancelAwareTmuxObservationRunner) Run(ctx context.Context, args ...string) (string, error) {
	if len(args) > 0 && args[0] == "list-sessions" {
		select {
		case <-r.entered:
		default:
			close(r.entered)
		}
		<-ctx.Done()
		return "", ctx.Err()
	}
	return "", errors.New("unexpected tmux command")
}

func TestTmuxObservationWorkerCancellationWaitsForPollExit(t *testing.T) {
	runner := &cancelAwareTmuxObservationRunner{entered: make(chan struct{})}
	d := &Daemon{cfg: Config{Logger: slog.Default(), TmuxObservationInterval: time.Hour}, tmux: tmux.NewClient(runner, slog.Default())}
	ctx, cancel := context.WithCancel(context.Background())
	d.startTmuxObservationWorker(ctx)
	select {
	case <-runner.entered:
	case <-time.After(time.Second):
		t.Fatal("observer did not begin tmux poll")
	}
	cancel()
	done := make(chan struct{})
	go func() { d.tmuxObservationWG.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("observer did not exit after cancellation")
	}
}

func TestTmuxObserverDoesNotCreateDesiredIntentForUnprojectedRuntime(t *testing.T) {
	ctx := context.Background()
	const projectID = "observer-project"
	store := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(t.TempDir(), "runtime.db"), slog.Default())
	t.Cleanup(func() { _ = store.Close() })
	d := &Daemon{cfg: Config{Logger: slog.Default()}, runtimeStoresByProject: map[string]*daemonstate.RuntimeStateStore{projectID: store}}
	live := newTmuxRuntimeLiveness([]tmux.SessionInfo{{Name: "observer-orphan"}}, nil)
	if err := d.observeTmuxProject(ctx, projectID, live, domain.CurrentTmuxObservationProvenance(time.Now().UTC())); err != nil {
		t.Fatal(err)
	}
	rows, err := store.ListSessionStates(ctx, projectID)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Fatalf("observer invented desired session intent: %+v", rows)
	}
}

func TestTmuxObserverPurgesManagedIdentityWhenSessionDisappears(t *testing.T) {
	ctx := context.Background()
	const projectID, issueID, sessionID = "observer-project", "obs", "observer-session"
	store := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(t.TempDir(), "runtime.db"), slog.Default())
	t.Cleanup(func() { _ = store.Close() })
	observedAt := time.Date(2026, time.July, 19, 12, 0, 0, 0, time.UTC)
	if err := upsertSessionStateFixture(store, ctx, projectID, daemonstate.Session{
		ID: sessionID, IssueID: issueID, State: daemonstate.SessionStateRunning,
		ObservedState: daemonstate.SessionStateRunning, UpdatedAt: observedAt,
	}); err != nil {
		t.Fatal(err)
	}
	d := &Daemon{
		cfg: Config{Logger: slog.Default()}, hub: publish.NewHub(8, 8, slog.Default()),
		runtimeStoresByProject: map[string]*daemonstate.RuntimeStateStore{projectID: store}, revision: map[string]uint64{},
	}
	d.recordManagedAgentIdentityProjection(daemonstate.ManagedAgentIdentity{
		ProjectID: projectID, SessionID: sessionID, LogicalPaneID: "agent", TmuxPaneID: "7",
		PanePID: 123, AgentIncarnation: "observer-incarnation", ObservedAt: observedAt,
	}, true)
	if err := d.observeTmuxProject(ctx, projectID, newTmuxRuntimeLiveness(nil, nil), domain.CurrentTmuxObservationProvenance(observedAt.Add(time.Second))); err != nil {
		t.Fatalf("observe disappeared tmux session: %v", err)
	}
	if _, found := d.projectedManagedAgentIdentity(projectID, sessionID, "agent"); found {
		t.Fatal("tmux disappearance retained managed-agent identity projection")
	}
}
