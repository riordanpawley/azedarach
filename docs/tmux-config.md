# tmux Configuration for Azedarach

Azedarach uses tmux to manage Claude Code sessions. This guide provides a recommended tmux configuration optimized for multi-worktree development workflows.

## Quick Start

1. Copy the [example config](#example-configuration) to `~/.tmux.conf`
2. Install TPM (plugin manager):
   ```bash
   git clone https://github.com/tmux-plugins/tpm ~/.tmux/plugins/tpm
   ```
3. Reload and install plugins:
   ```bash
   tmux source-file ~/.tmux.conf
   # Then press: Ctrl-a I (capital I)
   ```

## Cheatsheet

**Prefix:** `Ctrl-a`

### Windows (Your Worktrees)

| Keys | Action |
|------|--------|
| `1-9` | Jump to window |
| `c` | New window (in current dir) |
| `p/n` | Prev/next window |
| `a` | Last window (toggle) |
| `N/P` | Move window right/left |
| `f` | FZF window picker |
| `F` | FZF session picker |
| `w` | Tree picker with preview |

### Panes (Your Tools)

| Keys | Action |
|------|--------|
| `\|` / `-` | Split horizontal / vertical |
| `Arrow` | Navigate panes |
| `Shift+Arrow` | Resize panes |
| `z` | Toggle zoom (fullscreen) |
| `>` / `<` | Swap pane down/up |
| `b` | Break pane to new window |
| `B` | Join pane from another window |
| `S` | Sync panes (type in all) |

### Copy Mode (Scrolling Claude Code Output)

| Keys | Action |
|------|--------|
| Trackpad scroll | Auto-enters copy mode (no prefix!) |
| `u` | Enter copy mode + scroll up |
| `Escape` | Enter copy mode |
| `v` | Enter copy mode (vim-style) |
| `Ctrl-u` / `Ctrl-d` | Page up/down (in copy mode) |
| `j` / `k` | Line up/down (in copy mode) |
| `/` | Search forward |
| `q` or `Escape` | Exit copy mode |
| `v` then `y` | Select then copy |

### Special

| Keys | Action |
|------|--------|
| `` ` `` | Popup terminal (floating shell) |
| `r` | Reload config |
| `Ctrl-s` | Save session (resurrect) |
| `Ctrl-r` | Restore session (resurrect) |

## Workflow Tips

### Popup Terminal

Press `` Ctrl-a ` `` for a floating terminal overlay. Perfect for:
- Quick `git status` checks
- Running `bd ready` to find work
- Any one-off command without disrupting your layout

The popup closes automatically when you exit.

### Sync Panes

Press `Ctrl-a S` to toggle synchronized input across all panes. Type once, execute everywhere. Great for:
- Running `git pull` across multiple worktrees
- Restarting services in parallel
- Any repeated command across panes

### Session Persistence

With tmux-resurrect and tmux-continuum:
- Sessions auto-save every 15 minutes
- On tmux restart, your entire layout restores automatically
- Pane contents (scrollback) are preserved
- Working directories are remembered

Manual save/restore: `Ctrl-a Ctrl-s` / `Ctrl-a Ctrl-r`

### FZF Integration

- `Ctrl-a f` - Fuzzy find any window across ALL sessions
- `Ctrl-a F` - Fuzzy find sessions

Great when you have many worktrees open and need to jump quickly.

## Example Configuration

```bash
# ============================================================================
# tmux Configuration for Azedarach / Multi-Worktree Development
# ============================================================================

# -----------------------------------------------------------------------------
# General Settings
# -----------------------------------------------------------------------------

# True colors (works with modern terminals like ghostty, kitty, alacritty)
set -g default-terminal "tmux-256color"
set -ga terminal-overrides ",*256col*:Tc"

# Increase scrollback buffer
set -g history-limit 50000

# Start numbering at 1 (easier keyboard access)
set -g base-index 1
setw -g pane-base-index 1

# Renumber windows when one is closed
set -g renumber-windows on

# Zero escape delay (critical for editors like helix/vim)
set -sg escape-time 0

# Enable focus events (for editor integration)
set -g focus-events on

# Enable mouse support
set -g mouse on

# -----------------------------------------------------------------------------
# Prefix Key
# -----------------------------------------------------------------------------

# Use Ctrl-a instead of Ctrl-b (easier to reach)
unbind C-b
set -g prefix C-a
bind C-a send-prefix

# -----------------------------------------------------------------------------
# Window Navigation
# -----------------------------------------------------------------------------

# Direct window jumping (1-9)
bind 1 select-window -t 1
bind 2 select-window -t 2
bind 3 select-window -t 3
bind 4 select-window -t 4
bind 5 select-window -t 5
bind 6 select-window -t 6
bind 7 select-window -t 7
bind 8 select-window -t 8
bind 9 select-window -t 9

# Previous/next window
bind p previous-window
bind n next-window

# Last window toggle
bind a last-window

# New window in current directory
bind c new-window -c "#{pane_current_path}"

# -----------------------------------------------------------------------------
# Pane Management
# -----------------------------------------------------------------------------

# Split panes (preserving current directory)
bind | split-window -h -c "#{pane_current_path}"
bind - split-window -v -c "#{pane_current_path}"
bind '"' split-window -v -c "#{pane_current_path}"
bind % split-window -h -c "#{pane_current_path}"

# Navigate panes with arrows
bind Up select-pane -U
bind Down select-pane -D
bind Left select-pane -L
bind Right select-pane -R

# Resize panes with Shift+arrows
bind -r S-Up resize-pane -U 5
bind -r S-Down resize-pane -D 5
bind -r S-Left resize-pane -L 5
bind -r S-Right resize-pane -R 5

# Zoom toggle
bind z resize-pane -Z

# -----------------------------------------------------------------------------
# Copy Mode (Vi-style)
# -----------------------------------------------------------------------------

setw -g mode-keys vi
bind [ copy-mode
bind -T copy-mode-vi v send-keys -X begin-selection
bind -T copy-mode-vi y send-keys -X copy-pipe-and-cancel "pbcopy"
bind -T copy-mode-vi C-v send-keys -X rectangle-toggle

# -----------------------------------------------------------------------------
# Workflow Enhancements
# -----------------------------------------------------------------------------

# Swap panes
bind > swap-pane -D
bind < swap-pane -U

# Break/join panes
bind b break-pane -d
bind B choose-window "join-pane -h -s '%%'"

# Sync panes (type in all at once)
bind S setw synchronize-panes \; display "Sync #{?synchronize-panes,ON,OFF}"

# Move windows
bind -r N swap-window -t +1 \; next-window
bind -r P swap-window -t -1 \; previous-window

# Tree picker with preview
bind w choose-tree -Zw

# Popup terminal
bind ` display-popup -E -w 80% -h 80% -d "#{pane_current_path}"

# FZF integration (requires fzf)
bind f run-shell "tmux list-windows -a -F '#{session_name}:#{window_index} #{window_name}' | fzf-tmux -p 80%,60% | cut -d' ' -f1 | xargs -I{} tmux switch-client -t {}"
bind F run-shell "tmux list-sessions -F '#{session_name}' | fzf-tmux -p 50%,40% | xargs -I{} tmux switch-client -t {}"

# Reload config
bind r source-file ~/.tmux.conf \; display "Config reloaded!"

# -----------------------------------------------------------------------------
# Status Bar
# -----------------------------------------------------------------------------

set -g status-position bottom
set -g status-style bg=colour235,fg=colour255
set -g status-left-length 50
set -g status-left "#[fg=colour39,bold] #S "
set -g status-right-length 50
set -g status-right "#[fg=colour245] %Y-%m-%d #[fg=colour255,bold] %H:%M "
set -g window-status-format " #I:#W "
set -g window-status-current-format "#[fg=colour39,bold] #I:#W "

# Pane borders
set -g pane-border-style fg=colour240
set -g pane-active-border-style fg=colour39

# -----------------------------------------------------------------------------
# Plugins (TPM)
# -----------------------------------------------------------------------------
# Install: git clone https://github.com/tmux-plugins/tpm ~/.tmux/plugins/tpm
# Then press: Prefix + I

set -g @plugin 'tmux-plugins/tpm'
set -g @plugin 'tmux-plugins/tmux-resurrect'
set -g @plugin 'tmux-plugins/tmux-continuum'

set -g @resurrect-capture-pane-contents 'on'
set -g @continuum-restore 'on'

# Initialize TPM (keep at bottom)
run '~/.tmux/plugins/tpm/tpm'
```

## See Also

- [tmux Guide](tmux-guide.md) - tmux primer for new users
- [Keybindings](keybindings.md) - Azedarach keyboard shortcuts
