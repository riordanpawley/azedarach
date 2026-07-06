# dec-1: Local learnings complement decisions

- Created: 2026-07-01
- Updated: 2026-07-01

## Rationale

Use az decision for durable why/choice records and add az learn for evidence-backed observations, heuristics, and session discoveries that may later promote into decisions, spec requirements, or curated guidance. This avoids duplicating the existing decision model while giving agents a lower-friction capture and review loop.

## Context

Issue csk overlaps older issue nf's az reason idea. The current repo already has az decision commands/storage, but no decision records. The learning loop should reuse decisions as one promotion target rather than introducing a separate reason command.

## Consequences

csk implementation should expose learning statuses and recall, keep evidence out of prime by default, and make promotion-to-decision guidance explicit. nf can remain related historical product context rather than a separate active prerequisite.

## Links

- applies-to issue:csk
- applies-to issue:nf
- applies-to requirement:csk-req-learning-loop
