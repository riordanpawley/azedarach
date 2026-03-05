# 05 - Edge Cases and Failure Spec

## 5.1 Goals

Define mandatory behavior for degraded conditions so users can recover quickly without losing context.

## 5.2 Failure Handling Principles

- Fail visibly.
- Preserve user context (focus, selection, active mode) when safe.
- Prefer reversible operations.
- Provide concrete next step in every error message.

## 5.3 Data Source Failures (Local Store and Sync Adapters)

### Case F-001: Local issue store unavailable

- Symptom: local issue load/update commands fail immediately.
- Required behavior:
  - show blocking diagnostic explaining local store initialization/open failure.
  - keep app responsive for read-only or setup actions where possible.

### Case F-002: Sync adapter returns malformed payload

- Required behavior:
  - reject malformed payload safely.
  - log parse context.
  - preserve last good board state.

### Case F-003: Permission denied on local store path

- Required behavior:
  - show remediation hint (permissions/path).
  - disable mutating actions until resolved.

## 5.4 tmux / Session Failures

### Case F-010: Session start command succeeds but tmux session missing

- Required behavior:
  - validate session existence after start.
  - surface mismatch and offer retry.

### Case F-011: Attach requested but no session found

- Required behavior:
  - show non-blocking toast with issue ID and expected session name.
  - keep board focus unchanged.

### Case F-012: Pause requested for already stopped session

- Required behavior:
  - no-op with informational feedback.

### Case F-013: Resume requested but execution binary unavailable

- Required behavior:
  - transition to error state.
  - provide setup command hint.

## 5.5 Worktree and Git Failures

### Case F-020: Worktree path already exists but is invalid

- Required behavior:
  - detect inconsistency.
  - offer cleanup/recreate or manual inspect option.

### Case F-021: Branch creation fails due to naming conflict

- Required behavior:
  - detect existing branch target.
  - reuse compatible branch or request explicit override.

### Case F-022: Merge conflict in update-from-base-branch

- Required behavior:
  - show conflict state.
  - expose conflict resolution path and abort action.

### Case F-023: Abort merge invoked with no active merge

- Required behavior:
  - safe no-op with status message.

### Case F-024: Merge to base branch fails mid-operation

- Required behavior:
  - keep repository in explicit known state (conflict or aborted).
  - provide exact next command guidance.

### Case F-025: Diff tool unavailable

- Required behavior:
  - fallback to plain git diff view.
  - notify user of reduced output mode.

## 5.6 PR and Network Failures

### Case F-030: Network offline during PR creation

- Required behavior:
  - detect and report offline state.
  - keep local branch/worktree unchanged.
  - allow retry once online.

### Case F-031: Authentication expired for PR host

- Required behavior:
  - surface auth-specific remediation steps.

### Case F-032: PR already exists

- Required behavior:
  - avoid duplicate creation.
  - surface existing PR URL.

### Case F-033: Push rejected (non-fast-forward)

- Required behavior:
  - show explicit rejection reason.
  - provide update-from-base-branch/resolution path.

## 5.7 UI State Edge Cases

### Case F-040: Focus points to issue removed by refresh

- Required behavior:
  - move focus to nearest valid card in same column, else first available card.

### Case F-041: Active selections contain hidden items after filter change

- Required behavior:
  - preserve selection by ID but clearly indicate hidden count OR clear with warning (must be consistent by profile).

### Case F-042: Overlay opens while another modal pending

- Required behavior:
  - enforce one active primary modal stack.
  - prevent orphaned mode tags.

### Case F-043: Terminal resize during overlay rendering

- Required behavior:
  - recalculate layout.
  - avoid clipped, non-dismissible overlays.

## 5.8 Search/Filter/Sort Edge Cases

### Case F-050: Empty result set after filter

- Required behavior:
  - show explicit empty-state message with clear-filters hint.

### Case F-051: Invalid unicode/path input in attachment path mode

- Required behavior:
  - reject input safely.
  - keep overlay open for correction.

### Case F-052: Repeated sort key spam

- Required behavior:
  - deterministic toggle behavior.
  - no race-induced order corruption.

## 5.9 Attachment Edge Cases

### Case F-060: Clipboard contains non-image payload

- Required behavior:
  - reject with message "clipboard has no supported image".

### Case F-061: File path exists but unsupported format

- Required behavior:
  - reject and show supported format list.

### Case F-062: Attachment metadata index corrupt

- Required behavior:
  - isolate damaged entries.
  - continue rendering unaffected attachments.
  - provide repair guidance.

### Case F-063: External viewer launch unavailable

- Required behavior:
  - show path so user can open manually.

## 5.10 Planning Edge Cases

### Case F-070: Planning session times out

- Required behavior:
  - mark planning as failed with timeout reason.
  - retain prompt input for retry.

### Case F-071: Planning creates partial tasks then fails

- Required behavior:
  - show created IDs and failed steps.
  - provide cleanup/retry instructions.

### Case F-072: Planning suggests invalid dependency graph

- Required behavior:
  - validate dependency structure before commit.
  - reject cyclic or malformed graph updates.

## 5.11 Multi-Project Edge Cases

### Case F-080: Current project path deleted/moved

- Required behavior:
  - show invalid project warning.
  - offer switch to next valid registered project.

### Case F-081: Project selector key out of range

- Required behavior:
  - ignore key with non-disruptive feedback.

### Case F-082: Auto-detect finds multiple candidate projects

- Required behavior:
  - choose deterministic precedence or prompt selector.

### Case F-083: Selected project store directory/DB missing

- Required behavior:
  - initialize `<project-root>/.azedarach/` and canonical SQLite DB if absent.
  - continue with empty/local baseline board state without crashing.
  - show non-blocking feedback that project-local store was initialized.

## 5.12 Bulk Operation Edge Cases

### Case F-090: Bulk stop includes tasks without sessions

- Required behavior:
  - skip inapplicable tasks and report counts.

### Case F-091: Bulk cleanup partially fails

- Required behavior:
  - produce per-task result summary.
  - leave successful tasks cleaned.

### Case F-092: Bulk move crosses invalid state transitions

- Required behavior:
  - apply valid transitions; report blocked items.

### Case F-093: Hidden selection grows after filter/sort changes

- Required behavior:
  - keep selection deterministic by ID.
  - expose hidden-selection count and provide explicit clear path.

### Case F-094: Bulk target drift between preview and execution

- Required behavior:
  - freeze target IDs at execution start.
  - report skipped/drifted IDs with reason codes.

### Case F-095: Invert-visible selection on sparse mixed tombstoned data

- Required behavior:
  - invert only visible non-tombstoned IDs.
  - preserve non-visible selected IDs unless explicitly cleared.

## 5.13 Safety Confirmation Requirements

The following operations MUST include confirmation or explicit two-step UX:

- merge to base branch when conflicts likely
- full cleanup that closes issues
- deleting attachments
- branch/worktree destructive cleanup on active sessions

## 5.14 Degradation Matrix

| Subsystem Down | Must Still Work | Degraded |
|---|---|---|
| tmux | board browse, filter, sort | start/attach session actions disabled |
| git | board browse, non-git issue edits | worktree/merge/pr actions disabled |
| network | local board and local git ops | PR/open-remote actions disabled |
| image preview backend | attachment list and open external | inline preview disabled |
| system notifications | in-app toasts | OS notifications disabled |

## 5.15 Error Message Contract

Every user-visible error SHOULD include:

- what failed
- target context (issue ID/project)
- likely cause (if known)
- single recommended next action

Example format:

`Failed to create PR for <issue-id>: authentication required. Run gh auth login, then retry Space+P.`

## 5.16 Recovery Checklist (User-Facing)

Minimum quick-recovery actions to surface in help/docs:

1. redraw (`Ctrl-l`)
2. clear filters (`f` -> `c`)
3. exit modes (`Esc`)
4. abort merge (`Space M`)
5. reattach session (`Space a`)
6. restart dev server (`Space Ctrl-r`)
7. reopen project selector (`g p`)

## 5.17 State Consistency Rules

- Issue state changes MUST be transactional at command level.
- Session indicators MUST not claim running state without session existence.
- PR indicator MUST not show available/open if URL metadata missing.
- Cleanup MUST not report success unless filesystem/git operations succeeded.

## 5.18 Logging Requirements for Failure Analysis

On failure, logs SHOULD capture:

- operation name
- issue ID
- project path/name
- invoked command (sanitized)
- exit code/stdout/stderr snippets (sanitized)
- timestamp

## 5.19 Startup and Shutdown Edge Cases

### Case F-100: Mandatory dependency missing at startup

- Required behavior:
  - startup completes into diagnostics-capable shell, not hard crash.
  - disable only affected action families.

### Case F-101: Last active project no longer exists

- Required behavior:
  - fall back to deterministic project selection order.
  - show one-time notice of fallback reason.

### Case F-102: Corrupt persisted UI state file

- Required behavior:
  - ignore invalid persisted state.
  - boot with defaults and emit repair guidance.

### Case F-103: Exit requested while async operation in flight

- Required behavior:
  - either complete operation atomically or cancel safely before process exit.
  - never leave terminal in broken/raw state.

## 5.20 Concurrency and Mutation Edge Cases

### Case F-110: Issue edited externally while local edit overlay open

- Required behavior:
  - detect stale revision at save time.
  - present reload/overwrite/cancel options.
  - preserve local draft unless explicitly discarded.

### Case F-111: Background refresh removes selected issues mid-bulk action

- Required behavior:
  - revalidate target IDs before action execution.
  - skip missing IDs with per-item status report.

### Case F-112: Local store lock contention on write

- Required behavior:
  - do not spin indefinitely.
  - surface lock owner/context when available.
  - allow explicit user retry.

### Case F-113: Duplicate command invocation from key repeat

- Required behavior:
  - deduplicate idempotent operation requests within safety window.
  - prevent duplicate PR creation and duplicate cleanup calls.

## 5.21 Terminal and Rendering Edge Cases

### Case F-120: Terminal width too narrow for full status line

- Required behavior:
  - collapse to compact status representation.
  - keep mode tag visible at all times.

### Case F-121: Extremely long titles/paths overflow card/panel

- Required behavior:
  - deterministic truncation with ellipsis.
  - preserve issue ID and actionable controls.

### Case F-122: Terminal reports no color support

- Required behavior:
  - rely on text/icon fallbacks.
  - avoid color-only meaning for critical states.

### Case F-123: Rapid resize oscillation during redraw

- Required behavior:
  - coalesce redraws.
  - avoid mode/tag desynchronization or stuck overlays.

## 5.22 Destructive Preflight and Target Drift Edge Cases

### Case F-130: Target set changes between preflight and execute

- Required behavior:
  - revalidate target IDs at execution time.
  - report dropped or newly invalid targets before continuing.

### Case F-131: User confirms full cleanup while a selected issue gains active session

- Required behavior:
  - detect active-session conflict during revalidation.
  - require explicit override or skip conflicting issue.

### Case F-132: Confirmation dialog loses focus due to redraw

- Required behavior:
  - preserve explicit default on cancel-safe option.
  - never execute destructive action without deliberate confirm keypath.

## 5.23 Divergence and Reconciliation Edge Cases

### Case F-140: Push rejected due to remote rewritten history

- Required behavior:
  - surface divergence reason without suggesting destructive force actions by default.
  - provide safe sync/retry path.

### Case F-141: Session marked running but tmux session missing

- Required behavior:
  - clear stale running indicator after verification.
  - log reconciliation event with issue context.

### Case F-142: Orphan tmux session with no matching issue metadata

- Required behavior:
  - expose adopt/terminate options.
  - avoid auto-terminating without user choice.

## 5.24 Ordering and Time Anomaly Edge Cases

### Case F-150: Missing updated timestamp on subset of issues

- Required behavior:
  - place items deterministically using fallback order.
  - keep ordering stable across refreshes.

### Case F-151: Clock skew causes future timestamps

- Required behavior:
  - avoid jittery reordering loops.
  - show non-blocking consistency hint.

### Case F-152: Equal sort keys across many items

- Required behavior:
  - apply deterministic tie-breakers.
  - preserve navigation predictability.

## 5.25 Dependency Graph Edge Cases

### Case F-160: Dependency edge references issue not visible in current board scope

- Required behavior:
  - keep edge information visible in detail context.
  - provide navigation hint to locate referenced issue.

### Case F-161: Dependency target issue deleted or inaccessible

- Required behavior:
  - mark edge as unresolved with clear remediation guidance.
  - do not crash or silently drop relationship metadata.

### Case F-162: Duplicate edge create attempt

- Required behavior:
  - detect duplicate endpoint+type combination.
  - no-op safely with explicit feedback.

### Case F-163: Cycle introduced in disallowed dependency type

- Required behavior:
  - reject write with actionable cycle diagnostics.
  - preserve pre-operation graph state.

## 5.26 Upstream Follow-On Merge Edge Cases

### Case F-170: User attempts follow-on merge from source that is not ready

- Required behavior:
  - block action with explicit completion prerequisite guidance.

### Case F-171: Selected source issue is not upstream of target by relation direction

- Required behavior:
  - reject with clear relation-type mismatch message.
  - suggest selecting a valid upstream dependency.

### Case F-177: Relation type has ambiguous direction interpretation

- Required behavior:
  - apply normative relation-direction table.
  - log relation type and evaluated direction in diagnostics.

### Case F-172: Multiple upstream sources merged in conflicting order

- Required behavior:
  - require explicit source choice per merge invocation.
  - preserve recoverable target state and conflict instructions.

### Case F-173: Follow-on merge updates code but unresolved upstream dependencies remain

- Required behavior:
  - keep issue in blocked/dependency-unsatisfied state when required.
  - display remaining unsatisfied upstream count/list.

### Case F-174: Branch-origin chooser has no eligible upstream source branches

- Required behavior:
  - show base-branch path as valid default.
  - explain why upstream-origin options are unavailable.

### Case F-175: Parent drill-down fork uses wrong default source

- Required behavior:
  - preselect parent as upstream source when creating child from parent drill-down.
  - allow explicit override before create.

### Case F-176: Suggested merge order changes nondeterministically between refreshes

- Required behavior:
  - keep deterministic ordering heuristic stable across unchanged inputs.
  - allow explicit manual override regardless of suggestion order.

## 5.27 Optimistic Mutation Edge Cases

### Case F-180: Optimistic issue move accepted locally but local commit fails

- Required behavior:
  - rollback issue to prior status lane.
  - show failure toast with retry action.

### Case F-183: Local commit succeeds but outbound sync fails

- Required behavior:
  - keep local canonical state unchanged.
  - show retryable sync failure diagnostics without forcing rollback.
  - preserve pending sync queue entry for background/manual retry.

### Case F-181: Optimistic edit conflicts with concurrent external edit

- Required behavior:
  - rollback local optimistic fields.
  - present stale-write guidance and preserve draft where possible.

### Case F-182: Partial optimistic dependency batch succeeds/fails mixed

- Required behavior:
  - rollback failed edges only.
  - keep successful edges and report per-edge result.

## 5.28 Background Operation Edge Cases

### Case F-190: User cancels operation during non-cancelable phase

- Required behavior:
  - return deterministic "cannot cancel now" status.
  - keep operation progress visible.

### Case F-191: App restarts while background operations were running

- Required behavior:
  - recover operation states where possible.
  - mark unknown outcomes explicitly and provide reconcile actions.

### Case F-192: Two conflicting background operations launched on same issue

- Required behavior:
  - enforce operation serialization or explicit conflict rejection.
  - avoid repository corruption.

### Case F-193: Async action incorrectly blocks board interaction

- Required behavior:
  - migrate action to background execution path when non-blocking by policy.
  - preserve interactive navigation during execution.

## 5.29 Probe and E2E Harness Edge Cases

### Case F-200: Probe returns inconsistent snapshot during rapid UI updates

- Required behavior:
  - provide atomically consistent snapshot view.
  - include revision/timestamp for ordering.

### Case F-201: Probe unavailable in headless environment

- Required behavior:
  - fail with actionable setup diagnostics.
  - avoid silent fallback to partial payloads.

### Case F-202: Probe schema mismatch with test harness expectation

- Required behavior:
  - include schema version in response.
  - provide compatibility failure reason.

### Case F-203: Visual snapshot drift due to terminal profile mismatch

- Required behavior:
  - annotate snapshot artifacts with terminal profile metadata.
  - fail with clear profile mismatch diagnostics before comparing baselines.

### Case F-204: Linear webhook delivery delay or outage

- Required behavior:
  - keep board fully functional from local canonical state.
  - show stale-external-sync indicator with last successful sync/event timestamp.
  - allow manual sync trigger while webhook path is degraded.

### Case F-205: Linear outbound rate limit exceeded

- Required behavior:
  - keep local canonical commits successful and queue outbound sync work for retry.
  - enforce internal throttling ceiling and burst policy deterministically.
  - surface retry/backlog diagnostics (queued count and next eligible dispatch window).

## 5.30 Top-Level Az CLI Edge Cases

### Case F-206: `az show/update/close/reopen/delete` targets missing issue

- Required behavior:
  - return deterministic not-found diagnostics with issue ID context.
  - return non-zero exit and machine-readable error payload when JSON mode is requested.
  - avoid partial local mutations.

### Case F-207: `az update/close/reopen/delete` runs while canonical store is unavailable/locked

- Required behavior:
  - fail with actionable diagnostics including project/canonical DB context.
  - preserve canonical local state and avoid fallback to remote tracker as runtime source of truth.
  - provide explicit retry guidance once lock/unavailability clears.

### Case F-208: Session bootstrap prompt references backend-specific issue CLI

- Required behavior:
  - reject or normalize prompt template to top-level `az` command contract before session launch.
  - expose diagnostics for prompt template mismatch.
  - continue allowing backend adapters internally without leaking backend-specific instructions to agents.

### Case F-209: Title-hash ID strategy generates collision

- Required behavior:
  - resolve collision deterministically without requiring manual DB intervention.
  - preserve configured strategy constraints (for example lowercase alphabetic hash policy).
  - return actionable diagnostics only if collision cannot be resolved automatically.

### Case F-210: `az dep add/remove/list/tree/cycles` receives invalid issue references or graph state

- Required behavior:
  - dependency mutations reject missing source/target issue IDs with deterministic diagnostics.
  - cycle detection output remains deterministic for identical graph inputs.
  - dependency tree/list operations fail clearly if canonical store is unavailable.

### Case F-211: `az config validate/show` called with invalid or incompatible configuration payload

- Required behavior:
  - `az config validate` returns schema-path-specific errors without mutating runtime config.
  - `az config show` returns effective config snapshot or explicit unavailable diagnostics.
  - both commands support deterministic JSON error/success payloads when requested.

### Case F-212: `az stats` on empty or partially hydrated project data

- Required behavior:
  - return deterministic zero-safe aggregates for empty datasets.
  - avoid blocking on optional sync targets; stats are sourced from canonical local state.
  - include freshness/backlog hints when statistic inputs are still converging.

### Case F-213: `az list/ready/blocked/search/stale/count` runs while canonical store is unavailable/locked

- Required behavior:
  - fail with actionable diagnostics including project/canonical DB context.
  - return deterministic non-zero exit in machine-readable mode.
  - avoid returning partial/ambiguous query payloads.

### Case F-214: `az project add` receives invalid path or duplicate project name

- Required behavior:
  - reject non-existent or non-readable paths with explicit remediation guidance.
  - reject duplicate registration/name collisions deterministically.
  - avoid mutating existing project registry entries on failed add attempts.

### Case F-215: `az project remove/switch` targets unknown project

- Required behavior:
  - return deterministic unknown-project diagnostics with available next actions.
  - avoid changing current/default project state on failed switch/remove.
  - keep TUI project selector state consistent with persisted registry after failures.

### Case F-216: Command namespace ambiguity between issue list and project list

- Required behavior:
  - `az list` resolves issue-query semantics deterministically.
  - `az project list` resolves registry semantics deterministically.
  - diagnostics and help text must disambiguate list-command scope when user intent is ambiguous.
