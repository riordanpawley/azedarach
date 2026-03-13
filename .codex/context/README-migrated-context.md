# RuleSync

This repository treats RuleSync-native sources as canonical for AI tool context generation.

## Native Sources

`rulesync` is configured via `rulesync.jsonc` with:
- targets: `agentsmd`, `codexcli`, `opencode`
- features:
  - `agentsmd`: `rules`
  - `codexcli`: `subagents`, `skills`
  - `opencode`: `subagents`, `skills`
- baseDirs: `.`

Rule sources:
- `./.rulesync/rules/root-context.md` (root workspace context)
- `./.rulesync/rules/ts-opentui.md` (ts-opentui context, `agentsmd.subprojectPath`)
- `./.rulesync/rules/go-bubbletea.md` (go-bubbletea context, `agentsmd.subprojectPath`)

Generation runs in one RuleSync pass:
- `agentsmd + rules` emits path-scoped `AGENTS.md` files (`AGENTS.md`,
  `ts-opentui/AGENTS.md`, `go-bubbletea/AGENTS.md`) using
  `agentsmd.subprojectPath`.
- `codexcli/opencode + subagents/skills` emits tool-specific subagent/skill outputs.
- `opencode` rules are intentionally disabled to avoid `.opencode/memories` outputs.

## Commands

```bash
rulesync generate -c rulesync.jsonc --silent

rulesync generate -c rulesync.jsonc --check --silent
```

## Auto Sync Hooks

Git hooks run sync automatically:
- `.githooks/post-checkout`
- `.githooks/post-merge`

Set `RULESYNC_SKIP=1` to bypass auto-sync when needed.
