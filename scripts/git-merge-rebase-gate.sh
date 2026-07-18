#!/usr/bin/env sh
set -eu

if [ "${AZEDARACH_SKIP_MERGE_REBASE_GATE:-0}" = "1" ]; then
  echo "merge/rebase gate skipped for internal scratch integration (AZEDARACH_SKIP_MERGE_REBASE_GATE=1)"
  exit 0
fi

repo_root="$(git rev-parse --show-toplevel)"
target_gate_root="${AZEDARACH_TARGET_GATE_ROOT:-$repo_root}"
target_gate_root="$(cd "$target_gate_root" && pwd -P)"
target_git_common_dir="$(git -C "$target_gate_root" rev-parse --path-format=absolute --git-common-dir)"
project_root="$(dirname "$target_git_common_dir")"
candidate_head="${AZEDARACH_CANDIDATE_HEAD:-$(git rev-parse --verify HEAD)}"
validation_body="${AZEDARACH_MERGE_GATE_BODY:-$repo_root/scripts/git-merge-rebase-gate-body.sh}"
trusted_scripts="$target_gate_root/scripts"
validation_wrapper="$trusted_scripts/with-machine-validation-lease"
artifact_publisher="$trusted_scripts/publish-validation-artifacts"
control_dir="$(mktemp -d -t azedarach-merge-gate-control.XXXXXX)"
chmod 700 "$control_dir"
validation_status="$control_dir/status"
validation_runner_pid="$control_dir/runner-pid"
validation_gate_output="$control_dir/gate-output.log"
validation_evidence="$control_dir/evidence.json"
publication_failed=0
umask 077
: >"$validation_gate_output"

unset AZEDARACH_TICKET_ID AZEDARACH_ISSUE_ID
export AZEDARACH_VALIDATION_SCOPE=repository
export AZEDARACH_VALIDATION_PURPOSE=push_gate

gate_log() {
  printf '%s\n' "$*" | tee -a "$validation_gate_output" >&2
}

cleanup() {
  if [ "$publication_failed" -ne 0 ]; then
    printf '%s\n' "[gate] trusted control bundle preserved at $control_dir" >&2
    return
  fi
  rm -rf "$control_dir"
}

ensure_failure_evidence() {
  phase="$1"
  detail="$2"
  if [ -s "$validation_evidence" ] && [ ! -L "$validation_evidence" ] && valid_failure_evidence; then
    return 0
  fi
  rm -f "$validation_evidence"
  synthetic_request="outer-${candidate_head}-$$"
  temporary="$control_dir/evidence.tmp.$$"
  perl -MJSON::PP -e '
    my ($path,$request,$revision,$issue,$phase,$detail)=@ARGV;
    open my $random, "<:raw", "/dev/urandom" or die $!;
    read($random, my $bytes, 32) == 32 or die "read publication nonce";
    close $random;
    my $nonce=unpack("H*", $bytes);
    open my $out, ">", $path or die $!;
    chmod 0600, $path;
    print {$out} encode_json({
      held=>JSON::PP::false, present=>JSON::PP::true,
      synthetic_request=>JSON::PP::true,
      request_id=>$request, authoritative_request_id=>$request,
      source_revision=>$revision, issue_id=>$issue,
      reviewer_id=>($ENV{AZEDARACH_REVIEWER_ID}||""),
      review_epoch_event_id=>0+($ENV{AZEDARACH_REVIEW_EPOCH_EVENT_ID}||0),
      fatal_phase=>$phase, fatal_detail=>$detail,
      publication_nonce=>$nonce,
    })."\n";
    close $out or die $!;
  ' "$temporary" "$synthetic_request" "$candidate_head" "${AZEDARACH_CANDIDATE_ISSUE_ID:-}" "$phase" "$detail"
  mv "$temporary" "$validation_evidence"
}

valid_failure_evidence() {
  perl -MJSON::PP -e '
    my ($path, $revision)=@ARGV;
    open my $in, "<", $path or exit 1;
    local $/;
    my $value=eval { decode_json(<$in>||"{}") };
    close $in;
    exit 1 unless ref($value) eq "HASH";
    exit 1 unless ($value->{request_id}||"") =~ /^[A-Za-z0-9_.-]+$/;
    exit 1 unless ($value->{source_revision}||"") eq $revision;
    exit 1 unless ($value->{publication_nonce}||"") =~ /^[A-Za-z0-9_.-]+$/;
  ' "$validation_evidence" "$candidate_head"
}

evidence_field() {
  perl -MJSON::PP -e '
    my ($path,$field)=@ARGV;
    open my $in, "<", $path or die $!;
    local $/; my $value=decode_json(<$in>||"{}");
    print $value->{$field} // "";
  ' "$validation_evidence" "$1"
}

publish_failure() {
  status="$1"
  phase="$2"
  detail="$3"
  gate_log "[gate] candidate_head=$candidate_head canonical=false status=failed phase=$phase exit_status=$status detail=$detail"
  ensure_failure_evidence "$phase" "$detail"
  request_id="$(evidence_field request_id)"
  source_revision="$(evidence_field source_revision)"
  set +e
  publication_output="$(
    "$artifact_publisher" \
      --project-root "$project_root" \
      --candidate-root "$repo_root" \
      --control-root "$control_dir" \
      --evidence "$validation_evidence" \
      --gate-output "$validation_gate_output" \
      --request "$request_id" \
      --revision "$source_revision" \
      --exit-code "$status" \
      --fatal-phase "$phase" \
      --fatal-detail "$detail"
  )"
  publication_status=$?
  set -e
  if [ -n "$publication_output" ]; then
    printf '%s\n' "$publication_output" >&2
  fi
  if [ "$publication_status" -ne 0 ]; then
    publication_failed=1
    gate_log "[gate] candidate_head=$candidate_head failure=artifact-publication-failed exit_status=$publication_status"
  fi
}

cancelled() {
  trap - INT TERM
  if [ -s "$validation_runner_pid" ] && [ ! -L "$validation_runner_pid" ]; then
    runner_pid="$(cat "$validation_runner_pid")"
    kill -TERM "-$runner_pid" 2>/dev/null || kill -TERM "$runner_pid" 2>/dev/null || true
    sleep 0.2
    kill -KILL "-$runner_pid" 2>/dev/null || kill -KILL "$runner_pid" 2>/dev/null || true
  fi
  publish_failure 130 outer_gate_cancelled "validation gate was cancelled"
  exit 130
}

trap cleanup EXIT
trap cancelled INT TERM

current_head="$(git rev-parse --verify HEAD)"
if [ "$current_head" != "$candidate_head" ]; then
  publish_failure 1 head_mismatch_before_start "observed HEAD $current_head"
  exit 1
fi
if [ -n "$(git status --porcelain)" ]; then
  publish_failure 1 dirty_before_start "candidate worktree was dirty before validation"
  exit 1
fi

gate_log "[gate] candidate_head=$candidate_head canonical=false status=running"

timeout_cmd=""
if command -v timeout >/dev/null 2>&1; then
  timeout_cmd="timeout"
elif command -v gtimeout >/dev/null 2>&1; then
  timeout_cmd="gtimeout"
else
  publish_failure 1 missing_timeout "GNU timeout is required"
  exit 1
fi
if [ ! -x "$validation_wrapper" ]; then
  publish_failure 1 missing_validation_wrapper "trusted validation wrapper is unavailable"
  exit 1
fi
validation_timeout="${AZEDARACH_MERGE_GATE_TIMEOUT:-10m}"
(
  set +e
  AZEDARACH_VALIDATION_PUBLICATION_EVIDENCE="$validation_evidence" \
  AZEDARACH_CANDIDATE_ISSUE_ID="${AZEDARACH_CANDIDATE_ISSUE_ID:-}" \
    "$timeout_cmd" --signal=TERM --kill-after=15s "$validation_timeout" \
    "$validation_wrapper" --class aggregate --profile merge-gate -- "$validation_body" &
  runner_pid=$!
  printf '%s\n' "$runner_pid" >"$validation_runner_pid"
  wait "$runner_pid"
  printf '%s\n' "$?" >"$validation_status"
) 2>&1 | tee -a "$validation_gate_output"

if [ ! -s "$validation_status" ] || [ -L "$validation_status" ]; then
  publish_failure 1 missing_runner_status "validation runner ended without a trusted status"
  exit 1
fi
status="$(cat "$validation_status")"
if [ "$status" -eq 124 ] || [ "$status" -eq 137 ]; then
  publish_failure "$status" validation_timeout "validation exceeded the $validation_timeout wall-clock budget"
  exit "$status"
fi
if [ "$status" -ne 0 ]; then
  publish_failure "$status" validation_payload "validation payload exited $status"
  exit "$status"
fi

current_head="$(git rev-parse --verify HEAD)"
if [ "$current_head" != "$candidate_head" ]; then
  publish_failure 1 head_moved_during_validation "observed HEAD $current_head"
  exit 1
fi
if [ -n "$(git status --porcelain)" ]; then
  publish_failure 1 dirty_after_validation "candidate worktree became dirty during validation"
  exit 1
fi

gate_log "[gate] candidate_head=$candidate_head canonical=false status=passed awaiting_exact_apply=true"
exit 0
