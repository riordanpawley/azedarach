# Merge Conflict UX

## Overview

When a worktree branch is behind main, Azedarach provides a streamlined flow to update and handle conflicts.

## Trigger Points

Merge conflict UX appears when:

1. **Attaching to session** (`Space+a`) - when branch is behind main
2. **Updating from main** (`Space+u`) - explicit update

## Flow Diagram

```
┌─────────────────────────────────────────────────────────────────────────────┐
│                         MERGE CONFLICT FLOW                                  │
└─────────────────────────────────────────────────────────────────────────────┘

User triggers attach (Space+a) or update (Space+u)
                    │
                    ▼
        ┌───────────────────────┐
        │ Check branch status   │
        │ git rev-list --count  │
        └───────────────────────┘
                    │
          ┌─────────┴─────────┐
          │                   │
     behind = 0          behind > 0
          │                   │
          ▼                   ▼
    ┌───────────┐    ┌─────────────────────────┐
    │  Direct   │    │   Show MergeChoice      │
    │  attach   │    │   overlay               │
    └───────────┘    └─────────────────────────┘
                              │
                    ┌─────────┼─────────┐
                    │         │         │
                  'm'       's'       Esc
                    │         │         │
                    ▼         ▼         ▼
            ┌───────────┐ ┌───────┐ ┌───────┐
            │   Merge   │ │ Skip  │ │Cancel │
            │   Flow    │ │attach │ │       │
            └───────────┘ └───────┘ └───────┘
                    │
                    ▼
        ┌───────────────────────┐
        │ Check for conflicts   │
        │ git merge-tree        │
        │ (safe, in-memory)     │
        └───────────────────────┘
                    │
          ┌─────────┴─────────┐
          │                   │
     exit 0 (clean)      exit !0 (conflicts)
          │                   │
          ▼                   ▼
    ┌───────────┐    ┌─────────────────────────┐
    │   Merge   │    │   Start merge           │
    │   clean   │    │   (creates markers)     │
    │           │    │                         │
    │ git merge │    │   Spawn Claude in       │
    │ --no-edit │    │   "merge" window with   │
    │           │    │   resolve prompt        │
    └───────────┘    └─────────────────────────┘
          │                   │
          ▼                   ▼
    ┌───────────┐    ┌─────────────────────────┐
    │  Attach   │    │   Toast: "Conflicts     │
    │  to       │    │   detected. Claude      │
    │  session  │    │   started to resolve."  │
    └───────────┘    │                         │
                     │   User resolves via     │
                     │   Claude, then retries  │
                     └─────────────────────────┘
```

## MergeChoice Overlay

```
┌─────────────────────────────────────────────────────────┐
│ ↓ Branch Behind main                                    │
├─────────────────────────────────────────────────────────┤
│                                                         │
│ 5 commits behind                                        │
│                                                         │
│ Merge main into your branch before attaching?           │
│                                                         │
│ m: Merge & Attach  (pull latest main into branch)       │
│ s: Skip & Attach   (attach without merging)             │
│ Esc: Cancel                                             │
│                                                         │
└─────────────────────────────────────────────────────────┘
```

### Keys

| Key | Action |
|-----|--------|
| m | Merge main into branch, then attach (or show conflicts) |
| s | Skip merge, attach directly |
| Esc | Cancel, return to board |

## Conflict Detection

Uses `git merge-tree` for **safe, in-memory** conflict detection:

```bash
git merge-tree --write-tree main HEAD
```

- Exit 0: No conflicts, safe to merge
- Exit !0: Conflicts detected

This does NOT modify the working tree. It's a read-only check.

### Excluded Files

`.beads/` conflicts are **filtered out** - these are handled separately by `bd sync`.

```gleam
conflicting_files
|> list.filter(fn(f) { !string.starts_with(f, ".beads/") })
```

## Conflict Resolution Flow

When conflicts detected:

1. **Start the merge** (creates conflict markers in files):
   ```bash
   git merge main -m "Merge main into {branch}"
   ```

2. **Create "merge" window** in tmux session:
   ```bash
   tmux new-window -t {session} -n merge
   ```

3. **Start Claude with resolve prompt**:
   ```
   There are merge conflicts in: src/login.ts, src/auth.ts.
   Please resolve these conflicts, then stage and commit the resolution.
   ```

4. **Toast notification**:
   ```
   Conflicts detected in: src/login.ts, src/auth.ts.
   Claude started in 'merge' window to resolve.
   Retry attach after resolution.
   ```

5. **User retries** after Claude resolves

## Implementation (Gleam)

### Check Branch Status

```gleam
pub fn check_branch_behind_main(
  worktree_path: String,
  base_branch: String,
) -> Result(BranchStatus, Error) {
  // Get commits behind
  let behind = run_git(
    ["rev-list", "--count", "HEAD.." <> base_branch],
    worktree_path,
  )
  |> result.map(string.trim)
  |> result.map(int.parse)
  |> result.flatten()
  |> result.unwrap(0)

  // Get commits ahead
  let ahead = run_git(
    ["rev-list", "--count", base_branch <> "..HEAD"],
    worktree_path,
  )
  |> result.map(string.trim)
  |> result.map(int.parse)
  |> result.flatten()
  |> result.unwrap(0)

  Ok(BranchStatus(behind: behind, ahead: ahead))
}
```

### Conflict Detection

```gleam
pub fn check_merge_conflicts(
  worktree_path: String,
  base_branch: String,
) -> Result(MergeResult, Error) {
  let exit_code = shell.run_exit_code(
    "git",
    ["merge-tree", "--write-tree", base_branch, "HEAD"],
    worktree_path,
  )

  case exit_code {
    0 -> Ok(MergeResult(has_conflicts: False, files: []))
    _ -> {
      // Get conflicting files
      let output = run_git(
        ["merge-tree", "--write-tree", "--name-only", "--no-messages", base_branch, "HEAD"],
        worktree_path,
      )
      |> result.unwrap("")

      let files = output
        |> string.split("\n")
        |> list.drop(1)  // First line is tree hash
        |> list.filter(fn(f) { f != "" })
        |> list.filter(fn(f) { !string.starts_with(f, ".beads/") })

      Ok(MergeResult(has_conflicts: list.length(files) > 0, files: files))
    }
  }
}
```

### Merge with Conflict Handling

```gleam
pub fn merge_main_into_branch(
  bead_id: String,
  worktree_path: String,
  base_branch: String,
) -> Result(Nil, MergeError) {
  // Check for conflicts first
  let merge_result = check_merge_conflicts(worktree_path, base_branch)

  case merge_result {
    Ok(MergeResult(has_conflicts: False, ..)) -> {
      // Clean merge
      run_git(["merge", base_branch, "--no-edit"], worktree_path)
      Ok(Nil)
    }

    Ok(MergeResult(has_conflicts: True, files: files)) -> {
      // Start merge (creates conflict markers)
      run_git(
        ["merge", base_branch, "-m", "Merge " <> base_branch <> " into " <> bead_id],
        worktree_path,
      )
      |> result.unwrap(Nil)  // Will fail, expected

      // Build resolve prompt
      let file_list = string.join(files, ", ")
      let prompt = "There are merge conflicts in: " <> file_list <> ". Please resolve these conflicts, then stage and commit the resolution."

      // Spawn Claude in merge window
      tmux.new_window(bead_id <> "-az", "merge")
      tmux.send_keys(bead_id <> "-az:merge", "claude \"" <> prompt <> "\"", True)

      // Return error with info
      Error(MergeConflictError(
        bead_id: bead_id,
        files: files,
        message: "Conflicts detected. Claude started in 'merge' window.",
      ))
    }

    Error(e) -> Error(GitError(e))
  }
}
```

## Window Management

When conflicts spawn Claude in "merge" window:

```
Session: az-123-az
├── Window: main      → Original Claude (may be paused)
├── Window: dev-web   → Dev server
├── Window: merge     → Claude resolving conflicts  ← NEW
```

After resolution:
- User can kill merge window or let it complete
- Retry attach will succeed (branch now clean)
