---
name: workflow-spec-maintenance
description: "Spec Maintenance Skill"
targets: ["claudecode"]
---

# Spec Maintenance Skill

**Version:** 1.1
**Purpose:** Keep `docs/spec/` synchronized with `ts-opentui` behavior changes using a dedicated Spec Sync subagent.

## Mandatory Policy

For `ts-opentui`, this workflow is required whenever behavior contracts may change. Every such task must end with exactly one documented outcome:
- `docs/spec/` updated to match behavior changes, or
- issue notes contain `Spec impact: none` with file-specific justification proving the change is implementation-only.

Do not mark work complete without one of these outcomes.

## When To Use

Use this skill whenever a change affects any of the following:
- User-visible behavior in board/session/git/pr/dev flows
- Status mapping, sync semantics, or retry/failure behavior
- Guardrails, destructive-action handling, or consistency contracts
- Acceptance criteria or release-gate expectations

Typical triggers:
- edits in `ts-opentui/src/`
- edits in `docs/spec/`
- requests mentioning "spec", "acceptance", "AZ-FR", "AZ-AT", or "docs/spec"

If a trigger appears, assume spec review is required unless you can explicitly prove "implementation-only" scope.

## Subagent + Skill Combo

### 1) Delegate Spec Analysis

Spawn the **Spec Sync Agent** (`.rulesync/subagents/spec-sync.md`) with:
- parent issue ID
- concise behavior delta summary
- changed code file list
- any known FR/AT/F-case IDs

### 2) Require Integrated Edits

The subagent must update linked sections where needed:
- workflow (`03`)
- requirements (`04`)
- failure cases (`05`)
- acceptance (`06`)
- range/cross-reference updates (`06` and `08`)
- top-level invariant (`README`) only when cross-cutting

If no section changes are needed, the subagent must produce a concise no-impact rationale tied to changed files.

### 3) Review and Merge

Before accepting the patch:
- verify no "orphan" FRs without acceptance links
- verify no new acceptance scenario without FR link
- verify ID ranges still match actual IDs
- verify duplicate ID checks pass

### 4) Record Spec Outcome in Issue

Before completion, add a short "spec follow-up" note:
- If spec changed: list updated spec files and key ID deltas (AZ-FR/AZ-AT/Case F).
- If no spec change: include `Spec impact: none`, changed code files, and why behavior contracts did not change.

Suggested no-impact template:

```text
Spec follow-up:
- Spec impact: none
- Changed files: <file1>, <file2>
- Rationale: <why change is implementation-only and does not alter behavior contract>
```

## Integration Checklist

Use this checklist after spec edits:

```bash
# Confirm new/changed IDs are referenced where expected
rg -n "AZ-FR-|AZ-AT-|Case F-" docs/spec -S

# Duplicate definition checks
rg "^### AZ-AT-[0-9]{4}\b" docs/spec/06-acceptance-catalog.md -o | sort | uniq -d
rg "^- AZ-FR-[0-9]{4}[a-z]?:" docs/spec/04-functional-requirements.md -o | sort | uniq -d
rg "^### Case F-[0-9]{3}[a-z]?:" docs/spec/05-edge-cases-and-failure-spec.md -o | sort | uniq -d
```

Expected result: duplicate checks return no lines.

## Definition Of Done

This skill is complete when:
- spec text reflects the implemented behavior change
- updates are integrated across relevant sections (not bolted on)
- FR/AT/F-case IDs and ranges are internally consistent
- issue notes include a "spec follow-up" summary for resumability

When no spec edit is needed, completion instead requires a `Spec impact: none` note with file-specific rationale.

## Anti-Patterns

- Adding only one bullet to `docs/spec/README.md` while skipping normative sections
- Creating new `AZ-FR-*` without acceptance coverage
- Extending release-gate ranges without adding corresponding scenarios
- Renumbering existing IDs for cosmetic ordering
- Skipping spec updates without a `Spec impact: none` issue note
