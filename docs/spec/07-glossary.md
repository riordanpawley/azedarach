# 07 - Glossary

## Terms

- Azedarach: terminal Kanban orchestration application specified in this folder.
- Board: primary TUI surface listing issues by workflow status.
- Card: visual representation of one issue on the board.
- Issue / Bead: task tracker item managed through beads CLI.
- Epic: issue that groups child issues for scoped drill-down and progress tracking; one relationship type among many possible dependencies.
- Drill-down: focused board mode showing only children of selected epic.
- Dependency edge: typed directed relationship between issues (for example blocks, depends-on, discovered-from, parent-child, related).
- Dependency graph: full set of issue nodes and dependency edges; not restricted to a tree.
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
