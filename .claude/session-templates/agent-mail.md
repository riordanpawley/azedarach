# Agent Mail Integration Guide

This guide covers using **Agent Mail MCP** for inter-agent coordination in azedarach orchestration. Agent Mail provides messaging and file reservations between parallel Claude Code sessions.

## When to Use Agent Mail

**Use Agent Mail when:**
- Multiple agents need to coordinate on shared concerns
- File conflict avoidance alone isn't sufficient
- Agents need to share discovered information in real-time
- You need advisory file locks (not just avoidance)
- Long-running discussions about design decisions need to be searchable

**Don't use Agent Mail when:**
- Simple file conflict avoidance is enough (use orchestrator file analysis)
- Tasks are truly independent with no shared context
- Single-agent work

## Setup

### Installation

```bash
curl -fsSL "https://raw.githubusercontent.com/Dicklesworthstone/mcp_agent_mail/main/scripts/install.sh" | bash -s -- --yes
```

### MCP Configuration

Add to your Claude Code MCP settings:

```json
{
  "mcpServers": {
    "agent-mail": {
      "command": "uvx",
      "args": ["mcp-agent-mail"]
    }
  }
}
```

## Agent Identity

Each Claude session gets a unique identity for coordination.

### Registration Pattern

At session start, register with a meaningful name:

```
Use MCP tool: register_agent
- project_key: "{{PROJECT_PATH}}"
- agent_name: "{{TASK_ID}}-worker"  # e.g., "az-001-worker"
- capabilities: ["code", "test"]     # What this agent can do
```

**Naming convention:**
- Worker agents: `{task-id}-worker` (e.g., `az-001-worker`)
- Orchestrator: `orchestrator-{epic-id}` (e.g., `orchestrator-az-epic`)
- Supervisor: `supervisor-{timestamp}` (e.g., `supervisor-20251227`)

### Check Your Identity

```
Use MCP tool: whoami
- project_key: "{{PROJECT_PATH}}"
```

## File Reservations (Advisory Locks)

Prevent file conflicts by reserving files before modification.

### Acquire Reservation

Before modifying files, acquire a reservation:

```
Use MCP tool: file_reservation_paths
- project_key: "{{PROJECT_PATH}}"
- agent_name: "{{AGENT_NAME}}"
- paths: ["src/services/UserService.ts", "src/ui/UserForm.tsx"]
- ttl_seconds: 3600        # 1 hour lease
- exclusive: true          # No one else can reserve these
- reason: "{{TASK_ID}}"    # Link to bead for tracking
```

**Glob patterns supported:**
- `src/services/**/*.ts` - All TypeScript in services
- `src/ui/components/*.tsx` - Direct children only
- `**/*.test.ts` - All test files

### Check Existing Reservations

Before starting work, check what's reserved:

```
Use MCP tool: get_file_reservations
- project_key: "{{PROJECT_PATH}}"
```

**If files are reserved by another agent:**
1. Check the `reason` field for the task ID
2. Message that agent to coordinate
3. Wait for release, or work on non-conflicting files

### Release Reservations

After completing work, release your reservations:

```
Use MCP tool: release_file_reservations
- project_key: "{{PROJECT_PATH}}"
- agent_name: "{{AGENT_NAME}}"
- paths: ["src/services/UserService.ts"]  # Or omit for all
```

**Always release reservations:**
- When task is complete
- When switching to different files
- Before session ends

## Messaging

Agents communicate through a shared inbox system.

### Send Message

```
Use MCP tool: send_message
- project_key: "{{PROJECT_PATH}}"
- sender: "{{AGENT_NAME}}"
- recipients: ["az-002-worker"]        # Or ["orchestrator-az-epic"]
- subject: "API Contract Question"
- body: "What format should the user settings endpoint return?"
- importance: "normal"                  # or "high", "low"
- cc: ["supervisor-20251227"]          # Optional
```

**Message types:**
| Subject Pattern | Purpose |
|-----------------|---------|
| `[DISCOVERY] ...` | Found new work, bug, or issue |
| `[BLOCKED] ...` | Need help, can't proceed |
| `[QUESTION] ...` | Need clarification |
| `[INFO] ...` | FYI, no response needed |
| `[HANDOFF] ...` | Passing work to another agent |

### Check Inbox

```
Use MCP tool: fetch_inbox
- project_key: "{{PROJECT_PATH}}"
- agent_name: "{{AGENT_NAME}}"
- unread_only: true
```

**Check inbox:**
- At session start
- Before major decisions
- When waiting for dependencies
- Periodically during long tasks

### Acknowledge Messages

After reading/handling a message:

```
Use MCP tool: acknowledge_message
- project_key: "{{PROJECT_PATH}}"
- agent_name: "{{AGENT_NAME}}"
- message_id: "msg-uuid-here"
```

### Search Messages

Find past discussions:

```
Use MCP tool: search_messages
- project_key: "{{PROJECT_PATH}}"
- query: "user settings endpoint"
- limit: 10
```

## Integration Patterns

### Pattern 1: Worker with File Leases

For a worker agent implementing a task:

```markdown
## Session Start

1. Register identity:
   - register_agent(project_key, "{{TASK_ID}}-worker", ["code"])

2. Check inbox for instructions:
   - fetch_inbox(project_key, agent_name, unread_only=true)

3. Acquire file reservations:
   - file_reservation_paths(project_key, agent_name, [files], 3600, true, "{{TASK_ID}}")

4. Claim bead:
   - bd update {{TASK_ID}} --status=in_progress --assignee="{{TASK_ID}}-worker"

## During Work

5. If blocked, message orchestrator:
   - send_message(project_key, agent_name, ["orchestrator"], "[BLOCKED] Need X")

6. If discover new work:
   - Create bead: bd create --title="Found: ..." --type=bug
   - Link it: bd dep add NEW_ID {{TASK_ID}} --type=discovered-from
   - Notify: send_message(project_key, agent_name, ["orchestrator"], "[DISCOVERY] ...")

## Session End

7. Release file reservations:
   - release_file_reservations(project_key, agent_name)

8. Close bead:
   - bd close {{TASK_ID}} --reason="..."
```

### Pattern 2: Orchestrator Coordination

For an orchestrator managing multiple workers:

```markdown
## Monitor Workers

1. Check for messages from workers:
   - fetch_inbox(project_key, "orchestrator-{{EPIC_ID}}", unread_only=true)

2. Handle [BLOCKED] messages:
   - Analyze blocker
   - Either provide guidance via reply, or reassign task

3. Handle [DISCOVERY] messages:
   - Review discovered work
   - Decide: add to current epic, defer, or ignore

4. Check file reservation conflicts:
   - get_file_reservations(project_key)
   - If conflicts detected, message affected agents

## Spawn Decisions

5. Before spawning new worker:
   - Check existing reservations for target files
   - If conflict, wait or choose different task
```

### Pattern 3: Handoff Between Agents

When one agent needs another to take over:

```markdown
## Agent A (Handing Off)

1. Complete current phase
2. Update bead notes with state
3. Release file reservations
4. Send handoff message:
   - send_message(project_key, "agent-a", ["agent-b"],
     "[HANDOFF] API complete, ready for UI integration",
     "Endpoints: GET /users, POST /users. Schema in src/types/user.ts")

## Agent B (Receiving)

1. Check inbox, find handoff message
2. Acknowledge message
3. Acquire file reservations for UI files
4. Continue work from handoff point
```

## Supervisor Loop with Agent Mail

Enhanced supervisor loop integrating Agent Mail:

```
┌─────────────────────────────────────────────────────────────┐
│              SUPERVISOR LOOP (with Agent Mail)              │
├─────────────────────────────────────────────────────────────┤
│  1. DISCOVER    → bd ready + check inbox for discoveries    │
│  2. ASSESS      → Check file reservations for conflicts     │
│  3. SPAWN       → Worker registers + acquires leases        │
│  4. MONITOR     → az status + fetch_inbox for messages      │
│  5. ANALYZE     → Review worker messages + discoveries      │
│  6. GATE        → Quality checks                            │
│  7. RESOLVE     → Release leases + close task               │
│  8. ITERATE     → Acknowledge messages + continue           │
└─────────────────────────────────────────────────────────────┘
```

### Phase 2 Enhancement: ASSESS with Reservations

Before spawning, check for file conflicts via reservations:

```bash
# In supervisor, before spawning az-002:
# 1. Get files az-002 will modify (from task design)
# 2. Check if any are reserved:
#    - get_file_reservations(project_key)
# 3. If reserved by az-001, either:
#    - Wait for az-001 to complete and release
#    - Message az-001 to coordinate
#    - Choose different task
```

### Phase 4 Enhancement: MONITOR with Inbox

While monitoring, also check inbox:

```bash
# In supervisor monitoring loop:
while sessions_active; do
  az status                              # Check session states
  fetch_inbox(project_key, supervisor)   # Check for worker messages

  # Handle messages
  for message in unread_messages; do
    case message.subject:
      "[BLOCKED]*" -> handle_blocked(message)
      "[DISCOVERY]*" -> handle_discovery(message)
      "[QUESTION]*" -> handle_question(message)
    acknowledge_message(message.id)
  done

  sleep 30
done
```

## MCP Tools Reference

| Tool | Purpose |
|------|---------|
| `ensure_project` | Initialize project in Agent Mail |
| `register_agent` | Create agent identity |
| `whoami` | Check current identity |
| `send_message` | Send message to other agents |
| `fetch_inbox` | Get messages for an agent |
| `acknowledge_message` | Mark message as read |
| `search_messages` | Search message history |
| `file_reservation_paths` | Acquire file leases |
| `get_file_reservations` | Check existing leases |
| `release_file_reservations` | Release file leases |
| `list_agents` | See all registered agents |

## Best Practices

1. **Always register first**: Before any other Agent Mail operations
2. **Reserve before modify**: Acquire leases before editing files
3. **Release promptly**: Don't hold leases longer than needed
4. **Check inbox regularly**: Especially when waiting or blocked
5. **Use meaningful subjects**: Prefix with [TYPE] for easy filtering
6. **Link to beads**: Use task ID in reservation reasons
7. **Acknowledge messages**: Keep inbox clean for real-time coordination

## Troubleshooting

### "Agent not registered"
- Run `register_agent` before other operations
- Check `project_key` matches exactly

### "File already reserved"
- Run `get_file_reservations` to see who has it
- Message that agent to coordinate
- Wait for TTL to expire (check `expires_at`)

### "Message not delivered"
- Check recipient agent name is correct
- Verify recipient is registered (`list_agents`)
- Check project_key matches

### Messages not appearing
- Ensure you're checking the right agent name
- Try `unread_only: false` to see all messages
- Check `search_messages` for delivery confirmation
