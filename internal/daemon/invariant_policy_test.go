package daemon

import (
	"testing"

	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
)

func TestInvariantSourceMatrixIncludesExpectedRuntimeInvariants(t *testing.T) {
	matrix := invariantSourceMatrix()
	expected := map[daemonInvariantID]daemonInvariantSource{
		daemonInvariantSessionStartConflict:         daemonInvariantSourceTmux,
		daemonInvariantSessionAttachTarget:          daemonInvariantSourceTmux,
		daemonInvariantSessionLifecycleTarget:       daemonInvariantSourceTmux,
		daemonInvariantSessionStopTargets:           daemonInvariantSourceTmux,
		daemonInvariantSessionReconcile:             daemonInvariantSourceHybrid,
		daemonInvariantSessionIssueLifecycle:        daemonInvariantSourceHybrid,
		daemonInvariantSessionActivityConverge:      daemonInvariantSourceHybrid,
		daemonInvariantManagedAgentRestart:          daemonInvariantSourceHybrid,
		daemonInvariantAdvisorSingleton:             daemonInvariantSourceHybrid,
		daemonInvariantTaskListFreshness:            daemonInvariantSourceProjection,
		daemonInvariantTaskReadAfterWrite:           daemonInvariantSourceProjection,
		daemonInvariantTaskClose:                    daemonInvariantSourceHybrid,
		daemonInvariantTaskClosePreflight:           daemonInvariantSourceHybrid,
		daemonInvariantTaskDelete:                   daemonInvariantSourceHybrid,
		daemonInvariantTaskDeletePreflight:          daemonInvariantSourceHybrid,
		daemonInvariantTaskGraphReadiness:           daemonInvariantSourceHybrid,
		daemonInvariantTaskCompleteCheck:            daemonInvariantSourceHybrid,
		daemonInvariantTaskReviewHandoff:            daemonInvariantSourceProjection,
		daemonInvariantTaskIntegration:              daemonInvariantSourceProjection,
		daemonInvariantTaskContextRisk:              daemonInvariantSourceProjection,
		daemonInvariantTaskMergeBaseTarget:          daemonInvariantSourceProjection,
		daemonInvariantTaskFollowOnMerge:            daemonInvariantSourceProjection,
		daemonInvariantTaskSplitIntent:              daemonInvariantSourceHybrid,
		daemonInvariantTaskPublicationQueue:         daemonInvariantSourceHybrid,
		daemonInvariantWorkerObservation:            daemonInvariantSourceHybrid,
		daemonInvariantInteractionWaiting:           daemonInvariantSourceProjection,
		daemonInvariantInvestigationWaiting:         daemonInvariantSourceProjection,
		daemonInvariantDecisionMDTransfer:           daemonInvariantSourceHybrid,
		daemonInvariantDecisionPropagation:          daemonInvariantSourceHybrid,
		daemonInvariantRuntimeKnownProjectIDs:       daemonInvariantSourceProjection,
		daemonInvariantIssueResourceLifecycle:       daemonInvariantSourceProjection,
		daemonInvariantOrchestrationScope:           daemonInvariantSourceProjection,
		daemonInvariantOrchestrationSingleton:       daemonInvariantSourceHybrid,
		daemonInvariantOrchestrationRootedBootstrap: daemonInvariantSourceHybrid,
		daemonInvariantOrchestrationRootBlockerGate: daemonInvariantSourceProjection,
		daemonInvariantOrchestrationCompletion:      daemonInvariantSourceHybrid,
		daemonInvariantOrchestrationCandidates:      daemonInvariantSourceProjection,
		daemonInvariantOrchestrationParentWake:      daemonInvariantSourceHybrid,
		daemonInvariantOrchestrationReview:          daemonInvariantSourceHybrid,
		daemonInvariantOrchestrationClaimStart:      daemonInvariantSourceHybrid,
		daemonInvariantOrchestrationLoop:            daemonInvariantSourceProjection,
		daemonInvariantProjectionDeltaStream:        daemonInvariantSourceProjection,
		daemonInvariantTmuxObservation:              daemonInvariantSourceTmux,
		daemonInvariantValidationCapacity:           daemonInvariantSourceProjection,
		daemonInvariantPublicationEvidence:          daemonInvariantSourceProjection,
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

func TestInvariantSourceMatrixUsesReviewCheckpointVocabulary(t *testing.T) {
	if got, want := protocol.KnownDaemonInvariantCount(), len(daemonInvariantSourceMatrix); got != want {
		t.Fatalf("review checkpoint vocabulary has %d invariants, source matrix has %d", got, want)
	}
	for id := range daemonInvariantSourceMatrix {
		if !protocol.KnownDaemonInvariant(string(id)) {
			t.Errorf("invariant %q is missing from review checkpoint vocabulary", id)
		}
	}
}

func TestSourceForInvariantDefaultsToProjection(t *testing.T) {
	if got, want := sourceForInvariant(daemonInvariantID("unknown.invariant")), daemonInvariantSourceProjection; got != want {
		t.Fatalf("sourceForInvariant(unknown) = %q, want %q", got, want)
	}
}
