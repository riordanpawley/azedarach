# Agent Instructions

This project uses **`linear-cli`** for issue tracking.
Set `issueTracker.linear.team` in `.azedarach.json` (or run `linear-cli setup`) to avoid passing `-t` on every create command.

## Quick Reference

```bash
linear-cli i list --output json --compact --all   # List issues
linear-cli i get <id> --output json --compact     # View issue details
linear-cli i start <id>                            # Claim/start work
linear-cli i update <id> --output json --compact ...  # Update details/status
linear-cli i close <id>                            # Complete work
```

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
