# ADR: Daemon-Owned Async Notices

## Status

Accepted

## Context

Async mutation feedback currently spans several TUI-owned paths: task-local
mutation failure state, workspace failure detail, floating toasts, notification
history, and footer attention counts. This gave users better immediate feedback,
but the durable lifecycle is still local to one TUI process.

The parent async-notice work requires feedback to survive daemon and TUI restart,
converge across multiple TUI clients, link to daemon operations, expose typed
recovery actions, and keep the footer as a compact route indicator.

## Decision

Represent async user feedback as daemon-owned notice records. The daemon owns
stable IDs, source operation linkage, typed cause, severity, lifecycle state,
read/dismissed/resolved state, dedupe, retention, action validation, SQLite
persistence, and event publication.

The TUI becomes a projection renderer. It maps daemon notices and operation
state into task card markers, workspace detail, floating toasts, notification
history/action center rows, and footer counts. It does not generate durable
notice IDs or own durable lifecycle state.

## Consequences

Positive:

- notices survive daemon/TUI restart
- multiple TUI clients converge on read/dismissed/resolved state
- operation failures and recovery actions have stable typed context
- notification history can become an action center without adding UI-owned
  authority
- footer remains compact while preserving routes to detailed feedback

Tradeoffs:

- notice store, protocol, and event-stream compatibility become part of the
  daemon contract
- producers must provide dedupe keys and typed causes
- TUI projection needs a migration bridge from current local notification fields
  until daemon notices are fully wired

## Non-Goals

- Replacing daemon operation records or logs.
- Streaming Bubble Tea message types over daemon protocol.
- Reintroducing full async message bodies into the footer.
- Implementing the runtime store/protocol/TUI migration in this ADR slice.

## Implementation Notes

- The detailed contract lives in
  [docs/18-async-notice-architecture.md](../18-async-notice-architecture.md).
- `ctx` should implement the durable store and protocol shape.
- `cty` should emit notices from daemon mutation and operation paths.
- `ctz` should migrate TUI surfaces to the daemon projection.
- `cua` should implement action-center routing.
- `cub` should validate restart, multi-client, stale-operation, dedupe,
  viewport, and boundary behavior.
