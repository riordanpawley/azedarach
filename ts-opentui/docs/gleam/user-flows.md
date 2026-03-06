# User Flows

## 1. Starting Fresh Session

**Goal:** Start working on an issue from scratch

```
┌─────────────────────────────────────────────────────────────────────────────┐
│ USER FLOW: Start Fresh Session                                               │
├─────────────────────────────────────────────────────────────────────────────┤
│                                                                              │
│  1. Navigate to task                                                         │
│     └── hjkl to move cursor to desired bead                                 │
│                                                                              │
│  2. Press Space to open Action Menu                                          │
│     ┌─────────────────────────────┐                                         │
│     │ Session                     │                                         │
│     │ ├── s  Start session        │ ← simple start                          │
│     │ ├── S  Start+work           │ ← with context prompt                   │
│     │ └── !  Start yolo           │ ← skip permissions                      │
│     └─────────────────────────────┘                                         │
│                                                                              │
│  3a. Press 's' (simple start)                                                │
│      └── Creates worktree, session, runs init, starts Claude                │
│      └── Card shows: BUSY indicator                                         │
│                                                                              │
│  3b. Press 'S' (start+work)                                                  │
│      └── Same as above but Claude gets prompt:                              │
│          "work on issue az-123 (task): Fix login bug                        │
│           Run `az issue get az-123` to see full description...               │
│           Before starting: ASK ME if anything unclear..."                   │
│                                                                              │
│  4. Claude starts working                                                    │
│     └── Monitor polls output every 500ms                                    │
│     └── State updates: BUSY → WAITING → DONE                                │
│                                                                              │
│  5. When Claude waits (WAITING state)                                        │
│     └── Press 'a' to attach and provide input                               │
│                                                                              │
└─────────────────────────────────────────────────────────────────────────────┘
```

## 2. Resuming Work

**Goal:** Continue work on an existing session

```
┌─────────────────────────────────────────────────────────────────────────────┐
│ USER FLOW: Resume Work                                                       │
├─────────────────────────────────────────────────────────────────────────────┤
│                                                                              │
│  1. Navigate to task with existing session                                   │
│     └── Card shows session indicator (🔵 BUSY, 🟡 WAITING, etc.)             │
│                                                                              │
│  2. Press Space → 'a' to attach                                              │
│                                                                              │
│  3. If branch is behind main:                                                │
│     ┌─────────────────────────────────────┐                                 │
│     │ ↓ Branch Behind main                │                                 │
│     │                                     │                                 │
│     │ 3 commits behind                    │                                 │
│     │                                     │                                 │
│     │ m: Merge & Attach                   │                                 │
│     │ s: Skip & Attach                    │                                 │
│     │ Esc: Cancel                         │                                 │
│     └─────────────────────────────────────┘                                 │
│                                                                              │
│  4a. Press 'm' to merge first                                                │
│      └── If conflicts: Claude spawns in "merge" window to resolve           │
│      └── If clean: merge happens, then attach                               │
│                                                                              │
│  4b. Press 's' to skip and attach directly                                   │
│                                                                              │
│  5. Now in tmux session                                                      │
│     └── Ctrl+a Ctrl+a to return to Azedarach                                │
│                                                                              │
└─────────────────────────────────────────────────────────────────────────────┘
```

## 3. Dev Server Workflow

**Goal:** Start dev server for an issue

```
┌─────────────────────────────────────────────────────────────────────────────┐
│ USER FLOW: Dev Server                                                        │
├─────────────────────────────────────────────────────────────────────────────┤
│                                                                              │
│  1. Navigate to task                                                         │
│                                                                              │
│  2. Press Space → 'r' to toggle dev server                                   │
│     └── If no session: creates worktree, session, runs init                 │
│     └── Creates window: dev-{server-name}                                   │
│     └── Sets PORT env var, runs command                                     │
│                                                                              │
│  3. Status bar shows: DEV: localhost:3000                                    │
│                                                                              │
│  4. To view dev server:                                                      │
│     └── Space → 'v' to attach to dev window                                 │
│                                                                              │
│  5. To restart:                                                              │
│     └── Space → Ctrl+r                                                      │
│                                                                              │
│  6. To stop:                                                                 │
│     └── Space → 'r' again (toggle)                                          │
│                                                                              │
│  Multiple servers:                                                           │
│  └── Each gets own window: dev-web, dev-api, etc.                           │
│  └── Ports auto-allocated: 3000, 3001, etc.                                 │
│                                                                              │
└─────────────────────────────────────────────────────────────────────────────┘
```

## 4. Creating PR

**Goal:** Create GitHub PR after completing work

```
┌─────────────────────────────────────────────────────────────────────────────┐
│ USER FLOW: Create PR                                                         │
├─────────────────────────────────────────────────────────────────────────────┤
│                                                                              │
│  1. Complete work (session shows DONE)                                       │
│                                                                              │
│  2. Update from main first:                                                  │
│     └── Space → 'u' (update from main)                                      │
│     └── Merges latest main into branch                                      │
│     └── Resolves conflicts if any                                           │
│                                                                              │
│  3. Review changes:                                                          │
│     └── Space → 'f' to show diff                                            │
│                                                                              │
│  4. Create PR:                                                               │
│     └── Space → Shift+p                                                     │
│     └── Runs: gh pr create --draft                                          │
│     └── Toast: "PR created: #123"                                           │
│                                                                              │
│  5. After PR merged (externally):                                            │
│     └── Space → 'd' to cleanup                                              │
│     └── Deletes: worktree, branch, session                                  │
│                                                                              │
└─────────────────────────────────────────────────────────────────────────────┘
```

## 5. Local Merge Workflow

**Goal:** Merge directly to main without PR

```
┌─────────────────────────────────────────────────────────────────────────────┐
│ USER FLOW: Local Merge                                                       │
├─────────────────────────────────────────────────────────────────────────────┤
│                                                                              │
│  1. Complete work                                                            │
│                                                                              │
│  2. Merge to main:                                                           │
│     └── Space → 'm'                                                         │
│     └── Checks for conflicts (merge-tree)                                   │
│                                                                              │
│  3a. No conflicts:                                                           │
│      └── Merges in worktree                                                 │
│      └── Toast: "Merged to main"                                            │
│      └── Worktree stays active (can iterate)                                │
│                                                                              │
│  3b. Conflicts detected:                                                     │
│      ┌─────────────────────────────────────┐                                │
│      │ Conflicts detected in:              │                                │
│      │ - src/login.ts                      │                                │
│      │ - src/auth.ts                       │                                │
│      │                                     │                                │
│      │ Claude started in 'merge' window    │                                │
│      │ to resolve. Retry after resolution. │                                │
│      └─────────────────────────────────────┘                                │
│                                                                              │
│  4. After successful merge, cleanup:                                         │
│     └── Space → 'd'                                                         │
│                                                                              │
└─────────────────────────────────────────────────────────────────────────────┘
```

## 6. Image Attachment

**Goal:** Attach image to bead for context

```
┌─────────────────────────────────────────────────────────────────────────────┐
│ USER FLOW: Attach Image                                                      │
├─────────────────────────────────────────────────────────────────────────────┤
│                                                                              │
│  1. Open detail panel:                                                       │
│     └── Enter on task                                                       │
│                                                                              │
│  2. Press 'i' to attach image                                                │
│     ┌─────────────────────────────────────┐                                 │
│     │ Attach Image                        │                                 │
│     │                                     │                                 │
│     │ p/v: Paste from clipboard           │                                 │
│     │ f: Enter file path                  │                                 │
│     │ Esc: Cancel                         │                                 │
│     └─────────────────────────────────────┘                                 │
│                                                                              │
│  3a. Press 'p' to paste from clipboard                                       │
│      └── Uses: pbpaste (mac) / wl-paste (wayland) / xclip (x11)             │
│      └── Saves to: .linear/images/{bead-id}/{id}.png                         │
│      └── Adds markdown link to bead notes                                   │
│                                                                              │
│  3b. Press 'f' then type path                                                │
│      └── /path/to/image.png                                                 │
│      └── Copies to .linear/images/                                           │
│                                                                              │
│  4. Image appears in attachment list                                         │
│     └── j/k to navigate                                                     │
│     └── v to preview                                                        │
│     └── o to open in viewer                                                 │
│     └── x to delete                                                         │
│                                                                              │
│  5. When starting session (Space+S), image paths included:                   │
│     "Attached images (use Read tool to view):                               │
│      /path/to/.linear/images/az-123/abc123.png"                              │
│                                                                              │
└─────────────────────────────────────────────────────────────────────────────┘
```

## 7. Filtering & Searching

**Goal:** Find specific tasks

```
┌─────────────────────────────────────────────────────────────────────────────┐
│ USER FLOW: Filter & Search                                                   │
├─────────────────────────────────────────────────────────────────────────────┤
│                                                                              │
│  SEARCH (quick filter by text):                                              │
│  └── Press '/' to enter search mode                                         │
│  └── Type query (filters title and ID)                                      │
│  └── Enter to confirm, Esc to clear                                         │
│                                                                              │
│  FILTER (by properties):                                                     │
│  └── Press 'f' to open filter menu                                          │
│      ┌─────────────────────────────────────┐                                │
│      │ Filter                              │                                │
│      │                                     │                                │
│      │ s: Status   [o] open [b] blocked    │                                │
│      │ p: Priority [1] [2] [3] [4]         │                                │
│      │ t: Type     [T] task [B] bug        │                                │
│      │ S: Session  [U] busy [W] waiting    │                                │
│      │                                     │                                │
│      │ e: Toggle hide epic children        │                                │
│      │ c: Clear all filters                │                                │
│      └─────────────────────────────────────┘                                │
│                                                                              │
│  SORT:                                                                       │
│  └── Press ',' to open sort menu                                            │
│      ┌─────────────────────────────────────┐                                │
│      │ Sort                                │                                │
│      │                                     │                                │
│      │ s: Session status (busy first)      │                                │
│      │ p: Priority (P1 first)              │                                │
│      │ u: Updated (recent first)           │                                │
│      └─────────────────────────────────────┘                                │
│                                                                              │
└─────────────────────────────────────────────────────────────────────────────┘
```

## 8. Multi-Project

**Goal:** Switch between projects

```
┌─────────────────────────────────────────────────────────────────────────────┐
│ USER FLOW: Project Switching                                                 │
├─────────────────────────────────────────────────────────────────────────────┤
│                                                                              │
│  1. Press 'g' then 'p' to open project selector                              │
│     ┌─────────────────────────────────────┐                                 │
│     │ Select Project                      │                                 │
│     │                                     │                                 │
│     │ 1. azedarach        /home/user/az   │ ← current                       │
│     │ 2. linear            /home/user/az   │                                 │
│     │ 3. my-app           /home/user/app  │                                 │
│     │                                     │                                 │
│     │ Press 1-9 to select                 │                                 │
│     └─────────────────────────────────────┘                                 │
│                                                                              │
│  2. Press number to switch                                                   │
│     └── Loads project config                                                │
│     └── Refreshes linear                                                     │
│     └── Discovers existing sessions                                         │
│                                                                              │
│  3. Status bar shows current project                                         │
│                                                                              │
└─────────────────────────────────────────────────────────────────────────────┘
```

## 9. Pause & Resume

**Goal:** Temporarily stop and continue later

```
┌─────────────────────────────────────────────────────────────────────────────┐
│ USER FLOW: Pause & Resume                                                    │
├─────────────────────────────────────────────────────────────────────────────┤
│                                                                              │
│  PAUSE:                                                                      │
│  1. Navigate to running session (BUSY state)                                 │
│  2. Space → 'p' to pause                                                    │
│     └── Sends Ctrl+C to Claude                                              │
│     └── Creates WIP commit: "wip: paused session"                           │
│     └── Card shows: PAUSED state                                            │
│                                                                              │
│  RESUME:                                                                     │
│  1. Navigate to paused session                                               │
│  2. Space → Shift+r to resume                                               │
│     └── Restarts Claude in session                                          │
│     └── Card shows: BUSY state                                              │
│                                                                              │
└─────────────────────────────────────────────────────────────────────────────┘
```

## 10. Planning Workflow

**Goal:** Use AI to plan complex features and auto-create linear

```
┌─────────────────────────────────────────────────────────────────────────────┐
│ USER FLOW: Planning Workflow                                                 │
├─────────────────────────────────────────────────────────────────────────────┤
│                                                                              │
│  1. Press 'p' to open planning overlay                                       │
│     ┌─────────────────────────────────────────────────────────────────────┐ │
│     │ Planning                                                             │ │
│     │                                                                      │ │
│     │ Describe the feature you want to build:                              │ │
│     │ ┌──────────────────────────────────────────────────────────────────┐│ │
│     │ │ Add user authentication with OAuth and JWT tokens               ││ │
│     │ └──────────────────────────────────────────────────────────────────┘│ │
│     │                                                                      │ │
│     │ Enter: Start planning                                                │ │
│     │ Esc: Cancel                                                          │ │
│     └─────────────────────────────────────────────────────────────────────┘ │
│                                                                              │
│  2. Press Enter to submit                                                    │
│     └── Claude Code starts in tmux session "az-planning"                    │
│     └── Overlay shows: "Generating plan..."                                 │
│                                                                              │
│  3. Planning phases (automatic):                                             │
│                                                                              │
│     ┌───────────────────────────────────────────────────────────────────┐   │
│     │ Phase 1: GENERATE                                                  │   │
│     │ Claude analyzes your description and creates initial plan:         │   │
│     │ - Identifies distinct tasks                                        │   │
│     │ - Determines task types (epic, task, bug, feature, chore)          │   │
│     │ - Sets priorities (P1-P4)                                          │   │
│     │ - Creates dependency graph                                         │   │
│     └───────────────────────────────────────────────────────────────────┘   │
│                              ↓                                               │
│     ┌───────────────────────────────────────────────────────────────────┐   │
│     │ Phase 2: REVIEW (up to 5 passes)                                   │   │
│     │ Claude reviews and refines the plan:                               │   │
│     │ - Are tasks small enough? (break down if needed)                   │   │
│     │ - Are dependencies correct? (sequential vs parallel)              │   │
│     │ - Is the epic structure right?                                     │   │
│     │ - Missing edge cases or tests?                                     │   │
│     │                                                                    │   │
│     │ Overlay shows: "Reviewing plan... (pass 2/5)"                      │   │
│     └───────────────────────────────────────────────────────────────────┘   │
│                              ↓                                               │
│     ┌───────────────────────────────────────────────────────────────────┐   │
│     │ Phase 3: CREATE BEADS                                              │   │
│     │ Once approved, creates linear with az CLI:                          │   │
│     │ - Epic for the main feature                                        │   │
│     │ - Child tasks linked to epic (parent-child deps)                   │   │
│     │ - Blocking dependencies between sequential tasks                   │   │
│     │ - Proper metadata (type, priority, description)                    │   │
│     └───────────────────────────────────────────────────────────────────┘   │
│                                                                              │
│  4. Planning complete                                                        │
│     ┌─────────────────────────────────────────────────────────────────────┐ │
│     │ Planning Complete                                                    │ │
│     │                                                                      │ │
│     │ Created 5 linear:                                                     │ │
│     │   [epic]    az-abc  Add user authentication                          │ │
│     │   [task]    az-def  Set up OAuth providers                           │ │
│     │   [task]    az-ghi  Implement JWT token flow                         │ │
│     │   [task]    az-jkl  Create login/signup UI                           │ │
│     │   [task]    az-mno  Add auth middleware                              │ │
│     │                                                                      │ │
│     │ Press Enter or Esc to close                                          │ │
│     └─────────────────────────────────────────────────────────────────────┘ │
│                                                                              │
│  5. Start working                                                            │
│     └── Navigate to epic: Enter to drill-down and see children             │
│     └── Navigate to tasks: Space+s on each to start parallel sessions      │
│     └── Dependencies ensure proper order for blocked tasks                  │
│                                                                              │
└─────────────────────────────────────────────────────────────────────────────┘
```

### Planning Overlay Keybindings

| Key | Action | When |
|-----|--------|------|
| `Enter` | Submit description | In input mode |
| `Esc` | Cancel / Close | Any phase |
| `a` | Attach to session | During generation/review |
| `Enter` / `q` | Close | After completion |

### Debugging the Planning Session

Press `a` during generation or review to attach to the planning tmux session. This is useful for:
- Seeing what Claude is generating in real-time
- Manually guiding the planning if needed
- Debugging if planning gets stuck

### Example Use Cases

**Feature with clear subtasks:**
```
"Add dark mode with user preference persistence"
→ Creates: epic + toggle component + theme context + localStorage + CSS variables tasks
```

**Bug fix with investigation:**
```
"Users report slow load times on dashboard"
→ Creates: epic + profiling task + optimization tasks based on findings
```

**Infrastructure work:**
```
"Set up CI/CD pipeline with GitHub Actions"
→ Creates: epic + build workflow + test workflow + deploy workflow tasks
```

## 11. Cleanup

**Goal:** Remove worktree, branch, session after work complete

```
┌─────────────────────────────────────────────────────────────────────────────┐
│ USER FLOW: Cleanup                                                           │
├─────────────────────────────────────────────────────────────────────────────┤
│                                                                              │
│  1. Navigate to completed task                                               │
│                                                                              │
│  2. Space → 'd' to delete/cleanup                                           │
│     ┌─────────────────────────────────────┐                                 │
│     │ Cleanup az-123?                     │                                 │
│     │                                     │                                 │
│     │ This will:                          │                                 │
│     │ • Kill tmux session                 │                                 │
│     │ • Delete worktree                   │                                 │
│     │ • Delete remote branch              │                                 │
│     │ • Delete local branch               │                                 │
│     │                                     │                                 │
│     │ y: Confirm                          │                                 │
│     │ n/Esc: Cancel                       │                                 │
│     └─────────────────────────────────────┘                                 │
│                                                                              │
│  3. Press 'y' to confirm                                                     │
│     └── All resources cleaned up                                            │
│     └── Bead stays in linear (can close separately)                          │
│                                                                              │
└─────────────────────────────────────────────────────────────────────────────┘
```
