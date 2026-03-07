---
name: workflow-rulesync-context
description: Maintain RuleSync nested AGENTS context generation for this repo using the canonical single-config model (agentsmd rules only, codex/opencode skills+subagents, no opencode memories).
allowed-tools: Bash
---

# RuleSync Context Workflow

Use this skill when changing AI context generation in this repo (`rulesync.jsonc`, `.rulesync/rules/*`, hooks, or `just` sync commands).

## Canonical Model

- One config file: `rulesync.jsonc`
- One sync command: `rulesync generate -c rulesync.jsonc --silent`
- Nested AGENTS generation comes from `agentsmd` + `rules`
- `opencode` and `codexcli` are used for `skills`/`subagents`, not `rules`
- Do not introduce a second RuleSync config for TUI/project splitting

## Required Config Shape

`rulesync.jsonc` must keep this intent:

```jsonc
{
  "targets": ["agentsmd", "codexcli", "opencode"],
  "features": {
    "agentsmd": ["rules"],
    "codexcli": ["subagents", "skills"],
    "opencode": ["subagents", "skills"]
  }
}
```

Why:
- `agentsmd` rules generate `AGENTS.md` files (root + nested)
- keeping `rules` off `opencode` prevents `.opencode/memories` rule output

## Nested Context Source Rules

- Edit canonical context only in `.rulesync/rules/*.md`
- Root shared policy belongs in `.rulesync/rules/overview.md`
- Subproject files are overlays only:
  - `.rulesync/rules/ts-opentui.md`
  - `.rulesync/rules/go-bubbletea.md`
- Overlays should avoid duplicating shared workflow policy from root
- Overlays should not add backward-path file-loading requirements

For nested AGENTS file placement, each overlay must include:

```yaml
targets:
  - agentsmd
agentsmd:
  subprojectPath: <subproject-dir>
```

## Execution and Validation

After any RuleSync source/config change:

```bash
rulesync generate -c rulesync.jsonc --silent
just rulesync-check
```

Then verify expected files:

```bash
test -f AGENTS.md
test -f ts-opentui/AGENTS.md
test -f go-bubbletea/AGENTS.md
```

Optional sanity check for rules leakage:

```bash
find .opencode/memories -type f 2>/dev/null
```

Expected: no rule-generated files for this workflow.

## Common Mistakes

1. **Enabling `rules` for `opencode`**
   - Symptom: `.opencode/memories` files appear
   - Fix: keep `rules` only under `agentsmd`
2. **Two-pass sync commands**
   - Symptom: duplicate/competing generation flow
   - Fix: use only `rulesync generate -c rulesync.jsonc --silent`
3. **Shared policy duplicated in overlays**
   - Symptom: one policy tweak requires editing multiple files
   - Fix: keep root policy in `overview.md`; overlays stay implementation-specific

## Change Discipline

- Treat generated outputs as derived; edit `.rulesync/*` sources
- Keep issue notes explicit about RuleSync behavior changes
- If the change affects only context plumbing, note that no product behavior changed
