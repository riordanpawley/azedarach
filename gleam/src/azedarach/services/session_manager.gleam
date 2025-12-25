// Session Manager - orchestrates Claude Code sessions
//
// Core service that manages the lifecycle of Claude Code sessions:
// - start(): Create worktree, tmux session, and launch Claude
// - stop(): Kill tmux session and cleanup
// - pause(): Send Ctrl+C to pause Claude
// - resume(): Continue paused session
// - get_state(): Get current session state
// - list_active(): List all running sessions

import gleam/dict.{type Dict}
import gleam/int
import gleam/list
import gleam/option.{type Option, None, Some}
import gleam/result
import gleam/string
import azedarach/config.{type Config}
import azedarach/domain/session.{type SessionState, type State, SessionState}
import azedarach/services/tmux
import azedarach/services/worktree
import azedarach/services/state_detector
import azedarach/services/git
import azedarach/util/shell

// ============================================================================
// Types
// ============================================================================

pub type SessionError {
  WorktreeError(worktree.WorktreeError)
  TmuxError(tmux.TmuxError)
  SessionNotFound(bead_id: String)
  SessionExists(bead_id: String)
  InvalidState(message: String)
}

pub fn error_to_string(err: SessionError) -> String {
  case err {
    WorktreeError(e) -> worktree.error_to_string(e)
    TmuxError(e) -> tmux.error_to_string(e)
    SessionNotFound(id) -> "Session not found: " <> id
    SessionExists(id) -> "Session already exists: " <> id
    InvalidState(msg) -> "Invalid state: " <> msg
  }
}

pub type StartOptions {
  StartOptions(
    bead_id: String,
    project_path: String,
    initial_prompt: Option(String),
    model: Option(String),
    yolo_mode: Bool,
  )
}

/// Window names used in tmux sessions
pub const window_claude = "claude"

pub const window_shell = "shell"

pub const window_dev = "dev"

// ============================================================================
// Session Naming
// ============================================================================

/// Get tmux session name for a bead
pub fn session_name(bead_id: String) -> String {
  "az-" <> bead_id
}

/// Parse bead ID from session name
pub fn parse_session_name(name: String) -> Option(String) {
  case string.starts_with(name, "az-") {
    True -> Some(string.drop_start(name, 3))
    False -> None
  }
}

// ============================================================================
// Session Lifecycle
// ============================================================================

/// Start a new Claude session for a bead
pub fn start(
  opts: StartOptions,
  config: Config,
) -> Result(SessionState, SessionError) {
  let bead_id = opts.bead_id
  let tmux_name = session_name(bead_id)

  // Check if session already exists
  case tmux.session_exists(tmux_name) {
    True -> Error(SessionExists(bead_id))
    False -> {
      // Create worktree (using project_path from opts)
      case worktree.ensure(bead_id, config, opts.project_path) {
        Ok(wt_path) -> {
          // Create tmux session
          case tmux.new_session(tmux_name, wt_path) {
            Ok(_) -> {
              // Create additional windows
              let _ = tmux.new_window(tmux_name, window_shell)

              // Install Claude Code hooks
              let _ = install_hooks(bead_id, wt_path)

              // Launch Claude in the main window
              let claude_cmd = build_claude_command(opts, config)
              let _ = tmux.send_keys(tmux_name <> ":" <> window_claude, claude_cmd <> " Enter")

              // Set session status option
              let _ = tmux.set_option(tmux_name, "@az_status", "busy")

              Ok(SessionState(
                bead_id: bead_id,
                state: session.Busy,
                started_at: Some(now_iso()),
                last_output: None,
                worktree_path: Some(wt_path),
                tmux_session: Some(tmux_name),
              ))
            }
            Error(e) -> Error(TmuxError(e))
          }
        }
        Error(e) -> Error(WorktreeError(e))
      }
    }
  }
}

/// Stop a session
pub fn stop(bead_id: String) -> Result(Nil, SessionError) {
  let tmux_name = session_name(bead_id)

  case tmux.session_exists(tmux_name) {
    False -> Error(SessionNotFound(bead_id))
    True -> {
      case tmux.kill_session(tmux_name) {
        Ok(_) -> Ok(Nil)
        Error(e) -> Error(TmuxError(e))
      }
    }
  }
}

/// Pause a session (send Ctrl+C)
pub fn pause(bead_id: String) -> Result(Nil, SessionError) {
  let tmux_name = session_name(bead_id)

  case tmux.session_exists(tmux_name) {
    False -> Error(SessionNotFound(bead_id))
    True -> {
      // Send Ctrl+C to the claude window
      let target = tmux_name <> ":" <> window_claude
      case tmux.send_keys(target, "C-c") {
        Ok(_) -> {
          // Update status
          let _ = tmux.set_option(tmux_name, "@az_status", "paused")
          Ok(Nil)
        }
        Error(e) -> Error(TmuxError(e))
      }
    }
  }
}

/// Resume a paused session
pub fn resume(bead_id: String, prompt: Option(String)) -> Result(Nil, SessionError) {
  let tmux_name = session_name(bead_id)

  case tmux.session_exists(tmux_name) {
    False -> Error(SessionNotFound(bead_id))
    True -> {
      let target = tmux_name <> ":" <> window_claude
      let resume_prompt = case prompt {
        Some(p) -> p
        None -> "/resume"
      }

      case tmux.send_keys(target, resume_prompt <> " Enter") {
        Ok(_) -> {
          let _ = tmux.set_option(tmux_name, "@az_status", "busy")
          Ok(Nil)
        }
        Error(e) -> Error(TmuxError(e))
      }
    }
  }
}

/// Attach to a session
pub fn attach(bead_id: String) -> Result(Nil, SessionError) {
  let tmux_name = session_name(bead_id)

  case tmux.session_exists(tmux_name) {
    False -> Error(SessionNotFound(bead_id))
    True -> {
      case tmux.attach(tmux_name) {
        Ok(_) -> Ok(Nil)
        Error(e) -> Error(TmuxError(e))
      }
    }
  }
}

// ============================================================================
// Session State
// ============================================================================

/// Get current state of a session
pub fn get_state(bead_id: String) -> Result(SessionState, SessionError) {
  let tmux_name = session_name(bead_id)

  case tmux.session_exists(tmux_name) {
    False -> Error(SessionNotFound(bead_id))
    True -> {
      // Try to get status from tmux option first
      let state = case tmux.get_option(tmux_name, "@az_status") {
        Ok(status) -> session.state_from_string(status)
        Error(_) -> {
          // Fall back to detecting from pane output
          case tmux.capture_pane(tmux_name <> ":" <> window_claude, 50) {
            Ok(output) -> state_detector.detect(output)
            Error(_) -> session.Unknown
          }
        }
      }

      Ok(SessionState(
        bead_id: bead_id,
        state: state,
        started_at: None,
        last_output: None,
        worktree_path: None,
        tmux_session: Some(tmux_name),
      ))
    }
  }
}

/// List all active azedarach sessions
pub fn list_active() -> Result(List(SessionState), SessionError) {
  case tmux.discover_az_sessions() {
    Ok(sessions) -> {
      let states =
        sessions
        |> list.filter_map(fn(name) {
          case parse_session_name(name) {
            Some(bead_id) -> {
              case get_state(bead_id) {
                Ok(state) -> Ok(state)
                Error(_) -> Error(Nil)
              }
            }
            None -> Error(Nil)
          }
        })
      Ok(states)
    }
    Error(e) -> Error(TmuxError(e))
  }
}

/// Check if a session exists
pub fn exists(bead_id: String) -> Bool {
  tmux.session_exists(session_name(bead_id))
}

// ============================================================================
// Claude Command Building
// ============================================================================

fn build_claude_command(opts: StartOptions, config: Config) -> String {
  let base = "claude"

  // Add model if specified
  let with_model = case opts.model {
    Some(m) -> base <> " --model " <> m
    None -> base
  }

  // Add yolo mode if enabled
  let with_yolo = case opts.yolo_mode {
    True -> with_model <> " --dangerously-skip-permissions"
    False -> with_model
  }

  // Add initial prompt if provided
  case opts.initial_prompt {
    Some(prompt) -> with_yolo <> " \"" <> escape_quotes(prompt) <> "\""
    None -> with_yolo
  }
}

fn escape_quotes(s: String) -> String {
  string.replace(s, "\"", "\\\"")
}

// ============================================================================
// Hooks Setup
// ============================================================================

/// Install Claude Code hooks for session state detection
fn install_hooks(bead_id: String, worktree_path: String) -> Result(Nil, Nil) {
  let claude_dir = worktree_path <> "/.claude"
  let settings_path = claude_dir <> "/settings.local.json"

  // Create .claude directory
  let _ = shell.mkdir_p(claude_dir)

  // Generate hooks configuration
  let hooks_json = generate_hooks_json(bead_id)

  // Write settings file
  case shell.write_file(settings_path, hooks_json) {
    Ok(_) -> Ok(Nil)
    Error(_) -> Error(Nil)
  }
}

fn generate_hooks_json(bead_id: String) -> String {
  let notify_cmd = "az notify"

  "{
  \"hooks\": {
    \"pretooluse\": [
      {
        \"matcher\": \".*\",
        \"hooks\": [
          {
            \"type\": \"command\",
            \"command\": \"" <> notify_cmd <> " pretooluse " <> bead_id <> "\"
          }
        ]
      }
    ],
    \"idle_prompt\": [
      {
        \"type\": \"command\",
        \"command\": \"" <> notify_cmd <> " idle_prompt " <> bead_id <> "\"
      }
    ],
    \"permission_request\": [
      {
        \"type\": \"command\",
        \"command\": \"" <> notify_cmd <> " permission_request " <> bead_id <> "\"
      }
    ],
    \"stop\": [
      {
        \"type\": \"command\",
        \"command\": \"" <> notify_cmd <> " stop " <> bead_id <> "\"
      }
    ],
    \"session_end\": [
      {
        \"type\": \"command\",
        \"command\": \"" <> notify_cmd <> " session_end " <> bead_id <> "\"
      }
    ]
  }
}"
}

// ============================================================================
// Utilities
// ============================================================================

fn now_iso() -> String {
  // Simplified - real impl would use datetime library
  "2025-01-01T00:00:00Z"
}
