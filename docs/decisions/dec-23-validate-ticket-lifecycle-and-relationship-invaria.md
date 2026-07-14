# dec-23: Validate ticket lifecycle and relationship invariants online

- Created: 2026-07-14
- Updated: 2026-07-14

## Rationale

Require current project authority for lifecycle and graph commands. Keep containment and blocking relations distinct, make parent completion wait for live children, reject derived wait-for deadlocks, and never silently reopen a closed parent when attaching a child.

## Context

Shared ticket relationships affect readiness, completion, review, and integration routing and therefore need one current ordered decision boundary.

## Consequences

Reopen-and-attach is an explicit user intent with visible retry/intervention if its multi-step realization is incomplete. Reparenting is one user-visible operation, and making a child a root warns when integration routing changes.

## Links

- applies-to issue:dda
