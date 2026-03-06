<!--
File: CONTEXT.md
Version: 1.0.0
Updated: 2026-03-07
Purpose: Canonical ts-opentui AI context source synced to nested entrypoints
-->

<ai_context version="1.0" tool="shared">

# Azedarach Context - ts-opentui

> TypeScript/OpenTUI implementation context for Azedarach

## Entrypoint Generation

This file is the canonical ts-opentui context source in `.rulesync/docs/ts-opentui/`.

RuleSync mapping fans this one file out to:
- `ts-opentui/AGENTS.md` (OpenCode/Codex entrypoint filename)
- `ts-opentui/CLAUDE.md` (Claude Code entrypoint filename)

## Critical Rules

1. **Type Safety**: ALWAYS use TypeScript strict mode. NEVER use `as` casting or `any`.
2. **Modern CLI Tools**: Use `rg` (not grep), `fd` (not find), `sd` (not sed), and `bat` (not cat).
3. **Issue Tracking**: Start with `az prime`, then use `az issue` for tracked work.
4. **Spec Sync Required**: For `ts-opentui` behavior changes, keep `docs/spec/` aligned in the same task, or log `Spec impact: none` with file-specific rationale in issue notes.
5. **Spec Skill**: Use `.claude/skills/workflow-spec-maintenance/SKILL.md` for spec-sync analysis and validation.
6. **Git CWD Discipline**: When already in this worktree, use plain `git` commands. Use `git -C <path>` only when intentionally targeting another repo/path.
7. **Branch Workflow**: Use local-only git flow by default. Do not run remote sync/cleanup commands (for example pull/rebase, push, remote prune) unless explicitly requested.
8. **Safe File Operations**: Never delete untracked files or run `git restore` without explicit permission.
9. **Commit Before Done**: Always commit all changes before saying "done" or "complete".

## Quick Commands

```bash
# Development
cd ts-opentui
bun run dev

# Quality gates
bun run type-check
bun run build

# Search (modern tools)
rg "pattern" --type ts
fd "filename" -t f

# Issue tracking
az prime
az issue --help
```

## Architecture Quick Reference

```
ts-opentui/
├── src/
│   ├── ui/           # OpenTUI + React components
│   ├── core/         # Effect services
│   ├── services/     # Application services
│   └── config/       # Configuration and schemas
└── docs/spec/        # Product behavior specifications
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
