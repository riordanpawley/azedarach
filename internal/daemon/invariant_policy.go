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
	daemonInvariantSessionStartConflict daemonInvariantID = "session.start_conflict"
	daemonInvariantSessionAttachTarget  daemonInvariantID = "session.attach_target"
	daemonInvariantSessionStopTargets   daemonInvariantID = "session.stop_targets"
	daemonInvariantSessionReconcile     daemonInvariantID = "session.reconcile"

	daemonInvariantTaskListFreshness daemonInvariantID = "task.list_freshness"

	daemonInvariantRuntimeKnownProjectIDs daemonInvariantID = "runtime.known_project_ids"
)

var daemonInvariantSourceMatrix = map[daemonInvariantID]daemonInvariantSource{
	daemonInvariantSessionStartConflict:   daemonInvariantSourceTmux,
	daemonInvariantSessionAttachTarget:    daemonInvariantSourceTmux,
	daemonInvariantSessionStopTargets:     daemonInvariantSourceTmux,
	daemonInvariantSessionReconcile:       daemonInvariantSourceHybrid,
	daemonInvariantTaskListFreshness:      daemonInvariantSourceProjection,
	daemonInvariantRuntimeKnownProjectIDs: daemonInvariantSourceProjection,
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
