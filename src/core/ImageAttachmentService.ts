/**
 * ImageAttachmentService - Effect service for managing image attachments on tasks
 *
 * Images are stored in .beads/images/{issue-id}/ with metadata tracked in
 * .beads/images/index.json since the beads CLI doesn't natively support attachments.
 */

import { Command, type CommandExecutor, FileSystem, Path } from "@effect/platform"
import { Data, Effect, Record, Schema, SubscriptionRef } from "effect"
import { BeadsClient } from "./BeadsClient.js"

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

/**
 * Index mapping issue IDs to their attachments
 */
const AttachmentIndexSchema = Schema.Record({
	key: Schema.String,
	value: Schema.Array(ImageAttachmentSchema),
})

type AttachmentIndex = Schema.Schema.Type<typeof AttachmentIndexSchema>

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

const BEADS_IMAGES_DIR = ".beads/images"
const INDEX_FILE = "index.json"

// Supported image extensions
const IMAGE_EXTENSIONS = [".png", ".jpg", ".jpeg", ".gif", ".webp", ".bmp", ".svg"]

// ============================================================================
// Service Implementation
// ============================================================================

/**
 * ImageAttachmentService
 *
 * Manages image attachments for beads tasks. Since the beads CLI doesn't support
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
		dependencies: [BeadsClient.Default],
		effect: Effect.gen(function* () {
			const fs = yield* FileSystem.FileSystem
			const path = yield* Path.Path
			const beadsClient = yield* BeadsClient

			// Base directory for all image attachments
			const getBaseDir = () => path.join(process.cwd(), BEADS_IMAGES_DIR)
			const getIndexPath = () => path.join(getBaseDir(), INDEX_FILE)
			const getIssueDir = (issueId: string) => path.join(getBaseDir(), issueId)

			/**
			 * Ensure base directory and index file exist
			 */
			const ensureStorage = Effect.gen(function* () {
				const baseDir = getBaseDir()
				const indexPath = getIndexPath()

				// Create base directory if not exists
				yield* fs.makeDirectory(baseDir, { recursive: true }).pipe(Effect.ignore)

				// Create index file if not exists
				const exists = yield* fs.exists(indexPath)
				if (!exists) {
					yield* fs.writeFileString(indexPath, "{}")
				}
			})

			// Decoder for reading index from JSON string
			const decodeIndex = Schema.decodeUnknown(Schema.parseJson(AttachmentIndexSchema))

			/**
			 * Read the attachment index
			 */
			const readIndex = Effect.gen(function* () {
				yield* ensureStorage
				const indexPath = getIndexPath()
				const content = yield* fs.readFileString(indexPath)
				return yield* decodeIndex(content).pipe(
					Effect.catchAll(() => Effect.succeed({} as AttachmentIndex)),
				)
			})

			/**
			 * Write the attachment index
			 */
			const writeIndex = (index: AttachmentIndex) =>
				Effect.gen(function* () {
					const indexPath = getIndexPath()
					yield* fs.writeFileString(indexPath, JSON.stringify(index, null, 2))
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
					const relativePath = `.beads/images/${issueId}/${attachment.filename}`

					// Create a formatted attachment entry
					const timestamp = new Date(attachment.createdAt).toLocaleString()
					const source = attachment.originalPath === "clipboard" ? "clipboard" : "file"
					const attachmentLine = `📎 [${attachment.filename}](${relativePath}) (${source}, ${timestamp})`

					// Get current issue to read existing notes
					const issue = yield* beadsClient
						.show(issueId)
						.pipe(Effect.catchAll(() => Effect.succeed(null)))

					// Append to existing notes or create new
					const existingNotes = issue?.notes ?? ""
					const separator = existingNotes.trim() ? "\n" : ""
					const newNotes = `${existingNotes}${separator}${attachmentLine}`

					// Update the bead with new notes
					yield* beadsClient
						.update(issueId, { notes: newNotes })
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
						const index = yield* readIndex
						const attachments = index[taskId] ?? []
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

						const filePath = path.join(getIssueDir(current.taskId), attachment.filename)

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

						// Remove file
						const filePath = path.join(getIssueDir(current.taskId), attachment.filename)
						yield* fs.remove(filePath).pipe(Effect.ignore)

						// Update index
						const index = yield* readIndex
						const issueAttachments = index[current.taskId] ?? []
						const newAttachments = issueAttachments.filter((a) => a.id !== attachment.id)
						const updatedIndex =
							newAttachments.length === 0
								? Record.remove(index, current.taskId)
								: Record.set(index, current.taskId, newAttachments)
						if (newAttachments.length === 0) {
							yield* fs.remove(getIssueDir(current.taskId)).pipe(Effect.ignore)
						}
						yield* writeIndex(updatedIndex)

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

						return attachment
					}),

				/**
				 * List all attachments for an issue
				 */
				list: (issueId: string) =>
					Effect.gen(function* () {
						const index = yield* readIndex
						return index[issueId] ?? []
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

						// Get file stats
						const stats = yield* fs.stat(filePath)

						// Create issue directory
						const issueDir = getIssueDir(issueId)
						yield* fs.makeDirectory(issueDir, { recursive: true }).pipe(Effect.ignore)

						// Generate unique ID and destination path
						const id = generateId()
						const ext = path.extname(filename)
						const destFilename = `${id}${ext}`
						const destPath = path.join(issueDir, destFilename)

						// Copy file to storage
						yield* fs.copyFile(filePath, destPath)

						// Create attachment metadata
						const attachment: ImageAttachment = {
							id,
							filename: destFilename,
							originalPath: filePath,
							mimeType: getMimeType(filename),
							size: Number(stats.size),
							createdAt: new Date().toISOString(),
						}

						// Update index using Effect's Record.set for immutability
						const index = yield* readIndex
						const issueAttachments = index[issueId] ?? []
						const newAttachments = [...issueAttachments, attachment]
						yield* writeIndex(Record.set(index, issueId, newAttachments))

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

						// Create issue directory
						const issueDir = getIssueDir(issueId)
						yield* fs.makeDirectory(issueDir, { recursive: true }).pipe(Effect.ignore)

						// Generate unique ID
						const id = generateId()
						const destFilename = `${id}.png`
						const destPath = path.join(issueDir, destFilename)

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

						// Update index using Effect's Record.set for immutability
						const index = yield* readIndex
						const issueAttachments = index[issueId] ?? []
						const newAttachments = [...issueAttachments, attachment]
						yield* writeIndex(Record.set(index, issueId, newAttachments))

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
						const index = yield* readIndex
						const issueAttachments = index[issueId] ?? []
						const attachment = issueAttachments.find((a) => a.id === attachmentId)

						if (!attachment) {
							return yield* Effect.fail(
								new ImageAttachmentError({
									message: `Attachment not found: ${attachmentId}`,
								}),
							)
						}

						// Remove file
						const filePath = path.join(getIssueDir(issueId), attachment.filename)
						yield* fs.remove(filePath).pipe(Effect.ignore)

						// Update index using Effect's Record for immutability
						const newAttachments = issueAttachments.filter((a) => a.id !== attachmentId)
						const updatedIndex =
							newAttachments.length === 0
								? Record.remove(index, issueId)
								: Record.set(index, issueId, newAttachments)
						if (newAttachments.length === 0) {
							// Also try to remove the empty directory
							yield* fs.remove(getIssueDir(issueId)).pipe(Effect.ignore)
						}
						yield* writeIndex(updatedIndex)

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
					}),

				/**
				 * Get full path to an attachment file
				 */
				getPath: (issueId: string, attachmentId: string) =>
					Effect.gen(function* () {
						const index = yield* readIndex
						const issueAttachments = index[issueId] ?? []
						const attachment = issueAttachments.find((a) => a.id === attachmentId)

						if (!attachment) {
							return yield* Effect.fail(
								new ImageAttachmentError({
									message: `Attachment not found: ${attachmentId}`,
								}),
							)
						}

						return path.join(getIssueDir(issueId), attachment.filename)
					}),

				/**
				 * Open an attachment in the default image viewer
				 */
				open: (issueId: string, attachmentId: string) =>
					Effect.gen(function* () {
						const index = yield* readIndex
						const issueAttachments = index[issueId] ?? []
						const attachment = issueAttachments.find((a) => a.id === attachmentId)

						if (!attachment) {
							return yield* Effect.fail(
								new ImageAttachmentError({
									message: `Attachment not found: ${attachmentId}`,
								}),
							)
						}

						const filePath = path.join(getIssueDir(issueId), attachment.filename)

						// Use xdg-open on Linux
						yield* Command.make("xdg-open", filePath).pipe(
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
						const index = yield* readIndex
						return (index[issueId] ?? []).length
					}),

				/**
				 * Get attachment counts for multiple issues (batch)
				 */
				countBatch: (issueIds: readonly string[]) =>
					Effect.gen(function* () {
						const index = yield* readIndex
						const result: Record<string, number> = {}
						for (const id of issueIds) {
							result[id] = (index[id] ?? []).length
						}
						return result
					}),

				// ========================================================================
				// Image Preview Methods
				// ========================================================================

				/**
				 * Open preview for the currently selected attachment.
				 * Renders the image to terminal-compatible text and stores in previewState.
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

						// Set loading state
						yield* SubscriptionRef.set(previewState, {
							taskId: current.taskId,
							attachmentId: attachment.id,
							filename: attachment.filename,
							renderedImage: null,
							isLoading: true,
							error: null,
						})

						// Get full file path
						const filePath = path.join(getIssueDir(current.taskId), attachment.filename)

						// Import terminal-image dynamically and render
						const { default: terminalImage } = yield* Effect.tryPromise({
							try: () => import("terminal-image"),
							catch: (e) =>
								new ImageAttachmentError({
									message: `Failed to load terminal-image: ${e}`,
								}),
						})

						// Get terminal dimensions for sizing (leave room for borders and info)
						const termCols = process.stdout.columns || 80
						const termRows = process.stdout.rows || 24
						// Reserve space for borders (4 cols), header (2 rows), footer (3 rows)
						const maxWidth = Math.max(20, termCols - 10)
						const maxHeight = Math.max(10, termRows - 10)

						const rendered = yield* Effect.tryPromise({
							try: () =>
								terminalImage.file(filePath, {
									width: maxWidth,
									height: maxHeight,
									preserveAspectRatio: true,
								}),
							catch: (e) =>
								new ImageAttachmentError({
									message: `Failed to render image: ${e}`,
								}),
						})

						// Store rendered result
						yield* SubscriptionRef.set(previewState, {
							taskId: current.taskId,
							attachmentId: attachment.id,
							filename: attachment.filename,
							renderedImage: rendered,
							isLoading: false,
							error: null,
						})

						return attachment
					}).pipe(
						Effect.catchAll((error) =>
							Effect.gen(function* () {
								const msg =
									error && typeof error === "object" && "message" in error
										? String(error.message)
										: String(error)
								yield* SubscriptionRef.update(previewState, (s) => ({
									...s,
									isLoading: false,
									error: msg,
								}))
								return yield* Effect.fail(error)
							}),
						),
					),

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
			}
		}),
	},
) {}
