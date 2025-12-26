// Tmux service - tmux command wrapper

import gleam/int
import gleam/list
import gleam/string
import azedarach/util/shell

pub type TmuxError {
  CommandFailed(exit_code: Int, stderr: String)
  SessionNotFound(name: String)
}

pub fn error_to_string(err: TmuxError) -> String {
  case err {
    CommandFailed(code, stderr) ->
      "tmux command failed (" <> int.to_string(code) <> "): " <> stderr
    SessionNotFound(name) -> "Session not found: " <> name
  }
}

// Helper to convert shell errors to tmux errors
fn shell_to_tmux_error(err: shell.ShellError) -> TmuxError {
  case err {
    shell.CommandError(code, stderr) -> CommandFailed(code, stderr)
    shell.NotFound(cmd) -> CommandFailed(127, "Command not found: " <> cmd)
  }
}

// Check if session exists
pub fn session_exists(name: String) -> Bool {
  case shell.run("tmux", ["has-session", "-t", name], ".") {
    Ok(_) -> True
    Error(_) -> False
  }
}

// Create new session
pub fn new_session(name: String, cwd: String) -> Result(Nil, TmuxError) {
  let args = [
    "new-session",
    "-d",
    "-s",
    name,
    "-n",
    "main",
    "-c",
    cwd,
  ]
  case shell.run("tmux", args, ".") {
    Ok(_) -> Ok(Nil)
    Error(e) -> Error(shell_to_tmux_error(e))
  }
}

// Kill session
pub fn kill_session(name: String) -> Result(Nil, TmuxError) {
  case shell.run("tmux", ["kill-session", "-t", name], ".") {
    Ok(_) -> Ok(Nil)
    Error(e) -> Error(shell_to_tmux_error(e))
  }
}

// Attach to session (this will switch the terminal)
pub fn attach(name: String) -> Result(Nil, TmuxError) {
  // Use switch-client if already in tmux, attach-session otherwise
  case is_inside_tmux() {
    True -> {
      case shell.run("tmux", ["switch-client", "-t", name], ".") {
        Ok(_) -> Ok(Nil)
        Error(e) -> Error(shell_to_tmux_error(e))
      }
    }
    False -> {
      case shell.run("tmux", ["attach-session", "-t", name], ".") {
        Ok(_) -> Ok(Nil)
        Error(e) -> Error(shell_to_tmux_error(e))
      }
    }
  }
}

// Create new window
pub fn new_window(session: String, name: String) -> Result(Nil, TmuxError) {
  let args = ["new-window", "-t", session, "-n", name]
  case shell.run("tmux", args, ".") {
    Ok(_) -> Ok(Nil)
    Error(e) -> Error(shell_to_tmux_error(e))
  }
}

// Kill window
pub fn kill_window(session: String, window: String) -> Result(Nil, TmuxError) {
  let target = session <> ":" <> window
  case shell.run("tmux", ["kill-window", "-t", target], ".") {
    Ok(_) -> Ok(Nil)
    Error(e) -> Error(shell_to_tmux_error(e))
  }
}

// Select window
pub fn select_window(session: String, window: String) -> Result(Nil, TmuxError) {
  let target = session <> ":" <> window
  case shell.run("tmux", ["select-window", "-t", target], ".") {
    Ok(_) -> Ok(Nil)
    Error(e) -> Error(shell_to_tmux_error(e))
  }
}

// Send keys to pane
pub fn send_keys(target: String, keys: String) -> Result(Nil, TmuxError) {
  case shell.run("tmux", ["send-keys", "-t", target, keys], ".") {
    Ok(_) -> Ok(Nil)
    Error(e) -> Error(shell_to_tmux_error(e))
  }
}

// Capture pane content
pub fn capture_pane(target: String, lines: Int) -> Result(String, TmuxError) {
  let args = [
    "capture-pane",
    "-t",
    target,
    "-p",
    "-S",
    "-" <> int.to_string(lines),
  ]
  case shell.run("tmux", args, ".") {
    Ok(output) -> Ok(output)
    Error(e) -> Error(shell_to_tmux_error(e))
  }
}

// Set session/window option
pub fn set_option(target: String, name: String, value: String) -> Result(Nil, TmuxError) {
  case shell.run("tmux", ["set-option", "-t", target, name, value], ".") {
    Ok(_) -> Ok(Nil)
    Error(e) -> Error(shell_to_tmux_error(e))
  }
}

// Get session/window option
pub fn get_option(target: String, name: String) -> Result(String, TmuxError) {
  case shell.run("tmux", ["show-option", "-v", "-t", target, name], ".") {
    Ok(output) -> Ok(string.trim(output))
    Error(e) -> Error(shell_to_tmux_error(e))
  }
}

// List windows in session
pub fn list_windows(session: String) -> Result(List(String), TmuxError) {
  let args = ["list-windows", "-t", session, "-F", "#{window_name}"]
  case shell.run("tmux", args, ".") {
    Ok(output) -> {
      let windows =
        output
        |> string.split("\n")
        |> list.filter(fn(s) { s != "" })
      Ok(windows)
    }
    Error(e) -> Error(shell_to_tmux_error(e))
  }
}

// List all sessions
pub fn list_sessions() -> Result(List(String), TmuxError) {
  case shell.run("tmux", ["list-sessions", "-F", "#{session_name}"], ".") {
    Ok(output) -> {
      let sessions =
        output
        |> string.split("\n")
        |> list.filter(fn(s) { s != "" })
      Ok(sessions)
    }
    Error(e) -> Error(shell_to_tmux_error(e))
  }
}

// Discover azedarach sessions (ending in -az)
pub fn discover_az_sessions() -> Result(List(String), TmuxError) {
  case list_sessions() {
    Ok(sessions) -> {
      let az_sessions =
        sessions
        |> list.filter(fn(s) { string.ends_with(s, "-az") })
      Ok(az_sessions)
    }
    Error(e) -> Error(e)
  }
}

// Check if we're inside tmux
fn is_inside_tmux() -> Bool {
  case shell.get_env("TMUX") {
    Ok(_) -> True
    Error(_) -> False
  }
}
