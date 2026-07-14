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
  local engine="$1" name="$2" ui_port otlp_port expires endpoint_record
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
  if (( expires > 0 )) && jaeger_adopt_expiry_owner "$engine" "$name" "$expires" "localhost:${otlp_port}"; then
    endpoint_record="$JAEGER_READY_RECORD"
  elif ! endpoint_record="$(jaeger_write_endpoint_state "localhost:${otlp_port}" "$expires" "$name")"; then
    echo "Warning: could not persist the managed Jaeger endpoint for later commands" >&2
    endpoint_record=""
  fi
  JAEGER_PUBLISHED_ENDPOINT_RECORD="$endpoint_record"
  JAEGER_PUBLISHED_EXPIRES_AT="$expires"
  echo "Jaeger ready: http://localhost:${ui_port} (OTLP ${OTEL_EXPORTER_OTLP_TRACES_ENDPOINT})"
}

jaeger_link_endpoint_record() {
  local record="$1" target link_tmp previous
  jaeger_endpoint_record_valid "$record" || return 1
  target="$(jaeger_endpoint_file)" || return 1
  link_tmp="${target}.link.$$.$RANDOM"
  previous="$(readlink "$target" 2>/dev/null || true)"
  ln -s "$record" "$link_tmp" || return 1
  mv -f "$link_tmp" "$target" || { rm -f "$link_tmp"; return 1; }
  if [[ -n "$previous" && "$previous" != "$record" ]]; then
    jaeger_clear_endpoint_record "$previous" || true
  fi
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
  local endpoint="$1" expires="$2" generation="${3:-$$.$RANDOM}"
  local target dir record_dir record tmp link_tmp previous generation_key
  target="$(jaeger_endpoint_file)" || return 1
  dir="$(dirname "$target")"
  mkdir -p "$dir" || return 1
  chmod 0700 "$dir" 2>/dev/null || true
  record_dir="${target}.d"
  mkdir -p "$record_dir" || return 1
  chmod 0700 "$record_dir" 2>/dev/null || true
  generation_key="$(printf '%s' "${generation}:${expires}" | cksum | awk '{print $1}')" || return 1
  record="${record_dir}/${expires}.${generation_key}"
  tmp="${record}.tmp"
  link_tmp="${target}.link.$$.$RANDOM"
  umask 077
  printf '%s\n%s\n' "$endpoint" "$expires" >"$tmp" || return 1
  mv -f "$tmp" "$record" || { rm -f "$tmp"; return 1; }
  previous="$(readlink "$target" 2>/dev/null || true)"
  ln -s "$record" "$link_tmp" || { rm -f "$record"; return 1; }
  mv -f "$link_tmp" "$target" || { rm -f "$link_tmp" "$record"; return 1; }
  if [[ -n "$previous" && "$previous" != "$record" ]]; then
    jaeger_clear_endpoint_record "$previous" || true
  fi
  echo "$record"
}

jaeger_clear_endpoint_record() {
  local record="$1" target record_dir suffix
  [[ -n "$record" ]] || return 0
  target="$(jaeger_endpoint_file)" || return 1
  record_dir="${target}.d/"
  suffix="${record#"$record_dir"}"
  case "$record" in
    "$record_dir"*)
      [[ -n "$suffix" && "$suffix" != */* ]] || return 1
      rm -f "$record"
      ;;
    *) echo "Warning: refusing to clear endpoint record outside $record_dir" >&2; return 1 ;;
  esac
}

jaeger_schedule_published_expiry() {
  local engine="$1" name="$2" expires="${JAEGER_PUBLISHED_EXPIRES_AT:-0}"
  local record="${JAEGER_PUBLISHED_ENDPOINT_RECORD:-}" target worker_dir worker_key slot pid worker_shell nonce
  [[ "$expires" =~ ^[0-9]+$ ]] && (( expires > 0 )) || return 0
  [[ "${AZEDARACH_JAEGER_DISABLE_EXPIRY_WORKER:-0}" != "1" ]] || return 0
  target="$(jaeger_endpoint_file)" || return 1
  worker_dir="${target}.workers"
  mkdir -p "$worker_dir" || return 1
  chmod 0700 "$worker_dir" 2>/dev/null || true
  worker_key="$(printf '%s' "${engine}:${name}:${expires}" | cksum | awk '{print $1}')" || return 1
  slot="${worker_dir}/${worker_key}"
  if [[ -d "$slot" ]]; then
    if jaeger_wait_expiry_ready "$slot" "$engine" "$name" "$expires" "$record"; then
      return 0
    fi
    if ! jaeger_discard_worker_slot "$slot" "$name" "$expires"; then
      jaeger_abort_fallback_expiry "$engine" "$name" "$expires" "$record" "$slot"
      return 1
    fi
    if ! jaeger_endpoint_record_valid "$record" || ! jaeger_running "$engine" "$name"; then
      jaeger_abort_fallback_expiry "$engine" "$name" "$expires" "$record" "$slot"
      return 1
    fi
  fi
  nonce="$(printf '%s' "$$:${RANDOM}:$(date +%s%N 2>/dev/null || date +%s)" | cksum | awk '{print $1}')" || return 1
  if ! mkdir "$slot" 2>/dev/null; then
    if jaeger_wait_expiry_ready "$slot" "$engine" "$name" "$expires" "$record"; then
      return 0
    fi
    jaeger_abort_fallback_expiry "$engine" "$name" "$expires" "$record" "$slot"
    return 1
  fi
  worker_shell="${AZEDARACH_JAEGER_EXPIRY_SHELL:-bash}"
  nohup "$worker_shell" "$jaeger_script_dir/jaeger-local.sh" expiry-worker \
    "$engine" "$name" "$expires" "$record" "$slot" "$nonce" </dev/null >/dev/null 2>&1 &
  pid=$!
  if jaeger_wait_expiry_ready "$slot" "$engine" "$name" "$expires" "$record" "$nonce"; then
    return 0
  fi
  kill "$pid" 2>/dev/null || true
  jaeger_abort_fallback_expiry "$engine" "$name" "$expires" "$record" "$slot"
  return 1
}

jaeger_require_published_expiry() {
  local engine="$1" name="$2" record="${JAEGER_PUBLISHED_ENDPOINT_RECORD:-}"
  if jaeger_schedule_published_expiry "$engine" "$name"; then
    return 0
  fi
  jaeger_remove_matching_fallback "$engine" "$name" "${JAEGER_PUBLISHED_EXPIRES_AT:-0}"
  jaeger_clear_endpoint_record "$record" || true
  return 1
}

jaeger_discard_worker_slot() {
  local slot="$1" name="$2" expires="$3" pid start nonce engine ready_name ready_expires record ready_slot attempt
  pid="$(sed -n '1p' "$slot/ready" 2>/dev/null || true)"
  start="$(sed -n '2p' "$slot/ready" 2>/dev/null || true)"
  nonce="$(sed -n '3p' "$slot/ready" 2>/dev/null || true)"
  engine="$(sed -n '4p' "$slot/ready" 2>/dev/null || true)"
  ready_name="$(sed -n '5p' "$slot/ready" 2>/dev/null || true)"
  ready_expires="$(sed -n '6p' "$slot/ready" 2>/dev/null || true)"
  record="$(sed -n '7p' "$slot/ready" 2>/dev/null || true)"
  ready_slot="$(sed -n '8p' "$slot/ready" 2>/dev/null || true)"
  if [[ "$ready_name" == "$name" && "$ready_expires" == "$expires" && "$ready_slot" == "$slot" ]] &&
    jaeger_expiry_worker_alive "$pid" "$start" "$nonce" "$engine" "$name" "$expires" "$record" "$slot"; then
    printf '%s\n' "$nonce" >"$slot/disarm"
    kill "$pid" 2>/dev/null || true
    wait "$pid" 2>/dev/null || true
    for attempt in {1..40}; do
      kill -0 "$pid" 2>/dev/null || break
      sleep 0.05
    done
    kill -0 "$pid" 2>/dev/null && return 1
  fi
  jaeger_clear_worker_slot "$slot" || true
}

jaeger_expiry_worker_alive() {
  local pid="$1" start="$2" nonce="$3" engine="$4" name="$5" expires="$6" record="$7" slot="$8"
  local command actual_start
  [[ "$pid" =~ ^[0-9]+$ ]] && kill -0 "$pid" 2>/dev/null || return 1
  [[ -n "$nonce" && -n "$engine" && -n "$record" && -n "$slot" ]] || return 1
  actual_start="$(ps -p "$pid" -o lstart= 2>/dev/null | sed 's/^[[:space:]]*//;s/[[:space:]]*$//' || true)"
  [[ -n "$actual_start" && "$actual_start" == "$start" ]] || return 1
  command="$(ps -p "$pid" -o command= 2>/dev/null || true)"
  [[ "$command" == *"jaeger-local.sh expiry-worker"* &&
    "$command" == *" $engine $name $expires $record $slot $nonce"* ]]
}

jaeger_wait_expiry_ready() {
  local slot="$1" engine="$2" name="$3" expires="$4" record="${5:-}" nonce="${6:-}"
  local attempt=1 attempts="${AZEDARACH_JAEGER_EXPIRY_READY_ATTEMPTS:-40}"
  local ready pid ready_start ready_nonce ready_engine ready_name ready_expires ready_record ready_slot
  ready="$slot/ready"
  while (( attempt <= attempts )); do
    if [[ -f "$ready" ]]; then
      pid="$(sed -n '1p' "$ready" 2>/dev/null || true)"
      ready_start="$(sed -n '2p' "$ready" 2>/dev/null || true)"
      ready_nonce="$(sed -n '3p' "$ready" 2>/dev/null || true)"
      ready_engine="$(sed -n '4p' "$ready" 2>/dev/null || true)"
      ready_name="$(sed -n '5p' "$ready" 2>/dev/null || true)"
      ready_expires="$(sed -n '6p' "$ready" 2>/dev/null || true)"
      ready_record="$(sed -n '7p' "$ready" 2>/dev/null || true)"
      ready_slot="$(sed -n '8p' "$ready" 2>/dev/null || true)"
      if [[ "$ready_engine" == "$engine" && "$ready_name" == "$name" &&
        "$ready_expires" == "$expires" && "$ready_slot" == "$slot" &&
        ( -z "$record" || "$ready_record" == "$record" ) &&
        ( -z "$nonce" || "$ready_nonce" == "$nonce" ) ]] &&
        jaeger_expiry_worker_alive "$pid" "$ready_start" "$ready_nonce" "$ready_engine" "$ready_name" "$ready_expires" "$ready_record" "$ready_slot"; then
        JAEGER_READY_RECORD="$ready_record"
        JAEGER_READY_PID="$pid"
        JAEGER_READY_NONCE="$ready_nonce"
        return 0
      fi
      return 1
    fi
    sleep 0.05
    attempt=$((attempt + 1))
  done
  return 1
}

jaeger_adopt_expiry_owner() {
  local engine="$1" name="$2" expires="$3" endpoint="$4" target worker_dir worker_key slot record_endpoint record_expires
  [[ "${AZEDARACH_JAEGER_DISABLE_EXPIRY_WORKER:-0}" != "1" ]] || return 1
  [[ "$("$engine" inspect -f '{{index .Config.Labels "azedarach.jaeger.fallback"}}' "$name" 2>/dev/null || true)" == "true" ]] || return 1
  [[ "$("$engine" inspect -f '{{index .Config.Labels "azedarach.jaeger.expires_at"}}' "$name" 2>/dev/null || true)" == "$expires" ]] || return 1
  target="$(jaeger_endpoint_file)" || return 1
  worker_dir="${target}.workers"
  worker_key="$(printf '%s' "${engine}:${name}:${expires}" | cksum | awk '{print $1}')" || return 1
  slot="${worker_dir}/${worker_key}"
  [[ -d "$slot" ]] || return 1
  jaeger_wait_expiry_ready "$slot" "$engine" "$name" "$expires" || return 1
  record_endpoint="$(sed -n '1p' "$JAEGER_READY_RECORD" 2>/dev/null || true)"
  record_expires="$(sed -n '2p' "$JAEGER_READY_RECORD" 2>/dev/null || true)"
  [[ "$record_endpoint" == "$endpoint" && "$record_expires" == "$expires" ]] || return 1
  jaeger_link_endpoint_record "$JAEGER_READY_RECORD"
}

jaeger_expiry_worker() {
  local engine="$1" name="$2" expires="$3" record="$4" slot="$5" nonce="$6"
  local ready_tmp start armed=1 disarm_nonce
  trap 'status=$?; disarm_nonce="$(sed -n "1p" "$slot/disarm" 2>/dev/null || true)"; if [[ "$armed" == "1" && "$disarm_nonce" != "$nonce" ]]; then jaeger_abort_fallback_expiry "$engine" "$name" "$expires" "$record" "$slot"; fi; exit "$status"' EXIT
  [[ "${AZEDARACH_JAEGER_EXPIRY_WORKER_FAIL_INIT:-0}" != "1" ]] || return 1
  [[ -n "$name" && "$expires" =~ ^[0-9]+$ ]] || return 1
  command -v "$engine" >/dev/null 2>&1 || return 1
  jaeger_endpoint_record_valid "$record" || return 1
  jaeger_worker_slot_valid "$slot" || return 1
  [[ "$("$engine" inspect -f '{{index .Config.Labels "azedarach.jaeger.fallback"}}' "$name" 2>/dev/null || true)" == "true" ]] || return 1
  [[ "$("$engine" inspect -f '{{index .Config.Labels "azedarach.jaeger.expires_at"}}' "$name" 2>/dev/null || true)" == "$expires" ]] || return 1
  start="$(ps -p "$$" -o lstart= 2>/dev/null | sed 's/^[[:space:]]*//;s/[[:space:]]*$//' || true)"
  [[ -n "$start" && -n "$nonce" ]] || return 1
  ready_tmp="$slot/ready.tmp.$$"
  printf '%s\n%s\n%s\n%s\n%s\n%s\n%s\n%s\n' "$$" "$start" "$nonce" "$engine" "$name" "$expires" "$record" "$slot" >"$ready_tmp" || return 1
  mv -f "$ready_tmp" "$slot/ready" || return 1
  jaeger_expire_fallback "$engine" "$name" "$expires" "$record" "$slot"
  armed=0
  trap - EXIT
}

jaeger_endpoint_record_valid() {
  local record="$1" target record_dir suffix
  target="$(jaeger_endpoint_file)" || return 1
  record_dir="${target}.d/"
  suffix="${record#"$record_dir"}"
  [[ -f "$record" && "$record" == "$record_dir"* && -n "$suffix" && "$suffix" != */* ]]
}

jaeger_worker_slot_valid() {
  local slot="$1" target worker_dir suffix
  target="$(jaeger_endpoint_file)" || return 1
  worker_dir="${target}.workers/"
  suffix="${slot#"$worker_dir"}"
  [[ -d "$slot" && "$slot" == "$worker_dir"* && -n "$suffix" && "$suffix" != */* ]]
}

jaeger_abort_fallback_expiry() {
  local engine="$1" name="$2" expires="$3" record="$4" slot="$5"
  jaeger_remove_matching_fallback "$engine" "$name" "$expires"
  jaeger_clear_endpoint_record "$record" || true
  jaeger_clear_worker_slot "$slot" || true
}

jaeger_remove_matching_fallback() {
  local engine="$1" name="$2" expires="$3"
  [[ "$("$engine" inspect -f '{{index .Config.Labels "azedarach.jaeger.fallback"}}' "$name" 2>/dev/null || true)" == "true" ]] || return 0
  [[ "$("$engine" inspect -f '{{index .Config.Labels "azedarach.jaeger.expires_at"}}' "$name" 2>/dev/null || true)" == "$expires" ]] || return 0
  "$engine" rm -f "$name" >/dev/null 2>&1 || true
}

jaeger_expire_fallback() {
  local engine="$1" name="$2" expires="$3" record="$4" slot="${5:-}" now delay
  now="$(date +%s)"
  delay=$((expires - now))
  if (( delay > 0 )); then
    sleep "$delay"
  fi
  if [[ "$("$engine" inspect -f '{{index .Config.Labels "azedarach.jaeger.fallback"}}' "$name" 2>/dev/null || true)" == "true" ]] &&
    [[ "$("$engine" inspect -f '{{index .Config.Labels "azedarach.jaeger.expires_at"}}' "$name" 2>/dev/null || true)" == "$expires" ]]; then
    jaeger_remove_matching_fallback "$engine" "$name" "$expires"
  fi
  jaeger_clear_endpoint_record "$record" || true
  jaeger_clear_worker_slot "$slot" || true
}

jaeger_clear_worker_slot() {
  local slot="$1" target worker_dir suffix
  [[ -n "$slot" ]] || return 0
  target="$(jaeger_endpoint_file)" || return 1
  worker_dir="${target}.workers/"
  suffix="${slot#"$worker_dir"}"
  case "$slot" in
    "$worker_dir"*)
      [[ -n "$suffix" && "$suffix" != */* ]] || return 1
      rm -f "$slot/ready" "$slot/disarm" "$slot"/ready.tmp.*
      rmdir "$slot" 2>/dev/null || true
      ;;
    *) return 1 ;;
  esac
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
      jaeger_publish_env "$engine" "$id" || return 1
      if ! jaeger_require_published_expiry "$engine" "$id"; then
        return 1
      fi
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
  jaeger_publish_env "$engine" "$fallback" || {
    "$engine" rm -f "$fallback" >/dev/null 2>&1 || true
    return 1
  }
  if ! jaeger_require_published_expiry "$engine" "$fallback"; then
    return 1
  fi
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
  case "${1:-inventory}" in
    inventory)
      engine="$(jaeger_choose_engine)" || { echo "docker/podman not found" >&2; exit 1; }
      jaeger_inventory "$engine"
      ;;
    cleanup)
      [[ "${2:-}" == "--confirm" ]] || { echo "Usage: $0 cleanup --confirm" >&2; exit 2; }
      engine="$(jaeger_choose_engine)" || { echo "docker/podman not found" >&2; exit 1; }
      jaeger_cleanup "$engine"
      ;;
    ensure) jaeger_ensure ;;
    expiry-worker)
      [[ "$#" == "7" ]] || { echo "invalid expiry worker arguments" >&2; exit 2; }
      jaeger_expiry_worker "$2" "$3" "$4" "$5" "$6" "$7"
      ;;
    *) echo "Usage: $0 {ensure|inventory|cleanup --confirm}" >&2; exit 2 ;;
  esac
fi
