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

2. Start Jaeger with the collector endpoint exposed:

```bash
docker run --rm --name azedarach-jaeger \
  -p 16686:16686 -p 4318:4318 \
  jaegertracing/all-in-one:latest
```

3. Reproduce the slow path with low-noise commands:

```bash
AZEDARACH_OTEL=true go run ./cmd/az issue get cxk
AZEDARACH_OTEL=true go run ./cmd/az prime
```

4. Open `http://localhost:16686`, select `az` or `azd`, then filter by operation/span names such as `cli.command_execute`, `cli.daemonclient.command_attempt`, `daemon.command.begin`, and `daemon.command.dispatcher_handle`.

## Investigation Workflow

Start with service separation:

- `az`: CLI and TUI process startup, command execution, daemon client attempts.
- `azd`: daemon command receipt, begin-command contention, dispatcher handling.

Check whether the CLI span time is mostly before, during, or after `daemonclient.command_attempt`. If the command attempt dominates, switch to `azd` traces using the shared `request_id`.

Use `command_shape` instead of raw argv when grouping slow commands. Treat missing `request_id` as evidence that the path did not cross the daemon RPC boundary.

For daemon bottlenecks:

- Long `daemon.command.begin` usually points to command concurrency/backpressure.
- Long `daemon.command.dispatcher_handle` points to domain/store/runtime work behind the typed daemon command.
- Repeated failed spans with `error=true` need the paired logs for the sanitized error message.

For CLI/TUI bottlenecks:

- Long `cli.dependencies_init` points to config, project resolution, or client setup.
- Long `cli.daemonclient.command_attempt` points to daemon transport or server-side work.
- Long `cli.command_execute` with short daemon attempts points to client-side rendering, parsing, or post-processing.

## Guardrails

Do not add high-cardinality span names. Put dynamic values in bounded attributes such as `request_id`, `project_id`, and `command_shape`.

Do not record raw prompt bodies, issue descriptions, tokens, cookies, Authorization headers, full argv values, or SQL with values.

Before calling a regression fixed, run the same command shape at least twice and compare the same span names. The first run may include cache and daemon warmup cost.

## Troubleshooting

If no spans appear, check:

- `AZEDARACH_OTEL=true` or `diagnostics.latencyTrace=true`.
- Jaeger is listening for OTLP HTTP on `http://localhost:4318/v1/traces`.
- The process was restarted after changing persisted config.
- Custom `OTEL_EXPORTER_OTLP_ENDPOINT` or `OTEL_EXPORTER_OTLP_TRACES_ENDPOINT` values point to the intended collector.

If the CLI exits before spans appear, verify process shutdown is flushing traces and rerun with the local `go run ./cmd/az ...` path.
