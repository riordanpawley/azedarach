#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd -P)"
fixture="$(mktemp -d "${TMPDIR:-/tmp}/azedarach-go-admission.XXXXXX")"
trap 'rm -rf "$fixture"' EXIT
mkdir -p "$fixture/bin"

cat >"$fixture/bin/admit" <<'EOF'
#!/usr/bin/env bash
printf 'called\n' >"$ADMISSION_CALLED_FILE"
exit 97
EOF
cat >"$fixture/bin/real-go" <<'EOF'
#!/usr/bin/env bash
printf 'started\n' >"$ADMISSION_STARTED_FILE"
EOF
chmod +x "$fixture/bin/admit" "$fixture/bin/real-go"

started="$fixture/go-started"
called="$fixture/admission-called"
env -u AZEDARACH_VALIDATION_REQUEST_ID -u AZEDARACH_VALIDATION_NESTED_FD \
  ADMISSION_STARTED_FILE="$started" ADMISSION_CALLED_FILE="$called" \
  AZEDARACH_REAL_GO_BIN="$fixture/bin/real-go" \
  AZEDARACH_GO_ADMISSION_WRAPPER="$fixture/bin/admit" \
  "$repo_root/scripts/validation-bin/go" test ./...
test -e "$started"
test ! -e "$called"

lease_run_count="$(grep -c 'with-machine-validation-lease --class' "$repo_root/justfile")"
if [[ "$lease_run_count" -ne 3 ]]; then
  echo "justfile has $lease_run_count admission-wrapped run recipes, want controlled timing plus push/review publication" >&2
  exit 1
fi
grep -q 'with-machine-validation-lease --class aggregate --scope repository --purpose capacity' "$repo_root/justfile"
grep -q 'with-machine-validation-lease --class aggregate --profile merge-gate' "$repo_root/justfile"
grep -q 'with-machine-validation-lease --class aggregate --scope ticket --purpose review_evidence' "$repo_root/justfile"

echo "go development bypass regression passed"
