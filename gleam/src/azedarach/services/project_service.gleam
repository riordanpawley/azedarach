// Project Service - manages multi-project support
//
// Handles project discovery, switching, and state persistence.
// Each project has its own beads database at .beads/

import gleam/decode.{type Decoder}
import gleam/dict.{type Dict}
import gleam/json
import gleam/list
import gleam/option.{type Option, None, Some}
import gleam/result
import gleam/string
import azedarach/config.{type Config}
import azedarach/domain/project.{type Project, Project}
import azedarach/util/shell
import simplifile

// ============================================================================
// Types
// ============================================================================

pub type ProjectError {
  NotFound(path: String)
  ConfigError(message: String)
  StorageError(message: String)
}

pub fn error_to_string(err: ProjectError) -> String {
  case err {
    NotFound(path) -> "Project not found: " <> path
    ConfigError(msg) -> "Config error: " <> msg
    StorageError(msg) -> "Storage error: " <> msg
  }
}

/// State file for remembering last project
const state_file = ".azedarach-state.json"

/// Project state stored in home directory
pub type ProjectState {
  ProjectState(
    last_project: Option(String),
    recent_projects: List(String),
  )
}

// ============================================================================
// Project Discovery
// ============================================================================

/// Discover all projects with .azedarach.json
pub fn discover_all() -> List(Project) {
  project.discover()
}

/// Get project from current working directory
pub fn from_cwd() -> Result(Project, ProjectError) {
  case project.from_cwd() {
    Ok(p) -> Ok(p)
    Error(msg) -> Error(NotFound(msg))
  }
}

/// Get project from a specific path
pub fn from_path(path: String) -> Result(Project, ProjectError) {
  case project.from_path(path) {
    Ok(p) -> Ok(p)
    Error(msg) -> Error(NotFound(msg))
  }
}

// ============================================================================
// Project Selection
// ============================================================================

/// Select the best project based on context
/// 1. If CWD has .azedarach.json, use it
/// 2. If we have a saved last project, use it
/// 3. Otherwise, use the first discovered project
pub fn select_initial() -> Result(Project, ProjectError) {
  // Try CWD first
  case from_cwd() {
    Ok(p) if p.has_config -> {
      // Save as last project
      let _ = save_last_project(p.path)
      Ok(p)
    }
    _ -> {
      // Try last saved project
      case load_last_project() {
        Ok(Some(path)) -> {
          case from_path(path) {
            Ok(p) -> Ok(p)
            Error(_) -> select_from_discovered()
          }
        }
        _ -> select_from_discovered()
      }
    }
  }
}

fn select_from_discovered() -> Result(Project, ProjectError) {
  case discover_all() {
    [first, ..] -> {
      let _ = save_last_project(first.path)
      Ok(first)
    }
    [] -> Error(NotFound("No projects found"))
  }
}

/// Switch to a different project by path
pub fn switch_to(path: String) -> Result(Project, ProjectError) {
  case from_path(path) {
    Ok(p) -> {
      let _ = save_last_project(path)
      let _ = add_to_recent(path)
      Ok(p)
    }
    Error(e) -> Error(e)
  }
}

// ============================================================================
// State Persistence
// ============================================================================

fn state_path() -> String {
  shell.home_dir() <> "/" <> state_file
}

fn load_state() -> Result(ProjectState, ProjectError) {
  case simplifile.read(state_path()) {
    Ok(content) -> parse_state(content)
    Error(_) -> Ok(ProjectState(last_project: None, recent_projects: []))
  }
}

fn save_state(state: ProjectState) -> Result(Nil, ProjectError) {
  let content = encode_state(state)
  case simplifile.write(state_path(), content) {
    Ok(_) -> Ok(Nil)
    Error(_) -> Error(StorageError("Failed to save project state"))
  }
}

fn parse_state(content: String) -> Result(ProjectState, ProjectError) {
  case json.parse(content, state_decoder()) {
    Ok(state) -> Ok(state)
    Error(_) -> Ok(ProjectState(last_project: None, recent_projects: []))
  }
}

fn state_decoder() -> Decoder(ProjectState) {
  use last_project <- decode.optional_field(
    "lastProject",
    decode.optional(decode.string),
    None,
  )
  use recent_projects <- decode.optional_field(
    "recentProjects",
    decode.list(decode.string),
    [],
  )
  decode.success(ProjectState(last_project:, recent_projects:))
}

fn encode_state(state: ProjectState) -> String {
  json.object([
    #("lastProject", case state.last_project {
      Some(p) -> json.string(p)
      None -> json.null()
    }),
    #("recentProjects", json.array(state.recent_projects, json.string)),
  ])
  |> json.to_string
}

fn load_last_project() -> Result(Option(String), ProjectError) {
  case load_state() {
    Ok(state) -> Ok(state.last_project)
    Error(e) -> Error(e)
  }
}

fn save_last_project(path: String) -> Result(Nil, ProjectError) {
  case load_state() {
    Ok(state) -> save_state(ProjectState(..state, last_project: Some(path)))
    Error(_) ->
      save_state(ProjectState(last_project: Some(path), recent_projects: [path]))
  }
}

fn add_to_recent(path: String) -> Result(Nil, ProjectError) {
  case load_state() {
    Ok(state) -> {
      let recent =
        [path, ..state.recent_projects]
        |> list.unique
        |> list.take(10)
      save_state(ProjectState(..state, recent_projects: recent))
    }
    Error(_) ->
      save_state(ProjectState(last_project: Some(path), recent_projects: [path]))
  }
}

/// Get recent projects
pub fn get_recent() -> List(String) {
  case load_state() {
    Ok(state) -> state.recent_projects
    Error(_) -> []
  }
}

// ============================================================================
// Project Operations
// ============================================================================

/// Initialize beads in a project
pub fn init_beads(proj: Project) -> Result(Nil, ProjectError) {
  case project.init_beads(proj) {
    Ok(_) -> Ok(Nil)
    Error(msg) -> Error(ConfigError(msg))
  }
}

/// Check if project has beads initialized
pub fn has_beads(proj: Project) -> Bool {
  project.has_beads(proj)
}

/// Get project config path
pub fn config_path(proj: Project) -> String {
  proj.path <> "/.azedarach.json"
}

/// Get beads database path
pub fn beads_path(proj: Project) -> String {
  proj.path <> "/.beads"
}

// ============================================================================
// Display Helpers
// ============================================================================

/// Format project for display
pub fn display(proj: Project) -> String {
  project.display_with_path(proj)
}

/// Get short display name
pub fn display_name(proj: Project) -> String {
  project.display_name(proj)
}

/// Get display path (with ~ for home)
pub fn display_path(proj: Project) -> String {
  project.short_path(proj)
}

/// Format project list for selector
pub fn format_project_list(projects: List(Project)) -> List(#(String, String)) {
  projects
  |> list.map(fn(p) { #(p.path, display(p)) })
}
