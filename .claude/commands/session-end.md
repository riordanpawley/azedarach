---
description: End session with beads update verification and retrospective analysis
argument-hint: Optional - focus area for retrospective
---

# Session End Command

**Purpose:** Replace `/compact` with a comprehensive session-end workflow that preserves context and learns from the session.

**What this does:**
1. Verify beads are updated with session progress
2. Run retrospective analysis
3. Suggest next steps for resumption

---

## Your Task

Execute the session-end workflow in this order:

### Phase 0: Git Status Check (CRITICAL)

**NEVER leave uncommitted changes.** Before anything else:

```bash
git status
```

**If there are uncommitted changes:**

1. **Review what changed:**
   ```bash
   git diff --stat
   ```

2. **Stage and commit all work:**
   ```bash
   git add -A
   git commit -m "wip: session checkpoint

   🤖 Generated with [Claude Code](https://claude.com/claude-code)

   Co-Authored-By: Claude Opus 4.5 <noreply@anthropic.com>"
   ```

   Or if work is complete, use a proper commit message describing the changes.

3. **Verify clean state:**
   ```bash
   git status  # Should show "nothing to commit"
   ```

**Only proceed to Phase 1 after git is clean.**

---

### Phase 1: Beads Update Verification

#### 1.1 Check Current Beads Status

Query in-progress beads:

```bash
bd search "" --status=in_progress
```

#### 1.2 Analyze Conversation for Work Done

Review the CURRENT conversation (not transcript) to identify:
- What work was completed?
- What issues were discussed?
- What decisions were made?
- What blockers were encountered?
- What's the natural next step?

#### 1.3 Verify Beads Capture All Work

**For EACH piece of work identified:**

**Case A: Work matches existing in-progress bead**
- Verify bead has recent notes (check with `bd show <id>`)
- If notes missing or stale, UPDATE bead with missing context

**Case B: Work NOT tracked in any bead**
- Create new bead for untracked work
- Include comprehensive notes about what was done

**Case C: Work completed but bead still open**
- Close bead with completion summary
- Verify acceptance criteria met

#### 1.4 Proactive Note Updates

**DO NOT ask user for permission to update notes** - just do it proactively:

For each in-progress bead, check if notes are comprehensive:

```bash
bd update AZ-123 --notes="
Session progress ($(date -Iseconds)):

COMPLETED:
- [Specific deliverables completed this session]
- [Files modified and why]

IN PROGRESS:
- [Current state - what's partially done]
- [Exact next step to resume work]

BLOCKERS:
- [What prevents progress, if any]

KEY DECISIONS:
- [Important context for future sessions]
- [Rationale for approach chosen]

FILES MODIFIED:
- path/to/file1.ts - [why it was changed]
- path/to/file2.ts - [why it was changed]
"
```

**Note structure rules:**
- Write for future agent with ZERO context
- Be specific (exact file names, line numbers if relevant)
- Explain WHY, not just WHAT
- Include next step explicitly

#### 1.5 Report Verification Results

Show user what beads were updated/created:

```
Beads Update Verification Complete

Updated beads:
- AZ-abc: [title] - Added session notes
- AZ-def: [title] - Updated with blocker information

Created beads:
- AZ-xyz: [title] - New work discovered during session

Closed beads:
- AZ-123: [title] - Work completed

All work tracked
```

---

### Phase 2: Retrospective Analysis

**After beads are verified**, run the retrospective:

Execute the `/retrospective` command by invoking it directly.

**Include context parameter if provided:**
- User ran `/session-end fix-types` → Pass "fix-types" to retrospective as focus area
- User ran `/session-end` → Run retrospective without focus

**Note:** The retrospective command will create `internal-docs/retrospectives/session-YYYY-MM-DD-HHMMSS.md` with full analysis.

---

### Phase 3: Next Steps Summary

After retrospective completes, show user:

```
================================================================================
Session End Complete
================================================================================

Git Status:
   - Committed: [commit hash] [message summary]
   - Working tree: clean ✓

Beads Status:
   - Updated: 2 issue(s) with session notes
   - Created: 1 new issue(s)
   - Closed: 0 issue(s)

Retrospective:
   - Saved to: internal-docs/retrospectives/session-YYYY-MM-DD-HHMMSS.md
   - Wins documented: 3
   - Issues found: 2

To Resume Work:
   1. Check ready work: bd ready
   2. Review retrospective: /retro-review
   3. Continue on: [most likely next bead based on notes]

Tip: Use /retro-review to create beads from retrospective findings
================================================================================
```

---

## Command Arguments

**Format:** `/session-end [focus-area]`

**Examples:**
```
/session-end                          # Standard session end
/session-end tui-components           # Retrospective focuses on TUI work
/session-end session-manager          # Retrospective focuses on session manager
```

**Focus area** is passed to retrospective for targeted analysis.

---

## Why This Replaces /compact

**Problems with /compact:**
- Loses conversation context immediately
- No verification that work was tracked
- No learning from session patterns
- No guidance for resumption

**Benefits of /session-end:**
- Proactive beads verification (not just warnings)
- Creates resumable context via beads notes
- Learns from session via retrospective
- Identifies gaps for future improvement
- Provides clear resumption path
- Safe to compact after this runs

---

## Related Commands

- `/retrospective` - This command invokes it (Phase 2)
- `/retro-review` - Use after session-end to create tasks from findings
- `/retro-patterns` - Use periodically to find recurring patterns

---

## Related Skills

- `.claude/skills/workflow/beads-tracking.skill.md` - Beads workflow patterns

---

**Remember:** This command is PROACTIVE, not reactive. Don't wait for user permission to update beads - analyze the conversation and ensure everything is tracked with comprehensive notes for future resumability.
