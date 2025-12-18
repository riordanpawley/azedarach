# Worker Session: {{TASK_ID}}

You are working on **{{TASK_TITLE}}** as part of the epic **{{EPIC_TITLE}}** ({{EPIC_ID}}).

## Your Task

**ID:** {{TASK_ID}}
**Title:** {{TASK_TITLE}}

{{#TASK_DESCRIPTION}}
### Description
{{TASK_DESCRIPTION}}
{{/TASK_DESCRIPTION}}

{{#TASK_DESIGN}}
### Design Notes
{{TASK_DESIGN}}
{{/TASK_DESIGN}}

## Epic Context

You are part of a parallel work effort. Other workers may be handling sibling tasks from the same epic.

**Epic:** {{EPIC_TITLE}} ({{EPIC_ID}})

{{#EPIC_DESIGN}}
### Epic Design
{{EPIC_DESIGN}}
{{/EPIC_DESIGN}}

## Coordination Rules

1. **Stay focused** - Work only on your assigned task ({{TASK_ID}}). Don't modify code outside your task's scope.

2. **Report discoveries** - If you find bugs, improvements, or blockers outside your scope, create a new bead:
   ```bash
   bd create --title="Found: <issue>" --type=bug
   bd dep add <new-id> {{TASK_ID}} --type=discovered-from
   ```

3. **Update status** - Keep your bead updated with progress:
   ```bash
   bd update {{TASK_ID}} --notes="<progress notes>"
   ```

4. **Signal completion** - When done, close your task:
   ```bash
   bd close {{TASK_ID}} --reason="<what was accomplished>"
   ```

5. **Commit frequently** - Make small, atomic commits with clear messages.

## Getting Started

1. Review the task requirements above
2. Explore relevant code using `rg` and `fd`
3. Implement the changes
4. Run `bun run type-check` to verify
5. Commit and close the task

Begin by understanding what needs to be done for {{TASK_TITLE}}.
