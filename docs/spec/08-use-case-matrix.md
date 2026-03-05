# 08 - Use Case Matrix

This section expands product behavior into concrete user-centered use cases.

## 8.1 Format

- UC ID
- Actor
- Goal
- Preconditions
- Trigger
- Main Flow
- Alternate Flows
- Postconditions

## 8.2 Board and Navigation Use Cases

### UC-BOARD-001 Scan open work quickly

- Actor: engineer
- Goal: identify top candidate issue to start
- Preconditions: board loaded
- Trigger: app opened or project switched
- Main Flow:
  1. user scans open column cards by priority and recency
  2. user navigates with `j/k`
  3. user opens detail with `Enter` for candidate
- Alternate Flows:
  - no open items -> user moves to blocked/in_progress for triage
  - dense board -> user toggles compact view with `Tab`
- Postconditions: focused issue selected for action

### UC-BOARD-002 Jump to remote card using labels

- Actor: engineer
- Goal: reach specific visible card without repeated navigation
- Preconditions: many visible cards
- Trigger: `g w`
- Main Flow:
  1. labels appear on visible cards
  2. user types two-char label
  3. focus jumps to target
- Alternate Flows:
  - mistyped label -> no jump and user retries
- Postconditions: target card focused

### UC-BOARD-003 Recover from visual corruption

- Actor: engineer
- Goal: restore readable screen
- Trigger: display corruption after terminal resize
- Main Flow:
  1. user presses `Ctrl-l`
  2. board redraws entirely
  3. focus and mode remain valid
- Postconditions: usable board state restored

## 8.3 Search/Filter/Sort Use Cases

### UC-FILT-001 Find auth-related tasks

- Trigger: `/` then `auth`
- Expected: results filtered by title/ID matches

### UC-FILT-002 Keep filter active while working

- Trigger: search query `Enter`
- Expected: user remains in Normal mode with active filter indicator

### UC-FILT-003 Show only high priority open tasks

- Trigger: `f s o` then `f p 1`
- Expected: set intersection shown

### UC-FILT-004 Focus on active sessions only

- Trigger: `f S U` and optionally `f S W`
- Expected: busy/waiting cards remain visible

### UC-FILT-005 Identify stale issues

- Trigger: age filter `7`
- Expected: tasks older than 7 days shown

### UC-FILT-006 Reset triage filters

- Trigger: `f c`
- Expected: all structured filters cleared

### UC-FILT-007 Re-prioritize visual order by urgency

- Trigger: `, p`
- Expected: highest priority cards float to top

### UC-FILT-008 Re-prioritize by freshest updates

- Trigger: `, u`
- Expected: most recently touched tasks first

### UC-FILT-009 Toggle sort direction for backlog review

- Trigger: repeat same sort key
- Expected: opposite order rendered

## 8.4 Select and Bulk Use Cases

### UC-BULK-001 Move a set of tasks into in_progress

- Trigger: `v` select tasks then `Space l`
- Expected: all valid tasks move right one status

### UC-BULK-002 Stop all sessions before context switch

- Trigger: selection then `Space x`
- Expected: all selected running sessions stopped

### UC-BULK-003 Cleanup old worktrees in one action

- Trigger: selection then `Space d`
- Expected: choice dialog then bulk cleanup result summary

### UC-BULK-004 Cancel dangerous bulk operation

- Trigger: bulk cleanup dialog then `Esc`
- Expected: operation aborted without side effects

## 8.5 Session Lifecycle Use Cases

### UC-SESS-001 Start implementation session

- Trigger: `Space s`
- Main Flow:
  1. create/ensure worktree
  2. create/ensure branch
  3. start tmux session
  4. run AI CLI
  5. show busy indicator

### UC-SESS-002 Start session with explicit work instruction

- Trigger: `Space S`
- Expected: assistant begins with default work-on-task prompt that instructs `az show <issue-id>` for canonical context retrieval

### UC-SESS-003 Fast autonomous mode

- Trigger: `Space !`
- Expected: skip-permission launch variant starts

### UC-SESS-004 Enter conversational mode

- Trigger: `Space c`
- Expected: chat-oriented assistant session starts

### UC-SESS-005 Attach to waiting session

- Preconditions: session state waiting
- Trigger: `Space a`
- Expected: tmux attach succeeds; user responds; returns to busy state

### UC-SESS-006 Pause long-running session

- Trigger: `Space p`
- Expected: session transitions to paused, worktree preserved

### UC-SESS-007 Resume paused session

- Trigger: `Space R`
- Expected: session resumes in same task context

### UC-SESS-008 Stop session to free resources

- Trigger: `Space x`
- Expected: session ends; card remains for future restarts

## 8.6 Dev Server Use Cases

### UC-DEV-001 Start per-task dev server

- Trigger: `Space r` after worktree exists
- Expected: server starts in dedicated session with task-specific ports

### UC-DEV-002 Inspect dev server logs

- Trigger: `Space v`
- Expected: user attaches to dev server tmux output

### UC-DEV-003 Recover from bad server state

- Trigger: `Space Ctrl-r`
- Expected: server restarts and status re-evaluates

### UC-DEV-004 Stop dev server

- Trigger: `Space r` when running
- Expected: server stops and indicator clears

## 8.7 Git and Merge Use Cases

### UC-GIT-001 Update branch from configured base branch before PR

- Trigger: `Space u`
- Expected: branch synced; conflict path available if needed

### UC-GIT-002 Review diff before merge

- Trigger: `Space f`
- Expected: semantic/plain diff opens in readable viewer

### UC-GIT-003 Merge branch into configured base branch iteratively

- Trigger: `Space m`
- Expected: local merge succeeds; worktree remains for continued edits

### UC-GIT-004 Abort conflicted merge

- Trigger: `Space M`
- Expected: merge state aborted safely

### UC-GIT-005 Consolidate exploratory work into another task

- Trigger: source `Space b`, choose target
- Expected: source changes merged into target branch

## 8.8 PR Use Cases

### UC-PR-001 Create draft PR

- Trigger: `Space P` with auto-draft enabled
- Expected: branch pushed and draft PR created

### UC-PR-002 Open existing PR for review

- Trigger: `Space O`
- Expected: browser opens task PR URL

### UC-PR-003 Retry PR after auth failure

- Trigger: auth failure then login then `Space P` retry
- Expected: successful creation without data loss

## 8.9 Epic Use Cases

### UC-EPIC-001 Open epic detail like any other issue

- Trigger: `Enter` on epic
- Expected: standard detail panel opens and includes epic progress summary

### UC-EPIC-002 Focus on epic children only

- Trigger: `Space G` on epic (or `g` from epic detail)
- Expected: drill-down board with header and progress

### UC-EPIC-003 Return to global board context

- Trigger: `q` in drill-down
- Expected: parent board restored, focus on epic card

### UC-EPIC-004 Track epic completion trend

- Trigger: repeated drill-down checks
- Expected: progress count/bar updates as children close

## 8.10 Authoring Use Cases

### UC-AUTH-001 Create issue manually with template

- Trigger: `c`
- Expected: structured form saved into new issue

### UC-AUTH-002 Edit issue manually

- Trigger: `Space e`
- Expected: changes persisted to local canonical issue store (and queued for sync if configured)

### UC-AUTH-003 Create issue via natural language

- Trigger: `C`
- Expected: assistant creates issue with sensible defaults

### UC-AUTH-004 Edit issue via natural language

- Trigger: `Space E`
- Expected: assistant applies update command sequence

### UC-AUTH-005 Fork issue into child task

- Trigger: `Space F`
- Expected: dependency relationship created with parent context

### UC-AUTH-006 Initialize CLI workspace for project

- Trigger: run `az init` in project context
- Expected: `.azedarach` workspace prerequisites are initialized deterministically without mutating unrelated project files

### UC-AUTH-007 Create issue via CLI lifecycle command

- Trigger: run `az create "Title" -p 1 --type bug`
- Expected: issue is created in canonical local store with configured ID strategy and appears on board refresh

### UC-AUTH-008 Quick-capture issue via CLI

- Trigger: run `az q "Fix typo"`
- Expected: minimal-input issue is created with canonical defaults and deterministic output payload

### UC-AUTH-009 Show issue via CLI

- Trigger: run `az show <issue-id>`
- Expected: command resolves canonical issue details for active project context

### UC-AUTH-010 Update issue metadata via CLI

- Trigger: run `az update <issue-id> ...`
- Expected: canonical local issue metadata is updated and persists across board refresh

### UC-AUTH-011 Close issue via CLI

- Trigger: run `az close <issue-id>`
- Expected: issue transitions to closed in canonical local state

### UC-AUTH-012 Reopen issue via CLI

- Trigger: run `az reopen <issue-id>`
- Expected: closed issue transitions back to an active workflow state

### UC-AUTH-013 Delete issue via CLI (guarded tombstone)

- Trigger: run `az delete <issue-id>`
- Expected: destructive guardrail path requires explicit confirmation before tombstone delete applies

### UC-AUTH-014 Query active issues via list command

- Trigger: run `az list --status open --priority 0-1`
- Expected: canonical active-project issue set is returned with backend-agnostic semantics

### UC-AUTH-015 Query actionable and blocked work

- Trigger: run `az ready` and `az blocked`
- Expected: actionable and blocked subsets are returned deterministically from canonical local state

### UC-AUTH-016 Query search, stale, and grouped counts

- Trigger: run `az search "authentication"`, `az stale --days 30`, and `az count --by status`
- Expected: text search, staleness filters, and grouped aggregates are deterministic and machine-readable

### UC-AUTH-017 Manage and inspect dependencies via CLI

- Trigger: run `az dep add/remove/list/tree/cycles` against fixture issues
- Expected: dependency graph changes and inspection outputs align with canonical dependency model and cycle policy

### UC-AUTH-018 Validate and inspect configuration via CLI

- Trigger: run `az config validate` and `az config show`
- Expected: config validation returns schema diagnostics and config show returns effective runtime settings

### UC-AUTH-019 Inspect project statistics via CLI

- Trigger: run `az stats`
- Expected: command returns canonical project issue/statistical summaries without requiring sync-target reads

### UC-AUTH-020 Create issue with prefix-free short internal ID

- Trigger: create issue through manual or AI create path
- Expected: internal canonical ID is short/typable and does not require fixed textual prefix

### UC-AUTH-021 Switch ID generation strategy by project

- Trigger: change ID strategy setting (numeric increment vs alpha hash), then create issues
- Expected: generated IDs follow configured strategy constraints without mixed alphanumeric typing in built-in modes

## 8.11 Attachment Use Cases

### UC-ATT-001 Attach screenshot from clipboard

- Trigger: `Space i`, choose paste
- Expected: attachment saved and listed

### UC-ATT-002 Attach image from file path

- Trigger: `Space i`, path entry mode
- Expected: file validated and attached

### UC-ATT-003 Preview attachment inline

- Trigger: detail panel select item, press `v`
- Expected: preview overlay renders with next/prev navigation

### UC-ATT-004 Open attachment externally

- Trigger: detail panel select item, press `o`
- Expected: system image viewer opens file

### UC-ATT-005 Remove obsolete attachment

- Trigger: detail panel select item, press `x`
- Expected: file and metadata removed

## 8.12 Planning Use Cases

### UC-PLAN-001 Generate epic from feature prompt

- Trigger: `p`, submit feature text
- Expected: epic and child tasks created with dependencies

### UC-PLAN-002 Monitor planning execution in tmux

- Trigger: planning running, press `a`
- Expected: user can observe/assist generation session

### UC-PLAN-003 Cancel planning before commit

- Trigger: `Esc` during cancellable phase
- Expected: planning stops safely and returns to board

## 8.13 Settings Use Cases

### UC-SET-001 Switch AI tool profile

- Trigger: `s`, navigate to CLI tool, toggle
- Expected: subsequent session starts use selected tool

### UC-SET-002 Toggle skip permissions default

- Trigger: settings toggle
- Expected: start behavior reflects value

### UC-SET-003 Toggle PR auto-draft

- Trigger: settings toggle
- Expected: PR creation mode changes accordingly

### UC-SET-004 Open raw config for advanced edits

- Trigger: `e` in settings
- Expected: config editor opens and saves valid structure

### UC-SET-005 Configure internal issue ID strategy

- Trigger: settings toggle for issue ID strategy (incrementing numeric vs alpha hash)
- Expected: newly created issue IDs follow configured strategy, remain prefix-free, and avoid mixed alphanumeric typing in built-in modes

## 8.14 Dependency Graph Use Cases

### UC-DEP-001 Inspect upstream dependencies beyond epic hierarchy

- Trigger: open issue detail on non-epic issue
- Expected: typed incoming/outgoing dependency edges are visible

### UC-DEP-002 Add blocks relation between sibling tasks

- Trigger: dependency add flow from source task to target task
- Expected: relation persists and target/source readiness state updates

### UC-DEP-003 Remove stale discovered-from link

- Trigger: dependency remove flow on lineage edge
- Expected: explicit confirmation then edge removal and graph refresh

### UC-DEP-004 Continue issue from eligible upstream-related work

- Trigger: open issue dependency context, select eligible upstream source, invoke follow-on merge
- Expected: source branch merges into target issue branch directly (no base-branch hop)

### UC-DEP-005 Resolve one of multiple upstream sources incrementally

- Trigger: target issue with multiple upstream sources; merge from one source
- Expected: selected relationship updates while remaining unsatisfied sources stay visible

### UC-DEP-006 Create issue branch from upstream source at runtime

- Trigger: start session on issue with missing branch and eligible upstream sources
- Expected: runtime origin chooser allows base branch or chosen upstream source

### UC-DEP-007 Fork child from parent drill-down using parent branch

- Trigger: in parent drill-down invoke `Space F` for child flow
- Expected: parent preselected as upstream branch source with override option

### UC-DEP-008 Read complex graph on main board quickly

- Trigger: scan board with mixed dependency types
- Expected: compact relationship chips communicate upstream/downstream/blocking counts without clutter

### UC-DEP-009 Switch drill-down relation scope

- Trigger: in drill-down toggle relation scope from children to upstream/downstream
- Expected: board slice updates deterministically with preserved focus

### UC-DEP-010 Parent drill-down context merge into child

- Trigger: in parent drill-down focus child and invoke `Space m`
- Expected: parent is preselected as upstream merge source with explicit override path

### UC-DEP-011 Recreate missing branch with runtime origin chooser

- Trigger: start issue flow after branch loss/invalidation
- Expected: runtime chooser appears again and allows base/upstream origin selection

### UC-DEP-012 Apply deterministic suggested source order

- Trigger: open follow-on merge picker with many upstream sources
- Expected: suggested source order is stable while manual selection remains available

## 8.15 Multi-Project Use Cases

### UC-PROJ-001 Switch active project

- Trigger: `g p`, select project index
- Expected: board reloads against selected project data

### UC-PROJ-002 Auto-select project by cwd

- Trigger: launch app inside registered project path
- Expected: matching project auto-selected

### UC-PROJ-003 Fallback to default project

- Trigger: launch app outside registered paths
- Expected: configured default project used

### UC-PROJ-004 Project-local DB isolation on switch

- Trigger: switch between two registered projects with different issue sets
- Expected: app fully swaps to selected project's `<project-root>/.azedarach/azedarach.db` with no cross-project issue leakage

### UC-PROJ-005 Register project via CLI

- Trigger: run `az project add /path/to/project --name my-project`
- Expected: project registry persists entry and project becomes selectable via `g p`

### UC-PROJ-006 List and remove project via CLI

- Trigger: run `az project list`, then `az project remove my-project`
- Expected: listing reflects deterministic registry state before/after removal

### UC-PROJ-007 Switch active/default project via CLI

- Trigger: run `az project switch project-name`
- Expected: active/default project changes and subsequent issue commands bind to switched project context

### UC-PROJ-008 Distinguish issue list and project list commands

- Trigger: run `az list` and `az project list` in same context
- Expected: issue-query and project-registry payloads are unambiguous and deterministic

## 8.16 Operational and Recovery Use Cases

### UC-OPS-001 Handle missing session on attach

- Trigger: `Space a` with no tmux session
- Expected: actionable error, no crash

### UC-OPS-002 Handle offline PR attempt

- Trigger: `Space P` while offline
- Expected: clear offline failure and retry guidance

### UC-OPS-003 Recover from merge conflict loop

- Trigger: repeated conflict failures
- Expected: abort path and explicit manual resolution guidance

### UC-OPS-004 Clear stale filters causing empty board

- Trigger: no cards visible after filter stack
- Expected: empty-state hint points to `f c`

### UC-OPS-005 Restore focus after refreshed dataset changes

- Trigger: issue removed during refresh
- Expected: nearest valid focus chosen without panic

## 8.17 Startup and Re-entry Use Cases

### UC-BOOT-001 Launch with missing required dependency

- Trigger: app starts while required external tool is unavailable
- Expected: diagnostics shown; dependent actions disabled; board still accessible where possible

### UC-BOOT-002 Resume previous board context

- Trigger: relaunch app in same project after normal exit
- Expected: last view/filter/sort restored and focus rehydrated when valid

### UC-BOOT-003 Recover when last focused issue no longer exists

- Trigger: app relaunch after issue was removed/closed externally
- Expected: focus falls back to nearest valid item with non-blocking notice

## 8.18 Concurrency and Mutation Use Cases

### UC-CONC-001 Save edit after external update

- Trigger: `Space e`, edit open, issue changed elsewhere, then save
- Expected: stale conflict prompt offers reload/overwrite/cancel

### UC-CONC-002 Keep typing during background refresh

- Trigger: input overlay active while refresh occurs
- Expected: local draft input preserved; no forced overlay close

### UC-CONC-003 Bulk operation with disappearing targets

- Trigger: selected items change externally before `Space d` or `Space l`
- Expected: missing items skipped and reported; remaining items processed

### UC-CONC-004 Local store lock contention on mutate

- Trigger: mutating action while local issue store is locked
- Expected: explicit busy/lock error with retry guidance and no duplicate writes

## 8.19 Terminal Constraints Use Cases

### UC-TERM-001 Operate on narrow terminal

- Trigger: resize to constrained width/height
- Expected: compact status line, preserved mode visibility, navigable board

### UC-TERM-002 Handle long titles and paths

- Trigger: board/detail with very long metadata
- Expected: deterministic truncation without losing issue identity

### UC-TERM-003 Run in low/no color terminal

- Trigger: terminal color support disabled
- Expected: state still understandable via text/symbol fallback

## 8.20 Use Case Priority Bands

### P0 Critical

- UC-SESS-001, UC-SESS-005, UC-GIT-001, UC-PR-001, UC-BOARD-001

### P1 High

- UC-BULK-003, UC-GIT-003, UC-EPIC-001, UC-FILT-003, UC-DEV-001

### P2 Medium

- UC-AUTH-003, UC-PLAN-001, UC-PROJ-001, UC-ATT-003, UC-SET-001

### P3 Low

- UC-BOARD-003, UC-ATT-004, UC-FILT-009, UC-OPS-004

## 8.21 Cross-Reference to Requirements

- Board/nav use cases -> AZ-FR-0001..0309
- Search/filter/sort use cases -> AZ-FR-0501..0518
- Session/dev use cases -> AZ-FR-0801..0907
- Git/PR use cases -> AZ-FR-1001..1108
- Authoring/planning -> AZ-FR-1301..1506
- Attachments/settings/projects -> AZ-FR-1601..1807
- Recovery/ops -> AZ-FR-2101..2205 and Section 05 failure cases
- Startup/re-entry -> AZ-FR-2701..2708
- Concurrency/mutation -> AZ-FR-2801..2808
- Terminal/idempotence -> AZ-FR-2901..3004
- Guardrails/reconciliation/determinism -> AZ-FR-3101..3304
- Dependency graph -> AZ-FR-3401..3410
- Runtime branch origin -> AZ-FR-3501..3506
- Upstream follow-on merge -> AZ-FR-3601..3611
- Relationship representation -> AZ-FR-3701..3705
- Optimistic mutation -> AZ-FR-3801..3818
- Background operations -> AZ-FR-3901..3909
- State probe and harness -> AZ-FR-4001..4010, AZ-FR-4101..4110
- Az CLI command suite -> AZ-FR-4201..4237

## 8.22 Extended Scenario Catalog (Condensed)

The following condensed scenarios provide additional edge and scale coverage.

### Navigation and Rendering

- UC-EXT-001: navigate across empty columns without losing focus.
- UC-EXT-002: preserve focus when switching between Kanban and list view.
- UC-EXT-003: maintain stable card ordering after repeated refreshes.
- UC-EXT-004: show overflow indicators for long columns.
- UC-EXT-005: respect terminal resize from very wide to very narrow.
- UC-EXT-006: ensure help overlay remains readable on small terminals.

### Session and Attach

- UC-EXT-010: attach while session transitions busy->waiting.
- UC-EXT-011: attach while session exits unexpectedly.
- UC-EXT-012: pause command during waiting state behaves predictably.
- UC-EXT-013: stop command on already-stopped session no-ops.
- UC-EXT-014: resume command on non-paused session gives guidance.
- UC-EXT-015: state indicator updates within acceptable latency.

### Dev Server

- UC-EXT-020: starting dev server without worktree prompts creation path.
- UC-EXT-021: restarting dev server during startup recovers cleanly.
- UC-EXT-022: simultaneous servers allocate unique ports.
- UC-EXT-023: server crash updates indicator to error/stopped.
- UC-EXT-024: view action unavailable when no server exists.

### Git and Merge

- UC-EXT-030: update-from-base-branch when branch already up to date.
- UC-EXT-031: update-from-base-branch with uncommitted local changes.
- UC-EXT-032: merge to base branch canceled at confirmation prompt.
- UC-EXT-033: merge to base branch conflicts then abort and retry.
- UC-EXT-034: show diff for binary file changes with graceful fallback.
- UC-EXT-035: issue-branch-to-issue-branch merge flow canceled before target selection.

### PR and Network

- UC-EXT-040: PR create on branch with no commits.
- UC-EXT-041: PR create while disconnected then reconnect and retry.
- UC-EXT-042: open PR when metadata URL malformed.
- UC-EXT-043: PR already merged status indicator updates.
- UC-EXT-044: PR closed without merge reflected on card.
- UC-EXT-045: Linear outbound sync bursts above limit are throttled with queued retry diagnostics.

### Authoring and Planning

- UC-EXT-050: manual create canceled before save leaves no issue.
- UC-EXT-051: manual edit invalid field rejected safely.
- UC-EXT-052: AI create returns multiple issue options for confirmation.
- UC-EXT-053: planning produces cyclic dependencies and validation rejects.
- UC-EXT-054: planning partially succeeds; user can continue manually.
- UC-EXT-055: top-level `az` command targets missing issue and returns deterministic diagnostics.

### Attachments

- UC-EXT-060: very large image attaches with progress feedback.
- UC-EXT-061: unsupported file extension rejected with guidance.
- UC-EXT-062: preview unavailable in terminal fallback mode.
- UC-EXT-063: remove attachment while preview overlay open.
- UC-EXT-064: external open command unavailable on host OS.

### Multi-Project

- UC-EXT-070: switch projects while session operations in flight.
- UC-EXT-071: project path exists but Azedarach local store is not initialized.
- UC-EXT-072: default project removed from registry.
- UC-EXT-073: duplicate project names in registry handled safely.
- UC-EXT-074: selected project is missing `.azedarach/azedarach.db` and store bootstrap is created automatically.

### Bulk and Scale

- UC-EXT-080: select all on 500+ visible tasks remains responsive.
- UC-EXT-081: bulk move mixed-validity transitions reports partial success.
- UC-EXT-082: bulk stop with rapidly changing session states remains deterministic.
- UC-EXT-083: bulk cleanup interrupted by permission errors gives per-task results.
- UC-EXT-084: invert visible selection preserves hidden selected IDs unless explicitly cleared.
- UC-EXT-085: selection status clearly differentiates selected total, visible selected, and hidden selected.
- UC-EXT-086: destructive bulk target preview reflects frozen execution set and reports drifted IDs.

### UX and Safety

- UC-EXT-090: destructive actions always present a clear cancel path.
- UC-EXT-091: error messages include issue/project context.
- UC-EXT-092: status bar never displays stale mode after overlay close.
- UC-EXT-093: repeated Esc key presses unwind to Normal mode.
- UC-EXT-094: app exit from nested overlays behaves predictably.

## 8.23 Additional Gap-Closure Use Cases

### UC-GAP-001 Confirm destructive operation after target drift

- Trigger: preflight opens, target set changes externally, user confirms
- Expected: operation revalidates targets and reports skipped entries

### UC-GAP-002 Recover from remote branch divergence during PR create

- Trigger: `Space P` on branch rejected as non-fast-forward
- Expected: guided reconciliation then retry continues PR flow

### UC-GAP-003 Reconcile stale running indicator with missing tmux session

- Trigger: card shows busy but session lookup fails
- Expected: stale state corrected or recovery options offered

### UC-GAP-004 Adopt orphan tmux session into board metadata

- Trigger: tmux session exists with deterministic task name but no board linkage
- Expected: user can adopt session and regain attach/control actions

### UC-GAP-005 Maintain deterministic order with equal sort keys

- Trigger: many cards share same priority/updated value
- Expected: stable secondary ordering prevents cursor jumpiness on refresh

## 8.24 E2E and Operations Use Cases

### UC-E2E-001 Assert mode/focus via machine probe

- Trigger: test harness performs navigation then requests probe
- Expected: probe exposes current mode, focus issue ID, and visible card IDs for assertions

### UC-E2E-002 Validate optimistic rollback behavior

- Trigger: harness injects local commit failure during optimistic move/edit
- Expected: UI rolls back to prior confirmed state and emits actionable error

### UC-E2E-003 Track and cancel background operation

- Trigger: start long-running operation, open operations monitor, request cancel
- Expected: operation transitions through lifecycle states with deterministic completion semantics

### UC-E2E-004 Validate full-screen visual snapshots

- Trigger: run deterministic fixture scenario and capture TUI frame snapshot
- Expected: snapshot matches approved baseline at cell-level fidelity

### UC-E2E-005 Run performance and stress suites

- Trigger: run perf and stress test profiles on large dependency graph
- Expected: latency budgets pass and UI remains stable under concurrent operations
