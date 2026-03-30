#!/usr/bin/env bash
set -euo pipefail

no_run=0
if [[ "${1:-}" == "--no-run" ]]; then
  no_run=1
  shift
fi

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo_root"

mkdir -p bin
go build -o bin/az ./cmd/az
go build -o bin/azd ./cmd/azd

choose_link_dir() {
  if [ -n "${AZ_LINK_DIR:-}" ]; then
    mkdir -p "$AZ_LINK_DIR"
    echo "$AZ_LINK_DIR"
    return 0
  fi

  if command -v az >/dev/null 2>&1; then
    local current_dir
    current_dir="$(dirname "$(command -v az)")"
    if [ -w "$current_dir" ]; then
      echo "$current_dir"
      return 0
    fi
  fi

  if command -v brew >/dev/null 2>&1; then
    local brew_bin
    brew_bin="$(brew --prefix)/bin"
    if [ -w "$brew_bin" ]; then
      echo "$brew_bin"
      return 0
    fi
  fi

  mkdir -p "$HOME/.local/bin"
  echo "$HOME/.local/bin"
}

link_dir="$(choose_link_dir)"
ln -sf "$repo_root/bin/az" "$link_dir/az"
ln -sf "$repo_root/bin/azd" "$link_dir/azd"

echo "Linked az -> $link_dir/az"
echo "Linked azd -> $link_dir/azd"
if [[ "$no_run" -eq 1 ]]; then
  echo "Skipping run (--no-run)"
  exit 0
fi
echo "Running az..."
exec "$repo_root/bin/az"
