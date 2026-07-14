# dec-16: Limit collaboration v1 to shared semantic project context

- Created: 2026-07-14
- Updated: 2026-07-14

## Rationale

The first team audience needs one shared source of truth for tickets, specs, decisions, learnings, and closely related semantic context while each developer continues running agents, Git, worktrees, tmux, and orchestration locally. Adding network runner control, leases, presence, fenced attempts, server-side raw Git execution, or distributed orchestration would multiply security and failure modes without being required to deliver shared context.

## Context

DDA initially explored a broader collaboration control plane. Human scope correction narrows the first release to collaborative knowledge and work tracking for one small developer team and one repository.

## Consequences

Collaboration v1 synchronizes and authorizes shared semantic records only. It does not enroll runners, observe or control sessions, coordinate execution leases, start agents remotely, operate tmux/worktrees, or orchestrate workers across machines. Developers continue using local Git credentials and local clones for clone, fetch, and push. The project service does not maintain a checkout or execute raw Git commands such as clone, fetch, push, merge, or rebase. Decision dec-17 separately includes GitHub App provider APIs for repository linking and the guarded PR lifecycle without expanding this execution boundary. Network orchestration and shared runners are later possibilities, not assumed roadmap commitments.

## Links

- applies-to issue:dda
