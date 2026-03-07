# Keybinding Reference

Azedarach uses **Helix-style modal keybindings** inspired by the Helix editor. This provides efficient, ergonomic navigation without leaving the home row.

## Mode Overview

```
┌────────────────────────────────────────────────────────────────────────────┐
│                            NORMAL MODE (NOR)                                │
│  hjkl: navigate  g: goto  v: select  Space: act  /: search  ,: sort  f: filter │
└────────────────────────────────────────────────────────────────────────────┘
         │           │           │           │           │           │
         │           ▼           ▼           ▼           ▼           ▼
         │   ┌────────────┐ ┌──────────┐ ┌──────────┐ ┌──────────┐ ┌──────────┐
         │   │ GOTO (GTO) │ │ SELECT   │ │ ACTION   │ │ SEARCH   │ │ SORT     │
         │   │ gg/ge/gl   │ │ (SEL)    │ │ (ACT)    │ │ (SRC)    │ │ (SRT)    │
         │   │ gw: labels │ │ a/5:     │ │ h/l:move │ │ filter   │ │ s/p/u:   │
         │   └────────────┘ │ toggle   │ │ a:attach │ │ by title │ │ sort by  │
         │         │        └──────────┘ └──────────┘ └──────────┘ └──────────┘
         │         │             │           │             │            │
         └─────────┴─────────────┴───────────┴─────────────┴────────────┘
                                    Esc (or q in non-text close contexts): back/close
```

## Normal Mode

The default mode for navigation and basic actions.

### Navigation

| Key | Action | Notes |
|-----|--------|-------|
| `h` | Move left (previous column) | Wraps at edges |
| `j` | Move down (next task) | Virtual scrolling |
| `k` | Move up (previous task) | Virtual scrolling |
| `l` | Move right (next column) | Wraps at edges |
| `←` | Same as `h` | Arrow key alternative |
| `↓` | Same as `j` | Arrow key alternative |
| `↑` | Same as `k` | Arrow key alternative |
| `→` | Same as `l` | Arrow key alternative |
| `Ctrl-d` | Half-page down | Fast scrolling |
| `Ctrl-u` | Half-page up | Fast scrolling |

### Actions

| Key | Action | Notes |
|-----|--------|-------|
| `Enter` | View/Enter | Show task details; on epics, enters drill-down |
| `Space` | Enter Action mode | Prefix for commands |
| `,` | Enter Sort mode | Change task sort order |
| `f` | Enter Filter mode | Filter tasks by status/priority/type/session |
| `/` | Enter Search mode | Filter tasks by title/ID |
| `g` | Enter Goto mode | Prefix for jumps |
| `v` | Enter Select mode | Multi-selection |
| `Tab` | Toggle view mode | Switch between Kanban and Compact views (board only) |
| `r` | Refresh git stats | Update git stats for all active sessions |
| `p` | Open planning | AI-powered planning workflow |
| `c` | Create bead (manual) | Opens $EDITOR with template |
| `C` | Create via Claude | Natural language task creation |
| `s` | Show settings | Opens interactive settings overlay |
| `?` | Show help | Dismiss with `Esc` or `q` |
| `L` | View logs | Opens az.log menu (v=view, e=edit, q=quit) |
| `Ctrl-l` | Redraw screen | Force full screen refresh |
| `q` | Quit/Back | Exits drill-down; otherwise quits app; also closes non-text overlays |
| `Esc` | Back/close | Primary back key for modal contexts |

## Epic Drill-Down

When the cursor is on an epic card, pressing `Enter` enters **drill-down mode** instead of showing the detail panel. This focuses the board to show only that epic's children.

### Entering Drill-Down

| Key | Action | Notes |
|-----|--------|-------|
| `Enter` (on epic) | Enter drill-down | Shows only epic's children |

### Drill-Down View

When in drill-down mode:

```
┌─ Open ─────┬─ In Progress ─┬─ Done ──────┐
│  ◀ az-gds  │   Epic View   │  ████░ 3/5  │  ← Header bar
│  az-lqb    │  az-7sr ⚙    │  az-aiu ✓   │
│  az-bjp    │               │  az-xxx ✓   │
└────────────┴───────────────┴─────────────┘
```

**Header bar shows:**
- Back arrow (`◀`) with epic ID
- Epic title (centered)
- Progress bar (`████░░`) with count (e.g., `3/5` children closed)

### Navigation in Drill-Down

| Key | Action | Notes |
|-----|--------|-------|
| `h/j/k/l` | Navigate | Works normally within children |
| `Enter` | Open detail | On child tasks, opens detail panel |
| `q` | Exit drill-down | Returns to main board (doesn't quit app) |
| `Esc` | Exit drill-down | Same as `q` |

### Cursor Restoration

When you exit drill-down mode:
- The cursor returns to the epic you drilled into
- Your previous position is remembered

### Notes

- **Epic children** are tasks with a parent-child dependency to the epic
- All normal actions (Space menu, search, sort) work in drill-down
- The progress bar fills based on closed/total children ratio

## Settings Overlay

Press `s` to open the interactive settings overlay. This allows you to view and modify Azedarach configuration values in real-time.

### Navigation

| Key | Action | Notes |
|-----|--------|-------|
| `j` | Move down | Select next setting |
| `k` | Move up | Select previous setting |
| `Space` or `Enter` | Toggle value | Changes boolean settings; cycles enum values |
| `e` | Edit in editor | Opens .azedarach.json in $EDITOR for advanced changes |
| `Esc` / `q` | Close overlay | Returns to normal mode |

### Available Settings

The settings overlay shows all editable configuration options:

#### Session Settings
- **CLI Tool**: Choose between "claude" (default), "opencode", or "codex" for AI sessions
- **Skip Permissions**: When enabled, Claude can execute commands without asking for permission (dangerous mode)

#### Git Settings
- **Push on Create**: Whether to push branches to remote when worktrees are created
- **Git Push**: Enable/disable automatic pushing to remote
- **Git Fetch**: Enable/disable automatic fetching from remote
- **Line Changes**: Show line-by-line changes in diffs instead of file-level summaries

#### PR Settings
- **PR Enabled**: Enable/disable PR workflow automation
- **Auto Draft PR**: Create PRs as drafts by default
- **Auto Merge PR**: Automatically merge approved PRs

#### Notification Settings
- **Bell Notify**: Enable/disable terminal bell for notifications
- **System Notify**: Enable/disable system notifications

#### Network Settings
- **Auto Detect Network**: Enable/disable automatic network connectivity detection

#### Linear Settings
- **Linear Sync**: Enable/disable automatic synchronization with linear backend
- **Linear Webhooks**: Enable/disable webhook-driven board refresh for the linear backend

#### State Detection Settings
- **Pattern Matching**: Enable/disable AI pattern-based state detection for sessions

### How Settings Work

- **Real-time updates**: Changes are applied immediately and saved to `.azedarach.json`
- **Type safety**: Only valid values are accepted for each setting type
- **Reload on change**: The TUI automatically reloads configuration when you toggle settings
- **Advanced editing**: Press `e` to open the raw JSON file in your $EDITOR for complex changes

### Example Workflow

```
1. Press `s` to open settings overlay
2. Use `j`/`k` to navigate to "CLI Tool" setting
3. Press `Space` to cycle between "claude", "opencode", and "codex"
4. Press `j` to move to "Skip Permissions" setting
5. Press `Space` to toggle the boolean value
6. Press `Esc` to close and return to normal mode
```

### Configuration File

Settings are stored in `.azedarach.json` in your project root:

```json
{
  "cliTool": "claude",
  "session": {
    "dangerouslySkipPermissions": false
  },
  "sessionRecovery": {
    "mode": "auto",
    "autoRecoveryDelayMs": 2000,
    "retryBaseDelayMs": 1000,
    "retryMaxDelayMs": 60000
  },
  "git": {
    "pushBranchOnCreate": true,
    "pushEnabled": true,
    "fetchEnabled": true,
    "showLineChanges": false
  },
  "pr": {
    "enabled": true,
    "autoDraft": true,
    "autoMerge": false
  },
  "notifications": {
    "bell": true,
    "system": false
  },
  "network": {
    "autoDetect": true
  },
  "issueTracker": {
    "linear": {
      "syncEnabled": true,
      "team": "AZE",
      "webhooks": {
        "enabled": true
      }
    }
  },
  "stateDetection": {
    "patternMatching": false
  }
}
```

`sessionRecovery.retryBaseDelayMs` and `sessionRecovery.retryMaxDelayMs` are advanced controls for auto-recovery retry timing in raw JSON. Azedarach retries transient recovery failures indefinitely, while capping each wait duration at `retryMaxDelayMs`.

## Planning Overlay

Press `p` to open the AI-powered planning overlay. This lets you describe a feature in natural language and automatically generates linear with proper epics, tasks, and dependencies.

### Input Phase

| Key | Action | Notes |
|-----|--------|-------|
| Type | Add characters | Build your description |
| `Backspace` | Delete character | Remove last character |
| `Enter` | Submit | Start the planning workflow |
| `Esc` | Cancel | Close without planning |

### Generation/Review Phase

| Key | Action | Notes |
|-----|--------|-------|
| `Esc` | Cancel | Stop planning and close |
| `a` | Attach to session | View the planning tmux session |

### Completion/Error Phase

| Key | Action | Notes |
|-----|--------|-------|
| `Enter` | Close | Return to normal mode |
| `Esc` | Close | Return to normal mode |

### How Planning Works

1. **Input**: Describe what you want to build in natural language
2. **Generate**: Claude Code creates an initial plan with tasks, types, and dependencies
3. **Review**: The plan is automatically reviewed up to 5 times to:
   - Break down large tasks into smaller ones
   - Ensure correct dependency ordering
   - Optimize for parallel development
4. **Create**: Linear are created with:
   - An epic as the parent feature
   - Child tasks linked to the epic
   - Blocking dependencies for sequential work
   - Proper priorities and types

### Example Workflow

```
1. Press 'p' to open planning
2. Type: "Add user authentication with OAuth"
3. Press Enter to start
4. Wait for generation and review passes
5. Linear are created automatically
6. Navigate to the epic and start sessions on children
```

### Tips

- **Be specific**: "Add dark mode toggle to settings page with localStorage" works better than "add dark mode"
- **Include constraints**: "Must work offline" or "Should use existing auth library"
- **Press 'a' to debug**: Attach to the planning session if you want to see what Claude is doing

## View Modes

Azedarach supports two view modes that can be toggled with `Tab`:

### Kanban View (Default)

The traditional column-based view showing tasks organized by status:

```
┌─────────────┬─────────────┬─────────────┬─────────────┐
│    OPEN     │ IN PROGRESS │   BLOCKED   │   CLOSED    │
├─────────────┼─────────────┼─────────────┼─────────────┤
│ ┌─────────┐ │ ┌─────────┐ │ ┌─────────┐ │ ┌─────────┐ │
│ │ P1 az-1 │ │ │ P2 az-4 │ │ │ P1 az-7 │ │ │ P3 az-9 │ │
│ │ Task 1  │ │ │ Task 4  │ │ │ Task 7  │ │ │ Task 9  │ │
│ └─────────┘ │ └─────────┘ │ └─────────┘ │ └─────────┘ │
└─────────────┴─────────────┴─────────────┴─────────────┘
```

- **Navigation**: `h/l` moves between columns, `j/k` moves within column
- **Best for**: Visualizing workflow stages, status overview

### Compact View

A linear list showing all tasks sorted by status then priority:

```
Pri Stat   ID       Title
P1  OPEN 🔵 az-123  Fix authentication bug
P2  OPEN   az-124  Add dark mode toggle
P1  PROG 🟡 az-125  Refactor database layer
P2  BLKD   az-126  Update API documentation
P3  DONE ✅ az-127  Fix typo in README
```

- **Navigation**: `j/k` moves through the full list, `h/l` has no effect
- **Best for**: Seeing more tasks at once, priority-based scanning

### Visual Indicator

The status bar shows the current view mode:
- **KAN**: Kanban view (columns)
- **LST**: Compact list view (linear)

## Create & Edit Modes

Azedarach provides both manual (via $EDITOR) and AI-assisted (via Claude) modes for creating and editing linear.

| Action | Manual | AI-Assisted |
|--------|--------|-------------|
| Create | `c` (editor) | `C` (Claude) |
| Edit | `Space` `e` (editor) | `Space` `E` (Claude) |

## Manual Create Mode (`c`)

Press `c` to create a new bead using your $EDITOR with a structured template.

### How It Works

1. Press `c` to open your $EDITOR with a blank template
2. Fill in the fields:
   - **Title**: Required - the task name
   - **Type**: task, bug, feature, epic, or chore
   - **Priority**: P0 (highest) to P4 (lowest)
   - **Status**: backlog, ready, in_progress, review, done
   - **Description**, **Design**, **Notes**, **Acceptance Criteria**: Optional sections
3. Save and close the editor
4. The bead is created via `az issue create`

### Template Format

```markdown
# NEW: Enter title here
───────────────────────────────────────────────────────────────
Type:     task        (task | bug | feature | epic | chore)
Priority: P2          (P0 = highest, P4 = lowest)
Status:   backlog     (backlog | ready | in_progress | review | done)
Assignee:
Labels:
Estimate:

───────────────────────────────────────────────────────────────
## Description

Describe the task here...

───────────────────────────────────────────────────────────────
## Acceptance Criteria

- [ ] Criteria 1
```

## Manual Edit Mode (`e`)

Press `e` to edit the selected bead in your $EDITOR.

### How It Works

1. Select a task with hjkl navigation
2. Press `e` to open your $EDITOR with the bead's current data
3. Modify any fields you want to change
4. Save and close the editor
5. Changes are applied via `az issue update`

## Claude Create Mode (`C`)

Press `C` (capital C) to create a task using natural language. This spawns a Claude session that interprets your description and creates the appropriate bead.

### How It Works

1. Press `C` to open the Claude Create prompt
2. Type a natural language description of what you want to do
3. Press `Enter` to launch a Claude session
4. Claude will:
   - Interpret your description
   - Create a bead with appropriate title, type, and description using `az issue create`
   - Remain in the session, ready to work on the task if you want

### Example

1. Press `C`
2. Type: `Add dark mode toggle to settings page`
3. Press `Enter`
4. Claude creates a bead and asks if you'd like to start working on it
5. Attach to the session: `tmux attach -t claude-create-xxx`

### Prompt Shortcuts

When entering your description:
- `Enter`: Submit and launch session
- `Esc`: Cancel
- `Ctrl-U`: Clear entire line
- `Ctrl-W`: Delete last word

## Claude Edit Mode (`E`)

Press `E` (capital E) to edit the selected bead with Claude's assistance.

### How It Works

1. Select a task with hjkl navigation
2. Press `E` to launch a Claude edit session
3. Claude receives the issue's current details and `az issue update` syntax
4. Describe what changes you want in natural language
5. Claude will help update the issue using `az issue update`

### Example

1. Select a task
2. Press `E`
3. Claude shows you the bead details and asks what you'd like to change
4. Type: `Change the priority to P1 and add a note about the deadline`
5. Claude runs the appropriate `az issue update` command

### Comparison: Manual vs Claude

| Feature | Manual (`e`/`c`) | Claude (`E`/`C`) |
|---------|------------------|------------------|
| Interface | $EDITOR | tmux session |
| Input style | Structured fields | Natural language |
| Session | No session started | tmux session persists |
| Follow-up | None | Can continue chatting |
| Best for | Precise edits | Exploration, complex changes |

## Goto Mode

Press `g` to enter goto mode. The next key determines the jump target.

| Sequence | Action | Description |
|----------|--------|-------------|
| `g` `g` | Column top | Jump to first task in current column |
| `g` `e` | Column bottom | Jump to last task in current column |
| `g` `h` | First column | Jump to first column |
| `g` `l` | Last column | Jump to last column |
| `g` `w` | Jump labels | Shows 2-char labels on each task |
| `g` `s` | Spec workspace | Enter Spec workspace (Requirements/Coverage/Publish) |
| `g` `p` | Project selector | Switch between registered projects |

### Jump Labels (gw)

When you press `g` `w`, each visible task gets a 2-character label from the home row keys. Type the label to jump directly to that task.

```
┌─────────────┬─────────────┬─────────────┐
│   OPEN      │ IN PROGRESS │   CLOSED    │
├─────────────┼─────────────┼─────────────┤
│ [aa] Task 1 │ [as] Task 4 │ [ad] Task 7 │
│ [af] Task 2 │ [ag] Task 5 │ [ah] Task 8 │
│ [aj] Task 3 │ [ak] Task 6 │ [al] Task 9 │
└─────────────┴─────────────┴─────────────┘
```

Labels use these home row keys: `a s d f g h j k l ;`

## Spec Workspace

Press `g` `s` from Normal mode to enter the Spec workspace.

| Key | Action | Notes |
|-----|--------|-------|
| `Tab` | Cycle subview | Requirements → Coverage → Publish → Requirements |
| `Esc` / `q` | Return to board | Restores normal board navigation context |

### Notes

- `Tab` continues to toggle Kanban/Compact when you are on the board.
- Inside Spec workspace, `Tab` is reserved for subview cycling.

## Select Mode

Press `v` to enter select mode for multi-task operations.

| Key | Action | Notes |
|-----|--------|-------|
| `a` / `5` | Toggle selection | Add/remove current task from selection |
| `A` | Select all in column | Add all visible non-tombstoned tasks in current column |
| `%` (`Shift+5`) | Select all | Select all non-tombstoned tasks |
| `Space` | Enter action mode | Apply actions to all selected tasks |
| `h/j/k/l` | Navigate | Selections persist while navigating |
| `v` | Exit select mode | Return to normal mode |
| `Esc` / `q` | Exit + clear | Return to normal mode |

### Visual Feedback

Selected tasks are highlighted with a different background color. The status bar shows the selection count.

### Bulk Operations with Selections

When you have multiple tasks selected, action mode commands apply to all selected tasks:

1. Press `v` to enter select mode
2. Navigate with `h/j/k/l` and press `a` (or `5`) to toggle individual tasks
   - Or press `A` to select all in current column
   - Or press `%` to select all tasks
3. Press `Space` to enter action mode
4. Run bulk commands:
   - `x`: Stop all selected sessions
   - `d`: Cleanup all selected worktrees (shows choice dialog)
   - `h/l`: Move all selected tasks left/right

### Bulk Cleanup Dialog

When cleaning up multiple worktrees (`Space` `d` with selections), a choice dialog appears:

| Key | Action | Description |
|-----|--------|-------------|
| `w` | Worktrees only | Delete worktrees but keep linear open |
| `f` | Full cleanup | Delete worktrees AND close linear |
| `Esc` / `q` | Cancel | Return without cleanup |

## Search Mode

Press `/` to enter search mode for filtering tasks.

| Key | Action | Notes |
|-----|--------|-------|
| `Enter` | Confirm | Return to Normal mode |
| `Esc` | Cancel/clear | Return to Normal mode |
| `Backspace` | Delete character | Remove last character from query |
| Any char | Add to query | Case-insensitive search |

### How Search Works

- **Live filtering**: Tasks are filtered as you type
- **Matches**: Title and task ID are searched
- **Case-insensitive**: "Fix" matches "fix", "FIX", etc.
- **Transient query**: Search text is active while in Search mode and clears when you return to Normal
- **Visual indicator**: Status bar shows search input while search mode is active

### Example

1. Press `/` to enter search mode
2. Type `auth` to filter tasks containing "auth"
3. Press `Enter` to return to normal mode

## Sort Mode

Press `,` to enter sort mode for changing how tasks are ordered within each column.

| Key | Action | Description |
|-----|--------|-------------|
| `s` | Sort by Session | Active sessions first (busy, waiting, paused, then idle) |
| `p` | Sort by Priority | Higher priority tasks first (P1 > P2 > P3 > P4) |
| `u` | Sort by Updated | Most recently updated tasks first |
| `Esc` / `q` | Cancel | Exit sort mode without changing |

### How Sort Works

- **Default sort**: Session status (active first) → Priority → Updated at
- **Toggle direction**: Pressing the same sort key again reverses the direction (↓ to ↑)
- **Visual indicator**: The SortMenu shows the current sort with a ↓ (descending) or ↑ (ascending) arrow
- **Multi-level sorting**: Each sort option has secondary and tertiary sort criteria for stable ordering

### Sort Criteria Details

All sort modes prioritize active sessions first, then apply multi-level sorting within each group. The key insight is that `updated` serves as the natural secondary sort—within any primary grouping, you want to see recently-touched tasks rise to the top.

**Session Status Sort (s)**:
1. Primary: Session state (busy → waiting → paused → done → error → idle)
2. Secondary: Updated at (most recent first)
3. Tertiary: Priority (P1 first)

**Priority Sort (p)**:
1. Primary: Priority number (lower = higher priority)
2. Secondary: Updated at (most recent first)
3. Tertiary: Session state

**Updated Sort (u)**:
1. Primary: Updated timestamp (most recent first)
2. Secondary: Priority (P1 first)
3. Tertiary: Session state

## Filter Mode

Press `f` to enter filter mode for filtering tasks by various attributes. Unlike search mode which filters by text, filter mode lets you filter by structured fields.

### Filter Menu

| Key | Action | Description |
|-----|--------|-------------|
| `s` | Status sub-menu | Toggle filtering by issue status |
| `p` | Priority sub-menu | Toggle filtering by priority (P0-P4) |
| `t` | Type sub-menu | Toggle filtering by issue type |
| `S` | Session sub-menu | Toggle filtering by session state |
| `c` | Clear all filters | Remove all filters and return to normal mode |
| `Esc` / `q` | Exit filter mode | Keep current filters and return to normal mode |

### Status Sub-Menu

When `s` is pressed, these keys toggle status filters:

| Key | Status | Description |
|-----|--------|-------------|
| `o` | Open | Toggle showing open tasks |
| `i` | In Progress | Toggle showing in-progress tasks |
| `b` | Blocked | Toggle showing blocked tasks |
| `d` | Closed | Toggle showing closed tasks |

### Priority Sub-Menu

Number keys toggle priority filters:

| Key | Priority | Description |
|-----|----------|-------------|
| `0` | P0 | Toggle critical priority |
| `1` | P1 | Toggle high priority |
| `2` | P2 | Toggle medium priority |
| `3` | P3 | Toggle low priority |
| `4` | P4 | Toggle backlog priority |

### Type Sub-Menu

When `t` is pressed, these keys toggle type filters (uppercase):

| Key | Type | Description |
|-----|------|-------------|
| `B` | Bug | Toggle showing bugs |
| `F` | Feature | Toggle showing features |
| `T` | Task | Toggle showing tasks |
| `E` | Epic | Toggle showing epics |
| `C` | Chore | Toggle showing chores |

### Session Sub-Menu

When `S` is pressed, these keys toggle session state filters (uppercase):

| Key | State | Description |
|-----|-------|-------------|
| `I` | Idle | Toggle showing tasks with no session |
| `U` | Busy | Toggle showing tasks with active Claude sessions |
| `W` | Waiting | Toggle showing tasks waiting for input |
| `D` | Done | Toggle showing completed sessions |
| `X` | Error | Toggle showing errored sessions |
| `P` | Paused | Toggle showing paused sessions |

### Age Filter

Filter tasks by how recently they were updated. Useful for finding stale tasks for cleanup:

| Key | Action | Description |
|-----|--------|-------------|
| `1` | >1 day old | Show tasks not updated in the last day |
| `7` | >7 days old | Show tasks not updated in the last week |
| `3` | >30 days old | Show tasks not updated in the last month |
| `0` | Clear age filter | Remove age filter |

### How Filter Mode Works

- **Multiple values within a field**: OR logic (e.g., `open OR blocked`)
- **Different fields**: AND logic (e.g., `status=open AND priority=P1`)
- **Empty filters**: Show all (no filtering for that field)
- **Hide epic children**: ON by default - hides tasks that are children of epics (encourages drill-down)
- **Persistent filters**: Filters remain active until cleared with `c`
- **Visual feedback**: Menu shows `(N)` next to each field with active filters

### Example Workflows

**Show only high-priority open tasks:**
```
1. Press `f` to enter filter mode
2. Press `s` then `o` to toggle "open" status
3. Press `p` then `1` to toggle P1 priority
4. Press `Esc` to exit filter mode
5. Only open P1 tasks are now visible
```

**Show tasks with active sessions:**
```
1. Press `f` to enter filter mode
2. Press `S` to open session sub-menu
3. Press `U` for busy sessions
4. Press `W` for waiting sessions
5. Press `Esc` to exit filter mode
6. Only tasks with busy or waiting sessions are visible
```

**Clear all filters:**
```
1. Press `f` to enter filter mode
2. Press `c` to clear all filters
3. All tasks are visible again
```

**Bulk cleanup stale worktrees (full workflow):**
```
1. Press `f` to enter filter mode
2. Press `7` to filter to tasks not updated in 7+ days
3. Press `Esc` to exit filter mode
4. Press `%` to select all tasks (includes closed, excludes tombstoned)
5. Navigate and deselect any you want to keep (`a` or `5` toggles)
6. Press `Space` `d` to initiate cleanup
7. Press `w` for worktrees only, or `f` for full cleanup
8. All selected worktrees are cleaned up in parallel
```

**Alternative: Select then filter to review:**
```
1. Press `%` to select all tasks (excludes tombstoned only)
2. Press `f` then `7` to filter view to stale tasks
3. Review what's selected, deselect as needed
4. Press `Space` `d` → `w` to cleanup worktrees
```

### Combining with Search Mode

Filter mode (`f`) and search mode (`/`) work together:
- **Filter mode**: Structured field filtering (status, priority, type, session)
- **Search mode**: Text-based filtering (title, ID)
- Both can be active simultaneously - tasks must match both to be visible

## Action Mode

Press `Space` in Normal mode to enter action mode. A floating palette shows available actions.

### Session Actions

| Sequence | Action | Available When |
|----------|--------|----------------|
| `Space` `s` | Start session | Task is idle (creates worktree + tmux) |
| `Space` `S` | Start+work | Task is idle (starts session with "work on {beadId}" prompt) |
| `Space` `!` | Start (yolo) | Task is idle (like S but with --dangerously-skip-permissions) |
| `Space` `c` | Chat (Haiku) | Task is idle (creates worktree + Haiku session for discussion) |
| `Space` `a` | Attach to session | Session exists (offers to merge main if behind) |
| `Space` `p` | Pause session | Session is busy (Ctrl-C + WIP commit) |
| `Space` `r` | Toggle dev server | Worktree exists (start/stop dev server) |
| `Space` `Ctrl+r` | Restart dev server | Dev server is running (stop + start) |
| `Space` `R` | Resume session | Session is paused |
| `Space` `x` | Stop session | Session exists (kills tmux) |

#### Start (yolo) Mode (Space+!)

The "yolo" start mode (`Space` `!`) launches Claude with the `--dangerously-skip-permissions` flag. This allows Claude to run commands and edit files without asking for permission on each operation.

**Use cases:**
- Trusted, well-defined tasks where you want Claude to work autonomously
- Tasks with clear scope that don't require manual review of each step
- When you're ready to accept all changes Claude makes

**Caution:** Since Claude won't ask for permission, it can make changes faster but with less oversight. Use this for tasks where you trust Claude's judgment.

#### Toggle Dev Server (Space+r)

Toggle a dev server for the selected task's worktree. Each worktree can have its own dev server running with injected port environment variables to avoid conflicts.

**How it works:**
1. If no dev server is running for this bead, starts one in a new tmux session
2. If a dev server is already running, stops it

**Port injection:**
- Ports are auto-allocated starting from 3000
- Environment variables are injected (e.g., `PORT=3001`)
- Configure additional port variables in `.azedarach.json`:

```json
{
  "devServer": {
    "command": "bun run dev",
    "ports": {
      "web": { "default": 3000, "aliases": ["PORT", "VITE_PORT"] },
      "server": { "default": 8000, "aliases": ["SERVER_PORT", "VITE_SERVER_PORT"] }
    }
  }
}
```

**StatusBar indicator:**
- Shows `DEV: localhost:3001` when dev server is running for the selected bead
- Shows `DEV: starting...` during startup
- Shows `DEV: error` if the server failed

**Requirements:**
- A worktree must exist for the bead (start a session first with `Space+s`)
- The worktree must have a `package.json` with a `dev`, `start`, or `serve` script

### Git/PR Actions

| Sequence | Action | Available When |
|----------|--------|----------------|
| `Space` `u` | Update from main | Worktree exists (merge main into branch) |
| `Space` `f` | Show diff | Worktree exists (difftastic side-by-side) |
| `Space` `P` | Create PR | Worktree exists (push + gh pr create) |
| `Space` `O` | Open PR | PR exists (opens in browser via `gh pr view --web`) |
| `Space` `m` | Merge to main | Worktree exists (merge branch to main) |
| `Space` `M` | Abort merge | Worktree exists (abort stuck merge) |
| `Space` `b` | Merge bead into... | Worktree exists (merge into another bead) |
| `Space` `d` | Delete worktree | Worktree exists (cleanup branches) |

#### Update from Main (Space+u)

The update action merges the latest changes from main into your worktree branch. This keeps your branch up to date with the main branch and is useful before creating a PR or when you need the latest changes from main.

**What it does:**

1. Fetches the latest changes from `origin/main`
2. Merges `origin/main` into your worktree branch
3. If there are conflicts, starts a Claude session to resolve them
4. If no conflicts, the merge completes successfully

**When to use:**

- Before creating a PR to ensure your changes are based on the latest main
- To stay up to date with changes other people have merged to main
- When you need a feature or fix that was merged to main after you created your branch

**Conflict resolution:**

If the merge results in conflicts:
- A Claude session is automatically started in the worktree
- Claude will attempt to resolve the conflicts
- You can attach to the session with `Space` `a` to guide Claude or review the resolution
- If Claude gets stuck, use `Space` `M` to abort the merge and try again manually

**Note:** This operation works in the worktree, so your local main branch is not affected. The merge only updates the worktree branch.

#### Create PR (Space+P)

Creating a PR now **automatically syncs with main first** to ensure your branch is up to date. The workflow is:

1. Fetches the latest changes from `origin/main`
2. Merges `origin/main` into your branch (same as `Space` `u`)
3. If conflicts occur, starts a Claude session to resolve them
4. After successful merge, pushes your branch to origin
5. Creates a GitHub PR using `gh pr create`

This ensures PRs are always based on the latest main and reduces the chance of merge conflicts after review.

#### Open PR (Space+O)

Opens the task's existing PR in your default browser. This uses `gh pr view --web` to launch the GitHub PR page.

**Requirements:**
- The task must have a PR (created via `Space` `P`)
- The PR URL must be stored in the task's notes field

**Visual indicator:**
- Tasks with PRs show a PR icon in their header line (🔗 open, 📝 draft, ✅ merged, 🚫 closed)
- Only available (not dimmed) in Action Palette when `hasPR` is true

**Use case:** Quick access to review comments, CI status, or merge the PR on GitHub.

#### Merge to Main (Space+m)

The merge action merges your branch to main **without cleanup** - you can keep iterating:

1. Before merging, az checks if files were modified in both your branch and main
2. If potential conflicts are detected, a **confirmation dialog** appears:
   - Shows which files might conflict
   - Explains that the merge is tested in the worktree first (main isn't affected if it fails)
   - Press `y` to proceed, `n` to cancel
3. If no conflicts detected, the merge proceeds directly
4. On success, the branch changes are merged into main locally

**After merge, your worktree and session remain active** so you can:
- Test changes in main's dev server
- Make additional changes and merge again
- Use `Space+d` when done to cleanup (delete worktree, branch)

**Typical workflow:**
```
Space+m  → merge to main, continue working
Space+m  → merge again after more changes
Space+d  → cleanup when completely done
```

**Note:** This is a local merge operation, not a GitHub PR merge. Use `Space+P` to create a PR for code review workflows.

#### Abort Merge (Space+M)

If a merge gets stuck (e.g., Claude is resolving conflicts but you want to cancel), use `Space` `M` to abort:

1. Runs `git merge --abort` in the worktree
2. Returns the worktree to its pre-merge state
3. You can then:
   - Try the merge again later
   - Manually resolve conflicts
   - Use `Space` `a` to attach to the Claude session and guide resolution

**When to use:**
- Merge conflict resolution is taking too long
- Claude is stuck or going in the wrong direction
- You want to resolve conflicts manually instead

**Note:** Aborting a merge preserves your branch's changes but discards the attempted merge from main. The worktree returns to its state before the merge began.

#### Merge Bead Into... (Space+b)

Merges one bead's work into another bead's branch without going through main or creating a PR. This is useful when you have exploratory work in bead A that you realize belongs with other work in bead B.

**How it works:**

1. Select the source bead (must have a worktree with commits)
2. Press `Space` `b` to enter **merge select mode**
3. The source bead is highlighted with a distinct border (flamingo color)
4. Navigate to the target bead using `h/j/k/l`
5. Press `Space` or `Enter` to confirm the merge
6. Press `Esc` or `q` to cancel

**Merge behavior:**

- Source bead's commits are merged INTO the target bead's branch
- If target has no worktree yet, one is created automatically
- Conflicts are handled the same as other merges (Claude session for resolution)
- After successful merge, the source bead is **closed**
- Source worktree is kept (for reference or manual cleanup later)

**Visual feedback:**

- **Status bar**: Shows "MRG" mode label with keybinding hints
- **Source bead**: Highlighted with flamingo-colored border
- **Toast notifications**: Progress and result messages

**Use cases:**

- Consolidating exploratory work into a main feature bead
- Combining related tasks that were started separately
- Moving completed work from one bead to another

**Example workflow:**

```
1. Work on bead A (exploratory fix)
2. Realize the fix belongs with bead B (main feature)
3. Select bead A, press Space+b
4. Navigate to bead B, press Space
5. Bead A's work is now in bead B, and A is closed
6. Continue working on bead B with all the changes
```

**Requirements:**

- Source bead must have a worktree (has commits to merge)
- Target bead must exist in the tracker (worktree is created if needed)
- Cannot merge a bead into itself

### Orchestrate Mode (Epic Child Spawning)

When orchestrating an epic's child tasks, use the following keys:

| Key | Action | Description |
|-----|--------|-------------|
| `j` / `k` / `↓` / `↑` | Move focus | Navigate child task list |
| `Space` | Toggle task | Select/deselect focused child task |
| `a` | Select all | Select all spawnable tasks |
| `n` | Select none | Clear all selected tasks |
| `Enter` | Spawn selected | Start sessions for selected child tasks |
| `Esc` / `q` | Exit | Close orchestrate mode without spawning |

#### Show Diff (Space+f)

Opens **difftastic side-by-side diff** in a tmux popup showing all changes since the branch diverged from main.

**What you see:**
- Stat summary first (files changed, insertions, deletions)
- Then full diff with syntax-aware highlighting
- Side-by-side comparison for easy review

**Navigation (less):**
- `j/k` or arrows: Scroll up/down
- `←/→`: Horizontal scroll for wide diffs
- `q`: Quit and return to az

**Use case:** Review Claude's changes before merging to main.

### Editor Actions

| Sequence | Action | Available When |
|----------|--------|----------------|
| `Space` `H` | Open Helix editor | Always (creates worktree if needed) |

Opens Helix in a dedicated "hx" tmux window for the selected task. If no worktree exists, creates one first. This is useful for:
- Manual code exploration alongside Claude
- Quick edits when Claude is busy
- Pair-programming with Claude (Helix in one window, Claude in another)

After starting Helix, use `Space` `a` to attach to the tmux session.

### Movement Actions

| Sequence | Action | Description |
|----------|--------|-------------|
| `Space` `h` | Move left | Move task(s) to previous column |
| `Space` `l` | Move right | Move task(s) to next column |

### Edit Actions

| Sequence | Action | Description |
|----------|--------|-------------|
| `Space` `e` | Edit bead (manual) | Opens in $EDITOR as markdown |
| `Space` `E` | Edit bead (Claude) | Placeholder (currently shows "not implemented" toast) |

### Fork Actions

| Sequence | Action | Description |
|----------|--------|-------------|
| `Space` `F` | Fork bead | Create child, new epic, or sibling fork |

### Attachment Actions

| Sequence | Action | Description |
|----------|--------|-------------|
| `Space` `i` | Attach image | Open image attachment overlay (paste or file path) |

### Other Actions

| Sequence | Action | Description |
|----------|--------|-------------|
| `Esc` / `q` | Cancel | Exit action mode |

### Batch Operations

If you have tasks selected (from Select mode), Action mode commands apply to all selected tasks:

1. Press `v` to enter Select mode
2. Navigate and press `a` (or `5`) to select multiple tasks
3. Press `Esc` to return to Normal mode
4. Press `Space` `l` to move all selected tasks right

## Image Attachments

Tasks can have images attached to provide visual context for Claude sessions.

### Viewing & Managing Attachments (Detail Panel)

When viewing a task's details (`Enter`), you can scroll and manage attachments:

**Scrolling (always available):**

| Key | Action | Description |
|-----|--------|-------------|
| `Ctrl-u` | Scroll up | Scroll up half page |
| `Ctrl-d` | Scroll down | Scroll down half page |

**Attachment navigation (when attachments exist):**

| Key | Action | Description |
|-----|--------|-------------|
| `j` / `↓` | Select next | Move selection to next attachment |
| `k` / `↑` | Select previous | Move selection to previous attachment (or deselect) |
| `v` | Preview | Open image preview overlay (in-terminal rendering) |
| `o` | Open | Open selected attachment in system image viewer |
| `x` | Remove | Delete selected attachment |
| `i` | Add | Open image attachment overlay to add more |
| `Enter` / `Esc` / `q` | Close | Close detail panel |

**Visual Feedback:**
- Selected attachment is highlighted with `▶` prefix and mauve color
- When no attachment is selected, `j` moves into attachment list
- When first attachment is selected, `k` deselects (exits attachment navigation)

### Adding Attachments (Image Attach Overlay)

Press `Space` `i` to open the image attachment overlay for the selected task.

| Key | Action | Description |
|-----|--------|-------------|
| `p` or `v` | Paste from clipboard | Attach image from system clipboard (macOS/Linux) |
| `f` | Enter file path mode | Type a file path to attach |
| `Esc` (or `q` in menu mode) | Close/back | Close overlay or exit path input mode |

### Path Input Mode

When in path input mode (after pressing `f`):

| Key | Action |
|-----|--------|
| Type | Add characters to path |
| `Backspace` | Delete last character |
| `Enter` | Attach file at path |
| `Esc` | Return to menu mode |
| `q` | Type `q` | Printable input in path mode (not a close shortcut) |

### Image Preview Overlay

Press `v` on a selected attachment to preview the image directly in the terminal:

| Key | Action | Description |
|-----|--------|-------------|
| `j` / `↓` | Next image | Navigate to next attachment |
| `k` / `↑` | Previous image | Navigate to previous attachment |
| `o` | Open | Open in system image viewer |
| `q` or `Esc` | Close | Close preview overlay |

**How Preview Works:**

The preview uses the `terminal-image` library which automatically selects the best rendering method for your terminal:

1. **Kitty/WezTerm**: Full-resolution graphics via Kitty protocol
2. **iTerm2**: Full-resolution inline images via iTerm2 protocol
3. **Other terminals**: Unicode half-blocks with 24-bit color (works everywhere)

Images are scaled to fit the terminal window while preserving aspect ratio.

### How Image Attachment Works

1. Images are stored in `.linear/images/{bead-id}/`
2. Metadata is tracked in `.linear/images/index.json`
3. Supported formats: PNG, JPG, GIF, WebP, BMP, SVG
4. Claude sessions can reference attached images for visual context

### Example Workflows

**Attaching an image:**
```
1. Navigate to a task
2. Press Space+i to open attachment overlay
3. Either:
   a. Copy an image to clipboard, then press 'p' to paste
   b. Press 'f', type "/path/to/screenshot.png", press Enter
4. Success toast confirms attachment
```

**Previewing an image in-terminal:**
```
1. Navigate to a task with attachments
2. Press Enter to open detail panel
3. Press j to select first attachment
4. Press v to preview (renders in terminal)
5. Press j/k to navigate through attachments
6. Press Esc or q to close preview
```

**Opening an attached image externally:**
```
1. Navigate to a task with attachments
2. Press Enter to open detail panel
3. Press j to select first attachment
4. Press o to open in system viewer
```

**Removing an attachment:**
```
1. Navigate to a task with attachments
2. Press Enter to open detail panel
3. Press j/k to select the attachment to remove
4. Press x to delete it
```

## Status Bar Indicators

The status bar at the bottom shows:

```
┌────────────────────────────────────────────────────────────┐
│ azedarach   ● connected   [NOR]   hjkl:nav  Space:act  ?:help  3 selected │
└────────────────────────────────────────────────────────────┘
              │              │
              │              └── Current mode
              └── Connection status (green = connected)
```

## Catppuccin Theme Colors

The UI uses the Catppuccin Mocha color palette:

- **Selected task**: Mauve background (`#cba6f7`)
- **Priority P1**: Red (`#f38ba8`)
- **Priority P2**: Yellow (`#f9e2af`)
- **Priority P3**: Green (`#a6e3a1`)
- **Priority P4**: Blue (`#89b4fa`)
- **Session indicators**:
  - 🔵 Busy
  - 🟡 Waiting
  - ✅ Done
  - ❌ Error
  - ⏸️ Paused

## tmux Navigation

Azedarach registers global tmux keybindings for session navigation:

| Key | Action | Notes |
|-----|--------|-------|
| `Ctrl-a g` | Return to az | Default "go board" bind from any AI session |
| `Ctrl-a Ctrl-a` | Send prefix | Native tmux behavior (not overridden by Az) |
| `Ctrl-a Tab` | Toggle Claude ↔ Dev Server | Optional custom bind (not auto-installed) |

Set `AZ_RETURN_KEY` to override the default return key if needed.

**Navigation flow:**
1. From az: `Space` `a` → attach to Claude session
2. From Claude: `Ctrl-a g` → return to az TUI
3. `Ctrl-a Ctrl-a` remains available as tmux send-prefix

This makes az the central hub for all session navigation.

## Multi-Project Support

Azedarach supports working with multiple linear-enabled projects. Each project has its own set of tasks (linear), and you can switch between them using the project selector.

### Project Management (CLI)

Use the CLI to manage registered projects:

```bash
# Register a project
az project add /path/to/project

# Register with a custom name
az project add /path/to/project --name my-project

# List registered projects
az project list

# Remove a project
az project remove project-name

# Set default project
az project switch project-name
```

### Project Selector (TUI)

Press `g` `p` to open the project selector overlay:

| Key | Action |
|-----|--------|
| `1`-`9` | Select project by number |
| `Esc` / `q` | Cancel and close |

The current project is highlighted with "(current)". When you switch projects:
1. The board refreshes to show tasks from the new project
2. All session operations (start, attach, etc.) use the new project's path
3. PR and merge operations target the new project's repository

### Auto-Detection

When launching Azedarach, it automatically selects a project based on:
1. **Current directory**: If you're inside a registered project's directory
2. **Default project**: Falls back to the configured default project
3. **First project**: Falls back to the first registered project

### Project Configuration

Projects are stored globally in `~/.config/azedarach/projects.json`:

```json
{
  "projects": [
    { "name": "azedarach", "path": "/Users/name/prog/azedarach" },
    { "name": "other-project", "path": "/Users/name/work/other" }
  ],
  "defaultProject": "azedarach"
}
```

## Tips

1. **Stay on home row**: All primary keys (hjkl, g, v, Space) are accessible without moving your hands.

2. **Use jump labels for large boards**: `gw` + 2 chars is faster than repeated hjkl navigation.

3. **Batch moves with Select mode**: Select multiple related tasks, then move them together.

4. **Quick column jumps**: `gh` and `gl` jump between first and last columns; `gg` and `ge` jump to top/bottom of current column.

5. **Half-page scrolling**: `Ctrl-d` and `Ctrl-u` are great for tall columns.
