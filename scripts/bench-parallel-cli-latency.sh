#!/usr/bin/env bash
set -euo pipefail

ROOT="$(git rev-parse --show-toplevel)"
cd "$ROOT"

AZ_BIN="${AZ_BIN:-$ROOT/bin/az}"
AZD_BIN="${AZD_BIN:-$ROOT/bin/azd}"
ISSUE_ID="${ISSUE_ID:-cjp}"
CONCURRENCY="${CONCURRENCY:-12}"
ROUNDS="${ROUNDS:-2}"
OUT_DIR="${OUT_DIR:-$ROOT/.azedarach/bench/parallel-cli-latency-$(date +%Y%m%d-%H%M%S)}"

mkdir -p "$OUT_DIR"

now_ms() {
	perl -MTime::HiRes=time -e 'printf "%.0f\n", time * 1000'
}

run_call() {
	local run_name="$1"
	local index="$2"
	local output_file="$OUT_DIR/${run_name}-${index}.tsv"
	local stdout_file="$OUT_DIR/${run_name}-${index}.out"
	local stderr_file="$OUT_DIR/${run_name}-${index}.err"
	local start_ms end_ms status command_name

	case $((index % 3)) in
		0)
			command_name="issue-get"
			start_ms="$(now_ms)"
			set +e
			AZEDARACH_LATENCY_TRACE=1 "$AZ_BIN" issue get "$ISSUE_ID" >"$stdout_file" 2>"$stderr_file"
			status=$?
			set -e
			;;
		1)
			command_name="issue-list"
			start_ms="$(now_ms)"
			set +e
			AZEDARACH_LATENCY_TRACE=1 "$AZ_BIN" issue list --limit 20 >"$stdout_file" 2>"$stderr_file"
			status=$?
			set -e
			;;
		*)
			command_name="githooks-notify"
			start_ms="$(now_ms)"
			set +e
			AZEDARACH_LATENCY_TRACE=1 "$AZ_BIN" githooks notify --hook post-checkout --project-dir "$ROOT" >"$stdout_file" 2>"$stderr_file"
			status=$?
			set -e
			;;
	esac
	end_ms="$(now_ms)"
	printf "%s\t%s\t%s\t%s\t%s\n" "$run_name" "$index" "$command_name" "$status" "$((end_ms - start_ms))" >"$output_file"
	return 0
}

run_parallel_round() {
	local run_name="$1"
	local round="$2"
	local pids=()
	local i

	for i in $(seq 1 "$CONCURRENCY"); do
		run_call "${run_name}-r${round}" "$i" &
		pids+=("$!")
	done
	for pid in "${pids[@]}"; do
		wait "$pid"
	done
}

summarize() {
	local label="$1"
	local files=("$OUT_DIR"/"${label}"-r*.tsv)
	awk -F '\t' '
		BEGIN { failures=0; count=0; total=0; max=0; min=-1 }
		{
			count++;
			status[$4]++;
			cmd[$3]++;
			total += $5;
			if ($5 > max) max = $5;
			if (min < 0 || $5 < min) min = $5;
			values[count] = $5;
			if ($4 != 0) failures++;
		}
		END {
			if (count == 0) {
				print "no samples";
				exit 1;
			}
			for (i = 1; i <= count; i++) {
				for (j = i + 1; j <= count; j++) {
					if (values[i] > values[j]) {
						tmp = values[i]; values[i] = values[j]; values[j] = tmp;
					}
				}
			}
			p50 = values[int((count + 1) * 0.50)];
			p90 = values[int((count + 1) * 0.90)];
			p95 = values[int((count + 1) * 0.95)];
			printf "samples=%d failures=%d min_ms=%d p50_ms=%d p90_ms=%d p95_ms=%d max_ms=%d avg_ms=%.1f\n", count, failures, min, p50, p90, p95, max, total / count;
		}
	' "${files[@]}"
}

echo "Building local binaries..."
just build >/dev/null

echo "Writing samples to $OUT_DIR"
echo "run	parallelism	rounds	issue_id" >"$OUT_DIR/config.tsv"
printf "cold\t%s\t%s\t%s\nwarm\t%s\t%s\t%s\n" "$CONCURRENCY" "$ROUNDS" "$ISSUE_ID" "$CONCURRENCY" "$ROUNDS" "$ISSUE_ID" >>"$OUT_DIR/config.tsv"

echo "Cold daemon run..."
AZEDARACH_LATENCY_TRACE=1 AZEDARACH_DAEMON_BIN="$AZD_BIN" "$AZ_BIN" daemon restart >/dev/null
for round in $(seq 1 "$ROUNDS"); do
	run_parallel_round cold "$round"
done

echo "Warm daemon run..."
AZEDARACH_LATENCY_TRACE=1 AZEDARACH_DAEMON_BIN="$AZD_BIN" "$AZ_BIN" daemon restart >/dev/null
AZEDARACH_LATENCY_TRACE=1 "$AZ_BIN" issue list --limit 1 >/dev/null
for round in $(seq 1 "$ROUNDS"); do
	run_parallel_round warm "$round"
done

{
	echo "cold $(summarize cold)"
	echo "warm $(summarize warm)"
} | tee "$OUT_DIR/summary.txt"

echo "Trace logs:"
echo "  CLI:    run: $AZ_BIN log --source cli --lines 200 --no-follow"
echo "  Daemon: run: $AZ_BIN log --source daemon --lines 200 --no-follow"
