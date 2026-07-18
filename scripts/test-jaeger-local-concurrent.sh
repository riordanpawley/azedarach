#!/usr/bin/env bash

set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
harness_dir="$(mktemp -d "${TMPDIR:-/tmp}/azedarach-jaeger-concurrent.XXXXXX")"
validator_one_pid=""
validator_two_pid=""
validator_one_event=""
validator_two_event=""
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

terminate_validator() {
  local pid="$1" event_fifo="$2" continue_fifo="$3" observed_event="$4" relay_pid release_pid
  if [[ "$observed_event" == cleanup\|* ]]; then
    if ! kill -0 "$pid" 2>/dev/null; then
      wait "$pid" 2>/dev/null || true
      return 0
    fi
    printf '%s\n' continue >"$continue_fifo" &
    release_pid=$!
    wait "$pid" 2>/dev/null || true
    wait "$release_pid" 2>/dev/null || true
    return 0
  fi
  (
    trap - ERR EXIT
    set +e
    if IFS= read -r event <"$event_fifo" && [[ "$event" == cleanup\|* ]]; then
      printf '%s\n' continue >"$continue_fifo"
    fi
  ) &
  relay_pid=$!

  kill "$pid" 2>/dev/null || true
  wait "$pid" 2>/dev/null || true
  kill "$relay_pid" 2>/dev/null || true
  wait "$relay_pid" 2>/dev/null || true
}

cleanup_harness() {
  trap - ERR EXIT
  set +e
  if [[ -n "$validator_one_pid" ]]; then
    terminate_validator "$validator_one_pid" \
      "$harness_dir/one.event" "$harness_dir/one.continue" "$validator_one_event"
  fi
  if [[ -n "$validator_two_pid" ]]; then
    terminate_validator "$validator_two_pid" \
      "$harness_dir/two.event" "$harness_dir/two.continue" "$validator_two_event"
  fi
  if [[ -n "${AZEDARACH_JAEGER_TEST_TERMINATION_COMPLETE_FIFO:-}" ]]; then
    if [[ -p "$AZEDARACH_JAEGER_TEST_TERMINATION_COMPLETE_FIFO" ]]; then
      printf '%s\n' complete >"$AZEDARACH_JAEGER_TEST_TERMINATION_COMPLETE_FIFO" || true
    else
      echo "termination completion hook is not a FIFO" >&2
    fi
  fi
  rm -f "$harness_dir/one.event" "$harness_dir/one.continue" \
    "$harness_dir/two.event" "$harness_dir/two.continue" \
    "$harness_dir/one.oom-continue" "$harness_dir/two.oom-continue" \
    "$harness_dir/one.log" "$harness_dir/two.log"
  rmdir "$harness_dir" 2>/dev/null || true
}
trap cleanup_harness EXIT

mkfifo "$harness_dir/one.event" "$harness_dir/one.continue" \
  "$harness_dir/two.event" "$harness_dir/two.continue" \
  "$harness_dir/one.oom-continue" "$harness_dir/two.oom-continue"

AZEDARACH_JAEGER_TEST_EVENT_FIFO="$harness_dir/one.event" \
  AZEDARACH_JAEGER_TEST_CLEANUP_CONTINUE_FIFO="$harness_dir/one.continue" \
  AZEDARACH_JAEGER_TEST_OOM_CONTINUE_FIFO="$harness_dir/one.oom-continue" \
  AZEDARACH_JAEGER_TEST_FAIL_BEFORE_WORKER_INIT="${AZEDARACH_JAEGER_TEST_VALIDATOR_ONE_FAIL_BEFORE_WORKER_INIT:-0}" \
  AZEDARACH_JAEGER_PERL="${AZEDARACH_JAEGER_TEST_VALIDATOR_ONE_PERL:-perl}" \
  "$repo_root/scripts/test-jaeger-local.sh" >"$harness_dir/one.log" 2>&1 &
validator_one_pid=$!
AZEDARACH_JAEGER_TEST_EVENT_FIFO="$harness_dir/two.event" \
  AZEDARACH_JAEGER_TEST_CLEANUP_CONTINUE_FIFO="$harness_dir/two.continue" \
  AZEDARACH_JAEGER_TEST_OOM_CONTINUE_FIFO="$harness_dir/two.oom-continue" \
  "$repo_root/scripts/test-jaeger-local.sh" >"$harness_dir/two.log" 2>&1 &
validator_two_pid=$!

concurrent_phase="OOM fallback overlap"
IFS= read -r validator_one_event <"$harness_dir/one.event"
IFS= read -r validator_two_event <"$harness_dir/two.event"
if [[ "$validator_one_event" != oom\|* || "$validator_two_event" != oom\|* ]]; then
  echo "concurrent Jaeger validator terminated before OOM barrier" >&2
  echo "one event: $validator_one_event" >&2
  echo "two event: $validator_two_event" >&2
  dump_validator_logs
  exit 1
fi
IFS='|' read -r _ validator_one_oom_tmp validator_one_lock validator_one_endpoint <<<"$validator_one_event"
IFS='|' read -r _ validator_two_oom_tmp validator_two_lock validator_two_endpoint <<<"$validator_two_event"
if [[ "${AZEDARACH_JAEGER_TEST_INJECT_RESOURCE_COLLISION:-0}" == "1" ]]; then
  validator_two_lock="$validator_one_lock"
fi
if [[ "$validator_one_oom_tmp" == "$validator_two_oom_tmp" ||
  "$validator_one_lock" == "$validator_two_lock" ||
  "$validator_one_endpoint" == "$validator_two_endpoint" ]]; then
  echo "concurrent OOM validators shared a fixture, lifecycle lock, or endpoint resource" >&2
  printf 'one resources: %s|%s|%s\n' "$validator_one_oom_tmp" \
    "$validator_one_lock" "$validator_one_endpoint" >&2
  printf 'two resources: %s|%s|%s\n' "$validator_two_oom_tmp" \
    "$validator_two_lock" "$validator_two_endpoint" >&2
  dump_validator_logs
  exit 1
fi
[[ -d "$validator_one_oom_tmp" && -d "$validator_two_oom_tmp" ]]
if [[ -n "${AZEDARACH_JAEGER_TEST_PARENT_OOM_READY_FIFO:-}" ||
  -n "${AZEDARACH_JAEGER_TEST_PARENT_OOM_CONTINUE_FIFO:-}" ]]; then
  if [[ ! -p "${AZEDARACH_JAEGER_TEST_PARENT_OOM_READY_FIFO:-}" ||
    ! -p "${AZEDARACH_JAEGER_TEST_PARENT_OOM_CONTINUE_FIFO:-}" ]]; then
    echo "parent OOM signal barrier requires ready and continue FIFOs" >&2
    exit 1
  fi
  printf 'ready|%s|%s|%s|%s|%s|%s\n' "$$" "$validator_one_pid" \
    "$validator_two_pid" "$validator_one_oom_tmp" "$validator_two_oom_tmp" \
    "$harness_dir" >"$AZEDARACH_JAEGER_TEST_PARENT_OOM_READY_FIFO"
  IFS= read -r parent_oom_release <"$AZEDARACH_JAEGER_TEST_PARENT_OOM_CONTINUE_FIFO"
  if [[ "$parent_oom_release" != "continue" ]]; then
    echo "parent OOM signal barrier received invalid release '$parent_oom_release'" >&2
    exit 1
  fi
fi
printf '%s\n' continue >"$harness_dir/one.oom-continue"
printf '%s\n' continue >"$harness_dir/two.oom-continue"
validator_one_event=""
validator_two_event=""

concurrent_phase="cleanup overlap"
IFS= read -r validator_one_event <"$harness_dir/one.event"
IFS= read -r validator_two_event <"$harness_dir/two.event"
if [[ "$validator_one_event" != cleanup\|* || "$validator_two_event" != cleanup\|* ]]; then
  echo "concurrent Jaeger validator emitted an unexpected terminal event" >&2
  echo "one event: $validator_one_event" >&2
  echo "two event: $validator_two_event" >&2
  dump_validator_logs
  exit 1
fi
validator_one_tmp="${validator_one_event#cleanup|}"
validator_two_tmp="${validator_two_event#cleanup|}"
if [[ "$validator_one_tmp" == "$validator_two_tmp" ]]; then
  echo "concurrent Jaeger validators shared fixture root: $validator_one_tmp" >&2
  exit 1
fi
[[ -d "$validator_one_tmp" && -d "$validator_two_tmp" ]]

printf '%s\n' continue >"$harness_dir/one.continue"
printf '%s\n' continue >"$harness_dir/two.continue"

if wait "$validator_one_pid"; then
  validator_one_status=0
else
  validator_one_status=$?
fi
if wait "$validator_two_pid"; then
  validator_two_status=0
else
  validator_two_status=$?
fi
validator_one_pid=""
validator_two_pid=""

if [[ -e "$validator_one_tmp" || -e "$validator_two_tmp" ]]; then
  echo "concurrent Jaeger validator left an owned fixture root" >&2
  exit 1
fi
if (( validator_one_status != 0 || validator_two_status != 0 )); then
  echo "concurrent Jaeger validators exited nonzero: one=$validator_one_status two=$validator_two_status" >&2
  dump_validator_logs
  exit 1
fi
concurrent_phase="success marker verification"
grep -q '^jaeger local lifecycle tests passed$' "$harness_dir/one.log"
grep -q '^jaeger local lifecycle tests passed$' "$harness_dir/two.log"

echo "concurrent Jaeger validator cleanup isolation passed"
