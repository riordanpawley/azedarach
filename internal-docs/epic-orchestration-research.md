# Epic Orchestration Research

> Research into orchestrating parallel Claude Code sessions via beads and azedarach

**Date:** 2025-12-27
**Status:** Initial Research Complete

---

## Table of Contents

1. [Executive Summary](#executive-summary)
2. [Current Beads Capabilities](#current-beads-capabilities)
3. [Current Azedarach Capabilities](#current-azedarach-capabilities)
4. [VC (VibeCoder) Patterns](#vc-vibecoder-patterns)
5. [Agent Mail](#agent-mail)
6. [Steve Yegge Insights](#steve-yegge-insights)
7. [Orchestration Approaches](#orchestration-approaches)
8. [Recommendations](#recommendations)

---

## Executive Summary

This document researches approaches for orchestrating epics (multi-task features) using beads issue tracking and azedarach TUI. The goal is to enable **autonomous parallel agent execution** where multiple Claude Code sessions work on independent tasks simultaneously, coordinated by either a human via TUI or an AI orchestrator.

### Key Findings

1. **Beads already has the primitives** - Epic/child relationships, dependency tracking, `bd ready` for unblocked work
2. **VC (steveyegge/vc)** demonstrates a working "AI Supervised Issue Workflow" using beads
3. **Agent Mail MCP** provides inter-agent coordination via shared inboxes and file leases
4. **Azedarach's Task tool** enables spawning background subagents with monitoring

---

## Current Beads Capabilities

### Core APIs (via `bd` CLI)

| Operation | Command | Use Case |
|-----------|---------|----------|
| Search issues | `bd search "keywords"` | Discovery (NOT `bd list`) |
| Find unblocked | `bd ready` | Get next workable tasks |
| Show details | `bd show <id>` | Get full issue context |
| Create issue | `bd create --title="..." --type=epic/task` | Spawn new work items |
| Update status | `bd update <id> --status=in_progress` | Track state |
| Add notes | `bd update <id> --notes="..."` | Progress tracking |
| Close issue | `bd close <id> --reason="..."` | Mark complete |
| Add dependency | `bd dep add <child> <parent> --type=parent-child` | Link epics to children |

### Agent Claiming

**Use `assignee` field** to track which agent is working on an issue:

```bash
bd update az-123 --status=in_progress --assignee="claude-session-abc"
```

This provides:
- Agent identity tracking (who's working on what)
- Resumability (if agent crashes, we know who had it)
- Conflict detection (`bd ready` can filter out already-assigned issues)

**Note**: Unlike VC's SQLite transactions, this isn't atomically exclusive - two agents could theoretically claim the same issue. In practice, the orchestrator controls spawning so this is fine.

### Dependency Types

```
parent-child    : Task is child of epic (epic blocked until children done)
blocks          : Issue A blocks issue B
discovered-from : Bug found while working on another issue
```

### Key Pattern: Epic → Children

```bash
# 1. Create epic
bd create --title="User Settings Feature" --type=epic

# 2. Create independent child tasks
bd create --title="Settings UI" --type=task
bd create --title="Settings API" --type=task
bd create --title="Settings DB" --type=task

# 3. Link children to epic
bd dep add az-ui az-epic --type=parent-child
bd dep add az-api az-epic --type=parent-child
bd dep add az-db az-epic --type=parent-child

# 4. `bd ready` now shows children (unblocked), NOT epic (blocked by children)
```

### MCP Tools Available

The `mcp__plugin_beads_beads__*` tools provide programmatic access to all beads operations. The Gleam service (`gleam/src/azedarach/services/beads.gleam`) wraps these with type safety.

---

## Current Azedarach Capabilities

### Orchestrator Template

`.claude/session-templates/orchestrator.md` defines the orchestrator pattern:

1. **Analyze task dependencies** - Group into parallel batches
2. **Spawn subagent batches** - Use Task tool with `run_in_background: true`
3. **Monitor progress** - Use TaskOutput with `block: false/true`
4. **Handle completion** - Verify, update beads, spawn next batch
5. **Complete epic** - Close when all children done

### Current Workflow

```
User selects epic in TUI
       ↓
Press Space+s to spawn orchestrator session
       ↓
Orchestrator (Claude) analyzes child tasks
       ↓
Spawns parallel subagents via Task tool
       ↓
Each subagent works in isolated git worktree
       ↓
Subagents update beads, commit, close tasks
       ↓
Orchestrator monitors, spawns next batch
       ↓
Epic closes when all children complete
```

### Key Patterns

- **Parallel safety**: Only spawn parallel if no shared file modifications
- **Discovery protocol**: Subagents create linked beads for found work
- **Error recovery**: Mark blocked, continue with other tasks
- **Git worktrees**: Full isolation per task/epic

---

## VC (VibeCoder) Patterns

[steveyegge/vc](https://github.com/steveyegge/vc) is Steve Yegge's AI-orchestrated coding agent colony. Key patterns:

### Architecture

```
VC Shell (REPL)
  ↓
AI Supervisor (Sonnet 4.5)
  ↓
Issue Workflow Executor (event loop)
  ↓
Worker Agents (Amp, Claude Code)
  ↓
Code Changes
```

### AI Supervised Issue Workflow Loop

1. **Claim Ready Issue** - Atomic SQL reservation
2. **AI Assessment** - Strategy, steps, risks analysis
3. **Agent Execution** - Worker implements changes
4. **AI Analysis** - Extract discovered work, identify bugs
5. **Auto-Discovery** - Create issues for punted/found work
6. **Quality Gates** - Tests, linting, build (90.9% pass rate)
7. **Resolution Decision** - Close, partial, or blocked

### Key Principle: Nondeterministic Idempotence

> "Workflows can be interrupted and resumed—AI figures out where it left off."

- Work state persists in issue database, not agent memory
- Supervisor re-evaluates current state on resume
- No deterministic checkpoints needed
- Supports concurrent execution via Git worktrees

### Zero Framework Cognition

> All decisions delegated to AI; no heuristics or regex parsing.

The supervisor (Claude Sonnet) makes ALL orchestration decisions:
- Which issue to work on next
- How to decompose work
- When to retry vs. mark blocked
- Quality assessment

### Agent Coordination Pattern

Agents don't communicate directly—they communicate through the issue tracker:

1. **Shared Issue Database** - Single source of truth
2. **Explicit Dependencies** - Issues reference blockers
3. **Async Execution** - Parallel via sandboxed worktrees
4. **Quality Gates** - Shared validation
5. **Supervisor Oversight** - AI manages priority/strategy

---

## Agent Mail

[MCP Agent Mail](https://mcpagentmail.com/) provides inter-agent coordination:

### Features

| Feature | Description |
|---------|-------------|
| **Identity Management** | Unique agent identities (adjective+noun) |
| **Messaging System** | Inbox/outbox with subject, body, CC, importance |
| **File Reservations** | Lock files to prevent conflicts |
| **Git-backed Persistence** | All comms recorded in Git |

### Use Cases

- Multiple agents on large refactoring
- Front-end/back-end agent coordination
- Critical migrations requiring file protection
- Technical discussions that need to be searched

### Integration Potential

Agent Mail could complement beads:
- **Beads**: Work items, dependencies, status
- **Agent Mail**: Real-time coordination, file locking, discussions

---

## Steve Yegge Insights

Key insights from Yegge's Medium articles:

### On Automation Vision

> "95 to 99% of interactions with coding agents could be handled by a properly briefed model."
> — [Introducing Beads](https://steve-yegge.medium.com/introducing-beads-a-coding-agent-memory-system-637d7d92514a)

### On Beads Success

- 5000+ stars, 250+ forks
- 100% vibe coded (130k LOC Go)
- Built in ~6 days
- Tens of thousands using it daily

### On Multi-Agent Future

From [O'Reilly podcast](https://www.oreilly.com/radar/podcast/generative-ai-in-the-real-world-vibe-coding-with-steve-yegge/):
> "The developer becomes a high-level orchestrator instead of writing code line by line."

### On VC Results

- 254 issues resolved through self-improvement
- 24+ successful missions
- 90.9% quality gate pass rate
- Uses Beads v0.12.0 SQLite tracker

---

## Orchestration Approaches

### Approach 1: Fan-Out via Prompt Injection

**Pattern**: User-triggered parallel spawning with orchestration context injected.

```
User presses Space+s on epic
       ↓
Azedarach creates orchestrator session
       ↓
System prompt includes:
  - Epic context
  - Child tasks and dependencies
  - Task tool instructions
  - Beads commands
       ↓
Claude autonomously spawns parallel subagents
       ↓
Monitors via TaskOutput, updates beads
```

**Current Status**: Already implemented in `orchestrator.md`

**Enhancements**:
- Auto-detect parallel-safe tasks (file analysis)
- Smarter batching based on estimated complexity
- Better progress reporting to TUI

### Approach 2: AI Orchestrator Session

**Pattern**: Dedicated AI session that uses `az` CLI for orchestration.

```
Orchestrator session (long-running)
       ↓
Uses az CLI: az spawn <task-id>
             az status <session-id>
             az attach <session-id>
             az kill <session-id>
       ↓
Monitors all sessions, makes decisions
       ↓
Reports to user via TUI or notifications
```

**Requirements**:
- `az` CLI for programmatic control
- Session state API (busy/waiting/done/error)
- Notification system for user alerts

**Advantages**:
- Single orchestrator has full context
- Can make complex decisions (retry, reorder, decompose)
- Continuous operation without user intervention

### Approach 3: VC-Style Event Loop

**Pattern**: Adopt VC's "AI Supervised Issue Workflow" loop.

```go
for {
    issue := ClaimReadyIssue()  // bd ready, atomic claim
    assessment := AISupervisor.Assess(issue)
    result := Agent.Execute(issue, assessment)
    analysis := AISupervisor.Analyze(result)

    for _, discovered := range analysis.DiscoveredWork {
        CreateIssue(discovered)
    }

    if analysis.QualityGatesPassed {
        CloseIssue(issue)
    } else {
        MarkBlocked(issue, analysis.Reason)
    }
}
```

**Key Differences from Current**:
- Continuous event loop (not user-triggered)
- AI assessment before execution
- AI analysis after execution
- Automatic discovery and creation of issues
- Quality gate enforcement

**Integration Path**:
1. Add `bd claim` for atomic issue claiming
2. Implement assessment phase (pre-execution AI review)
3. Implement analysis phase (post-execution AI review)
4. Add quality gate runner (tests, type-check, lint)

### Approach 4: Agent Mail Integration

**Pattern**: Use Agent Mail for inter-agent coordination.

```
Agent A (UI) ←→ Mailbox ←→ Agent B (API)
                  ↑
            File Leases
```

**Use Cases**:
- Agents need to share discovered information
- Prevent file conflicts during parallel work
- Long-running discussions about design decisions

**Integration**:
1. Add Agent Mail MCP server
2. Each subagent gets identity
3. Subagents can message each other
4. File reservations prevent conflicts

### Approach 5: Hybrid Multi-Layer

**Pattern**: Combine approaches based on scope.

```
┌─────────────────────────────────────────────┐
│          Human (via Azedarach TUI)          │
│  - Create epics, prioritize, approve PRs    │
└─────────────────────────────────────────────┘
                      ↓
┌─────────────────────────────────────────────┐
│         AI Orchestrator (long-running)      │
│  - VC-style event loop                      │
│  - Claims issues, spawns agents, monitors   │
│  - Uses az CLI for control                  │
└─────────────────────────────────────────────┘
                      ↓
┌─────────────────────────────────────────────┐
│         Worker Agents (parallel)            │
│  - Use Agent Mail for coordination          │
│  - Update beads with progress               │
│  - Isolated in git worktrees                │
└─────────────────────────────────────────────┘
```

**Layers**:
1. **Human layer**: High-level direction, approval
2. **Orchestrator layer**: AI supervision, event loop
3. **Worker layer**: Parallel execution, coordination

---

## Recommendations

### Short-Term (Quick Wins)

1. **Enhance orchestrator.md template**
   - Add file conflict detection
   - Improve progress reporting
   - Add retry logic

2. **Add `az` CLI commands**
   - `az sessions` - List active sessions
   - `az status <id>` - Get session state
   - `az spawn <task>` - Spawn for task
   - `az attach <id>` - Attach to session

3. **Improve TUI feedback**
   - Real-time session state updates
   - Progress bars for epics
   - Notification for completed tasks

### Medium-Term (VC-Style Features)

1. **AI Assessment Phase**
   - Pre-execution review by supervisor
   - Strategy and risk identification
   - Complexity estimation

2. **AI Analysis Phase**
   - Post-execution review
   - Discovery extraction
   - Quality determination

3. **Quality Gates**
   - Automated test running
   - Type-check enforcement
   - Lint checking
   - Build verification

4. **Agent Claiming via Assignee**
   - Use `assignee` field with session ID
   - Filter `bd ready` results by unassigned
   - Already supported in beads CLI

### Long-Term (Full Orchestration)

1. **Agent Mail Integration**
   - MCP server installation
   - Identity management
   - File lease system

2. **Continuous Orchestrator**
   - Long-running AI supervisor
   - Event loop processing
   - Automatic issue discovery

3. **Multi-Repo Support**
   - Cross-repo dependencies
   - Monorepo orchestration
   - Shared context

---

## Sources

- [steveyegge/vc GitHub Repository](https://github.com/steveyegge/vc)
- [MCP Agent Mail](https://mcpagentmail.com/)
- [Introducing Beads - Steve Yegge](https://steve-yegge.medium.com/introducing-beads-a-coding-agent-memory-system-637d7d92514a)
- [Six New Tips for Better Coding With Agents - Steve Yegge](https://steve-yegge.medium.com/six-new-tips-for-better-coding-with-agents-d4e9c86e42a9)
- [Beads Best Practices - Steve Yegge](https://steve-yegge.medium.com/beads-best-practices-2db636b9760c)
- [O'Reilly Podcast: Vibe Coding with Steve Yegge](https://www.oreilly.com/radar/podcast/generative-ai-in-the-real-world-vibe-coding-with-steve-yegge/)
