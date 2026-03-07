---
name: workflow-rulesync-context
description: Comprehensive playbook for managing this repo's AI agent context via RuleSync, including canonical config, nested AGENTS overlays, safe change workflows, and validation/troubleshooting.
allowed-tools: Bash
---

# RuleSync Context Playbook

Use this skill for any change that affects AI context generation in this repo:
- `rulesync.jsonc`
- `.rulesync/rules/*`
- `.rulesync/skills/*`, `.rulesync/subagents/*`
- sync hooks / `just` RuleSync commands
- generated context expectations (`AGENTS.md`, nested AGENTS)

## Goals

1. Keep one canonical RuleSync model.
2. Avoid duplicated context across root and subproject overlays.
3. Ensure nested AGENTS are path-scoped and stable.
4. Avoid generating `.opencode/memories` for rule content.

## Canonical Architecture

### Config model (`rulesync.jsonc`)

```jsonc
{
  "targets": ["agentsmd", "codexcli", "opencode"],
  "features": {
    "agentsmd": ["rules"],
    "codexcli": ["subagents", "skills"],
    "opencode": ["subagents", "skills"]
  },
  "baseDirs": ["."],
  "delete": true,
  "silent": true
}
```

Rules:
- `rules` must stay on `agentsmd` only.
- `opencode` and `codexcli` are for `skills`/`subagents` in this repo model.
- Do not add a second RuleSync config file for TUI/subproject splits.

### Source-of-truth layout

- Shared context policy: `.rulesync/rules/root-context.md`
- Nested overlays:
  - `.rulesync/rules/ts-opentui.md`
  - `.rulesync/rules/go-bubbletea.md`
- Skills source: `.rulesync/skills/*/SKILL.md`
- Subagents source: `.rulesync/subagents/*.md`

Generated outputs are derived artifacts. Edit `.rulesync/*` sources, not generated copies.

### Nested AGENTS placement

Each overlay must include:

```yaml
targets:
  - agentsmd
agentsmd:
  subprojectPath: <subproject-dir>
```

This maps overlays to:
- `ts-opentui/AGENTS.md`
- `go-bubbletea/AGENTS.md`

## Operating Principles

1. Root-first policy ownership:
   - Put shared workflow and guardrails in `root-context.md` once.
   - Keep overlays implementation-specific only.
2. Overlay minimalism:
   - No repeated global policy text.
   - No backward file-loading requirements.
3. Single-pass generation:
   - One command for sync/check.
   - Keep hooks and `just` recipes aligned with same command.
4. Explicit issue hygiene:
   - For non-trivial context plumbing changes, track via `az issue` and include verification in notes.

## Standard Workflows

### A) Change shared policy/rules

Use when a rule should apply repo-wide.

1. Edit `.rulesync/rules/root-context.md`.
2. Do not copy the same change into ts/go overlays unless behavior is implementation-specific.
3. Regenerate and validate (see Validation section).

### B) Change ts/go-specific guidance

Use when a rule applies to one implementation only.

1. Edit one overlay file only (`ts-opentui.md` or `go-bubbletea.md`).
2. Keep shared language out of overlay.
3. Regenerate and validate.

### C) Add a new nested subproject context

1. Add `.rulesync/rules/<new-subproject>.md` with `targets: [agentsmd]`.
2. Set `agentsmd.subprojectPath: <new-subproject-dir>`.
3. Keep file as overlay-only guidance.
4. Regenerate and verify `<new-subproject>/AGENTS.md` exists.

### D) Add/update a reusable skill

1. Create or edit `.rulesync/skills/<skill-name>/SKILL.md`.
2. Keep skill task-focused and durable (not turn-specific).
3. Regenerate and verify presence in:
   - `.codex/skills/<skill-name>/SKILL.md`
   - `.opencode/skill/<skill-name>/SKILL.md`

### E) Update RuleSync execution points

If changing sync behavior, update all three together:
- `justfile` (`rulesync-sync`, `rulesync-check`)
- `.githooks/post-checkout`
- `.githooks/post-merge`

The repo default is single-pass:
- `rulesync generate -c rulesync.jsonc --silent`

## Validation Matrix

Run after any RuleSync context change:

```bash
rulesync generate -c rulesync.jsonc --silent
just rulesync-check
```

Verify expected AGENTS outputs:

```bash
test -f AGENTS.md
test -f ts-opentui/AGENTS.md
test -f go-bubbletea/AGENTS.md
```

Optional leakage checks:

```bash
find .opencode/memories -type f 2>/dev/null
find .agents/memories -type f 2>/dev/null
```

Expected for current model:
- no rule-generated files required under `.opencode/memories`

## Troubleshooting

### Symptom: `.opencode/memories` rule files appear

Cause:
- `rules` got enabled for `opencode` (or wildcard feature setup).

Fix:
1. Restore per-target features with `rules` under `agentsmd` only.
2. Regenerate.
3. Re-run `just rulesync-check`.

### Symptom: one policy fix requires editing multiple rule files

Cause:
- Shared policy duplicated across overlays.

Fix:
1. Move shared text to `root-context.md`.
2. Keep overlays implementation-specific.
3. Regenerate.

### Symptom: nested AGENTS missing

Cause:
- missing `targets: [agentsmd]` or missing `agentsmd.subprojectPath` in overlay frontmatter.

Fix:
1. Correct frontmatter.
2. Regenerate and verify target file presence.

### Symptom: generation behavior differs between manual and hooks

Cause:
- hooks/justfile drift.

Fix:
1. Align hooks and `just` targets to the canonical single command.
2. Re-run checks.

## Anti-patterns to Avoid

1. Multiple RuleSync config files for local subflows.
2. Two-pass sync for this repo model.
3. Editing generated AGENTS directly.
4. Adding heavy global policy text to overlays.
5. Encoding ephemeral turn context as permanent skill guidance.

## Change Logging Template

When updating issue notes for RuleSync context plumbing, include:
- files changed in `.rulesync/*`
- command(s) run: `rulesync generate`, `just rulesync-check`
- output verification performed
- confirmation that behavior is context-plumbing only (if no product behavior change)
