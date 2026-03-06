# Spec Maintenance Skill

**Version:** 1.0
**Purpose:** Keep `docs/spec/` synchronized with `ts-opentui` behavior changes using a dedicated Spec Sync subagent.

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

## Subagent + Skill Combo

### 1) Delegate Spec Analysis

Spawn the **Spec Sync Agent** (`.claude/agents/spec-sync.md`) with:
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

### 3) Review and Merge

Before accepting the patch:
- verify no "orphan" FRs without acceptance links
- verify no new acceptance scenario without FR link
- verify ID ranges still match actual IDs
- verify duplicate ID checks pass

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

## Anti-Patterns

- Adding only one bullet to `docs/spec/README.md` while skipping normative sections
- Creating new `AZ-FR-*` without acceptance coverage
- Extending release-gate ranges without adding corresponding scenarios
- Renumbering existing IDs for cosmetic ordering
