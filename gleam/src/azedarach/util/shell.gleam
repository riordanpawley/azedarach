// Shell utility - command execution

import gleam/erlang/os
import gleam/list
import gleam/option.{type Option, None, Some}
import gleam/result
import gleam/string
import shellout

pub type ShellError {
  CommandError(exit_code: Int, stderr: String)
  NotFound(command: String)
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

// Get environment variable
pub fn get_env(name: String) -> Result(String, Nil) {
  os.get_env(name)
}

// Set environment variable for subprocess
pub fn with_env(
  cmd: String,
  args: List(String),
  env: List(#(String, String)),
  cwd: String,
) -> Result(String, ShellError) {
  // Build env prefix
  let env_prefix =
    env
    |> list.map(fn(pair) { pair.0 <> "=" <> pair.1 })
    |> string.join(" ")

  let full_cmd = env_prefix <> " " <> cmd <> " " <> string.join(args, " ")
  case shellout.command(run: "sh", with: ["-c", full_cmd], in: cwd, opt: []) {
    Ok(output) -> Ok(output)
    Error(#(code, stderr)) -> Error(CommandError(code, stderr))
  }
}
