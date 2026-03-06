#!/usr/bin/env bash

set -euo pipefail

usage() {
	cat <<'EOF'
Cut an Azedarach ts-opentui release with version/tag sync.

Usage:
  release.sh <version> [--dry-run] [--skip-checks] [--skip-pull] [--remote <name>] [--branch <name>]

Examples:
  ./ts-opentui/scripts/release.sh 0.3.2
  ./ts-opentui/scripts/release.sh v0.3.2 --dry-run

What this script does:
  1. Validates branch, working tree cleanliness, and semver input.
  2. Pulls latest changes (git pull --rebase) unless --skip-pull is set.
  3. Runs quality gate: bun run type-check (unless --skip-checks).
  4. Updates ts-opentui/package.json version.
  5. Commits the version bump.
  6. Creates annotated tag v<version>.
  7. Pushes branch and tag to remote.
EOF
}

fail() {
	echo "Error: $*" >&2
	exit 1
}

run() {
	echo "+ $*"
	if [[ "$DRY_RUN" -eq 0 ]]; then
		"$@"
	fi
}

if [[ $# -lt 1 ]]; then
	usage
	exit 1
fi

if [[ "$1" == "-h" || "$1" == "--help" ]]; then
	usage
	exit 0
fi

VERSION_INPUT="$1"
shift

DRY_RUN=0
RUN_CHECKS=1
RUN_PULL=1
REMOTE="origin"
BRANCH="main"

while [[ $# -gt 0 ]]; do
	case "$1" in
	--dry-run)
		DRY_RUN=1
		shift
		;;
	--skip-checks)
		RUN_CHECKS=0
		shift
		;;
	--skip-pull)
		RUN_PULL=0
		shift
		;;
	--remote)
		REMOTE="$2"
		shift 2
		;;
	--branch)
		BRANCH="$2"
		shift 2
		;;
	-h | --help)
		usage
		exit 0
		;;
	*)
		fail "Unknown argument: $1"
		;;
	esac
done

VERSION="${VERSION_INPUT#v}"
SEMVER_REGEX='^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?(\+[0-9A-Za-z.-]+)?$'
if [[ ! "$VERSION" =~ $SEMVER_REGEX ]]; then
	fail "Version must be semver (for example 0.3.2 or 0.3.2-rc.1). Received: $VERSION_INPUT"
fi

TAG="v${VERSION}"

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
PACKAGE_JSON_PATH="$REPO_ROOT/ts-opentui/package.json"

if [[ ! -f "$PACKAGE_JSON_PATH" ]]; then
	fail "Missing package file: $PACKAGE_JSON_PATH"
fi

cd "$REPO_ROOT"

CURRENT_BRANCH="$(git branch --show-current)"
if [[ "$CURRENT_BRANCH" != "$BRANCH" ]]; then
	fail "Current branch is '$CURRENT_BRANCH'. Switch to '$BRANCH' before releasing."
fi

if [[ -n "$(git status --porcelain)" ]]; then
	fail "Working tree is not clean. Commit/stash changes before running release."
fi

if ! git remote get-url "$REMOTE" >/dev/null 2>&1; then
	fail "Remote '$REMOTE' does not exist."
fi

if git rev-parse -q --verify "refs/tags/$TAG" >/dev/null; then
	fail "Tag '$TAG' already exists locally."
fi

if git ls-remote --exit-code --tags "$REMOTE" "refs/tags/$TAG" >/dev/null 2>&1; then
	fail "Tag '$TAG' already exists on remote '$REMOTE'."
fi

CURRENT_VERSION="$(
	bun -e '
		import { readFileSync } from "node:fs";
		const pkg = JSON.parse(readFileSync(process.argv[1], "utf8"));
		process.stdout.write(String(pkg.version));
	' "$PACKAGE_JSON_PATH"
)"

if [[ "$CURRENT_VERSION" == "$VERSION" ]]; then
	fail "ts-opentui/package.json is already $VERSION. Choose a new version."
fi

if [[ "$RUN_PULL" -eq 1 ]]; then
	run git pull --rebase "$REMOTE" "$BRANCH"
fi

if [[ "$RUN_CHECKS" -eq 1 ]]; then
	echo "+ (cd ts-opentui && bun run type-check)"
	if [[ "$DRY_RUN" -eq 0 ]]; then
		(cd ts-opentui && bun run type-check)
	fi
fi

echo "+ bump ts-opentui/package.json version: $CURRENT_VERSION -> $VERSION"
if [[ "$DRY_RUN" -eq 0 ]]; then
	bun -e '
		import { readFileSync, writeFileSync } from "node:fs";
		const filePath = process.argv[1];
		const nextVersion = process.argv[2];
		const pkg = JSON.parse(readFileSync(filePath, "utf8"));
		pkg.version = nextVersion;
		writeFileSync(filePath, `${JSON.stringify(pkg, null, "\t")}\n`);
	' "$PACKAGE_JSON_PATH" "$VERSION"
fi

run git add "$PACKAGE_JSON_PATH"
run git commit -m "release: $TAG"
run git tag -a "$TAG" -m "Release $TAG"
run git push "$REMOTE" "$BRANCH"
run git push "$REMOTE" "$TAG"

echo
echo "Release cut complete."
echo "Version: $VERSION"
echo "Tag: $TAG"
if [[ "$DRY_RUN" -eq 1 ]]; then
	echo "Dry run mode: no changes were made."
fi
