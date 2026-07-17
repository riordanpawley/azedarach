#!/usr/bin/env bash

set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
harness_dir="$(mktemp -d "${TMPDIR:-/tmp}/azedarach-jaeger-termination.XXXXXX")"
completion_fifo="$harness_dir/complete"
output="$harness_dir/output"
validator_pid=""

cleanup_test() {
  trap - ERR EXIT
  set +e
  if [[ -n "$validator_pid" ]]; then
    kill "$validator_pid" 2>/dev/null || true
    wait "$validator_pid" 2>/dev/null || true
  fi
  rm -f "$completion_fifo" "$output"
  rmdir "$harness_dir" 2>/dev/null || true
}
trap cleanup_test EXIT

mkfifo "$completion_fifo"
AZEDARACH_JAEGER_TEST_TERMINATE_AT_OOM=1 \
  AZEDARACH_JAEGER_TEST_TERMINATION_COMPLETE_FIFO="$completion_fifo" \
  "$repo_root/scripts/test-jaeger-local-concurrent.sh" >"$output" 2>&1 &
validator_pid=$!

IFS= read -r completion <"$completion_fifo"
[[ "$completion" == "complete" ]]
set +e
wait "$validator_pid"
validator_status=$?
set -e
validator_pid=""
if [[ "$validator_status" != "75" ]]; then
  echo "termination regression exited $validator_status, want 75" >&2
  sed 's/^/termination: /' "$output" >&2
  exit 1
fi

echo "concurrent Jaeger validator termination cleanup passed"

run_failure_regression() {
  local injection="$1" value="$2" expected="$3" validator_status
  env "$injection=$value" \
    AZEDARACH_JAEGER_TEST_TERMINATION_COMPLETE_FIFO="$completion_fifo" \
    "$repo_root/scripts/test-jaeger-local-concurrent.sh" >"$output" 2>&1 &
  validator_pid=$!
  IFS= read -r completion <"$completion_fifo"
  [[ "$completion" == "complete" ]]
  set +e
  wait "$validator_pid"
  validator_status=$?
  set -e
  validator_pid=""
  if [[ "$validator_status" != "1" ]]; then
    echo "$injection regression exited $validator_status, want 1" >&2
    sed 's/^/failure: /' "$output" >&2
    exit 1
  fi
  grep -q "$expected" "$output"
  grep -q 'concurrent Jaeger validator one output:' "$output"
  if grep -q 'left an owned fixture root' "$output"; then
    echo "$injection regression leaked a fixture root" >&2
    sed 's/^/failure: /' "$output" >&2
    exit 1
  fi
}

run_failure_regression AZEDARACH_JAEGER_TEST_VALIDATOR_ONE_PERL /missing \
  'Perl with Fcntl flock support is required'
run_failure_regression AZEDARACH_JAEGER_TEST_VALIDATOR_ONE_FAIL_BEFORE_WORKER_INIT 1 \
  'deliberate validator failure before worker initialization'

echo "concurrent Jaeger validator terminal failure cleanup passed"
