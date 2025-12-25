// Session Monitor Actor - polls tmux sessions for state changes
//
// Polls @az_status tmux session option to detect Claude session state
// changes. Sends SessionMonitorUpdate messages to the coordinator when
// state transitions are detected.
//
// This mirrors TmuxSessionMonitor.ts from the TypeScript implementation.

import gleam/dict.{type Dict}
import gleam/erlang/process.{type Subject}
import gleam/list
import gleam/option.{type Option, None, Some}
import gleam/otp/actor
import gleam/string
import azedarach/domain/session
import azedarach/services/tmux

// Polling interval in milliseconds
const poll_interval_ms = 500

// ============================================================================
// Types
// ============================================================================

/// State update sent when a session's state changes
pub type StateUpdate {
  StateUpdate(bead_id: String, state: session.State)
}

/// Callback function type for state updates
pub type UpdateCallback =
  fn(StateUpdate) -> Nil

pub type SessionMonitorState {
  SessionMonitorState(
    // Previous session states for change detection (bead_id -> State)
    previous_states: Dict(String, session.State),
    // Callback to invoke on state changes
    on_update: Option(UpdateCallback),
    // Self reference for scheduling
    self_subject: Option(Subject(Msg)),
  )
}

// Messages the session monitor can receive
pub type Msg {
  // Set the update callback
  SetCallback(UpdateCallback)
  // Trigger a poll cycle
  Poll
  // Stop the monitor
  Stop
}

// ============================================================================
// Actor Implementation
// ============================================================================

/// Start the session monitor actor
pub fn start() -> Result(Subject(Msg), actor.StartError) {
  actor.start_spec(actor.Spec(
    init: fn() {
      let state =
        SessionMonitorState(
          previous_states: dict.new(),
          on_update: None,
          self_subject: None,
        )
      actor.Ready(state, process.new_selector())
    },
    init_timeout: 5000,
    loop: handle_message,
  ))
}

/// Initialize the monitor and start polling
pub fn initialize(subject: Subject(Msg)) -> Nil {
  schedule_poll(subject)
}

/// Set the callback to receive state updates
pub fn set_callback(subject: Subject(Msg), callback: UpdateCallback) -> Nil {
  process.send(subject, SetCallback(callback))
}

/// Stop the session monitor
pub fn stop(subject: Subject(Msg)) -> Nil {
  process.send(subject, Stop)
}

// Message handler
fn handle_message(
  msg: Msg,
  state: SessionMonitorState,
) -> actor.Next(Msg, SessionMonitorState) {
  case msg {
    SetCallback(callback) -> {
      actor.continue(SessionMonitorState(..state, on_update: Some(callback)))
    }

    Poll -> {
      let new_state = poll_sessions(state)

      // Schedule next poll
      case state.self_subject {
        Some(self) -> {
          schedule_poll(self)
          actor.continue(new_state)
        }
        None -> {
          // First poll - set up self reference
          let self = process.new_subject()
          schedule_poll(self)
          actor.continue(SessionMonitorState(..new_state, self_subject: Some(self)))
        }
      }
    }

    Stop -> {
      actor.Stop(process.Normal)
    }
  }
}

// ============================================================================
// Polling Logic
// ============================================================================

/// Poll all tmux sessions and detect state changes
fn poll_sessions(state: SessionMonitorState) -> SessionMonitorState {
  case tmux.discover_az_sessions() {
    Ok(sessions) -> {
      let #(new_states, updates) =
        process_sessions(sessions, state.previous_states)

      // Invoke callback for each update
      case state.on_update {
        Some(callback) -> {
          list.each(updates, fn(update) {
            callback(update)
          })
        }
        None -> Nil
      }

      SessionMonitorState(..state, previous_states: new_states)
    }
    Error(_) -> {
      // tmux not running or error - clear state
      SessionMonitorState(..state, previous_states: dict.new())
    }
  }
}

/// Process all sessions and detect state changes
fn process_sessions(
  sessions: List(String),
  previous_states: Dict(String, session.State),
) -> #(Dict(String, session.State), List(StateUpdate)) {
  let new_states = dict.new()
  let updates = []

  // Process each session
  let #(new_states, updates) =
    list.fold(sessions, #(new_states, updates), fn(acc, session_name) {
      let #(states, updates) = acc
      case parse_bead_id(session_name) {
        Some(bead_id) -> {
          let current_state = get_session_state(session_name)
          let updated_states = dict.insert(states, bead_id, current_state)

          // Check if state changed
          case dict.get(previous_states, bead_id) {
            Ok(prev_state) if prev_state != current_state -> {
              // State changed - create update
              let update = StateUpdate(bead_id, current_state)
              #(updated_states, [update, ..updates])
            }
            Error(_) -> {
              // New session - also send update for initial state
              let update = StateUpdate(bead_id, current_state)
              #(updated_states, [update, ..updates])
            }
            _ -> {
              // No change
              #(updated_states, updates)
            }
          }
        }
        None -> acc
      }
    })

  // Check for sessions that disappeared (session ended)
  let #(final_states, final_updates) =
    dict.fold(previous_states, #(new_states, updates), fn(acc, bead_id, _prev_state) {
      let #(states, updates) = acc
      case dict.has_key(new_states, bead_id) {
        True -> acc
        False -> {
          // Session disappeared - send Idle update
          let update = StateUpdate(bead_id, session.Idle)
          #(states, [update, ..updates])
        }
      }
    })

  #(final_states, final_updates)
}

/// Get current state from tmux session option
fn get_session_state(session_name: String) -> session.State {
  case tmux.get_option(session_name, "@az_status") {
    Ok(status) -> session.state_from_string(status)
    Error(_) -> {
      // Fall back to detecting from pane output
      // Default to Busy for sessions that haven't set status yet
      session.Busy
    }
  }
}

/// Parse bead ID from session name (e.g., "az-abc123" -> "abc123")
fn parse_bead_id(session_name: String) -> Option(String) {
  case string.starts_with(session_name, "az-") {
    True -> Some(string.drop_start(session_name, 3))
    False -> {
      // Also handle sessions ending in -az (legacy format)
      case string.ends_with(session_name, "-az") {
        True -> {
          let len = string.length(session_name)
          Some(string.slice(session_name, 0, len - 3))
        }
        False -> None
      }
    }
  }
}

/// Schedule next poll after interval
fn schedule_poll(subject: Subject(Msg)) -> Nil {
  process.start(
    fn() {
      process.sleep(poll_interval_ms)
      process.send(subject, Poll)
    },
    True,
  )
  Nil
}
