# Retro Review Command

Review retrospective session files and create beads tasks for selected issues.

## Workflow

This command helps you triage retrospective findings and create actionable tasks:

1. Find recent session files (default: last 7 days)
2. Select a session to review
3. Review suggested action items from the session
4. Interactively select which issues to track as beads
5. Create beads tasks with full context from session
6. Mark session as reviewed

## Your Task

### 1. Find Session Files

Look for session files in `internal-docs/retrospectives/`:

```bash
# Find sessions from last 7 days
fd -t f "session-.*\.md" internal-docs/retrospectives/ --changed-within 7d
```

If user specified a date range or specific session, use that instead.

### 2. List Available Sessions

Show sessions with metadata:
- Filename (with timestamp)
- Date/time
- Reviewed status (check for `<!-- REVIEWED: YYYY-MM-DD -->` marker in file)
- Brief summary (first few lines after title)

Example output:
```
Available retrospective sessions (last 7 days):

1. session-2025-12-11-143022.md (Today 14:30)
   Not reviewed
   Summary: TUI board component work, 2 issues found

2. session-2025-12-10-091205.md (Yesterday 09:12)
   Reviewed on 2025-12-11
   Summary: Session manager implementation, 3 issues found
```

### 3. Ask User to Select Session

Use AskUserQuestion to let user pick which session to review:
- If only 1 unreviewed session: confirm that one
- If multiple: show list with options
- Allow "Review all unreviewed" option

### 4. Parse Session File

Read the selected session file and extract:
- **Date**: From filename or header
- **Period reviewed**: From "Reviewed period" section
- **Wins**: From "What Went Well" section (for context)
- **Issues**: From "What Could Be Better" section
- **Suggested actions**: Parse the task structure from each issue

For each issue, extract:
- **Title** (from issue heading)
- **Root cause** (discovery failure or documentation gap)
- **Suggested task title**
- **Type** (task, chore, etc.)
- **Priority** (1-5)
- **Labels** (tags)
- **Rationale/Description**

### 5. Present Issues for Selection

Show each issue with summary:

```
Issues from session-2025-12-11-143022.md:

Issue 1: State detection patterns unclear
  Root cause: Documentation gap
  Suggested task: "Create state-detection skill with pattern examples"
  Type: task | Priority: 2 | Labels: skills-system, documentation

  [Create bead?] Yes / No

Issue 2: tmux integration not documented
  Root cause: Documentation gap
  Suggested task: "Document tmux session management patterns"
  Type: chore | Priority: 2 | Labels: documentation

  [Create bead?] Yes / No
```

Use AskUserQuestion with multiSelect to let user choose which issues to create beads for.

### 6. Create Selected Beads

For each selected issue, create a bead:

```bash
bd create --title="Extracted title from suggested action" \
  --type=task \
  --priority=2 \
  --description="
From retrospective session [sessionDate]:

**Root cause**: [rootCause]

**Issue**: [issueDescription]

**Action**: [actionDescription]

**Context**: [additionalContext]
"
```

Track created beads and report them to user.

### 7. Mark Session as Reviewed

Add review marker to session file:

```markdown
<!-- REVIEWED: YYYY-MM-DD -->
<!-- Created beads: AZ-XXX, AZ-YYY, AZ-ZZZ -->
```

Add at the very end of the session file.

### 8. Summary Report

Show final summary:

```
Review complete: session-2025-12-11-143022.md

Created beads:
- AZ-abc1: Create state-detection skill (task, P2)
- AZ-def2: Document tmux patterns (chore, P2)

Skipped: 1 issue

Session marked as reviewed.

Next steps:
- Review beads: bd search "" --status=open
- Work on tasks: bd ready
- Run /retro-patterns periodically to find recurring patterns
```

## Command Options

User can specify:
- **Session**: Specific session filename or date
- **Days**: Number of days to look back (default: 7)
- **All**: Review all unreviewed sessions in batch

Examples:
```
/retro-review
/retro-review session-2025-12-11-143022.md
/retro-review --days 14
/retro-review --all
```

## Notes

- Sessions already marked as reviewed should show status
- Allow re-reviewing sessions (in case new issues discovered)
- Preserve session file content - only append review marker
- Link back to session file in bead description for full context

## Error Handling

**No session files found:**
```
No retrospective sessions found in last 7 days.
Run /retrospective to create a session first.
```

**All sessions reviewed:**
```
All sessions in last 7 days have been reviewed.
Run /retrospective to create a new session, or use --days to expand range.
```
