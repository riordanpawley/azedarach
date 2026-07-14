#!/usr/bin/env bash

set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
source "$repo_root/scripts/jaeger-local.sh"

test_tmp_parent="${TMPDIR:-/tmp}"
tmp="$(mktemp -d "$test_tmp_parent/azedarach jaeger.XXXXXX")"
trap 'rm -rf "$tmp"' EXIT
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
        *Config.Labels*)
          echo "azedarach.jaeger.managed = true"
          echo "azedarach.jaeger.storage = ${AZEDARACH_JAEGER_STORAGE:-memory}"
          echo "azedarach.jaeger.max_traces = ${AZEDARACH_JAEGER_MAX_TRACES:-2000}"
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

export AZEDARACH_CONTAINER_ENGINE=fake_engine
export AZEDARACH_JAEGER_ENDPOINT_FILE="$tmp/endpoint"
export AZEDARACH_JAEGER_STARTUP_GRACE_SECONDS=0
export AZEDARACH_JAEGER_DISABLE_EXPIRY_WORKER=1
export AZEDARACH_JAEGER_EXPIRY_READY_ATTEMPTS=5

: >"$calls"
jaeger_start fake_engine azedarach-jaeger-fallback 0 memory
grep -q -- '--rm' "$calls"
grep -q 'azedarach.jaeger.fallback=true' "$calls"
grep -q 'jaeger:2.19.0' "$calls"
grep -q 'jaeger-memory.yaml' "$calls"
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

# Rewriting stale-looking file contents cannot steal a live advisory lock or
# admit another writer into the critical section.
jaeger_endpoint_lock_acquire
(
  jaeger_write_endpoint_state localhost:34318 "$((now + 3600))" blocked-writer >"$tmp/blocked record"
  : >"$tmp/blocked done"
) &
blocked_pid=$!
sleep 0.2
[[ ! -e "$tmp/blocked done" ]]
printf '%s\n%s\n' 88888888 replacement-looking >"${AZEDARACH_JAEGER_ENDPOINT_FILE}.state-lock"
sleep 0.1
[[ ! -e "$tmp/blocked done" ]]
jaeger_endpoint_lock_release
wait "$blocked_pid"
[[ -e "$(sed -n '1p' "$tmp/blocked record")" ]]

# macOS /bin/bash 3.2 does not expose BASHPID and keeps $$ equal to the
# long-lived parent in subshells. The Perl holder captures getppid() itself, so
# killing the actual spawning shell releases flock and unblocks the contender.
bash32_endpoint="$tmp/bash 3.2 endpoint"
bash32_ready="$tmp/bash 3.2 lock ready"
JAEGER_SCRIPT="$repo_root/scripts/jaeger-local.sh" \
  AZEDARACH_JAEGER_ENDPOINT_FILE="$bash32_endpoint" \
  LOCK_READY="$bash32_ready" \
  /bin/bash -c '
    set -euo pipefail
    source "$JAEGER_SCRIPT"
    jaeger_endpoint_lock_acquire
    : >"$LOCK_READY"
    while :; do sleep 1; done
  ' &
bash32_owner=$!
for attempt in {1..200}; do
  [[ -e "$bash32_ready" ]] && break
  sleep 0.05
done
[[ -e "$bash32_ready" ]]
(
  AZEDARACH_JAEGER_ENDPOINT_FILE="$bash32_endpoint" \
    jaeger_write_endpoint_state localhost:4318 0 bash32-contender >"$tmp/bash 3.2 record"
  : >"$tmp/bash 3.2 done"
) &
bash32_contender=$!
sleep 0.2
[[ ! -e "$tmp/bash 3.2 done" ]]
kill -9 "$bash32_owner"
wait "$bash32_owner" 2>/dev/null || true
for attempt in {1..200}; do
  [[ -e "$tmp/bash 3.2 done" ]] && break
  sleep 0.05
done
wait "$bash32_contender"
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
export FAKE_FALLBACK=1 FAKE_EXPIRES_AT="$(( $(date +%s) + 3 ))"
jaeger_publish_env "$fake_engine_executable" azedarach-jaeger-fallback >/dev/null
jaeger_schedule_published_expiry "$fake_engine_executable" azedarach-jaeger-fallback
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
sleep 4
grep -q '^rm -f azedarach-jaeger-fallback ' "$calls"
[[ ! -e "$worker_record" && ! -e "$AZEDARACH_JAEGER_ENDPOINT_FILE" ]]
unset FAKE_FALLBACK

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
export AZEDARACH_JAEGER_TEST_ADOPT_OBSERVED_FILE="$tmp/adopt observed"
export AZEDARACH_JAEGER_TEST_ADOPT_CONTINUE_FILE="$tmp/adopt continue"
(
  set +e
  jaeger_start_fallback "$fake_engine_executable" azedarach-jaeger >"$tmp/adopt output" 2>&1
  printf '%s\n' "$?" >"$tmp/adopt status"
) &
adopt_pid=$!
for attempt in {1..200}; do
  [[ -e "$AZEDARACH_JAEGER_TEST_ADOPT_OBSERVED_FILE" ]] && break
  sleep 0.05
done
[[ -e "$AZEDARACH_JAEGER_TEST_ADOPT_OBSERVED_FILE" ]]
primary_record="$(jaeger_write_endpoint_state localhost:4318 0 race-primary)"
: >"$AZEDARACH_JAEGER_TEST_ADOPT_CONTINUE_FILE"
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
unset AZEDARACH_JAEGER_TEST_ADOPT_OBSERVED_FILE AZEDARACH_JAEGER_TEST_ADOPT_CONTINUE_FILE
kill "$race_pid" 2>/dev/null || true
wait "$race_pid" 2>/dev/null || true
sleep 0.1
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
if jaeger_schedule_published_expiry "$fake_engine_executable" azedarach-jaeger-fallback; then
  echo "failed successor unexpectedly retained expiry ownership" >&2
  exit 1
fi
if kill -0 "$handoff_pid" 2>/dev/null; then
  echo "established expiry owner survived generation handoff" >&2
  exit 1
fi
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
sleep 30 &
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
sleep 0.1
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
sleep 0.1
jaeger_clear_worker_slot "$abandoned_slot" || true

# The ready worker survives its scheduling caller and expires the fallback.
: >"$calls"
export FAKE_EXPIRES_AT="$(( $(date +%s) + 2 ))"
JAEGER_PUBLISHED_EXPIRES_AT="$FAKE_EXPIRES_AT"
JAEGER_PUBLISHED_ENDPOINT_RECORD="$(jaeger_write_endpoint_state localhost:34318 "$JAEGER_PUBLISHED_EXPIRES_AT" azedarach-jaeger-fallback)"
(jaeger_schedule_published_expiry "$fake_engine_executable" azedarach-jaeger-fallback)
sleep 3
grep -q '^rm -f azedarach-jaeger-fallback ' "$calls"
[[ ! -e "$AZEDARACH_JAEGER_ENDPOINT_FILE" ]]
export AZEDARACH_JAEGER_DISABLE_EXPIRY_WORKER=1

echo "jaeger local lifecycle tests passed"
