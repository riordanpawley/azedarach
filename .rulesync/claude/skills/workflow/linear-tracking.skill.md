# Issue Tracking Skill (`linear-cli` backend)

**Version:** 2.0
**Purpose:** Reliable `linear-cli` workflow for issue discovery, updates, and handoff context.

## Core Rules

- Use `linear-cli` for issue tracking.
- Configure default team once in `.azedarach.json` under `issueTracker.linear.team` (or run `linear-cli setup`).
- Keep issue status and notes current during work.
- Use git pull/push for code sync.

## Command Reference

```bash
# List issues (JSON for automation)
linear-cli i list --output json --compact --all

# Show one issue
linear-cli i get <issue-id> --output json --compact

# Create issue
linear-cli i create "Issue title" --output json --compact
# Optional override when needed: add `-t <TEAM>`

# Start / stop / close
linear-cli i start <issue-id>
linear-cli i stop <issue-id>
linear-cli i close <issue-id>

# Update fields
linear-cli i update <issue-id> --output json --compact ...
```

## Status Workflow

- Start work: `linear-cli i start <id>`
- Blocked/open transition: `linear-cli i stop <id>` then update state/details as needed
- Complete work: `linear-cli i close <id>`

## Dependency Workflow (Parent/Child)

Set parent relationship using `i update --data`:

```bash
linear-cli i update <child-id> \
  --data '{"parentId":"<parent-linear-id>"}' \
  --output json --compact
```

## Session Start Checklist

- List issues: `linear-cli i list --output json --compact --all`
- Open the target issue: `linear-cli i get <id> --output json --compact`
- Start it if needed: `linear-cli i start <id>`

## Session End Checklist

- Update issue notes/status with current state
- Close completed issues
- Commit code changes
- `git pull --rebase`
- `git push`

## Handoff Notes Template

Use structured notes so another session can resume quickly:

```text
COMPLETED:
- ...

IN PROGRESS:
- ...

NEXT:
- ...

BLOCKERS:
- ...
```

## Anti-Patterns

- Do not leave long-running work without status updates.
- Do not rely on local TODO lists as the source of truth.
