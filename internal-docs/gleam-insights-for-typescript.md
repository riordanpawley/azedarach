# Gleam Planning Insights for TypeScript App

Extracted from Gleam rewrite planning docs. The Gleam rewrite is discontinued, but these insights remain valuable for improving the TypeScript implementation.

## Executive Summary

The Gleam planning identified several architectural issues with the TypeScript app:
- **45 Effect services** is excessive for what the app does
- **Three-layer state management** (SubscriptionRef + Atoms + React) adds complexity

### Already Implemented ✓

Many key insights from the Gleam planning are **already in the TypeScript app**:

| Insight | Status | Location |
|---------|--------|----------|
| State hierarchy (tmux as source of truth) | ✓ Done | `WorktreeSessionService` stores `azOptions`, `ClaudeSessionManager.listActive()` does orphan recovery |
| Init commands run once | ✓ Done | Uses `@az_init_done` marker in tmux session options |
| Start+work prompt (ask first, design notes) | ✓ Done | `SessionHandlersService.ts:147-176` |
| Image attachment paths in prompt | ✓ Done | `SessionHandlersService.ts:169-176` |
| Worktree continuation context | ✓ Done | `SessionHandlersService.ts:158-165` |
| Merge choice UX (branch behind) | ✓ Done | `SessionHandlersService.ts:404-462` |
| Session persistence | ✓ Done | `.azedarach/sessions.json` |

### Remaining Opportunities

---

## 1. Service Consolidation (MEDIUM PRIORITY)

**Current state**: 45 Effect services across `src/services/` and `src/core/`.

**Gleam proposal**: ~6 actors covering the same functionality.

### Recommended Service Merges

| Current Services | Merge Into | Rationale |
|-----------------|------------|-----------|
| `NavigationService`, `EditorService`, `KeyboardService`, + 7 keyboard handlers | `UIService` | All UI state and input handling |
| `SessionService`, `ClaudeSessionManager`, `TmuxSessionMonitor`, `PTYMonitor`, `StateDetector` | `SessionService` | Session lifecycle is one domain |
| `DevServerService`, `WorktreeSessionService` | `WorktreeService` | Worktree + dev server are coupled |
| `BeadsClient`, `BoardService`, `BeadEditorService`, `AttachmentService`, `ImageAttachmentService` | `BeadsService` | All bead operations |
| `OverlayService`, `ToastService`, `DiagnosticsService`, `ViewService` | `UIFeedbackService` | UI feedback mechanisms |
| `ProjectService`, `ProjectStateService`, `SettingsService` | `ConfigService` | Project and settings |

**Target**: 10-15 services maximum, down from 45.

### Action Items

- [ ] Audit keyboard handler services - likely can be functions, not services
- [ ] Merge `ClaudeSessionManager` + `SessionService` + monitors into single service
- [ ] Combine bead-related services into one `BeadsService`
- [ ] Evaluate if `MutationQueue`, `CommandQueueService` can be Effect patterns instead of services

---

## 2. State Hierarchy: Tmux as Source of Truth ✓ IMPLEMENTED

**Gleam design**:
```
Priority 1: TMUX (Source of Truth)
  - Session exists? → tmux has-session
  - Session output? → tmux capture-pane
  - On restart: reconstruct state from tmux

Priority 2: IN-MEMORY (Optimistic Updates)
  - UI state, derived state
  - Fast, may be stale

Priority 3: FILES (Last Resort)
  - Config, image attachments
  - Only via bd CLI for bead data
```

**TypeScript implementation:**
- `WorktreeSessionService` stores `azOptions: { worktreePath, projectPath }` in tmux session
- `ClaudeSessionManager.listActive()` (lines 843-911) recovers orphaned sessions from tmux
- `TmuxSessionMonitor` polls tmux for session state
- `.azedarach/sessions.json` for persistence across app restarts

---

## 3. Simpler Mode Structure

**Current TS**: Multiple modes scattered across EditorService, InputState types, overlays.

**Gleam design**: Only 2 actual modes:
```typescript
type Mode =
  | { _tag: "Normal" }
  | { _tag: "Select"; selected: Set<string> }
```

Everything else is **InputState** (search, title input, etc.) or **Overlay** (menus, panels).

### Recommendation

Keep the three-way split conceptually clear:
1. **Mode** - Normal or Select (multi-select)
2. **InputState** - Active text input (search, title editing)
3. **Overlay** - Visible panels/menus

Don't add more "modes" - add InputStates or Overlays instead.

---

## 4. Merge Conflict UX Improvements ✓ PARTIALLY IMPLEMENTED

**Gleam design** (from `docs/gleam/merge-conflict-ux.md`):

1. Use `git merge-tree --write-tree` for **safe, in-memory** conflict detection
2. Only start actual merge if conflicts detected
3. Spawn Claude in dedicated "merge" window with resolve prompt
4. Filter out `.beads/` conflicts (handled by `bd sync`)

**TypeScript implementation:**
- ✓ MergeChoice overlay exists (`SessionHandlersService.ts:404-462`)
- ✓ Checks `branchStatus.behind` before attach
- ✓ Options: Merge & Attach / Skip & Attach / Cancel

### Remaining Action Items

- [ ] Add `git merge-tree --write-tree` for in-memory conflict detection before merge
- [ ] Filter out `.beads/` conflicts (handled by `bd sync`)

---

## 5. Start+Work Prompt Structure ✓ IMPLEMENTED

**Gleam design** (from `docs/gleam/start-work-prompt.md`):

```
work on bead {bead-id} ({issue_type}): {title}

Run `bd show {bead-id}` to see full description and context.

Before starting implementation:
1. If ANYTHING is unclear or underspecified, ASK ME questions before proceeding
2. Once you understand the task, update the bead with your implementation plan using `bd update {bead-id} --design="..."`

Goal: Make this bead self-sufficient so any future session could pick it up without extra context.

[If images attached:]
Attached images (use Read tool to view):
/path/to/.beads/images/{bead-id}/abc123.png
```

**TypeScript implementation:** Already has this exact prompt! See `SessionHandlersService.ts:147-176`:

- ✓ "work on bead {id} ({type}): {title}"
- ✓ "Run `bd show` to see full description"
- ✓ "If ANYTHING is unclear, ASK ME questions before proceeding"
- ✓ "update the bead with your implementation plan using `bd update {id} --design=...`"
- ✓ "Goal: Make this bead self-sufficient"
- ✓ Image attachment paths included (lines 169-176)
- ✓ Worktree continuation context for resumed work (lines 158-165)

---

## 6. Init Commands: Run Once Per Session ✓ IMPLEMENTED

**Gleam design**: Init commands (direnv, bun install, bd sync) run **only once** when tmux session is first created. Marked with `@az_init_done` tmux variable.

**TypeScript implementation:** Already uses `@az_init_done` marker! See `WorktreeSessionService.ts:257-265`:

- ✓ Sends init commands sequentially via `tmux.sendKeys()`
- ✓ Sets `@az_init_done` marker after completion
- ✓ New windows wait for marker before running commands (lines 316-317)
- ✓ Shell ready detection with `@az_shell_ready` marker

---

## 7. Background Task Window Lifecycle

**Gleam design**: Background task windows **close on success**, stay open **only on failure** for debugging.

### Action Items

- [ ] Implement auto-close on exit 0
- [ ] Keep window open on non-zero exit for debugging
- [ ] Add toast notification on background task failure

---

## 8. Git Workflow Configuration

**Gleam design** adds explicit workflow configuration:

```json
{
  "git": {
    "workflowMode": "origin",  // "local" for direct merge, "origin" for PR
    "pushBranchOnCreate": true,
    "pushEnabled": true,       // kill switch
    "fetchEnabled": true,      // kill switch
    "baseBranch": "main",
    "remote": "origin",
    "branchPrefix": "az-"
  }
}
```

### Action Items

- [ ] Add `pushEnabled`/`fetchEnabled` kill switches
- [ ] Make base branch configurable (not hardcoded "main")
- [ ] Add settings overlay toggles for push/fetch

---

## 9. Polling Interval Consolidation

**Gleam design**:
```json
{
  "polling": {
    "beadsRefresh": 30000,
    "sessionMonitor": 500
  }
}
```

Two configurable intervals, not scattered magic numbers.

### Action Items

- [ ] Audit all polling intervals in codebase
- [ ] Consolidate into config
- [ ] Document default values and tradeoffs

---

## 10. Domain Type Design

The Gleam domain types are notably clean. Example from `session.gleam`:

```gleam
pub type State {
  Idle
  Busy
  Waiting
  Done
  Error
  Paused
  Unknown
}

pub fn state_rank(state: State) -> Int {
  case state {
    Waiting -> 0  // Needs attention first
    Busy -> 1
    Error -> 2
    Paused -> 3
    Done -> 4
    Idle -> 5
    Unknown -> 6
  }
}
```

**Key insight**: State has explicit **priority ranking** for sorting. Waiting comes first because it needs human attention.

### Action Items

- [ ] Review TypeScript session state types
- [ ] Add explicit sort ranking (Waiting > Busy > Error > ...)
- [ ] Ensure consistent state display across app

---

## 11. Feature Deferrals (Scope Control)

The Gleam planning explicitly moved these to v2+:
- Epic orchestration / swarm pattern
- VC integration
- Command mode (`:`)
- Compact view
- Keybind customization

This suggests the current TS implementation may have scope creep.

### Action Items

- [ ] Audit features: are VC/command mode/compact view actually used?
- [ ] Consider deprecating unused features
- [ ] Document which features are "experimental"

---

## 12. OTP-Inspired Fault Tolerance

**Gleam design**: Session monitors auto-restart on crash, reconstruct state from tmux.

```
Monitor crashes:
  → Supervisor auto-restarts
  → Monitor polls tmux for current state
  → UI shows "refreshing..." briefly
  → Normal operation resumes

If 3 crashes in 60 seconds:
  → Mark session/server "unknown"
  → Surface toast warning
```

### Action Items

- [ ] Add crash counting to session monitors
- [ ] Implement "unknown" state after repeated failures
- [ ] Surface toast warnings on monitor failures
- [ ] Consider Effect `Scope` patterns for monitor lifecycle

---

## Implementation Priority

### Already Done ✓
- Start+work prompt improvements
- State hierarchy with tmux source of truth
- Init command lifecycle with markers
- Merge choice UX

### Remaining Work

**Quick Wins:**
1. `git merge-tree` for in-memory conflict detection
2. Background task windows close on success
3. Polling interval consolidation

**Architecture:**
4. Service consolidation audit (45 → 10-15)
5. Feature scope audit (deprecate unused features)

**Robustness:**
6. Fault tolerance patterns (crash counting, "unknown" state)
7. Session state ranking (Waiting > Busy > Error)

---

## Appendix: Service Inventory

Current services (45 total):

**src/services/** (28):
- BoardService, ClockService, CommandQueueService, DevServerService
- DiagnosticsService, DiffService, EditorService, ErrorFormatter
- KeyboardService, MutationQueue, NavigationService, NetworkService
- OfflineService, OverlayService, ProjectService, ProjectStateService
- SessionService, SettingsService, ToastService, ViewService
- keyboard/: DevServerHandlersService, InputHandlersService, KeyboardHelpersService
- keyboard/: OrchestrateHandlersService, PRHandlersService, SessionHandlersService, TaskHandlersService

**src/core/** (17):
- AppConfig, AttachmentService, BeadEditorService, BeadsClient
- ClaudeSessionManager, FileLockManager, ImageAttachmentService
- PlanningService, PRWorkflow, PTYMonitor, StateDetector
- TemplateService, TerminalService, TmuxService, TmuxSessionMonitor
- VCService, WorktreeManager, WorktreeSessionService
