# Gastown Mode Usage

This guide explains how to use Azedarach in Gastown mode for multi-agent orchestration.

## Detection

Azedarach automatically detects if it's running in a Gastown environment by checking:

1. **Config override**: Set `gastown.enabled = true/false` in `.azedarach.json`
2. **Environment**: `GASTOWN_TOWN_DIR` environment variable
3. **Filesystem**: `.gastown` directory in current or parent directories

If any of these indicate Gastown, the UI adapts automatically.

## Configuration

Add Gastown settings to your `.azedarach.json`:

```json
{
  "$schema": 2,
  "cliTool": "claude",
  "gastown": {
    "enabled": true,
    "townDir": "~/gt",
    "defaultAgent": "claude",
    "mayorSession": "mayor",
    "convoyNotifications": true,
    "showRigNames": true
  }
}
```

### Configuration Options

- **`enabled`**: Override auto-detection (true/false/undefined for auto)
- **`townDir`**: Path to your Gastown town directory
- **`defaultAgent`**: Default AI runtime for new sessions
  - Options: `"claude"`, `"codex"`, `"gemini"`, `"cursor"`, `"auggie"`, `"amp"`
- **`mayorSession`**: Special session name for Mayor coordinator
- **`convoyNotifications`**: Show notifications when convoys complete
- **`showRigNames`**: Display rig names in task cards (useful in multi-rig towns)

## UI Differences in Gastown Mode

When running in Gastown mode, the UI uses Gastown terminology:

| Standalone Mode | Gastown Mode |
|----------------|--------------|
| Project | Rig |
| Session | Polecat |
| Worktree | Crew Member |
| Board | Town View |

## Workflow

### Creating a Convoy

Convoys bundle related beads for coordinated parallel work:

```bash
# Via gt CLI (outside Azedarach)
gt convoy create "Auth System" gt-abc12 gt-def34 --notify

# In Azedarach (future feature)
# 1. Select multiple tasks with 'v' (select mode)
# 2. Press 'c' to create convoy
# 3. Enter convoy name
```

### Spawning with Runtime Selection

When spawning a session in Gastown mode:

1. Press `Space+s` to spawn session
2. Choose runtime:
   - Default uses `gastown.defaultAgent` from config
   - Override with `--agent` flag in future UI

### Viewing Convoy Progress

Convoys show progress in the UI:

```
┌──────────────────────────────────┐
│ Convoy: Auth System              │
│ Progress: ███████░░░ 7/10        │
│ gt-abc12 ✅  gt-def34 🔵  ...    │
└──────────────────────────────────┘
```

## Integration Points

### Session Spawning

In Gastown mode, sessions are spawned via `gt sling`:

```bash
# Standalone mode
claude --task "work on: gt-abc12"

# Gastown mode
gt sling gt-abc12 myrig --agent claude
```

Azedarach handles this automatically based on detected mode.

### State Persistence

Gastown uses hooks for persistent state:
- Session progress stored in git worktree hooks
- Survives crashes and restarts
- Enables resumable work

### Multi-Rig Support

When working with multiple rigs:
- Tasks show rig name badges
- Navigate between rigs with `g` (goto) menu
- Filter by rig in search mode

## Future Enhancements

Planned features for enhanced Gastown integration:

- [ ] Convoy creation UI (`c` key in select mode)
- [ ] Visual convoy progress bars
- [ ] Mayor dashboard view
- [ ] Multi-rig switcher
- [ ] Agent runtime selector in spawn menu
- [ ] Dependency graph visualization
- [ ] Cross-rig task search

## Troubleshooting

### "gt: command not found"

Install Gastown:
```bash
go install github.com/steveyegge/gastown/cmd/gt@latest
```

### Sessions not spawning

1. Check `gt agents` to see active agents
2. Verify `gt config get default-agent` 
3. Check logs: `cat az.log`

### UI not detecting Gastown mode

1. Check for `.gastown` directory
2. Set explicit config: `"gastown": { "enabled": true }`
3. Restart Azedarach

## References

- [Gastown Documentation](https://github.com/steveyegge/gastown)
- [Integration Design](./gastown-integration.md)
- [Azedarach Main README](../README.md)
