// Session Monitor Actor
// Polls tmux pane output and detects Claude session state changes
// Managed by the SessionsSupervisor, transient restart strategy

import gleam/erlang/process.{type Subject}
import gleam/option.{type Option, None, Some}
import gleam/otp/actor
import gleam/result
import azedarach/domain/session.{type State}
import azedarach/services/tmux
import azedarach/services/state_detector

// Default polling interval in milliseconds
const default_poll_interval_ms = 500

// Lines to capture from tmux pane
const capture_lines = 50

// Crash threshold for "unknown" state
const max_crashes = 3

const crash_window_ms = 60_000

/// Monitor state
pub type MonitorState {
  MonitorState(
    bead_id: String,
    tmux_session: String,
    poll_interval_ms: Int,
    last_state: State,
    coordinator: Subject(CoordinatorUpdate),
    crash_count: Int,
    first_crash_at: Option(Int),
    // Self reference for scheduling
    self_subject: Option(Subject(Msg)),
  )
}

/// Messages the monitor can receive
pub type Msg {
  /// Set self reference and trigger first poll
  SetSelf(Subject(Msg))
  /// Periodic poll tick
  Poll
  /// Stop the monitor gracefully
  Stop
  /// Manual state refresh request
  Refresh
}

/// Message sent to coordinator when state changes
pub type CoordinatorUpdate {
  StateChanged(bead_id: String, state: State)
  MonitorCrashed(bead_id: String, crash_count: Int)
}

/// Configuration for starting a monitor
pub type MonitorConfig {
  MonitorConfig(
    bead_id: String,
    tmux_session: String,
    poll_interval_ms: Option(Int),
    coordinator: Subject(CoordinatorUpdate),
  )
}

/// Start a new session monitor
pub fn start(
  config: MonitorConfig,
) -> Result(Subject(Msg), actor.StartError) {
  let poll_interval = option.unwrap(config.poll_interval_ms, default_poll_interval_ms)

  let initial_state =
    MonitorState(
      bead_id: config.bead_id,
      tmux_session: config.tmux_session,
      poll_interval_ms: poll_interval,
      last_state: session.Idle,
      coordinator: config.coordinator,
      crash_count: 0,
      first_crash_at: None,
      self_subject: None,
    )

  let result =
    actor.new(initial_state)
    |> actor.on_message(handle_message)
    |> actor.start
    |> result.map(fn(started) {
      let actor.Started(_, data) = started
      data
    })

  // After starting, send a message to set self reference and trigger first poll
  case result {
    Ok(subject) -> {
      process.send(subject, SetSelf(subject))
      Ok(subject)
    }
    Error(e) -> Error(e)
  }
}

/// Send stop message to monitor
pub fn stop(subject: Subject(Msg)) -> Nil {
  process.send(subject, Stop)
}

/// Request immediate state refresh
pub fn refresh(subject: Subject(Msg)) -> Nil {
  process.send(subject, Refresh)
}

/// Main message handler
fn handle_message(state: MonitorState, msg: Msg) -> actor.Next(MonitorState, Msg) {
  case msg {
    SetSelf(subject) -> {
      // Store self reference and schedule first poll
      let new_state = MonitorState(..state, self_subject: Some(subject))
      schedule_poll_with_subject(subject, new_state.poll_interval_ms)
      actor.continue(new_state)
    }
    Poll -> handle_poll(state)
    Refresh -> handle_poll(state)
    Stop -> actor.stop()
  }
}

/// Handle poll tick - capture pane and detect state
fn handle_poll(state: MonitorState) -> actor.Next(MonitorState, Msg) {
  // Check if tmux session still exists
  case tmux.session_exists(state.tmux_session) {
    False -> {
      // Session gone, notify and stop
      notify_state_change(state, session.Idle)
      actor.stop()
    }
    True -> {
      // Capture pane and detect state
      case tmux.capture_pane(state.tmux_session <> ":main", capture_lines) {
        Ok(output) -> {
          let detected = state_detector.detect(output)

          // Only notify if state changed
          let new_state = case detected != state.last_state {
            True -> {
              notify_state_change(state, detected)
              MonitorState(..state, last_state: detected)
            }
            False -> state
          }

          // Schedule next poll using self reference
          case state.self_subject {
            Some(subject) ->
              schedule_poll_with_subject(subject, state.poll_interval_ms)
            None -> Nil
          }
          actor.continue(new_state)
        }
        Error(_) -> {
          // Tmux command failed - might be transient, keep polling
          case state.self_subject {
            Some(subject) ->
              schedule_poll_with_subject(subject, state.poll_interval_ms)
            None -> Nil
          }
          actor.continue(state)
        }
      }
    }
  }
}

/// Notify coordinator of state change
fn notify_state_change(state: MonitorState, new_state: State) -> Nil {
  process.send(state.coordinator, StateChanged(state.bead_id, new_state))
}

/// Schedule next poll tick with explicit subject
fn schedule_poll_with_subject(subject: Subject(Msg), interval_ms: Int) -> Nil {
  // Spawn a linked process to sleep and then send Poll message
  let _ = process.spawn(fn() {
    process.sleep(interval_ms)
    process.send(subject, Poll)
  })
  Nil
}

/// Get child spec for supervision
pub fn child_spec(config: MonitorConfig) -> ChildSpec {
  ChildSpec(
    id: "session_monitor_" <> config.bead_id,
    start: fn() { start(config) },
    restart: Transient,
    shutdown_ms: 5000,
  )
}

/// Restart strategy for supervisors
pub type RestartStrategy {
  /// Always restart
  Permanent
  /// Restart only on abnormal exit
  Transient
  /// Never restart
  Temporary
}

/// Child specification for supervisor
pub type ChildSpec {
  ChildSpec(
    id: String,
    start: fn() -> Result(Subject(Msg), actor.StartError),
    restart: RestartStrategy,
    shutdown_ms: Int,
  )
}
