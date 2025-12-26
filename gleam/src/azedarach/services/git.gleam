// Git service - git operations

import gleam/int
import gleam/list
import gleam/result
import gleam/string
import azedarach/config.{type Config, Local, Origin}
import azedarach/util/shell

pub type GitError {
  CommandFailed(exit_code: Int, stderr: String)
  MergeConflict(files: List(String))
  NothingToCommit
  NotARepository
}

pub fn error_to_string(err: GitError) -> String {
  case err {
    CommandFailed(code, stderr) ->
      "git failed (" <> int.to_string(code) <> "): " <> stderr
    MergeConflict(files) ->
      "Merge conflict in: " <> string.join(files, ", ")
    NothingToCommit -> "Nothing to commit"
    NotARepository -> "Not a git repository"
  }
}

/// Get the comparison base ref based on workflow mode.
/// Local mode: compare against local base branch (e.g., "main")
/// Origin mode: compare against remote tracking branch (e.g., "origin/main")
pub fn comparison_base(config: Config) -> String {
  let base = config.git.base_branch
  case config.git.workflow_mode {
    Local -> base
    Origin -> config.git.remote <> "/" <> base
  }
}

// Check how many commits behind base (uses origin/main in Origin mode)
pub fn commits_behind_main(worktree: String, config: Config) -> Result(Int, GitError) {
  let base = comparison_base(config)
  let args = ["rev-list", "--count", "HEAD.." <> base]
  case shell.run("git", args, worktree) {
    Ok(output) -> {
      case int.parse(string.trim(output)) {
        Ok(n) -> Ok(n)
        Error(_) -> Ok(0)
      }
    }
    Error(shell.CommandError(code, stderr)) -> Error(CommandFailed(code, stderr))
  }
}

// Check how many commits ahead of base (uses origin/main in Origin mode)
pub fn commits_ahead_main(worktree: String, config: Config) -> Result(Int, GitError) {
  let base = comparison_base(config)
  let args = ["rev-list", "--count", base <> "..HEAD"]
  case shell.run("git", args, worktree) {
    Ok(output) -> {
      case int.parse(string.trim(output)) {
        Ok(n) -> Ok(n)
        Error(_) -> Ok(0)
      }
    }
    Error(shell.CommandError(code, stderr)) -> Error(CommandFailed(code, stderr))
  }
}

// Check for merge conflicts (dry run) - compares against comparison base
pub fn check_merge_conflicts(
  worktree: String,
  config: Config,
) -> Result(List(String), GitError) {
  let base = comparison_base(config)
  let args = ["merge-tree", "--write-tree", base, "HEAD"]
  case shell.run_exit_code("git", args, worktree) {
    #(0, _) -> Ok([])
    // No conflicts
    #(_, output) -> {
      // Parse conflicting files
      let files =
        output
        |> string.split("\n")
        |> list.drop(1)
        // First line is tree hash
        |> list.filter(fn(f) { f != "" })
        |> list.filter(fn(f) { !string.starts_with(f, ".beads/") })
      Ok(files)
    }
  }
}

// Merge main into current branch
pub fn merge_main(worktree: String, config: Config) -> Result(Nil, GitError) {
  // First check for conflicts
  case check_merge_conflicts(worktree, config) {
    Ok([]) -> {
      // No conflicts, safe to merge
      let base = config.git.base_branch
      case shell.run("git", ["merge", base, "--no-edit"], worktree) {
        Ok(_) -> Ok(Nil)
        Error(shell.CommandError(code, stderr)) -> Error(CommandFailed(code, stderr))
      }
    }
    Ok(files) if list.length(files) > 0 -> {
      // Conflicts detected - start merge anyway (creates conflict markers)
      let base = config.git.base_branch
      shell.run("git", ["merge", base, "-m", "Merge " <> base], worktree)
      Error(MergeConflict(files))
    }
    Ok(_) -> Ok(Nil)
    Error(e) -> Error(e)
  }
}

// Merge current branch to main (in worktree)
pub fn merge_to_main(worktree: String, config: Config) -> Result(Nil, GitError) {
  let base = config.git.base_branch

  // Get current branch name
  case shell.run("git", ["branch", "--show-current"], worktree) {
    Ok(branch) -> {
      let branch_name = string.trim(branch)

      // Switch to main, merge, switch back
      case shell.run("git", ["checkout", base], worktree) {
        Ok(_) -> {
          case shell.run("git", ["merge", branch_name, "--no-edit"], worktree) {
            Ok(_) -> {
              // Switch back
              shell.run("git", ["checkout", branch_name], worktree)
              Ok(Nil)
            }
            Error(shell.CommandError(code, stderr)) -> {
              // Switch back even on error
              shell.run("git", ["checkout", branch_name], worktree)
              Error(CommandFailed(code, stderr))
            }
          }
        }
        Error(shell.CommandError(code, stderr)) -> Error(CommandFailed(code, stderr))
      }
    }
    Error(shell.CommandError(code, stderr)) -> Error(CommandFailed(code, stderr))
  }
}

// Create WIP commit
pub fn wip_commit(worktree: String) -> Result(Nil, GitError) {
  // Stage all changes
  case shell.run("git", ["add", "-A"], worktree) {
    Ok(_) -> {
      // Check if there's anything to commit
      case shell.run("git", ["diff", "--cached", "--quiet"], worktree) {
        Ok(_) -> Error(NothingToCommit)
        // Exit 0 means no changes
        Error(_) -> {
          // There are changes, commit
          case shell.run("git", ["commit", "-m", "wip: paused session"], worktree) {
            Ok(_) -> Ok(Nil)
            Error(shell.CommandError(code, stderr)) -> Error(CommandFailed(code, stderr))
          }
        }
      }
    }
    Error(shell.CommandError(code, stderr)) -> Error(CommandFailed(code, stderr))
  }
}

// Create PR using gh CLI
pub fn create_pr(
  worktree: String,
  bead_id: String,
  config: Config,
) -> Result(String, GitError) {
  // Ensure changes are pushed
  case config.git.push_enabled {
    True -> {
      let branch = config.git.branch_prefix <> bead_id
      shell.run("git", ["push", "-u", config.git.remote, branch], worktree)
      |> result.unwrap(Nil)
    }
    False -> Nil
  }

  // Create PR
  let draft_flag = case config.pr.auto_draft {
    True -> ["--draft"]
    False -> []
  }

  let args =
    ["pr", "create", "--fill"]
    |> list.append(draft_flag)

  case shell.run("gh", args, worktree) {
    Ok(output) -> Ok(string.trim(output))
    Error(shell.CommandError(code, stderr)) -> Error(CommandFailed(code, stderr))
  }
}

// Delete branch (local and remote)
pub fn delete_branch(
  bead_id: String,
  config: Config,
  project_path: String,
) -> Result(Nil, GitError) {
  let branch = config.git.branch_prefix <> bead_id

  // Delete local
  shell.run("git", ["branch", "-D", branch], project_path)
  |> result.unwrap(Nil)

  // Delete remote if push enabled
  case config.git.push_enabled {
    True -> {
      shell.run("git", ["push", config.git.remote, "--delete", branch], project_path)
      |> result.unwrap(Nil)
    }
    False -> Nil
  }

  Ok(Nil)
}

// Get diff (for viewing) - uses origin/main in Origin mode
pub fn diff(worktree: String, config: Config) -> Result(String, GitError) {
  let base = comparison_base(config)
  case shell.run("git", ["diff", base <> "...HEAD"], worktree) {
    Ok(output) -> Ok(output)
    Error(shell.CommandError(code, stderr)) -> Error(CommandFailed(code, stderr))
  }
}

// Fetch from remote
pub fn fetch(config: Config, project_path: String) -> Result(Nil, GitError) {
  case config.git.fetch_enabled {
    True -> {
      case shell.run("git", ["fetch", config.git.remote], project_path) {
        Ok(_) -> Ok(Nil)
        Error(shell.CommandError(code, stderr)) -> Error(CommandFailed(code, stderr))
      }
    }
    False -> Ok(Nil)
  }
}
