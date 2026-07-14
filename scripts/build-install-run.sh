#!/usr/bin/env bash
set -euo pipefail

no_run=0
if [[ "${1:-}" == "--no-run" ]]; then
  no_run=1
  shift
fi

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo_root"

git_dir="$(git rev-parse --git-dir)"
git_common_dir="$(git rev-parse --git-common-dir)"
if [[ "$git_dir" != "$git_common_dir" ]]; then
  echo "Refusing build-install-run from a linked worktree: $repo_root" >&2
  echo "Run it from the primary Azedarach worktree because it mutates user-global runtime assets." >&2
  exit 1
fi

build_dir="$(mktemp -d "${TMPDIR:-/tmp}/azedarach-install.XXXXXX")"
lock_dir=""
lock_owned=0
new_generation=""
cleanup_install() {
  rm -rf "$build_dir"
  if [[ -n "$new_generation" && -d "$new_generation" ]]; then
    rm -rf "$new_generation"
  fi
  if [[ "$lock_owned" -eq 1 && -n "$lock_dir" && -d "$lock_dir" &&
        "$(cat "$lock_dir/pid" 2>/dev/null || true)" == "$$" ]]; then
    rm -rf "$lock_dir"
  fi
}
trap cleanup_install EXIT
sha="$(git rev-parse HEAD 2>/dev/null || echo unknown)"
ldflags="-X github.com/riordanpawley/azedarach/internal/buildinfo.Version=dev -X github.com/riordanpawley/azedarach/internal/buildinfo.GitCommit=${sha}"
go build -ldflags "$ldflags" -o "$build_dir/az" ./cmd/az
go build -ldflags "$ldflags" -o "$build_dir/azd" ./cmd/azd
az_version="$("$build_dir/az" version)"
azd_version="$("$build_dir/azd" version)"
if [[ -z "$az_version" || "$az_version" != "$azd_version" ]]; then
  echo "Refusing incoherent az/azd install: version mismatch (az=$az_version, azd=$azd_version)" >&2
  exit 1
fi
atomic_replace_bin="${AZEDARACH_ATOMIC_REPLACE_BIN:-}"
if [[ -z "$atomic_replace_bin" ]]; then
  atomic_replace_bin="$build_dir/atomic-replace"
  go build -o "$atomic_replace_bin" ./cmd/atomic-replace
fi

choose_install_dir() {
  if [ -n "${AZ_INSTALL_DIR:-}" ]; then
    mkdir -p "$AZ_INSTALL_DIR"
    echo "$AZ_INSTALL_DIR"
    return 0
  fi

  # Backward compatibility for existing automation and local commands.
  if [ -n "${AZ_LINK_DIR:-}" ]; then
    mkdir -p "$AZ_LINK_DIR"
    echo "$AZ_LINK_DIR"
    return 0
  fi

  if command -v az >/dev/null 2>&1; then
    local current_dir
    current_dir="$(dirname "$(command -v az)")"
    # Skip repo-local bin so the installed command does not depend on a worktree.
    if [ "$current_dir" != "$repo_root/bin" ] && [ -w "$current_dir" ]; then
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

install_dir="$(choose_install_dir)"
lock_dir="$install_dir/.azedarach-install.lock"
acquire_install_lock() {
  local attempts=0
  while ! mkdir "$lock_dir" 2>/dev/null; do
    attempts=$((attempts + 1))
    if ((attempts >= 600)); then
      echo "Timed out waiting for paired az/azd install lock: $lock_dir" >&2
      echo "If no installer is running, remove that stale lock directory and retry." >&2
      return 1
    fi
    sleep 0.05
  done
  printf '%s\n' "$$" >"$lock_dir/pid"
  lock_owned=1
}

atomic_replace() {
  local source="$1"
  local destination="$2"
  "$atomic_replace_bin" "$source" "$destination"
}

atomic_symlink() {
  local target="$1"
  local destination="$2"
  local temporary
  temporary="$(mktemp "${destination}.tmp.XXXXXX")"
  rm -f "$temporary"
  ln -s "$target" "$temporary"
  if ! atomic_replace "$temporary" "$destination"; then
    rm -f "$temporary"
    return 1
  fi
}

public_links_are_managed() {
  [[ -L "$install_dir/az" ]] &&
    [[ "$(readlink "$install_dir/az")" == ".azedarach-current/az" ]] &&
    [[ -L "$install_dir/azd" ]] &&
    [[ "$(readlink "$install_dir/azd")" == ".azedarach-current/azd" ]] &&
    [[ -L "$install_dir/.azedarach-current" ]]
}

migrate_public_links() {
  local generations_dir="$1"
  local previous_generation binary
  previous_generation="$(mktemp -d "$generations_dir/generation.previous.XXXXXX")"

  if [[ -e "$install_dir/.azedarach-current" || -L "$install_dir/.azedarach-current" ]]; then
    if [[ ! -L "$install_dir/.azedarach-current" ]]; then
      echo "Refusing to replace non-symlink install control path: $install_dir/.azedarach-current" >&2
      rm -rf "$previous_generation"
      return 1
    fi
  fi

  for binary in az azd; do
    if [[ -e "$install_dir/$binary" || -L "$install_dir/$binary" ]]; then
      cp -L "$install_dir/$binary" "$previous_generation/$binary"
      chmod 0755 "$previous_generation/$binary"
    fi
  done

  atomic_symlink ".azedarach-generations/$(basename "$previous_generation")" \
    "$install_dir/.azedarach-current"

  for binary in az azd; do
    if [[ -L "$install_dir/$binary" ]] &&
      [[ "$(readlink "$install_dir/$binary")" == ".azedarach-current/$binary" ]]; then
      continue
    fi
    if ! atomic_symlink ".azedarach-current/$binary" "$install_dir/$binary"; then
      # Keep earlier entries committed forward through the copied old pair.
      # Rewriting them during recovery creates an availability gap on Darwin;
      # the next install safely resumes any entries that remain unmanaged.
      return 1
    fi
  done
}

acquire_install_lock
generations_dir="$install_dir/.azedarach-generations"
mkdir -p "$generations_dir"
new_generation="$(mktemp -d "$generations_dir/generation.XXXXXX")"
cp "$build_dir/az" "$new_generation/az"
cp "$build_dir/azd" "$new_generation/azd"
chmod 0755 "$new_generation/az" "$new_generation/azd"

if ! public_links_are_managed; then
  migrate_public_links "$generations_dir"
fi

candidate_generation="$new_generation"
new_generation=""
if ! atomic_symlink ".azedarach-generations/$(basename "$candidate_generation")" \
  "$install_dir/.azedarach-current"; then
  rm -rf "$candidate_generation"
  exit 1
fi
# Successful immutable generations are retained. A long-lived az/watch process
# may need to launch the sibling azd from its own generation after any number
# of later installs. Lifetime-aware cleanup must therefore be an explicit
# maintenance operation, not automatic installer garbage collection.
if [[ "$(cat "$lock_dir/pid" 2>/dev/null || true)" == "$$" ]]; then
  rm -rf "$lock_dir"
fi
lock_owned=0
lock_dir=""

echo "Installed az -> $install_dir/az"
echo "Installed azd -> $install_dir/azd"
echo "Global az resolves to: $(command -v az || true)"
if [[ "$no_run" -eq 1 ]]; then
  echo "Skipping run (--no-run)"
  exit 0
fi
source "$repo_root/scripts/jaeger-local.sh"
jaeger_ensure
if [ "$(git rev-parse --git-dir)" != "$(git rev-parse --git-common-dir)" ]; then
  AZEDARACH_DAEMON_SCOPE=worktree "$install_dir/az" daemon restart
else
  "$install_dir/az" daemon restart
fi
echo "Running az..."
exec "$install_dir/az"
