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
  echo "Refusing build-link-run from a linked worktree: $repo_root" >&2
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

atomic_symlink() {
  local target="$1"
  local destination="$2"
  local temporary
  local -a mv_args
  temporary="$(mktemp "${destination}.tmp.XXXXXX")"
  rm -f "$temporary"
  ln -s "$target" "$temporary"
  # GNU mv needs -T and BSD mv needs -h to replace a symlink-to-directory
  # instead of moving the temporary link through it.
  if mv --help 2>&1 | grep -q -- '--no-target-directory'; then
    mv_args=(-fT)
  else
    mv_args=(-fh)
  fi
  if ! mv "${mv_args[@]}" "$temporary" "$destination"; then
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
  local previous_generation rollback_dir
  previous_generation="$(mktemp -d "$generations_dir/generation.previous.XXXXXX")"
  rollback_dir="$(mktemp -d "$install_dir/.azedarach-rollback.XXXXXX")"

  for binary in az azd; do
    if [[ -e "$install_dir/$binary" || -L "$install_dir/$binary" ]]; then
      cp -L "$install_dir/$binary" "$previous_generation/$binary"
      chmod 0755 "$previous_generation/$binary"
      cp -P "$install_dir/$binary" "$rollback_dir/$binary"
    fi
  done

  atomic_symlink ".azedarach-generations/$(basename "$previous_generation")" \
    "$install_dir/.azedarach-current"

  if ! atomic_symlink ".azedarach-current/az" "$install_dir/az" ||
    ! atomic_symlink ".azedarach-current/azd" "$install_dir/azd"; then
    for binary in az azd; do
      rm -f "$install_dir/$binary"
      if [[ -e "$rollback_dir/$binary" || -L "$rollback_dir/$binary" ]]; then
        mv -f "$rollback_dir/$binary" "$install_dir/$binary"
      fi
    done
    rm -rf "$rollback_dir" "$previous_generation"
    return 1
  fi
  rm -rf "$rollback_dir"
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
previous_active_target="$(readlink "$install_dir/.azedarach-current" 2>/dev/null || true)"
if ! atomic_symlink ".azedarach-generations/$(basename "$candidate_generation")" \
  "$install_dir/.azedarach-current"; then
  rm -rf "$candidate_generation"
  exit 1
fi
active_generation="$candidate_generation"
previous_active_generation="$install_dir/$previous_active_target"
for generation in "$generations_dir"/generation.*; do
  if [[ -d "$generation" && "$generation" != "$active_generation" &&
        "$generation" != "$previous_active_generation" ]]; then
    rm -rf "$generation"
  fi
done
if [[ "$(cat "$lock_dir/pid" 2>/dev/null || true)" == "$$" ]]; then
  rm -rf "$lock_dir"
fi
lock_owned=0
lock_dir=""

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

container_host_port() {
  local engine="$1"
  local name="$2"
  local container_port="$3"
  local mapping
  mapping="$("$engine" port "$name" "${container_port}/tcp" 2>/dev/null | head -n 1 || true)"
  if [[ -z "$mapping" ]]; then
    return 1
  fi
  mapping="${mapping##*:}"
  if [[ -z "$mapping" ]]; then
    return 1
  fi
  echo "$mapping"
}

publish_jaeger_env() {
  local engine="$1"
  local name="$2"
  local ui_port
  local otlp_port

  if ! ui_port="$(container_host_port "$engine" "$name" 16686)"; then
    echo "Warning: Jaeger container $name has no published UI port" >&2
    return 1
  fi
  if ! otlp_port="$(container_host_port "$engine" "$name" 4318)"; then
    echo "Warning: Jaeger container $name has no published OTLP HTTP port" >&2
    return 1
  fi

  export OTEL_EXPORTER_OTLP_ENDPOINT="http://localhost:${otlp_port}"
  export OTEL_EXPORTER_OTLP_TRACES_ENDPOINT="http://localhost:${otlp_port}/v1/traces"
  echo "Jaeger ready: http://localhost:${ui_port} (OTLP ${OTEL_EXPORTER_OTLP_TRACES_ENDPOINT})"
}

start_jaeger_container() {
  local engine="$1"
  local name="$2"
  local image="$3"
  local ui_port="$4"
  local otlp_port="$5"
  local fixed_ports="$6"
  local storage_type="${AZEDARACH_JAEGER_STORAGE:-badger}"
  local max_traces="${AZEDARACH_JAEGER_MAX_TRACES:-20000}"
  local memory_limit="${AZEDARACH_JAEGER_MEMORY:-1g}"
  local badger_ttl="${AZEDARACH_JAEGER_BADGER_TTL:-72h}"
  local volume_name="${AZEDARACH_JAEGER_VOLUME:-${name}-data}"
  local run_args=(run -d --name "$name")
  local image_args=()

  case "$storage_type" in
    badger | memory) ;;
    *)
      echo "Warning: unsupported AZEDARACH_JAEGER_STORAGE=$storage_type; using memory storage" >&2
      storage_type="memory"
      ;;
  esac

  run_args+=(
    --label "azedarach.jaeger.storage=${storage_type}"
    --label "azedarach.jaeger.volume=${volume_name}"
    --label "azedarach.jaeger.badger_ttl=${badger_ttl}"
    --label "azedarach.jaeger.max_traces=${max_traces}"
  )

  if [[ -n "$memory_limit" && "$memory_limit" != "0" && "$memory_limit" != "none" ]]; then
    run_args+=(--memory "$memory_limit")
  fi

  case "$storage_type" in
    badger)
      run_args+=(
        -e SPAN_STORAGE_TYPE=badger
        -e DEPENDENCY_STORAGE_TYPE=badger
        -v "${volume_name}:/badger"
      )
      image_args+=(
        --badger.ephemeral=false
        --badger.directory-key=/badger/keys
        --badger.directory-value=/badger/values
        --badger.span-store-ttl "$badger_ttl"
      )
      ;;
    memory)
      image_args+=(--memory.max-traces "$max_traces")
      ;;
  esac

  if [[ "$fixed_ports" == "1" ]]; then
    run_args+=(
      -p "127.0.0.1:${ui_port}:16686"
      -p "127.0.0.1:${otlp_port}:4318"
    )
    "$engine" "${run_args[@]}" "$image" "${image_args[@]}" >/dev/null
    return $?
  fi

  run_args+=(
    -p "127.0.0.1::16686"
    -p "127.0.0.1::4318"
  )
  "$engine" "${run_args[@]}" "$image" "${image_args[@]}" >/dev/null
}

jaeger_container_oom_killed() {
  local engine="$1"
  local name="$2"

  [[ "$("$engine" inspect -f '{{.State.OOMKilled}}' "$name" 2>/dev/null || true)" == "true" ]]
}

jaeger_container_uses_expected_storage() {
  local engine="$1"
  local name="$2"
  local storage_type="${AZEDARACH_JAEGER_STORAGE:-badger}"
  local max_traces="${AZEDARACH_JAEGER_MAX_TRACES:-20000}"
  local badger_ttl="${AZEDARACH_JAEGER_BADGER_TTL:-72h}"
  local volume_name="${AZEDARACH_JAEGER_VOLUME:-${name}-data}"
  local labels

  labels="$("$engine" inspect -f '{{range $key, $value := .Config.Labels}}{{println $key "=" $value}}{{end}}' "$name" 2>/dev/null || true)"

  case "$storage_type" in
    badger)
      grep -qx "azedarach.jaeger.storage = badger" <<<"$labels" &&
        grep -qx "azedarach.jaeger.volume = ${volume_name}" <<<"$labels" &&
        grep -qx "azedarach.jaeger.badger_ttl = ${badger_ttl}" <<<"$labels"
      ;;
    memory)
      grep -qx "azedarach.jaeger.storage = memory" <<<"$labels" &&
        grep -qx "azedarach.jaeger.max_traces = ${max_traces}" <<<"$labels"
      ;;
    *)
      grep -qx "azedarach.jaeger.storage = memory" <<<"$labels" &&
        grep -qx "azedarach.jaeger.max_traces = ${max_traces}" <<<"$labels"
      ;;
  esac
}

remove_jaeger_container() {
  local engine="$1"
  local name="$2"

  "$engine" rm -f "$name" >/dev/null 2>&1 || true
}

recreate_jaeger_container() {
  local engine="$1"
  local name="$2"
  local image="$3"
  local ui_port="$4"
  local otlp_port="$5"
  local reason="$6"

  echo "Warning: recreating Jaeger container $name: $reason" >&2
  remove_jaeger_container "$engine" "$name"
  echo "Starting Jaeger container: $name"
  if ! start_jaeger_container "$engine" "$name" "$image" "$ui_port" "$otlp_port" 1; then
    echo "Warning: failed to start Jaeger on ${ui_port}/${otlp_port}; retrying with dynamic ports" >&2
    remove_jaeger_container "$engine" "$name"
    if ! start_jaeger_container "$engine" "$name" "$image" "$ui_port" "$otlp_port" 0; then
      echo "Warning: failed to start Jaeger container $name" >&2
      return 0
    fi
  fi
  publish_jaeger_env "$engine" "$name" || true
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

  if "$engine" inspect "$name" >/dev/null 2>&1; then
    if jaeger_container_oom_killed "$engine" "$name"; then
      recreate_jaeger_container "$engine" "$name" "$image" "$ui_port" "$otlp_port" "previous container was OOM-killed"
      return 0
    fi

    if ! jaeger_container_uses_expected_storage "$engine" "$name"; then
      recreate_jaeger_container "$engine" "$name" "$image" "$ui_port" "$otlp_port" "storage settings do not match AZEDARACH_JAEGER_STORAGE=${AZEDARACH_JAEGER_STORAGE:-badger}"
      return 0
    fi

    if [[ "$("$engine" inspect -f '{{.State.Running}}' "$name" 2>/dev/null || true)" == "true" ]]; then
      echo "Jaeger already running: $name"
      publish_jaeger_env "$engine" "$name" || true
      return 0
    fi

    echo "Starting existing Jaeger container: $name"
    if ! "$engine" start "$name" >/dev/null; then
      local fallback_name="${name}-$$"
      echo "Warning: failed to start Jaeger container $name; starting $fallback_name with dynamic ports" >&2
      if ! start_jaeger_container "$engine" "$fallback_name" "$image" "$ui_port" "$otlp_port" 0; then
        echo "Warning: failed to start Jaeger container $fallback_name" >&2
        return 0
      fi
      publish_jaeger_env "$engine" "$fallback_name" || true
      return 0
    fi
    publish_jaeger_env "$engine" "$name" || true
    return 0
  fi

  echo "Starting Jaeger container: $name"
  if ! start_jaeger_container "$engine" "$name" "$image" "$ui_port" "$otlp_port" 1; then
    echo "Warning: failed to start Jaeger on ${ui_port}/${otlp_port}; retrying with dynamic ports" >&2
    remove_jaeger_container "$engine" "$name"
    if ! start_jaeger_container "$engine" "$name" "$image" "$ui_port" "$otlp_port" 0; then
      echo "Warning: failed to start Jaeger container $name" >&2
      return 0
    fi
  fi
  publish_jaeger_env "$engine" "$name" || true
}

echo "Installed az -> $install_dir/az"
echo "Installed azd -> $install_dir/azd"
echo "Global az resolves to: $(command -v az || true)"
if [[ "$no_run" -eq 1 ]]; then
  echo "Skipping run (--no-run)"
  exit 0
fi
ensure_jaeger
if [ "$(git rev-parse --git-dir)" != "$(git rev-parse --git-common-dir)" ]; then
  AZEDARACH_DAEMON_SCOPE=worktree "$install_dir/az" daemon restart
else
  "$install_dir/az" daemon restart
fi
echo "Running az..."
exec "$install_dir/az"
