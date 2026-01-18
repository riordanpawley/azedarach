# Gastown Integration Design

> How Azedarach adapts to work with Gastown's multi-agent orchestration system

## Overview

Azedarach is evolving to work seamlessly with [Gastown](https://github.com/steveyegge/gastown), a multi-agent orchestration system. This document outlines the integration strategy.

## Conceptual Alignment

### Terminology Mapping

Gastown and Azedarach share similar concepts but use different terminology:

| Azedarach Term | Gastown Term | Shared Concept |
|----------------|--------------|----------------|
| Project | Rig | A git repository with associated agents |
| Worktree | Crew Member workspace | Developer's personal working directory |
| Session | Polecat | Ephemeral AI agent working on a task |
| Task/Issue | Bead/Issue | Work item (both use Beads) |
| Board | Town View | Overview of all work items |
| - | Mayor | AI coordinator for complex orchestration |
| - | Convoy | Bundle of related beads for coordinated work |
| - | Hooks | Git worktree-based persistent storage |

### Architectural Fit

```
┌─────────────────────────────────────────────────────────────┐
│                    Gastown Town (~/gt/)                      │
│  ┌────────────┐  ┌────────────┐  ┌────────────┐            │
│  │ Rig: Proj1 │  │ Rig: Proj2 │  │ The Mayor  │            │
│  └────────────┘  └────────────┘  └────────────┘            │
└─────────────────────────────────────────────────────────────┘
                        │
                        │ Azedarach provides TUI view
                        ▼
┌─────────────────────────────────────────────────────────────┐
│                    Azedarach TUI                             │
│  ┌─────────┐ ┌─────────────┐ ┌─────────┐ ┌────────┐        │
│  │  open   │ │ in_progress │ │ blocked │ │ review │        │
│  ├─────────┤ ├─────────────┤ ├─────────┤ ├────────┤        │
│  │ gt-abc  │ │ gt-def 🔵  │ │ gt-ghi  │ │ gt-jkl │        │
│  │ gt-mno  │ │ gt-pqr 🟡  │ │         │ │   ✅   │        │
│  └─────────┘ └─────────────┘ └─────────┘ └────────┘        │
│                                                              │
│  [Space] Actions  [c] Convoy  [g] Goto  [s] Sling  [q] Quit │
└─────────────────────────────────────────────────────────────┘
```

## Integration Strategy

### Phase 1: Coexistence (Minimal Changes)

**Goal:** Azedarach works as a TUI frontend to Gastown without breaking existing functionality.

**Changes:**
1. Add `gt` CLI wrapper alongside existing `bd` CLI
2. Detect if running in a Gastown town (`~/gt/` or has `.gastown/` directory)
3. Update UI labels to show Gastown terminology when appropriate
4. Support both standalone mode (current) and Gastown mode

**Detection Logic:**
```typescript
// Detect Gastown environment
const isGastownProject = () => {
  // Check for .gastown/ directory or gt config
  return fs.existsSync('.gastown') || 
         fs.existsSync('../.gastown') ||
         process.env.GASTOWN_TOWN_DIR
}
```

### Phase 2: Enhanced Integration

**Goal:** Full feature parity with Gastown CLI, but with visual TUI interface.

**New Features:**

1. **Convoy Management**
   - Create convoys: Bundle related beads
   - View convoy status: Progress tracking
   - Convoy drill-down: See all beads in a convoy

2. **Runtime Selection**
   - Support multiple AI runtimes (Claude, Codex, Gemini, Cursor, etc.)
   - Per-session runtime override
   - Visual indicator of which runtime is used

3. **Rig/Crew Integration**
   - Show which rig a session belongs to
   - Support crew member workspaces
   - Navigate between rigs in multi-project towns

4. **Mayor Mode**
   - Special view for the Mayor session
   - High-level orchestration actions
   - Convoy creation and management

### Phase 3: Advanced Features

**Goal:** Unique TUI capabilities that complement Gastown CLI.

**Unique Value:**
1. **Visual Convoy Status**
   - Real-time progress bars for convoys
   - Dependency graph visualization
   - Parallel work visualization

2. **Multi-Rig Dashboard**
   - See all rigs at once
   - Cross-rig task dependencies
   - Global search across all projects

3. **Session Monitoring**
   - Live tailing of agent output
   - Pattern detection for "waiting" states
   - Quick attach to any session

## Implementation Details

### CLI Integration

Add new `GastownClient` service that wraps the `gt` CLI:

```typescript
// src/services/GastownClient.ts
export class GastownClient extends Effect.Service<GastownClient>()("GastownClient", {
  effect: Effect.gen(function* () {
    const command = yield* CommandExecutor
    
    return {
      // Convoy operations
      convoyCreate: (name: string, beadIds: string[]) => 
        command.execute(`gt convoy create "${name}" ${beadIds.join(' ')}`),
      
      convoyList: () => 
        command.execute('gt convoy list'),
      
      // Sling operations
      sling: (beadId: string, rig: string, options?: { agent?: string }) =>
        command.execute(`gt sling ${beadId} ${rig}${options?.agent ? ` --agent ${options.agent}` : ''}`),
      
      // Agent operations
      agents: () => 
        command.execute('gt agents'),
      
      // Config operations
      getDefaultAgent: () =>
        command.execute('gt config get default-agent'),
      
      listAgents: () =>
        command.execute('gt config agent list'),
    }
  }),
}) {}
```

### Mode Detection

Add configuration to detect and adapt to environment:

```typescript
// src/config/environment.ts
export interface EnvironmentConfig {
  mode: 'standalone' | 'gastown'
  townDir?: string
  rigName?: string
  crewMember?: string
}

export const detectEnvironment = (): EnvironmentConfig => {
  // Check for Gastown markers
  if (fs.existsSync('.gastown') || process.env.GASTOWN_TOWN_DIR) {
    return {
      mode: 'gastown',
      townDir: process.env.GASTOWN_TOWN_DIR,
      rigName: process.env.GASTOWN_RIG_NAME,
      crewMember: process.env.GASTOWN_CREW_MEMBER,
    }
  }
  
  return { mode: 'standalone' }
}
```

### UI Adaptations

Update UI components to use context-appropriate terminology:

```typescript
// src/ui/Board.tsx
const Board = () => {
  const env = useAtomValue(environmentAtom)
  const labels = env.mode === 'gastown' 
    ? { project: 'Rig', session: 'Polecat' }
    : { project: 'Project', session: 'Session' }
  
  return (
    <box>
      <text>Welcome to {env.mode === 'gastown' ? 'Gastown TUI' : 'Azedarach'}</text>
      {/* ... */}
    </box>
  )
}
```

### Configuration Schema

Extend configuration to support Gastown-specific settings:

```typescript
// Add to existing config schema
interface AzedarachConfig {
  // ... existing fields
  
  gastown?: {
    enabled: boolean
    townDir: string
    defaultAgent: 'claude' | 'codex' | 'gemini' | 'cursor' | 'auggie' | 'amp'
    mayorSession?: string
    convoyNotifications: boolean
  }
}
```

## Migration Path

### For Existing Azedarach Users

**No breaking changes:**
- All existing functionality remains
- Gastown features are opt-in
- Can still use standalone mode

### For Gastown Users

**Easy adoption:**
1. Install Azedarach: `bun install -g azedarach`
2. Navigate to your town: `cd ~/gt/`
3. Run: `az`
4. Azedarach auto-detects Gastown environment

## Design Decisions

### Why Keep Both Modes?

1. **Backward Compatibility:** Existing users shouldn't be forced to adopt Gastown
2. **Standalone Value:** Azedarach has value as a pure Beads TUI
3. **Gradual Migration:** Users can adopt Gastown features incrementally

### Why Not Rewrite Everything?

1. **Shared Foundation:** Both use Beads, tmux, git worktrees
2. **Incremental Value:** Small changes unlock big capabilities
3. **Risk Management:** Preserve what works, enhance gradually

### What Could Be Rethought?

If we were to do a complete redesign:

1. **Agent Abstraction Layer:**
   - Define common interface for all AI runtimes
   - Plugin system for new runtimes
   - Runtime-specific configuration templates

2. **Persistent State:**
   - Use Gastown's hooks directory for all state
   - Reduce reliance on in-memory state
   - Enable true crash recovery

3. **Multi-Project First:**
   - Design for multiple rigs from day one
   - Cross-project task dependencies
   - Global search and navigation

## Open Questions

1. **Session Lifecycle:** Should Azedarach spawn sessions via `gt sling` or directly via runtime CLIs?
2. **State Synchronization:** How do we stay in sync with state changes made via `gt` CLI?
3. **Mayor Integration:** Should Azedarach have a special "Mayor dashboard" or just treat it as another session?
4. **Convoy UI:** What's the best visualization for convoy progress and dependencies?

## Next Steps

1. **Spike:** Test Gastown integration with minimal prototype
2. **Feedback:** Get input from Gastown users on desired TUI features
3. **Iterate:** Build, measure, learn

## References

- [Gastown Repository](https://github.com/steveyegge/gastown)
- [Gastown Glossary](https://github.com/steveyegge/gastown/blob/main/docs/glossary.md)
- [Beads Documentation](https://github.com/steveyegge/beads)
- [Azedarach Architecture](../README.md)
