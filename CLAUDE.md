<!--
File: CLAUDE.md
Version: 1.0.0
Updated: 2025-12-21
Purpose: Root entry point - redirects to app-specific context
-->

<ai_context version="1.0" tool="claude">

# Azedarach Project - Multi-Implementation

> TUI Kanban board for orchestrating parallel Claude Code sessions with Beads task tracking

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

3. **Beads Tracker**: ALWAYS use `bd` CLI commands for beads operations. Use `bd search` for discovery, `bd ready` for unblocked work. NEVER use `bd list` (causes context bloat).

4. **Branch Workflow**: Azedarach pushes branches at worktree creation (`git push -u`) so they have upstreams and use normal `bd sync`. If you're on a truly ephemeral branch (no upstream), DON'T run `bd sync --from-main` at session end.

5. **File Deletion**: NEVER delete untracked files without permission. Check references first (`rg "filename"`).

6. **Git Restore**: NEVER use `git restore` without EXPLICIT user permission.

## Task Management

**Track ALL work in beads** (preserves context across sessions):

```bash
bd ready                          # Find available work
bd update <id> --status=in_progress  # Claim it
bd close <id>                     # Mark complete
```

## OpenCode Plugins

This project uses two OpenCode plugins:

1. **opencode-beads** - Beads integration (bd prime, /bd-* commands)
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
