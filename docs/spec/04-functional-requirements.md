# 04 - Functional Requirements

This section is normative.

## 4.1 Requirement ID Scheme

- Prefix: `AZ-FR`
- Format: `AZ-FR-####`

## 4.2 Board and Data Requirements

- AZ-FR-0001: The system MUST render a TUI board with issue cards grouped by status.
- AZ-FR-0002: The board MUST support at least statuses Open, In Progress, Blocked, Closed.
- AZ-FR-0003: Each card MUST display issue ID and title.
- AZ-FR-0004: Each card MUST display priority.
- AZ-FR-0005: Each card SHOULD display issue type.
- AZ-FR-0006: The board MUST track focused card/column location.
- AZ-FR-0007: The board MUST handle empty columns without focus corruption.
- AZ-FR-0008: The board MUST refresh from tracker data on demand.
- AZ-FR-0009: The board SHOULD support periodic refresh.
- AZ-FR-0010: Loading states MUST be visible when data is unavailable.

## 4.3 Modal Interaction Requirements

- AZ-FR-0101: The UI MUST implement Normal mode as default mode.
- AZ-FR-0102: The UI MUST support Action mode activation from Normal via `Space`.
- AZ-FR-0103: The UI MUST support Goto mode activation via `g`.
- AZ-FR-0104: The UI MUST support Select mode activation via `v`.
- AZ-FR-0105: The UI MUST support Search mode activation via `/`.
- AZ-FR-0106: The UI MUST support Filter mode activation via `f`.
- AZ-FR-0107: The UI MUST support Sort mode activation via `,`.
- AZ-FR-0108: Active mode MUST be visible in status bar.
- AZ-FR-0109: `Esc` MUST return to previous stable mode in modal contexts.
- AZ-FR-0110: Unsupported keys in a mode SHOULD no-op safely.

## 4.4 Navigation Requirements

- AZ-FR-0201: `h/j/k/l` MUST navigate board focus.
- AZ-FR-0202: Arrow keys SHOULD map to equivalent navigation.
- AZ-FR-0203: Navigation MUST support virtual scrolling in overflowing columns.
- AZ-FR-0204: Half-page movement MUST be supported.
- AZ-FR-0205: Cursor MUST remain on valid item after data refresh.
- AZ-FR-0206: Column boundary behavior MUST be deterministic.
- AZ-FR-0207: Focus MUST survive transient overlay open/close cycles.

## 4.5 Goto Requirements

- AZ-FR-0301: `g g` MUST jump to top of current column.
- AZ-FR-0302: `g e` MUST jump to bottom of current column.
- AZ-FR-0303: `g h` MUST jump to first column.
- AZ-FR-0304: `g l` MUST jump to last column.
- AZ-FR-0305: `g w` MUST activate jump labels.
- AZ-FR-0306: Jump labels MUST map uniquely to visible cards.
- AZ-FR-0307: Entering valid label MUST move focus to mapped card.
- AZ-FR-0308: Invalid label input MUST fail gracefully and stay in safe state.
- AZ-FR-0309: `g p` MUST open project selector.

## 4.6 Select and Bulk Requirements

- AZ-FR-0401: Select mode MUST track selected issue IDs.
- AZ-FR-0402: `a` in Select mode MUST toggle current selection.
- AZ-FR-0403: `A` in Select mode MUST select all in current column.
- AZ-FR-0404: `%` MUST select all visible non-tombstoned tasks.
- AZ-FR-0405: Selection count MUST be visible when >0.
- AZ-FR-0406: Exiting Select mode with `v` or `Esc` MUST clear selection.
- AZ-FR-0407: Bulk-compatible actions MUST apply to selected set.
- AZ-FR-0408: Bulk operations MUST report per-item failures.
- AZ-FR-0409: `*` in Select mode MUST invert selection for visible non-tombstoned issues.
- AZ-FR-0410: `x` in Select mode MUST clear all selected IDs while remaining in Select mode.
- AZ-FR-0411: Selection membership MUST be ID-based and MUST reconcile deterministically across refresh/sort/filter changes.
- AZ-FR-0412: When hidden selected items exist, status UI MUST expose explicit hidden-selection count.
- AZ-FR-0413: Entering bulk destructive actions from Select mode MUST show target preview with selected-count and scope before confirmation.
- AZ-FR-0414: Bulk execution target set MUST freeze at execute-time and MUST report drifted/skipped IDs if state changes before apply.

## 4.7 Search/Filter/Sort Requirements

- AZ-FR-0501: Search mode MUST filter by title and ID.
- AZ-FR-0502: Search filtering MUST update live while typing.
- AZ-FR-0503: Search MUST be case-insensitive by default.
- AZ-FR-0504: `Enter` in search MUST commit query and return to Normal.
- AZ-FR-0505: `Esc` in search MUST clear query and return to Normal.
- AZ-FR-0506: Filter mode MUST support status dimension.
- AZ-FR-0507: Filter mode MUST support priority dimension.
- AZ-FR-0508: Filter mode MUST support type dimension.
- AZ-FR-0509: Filter mode MUST support session-state dimension.
- AZ-FR-0510: Filter mode MUST support clear-all action.
- AZ-FR-0511: Filter mode SHOULD support age filters.
- AZ-FR-0512: OR logic MUST apply within same filter dimension.
- AZ-FR-0513: AND logic MUST apply across dimensions.
- AZ-FR-0514: Sort mode MUST support session-based sort.
- AZ-FR-0515: Sort mode MUST support priority-based sort.
- AZ-FR-0516: Sort mode MUST support updated-time sort.
- AZ-FR-0517: Repeating sort key MUST toggle sort direction.
- AZ-FR-0518: Active sort state SHOULD be visible.

## 4.8 View Requirements

- AZ-FR-0601: The system MUST provide Kanban view.
- AZ-FR-0602: The system MUST provide Compact list view.
- AZ-FR-0603: `Tab` MUST toggle between views.
- AZ-FR-0604: Status bar SHOULD display current view mode indicator.
- AZ-FR-0605: Filtering, sorting, and search MUST work in both views.

## 4.9 Epic Requirements

- AZ-FR-0701: Issues of type epic MUST be distinguishable on card/detail.
- AZ-FR-0702: Epics MUST support drill-down child-board view.
- AZ-FR-0703: Drill-down MUST show only epic children.
- AZ-FR-0704: Drill-down header MUST include epic ID and title.
- AZ-FR-0705: Drill-down header MUST include closed/total progress.
- AZ-FR-0706: `q` and `Esc` MUST exit drill-down.
- AZ-FR-0707: Exiting drill-down SHOULD restore focus to source epic.
- AZ-FR-0708: `Enter` on epic MUST open standard issue detail panel.
- AZ-FR-0709: Epic detail panel MUST expose explicit drill-down entry action.

## 4.10 Session Orchestration Requirements

- AZ-FR-0801: `Space s` MUST start session for focused issue.
- AZ-FR-0802: `Space S` MUST start session with default work prompt.
- AZ-FR-0802a: `Space S` and `Space !` prompt injection MUST preload `az prime` context and issue-specific details via `az issue get <issue-id>` using runtime issue context from session launch.
- AZ-FR-0802b: When issue ID cannot be resolved for prompt injection, the system MUST preload `az prime` context and SHOULD include `az issue --help` fallback guidance.
- AZ-FR-0803: `Space !` MUST support skip-permission start variant.
- AZ-FR-0804: `Space c` SHOULD support chat session variant.
- AZ-FR-0805: Session start MUST ensure task worktree context exists.
- AZ-FR-0806: Session start MUST ensure task branch context exists.
- AZ-FR-0807: Session start SHOULD update issue status to in_progress.
- AZ-FR-0808: Session state MUST be shown on task card.
- AZ-FR-0809: `Space a` MUST attach to running session.
- AZ-FR-0810: Attach flow SHOULD offer update-from-base-branch prompt when stale.
- AZ-FR-0811: `Space p` MUST pause running session.
- AZ-FR-0812: `Space R` MUST resume paused session.
- AZ-FR-0813: `Space x` MUST stop session.
- AZ-FR-0814: Stop MUST not implicitly delete worktree.
- AZ-FR-0815: Session state detector MUST map busy/waiting/done/error/paused.

## 4.11 Dev Server Requirements

- AZ-FR-0901: `Space r` MUST toggle dev server lifecycle for issue.
- AZ-FR-0902: Dev server start MUST run in issue-scoped execution context.
- AZ-FR-0903: `Space v` MUST attach/view dev server output.
- AZ-FR-0904: `Space Ctrl-r` MUST restart dev server.
- AZ-FR-0905: Port assignment MUST avoid collisions across active worktrees.
- AZ-FR-0906: Active dev server indicator MUST be visible on card/status.
- AZ-FR-0907: Startup errors MUST produce actionable feedback.

## 4.12 Git Workflow Requirements

- AZ-FR-1001: `Space u` MUST update task branch from configured base branch.
- AZ-FR-1001a: New issue branch mappings MUST use stable, human-readable names in `<author>/<slug>` format where `author` is derived from `git config user.name` and `slug` is title-derived (not internal issue IDs).
- AZ-FR-1001b: Branch-name mapping MUST be deterministic per issue once assigned and MUST be collision-safe across local and relevant remote refs.
- AZ-FR-1001c: Title-derived slug generation MUST enforce a configurable maximum length via truncation (applies to the slug segment only).
- AZ-FR-1001d: Existing pre-author-prefix branch mappings MUST remain supported and MUST NOT be auto-migrated during normal operations.
- AZ-FR-1002: Update flow MUST surface merge conflicts clearly.
- AZ-FR-1003: Conflict resolution path MUST be available.
- AZ-FR-1004: `Space M` MUST abort in-progress merge.
- AZ-FR-1005: Abort merge MUST restore pre-merge state when possible.
- AZ-FR-1006: `Space f` MUST show diff from branch merge-base.
- AZ-FR-1007: `Space m` MUST merge task branch into configured base branch in default context.
- AZ-FR-1008: Merge flow MUST warn when conflict risk is detected.
- AZ-FR-1009: Merge to base branch SHOULD keep worktree active post-merge.
- AZ-FR-1010: `Space b` MUST support merge source issue branch into target issue branch.
- AZ-FR-1011: merge-issue flow MUST prevent self-merge.
- AZ-FR-1012: The system MUST provide a bulk "bring up to date" operation across a selected issue set.
- AZ-FR-1013: For each issue in bulk update, merge source MUST be resolved per policy (configured base branch or eligible parent/upstream branch when relation context applies).
- AZ-FR-1014: Bulk update execution MUST use a FIFO work queue with bounded maximum concurrency.
- AZ-FR-1015: When a bulk item hits merge conflicts and conflict-assistant policy is enabled, the system MUST trigger an automated conflict-resolution assistant attempt for that item.
- AZ-FR-1016: If automated conflict resolution fails or exhausts allowed attempts, the item MUST remain in recoverable conflict state with explicit manual-resolution guidance.
- AZ-FR-1017: Bulk update MUST continue processing remaining queued items after per-item failure and report per-item outcomes in completion summary.

## 4.13 PR Requirements

- AZ-FR-1101: `Space P` MUST create PR for focused issue branch.
- AZ-FR-1102: PR creation MUST ensure branch is pushed.
- AZ-FR-1103: PR creation SHOULD sync from configured base branch before opening PR.
- AZ-FR-1104: PR defaults (draft/ready) MUST be configurable.
- AZ-FR-1105: PR metadata MUST be persisted to issue context.
- AZ-FR-1106: Card SHOULD show PR state indicator.
- AZ-FR-1107: `Space O` MUST open existing PR in browser.
- AZ-FR-1108: open-PR action MUST fail gracefully when no PR exists.

## 4.14 Cleanup Requirements

- AZ-FR-1201: `Space d` MUST support worktree cleanup for focused issue.
- AZ-FR-1202: Cleanup MUST allow worktree-only mode.
- AZ-FR-1203: Cleanup SHOULD allow full mode (worktree + issue close).
- AZ-FR-1204: Bulk cleanup MUST offer explicit mode choice.
- AZ-FR-1205: Cleanup MUST report partial failures explicitly.

## 4.15 Issue Edit/Create Requirements

- AZ-FR-1301: `c` MUST support creating issue manually.
- AZ-FR-1302: Manual create MUST enforce required title field.
- AZ-FR-1303: Manual create SHOULD support type/priority/status fields.
- AZ-FR-1304: `C` SHOULD support AI-assisted creation.
- AZ-FR-1305: `Space e` MUST support manual issue edit.
- AZ-FR-1306: `Space E` SHOULD support AI-assisted issue edit.
- AZ-FR-1307: edit/create flows MUST preserve tracker schema validity.

## 4.16 Fork Requirements

- AZ-FR-1401: `Space F` MUST support creating forked work items.
- AZ-FR-1402: Fork flow SHOULD support child, sibling, and epic-related variants.
- AZ-FR-1403: Forked issue relationships MUST be persisted in tracker dependencies.
- AZ-FR-1404: Fork flow MUST provide runtime branch-origin choice (base branch or eligible upstream-related source branch).
- AZ-FR-1405: When forking a child from parent drill-down context, parent SHOULD be preselected as upstream branch source.

## 4.17 Planning Requirements

- AZ-FR-1501: `p` MUST open planning overlay.
- AZ-FR-1502: Planning overlay MUST accept natural language prompt.
- AZ-FR-1503: Planning execution MUST provide progress visibility.
- AZ-FR-1504: Planning SHOULD perform iterative review/refinement.
- AZ-FR-1505: Planning MUST create issues and dependencies on success.
- AZ-FR-1506: Planning failures MUST preserve recoverable state and message.

## 4.18 Attachment Requirements

- AZ-FR-1601: `Space i` MUST open image attachment overlay.
- AZ-FR-1602: Attachment overlay MUST support paste-from-clipboard path.
- AZ-FR-1603: Attachment overlay MUST support file-path input path.
- AZ-FR-1604: Detail panel MUST list attachments for issue.
- AZ-FR-1605: Detail panel MUST support attachment selection navigation.
- AZ-FR-1606: `v` on selected attachment MUST open preview.
- AZ-FR-1607: `o` on selected attachment MUST open system viewer.
- AZ-FR-1608: `x` on selected attachment MUST remove attachment.
- AZ-FR-1609: Attachments MUST be indexed and linked to issue IDs.
- AZ-FR-1610: Session start prompts that include attachment paths (`Space S`, `Space !`) MUST materialize and reference those files inside the target issue worktree path, not a user-global or sibling project path.

## 4.19 Settings Requirements

- AZ-FR-1701: `s` MUST open settings overlay.
- AZ-FR-1702: Settings overlay MUST support keyboard navigation.
- AZ-FR-1703: Settings overlay MUST support toggle/cycle edits.
- AZ-FR-1704: Setting changes MUST persist to local config file.
- AZ-FR-1705: `e` in settings MUST open raw config editor.
- AZ-FR-1706: Configuration reload SHOULD apply changes without restart when safe.

## 4.20 Multi-Project Requirements

- AZ-FR-1801: The app MUST support multiple registered projects.
- AZ-FR-1802: `g p` MUST open project selector.
- AZ-FR-1803: Project switch MUST reload board against selected project.
- AZ-FR-1804: Startup SHOULD auto-select project by cwd/default fallback order.
- AZ-FR-1805: Project metadata MUST persist in global registry config.

## 4.21 Status Bar and Overlay Requirements

- AZ-FR-1901: Status bar MUST remain visible in board context.
- AZ-FR-1902: Status bar MUST show current mode.
- AZ-FR-1903: Status bar SHOULD show connectivity state.
- AZ-FR-1904: Status bar SHOULD show quick key hints.
- AZ-FR-1905: Help overlay (`?`) MUST exist and be dismissible quickly.
- AZ-FR-1906: Logs overlay (`L`) MUST provide exit path.
- AZ-FR-1907: Toast system SHOULD present operation feedback.

## 4.22 tmux Interop Requirements

- AZ-FR-2001: Session naming MUST be deterministic from issue ID.
- AZ-FR-2001a: Session naming MUST include deterministic project prefix plus issue identity (for example `az-b`, `ch-a`) to avoid cross-project collisions.
- AZ-FR-2002: App MUST discover existing relevant tmux sessions.
- AZ-FR-2002a: When project context is known, tmux session discovery MUST ignore project-prefixed sessions belonging to other projects (for example ignore `ch-f` while scoped to Azedarach).
- AZ-FR-2003: App SHOULD support return-to-board tmux key convention.
- AZ-FR-2004: App SHOULD support AI<->dev-server toggle tmux key convention.

## 4.23 Command Execution and Safety Requirements

- AZ-FR-2101: CLI command failures MUST be surfaced to user.
- AZ-FR-2102: Long-running operations SHOULD show progress/loading affordance.
- AZ-FR-2103: Destructive actions MUST require explicit confirmation or safe mode.
- AZ-FR-2104: Shell command invocation MUST avoid interactive hangs where possible.
- AZ-FR-2105: Operations that modify tracker/git state MUST be idempotent where feasible.

## 4.24 Reliability Requirements

- AZ-FR-2201: UI MUST not crash on missing optional dependencies.
- AZ-FR-2202: Missing mandatory dependencies MUST produce startup diagnostics.
- AZ-FR-2203: Partial subsystem failure SHOULD preserve board navigation.
- AZ-FR-2204: Interrupted operations SHOULD be resumable or abortable.
- AZ-FR-2205: Data refresh errors SHOULD not wipe last known good board state.

## 4.25 Performance Requirements

- AZ-FR-2301: Common keypress handling SHOULD complete within interactive latency budgets.
- AZ-FR-2302: Board refresh SHOULD scale to large issue counts without lockups.
- AZ-FR-2303: Overlay open/close SHOULD feel immediate.
- AZ-FR-2304: Search/filter updates SHOULD provide near-live feedback.

## 4.26 Observability Requirements

- AZ-FR-2401: Operations SHOULD emit structured logs.
- AZ-FR-2402: User-visible errors SHOULD include failed operation context.
- AZ-FR-2403: Session state transitions SHOULD be loggable.
- AZ-FR-2404: Merge and PR operations SHOULD include audit trail metadata.

## 4.27 Security and Privacy Requirements

- AZ-FR-2501: App MUST avoid exposing secrets in logs/toasts.
- AZ-FR-2502: Local config edits MUST avoid touching unrelated credential files.
- AZ-FR-2503: Attachment storage MUST remain project-local unless explicitly exported.
- AZ-FR-2504: External command execution MUST use explicit argument boundaries.

## 4.28 Internationalization and Terminal Constraints

- AZ-FR-2601: Core functionality MUST operate with ASCII-only keybindings.
- AZ-FR-2602: UI SHOULD tolerate terminals with limited glyph support.
- AZ-FR-2603: Icon-only states SHOULD have text fallback in detail/help surfaces.

## 4.29 Requirement Mapping Notes

- Interaction requirements map to Section 02.
- Workflow requirements map to Section 03.
- Failure handling requirements map to Section 05.
- Acceptance validation maps to Section 06.

## 4.30 Startup, Re-entry, and Shutdown Requirements

- AZ-FR-2701: Startup MUST validate mandatory external tool availability and project compatibility.
- AZ-FR-2702: Missing mandatory tooling MUST disable dependent actions while preserving board navigation where possible.
- AZ-FR-2703: App SHOULD restore last known view mode on restart per project.
- AZ-FR-2704: App SHOULD restore active search/filter/sort profile on restart per project.
- AZ-FR-2705: App SHOULD attempt to restore focus to last focused issue when still present.
- AZ-FR-2706: If restored issue focus is invalid, app MUST fall back to nearest valid focus target.
- AZ-FR-2707: Normal app exit MUST persist UI context without stopping active sessions unless explicitly requested.
- AZ-FR-2708: Shutdown MUST restore terminal state cleanly (input echo, cursor, alternate screen semantics as applicable).

## 4.31 Concurrency and External Mutation Requirements

- AZ-FR-2801: Save operations in edit/create flows MUST detect stale revision conflicts before applying writes.
- AZ-FR-2802: On stale conflict, user MUST be offered reload, overwrite, or cancel choices.
- AZ-FR-2803: Draft text SHOULD be preserved across stale conflict handling unless user discards.
- AZ-FR-2804: Background refresh MUST not discard active overlay input state.
- AZ-FR-2805: Selection and focus reconciliation after refresh MUST avoid selecting non-existent issue IDs.
- AZ-FR-2806: Tracker lock contention MUST surface explicit lock/busy feedback.
- AZ-FR-2807: Read operations MAY auto-retry with bounded strategy on transient tracker locks.
- AZ-FR-2808: Mutating operations MUST avoid duplicate submission under retry conditions.

## 4.32 Terminal Compatibility and Layout Safety Requirements

- AZ-FR-2901: App MUST remain navigable at constrained terminal sizes using truncation and scroll affordances.
- AZ-FR-2902: Overlays MUST remain dismissible at all supported terminal dimensions.
- AZ-FR-2903: Status bar MUST degrade to compact form rather than overlapping content when width is limited.
- AZ-FR-2904: Long titles/paths MUST truncate deterministically and preserve issue identity visibility.
- AZ-FR-2905: Color-independent textual state cues SHOULD be present for critical status indicators.

## 4.33 Consistency and Idempotence Requirements

- AZ-FR-3001: Multi-step actions (session start, cleanup, PR create) MUST be resumable or safely repeatable after interruption.
- AZ-FR-3002: User-visible success messages MUST only appear after all required sub-steps complete.
- AZ-FR-3003: Partial-success operations MUST report completed and failed sub-steps separately.
- AZ-FR-3004: Action menus SHOULD suppress commands that are impossible in current context.

## 4.34 Destructive Action Guardrail Requirements

- AZ-FR-3101: Destructive operations MUST present an impact preview before confirmation.
- AZ-FR-3102: Preflight impact preview MUST include concrete target count and operation scope.
- AZ-FR-3103: Canceling destructive preflight MUST guarantee zero side effects.
- AZ-FR-3104: Destructive flows MUST revalidate targets immediately before execution.

## 4.35 Divergence and Reconciliation Requirements

- AZ-FR-3201: Non-fast-forward and remote divergence errors MUST surface guided remediation.
- AZ-FR-3202: Retried operations after reconciliation SHOULD continue original workflow intent automatically.
- AZ-FR-3203: Session indicator and tmux discovery mismatches MUST trigger reconciliation behavior.
- AZ-FR-3204: Reconciliation actions (adopt/clear/terminate orphan session) MUST be explicit and auditable.

## 4.36 Determinism and Ordering Requirements

- AZ-FR-3301: Sort operations MUST use deterministic secondary tie-breakers.
- AZ-FR-3302: Missing sort-field values MUST not cause unstable reordering.
- AZ-FR-3303: Timestamp/clock anomalies SHOULD show consistency hints without blocking work.
- AZ-FR-3304: Consecutive refreshes with unchanged data MUST preserve visible ordering.

## 4.37 Dependency Graph Requirements

- AZ-FR-3401: The system MUST support general issue dependency graphs, not only epic parent/child relationships.
- AZ-FR-3402: Dependency edges MUST be typed and preserve tracker-native semantics.
- AZ-FR-3403: Issue detail MUST expose incoming and outgoing dependency edges.
- AZ-FR-3404: UI MUST distinguish at least blockers, blocked-by, and lineage/discovery-style relations when present.
- AZ-FR-3405: Dependency create/update flows MUST validate target issue existence before persist.
- AZ-FR-3406: Duplicate dependency edges of same type and endpoints SHOULD be prevented.
- AZ-FR-3407: Dependency changes MUST refresh blocked/readiness indicators consistently.
- AZ-FR-3408: Dependency removal MUST require explicit confirmation when it can unblock/retarget workflow.
- AZ-FR-3409: Planning/fork flows MUST preserve and/or create non-hierarchical dependencies when specified by user intent.
- AZ-FR-3410: Cyclic dependency validation MUST follow tracker policy and surface actionable feedback when rejected.

## 4.38 Runtime Branch-Origin Selection Requirements

- AZ-FR-3501: Branch creation flows MUST provide runtime choice of origin branch when creating a missing issue branch.
- AZ-FR-3502: Branch-origin options MUST include configured base branch.
- AZ-FR-3503: Branch-origin options MUST include eligible upstream-related issue branches when available.
- AZ-FR-3504: If multiple eligible upstream sources exist, user MUST explicitly choose source issue.
- AZ-FR-3505: If no eligible upstream source exists, UI MUST provide clear reason and allow base-branch path.
- AZ-FR-3506: Runtime branch-origin chooser MUST appear for any branch-creation event, including branch recreate after loss/invalidation.

## 4.39 Upstream Follow-On Merge Requirements

- AZ-FR-3601: The UI MUST provide a low-friction path to merge eligible upstream-related work into the current issue branch directly.
- AZ-FR-3602: Upstream follow-on merge MUST operate source issue branch -> target issue branch without requiring merge through base branch.
- AZ-FR-3603: Follow-on merge action MUST be available from dependency-aware context (detail and relationship-scoped drill-down).
- AZ-FR-3604: Follow-on merge MUST verify selected source is upstream of target per relation-direction rules.
- AZ-FR-3605: Follow-on merge SHOULD enforce relation-type readiness policy with explicit guidance on rejection.
- AZ-FR-3606: When multiple upstream sources exist, user MUST be able to choose which source to merge from.
- AZ-FR-3607: Follow-on merge conflicts MUST preserve recoverable repository state and provide abort/retry guidance.
- AZ-FR-3608: Successful follow-on merge MUST refresh dependency/blocking indicators on affected issues.
- AZ-FR-3609: Upstream eligibility MUST follow a normative relation-direction table.
- AZ-FR-3610: Readiness policy MUST allow `in_progress` sources for selected relation types where partial integration is valid.
- AZ-FR-3611: Multi-upstream flows SHOULD provide deterministic suggested merge order while preserving explicit user source choice.

## 4.40 Relationship Representation Requirements

- AZ-FR-3701: Main board cards SHOULD expose compact relationship summary chips for upstream/downstream/blocking counts.
- AZ-FR-3702: Relationship summaries MUST avoid hiding hard-blocked state.
- AZ-FR-3703: Issue detail MUST expose typed incoming/outgoing dependency lists with relation direction.
- AZ-FR-3704: Drill-down MUST support relation-scope switching beyond child-only view when requested.
- AZ-FR-3705: Switching relationship scopes MUST preserve deterministic focus and action behavior.

## 4.41 Optimistic Mutation Requirements

- AZ-FR-3801: Issue create/edit/move/status mutations SHOULD use optimistic in-memory updates.
- AZ-FR-3802: Optimistic mutations MUST mark pending state visibly until persistence resolves.
- AZ-FR-3803: On persistence failure, optimistic mutations MUST rollback to last confirmed state.
- AZ-FR-3804: Rollback events MUST produce actionable user feedback with failed operation context.
- AZ-FR-3805: Optimistic dependency mutations (add/remove/update) MUST obey the same rollback contract.
- AZ-FR-3806: Optimistic fork metadata creation MUST rollback cleanly on persistence failure.
- AZ-FR-3807: Rollback logic MUST be scoped to affected entities and MUST NOT revert unrelated successful changes.
- AZ-FR-3808: Linear tracker data MUST be treated as source of truth for hydration.
- AZ-FR-3809: Hydration polling MUST reconcile external changes without clobbering pending optimistic updates.
- AZ-FR-3810: Selected optimistic flows MAY enter retryable-pending state instead of immediate rollback when safe and user-visible.
- AZ-FR-3811: Backend sync requests MUST be processed through a bounded-rate queue with configurable sustained rate and burst allowance.
- AZ-FR-3812: Equivalent in-flight backend sync requests MUST be deduplicated and MUST NOT be dropped silently.
- AZ-FR-3813: Read operations SHOULD support bounded wait budgets and MUST return a clear stale/freshness hint when timeout occurs before sync completion.
- AZ-FR-3814: Explicit wait mode for reads MUST allow a higher wait budget than default non-blocking read mode.

## 4.42 Background Operation Requirements

- AZ-FR-3901: Long-running operations (session start/resume, update from base branch, merge, PR create, cleanup) MUST execute as trackable background operations.
- AZ-FR-3902: Each background operation MUST have a unique operation ID and lifecycle state (queued/running/succeeded/failed/canceled).
- AZ-FR-3903: Users MUST be able to inspect operation progress and current step while board interaction continues.
- AZ-FR-3904: Operations SHOULD expose cancellation where phases are safely interruptible.
- AZ-FR-3905: Cancel requests MUST provide deterministic completion semantics (canceled or non-cancelable in current phase).
- AZ-FR-3906: Background operation completion MUST emit user-visible result summaries.
- AZ-FR-3907: Failed background operations MUST include root cause context and retry guidance.
- AZ-FR-3908: Async actions that are not required to block immediate interaction SHOULD execute as background operations.
- AZ-FR-3909: Operation probe/status SHOULD expose phase-level cancelability as stretch-goal metadata.

## 4.43 Machine-Readable State Probe Requirements

- AZ-FR-4001: The system MUST expose a machine-readable state probe for automation.
- AZ-FR-4002: Probe output MUST be side-effect free.
- AZ-FR-4003: Probe payload MUST include active mode, focus identity, view type, and active overlays.
- AZ-FR-4004: Probe payload MUST include visible card identities and critical card indicators (session, PR, dependency summary).
- AZ-FR-4005: Probe payload MUST include operation queue states and recent user-visible errors.
- AZ-FR-4006: Probe schema MUST be versioned and include schema version in every response.
- AZ-FR-4007: Probe snapshots MUST include monotonic revision or timestamp suitable for ordering assertions.
- AZ-FR-4008: Probe access MUST be available in non-interactive/headless test environments.

## 4.44 E2E Testability Requirements

- AZ-FR-4101: Acceptance scenarios MUST be translatable into deterministic E2E scripts without undefined manual judgment.
- AZ-FR-4102: The spec MUST define canonical fixture profiles for smoke, integration, and scale test datasets.
- AZ-FR-4103: Fixture profiles MUST include dependency-graph variants (single upstream, multi-upstream, hierarchy + non-hierarchy mix).
- AZ-FR-4104: Test profile MUST define terminal dimensions and support verification on at least narrow and standard widths.
- AZ-FR-4105: Test profile MUST define base-branch variability (non-default names) for git workflow assertions.
- AZ-FR-4106: Every MUST requirement in this section set MUST map to at least one acceptance scenario.
- AZ-FR-4107: E2E suite SHOULD include full-screen visual snapshot assertions in deterministic terminal profiles.
- AZ-FR-4108: E2E suite MUST include performance assertions for representative critical flows.
- AZ-FR-4109: E2E suite MUST include stress scenarios for scale datasets and rapid operation concurrency.
- AZ-FR-4110: E2E release validation SHOULD combine probe assertions with visual assertions for high-risk workflows.
