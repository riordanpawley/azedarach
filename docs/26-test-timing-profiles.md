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

# Canonical completion suite (alias for the cold profile)
just test

# Developer-selected package/test
just test-timing focused --package ./internal/cli --run TestCommand

# Complete profiles
just test-timing cold
just test-timing cached
just test-timing race
just test-timing integration
just test-timing migration-clone
just test-timing boundary

# Required local merge contract
just merge-gate
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

Every report also records `cache_mode` as `cleared-and-bypassed`, `bypassed`, or
`permitted`, so a zero-cache-hit run still states its selected cache policy.

The runner writes all artifacts before returning the test command's exit status
or a budget failure. Do not replace it with a shell pipeline that loses earlier
failures or reports only the last failing package.

## Profile semantics

| Profile | Cache | Scope | Purpose |
|---|---|---|---|
| `cold` | cleared, plus `-count=1` | `./...` with `-p=2` | Canonical complete before/after measurement and all-failures run with bounded package concurrency. |
| `cached` | explicitly permitted | `./...` | Measures normal repeat developer feedback; cached packages remain visible in the JSON stream. |
| `focused` | bypassed with `-count=1` | defaults to `./internal/testtiming`; override with repeated `--package` | Fast development checks without pretending to be complete coverage. |
| `integration` | bypassed with `-count=1` | daemon, daemon-process, Git, and tmux tests named `RealProcessProfile…` | Real subprocess and lifecycle contracts only. |
| `migration-clone` | bypassed with `-count=1` | issue, user-store, runtime-state, and daemon migration/repair tests | Fresh, historical-upgrade, rollback, drift, repair, reopen, and clone-isolation execution paths. |
| `race` | bypassed with `-count=1` | selected SQLite-clone, daemon-process, and concurrent Git contracts under `-race` | Focused shared-state validation that remains useful on ordinary developer hosts. |
| `boundary` | bypassed with `-count=1` | CLI/TUI executable boundary guards | Thin-client, transport-shim, and session-projection regressions; static graph checks remain in `just check-boundaries`. |

Use `--check-budgets=false` only for exploratory measurement; it never suppresses
test failures. `--output` and `--baseline` may select alternative artifact roots
or baseline files. `--package` and `--run` are intentionally accepted only by the
`focused` profile; canonical named profiles reject scope-changing overrides so
a narrow run cannot be mislabeled or compared with full-suite budgets.

## Completion and merge gates

The cold profile fixes Go package concurrency at two workers. This avoids
SQLite and subprocess contention transferring work into the daemon and Git
packages on high-core hosts while retaining package-level parallel execution.

`just test` is deliberately the cold profile, rather than a second `go test
./...` path. `just merge-gate` performs three non-overlapping responsibilities:

1. `just build` verifies the production commands compile.
2. `just test` runs ordinary semantic coverage exactly once and preserves the
   complete JSON failure stream.
3. `just check-boundaries` runs static dependency/drift checks and the small
   boundary profile. It no longer reruns all CLI, TUI, daemon, and client tests
   after the cold suite.

The merge/rebase/push hook delegates to this same recipe. It reports failures
and leaves source files untouched; golden updates remain an explicit developer
action. Run `just test-integration`, `just test-migration-clone`, or `just
test-race` when a change affects those execution modes. Their overlap with cold
is intentional mode-specific evidence, not part of the normal completion path.

## Baseline and budgets

[`testdata/test-timing-baseline-2026-07-13.json`](../testdata/test-timing-baseline-2026-07-13.json)
records the parent optimization issue's same-machine cold baseline: 283.26s wall,
134.59s user CPU, 62.86s system CPU, and the six measured slowest packages. That
complete baseline run exited 1 on the known contention-sensitive
`TestHandleTaskListReadsSQLiteProjection`; it remains a valid duration reference,
not successful acceptance evidence. New canonical runs still fail on any test
failure and preserve the full failure set.

The baseline distinguishes two kinds of limits:

- Regression limits use the lower of a hard budget and `baseline × 1.60` for the
  cold wall time and each recorded package.
- Pathology limits apply to every measured package/test. The default ceilings are
  60s per package and 30s per individual test, with a documented daemon-package
  override for its intentionally aggregated contract suite.

The epic's 120s cold-suite target is now the hard cold wall gate. The issue-store
package ceiling is tightened to 60s after fixture cloning and bounded
parallelism. The daemon package has an explicit 120s ceiling because it remains
one large package containing SQLite, subprocess, orchestration, and lifecycle
contracts; this documents the limiting contract instead of silently weakening
the default 60s pathology threshold. Tighten the committed baseline and budgets
after a reviewed same-machine cold measurement; do not loosen them merely to
make a regression pass.

The 1.60 package factor is based on repeated final-gate evidence: the TUI
package varied up to 33.79s while complete-suite wall remained 77.87–97.59s and
all 5,773 tests passed. The earlier 1.25 factor rejected both 33.79s and 26.90s
(only 0.20s above its ceiling), making a single historical package sample a
contention-sensitive merge blocker. The 60s default package pathology ceiling
and 120s total wall gate remain effective upper bounds.

Cold reports record wall plus direct-`go`-command user CPU, system CPU, and peak
resident memory from `os.ProcessState`. Descendant test-binary resource use is
not aggregated. Reports compare CPU/RSS only when the baseline declares the
same `resource_measurement` method; the historical baseline does not, so its
CPU/RSS values remain provenance-limited observations rather than deltas.
Wall and slow-package/test deltas remain comparable. Coverage equivalence is
established by the unchanged cold command scope (`./...`) plus its package/test
event inventory; a focused, cached, or specialized profile is never presented
as equivalent full-suite coverage.

## Consolidation acceptance measurement

Three consecutive same-machine cold passes on 2026-07-13 completed in 77.52s,
65.13s, and 69.49s. Every pass reported 72 packages, 5,774 tests, zero failures,
zero invalid JSON lines, and zero budget violations. This meets the required
120s target and the optional sub-90s stretch target.

The median pass compared with the committed pre-optimization baseline as
follows:

| Resource | Before | Median after | Change |
|---|---:|---:|---:|
| Wall | 283.26s | 69.49s | -75.5% |
| User CPU | 134.59s (method undocumented) | 72.03s direct command | not comparable |
| System CPU | 62.86s (method undocumented) | 44.56s direct command | not comparable |
| Peak RSS | 495.0 MiB (method undocumented) | 471.9 MiB direct command | not comparable |

The daemon remained the limiting package at 59.25–70.38s across the three
passes. The issue store was next at 22.08–24.24s, down from the 173.47s
baseline. The slowest individual test varied by host scheduling: advisor real
process launch led two passes, with the observed maximum at 14.03s; bounded
learning-consolidation retrieval and canonical claim migration preflight were
the next recurring slow contracts.

The original baseline inventory contained 69 packages and 5,761 tests; the
final cold inventory contains 72 packages and 5,774 tests under the same
`go test -json -count=1 ./...` semantic scope. The increase reflects added
runner and regression coverage, with no package/test filtering in the cold
profile. Specialized validation also passed: boundary 7 tests, real-subprocess
integration 25 tests, migration-clone 83 tests, and focused race 5 tests.

The historical CPU/RSS values were already present in the committed baseline,
but their collection method was not durably recorded. They are retained for
audit provenance and intentionally excluded from computed comparisons. A future
before/after resource comparison requires two measurements from the same
declared method; aggregate descendant accounting would require a separate
platform-aware runner rather than relabeling direct-process `ProcessState` data.

## Interpreting results

Package and individual-test rows are ordered by elapsed seconds descending, then
by name for stable diffs. Test keys use `<import-path>::<test-or-subtest>` so
budget exceptions, if ever needed, cannot collide across packages. A failed run
may contain both failed subtests and their failed parents; each `fail` event is
retained deliberately, with the raw JSON stream remaining the lossless authority.

Spec impact: none (test tooling and documentation only).
