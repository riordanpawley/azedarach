#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=./g5-validation.sh
source "$SCRIPT_DIR/g5-validation.sh"

pass_count=0

assert_equals() {
  local got="$1"
  local want="$2"
  local msg="$3"
  if [[ "$got" != "$want" ]]; then
    echo "FAIL: $msg"
    echo "  got:  $got"
    echo "  want: $want"
    exit 1
  fi
}

assert_nonzero() {
  local code="$1"
  local msg="$2"
  if [[ "$code" -eq 0 ]]; then
    echo "FAIL: $msg"
    exit 1
  fi
}

run_test() {
  local name="$1"
  "$name"
  pass_count=$((pass_count + 1))
  echo "PASS: $name"
}

test_collect_must_fr_ids_filters_should_entries() {
  local tmp
  tmp="$(mktemp -d)"

  cat >"$tmp/fr.md" <<'FR'
## 4.44 E2E Testability Requirements
- AZ-FR-4101: Acceptance scenarios MUST be deterministic.
- AZ-FR-4102: The spec MUST define fixture profiles.
- AZ-FR-4107: E2E suite SHOULD include visual snapshot assertions.
- AZ-FR-4108: E2E suite MUST include performance assertions.

## 4.45 Another Section
- AZ-FR-4201: The CLI MUST expose az init.
FR

  local got
  got="$(collect_must_fr_ids "$tmp/fr.md" | tr '\n' ' ' | xargs)"
  assert_equals "$got" "AZ-FR-4101 AZ-FR-4102 AZ-FR-4108" "collect_must_fr_ids should only include MUST IDs in 4.44"
  rm -rf "$tmp"
}

test_validate_traceability_passes_with_expected_links() {
  local tmp
  tmp="$(mktemp -d)"

  cat >"$tmp/fr.md" <<'FR'
## 4.44 E2E Testability Requirements
- AZ-FR-4101: Unit harness MUST execute deterministically for repeated runs.
- AZ-FR-4104: Test profile MUST define terminal dimensions.
- AZ-FR-4105: Test profile MUST define base-branch variability.
- AZ-FR-4106: Every MUST requirement in this section set MUST map.
- AZ-FR-4108: E2E suite MUST include performance assertions.

## 4.45 Another Section
FR

  cat >"$tmp/at.md" <<'AT'
## 6.30 E2E Testability Meta Acceptance
### AZ-AT-2801 MUST requirements have acceptance coverage
- Links: AZ-FR-4101, AZ-FR-4106.

### AZ-AT-2803 Test profile covers terminal and base-branch variance
- Links: AZ-FR-4104, AZ-FR-4105.

### AZ-AT-2805 Performance thresholds on critical paths
- Links: AZ-FR-4108.

## 6.31 Another Section
AT

  validate_traceability "$tmp/fr.md" "$tmp/at.md"
  rm -rf "$tmp"
}

test_validate_traceability_fails_when_required_fr_4101_missing_in_spec() {
  local tmp
  tmp="$(mktemp -d)"

  cat >"$tmp/fr.md" <<'FR'
## 4.44 E2E Testability Requirements
- AZ-FR-4104: Test profile MUST define terminal dimensions.
- AZ-FR-4105: Test profile MUST define base-branch variability.
- AZ-FR-4106: Every MUST requirement in this section set MUST map.
- AZ-FR-4108: E2E suite MUST include performance assertions.

## 4.45 Another Section
FR

  cat >"$tmp/at.md" <<'AT'
## 6.30 E2E Testability Meta Acceptance
### AZ-AT-2801 MUST requirements have acceptance coverage
- Links: AZ-FR-4101, AZ-FR-4106.

### AZ-AT-2803 Test profile covers terminal and base-branch variance
- Links: AZ-FR-4104, AZ-FR-4105.

### AZ-AT-2805 Performance thresholds on critical paths
- Links: AZ-FR-4108.

## 6.31 Another Section
AT

  set +e
  validate_traceability "$tmp/fr.md" "$tmp/at.md" >/dev/null 2>&1
  local rc=$?
  set -e

  assert_nonzero "$rc" "validate_traceability should fail when AZ-FR-4101 is missing from functional requirements spec"
  rm -rf "$tmp"
}

test_validate_traceability_fails_when_mapping_missing() {
  local tmp
  tmp="$(mktemp -d)"

  cat >"$tmp/fr.md" <<'FR'
## 4.44 E2E Testability Requirements
- AZ-FR-4104: Test profile MUST define terminal dimensions.
- AZ-FR-4105: Test profile MUST define base-branch variability.

## 4.45 Another Section
FR

  cat >"$tmp/at.md" <<'AT'
## 6.30 E2E Testability Meta Acceptance
### AZ-AT-2803 Test profile covers terminal and base-branch variance
- Links: AZ-FR-4104.

## 6.31 Another Section
AT

  set +e
  validate_traceability "$tmp/fr.md" "$tmp/at.md" >/dev/null 2>&1
  local rc=$?
  set -e

  assert_nonzero "$rc" "validate_traceability should fail when a MUST FR has no mapped acceptance link"
  rm -rf "$tmp"
}

test_extract_ns_per_op_parses_benchmark_output() {
  local bench_out
  bench_out=$'BenchmarkParseWorktreeList-10 \t 1851180\t 892.5 ns/op\t 336 B/op\t 3 allocs/op\nPASS'
  local got
  got="$(extract_ns_per_op "$bench_out" "BenchmarkParseWorktreeList")"
  assert_equals "$got" "892.5" "extract_ns_per_op should parse ns/op value"
}

run_test test_collect_must_fr_ids_filters_should_entries
run_test test_validate_traceability_passes_with_expected_links
run_test test_validate_traceability_fails_when_required_fr_4101_missing_in_spec
run_test test_validate_traceability_fails_when_mapping_missing
run_test test_extract_ns_per_op_parses_benchmark_output

echo "All tests passed ($pass_count)."
