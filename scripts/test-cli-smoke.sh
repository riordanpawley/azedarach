#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"

GIT_COMMON_DIR="$(git rev-parse --path-format=absolute --git-common-dir 2>/dev/null || true)"
if [[ -n "$GIT_COMMON_DIR" && "$(basename "$GIT_COMMON_DIR")" == ".git" ]]; then
  GO_CACHE_ROOT="$(dirname "$GIT_COMMON_DIR")/.azedarach/go"
else
  GO_CACHE_ROOT="$ROOT_DIR/.azedarach/go"
fi
GOCACHE="${GOCACHE:-$GO_CACHE_ROOT/build-cache}"
GOPATH="${GOPATH:-$GO_CACHE_ROOT/path}"

TEST_ENV_DIR="$(mktemp -d)"
cleanup() {
  rm -rf "$TEST_ENV_DIR"
}
trap cleanup EXIT

export HOME="$TEST_ENV_DIR/home"
mkdir -p "$HOME" "$TEST_ENV_DIR/db"
export AZEDARACH_DB_PATH="$TEST_ENV_DIR/db/azedarach.db"

REAL_DB_PATH="$ROOT_DIR/.azedarach/azedarach.db"
real_db_before_state="missing"
if [[ -f "$REAL_DB_PATH" ]]; then
  real_db_before_state="$(cksum "$REAL_DB_PATH")"
fi

echo "[smoke] building go CLI binary"
GOCACHE="$GOCACHE" GOPATH="$GOPATH" go build -o ./bin/az ./cmd/az

assert_contains() {
  local output="$1"
  local expected="$2"
  if [[ "$output" != *"$expected"* ]]; then
    echo "[smoke] expected output to contain: $expected" >&2
    echo "[smoke] actual output:" >&2
    echo "$output" >&2
    exit 1
  fi
}

run_expect_success() {
  local label="$1"
  local expected="$2"
  shift 2
  local output
  output="$("$@" 2>&1)"
  assert_contains "$output" "$expected"
  echo "[smoke] PASS: $label"
}

run_expect_failure_contains() {
  local label="$1"
  local expected="$2"
  shift 2
  local output
  set +e
  output="$("$@" 2>&1)"
  local status=$?
  set -e
  if [[ $status -eq 0 ]]; then
    echo "[smoke] expected failure for: $label" >&2
    echo "$output" >&2
    exit 1
  fi
  assert_contains "$output" "$expected"
  echo "[smoke] PASS: $label"
}

run_expect_success "session help" "Usage: az session <start|attach|stop|status|restart-all|resolve-conflict>" ./bin/az session --help
run_expect_success "top-level help" "Usage:" ./bin/az --help
run_expect_success "top-level version" "dev" ./bin/az --version
run_expect_success "impl help usage" "az impl delete --confirm <implementation>" ./bin/az impl --help
run_expect_success "prime banner" "Azedarach Session Primer" ./bin/az prime
run_expect_success "issue help has lifecycle and bulk" "bulk-update [--project <project-id>] [--impl <implementation>] --input <path>" ./bin/az issue --help
run_expect_success "issue help has delete guard" "delete [--project <project-id>] [--id <issue-id>] [--json] [<issue-id>] --confirm" ./bin/az issue --help
run_expect_failure_contains "daemon usage guard" "Usage: az daemon <start|stop|restart>" ./bin/az daemon
run_expect_failure_contains "impl delete confirm guard" "az impl delete --confirm <implementation>" ./bin/az impl delete ts-opentui
run_expect_failure_contains "issue get usage guard" "az issue get [--project <project-id>] [--id <issue-id>] [--json] [--with-notes] [<issue-id>]" ./bin/az issue get
run_expect_failure_contains "issue delete confirm guard" "az issue delete [--project <project-id>] --confirm [--id <issue-id>] [--json] [<issue-id>]" ./bin/az issue delete az-1 --impl go-bubbletea
run_expect_failure_contains "issue bulk-create usage guard" "az issue bulk-create [--project <project-id>] [--impl <implementation>] --input <path>" ./bin/az issue bulk-create

real_db_after_state="missing"
if [[ -f "$REAL_DB_PATH" ]]; then
  real_db_after_state="$(cksum "$REAL_DB_PATH")"
fi
if [[ "$real_db_before_state" != "$real_db_after_state" ]]; then
  echo "[smoke] real repository DB was modified: $REAL_DB_PATH" >&2
  exit 1
fi

echo "[smoke] completed"
