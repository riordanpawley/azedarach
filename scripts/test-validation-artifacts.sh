#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd -P)"
fixture="$(mktemp -d "${TMPDIR:-/tmp}/azedarach-validation-artifacts.XXXXXX")"
fixture="$(cd "$fixture" && pwd -P)"
trap 'rm -rf "$fixture"' EXIT

project_root="$fixture/project"
scratch_root="$fixture/scratch"
report_dir="$scratch_root/.tmp/test-timing/cold-run"
mkdir -p "$project_root/.azedarach/validation-artifacts/failures" "$report_dir"

cat >"$report_dir/report.json" <<EOF
{
  "exit_code": 1,
  "raw_json_path": "$report_dir/events.jsonl",
  "stderr_path": "$report_dir/stderr.txt",
  "failures": [
    {"package": "example/broken", "output": "FAIL example/broken"},
    {"package": "example/broken", "test": "TestRetained", "output": "retained assertion failed"}
  ]
}
EOF
printf '# failed report\n\nRaw events: `%s`; stderr: `%s`\n' "$report_dir/events.jsonl" "$report_dir/stderr.txt" >"$report_dir/report.md"
printf '{"Action":"fail","Package":"example/broken","Test":"TestRetained"}\n' >"$report_dir/events.jsonl"
printf 'test stderr detail\n' >"$report_dir/stderr.txt"
printf '[gate] build started\n[gate] failing boundary detail\n' >"$scratch_root/gate-output.log"

request_id="dov-123-request"
revision="0123456789abcdef0123456789abcdef01234567"
evidence="$fixture/evidence.json"
result="$fixture/result.txt"
cat >"$evidence" <<EOF
{"held":true,"request_id":"$request_id","class":"aggregate","profile":"merge-gate","source_revision":"$revision","present":true,"report_path":"$report_dir/report.json","report_paths":["$report_dir/report.json"],"overlap_detected":false,"external_go_processes":0}
EOF

"$repo_root/scripts/publish-validation-artifacts" \
  --project-root "$project_root" \
  --candidate-root "$scratch_root" \
  --evidence "$evidence" \
  --gate-output "$scratch_root/gate-output.log" \
  --request "$request_id" \
  --revision "$revision" \
  --issue dov \
  --exit-code 1 \
  --result "$result"

artifact_dir="$project_root/.azedarach/validation-artifacts/failures/$revision/$request_id"
test -f "$artifact_dir/manifest.json"

non_test_request="dov-non-test-request"
non_test_evidence="$fixture/non-test-evidence.json"
non_test_result="$fixture/non-test-result.txt"
printf 'fatal boundary command exploded\n' >"$scratch_root/non-test-gate-output.log"
cat >"$non_test_evidence" <<EOF
{"held":true,"request_id":"$non_test_request","class":"aggregate","profile":"merge-gate","source_revision":"$revision","present":true,"report_paths":[],"overlap_detected":false,"external_go_processes":0}
EOF
"$repo_root/scripts/publish-validation-artifacts" \
  --project-root "$project_root" \
  --candidate-root "$scratch_root" \
  --evidence "$non_test_evidence" \
  --gate-output "$scratch_root/non-test-gate-output.log" \
  --request "$non_test_request" \
  --revision "$revision" \
  --exit-code 76 \
  --result "$non_test_result"
non_test_dir="$project_root/.azedarach/validation-artifacts/failures/$revision/$non_test_request"
test -f "$non_test_dir/manifest.json"
grep -q 'failure=fatal boundary command exploded' "$non_test_result"
grep -q "$non_test_dir/manifest.json" "$non_test_evidence"

failed_publish_request="dov-empty-request"
failed_publish_evidence="$fixture/failed-publish-evidence.json"
cat >"$failed_publish_evidence" <<EOF
{"held":true,"request_id":"$failed_publish_request","class":"aggregate","profile":"merge-gate","source_revision":"$revision","present":true,"report_paths":[],"overlap_detected":false,"external_go_processes":0}
EOF
if "$repo_root/scripts/publish-validation-artifacts" \
  --project-root "$project_root" \
  --candidate-root "$scratch_root" \
  --evidence "$failed_publish_evidence" \
  --gate-output "$scratch_root/missing-gate-output.log" \
  --request "$failed_publish_request" \
  --revision "$revision" \
  --exit-code 1 \
  --result "$fixture/failed-publish-result.txt" 2>"$fixture/failed-publish.stderr"; then
  echo "empty failed validation unexpectedly published" >&2
  exit 1
fi
grep -q 'did not produce publishable report or gate output' "$fixture/failed-publish.stderr"
empty_dir="$project_root/.azedarach/validation-artifacts/failures/$revision/$failed_publish_request"
test ! -e "$empty_dir"
test -z "$(find "$(dirname "$empty_dir")" -maxdepth 1 -name "$failed_publish_request.tmp.*" -print -quit)"

outside="$fixture/outside"
symlink_report_dir="$scratch_root/.tmp/test-timing/symlink-run"
mkdir -p "$outside" "$symlink_report_dir"
printf '{"secret":"must-not-copy"}\n' >"$outside/report.json"
ln -s "$outside/report.json" "$symlink_report_dir/report.json"
symlink_request="dov-symlink-request"
symlink_evidence="$fixture/symlink-evidence.json"
symlink_result="$fixture/symlink-result.txt"
cat >"$symlink_evidence" <<EOF
{"held":true,"request_id":"$symlink_request","class":"aggregate","profile":"merge-gate","source_revision":"$revision","present":true,"report_path":"$symlink_report_dir/report.json","report_paths":["$symlink_report_dir/report.json"],"overlap_detected":false,"external_go_processes":0}
EOF
"$repo_root/scripts/publish-validation-artifacts" \
  --project-root "$project_root" \
  --candidate-root "$scratch_root" \
  --evidence "$symlink_evidence" \
  --gate-output "$scratch_root/non-test-gate-output.log" \
  --request "$symlink_request" \
  --revision "$revision" \
  --exit-code 1 \
  --result "$symlink_result"
symlink_dir="$project_root/.azedarach/validation-artifacts/failures/$revision/$symlink_request"
test ! -e "$symlink_dir/reports/001/report.json"
! grep -R -q 'must-not-copy' "$symlink_dir"
test -f "$artifact_dir/gate-output.log"
for name in report.json report.md events.jsonl stderr.txt; do
  test -f "$artifact_dir/reports/001/$name"
done
grep -q '"request_id":"dov-123-request"' "$artifact_dir/manifest.json"
grep -q '"candidate_revision":"0123456789abcdef0123456789abcdef01234567"' "$artifact_dir/manifest.json"
grep -q 'example/broken::TestRetained' "$artifact_dir/manifest.json"
grep -q "validation_request=$request_id" "$result"
grep -q 'failure=example/broken::TestRetained: retained assertion failed' "$result"
grep -q "artifacts=$artifact_dir" "$result"
grep -q "$artifact_dir/reports/001/report.json" "$evidence"
grep -q '"kind":"issue"' "$artifact_dir/manifest.json"
grep -q '"id":"dov"' "$artifact_dir/manifest.json"
grep -F -q "\"raw_json_path\":\"$artifact_dir/reports/001/events.jsonl\"" "$artifact_dir/reports/001/report.json"
grep -F -q "\"stderr_path\":\"$artifact_dir/reports/001/stderr.txt\"" "$artifact_dir/reports/001/report.json"
grep -q "$artifact_dir/reports/001/events.jsonl" "$artifact_dir/reports/001/report.md"
grep -q "$artifact_dir/reports/001/stderr.txt" "$artifact_dir/reports/001/report.md"
! grep -q "$scratch_root" "$artifact_dir/reports/001/report.md"

orphan="$project_root/.azedarach/validation-artifacts/failures/$revision/orphan-request"
referenced="$project_root/.azedarach/validation-artifacts/failures/$revision/referenced-request"
mkdir -p "$orphan" "$referenced"
printf '{"request_id":"orphan-request","candidate_revision":"%s","created_at":"2000-01-01T00:00:00Z","references":[]}\n' "$revision" >"$orphan/manifest.json"
printf '{"request_id":"referenced-request","candidate_revision":"%s","created_at":"2000-01-01T00:00:00Z","references":[{"kind":"issue","id":"dov"}]}\n' "$revision" >"$referenced/manifest.json"

"$repo_root/scripts/publish-validation-artifacts" \
  --project-root "$project_root" \
  --prune-only

test ! -e "$orphan"
test -f "$referenced/manifest.json"
test -f "$artifact_dir/manifest.json"

for index in $(seq 1 22); do
  fresh="$project_root/.azedarach/validation-artifacts/failures/$revision/fresh-orphan-$index"
  mkdir -p "$fresh"
  printf '{"request_id":"fresh-orphan-%s","candidate_revision":"%s","created_at":"2099-01-01T00:00:00Z","references":[]}\n' "$index" "$revision" >"$fresh/manifest.json"
done
"$repo_root/scripts/publish-validation-artifacts" --project-root "$project_root" --prune-only
fresh_count="$(find "$project_root/.azedarach/validation-artifacts/failures/$revision" -maxdepth 1 -type d -name 'fresh-orphan-*' | wc -l | tr -d ' ')"
test "$fresh_count" = 20

echo "validation artifact retention contract: PASS"
