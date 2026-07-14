# dec-17: Keep raw Git local and use the GitHub App for shared delivery

- Created: 2026-07-14
- Updated: 2026-07-14

## Rationale

Developers already have the repository checkout and Git credentials needed for clone, fetch, commit, push, merge conflict resolution, and branch repair. The shared service can coordinate delivery through GitHub provider APIs without becoming a runner, maintaining a checkout, executing hooks, or holding raw Git credentials.

## Context

Collaboration v1 is limited to shared semantic project context, but the team still benefits from repository linkage and a shared PR lifecycle. Human accepted the distinction between raw Git execution and provider API integration.

## Consequences

Local clients own raw Git commands and push exact commits to the configured remote. Shared tickets may reference branches, commits, and PRs. The published GitHub App may link repositories, create and update the root PR, observe reviews and checks, reconcile webhooks with polling, verify the expected head SHA, and perform guarded merge. The project service keeps no checkout and does not run clone, fetch, push, merge, rebase, hooks, or worktree operations.

## Links

- applies-to issue:dda
