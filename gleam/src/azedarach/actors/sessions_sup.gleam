// Sessions Supervisor
// Dynamic supervisor for session monitor actors
// Manages the lifecycle of SessionMonitor children with transient restart

import gleam/dict.{type Dict}
import gleam/erlang/process.{type Subject}
import gleam/int
import gleam/option.{type Option, None, Some}
import gleam/otp/actor
import gleam/result
import azedarach/actors/session_monitor.{type MonitorConfig}
import azedarach/domain/session.{type State}

// Crash tracking for "unknown" state after repeated failures
const max_crashes = 3

const crash_window_ms = 60_000

/// Supervisor state
pub type SupervisorState {
  SupervisorState(
    // Map of bead_id -> monitor subject
    monitors: Dict(String, Subject(session_monitor.Msg)),
    // Map of bead_id -> crash info
    crash_tracking: Dict(String, CrashInfo),
    // Subject for coordinator updates
    coordinator: Subject(CoordinatorUpdate),
    // Self reference for async operations
    self_subject: Option(Subject(Msg)),
  )
}

/// Crash tracking info
pub type CrashInfo {
  CrashInfo(count: Int, first_crash_at: Int)
}

/// Messages the supervisor handles
pub type Msg {
  /// Start a new session monitor
  StartMonitor(config: MonitorConfig)
  /// Stop a session monitor
  StopMonitor(bead_id: String)
  /// Monitor crashed notification (from process link)
  MonitorDown(bead_id: String)
  /// List active monitors
  ListMonitors(reply_to: Subject(List(String)))
  /// Internal: Handle state update from a monitor
  HandleStateChange(bead_id: String, state: State)
}

/// Messages sent to coordinator
pub type CoordinatorUpdate {
  SessionStateChanged(bead_id: String, state: State)
  SessionMarkedUnknown(bead_id: String, reason: String)
}

/// Start the sessions supervisor
pub fn start(
  coordinator: Subject(CoordinatorUpdate),
) -> Result(Subject(Msg), actor.StartError) {
  let initial_state =
    SupervisorState(
      monitors: dict.new(),
      crash_tracking: dict.new(),
      coordinator: coordinator,
      self_subject: None,
    )

  actor.new(initial_state)
  |> actor.on_message(handle_message)
  |> actor.start
  |> result.map(fn(started) {
    let actor.Started(_, data) = started
    data
  })
}

/// Start a monitor for a session
pub fn start_monitor(
  supervisor: Subject(Msg),
  bead_id: String,
  tmux_session: String,
  poll_interval_ms: Option(Int),
) -> Nil {
  let config =
    session_monitor.MonitorConfig(
      bead_id: bead_id,
      tmux_session: tmux_session,
      poll_interval_ms: poll_interval_ms,
      coordinator: create_monitor_callback(supervisor, bead_id),
    )
  process.send(supervisor, StartMonitor(config))
}

/// Stop a monitor for a session
pub fn stop_monitor(supervisor: Subject(Msg), bead_id: String) -> Nil {
  process.send(supervisor, StopMonitor(bead_id))
}

/// Get list of active monitor bead IDs
pub fn list_monitors(supervisor: Subject(Msg)) -> List(String) {
  let reply_subject = process.new_subject()
  process.send(supervisor, ListMonitors(reply_subject))
  // Wait for reply with timeout
  case process.receive(reply_subject, 5000) {
    Ok(monitors) -> monitors
    Error(_) -> []
  }
}

/// Main message handler
fn handle_message(
  state: SupervisorState,
  msg: Msg,
) -> actor.Next(SupervisorState, Msg) {
  case msg {
    StartMonitor(config) -> handle_start_monitor(state, config)

    StopMonitor(bead_id) -> handle_stop_monitor(state, bead_id)

    MonitorDown(bead_id) -> handle_monitor_down(state, bead_id)

    ListMonitors(reply_to) -> {
      let ids = dict.keys(state.monitors)
      process.send(reply_to, ids)
      actor.continue(state)
    }

    HandleStateChange(bead_id, new_state) -> {
      // Forward to coordinator
      process.send(state.coordinator, SessionStateChanged(bead_id, new_state))
      actor.continue(state)
    }
  }
}

/// Handle starting a new monitor
fn handle_start_monitor(
  state: SupervisorState,
  config: MonitorConfig,
) -> actor.Next(SupervisorState, Msg) {
  // Check if monitor already exists
  case dict.get(state.monitors, config.bead_id) {
    Ok(_) -> {
      // Already monitoring, ignore
      actor.continue(state)
    }
    Error(_) -> {
      // Start new monitor
      case session_monitor.start(config) {
        Ok(monitor_subject) -> {
          let new_monitors =
            dict.insert(state.monitors, config.bead_id, monitor_subject)
          actor.continue(SupervisorState(..state, monitors: new_monitors))
        }
        Error(_) -> {
          // Failed to start, track crash
          let new_state = track_crash(state, config.bead_id)
          actor.continue(new_state)
        }
      }
    }
  }
}

/// Handle stopping a monitor
fn handle_stop_monitor(
  state: SupervisorState,
  bead_id: String,
) -> actor.Next(SupervisorState, Msg) {
  case dict.get(state.monitors, bead_id) {
    Ok(monitor) -> {
      session_monitor.stop(monitor)
      let new_monitors = dict.delete(state.monitors, bead_id)
      actor.continue(SupervisorState(..state, monitors: new_monitors))
    }
    Error(_) -> actor.continue(state)
  }
}

/// Handle monitor crash (would be called from process link in real impl)
fn handle_monitor_down(
  state: SupervisorState,
  bead_id: String,
) -> actor.Next(SupervisorState, Msg) {
  // Remove from monitors
  let new_monitors = dict.delete(state.monitors, bead_id)

  // Track crash
  let new_state =
    track_crash(SupervisorState(..state, monitors: new_monitors), bead_id)

  // Check if we should mark as unknown
  case dict.get(new_state.crash_tracking, bead_id) {
    Ok(info) if info.count >= max_crashes -> {
      process.send(
        state.coordinator,
        SessionMarkedUnknown(bead_id, "Too many crashes (" <> int.to_string(info.count) <> " in 60s)"),
      )
      // Clear crash tracking
      let cleared =
        SupervisorState(
          ..new_state,
          crash_tracking: dict.delete(new_state.crash_tracking, bead_id),
        )
      actor.continue(cleared)
    }
    _ -> {
      // Attempt restart with transient strategy
      // In a real impl, we'd re-read the config and restart
      actor.continue(new_state)
    }
  }
}

/// Track a crash for a bead
fn track_crash(state: SupervisorState, bead_id: String) -> SupervisorState {
  let now = erlang_monotonic_time()

  case dict.get(state.crash_tracking, bead_id) {
    Ok(info) -> {
      // Check if within crash window
      case now - info.first_crash_at < crash_window_ms {
        True -> {
          // Within window, increment count
          let new_info = CrashInfo(count: info.count + 1, first_crash_at: info.first_crash_at)
          let new_tracking = dict.insert(state.crash_tracking, bead_id, new_info)
          SupervisorState(..state, crash_tracking: new_tracking)
        }
        False -> {
          // Outside window, reset
          let new_info = CrashInfo(count: 1, first_crash_at: now)
          let new_tracking = dict.insert(state.crash_tracking, bead_id, new_info)
          SupervisorState(..state, crash_tracking: new_tracking)
        }
      }
    }
    Error(_) -> {
      // First crash
      let info = CrashInfo(count: 1, first_crash_at: now)
      let new_tracking = dict.insert(state.crash_tracking, bead_id, info)
      SupervisorState(..state, crash_tracking: new_tracking)
    }
  }
}

/// Create a callback subject that routes monitor updates to the supervisor
fn create_monitor_callback(
  supervisor: Subject(Msg),
  bead_id: String,
) -> Subject(session_monitor.CoordinatorUpdate) {
  // In a real impl, we'd use a proper mapping
  // For now, create a simple forwarding subject
  let callback_subject = process.new_subject()

  // Spawn a linked process to forward messages
  let _ = process.spawn(fn() {
    forward_monitor_updates(callback_subject, supervisor, bead_id)
  })

  callback_subject
}

/// Forward monitor updates to supervisor
fn forward_monitor_updates(
  from: Subject(session_monitor.CoordinatorUpdate),
  to: Subject(Msg),
  bead_id: String,
) -> Nil {
  case process.receive(from, 60_000) {
    Ok(session_monitor.StateChanged(_, state)) -> {
      process.send(to, HandleStateChange(bead_id, state))
      forward_monitor_updates(from, to, bead_id)
    }
    Ok(session_monitor.MonitorCrashed(_, _)) -> {
      process.send(to, MonitorDown(bead_id))
      Nil
    }
    Error(_) -> {
      // Timeout or closed, stop forwarding
      Nil
    }
  }
}

/// External bindings
@external(erlang, "erlang", "monotonic_time")
fn erlang_monotonic_time() -> Int
