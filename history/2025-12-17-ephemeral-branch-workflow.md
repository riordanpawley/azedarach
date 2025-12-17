# Ephemeral Branch Workflow Decision

**Date**: 2025-12-17
**Decision**: Option A - Align with Beads Design
**Status**: Accepted

## Context

We discovered that the `bd prime` session close protocol for ephemeral branches:
```
git status → git add → bd sync --from-main → git commit
```

...puts `bd sync --from-main` AFTER staging but BEFORE commit. Since `--from-main` does `git checkout origin/main -- .beads/`, this would overwrite any local beads changes.

## Research Findings

### Key Discovery
The `--from-main` behavior is **INTENTIONAL**, not a bug. From `sync.go:1192-1194`:
```go
// doSyncFromMain performs a one-way sync from the default branch (main/master)
// Used for ephemeral branches without upstream tracking (gt-ick9)
// This fetches beads from main and imports them, discarding local beads changes.
```

### Beads Architecture for Ephemeral Branches

| Concern | Location | Rationale |
|---------|----------|-----------|
| **Code changes** | Ephemeral branches | Isolation for parallel work |
| **Beads mutations** | Main branch only | Single source of truth |
| **Daemon** | Main worktree only | Prevents race conditions |
| **Sync direction** | One-way (main → ephemeral) | Simpler, no merge conflicts |

### Why This Design Makes Sense

1. **Single source of truth**: All beads changes go through main's daemon
2. **No race conditions**: Only one process commits beads
3. **Clean merges**: Code-only branches have simpler merge conflicts
4. **Protected branch compatible**: Via sync-branch (beads-metadata) + PR

## Decision: Option A (Revised)

**Push branches at worktree creation to avoid ephemeral branch issues entirely.**

### The Key Insight

"Ephemeral" in beads just means "no upstream tracking". The detection is:
```go
// git rev-parse --abbrev-ref --symbolic-full-name @{u}
// Returns error if no upstream = ephemeral
```

**Solution**: Push branches at worktree creation. They become non-ephemeral and use normal `bd sync`.

### Correct Workflow

```
┌─────────────────────────────────────────────────────────────┐
│                    CORRECT WORKFLOW                          │
├─────────────────────────────────────────────────────────────┤
│                                                              │
│  WORKTREE CREATION (Azedarach orchestrator):                │
│    git worktree add ../project-az-123 -b az-123             │
│    git push -u origin az-123   ← Makes branch non-ephemeral │
│                                                              │
│  ON THE BRANCH (agent session):                             │
│    1. Work on code AND beads (both allowed!)                │
│    2. bd update, bd close, bd create - all work normally    │
│    3. bd sync                  (normal bidirectional sync)  │
│    4. git add && git commit                                 │
│    5. git push                                              │
│                                                              │
│  PR/MERGE:                                                  │
│    Create PR from az-123 → main                             │
│    Code AND beads changes merge together                    │
│                                                              │
└─────────────────────────────────────────────────────────────┘
```

### Why This Works

1. `bd prime` detects upstream exists → outputs normal `bd sync` protocol
2. No conflicting instructions between hook and CLAUDE.md
3. Agents can modify beads normally (notes, status, closures)
4. Bidirectional sync works - beads changes push to branch, pull from remote
5. PRs need the branch pushed anyway - no extra step

### For Truly Ephemeral Branches (Unpushed)

If you CAN'T push the branch:
- DON'T run `bd sync --from-main` at session end (overwrites local beads)
- Instead: `git add -A && git commit` (includes .beads/ changes)
- Merge to main later - beads changes propagate via git merge

## Implications for Azedarach

### WorktreeManager.ts
```typescript
// Create worktree
await exec(`git worktree add ../${projectName}-${beadId} -b ${branchName}`)

// Push to make non-ephemeral (enables normal bd sync)
await exec(`git -C ../${projectName}-${beadId} push -u origin ${branchName}`)
```

### Session Behavior
- Agents work on code AND beads normally
- `bd sync` works bidirectionally
- No special handling needed for beads closures

## Files Referenced

- `~/.claude/plugins/marketplaces/beads-marketplace/cmd/bd/sync.go` (lines 1192-1255)
- `~/.claude/plugins/marketplaces/beads-marketplace/cmd/bd/prime.go` (lines 130-140, 175-190)
- `~/.claude/plugins/marketplaces/beads-marketplace/docs/WORKTREES.md`
- `~/.claude/plugins/marketplaces/beads-marketplace/docs/PROTECTED_BRANCHES.md`
- `~/.claude/plugins/marketplaces/beads-marketplace/CHANGELOG.md` (v0.25.0 - ephemeral branch sync)

## Related

- Beads issue tracking: Works as designed, no PR needed
- Azedarach architecture: Update orchestration to close beads on main
