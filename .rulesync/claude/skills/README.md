# Claude Code Skills

**Version:** 2.0
**Project:** Azedarach

> Canonical source: `.rulesync/claude/skills/`
> Synced runtime destination: `.claude/skills/`

## Overview

This directory contains curated skill documents used by agents during implementation work.

Skills are plain markdown guidance files grouped by domain. They are loaded when explicitly referenced by workflow instructions or user intent; there is no repo-level auto-activation hook.

## Directory Structure

```
.claude/skills/
├── README.md                    # This file
├── effect/                      # Effect/TypeScript skills
│   ├── effect-atom-interactions.skill.md
│   ├── effect-concurrency.skill.md
│   ├── effect-errors.skill.md
│   ├── effect-resources.skill.md
│   ├── effect-services.skill.md
│   └── effect-testing.skill.md
│
├── go/                          # Go/Bubbletea skills
│   ├── go-concurrency.skill.md
│   ├── go-testing.skill.md
│   └── bubbletea-patterns.skill.md
│
├── workflow/                    # Workflow skills
│   ├── azedarach-cli.skill.md
│   ├── linear-tracking.skill.md
│   └── spec-maintenance.skill.md
│
└── resources/                   # Supporting reference docs
    └── linear/
        ├── workflows.md
        └── worktree-integration.md
```

## Skill Categories

### Workflow Skills
- `workflow/linear-tracking.skill.md`: issue tracking and resumability workflow
- `workflow/azedarach-cli.skill.md`: Azedarach CLI usage in worktrees
- `workflow/spec-maintenance.skill.md`: keep `docs/spec/` aligned with behavior changes

### Effect Skills (ts-opentui)
- **effect-atom-interactions** - Move UI interaction orchestration from React into atom actions
- `effect/effect-services.skill.md`
- `effect/effect-errors.skill.md`
- `effect/effect-concurrency.skill.md`
- `effect/effect-resources.skill.md`
- `effect/effect-testing.skill.md`

### Go Skills (go-bubbletea)
- `go/go-testing.skill.md`
- `go/go-concurrency.skill.md`
- `go/bubbletea-patterns.skill.md`

## Maintenance

When adding or updating skills:
- edit canonical files under `.rulesync/claude/skills/`
- sync managed outputs to `.claude/skills/`
- keep this README aligned with the actual folder contents
