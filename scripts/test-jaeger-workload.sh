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
export AZEDARACH_JAEGER_MAX_TRACES=20
export AZEDARACH_JAEGER_MEMORY=1g
export AZEDARACH_JAEGER_MEMORY_LIMIT_MIB=768

jaeger_start "$engine" "$name" 0 memory
ui_port="$(jaeger_host_port "$engine" "$name" 16686)"
otlp_port="$(jaeger_host_port "$engine" "$name" 4318)"

ruby -rjson -e '
  count = Integer(ARGV.fetch(0))
  now = (Time.now.to_r * 1_000_000_000).to_i
  trace_id = "d1" * 16
  spans = Array.new(count) do |index|
    {
      traceId: trace_id,
      spanId: format("%016x", index + 1),
      name: "high-span-query-workload",
      kind: 1,
      startTimeUnixNano: (now + index).to_s,
      endTimeUnixNano: (now + index + 1_000).to_s
    }
  end
  payload = {
    resourceSpans: [{
      resource: {attributes: [{key: "service.name", value: {stringValue: "az-jaeger-workload"}}]},
      scopeSpans: [{scope: {name: "djn-workload"}, spans: spans}]
    }]
  }
  File.write(ARGV.fetch(1), JSON.generate(payload))
' 15000 "$tmp/traces.json"

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

echo "Jaeger high-span workload passed under the 1 GiB container limit"
