// Bead Editor Service - markdown template editing in tmux popup
// Matches TypeScript BeadEditorService behavior

import gleam/list
import gleam/option.{type Option, None, Some}
import gleam/result
import gleam/string
import azedarach/config.{type Config}
import azedarach/domain/task.{type IssueType, type Priority, type Status, type Task}
import azedarach/services/beads
import azedarach/util/shell

// ============================================================================
// Constants
// ============================================================================

const separator = "───────────────────────────────────────────────────"

// Anchor placeholders for new bead creation
const anchor_title = "TITLE"

const anchor_description = "DESCRIPTION"

const anchor_design = "DESIGN"

const anchor_notes = "NOTES"

const anchor_acceptance = "ACCEPTANCE"

// ============================================================================
// Error Types
// ============================================================================

pub type EditorError {
  BeadNotFound(id: String)
  EditorFailed(message: String)
  ParseFailed(message: String)
  TmuxError(message: String)
}

pub fn error_to_string(err: EditorError) -> String {
  case err {
    BeadNotFound(id) -> "Bead not found: " <> id
    EditorFailed(msg) -> "Editor failed: " <> msg
    ParseFailed(msg) -> "Failed to parse editor content: " <> msg
    TmuxError(msg) -> "Tmux error: " <> msg
  }
}

// ============================================================================
// Markdown Serialization
// ============================================================================

/// Serialize a task to markdown format for editing
pub fn serialize_to_markdown(t: Task) -> String {
  let header = build_header(t)
  let metadata = build_metadata(t)
  let sections = build_sections(t)

  header <> "\n" <> separator <> "\n" <> metadata <> separator <> "\n\n" <> sections
}

/// Serialize an empty template for new bead creation
pub fn serialize_new_template(issue_type: IssueType) -> String {
  let id_placeholder = "NEW"
  let header = "# " <> id_placeholder <> ": " <> anchor_title

  let metadata =
    [
      "Type:     " <> task.issue_type_to_string(issue_type) <> "        (read-only - changing requires delete+create)",
      "Priority: P2",
      "Status:   open",
      "Assignee: ",
      "Labels:   ",
      "Estimate: ",
    ]
    |> string.join("\n")

  let sections =
    [
      "## Description",
      "",
      anchor_description,
      "",
      "## Design",
      "",
      anchor_design,
      "",
      "## Notes",
      "",
      anchor_notes,
      "",
      "## Acceptance Criteria",
      "",
      anchor_acceptance,
      "",
    ]
    |> string.join("\n")

  header <> "\n" <> separator <> "\n" <> metadata <> "\n" <> separator <> "\n\n" <> sections
}

fn build_header(t: Task) -> String {
  "# " <> t.id <> ": " <> t.title
}

fn build_metadata(t: Task) -> String {
  let type_str = task.issue_type_to_string(t.issue_type)
  let priority_str = task.priority_to_string(t.priority)
  let status_str = task.status_to_string(t.status)
  let assignee_str = option.unwrap(t.assignee, "")
  let labels_str = string.join(t.labels, ", ")
  let estimate_str = option.unwrap(t.estimate, "")

  [
    "Type:     " <> type_str <> "        (read-only - changing requires delete+create)",
    "Priority: " <> priority_str,
    "Status:   " <> status_str,
    "Assignee: " <> assignee_str,
    "Labels:   " <> labels_str,
    "Estimate: " <> estimate_str,
  ]
  |> string.join("\n")
  |> fn(s) { s <> "\n" }
}

fn build_sections(t: Task) -> String {
  let description_content = case t.description {
    "" -> ""
    d -> d
  }

  let design_content = option.unwrap(t.design, "")
  let notes_content = option.unwrap(t.notes, "")
  let acceptance_content = option.unwrap(t.acceptance, "")

  [
    "## Description",
    "",
    description_content,
    "",
    "## Design",
    "",
    design_content,
    "",
    "## Notes",
    "",
    notes_content,
    "",
    "## Acceptance Criteria",
    "",
    acceptance_content,
    "",
  ]
  |> string.join("\n")
}

// ============================================================================
// Markdown Parsing
// ============================================================================

/// Parsed content from markdown editor
pub type ParsedContent {
  ParsedContent(
    title: String,
    priority: Priority,
    status: Status,
    assignee: Option(String),
    labels: List(String),
    estimate: Option(String),
    description: String,
    design: String,
    notes: String,
    acceptance: String,
  )
}

/// Parse markdown content back to structured data
pub fn parse_from_markdown(content: String) -> Result(ParsedContent, EditorError) {
  let lines = string.split(content, "\n")

  // Parse header (title)
  let title = parse_title(lines)

  // Parse metadata section
  let priority = parse_metadata_field(lines, "Priority:")
  let status = parse_metadata_field(lines, "Status:")
  let assignee = parse_metadata_field(lines, "Assignee:")
  let labels = parse_metadata_field(lines, "Labels:")
  let estimate = parse_metadata_field(lines, "Estimate:")

  // Parse content sections
  let description = parse_section(content, "## Description")
  let design = parse_section(content, "## Design")
  let notes = parse_section(content, "## Notes")
  let acceptance = parse_section(content, "## Acceptance Criteria")

  // Validate and build result
  case title {
    "" -> Error(ParseFailed("Missing title in header"))
    t ->
      Ok(ParsedContent(
        title: clean_anchor(t, anchor_title),
        priority: task.priority_from_string(priority),
        status: task.status_from_string(status),
        assignee: non_empty_option(assignee),
        labels: parse_labels(labels),
        estimate: non_empty_option(estimate),
        description: clean_anchor(description, anchor_description),
        design: clean_anchor(design, anchor_design),
        notes: clean_anchor(notes, anchor_notes),
        acceptance: clean_anchor(acceptance, anchor_acceptance),
      ))
  }
}

fn parse_title(lines: List(String)) -> String {
  lines
  |> list.find(fn(line) { string.starts_with(line, "# ") })
  |> result.map(fn(line) {
    // Format: "# ID: Title"
    let trimmed = string.drop_start(line, 2)
    case string.split_once(trimmed, ": ") {
      Ok(#(_, title)) -> string.trim(title)
      Error(_) -> string.trim(trimmed)
    }
  })
  |> result.unwrap("")
}

fn parse_metadata_field(lines: List(String), prefix: String) -> String {
  lines
  |> list.find(fn(line) {
    let trimmed = string.trim(line)
    string.starts_with(trimmed, prefix)
  })
  |> result.map(fn(line) {
    let parts = string.split(line, ":")
    case list.rest(parts) {
      Ok(rest) -> {
        let value = string.join(rest, ":")
        // Remove any trailing comments like "(read-only...)"
        case string.split_once(value, "(") {
          Ok(#(before, _)) -> string.trim(before)
          Error(_) -> string.trim(value)
        }
      }
      Error(_) -> ""
    }
  })
  |> result.unwrap("")
}

fn parse_section(content: String, header: String) -> String {
  // Find content between this header and the next ## header (or end)
  let parts = string.split(content, header)
  case list.rest(parts) {
    Ok([section_content, ..]) -> {
      // Find next section header
      case string.split_once(section_content, "\n## ") {
        Ok(#(before, _)) -> string.trim(before)
        Error(_) -> string.trim(section_content)
      }
    }
    _ -> ""
  }
}

fn parse_labels(labels_str: String) -> List(String) {
  case string.trim(labels_str) {
    "" -> []
    s ->
      string.split(s, ",")
      |> list.map(string.trim)
      |> list.filter(fn(l) { l != "" })
  }
}

fn non_empty_option(s: String) -> Option(String) {
  case string.trim(s) {
    "" -> None
    trimmed -> Some(trimmed)
  }
}

fn clean_anchor(content: String, anchor: String) -> String {
  // Remove anchor placeholder if it exists
  let trimmed = string.trim(content)
  case trimmed == anchor {
    True -> ""
    False -> trimmed
  }
}

// ============================================================================
// Tmux Editor Integration
// ============================================================================

/// Open a bead in the editor via tmux popup
pub fn edit_bead(
  id: String,
  config: Config,
) -> Result(ParsedContent, EditorError) {
  // Fetch the bead
  case beads.show(id, config) {
    Ok(t) -> {
      // Serialize to markdown
      let markdown = serialize_to_markdown(t)

      // Write to temp file
      let temp_file = "/tmp/bead-edit-" <> id <> ".md"
      case write_temp_file(temp_file, markdown) {
        Ok(_) -> {
          // Open editor in tmux popup
          case open_editor_popup(temp_file, config) {
            Ok(_) -> {
              // Read back and parse
              case read_temp_file(temp_file) {
                Ok(edited) -> {
                  let _ = delete_temp_file(temp_file)
                  parse_from_markdown(edited)
                }
                Error(msg) -> Error(EditorFailed(msg))
              }
            }
            Error(e) -> Error(e)
          }
        }
        Error(msg) -> Error(EditorFailed(msg))
      }
    }
    Error(_) -> Error(BeadNotFound(id))
  }
}

/// Create a new bead via editor
pub fn create_bead(
  issue_type: IssueType,
  config: Config,
) -> Result(ParsedContent, EditorError) {
  // Generate template
  let markdown = serialize_new_template(issue_type)

  // Write to temp file
  let temp_file = "/tmp/bead-new-" <> task.issue_type_to_string(issue_type) <> ".md"
  case write_temp_file(temp_file, markdown) {
    Ok(_) -> {
      // Open editor in tmux popup
      case open_editor_popup(temp_file, config) {
        Ok(_) -> {
          // Read back and parse
          case read_temp_file(temp_file) {
            Ok(edited) -> {
              let _ = delete_temp_file(temp_file)
              parse_from_markdown(edited)
            }
            Error(msg) -> Error(EditorFailed(msg))
          }
        }
        Error(e) -> Error(e)
      }
    }
    Error(msg) -> Error(EditorFailed(msg))
  }
}

/// Open editor in tmux popup with wait-for synchronization
fn open_editor_popup(file_path: String, _config: Config) -> Result(Nil, EditorError) {
  // Get editor from environment
  let editor = get_editor()

  // Generate unique channel for wait-for
  let channel = "bead-editor-" <> generate_id()

  // Build popup command with wait-for
  let popup_cmd =
    editor
    <> " "
    <> file_path
    <> "; tmux wait-for -S "
    <> channel

  // Calculate popup size (80% of terminal)
  let popup_args = [
    "display-popup",
    "-E",
    "-w",
    "80%",
    "-h",
    "80%",
    popup_cmd,
  ]

  // Run tmux popup
  case shell.run("tmux", popup_args, ".") {
    Ok(_) -> Ok(Nil)
    Error(shell.CommandError(_, stderr)) -> Error(TmuxError(stderr))
    Error(shell.NotFound(_)) -> Error(TmuxError("tmux not found"))
  }
}

fn get_editor() -> String {
  case shell.get_env("EDITOR") {
    Ok(e) -> e
    Error(_) -> {
      case shell.get_env("VISUAL") {
        Ok(v) -> v
        Error(_) -> "vim"
      }
    }
  }
}

fn generate_id() -> String {
  // Simple timestamp-based ID
  case shell.run("date", ["+%s%N"], ".") {
    Ok(output) -> string.trim(output)
    Error(_) -> "default"
  }
}

// ============================================================================
// File Operations
// ============================================================================

fn write_temp_file(path: String, content: String) -> Result(Nil, String) {
  // Use shell echo to write file
  let escaped = escape_for_shell(content)
  case shell.run("sh", ["-c", "echo '" <> escaped <> "' > " <> path], ".") {
    Ok(_) -> Ok(Nil)
    Error(shell.CommandError(_, stderr)) -> Error(stderr)
    Error(shell.NotFound(_)) -> Error("sh not found")
  }
}

fn read_temp_file(path: String) -> Result(String, String) {
  case shell.run("cat", [path], ".") {
    Ok(content) -> Ok(content)
    Error(shell.CommandError(_, stderr)) -> Error(stderr)
    Error(shell.NotFound(_)) -> Error("cat not found")
  }
}

fn delete_temp_file(path: String) -> Result(Nil, String) {
  case shell.run("rm", ["-f", path], ".") {
    Ok(_) -> Ok(Nil)
    Error(shell.CommandError(_, stderr)) -> Error(stderr)
    Error(shell.NotFound(_)) -> Error("rm not found")
  }
}

fn escape_for_shell(s: String) -> String {
  // Escape single quotes for shell
  string.replace(s, "'", "'\"'\"'")
}

// ============================================================================
// Apply Changes
// ============================================================================

/// Apply parsed content changes to a bead
pub fn apply_changes(
  id: String,
  parsed: ParsedContent,
  config: Config,
) -> Result(Nil, EditorError) {
  let options =
    beads.UpdateOptions(
      title: Some(parsed.title),
      status: Some(parsed.status),
      priority: Some(parsed.priority),
      description: Some(parsed.description),
      design: Some(parsed.design),
      notes: Some(parsed.notes),
      acceptance: Some(parsed.acceptance),
      assignee: parsed.assignee,
      labels: Some(parsed.labels),
      estimate: parsed.estimate,
    )

  case beads.update(id, options, config) {
    Ok(_) -> Ok(Nil)
    Error(beads.NotFound(i)) -> Error(BeadNotFound(i))
    Error(e) -> Error(EditorFailed(beads.error_to_string(e)))
  }
}

/// Create a new bead from parsed content
pub fn create_from_parsed(
  parsed: ParsedContent,
  issue_type: IssueType,
  config: Config,
) -> Result(String, EditorError) {
  let options =
    beads.CreateOptions(
      title: Some(parsed.title),
      issue_type: Some(issue_type),
      priority: Some(parsed.priority),
      description: Some(parsed.description),
      design: Some(parsed.design),
      notes: Some(parsed.notes),
      acceptance: Some(parsed.acceptance),
      assignee: parsed.assignee,
      labels: parsed.labels,
      estimate: parsed.estimate,
    )

  case beads.create(options, config) {
    Ok(id) -> Ok(id)
    Error(e) -> Error(EditorFailed(beads.error_to_string(e)))
  }
}
