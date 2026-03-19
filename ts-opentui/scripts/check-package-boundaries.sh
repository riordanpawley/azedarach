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
	'@azedarach/daemon(["/]):::Daemon-control must stay contract-only and must not import daemon implementation modules.' \
	'packages/(tui|cli|entry)/src/:::Import sibling package source through public package exports instead of direct source paths.' \
	'\.\./\.\./(tui|cli|entry)/src/:::Import sibling package source through public package exports instead of direct source paths.'

check_forbidden_imports "packages/entry/src" "packages/entry" \
	'packages/(tui|cli|daemon|daemon-control|shared)/src/:::Import sibling package source through public package exports instead of direct source paths.' \
	'\.\./\.\./(tui|cli|daemon|daemon-control|shared)/src/:::Import sibling package source through public package exports instead of direct source paths.'

check_forbidden_imports "packages/shared/src" "packages/shared" \
	'packages/(tui|cli|daemon)/:::Import sibling package source through the shared package or the public facade instead of a direct sibling package path.' \
	'@azedarach/(tui|cli|daemon)(["/]):::Import sibling package source through the shared package or the public facade instead of a direct sibling package alias.' \
	'GlobalDaemon(Bootstrap|Discovery):::Shared must not own daemon lifecycle or discovery wiring. Keep lifecycle contracts in daemon-control and live implementation in daemon.' \
	'src/(cli|core|daemon|rpc|services)/:::Import legacy src tree from @azedarach/shared. Shared must stay RPC-only.'

check_forbidden_imports "packages/cli/src" "packages/cli" \
	'src/(cli|core|daemon|rpc)/:::Import legacy src tree from a package module. Use package-local modules or dedicated runtime facades instead.'

check_forbidden_imports "packages/tui/src" "packages/tui" \
	'src/(cli|core|daemon|rpc)/:::Import legacy src tree from a package module. Use package-local modules or dedicated runtime facades instead.'

check_forbidden_imports "packages/daemon/src" "packages/daemon" \
	'src/(cli|core|daemon|rpc)/:::Import legacy src tree from a package module. Use package-local modules or dedicated runtime facades instead.'

check_forbidden_imports "packages/daemon-control/src" "packages/daemon-control" \
	'src/(cli|core|daemon|rpc)/:::Import legacy src tree from a package module. Use package-local modules or dedicated runtime facades instead.'

check_forbidden_imports "packages/entry/src" "packages/entry" \
	'src/(cli|core|daemon|rpc)/:::Import legacy src tree from a package module. Use package-local modules or dedicated runtime facades instead.'

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
			packages/tui/src packages/cli/src packages/daemon/src packages/shared/src packages/daemon-control/src packages/entry/src || true
	)

	if ((${#hits[@]} > 0)); then
		printf 'Boundary violations in packages (legacy services imports):\n' >&2
		printf '  The following imports are not allowed here:\n' >&2
		printf '  - Import legacy src/services from packages/* is forbidden.\n' >&2
		printf '  %s\n' "${hits[@]}" >&2
		fail=1
	fi
}

check_shared_surface_is_rpc_only() {
	local hits=()
	while IFS= read -r hit; do
		[[ -n "$hit" ]] && hits+=("$hit")
	done < <(
		rg -n --pcre2 --no-heading --glob '!**/node_modules/**' \
			--glob "${scan_globs[0]}" \
			--glob "${scan_globs[1]}" \
			--glob "${scan_globs[2]}" \
			--glob "${scan_globs[3]}" \
			--glob "${scan_globs[4]}" \
			--glob "${scan_globs[5]}" \
			"(import|export).*@azedarach/shared(?!/rpc)" \
			packages/cli/src packages/tui/src packages/daemon/src packages/entry/src packages/daemon-control/src || true
	)

	if ((${#hits[@]} > 0)); then
		printf 'Boundary violations (shared rpc-only surface):\n' >&2
		printf '  Import @azedarach/shared/rpc for shared package access; do not import non-rpc shared surfaces.\n' >&2
		printf '  %s\n' "${hits[@]}" >&2
		fail=1
	fi
}

check_legacy_shim_tree_absent() {
	local path="$1"
	if [[ -d "$path" ]]; then
		local files=()
		while IFS= read -r hit; do
			[[ -n "$hit" ]] && files+=("$hit")
		done < <(fd -t f . "$path" || true)

		if ((${#files[@]} > 0)); then
			printf 'Boundary violations (legacy shim tree reintroduced):\n' >&2
			printf '  Remove legacy shim files under %s and import from packages/* instead.\n' "$path" >&2
			printf '  %s\n' "${files[@]}" >&2
			fail=1
		fi
	fi
}

check_legacy_shim_core_files_absent() {
	local files=()
	while IFS= read -r hit; do
		[[ -n "$hit" ]] && files+=("$hit")
	done < <(fd -t f . src/core | rg '/(BackendDaemon|BackendSyncDaemon|Daemon|DevServerDaemonService|GlobalDaemonRegistry).*\.ts$' || true)

	if ((${#files[@]} > 0)); then
		printf 'Boundary violations (legacy daemon shim core files reintroduced):\n' >&2
		printf '  Move daemon lifecycle/runtime helpers to packages/daemon/src and remove legacy src/core shim files.\n' >&2
		printf '  %s\n' "${files[@]}" >&2
		fail=1
	fi
}

check_legacy_service_imports
check_shared_surface_is_rpc_only
check_legacy_shim_tree_absent "src/cli"
check_legacy_shim_tree_absent "src/rpc"
check_legacy_shim_tree_absent "src/daemon"
check_legacy_shim_core_files_absent

if ((fail != 0)); then
	exit 1
fi

printf 'Package boundary check passed.\n'
