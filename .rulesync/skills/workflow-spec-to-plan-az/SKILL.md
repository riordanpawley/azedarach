---
name: workflow-spec-to-plan-az
description: "Spec -> Plan Skill (`az issue` backend)"
targets: ["claudecode"]
---

# Spec -> Plan Skill (`az issue` backend)

**Version:** 2.0  
**Purpose:** Translate current `az spec` requirement/link records into an actionable `az issue` plan (epic, tasks, dependencies, and traceability notes).

## Scope

- Use `az spec` records as source of truth:
  - `az spec req list`
  - `az spec req get`
  - `az spec link list` (for existing coverage/traceability context)
- Use `az issue` for planning artifacts.

## When To Use

Use this skill when asked to:
- create a plan from spec
- break down `AZ-FR` / `AZ-AT` requirements into issues
- generate epic/task structure for a feature area
- map spec contracts to implementation tasks

## Mandatory Rules

- Every created task must trace to at least one requirement reference (`local_id` and/or `external_code`).
- Link relevant acceptance requirements when they define validation scope.
- Use `az issue` as system of record (no markdown TODO trackers).
- Keep tasks implementation-ready and narrowly scoped.
- Record a plan summary in parent issue notes.

## Subagent + Skill Combo

### 1) Delegate Plan Synthesis

Spawn the **Spec -> Plan Agent**: `.rulesync/subagents/spec-to-plan-az.md`

Provide:
- parent issue ID
- planning scope (feature/topic)
- optional constraints (team size, sequencing, must-have/maybe)

### 2) Require Traceable Plan Artifacts

The subagent should produce:
- one parent epic (or update existing parent if instructed)
- child tasks linked to parent
- dependency edges where sequencing is required
- issue fields populated with spec trace context:
  - `description`: user-visible objective and scope
  - `design`: implementation outline + referenced requirement refs
  - `acceptance`: verification expectations + referenced acceptance refs

### 3) Review Before Accepting

Validate:
- no task without a requirement reference
- dependencies only where needed (avoid over-constraining graph)
- task slices are independently completable
- naming is clear and action-oriented

### 4) Record Plan Summary

Update parent issue notes with:
- created epic/task IDs
- dependency highlights
- requirement/acceptance coverage summary
- unresolved ambiguities or follow-up questions

## Command Playbook

```bash
# Inspect in-scope requirements
az spec req list
az spec req get <requirement-ref>

# Create epic
az issue create "<epic title>" --type epic --priority 2 --parent <parent-id>

# Create child task
az issue create "<task title>" --type task --priority 2 --parent <epic-id> \
  --description "..." --design "Refs: fr4201 (AZ-FR-4201) ..." --acceptance "Refs: at2901 (AZ-AT-2901) ..."

# Add sequencing dependency
az issue dep add --type blocks <downstream-task-id> <upstream-task-id>

# Capture plan summary
az issue update <parent-id> --notes "Plan summary ..."
```

## Definition Of Done

This skill is complete when:
- an `az issue` plan exists for the requested scope
- tasks are traceably linked to current `az spec` requirements
- dependencies are explicit and minimal
- parent issue notes capture resumable plan context

## Anti-Patterns

- creating tasks from intuition without citing requirement refs
- planning from stale markdown docs after spec has moved to `az spec`
- dumping a giant single task instead of sequenced slices
- leaving dependency logic implicit
