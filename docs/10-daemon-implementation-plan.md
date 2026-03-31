# Daemon Split Implementation Plan (go-bubbletea)

## Scope

Implement daemon-authoritative backend with thin TUI/CLI clients, aligned with:

- `go-bubbletea/docs/09-daemon-battle-tested-path.md`
- `req-backend-authoritative-runtime`
- `fr-daemon-thin-client-rpc-boundary`
- `req-daemon-client-migration-ipc-only`
- `req-daemon-typed-ipc-msgpack-contract`
- `req-backend-multi-client-coherence`

## Current-State Baseline

- Runtime orchestration is currently in-process in:
  - `internal/app/model.go`
  - `internal/cli/commands.go`
- Service clients are constructed directly in frontend paths:
  - beads/tmux/git/pr/devserver/worktree/monitor
- No dedicated daemon process, IPC transport package, or shared contract package exists yet.

## Target Package Map

```text
go-bubbletea/
├── cmd/
│   ├── az/                    # user-facing CLI entrypoint (client mode)
│   └── azd/                   # daemon entrypoint
├── internal/
│   ├── contracts/
│   │   ├── protocol/          # envelope, handshake, error codes, versioning
│   │   ├── commands/          # typed command payloads
│   │   ├── events/            # typed event payloads
│   │   └── snapshots/         # typed snapshot schemas
│   ├── ipc/
│   │   ├── transport/         # unix socket listener/client, framing, deadlines
│   │   ├── codec/             # messagepack encode/decode
│   │   ├── server/            # request dispatch + subscription hubs
│   │   └── client/            # request/response + stream subscribe
│   ├── daemon/
│   │   ├── lifecycle/         # singleton lock, autostart helpers, idle shutdown
│   │   ├── runtime/           # daemon process wiring + supervisor
│   │   ├── state/             # authoritative state store + revision sequencing
│   │   ├── handlers/          # command handlers
│   │   └── publish/           # event fanout + snapshot generation
│   ├── client/
│   │   ├── daemonclient/      # shared thin client for TUI/CLI
│   │   ├── reconnect/         # attach/retry/backoff/resume
│   │   └── compatibility/     # handshake compatibility decisions
│   ├── app/                   # bubbletea model + ui-only logic
│   ├── cli/                   # cli command UX only
│   ├── services/              # transitional local services (shrinking scope)
│   ├── domain/                # domain types
│   └── ui/
```

## Ownership Matrix

- Daemon-only ownership:
  - session/worktree/devserver lifecycle
  - sync/reconciliation/background jobs
  - authoritative board/task/session state + revisioning
  - domain mutation validation and execution
- Frontend ownership:
  - rendering, key handling, local UI interaction state
  - command intent construction
  - daemon response/event presentation
- Shared ownership:
  - typed contracts and protocol enums/constants
  - typed error model

## Forbidden Dependencies

- `internal/app` and `internal/cli` MUST NOT import daemon-owned mutation services directly (`tmux`, `git`, `worktree`, `pr`, `devserver`, monitor orchestration) once migrated.
- `internal/daemon` MUST NOT import Bubble Tea packages.
- Cross-process payloads MUST NOT use `tea.Msg` types.

## Core Interfaces (Initial)

```go
// internal/client/daemonclient/client.go
type DaemonClient interface {
    Handshake(ctx context.Context, hello protocol.Hello) (protocol.HelloAck, error)
    Command(ctx context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error)
    Snapshot(ctx context.Context, req snapshots.ReadRequest) (snapshots.BoardSnapshot, error)
    Subscribe(ctx context.Context, req events.SubscribeRequest) (<-chan events.Envelope, error)
}
```

```go
// internal/daemon/state/store.go
type Store interface {
    ReadSnapshot(ctx context.Context, projectID string) (snapshots.BoardSnapshot, error)
    ApplyCommand(ctx context.Context, cmd commands.Envelope) (commands.Result, []events.Envelope, error)
    CurrentRevision(projectID string) uint64
}
```

## Streaming + Freshness Model

- Attach flow:
  - client connects + handshake
  - client requests snapshot and receives `revision`
  - client subscribes to stream with `from_revision`
- Event flow:
  - daemon emits ordered domain events with monotonic per-project revision
  - client applies only `revision > last_applied_revision`
- Recovery flow:
  - on gap, client requests catch-up or full snapshot rehydrate
  - on disconnect, client reconnects with bounded backoff and resume token
- Degraded mode:
  - if stream unavailable, client falls back to periodic snapshot pull

## Upgrade and Compatibility Policy

- Compatibility gate is handshake-only.
- Version mismatch behavior:
  - client attempts controlled daemon replacement (stop/start/reconnect once)
  - if still incompatible, return typed upgrade-required error
- Request/event envelopes carry protocol version metadata as contract fields.

## Migration Phases (Execution)

1. Phase A: boundary freeze + package scaffolding
2. Phase B: protocol/contracts + codec + handshake tests
3. Phase C: daemon runtime singleton/autostart/idle shutdown
4. Phase D: lifecycle authority migration (session/worktree/devserver)
5. Phase E: TUI/CLI migration to shared daemon client
6. Phase F: coherence and convergence validation + spec closure

## Test Plan

- Unit:
  - codec roundtrip and malformed frames
  - handshake accept/reject matrix
  - revision ordering and gap detection
  - singleton lock/stale lock behavior
- Integration:
  - client autostart races (N clients attaching simultaneously)
  - daemon restart with client reconnect and rehydrate
  - stream drop/overflow fallback behavior
  - multi-client coherence on overlapping mutations
- Regression:
  - preserve existing user-facing command/key semantics unless explicitly changed

## Rollout and Rollback

- Rollout strategy:
  - behind feature flag (`daemon.enabled`)
  - dual-path phase where legacy in-process path is still available
  - migration complete only when CLI/TUI default to daemon path
- Rollback:
  - immediate fallback to legacy path if daemon health checks fail hard
  - preserve typed diagnostics for user-visible failure reason

## Completion Gates

- No direct frontend mutation authority imports remain in migrated paths.
- Daemon/backend spec links updated with evidence in each completed slice.
- Required acceptance scenarios for daemon/backend are moved from planned to partial/complete with proof.
