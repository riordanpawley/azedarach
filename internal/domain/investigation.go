package domain

import (
	"sort"
	"strings"
)

// InvestigationDisposition identifies who consumes an investigation's terminal
// findings. Missing declarations deliberately retain the legacy-safe human gate.
type InvestigationDisposition string

const (
	InvestigationDispositionHumanFindings  InvestigationDisposition = "human_findings"
	InvestigationDispositionInternalReview InvestigationDisposition = "internal_review"
)

type InvestigationAcceptance struct {
	Disposition InvestigationDisposition
	Accepted    bool
	Reason      string
}

// ReviewOutcome is a trusted daemon review result that changes durable review authority.
type ReviewOutcome string

const (
	ReviewOutcomeAccepted          ReviewOutcome = "accepted"
	ReviewOutcomeReturned          ReviewOutcome = "returned"
	ReviewOutcomeIntegrationFailed ReviewOutcome = "integration_failed"
)

// EvaluateInvestigationAcceptance derives the terminal acceptance gate from the
// append-only issue evidence projection. The newest declaration wins.
func EvaluateInvestigationAcceptance(task Task, events []IssueObservationEvent) InvestigationAcceptance {
	if task.Type != TypeInvestigation {
		return InvestigationAcceptance{Accepted: true}
	}
	disposition := InvestigationDispositionHumanFindings
	accepted := false
	ordered := append([]IssueObservationEvent(nil), events...)
	sort.SliceStable(ordered, func(i, j int) bool {
		if ordered[i].ID != ordered[j].ID {
			return ordered[i].ID < ordered[j].ID
		}
		return ordered[i].ObservedAt.Before(ordered[j].ObservedAt)
	})
	for _, event := range ordered {
		if event.Type == IssueEventInvestigationDisposition {
			value := InvestigationDisposition(strings.TrimSpace(stringValue(event.Payload["disposition"])))
			if value == InvestigationDispositionHumanFindings || value == InvestigationDispositionInternalReview {
				disposition = value
				accepted = false
			}
			continue
		}
		switch disposition {
		case InvestigationDispositionInternalReview:
			if outcome, trusted := TrustedReviewOutcome(event); trusted {
				switch outcome {
				case ReviewOutcomeAccepted:
					accepted = true
				case ReviewOutcomeReturned, ReviewOutcomeIntegrationFailed:
					accepted = false
				}
			}
		default:
			if event.Type == IssueEventHumanInputProvided {
				if value, ok := event.Payload["investigation_findings_accepted"].(bool); ok && value {
					accepted = true
				}
			}
		}
	}
	if accepted {
		return InvestigationAcceptance{Disposition: disposition, Accepted: true}
	}
	if disposition == InvestigationDispositionInternalReview {
		return InvestigationAcceptance{Disposition: disposition, Reason: "internal review lacks durable accepted reviewer outcome or has unresolved returned findings"}
	}
	return InvestigationAcceptance{Disposition: disposition, Reason: "human-facing investigation lacks explicit issue-specific findings acceptance"}
}

func stringValue(value any) string {
	text, _ := value.(string)
	return text
}

// TrustedReviewOutcome validates and extracts a daemon-authored review result.
func TrustedReviewOutcome(event IssueObservationEvent) (ReviewOutcome, bool) {
	if event.Type != IssueEventReviewCompleted || strings.TrimSpace(event.Source) != "daemon-orchestration" {
		return "", false
	}
	command := strings.TrimSpace(event.SourceCommand)
	if command != "review-accept" && command != "review-return" {
		return "", false
	}
	if strings.TrimSpace(stringValue(event.Payload["actor_id"])) == "" {
		return "", false
	}
	outcome := ReviewOutcome(strings.TrimSpace(stringValue(event.Payload["outcome"])))
	switch {
	case command == "review-accept" && (outcome == ReviewOutcomeAccepted || outcome == ReviewOutcomeIntegrationFailed):
		return outcome, true
	case command == "review-return" && outcome == ReviewOutcomeReturned:
		return outcome, true
	default:
		return "", false
	}
}
