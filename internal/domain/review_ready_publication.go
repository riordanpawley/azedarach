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
	Evidence    WorkerEvidencePacket
	Validation  WorkerEvidenceParseResult
}

// ReviewReadyEvidenceReduction is the authoritative reduction of one issue's
// review-status and worker-evidence observations. Publications and acceptance
// consume the same latest-evidence ordering decision.
type ReviewReadyEvidenceReduction struct {
	Publications   []ReviewReadyPublication
	LatestEvidence *ReviewReadyPublication
}

// DeriveReviewReadyPublications reduces an issue's ordered observation stream
// to exactly one publication per review-ready episode. Integration-ready
// evidence immediately before an in_review transition is retained as the
// source; evidence immediately after the transition is part of the same
// episode and cannot create a duplicate publication.
func DeriveReviewReadyPublications(events []IssueObservationEvent) []ReviewReadyPublication {
	return ReduceReviewReadyEvidence(events).Publications
}

// ReduceReviewReadyEvidence orders observations by their durable authority
// time, with the database ID breaking timestamp ties, then derives both replay
// publications and the latest packet inspected by review acceptance.
func ReduceReviewReadyEvidence(events []IssueObservationEvent) ReviewReadyEvidenceReduction {
	ordered := append([]IssueObservationEvent(nil), events...)
	sort.SliceStable(ordered, func(i, j int) bool {
		if ordered[i].ObservedAt.Equal(ordered[j].ObservedAt) {
			return ordered[i].ID < ordered[j].ID
		}
		return ordered[i].ObservedAt.Before(ordered[j].ObservedAt)
	})

	inReview := false
	episodePublished := false
	var pendingEvidence *IssueObservationEvent
	publications := make([]ReviewReadyPublication, 0)
	var latestEvidence *ReviewReadyPublication
	for i := range ordered {
		event := ordered[i]
		packet, validation := ParseWorkerEvidenceIssueEvent(event)
		if IsWorkerEvidenceEventType(event.Type) {
			candidate := ReviewReadyPublication{SourceEvent: event, Evidence: packet, Validation: validation}
			latestEvidence = &candidate
			if inReview {
				if validation.Complete && !episodePublished {
					publications = append(publications, candidate)
					episodePublished = true
				}
			} else if validation.Complete {
				copy := event
				pendingEvidence = &copy
			} else {
				pendingEvidence = nil
			}
			continue
		}
		if IsReviewRequestTransition(event) {
			if inReview {
				continue
			}
			inReview = true
			episodePublished = false
			if pendingEvidence != nil {
				packet, validation := ParseWorkerEvidenceIssueEvent(*pendingEvidence)
				publications = append(publications, ReviewReadyPublication{SourceEvent: *pendingEvidence, Evidence: packet, Validation: validation})
				episodePublished = true
			}
			pendingEvidence = nil
			continue
		}
		if event.Type != IssueEventIssueStatusChanged {
			continue
		}
		if inReview {
			inReview = false
			episodePublished = false
		}
		pendingEvidence = nil
	}
	return ReviewReadyEvidenceReduction{Publications: publications, LatestEvidence: latestEvidence}
}

// IsReviewRequestTransition reports whether an observation starts a durable
// review-request epoch.
func IsReviewRequestTransition(event IssueObservationEvent) bool {
	if event.Type != IssueEventIssueStatusChanged || strings.TrimSpace(event.Source) != "issue-store" {
		return false
	}
	toStatus := strings.ToLower(strings.TrimSpace(payloadString(event.Payload, "to_status")))
	return toStatus == string(StatusInReview)
}

// IsReviewReadyEvidenceEvent accepts the durable event spellings emitted by
// workers over time and structured evidence packets used by current clients.
func IsReviewReadyEvidenceEvent(event IssueObservationEvent) bool {
	_, validation := ParseWorkerEvidenceIssueEvent(event)
	return validation.Complete
}

func payloadString(payload map[string]any, key string) string {
	if payload == nil {
		return ""
	}
	value, _ := payload[key].(string)
	return value
}
