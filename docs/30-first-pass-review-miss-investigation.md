# First-pass review miss investigation (DUG)

Status: human-facing investigation; no review policy is changed by this document.

Session/artifact location: issue `dug`, attach with `az attach dug`; worktree `/Users/riordan/prog/azedarach-dug`. The durable source events are available with `az ticket events <issue>`. Exact controlled-experiment prompts and results are reproduced below.

## Executive finding

The sampled miss-focused history does not support “more generic passes” as the remedy. Six exact candidate checkpoints were called clean before 15 later confirmed defects were found at those same revisions. Recorded first-pass recall was 0/15. The misses cluster around a small number of selectable risk shapes: incomplete subprocess endings, failure to trace an invariant through adjacent consumers, freshness/authority bypasses, fixture constructions that prove only the producer and not the consumer, and evidence claims that were not checked against executable/raw facts.

The recommended change is typed prompt selection plus auditable coverage, not a universal checklist. A reviewer should receive the exact revision and acceptance contract, select the smallest applicable risk matrix, enumerate that matrix once, audit adjacent consumers, and return one consolidated batch. A clean verdict should be impossible to substantiate without covered cells, skipped cells with reasons, inspected active paths, and evidence/tool provenance.

## Method and limits

Sources:

- `az ticket get-many --ids dih,dtv,dqe,dtr,dmi,dmj,dte,dtx,duc,duf --with-notes --json`
- ascending ticket event streams for the same issues
- DTV mailbox history, Git objects, exact diffs, and parent revisions
- three retained fresh read-only reviews of immutable DTV range `d7396f39656d9e095f2b2666e9c497cdda050cdb..727f9174a099a2051e13a3411d36d4266bdba0b0`, plus one discarded control-contaminated pilot

Ground truth is conservative: only later findings that were returned as substantive and repaired are counted. Style preferences, speculative concerns, rejected findings, and defects introduced only by a later repair are excluded from recall for the earlier revision.

Important limits:

- Historical events usually preserve an angle summary, not the exact reviewer prompt. “Exact prompt” therefore cannot be reconstructed for the historical runs; this is a confirmed evidence gap, not an invitation to infer one.
- Claims of independent review usually lack provider, model, reviewer ID, thread/session ID, and incarnation. Independence cannot be verified from the durable evidence.
- Historical token usage was not recorded. Wall time below is event-to-event elapsed time, and machine cost is reported only as commands/repetitions when present. Neither is a correctness gate.
- The experiment is a small, single-diff directional case study with one reviewer per retained arm, no crossover or replication, unavailable per-run model/incarnation data, and unequal context bundles. It reports observed yield only; it cannot attribute a causal recall difference to prompt type.

## Revision-bound chronology and measurements

| Issue / exact candidate | Recorded clean context | Later confirmed defects at same revision | First-pass recall | First finding delay | Rework / validation diagnostic |
|---|---|---:|---:|---:|---|
| DTV `727f9174a` | Pass 1: requested behavior/active path; pass 2: cancellation, concurrency, persistence, retry | 4: independent cleanup budgets; detached compensation before deferred cleanup; rooted lease/projection cleanup; unconditional cleanup for pre-existing in-progress issue | 0/4 | 9m 23s | Five later repair revisions before final `708d9cce3`; focused, race, Linux cross-compile, merge gate, and two production starts |
| DQE `a69a06876` | Self and independent reviews reported clean; focused sharder repetitions | 2: readiness published before TERM trap; cleanup registered after fallible PID parsing | 0/2 | 2m 53s | One repair revision `3d9ddbcb`; repeated focused and race runs |
| DTR `3b331122e` | Independent repair review reported lifecycle, start/error paths, portability clean | 3: leader-exit descendant hang; authority mapping failed open; no deterministic shared-clone lock reproduction | 0/3 | 6h 37m | Repair to `a0da3280b`; three typed clean passes, focused/race/Linux build, real root + five-project clone evidence |
| DMI `0d4aa1ad7` | Independent review reported five repaired integrity seams clean | 3: revision-scoped invalidation; mailbox fallback reviewer authority; profile-level command supersession | 0/3 | 4h 42m | Persistence-authority rereview and safe-clone checks; repair to `729128074` |
| DMI `729128074` | Final independent repair review reported clean | 2: named test selectors executed zero tests; clean projection admitted a real worktree edit before observer refresh | 0/2 | 1h 50m | Restart delivery then failed because the session already existed; repair remained outstanding in sampled history |
| DIH `be18dffae` | Two clean passes plus cold/boundary evidence | 1: AST guard proved authoritative assignments but not consumption by comparisons/callbacks | 0/1 | 5m 03s | Repair introduced consumer assertions and adversarial fixtures; later DIH cycles found additional bypass constructions on later revisions |

Aggregate over these exact checkpoints: 0 of 15 eventual confirmed defects were reported by the recorded clean pass, so observed first-pass recall was 0%. Eventual later reviews produced all 15 unique findings (100% eventual later-review yield relative to this conservative ground truth), sometimes across multiple cycles. This result is selection-biased—the issue deliberately sampled misses—and must not be interpreted as the repository-wide review recall rate. Its valid use is diagnostic and directional: the event histories show recurring coverage omissions associated with clean verdicts.

Later-repair defects are counted separately. For example, DTV `95dfe8e9c` introduced/exposed a descendant retaining `CombinedOutput` pipes; `33cfcd97f` then missed normal-parent-exit descendant cleanup; later repair tests had a signal/FIFO causality race and Linux `pidfd_open` portability blocker. Those do not reduce recall for `727f9174a`, but they show why a repair review must cover the complete affected invariant whenever the local delta changes lifecycle ownership.

## Missed-defect taxonomy

| Cause | Confirmed evidence | Prompt implication |
|---|---|---|
| Requested-fix confirmation | DTV clean review covered terminalization/retry but missed four compensation endings; DMI verified named integrity fixes but missed a post-projection mutation counterexample | Ask for counterexamples to the invariant, not confirmation of the named fix |
| Incomplete lifecycle matrix | DTV and DTR repeatedly missed parent-exits-first, descendant-retains-pipe, early setup failure, cancellation, and cleanup completion combinations | Select a subprocess matrix and enumerate every ending before verdict |
| Wrong abstraction level | DIH inspected exact assignments/dataflow helpers rather than the package import boundary and actual consumer binding | State the durable boundary/invariant; inspect both producer and consumer, then try bypass constructions |
| Adjacent-consumer omission | Rooted cleanup, shared process-group runner, mailbox fallback, and worktree freshness paths were found only later | Record direct callers, alternate entrypoints, recovery paths, and shared helpers inspected |
| Fixture blindness | DQE readiness happened before trap installation; DTR lacked a causal shared-clone contention reproduction; DMI test selectors ran zero tests | Treat test harness ordering and test-count output as production evidence that itself needs review |
| Authority/freshness omission | DMI admitted stale/worker-only/mailbox evidence and asynchronous Git projection gaps | For authority changes, enumerate source, refresh/fence, bypass, stale state, and failure-to-refresh |
| Implementer anchoring | Self-review evidence reused implementation terminology and asserted the intended fix; later reviewers constructed different failure orderings | Independent prompt should omit implementation claims beyond acceptance context and immutable diff |
| Serial one-finding returns | DTV required multiple returned cycles for related process lifecycle endings | Require one consolidated batch after completing the selected matrix; do not stop after first finding |
| Prompt/evidence incompleteness | Historical exact prompts and reviewer incarnations are absent; clean records often list broad angles without covered/skipped cells | Persist exact prompt hash/text reference, identity/incarnation, matrix, inspected paths, tools, and skips |
| Prompt overload / context exhaustion | Not directly provable from available events | Keep as a hypothesis; measure tokens/context and unfinished matrix cells in future runs rather than asserting causation |

## Controlled immutable-diff experiment

Candidate: DTV `d7396f3..727f917`. Each retained reviewer was fresh, read-only, received no ticket/mail/later-commit history, and was told not to mutate files. Run identities were `/root/generic_strict`, `/root/matrix_review`, and `/root/invariant_review`; the harness did not expose a reusable provider/model/incarnation identifier, so those fields are unavailable. The known historical ground truth for this exact revision is the four compensation defects in the chronology table.

The first generic pilot (`/root/generic_review`) was excluded from quantitative results. It accidentally started one test from the current worktree, noticed and interrupted it after about one second, used none of that output, and reran its evidence in exact candidate archives. The brief later-source access still violated the strict control. The retained generic run therefore forbade current-worktree source access and recorded its archive manifest.

Prompt C intentionally carries the pre-candidate DTV acceptance bundle, which named worktree/session/lease leak freedom, cancellation, bounded cleanup, and retry. That arm tests the upper bound of an acceptance-rich invariant prompt; it is not a blinded prompt-only comparison with A or B. The experimenter selected that context after knowing the historical outcome, although the text itself came from the issue created before candidate `727f9174a`. Treat C's yield as directional/oracle evidence, not an unbiased effect estimate.

### Prompt A — generic

> Controlled read-only review experiment. In `/Users/riordan/prog/azedarach-dug`, review immutable Git range `d7396f39656d9e095f2b2666e9c497cdda050cdb..727f9174a099a2051e13a3411d36d4266bdba0b0`. Do not edit repository files. Prompt: Review this change for substantive correctness, reliability, regressions, and missing tests. Return one consolidated batch of actionable findings with file/line, severity, evidence, and fix; then list files/adjacent code inspected and commands/tools used. Do not inspect Azedarach ticket events, issue notes, mail, later commits, or current-worktree source contents; only immutable Git objects for the candidate and base. If executing tests, materialize a disposable exact-candidate archive outside the repo, print/verify its path and candidate manifest before each test, and never invoke tests from the current worktree. Explicitly state if clean. Do not mutate the historical candidate.

### Prompt B — subprocess matrix

> Controlled read-only review experiment. In `/Users/riordan/prog/azedarach-dug`, review immutable Git range `d7396f39656d9e095f2b2666e9c497cdda050cdb..727f9174a099a2051e13a3411d36d4266bdba0b0`. Do not edit files. Do not inspect ticket events, issue notes, mail, or later commits. Review as a subprocess lifecycle change. Enumerate and inspect every matrix cell before verdict: startup, partial-start failure, normal parent exit with live descendants, error exit, cancellation before/during/after cleanup, timeout/kill escalation, retained stdout/stderr pipes, cleanup failure, retry, goroutine/channel completion, Unix and non-Unix portability. Audit all changed functions and their direct production callers/tests. Return one consolidated batch of actionable findings with file/line, severity, evidence, fix; then covered/skipped cells, files/adjacent code inspected, commands/tools. Explicitly state if clean.

### Prompt C — invariant and adjacent consumers

> Controlled read-only review experiment. In `/Users/riordan/prog/azedarach-dug`, review immutable Git range `d7396f39656d9e095f2b2666e9c497cdda050cdb..727f9174a099a2051e13a3411d36d4266bdba0b0`. Do not edit files. Do not inspect ticket events, issue notes, mail, or later commits. Establish the intended invariant from the diff: every session-start operation must durably terminalize after launch failure while cleanup is bounded and leaves no worktree/session/lease/process leak, cancellation works, and retry succeeds. Trace each mutated function through all active callers, side effects, recovery/compensation endings, and relevant tests. Try to construct counterexamples at each ownership boundary. Return one consolidated batch of actionable findings with file/line, severity, evidence, fix; distinguish confirmed defects from speculative concerns; then list invariant paths, adjacent consumers, commands/tools. Explicitly state if clean.

### Results

Results come from the exact retained fresh-review outputs. No reviewer received ticket/mail/later-commit findings. As disclosed above, C received the pre-candidate acceptance bundle and is an acceptance-rich upper-bound arm.

| Prompt | Confirmed ground-truth matches | Other actionable observations | Speculative/rejected | Files/adjacent consumers/tools | Wall/token diagnostic |
|---|---:|---:|---:|---|---|
| A strict generic | 1/4 (25%): shared cleanup budget starving lifecycle rollback | 1 new, not historically validated: terminal publication can race active-dedupe retirement and defeat immediate retry | 0 | All 7 changed files; operation runtime, dedupe/publication, cleanup and worktree paths; verified candidate archive manifest plus focused tests | Wall and token use unavailable |
| B subprocess matrix | 2/4 (50%): in-progress worktree cleanup bypass; shared cleanup budget | Fixture/assertion gap corroborated the shared-budget miss: context-ignorant fake and omitted issue status | 1: manager-wide failed-operation dedupe blast radius was plausible but no duplicate side effect was proven | All 7 changed files; operation runtime, Git runner/worktree, daemon routing; immutable Git inspection, no tests | Wall and token use unavailable |
| C invariant/consumer | 4/4 (100%): cancelled-context/detached compensation; rooted lease cleanup; in-progress worktree cleanup; whole-unit/per-stage boundedness | 0 | 1: manager-wide dedupe blast radius, explicitly classified speculative | Full operation/session/rooted/orchestration/recovery chain; all ownership side effects and callers; immutable Git inspection, no tests | Wall and token use unavailable |

Observed yield increased with the amount of invariant and acceptance context supplied, but reviewer variance, single runs, and unequal context prevent causal attribution. The case nevertheless exposes a concrete routing issue: the subprocess was nested inside a stateful compensation transaction spanning issue lifecycle, worktree projection, tmux, resources, rooted leases, and operation terminal state. A subprocess-only matrix can therefore select the wrong primary abstraction. Typed selection should use one primary matrix plus declared secondary cells, or choose the outer ownership invariant when several risk types nest.

The retained generic reviewer found one historical defect and a new candidate-level retry race. The matrix reviewer found two historical defects and made the fixture/assertion weakness explicit; the excluded pilot had independently noticed the same fake and missing issue-status assertion, so that observation is not unique to B. The acceptance-rich invariant reviewer was the only run to cover all historical ground truth. No run produced a rejected style-only finding. Replicated randomized/counterbalanced runs across several immutable diffs and reviewers are required before attributing differences to prompt structure.

### Discarded generic pilot

Exact prompt:

> Controlled read-only review experiment. In `/Users/riordan/prog/azedarach-dug`, review immutable Git range `d7396f39656d9e095f2b2666e9c497cdda050cdb..727f9174a099a2051e13a3411d36d4266bdba0b0`. Do not edit any files. Prompt: Review this change for substantive correctness, reliability, regressions, and missing tests. Return one consolidated batch of actionable findings with file/line, severity, evidence, and fix; then list files/adjacent code inspected and commands/tools used. Do not inspect Azedarach ticket events, issue notes, mail, or later commits; only the candidate range and its base repository state. Explicitly state if clean.

Observed result: 2/4 historical matches (cancelled-context compensation and shared cleanup budget), plus the context-ignorant fake/missing issue-status assertion. The reviewer inspected all seven changed files, operation runtime, session cleanup/reconciliation, tmux runner, and fakes. It reran the reported focused tests in verified exact-candidate archives, and no output from the accidental current-worktree invocation informed the findings. The arm remains excluded because control compliance is binary; correction after later-source access cannot restore a blinded run.

## Recommended risk-selected prompts

Common preamble for every review:

> Review exact base `<base>` through exact candidate `<head>` against the attached acceptance contract. Do not inspect later revisions or prior findings. Select the smallest applicable risk type below. Inspect the complete assigned revision and return one consolidated finding batch. A clean verdict requires files/callers inspected, tools and test counts, covered cells, skipped cells with reasons, and confirmed-versus-speculative classification.

Stateful/concurrent:

> Enumerate states and transitions; attempt/completion ordering; success/failure combinations; authorization and bypass paths; side effects; recovery; stale/fresh observations; and adjacent consumers. Construct at least one adversarial interleaving per ownership boundary.

Subprocess:

> Enumerate startup, partial start, normal exit, error exit, cancellation at each phase, timeout/TERM/KILL, parent-before-descendant exit, retained output pipes, cleanup failure, retry, completion signaling, and platform/shell portability. Prove no ending leaks a process, descriptor, goroutine, worktree, lease, or terminal result.

Boundary/authority:

> Name the authority and invariant. Trace authoritative production producers through every active consumer. Try aliases, wrappers, receiver methods, cross-file cycles, stale projections, fallback sources, substituted consumers, missing/failed refresh, and absent capability. Prefer package/dependency or typed authority guards over symbol-name heuristics.

Test/evidence integrity:

> Verify that each claimed command executed nonzero intended tests, its raw outcome supports the claim, the evidence is exact-revision and authoritative, supersession/invalidation is scoped correctly, and fixtures causally establish the ordering they claim. Treat harness cleanup and portability as subprocess behavior when applicable.

No risk type selected:

> Review requested behavior, regressions, active integration points, tests, and adjacent consumers. Explain why no typed matrix applies. Do not silently inherit the universal matrices.

### When full enumeration is mandatory

Require the full subprocess matrix when the diff starts, waits for, signals, cancels, times out, captures output from, or cleans up a child process—even if the changed file is “only a test runner.” Require the stateful matrix when correctness depends on ordering, cache/projection freshness, leases, retries, or multiple authorities. Require adjacent-consumer audit whenever a shared helper, protocol/domain type, persistence projection, boundary guard, or lifecycle cleanup function changes.

After a repair, reuse unchanged layers only when the delta cannot affect their assumptions. If ownership, lifecycle completion, authority, or shared helpers changed, fall back to the complete affected invariant.

## Required review evidence fields

- Exact `base_revision`, `head_revision`, scope, prompt text/hash, provider/model, reviewer ID, thread/session ID, and incarnation.
- Acceptance/spec revision and context bundle manifest; explicit declaration that later revisions/prior findings were unavailable for controlled reviews.
- Review angle and selected matrix type.
- Covered cells and skipped cells with reasons.
- Changed files, production callers, alternate entrypoints, recovery paths, adjacent consumers, and tests inspected.
- Commands/tools, exit status, intended test selector, actual nonzero test count, and raw artifact reference.
- Deduplicated findings with severity, location, invariant, counterexample, evidence, and suggested fix.
- Confirmed, speculative, rejected, and pre-existing classifications.
- Reused layers and exact reasons; invalidated layers and exact reasons; fallback-to-full reason.
- `clean_pass`, target, unique-finding count, cumulative ground-truth matches when running an experiment.
- Diagnostic-only wall time, input/output tokens, context utilization/truncation, and machine-heavy commands.

## Mechanized guard candidates

1. Reject clean review evidence missing exact revisions, selected matrix, covered/skipped cells, inspected production paths, or reviewer incarnation.
2. Parse `go test -json` and reject evidence whose selector executed zero intended tests or whose raw result contradicts the assertion.
3. Build a changed-symbol/direct-caller manifest and flag a clean verdict when active callers or recovery paths are neither inspected nor explicitly skipped.
4. Detect subprocess APIs/shell execution/process-group helpers and require the subprocess matrix evidence type.
5. Detect projection/cache/lease/authority mutations and require source/freshness/bypass matrix cells.
6. Require consolidated finding delivery only after matrix completion; permit urgent early delivery without ending the review.
7. Track finding signatures by immutable revision to calculate first-pass recall, marginal unique yield, and repair invalidation without mixing revisions.
8. Preserve raw prompt/context manifests and reviewer identity so independence and cross-provider comparisons are auditable.

These guards should validate evidence and route risk; they should not pretend static detection proves semantic correctness.

## Non-Azedarach and provider portability

The templates are deliberately product- and provider-neutral. Examples:

- A Go build runner using `exec.CommandContext` needs parent/descendant, retained-pipe, signal escalation, and platform matrix coverage even without tmux or Azedarach.
- A Node.js worker using `child_process.spawn` needs `exit` versus `close`, detached process groups, stream ownership, abort, and Windows/POSIX coverage.
- A Python service using `asyncio.create_subprocess_exec` needs cancellation, `communicate()` pipe draining, child-tree termination, and retry/idempotency coverage.
- A database-backed approval service needs authoritative source, stale cache, fallback, supersession, failed refresh, and concurrent mutation coverage even when its provider uses pull requests rather than local worktrees.

Provider portability requires a capability contract, not provider-specific wording: the reviewer must be able to read exact immutable revisions, search adjacent code, run declared tools read-only, emit structured evidence, and expose identity/incarnation and cost diagnostics. If a provider cannot expose one field, record it as unavailable; never fabricate independence or coverage. Repository-specific commands such as `just`, Azedarach issue IDs, or tmux belong in the context bundle, not in the generic prompt or product default.

## Relationship to linked work

- DTE should own longitudinal marginal-yield measurement across a less selection-biased population.
- DTX is the implementation venue for typed risk selection and structured review fields; this investigation does not change that policy.
- DUF progress checkpoints should include matrix completion and context-consumption diagnostics for long reviews.
- DMJ independent pre-handoff review should persist reviewer identity/incarnation and prevent prior-finding leakage in controlled runs.
- Mechanized guard implementation should be split into bounded tickets rather than added to this human-findings investigation.

## Human acceptance gate

This investigation remains open/in review until issue-specific durable evidence records `investigation_findings_accepted=true`. Review or integration status alone is not acceptance. Preserve issue `dug`, its session, and this worktree while awaiting that decision.
