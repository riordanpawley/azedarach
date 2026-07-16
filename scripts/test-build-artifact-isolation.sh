#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
fixture="$(mktemp -d "${TMPDIR:-/tmp}/azedarach-build-contract.XXXXXX")"
tui_pid=""
tui_release=""
cleanup_fixture() {
  if [[ -n "$tui_pid" ]] && kill -0 "$tui_pid" 2>/dev/null; then
    if [[ -n "$tui_release" ]]; then
      : >"$tui_release"
    fi
    kill "$tui_pid" 2>/dev/null || true
    wait "$tui_pid" 2>/dev/null || true
  fi
  rm -rf "$fixture"
}
trap cleanup_fixture EXIT
export FAKE_GIT_COMMON_ROOT="$fixture"
export AZEDARACH_REAL_GO_BIN="$fixture/fake-bin/go"

mkdir -p "$fixture/bin" "$fixture/fake-bin" "$fixture/real-bin"
cp "$repo_root/justfile" "$fixture/justfile"
mkdir -p "$fixture/scripts"
cp "$repo_root/scripts/build-install-run.sh" "$fixture/scripts/build-install-run.sh"
cp "$repo_root/scripts/with-machine-validation-lease" "$fixture/scripts/with-machine-validation-lease"
cp "$repo_root/scripts/with-production-install-admission" "$fixture/scripts/with-production-install-admission"
chmod +x "$fixture/scripts/with-production-install-admission"
cat >"$fixture/scripts/jaeger-local.sh" <<'EOF'
#!/usr/bin/env bash
jaeger_ensure() { :; }
EOF
printf 'production az sentinel\n' >"$fixture/bin/az"
printf 'production azd sentinel\n' >"$fixture/bin/azd"
cp "$fixture/bin/az" "$fixture/az.before"
cp "$fixture/bin/azd" "$fixture/azd.before"

cat >"$fixture/fake-bin/go" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail

args="$*"
output=""
while (($# > 0)); do
  if [[ "$1" == "-o" ]]; then
    output="$2"
    break
  fi
  shift
done
if [[ -z "$output" ]]; then
  echo "stub go: missing -o output" >&2
  exit 1
fi
if [[ "${FAKE_GO_FAIL_AZD:-0}" == "1" && "$args" == *"./cmd/azd"* ]]; then
  echo "stub go: requested azd build failure" >&2
  exit 1
fi
mkdir -p "$(dirname "$output")"
marker="${FAKE_GO_MARKER:-default}"
if [[ "$args" == *"./cmd/azd"* ]]; then
  marker="${FAKE_GO_AZD_MARKER:-$marker}"
else
  marker="${FAKE_GO_AZ_MARKER:-$marker}"
fi
cat >"$output" <<EOF_SCRIPT
#!/bin/sh
if [ "\${1:-}" = "version" ] || [ "\${1:-}" = "--version" ] || [ "\${1:-}" = "-v" ]; then
  printf 'dev (%s)\\n' '$marker'
fi
if [ "\$#" -eq 0 ] && [ -n "\${FAKE_AZ_TUI_STARTED:-}" ]; then
  if [ -d "\${FAKE_GIT_COMMON_ROOT:?}/azedarach-production-admission/production" ]; then
    echo "production admission remained held while TUI launched" >&2
    exit 91
  fi
  printf '%s\n' "\$0" >"\${FAKE_AZ_TUI_EXECUTABLE:?}"
  : >"\$FAKE_AZ_TUI_STARTED"
  while [ ! -e "\${FAKE_AZ_TUI_RELEASE:?}" ]; do
    sleep 0.05
  done
fi
exit 0
: <<'AZEDARACH_BUILD_METADATA'
scratch build $marker
AZEDARACH_BUILD_METADATA
EOF_SCRIPT
chmod +x "$output"
EOF
chmod +x "$fixture/fake-bin/go"

cat >"$fixture/fake-bin/az" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail

if [[ "${1:-}" == "validation" && "${2:-}" == "acquire" ]]; then
  request=""
  issue=""
  scope=""
  purpose=""
  while (($# > 0)); do
    if [[ "$1" == "--request" ]]; then
      request="$2"
      shift 2
      continue
    fi
    if [[ "$1" == "--issue" ]]; then issue="$2"; shift 2; continue; fi
    if [[ "$1" == "--scope" ]]; then scope="$2"; shift 2; continue; fi
    if [[ "$1" == "--purpose" ]]; then purpose="$2"; shift 2; continue; fi
    shift
  done
  if [[ "${FAKE_AZ_REQUIRE_REPOSITORY:-0}" == "1" ]]; then
    if [[ -n "$issue" || "$scope" != "repository" || "$purpose" != "push_gate" ]]; then
      echo "stub az: repository validation identity mismatch issue=$issue scope=$scope purpose=$purpose" >&2
      exit 1
    fi
  fi
  token="${AZEDARACH_VALIDATION_LEASE_TOKEN:-}"
  if [[ -n "$token" && "$request" == *"${token:0:12}"* ]]; then
    echo "stub az: public request id leaked lease-token material" >&2
    exit 1
  fi
  state=active
  if [[ -n "${FAKE_AZ_QUEUE_ONCE_FILE:-}" && ! -e "$FAKE_AZ_QUEUE_ONCE_FILE" ]]; then
    : >"$FAKE_AZ_QUEUE_ONCE_FILE"
    state=queued
  fi
  execution="${FAKE_AZ_EXECUTION:-executed}"
  [[ "$execution" == "reused" || "$execution" == "joined" ]] && state=completed
  [[ "$execution" == "skipped" ]] && state=cancelled
  printf '{"request":{"request_id":"%s","state":"%s","execution":"%s","authoritative_request_id":"source-request"}}\n' "$request" "$state" "$execution"
  exit 0
fi
if [[ "${1:-}" == "validation" && "${2:-}" == "status" ]]; then
  printf '{"active":[{"request_id":"%s","class":"%s","profile":"%s"}],"queued":[],"revision":1}\n' \
    "${AZEDARACH_VALIDATION_REQUEST_ID:-fixture}" "${AZEDARACH_VALIDATION_CLASS:-shared}" "${AZEDARACH_VALIDATION_PROFILE:-fixture}"
  exit 0
fi
if [[ "${1:-}" == "validation" && "${2:-}" == "authorize-nested" ]]; then
  for arg in "$@"; do
    if [[ -n "${AZEDARACH_VALIDATION_LEASE_TOKEN:-}" && "$arg" == "$AZEDARACH_VALIDATION_LEASE_TOKEN" ]]; then
      echo "stub az: nested authorization leaked lease token in argv" >&2
      exit 1
    fi
  done
  requested_class=""
  while (($# > 0)); do
    if [[ "$1" == "--class" ]]; then
      requested_class="$2"
      break
    fi
    shift
  done
  if [[ -z "${AZEDARACH_VALIDATION_LEASE_TOKEN:-}" ]]; then
    echo "stub az: nested authorization missing lease token" >&2
    exit 1
  fi
  if [[ "${AZEDARACH_VALIDATION_CLASS:-}" != "aggregate" &&
        "$requested_class" != "${AZEDARACH_VALIDATION_CLASS:-}" ]]; then
    echo "stub az: nested $requested_class validation cannot join active ${AZEDARACH_VALIDATION_CLASS:-} request" >&2
    exit 1
  fi
  exit 0
fi
if [[ "${1:-}" == "validation" && "${2:-}" == "heartbeat" ]]; then
  if [[ -n "${FAKE_AZ_HEARTBEAT_BLOCK_FILE:-}" ]]; then
    : >"$FAKE_AZ_HEARTBEAT_BLOCK_FILE"
    sleep 30
  fi
  exit 0
fi
if [[ "${1:-}" == "validation" && "${2:-}" == "finish" ]]; then
  exit 0
fi
echo "stub az: unsupported arguments: $*" >&2
exit 1
EOF
chmod +x "$fixture/fake-bin/az"

cat >"$fixture/fake-bin/validation-git" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
if [[ "${1:-}" == "rev-parse" && "${2:-}" == "HEAD" ]]; then
  printf 'fixture-sha\n'
  exit 0
fi
if [[ "${1:-}" == "status" && "${2:-}" == "--porcelain" ]]; then
  exit 0
fi
if [[ "${1:-}" == "rev-parse" && "${2:-}" == "--path-format=absolute" && "${3:-}" == "--git-common-dir" ]]; then
  printf '%s/.git\n' "${FAKE_GIT_COMMON_ROOT:?}"
  exit 0
fi
if [[ "${1:-}" == "rev-parse" && "${2:-}" == "--show-toplevel" ]]; then
  printf '%s\n' "${FAKE_GIT_COMMON_ROOT:?}"
  exit 0
fi
echo "stub validation git: unsupported arguments: $*" >&2
exit 1
EOF
chmod +x "$fixture/fake-bin/validation-git"
export AZEDARACH_VALIDATION_GIT_BIN="$fixture/fake-bin/validation-git"

fresh_validation_environment=(
    -u AZEDARACH_VALIDATION_REQUEST_ID \
    -u AZEDARACH_VALIDATION_NESTED_FD \
    -u AZEDARACH_VALIDATION_LEASE_TOKEN \
    -u AZEDARACH_VALIDATION_CLASS \
    -u AZEDARACH_VALIDATION_PROFILE \
    -u AZEDARACH_VALIDATION_SOURCE_REVISION \
    -u AZEDARACH_VALIDATION_SCOPE \
    -u AZEDARACH_VALIDATION_PURPOSE \
    -u AZEDARACH_VALIDATION_EXECUTION \
    -u AZEDARACH_VALIDATION_AUTHORITATIVE_REQUEST_ID \
    -u AZEDARACH_VALIDATION_OVERRIDE \
    -u AZEDARACH_TICKET_ID \
    -u AZEDARACH_ISSUE_ID
)

repository_payload="$fixture/repository-payload-ran"
env "${fresh_validation_environment[@]}" \
  AZEDARACH_VALIDATION_AZ_BIN="$fixture/fake-bin/az" \
  FAKE_AZ_REQUIRE_REPOSITORY=1 \
  "$fixture/scripts/with-machine-validation-lease" --class aggregate --scope repository --purpose push_gate --profile repository-push -- \
  sh -c 'touch "$1"' sh "$repository_payload"
test -e "$repository_payload"

# The remaining lease-control fixtures exercise the controlled-capacity path.
# Ordinary ticket development bypasses this wrapper entirely.

reused_payload="$fixture/reused-payload-ran"
env "${fresh_validation_environment[@]}" \
  AZEDARACH_VALIDATION_AZ_BIN="$fixture/fake-bin/az" AZEDARACH_TICKET_ID=fixture \
  FAKE_AZ_EXECUTION=reused \
  "$fixture/scripts/with-machine-validation-lease" --class shared --scope ticket --purpose capacity --profile reused -- \
  sh -c 'touch "$1"' sh "$reused_payload"
test ! -e "$reused_payload"

skipped_payload="$fixture/skipped-payload-ran"
env "${fresh_validation_environment[@]}" \
  AZEDARACH_VALIDATION_AZ_BIN="$fixture/fake-bin/az" AZEDARACH_TICKET_ID=fixture \
  FAKE_AZ_EXECUTION=skipped \
  "$fixture/scripts/with-machine-validation-lease" --class shared --scope ticket --purpose capacity --profile skipped \
  --emergency-skip 'operator-approved outage recovery' --override-actor operator -- true
test ! -e "$skipped_payload"

env "${fresh_validation_environment[@]}" \
  AZEDARACH_VALIDATION_AZ_BIN="$fixture/fake-bin/az" \
  AZEDARACH_TICKET_ID=fixture \
  FAKE_AZ_QUEUE_ONCE_FILE="$fixture/queued-once" \
  "$fixture/scripts/with-machine-validation-lease" --class shared --scope ticket --purpose capacity --profile token-isolation -- \
  sh -c 'test -z "${AZEDARACH_VALIDATION_LEASE_TOKEN:-}"'
test -e "$fixture/queued-once"

runtime_probe="$fixture/runtime-probe"
env "${fresh_validation_environment[@]}" \
XDG_RUNTIME_DIR="$fixture/ordinary-runtime" \
AZEDARACH_DAEMON_SCOPE=validation-bootstrap \
AZEDARACH_VALIDATION_BOOTSTRAP_XDG_WAS_SET=1 \
AZEDARACH_VALIDATION_BOOTSTRAP_ORIGINAL_XDG_RUNTIME_DIR="$fixture/forged-runtime" \
AZEDARACH_VALIDATION_BOOTSTRAP_ID=forged \
AZEDARACH_VALIDATION_AZ_BIN="$fixture/fake-bin/az" \
AZEDARACH_TICKET_ID=fixture \
  "$fixture/scripts/with-machine-validation-lease" --class shared --scope ticket --purpose capacity --profile runtime-preservation -- \
  sh -c 'printf "%s\n%s" "$XDG_RUNTIME_DIR" "$AZEDARACH_VALIDATION_CLEANUP_HANDLE" >"$0"' "$runtime_probe"
test "$(sed -n '1p' "$runtime_probe")" != "$fixture/ordinary-runtime"
test "$(sed -n '1p' "$runtime_probe")" = "$(sed -n '2p' "$runtime_probe")"

env "${fresh_validation_environment[@]}" \
  AZEDARACH_VALIDATION_AZ_BIN="$fixture/fake-bin/az" \
  AZEDARACH_TICKET_ID=fixture \
  "$fixture/scripts/with-machine-validation-lease" --class shared --scope ticket --purpose capacity --profile outer -- \
  "$fixture/scripts/with-machine-validation-lease" --class shared --profile nested -- \
  "$fixture/scripts/with-machine-validation-lease" --class shared --profile nested-deep -- true
if env "${fresh_validation_environment[@]}" \
  AZEDARACH_VALIDATION_AZ_BIN="$fixture/fake-bin/az" \
  AZEDARACH_TICKET_ID=fixture \
  "$fixture/scripts/with-machine-validation-lease" --class shared --scope ticket --purpose capacity --profile outer -- \
  "$fixture/scripts/with-machine-validation-lease" --class aggregate --profile nested -- true \
  >"$fixture/nested-upgrade.stdout" 2>"$fixture/nested-upgrade.stderr"; then
  echo "nested aggregate unexpectedly joined a shared validation request" >&2
  exit 1
fi
grep -q "daemon rejected nested validation authorization" "$fixture/nested-upgrade.stderr"

# A public request id and status are not a nested capability.
if env "${fresh_validation_environment[@]}" \
  AZEDARACH_TICKET_ID=fixture \
  AZEDARACH_VALIDATION_AZ_BIN="$fixture/fake-bin/az" \
  AZEDARACH_VALIDATION_REQUEST_ID=spoofed-public-id \
  AZEDARACH_VALIDATION_CLASS=aggregate \
  "$fixture/scripts/with-machine-validation-lease" --class shared --scope ticket --purpose capacity --profile spoof -- true \
  >"$fixture/nested-spoof.stdout" 2>"$fixture/nested-spoof.stderr"; then
  echo "public validation request id unexpectedly authorized nested work" >&2
  exit 1
fi
grep -q "requires an inherited authorization capability" "$fixture/nested-spoof.stderr"

# Killing the wrapper must reap both the heartbeat and the executed command.
env "${fresh_validation_environment[@]}" \
  AZEDARACH_VALIDATION_AZ_BIN="$fixture/fake-bin/az" AZEDARACH_TICKET_ID=fixture \
  AZEDARACH_VALIDATION_HEARTBEAT_INTERVAL_SECONDS=0.05 \
  FAKE_AZ_HEARTBEAT_BLOCK_FILE="$fixture/heartbeat-blocked" \
  "$fixture/scripts/with-machine-validation-lease" --class shared --scope ticket --purpose capacity --profile parent-death -- \
  sh -c 'echo $$ >"$1"; exec sleep 30' sh "$fixture/orphan-command.pid" &
wrapper_pid=$!
for _ in {1..100}; do
  [[ -s "$fixture/orphan-command.pid" && -e "$fixture/heartbeat-blocked" ]] && break
  sleep 0.02
done
test -s "$fixture/orphan-command.pid"
test -e "$fixture/heartbeat-blocked"
wrapper_children="$(pgrep -P "$wrapper_pid")"
test "$(printf '%s\n' "$wrapper_children" | sed '/^$/d' | wc -l | tr -d ' ')" -ge 2
heartbeat_pid="$(printf '%s\n' "$wrapper_children" | head -1)"
heartbeat_rpc="$(pgrep -P "$heartbeat_pid")"
test -n "$heartbeat_rpc"
orphan_command_pid="$(cat "$fixture/orphan-command.pid")"
kill -KILL "$wrapper_pid"
wait "$wrapper_pid" 2>/dev/null || true
for _ in {1..100}; do
  descendants_alive=0
  for descendant in $wrapper_children $heartbeat_rpc $orphan_command_pid; do
    kill -0 "$descendant" 2>/dev/null && descendants_alive=1
  done
  if [[ "$descendants_alive" -eq 0 ]]; then
    break
  fi
  sleep 0.03
done
for descendant in $wrapper_children $heartbeat_rpc $orphan_command_pid; do
  if kill -0 "$descendant" 2>/dev/null; then
    echo "wrapper SIGKILL left validation descendant $descendant alive" >&2
    exit 1
  fi
done

# Aggregate admission observes raw, unleased Go-shaped work before payload
# startup and waits for a stable quiescent window.
cat >"$fixture/fake-bin/raw-overlap.test" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
sleep 5
EOF
chmod +x "$fixture/fake-bin/raw-overlap.test"
"$fixture/fake-bin/raw-overlap.test" &
raw_go_pid=$!
cat >"$fixture/fake-bin/validation-ps" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
stat="$(/bin/ps -p "${RAW_GO_PID:?}" -o stat= 2>/dev/null || true)"
if [[ -n "$stat" && "$stat" != Z* ]]; then
  printf '%s 1 %s %s\n' "$RAW_GO_PID" "$stat" "${RAW_GO_COMMAND:?}"
fi
EOF
chmod +x "$fixture/fake-bin/validation-ps"
raw_started="$(date +%s)"
env "${fresh_validation_environment[@]}" \
  AZEDARACH_VALIDATION_AZ_BIN="$fixture/fake-bin/az" AZEDARACH_TICKET_ID=fixture \
  AZEDARACH_VALIDATION_PS_BIN="$fixture/fake-bin/validation-ps" \
  RAW_GO_PID="$raw_go_pid" RAW_GO_COMMAND="$fixture/fake-bin/raw-overlap.test" \
  "$fixture/scripts/with-machine-validation-lease" --class aggregate --scope ticket --purpose capacity --profile raw-go-overlap -- \
  sh -c 'date +%s >"$1"' sh "$fixture/raw-overlap-command.started"
wait "$raw_go_pid"
test "$(cat "$fixture/raw-overlap-command.started")" -gt "$raw_started"

# Raw Go-shaped work that starts after aggregate admission invalidates the
# complete outer gate, including stages outside test-timing.
cat >"$fixture/fake-bin/validation-ps-dynamic" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
[[ -s "${RAW_GO_PID_FILE:?}" ]] || exit 0
pid="$(cat "$RAW_GO_PID_FILE")"
stat="$(/bin/ps -p "$pid" -o stat= 2>/dev/null || true)"
if [[ -n "$stat" && "$stat" != Z* ]]; then
  printf '%s 1 %s %s\n' "$pid" "$stat" "${RAW_GO_COMMAND:?}"
fi
EOF
chmod +x "$fixture/fake-bin/validation-ps-dynamic"
env "${fresh_validation_environment[@]}" \
  AZEDARACH_VALIDATION_AZ_BIN="$fixture/fake-bin/az" AZEDARACH_TICKET_ID=fixture \
  AZEDARACH_VALIDATION_PS_BIN="$fixture/fake-bin/validation-ps-dynamic" \
  RAW_GO_PID_FILE="$fixture/raw-during-gate.pid" \
  RAW_GO_COMMAND="$fixture/fake-bin/raw-overlap.test" \
  "$fixture/scripts/with-machine-validation-lease" --class aggregate --scope ticket --purpose capacity --profile whole-gate-overlap -- \
  sh -c 'touch "$1"; sleep 2' sh "$fixture/whole-gate.started" \
  >"$fixture/whole-gate.stdout" 2>"$fixture/whole-gate.stderr" &
whole_gate_pid=$!
for _ in {1..100}; do
  [[ -e "$fixture/whole-gate.started" ]] && break
  sleep 0.02
done
test -e "$fixture/whole-gate.started"
"$fixture/fake-bin/raw-overlap.test" &
raw_during_gate_pid=$!
kill -0 "$raw_during_gate_pid"
printf '%s\n' "$raw_during_gate_pid" >"$fixture/raw-during-gate.pid"
if wait "$whole_gate_pid"; then
  echo "aggregate validation unexpectedly accepted raw Go overlap" >&2
  exit 1
fi
wait "$raw_during_gate_pid"
grep -q "aggregate validation overlapped" "$fixture/whole-gate.stderr"

cat >"$fixture/real-bin/atomic-replace" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
[[ "$#" -eq 2 ]]
exec perl -e 'rename $ARGV[0], $ARGV[1] or die "rename $ARGV[0] to $ARGV[1]: $!\n"' "$1" "$2"
EOF
chmod +x "$fixture/real-bin/atomic-replace"

cat >"$fixture/fake-bin/atomic-replace" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail

destination="$2"
if [[ "${FAKE_REPLACE_FAIL_AZD_LINK:-0}" == "1" && "$destination" == */azd &&
      ! -e "${FAKE_REPLACE_FAIL_ONCE_FILE:?}" ]]; then
  : >"$FAKE_REPLACE_FAIL_ONCE_FILE"
  if [[ "${FAKE_REPLACE_DELAY_FAILURE:-0}" == "1" ]]; then
    : >"${FAKE_REPLACE_FAILURE_MARKER:?}"
    sleep 0.4
  fi
  echo "stub atomic-replace: requested azd link install failure" >&2
  exit 1
fi
exec "${REAL_ATOMIC_REPLACE_BIN:?}" "$@"
EOF
chmod +x "$fixture/fake-bin/atomic-replace"
export AZEDARACH_ATOMIC_REPLACE_BIN="$fixture/fake-bin/atomic-replace"
export REAL_ATOMIC_REPLACE_BIN="$fixture/real-bin/atomic-replace"

cat >"$fixture/fake-bin/cp" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail

destination="${*: -1}"
if [[ "${FAKE_CP_ASSERT_SERIAL:-0}" == "1" &&
      "$destination" == */.azedarach-generations/generation.*/az ]]; then
  guard="${FAKE_CP_GUARD:?}"
  if ! mkdir "$guard" 2>/dev/null; then
    echo "stub cp: concurrent installers entered the critical section" >&2
    exit 1
  fi
  trap 'rmdir "$guard"' EXIT
  sleep 0.1
fi
/bin/cp "$@"
EOF
chmod +x "$fixture/fake-bin/cp"

cat >"$fixture/fake-bin/git" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail

if [[ "${1:-}" == "rev-parse" && "${2:-}" == "HEAD" ]]; then
  printf 'fixture-sha\n'
  exit 0
fi
if [[ "${1:-}" == "rev-parse" && ( "${2:-}" == "--git-dir" || "${3:-}" == "--git-dir" ) ]]; then
  if [[ "${FAKE_GIT_MODE:-linked}" == "primary" ]]; then
    printf '%s/.git\n' "${FAKE_GIT_COMMON_ROOT:?}"
  else
    printf '%s/.git/worktrees/test\n' "${FAKE_GIT_COMMON_ROOT:?}"
  fi
  exit 0
fi
if [[ "${1:-}" == "rev-parse" && ( "${2:-}" == "--git-common-dir" || "${3:-}" == "--git-common-dir" ) ]]; then
  printf '%s/.git\n' "${FAKE_GIT_COMMON_ROOT:?}"
  exit 0
fi
if [[ "${1:-}" == "rev-parse" && "${2:-}" == "--show-toplevel" ]]; then
  printf '%s\n' "${FAKE_GIT_COMMON_ROOT:?}"
  exit 0
fi
echo "stub git: unsupported arguments: $*" >&2
exit 1
EOF
chmod +x "$fixture/fake-bin/git"
export FAKE_GIT_COMMON_ROOT="$fixture"

aggregate_ready="$fixture/aggregate-holder.ready"
cat >"$fixture/fake-bin/validation-ps-empty" <<'EOF'
#!/usr/bin/env bash
exit 0
EOF
chmod +x "$fixture/fake-bin/validation-ps-empty"
env "${fresh_validation_environment[@]}" \
  AZEDARACH_VALIDATION_AZ_BIN="$fixture/fake-bin/az" \
  AZEDARACH_VALIDATION_PS_BIN="$fixture/fake-bin/validation-ps-empty" \
  AZEDARACH_TICKET_ID=aggregate-holder \
  "$fixture/scripts/with-machine-validation-lease" --class aggregate --scope ticket --purpose review_evidence --profile merge-gate -- \
  sh -c 'touch "$1"; sleep 2' sh "$aggregate_ready" \
  >"$fixture/aggregate-holder.stdout" 2>"$fixture/aggregate-holder.stderr" &
aggregate_holder_pid=$!
for _ in {1..300}; do
  [[ -e "$aggregate_ready" ]] && break
  sleep 0.02
done
test -e "$aggregate_ready"
AZEDARACH_PRODUCTION_ADMISSION_WAIT_SECONDS=1 PATH="$fixture/fake-bin:$PATH" \
  "$fixture/scripts/with-production-install-admission" -- true \
  >"$fixture/production-admission.stdout" 2>"$fixture/production-admission.stderr"
kill -0 "$aggregate_holder_pid"
test ! -s "$fixture/production-admission.stderr"
test -s "$fixture/.git/azedarach-production-admission/production-epoch"
if wait "$aggregate_holder_pid"; then
  echo "aggregate validation remained canonical after a production install overlapped it" >&2
  exit 1
else
  test "$?" -eq 74
fi
development_ready="$fixture/development-holder.ready"
env "${fresh_validation_environment[@]}" \
  AZEDARACH_VALIDATION_AZ_BIN="$fixture/fake-bin/az" \
  AZEDARACH_TICKET_ID=development-holder \
  "$fixture/scripts/with-machine-validation-lease" --class shared --scope ticket --purpose development --profile focused-development -- \
  sh -c 'touch "$1"; sleep 30' sh "$development_ready" &
development_holder_pid=$!
for _ in {1..300}; do
  [[ -e "$development_ready" ]] && break
  sleep 0.02
done
test -e "$development_ready"
grep -q "validation overlapped a production install and is noncanonical" \
  "$fixture/aggregate-holder.stderr"
AZEDARACH_PRODUCTION_ADMISSION_WAIT_SECONDS=1 PATH="$fixture/fake-bin:$PATH" \
  "$fixture/scripts/with-production-install-admission" -- true
kill "$development_holder_pid"
wait "$development_holder_pid" 2>/dev/null || true

AZEDARACH_GO_CACHE_ROOT= AZEDARACH_VALIDATION_LEASE_ID= AZEDARACH_VALIDATION_LEASE_ROOT= \
  PATH="$fixture/fake-bin:$PATH" \
  just --justfile "$fixture/justfile" --working-directory "$fixture" build

cmp "$fixture/az.before" "$fixture/bin/az"
cmp "$fixture/azd.before" "$fixture/bin/azd"
test -s "$fixture/.tmp/az-test/az"
test -s "$fixture/.tmp/az-test/azd"

just --justfile "$fixture/justfile" --working-directory "$fixture" clean

cmp "$fixture/az.before" "$fixture/bin/az"
cmp "$fixture/azd.before" "$fixture/bin/azd"
test ! -e "$fixture/.tmp/az-test"

if PATH="$fixture/fake-bin:$PATH" "$fixture/scripts/build-install-run.sh" --no-run \
  >"$fixture/build-install-run.stdout" 2>"$fixture/build-install-run.stderr"; then
  echo "build-install-run unexpectedly accepted a linked worktree" >&2
  exit 1
fi
grep -q "Refusing build-install-run from a linked worktree" "$fixture/build-install-run.stderr"
cmp "$fixture/az.before" "$fixture/bin/az"
cmp "$fixture/azd.before" "$fixture/bin/azd"

mkdir -p "$fixture/failure-bin"
printf 'installed az before failed build\n' >"$fixture/failure-bin/az"
printf 'installed azd before failed build\n' >"$fixture/failure-bin/azd"
cp "$fixture/failure-bin/az" "$fixture/failure-az.before"
cp "$fixture/failure-bin/azd" "$fixture/failure-azd.before"
if FAKE_GIT_MODE=primary FAKE_GO_FAIL_AZD=1 AZ_INSTALL_DIR="$fixture/failure-bin" \
  PATH="$fixture/fake-bin:$PATH" "$fixture/scripts/build-install-run.sh" --no-run \
  >"$fixture/build-failure.stdout" 2>"$fixture/build-failure.stderr"; then
  echo "build-install-run unexpectedly installed after an azd build failure" >&2
  exit 1
fi
grep -q "requested azd build failure" "$fixture/build-failure.stderr"
cmp "$fixture/failure-az.before" "$fixture/failure-bin/az"
cmp "$fixture/failure-azd.before" "$fixture/failure-bin/azd"

mkdir -p "$fixture/mismatch-bin"
if FAKE_GIT_MODE=primary FAKE_GO_AZ_MARKER=az-version FAKE_GO_AZD_MARKER=azd-version \
  AZ_INSTALL_DIR="$fixture/mismatch-bin" PATH="$fixture/fake-bin:$PATH" \
  "$fixture/scripts/build-install-run.sh" --no-run \
  >"$fixture/mismatch.stdout" 2>"$fixture/mismatch.stderr"; then
  echo "build-install-run unexpectedly installed a mismatched az/azd pair" >&2
  exit 1
fi
grep -q "Refusing incoherent az/azd install: version mismatch" "$fixture/mismatch.stderr"
test ! -e "$fixture/mismatch-bin/az"
test ! -e "$fixture/mismatch-bin/azd"
test ! -e "$fixture/mismatch-bin/.azedarach-current"
test ! -e "$fixture/failure-bin/.azedarach-current"
test ! -L "$fixture/failure-bin/.azedarach-current"

mkdir -p "$fixture/invalid-control-bin"
printf 'invalid control old az\n' >"$fixture/invalid-control-bin/az"
printf 'invalid control old azd\n' >"$fixture/invalid-control-bin/azd"
printf 'not a symlink\n' >"$fixture/invalid-control-bin/.azedarach-current"
cp "$fixture/invalid-control-bin/az" "$fixture/invalid-control-az.before"
cp "$fixture/invalid-control-bin/azd" "$fixture/invalid-control-azd.before"
if FAKE_GIT_MODE=primary AZ_INSTALL_DIR="$fixture/invalid-control-bin" \
  PATH="$fixture/fake-bin:$PATH" "$fixture/scripts/build-install-run.sh" --no-run \
  >"$fixture/invalid-control.stdout" 2>"$fixture/invalid-control.stderr"; then
  echo "build-install-run unexpectedly replaced a non-symlink control path" >&2
  exit 1
fi
grep -q "Refusing to replace non-symlink install control path" "$fixture/invalid-control.stderr"
cmp "$fixture/invalid-control-az.before" "$fixture/invalid-control-bin/az"
cmp "$fixture/invalid-control-azd.before" "$fixture/invalid-control-bin/azd"
grep -q "not a symlink" "$fixture/invalid-control-bin/.azedarach-current"

mkdir -p "$fixture/partial-bin/.azedarach-generations/generation.old"
printf 'partial old az\n' >"$fixture/partial-bin/.azedarach-generations/generation.old/az"
printf 'partial old azd\n' >"$fixture/partial-bin/.azedarach-generations/generation.old/azd"
chmod +x "$fixture/partial-bin/.azedarach-generations/generation.old/az" \
  "$fixture/partial-bin/.azedarach-generations/generation.old/azd"
ln -s .azedarach-generations/generation.old "$fixture/partial-bin/.azedarach-current"
ln -s .azedarach-current/az "$fixture/partial-bin/az"
cp "$fixture/partial-bin/.azedarach-generations/generation.old/azd" "$fixture/partial-bin/azd"
cp -L "$fixture/partial-bin/az" "$fixture/partial-az.before"
cp "$fixture/partial-bin/azd" "$fixture/partial-azd.before"

FAKE_GIT_MODE=primary FAKE_REPLACE_FAIL_AZD_LINK=1 FAKE_REPLACE_DELAY_FAILURE=1 \
  FAKE_REPLACE_FAIL_ONCE_FILE="$fixture/partial-azd-link-failed" \
  FAKE_REPLACE_FAILURE_MARKER="$fixture/partial-rollback-started" \
  AZ_INSTALL_DIR="$fixture/partial-bin" PATH="$fixture/fake-bin:$PATH" \
  "$fixture/scripts/build-install-run.sh" --no-run \
  >"$fixture/partial-failure.stdout" 2>"$fixture/partial-failure.stderr" &
partial_installer_pid=$!
while [[ ! -e "$fixture/partial-rollback-started" ]] &&
  kill -0 "$partial_installer_pid" 2>/dev/null; do
  sleep 0.005
done
test -e "$fixture/partial-rollback-started"
partial_observer_failed=0
while kill -0 "$partial_installer_pid" 2>/dev/null; do
  if [[ ! -x "$fixture/partial-bin/az" || ! -x "$fixture/partial-bin/azd" ]]; then
    partial_observer_failed=1
    {
      echo "rollback observer saw an unavailable command"
      ls -la "$fixture/partial-bin"
      find "$fixture/partial-bin/.azedarach-generations" -maxdepth 2 -type f -o -type l
      printf 'az -> %s\n' "$(readlink "$fixture/partial-bin/az" 2>/dev/null || echo '<not-link>')"
      printf 'azd -> %s\n' "$(readlink "$fixture/partial-bin/azd" 2>/dev/null || echo '<not-link>')"
      printf 'current -> %s\n' "$(readlink "$fixture/partial-bin/.azedarach-current" 2>/dev/null || echo '<not-link>')"
    } >&2
    break
  fi
done
if wait "$partial_installer_pid"; then
  echo "build-install-run unexpectedly succeeded from a partially managed state" >&2
  exit 1
fi
test "$partial_observer_failed" -eq 0
grep -q "requested azd link install failure" "$fixture/partial-failure.stderr"
test -x "$fixture/partial-bin/az"
test -x "$fixture/partial-bin/azd"
cmp "$fixture/partial-az.before" "$fixture/partial-bin/az"
cmp "$fixture/partial-azd.before" "$fixture/partial-bin/azd"
test "$(readlink "$fixture/partial-bin/az")" = ".azedarach-current/az"
test ! -L "$fixture/partial-bin/azd"
case "$(readlink "$fixture/partial-bin/.azedarach-current")" in
  .azedarach-generations/generation.previous.*) ;;
  *) echo "rollback control link does not target retained previous generation" >&2; exit 1 ;;
esac
test -x "$fixture/partial-bin/.azedarach-current/az"
test -x "$fixture/partial-bin/.azedarach-current/azd"
partial_generation_count="$(find "$fixture/partial-bin/.azedarach-generations" -mindepth 1 -maxdepth 1 -type d | wc -l | tr -d ' ')"
test "$partial_generation_count" -eq 2
while IFS= read -r link; do
  test -e "$link"
done < <(find "$fixture/partial-bin" -type l)

mkdir -p "$fixture/interrupted-bin"
printf 'interrupted old az\n' >"$fixture/interrupted-bin/az"
printf 'interrupted old azd\n' >"$fixture/interrupted-bin/azd"
chmod +x "$fixture/interrupted-bin/az" "$fixture/interrupted-bin/azd"
ln -s .azedarach-generations/missing "$fixture/interrupted-bin/.azedarach-current"
cp "$fixture/interrupted-bin/az" "$fixture/interrupted-az.before"
cp "$fixture/interrupted-bin/azd" "$fixture/interrupted-azd.before"

if FAKE_GIT_MODE=primary FAKE_REPLACE_FAIL_AZD_LINK=1 \
  FAKE_REPLACE_FAIL_ONCE_FILE="$fixture/interrupted-azd-link-failed" \
  AZ_INSTALL_DIR="$fixture/interrupted-bin" PATH="$fixture/fake-bin:$PATH" \
  "$fixture/scripts/build-install-run.sh" --no-run \
  >"$fixture/interrupted-failure.stdout" 2>"$fixture/interrupted-failure.stderr"; then
  echo "build-install-run unexpectedly succeeded from an interrupted control-link state" >&2
  exit 1
fi
grep -q "requested azd link install failure" "$fixture/interrupted-failure.stderr"
test -x "$fixture/interrupted-bin/az"
test -x "$fixture/interrupted-bin/azd"
cmp "$fixture/interrupted-az.before" "$fixture/interrupted-bin/az"
cmp "$fixture/interrupted-azd.before" "$fixture/interrupted-bin/azd"
test -L "$fixture/interrupted-bin/.azedarach-current"
test -x "$fixture/interrupted-bin/.azedarach-current/az"
test -x "$fixture/interrupted-bin/.azedarach-current/azd"

if FAKE_GIT_MODE=primary FAKE_REPLACE_FAIL_AZD_LINK=1 \
  FAKE_REPLACE_FAIL_ONCE_FILE="$fixture/azd-link-failed" \
  AZ_INSTALL_DIR="$fixture/failure-bin" PATH="$fixture/fake-bin:$PATH" \
  "$fixture/scripts/build-install-run.sh" --no-run \
  >"$fixture/install-failure.stdout" 2>"$fixture/install-failure.stderr"; then
  echo "build-install-run unexpectedly succeeded after an azd link install failure" >&2
  exit 1
fi
grep -q "requested azd link install failure" "$fixture/install-failure.stderr"
cmp "$fixture/failure-az.before" "$fixture/failure-bin/az"
cmp "$fixture/failure-azd.before" "$fixture/failure-bin/azd"
test -L "$fixture/failure-bin/.azedarach-current"
test -x "$fixture/failure-bin/.azedarach-current/az"
test -x "$fixture/failure-bin/.azedarach-current/azd"

mkdir -p "$fixture/global-bin"
ln -s "$fixture/bin/az" "$fixture/global-bin/az"
ln -s "$fixture/bin/azd" "$fixture/global-bin/azd"
FAKE_GIT_MODE=primary AZ_INSTALL_DIR="$fixture/global-bin" \
  PATH="$fixture/global-bin:$fixture/fake-bin:$PATH" "$fixture/scripts/build-install-run.sh" --no-run \
  >"$fixture/build-install-run-install.stdout" 2>"$fixture/build-install-run-install.stderr"
grep -q "Installed az -> $fixture/global-bin/az" "$fixture/build-install-run-install.stdout"
grep -q "Installed azd -> $fixture/global-bin/azd" "$fixture/build-install-run-install.stdout"
test -x "$fixture/global-bin/az"
test -x "$fixture/global-bin/azd"
test -L "$fixture/global-bin/az"
test -L "$fixture/global-bin/azd"
test "$(readlink "$fixture/global-bin/az")" = ".azedarach-current/az"
test "$(readlink "$fixture/global-bin/azd")" = ".azedarach-current/azd"
grep -q "scratch build" "$fixture/global-bin/az"
grep -q "scratch build" "$fixture/global-bin/azd"
cmp "$fixture/az.before" "$fixture/bin/az"
cmp "$fixture/azd.before" "$fixture/bin/azd"
first_installed_target="$(readlink "$fixture/global-bin/.azedarach-current")"
first_installed_generation="$fixture/global-bin/$first_installed_target"
test -x "$first_installed_generation/az"
test -x "$first_installed_generation/azd"
test "$("$first_installed_generation/az" version)" = "dev (default)"
test "$("$first_installed_generation/azd" version)" = "dev (default)"

tui_started="$fixture/production-tui.started"
tui_release="$fixture/production-tui.release"
tui_executable="$fixture/production-tui.executable"
FAKE_GIT_MODE=primary FAKE_GO_MARKER=tui-admission-lifetime \
  FAKE_AZ_TUI_STARTED="$tui_started" FAKE_AZ_TUI_RELEASE="$tui_release" \
  FAKE_AZ_TUI_EXECUTABLE="$tui_executable" \
  AZ_INSTALL_DIR="$fixture/global-bin" \
  PATH="$fixture/global-bin:$fixture/fake-bin:$PATH" \
  "$fixture/scripts/build-install-run.sh" \
  >"$fixture/tui-admission-lifetime.stdout" 2>"$fixture/tui-admission-lifetime.stderr" &
tui_pid=$!
for _ in {1..300}; do
  [[ -e "$tui_started" ]] && break
  kill -0 "$tui_pid" 2>/dev/null || break
  sleep 0.02
done
if [[ ! -e "$tui_started" ]]; then
  echo "production TUI did not start" >&2
  cat "$fixture/tui-admission-lifetime.stdout" >&2 || true
  cat "$fixture/tui-admission-lifetime.stderr" >&2 || true
  exit 1
fi
case "$(cat "$tui_executable")" in
  */global-bin/.azedarach-generations/generation.*/az) ;;
  *)
    echo "production TUI launched through mutable or unexpected path: $(cat "$tui_executable")" >&2
    exit 1
    ;;
esac
test ! -d "$fixture/.git/azedarach-production-admission/production"
FAKE_GIT_MODE=primary PATH="$fixture/fake-bin:$PATH" \
  AZEDARACH_PRODUCTION_ADMISSION_WAIT_SECONDS=1 \
  "$fixture/scripts/with-production-install-admission" -- true
touch "$tui_release"
wait "$tui_pid"
tui_pid=""
grep -q "releasing admission before TUI launch" "$fixture/tui-admission-lifetime.stdout"
grep -q "Running az outside production install admission" "$fixture/tui-admission-lifetime.stdout"

FAKE_GIT_MODE=primary FAKE_GO_MARKER=managed-path-default \
  PATH="$first_installed_generation:$fixture/fake-bin:$PATH" \
  "$fixture/scripts/build-install-run.sh" --no-run \
  >"$fixture/managed-path-default.stdout" 2>"$fixture/managed-path-default.stderr"
grep -q "Caller uses retained managed generation" "$fixture/managed-path-default.stderr"
test "$("$fixture/global-bin/az" version)" = "dev (managed-path-default)"
test "$("$fixture/global-bin/azd" version)" = "dev (managed-path-default)"
test ! -e "$first_installed_generation/.azedarach-generations"

mkdir -p "$fixture/shadow-bin"
cp -L "$fixture/global-bin/az" "$fixture/shadow-bin/az"
cp -L "$fixture/global-bin/azd" "$fixture/shadow-bin/azd"
if FAKE_GIT_MODE=primary FAKE_GO_MARKER=shadow-diagnostic AZ_INSTALL_DIR="$fixture/global-bin" \
  PATH="$fixture/shadow-bin:$fixture/fake-bin:$fixture/global-bin:$PATH" \
  "$fixture/scripts/build-install-run.sh" --no-run \
  >"$fixture/shadow-diagnostic.stdout" 2>"$fixture/shadow-diagnostic.stderr"; then
  echo "build-install-run unexpectedly reported success for a shadowed caller shell" >&2
  exit 1
fi
grep -q "caller shell remains shadowed" "$fixture/shadow-diagnostic.stderr"
grep -q "stable installed /opt/homebrew/bin/az control link" "$fixture/shadow-diagnostic.stderr"
test "$("$fixture/global-bin/az" version)" = "dev (shadow-diagnostic)"
test "$("$fixture/global-bin/azd" version)" = "dev (shadow-diagnostic)"

FAKE_GIT_MODE=primary FAKE_GO_MARKER=first FAKE_CP_ASSERT_SERIAL=1 \
  FAKE_CP_GUARD="$fixture/install-critical-section" AZ_INSTALL_DIR="$fixture/global-bin" \
  PATH="$fixture/global-bin:$fixture/fake-bin:$PATH" "$fixture/scripts/build-install-run.sh" --no-run \
  >"$fixture/concurrent-first.stdout" 2>"$fixture/concurrent-first.stderr" &
first_pid=$!
FAKE_GIT_MODE=primary FAKE_GO_MARKER=second FAKE_CP_ASSERT_SERIAL=1 \
  FAKE_CP_GUARD="$fixture/install-critical-section" AZ_INSTALL_DIR="$fixture/global-bin" \
  PATH="$fixture/global-bin:$fixture/fake-bin:$PATH" "$fixture/scripts/build-install-run.sh" --no-run \
  >"$fixture/concurrent-second.stdout" 2>"$fixture/concurrent-second.stderr" &
second_pid=$!
wait "$first_pid"
wait "$second_pid"
az_marker="$(sed -n 's/^scratch build //p' "$fixture/global-bin/az")"
azd_marker="$(sed -n 's/^scratch build //p' "$fixture/global-bin/azd")"
test -n "$az_marker"
test "$az_marker" = "$azd_marker"
test -x "$first_installed_generation/az"
test -x "$first_installed_generation/azd"
test "$("$first_installed_generation/az" version)" = "dev (default)"
test "$("$first_installed_generation/azd" version)" = "dev (default)"
generation_count="$(find "$fixture/global-bin/.azedarach-generations" -mindepth 1 -maxdepth 1 -type d | wc -l | tr -d ' ')"
test "$generation_count" -ge 4

just --justfile "$fixture/justfile" --working-directory "$fixture" clean
test ! -e "$fixture/.tmp/az-install"
test -x "$fixture/global-bin/az"
test -x "$fixture/global-bin/azd"

if just --justfile "$fixture/justfile" --working-directory "$fixture" install \
  >"$fixture/just-install.stdout" 2>"$fixture/just-install.stderr"; then
  echo "just install unexpectedly provided an independent mutation path" >&2
  exit 1
fi
grep -q "Refusing unpaired install" "$fixture/just-install.stderr"

forbidden_mutator='go install[[:space:]]+\./cmd/az|go build.*-o[[:space:]]+[^[:space:]]*bin/az|(^|[;&|[:space:]])(cp|mv|ln|rm)([[:space:]]|.*).*bin/(az|azd)'
if git -C "$repo_root" grep -nE "$forbidden_mutator" -- \
  justfile 'scripts/*.sh' '.github/workflows/*.yml' \
  ':!scripts/test-build-artifact-isolation.sh'; then
  echo "repository still contains an alternative production az/azd mutator" >&2
  exit 1
fi

echo "build artifact isolation contract: PASS"
