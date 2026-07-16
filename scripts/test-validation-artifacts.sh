#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd -P)"
fixture="$(mktemp -d "${TMPDIR:-/tmp}/azedarach-validation-artifacts.XXXXXX")"
trap 'rm -rf "$fixture"' EXIT

project_root="$fixture/project"
scratch_root="$fixture/scratch"
report_dir="$scratch_root/.tmp/test-timing/cold-run"
mkdir -p "$project_root/.azedarach/validation-artifacts/failures" "$report_dir"

cat >"$report_dir/report.json" <<'EOF'
{
  "exit_code": 1,
  "failures": [
    {"package": "example/broken", "test": "TestRetained", "output": "retained assertion failed"}
  ]
}
EOF
printf '# failed report\n' >"$report_dir/report.md"
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
  --evidence "$evidence" \
  --gate-output "$scratch_root/gate-output.log" \
  --request "$request_id" \
  --revision "$revision" \
  --issue dov \
  --exit-code 1 \
  --result "$result"

artifact_dir="$project_root/.azedarach/validation-artifacts/failures/$revision/$request_id"
test -f "$artifact_dir/manifest.json"
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

echo "validation artifact retention contract: PASS"
