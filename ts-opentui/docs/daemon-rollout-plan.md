# Daemon Architecture Rollout and Rollback Plan

This document defines the rollout plan for moving `az` to a persistent daemon-backed runtime model in `ts-opentui`, including measurable gates, rollback paths, and release risks.

## Scope

- In scope:
  - Daemon bootstrap/lifecycle supervision
  - Client session protocol + compatibility policy
  - Auto-daemonize behavior for CLI/TUI invocation paths
  - Operational controls and observability
  - Durable daemon state and restart recovery
  - Multi-client (CLI + TUI) coherence validation
- Out of scope:
  - Remote daemon deployments
  - Cross-machine trust/authentication beyond local host assumptions

## Release Phases

### Phase 0: Foundation (Completed)

- Deliverables:
  - Singleton lock/discovery
  - Lifecycle state machine
  - Crash/backoff policy
  - Typed attach/reconnect handshake
  - Daemon status/health/stop/restart surfaces
  - Integration harness for supervisor behavior
- Gate checks:
  - `bun test src/core/DaemonSupervisor.integration.test.ts`
  - `bun test src/core/BackendDaemonService.test.ts src/core/BackendSyncDaemonService.test.ts`
  - `bun run type-check`

### Phase 1: Protocol + Trust Boundary

- Deliverables:
  - Unified client attach/session contract used by all client entrypoints
  - Capability model for privileged operations
  - Contract documentation with compatibility rules
- Entry gate:
  - Foundation phase green
- Exit gate:
  - Protocol tests green
  - Capability checks enforced in service tests
  - Contract document published under `docs/`

### Phase 2: Auto-Daemonize + Operations

- Deliverables:
  - Default auto-attach/start policy for normal CLI/TUI execution
  - Explicit daemon bypass (`--no-daemon` and/or env toggle)
  - Operational diagnostics (status/log/restart/health)
- Entry gate:
  - Phase 1 complete
- Exit gate:
  - CLI policy tests pass (default and opt-out)
  - Operations command tests pass
  - Operator runbook published

### Phase 3: Durable Runtime State

- Deliverables:
  - Versioned persisted daemon state schema
  - Crash-safe write protocol (temp-file + atomic rename)
  - Restart recovery semantics validated
- Entry gate:
  - Phase 2 complete
- Exit gate:
  - Persistence tests (normal, restart, corrupted state) pass
  - Recovery behavior is deterministic

### Phase 4: Multi-Client Convergence

- Deliverables:
  - CLI + TUI consuming shared daemon-backed service path
  - E2E harness for mixed-client workflows and reconnect
- Entry gate:
  - Phase 3 complete
- Exit gate:
  - Mixed-client E2E suite green
  - Regression coverage included in CI gate set

## Rollback Strategy

### Rollback Levels

1. Feature rollback:
- Disable auto-daemonize policy and force direct/no-daemon operation path.
- Keep daemon command surfaces available for diagnosis.

2. Runtime rollback:
- Stop daemon and clear lock/state artifacts.
- Route clients to legacy in-process flows where available.

3. Code rollback:
- Revert to pre-rollout commit set for daemon integration wave.
- Re-run type-check + core tests before redeploy.

### Operator Rollback Playbook

1. Assess health:
- `az daemon health --verbose`
- `az daemon status --verbose`

2. Stop daemon safely:
- `az daemon stop`

3. Disable daemonized path for immediate mitigation:
- Use `--no-daemon` execution mode for affected commands

4. Recover state if needed:
- Remove stale lock if process is confirmed dead
- Restore from last known-good persisted state snapshot (Phase 3 store)

5. Verify post-rollback stability:
- `bun run type-check`
- Targeted command and integration tests

## Risk Register

- Protocol drift between clients and daemon
  - Mitigation: strict protocol version checks + compatibility metadata
- Stale lock ownership blocks startup
  - Mitigation: liveness checks + stale lock recovery path
- Crash loops degrade UX and data freshness
  - Mitigation: bounded backoff + degraded/crashed states + observability commands
- Incomplete client migration causes split behavior
  - Mitigation: explicit convergence gates before marking rollout complete
- Persisted state corruption
  - Mitigation: schema versioning + atomic writes + corruption fallback tests

## Quality Gates Summary

- Required on each rollout increment:
  - `bun run type-check`
  - Targeted `bun test` suites for touched daemon/core/cli areas
- Required before full rollout completion:
  - Supervisor integration harness
  - Multi-client E2E harness
  - Rollback drill checklist execution

## Completion Criteria

Rollout is complete when:
- All phase exit gates are satisfied.
- Rollback procedures are documented and successfully exercised.
- Risks have owner + mitigation status tracked in issue notes.
