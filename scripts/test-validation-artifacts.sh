#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd -P)"
publisher="$repo_root/scripts/publish-validation-artifacts"
fixture="$(mktemp -d "${TMPDIR:-/tmp}/azedarach-validation-artifacts.XXXXXX")"
fixture="$(cd "$fixture" && pwd -P)"
trap 'rm -rf "$fixture"' EXIT

# Capability discovery is consumer-facing and deterministic: an unrelated
# consumer can install the publisher outside Azedarach's repository layout and
# determine whether secure publication is supported on every release platform.
consumer_tools="$fixture/example-consumer/tools"
mkdir -p "$consumer_tools"
cp "$publisher" "$consumer_tools/publish-validation-artifacts"
consumer_publisher="$consumer_tools/publish-validation-artifacts"
for platform in darwin/amd64 darwin/arm64 linux/amd64; do
  capability="$($consumer_publisher --capability-check --platform "$platform")"
  grep -q '"schema":"azedarach.validation_artifact_capability.v1"' <<<"$capability"
  grep -q '"available":true' <<<"$capability"
done
if "$consumer_publisher" --capability-check --platform plan9/mips >"$fixture/absent-capability.json"; then
  echo "absent publication capability unexpectedly reported available" >&2
  exit 1
else
  test "$?" -eq 78
fi
grep -q '"available":false' "$fixture/absent-capability.json"
grep -q 'descriptor-relative openat syscall is not registered' "$fixture/absent-capability.json"

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

# Replacing the candidate root pathname after trusted traversal cannot redirect
# traversal: the publisher retains the root handle acquired by that traversal.
root_race_report="$scratch_root/.tmp/test-timing/root-race"
root_race_original="$fixture/scratch-root-race-original"
root_race_outside="$outside/root-race"
mkdir -p "$root_race_report" "$root_race_outside/.tmp/test-timing/root-race"
printf '{"exit_code":1,"failures":[]}\n' >"$root_race_report/report.json"
printf 'trusted root report\n' >"$root_race_report/report.md"
printf 'must-not-copy-root\n' >"$root_race_outside/.tmp/test-timing/root-race/report.md"
printf '{"exit_code":1,"failures":[]}\n' >"$root_race_outside/.tmp/test-timing/root-race/report.json"
root_race_request="dov-root-race"
root_race_evidence="$control_root/root-race-evidence.json"
root_race_ready="$fixture/root-race-ready"
root_race_release="$fixture/root-race-release"
cat >"$root_race_evidence" <<EOF
{"request_id":"$root_race_request","source_revision":"$revision","publication_nonce":"root-race-nonce","issue_id":"dov","report_path":"$root_race_report/report.json"}
EOF
AZEDARACH_VALIDATION_TEST_CANDIDATE_ROOTFD_READY_FILE="$root_race_ready" \
AZEDARACH_VALIDATION_TEST_CANDIDATE_ROOTFD_RELEASE_FILE="$root_race_release" \
  "$publisher" --project-root "$project_root" --candidate-root "$scratch_root" \
  --control-root "$control_root" --evidence "$root_race_evidence" \
  --gate-output "$control_root/gate-output.log" --request "$root_race_request" \
  --revision "$revision" --exit-code 1 >/dev/null &
root_race_publisher_pid=$!
while [[ ! -e "$root_race_ready" ]]; do kill -0 "$root_race_publisher_pid"; done
mv "$scratch_root" "$root_race_original"
ln -s "$root_race_outside" "$scratch_root"
: >"$root_race_release"
wait "$root_race_publisher_pid"
root_race_bundle="$project_root/.azedarach/validation-artifacts/failures/$revision/$root_race_request/reports/001"
grep -q 'trusted root report' "$root_race_bundle/report.md"
! grep -R -q 'must-not-copy-root' "$root_race_bundle"
unlink "$scratch_root"
mv "$root_race_original" "$scratch_root"

# Candidate artifacts must be isolated inodes. A hard link can expose bytes
# owned outside the disposable candidate even though its pathname is inside.
for hardlink_name in report.json report.md events.jsonl stderr.txt; do
  hardlink_request="dov-hardlink-${hardlink_name//./-}"
  hardlink_dir="$scratch_root/.tmp/test-timing/$hardlink_request"
  mkdir -p "$hardlink_dir"
  printf '{"exit_code":1,"failures":[]}\n' >"$hardlink_dir/report.json"
  printf 'trusted markdown\n' >"$hardlink_dir/report.md"
  printf 'trusted events\n' >"$hardlink_dir/events.jsonl"
  printf 'trusted stderr\n' >"$hardlink_dir/stderr.txt"
  printf 'must-not-copy-hardlink-%s\n' "$hardlink_name" >"$outside/hardlink-$hardlink_name"
  unlink "$hardlink_dir/$hardlink_name"
  ln "$outside/hardlink-$hardlink_name" "$hardlink_dir/$hardlink_name"
  hardlink_evidence="$control_root/$hardlink_request-evidence.json"
  cat >"$hardlink_evidence" <<EOF
{"request_id":"$hardlink_request","source_revision":"$revision","publication_nonce":"$hardlink_request-nonce","issue_id":"dov","report_path":"$hardlink_dir/report.json"}
EOF
  "$publisher" --project-root "$project_root" --candidate-root "$scratch_root" \
    --control-root "$control_root" --evidence "$hardlink_evidence" \
    --gate-output "$control_root/gate-output.log" --request "$hardlink_request" \
    --revision "$revision" --exit-code 1 >/dev/null
  hardlink_bundle="$project_root/.azedarach/validation-artifacts/failures/$revision/$hardlink_request"
  ! grep -R -q "must-not-copy-hardlink-$hardlink_name" "$hardlink_bundle"
done

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

# A destination directory that is renamed out of the trusted project after its
# handle is acquired must never receive later writes or END-block cleanup. Test
# every retained destination layer used by publication.
for destination_phase in artifact-root revision stage write-open write-chunk; do
  detach_project="$fixture/detach-$destination_phase-project"
  detach_control="$fixture/detach-$destination_phase-control"
  detach_outside="$fixture/detach-$destination_phase-outside"
  detach_request="dov-detach-$destination_phase"
  detach_evidence="$detach_control/evidence.json"
  detach_ready="$fixture/detach-$destination_phase-ready"
  detach_release="$fixture/detach-$destination_phase-release"
  mkdir -p "$detach_project" "$detach_control" "$detach_outside"
  cp "$control_root/gate-output.log" "$detach_control/gate-output.log"
  cat >"$detach_evidence" <<EOF
{"request_id":"$detach_request","source_revision":"$revision","publication_nonce":"detach-$destination_phase-nonce","issue_id":"dov","report_path":"$report_dir/report.json"}
EOF

  AZEDARACH_VALIDATION_TEST_DESTINATION_PHASE="$destination_phase" \
  AZEDARACH_VALIDATION_TEST_DESTINATION_READY_FILE="$detach_ready" \
  AZEDARACH_VALIDATION_TEST_DESTINATION_RELEASE_FILE="$detach_release" \
    "$publisher" --project-root "$detach_project" --candidate-root "$scratch_root" \
    --control-root "$detach_control" --evidence "$detach_evidence" \
    --gate-output "$detach_control/gate-output.log" --request "$detach_request" \
    --revision "$revision" --exit-code 1 >"$fixture/detach-$destination_phase.stdout" \
    2>"$fixture/detach-$destination_phase.stderr" &
  detach_pid=$!
  while [[ ! -e "$detach_ready" ]]; do kill -0 "$detach_pid"; done

  case "$destination_phase" in
    artifact-root)
      detach_path="$detach_project/.azedarach/validation-artifacts"
      replacement_path="$detach_project/.azedarach/validation-artifacts"
      ;;
    revision)
      detach_path="$detach_project/.azedarach/validation-artifacts/failures/$revision"
      replacement_path="$detach_path"
      ;;
    stage|write-open|write-chunk)
      detach_path="$(find "$detach_project/.azedarach/validation-artifacts/failures/$revision" -maxdepth 1 -type d -name ".$detach_request.tmp.*" -print -quit)"
      replacement_path="$detach_path"
      ;;
  esac
  detached_tree="$detach_outside/relocated"
  mv "$detach_path" "$detached_tree"
  mkdir -p "$replacement_path"
  printf 'outside sentinel\n' >"$detached_tree/sentinel"
  : >"$detach_release"
  if wait "$detach_pid"; then
    echo "detached $destination_phase destination unexpectedly accepted" >&2
    exit 1
  fi
  grep -Eq 'destination (detached from trusted project root|attachment changed)' "$fixture/detach-$destination_phase.stderr"
  test -f "$detached_tree/sentinel"
  test -z "$(find "$detached_tree" -type f ! -name sentinel -size +0c -print -quit)"
done

# END cleanup revalidates the dynamically opened stage edge immediately before
# recursive deletion. Relocating that edge must preserve both outside trees.
cleanup_project="$fixture/cleanup-project"
cleanup_control="$fixture/cleanup-control"
cleanup_outside="$fixture/cleanup-outside"
cleanup_request="dov-cleanup-detach"
cleanup_ready="$fixture/cleanup-ready"
cleanup_release="$fixture/cleanup-release"
mkdir -p "$cleanup_project" "$cleanup_control" "$cleanup_outside"
cp "$control_root/gate-output.log" "$cleanup_control/gate-output.log"
cat >"$cleanup_control/evidence.json" <<EOF
{"request_id":"$cleanup_request","source_revision":"$revision","publication_nonce":"cleanup-nonce","issue_id":"dov"}
EOF
AZEDARACH_VALIDATION_TEST_DESTINATION_PHASE=remove-tree \
AZEDARACH_VALIDATION_TEST_DESTINATION_READY_FILE="$cleanup_ready" \
AZEDARACH_VALIDATION_TEST_DESTINATION_RELEASE_FILE="$cleanup_release" \
AZEDARACH_VALIDATION_TEST_FAIL_AFTER_STAGE=1 \
  "$publisher" --project-root "$cleanup_project" --candidate-root "$scratch_root" \
  --control-root "$cleanup_control" --evidence "$cleanup_control/evidence.json" \
  --gate-output "$cleanup_control/gate-output.log" --request "$cleanup_request" \
  --revision "$revision" --exit-code 1 >"$fixture/cleanup-detach.stdout" \
  2>"$fixture/cleanup-detach.stderr" &
cleanup_pid=$!
while [[ ! -e "$cleanup_ready" ]]; do kill -0 "$cleanup_pid"; done
cleanup_stage="$(find "$cleanup_project/.azedarach/validation-artifacts/failures/$revision" -maxdepth 1 -type d -name ".$cleanup_request.tmp.*" -print -quit)"
mv "$cleanup_stage" "$cleanup_outside/relocated"
mkdir "$cleanup_stage"
printf 'outside sentinel\n' >"$cleanup_outside/relocated/sentinel"
printf 'replacement sentinel\n' >"$cleanup_stage/sentinel"
: >"$cleanup_release"
if wait "$cleanup_pid"; then
  echo "detached END cleanup unexpectedly accepted" >&2
  exit 1
fi
grep -Eq 'destination (detached from trusted project root|attachment changed)' "$fixture/cleanup-detach.stderr"
test -f "$cleanup_outside/relocated/sentinel"
test -f "$cleanup_stage/sentinel"

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

# Prune pins the exact selected request inode. Relocating it after enumeration
# must fail closed without deleting either the relocated tree or a replacement.
prune_ready="$fixture/prune-ready"
prune_release="$fixture/prune-release"
prune_outside="$fixture/prune-outside"
mkdir "$prune_outside"
AZEDARACH_VALIDATION_TEST_DESTINATION_PHASE=prune \
AZEDARACH_VALIDATION_TEST_DESTINATION_READY_FILE="$prune_ready" \
AZEDARACH_VALIDATION_TEST_DESTINATION_RELEASE_FILE="$prune_release" \
  "$publisher" --project-root "$project_root" --prune-only \
  >"$fixture/prune-detach.stdout" 2>"$fixture/prune-detach.stderr" &
prune_pid=$!
while [[ ! -e "$prune_ready" ]]; do kill -0 "$prune_pid"; done
mv "$orphan" "$prune_outside/relocated"
mkdir "$orphan"
printf 'replacement sentinel\n' >"$orphan/sentinel"
printf 'outside sentinel\n' >"$prune_outside/relocated/sentinel"
: >"$prune_release"
if wait "$prune_pid"; then
  echo "detached prune request unexpectedly accepted" >&2
  exit 1
fi
grep -Eq 'destination (detached|identity changed)' "$fixture/prune-detach.stderr"
test -f "$orphan/sentinel"
test -f "$prune_outside/relocated/sentinel"

# A normal prune still removes the original unreferenced fixture.
rm -r "$orphan"
mv "$prune_outside/relocated" "$orphan"
$publisher --project-root "$project_root" --prune-only
test ! -e "$orphan"
test -f "$fatal_dir/manifest.json"

echo "validation artifact retention contract: PASS"
