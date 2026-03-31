---
root: true
targets:
  - agentsmd
---

<!--
File: CONTEXT.md
Version: 3.0.0
Updated: 2026-03-31
Purpose: Canonical root AI context source synced to AGENTS.md entrypoints
-->

<ai_context version="1.0" tool="shared">

# Azedarach Project Context

> Go/Bubbletea TUI Kanban board for orchestrating parallel AI sessions with issue tracking

## Entrypoint Generation

This file is the canonical root context source in `.rulesync/rules/`.

RuleSync generates root `AGENTS.md` from path-scoped rule sources.

## Instructions Reference

This repository is now single-implementation:

- Root implementation -> [AGENTS.md](./AGENTS.md) (Go, Bubbletea)

## Critical Rules (Quick Reference)

1. **Modern CLI Tools**: Use `rg` (not grep), `fd` (not find), `sd` (not sed), and `bat` (not cat).
2. **Issue Tracker**: Start sessions with `az prime`, then use `az issue` for all tracked issue operations.
3. **Commit Before Done**: Always commit all changes before saying "done" or "complete".
4. **RuleSync Canonical Source**: Edit managed instruction assets in `.rulesync/` and sync, not direct edits in generated targets.
5. **Git CWD Discipline**: When already in the target worktree/repo, use plain `git` commands. Use `git -C <path>` only when intentionally targeting a different path.
6. **Branch Workflow**: Use local-only git flow by default. Do not run remote sync/cleanup commands (for example pull/rebase, push, remote prune) unless explicitly requested.
7. **Safe File Operations**: Never delete untracked files or run `git restore` without explicit permission.
8. **No Message Parsing for Logic Gates**: Never gate behavior by parsing free-form error/message text. Use typed/tagged errors and explicit control flow.
9. **If I say "merge in main" I mean merge this branch INTO main**: Not merge this branch into main

## Quick Commands

```bash
# Go (root)
just build                        # Build Go binaries
just test                         # Run tests
go run ./cmd/az                   # Run CLI

# Search (modern tools)
rg "pattern" --type go            # Search content
fd "filename" -t f                # Find files

# Issue Tracking
az prime                          # Session primer + AI workflow guide
az issue list --limit 20
az issue get <issue-id>

# RuleSync
just rulesync-sync                # Sync managed files from .rulesync/
just rulesync-check               # Verify managed files are in sync
```

## RuleSync Workflow

Managed paths are generated from `.rulesync/`:
- `.rulesync/rules/` -> `AGENTS.md`
- `.rulesync/subagents/` -> Codex/OpenCode subagent outputs
- `.rulesync/skills/` -> Codex/OpenCode skill outputs

Canonical context source:
- `./.rulesync/rules/root-context.md` -> `AGENTS.md`

Sync behavior:
- `post-checkout` and `post-merge` run `rulesync generate -c rulesync.jsonc --silent`
- `rulesync.jsonc` uses per-target features:
  - `agentsmd`: `rules`
  - `codexcli`/`opencode`: `subagents`, `skills` only
- OpenCode rules are intentionally disabled to avoid `.opencode/memories` outputs
- Set `RULESYNC_SKIP=1` to bypass auto-sync for one command/session
- OpenCode plugin files are intentionally not managed by RuleSync

## Architecture Quick Reference

```
.
├── cmd/              # Main applications (az, azd)
├── internal/         # Private code (app, daemon, client, services, ui)
├── docs/             # Documentation
└── scripts/          # Test and maintenance scripts
```

**Stack:**
- **go-bubbletea**: Go, Bubbletea, Lip Gloss, tmux, az issue tracker

## Task Management

Track all non-trivial work through issue tracking.

- Run `az prime` at the start of each AI session.
- Use `az issue` for all issue updates and transitions.
- Any task expected to take more than one command must be tracked in the issue tracker.

## Landing the Plane (Session Completion)

When ending a work session:

1. File issues for remaining follow-up work.
2. Run quality gates (if code changed).
3. Update issue status.
4. Verify local repo state with `git status`.
5. Ensure all intended changes are committed locally.

## Shared Skills

This repository has shared skills in `.rulesync/skills/` that apply to current tasks.

See `.rulesync/skills/README.md` for skill documentation.

## OpenCode Plugins

This project uses two OpenCode plugins:

1. **opencode-pty** - PTY integration
2. **.opencode/plugin/azedarach.js** - Session status monitoring for TUI

Both are configured in `opencode.json`.

</ai_context>
