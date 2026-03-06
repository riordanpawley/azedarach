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

## Why Both `AGENTS.md` and `CLAUDE.md` Exist

RuleSync can fan out a single source file to multiple targets. In this repo, we currently keep both files in `.rulesync/docs/` by choice:

- `AGENTS.md` is the OpenCode/Codex-facing entrypoint.
- `CLAUDE.md` is the Claude Code-facing entrypoint.

The core workflow policy is shared, but each file has tool-specific framing and metadata (for example the `<ai_context ... tool=\"...\">` header and tool-oriented guidance), and we allow them to evolve independently when needed. If we decide that divergence is no longer useful, `mappings.tsv` can be collapsed to a single shared source.

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
