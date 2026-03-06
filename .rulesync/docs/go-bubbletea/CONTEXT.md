<!--
File: CONTEXT.md
Version: 1.0.0
Updated: 2026-03-07
Purpose: Canonical go-bubbletea AI context source synced to nested entrypoints
-->

<ai_context version="1.0" tool="shared">

# Azedarach Context - go-bubbletea

> Go/Bubbletea implementation context for Azedarach

## Entrypoint Generation

This file is the canonical go-bubbletea context source in `.rulesync/docs/go-bubbletea/`.

RuleSync mapping fans this one file out to:
- `go-bubbletea/AGENTS.md` (OpenCode/Codex entrypoint filename)
- `go-bubbletea/CLAUDE.md` (Claude Code entrypoint filename)

## Critical Rules

1. **Go Layout Discipline**: Follow the standard Go layout in this repo (`cmd/`, `internal/`, `docs/`, `testdata/`).
2. **Modern CLI Tools**: Use `rg` (not grep), `fd` (not find), and `bat` (not cat).
3. **Issue Tracking**: Start with `az prime`, then use `az issue` for tracked work.
4. **Git CWD Discipline**: When already in this worktree, use plain `git` commands. Use `git -C <path>` only when intentionally targeting another repo/path.
5. **Branch Workflow**: Use local-only git flow by default. Do not run remote sync/cleanup commands (for example pull/rebase, push, remote prune) unless explicitly requested.
6. **Safe File Operations**: Never delete untracked files or run `git restore` without explicit permission.
7. **Commit Before Done**: Always commit all changes before saying "done" or "complete".

## Go/Bubbletea Practices

- Accept interfaces and return concrete structs to keep dependencies mockable.
- Pass `context.Context` as the first argument for I/O and long-running operations.
- Use idiomatic error wrapping: `fmt.Errorf("operation failed: %w", err)`.
- Keep Bubbletea models focused: top-level router + nested models with shared common state.
- Write table-driven tests in `*_test.go` near changed code.

## Quick Commands

```bash
# Development
cd go-bubbletea
make build
make test
make run

# Search (modern tools)
rg "pattern" --type go
fd "filename" -t f

# Issue tracking
az prime
az issue --help
```

## Architecture Quick Reference

```
go-bubbletea/
├── cmd/              # App entrypoints
├── internal/         # App logic, services, domain, UI
├── docs/             # Documentation
├── Makefile          # Build/test/run commands
└── go.mod            # Module definition
```

## Issue Tracking Workflow

- Track any task that takes more than one command in `az issue`.
- Keep one active parent issue per session whenever possible.
- If subagents are used, each subagent must create, maintain, and close a child issue linked to the active parent issue.
- Keep issue status updated as work progresses.

## Landing the Plane (Session Completion)

1. File issues for remaining work.
2. Run quality gates if code changed.
3. Update issue status.
4. Run `git status`.
5. Clean temporary local state.
6. Verify all changes are committed locally.
7. Hand off context for the next session.

</ai_context>
