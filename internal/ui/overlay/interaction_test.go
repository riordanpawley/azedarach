package overlay

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/riordanpawley/azedarach/internal/domain"
)

func TestInteractionOverlayDirectResolveCarriesDisplayedRevision(t *testing.T) {
	r, age := goldenInteractionRequest()
	o := NewInteractionOverlay(r, age)
	_, _ = o.Update(tea.KeyMsg{Type: tea.KeyDown})
	_, _ = o.Update(tea.KeyMsg{Type: tea.KeyEnter})
	for _, ch := range "Use direct after maintenance window" {
		_, _ = o.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{ch}})
	}
	_, cmd := o.Update(tea.KeyMsg{Type: tea.KeyCtrlS})
	if cmd != nil || !o.confirming {
		t.Fatal("material resolution must require significance confirmation")
	}
	_, cmd = o.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
	if cmd == nil {
		t.Fatal("expected resolve command")
	}
	selection := cmd().(SelectionMsg)
	action := selection.Value.(InteractionAction)
	if action.Kind != "resolve" || action.Answer.SelectedOption != "direct" || action.Answer.Revision != 3 {
		t.Fatalf("unexpected action: %+v", action)
	}
}

func TestInteractionOverlayProposalCanBeEdited(t *testing.T) {
	r, age := goldenInteractionRequest()
	r.State = domain.InteractionAnswerProposed
	r.Proposal = &domain.InteractionAnswerAudit{Answer: domain.InteractionAnswerPayload{SelectedOption: "gradual", Rationale: "Original", Revision: r.Revision}}
	o := NewInteractionOverlay(r, age)
	_, _ = o.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'e'}})
	if !o.editing || strings.TrimSpace(o.editor.Value()) != "Original" {
		t.Fatalf("proposal was not loaded for editing: editing=%v value=%q", o.editing, o.editor.Value())
	}
}

func TestInteractionOverlayDoesNotImplicitlyApproveProposalEffects(t *testing.T) {
	r, age := goldenInteractionRequest()
	r.Significance = domain.InteractionSignificanceRoutine
	design := "unsafe implicit edit"
	r.Proposal = &domain.InteractionAnswerAudit{Answer: domain.InteractionAnswerPayload{SelectedOption: "gradual", Rationale: "Proposal", SignificanceRecommendation: domain.InteractionSignificanceMaterial, ApprovedIssueFieldEffects: domain.InteractionIssueFieldEffects{Design: &design}, ApprovedRequirementEffects: []domain.InteractionRequirementEffect{{RequirementID: "req-1", Description: &design}}, ApprovedDecisionEffect: &domain.InteractionDecisionEffect{Title: "Decision"}, Revision: r.Revision}}
	o := NewInteractionOverlay(r, age)
	answer := o.answer()
	if answer.ApprovedIssueFieldEffects.Any() || len(answer.ApprovedRequirementEffects) != 0 || answer.ApprovedDecisionEffect != nil {
		t.Fatalf("AI proposal effects must require separate explicit human approval: %+v", answer)
	}
	if answer.SignificanceRecommendation != domain.InteractionSignificanceMaterial {
		t.Fatalf("proposal significance = %q, want material", answer.SignificanceRecommendation)
	}
	_, _ = o.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'e'}})
	_, cmd := o.Update(tea.KeyMsg{Type: tea.KeyCtrlS})
	if cmd != nil || !o.confirming || o.confirmAction != "resolve" {
		t.Fatal("material proposal on routine request must require confirmation")
	}
}

func TestInteractionOverlayWithdrawRequiresConfirmation(t *testing.T) {
	r, age := goldenInteractionRequest()
	o := NewInteractionOverlay(r, age)
	_, cmd := o.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'w'}})
	if cmd != nil || !o.confirming || o.confirmAction != "withdraw" {
		t.Fatal("withdraw must require confirmation")
	}
	_, cmd = o.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
	action := cmd().(SelectionMsg).Value.(InteractionAction)
	if action.Kind != "withdraw" {
		t.Fatalf("action = %q", action.Kind)
	}
}

func TestInteractionOverlayMaterialDirectAnswerRequiresConfirmation(t *testing.T) {
	r, age := goldenInteractionRequest()
	o := NewInteractionOverlay(r, age)
	_, _ = o.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
	for _, ch := range "Human answer" {
		_, _ = o.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{ch}})
	}
	_, cmd := o.Update(tea.KeyMsg{Type: tea.KeyCtrlS})
	if cmd != nil || !o.confirming || o.confirmAction != "answer" {
		t.Fatal("material direct answer must require significance confirmation")
	}
}

func TestInteractionOverlayFitsDefaultAndNarrowViewports(t *testing.T) {
	r, age := goldenInteractionRequest()
	o := NewInteractionOverlay(r, age)
	for _, size := range []tea.WindowSizeMsg{{Width: 120, Height: 34}, {Width: 72, Height: 22}} {
		_, _ = o.Update(size)
		w, h := o.Size()
		if w > size.Width || h > size.Height {
			t.Fatalf("size %dx%d exceeds viewport %dx%d", w, h, size.Width, size.Height)
		}
	}
}

func TestInteractionOverlayRequestIDIsInteractionIdentity(t *testing.T) {
	r, age := goldenInteractionRequest()
	o := NewInteractionOverlay(r, age)
	if got := o.RequestID(); got != "int-7" {
		t.Fatalf("RequestID() = %q, want interaction request ID int-7 (not issue ID %s)", got, r.IssueID)
	}
}

func TestInteractionOverlayRecoveryOnlyEmitsForStaleRequest(t *testing.T) {
	r, _ := goldenInteractionRequest()
	fresh := NewInteractionOverlay(r, domain.InteractionAgeView{Stale: false})
	_, cmd := fresh.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
	if cmd != nil {
		t.Fatal("fresh request must not emit recovery action")
	}

	stale := NewInteractionOverlay(r, domain.InteractionAgeView{Stale: true})
	_, cmd = stale.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
	if cmd == nil {
		t.Fatal("stale request must emit recovery action")
	}
	action := cmd().(SelectionMsg).Value.(InteractionAction)
	if action.Kind != "recover" || action.Request.ID != r.ID || action.Request.Revision != r.Revision {
		t.Fatalf("unexpected recovery action: %+v", action)
	}
}
