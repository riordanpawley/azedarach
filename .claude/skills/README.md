# Claude Code Skills System

**Version:** 1.0
**Project:** Azedarach

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
├── workflow/                    # Workflow skills
│   └── beads-tracking.skill.md  # Issue tracking workflow
│
└── resources/                   # Progressive disclosure (detailed docs)
    └── beads/
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
- **beads-tracking** - Issue tracking, resumability, multi-session work

## Configuration

### skill-rules.json

Each skill defined with:

```json
{
  "id": "beads-tracking",
  "name": "Beads Issue Tracking",
  "path": ".claude/skills/workflow/beads-tracking.skill.md",
  "type": "workflow",
  "priority": "high",
  "confidence": {
    "required": 0.60,
    "suggested": 0.45
  },
  "triggers": {
    "filePatterns": [".beads/**"],
    "contentPatterns": ["bd create", "bd update", "bd close"],
    "keywords": ["beads", "issue", "task", "tracking", "bd"]
  },
  "resources": [
    ".claude/skills/resources/beads/workflows.md",
    ".claude/skills/resources/beads/worktree-integration.md"
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
