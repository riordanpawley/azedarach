#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo_root"

scan_globs=(
    '*.ts'
    '*.tsx'
    '*.js'
    '*.mjs'
    '*.mts'
    '*.cts'
)

excludes=(
    '!**/node_modules/**'
    '!**/dist/**'
    '!**/*.test.ts'
    '!**/*.test.tsx'
    '!**/*.spec.ts'
    '!**/*.spec.tsx'
)

fail=0

print_hits() {
    local label="$1"
    shift
    local hits=("$@")
    if ((${#hits[@]} == 0)); then
        return
    fi
    printf 'Typed error policy violations (%s):\n' "$label" >&2
    printf '  %s\n' "${hits[@]}" >&2
    fail=1
}

check_pattern() {
    local pattern="$1"
    local label="$2"
    local hits=()
    while IFS= read -r hit; do
        [[ -n "$hit" ]] && hits+=("$hit")
    done < <(
        rg -n --no-heading --glob "${scan_globs[0]}" --glob "${scan_globs[1]}" --glob "${scan_globs[2]}" \
            --glob "${scan_globs[3]}" --glob "${scan_globs[4]}" --glob "${scan_globs[5]}" \
            --glob "${excludes[0]}" --glob "${excludes[1]}" --glob "${excludes[2]}" \
            --glob "${excludes[3]}" --glob "${excludes[4]}" --glob "${excludes[5]}" \
            "$pattern" packages src bin || true
    )
    print_hits "$label" "${hits[@]}"
}

check_pattern 'new Error\(' 'new Error(...) is banned in non-test app code; use tagged errors'
check_pattern 'throw new Error\(' 'throw new Error(...) is banned in non-test app code; use tagged errors'
check_pattern 'Effect\.fail\(new Error\(' 'Effect.fail(new Error(...)) is banned; fail with tagged errors'
check_pattern 'instanceof Error' 'instanceof Error checks are banned; use _tag / typed error surfaces'
check_pattern 'String\([[:space:]]*error[[:space:]]*\)' 'String(error) coercion is banned in error mapping paths'

if ((fail != 0)); then
    exit 1
fi

printf 'Typed error policy check passed.\n'
