#!/usr/bin/env bash

set -euo pipefail

usage() {
  cat <<'EOF'
Cut a Go release and update Homebrew formula in one command.

Usage:
  release-homebrew.sh (--patch|--minor|--major) --tap-dir <path> [options] [-- <release-script-args...>]

Options:
  --patch                    Bump patch version (required exactly one bump flag)
  --minor                    Bump minor version
  --major                    Bump major version
  --tap-dir <path>           Local clone of homebrew tap repo (required)
  --repo <owner/repo>        GitHub release repo (default: riordanpawley/azedarach)
  --formula <path>           Formula path inside tap repo (default: Formula/azedarach.rb)
  --max-wait-seconds <n>     Max wait for release assets (default: 600)
  --poll-seconds <n>         Poll interval while waiting (default: 10)
  --skip-tap-commit          Skip creating a tap repo commit
  --skip-tap-push            Skip pushing tap repo changes
  -h, --help                 Show help

Examples:
  ./scripts/release-homebrew.sh --patch --tap-dir /Users/me/prog/homebrew-azedarach
  ./scripts/release-homebrew.sh --minor --tap-dir /Users/me/prog/homebrew-azedarach -- --skip-checks
EOF
}

if [[ $# -lt 1 ]]; then
  usage
  exit 1
fi

if [[ "$1" == "-h" || "$1" == "--help" ]]; then
  usage
  exit 0
fi

RELEASE_TARGET=""
TAP_DIR=""
REPO="riordanpawley/azedarach"
FORMULA_PATH="Formula/azedarach.rb"
SKIP_TAP_COMMIT=0
SKIP_TAP_PUSH=0
MAX_WAIT_SECONDS=600
POLL_SECONDS=10
RELEASE_ARGS=()

while [[ $# -gt 0 ]]; do
  case "$1" in
    --patch)
      [[ -z "$RELEASE_TARGET" ]] || { echo "Only one of --patch/--minor/--major is allowed" >&2; exit 1; }
      RELEASE_TARGET="patch"
      shift
      ;;
    --minor)
      [[ -z "$RELEASE_TARGET" ]] || { echo "Only one of --patch/--minor/--major is allowed" >&2; exit 1; }
      RELEASE_TARGET="minor"
      shift
      ;;
    --major)
      [[ -z "$RELEASE_TARGET" ]] || { echo "Only one of --patch/--minor/--major is allowed" >&2; exit 1; }
      RELEASE_TARGET="major"
      shift
      ;;
    --tap-dir)
      TAP_DIR="$2"
      shift 2
      ;;
    --repo)
      REPO="$2"
      shift 2
      ;;
    --formula)
      FORMULA_PATH="$2"
      shift 2
      ;;
    --max-wait-seconds)
      MAX_WAIT_SECONDS="$2"
      shift 2
      ;;
    --poll-seconds)
      POLL_SECONDS="$2"
      shift 2
      ;;
    --skip-tap-commit)
      SKIP_TAP_COMMIT=1
      shift
      ;;
    --skip-tap-push)
      SKIP_TAP_PUSH=1
      shift
      ;;
    --)
      shift
      RELEASE_ARGS=("$@")
      break
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      echo "Unknown argument: $1" >&2
      usage
      exit 1
      ;;
  esac
done

if [[ -z "$RELEASE_TARGET" ]]; then
  echo "Exactly one bump option is required: --patch | --minor | --major" >&2
  usage
  exit 1
fi

if [[ -z "$TAP_DIR" ]]; then
  echo "--tap-dir is required" >&2
  usage
  exit 1
fi

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
TAP_DIR_ABS="$(cd "$TAP_DIR" && pwd)"
FORMULA_OUTPUT="$TAP_DIR_ABS/$FORMULA_PATH"

if [[ ! -d "$TAP_DIR_ABS/.git" ]]; then
  echo "Tap directory is not a git repository: $TAP_DIR_ABS" >&2
  exit 1
fi

if [[ -n "$(git -C "$TAP_DIR_ABS" status --porcelain)" ]]; then
  echo "Tap repository has uncommitted changes: $TAP_DIR_ABS" >&2
  exit 1
fi

cd "$REPO_ROOT"

echo "==> Running release script"
"$REPO_ROOT/scripts/release.sh" "$RELEASE_TARGET" "${RELEASE_ARGS[@]}"

VERSION="$(git tag --list 'v*' | sed 's/^v//' | sort -V | tail -n 1)"
TAG="v${VERSION}"

echo "==> Waiting for release assets for $TAG"
elapsed=0
while true; do
  if gh release view "$TAG" --repo "$REPO" --json assets --jq '.assets[].name' 2>/dev/null | rg -x "SHA256SUMS.txt" -q; then
    echo "Found SHA256SUMS.txt for $TAG"
    break
  fi

  if [[ "$elapsed" -ge "$MAX_WAIT_SECONDS" ]]; then
    echo "Timed out after ${MAX_WAIT_SECONDS}s waiting for SHA256SUMS.txt in release $TAG" >&2
    exit 1
  fi

  sleep "$POLL_SECONDS"
  elapsed=$((elapsed + POLL_SECONDS))
done

echo "==> Generating Homebrew formula for $TAG"
"$REPO_ROOT/scripts/generate-homebrew-formula.sh" \
  "$TAG" \
  --repo "$REPO" \
  --output "$FORMULA_OUTPUT"

if git -C "$TAP_DIR_ABS" diff --quiet -- "$FORMULA_PATH"; then
  echo "==> Formula unchanged; nothing to commit or push"
  FORMULA_CHANGED=0
else
  FORMULA_CHANGED=1
fi

if [[ "$FORMULA_CHANGED" -eq 1 && "$SKIP_TAP_COMMIT" -eq 0 ]]; then
  echo "==> Committing tap formula update"
  git -C "$TAP_DIR_ABS" add "$FORMULA_PATH"
  git -C "$TAP_DIR_ABS" commit -m "azedarach $TAG"
elif [[ "$FORMULA_CHANGED" -eq 1 ]]; then
  echo "==> Skipping tap commit (--skip-tap-commit)"
fi

if [[ "$FORMULA_CHANGED" -eq 1 && "$SKIP_TAP_PUSH" -eq 0 ]]; then
  echo "==> Pushing tap repository"
  git -C "$TAP_DIR_ABS" push
elif [[ "$FORMULA_CHANGED" -eq 1 ]]; then
  echo "==> Skipping tap push (--skip-tap-push)"
fi

echo
echo "Release + Homebrew update complete."
echo "Version: $VERSION"
echo "Tag: $TAG"
echo "Formula: $FORMULA_OUTPUT"
