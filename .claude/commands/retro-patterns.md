# Retro Patterns Command

Analyze retrospective sessions to find recurring patterns and archive old sessions.

## Purpose

This command helps you:
1. Identify issues that appear repeatedly across sessions
2. Create strategic improvements for systemic problems
3. Archive old sessions to keep the retrospectives directory clean
4. Track patterns over time for process improvement

## When to Run

- **Monthly**: Check for recurring patterns
- **Quarterly**: Archive old sessions
- **On demand**: When you notice the same issues appearing multiple times
- **Before major planning**: Understand systemic improvements needed

## Workflow

### 1. Gather Session Files

Find all session files in `internal-docs/retrospectives/`:

```bash
# All sessions (default)
fd -t f "session-.*\.md" internal-docs/retrospectives/

# Specific date range (if user specified)
fd -t f "session-.*\.md" internal-docs/retrospectives/ --changed-after YYYY-MM-DD
```

### 2. Extract Issues from All Sessions

For each session file, parse and extract:
- **Issue title/description**
- **Root cause** (discovery failure or documentation gap)
- **Category** (skills system, documentation, tooling, process, etc.)
- **Date** (from session filename)
- **Status** (was bead created? check for REVIEWED marker with bead IDs)

Build a comprehensive list of all issues across all sessions.

### 3. Group Similar Issues

Use analysis to group similar issues together:

**Similarity criteria:**
- Same root cause type (discovery vs gap)
- Same category (skills, docs, tooling)
- Similar symptoms or manifestations
- Related to same system/layer

**Example grouping:**
```
Pattern Group 1: "State management confusion"
  - Session 2025-12-10: "Unclear state detection patterns"
  - Session 2025-12-05: "Output parsing approach unknown"
  Count: 2 occurrences
  Category: Documentation gap
```

### 4. Identify Recurring Patterns

Filter groups to find patterns with:
- **3+ occurrences** across different sessions
- **Within date range** (default: last 3 months)
- **Not all resolved** (at least some without beads created)

For each recurring pattern, generate:
- **Pattern name**: Concise description
- **Frequency**: Number of times it appeared
- **Date range**: First and last occurrence
- **Root cause**: Common root cause across occurrences
- **Impact**: Which areas affected

### 5. Analyze Pattern Types

Categorize recurring patterns:

**Discovery Failures** (docs exist but not found):
- Skills auto-loading issues
- Documentation buried/unclear
- Skills system configuration problems
- Suggest: Skills improvements, better triggers, clearer docs

**Documentation Gaps** (docs missing):
- Missing skills
- Missing resource docs
- Patterns not codified
- Suggest: Create new skills, add resources, document patterns

**Process/Tooling Issues**:
- Workflow friction
- Missing automation
- Tool configuration problems
- Suggest: Process improvements, new automation

### 6. Generate Recommendations

For each recurring pattern, recommend:

**High-impact patterns (5+ occurrences):**
```
CRITICAL: [Pattern Name]
  Frequency: 7 times over 2 months
  Category: [Category]
  Root cause: [Discovery failure / Documentation gap]

  Impact:
  - Repeated confusion on [topic]
  - Time wasted per occurrence

  Recommended action:
  - Title: "Improve [specific area] to address [pattern]"
  - Type: task
  - Priority: 1
  - Labels: process-improvement, [category]
```

**Medium-impact patterns (3-4 occurrences):**
```
MODERATE: [Pattern Name]
  [Similar format, Priority: 2]
```

### 7. Present Findings

Show analysis to user:

```markdown
# Pattern Analysis - [Date Range]

Analyzed: 15 sessions from 2025-09-01 to 2025-12-11

## Summary

- Total issues: 42
- Recurring patterns: 6 (3+ occurrences)
- Critical patterns: 2 (5+ occurrences)
- Already addressed: 18 issues (beads created)
- Unaddressed: 24 issues

## Recurring Patterns

### CRITICAL: Skills Auto-Loading Failures (7 occurrences)

**Sessions**: 2025-12-10, 2025-12-05, ...

**Root cause**: Discovery failure

**Manifestations**:
- Pattern docs not loading (3x)
- Resource docs missed (2x)

**Impact**: ~40-60 minutes wasted per occurrence

**Recommended action**:
Title: "Audit and improve skills auto-loading triggers"
Type: task, Priority: 1

[Create bead?] Yes / No
```

### 8. Create Selected Tasks

Use AskUserQuestion with multiSelect to let user choose which pattern tasks to create.

For each selected pattern, create a comprehensive bead:

```bash
bd create --title="Address recurring pattern: [Pattern Name]" \
  --type=task \
  --priority=1 \
  --description="
# Recurring Pattern Analysis

**Pattern**: [patternName]
**Frequency**: [occurrences] times over [dateRange]
**Category**: [category]
**Root cause**: [rootCause]

## Occurrences

- 2025-12-10: [description]
- 2025-12-05: [description]

## Impact

[impactAnalysis]

## Recommended Solution

[proposedSolution]

## Success Criteria

- Pattern no longer appears in retrospectives
"
```

### 9. Archive Old Sessions

If sessions are older than 3 months (or user-specified threshold):

1. Create archive directory if needed:
```bash
mkdir -p internal-docs/retrospectives/archive/YYYY-QN
```

2. Generate patterns summary for the quarter

3. Move session files to archive:
```bash
mv internal-docs/retrospectives/session-2025-09-*.md internal-docs/retrospectives/archive/2025-Q3/
```

4. Report archive results

## Command Options

User can specify:
- **Date range**: Analyze specific period
- **Min occurrences**: Threshold for "recurring" (default: 3)
- **Archive age**: How old before archiving (default: 3 months)
- **No archive**: Skip archiving step

Examples:
```
/retro-patterns
/retro-patterns --since 2025-09-01
/retro-patterns --min-occurrences 5
/retro-patterns --no-archive
```

## Output Summary

Final summary to user:

```
Pattern analysis complete

Analyzed: 15 sessions (2025-09-01 to 2025-12-11)
Recurring patterns: 6 found
Critical patterns: 2 (5+ occurrences)

Created beads:
- AZ-xxx1: Audit and improve skills auto-loading triggers (P1)
- AZ-yyy2: Document state detection patterns (P2)

Archived: 24 old sessions → archive/2025-Q3/

Next steps:
- Work on recurring pattern tasks: bd search "recurring-pattern"
- Continue running retrospectives: /retrospective
- Review patterns again in 1 month
```

## Benefits

1. **Identifies systemic issues**: Patterns that need strategic fixes
2. **Prevents recurring problems**: Create lasting improvements
3. **Tracks trends**: See if issues are getting better or worse
4. **Keeps repo clean**: Archives old sessions while preserving insights
5. **Data-driven improvement**: Objective view of what needs attention

## Notes

- Run monthly to catch patterns early
- Archive quarterly to keep directory manageable
- Critical patterns (5+) deserve immediate attention
- Track pattern resolution: Did the fix work? Did pattern stop appearing?
