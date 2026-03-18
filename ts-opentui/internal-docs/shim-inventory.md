# Shim Inventory (Transitional)

Status: active transitional compatibility layer after package cutover.

Purpose: preserve existing import paths while canonical runtime code lives in
`packages/{cli,tui,daemon,shared}`.

## Canonical Package Paths

- CLI entry/runtime: `packages/cli/src`
- TUI entry/runtime: `packages/tui/src`
- Daemon runtime/services: `packages/daemon/src`
- Shared daemon RPC contracts/client: `packages/shared/src/rpc`

## Shim Groups

1. `src/cli/*.ts`
- Role: compatibility re-exports to `packages/cli/src/*`.
- Owner: CLI package maintainers.
- Removal criterion: no imports from `src/cli/*` outside shim tests/docs.

2. `src/core/BackendDaemon*.ts`, `src/core/Daemon*.ts`, `src/core/DevServerDaemonService.ts`, `src/core/GlobalDaemonRegistry.ts`
- Role: compatibility re-exports to `packages/daemon/src/*`.
- Owner: Daemon package maintainers.
- Removal criterion: all daemon runtime/test imports switch to package paths or approved facade.

3. `src/daemon/GlobalDaemonMain.ts`, `src/daemon/GlobalDaemonServer.ts`
- Role: compatibility re-exports to `packages/daemon/src/*`.
- Owner: Daemon package maintainers.
- Removal criterion: bootstrap/test invocations no longer reference `src/daemon/*`.

4. `src/rpc/DaemonRpcClient.ts`, `src/rpc/DaemonRpcSchemas.ts`, `src/rpc/DaemonRpcs.ts`
- Role: compatibility re-exports to `packages/shared/src/rpc/*`.
- Owner: Shared package maintainers.
- Removal criterion: all call sites import from `@azedarach/shared/rpc` or package-local shared facade.

## Audit Commands

- Find remaining shim consumers:
`rg -n "src/(cli|core|daemon|rpc)/" ts-opentui/src ts-opentui/packages ts-opentui/bin`
- Validate boundary policy:
`cd ts-opentui && bun run check:boundaries`

