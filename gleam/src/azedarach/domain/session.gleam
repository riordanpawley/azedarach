// Session state domain type

import gleam/option.{type Option}
import gleam/order.{type Order}

pub type SessionState {
  SessionState(
    bead_id: String,
    state: State,
    started_at: Option(String),
    last_output: Option(String),
    worktree_path: Option(String),
    tmux_session: Option(String),
  )
}

pub type State {
  Idle
  Busy
  Waiting
  Done
  Error
  Paused
  Unknown
}

pub fn state_to_string(state: State) -> String {
  case state {
    Idle -> "idle"
    Busy -> "busy"
    Waiting -> "waiting"
    Done -> "done"
    Error -> "error"
    Paused -> "paused"
    Unknown -> "unknown"
  }
}

pub fn state_from_string(s: String) -> State {
  case s {
    "idle" -> Idle
    "busy" -> Busy
    "waiting" -> Waiting
    "done" -> Done
    "error" -> Error
    "paused" -> Paused
    _ -> Unknown
  }
}

pub fn state_display(state: State) -> String {
  case state {
    Idle -> ""
    Busy -> "BUSY"
    Waiting -> "WAIT"
    Done -> "DONE"
    Error -> "ERR"
    Paused -> "PAUSE"
    Unknown -> "???"
  }
}

pub fn state_icon(state: State) -> String {
  case state {
    Idle -> " "
    Busy -> "●"
    Waiting -> "○"
    Done -> "✓"
    Error -> "✗"
    Paused -> "‖"
    Unknown -> "?"
  }
}

// Compare states for sorting (busier states first)
pub fn compare_state(a: State, b: State) -> Order {
  let a_rank = state_rank(a)
  let b_rank = state_rank(b)
  case a_rank < b_rank {
    True -> order.Lt
    False ->
      case a_rank > b_rank {
        True -> order.Gt
        False -> order.Eq
      }
  }
}

fn state_rank(state: State) -> Int {
  case state {
    Waiting -> 0
    // Waiting needs attention first
    Busy -> 1
    Error -> 2
    Paused -> 3
    Done -> 4
    Idle -> 5
    Unknown -> 6
  }
}

pub fn is_running(state: State) -> Bool {
  case state {
    Busy | Waiting -> True
    _ -> False
  }
}

pub fn has_session(session: SessionState) -> Bool {
  option.is_some(session.tmux_session)
}
