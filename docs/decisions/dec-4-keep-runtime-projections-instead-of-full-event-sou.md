# dec-4: Keep runtime projections instead of full event sourcing

- Created: 2026-07-07
- Updated: 2026-07-07
- Revised by: dec-5

## Rationale

Runtime state includes live external authorities such as tmux, worktrees, and git. Historical daemon events cannot prove current runtime truth without reconciliation, while current SQLite projections plus revisioned streams already provide client convergence. Keep projections hot and add only a narrow append-only runtime evidence ledger if a concrete replay or audit consumer appears.

## Context

Issue cro evaluated full event sourcing for daemon runtime state. Current code persists current session/worktree/git projections through internal/daemon/runtime_projection_writer.go, publishes revisioned stream events for client convergence, and uses projection/tmux/hybrid invariant source policy.

## Consequences

Runtime stream events remain client convergence signals rather than durable replay authority. Startup and repair continue to reconcile projections against tmux/worktree/git. Future ledger work must be additive behind the daemon writer, replay only into projections, and define retention for noisy telemetry.

## Links

- applies-to issue:cro
- informs issue:dgv — Historical runtime-only decision; superseded by dec-5 for whole authority-plane evaluation, but its external-truth and telemetry cautions remain relevant.
