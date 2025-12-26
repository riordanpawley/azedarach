// Beads service - bd CLI wrapper
// Full implementation matching TypeScript BeadsClient

import gleam/dynamic/decode.{type Decoder}
import gleam/int
import gleam/json
import gleam/list
import gleam/option.{type Option, None, Some}
import gleam/result
import gleam/string
import azedarach/config.{type Config}
import azedarach/domain/task.{
  type Dependent, type DependentType, type IssueType, type Priority, type Status,
  type Task, Dependent, Task,
}
import azedarach/util/shell

// ============================================================================
// Error Types
// ============================================================================

pub type BeadsError {
  CommandFailed(exit_code: Int, stderr: String)
  ParseError(message: String)
  NotFound(id: String)
  ValidationError(message: String)
}

pub fn error_to_string(err: BeadsError) -> String {
  case err {
    CommandFailed(code, stderr) ->
      "bd command failed (" <> int.to_string(code) <> "): " <> stderr
    ParseError(msg) -> "Failed to parse bd output: " <> msg
    NotFound(id) -> "Bead not found: " <> id
    ValidationError(msg) -> "Validation error: " <> msg
  }
}

// ============================================================================
// Create Options
// ============================================================================

pub type CreateOptions {
  CreateOptions(
    title: Option(String),
    issue_type: Option(IssueType),
    priority: Option(Priority),
    description: Option(String),
    design: Option(String),
    notes: Option(String),
    acceptance: Option(String),
    assignee: Option(String),
    labels: List(String),
    estimate: Option(String),
  )
}

pub fn default_create_options() -> CreateOptions {
  CreateOptions(
    title: None,
    issue_type: None,
    priority: None,
    description: None,
    design: None,
    notes: None,
    acceptance: None,
    assignee: None,
    labels: [],
    estimate: None,
  )
}

// ============================================================================
// Update Options
// ============================================================================

pub type UpdateOptions {
  UpdateOptions(
    title: Option(String),
    status: Option(Status),
    priority: Option(Priority),
    description: Option(String),
    design: Option(String),
    notes: Option(String),
    acceptance: Option(String),
    assignee: Option(String),
    labels: Option(List(String)),
    estimate: Option(String),
  )
}

pub fn default_update_options() -> UpdateOptions {
  UpdateOptions(
    title: None,
    status: None,
    priority: None,
    description: None,
    design: None,
    notes: None,
    acceptance: None,
    assignee: None,
    labels: None,
    estimate: None,
  )
}

// ============================================================================
// Core Operations
// ============================================================================

/// List all beads (filters out tombstones)
pub fn list_all(config: Config) -> Result(List(Task), BeadsError) {
  list_all_in_dir(".", config)
}

/// List all beads in a specific directory
pub fn list_all_in_dir(dir: String, config: Config) -> Result(List(Task), BeadsError) {
  case shell.run("bd", ["list", "--json"], dir) {
    Ok(output) -> {
      case parse_beads_list(output) {
        Ok(tasks) -> Ok(filter_tombstones(tasks))
        Error(e) -> Error(e)
      }
    }
    Error(shell.CommandError(code, stderr)) -> Error(CommandFailed(code, stderr))
    Error(shell.NotFound(cmd)) -> Error(CommandFailed(127, "Command not found: " <> cmd))
  }
}

/// List all beads including tombstones
pub fn list_all_with_tombstones(config: Config) -> Result(List(Task), BeadsError) {
  case shell.run("bd", ["list", "--json"], ".") {
    Ok(output) -> parse_beads_list(output)
    Error(shell.CommandError(code, stderr)) -> Error(CommandFailed(code, stderr))
    Error(shell.NotFound(cmd)) -> Error(CommandFailed(127, "Command not found: " <> cmd))
  }
}

/// Show a specific bead
pub fn show(id: String, config: Config) -> Result(Task, BeadsError) {
  case shell.run("bd", ["show", id, "--json"], ".") {
    Ok(output) -> parse_single_bead(output, id)
    Error(shell.CommandError(code, stderr)) -> Error(CommandFailed(code, stderr))
    Error(shell.NotFound(cmd)) -> Error(CommandFailed(127, "Command not found: " <> cmd))
  }
}

/// Create a new bead with options
pub fn create(options: CreateOptions, config: Config) -> Result(String, BeadsError) {
  let args = build_create_args(options)
  case shell.run("bd", args, ".") {
    Ok(output) -> Ok(string.trim(output))
    Error(shell.CommandError(code, stderr)) -> Error(CommandFailed(code, stderr))
    Error(shell.NotFound(cmd)) -> Error(CommandFailed(127, "Command not found: " <> cmd))
  }
}

/// Create a bead with just a title (convenience function)
pub fn create_simple(
  title: String,
  issue_type: IssueType,
  config: Config,
) -> Result(String, BeadsError) {
  let options =
    CreateOptions(
      ..default_create_options(),
      title: Some(title),
      issue_type: Some(issue_type),
    )
  create(options, config)
}

/// Update a bead with options
pub fn update(
  id: String,
  options: UpdateOptions,
  config: Config,
) -> Result(Nil, BeadsError) {
  let args = build_update_args(id, options)
  case args {
    [_] -> Ok(Nil)
    // No updates to make
    _ -> {
      case shell.run("bd", args, ".") {
        Ok(_) -> Ok(Nil)
        Error(shell.CommandError(code, stderr)) ->
          Error(CommandFailed(code, stderr))
        Error(shell.NotFound(cmd)) ->
          Error(CommandFailed(127, "Command not found: " <> cmd))
      }
    }
  }
}

/// Update bead status (convenience function)
pub fn update_status(
  id: String,
  status: Status,
  config: Config,
) -> Result(Nil, BeadsError) {
  let options = UpdateOptions(..default_update_options(), status: Some(status))
  update(id, options, config)
}

/// Update bead notes (convenience function)
pub fn update_notes(
  id: String,
  notes: String,
  config: Config,
) -> Result(Nil, BeadsError) {
  let options = UpdateOptions(..default_update_options(), notes: Some(notes))
  update(id, options, config)
}

/// Append to bead notes
pub fn append_notes(
  id: String,
  line: String,
  config: Config,
) -> Result(Nil, BeadsError) {
  case show(id, config) {
    Ok(t) -> {
      let existing = option.unwrap(t.notes, "")
      let separator = case existing {
        "" -> ""
        _ -> "\n"
      }
      let new_notes = existing <> separator <> line
      update_notes(id, new_notes, config)
    }
    Error(e) -> Error(e)
  }
}

/// Delete a bead (uses --no-daemon to avoid process issues)
pub fn delete(id: String, config: Config) -> Result(Nil, BeadsError) {
  case shell.run("bd", ["delete", id, "--no-daemon"], ".") {
    Ok(_) -> Ok(Nil)
    Error(shell.CommandError(code, stderr)) -> Error(CommandFailed(code, stderr))
    Error(shell.NotFound(cmd)) -> Error(CommandFailed(127, "Command not found: " <> cmd))
  }
}

/// Close a bead with optional reason
pub fn close(
  id: String,
  reason: Option(String),
  config: Config,
) -> Result(Nil, BeadsError) {
  let args = case reason {
    Some(r) -> ["close", id, "--reason=" <> r]
    None -> ["close", id]
  }
  case shell.run("bd", args, ".") {
    Ok(_) -> Ok(Nil)
    Error(shell.CommandError(code, stderr)) -> Error(CommandFailed(code, stderr))
    Error(shell.NotFound(cmd)) -> Error(CommandFailed(127, "Command not found: " <> cmd))
  }
}

/// Edit a bead in $EDITOR (opens external editor)
pub fn edit(id: String, config: Config) -> Result(Nil, BeadsError) {
  case shell.run("bd", ["edit", id], ".") {
    Ok(_) -> Ok(Nil)
    Error(shell.CommandError(code, stderr)) -> Error(CommandFailed(code, stderr))
    Error(shell.NotFound(cmd)) -> Error(CommandFailed(127, "Command not found: " <> cmd))
  }
}

// ============================================================================
// Search & Discovery
// ============================================================================

/// Search beads by pattern
pub fn search(pattern: String, config: Config) -> Result(List(Task), BeadsError) {
  case shell.run("bd", ["search", pattern, "--json"], ".") {
    Ok(output) -> {
      case parse_beads_list(output) {
        Ok(tasks) -> Ok(filter_tombstones(tasks))
        Error(e) -> Error(e)
      }
    }
    Error(shell.CommandError(code, stderr)) -> Error(CommandFailed(code, stderr))
    Error(shell.NotFound(cmd)) -> Error(CommandFailed(127, "Command not found: " <> cmd))
  }
}

/// Get ready (unblocked) beads
pub fn ready(config: Config) -> Result(List(Task), BeadsError) {
  case shell.run("bd", ["ready", "--json"], ".") {
    Ok(output) -> {
      case parse_beads_list(output) {
        Ok(tasks) -> Ok(filter_tombstones(tasks))
        Error(e) -> Error(e)
      }
    }
    Error(shell.CommandError(code, stderr)) -> Error(CommandFailed(code, stderr))
    Error(shell.NotFound(cmd)) -> Error(CommandFailed(127, "Command not found: " <> cmd))
  }
}

/// Get children of an epic
pub fn get_epic_children(
  epic_id: String,
  config: Config,
) -> Result(List(Task), BeadsError) {
  case show(epic_id, config) {
    Ok(epic) -> {
      // Get child IDs from dependents with parent-child type
      let child_ids = task.get_children(epic)
      // Fetch each child
      let children =
        child_ids
        |> list.filter_map(fn(id) {
          case show(id, config) {
            Ok(child) ->
              case child.is_tombstone {
                True -> Error(Nil)
                False -> Ok(child)
              }
            Error(_) -> Error(Nil)
          }
        })
      Ok(children)
    }
    Error(e) -> Error(e)
  }
}

// ============================================================================
// Dependencies
// ============================================================================

/// Add a dependency between beads
pub fn add_dependency(
  id: String,
  depends_on: String,
  dep_type: DependentType,
  config: Config,
) -> Result(Nil, BeadsError) {
  let type_str = task.dependent_type_to_string(dep_type)
  case shell.run("bd", ["dep", "add", id, depends_on, "--type=" <> type_str], ".") {
    Ok(_) -> Ok(Nil)
    Error(shell.CommandError(code, stderr)) -> Error(CommandFailed(code, stderr))
    Error(shell.NotFound(cmd)) -> Error(CommandFailed(127, "Command not found: " <> cmd))
  }
}

/// Remove a dependency between beads
pub fn remove_dependency(
  id: String,
  depends_on: String,
  config: Config,
) -> Result(Nil, BeadsError) {
  case shell.run("bd", ["dep", "remove", id, depends_on], ".") {
    Ok(_) -> Ok(Nil)
    Error(shell.CommandError(code, stderr)) -> Error(CommandFailed(code, stderr))
    Error(shell.NotFound(cmd)) -> Error(CommandFailed(127, "Command not found: " <> cmd))
  }
}

/// Add parent-child relationship (convenience for epics)
pub fn add_child_to_epic(
  epic_id: String,
  child_id: String,
  config: Config,
) -> Result(Nil, BeadsError) {
  add_dependency(child_id, epic_id, task.ParentChild, config)
}

// ============================================================================
// Sync
// ============================================================================

/// Sync beads (for worktrees)
pub fn sync(config: Config) -> Result(Nil, BeadsError) {
  case shell.run("bd", ["sync"], ".") {
    Ok(_) -> Ok(Nil)
    Error(shell.CommandError(code, stderr)) -> Error(CommandFailed(code, stderr))
    Error(shell.NotFound(cmd)) -> Error(CommandFailed(127, "Command not found: " <> cmd))
  }
}

/// Sync from main branch (for new worktrees)
pub fn sync_from_main(config: Config) -> Result(Nil, BeadsError) {
  case shell.run("bd", ["sync", "--from-main"], ".") {
    Ok(_) -> Ok(Nil)
    Error(shell.CommandError(code, stderr)) -> Error(CommandFailed(code, stderr))
    Error(shell.NotFound(cmd)) -> Error(CommandFailed(127, "Command not found: " <> cmd))
  }
}

// ============================================================================
// Argument Builders
// ============================================================================

fn build_create_args(options: CreateOptions) -> List(String) {
  let base = ["create"]

  let with_title = case options.title {
    Some(t) -> list.append(base, ["--title=" <> t])
    None -> base
  }

  let with_type = case options.issue_type {
    Some(t) -> list.append(with_title, ["--type=" <> task.issue_type_to_string(t)])
    None -> list.append(with_title, ["--type=task"])
  }

  let with_priority = case options.priority {
    Some(p) -> list.append(with_type, ["--priority=" <> int.to_string(task.priority_to_int(p))])
    None -> with_type
  }

  let with_description = case options.description {
    Some(d) -> list.append(with_priority, ["--description=" <> d])
    None -> with_priority
  }

  let with_design = case options.design {
    Some(d) -> list.append(with_description, ["--design=" <> d])
    None -> with_description
  }

  let with_notes = case options.notes {
    Some(n) -> list.append(with_design, ["--notes=" <> n])
    None -> with_design
  }

  let with_acceptance = case options.acceptance {
    Some(a) -> list.append(with_notes, ["--acceptance=" <> a])
    None -> with_notes
  }

  let with_assignee = case options.assignee {
    Some(a) -> list.append(with_acceptance, ["--assignee=" <> a])
    None -> with_acceptance
  }

  let with_labels = case options.labels {
    [] -> with_assignee
    labels -> {
      list.fold(labels, with_assignee, fn(acc, label) {
        list.append(acc, ["--label=" <> label])
      })
    }
  }

  let with_estimate = case options.estimate {
    Some(e) -> list.append(with_labels, ["--estimate=" <> e])
    None -> with_labels
  }

  with_estimate
}

fn build_update_args(id: String, options: UpdateOptions) -> List(String) {
  let base = ["update", id]

  let with_title = case options.title {
    Some(t) -> list.append(base, ["--title=" <> t])
    None -> base
  }

  let with_status = case options.status {
    Some(s) -> list.append(with_title, ["--status=" <> task.status_to_string(s)])
    None -> with_title
  }

  let with_priority = case options.priority {
    Some(p) -> list.append(with_status, ["--priority=" <> int.to_string(task.priority_to_int(p))])
    None -> with_status
  }

  let with_description = case options.description {
    Some(d) -> list.append(with_priority, ["--description=" <> d])
    None -> with_priority
  }

  let with_design = case options.design {
    Some(d) -> list.append(with_description, ["--design=" <> d])
    None -> with_description
  }

  let with_notes = case options.notes {
    Some(n) -> list.append(with_design, ["--notes=" <> n])
    None -> with_design
  }

  let with_acceptance = case options.acceptance {
    Some(a) -> list.append(with_notes, ["--acceptance=" <> a])
    None -> with_notes
  }

  let with_assignee = case options.assignee {
    Some(a) -> list.append(with_acceptance, ["--assignee=" <> a])
    None -> with_acceptance
  }

  let with_labels = case options.labels {
    Some(labels) -> {
      list.fold(labels, with_assignee, fn(acc, label) {
        list.append(acc, ["--label=" <> label])
      })
    }
    None -> with_assignee
  }

  let with_estimate = case options.estimate {
    Some(e) -> list.append(with_labels, ["--estimate=" <> e])
    None -> with_labels
  }

  with_estimate
}

// ============================================================================
// JSON Parsing
// ============================================================================

fn parse_beads_list(output: String) -> Result(List(Task), BeadsError) {
  case json.parse(from: output, using: decode.list(task_decoder())) {
    Ok(tasks) -> Ok(tasks)
    Error(e) -> Error(ParseError(string.inspect(e)))
  }
}

fn parse_single_bead(output: String, id: String) -> Result(Task, BeadsError) {
  case json.parse(from: output, using: task_decoder()) {
    Ok(t) -> Ok(t)
    Error(_) -> Error(NotFound(id))
  }
}

/// Decoder for Task/Bead
fn task_decoder() -> Decoder(Task) {
  use id <- decode.field("id", decode.string)
  use title <- decode.field("title", decode.string)
  use status <- decode.field("status", status_decoder())
  use created_at <- decode.field("created_at", decode.string)
  use updated_at <- decode.field("updated_at", decode.string)
  use description <- decode.optional_field("description", "", decode.string)
  use priority <- decode.optional_field("priority", task.P2, priority_decoder())
  use issue_type <- decode.optional_field("type", task.TaskType, issue_type_decoder())
  use parent_id <- decode.optional_field("parent_id", None, decode.optional(decode.string))
  use design <- decode.optional_field("design", None, decode.optional(decode.string))
  use notes <- decode.optional_field("notes", None, decode.optional(decode.string))
  use acceptance <- decode.optional_field("acceptance", None, decode.optional(decode.string))
  use assignee <- decode.optional_field("assignee", None, decode.optional(decode.string))
  use labels <- decode.optional_field("labels", [], decode.list(decode.string))
  use estimate <- decode.optional_field("estimate", None, decode.optional(decode.string))
  use dependents <- decode.optional_field("dependents", [], decode.list(dependent_decoder()))
  use blockers <- decode.optional_field("blockers", [], decode.list(decode.string))
  use attachments <- decode.optional_field("attachments", [], decode.list(decode.string))
  use is_tombstone <- decode.optional_field("tombstone", False, decode.bool)

  decode.success(Task(
    id:,
    title:,
    description:,
    status:,
    priority:,
    issue_type:,
    parent_id:,
    created_at:,
    updated_at:,
    design:,
    notes:,
    acceptance:,
    assignee:,
    labels:,
    estimate:,
    dependents:,
    blockers:,
    attachments:,
    is_tombstone:,
  ))
}

/// Decoder for Dependent
fn dependent_decoder() -> Decoder(Dependent) {
  use id <- decode.field("id", decode.string)
  use dep_type <- decode.field("type", dependent_type_decoder())
  decode.success(Dependent(id:, dep_type:))
}

/// Decoder for Status
fn status_decoder() -> Decoder(Status) {
  use s <- decode.then(decode.string)
  decode.success(task.status_from_string(s))
}

/// Decoder for Priority (handles both int and string formats)
fn priority_decoder() -> Decoder(Priority) {
  decode.one_of(decode.int |> decode.map(task.priority_from_int), [
    decode.string |> decode.map(task.priority_from_string),
  ])
}

/// Decoder for IssueType
fn issue_type_decoder() -> Decoder(IssueType) {
  use s <- decode.then(decode.string)
  decode.success(task.issue_type_from_string(s))
}

/// Decoder for DependentType
fn dependent_type_decoder() -> Decoder(DependentType) {
  use s <- decode.then(decode.string)
  decode.success(task.dependent_type_from_string(s))
}

// ============================================================================
// Utilities
// ============================================================================

fn filter_tombstones(tasks: List(Task)) -> List(Task) {
  list.filter(tasks, fn(t) { !t.is_tombstone })
}
