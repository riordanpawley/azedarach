package domain

import "testing"

func TestTrustedReviewRestartSubmissionRequiresDaemonProvenanceAndTypedRelation(t *testing.T) {
	valid := IssueObservationEvent{
		Type: IssueEventReviewCompleted, Source: "daemon-orchestration", SourceCommand: "review-return",
		OperationID: "op-1", SessionID: "session-1",
		Payload: map[string]any{"outcome": "restart_submitted", "actor_id": "orchestrator", "operation_state": "queued"},
	}
	submission, trusted, err := TrustedReviewRestartSubmission(valid)
	if err != nil || !trusted || submission.OperationID != "op-1" || submission.State != ReviewRestartOperationQueued || submission.ActorID != "orchestrator" {
		t.Fatalf("valid submission = %+v trusted=%t err=%v", submission, trusted, err)
	}

	untrusted := valid
	untrusted.Source = "agent"
	if _, trusted, err := TrustedReviewRestartSubmission(untrusted); err != nil || trusted {
		t.Fatalf("untrusted event trusted=%t err=%v", trusted, err)
	}

	malformed := valid
	malformed.Payload = map[string]any{"outcome": "restart_submitted", "actor_id": "orchestrator", "operation_state": "done"}
	if _, trusted, err := TrustedReviewRestartSubmission(malformed); err == nil || trusted {
		t.Fatalf("malformed event trusted=%t err=%v, want typed pending-state error", trusted, err)
	}
}
