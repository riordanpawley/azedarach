<!--
File: CLAUDE.md
Version: 1.0.0
Updated: 2025-12-11
Purpose: Claude Code entry point for Azedarach development
-->

<ai_context version="1.0" tool="claude">

# Azedarach Project Context

> TUI Kanban board for orchestrating parallel Claude Code sessions with Beads task tracking

## Critical Rules (Always Apply)

1. **Type Safety**: ALWAYS use TypeScript strict mode. NEVER use 'as' casting or 'any'.

2. **Modern CLI Tools**: ALWAYS use `rg` (NOT grep), `fd` (NOT find), `sd` (NOT sed), `bat` (NOT cat). 10x faster, gitignore-aware.

3. **Beads Tracker**: ALWAYS use `bd` CLI commands for beads operations. Use `bd search` for discovery, `bd ready` for unblocked work. NEVER use `bd list` (causes context bloat). **In worktrees: run `bd sync` manually.** See beads-tracking.skill.md for details.

4. **File Deletion**: NEVER delete untracked files without permission. Check references first (`rg "filename"`).

5. **Git Restore**: NEVER use `git restore` without EXPLICIT user permission.

6. **Beads Tracking**: ALWAYS track ALL work in beads. Update notes during work. Close with summary when done.

## Quick Commands

```bash
# Development
pnpm dev                          # Start development

# Type Checking
pnpm type-check                   # Full project check

# Search (modern tools)
rg "pattern" --type ts            # Search content (NOT grep)
fd "filename" -t f                # Find files (NOT find)

# Beads (Task Management)
bd search "keywords"              # Search issues (PRIMARY - not list!)
bd ready                          # Find unblocked work
bd create --title="..." --type=task  # Create issue
bd update <id> --status=in_progress  # Update status/notes
bd close <id>                     # Mark complete
bd sync                           # REQUIRED in worktrees (manual sync)
```

## Project Overview

**Azedarach:** TUI Kanban board for parallel Claude Code orchestration

**Stack:**
- TypeScript (strict mode)
- Ink (React for CLI)
- tmux (session persistence)
- node-pty (PTY handling)
- Beads (task tracking backend)

**Core Features:**
- Kanban board displaying beads issues
- Spawn Claude sessions in isolated git worktrees
- Monitor session state (busy/waiting/done/error)
- Auto-create GitHub PRs on completion
- Attach to sessions for manual intervention

## Architecture

```
src/
├── index.tsx              # Entry point
├── cli.ts                 # CLI argument parsing
│
├── ui/                    # Ink components
│   ├── App.tsx            # Root component
│   ├── Board.tsx          # Kanban board
│   ├── Column.tsx         # Status column
│   ├── TaskCard.tsx       # Task card
│   └── StatusBar.tsx      # Bottom status bar
│
├── core/                  # Business logic
│   ├── SessionManager.ts  # Claude session orchestration
│   ├── WorktreeManager.ts # Git worktree lifecycle
│   ├── StateDetector.ts   # Output pattern matching
│   ├── BeadsClient.ts     # bd CLI wrapper
│   └── PRWorkflow.ts      # GitHub PR automation
│
├── hooks/                 # State transition hooks
│   ├── onWaiting.ts       # Notify when Claude waits
│   ├── onDone.ts          # PR creation
│   └── onError.ts         # Error handling
│
└── config/                # Configuration
    ├── schema.ts          # Config validation (zod)
    └── defaults.ts        # Default values
```

## Beads Task Management

**Track ALL work** - preserves context across sessions, enables resumability.

**Quick workflow (CLI):**
1. User requests work → Search: `bd search "keywords"` or check `bd ready`
2. Start work → Update: `bd update <id> --status=in_progress`
3. During work → Add notes: `bd update <id> --notes="..."`
4. Complete → Close: `bd close <id> --reason="..."`

**Essential CLI commands:**
- `bd search "pattern"` - Search issues (PRIMARY discovery tool)
- `bd ready` - Find unblocked work
- `bd create --title="..." --type=task` - Create new issue
- `bd update <id> --status=in_progress` - Update status, notes
- `bd close <id>` - Mark work complete
- `bd show <id>` - Get issue details
- `bd list` - NEVER USE (causes context bloat)
- `bd dep add <issue> <depends-on>` - Add dependencies

**Worktree sync:**
- **Main worktree**: Auto-sync works normally
- **Other worktrees**: Run `bd sync` manually at session end

**Full reference:** `.claude/skills/workflow/beads-tracking.skill.md`

## Key Design Decisions

### Session State Detection

Detect Claude session state via output pattern matching:

```typescript
const PATTERNS = {
  waiting: [/\[y\/n\]/i, /Do you want to/i],
  done: [/Task completed/i, /Successfully/i],
  error: [/Error:|Exception:|Failed:/i],
};
```

### Worktree Naming

Worktrees created as siblings to the project:
```
../ProjectName-<bead-id>/
```

### Epic/Task Handling

- Epic → dedicated worktree
- Task with epic parent → use epic's worktree
- Standalone task → dedicated worktree

### PR Workflow

Default: Auto-create draft PR, notify user
Configurable: Ready PR, auto-merge after CI, immediate merge

## Skills

Skills auto-load when you edit files or mention keywords:

**Workflow Skills:**
- `.claude/skills/workflow/beads-tracking.skill.md` - Issue tracking workflow

## Development Tips

- **Type errors:** Always run `pnpm type-check` for validation
- **Files:** Check references before deleting (`rg "filename"`)
- **Testing:** Test TUI components with `ink-testing-library`

## Documentation

**IMPORTANT:** Keep the user guide updated when implementing features.

**Documentation location:** `docs/`

| File | Purpose |
|------|---------|
| `docs/README.md` | Main user guide index - UPDATE when adding features |
| `docs/keybindings.md` | Keybinding reference |
| `docs/services.md` | Effect services architecture |
| `docs/testing.md` | Testing guide |
| `docs/tmux-guide.md` | tmux primer for new users |

**When to update docs:**
- Adding new keybindings → Update `keybindings.md` AND `README.md`
- Adding new services → Update `services.md`
- Changing test procedures → Update `testing.md`
- Any user-facing feature → Update `README.md`

## Quick Help

- Workflow help: Use beads-tracking skill
- Architecture: See README.md for full spec
- User guide: See `docs/README.md`

</ai_context>
