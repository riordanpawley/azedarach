# Claude Code Skills System

**Version:** 1.0
**Project:** Azedarach

> Canonical source: `.rulesync/claude/skills/`
> Synced runtime destination: `.claude/skills/` via `bin/rulesync`

## Overview

This directory implements an **auto-activating skills system** that combines:

1. **Pattern-based activation** - File paths and content patterns trigger relevant skills
2. **AI-driven detection** - Claude Haiku analyzes prompts for intelligent skill matching
3. **Prompt clarity checking** - Prevents vague prompts with targeted questions
4. **Progressive disclosure** - Main skills <500 lines, detailed resources loaded on demand
5. **Session awareness** - Tracks loaded skills to prevent duplicates

## Architecture

### Directory Structure

```
.claude/skills/
├── README.md                    # This file
├── skill-rules.json             # Activation rules and configuration
│
├── effect/                      # Effect/TypeScript skills
│   ├── effect-atom-interactions.skill.md
│   ├── effect-concurrency.skill.md
│   ├── effect-errors.skill.md
│   ├── effect-resources.skill.md
│   ├── effect-services.skill.md
│   └── effect-testing.skill.md  # TDD for TypeScript/Effect
│
├── go/                          # Go/Bubbletea skills
│   ├── go-concurrency.skill.md
│   ├── go-testing.skill.md      # TDD for Go
│   └── bubbletea-patterns.skill.md
│
├── workflow/                    # Workflow skills
│   └── linear-tracking.skill.md  # Issue tracking workflow
│
└── resources/                   # Progressive disclosure (detailed docs)
    └── linear/
        ├── workflows.md         # Detailed workflow patterns
        └── worktree-integration.md  # Git worktree patterns
```

### Activation Flow

1. **User submits prompt** → UserPromptSubmit hook intercepts
2. **Pattern matching** → Check file paths and content patterns in skill-rules.json
3. **AI analysis** (optional) → Claude Haiku scores skill relevance
4. **Confidence scoring**:
   - >= 0.70: Auto-load skill
   - 0.50-0.69: Suggest to user
   - < 0.50: Skip
5. **Session deduplication** → Don't load skills already active
6. **Skill injection** → Load skill content into conversation context

### Hooks

**user-prompt-submit-orchestrator.cjs**
- Intercepts all user prompts
- Runs prompt clarity check
- Performs pattern matching (file, content, keyword, anti-pattern)
- Executes AI skill detection
- Manages skill loading

## Skill Types

### Workflow Skills
Development workflow and process patterns:
- **linear-tracking** - Issue tracking, resumability, multi-session work

### Effect Skills (ts-opentui only)
Effect framework and TypeScript patterns:
- **effect-atom-interactions** - Move UI interaction orchestration from React into atom actions
- **effect-concurrency** - Fibers, forking, scheduling
- **effect-errors** - Error handling patterns
- **effect-resources** - Resource management
- **effect-services** - Service construction patterns
- **effect-testing** - TDD for TypeScript/Effect with Bun

### Go Skills (go-bubbletea only)
Go/Bubbletea patterns:
- **go-concurrency** - Goroutines, channels, context
- **go-testing** - TDD for Go with table-driven tests
- **bubbletea-patterns** - TEA architecture, Model-Update-View

## Configuration

### skill-rules.json

Each skill defined with:

```json
{
  "id": "linear-tracking",
  "name": "Linear Issue Tracking",
  "path": ".claude/skills/workflow/linear-tracking.skill.md",
  "type": "workflow",
  "priority": "high",
  "confidence": {
    "required": 0.60,
    "suggested": 0.45
  },
  "triggers": {
    "filePatterns": [".azedarach.json", "ts-opentui/src/core/BeadsClient.ts"],
    "contentPatterns": ["linear-cli i create", "linear-cli i update", "linear-cli i close"],
    "keywords": ["linear", "issue", "task", "tracking", "linear-cli"]
  },
  "resources": [
    ".claude/skills/resources/linear/workflows.md",
    ".claude/skills/resources/linear/worktree-integration.md"
  ]
}
```

## Bypass Prefixes

Skip clarity check using:
- `*` - "Just do it" (skip all checks)
- `/` - Slash command (skip checks)
- `#` - Context only (add to memory, no action)

## References

### Source Repositories

1. **Auto-Activating Skills:** [diet103/claude-code-infrastructure-showcase](https://github.com/diet103/claude-code-infrastructure-showcase)
2. **Prompt Clarity:** [severity1/claude-code-prompt-improver](https://github.com/severity1/claude-code-prompt-improver)
3. **AI Detection:** [jefflester/claude-skills-supercharged](https://github.com/jefflester/claude-skills-supercharged)
