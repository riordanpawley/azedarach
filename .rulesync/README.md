# RuleSync

This repository treats `.rulesync/` as the canonical source for selected Claude-facing workflow assets.

## Managed Mappings

Mappings are declared in `.rulesync/mappings.tsv` and synced to runtime locations by the `rulesync` CLI.

Current managed targets:
- `.claude/agents/`
- `.claude/commands/`
- `.claude/hooks/`
- `.claude/session-templates/`
- `.claude/skills/`
- `AGENTS.md`
- `CLAUDE.md`
- `ts-opentui/AGENTS.md`
- `ts-opentui/CLAUDE.md`
- `go-bubbletea/CLAUDE.md`

## Commands

```bash
just rulesync-sync
just rulesync-check
```

## Auto Sync Hooks

Git hooks run sync automatically:
- `.githooks/post-checkout`
- `.githooks/post-merge`

Set `RULESYNC_SKIP=1` to bypass auto-sync when needed.
