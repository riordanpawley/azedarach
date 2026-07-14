#!/usr/bin/env bash

set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
source "$repo_root/scripts/jaeger-local.sh"

tmp="$(mktemp -d)"
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
        *expires_at*) echo "$((now + 3600))" ;;
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

export AZEDARACH_CONTAINER_ENGINE=fake_engine
export AZEDARACH_JAEGER_ENDPOINT_FILE="$tmp/endpoint"
export AZEDARACH_JAEGER_STARTUP_GRACE_SECONDS=0

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

echo "jaeger local lifecycle tests passed"
