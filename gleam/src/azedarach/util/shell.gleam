// Shell utility - command execution

import gleam/int
import gleam/list
import gleam/result
import gleam/string
import shellout

pub type ShellError {
  CommandError(exit_code: Int, stderr: String)
  NotFound(command: String)
}

pub fn error_to_string(err: ShellError) -> String {
  case err {
    CommandError(code, stderr) ->
      "command failed (" <> int.to_string(code) <> "): " <> stderr
    NotFound(cmd) -> "command not found: " <> cmd
  }
}

// Run a command and get output
pub fn run(
  cmd: String,
  args: List(String),
  cwd: String,
) -> Result(String, ShellError) {
  case shellout.command(run: cmd, with: args, in: cwd, opt: []) {
    Ok(output) -> Ok(output)
    Error(#(code, stderr)) -> Error(CommandError(code, stderr))
  }
}

// Run a command and get exit code + output
pub fn run_exit_code(
  cmd: String,
  args: List(String),
  cwd: String,
) -> #(Int, String) {
  case shellout.command(run: cmd, with: args, in: cwd, opt: []) {
    Ok(output) -> #(0, output)
    Error(#(code, output)) -> #(code, output)
  }
}

// Run a command with output redirected to a file
pub fn run_with_output(
  cmd: String,
  args: List(String),
  output_file: String,
) -> Result(Nil, ShellError) {
  // Use shell redirection
  let full_cmd = cmd <> " " <> string.join(args, " ") <> " > " <> output_file
  case shellout.command(run: "sh", with: ["-c", full_cmd], in: ".", opt: []) {
    Ok(_) -> Ok(Nil)
    Error(#(code, stderr)) -> Error(CommandError(code, stderr))
  }
}

// Check if a command exists
pub fn command_exists(cmd: String) -> Bool {
  case shellout.command(run: "which", with: [cmd], in: ".", opt: []) {
    Ok(_) -> True
    Error(_) -> False
  }
}

// Get environment variable using Erlang FFI
@external(erlang, "os", "getenv")
fn erlang_getenv(name: String) -> Result(String, Nil)

// Get environment variable
pub fn get_env(name: String) -> Result(String, Nil) {
  erlang_getenv(name)
}

// Set environment variable for subprocess
pub fn with_env(
  cmd: String,
  args: List(String),
  env: List(#(String, String)),
  cwd_path: String,
) -> Result(String, ShellError) {
  // Build env prefix
  let env_prefix =
    env
    |> list.map(fn(pair) { pair.0 <> "=" <> pair.1 })
    |> string.join(" ")

  let full_cmd = env_prefix <> " " <> cmd <> " " <> string.join(args, " ")
  case shellout.command(run: "sh", with: ["-c", full_cmd], in: cwd_path, opt: []) {
    Ok(output) -> Ok(output)
    Error(#(code, stderr)) -> Error(CommandError(code, stderr))
  }
}

// ============================================================================
// Filesystem Operations
// ============================================================================

/// Check if a path exists
pub fn path_exists(path: String) -> Bool {
  case shellout.command(run: "test", with: ["-e", path], in: ".", opt: []) {
    Ok(_) -> True
    Error(_) -> False
  }
}

/// Check if path is a directory
pub fn is_directory(path: String) -> Bool {
  case shellout.command(run: "test", with: ["-d", path], in: ".", opt: []) {
    Ok(_) -> True
    Error(_) -> False
  }
}

/// Check if path is a file
pub fn is_file(path: String) -> Bool {
  case shellout.command(run: "test", with: ["-f", path], in: ".", opt: []) {
    Ok(_) -> True
    Error(_) -> False
  }
}

/// Read a file's contents
pub fn read_file(path: String) -> Result(String, ShellError) {
  case shellout.command(run: "cat", with: [path], in: ".", opt: []) {
    Ok(content) -> Ok(content)
    Error(#(code, stderr)) -> Error(CommandError(code, stderr))
  }
}

/// Write content to a file
pub fn write_file(path: String, content: String) -> Result(Nil, ShellError) {
  // Use printf to handle special characters better than echo
  let escaped = escape_for_shell(content)
  case shellout.command(run: "sh", with: ["-c", "printf '%s' '" <> escaped <> "' > " <> path], in: ".", opt: []) {
    Ok(_) -> Ok(Nil)
    Error(#(code, stderr)) -> Error(CommandError(code, stderr))
  }
}

/// List directory contents
pub fn list_dir(path: String) -> Result(List(String), ShellError) {
  case shellout.command(run: "ls", with: ["-1", path], in: ".", opt: []) {
    Ok(output) -> {
      let entries =
        output
        |> string.trim
        |> string.split("\n")
        |> list.filter(fn(s) { s != "" })
      Ok(entries)
    }
    Error(#(code, stderr)) -> Error(CommandError(code, stderr))
  }
}

/// Create a directory (with parents)
pub fn mkdir_p(path: String) -> Result(Nil, ShellError) {
  case shellout.command(run: "mkdir", with: ["-p", path], in: ".", opt: []) {
    Ok(_) -> Ok(Nil)
    Error(#(code, stderr)) -> Error(CommandError(code, stderr))
  }
}

/// Remove a file or directory
pub fn rm(path: String, recursive: Bool) -> Result(Nil, ShellError) {
  let args = case recursive {
    True -> ["-rf", path]
    False -> ["-f", path]
  }
  case shellout.command(run: "rm", with: args, in: ".", opt: []) {
    Ok(_) -> Ok(Nil)
    Error(#(code, stderr)) -> Error(CommandError(code, stderr))
  }
}

// ============================================================================
// Path Operations
// ============================================================================

/// Get home directory
pub fn home_dir() -> String {
  get_env("HOME")
  |> result.unwrap("/home/user")
}

/// Get current working directory
pub fn cwd() -> Result(String, ShellError) {
  case shellout.command(run: "pwd", with: [], in: ".", opt: []) {
    Ok(output) -> Ok(string.trim(output))
    Error(#(code, stderr)) -> Error(CommandError(code, stderr))
  }
}

/// Get absolute path
pub fn realpath(path: String) -> Result(String, ShellError) {
  case shellout.command(run: "realpath", with: [path], in: ".", opt: []) {
    Ok(output) -> Ok(string.trim(output))
    Error(#(code, stderr)) -> Error(CommandError(code, stderr))
  }
}

/// Join path components
pub fn join_path(parts: List(String)) -> String {
  parts
  |> list.filter(fn(p) { p != "" })
  |> string.join("/")
}

/// Get directory name from path
pub fn dirname(path: String) -> String {
  case string.split(path, "/") {
    [] -> "."
    parts -> {
      case list.reverse(parts) {
        [_, ..rest] -> {
          case list.reverse(rest) {
            [] -> "/"
            dirs -> string.join(dirs, "/")
          }
        }
        _ -> "."
      }
    }
  }
}

/// Get file name from path
pub fn basename(path: String) -> String {
  path
  |> string.split("/")
  |> list.last
  |> result.unwrap("")
}

// ============================================================================
// Timing
// ============================================================================

/// Sleep for a number of milliseconds
@external(erlang, "timer", "sleep")
pub fn sleep_ms(ms: Int) -> Nil

// ============================================================================
// Helpers
// ============================================================================

/// Escape string for shell single quotes
fn escape_for_shell(s: String) -> String {
  string.replace(s, "'", "'\"'\"'")
}
