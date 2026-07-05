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
	daemonInvariantSessionStartConflict   daemonInvariantID = "session.start_conflict"
	daemonInvariantSessionAttachTarget    daemonInvariantID = "session.attach_target"
	daemonInvariantSessionLifecycleTarget daemonInvariantID = "session.lifecycle_target"
	daemonInvariantSessionStopTargets     daemonInvariantID = "session.stop_targets"
	daemonInvariantSessionReconcile       daemonInvariantID = "session.reconcile"

	daemonInvariantTaskListFreshness   daemonInvariantID = "task.list_freshness"
	daemonInvariantTaskClose           daemonInvariantID = "task.close"
	daemonInvariantTaskClosePreflight  daemonInvariantID = "task.close_preflight"
	daemonInvariantTaskDelete          daemonInvariantID = "task.delete"
	daemonInvariantTaskDeletePreflight daemonInvariantID = "task.delete_preflight"
	daemonInvariantTaskGraphReadiness  daemonInvariantID = "task.graph_readiness"
	daemonInvariantTaskCompleteCheck   daemonInvariantID = "task.complete_check"
	daemonInvariantTaskIntegration     daemonInvariantID = "task.integration_readiness"
	daemonInvariantTaskMergeBaseTarget daemonInvariantID = "task.merge_base_target"
	daemonInvariantTaskFollowOnMerge   daemonInvariantID = "task.follow_on_merge_candidates"
	daemonInvariantWorkerObservation   daemonInvariantID = "worker.observation_projection"

	daemonInvariantRuntimeKnownProjectIDs daemonInvariantID = "runtime.known_project_ids"
	daemonInvariantIssueResourceLifecycle daemonInvariantID = "issue_resources.lifecycle"
)

var daemonInvariantSourceMatrix = map[daemonInvariantID]daemonInvariantSource{
	daemonInvariantSessionStartConflict:   daemonInvariantSourceTmux,
	daemonInvariantSessionAttachTarget:    daemonInvariantSourceTmux,
	daemonInvariantSessionLifecycleTarget: daemonInvariantSourceTmux,
	daemonInvariantSessionStopTargets:     daemonInvariantSourceTmux,
	daemonInvariantSessionReconcile:       daemonInvariantSourceHybrid,
	daemonInvariantTaskListFreshness:      daemonInvariantSourceProjection,
	daemonInvariantTaskClose:              daemonInvariantSourceHybrid,
	daemonInvariantTaskClosePreflight:     daemonInvariantSourceHybrid,
	daemonInvariantTaskDelete:             daemonInvariantSourceHybrid,
	daemonInvariantTaskDeletePreflight:    daemonInvariantSourceHybrid,
	daemonInvariantTaskGraphReadiness:     daemonInvariantSourceHybrid,
	daemonInvariantTaskCompleteCheck:      daemonInvariantSourceHybrid,
	daemonInvariantTaskIntegration:        daemonInvariantSourceProjection,
	daemonInvariantTaskMergeBaseTarget:    daemonInvariantSourceProjection,
	daemonInvariantTaskFollowOnMerge:      daemonInvariantSourceProjection,
	daemonInvariantWorkerObservation:      daemonInvariantSourceHybrid,
	daemonInvariantRuntimeKnownProjectIDs: daemonInvariantSourceProjection,
	daemonInvariantIssueResourceLifecycle: daemonInvariantSourceProjection,
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
