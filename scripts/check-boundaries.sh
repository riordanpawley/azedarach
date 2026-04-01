#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo_root"

module_path="$(go list -m -f '{{.Path}}')"
prefix="${module_path}/internal"

violations=0

fail() {
  printf 'FAIL: %s\n' "$1" >&2
  violations=$((violations + 1))
}

pass() {
  printf 'PASS: %s\n' "$1"
}

has_prefix() {
  local value="$1"
  local prefix_value="$2"
  [[ "$value" == "$prefix_value" || "$value" == "$prefix_value/"* ]]
}

is_authority_service_import() {
  local pkg="$1"
  case "$pkg" in
    "$prefix/services/worktree"|"$prefix/services/worktree/"*|\
    "$prefix/services/tmux"|"$prefix/services/tmux/"*|\
    "$prefix/services/devserver"|"$prefix/services/devserver/"*|\
    "$prefix/services/pr"|"$prefix/services/pr/"*)
      return 0
      ;;
  esac
  return 1
}

while IFS= read -r line; do
  importer="${line%% -> *}"
  imports="${line#* -> }"

  for imported in $imports; do
    # Ignore stdlib imports (no module path slash).
    if [[ "$imported" != */* ]]; then
      continue
    fi

    if { has_prefix "$importer" "$prefix/app" || has_prefix "$importer" "$prefix/cli"; } \
      && has_prefix "$imported" "$prefix/daemon"; then
      fail "$importer imports daemon package $imported"
    fi

    if { has_prefix "$importer" "$prefix/cli" || has_prefix "$importer" "$prefix/ui"; } \
      && is_authority_service_import "$imported"; then
      fail "$importer imports authority service $imported"
    fi

    if has_prefix "$importer" "$prefix/contracts" \
      && { has_prefix "$imported" "$prefix/app" || has_prefix "$imported" "$prefix/daemon" || has_prefix "$imported" "$prefix/ui"; }; then
      fail "$importer imports forbidden runtime package $imported"
    fi
  done
done < <(go list -f '{{.ImportPath}} -> {{range .Imports}}{{.}} {{end}}' ./internal/...)

if ! env -u GIT_INDEX_FILE -u GIT_DIR -u GIT_WORK_TREE go test ./internal/app -run '^TestIntegrationBoundaryGuard_NoDirectGitExecInAppOrCli$' -count=1; then
  printf 'Boundary runtime git-exec guard failed\n' >&2
  exit 1
fi

if (( violations > 0 )); then
  printf 'Boundary graph check failed: %d violation(s)\n' "$violations" >&2
  exit 1
fi

pass "go package boundary graph check passed"
