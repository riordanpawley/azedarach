# External Sync Backend Internals (Linear, Local-First)

This document describes how the external sync backend works now, including all key internals in the `ts-opentui` path.

## Scope

- Local-first issue backend behavior for `issueTracker: linear`
- Mutation write path (`create`/`update`/`close`/`delete`) and sync queue processing
- Read path behavior (`list`/`show`/`showMultiple`/`ready`/`search`) with read-sync + fallback
- Board refresh triggers (background polling, SDK/CLI webhooks, PTY-triggered local refresh)
- Diagnostics + queue/runtime state reporting

## 1) Service and Dataflow Topology

```mermaid
flowchart LR
  UI[UI and Keyboard Handlers]
  BS[BoardService]
  TH[TaskHandlersService]
  MQ[MutationQueue]
  ITC[IssueTrackerClient]
  LDB[LocalIssueStore SQLite]
  BSR[BackendSyncRouter]
  BSL[BackendSyncLinear]
  ISS[IssueSyncService]
  LS[LinearSdk]
  LWS[LinearWebhookService]
  DIAG[DiagnosticsService]
  PTY[PTYMonitor]

  UI --> TH
  UI --> BS
  TH --> BS
  TH --> MQ
  MQ --> ITC

  BS --> ITC
  BS --> ITC
  ITC --> LDB
  ITC --> BSR
  BSR --> BSL
  BSL --> ISS
  ISS --> LDB
  ISS --> LS
  ISS --> DIAG
  BS --> DIAG

  LWS --> BS
  PTY --> BS
```

## 2) Mutation Write Path (Optimistic -> Queue -> Linear)

```mermaid
sequenceDiagram
  participant U as User Action
  participant TH as TaskHandlersService
  participant BS as BoardService
  participant MQ as MutationQueue
  participant ITC as IssueTrackerClient
  participant LDB as LocalIssueStore
  participant BSR as BackendSyncRouter
  participant ISS as IssueSyncService
  participant LS as LinearSdk
  participant D as DiagnosticsService

  U->>TH: move/update/delete
  TH->>BS: applyOptimisticMove()
  TH->>MQ: add(mutation)
  TH->>MQ: process(taskId)

  MQ->>ITC: update/close/delete (local-first)
  ITC->>LDB: mutate issue rows
  ITC->>LDB: enqueue sync_queue item (upsert/close/delete)
  MQ->>ITC: sync() (best effort post-mutation)

  ITC->>BSR: resolve()
  BSR->>ISS: flushLinearQueue()
  ISS->>LDB: listPendingSync(target=linear, claimable)
  ISS->>ISS: collapsePendingItems() by issueId
  Note over ISS: create->close collapse is preserved as upsert

  loop each collapsed item
    ISS->>LDB: getIssueForSync + getExternalRef + parent ref
    alt operation == upsert
      ISS->>LS: createIssue or updateIssue
      ISS->>LDB: upsertExternalRef
    else operation == close/delete
      ISS->>LS: updateIssue(state=closed)
    end
    alt success
      ISS->>LDB: markSyncSucceeded(claims)
    else retriable error
      ISS->>LDB: markSyncRetriable(delay/backoff)
    else terminal error
      ISS->>LDB: markSyncTerminalFailure
    end
  end

  ISS->>ISS: pullRemoteSnapshots() when hydration interval due
  ISS->>LS: fetch issues + metadata
  ISS->>LDB: importExternalSnapshot()
  ISS->>D: setIssueSyncHealth(runtime/queue/run)
  ISS-->>ITC: { pushed, pulled }
```

## 3) Read Path (Local-First + Read Sync + Direct Fallback)

```mermaid
sequenceDiagram
  participant BS as BoardService refresh()
  participant ITC as IssueTrackerClient
  participant LDB as LocalIssueStore
  participant BSR as BackendSyncRouter
  participant ISS as IssueSyncService
  participant LS as LinearSdk-backed client path

  BS->>ITC: list/show/showMultiple/ready/search

  alt list/ready/search/getEpic*
    ITC->>ITC: ensureLinearReadSync()
    ITC->>BSR: resolve()
    BSR->>ISS: flushLinearQueue() (bounded wait)
    ITC->>LDB: read local data
    ITC-->>BS: local-first result set
  else show(id)
    ITC->>LDB: show(id)
    alt found and not tombstone
      ITC-->>BS: return local issue
    else missing
      ITC->>ITC: ensureLinearReadSync(maxWaitMs)
      ITC->>LDB: show(id) again
      alt still missing and pulled==0
        ITC->>LS: direct fallback read ("show id")
        ITC-->>BS: fallback issue if found
      else
        ITC-->>BS: NotFound
      end
    end
  else showMultiple(ids)
    ITC->>ITC: ensureLinearReadSync()
    ITC->>LDB: showMultiple(ids)
    alt missing subset and pulled==0
      ITC->>LS: direct fallback read for missing ids
      ITC->>ITC: mergeIssuesByRequestedIds()
    end
    ITC-->>BS: merged ordered result
  end
```

## 4) Sync Queue Lifecycle

```mermaid
stateDiagram-v2
  [*] --> Pending: enqueueSync(operation)

  Pending --> Processing: listPendingSync() claim\n(set attempt_token + lease_expires_at)
  Processing --> Removed: markSyncSucceeded()\n(delete claimed + <=maxQueueId for issue)
  Processing --> Pending: markSyncRetriable()\n(set next_attempt_at, attempts++)
  Processing --> Failed: markSyncTerminalFailure()\n(status=failed, attempts++)

  Processing --> Processing: lease valid (active)
  Processing --> Pending: lease stale/expired\n(claimable again)

  Failed --> [*]
  Removed --> [*]
```

## 5) Board Refresh Strategy State Machine

```mermaid
stateDiagram-v2
  [*] --> NonLinearPolling: backend != linear

  NonLinearPolling --> LinearDisabledPolling: backend=linear, webhooks disabled
  NonLinearPolling --> CliListener: backend=linear, transport=cli, listener config ok
  NonLinearPolling --> CliPollingFallback: backend=linear, transport=cli, config missing

  NonLinearPolling --> SdkEventsLocalRefresh: transport=sdk, mode=sdk, healthy=true
  NonLinearPolling --> SdkCliFallbackListener: transport=sdk, mode!=sdk or unhealthy, listener config ok
  NonLinearPolling --> SdkPollingFallback: transport=sdk, mode!=sdk or unhealthy, no listener config

  SdkEventsLocalRefresh --> SdkCliFallbackListener: SDK unhealthy/mode change + listener config ok
  SdkEventsLocalRefresh --> SdkPollingFallback: SDK unhealthy/mode change + no listener config
  SdkCliFallbackListener --> SdkEventsLocalRefresh: SDK healthy again
  SdkPollingFallback --> SdkEventsLocalRefresh: SDK healthy again

  note right of SdkEventsLocalRefresh
    localRefreshOnly=true
    PTY-triggered refresh uses local session-state updates only
  end note
```

## 6) Bootstrap and Flush Concurrency Gates

```mermaid
flowchart TD
  A[bootstrapLinear or flushLinearQueue called] --> B{in-flight map has projectPath?}
  B -- yes --> C[await existing Deferred result]
  B -- no --> D[create Deferred + store in map]
  D --> E[execute run]
  E --> F{runtime available?}
  F -- no --> G[report skipped]
  F -- yes --> H[perform bootstrap or flush]
  H --> I[report success/failure]
  G --> J[resolve Deferred]
  I --> J
  J --> K[remove in-flight entry in ensuring/finalizer]
```

## Internal Notes and Guards

- `IssueSyncService` de-duplicates concurrent bootstrap/flush per project path via in-flight `Deferred` maps.
- Hydration pull in flush is interval-gated (`LINEAR_REMOTE_HYDRATION_MIN_INTERVAL_MS = 60_000`), not triggered on every push.
- Parent-child mapping safety: child upsert is retried when parent local id exists but parent external ref is missing.
- Collapse safety: grouped `upsert + close` resolves to `upsert` so create-intent is not dropped before first sync.
- PTY-triggered board refresh is intentionally local-only (session state + PTY-derived fields only), not a remote backend sync trigger.
