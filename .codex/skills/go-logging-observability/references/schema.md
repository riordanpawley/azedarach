# Go Logging and Observability Schema

Use this schema for stable, queryable fields.

## Required Base Fields

- `event`: Stable event name (`http.request.completed`)
- `service`: Service name
- `env`: Deployment environment (`dev`, `staging`, `prod`)
- `timestamp`: Emission time (RFC3339 or backend-native timestamp)
- `level`: Log level

## Correlation Fields

- `request_id`: Request correlation key from ingress
- `trace_id`: OpenTelemetry trace id
- `span_id`: OpenTelemetry span id
- `operation`: Logical handler/use-case name

## HTTP Request Summary Fields

- `http.method`
- `http.route`
- `http.status_code`
- `duration_ms`
- `outcome`: `success` or `error`

## Error Fields

- `error_class`: Stable class/category (`timeout`, `validation`, `dependency`)
- `error_code`: Optional stable machine code
- `error_retryable`: Boolean
- `error_boundary`: `client`, `dependency`, or `internal`
- `error_message`: Sanitized, non-secret message only

## Dependency Call Fields

- `dependency.name`
- `dependency.operation`
- `dependency.duration_ms`
- `dependency.outcome`

## Naming Rules

- Use lowercase snake_case for custom fields.
- Prefer dotted namespaces for protocol groups (`http.status_code`).
- Keep names stable across repos once introduced.

## Redaction Rules

- Never log secrets or credentials.
- Never log full request/response bodies by default.
- Never log raw authorization headers or cookies.
- Never log card numbers, API keys, access tokens, or session identifiers.
- Apply explicit redaction for user-generated text that may contain secrets.
