#!/usr/bin/env bash

set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
harness_dir="$(mktemp -d "${TMPDIR:-/tmp}/azedarach-jaeger-termination.XXXXXX")"
completion_fifo="$harness_dir/complete"
parent_ready_fifo="$harness_dir/parent-ready"
parent_continue_fifo="$harness_dir/parent-continue"
output="$harness_dir/output"
validator_pid=""

cleanup_test() {
  trap - ERR EXIT
  set +e
  if [[ -n "$validator_pid" ]]; then
    kill "$validator_pid" 2>/dev/null || true
    wait "$validator_pid" 2>/dev/null || true
  fi
  rm -f "$completion_fifo" "$parent_ready_fifo" "$parent_continue_fifo" "$output"
  rmdir "$harness_dir" 2>/dev/null || true
}
trap cleanup_test EXIT

mkfifo "$completion_fifo" "$parent_ready_fifo" "$parent_continue_fifo"
AZEDARACH_JAEGER_TEST_PARENT_OOM_READY_FIFO="$parent_ready_fifo" \
  AZEDARACH_JAEGER_TEST_PARENT_OOM_CONTINUE_FIFO="$parent_continue_fifo" \
  AZEDARACH_JAEGER_TEST_TERMINATION_COMPLETE_FIFO="$completion_fifo" \
  "$repo_root/scripts/test-jaeger-local-concurrent.sh" >"$output" 2>&1 &
validator_pid=$!

IFS= read -r parent_ready <"$parent_ready_fifo"
IFS='|' read -r ready_kind parent_pid validator_one_pid validator_two_pid \
  validator_one_root validator_two_root validator_harness <<<"$parent_ready"
[[ "$ready_kind" == "ready" && "$parent_pid" == "$validator_pid" ]]
kill -TERM "$validator_pid"
IFS= read -r completion <"$completion_fifo"
[[ "$completion" == "complete" ]]
if wait "$validator_pid" 2>/dev/null; then
  validator_status=0
else
  validator_status=$?
fi
validator_pid=""
if [[ "$validator_status" != "143" ]]; then
  echo "termination regression exited $validator_status, want SIGTERM status 143" >&2
  sed 's/^/termination: /' "$output" >&2
  exit 1
fi
if kill -0 "$validator_one_pid" 2>/dev/null || kill -0 "$validator_two_pid" 2>/dev/null; then
  echo "termination regression left a validator process alive" >&2
  exit 1
fi
if [[ -e "$validator_one_root" || -e "$validator_two_root" || -e "$validator_harness" ]]; then
  echo "termination regression left a fixture root, lock, or FIFO harness" >&2
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

run_failure_regression AZEDARACH_JAEGER_TEST_INJECT_RESOURCE_COLLISION 1 \
  'concurrent OOM validators shared a fixture, lifecycle lock, or endpoint resource'
collision_one_root="$(sed -n 's/^one resources: \([^|]*\)|.*/\1/p' "$output")"
collision_two_root="$(sed -n 's/^two resources: \([^|]*\)|.*/\1/p' "$output")"
[[ -n "$collision_one_root" && -n "$collision_two_root" ]]
if [[ -e "$collision_one_root" || -e "$collision_two_root" ]]; then
  echo "collision regression left an owned fixture root" >&2
  exit 1
fi

echo "concurrent Jaeger validator terminal failure cleanup passed"
