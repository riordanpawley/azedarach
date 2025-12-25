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
//
// Optimistic Updates:
// For task moves, the UI applies changes immediately. The coordinator sends
// success/failure messages which are received via the subscription subject
// and translated to model.Msg through poll_coordinator_messages.

import gleam/erlang/process.{type Subject}
import gleam/list
import gleam/option.{type Option, None, Some}
import azedarach/ui/model.{type Msg}
import azedarach/actors/coordinator
import azedarach/domain/task

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
    coordinator.send(coord, coordinator.CreateBeadViaEditor(task.Task))
    model.Tick
  })
}

/// Create bead with Claude integration
pub fn create_bead_with_claude(coord: Subject(coordinator.Msg)) -> Effect(Msg) {
  from(fn() {
    coordinator.send(coord, coordinator.CreateBeadViaEditor(task.Task))
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
// Coordinator Message Subscription
// =============================================================================
// These functions support the optimistic update pattern by receiving async
// messages from the coordinator and translating them to model.Msg.

/// Poll the coordinator subscription for any pending messages
/// Returns the translated message or Tick if no messages pending
pub fn poll_coordinator_messages(
  subscription: Option(Subject(coordinator.UiMsg)),
) -> Effect(Msg) {
  case subscription {
    Some(subject) ->
      from(fn() {
        // Try to receive with 0 timeout (non-blocking)
        case process.receive(subject, 0) {
          Ok(ui_msg) -> translate_ui_msg(ui_msg)
          Error(_) -> model.Tick
        }
      })
    None -> none()
  }
}

/// Translate coordinator.UiMsg to model.Msg
fn translate_ui_msg(ui_msg: coordinator.UiMsg) -> Msg {
  case ui_msg {
    coordinator.TasksUpdated(tasks) -> model.BeadsLoaded(tasks)
    coordinator.SearchResults(_) -> model.Tick
    // Not currently used in this context
    coordinator.SessionStateChanged(id, state) ->
      model.SessionStateChanged(id, state)
    coordinator.DevServerStateChanged(_id, _state) -> model.Tick
    // Dev server updates handled via coordinator
    coordinator.Toast(message, level) -> {
      let model_level = case level {
        coordinator.Info -> model.Info
        coordinator.Success -> model.Success
        coordinator.Warning -> model.Warning
        coordinator.Error -> model.Error
      }
      // Create a toast with a placeholder expiration (will be set by UI)
      model.BeadsLoaded([])
      // Can't create Toast directly, so use Tick and let periodic refresh handle
      // TODO: Add proper toast message support
      let _ = message
      let _ = model_level
      model.Tick
    }
    coordinator.RequestMergeChoice(_id, _count) -> model.Tick
    // Handled via overlay
    coordinator.ProjectChanged(_) -> model.Tick
    coordinator.ProjectsUpdated(_) -> model.Tick
    // Optimistic update responses - the key messages for this feature
    coordinator.TaskMoveSucceeded(id, new_status) ->
      model.TaskMoveSucceeded(id, new_status)
    coordinator.TaskMoveFailed(id, error) -> model.TaskMoveFailed(id, error)
  }
}
