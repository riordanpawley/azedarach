package daemon

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	daemonstate "github.com/riordanpawley/azedarach/internal/daemon/state"
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/naming"
	"github.com/riordanpawley/azedarach/internal/services/issues"
)

type workerMailWakeFixture struct {
	daemon    *Daemon
	client    *issues.Client
	store     *daemonstate.RuntimeStateStore
	receiver  *recordingAuthoritativeReceiver
	repoDir   string
	projectID string
	parentID  string
	issueID   string
	sessionID string
	now       time.Time
}

func newWorkerMailWakeFixture(t *testing.T, activity string, withDelivery bool) workerMailWakeFixture {
	t.Helper()
	ctx := context.Background()
	repoDir := t.TempDir()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	client := newMigratedIssueClientAtPath(t, filepath.Join(repoDir, "issues.db"), logger)
	t.Cleanup(func() { _ = client.CloseDB() })
	parentID, err := client.Create(ctx, issues.CreateTaskParams{Title: "parent", Type: domain.TypeEpic, Priority: domain.P1, Status: domain.StatusInProgress})
	if err != nil {
		t.Fatal(err)
	}
	issueID, err := client.Create(ctx, issues.CreateTaskParams{Title: "worker", Type: domain.TypeBug, Priority: domain.P1, Status: domain.StatusInProgress, ParentID: &parentID})
	if err != nil {
		t.Fatal(err)
	}
	const projectID = "worker-mail-project"
	store := daemonstate.NewRuntimeStateStoreAtPath(filepath.Join(repoDir, "runtime.db"), logger)
	t.Cleanup(func() { _ = store.Close() })
	d := &Daemon{
		cfg:                    Config{RepoDir: repoDir, Logger: logger},
		issueClientsByProject:  map[string]*issues.Client{projectID: client},
		runtimeStoresByProject: map[string]*daemonstate.RuntimeStateStore{projectID: store},
		revision:               map[string]uint64{},
	}
	sessionID := naming.CanonicalSessionIDForIssue(d.sessionNamingScope(projectID), naming.IssueID(issueID)).String()
	now := time.Now().UTC()
	if err := store.UpsertSessionState(ctx, projectID, daemonstate.Session{ID: sessionID, IssueID: issueID, State: daemonstate.SessionStateRunning, ObservedState: daemonstate.SessionStateRunning, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertManagedAgentIdentity(ctx, daemonstate.ManagedAgentIdentity{ProjectID: projectID, SessionID: sessionID, LogicalPaneID: "agent", TmuxPaneID: "%7", PanePID: 123, AgentIncarnation: "inc-1", AgentThreadID: "thread-1", ObservedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.ApplyPhysicalSessionObservation(ctx, daemonstate.PhysicalSessionObservation{ProjectID: projectID, SessionID: sessionID, ObservedState: daemonstate.SessionStateRunning, Activity: activity, ActivitySource: "hooks", UpdatedAt: now, ObservedVersion: now.UnixNano()}); err != nil {
		t.Fatal(err)
	}
	receiver := &recordingAuthoritativeReceiver{accepted: map[string]string{}}
	if withDelivery {
		d.agentInput = newAgentInputDeliveryService(d.sessionRuntimeStateStoreIfConfigured, d.issueClientForProject, receiver, "worker-mail-test")
		d.agentInput.deliveryEligible = d.agentInputDeliveryEligible
	}
	return workerMailWakeFixture{daemon: d, client: client, store: store, receiver: receiver, repoDir: repoDir, projectID: projectID, parentID: parentID, issueID: issueID, sessionID: sessionID, now: now}
}

func (f workerMailWakeFixture) send(t *testing.T, requestID string) {
	t.Helper()
	resp, err := f.daemon.handleMailSend(context.Background(), protocol.RequestEnvelope{
		RequestID: naming.RequestID(requestID),
		Meta:      protocol.Metadata{ProjectID: naming.ProjectID(f.projectID)},
		Body: mustMarshal(t, protocol.MailSendCommandBody{
			RepoDir: f.repoDir, ParentIssue: f.parentID, IssueID: naming.IssueID(f.issueID),
			Type: "orchestrator-message", From: "orchestrator", To: f.issueID, Body: "apply the review guidance",
		}),
	})
	if err != nil || !resp.OK {
		t.Fatalf("mail send response=%+v err=%v", resp, err)
	}
}

func TestWorkerMailWakeDeliversIdleExactlyOnce(t *testing.T) {
	f := newWorkerMailWakeFixture(t, "idle", true)
	f.send(t, "idle-wake")
	f.send(t, "idle-wake")
	if f.receiver.calls != 1 {
		t.Fatalf("receiver calls=%d, want exactly one", f.receiver.calls)
	}
	counts, err := f.client.CountAgentInputDeliveryIntentsByKind(context.Background(), f.projectID, domain.AgentInputMessageWorkerMailWake)
	if err != nil || counts["delivered"] != 1 {
		t.Fatalf("delivery diagnostics=%v err=%v", counts, err)
	}
}

func TestWorkerMailWakeDefersBusyAndRecoversAfterRestart(t *testing.T) {
	f := newWorkerMailWakeFixture(t, "busy", true)
	f.send(t, "busy-wake")
	if f.receiver.calls != 0 {
		t.Fatalf("busy receiver calls=%d, want zero", f.receiver.calls)
	}
	later := f.now.Add(time.Second)
	if _, _, err := f.store.ApplyPhysicalSessionObservation(context.Background(), daemonstate.PhysicalSessionObservation{ProjectID: f.projectID, SessionID: f.sessionID, ObservedState: daemonstate.SessionStateRunning, Activity: "idle", ActivitySource: "hooks", UpdatedAt: later, ObservedVersion: later.UnixNano()}); err != nil {
		t.Fatal(err)
	}
	restartedReceiver := &recordingAuthoritativeReceiver{accepted: map[string]string{}}
	restarted := &Daemon{
		cfg:                    f.daemon.cfg,
		issueClientsByProject:  map[string]*issues.Client{f.projectID: f.client},
		runtimeStoresByProject: map[string]*daemonstate.RuntimeStateStore{f.projectID: f.store},
		revision:               map[string]uint64{},
	}
	restarted.agentInput = newAgentInputDeliveryService(restarted.sessionRuntimeStateStoreIfConfigured, restarted.issueClientForProject, restartedReceiver, "worker-mail-restart")
	restarted.agentInput.deliveryEligible = restarted.agentInputDeliveryEligible
	if err := restarted.reconcilePendingWorkerMailWakes(context.Background(), f.projectID); err != nil {
		t.Fatal(err)
	}
	if err := restarted.agentInput.RetryPending(context.Background(), f.projectID, 100); err != nil {
		t.Fatal(err)
	}
	if restartedReceiver.calls != 1 {
		t.Fatalf("restart receiver calls=%d, want one", restartedReceiver.calls)
	}
	if err := restarted.reconcilePendingWorkerMailWakes(context.Background(), f.projectID); err != nil {
		t.Fatal(err)
	}
	if restartedReceiver.calls != 1 {
		t.Fatalf("deduplicated restart receiver calls=%d, want one", restartedReceiver.calls)
	}
}

func TestWorkerMailWakeMaterializesAfterCrashBetweenMailAndIntent(t *testing.T) {
	f := newWorkerMailWakeFixture(t, "idle", false)
	f.send(t, "crash-gap-wake")
	receiver := &recordingAuthoritativeReceiver{accepted: map[string]string{}}
	restarted := &Daemon{
		cfg:                    f.daemon.cfg,
		issueClientsByProject:  map[string]*issues.Client{f.projectID: f.client},
		runtimeStoresByProject: map[string]*daemonstate.RuntimeStateStore{f.projectID: f.store},
		revision:               map[string]uint64{},
	}
	restarted.agentInput = newAgentInputDeliveryService(restarted.sessionRuntimeStateStoreIfConfigured, restarted.issueClientForProject, receiver, "worker-mail-crash-recovery")
	restarted.agentInput.deliveryEligible = restarted.agentInputDeliveryEligible
	if err := restarted.reconcilePendingWorkerMailWakes(context.Background(), f.projectID); err != nil {
		t.Fatal(err)
	}
	if receiver.calls != 1 {
		t.Fatalf("crash recovery receiver calls=%d, want one", receiver.calls)
	}
}

func TestWorkerMailWakeRejectsMismatchedIssueTarget(t *testing.T) {
	f := newWorkerMailWakeFixture(t, "idle", true)
	resp, err := f.daemon.handleMailSend(context.Background(), protocol.RequestEnvelope{
		RequestID: "wrong-target",
		Meta:      protocol.Metadata{ProjectID: naming.ProjectID(f.projectID)},
		Body: mustMarshal(t, protocol.MailSendCommandBody{
			RepoDir: f.repoDir, ParentIssue: f.parentID, IssueID: naming.IssueID(f.issueID),
			Type: "review-finding", From: "orchestrator", To: "different-issue", Body: "must not inject",
		}),
	})
	if err != nil || !resp.OK {
		t.Fatalf("mail send response=%+v err=%v", resp, err)
	}
	if f.receiver.calls != 0 {
		t.Fatalf("mismatched target receiver calls=%d, want zero", f.receiver.calls)
	}
	counts, err := f.client.CountAgentInputDeliveryIntentsByKind(context.Background(), f.projectID, domain.AgentInputMessageWorkerMailWake)
	if err != nil || len(counts) != 0 {
		t.Fatalf("mismatched target diagnostics=%v err=%v, want no intent", counts, err)
	}
}
