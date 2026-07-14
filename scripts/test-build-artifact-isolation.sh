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
printf 'scratch build\n' >"$output"
EOF
chmod +x "$fixture/fake-bin/go"

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
test ! -L "$fixture/global-bin/az"
test ! -L "$fixture/global-bin/azd"
grep -q "scratch build" "$fixture/global-bin/az"
grep -q "scratch build" "$fixture/global-bin/azd"
cmp "$fixture/az.before" "$fixture/bin/az"
cmp "$fixture/azd.before" "$fixture/bin/azd"

just --justfile "$fixture/justfile" --working-directory "$fixture" clean
test ! -e "$fixture/.tmp/az-install"
test -x "$fixture/global-bin/az"
test -x "$fixture/global-bin/azd"

echo "build artifact isolation contract: PASS"
