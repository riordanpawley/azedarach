import { AppConfigProjectContext } from "@azedarach/config"
import { resolveEffectiveProjectPath } from "@azedarach/shared/project-path"
import { DaemonRpcClient, mapDaemonRpcClientErrorMessage } from "@azedarach/shared/rpc"
import { Command, FileSystem, Path } from "@effect/platform"
import * as CommandExecutor from "@effect/platform/CommandExecutor"
import { BunContext } from "@effect/platform-bun"
import { Data, Effect, SubscriptionRef } from "effect"
import type { ImageAttachment } from "../contracts.js"
import { TmuxService } from "./TmuxService.js"

type ClipboardTool = "osascript" | "wl-paste" | "xclip"

type CurrentAttachmentsState = {
	readonly taskId: string
	readonly attachments: readonly ImageAttachment[]
	readonly selectedIndex: number
} | null

type OverlayState = {
	readonly mode: "menu" | "path"
	readonly pathInput: string
	readonly isAttaching: boolean
	readonly taskId: string | null
}

type PreviewState = {
	readonly taskId: string | null
	readonly attachmentId: string | null
	readonly filename: string | null
	readonly renderedImage: string | null
	readonly isLoading: boolean
	readonly error: string | null
}

export class ClipboardError extends Data.TaggedError("ClipboardError")<{
	readonly message: string
	readonly tool?: ClipboardTool
}> {}

export class ImageAttachmentError extends Data.TaggedError("ImageAttachmentError")<{
	readonly message: string
}> {}

export class FileNotFoundError extends Data.TaggedError("FileNotFoundError")<{
	readonly path: string
}> {}

export interface ImageAttachmentServiceApi {
	readonly currentAttachments: SubscriptionRef.SubscriptionRef<CurrentAttachmentsState>
	readonly overlayState: SubscriptionRef.SubscriptionRef<OverlayState>
	readonly previewState: SubscriptionRef.SubscriptionRef<PreviewState>
	readonly hasClipboardSupport: () => Effect.Effect<boolean>
	readonly loadForTask: (
		taskId: string,
	) => Effect.Effect<ReadonlyArray<ImageAttachment>, ImageAttachmentError>
	readonly clearCurrent: () => Effect.Effect<void>
	readonly openOverlay: (taskId: string) => Effect.Effect<void>
	readonly closeOverlay: () => Effect.Effect<void>
	readonly enterPathMode: () => Effect.Effect<void>
	readonly exitPathMode: () => Effect.Effect<void>
	readonly setPathInput: (value: string) => Effect.Effect<void>
	readonly setAttaching: (isAttaching: boolean) => Effect.Effect<void>
	readonly list: (
		taskId: string,
	) => Effect.Effect<ReadonlyArray<ImageAttachment>, ImageAttachmentError>
	readonly count: (taskId: string) => Effect.Effect<number, ImageAttachmentError>
	readonly countBatch: (
		taskIds: readonly string[],
	) => Effect.Effect<Record<string, number>, ImageAttachmentError>
	readonly attachFile: (
		taskId: string,
		filePath: string,
	) => Effect.Effect<ImageAttachment, ImageAttachmentError | FileNotFoundError>
	readonly attachFromClipboard: (
		taskId: string,
	) => Effect.Effect<ImageAttachment, ImageAttachmentError | ClipboardError>
	readonly remove: (
		taskId: string,
		attachmentId: string,
	) => Effect.Effect<void, ImageAttachmentError>
	readonly open: (taskId: string, attachmentId: string) => Effect.Effect<void, ImageAttachmentError>
	readonly getPath: (
		taskId: string,
		attachmentId: string,
	) => Effect.Effect<string, ImageAttachmentError>
	readonly getPathForProjectRoot: (
		taskId: string,
		attachmentId: string,
		projectRootPath: string,
	) => Effect.Effect<string, ImageAttachmentError>
	readonly selectNextAttachment: () => Effect.Effect<void>
	readonly selectPreviousAttachment: () => Effect.Effect<void>
	readonly getSelectedAttachment: () => Effect.Effect<ImageAttachment | null>
	readonly openSelectedAttachment: () => Effect.Effect<ImageAttachment, ImageAttachmentError>
	readonly removeSelectedAttachment: () => Effect.Effect<ImageAttachment, ImageAttachmentError>
	readonly openPreview: () => Effect.Effect<ImageAttachment, ImageAttachmentError>
	readonly closePreview: () => Effect.Effect<void>
	readonly previewNext: () => Effect.Effect<void>
	readonly previewPrevious: () => Effect.Effect<void>
	readonly cleanupImagesForIssue: (taskId: string) => Effect.Effect<number, ImageAttachmentError>
	readonly cleanupImagesForIssues: (
		taskIds: readonly string[],
	) => Effect.Effect<number, ImageAttachmentError>
}

const IMAGE_EXTENSIONS = new Set([".png", ".jpg", ".jpeg", ".gif", ".webp", ".bmp", ".svg"])
const CLIPBOARD_TMP_DIR = ".azedarach/tmp/clipboard"

const emptyPreviewState = (): PreviewState => ({
	taskId: null,
	attachmentId: null,
	filename: null,
	renderedImage: null,
	isLoading: false,
	error: null,
})

const quoteShell = (value: string): string => {
	const escaped = value.replaceAll("'", "'\"'\"'")
	return `'${escaped}'`
}

const resolveExtension = (filename: string): string => {
	const dotIndex = filename.lastIndexOf(".")
	return dotIndex >= 0 ? filename.slice(dotIndex).toLowerCase() : ""
}

const requireImageExtension = (filename: string): Effect.Effect<string, ImageAttachmentError> => {
	const extension = resolveExtension(filename)
	return IMAGE_EXTENSIONS.has(extension)
		? Effect.succeed(extension)
		: Effect.fail(
				new ImageAttachmentError({
					message: `Not a supported image format: ${filename}. Supported: ${[...IMAGE_EXTENSIONS].join(", ")}`,
				}),
			)
}

const mapRpcError = (message: string): ImageAttachmentError => new ImageAttachmentError({ message })

const mapClipboardFailure = (message: string, tool?: ClipboardTool): ClipboardError =>
	new ClipboardError({ message, tool })

export class ImageAttachmentService extends Effect.Service<ImageAttachmentService>()(
	"TuiImageAttachmentService",
	{
		dependencies: [TmuxService.Default, BunContext.layer],
		effect: Effect.gen(function* () {
			const daemonRpcClient = yield* DaemonRpcClient
			const projectContext = yield* AppConfigProjectContext
			const fs = yield* FileSystem.FileSystem
			const pathService = yield* Path.Path
			const tmux = yield* TmuxService
			const executor = yield* CommandExecutor.CommandExecutor

			const resolveProjectPath = (): Effect.Effect<string> =>
				projectContext.getCurrentPath().pipe(Effect.map(resolveEffectiveProjectPath))

			const currentAttachments = yield* SubscriptionRef.make<CurrentAttachmentsState>(null)
			const overlayState = yield* SubscriptionRef.make<OverlayState>({
				mode: "menu",
				pathInput: "",
				isAttaching: false,
				taskId: null,
			})
			const previewState = yield* SubscriptionRef.make<PreviewState>(emptyPreviewState())

			const detectClipboardTool = (): Effect.Effect<ClipboardTool | null> =>
				Effect.gen(function* () {
					const commandExists = (commandName: ClipboardTool) =>
						executor.exitCode(Command.make("which", commandName)).pipe(
							Effect.map((exitCode) => exitCode === 0),
							Effect.orElseSucceed(() => false),
						)

					if (process.platform === "darwin") {
						return (yield* commandExists("osascript")) ? "osascript" : null
					}
					if (yield* commandExists("wl-paste")) {
						return "wl-paste"
					}
					if (yield* commandExists("xclip")) {
						return "xclip"
					}
					return null
				})

			const setAttaching = (isAttaching: boolean): Effect.Effect<void> =>
				SubscriptionRef.update(overlayState, (state) => ({ ...state, isAttaching }))

			const setCurrentAttachments = (
				taskId: string,
				attachments: ReadonlyArray<ImageAttachment>,
				selectedIndex: number,
			): Effect.Effect<void> =>
				SubscriptionRef.set(currentAttachments, {
					taskId,
					attachments,
					selectedIndex,
				})

			const clampSelectedIndex = (selectedIndex: number, attachmentCount: number): number => {
				if (attachmentCount === 0) {
					return -1
				}
				if (selectedIndex < -1) {
					return -1
				}
				return Math.min(selectedIndex, attachmentCount - 1)
			}

			const refreshCurrentTaskAttachments = (
				taskId: string,
				attachments: ReadonlyArray<ImageAttachment>,
			): Effect.Effect<void> =>
				SubscriptionRef.update(currentAttachments, (current) => {
					if (current === null || current.taskId !== taskId) {
						return current
					}
					return {
						taskId,
						attachments,
						selectedIndex: clampSelectedIndex(current.selectedIndex, attachments.length),
					}
				})

			const list = (
				taskId: string,
				projectPath?: string,
			): Effect.Effect<ReadonlyArray<ImageAttachment>, ImageAttachmentError> =>
				Effect.gen(function* () {
					const effectiveProjectPath = projectPath ?? (yield* resolveProjectPath())
					return yield* daemonRpcClient
						.attachmentList({
							issueId: taskId,
							projectPath: effectiveProjectPath,
						})
						.pipe(
							Effect.map((result) => result.attachments),
							Effect.mapError((error) => mapDaemonRpcClientErrorMessage(error, mapRpcError)),
						)
				})

			const materializePath = (
				taskId: string,
				attachmentId: string,
				projectPath?: string,
			): Effect.Effect<string, ImageAttachmentError> =>
				Effect.gen(function* () {
					const effectiveProjectPath = projectPath ?? (yield* resolveProjectPath())
					return yield* daemonRpcClient
						.attachmentMaterializePath({
							issueId: taskId,
							attachmentId,
							projectPath: effectiveProjectPath,
						})
						.pipe(
							Effect.map((result) => result.path),
							Effect.mapError((error) => mapDaemonRpcClientErrorMessage(error, mapRpcError)),
						)
				})

			const openAttachment = (
				taskId: string,
				attachmentId: string,
			): Effect.Effect<void, ImageAttachmentError> =>
				Effect.gen(function* () {
					const filePath = yield* materializePath(taskId, attachmentId)
					const openCommand = process.platform === "darwin" ? "open" : "xdg-open"
					yield* Command.make(openCommand, filePath).pipe(
						executor.exitCode,
						Effect.flatMap((exitCode) =>
							exitCode === 0
								? Effect.void
								: Effect.fail(
										new ImageAttachmentError({
											message: `Failed to open image: ${filePath}`,
										}),
									),
						),
						Effect.mapError((error) =>
							error instanceof ImageAttachmentError
								? error
								: new ImageAttachmentError({
										message: `Failed to open image: ${String(error)}`,
									}),
						),
					)
				})

			const removeAttachment = (
				taskId: string,
				attachmentId: string,
			): Effect.Effect<void, ImageAttachmentError> =>
				Effect.gen(function* () {
					const projectPath = yield* resolveProjectPath()
					yield* daemonRpcClient
						.attachmentRemove({
							issueId: taskId,
							attachmentId,
							projectPath,
						})
						.pipe(Effect.mapError((error) => mapDaemonRpcClientErrorMessage(error, mapRpcError)))
					const attachments = yield* list(taskId, projectPath)
					yield* refreshCurrentTaskAttachments(taskId, attachments)
				})

			const readClipboardImage = (): Effect.Effect<
				{ readonly content: Uint8Array; readonly filename: string; readonly mimeType: string },
				ImageAttachmentError | ClipboardError
			> =>
				Effect.gen(function* () {
					const tool = yield* detectClipboardTool()
					if (tool === null) {
						return yield* Effect.fail(
							mapClipboardFailure(
								process.platform === "darwin"
									? "Clipboard tool not available on macOS."
									: "No clipboard tool available. Install xclip (X11) or wl-clipboard (Wayland).",
							),
						)
					}

					const projectPath = yield* resolveProjectPath()
					const clipboardDirectory = pathService.join(projectPath, CLIPBOARD_TMP_DIR)
					yield* fs.makeDirectory(clipboardDirectory, { recursive: true }).pipe(
						Effect.mapError(
							() =>
								new ImageAttachmentError({
									message: `Failed to create clipboard directory ${clipboardDirectory}`,
								}),
						),
					)

					const clipboardFile = pathService.join(clipboardDirectory, `${crypto.randomUUID()}.png`)
					const captureClipboard =
						tool === "osascript"
							? Command.make(
									"osascript",
									"-e",
									"set png_data to (the clipboard as «class PNGf»)",
									"-e",
									`set file_ref to open for access POSIX file "${clipboardFile}" with write permission`,
									"-e",
									"write png_data to file_ref",
									"-e",
									"close access file_ref",
								)
							: Command.make(
									"sh",
									"-c",
									tool === "wl-paste"
										? `wl-paste --type image/png > ${quoteShell(clipboardFile)}`
										: `xclip -selection clipboard -t image/png -o > ${quoteShell(clipboardFile)}`,
								)

					yield* captureClipboard.pipe(
						executor.exitCode,
						Effect.flatMap((exitCode) =>
							exitCode === 0
								? Effect.void
								: Effect.fail(
										mapClipboardFailure("Failed to read image data from clipboard.", tool),
									),
						),
						Effect.mapError((error) =>
							error instanceof ClipboardError
								? error
								: mapClipboardFailure(
										`Failed to read image data from clipboard: ${String(error)}`,
										tool,
									),
						),
					)

					const exists = yield* fs.exists(clipboardFile).pipe(Effect.orElseSucceed(() => false))
					if (!exists) {
						return yield* Effect.fail(
							mapClipboardFailure("Clipboard does not contain image data.", tool),
						)
					}

					const stats = yield* fs.stat(clipboardFile).pipe(
						Effect.mapError(
							() =>
								new ImageAttachmentError({
									message: `Failed to inspect clipboard file ${clipboardFile}`,
								}),
						),
					)
					if (Number(stats.size) === 0) {
						yield* fs.remove(clipboardFile).pipe(Effect.ignore)
						return yield* Effect.fail(
							mapClipboardFailure("Clipboard does not contain image data.", tool),
						)
					}

					const content = yield* fs.readFile(clipboardFile).pipe(
						Effect.mapError(
							() =>
								new ImageAttachmentError({
									message: `Failed to read clipboard file ${clipboardFile}`,
								}),
						),
					)
					yield* fs.remove(clipboardFile).pipe(Effect.ignore)
					return {
						content,
						filename: "clipboard.png",
						mimeType: "image/png",
					}
				})

			const requireSelectedAttachment = (): Effect.Effect<
				{ readonly taskId: string; readonly attachment: ImageAttachment },
				ImageAttachmentError
			> =>
				Effect.gen(function* () {
					const current = yield* SubscriptionRef.get(currentAttachments)
					if (current === null || current.selectedIndex < 0) {
						return yield* Effect.fail(
							new ImageAttachmentError({ message: "No attachment selected" }),
						)
					}
					const attachment = current.attachments[current.selectedIndex]
					if (attachment === undefined) {
						return yield* Effect.fail(new ImageAttachmentError({ message: "Attachment not found" }))
					}
					return { taskId: current.taskId, attachment }
				})

			return {
				currentAttachments,
				overlayState,
				previewState,
				hasClipboardSupport: () => detectClipboardTool().pipe(Effect.map((tool) => tool !== null)),
				loadForTask: (taskId) =>
					list(taskId).pipe(
						Effect.tap((attachments) => setCurrentAttachments(taskId, attachments, -1)),
					),
				clearCurrent: () => SubscriptionRef.set(currentAttachments, null),
				openOverlay: (taskId) =>
					SubscriptionRef.set(overlayState, {
						mode: "menu",
						pathInput: "",
						isAttaching: false,
						taskId,
					}),
				closeOverlay: () =>
					SubscriptionRef.set(overlayState, {
						mode: "menu",
						pathInput: "",
						isAttaching: false,
						taskId: null,
					}),
				enterPathMode: () =>
					SubscriptionRef.update(
						overlayState,
						(state): OverlayState => ({ ...state, mode: "path" }),
					),
				exitPathMode: () =>
					SubscriptionRef.update(
						overlayState,
						(state): OverlayState => ({
							...state,
							mode: "menu",
							pathInput: "",
						}),
					),
				setPathInput: (value) =>
					SubscriptionRef.update(overlayState, (state) => ({ ...state, pathInput: value })),
				setAttaching,
				list: (taskId) => list(taskId),
				count: (taskId) => list(taskId).pipe(Effect.map((attachments) => attachments.length)),
				countBatch: (taskIds) =>
					Effect.gen(function* () {
						const projectPath = yield* resolveProjectPath()
						return yield* daemonRpcClient
							.attachmentCountBatch({
								issueIds: [...taskIds],
								projectPath,
							})
							.pipe(
								Effect.map((result) => result.counts),
								Effect.mapError((error) => mapDaemonRpcClientErrorMessage(error, mapRpcError)),
							)
					}),
				attachFile: (taskId, filePath) =>
					Effect.gen(function* () {
						const exists = yield* fs.exists(filePath).pipe(Effect.orElseSucceed(() => false))
						if (!exists) {
							return yield* Effect.fail(new FileNotFoundError({ path: filePath }))
						}
						yield* requireImageExtension(pathService.basename(filePath))
						yield* setAttaching(true)
						const projectPath = yield* resolveProjectPath()
						const attachment = yield* daemonRpcClient
							.attachmentAttachFile({
								issueId: taskId,
								filePath,
								projectPath,
							})
							.pipe(
								Effect.map((result) => result.attachment),
								Effect.mapError((error) => mapDaemonRpcClientErrorMessage(error, mapRpcError)),
							)
						const attachments = yield* list(taskId, projectPath)
						yield* refreshCurrentTaskAttachments(taskId, attachments)
						return attachment
					}).pipe(Effect.ensuring(setAttaching(false))),
				attachFromClipboard: (taskId) =>
					Effect.gen(function* () {
						yield* setAttaching(true)
						const projectPath = yield* resolveProjectPath()
						const clipboardImage = yield* readClipboardImage()
						const attachment = yield* daemonRpcClient
							.attachmentAttachClipboard({
								issueId: taskId,
								base64Content: Buffer.from(clipboardImage.content).toString("base64"),
								filename: clipboardImage.filename,
								mimeType: clipboardImage.mimeType,
								projectPath,
							})
							.pipe(
								Effect.map((result) => result.attachment),
								Effect.mapError((error) => mapDaemonRpcClientErrorMessage(error, mapRpcError)),
							)
						const attachments = yield* list(taskId, projectPath)
						yield* refreshCurrentTaskAttachments(taskId, attachments)
						return attachment
					}).pipe(Effect.ensuring(setAttaching(false))),
				remove: (taskId, attachmentId) => removeAttachment(taskId, attachmentId),
				open: (taskId, attachmentId) => openAttachment(taskId, attachmentId),
				getPath: (taskId, attachmentId) => materializePath(taskId, attachmentId),
				getPathForProjectRoot: (taskId, attachmentId, projectRootPath) =>
					materializePath(taskId, attachmentId, projectRootPath),
				selectNextAttachment: () =>
					SubscriptionRef.update(currentAttachments, (current) => {
						if (current === null || current.attachments.length === 0) {
							return current
						}
						return {
							...current,
							selectedIndex: Math.min(current.attachments.length - 1, current.selectedIndex + 1),
						}
					}),
				selectPreviousAttachment: () =>
					SubscriptionRef.update(currentAttachments, (current) => {
						if (current === null || current.attachments.length === 0) {
							return current
						}
						return {
							...current,
							selectedIndex: Math.max(-1, current.selectedIndex - 1),
						}
					}),
				getSelectedAttachment: () =>
					Effect.gen(function* () {
						const current = yield* SubscriptionRef.get(currentAttachments)
						if (current === null || current.selectedIndex < 0) {
							return null
						}
						return current.attachments[current.selectedIndex] ?? null
					}),
				openSelectedAttachment: () =>
					Effect.gen(function* () {
						const selected = yield* requireSelectedAttachment()
						yield* openAttachment(selected.taskId, selected.attachment.id)
						return selected.attachment
					}),
				removeSelectedAttachment: () =>
					Effect.gen(function* () {
						const selected = yield* requireSelectedAttachment()
						yield* removeAttachment(selected.taskId, selected.attachment.id)
						return selected.attachment
					}),
				openPreview: () =>
					Effect.gen(function* () {
						const selected = yield* requireSelectedAttachment()
						yield* SubscriptionRef.set(previewState, {
							taskId: selected.taskId,
							attachmentId: selected.attachment.id,
							filename: selected.attachment.filename,
							renderedImage: null,
							isLoading: true,
							error: null,
						})
						const filePath = yield* materializePath(selected.taskId, selected.attachment.id)
						const current = yield* SubscriptionRef.get(currentAttachments)
						const navInfo =
							current !== null && current.attachments.length > 1
								? ` (${current.selectedIndex + 1}/${current.attachments.length})`
								: ""
						const popupTitle = ` 📷 ${selected.attachment.filename}${navInfo} `
						const popupCommand = `viu ${quoteShell(filePath)}; echo ""; echo "Press any key to close..."; read -rsn1`
						yield* tmux
							.displayPopup({
								command: `bash -c ${quoteShell(popupCommand)}`,
								width: "90%",
								height: "90%",
								title: popupTitle,
							})
							.pipe(
								Effect.mapError(
									(error) =>
										new ImageAttachmentError({
											message: `Failed to open image popup: ${error.message}`,
										}),
								),
							)
						yield* SubscriptionRef.update(previewState, (state) => ({
							...state,
							isLoading: false,
						}))
						return selected.attachment
					}),
				closePreview: () => SubscriptionRef.set(previewState, emptyPreviewState()),
				previewNext: () =>
					SubscriptionRef.update(currentAttachments, (current) => {
						if (current === null || current.attachments.length === 0) {
							return current
						}
						return {
							...current,
							selectedIndex: Math.min(current.attachments.length - 1, current.selectedIndex + 1),
						}
					}),
				previewPrevious: () =>
					SubscriptionRef.update(currentAttachments, (current) => {
						if (current === null || current.attachments.length === 0) {
							return current
						}
						return {
							...current,
							selectedIndex: Math.max(0, current.selectedIndex - 1),
						}
					}),
				cleanupImagesForIssue: (taskId) =>
					Effect.gen(function* () {
						const attachments = yield* list(taskId)
						yield* Effect.forEach(attachments, (attachment) =>
							removeAttachment(taskId, attachment.id),
						)
						yield* SubscriptionRef.update(currentAttachments, (current) =>
							current?.taskId === taskId ? null : current,
						)
						return attachments.length
					}),
				cleanupImagesForIssues: (taskIds) =>
					Effect.gen(function* () {
						let totalDeleted = 0
						for (const taskId of taskIds) {
							const attachments = yield* list(taskId)
							totalDeleted += attachments.length
							yield* Effect.forEach(attachments, (attachment) =>
								removeAttachment(taskId, attachment.id),
							)
						}
						return totalDeleted
					}),
			} satisfies ImageAttachmentServiceApi
		}),
	},
) {}
