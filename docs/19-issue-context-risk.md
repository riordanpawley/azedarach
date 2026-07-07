# Issue Context Risk

`az issue context-risk <issue-id>` emits a compact read-only summary for repeated local failure risk before worker closeout.

The command is advisory by default. It is meant to make narrow repeated failures visible without gating every child under a large root.

Use `--summary` for the bounded closeout-oriented payload explicitly. Summary output is also the default and includes level, confidence, top signals, closeout prompts, handoff fields, related issue IDs/counts, at most three evidence snippets, and degraded/timeout metadata when a scan cannot complete. Use `--full` only when you need the raw evidence packet.

## Evidence Model

The unit is a local overlap cluster, not the root issue. The daemon checks recent sibling and directly related issues, then compares structured locality evidence:

- `worker_evidence.v1.files_changed` from `worker-integration-ready` mailbox events.
- Issue observation payload fields: `files_changed`, `changed_files`, `changed_symbols`, `symbols`, `tests_changed`, `tests`, `root_cause`, `invariant`, and `regression_validation`.
- Risk evidence from `risk.recorded`, `validation.failed`, and review-related observation events.

Broad parent membership alone is not a signal. Generic commands such as `go test ./...` are validation evidence, not locality evidence, unless the issue also records explicit `tests` or `tests_changed` fields.

## Confidence

The packet reports:

- `none`: no local overlap was found, or the target has no structured locality evidence yet.
- `fyi`: a low-confidence overlap exists; workers should glance at it during closeout.
- `medium`: repeated local overlap should trigger bounded closeout prompts.
- `high`: repeated local overlap with stronger evidence should require a diagnosis or structured risk note before closeout.

Medium and high packets ask:

- What invariant was added or preserved?
- Which related consumers were audited?
- What regression test or validation proves this repeated failure will not recur?

## Handoff Fields

Workers should prefer structured handoff fields over prose-only notes:

- `files_changed`
- `root_cause`
- `invariant`
- `changed_symbols`
- `tests_changed`
- `related_consumers_audited`
- `regression_validation`

These fields can live in `worker_evidence.v1` where supported or in issue observation payloads such as `risk.recorded`.

## Orchestrator Use

Run `az issue context-risk <worker-issue> --since 14d --summary --json` when a worker is approaching `in_review` or when a repeated local failure is suspected. Add `--full` only for deliberate evidence expansion.

Use the result as local context:

- `none` or `fyi`: do not block closeout only because the issue belongs to a large root.
- `medium`: ask the bounded prompts if the worker has not already answered them.
- `high`: ask for a diagnosis or structured risk note before accepting closeout unless a human explicitly waives it.

Do not promote this into a blanket root-level gate. The signal is useful only when the overlapping files, symbols, tests, or risk observations are narrow enough to name.

## Closeout Flow

Context risk is part of closeout, not only a manual inspection command.

- `az issue update <issue-id> --status in_review` evaluates context risk before marking the issue ready for review.
- `az orchestrate integrate --issue <issue-id>` includes context risk in integration guidance and withholds close commands when high risk lacks closeout evidence.
- `az orchestrate integrate --issue <issue-id> --apply` fails before merge/close when high risk lacks closeout evidence.
- `az issue close <issue-id>` evaluates context risk before daemon-owned integration and cleanup.

The closeout paths use the same daemon-owned risk calculation as standalone `context-risk`; they request compact packets so JSON closeout responses do not carry unbounded related evidence.

High risk does not always block. It blocks only when the target issue has no target-side closeout evidence: `root_cause`, `invariant`, `regression_validation`, or a structured risk note. This keeps the gate focused on repeated local failures with no diagnosis, while allowing workers that already supplied structured evidence to proceed.

## User Stories

- As an orchestrator reviewing a worker, I want `az orchestrate integrate --issue <worker>` to show repeated local failure risk before it suggests closing the worker, so I do not integrate a repeated mistake just because the worker says it is ready.
- As a worker moving my issue to `in_review`, I want a high-risk repeated-locality warning to stop me until I record diagnosis or validation evidence, so the orchestrator gets actionable closeout context.
- As a maintainer closing an issue directly, I want `az issue close` to run the same closeout risk check as orchestration, so bypassing orchestration does not bypass the repeated-failure signal.
- As a maintainer working under a huge epic, I want broad parent membership to stay quiet, so the process does not punish ordinary large-root work.
- As a worker on isolated work with no structured locality evidence, I want context risk to report `none` and stay out of my way.

## Edge Case Flow Analysis

- Large root with 100+ unrelated children: improves the situation. Candidate count can be large, but no shared file/symbol/test cluster means no risk escalation and no blanket root gate.
- Two recent siblings touched the same file and one recorded risk, while the current worker has no diagnosis or validation: improves the situation. The worker cannot mark `in_review` or close through normal paths until it records structured closeout evidence.
- Same repeated file cluster, but the current worker already supplied `worker_evidence.v1` with validation assertions or a risk note: improves the situation. The packet remains visible, but closeout can proceed because the repeated-risk question was answered.
- Current issue has no `files_changed`, symbols, or tests evidence: mostly neutral. The packet reports `none` and asks workers to record structured handoff fields; it does not invent risk from titles or root membership.
- Related top-level issues with no parent: improves the situation. The command reads each top-level issue mailbox under its own issue id, so standalone related work can still form an overlap cluster.
- Missing or stale mailbox evidence: partly bad. The model will under-report risk when workers do not emit structured evidence. The closeout flow mitigates this by making missing target handoff fields visible, but it cannot infer precise locality from prose-only notes.
- High-risk false positive from a shared utility file: partly bad but bounded. The packet names the exact cluster and asks for diagnosis/validation rather than blocking all children under the root. Supplying target-side evidence unblocks the flow.
- Automation callers that bypass the CLI and call daemon close/status commands directly: improved. The daemon also enforces high-risk closeout evidence for `in_review` and close, so the core lifecycle path still sees the gate.
