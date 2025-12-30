# Implementation Phases

## Phase Dependencies Graph

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

---

## Phase 1: Core Framework

**Goal**: Basic TEA loop with navigation

**Deliverables**:
- [ ] Project setup (go.mod, Makefile, .goreleaser.yaml)
- [ ] Main model struct with cursor, mode, basic state
- [ ] Basic keybinding handling (hjkl + arrows navigation)
- [ ] Half-page scroll (`Ctrl-Shift-d/u`)
- [ ] Force redraw (`Ctrl-l`)
- [ ] Static 4-column Kanban board rendering
- [ ] Lip Gloss theme (Catppuccin Macchiato)
- [ ] StatusBar with mode indicator + keybinding hints
- [ ] Quit (`q`, `Ctrl-c`)

**Acceptance Criteria**:
- `go build` produces working binary
- Navigate between columns with h/l
- Navigate within columns with j/k
- StatusBar shows current mode
- Half-page scroll works in tall columns

**Dependencies**: None

**Testing**:
- Unit: None (pure UI)
- Golden: Board rendering snapshots
- Manual: Navigation feel

---

## Phase 2: Beads Integration

**Goal**: Load and display real bead data

**Deliverables**:
- [ ] Domain types (Task, Session, DevServer, Project)
- [ ] Beads CLI client (list, search, ready, create, update, close)
- [ ] JSON parsing with proper error handling
- [ ] Async loading with `tea.Cmd`
- [ ] Task cards with status/priority/type badges
- [ ] Elapsed timer on cards (session duration)
- [ ] Periodic refresh (tea.Tick every 2s)
- [ ] Toast notifications (info, success, warning, error)
- [ ] Loading spinner during initial load
- [ ] Connection status indicator placeholder

**Acceptance Criteria**:
- Board shows real beads from `bd list`
- Cards show correct status colors
- Priority badges (P0-P4) visible
- Toasts appear and auto-dismiss
- Periodic refresh doesn't flicker

**Dependencies**: Phase 1

**Testing**:
- Unit: Beads client parsing, filter logic
- Golden: Card rendering with various states
- Integration: Mock `bd` CLI responses

---

## Phase 3: Overlays & Filters

**Goal**: Modal overlays and filtering

**Deliverables**:
- [ ] Overlay stack system (push/pop)
- [ ] Action menu (`Space`) with available actions
- [ ] Filter menu (`f`) with sub-menus:
  - Status (`f` `s`): o/i/b/d toggles
  - Priority (`f` `p`): 0-4 toggles
  - Type (`f` `t`): B/F/T/E/C toggles
  - Session (`f` `S`): I/U/W/D/X/P toggles
  - Hide epic children (`f` `e`)
  - Age filter (`f` `1/7/3/0`)
  - Clear all (`f` `c`)
- [ ] Sort menu (`,`):
  - Sort by session (`,` `s`)
  - Sort by priority (`,` `p`)
  - Sort by updated (`,` `u`)
  - Toggle direction (repeat key)
- [ ] Help overlay (`?`)
- [ ] Search input (`/`) with live filtering
- [ ] Select mode (`v`) with visual highlighting
- [ ] Select all (`%`) and clear (`A`)
- [ ] Goto mode (`g`):
  - Column top (`g` `g`)
  - Column bottom (`g` `e`)
  - First/last column (`g` `h`/`l`)
- [ ] Compact/list view toggle (`Tab`)
- [ ] Move task left/right (`Space` `h/l`)
- [ ] StatusBar selection count

**Acceptance Criteria**:
- All overlays render centered with proper styling
- Filter combinations work (AND between types, OR within)
- Search is case-insensitive, matches title + ID
- Selected tasks visually distinct
- Compact view shows all tasks in priority order

**Dependencies**: Phase 2

**Testing**:
- Unit: Filter logic, sort comparisons
- Golden: All overlay renderings
- Manual: Mode transitions feel snappy

---

## Phase 4: Session Management

**Goal**: Spawn and manage Claude sessions

**Deliverables**:
- [ ] tmux client (new-session, attach, send-keys, capture-pane, kill-session)
- [ ] Worktree management (create, delete, list)
- [ ] Session state detection (polling + pattern matching)
- [ ] Session actions:
  - Start session (`Space` `s`)
  - Start + work (`Space` `S`)
  - Start yolo (`Space` `!`)
  - Attach (`Space` `a`)
  - Pause (`Space` `p`)
  - Resume (`Space` `R`)
  - Stop (`Space` `x`)
- [ ] Dev server management:
  - Toggle (`Space` `r`)
  - View (`Space` `v`)
  - Restart (`Space` `Ctrl+r`)
  - Port allocation with conflict resolution
  - StatusBar port indicator
  - Dev server menu overlay
- [ ] Delete worktree/cleanup (`Space` `d`)
- [ ] Confirm dialog for destructive actions
- [ ] Bulk cleanup dialog (worktrees only / full)
- [ ] Bulk stop sessions
- [ ] Register tmux global bindings:
  - `Ctrl-a Ctrl-a`: Return to az
  - `Ctrl-a Tab`: Toggle Claude/Dev
- [ ] Session monitor goroutine (500ms polling)

**Acceptance Criteria**:
- `Space+s` creates worktree + tmux session + launches claude
- Session state updates within 1s of change
- `Space+a` attaches to correct session
- Port allocation avoids conflicts
- Cleanup removes worktree and closes bead (if selected)

**Dependencies**: Phase 3 (for confirm dialog, bulk operations)

**Testing**:
- Unit: State pattern matching, port allocation
- Integration: Mock tmux/git commands
- Manual: Full session lifecycle

---

## Phase 5: Git Operations

**Goal**: Git workflow support

**Deliverables**:
- [ ] Git client (status, fetch, merge, diff, branch, push)
- [ ] Update from main (`Space` `u`)
- [ ] Merge to main (`Space` `m`) with conflict detection
- [ ] Create PR (`Space` `P`) via `gh` CLI
- [ ] Show diff with difftastic (`Space` `f`)
- [ ] Abort merge (`Space` `M`)
- [ ] Merge bead into... (`Space` `b`) with merge select mode
- [ ] Refresh git stats (`r`)
- [ ] Merge choice dialog
- [ ] Network status detection
- [ ] Offline mode / graceful degradation
- [ ] Connection status in StatusBar

**Acceptance Criteria**:
- Merge detects conflicts and shows affected files
- Conflict resolution starts Claude session automatically
- PR creation syncs with main first
- Diff viewer shows side-by-side difftastic output
- Offline mode disables push/fetch gracefully

**Dependencies**: Phase 4 (session management for conflict resolution)

**Testing**:
- Unit: Git output parsing
- Integration: Mock git/gh commands
- Manual: Full merge workflow

---

## Phase 6: Advanced Features

**Goal**: Full feature parity

**Deliverables**:
- [ ] Epic drill-down view:
  - Enter on epic shows only children
  - Progress bar (closed/total)
  - Back navigation (`q`/`Esc`)
- [ ] Jump labels (`g` `w`):
  - 2-char labels from home row
  - Type label to jump
- [ ] Multi-project support:
  - Project selector overlay (`g` `p`)
  - Project auto-detection from cwd
  - CLI: `az project add/list/remove/switch`
- [ ] Create/edit beads:
  - Manual create (`c`) via $EDITOR
  - Claude create (`C`) with prompt overlay
  - Manual edit (`Space` `e`)
  - Claude edit (`Space` `E`)
- [ ] Open Helix editor (`Space` `H`)
- [ ] Chat with Haiku (`Space` `c`)
- [ ] Image attachments:
  - Attach overlay (`Space` `i`)
  - Paste from clipboard (`p`/`v`)
  - Attach from file path (`f`)
  - Preview in terminal
  - Open in external viewer (`o`)
  - Navigate/delete in detail panel
- [ ] Detail panel:
  - Scrollable description
  - Attachment list
  - Edit actions
- [ ] Settings overlay (`s`):
  - All toggleable settings
  - Edit in $EDITOR option
- [ ] Diagnostics overlay
- [ ] Logs viewer (`L`)
- [ ] Planning workflow integration

**Acceptance Criteria**:
- Epic drill-down filters to children only
- Jump labels work across visible tasks
- Project switch refreshes board
- Image preview works in major terminals
- Settings persist to `.azedarach.json`

**Dependencies**: All previous phases

**Testing**:
- Unit: Jump label generation, image detection
- Golden: Epic header, settings overlay
- Manual: Full workflow testing

---

## Updated Phase Additions

### Phase 1 Additions (from gap analysis)
- [ ] Half-page scroll (`Ctrl-Shift-d/u`)
- [ ] Force redraw (`Ctrl-l`)
- [ ] StatusBar mode indicator + keybinding hints

### Phase 2 Additions
- [ ] Elapsed timer on task cards (session duration)
- [ ] Connection status indicator in StatusBar

### Phase 3 Additions
- [ ] Select mode (`v`) with visual highlighting
- [ ] Select all (`%`) and clear (`A`)
- [ ] Goto mode (`g`) with `gg`, `ge`, `gh`, `gl`
- [ ] Compact/list view toggle (`Tab`)
- [ ] Move task left/right (`Space` `h/l`)
- [ ] Hide epic children toggle (`f` `e`)
- [ ] Age filter (`f` `1/7/3/0`)
- [ ] Clear all filters (`f` `c`)
- [ ] Sort directions with toggle
- [ ] StatusBar selection count

### Phase 4 Additions
- [ ] Start+work mode (`Space` `S`)
- [ ] Start yolo mode (`Space` `!`)
- [ ] View dev server (`Space` `v`)
- [ ] Restart dev server (`Space` `Ctrl+r`)
- [ ] Port allocation with conflict resolution
- [ ] StatusBar dev server port indicator
- [ ] Delete worktree/cleanup (`Space` `d`)
- [ ] Confirm dialog for destructive actions
- [ ] Bulk cleanup dialog (worktrees vs full)
- [ ] Bulk stop sessions
- [ ] Register tmux global bindings (Ctrl-a Ctrl-a, Ctrl-a Tab)
- [ ] Dev server menu overlay

### Phase 5 Additions
- [ ] Show diff with difftastic (`Space` `f`)
- [ ] Abort merge (`Space` `M`)
- [ ] Merge bead into... (`Space` `b`) with merge select mode
- [ ] Refresh git stats (`r`)
- [ ] Network status detection
- [ ] Offline mode / graceful degradation
- [ ] Connection status in StatusBar

### Phase 6 Additions
- [ ] Jump labels (`g` `w`)
- [ ] Project selector overlay (`g` `p`)
- [ ] Project auto-detection from cwd
- [ ] CLI: az project add/list/remove/switch
- [ ] Manual create bead (`c`) via $EDITOR
- [ ] Claude create bead (`C`) with prompt overlay
- [ ] Manual edit bead (`Space` `e`)
- [ ] Claude edit bead (`Space` `E`)
- [ ] Open Helix editor (`Space` `H`)
- [ ] Chat with Haiku (`Space` `c`)
- [ ] Diagnostics overlay
- [ ] Logs viewer (`L`)
- [ ] Image preview in terminal
- [ ] Open image in external viewer
- [ ] Navigate/delete attachments in detail panel
- [ ] Full settings overlay with all toggles
