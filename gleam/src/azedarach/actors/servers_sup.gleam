// Servers Supervisor
// Dynamic supervisor for server monitor actors
// Manages the lifecycle of ServerMonitor children with transient restart

import gleam/dict.{type Dict}
import gleam/erlang/process.{type Subject}
import gleam/option.{type Option, None, Some}
import gleam/otp/actor
import gleam/string
import azedarach/actors/server_monitor.{type MonitorConfig}
import azedarach/services/dev_server_state.{type DevServerState}

// Crash tracking for "unknown" state after repeated failures
const max_crashes = 3

const crash_window_ms = 60_000

/// Supervisor state
pub type SupervisorState {
  SupervisorState(
    // Map of "bead_id:server_name" -> monitor subject
    monitors: Dict(String, Subject(server_monitor.Msg)),
    // Map of key -> crash info
    crash_tracking: Dict(String, CrashInfo),
    // Subject for coordinator updates
    coordinator: Subject(CoordinatorUpdate),
  )
}

/// Crash tracking info
pub type CrashInfo {
  CrashInfo(count: Int, first_crash_at: Int)
}

/// Messages the supervisor handles
pub type Msg {
  /// Start a new server monitor
  StartMonitor(config: MonitorConfig)
  /// Stop a server monitor
  StopMonitor(bead_id: String, server_name: String)
  /// Monitor crashed notification
  MonitorDown(key: String)
  /// List active monitors
  ListMonitors(reply_to: Subject(List(String)))
  /// Handle status update from a monitor
  HandleStatusChange(
    bead_id: String,
    server_name: String,
    state: DevServerState,
  )
}

/// Messages sent to coordinator
pub type CoordinatorUpdate {
  ServerStatusChanged(
    bead_id: String,
    server_name: String,
    state: DevServerState,
  )
  ServerMarkedUnknown(bead_id: String, server_name: String, reason: String)
}

/// Start the servers supervisor
pub fn start(
  coordinator: Subject(CoordinatorUpdate),
) -> Result(Subject(Msg), actor.StartError) {
  actor.start_spec(actor.Spec(
    init: fn() {
      let state =
        SupervisorState(
          monitors: dict.new(),
          crash_tracking: dict.new(),
          coordinator: coordinator,
        )

      actor.Ready(state, process.new_selector())
    },
    init_timeout: 5000,
    loop: handle_message,
  ))
}

/// Start a monitor for a server
pub fn start_monitor(
  supervisor: Subject(Msg),
  bead_id: String,
  server_name: String,
  tmux_session: String,
  window_name: String,
  port: Option(Int),
  poll_interval_ms: Option(Int),
  port_pattern: Option(String),
  worktree_path: Option(String),
) -> Nil {
  let config =
    server_monitor.MonitorConfig(
      bead_id: bead_id,
      server_name: server_name,
      tmux_session: tmux_session,
      window_name: window_name,
      port: port,
      poll_interval_ms: poll_interval_ms,
      port_pattern: port_pattern,
      worktree_path: worktree_path,
      coordinator: create_monitor_callback(supervisor, bead_id, server_name),
    )
  process.send(supervisor, StartMonitor(config))
}

/// Stop a monitor for a server
pub fn stop_monitor(
  supervisor: Subject(Msg),
  bead_id: String,
  server_name: String,
) -> Nil {
  process.send(supervisor, StopMonitor(bead_id, server_name))
}

/// Get list of active monitor keys
pub fn list_monitors(supervisor: Subject(Msg)) -> List(String) {
  let reply_subject = process.new_subject()
  process.send(supervisor, ListMonitors(reply_subject))
  case process.receive(reply_subject, 5000) {
    Ok(monitors) -> monitors
    Error(_) -> []
  }
}

/// Main message handler
fn handle_message(
  msg: Msg,
  state: SupervisorState,
) -> actor.Next(Msg, SupervisorState) {
  case msg {
    StartMonitor(config) -> handle_start_monitor(state, config)

    StopMonitor(bead_id, server_name) -> {
      let key = make_key(bead_id, server_name)
      handle_stop_monitor(state, key)
    }

    MonitorDown(key) -> handle_monitor_down(state, key)

    ListMonitors(reply_to) -> {
      let ids = dict.keys(state.monitors)
      process.send(reply_to, ids)
      actor.continue(state)
    }

    HandleStatusChange(bead_id, server_name, dev_state) -> {
      process.send(
        state.coordinator,
        ServerStatusChanged(bead_id, server_name, dev_state),
      )
      actor.continue(state)
    }
  }
}

/// Handle starting a new monitor
fn handle_start_monitor(
  state: SupervisorState,
  config: MonitorConfig,
) -> actor.Next(Msg, SupervisorState) {
  let key = make_key(config.bead_id, config.server_name)

  // Check if monitor already exists
  case dict.get(state.monitors, key) {
    Ok(_) -> {
      // Already monitoring, ignore
      actor.continue(state)
    }
    Error(_) -> {
      // Start new monitor
      case server_monitor.start(config) {
        Ok(monitor_subject) -> {
          let new_monitors = dict.insert(state.monitors, key, monitor_subject)
          actor.continue(SupervisorState(..state, monitors: new_monitors))
        }
        Error(_) -> {
          // Failed to start, track crash
          let new_state = track_crash(state, key)
          actor.continue(new_state)
        }
      }
    }
  }
}

/// Handle stopping a monitor
fn handle_stop_monitor(
  state: SupervisorState,
  key: String,
) -> actor.Next(Msg, SupervisorState) {
  case dict.get(state.monitors, key) {
    Ok(monitor) -> {
      server_monitor.stop(monitor)
      let new_monitors = dict.delete(state.monitors, key)
      actor.continue(SupervisorState(..state, monitors: new_monitors))
    }
    Error(_) -> actor.continue(state)
  }
}

/// Handle monitor crash
fn handle_monitor_down(
  state: SupervisorState,
  key: String,
) -> actor.Next(Msg, SupervisorState) {
  // Remove from monitors
  let new_monitors = dict.delete(state.monitors, key)

  // Track crash
  let new_state =
    track_crash(SupervisorState(..state, monitors: new_monitors), key)

  // Check if we should mark as unknown
  case dict.get(new_state.crash_tracking, key) {
    Ok(info) if info.count >= max_crashes -> {
      case parse_key(key) {
        Ok(#(bead_id, server_name)) -> {
          process.send(
            state.coordinator,
            ServerMarkedUnknown(bead_id, server_name, "Too many crashes"),
          )
        }
        Error(_) -> Nil
      }
      // Clear crash tracking
      let cleared =
        SupervisorState(
          ..new_state,
          crash_tracking: dict.delete(new_state.crash_tracking, key),
        )
      actor.continue(cleared)
    }
    _ -> actor.continue(new_state)
  }
}

/// Track a crash for a key
fn track_crash(state: SupervisorState, key: String) -> SupervisorState {
  let now = erlang_monotonic_time()

  case dict.get(state.crash_tracking, key) {
    Ok(info) -> {
      case now - info.first_crash_at < crash_window_ms {
        True -> {
          let new_info =
            CrashInfo(count: info.count + 1, first_crash_at: info.first_crash_at)
          let new_tracking = dict.insert(state.crash_tracking, key, new_info)
          SupervisorState(..state, crash_tracking: new_tracking)
        }
        False -> {
          let new_info = CrashInfo(count: 1, first_crash_at: now)
          let new_tracking = dict.insert(state.crash_tracking, key, new_info)
          SupervisorState(..state, crash_tracking: new_tracking)
        }
      }
    }
    Error(_) -> {
      let info = CrashInfo(count: 1, first_crash_at: now)
      let new_tracking = dict.insert(state.crash_tracking, key, info)
      SupervisorState(..state, crash_tracking: new_tracking)
    }
  }
}

/// Create key from bead_id and server_name
fn make_key(bead_id: String, server_name: String) -> String {
  bead_id <> ":" <> server_name
}

/// Parse key back to bead_id and server_name
fn parse_key(key: String) -> Result(#(String, String), Nil) {
  case string.split_once(key, ":") {
    Ok(#(bead_id, server_name)) -> Ok(#(bead_id, server_name))
    Error(_) -> Error(Nil)
  }
}

/// Create a callback subject for monitor updates
fn create_monitor_callback(
  supervisor: Subject(Msg),
  bead_id: String,
  server_name: String,
) -> Subject(server_monitor.CoordinatorUpdate) {
  let callback_subject = process.new_subject()

  process.start(
    fn() {
      forward_monitor_updates(callback_subject, supervisor, bead_id, server_name)
    },
    True,
  )

  callback_subject
}

/// Forward monitor updates to supervisor
fn forward_monitor_updates(
  from: Subject(server_monitor.CoordinatorUpdate),
  to: Subject(Msg),
  bead_id: String,
  server_name: String,
) -> Nil {
  case process.receive(from, 60_000) {
    Ok(server_monitor.ServerStatusChanged(_, _, dev_state)) -> {
      process.send(to, HandleStatusChange(bead_id, server_name, dev_state))
      forward_monitor_updates(from, to, bead_id, server_name)
    }
    Ok(server_monitor.ServerMonitorCrashed(_, _)) -> {
      process.send(to, MonitorDown(make_key(bead_id, server_name)))
      Nil
    }
    Error(_) -> Nil
  }
}

/// External bindings
@external(erlang, "erlang", "monotonic_time")
fn erlang_monotonic_time() -> Int
