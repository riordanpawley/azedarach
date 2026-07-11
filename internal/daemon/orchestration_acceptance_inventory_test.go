package daemon

import "testing"

// TestProjectOrchestrationEndToEndAcceptanceInventory is the executable index
// for the detailed production-path tests documented in
// docs/25-rootless-orchestrator-contracts.md. It intentionally locks the
// cross-cutting invariant contract here; scenario mechanics remain in focused
// tests so failures identify the broken authority boundary.
func TestProjectOrchestrationEndToEndAcceptanceInventory(t *testing.T) {
	scenarios := []struct {
		name string
		run  func(*testing.T)
	}{
		{"singleton-start-attach-recovery", TestProjectOrchestratorSessionStartAttachesExactScopeSingleton},
		{"project-scheduling-cursor", TestProjectOrchestratorLoopPrioritizesReviewAndPersistsCursor},
		{"cross-process-ownership", TestProjectOrchestrationSnapshotRefreshesCrossProcessOwnership},
		{"human-advisor-discussion", TestInteractionDiscussStartsAndAttachesLiveAdvisorWithoutMutatingIssueLifecycle},
		{"human-answer-resolution", TestInteractionStructuredProposalCanBeHumanEditedAndAtomicallyResolved},
		{"review-return", TestReviewReturnPreservesWorkerOwnerAndDurablyDeliversFindings},
		{"review-close-authority", TestReviewAcceptSurfacesAuthoritativeCloseFailureAndKeepsReviewState},
		{"restart-action-replay", TestProjectOrchestratorLoopMultiDaemonReplayDoesNotDuplicateCheckpointAction},
		{"advisor-cross-daemon-race", TestAdvisorRecoveryCleansRuntimeWhenTerminalRequestWinsCrossDaemonRace},
	}
	for _, scenario := range scenarios {
		t.Run(scenario.name, scenario.run)
	}

	wantSources := map[daemonInvariantID]daemonInvariantSource{
		daemonInvariantOrchestrationScope:      daemonInvariantSourceProjection,
		daemonInvariantOrchestrationSingleton:  daemonInvariantSourceHybrid,
		daemonInvariantOrchestrationCompletion: daemonInvariantSourceHybrid,
		daemonInvariantOrchestrationCandidates: daemonInvariantSourceProjection,
		daemonInvariantOrchestrationReview:     daemonInvariantSourceProjection,
		daemonInvariantOrchestrationClaimStart: daemonInvariantSourceHybrid,
		daemonInvariantOrchestrationLoop:       daemonInvariantSourceProjection,
		daemonInvariantOrchestrationParentWake: daemonInvariantSourceHybrid,
		daemonInvariantInteractionWaiting:      daemonInvariantSourceProjection,
		daemonInvariantInteractionStaleness:    daemonInvariantSourceProjection,
		daemonInvariantAdvisorSingleton:        daemonInvariantSourceHybrid,
	}

	debug := invariantSourceDebugMap()
	for id, want := range wantSources {
		if got := sourceForInvariant(id); got != want {
			t.Errorf("sourceForInvariant(%q) = %q, want %q", id, got, want)
		}
		if got := debug[string(id)]; got != string(want) {
			t.Errorf("runtime reconcile invariant_sources[%q] = %q, want %q", id, got, want)
		}
	}
}
