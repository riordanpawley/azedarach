#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo_root"

fail=0

check_forbidden_imports() {
	local package_dir="$1"
	shift
	local label="$1"
	shift

	local patterns=("$@")
	local hits=()

	if [[ ! -d "$package_dir" ]]; then
		return
	fi

	for pattern in "${patterns[@]}"; do
		while IFS= read -r hit; do
			[[ -n "$hit" ]] && hits+=("$hit")
		done < <(rg -n --glob '*.ts' --glob '*.tsx' --glob '*.js' --glob '*.mjs' --glob '*.mts' --glob '*.cts' "$pattern" "$package_dir" || true)
	done

	if ((${#hits[@]} > 0)); then
		printf 'Boundary violations in %s:\n' "$label" >&2
		printf '  %s\n' "${hits[@]}" >&2
		fail=1
	fi
}

check_forbidden_imports "packages/tui/src" "packages/tui" \
	'packages/(cli|daemon)/' \
	'@azedarach/(cli|daemon)'

check_forbidden_imports "packages/cli/src" "packages/cli" \
	'packages/(tui|daemon)/' \
	'@azedarach/(tui|daemon)'

check_forbidden_imports "packages/daemon/src" "packages/daemon" \
	'packages/(tui|cli)/' \
	'@azedarach/(tui|cli)'

check_forbidden_imports "packages/shared/src" "packages/shared" \
	'packages/(tui|cli|daemon)/' \
	'@azedarach/(tui|cli|daemon)'

if ((fail != 0)); then
	exit 1
fi

printf 'Package boundary check passed.\n'
