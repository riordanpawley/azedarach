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

func interactionTestAnswer(selected string, revision int64) InteractionAnswerPayload {
	return InteractionAnswerPayload{
		SelectedOption: selected, Rationale: "Because it is safest.",
		Constraints: []string{"preserve history"}, SignificanceRecommendation: InteractionSignificanceMaterial,
		Revision: revision,
	}
}
