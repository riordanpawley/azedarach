# Ticket terminology migration

`ticket` is Azedarach's canonical term for a tracked work item. This rollout is
compatibility-first: user-facing surfaces and new Go APIs use ticket language,
while established persistence and integration contracts remain readable.

## Canonical interfaces

- Use `az ticket ...` for work-item commands.
- Use `AZEDARACH_TICKET_ID` for the active ticket scope.
- Use `naming.TicketID`, `domain.Ticket`, and `domain.TicketOwnership` in new Go code.
- CLI help, the session primer, and TUI copy refer to tickets.

## Compatibility interfaces

The following names remain supported during the deprecation window:

- `az issue ...` dispatches to the same implementation as `az ticket ...`.
- `AZEDARACH_ISSUE_ID` and `AZEDARACH_TICKET_ID` are mirrored at CLI startup;
  the legacy value wins if both are present, preventing a nested process from
  silently changing an existing active scope.
- JSON and MessagePack fields named `issue_id`, existing daemon command names,
  telemetry field keys, and SQLite `issues`/`issue_*` objects are unchanged.
- Existing ticket identifiers, branches, worktrees, databases, scripts, and
  external-provider mappings require no migration.

Keeping storage and wire names stable is intentional. Renaming them safely
requires a separately versioned protocol and database migration with dual-read
and dual-write behavior; a textual schema rewrite would break older binaries
and automation.

## Contributor guidance

Prefer ticket terminology in new identifiers and visible copy. When touching a
legacy API, preserve its serialized representation unless the change includes a
versioned migration. Compatibility tests must prove that canonical and legacy
CLI/environment entry points select the same behavior.
