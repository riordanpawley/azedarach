# Idiomatic Gleam/OTP Refactoring Plan

## Overview
Refactor the Gleam codebase to use more idiomatic patterns for Gleam and OTP.

## Tasks

### 1. Use `use` syntax for Result chaining
- [x] `git.gleam` - Refactored with `result.try`, `result.map_error`, pipelines
- [x] `bead_editor.gleam` - Refactored `edit_bead`, `create_bead`, `get_editor`
- [x] `session_manager.gleam` - Refactored `start`, `stop`, `pause`, `resume`, `attach`, `list_active`
- [x] `image.gleam` - Already idiomatic with guard functions and `use` syntax

### 2. Monolithic coordinator refactoring
- [x] Audited coordinator message types: ~30 messages across 9 domains

**Decision: Keep as single coordinator**

Rationale:
- TUI application, not distributed system
- Coordinator serves as facade/mediator pattern
- All messages relate to orchestrating Claude sessions
- Splitting would add complexity (inter-actor messaging, routing)
- Current size is manageable for single actor

If splitting becomes necessary in the future, suggested domains:
- `BeadsActor` - CRUD operations for beads
- `SessionActor` - Session lifecycle management
- `GitActor` - Git operations (merge, PR creation)
- `DevServerActor` - Dev server management

### 3. Supervised spawns
- [x] Audited all `process.spawn` calls (9 locations)

**Decision: Current pattern is acceptable**

All spawns are for async background tasks:
- `coordinator.gleam` - beads loading, project discovery, scheduling
- `sessions_sup.gleam`, `servers_sup.gleam` - async state updates
- `session_monitor.gleam`, `server_monitor.gleam` - async polling

These are "fire-and-forget" style tasks where:
- Failures don't need to crash parent
- Results are sent via message passing
- No long-running state to maintain

For more robust OTP:
- Use `process.spawn_link` for tasks that should crash parent on failure
- Set up proper supervision for long-running workers
- Consider `gleam_otp/supervisor` for critical services

### 4. Supervision tree
- [x] Evaluated: Not required for current application scope

The app has existing supervision patterns:
- `app_supervisor.gleam` manages actor lifecycle
- `sessions_sup.gleam`, `servers_sup.gleam` manage monitors

A full OTP supervision tree would be overkill for a TUI application.

### 5. Clean up unused parameters
- [x] Audited: 15+ functions have `_config: Config` parameter

**Decision: Keep as-is**

Rationale:
- `_` prefix tells Gleam not to warn (intentional)
- Maintains consistent API across service functions
- Config may be needed in future for customization
- Removing would break API consistency

---

## Completed Changes

### git.gleam
- Added `import gleam/result`
- `commits_behind_main`, `commits_ahead_main`: Use `result.try` + pipelines
- `wip_commit`: Use `result.try` for staging, simplified commit logic
- `create_pr`: Use `result.try` for push, pipeline for PR creation
- `diff`, `fetch`: Use `result.map_error` pipelines

### bead_editor.gleam
- `edit_bead`: Flattened from 6 levels to 4 `use` statements
- `create_bead`: Flattened from 4 levels to 3 `use` statements
- `get_editor`: Use `result.lazy_or` + `result.unwrap`
- `generate_id`: Use pipeline with `result.map`

### session_manager.gleam
- Added `import gleam/result`
- `start`: Extracted `start_new_session` helper, uses `result.try`
- `stop`, `pause`, `resume`, `attach`: Use `require_session_exists` guard
- `get_state`: Use `result.lazy_unwrap` for fallback detection
- `list_active`: Use `result.try` + `option.to_result`

---

## Progress Log

### Session: 2025-12-26
- Completed `use` syntax refactoring for 4 files
- Audited coordinator structure - decided to keep monolithic
- Audited process.spawn calls - current pattern acceptable
- Audited unused params - intentional for API consistency
- All changes compile successfully
