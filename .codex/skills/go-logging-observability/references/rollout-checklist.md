# Rollout Checklist: Go Logging and Observability

Use this checklist when introducing or migrating logging patterns.

## Pre-Implementation

- Inventory existing logger entry points and middleware.
- Confirm target sink constraints (field limits, payload size, cost model).
- Define required event names and required fields per event.
- Agree on redaction policy and data classification.

## Implementation

- Add or centralize shared `slog.Logger` construction.
- Add request correlation extraction and propagation.
- Add OTel span propagation and trace/span field mapping.
- Add request summary event emission for success and failure paths.
- Add dependency timing/error events for external boundaries.
- Add sampling or rate limiting for high-volume event types.

## Validation

- Unit test log field presence for key events.
- Unit test redaction behavior for sensitive fields.
- Validate no cardinality explosion from dynamic field keys.
- Validate that error paths still emit summary events.

## Operational Readiness

- Create saved queries/dashboards for top failure classes.
- Create alerts on error rate and latency SLO breaches.
- Confirm on-call runbook references event names and fields.
- Document migration notes and deprecated log keys.
