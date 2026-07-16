#!/usr/bin/env sh
set -eu

if [ "${AZEDARACH_SKIP_MERGE_REBASE_GATE:-0}" = "1" ]; then
  echo "merge/rebase gate skipped for internal scratch integration (AZEDARACH_SKIP_MERGE_REBASE_GATE=1)"
  exit 0
fi

repo_root="$(git rev-parse --show-toplevel)"
unset AZEDARACH_TICKET_ID AZEDARACH_ISSUE_ID
export AZEDARACH_VALIDATION_SCOPE=repository
export AZEDARACH_VALIDATION_PURPOSE=push_gate
candidate_head="${AZEDARACH_CANDIDATE_HEAD:-$(git rev-parse --verify HEAD)}"
current_head="$(git rev-parse --verify HEAD)"
if [ "$current_head" != "$candidate_head" ]; then
	echo "[gate] candidate_head=$candidate_head observed_head=$current_head canonical=false reason=head-mismatch-before-start" >&2
	exit 1
fi
if [ -n "$(git status --porcelain)" ]; then
	echo "[gate] candidate_head=$candidate_head canonical=false reason=dirty-before-start" >&2
	exit 1
fi

echo "[gate] candidate_head=$candidate_head canonical=false status=running"

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
validation_body="${AZEDARACH_MERGE_GATE_BODY:-$repo_root/scripts/git-merge-rebase-gate-body.sh}"
validation_status="$(mktemp -t azedarach-merge-gate-wrapper-status.XXXXXX)"
validation_runner_pid="$(mktemp -t azedarach-merge-gate-wrapper-pid.XXXXXX)"
validation_gate_output="$(mktemp -t azedarach-merge-gate-output.XXXXXX)"
validation_artifact_result="$(mktemp -t azedarach-merge-gate-artifact-result.XXXXXX)"
export AZEDARACH_CANDIDATE_GATE_OUTPUT_PATH="$validation_gate_output"
export AZEDARACH_CANDIDATE_ARTIFACT_RESULT_FILE="$validation_artifact_result"
cleanup() {
	rm -f "$validation_status" "$validation_runner_pid" "$validation_gate_output" "$validation_artifact_result"
}
cancelled() {
	if [ -s "$validation_runner_pid" ]; then
		runner_pid="$(cat "$validation_runner_pid")"
		kill -TERM "-$runner_pid" 2>/dev/null || kill -TERM "$runner_pid" 2>/dev/null || true
		sleep 0.2
		kill -KILL "-$runner_pid" 2>/dev/null || kill -KILL "$runner_pid" 2>/dev/null || true
	fi
	echo "[gate] candidate_head=$candidate_head canonical=false status=cancelled" >&2
	exit 130
}
trap cleanup EXIT
trap cancelled INT TERM
(
	set +e
	"$timeout_cmd" --signal=TERM --kill-after=15s "$validation_timeout" "$validation_body" &
	runner_pid=$!
	printf '%s\n' "$runner_pid" >"$validation_runner_pid"
	wait "$runner_pid"
	printf '%s\n' "$?" >"$validation_status"
) 2>&1 | tee "$validation_gate_output"
if [ ! -s "$validation_status" ]; then
	echo "[gate] validation runner ended without a status" >&2
	exit 1
fi
status="$(cat "$validation_status")"
if [ "$status" -eq 124 ] || [ "$status" -eq 137 ]; then
	echo "[gate] validation exceeded the $validation_timeout wall-clock budget; inspect retained Go timeout stacks/output above" >&2
fi
if [ "$status" -eq 0 ]; then
	current_head="$(git rev-parse --verify HEAD)"
	if [ "$current_head" != "$candidate_head" ]; then
		echo "[gate] candidate_head=$candidate_head observed_head=$current_head canonical=false reason=head-moved-during-validation" >&2
		exit 1
	fi
	if [ -n "$(git status --porcelain)" ]; then
		echo "[gate] candidate_head=$candidate_head canonical=false reason=dirty-after-validation" >&2
		exit 1
	fi
	echo "[gate] candidate_head=$candidate_head canonical=false status=passed awaiting_exact_apply=true"
else
	if [ -s "$validation_artifact_result" ]; then
		cat "$validation_artifact_result" >&2
	fi
	echo "[gate] candidate_head=$candidate_head canonical=false status=failed exit_status=$status" >&2
fi
exit "$status"
