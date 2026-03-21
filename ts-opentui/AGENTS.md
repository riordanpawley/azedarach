<!--
File: CONTEXT.md
Version: 2.4.0
Updated: 2026-03-07
Purpose: ts-opentui overlay context synced to nested AGENTS.md
-->

<ai_context version="1.0" tool="shared">

# Azedarach ts-opentui Overlay

> TypeScript/OpenTUI implementation-specific guidance for `ts-opentui/`

## Shared Baseline

Shared repository workflow and policy rules are already loaded before this overlay and apply here:
- issue tracking (`az prime`, `az issue`)
- git workflow and safety constraints
- completion/commit discipline
- cross-repo Codex context workflow

This file is intentionally an overlay with ts-opentui-specific rules only.

## Critical ts-opentui Rules

1. **Type Safety**: Always use TypeScript strict mode. Never use `as` casting or `any`.
2. **Effect Service Boundaries**:
   - Service modules export only service-facing surface: `API` type, service tag/class, typed errors/types, and `Default` layer.
   - Do not create or re-export top-level Effect-returning helpers in service modules.
   - Do not call `Effect.provide` / `Effect.provideService` inside service methods.
   - Acquire dependencies at layer construction, then use concrete captured values directly in methods.
   - Consumers must obtain services via `yield* ServiceTag` and call methods on that service instance.
   - Compose layers in runtime entrypoints and test harnesses, not ad-hoc inside domain logic.
   - Keep `Effect.runPromise`/`Effect.runSync` at runtime entrypoints and tests only.
   - Enforce the current repo boundary gate set with `bun run type-check` and `bun run check:boundaries` before closing work.
   - There is no standalone `guard:effect-boundaries` script in the current repo state; if a real guard returns, update this file and `package.json` together.
3. **No `node:*` Imports**:
   - Use `@effect/platform` alternatives (`Path`, `Command`, etc.).
   - Use `crypto.randomUUID()` and `process.env.HOME` where applicable.
4. **effect-atom Role**:
   - Core state and business logic belong in Effect services (`SubscriptionRef`/`Ref`).
   - Atoms are a bridge/derivation layer for React consumption.
5. **OpenTUI Text Rules**:
   - Never nest `<text>` inside `<text>`.
   - Use `<span>` for inline styling inside `<text>`.
6. **Serialization Rules**:
   - Use `Schema.encode()` / `Schema.decode()`.
   - Do not hand-roll JSON conversion or manual optional/null transforms.
7. **tmux Session Startup**:
   - Use interactive shell form `${shell} -i -c '<cmd>; exec ${shell}'` to ensure direnv/env loading.
8. **Spec Sync Discipline**:
   - For behavior changes in `ts-opentui`, inspect relevant linked `az spec` requirements before implementation.
   - Update `az spec` requirement/link records in the same task when behavior scope changes.
   - If no spec record updates are needed, record `Spec impact: none` with file-specific rationale in issue notes.
9. **Completion/Closure Discipline (ts-opentui)**:
   - Do not mark work complete, close child issues, or close parent issues until evidence is captured in `az issue` notes.
   - Required evidence for closure: exact commands run, pass/fail status, and any manual verification results relevant to changed surfaces.
   - If any required validation is skipped, keep the issue open and record the explicit blocker.
10. **Manual Runtime Verification Requires Fresh Daemon**:
   - Before any manual verification that hits daemon-backed flows (for example `bun run dev`, `bun run dev issue ...`, board/TUI checks), restart daemon from current code first: `bun run dev daemon restart --force`.
   - Treat manual checks as invalid if the daemon was not refreshed from the current branch/worktree immediately beforehand.
11. **Issue Source-of-Truth vs Sync Provider**:
   - Local sqlite state is the read source-of-truth for daemon, CLI, and TUI issue/board rendering.
   - `linear` configuration is a sync transport mode, not a board/list read backend selector.
   - Do not gate local issue reads on sync-provider type; provider choice affects sync behavior only.

## Quick Commands

```bash
# in ts-opentui/
cd ts-opentui

bun run dev
bun run type-check
bun run build
bun run check:boundaries

# focused search
rg "pattern" --type ts src docs
fd "filename" -t f src docs
```

## Architecture Quick Reference

```text
ts-opentui/
├── packages/
│   ├── shared/         # RPC contracts/client primitives only
│   ├── daemon-control/ # Lifecycle service contract only
│   ├── daemon/         # Live daemon lifecycle/discovery implementation
│   ├── cli/            # CLI runtime/commands (depends on daemon-control contract)
│   ├── tui/            # TUI runtime/launch (depends on daemon-control contract)
│   └── entry/          # Runtime composition + mode routing for az
├── src/
│   ├── core/           # Remaining app-core modules not yet package-owned
│   ├── services/       # Remaining app services not yet package-owned
│   ├── runtime/        # Facades used to avoid package->legacy src/core|services imports
│   └── config/         # config + schema
└── docs/spec/          # behavior specification
```

## Boundary Contributor Guide

- Keep `@azedarach/shared` RPC-only; import shared contracts from `@azedarach/shared/rpc`.
- Keep daemon lifecycle contracts in `@azedarach/daemon-control` and live implementation in `@azedarach/daemon`.
- Do not import `@azedarach/daemon` from `packages/cli` or `packages/tui`.
- Do not add package imports to legacy `src/cli`, `src/core`, `src/daemon`, `src/rpc`, or `src/services`.
- Compose runtime layers in `packages/entry` (CLI + TUI launch paths), not in package internals.

## Skills

Skills are task-scoped references, not mandatory bootstrap. Load only the specific skill(s) needed for the current task.

Useful local skills:
- `.codex/skills/effect-services/SKILL.md`
- `.codex/skills/effect-errors/SKILL.md`
- `.codex/skills/effect-concurrency/SKILL.md`
- `.codex/skills/effect-resources/SKILL.md`

## Quick Help

- ts-opentui behavior spec: `docs/spec/`
- user docs: `docs/README.md`

</ai_context>
