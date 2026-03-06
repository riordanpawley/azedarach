# Agent Instructions

First command in a new AI session: **`az prime`**.
Use **`az issue`** for issue tracking commands.
`az prime` is the source of AI workflow guidance for issue tracking.
Track any task that takes more than one command in the issue tracker.
Keep one active parent issue per session whenever possible.
When spawning subagents, each subagent must create, maintain, and close a child issue linked to the active parent issue.
When already in the target worktree/repo, use plain `git` commands (avoid defensive `git -C <same-path>`).

## Landing the Plane (Session Completion)

**When ending a work session**, you MUST complete ALL steps below. Work is NOT complete until `git push` succeeds.

**MANDATORY WORKFLOW:**

1. **File issues for remaining work** - Create issues for anything that needs follow-up
2. **Run quality gates** (if code changed) - Tests, linters, builds
3. **Update issue status** - Close finished work, update in-progress items
4. **PUSH TO REMOTE** - This is MANDATORY:
   ```bash
   git pull --rebase
   git push
   git status  # MUST show "up to date with origin"
   ```
5. **Clean up** - Clear stashes, prune remote branches
6. **Verify** - All changes committed AND pushed
7. **Hand off** - Provide context for next session

**CRITICAL RULES:**
- Work is NOT complete until `git push` succeeds
- NEVER stop before pushing - that leaves work stranded locally
- NEVER say "ready to push when you are" - YOU must push
- If push fails, resolve and retry until it succeeds
