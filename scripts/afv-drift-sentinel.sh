#!/usr/bin/env bash
set -euo pipefail

repo_root="$(git rev-parse --show-toplevel 2>/dev/null || true)"
if [[ -z "$repo_root" ]]; then
  printf 'FAIL: could not determine repo root. Run this from inside the repository.\n' >&2
  exit 1
fi

cd "$repo_root"

failures=0

pass() {
  printf 'PASS: %s\n' "$1"
}

fail() {
  printf 'FAIL: %s\n' "$1" >&2
  failures=$((failures + 1))
}

require_marker() {
  local marker="$1"
  local ref_file="$2"

  if rg -n -F -- "$marker" "$ref_file" >/dev/null; then
    pass "required marker present: $marker ($ref_file)"
  else
    fail "required marker missing: $marker ($ref_file)"
    printf '      Add the marker to the docs reference so the drift sentinel can verify it.\n' >&2
  fi
}

forbidden_scan() {
  local description="$1"
  local pattern="$2"
  shift 2

  if rg -n -F \
    --glob 'internal/cli/**' \
    --glob 'internal/ui/**' \
    --glob '!**/*_test.go' \
    "$pattern" "$@" >/dev/null; then
    fail "$description"
    rg -n -F \
      --glob 'internal/cli/**' \
      --glob 'internal/ui/**' \
      --glob '!**/*_test.go' \
      "$pattern" "$@" >&2 || true
    printf '      Move the ownership boundary back to the daemon/client layer.\n' >&2
  else
    pass "$description"
  fi
}

strict_app_scan() {
  local description="$1"
  local pattern="$2"
  shift 2

  if rg -n -P \
    --glob 'internal/tui/**' \
    --glob '!**/*_test.go' \
    "$pattern" "$@" >/dev/null; then
    fail "$description"
    rg -n -P \
      --glob 'internal/tui/**' \
      --glob '!**/*_test.go' \
      "$pattern" "$@" >&2 || true
    printf '      Move authority ownership back to daemon snapshot projection paths.\n' >&2
  else
    pass "$description"
  fi
}

require_marker 'worktree.cleanup_orphaned' 'docs/adr/1-daemon-ownership-adr.md'
require_marker 'task.snapshot.export' 'docs/adr/1-daemon-ownership-adr.md'
require_marker 'task.bulk.apply' 'docs/adr/1-daemon-ownership-adr.md'

forbidden_scan \
  'no direct worktree ownership imports remain in CLI/TUI paths' \
  'github.com/riordanpawley/azedarach/internal/services/worktree'

forbidden_scan \
  'no direct tmux ownership imports remain in CLI/TUI paths' \
  'github.com/riordanpawley/azedarach/internal/services/tmux'

forbidden_scan \
  'no direct devserver ownership imports remain in CLI/TUI paths' \
  'github.com/riordanpawley/azedarach/internal/services/devserver'

forbidden_scan \
  'no direct PR ownership imports remain in CLI/TUI paths' \
  'github.com/riordanpawley/azedarach/internal/services/pr'

strict_app_scan \
  'no internal/tui/model.go local session monitor starts' \
  'sessionMonitor\.Start\(' \
  internal/tui/model.go

strict_app_scan \
  'no internal/tui/model.go direct session projection writes' \
  '^\s*m\.sessions\[[^]]+\]\s*=' \
  internal/tui/model.go

strict_app_scan \
  'no internal/tui/model.go direct session projection deletes' \
  'delete\(m\.sessions,' \
  internal/tui/model.go

if (( failures > 0 )); then
  printf 'Drift sentinel failed: %d check(s) failed.\n' "$failures" >&2
  exit 1
fi

printf 'Drift sentinel passed: %d checks verified.\n' 10
