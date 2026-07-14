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
AZ_BIN="$TEST_ENV_DIR/bin/az"
mkdir -p "$(dirname "$AZ_BIN")"

REAL_DB_PATH="$ROOT_DIR/.azedarach/azedarach.db"
real_db_before_state="missing"
if [[ -f "$REAL_DB_PATH" ]]; then
  real_db_before_state="$(cksum "$REAL_DB_PATH")"
fi

echo "[smoke] building go CLI binary"
GOCACHE="$GOCACHE" GOPATH="$GOPATH" go build -o "$AZ_BIN" ./cmd/az

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

run_expect_success "session help" "Usage: az session <start|attach|stop|status|capture|diagnose|restart-all|resolve-conflict>" "$AZ_BIN" session --help
run_expect_success "top-level help" "Usage:" "$AZ_BIN" --help
run_expect_success "top-level version" "dev" "$AZ_BIN" --version
run_expect_success "impl help usage" "az impl delete --confirm <implementation>" "$AZ_BIN" impl --help
run_expect_success "prime banner" "Azedarach Session Primer" "$AZ_BIN" prime
run_expect_success "issue help has lifecycle and bulk" "bulk-update [--project <project-id>] [--impl <implementation>] --input <path>" "$AZ_BIN" issue --help
run_expect_success "issue help has delete guard" "delete [--project <project-id>] [--id <ticket-id>] [--json] [<ticket-id>] --confirm" "$AZ_BIN" issue --help
run_expect_failure_contains "daemon usage guard" "Usage: az daemon <start|stop|restart|watch-clients>" "$AZ_BIN" daemon
run_expect_failure_contains "impl delete confirm guard" "az impl delete --confirm <implementation>" "$AZ_BIN" impl delete ts-opentui
run_expect_failure_contains "issue get usage guard" "az ticket get [--project <project-id>] [--id <ticket-id>] [--json] [--with-notes]" "$AZ_BIN" issue get
run_expect_failure_contains "issue delete confirm guard" "az ticket delete [--project <project-id>] --confirm [--id <ticket-id>] [--json] [<ticket-id>]" "$AZ_BIN" issue delete az-1 --impl go-bubbletea
run_expect_failure_contains "issue bulk-create usage guard" "az ticket bulk-create [--project <project-id>] [--impl <implementation>] --input <path>" "$AZ_BIN" issue bulk-create

real_db_after_state="missing"
if [[ -f "$REAL_DB_PATH" ]]; then
  real_db_after_state="$(cksum "$REAL_DB_PATH")"
fi
if [[ "$real_db_before_state" != "$real_db_after_state" ]]; then
  echo "[smoke] real repository DB was modified: $REAL_DB_PATH" >&2
  exit 1
fi

echo "[smoke] completed"
