# Keybinding Reference

Azedarach uses **Helix-style modal keybindings** inspired by the Helix editor. This provides efficient, ergonomic navigation without leaving the home row.

## Mode Overview

```
┌─────────────────────────────────────────────────────────────┐
│                    NORMAL MODE (NOR)                        │
│   hjkl: navigate   g: goto prefix   v: select   Space: act  │
└─────────────────────────────────────────────────────────────┘
         │                │              │            │
         │                ▼              ▼            ▼
         │        ┌──────────────┐ ┌──────────┐ ┌──────────┐
         │        │ GOTO (GTO)   │ │ SELECT   │ │ ACTION   │
         │        │ gg/ge/gh/gl  │ │ (SEL)    │ │ (ACT)    │
         │        │ gw: labels   │ │ Space:   │ │ h/l:move │
         │        └──────────────┘ │ toggle   │ │ a:attach │
         │               │         └──────────┘ └──────────┘
         │               │              │            │
         └───────────────┴──────────────┴────────────┘
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
| `Ctrl-d` | Half-page down | Fast scrolling |
| `Ctrl-u` | Half-page up | Fast scrolling |

### Actions

| Key | Action | Notes |
|-----|--------|-------|
| `Enter` | Show task details | Modal overlay |
| `Space` | Enter Action mode | Prefix for commands |
| `g` | Enter Goto mode | Prefix for jumps |
| `v` | Enter Select mode | Multi-selection |
| `c` | Create new task | Opens task creation prompt |
| `?` | Show help | Press any key to dismiss |
| `q` | Quit | Exit application |
| `Esc` | Dismiss overlay | Or return from sub-mode |

## Goto Mode

Press `g` to enter goto mode. The next key determines the jump target.

| Sequence | Action | Description |
|----------|--------|-------------|
| `g` `g` | First task | Jump to top of board |
| `g` `e` | Last task | Jump to bottom of board |
| `g` `h` | Column start | First task in current column |
| `g` `l` | Column end | Last task in current column |
| `g` `w` | Jump labels | Shows 2-char labels on each task |

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

## Action Mode

Press `Space` in Normal mode to enter action mode. A floating palette shows available actions.

### Session Actions

| Sequence | Action | Available When |
|----------|--------|----------------|
| `Space` `s` | Start session | Task is idle (creates worktree + tmux) |
| `Space` `a` | Attach to session | Session exists (switches tmux client) |
| `Space` `p` | Pause session | Session is busy (Ctrl-C + WIP commit) |
| `Space` `r` | Resume session | Session is paused |
| `Space` `x` | Stop session | Session exists (kills tmux) |

### Git/PR Actions

| Sequence | Action | Available When |
|----------|--------|----------------|
| `Space` `P` | Create PR | Worktree exists (push + gh pr create) |
| `Space` `d` | Delete worktree | Worktree exists (cleanup branches) |

### Movement Actions

| Sequence | Action | Description |
|----------|--------|-------------|
| `Space` `h` | Move left | Move task(s) to previous column |
| `Space` `l` | Move right | Move task(s) to next column |

### Other Actions

| Sequence | Action | Description |
|----------|--------|-------------|
| `Space` `e` | Edit bead | Opens in $EDITOR as markdown |
| `Esc` | Cancel | Exit action mode |

### Batch Operations

If you have tasks selected (from Select mode), Action mode commands apply to all selected tasks:

1. Press `v` to enter Select mode
2. Navigate and press `Space` to select multiple tasks
3. Press `Esc` to return to Normal (selections persist)
4. Press `Space` `l` to move all selected tasks right

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

## Tips

1. **Stay on home row**: All primary keys (hjkl, g, v, Space) are accessible without moving your hands.

2. **Use jump labels for large boards**: `gw` + 2 chars is faster than repeated hjkl navigation.

3. **Batch moves with Select mode**: Select multiple related tasks, then move them together.

4. **Quick column jumps**: `gh` and `gl` jump to column boundaries quickly.

5. **Half-page scrolling**: `Ctrl-d` and `Ctrl-u` are great for tall columns.
