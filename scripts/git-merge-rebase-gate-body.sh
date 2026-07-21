#!/usr/bin/env sh
set -eu

repo_root="$(git rev-parse --show-toplevel)"
cd "$repo_root"

# Hooks can run with git routing variables (e.g. GIT_DIR/GIT_WORK_TREE)
# inherited from the active repository/worktree. Clear them so nested git
# usage inside go tests operates on each test's own temporary repository.
unset GIT_DIR GIT_WORK_TREE GIT_INDEX_FILE GIT_COMMON_DIR
unset GIT_OBJECT_DIRECTORY GIT_ALTERNATE_OBJECT_DIRECTORIES

# These values authorize and route only the outer candidate wrapper. Letting
# them reach the nested cold suite changes the behavior of tests that execute
# their own merge gates against temporary repositories.
unset AZEDARACH_CANDIDATE_HEAD AZEDARACH_MERGE_GATE_BODY
unset AZEDARACH_SKIP_MERGE_REBASE_GATE

echo "[gate] running canonical build, cold semantic suite, and boundary gates for candidate $(git rev-parse --verify HEAD)"
if [ -f "$repo_root/justfile" ] && command -v just >/dev/null 2>&1; then
	just _merge-gate-unleased
else
	# The trusted outer gate already owns aggregate admission. Keep this body
	# independently runnable beneath that lease when `just` is unavailable.
	go build ./...
	go test -timeout 8m ./...
fi

echo "[gate] merge gates passed"
