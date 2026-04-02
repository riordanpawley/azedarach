# ADR: Daemon Ownership Boundary

## Status

Accepted

## Context

The legacy go-bubbletea implementation keeps lifecycle and mutation authority in
frontend paths (`internal/tui` and `internal/cli`), which makes multi-client
coherence and restart/reconnect behavior hard to guarantee.

The daemon split requires:
- one authoritative writer for lifecycle and board mutations
- thin frontend clients
- typed IPC contracts and deterministic revision semantics

## Decision

Adopt a daemon-authoritative architecture with explicit ownership boundaries:

1. Daemon owns:
- session/worktree/devserver mutation authority
- background orchestration and lifecycle transitions
- revision sequencing and event publication
- runtime projection persistence and publish flow through one daemon-owned writer

Drift sentinel markers:
- `worktree.cleanup_orphaned`
- `task.snapshot.export`
- `task.bulk.apply`

2. Frontends own:
- rendering, keyboard/input handling, local UI projection state
- intent construction and response presentation

3. Shared contracts own:
- typed request/response/event envelopes
- handshake compatibility metadata and typed error taxonomy

## Consequences

Positive:
- deterministic multi-client state convergence
- clear replay/rehydrate model using snapshot + revisioned events
- reduced frontend coupling to side-effectful services

Tradeoffs:
- more explicit client transport/reconnect complexity
- daemon lifecycle and observability become critical-path responsibilities

## Non-goals

- Moving Bubble Tea UI logic into the daemon process.
- Encoding UI-specific message types over IPC.
- Preserving direct frontend mutation calls in migrated paths.

## Implementation Notes

- `internal/daemon/*` and `internal/client/*` are the core split packages.
- `internal/contracts/*` remains transport-agnostic and versioned.
- Frontend command paths must migrate to daemon RPC calls only.
- Runtime projection writes must flow through `internal/daemon/runtime_projection_writer.go` so persist, revision, and publish stay ordered in one place.
