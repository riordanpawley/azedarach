package protocol

// KnownDaemonInvariant reports whether id names an invariant exposed by the
// daemon invariant source registry. Review checkpoint data crosses the CLI and
// daemon boundary, so both sides use this transport-level vocabulary rather
// than accepting arbitrary strings.
func KnownDaemonInvariant(id string) bool {
	_, ok := knownDaemonInvariants[id]
	return ok
}

// KnownDaemonInvariantCount supports exact registry-alignment guards without
// exposing mutable registry state.
func KnownDaemonInvariantCount() int { return len(knownDaemonInvariants) }

var knownDaemonInvariants = map[string]struct{}{
	"session.start_conflict": {}, "session.attach_target": {}, "session.lifecycle_target": {},
	"session.stop_targets": {}, "session.reconcile": {}, "session.issue_lifecycle_runtime": {},
	"session.activity_convergence": {}, "session.managed_agent_identity": {}, "session.managed_agent_restart": {},
	"session.agent_input_delivery": {}, "session.advisor_singleton": {}, "task.list_freshness": {},
	"task.read_committed_revision": {}, "task.close": {}, "task.close_preflight": {}, "task.delete": {},
	"task.delete_preflight": {}, "task.graph_readiness": {}, "task.complete_check": {}, "task.review_handoff": {},
	"task.integration_readiness": {}, "task.context_risk_closeout": {}, "task.merge_base_target": {},
	"task.follow_on_merge_candidates": {}, "task.split_intent": {}, "task.publication_queue": {}, "worker.observation_projection": {},
	"interaction.waiting_human": {}, "investigation.waiting_human": {}, "interaction.staleness": {},
	"decision.markdown_transfer_target": {}, "decision.propagation_delivery": {}, "runtime.known_project_ids": {},
	"cross_project.view_projection": {}, "issue_resources.lifecycle": {}, "orchestration.scope_identity": {},
	"orchestration.scope_singleton": {}, "orchestration.rooted_bootstrap_delivery": {},
	"orchestration.root_dependency_gate": {},
	"orchestration.project_completion":   {}, "orchestration.project_candidates": {},
	"orchestration.parent_continuation": {}, "orchestration.project_review": {}, "orchestration.claim_start": {},
	"orchestration.project_loop": {}, "projection.delta_stream": {}, "external.tmux_observation": {},
	"validation.machine_capacity": {}, "validation.publication_evidence": {},
}
