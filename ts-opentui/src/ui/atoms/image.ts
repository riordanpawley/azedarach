/**
 * Image Attachment Atoms
 *
 * Handles image attachment management including file attachment,
 * clipboard paste, preview, and removal.
 */

import { Effect } from "effect"
import { type ImageAttachment, ImageAttachmentService } from "../../core/ImageAttachmentService.js"
import { appRuntime } from "./runtime.js"

// Re-export ImageAttachment type for components
export type { ImageAttachment }

// ============================================================================
// Image Attachment State Atoms
// ============================================================================

/**
 * Reactive state for the currently viewed task's attachments.
 * Subscribe to this in DetailPanel for automatic updates.
 */
export const currentAttachmentsAtom = appRuntime.subscriptionRef(
	Effect.gen(function* () {
		const service = yield* ImageAttachmentService
		return service.currentAttachments
	}),
)

/**
 * Image attach overlay state.
 * Subscribe to this in ImageAttachOverlay for reactive updates.
 */
export const imageAttachOverlayStateAtom = appRuntime.subscriptionRef(
	Effect.gen(function* () {
		const service = yield* ImageAttachmentService
		return service.overlayState
	}),
)

/**
 * Image preview overlay state.
 * Subscribe to this in ImagePreviewOverlay for reactive updates.
 */
export const imagePreviewStateAtom = appRuntime.subscriptionRef(
	Effect.gen(function* () {
		const service = yield* ImageAttachmentService
		return service.previewState
	}),
)

/**
 * Check if clipboard tools are available
 *
 * Usage: const hasClipboard = await checkClipboardSupport()
 */
export const hasClipboardSupportAtom = appRuntime.atom(
	Effect.gen(function* () {
		const service = yield* ImageAttachmentService
		return yield* service.hasClipboardSupport()
	}),
	{ initialValue: false },
)

// ============================================================================
// Image Attachment Action Atoms
// ============================================================================

/**
 * Load attachments for a task and update reactive state.
 * Called when detail panel opens.
 */
export const loadAttachmentsForTaskAtom = appRuntime.fn((taskId: string) =>
	Effect.gen(function* () {
		const service = yield* ImageAttachmentService
		return yield* service.loadForTask(taskId)
	}).pipe(Effect.tapError(Effect.logError)),
)

/**
 * Clear current attachments state.
 * Called when detail panel closes.
 */
export const clearCurrentAttachmentsAtom = appRuntime.fn(() =>
	Effect.gen(function* () {
		const service = yield* ImageAttachmentService
		yield* service.clearCurrent()
	}).pipe(Effect.catchAll(Effect.logError)),
)

/**
 * Open the image attach overlay for a task
 */
export const openImageAttachOverlayAtom = appRuntime.fn((taskId: string) =>
	Effect.gen(function* () {
		const service = yield* ImageAttachmentService
		yield* service.openOverlay(taskId)
	}).pipe(Effect.catchAll(Effect.logError)),
)

/**
 * Close the image attach overlay
 */
export const closeImageAttachOverlayAtom = appRuntime.fn(() =>
	Effect.gen(function* () {
		const service = yield* ImageAttachmentService
		yield* service.closeOverlay()
	}).pipe(Effect.catchAll(Effect.logError)),
)

/**
 * Enter path input mode in image attach overlay
 */
export const enterImagePathModeAtom = appRuntime.fn(() =>
	Effect.gen(function* () {
		const service = yield* ImageAttachmentService
		yield* service.enterPathMode()
	}).pipe(Effect.catchAll(Effect.logError)),
)

/**
 * Exit path input mode in image attach overlay
 */
export const exitImagePathModeAtom = appRuntime.fn(() =>
	Effect.gen(function* () {
		const service = yield* ImageAttachmentService
		yield* service.exitPathMode()
	}).pipe(Effect.catchAll(Effect.logError)),
)

/**
 * Update path input value in image attach overlay
 */
export const setImagePathInputAtom = appRuntime.fn((value: string) =>
	Effect.gen(function* () {
		const service = yield* ImageAttachmentService
		yield* service.setPathInput(value)
	}).pipe(Effect.catchAll(Effect.logError)),
)

/**
 * List image attachments for a task
 *
 * Usage: const attachments = await listAttachments(taskId)
 */
export const listImageAttachmentsAtom = appRuntime.fn((taskId: string) =>
	Effect.gen(function* () {
		const service = yield* ImageAttachmentService
		return yield* service.list(taskId)
	}).pipe(Effect.tapError(Effect.logError)),
)

/**
 * Attach image from file path
 *
 * Usage: await attachImageFile({ taskId: "az-123", filePath: "/path/to/image.png" })
 */
export const attachImageFileAtom = appRuntime.fn(
	({ taskId, filePath }: { taskId: string; filePath: string }) =>
		Effect.gen(function* () {
			const service = yield* ImageAttachmentService
			return yield* service.attachFile(taskId, filePath)
		}).pipe(Effect.tapError(Effect.logError)),
)

/**
 * Attach image from clipboard
 *
 * Usage: await attachImageClipboard(taskId)
 */
export const attachImageClipboardAtom = appRuntime.fn((taskId: string) =>
	Effect.gen(function* () {
		const service = yield* ImageAttachmentService
		return yield* service.attachFromClipboard(taskId)
	}).pipe(Effect.tapError(Effect.logError)),
)

/**
 * Remove an image attachment
 *
 * Usage: await removeImageAttachment({ taskId: "az-123", attachmentId: "abc123" })
 */
export const removeImageAttachmentAtom = appRuntime.fn(
	({ taskId, attachmentId }: { taskId: string; attachmentId: string }) =>
		Effect.gen(function* () {
			const service = yield* ImageAttachmentService
			yield* service.remove(taskId, attachmentId)
		}).pipe(Effect.catchAll(Effect.logError)),
)

/**
 * Open image attachment in default viewer
 *
 * Usage: await openImageAttachment({ taskId: "az-123", attachmentId: "abc123" })
 */
export const openImageAttachmentAtom = appRuntime.fn(
	({ taskId, attachmentId }: { taskId: string; attachmentId: string }) =>
		Effect.gen(function* () {
			const service = yield* ImageAttachmentService
			yield* service.open(taskId, attachmentId)
		}).pipe(Effect.catchAll(Effect.logError)),
)

/**
 * Get attachment counts for all tasks (batch)
 *
 * Usage: const counts = await getAttachmentCounts(taskIds)
 */
export const getAttachmentCountsAtom = appRuntime.fn((taskIds: readonly string[]) =>
	Effect.gen(function* () {
		const service = yield* ImageAttachmentService
		return yield* service.countBatch(taskIds)
	}).pipe(Effect.tapError(Effect.logError)),
)

// ============================================================================
// Image Preview Atoms
// ============================================================================

/**
 * Open image preview for the currently selected attachment.
 * Renders the image to terminal-compatible text.
 */
export const openImagePreviewAtom = appRuntime.fn(() =>
	Effect.gen(function* () {
		const service = yield* ImageAttachmentService
		return yield* service.openPreview()
	}).pipe(Effect.tapError(Effect.logError)),
)

/**
 * Close image preview and clear state
 */
export const closeImagePreviewAtom = appRuntime.fn(() =>
	Effect.gen(function* () {
		const service = yield* ImageAttachmentService
		yield* service.closePreview()
	}).pipe(Effect.catchAll(Effect.logError)),
)

/**
 * Navigate to next attachment in preview
 */
export const previewNextAtom = appRuntime.fn(() =>
	Effect.gen(function* () {
		const service = yield* ImageAttachmentService
		yield* service.previewNext()
	}).pipe(Effect.catchAll(Effect.logError)),
)

/**
 * Navigate to previous attachment in preview
 */
export const previewPreviousAtom = appRuntime.fn(() =>
	Effect.gen(function* () {
		const service = yield* ImageAttachmentService
		yield* service.previewPrevious()
	}).pipe(Effect.catchAll(Effect.logError)),
)
