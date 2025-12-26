// Worktree service - git worktree management

import gleam/int
import gleam/list
import gleam/string
import azedarach/config.{type Config}
import azedarach/util/shell

pub type WorktreeError {
  CommandFailed(exit_code: Int, stderr: String)
  AlreadyExists(path: String)
  NotFound(path: String)
}

pub fn error_to_string(err: WorktreeError) -> String {
  case err {
    CommandFailed(code, stderr) ->
      "git worktree failed (" <> int.to_string(code) <> "): " <> stderr
    AlreadyExists(path) -> "Worktree already exists: " <> path
    NotFound(path) -> "Worktree not found: " <> path
  }
}

// Helper to convert shell errors to worktree errors
fn shell_to_worktree_error(err: shell.ShellError) -> WorktreeError {
  case err {
    shell.CommandError(code, stderr) -> CommandFailed(code, stderr)
    shell.NotFound(cmd) -> CommandFailed(127, "Command not found: " <> cmd)
  }
}

// Ensure worktree exists, creating if necessary
pub fn ensure(bead_id: String, config: Config) -> Result(String, WorktreeError) {
  let path = worktree_path(bead_id, config)

  case exists(path) {
    True -> Ok(path)
    False -> create(bead_id, path, config)
  }
}

// Create a new worktree
pub fn create(
  bead_id: String,
  path: String,
  config: Config,
) -> Result(String, WorktreeError) {
  let branch_name = config.git.branch_prefix <> bead_id
  let base_branch = config.git.base_branch

  // Create worktree with new branch from base
  let args = ["worktree", "add", "-b", branch_name, path, base_branch]
  case shell.run("git", args, ".") {
    Ok(_) -> {
      // Push branch if configured (non-fatal - worktree creation succeeded)
      case config.git.push_branch_on_create, config.git.push_enabled {
        True, True -> {
          let push_args = ["push", "-u", config.git.remote, branch_name]
          case shell.run("git", push_args, path) {
            Ok(_) -> Nil
            Error(_) -> Nil  // Push failure is non-fatal - worktree was created
          }
        }
        _, _ -> Nil
      }
      Ok(path)
    }
    Error(shell.CommandError(code, stderr)) -> {
      // Check if branch already exists
      case string.contains(stderr, "already exists") {
        True -> {
          // Try to add worktree for existing branch
          let args2 = ["worktree", "add", path, branch_name]
          case shell.run("git", args2, ".") {
            Ok(_) -> Ok(path)
            Error(e) -> Error(shell_to_worktree_error(e))
          }
        }
        False -> Error(CommandFailed(code, stderr))
      }
    }
    Error(e) -> Error(shell_to_worktree_error(e))
  }
}

// Delete a worktree
pub fn delete(path: String) -> Result(Nil, WorktreeError) {
  case shell.run("git", ["worktree", "remove", "--force", path], ".") {
    Ok(_) -> Ok(Nil)
    Error(e) -> Error(shell_to_worktree_error(e))
  }
}

// Check if worktree exists
pub fn exists(path: String) -> Bool {
  case shell.run("test", ["-d", path], ".") {
    Ok(_) -> True
    Error(_) -> False
  }
}

// List all worktrees
pub fn list() -> Result(List(String), WorktreeError) {
  case shell.run("git", ["worktree", "list", "--porcelain"], ".") {
    Ok(output) -> {
      let paths =
        output
        |> string.split("\n")
        |> list.filter_map(fn(line) {
          case string.starts_with(line, "worktree ") {
            True -> Ok(string.drop_start(line, 9))
            False -> Error(Nil)
          }
        })
      Ok(paths)
    }
    Error(e) -> Error(shell_to_worktree_error(e))
  }
}

// Get the worktree path for a bead
pub fn worktree_path(bead_id: String, config: Config) -> String {
  let template = config.worktree.path_template

  // Replace placeholders
  template
  |> string.replace("{bead-id}", bead_id)
  |> string.replace("{project}", get_project_name())
}

// Get project name from current directory
fn get_project_name() -> String {
  case shell.run("basename", ["$(pwd)"], ".") {
    Ok(name) -> string.trim(name)
    Error(_) -> "project"
  }
}
