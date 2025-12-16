# Azedarach vs AutoMaker Comparison

> Competitive analysis comparing Azedarach with [AutoMaker](https://github.com/AutoMaker-Org/automaker)
>
> Date: 2025-12-16

## Overview

| Aspect | Azedarach | AutoMaker |
|--------|-----------|-----------|
| **Type** | TUI (Terminal UI) | Desktop/Web GUI (Electron + Next.js) |
| **State Mgmt** | Effect + effect-atom | Zustand |
| **UI Framework** | OpenTUI + React | Next.js + Tailwind + dnd-kit |
| **Target Users** | Developers in terminal | Developers preferring GUI |
| **License** | Open source | Proprietary (restrictive) |

## What AutoMaker Does

AutoMaker is an autonomous AI development studio that automates software feature implementation. Users describe features on a Kanban board, and AI agents powered by Claude automatically implement them.

**Core Workflow:**
1. Add feature descriptions to a backlog
2. Move tasks to "In Progress" to trigger automatic AI agent assignment
3. Monitor real-time implementation progress
4. Review and verify completed work
5. Deploy verified features

---

## Features AutoMaker Has That We Could Consider

### 1. AI Profiles / Presets ⭐⭐

AutoMaker has customizable AI profiles with model selection and "thinking levels":
- "Heavy Task" (Opus + ultrathink)
- "Balanced" (Sonnet + medium)
- "Quick Edit" (Haiku + none)

Users can create custom profiles with specific model and configuration combinations.

**Inspiration for az:** Could add a profile system to quickly switch Claude configurations per task type. Store in config or as part of bead metadata.

### 2. Concurrent Task Limiting ⭐

AutoMaker has `maxConcurrency` setting to limit how many features can run in parallel. The `useAutoMode` hook enforces this limit.

```typescript
canStartNewTask: runningTasks.length < maxConcurrency
```

**Inspiration for az:** Currently we track running sessions, but a configurable concurrency limit could help manage resources and prevent overload.

### 3. Project Analysis View ⭐

Generates `app_spec.txt` in XML format with:
- Technology stack detection (from package.json, file extensions)
- File structure overview (recursive scan up to 10 levels)
- Feature detection from directory patterns
- Framework identification (React, Next.js, Vue, etc.)

**Inspiration for az:** Could auto-generate project context files for new beads/sessions, reducing manual context setup.

### 4. Context File Management ⭐⭐

Dedicated `.automaker/context` directory with full management UI:
- Text/image file organization
- In-app editing with markdown preview
- Drag-and-drop file import
- Save state tracking ("Save" / "Saving" / "Saved")

**Inspiration for az:** We use CLAUDE.md, but a managed context folder with TUI browsing could enhance context control. Could add a view to browse/edit `.claude/` files.

### 5. Image Attachments to Tasks ⭐⭐

Users can attach screenshots to feature descriptions directly in the UI:
- Drag-and-drop onto input area
- Clipboard paste operations
- Base64 encoded storage

**Inspiration for az:** Could support image attachments in beads, viewable in detail panel. Beads already has attachment support, just need UI.

### 6. Board Backgrounds & Theming

Customizable backgrounds with:
- Custom images
- Opacity controls
- Glassmorphism effects
- 13 theme options (light, dark, retro, dracula, nord, etc.)

**Inspiration for az:** Already have Catppuccin Mocha, but could add more theme options via config.

### 7. Running Agents Dashboard ⭐

Centralized view of all running agents across projects:
- Real-time status (polling every 2s)
- Project association (clickable to navigate)
- Pulsing green dot for active operation
- "AUTO" badge for auto-mode agents
- Total agent count in header
- Stop individual agents

**Inspiration for az:** Our board already shows session states inline, but a dedicated "running sessions" overlay could provide better visibility across many tasks.

### 8. Activity Log for Auto Mode

Logs autonomous execution events with timestamps, phases:
- `auto_mode_feature_start`
- `auto_mode_progress`
- `auto_mode_phase` (Planning, Action, Verification)
- `auto_mode_tool` (specific tool usage)
- `auto_mode_complete`
- `auto_mode_error`

**Inspiration for az:** Could add session activity logging visible in detail panel or separate view.

### 9. Chat History per Session

Persistent conversation tracking with:
- Message threading
- Session archiving
- Image attachments in messages
- Clear/delete with confirmation

**Inspiration for az:** Could capture Claude conversation history from sessions for review. Would require parsing tmux scrollback or Claude output.

### 10. Feature Suggestions Generation

AI analyzes codebase and suggests features based on:
- Project structure
- TODO comments
- Missing tests
- Common patterns

**Inspiration for az:** Could add a "suggest tasks" command using Claude to analyze codebase and create beads.

---

## Features Azedarach Already Does Better

| Feature | Azedarach | AutoMaker |
|---------|-----------|-----------|
| **Modal Editing** | Full Helix-style (7 modes: normal/select/goto/action/search/command/sort) | Basic keyboard shortcuts only |
| **Jump Labels** | 2-char ergonomic labels (gw) for instant task navigation | None |
| **Multi-selection** | Batch operations with v mode | Limited/none |
| **Sorting Options** | By session state/priority/updated with chained fallbacks | Basic |
| **VC Integration** | Auto-pilot toggle, REPL commands via command mode | None |
| **Beads Backend** | Structured issue tracking with dependencies, types, priorities | Simple local Zustand state |
| **tmux Integration** | Deep integration (scrollback, vi-copy-mode, Ctrl-U binding) | Terminal tabs (less integrated) |
| **State Detection** | Pattern matching on output (waiting/done/error/busy) | Event-based (less granular) |
| **Worktree Management** | Epic/task inheritance, auto-cleanup, idempotent creation | Basic worktree isolation |
| **Compact View** | Toggle between Kanban and linear list | Kanban only |
| **Context Window Health** | Visual indicators at 70%/90% thresholds | None |

---

## Implementation Patterns to Learn From

### AutoMaker's Zustand Store Pattern

They use `partialize` for selective persistence to avoid storing transient state:

```typescript
persist(
  (set, get) => ({ /* store */ }),
  {
    name: "automaker-storage",
    version: 2,
    partialize: (state) => ({
      projects: state.projects,
      aiProfiles: state.aiProfiles,
      settings: state.settings,
      // Exclude: ipcConnected, runningTasks, etc.
    }),
    migrate: (persisted, version) => {
      // Handle version upgrades
    }
  }
)
```

**Lesson:** We could optimize what we persist to disk if we add local persistence.

### AutoMaker's Custom Collision Detection (dnd-kit)

Prioritizes columns over cards for intuitive drag-drop:

```typescript
function customCollisionDetection(args) {
  // First, check for column collisions using pointer
  const columnCollisions = pointerWithin(args).filter(
    collision => COLUMNS.some(col => col.id === collision.id)
  );

  if (columnCollisions.length > 0) {
    return columnCollisions;
  }

  // Fall back to rectangle intersection for cards
  return rectIntersection(args);
}
```

**Lesson:** Good pattern for complex drag-drop if we add mouse support.

### AutoMaker's Event-Driven Auto-Mode

They use distinct events for workflow phases:

```typescript
// Event types
'auto_mode_feature_start'    // Agent begins work
'auto_mode_progress'         // Incremental updates
'auto_mode_phase'            // Workflow transitions
'auto_mode_tool'             // Tool usage tracking
'auto_mode_complete'         // Feature done (pass/fail)
'auto_mode_error'            // Failure with context
```

**Lesson:** Our StateDetector could emit more granular events for better observability.

### AutoMaker's Session Naming

Random memorable names for quick session creation:

```typescript
const adjectives = ["Swift", "Clever", "Bold", ...]
const nouns = ["Agent", "Builder", "Coder", ...]
const name = `${random(adjectives)} ${random(nouns)} ${randomInt(1, 99)}`
// → "Swift Agent 42"
```

**Lesson:** Could use for auto-naming sessions if user doesn't provide a name.

---

## Recommended Features to Add

### High Priority (significant value)

1. **AI Profiles**
   - Quick model/thinking presets per task type
   - Store in config: `{ name: "Deep Work", model: "opus", thinking: "high" }`
   - Apply via action menu or per-bead

2. **Context File Manager**
   - TUI for browsing/editing `.claude/` context files
   - New view mode or overlay
   - Markdown preview support

3. **Concurrency Limits**
   - Max parallel sessions config option
   - Visual indicator when at limit
   - Queue pending tasks

4. **Image Attachment Viewing**
   - Display beads image attachments in detail panel
   - Support for common formats (PNG, JPG)
   - OpenTUI supports image rendering

### Medium Priority (nice to have)

5. **Session Activity Log**
   - Timestamped log of Claude actions
   - Tool usage tracking
   - Viewable in detail panel

6. **Project Analysis**
   - Auto-generate context from codebase structure
   - Tech stack detection
   - Store as `.claude/project-context.md`

7. **Running Sessions Dashboard**
   - Dedicated overlay for all active sessions
   - Quick actions (attach, pause, stop)
   - Aggregate metrics

### Low Priority (cosmetic/future)

8. **More Themes**
   - Additional color schemes beyond Catppuccin Mocha
   - Theme switching via config or runtime

9. **Feature Suggestions**
   - AI-generated task recommendations
   - Analyze codebase for TODOs, missing tests, etc.

10. **Chat History Capture**
    - Parse Claude output for conversation history
    - Store with bead for reference

---

## Summary

AutoMaker is a polished GUI tool that prioritizes visual workflows and accessibility for less terminal-oriented developers. Azedarach excels at keyboard-driven efficiency and deep terminal integration.

**Key differentiators for Azedarach:**
- Modal editing (Helix-style) far superior for power users
- Beads provides structured issue tracking vs simple local state
- tmux integration enables true session persistence
- Output pattern matching gives finer-grained state detection

**Main inspirations to draw from AutoMaker:**
1. **AI Profiles** - Switch model configs quickly
2. **Context Management** - Manage context files in TUI
3. **Concurrency Control** - Limit parallel sessions
4. **Image Support** - View attachments in detail panel
5. **Activity Logging** - More visibility into session activity

---

## Related Beads

<!-- Add bead IDs here when features are tracked -->
- AI Profiles: TBD
- Context Manager: TBD
- Concurrency Limits: TBD
