<!--
File: AGENTS.md
Version: 1.1.0
Updated: 2026-03-04
Purpose: OpenCode entry point - references CLAUDE.md for full context
-->
<ai_context version="1.0" tool="opencode">

# Azedarach Project Context

> TUI Kanban board for orchestrating parallel Claude Code sessions with issue tracking

## Instructions Reference

**This repository has multiple implementations:**

- **ts-opentui/** → [CLAUDE.md](./ts-opentui/CLAUDE.md) (TypeScript, Bun, OpenTUI, Effect)
- **go-bubbletea/** → [CLAUDE.md](./go-bubbletea/CLAUDE.md) (Go, Bubbletea)

Select the implementation based on user request or current working directory.

This file provides a condensed reference for OpenCode sessions.

## Critical Rules (Quick Reference)

1. **Type Safety**: ALWAYS use TypeScript strict mode. NEVER use 'as' casting or 'any' (ts-opentui only).
2. **Modern CLI Tools**: Use `rg` (not grep), `fd` (not find), `sd` (not sed).
3. **Issue Tracker**: Use `linear-cli` for issue tracking. Prefer `linear-cli i list` / `linear-cli i get` / `linear-cli i create` / `linear-cli i update` / `linear-cli i close`.
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

# Issue Tracking
linear-cli i list --output json --compact --all             # List issues
linear-cli i get <id> --output json --compact               # Show issue details
linear-cli i create "Title" --output json --compact         # Create issue (uses configured default team)
linear-cli i update <id> --output json --compact ...        # Update issue
linear-cli i close <id>                                     # Mark complete
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
│   ├── services/     # Business logic (Linear, Tmux, Git)
│   ├── types/        # Domain models
│   └── ui/          # Bubbletea UI components
└── docs/             # Documentation
```

**Stacks:**
- **ts-opentui**: TypeScript, OpenTUI + React, Effect, tmux, Linear CLI
- **go-bubbletea**: Go, Bubbletea, Lip Gloss, tmux, Linear CLI

## Task Management

**Track ALL work through issue tracking** (preserves context across sessions):

```bash
linear-cli i list --output json --compact --all
linear-cli i update <id> --output json --compact ...
linear-cli i close <id>
```

## OpenCode Plugins

This project uses two OpenCode plugins:

1. **opencode-pty** - PTY integration
2. **.opencode/plugin/azedarach.js** - Session status monitoring for TUI

Both are configured in `opencode.json`.

</ai_context>

## Issue Tracking with linear-cli

**IMPORTANT**: This project uses **`linear-cli`** for issue tracking. Do NOT use markdown TODOs, task lists, or parallel tracking systems.

### Quick Start

```bash
# List issues
linear-cli i list --output json --compact --all

# Show issue details
linear-cli i get <id> --output json --compact

# Create issue
linear-cli i create "Issue title" --output json --compact

# Start / update / close
linear-cli i start <id>
linear-cli i update <id> --output json --compact ...
linear-cli i close <id>
```

### Important Rules

- ✅ Use `linear-cli` for issue tracking commands
- ✅ Configure default team once in `.azedarach.json` (`issueTracker.linear.team`) or via `linear-cli setup`
- ✅ Keep issue status updated as work progresses
- ✅ Keep the issue tracker as the single source of truth for issue state
- ❌ Do NOT create markdown TODO lists as a parallel tracker

## Landing the Plane (Session Completion)

**When ending a work session**, you MUST complete ALL steps below. Work is NOT complete until `git push` succeeds.

**MANDATORY WORKFLOW:**

1. **File issues for remaining work** - Create issues for anything that needs follow-up
2. **Run quality gates** (if code changed) - Tests, linters, builds
3. **Update issue status** - Close finished work, update in-progress items
4. **PUSH TO REMOTE** - This is MANDATORY:
   ```bash
   git pull --rebase
   git push
   git status  # MUST show "up to date with origin"
   ```
5. **Clean up** - Clear stashes, prune remote branches
6. **Verify** - All changes committed AND pushed
7. **Hand off** - Provide context for next session

**CRITICAL RULES:**
- Work is NOT complete until `git push` succeeds
- NEVER stop before pushing - that leaves work stranded locally
- NEVER say "ready to push when you are" - YOU must push
- If push fails, resolve and retry until it succeeds
