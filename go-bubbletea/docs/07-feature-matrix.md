# Complete Feature Matrix (TypeScript → Go)

This matrix ensures no features are lost in the rewrite. Checked items are covered in implementation phases.

## Gap Summary

**Total Features: ~100**
- ✅ Covered: ~35 (35%)
- ⚠️ Missing/Partial: ~65 (65%)

### Critical Missing (must have for v1.0)

1. **Select mode & bulk operations** - Core workflow
2. **Goto mode with jump labels** - Fast navigation
3. **Compact view toggle** - Essential for large boards
4. **Start+work & yolo modes** - Common session patterns
5. **Delete/cleanup workflow** - Resource management
6. **Diff viewer** - Code review before merge
7. **Manual/Claude create & edit** - Bead management
8. **Port allocation for dev servers** - Multi-session support
9. **Confirm dialogs** - Destructive action safety
10. **Move tasks left/right** - Status transitions

### Nice to Have (v1.5+)

- Logs viewer
- Diagnostics overlay
- System notifications
- Network status detection
- Image preview in terminal
- Helix editor integration
- Chat with Haiku mode

---

## Navigation & Modes

| Feature | Key | Status | Phase |
|---------|-----|--------|-------|
| hjkl navigation | `h/j/k/l` | ✅ Covered | 1 |
| Arrow key alternatives | `←↓↑→` | ✅ Covered | 1 |
| Half-page scroll | `Ctrl-Shift-d/u` | ⚠️ Missing | 1 |
| Normal mode | default | ✅ Covered | 1 |
| Select mode | `v` | ⚠️ Missing | 3 |
| Select all | `%` | ⚠️ Missing | 3 |
| Clear selections | `A` | ⚠️ Missing | 3 |
| Search mode | `/` | ✅ Covered | 3 |
| Goto mode | `g` | ⚠️ Missing | 3 |
| Jump labels | `g` `w` | ⚠️ Missing | 6 |
| Goto column top | `g` `g` | ⚠️ Missing | 3 |
| Goto column bottom | `g` `e` | ⚠️ Missing | 3 |
| Goto first/last column | `g` `h`/`l` | ⚠️ Missing | 3 |
| Project selector | `g` `p` | ⚠️ Missing | 6 |
| Action mode | `Space` | ✅ Covered | 3 |
| Merge select mode | `Space` `b` | ⚠️ Missing | 5 |

## View Modes

| Feature | Key | Status | Phase |
|---------|-----|--------|-------|
| Kanban view (default) | - | ✅ Covered | 1 |
| Compact/list view | `Tab` | ⚠️ Missing | 3 |
| Epic drill-down | `Enter` on epic | ✅ Covered | 6 |
| Epic progress bar | - | ✅ Covered | 6 |
| Force redraw | `Ctrl-l` | ⚠️ Missing | 1 |

## Session Management

| Feature | Key | Status | Phase |
|---------|-----|--------|-------|
| Start session | `Space` `s` | ✅ Covered | 4 |
| Start + work | `Space` `S` | ⚠️ Missing | 4 |
| Start yolo (skip perms) | `Space` `!` | ⚠️ Missing | 4 |
| Chat with Haiku | `Space` `c` | ⚠️ Missing | 6 |
| Attach to session | `Space` `a` | ✅ Covered | 4 |
| Pause session | `Space` `p` | ✅ Covered | 4 |
| Resume session | `Space` `R` | ✅ Covered | 4 |
| Stop session | `Space` `x` | ✅ Covered | 4 |
| Session state detection | - | ✅ Covered | 4 |
| Elapsed timer on cards | - | ⚠️ Missing | 2 |

## Dev Server

| Feature | Key | Status | Phase |
|---------|-----|--------|-------|
| Toggle dev server | `Space` `r` | ✅ Covered | 4 |
| View dev server | `Space` `v` | ⚠️ Missing | 4 |
| Restart dev server | `Space` `Ctrl+r` | ⚠️ Missing | 4 |
| Port allocation | - | ⚠️ Missing | 4 |
| Port conflict resolution | - | ⚠️ Missing | 4 |
| StatusBar port indicator | - | ⚠️ Missing | 4 |

## Git Operations

| Feature | Key | Status | Phase |
|---------|-----|--------|-------|
| Update from main | `Space` `u` | ✅ Covered | 5 |
| Merge to main | `Space` `m` | ✅ Covered | 5 |
| Create PR | `Space` `P` | ✅ Covered | 5 |
| Show diff (difftastic) | `Space` `f` | ⚠️ Missing | 5 |
| Abort merge | `Space` `M` | ⚠️ Missing | 5 |
| Merge bead into... | `Space` `b` | ⚠️ Missing | 5 |
| Delete worktree/cleanup | `Space` `d` | ⚠️ Missing | 4 |
| Refresh git stats | `r` | ⚠️ Missing | 5 |
| Conflict detection | - | ✅ Covered | 5 |
| Conflict resolution flow | - | ✅ Covered | 5 |

## Editor/Create Actions

| Feature | Key | Status | Phase |
|---------|-----|--------|-------|
| Open Helix editor | `Space` `H` | ⚠️ Missing | 6 |
| Manual edit bead | `Space` `e` | ⚠️ Missing | 6 |
| Claude edit bead | `Space` `E` | ⚠️ Missing | 6 |
| Manual create bead | `c` | ⚠️ Missing | 6 |
| Claude create bead | `C` | ⚠️ Missing | 6 |
| Move task left/right | `Space` `h`/`l` | ⚠️ Missing | 3 |

## Filters

| Feature | Key | Status | Phase |
|---------|-----|--------|-------|
| Filter menu | `f` | ✅ Covered | 3 |
| Status filter (sub-menu) | `f` `s` | ✅ Covered | 3 |
| Priority filter (sub-menu) | `f` `p` | ✅ Covered | 3 |
| Type filter (sub-menu) | `f` `t` | ✅ Covered | 3 |
| Session filter (sub-menu) | `f` `S` | ✅ Covered | 3 |
| Hide epic children toggle | `f` `e` | ⚠️ Missing | 3 |
| Age filter | `f` `1/7/3/0` | ⚠️ Missing | 3 |
| Clear all filters | `f` `c` | ⚠️ Missing | 3 |

## Sort

| Feature | Key | Status | Phase |
|---------|-----|--------|-------|
| Sort menu | `,` | ✅ Covered | 3 |
| Sort by session | `,` `s` | ⚠️ Missing | 3 |
| Sort by priority | `,` `p` | ⚠️ Missing | 3 |
| Sort by updated | `,` `u` | ⚠️ Missing | 3 |
| Toggle direction | repeat key | ⚠️ Missing | 3 |

## Overlays

| Feature | Key | Status | Phase |
|---------|-----|--------|-------|
| Action menu | `Space` | ✅ Covered | 3 |
| Filter menu | `f` | ✅ Covered | 3 |
| Sort menu | `,` | ✅ Covered | 3 |
| Help overlay | `?` | ✅ Covered | 3 |
| Detail panel | `Enter` | ✅ Covered | 6 |
| Settings overlay | `s` | ✅ Covered | 6 |
| Diagnostics overlay | `d` | ⚠️ Missing | 6 |
| Logs viewer | `L` | ⚠️ Missing | 6 |
| Planning overlay | `p` | ⚠️ Partial | 6 |
| Merge choice dialog | - | ✅ Covered | 5 |
| Confirm dialog | - | ⚠️ Missing | 4 |
| Bulk cleanup dialog | - | ⚠️ Missing | 4 |
| Project selector | `g` `p` | ⚠️ Missing | 6 |
| Claude create prompt | `C` | ⚠️ Missing | 6 |
| Dev server menu | - | ⚠️ Missing | 4 |

## Image Attachments

| Feature | Key | Status | Phase |
|---------|-----|--------|-------|
| Image attach overlay | `Space` `i` | ⚠️ Partial | 6 |
| Paste from clipboard | `p`/`v` | ⚠️ Partial | 6 |
| Attach from file | `f` | ⚠️ Partial | 6 |
| Preview in terminal | `v` in detail | ⚠️ Missing | 6 |
| Open in external viewer | `o` | ⚠️ Missing | 6 |
| Delete attachment | `x` | ⚠️ Missing | 6 |
| Navigate attachments | `j`/`k` in detail | ⚠️ Missing | 6 |

## Bulk Operations

| Feature | Key | Status | Phase |
|---------|-----|--------|-------|
| Select all | `%` | ⚠️ Missing | 3 |
| Bulk stop sessions | selections + `Space` `x` | ⚠️ Missing | 4 |
| Bulk cleanup | selections + `Space` `d` | ⚠️ Missing | 4 |
| Bulk move | selections + `Space` `h/l` | ⚠️ Missing | 3 |
| Cleanup choice (worktrees/full) | - | ⚠️ Missing | 4 |

## Network/Offline

| Feature | Status | Phase |
|---------|--------|-------|
| Network status detection | ⚠️ Missing | 5 |
| Offline mode | ⚠️ Missing | 5 |
| Graceful degradation | ⚠️ Missing | 5 |
| Connection indicator | ⚠️ Missing | 2 |

## Settings (all via `s` overlay)

| Setting | Status | Phase |
|---------|--------|-------|
| CLI Tool (claude/opencode) | ⚠️ Missing | 6 |
| Skip Permissions | ⚠️ Missing | 6 |
| Push on Create | ⚠️ Missing | 6 |
| Git Push/Fetch enabled | ⚠️ Missing | 6 |
| Line Changes in diff | ⚠️ Missing | 6 |
| PR Enabled/Auto Draft/Auto Merge | ⚠️ Missing | 6 |
| Bell/System Notifications | ⚠️ Missing | 6 |
| Auto Detect Network | ⚠️ Missing | 6 |
| Beads Sync | ⚠️ Missing | 6 |
| Pattern Matching state detection | ⚠️ Missing | 6 |

## tmux Integration

| Feature | Status | Phase |
|---------|--------|-------|
| Return to az (Ctrl-a Ctrl-a) | ⚠️ Missing | 4 |
| Toggle Claude/Dev (Ctrl-a Tab) | ⚠️ Missing | 4 |
| Register global tmux bindings | ⚠️ Missing | 4 |

## Multi-Project

| Feature | Status | Phase |
|---------|--------|-------|
| Project selector overlay | ⚠️ Missing | 6 |
| Project auto-detection | ⚠️ Missing | 6 |
| Global projects.json | ⚠️ Missing | 6 |
| CLI: az project add/list/remove/switch | ⚠️ Missing | 6 |

## Misc

| Feature | Status | Phase |
|---------|--------|-------|
| Toast notifications | ✅ Covered | 2 |
| StatusBar mode indicator | ⚠️ Missing | 1 |
| StatusBar keybinding hints | ⚠️ Missing | 1 |
| StatusBar selection count | ⚠️ Missing | 3 |
| StatusBar connection status | ⚠️ Missing | 5 |
