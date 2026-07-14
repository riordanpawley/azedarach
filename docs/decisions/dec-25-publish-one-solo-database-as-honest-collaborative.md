# dec-25: Publish one solo database as honest collaborative genesis

- Created: 2026-07-14
- Updated: 2026-07-14

## Rationale

Promote exactly one owner-selected solo project database into a new collaborative authority after previewing imported and excluded classes, binding the local actor to the initial owner, and recording provenance. Other machines join as fresh clients.

## Context

Automatically merging independent solo databases would create ambiguous identities, ordering, and provenance.

## Consequences

Do not invent pre-collaboration event history or silently merge another local store. Import current semantic state and available provenance, surface the collaboration cutover, and preserve excluded local-only material locally.

## Links

- applies-to issue:dda
