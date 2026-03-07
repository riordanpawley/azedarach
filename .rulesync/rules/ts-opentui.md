---
targets:
  - agentsmd
description: ts-opentui scoped context
agentsmd:
  subprojectPath: ts-opentui
---
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
- cross-repo RuleSync workflow

This file is intentionally an overlay with ts-opentui-specific rules only.

## Critical ts-opentui Rules

1. **Type Safety**: Always use TypeScript strict mode. Never use `as` casting or `any`.
2. **Effect Service Boundaries**:
   - Do not create global Effect-returning helpers with service requirements.
   - Do not call `Effect.provide` / `Effect.provideService` inside service methods.
   - Acquire dependencies at layer construction, then use concrete service values directly.
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
   - For behavior changes in `ts-opentui`, update `docs/spec/` in the same task, or record `Spec impact: none` with file-specific rationale in issue notes.

## Quick Commands

```bash
# in ts-opentui/
cd ts-opentui

bun run dev
bun run type-check
bun run build

# focused search
rg "pattern" --type ts src docs
fd "filename" -t f src docs
```

## Architecture Quick Reference

```text
ts-opentui/
├── src/ui/          # OpenTUI + React components
├── src/core/        # Effect-backed core services
├── src/services/    # App-level orchestration
├── src/config/      # config + schema
└── docs/spec/       # behavior specification
```

## Skills

Skills are task-scoped references, not mandatory bootstrap. Load only the specific skill(s) needed for the current task.

Useful local skills:
- `.rulesync/skills/effect-services/SKILL.md`
- `.rulesync/skills/effect-errors/SKILL.md`
- `.rulesync/skills/effect-concurrency/SKILL.md`
- `.rulesync/skills/effect-resources/SKILL.md`
- `.rulesync/skills/workflow-spec-maintenance/SKILL.md`

## Quick Help

- ts-opentui behavior spec: `docs/spec/`
- user docs: `docs/README.md`

</ai_context>
