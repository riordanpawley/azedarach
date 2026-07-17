---
name: code-review-loop
description: Run an iterative code review and fix cycle until findings stabilize. Use when the user asks Codex to review code, review and fix, loop review/fix, address review findings, harden a change before handoff, or keep reviewing until repeated passes find no new actionable issues. Also use proactively before Codex declares coding work done, marks an issue in_review, says a change is ready, or hands off implementation that modified code, tests, build scripts, migrations, config, or developer tooling.
---

# Code Review Loop

## Overview

Drive code review as a closed loop: inspect the change, fix actionable findings, validate, then review again. Continue until the configured clean-pass target is reached or a genuine blocker prevents progress.

## Pre-Completion Gate

Use this skill automatically when implementation appears complete. Do not wait for the user to say "code review" or "fix em" after an implementation pass.

Before saying the task is done, ready for review, ready to merge, or moving a tracked issue to `in_review`, run the review loop against the actual change set. If the review finds actionable issues, fix them, validate, and restart the clean-pass count. Only hand off after the clean-pass target is met or after preserving an explicit blocker with evidence.

Skip this gate only for tasks that did not change code or executable behavior, such as pure explanation, read-only investigation, or issue-tracker updates. If docs, tests, config, or scripts changed in a way that can affect users or developers, run the gate.

## Completion Target

Default clean-pass target:

- Use 2 consecutive clean review passes for small or medium-risk changes.
- Use 3 consecutive clean review passes for high-risk changes, shared infrastructure, security/privacy/auth paths, data migrations, concurrency, persistence, public APIs, or release-critical code.
- If the user specifies a pass count, use that count.

A pass is clean only when it finds no new actionable correctness, regression, security, reliability, data-loss, compatibility, or missing-test issue in the requested scope. Style-only preferences, speculative rewrites, and unrelated pre-existing problems do not reset the clean-pass count unless the user asked for them.

Reset the clean-pass count to 0 after any code or test change. Do not reset it for notes, issue metadata, or formatting of the final report unless those edits change executable behavior or tests.

## Revision Checkpoints

Treat the review loop as revision-incremental, not as repeated full rereads.

Before each pass, resolve and retain:

- the exact current candidate `HEAD`
- the exact merge-base revision and configured task scope
- the last completed review checkpoint, if one exists
- unresolved findings and the contracts or invariants they affect

When this skill's helper is available, select the safe range with:

```bash
.codex/skills/code-review-loop/scripts/review-range --repo <worktree> --base-revision <exact-base> --head-revision <exact-head> --scope <configured-scope> [--checkpoint-head <reviewed-head> --checkpoint-base <reviewed-base> --checkpoint-scope <reviewed-scope>] [--broader-invalidated]
```

The helper only verifies and selects an intra-candidate Git range. It does not persist evidence or decide cross-base reuse.

The first verifiable pass reviews the complete task diff from the resolved merge base to candidate `HEAD`. After every completed pass, retain a checkpoint in the task's existing review-fact channel (for example its issue record or native result channel) with:

- `head_revision`, `diff_base_revision`, and stable `scope`
- `mode` (`full` or `incremental`) and `delta_base_revision` for incremental passes
- `verdict`, `unresolved_findings`, and `affected_invariants`
- `clean_pass` and `clean_pass_target`
- `fallback_reason` when a full pass replaced an unsafe incremental pass

Do not invent a persistence or evidence schema for this skill. If the current workflow cannot recover a complete checkpoint from its existing review facts, treat it as absent and run a full pass.

A later pass may inspect only `last-reviewed-revision..current-HEAD`, plus:

- every unresolved finding
- contracts and invariants affected by the delta
- callers or tests whose assumptions the delta may have changed

Use a full task-diff pass when the checkpoint is missing or malformed, the checkpoint revision cannot be resolved or is not an ancestor of current `HEAD`, the merge base or task scope changed, or the delta invalidates assumptions outside its changed lines. Re-resolve an invalid current base or head before reviewing; if either remains unverifiable, stop rather than emitting an unusable range. Record the reason; never silently widen or narrow the review.

Incremental passes still count toward the configured two- or three-clean-pass target when they inspect a new delta or apply a distinct review angle to unresolved findings and affected contracts. An empty delta does not justify rereading unchanged lines: rotate to a materially different review angle and record what was rechecked, or do not count the pass as independent confidence evidence.

## Review Loop

1. Establish scope.
   - Inspect the user's request, active issue, branch state, and changed files.
   - Identify explicit acceptance criteria and relevant local instructions.
   - If the task is issue-tracked, keep status and notes current with concise evidence.

2. Build a review baseline.
   - Resolve the exact task diff base and candidate `HEAD` before judging.
   - Select full or incremental mode using the checkpoint rules above and state the exact range.
   - Read that range, unresolved findings, affected contracts, and nearby code before judging.
   - Run or identify the smallest meaningful validation command if the repo provides one.
   - Prefer direct evidence from code, tests, logs, and contracts over broad guesses.

3. Review in code-review stance.
   - Lead with bugs, regressions, security issues, data loss, race conditions, lifecycle leaks, broken contracts, and missing tests.
   - Ground each finding in a file and line when possible.
   - Avoid low-value comments about taste unless they hide a maintainability risk that will matter.

4. Fix actionable findings.
   - Make the smallest coherent fix that addresses the root cause.
   - Add or update focused tests when the finding is behavioral or regression-prone.
   - Leave unrelated cleanup alone; create follow-up work only when it is real and outside scope.

5. Validate.
   - Run targeted tests while developing a regression, then run the complete relevant suite when risk or repo policy requires it.
   - When a broad suite fails, capture one complete machine-readable run (for Go, prefer `go test -json ... -count=1`), extract every failed test/package, and classify the full failure set before editing.
   - Fix failures by coherent root-cause batch rather than repairing the first visible test and rerunning serially. Truncated console output must not be treated as the complete failure set.
   - Rerun the complete suite after the batch. Focused green tests support diagnosis but do not replace the complete rerun.
   - Classify unrelated or flaky failures explicitly; fix them when in scope or preserve durable follow-up evidence rather than silently ignoring them.
   - If validation fails, treat the complete classified failure set as findings and continue the loop.
   - Record commands and key assertions for the final report and issue notes.

6. Repeat.
   - Start a fresh revision-bound review pass after every fix.
   - When a fix advances `HEAD`, review the prior checkpoint revision through the new `HEAD`; uncommitted changes cannot form a verifiable incremental checkpoint and require a full task-diff pass.
   - Persist the completed checkpoint before starting the next pass or handing off.
   - Count consecutive clean passes only after validation succeeds or the remaining unrun validation is explicitly justified.
   - Stop only when the clean-pass target is met, a blocker is genuine, or the user explicitly pauses the loop.

## Review Angles

Rotate emphasis across passes so the loop does not repeat the same shallow inspection:

- Pass 1: requested behavior, obvious regressions, integration points, and tests.
- Pass 2: edge cases, error paths, lifecycle/cancellation, concurrency, persistence, and compatibility.
- Pass 3 when needed: security/privacy, operational failure modes, migration/release risk, and whether earlier fixes created new behavior.

Use the repo's domain-specific review skills when they apply, but keep this loop responsible for convergence.

## Migration Review Gate

For any database migration, schema ensure/repair logic, persistence authority change, or migration-adjacent trigger/index edit:

- Use `$database-migration-review` and apply its full pre-merge gate.
- Default to one consolidated new migration per merge to main; reject unconsolidated branch-local migration sequences unless the skill's documented correctness exception applies.
- Require safe clone-based upgrade testing against the root user database and every registered project database, in addition to fresh, historical, rollback, drift, and idempotent paths.
- Require 3 clean passes after the last migration-affecting change. A fresh-database-only green pass is not clean.

## Reporting

During an active loop, keep user updates short and evidence-oriented. Do not report every non-finding.

Final response:

- State the number of clean review passes achieved.
- Summarize fixes made and files changed.
- List validation commands and results.
- Call out any residual risk or validation that could not be run.

If reviewing only, lead with findings ordered by severity. If review-and-fix, lead with what was fixed and the achieved clean-pass count after the loop ends.

## Stop Conditions

Stop and ask or mark blocked only when:

- the next action needs authority the agent does not have
- the requested scope conflicts with repo instructions or user intent
- validation requires unavailable credentials, services, hardware, or production access
- further fixes would require a product decision that local evidence cannot answer

When stopping early, preserve the loop state: findings fixed, findings remaining, validation status, clean-pass count, and exact blocker.
