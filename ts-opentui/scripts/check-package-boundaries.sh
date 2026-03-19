#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo_root"

fail=0

scan_globs=(
	'*.ts'
	'*.tsx'
	'*.js'
	'*.mjs'
	'*.mts'
	'*.cts'
)

print_violations() {
	local label="$1"
	shift

	local violations=("$@")

	printf 'Boundary violations in %s:\n' "$label" >&2
	printf '  The following imports are not allowed here:\n' >&2
	printf '  %s\n' "${violations[@]}" >&2
}

check_forbidden_imports() {
	local package_dir="$1"
	shift
	local label="$1"
	shift

	local rules=("$@")
	local violations=()
	local extra_globs=()

	if [[ "$package_dir" == "packages/shared/src" ]]; then
		extra_globs+=(
			--glob '!packages/shared/src/core.ts'
			--glob '!packages/shared/src/storagePaths.ts'
		)
	fi

	if [[ ! -d "$package_dir" ]]; then
		return
	fi

	for rule in "${rules[@]}"; do
		local pattern="${rule%%:::*}"
		local message="${rule#*:::}"
		local hits=()

		while IFS= read -r hit; do
			[[ -n "$hit" ]] && hits+=("$hit")
		done < <(
			rg -n --no-heading --glob '!**/node_modules/**' \
				--glob "${scan_globs[0]}" \
				--glob "${scan_globs[1]}" \
				--glob "${scan_globs[2]}" \
				--glob "${scan_globs[3]}" \
				--glob "${scan_globs[4]}" \
				--glob "${scan_globs[5]}" \
				"${extra_globs[@]}" \
				"(import|export).*${pattern}" "$package_dir" || true
		)

		if ((${#hits[@]} > 0)); then
			violations+=("  - ${message}")
			for hit in "${hits[@]}"; do
				violations+=("    ${hit} | ${message}")
			done
		fi
	done

	if ((${#violations[@]} > 0)); then
		print_violations "$label" "${violations[@]}"
		fail=1
	fi
}

check_forbidden_imports "src" "src" \
	'@azedarach/(daemon|tui):::Import workspace packages through the local source tree or the public facade instead of a package alias.'

check_forbidden_imports "bin" "bin" \
	'@azedarach/daemon:::Import workspace packages through the local source tree or the public facade instead of a package alias.'

check_forbidden_imports "packages/tui/src" "packages/tui" \
    'packages/(cli|daemon)/:::Import sibling package source through the shared package or the public facade instead of a direct sibling package path.' \
    '\.\./\.\./cli/src/:::Import sibling package source through the shared package or the public facade instead of a direct sibling package path.' \
    '@azedarach/cli(["/]):::Import sibling package source through the shared package or the public facade instead of a direct sibling package alias.' \
    '@azedarach/daemon(["/]):::Import sibling package source through the shared package or the public facade instead of a direct sibling package alias.'

check_forbidden_imports "packages/cli/src" "packages/cli" \
    'packages/(tui|daemon)/:::Import sibling package source through the shared package or the public facade instead of a direct sibling package path.' \
    '\.\./\.\./tui/src/:::Import sibling package source through the shared package or the public facade instead of a direct sibling package path.' \
    '@azedarach/tui(["/]):::Import sibling package source through the shared package or the public facade instead of a direct sibling package alias.' \
    '@azedarach/daemon(["/]):::Import sibling package source through the shared package or the public facade instead of a direct sibling package alias.'

check_forbidden_imports "packages/daemon/src" "packages/daemon" \
    'packages/(tui|cli)/:::Import sibling package source through the shared package or the public facade instead of a direct sibling package path.' \
    '@azedarach/(tui|cli):::Import sibling package source through the shared package or the public facade instead of a direct sibling package alias.'

check_forbidden_imports "packages/daemon-control/src" "packages/daemon-control" \
    'packages/daemon/src/:::Daemon-control must stay contract-only and must not import daemon implementation modules.' \
    '@azedarach/daemon(["/]):::Daemon-control must stay contract-only and must not import daemon implementation modules.'

check_forbidden_imports "packages/shared/src" "packages/shared" \
    'packages/(tui|cli|daemon)/:::Import sibling package source through the shared package or the public facade instead of a direct sibling package path.' \
    '@azedarach/(tui|cli|daemon)(["/]):::Import sibling package source through the shared package or the public facade instead of a direct sibling package alias.' \
    'GlobalDaemon(Bootstrap|Discovery):::Shared must not own daemon lifecycle or discovery wiring. Keep lifecycle contracts in daemon-control and live implementation in daemon.'

check_forbidden_imports "packages/cli/src" "packages/cli" \
	'src/(cli|core|daemon|rpc)/:::Import legacy src tree from a package module. Use package-local modules, shared package exports, or public facades instead.'

check_forbidden_imports "packages/tui/src" "packages/tui" \
	'src/(cli|core|daemon|rpc)/:::Import legacy src tree from a package module. Use package-local modules, shared package exports, or public facades instead.'

check_forbidden_imports "packages/daemon/src" "packages/daemon" \
	'src/(cli|core|daemon|rpc)/:::Import legacy src tree from a package module. Use package-local modules, shared package exports, or public facades instead.'

check_forbidden_imports "packages/shared/src" "packages/shared" \
	'src/(cli|core|daemon|rpc)/:::Import legacy src tree from a package module. Use package-local modules, shared package exports, or public facades instead.'

check_legacy_service_imports() {
	local hits=()
	while IFS= read -r hit; do
		[[ -n "$hit" ]] && hits+=("$hit")
	done < <(
		rg -n --no-heading --glob '!**/node_modules/**' \
			--glob "${scan_globs[0]}" \
			--glob "${scan_globs[1]}" \
			--glob "${scan_globs[2]}" \
			--glob "${scan_globs[3]}" \
			--glob "${scan_globs[4]}" \
			--glob "${scan_globs[5]}" \
			"(import|export).*src/services/" \
			packages/tui/src packages/cli/src packages/daemon/src packages/shared/src || true
	)

	local allow_shared_bridge='^packages/shared/src/services\.ts:[0-9]+:'
	local violations=()
	for hit in "${hits[@]}"; do
		if [[ "$hit" =~ $allow_shared_bridge ]]; then
			continue
		fi
		violations+=("$hit")
	done

	if ((${#violations[@]} > 0)); then
		printf 'Boundary violations in packages (legacy services imports):\n' >&2
		printf '  The following imports are not allowed here:\n' >&2
		printf '  - Import legacy src/services from packages/* is forbidden.\n' >&2
		printf '    Allowed temporary exceptions:\n' >&2
		printf '    - packages/shared/src/services.ts bridge file only (tracked by wt).\n' >&2
		printf '  %s\n' "${violations[@]}" >&2
		fail=1
	fi
}

check_legacy_service_imports

if ((fail != 0)); then
	exit 1
fi

printf 'Package boundary check passed.\n'
