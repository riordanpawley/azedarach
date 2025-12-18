# Keybinding Reference

Azedarach uses **Helix-style modal keybindings** inspired by the Helix editor. This provides efficient, ergonomic navigation without leaving the home row.

## Mode Overview

```
┌────────────────────────────────────────────────────────────────────────┐
│                       NORMAL MODE (NOR)                                │
│  hjkl: navigate  g: goto  v: select  Space: act  /: search  ,: sort  :: cmd    │
└────────────────────────────────────────────────────────────────────────┘
         │           │           │           │           │           │           │
         │           ▼           ▼           ▼           ▼           ▼           ▼
         │   ┌────────────┐ ┌──────────┐ ┌──────────┐ ┌──────────┐ ┌──────────┐ ┌──────────┐
         │   │ GOTO (GTO) │ │ SELECT   │ │ ACTION   │ │ SEARCH   │ │ SORT     │ │ COMMAND  │
         │   │ gg/ge/gl   │ │ (SEL)    │ │ (ACT)    │ │ (SRC)    │ │ (SRT)    │ │ (CMD)    │
         │   │ gw: labels │ │ Space:   │ │ h/l:move │ │ filter   │ │ s/p/u:   │ │ send to  │
         │   └────────────┘ │ toggle   │ │ a:attach │ │ by title │ │ sort by  │ │ VC REPL  │
         │         │        └──────────┘ └──────────┘ └──────────┘ └──────────┘ └──────────┘
         │         │             │           │             │            │            │
         └─────────┴─────────────┴───────────┴─────────────┴────────────┴────────────┘
                              Esc: return to Normal
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
| `Ctrl-Shift-d` | Half-page down | Fast scrolling |
| `Ctrl-Shift-u` | Half-page up | Fast scrolling |

### Actions

| Key | Action | Notes |
|-----|--------|-------|
| `Enter` | Show task details | Modal overlay |
| `Space` | Enter Action mode | Prefix for commands |
| `,` | Enter Sort mode | Change task sort order |
| `/` | Enter Search mode | Filter tasks by title/ID |
| `:` | Enter Command mode | Send commands to VC REPL |
| `g` | Enter Goto mode | Prefix for jumps |
| `v` | Enter Select mode | Multi-selection |
| `Tab` | Toggle view mode | Switch between Kanban and Compact views |
| `c` | Create bead (manual) | Opens $EDITOR with template |
| `C` | Create via Claude | Natural language task creation |
| `a` | Toggle VC auto-pilot | Start/stop VC executor |
| `?` | Show help | Press any key to dismiss |
| `L` | View logs | Opens az.log in tmux popup |
| `q` | Quit | Exit application |
| `Esc` | Dismiss overlay | Or return from sub-mode |

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

Azedarach provides both manual (via $EDITOR) and AI-assisted (via Claude) modes for creating and editing beads.

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
4. The bead is created via `bd create`

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
5. Changes are applied via `bd update`

## Claude Create Mode (`C`)

Press `C` (capital C) to create a task using natural language. This spawns a Claude session that interprets your description and creates the appropriate bead.

### How It Works

1. Press `C` to open the Claude Create prompt
2. Type a natural language description of what you want to do
3. Press `Enter` to launch a Claude session
4. Claude will:
   - Interpret your description
   - Create a bead with appropriate title, type, and description using `bd create`
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
3. Claude receives the bead's current details and `bd update` syntax
4. Describe what changes you want in natural language
5. Claude will help update the bead using `bd update`

### Example

1. Select a task
2. Press `E`
3. Claude shows you the bead details and asks what you'd like to change
4. Type: `Change the priority to P1 and add a note about the deadline`
5. Claude runs the appropriate `bd update` command

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

## Select Mode

Press `v` to enter select mode for multi-task operations.

| Key | Action | Notes |
|-----|--------|-------|
| `Space` | Toggle selection | Add/remove current task |
| `h/j/k/l` | Navigate | Selections persist |
| `Esc` | Exit + clear | Clears all selections |

### Visual Feedback

Selected tasks are highlighted with a different background color. The status bar shows the selection count.

## Search Mode

Press `/` to enter search mode for filtering tasks.

| Key | Action | Notes |
|-----|--------|-------|
| `Enter` | Confirm search | Keep filter active, return to Normal |
| `Esc` | Clear search | Remove filter, return to Normal |
| `Backspace` | Delete character | Remove last character from query |
| Any char | Add to query | Case-insensitive search |

### How Search Works

- **Live filtering**: Tasks are filtered as you type
- **Matches**: Title and task ID are searched
- **Case-insensitive**: "Fix" matches "fix", "FIX", etc.
- **Persistent filter**: After pressing Enter, the filter stays active until you press `/` then `Esc` to clear it
- **Visual indicator**: Status bar shows "filter: <query>" when a filter is active

### Example

1. Press `/` to enter search mode
2. Type `auth` to filter tasks containing "auth"
3. Press `Enter` to confirm and return to normal mode
4. Navigate filtered results with hjkl
5. Press `/` then `Esc` to clear the filter

## Command Mode

Press `:` to enter command mode for sending commands to the VC REPL.

| Key | Action | Notes |
|-----|--------|-------|
| `Enter` | Send command | Sends command to VC, returns to Normal |
| `Esc` | Cancel | Clear input, return to Normal |
| `Backspace` | Delete character | Remove last character from input |
| Any char | Add to input | Build command to send |

### How Command Mode Works

- **VC must be running**: The VC auto-pilot must be active (toggle with `a` key)
- **Natural language**: Commands are conversational (e.g., "What's ready to work on?")
- **Sent to REPL**: The command is sent directly to the VC tmux session
- **Feedback**: A toast notification confirms when the command is sent
- **Error handling**: Shows error if VC is not running

### Example

1. Press `a` to start VC auto-pilot (if not already running)
2. Press `:` to enter command mode
3. Type `Let's continue working`
4. Press `Enter` to send the command to VC
5. VC will process the command in its REPL session

### Common Commands

- `What's ready to work on?` - Ask VC for available tasks
- `Let's continue working` - Resume work on current task
- `Add Docker support` - Request a new feature
- `Run tests` - Ask VC to run tests

## Sort Mode

Press `,` to enter sort mode for changing how tasks are ordered within each column.

| Key | Action | Description |
|-----|--------|-------------|
| `s` | Sort by Session | Active sessions first (busy, waiting, paused, then idle) |
| `p` | Sort by Priority | Higher priority tasks first (P1 > P2 > P3 > P4) |
| `u` | Sort by Updated | Most recently updated tasks first |
| `Esc` | Cancel | Exit sort mode without changing |

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

## Action Mode

Press `Space` in Normal mode to enter action mode. A floating palette shows available actions.

### Session Actions

| Sequence | Action | Available When |
|----------|--------|----------------|
| `Space` `s` | Start session | Task is idle (creates worktree + tmux) |
| `Space` `S` | Start+work | Task is idle (starts session with "work on {beadId}" prompt) |
| `Space` `!` | Start (yolo) | Task is idle (like S but with --dangerously-skip-permissions) |
| `Space` `c` | Chat (Haiku) | Always (opens Haiku in tmux popup to discuss task) |
| `Space` `a` | Attach to session | Session exists (switches tmux client) |
| `Space` `p` | Pause session | Session is busy (Ctrl-C + WIP commit) |
| `Space` `r` | Resume session | Session is paused |
| `Space` `x` | Stop session | Session exists (kills tmux) |

#### Start (yolo) Mode (Space+!)

The "yolo" start mode (`Space` `!`) launches Claude with the `--dangerously-skip-permissions` flag. This allows Claude to run commands and edit files without asking for permission on each operation.

**Use cases:**
- Trusted, well-defined tasks where you want Claude to work autonomously
- Tasks with clear scope that don't require manual review of each step
- When you're ready to accept all changes Claude makes

**Caution:** Since Claude won't ask for permission, it can make changes faster but with less oversight. Use this for tasks where you trust Claude's judgment.

### Git/PR Actions

| Sequence | Action | Available When |
|----------|--------|----------------|
| `Space` `P` | Create PR | Worktree exists (push + gh pr create) |
| `Space` `m` | Merge to main | Worktree exists (merge branch to main) |
| `Space` `M` | Abort merge | Worktree exists (abort stuck merge) |
| `Space` `d` | Delete worktree | Worktree exists (cleanup branches) |

#### Merge to Main (Space+m)

The merge action includes **conflict detection**:

1. Before merging, az checks if files were modified in both your branch and main
2. If potential conflicts are detected, a **confirmation dialog** appears:
   - Shows which files might conflict
   - Explains that the merge is tested in the worktree first (main isn't affected if it fails)
   - Press `y` to proceed, `n` to cancel
3. If no conflicts detected, the merge proceeds directly
4. On success, the branch changes are merged into main locally

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

### Movement Actions

| Sequence | Action | Description |
|----------|--------|-------------|
| `Space` `h` | Move left | Move task(s) to previous column |
| `Space` `l` | Move right | Move task(s) to next column |

### Edit Actions

| Sequence | Action | Description |
|----------|--------|-------------|
| `Space` `e` | Edit bead (manual) | Opens in $EDITOR as markdown |
| `Space` `E` | Edit bead (Claude) | AI-assisted editing |

### Attachment Actions

| Sequence | Action | Description |
|----------|--------|-------------|
| `Space` `i` | Attach image | Open image attachment overlay (paste or file path) |

### Other Actions

| Sequence | Action | Description |
|----------|--------|-------------|
| `Esc` | Cancel | Exit action mode |

### Batch Operations

If you have tasks selected (from Select mode), Action mode commands apply to all selected tasks:

1. Press `v` to enter Select mode
2. Navigate and press `Space` to select multiple tasks
3. Press `Esc` to return to Normal (selections persist)
4. Press `Space` `l` to move all selected tasks right

## Image Attachments

Tasks can have images attached to provide visual context for Claude sessions.

### Viewing & Managing Attachments (Detail Panel)

When viewing a task's details (`Enter`), if it has attachments, you can navigate and manage them:

| Key | Action | Description |
|-----|--------|-------------|
| `j` / `↓` | Select next | Move selection to next attachment |
| `k` / `↑` | Select previous | Move selection to previous attachment (or deselect) |
| `o` | Open | Open selected attachment in system image viewer |
| `x` | Remove | Delete selected attachment |
| `i` | Add | Open image attachment overlay to add more |
| `Esc` | Close | Close detail panel |

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
| `Esc` | Close/back | Close overlay or exit path input mode |

### Path Input Mode

When in path input mode (after pressing `f`):

| Key | Action |
|-----|--------|
| Type | Add characters to path |
| `Backspace` | Delete last character |
| `Enter` | Attach file at path |
| `Esc` | Return to menu mode |

### How Image Attachment Works

1. Images are stored in `.beads/images/{bead-id}/`
2. Metadata is tracked in `.beads/images/index.json`
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

**Viewing an attached image:**
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

Azedarach registers a global tmux keybinding for session navigation:

| Key | Action | Notes |
|-----|--------|-------|
| `Ctrl-a Ctrl-a` | Return to az | Double-tap prefix from any Claude session |

**Navigation flow:**
1. From az: `Space` `a` → attach to Claude session
2. From Claude: `Ctrl-a Ctrl-a` → return to az TUI (double-tap prefix)

This makes az the central hub for all session navigation.

## Multi-Project Support

Azedarach supports working with multiple beads-enabled projects. Each project has its own set of tasks (beads), and you can switch between them using the project selector.

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
| `Esc` | Cancel and close |

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

5. **Half-page scrolling**: `Ctrl-Shift-d` and `Ctrl-Shift-u` are great for tall columns.
