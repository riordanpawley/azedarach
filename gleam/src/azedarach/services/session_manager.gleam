// Session Manager - orchestrates Claude Code sessions
//
// Core service that manages the lifecycle of Claude Code sessions:
// - start(): Create worktree, tmux session, and launch Claude
// - stop(): Kill tmux session and cleanup
// - pause(): Send Ctrl+C to pause Claude
// - resume(): Continue paused session
// - get_state(): Get current session state
// - list_active(): List all running sessions

import gleam/io
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
// Session Building Utilities
// ============================================================================

/// Options for building a tmux session from a bead
pub type BuildSessionOptions {
  BuildSessionOptions(
    bead_id: String,
    project_path: String,
    window_name: String,
    command: String,
    init_commands: List(String),
  )
}

/// Result of building a tmux session
pub type BuildSessionResult {
  BuildSessionResult(
    session_name: String,
    target: String,
    worktree_path: String,
  )
}

/// Build a tmux session from a bead ID
///
/// This is the primary entry point for creating tmux sessions for beads.
/// It consolidates:
/// 1. Session name generation (uses session_name)
/// 2. Worktree path computation (uses worktree.worktree_path)
/// 3. Session creation
/// 4. Window creation with command
///
/// Example:
/// ```gleam
/// let result = build_session_from_bead(
///   BuildSessionOptions(
///     bead_id: "az-05y",
///     project_path: "/home/user/project",
///     window_name: "claude",
///     command: "claude --model opus",
///     init_commands: [],
///   ),
///   config,
/// )
/// // result.target = "az-05y:claude"
/// ```
pub fn build_session_from_bead(
  opts: BuildSessionOptions,
  config: Config,
) -> Result(BuildSessionResult, SessionError) {
  let tmux_name = session_name(opts.bead_id)
  let target = tmux_name <> ":" <> opts.window_name

  // Ensure worktree exists (using project_path)
  use path <- result.try(
    worktree.ensure(opts.bead_id, config, opts.project_path)
    |> result.map_error(WorktreeError),
  )

  // Create or get session
  use _ <- result.try(
    ensure_session(tmux_name, path, opts.init_commands)
    |> result.map_error(TmuxError),
  )

  // Ensure window exists and run command
  use _ <- result.try(
    ensure_window(tmux_name, opts.window_name, opts.command)
    |> result.map_error(TmuxError),
  )

  Ok(BuildSessionResult(
    session_name: tmux_name,
    target: target,
    worktree_path: path,
  ))
}

/// Ensure a tmux session exists, creating if necessary
fn ensure_session(
  name: String,
  cwd: String,
  init_commands: List(String),
) -> Result(Nil, tmux.TmuxError) {
  case tmux.session_exists(name) {
    True -> Ok(Nil)
    False -> {
      use _ <- result.try(tmux.new_session(name, cwd))
      // Run init commands, logging any failures
      list.each(init_commands, fn(cmd) {
        case tmux.send_keys(name <> ":main", cmd <> " Enter") {
          Ok(_) -> Nil
          Error(e) ->
            io.println_error(
              "Warning: init command failed for " <> name <> ": " <> tmux.error_to_string(e),
            )
        }
      })
      Ok(Nil)
    }
  }
}

/// Ensure a window exists in a session, creating if necessary
///
/// If the window doesn't exist, creates it and sends the command.
/// If it does exist, just sends the command to the existing window.
pub fn ensure_window(
  session: String,
  window: String,
  command: String,
) -> Result(Nil, tmux.TmuxError) {
  let target = session <> ":" <> window

  case window_exists(session, window) {
    True -> {
      // Window exists, select it and send command
      case tmux.select_window(session, window) {
        Ok(_) -> Nil
        Error(e) ->
          io.println_error(
            "Warning: select_window failed for " <> target <> ": " <> tmux.error_to_string(e),
          )
      }
      tmux.send_keys(target, command <> " Enter")
    }
    False -> {
      // Create new window and send command
      use _ <- result.try(tmux.new_window(session, window))
      tmux.send_keys(target, command <> " Enter")
    }
  }
}

/// Check if a window exists in a session
fn window_exists(session: String, window: String) -> Bool {
  case tmux.list_windows(session) {
    Ok(windows) -> list.contains(windows, window)
    Error(_) -> False
  }
}

// ============================================================================
// Session Lifecycle
// ============================================================================

/// Start a new Claude session for a bead
///
/// Uses build_session_from_bead internally for consistent session creation.
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
      // Build the Claude command
      let claude_cmd = build_claude_command(opts, config)

      // Use the consolidated build function
      use result <- result.try(
        build_session_from_bead(
          BuildSessionOptions(
            bead_id: bead_id,
            project_path: opts.project_path,
            window_name: window_claude,
            command: claude_cmd,
            init_commands: [],
          ),
          config,
        ),
      )

      // Create additional shell window
      case tmux.new_window(tmux_name, window_shell) {
        Ok(_) -> Nil
        Error(e) ->
          io.println_error(
            "Warning: failed to create shell window for " <> tmux_name <> ": " <> tmux.error_to_string(e),
          )
      }

      // Install Claude Code hooks
      case install_hooks(bead_id, result.worktree_path) {
        Ok(_) -> Nil
        Error(msg) ->
          io.println_error(
            "Warning: failed to install hooks for " <> bead_id <> ": " <> msg,
          )
      }

      // Set session status option
      case tmux.set_option(tmux_name, "@az_status", "busy") {
        Ok(_) -> Nil
        Error(e) ->
          io.println_error(
            "Warning: failed to set status for " <> tmux_name <> ": " <> tmux.error_to_string(e),
          )
      }

      Ok(SessionState(
        bead_id: bead_id,
        state: session.Busy,
        started_at: Some(now_iso()),
        last_output: None,
        worktree_path: Some(result.worktree_path),
        tmux_session: Some(result.session_name),
      ))
    }
  }
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

  // Update status (non-fatal, log warning if fails)
  case tmux.set_option(tmux_name, "@az_status", "paused") {
    Ok(_) -> Nil
    Error(e) ->
      io.println_error(
        "Warning: failed to set paused status for " <> tmux_name <> ": " <> tmux.error_to_string(e),
      )
  }
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

  // Update status (non-fatal, log warning if fails)
  case tmux.set_option(tmux_name, "@az_status", "busy") {
    Ok(_) -> Nil
    Error(e) ->
      io.println_error(
        "Warning: failed to set busy status for " <> tmux_name <> ": " <> tmux.error_to_string(e),
      )
  }
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
fn install_hooks(bead_id: String, worktree_path: String) -> Result(Nil, String) {
  let claude_dir = worktree_path <> "/.claude"
  let settings_path = claude_dir <> "/settings.local.json"

  // Create .claude directory
  case shell.mkdir_p(claude_dir) {
    Ok(_) -> Nil
    Error(e) -> {
      // Log but continue - directory might already exist
      io.println_error(
        "Warning: mkdir_p failed for " <> claude_dir <> ": " <> shell.error_to_string(e),
      )
    }
  }

  // Generate hooks configuration using the hooks module
  case hooks.generate_hook_config_auto(bead_id) {
    Ok(hooks_json) -> {
      // Write settings file
      case shell.write_file(settings_path, hooks_json) {
        Ok(_) -> Ok(Nil)
        Error(e) ->
          Error("failed to write settings file: " <> shell.error_to_string(e))
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
            Error(e) ->
              Error("failed to write settings file: " <> shell.error_to_string(e))
          }
        }
        None -> {
          // No notify script found - hooks won't work but session can still run
          Error("no az-notify.sh script found in common locations")
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
