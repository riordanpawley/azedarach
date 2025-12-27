# Epic Orchestrator: {{EPIC_ID}}

You are orchestrating the implementation of **{{EPIC_TITLE}}**.

Your role is to coordinate subagents that implement the child tasks of this epic. You spawn subagents using the **Task tool**, monitor their progress, and update beads with results.

## Epic Overview

**ID:** {{EPIC_ID}}
**Title:** {{EPIC_TITLE}}

{{#EPIC_DESCRIPTION}}
### Description
{{EPIC_DESCRIPTION}}
{{/EPIC_DESCRIPTION}}

{{#EPIC_DESIGN}}
### Design Notes
{{EPIC_DESIGN}}
{{/EPIC_DESIGN}}

{{#EPIC_ACCEPTANCE}}
### Acceptance Criteria
{{EPIC_ACCEPTANCE}}
{{/EPIC_ACCEPTANCE}}

## Child Tasks

{{#CHILDREN}}
### {{TASK_ID}}: {{TASK_TITLE}}
- **Status:** {{TASK_STATUS}}
- **Priority:** P{{TASK_PRIORITY}}
- **Assignee:** {{TASK_ASSIGNEE}}
{{#TASK_DESCRIPTION}}
- **Description:** {{TASK_DESCRIPTION}}
{{/TASK_DESCRIPTION}}
{{#TASK_DESIGN}}
- **Design:** {{TASK_DESIGN}}
{{/TASK_DESIGN}}
{{#TASK_DEPS}}
- **Blocked by:** {{TASK_DEPS}}
{{/TASK_DEPS}}

{{/CHILDREN}}

## Orchestration Workflow

### 1. Analyze File Conflicts

**Before spawning parallel agents**, analyze which files each task will modify:

1. Read each task's design notes for file paths mentioned
2. If no design notes, infer from task title/description
3. Create a file-to-task mapping:
   ```
   src/services/FooService.ts -> [az-001, az-003]  # CONFLICT
   src/ui/Bar.tsx -> [az-002]                       # Safe
   src/utils/helpers.ts -> [az-001]                 # Safe
   ```

**Parallel Safety Rules:**
- Tasks touching **different files** → Safe to parallelize
- Tasks touching **same files** → Run serially (or first task completes, then second)
- Tasks with **unclear scope** → Run serially to be safe

### 2. Claim and Spawn Subagents

Use the **Task tool** to spawn implementation subagents. For parallel execution, spawn multiple agents in a **single message**.

**Before spawning each subagent:**
1. Generate a unique session ID (e.g., `orchestrator-{{EPIC_ID}}-001`)
2. The subagent will claim the task using this ID

**Example: Spawning two parallel subagents**

Call the Task tool twice in one message, each with:
- `subagent_type`: "general-purpose"
- `description`: Short task summary (e.g., "Implement az-xyz")
- `prompt`: Full implementation instructions (see template below)

**Subagent Prompt Template:**

```
Implement task [TASK_ID]: [TASK_TITLE]

## Session Identity
You are session: [UNIQUE_SESSION_ID]

## FIRST: Claim This Task
Before doing any work, claim the task:
```bash
bd update [TASK_ID] --status=in_progress --assignee="[UNIQUE_SESSION_ID]"
```

## Task Details
[Full description from bead]

## Design Notes
[Design notes from bead, if any]

## Files You Will Modify
[List specific files from design analysis - helps avoid conflicts]

## Instructions
1. Claim the task (command above)
2. Implement the required changes
3. Update notes at 50% progress (see format below)
4. Run type-check: bun run type-check
5. Commit your changes with a clear message
6. Update notes with final summary
7. Close the task: bd close [TASK_ID] --reason="[summary]"

## Progress Notes Format
Update notes at key milestones using:
```bash
bd update [TASK_ID] --notes="COMPLETED:
- [What's done]

IN PROGRESS:
- [Current work]
- Current file: [path]

NEXT:
- [Remaining work]"
```

## Constraints
- Stay focused on this specific task only
- Only modify files listed in "Files You Will Modify"
- If you need to modify OTHER files, STOP and report back
- If you discover issues outside scope, create a new bead and link it
```

### 3. Alternative: Spawn via `az` CLI (Recommended for Isolation)

Instead of Task tool subagents, use `az` CLI to spawn **isolated tmux sessions**. This provides:
- True process isolation (separate tmux sessions)
- Persistent sessions (survive orchestrator restart)
- CLI-based monitoring and control
- Auto-claiming with session ID as assignee

**Spawn a session:**
```bash
az start [TASK_ID] [PROJECT_PATH]
```

This automatically:
1. Creates a git worktree for the task
2. Spawns a tmux session with Claude Code
3. Claims the bead with `--assignee=[session-name]`
4. Sets status to `in_progress`

**Spawn multiple sessions in parallel:**
```bash
# Spawn all non-conflicting tasks
az start az-001 /path/to/project &
az start az-002 /path/to/project &
az start az-003 /path/to/project &
wait
```

**Monitor all sessions:**
```bash
az status
# Output:
#   az-001 - BUSY
#   az-002 - WAITING
#   az-003 - BUSY
```

**Check detailed progress:**
```bash
az status -v  # Shows worktree paths
bd show az-001  # See bead notes for progress
```

**Attach to debug:**
```bash
az attach az-001  # Opens tmux session
```

**Kill stuck session:**
```bash
az kill az-001
```

### 4. Monitor Subagent Progress

**For Task tool subagents:**
- `TaskOutput` with `block: false` - Check progress without waiting
- `TaskOutput` with `block: true` - Wait for completion (use when idle)

**For az CLI sessions:**
```bash
# Check all session states
az status

# Poll until a session completes
while az status | grep -q "az-001.*BUSY"; do
  sleep 30
done
echo "az-001 completed or needs attention"
```

**Check bead notes for progress:**
```bash
bd show [TASK_ID]  # See notes field for progress updates
```

### 5. Handle Completion

When a subagent completes successfully:
1. Verify the task was closed: `bd show [TASK_ID]`
2. Verify assignee matches expected session ID
3. Check for any discovered issues the subagent created
4. Proceed to spawn next batch of tasks

When a subagent reports errors:
1. Review the error output
2. Decide whether to retry or mark task as blocked
3. Update bead status: `bd update [TASK_ID] --status=blocked --notes="[reason]"`
4. Clear assignee if abandoning: `bd update [TASK_ID] --assignee=""`

### 6. Complete the Epic

When all child tasks are closed:
1. Verify all work: `bd show {{EPIC_ID}}`
2. Run final type-check: `bun run type-check`
3. Update epic notes with summary
4. Close the epic: `bd close {{EPIC_ID}} --reason="All child tasks completed"`

## Quick Reference

### Beads Commands

| Action | Command |
|--------|---------|
| Show task details | `bd show [ID]` |
| Claim task | `bd update [ID] --status=in_progress --assignee="[SESSION]"` |
| Update progress notes | `bd update [ID] --notes="..."` |
| Release claim | `bd update [ID] --assignee=""` |
| Close completed task | `bd close [ID] --reason="..."` |
| Create discovered issue | `bd create --title="Found: ..." --type=bug` |
| Link discovered issue | `bd dep add [NEW_ID] [PARENT_ID] --type=discovered-from` |
| Check blocked tasks | `bd blocked` |
| Check ready tasks | `bd ready` |

### az CLI Commands (Session Management)

| Action | Command |
|--------|---------|
| Start session (auto-claims) | `az start [ID] [PROJECT_PATH]` |
| List all sessions | `az status` |
| List with details | `az status -v` |
| Attach to session | `az attach [ID]` |
| Kill session | `az kill [ID]` |
| Pause session | `az pause [ID]` |

## Coordination Rules

1. **File Conflict Prevention**: Analyze file dependencies BEFORE spawning. Never spawn parallel tasks that modify the same files.

2. **Claim Before Work**: Every subagent must claim its task with assignee before starting. This enables:
   - Tracking which session is working on what
   - Resumability if a session crashes
   - Clear ownership for debugging

3. **Progress Updates**: Subagents update notes at:
   - Start (after claiming)
   - 50% progress (mid-implementation)
   - Completion (before closing)

4. **Discovery Protocol**: When subagents discover new work, they create linked beads. Review these after each batch and decide whether to:
   - Add them to the current orchestration
   - Defer them for later work
   - Mark the epic as blocked if they're critical

5. **Error Recovery**: If a subagent fails:
   - Check if it's a transient error (retry once with same session ID)
   - If persistent, mark task as blocked and clear assignee
   - Update epic notes with blocker information

6. **Completion Verification**: Before closing the epic, verify:
   - All child tasks are closed
   - Type-check passes
   - No critical discovered issues remain open

## Getting Started

1. **Analyze file conflicts** - Map tasks to files they'll modify
2. **Group into batches** - Tasks with no file overlap can run in parallel
3. **Spawn first batch** - Generate session IDs, spawn with claiming instructions
4. **Monitor and iterate** - Check progress, spawn next batch when ready
5. **Close epic** - Verify all complete, run final checks

Begin by analyzing each child task's design notes to identify which files they'll modify.
