# Azedarach vs AutoMaker Comparison

> Deep competitive analysis comparing Azedarach with [AutoMaker](https://github.com/AutoMaker-Org/automaker)
>
> Date: 2025-12-16 (Updated with deep dive)

## Overview

| Aspect | Azedarach | AutoMaker |
|--------|-----------|-----------|
| **Type** | TUI (Terminal UI) | Desktop/Web GUI (Electron + Next.js) |
| **State Mgmt** | Effect + effect-atom | Zustand with persistence |
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

## AutoMaker Architecture Deep Dive

### Application Structure

```
apps/app/src/
├── app/                    # Next.js app directory
├── components/
│   ├── dialogs/           # Modal dialogs
│   │   ├── board-background-modal.tsx
│   │   └── file-browser-dialog.tsx
│   ├── layout/
│   │   ├── sidebar.tsx    # Main navigation
│   │   └── project-setup-dialog.tsx
│   ├── ui/                # 29 reusable components
│   │   ├── git-diff-panel.tsx
│   │   ├── log-viewer.tsx
│   │   ├── keyboard-map.tsx
│   │   ├── count-up-timer.tsx
│   │   ├── hotkey-button.tsx
│   │   └── ...
│   └── views/             # Main application views
│       ├── board-view/    # Kanban board (with subdirs)
│       ├── agent-view.tsx
│       ├── analysis-view.tsx
│       ├── code-view.tsx
│       ├── context-view.tsx
│       ├── interview-view.tsx
│       ├── profiles-view.tsx
│       ├── running-agents-view.tsx
│       ├── settings-view.tsx
│       ├── spec-view.tsx
│       ├── terminal-view.tsx
│       ├── welcome-view.tsx
│       └── wiki-view.tsx
├── hooks/
│   ├── use-auto-mode.ts
│   ├── use-keyboard-shortcuts.ts
│   ├── use-message-queue.ts
│   └── ...
├── store/
│   ├── app-store.ts       # Main Zustand store (110+ actions)
│   └── setup-store.ts
└── types/
```

### View Breakdown

| View | Purpose | Key Features |
|------|---------|--------------|
| **board-view** | Kanban board | Drag-drop, columns, search, detail levels |
| **agent-view** | Chat with Claude | Message history, image attachments, session management |
| **analysis-view** | Codebase analysis | Tech stack detection, file structure, spec generation |
| **code-view** | File browser | Tree navigation, content preview, syntax highlighting |
| **context-view** | Context files | Add/edit/delete files, markdown preview, drag-drop |
| **interview-view** | Project onboarding | Guided Q&A to generate initial spec |
| **profiles-view** | AI profiles | Create/edit/reorder model presets |
| **running-agents-view** | Active agents | Real-time monitoring, stop controls |
| **settings-view** | Configuration | API keys, themes, shortcuts, defaults |
| **spec-view** | Spec editor | XML editor, regeneration, feature generation |
| **terminal-view** | Integrated terminal | Tabs, splits, auth, platform detection |
| **welcome-view** | Landing page | Project creation, recent projects |
| **wiki-view** | Documentation | Collapsible sections, getting started guide |

---

## Features Deep Dive

### 1. AI Profiles System ⭐⭐⭐

**What They Have:**
- Built-in profiles: "Heavy Task" (Opus + ultrathink), "Balanced" (Sonnet + medium), "Quick Edit" (Haiku + none)
- Custom profile creation with full configuration
- Drag-and-drop reordering
- Reset built-in profiles to defaults
- Per-feature profile selection

**Implementation Details:**
```typescript
interface AIProfile {
  id: string
  name: string
  model: "opus" | "sonnet" | "haiku"
  thinkingLevel: "none" | "low" | "medium" | "high" | "ultrathink"
  isBuiltIn: boolean
  // Additional config options
}
```

**Inspiration for az:**
- Add profile system to config schema
- Quick switch via action menu (Space → p)
- Per-bead profile override in metadata
- Profiles could include: model, thinking level, permission settings, custom system prompt additions

### 2. Board Detail Levels ⭐⭐

**What They Have:**
Three card detail levels toggled via segmented control:

| Level | Shows |
|-------|-------|
| **Minimal** | Title & category only |
| **Standard** | Steps & progress |
| **Detailed** | Model, tools & tasks |

**Implementation:**
```typescript
type DetailLevel = "minimal" | "standard" | "detailed"
// Stored per-project in Zustand
```

**Inspiration for az:**
- Add detail level toggle (could use `d` key to cycle)
- Minimal: Just ID + title (fits more on screen)
- Standard: Current view
- Detailed: Show description preview, session metrics

### 3. Kanban Card Features ⭐⭐⭐

**What They Have:**

Status indicators on each card:
- Priority badges (P1/P2/P3 with color coding)
- Category labels
- Error alerts with detailed tooltips
- "Just finished" status with animated pulse
- Branch/worktree information
- Elapsed timer for active tasks (MM:SS format)
- Agent phase status (Planning/Action/Verification)
- Task progress with individual todo items

**Card Actions by Status:**

| Status | Available Actions |
|--------|-------------------|
| Backlog | Edit, Make (implement) |
| In Progress | Resume/Verify, manual verify, view logs, force stop |
| Waiting Approval | Refine prompt, revert changes, merge/commit, view logs |
| Verified | Complete, view logs |

**Inspiration for az:**
- Add elapsed timer to cards (we have duration in session metrics)
- Show agent phase visually (could parse from output)
- "Just finished" pulse animation for recently completed
- Revert action for waiting approval state

### 4. Count-Up Timer ⭐

**What They Have:**
Simple MM:SS timer showing elapsed time since task started.

```typescript
// Updates every second, recalculates from startedAt timestamp
const elapsed = Math.floor((now - startedAt) / 1000)
const minutes = Math.floor(elapsed / 60)
const seconds = elapsed % 60
return `${pad(minutes)}:${pad(seconds)}`
```

**Inspiration for az:**
- Add to TaskCard when session is busy
- Already have session start time, just need display

### 5. Git Diff Panel ⭐⭐

**What They Have:**
- Unified diff parsing into files and hunks
- Color-coded additions (green) and deletions (red)
- Expand/collapse individual files
- File grouping by status (Added/Modified/Deleted/Renamed)
- Statistics: total files, additions, deletions
- "Expand All" / "Collapse All" controls
- Retry on error with loading states

**Inspiration for az:**
- Add diff view overlay for sessions
- Could run `git diff` in worktree and display
- Useful for reviewing changes before PR creation

### 6. Log Viewer ⭐⭐

**What They Have:**
- Parses structured log output
- Color-coded badges by type (prompt, tool_call, error, success)
- Entry counts per type in header
- JSON syntax highlighting in logs
- Truncated preview (80 chars) when collapsed
- Expand/collapse per entry
- "Expand All" / "Collapse All" buttons

**Inspiration for az:**
- Could parse Claude output into structured log entries
- Display in detail panel or separate overlay
- Filter by type (tool calls, errors, etc.)

### 7. Keyboard Map Visualization ⭐

**What They Have:**
- Visual US QWERTY keyboard layout
- Color-coded keys showing shortcut categories (navigation, UI, actions)
- Conflict detection when multiple shortcuts use same key
- Yellow indicator dot for customized shortcuts
- Click to select keys
- Tooltip on hover showing shortcut details
- Stats: total shortcuts, keys in use, available keys

**21 Configurable Shortcuts:**
- Navigation (7): Board, Agent, Spec, Context, Settings, Profiles, Terminal
- UI (1): Toggle Sidebar
- Actions (13): Add Feature, Add Context, Start Next, New Session, etc.

**Inspiration for az:**
- Already have HelpOverlay, but visual keyboard map could be nice
- Show which keys are bound vs available
- Useful for onboarding new users

### 8. Project Onboarding (Interview View) ⭐⭐

**What They Have:**
Guided Q&A flow to gather project requirements:

1. "What do you want to build?" (description)
2. Technology stack preferences
3. Core features/functionality
4. Additional requirements

Then auto-generates `app_spec.txt` in XML format with project overview, tech stack, and development guidelines.

**Inspiration for az:**
- Could add onboarding flow for new projects
- Generate initial CLAUDE.md or context files
- Create initial beads from described features

### 9. Context File Management ⭐⭐⭐

**What They Have:**
- Dedicated `.automaker/context` directory
- Left sidebar listing all context files with count
- Add files via button, drag-drop, or paste
- Text file editing with monospace textarea
- Markdown preview toggle
- Image preview for image files
- Save state tracking ("Save" / "Saving" / "Saved")
- Delete with confirmation

**Inspiration for az:**
- Add context view (new mode or overlay)
- Browse/edit `.claude/` directory files
- Support skills, commands, settings files
- Markdown preview in TUI (simplified)

### 10. Running Agents Dashboard ⭐⭐

**What They Have:**
- Centralized view of ALL running agents across projects
- Polling every 2 seconds
- Event-based updates on completion/error
- Per-agent display:
  - Feature ID being processed
  - Project name (clickable to navigate)
  - Pulsing green status dot
  - "AUTO" badge for auto-mode
- Stop individual agents
- Total count in header

**Inspiration for az:**
- Dedicated overlay showing all active sessions
- Quick attach/pause/stop actions
- Aggregate stats (total running, total waiting, etc.)

### 11. Board Search ⭐

**What They Have:**
- Search input with "/" hotkey to focus
- Filters features by keyword
- Clear button when text entered
- Shows "Creating spec" loading badge

**Inspiration for az:**
- Already have search mode (`/`)
- Could add loading indicator when beads are syncing

### 12. Board Background Customization ⭐

**What They Have:**
- Upload custom background images (JPG, PNG, GIF, WebP up to 10MB)
- Drag-drop or file browser
- Opacity controls:
  - Card opacity (0-100%)
  - Column opacity (0-100%)
  - Card border opacity (0-100%)
- Visual effects:
  - Column border visibility toggle
  - Card glassmorphism with blur
  - Card border display toggle
  - Scrollbar hiding
- Per-project settings

**Inspiration for az:**
- TUI doesn't support images, but could add opacity/transparency controls
- Could support different board "styles" via config

### 13. Sidebar Navigation ⭐⭐

**What They Have:**
- Collapsible (72px expanded on mobile, 288px desktop, 64px collapsed)
- Glass morphism styling with gradient background
- Sections: Project, Tools, Bottom
- Active item highlighting with gradient and left border
- Keyboard shortcuts shown for each item
- Project picker with search and drag-drop reordering
- Running agents count badge
- Responsive (auto-collapse on small screens)

**Navigation Items:**
- Kanban Board, Agent Runner (Project section)
- Spec Editor, Context, AI Profiles, Terminal (Tools)
- Wiki, Running Agents, Settings (Bottom)

**Inspiration for az:**
- Our modal system is different (keyboard-driven)
- But could add visual indicators for "active view"
- Running sessions count in status bar

### 14. Terminal Integration ⭐⭐

**What They Have:**
- Multiple terminal sessions in tabs
- Split terminals horizontally (Cmd+D) or vertically (Cmd+Shift+D)
- Resizable panels
- Drag-drop terminals between tabs
- Per-terminal font size
- Password authentication option
- Platform/shell/architecture display
- Kill sessions on close

**Inspiration for az:**
- We have tmux which is more powerful
- But split view within detail panel could be useful
- Font size control for terminal output

### 15. Message Queue ⭐

**What They Have:**
```typescript
interface QueuedMessage {
  id: string
  content: string
  images?: string[]
  timestamp: number
}
```
- Queue messages while one is processing
- Retry on failure (keeps message in queue)
- Sequential processing with isProcessingQueue flag

**Inspiration for az:**
- Could queue bead updates when offline
- Retry failed sync operations

### 16. Auto-Mode Event System ⭐⭐

**What They Have:**
Granular events for workflow tracking:

| Event | Purpose |
|-------|---------|
| `auto_mode_started` | Mode enabled for project |
| `auto_mode_stopped` | Mode disabled |
| `auto_mode_idle` | Waiting for work |
| `auto_mode_feature_start` | Agent begins feature |
| `auto_mode_progress` | Incremental updates |
| `auto_mode_phase` | Planning → Action → Verification |
| `auto_mode_tool` | Specific tool usage |
| `auto_mode_feature_complete` | Feature done (pass/fail) |
| `auto_mode_error` | Failure with context |

Special handling for auth errors with actionable guidance.

**Inspiration for az:**
- Our StateDetector could emit more granular events
- Add phase detection (planning/implementing/testing)
- Better error categorization

### 17. Board Actions (20 Total) ⭐⭐

**What They Have:**
1. handleAddFeature - Create with category, description, steps, images
2. handleUpdateFeature - Modify and persist
3. handleDeleteFeature - Remove, stop agents, cleanup images
4. handleStartImplementation - Move to in-progress, start agent
5. handleVerifyFeature - Trigger verification
6. handleResumeFeature - Continue paused work
7. handleManualVerify - Mark verified without checks
8. handleMoveBackToInProgress - Revert from verification
9. handleOpenFollowUp - Prepare follow-up dialog
10. handleSendFollowUp - Send continuation with optional images
11. handleCommitFeature - Finalize changes
12. handleRevertFeature - Discard all changes, return to backlog
13. handleMergeFeature - Integrate to main branch
14. handleCompleteFeature - Archive completed
15. handleUnarchiveFeature - Restore from archive
16. handleViewOutput - Display execution results
17. handleOutputModalNumberKeyPress - Navigate outputs with 1-9, 0
18. handleForceStopFeature - Kill active agent
19. handleStartNextFeatures - Start multiple with concurrency limit
20. handleDeleteAllVerified - Bulk remove verified

**Inspiration for az:**
- Add more granular actions:
  - Revert (discard changes)
  - Merge (integrate to main)
  - Follow-up (send additional instructions)
- Number keys for quick output viewing (1-9)

### 18. Welcome/Onboarding ⭐

**What They Have:**
- Landing page with branding and tagline
- Quick action cards: New Project, Open Project
- Project creation options:
  - Blank project
  - From GitHub templates
  - From custom GitHub URL
- Recent projects list
- Workspace picker modal
- Auto-creates `.automaker` directory structure

**Inspiration for az:**
- Could add welcome screen on first run
- Quick project setup wizard
- Template support for common project types

### 19. Wiki/Documentation ⭐

**What They Have:**
- 8 collapsible sections with expand/collapse all
- Topics:
  - Project Overview
  - Architecture
  - Key Features (12 capabilities)
  - Data Flow (step-by-step lifecycle)
  - Project Structure
  - Key Components
  - Configuration
  - Getting Started (7-step guide)
- Code blocks with syntax highlighting
- Feature cards with icons

**Inspiration for az:**
- In-app help beyond keybinding reference
- Architecture overview for contributors
- Getting started guide

### 20. Settings Organization ⭐

**What They Have:**
Seven settings categories:
1. Claude CLI Status - Monitor integration
2. AI Enhancement - AI feature config
3. Appearance - Theme selection (global + per-project)
4. Keyboard Shortcuts - Bindings with keyboard map
5. Audio - Notification sounds toggle
6. Feature Defaults - Profile visibility, test-skipping, worktree usage
7. Danger Zone - Project deletion

**Inspiration for az:**
- Organize settings into categories
- Per-project overrides
- "Danger zone" pattern for destructive actions

---

## Features Azedarach Already Does Better

| Feature | Azedarach | AutoMaker |
|---------|-----------|-----------|
| **Modal Editing** | Full Helix-style (7 modes) | Basic keyboard shortcuts |
| **Jump Labels** | 2-char ergonomic labels (gw) | None |
| **Multi-selection** | Batch operations with v mode | Limited/none |
| **Sorting Options** | By session state/priority/updated | Basic |
| **VC Integration** | Auto-pilot toggle, REPL commands | None |
| **Beads Backend** | Structured issue tracking with deps | Simple local Zustand |
| **tmux Integration** | Deep (scrollback, vi-mode, keybindings) | Terminal tabs |
| **State Detection** | Pattern matching (waiting/done/error/busy) | Event-based |
| **Worktree Management** | Epic/task inheritance, auto-cleanup | Basic isolation |
| **Compact View** | Toggle Kanban/linear list | Kanban only |
| **Context Window Health** | Visual indicators at 70%/90% | None |
| **CLI-first** | Works over SSH, in tmux, anywhere | Requires GUI |

---

## UI Components to Consider

### High-Value Components

| Component | Purpose | Complexity | Value |
|-----------|---------|------------|-------|
| **CountUpTimer** | Show elapsed time | Low | Medium |
| **GitDiffPanel** | Show changes | Medium | High |
| **LogViewer** | Structured logs | Medium | High |
| **DetailLevelToggle** | Card verbosity | Low | Medium |
| **KeyboardMap** | Visual shortcuts | High | Low |

### Simpler Wins

1. **Elapsed Timer on Cards** - Already have data, just add display
2. **Detail Level Toggle** - Minimal/Standard/Detailed cards
3. **Diff Overlay** - Run git diff and display before PR
4. **Activity Log** - Parse Claude output into structured entries

---

## Implementation Patterns

### AutoMaker's Zustand Store Pattern

Selective persistence to avoid storing transient state:

```typescript
persist(
  (set, get) => ({ /* 110+ actions */ }),
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

### AutoMaker's Event-Driven Architecture

Granular events for workflow phases with project scoping:

```typescript
// Event handler determines project ownership
const projectId = event.projectPath || event.projectId || currentProjectId

switch (event.type) {
  case 'auto_mode_feature_start':
    addRunningTask(projectId, event.featureId)
    break
  case 'auto_mode_feature_complete':
    removeRunningTask(projectId, event.featureId)
    // Update feature status based on pass/fail
    break
  case 'auto_mode_error':
    if (isAuthError(event.message)) {
      showAuthErrorGuidance()
    }
    break
}
```

### AutoMaker's Concurrency Control

```typescript
const { runningTasks, maxConcurrency } = getProjectState(projectId)

const canStartNewTask = runningTasks.length < maxConcurrency

const startNextFeatures = async () => {
  const backlogFeatures = getBacklogFeatures()
  const availableSlots = maxConcurrency - runningTasks.length

  for (const feature of backlogFeatures.slice(0, availableSlots)) {
    await startFeature(feature.id)
  }
}
```

### AutoMaker's Session Naming

Random memorable names:

```typescript
const adjectives = ["Swift", "Clever", "Bold", "Quick", "Brave"]
const nouns = ["Agent", "Builder", "Coder", "Maker", "Runner"]

const generateName = () => {
  const adj = adjectives[Math.floor(Math.random() * adjectives.length)]
  const noun = nouns[Math.floor(Math.random() * nouns.length)]
  const num = Math.floor(Math.random() * 99) + 1
  return `${adj} ${noun} ${num}`
}
// → "Swift Agent 42"
```

---

## Recommended Features to Add

### Tier 1: High Impact, Low Effort

| Feature | Effort | Impact | Notes |
|---------|--------|--------|-------|
| Elapsed timer on cards | 2h | Medium | Already have startedAt |
| Detail level toggle | 4h | Medium | Minimal/Standard/Detailed |
| Concurrency limit | 4h | High | Add to config, enforce in SessionManager |
| Number keys for quick attach | 2h | Medium | 1-9 to attach to running sessions |

### Tier 2: High Impact, Medium Effort

| Feature | Effort | Impact | Notes |
|---------|--------|--------|-------|
| AI Profiles | 1d | High | Config schema + action menu |
| Git diff overlay | 1d | High | Run git diff, parse, display |
| Activity log | 1d | Medium | Parse Claude output |
| Context file browser | 2d | High | New view for .claude/ files |

### Tier 3: Medium Impact, Higher Effort

| Feature | Effort | Impact | Notes |
|---------|--------|--------|-------|
| Structured log viewer | 2d | Medium | Parse output into entries |
| Running sessions overlay | 1d | Medium | Dedicated view for active sessions |
| Revert action | 4h | Medium | Git reset worktree |
| Follow-up action | 4h | Medium | Send additional input to session |

### Tier 4: Nice to Have

| Feature | Effort | Impact | Notes |
|---------|--------|--------|-------|
| Project analysis | 2d | Low | Auto-generate context |
| Feature suggestions | 1d | Low | AI suggests tasks |
| More themes | 4h | Low | Additional color schemes |
| Keyboard map viz | 2d | Low | Visual keyboard display |

---

## Summary

AutoMaker is a polished GUI tool with strong UX patterns. Key takeaways:

### Adopt These Patterns
1. **AI Profiles** - Quick model configuration switching
2. **Detail Levels** - Toggle card verbosity
3. **Elapsed Timer** - Show task duration
4. **Concurrency Control** - Limit parallel sessions
5. **Granular Events** - Better workflow tracking
6. **Git Diff View** - Review changes before PR

### Keep Our Advantages
1. **Modal Editing** - Helix-style is far more powerful
2. **Beads Backend** - Structured > local state
3. **CLI-First** - Works everywhere (SSH, tmux, containers)
4. **tmux Integration** - True session persistence
5. **Pattern Detection** - More granular state tracking

### Quick Wins (< 1 day each)
1. Elapsed timer on cards
2. Detail level toggle (d key)
3. Concurrency limit in config
4. Number keys 1-9 for quick attach

---

## Related Beads

| Bead | Title | Priority |
|------|-------|----------|
| `az-ph1` | Add agent phase detection and display | P1 |
| `az-et1` | Add elapsed timer to TaskCard | P2 |
| `az-dl1` | Add detail level toggle for cards | P2 |
| `az-cl1` | Add concurrency limit config | P2 |
| `az-nk1` | Add number keys 1-9 for quick attach | P3 |

**Not yet tracked:**
- AI Profiles system (epic)
- Git diff overlay
- Context file browser
