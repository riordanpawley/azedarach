# Daemon Operations

This document defines the `ts-opentui` daemon operator workflow for CLI/TUI usage.

## Auto-Daemonize Policy

Auto-daemonize is enabled by default for CLI paths that interact with backend runtime/sync:
- `az` (default TUI launch path)
- `az sync`

When enabled, the CLI attempts to ensure daemon runtime/sync is started for the current project before continuing.

### Opt-Out Controls

- Per-command opt-out:
  - `--no-daemon`
- Environment opt-out:
  - `AZEDARACH_NO_DAEMON=true`
  - `AZEDARACH_DAEMON_MODE=off` (also accepts `disabled`)

### Environment Overrides

- Force enable:
  - `AZEDARACH_DAEMON_MODE=on` (also accepts `enabled`, `auto`)
- Optional interval override for auto-daemonized restarts:
  - `AZEDARACH_DAEMON_INTERVAL_MS=<positive-integer>`

If `AZEDARACH_DAEMON_INTERVAL_MS` is invalid, the CLI logs a warning and falls back to default interval behavior.

## Operator Commands

- `az daemon status`
  - Show runtime/sync state summary.
- `az daemon health`
  - Show aggregated health and reason.
  - Includes suggested diagnostics when degraded/unhealthy.
- `az daemon restart [--project-dir <path>] [--interval-ms <ms>]`
  - Restart runtime/sync using explicit or discovered project context.
- `az daemon stop`
  - Stop sync daemon runtime.
- `az daemon logs [--project-dir <path>] [--lines <n>]`
  - Tail daemon operation logs from `az-cli.log`.
  - Emits actionable guidance when logs are unavailable.

## Diagnostics Workflow

When daemon behavior looks wrong, use this order:
1. `az daemon health`
2. `az daemon status`
3. `az daemon logs --lines 100`
4. `az daemon restart --project-dir <path>`
