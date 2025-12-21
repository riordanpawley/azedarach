---
name: beads-tracking
description: CLI-based patterns for multi-session task persistence with beads issue tracker
---

<!-- This skill references the Claude Code skill for full content -->
<!-- Source: .claude/skills/workflow/beads-tracking.skill.md -->

# Beads Issue Tracking

**bd** is a git-backed issue tracker for multi-session task persistence.

## Quick Reference

| Operation | Command |
|-----------|---------|
| **Search** | `bd search "pattern"` |
| Ready work | `bd ready` |
| Show issue | `bd show <id>` |
| Create | `bd create --title="..." --type=task` |
| Update | `bd update <id> --status=in_progress` |
| Close | `bd close <id>` |

## Full Documentation

See `.claude/skills/workflow/beads-tracking.skill.md` for complete patterns including:
- Session workflow (start → work → complete)
- Dependency management
- Worktree sync patterns
- Issue lifecycle best practices

## Critical Rules

1. **NEVER use `bd list`** - causes context bloat
2. **Use `bd search`** - primary discovery tool
3. **Track ALL work** - preserves context across compaction
4. **In worktrees**: Run `bd sync` manually at session end
