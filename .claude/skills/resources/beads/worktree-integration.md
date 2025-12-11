# Beads + Git Worktrees Integration Guide

## Overview

This guide explains how to use beads effectively with git worktrees for parallel development with multiple Claude Code sessions.

## The Challenge

**Git worktrees** enable parallel development:
- Main worktree: `/path/to/project`
- Feature worktrees: `../project-feature-name`, etc.
- Each worktree has independent working directory
- All share same `.git` directory

**Beads daemon problem:**
- Daemon discovers workspace root by finding `.git`
- All worktrees point to same `.git`
- Daemon treats all worktrees as one workspace
- Conflicts when multiple worktrees try to use same daemon socket

**Solution:** Use daemon mode in main worktree, no-daemon mode in others.

## Architecture

### Files Shared Across Worktrees (via git)

**`.beads/issues.jsonl`** - Source of truth
- All issues, dependencies, metadata
- Committed to git
- Synced across worktrees via git pull/push
- JSONL format (line-based, merge-friendly)

**`.beads/config.yaml`** - Configuration
- Project settings
- Issue prefix
- Committed to git

### Files Local Per-Worktree (gitignored)

**`.beads/beads.db`** - SQLite cache
- Fast local queries
- Auto-imports from `issues.jsonl`
- Recreated per-worktree as needed
- Gitignored (local-only)

**Daemon files** (main worktree only):
- `.beads/bd.sock` - Unix socket
- `.beads/daemon.lock` - Lock file
- `.beads/daemon.log` - Logs
- All gitignored

## Setup

### Main Worktree

```bash
cd /path/to/project

# Initialize beads (if not already)
bd init --prefix AZ

# Daemon runs automatically in main worktree
bd ready  # Works with daemon
```

### Feature Worktrees

Create `.envrc` in worktree or set environment:

```bash
export BEADS_NO_DAEMON=1
```

With direnv, this can be automatic:
```bash
# .envrc
if [ -f .git ]; then
  export BEADS_NO_DAEMON=1
  echo "Git worktree detected - enabled BEADS_NO_DAEMON=1"
fi
```

## Daily Workflow

### Creating Issues in Main Worktree

```bash
cd /path/to/project

# Create epic
bd create --title="Implement TUI board" --type=epic --priority=1

# Create child tasks
bd create --title="Create Board component" --type=task --priority=1

# Establish relationships
bd dep add AZ-100 AZ-101

# Commit to share with other worktrees
git add .beads/issues.jsonl .beads/metadata.json
git commit -m "beads: add TUI board epic"
git push
```

### Working in Feature Worktree

```bash
cd ../project-feature

# Pull latest issues from main worktree
git pull

# Check ready work (BEADS_NO_DAEMON=1 should be set)
bd ready

# Start work
bd update AZ-101 --status=in_progress

# Update notes as you work
bd update AZ-101 --notes="COMPLETED: Board layout. IN PROGRESS: Column component."

# Complete work
bd close AZ-101 --reason="Board component complete with columns"

# Sync and commit
bd sync
git add .beads/issues.jsonl
git commit -m "beads: complete board component"
git push
```

### Parallel Claude Sessions

**Session 1 - Main worktree (TUI components):**
```bash
cd /path/to/project
claude
> "Work on TUI board"
```

**Session 2 - Feature worktree (session management):**
```bash
cd ../project-sessions
claude
> "Implement session manager"
```

**No conflicts!** Each session:
- Sees same issues (shared)
- Uses separate SQLite cache
- Creates issues independently
- Syncs via git commits

## Syncing Between Worktrees

### Push from Main Worktree

```bash
cd /path/to/project
bd create --title="New task" --type=task
git add .beads/issues.jsonl .beads/metadata.json
git commit -m "beads: add new task"
git push
```

### Pull in Feature Worktree

```bash
cd ../project-feature
git pull
bd ready  # Sees new task automatically
```

### Auto-Import Behavior

Beads automatically imports from `issues.jsonl` when:
1. File is newer than database
2. On first `bd` command after git pull
3. Every 5 seconds (in daemon mode)
4. On startup (no-daemon mode)

## Troubleshooting

### Problem: "no beads database found" in new worktree

**Cause:** New git worktrees have committed beads files but NOT the local SQLite cache.

**Fix:**
```bash
bd init
```

This creates `.beads/beads.db` and auto-imports existing issues.

### Problem: "readonly database" error in worktree

**Cause:** BEADS_NO_DAEMON not set

**Fix:**
```bash
export BEADS_NO_DAEMON=1
bd ready  # Should work now
```

### Problem: Issues not syncing between worktrees

**Cause:** Forgot to commit/push `issues.jsonl`

**Fix:**
```bash
git status .beads/
git add .beads/issues.jsonl .beads/metadata.json
git commit -m "beads: sync issues"
git push

# Pull in other worktree
cd /path/to/other/worktree
git pull
```

## Best Practices

### Commit Frequency

**Commit `issues.jsonl` when:**
- Creating epics or major issues
- Completing significant work
- Before switching worktrees
- End of session (with other code changes)

### Issue Organization

**Main worktree:** Strategic planning
- Epics for large features
- High-level architecture issues
- Cross-cutting concerns

**Feature worktrees:** Tactical execution
- Implementation tasks
- Specific bugs
- Discovered work during feature development

### Git Workflow

```bash
# Standard flow
bd create --title="Task" --type=task
# ... work on code ...
bd close AZ-123 --reason="Completed"

# Commit everything together
git add .beads/issues.jsonl src/
git commit -m "feat: implement feature

- Added feature implementation
- beads: completed AZ-123"
git push
```

## Summary Checklist

**Daily workflow:**
- [ ] Check `bd ready` at session start
- [ ] Update issues as work progresses
- [ ] Commit `issues.jsonl` with code changes
- [ ] Pull in other worktrees to see updates

**Verification:**
- [ ] Main worktree: `bd ready` works (daemon mode)
- [ ] Feature worktrees: `echo $BEADS_NO_DAEMON` returns 1
- [ ] Feature worktrees: `bd ready` works (no-daemon mode)
- [ ] Issues visible in all worktrees after git pull
