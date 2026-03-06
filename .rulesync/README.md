# RuleSync

This repository treats `.rulesync/` as the canonical source for selected Claude-facing workflow assets.

## Managed Mappings

`rulesync` generates RuleSync-native features from `rulesync.jsonc`:
- `.rulesync/subagents/` -> `.claude/agents/`
- `.rulesync/skills/` -> `.claude/skills/`

Additional passthrough mappings are declared in `.rulesync/mappings.tsv` and synced by `scripts/rulesync-sync.sh`.

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
- `go-bubbletea/AGENTS.md`
- `go-bubbletea/CLAUDE.md`

## Unified Root Context Source

Root entrypoint files are now unified to a single canonical source:

- Source: `.rulesync/docs/CONTEXT.md`
- Targets: `AGENTS.md` and `CLAUDE.md`

`mappings.tsv` fans out this one source to both runtime entrypoint filenames.

## Unified Nested Context Sources

Implementation-specific entrypoints are also unified to canonical nested sources:

- `.rulesync/docs/ts-opentui/CONTEXT.md` -> `ts-opentui/AGENTS.md`, `ts-opentui/CLAUDE.md`
- `.rulesync/docs/go-bubbletea/CONTEXT.md` -> `go-bubbletea/AGENTS.md`, `go-bubbletea/CLAUDE.md`

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
