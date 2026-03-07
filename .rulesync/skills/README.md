# RuleSync Skills

This directory is the canonical RuleSync skill source.

- Source format: one directory per skill with `SKILL.md`
- Generated target for Claude Code: `.claude/skills/<skill-name>/SKILL.md`

## Scope

This tree now contains:
- Linear/OpenCode workflow skills (`linear-*`)
- Migrated Claude Code domain skills (`effect-*`, `go-*`, `workflow-*`, `gleam-*`)
- RuleSync context maintenance playbook (`workflow-rulesync-context`)
- Effect schema codec guidance in `effect-schema`

## Migration Note

Legacy Claude skill files under `.rulesync/claude/skills/*.skill.md` were migrated into this RuleSync-native structure so all skills are defined in one place.
