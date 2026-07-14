package domain

import (
	"fmt"
	"sort"
	"strings"
)

const (
	DecisionAcknowledgementReconciled = "reconciled"
	DecisionAcknowledgementCompatible = "compatible"
)

// PendingDecisionChange is the latest integration-affecting decision revision
// that an issue has not yet explicitly reconciled or proved compatible with.
type PendingDecisionChange struct {
	DecisionID      string `json:"decision_id" msgpack:"decision_id"`
	Revision        int64  `json:"revision" msgpack:"revision"`
	Title           string `json:"title,omitempty" msgpack:"title,omitempty"`
	Reason          string `json:"reason,omitempty" msgpack:"reason,omitempty"`
	SourceIssueID   string `json:"source_issue_id,omitempty" msgpack:"source_issue_id,omitempty"`
	Supersedes      string `json:"supersedes,omitempty" msgpack:"supersedes,omitempty"`
	RequiredAction  string `json:"required_action" msgpack:"required_action"`
	ChangeEventID   int64  `json:"change_event_id" msgpack:"change_event_id"`
	Acknowledgement string `json:"acknowledgement,omitempty" msgpack:"acknowledgement,omitempty"`
}

// ReducePendingDecisionChanges derives acknowledgement state solely from the
// durable issue observation stream. Exact revisions matter: acknowledging an
// older revision cannot accidentally accept a later change, and a replacement
// decision removes the superseded decision from the pending set.
func ReducePendingDecisionChanges(events []IssueObservationEvent) []PendingDecisionChange {
	ordered := append([]IssueObservationEvent(nil), events...)
	sort.SliceStable(ordered, func(i, j int) bool {
		return ordered[i].ID < ordered[j].ID
	})

	latest := make(map[string]PendingDecisionChange)
	acknowledged := make(map[string]map[int64]string)
	for _, event := range ordered {
		switch event.Type {
		case IssueEventDecisionChanged:
			decisionID := decisionPayloadString(event.Payload, "decision_id")
			revision := decisionPayloadInt64(event.Payload, "revision")
			if decisionID == "" || revision <= 0 {
				continue
			}
			if decisionPayloadBool(event.Payload, "withdrawn") {
				if current, exists := latest[decisionID]; exists && current.Revision > revision {
					continue
				}
				delete(latest, decisionID)
				continue
			}
			current, exists := latest[decisionID]
			if exists && current.Revision > revision {
				continue
			}
			change := PendingDecisionChange{
				DecisionID:     decisionID,
				Revision:       revision,
				Title:          decisionPayloadString(event.Payload, "title"),
				Reason:         decisionPayloadString(event.Payload, "reason"),
				SourceIssueID:  decisionPayloadString(event.Payload, "source_issue_id"),
				Supersedes:     decisionPayloadString(event.Payload, "supersedes_decision_id"),
				RequiredAction: fmt.Sprintf("acknowledge with `az decision acknowledge --issue %s --id %s --revision %d --disposition reconciled|compatible`", event.IssueID, decisionID, revision),
				ChangeEventID:  event.ID,
			}
			latest[decisionID] = change
		case IssueEventDecisionAcknowledged:
			if strings.TrimSpace(event.Source) != "daemon-decision" || strings.TrimSpace(event.SourceCommand) != "decision.acknowledge" {
				continue
			}
			decisionID := decisionPayloadString(event.Payload, "decision_id")
			revision := decisionPayloadInt64(event.Payload, "revision")
			disposition := decisionPayloadString(event.Payload, "disposition")
			if decisionID == "" || revision <= 0 || !ValidDecisionAcknowledgementDisposition(disposition) {
				continue
			}
			if acknowledged[decisionID] == nil {
				acknowledged[decisionID] = make(map[int64]string)
			}
			acknowledged[decisionID][revision] = disposition
		}
	}

	superseded := make(map[string]struct{})
	for _, change := range latest {
		if change.Supersedes != "" {
			superseded[change.Supersedes] = struct{}{}
		}
	}
	pending := make([]PendingDecisionChange, 0, len(latest))
	for decisionID, change := range latest {
		if _, replaced := superseded[decisionID]; replaced {
			continue
		}
		if disposition := acknowledged[decisionID][change.Revision]; disposition != "" {
			continue
		}
		pending = append(pending, change)
	}
	sort.Slice(pending, func(i, j int) bool {
		if pending[i].ChangeEventID != pending[j].ChangeEventID {
			return pending[i].ChangeEventID < pending[j].ChangeEventID
		}
		return pending[i].DecisionID < pending[j].DecisionID
	})
	return pending
}

func ValidDecisionAcknowledgementDisposition(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case DecisionAcknowledgementReconciled, DecisionAcknowledgementCompatible:
		return true
	default:
		return false
	}
}

func decisionPayloadString(payload map[string]any, key string) string {
	value, _ := payload[key].(string)
	return strings.TrimSpace(value)
}

func decisionPayloadInt64(payload map[string]any, key string) int64 {
	switch value := payload[key].(type) {
	case int:
		return int64(value)
	case int64:
		return value
	case float64:
		return int64(value)
	case float32:
		return int64(value)
	default:
		return 0
	}
}

func decisionPayloadBool(payload map[string]any, key string) bool {
	value, _ := payload[key].(bool)
	return value
}
