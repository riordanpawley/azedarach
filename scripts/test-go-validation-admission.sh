#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd -P)"
fixture="$(mktemp -d "${TMPDIR:-/tmp}/azedarach-go-admission.XXXXXX")"
trap 'rm -rf "$fixture"' EXIT
mkdir -p "$fixture/bin"

cat >"$fixture/bin/admit" <<'EOF'
#!/usr/bin/env bash
while [[ "$(cat "$ADMISSION_STATE_FILE")" == "active" ]]; do sleep 0.02; done
while [[ "$1" != "--" ]]; do shift; done
shift
exec "$@"
EOF
cat >"$fixture/bin/real-go" <<'EOF'
#!/usr/bin/env bash
printf 'started\n' >"$ADMISSION_STARTED_FILE"
EOF
chmod +x "$fixture/bin/admit" "$fixture/bin/real-go"

state="$fixture/aggregate-state"
started="$fixture/go-started"
printf 'active\n' >"$state"
env -u AZEDARACH_VALIDATION_REQUEST_ID -u AZEDARACH_VALIDATION_NESTED_FD \
  ADMISSION_STATE_FILE="$state" ADMISSION_STARTED_FILE="$started" \
  AZEDARACH_REAL_GO_BIN="$fixture/bin/real-go" \
  AZEDARACH_GO_ADMISSION_WRAPPER="$fixture/bin/admit" \
  "$repo_root/scripts/validation-bin/go" test ./... &
guard_pid=$!

sleep 0.2
if [[ -e "$started" ]]; then
  echo "guarded Go started while aggregate validation was active" >&2
  exit 1
fi
printf 'queued\n' >"$state"
wait "$guard_pid"
test -e "$started"

echo "go validation admission regression passed"
