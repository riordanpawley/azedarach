# Boundary Hardening Policy (Draft)

## Goal

Make daemon authority boundaries mechanically enforceable, not convention-based.

This policy is a follow-up to:
- `docs/11-daemon-package-boundaries.md`
- `docs/12-daemon-ownership-adr.md`

## Target Shape

### Go (current repo)

Authoritative ownership:
- `internal/daemon/*` owns lifecycle + durable mutation authority.
- `internal/client/*` is the only frontend transport boundary.
- `internal/app` and `internal/cli` are thin intent/render layers with ephemeral runtime state.

Hard import direction:
- `internal/app`, `internal/cli` -> `internal/client/*`, `internal/contracts/*`, ui/domain helpers.
- `internal/app`, `internal/cli` X> `internal/daemon/*`.
- `internal/daemon/*` X> Bubble Tea/UI packages.

### TypeScript (future monorepo package split)

Recommended package boundaries:
- `@azedarach/protocol` (contracts only)
- `@azedarach/daemon` (authority runtime)
- `@azedarach/client` (rpc client boundary)
- `@azedarach/cli` (commands/presentation)
- `@azedarach/tui` (ui/presentation)

Hard dependency direction:
- `cli|tui` -> `client|protocol`
- `client` -> `protocol`
- `daemon` -> `protocol`
- forbidden: `cli|tui -> daemon`

## Enforcement Levels

### Level 0 (now)

- Existing sentinel checks in `scripts/afv-drift-sentinel.sh` are required.
- App-path checks run in advisory mode (warnings only).

### Level 1 (next)

- Upgrade app-path service-import checks to hard-fail.
- Add explicit forbidden scans for direct authority write patterns in `internal/app`.
- Wire sentinel into CI as required status check.

### Level 2 (next)

- Add compile-time package guard tests for import graph expectations.
- Add whitelist-based boundary exceptions with expiry dates.

## Mandatory Guardrail Checks

Run in repo root:

```bash
./scripts/afv-drift-sentinel.sh
cd go-bubbletea && go test ./internal/app ./internal/cli
cd go-bubbletea && go test ./internal/daemon/... ./internal/client/...
```

If socket tests fail in sandbox, rerun with elevated permissions and record it in issue notes.

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
