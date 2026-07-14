# dec-26: Ship a recoverable single-service SQLite deployment

- Created: 2026-07-14
- Updated: 2026-07-14

## Rationale

Support Docker Compose around one Go project service, one active SQLite WAL authority, durable database and artifact volumes, and an optional Caddy TLS profile. Require HTTPS outside loopback and make backup, restore, upgrade, health, and maintenance explicit product paths.

## Context

Self-host first is only credible for a small team if ordinary operators can secure, back up, restore, and upgrade it without introducing a second writer.

## Consequences

Provide automated off-machine backups, pre-upgrade snapshots, verified whole-project restore, visible maintenance and degraded states, and health for disk/WAL pressure, backup age, sync lag, and provider/auth failures. Reconsider PostgreSQL only for multi-instance HA, consolidated multi-project hosting, or measured sustained writer contention.

## Links

- applies-to issue:dda
