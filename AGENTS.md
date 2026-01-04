<!--
File: AGENTS.md
Version: 1.0.0
Updated: 2025-12-22
Purpose: OpenCode entry point - references CLAUDE.md for full context
-->
<ai_context version="1.0" tool="opencode">

# Azedarach Project Context

> TUI Kanban board for orchestrating parallel Claude Code sessions with Beads task tracking

## Instructions Reference

**This repository has multiple implementations:**

- **ts-opentui/** → [CLAUDE.md](./ts-opentui/CLAUDE.md) (TypeScript, Bun, OpenTUI, Effect)
- **go-bubbletea/** → [CLAUDE.md](./go-bubbletea/CLAUDE.md) (Go, Bubbletea)

Select the implementation based on user request or current working directory.

This file provides a condensed reference for OpenCode sessions.

## Critical Rules (Quick Reference)

1. **Type Safety**: ALWAYS use TypeScript strict mode. NEVER use 'as' casting or 'any' (ts-opentui only).
2. **Modern CLI Tools**: Use `rg` (not grep), `fd` (not find), `sd` (not sed).
3. **Beads Tracker**: Use `bd` CLI commands. `bd search` for discovery, `bd ready` for unblocked work. NEVER `bd list`.
4. **Commit Before Done**: Always commit all changes before saying "done" or "complete".

## Quick Commands

```bash
# ts-opentui (TypeScript/Bun)
cd ts-opentui
bun run dev                       # Start development TUI
bun run type-check                # Full project check
bun run build                     # Build the project

# go-bubbletea (Go)
cd go-bubbletea
make build                        # Build Go binary
make test                         # Run tests
make run                          # Build and run

# Search (modern tools)
rg "pattern" --type ts            # Search content (NOT grep)
fd "filename" -t f                # Find files (NOT find)

# Beads (Task Management)
bd search "keywords"              # Search issues (PRIMARY - not list!)
bd ready                          # Find unblocked work
bd create --title="..." --type=task  # Create issue
bd update <id> --status=in_progress  # Update status
bd close <id>                     # Mark complete
```

## Architecture Quick Reference

```
ts-opentui/
├── src/
│   ├── ui/           # OpenTUI + React components (Board, TaskCard, etc.)
│   ├── core/         # Effect services (SessionManager, TmuxService, etc.)
│   ├── services/     # Application services (Navigation, Editor, etc.)
│   └── config/       # Configuration and schemas

go-bubbletea/
├── cmd/              # Main applications (minimal wiring)
├── internal/         # Private code (app, services, types, ui)
│   ├── app/          # Bubbletea application logic
│   ├── services/     # Business logic (Beads, Tmux, Git)
│   ├── types/        # Domain models
│   └── ui/          # Bubbletea UI components
└── docs/             # Documentation
```

**Stacks:**
- **ts-opentui**: TypeScript, OpenTUI + React, Effect, tmux, Beads
- **go-bubbletea**: Go, Bubbletea, Lip Gloss, tmux, Beads

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

</ai_context>
