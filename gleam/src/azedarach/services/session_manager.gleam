// Session Manager - orchestrates Claude Code sessions
//
// Core service that manages the lifecycle of Claude Code sessions:
// - start(): Create worktree, tmux session, and launch Claude
// - stop(): Kill tmux session and cleanup
// - pause(): Send Ctrl+C to pause Claude
// - resume(): Continue paused session
// - get_state(): Get current session state
// - list_active(): List all running sessions

import gleam/list
import gleam/option.{type Option, None, Some}
import gleam/result
import gleam/string
import azedarach/config.{type Config}
import azedarach/core/hooks
import azedarach/domain/session.{type SessionState, SessionState}
import azedarach/services/tmux
import azedarach/services/worktree
import azedarach/services/state_detector
import azedarach/util/shell
import tempo

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
///
/// Returns exactly the beadId for consistent naming across:
/// - Session creation
/// - Session monitoring
/// - Hook notifications
///
/// Note: The bead ID already includes the "az-" prefix (e.g., "az-05y")
pub fn session_name(bead_id: String) -> String {
  bead_id
}

/// Parse bead ID from session name
///
/// Since the session name is the bead ID, this validates the format
/// matches the expected pattern: [a-z]+-[a-z0-9]+
pub fn parse_session_name(name: String) -> Option(String) {
  // Bead IDs follow the pattern: prefix-suffix (e.g., "az-05y")
  case string.contains(name, "-") {
    True -> Some(name)
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
    False -> start_new_session(opts, config, bead_id, tmux_name)
  }
}

fn start_new_session(
  opts: StartOptions,
  config: Config,
  bead_id: String,
  tmux_name: String,
) -> Result(SessionState, SessionError) {
  // Create worktree (using project_path from opts)
  use wt_path <- result.try(
    worktree.ensure(bead_id, config, opts.project_path)
    |> result.map_error(WorktreeError),
  )

  // Create tmux session
  use _ <- result.try(
    tmux.new_session(tmux_name, wt_path)
    |> result.map_error(TmuxError),
  )

  // Create additional windows
  use _ <- result.try(
    tmux.new_window(tmux_name, window_shell)
    |> result.map_error(TmuxError),
  )

  // Install Claude Code hooks (optional - warn on failure but continue)
  let _ = install_hooks(bead_id, wt_path)

  // Launch Claude in the main window
  let claude_cmd = build_claude_command(opts, config)
  use _ <- result.try(
    tmux.send_keys(tmux_name <> ":" <> window_claude, claude_cmd <> " Enter")
    |> result.map_error(TmuxError),
  )

  // Set session status option (non-fatal if this fails)
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

/// Stop a session
pub fn stop(bead_id: String) -> Result(Nil, SessionError) {
  let tmux_name = session_name(bead_id)
  use _ <- require_session_exists(bead_id, tmux_name)

  tmux.kill_session(tmux_name)
  |> result.map_error(TmuxError)
}

/// Pause a session (send Ctrl+C)
pub fn pause(bead_id: String) -> Result(Nil, SessionError) {
  let tmux_name = session_name(bead_id)
  use _ <- require_session_exists(bead_id, tmux_name)

  let target = tmux_name <> ":" <> window_claude
  use _ <- result.try(
    tmux.send_keys(target, "C-c")
    |> result.map_error(TmuxError),
  )

  // Update status (non-fatal if this fails)
  let _ = tmux.set_option(tmux_name, "@az_status", "paused")
  Ok(Nil)
}

/// Resume a paused session
pub fn resume(bead_id: String, prompt: Option(String)) -> Result(Nil, SessionError) {
  let tmux_name = session_name(bead_id)
  use _ <- require_session_exists(bead_id, tmux_name)

  let target = tmux_name <> ":" <> window_claude
  let resume_prompt = option.unwrap(prompt, "/resume")

  use _ <- result.try(
    tmux.send_keys(target, resume_prompt <> " Enter")
    |> result.map_error(TmuxError),
  )

  // Update status (non-fatal if this fails)
  let _ = tmux.set_option(tmux_name, "@az_status", "busy")
  Ok(Nil)
}

/// Attach to a session
pub fn attach(bead_id: String) -> Result(Nil, SessionError) {
  let tmux_name = session_name(bead_id)
  use _ <- require_session_exists(bead_id, tmux_name)

  tmux.attach(tmux_name)
  |> result.map_error(TmuxError)
}

/// Helper: require session exists before proceeding
fn require_session_exists(
  bead_id: String,
  tmux_name: String,
  next: fn(Nil) -> Result(Nil, SessionError),
) -> Result(Nil, SessionError) {
  case tmux.session_exists(tmux_name) {
    False -> Error(SessionNotFound(bead_id))
    True -> next(Nil)
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
      // Try to get status from tmux option, fall back to pane detection
      let state =
        tmux.get_option(tmux_name, "@az_status")
        |> result.map(session.state_from_string)
        |> result.lazy_unwrap(fn() {
          tmux.capture_pane(tmux_name <> ":" <> window_claude, 50)
          |> result.map(state_detector.detect)
          |> result.unwrap(session.Unknown)
        })

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
  use sessions <- result.try(
    tmux.discover_az_sessions()
    |> result.map_error(TmuxError),
  )

  sessions
  |> list.filter_map(fn(name) {
    parse_session_name(name)
    |> option.to_result(Nil)
    |> result.try(fn(bead_id) {
      get_state(bead_id) |> result.replace_error(Nil)
    })
  })
  |> Ok
}

/// Check if a session exists
pub fn exists(bead_id: String) -> Bool {
  tmux.session_exists(session_name(bead_id))
}

// ============================================================================
// Claude Command Building
// ============================================================================

fn build_claude_command(opts: StartOptions, _config: Config) -> String {
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
///
/// Creates .claude/settings.local.json with hook configuration that calls
/// az-notify.sh for each Claude Code event. This enables authoritative
/// state detection from Claude's native hook system.
fn install_hooks(bead_id: String, worktree_path: String) -> Result(Nil, Nil) {
  let claude_dir = worktree_path <> "/.claude"
  let settings_path = claude_dir <> "/settings.local.json"

  // Create .claude directory
  case shell.mkdir_p(claude_dir) {
    Ok(_) -> Nil
    Error(_) -> Nil  // Directory might already exist
  }

  // Generate hooks configuration using the hooks module
  case hooks.generate_hook_config_auto(bead_id) {
    Ok(hooks_json) -> {
      // Write settings file
      case shell.write_file(settings_path, hooks_json) {
        Ok(_) -> Ok(Nil)
        Error(_) -> Error(Nil)
      }
    }
    Error(_) -> {
      // Fallback: try to find notify script in common locations
      let fallback_paths = [
        worktree_path <> "/bin/az-notify.sh",
        shell.home_dir() <> "/.local/bin/az-notify.sh",
        "/usr/local/bin/az-notify.sh",
      ]

      case find_existing_path(fallback_paths) {
        Some(path) -> {
          let hooks_json = hooks.generate_hook_config(bead_id, path)
          case shell.write_file(settings_path, hooks_json) {
            Ok(_) -> Ok(Nil)
            Error(_) -> Error(Nil)
          }
        }
        None -> {
          // No notify script found - hooks won't work but session can still run
          Error(Nil)
        }
      }
    }
  }
}

/// Find the first path that exists from a list
fn find_existing_path(paths: List(String)) -> Option(String) {
  case paths {
    [] -> None
    [path, ..rest] -> {
      case shell.path_exists(path) {
        True -> Some(path)
        False -> find_existing_path(rest)
      }
    }
  }
}

// ============================================================================
// Utilities
// ============================================================================

fn now_iso() -> String {
  tempo.format_utc(tempo.ISO8601Seconds)
}
