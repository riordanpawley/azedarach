// Project Service - manages multi-project support
//
// Handles project registry, switching, and state persistence.
// Projects are stored globally in ~/.config/azedarach/projects.json
// Each project has its own beads database at .beads/

import gleam/dynamic/decode.{type Decoder}
import gleam/json
import gleam/list
import gleam/option.{type Option, None, Some}
import gleam/result
import gleam/string
import azedarach/domain/project.{type Project, Project}
import azedarach/util/shell
import simplifile

// ============================================================================
// Types
// ============================================================================

pub type ProjectError {
  NotFound(path: String)
  AlreadyExists(name: String)
  NoBeadsDir(path: String)
  ConfigError(message: String)
  StorageError(message: String)
}

pub fn error_to_string(err: ProjectError) -> String {
  case err {
    NotFound(path) -> "Project not found: " <> path
    AlreadyExists(name) -> "Project with name '" <> name <> "' already exists"
    NoBeadsDir(path) ->
      "No .beads directory found in " <> path <> ". Run 'bd init' to initialize beads tracking."
    ConfigError(msg) -> "Config error: " <> msg
    StorageError(msg) -> "Storage error: " <> msg
  }
}

/// Project entry in the registry
pub type ProjectEntry {
  ProjectEntry(name: String, path: String, beads_path: Option(String))
}

/// Projects registry stored in ~/.config/azedarach/projects.json
pub type ProjectsConfig {
  ProjectsConfig(projects: List(ProjectEntry), default_project: Option(String))
}

// ============================================================================
// Config Paths
// ============================================================================

const config_dir = ".config/azedarach"

const projects_file = "projects.json"

fn config_path() -> String {
  shell.home_dir() <> "/" <> config_dir
}

fn projects_path() -> String {
  config_path() <> "/" <> projects_file
}

// ============================================================================
// Registry Operations
// ============================================================================

/// Load projects registry from global config file
pub fn load_registry() -> Result(ProjectsConfig, ProjectError) {
  case simplifile.read(projects_path()) {
    Ok(content) -> parse_registry(content)
    Error(_) -> Ok(ProjectsConfig(projects: [], default_project: None))
  }
}

/// Save projects registry to global config file
/// Uses atomic write (write-to-temp-then-rename) to prevent corruption on crash
pub fn save_registry(config: ProjectsConfig) -> Result(Nil, ProjectError) {
  // Ensure config directory exists
  let _ = simplifile.create_directory_all(config_path())

  let content = encode_registry(config)
  let final_path = projects_path()
  let temp_path = final_path <> ".tmp"

  // Write to temp file first
  case simplifile.write(temp_path, content) {
    Ok(_) -> {
      // Atomic rename from temp to final (POSIX guarantees atomicity)
      case simplifile.rename(temp_path, final_path) {
        Ok(_) -> Ok(Nil)
        Error(_) -> {
          // Clean up temp file on rename failure
          let _ = simplifile.delete(temp_path)
          Error(StorageError("Failed to save projects config (rename failed)"))
        }
      }
    }
    Error(_) -> Error(StorageError("Failed to save projects config (write failed)"))
  }
}

fn parse_registry(content: String) -> Result(ProjectsConfig, ProjectError) {
  case json.parse(content, registry_decoder()) {
    Ok(config) -> Ok(config)
    Error(_) -> Ok(ProjectsConfig(projects: [], default_project: None))
  }
}

fn registry_decoder() -> Decoder(ProjectsConfig) {
  use projects <- decode.optional_field(
    "projects",
    [],
    decode.list(project_entry_decoder()),
  )
  use default_project <- decode.optional_field(
    "defaultProject",
    None,
    decode.optional(decode.string),
  )
  decode.success(ProjectsConfig(projects:, default_project:))
}

fn project_entry_decoder() -> Decoder(ProjectEntry) {
  use name <- decode.field("name", decode.string)
  use path <- decode.field("path", decode.string)
  use beads_path <- decode.optional_field(
    "beadsPath",
    None,
    decode.optional(decode.string),
  )
  decode.success(ProjectEntry(name:, path:, beads_path:))
}

fn encode_registry(config: ProjectsConfig) -> String {
  json.object([
    #("projects", json.array(config.projects, encode_project_entry)),
    #(
      "defaultProject",
      case config.default_project {
        Some(name) -> json.string(name)
        None -> json.null()
      },
    ),
  ])
  |> json.to_string
}

fn encode_project_entry(entry: ProjectEntry) -> json.Json {
  json.object([
    #("name", json.string(entry.name)),
    #("path", json.string(entry.path)),
    #(
      "beadsPath",
      case entry.beads_path {
        Some(p) -> json.string(p)
        None -> json.null()
      },
    ),
  ])
}

// ============================================================================
// Project Management
// ============================================================================

/// Add a new project to the registry
pub fn add_project(
  path: String,
  name: Option(String),
) -> Result(ProjectEntry, ProjectError) {
  // Resolve absolute path
  let abs_path = case shell.realpath(path) {
    Ok(p) -> p
    Error(_) -> path
  }

  // Validate path exists
  case shell.is_directory(abs_path) {
    False -> Error(NotFound(abs_path))
    True -> {
      // Validate .beads directory exists
      let beads_dir = abs_path <> "/.beads"
      case shell.is_directory(beads_dir) {
        False -> Error(NoBeadsDir(abs_path))
        True -> {
          // Derive name from directory if not provided
          let project_name = case name {
            Some(n) -> n
            None -> shell.basename(abs_path)
          }

          // Load current registry
          case load_registry() {
            Ok(config) -> {
              // Check for duplicate name
              case list.find(config.projects, fn(p) { p.name == project_name }) {
                Ok(_) -> Error(AlreadyExists(project_name))
                Error(_) -> {
                  // Check for duplicate path
                  case list.find(config.projects, fn(p) { p.path == abs_path }) {
                    Ok(existing) -> Error(AlreadyExists(existing.name))
                    Error(_) -> {
                      // Create new entry
                      let entry =
                        ProjectEntry(
                          name: project_name,
                          path: abs_path,
                          beads_path: Some(beads_dir),
                        )

                      // Add to registry
                      let new_config =
                        ProjectsConfig(
                          ..config,
                          projects: list.append(config.projects, [entry]),
                        )

                      case save_registry(new_config) {
                        Ok(_) -> Ok(entry)
                        Error(e) -> Error(e)
                      }
                    }
                  }
                }
              }
            }
            Error(e) -> Error(e)
          }
        }
      }
    }
  }
}

/// Remove a project from the registry by name
pub fn remove_project(name: String) -> Result(Nil, ProjectError) {
  case load_registry() {
    Ok(config) -> {
      case list.find(config.projects, fn(p) { p.name == name }) {
        Ok(_) -> {
          let new_projects = list.filter(config.projects, fn(p) { p.name != name })
          let new_default = case config.default_project {
            Some(d) if d == name -> None
            other -> other
          }
          let new_config =
            ProjectsConfig(projects: new_projects, default_project: new_default)
          save_registry(new_config)
        }
        Error(_) -> Error(NotFound(name))
      }
    }
    Error(e) -> Error(e)
  }
}

/// List all registered projects
pub fn list_projects() -> Result(List(ProjectEntry), ProjectError) {
  case load_registry() {
    Ok(config) -> Ok(config.projects)
    Error(e) -> Error(e)
  }
}

/// Switch to a project by name and set it as default
pub fn switch_project(name: String) -> Result(ProjectEntry, ProjectError) {
  case load_registry() {
    Ok(config) -> {
      case list.find(config.projects, fn(p) { p.name == name }) {
        Ok(project) -> {
          let new_config = ProjectsConfig(..config, default_project: Some(name))
          case save_registry(new_config) {
            Ok(_) -> Ok(project)
            Error(e) -> Error(e)
          }
        }
        Error(_) -> Error(NotFound(name))
      }
    }
    Error(e) -> Error(e)
  }
}

/// Get current project based on CWD or default
pub fn get_current() -> Result(ProjectEntry, ProjectError) {
  case load_registry() {
    Ok(config) -> {
      let cwd = case shell.cwd() {
        Ok(c) -> c
        Error(_) -> "."
      }

      // Check if CWD matches a registered project
      case list.find(config.projects, fn(p) { p.path == cwd }) {
        Ok(project) -> Ok(project)
        Error(_) -> {
          // Check if CWD is inside a registered project
          case
            list.find(config.projects, fn(p) {
              string.starts_with(cwd, p.path <> "/")
            })
          {
            Ok(project) -> Ok(project)
            Error(_) -> {
              // Check if CWD is a worktree of a registered project
              // Worktrees are siblings: /path/project-branchname
              let cwd_parent = shell.dirname(cwd)
              let cwd_base = shell.basename(cwd)
              case
                list.find(config.projects, fn(p) {
                  shell.dirname(p.path) == cwd_parent
                  && string.starts_with(cwd_base, shell.basename(p.path) <> "-")
                })
              {
                Ok(project) -> Ok(project)
                Error(_) -> {
                  // Fall back to default project
                  case config.default_project {
                    Some(name) -> {
                      case list.find(config.projects, fn(p) { p.name == name }) {
                        Ok(project) -> Ok(project)
                        Error(_) -> {
                          // Fall back to first project
                          case config.projects {
                            [first, ..] -> Ok(first)
                            [] -> Error(NotFound("No projects registered"))
                          }
                        }
                      }
                    }
                    None -> {
                      // Fall back to first project
                      case config.projects {
                        [first, ..] -> Ok(first)
                        [] -> Error(NotFound("No projects registered"))
                      }
                    }
                  }
                }
              }
            }
          }
        }
      }
    }
    Error(e) -> Error(e)
  }
}

/// Get the config file path (for display/debugging)
pub fn get_config_path() -> String {
  projects_path()
}

// ============================================================================
// Project Discovery (auto-discover from common locations)
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

/// Select the best project based on context
pub fn select_initial() -> Result(Project, ProjectError) {
  // First try the registry
  case get_current() {
    Ok(entry) -> from_path(entry.path)
    Error(_) -> {
      // Fall back to CWD
      case from_cwd() {
        Ok(p) -> Ok(p)
        Error(_) -> {
          // Fall back to discovery
          case discover_all() {
            [first, ..] -> Ok(first)
            [] -> Error(NotFound("No projects found"))
          }
        }
      }
    }
  }
}

/// Switch to a different project by path
pub fn switch_to(path: String) -> Result(Project, ProjectError) {
  from_path(path)
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
pub fn config_path_for(proj: Project) -> String {
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

/// Format entry for display
pub fn format_entry(entry: ProjectEntry, is_current: Bool) -> String {
  let marker = case is_current {
    True -> "* "
    False -> "  "
  }
  marker <> entry.name <> " (" <> short_path(entry.path) <> ")"
}

fn short_path(path: String) -> String {
  let home = shell.home_dir()
  case string.starts_with(path, home) {
    True -> "~" <> string.drop_start(path, string.length(home))
    False -> path
  }
}
