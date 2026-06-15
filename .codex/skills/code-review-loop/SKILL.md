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

## Review Loop

1. Establish scope.
   - Inspect the user's request, active issue, branch state, and changed files.
   - Identify explicit acceptance criteria and relevant local instructions.
   - If the task is issue-tracked, keep status and notes current with concise evidence.

2. Build a review baseline.
   - Read the relevant diffs and nearby code before judging.
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
   - Run targeted tests first, then broader checks when risk or repo policy requires them.
   - If validation fails, treat the failure as a finding and continue the loop.
   - Record commands and key assertions for the final report and issue notes.

6. Repeat.
   - Start a fresh review pass after every fix.
   - Count consecutive clean passes only after validation succeeds or the remaining unrun validation is explicitly justified.
   - Stop only when the clean-pass target is met, a blocker is genuine, or the user explicitly pauses the loop.

## Review Angles

Rotate emphasis across passes so the loop does not repeat the same shallow inspection:

- Pass 1: requested behavior, obvious regressions, integration points, and tests.
- Pass 2: edge cases, error paths, lifecycle/cancellation, concurrency, persistence, and compatibility.
- Pass 3 when needed: security/privacy, operational failure modes, migration/release risk, and whether earlier fixes created new behavior.

Use the repo's domain-specific review skills when they apply, but keep this loop responsible for convergence.

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
