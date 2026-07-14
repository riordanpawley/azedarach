#!/usr/bin/env bash
# Source this file from direnv to select the repository-family Go cache namespace.

_az_finalize=0
if [[ "${1:-}" == "--finalize" ]]; then
  _az_finalize=1
fi

_az_common_dir="$(git rev-parse --path-format=absolute --git-common-dir 2>/dev/null || true)"
if [[ -n "$_az_common_dir" && "$(basename "$_az_common_dir")" == ".git" ]]; then
  _az_go_cache_root="$(dirname "$_az_common_dir")/.azedarach/go"
else
  _az_go_cache_root="$(pwd -P)/.azedarach/go"
fi
if [[ -n "${AZEDARACH_GO_CACHE_ROOT:-}" && "$AZEDARACH_GO_CACHE_ROOT" != "$_az_go_cache_root" ]]; then
  echo "AZEDARACH_GO_CACHE_ROOT must equal daemon-authoritative project root $_az_go_cache_root (got $AZEDARACH_GO_CACHE_ROOT)" >&2
  return 1 2>/dev/null || exit 1
fi

_az_git_dir="$(git rev-parse --path-format=absolute --git-dir 2>/dev/null || true)"
_az_common_dir="$(git rev-parse --path-format=absolute --git-common-dir 2>/dev/null || true)"
_az_owner="main"
if [[ -n "$_az_git_dir" && -n "$_az_common_dir" && "$_az_git_dir" != "$_az_common_dir" ]]; then
  _az_issue="${AZEDARACH_TICKET_ID:-${AZEDARACH_ISSUE_ID:-}}"
  if [[ -z "$_az_issue" ]]; then
    _az_branch="$(git symbolic-ref --quiet --short HEAD 2>/dev/null || true)"
    if [[ "$_az_branch" =~ ^[^/]+/([A-Za-z0-9-]+)/ ]]; then
      _az_issue="${BASH_REMATCH[1]}"
    fi
  fi
  if [[ -z "$_az_issue" ]]; then
    _az_owner="main"
  else
    _az_issue="$(printf '%s' "$_az_issue" | tr '[:upper:]' '[:lower:]' | sed 's/[^a-z0-9-]/-/g; s/--*/-/g; s/^-//; s/-$//')"
    _az_owner="issue-${_az_issue:-unknown}"
  fi
fi

_az_kind="${AZEDARACH_GO_CACHE_KIND:-normal}"
case "$_az_kind" in
  normal|race|coverage) ;;
  *) echo "unsupported AZEDARACH_GO_CACHE_KIND=$_az_kind (expected normal, race, or coverage)" >&2; return 1 2>/dev/null || exit 1 ;;
esac

export AZEDARACH_GO_CACHE_ROOT="$_az_go_cache_root"
export AZEDARACH_GO_CACHE_OWNER="$_az_owner"
export AZEDARACH_GO_CACHE_NAMESPACE="${_az_kind}/${_az_owner}"
_az_managed_gocache="$_az_go_cache_root/caches/v1/$_az_kind/$_az_owner"
if [[ -n "${AZEDARACH_GOCACHE:-}" && "$AZEDARACH_GOCACHE" != "$_az_managed_gocache" ]]; then
  echo "AZEDARACH_GOCACHE must equal managed namespace $_az_managed_gocache (got $AZEDARACH_GOCACHE)" >&2
  return 1 2>/dev/null || exit 1
fi
if [[ "$_az_finalize" == "1" && -n "${GOCACHE:-}" && "$GOCACHE" != "$_az_managed_gocache" ]]; then
  echo "GOCACHE must equal managed namespace $_az_managed_gocache (got $GOCACHE)" >&2
  return 1 2>/dev/null || exit 1
fi
export GOCACHE="$_az_managed_gocache"
export GOPATH="${AZEDARACH_GOPATH:-$_az_go_cache_root/path}"
case " ${GOFLAGS:-} " in
  *" -trimpath "*) ;;
  *) export GOFLAGS="${GOFLAGS:+$GOFLAGS }-trimpath" ;;
esac
mkdir -p "$GOCACHE" "$GOPATH"

if [[ "${1:-}" == "--print" ]]; then
  printf 'AZEDARACH_GO_CACHE_ROOT=%s\nAZEDARACH_GO_CACHE_OWNER=%s\nAZEDARACH_GO_CACHE_NAMESPACE=%s\nGOCACHE=%s\nGOPATH=%s\nGOFLAGS=%s\n' \
    "$AZEDARACH_GO_CACHE_ROOT" "$AZEDARACH_GO_CACHE_OWNER" "$AZEDARACH_GO_CACHE_NAMESPACE" "$GOCACHE" "$GOPATH" "$GOFLAGS"
fi

unset _az_finalize _az_go_cache_root _az_common_dir _az_git_dir _az_owner _az_issue _az_branch _az_kind _az_managed_gocache
