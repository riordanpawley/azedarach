// Beads service - bd CLI wrapper

import gleam/dynamic.{type Dynamic}
import gleam/json
import gleam/list
import gleam/option.{type Option, None, Some}
import gleam/result
import gleam/string
import azedarach/config.{type Config}
import azedarach/domain/task.{type Task}
import azedarach/util/shell

pub type BeadsError {
  CommandFailed(exit_code: Int, stderr: String)
  ParseError(message: String)
  NotFound(id: String)
}

pub fn error_to_string(err: BeadsError) -> String {
  case err {
    CommandFailed(code, stderr) ->
      "bd command failed (" <> int.to_string(code) <> "): " <> stderr
    ParseError(msg) -> "Failed to parse bd output: " <> msg
    NotFound(id) -> "Bead not found: " <> id
  }
}

// List all beads
pub fn list_all(config: Config) -> Result(List(Task), BeadsError) {
  case shell.run("bd", ["list", "--json"], ".") {
    Ok(output) -> parse_beads_list(output)
    Error(shell.CommandError(code, stderr)) -> Error(CommandFailed(code, stderr))
  }
}

// Show a specific bead
pub fn show(id: String, config: Config) -> Result(Task, BeadsError) {
  case shell.run("bd", ["show", id, "--json"], ".") {
    Ok(output) -> parse_single_bead(output, id)
    Error(shell.CommandError(code, stderr)) -> Error(CommandFailed(code, stderr))
  }
}

// Create a new bead
pub fn create(title: Option(String), config: Config) -> Result(String, BeadsError) {
  let args = case title {
    Some(t) -> ["create", "--title=" <> t, "--type=task"]
    None -> ["create", "--type=task"]
    // Will open $EDITOR
  }
  case shell.run("bd", args, ".") {
    Ok(output) -> Ok(string.trim(output))
    Error(shell.CommandError(code, stderr)) -> Error(CommandFailed(code, stderr))
  }
}

// Create bead with Claude (natural language)
pub fn create_with_claude(config: Config) -> Result(String, BeadsError) {
  // This would spawn Claude to help create the bead
  // For now, just create with editor
  create(None, config)
}

// Edit a bead
pub fn edit(id: String, config: Config) -> Result(Nil, BeadsError) {
  case shell.run("bd", ["edit", id], ".") {
    Ok(_) -> Ok(Nil)
    Error(shell.CommandError(code, stderr)) -> Error(CommandFailed(code, stderr))
  }
}

// Delete a bead
pub fn delete(id: String, config: Config) -> Result(Nil, BeadsError) {
  case shell.run("bd", ["delete", id], ".") {
    Ok(_) -> Ok(Nil)
    Error(shell.CommandError(code, stderr)) -> Error(CommandFailed(code, stderr))
  }
}

// Update bead status
pub fn update_status(
  id: String,
  status: task.Status,
  config: Config,
) -> Result(Nil, BeadsError) {
  let status_str = task.status_to_string(status)
  case shell.run("bd", ["update", id, "--status=" <> status_str], ".") {
    Ok(_) -> Ok(Nil)
    Error(shell.CommandError(code, stderr)) -> Error(CommandFailed(code, stderr))
  }
}

// Paste image from clipboard
pub fn paste_image(id: String, config: Config) -> Result(String, BeadsError) {
  // Platform-specific clipboard handling
  let paste_cmd = detect_paste_command()
  case paste_cmd {
    Some(cmd) -> {
      // Generate filename
      let filename = id <> "-" <> random_id() <> ".png"
      let path = ".beads/images/" <> id <> "/" <> filename

      // Create directory
      shell.run("mkdir", ["-p", ".beads/images/" <> id], ".")

      // Paste image
      case shell.run_with_output(cmd.0, cmd.1, path) {
        Ok(_) -> Ok(path)
        Error(shell.CommandError(code, stderr)) -> Error(CommandFailed(code, stderr))
      }
    }
    None -> Error(CommandFailed(1, "No clipboard command found"))
  }
}

// Delete image
pub fn delete_image(
  id: String,
  path: String,
  config: Config,
) -> Result(Nil, BeadsError) {
  case shell.run("rm", [path], ".") {
    Ok(_) -> Ok(Nil)
    Error(shell.CommandError(code, stderr)) -> Error(CommandFailed(code, stderr))
  }
}

// Sync beads (for worktrees)
pub fn sync(config: Config) -> Result(Nil, BeadsError) {
  case shell.run("bd", ["sync"], ".") {
    Ok(_) -> Ok(Nil)
    Error(shell.CommandError(code, stderr)) -> Error(CommandFailed(code, stderr))
  }
}

// Parse JSON list output
fn parse_beads_list(output: String) -> Result(List(Task), BeadsError) {
  case json.decode(output, dynamic.list(bead_decoder())) {
    Ok(tasks) -> Ok(tasks)
    Error(e) -> Error(ParseError(string.inspect(e)))
  }
}

fn parse_single_bead(output: String, id: String) -> Result(Task, BeadsError) {
  case json.decode(output, bead_decoder()) {
    Ok(task) -> Ok(task)
    Error(_) -> Error(NotFound(id))
  }
}

fn bead_decoder() -> fn(Dynamic) -> Result(Task, List(dynamic.DecodeError)) {
  dynamic.decode12(
    Task,
    dynamic.field("id", dynamic.string),
    dynamic.field("title", dynamic.string),
    dynamic.optional_field("description", dynamic.string)
      |> map_option(""),
    dynamic.field("status", status_decoder()),
    dynamic.optional_field("priority", priority_decoder())
      |> map_option(task.P2),
    dynamic.optional_field("type", type_decoder()) |> map_option(task.Task),
    dynamic.optional_field("parent_id", dynamic.string),
    dynamic.field("created_at", dynamic.string),
    dynamic.field("updated_at", dynamic.string),
    dynamic.optional_field("design_notes", dynamic.string),
    dynamic.optional_field("actor", dynamic.string),
    dynamic.optional_field("attachments", dynamic.list(dynamic.string))
      |> map_option([]),
  )
}

fn status_decoder() -> fn(Dynamic) -> Result(task.Status, List(dynamic.DecodeError)) {
  fn(dyn) {
    case dynamic.string(dyn) {
      Ok(s) -> Ok(task.status_from_string(s))
      Error(e) -> Error(e)
    }
  }
}

fn priority_decoder() -> fn(Dynamic) ->
  Result(task.Priority, List(dynamic.DecodeError)) {
  fn(dyn) {
    case dynamic.int(dyn) {
      Ok(n) -> Ok(task.priority_from_int(n))
      Error(e) -> Error(e)
    }
  }
}

fn type_decoder() -> fn(Dynamic) ->
  Result(task.IssueType, List(dynamic.DecodeError)) {
  fn(dyn) {
    case dynamic.string(dyn) {
      Ok(s) -> Ok(task.issue_type_from_string(s))
      Error(e) -> Error(e)
    }
  }
}

fn map_option(
  decoder: fn(Dynamic) -> Result(Option(a), List(dynamic.DecodeError)),
  default: a,
) -> fn(Dynamic) -> Result(a, List(dynamic.DecodeError)) {
  fn(dyn) {
    case decoder(dyn) {
      Ok(Some(value)) -> Ok(value)
      Ok(None) -> Ok(default)
      Error(_) -> Ok(default)
    }
  }
}

fn detect_paste_command() -> Option(#(String, List(String))) {
  // Try different clipboard commands
  case shell.command_exists("pbpaste") {
    True -> Some(#("pbpaste", []))
    False -> {
      case shell.command_exists("wl-paste") {
        True -> Some(#("wl-paste", ["--type", "image/png"]))
        False -> {
          case shell.command_exists("xclip") {
            True -> Some(#("xclip", ["-selection", "clipboard", "-t", "image/png", "-o"]))
            False -> None
          }
        }
      }
    }
  }
}

fn random_id() -> String {
  int.to_string(erlang.unique_integer([positive]))
}

import gleam/erlang
import gleam/int
