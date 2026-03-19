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
   - Do not create global Effect-returning helpers with service requirements.
   - Do not call `Effect.provide` / `Effect.provideService` inside service methods.
   - Acquire dependencies at layer construction, then use concrete service values directly.
   - `bun run guard:effect-boundaries` must pass before closing work.
   - If baseline updates are intentional, run `bun run guard:effect-boundaries:update` and record rationale in issue notes.
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

## Quick Commands

```bash
# in ts-opentui/
cd ts-opentui

bun run dev
bun run type-check
bun run build
bun run guard:effect-boundaries

# focused search
rg "pattern" --type ts src docs
fd "filename" -t f src docs
```

## Architecture Quick Reference

```text
ts-opentui/
├── packages/
│   ├── shared/      # Canonical daemon RPC contracts/client
│   ├── daemon/      # Canonical daemon runtime/services
│   ├── tui/         # Canonical TUI runtime/launch
│   └── cli/         # Canonical CLI runtime/commands
├── src/             # Transitional compatibility shims + remaining app modules
├── src/config/      # config + schema
└── docs/spec/       # behavior specification
```

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
