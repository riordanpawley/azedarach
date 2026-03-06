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

We keep both files in `.rulesync/docs/` because different AI runtimes bootstrap from different entrypoint filenames:

- `AGENTS.md` is the OpenCode/Codex-facing entrypoint.
- `CLAUDE.md` is the Claude Code-facing entrypoint.

The core workflow policy is shared, but each file has tool-specific framing and metadata (for example the `<ai_context ... tool=\"...\">` header and tool-oriented guidance). Keeping both in `.rulesync/` gives us one canonical authoring location while still generating the runtime file each tool expects.

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
