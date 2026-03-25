<!--
File: CONTEXT.md
Version: 1.2.0
Updated: 2026-03-25
Purpose: go-bubbletea overlay context synced to nested AGENTS.md
-->

<ai_context version="1.0" tool="shared">

# Azedarach go-bubbletea Overlay

> Go/Bubbletea implementation-specific guidance for `go-bubbletea/`

## Shared Baseline

Shared repository workflow and policy rules are already loaded before this overlay and apply here:
- issue tracking (`az prime`, `az issue`)
- git workflow and safety constraints
- completion/commit discipline
- cross-repo Codex context workflow

This file is intentionally an overlay with go-bubbletea-specific rules only.

## Critical go-bubbletea Rules

1. **Project Layout**:
   - `cmd/` for entrypoints and wiring
   - `internal/` for private implementation
   - `docs/` for implementation docs
2. **Dependency Injection**:
   - Accept interfaces, return concrete structs.
   - Keep external command/tmux/git clients mockable.
3. **Functional Options**:
   - Use `Option` functions for constructors with optional settings.
4. **Bubbletea Model Structure**:
   - Use nested models with clear routing.
   - Keep shared state explicit and avoid duplicated mutable state.
5. **Initialization**:
   - Batch sub-model init commands via `tea.Batch(...)`.
6. **Context Propagation**:
   - Pass `context.Context` as the first parameter for I/O or long-running operations.
7. **Error Handling**:
   - Return and wrap errors with context (`fmt.Errorf("...: %w", err)`).
   - Prefer structured logging via `slog`.
8. **Testing**:
   - Keep tests deterministic and fast.
   - Use table-driven tests and interface-based mocks.
9. **Concurrency**:
   - Prefer context cancellation for goroutine lifecycle.
   - Use buffered channels when bounded capacity is known.
10. **Architecture Migration Gates**:
   - Do not mark a migration complete while active entrypoints still depend on transitional adapters or legacy execution paths.
   - Runtime AC must prove the intended production path is wired on active entrypoints, not only in isolated packages/tests.
   - Cross-process boundaries must use typed protocol/domain payloads rather than UI-framework message types.
11. **Active-Path Placeholder Policy**:
   - Placeholder implementations are allowed only when they are off active runtime paths or explicitly tracked as incomplete follow-up work.
   - If an active-path placeholder remains, the issue must stay partial/in-progress with linked follow-up issue IDs.
12. **Closure Evidence (Required)**:
   - For architecture issues, close only after notes include commands run, key outputs, files changed, and explicit AC pass/fail checklist.
13. **Binary Boundary (Critical)**:
   - PATH `az` belongs to the TypeScript implementation (`ts-opentui`), not `go-bubbletea`.
   - Never use PATH `az` to test Go CLI/TUI/daemon behavior.
   - For Go validation, use `go run ./cmd/az ...`, `go run ./cmd/azd ...`, or `./bin/az ...` from `go-bubbletea/`.
14. **Daemon Restart Policy**:
   - For operational daemon restarts, use `az daemon restart` (from `go-bubbletea/` with the Go binary/path).
   - Do not bump protocol/version just to force restarts; version bumps are for contract changes.

## Thin-Client Boundary Contract (Critical)

This section is non-optional for all `go-bubbletea` architecture work.

1. **Authority Ownership**:
   - Daemon owns durable/project state and lifecycle authority (tasks, session lifecycle, worktree lifecycle, devserver lifecycle).
   - CLI and TUI are clients only. They may hold presentation/runtime-ephemeral state only (cursor position, overlays, viewport, transient loading flags).
2. **No Direct Authority Operations in TUI/CLI**:
   - `internal/app` and `internal/cli` must not directly execute authority operations through `internal/services/git`, `internal/services/tmux`, `internal/services/issues`, `internal/services/devserver`, or `internal/services/pr` when a daemon command path exists.
   - Boundary operations must go through `internal/client/daemonclient` and typed protocol contracts.
3. **Daemon Routing Requirement**:
   - New or changed boundary operations must be represented as daemon commands with typed request/response payloads.
   - Runtime daemon command paths must be wired through active handlers on production entrypoints; test-only handlers do not satisfy this.
4. **Session Authority Rule**:
   - Session lifecycle transitions must be daemon-authoritative.
   - TUI session maps are projections only; do not perform local writes that establish or mutate lifecycle authority.
   - `internal/app` may stop or clear `SessionMonitor` state during teardown, but it must not call `SessionMonitor.Start` or recreate lifecycle monitoring locally.
   - Any `m.sessions` mutation must come from daemon snapshot refresh (`projectSessionProjection`), not from per-session authority writes or monitor callbacks.
5. **Singleton Scope Rule**:
   - If daemon runtime assets are user-global (socket/lock), design changes must explicitly preserve project isolation semantics and avoid cross-repo authority bleed.
6. **Required Drift Guards**:
   - Boundary changes must include regression guards that fail when direct authority operations reappear in client layers.
   - Keep migration/boundary guard tests in `internal/app` and `internal/cli` current with each boundary change.
   - Guard coverage for session projection must fail if `model.go` regains `SessionMonitor.Start` or direct `m.sessions[...] =` / `delete(m.sessions, ...)` authority writes.
7. **Boundary Evidence Before Close**:
   - For any boundary issue, notes must include:
     - commands run
     - key outputs/assertions
     - files changed
     - explicit AC verdicts for runtime, integration, and regression checks
   - If any are missing, keep the issue in `in_progress` or `blocked`.

## Boundary Verification Checklist (Required)

Run this checklist whenever touching daemon/client boundaries:

```bash
cd go-bubbletea

# 1) Guard rails remain active
go test ./internal/app ./internal/cli

# 2) Daemon boundary behavior remains correct
go test ./internal/daemon/... ./internal/client/...

# 3) Full boundary-facing integration safety net
go test ./internal/app ./internal/cli ./internal/daemon/...
```

If sandbox restrictions prevent unix socket integration tests, rerun with the required approval path and record that in issue notes.

## Quick Commands

```bash
# in go-bubbletea/
cd go-bubbletea

make build
make test
make run

# focused search
rg "pattern" --type go internal cmd
fd "filename" -t f internal cmd
```

## Architecture Quick Reference

```text
go-bubbletea/
├── cmd/              # app entrypoints
├── internal/app/     # bubbletea app flow
├── internal/services/# domain integrations
├── internal/types/   # domain types
└── internal/ui/      # bubbletea views/components
```

## Quick Help

- go docs: `docs/`

</ai_context>
