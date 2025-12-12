# Retrospective Command

Run a structured retrospective on recent work and save it to a session file.

**Workflow:**
1. This command analyzes recent work and writes a session file
2. Review the session file when ready
3. Run `/retro-review` to create beads tasks for selected issues
4. Run `/retro-patterns` periodically to find recurring patterns across sessions

## Your Task

Conduct a structured retrospective and save to `internal-docs/retrospectives/session-YYYY-MM-DD-HHMMSS.md`:

### 1. What Went Well

- List 3-5 things that worked effectively
- Note any particularly smooth workflows or good decisions
- Identify patterns worth repeating
- **Consider project-specific patterns:**
  - Did skills system help? Which skills were useful?
  - Did beads integration work smoothly?
  - Was documentation (CLAUDE.md, skills/) helpful?
  - Were modern CLI tools (rg, fd) effective?

**For each win, classify it:**
- **Validates existing pattern**: Pattern already documented and worked as designed
- **Needs codification**: Pattern not documented anywhere, worth sharing

### 2. What Could Be Better

- List 3-5 pain points, inefficiencies, or issues encountered
- Include both technical issues and process problems
- Note any confusion, repeated questions, or friction
- **Areas to check:**
  - Missing or unclear documentation in CLAUDE.md/skills files?
  - Skills that should exist but don't?
  - Beads workflow friction?
  - Type errors that could be prevented with better patterns?
  - Missing automation or tooling?
  - **Skills system health:**
    - Are skills > 500 lines? (Should use progressive disclosure with resources)
    - Are auto-loading triggers working properly?
    - Are confidence thresholds appropriate?

**CRITICAL: For each issue, check existing documentation:**

Search for related content in:
- `.claude/skills/` (skill files)
- `CLAUDE.md`

**If documentation EXISTS:**
- **Discovery failure** - Why wasn't it found/used?
  - Skills auto-loading didn't trigger
  - Documentation unclear or buried
  - Skill file too long (>500 lines)

**If documentation DOES NOT exist:**
- **Documentation gap** - Missing docs/skill/pattern
- Should it be a skill, resource doc, or CLAUDE.md update?

### 3. Suggested Action Items

For each issue, suggest appropriate beads task structure (don't create yet):

**Discovery failures:**
- Title: "Fix skill auto-loading for [pattern]"
- Type: `task`
- Labels: `skills-system`, `discovery`, `dx`
- Rationale: Why documentation exists but wasn't found

**Documentation gaps:**
- Title: "Document [pattern] in [location]"
- Type: `chore`
- Labels: `documentation`, `dx`
- Rationale: What to document, where to put it

**Process/tooling improvements:**
- Type: `task` or `chore` depending on scope
- Labels: `process-improvement`, `automation`, etc.

### 4. Summary

- Briefly summarize key insights
- Note ratio of discovery failures vs documentation gaps
- Highlight any skills system issues needing attention

## Output Instructions

1. **Write session file**: Create `internal-docs/retrospectives/session-YYYY-MM-DD-HHMMSS.md` with timestamp
2. **Include all analysis**: Full retrospective content in structured format
3. **Don't create beads**: Just document suggested tasks (run `/retro-review` later to create them)
4. **Remind user**: End with reminder to run `/retro-review` when ready

## Example Session File Format

```markdown
# Retrospective Session - 2025-12-11 14:30:22

**Reviewed period**: Last 2 days of work on TUI board component

## What Went Well

### 1. OpenTUI component composition
**Classification**: Validates existing pattern
**Details**: React patterns translated well to OpenTUI, component composition worked smoothly
**Impact**: Clean code, good separation of concerns

### 2. Beads CLI integration
**Classification**: Needs codification
**Details**: bd CLI commands work great in scripts, pattern worth documenting
**Impact**: Effective automation, but pattern not discoverable

## What Could Be Better

### Issue 1: State detection patterns unclear
**Documentation check**: NOT found in skills
**Root cause**: **Documentation gap**
**Analysis**:
- No skill for Claude output pattern detection
- Had to research ccmanager approach
- Common pattern for session monitoring

**Suggested action**: Document state detection patterns
- **Title**: "Create state-detection skill with pattern examples"
- **Type**: `task`
- **Priority**: 2
- **Labels**: `skills-system`, `documentation`
- **Content**: Cover output parsing, regex patterns, state machine

## Summary

**Insights**:
- 2 wins identified (1 validates existing, 1 needs codification)
- 2 issues found: 0 discovery failures vs 2 documentation gaps
- New project - many patterns not yet documented

**Suggested tasks**: 3 total

---

**Next step**: Run `/retro-review` to select which tasks to create in beads
```

## After Writing Session File

Show this message to the user:

```
Retrospective session saved to: internal-docs/retrospectives/session-[timestamp].md

Next steps:
- Review the session file when ready
- Run `/retro-review` to interactively create beads tasks
- Run `/retro-patterns` periodically to find recurring patterns
```
