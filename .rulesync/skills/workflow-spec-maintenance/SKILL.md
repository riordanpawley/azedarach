---
name: workflow-spec-maintenance
description: "Spec Maintenance Skill"
targets: ["claudecode"]
---

# Spec Maintenance Skill

**Version:** 2.0  
**Purpose:** Keep `az spec` requirement/link records synchronized with `ts-opentui` behavior changes using a dedicated Spec Sync subagent.

## Mandatory Policy

For `ts-opentui`, this workflow is required whenever behavior contracts may change. Every such task must end with exactly one documented outcome:
- `az spec` requirement/link records updated to match behavior changes, or
- issue notes contain `Spec impact: none` with file-specific justification proving the change is implementation-only.

Do not mark work complete without one of these outcomes.

## When To Use

Use this skill whenever a change affects any of the following:
- user-visible behavior in board/session/git/pr/dev flows
- status mapping, sync semantics, or retry/failure behavior
- guardrails, destructive-action handling, or consistency contracts
- acceptance criteria or release-gate expectations

Typical triggers:
- edits in `ts-opentui/src/`
- edits touching spec CLI/service surfaces
- requests mentioning "spec", "acceptance", "AZ-FR", "AZ-AT", or `az spec`

If a trigger appears, assume spec review is required unless you can explicitly prove "implementation-only" scope.

## Subagent + Skill Combo

### 1) Delegate Spec Analysis

Spawn the **Spec Sync Agent** (`.rulesync/subagents/spec-sync.md`) with:
- parent issue ID
- concise behavior delta summary
- changed code file list
- any known requirement references (`local_id`, `external_code`)

### 2) Require Integrated Spec Updates

The subagent must update az-native spec records where needed:
- requirement records via `az spec req create/update`
- issue trace links via `az spec link add/remove --type implements|tests`
- optional relationship links (`blocks`, `relates`) when explicitly relevant

If behavior is implementation-only and not product-contract-relevant, the subagent must produce a concise no-impact rationale tied to changed files.

### 3) Review and Merge

Before accepting the patch:
- verify behavior-affecting tasks have issue<->spec links
- verify linked requirements use appropriate link types (`implements` vs `tests`)
- verify any new requirement has stable identity (`local_id`, optional `external_code`)

### 4) Record Spec Outcome in Issue

Before completion, add a short "spec follow-up" note:
- if spec changed: list updated requirement refs and link changes
- if no spec change: include `Spec impact: none`, changed code files, and why behavior contracts did not change

Suggested no-impact template:

```text
Spec follow-up:
- Spec impact: none
- Changed files: <file1>, <file2>
- Rationale: <why change is implementation-only and does not alter behavior contract>
```

## Integration Checklist

Use this checklist after spec updates:

```bash
# Inspect requirements and key refs
az spec req list
az spec req get <requirement-ref>

# Inspect issue/spec trace links
az spec link list --issue <issue-id>
az issue get <issue-id>
```

Expected result: changed behavior is represented by updated requirement content and issue-linked traceability.

## Definition Of Done

This skill is complete when:
- `az spec` records reflect the implemented behavior change
- issue<->spec trace links are updated with correct link types
- issue notes include a "spec follow-up" summary for resumability

When no spec update is needed, completion instead requires a `Spec impact: none` note with file-specific rationale.

## Anti-Patterns

- updating only markdown docs while leaving `az spec` stale
- introducing behavior changes without updating requirement records/links
- using only `relates` links where `implements` or `tests` is the correct contract
- skipping spec updates without a `Spec impact: none` issue note
