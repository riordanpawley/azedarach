Please also reference the following rules as needed. The list below is provided in TOON format, and `@` stands for the project root directory.

rules[2]{path,description}:
  @go-bubbletea/AGENTS.md,go-bubbletea scoped context
  @ts-opentui/AGENTS.md,ts-opentui scoped context

# Additional Conventions Beyond the Built-in Functions

As this project's AI coding tool, you must follow the additional conventions below, in addition to the built-in functions.

<!--
File: CONTEXT.md
Version: 2.0.1
Updated: 2026-03-07
Purpose: Canonical root AI context source synced to AGENTS.md entrypoints
-->

<ai_context version="1.0" tool="shared">

# Azedarach Project Context

> TUI Kanban board for orchestrating parallel AI sessions with issue tracking

## Entrypoint Generation

This file is the canonical root context source for Codex-native configuration.

Nested `AGENTS.md` overlays are maintained directly in-repo for path-scoped guidance.

## Instructions Reference

**This repository has multiple implementations:**

- **ts-opentui/** -> [AGENTS.md](./ts-opentui/AGENTS.md) (TypeScript, Bun, OpenTUI, Effect)
- **go-bubbletea/** -> [AGENTS.md](./go-bubbletea/AGENTS.md) (Go, Bubbletea)

Select the implementation based on user request or current working directory.

## Critical Rules (Quick Reference)

1. **Type Safety**: ALWAYS use TypeScript strict mode. NEVER use `as` casting or `any` (ts-opentui only).
2. **Modern CLI Tools**: Use `rg` (not grep), `fd` (not find), `sd` (not sed), and `bat` (not cat).
3. **Issue Tracker**: Start sessions with `az prime`, then use `az issue` for all tracked issue operations.
4. **Commit Before Done**: Always commit all changes before saying "done" or "complete".
5. **Codex Canonical Source**: Edit live instruction assets directly (`AGENTS.md`, nested `AGENTS.md`, `.codex/skills/*`, `.codex/subagents/*`).
6. **Git CWD Discipline**: When already in the target worktree/repo, use plain `git` commands. Use `git -C <path>` only when intentionally targeting a different path.
7. **Branch Workflow**: Use local-only git flow by default. Do not run remote sync/cleanup commands (for example pull/rebase, push, remote prune) unless explicitly requested.
8. **Spec Sync Discipline (ts-opentui)**: Keep `az spec` requirements/links aligned with `ts-opentui` behavior improvements in the same task, or log `Spec impact: none` with file-specific rationale in issue notes.
9. **Safe File Operations**: Never delete untracked files or run `git restore` without explicit permission.
10. **No Message Parsing for Logic Gates**: Never gate behavior by parsing free-form error/message text. Use typed/tagged errors (for example `Data.TaggedError`) and `_tag`-based control flow.
11. **ts-opentui Boundary Guard**: For `ts-opentui`, enforce Effect boundary hygiene with `bun run type-check` and `bun run check:boundaries`; there is no standalone `guard:effect-boundaries` script in the current repo state.
12. **ts-opentui Service Architecture Contract**:
    - Service modules must export only service surface (`API`, service tag/class, typed errors/types, `Default` layer).
    - Do not export top-level effectful helper functions from service modules.
    - Capture infra dependencies at service construction (`effect`/`scoped`), then close over concrete values in methods.
    - Keep `Effect.run*` at runtime entrypoints and test harnesses only.
13. **Typed Error Policy (ts-opentui)**:
    - In non-test app code, runtime failures must be strongly typed/tagged classes (`Data.TaggedError` or `Schema.TaggedClass`), not raw JS `Error`.
    - Prefer `Data.TaggedError` for internal runtime/service errors; use `Schema.TaggedClass` at schema/codec boundaries.
    - Do not use `new Error`, `throw new Error`, `Effect.fail(new Error(...))`, `instanceof Error`, or `String(error)` in non-test app code.
13. **Issue Sync Naming Contract (ts-opentui)**:
    - Use `issueSyncBackend` for configurable issue synchronization behavior (`linear`, `tracker`, etc.).
    - Local sqlite issue data remains the read source-of-truth for daemon, CLI, and TUI board/list rendering.
    - Do not use sync backend choice as a read source selector.

## Quick Commands

```bash
# ts-opentui (TypeScript/Bun)
cd ts-opentui
bun run dev                       # Start development TUI
bun run type-check                # Full project check
bun run build                     # Build the project
bun run check:boundaries          # Enforce current package boundary rules

# go-bubbletea (Go)
cd go-bubbletea
just build                        # Build Go binary
just test                         # Run tests
just run                          # Build and run

# Search (modern tools)
rg "pattern" --type ts            # Search content (NOT grep)
fd "filename" -t f                # Find files (NOT find)

# Issue Tracking
az prime                          # Session primer + AI workflow guide
az impl list                      # Show project implementations/default
az issue create "Title" --impl ts-opentui
az issue create "Shared task" --impl ts-opentui --impl go-bubbletea

# Codex Context
fd . .codex/skills -td            # List installed local skills
fd . .codex/subagents -tf         # List local subagent prompts
```

## Codex Native Context Workflow

Canonical live paths:
- `AGENTS.md`, `ts-opentui/AGENTS.md`, `go-bubbletea/AGENTS.md` (directly maintained overlays)
- `.codex/skills/*` (Codex skills)
- `.codex/subagents/*` (Codex subagent prompts)
- `.codex/rules/*` and `.codex/config.toml` (Codex configuration)

Legacy migration snapshot (read-only reference):
- `.codex/context/rules/*`
- `.codex/context/docs/*`
- `.codex/context/README-migrated-context.md`

Sync behavior:
- There is no generation step in the active workflow.
- Edit Codex-native files directly and keep root + nested AGENTS aligned in the same change.
- OpenCode plugin files remain intentionally unmanaged by Codex context files.

## Architecture Quick Reference

```
ts-opentui/
├── packages/
│   ├── shared/         # shared contracts + package-safe runtime utilities
│   ├── daemon-control/ # lifecycle contract only
│   ├── daemon/         # live daemon implementation
│   ├── cli/            # CLI runtime
│   ├── tui/            # TUI runtime
│   └── entry/          # az runtime composition/router
├── src/
│   ├── core/           # remaining app-core modules
│   ├── services/       # remaining app services
│   ├── runtime/        # package-safe runtime facades
│   └── config/         # Configuration and schemas

go-bubbletea/
├── cmd/              # Main applications (minimal wiring)
├── internal/         # Private code (app, services, types, ui)
│   ├── app/          # Bubbletea application logic
│   ├── services/     # Business logic (Linear, Tmux, Git)
│   ├── types/        # Domain models
│   └── ui/           # Bubbletea UI components
└── docs/             # Documentation
```

**Stacks:**
- **ts-opentui**: TypeScript, OpenTUI + React, Effect, tmux, az issue tracker
- **go-bubbletea**: Go, Bubbletea, Lip Gloss, tmux, az issue tracker

## Decision Matrix

When user requests work, use this matrix to decide which implementation to work on:

| Request | Implementation | Rationale |
|---------|---------------|------------|
| Default / unspecified | ts-opentui/ | Primary, most mature |
| "TypeScript", "Bun", "Effect" | ts-opentui/ | Tech-specific match |
| "Go", "Bubbletea" | go-bubbletea/ | Tech-specific match |
| "Gleam", "Erlang", "BEAM" | gleam/ | Experimental match |
| Explicit app folder mentioned | That folder | User-specified |

## Implementation Registry

- This project registers `ts-opentui` and `go-bubbletea` in `az impl`.
- Treat `ts-opentui` as the project default implementation when a single default is needed.
- Once multiple implementations are configured, new `az issue` and `az spec link` writes MUST include one or more explicit `--impl <impl>` selections.
- Repeat `--impl` flags only for intentionally shared work spanning both implementations.

## Task Management

**Track all non-trivial work through issue tracking** (preserves context across sessions).

- Run `az prime` at the start of each AI session.
- Use `az issue` for all issue updates and transitions.
- Any task expected to take more than one command MUST be tracked in the issue tracker.
- Keep one active parent issue per session whenever possible.
- If subagents are used, each subagent MUST create and maintain a child issue under the active parent issue, and close it when that subagent task is done.

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
- ✅ For `ts-opentui` behavior changes, update `az spec` requirement/link records or document `Spec impact: none` with concrete file-based rationale
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

## Shared Skills

This repository has shared skills in `.codex/skills/` that apply to all implementations:

- **Skill Loading Policy**: Skills are task-scoped references, not mandatory bootstrap. Only load a skill when the current task explicitly needs it.
- **Workflow Skills** (`workflow/`): issue tracking, Azedarach CLI workflows, and spec maintenance
- **Effect Skills** (`effect/`): Effect patterns (ts-opentui only)
- **Gleam Skills** (`gleam/`): Gleam patterns (gleam only)

See `.codex/skills/` for available skill docs.

## OpenCode Plugins

This project uses two OpenCode plugins:

1. **opencode-pty** - PTY integration
2. **.opencode/plugin/azedarach.js** - Session status monitoring for TUI

Both are configured in `opencode.json`.

</ai_context>
