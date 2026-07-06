# Issue Context Risk

`az issue context-risk <issue-id>` emits a compact read-only packet for repeated local failure risk before worker closeout.

The command is advisory by default. It is meant to make narrow repeated failures visible without gating every child under a large root.

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

Run `az issue context-risk <worker-issue> --since 14d` when a worker is approaching `in_review` or when a repeated local failure is suspected.

Use the result as local context:

- `none` or `fyi`: do not block closeout only because the issue belongs to a large root.
- `medium`: ask the bounded prompts if the worker has not already answered them.
- `high`: ask for a diagnosis or structured risk note before accepting closeout unless a human explicitly waives it.

Do not promote this into a blanket root-level gate. The signal is useful only when the overlapping files, symbols, tests, or risk observations are narrow enough to name.
