# RuleSync

This repository treats RuleSync-native sources as canonical for AI tool context generation.

## Native Sources

`rulesync` is configured via `rulesync.jsonc` with:
- targets: `codexcli`, `opencode`
- features: `subagents`, `skills`
- baseDirs: `.`

Rule sources:
- `./.rulesync/rules/overview.md` (root workspace context)
- `./.rulesync/rules/ts-opentui.md` (ts-opentui context, `agentsmd.subprojectPath`)
- `./.rulesync/rules/go-bubbletea.md` (go-bubbletea context, `agentsmd.subprojectPath`)

Generation runs in two native RuleSync passes:
- `agentsmd + rules` pass emits path-scoped `AGENTS.md` files (`AGENTS.md`,
  `ts-opentui/AGENTS.md`, `go-bubbletea/AGENTS.md`) using
  `agentsmd.subprojectPath`.
- `codexcli/opencode + subagents/skills` pass emits tool-specific subagent/skill outputs.

## Commands

```bash
rulesync generate -c rulesync.jsonc -t agentsmd -f rules -b . --delete --silent
rulesync generate -c rulesync.jsonc --silent

rulesync generate -c rulesync.jsonc -t agentsmd -f rules -b . --delete --check --silent
rulesync generate -c rulesync.jsonc --check --silent
```

## Auto Sync Hooks

Git hooks run sync automatically:
- `.githooks/post-checkout`
- `.githooks/post-merge`

Set `RULESYNC_SKIP=1` to bypass auto-sync when needed.
