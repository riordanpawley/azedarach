#!/usr/bin/env bash

set -euo pipefail

usage() {
  cat <<'EOF'
Cut an Azedarach Go release with semantic version bump + tag push.

Usage:
  release.sh <major|minor|patch> [--dry-run] [--skip-checks] [--skip-pull] [--remote <name>] [--branch <name>]

Examples:
  ./scripts/release.sh patch
  ./scripts/release.sh minor --skip-checks
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

RELEASE_TARGET="$1"
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
    -h|--help)
      usage
      exit 0
      ;;
    *)
      fail "Unknown argument: $1"
      ;;
  esac
done

if [[ "$RELEASE_TARGET" != "major" && "$RELEASE_TARGET" != "minor" && "$RELEASE_TARGET" != "patch" ]]; then
  fail "Release target must be one of: major, minor, patch"
fi

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
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

if [[ "$RUN_PULL" -eq 1 ]]; then
  # Never rewrite local history during release.
  # This only fast-forwards when remote has new commits.
  run git pull --ff-only "$REMOTE" "$BRANCH"
fi

latest_tag="$(git tag --list 'v*' | sed 's/^v//' | sort -V | tail -n 1)"
if [[ -z "$latest_tag" ]]; then
  latest_tag="0.0.0"
fi

if [[ ! "$latest_tag" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
  fail "Latest tag '$latest_tag' is not plain semver (x.y.z)."
fi

IFS='.' read -r current_major current_minor current_patch <<<"$latest_tag"
case "$RELEASE_TARGET" in
  major)
    next_version="$((current_major + 1)).0.0"
    ;;
  minor)
    next_version="${current_major}.$((current_minor + 1)).0"
    ;;
  patch)
    next_version="${current_major}.${current_minor}.$((current_patch + 1))"
    ;;
esac

tag="v${next_version}"

if git rev-parse -q --verify "refs/tags/$tag" >/dev/null; then
  fail "Tag '$tag' already exists locally."
fi

if git ls-remote --exit-code --tags "$REMOTE" "refs/tags/$tag" >/dev/null 2>&1; then
  fail "Tag '$tag' already exists on remote '$REMOTE'."
fi

echo "Current version: $latest_tag"
echo "Next version:    $next_version"
echo "Release tag:     $tag"

if [[ "$RUN_CHECKS" -eq 1 ]]; then
  echo "+ go test ./..."
  if [[ "$DRY_RUN" -eq 0 ]]; then
    go test ./...
  fi
fi

run git tag -a "$tag" -m "Release $tag"
run git push "$REMOTE" "$BRANCH"
run git push "$REMOTE" "$tag"

echo
echo "Release cut complete."
echo "Version: $next_version"
echo "Tag: $tag"
if [[ "$DRY_RUN" -eq 1 ]]; then
  echo "Dry run mode: no changes were made."
fi
