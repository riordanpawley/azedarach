# Backend API Contract (Frontend-Agnostic)

## Purpose

This document defines the daemon-facing client session contract used by current `ts-opentui` surfaces and future non-TUI clients.

The contract is implemented by:
- `src/core/BackendClientSessionProtocol.ts`
- `src/core/BackendDaemonService.ts`

## Versioning Policy

- **Current protocol version**: `1`
- Clients negotiate via attach/reconnect handshake fields:
  - `requestedProtocolVersion`
  - `negotiatedProtocolVersion`
  - `serverSupportedProtocolVersions`
  - `compatibilityDecision`
- A version mismatch is rejected with tagged defect:
  - `_tag: "BackendDaemonProtocolVersionMismatchError"`
- Compatibility policy for now is exact-match only. Future versions may introduce compatible ranges while retaining these fields.

## Session Operations

### Attach

Attach establishes client identity and registers the client in daemon state.

Inputs:
- `clientId`
- `requestedAtMs` (optional, defaults to current time)
- `protocolVersion` (optional, defaults to current protocol version)
- `auth` (optional; defaults to trusted local client auth context)

Outputs:
- `resumeToken` (`<clientId>:<revision>`)
- handshake metadata
- negotiated capability payload
- daemon snapshot

### Reconnect

Reconnect re-establishes a known client and updates lifecycle/recovery tracking.

Inputs:
- same base fields as attach
- `lastSeenRevision` (optional)
- `lastSeenLifecycleGeneration` (optional)

Behavior:
- reconnect cursor fields are explicit and normalized
- if cursor fields are omitted, daemon preserves prior known cursor for that client

### Heartbeat

Heartbeat refreshes `lastHeartbeatAtMs` and uses the connected client's auth context for capability checks.

## Auth + Capability Model (Local Trust Boundary)

Protocol auth context:
- `actorId`
- `trustLevel`: `system | trusted-local | untrusted-local`
- `capabilities`: sorted capability list

Current capabilities:
- `session:attach`
- `session:reconnect`
- `session:heartbeat`
- `runtime:restart` (privileged)

Default client context is local/trusted and does **not** include `runtime:restart`.
System context includes all capabilities.

## Privileged Operation Gating

`runtime.restart` is capability-gated:
- allowed when actor has `runtime:restart`
- denied otherwise, with tagged defect:
  - `_tag: "BackendDaemonAuthorizationError"`

This gating is enforced in protocol/service layer, independent of UI surface.

## Auditing Hooks

Daemon state and snapshots include `auditEvents[]`.
Each event records:
- `operation`
- `actorId`
- `trustLevel`
- `capability` (or `null`)
- `outcome` (`allowed` / `denied`)
- `reason`
- timestamp

This supports local trust-boundary observability for both successful and denied actions.

## Frontend-Agnostic Client Feasibility Path

Any client (TUI, CLI, script, editor plugin, web bridge) can implement this flow:
1. Send `attach` with `clientId`, `protocolVersion`, optional auth metadata.
2. Persist `resumeToken`, `revision`, `lifecycleGeneration`.
3. Periodically send heartbeat.
4. Reconnect using prior cursor when re-attaching after interruption.
5. Attempt privileged operations only when capability is explicitly granted.

Because the contract is explicit and typed in core modules, non-TUI clients can reuse the same semantics without importing UI code.
