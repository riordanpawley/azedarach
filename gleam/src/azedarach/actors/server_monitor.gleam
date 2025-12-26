// Server Monitor Actor
// Tracks dev server state (running, port, health)
// Managed by the ServersSupervisor, transient restart strategy
// Now integrates with port detection and rich state model

import gleam/erlang/process.{type Subject}
import gleam/option.{type Option, None, Some}
import gleam/otp/actor
import azedarach/services/dev_server_state.{type DevServerState, type DevServerStatus}
import azedarach/services/port_detector
import azedarach/services/tmux

// Default polling interval in milliseconds
const default_poll_interval_ms = 2000

/// Monitor state
pub type MonitorState {
  MonitorState(
    bead_id: String,
    server_name: String,
    tmux_session: String,
    window_name: String,
    port: Option(Int),
    poll_interval_ms: Int,
    last_status: DevServerStatus,
    port_pattern: Option(String),
    worktree_path: Option(String),
    started_at: Option(Int),
    port_detector: Option(Subject(port_detector.Msg)),
    self_subject: Option(Subject(Msg)),
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
  /// Port detected from port detector
  PortDetected(Int)
  /// Port detection timed out
  PortDetectionTimeout
  /// Port detection failed
  PortDetectionFailed(String)
}

/// Message sent to coordinator when status changes
pub type CoordinatorUpdate {
  ServerStatusChanged(
    bead_id: String,
    server_name: String,
    state: DevServerState,
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
    port_pattern: Option(String),
    worktree_path: Option(String),
    coordinator: Subject(CoordinatorUpdate),
  )
}

/// Start a new server monitor
pub fn start(
  config: MonitorConfig,
) -> Result(Subject(Msg), actor.StartError) {
  let poll_interval =
    option.unwrap(config.poll_interval_ms, default_poll_interval_ms)

  // Create the actor with a two-phase init to capture self subject
  let self_subject_ref = process.new_subject()

  let result =
    actor.start_spec(actor.Spec(
      init: fn() {
        // Wait briefly for self_subject to be sent back
        let self_sub = case process.receive(self_subject_ref, 100) {
          Ok(s) -> Some(s)
          Error(_) -> None
        }

        let state =
          MonitorState(
            bead_id: config.bead_id,
            server_name: config.server_name,
            tmux_session: config.tmux_session,
            window_name: config.window_name,
            port: config.port,
            poll_interval_ms: poll_interval,
            last_status: dev_server_state.Starting,
            port_pattern: config.port_pattern,
            worktree_path: config.worktree_path,
            started_at: Some(erlang_monotonic_time()),
            port_detector: None,
            self_subject: self_sub,
            coordinator: config.coordinator,
          )

        // Schedule initial poll
        schedule_poll(poll_interval)

        actor.Ready(state, process.new_selector())
      },
      init_timeout: 5000,
      loop: handle_message,
    ))

  // Send the self subject back to the actor
  case result {
    Ok(subject) -> process.send(self_subject_ref, subject)
    Error(_) -> Nil
  }

  result
}

/// External bindings
@external(erlang, "erlang", "monotonic_time")
fn erlang_monotonic_time() -> Int

/// Send stop message to monitor
pub fn stop(subject: Subject(Msg)) -> Nil {
  process.send(subject, Stop)
}

/// Request immediate status refresh
pub fn refresh(subject: Subject(Msg)) -> Nil {
  process.send(subject, Refresh)
}

/// Main message handler
fn handle_message(msg: Msg, state: MonitorState) -> actor.Next(Msg, MonitorState) {
  case msg {
    Poll -> handle_poll(state)
    Refresh -> handle_poll(state)
    Stop -> {
      // Stop port detector if running
      case state.port_detector {
        Some(detector) -> port_detector.stop(detector)
        None -> Nil
      }
      actor.Stop(process.Normal)
    }
    PortDetected(port) -> handle_port_detected(state, port)
    PortDetectionTimeout -> handle_port_timeout(state)
    PortDetectionFailed(reason) -> handle_port_failed(state, reason)
  }
}

/// Handle poll tick - check window status
fn handle_poll(state: MonitorState) -> actor.Next(Msg, MonitorState) {
  // Check if tmux session still exists
  case tmux.session_exists(state.tmux_session) {
    False -> {
      // Session gone, notify and stop
      notify_status_change(state, dev_server_state.Error("Session stopped"))
      actor.Stop(process.Normal)
    }
    True -> {
      // Check if window exists
      case tmux.list_windows(state.tmux_session) {
        Ok(windows) -> {
          let window_exists = list_contains(windows, state.window_name)

          case window_exists {
            False -> {
              // Window gone, server stopped
              notify_status_change(state, dev_server_state.Error("Stopped unexpectedly"))
              actor.Stop(process.Normal)
            }
            True -> {
              // Window exists - check if we need to start port detection
              let new_state = case state.last_status {
                dev_server_state.Starting -> {
                  // Transition to Running and start port detection if no port yet
                  case state.port {
                    None -> start_port_detection(state)
                    Some(_) -> {
                      // Already have port, just transition to running
                      let updated = MonitorState(..state, last_status: dev_server_state.Running)
                      notify_status_change(updated, dev_server_state.Running)
                      updated
                    }
                  }
                }
                _ -> state
              }

              schedule_poll(state.poll_interval_ms)
              actor.continue(new_state)
            }
          }
        }
        Error(_) -> {
          // Tmux command failed - mark error, keep polling
          let new_state = MonitorState(
            ..state,
            last_status: dev_server_state.Error("Tmux command failed"),
          )
          notify_status_change(new_state, dev_server_state.Error("Tmux command failed"))
          schedule_poll(state.poll_interval_ms)
          actor.continue(new_state)
        }
      }
    }
  }
}

/// Start port detection in background
fn start_port_detection(state: MonitorState) -> MonitorState {
  case state.self_subject {
    None -> {
      // No self subject, just transition to running without port detection
      let updated = MonitorState(..state, last_status: dev_server_state.Running)
      notify_status_change(updated, dev_server_state.Running)
      updated
    }
    Some(self) -> {
      // Create a callback subject that will receive port detection results
      let callback = process.new_subject()

      // Forward callback messages to self
      process.start(fn() { forward_port_detection(callback, self) }, True)

      // Start the port detector
      let target = state.tmux_session <> ":" <> state.window_name
      let pattern =
        option.unwrap(state.port_pattern, port_detector.default_port_pattern)

      case port_detector.start(port_detector.DetectorConfig(
        target: target,
        pattern: pattern,
        callback: callback,
      )) {
        Ok(detector) -> {
          let updated =
            MonitorState(
              ..state,
              last_status: dev_server_state.Running,
              port_detector: Some(detector),
            )
          notify_status_change(updated, dev_server_state.Running)
          updated
        }
        Error(_) -> {
          // Failed to start detector, continue without port
          let updated =
            MonitorState(..state, last_status: dev_server_state.Running)
          notify_status_change(updated, dev_server_state.Running)
          updated
        }
      }
    }
  }
}

/// Forward port detection results to monitor
fn forward_port_detection(
  from: Subject(port_detector.DetectorResult),
  to: Subject(Msg),
) -> Nil {
  case process.receive(from, 60_000) {
    Ok(port_detector.PortDetected(port)) -> {
      process.send(to, PortDetected(port))
    }
    Ok(port_detector.DetectionTimedOut) -> {
      process.send(to, PortDetectionTimeout)
    }
    Ok(port_detector.DetectionFailed(reason)) -> {
      process.send(to, PortDetectionFailed(reason))
    }
    Error(_) -> Nil
  }
}

/// Handle port detected
fn handle_port_detected(state: MonitorState, port: Int) -> actor.Next(Msg, MonitorState) {
  let new_state = MonitorState(
    ..state,
    port: Some(port),
    port_detector: None,
  )
  notify_status_change(new_state, dev_server_state.Running)
  actor.continue(new_state)
}

/// Handle port detection timeout
fn handle_port_timeout(state: MonitorState) -> actor.Next(Msg, MonitorState) {
  // Keep running without port - not an error, just couldn't detect
  let new_state = MonitorState(..state, port_detector: None)
  actor.continue(new_state)
}

/// Handle port detection failed
fn handle_port_failed(state: MonitorState, _reason: String) -> actor.Next(Msg, MonitorState) {
  // Keep running without port
  let new_state = MonitorState(..state, port_detector: None)
  actor.continue(new_state)
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
fn notify_status_change(state: MonitorState, new_status: DevServerStatus) -> Nil {
  let dev_state =
    dev_server_state.DevServerState(
      name: state.server_name,
      status: new_status,
      port: state.port,
      window_name: state.window_name,
      tmux_session: Some(state.tmux_session),
      worktree_path: state.worktree_path,
      started_at: state.started_at,
      error: case new_status {
        dev_server_state.Error(msg) -> Some(msg)
        _ -> None
      },
    )
  process.send(
    state.coordinator,
    ServerStatusChanged(state.bead_id, state.server_name, dev_state),
  )
}

/// Schedule next poll tick
fn schedule_poll(interval_ms: Int) -> Nil {
  process.start(
    fn() {
      process.sleep(interval_ms)
      Nil
    },
    True,
  )
  Nil
}

// Re-export convenience functions from dev_server_state
pub const status_to_string = dev_server_state.status_to_string

pub const status_icon = dev_server_state.status_icon
