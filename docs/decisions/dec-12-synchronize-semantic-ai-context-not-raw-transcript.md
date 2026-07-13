# dec-12: Synchronize semantic AI context, not raw transcripts

- Created: 2026-07-12
- Updated: 2026-07-13

## Rationale

Synchronize compact semantic context: issue intent/current state, meaningful commands and outcomes, decisions and rationale, affected files/symbols, tests/failures/validation, human questions and responses, curated learnings, event-driven continuation checkpoints, and references to deliberately published artifacts. Checkpoints occur only at lifecycle or material semantic changes and are deduplicated; there is no periodic timer. Raw prompt/response/tool streams and transcripts remain local unless deliberately published.

## Context

Raw transcripts are heavy, noisy, sensitive, provider-specific, and poor shared continuation material. Azedarach already has structured issues, observations, evidence, interactions, decisions, specs, and promoted learnings that can form compact agent context.

## Consequences

Accepted semantic records remain permanent for the project lifetime and corrections use superseding history; redaction mechanics are deferred beyond dda/dgv. Do not permanently share terminal bytes, heartbeats, activity samples, noisy telemetry, secrets, private evidence, or absolute local paths. Attachment metadata may be shared permanently while content-addressed bytes download on demand. The service synchronizes context, leases, presence, and coordination but owns no clone, worktree, tmux session, credentials, or process. Cross-machine orchestration, shared runners, remote attach, and remote execution remain deferred.

## Links

- applies-to issue:dda
- applies-to issue:dgv — Defines permanent compact semantic payloads, event-driven checkpoints, and excluded transcript/telemetry classes.
