# Shim Inventory (Historical)

Status: completed removal on 2026-03-19.

Purpose: historical record of shim groups that were deleted after package boundary cutover.

## Canonical Package Paths

- CLI entry/runtime: `packages/cli/src`
- TUI entry/runtime: `packages/tui/src`
- Daemon runtime/services: `packages/daemon/src`
- Shared daemon RPC contracts/client: `packages/shared/src/rpc`

## Deleted Shim Groups

1. `src/cli/*.ts`
2. `src/rpc/*.ts`
3. `src/daemon/*.ts`
4. Daemon-related `src/core` shim set:
`BackendDaemon*`, `BackendSyncDaemon*`, `Daemon*`, `DevServerDaemonService*`.

## Audit Commands

- Verify shim trees stay removed:
`fd . src/cli src/rpc src/daemon -t f`
- Verify daemon shim core files stay removed:
`fd '^(BackendDaemon|BackendSyncDaemon|Daemon|DevServerDaemonService).*\\.ts$' src/core -t f`
- Validate boundary policy:
`cd ts-opentui && bun run check:boundaries`
