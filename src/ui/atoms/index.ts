/**
 * Atoms Index - Re-exports all atoms from submodules
 *
 * This file provides the same API as the original atoms.ts
 * but the atoms are now organized into logical modules.
 */

// Board state atoms
export {
	activeSessionsCountAtom,
	allTasksAtom,
	boardIsLoadingAtom,
	boardTasksAtom,
	boardTasksByColumnAtom,
	drillDownFilteredTasksAtom,
	errorAtom,
	filteredTasksByColumnAtom,
	isRefreshingGitStatsAtom,
	maxVisibleTasksAtom,
	refreshBoardAtom,
	refreshGitStatsAtom,
	selectedTaskIdAtom,
	totalTasksCountAtom,
	viewModeAtom,
} from "./board.js"
// Clock atoms
export { clockTickAtom, elapsedFormattedAtom } from "./clock.js"
// Command queue atoms
export {
	checkTaskBusyAtom,
	commandQueueStateAtom,
	focusedTaskRunningOperationAtom,
	getQueueInfoAtom,
	taskRunningOperationAtom,
} from "./commandQueue.js"
// Config atoms
export { appConfigAtom, workflowModeAtom } from "./config.js"
export type { DevServerView } from "./devServer.js"
// Dev server atoms
export {
	attachDevServerAtom,
	beadDevServerViewsAtom,
	devServersAtom,
	focusedBeadDevServerViewsAtom,
	focusedBeadPrimaryDevServerAtom,
	stopDevServerAtom,
	syncDevServerStateAtom,
	toggleDevServerAtom,
} from "./devServer.js"
export type { DiagnosticsState } from "./diagnostics.js"
// Diagnostics atoms
export { diagnosticsAtom } from "./diagnostics.js"
export type { ImageAttachment } from "./image.js"
// Image attachment atoms
export {
	attachImageClipboardAtom,
	attachImageFileAtom,
	clearCurrentAttachmentsAtom,
	closeImageAttachOverlayAtom,
	closeImagePreviewAtom,
	currentAttachmentsAtom,
	enterImagePathModeAtom,
	exitImagePathModeAtom,
	getAttachmentCountsAtom,
	hasClipboardSupportAtom,
	imageAttachOverlayStateAtom,
	imagePreviewStateAtom,
	listImageAttachmentsAtom,
	loadAttachmentsForTaskAtom,
	openImageAttachmentAtom,
	openImagePreviewAtom,
	previewNextAtom,
	previewPreviousAtom,
	removeImageAttachmentAtom,
	setImagePathInputAtom,
} from "./image.js"
// Keyboard handling atoms
export { handleKeyAtom } from "./keyboard.js"
// Mode service atoms
export {
	activeFilterFieldAtom,
	clearFiltersAtom,
	clearSearchAtom,
	cycleSortAtom,
	enterActionAtom,
	enterFilterAtom,
	enterGotoAtom,
	enterJumpAtom,
	enterOrchestrateAtom,
	enterSearchAtom,
	enterSelectAtom,
	enterSortAtom,
	exitOrchestrateAtom,
	exitSelectAtom,
	exitToNormalAtom,
	filterConfigAtom,
	isFilterAtom,
	isOrchestrateAtom,
	modeAtom,
	orchestrateFocusIndexAtom,
	orchestrateMoveDownAtom,
	orchestrateMoveUpAtom,
	orchestrateSelectAllAtom,
	orchestrateSelectedIdsAtom,
	orchestrateSelectNoneAtom,
	orchestrateSpawnableCountAtom,
	orchestrateStateAtom,
	orchestrateToggleAtom,
	searchQueryAtom,
	selectedIdsAtom,
	setPendingJumpKeyAtom,
	sortConfigAtom,
	toggleSelectionAtom,
	updateSearchAtom,
} from "./mode.js"
// Navigation atoms
export {
	blockerTitlesAtom,
	drillDownChildDetailsAtom,
	drillDownChildIdsAtom,
	drillDownEpicAtom,
	drillDownPhasesAtom,
	enterDrillDownAtom,
	exitDrillDownAtom,
	focusedTaskIdAtom,
	getEpicChildrenAtom,
	getEpicInfoAtom,
	initializeNavigationAtom,
	isTaskBlockedAtom,
	jumpToAtom,
	jumpToTaskAtom,
	navigateAtom,
	taskPhaseInfoAtom,
} from "./navigation.js"
// Network status atoms
export { isOnlineAtom } from "./network.js"
// Overlay and toast atoms
export {
	closeSettingsAtom,
	currentOverlayAtom,
	detailScrollAtom,
	dismissToastAtom,
	moveDownSettingsAtom,
	moveUpSettingsAtom,
	openSettingsAtom,
	openSettingsEditorAtom,
	overlaysAtom,
	popOverlayAtom,
	pushOverlayAtom,
	settingsStateAtom,
	showToastAtom,
	toastsAtom,
	toggleCurrentSettingAtom,
	validateSettingsAfterEditAtom,
} from "./overlay.js"
// Planning workflow atoms
export type { Plan, PlannedTask, PlanningState, ReviewFeedback } from "./planning.js"
export { planningStateAtom, resetPlanningAtom, runPlanningAtom } from "./planning.js"
// PR workflow atoms
export { cleanupAtom, createPRAtom, ghCLIAvailableAtom, mergeToMainAtom } from "./pr.js"
// Project service atoms
export { currentProjectAtom, projectsAtom, switchProjectAtom } from "./project.js"
// Runtime (foundation for all other atoms)
export { appRuntime } from "./runtime.js"
// Session management atoms
export {
	attachExternalAtom,
	attachInlineAtom,
	pauseSessionAtom,
	resumeSessionAtom,
	sessionMetricsAtom,
	sessionMonitorStarterAtom,
	startSessionAtom,
	stopSessionAtom,
} from "./session.js"
// Task CRUD atoms
export {
	claudeCreateSessionAtom,
	createBeadViaEditorAtom,
	createTaskAtom,
	deleteBeadAtom,
	editBeadAtom,
	epicChildrenAtom,
	moveTaskAtom,
	moveTasksAtom,
} from "./task.js"
// VC status atoms
export {
	sendVCCommandAtom,
	toggleVCAutoPilotAtom,
	vcStatusAtom,
	vcStatusPollerAtom,
	vcStatusRefAtom,
} from "./vc.js"
