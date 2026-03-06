/**
 * ImageAttachmentService - Effect service for managing image attachments on tasks
 *
 * Images are stored in the local SQLite issue store as BLOBs.
 * Filesystem paths are materialized on-demand under .azedarach/tmp/attachments/
 * for tools that require file paths (viewer integration + Claude Read tool).
 */

import { Command, type CommandExecutor, FileSystem, Path } from "@effect/platform"
import { Data, Effect, Schema, SubscriptionRef } from "effect"
import { ProjectService } from "../services/ProjectService.js"
import { IssueTrackerClient } from "./IssueTrackerClient.js"
import { LocalIssueStore } from "./LocalIssueStore.js"

// ============================================================================
// Schema Definitions
// ============================================================================

/**
 * Single image attachment metadata
 */
const ImageAttachmentSchema = Schema.Struct({
	id: Schema.String,
	filename: Schema.String,
	originalPath: Schema.String,
	mimeType: Schema.String,
	size: Schema.Number,
	createdAt: Schema.String,
})

export type ImageAttachment = Schema.Schema.Type<typeof ImageAttachmentSchema>

// ============================================================================
// Error Types
// ============================================================================

export class ImageAttachmentError extends Data.TaggedError("ImageAttachmentError")<{
	readonly message: string
	readonly cause?: unknown
}> {}

export class ClipboardError extends Data.TaggedError("ClipboardError")<{
	readonly message: string
	readonly tool?: string
}> {}

export class FileNotFoundError extends Data.TaggedError("FileNotFoundError")<{
	readonly path: string
}> {}

// ============================================================================
// Constants
// ============================================================================

const ATTACHMENTS_TMP_DIR = ".azedarach/tmp/attachments"
const CLIPBOARD_TMP_DIR = ".azedarach/tmp/clipboard"

// Supported image extensions
const IMAGE_EXTENSIONS = [".png", ".jpg", ".jpeg", ".gif", ".webp", ".bmp", ".svg"]

// ============================================================================
// Service Implementation
// ============================================================================

/**
 * ImageAttachmentService
 *
 * Manages image attachments for tracker tasks. Since the tracker CLI doesn't support
 * file attachments, we store images in a local directory structure and track
 * metadata in a JSON index file.
 *
 * Features:
 * - Attach images by file path (copies to storage)
 * - Paste images from clipboard (xclip/wl-paste)
 * - List attachments for a task
 * - Open attachments in default viewer
 * - Remove attachments
 */
export class ImageAttachmentService extends Effect.Service<ImageAttachmentService>()(
	"ImageAttachmentService",
	{
		dependencies: [IssueTrackerClient.Default, LocalIssueStore.Default, ProjectService.Default],
		effect: Effect.gen(function* () {
			const fs = yield* FileSystem.FileSystem
			const path = yield* Path.Path
			const issueTrackerClient = yield* IssueTrackerClient
			const localIssueStore = yield* LocalIssueStore
			const projectService = yield* ProjectService

			/**
			 * Get the project path from ProjectService, falling back to process.cwd()
			 */
			const getProjectPath = (): Effect.Effect<string> =>
				Effect.gen(function* () {
					const projectPath = yield* projectService.getCurrentPath()
					return projectPath ?? process.cwd()
				})

			const getTmpBaseDir = (projectRootPath?: string) =>
				projectRootPath
					? Effect.succeed(path.join(projectRootPath, ATTACHMENTS_TMP_DIR))
					: Effect.map(getProjectPath(), (projectPath) =>
							path.join(projectPath, ATTACHMENTS_TMP_DIR),
						)
			const getTmpIssueDir = (issueId: string, projectRootPath?: string) =>
				Effect.map(getTmpBaseDir(projectRootPath), (baseDir) => path.join(baseDir, issueId))
			const getTmpAttachmentDir = (
				issueId: string,
				attachmentId: string,
				projectRootPath?: string,
			) =>
				Effect.map(getTmpIssueDir(issueId, projectRootPath), (issueDir) =>
					path.join(issueDir, attachmentId),
				)

			const getClipboardTmpDir = () =>
				Effect.map(getProjectPath(), (projectPath) => path.join(projectPath, CLIPBOARD_TMP_DIR))

			const removeIssueTmpDir = (issueId: string) =>
				Effect.gen(function* () {
					const issueTmpDir = yield* getTmpIssueDir(issueId)
					yield* fs.remove(issueTmpDir, { recursive: true }).pipe(Effect.ignore)
					yield* fs.remove(issueTmpDir).pipe(Effect.ignore)
				})

			const materializeAttachment = (
				issueId: string,
				attachment: ImageAttachment,
				projectRootPath?: string,
			) =>
				Effect.gen(function* () {
					const record = yield* localIssueStore.getIssueAttachment(issueId, attachment.id)
					if (!record) {
						return yield* Effect.fail(
							new ImageAttachmentError({
								message: `Attachment not found: ${attachment.id}`,
							}),
						)
					}

					const attachmentDir = yield* getTmpAttachmentDir(issueId, attachment.id, projectRootPath)
					yield* fs.makeDirectory(attachmentDir, { recursive: true }).pipe(Effect.ignore)
					const filePath = path.join(attachmentDir, attachment.filename)
					yield* fs.writeFile(filePath, record.content)
					return filePath
				})

			/**
			 * Generate a unique ID for an attachment
			 */
			const generateId = () => {
				const timestamp = Date.now().toString(36)
				const random = Math.random().toString(36).substring(2, 6)
				return `${timestamp}-${random}`
			}

			/**
			 * Get MIME type from filename
			 */
			const getMimeType = (filename: string): string => {
				const ext = path.extname(filename).toLowerCase()
				switch (ext) {
					case ".png":
						return "image/png"
					case ".jpg":
					case ".jpeg":
						return "image/jpeg"
					case ".gif":
						return "image/gif"
					case ".webp":
						return "image/webp"
					case ".bmp":
						return "image/bmp"
					case ".svg":
						return "image/svg+xml"
					default:
						return "application/octet-stream"
				}
			}

			/**
			 * Check if a file is an image based on extension
			 */
			const isImageFile = (filename: string): boolean => {
				const ext = path.extname(filename).toLowerCase()
				return IMAGE_EXTENSIONS.includes(ext)
			}

			/**
			 * Detect clipboard tool availability
			 * Returns: "pbpaste" (macOS) | "wl-paste" (Wayland) | "xclip" (X11) | null
			 */
			const detectClipboardTool = Effect.gen(function* () {
				// macOS: pbpaste is always available
				if (process.platform === "darwin") {
					const pbpasteCheck = yield* Command.make("which", "pbpaste").pipe(
						Command.exitCode,
						Effect.catchAll(() => Effect.succeed(1)),
					)

					if (pbpasteCheck === 0) {
						return "pbpaste" as const
					}
				}

				// Try wl-paste (Wayland)
				const wlPasteCheck = yield* Command.make("which", "wl-paste").pipe(
					Command.exitCode,
					Effect.catchAll(() => Effect.succeed(1)),
				)

				if (wlPasteCheck === 0) {
					return "wl-paste" as const
				}

				// Try xclip (X11)
				const xclipCheck = yield* Command.make("which", "xclip").pipe(
					Command.exitCode,
					Effect.catchAll(() => Effect.succeed(1)),
				)

				if (xclipCheck === 0) {
					return "xclip" as const
				}

				return null
			})

			/**
			 * Update bead notes to include link to attached image.
			 * Appends a markdown-style link to the notes field.
			 */
			const linkAttachmentInNotes = (
				issueId: string,
				attachment: ImageAttachment,
			): Effect.Effect<void, unknown, CommandExecutor.CommandExecutor> =>
				Effect.gen(function* () {
					// Build relative path from project root
					const relativePath = `.azedarach/tmp/attachments/${issueId}/${attachment.id}/${attachment.filename}`

					// Create a formatted attachment entry
					const timestamp = new Date(attachment.createdAt).toLocaleString()
					const source = attachment.originalPath === "clipboard" ? "clipboard" : "file"
					const attachmentLine = `📎 [${attachment.filename}](${relativePath}) (${source}, ${timestamp})`

					// Get current issue to read existing notes
					const issue = yield* issueTrackerClient
						.show(issueId)
						.pipe(Effect.catchAll(() => Effect.succeed(null)))

					// Append to existing notes or create new
					const existingNotes = issue?.notes ?? ""
					const separator = existingNotes.trim() ? "\n" : ""
					const newNotes = `${existingNotes}${separator}${attachmentLine}`

					// Update the bead with new notes
					yield* issueTrackerClient
						.update(issueId, { notes: newNotes })
						.pipe(
							Effect.catchAll((error) =>
								Effect.logWarning(`Failed to update bead notes: ${error}`),
							),
						)
				})

			/**
			 * Remove attachment link from bead notes when image is deleted.
			 * Removes lines matching the pattern: 📎 [filename](...)
			 */
			const unlinkAttachmentFromNotes = (
				issueId: string,
				attachment: ImageAttachment,
			): Effect.Effect<void, unknown, CommandExecutor.CommandExecutor> =>
				Effect.gen(function* () {
					// Get current issue to read existing notes
					const issue = yield* issueTrackerClient
						.show(issueId)
						.pipe(Effect.catchAll(() => Effect.succeed(null)))

					const existingNotes = issue?.notes
					if (!existingNotes) return

					// Remove lines that start with the attachment link pattern for this file
					// Pattern: 📎 [filename](path) (source, timestamp)
					const lines = existingNotes.split("\n")
					const attachmentPrefix = `📎 [${attachment.filename}]`
					const filteredLines = lines.filter((line) => !line.startsWith(attachmentPrefix))

					// Only update if we actually removed something
					if (filteredLines.length === lines.length) return

					const newNotes = filteredLines.join("\n").trim()

					// Update the bead with cleaned notes
					yield* issueTrackerClient
						.update(issueId, { notes: newNotes || "" })
						.pipe(
							Effect.catchAll((error) =>
								Effect.logWarning(`Failed to update bead notes: ${error}`),
							),
						)
				})

			// ========================================================================
			// Reactive State
			// ========================================================================

			/**
			 * State for the currently viewed task's attachments.
			 * Updated when detail panel opens for a task.
			 * selectedIndex: -1 means no attachment is selected (focus on task details)
			 */
			const currentAttachments = yield* SubscriptionRef.make<{
				readonly taskId: string
				readonly attachments: readonly ImageAttachment[]
				readonly selectedIndex: number
			} | null>(null)

			/**
			 * State for the image attach overlay.
			 */
			const overlayState = yield* SubscriptionRef.make<{
				readonly mode: "menu" | "path"
				readonly pathInput: string
				readonly isAttaching: boolean
				readonly taskId: string | null
			}>({
				mode: "menu",
				pathInput: "",
				isAttaching: false,
				taskId: null,
			})

			/**
			 * State for the image preview overlay.
			 * Holds the rendered ANSI art string for the currently previewed image.
			 */
			const previewState = yield* SubscriptionRef.make<{
				readonly taskId: string | null
				readonly attachmentId: string | null
				readonly filename: string | null
				readonly renderedImage: string | null
				readonly isLoading: boolean
				readonly error: string | null
			}>({
				taskId: null,
				attachmentId: null,
				filename: null,
				renderedImage: null,
				isLoading: false,
				error: null,
			})

			return {
				// Expose SubscriptionRefs for atom subscription
				currentAttachments,
				overlayState,
				previewState,

				/**
				 * Open the image attach overlay for a task
				 */
				openOverlay: (taskId: string) =>
					SubscriptionRef.set(overlayState, {
						mode: "menu",
						pathInput: "",
						isAttaching: false,
						taskId,
					}),

				/**
				 * Close the image attach overlay
				 */
				closeOverlay: () =>
					SubscriptionRef.set(overlayState, {
						mode: "menu",
						pathInput: "",
						isAttaching: false,
						taskId: null,
					}),

				/**
				 * Switch overlay to path input mode
				 */
				enterPathMode: () =>
					SubscriptionRef.update(overlayState, (s) => ({ ...s, mode: "path" as const })),

				/**
				 * Switch overlay back to menu mode
				 */
				exitPathMode: () =>
					SubscriptionRef.update(overlayState, (s) => ({
						...s,
						mode: "menu" as const,
						pathInput: "",
					})),

				/**
				 * Update the path input value
				 */
				setPathInput: (value: string) =>
					SubscriptionRef.update(overlayState, (s) => ({ ...s, pathInput: value })),

				/**
				 * Set attaching state
				 */
				setAttaching: (isAttaching: boolean) =>
					SubscriptionRef.update(overlayState, (s) => ({ ...s, isAttaching })),

				/**
				 * Load attachments for a task and update reactive state.
				 * Call this when opening detail panel.
				 */
				loadForTask: (taskId: string) =>
					Effect.gen(function* () {
						const attachments = yield* localIssueStore.listIssueAttachments(taskId)
						yield* SubscriptionRef.set(currentAttachments, {
							taskId,
							attachments,
							selectedIndex: -1, // -1 = no attachment selected
						})
						return attachments
					}),

				/**
				 * Clear current attachments state (when closing detail panel)
				 */
				clearCurrent: () => SubscriptionRef.set(currentAttachments, null),

				/**
				 * Move attachment selection up (toward index 0, or -1 to exit selection)
				 */
				selectPreviousAttachment: () =>
					SubscriptionRef.update(currentAttachments, (current) => {
						if (!current || current.attachments.length === 0) return current
						const newIndex = Math.max(-1, current.selectedIndex - 1)
						return { ...current, selectedIndex: newIndex }
					}),

				/**
				 * Move attachment selection down (toward last attachment)
				 */
				selectNextAttachment: () =>
					SubscriptionRef.update(currentAttachments, (current) => {
						if (!current || current.attachments.length === 0) return current
						const maxIndex = current.attachments.length - 1
						const newIndex = Math.min(maxIndex, current.selectedIndex + 1)
						return { ...current, selectedIndex: newIndex }
					}),

				/**
				 * Get currently selected attachment (if any)
				 */
				getSelectedAttachment: () =>
					Effect.gen(function* () {
						const current = yield* SubscriptionRef.get(currentAttachments)
						if (!current || current.selectedIndex < 0) return null
						return current.attachments[current.selectedIndex] ?? null
					}),

				/**
				 * Open currently selected attachment in default viewer
				 */
				openSelectedAttachment: () =>
					Effect.gen(function* () {
						const current = yield* SubscriptionRef.get(currentAttachments)
						if (!current || current.selectedIndex < 0) {
							return yield* Effect.fail(
								new ImageAttachmentError({ message: "No attachment selected" }),
							)
						}
						const attachment = current.attachments[current.selectedIndex]
						if (!attachment) {
							return yield* Effect.fail(
								new ImageAttachmentError({ message: "Attachment not found" }),
							)
						}

						const filePath = yield* materializeAttachment(current.taskId, attachment)

						// Use platform-specific open command
						const openCmd = process.platform === "darwin" ? "open" : "xdg-open"
						yield* Command.make(openCmd, filePath).pipe(
							Command.exitCode,
							Effect.catchAll((error) =>
								Effect.fail(
									new ImageAttachmentError({
										message: `Failed to open image: ${error}`,
									}),
								),
							),
						)
					}),

				/**
				 * Remove currently selected attachment
				 * Returns the removed attachment or fails if none selected
				 */
				removeSelectedAttachment: () =>
					Effect.gen(function* () {
						const current = yield* SubscriptionRef.get(currentAttachments)
						if (!current || current.selectedIndex < 0) {
							return yield* Effect.fail(
								new ImageAttachmentError({ message: "No attachment selected" }),
							)
						}
						const attachment = current.attachments[current.selectedIndex]
						if (!attachment) {
							return yield* Effect.fail(
								new ImageAttachmentError({ message: "Attachment not found" }),
							)
						}

						yield* localIssueStore.removeIssueAttachment(current.taskId, attachment.id)
						const attachmentDir = yield* getTmpAttachmentDir(current.taskId, attachment.id)
						yield* fs.remove(attachmentDir, { recursive: true }).pipe(Effect.ignore)

						const newAttachments = yield* localIssueStore.listIssueAttachments(current.taskId)
						if (newAttachments.length === 0) {
							yield* removeIssueTmpDir(current.taskId)
						}

						// Update reactive state - adjust selected index if needed
						const newSelectedIndex =
							newAttachments.length === 0
								? -1
								: Math.min(current.selectedIndex, newAttachments.length - 1)
						yield* SubscriptionRef.set(currentAttachments, {
							taskId: current.taskId,
							attachments: newAttachments,
							selectedIndex: newSelectedIndex,
						})

						// Remove attachment link from notes
						yield* unlinkAttachmentFromNotes(current.taskId, attachment)

						return attachment
					}),

				/**
				 * List all attachments for an issue
				 */
				list: (issueId: string) =>
					Effect.gen(function* () {
						return yield* localIssueStore.listIssueAttachments(issueId)
					}),

				/**
				 * Attach an image from a file path
				 */
				attachFile: (issueId: string, filePath: string) =>
					Effect.gen(function* () {
						// Verify file exists
						const exists = yield* fs.exists(filePath)
						if (!exists) {
							return yield* Effect.fail(new FileNotFoundError({ path: filePath }))
						}

						// Check if it's an image
						const filename = path.basename(filePath)
						if (!isImageFile(filename)) {
							return yield* Effect.fail(
								new ImageAttachmentError({
									message: `Not a supported image format: ${filename}. Supported: ${IMAGE_EXTENSIONS.join(", ")}`,
								}),
							)
						}

						// Get file stats and content
						const stats = yield* fs.stat(filePath)
						const content = yield* fs.readFile(filePath)

						// Generate unique ID and storage filename
						const id = generateId()
						const ext = path.extname(filename)
						const destFilename = `${id}${ext}`

						// Create attachment metadata
						const attachment: ImageAttachment = {
							id,
							filename: destFilename,
							originalPath: filePath,
							mimeType: getMimeType(filename),
							size: Number(stats.size),
							createdAt: new Date().toISOString(),
						}

						yield* localIssueStore.upsertIssueAttachment({
							issueId,
							attachmentId: attachment.id,
							filename: attachment.filename,
							originalPath: attachment.originalPath,
							mimeType: attachment.mimeType,
							size: attachment.size,
							createdAt: attachment.createdAt,
							content,
						})

						const newAttachments = yield* localIssueStore.listIssueAttachments(issueId)
						if (newAttachments.length === 0) {
							yield* removeIssueTmpDir(issueId)
						}

						// Update reactive state if viewing this task
						const current = yield* SubscriptionRef.get(currentAttachments)
						if (current?.taskId === issueId) {
							yield* SubscriptionRef.set(currentAttachments, {
								taskId: issueId,
								attachments: newAttachments,
								selectedIndex: current.selectedIndex,
							})
						}

						// Link attachment in bead notes
						yield* linkAttachmentInNotes(issueId, attachment)

						return attachment
					}),

				/**
				 * Attach image from clipboard
				 */
				attachFromClipboard: (issueId: string) =>
					Effect.gen(function* () {
						const tool = yield* detectClipboardTool

						if (!tool) {
							return yield* Effect.fail(
								new ClipboardError({
									message:
										process.platform === "darwin"
											? "Clipboard tool not available on macOS."
											: "No clipboard tool available. Install xclip (X11) or wl-clipboard (Wayland).",
								}),
							)
						}

						const clipboardTmpDir = yield* getClipboardTmpDir()
						yield* fs.makeDirectory(clipboardTmpDir, { recursive: true }).pipe(Effect.ignore)

						// Generate unique ID
						const id = generateId()
						const destFilename = `${id}.png`
						const destPath = path.join(clipboardTmpDir, `${issueId}-${destFilename}`)

						// Get image from clipboard using platform-specific command
						// macOS: Use osascript to write clipboard image data as PNG
						// Linux: Use wl-paste or xclip depending on display server
						let shellCmd: string
						if (tool === "pbpaste") {
							// macOS: osascript writes PNG data of clipboard image to file
							shellCmd = `osascript -e 'set png_data to (the clipboard as «class PNGf»)' -e 'set fp to open for access POSIX file "${destPath}" with write permission' -e 'write png_data to fp' -e 'close access fp'`
						} else if (tool === "wl-paste") {
							shellCmd = `wl-paste --type image/png > "${destPath}"`
						} else {
							shellCmd = `xclip -selection clipboard -t image/png -o > "${destPath}"`
						}

						yield* Command.make("sh", "-c", shellCmd).pipe(
							Command.exitCode,
							Effect.catchAll((error) =>
								Effect.fail(
									new ClipboardError({
										message: `Failed to get image from clipboard: ${error}`,
										tool,
									}),
								),
							),
						)

						// Verify the file was created and has content
						const exists = yield* fs.exists(destPath)
						if (!exists) {
							return yield* Effect.fail(
								new ClipboardError({
									message: "No image data in clipboard or failed to save image.",
									tool,
								}),
							)
						}

						const stats = yield* fs.stat(destPath)
						if (Number(stats.size) === 0) {
							yield* fs.remove(destPath).pipe(Effect.ignore)
							return yield* Effect.fail(
								new ClipboardError({
									message: "Clipboard does not contain image data.",
									tool,
								}),
							)
						}

						// Create attachment metadata
						const attachment: ImageAttachment = {
							id,
							filename: destFilename,
							originalPath: "clipboard",
							mimeType: "image/png",
							size: Number(stats.size),
							createdAt: new Date().toISOString(),
						}

						const content = yield* fs.readFile(destPath)
						yield* fs.remove(destPath).pipe(Effect.ignore)

						yield* localIssueStore.upsertIssueAttachment({
							issueId,
							attachmentId: attachment.id,
							filename: attachment.filename,
							originalPath: attachment.originalPath,
							mimeType: attachment.mimeType,
							size: attachment.size,
							createdAt: attachment.createdAt,
							content,
						})

						const newAttachments = yield* localIssueStore.listIssueAttachments(issueId)

						// Update reactive state if viewing this task
						const current = yield* SubscriptionRef.get(currentAttachments)
						if (current?.taskId === issueId) {
							yield* SubscriptionRef.set(currentAttachments, {
								taskId: issueId,
								attachments: newAttachments,
								selectedIndex: current.selectedIndex,
							})
						}

						// Link attachment in bead notes
						yield* linkAttachmentInNotes(issueId, attachment)

						return attachment
					}),

				/**
				 * Remove an attachment
				 */
				remove: (issueId: string, attachmentId: string) =>
					Effect.gen(function* () {
						const issueAttachments = yield* localIssueStore.listIssueAttachments(issueId)
						const attachment = issueAttachments.find((a) => a.id === attachmentId)

						if (!attachment) {
							return yield* Effect.fail(
								new ImageAttachmentError({
									message: `Attachment not found: ${attachmentId}`,
								}),
							)
						}

						yield* localIssueStore.removeIssueAttachment(issueId, attachmentId)
						const attachmentDir = yield* getTmpAttachmentDir(issueId, attachmentId)
						yield* fs.remove(attachmentDir, { recursive: true }).pipe(Effect.ignore)

						const newAttachments = yield* localIssueStore.listIssueAttachments(issueId)

						// Update reactive state if viewing this task
						const current = yield* SubscriptionRef.get(currentAttachments)
						if (current?.taskId === issueId) {
							// Adjust selected index if needed
							const newSelectedIndex =
								newAttachments.length === 0
									? -1
									: Math.min(current.selectedIndex, newAttachments.length - 1)
							yield* SubscriptionRef.set(currentAttachments, {
								taskId: issueId,
								attachments: newAttachments,
								selectedIndex: newSelectedIndex,
							})
						}

						// Remove attachment link from notes
						yield* unlinkAttachmentFromNotes(issueId, attachment)
					}),

				/**
				 * Get full path to an attachment file
				 */
				getPath: (issueId: string, attachmentId: string) =>
					Effect.gen(function* () {
						const issueAttachments = yield* localIssueStore.listIssueAttachments(issueId)
						const attachment = issueAttachments.find((a) => a.id === attachmentId)

						if (!attachment) {
							return yield* Effect.fail(
								new ImageAttachmentError({
									message: `Attachment not found: ${attachmentId}`,
								}),
							)
						}

						return yield* materializeAttachment(issueId, attachment)
					}),

				/**
				 * Get full path to an attachment file materialized under a specific project root.
				 *
				 * Used when starting a session in a worktree so attachment paths resolve inside
				 * that worktree instead of a sibling/parent project.
				 */
				getPathForProjectRoot: (issueId: string, attachmentId: string, projectRootPath: string) =>
					Effect.gen(function* () {
						const issueAttachments = yield* localIssueStore.listIssueAttachments(issueId)
						const attachment = issueAttachments.find((a) => a.id === attachmentId)

						if (!attachment) {
							return yield* Effect.fail(
								new ImageAttachmentError({
									message: `Attachment not found: ${attachmentId}`,
								}),
							)
						}

						return yield* materializeAttachment(issueId, attachment, projectRootPath)
					}),

				/**
				 * Open an attachment in the default image viewer
				 */
				open: (issueId: string, attachmentId: string) =>
					Effect.gen(function* () {
						const issueAttachments = yield* localIssueStore.listIssueAttachments(issueId)
						const attachment = issueAttachments.find((a) => a.id === attachmentId)

						if (!attachment) {
							return yield* Effect.fail(
								new ImageAttachmentError({
									message: `Attachment not found: ${attachmentId}`,
								}),
							)
						}

						const filePath = yield* materializeAttachment(issueId, attachment)

						// Use platform-specific open command
						const openCmd = process.platform === "darwin" ? "open" : "xdg-open"
						yield* Command.make(openCmd, filePath).pipe(
							Command.exitCode,
							Effect.catchAll((error) =>
								Effect.fail(
									new ImageAttachmentError({
										message: `Failed to open image: ${error}`,
									}),
								),
							),
						)
					}),

				/**
				 * Check if clipboard tools are available
				 */
				hasClipboardSupport: () =>
					Effect.gen(function* () {
						const tool = yield* detectClipboardTool
						return tool !== null
					}),

				/**
				 * Get count of attachments for an issue
				 */
				count: (issueId: string) =>
					Effect.gen(function* () {
						const attachments = yield* localIssueStore.listIssueAttachments(issueId)
						return attachments.length
					}),

				/**
				 * Get attachment counts for multiple issues (batch)
				 */
				countBatch: (issueIds: readonly string[]) =>
					Effect.gen(function* () {
						const result: Record<string, number> = {}
						for (const id of issueIds) {
							const attachments = yield* localIssueStore.listIssueAttachments(id)
							result[id] = attachments.length
						}
						return result
					}),

				// ========================================================================
				// Image Preview Methods
				// ========================================================================

				/**
				 * Open preview for the currently selected attachment.
				 * Uses viu in a tmux popup to display the image with native Kitty graphics.
				 * This bypasses OpenTUI entirely since it can't handle ANSI escape sequences.
				 */
				openPreview: () =>
					Effect.gen(function* () {
						const current = yield* SubscriptionRef.get(currentAttachments)
						if (!current || current.selectedIndex < 0) {
							return yield* Effect.fail(
								new ImageAttachmentError({ message: "No attachment selected" }),
							)
						}
						const attachment = current.attachments[current.selectedIndex]
						if (!attachment) {
							return yield* Effect.fail(
								new ImageAttachmentError({ message: "Attachment not found" }),
							)
						}

						const filePath = yield* materializeAttachment(current.taskId, attachment)

						// Check if viu is available
						const viuCheck = yield* Command.make("which", "viu").pipe(
							Command.exitCode,
							Effect.catchAll(() => Effect.succeed(1)),
						)

						if (viuCheck !== 0) {
							return yield* Effect.fail(
								new ImageAttachmentError({
									message:
										"viu not installed. Add 'viu' to ~/nix/darwin.nix brews and run darwin-rebuild.",
								}),
							)
						}

						// Build the navigation info
						const navInfo =
							current.attachments.length > 1
								? ` (${current.selectedIndex + 1}/${current.attachments.length})`
								: ""

						// Use tmux display-popup to show the image with viu
						// viu uses Kitty graphics protocol when available for high quality
						const popupTitle = ` 📷 ${attachment.filename}${navInfo} `

						// The popup command: show image with viu, then wait for keypress
						// Use semicolons (not &&) so read always runs even if viu fails
						// Note: We use bash here intentionally because `read -rsn1` is bash-specific syntax
						// (zsh uses `read -rsk1`, fish is completely different). These utility popups
						// don't need the user's shell environment - they just run simple scripts.
						const popupCmd = `viu "${filePath}"; echo ""; echo "Press any key to close..."; read -rsn1`

						// Use TmuxService-style approach: pass entire command as single string
						// This ensures proper command parsing by tmux display-popup
						yield* Command.make(
							"tmux",
							"display-popup",
							"-E", // Close popup when command exits
							"-w",
							"90%",
							"-h",
							"90%",
							"-T",
							popupTitle,
							`bash -c '${popupCmd}'`,
						).pipe(
							Command.exitCode,
							Effect.catchAll((error) =>
								Effect.fail(
									new ImageAttachmentError({
										message: `Failed to open image popup: ${error}`,
									}),
								),
							),
						)

						return attachment
					}),

				/**
				 * Close the image preview and clear state
				 */
				closePreview: () =>
					SubscriptionRef.set(previewState, {
						taskId: null,
						attachmentId: null,
						filename: null,
						renderedImage: null,
						isLoading: false,
						error: null,
					}),

				/**
				 * Navigate to next attachment while in preview mode
				 */
				previewNext: () =>
					Effect.gen(function* () {
						const current = yield* SubscriptionRef.get(currentAttachments)
						if (!current || current.attachments.length === 0) return

						const newIndex = Math.min(current.attachments.length - 1, current.selectedIndex + 1)
						if (newIndex !== current.selectedIndex) {
							yield* SubscriptionRef.set(currentAttachments, {
								...current,
								selectedIndex: newIndex,
							})
						}
					}),

				/**
				 * Navigate to previous attachment while in preview mode
				 */
				previewPrevious: () =>
					Effect.gen(function* () {
						const current = yield* SubscriptionRef.get(currentAttachments)
						if (!current || current.attachments.length === 0) return

						const newIndex = Math.max(0, current.selectedIndex - 1)
						if (newIndex !== current.selectedIndex) {
							yield* SubscriptionRef.set(currentAttachments, {
								...current,
								selectedIndex: newIndex,
							})
						}
					}),

				// ========================================================================
				// Cleanup Methods (for issue close/compact)
				// ========================================================================

				/**
				 * Delete all images for an issue.
				 * Called when an issue is closed or compacted to prevent unbounded storage growth.
				 * This removes the files, the issue directory, and the index entry.
				 * Does NOT update bead notes (issue is already closed/compacted).
				 *
				 * @returns Number of images deleted, or 0 if no images existed
				 */
				cleanupImagesForIssue: (issueId: string) =>
					Effect.gen(function* () {
						const attachments = yield* localIssueStore.listIssueAttachments(issueId)

						if (attachments.length === 0) {
							yield* removeIssueTmpDir(issueId)
							return 0
						}

						const count = yield* localIssueStore.clearIssueAttachments(issueId)
						yield* removeIssueTmpDir(issueId)

						// Clear reactive state if viewing this task
						const current = yield* SubscriptionRef.get(currentAttachments)
						if (current?.taskId === issueId) {
							yield* SubscriptionRef.set(currentAttachments, null)
						}

						yield* Effect.log(`Cleaned up ${count} image(s) for closed issue ${issueId}`)

						return count
					}),

				/**
				 * Delete images for multiple issues at once.
				 * Used for batch cleanup during compaction.
				 *
				 * @returns Total number of images deleted
				 */
				cleanupImagesForIssues: (issueIds: readonly string[]) =>
					Effect.gen(function* () {
						let totalDeleted = 0
						for (const issueId of issueIds) {
							const deleted = yield* localIssueStore.clearIssueAttachments(issueId)
							yield* removeIssueTmpDir(issueId)
							totalDeleted += deleted
						}

						if (totalDeleted > 0) {
							yield* Effect.log(
								`Cleaned up ${totalDeleted} image(s) for ${issueIds.length} compacted issues`,
							)
						}

						return totalDeleted
					}),
			}
		}),
	},
) {}
