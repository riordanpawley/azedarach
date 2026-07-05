# dec-3: Use daemon-owned async notice records

- Created: 2026-07-05
- Updated: 2026-07-05

## Rationale

Async feedback needs stable IDs, lifecycle, operation linkage, recovery actions, dedupe, retention, restart recovery, and multi-client convergence that TUI-local history cannot provide.

## Context

ctw specifies the async notice architecture for CTG. Current v1 TUI state remains the migration source, while runtime implementation is split across ctx, cty, ctz, cua, and cub.

## Consequences

Daemon protocol/store/event compatibility expands; TUI becomes a projection renderer; producers must emit typed causes and dedupe keys.

## Links

- applies-to issue:ctw
- applies-to requirement:async-notice-action-center
- applies-to requirement:async-notice-daemon-contract
- applies-to requirement:async-notice-durable-store
- applies-to requirement:async-notice-tui-projection
- applies-to requirement:async-notice-validation

