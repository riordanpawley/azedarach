// Dev Server State Types and Persistence
// Rich state model matching TypeScript implementation

import gleam/dynamic/decode.{type Decoder}
import gleam/json
import gleam/option.{type Option, None, Some}
import gleam/result
import gleam/string
import azedarach/services/tmux

/// Dev server status - matches TypeScript DevServerStatus
pub type DevServerStatus {
  Idle
  Starting
  Running
  Error(String)
}

/// Full dev server state - matches TypeScript DevServerState
pub type DevServerState {
  DevServerState(
    name: String,
    status: DevServerStatus,
    port: Option(Int),
    window_name: String,
    tmux_session: Option(String),
    worktree_path: Option(String),
    started_at: Option(Int),
    error: Option(String),
  )
}

/// Create an idle state for a server
pub fn make_idle(name: String) -> DevServerState {
  DevServerState(
    name: name,
    status: Idle,
    port: None,
    window_name: name,
    tmux_session: None,
    worktree_path: None,
    started_at: None,
    error: None,
  )
}

/// Check if server is running
pub fn is_running(state: DevServerState) -> Bool {
  case state.status {
    Running -> True
    _ -> False
  }
}

/// Check if server is starting
pub fn is_starting(state: DevServerState) -> Bool {
  case state.status {
    Starting -> True
    _ -> False
  }
}

/// Check if server has error
pub fn has_error(state: DevServerState) -> Bool {
  case state.status {
    Error(_) -> True
    _ -> False
  }
}

/// Status to string for display
pub fn status_to_string(status: DevServerStatus) -> String {
  case status {
    Idle -> "idle"
    Starting -> "starting"
    Running -> "running"
    Error(_) -> "error"
  }
}

/// Status icon for UI
pub fn status_icon(status: DevServerStatus) -> String {
  case status {
    Idle -> "○"
    Starting -> "◐"
    Running -> "●"
    Error(_) -> "✗"
  }
}

// ============================================================================
// Tmux Persistence
// ============================================================================

const tmux_opt_metadata = "@az-devserver-meta"

/// Metadata for tmux persistence - matches TypeScript DevServerMetadata
pub type TmuxMetadata {
  TmuxMetadata(
    bead_id: String,
    server_name: String,
    status: String,
    port: Option(Int),
    worktree_path: Option(String),
    project_path: Option(String),
    started_at: Option(Int),
    error: Option(String),
  )
}

/// Store state to tmux session option
pub fn store_to_tmux(
  session_name: String,
  bead_id: String,
  state: DevServerState,
  project_path: String,
) -> Result(Nil, tmux.TmuxError) {
  let metadata =
    TmuxMetadata(
      bead_id: bead_id,
      server_name: state.name,
      status: status_to_string(state.status),
      port: state.port,
      worktree_path: state.worktree_path,
      project_path: Some(project_path),
      started_at: state.started_at,
      error: case state.status {
        Error(msg) -> Some(msg)
        _ -> None
      },
    )

  let json_str = encode_metadata(metadata)
  tmux.set_option(session_name, tmux_opt_metadata, json_str)
}

/// Load state from tmux session option
pub fn load_from_tmux(
  session_name: String,
) -> Result(Option(DevServerState), tmux.TmuxError) {
  case tmux.session_exists(session_name) {
    False -> Ok(None)
    True -> {
      tmux.get_option(session_name, tmux_opt_metadata)
      |> result.map(fn(json_str) {
        decode_metadata(json_str)
        |> result.map(fn(meta) { Some(metadata_to_state(meta)) })
        |> result.unwrap(None)
      })
      |> result.unwrap(None)
      |> Ok
    }
  }
}

/// Convert metadata to state
fn metadata_to_state(meta: TmuxMetadata) -> DevServerState {
  let status = case meta.status {
    "idle" -> Idle
    "starting" -> Starting
    "running" -> Running
    "error" ->
      Error(option.unwrap(meta.error, "Unknown error"))
    _ -> Idle
  }

  DevServerState(
    name: meta.server_name,
    status: status,
    port: meta.port,
    window_name: meta.server_name,
    tmux_session: None,
    worktree_path: meta.worktree_path,
    started_at: meta.started_at,
    error: meta.error,
  )
}

/// Encode metadata to JSON string
fn encode_metadata(meta: TmuxMetadata) -> String {
  let port_json = case meta.port {
    Some(p) -> json.int(p)
    None -> json.null()
  }
  let worktree_json = case meta.worktree_path {
    Some(w) -> json.string(w)
    None -> json.null()
  }
  let project_json = case meta.project_path {
    Some(p) -> json.string(p)
    None -> json.null()
  }
  let started_json = case meta.started_at {
    Some(t) -> json.int(t)
    None -> json.null()
  }
  let error_json = case meta.error {
    Some(e) -> json.string(e)
    None -> json.null()
  }

  json.to_string(
    json.object([
      #("beadId", json.string(meta.bead_id)),
      #("serverName", json.string(meta.server_name)),
      #("status", json.string(meta.status)),
      #("port", port_json),
      #("worktreePath", worktree_json),
      #("projectPath", project_json),
      #("startedAt", started_json),
      #("error", error_json),
    ]),
  )
}

/// Decode metadata from JSON string
fn decode_metadata(json_str: String) -> Result(TmuxMetadata, Nil) {
  json.parse(from: json_str, using: metadata_decoder())
  |> result.replace_error(Nil)
}

fn metadata_decoder() -> Decoder(TmuxMetadata) {
  use bead_id <- decode.field("beadId", decode.string)
  use server_name <- decode.field("serverName", decode.string)
  use status <- decode.field("status", decode.string)
  use port <- decode.optional_field("port", None, decode.optional(decode.int))
  use worktree_path <- decode.optional_field(
    "worktreePath",
    None,
    decode.optional(decode.string),
  )
  use project_path <- decode.optional_field(
    "projectPath",
    None,
    decode.optional(decode.string),
  )
  use started_at <- decode.optional_field(
    "startedAt",
    None,
    decode.optional(decode.int),
  )
  use error <- decode.optional_field(
    "error",
    None,
    decode.optional(decode.string),
  )

  decode.success(TmuxMetadata(
    bead_id:,
    server_name:,
    status:,
    port:,
    worktree_path:,
    project_path:,
    started_at:,
    error:,
  ))
}
