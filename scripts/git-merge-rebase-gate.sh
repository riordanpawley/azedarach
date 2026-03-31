#!/usr/bin/env sh
set -eu

if [ "${AZEDARACH_SKIP_MERGE_REBASE_GATE:-0}" = "1" ]; then
  echo "merge/rebase gate skipped (AZEDARACH_SKIP_MERGE_REBASE_GATE=1)"
  exit 0
fi

repo_root="$(git rev-parse --show-toplevel)"
cd "$repo_root"

echo "[gate] running build check (go build ./...)"
go build ./...

echo "[gate] running test check (go test ./...)"
go test ./...

echo "[gate] build+tests passed"

