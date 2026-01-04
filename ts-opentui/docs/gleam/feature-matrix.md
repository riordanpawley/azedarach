# Feature Matrix

## In Scope (v1.0)

### Core UI

| Feature | Description | Priority |
|---------|-------------|----------|
| Kanban Board | 4-column board (backlog, in_progress, review, done) | P0 |
| Task Cards | Show bead info + session state indicator | P0 |
| Status Bar | Mode, project, session info, dev server port | P0 |
| Navigation | hjkl, arrows, Ctrl+Shift+u/d | P0 |
| Cursor | Visual cursor with column/task position | P0 |
| Catppuccin Theme | Macchiato as default | P0 |
| Custom Themes | User-defined color schemes | P2 |

### Modes & Input

| Feature | Description | Priority |
|---------|-------------|----------|
| Normal Mode | Default navigation mode | P0 |
| Select Mode | Multi-select with v | P1 |
| Search | Filter by title/ID with / | P0 |
| Goto | Jump with g+key (column, first, last, labels) | P1 |
| Jump Labels | 2-char labels for instant jumping (g+w) | P2 |

### Overlays

| Feature | Description | Priority |
|---------|-------------|----------|
| Action Menu | Space to open, shows all actions | P0 |
| Filter Menu | f to filter by status/priority/type/session | P0 |
| Sort Menu | , to sort by session/priority/updated | P0 |
| Help Overlay | ? to show all keybindings | P0 |
| Settings Overlay | s to configure settings | P1 |
| Diagnostics | d to show system health | P2 |
| Logs Viewer | Shift+l to view logs | P2 |
| Detail Panel | Enter to view bead details | P0 |
| Project Selector | g+p to switch projects | P0 |
| Confirm Dialog | Confirmation for destructive actions | P0 |
| Merge Choice | Dialog when branch behind main | P0 |
| Diff Viewer | f in action menu to show diff | P1 |

### Bead Operations

| Feature | Description | Priority |
|---------|-------------|----------|
| List Beads | Fetch from bd CLI | P0 |
| Create Bead | c to create via $EDITOR | P0 |
| Create via Claude | Shift+c for natural language | P1 |
| Edit Bead | e in detail panel | P0 |
| Delete Bead | Shift+d with confirmation | P1 |
| Move Bead | h/l in action menu | P0 |
| Periodic Refresh | Configurable interval (default 30s) | P0 |

### Image Attachments

| Feature | Description | Priority |
|---------|-------------|----------|
| Attach from Clipboard | p/v in overlay | P0 |
| Attach from File | f then path input | P0 |
| List Attachments | In detail panel | P0 |
| Preview Image | v on attachment | P1 |
| Open in Viewer | o on attachment | P1 |
| Delete Attachment | x on attachment | P0 |
| Platform Support | macOS (pbpaste), Linux (wl-paste/xclip) | P0 |

### Session Management

| Feature | Description | Priority |
|---------|-------------|----------|
| Start Session | Space+s | P0 |
| Start+Work | Space+S with bead context prompt | P0 |
| Start Yolo | Space+! skip permissions | P0 |
| Attach | Space+a switch to tmux | P0 |
| Pause | Space+p send Ctrl+C, WIP commit | P0 |
| Resume | Space+Shift+r continue | P0 |
| Stop | Space+x kill session | P0 |
| State Detection | Polling with pattern matching | P0 |
| State Display | Busy/Waiting/Done/Error on cards | P0 |

### Worktree Management

| Feature | Description | Priority |
|---------|-------------|----------|
| Create Worktree | On session/dev server start | P0 |
| Template Path | Configurable path pattern | P0 |
| Delete Worktree | Space+d cleanup | P0 |
| Idempotent Create | Reuse existing worktree | P0 |

### Git Operations

| Feature | Description | Priority |
|---------|-------------|----------|
| Update from Main | Space+u merge main into branch | P0 |
| Merge to Main | Space+m local merge | P0 |
| Show Diff | Space+f difftastic view | P1 |
| Conflict Detection | git merge-tree check | P0 |
| Conflict Resolution | Spawn Claude in merge window | P0 |
| Delete Branch | Part of cleanup | P0 |

### PR Workflow

| Feature | Description | Priority |
|---------|-------------|----------|
| Create PR | Space+Shift+p via gh CLI | P0 |
| Draft by Default | Configurable | P1 |

### Dev Servers

| Feature | Description | Priority |
|---------|-------------|----------|
| Toggle Server | Space+r start/stop | P0 |
| View Server | Space+v attach to window | P0 |
| Restart Server | Space+Ctrl+r | P1 |
| Port Allocation | Base port + offset | P0 |
| Trust Port | No polling for detected port | P0 |
| One Window per Server | Named dev-{name} | P0 |
| Multiple Servers | Support multiple per bead | P1 |

### Init & Background

| Feature | Description | Priority |
|---------|-------------|----------|
| Init Commands | Run once per session creation | P0 |
| Sequential Execution | Wait for prompt between commands | P0 |
| direnv Support | Load env between commands | P0 |
| Background Tasks | Separate windows | P0 |
| Close on Success | Windows close when task exits 0 | P0 |
| Stay on Failure | Debug failed tasks | P0 |

### Multi-Project

| Feature | Description | Priority |
|---------|-------------|----------|
| Project Switcher | g+p overlay | P0 |
| Project List | From config or discovery | P0 |
| Per-Project Config | Each project has .azedarach.json | P0 |

### Toasts & Notifications

| Feature | Description | Priority |
|---------|-------------|----------|
| Toast Messages | Success/error/info/warning | P0 |
| Auto-Dismiss | Configurable timeout | P1 |

### Git Workflow Configuration

| Feature | Description | Priority |
|---------|-------------|----------|
| Workflow Mode | `"local"` (direct merge) vs `"origin"` (PR-based) | P0 |
| Push on Create | Auto-push branch after worktree creation | P0 |
| Push Enabled | Global kill-switch for all git push operations | P0 |
| Fetch Enabled | Global kill-switch for all git fetch operations | P0 |
| Base Branch | Configurable main branch (main, master, develop) | P0 |
| Remote Name | Configurable remote (default: origin) | P1 |
| Branch Prefix | Prefix for auto-generated branches (default: az-) | P1 |
| Offline Mode | Graceful degradation when network unavailable | P0 |
| Settings Toggle | Live toggle in settings overlay | P0 |

### Configuration

| Feature | Description | Priority |
|---------|-------------|----------|
| JSON Config | .azedarach.json | P0 |
| Worktree Config | pathTemplate, initCommands | P0 |
| Session Config | shell, tmuxPrefix, backgroundTasks | P0 |
| Dev Server Config | servers, ports | P0 |
| Polling Config | beadsRefresh, sessionMonitor | P0 |
| Theme Config | theme name | P0 |
| Git Config | workflowMode, pushEnabled, fetchEnabled, baseBranch | P0 |
| PR Config | enabled, autoDraft, autoMerge | P0 |
| Beads Config | syncEnabled | P0 |

---

## Out of Scope (v1.0)

| Feature | Reason | Future Version |
|---------|--------|----------------|
| Epic Orchestration | Complex, needs more design | v2 |
| Swarm Pattern | Parallel session spawning | v2 |
| VC Integration | Not yet used | v2+ |
| Command Mode (`:`) | Tied to VC | v2+ |
| Compact View | Nice-to-have, focus on kanban | v1.5 |
| Keybind Customization | Stretch goal | v1.5 |
| Attach Inline | Deprecated in TS version | Never |
| Chat About Task | Not needed | Never |
| Auto PR Creation | Manual keybind sufficient | v2 |

---

## Comparison: TypeScript vs Gleam

| Aspect | TypeScript | Gleam |
|--------|-----------|-------|
| Architecture | 43 Effect services | ~6 OTP actors |
| State Management | SubscriptionRef + Atoms + React | TEA Model |
| Concurrency | Effect fibers (cooperative) | OTP processes (preemptive) |
| UI Framework | React + OpenTUI | Shore |
| Error Handling | Effect typed errors | Gleam Result + OTP supervision |
| Lines of Code | ~33,000 | ~7,000-8,000 (estimated) |
| Fault Tolerance | Manual | OTP supervision trees |
| Hot Reload | Bun watch | Erlang hot code loading (potential) |
