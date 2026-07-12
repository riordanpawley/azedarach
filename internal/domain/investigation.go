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
			if trustedInvestigationReviewEvent(event) {
				switch strings.TrimSpace(stringValue(event.Payload["outcome"])) {
				case "accepted":
					accepted = true
				case "returned", "integration_failed":
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

func trustedInvestigationReviewEvent(event IssueObservationEvent) bool {
	if event.Type != IssueEventReviewCompleted || strings.TrimSpace(event.Source) != "daemon-orchestration" {
		return false
	}
	switch strings.TrimSpace(event.SourceCommand) {
	case "review-accept", "review-return":
		return strings.TrimSpace(stringValue(event.Payload["actor_id"])) != ""
	default:
		return false
	}
}
