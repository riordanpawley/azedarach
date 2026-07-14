#!/usr/bin/env bash

# Shared lifecycle for the developer Jaeger collector. This file is sourced by
# build-install-run.sh and can also be run directly for safe maintenance.

set -euo pipefail

jaeger_script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

jaeger_choose_engine() {
  if [[ -n "${AZEDARACH_CONTAINER_ENGINE:-}" ]]; then
    command -v "$AZEDARACH_CONTAINER_ENGINE" >/dev/null 2>&1 || return 1
    echo "$AZEDARACH_CONTAINER_ENGINE"
  elif command -v docker >/dev/null 2>&1; then
    echo docker
  elif command -v podman >/dev/null 2>&1; then
    echo podman
  else
    return 1
  fi
}

jaeger_host_port() {
  local engine="$1" name="$2" container_port="$3" mapping
  mapping="$("$engine" port "$name" "${container_port}/tcp" 2>/dev/null | head -n 1 || true)"
  [[ -n "$mapping" && -n "${mapping##*:}" ]] || return 1
  echo "${mapping##*:}"
}

jaeger_publish_env() {
  local engine="$1" name="$2" ui_port otlp_port expires
  ui_port="$(jaeger_host_port "$engine" "$name" 16686)" || {
    echo "Warning: Jaeger container $name has no published UI port" >&2
    return 1
  }
  otlp_port="$(jaeger_host_port "$engine" "$name" 4318)" || {
    echo "Warning: Jaeger container $name has no published OTLP HTTP port" >&2
    return 1
  }
  export OTEL_EXPORTER_OTLP_ENDPOINT="http://localhost:${otlp_port}"
  export OTEL_EXPORTER_OTLP_TRACES_ENDPOINT="http://localhost:${otlp_port}/v1/traces"
  expires="$("$engine" inspect -f '{{index .Config.Labels "azedarach.jaeger.expires_at"}}' "$name" 2>/dev/null || true)"
  [[ "$expires" =~ ^[0-9]+$ ]] || expires=0
  if ! jaeger_write_endpoint_state "localhost:${otlp_port}" "$expires"; then
    echo "Warning: could not persist the managed Jaeger endpoint for later commands" >&2
  fi
  echo "Jaeger ready: http://localhost:${ui_port} (OTLP ${OTEL_EXPORTER_OTLP_TRACES_ENDPOINT})"
}

jaeger_endpoint_file() {
  if [[ -n "${AZEDARACH_JAEGER_ENDPOINT_FILE:-}" ]]; then
    echo "$AZEDARACH_JAEGER_ENDPOINT_FILE"
  else
    [[ -n "${XDG_STATE_HOME:-${HOME:-}}" ]] || return 1
    echo "${XDG_STATE_HOME:-$HOME/.local/state}/azedarach/jaeger-otlp-endpoint"
  fi
}

jaeger_write_endpoint_state() {
  local endpoint="$1" expires="$2" target dir tmp
  target="$(jaeger_endpoint_file)" || return 1
  dir="$(dirname "$target")"
  mkdir -p "$dir"
  chmod 0700 "$dir" 2>/dev/null || true
  tmp="${target}.tmp.$$"
  umask 077
  printf '%s\n%s\n' "$endpoint" "$expires" >"$tmp"
  mv -f "$tmp" "$target"
}

jaeger_storage_type() {
  case "${AZEDARACH_JAEGER_STORAGE:-memory}" in
    memory | badger) echo "${AZEDARACH_JAEGER_STORAGE:-memory}" ;;
    *) echo "Warning: unsupported AZEDARACH_JAEGER_STORAGE=${AZEDARACH_JAEGER_STORAGE}; using memory" >&2; echo memory ;;
  esac
}

jaeger_start() {
  local engine="$1" name="$2" fixed_ports="$3" storage="$4"
  local image="${AZEDARACH_JAEGER_IMAGE:-cr.jaegertracing.io/jaegertracing/jaeger:2.19.0}"
  local memory="${AZEDARACH_JAEGER_MEMORY:-1g}"
  local max_traces="${AZEDARACH_JAEGER_MAX_TRACES:-2000}"
  local ttl="${AZEDARACH_JAEGER_BADGER_TTL:-24h}"
  local volume="${AZEDARACH_JAEGER_VOLUME:-${AZEDARACH_JAEGER_CONTAINER:-azedarach-jaeger}-data}"
  local config="/etc/jaeger/jaeger-${storage}.yaml"
  local args=(run -d --name "$name"
    --label azedarach.jaeger.managed=true
    --label "azedarach.jaeger.image=${image}"
    --label "azedarach.jaeger.primary=${AZEDARACH_JAEGER_CONTAINER:-azedarach-jaeger}"
    --label "azedarach.jaeger.storage=${storage}"
    --label "azedarach.jaeger.volume=${volume}"
    --label "azedarach.jaeger.max_traces=${max_traces}"
    --label "azedarach.jaeger.badger_ttl=${ttl}"
    -e JAEGER_MAX_TRACES="$max_traces"
    -e JAEGER_BADGER_TTL="$ttl"
    -e JAEGER_MEMORY_LIMIT_MIB="${AZEDARACH_JAEGER_MEMORY_LIMIT_MIB:-768}"
    -v "${jaeger_script_dir}/jaeger-${storage}.yaml:${config}:ro")

  if [[ "$fixed_ports" == "0" ]]; then
    args+=(--rm
      --label azedarach.jaeger.fallback=true
      --label "azedarach.jaeger.expires_at=$(($(date +%s) + ${AZEDARACH_JAEGER_FALLBACK_TTL_SECONDS:-14400}))")
  fi
  if [[ -n "$memory" && "$memory" != "0" && "$memory" != "none" ]]; then
    args+=(--memory "$memory")
  fi
  if [[ "$storage" == "badger" ]]; then
    args+=(-v "${volume}:/badger")
  fi
  if [[ "$fixed_ports" == "1" ]]; then
    args+=(-p "127.0.0.1:${AZEDARACH_JAEGER_UI_PORT:-16686}:16686"
      -p "127.0.0.1:${AZEDARACH_OTLP_HTTP_PORT:-4318}:4318")
  else
    args+=(-p 127.0.0.1::16686 -p 127.0.0.1::4318)
  fi
  "$engine" "${args[@]}" "$image" --config "$config" >/dev/null || return 1
  jaeger_wait_ready "$engine" "$name"
}

jaeger_wait_ready() {
  local engine="$1" name="$2" grace="${AZEDARACH_JAEGER_STARTUP_GRACE_SECONDS:-1}"
  if [[ "$grace" != "0" ]]; then
    sleep "$grace"
  fi
  jaeger_running "$engine" "$name" &&
    jaeger_host_port "$engine" "$name" 16686 >/dev/null &&
    jaeger_host_port "$engine" "$name" 4318 >/dev/null
}

jaeger_running() {
  [[ "$("$1" inspect -f '{{.State.Running}}' "$2" 2>/dev/null || true)" == "true" ]]
}

jaeger_oom_killed() {
  [[ "$("$1" inspect -f '{{.State.OOMKilled}}' "$2" 2>/dev/null || true)" == "true" ]]
}

jaeger_matching_fallbacks() {
  "$1" ps -aq --filter label=azedarach.jaeger.fallback=true \
    --filter "label=azedarach.jaeger.primary=$2" 2>/dev/null || true
}

jaeger_legacy_fallbacks() {
  local engine="$1" primary="$2" name remainder
  "$engine" ps -a --format '{{.Names}}' 2>/dev/null | while IFS= read -r name; do
    remainder="${name#"$primary"-}"
    [[ "$name" != "$remainder" && "$remainder" =~ ^[0-9]+$ ]] || continue
    echo "$name"
  done
}

jaeger_reclaim_fallbacks() {
  local engine="$1" primary="$2" keep_running="${3:-1}" id expires now
  now="$(date +%s)"
  while IFS= read -r id; do
    [[ -n "$id" ]] || continue
    expires="$("$engine" inspect -f '{{index .Config.Labels "azedarach.jaeger.expires_at"}}' "$id" 2>/dev/null || true)"
    if jaeger_running "$engine" "$id"; then
      if [[ "$keep_running" == "2" ]]; then
        continue
      fi
      if [[ "$keep_running" == "1" ]] && [[ "$expires" =~ ^[0-9]+$ ]] && (( expires > now )); then
        continue
      fi
    fi
    "$engine" rm -f "$id" >/dev/null 2>&1 || true
  done < <(jaeger_matching_fallbacks "$engine" "$primary")
}

jaeger_reuse_fallback() {
  local engine="$1" primary="$2" id expires now
  now="$(date +%s)"
  while IFS= read -r id; do
    [[ -n "$id" ]] || continue
    expires="$("$engine" inspect -f '{{index .Config.Labels "azedarach.jaeger.expires_at"}}' "$id" 2>/dev/null || true)"
    if jaeger_running "$engine" "$id" && [[ "$expires" =~ ^[0-9]+$ ]] && (( expires > now )); then
      jaeger_publish_env "$engine" "$id"
      return 0
    fi
  done < <(jaeger_matching_fallbacks "$engine" "$primary")
  return 1
}

jaeger_start_fallback() {
  local engine="$1" primary="$2" fallback
  fallback="${primary}-fallback"
  jaeger_reclaim_fallbacks "$engine" "$primary" 1
  if jaeger_reuse_fallback "$engine" "$primary"; then
    echo "Reusing ephemeral Jaeger fallback: $fallback" >&2
    return 0
  fi
  "$engine" rm -f "$fallback" >/dev/null 2>&1 || true
  echo "Warning: starting ephemeral Jaeger fallback with dynamic ports; traces are not persisted" >&2
  jaeger_start "$engine" "$fallback" 0 memory || return 1
  jaeger_publish_env "$engine" "$fallback"
}

jaeger_activate_primary() {
  local engine="$1" primary="$2"
  jaeger_publish_env "$engine" "$primary" || return 1
  jaeger_reclaim_fallbacks "$engine" "$primary" 0
}

jaeger_labels_match() {
  local engine="$1" name="$2" storage="$3" labels image volume
  image="${AZEDARACH_JAEGER_IMAGE:-cr.jaegertracing.io/jaegertracing/jaeger:2.19.0}"
  volume="${AZEDARACH_JAEGER_VOLUME:-${AZEDARACH_JAEGER_CONTAINER:-azedarach-jaeger}-data}"
  labels="$("$engine" inspect -f '{{range $key, $value := .Config.Labels}}{{println $key "=" $value}}{{end}}' "$name" 2>/dev/null || true)"
  grep -qx "azedarach.jaeger.managed = true" <<<"$labels" &&
    grep -qx "azedarach.jaeger.image = ${image}" <<<"$labels" &&
    grep -qx "azedarach.jaeger.storage = ${storage}" <<<"$labels" &&
    grep -qx "azedarach.jaeger.volume = ${volume}" <<<"$labels" &&
    grep -qx "azedarach.jaeger.max_traces = ${AZEDARACH_JAEGER_MAX_TRACES:-2000}" <<<"$labels" &&
    grep -qx "azedarach.jaeger.badger_ttl = ${AZEDARACH_JAEGER_BADGER_TTL:-24h}" <<<"$labels"
}

jaeger_ensure() {
  if [[ "${AZEDARACH_SKIP_JAEGER:-0}" == "1" ]]; then
    echo "Skipping Jaeger (AZEDARACH_SKIP_JAEGER=1)"
    return 0
  fi
  local engine name storage
  engine="$(jaeger_choose_engine)" || { echo "Warning: docker/podman not found; skipping Jaeger startup" >&2; return 0; }
  name="${AZEDARACH_JAEGER_CONTAINER:-azedarach-jaeger}"
  storage="$(jaeger_storage_type)"
  jaeger_reclaim_fallbacks "$engine" "$name" 1

  if "$engine" inspect "$name" >/dev/null 2>&1; then
    if jaeger_oom_killed "$engine" "$name"; then
      echo "Warning: $name was OOM-killed; preserving its store and using an ephemeral fallback" >&2
      jaeger_start_fallback "$engine" "$name" || echo "Warning: failed to start Jaeger fallback" >&2
      return 0
    fi
    if ! jaeger_labels_match "$engine" "$name" "$storage"; then
      echo "Warning: recreating $name to apply supported bounded Jaeger settings" >&2
      "$engine" rm -f "$name" >/dev/null 2>&1 || true
    elif jaeger_running "$engine" "$name"; then
      echo "Jaeger already running: $name"
      jaeger_activate_primary "$engine" "$name" || true
      return 0
    elif "$engine" start "$name" >/dev/null 2>&1; then
      if jaeger_wait_ready "$engine" "$name"; then
        jaeger_activate_primary "$engine" "$name" || true
        return 0
      fi
      "$engine" stop "$name" >/dev/null 2>&1 || true
      jaeger_start_fallback "$engine" "$name" || echo "Warning: failed to start Jaeger fallback" >&2
      return 0
    else
      jaeger_start_fallback "$engine" "$name" || echo "Warning: failed to start Jaeger fallback" >&2
      return 0
    fi
  fi

  echo "Starting Jaeger container: $name"
  if ! jaeger_start "$engine" "$name" 1 "$storage"; then
    "$engine" rm -f "$name" >/dev/null 2>&1 || true
    jaeger_start_fallback "$engine" "$name" || echo "Warning: failed to start Jaeger fallback" >&2
    return 0
  fi
  jaeger_activate_primary "$engine" "$name" || true
}

jaeger_inventory() {
  local engine="$1" primary="${AZEDARACH_JAEGER_CONTAINER:-azedarach-jaeger}" selected remainder
  selected="${AZEDARACH_JAEGER_VOLUME:-${primary}-data}"
  echo "Managed fallback containers:"
  jaeger_matching_fallbacks "$engine" "$primary"
  echo "Legacy fallback containers (running containers are never cleaned):"
  jaeger_legacy_fallbacks "$engine" "$primary"
  echo "Legacy inactive fallback volumes (selected store '$selected' is always excluded):"
  "$engine" volume ls -q 2>/dev/null | while IFS= read -r volume; do
    remainder="${volume#"$primary"-}"
    [[ "$volume" != "$remainder" && "$remainder" =~ ^[0-9]+-data$ ]] || continue
    [[ "$volume" == "$selected" ]] && continue
    if [[ -z "$("$engine" ps -aq --filter "volume=$volume" 2>/dev/null || true)" ]]; then
      echo "$volume"
    fi
  done
}

jaeger_cleanup() {
  local engine="$1" primary="${AZEDARACH_JAEGER_CONTAINER:-azedarach-jaeger}" selected volume remainder
  selected="${AZEDARACH_JAEGER_VOLUME:-${primary}-data}"
  # Cleanup is intentionally conservative: even an expired running fallback
  # may still be serving a user, so only an ensure operation may replace it.
  jaeger_reclaim_fallbacks "$engine" "$primary" 2
  while IFS= read -r legacy; do
    [[ -n "$legacy" ]] || continue
    jaeger_running "$engine" "$legacy" && continue
    "$engine" rm "$legacy" >/dev/null 2>&1 || true
  done < <(jaeger_legacy_fallbacks "$engine" "$primary")
  "$engine" volume ls -q 2>/dev/null | while IFS= read -r volume; do
    remainder="${volume#"$primary"-}"
    [[ "$volume" != "$remainder" && "$remainder" =~ ^[0-9]+-data$ ]] || continue
    [[ "$volume" == "$selected" ]] && continue
    [[ -z "$("$engine" ps -aq --filter "volume=$volume" 2>/dev/null || true)" ]] || continue
    "$engine" volume rm "$volume"
  done
}

if [[ "${BASH_SOURCE[0]}" == "$0" ]]; then
  engine="$(jaeger_choose_engine)" || { echo "docker/podman not found" >&2; exit 1; }
  case "${1:-inventory}" in
    inventory) jaeger_inventory "$engine" ;;
    cleanup)
      [[ "${2:-}" == "--confirm" ]] || { echo "Usage: $0 cleanup --confirm" >&2; exit 2; }
      jaeger_cleanup "$engine"
      ;;
    ensure) jaeger_ensure ;;
    *) echo "Usage: $0 {ensure|inventory|cleanup --confirm}" >&2; exit 2 ;;
  esac
fi
