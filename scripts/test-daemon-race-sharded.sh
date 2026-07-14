#!/usr/bin/env sh
set -eu

emit_complete_jsonl() {
	file="$1"
	if [ ! -s "$file" ]; then
		return
	fi
	if [ "$(tail -c 1 "$file" | wc -l | tr -d ' ')" -eq 1 ]; then
		cat "$file"
	else
		sed '$d' "$file"
	fi
}
emit_all_output() {
	for file in "$tmpdir"/shard-*.json; do
		[ -e "$file" ] || continue
		emit_complete_jsonl "$file"
	done
}

if [ "${AZEDARACH_DAEMON_RACE_INNER:-0}" != "1" ]; then
	timeout_cmd=""
	if command -v timeout >/dev/null 2>&1; then
		timeout_cmd="timeout"
	elif command -v gtimeout >/dev/null 2>&1; then
		timeout_cmd="gtimeout"
	else
		echo "daemon race sweep requires GNU timeout" >&2
		exit 1
	fi
	tmpdir="$(mktemp -d -t azedarach-daemon-race.XXXXXX)"
	cleanup_outer() {
		rm -rf "$tmpdir"
	}
	trap cleanup_outer EXIT
	aggregate_timeout="${AZEDARACH_DAEMON_RACE_TIMEOUT:-45m}"
	kill_after="${AZEDARACH_DAEMON_RACE_KILL_AFTER:-15s}"
	cancel_grace="${AZEDARACH_DAEMON_RACE_CANCEL_GRACE:-2}"
	timeout_pid=""
	pending_cancel_status=0
	canceling=0
	cancel_supervisor() {
		exit_status="$1"
		if [ "$canceling" -eq 1 ]; then
			return
		fi
		canceling=1
		# Coalesce repeated terminal signals until the supervised process
		# group has been escalated and reaped.
		trap '' INT TERM HUP
		if [ -n "$timeout_pid" ]; then
			# GNU timeout owns a distinct process group unless --foreground is
			# used. Cancel that whole group, then escalate independently of its
			# aggregate-expiry-only --kill-after timer.
			kill -TERM "-$timeout_pid" 2>/dev/null || kill -TERM "$timeout_pid" 2>/dev/null || true
			sleep "$cancel_grace"
			kill -KILL "-$timeout_pid" 2>/dev/null || kill -KILL "$timeout_pid" 2>/dev/null || true
			wait "$timeout_pid" 2>/dev/null || true
		fi
		emit_all_output
		exit "$exit_status"
	}
	request_cancel() {
		pending_cancel_status="$1"
		if [ -n "$timeout_pid" ]; then
			cancel_supervisor "$pending_cancel_status"
		fi
	}
	# Install cancellation handlers before launching any process that could be
	# orphaned. A signal before timeout_pid assignment is remembered below.
	trap 'request_cancel 130' INT
	trap 'request_cancel 143' TERM
	trap 'request_cancel 129' HUP
	set +e
	"$timeout_cmd" --signal=TERM --kill-after="$kill_after" "$aggregate_timeout" \
		env AZEDARACH_DAEMON_RACE_INNER=1 AZEDARACH_DAEMON_RACE_TMPDIR="$tmpdir" "$0" "$@" &
	if [ -n "${AZEDARACH_DAEMON_RACE_SUPERVISOR_START_DELAY:-}" ]; then
		sleep "$AZEDARACH_DAEMON_RACE_SUPERVISOR_START_DELAY"
	fi
	timeout_pid=$!
	if [ "$pending_cancel_status" -ne 0 ]; then
		cancel_supervisor "$pending_cancel_status"
	fi
	wait "$timeout_pid"
	status=$?
	trap - INT TERM HUP
	set -e
	emit_all_output
	if [ "$status" -eq 124 ] || [ "$status" -eq 137 ]; then
		echo "daemon race sweep exceeded the $aggregate_timeout aggregate budget" >&2
	fi
	exit "$status"
fi

repo_root="$(git rev-parse --show-toplevel)"
cd "$repo_root"

# Race instrumentation must never populate the normal development namespace.
common_dir="$(git rev-parse --path-format=absolute --git-common-dir)"
cache_root="${AZEDARACH_GO_CACHE_ROOT:-$(dirname "$common_dir")/.azedarach/go}"
cache_owner="${AZEDARACH_GO_CACHE_OWNER:-}"
if [ -z "$cache_owner" ]; then
	branch="$(git symbolic-ref --quiet --short HEAD 2>/dev/null || true)"
	cache_owner="$(printf '%s' "$branch" | awk -F/ 'NF >= 3 { print "issue-" $2 }')"
	cache_owner="${cache_owner:-main}"
fi
GOCACHE="$cache_root/caches/v1/race/$cache_owner"
export GOCACHE
case " ${GOFLAGS:-} " in
	*" -trimpath "*) ;;
	*) GOFLAGS="${GOFLAGS:+$GOFLAGS }-trimpath"; export GOFLAGS ;;
esac
mkdir -p "$GOCACHE"

shard_count="${AZEDARACH_DAEMON_RACE_SHARDS:-4}"
case "$shard_count" in
	*[!0-9]*|'') echo "daemon race shard count must be a positive integer" >&2; exit 2 ;;
	0) echo "daemon race shard count must be positive" >&2; exit 2 ;;
esac
parallelism="${AZEDARACH_DAEMON_RACE_PARALLELISM:-1}"
case "$parallelism" in
	*[!0-9]*|'') echo "daemon race parallelism must be a positive integer" >&2; exit 2 ;;
	0) echo "daemon race parallelism must be positive" >&2; exit 2 ;;
esac

owns_tmpdir=0
if [ -n "${AZEDARACH_DAEMON_RACE_TMPDIR:-}" ]; then
	tmpdir="$AZEDARACH_DAEMON_RACE_TMPDIR"
else
	tmpdir="$(mktemp -d -t azedarach-daemon-race.XXXXXX)"
	owns_tmpdir=1
fi
cleanup_inner() {
	if [ "$owns_tmpdir" -eq 1 ]; then
		rm -rf "$tmpdir"
	fi
}
mark_timeout() {
	# Remain the process-group leader's supervised child until GNU timeout sends
	# its delayed KILL. If we returned/exited here, TERM-ignoring grandchildren
	# could outlive timeout and late diagnostics could race output collection.
	while :; do
		sleep 1
	done
}
trap cleanup_inner EXIT
trap mark_timeout TERM

if ! go test -race ./internal/daemon -list '^Test' >"$tmpdir/discovery"; then
	echo "daemon race sweep test discovery failed" >&2
	exit 1
fi
awk '/^Test/ { print }' "$tmpdir/discovery" >"$tmpdir/tests"
test_count="$(wc -l <"$tmpdir/tests" | tr -d ' ')"
if [ "$test_count" -eq 0 ]; then
	echo "daemon race sweep discovered no tests" >&2
	exit 1
fi

awk -v shards="$shard_count" -v dir="$tmpdir" '{ print $0 >> (dir "/names-" ((NR - 1) % shards)) }' "$tmpdir/tests"

echo "daemon race sweep: $test_count tests across $shard_count shards with at most $parallelism concurrent; 15m per shard, ${AZEDARACH_DAEMON_RACE_TIMEOUT:-45m} aggregate" >&2
failed=0
shard=0
while [ "$shard" -lt "$shard_count" ]; do
	wave_start="$shard"
	launched=0
	while [ "$shard" -lt "$shard_count" ] && [ "$launched" -lt "$parallelism" ]; do
		names="$tmpdir/names-$shard"
		if [ -s "$names" ]; then
			pattern="^($(paste -sd '|' "$names"))$"
			(
				go test -json -race ./internal/daemon -count=1 -timeout=15m -run "$pattern" >"$tmpdir/shard-$shard.json"
			) &
			echo "$!" >"$tmpdir/pid-$shard"
			launched=$((launched + 1))
		fi
		shard=$((shard + 1))
	done
	wait_shard="$wave_start"
	while [ "$wait_shard" -lt "$shard" ]; do
		pid_file="$tmpdir/pid-$wait_shard"
		if [ -s "$pid_file" ]; then
			pid="$(cat "$pid_file")"
			if ! wait "$pid"; then
				failed=1
			fi
		fi
		wait_shard=$((wait_shard + 1))
	done
done

if [ "$owns_tmpdir" -eq 1 ]; then
	emit_all_output
fi
exit "$failed"
