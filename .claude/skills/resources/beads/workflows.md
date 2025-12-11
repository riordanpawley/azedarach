# Beads Workflow Patterns & Checklists

## Session Start Workflow

**Key principle**: "Always run `bd ready` when starting work where bd is available."

### Checklist: Starting Work

- [ ] Check for `.beads/` directory existence
- [ ] Run `bd ready` to find available work
- [ ] Run `bd search "" --status=in_progress` for active items
- [ ] Run `bd stats` for project overview
- [ ] Report findings to user
- [ ] If work found: Show issue details with `bd show <issue-id>`
- [ ] Get user direction on which work to tackle
- [ ] Update chosen issue to `in_progress` status
- [ ] Begin work with full context from notes

## Compaction Survival Workflow

**Problem**: Conversation history deleted, only bd persists.

### Recovery Process

After compaction (history gone):

- [ ] Run `bd search "" --status=in_progress`
- [ ] Run `bd show <issue-id>` for each in-progress item
- [ ] Read notes field for context (COMPLETED, IN PROGRESS, BLOCKERS)
- [ ] Check dependency relationships with `bd show`
- [ ] Reconstruct work state from saved data
- [ ] Report status to user
- [ ] Update status and continue work

### Note Quality Requirements

Notes must enable full recovery WITHOUT conversation history.

**Bad note** (useless after compaction):
```
"Working on the feature"
```

**Good note** (resumable):
```
COMPLETED:
- Implemented SessionManager with tmux integration
- Added state detection patterns

IN PROGRESS:
- Adding worktree cleanup on task completion
- Current file: src/core/WorktreeManager.ts
- Implemented creation, next: cleanup logic

BLOCKERS:
- None

KEY DECISIONS:
- Using tmux over screen (better programmatic control)
- Worktree naming: ../Project-<bead-id>
```

### When to Update Notes

**Proactively update at**:
- 70% token usage (compaction imminent)
- Hitting blockers (preserve investigation context)
- Reaching milestones (completion checkpoint)
- Before major transitions (switching focus areas)
- End of session (handoff to future session)

## Epic Planning Workflow

**Use when**: Complex multi-step feature with 5+ related tasks.

### Process: Planning Epic

- [ ] Create epic for high-level goal
- [ ] Break into granular child tasks (each < 1 day)
- [ ] Create all child task issues
- [ ] Establish parent-child relationships
- [ ] Add blocking relationships between children (order matters)
- [ ] Work through in dependency order using `bd ready`

### Example: Epic Planning

```bash
# Create epic
bd create --title="Implement TUI Kanban board" --type=epic --priority=1

# Create subtasks
bd create --title="Create Board component with columns" --type=task --priority=1
bd create --title="Implement TaskCard component" --type=task --priority=1
bd create --title="Add keyboard navigation" --type=task --priority=1

# Epic depends on children
bd dep add AZ-100 AZ-101
bd dep add AZ-100 AZ-102
bd dep add AZ-100 AZ-103

# Work through in order
bd ready  # Shows children (not blocked)
```

## Session Handoff Workflow

**Purpose**: Ensure next session can resume seamlessly.

### At Session End Checklist

- [ ] Recognize logical stopping point
- [ ] Review what was accomplished this session
- [ ] Note current state (what's in progress, file locations)
- [ ] Identify concrete next step
- [ ] Document any blockers found
- [ ] Capture key decisions made
- [ ] Update notes with structured handoff
- [ ] Verify notes make sense without conversation context

### Notes Structure Template

```
COMPLETED:
- [Specific deliverable 1]
- [Specific deliverable 2]
- [Files modified: path/to/file1.ts, path/to/file2.ts]

IN PROGRESS:
- [Current focus area]
- [Current file: path/to/current/file.ts]
- [What's done in this area, what's left]

NEXT:
- [Concrete next step - specific enough to start immediately]
- [File to work on: path/to/next/file.ts]

BLOCKERS:
- [Blocker 1 if any]
- None (if no blockers)

KEY DECISIONS:
- [Decision 1 with brief rationale]
- [Decision 2 with brief rationale]
```

## Discovery & Side Quests

### Process: Discovering New Work

- [ ] Notice emerging work during implementation
- [ ] Assess blocker status (does it block current work?)
- [ ] Create issue immediately with context
- [ ] Link via `discovered-from` dependency to current work
- [ ] If blocking: Pause current work, tackle blocker
- [ ] If deferrable: Continue current work, defer new issue

### Ask vs Create Decision

**Ask first when**:
- Knowledge work with fuzzy boundaries
- Task scope unclear
- Multiple valid approaches exist

**Create directly when**:
- Clear bug discovered during implementation
- Obvious follow-up work identified
- Technical debt with clear scope
- Dependency/blocker found

## Multi-Session Resume Workflow

**Scenario**: Returning after extended absence.

### Process: Resuming After Time Away

- [ ] Run `bd ready` to see available work
- [ ] Run `bd stats` for project overview
- [ ] Review ready work options
- [ ] Choose work item based on priority/interest
- [ ] Run `bd show <issue-id>` for full details
- [ ] Study design notes (implementation approach)
- [ ] Review acceptance criteria (definition of done)
- [ ] Update status to `in_progress`
- [ ] Begin work with full context
