# Test timing report: cold

- Started: 2026-07-13T01:02:03Z
- Command: `go test -json -count=1 ./...`
- Test-result cache: cleared-and-bypassed
- Timing budgets: diagnostic-only
- Build cache: `normal/issue-dhc` (retained-build-cache); before 100 bytes/2 files; after 140 bytes/3 files; delta 40 bytes/1 files; family 500 bytes; decision `within-limits`
- Resource measurement: `direct-go-command-process-state-v1` (direct `go` command process; descendant test-binary resources are not aggregated)
- Concurrent Go-process load: max `4` total / `0` external across `25` samples (`ps-pid-ppid-comm-v1`); overlap `false`
- Validation lease: held `true`; request `req-1`; class `aggregate`; scope ``; purpose ``; execution ``; source ``; override ``; profile `cold`
- Result: exit 1; 12.50s wall; 8.20s user CPU; 1.10s system CPU; 64.0 MiB peak RSS
- Events: 2 packages; 1 tests; 1 failures; 0 invalid lines
- Raw events: `.tmp/test-timing/cold/events.jsonl`; stderr: `.tmp/test-timing/cold/stderr.txt`
- Baseline (2026-07-13): 10.00s (+25.0%)
- CPU baseline: 10.00s user (-18.0%); 2.00s system (-45.0%)
- Peak RSS baseline: 128.0 MiB (-50.0%)
- Budget violations: 1

## Slowest packages

| Package | Seconds | Result | Baseline delta |
|---|---:|---|---:|
| `example/slow` | 9.00 | fail | +12.5% |
| `example/fast` | 1.00 | pass | — |

## Slowest tests

| Test | Seconds | Result |
|---|---:|---|
| `example/slow::TestPathological` | 8.50 | fail |

## Failures

### `example/slow::TestPathological`

```text
sentinel failure
```

## Budget violations

- test `example/slow::TestPathological`: 8.50s > 8.00s (default test budget)

## Peak concurrent Go processes

No matching process snapshot was available.
