package daemon

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	"github.com/riordanpawley/azedarach/internal/daemon/publish"
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/services/issues"
)

func TestInteractionStalenessReconcilePersistsAndPublishesOnce(t *testing.T) {
	ctx := context.Background()
	projectID := "proj-interaction-stale"
	dbPath := filepath.Join(t.TempDir(), "issues.db")
	client := issues.NewClientAtPath(dbPath, nil)
	if _, err := client.List(ctx); err != nil {
		t.Fatal(err)
	}
	issueID, err := client.Create(ctx, issues.CreateTaskParams{Title: "waiting", Type: domain.TypeTask, Priority: domain.P1, Status: domain.StatusOpen})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	r := domain.InteractionRequest{
		ID: "req-stale", IssueID: issueID, DecisionKey: "deploy", OrchestrationScope: projectID,
		Question: "Deploy?", Why: "release gate", RequiredDecisions: []string{"approve"},
		Significance: domain.InteractionSignificanceMaterial, Respondent: "human",
		DecisionPacket: domain.InteractionDecisionPacket{Summary: "Choose deploy timing"},
		State:          domain.InteractionOpen, Revision: 1, CreatedAt: now.Add(-48 * time.Hour), UpdatedAt: now.Add(-48 * time.Hour),
	}
	if err := client.CreateInteraction(ctx, r); err != nil {
		t.Fatal(err)
	}
	hub := publish.NewHub(16, 8, nil)
	d := &Daemon{hub: hub, revision: map[string]uint64{}, issueClientsByProject: map[string]*issues.Client{projectID: client}}
	service := issueInteractionService{daemon: d}
	ctx = withDaemonProjectIDContext(ctx, projectID)
	first, err := service.ListInteractions(ctx, protocol.InteractionListRequestBody{})
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Requests) != 1 || first.Requests[0].StaleAt == nil || len(first.Requests[0].Reminders) != 1 || !first.Ages[r.ID].Stale || first.Requests[0].State != domain.InteractionOpen {
		t.Fatalf("first reconcile = %+v", first)
	}
	second, err := service.ListInteractions(ctx, protocol.InteractionListRequestBody{})
	if err != nil {
		t.Fatal(err)
	}
	if second.Requests[0].Revision != first.Requests[0].Revision || len(second.Requests[0].Reminders) != 1 {
		t.Fatalf("second reconcile was not idempotent: first=%+v second=%+v", first.Requests[0], second.Requests[0])
	}
	recovered, err := service.MutateInteraction(ctx, protocol.CommandInteractionRecover, protocol.InteractionMutationRequestBody{ID: r.ID, ExpectedRevision: second.Requests[0].Revision, Actor: "orchestrator", SessionID: "session-recovered"})
	if err != nil {
		t.Fatal(err)
	}
	if recovered.Request.State != domain.InteractionOpen || recovered.Request.SessionID != "session-recovered" || recovered.Request.Recovery == nil || !recovered.Age.Stale {
		t.Fatalf("recovery response = %+v", recovered)
	}
	durable := issues.NewClientAtPath(dbPath, nil)
	got, found, err := durable.GetInteraction(context.Background(), r.ID)
	if err != nil || !found || got.StaleAt == nil || len(got.Reminders) != 1 || !got.Unresolved() || got.SessionID != "session-recovered" {
		t.Fatalf("restart projection = %+v found=%t err=%v", got, found, err)
	}
	events, cancel := hub.Subscribe(projectID, 0)
	defer cancel()
	want := map[string]bool{protocol.EventInteractionStale: false, protocol.EventInteractionReminder: false}
	for range 2 {
		select {
		case event := <-events:
			want[event.Event] = true
		case <-time.After(time.Second):
			t.Fatal("timed out waiting for interaction lifecycle events")
		}
	}
	if !want[protocol.EventInteractionStale] || !want[protocol.EventInteractionReminder] {
		t.Fatalf("events = %+v", want)
	}
}

func TestInteractionStalenessPolicyEscalatesBySignificance(t *testing.T) {
	routine := interactionStalenessPolicy(domain.InteractionSignificanceRoutine)
	material := interactionStalenessPolicy(domain.InteractionSignificanceMaterial)
	critical := interactionStalenessPolicy(domain.InteractionSignificanceCritical)
	if !(critical.StaleAfter < material.StaleAfter && material.StaleAfter < routine.StaleAfter) {
		t.Fatalf("stale thresholds routine=%v material=%v critical=%v", routine.StaleAfter, material.StaleAfter, critical.StaleAfter)
	}
	if critical.ReminderInterval >= material.ReminderInterval || material.ReminderInterval >= routine.ReminderInterval {
		t.Fatalf("reminder thresholds routine=%v material=%v critical=%v", routine.ReminderInterval, material.ReminderInterval, critical.ReminderInterval)
	}
}
