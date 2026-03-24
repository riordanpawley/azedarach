---
name: go-logging-observability
description: Build and review production-grade Go logging and observability with `log/slog` and OpenTelemetry. Use when adding or refactoring logs, instrumenting request paths, defining event schemas, reducing log cost/cardinality, enforcing redaction/PII policy, correlating logs with traces, or reviewing Go services for observability gaps.
---

# Go Logging Observability

Implement logging that is queryable, correlated, and safe by default.
Prefer structured `slog` events plus traces over free-form messages.

## Workflow

1. Inspect current logger + telemetry setup.
2. Define event schema before writing new events.
3. Add correlation fields (`trace_id`, `span_id`, `request_id`) everywhere.
4. Enforce redaction and field allowlists before adding business context.
5. Add request summary events and targeted step/error events.
6. Validate cardinality, payload size, and sampling policy.
7. Add or update tests that assert log fields and redaction behavior.

## Core Rules

### Use structured logs with `log/slog`

- Instantiate one shared base logger at process startup.
- Use `slog.Attr` keys from a stable schema.
- Prefer typed values (`Int`, `Bool`, `Duration`, `Time`) over string formatting.
- Do not log raw JSON strings.

### Correlate logs and traces

- Include `trace_id` and `span_id` on every request-scoped event.
- Include stable request correlation (`request_id`, `operation`, `service`, `env`).
- Start spans at service boundaries and propagate context to downstream calls.

### Enforce redaction and secrets policy

- Default to denylist + allowlist:
- Never log credentials, tokens, headers like `authorization`, cookies, full payload bodies, or raw SQL with values.
- Hash or truncate user identifiers where exact identity is not required.
- Log error classes/codes; sanitize error messages before emission.

### Control cardinality and cost

- Keep high-cardinality fields limited to required correlation keys.
- Keep request summary events bounded in width.
- Use sampling/rate limiting for high-volume info events.
- Do not emit large nested objects; prefer compact, named subfields.

### Use event taxonomy, not ad-hoc text

- Emit at least one request summary event per completed request.
- Emit step events only for meaningful state transitions or latency bottlenecks.
- Emit error events with category + retryability + boundary (client/dependency/internal).
- Keep event names stable (`http.request.completed`, `db.query.failed`).

### Preserve useful levels

- Use `DEBUG` for local/targeted diagnostics.
- Use `INFO` for request summaries and business milestones.
- Use `WARN` for degraded but recoverable conditions.
- Use `ERROR` for failed operations requiring attention.

## Go Implementation Pattern

```go
func LogRequestComplete(ctx context.Context, logger *slog.Logger, reqID string, status int, dur time.Duration, err error) {
    attrs := []slog.Attr{
        slog.String("event", "http.request.completed"),
        slog.String("request_id", reqID),
        slog.Int("status_code", status),
        slog.Duration("duration_ms", dur),
    }

    if span := trace.SpanContextFromContext(ctx); span.IsValid() {
        attrs = append(attrs,
            slog.String("trace_id", span.TraceID().String()),
            slog.String("span_id", span.SpanID().String()),
        )
    }

    if err != nil {
        logger.ErrorContext(ctx, "request failed",
            append(attrs, slog.String("error_class", classifyError(err)))...,
        )
        return
    }

    logger.InfoContext(ctx, "request completed", attrs...)
}
```

## Review Checklist

- Verify new log fields match [schema.md](./references/schema.md).
- Verify no PII/secrets exposure and redaction test coverage exists.
- Verify correlation fields exist on all request and dependency events.
- Verify cardinality budget and sampling decisions are documented.
- Verify event names and levels are consistent across services.

## References

- Schema and naming rules: `references/schema.md`
- Rollout and migration checklist: `references/rollout-checklist.md`
