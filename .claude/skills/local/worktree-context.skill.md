# Azedarach Worktree Context

**This is an Azedarach-managed worktree session.**

## Your Session

- **Bead ID:** `az-r883`
- **Branch:** `az-r883`

## Dev Server Commands

Control dev servers without breaking TUI state tracking:

```bash
# Start the dev server
az dev start az-r883

# Stop the dev server
az dev stop az-r883

# Restart after config changes
az dev restart az-r883

# Check server status
az dev status az-r883
```

**Why use az CLI?** Direct commands (npm run dev, ctrl-c) break TUI state tracking.
The `az dev` commands sync state via tmux metadata.

## Session Lifecycle

1. **You're here** - TUI spawned your session in this worktree
2. **Do your work** - Use `az dev` for server control
3. **Sync beads** - Run `bd sync` before finishing
4. **Complete** - Clean exit triggers TUI completion workflow (PR creation)

## Quick Reference

| Command | Description |
|---------|-------------|
| `az dev start az-r883` | Start dev server |
| `az dev stop az-r883` | Stop dev server |
| `az dev restart az-r883` | Restart dev server |
| `az dev status az-r883` | Check server status |
| `bd sync` | Sync beads changes |
| `bd close az-r883` | Mark bead complete |
