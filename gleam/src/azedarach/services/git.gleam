// Git service - git operations

import gleam/int
import gleam/list
import gleam/string
import azedarach/config.{type Config}
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

// Helper to convert shell errors to git errors
fn shell_to_git_error(err: shell.ShellError) -> GitError {
  case err {
    shell.CommandError(code, stderr) -> CommandFailed(code, stderr)
    shell.NotFound(cmd) -> CommandFailed(127, "Command not found: " <> cmd)
  }
}

// Check how many commits behind main
pub fn commits_behind_main(worktree: String, config: Config) -> Result(Int, GitError) {
  let base = config.git.base_branch
  let args = ["rev-list", "--count", "HEAD.." <> base]
  case shell.run("git", args, worktree) {
    Ok(output) -> {
      case int.parse(string.trim(output)) {
        Ok(n) -> Ok(n)
        Error(_) -> Ok(0)
      }
    }
    Error(e) -> Error(shell_to_git_error(e))
  }
}

// Check how many commits ahead of main
pub fn commits_ahead_main(worktree: String, config: Config) -> Result(Int, GitError) {
  let base = config.git.base_branch
  let args = ["rev-list", "--count", base <> "..HEAD"]
  case shell.run("git", args, worktree) {
    Ok(output) -> {
      case int.parse(string.trim(output)) {
        Ok(n) -> Ok(n)
        Error(_) -> Ok(0)
      }
    }
    Error(e) -> Error(shell_to_git_error(e))
  }
}

// Check for merge conflicts (dry run)
pub fn check_merge_conflicts(
  worktree: String,
  config: Config,
) -> Result(List(String), GitError) {
  let base = config.git.base_branch
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
        Error(e) -> Error(shell_to_git_error(e))
      }
    }
    Ok(files) -> {
      // Conflicts detected - start merge anyway (creates conflict markers)
      let base = config.git.base_branch
      let _ = shell.run("git", ["merge", base, "-m", "Merge " <> base], worktree)
      Error(MergeConflict(files))
    }
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
              let _ = shell.run("git", ["checkout", branch_name], worktree)
              Ok(Nil)
            }
            Error(e) -> {
              // Switch back even on error
              let _ = shell.run("git", ["checkout", branch_name], worktree)
              Error(shell_to_git_error(e))
            }
          }
        }
        Error(e) -> Error(shell_to_git_error(e))
      }
    }
    Error(e) -> Error(shell_to_git_error(e))
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
            Error(e) -> Error(shell_to_git_error(e))
          }
        }
      }
    }
    Error(e) -> Error(shell_to_git_error(e))
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
      let _ = shell.run("git", ["push", "-u", config.git.remote, branch], worktree)
      Nil
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
    Error(e) -> Error(shell_to_git_error(e))
  }
}

// Delete branch (local and remote)
pub fn delete_branch(bead_id: String, config: Config) -> Result(Nil, GitError) {
  let branch = config.git.branch_prefix <> bead_id

  // Delete local
  let _ = shell.run("git", ["branch", "-D", branch], ".")

  // Delete remote if push enabled
  case config.git.push_enabled {
    True -> {
      let _ = shell.run("git", ["push", config.git.remote, "--delete", branch], ".")
      Nil
    }
    False -> Nil
  }

  Ok(Nil)
}

// Get diff (for viewing)
pub fn diff(worktree: String, config: Config) -> Result(String, GitError) {
  let base = config.git.base_branch
  case shell.run("git", ["diff", base <> "...HEAD"], worktree) {
    Ok(output) -> Ok(output)
    Error(e) -> Error(shell_to_git_error(e))
  }
}

// Fetch from remote
pub fn fetch(config: Config) -> Result(Nil, GitError) {
  case config.git.fetch_enabled {
    True -> {
      case shell.run("git", ["fetch", config.git.remote], ".") {
        Ok(_) -> Ok(Nil)
        Error(e) -> Error(shell_to_git_error(e))
      }
    }
    False -> Ok(Nil)
  }
}
