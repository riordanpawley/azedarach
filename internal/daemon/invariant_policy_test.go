package daemon

import "testing"

func TestInvariantSourceMatrixIncludesExpectedRuntimeInvariants(t *testing.T) {
	matrix := invariantSourceMatrix()
	expected := map[daemonInvariantID]daemonInvariantSource{
		daemonInvariantSessionStartConflict:    daemonInvariantSourceTmux,
		daemonInvariantSessionAttachTarget:     daemonInvariantSourceTmux,
		daemonInvariantSessionLifecycleTarget:  daemonInvariantSourceTmux,
		daemonInvariantSessionStopTargets:      daemonInvariantSourceTmux,
		daemonInvariantSessionReconcile:        daemonInvariantSourceHybrid,
		daemonInvariantSessionIssueLifecycle:   daemonInvariantSourceHybrid,
		daemonInvariantSessionActivityConverge: daemonInvariantSourceHybrid,
		daemonInvariantAdvisorSingleton:        daemonInvariantSourceHybrid,
		daemonInvariantTaskListFreshness:       daemonInvariantSourceProjection,
		daemonInvariantTaskClose:               daemonInvariantSourceHybrid,
		daemonInvariantTaskClosePreflight:      daemonInvariantSourceHybrid,
		daemonInvariantTaskDelete:              daemonInvariantSourceHybrid,
		daemonInvariantTaskDeletePreflight:     daemonInvariantSourceHybrid,
		daemonInvariantTaskGraphReadiness:      daemonInvariantSourceHybrid,
		daemonInvariantTaskCompleteCheck:       daemonInvariantSourceHybrid,
		daemonInvariantTaskReviewHandoff:       daemonInvariantSourceProjection,
		daemonInvariantTaskIntegration:         daemonInvariantSourceProjection,
		daemonInvariantTaskContextRisk:         daemonInvariantSourceProjection,
		daemonInvariantTaskMergeBaseTarget:     daemonInvariantSourceProjection,
		daemonInvariantTaskFollowOnMerge:       daemonInvariantSourceProjection,
		daemonInvariantWorkerObservation:       daemonInvariantSourceHybrid,
		daemonInvariantInteractionWaiting:      daemonInvariantSourceProjection,
		daemonInvariantInvestigationWaiting:    daemonInvariantSourceProjection,
		daemonInvariantRuntimeKnownProjectIDs:  daemonInvariantSourceProjection,
		daemonInvariantIssueResourceLifecycle:  daemonInvariantSourceProjection,
		daemonInvariantOrchestrationScope:      daemonInvariantSourceProjection,
		daemonInvariantOrchestrationSingleton:  daemonInvariantSourceHybrid,
		daemonInvariantOrchestrationCompletion: daemonInvariantSourceHybrid,
		daemonInvariantOrchestrationCandidates: daemonInvariantSourceProjection,
		daemonInvariantOrchestrationParentWake: daemonInvariantSourceHybrid,
		daemonInvariantOrchestrationReview:     daemonInvariantSourceProjection,
		daemonInvariantOrchestrationClaimStart: daemonInvariantSourceHybrid,
		daemonInvariantOrchestrationLoop:       daemonInvariantSourceProjection,
		daemonInvariantProjectionDeltaStream:   daemonInvariantSourceProjection,
		daemonInvariantTmuxObservation:         daemonInvariantSourceTmux,
	}
	for id, want := range expected {
		got, ok := matrix[id]
		if !ok {
			t.Fatalf("missing invariant %q in source matrix", id)
		}
		if got != want {
			t.Fatalf("source matrix[%q] = %q, want %q", id, got, want)
		}
	}
}

func TestSourceForInvariantDefaultsToProjection(t *testing.T) {
	if got, want := sourceForInvariant(daemonInvariantID("unknown.invariant")), daemonInvariantSourceProjection; got != want {
		t.Fatalf("sourceForInvariant(unknown) = %q, want %q", got, want)
	}
}
