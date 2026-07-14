---
name: jaeger-otel-diagnostics
description: Diagnose Azedarach command latency and daemon/TUI performance with OpenTelemetry traces exported to a local Jaeger backend. Use when investigating slow `az` commands, TUI stalls, daemon RPC latency, missing spans, local Jaeger setup, or trace-driven performance regressions in this repository.
---

# Jaeger OTel Diagnostics

## Overview

Use local Jaeger as the first inspection surface for Azedarach latency work. Keep the investigation bounded to low-cardinality command shapes, request IDs, span durations, and daemon/client boundaries.

## Quick Start

1. Confirm tracing is enabled:

```bash
echo "$AZEDARACH_OTEL"
echo "$AZEDARACH_LATENCY_TRACE"
az config set diagnostics.latencyTrace true
```

2. Start the repository-managed pinned Jaeger v2 collector:

```bash
just build-install-run
```

The lifecycle script publishes the actual OTLP endpoint to the launched
daemon/TUI, including when a bounded ephemeral fallback must use dynamic ports.
Use `just jaeger-inventory` before `just jaeger-cleanup --confirm` to inspect
inactive legacy fallback stores. Cleanup preserves the selected primary store
and every volume still attached to a container.

3. Reproduce the slow path with low-noise commands:

```bash
AZEDARACH_OTEL=true go run ./cmd/az issue get cxk
AZEDARACH_OTEL=true go run ./cmd/az prime
```

4. Open the UI URL printed by startup, select `az` or `azd`, use a 15-minute
   lookback and a limit of 10, then filter by operation/span names such as
   `cli.command`, `cli.daemonclient.command`, `cli.transport.command`,
   `daemon.ipc.command`, `daemon.command`, `daemon.command.dispatcher_handle`,
   `daemon.operation.run`, `dependency.git`, `dependency.tmux`,
   `dependency.http`, and `dependency.sqlite.issue.runtime_projection`.

## Investigation Workflow

Start with service separation:

- `az`: CLI and TUI process startup, command execution, daemon client attempts.
- `azd`: daemon command receipt, begin-command contention, dispatcher handling.

Check whether the CLI span time is mostly before, during, or after `cli.daemonclient.command` and `cli.transport.command`. If the command attempt dominates, switch to `azd` traces using the shared `request_id`.

Use `command_shape` instead of raw argv when grouping slow commands. Treat missing `request_id` as evidence that the path did not cross the daemon RPC boundary.

For daemon bottlenecks:

- Long `daemon.command.begin` usually points to command concurrency/backpressure.
- Long `daemon.command.dispatcher_handle` points to domain/store/runtime work behind the typed daemon command.
- Long `daemon.operation.run` points to asynchronous operation execution after daemon admission.
- Long `dependency.sqlite.*` points to issue-store projection/search reads; use `dependency.operation` to group controlled query names.
- Repeated failed spans with `error=true` need the paired logs for the sanitized error message.

For CLI/TUI bottlenecks:

- Long `cli.dependencies_init` points to config, project resolution, or client setup.
- Long `cli.daemonclient.command` or `cli.transport.command` points to daemon transport or server-side work.
- Long `cli.command_execute` with short daemon attempts points to client-side rendering, parsing, or post-processing.
- Long `dependency.git`, `dependency.tmux`, `dependency.process`, `dependency.clipboard`, `dependency.editor`, `dependency.file_viewer`, or `dependency.http` points to local process, helper, viewer, or remote API boundaries.

## Guardrails

Do not add high-cardinality span names. Put dynamic values in bounded attributes such as `request_id`, `project_id`, and `command_shape`.

Do not record raw prompt bodies, issue descriptions, tokens, cookies, Authorization headers, full argv values, or SQL with values.

Before calling a regression fixed, run the same command shape at least twice and compare the same span names. The first run may include cache and daemon warmup cost.

Keep query windows and result limits small even with a container memory limit.
Jaeger's result limit counts traces, not spans inside each trace, so a single
very large trace can still be expensive. Export evidence by trace ID before
widening a search, and widen either the lookback or result count, never both at
once.

## Troubleshooting

If no spans appear, check:

- `AZEDARACH_OTEL=true` or `diagnostics.latencyTrace=true`.
- Jaeger is listening for OTLP HTTP on `http://localhost:4318/v1/traces`.
- The process was restarted after changing persisted config.
- Custom `OTEL_EXPORTER_OTLP_ENDPOINT` or `OTEL_EXPORTER_OTLP_TRACES_ENDPOINT` values point to the intended collector.

If the CLI exits before spans appear, verify process shutdown is flushing traces and rerun with the local `go run ./cmd/az ...` path.
