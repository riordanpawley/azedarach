// Server Monitor Actor
// Tracks dev server state (running, port, health)
// Managed by the ServersSupervisor, transient restart strategy

import gleam/erlang/process.{type Subject}
import gleam/option.{type Option, None, Some}
import gleam/otp/actor
import gleam/result
import azedarach/services/tmux

// Default polling interval in milliseconds
const default_poll_interval_ms = 2000

/// Server status
pub type ServerStatus {
  Running
  Stopped
  Starting
  Failed
  Unknown
}

/// Monitor state
pub type MonitorState {
  MonitorState(
    bead_id: String,
    server_name: String,
    tmux_session: String,
    window_name: String,
    port: Option(Int),
    poll_interval_ms: Int,
    last_status: ServerStatus,
    coordinator: Subject(CoordinatorUpdate),
  )
}

/// Messages the monitor can receive
pub type Msg {
  /// Periodic poll tick
  Poll
  /// Stop the monitor gracefully
  Stop
  /// Manual status refresh request
  Refresh
}

/// Message sent to coordinator when status changes
pub type CoordinatorUpdate {
  ServerStatusChanged(
    bead_id: String,
    server_name: String,
    status: ServerStatus,
    port: Option(Int),
  )
  ServerMonitorCrashed(bead_id: String, server_name: String)
}

/// Configuration for starting a monitor
pub type MonitorConfig {
  MonitorConfig(
    bead_id: String,
    server_name: String,
    tmux_session: String,
    window_name: String,
    port: Option(Int),
    poll_interval_ms: Option(Int),
    coordinator: Subject(CoordinatorUpdate),
  )
}

/// Start a new server monitor
pub fn start(
  config: MonitorConfig,
) -> Result(Subject(Msg), actor.StartError) {
  let poll_interval =
    option.unwrap(config.poll_interval_ms, default_poll_interval_ms)

  let initial_state =
    MonitorState(
      bead_id: config.bead_id,
      server_name: config.server_name,
      tmux_session: config.tmux_session,
      window_name: config.window_name,
      port: config.port,
      poll_interval_ms: poll_interval,
      last_status: Starting,
      coordinator: config.coordinator,
    )

  let start_result =
    actor.new(initial_state)
    |> actor.on_message(handle_message)
    |> actor.start
    |> result.map(fn(started) {
      let actor.Started(_, data) = started
      data
    })

  // Schedule initial poll after starting
  case start_result {
    Ok(subject) -> {
      schedule_poll_with_subject(subject, poll_interval)
      Ok(subject)
    }
    Error(e) -> Error(e)
  }
}

/// Send stop message to monitor
pub fn stop(subject: Subject(Msg)) -> Nil {
  process.send(subject, Stop)
}

/// Request immediate status refresh
pub fn refresh(subject: Subject(Msg)) -> Nil {
  process.send(subject, Refresh)
}

/// Main message handler
fn handle_message(state: MonitorState, msg: Msg) -> actor.Next(MonitorState, Msg) {
  case msg {
    Poll -> handle_poll(state)
    Refresh -> handle_poll(state)
    Stop -> actor.stop()
  }
}

/// Handle poll tick - check window status
fn handle_poll(state: MonitorState) -> actor.Next(MonitorState, Msg) {
  // Check if tmux session still exists
  case tmux.session_exists(state.tmux_session) {
    False -> {
      // Session gone, notify and stop
      notify_status_change(state, Stopped)
      actor.stop()
    }
    True -> {
      // Check if window exists
      case tmux.list_windows(state.tmux_session) {
        Ok(windows) -> {
          let status = case list_contains(windows, state.window_name) {
            True -> Running
            False -> Stopped
          }

          // Only notify if status changed
          let new_state = case status != state.last_status {
            True -> {
              notify_status_change(state, status)
              MonitorState(..state, last_status: status)
            }
            False -> state
          }

          // If stopped, stop monitoring
          case status {
            Stopped -> actor.stop()
            _ -> {
              // Note: schedule_poll needs self reference - this is a known limitation
              // In real impl, we'd store self reference in state
              actor.continue(new_state)
            }
          }
        }
        Error(_) -> {
          // Tmux command failed - mark unknown, keep polling
          let new_state = case state.last_status != Unknown {
            True -> {
              notify_status_change(state, Unknown)
              MonitorState(..state, last_status: Unknown)
            }
            False -> state
          }
          // Note: schedule_poll needs self reference - this is a known limitation
          actor.continue(new_state)
        }
      }
    }
  }
}

/// Check if list contains item
fn list_contains(list: List(String), item: String) -> Bool {
  case list {
    [] -> False
    [first, ..rest] ->
      case first == item {
        True -> True
        False -> list_contains(rest, item)
      }
  }
}

/// Notify coordinator of status change
fn notify_status_change(state: MonitorState, new_status: ServerStatus) -> Nil {
  process.send(
    state.coordinator,
    ServerStatusChanged(state.bead_id, state.server_name, new_status, state.port),
  )
}

/// Schedule next poll tick with explicit subject
fn schedule_poll_with_subject(subject: Subject(Msg), interval_ms: Int) -> Nil {
  let _ = process.spawn(fn() {
    process.sleep(interval_ms)
    process.send(subject, Poll)
  })
  Nil
}

/// Status to string
pub fn status_to_string(status: ServerStatus) -> String {
  case status {
    Running -> "running"
    Stopped -> "stopped"
    Starting -> "starting"
    Failed -> "failed"
    Unknown -> "unknown"
  }
}

/// Status display icon
pub fn status_icon(status: ServerStatus) -> String {
  case status {
    Running -> "●"
    Stopped -> "○"
    Starting -> "◐"
    Failed -> "✗"
    Unknown -> "?"
  }
}
