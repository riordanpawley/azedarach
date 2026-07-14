#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
fixture="$(mktemp -d "${TMPDIR:-/tmp}/azedarach-build-contract.XXXXXX")"
trap 'rm -rf "$fixture"' EXIT

mkdir -p "$fixture/bin" "$fixture/fake-bin"
cp "$repo_root/justfile" "$fixture/justfile"
mkdir -p "$fixture/scripts"
cp "$repo_root/scripts/build-link-run.sh" "$fixture/scripts/build-link-run.sh"
printf 'production az sentinel\n' >"$fixture/bin/az"
printf 'production azd sentinel\n' >"$fixture/bin/azd"
cp "$fixture/bin/az" "$fixture/az.before"
cp "$fixture/bin/azd" "$fixture/azd.before"

cat >"$fixture/fake-bin/go" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail

args="$*"
output=""
while (($# > 0)); do
  if [[ "$1" == "-o" ]]; then
    output="$2"
    break
  fi
  shift
done
if [[ -z "$output" ]]; then
  echo "stub go: missing -o output" >&2
  exit 1
fi
if [[ "${FAKE_GO_FAIL_AZD:-0}" == "1" && "$args" == *"./cmd/azd"* ]]; then
  echo "stub go: requested azd build failure" >&2
  exit 1
fi
mkdir -p "$(dirname "$output")"
printf 'scratch build %s\n' "${FAKE_GO_MARKER:-default}" >"$output"
EOF
chmod +x "$fixture/fake-bin/go"

cat >"$fixture/fake-bin/mv" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail

destination="${*: -1}"
if [[ "${FAKE_MV_FAIL_AZD_LINK:-0}" == "1" && "$destination" == */azd &&
      ! -e "${FAKE_MV_FAIL_ONCE_FILE:?}" ]]; then
  : >"$FAKE_MV_FAIL_ONCE_FILE"
  echo "stub mv: requested azd link install failure" >&2
  exit 1
fi
exec /bin/mv "$@"
EOF
chmod +x "$fixture/fake-bin/mv"

cat >"$fixture/fake-bin/cp" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail

destination="${*: -1}"
if [[ "${FAKE_CP_ASSERT_SERIAL:-0}" == "1" &&
      "$destination" == */.azedarach-generations/generation.*/az ]]; then
  guard="${FAKE_CP_GUARD:?}"
  if ! mkdir "$guard" 2>/dev/null; then
    echo "stub cp: concurrent installers entered the critical section" >&2
    exit 1
  fi
  trap 'rmdir "$guard"' EXIT
  sleep 0.1
fi
/bin/cp "$@"
EOF
chmod +x "$fixture/fake-bin/cp"

cat >"$fixture/fake-bin/git" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail

if [[ "${1:-}" == "rev-parse" && "${2:-}" == "HEAD" ]]; then
  printf 'fixture-sha\n'
  exit 0
fi
if [[ "${1:-}" == "rev-parse" && "${2:-}" == "--git-dir" ]]; then
  if [[ "${FAKE_GIT_MODE:-linked}" == "primary" ]]; then
    printf '/fixture/repo/.git\n'
  else
    printf '/fixture/repo/.git/worktrees/test\n'
  fi
  exit 0
fi
if [[ "${1:-}" == "rev-parse" && "${2:-}" == "--git-common-dir" ]]; then
  printf '/fixture/repo/.git\n'
  exit 0
fi
echo "stub git: unsupported arguments: $*" >&2
exit 1
EOF
chmod +x "$fixture/fake-bin/git"

PATH="$fixture/fake-bin:$PATH" just --justfile "$fixture/justfile" --working-directory "$fixture" build

cmp "$fixture/az.before" "$fixture/bin/az"
cmp "$fixture/azd.before" "$fixture/bin/azd"
test -s "$fixture/.tmp/az-test/az"
test -s "$fixture/.tmp/az-test/azd"

just --justfile "$fixture/justfile" --working-directory "$fixture" clean

cmp "$fixture/az.before" "$fixture/bin/az"
cmp "$fixture/azd.before" "$fixture/bin/azd"
test ! -e "$fixture/.tmp/az-test"

if PATH="$fixture/fake-bin:$PATH" "$fixture/scripts/build-link-run.sh" --no-run \
  >"$fixture/build-link-run.stdout" 2>"$fixture/build-link-run.stderr"; then
  echo "build-link-run unexpectedly accepted a linked worktree" >&2
  exit 1
fi
grep -q "Refusing build-link-run from a linked worktree" "$fixture/build-link-run.stderr"
cmp "$fixture/az.before" "$fixture/bin/az"
cmp "$fixture/azd.before" "$fixture/bin/azd"

mkdir -p "$fixture/failure-bin"
printf 'installed az before failed build\n' >"$fixture/failure-bin/az"
printf 'installed azd before failed build\n' >"$fixture/failure-bin/azd"
cp "$fixture/failure-bin/az" "$fixture/failure-az.before"
cp "$fixture/failure-bin/azd" "$fixture/failure-azd.before"
if FAKE_GIT_MODE=primary FAKE_GO_FAIL_AZD=1 AZ_INSTALL_DIR="$fixture/failure-bin" \
  PATH="$fixture/fake-bin:$PATH" "$fixture/scripts/build-link-run.sh" --no-run \
  >"$fixture/build-failure.stdout" 2>"$fixture/build-failure.stderr"; then
  echo "build-link-run unexpectedly installed after an azd build failure" >&2
  exit 1
fi
grep -q "requested azd build failure" "$fixture/build-failure.stderr"
cmp "$fixture/failure-az.before" "$fixture/failure-bin/az"
cmp "$fixture/failure-azd.before" "$fixture/failure-bin/azd"
test ! -e "$fixture/failure-bin/.azedarach-current"
test ! -L "$fixture/failure-bin/.azedarach-current"

mkdir -p "$fixture/partial-bin/.azedarach-generations/generation.old"
printf 'partial old az\n' >"$fixture/partial-bin/.azedarach-generations/generation.old/az"
printf 'partial old azd\n' >"$fixture/partial-bin/.azedarach-generations/generation.old/azd"
chmod +x "$fixture/partial-bin/.azedarach-generations/generation.old/az" \
  "$fixture/partial-bin/.azedarach-generations/generation.old/azd"
ln -s .azedarach-generations/generation.old "$fixture/partial-bin/.azedarach-current"
ln -s .azedarach-current/az "$fixture/partial-bin/az"
cp "$fixture/partial-bin/.azedarach-generations/generation.old/azd" "$fixture/partial-bin/azd"
cp -L "$fixture/partial-bin/az" "$fixture/partial-az.before"
cp "$fixture/partial-bin/azd" "$fixture/partial-azd.before"

if FAKE_GIT_MODE=primary FAKE_MV_FAIL_AZD_LINK=1 \
  FAKE_MV_FAIL_ONCE_FILE="$fixture/partial-azd-link-failed" \
  AZ_INSTALL_DIR="$fixture/partial-bin" PATH="$fixture/fake-bin:$PATH" \
  "$fixture/scripts/build-link-run.sh" --no-run \
  >"$fixture/partial-failure.stdout" 2>"$fixture/partial-failure.stderr"; then
  echo "build-link-run unexpectedly succeeded from a partially managed state" >&2
  exit 1
fi
grep -q "requested azd link install failure" "$fixture/partial-failure.stderr"
test -x "$fixture/partial-bin/az"
test -x "$fixture/partial-bin/azd"
cmp "$fixture/partial-az.before" "$fixture/partial-bin/az"
cmp "$fixture/partial-azd.before" "$fixture/partial-bin/azd"
test "$(readlink "$fixture/partial-bin/az")" = ".azedarach-current/az"
test ! -L "$fixture/partial-bin/azd"
test "$(readlink "$fixture/partial-bin/.azedarach-current")" = ".azedarach-generations/generation.old"
test -x "$fixture/partial-bin/.azedarach-current/az"
test -x "$fixture/partial-bin/.azedarach-current/azd"
partial_generation_count="$(find "$fixture/partial-bin/.azedarach-generations" -mindepth 1 -maxdepth 1 -type d | wc -l | tr -d ' ')"
test "$partial_generation_count" -eq 1
while IFS= read -r link; do
  test -e "$link"
done < <(find "$fixture/partial-bin" -type l)

mkdir -p "$fixture/interrupted-bin"
printf 'interrupted old az\n' >"$fixture/interrupted-bin/az"
printf 'interrupted old azd\n' >"$fixture/interrupted-bin/azd"
chmod +x "$fixture/interrupted-bin/az" "$fixture/interrupted-bin/azd"
ln -s .azedarach-generations/missing "$fixture/interrupted-bin/.azedarach-current"
cp "$fixture/interrupted-bin/az" "$fixture/interrupted-az.before"
cp "$fixture/interrupted-bin/azd" "$fixture/interrupted-azd.before"

if FAKE_GIT_MODE=primary FAKE_MV_FAIL_AZD_LINK=1 \
  FAKE_MV_FAIL_ONCE_FILE="$fixture/interrupted-azd-link-failed" \
  AZ_INSTALL_DIR="$fixture/interrupted-bin" PATH="$fixture/fake-bin:$PATH" \
  "$fixture/scripts/build-link-run.sh" --no-run \
  >"$fixture/interrupted-failure.stdout" 2>"$fixture/interrupted-failure.stderr"; then
  echo "build-link-run unexpectedly succeeded from an interrupted control-link state" >&2
  exit 1
fi
grep -q "requested azd link install failure" "$fixture/interrupted-failure.stderr"
test -x "$fixture/interrupted-bin/az"
test -x "$fixture/interrupted-bin/azd"
cmp "$fixture/interrupted-az.before" "$fixture/interrupted-bin/az"
cmp "$fixture/interrupted-azd.before" "$fixture/interrupted-bin/azd"
test ! -e "$fixture/interrupted-bin/.azedarach-current"
test ! -L "$fixture/interrupted-bin/.azedarach-current"

if FAKE_GIT_MODE=primary FAKE_MV_FAIL_AZD_LINK=1 \
  FAKE_MV_FAIL_ONCE_FILE="$fixture/azd-link-failed" \
  AZ_INSTALL_DIR="$fixture/failure-bin" PATH="$fixture/fake-bin:$PATH" \
  "$fixture/scripts/build-link-run.sh" --no-run \
  >"$fixture/install-failure.stdout" 2>"$fixture/install-failure.stderr"; then
  echo "build-link-run unexpectedly succeeded after an azd link install failure" >&2
  exit 1
fi
grep -q "requested azd link install failure" "$fixture/install-failure.stderr"
cmp "$fixture/failure-az.before" "$fixture/failure-bin/az"
cmp "$fixture/failure-azd.before" "$fixture/failure-bin/azd"
test ! -e "$fixture/failure-bin/.azedarach-current"
test ! -L "$fixture/failure-bin/.azedarach-current"

mkdir -p "$fixture/global-bin"
ln -s "$fixture/bin/az" "$fixture/global-bin/az"
ln -s "$fixture/bin/azd" "$fixture/global-bin/azd"
FAKE_GIT_MODE=primary AZ_INSTALL_DIR="$fixture/global-bin" \
  PATH="$fixture/fake-bin:$PATH" "$fixture/scripts/build-link-run.sh" --no-run \
  >"$fixture/build-link-run-install.stdout" 2>"$fixture/build-link-run-install.stderr"
grep -q "Installed az -> $fixture/global-bin/az" "$fixture/build-link-run-install.stdout"
grep -q "Installed azd -> $fixture/global-bin/azd" "$fixture/build-link-run-install.stdout"
test -x "$fixture/global-bin/az"
test -x "$fixture/global-bin/azd"
test -L "$fixture/global-bin/az"
test -L "$fixture/global-bin/azd"
test "$(readlink "$fixture/global-bin/az")" = ".azedarach-current/az"
test "$(readlink "$fixture/global-bin/azd")" = ".azedarach-current/azd"
grep -q "scratch build" "$fixture/global-bin/az"
grep -q "scratch build" "$fixture/global-bin/azd"
cmp "$fixture/az.before" "$fixture/bin/az"
cmp "$fixture/azd.before" "$fixture/bin/azd"

FAKE_GIT_MODE=primary FAKE_GO_MARKER=first FAKE_CP_ASSERT_SERIAL=1 \
  FAKE_CP_GUARD="$fixture/install-critical-section" AZ_INSTALL_DIR="$fixture/global-bin" \
  PATH="$fixture/fake-bin:$PATH" "$fixture/scripts/build-link-run.sh" --no-run \
  >"$fixture/concurrent-first.stdout" 2>"$fixture/concurrent-first.stderr" &
first_pid=$!
FAKE_GIT_MODE=primary FAKE_GO_MARKER=second FAKE_CP_ASSERT_SERIAL=1 \
  FAKE_CP_GUARD="$fixture/install-critical-section" AZ_INSTALL_DIR="$fixture/global-bin" \
  PATH="$fixture/fake-bin:$PATH" "$fixture/scripts/build-link-run.sh" --no-run \
  >"$fixture/concurrent-second.stdout" 2>"$fixture/concurrent-second.stderr" &
second_pid=$!
wait "$first_pid"
wait "$second_pid"
az_marker="$(sed -n 's/^scratch build //p' "$fixture/global-bin/az")"
azd_marker="$(sed -n 's/^scratch build //p' "$fixture/global-bin/azd")"
test -n "$az_marker"
test "$az_marker" = "$azd_marker"
generation_count="$(find "$fixture/global-bin/.azedarach-generations" -mindepth 1 -maxdepth 1 -type d | wc -l | tr -d ' ')"
test "$generation_count" -le 2

just --justfile "$fixture/justfile" --working-directory "$fixture" clean
test ! -e "$fixture/.tmp/az-install"
test -x "$fixture/global-bin/az"
test -x "$fixture/global-bin/azd"

if just --justfile "$fixture/justfile" --working-directory "$fixture" install \
  >"$fixture/just-install.stdout" 2>"$fixture/just-install.stderr"; then
  echo "just install unexpectedly provided an independent mutation path" >&2
  exit 1
fi
grep -q "Refusing unpaired install" "$fixture/just-install.stderr"

forbidden_mutator='go install[[:space:]]+\./cmd/az|go build.*-o[[:space:]]+[^[:space:]]*bin/az|(^|[;&|[:space:]])(cp|mv|ln|rm)([[:space:]]|.*).*bin/(az|azd)'
if git -C "$repo_root" grep -nE "$forbidden_mutator" -- \
  justfile 'scripts/*.sh' '.github/workflows/*.yml' \
  ':!scripts/test-build-artifact-isolation.sh'; then
  echo "repository still contains an alternative production az/azd mutator" >&2
  exit 1
fi

echo "build artifact isolation contract: PASS"
