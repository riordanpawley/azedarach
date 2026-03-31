# Daemon Battle-Tested Path (Go + Bubble Tea)

## Goal

Move Azedarach to a production-safe split architecture:

- single daemon backend per project namespace
- thin TUI/CLI clients
- typed MessagePack IPC boundary
- robust reconnect/autostart/idle-shutdown lifecycle

## Architecture Boundaries

- Daemon owns authoritative runtime state and side effects:
  - session lifecycle
  - worktree lifecycle
  - dev server lifecycle
  - background sync and reconciliation
- Frontends (TUI/CLI) own presentation and user intent only:
  - collect input
  - send typed commands
  - render command results/events

## IPC Pattern (Battle-Tested)

- Transport:
  - Unix domain socket per project namespace
  - explicit socket path/version namespace
- Framing:
  - length-prefixed frames (fixed-size header + payload)
  - one MessagePack envelope per frame
- Envelope shape:
  - `protocol_version`
  - `message_type` (`request|response|event|error|hello|hello_ack|ping|pong`)
  - `request_id` (for request/response correlation)
  - `method`
  - `payload`
  - `error` (structured code + message + retriable flag)
- Handshake:
  - client sends `hello` with protocol + capability versions
  - daemon returns `hello_ack` with accept/reject reason
  - compatibility decision is handshake-only (not ad-hoc runtime parsing)
- Reliability:
  - deadlines on all socket I/O
  - heartbeat (`ping/pong`) for liveness
  - bounded request queue and backpressure errors

## Daemon Lifecycle Pattern (Battle-Tested)

- Singleton:
  - lock file + PID ownership check for duplicate suppression
  - stale lock recovery after PID non-existence validation
- Startup:
  - client connect -> if unavailable -> controlled autostart -> retry attach with backoff
  - autostart path guarded by `singleflight` to prevent stampede
- Runtime:
  - `context.Context` root + `errgroup` for goroutine supervision
  - explicit worker pools for blocking side effects
  - structured logs with operation/request IDs
- Shutdown:
  - idle timer (default 45 minutes)
  - graceful drain: stop new requests, finish/cancel in-flight, flush state, close socket
  - signal handling (`SIGTERM`, `SIGINT`) with same graceful path
- Recovery:
  - client reconnect with jittered exponential backoff
  - idempotent command semantics where feasible

## Bubble Tea Client Pattern (Battle-Tested)

- TUI:
  - all daemon calls wrapped as `tea.Cmd` returning typed `tea.Msg`
  - no direct tmux/git/worktree mutations in model layer
  - event stream converted into typed update messages
  - keep Bubble Tea model local; do not stream raw `tea.Msg` from daemon
  - daemon streams domain events; client maps events to local UI state transitions
- CLI:
  - thin command handlers that call daemon client methods only
  - no direct ownership of backend lifecycle resources
- Common:
  - shared daemon client package for connect/attach/reconnect + handshake
  - typed error mapping to actionable UX states (offline, incompatible, timeout, busy)

## Rollout Order

1. Boundary freeze and migration map
2. Shared IPC contract + codec + handshake tests
3. Singleton daemon process manager + autostart + idle shutdown
4. Move lifecycle authority (session/worktree/devserver) into daemon
5. Rewire TUI/CLI to thin client boundary
6. Convergence: tests, spec links, residual gap issues

## Required Gates Per Slice

- unit tests for new contract/lifecycle logic
- integration tests for reconnect/autostart/idle-shutdown
- no new direct frontend imports of backend side-effect services
- `az spec` link updates with evidence notes in the same slice
