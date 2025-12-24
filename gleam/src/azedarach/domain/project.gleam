// Project domain type for multi-project support

pub type Project {
  Project(
    name: String,
    path: String,
    bead_prefix: String,
  )
}

pub fn display_name(project: Project) -> String {
  project.name
}

pub fn short_path(project: Project) -> String {
  // Abbreviate home directory
  case string.starts_with(project.path, "/home/") {
    True -> "~" <> string.drop_start(project.path, string.length("/home/user"))
    False -> project.path
  }
}

import gleam/string
