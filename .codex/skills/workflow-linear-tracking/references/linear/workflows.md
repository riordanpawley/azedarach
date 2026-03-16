# Issue Tracking Workflow Patterns

## Daily Flow

```bash
# Discover work
linear-cli i list --output json --compact --all

# Inspect target
linear-cli i get <issue-id> --output json --compact

# Start
linear-cli i start <issue-id>

# Update during work
linear-cli i update <issue-id> --output json --compact ...

# Complete
linear-cli i close <issue-id>
```

Set `issueTracker.linear.team` in `.azedarach.json` (or run `linear-cli setup`) to make `i create` use your default team.

## Epic + Child Pattern

```bash
# Create epic
linear-cli i create "Epic title" --output json --compact

# Create child
linear-cli i create "Child task" --output json --compact

# Link child -> epic parent
linear-cli i update <child-id> \
  --data '{"parentId":"<epic-linear-id>"}' \
  --output json --compact
```

## Session Handoff Pattern

Before ending a session:

1. Update issue status and key notes.
2. Commit code changes.
3. Keep git flow local unless remote sync is explicitly requested.

Suggested note format:

```text
COMPLETED:
IN PROGRESS:
NEXT:
BLOCKERS:
```
