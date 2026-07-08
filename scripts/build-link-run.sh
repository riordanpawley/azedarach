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
sha="$(git rev-parse HEAD 2>/dev/null || echo unknown)"
ldflags="-X github.com/riordanpawley/azedarach/internal/buildinfo.Version=dev -X github.com/riordanpawley/azedarach/internal/buildinfo.GitCommit=${sha}"
go build -ldflags "$ldflags" -o bin/az ./cmd/az
go build -ldflags "$ldflags" -o bin/azd ./cmd/azd

choose_link_dir() {
  if [ -n "${AZ_LINK_DIR:-}" ]; then
    mkdir -p "$AZ_LINK_DIR"
    echo "$AZ_LINK_DIR"
    return 0
  fi

  if command -v az >/dev/null 2>&1; then
    local current_dir
    current_dir="$(dirname "$(command -v az)")"
    # Skip repo-local bin to avoid "linking" to the same local build path.
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

link_dir="$(choose_link_dir)"
link_binary() {
  local src="$1"
  local dst="$2"

  if [[ -e "$dst" && "$src" -ef "$dst" ]]; then
    return 0
  fi

  ln -sf "$src" "$dst"
}

link_binary "$repo_root/bin/az" "$link_dir/az"
link_binary "$repo_root/bin/azd" "$link_dir/azd"

choose_container_engine() {
  if command -v docker >/dev/null 2>&1; then
    echo docker
    return 0
  fi
  if command -v podman >/dev/null 2>&1; then
    echo podman
    return 0
  fi
  return 1
}

ensure_jaeger() {
  if [[ "${AZEDARACH_SKIP_JAEGER:-0}" == "1" ]]; then
    echo "Skipping Jaeger (AZEDARACH_SKIP_JAEGER=1)"
    return 0
  fi

  local engine
  if ! engine="$(choose_container_engine)"; then
    echo "Warning: docker/podman not found; skipping Jaeger startup" >&2
    return 0
  fi

  local name="${AZEDARACH_JAEGER_CONTAINER:-azedarach-jaeger}"
  local image="${AZEDARACH_JAEGER_IMAGE:-jaegertracing/all-in-one:latest}"
  local ui_port="${AZEDARACH_JAEGER_UI_PORT:-16686}"
  local otlp_port="${AZEDARACH_OTLP_HTTP_PORT:-4318}"

  if [[ "$("$engine" inspect -f '{{.State.Running}}' "$name" 2>/dev/null || true)" == "true" ]]; then
    echo "Jaeger already running: http://localhost:${ui_port}"
    return 0
  fi

  if "$engine" inspect "$name" >/dev/null 2>&1; then
    echo "Starting existing Jaeger container: $name"
    if ! "$engine" start "$name" >/dev/null; then
      echo "Warning: failed to start Jaeger container $name" >&2
      return 0
    fi
    echo "Jaeger ready: http://localhost:${ui_port} (OTLP http://localhost:${otlp_port}/v1/traces)"
    return 0
  fi

  echo "Starting Jaeger container: $name"
  if ! "$engine" run -d \
    --name "$name" \
    -p "${ui_port}:16686" \
    -p "${otlp_port}:4318" \
    "$image" >/dev/null; then
    echo "Warning: failed to start Jaeger container $name" >&2
    return 0
  fi
  echo "Jaeger ready: http://localhost:${ui_port} (OTLP http://localhost:${otlp_port}/v1/traces)"
}

echo "Linked az -> $link_dir/az"
echo "Linked azd -> $link_dir/azd"
echo "Global az resolves to: $(command -v az || true)"
if [[ "$no_run" -eq 1 ]]; then
  echo "Skipping run (--no-run)"
  exit 0
fi
ensure_jaeger
if [ "$(git rev-parse --git-dir)" != "$(git rev-parse --git-common-dir)" ]; then
  AZEDARACH_DAEMON_SCOPE=worktree "$link_dir/az" daemon restart
else
  "$link_dir/az" daemon restart
fi
echo "Running az..."
exec "$link_dir/az"
