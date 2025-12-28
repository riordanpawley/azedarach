// Shore Effects - side effect functions for TEA architecture
//
// All side effects in the application go through Shore's effect system.
// Effects are thunks (zero-argument functions) that:
// 1. Perform the side effect (e.g., coordinator.send)
// 2. Return a message for Shore to dispatch
//
// This ensures:
// - Side effects are properly sequenced by Shore
// - The update function remains pure (returns effects, doesn't execute them)
// - Better testability (effects can be inspected without execution)

import gleam/erlang/process.{type Subject}
import gleam/list
import azedarach/ui/model.{type Msg}
import azedarach/actors/coordinator
import azedarach/domain/task
import azedarach/util/logger

/// Effect type - a list of thunks that return messages
pub type Effect(msg) =
  List(fn() -> msg)

/// No effects - pure state update
pub fn none() -> Effect(msg) {
  []
}

/// Single effect from a thunk
pub fn from(thunk: fn() -> msg) -> Effect(msg) {
  [thunk]
}

/// Batch multiple effects into one
pub fn batch(effects: List(Effect(msg))) -> Effect(msg) {
  effects
  |> list.flatten
}

/// Map a function over effect messages
pub fn map(effect: Effect(a), f: fn(a) -> b) -> Effect(b) {
  list.map(effect, fn(thunk) { fn() { f(thunk()) } })
}

// =============================================================================
// Coordinator Effects
// =============================================================================
// These create effects that send messages to the coordinator actor.
// Each returns a Tick message after sending, which triggers UI refresh.

/// Refresh beads data
pub fn refresh_beads(coord: Subject(coordinator.Msg)) -> Effect(Msg) {
  from(fn() {
    coordinator.send(coord, coordinator.RefreshBeads)
    model.Tick
  })
}

/// Switch to a different project
pub fn switch_project(coord: Subject(coordinator.Msg), path: String) -> Effect(Msg) {
  from(fn() {
    coordinator.send(coord, coordinator.SwitchProject(path))
    model.Tick
  })
}

/// Start a Claude session for a task
pub fn start_session(
  coord: Subject(coordinator.Msg),
  id: String,
  with_work: Bool,
  yolo: Bool,
) -> Effect(Msg) {
  from(fn() {
    coordinator.send(coord, coordinator.StartSession(id, with_work, yolo))
    model.Tick
  })
}

/// Attach to an existing session
pub fn attach_session(
  coord: Subject(coordinator.Msg),
  id: String,
) -> Effect(Msg) {
  from(fn() {
    coordinator.send(coord, coordinator.AttachSession(id))
    model.Tick
  })
}

/// Pause a running session
pub fn pause_session(
  coord: Subject(coordinator.Msg),
  id: String,
) -> Effect(Msg) {
  from(fn() {
    coordinator.send(coord, coordinator.PauseSession(id))
    model.Tick
  })
}

/// Resume a paused session
pub fn resume_session(
  coord: Subject(coordinator.Msg),
  id: String,
) -> Effect(Msg) {
  from(fn() {
    coordinator.send(coord, coordinator.ResumeSession(id))
    model.Tick
  })
}

/// Stop a session
pub fn stop_session(
  coord: Subject(coordinator.Msg),
  id: String,
) -> Effect(Msg) {
  from(fn() {
    coordinator.send(coord, coordinator.StopSession(id))
    model.Tick
  })
}

/// Merge main and attach to session
pub fn merge_and_attach(
  coord: Subject(coordinator.Msg),
  id: String,
) -> Effect(Msg) {
  from(fn() {
    coordinator.send(coord, coordinator.MergeAndAttach(id))
    model.Tick
  })
}

/// Abort an in-progress merge
pub fn abort_merge(
  coord: Subject(coordinator.Msg),
  id: String,
) -> Effect(Msg) {
  from(fn() {
    coordinator.send(coord, coordinator.AbortMerge(id))
    model.Tick
  })
}

// =============================================================================
// Dev Server Effects
// =============================================================================

/// Toggle dev server on/off
pub fn toggle_dev_server(
  coord: Subject(coordinator.Msg),
  id: String,
  server: String,
) -> Effect(Msg) {
  from(fn() {
    coordinator.send(coord, coordinator.ToggleDevServer(id, server))
    model.Tick
  })
}

/// View dev server window
pub fn view_dev_server(
  coord: Subject(coordinator.Msg),
  id: String,
  server: String,
) -> Effect(Msg) {
  from(fn() {
    coordinator.send(coord, coordinator.ViewDevServer(id, server))
    model.Tick
  })
}

/// Restart dev server
pub fn restart_dev_server(
  coord: Subject(coordinator.Msg),
  id: String,
  server: String,
) -> Effect(Msg) {
  from(fn() {
    coordinator.send(coord, coordinator.RestartDevServer(id, server))
    model.Tick
  })
}

// =============================================================================
// Git Effects
// =============================================================================

/// Update worktree from main branch
pub fn update_from_main(
  coord: Subject(coordinator.Msg),
  id: String,
) -> Effect(Msg) {
  from(fn() {
    coordinator.send(coord, coordinator.UpdateFromMain(id))
    model.Tick
  })
}

/// Merge worktree to main
pub fn merge_to_main(
  coord: Subject(coordinator.Msg),
  id: String,
) -> Effect(Msg) {
  from(fn() {
    coordinator.send(coord, coordinator.MergeToMain(id))
    model.Tick
  })
}

/// Create a pull request
pub fn create_pr(coord: Subject(coordinator.Msg), id: String) -> Effect(Msg) {
  from(fn() {
    coordinator.send(coord, coordinator.CreatePR(id))
    model.Tick
  })
}

/// Delete worktree and cleanup
pub fn delete_cleanup(
  coord: Subject(coordinator.Msg),
  id: String,
) -> Effect(Msg) {
  from(fn() {
    coordinator.send(coord, coordinator.DeleteCleanup(id))
    model.Tick
  })
}

// =============================================================================
// Task/Bead Effects
// =============================================================================

/// Move task to adjacent column
pub fn move_task(
  coord: Subject(coordinator.Msg),
  id: String,
  direction: Int,
) -> Effect(Msg) {
  from(fn() {
    coordinator.send(coord, coordinator.MoveTask(id, direction))
    model.Tick
  })
}

/// Create a new bead
pub fn create_bead(coord: Subject(coordinator.Msg)) -> Effect(Msg) {
  from(fn() {
    coordinator.send(coord, coordinator.CreateBeadViaEditor(task.TaskType))
    model.Tick
  })
}

/// Create bead with Claude integration
pub fn create_bead_with_claude(coord: Subject(coordinator.Msg)) -> Effect(Msg) {
  from(fn() {
    coordinator.send(coord, coordinator.CreateBeadViaEditor(task.TaskType))
    model.Tick
  })
}

/// Edit an existing bead
pub fn edit_bead(coord: Subject(coordinator.Msg), id: String) -> Effect(Msg) {
  from(fn() {
    coordinator.send(coord, coordinator.EditBead(id))
    model.Tick
  })
}

/// Delete a bead
pub fn delete_bead(coord: Subject(coordinator.Msg), id: String) -> Effect(Msg) {
  from(fn() {
    coordinator.send(coord, coordinator.DeleteBead(id))
    model.Tick
  })
}

// =============================================================================
// Image Effects
// =============================================================================

/// Paste image from clipboard
pub fn paste_image(coord: Subject(coordinator.Msg), id: String) -> Effect(Msg) {
  from(fn() {
    coordinator.send(coord, coordinator.PasteImage(id))
    model.Tick
  })
}

/// Attach image from file path
pub fn attach_file(
  coord: Subject(coordinator.Msg),
  id: String,
  path: String,
) -> Effect(Msg) {
  from(fn() {
    coordinator.send(coord, coordinator.AttachFile(id, path))
    model.Tick
  })
}

/// Open image in system viewer
pub fn open_image(
  coord: Subject(coordinator.Msg),
  id: String,
  attachment_id: String,
) -> Effect(Msg) {
  from(fn() {
    coordinator.send(coord, coordinator.OpenImage(id, attachment_id))
    model.Tick
  })
}

/// Delete an attached image
pub fn delete_image(
  coord: Subject(coordinator.Msg),
  id: String,
  attachment_id: String,
) -> Effect(Msg) {
  from(fn() {
    coordinator.send(coord, coordinator.DeleteImage(id, attachment_id))
    model.Tick
  })
}

// =============================================================================
// Toast Effects
// =============================================================================

/// Schedule a toast expiration message after the specified delay
/// The delay_ms is calculated as (expires_at - now_ms) in the update handler
pub fn schedule_toast_expiration(
  toast_id: Int,
  delay_ms: Int,
) -> Effect(Msg) {
  from(fn() {
    // Use process.sleep to wait, then return the expiration message
    // Note: This is a simple approach - in production you might want
    // to use process.send_after for true async scheduling
    let safe_delay = case delay_ms > 0 {
      True -> delay_ms
      False -> 0
    }
    process.sleep(safe_delay)
    model.ToastExpired(toast_id)
  })
}

/// Show a toast notification directly
/// This is useful for UI-local notifications or testing
pub fn show_toast(level: model.ToastLevel, message: String) -> Effect(Msg) {
  from(fn() {
    model.ShowToast(level, message)
  })
}

/// Convert coordinator toast level to model toast level
pub fn coordinator_to_model_toast_level(
  level: coordinator.ToastLevel,
) -> model.ToastLevel {
  case level {
    coordinator.Info -> model.Info
    coordinator.Success -> model.Success
    coordinator.Warning -> model.Warning
    coordinator.ErrorLevel -> model.ErrorLevel
  }
}

// =============================================================================
// Planning Effects
// =============================================================================

/// Run the planning workflow
/// This spawns a process that will send state updates back to the UI
pub fn run_planning(
  coord: Subject(coordinator.Msg),
  description: String,
) -> Effect(Msg) {
  from(fn() {
    coordinator.send(coord, coordinator.RunPlanning(description))
    model.Tick
  })
}

/// Attach to the planning session (for manual inspection)
pub fn attach_planning_session(
  coord: Subject(coordinator.Msg),
) -> Effect(Msg) {
  from(fn() {
    coordinator.send(coord, coordinator.AttachPlanningSession)
    model.Tick
  })
}

// =============================================================================
// Quit Effect
// =============================================================================

/// Send signal to exit the application
pub fn quit(exit_subject: Subject(Nil)) -> Effect(Msg) {
  logger.info("effects: quit effect created")
  from(fn() {
    logger.info("effects: quit effect EXECUTING - sending to exit_subject")
    process.send(exit_subject, Nil)
    model.Tick
  })
}
