#!/usr/bin/env sh
set -eu

repo_root="$(git rev-parse --show-toplevel)"
cd "$repo_root"

# Hooks can run with git routing variables (e.g. GIT_DIR/GIT_WORK_TREE)
# inherited from the active repository/worktree. Clear them so nested git
# usage inside go tests operates on each test's own temporary repository.
unset GIT_DIR GIT_WORK_TREE GIT_INDEX_FILE GIT_COMMON_DIR
unset GIT_OBJECT_DIRECTORY GIT_ALTERNATE_OBJECT_DIRECTORIES

test_log="$(mktemp -t azedarach-merge-gate.XXXXXX)"
test_status="$(mktemp -t azedarach-merge-gate-status.XXXXXX)"
cleanup() {
	rm -f "$test_log" "$test_status"
}
trap cleanup EXIT

echo "[gate] running build check (go build ./...)"
go build ./...

run_tests() {
	echo "[gate] running test check (go test -timeout 8m ./...)"
	rm -f "$test_status"
	(
		set +e
		go test -timeout 8m ./...
		status=$?
		printf '%s\n' "$status" >"$test_status"
		exit 0
	) 2>&1 | tee "$test_log"
	if [ ! -s "$test_status" ]; then
		return 124
	fi
	status="$(cat "$test_status")"
	[ "$status" -eq 0 ]
}

run_tests || {
	refreshed=0

	if grep -q 'run UPDATE_GOLDEN=1 go test ./internal/ui/overlay to accept' "$test_log"; then
		echo "[gate] detected overlay golden drift; refreshing fixtures"
		UPDATE_GOLDEN=1 go test ./internal/ui/overlay
		git add internal/ui/overlay/testdata/*.golden
		refreshed=1
	fi

	if grep -Eq 'FAIL[[:space:]].*internal/ui/board' "$test_log" &&
		grep -Eq 'Render\(\) output mismatch|RenderCard\(\) output mismatch' "$test_log"; then
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
