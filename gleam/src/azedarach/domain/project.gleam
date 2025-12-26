// Project domain type for multi-project support
//
// Projects are discovered by finding directories with .azedarach.json
// Each project has its own beads database and configuration.

import gleam/dynamic/decode
import gleam/json
import gleam/list
import gleam/option.{type Option, None}
import gleam/result
import gleam/string
import azedarach/util/shell

/// A project that can be managed by Azedarach
pub type Project {
  Project(
    /// Display name for the project
    name: String,
    /// Absolute path to project root
    path: String,
    /// Bead ID prefix (usually short name like "az")
    bead_prefix: String,
    /// Whether this project has a valid .azedarach.json
    has_config: Bool,
    /// Last time we synced beads for this project
    last_sync: Option(String),
  )
}

/// Create a project from a path, detecting config if present
pub fn from_path(path: String) -> Result(Project, String) {
  let config_path = path <> "/.azedarach.json"

  // Check if directory exists
  case shell.path_exists(path) {
    False -> Error("Path does not exist: " <> path)
    True -> {
      // Try to read config
      let #(name, prefix, has_config) = case shell.read_file(config_path) {
        Ok(content) -> {
          case parse_project_config(content) {
            Ok(#(n, p)) -> #(n, p, True)
            Error(_) -> #(basename(path), short_prefix(path), False)
          }
        }
        Error(_) -> #(basename(path), short_prefix(path), False)
      }

      Ok(Project(
        name: name,
        path: path,
        bead_prefix: prefix,
        has_config: has_config,
        last_sync: None,
      ))
    }
  }
}

/// Parse project config JSON for name and prefix
fn parse_project_config(content: String) -> Result(#(String, String), Nil) {
  let decoder = {
    use name <- decode.optional_field("projectName", "", decode.string)
    use prefix <- decode.optional_field("beadPrefix", "", decode.string)
    decode.success(#(name, prefix))
  }

  json.parse(from: content, using: decoder)
  |> result.map_error(fn(_) { Nil })
}

/// Discover projects in common locations
pub fn discover() -> List(Project) {
  let home = shell.home_dir()
  let paths = [
    home <> "/dev",
    home <> "/projects",
    home <> "/code",
    home <> "/work",
    home <> "/src",
  ]

  paths
  |> list.flat_map(fn(base) {
    case shell.list_dir(base) {
      Ok(entries) -> {
        entries
        |> list.filter_map(fn(entry) {
          let full_path = base <> "/" <> entry
          // Only include if it has .azedarach.json
          case shell.path_exists(full_path <> "/.azedarach.json") {
            True -> from_path(full_path)
            False -> Error("No config")
          }
        })
      }
      Error(_) -> []
    }
  })
}

/// Get the current working directory as a project
pub fn from_cwd() -> Result(Project, String) {
  case shell.cwd() {
    Ok(path) -> from_path(path)
    Error(_) -> Error("Could not get current directory")
  }
}

// Display helpers

pub fn display_name(project: Project) -> String {
  case project.name {
    "" -> basename(project.path)
    n -> n
  }
}

pub fn short_path(project: Project) -> String {
  let home = shell.home_dir()
  case string.starts_with(project.path, home) {
    True -> "~" <> string.drop_start(project.path, string.length(home))
    False -> project.path
  }
}

/// Format as "name (~/path)"
pub fn display_with_path(project: Project) -> String {
  display_name(project) <> " (" <> short_path(project) <> ")"
}

/// Check if project has beads database
pub fn has_beads(project: Project) -> Bool {
  shell.path_exists(project.path <> "/.beads")
}

/// Initialize beads in a project
pub fn init_beads(project: Project) -> Result(Nil, String) {
  case shell.run("bd", ["init"], project.path) {
    Ok(_) -> Ok(Nil)
    Error(shell.CommandError(_, stderr)) -> Error(stderr)
    Error(shell.NotFound(_)) -> Error("bd command not found")
  }
}

// Internal helpers

fn basename(path: String) -> String {
  path
  |> string.split("/")
  |> list.last
  |> result.unwrap("project")
}

fn short_prefix(path: String) -> String {
  basename(path)
  |> string.slice(0, 2)
  |> string.lowercase
}
