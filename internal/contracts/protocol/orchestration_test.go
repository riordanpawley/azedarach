package protocol

import (
	"encoding/json"
	"testing"

	"github.com/riordanpawley/azedarach/internal/domain"
)

func TestProtocolV61PreservesCombinedOrchestrationViewProjectionDecisionLearningAndTypedMergeContracts(t *testing.T) {
	if CurrentVersion != 61 {
		t.Fatalf("protocol version = %d, want 61", CurrentVersion)
	}
	if CommandOrchestratorSessionStart == "" || CommandOrchestratorSessionAttach == "" || CommandOrchestratorSessionStop == "" || CommandOrchestratorSessionStatus == "" || EventOrchestrationLoopUpdated == "" || EventBoardViewChanged == "" {
		t.Fatal("combined orchestration and board-view protocol contracts must remain registered")
	}

	snapshot := OrchestrationSnapshot{
		Role:                 "orchestrator",
		SessionID:            "az-root",
		Lifecycle:            domain.OrchestratorWorking,
		Scope:                domain.ProjectOrchestrationScope(),
		ProjectionRevision:   41,
		ProjectionAuthority:  OrchestrationProjectionAuthoritySQLite,
		Cursor:               17,
		ContinuationRequired: true,
		Completion:           OrchestrationCompletion{Pass: false, Reasons: []string{"work remains"}},
		ReviewQueue:          []OrchestrationReview{{IssueID: "review", Actionable: true}},
		Candidates: []OrchestrationCandidate{{
			IssueID:       "candidate",
			Executability: domain.IssueExecutabilityAssessment{Executable: true},
		}},
	}
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	var shape map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &shape); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"role", "session_id", "lifecycle", "projection_revision", "projection_authority", "cursor", "continuation_required", "completion", "review_queue", "candidates"} {
		if _, ok := shape[key]; !ok {
			t.Fatalf("combined snapshot omitted %q: %s", key, encoded)
		}
	}

	intent := OrchestrationIntentRequest{
		Scope:         domain.ProjectOrchestrationScope(),
		Kind:          OrchestrationIntentReviewReturn,
		IntentKey:     "review:return:1",
		Findings:      []OrchestrationReviewFinding{{Severity: "blocking", Finding: "repair"}},
		RestartWorker: true,
		Routes:        []domain.OrchestrationCandidateRoute{{IssueID: "candidate"}},
	}
	encoded, err = json.Marshal(intent)
	if err != nil {
		t.Fatal(err)
	}
	shape = map[string]json.RawMessage{}
	if err := json.Unmarshal(encoded, &shape); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"findings", "restart_worker", "routes"} {
		if _, ok := shape[key]; !ok {
			t.Fatalf("combined intent omitted %q: %s", key, encoded)
		}
	}
	pending := OrchestrationPending{IssueID: "candidate", Phase: "operations_store", Message: "queued", Retryable: true}
	encoded, err = json.Marshal(pending)
	if err != nil {
		t.Fatal(err)
	}
	shape = map[string]json.RawMessage{}
	if err := json.Unmarshal(encoded, &shape); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"phase", "message", "retryable"} {
		if _, ok := shape[key]; !ok {
			t.Fatalf("queued start progress omitted %q: %s", key, encoded)
		}
	}

	stopRequest := OrchestratorSessionRequest{Scope: domain.ProjectOrchestrationScope(), ExpectedSessionID: "az-orchestrator-project"}
	encoded, err = json.Marshal(stopRequest)
	if err != nil {
		t.Fatal(err)
	}
	shape = map[string]json.RawMessage{}
	if err := json.Unmarshal(encoded, &shape); err != nil {
		t.Fatal(err)
	}
	if _, ok := shape["expected_session_id"]; !ok {
		t.Fatalf("orchestrator stop precondition omitted from request: %s", encoded)
	}
}

func TestValidateReturnedReviewPassRejectsIncompleteProtocolPayloads(t *testing.T) {
	broaderInvalidation := false
	valid := OrchestrationReviewPass{
		Verdict: "returned", Angle: "authority review", ReusedLayers: []string{},
		Matrix:             domain.WorkerEvidenceReviewMatrix{Type: "stateful", CoveredCells: []string{"authorization"}},
		AffectedInvariants: []string{"orchestration.project_review"}, BroaderInvalidation: &broaderInvalidation,
	}
	if err := ValidateReturnedReviewPass(valid); err != nil {
		t.Fatalf("valid review pass: %v", err)
	}
	tests := []struct {
		name   string
		mutate func(*OrchestrationReviewPass)
	}{
		{name: "empty verdict", mutate: func(pass *OrchestrationReviewPass) { pass.Verdict = "" }},
		{name: "empty angle", mutate: func(pass *OrchestrationReviewPass) { pass.Angle = "" }},
		{name: "omitted reused layers", mutate: func(pass *OrchestrationReviewPass) { pass.ReusedLayers = nil }},
		{name: "empty matrix type", mutate: func(pass *OrchestrationReviewPass) { pass.Matrix.Type = "" }},
		{name: "empty matrix coverage", mutate: func(pass *OrchestrationReviewPass) { pass.Matrix.CoveredCells = nil }},
		{name: "blank covered cell", mutate: func(pass *OrchestrationReviewPass) { pass.Matrix.CoveredCells = []string{" "} }},
		{name: "duplicate matrix cell", mutate: func(pass *OrchestrationReviewPass) {
			pass.Matrix.SkippedCells = []domain.WorkerEvidenceReviewSkippedMatrix{{Cell: "AUTHORIZATION", Reason: "covered"}}
		}},
		{name: "skipped cell without reason", mutate: func(pass *OrchestrationReviewPass) {
			pass.Matrix.CoveredCells = nil
			pass.Matrix.SkippedCells = []domain.WorkerEvidenceReviewSkippedMatrix{{Cell: "recovery"}}
		}},
		{name: "blank reused layer", mutate: func(pass *OrchestrationReviewPass) { pass.ReusedLayers = []string{" "} }},
		{name: "duplicate reused layer", mutate: func(pass *OrchestrationReviewPass) { pass.ReusedLayers = []string{"migration", "MIGRATION"} }},
		{name: "duplicate invariant", mutate: func(pass *OrchestrationReviewPass) {
			pass.AffectedInvariants = []string{"orchestration.project_review", "orchestration.project_review"}
		}},
		{name: "missing judgment", mutate: func(pass *OrchestrationReviewPass) { pass.BroaderInvalidation = nil }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			invalid := valid
			tt.mutate(&invalid)
			if err := ValidateReturnedReviewPass(invalid); err == nil {
				t.Fatal("incomplete review pass succeeded")
			}
		})
	}
}

func TestOrchestrationReviewPassPreservesBroaderInvalidationPresence(t *testing.T) {
	broaderInvalidation := false
	encoded, err := json.Marshal(OrchestrationReviewPass{BroaderInvalidation: &broaderInvalidation})
	if err != nil {
		t.Fatal(err)
	}
	var shape map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &shape); err != nil {
		t.Fatal(err)
	}
	if got := string(shape["broader_invalidation"]); got != "false" {
		t.Fatalf("broader_invalidation = %s, want explicit false", got)
	}
	var omitted OrchestrationReviewPass
	if err := json.Unmarshal([]byte(`{}`), &omitted); err != nil {
		t.Fatal(err)
	}
	if omitted.BroaderInvalidation != nil {
		t.Fatalf("omitted broader_invalidation = %v, want nil", *omitted.BroaderInvalidation)
	}
}
