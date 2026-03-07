---
name: workflow-spec-to-plan-az
description: "Spec -> Plan Skill (`az issue` backend)"
targets: ["claudecode"]
---

# Spec -> Plan Skill (`az issue` backend)

**Version:** 1.0  
**Purpose:** Translate the current Markdown spec (`docs/spec/*.md`) into an actionable `az issue` plan (epic, tasks, dependencies, and traceability notes).

## Scope

- Use **current docs-based spec** as source of truth:
  - `docs/spec/03-workflow-spec.md`
  - `docs/spec/04-functional-requirements.md`
  - `docs/spec/05-edge-cases-and-failure-spec.md`
  - `docs/spec/06-acceptance-catalog.md`
  - related cross-reference files in `docs/spec/`
- Use `az issue` for planning artifacts.
- Do **not** assume or require future `az spec ...` commands.

## When To Use

Use this skill when asked to:
- create a plan from spec
- break down `docs/spec` requirements into issues
- generate epic/task structure for a feature area
- map AZ-FR/AZ-AT contracts to implementation tasks

## Mandatory Rules

- Every created task must trace to at least one `AZ-FR-*` ID.
- Link relevant `AZ-AT-*` scenarios when they define validation scope.
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
  - `design`: implementation outline + referenced `AZ-FR-*`
  - `acceptance`: verification expectations + referenced `AZ-AT-*`

### 3) Review Before Accepting

Validate:
- no task without `AZ-FR-*` reference
- dependencies only where needed (avoid over-constraining graph)
- task slices are independently completable
- naming is clear and action-oriented

### 4) Record Plan Summary

Update parent issue notes with:
- created epic/task IDs
- dependency highlights
- FR/AT coverage summary
- unresolved ambiguities or follow-up questions

## Command Playbook

```bash
# Create epic
az issue create "<epic title>" --type epic --priority 2 --parent <parent-id>

# Create child task
az issue create "<task title>" --type task --priority 2 --parent <epic-id> \
  --description "..." --design "Refs: AZ-FR-...." --acceptance "Refs: AZ-AT-...."

# Add sequencing dependency
az issue dep add --type blocks <downstream-task-id> <upstream-task-id>

# Capture plan summary
az issue update <parent-id> --notes "Plan summary ..."
```

## Definition Of Done

This skill is complete when:
- an `az issue` plan exists for the requested scope
- tasks are traceably linked to current Markdown spec IDs
- dependencies are explicit and minimal
- parent issue notes capture resumable plan context

## Anti-Patterns

- Creating tasks from intuition without citing `AZ-FR-*`
- Referencing planned `az spec` commands as required inputs
- Dumping a giant single task instead of sequenced slices
- Leaving dependency logic implicit
