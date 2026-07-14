#!/usr/bin/env sh
set -eu

repo_root="$(git rev-parse --show-toplevel)"
cd "$repo_root"

# Hooks can run with git routing variables (e.g. GIT_DIR/GIT_WORK_TREE)
# inherited from the active repository/worktree. Clear them so nested git
# usage inside go tests operates on each test's own temporary repository.
unset GIT_DIR GIT_WORK_TREE GIT_INDEX_FILE GIT_COMMON_DIR
unset GIT_OBJECT_DIRECTORY GIT_ALTERNATE_OBJECT_DIRECTORIES

echo "[gate] running canonical build, cold semantic suite, and boundary gates"
if [ -f "$repo_root/justfile" ] && command -v just >/dev/null 2>&1; then
	just merge-gate
else
	# Keep the body independently runnable for timeout-contract tests and
	# recovery environments that have Go but not the repository task runner.
	# The canonical cold profile uses the same 8m test-binary timeout.
	go build ./...
	go test -timeout 8m ./...
fi

echo "[gate] merge gates passed"
