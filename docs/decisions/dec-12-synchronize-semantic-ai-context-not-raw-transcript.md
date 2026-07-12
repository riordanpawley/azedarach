# dec-12: Synchronize semantic AI context, not raw transcripts

- Created: 2026-07-12
- Updated: 2026-07-12

## Rationale

Synchronize a compact semantic history: issue intent/current context, meaningful commands and outcomes, decisions and rationale, affected files/symbols, tests/failures/validation, human questions and responses, curated learnings, lifecycle continuation checkpoints, and references to deliberately published artifacts. Produce automatic checkpoints at pause, handoff, review, and completion, with periodic checkpoints for long sessions; keep explicit agent-authored learnings distinct. Keep raw prompts/responses/tool streams/transcripts local and ephemeral unless a user deliberately publishes an artifact.

## Context

Raw transcripts are extremely heavy, noisy, sensitive, provider-specific, and poorly suited to synchronization or durable agent continuation. Current Azedarach already centers structured issue observations, worker evidence, interactions, decisions, specs, and promoted learnings rather than complete conversations.

## Consequences

Meaningful daemon-authoritative domain changes form an append-only shared history with projections for hot reads. Do not permanently event-source heartbeats, terminal bytes, high-volume progress/activity telemetry, or raw transcripts. Initially every developer starts agents on their own personal machine; the project service synchronizes context, leases, presence, and coordination but owns no clone, worktree, tmux session, credentials, or process. Cross-machine orchestration, project-shared runners, remote attach, and remote execution are deferred beyond the first capability. Secrets remain local and private evidence is excluded from sync by default.

## Links

- applies-to issue:dda
