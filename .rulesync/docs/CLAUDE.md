<!--
File: CLAUDE.md
Version: 1.0.0
Updated: 2025-12-21
Purpose: Root entry point - redirects to app-specific context
-->

<ai_context version="1.0" tool="claude">

# Azedarach Project - Multi-Implementation

> TUI Kanban board for orchestrating parallel Claude Code sessions with issue tracking

This repository contains multiple implementations of Azedarach, each in its own directory:

## Implementations

### 🚀 ts-opentui/ (Primary, Active Development)
**Tech Stack:** TypeScript, Bun, OpenTUI, Effect, React

**Entry Point:** `ts-opentui/CLAUDE.md`

**Status:** Active development, most features implemented

**Use when:**
- Working on the main implementation
- User requests TypeScript/Bun work
- Effect/OpenTUI patterns needed

**Quick Start:**
```bash
cd ts-opentui
bun run dev              # Start development TUI
bun run type-check       # Full project check
bun run build            # Build the project
```

---

### 🧊 go-bubbletea/ (Alternative Implementation)
**Tech Stack:** Go, Bubbletea, Lip Gloss

**Entry Point:** `go-bubbletea/CLAUDE.md`

**Status:** Implemented, alternative implementation

**Use when:**
- Working on the Go implementation
- User requests Go work
- Bubbletea patterns needed

**Quick Start:**
```bash
cd go-bubbletea
make build              # Build Go binary
make run                # Build and run
make test               # Run tests
```

---

### 🧪 gleam/ (Experimental)
**Tech Stack:** Gleam (Beam/Erlang VM)

**Status:** Experimental, not actively developed

**Note:** For exploration purposes only

---

## Shared Critical Rules (Apply to ALL Implementations)

1. **🚨 CRITICAL: Commit Before Done 🚨**: Before saying "done", "complete", "finished", or stopping work, you MUST commit all changes.

   **MANDATORY CHECKLIST** (run these commands):
   ```bash
   git status                    # Check for uncommitted changes
   git add -A                    # Stage all changes
   git commit -m "descriptive message"   # Commit with clear message
   ```

2. **Modern CLI Tools**: ALWAYS use `rg` (NOT grep), `fd` (NOT find), `bat` (NOT cat). 10x faster, gitignore-aware.

3. **Issue Tracking**: ALWAYS start with `az prime`, then use `az issue` for issue operations.

4. **Branch Workflow**: Azedarach pushes branches at worktree creation (`git push -u`) so they have upstreams. Use normal git pull/push flow for synchronization.

5. **File Deletion**: NEVER delete untracked files without permission. Check references first (`rg "filename"`).

6. **Git Restore**: NEVER use `git restore` without EXPLICIT user permission.

## RuleSync Workflow

The following instruction assets are managed from `.rulesync/` and synced into runtime paths:
- `.claude/agents/`
- `.claude/commands/`
- `.claude/hooks/`
- `.claude/session-templates/`
- `.claude/skills/`
- `AGENTS.md`, `CLAUDE.md`, `ts-opentui/AGENTS.md`, `ts-opentui/CLAUDE.md`, `go-bubbletea/CLAUDE.md`

Use:
```bash
just rulesync-sync
just rulesync-check
```

Git hooks run sync automatically on checkout/merge. Set `RULESYNC_SKIP=1` to bypass when needed.

OpenCode plugin assets remain outside RuleSync management.

## Task Management

**Track ALL work through issue tracking** (preserves context across sessions).

Run `az prime` first in each AI session, then use `az issue` commands for tracking:

```bash
az issue --help
az issue get <issue-id>
az issue create "Issue title" --description "Detailed context"
az issue update <issue-id> --notes "progress update"
az issue close <issue-id>
```

## OpenCode Plugins

This project uses two OpenCode plugins:

1. **opencode-pty** - PTY integration
2. **.opencode/plugin/azedarach.js** - Session status monitoring for TUI

Both are configured in `opencode.json`.

## Decision Matrix

When user requests work, use this matrix to decide which implementation to work on:

| Request | Implementation | Rationale |
|---------|---------------|------------|
| Default / unspecified | ts-opentui/ | Primary, most mature |
| "TypeScript", "Bun", "Effect" | ts-opentui/ | Tech-specific match |
| "Go", "Bubbletea" | go-bubbletea/ | Tech-specific match |
| "Gleam", "Erlang", "BEAM" | gleam/ | Experimental match |
| Explicit app folder mentioned | That folder | User-specified |

## Shared Skills

This repository has shared skills in `.claude/skills/` that apply to all implementations:

- **Workflow Skills** (`workflow/`): TDD patterns, retrospectives
- **Effect Skills** (`effect/`): Effect patterns (ts-opentui only)
- **Gleam Skills** (`gleam/`): Gleam patterns (gleam only)

See `.claude/skills/README.md` for skill documentation.

</ai_context>
