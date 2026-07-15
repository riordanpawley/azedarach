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
- `session.agent_input_delivery`: `hybrid` (durable incarnation-bound intent/lease + refreshed identity and hook readiness + a native receiver's atomic empty-composer/human-exclusion proof and exact acknowledgement; tmux capture/send/paste cannot acknowledge delivery and missing native proof remains queued).
- `session.advisor_singleton`: `hybrid` (refreshed interaction/session-role projection + tmux runtime); reconcile recreates missing discussion runtimes, resumes paused projections, removes terminal/orphan reservations, and project removal runs daemon cleanup before unregistering the project.
- `task.close`, `task.close_preflight`, `task.delete`, `task.delete_preflight`, `task.graph_readiness`, and `task.complete_check`: `hybrid` (durable issue graph/v2 lifecycle and investigation disposition/acceptance evidence projection + live runtime attachment state). Missing investigation disposition remains human-facing; declared internal reviews require accepted reviewer evidence with no later returned findings.
- `orchestration.project_candidates`: `projection` (bounded durable lifecycle/graph, ownership, session activity, and interaction candidate projection).
- `orchestration.project_review`: `projection` (durable issue/review/ownership, mailbox/observation evidence, and worktree projections; accepted outcomes delegate integration and cleanup to hybrid `task.close`).
- `orchestration.claim_start`: `hybrid` (durable ownership/start-attempt projection plus daemon session-start operation/runtime compensation).
- `orchestration.project_loop`: `projection` (durable issue-observation cursor and loop checkpoint refreshed before deterministic non-blocking start/review action replay; queued reviews remain visible without globally stalling unrelated starts).
- `validation.machine_capacity`: `projection` (durable daemon-owned aggregate/shared/safe validation queue with heartbeat expiry and transactionally refreshed admission state).
- `task.review_handoff`: `projection` (durable issue v2 lifecycle/review projection + revisioned material decision change/acknowledgement observations + session activity projection; active issue self-handoff remains allowed only after material decisions are current).
- `decision.propagation_delivery`: `hybrid` (atomic decision audit/outbox and per-issue materialization checkpoints reconciled with live tmux delivery until an authoritative exact-revision acknowledgement; superseded and withdrawn revisions are not delivered).
- `task.integration_readiness` and `task.context_risk_closeout`: `projection` (durable issue projection + mailbox/observation evidence; integration readiness additionally requires completed non-overlapping aggregate proof bound to the clean exact candidate revision).
- `task.merge_base_target`: `projection` (durable issue graph + worktree projection; explicit root-to-base requests also require issue-scoped `human.input_provided` acceptance evidence).
- `decision.markdown_transfer_target`: `hybrid` (refreshed durable worktree ownership plus live Git worktree path and HEAD revision).
- `task.follow_on_merge_candidates`: `projection` (durable issue graph + worktree projection).
- `issue_resources.lifecycle`: `projection` (durable issue status + runtime attachment projection).
- `interaction.waiting_human`: `projection` (durable interaction requests refreshed before decision-waiting and pickup evaluation).
- `investigation.waiting_human`: `projection` (durable investigation disposition and issue-specific acceptance/review evidence refreshed before human-authority evaluation).
- `interaction.staleness`: `projection` (durable interaction requests refreshed before age evaluation and revision-safe stale/reminder/disposition/recovery audit writes).
- `task.list` freshness/session timestamps: `projection` (refresh-then-cache).
- `cross_project.view_projection`: `projection` (the global daemon refreshes the user database from authoritative project stores, then evaluates typed cross-project views from the refreshed user-level projection; stale and unavailable projects remain explicit).
- `orchestration.scope_identity`: `projection` (durable project plus typed rooted/project scope; startup environment is not authority).
- `orchestration.scope_singleton`: `hybrid` (refreshed durable scope lease compared with live tmux runtime).
- `orchestration.project_completion`: `hybrid` (refreshed issue/review/interaction/session projections compared with live tmux runtime).
- `orchestration.parent_continuation`: `hybrid` (durable rooted lease/cursor + refreshed direct nested-root, interaction, completion, and session projections compared with live tmux before a wake prompt is delivered).
- `runtime.reconcile` includes `invariant_sources` debug output reflecting the active source-policy matrix.
- Treat this as the required cross-daemon safety contract for session/worktree/runtime invariants.

## Spec Records

- [Ticket terminology migration](25-ticket-terminology-migration.md) defines the canonical language and compatibility boundaries.

- `az spec read --json` reads daemon-backed requirement/link records.
- Markdown spec export is disabled until it can export the real stored spec data.
