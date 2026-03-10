# Spec Parity Brainstorm

Date: 2026-03-10
Issue: `ia`

## Problem

We have a canonical product spec in `az spec`, but we do not have a fast way to answer:

- how much of the spec is implemented
- which implementation that answer refers to
- whether the answer is based on shipped behavior, linked work, or testing evidence

This is especially important when the repository has multiple implementations, such as:

- `ts-opentui` as the primary implementation
- `go-bubbletea` as a rewrite track with partial parity

## Current State

Observed in the repo and local data:

- `az spec` has a substantial requirement corpus.
- `az spec link` coverage is still sparse relative to the requirement count.
- `ts-opentui` already has a Spec workspace, but it only shows:
  - requirement counts
  - unlinked requirements
  - integrity gaps
  - publish state
- `go-bubbletea` has a manual feature matrix in docs, separate from `az spec`.

This means the current system can answer "is the spec linked to issues?" better than it can answer "is implementation X at parity with the spec?"

## Key Constraint

A single parity percentage is not trustworthy unless we define what counts as parity.

At least three distinct notions exist:

1. Work planned: a requirement has linked issues.
2. Work implemented: a requirement has an issue that actually landed for a given implementation.
3. Work verified: a requirement has implementation evidence and test or acceptance evidence.

If these are collapsed into one number, the result will be easy to read and easy to misread.

## Recommendation

Use implementation-scoped issue labels as the issue-level flag instead of adding a brand new top-level issue field.

Recommended label set:

- `impl:shared`
- `impl:ts-opentui`
- `impl:go-bubbletea`
- optional later: `impl:gleam`

Why labels instead of a new issue field:

- `az issue create` and `az issue update` already support labels.
- labels already persist through the issue model, local store, sync, and editor flows.
- labels avoid a schema migration across all issue backends up front.
- labels can represent shared work without forcing a single-valued field.
- labels are enough to power grouped parity reporting.

The main downside is discipline: we need a constrained vocabulary and helper UX so labels do not drift.

## Parity Models

### Option A: Label-scoped issue/spec summary

Add a parity report that groups spec links by implementation label.

Example output shape:

- `ts-opentui`: 41 requirements with linked implementation issues
- `go-bubbletea`: 19 requirements with linked implementation issues
- `shared`: 12 requirements with shared implementation work
- unscoped: 87 linked issues missing an implementation label

Pros:

- cheapest path
- leverages existing issue labels and spec links
- immediately exposes missing implementation metadata

Cons:

- still measures linked work, not confirmed behavior
- can overstate parity when linked issues are incomplete or stale

### Option B: Requirement-level implementation coverage

Add implementation coverage records keyed by requirement and implementation.

Example states:

- `not_started`
- `planned`
- `implemented`
- `verified`

Pros:

- honest and explicit
- works even when one issue touches many requirements
- supports real parity dashboards

Cons:

- new data model
- more workflow overhead
- likely needs dedicated commands and UI

### Option C: Test/probe-derived parity

For implementations that expose machine-readable behavior probes or targeted acceptance harnesses, derive parity from test evidence.

Pros:

- strongest signal
- least dependent on manual hygiene

Cons:

- highest cost
- not available uniformly across implementations today

## Suggested Path

### Phase 1: Make implementation identity explicit

- standardize implementation labels on issues
- add CLI and editor affordances so users can set labels consistently
- surface missing implementation labels as a reportable hygiene gap

### Phase 2: Add parity reporting

Build a report over:

- spec requirements
- linked issues
- issue labels
- link types (`implements`, `tests`, `relates`)

Report should answer:

- how many requirements have an `implements` issue for each implementation
- how many have test evidence for each implementation
- how many linked issues are missing implementation labels
- which requirements are completely uncovered for a given implementation

### Phase 3: Add UI surfacing

Extend the Spec workspace with an implementation-aware parity view.

Good first slices:

- one subview or filter per implementation
- headline counts for `planned`, `implemented`, `verified`
- explicit "unknown due to unlabeled issue" bucket

## Concrete UX Ideas

Potential surfaces:

- `az spec parity --impl ts-opentui`
- `az spec parity --impl go-bubbletea --json`
- Spec workspace parity tab with implementation summary cards
- issue editor hint when saving an issue with a spec link but no `impl:*` label
- README summary generated from parity report instead of a hand-maintained matrix

## Data Rules

If we use labels as the implementation flag, we should enforce these rules:

- every issue linked with `implements` or `tests` should have at least one `impl:*` label
- multiple `impl:*` labels are allowed only for truly shared work
- `relates` links do not imply implementation coverage
- parity summaries must distinguish planned, implemented, and verified states
- unlabeled linked issues should reduce trust in the summary and be reported explicitly

## Open Questions

- Should acceptance requirements be counted separately from functional requirements in the headline parity score?
- Should a requirement count as covered for `impl:X` only when it has an `implements` link, or can `tests` contribute independently?
- Do we want one canonical matrix generated from `az spec`, replacing `go-bubbletea/docs/07-feature-matrix.md`, or should both coexist during migration?
- Should `impl:shared` count toward both implementations, or remain separate until implementation-specific work lands?

## Proposed Follow-up Work

1. Add constrained implementation labels and helper affordances to issue workflows.
2. Add an implementation-aware `az spec` parity report.
3. Surface parity in the `ts-opentui` Spec workspace and decide whether to generate the rewrite matrix from that report.

## Bottom Line

The fastest credible path is:

1. use labels as the implementation flag
2. group spec links by those labels
3. present parity as staged evidence, not a single raw percentage

That gives us a useful parity signal quickly without committing to a heavy issue-schema migration too early.
