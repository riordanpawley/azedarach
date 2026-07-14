package domain

import (
	"fmt"
	"strings"

	"github.com/riordanpawley/azedarach/internal/naming"
)

// ReviewRestartOperationState is the durable pending state captured when a
// review return submits an asynchronous worker restart.
type ReviewRestartOperationState string

const (
	ReviewRestartOperationQueued  ReviewRestartOperationState = "queued"
	ReviewRestartOperationRunning ReviewRestartOperationState = "running"
)

// ReviewRestartSubmission binds one review-return intent to its durable
// session.start operation so replay can reconcile outside the review queue.
type ReviewRestartSubmission struct {
	OperationID naming.OperationID
	State       ReviewRestartOperationState
	SessionID   string
	ActorID     string
}

// TrustedReviewRestartSubmission validates daemon provenance and decodes the
// typed operation relation from a restart_submitted review event.
func TrustedReviewRestartSubmission(event IssueObservationEvent) (ReviewRestartSubmission, bool, error) {
	if event.Type != IssueEventReviewCompleted || strings.TrimSpace(event.Source) != "daemon-orchestration" || strings.TrimSpace(event.SourceCommand) != "review-return" {
		return ReviewRestartSubmission{}, false, nil
	}
	if strings.TrimSpace(stringValue(event.Payload["outcome"])) != "restart_submitted" {
		return ReviewRestartSubmission{}, false, nil
	}
	actorID := strings.TrimSpace(stringValue(event.Payload["actor_id"]))
	if actorID == "" {
		return ReviewRestartSubmission{}, false, fmt.Errorf("restart_submitted review event requires actor_id")
	}
	operationID, err := naming.ParseOperationID(event.OperationID)
	if err != nil {
		return ReviewRestartSubmission{}, false, fmt.Errorf("restart_submitted review event requires operation_id: %w", err)
	}
	state := ReviewRestartOperationState(strings.TrimSpace(stringValue(event.Payload["operation_state"])))
	if state != ReviewRestartOperationQueued && state != ReviewRestartOperationRunning {
		return ReviewRestartSubmission{}, false, fmt.Errorf("restart_submitted review operation %s has invalid pending state %q", operationID, state)
	}
	return ReviewRestartSubmission{OperationID: operationID, State: state, SessionID: strings.TrimSpace(event.SessionID), ActorID: actorID}, true, nil
}
