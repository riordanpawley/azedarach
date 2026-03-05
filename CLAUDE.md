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

3. **Issue Tracking**: ALWAYS use `linear-cli` for issue operations. Prefer `linear-cli i list`, `linear-cli i get`, `linear-cli i create`, `linear-cli i update`, and `linear-cli i close`.

4. **Branch Workflow**: Azedarach pushes branches at worktree creation (`git push -u`) so they have upstreams. Use normal git pull/push flow for synchronization.

5. **File Deletion**: NEVER delete untracked files without permission. Check references first (`rg "filename"`).

6. **Git Restore**: NEVER use `git restore` without EXPLICIT user permission.

## Task Management

**Track ALL work through issue tracking** (preserves context across sessions):
Configure `issueTracker.linear.team` in `.azedarach.json` (or run `linear-cli setup`) to avoid passing `-t` for every `i create`.

```bash
linear-cli i list --output json --compact --all
linear-cli i start <id>
linear-cli i update <id> --output json --compact ...
linear-cli i close <id>
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
