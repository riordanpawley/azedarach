package domain

import "testing"

func TestManagedRuntimeLifecycleCartesianProduct(t *testing.T) {
	dispositions := []IssueDisposition{IssueDispositionBacklog, IssueDispositionReady, IssueDispositionCompleted, IssueDispositionCancelled}
	engagements := []IssueEngagement{IssueEngagementIdle, IssueEngagementWorking, IssueEngagementReviewRequested}
	visibilities := []IssueVisibility{IssueVisibilityLive, IssueVisibilityArchived}
	for _, disposition := range dispositions {
		for _, engagement := range engagements {
			for _, visibility := range visibilities {
				parts := CanonicalIssueStateParts{Disposition: disposition, Engagement: engagement, Visibility: visibility}
				state, stateErr := NewCanonicalIssueState(parts)
				if disposition != IssueDispositionReady && engagement != IssueEngagementIdle {
					if stateErr == nil {
						t.Fatalf("NewCanonicalIssueState(%+v) accepted non-ready engagement", parts)
					}
					continue
				}
				if stateErr != nil {
					t.Fatalf("NewCanonicalIssueState(%+v): %v", parts, stateErr)
				}
				action, err := EvaluateManagedRuntimeLifecycle(state, true)
				switch {
				case visibility == IssueVisibilityArchived:
					if action != ManagedRuntimeLifecycleReject || err == nil {
						t.Fatalf("archived %+v = %s/%v, want reject", parts, action, err)
					}
				case disposition == IssueDispositionReady && engagement == IssueEngagementIdle:
					if action != ManagedRuntimeLifecycleRepairWorking || err != nil {
						t.Fatalf("ready idle %+v = %s/%v, want repair", parts, action, err)
					}
				case disposition == IssueDispositionReady:
					if action != ManagedRuntimeLifecycleNoop || err != nil {
						t.Fatalf("ready engaged %+v = %s/%v, want noop", parts, action, err)
					}
				default:
					if action != ManagedRuntimeLifecycleReject || err == nil {
						t.Fatalf("non-ready %+v = %s/%v, want reject", parts, action, err)
					}
				}
			}
		}
	}
}

func TestManagedRuntimeLifecycleIgnoresAbsentRuntime(t *testing.T) {
	state, err := NewCanonicalIssueState(CanonicalIssueStateParts{Disposition: IssueDispositionBacklog})
	if err != nil {
		t.Fatal(err)
	}
	action, err := EvaluateManagedRuntimeLifecycle(state, false)
	if action != ManagedRuntimeLifecycleNoop || err != nil {
		t.Fatalf("action=%s err=%v", action, err)
	}
}
