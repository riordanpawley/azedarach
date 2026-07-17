package domain

import "time"

// GitFactsAvailability describes the freshness of disposable Git observation
// fields attached to a durable task projection.
type GitFactsAvailability string

const (
	GitFactsAvailable   GitFactsAvailability = "available"
	GitFactsStale       GitFactsAvailability = "stale"
	GitFactsPartial     GitFactsAvailability = "partial"
	GitFactsUnavailable GitFactsAvailability = "unavailable"
)

const DefaultGitFactsStaleAfter = 30 * time.Second

// GitFactsStatusState describes whether the projected Git status payload can
// supply facts. An observation timestamp alone is not evidence that a payload
// exists or decoded successfully.
type GitFactsStatusState uint8

const (
	GitFactsStatusMissing GitFactsStatusState = iota
	GitFactsStatusValid
	GitFactsStatusInvalid
)

// GitFactsObservation makes degraded external Git state explicit without
// withholding the durable task snapshot.
type GitFactsObservation struct {
	Availability GitFactsAvailability `json:"availability" msgpack:"availability"`
	ObservedAt   *time.Time           `json:"observed_at,omitempty" msgpack:"observed_at,omitempty"`
	Reason       string               `json:"reason,omitempty" msgpack:"reason,omitempty"`
}

func (o GitFactsObservation) IsZero() bool {
	return o.Availability == "" && o.ObservedAt == nil && o.Reason == ""
}

// DeriveGitFactsObservation classifies already-projected Git facts. It never
// performs external observation itself.
func DeriveGitFactsObservation(hasWorktree bool, statusState GitFactsStatusState, observedAt, now time.Time, staleAfter time.Duration) GitFactsObservation {
	if !hasWorktree {
		return GitFactsObservation{Availability: GitFactsUnavailable, Reason: "worktree_unavailable"}
	}
	if statusState == GitFactsStatusMissing {
		return GitFactsObservation{Availability: GitFactsUnavailable, Reason: "git_status_not_observed"}
	}
	if statusState == GitFactsStatusInvalid {
		return GitFactsObservation{Availability: GitFactsPartial, Reason: "git_status_invalid"}
	}
	if observedAt.IsZero() {
		return GitFactsObservation{Availability: GitFactsPartial, Reason: "observation_time_unavailable"}
	}
	observedAt = observedAt.UTC()
	observation := GitFactsObservation{Availability: GitFactsAvailable, ObservedAt: &observedAt}
	if staleAfter > 0 && now.UTC().Sub(observedAt) > staleAfter {
		observation.Availability = GitFactsStale
		observation.Reason = "observation_stale"
	}
	return observation
}
