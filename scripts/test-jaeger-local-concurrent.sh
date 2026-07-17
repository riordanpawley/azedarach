#!/usr/bin/env bash

set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
harness_dir="$(mktemp -d "${TMPDIR:-/tmp}/azedarach-jaeger-concurrent.XXXXXX")"
validator_one_pid=""
validator_two_pid=""
concurrent_phase="startup"

dump_validator_logs() {
  if [[ -f "$harness_dir/one.log" ]]; then
    echo "concurrent Jaeger validator one output:" >&2
    sed 's/^/one: /' "$harness_dir/one.log" >&2
  fi
  if [[ -f "$harness_dir/two.log" ]]; then
    echo "concurrent Jaeger validator two output:" >&2
    sed 's/^/two: /' "$harness_dir/two.log" >&2
  fi
}

concurrent_failure() {
  local status="$1" line="$2" command="$3"
  trap - ERR
  printf 'concurrent Jaeger lifecycle assertion failed during %s at line %s (status %s): %s\n' \
    "$concurrent_phase" "$line" "$status" "$command" >&2
  dump_validator_logs
  exit "$status"
}

trap 'concurrent_failure "$?" "$LINENO" "$BASH_COMMAND"' ERR

cleanup_harness() {
  trap - ERR EXIT
  set +e
  if [[ -n "$validator_one_pid" ]]; then
    kill "$validator_one_pid" 2>/dev/null || true
    wait "$validator_one_pid" 2>/dev/null || true
  fi
  if [[ -n "$validator_two_pid" ]]; then
    kill "$validator_two_pid" 2>/dev/null || true
    wait "$validator_two_pid" 2>/dev/null || true
  fi
  rm -f "$harness_dir/one.ready" "$harness_dir/one.continue" \
    "$harness_dir/two.ready" "$harness_dir/two.continue" \
    "$harness_dir/one.oom-ready" "$harness_dir/one.oom-continue" \
    "$harness_dir/two.oom-ready" "$harness_dir/two.oom-continue" \
    "$harness_dir/one.log" "$harness_dir/two.log"
  rmdir "$harness_dir" 2>/dev/null || true
}
trap cleanup_harness EXIT

mkfifo "$harness_dir/one.ready" "$harness_dir/one.continue" \
  "$harness_dir/two.ready" "$harness_dir/two.continue" \
  "$harness_dir/one.oom-ready" "$harness_dir/one.oom-continue" \
  "$harness_dir/two.oom-ready" "$harness_dir/two.oom-continue"

AZEDARACH_JAEGER_TEST_CLEANUP_READY_FIFO="$harness_dir/one.ready" \
  AZEDARACH_JAEGER_TEST_CLEANUP_CONTINUE_FIFO="$harness_dir/one.continue" \
  AZEDARACH_JAEGER_TEST_OOM_READY_FIFO="$harness_dir/one.oom-ready" \
  AZEDARACH_JAEGER_TEST_OOM_CONTINUE_FIFO="$harness_dir/one.oom-continue" \
  "$repo_root/scripts/test-jaeger-local.sh" >"$harness_dir/one.log" 2>&1 &
validator_one_pid=$!
AZEDARACH_JAEGER_TEST_CLEANUP_READY_FIFO="$harness_dir/two.ready" \
  AZEDARACH_JAEGER_TEST_CLEANUP_CONTINUE_FIFO="$harness_dir/two.continue" \
  AZEDARACH_JAEGER_TEST_OOM_READY_FIFO="$harness_dir/two.oom-ready" \
  AZEDARACH_JAEGER_TEST_OOM_CONTINUE_FIFO="$harness_dir/two.oom-continue" \
  "$repo_root/scripts/test-jaeger-local.sh" >"$harness_dir/two.log" 2>&1 &
validator_two_pid=$!

concurrent_phase="OOM fallback overlap"
IFS= read -r validator_one_oom <"$harness_dir/one.oom-ready"
IFS= read -r validator_two_oom <"$harness_dir/two.oom-ready"
IFS='|' read -r validator_one_oom_tmp validator_one_lock validator_one_endpoint <<<"$validator_one_oom"
IFS='|' read -r validator_two_oom_tmp validator_two_lock validator_two_endpoint <<<"$validator_two_oom"
if [[ "$validator_one_oom_tmp" == "$validator_two_oom_tmp" ||
  "$validator_one_lock" == "$validator_two_lock" ||
  "$validator_one_endpoint" == "$validator_two_endpoint" ]]; then
  echo "concurrent OOM validators shared a fixture, lifecycle lock, or endpoint resource" >&2
  echo "one: $validator_one_oom" >&2
  echo "two: $validator_two_oom" >&2
  exit 1
fi
[[ -d "$validator_one_oom_tmp" && -d "$validator_two_oom_tmp" ]]
printf '%s\n' continue >"$harness_dir/one.oom-continue"
printf '%s\n' continue >"$harness_dir/two.oom-continue"

concurrent_phase="cleanup overlap"
IFS= read -r validator_one_tmp <"$harness_dir/one.ready"
IFS= read -r validator_two_tmp <"$harness_dir/two.ready"
if [[ "$validator_one_tmp" == "$validator_two_tmp" ]]; then
  echo "concurrent Jaeger validators shared fixture root: $validator_one_tmp" >&2
  exit 1
fi
[[ -d "$validator_one_tmp" && -d "$validator_two_tmp" ]]

printf '%s\n' continue >"$harness_dir/one.continue"
printf '%s\n' continue >"$harness_dir/two.continue"

set +e
wait "$validator_one_pid"
validator_one_status=$?
wait "$validator_two_pid"
validator_two_status=$?
set -e
validator_one_pid=""
validator_two_pid=""

if (( validator_one_status != 0 || validator_two_status != 0 )); then
  echo "concurrent Jaeger validators exited nonzero: one=$validator_one_status two=$validator_two_status" >&2
  dump_validator_logs
  exit 1
fi
concurrent_phase="success marker verification"
grep -q '^jaeger local lifecycle tests passed$' "$harness_dir/one.log"
grep -q '^jaeger local lifecycle tests passed$' "$harness_dir/two.log"
if [[ -e "$validator_one_tmp" || -e "$validator_two_tmp" ]]; then
  echo "concurrent Jaeger validator left an owned fixture root" >&2
  exit 1
fi

echo "concurrent Jaeger validator cleanup isolation passed"
