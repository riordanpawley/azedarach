# Docs Index

This directory primarily contains **developer/internal documentation**.

## Audience

- Developer docs: architecture, implementation, boundaries, operations, and release workflows for contributors.
- User docs: not currently maintained in `docs/`; end-user usage starts from the repo root [README.md](../README.md).

## Developer Docs

- [01-overview.md](01-overview.md)
- [02-architecture.md](02-architecture.md)
- [03-project-structure.md](03-project-structure.md)
- [04-go-best-practices.md](04-go-best-practices.md)
- [05-bubbletea-patterns.md](05-bubbletea-patterns.md)
- [06-daemon-battle-tested-path.md](06-daemon-battle-tested-path.md)
- [07-daemon-package-boundaries.md](07-daemon-package-boundaries.md)
- [08-recovery-playbook.md](08-recovery-playbook.md)
- [09-boundary-hardening-policy.md](09-boundary-hardening-policy.md)
- [10-go-release-and-homebrew.md](10-go-release-and-homebrew.md)
- [11-az-spec-v1-contract.md](11-az-spec-v1-contract.md)
- [12-overlay-sizing.md](12-overlay-sizing.md)
- [13-backlog-resilience-acceptance.md](13-backlog-resilience-acceptance.md)
- [14-issue-resource-lifecycle.md](14-issue-resource-lifecycle.md)
- [15-orchestration-flow-comparison.md](15-orchestration-flow-comparison.md)
- [16-scheduled-project-scripts.md](16-scheduled-project-scripts.md)
- [17-sqlite-query-plan-guardrails.md](17-sqlite-query-plan-guardrails.md)
- [18-async-notice-architecture.md](18-async-notice-architecture.md)
- [19-issue-context-risk.md](19-issue-context-risk.md)
- [20-runtime-event-sourcing-evaluation.md](20-runtime-event-sourcing-evaluation.md)
- [21-event-sourcing-migration-plan.md](21-event-sourcing-migration-plan.md)
- [22-event-sourcing-detailed-map-and-risk-register.md](22-event-sourcing-detailed-map-and-risk-register.md)
- [23-sqlite-wal-policy.md](23-sqlite-wal-policy.md)
- [24-issue-state-model-v2-rollout.md](24-issue-state-model-v2-rollout.md)
- [25-migration-artifacts.md](25-migration-artifacts.md) — immutable migration artifact and checksum convention
- [25-configurable-views.md](25-configurable-views.md)
- [25-cross-project-user-database.md](25-cross-project-user-database.md)
- [25-rootless-orchestrator-contracts.md](25-rootless-orchestrator-contracts.md)
- [26-team-collaboration-architecture.md](26-team-collaboration-architecture.md)
- [26-test-wait-audit.md](26-test-wait-audit.md) — inventory and policy for test waits over 500 ms
- [26-test-timing-profiles.md](26-test-timing-profiles.md)
- [27-go-cache-protocol.md](27-go-cache-protocol.md) — bounded worktree-aware Go cache ownership, validation, and maintenance
- [28-decision-markdown-sync.md](28-decision-markdown-sync.md) — worktree-safe decision store/export authority and recovery workflow
- [29-native-agent-input-authority.md](29-native-agent-input-authority.md) — exact-incarnation native delivery and fail-closed tool capability contract
- [29-ticket-event-history-query.md](29-ticket-event-history-query.md) — searchable, cursor-pageable ticket observation history
- [30-first-pass-review-miss-investigation.md](30-first-pass-review-miss-investigation.md) — DUG chronology, prompt experiment, taxonomy, and review-evidence recommendations
- [adr/1-daemon-ownership-adr.md](adr/1-daemon-ownership-adr.md)
- [adr/2-daemon-owned-async-notices.md](adr/2-daemon-owned-async-notices.md)

## Contributor Workflow

- Use `az prime` at session start, keep non-trivial implementation tied to an `az issue`, and work from that issue's worktree/session instead of editing the main worktree directly.
- If accidental main-worktree changes exist, preserve any useful state in the issue worktree before cleaning main.

## Daemon Invariant Rule

- Every invariant must declare an explicit source policy: `projection`, `tmux`, or `hybrid`.
- For `projection` and `hybrid`, refresh in-memory cache from durable SQLite projections, then evaluate from the refreshed cache.
- For `tmux`, use live tmux runtime as source of truth (do not infer runtime presence from projection alone).
- Current source-policy examples:
- `session.start` conflict / `session.attach` target / `session.pause` and `session.resume` lifecycle targets / `session.stop` targets: `tmux`.
- `session.recover` reconciliation: `hybrid` (projection intent + tmux runtime).
- `session.issue_lifecycle_runtime`: `hybrid` (refreshed factored issue state + live tmux; ready+idle is repaired to working, while backlog/terminal/archived divergence is preserved for explicit reconciliation).
- `session.activity_convergence`: `hybrid` (refreshed durable activity/runtime projections + bounded live tmux prompt probe; newer hook evidence wins races).
- `session.managed_agent_identity`: `hybrid` (refreshed durable logical-pane/process incarnation + exact live tmux pane/PID comparison; stale or reused identities fail closed).
- `session.managed_agent_restart`: `hybrid` (refreshed durable session role/scope, exact orchestrator lease authority when applicable, and managed-agent identity + canonical live tmux session/pane/PID preflight, followed by exact-scope serialized canonical replacement in the authoritative project root or issue worktree; Codex resumes the separately stored hook-reported thread ID directly and refuses missing/stale identity instead of entering a picker, then requires distinct hook-acknowledged live incarnation proof before success or no-respawn recovery).
- `session.agent_input_delivery`: `hybrid` (durable incarnation-bound intent/lease + refreshed identity and hook readiness + direct exact-thread app-server submission behind a daemon-managed tmux attached-client read-only gate; actionable orchestrator mail is projected first and materializes a project/issue/session-bound wake keyed by its monotonic observation ID, busy workers defer until hook-backed readiness, and restart reconciliation retries undelivered wakes; tmux capture/send/paste cannot acknowledge delivery, uncertain submissions remain ambiguous without automatic retry, and diagnostics expose delivery states).
- `session.advisor_singleton`: `hybrid` (refreshed interaction/session-role projection + tmux runtime); reconcile recreates missing discussion runtimes, resumes paused projections, removes terminal/orphan reservations, and project removal runs daemon cleanup before unregistering the project.
- `task.close`, `task.close_preflight`, `task.delete`, `task.delete_preflight`, `task.graph_readiness`, and `task.complete_check`: `hybrid` (durable issue graph/v2 lifecycle and investigation disposition/acceptance evidence projection + live runtime attachment state). Missing investigation disposition remains human-facing; declared internal reviews require accepted reviewer evidence with no later returned findings.
- `orchestration.project_candidates`: `projection` (bounded durable lifecycle/graph, ownership, session activity, and interaction candidate projection).
- `orchestration.project_review`: `hybrid` (refreshed durable issue/review/ownership and exact issue-worktree projections plus live Git worktree identity, clean HEAD, and aggregate-revision verification; accepted outcomes delegate integration and cleanup to hybrid `task.close`).
- `orchestration.claim_start`: `hybrid` (durable ownership/start-attempt projection plus daemon session-start operation/runtime compensation).
- `task.split_intent`: `hybrid` (durable project-scoped split identity, canonical child, and graph edges plus exact orchestration start operation/session runtime).
- `orchestration.project_loop`: `projection` (durable issue-observation cursor and loop checkpoint refreshed before deterministic non-blocking start/review action replay; daemon-owned starts need no model turn, while exact review epochs produce scope-bound semantic wake keys and queued reviews remain visible without globally stalling unrelated starts).
- `projection.delta_stream`: `projection` (durable project delta ledger and version history; cursor replay and snapshot reads never reconcile or poll tmux).
- `validation.machine_capacity`: `projection` (durable daemon-owned publication/timing-capacity projection; ordinary worktree validation bypasses admission, publication starts immediately, and only controlled timing capacity is overlap-sensitive).
- `validation.publication_evidence`: `projection` (immutable daemon-owned patch-review, active-path, and synthetic merge-result evidence plus append-only invalidation reasons; unrelated base movement preserves patch evidence while configured overlap and high-risk uncertainty fail closed).
- `task.publication_queue`: `hybrid` (refresh and atomically acquire a unique expiring durable attempt claim; require the canonical typed reviewer, exact review epoch, accepted-review event, publication operation, and live claim through validation, retry, apply, lease consumption, and close; reclaim only expired attempts, then create and validate an isolated synthetic Git candidate and compare-and-apply only while the pinned source and base identities remain exact).
- `task.review_handoff`: `projection` (durable issue v2 lifecycle/review projection + revisioned material decision change/acknowledgement observations + session activity projection; active issue self-handoff remains allowed only after material decisions are current).
- `decision.propagation_delivery`: `hybrid` (atomic decision audit/outbox and per-issue materialization checkpoints reconciled with live tmux delivery until an authoritative exact-revision acknowledgement; superseded and withdrawn revisions are not delivered).
- `task.integration_readiness` and `task.context_risk_closeout`: `projection` (durable issue projection + mailbox/observation evidence; integration readiness additionally requires completed publication proof bound to the clean exact candidate revision, independent of concurrent development load).
- `task.merge_base_target`: `projection` (durable issue graph + worktree projection; explicit root-to-base requests also require issue-scoped `human.input_provided` acceptance evidence).
- `decision.markdown_transfer_target`: `hybrid` (refreshed durable worktree ownership plus live Git worktree path and HEAD revision).
- `task.follow_on_merge_candidates`: `projection` (durable issue graph + worktree projection).
- `issue_resources.lifecycle`: `projection` (durable issue status + runtime attachment projection).
- `interaction.waiting_human`: `projection` (durable interaction requests refreshed before decision-waiting and pickup evaluation).
- `investigation.waiting_human`: `projection` (durable investigation disposition and issue-specific acceptance/review evidence refreshed before human-authority evaluation).
- `interaction.staleness`: `projection` (durable interaction requests refreshed before age evaluation and revision-safe stale/reminder/disposition/recovery audit writes).
- `task.list` freshness/session timestamps: `projection` (refresh-then-cache).
- `task.read_committed_revision`: `projection` (an authoritative issue commit advances or invalidates the daemon-local task materializer before mutation acknowledgement; list/search/detail/get-many and close-preflight fence durable keyed deltas and strictly refresh their runtime facts before evaluation, while a committed session cleanup fails unavailable if that task-view refresh cannot be completed; ticket-only degradation is confined to bootstrap/background enrichment).
- `cross_project.view_projection`: `projection` (the global daemon incrementally consumes verified per-project issue deltas and independently keyed current runtime/fact materializations into the user database, then evaluates typed cross-project views there; full export is limited to bootstrap, explicit rebuild, and isolated recovery, while stale and unavailable projects remain explicit).
- `orchestration.scope_identity`: `projection` (durable project plus typed rooted/project scope; startup environment is not authority).
- `daemon.project_routing`: `projection` (the registered-project registry is
  resolved before project-owned configuration, stores, capabilities, logs, or
  adapters are opened; unknown and unavailable identities fail closed without
  cross-project fallback).
- `orchestration.scope_singleton`: `hybrid` (refreshed durable scope lease compared with live tmux runtime).
- `orchestration.rooted_bootstrap_delivery`: `hybrid` (one physical top-level session has one mutually exclusive desired role/scope; epic session start and rooted ticket attach/stop delegate to exact rooted-orchestrator authority; each rooted transition atomically retires matching legacy worker intent; refreshed durable accepted prompt acknowledgement is compared with the exact pane/PID/launch-incarnation/native-thread binding and live tmux marker; restart preserves rooted intent and re-acknowledges only after hook-backed readiness).
- `orchestration.root_dependency_gate`: `projection` (wait for the local issue/dependency materializer to consume the durable delivery head, resolve the requested root's ancestor containment chain, then reject local or inherited unresolved root blockers before any rooted lease, intent, lifecycle, worktree, or tmux mutation; descendant fanout readiness uses the same ancestor-aware gate).
- `orchestration.project_completion`: `hybrid` (refreshed issue/review/interaction/session projections compared with live tmux runtime).
- `orchestration.parent_continuation`: `hybrid` (durable rooted lease/cursor + refreshed direct runnable, review, nested-root, scoped worker-blocker, interaction, completion, and session projections compared with live tmux and managed-agent identity/activity; exact durable blocker-event identities are actionable while progress-only observations are not; actionable semantic revisions enqueue one scope-bound, session/incarnation-bound durable input intent, action/idle transitions advance a durable generation so recurring actions and replacement targets remain deliverable, busy agents defer delivery, equivalent state coalesces, and restart retries unacknowledged intent exactly once).
- `runtime.reconcile` includes `invariant_sources` debug output reflecting the active source-policy matrix.
- `external.tmux_observation` is a daemon-owned `tmux` source: bounded inventory and sparse pane observation publish coalesced current-state projection changes; snapshot/watch readers never poll tmux and routine observations never become semantic history.
- Treat this as the required cross-daemon safety contract for session/worktree/runtime invariants.

## Spec Records

- [Ticket terminology migration](25-ticket-terminology-migration.md) defines the canonical language and compatibility boundaries.

- `az spec read --json` reads daemon-backed requirement/link records.
- Markdown spec export is disabled until it can export the real stored spec data.
