package domain

import (
	"sort"
	"strings"
)

// ReviewReadyPublication identifies the durable observation event that owns
// one review-ready episode. A review return ends the episode; a later
// resubmission starts a new one and therefore receives a new source event ID.
type ReviewReadyPublication struct {
	SourceEvent IssueObservationEvent
}

// DeriveReviewReadyPublications reduces an issue's ordered observation stream
// to exactly one publication per review-ready episode. Integration-ready
// evidence immediately before an in_review transition is retained as the
// source; evidence immediately after the transition is part of the same
// episode and cannot create a duplicate publication.
func DeriveReviewReadyPublications(events []IssueObservationEvent) []ReviewReadyPublication {
	ordered := append([]IssueObservationEvent(nil), events...)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].ID < ordered[j].ID })

	inReview := false
	episodePublished := false
	var pendingEvidence *IssueObservationEvent
	publications := make([]ReviewReadyPublication, 0)
	for i := range ordered {
		event := ordered[i]
		if IsReviewReadyEvidenceEvent(event) {
			if inReview {
				if !episodePublished {
					publications = append(publications, ReviewReadyPublication{SourceEvent: event})
					episodePublished = true
				}
			} else {
				copy := event
				pendingEvidence = &copy
			}
			continue
		}
		if event.Type != IssueEventIssueStatusChanged {
			continue
		}
		toStatus := strings.ToLower(strings.TrimSpace(payloadString(event.Payload, "to_status")))
		if toStatus == string(StatusInReview) {
			if inReview {
				continue
			}
			inReview = true
			episodePublished = true
			source := event
			if pendingEvidence != nil {
				source = *pendingEvidence
			}
			publications = append(publications, ReviewReadyPublication{SourceEvent: source})
			pendingEvidence = nil
			continue
		}
		if inReview {
			inReview = false
			episodePublished = false
		}
		pendingEvidence = nil
	}
	return publications
}

// IsReviewReadyEvidenceEvent accepts the durable event spellings emitted by
// workers over time and structured evidence packets used by current clients.
func IsReviewReadyEvidenceEvent(event IssueObservationEvent) bool {
	normalized := strings.NewReplacer("_", ".", "-", ".").Replace(strings.ToLower(strings.TrimSpace(string(event.Type))))
	switch normalized {
	case "worker.integration.ready", "worker.ready", "worker.complete":
		return true
	case string(IssueEventEvidenceSubmitted):
		return strings.EqualFold(strings.TrimSpace(payloadString(event.Payload, "schema")), WorkerEvidenceSchemaV1)
	default:
		return false
	}
}

func payloadString(payload map[string]any, key string) string {
	if payload == nil {
		return ""
	}
	value, _ := payload[key].(string)
	return value
}
