# Test timing report: cold

- Started: 2026-07-13T01:02:03Z
- Command: `go test -json -count=1 ./...`
- Result: exit 1; 12.50s wall; 8.20s user CPU; 1.10s system CPU
- Events: 2 packages; 1 tests; 1 failures; 0 invalid lines
- Raw events: `.tmp/test-timing/cold/events.jsonl`; stderr: `.tmp/test-timing/cold/stderr.txt`
- Baseline (2026-07-13): 10.00s (+25.0%)
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
