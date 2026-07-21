package domain

import (
	"fmt"
	"strings"
	"time"

	"github.com/riordanpawley/azedarach/internal/naming"
)

// IssueObservationEventType identifies durable issue-scoped observation facts.
type IssueObservationEventType string

const (
	IssueEventIssueCreated                        IssueObservationEventType = "issue.created"
	IssueEventIssueStatusChanged                  IssueObservationEventType = "issue.status_changed"
	IssueEventIssueDetailsChanged                 IssueObservationEventType = "issue.details_changed"
	IssueEventIssueNotesAppended                  IssueObservationEventType = "issue.notes_appended"
	IssueEventIssueDependencyAdded                IssueObservationEventType = "issue.dependency_added"
	IssueEventIssueDependencyRemoved              IssueObservationEventType = "issue.dependency_removed"
	IssueEventIssueOwnershipChanged               IssueObservationEventType = "issue.ownership_changed"
	IssueEventIssueArchived                       IssueObservationEventType = "issue.archived"
	IssueEventIssueUnarchived                     IssueObservationEventType = "issue.unarchived"
	IssueEventIssueDeleted                        IssueObservationEventType = "issue.deleted"
	IssueEventProgressRecorded                    IssueObservationEventType = "progress.recorded"
	IssueEventFollowupCreated                     IssueObservationEventType = "follow_up.created"
	IssueEventSessionLifecycleChanged             IssueObservationEventType = "session.lifecycle_changed"
	IssueEventAgentActivityChanged                IssueObservationEventType = "agent.activity_changed"
	IssueEventWorktreeGitChanged                  IssueObservationEventType = "worktree.git_changed"
	IssueEventCommandStarted                      IssueObservationEventType = "command.started"
	IssueEventCommandFinished                     IssueObservationEventType = "command.finished"
	IssueEventValidationPassed                    IssueObservationEventType = "validation.passed"
	IssueEventValidationFailed                    IssueObservationEventType = "validation.failed"
	IssueEventEvidenceSubmitted                   IssueObservationEventType = "evidence.submitted"
	IssueEventReviewCompleted                     IssueObservationEventType = "review.completed"
	IssueEventReviewPromptBound                   IssueObservationEventType = "review.prompt_bound"
	IssueEventReviewCloseFailed                   IssueObservationEventType = "review.close_failed"
	IssueEventTaskIntegrationCompleted            IssueObservationEventType = "task.integration_completed"
	IssueEventTaskIntegrationHistoricalAuthorized IssueObservationEventType = "task.integration_historical_authorized"
	IssueEventRiskRecorded                        IssueObservationEventType = "risk.recorded"
	IssueEventBlockerReported                     IssueObservationEventType = "blocker.reported"
	IssueEventHumanInputRequested                 IssueObservationEventType = "human.input_requested"
	IssueEventHumanInputProvided                  IssueObservationEventType = "human.input_provided"
	IssueEventInvestigationDisposition            IssueObservationEventType = "investigation.disposition_declared"
	IssueEventOrchestrationRouted                 IssueObservationEventType = "orchestration.candidate_routed"
	IssueEventDecisionChanged                     IssueObservationEventType = "decision.changed"
	IssueEventDecisionAcknowledged                IssueObservationEventType = "decision.acknowledged"
	// These legacy evidence types predate daemon-owned review publication.
	// They remain readable only for the fail-closed historical integration
	// recovery path; new review acceptance uses IssueEventReviewCompleted.
	IssueEventHistoricalReviewAccepted      IssueObservationEventType = "review.accepted"
	IssueEventHistoricalReviewReturned      IssueObservationEventType = "review.returned"
	IssueEventHistoricalValidationCompleted IssueObservationEventType = "validation.completed"
)

// IssueObservationEvent is an append-only fact associated with one issue.
type IssueObservationEvent struct {
	ID            int64                     `json:"id"`
	IssueID       naming.IssueID            `json:"issue_id"`
	Type          IssueObservationEventType `json:"event_type"`
	ObservedAt    time.Time                 `json:"observed_at"`
	Source        string                    `json:"source,omitempty"`
	SourceCommand string                    `json:"source_command,omitempty"`
	OperationID   string                    `json:"operation_id,omitempty"`
	SessionID     string                    `json:"session_id,omitempty"`
	WorktreePath  string                    `json:"worktree_path,omitempty"`
	Payload       map[string]any            `json:"payload,omitempty"`
}

// IssueObservationEventSearchFields returns the human-facing payload surface
// shared by the durable FTS projection and the final semantic search filter.
func IssueObservationEventSearchFields(event IssueObservationEvent) []string {
	fields := make([]string, 0, 5)
	for _, key := range []string{"summary", "body", "message", "line", "evidence"} {
		if value, ok := event.Payload[key]; ok {
			text := strings.TrimSpace(fmt.Sprint(value))
			if text != "" {
				fields = append(fields, text)
			}
		}
	}
	return fields
}

// IssueObservationEventMatchesQuery applies the canonical content-query term
// semantics after FTS has selected a bounded candidate set.
func IssueObservationEventMatchesQuery(event IssueObservationEvent, query string) bool {
	wanted := ContentQueryTerms(query)
	if len(wanted) == 0 {
		return true
	}
	available := make(map[string]struct{})
	for _, field := range IssueObservationEventSearchFields(event) {
		for _, term := range ContentQueryTerms(field) {
			available[term] = struct{}{}
		}
	}
	for _, term := range wanted {
		if _, ok := available[term]; !ok {
			return false
		}
	}
	return true
}

// IssueObservationEventTypeRequiresAuthority reports event types that may only
// be emitted by the store or daemon operation that owns the corresponding
// durable transition. Generic issue-record appenders must reject them.
func IssueObservationEventTypeRequiresAuthority(eventType IssueObservationEventType) bool {
	switch eventType {
	case IssueEventIssueCreated,
		IssueEventIssueStatusChanged,
		IssueEventIssueDetailsChanged,
		IssueEventIssueNotesAppended,
		IssueEventIssueDependencyAdded,
		IssueEventIssueDependencyRemoved,
		IssueEventIssueOwnershipChanged,
		IssueEventIssueArchived,
		IssueEventIssueUnarchived,
		IssueEventIssueDeleted,
		IssueEventReviewCompleted,
		IssueEventReviewPromptBound,
		IssueEventReviewCloseFailed,
		IssueEventTaskIntegrationCompleted,
		IssueEventTaskIntegrationHistoricalAuthorized,
		IssueEventOrchestrationRouted,
		IssueEventDecisionChanged,
		IssueEventDecisionAcknowledged:
		return true
	default:
		return false
	}
}
