# Issue Tracking + Worktree Integration

## Principle

Issue state is remote-backed via `linear-cli`. Worktrees isolate code; updates remain shared through the issue tracker.

## Recommended Flow per Worktree

```bash
# 1) Open/update issue context
linear-cli i get <issue-id> --output json --compact
linear-cli i start <issue-id>

# 2) Do code work in this worktree
# ... edit, test, commit ...

# 3) Keep issue up to date
linear-cli i update <issue-id> --output json --compact ...

# 4) Land branch updates
# Keep sync local by default; only run remote sync if explicitly requested.

# 5) Close issue when complete
linear-cli i close <issue-id>
```

## Parallel Sessions

For parallel sessions, each worktree can independently:

- fetch issue details (`i get`)
- update status/notes (`i update`, `i start`, `i stop`, `i close`)

No local issue-db sync workflow is needed.

## Common Failure Modes

- `linear-cli` auth expired: re-authenticate and retry.
- Wrong issue ID: run `linear-cli i list --output json --compact --all` and re-check.
- Remote sync rejected: resolve locally and retry remote sync only if explicitly requested.
