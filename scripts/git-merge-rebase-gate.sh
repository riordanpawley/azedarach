#!/usr/bin/env sh
set -eu

if [ "${AZEDARACH_SKIP_MERGE_REBASE_GATE:-0}" = "1" ]; then
  echo "merge/rebase gate skipped (AZEDARACH_SKIP_MERGE_REBASE_GATE=1)"
  exit 0
fi

repo_root="$(git rev-parse --show-toplevel)"

timeout_cmd=""
if command -v timeout >/dev/null 2>&1; then
	timeout_cmd="timeout"
elif command -v gtimeout >/dev/null 2>&1; then
	timeout_cmd="gtimeout"
else
	echo "[gate] GNU timeout is required (provided by the repository Nix shell or Homebrew coreutils)" >&2
	exit 1
fi

validation_timeout="${AZEDARACH_MERGE_GATE_TIMEOUT:-10m}"
validation_status="$(mktemp -t azedarach-merge-gate-wrapper-status.XXXXXX)"
cleanup() {
	rm -f "$validation_status"
}
trap cleanup EXIT
(
	set +e
	"$timeout_cmd" --signal=TERM --kill-after=15s "$validation_timeout" "$repo_root/scripts/git-merge-rebase-gate-body.sh"
	printf '%s\n' "$?" >"$validation_status"
) 2>&1 | tee
if [ ! -s "$validation_status" ]; then
	echo "[gate] validation runner ended without a status" >&2
	exit 1
fi
status="$(cat "$validation_status")"
if [ "$status" -eq 124 ] || [ "$status" -eq 137 ]; then
	echo "[gate] validation exceeded the $validation_timeout wall-clock budget; inspect retained Go timeout stacks/output above" >&2
fi
exit "$status"
