package daemon

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	daemonstate "github.com/riordanpawley/azedarach/internal/daemon/state"
	"github.com/riordanpawley/azedarach/internal/domain"
)

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
		{"review-does-not-stall-starts", TestProjectOrchestratorSnapshotKeepsStartsActionableAlongsideReview},
		{"start-intent-does-not-stall-on-review", TestProjectStartIntentDoesNotGloballyBlockOnActionableReview},
		{"cross-process-ownership", TestProjectOrchestrationSnapshotRefreshesCrossProcessOwnership},
		{"human-advisor-discussion", TestInteractionDiscussStartsAndAttachesLiveAdvisorWithoutMutatingIssueLifecycle},
		{"human-answer-resolution", TestInteractionStructuredProposalCanBeHumanEditedAndAtomicallyResolved},
		{"review-return", TestReviewReturnPreservesWorkerOwnerAndDurablyDeliversFindings},
		{"review-close-authority", TestReviewAcceptSurfacesAuthoritativeCloseFailureAndKeepsReviewState},
		{"review-close-before-dependent-completion", TestReviewAcceptClosesMultipleInternalReviewsBeforeDependentCompletion},
		{"quiescence-grace-pause-wake", runProjectOrchestratorLifecycleAcceptance},
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
		daemonInvariantInvestigationWaiting:    daemonInvariantSourceProjection,
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

func runProjectOrchestratorLifecycleAcceptance(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "runtime.db")
	store := daemonstate.NewRuntimeStateStoreAtPath(dbPath, nil)
	identity, err := domain.NewOrchestratorIdentity("acceptance-project", domain.ProjectOrchestrationScope())
	if err != nil {
		t.Fatal(err)
	}
	const sessionID = "project-orchestrator"
	authority := daemonstate.NewOrchestratorLeaseAuthority(store)
	if _, err := authority.Acquire(ctx, identity, sessionID, func(context.Context, string) (bool, error) { return true, nil }); err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 7, 11, 0, 0, 0, 0, time.UTC)
	policy := domain.OrchestratorLifecyclePolicy{CompleteGrace: 5 * time.Minute, WakeDebounce: 2 * time.Second}

	lease, err := authority.Evaluate(ctx, identity, sessionID, start, domain.OrchestratorLifecycleFacts{UnresolvedInteractions: 1}, policy)
	if err != nil || lease.Lifecycle != domain.OrchestratorQuiescent || lease.CompleteSince != nil {
		t.Fatalf("quiescent lifecycle = %+v, err=%v", lease, err)
	}
	lease, err = authority.Evaluate(ctx, identity, sessionID, start.Add(time.Minute), domain.OrchestratorLifecycleFacts{}, policy)
	if err != nil || lease.Lifecycle != domain.OrchestratorCompleteGrace || lease.CompleteSince == nil || !lease.CompleteSince.Equal(start.Add(time.Minute)) {
		t.Fatalf("complete grace lifecycle = %+v, err=%v", lease, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	store = daemonstate.NewRuntimeStateStoreAtPath(dbPath, nil)
	t.Cleanup(func() { _ = store.Close() })
	authority = daemonstate.NewOrchestratorLeaseAuthority(store)
	lease, err = authority.Evaluate(ctx, identity, sessionID, start.Add(7*time.Minute), domain.OrchestratorLifecycleFacts{}, policy)
	if err != nil || lease.Lifecycle != domain.OrchestratorPaused {
		t.Fatalf("paused lifecycle after persisted grace = %+v, err=%v", lease, err)
	}

	woken, changed, err := authority.Wake(ctx, identity, start.Add(8*time.Minute), domain.OrchestratorWakeOpenWork, policy)
	if err != nil || !changed || woken.Lifecycle != domain.OrchestratorWorking || woken.CompleteSince != nil {
		t.Fatalf("relevant-change wake = %+v, changed=%t, err=%v", woken, changed, err)
	}
	duplicate, changed, err := authority.Wake(ctx, identity, start.Add(8*time.Minute+time.Second), domain.OrchestratorWakeReviewRequest, policy)
	if err != nil || changed || duplicate.LastWakeReason != domain.OrchestratorWakeOpenWork {
		t.Fatalf("idempotent wake = %+v, changed=%t, err=%v", duplicate, changed, err)
	}
	reset, err := authority.Evaluate(ctx, identity, sessionID, start.Add(9*time.Minute), domain.OrchestratorLifecycleFacts{}, policy)
	if err != nil || reset.Lifecycle != domain.OrchestratorCompleteGrace || reset.CompleteSince == nil || !reset.CompleteSince.Equal(start.Add(9*time.Minute)) {
		t.Fatalf("grace reset after wake = %+v, err=%v", reset, err)
	}
}
