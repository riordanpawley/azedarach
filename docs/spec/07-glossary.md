# 07 - Glossary

## Terms

- Azedarach: terminal Kanban orchestration application specified in this folder.
- Board: primary TUI surface listing issues by workflow status.
- Card: visual representation of one issue on the board.
- Issue: canonical work item tracked in Azedarach local issue store.
- Project-local canonical DB: SQLite database at `<project-root>/.azedarach/azedarach.db` used as canonical persisted issue store for one project.
- Worktree-aware project resolution: command resolution behavior mapping sibling git-worktree paths (including nested subdirectories) back to the registered base project canonical DB.
- Sync target: optional external system mirrored from local canonical issue state.
- Beads adapter: optional issue<->Beads sync interface layer.
- Linear adapter: optional issue<->Linear sync interface layer.
- Burst window: short-term allowance for sync requests above sustained throughput before throttling/deferred execution begins.
- Az CLI Suite: canonical top-level `az` issue command suite (`init/prime/create/q/show/update/close/reopen/delete/list/ready/blocked/search/stale/count`) plus dependency/config/stats commands (`az dep ...`, `az config ...`, `az stats`) and project-management commands (`az project add/list/remove/switch`) used by agent workflows.
- Bootstrap prompt contract: normative session-start prompt guidance requiring top-level `az` commands for issue context and mutation flows.
- Backend-agnostic issue retrieval: requirement that agent-facing issue commands keep stable semantics regardless of optional sync adapter configuration.
- Backend-neutral not-found diagnostic: required issue-not-found message contract (`Issue not found internally nor externally: <issue-id>`) that avoids backend/sync implementation leakage.
- Destructive issue operation: issue mutation that removes canonical issue records (for example `az delete`) and requires explicit guardrails.
- Internal issue ID: canonical local identifier for issues in project SQLite that has no mandatory textual prefix requirement.
- Issue ID strategy: configurable per-project policy for generating internal IDs (for example incrementing numeric or adaptive-length lowercase alphabetic title hash).
- Tombstone issue record: logically deleted issue record retained for audit/history metadata and optional include-deleted query surfaces.
- Epic: issue that groups child issues for scoped drill-down and progress tracking; one relationship type among many possible dependencies.
- Drill-down: focused board mode showing only children of selected epic.
- Dependency edge: typed directed relationship between issues (for example blocks, depends-on, discovered-from, parent-child, related).
- Dependency graph: full set of issue nodes and dependency edges; not restricted to a tree.
- Dependency relation type key: canonical CLI/schema key representing one directed dependency class (for example `blocking`, `blocked-by`, `parent-child`, `discovered-from`).
- Dependency projection mode: `az show` dependency output shape selector (`none`, `counts`, `direct`, `verbose`).
- Dependency depth: maximum dependency-expansion depth in projection output; depth `0` returns counts-only without expanded dependency nodes.
- Prime guidance: deterministic agent briefing output from `az prime` containing command quick-reference and required workflow policies.
- Base branch: configurable integration branch used for default update/merge flows (for example develop, trunk, main).
- Follow-on merge: direct merge of eligible upstream source branch into target issue branch without routing through the base branch.
- Optimistic mutation: immediate in-memory UI state update performed before async persistence completes.
- Rollback: restoration of prior confirmed state after optimistic mutation persistence failure.
- Background operation: long-running tracked action with operation ID and lifecycle state.
- State probe: side-effect-free machine-readable snapshot surface for E2E automation assertions.
- Session: AI CLI process context tied to an issue, typically in tmux.
- Worktree: git worktree tied to issue-specific branch context.
- Dev server session: issue-scoped long-running app server process.
- Action mode: modal key prefix for operational commands.
- Goto mode: modal key prefix for jump navigation commands.
- Select mode: mode for multi-card selection and batch operations.
- Search mode: free-text filtering mode.
- Filter mode: structured field-based filtering mode.
- Sort mode: ordering mode for issue display.
- PR: pull request associated with issue branch.
- Cleanup: deletion of worktree and optionally closure of issue.
- Yolo start: session start variant that skips permissions prompts.
- Planning overlay: natural-language workflow creating epic/task structures.
- Attachment: image artifact linked to an issue.
- Connection state: availability state for remote/network-dependent operations.

## Status Terms

- Issue status:
  - Open: ready/unstarted work.
  - In Progress: currently active work.
  - Blocked: waiting on dependency or external condition.
  - Closed: completed/verified work.

- Session status:
  - Idle: no active session.
  - Busy: active execution.
  - Waiting: session needs user input.
  - Done: execution finished.
  - Error: execution failed.
  - Paused: execution intentionally suspended.

## Requirement/Acceptance IDs

- `AZ-FR-####`: functional requirement identifier.
- `AZ-AT-####`: acceptance scenario identifier.
