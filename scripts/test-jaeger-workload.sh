#!/usr/bin/env bash

set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
source "$repo_root/scripts/jaeger-local.sh"

engine="$(jaeger_choose_engine)" || {
  echo "docker/podman not found; cannot run Jaeger workload test" >&2
  exit 1
}
command -v curl >/dev/null 2>&1 || {
  echo "curl not found; cannot run Jaeger workload test" >&2
  exit 1
}
command -v ruby >/dev/null 2>&1 || {
  echo "ruby not found; cannot generate Jaeger workload" >&2
  exit 1
}

name="azedarach-jaeger-workload-$$"
expiry_name="${name}-expiry"
tmp="$(mktemp -d)"
export AZEDARACH_JAEGER_LIFECYCLE_LOCK_FILE="$tmp/lifecycle-lock"
cleanup() {
  "$engine" rm -f "$name" >/dev/null 2>&1 || true
  "$engine" rm -f "$expiry_name" >/dev/null 2>&1 || true
  rm -rf "$tmp"
}
trap cleanup EXIT

export AZEDARACH_JAEGER_CONTAINER="$name"
export AZEDARACH_JAEGER_ENDPOINT_FILE="$tmp/endpoint"
export AZEDARACH_JAEGER_MAX_TRACES=40000
export AZEDARACH_JAEGER_MEMORY=1g
export AZEDARACH_JAEGER_MEMORY_LIMIT_MIB=768

jaeger_start "$engine" "$name" 0 memory
ui_port="$(jaeger_host_port "$engine" "$name" 16686)"
otlp_port="$(jaeger_host_port "$engine" "$name" 4318)"

ruby -rjson -e '
  capacity = Integer(ARGV.fetch(0))
  high_span_count = Integer(ARGV.fetch(1))
  now = (Time.now.to_r * 1_000_000_000).to_i
  sentinel_trace_id = "a1" * 16
  high_span_trace_id = "d1" * 16
  spans = Array.new(capacity - 1) do |index|
    {
      traceId: index.zero? ? sentinel_trace_id : format("%032x", index + 1),
      spanId: format("%016x", index + 1),
      name: "capacity-root-workload",
      kind: 1,
      startTimeUnixNano: (now + index).to_s,
      endTimeUnixNano: (now + index + 1_000).to_s
    }
  end
  spans.concat(Array.new(high_span_count) do |index|
    {
      traceId: high_span_trace_id,
      spanId: format("%016x", capacity + index),
      name: "high-span-query-workload",
      kind: 1,
      startTimeUnixNano: (now + capacity + index).to_s,
      endTimeUnixNano: (now + capacity + index + 1_000).to_s
    }
  end)
  payload = {
    resourceSpans: [{
      resource: {attributes: [{key: "service.name", value: {stringValue: "az-jaeger-workload"}}]},
      scopeSpans: [{scope: {name: "djn-workload"}, spans: spans}]
    }]
  }
  File.write(ARGV.fetch(2), JSON.generate(payload))
' 40000 15000 "$tmp/traces.json"

curl --fail --silent --show-error --max-time 60 \
  --retry 10 --retry-connrefused --retry-delay 1 \
  -H 'Content-Type: application/json' \
  --data-binary "@$tmp/traces.json" \
  "http://localhost:${otlp_port}/v1/traces" >/dev/null

# Poll for the batch exporter to commit before asserting the full trace size.
for attempt in 1 2 3 4 5 6 7 8 9 10; do
  curl --fail --silent --show-error --max-time 60 \
    --retry 3 --retry-connrefused --retry-delay 1 \
    --get "http://localhost:${ui_port}/api/traces" \
    --data-urlencode service=az-jaeger-workload \
    --data-urlencode operation=high-span-query-workload \
    --data-urlencode limit=10 \
    --data-urlencode lookback=15m >"$tmp/query.json"
  if ruby -rjson -e 'exit(JSON.parse(File.read(ARGV.fetch(0))).fetch("data").empty? ? 1 : 0)' "$tmp/query.json"; then
    break
  fi
  sleep 1
done

ruby -rjson -e '
  response = JSON.parse(File.read(ARGV.fetch(0)))
  traces = response.fetch("data")
  abort "query returned no traces" if traces.empty?
  count = traces.fetch(0).fetch("spans").length
  abort "query returned #{count} spans, want 15000" unless count == 15000
' "$tmp/query.json"

# Filling the configured capacity with distinct traces must not evict the first
# trace, and the supported query shape remains bounded to ten results.
curl --fail --silent --show-error --max-time 60 \
  "http://localhost:${ui_port}/api/traces/$(printf 'a1%.0s' {1..16})" >"$tmp/sentinel.json"
ruby -rjson -e '
  traces = JSON.parse(File.read(ARGV.fetch(0))).fetch("data")
  abort "sentinel trace lookup failed at configured capacity" if traces.empty?
' "$tmp/sentinel.json"

curl --fail --silent --show-error --max-time 60 \
  --get "http://localhost:${ui_port}/api/traces" \
  --data-urlencode service=az-jaeger-workload \
  --data-urlencode operation=capacity-root-workload \
  --data-urlencode limit=10 \
  --data-urlencode lookback=15m >"$tmp/capacity-query.json"
ruby -rjson -e '
  count = JSON.parse(File.read(ARGV.fetch(0))).fetch("data").length
  abort "bounded capacity query returned #{count} traces, want 10" unless count == 10
' "$tmp/capacity-query.json"

effective_limit="$(jaeger_container_trace_limit "$engine" "$name")"
[[ "$effective_limit" == "40000" ]] || {
  echo "effective trace limit is $effective_limit, want 40000" >&2
  exit 1
}
memory_usage="$($engine stats --no-stream --format '{{.MemUsage}}' "$name" 2>/dev/null || true)"
echo "Jaeger capacity diagnostics: effective_trace_limit=$effective_limit stored_traces=40000 memory_usage=${memory_usage:-unavailable}"

jaeger_running "$engine" "$name" || {
  echo "Jaeger exited during the high-span query" >&2
  exit 1
}
jaeger_oom_killed "$engine" "$name" && {
  echo "Jaeger was OOM-killed during the high-span query" >&2
  exit 1
}

# Exercise the real detached TTL owner and endpoint-generation invalidation.
"$engine" rm -f "$name" >/dev/null 2>&1 || true
export AZEDARACH_JAEGER_FALLBACK_TTL_SECONDS=2
jaeger_start "$engine" "$expiry_name" 0 memory
jaeger_publish_env "$engine" "$expiry_name" >/dev/null
jaeger_schedule_published_expiry "$engine" "$expiry_name"
for attempt in 1 2 3 4 5 6 7 8 9 10; do
  if ! "$engine" inspect "$expiry_name" >/dev/null 2>&1; then
    break
  fi
  sleep 1
done
if "$engine" inspect "$expiry_name" >/dev/null 2>&1; then
  echo "expired Jaeger fallback was not removed" >&2
  exit 1
fi
if [[ -e "$AZEDARACH_JAEGER_ENDPOINT_FILE" ]]; then
  echo "expired Jaeger fallback endpoint remained readable" >&2
  exit 1
fi

echo "Jaeger 40,000-trace and high-span query workload passed under the 1 GiB container limit"
