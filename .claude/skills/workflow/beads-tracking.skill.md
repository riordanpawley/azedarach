# Beads Issue Tracking Skill

**Version:** 1.0
**Purpose:** CLI-based patterns and quality guidelines for multi-session task persistence with beads

## Overview

**bd** is a git-backed graph issue tracker designed to survive conversation compaction by persisting task context, dependencies, and notes across sessions.

### Core Philosophy

- **Multi-session memory**: bd preserves context when conversation history is deleted
- **Dependency tracking**: Automatic ready detection via dependency graph
- **Strategic tracking**: bd captures *why* and *what*, TodoWrite handles *how*
- **Proactive usage**: Create issues during work, not after compaction

## CLI Commands Reference

| Operation | Command | Notes |
|-----------|---------|-------|
| **Search issues** | **`bd search "pattern"`** | **PRIMARY discovery tool** |
| Check ready | `bd ready` | Find unblocked work |
| Show issue | `bd show <id>` | Get full details for ONE issue |
| Create issue | `bd create --title="..." --type=task` | |
| Update issue | `bd update <id> --status=in_progress` | |
| Add notes | `bd update <id> --notes="..."` | |
| Close issue | `bd close <id>` | |
| Close with reason | `bd close <id> --reason="..."` | |
| Add dependency | `bd dep add <issue> <depends-on>` | |
| Get stats | `bd stats` | |
| Find blocked | `bd blocked` | |
| Sync changes | `bd sync` | **REQUIRED in worktrees** |

## CRITICAL: Use `search`, NEVER `list`

**`list` returns FULL issue details (15k+ tokens) and causes massive context bloat. ALWAYS use `search` instead.**

| Need | Use | Why |
|------|-----|-----|
| Find issues about "auth" | **`bd search "auth"`** | Targeted, efficient |
| Find by partial ID | **`bd search "AZ-5"`** | Direct lookup |
| Show ALL open issues | **`bd search "" --status=open`** | Efficient even for broad queries |
| What can I work on? | **`bd ready`** | Shows unblocked tasks only |

## Status Values Reference

| Status | Meaning | How to Set |
|--------|---------|------------|
| `open` | Not started, ready to work | Default on creation |
| `in_progress` | Currently being worked on | `bd update <id> --status=in_progress` |
| `blocked` | Waiting on dependencies | `bd update <id> --status=blocked` |
| `closed` | Work completed or abandoned | `bd close <id>` |

## Git Worktree & Branch Support

### Branches WITH Upstream (Pushed)

Normal workflow - `bd sync` works bidirectionally:
```bash
bd sync   # Commits beads, pushes to upstream, pulls from remote
```

### Branches WITHOUT Upstream (Ephemeral)

**CRITICAL**: `bd sync --from-main` OVERWRITES local beads changes!

**Best practice**: Push the branch to create an upstream:
```bash
git push -u origin branch-name   # Now bd sync works normally
```

**If you can't push**: Don't use `--from-main` at session end. Instead:
```bash
git add -A        # Includes .beads/ changes
git commit
# Merge to main later - beads changes propagate via git merge
```

### Worktrees

- **BEADS_NO_DAEMON=1** should be set via `.envrc`
- Push the worktree branch at creation time for best results
- Run `bd sync` manually at session end

## Critical Patterns

### 1. Compaction Survival

**Problem**: Conversation history deleted, only bd persists.

**Solution**: Write notes as if explaining to future agent with ZERO context.

**Notes Structure**:
```
COMPLETED: [Specific deliverables done]
IN PROGRESS: [Current state + exact next step]
BLOCKERS: [What prevents progress]
KEY DECISIONS: [Important context/rationale]
```

### 2. Session Handoff

**At session start:**
1. Check `bd ready` for unblocked work
2. Search for in-progress: `bd search "" --status=in_progress`
3. Show issue details: `bd show <id>`
4. Report context to user

**At session end:**
1. Recognize logical stopping point
2. Update notes with current state + next steps
3. **In worktrees:** Run `bd sync` manually
4. Commit code changes

### 3. Understanding Dependency Direction

**Syntax**: `bd dep add <issue> <depends-on>`

**Meaning**: "Issue depends on depends-on" = "issue is blocked by depends-on"

**Epic Pattern (Parent Blocked by Children)**:
```bash
# Epic depends on children (epic blocked until children done)
bd dep add AZ-100 AZ-101   # Epic blocked by child 1
bd dep add AZ-100 AZ-102   # Epic blocked by child 2

# Result: bd ready shows children (ready), NOT epic (blocked)
```

## Workflow Patterns

### Discovery & Side Quests

```bash
# FIRST: Check if issue already exists
bd search "validation logic refactor"

# Notice new work while implementing feature
bd create --title="Refactor duplicated validation logic" --type=chore --priority=2

# Link to current work
bd dep add AZ-123 AZ-125 --type=discovered-from
```

### Epic Planning

```bash
# Create epic
bd create --title="User authentication system" --type=epic --priority=1

# Create subtasks
bd create --title="Implement JWT token generation" --type=task --priority=1
bd create --title="Add login endpoint" --type=task --priority=1

# Epic depends on children
bd dep add AZ-100 AZ-101
bd dep add AZ-100 AZ-102

# Work through in dependency order
bd ready  # Shows children (not blocked)
```

## Issue Quality Standards

### Design vs Acceptance (CRITICAL)

**Design field**: Implementation approach, architecture, trade-offs (CAN EVOLVE)

**Acceptance field**: What success looks like, definition of done (STABLE)

**Common mistake**: Putting implementation details in acceptance criteria.

Examples:
- Design: "Use two-phase batchUpdate approach"
- Acceptance: "Formatting applied atomically (all or nothing)"
- Acceptance: "Must use batchUpdate API" (too prescriptive)

## Context Quality Guidelines

**Purpose**: Notes are resumption guides, not development diaries. Optimize for "What should I do next?" not "What did I do?"

### Language Style: Precise vs Fluffy

**Use technical precision**:
- "67 type errors resolved across src/components"
- "Refactored AuthService to use unified token IDs"
- "MAJOR BREAKTHROUGHS! Massive cascade of fixes!" (avoid)

## Common Mistakes

- **Using `list` instead of `search`** - `list` returns 15k+ tokens
- **Passing `--notes` to create** - `--notes` only works with `update`
- **Using invalid status values** - Valid: `open`, `in_progress`, `blocked`, `closed`
- **Reversing dependency direction** - `bd dep add A B` means "A depends on B"
- **Not syncing manually in worktrees** - Auto-sync is disabled
- **Vague notes** - "Working on feature" vs "Completed auth token generation, next: add expiry logic"

## Summary

**Track ALL work in beads** - Preserves context, tracks dependencies, enables resumption.

Key practices:
- **ALWAYS use `search` for finding issues - NEVER use `list`**
- Create issues proactively for all work
- Update notes continuously (clean as you go)
- **In worktrees: Run `bd sync` manually** at session end
- Write notes for resumability (future agent has zero context)
