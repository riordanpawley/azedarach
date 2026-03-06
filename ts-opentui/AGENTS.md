# Agent Instructions

First command in a new AI session: **`az prime`**.
Use **`az issue`** for issue tracking commands.
`az prime` is the source of AI workflow guidance for issue tracking.
Track any task that takes more than one command in the issue tracker.
Keep one active parent issue per session whenever possible.
When spawning subagents, each subagent must create, maintain, and close a child issue linked to the active parent issue.
When already in the target worktree/repo, use plain `git` commands (avoid defensive `git -C <same-path>`).

## Landing the Plane (Session Completion)

**When ending a work session**, you MUST complete ALL steps below.

**MANDATORY WORKFLOW:**

1. **File issues for remaining work** - Create issues for anything that needs follow-up
2. **Run quality gates** (if code changed) - Tests, linters, builds
3. **Update issue status** - Close finished work, update in-progress items
4. **Finalize locally**:
   ```bash
   git status
   ```
   Do not run remote sync/cleanup commands (for example pull/rebase, push, remote prune) unless explicitly requested.
5. **Clean up** - Clear stashes and local temporary state
6. **Verify** - All changes committed locally
7. **Hand off** - Provide context for next session

**CRITICAL RULES:**
- When already in the target worktree/repo, use plain `git` commands. Use `git -C <path>` only when intentionally targeting a different path.
- Avoid remote git flows unless explicitly requested.
- Never auto-run pull/rebase/push as part of completion.
