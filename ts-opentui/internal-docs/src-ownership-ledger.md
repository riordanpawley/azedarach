# src Ownership Ledger

Status: active (reopened after premature closure)
Date: 2026-03-19
Parent epic: uw

## Close Gate (Hard)

uw cannot close until:
1) Every file under ts-opentui/src is classified as migrated or approved-residual.
2) Pending count is 0.
3) src file count matches approved target.
4) package->src imports are zero except explicitly approved residual paths.

## Execution Plan (v2)

Phase order:
1) `xm` restore green baseline (`type-check` + boundaries)
2) `xi` ledger discipline (always-on)
3) `xh` migrate `src/config` + `src/lib`
4) `xg` migrate `src/core` by ownership tranche
5) `xj` migrate `src/services`
6) `xl` migrate `src/ui` into `packages/tui`
7) `xk` remove `src/runtime` facades and finalize zero package->src

Dependency rules:
- `xi` must remain in progress until all other migration children are closed.
- `xm` must be closed before `xh/xg/xj/xl/xk` can close.
- `xk` starts only after `xh`, `xg`, `xj`, and `xl` are complete.
- No child can close unless ledger entries it touched are updated from `pending` to `migrated` or `approved-residual`.

Per-phase gates:
- Required per slice:
  - `cd ts-opentui && bun run type-check`
  - `cd ts-opentui && bun run check:boundaries`
  - focused tests for touched surfaces
- Required for integration checkpoints:
  - `cd ts-opentui && bun run check:ci`
- Required evidence:
  - changed file list
  - commands run + outcomes
  - ledger delta (what moved, what remains)

Risk controls:
- No temporary facade additions without a tracked removal owner + child issue.
- No acceptance drift: if target changes, update child AC before code changes.
- No “green-check illusion”: package-only scans are insufficient without ledger-count delta.
- No parent close while any `pending` ledger entries remain.

## Baseline Snapshot

- src total files: 225
- by top-level dir:
  - config: 5
  - core: 61
  - lib: 8
  - runtime: 3
  - services: 51
  - ui: 97

- current package->src import edges:
```
ts-opentui/packages/cli/src/ui-launch.d.ts:1:declare module "../../../src/ui/launch.js" {
ts-opentui/packages/cli/src/runtimeServices.ts:29:} from "../../../src/runtime/appServicesFacade.js"
ts-opentui/packages/cli/src/runtimeServices.ts:61:} from "../../../src/runtime/coreServicesFacade.js"
ts-opentui/packages/daemon/src/BackendDaemonControlService.ts:3:import type { TaskWithSession } from "../../../src/ui/types.js"
ts-opentui/packages/daemon/src/runtimeServices.ts:8:} from "../../../src/runtime/coreServicesFacade.js"
ts-opentui/packages/tui/src/utils/runtimeServices.ts:29:} from "../../../../src/runtime/appServicesFacade.js"
ts-opentui/packages/tui/src/utils/runtimeServices.ts:59:} from "../../../../src/runtime/coreServicesFacade.js"
```

## Classification Legend

- status: pending | migrated | approved-residual
- owner-child: xg (core), xj (services), xl (ui), xh (config+lib), xk (runtime facade removal)

## config

- [x] status=approved-residual owner-child=xh path=src/config/AppConfig.ts
- [x] status=approved-residual owner-child=xh path=src/config/defaults.ts
- [x] status=approved-residual owner-child=xh path=src/config/index.ts
- [x] status=approved-residual owner-child=xh path=src/config/schema.test.ts
- [x] status=approved-residual owner-child=xh path=src/config/schema.ts

## core

- [ ] status=pending owner-child= path=src/core/AttachmentService.ts
- [ ] status=pending owner-child= path=src/core/BackendClientSessionProtocol.test.ts
- [ ] status=pending owner-child= path=src/core/BackendClientSessionProtocol.ts
- [ ] status=pending owner-child= path=src/core/BackendSyncInterface.ts
- [ ] status=pending owner-child= path=src/core/BackendSyncLinear.integration.test.ts
- [ ] status=pending owner-child= path=src/core/BackendSyncLinear.ts
- [ ] status=pending owner-child= path=src/core/BackendSyncRouter.ts
- [ ] status=pending owner-child= path=src/core/CliToolRegistry.test.ts
- [ ] status=pending owner-child= path=src/core/CliToolRegistry.ts
- [ ] status=pending owner-child= path=src/core/FileLockManager.test.ts
- [ ] status=pending owner-child= path=src/core/FileLockManager.ts
- [ ] status=pending owner-child= path=src/core/GlobalDaemonDiscovery.test.ts
- [ ] status=pending owner-child= path=src/core/ImageAttachmentService.ts
- [ ] status=pending owner-child= path=src/core/IssueEditorService.ts
- [ ] status=pending owner-child= path=src/core/IssueImplementations.test.ts
- [ ] status=pending owner-child= path=src/core/IssueImplementations.ts
- [ ] status=pending owner-child= path=src/core/IssueSyncService.test.ts
- [ ] status=pending owner-child= path=src/core/IssueSyncService.ts
- [ ] status=pending owner-child= path=src/core/IssueTrackerClient.linear-type.test.ts
- [ ] status=pending owner-child= path=src/core/IssueTrackerClient.test.ts
- [ ] status=pending owner-child= path=src/core/IssueTrackerClient.ts
- [ ] status=pending owner-child= path=src/core/LinearSdk.test.ts
- [ ] status=pending owner-child= path=src/core/LinearSdk.ts
- [ ] status=pending owner-child= path=src/core/LinearSyncThrottle.test.ts
- [ ] status=pending owner-child= path=src/core/LinearSyncThrottle.ts
- [ ] status=pending owner-child= path=src/core/LocalIssueStore.test.ts
- [ ] status=pending owner-child= path=src/core/LocalIssueStore.ts
- [ ] status=pending owner-child= path=src/core/PRWorkflow.ts
- [ ] status=pending owner-child= path=src/core/PTYMonitor.ts
- [ ] status=pending owner-child= path=src/core/PlanningService.ts
- [ ] status=pending owner-child= path=src/core/SessionManager.recovery.test.ts
- [ ] status=pending owner-child= path=src/core/SessionManager.ts
- [ ] status=pending owner-child= path=src/core/SessionStateStore.test.ts
- [ ] status=pending owner-child= path=src/core/SessionStateStore.ts
- [ ] status=pending owner-child= path=src/core/SpecService.test.ts
- [ ] status=pending owner-child= path=src/core/SpecService.ts
- [ ] status=pending owner-child= path=src/core/StateDetector.ts
- [ ] status=pending owner-child= path=src/core/TemplateService.ts
- [ ] status=pending owner-child= path=src/core/TerminalService.ts
- [ ] status=pending owner-child= path=src/core/TmuxCapabilities.test.ts
- [ ] status=pending owner-child= path=src/core/TmuxCapabilities.ts
- [ ] status=pending owner-child= path=src/core/TmuxService.ts
- [ ] status=pending owner-child= path=src/core/TmuxSessionMonitor.ts
- [ ] status=pending owner-child= path=src/core/VCService.ts
- [ ] status=pending owner-child= path=src/core/WorktreeManager.branchNaming.test.ts
- [ ] status=pending owner-child= path=src/core/WorktreeManager.lookup.test.ts
- [ ] status=pending owner-child= path=src/core/WorktreeManager.ts
- [ ] status=pending owner-child= path=src/core/WorktreeSessionService.test.ts
- [ ] status=pending owner-child= path=src/core/WorktreeSessionService.ts
- [ ] status=pending owner-child= path=src/core/dependencyPhases.ts
- [ ] status=pending owner-child= path=src/core/gitRecovery.test.ts
- [ ] status=pending owner-child= path=src/core/gitRecovery.ts
- [ ] status=pending owner-child= path=src/core/hooks.test.ts
- [ ] status=pending owner-child= path=src/core/hooks.ts
- [ ] status=pending owner-child= path=src/core/paths.test.ts
- [ ] status=pending owner-child= path=src/core/paths.ts
- [ ] status=pending owner-child= path=src/core/ptyHeuristics.test.ts
- [ ] status=pending owner-child= path=src/core/ptyHeuristics.ts
- [ ] status=pending owner-child= path=src/core/shell.ts
- [ ] status=pending owner-child= path=src/core/specTypes.ts
- [x] status=approved-residual owner-child=xg path=src/core/storagePaths.ts

## lib

- [x] status=approved-residual owner-child=xh path=src/lib/ansi.ts
- [x] status=approved-residual owner-child=xh path=src/lib/editorPopupState.ts
- [x] status=approved-residual owner-child=xh path=src/lib/empty.ts
- [x] status=approved-residual owner-child=xh path=src/lib/runtimeControl.ts
- [x] status=approved-residual owner-child=xh path=src/lib/taskTypes.ts
- [x] status=approved-residual owner-child=xh path=src/lib/tmux-wrap.ts
- [x] status=approved-residual owner-child=xh path=src/lib/waitingSessions.test.ts
- [x] status=approved-residual owner-child=xh path=src/lib/waitingSessions.ts

## runtime

- [ ] status=pending owner-child= path=src/runtime/appServicesFacade.ts
- [ ] status=pending owner-child= path=src/runtime/coreServicesFacade.ts
- [ ] status=pending owner-child= path=src/runtime/daemonOperationsPolicy.ts

## services

- [ ] status=pending owner-child= path=src/services/BoardService.daemonIpc.test.ts
- [ ] status=pending owner-child= path=src/services/BoardService.recovery.test.ts
- [ ] status=pending owner-child= path=src/services/BoardService.retrySchedule.vitest.ts
- [ ] status=pending owner-child= path=src/services/BoardService.ts
- [ ] status=pending owner-child= path=src/services/ClockService.ts
- [ ] status=pending owner-child= path=src/services/CommandQueueService.test.ts
- [ ] status=pending owner-child= path=src/services/CommandQueueService.ts
- [ ] status=pending owner-child= path=src/services/DevServerService.ts
- [ ] status=pending owner-child= path=src/services/DiagnosticsService.test.ts
- [ ] status=pending owner-child= path=src/services/DiagnosticsService.ts
- [ ] status=pending owner-child= path=src/services/DiffService.ts
- [ ] status=pending owner-child= path=src/services/EditorService.specWorkspace.test.ts
- [ ] status=pending owner-child= path=src/services/EditorService.ts
- [ ] status=pending owner-child= path=src/services/ErrorFormatter.test.ts
- [ ] status=pending owner-child= path=src/services/ErrorFormatter.ts
- [ ] status=pending owner-child= path=src/services/GitSyncService.test.ts
- [ ] status=pending owner-child= path=src/services/GitSyncService.ts
- [ ] status=pending owner-child= path=src/services/KeyboardService.ts
- [ ] status=pending owner-child= path=src/services/LinearWebhookService.integration.test.ts
- [ ] status=pending owner-child= path=src/services/LinearWebhookService.test.ts
- [ ] status=pending owner-child= path=src/services/LinearWebhookService.ts
- [ ] status=pending owner-child= path=src/services/MutationQueue.ts
- [ ] status=pending owner-child= path=src/services/NavigationService.ts
- [ ] status=pending owner-child= path=src/services/NetworkService.ts
- [ ] status=pending owner-child= path=src/services/OfflineService.ts
- [ ] status=pending owner-child= path=src/services/OverlayService.ts
- [ ] status=pending owner-child= path=src/services/PRStateService.ts
- [ ] status=pending owner-child= path=src/services/ProjectService.test.ts
- [x] status=approved-residual owner-child=xj path=src/services/ProjectService.ts
- [ ] status=pending owner-child= path=src/services/ProjectStateService.ts
- [ ] status=pending owner-child= path=src/services/SessionService.ts
- [ ] status=pending owner-child= path=src/services/SettingsService.test.ts
- [ ] status=pending owner-child= path=src/services/SettingsService.ts
- [x] status=approved-residual owner-child=xj path=src/services/ToastService.ts
- [ ] status=pending owner-child= path=src/services/ViewService.ts
- [ ] status=pending owner-child= path=src/services/keyboard/DevServerHandlersService.ts
- [ ] status=pending owner-child= path=src/services/keyboard/InputHandlersService.ts
- [ ] status=pending owner-child= path=src/services/keyboard/KeyboardHelpersService.ts
- [ ] status=pending owner-child= path=src/services/keyboard/OrchestrateHandlersService.ts
- [ ] status=pending owner-child= path=src/services/keyboard/PRHandlersService.ts
- [ ] status=pending owner-child= path=src/services/keyboard/SessionHandlersService.ts
- [ ] status=pending owner-child= path=src/services/keyboard/SessionPrompt.test.ts
- [ ] status=pending owner-child= path=src/services/keyboard/SessionPrompt.ts
- [ ] status=pending owner-child= path=src/services/keyboard/TaskHandlersService.ts
- [ ] status=pending owner-child= path=src/services/keyboard/bindings.ts
- [ ] status=pending owner-child= path=src/services/keyboard/types.ts
- [ ] status=pending owner-child= path=src/services/projectUiState.ts
- [ ] status=pending owner-child= path=src/services/sessionRecoveryRetrySchedule.ts
- [ ] status=pending owner-child= path=src/services/transientError.ts
- [ ] status=pending owner-child= path=src/services/transientRetrySchedule.ts
- [ ] status=pending owner-child= path=src/services/transientRetrySchedule.vitest.ts

## ui

- [ ] status=pending owner-child= path=src/ui/AICreatePrompt.tsx
- [ ] status=pending owner-child= path=src/ui/ActionPalette.tsx
- [ ] status=pending owner-child= path=src/ui/App.tsx
- [ ] status=pending owner-child= path=src/ui/Board.tsx
- [ ] status=pending owner-child= path=src/ui/BulkCleanupOverlay.tsx
- [ ] status=pending owner-child= path=src/ui/Column.tsx
- [ ] status=pending owner-child= path=src/ui/CompactView.tsx
- [ ] status=pending owner-child= path=src/ui/ConfirmOverlay.tsx
- [ ] status=pending owner-child= path=src/ui/CreateTaskPrompt.tsx
- [ ] status=pending owner-child= path=src/ui/DetailPanel.tsx
- [ ] status=pending owner-child= path=src/ui/DevServerMenu.tsx
- [ ] status=pending owner-child= path=src/ui/DiagnosticsOverlay.tsx
- [ ] status=pending owner-child= path=src/ui/DiffViewer/DiffViewer.tsx
- [ ] status=pending owner-child= path=src/ui/DiffViewer/FilePicker.tsx
- [ ] status=pending owner-child= path=src/ui/DiffViewer/fileTree.ts
- [ ] status=pending owner-child= path=src/ui/DiffViewer/index.ts
- [ ] status=pending owner-child= path=src/ui/DiffViewer/types.ts
- [ ] status=pending owner-child= path=src/ui/ElapsedTimer.tsx
- [ ] status=pending owner-child= path=src/ui/EpicHeader.tsx
- [ ] status=pending owner-child= path=src/ui/FilterMenu.tsx
- [ ] status=pending owner-child= path=src/ui/ForkOverlay.tsx
- [ ] status=pending owner-child= path=src/ui/GitPullOverlay.tsx
- [ ] status=pending owner-child= path=src/ui/HelpOverlay.tsx
- [ ] status=pending owner-child= path=src/ui/ImageAttachOverlay.tsx
- [ ] status=pending owner-child= path=src/ui/ImagePreviewOverlay.tsx
- [ ] status=pending owner-child= path=src/ui/MergeChoiceOverlay.tsx
- [ ] status=pending owner-child= path=src/ui/OrchestrationOverlay.tsx
- [ ] status=pending owner-child= path=src/ui/PhaseSeparator.tsx
- [ ] status=pending owner-child= path=src/ui/PlanningOverlay.tsx
- [ ] status=pending owner-child= path=src/ui/ProjectSelector.tsx
- [ ] status=pending owner-child= path=src/ui/SearchInput.tsx
- [ ] status=pending owner-child= path=src/ui/SettingsOverlay.tsx
- [ ] status=pending owner-child= path=src/ui/SortMenu.tsx
- [ ] status=pending owner-child= path=src/ui/SpecWorkspace.tsx
- [ ] status=pending owner-child= path=src/ui/StatusBar.tsx
- [ ] status=pending owner-child= path=src/ui/TaskCard.tsx
- [ ] status=pending owner-child= path=src/ui/Toast.tsx
- [ ] status=pending owner-child= path=src/ui/WaitingSessionPicker.tsx
- [ ] status=pending owner-child= path=src/ui/actionPaletteTmuxMode.test.ts
- [ ] status=pending owner-child= path=src/ui/atoms.ts
- [ ] status=pending owner-child= path=src/ui/atoms/board.ts
- [ ] status=pending owner-child= path=src/ui/atoms/clock.ts
- [ ] status=pending owner-child= path=src/ui/atoms/commandQueue.ts
- [ ] status=pending owner-child= path=src/ui/atoms/config.ts
- [ ] status=pending owner-child= path=src/ui/atoms/devServer.ts
- [ ] status=pending owner-child= path=src/ui/atoms/diagnostics.ts
- [ ] status=pending owner-child= path=src/ui/atoms/diff.ts
- [ ] status=pending owner-child= path=src/ui/atoms/gitSync.ts
- [ ] status=pending owner-child= path=src/ui/atoms/image.ts
- [ ] status=pending owner-child= path=src/ui/atoms/index.ts
- [ ] status=pending owner-child= path=src/ui/atoms/keyboard.ts
- [ ] status=pending owner-child= path=src/ui/atoms/mode.ts
- [ ] status=pending owner-child= path=src/ui/atoms/mouse.ts
- [ ] status=pending owner-child= path=src/ui/atoms/navigation.ts
- [ ] status=pending owner-child= path=src/ui/atoms/network.ts
- [ ] status=pending owner-child= path=src/ui/atoms/overlay.ts
- [ ] status=pending owner-child= path=src/ui/atoms/planning.ts
- [ ] status=pending owner-child= path=src/ui/atoms/pr.ts
- [ ] status=pending owner-child= path=src/ui/atoms/project.ts
- [ ] status=pending owner-child= path=src/ui/atoms/runtime.test.ts
- [ ] status=pending owner-child= path=src/ui/atoms/runtime.ts
- [ ] status=pending owner-child= path=src/ui/atoms/session.ts
- [ ] status=pending owner-child= path=src/ui/atoms/spec.ts
- [ ] status=pending owner-child= path=src/ui/atoms/startup.ts
- [ ] status=pending owner-child= path=src/ui/atoms/task.test.ts
- [ ] status=pending owner-child= path=src/ui/atoms/task.ts
- [ ] status=pending owner-child= path=src/ui/atoms/vc.ts
- [ ] status=pending owner-child= path=src/ui/atoms/waitingSessions.ts
- [ ] status=pending owner-child= path=src/ui/components/VirtualList.tsx
- [ ] status=pending owner-child= path=src/ui/components/index.ts
- [ ] status=pending owner-child= path=src/ui/diagnosticsOverlayLayout.test.ts
- [ ] status=pending owner-child= path=src/ui/diagnosticsOverlayLayout.ts
- [ ] status=pending owner-child= path=src/ui/diagnosticsOverlayScroll.ts
- [ ] status=pending owner-child= path=src/ui/diagnosticsText.test.ts
- [ ] status=pending owner-child= path=src/ui/diagnosticsText.ts
- [ ] status=pending owner-child= path=src/ui/hooks/index.ts
- [ ] status=pending owner-child= path=src/ui/hooks/useEditorMode.ts
- [ ] status=pending owner-child= path=src/ui/hooks/useNavigation.ts
- [ ] status=pending owner-child= path=src/ui/hooks/useOverlays.ts
- [ ] status=pending owner-child= path=src/ui/hooks/usePaste.ts
- [ ] status=pending owner-child= path=src/ui/hooks/useToasts.ts
- [ ] status=pending owner-child= path=src/ui/launch.test.ts
- [ ] status=pending owner-child= path=src/ui/launch.tsx
- [ ] status=pending owner-child= path=src/ui/logMaintenance.test.ts
- [ ] status=pending owner-child= path=src/ui/logMaintenance.ts
- [ ] status=pending owner-child= path=src/ui/quitFallbackPolicy.test.ts
- [ ] status=pending owner-child= path=src/ui/quitFallbackPolicy.ts
- [ ] status=pending owner-child= path=src/ui/responsive.ts
- [ ] status=pending owner-child= path=src/ui/runtimeControl.test.ts
- [ ] status=pending owner-child= path=src/ui/runtimeControl.ts
- [ ] status=pending owner-child= path=src/ui/statusBarFormatting.test.ts
- [ ] status=pending owner-child= path=src/ui/statusBarFormatting.ts
- [ ] status=pending owner-child= path=src/ui/statusTokens.test.ts
- [ ] status=pending owner-child= path=src/ui/statusTokens.ts
- [ ] status=pending owner-child= path=src/ui/theme.ts
- [ ] status=pending owner-child= path=src/ui/types.sessionPresence.test.ts
- [ ] status=pending owner-child= path=src/ui/types.ts
