---
name: workflow-subagent-orchestration-loop
description: >-
  Orchestrate complex tasks through a persistent delegate-integrate-verify loop
  using subagents, escalating to the human only when authority, intent, or
  irreversible risk truly requires it.
targets: ["claudecode"]
---

# Subagent Orchestration Loop

**Version:** 1.0
**Purpose:** Run a complex task as an orchestration agent that keeps momentum through delegation, integration, verification, and issue tracking instead of stopping early for avoidable handoffs.

## When To Use

Use this skill when:
- the task is too broad for one linear pass
- multiple independent subtasks can run in parallel
- the task needs integration across several files or domains
- you are expected to drive work to a real completion point, not just produce a plan

## Core Stance

Act like a persistent orchestration loop:

1. clarify the immediate objective
2. identify the next blocking step
3. delegate parallelizable work
4. keep doing non-overlapping work locally
5. integrate returned results
6. verify the combined result
7. repeat until the task is actually landed or genuinely blocked

Do not stop because the work became large. Break it apart and continue.

## Ralph Loop

Run this loop continuously:

1. **Orient**
   - restate the concrete user goal
   - inspect local state before making assumptions
   - identify what must be true for the task to be considered done

2. **Reduce Uncertainty**
   - do the immediate blocking investigation locally
   - avoid delegating the one answer you need before you can take the next step
   - prefer bounded facts over broad exploratory detours

3. **Delegate**
   - spawn subagents for sidecar work that can proceed independently
   - give each subagent a narrow deliverable and explicit ownership
   - if the repo uses issue tracking for non-trivial work, create child issues first and have subagents track them

4. **Advance Locally**
   - while subagents run, do integration prep, adjacent edits, or verification setup
   - never wait by reflex if useful local work remains

5. **Integrate**
   - review returned work quickly but critically
   - keep good changes, refine weak edges, and resolve interface mismatches
   - do not redo completed delegated work from scratch unless necessary

6. **Verify**
   - run the smallest meaningful checks first
   - if the task changed behavior, verify the affected path directly
   - if checks fail, feed concrete fixes back into the loop

7. **Close Or Continue**
   - land the task if it is actually complete
   - otherwise create the next bounded subtask and keep going

## Subagent Rules

- Delegate concrete outcomes, not vague exploration.
- Prefer multiple small agents over one large ambiguous agent.
- Keep write scopes disjoint whenever possible.
- Tell agents they are not alone in the codebase and must not revert others' work.
- Reuse an existing agent when the follow-up depends on its prior context.
- Only wait immediately when the next local action is blocked on that result.

## Human Escalation Threshold

Only stop for the human when one of these is true:

- **Authority gap**: approval is required for an escalated command or external side effect.
- **Intent gap**: two materially different product choices are plausible and local evidence cannot resolve which one the user wants.
- **Risk gap**: the next action is destructive, irreversible, or likely to corrupt unrelated work.
- **Dependency gap**: a required credential, service, file, or environment capability is missing and cannot be recovered locally.

Do **not** stop just because:

- the task is large
- the codebase is messy
- you found more follow-up work
- subagents are still running
- the first approach failed

If you can take another safe productive step, do that instead of asking.

## Issue Tracking

For repos that require issue tracking:

- keep one active parent issue for the session when possible
- create child issues for non-trivial delegated tasks
- update issue notes when the plan or constraints materially change
- close child issues when their bounded task is done

The issue tracker should reflect the actual execution graph, not just the initial plan.

## Verification Discipline

- Verify at the seam where integration happened.
- Prefer targeted tests before broad suites, then broaden if needed.
- If you could not verify something important, say exactly what remains unverified and why.
- Before declaring completion, check that edits, tracker state, and local git state all agree.

## Anti-Patterns

- stopping at a plan when implementation was possible
- waiting on subagents while doing nothing locally
- delegating the current critical-path blocker
- asking the human for choices that local code or repo context can answer
- treating partial progress as completion
- closing the session with uncommitted changes when local policy requires commits

## Completion Standard

The loop ends only when one of these is true:

- the requested task is implemented, integrated, verified as far as possible, and committed if required
- the remaining blocker genuinely requires human input or approval
- the remaining work has been captured as explicit follow-up issues with clear context

Default bias: continue the loop.
