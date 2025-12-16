# tmux Guide for Azedarach Users

tmux (terminal multiplexer) is essential for Azedarach's session management. This guide covers everything you need to know to use tmux with Azedarach, even if you've never used tmux before.

## What is tmux?

tmux lets you:
- Run terminal sessions in the background (they persist even if you close your terminal)
- Attach and detach from sessions at any time
- Split terminals into multiple panes
- Share sessions between terminals

**Why Azedarach uses tmux:** Claude Code sessions need to run persistently. tmux keeps them alive in the background so you can:
- Start multiple Claude sessions without multiple terminal windows
- Attach to see what Claude is doing
- Detach and let Claude continue working
- Resume sessions after disconnects

## Installation

### macOS

```bash
# Using Homebrew (recommended)
brew install tmux

# Verify installation
tmux -V
# Expected: tmux 3.x
```

### Linux

```bash
# Ubuntu/Debian
sudo apt install tmux

# Fedora
sudo dnf install tmux

# Arch
sudo pacman -S tmux
```

### Verify Installation

```bash
tmux -V
# Should output: tmux 3.x (version number)
```

## tmux Concepts

### Sessions, Windows, and Panes

```
┌─────────────────────────────────────────────────────────┐
│                     tmux server                          │
│  ┌─────────────────────────────────────────────────┐    │
│  │               Session: claude-az-05y             │    │
│  │  ┌─────────────────────────────────────────┐    │    │
│  │  │              Window 0                    │    │    │
│  │  │  ┌─────────────┬─────────────┐          │    │    │
│  │  │  │   Pane 0    │   Pane 1    │          │    │    │
│  │  │  │  (Claude)   │  (optional) │          │    │    │
│  │  │  └─────────────┴─────────────┘          │    │    │
│  │  └─────────────────────────────────────────┘    │    │
│  └─────────────────────────────────────────────────┘    │
│                                                          │
│  ┌─────────────────────────────────────────────────┐    │
│  │               Session: claude-az-xyz             │    │
│  │  ...                                             │    │
│  └─────────────────────────────────────────────────┘    │
└─────────────────────────────────────────────────────────┘
```

- **Session**: Named container for your work (e.g., `claude-az-05y`)
- **Window**: Like a tab within a session
- **Pane**: Split within a window

**For Azedarach:** Each Claude session is a tmux session. You mostly just need to know about sessions.

### Attached vs Detached

- **Attached**: You're viewing the session in your terminal
- **Detached**: Session runs in background, you can't see it

## Essential Commands

### The Prefix Key

tmux commands start with a **prefix key**. Azedarach uses `Ctrl-a` (configured in the recommended `.tmux.conf` below).

To run a tmux command:
1. Press `Ctrl-a` (nothing visible happens)
2. Then press the command key

Example: To detach, press `Ctrl-a` then `d`.

I'll write this as: `Ctrl-a d`

> **Note:** The tmux default is `Ctrl-b`, but `Ctrl-a` is easier to reach and matches GNU Screen.

### Session Commands

| Command | Description |
|---------|-------------|
| `tmux new-session -s name` | Create new session named "name" |
| `tmux new-session -d -s name` | Create detached session |
| `tmux attach -t name` | Attach to session "name" |
| `tmux list-sessions` | List all sessions |
| `tmux kill-session -t name` | Kill session "name" |
| `Ctrl-a d` | Detach from current session |

### Quick Reference Card

```
┌────────────────────────────────────────────────────────┐
│               tmux Quick Reference                      │
├────────────────────────────────────────────────────────┤
│ PREFIX = Ctrl-a (press first, then command key)        │
├────────────────────────────────────────────────────────┤
│ Session Commands:                                       │
│   Ctrl-a Ctrl-a Return to az TUI (double-tap prefix)   │
│   Ctrl-a d     Detach from session                     │
│   Ctrl-a $     Rename session                          │
│   Ctrl-a s     List sessions (interactive)             │
│   Ctrl-a (     Previous session                        │
│   Ctrl-a )     Next session                            │
├────────────────────────────────────────────────────────┤
│ Window Commands:                                        │
│   Ctrl-a c     Create new window                       │
│   Ctrl-a n     Next window                             │
│   Ctrl-a p     Previous window                         │
│   Ctrl-a 0-9   Switch to window by number              │
├────────────────────────────────────────────────────────┤
│ Pane Commands:                                          │
│   Ctrl-a %     Split vertically                        │
│   Ctrl-a "     Split horizontally                      │
│   Ctrl-a o     Switch to next pane                     │
│   Ctrl-a x     Close current pane                      │
├────────────────────────────────────────────────────────┤
│ Special:                                                │
│   Ctrl-a `     Popup terminal (floating shell)         │
│   Ctrl-a ?     Help (list all keybindings)             │
│   Ctrl-a :     Command prompt                          │
├────────────────────────────────────────────────────────┤
│ Copy Mode (scrolling):                                  │
│   Ctrl-a u     Enter scroll mode + page up             │
│   Ctrl-a Escape Enter scroll mode                      │
│   Ctrl-u/d     Page up/down (in scroll mode)           │
│   j/k          Line up/down (in scroll mode)           │
│   q or Escape  Exit scroll mode                        │
└────────────────────────────────────────────────────────┘
```

## Using tmux with Azedarach

### How Azedarach Uses tmux

When you start az, it runs inside a tmux session (default name: `az`). When you start Claude sessions:

1. **SessionManager** creates a tmux session named after the bead ID
2. Claude Code runs inside that tmux session
3. You can attach to see/interact with Claude
4. Detaching lets Claude continue working
5. **Press `Ctrl-a Ctrl-a` (double-tap prefix) from any Claude session to return to az**

### Session Naming Convention

Azedarach creates tmux sessions with this pattern:
- Main TUI: `az` (configurable via `AZ_TMUX_SESSION` env var)
- Claude sessions: `{bead-id}` (e.g., `az-05y`, `az-xyz`)

Examples:
- `az` - the main Azedarach TUI
- `az-05y` - Claude session for bead `az-05y`
- `az-xyz` - Claude session for bead `az-xyz`

### Attaching to Sessions

**From Azedarach TUI:**
1. Navigate to a task with an active session
2. Press `Space` then `a`
3. A new terminal window opens attached to the session

**From command line:**
```bash
# List all Claude sessions
tmux list-sessions | grep claude

# Attach to a specific session
tmux attach -t claude-az-05y
```

### Detaching from Sessions

When viewing a Claude session:
- Press `Ctrl-a` then `d` to detach
- The session continues running in the background
- You return to your previous terminal

### Viewing Session Output

If you want to scroll through Claude's output:
1. Attach to the session
2. Enter scroll mode using one of:
   - `Ctrl-a u` - enters scroll mode and pages up
   - `Ctrl-a Escape` - enters scroll mode
   - Trackpad scroll - auto-enters scroll mode
3. Navigate with:
   - `Ctrl-u` / `Ctrl-d` - page up/down
   - `j` / `k` - line by line
   - `/` - search forward
4. Press `q` or `Escape` to exit scroll mode

### Popup Terminal

Need a quick shell without leaving your current view?

Press `` Ctrl-a ` `` (backtick) to open a floating popup terminal. It:
- Opens at 80% size in the center of your screen
- Uses your current directory
- Closes when you exit (`Ctrl-d` or `exit`)

Great for quick `git status`, `bd ready`, or any one-off command!

## Common Workflows

### Workflow 0: Navigate Between az and Claude Sessions

**The quick workflow:**
```bash
# From az TUI: Space+a on a task to attach to its Claude session
# From Claude session: Ctrl-a Ctrl-a to return to az TUI (double-tap prefix)
```

This two-keystroke pattern makes az the hub for all session navigation:
- `Space` `a` → go TO a Claude session (from az)
- `Ctrl-a Ctrl-a` → go BACK to az (double-tap prefix, from anywhere)

### Workflow 1: Monitor a Running Session

```bash
# 1. Check what sessions are running
tmux list-sessions

# 2. Attach to see what Claude is doing
tmux attach -t claude-az-05y

# 3. Watch for a while, then detach
# Press: Ctrl-a d

# Claude continues working in the background
```

### Workflow 2: Interact with Claude

```bash
# 1. Attach to the session
tmux attach -t claude-az-05y

# 2. You can now type to interact with Claude
# (if Claude is waiting for input)

# 3. When done, detach
# Press: Ctrl-a d
```

### Workflow 3: Check Multiple Sessions

```bash
# List all sessions with their status
tmux list-sessions

# Output example:
# claude-az-05y: 1 windows (created Thu Dec 12 10:30:00 2024)
# claude-az-xyz: 1 windows (created Thu Dec 12 10:35:00 2024)

# Quick switch between sessions (while attached):
# Ctrl-a s  (shows session list, use arrows to select)
```

### Workflow 4: Kill a Stuck Session

```bash
# If a Claude session is stuck and needs to be killed:
tmux kill-session -t claude-az-05y

# Or from inside the session:
# Type: exit
# Or press: Ctrl-d
```

## Testing tmux with Azedarach

### Quick Test

```bash
# 1. Create a test session
tmux new-session -d -s claude-test "echo 'Hello from tmux!' && sleep 3600"

# 2. Verify it's running
tmux list-sessions
# Should show: claude-test: 1 windows ...

# 3. Attach to see it
tmux attach -t claude-test
# You should see "Hello from tmux!"

# 4. Detach
# Press: Ctrl-a d

# 5. Clean up
tmux kill-session -t claude-test
```

### Test with Azedarach Naming

```bash
# 1. Pick a bead ID from your project
bd ready | head -1
# Let's say it returns: az-05y

# 2. Create a session with matching name
tmux new-session -d -s claude-az-05y "bash"

# 3. Start Azedarach
pnpm dev

# 4. Navigate to az-05y task
# Press: Space then a
# Should open new terminal attached to the session!

# 5. Clean up
tmux kill-session -t claude-az-05y
```

## Troubleshooting

### "sessions should be nested with care"

**Problem:** You're trying to start tmux inside tmux.

**Solution:** Detach first (`Ctrl-a d`), then run your tmux command.

### "no server running"

**Problem:** No tmux sessions exist yet.

**Solution:** This is normal - create a session first:
```bash
tmux new-session -s mysession
```

### "session not found"

**Problem:** The session name doesn't exist.

**Solution:** Check existing sessions:
```bash
tmux list-sessions
```

### "can't find tmux"

**Problem:** tmux isn't installed.

**Solution:** Install it (see Installation section above).

### Session Attached Elsewhere

**Problem:** "sessions should be nested with care, unset $TMUX to force"

**Solution:** The session is attached in another terminal. Either:
1. Detach from the other terminal first
2. Or force attach (detaches other):
   ```bash
   tmux attach -d -t claude-az-05y
   ```

### Terminal Looks Corrupted

**Solution:**
```bash
# Reset the terminal
reset

# Or inside tmux:
# Ctrl-a :
# Then type: refresh-client
```

## Required Configuration

### Recommended .tmux.conf

For the full recommended tmux configuration with all features (popup terminal, FZF integration, session persistence, etc.), see **[tmux-config.md](tmux-config.md)**.

**Minimal config** for basic Azedarach compatibility:

```bash
# Use Ctrl-a instead of Ctrl-b (easier to reach)
set -g prefix C-a
unbind C-b
bind C-a send-prefix

# Enable mouse support
set -g mouse on

# Better colors
set -g default-terminal "tmux-256color"
set -ga terminal-overrides ",*256col*:Tc"

# Start window numbering at 1
set -g base-index 1
setw -g pane-base-index 1

# Faster key repetition (critical for editors)
set -s escape-time 0

# Increase scrollback buffer
set -g history-limit 50000

# Popup terminal (Ctrl-a `)
bind ` display-popup -E -w 80% -h 80% -d "#{pane_current_path}"

# Better scroll mode entry
bind u copy-mode -eu
bind Escape copy-mode
```

After creating/editing, reload with:
```bash
tmux source-file ~/.tmux.conf
```

### Useful Aliases

Add to your `~/.bashrc` or `~/.zshrc`:

```bash
# tmux shortcuts
alias ta='tmux attach -t'
alias tl='tmux list-sessions'
alias tk='tmux kill-session -t'
alias tn='tmux new-session -s'

# Azedarach-specific
alias tclaude='tmux list-sessions | grep claude'
```

## Further Reading

- [tmux official wiki](https://github.com/tmux/tmux/wiki)
- [tmux cheat sheet](https://tmuxcheatsheet.com/)
- [The Tao of tmux](https://leanpub.com/the-tao-of-tmux/read) (free online book)
