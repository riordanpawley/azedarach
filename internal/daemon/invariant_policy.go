package daemon

import "sort"

type daemonInvariantID string

type daemonInvariantSource string

const (
	daemonInvariantSourceProjection daemonInvariantSource = "projection"
	daemonInvariantSourceTmux       daemonInvariantSource = "tmux"
	daemonInvariantSourceHybrid     daemonInvariantSource = "hybrid"
)

const (
	daemonInvariantSessionStartConflict    daemonInvariantID = "session.start_conflict"
	daemonInvariantSessionAttachTarget     daemonInvariantID = "session.attach_target"
	daemonInvariantSessionLifecycleTarget  daemonInvariantID = "session.lifecycle_target"
	daemonInvariantSessionStopTargets      daemonInvariantID = "session.stop_targets"
	daemonInvariantSessionReconcile        daemonInvariantID = "session.reconcile"
	daemonInvariantSessionIssueLifecycle   daemonInvariantID = "session.issue_lifecycle_runtime"
	daemonInvariantSessionActivityConverge daemonInvariantID = "session.activity_convergence"
	daemonInvariantAdvisorSingleton        daemonInvariantID = "session.advisor_singleton"

	daemonInvariantTaskListFreshness    daemonInvariantID = "task.list_freshness"
	daemonInvariantTaskClose            daemonInvariantID = "task.close"
	daemonInvariantTaskClosePreflight   daemonInvariantID = "task.close_preflight"
	daemonInvariantTaskDelete           daemonInvariantID = "task.delete"
	daemonInvariantTaskDeletePreflight  daemonInvariantID = "task.delete_preflight"
	daemonInvariantTaskGraphReadiness   daemonInvariantID = "task.graph_readiness"
	daemonInvariantTaskCompleteCheck    daemonInvariantID = "task.complete_check"
	daemonInvariantTaskReviewHandoff    daemonInvariantID = "task.review_handoff"
	daemonInvariantTaskIntegration      daemonInvariantID = "task.integration_readiness"
	daemonInvariantTaskContextRisk      daemonInvariantID = "task.context_risk_closeout"
	daemonInvariantTaskMergeBaseTarget  daemonInvariantID = "task.merge_base_target"
	daemonInvariantTaskFollowOnMerge    daemonInvariantID = "task.follow_on_merge_candidates"
	daemonInvariantWorkerObservation    daemonInvariantID = "worker.observation_projection"
	daemonInvariantInteractionWaiting   daemonInvariantID = "interaction.waiting_human"
	daemonInvariantInvestigationWaiting daemonInvariantID = "investigation.waiting_human"
	daemonInvariantInteractionStaleness daemonInvariantID = "interaction.staleness"
	daemonInvariantDecisionMDTransfer   daemonInvariantID = "decision.markdown_transfer_target"
	daemonInvariantDecisionPropagation  daemonInvariantID = "decision.propagation_delivery"

	daemonInvariantRuntimeKnownProjectIDs  daemonInvariantID = "runtime.known_project_ids"
	daemonInvariantCrossProjectViews       daemonInvariantID = "cross_project.view_projection"
	daemonInvariantIssueResourceLifecycle  daemonInvariantID = "issue_resources.lifecycle"
	daemonInvariantOrchestrationScope      daemonInvariantID = "orchestration.scope_identity"
	daemonInvariantOrchestrationSingleton  daemonInvariantID = "orchestration.scope_singleton"
	daemonInvariantOrchestrationCompletion daemonInvariantID = "orchestration.project_completion"
	daemonInvariantOrchestrationCandidates daemonInvariantID = "orchestration.project_candidates"
	daemonInvariantOrchestrationParentWake daemonInvariantID = "orchestration.parent_continuation"
	daemonInvariantOrchestrationReview     daemonInvariantID = "orchestration.project_review"
	daemonInvariantOrchestrationClaimStart daemonInvariantID = "orchestration.claim_start"
	daemonInvariantOrchestrationLoop       daemonInvariantID = "orchestration.project_loop"
	daemonInvariantProjectionDeltaStream   daemonInvariantID = "projection.delta_stream"
	daemonInvariantTmuxObservation         daemonInvariantID = "external.tmux_observation"
)

var daemonInvariantSourceMatrix = map[daemonInvariantID]daemonInvariantSource{
	daemonInvariantSessionStartConflict:    daemonInvariantSourceTmux,
	daemonInvariantSessionAttachTarget:     daemonInvariantSourceTmux,
	daemonInvariantSessionLifecycleTarget:  daemonInvariantSourceTmux,
	daemonInvariantSessionStopTargets:      daemonInvariantSourceTmux,
	daemonInvariantSessionReconcile:        daemonInvariantSourceHybrid,
	daemonInvariantSessionIssueLifecycle:   daemonInvariantSourceHybrid,
	daemonInvariantSessionActivityConverge: daemonInvariantSourceHybrid,
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
	daemonInvariantRuntimeKnownProjectIDs:  daemonInvariantSourceProjection,
	daemonInvariantCrossProjectViews:       daemonInvariantSourceProjection,
	daemonInvariantIssueResourceLifecycle:  daemonInvariantSourceProjection,
	daemonInvariantOrchestrationScope:      daemonInvariantSourceProjection,
	daemonInvariantOrchestrationSingleton:  daemonInvariantSourceHybrid,
	daemonInvariantOrchestrationCompletion: daemonInvariantSourceHybrid,
	daemonInvariantOrchestrationCandidates: daemonInvariantSourceProjection,
	daemonInvariantOrchestrationParentWake: daemonInvariantSourceHybrid,
	daemonInvariantInteractionWaiting:      daemonInvariantSourceProjection,
	daemonInvariantInvestigationWaiting:    daemonInvariantSourceProjection,
	daemonInvariantAdvisorSingleton:        daemonInvariantSourceHybrid,
	daemonInvariantInteractionStaleness:    daemonInvariantSourceProjection,
	daemonInvariantDecisionMDTransfer:      daemonInvariantSourceHybrid,
	daemonInvariantDecisionPropagation:     daemonInvariantSourceHybrid,
	daemonInvariantOrchestrationReview:     daemonInvariantSourceProjection,
	daemonInvariantOrchestrationClaimStart: daemonInvariantSourceHybrid,
	daemonInvariantOrchestrationLoop:       daemonInvariantSourceProjection,
	daemonInvariantProjectionDeltaStream:   daemonInvariantSourceProjection,
	daemonInvariantTmuxObservation:         daemonInvariantSourceTmux,
}

func sourceForInvariant(id daemonInvariantID) daemonInvariantSource {
	if source, ok := daemonInvariantSourceMatrix[id]; ok {
		return source
	}
	return daemonInvariantSourceProjection
}

func invariantSourceMatrix() map[daemonInvariantID]daemonInvariantSource {
	out := make(map[daemonInvariantID]daemonInvariantSource, len(daemonInvariantSourceMatrix))
	for id, source := range daemonInvariantSourceMatrix {
		out[id] = source
	}
	return out
}

func invariantSourceDebugMap() map[string]string {
	if len(daemonInvariantSourceMatrix) == 0 {
		return nil
	}
	ids := make([]string, 0, len(daemonInvariantSourceMatrix))
	for id := range daemonInvariantSourceMatrix {
		ids = append(ids, string(id))
	}
	sort.Strings(ids)

	out := make(map[string]string, len(ids))
	for _, id := range ids {
		out[id] = string(daemonInvariantSourceMatrix[daemonInvariantID(id)])
	}
	return out
}

func usesProjectionSource(source daemonInvariantSource) bool {
	return source == daemonInvariantSourceProjection || source == daemonInvariantSourceHybrid
}

func usesTmuxSource(source daemonInvariantSource) bool {
	return source == daemonInvariantSourceTmux || source == daemonInvariantSourceHybrid
}
