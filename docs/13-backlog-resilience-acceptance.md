# Backlog Resilience Acceptance Matrix

This matrix is the closure checklist for issue `ckj`. It covers the daemon
stream and TUI catch-up paths that keep task mutations visible when runtime
telemetry is noisy.

## Scope

- Task mutation events must surface promptly behind git/worktree/session noise.
- Runtime projection telemetry may be coalesced only where latest-wins semantics
  are valid.
- Revision gaps must trigger snapshot rehydrate instead of applying skipped
  state.
- Slow consumers must either catch up from backlog or rehydrate from snapshot.
- Metrics must make drained, coalesced, and rehydrated stream work visible in
  tests.

## Matrix

| Risk | Focused coverage | Command | Required assertion |
| --- | --- | --- | --- |
| `task.created` waits behind runtime noise | `TestDaemonStreamEventBatchAppliesTaskEventBehindProjectionBacklog` | `go test ./internal/tui -run TestDaemonStreamEventBatchAppliesTaskEventBehindProjectionBacklog` | A batched `task.created` after runtime telemetry advances the TUI revision and appends the new task without waiting for snapshot polling. |
| Duplicate refresh work during noisy batches | `TestDaemonStreamEventBatchCoalescesSnapshotRefreshCommands`, `TestScheduleIssuesRefreshDedupesInFlightAndReplaysPending` | `go test ./internal/tui -run 'TestDaemonStreamEventBatchCoalescesSnapshotRefreshCommands|TestScheduleIssuesRefreshDedupesInFlightAndReplaysPending'` | Multiple refresh-triggering events produce one immediate snapshot refresh; in-flight refresh duplicates are coalesced and one pending replay is preserved. |
| TUI projection backlog applies stale intermediate state | `TestDaemonStreamEventBatchCoalescesPureRuntimeProjectionByIssue` | `go test ./internal/tui -run TestDaemonStreamEventBatchCoalescesPureRuntimeProjectionByIssue` | Repeated pure runtime projections for the same issue apply only the latest projection and increment the coalesced-projection metric. |
| Adaptive TUI batch budget regresses to one event per update | `TestDaemonStreamEventBatchCoalescesSnapshotRefreshCommands` | `go test ./internal/tui -run TestDaemonStreamEventBatchCoalescesSnapshotRefreshCommands` | Batch metrics report `EventsDrained=2` and `MaxBatchSize=2`, proving the update path handles more than one stream event per render cycle. |
| Revision gap applies unsafe state before rehydrate | `TestDaemonStreamEventBatchRehydratesOnGapAfterCoalescedProjection`, `TestProjectionEventRevisionGate`, `TestSessionProjectionEventRevisionGate` | `go test ./internal/tui -run 'TestDaemonStreamEventBatchRehydratesOnGapAfterCoalescedProjection|TestProjectionEventRevisionGate|TestSessionProjectionEventRevisionGate'` | Gap events stop the current stream, preserve the last safe revision, avoid applying the gap projection, and count a rehydrate. |
| Daemon-side projection bursts overload subscribers | `TestRuntimeProjectionWriterCoalescesProjectionBurstsByIssue`, `TestRuntimeProjectionCoalescingDoesNotDelayNonProjectionEvents` | `go test ./internal/daemon -run 'TestRuntimeProjectionWriterCoalescesProjectionBurstsByIssue|TestRuntimeProjectionCoalescingDoesNotDelayNonProjectionEvents'` | Rapid git/worktree/session projection updates emit a bounded latest projection, while non-projection events stay on the immediate publish path. |
| Slow consumer drops task mutations under telemetry pressure | `TestPriorityTaskMutationEvictsQueuedRuntimeTelemetry`, `TestPriorityTaskMutationDoesNotEvictQueuedTaskMutations`, `TestDaemonStreamPriorityTaskEventAppliesAcrossAnnotatedTelemetryGap` | `go test ./internal/daemon/publish ./internal/tui -run 'TestPriorityTaskMutationEvictsQueuedRuntimeTelemetry|TestPriorityTaskMutationDoesNotEvictQueuedTaskMutations|TestDaemonStreamPriorityTaskEventAppliesAcrossAnnotatedTelemetryGap'` | A saturated subscriber queue evicts low-priority runtime telemetry for task mutations, annotates the skipped revisions, and the TUI applies the task mutation without rehydrate; already queued task mutations are never evicted. |
| Backlog overflow recovery loses convergence | `TestStreamDropResubscribeCatchup`, `TestStreamOverflowGapFallbackAndIdempotency`, `TestStreamSnapshotFallbackAfterSubscriberOverflow` | `go test ./internal/daemon/testharness -run 'TestStreamDropResubscribeCatchup|TestStreamOverflowGapFallbackAndIdempotency|TestStreamSnapshotFallbackAfterSubscriberOverflow'` | In-window backlog catch-up preserves order; overflow triggers snapshot fallback; duplicate and out-of-order deltas are idempotent. |
| Stream cursor semantics drift | `TestStreamCursorDecideAndAdvance`, `TestDaemonEventRevisionReducer` | `go test ./internal/contracts/protocol ./internal/tui -run 'TestStreamCursorDecideAndAdvance|TestDaemonEventRevisionReducer'` | Duplicate/stale events are ignored, sequential events advance the cursor, and gaps request resync without advancing. |
| Metrics visibility regresses | TUI stream batch tests above | `go test ./internal/tui -run 'TestDaemonStreamEventBatchCoalescesSnapshotRefreshCommands|TestDaemonStreamEventBatchCoalescesPureRuntimeProjectionByIssue|TestDaemonStreamEventBatchRehydratesOnGapAfterCoalescedProjection'` | Tests assert `EventsDrained`, `MaxBatchSize`, `RefreshesCoalesced`, `RuntimeProjectionsCoalesced`, and `Rehydrates`. |

## Closure Commands

Run the focused matrix first:

```bash
go test ./internal/tui -run 'TestDaemonStreamEventBatchAppliesTaskEventBehindProjectionBacklog|TestDaemonStreamEventBatchCoalescesSnapshotRefreshCommands|TestDaemonStreamEventBatchCoalescesPureRuntimeProjectionByIssue|TestDaemonStreamEventBatchRehydratesOnGapAfterCoalescedProjection|TestScheduleIssuesRefreshDedupesInFlightAndReplaysPending|TestProjectionEventRevisionGate|TestSessionProjectionEventRevisionGate'
go test ./internal/daemon -run 'TestRuntimeProjectionWriterCoalescesProjectionBurstsByIssue|TestRuntimeProjectionCoalescingDoesNotDelayNonProjectionEvents'
go test ./internal/daemon/publish ./internal/tui -run 'TestPriorityTaskMutationEvictsQueuedRuntimeTelemetry|TestPriorityTaskMutationDoesNotEvictQueuedTaskMutations|TestDaemonStreamPriorityTaskEventAppliesAcrossAnnotatedTelemetryGap'
go test ./internal/daemon/testharness -run 'TestStreamDropResubscribeCatchup|TestStreamOverflowGapFallbackAndIdempotency|TestStreamSnapshotFallbackAfterSubscriberOverflow'
go test ./internal/contracts/protocol ./internal/tui -run 'TestStreamCursorDecideAndAdvance|TestDaemonEventRevisionReducer'
```

Then run the slice and project gates before closing `ckj`:

```bash
go test ./internal/tui ./internal/cli
go test ./internal/daemon/... ./internal/client/...
just build
just test
```
