#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd -- "$SCRIPT_DIR/.." && pwd)"

FR_SPEC_FILE="${G5_FR_SPEC_FILE:-$REPO_ROOT/../docs/spec/04-functional-requirements.md}"
AT_SPEC_FILE="${G5_AT_SPEC_FILE:-$REPO_ROOT/../docs/spec/06-acceptance-catalog.md}"
PERF_BENCH_PKG="${G5_PERF_BENCH_PKG:-./internal/services/git}"
PERF_BENCH_NAME="${G5_PERF_BENCH_NAME:-BenchmarkParseWorktreeList}"
PERF_MAX_NS_PER_OP="${G5_PERF_MAX_NS_PER_OP:-50000}"

log() {
  printf '%s\n' "$*"
}

fail() {
  printf 'ERROR: %s\n' "$*" >&2
  return 1
}

require_command() {
  local cmd="$1"
  command -v "$cmd" >/dev/null 2>&1 || fail "required command not found: $cmd"
}

extract_fr_ids_from_line() {
  local line="$1"
  printf '%s\n' "$line" | awk '
    {
      while (match($0, /AZ-FR-[0-9]{4}/)) {
        print substr($0, RSTART, RLENGTH)
        $0 = substr($0, RSTART + RLENGTH)
      }
    }
  '
}

collect_must_fr_ids() {
  local fr_file="$1"
  awk '
    /^## 4\.44 E2E Testability Requirements/ { in_section=1; next }
    in_section && /^## / { in_section=0 }
    in_section && /AZ-FR-[0-9]{4}:/ && / MUST / {
      if (match($0, /AZ-FR-[0-9]{4}/)) {
        print substr($0, RSTART, RLENGTH)
      }
    }
  ' "$fr_file"
}

collect_linked_fr_ids() {
  local at_file="$1"
  awk '
    /- Links:/ {
      line = $0
      while (match(line, /AZ-FR-[0-9]{4}/)) {
        print substr(line, RSTART, RLENGTH)
        line = substr(line, RSTART + RLENGTH)
      }
    }
  ' "$at_file" | sort -u
}

acceptance_links_line() {
  local at_file="$1"
  local at_id="$2"

  awk -v at_id="$at_id" '
    $0 ~ "^### " at_id " " { in_block=1; next }
    in_block && /^### / { exit }
    in_block && /- Links:/ { print; exit }
  ' "$at_file"
}

assert_acceptance_links() {
  local at_file="$1"
  local at_id="$2"
  shift 2

  local links_line
  links_line="$(acceptance_links_line "$at_file" "$at_id")"

  if [[ -z "$links_line" ]]; then
    fail "missing Links line for $at_id"
    return 1
  fi

  local fr_id
  for fr_id in "$@"; do
    if [[ "$links_line" != *"$fr_id"* ]]; then
      fail "$at_id Links line missing $fr_id"
      return 1
    fi
  done
}

validate_traceability() {
  local fr_file="$1"
  local at_file="$2"

  [[ -f "$fr_file" ]] || fail "functional requirements file not found: $fr_file"
  [[ -f "$at_file" ]] || fail "acceptance catalog file not found: $at_file"

  local required_fr_ids
  required_fr_ids="AZ-FR-4104 AZ-FR-4105 AZ-FR-4106 AZ-FR-4108"

  local fr_id
  for fr_id in $required_fr_ids; do
    rg -q "${fr_id}:" "$fr_file" || {
      fail "missing required requirement ID in spec: $fr_id"
      return 1
    }
  done

  rg -q '^### AZ-AT-2801 ' "$at_file" || {
    fail "missing acceptance scenario: AZ-AT-2801"
    return 1
  }
  rg -q '^### AZ-AT-2803 ' "$at_file" || {
    fail "missing acceptance scenario: AZ-AT-2803"
    return 1
  }
  rg -q '^### AZ-AT-2805 ' "$at_file" || {
    fail "missing acceptance scenario: AZ-AT-2805"
    return 1
  }

  assert_acceptance_links "$at_file" "AZ-AT-2801" "AZ-FR-4106"
  assert_acceptance_links "$at_file" "AZ-AT-2803" "AZ-FR-4104" "AZ-FR-4105"
  assert_acceptance_links "$at_file" "AZ-AT-2805" "AZ-FR-4108"

  local linked_ids
  linked_ids="$(collect_linked_fr_ids "$at_file")"

  local missing=0
  while IFS= read -r must_fr_id; do
    if [[ -z "$must_fr_id" ]]; then
      continue
    fi
    if ! printf '%s\n' "$linked_ids" | rg -qx "$must_fr_id"; then
      printf 'ERROR: MUST FR missing acceptance link: %s\n' "$must_fr_id" >&2
      missing=1
    fi
  done < <(collect_must_fr_ids "$fr_file")

  [[ "$missing" -eq 0 ]] || return 1
}

validate_profile_variance() {
  local narrow_golden="$REPO_ROOT/internal/ui/board/testdata/narrow_terminal.golden"
  local standard_golden="$REPO_ROOT/internal/ui/board/testdata/default_cursor_at_origin.golden"

  [[ -f "$narrow_golden" ]] || fail "missing narrow baseline golden file: $narrow_golden"
  [[ -f "$standard_golden" ]] || fail "missing standard baseline golden file: $standard_golden"

  (
    cd "$REPO_ROOT"
    go test ./internal/ui/board -run 'TestRender/(default_cursor_at_origin|narrow_terminal)$' -count=1
    go test ./internal/ui/overlay -run '^TestPRCreateOverlaySubmitWithBody$' -count=1
  )
}

extract_ns_per_op() {
  local benchmark_output="$1"
  local bench_name="$2"

  printf '%s\n' "$benchmark_output" | awk -v bench_name="$bench_name" '
    $1 ~ bench_name {
      for (i = 1; i <= NF; i++) {
        if ($i == "ns/op") {
          print $(i - 1)
          exit
        }
      }
    }
  '
}

validate_performance_budget() {
  local bench_output
  bench_output="$(
    cd "$REPO_ROOT"
    go test "$PERF_BENCH_PKG" -run '^$' -bench "^${PERF_BENCH_NAME}$" -benchmem -count=1
  )"

  local ns_per_op
  ns_per_op="$(extract_ns_per_op "$bench_output" "$PERF_BENCH_NAME")"

  if [[ -z "$ns_per_op" ]]; then
    printf '%s\n' "$bench_output" >&2
    fail "unable to parse ns/op for benchmark ${PERF_BENCH_NAME}"
    return 1
  fi

  if ! awk -v actual="$ns_per_op" -v budget="$PERF_MAX_NS_PER_OP" 'BEGIN { exit (actual <= budget) ? 0 : 1 }'; then
    printf '%s\n' "$bench_output" >&2
    fail "performance budget exceeded for ${PERF_BENCH_NAME}: ${ns_per_op} ns/op > ${PERF_MAX_NS_PER_OP} ns/op"
    return 1
  fi

  log "performance gate: ${PERF_BENCH_NAME} ${ns_per_op} ns/op <= ${PERF_MAX_NS_PER_OP} ns/op"
}

main() {
  require_command rg
  require_command go

  log "== G5 traceability gate (AZ-AT-2801 / AZ-FR-4106) =="
  validate_traceability "$FR_SPEC_FILE" "$AT_SPEC_FILE"

  log "== G5 profile variance gate (AZ-AT-2803 / AZ-FR-4104, AZ-FR-4105) =="
  validate_profile_variance

  log "== G5 performance budget gate (AZ-AT-2805 / AZ-FR-4108) =="
  validate_performance_budget

  log "G5 validation gates passed."
}

if [[ "${BASH_SOURCE[0]}" == "$0" ]]; then
  main "$@"
fi
