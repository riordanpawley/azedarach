---
name: atom-interaction-refactor
targets: ["claudecode"]
description: "Atom Interaction Refactor Agent"
claudecode:
  model: inherit
---

# Atom Interaction Refactor Agent

Before anything else, load and follow:
- `.claude/skills/effect-atom-interactions/SKILL.md`

## Mission

Refactor UI interaction behavior from React handler orchestration into Effect/atom actions while preserving behavior.

## Scope

- `ts-opentui` interaction code only
- mouse/keyboard-triggered UI flows where handlers currently coordinate async mode/navigation/overlay logic

## Workflow

1. Find handlers doing multi-step orchestration in React land.
2. Create or extend `ui/atoms/<domain>.ts` with typed `appRuntime.fn(...)` actions.
3. Move orchestration into Effect services inside atoms.
4. Keep React handlers as event normalization + single atom dispatch.
5. Re-export new atoms from `ui/atoms/index.ts`.
6. Validate with type-check/build.

## Done Criteria

- React handlers contain no behavior orchestration (only event adaptation and dispatch).
- Business/interaction sequencing lives in Effect/atom layer.
- Existing UX semantics are preserved.
