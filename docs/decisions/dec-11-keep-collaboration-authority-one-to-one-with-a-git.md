# dec-11: Keep collaboration authority one-to-one with a Git repository

- Created: 2026-07-12
- Updated: 2026-07-12

## Rationale

Keep one collaboration deployment authoritative for exactly one Azedarach project, and keep one project permanently mapped to exactly one Git repository. This matches the strongest current authority boundary and avoids introducing a workspace-of-projects domain solely for collaboration. A developer may connect their local registry to several unrelated local or collaborative project authorities.

## Context

The existing Azedarach project owns one Git repository root, issue graph/database, worktree and session namespace, orchestration scope, specs, decisions, learnings, and runtime invariants. The global projects registry is a local aggregation convenience, not a durable shared workspace.

## Consequences

Projects remain independently hosted, authorized, exported, backed up, restored, migrated, and deleted. The project service does not need a repository checkout and does not become Git authority. Cross-project global views remain client-local. Cross-project dependencies or a team hub may later use project-qualified federated links, but are not part of the initial collaboration boundary.

## Links

- applies-to issue:dda
- applies-to issue:dgv — Event streams and projections remain project scoped: one deployment, project, and repository.
