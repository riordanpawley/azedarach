<!--
File: CONTEXT.md
Version: 2.0.0
Updated: 2026-03-07
Purpose: go-bubbletea overlay context with no shared-policy duplication
-->

<ai_context version="1.0" tool="shared">

# Azedarach go-bubbletea Overlay

> Implementation-specific overlay for `go-bubbletea/`.

## Scope

This file intentionally contains only go-bubbletea-specific guidance.
Shared repository workflow, issue tracking, git, and RuleSync policies live only in `../CONTEXT.md`.

## go-bubbletea Rules

1. **Project Layout**:
   - Keep entrypoint wiring in `cmd/`.
   - Keep private implementation in `internal/`.
   - Keep docs in `docs/`.
2. **Dependency Injection**:
   - Accept interfaces at boundaries.
   - Return concrete structs from constructors.
   - Keep external process adapters mockable (`git`, `tmux`, `az issue`).
3. **Constructor Pattern**:
   - Use functional options for optional configuration.
4. **Bubbletea Architecture**:
   - Use nested models and explicit message routing.
   - Keep shared state explicit; avoid duplicated mutable state.
5. **Initialization**:
   - Batch startup commands with `tea.Batch(...)`.
6. **Context Propagation**:
   - Pass `context.Context` first for I/O and long-running work.
7. **Error Handling**:
   - Return errors instead of swallowing.
   - Wrap with operation context (`fmt.Errorf("...: %w", err)`).
   - Use structured logging (`slog`).
8. **Testing**:
   - Prefer table-driven tests.
   - Mock dependencies through interfaces.
   - Keep tests deterministic and fast.
9. **Concurrency**:
   - Use context cancellation for goroutine lifecycle.
   - Use buffered channels when capacity is known.

## Structure Reference

```text
go-bubbletea/
├── cmd/              # app entrypoints
├── internal/app/     # bubbletea model/update/view
├── internal/services/# integrations (git, tmux, az issue)
├── internal/types/   # domain types
└── internal/ui/      # bubbletea views/components
```

</ai_context>
