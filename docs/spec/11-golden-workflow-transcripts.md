# 11 - Golden Workflow Transcripts

These transcripts define canonical high-risk interaction paths for product validation.

Each transcript specifies deterministic input and expected user-visible checkpoints.

## 11.1 Transcript Format

- Fixture: canonical fixture profile from Section 06
- Start State: required initial board context
- Input Sequence: exact keys/actions
- Checkpoints: expected mode/focus/indicator/operation states
- End State: completion criteria

## 11.2 GW-001 Session Start + Attach + Resume

- Fixture: `integration`
- Start State: focused issue has no running session; branch/worktree can be created.

Input Sequence:

1. `Space s`
2. wait for operation monitor to show running start-session op
3. `Space a`
4. return to board (`Ctrl-a Ctrl-a` if available)
5. `Space p`
6. `Space R`

Checkpoints:

- mode returns to `NOR` after each action sequence
- card session indicator transitions idle -> busy -> paused -> busy
- operation monitor records successful session start and resume operations
- board remains navigable during operation updates

End State:

- focused issue shows running session with no unresolved operation failure

## 11.3 GW-002 Upstream Follow-On Merge With Conflict/Abort Path

- Fixture: `conflict`
- Start State: focused target issue has eligible upstream dependency source.

Input Sequence:

1. open detail with `Enter`
2. select eligible upstream relation
3. invoke context merge (`m` in detail relation context)
4. when conflict is injected, invoke abort merge (`Space M` from board action mode after returning)

Checkpoints:

- merge path validates upstream eligibility before execution
- conflict outcome is explicit and recoverable
- abort path restores recoverable pre-merge repository state
- dependency indicators refresh after abort/resolve action

End State:

- repository and board state are consistent; no ambiguous in-progress merge marker

## 11.4 GW-003 Create PR With Viewport-Scoped Responsiveness

- Fixture: `scale`
- Start State: large board dataset; focused issue has pending branch changes and no existing PR.

Input Sequence:

1. `Space P`
2. while PR operation runs, perform `j/k` navigation and toggle view with `Tab`
3. scroll to a new viewport region
4. return to original issue and inspect PR indicator

Checkpoints:

- operation monitor shows create-PR lifecycle and final result
- board remains responsive during background PR operation
- newly visible viewport issues converge quickly for session/git indicators
- off-screen monitoring backlog does not block navigation or mode transitions

End State:

- PR indicator is persisted on target issue and operation result is discoverable in logs/monitor

## 11.5 GW-004 Spec Workspace Coverage and Debounced Publish

- Fixture: `integration`
- Start State: spec workflow is configured with publish targets and auto-config enabled.

Input Sequence:

1. from board, press `g s` to enter Spec workspace
2. press `Tab` to cycle into `Coverage` and confirm at least one visible gap
3. perform multiple spec mutations in quick succession (for example, requirement metadata update and issue-link add/remove)
4. press `Tab` to `Publish` and observe publish status surfaces
5. wait for debounced publish trigger to settle

Checkpoints:

- probe reports `spec.workspace.active=true` and subview transitions in deterministic `Tab` order
- rapid mutations are coalesced by debounce policy (no publish storm behavior)
- publish summary exposes per-target outcomes and last-run metadata
- managed sections are updated while non-managed document regions remain unchanged
- local spec records remain authoritative despite remote doc edits

End State:

- Spec workspace exits cleanly to prior board context and publish state is durable/inspectable after exit

## 11.6 Rewrite Exit Criterion

An implementation passes golden transcript validation when all GW-001 through GW-004 runs complete with matching checkpoints in both probe assertions and approved visual snapshots.
