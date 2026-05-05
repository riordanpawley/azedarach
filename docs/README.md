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
- [adr/1-daemon-ownership-adr.md](adr/1-daemon-ownership-adr.md)

## Daemon Invariant Rule

- Every invariant must declare an explicit source policy: `projection`, `tmux`, or `hybrid`.
- For `projection` and `hybrid`, refresh in-memory cache from durable SQLite projections, then evaluate from the refreshed cache.
- For `tmux`, use live tmux runtime as source of truth (do not infer runtime presence from projection alone).
- Current source-policy examples:
- `session.start` conflict / `session.attach` target / `session.stop` targets: `tmux`.
- `session.recover` reconciliation: `hybrid` (projection intent + tmux runtime).
- `task.list` freshness/session timestamps: `projection` (refresh-then-cache).
- `runtime.reconcile` includes `invariant_sources` debug output reflecting the active source-policy matrix.
- Treat this as the required cross-daemon safety contract for session/worktree/runtime invariants.

## Generated Spec Docs

- `az spec sync --target md` generates deterministic Markdown under `docs/spec/`.
- When `docs/spec/` is present, treat it as the spec source of truth for synced command grammar/status.
- [spec/README.md](spec/README.md)
