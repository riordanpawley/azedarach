// Git service - git operations

import gleam/int
import gleam/list
import gleam/result
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
  use output <- result.try(
    shell.run("git", args, worktree)
    |> result.map_error(shell_to_git_error),
  )
  int.parse(string.trim(output))
  |> result.unwrap(0)
  |> Ok
}

// Check how many commits ahead of main
pub fn commits_ahead_main(worktree: String, config: Config) -> Result(Int, GitError) {
  let base = config.git.base_branch
  let args = ["rev-list", "--count", base <> "..HEAD"]
  use output <- result.try(
    shell.run("git", args, worktree)
    |> result.map_error(shell_to_git_error),
  )
  int.parse(string.trim(output))
  |> result.unwrap(0)
  |> Ok
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
      // We intentionally ignore the result here because we expect it to "fail"
      // with conflicts - that's the point
      let base = config.git.base_branch
      case shell.run("git", ["merge", base, "-m", "Merge " <> base], worktree) {
        Ok(_) -> Nil
        Error(_) -> Nil  // Expected - merge will exit non-zero due to conflicts
      }
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
              // Switch back - must succeed or we're in a bad state
              case shell.run("git", ["checkout", branch_name], worktree) {
                Ok(_) -> Ok(Nil)
                Error(e) -> Error(shell_to_git_error(e))
              }
            }
            Error(e) -> {
              // Switch back even on error - best effort
              case shell.run("git", ["checkout", branch_name], worktree) {
                Ok(_) -> Nil
                Error(_) -> Nil  // Already have an error to report
              }
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
  use _ <- result.try(
    shell.run("git", ["add", "-A"], worktree)
    |> result.map_error(shell_to_git_error),
  )

  // Check if there's anything to commit (exit 0 = no changes)
  case shell.run("git", ["diff", "--cached", "--quiet"], worktree) {
    Ok(_) -> Error(NothingToCommit)
    Error(_) -> {
      // There are changes, commit
      shell.run("git", ["commit", "-m", "wip: paused session"], worktree)
      |> result.map_error(shell_to_git_error)
      |> result.replace(Nil)
    }
  }
}

// Create PR using gh CLI
pub fn create_pr(
  worktree: String,
  bead_id: String,
  config: Config,
) -> Result(String, GitError) {
  // Ensure changes are pushed
  use _ <- result.try(case config.git.push_enabled {
    True -> {
      let branch = config.git.branch_prefix <> bead_id
      shell.run("git", ["push", "-u", config.git.remote, branch], worktree)
      |> result.map_error(shell_to_git_error)
    }
    False -> Ok("")
  })

  // Create PR
  let draft_flag = case config.pr.auto_draft {
    True -> ["--draft"]
    False -> []
  }
  let args = ["pr", "create", "--fill"] |> list.append(draft_flag)

  shell.run("gh", args, worktree)
  |> result.map_error(shell_to_git_error)
  |> result.map(string.trim)
}

// Delete branch (local and remote)
pub fn delete_branch(bead_id: String, config: Config) -> Result(Nil, GitError) {
  let branch = config.git.branch_prefix <> bead_id

  // Delete local - may fail if branch doesn't exist
  case shell.run("git", ["branch", "-D", branch], ".") {
    Ok(_) -> Nil
    Error(_) -> Nil  // Branch might not exist locally
  }

  // Delete remote if push enabled
  case config.git.push_enabled {
    True -> {
      case shell.run("git", ["push", config.git.remote, "--delete", branch], ".") {
        Ok(_) -> Nil
        Error(_) -> Nil  // Branch might not exist remotely
      }
    }
    False -> Nil
  }

  Ok(Nil)
}

// Get diff (for viewing)
pub fn diff(worktree: String, config: Config) -> Result(String, GitError) {
  let base = config.git.base_branch
  shell.run("git", ["diff", base <> "...HEAD"], worktree)
  |> result.map_error(shell_to_git_error)
}

// Fetch from remote
pub fn fetch(config: Config) -> Result(Nil, GitError) {
  case config.git.fetch_enabled {
    True ->
      shell.run("git", ["fetch", config.git.remote], ".")
      |> result.map_error(shell_to_git_error)
      |> result.replace(Nil)
    False -> Ok(Nil)
  }
}
