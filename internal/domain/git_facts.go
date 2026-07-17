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
func DeriveGitFactsObservation(hasWorktree, hasStatus bool, observedAt, now time.Time, staleAfter time.Duration) GitFactsObservation {
	if !hasWorktree {
		return GitFactsObservation{Availability: GitFactsUnavailable, Reason: "worktree_unavailable"}
	}
	if !hasStatus {
		return GitFactsObservation{Availability: GitFactsUnavailable, Reason: "git_status_not_observed"}
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
