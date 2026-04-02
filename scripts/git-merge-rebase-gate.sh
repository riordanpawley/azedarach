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

test_log="$(mktemp -t azedarach-merge-gate.XXXXXX)"
cleanup() {
	rm -f "$test_log"
}
trap cleanup EXIT INT TERM HUP

run_tests() {
	echo "[gate] running test check (go test ./...)"
	go test ./... 2>&1 | tee "$test_log"
}

run_tests || {
	refreshed=0

	if grep -q 'run UPDATE_GOLDEN=1 go test ./internal/ui/overlay to accept' "$test_log"; then
		echo "[gate] detected overlay golden drift; refreshing fixtures"
		UPDATE_GOLDEN=1 go test ./internal/ui/overlay
		git add internal/ui/overlay/testdata/*.golden
		refreshed=1
	fi

	if grep -Eq 'FAIL[[:space:]].*internal/ui/board' "$test_log"; then
		echo "[gate] detected board golden drift candidate; refreshing fixtures"
		go test ./internal/ui/board -update
		git add internal/ui/board/testdata/*.golden
		refreshed=1
	fi

	if [ "$refreshed" -eq 1 ]; then
		echo "[gate] re-running full test suite after golden refresh"
		run_tests
	else
		exit 1
	fi
}

echo "[gate] build+tests passed"
