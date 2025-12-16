/**
 * ImageAttachmentService - Effect service for managing image attachments on tasks
 *
 * Images are stored in .beads/images/{issue-id}/ with metadata tracked in
 * .beads/images/index.json since the beads CLI doesn't natively support attachments.
 */

import { Command, type CommandExecutor, FileSystem, Path } from "@effect/platform"
import { Data, Effect, Schema } from "effect"

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
		effect: Effect.gen(function* () {
			const fs = yield* FileSystem.FileSystem
			const path = yield* Path.Path

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

			/**
			 * Read the attachment index
			 */
			const readIndex = Effect.gen(function* () {
				yield* ensureStorage
				const indexPath = getIndexPath()
				const content = yield* fs.readFileString(indexPath)
				try {
					const parsed = JSON.parse(content)
					return Schema.decodeUnknownSync(AttachmentIndexSchema)(parsed)
				} catch {
					return {} as AttachmentIndex
				}
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
			 * Returns: "xclip" | "wl-paste" | null
			 */
			const detectClipboardTool = Effect.gen(function* () {
				// Try wl-paste first (Wayland)
				const wlPasteCheck = yield* Command.make("which", "wl-paste")
					.pipe(Command.exitCode)
					.pipe(Effect.catchAll(() => Effect.succeed(1)))

				if (wlPasteCheck === 0) {
					return "wl-paste" as const
				}

				// Try xclip (X11)
				const xclipCheck = yield* Command.make("which", "xclip")
					.pipe(Command.exitCode)
					.pipe(Effect.catchAll(() => Effect.succeed(1)))

				if (xclipCheck === 0) {
					return "xclip" as const
				}

				return null
			})

			return {
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

						// Update index
						const index = yield* readIndex
						const issueAttachments = index[issueId] ?? []
						index[issueId] = [...issueAttachments, attachment]
						yield* writeIndex(index)

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
										"No clipboard tool available. Install xclip (X11) or wl-clipboard (Wayland).",
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

						// Get image from clipboard based on tool
						if (tool === "wl-paste") {
							// wl-paste --type image/png > file
							const result = yield* Command.make("wl-paste", "--type", "image/png")
								.pipe(Command.stdout("inherit"), Command.stderr("inherit"))
								.pipe(
									Effect.flatMap((process) => Effect.promise(() => process.stdout)),
									Effect.catchAll((error) =>
										Effect.fail(
											new ClipboardError({
												message: `Failed to get image from clipboard: ${error}`,
												tool: "wl-paste",
											}),
										),
									),
								)

							yield* fs.writeFile(destPath, new Uint8Array(result as ArrayBuffer))
						} else {
							// xclip -selection clipboard -t image/png -o > file
							const result = yield* Command.make(
								"xclip",
								"-selection",
								"clipboard",
								"-t",
								"image/png",
								"-o",
							)
								.pipe(Command.stdout("inherit"), Command.stderr("inherit"))
								.pipe(
									Effect.flatMap((process) => Effect.promise(() => process.stdout)),
									Effect.catchAll((error) =>
										Effect.fail(
											new ClipboardError({
												message: `Failed to get image from clipboard: ${error}`,
												tool: "xclip",
											}),
										),
									),
								)

							yield* fs.writeFile(destPath, new Uint8Array(result as ArrayBuffer))
						}

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

						// Update index
						const index = yield* readIndex
						const issueAttachments = index[issueId] ?? []
						index[issueId] = [...issueAttachments, attachment]
						yield* writeIndex(index)

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

						// Update index
						index[issueId] = issueAttachments.filter((a) => a.id !== attachmentId)
						if (index[issueId].length === 0) {
							delete index[issueId]
							// Also try to remove the empty directory
							yield* fs.remove(getIssueDir(issueId)).pipe(Effect.ignore)
						}
						yield* writeIndex(index)
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
						yield* Command.make("xdg-open", filePath)
							.pipe(Command.exitCode)
							.pipe(
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
			}
		}),
	},
) {}
