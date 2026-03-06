#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd -- "$SCRIPT_DIR/.." && pwd)"

run_step() {
  local label="$1"
  shift

  printf '[ ] %s\n' "$label"
  "$@"
  printf '[x] %s\n' "$label"
}

main() {
  run_step "Deterministic G5 validation gates" "$SCRIPT_DIR/g5-validation.sh"
  run_step "Full Go test suite (go test ./...)" run_go_tests

  printf 'G5 completion checklist passed.\n'
}

run_go_tests() {
  (
    cd "$REPO_ROOT"
    go test ./...
  )
}

if [[ "${BASH_SOURCE[0]}" == "$0" ]]; then
  main "$@"
fi
