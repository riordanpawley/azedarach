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
- [25-rootless-orchestrator-contracts.md](25-rootless-orchestrator-contracts.md)
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
- `task.close`, `task.close_preflight`, `task.delete`, `task.delete_preflight`, `task.graph_readiness`, and `task.complete_check`: `hybrid` (durable issue graph/v2 lifecycle projection + live runtime attachment state).
- `task.review_handoff`: `projection` (durable issue v2 lifecycle/review projection + session activity projection; active issue self-handoff remains allowed).
- `task.integration_readiness` and `task.context_risk_closeout`: `projection` (durable issue projection + mailbox/observation evidence).
- `task.merge_base_target`: `projection` (durable issue graph + worktree projection).
- `task.follow_on_merge_candidates`: `projection` (durable issue graph + worktree projection).
- `issue_resources.lifecycle`: `projection` (durable issue status + runtime attachment projection).
- `interaction.waiting_human`: `projection` (durable interaction requests refreshed before decision-waiting and pickup evaluation).
- `interaction.staleness`: `projection` (durable interaction requests refreshed before age evaluation and revision-safe stale/reminder/disposition/recovery audit writes).
- `task.list` freshness/session timestamps: `projection` (refresh-then-cache).
- `orchestration.scope_identity`: `projection` (durable project plus typed rooted/project scope; startup environment is not authority).
- `orchestration.scope_singleton`: `hybrid` (refreshed durable scope lease compared with live tmux runtime).
- `orchestration.project_completion`: `hybrid` (refreshed issue/review/interaction/session projections compared with live tmux runtime).
- `runtime.reconcile` includes `invariant_sources` debug output reflecting the active source-policy matrix.
- Treat this as the required cross-daemon safety contract for session/worktree/runtime invariants.

## Spec Records

- `az spec read --json` reads daemon-backed requirement/link records.
- Markdown spec export is disabled until it can export the real stored spec data.
