# 06 - Acceptance Catalog

This catalog defines validation scenarios for requirements in Section 04.

## 6.1 Scenario Format

- ID: `AZ-AT-####`
- Preconditions
- Steps
- Expected Results
- Requirement Links

ID policy:

- Scenario IDs are stable once published and MAY be non-contiguous.
- Existing IDs MUST NOT be renumbered solely for ordering aesthetics.

Automation-ready scenario fields (normative for E2E authoring):

- Fixture Profile: canonical seed profile name and variant
- Setup: deterministic setup commands or harness actions
- Input Sequence: exact key/action sequence
- Assertions: machine-checkable assertions (mode/focus/visible entities/indicators/results)
- Probe Expectations: required machine-readable probe fields used for assertions
- Teardown: cleanup/reset steps

Canonical fixture profile names:

- `smoke`
- `integration`
- `scale`
- `conflict`
- `network-failure`
- `orphan-session`

## 6.2 Board and Navigation Acceptance

### AZ-AT-0001 Board loads issues by status

- Preconditions: tracker has issues in all major statuses.
- Steps: open app.
- Expected: board renders columns with issues in corresponding status lanes.
- Links: AZ-FR-0001, AZ-FR-0002.

### AZ-AT-0002 Focus navigation with hjkl

- Steps: press `h/j/k/l` in board.
- Expected: focus moves deterministically; no crash at boundaries.
- Links: AZ-FR-0201, AZ-FR-0206.

### AZ-AT-0003 Half-page scroll

- Preconditions: long column list.
- Steps: press `Ctrl-Shift-d` then `Ctrl-Shift-u`.
- Expected: view scrolls approximately half page each command.
- Links: AZ-FR-0204.

### AZ-AT-0004 Redraw integrity

- Steps: resize terminal, then press `Ctrl-l`.
- Expected: full repaint with intact status bar and focus.
- Links: AZ-FR-1901.

### AZ-AT-2812 Board baseline rendering contract

- Preconditions: fixture includes at least one issue per major status and one empty column case.
- Steps: launch app; observe initial board; trigger manual refresh.
- Expected: cards show ID/title/priority, focus anchor exists, empty columns remain stable, and loading state appears when data is unavailable.
- Links: AZ-FR-0003, AZ-FR-0004, AZ-FR-0006, AZ-FR-0007, AZ-FR-0008, AZ-FR-0010, AZ-FR-0101.

### AZ-AT-2813 Navigation reconciliation after refresh

- Preconditions: overflowing column and active overlay open/close cycle.
- Steps: navigate in long column, open/close transient overlay, trigger data refresh.
- Expected: virtual-scroll navigation remains valid, focus resolves to an existing card after refresh, and overlay cycle does not corrupt focus.
- Links: AZ-FR-0203, AZ-FR-0205, AZ-FR-0207.

## 6.3 Mode Acceptance

### AZ-AT-0101 Mode indicator updates

- Steps: enter each mode (`Space`, `g`, `v`, `/`, `f`, `,`) then `Esc`.
- Expected: status bar mode tag updates correctly and returns to NOR.
- Links: AZ-FR-0102..AZ-FR-0109.

### AZ-AT-0102 Unknown key no-op safety

- Steps: press unsupported key in each mode.
- Expected: no crash; mode remains stable.
- Links: AZ-FR-0110.

## 6.4 Goto Acceptance

### AZ-AT-0201 Column top/bottom jumps

- Steps: `g g`, `g e`.
- Expected: focus jumps to first and last item in current column.
- Links: AZ-FR-0301, AZ-FR-0302.

### AZ-AT-0202 First/last column jumps

- Steps: `g h`, `g l`.
- Expected: focus moves to first and last columns.
- Links: AZ-FR-0303, AZ-FR-0304.

### AZ-AT-0203 Jump labels target selection

- Steps: `g w`, type shown two-char label.
- Expected: focus lands on target card.
- Links: AZ-FR-0305..AZ-FR-0307.

### AZ-AT-2814 Goto invalid-label safety and project selector

- Steps: invoke `g w` and enter invalid label; invoke `g p` and select visible project option.
- Expected: invalid goto input fails safely; project selector opens and selection applies to visible option set.
- Links: AZ-FR-0308, AZ-FR-0309.

## 6.5 Selection and Bulk Acceptance

### AZ-AT-0301 Select mode toggle and clear

- Steps: `v`, toggle selections with `a`, exit with `Esc`.
- Expected: selection count increases then clears on exit.
- Links: AZ-FR-0401..AZ-FR-0406.

### AZ-AT-0302 Bulk move right

- Steps: select multiple cards, `Space l`.
- Expected: each selected issue transitions rightward when valid.
- Links: AZ-FR-0407, AZ-FR-1205.

### AZ-AT-0303 Bulk cleanup choice dialog

- Steps: select multiple, `Space d`.
- Expected: dialog offers worktrees-only/full/cancel options.
- Links: AZ-FR-1202..AZ-FR-1204.

### AZ-AT-0304 Invert-visible selection and clear-in-place

- Preconditions: visible set includes selected and unselected issues; hidden selected IDs also exist.
- Steps: enter Select mode, press `*`, then `x`.
- Expected: `*` inverts only visible non-tombstoned selection membership; `x` clears selected IDs while remaining in Select mode.
- Links: AZ-FR-0409, AZ-FR-0410.

### AZ-AT-0305 Selection stability across refresh/sort/filter

- Preconditions: active selection with mixed visible/hidden IDs.
- Steps: change sort/filter, trigger refresh, then inspect selection status.
- Expected: selection reconciles by ID deterministically; hidden selected count is shown explicitly.
- Links: AZ-FR-0411, AZ-FR-0412.

### AZ-AT-0306 Bulk destructive preview and frozen target set

- Preconditions: selected set where at least one item changes eligibility before apply.
- Steps: enter Action mode from Select, choose destructive bulk action, confirm.
- Expected: preview shows selected count/scope before confirm; execution freezes target set; drifted/skipped IDs are reported with reasons.
- Links: AZ-FR-0413, AZ-FR-0414.

### AZ-AT-2815 Bulk partial failure reporting

- Preconditions: select multiple issues where one targeted mutation is forced to fail.
- Steps: execute bulk-compatible action.
- Expected: successful items complete; failed item is explicitly reported with per-item context.
- Links: AZ-FR-0408.

## 6.6 Search/Filter/Sort Acceptance

### AZ-AT-0401 Live search by ID and title

- Steps: `/`, type query matching known ID fragment and title term.
- Expected: result set updates live and case-insensitively.
- Links: AZ-FR-0501..AZ-FR-0503.

### AZ-AT-0402 Search commit and clear

- Steps: `/` query `Enter`; then `/` `Esc`.
- Expected: committed filter persists; clear removes it.
- Links: AZ-FR-0504, AZ-FR-0505.

### AZ-AT-0403 Structured filter composition

- Steps: `f s o`, `f p 1`.
- Expected: only open P1 items shown (AND across dimensions).
- Links: AZ-FR-0506..AZ-FR-0513.

### AZ-AT-0404 Sort toggles direction

- Steps: `, p` then `, p` again.
- Expected: direction toggles; order reverses deterministically.
- Links: AZ-FR-0514..AZ-FR-0518.

## 6.7 View Acceptance

### AZ-AT-0501 Toggle Kanban and compact view

- Steps: press `Tab` repeatedly.
- Expected: view alternates KAN/LST while maintaining focused issue identity when possible.
- Links: AZ-FR-0601..AZ-FR-0604.

### AZ-AT-2816 Search/filter/sort parity across views

- Steps: apply search/filter/sort in Kanban, toggle to Compact with `Tab`, then back.
- Expected: equivalent result set and ordering semantics hold in both views.
- Links: AZ-FR-0605.

## 6.8 Epic Acceptance

### AZ-AT-0601 Epic detail opens with Enter

- Preconditions: epic with children.
- Steps: focus epic, press `Enter`.
- Expected: standard detail panel opens (same baseline structure as non-epic) and includes child progress summary.
- Links: AZ-FR-0701, AZ-FR-0708.

### AZ-AT-0602 Explicit epic drill-down entry and exit

- Preconditions: epic with children.
- Steps: focus epic, run `Space G` (or open epic detail and press `g`), then `q`.
- Expected: child-only board appears with header/progress; exit returns to parent board and epic focus.
- Links: AZ-FR-0702..AZ-FR-0707, AZ-FR-0709.

## 6.9 Session Acceptance

### AZ-AT-0701 Start standard session

- Steps: `Space s` on open issue.
- Expected: session starts, state shown on card, issue enters in_progress when configured.
- Links: AZ-FR-0801, AZ-FR-0805, AZ-FR-0808.

### AZ-AT-0702 Start with work prompt

- Steps: `Space S`.
- Expected: session starts with default work instruction and injected context includes `az prime` plus `az issue get <issue-id>` output for the focused issue.
- Links: AZ-FR-0802, AZ-FR-0802a.

### AZ-AT-0703 Yolo start variant

- Steps: `Space !`.
- Expected: start variant uses skip-permission mode and is visibly acknowledged.
- Links: AZ-FR-0803.

### AZ-AT-0704 Attach and return

- Steps: start session, `Space a`, detach back.
- Expected: attach succeeds and user can return to board.
- Links: AZ-FR-0809.

### AZ-AT-0705 Pause resume stop lifecycle

- Steps: `Space p`, `Space R`, `Space x`.
- Expected: session transitions paused -> busy/idle -> stopped with correct indicators.
- Links: AZ-FR-0811..AZ-FR-0814.

### AZ-AT-2817 Session branch context and state detection

- Preconditions: issue branch missing for one run; mixed tmux session states present for another run.
- Steps: start session and inspect card indicators.
- Expected: session start ensures issue branch context exists; detector maps and displays busy/waiting/done/error/paused accurately.
- Links: AZ-FR-0806, AZ-FR-0815.

## 6.10 Dev Server Acceptance

### AZ-AT-0801 Toggle dev server

- Steps: `Space r` to start and stop.
- Expected: dev server state toggles and indicator updates.
- Links: AZ-FR-0901, AZ-FR-0906.

### AZ-AT-0802 View and restart dev server

- Steps: start server, `Space v`, then `Space Ctrl-r`.
- Expected: viewer attach works; restart recovers server process.
- Links: AZ-FR-0903, AZ-FR-0904.

### AZ-AT-2818 Dev server execution context and collision handling

- Preconditions: multiple active issue contexts and one forced dev-server startup failure.
- Steps: start/toggle dev servers across issues.
- Expected: server runs in issue-scoped context, ports avoid collisions, and startup failure includes actionable guidance.
- Links: AZ-FR-0902, AZ-FR-0905, AZ-FR-0907.

## 6.11 Git and Merge Acceptance

### AZ-AT-0901 Update from base branch no conflicts

- Steps: `Space u` on clean branch.
- Expected: update completes with success toast/log.
- Links: AZ-FR-1001.

### AZ-AT-0902 Update from base branch with conflicts

- Preconditions: conflicting edits.
- Steps: `Space u`.
- Expected: conflict state surfaced with resolution/abort path.
- Links: AZ-FR-1002, AZ-FR-1003.

### AZ-AT-0903 Abort merge

- Steps: during active merge conflict, `Space M`.
- Expected: merge abort succeeds and repo returns to pre-merge state.
- Links: AZ-FR-1004, AZ-FR-1005.

### AZ-AT-0904 Merge to base branch with conflict warning

- Steps: `Space m` when overlap detected.
- Expected: confirmation appears; cancel preserves state.
- Links: AZ-FR-1008.

### AZ-AT-2819 Merge-to-base default context behavior

- Steps: invoke `Space m` from default board context.
- Expected: merge target is configured base branch by default context contract.
- Links: AZ-FR-1007.

### AZ-AT-0905 Show diff

- Steps: `Space f`.
- Expected: diff opens against merge-base with readable output.
- Links: AZ-FR-1006.

### AZ-AT-0906 Merge issue into issue

- Steps: source issue `Space b`, select target, confirm.
- Expected: source changes merged to target branch, self-merge prevented.
- Links: AZ-FR-1010, AZ-FR-1011.

### AZ-AT-0907 Bulk bring-up-to-date with queued conflict assistant

- Preconditions: selected issue set includes clean merges, conflicting merges, and at least one parent/upstream-sourced branch case.
- Steps: invoke bulk bring-up-to-date for selected set with bounded concurrency > 1 and conflict-assistant policy enabled.
- Expected: items process in FIFO queue order with bounded concurrent workers; each item resolves source branch per policy; conflicting items trigger automated conflict-resolution attempts; unresolved items remain recoverable with manual guidance; queue continues after per-item failure and ends with per-item summary.
- Links: AZ-FR-1012, AZ-FR-1013, AZ-FR-1014, AZ-FR-1015, AZ-FR-1016, AZ-FR-1017.

## 6.12 PR Acceptance

### AZ-AT-1001 Create PR

- Steps: `Space P` on pushed or pushable branch.
- Expected: PR created (or existing surfaced), metadata saved, indicator updated.
- Links: AZ-FR-1101..AZ-FR-1106.

### AZ-AT-1002 Open PR

- Steps: `Space O` with PR metadata present.
- Expected: browser opens PR URL.
- Links: AZ-FR-1107.

### AZ-AT-1003 Open PR missing metadata

- Steps: `Space O` with no PR info.
- Expected: actionable error toast.
- Links: AZ-FR-1108.

## 6.13 Authoring Acceptance

### AZ-AT-1101 Manual create issue

- Steps: `c`, fill minimal fields, save.
- Expected: issue created and visible on board.
- Links: AZ-FR-1301..AZ-FR-1303.

### AZ-AT-1102 Manual edit issue

- Steps: `Space e`, modify title/priority, save.
- Expected: issue updates reflected after refresh.
- Links: AZ-FR-1305.

### AZ-AT-1103 AI create/edit entrypoints

- Steps: invoke `C` and `Space E`.
- Expected: AI-assisted flows open and can submit valid updates.
- Links: AZ-FR-1304, AZ-FR-1306.

### AZ-AT-2820 Cleanup entrypoint and schema-safe issue writes

- Steps: invoke `Space d` for focused issue; run create/edit mutations including validation edge values.
- Expected: cleanup action is available from action palette; edit/create writes preserve tracker schema validity.
- Links: AZ-FR-1201, AZ-FR-1307.

## 6.14 Planning Acceptance

### AZ-AT-1201 Planning successful decomposition

- Steps: `p`, enter feature prompt, submit.
- Expected: epic plus child tasks and dependencies created.
- Links: AZ-FR-1501..AZ-FR-1505.

### AZ-AT-1202 Planning timeout/failure handling

- Steps: simulate timeout.
- Expected: clear failure state with retry guidance.
- Links: AZ-FR-1506.

## 6.15 Attachment Acceptance

### AZ-AT-1301 Add attachment from clipboard

- Steps: `Space i`, choose paste path.
- Expected: image stored and listed in detail panel.
- Links: AZ-FR-1601..AZ-FR-1604.

### AZ-AT-1302 Add attachment from file path

- Steps: `Space i`, path mode, enter image path.
- Expected: image attached and indexed.
- Links: AZ-FR-1603, AZ-FR-1609.

### AZ-AT-1303 Preview/open/remove attachment

- Steps: detail panel select attachment; `v`, `o`, `x`.
- Expected: preview works or degrades with message, external open works, deletion removes entry.
- Links: AZ-FR-1606..AZ-FR-1608.

### AZ-AT-2822 Attachment list selection navigation

- Preconditions: focused issue has multiple attachments.
- Steps: open detail and move selection with `j/k`.
- Expected: attachment selection navigation updates deterministically.
- Links: AZ-FR-1605.

### AZ-AT-2828 Attachment prompt paths are worktree-local

- Preconditions: issue has one or more image attachments and no existing session.
- Steps: trigger `Space S` (and optionally `Space !`) and inspect the generated startup prompt content sent to the AI CLI.
- Expected: attachment paths point to `<issue-worktree>/.azedarach/tmp/attachments/...`; flow does not rely on sibling/global project attachment directories.
- Links: AZ-FR-1610.

## 6.16 Settings and Projects Acceptance

### AZ-AT-1401 Edit settings and persist

- Steps: `s`, toggle value, close and reopen app.
- Expected: setting persists and behavior reflects change.
- Links: AZ-FR-1701..AZ-FR-1705, AZ-FR-1707.

### AZ-AT-1403 UI-only complete configuration

- Preconditions: profile with non-default values across all configurable domains.
- Steps: reset config to defaults, then use settings UI only to apply full target profile.
- Expected: resulting runtime behavior matches target profile with no mandatory direct file edits.
- Links: AZ-FR-1708, AZ-FR-1709.

### AZ-AT-1404 JSON schema autocomplete and validation support

- Preconditions: open config JSON in schema-aware editor.
- Steps: add known key, inspect autocomplete/type hints, then inject invalid value and run config reload.
- Expected: known keys/values receive schema-driven completion/validation hints; invalid schema value surfaces actionable error.
- Links: AZ-FR-1710, AZ-FR-1711.

### AZ-AT-1402 Project selector switching

- Steps: `g p`, choose project.
- Expected: board reloads against selected project context.
- Links: AZ-FR-1802, AZ-FR-1803.

### AZ-AT-2823 Multi-project registry persistence

- Preconditions: registry has multiple projects and persisted metadata.
- Steps: start app and switch projects via selector.
- Expected: multi-project model is supported and registry metadata persists/reloads correctly.
- Links: AZ-FR-1801, AZ-FR-1805.

### AZ-AT-2824 Status/help/log and tmux discovery contract

- Steps: open board, invoke help and logs overlays, run tmux discovery against known sessions.
- Expected: status bar shows current mode, help/log overlays are accessible and dismissible, and tmux session naming/discovery contract resolves known sessions including project-prefixed names while excluding mismatched project-prefixed sessions from other projects.
- Links: AZ-FR-1902, AZ-FR-1905, AZ-FR-1906, AZ-FR-2001, AZ-FR-2002, AZ-FR-2002a.

### AZ-AT-2826 Cross-project session prefix isolation

- Preconditions: at least two projects have active tmux sessions with overlapping short IDs (for example `ch-f` exists while viewing Azedarach project with issue `f`).
- Steps: load board for one project and run session discovery/refresh.
- Expected: only sessions matching the active project prefix (or valid legacy unprefixed sessions) influence active-session state; foreign prefixed sessions are ignored.
- Links: AZ-FR-2001a, AZ-FR-2002, AZ-FR-2002a, section 05 F-143.

## 6.17 Failure Acceptance

### AZ-AT-1501 No session attach failure clarity

- Steps: `Space a` on issue without session.
- Expected: non-crashing error with expected session identifier and next step.
- Links: AZ-FR-2101, section 05 F-011.

### AZ-AT-1502 Offline PR behavior

- Steps: disable network then `Space P`.
- Expected: explicit offline error; local state preserved.
- Links: AZ-FR-2203, section 05 F-030.

### AZ-AT-1503 Corrupt attachment index handling

- Steps: inject malformed metadata, open detail panel.
- Expected: unaffected attachments remain usable; repair hint shown.
- Links: section 05 F-062.

### AZ-AT-1504 Transient dependency retry and retry-exhausted guidance

- Preconditions: inject transient external dependency failures for one operation that eventually succeeds and one that never succeeds.
- Steps: execute both operations.
- Expected: first operation auto-recovers through bounded exponential backoff; second stops at max attempts with explicit retry-exhausted guidance.
- Links: AZ-FR-2206, AZ-FR-2207.

### AZ-AT-2825 Safety, non-interactive shell behavior, and startup diagnostics

- Preconditions: one destructive action path, one command requiring non-interactive-safe invocation, and environments with optional/mandatory dependency variance.
- Steps: run representative operations.
- Expected: destructive action requires explicit confirmation, shell path avoids interactive hangs, state-modifying operations remain idempotent where feasible, optional missing deps do not crash UI, and mandatory missing deps produce startup diagnostics.
- Links: AZ-FR-2103, AZ-FR-2104, AZ-FR-2105, AZ-FR-2201, AZ-FR-2202.

## 6.18 Startup and Re-entry Acceptance

### AZ-AT-1601 Startup with missing mandatory dependency

- Steps: make mandatory tool unavailable, launch app.
- Expected: app shows diagnostics and disables dependent actions without crashing.
- Links: AZ-FR-2701, AZ-FR-2702.

### AZ-AT-1602 Restore previous board context

- Steps: set view/filter/sort, exit app normally, relaunch same project.
- Expected: view/filter/sort restored; focus restored or nearest valid fallback used.
- Links: AZ-FR-2703..AZ-FR-2706.

### AZ-AT-1603 Graceful shutdown preserves running sessions

- Steps: start session, exit app.
- Expected: app exits cleanly; session remains running; terminal state restored.
- Links: AZ-FR-2707, AZ-FR-2708.

## 6.19 Concurrency and Mutation Acceptance

### AZ-AT-1701 Stale edit conflict handling

- Preconditions: open edit form; modify same issue externally.
- Steps: attempt save in app.
- Expected: conflict options shown; draft preserved unless discarded.
- Links: AZ-FR-2801..AZ-FR-2803.

### AZ-AT-1702 Background refresh does not drop active input

- Steps: open text input overlay; trigger refresh cycle.
- Expected: typed input remains intact; board re-sync occurs after overlay close.
- Links: AZ-FR-2804.

### AZ-AT-1703 Tracker lock contention behavior

- Steps: force tracker write lock then run mutating action.
- Expected: explicit lock feedback; no duplicate write; retry path available.
- Links: AZ-FR-2806..AZ-FR-2808.

### AZ-AT-2827 Refresh selection reconciliation and optimistic dependency/fork rollback

- Preconditions: selected set includes items affected by refresh removal; dependency/fork mutation failures can be injected.
- Steps: trigger refresh during active selections; run optimistic dependency and fork metadata mutations with forced failures.
- Expected: selection/focus reconcile to existing IDs only, detail shows typed incoming/outgoing dependencies, dependency-intent mutations are preserved in planning/fork behavior, and optimistic dependency/fork mutations rollback cleanly on failure.
- Links: AZ-FR-2805, AZ-FR-3409, AZ-FR-3703, AZ-FR-3805, AZ-FR-3806.

## 6.20 Terminal Compatibility Acceptance

### AZ-AT-1801 Narrow terminal usability

- Steps: resize terminal to constrained width/height.
- Expected: board remains navigable; overlays dismissible; mode tag always visible.
- Links: AZ-FR-2901..AZ-FR-2903.

### AZ-AT-1802 Long metadata truncation safety

- Steps: load issues with very long titles and attachment paths.
- Expected: deterministic truncation; issue ID remains visible.
- Links: AZ-FR-2904.

### AZ-AT-1803 No-color fallback readability

- Steps: run terminal with minimal/no color support.
- Expected: critical states understandable via textual cues.
- Links: AZ-FR-2905.

### AZ-AT-2826 Security/privacy redaction and ASCII-key operability

- Preconditions: operations produce logs/toasts and issue has attachments.
- Steps: execute sensitive operations, inspect outputs, and run primary workflows with ASCII keybindings only.
- Expected: secrets are redacted from logs/toasts, unrelated credential files remain untouched, attachment storage remains project-local, command execution preserves explicit argument boundaries, and core workflows are fully operable with ASCII mappings.
- Links: AZ-FR-2501, AZ-FR-2502, AZ-FR-2503, AZ-FR-2504, AZ-FR-2601.

## 6.21 Idempotence and Partial-Success Acceptance

### AZ-AT-1901 Interrupted multi-step action retry safety

- Steps: interrupt PR/session/cleanup flow mid-operation, retry action.
- Expected: operation resumes safely or repeats without double side effects.
- Links: AZ-FR-3001.

### AZ-AT-1902 Partial-success reporting clarity

- Steps: run bulk action where some targets fail.
- Expected: success/failure counts and per-item detail are explicit.
- Links: AZ-FR-3002, AZ-FR-3003.

### AZ-AT-1903 Impossible actions hidden or guarded

- Steps: open action mode in context lacking required resources.
- Expected: unavailable actions are suppressed or blocked with clear reason.
- Links: AZ-FR-3004.

## 6.22 Guardrail and Reconciliation Acceptance

### AZ-AT-2001 Destructive preflight impact preview

- Steps: invoke full cleanup or merge-to-base-branch path.
- Expected: impact summary shown with explicit confirm/cancel.
- Links: AZ-FR-3101, AZ-FR-3102.

### AZ-AT-2002 Cancel preflight has zero side effects

- Steps: open destructive confirmation and cancel.
- Expected: no issue/worktree/branch state changes.
- Links: AZ-FR-3103.

### AZ-AT-2003 Preflight target revalidation on execute

- Preconditions: modify target set between preview and confirm.
- Steps: confirm operation.
- Expected: stale targets revalidated and safely skipped/reported.
- Links: AZ-FR-3104.

### AZ-AT-2004 Remote divergence remediation flow

- Steps: induce non-fast-forward push rejection during PR flow.
- Expected: guided reconciliation appears; retry can continue original PR intent.
- Links: AZ-FR-3201, AZ-FR-3202.

### AZ-AT-2005 Orphan session reconciliation

- Steps: create indicator/tmux mismatch, trigger attach or refresh.
- Expected: reconciliation options shown; choice is applied and logged.
- Links: AZ-FR-3203, AZ-FR-3204.

## 6.23 Deterministic Ordering Acceptance

### AZ-AT-2101 Stable ordering under tied sort keys

- Steps: sort list where many cards share identical sort fields; refresh repeatedly.
- Expected: ordering remains deterministic.
- Links: AZ-FR-3301, AZ-FR-3304.

### AZ-AT-2102 Missing timestamp fallback ordering

- Steps: include cards lacking updated timestamp; sort by updated time.
- Expected: fallback tie-breaker ordering is stable and predictable.
- Links: AZ-FR-3302.

### AZ-AT-2103 Clock skew hint behavior

- Steps: simulate future-dated updates and refresh.
- Expected: non-blocking consistency hint appears, work remains possible.
- Links: AZ-FR-3303.

## 6.24 Dependency Graph Acceptance

### AZ-AT-2201 Inspect non-epic dependencies in detail

- Steps: open issue with mixed dependency types.
- Expected: incoming and outgoing edges are visible with relation types.
- Links: AZ-FR-3401..AZ-FR-3404.

### AZ-AT-2202 Create typed dependency edge

- Steps: create dependency from source to target with explicit relation type.
- Expected: edge persists and board indicators update.
- Links: AZ-FR-3402, AZ-FR-3405, AZ-FR-3407.

### AZ-AT-2203 Duplicate dependency prevention

- Steps: attempt to create identical edge twice.
- Expected: second attempt is safely rejected/no-op with feedback.
- Links: AZ-FR-3406.

### AZ-AT-2204 Remove dependency with confirmation

- Steps: remove existing dependency edge.
- Expected: confirmation required; on success, readiness/block indicators recalculate.
- Links: AZ-FR-3408.

### AZ-AT-2205 Invalid cyclic dependency handling

- Steps: submit cycle that violates tracker policy.
- Expected: operation rejected with actionable diagnostics.
- Links: AZ-FR-3410.

## 6.25 Runtime Branch-Origin and Relationship Representation Acceptance

### AZ-AT-2301 Runtime branch-origin choice on missing branch

- Preconditions: issue branch does not exist.
- Steps: start session and observe branch-creation flow.
- Expected: runtime origin chooser offers base branch and eligible upstream source branches when present.
- Links: AZ-FR-3501..AZ-FR-3503.

### AZ-AT-2302 Multi-upstream source selection required

- Preconditions: multiple eligible upstream sources exist.
- Steps: start branch creation.
- Expected: explicit source selection required; no ambiguous auto-pick.
- Links: AZ-FR-3504.

### AZ-AT-2303 Branch recreate reopens runtime origin chooser

- Preconditions: issue branch missing after prior existence.
- Steps: trigger branch recreation path.
- Expected: runtime origin chooser appears again for recreate flow.
- Links: AZ-FR-3506.

### AZ-AT-2821 Fork dependency persistence and origin chooser guidance

- Preconditions: fork flow with eligible and non-eligible upstream sources.
- Steps: invoke `Space F`, choose fork mode, choose origin source.
- Expected: fork action exists, selected relationship persists in dependencies, runtime branch-origin chooser appears, and no-upstream case provides explicit fallback reason.
- Links: AZ-FR-1401, AZ-FR-1403, AZ-FR-1404, AZ-FR-3505.

### AZ-AT-2304 Main board relationship chips and blocked signal

- Steps: load board with mixed dependency graph.
- Expected: compact relationship chips render and hard-blocked state remains explicit.
- Links: AZ-FR-3701, AZ-FR-3702.

### AZ-AT-2305 Drill-down relation scope switching

- Steps: enter epic drill-down and switch relation scope.
- Expected: scope switches deterministically while preserving valid focus/actions.
- Links: AZ-FR-3704, AZ-FR-3705.

## 6.26 Upstream Follow-On Merge Acceptance

### AZ-AT-2401 Merge upstream source into target issue directly

- Preconditions: issue A has eligible upstream source issue B with mergeable branch.
- Steps: open A dependency context, select B, invoke follow-on merge.
- Expected: B branch merges into A branch without routing through base branch.
- Links: AZ-FR-3601..AZ-FR-3604.

### AZ-AT-2402 Enforce relation-direction upstream rules

- Preconditions: reverse-direction relation candidate exists.
- Steps: attempt follow-on merge using reverse-direction source.
- Expected: source rejected as non-upstream according to relation-direction table.
- Links: AZ-FR-3609.

### AZ-AT-2403 Block follow-on merge from non-ready source

- Preconditions: selected upstream source does not satisfy readiness policy.
- Steps: attempt follow-on merge from B to A.
- Expected: operation is rejected with guidance.
- Links: AZ-FR-3605.

### AZ-AT-2404 Allow in-progress upstream source for eligible relation type

- Preconditions: relation type allows `in_progress` readiness and source issue is in_progress.
- Steps: invoke follow-on merge from that source.
- Expected: merge is permitted with clear readiness rationale.
- Links: AZ-FR-3610.

### AZ-AT-2405 Choose source when multiple upstream sources exist

- Preconditions: issue A has upstream sources B and C.
- Steps: invoke follow-on merge path and select C.
- Expected: selected source C is merged; B remains pending unless chosen later.
- Links: AZ-FR-3606.

### AZ-AT-2406 Suggested merge order shown for multi-upstream set

- Preconditions: issue has multiple eligible upstream sources.
- Steps: open merge source picker.
- Expected: deterministic suggested order is shown while preserving explicit source override.
- Links: AZ-FR-3611.

### AZ-AT-2407 Follow-on merge conflict recovery

- Preconditions: selected upstream->target merge has conflicts.
- Steps: invoke follow-on merge.
- Expected: recoverable conflict state with abort/retry guidance.
- Links: AZ-FR-3607.

### AZ-AT-2408 Dependency indicators refresh after merge

- Steps: complete follow-on merge for one upstream source on target issue.
- Expected: dependency indicators refresh and remaining unsatisfied upstream dependencies stay visible.
- Links: AZ-FR-3608.

### AZ-AT-2409 Parent drill-down contextual merge and fork defaults

- Preconditions: parent issue with child context in drill-down and parent branch available.
- Steps: focus child in drill-down, invoke `Space m`; invoke `Space F` for child creation.
- Expected: parent preselected as upstream source for merge/fork with explicit override option.
- Links: AZ-FR-1405, AZ-FR-3603, AZ-FR-3604.

## 6.27 Optimistic Mutation Acceptance

### AZ-AT-2501 Optimistic move success path

- Preconditions: movable issue in board.
- Steps: move issue status with action key.
- Expected: immediate in-memory move, pending marker clears on persistence success.
- Links: AZ-FR-3801, AZ-FR-3802.

### AZ-AT-2502 Optimistic move rollback on failure

- Preconditions: inject tracker write failure.
- Steps: move issue status with action key.
- Expected: immediate optimistic move then rollback to prior lane with actionable error.
- Links: AZ-FR-3803, AZ-FR-3804.

### AZ-AT-2503 Optimistic edit rollback isolation

- Preconditions: two issues visible; force edit write failure on one issue.
- Steps: edit failing issue while other issue receives successful mutation.
- Expected: only failing issue rolls back; unrelated success remains applied.
- Links: AZ-FR-3803, AZ-FR-3807.

### AZ-AT-2504 Hydration reconciliation preserves pending optimistic state

- Preconditions: optimistic mutation pending; external unrelated change appears in linear.
- Steps: run hydration refresh cycle.
- Expected: external change is applied, pending optimistic entity is preserved until resolution.
- Links: AZ-FR-3808, AZ-FR-3809.

### AZ-AT-2505 Retryable-pending mutation flow

- Preconditions: mutation type configured for retryable-pending behavior.
- Steps: force transient persistence failure.
- Expected: UI enters explicit retryable-pending state instead of immediate hard rollback.
- Links: AZ-FR-3810.

### AZ-AT-2506 Linear sync lifecycle logging visibility

- Preconditions: linear backend enabled with at least one queued mutation.
- Steps: trigger a sync flush with one successful dispatch and one forced transient failure.
- Expected: logs include flush start, per-item dispatch start, success path, and retry-or-terminal failure decision with project path, issue identity, operation type, and attempt context.
- Links: AZ-FR-3811, AZ-FR-3815.

## 6.28 Background Operation Acceptance

### AZ-AT-2601 Long-running actions register operation IDs

- Steps: start session and create PR in sequence.
- Expected: both operations appear in monitor with unique IDs and lifecycle states.
- Links: AZ-FR-3901, AZ-FR-3902.

### AZ-AT-2602 Board remains interactive during running operation

- Steps: launch merge/update operation, then navigate/filter board.
- Expected: navigation remains responsive while operation progress updates.
- Links: AZ-FR-3903.

### AZ-AT-2603 Cancel cancellable operation

- Steps: start cancellable operation and request cancel from operations monitor.
- Expected: operation transitions to canceled with deterministic result summary.
- Links: AZ-FR-3904, AZ-FR-3905, AZ-FR-3906.

### AZ-AT-2604 Failed operation shows retry guidance

- Preconditions: force operation failure.
- Steps: run background operation.
- Expected: failure includes root-cause context and retry guidance.
- Links: AZ-FR-3907.

### AZ-AT-2605 Async non-blocking action defaults to background

- Steps: trigger async action that is not interaction-blocking.
- Expected: action appears as background operation while board remains usable.
- Links: AZ-FR-3908.

### AZ-AT-2606 Operation cancelability metadata exposed when supported

- Steps: inspect operation status/probe for operation in multiple phases.
- Expected: phase-level cancelability metadata is present where implemented.
- Links: AZ-FR-3909.

### AZ-AT-2607 Bounded-wait read returns with stale hint under throttle

- Preconditions: force backend refresh to remain queued beyond default read wait budget.
- Steps: run read operation in default mode.
- Expected: read returns quickly from local state and clearly indicates potential staleness.
- Links: AZ-FR-3811, AZ-FR-3813.

### AZ-AT-2608 Explicit wait mode extends read freshness budget

- Preconditions: same throttled conditions as AZ-AT-2607.
- Steps: run equivalent read operation with explicit wait mode.
- Expected: operation uses higher wait budget and returns fresher data when sync completes within that budget.
- Links: AZ-FR-3812, AZ-FR-3814.

## 6.29 Machine-Readable Probe and Harness Acceptance

### AZ-AT-2701 Probe is side-effect free

- Steps: capture probe snapshot repeatedly with no user inputs.
- Expected: no state mutations attributable to probe calls.
- Links: AZ-FR-4001, AZ-FR-4002, AZ-FR-4101.

### AZ-AT-2702 Probe includes core board context

- Steps: open board, switch mode/view, open overlay, request probe.
- Expected: payload includes mode, focus, view, overlays, and visible card IDs/indicators.
- Links: AZ-FR-4003, AZ-FR-4004, AZ-FR-4101.

### AZ-AT-2703 Probe includes operation and error context

- Steps: run one successful and one failed background operation, request probe.
- Expected: payload includes operation queue states and recent user-visible errors.
- Links: AZ-FR-4005, AZ-FR-4101.

### AZ-AT-2704 Probe schema version and snapshot ordering

- Steps: capture consecutive probe snapshots during UI change.
- Expected: schema version present and revision/timestamp ordering is monotonic.
- Links: AZ-FR-4006, AZ-FR-4007, AZ-FR-4101.

### AZ-AT-2705 Headless probe availability

- Steps: run app in non-interactive test environment and request probe.
- Expected: probe remains available with full required payload.
- Links: AZ-FR-4008, AZ-FR-4101.

## 6.30 E2E Testability Meta Acceptance

### AZ-AT-2801 MUST requirements have acceptance coverage

- Steps: perform automated FR->AT mapping check for MUST requirements.
- Expected: each MUST FR maps to at least one acceptance scenario.
- Links: AZ-FR-4106.

### AZ-AT-2802 Canonical fixture profiles exist and are reusable

- Steps: load smoke/integration/scale fixture profiles and run representative scenarios from each profile.
- Expected: fixtures are deterministic and include hierarchy + non-hierarchy dependency graph variants.
- Links: AZ-FR-4102, AZ-FR-4103.

### AZ-AT-2803 Test profile covers terminal and base-branch variance

- Steps: run selected scenarios in narrow/standard terminal widths and with non-default base-branch name.
- Expected: assertions remain valid across profile variants.
- Links: AZ-FR-4104, AZ-FR-4105.

### AZ-AT-2804 Visual snapshot correctness for full-screen rendering

- Steps: run deterministic scenarios and compare captured terminal snapshots to approved baselines.
- Expected: no unexpected cell-level visual diffs for approved states.
- Links: AZ-FR-4107.

### AZ-AT-2805 Performance thresholds on critical paths

- Steps: run perf E2E suite for key flows (navigation, optimistic update, operation visibility updates).
- Expected: all measured thresholds pass configured budgets.
- Links: AZ-FR-4108.

### AZ-AT-2806 Stress and concurrency resilience

- Steps: run scale fixture with high-cardinality graph and concurrent background operations.
- Expected: no crash, deterministic state transitions, and acceptable responsiveness.
- Links: AZ-FR-4109.

### AZ-AT-2807 Probe + visual combined validation on high-risk workflows

- Steps: execute high-risk scenarios and assert both probe state and snapshot expectations.
- Expected: both assertion classes pass for merge/cleanup/session start/PR paths.
- Links: AZ-FR-4110.

### AZ-AT-2808 Initial interaction before full hydration

- Preconditions: `scale` fixture with dataset larger than a single viewport.
- Steps: launch app and immediately perform navigation and mode change inputs while additional rows/cards are still hydrating.
- Expected: board accepts inputs and updates focus/mode without waiting for full dataset hydration.
- Links: AZ-FR-2305.

### AZ-AT-2809 Virtualized rendering responsiveness at scale

- Preconditions: `scale` fixture with high-cardinality columns.
- Steps: perform sustained scroll/navigation across long columns and toggle view (`Tab`) repeatedly.
- Expected: no interaction lockups; navigation and view toggle remain responsive while traversing off-screen content.
- Links: AZ-FR-2306.

### AZ-AT-2810 Viewport-priority loading and monitoring

- Preconditions: `scale` fixture with mixed tmux/git/PR indicator states distributed across viewport and off-screen rows.
- Steps: open board, observe initial visible indicators, then jump/scroll to new viewport region.
- Expected: initial visible-window data and indicators appear first; newly visible region converges quickly after viewport change without blocking interaction.
- Links: AZ-FR-2307, AZ-FR-2308, AZ-FR-2310.

### AZ-AT-2811 Deferred off-screen work does not block foreground interaction

- Preconditions: `scale` fixture with intentional off-screen refresh backlog.
- Steps: keep navigating, filtering, opening/closing overlays while backlog exists.
- Expected: foreground interactions remain responsive; off-screen refresh proceeds opportunistically without mode or navigation stalls.
- Links: AZ-FR-2309.

## 6.31 Minimum Release Gate

A release candidate MUST pass:

- all scenarios AZ-AT-0001 through AZ-AT-1003
- at least one scenario in each remaining feature area
- all failure scenarios tagged high-risk (merge/cleanup/pr/session attach)
- all guardrail/reconciliation scenarios AZ-AT-2001 through AZ-AT-2005
- dependency graph scenarios AZ-AT-2201 through AZ-AT-2205
- branch-origin and relationship-display scenarios AZ-AT-2301 through AZ-AT-2305
- upstream follow-on scenarios AZ-AT-2401 through AZ-AT-2409
- optimistic mutation scenarios AZ-AT-2501 through AZ-AT-2505
- background operation scenarios AZ-AT-2601 through AZ-AT-2608
- probe/harness scenarios AZ-AT-2701 through AZ-AT-2705
- e2e meta scenarios AZ-AT-2801 through AZ-AT-2811
- extended conformance scenarios AZ-AT-2812 through AZ-AT-2827
