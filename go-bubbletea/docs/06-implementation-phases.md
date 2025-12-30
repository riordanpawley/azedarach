# Implementation Phases

## Overview

The Go/Bubbletea rewrite is divided into 6 phases, each building on the previous. Each phase has its own detailed document with deliverables, acceptance criteria, and implementation notes.

## Phase Documents

| Phase | Focus | Status | Document |
|-------|-------|--------|----------|
| **1** | Core Framework | 🔲 | [phase-1-core.md](phases/phase-1-core.md) |
| **2** | Beads Integration | 🔲 | [phase-2-beads.md](phases/phase-2-beads.md) |
| **3** | Overlays & Filters | 🔲 | [phase-3-overlays.md](phases/phase-3-overlays.md) |
| **4** | Session Management | 🔲 | [phase-4-sessions.md](phases/phase-4-sessions.md) |
| **5** | Git Operations | 🔲 | [phase-5-git.md](phases/phase-5-git.md) |
| **6** | Advanced Features | 🔲 | [phase-6-advanced.md](phases/phase-6-advanced.md) |

**Legend**: 🔲 Not Started | 🟡 In Progress | ✅ Complete

## Dependencies Graph

```
Phase 1 (Core)
    │
    ▼
Phase 2 (Beads)
    │
    ▼
Phase 3 (Overlays) ─────────────────┐
    │                               │
    ▼                               ▼
Phase 4 (Sessions) ──────────► Phase 5 (Git)
    │                               │
    └───────────────┬───────────────┘
                    ▼
              Phase 6 (Advanced)
```

## Milestone Targets

| Milestone | Phases | Description |
|-----------|--------|-------------|
| **Alpha** | 1-3 | Basic navigation, viewing, filtering |
| **Beta** | 1-5 | Full session + git workflow |
| **RC** | 1-6 | Feature parity with TypeScript |
| **GA** | 1-6 | Production ready, Go becomes default |

## Quick Reference

### Phase 1: Core Framework
TEA loop, navigation (hjkl), Catppuccin theme, StatusBar, half-page scroll

### Phase 2: Beads Integration
Domain types, CLI client, cards with badges, toasts, periodic refresh, elapsed timer

### Phase 3: Overlays & Filters
Action/filter/sort menus, search, select mode, goto mode, compact view

### Phase 4: Session Management
tmux, worktrees, state detection, dev servers, cleanup, confirm dialogs

### Phase 5: Git Operations
Merge, PR, diff, conflict resolution, offline mode, network detection

### Phase 6: Advanced Features
Epic drill-down, jump labels, multi-project, images, settings, diagnostics
