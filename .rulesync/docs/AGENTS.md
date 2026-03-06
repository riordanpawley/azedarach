<!--
File: AGENTS.md
Version: 1.3.0
Updated: 2026-03-05
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
3. **Issue Tracker**: Start sessions with `az prime`, then use `az issue` for all tracked issue operations.
4. **Commit Before Done**: Always commit all changes before saying "done" or "complete".
5. **RuleSync Canonical Source**: Edit managed instruction assets in `.rulesync/` and sync, not direct edits in generated targets.
6. **Git CWD Discipline**: When already in the target worktree/repo, use plain `git` commands. Use `git -C <path>` only when intentionally targeting a different path.

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
az prime                                                 # Session primer + AI workflow guide

# RuleSync
just rulesync-sync                                       # Sync managed files from .rulesync/
just rulesync-check                                      # Verify managed files are in sync
```

## RuleSync Workflow

Managed paths are generated from `.rulesync/`:
- `.claude/agents/`
- `.claude/commands/`
- `.claude/hooks/`
- `.claude/session-templates/`
- `.claude/skills/`
- `AGENTS.md`, `CLAUDE.md`, `ts-opentui/AGENTS.md`, `ts-opentui/CLAUDE.md`, `go-bubbletea/CLAUDE.md`

Sync behavior:
- `post-checkout` and `post-merge` run `rulesync generate --silent`
- Set `RULESYNC_SKIP=1` to bypass auto-sync for one command/session
- OpenCode plugin files are intentionally not managed by RuleSync

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
- **ts-opentui**: TypeScript, OpenTUI + React, Effect, tmux, az issue tracker
- **go-bubbletea**: Go, Bubbletea, Lip Gloss, tmux, az issue tracker

## Task Management

**Track all non-trivial work through issue tracking** (preserves context across sessions).

- Run `az prime` at the start of each AI session.
- Use `az issue` for all issue updates and transitions.
- Any task expected to take more than one command MUST be tracked in the issue tracker.
- Keep one active parent issue per session whenever possible.
- If subagents are used, each subagent MUST create and maintain a child issue under the active parent issue, and close it when that subagent task is done.

## OpenCode Plugins

This project uses two OpenCode plugins:

1. **opencode-pty** - PTY integration
2. **.opencode/plugin/azedarach.js** - Session status monitoring for TUI

Both are configured in `opencode.json`.

</ai_context>

## Issue Tracking Policy

**IMPORTANT**: This project uses **`az issue`** as the issue tracking interface. Do NOT use markdown TODOs, task lists, or parallel tracking systems.

### Important Rules

- ✅ First command in a new AI session: `az prime`
- ✅ Use `az issue` for issue tracking commands
- ✅ `az prime` is the source of AI workflow guidance for issue tracking
- ✅ Track every task that takes more than one command in the issue tracker
- ✅ Keep a session scoped to one active parent issue at a time when possible
- ✅ Subagents must create, maintain, and close child issues linked to the active parent issue
- ✅ Keep issue status updated as work progresses
- ✅ Keep the issue tracker as the single source of truth for issue state
- ❌ Do NOT create markdown TODO lists as a parallel tracker

## Landing the Plane (Session Completion)

**When ending a work session**, you MUST complete ALL steps below.

**MANDATORY WORKFLOW:**

1. **File issues for remaining work** - Create issues for anything that needs follow-up
2. **Run quality gates** (if code changed) - Tests, linters, builds
3. **Update issue status** - Close finished work, update in-progress items
4. **Apply the correct git finalization flow for this repo mode**:
   - **Local workflow mode** (`git.workflowMode: "local"` or remote git disabled):
     ```bash
     git status
     ```
     Do not run remote cleanup/sync commands (`git pull --rebase`, `git push`, remote prune) unless explicitly requested.
   - **Origin workflow mode** (`git.workflowMode: "origin"`):
     ```bash
     git pull --rebase
     git push
     git status  # MUST show "up to date with origin"
     ```
5. **Clean up** - Clear stashes, prune remote branches
6. **Verify** - All changes committed; pushed when origin mode requires it
7. **Hand off** - Provide context for next session

**CRITICAL RULES:**
- When already in the target worktree/repo, use plain `git` commands. Use `git -C <path>` only when intentionally targeting a different path.
- In local workflow mode, avoid remote git flows unless explicitly requested.
- In origin workflow mode, work is NOT complete until `git push` succeeds.
- If an origin-mode push fails, resolve and retry until it succeeds.
