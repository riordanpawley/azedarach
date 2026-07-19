# Canonical test timing profiles

`cmd/test-timing` is the canonical machine-readable test runner. It keeps the
complete `go test -json` stream even when tests fail, then emits deterministic
package/test duration tables, every failure identity and output, baseline
deltas, and budget violations. Ordinary local profiles report those violations
as diagnostic warnings; only the controlled `ci-timing` profile may turn them
into a failing performance gate.

Ordinary development entrypoints run directly with their worktree-isolated Go
cache and test roots. This includes `just build`, local named test profiles, raw
`go ...` diagnostics, benchmarks, race diagnostics, and boundary checks. They
do not enter daemon admission or create publication evidence.

`just merge-gate` and `just review-gate` use the daemon wrapper to record
publication authority for the complete build, cold suite, artifact-contract,
and boundary sequence. Push and review publication requests start immediately,
including while development work or timing-capacity work is active. Compatible
exact-revision publication may reuse completed authoritative evidence;
concurrent publication requests execute independently. Review evidence is
stronger than push evidence for the same execution contract; push evidence
never authorizes review. Integration readiness accepts publication proof only
when the daemon request, machine evidence, worker packet, and clean candidate
`HEAD` all name the same source revision.

Only `just test-ci-timing` enters queued timing-capacity admission. Its
aggregate request waits for already-running unleased Go processes to quiesce,
then samples the complete controlled timing command; Go work that appears after
admission invalidates the result. Nested managed recipes must prove membership
through the outer wrapper's inherited kernel authorization descriptor; a
public request id or status snapshot never admits work. Wrapper death stops
renewal and its supervised command tree. Inspect capacity owners and waiters
with `just validation-status`, or stream changes without compiling Go code with
`just validation-watch`.

Within timing-capacity admission, a bounded shared bypass prevents starvation
without creating an unbounded focused-work lane. If shared capacity validators
are already active when the oldest aggregate capacity request queues, exactly
one later shared capacity request may join that generation. Further shared
capacity requests drain behind the aggregate. The daemon counts durable started
capacity requests after the aggregate's queue sequence, so concurrent clients
and daemon replacement cannot reset the allowance. An aggregate capacity run
can therefore be overtaken by at most one shared capacity command, while safe
non-compiling commands remain eligible and aggregate capacity execution remains
exclusive from shared capacity work. This policy does not gate ordinary
development or delay push/review publication.

Ticket-scoped capacity requests also carry the daemon-authoritative issue
priority. Admission is priority-first and FIFO within equal priority. Once a
later higher-priority request has overtaken an older request, that durable
bypass debt protects the older request at the next compatible admission point;
daemon replacement and concurrent clients cannot reset it. `az validation
status` reports the effective queue position, issue priority, bypass count, and
whether ordering follows `priority_fifo` or `bounded_fairness`.

The runner establishes the mandatory database-isolation boundary before any
test binary starts. It snapshots the configured root-user database, the current
project database, and every registered project database into a pre-open refusal
set, then supplies temporary `HOME`, `XDG_CONFIG_HOME`, user database, project
registry, and project database roots. Use `just test` for broad validation; do
not substitute a bare `go test ./...` when claiming the safe broad-suite
contract. Real-database migration checks require explicit
`AZEDARACH_USER_DB_CLONE` or `AZEDARACH_PROJECT_DB_CLONES` paths to safe
temporary clones. Configured originals remain refused even if one is supplied
through a clone variable. The `migration-clone` profile snapshots every supplied
clone independently for each package before starting package processes, then
runs those processes in parallel with package-local HOME, config, and default
database roots. Its status line and v5 report identify the
`package-isolated-parallel` mode and the exact package-local clone paths.

## Commands

Flags precede positional arguments; the runner accepts no positional arguments.

```bash
# Small default self-check
just test-timing focused

# Canonical completion suite (alias for the cold profile)
just test

# Developer-selected package/test
just test-timing focused --package ./internal/cli --run TestCommand

# Raw diagnostic command (outside daemon admission)
go test -timeout 30s ./internal/daemon -run TestName

# Complete profiles
just test-timing cold
just test-timing cached
just test-timing race
just test-timing integration
just test-timing migration-clone
just test-timing boundary

# Authoritative only on the controlled, versioned CI runner
just test-ci-timing

# Required local merge contract
just merge-gate
```

Reviewers use `just review-gate` for the same canonical execution contract with
explicit ticket-scoped `review_evidence` authority. A later repository push of
the exact revision may reuse that stronger evidence; a push gate can never
authorize review in the opposite direction.

Artifacts are written beneath `.tmp/test-timing/<profile>-<UTC timestamp>/`:

- `events.jsonl` is the unmodified complete stdout stream from `go test -json`.
- `stderr.txt` preserves command diagnostics that are not JSON events.
- `report.json` is the versioned machine-readable measurement and comparison.
- `report.md` is the same result rendered for review, sorted slowest first.

Controlled CI timing reports sample the host process tree and separate the
validator's own Go compiler, linker, vet, and test processes from external Go
load. The daemon retains that capacity-run overlap evidence. Local semantic
profiles run directly and keep timing/overlap observations diagnostic.

Cached packages are marked with `cached: true` in `report.json` and `(cached)`
in the Markdown result column; their replayed individual-test rows are marked the
same way. This makes cache use observable without inferring it from unexpectedly
short wall time. Because Go replays historical elapsed values for cache hits,
replayed package/test durations are reported but excluded from current pathology
budgets; the cached profile's current wall time remains budgeted. The raw stream
remains authoritative.

Every v5 report records `test_result_cache_mode` as `cleared-and-bypassed`,
`bypassed`, or `permitted`. The compatibility `cache_mode` field has the same
test-result meaning. Neither field describes compiled build objects. The
`build_cache` object separately records its namespace and retention policy,
bytes/files before and after, deltas, family bytes, thresholds, and the selected
warning/refusal/maintenance decision.
The `timing_budget_policy` field is `diagnostic-only` for local runs and
`controlled-median-enforced` for the derived authoritative CI aggregate.

The runner writes all artifacts before returning the test command's exit status
or a budget failure. Do not replace it with a shell pipeline that loses earlier
failures or reports only the last failing package.

The command root handles interrupt and SIGTERM through context cancellation.
Managed Go commands run in dedicated process groups; cancellation and partial
startup kill the complete group and verify its exit before temporary private
database roots are removed. Cleanup failures are returned rather than ignored.

## Profile semantics

| Profile | Test-result cache | Build-cache namespace | Scope | Purpose |
|---|---|---|---|---|
| `cold` | cleared, plus `-count=1` | normal / lifecycle owner | `./...` with `-p=4` | Canonical local correctness and all-failures run; timing violations are diagnostic. |
| `ci-timing` | cleared before every sample, plus `-count=1` | normal / lifecycle owner | three or more complete `./...` samples with `-p=4` | CI-only timing authority; gates the per-metric median and retains every sample report. |
| `cached` | explicitly permitted | normal / lifecycle owner | `./...` | Measures normal repeat developer feedback; cached packages remain visible in the JSON stream. |
| `focused` | bypassed with `-count=1` | normal / lifecycle owner | defaults to `./internal/testtiming`; override with repeated `--package` | Fast development checks without pretending to be complete coverage. |
| `integration` | bypassed with `-count=1` | normal / lifecycle owner | daemon, daemon-process, Git, and tmux tests named `RealProcessProfile…` | Real subprocess and lifecycle contracts only. |
| `migration-clone` | bypassed with `-count=1` | normal / lifecycle owner | issue, user-store, runtime-state, operations-store, and daemon migration/repair tests in package-isolated parallel processes | Fresh, historical-upgrade, rollback, drift, repair, reopen, and clone-isolation execution paths without sharing mutable clone identity. |
| `race` | bypassed with `-count=1` | race / lifecycle owner | selected SQLite-clone, daemon-process, and concurrent Git contracts under `-race` | Focused shared-state validation that remains useful on ordinary developer hosts. |
| `boundary` | bypassed with `-count=1` | normal / lifecycle owner | CLI/TUI executable boundary guards | Thin-client, transport-shim, and session-projection regressions; static graph checks remain in `just check-boundaries`. |

Local profiles always treat budgets as diagnostic, even if
`--check-budgets=true` is passed; that flag can enforce budgets only for
`ci-timing` after its environment contract passes. It never suppresses test
failures. `--output` and `--baseline` may select alternative artifact roots
or baseline files. `--package` and `--run` are intentionally accepted only by the
`focused` profile; canonical named profiles reject scope-changing overrides so
a narrow run cannot be mislabeled or compared with full-suite budgets.

## Completion and merge gates

The cold profile fixes Go package concurrency at four workers. This avoids
SQLite and subprocess contention transferring work into the daemon and Git
packages on high-core hosts while retaining package-level parallel execution.

`just test` is deliberately the cold profile, rather than a second `go test
./...` path. `just merge-gate` performs four non-overlapping responsibilities:

1. `just build` verifies the production commands compile into worktree-local
   `.tmp/az-test` binaries without mutating user-global installed runtime assets.
2. `just test` runs ordinary semantic coverage exactly once and preserves the
   complete JSON failure stream.
3. `just test-build-contract` executes the real `build` and `clean` recipes in
   an isolated fixture and verifies they preserve existing `bin/az` and
   `bin/azd` link targets. It also verifies `build-install-run` rejects linked
   worktrees before mutation, rolls back a failed link migration, serializes
   concurrent installers, and commits `az` plus `azd` through one atomic
   generation switch. The stable public links never target the repository.
4. `just check-boundaries` runs static dependency/drift checks and the small
   boundary profile. It no longer reruns all CLI, TUI, daemon, and client tests
   after the cold suite.

The merge/rebase/push hook delegates to this same recipe. It reports failures
and leaves source files untouched; golden updates remain an explicit developer
action. Run `just test-integration`, `just test-migration-clone`, or `just
test-race` when a change affects those execution modes. Their overlap with cold
is intentional mode-specific evidence, not part of the normal completion path.

## Correctness timeouts versus performance budgets

The `-timeout` values in every profile are generous correctness guards. They
remain locally authoritative: a hung test exits non-zero and emits Go goroutine
stacks. Test failures, invalid JSON, database-isolation refusals, build-cache
hard-limit refusals, boundary failures, and build failures also remain fatal in
`just test` and `just merge-gate`.

Wall, package, and individual-test budgets are performance observations. A
developer machine can be affected by thermal state, background work, VM load,
and hardware differences, so a local budget violation is written to
`report.json` and `report.md` but does not change correctness success.

## Controlled CI timing authority

`.github/workflows/controlled-timing.yml` is deliberately manual and targets
the versioned self-hosted label `azedarach-timing-v1`. The runner must be a
dedicated 8-vCPU/16-GiB machine image with Go 1.25.7, no concurrent Go
validation, and a clean Go test-result cache before each sample. The daemon
timing-capacity lease supplies exclusive capacity admission and rejects
observed overlapping Go work.

The CLI refuses timing authority unless all controlled markers, the aggregate
lease identity, runner/toolchain/resource declarations, clean-per-sample cache
policy, and at least three samples are present. It runs the full cold scope for
every sample, retains each complete JSON/report directory, then gates the
per-metric median against the versioned baseline. A single wall-clock sample or
scheduler outlier therefore cannot decide the result.

The workflow cannot prove physical CPU/RAM allocation or absence of unrelated
non-Go host load from inside this repository. Those are external runner
provisioning assumptions attached to the versioned self-hosted label and must
be audited when that label/image changes. GitHub-hosted runner labels are not
treated as authoritative because their hardware and ambient load are outside
this contract. Local machines intentionally cannot satisfy these assumptions.

## Baseline and budgets

[`testdata/test-timing-baseline-2026-07-13.json`](../testdata/test-timing-baseline-2026-07-13.json)
records the parent optimization issue's same-machine cold baseline: 283.26s wall,
134.59s user CPU, 62.86s system CPU, and the six measured slowest packages. That
complete baseline run exited 1 on the known contention-sensitive
`TestHandleTaskListReadsSQLiteProjection`; it remains a valid duration reference,
not successful acceptance evidence. New canonical runs still fail on any test
failure and preserve the full failure set.

The baseline distinguishes two kinds of diagnostic limits locally and
authoritative limits in controlled CI:

- Regression limits use the lower of a hard budget and `baseline × 1.60` for the
  cold wall time and each recorded package.
- Pathology limits apply to every measured package/test. The default ceilings are
  60s per package and 30s per individual test, with a documented daemon-package
  override for its intentionally aggregated contract suite.

The epic's 120s cold-suite target is the controlled-CI cold wall gate. The issue-store
package ceiling is tightened to 60s after fixture cloning and bounded
parallelism. The daemon package has an explicit 120s ceiling because it remains
one large package containing SQLite, subprocess, orchestration, and lifecycle
contracts; this documents the limiting contract instead of silently weakening
the default 60s pathology threshold. Tighten the committed baseline and budgets
after a reviewed same-machine cold measurement; do not loosen them merely to
make a regression pass.

During worker iteration, prefer focused/package gates. Run one complete cold
failure-batch after the implementation stabilizes, classify and repair its full
failure set, and avoid starting overlapping broad matrices. Review runs only
the change-sensitive integration, migration-clone, race, or boundary profiles.
The final root/integration merge gate remains the one canonical full semantic
gate after accepted issue branches have been assembled.

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

Spec: `req-controlled-ci-timing` defines the correctness/timing authority split.
