#!/usr/bin/env sh
set -eu

if [ "${AZEDARACH_SKIP_MERGE_REBASE_GATE:-0}" = "1" ]; then
  echo "merge/rebase gate skipped (AZEDARACH_SKIP_MERGE_REBASE_GATE=1)"
  exit 0
fi

repo_root="$(git rev-parse --show-toplevel)"
cd "$repo_root"

# Hooks can run with git routing variables (e.g. GIT_DIR/GIT_WORK_TREE)
# inherited from the active repository/worktree. Clear them so nested git
# usage inside go tests operates on each test's own temporary repository.
unset GIT_DIR GIT_WORK_TREE GIT_INDEX_FILE GIT_COMMON_DIR
unset GIT_OBJECT_DIRECTORY GIT_ALTERNATE_OBJECT_DIRECTORIES

echo "[gate] running build check (go build ./...)"
go build ./...

echo "[gate] running test check (go test ./...)"
go test ./...

echo "[gate] build+tests passed"
