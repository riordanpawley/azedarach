# Boundary Hardening Policy (Draft)

## Goal

Make daemon authority boundaries mechanically enforceable, not convention-based.

This policy is a follow-up to:
- [docs/07-daemon-package-boundaries.md](07-daemon-package-boundaries.md)
- [docs/adr/1-daemon-ownership-adr.md](adr/1-daemon-ownership-adr.md)

## Target Shape

### Go (current repo)

Authoritative ownership:
- `internal/daemon/*` owns lifecycle + durable mutation authority.
- `internal/client/*` is the only frontend transport boundary.
- `internal/tui` and `internal/cli` are thin intent/render layers with ephemeral runtime state.

Hard import direction:
- `internal/tui`, `internal/cli` -> `internal/client/*`, `internal/contracts/*`, ui/domain helpers.
- `internal/tui`, `internal/cli` X> `internal/daemon/*`.
- `internal/daemon/*` X> Bubble Tea/UI packages.

## Enforcement Levels

### Level 0 (current baseline)

- Existing sentinel checks in `scripts/afv-drift-sentinel.sh` are required.
- Session projection checks in `internal/tui/model.go` run in hard-fail mode.

### Level 1 (implemented)

- Dedicated depguard lint config in `.golangci-boundary.yml`.
- Go package graph verifier in `scripts/check-boundaries.sh`.
- `just check-boundaries` runs depguard + graph verifier before tests.

### Level 2 (next)

- Add explicit exception allowlist entries with issue IDs and expiry dates.
- Wire `check-boundaries` as a required CI status gate for all Go boundary-touching PRs.

## Mandatory Guardrail Checks

Run in repo root:

```bash
cd . && just check-boundaries
```

If socket tests fail in sandbox, rerun with elevated permissions and record it in issue notes.

## Runtime Projection Consistency Pattern

All daemon runtime reads/mutations must follow this consistency split:

- Invariant source policy is explicit per check: `projection`, `tmux`, or `hybrid`.
- `projection`/`hybrid` checks must use refresh-then-cache (hydrate in-memory from durable projection, then evaluate).
- `tmux` checks must read live tmux runtime directly for runtime-presence decisions.

- Low-consistency reads (`*.list`, `*.status`, snapshot/export reads):
  - Use projection/cache reads only.
  - Do not fall back to live tmux/git/worktree probes inside read handlers.
- High-consistency mutations/invariant checks (`*.start`, `*.stop`, `*.remove`, etc):
  - Run a freshness barrier first (`ensureFreshRuntimeForMutation` / `runtime.reconcile`).
  - Wait for barrier completion before enforcing invariants or mutating state.

Rationale:
- Keeps read paths fast and deterministic.
- Prevents hidden authority probes in query paths.
- Centralizes live reconciliation in one explicit place with bounded timeout semantics.

## PR / Issue Checklist

Any boundary-touching change must include:
- commands run
- key outputs/assertions
- files changed
- explicit Runtime / Integration / Regression AC verdicts

And must not:
- reintroduce direct daemon-owned mutation authority into CLI/TUI active paths
- serialize UI framework message types over IPC
- rely on untyped or free-form boundary error parsing for control flow
