/**
 * Atoms Index - Re-exports all atoms from submodules
 *
 * This file provides the same API as the original atoms.ts
 * but the atoms are now organized into logical modules.
 */

// Board state atoms
export {
	boardTasksAtom,
	boardTasksByColumnAtom,
	drillDownFilteredTasksAtom,
	errorAtom,
	filteredTasksByColumnAtom,
	refreshBoardAtom,
	selectedTaskIdAtom,
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
	clearCommandAtom,
	clearSearchAtom,
	commandInputAtom,
	cycleSortAtom,
	enterActionAtom,
	enterCommandAtom,
	enterGotoAtom,
	enterJumpAtom,
	enterSearchAtom,
	enterSelectAtom,
	enterSortAtom,
	exitSelectAtom,
	exitToNormalAtom,
	modeAtom,
	searchQueryAtom,
	selectedIdsAtom,
	setPendingJumpKeyAtom,
	sortConfigAtom,
	toggleSelectionAtom,
	updateCommandAtom,
	updateSearchAtom,
} from "./mode.js"
// Navigation atoms
export {
	drillDownChildIdsAtom,
	drillDownEpicAtom,
	enterDrillDownAtom,
	exitDrillDownAtom,
	focusedTaskIdAtom,
	getEpicChildrenAtom,
	getEpicInfoAtom,
	initializeNavigationAtom,
	jumpToAtom,
	jumpToTaskAtom,
	navigateAtom,
} from "./navigation.js"
// Overlay and toast atoms
export {
	currentOverlayAtom,
	dismissToastAtom,
	overlaysAtom,
	popOverlayAtom,
	pushOverlayAtom,
	showToastAtom,
	toastsAtom,
} from "./overlay.js"
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
	hookReceiverStarterAtom,
	pauseSessionAtom,
	resumeSessionAtom,
	sessionMetricsAtom,
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
