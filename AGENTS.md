Please also reference the following rules as needed. The list below is provided in TOON format, and `@` stands for the project root directory.

rules[2]{path,description}:
  @go-bubbletea/AGENTS.md,go-bubbletea scoped context
  @ts-opentui/AGENTS.md,ts-opentui scoped context

# Additional Conventions Beyond the Built-in Functions

As this project's AI coding tool, you must follow the additional conventions below, in addition to the built-in functions.

<!--
File: CONTEXT.md
Version: 2.0.1
Updated: 2026-03-07
Purpose: Canonical root AI context source synced to AGENTS.md entrypoints
-->

<ai_context version="1.0" tool="shared">

# Azedarach Project Context

> TUI Kanban board and CLI frontend clients on top of daemon backend for orchestrating parallel AI sessions with issue tracking

## Entrypoint Generation

This file is the canonical root context source for Codex-native configuration.

Nested `AGENTS.md` overlays are maintained directly in-repo for path-scoped guidance.

## Instructions Reference

Implementation-specific overlays:
- [ts-opentui/AGENTS.md](./ts-opentui/AGENTS.md)
- [go-bubbletea/AGENTS.md](./go-bubbletea/AGENTS.md)

## Critical Rules (Quick Reference)

1. **Modern CLI Tools**: Use `rg` (not grep), `fd` (not find), `sd` (not sed), and `bat` (not cat).
2. **Issue Tracker**: Start sessions with `az prime`, then use `az issue` for all tracked issue operations.
3. **Commit Before Done**: Always commit all changes before saying "done" or "complete".
4. **Codex Canonical Source**: Edit live instruction assets directly (`AGENTS.md`, nested `AGENTS.md`, `.codex/skills/*`, `.codex/subagents/*`).
5. **Git CWD Discipline**: When already in the target worktree/repo, use plain `git` commands. Use `git -C <path>` only when intentionally targeting a different path.
6. **Branch Workflow**: Use local-only git flow by default. Do not run remote sync/cleanup commands (for example pull/rebase, push, remote prune) unless explicitly requested.
7. **Safe File Operations**: Never delete untracked files or run `git restore` without explicit permission.
8. **No Message Parsing for Logic Gates**: Never gate behavior by parsing free-form error/message text. Use typed/tagged errors (for example `Data.TaggedError`) and `_tag`-based control flow.
9. **Implementation-Specific Rules Live in Nested Overlays**: Keep implementation runtime, language, and architecture policy in `ts-opentui/AGENTS.md` or `go-bubbletea/AGENTS.md`, not in root.
10. **Currently az is linked to the go-bubbletea impl**: `go-bubbletea` is the active default implementation for `az`; keep `ts-opentui` guidance only where explicitly needed during transition/cleanup work.
11. **The word spec means az spec 99% of the time**: it usually is not used in the sense of file based spec

## Quick Commands

```bash
# Search (modern tools)
rg "pattern" --type ts            # Search content (NOT grep)
fd "filename" -t f                # Find files (NOT find)

# Go in this repo (cache/module paths are exported via .envrc)
go test ./...

# Issue Tracking
az prime                          # Session primer + AI workflow guide
az impl list                      # Show available implementations
az issue create "Title" --impl <impl>

# Codex Context
fd . .codex/skills -td            # List installed local skills
fd . .codex/subagents -tf         # List local subagent prompts
```

## Codex Native Context Workflow

Canonical live paths:
- `AGENTS.md`, `ts-opentui/AGENTS.md`, `go-bubbletea/AGENTS.md` (directly maintained overlays)
- `.codex/skills/*` (Codex skills)
- `.codex/subagents/*` (Codex subagent prompts)
- `.codex/rules/*` and `.codex/config.toml` (Codex configuration)

Legacy migration snapshot (read-only reference):
- `.codex/context/rules/*`
- `.codex/context/docs/*`
- `.codex/context/README-migrated-context.md`

Sync behavior:
- There is no generation step in the active workflow.
- Edit Codex-native files directly and keep root + nested AGENTS aligned in the same change.
- OpenCode plugin files remain intentionally unmanaged by Codex context files.

## Implementation Selection

- Root guidance is implementation-agnostic.
- Use the nested overlay for whichever implementation directory you are changing.
- Include explicit `--impl <impl>` on `az issue` and `az spec` writes when required by project configuration.

## Task Management

**Track all non-trivial work through issue tracking** (preserves context across sessions).

- Run `az prime` at the start of each AI session.
- Use `az issue` for all issue updates and transitions.
- Any task expected to take more than one command MUST be tracked in the issue tracker.
- Keep one active parent issue per session whenever possible.
- If subagents are used, each subagent MUST create and maintain a child issue under the active parent issue, and close it when that subagent task is done.

## Issue Tracking Policy

**IMPORTANT**: This project uses **`az issue`** as the issue tracking interface. Do NOT use markdown TODOs, task lists, or parallel tracking systems.

### Important Rules

- ✅ First command in a new AI session: `az prime`
- ✅ Use `az issue` for issue tracking commands
- ✅ `az prime` is the source of AI workflow guidance for issue tracking
- ✅ Track every task that takes more than one command in the issue tracker
- ✅ Keep a session scoped to one active parent issue at a time when possible
- ✅ Subagents must create, maintain, and close child issues linked to the active parent issue
- ✅ Keep issue status updated as work progresses
- ✅ Keep the issue tracker as the single source of truth for issue state
- ❌ Do NOT create markdown TODO lists as a parallel tracker

## Large Epic Orchestration (Anti-Drift Protocol)

Use this protocol for any epic that spans multiple sessions, subagents, or major architecture boundaries.

### 1) Mandatory Epic Structure

- Create one parent epic with explicit child issues for each lane.
- Child issues MUST be independently completable and reviewable.
- Set dependency edges so merge order is machine-readable (`az issue dep add ... --type blocks`).
- Mark every issue that has children as `type=epic`.

### 2) Three-Layer Acceptance Criteria (Required)

Each implementation issue must include all three AC layers:

- **Runtime AC**: proves active entrypoint wiring (real binaries/commands, not isolated packages).
- **Integration AC**: proves cross-boundary behavior end-to-end (IPC/reconnect/streaming/etc).
- **Regression AC**: proves existing user-facing semantics still hold unless explicitly changed.

Use optional **Drift Guard AC** for explicit anti-regression checks (forbidden imports, placeholder detectors, path guards).

### 3) Evidence Bundle Required Before Close

An issue cannot close without notes containing:

1. commands run
2. key outputs or summarized assertions
3. files changed
4. explicit pass/fail verdict for each AC line item

If any item is missing, move/keep the issue in `in_progress`.

### 4) Placeholder and Shim Policy

- Do not close architecture migration issues while active runtime paths still use placeholders or local shims.
- Placeholders/shims may exist only behind explicit TODO scope with non-migration issue status.
- For migration epics, closure requires active-path replacement, not just test coverage around scaffolding.

### 5) Closure Gate Issue

For large epics, add one final child gate issue that:

- blocks on all implementation children
- re-runs the integrated test matrix
- verifies no active-path placeholders/shims remain
- performs final evidence-based checklist

Only close the parent epic after that gate issue closes.

### 6) Parallel Subagent Execution Rules

- Assign each subagent one issue lane with a disjoint file budget.
- Prefer git worktrees per subagent lane for isolation; merge only when that lane AC is green.
- Do not accept “temporary red” mainline merges for incomplete lanes.
- If cross-lane changes are unavoidable, land prerequisite lane first and rebase dependent lanes.

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

## Shared Skills

This repository has shared skills in `.codex/skills/` that apply to all implementations:

- **Skill Loading Policy**: Skills are task-scoped references, not mandatory bootstrap. Only load a skill when the current task explicitly needs it.
- **Workflow Skills** (`workflow/`): issue tracking, Azedarach CLI workflows, and spec maintenance

See `.codex/skills/` for available skill docs.

## OpenCode Plugins

This project uses two OpenCode plugins:

1. **opencode-pty** - PTY integration
2. **.opencode/plugin/azedarach.js** - Session status monitoring for TUI

Both are configured in `opencode.json`.

## RTK / Rust Token Killer Usage Guidelines (for Codex CLI)
### Objective
* When executing shell commands that may produce large amounts of output, prioritize using RTK (Rust Token Killer) to compress, filter, and summarize the output, reducing LLM token consumption and improving context efficiency.
* RTK is suitable for:
  
  * File reading
  * Code search
  * git status / diff
  * Test output
  * build / lint output
  * docker / kubectl logs
  * Other high-noise, high-output commands

### Codex CLI Environment Constraints
* RTK’s automatic hook / auto-rewrite capabilities (e.g., `rtk init --global`) currently mainly support **Claude Code** and **OpenCode**.
* In **Codex CLI**, **do not assume RTK has automatically taken over shell commands**.
* When using RTK in Codex CLI, you must rely on:
  
  1. The agent explicitly invoking `rtk ...` commands
  2. The rules defined in this document
* Therefore, in this environment, if the display shows the original command being executed, it usually means the original command is actually executed, not automatically rewritten by RTK.

### Mandatory Rules
* For commands that produce high output, **do not use the original command directly**; you must prioritize rewriting it using the corresponding RTK command.
* Only skip RTK when conditions for “allowed fallback to original commands” are met.
* If you ultimately decide to fall back to the original command, you should narrow the output scope as much as possible to avoid unbounded output.

### RTK Command Replacement Rules
The following high-output commands must be rewritten to RTK form by default:

* `git status` → `rtk git status`
* `git diff` → `rtk git diff`
* `git log` → `rtk git log`
* `cat <file>` → `rtk read <file>`
* `head <file>` → `rtk read <file>`
* `tail <file>` → `rtk read <file>`
* `grep <pattern> .` → `rtk grep <pattern> .`
* `rg <pattern>` → `rtk grep <pattern> .`
* `ls` → `rtk ls`
* `pytest` → `rtk pytest`
* `cargo test` → `rtk cargo test`
* `go test` → `rtk go test`
* `tsc` → `rtk tsc`
* `docker ps` → `rtk docker ps`
* `docker logs <container>` → `rtk docker logs <container>`
* `kubectl logs <pod>` → `rtk kubectl logs <pod>`

### RTK Usage Priority
#### Must use RTK first
* File reading
* Search output
* git status / diff / logs
* Test output
* build / lint output
* docker / kubectl logs

#### Recommended to use RTK
* Package list queries
* Dependency analysis
* Query-type outputs from package managers
* Other commands known to produce large outputs

#### Can use original commands directly
* `pwd`
* `echo`
* Very short-output commands
* Commands with strict output format requirements where RTK may alter the format

### RTK Fallback Rules
* If RTK clearly supports a command, it must be used first.
* If unsure whether RTK supports a command, try:
  
  * `rtk proxy <command>`

Examples:

* `rtk proxy make build`
* `rtk proxy uv run script.py`

### Allowed Cases for Falling Back to Original Commands
Only allowed under the following conditions:

1. RTK does not support the command
2. RTK output is insufficient to diagnose the issue
3. Full raw logs are required for deep debugging
4. The command output is very short, with no significant benefit from RTK
5. RTK alters necessary output format, affecting current analysis or subsequent processing

### RTK Behavioral Principles
* Prioritize reducing unnecessary output rather than blindly preserving all original output.
* In high-output scenarios, do not use original commands directly unless there is a clear reason.
* If RTK does not provide sufficient information on the first attempt, then fall back to the original command.
* Even after fallback, limit the output scope (e.g., only view necessary sections, files, or error segments).
* In Codex CLI, treat RTK as an **explicit command strategy**, not an implicit hook capability.

</ai_context>
