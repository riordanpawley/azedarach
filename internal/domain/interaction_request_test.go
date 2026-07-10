package domain

import (
	"errors"
	"testing"
	"time"
)

func validInteractionRequest() InteractionRequest {
	now := time.Date(2026, 7, 10, 10, 0, 0, 0, time.UTC)
	return InteractionRequest{
		ID: "ir-1", IssueID: "issue-1", DecisionKey: "merge-strategy",
		OrchestrationScope: "project-1", Question: "Which strategy?", Why: "Integration is blocked.",
		Options:      []InteractionOption{{Key: "squash", Label: "Squash"}},
		Significance: InteractionSignificanceMaterial, Respondent: "owner",
		DecisionPacket: InteractionDecisionPacket{Summary: "Choose integration strategy."},
		State:          InteractionOpen, Revision: 1, CreatedAt: now, UpdatedAt: now,
	}
}

func TestInteractionRequestValidate(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*InteractionRequest)
		wantErr bool
	}{
		{"valid", func(*InteractionRequest) {}, false},
		{"missing decision key", func(r *InteractionRequest) { r.DecisionKey = "" }, true},
		{"missing decision shape", func(r *InteractionRequest) { r.Options = nil }, true},
		{"missing decision packet", func(r *InteractionRequest) { r.DecisionPacket.Summary = "" }, true},
		{"duplicate option key", func(r *InteractionRequest) { r.Options = append(r.Options, r.Options[0]) }, true},
		{"malformed effect", func(r *InteractionRequest) {
			empty := " "
			r.Proposal = &InteractionAnswerAudit{Answer: interactionTestAnswer("squash", 1), Actor: "agent", CreatedAt: r.UpdatedAt}
			r.Proposal.Answer.ApprovedIssueFieldEffects.Title = &empty
			r.State, r.Revision = InteractionAnswerProposed, 2
		}, true},
		{"invalid revision", func(r *InteractionRequest) { r.Revision = 0 }, true},
		{"proposal audit required", func(r *InteractionRequest) { r.State = InteractionAnswerProposed }, true},
		{"final audit required", func(r *InteractionRequest) { r.State = InteractionResolved }, true},
		{"final audit forbidden before resolution", func(r *InteractionRequest) {
			r.FinalAnswer = &InteractionAnswerAudit{Answer: interactionTestAnswer("squash", 1), Actor: "owner", CreatedAt: r.UpdatedAt}
		}, true},
		{"malformed historical proposal rejected", func(r *InteractionRequest) {
			r.Proposal = &InteractionAnswerAudit{Answer: interactionTestAnswer("", 1), Actor: "agent", CreatedAt: r.UpdatedAt}
		}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := validInteractionRequest()
			tt.mutate(&r)
			if (r.Validate() != nil) != tt.wantErr {
				t.Fatalf("Validate() error mismatch: %v", r.Validate())
			}
		})
	}
}

func TestInteractionRequestTransitions(t *testing.T) {
	now := validInteractionRequest().UpdatedAt
	proposal := &InteractionAnswerAudit{Answer: interactionTestAnswer("squash", 1), Actor: "agent", CreatedAt: now.Add(time.Minute)}
	final := &InteractionAnswerAudit{Answer: interactionTestAnswer("squash", 1), Actor: "owner", CreatedAt: now.Add(2 * time.Minute)}
	tests := []struct {
		name     string
		from, to InteractionState
		prepare  func(*InteractionRequest)
		allowed  bool
	}{
		{"open to discussing", InteractionOpen, InteractionDiscussing, nil, true},
		{"open to proposed", InteractionOpen, InteractionAnswerProposed, func(r *InteractionRequest) { r.Proposal = proposal }, true},
		{"proposed to discussing", InteractionAnswerProposed, InteractionDiscussing, func(r *InteractionRequest) { r.Proposal = proposal }, true},
		{"proposed to resolved", InteractionAnswerProposed, InteractionResolved, func(r *InteractionRequest) { r.Proposal = proposal; r.FinalAnswer = final }, true},
		{"open to withdrawn with evidence", InteractionOpen, InteractionWithdrawn, func(r *InteractionRequest) {
			r.Disposition = &InteractionDispositionAudit{Actor: "orchestrator", Reason: "obsolete", CreatedAt: now.Add(3 * time.Minute)}
		}, true},
		{"resolved terminal", InteractionResolved, InteractionDiscussing, func(r *InteractionRequest) { r.FinalAnswer = final }, false},
		{"withdrawn terminal", InteractionWithdrawn, InteractionOpen, nil, false},
		{"cannot skip backwards", InteractionDiscussing, InteractionOpen, nil, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := validInteractionRequest()
			r.State = tt.from
			if tt.prepare != nil {
				tt.prepare(&r)
			}
			got, err := r.Transition(tt.to, 1, now.Add(3*time.Minute))
			if (err == nil) != tt.allowed {
				t.Fatalf("Transition() error=%v", err)
			}
			if tt.allowed && (got.Revision != 2 || got.State != tt.to) {
				t.Fatalf("unexpected transition result: %+v", got)
			}
		})
	}
}

func TestInteractionRequestRejectsStaleRevision(t *testing.T) {
	r := validInteractionRequest()
	_, err := r.Transition(InteractionDiscussing, 2, r.UpdatedAt.Add(time.Minute))
	if !errors.Is(err, ErrStaleInteractionRevision) {
		t.Fatalf("expected stale revision error, got %v", err)
	}
}

func TestRejectDuplicateUnresolvedInteraction(t *testing.T) {
	existing := validInteractionRequest()
	duplicate := validInteractionRequest()
	duplicate.ID = "ir-2"
	if err := RejectDuplicateUnresolvedInteraction([]InteractionRequest{existing}, duplicate); !errors.Is(err, ErrDuplicateUnresolvedDecision) {
		t.Fatalf("expected duplicate error, got %v", err)
	}
	duplicate.DecisionKey = "release-strategy"
	if err := RejectDuplicateUnresolvedInteraction([]InteractionRequest{existing}, duplicate); err != nil {
		t.Fatalf("unrelated key rejected: %v", err)
	}
	existing.State = InteractionWithdrawn
	duplicate.DecisionKey = existing.DecisionKey
	if err := RejectDuplicateUnresolvedInteraction([]InteractionRequest{existing}, duplicate); err != nil {
		t.Fatalf("terminal request rejected replacement: %v", err)
	}
}

func TestInteractionRequestProjections(t *testing.T) {
	r := validInteractionRequest()
	if !r.BlocksIssue() || !IssueWaitingHuman(r.IssueID, []InteractionRequest{r}) {
		t.Fatal("unresolved request must block issue and project Waiting Human")
	}
	r.State = InteractionSuperseded
	if r.BlocksIssue() || IssueWaitingHuman(r.IssueID, []InteractionRequest{r}) {
		t.Fatal("terminal request must not block or project Waiting Human")
	}
}

func TestProjectInteractionPredicates(t *testing.T) {
	p := ProjectInteractionPredicates{HasUnresolvedRequests: true}
	if !p.Quiescent() {
		t.Fatal("unresolved human interaction should permit quiescence")
	}
	if p.Complete() {
		t.Fatal("unresolved human interaction should prevent completion")
	}
	p.HasUnresolvedRequests = false
	if !p.Complete() {
		t.Fatal("idle project without unresolved interactions should be complete")
	}
	p.HasExecutableWork = true
	if p.Quiescent() || p.Complete() {
		t.Fatal("executable work prevents quiescence and completion")
	}
}

func TestInteractionStalenessReminderIsIdempotentAndNeverResolves(t *testing.T) {
	r := validInteractionRequest()
	policy := InteractionStalenessPolicy{StaleAfter: time.Hour, ReminderInterval: 30 * time.Minute}
	now := r.CreatedAt.Add(2 * time.Hour)
	next, marked, reminded, err := r.ReconcileStaleness(now, policy)
	if err != nil {
		t.Fatal(err)
	}
	if !marked || !reminded || next.State != InteractionOpen || !next.Unresolved() || next.Revision != r.Revision+1 || len(next.Reminders) != 1 {
		t.Fatalf("first reconcile = %+v marked=%t reminded=%t", next, marked, reminded)
	}
	again, marked, reminded, err := next.ReconcileStaleness(now.Add(time.Minute), policy)
	if err != nil {
		t.Fatal(err)
	}
	if marked || reminded || again.Revision != next.Revision || len(again.Reminders) != 1 {
		t.Fatalf("idempotent reconcile = %+v marked=%t reminded=%t", again, marked, reminded)
	}
	third, marked, reminded, err := again.ReconcileStaleness(now.Add(31*time.Minute), policy)
	if err != nil {
		t.Fatal(err)
	}
	if marked || !reminded || third.Revision != again.Revision+1 || len(third.Reminders) != 2 || third.State != InteractionOpen {
		t.Fatalf("second reminder = %+v marked=%t reminded=%t", third, marked, reminded)
	}
	view := third.AgeView(now.Add(31*time.Minute), policy)
	if !view.Stale || view.AgeSeconds <= 0 || view.NextReminderAt == nil {
		t.Fatalf("age view = %+v", view)
	}
}

func TestInteractionDispositionAndRecoveryAudit(t *testing.T) {
	r := validInteractionRequest()
	recovered, err := r.Recover("orchestrator", "session-new", r.Revision, r.UpdatedAt.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if recovered.SessionID != "session-new" || recovered.Recovery == nil || recovered.State != InteractionOpen {
		t.Fatalf("recovered = %+v", recovered)
	}
	at := recovered.UpdatedAt.Add(time.Minute)
	recovered.Disposition = &InteractionDispositionAudit{Actor: "orchestrator", Reason: "replaced by clearer question", ReplacementID: "req-2", CreatedAt: at}
	superseded, err := recovered.Transition(InteractionSuperseded, recovered.Revision, at)
	if err != nil {
		t.Fatal(err)
	}
	if superseded.Unresolved() || superseded.FinalAnswer != nil || superseded.Disposition.ReplacementID != "req-2" {
		t.Fatalf("superseded = %+v", superseded)
	}
}

func interactionTestAnswer(selected string, revision int64) InteractionAnswerPayload {
	return InteractionAnswerPayload{
		SelectedOption: selected, Rationale: "Because it is safest.",
		Constraints: []string{"preserve history"}, SignificanceRecommendation: InteractionSignificanceMaterial,
		Revision: revision,
	}
}
