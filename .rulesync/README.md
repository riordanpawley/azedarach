# RuleSync

This repository treats RuleSync-native sources as canonical for AI tool context generation.

## Native Sources

`rulesync` is configured via `rulesync.jsonc` with:
- targets: `codexcli`, `opencode`
- features: `rules`, `subagents`, `skills`
- baseDirs: `.`, `ts-opentui`, `go-bubbletea`

Rule sources:
- `./.rulesync/rules/overview.md` (root workspace context)
- `./ts-opentui/.rulesync/rules/overview.md` (ts-opentui context)
- `./go-bubbletea/.rulesync/rules/overview.md` (go-bubbletea context)

These generate path-scoped `AGENTS.md` files at each baseDir and tool-specific
subagent/skill outputs for Codex/OpenCode.

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
