# Rootless Orchestrator Contracts

Orchestration scope is a typed domain value. Resolution applies this precedence:

1. an explicit `--root` selects rooted scope;
2. otherwise `AZEDARACH_ISSUE_ID` selects rooted scope;
3. otherwise the whole project is selected.

Flags and environment are startup inputs only. Durable orchestrator identity is
the project ID plus the resolved typed scope, so project and rooted singletons
cannot collide.

Project lifecycle has four states. `working` means executable work or runtime
activity remains. `quiescent` means no executable work is active; unresolved
human interactions are allowed and prevent completion. `complete-grace` means
all live issues are closed or backlog and there are no active sessions, review
requests, open/active issues, or unresolved interactions. After the configurable
`orchestration.completeGrace`, the singleton becomes `paused`, not destroyed.
Wake events are new open work, review requests, accepted human answers, and
recovery events. They are idempotent and coalesced by
`orchestration.wakeDebounce`.

The exact-scope lease persists `complete_since`, `last_wake_at`, and the last
wake reason. Completion changes clear and restart `complete_since`; daemon
restart therefore cannot shorten or extend grace. Wake updates are serialized
under the SQLite project write lock, so duplicate events from multiple daemons
are suppressed by the durable debounce timestamp.

`orchestration.scope_identity` uses refreshed durable projection state.
`orchestration.scope_singleton` is hybrid: refresh the durable exact-scope
lease, then compare its session identity with live tmux runtime.
`orchestration.rooted_bootstrap_delivery` is hybrid: refresh the durable
accepted prompt acknowledgement, then compare its session, prompt hash, and
runtime marker with live tmux before trusting the attached agent.
`orchestration.project_completion` is hybrid: refresh durable issue, review,
interaction, orchestration, and session projections, then compare runtime
presence with live tmux. `runtime.reconcile` exposes both mappings.
`session.managed_agent_restart` is hybrid: refresh the durable logical pane,
tmux pane, pane PID, and agent incarnation binding, then compare it with live
tmux before exact-pane replacement and again with hook-backed replacement
evidence before reporting success. Tmux command acceptance is never restart
acknowledgement.

## Client and authority boundary

The daemon owns scope resolution after startup, singleton identity, candidate
classification, ownership, lifecycle, interaction revisions, review outcomes,
and wake decisions. CLI and TUI commands parse intent, send typed protocol
requests, and render the returned snapshot. They must not reproduce those
policies or call git, tmux, issue-store, or session authority directly when a
daemon command exists.

Non-trivial review inspection is delegated to fresh, read-only ephemeral review
agents in bounded parallel batches. A delegate receives the issue contract,
worker evidence, context risk, exact worktree, and diff base, then returns a
structured clean-or-findings verdict. It cannot mutate code or durable state.
The owning orchestrator alone validates that packet and executes review return,
acceptance, integration, and close. Queued reviews remain visible and important,
but do not globally suppress unrelated runnable starts while managed capacity is
available.

The supported operator surfaces are:

- `az orchestrator-session start|attach|stop|status [--root <issue>] [--project <project>]`
- `az orchestrate status|start|watch|complete-check [--root <issue>]`
- `az interaction list|get|discuss|answer|resolve|withdraw`
- the TUI project overview for project-level start, attach, status, and health
- the TUI Waiting Human overlay for direct answers, proposal review, advisor
  discussion, revision-conflict reload, resolution, and withdrawal

`attach` is declarative: the daemon resumes or recovers the exact-scope session
and returns its tmux target. The CLI may then exec the user's terminal attach;
the daemon handler must never perform a blocking terminal attach.

Rooted startup is scope-authoritative rather than ticket-type-derived. A
ticket-session start for an epic delegates before any worker lifecycle write to
the same exact rooted `OrchestratorSession` start handler. Ticket-session attach
and stop also delegate when the durable rooted lease owns that physical session.
The
daemon acquires the rooted lease before agent launch, supplies an explicit
orchestrator-only prompt whose first commands are `az prime`, rooted status,
and rooted watch, and does not report startup success until the complete
file-backed prompt has been acknowledged. The launcher writes the rooted
orchestrator desired-session product directly; it never leaves a generic worker
desired-running product for the same root, and exact-scope startup retires any
legacy dual worker intent in the same SQLite transaction that writes the rooted
intent before accepting or recovering the runtime. A unique physical-session
guard prevents stale worker writers, runtime observation/activity ingestion,
recovery, or restart from recreating a second top-level role. Migration 0051
converges historical running/running, stopped/stopped, and stopped/running
worker/rooted pairs while failing closed for ambiguous duplicate roles. A durable SQLite projection binds
that acknowledgement to the exact project, root, session, prompt hash, and a
cryptographically random marker in the live tmux environment. The marker is
not itself a process identity; trust comes from comparing it with the refreshed
accepted acknowledgement while holding the exact rooted-scope transition lock.
`session restart-all` invalidates acknowledgement before interruption, launches
the replacement under that same cross-process lock, and re-delivers and
persists the rooted prompt acknowledgement before reporting success. A
cancelled replacement remains unacknowledged and must be repaired by the next
rooted start. Deterministic session ID reuse after stop, runtime loss, or
process replacement cannot inherit bootstrap trust. The
rooted session must never receive worker or contributor authority merely because
the root ticket is not typed as an epic.

`stop` is also exact-scope and daemon-authoritative. It first records paused
lease intent without releasing the durable scope or cursor, then requests agent
exit, closes the residual shell, and falls back to bounded tmux cleanup. Missing
and already-stopped scopes are idempotent results. Session-end hooks and hybrid
reconciliation repair interrupted exits to paused plus stopped runtime intent,
while status reports stale runtime without mutating lifecycle, so reconcile
cannot recreate a deliberately stopped orchestrator. A paused lease paired with
that exact stopped session intent is an operator pause and is excluded from
automatic lifecycle wake/evaluation until an explicit start records running
intent again; complete-grace pauses without stopped intent keep their existing
event-driven wake behavior.

## End-to-end acceptance matrix

The combined acceptance suite deliberately exercises production authority
paths rather than transitional adapters:

| Contract | Executable evidence |
| --- | --- |
| Exact-scope singleton start, attach, stale-runtime recovery | `TestProjectOrchestratorSessionStartAttachesExactScopeSingleton` |
| Rooted role bootstrap, first action, and attached-session repair | `TestRootedOrchestratorSessionStartupSeedsRoleAndRepairsMissingBootstrap` |
| Epic start delegation and mutually exclusive physical-session role | `TestRootedOrchestratorSessionStartupSeedsRoleAndRepairsMissingBootstrap`, `TestRuntimeStateStoreRootedTransitionRejectsStaleWorkerAcrossStores`, `TestRootedSessionRoleExclusivityMigrationConvergesRequiredStatePairs` |
| Bounded project scheduling and stable ordering | `TestOrchestrationCandidateOrderingIsStable`, `TestProjectOrchestratorLoopPrioritizesReviewAndPersistsCursor`, `TestProjectOrchestratorSnapshotKeepsStartsActionableAlongsideReview`, `TestProjectStartIntentDoesNotGloballyBlockOnActionableReview` |
| Foreign ownership exclusion and claim races | `TestProjectOrchestrationSnapshotRefreshesCrossProcessOwnership`, `TestProjectReviewQueueRefreshesCrossProcessReviewLease` |
| Human request, advisor discussion, edited/direct answer, atomic resolution | `TestInteractionDiscussStartsAndAttachesLiveAdvisorWithoutMutatingIssueLifecycle`, `TestInteractionStructuredProposalCanBeHumanEditedAndAtomicallyResolved` |
| Review return and authoritative accepted close | `TestReviewReturnPreservesWorkerOwnerAndDurablyDeliversFindings`, `TestReviewAcceptSurfacesAuthoritativeCloseFailureAndKeepsReviewState`, `TestReviewAcceptClosesMultipleInternalReviewsBeforeDependentCompletion` |
| Quiescent, complete-grace, pause, and wake | `TestProjectOrchestrationEndToEndAcceptanceInventory` executes the production lease authority across quiescence, persisted grace, pause, relevant-change wake, debounced duplicate wake, and grace reset; focused state regressions are `TestOrchestratorLifecycleGracePersistsAcrossRestartAndResets` and `TestOrchestratorWakeIsDurablyDebouncedAcrossStores` |
| Restart and durable cursor/action replay | `TestProjectOrchestratorActionKeyIsRestartStableAndStateSensitive`, `TestOrchestratorLoopCheckpointSurvivesRestartAndUsesCursorCAS` |
| Multi-daemon stale-cache/race behavior | `TestOrchestratorLeaseAuthorityRefreshesStaleCacheBeforeAcquire`, `TestProjectOrchestratorLoopMultiDaemonReplayDoesNotDuplicateCheckpointAction`, `TestAdvisorRecoveryCleansRuntimeWhenTerminalRequestWinsCrossDaemonRace` |

`TestProjectOrchestrationEndToEndAcceptanceInventory` locks this inventory and
the required invariant-source mappings in one discoverable gate. The named
tests retain the detailed state setup and assertions; the inventory prevents a
partial package run or future refactor from silently dropping a required leg.

Run the focused acceptance gate with:

```bash
go test ./internal/daemon -run 'TestProject(OrchestrationEndToEndAcceptanceInventory|OrchestratorSessionStartAttachesExactScopeSingleton|OrchestratorLoopPrioritizesReviewAndPersistsCursor|OrchestrationSnapshotRefreshesCrossProcessOwnership)|TestRootedOrchestratorSessionStartupSeedsRoleAndRepairsMissingBootstrap|TestInteraction(DiscussStartsAndAttachesLiveAdvisorWithoutMutatingIssueLifecycle|StructuredProposalCanBeHumanEditedAndAtomicallyResolved)|TestReviewReturnPreservesWorkerOwnerAndDurablyDeliversFindings|TestOrchestrator(LifecycleGracePersistsAcrossRestartAndResets|WakeIsDurablyDebouncedAcrossStores|LeaseAuthorityRefreshesStaleCacheBeforeAcquire)' -count=1
go test ./internal/daemon/state -run 'TestOrchestrator(LifecycleGracePersistsAcrossRestartAndResets|WakeIsDurablyDebouncedAcrossStores)' -count=1
```

## Rollout and rollback

1. Back up the project issue database and runtime projection before upgrading.
2. Start one daemon on the new binary. Runtime reconcile runs on daemon startup.
3. Confirm the typed reconcile debug contract with
   `go test ./internal/daemon -run TestCommandRuntimeReconcileRoutesToManualRepair`.
   Its `invariant_sources` must contain the scope, singleton, completion,
   candidates, review, claim/start, loop, interaction, advisor, and parent
   continuation mappings documented in `docs/README.md`.
4. Run `env -u AZEDARACH_ISSUE_ID az orchestrator-session status`, then start or
   attach the project scope. (Use `--root <issue>` for rooted scope.)
   A repeated start must return the same live exact-scope session.
5. Observe one bounded scheduling/review cycle. Confirm foreign-owned work is
   excluded, review inspection is delegated, unrelated runnable work can start,
   and Waiting Human work is not started.
6. Exercise one disposable interaction through discuss and human resolution;
   confirm the accepted answer wakes evaluation without directly starting work.
7. Leave the singleton running through quiescence and complete-grace. Confirm it
   pauses after grace and wakes once for new work.

Do not run old and new binaries concurrently after a protocol/schema contract
change. Roll back by stopping the upgraded daemon, restoring both backed-up
databases as a pair, and starting the prior binary. Never restore only the issue
database or only the runtime projection: their revisions and exact-scope leases
form one recovery boundary.

## Failure diagnosis

- Duplicate orchestrators: compare the durable exact-scope lease with live tmux;
  do not delete the lease before `runtime.reconcile` reports both sources.
- Stuck Waiting Human: fetch the request and use its current positive revision;
  stale answers must be rejected and reloaded, never force-written.
- Complete reported too early: inspect open/active issues, review requests,
  unresolved interactions, projected sessions, and live tmux independently.
- Replayed scheduling/review: inspect the durable cursor, checkpoint action key,
  and observation event before retrying. Reuse the same intent key only for an
  identical retry.
- Missing advisor: keep the request durable and run reconcile; terminal requests
  clean advisor resources, while unresolved requests recover the singleton.
