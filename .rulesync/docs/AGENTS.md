<!--
File: AGENTS.md
Version: 1.4.0
Updated: 2026-03-07
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

## Why This Exists Alongside `CLAUDE.md`

This repository intentionally keeps both files:
- `AGENTS.md` is the OpenCode/Codex entrypoint.
- `CLAUDE.md` is the Claude Code entrypoint.

RuleSync can generate both targets from a single source, so this split is a content strategy choice, not a generator limitation. We keep separate source files in `.rulesync/docs/` because framing and metadata can differ by runtime, and we may want independent edits without coupling both entrypoints.

## Critical Rules (Quick Reference)

1. **Type Safety**: ALWAYS use TypeScript strict mode. NEVER use 'as' casting or 'any' (ts-opentui only).
2. **Modern CLI Tools**: Use `rg` (not grep), `fd` (not find), `sd` (not sed).
3. **Issue Tracker**: Start sessions with `az prime`, then use `az issue` for all tracked issue operations.
4. **Commit Before Done**: Always commit all changes before saying "done" or "complete".
5. **RuleSync Canonical Source**: Edit managed instruction assets in `.rulesync/` and sync, not direct edits in generated targets.
6. **Git CWD Discipline**: When already in the target worktree/repo, use plain `git` commands. Use `git -C <path>` only when intentionally targeting a different path.
7. **Spec Sync Discipline (ts-opentui)**: Keep `docs/spec/` aligned with `ts-opentui` behavior improvements in the same task, or log `Spec impact: none` with file-specific rationale in issue notes.

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
- ✅ For `ts-opentui` behavior changes, update `docs/spec/` or document `Spec impact: none` with concrete file-based rationale
- ❌ Do NOT create markdown TODO lists as a parallel tracker

## Landing the Plane (Session Completion)

**When ending a work session**, you MUST complete ALL steps below.

**MANDATORY WORKFLOW:**

1. **File issues for remaining work** - Create issues for anything that needs follow-up
2. **Run quality gates** (if code changed) - Tests, linters, builds
3. **Update issue status** - Close finished work, update in-progress items
4. **Finalize locally**:
   ```bash
   git status
   ```
   Do not run remote sync/cleanup commands (for example pull/rebase, push, remote prune) unless explicitly requested.
5. **Clean up** - Clear stashes and local temporary state
6. **Verify** - All changes committed locally
7. **Hand off** - Provide context for next session

**CRITICAL RULES:**
- When already in the target worktree/repo, use plain `git` commands. Use `git -C <path>` only when intentionally targeting a different path.
- Avoid remote git flows unless explicitly requested.
- Never auto-run pull/rebase/push as part of completion.
