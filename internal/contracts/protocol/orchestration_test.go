package protocol

import (
	"encoding/json"
	"testing"

	"github.com/riordanpawley/azedarach/internal/domain"
)

func TestProtocolV42PreservesCombinedOrchestrationViewProjectionAndLearningContracts(t *testing.T) {
	if CurrentVersion != 49 {
		t.Fatalf("protocol version = %d, want 49", CurrentVersion)
	}
	if CommandOrchestratorSessionStart == "" || CommandOrchestratorSessionAttach == "" || CommandOrchestratorSessionStop == "" || CommandOrchestratorSessionStatus == "" || EventOrchestrationLoopUpdated == "" || EventBoardViewChanged == "" {
		t.Fatal("combined orchestration and board-view protocol contracts must remain registered")
	}

	snapshot := OrchestrationSnapshot{
		Role:                 "orchestrator",
		SessionID:            "az-root",
		Lifecycle:            domain.OrchestratorWorking,
		Scope:                domain.ProjectOrchestrationScope(),
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
	for _, key := range []string{"role", "session_id", "lifecycle", "cursor", "continuation_required", "completion", "review_queue", "candidates"} {
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
