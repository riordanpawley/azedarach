package protocol

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/vmihailenco/msgpack/v5"
)

func TestInteractionStructuredAnswerWireContractRoundTrip(t *testing.T) {
	title := "Approved title"
	priority := 1
	want := InteractionResolveRequestBody{InteractionMutationRequestBody: InteractionMutationRequestBody{
		ID: "request-1", ExpectedRevision: 7, Actor: "human:owner",
		Answer: domain.InteractionAnswerPayload{
			SelectedOption: "ship", Rationale: "The constraints are satisfied.",
			Constraints:                []string{"preserve audit history"},
			ApprovedIssueFieldEffects:  domain.InteractionIssueFieldEffects{Title: &title, Priority: &priority},
			SignificanceRecommendation: domain.InteractionSignificanceMaterial, Revision: 7,
		},
	}}

	raw, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"selected_option", "rationale", "constraints", "approved_issue_field_effects", "significance_recommendation", "revision"} {
		if !strings.Contains(string(raw), `"`+field+`"`) {
			t.Fatalf("JSON omitted %q: %s", field, raw)
		}
	}
	var jsonGot InteractionResolveRequestBody
	if err := json.Unmarshal(raw, &jsonGot); err != nil {
		t.Fatal(err)
	}
	assertInteractionAnswerWireValue(t, jsonGot.Answer, title, priority)

	packed, err := msgpack.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	var msgpackGot InteractionResolveRequestBody
	if err := msgpack.Unmarshal(packed, &msgpackGot); err != nil {
		t.Fatal(err)
	}
	assertInteractionAnswerWireValue(t, msgpackGot.Answer, title, priority)
}

func assertInteractionAnswerWireValue(t *testing.T, got domain.InteractionAnswerPayload, title string, priority int) {
	t.Helper()
	if got.SelectedOption != "ship" || got.Rationale == "" || got.Revision != 7 || got.SignificanceRecommendation != domain.InteractionSignificanceMaterial {
		t.Fatalf("answer = %+v", got)
	}
	if got.ApprovedIssueFieldEffects.Title == nil || *got.ApprovedIssueFieldEffects.Title != title || got.ApprovedIssueFieldEffects.Priority == nil || *got.ApprovedIssueFieldEffects.Priority != priority {
		t.Fatalf("approved effects = %+v", got.ApprovedIssueFieldEffects)
	}
}
