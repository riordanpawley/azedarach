# Daemon Package Dependency Boundaries

## Purpose

Define hard package boundaries and import-direction rules for daemon split work so
parallel slices can land without architectural regression.

## Boundary Rules

1. Frontend boundary:
- `internal/tui` and `internal/cli` may import `internal/client/*`, `internal/contracts/*`, and UI/domain packages.
- `internal/tui` and `internal/cli` must not import `internal/daemon/*` or daemon-owned mutation services directly.

2. Daemon boundary:
- `internal/daemon/*` may import `internal/contracts/*`, `internal/ipc/*`, `internal/domain`, and service adapters.
- `internal/daemon/*` must not import Bubble Tea packages or UI packages.

3. Contract boundary:
- `internal/contracts/*` is transport/runtime agnostic and contains only typed payloads, protocol enums, and compatibility structures.
- `internal/contracts/*` must not import `internal/daemon/*` or `internal/tui`.

4. IPC boundary:
- `internal/ipc/*` handles framing, encode/decode, and client/server transport primitives.
- `internal/ipc/*` must not import UI packages or Bubble Tea message types.

5. Client boundary:
- `internal/client/*` adapts daemon interactions for CLI/TUI and owns reconnect + compatibility translation.
- `internal/client/*` must not mutate domain state directly; daemon remains authoritative.

## Import Direction (Allowed)

```text
internal/tui, internal/cli
  -> internal/client/*
  -> internal/contracts/*

internal/client/*
  -> internal/ipc/*
  -> internal/contracts/*

internal/daemon/*
  -> internal/contracts/*
  -> internal/ipc/*
  -> internal/domain
  -> service adapters

internal/contracts/*
  -> (stdlib only)
```

## Enforcement Checklist

1. No direct `internal/tui` or `internal/cli` imports of daemon-owned service mutation clients.
2. No `internal/daemon` imports of Bubble Tea or UI packages.
3. Cross-process payloads use `internal/contracts/*` types, never `tea.Msg`.
4. New handler/client code paths expose typed errors and revision metadata.

## Notes

These boundaries are strict for all new daemon-split work and should be used as
review gates for `aeg` and downstream implementation issues.
