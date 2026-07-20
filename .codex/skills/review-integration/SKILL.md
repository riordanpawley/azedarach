---
name: review-integration
description: Review and integrate Azedarach issues that are in_review. Use when Codex is asked to run a reviewer/integrator session, sweep active issues that are ready for review, inspect worker evidence and diffs, return actionable findings to live or paused issue sessions, close accepted work, or get in_review issues integrated.
---

# Review Integration

## Overview

Use this skill for an integration-owner session, not for a worker's local pre-completion review. The job is to move `in_review` issues toward closure while preserving worker ownership when the worker session still exists.

## Workflow

1. Prime and discover candidates.
   - Run `az prime`.
   - List review-ready work with `az issue list --status in_review --limit 50 --json` or, for a known graph, `az orchestrate status --root <root> --json --summary`.
   - Prefer issues with structured `worker_evidence.v1` closeout, no unresolved blockers, and a clear parent/root.

2. Build a bounded review batch.
   - Run `az issue get <issue-id>`.
   - Read recent evidence with `az issue events <issue-id> --type evidence.submitted --limit 5 --json`.
   - Run `az issue context-risk <issue-id> --since 14d`; treat `none` and `fyi` as advisory, investigate or record risk evidence for higher levels.
   - Check runtime ownership with `az session status <issue-id>` and, when a root is known, `az orchestrate status --root <root> --json --summary`.
   - Check the durable issue type against its intent and submitted evidence before reviewing it as implementation work. Use `investigation` for research, discovery, spikes, analysis, experiments, audits, and any task whose primary deliverable is findings, options, recommendations, or an Agent Band/session discussion rather than a mergeable change. Correct a discrepant type with `az issue update <issue-id> --type investigation` (or the actual matching type) and record why.
   - Investigation-like issues require explicit, issue-specific human acceptance before integration, terminal close, cancellation, session stop, or worktree cleanup. A request to run an integration sweep, an issue being `in_review`, reviewer agreement with the findings, or the absence of a diff is not human acceptance.
   - For these issues, summarize the findings and link or identify every artifact and the relevant Agent Band/session location for the human. Preserve the issue, session, and worktree in review/waiting-human state, and record `review.recorded` evidence that human review is pending. Do not infer that an investigation produced nothing merely because it has no code diff.

3. Delegate non-trivial review inspection.
   - Launch a fresh ephemeral review subagent for each selected issue, bounded by the available subagent slots. Distinct issues may be reviewed concurrently; never assign the same issue to multiple delegates.
   - Give each delegate the issue ID, acceptance context, `worker_evidence.v1`, context-risk result, exact issue worktree, diff base, and relevant repository instructions.
   - Keep delegates read-only. They may inspect the diff and nearby code and run non-mutating validation, but they must not edit files, mutate issue/session state, send findings, accept or return a review, integrate, or close work.
   - Require the independent delegate to inspect the complete assigned revision and return one consolidated actionable finding batch before yielding. Confirming the requested repair or finding the first defect is not a stop condition; continue through the remaining diff and affected consumers unless missing authority/input blocks inspection or an immediate safety issue requires an early return.
   - Select a compact typed risk matrix instead of asking for generic pass counts. For stateful or concurrent work cover state, attempt/completion ordering, success/failure combinations, authorization/bypass paths, side effects, recovery, and adjacent consumers. For subprocess work cover every lifecycle ending and portability. For persistence work invoke the specialized migration gate.
   - Require a structured result with `verdict` (`clean` or `findings`), exact revision, review angle, deduplicated findings containing severity/file/line/impact/suggested fix/validation, reused evidence layers, covered matrix cells, deliberately skipped cells with reasons, commands run, and residual risks. A delegate that lacks enough evidence must return a finding rather than guessing clean.
   - For repair review, inspect the verified delta plus unresolved findings and every affected invariant. Fall back to the complete affected invariant, including its callers and adjacent consumers, whenever the local delta cannot establish completeness. Record the fallback reason.
   - The owning orchestrator validates each returned packet and remains solely responsible for durable review-return, review-accept, integration, and close operations. Perform an orchestrator-side spot check when context risk is high or the packet conflicts with submitted evidence; do not repeat the full diff review in the orchestrator context by default.
   - Validate enough to accept or reject the closeout. Prefer focused tests first; run broader checks when the blast radius justifies it.
   - Do not use `in_review` to mean blocked. If a dependency blocks integration, add or preserve a `blocks` edge and record blocker evidence.

4. Route non-trivial findings back to live sessions.
   - If the issue has a live or paused session, do not patch non-trivial actionable findings in the reviewer or integration worktree.
   - Move the issue back to active work when appropriate: `az issue update <issue-id> --status in_progress`.
   - Send the finding to the worker with:

```bash
az orchestrate message --root <root-issue> --issue <worker-issue> --type review-finding --body '{"type":"review-finding","severity":"medium","file":"path/to/file.go","line":123,"finding":"Explain the defect and impact.","suggested_fix":"Describe the expected fix.","validation":["go test ./internal/package"]}'
```

   - Include enough detail for the worker to act without rereading your full review transcript: severity, file, line, finding, suggested fix, and validation expectation.
   - Record the handoff with `az issue record <issue-id> --type review.recorded --summary "Returned review finding to live issue session." --data '<same-json-or-summary>'`.

5. Use narrow reviewer-side exceptions.
   - Patch directly only for trivial integration hygiene, already-closed or no-session branches, explicit human direction, or mechanical conflict repair needed to complete an otherwise accepted integration.
   - Record every exception with `az issue record <issue-id> --type review.recorded --summary "<why reviewer fixed directly>"`.
   - If the direct fix changes behavior or adds risk, stop treating it as trivial and route it back to the worker or create a follow-up/blocker issue.

6. Integrate accepted work.
   - Immediately before any terminal action, re-check the durable issue type and intent-based classification. For `investigation` or investigation-like work, proceed only when durable issue evidence contains explicit, issue-specific human acceptance of the findings and disposition. If it does not, leave the issue unclosed for human review even when the reviewer considers the work complete.
   - When evidence, review, and validation are acceptable, and every required human-acceptance gate has passed, close through the authoritative path: `az issue close --id <issue-id>`.
   - Use `az orchestrate integrate --issue <issue-id> --json` only when you need extra inspection or repair guidance; normal accepted-worker completion should use `az issue close`.
   - If close/integration fails, keep the issue `in_progress` or `in_review` according to the actual state, record the failure evidence, and repair through the daemon-backed workflow rather than ad-hoc branch surgery.

7. Record closeout.
   - For each accepted issue, record commands run, key assertions, files changed, exact reviewed revision, review angle, unique findings, reused layers, covered and deliberately skipped matrix cells, the reason for any extra pass or full-invariant fallback, risks, and whether the issue was closed.
   - Before ending the reviewer/integrator session, run `az orchestrate complete-check --root <root>` for graph roots you were asked to finish.
   - Leave any unclosed `in_review` issue with explicit evidence: returned finding, blocker, failed validation, missing evidence, or remaining review risk.
