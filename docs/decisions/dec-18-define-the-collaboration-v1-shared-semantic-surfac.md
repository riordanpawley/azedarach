# dec-18: Define the collaboration v1 shared semantic surface

- Created: 2026-07-14
- Updated: 2026-07-14

## Rationale

Synchronize the project records that let a small developer team and its locally run agents share intent, policy, rationale, and outcomes: tickets and relationships, lifecycle, comments, specs and requirements, decisions, learnings, human interactions, compact evidence, and commit/PR references.

## Context

Collaboration v1 is a shared semantic knowledge and work-tracking product, not a distributed execution plane.

## Consequences

Exclude sessions, runtime activity, orchestration state, terminal bytes, worktrees, local Git status, secrets, and noisy telemetry. Local agents may read and update shared semantic records through their local Az client without the project service observing or controlling execution. Accepted commands distinguish human versus local-agent origin and may carry a stable agent invocation identifier as provenance under the accountable member and device; that identifier grants no membership, presence, lease, or execution authority.

## Links

- applies-to issue:dda
