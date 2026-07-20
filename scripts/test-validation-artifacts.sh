#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd -P)"
publisher="$repo_root/scripts/publish-validation-artifacts"
fixture="$(mktemp -d "${TMPDIR:-/tmp}/azedarach-validation-artifacts.XXXXXX")"
fixture="$(cd "$fixture" && pwd -P)"
trap 'rm -rf "$fixture"' EXIT

project_root="$fixture/project"
scratch_root="$fixture/scratch"
control_root="$fixture/control"
report_dir="$scratch_root/.tmp/test-timing/cold-run"
mkdir -m 700 -p "$project_root" "$scratch_root" "$control_root" "$report_dir"

cat >"$report_dir/report.json" <<EOF
{"exit_code":1,"raw_json_path":"$report_dir/events.jsonl","stderr_path":"$report_dir/stderr.txt","failures":[{"package":"example/broken","test":"TestRetained","output":"retained assertion failed"}]}
EOF
printf '# failed report\n' >"$report_dir/report.md"
printf '{"Action":"fail","Package":"example/broken","Test":"TestRetained"}\n' >"$report_dir/events.jsonl"
printf 'test stderr detail\n' >"$report_dir/stderr.txt"
printf '[gate] build started\n[gate] failing boundary detail\n' >"$control_root/gate-output.log"

request_id="dov-123-request"
revision="0123456789abcdef0123456789abcdef01234567"
evidence="$control_root/evidence.json"
cat >"$evidence" <<EOF
{"held":true,"request_id":"$request_id","source_revision":"$revision","publication_nonce":"fixture-nonce","issue_id":"dov","reviewer_id":"reviewer-1","review_epoch_event_id":42,"report_path":"$report_dir/report.json","report_paths":["$report_dir/report.json"]}
EOF

result="$($publisher \
  --project-root "$project_root" \
  --candidate-root "$scratch_root" \
  --control-root "$control_root" \
  --evidence "$evidence" \
  --gate-output "$control_root/gate-output.log" \
  --request "$request_id" \
  --revision "$revision" \
  --exit-code 1)"

artifact_dir="$project_root/.azedarach/validation-artifacts/failures/$revision/$request_id"
test -f "$artifact_dir/manifest.json"
for name in report.json report.md events.jsonl stderr.txt; do
  test -f "$artifact_dir/reports/001/$name"
done
test -f "$artifact_dir/gate-output.log"
grep -q 'example/broken::TestRetained' "$artifact_dir/manifest.json"
grep -q '"kind":"validation_request"' "$artifact_dir/manifest.json"
grep -q '"kind":"issue"' "$artifact_dir/manifest.json"
grep -q '"kind":"reviewer"' "$artifact_dir/manifest.json"
grep -q '"kind":"review_epoch"' "$artifact_dir/manifest.json"
grep -q "validation_request=$request_id" <<<"$result"
grep -q "artifacts=$artifact_dir" <<<"$result"
grep -q "$artifact_dir/reports/001/report.json" "$evidence"

# Repeated publication validates the committed manifest and repairs the control
# evidence rather than accepting an unverified destination.
idempotent="$($publisher \
  --project-root "$project_root" --candidate-root "$scratch_root" \
  --control-root "$control_root" --evidence "$evidence" \
  --gate-output "$control_root/gate-output.log" --request "$request_id" \
  --revision "$revision" --exit-code 1)"
grep -q "artifacts=$artifact_dir" <<<"$idempotent"

# A non-test fatal failure still receives the complete standard bundle.
fatal_request="dov-fatal-request"
fatal_evidence="$control_root/fatal-evidence.json"
cat >"$fatal_evidence" <<EOF
{"held":true,"request_id":"$fatal_request","source_revision":"$revision","publication_nonce":"fatal-nonce","issue_id":"dov","fatal_phase":"toolchain_configuration","fatal_detail":"required Go toolchain unavailable","report_paths":[]}
EOF
fatal_result="$($publisher \
  --project-root "$project_root" --candidate-root "$scratch_root" \
  --control-root "$control_root" --evidence "$fatal_evidence" \
  --gate-output "$control_root/gate-output.log" --request "$fatal_request" \
  --revision "$revision" --exit-code 76)"
fatal_dir="$project_root/.azedarach/validation-artifacts/failures/$revision/$fatal_request"
for name in report.json report.md events.jsonl stderr.txt; do
  test -f "$fatal_dir/reports/001/$name"
done
grep -q 'required Go toolchain unavailable' <<<"$fatal_result"
grep -q 'required Go toolchain unavailable' "$fatal_dir/reports/001/report.json"

# Candidate-controlled source symlinks are ignored and cannot escape the
# candidate root; the trusted gate output still produces a standard bundle.
outside="$fixture/outside"
mkdir "$outside"
printf 'must-not-copy\n' >"$outside/report.json"
symlink_dir="$scratch_root/.tmp/test-timing/symlink-run"
mkdir -p "$symlink_dir"
ln -s "$outside/report.json" "$symlink_dir/report.json"
symlink_request="dov-source-symlink"
symlink_evidence="$control_root/symlink-evidence.json"
cat >"$symlink_evidence" <<EOF
{"request_id":"$symlink_request","source_revision":"$revision","publication_nonce":"symlink-nonce","issue_id":"dov","report_path":"$symlink_dir/report.json"}
EOF
$publisher --project-root "$project_root" --candidate-root "$scratch_root" \
  --control-root "$control_root" --evidence "$symlink_evidence" \
  --gate-output "$control_root/gate-output.log" --request "$symlink_request" \
  --revision "$revision" --exit-code 1 >/dev/null
symlink_bundle="$project_root/.azedarach/validation-artifacts/failures/$revision/$symlink_request/reports/001"
! grep -R -q 'must-not-copy' "$symlink_bundle"

# Candidate parent replacement after traversal cannot redirect any report
# artifact: every source open is relative to the already-verified directory
# handle, not to the replaced pathname.
race_report_dir="$scratch_root/.tmp/test-timing/parent-race"
race_original_dir="$scratch_root/.tmp/test-timing/parent-race-original"
race_outside_dir="$outside/parent-race"
mkdir -p "$race_report_dir" "$race_outside_dir"
printf '{"exit_code":1,"failures":[]}\n' >"$race_report_dir/report.json"
printf 'trusted report markdown\n' >"$race_report_dir/report.md"
printf 'trusted events\n' >"$race_report_dir/events.jsonl"
printf 'trusted stderr\n' >"$race_report_dir/stderr.txt"
printf '{"exit_code":1,"failures":[{"output":"must-not-copy-report"}]}\n' >"$race_outside_dir/report.json"
printf 'must-not-copy-markdown\n' >"$race_outside_dir/report.md"
printf 'must-not-copy-events\n' >"$race_outside_dir/events.jsonl"
printf 'must-not-copy-stderr\n' >"$race_outside_dir/stderr.txt"
race_request="dov-parent-race"
race_evidence="$control_root/parent-race-evidence.json"
race_ready="$fixture/parent-race-ready"
race_release="$fixture/parent-race-release"
cat >"$race_evidence" <<EOF
{"request_id":"$race_request","source_revision":"$revision","publication_nonce":"parent-race-nonce","issue_id":"dov","report_path":"$race_report_dir/report.json"}
EOF
AZEDARACH_VALIDATION_TEST_CANDIDATE_DIRFD_READY_FILE="$race_ready" \
AZEDARACH_VALIDATION_TEST_CANDIDATE_DIRFD_RELEASE_FILE="$race_release" \
  "$publisher" --project-root "$project_root" --candidate-root "$scratch_root" \
  --control-root "$control_root" --evidence "$race_evidence" \
  --gate-output "$control_root/gate-output.log" --request "$race_request" \
  --revision "$revision" --exit-code 1 >/dev/null &
race_publisher_pid=$!
while [[ ! -e "$race_ready" ]]; do kill -0 "$race_publisher_pid"; done
mv "$race_report_dir" "$race_original_dir"
ln -s "$race_outside_dir" "$race_report_dir"
: >"$race_release"
wait "$race_publisher_pid"
race_bundle="$project_root/.azedarach/validation-artifacts/failures/$revision/$race_request/reports/001"
grep -q 'trusted report markdown' "$race_bundle/report.md"
grep -q 'trusted events' "$race_bundle/events.jsonl"
grep -q 'trusted stderr' "$race_bundle/stderr.txt"
! grep -R -q 'must-not-copy' "$race_bundle"

# Trusted control inputs reject symlinks rather than following them.
printf 'outside-evidence\n' >"$outside/evidence.json"
ln -s "$outside/evidence.json" "$control_root/evidence-link.json"
if $publisher --project-root "$project_root" --candidate-root "$scratch_root" \
  --control-root "$control_root" --evidence "$control_root/evidence-link.json" \
  --gate-output "$control_root/gate-output.log" --request bad-control \
  --revision "$revision" --exit-code 1 2>"$fixture/control-symlink.stderr"; then
  echo "symlinked control evidence unexpectedly accepted" >&2
  exit 1
fi
grep -q 'control file is unsafe' "$fixture/control-symlink.stderr"
grep -q 'outside-evidence' "$outside/evidence.json"

# Artifact-root and final-destination symlinks are rejected without touching
# their targets.
unsafe_project="$fixture/unsafe-project"
unsafe_target="$fixture/unsafe-target"
mkdir "$unsafe_project" "$unsafe_target"
ln -s "$unsafe_target" "$unsafe_project/.azedarach"
if $publisher --project-root "$unsafe_project" --prune-only 2>"$fixture/root-symlink.stderr"; then
  echo "symlinked artifact root unexpectedly accepted" >&2
  exit 1
fi
grep -q 'not a real directory' "$fixture/root-symlink.stderr"

destination_request="dov-destination-symlink"
destination_evidence="$control_root/destination-evidence.json"
cat >"$destination_evidence" <<EOF
{"request_id":"$destination_request","source_revision":"$revision","publication_nonce":"destination-nonce","issue_id":"dov"}
EOF
ln -s "$unsafe_target" "$project_root/.azedarach/validation-artifacts/failures/$revision/$destination_request"
if $publisher --project-root "$project_root" --candidate-root "$scratch_root" \
  --control-root "$control_root" --evidence "$destination_evidence" \
  --gate-output "$control_root/gate-output.log" --request "$destination_request" \
  --revision "$revision" --exit-code 1 2>"$fixture/destination-symlink.stderr"; then
  echo "symlinked final destination unexpectedly accepted" >&2
  exit 1
fi
grep -q 'existing artifact destination is unsafe' "$fixture/destination-symlink.stderr"
test -z "$(find "$unsafe_target" -mindepth 1 -print -quit)"

# Existing destinations are checksum-validated on retry.
printf 'tampered\n' >>"$artifact_dir/reports/001/stderr.txt"
if $publisher --project-root "$project_root" --candidate-root "$scratch_root" \
  --control-root "$control_root" --evidence "$evidence" \
  --gate-output "$control_root/gate-output.log" --request "$request_id" \
  --revision "$revision" --exit-code 1 2>"$fixture/tamper.stderr"; then
  echo "tampered committed artifact unexpectedly accepted" >&2
  exit 1
fi
grep -q 'checksum mismatch' "$fixture/tamper.stderr"

# Only unreferenced manual fixtures are pruned; durable request references are
# always retained.
orphan="$project_root/.azedarach/validation-artifacts/failures/$revision/orphan-request"
mkdir "$orphan"
printf '{"request_id":"orphan-request","candidate_revision":"%s","created_at":"2000-01-01T00:00:00Z","references":[]}\n' "$revision" >"$orphan/manifest.json"
$publisher --project-root "$project_root" --prune-only
test ! -e "$orphan"
test -f "$fatal_dir/manifest.json"

echo "validation artifact retention contract: PASS"
