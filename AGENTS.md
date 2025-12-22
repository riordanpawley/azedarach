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

**See [CLAUDE.md](./CLAUDE.md) for comprehensive project instructions, critical rules, and architecture documentation.**

This file provides a condensed reference for OpenCode sessions.

## Critical Rules (Quick Reference)

1. **Type Safety**: ALWAYS use TypeScript strict mode. NEVER use 'as' casting or 'any'.
2. **Modern CLI Tools**: Use `rg` (not grep), `fd` (not find), `sd` (not sed).
3. **Beads Tracker**: Use `bd` CLI commands. `bd search` for discovery, `bd ready` for unblocked work. NEVER `bd list`.
4. **Effect Patterns**: Services grab dependencies at layer construction, not via Effect.provide().
5. **Commit Before Done**: Always commit all changes before saying "done" or "complete".

## Quick Commands

```bash
# Development
bun run dev                       # Start development TUI
bun run type-check                # Full project check
bun run build                     # Build the project

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
src/
├── ui/           # OpenTUI + React components (Board, TaskCard, etc.)
├── core/         # Effect services (SessionManager, TmuxService, etc.)
├── services/     # Application services (Navigation, Editor, etc.)
└── config/       # Configuration and schemas
```

**Stack:** TypeScript, OpenTUI + React, Effect, tmux, Beads

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
