#!/usr/bin/env bash

set -euo pipefail

test_failure() {
  local status="$1" line="$2" command="$3"
  trap - ERR
  printf 'jaeger lifecycle assertion failed at line %s (status %s): %s\n' \
    "$line" "$status" "$command" >&2
  exit "$status"
}

trap 'test_failure "$?" "$LINENO" "$BASH_COMMAND"' ERR

try_nonblocking_flock() {
  local lock="$1" perl_bin
  perl_bin="$(command -v "${AZEDARACH_JAEGER_PERL:-perl}")" || return 70
  "$perl_bin" -MFcntl=:flock -MErrno=EWOULDBLOCK,EAGAIN -e '
    use strict;
    use warnings;
    my ($lock) = @ARGV;
    open my $fh, ">>", $lock or exit 70;
    exit 0 if flock($fh, LOCK_EX | LOCK_NB);
    my $errno = 0 + $!;
    exit(($errno == EWOULDBLOCK || $errno == EAGAIN) ? 75 : 71);
  ' "$lock"
}

assert_flock_blocked() {
  local lock="$1" label="$2" probe_status
  if try_nonblocking_flock "$lock"; then
    probe_status=0
  else
    probe_status=$?
  fi
  case "$probe_status" in
    75) return 0 ;;
    0) echo "$label lock was acquirable while its owner was paused" >&2 ;;
    *) echo "$label nonblocking lock probe failed with status $probe_status" >&2 ;;
  esac
  return 1
}

assert_flock_available() {
  local lock="$1" label="$2" probe_status
  if try_nonblocking_flock "$lock"; then
    return 0
  else
    probe_status=$?
  fi
  if [[ "$probe_status" == "75" ]]; then
    echo "$label lock remained held after owner release" >&2
  else
    echo "$label nonblocking lock probe failed with status $probe_status" >&2
  fi
  return 1
}

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
source "$repo_root/scripts/jaeger-local.sh"

test_tmp_parent="${TMPDIR:-/tmp}"
tmp="$(mktemp -d "$test_tmp_parent/azedarach jaeger.XXXXXX")"
test_cleanup() {
  local child
  trap - ERR EXIT
  set +e
  for child in $(jobs -pr); do
    kill "$child" 2>/dev/null || true
  done
  wait 2>/dev/null || true
  rm -rf "$tmp"
}
trap test_cleanup EXIT
calls="$tmp/calls"
now="$(date +%s)"

fake_engine() {
  printf '%q ' "$@" >>"$calls"
  printf '\n' >>"$calls"
  case "$1" in
    inspect)
      if [[ "${2:-}" != "-f" ]]; then
        [[ "${FAKE_PRIMARY_EXISTS:-0}" == "1" ]]
        return
      fi
      case "$3" in
        *OOMKilled*) echo "${FAKE_OOM:-false}" ;;
        *State.Running*)
          case "${4:-}" in azedarach-jaeger-444) echo false ;; *) echo "${FAKE_RUNNING:-true}" ;; esac
          ;;
        *expires_at*) echo "${FAKE_EXPIRES_AT:-$((now + 3600))}" ;;
        *azedarach.jaeger.fallback*) echo true ;;
        *azedarach.jaeger.max_traces*) echo "${FAKE_LIVE_MAX_TRACES:-${AZEDARACH_JAEGER_MAX_TRACES:-40000}}" ;;
        *Config.Labels*)
          echo "azedarach.jaeger.managed = true"
          echo "azedarach.jaeger.storage = ${AZEDARACH_JAEGER_STORAGE:-memory}"
          echo "azedarach.jaeger.max_traces = ${AZEDARACH_JAEGER_MAX_TRACES:-40000}"
          echo "azedarach.jaeger.badger_ttl = ${AZEDARACH_JAEGER_BADGER_TTL:-24h}"
          ;;
      esac
      ;;
    port)
      case "$3" in 16686/tcp) echo "127.0.0.1:31686" ;; 4318/tcp) echo "127.0.0.1:34318" ;; esac
      ;;
    ps)
      case "$*" in
        *"-a --format"*) printf '%s\n' azedarach-jaeger-333 azedarach-jaeger-444 unrelated-555 ;;
        *volume=azedarach-jaeger-222-data*) echo attached ;;
        *label=azedarach.jaeger.fallback=true*) [[ "${FAKE_FALLBACK:-0}" == "1" ]] && echo azedarach-jaeger-fallback ;;
      esac
      ;;
    volume)
      if [[ "$2" == "ls" ]]; then
        printf '%s\n' azedarach-jaeger-data azedarach-jaeger-111-data azedarach-jaeger-222-data unrelated-data
      fi
      ;;
    run) [[ "${FAKE_RUN_FAIL:-0}" != "1" ]] ;;
    start) [[ "${FAKE_START_FAIL:-0}" != "1" ]] ;;
  esac
}

# The expiry owner validates and invokes its engine in a distinct Bash process.
# Export the fake through a real executable wrapper so that boundary is tested.
export -f fake_engine
export calls now
fake_engine_executable="$tmp/fake-engine"
printf '%s\n' '#!/usr/bin/env bash' 'fake_engine "$@"' >"$fake_engine_executable"
chmod +x "$fake_engine_executable"

# This fake persists container state across real Bash processes so concurrent
# lifecycle callers exercise the advisory lock rather than isolated shell data.
stateful_engine() {
  local name="" arg
  printf '%q ' "$@" >>"$stateful_calls"
  printf '\n' >>"$stateful_calls"
  case "$1" in
    inspect)
      name="${@: -1}"
      if [[ "${2:-}" != "-f" ]]; then
        [[ "$name" == "azedarach-jaeger" && -e "$stateful_primary" ]] ||
          [[ "$name" == "azedarach-jaeger-fallback" && -e "$stateful_fallback" ]]
        return
      fi
      case "$3" in
        *OOMKilled*) echo false ;;
        *State.Running*)
          if [[ "$name" == "azedarach-jaeger" && -e "$stateful_primary" ]] ||
            [[ "$name" == "azedarach-jaeger-fallback" && -e "$stateful_fallback" ]]; then
            echo true
          else
            echo false
          fi
          ;;
        *expires_at*) echo "$stateful_expires" ;;
        *azedarach.jaeger.fallback*)
          [[ "$name" == "azedarach-jaeger-fallback" ]] && echo true
          ;;
        *azedarach.jaeger.max_traces*) echo 40000 ;;
        *Config.Labels*)
          echo "azedarach.jaeger.managed = true"
          echo "azedarach.jaeger.image = cr.jaegertracing.io/jaegertracing/jaeger:2.19.0"
          echo "azedarach.jaeger.storage = memory"
          echo "azedarach.jaeger.volume = azedarach-jaeger-data"
          echo "azedarach.jaeger.max_traces = 40000"
          echo "azedarach.jaeger.badger_ttl = 24h"
          ;;
      esac
      ;;
    port)
      case "$3" in 16686/tcp) echo "127.0.0.1:31686" ;; 4318/tcp) echo "127.0.0.1:34318" ;; esac
      ;;
    ps)
      if [[ "$*" == *"label=azedarach.jaeger.fallback=true"* ]] && [[ -e "$stateful_fallback" ]]; then
        echo azedarach-jaeger-fallback
      fi
      ;;
    run)
      for ((arg = 2; arg <= $#; arg++)); do
        if [[ "${!arg}" == "--name" ]]; then
          arg=$((arg + 1))
          name="${!arg}"
          break
        fi
      done
      case "$name" in
        azedarach-jaeger)
          [[ ! -e "$stateful_primary" ]] || return 1
          : >"$stateful_primary"
          ;;
        azedarach-jaeger-fallback)
          [[ ! -e "$stateful_fallback" ]] || return 1
          : >"$stateful_fallback"
          ;;
        *) return 1 ;;
      esac
      ;;
    rm)
      name="${@: -1}"
      case "$name" in
        azedarach-jaeger) rm -f "$stateful_primary" ;;
        azedarach-jaeger-fallback) rm -f "$stateful_fallback" ;;
      esac
      ;;
    start) : >"$stateful_primary" ;;
    stop) rm -f "$stateful_primary" ;;
  esac
}

export -f stateful_engine
stateful_calls="$tmp/stateful calls"
stateful_primary="$tmp/stateful primary"
stateful_fallback="$tmp/stateful fallback"
stateful_expires="$((now + 3600))"
export stateful_calls stateful_primary stateful_fallback stateful_expires
stateful_engine_executable="$tmp/stateful-engine"
printf '%s\n' '#!/usr/bin/env bash' 'stateful_engine "$@"' >"$stateful_engine_executable"
chmod +x "$stateful_engine_executable"

export AZEDARACH_CONTAINER_ENGINE=fake_engine
export AZEDARACH_JAEGER_ENDPOINT_FILE="$tmp/endpoint"
export AZEDARACH_JAEGER_LIFECYCLE_LOCK_FILE="$tmp/jaeger lifecycle lock"
export AZEDARACH_JAEGER_STARTUP_GRACE_SECONDS=0
export AZEDARACH_JAEGER_DISABLE_EXPIRY_WORKER=1
export AZEDARACH_JAEGER_EXPIRY_READY_ATTEMPTS=5

: >"$calls"
jaeger_start fake_engine azedarach-jaeger-fallback 0 memory
grep -q -- '--rm' "$calls"
grep -q 'azedarach.jaeger.fallback=true' "$calls"
grep -q 'jaeger:2.19.0' "$calls"
grep -q 'jaeger-memory.yaml' "$calls"
grep -q 'JAEGER_MAX_TRACES=40000' "$calls"
grep -q 'azedarach.jaeger.max_traces=40000' "$calls"
if grep -q ':/badger' "$calls"; then
  echo "memory fallback unexpectedly mounted Badger" >&2
  exit 1
fi

: >"$calls"
AZEDARACH_JAEGER_STORAGE=badger AZEDARACH_JAEGER_VOLUME=chosen-store \
  jaeger_start fake_engine azedarach-jaeger 1 badger
grep -q 'chosen-store:/badger' "$calls"
[[ "$(grep -c 'chosen-store:/badger' "$calls")" == "1" ]]
grep -q 'jaeger-badger.yaml' "$calls"

inventory="$(FAKE_PRIMARY_EXISTS=1 jaeger_inventory fake_engine)"
grep -q '^Configured Jaeger trace limit: 40000$' <<<"$inventory"
grep -q '^Primary effective trace limit: 40000$' <<<"$inventory"
stale_inventory="$(FAKE_PRIMARY_EXISTS=1 FAKE_LIVE_MAX_TRACES=2000 jaeger_inventory fake_engine 2>&1)"
grep -q '^Primary effective trace limit: 2000$' <<<"$stale_inventory"
grep -q 'primary trace limit is stale' <<<"$stale_inventory"

# A successful container run followed by failed readiness is failure-atomic:
# the just-created fallback is removed before returning, and publication never
# creates endpoint or expiry-worker state.
: >"$calls"
if FAKE_RUNNING=false jaeger_start_fallback fake_engine azedarach-jaeger; then
  echo "fallback startup unexpectedly survived failed readiness" >&2
  exit 1
fi
awk '
  $1 == "run" { ran = 1 }
  ran && $1 == "rm" && $2 == "-f" && $3 == "azedarach-jaeger-fallback" { cleaned = 1 }
  END { exit !cleaned }
' "$calls"
[[ ! -e "$AZEDARACH_JAEGER_ENDPOINT_FILE" && ! -L "$AZEDARACH_JAEGER_ENDPOINT_FILE" ]]
[[ ! -e "${AZEDARACH_JAEGER_ENDPOINT_FILE}.workers" ]]

# Publication remains failure-atomic when the record and public link are
# installed but releasing the advisory lock reports failure. The exact emitted
# generation is rolled back before fallback cleanup returns.
: >"$calls"
rollback_endpoint="$tmp/rollback endpoint"
rollback_release="$tmp/fail release once"
: >"$rollback_release"
if AZEDARACH_JAEGER_ENDPOINT_FILE="$rollback_endpoint" \
  AZEDARACH_JAEGER_TEST_FAIL_LOCK_RELEASE_FILE="$rollback_release" \
  jaeger_start_fallback fake_engine azedarach-jaeger; then
  echo "fallback startup unexpectedly survived endpoint release failure" >&2
  exit 1
fi
awk '
  $1 == "run" { ran = 1 }
  ran && $1 == "rm" && $2 == "-f" && $3 == "azedarach-jaeger-fallback" { cleaned = 1 }
  END { exit !cleaned }
' "$calls"
[[ ! -e "$rollback_endpoint" && ! -L "$rollback_endpoint" ]]
if [[ -d "${rollback_endpoint}.d" ]] &&
  find "${rollback_endpoint}.d" -type f -print -quit | grep -q .; then
  echo "failed endpoint publication left an immutable record" >&2
  exit 1
fi
[[ ! -e "${rollback_endpoint}.workers" ]]

# Concurrent fixed-name fallback starts serialize the whole inspect/reuse/run/
# publish transaction. The loser must reuse the winner and cannot remove it.
concurrent_fallback_endpoint="$tmp/concurrent fallback endpoint"
concurrent_fallback_second_endpoint="$tmp/concurrent fallback second endpoint"
fallback_lock_acquired="$tmp/fallback lifecycle acquired fifo"
fallback_lock_continue="$tmp/fallback lifecycle continue fifo"
fallback_first_result="$tmp/fallback first result"
fallback_second_result="$tmp/fallback second result"
: >"$stateful_calls"
rm -f "$stateful_fallback"
mkfifo "$fallback_lock_acquired" "$fallback_lock_continue"
(
  set +e
  AZEDARACH_JAEGER_ENDPOINT_FILE="$concurrent_fallback_second_endpoint" \
    AZEDARACH_JAEGER_TEST_LIFECYCLE_ACQUIRED_FIFO="$fallback_lock_acquired" \
    AZEDARACH_JAEGER_TEST_LIFECYCLE_CONTINUE_FIFO="$fallback_lock_continue" \
    jaeger_start_fallback "$stateful_engine_executable" azedarach-jaeger
  printf '%s\n' "$?" >"$fallback_first_result"
) &
fallback_first_pid=$!
IFS= read -r fallback_lock_signal <"$fallback_lock_acquired"
[[ "$fallback_lock_signal" == "ready" ]]
(
  set +e
  AZEDARACH_JAEGER_ENDPOINT_FILE="$concurrent_fallback_endpoint" \
    jaeger_start_fallback "$stateful_engine_executable" azedarach-jaeger
  printf '%s\n' "$?" >"$fallback_second_result"
) &
fallback_second_pid=$!
assert_flock_blocked "$AZEDARACH_JAEGER_LIFECYCLE_LOCK_FILE" "fallback lifecycle"
printf '%s\n' continue >"$fallback_lock_continue"
wait "$fallback_first_pid"
wait "$fallback_second_pid"
assert_flock_available "$AZEDARACH_JAEGER_LIFECYCLE_LOCK_FILE" "fallback lifecycle"
[[ "$(<"$fallback_first_result")" == "0" && "$(<"$fallback_second_result")" == "0" ]]
[[ "$(grep -c '^run ' "$stateful_calls")" == "1" ]]
awk '
  $1 == "run" { ran = 1 }
  ran && $1 == "rm" && $2 == "-f" && $3 == "azedarach-jaeger-fallback" { exit 1 }
' "$stateful_calls"
[[ -e "$stateful_fallback" && -e "$concurrent_fallback_endpoint" && -e "$concurrent_fallback_second_endpoint" ]]

# Concurrent primary ensures use the same lifecycle lock and ordering. Once the
# first caller creates and activates the primary, the second observes/reuses it.
concurrent_primary_endpoint="$tmp/concurrent primary endpoint"
concurrent_primary_second_endpoint="$tmp/concurrent primary second endpoint"
primary_lock_acquired="$tmp/primary lifecycle acquired fifo"
primary_lock_continue="$tmp/primary lifecycle continue fifo"
primary_first_result="$tmp/primary first result"
primary_second_result="$tmp/primary second result"
: >"$stateful_calls"
rm -f "$stateful_primary" "$stateful_fallback"
mkfifo "$primary_lock_acquired" "$primary_lock_continue"
(
  set +e
  AZEDARACH_CONTAINER_ENGINE="$stateful_engine_executable" \
    AZEDARACH_JAEGER_ENDPOINT_FILE="$concurrent_primary_second_endpoint" \
    AZEDARACH_JAEGER_TEST_LIFECYCLE_ACQUIRED_FIFO="$primary_lock_acquired" \
    AZEDARACH_JAEGER_TEST_LIFECYCLE_CONTINUE_FIFO="$primary_lock_continue" \
    jaeger_ensure
  printf '%s\n' "$?" >"$primary_first_result"
) &
primary_first_pid=$!
IFS= read -r primary_lock_signal <"$primary_lock_acquired"
[[ "$primary_lock_signal" == "ready" ]]
(
  set +e
  AZEDARACH_CONTAINER_ENGINE="$stateful_engine_executable" \
    AZEDARACH_JAEGER_ENDPOINT_FILE="$concurrent_primary_endpoint" \
    jaeger_ensure
  printf '%s\n' "$?" >"$primary_second_result"
) &
primary_second_pid=$!
assert_flock_blocked "$AZEDARACH_JAEGER_LIFECYCLE_LOCK_FILE" "primary lifecycle"
printf '%s\n' continue >"$primary_lock_continue"
wait "$primary_first_pid"
wait "$primary_second_pid"
assert_flock_available "$AZEDARACH_JAEGER_LIFECYCLE_LOCK_FILE" "primary lifecycle"
[[ "$(<"$primary_first_result")" == "0" && "$(<"$primary_second_result")" == "0" ]]
[[ "$(grep -c '^run ' "$stateful_calls")" == "1" ]]
awk '
  $1 == "run" { ran = 1 }
  ran && $1 == "rm" && $2 == "-f" && $3 == "azedarach-jaeger" { exit 1 }
' "$stateful_calls"
[[ -e "$stateful_primary" && -e "$concurrent_primary_endpoint" && -e "$concurrent_primary_second_endpoint" ]]

: >"$calls"
FAKE_FALLBACK=1 jaeger_start_fallback fake_engine azedarach-jaeger
if grep -q '^run ' "$calls"; then
  echo "running fallback was not reused" >&2
  exit 1
fi

: >"$calls"
FAKE_FALLBACK=1 jaeger_activate_primary fake_engine azedarach-jaeger
grep -q '^rm -f azedarach-jaeger-fallback ' "$calls"
grep -qx 'localhost:34318' "$AZEDARACH_JAEGER_ENDPOINT_FILE"

: >"$calls"
FAKE_FALLBACK=1 jaeger_cleanup fake_engine
if grep -q '^rm -f azedarach-jaeger-fallback ' "$calls"; then
  echo "cleanup removed an active fallback" >&2
  exit 1
fi
if grep -q '^rm azedarach-jaeger-333 ' "$calls"; then
  echo "cleanup removed a running legacy fallback" >&2
  exit 1
fi
grep -q '^rm azedarach-jaeger-444 ' "$calls"

: >"$calls"
jaeger_cleanup fake_engine
grep -q '^volume rm azedarach-jaeger-111-data ' "$calls"
if grep -Eq '^volume rm (azedarach-jaeger-data|azedarach-jaeger-222-data|unrelated-data) ' "$calls"; then
  echo "cleanup selected an active, primary, or unrelated volume" >&2
  exit 1
fi

: >"$calls"
FAKE_PRIMARY_EXISTS=1 FAKE_OOM=true jaeger_ensure
if grep -q '^rm -f azedarach-jaeger ' "$calls"; then
  echo "OOM recovery removed the primary container" >&2
  exit 1
fi
grep -q '^run ' "$calls"
grep -q 'jaeger-memory.yaml' "$calls"
if grep -q ':/badger' "$calls"; then
  echo "OOM recovery reattached Badger" >&2
  exit 1
fi

: >"$calls"
output="$(jaeger_publish_env fake_engine azedarach-jaeger-fallback)"
grep -q 'localhost:31686' <<<"$output"
grep -q 'localhost:34318/v1/traces' <<<"$output"
grep -qx 'localhost:34318' "$AZEDARACH_JAEGER_ENDPOINT_FILE"
[[ "$(sed -n '2p' "$AZEDARACH_JAEGER_ENDPOINT_FILE")" -gt "$now" ]]

# The persistent lock file carries no ownership by itself. With no advisory
# holder, legacy contents cannot block the next publication.
printf '%s\n%s\n' 99999999 stale-start >"${AZEDARACH_JAEGER_ENDPOINT_FILE}.state-lock"
stale_lock_record="$(jaeger_write_endpoint_state localhost:34318 "$((now + 3600))" stale-lock-recovery)"
[[ -e "$stale_lock_record" && -f "${AZEDARACH_JAEGER_ENDPOINT_FILE}.state-lock" ]]

# Rewriting stale-looking file contents while an advisory lock is held cannot
# prevent the queued writer from acquiring the same lock after release.
jaeger_endpoint_lock_acquire
blocked_entered="$tmp/blocked entered fifo"
mkfifo "$blocked_entered"
(
  printf '%s\n' ready >"$blocked_entered"
  jaeger_write_endpoint_state localhost:34318 "$((now + 3600))" blocked-writer >"$tmp/blocked record"
) &
blocked_pid=$!
IFS= read -r blocked_signal <"$blocked_entered"
[[ "$blocked_signal" == "ready" ]]
printf '%s\n%s\n' 88888888 replacement-looking >"${AZEDARACH_JAEGER_ENDPOINT_FILE}.state-lock"
assert_flock_blocked "${AZEDARACH_JAEGER_ENDPOINT_FILE}.state-lock" "endpoint mutation"
jaeger_endpoint_lock_release
wait "$blocked_pid"
assert_flock_available "${AZEDARACH_JAEGER_ENDPOINT_FILE}.state-lock" "endpoint mutation"
[[ -e "$(sed -n '1p' "$tmp/blocked record")" ]]

# macOS /bin/bash 3.2 does not expose BASHPID and keeps $$ equal to the
# long-lived parent in subshells. The Perl holder captures getppid() itself, so
# killing the actual spawning shell releases flock and unblocks the contender.
bash32_endpoint="$tmp/bash 3.2 endpoint"
bash32_ready="$tmp/bash 3.2 lock ready fifo"
bash32_hold="$tmp/bash 3.2 lock hold fifo"
mkfifo "$bash32_ready" "$bash32_hold"
JAEGER_SCRIPT="$repo_root/scripts/jaeger-local.sh" \
  AZEDARACH_JAEGER_ENDPOINT_FILE="$bash32_endpoint" \
  LOCK_READY="$bash32_ready" \
  LOCK_HOLD="$bash32_hold" \
  /bin/bash -c '
    set -euo pipefail
    source "$JAEGER_SCRIPT"
    jaeger_endpoint_lock_acquire
    printf "%s\n" ready >"$LOCK_READY"
    IFS= read -r _ <"$LOCK_HOLD"
  ' &
bash32_owner=$!
IFS= read -r bash32_signal <"$bash32_ready"
[[ "$bash32_signal" == "ready" ]]
bash32_contender_entered="$tmp/bash 3.2 contender entered fifo"
mkfifo "$bash32_contender_entered"
(
  printf '%s\n' ready >"$bash32_contender_entered"
  AZEDARACH_JAEGER_ENDPOINT_FILE="$bash32_endpoint" \
    jaeger_write_endpoint_state localhost:4318 0 bash32-contender >"$tmp/bash 3.2 record"
) &
bash32_contender=$!
IFS= read -r bash32_contender_signal <"$bash32_contender_entered"
[[ "$bash32_contender_signal" == "ready" ]]
assert_flock_blocked "${bash32_endpoint}.state-lock" "Bash 3.2 endpoint"
kill -9 "$bash32_owner"
wait "$bash32_owner" 2>/dev/null || true
wait "$bash32_contender"
assert_flock_available "${bash32_endpoint}.state-lock" "Bash 3.2 endpoint"
[[ -e "$(sed -n '1p' "$tmp/bash 3.2 record")" ]]
if find "$tmp" -maxdepth 1 -name 'bash 3.2 endpoint.state-lock.control.*' -print -quit | grep -q .; then
  echo "dead Bash 3.2 lock owner left a helper control directory" >&2
  exit 1
fi

# The advisory lock dependency is explicit and endpoint mutation fails closed
# with a useful diagnostic when Perl/Fcntl cannot be launched.
missing_perl_endpoint="$tmp/missing perl endpoint"
if missing_perl_output="$(
  AZEDARACH_JAEGER_ENDPOINT_FILE="$missing_perl_endpoint" \
    AZEDARACH_JAEGER_PERL="$tmp/missing-perl" \
    jaeger_write_endpoint_state localhost:4318 0 missing-perl 2>&1
)"; then
  echo "endpoint publication unexpectedly succeeded without Perl" >&2
  exit 1
fi
grep -q 'Perl with Fcntl flock support is required' <<<"$missing_perl_output"
[[ ! -e "$missing_perl_endpoint" && ! -L "$missing_perl_endpoint" ]]

# Clearing the active generation removes both its immutable record and the
# public symlink, rather than leaving a dangling endpoint path.
active_record="$(jaeger_write_endpoint_state localhost:34318 "$((now + 3600))" active-clear)"
[[ -L "$AZEDARACH_JAEGER_ENDPOINT_FILE" ]]
jaeger_clear_endpoint_record "$active_record"
[[ ! -e "$active_record" && ! -L "$AZEDARACH_JAEGER_ENDPOINT_FILE" ]]

# Clearing an older generation cannot disturb a newer public target.
stale_record="$(jaeger_write_endpoint_state localhost:34318 "$((now + 3600))" stale-clear)"
newer_record="$(jaeger_write_endpoint_state localhost:4318 0 newer-clear)"
printf '%s\n%s\n' localhost:34318 "$((now + 3600))" >"$stale_record"
jaeger_clear_endpoint_record "$stale_record"
[[ ! -e "$stale_record" && -e "$newer_record" ]]
[[ "$(readlink "$AZEDARACH_JAEGER_ENDPOINT_FILE")" == "$newer_record" ]]

# Expiry removes the matching running fallback and invalidates only its
# immutable endpoint generation.
: >"$calls"
expired="$((now - 1))"
old_record="$(jaeger_write_endpoint_state localhost:34318 "$expired")"
FAKE_EXPIRES_AT="$expired" jaeger_expire_fallback fake_engine azedarach-jaeger-fallback "$expired" "$old_record"
grep -q '^rm -f azedarach-jaeger-fallback ' "$calls"
if [[ -e "$old_record" || -e "$AZEDARACH_JAEGER_ENDPOINT_FILE" || -L "$AZEDARACH_JAEGER_ENDPOINT_FILE" ]]; then
  echo "expired fallback endpoint remained readable" >&2
  exit 1
fi

# A newer primary publication is a different immutable generation and cannot
# be cleared by the older fallback worker.
fallback_record="$(jaeger_write_endpoint_state localhost:34318 "$expired")"
primary_record="$(jaeger_write_endpoint_state localhost:4318 0)"
FAKE_EXPIRES_AT="$expired" jaeger_expire_fallback fake_engine azedarach-jaeger-fallback "$expired" "$fallback_record"
[[ ! -e "$fallback_record" ]]
[[ -e "$primary_record" ]]
grep -qx 'localhost:4318' "$AZEDARACH_JAEGER_ENDPOINT_FILE"

# An old owner cannot remove a replacement container that reused the name but
# carries a different expiry identity.
: >"$calls"
old_expires="$((now - 1))"
replacement_expires="$((now + 3600))"
old_record="$(jaeger_write_endpoint_state localhost:34318 "$old_expires" old-owner)"
FAKE_EXPIRES_AT="$replacement_expires" jaeger_expire_fallback \
  fake_engine azedarach-jaeger-fallback "$old_expires" "$old_record"
if grep -q '^rm -f azedarach-jaeger-fallback ' "$calls"; then
  echo "old expiry owner removed a replacement fallback" >&2
  exit 1
fi
[[ ! -e "$old_record" ]]

# Initialization failure is failure-atomic: no collector, endpoint, or slot is
# left behind when the child cannot publish readiness.
export AZEDARACH_JAEGER_DISABLE_EXPIRY_WORKER=0
export FAKE_EXPIRES_AT="$((now + 60))"
export AZEDARACH_JAEGER_EXPIRY_WORKER_FAIL_INIT=1
JAEGER_PUBLISHED_EXPIRES_AT="$((now + 60))"
JAEGER_PUBLISHED_ENDPOINT_RECORD="$(jaeger_write_endpoint_state localhost:34318 "$JAEGER_PUBLISHED_EXPIRES_AT" azedarach-jaeger-fallback)"
if jaeger_schedule_published_expiry "$fake_engine_executable" azedarach-jaeger-fallback; then
  echo "expiry scheduling unexpectedly survived child initialization failure" >&2
  exit 1
fi
grep -q '^rm -f azedarach-jaeger-fallback ' "$calls"
[[ ! -e "$AZEDARACH_JAEGER_ENDPOINT_FILE" ]]
unset AZEDARACH_JAEGER_EXPIRY_WORKER_FAIL_INIT

# Failure to spawn the worker has the same fail-closed cleanup.
JAEGER_PUBLISHED_ENDPOINT_RECORD="$(jaeger_write_endpoint_state localhost:34318 "$JAEGER_PUBLISHED_EXPIRES_AT" azedarach-jaeger-fallback)"
export AZEDARACH_JAEGER_EXPIRY_SHELL="$tmp/missing-shell"
if jaeger_schedule_published_expiry "$fake_engine_executable" azedarach-jaeger-fallback; then
  echo "expiry scheduling unexpectedly survived spawn failure" >&2
  exit 1
fi
[[ ! -e "$AZEDARACH_JAEGER_ENDPOINT_FILE" ]]
unset AZEDARACH_JAEGER_EXPIRY_SHELL

# Reusing a live fallback also fails closed when ownership cannot initialize.
: >"$calls"
export FAKE_FALLBACK=1 AZEDARACH_JAEGER_EXPIRY_WORKER_FAIL_INIT=1
if jaeger_reuse_fallback "$fake_engine_executable" azedarach-jaeger; then
  echo "fallback reuse unexpectedly survived expiry initialization failure" >&2
  exit 1
fi
grep -q '^rm -f azedarach-jaeger-fallback ' "$calls"
unset FAKE_FALLBACK AZEDARACH_JAEGER_EXPIRY_WORKER_FAIL_INIT

# Errors before spawn are also fail-closed at the caller boundary.
: >"$calls"
JAEGER_PUBLISHED_ENDPOINT_RECORD="$(jaeger_write_endpoint_state localhost:34318 "$JAEGER_PUBLISHED_EXPIRES_AT" azedarach-jaeger-fallback)"
rmdir "${AZEDARACH_JAEGER_ENDPOINT_FILE}.workers"
printf '%s\n' occupied >"${AZEDARACH_JAEGER_ENDPOINT_FILE}.workers"
if jaeger_require_published_expiry "$fake_engine_executable" azedarach-jaeger-fallback; then
  echo "expiry ownership unexpectedly survived state-directory failure" >&2
  exit 1
fi
grep -q '^rm -f azedarach-jaeger-fallback ' "$calls"
[[ ! -e "$AZEDARACH_JAEGER_ENDPOINT_FILE" ]]
rm -f "${AZEDARACH_JAEGER_ENDPOINT_FILE}.workers"

# Stateful reuse relinks the established owner's immutable endpoint generation.
# It must not replace the collector or owner, and the original deadline wins.
: >"$calls"
worker_initialized_fifo="$tmp/worker initialized fifo"
mkfifo "$worker_initialized_fifo"
export AZEDARACH_JAEGER_TEST_WORKER_INITIALIZED_FIFO="$worker_initialized_fifo"
expiry_ready_fifo="$tmp/expiry ready fifo"
expiry_continue_fifo="$tmp/expiry continue fifo"
mkfifo "$expiry_ready_fifo" "$expiry_continue_fifo"
export AZEDARACH_JAEGER_TEST_EXPIRY_READY_FIFO="$expiry_ready_fifo"
export AZEDARACH_JAEGER_TEST_EXPIRY_CONTINUE_FIFO="$expiry_continue_fifo"
export FAKE_FALLBACK=1 FAKE_EXPIRES_AT="$((now + 60))"
jaeger_publish_env "$fake_engine_executable" azedarach-jaeger-fallback >/dev/null
jaeger_schedule_published_expiry "$fake_engine_executable" azedarach-jaeger-fallback
IFS= read -r expiry_ready_signal <"$expiry_ready_fifo"
[[ "$expiry_ready_signal" == "ready" ]]
worker_ready_file="$(find "${AZEDARACH_JAEGER_ENDPOINT_FILE}.workers" -name ready -type f)"
worker_pid="$(sed -n '1p' "$worker_ready_file")"
worker_record="$(sed -n '7p' "$worker_ready_file")"
original_expires="$JAEGER_PUBLISHED_EXPIRES_AT"
: >"$calls"
jaeger_reuse_fallback "$fake_engine_executable" azedarach-jaeger >/dev/null
[[ "$(find "${AZEDARACH_JAEGER_ENDPOINT_FILE}.workers" -name ready -type f | wc -l | tr -d ' ')" == "1" ]]
[[ "$(sed -n '1p' "$worker_ready_file")" == "$worker_pid" ]]
[[ "$JAEGER_PUBLISHED_ENDPOINT_RECORD" == "$worker_record" ]]
[[ "$JAEGER_PUBLISHED_EXPIRES_AT" == "$original_expires" ]]
if grep -Eq '^(rm -f|run) ' "$calls"; then
  echo "live fallback reuse replaced the collector" >&2
  exit 1
fi
printf '%s\n' continue >"$expiry_continue_fifo"
wait "$worker_pid"
grep -q '^rm -f azedarach-jaeger-fallback ' "$calls"
[[ ! -e "$worker_record" && ! -e "$AZEDARACH_JAEGER_ENDPOINT_FILE" ]]
unset FAKE_FALLBACK AZEDARACH_JAEGER_TEST_EXPIRY_READY_FIFO AZEDARACH_JAEGER_TEST_EXPIRY_CONTINUE_FIFO

# Endpoint mutation is serialized across the full adoption decision. If a
# primary publication wins after fallback adoption observes its owner, the
# fallback cannot relink its deleted record or overwrite the primary endpoint.
: >"$calls"
export FAKE_FALLBACK=1 FAKE_EXPIRES_AT="$(( $(date +%s) + 60 ))"
JAEGER_PUBLISHED_EXPIRES_AT="$FAKE_EXPIRES_AT"
JAEGER_PUBLISHED_ENDPOINT_RECORD="$(jaeger_write_endpoint_state localhost:34318 "$JAEGER_PUBLISHED_EXPIRES_AT" race-fallback)"
jaeger_schedule_published_expiry "$fake_engine_executable" azedarach-jaeger-fallback
race_ready_file="$(find "${AZEDARACH_JAEGER_ENDPOINT_FILE}.workers" -name ready -type f -print -quit)"
race_slot="$(dirname "$race_ready_file")"
race_pid="$(sed -n '1p' "$race_ready_file")"
race_record="$JAEGER_PUBLISHED_ENDPOINT_RECORD"
adopt_observed_fifo="$tmp/adopt observed fifo"
adopt_continue_fifo="$tmp/adopt continue fifo"
mkfifo "$adopt_observed_fifo" "$adopt_continue_fifo"
export AZEDARACH_JAEGER_TEST_ADOPT_OBSERVED_FIFO="$adopt_observed_fifo"
export AZEDARACH_JAEGER_TEST_ADOPT_CONTINUE_FIFO="$adopt_continue_fifo"
(
  set +e
  jaeger_start_fallback "$fake_engine_executable" azedarach-jaeger >"$tmp/adopt output" 2>&1
  printf '%s\n' "$?" >"$tmp/adopt status"
) &
adopt_pid=$!
IFS= read -r adopt_signal <"$adopt_observed_fifo"
[[ "$adopt_signal" == "ready" ]]
primary_record="$(jaeger_write_endpoint_state localhost:4318 0 race-primary)"
printf '%s\n' continue >"$adopt_continue_fifo"
wait "$adopt_pid"
[[ "$(sed -n '1p' "$tmp/adopt status")" == "0" ]]
[[ "$(readlink "$AZEDARACH_JAEGER_ENDPOINT_FILE")" == "$primary_record" ]]
[[ -e "$primary_record" && ! -e "$race_record" ]]
grep -q 'refusing to replace a newer managed Jaeger endpoint' "$tmp/adopt output"
grep -q 'newer managed Jaeger endpoint won fallback publication' "$tmp/adopt output"
if grep -Eq '^(rm -f|run) ' "$calls"; then
  echo "losing fallback publisher replaced the primary winner" >&2
  exit 1
fi
unset AZEDARACH_JAEGER_TEST_ADOPT_OBSERVED_FIFO AZEDARACH_JAEGER_TEST_ADOPT_CONTINUE_FIFO
kill "$race_pid" 2>/dev/null || true
wait "$race_pid" 2>/dev/null || true
jaeger_clear_worker_slot "$race_slot" || true
unset FAKE_FALLBACK

# A required generation handoff disarms the established owner before stopping
# it. If the successor cannot initialize, the new endpoint and collector are
# removed rather than leaving an unowned fallback.
: >"$calls"
export FAKE_EXPIRES_AT="$(( $(date +%s) + 60 ))"
JAEGER_PUBLISHED_EXPIRES_AT="$FAKE_EXPIRES_AT"
JAEGER_PUBLISHED_ENDPOINT_RECORD="$(jaeger_write_endpoint_state localhost:34318 "$JAEGER_PUBLISHED_EXPIRES_AT" first-generation)"
jaeger_schedule_published_expiry "$fake_engine_executable" azedarach-jaeger-fallback
handoff_ready_file="$(find "${AZEDARACH_JAEGER_ENDPOINT_FILE}.workers" -name ready -type f -print -quit)"
handoff_slot="$(dirname "$handoff_ready_file")"
handoff_pid="$(sed -n '1p' "$handoff_slot/ready")"
JAEGER_PUBLISHED_ENDPOINT_RECORD="$(jaeger_write_endpoint_state localhost:34318 "$JAEGER_PUBLISHED_EXPIRES_AT" successor-generation)"
export AZEDARACH_JAEGER_EXPIRY_WORKER_FAIL_INIT=1
unset AZEDARACH_JAEGER_TEST_WORKER_INITIALIZED_FIFO
if jaeger_schedule_published_expiry "$fake_engine_executable" azedarach-jaeger-fallback; then
  echo "failed successor unexpectedly retained expiry ownership" >&2
  exit 1
fi
if kill -0 "$handoff_pid" 2>/dev/null; then
  echo "established expiry owner survived generation handoff" >&2
  exit 1
fi
wait "$handoff_pid" 2>/dev/null || true
export AZEDARACH_JAEGER_TEST_WORKER_INITIALIZED_FIFO="$worker_initialized_fifo"
grep -q '^rm -f azedarach-jaeger-fallback ' "$calls"
[[ ! -e "$JAEGER_PUBLISHED_ENDPOINT_RECORD" && ! -e "$AZEDARACH_JAEGER_ENDPOINT_FILE" ]]
unset AZEDARACH_JAEGER_EXPIRY_WORKER_FAIL_INIT

# A stale ready record cannot substitute an unrelated reused PID. The unrelated
# process survives, while a new nonce/start-bound owner takes the abandoned slot.
export FAKE_EXPIRES_AT="$(( $(date +%s) + 60 ))"
JAEGER_PUBLISHED_EXPIRES_AT="$FAKE_EXPIRES_AT"
JAEGER_PUBLISHED_ENDPOINT_RECORD="$(jaeger_write_endpoint_state localhost:34318 "$JAEGER_PUBLISHED_EXPIRES_AT" azedarach-jaeger-fallback)"
worker_key="$(printf '%s' "${fake_engine_executable}:azedarach-jaeger-fallback:${JAEGER_PUBLISHED_EXPIRES_AT}" | cksum | awk '{print $1}')"
stale_slot="${AZEDARACH_JAEGER_ENDPOINT_FILE}.workers/${worker_key}"
mkdir "$stale_slot"
unrelated_hold_fifo="$tmp/unrelated process hold fifo"
mkfifo "$unrelated_hold_fifo"
/bin/bash -c 'IFS= read -r _ <"$1"' _ "$unrelated_hold_fifo" &
unrelated_pid=$!
unrelated_start="$(ps -p "$unrelated_pid" -o lstart= | sed 's/^[[:space:]]*//;s/[[:space:]]*$//')"
printf '%s\n%s\n%s\n%s\n%s\n%s\n%s\n%s\n' \
  "$unrelated_pid" "$unrelated_start" stale-nonce "$fake_engine_executable" \
  azedarach-jaeger-fallback "$JAEGER_PUBLISHED_EXPIRES_AT" \
  "$JAEGER_PUBLISHED_ENDPOINT_RECORD" "$stale_slot" >"$stale_slot/ready"
jaeger_schedule_published_expiry "$fake_engine_executable" azedarach-jaeger-fallback
kill -0 "$unrelated_pid"
replacement_pid="$(sed -n '1p' "$stale_slot/ready")"
[[ "$replacement_pid" != "$unrelated_pid" ]]
kill "$replacement_pid" 2>/dev/null || true
wait "$replacement_pid" 2>/dev/null || true
kill "$unrelated_pid" 2>/dev/null || true
wait "$unrelated_pid" 2>/dev/null || true
jaeger_clear_worker_slot "$stale_slot" || true

# An abandoned pre-readiness slot is reclaimed instead of suppressing expiry.
JAEGER_PUBLISHED_ENDPOINT_RECORD="$(jaeger_write_endpoint_state localhost:34318 "$JAEGER_PUBLISHED_EXPIRES_AT" azedarach-jaeger-fallback)"
worker_key="$(printf '%s' "${fake_engine_executable}:azedarach-jaeger-fallback:${JAEGER_PUBLISHED_EXPIRES_AT}" | cksum | awk '{print $1}')"
abandoned_slot="${AZEDARACH_JAEGER_ENDPOINT_FILE}.workers/${worker_key}"
mkdir "$abandoned_slot"
jaeger_schedule_published_expiry "$fake_engine_executable" azedarach-jaeger-fallback
[[ -f "$abandoned_slot/ready" ]]
worker_pid="$(sed -n '1p' "$abandoned_slot/ready")"
kill "$worker_pid" 2>/dev/null || true
wait "$worker_pid" 2>/dev/null || true
jaeger_clear_worker_slot "$abandoned_slot" || true

# The ready worker survives its scheduling caller and expires the fallback
# after an explicit test-controlled expiry transition.
: >"$calls"
final_expiry_ready_fifo="$tmp/final expiry ready fifo"
final_expiry_continue_fifo="$tmp/final expiry continue fifo"
final_expiry_complete_fifo="$tmp/final expiry complete fifo"
mkfifo "$final_expiry_ready_fifo" "$final_expiry_continue_fifo" "$final_expiry_complete_fifo"
export AZEDARACH_JAEGER_TEST_EXPIRY_READY_FIFO="$final_expiry_ready_fifo"
export AZEDARACH_JAEGER_TEST_EXPIRY_CONTINUE_FIFO="$final_expiry_continue_fifo"
export AZEDARACH_JAEGER_TEST_EXPIRY_COMPLETE_FIFO="$final_expiry_complete_fifo"
export FAKE_EXPIRES_AT="$((now + 60))"
JAEGER_PUBLISHED_EXPIRES_AT="$FAKE_EXPIRES_AT"
JAEGER_PUBLISHED_ENDPOINT_RECORD="$(jaeger_write_endpoint_state localhost:34318 "$JAEGER_PUBLISHED_EXPIRES_AT" azedarach-jaeger-fallback)"
(jaeger_schedule_published_expiry "$fake_engine_executable" azedarach-jaeger-fallback)
IFS= read -r final_expiry_signal <"$final_expiry_ready_fifo"
[[ "$final_expiry_signal" == "ready" ]]
final_worker_ready_file="$(find "${AZEDARACH_JAEGER_ENDPOINT_FILE}.workers" -name ready -type f -print -quit)"
[[ -n "$(sed -n '1p' "$final_worker_ready_file")" ]]
printf '%s\n' continue >"$final_expiry_continue_fifo"
IFS= read -r final_expiry_complete_signal <"$final_expiry_complete_fifo"
[[ "$final_expiry_complete_signal" == "complete" ]]
grep -q '^rm -f azedarach-jaeger-fallback ' "$calls"
[[ ! -e "$AZEDARACH_JAEGER_ENDPOINT_FILE" ]]
unset AZEDARACH_JAEGER_TEST_EXPIRY_READY_FIFO AZEDARACH_JAEGER_TEST_EXPIRY_CONTINUE_FIFO \
  AZEDARACH_JAEGER_TEST_EXPIRY_COMPLETE_FIFO AZEDARACH_JAEGER_TEST_WORKER_INITIALIZED_FIFO
export AZEDARACH_JAEGER_DISABLE_EXPIRY_WORKER=1

echo "jaeger local lifecycle tests passed"
