package learningactivation

import (
	"context"
	"testing"

	"github.com/riordanpawley/azedarach/internal/services/issues"
)

type fakeStore struct {
	rows       []issues.Learning
	suppressed map[string]struct{}
	proposed   issues.RecordLearningActivationParams
}

func (f *fakeStore) ListLearnings(context.Context, issues.LearningFilter) ([]issues.Learning, error) {
	return f.rows, nil
}
func (f *fakeStore) DeliveredLearningIDs(context.Context, string, string) (map[string]struct{}, error) {
	return f.suppressed, nil
}
func (f *fakeStore) ProposeLearningActivation(_ context.Context, p issues.RecordLearningActivationParams) (issues.LearningActivationProposal, error) {
	f.proposed = p
	return issues.LearningActivationProposal{ActivationID: "act-1", ProjectID: p.ProjectID, Surface: p.Surface, ContextFingerprint: p.ContextFingerprint, LearningIDs: p.LearningIDs, Explanation: p.Explanation}, nil
}
func (f *fakeStore) ConfirmLearningActivation(context.Context, string, string, int) (issues.LearningActivation, error) {
	return issues.LearningActivation{}, nil
}
func (f *fakeStore) RecordLearningActivationOutcome(context.Context, issues.LearningActivationOutcome) (issues.LearningActivationOutcome, bool, error) {
	return issues.LearningActivationOutcome{}, false, nil
}

func TestLearningActivationServiceOwnsSelectionSuppressionAndSafeContext(t *testing.T) {
	store := &fakeStore{rows: []issues.Learning{{LocalID: "learn-1", Summary: "one", RecallScore: 1, RecallReason: "issue"}, {LocalID: "learn-2", Summary: "two", RecallScore: 2, RecallReason: "query"}}, suppressed: map[string]struct{}{"learn-1": {}}}
	result, err := New(store).Propose(context.Background(), Request{ProjectID: "proj", Purpose: "session_start", Surface: "prime", SessionID: "s", IssueID: "az-1", Query: "safe query", TokenBudget: 100})
	if err != nil {
		t.Fatal(err)
	}
	if result.Activation.ActivationID != "act-1" || len(store.proposed.LearningIDs) != 1 || store.proposed.LearningIDs[0] != "learn-2" {
		t.Fatalf("proposal=%+v persisted=%+v", result, store.proposed)
	}
	if store.proposed.ContextFingerprint == "" || store.proposed.ContextFingerprint == "safe query" {
		t.Fatalf("context fingerprint is not privacy safe: %q", store.proposed.ContextFingerprint)
	}
}
