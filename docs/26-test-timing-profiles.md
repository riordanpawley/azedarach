# Canonical test timing profiles

`cmd/test-timing` is the canonical machine-readable test runner. It keeps the
complete `go test -json` stream even when tests fail, then emits deterministic
package/test duration tables, every failure identity and output, baseline
deltas, and budget violations.

## Commands

Flags precede positional arguments; the runner accepts no positional arguments.

```bash
# Small default self-check
just test-timing focused

# Developer-selected package/test
just test-timing focused --package ./internal/cli --run TestCommand

# Complete profiles
just test-timing cold
just test-timing cached
just test-timing race
just test-timing integration
```

Artifacts are written beneath `.tmp/test-timing/<profile>-<UTC timestamp>/`:

- `events.jsonl` is the unmodified complete stdout stream from `go test -json`.
- `stderr.txt` preserves command diagnostics that are not JSON events.
- `report.json` is the versioned machine-readable measurement and comparison.
- `report.md` is the same result rendered for review, sorted slowest first.

Cached packages are marked with `cached: true` in `report.json` and `(cached)`
in the Markdown result column; their replayed individual-test rows are marked the
same way. This makes cache use observable without inferring it from unexpectedly
short wall time. Because Go replays historical elapsed values for cache hits,
replayed package/test durations are reported but excluded from current pathology
budgets; the cached profile's current wall time remains budgeted. The raw stream
remains authoritative.

The runner writes all artifacts before returning the test command's exit status
or a budget failure. Do not replace it with a shell pipeline that loses earlier
failures or reports only the last failing package.

## Profile semantics

| Profile | Cache | Scope | Purpose |
|---|---|---|---|
| `cold` | cleared, plus `-count=1` | `./...` | Canonical complete before/after measurement and all-failures run. |
| `cached` | explicitly permitted | `./...` | Measures normal repeat developer feedback; cached packages remain visible in the JSON stream. |
| `focused` | bypassed with `-count=1` | defaults to `./internal/testtiming`; override with repeated `--package` | Fast development checks without pretending to be complete coverage. |
| `race` | bypassed with `-count=1` | `./...` under `-race` | Complete shared-state/race validation; intentionally has a separate wall budget. |
| `integration` | bypassed with `-count=1` | daemon test harness, IPC transport, Git, tmux, and monitor packages | Explicit real-boundary contract matrix, separate from ordinary focused tests. |

Use `--check-budgets=false` only for exploratory measurement; it never suppresses
test failures. `--output` and `--baseline` may select alternative artifact roots
or baseline files. `--package` and `--run` are intentionally accepted only by the
`focused` profile; canonical complete profiles reject scope-changing overrides so
a narrow run cannot be mislabeled or compared with full-suite budgets.

## Baseline and budgets

[`testdata/test-timing-baseline-2026-07-13.json`](../testdata/test-timing-baseline-2026-07-13.json)
records the parent optimization issue's same-machine cold baseline: 283.26s wall,
134.59s user CPU, 62.86s system CPU, and the six measured slowest packages. That
complete baseline run exited 1 on the known contention-sensitive
`TestHandleTaskListReadsSQLiteProjection`; it remains a valid duration reference,
not successful acceptance evidence. New canonical runs still fail on any test
failure and preserve the full failure set.

The baseline distinguishes two kinds of limits:

- Regression limits use the lower of a hard budget and `baseline × 1.25` for the
  cold wall time and each recorded package.
- Pathology limits apply to every measured package/test. The default ceilings are
  60s per package and 30s per individual test, with a documented package override
  for the existing issue-store suite while its dedicated optimization work lands.

The epic's 120s cold-suite target is an optimization goal, not a regression
ceiling. Keeping it separate prevents today's canonical run from failing while
still rejecting material deterioration and newly pathological tests. Tighten the
committed baseline and budgets after a reviewed same-machine cold measurement;
do not loosen them merely to make a regression pass.

## Interpreting results

Package and individual-test rows are ordered by elapsed seconds descending, then
by name for stable diffs. Test keys use `<import-path>::<test-or-subtest>` so
budget exceptions, if ever needed, cannot collide across packages. A failed run
may contain both failed subtests and their failed parents; each `fail` event is
retained deliberately, with the raw JSON stream remaining the lossless authority.

Spec impact: none (test tooling and documentation only).
