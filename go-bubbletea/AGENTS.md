<!--
File: CONTEXT.md
Version: 1.1.0
Updated: 2026-03-07
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
