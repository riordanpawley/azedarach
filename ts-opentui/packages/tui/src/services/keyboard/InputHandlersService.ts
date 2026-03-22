import { AppConfig } from "@azedarach/config"
import type { CommandExecutor } from "@effect/platform"
import { Cause, Effect, Queue, type Record, Stream, SubscriptionRef } from "effect"
import {
	deriveWaitingSessionOptions,
	toWaitingSessionSourcesFromDaemonSnapshot,
} from "../../lib/waitingSessions.js"
import { generateJumpLabels } from "../../types.js"
import { EditorService, type JumpTarget } from "../EditorService.js"
import { ImageAttachmentService } from "../ImageAttachmentService.js"
import { NavigationService } from "../NavigationService.js"
import { OverlayService } from "../OverlayService.js"
import { ProjectStateService } from "../ProjectStateService.js"
import { SettingsService } from "../SettingsService.js"
import { ToastService } from "../ToastService.js"
import { TuiBoardStoreService } from "../TuiBoardStoreService.js"
import { TuiProjectContextService } from "../TuiProjectContextService.js"
import { TuiSessionAdapterService } from "../TuiSessionAdapterService.js"
import { ViewService } from "../ViewService.js"
import { TaskHandlersService } from "./TaskHandlersService.js"
import type { KeyMode } from "./types.js"

const MAX_WAITING_SESSION_SELECTIONS = 9

export interface InputHandlersServiceApi {
	readonly handleEscape: () => Effect.Effect<void>
	readonly handleTextInput: (key: string) => Effect.Effect<boolean>
	readonly handleJumpInput: (key: string) => Effect.Effect<void>
	readonly handleConfirmInput: (
		key: string,
	) => Effect.Effect<boolean, never, CommandExecutor.CommandExecutor>
	readonly handleMergeChoiceInput: (
		key: string,
	) => Effect.Effect<boolean, never, CommandExecutor.CommandExecutor>
	readonly handleForkInput: (key: string) => Effect.Effect<boolean>
	readonly handleBulkCleanupInput: (
		key: string,
	) => Effect.Effect<boolean, never, CommandExecutor.CommandExecutor>
	readonly handleDetailOverlayInput: (key: string) => Effect.Effect<boolean>
	readonly handleDiagnosticsOverlayInput: (key: string) => Effect.Effect<boolean>
	readonly handleImageAttachInput: (key: string) => Effect.Effect<boolean>
	readonly handleProjectSelectorInput: (key: string) => Effect.Effect<boolean>
	readonly handleWaitingSessionPickerInput: (key: string) => Effect.Effect<boolean>
	readonly handleImagePreviewInput: (key: string) => Effect.Effect<boolean>
	readonly handleSettingsInput: (key: string) => Effect.Effect<boolean>
	readonly computeJumpLabels: () => Effect.Effect<Record.ReadonlyRecord<string, JumpTarget>>
	readonly enterSpecWorkspace: () => Effect.Effect<void>
	readonly getEffectiveMode: () => Effect.Effect<KeyMode>
}

export class InputHandlersService extends Effect.Service<InputHandlersService>()(
	"InputHandlersService",
	{
		dependencies: [
			ToastService.Default,
			OverlayService.Default,
			EditorService.Default,
			NavigationService.Default,
			TuiBoardStoreService.Default,
			ImageAttachmentService.Default,
			TuiProjectContextService.Default,
			ProjectStateService.Default,
			ViewService.Default,
			SettingsService.Default,
			AppConfig.Default,
			TaskHandlersService.Default,
			TuiSessionAdapterService.Default,
		],
		effect: Effect.gen(function* () {
			const toast = yield* ToastService
			const overlay = yield* OverlayService
			const editor = yield* EditorService
			const nav = yield* NavigationService
			const board = yield* TuiBoardStoreService
			const imageAttachment = yield* ImageAttachmentService
			const projectContext = yield* TuiProjectContextService
			const projectState = yield* ProjectStateService
			const _view = yield* ViewService
			const settings = yield* SettingsService
			const appConfig = yield* AppConfig
			const taskHandlers = yield* TaskHandlersService
			const sessionAdapter = yield* TuiSessionAdapterService
			const backgroundActions = yield* Queue.unbounded<Effect.Effect<void>>()
			yield* Effect.forkScoped(
				Stream.fromQueue(backgroundActions).pipe(
					Stream.runForEach((action) =>
						action.pipe(
							Effect.catchAllCause((cause) =>
								Effect.logWarning(Cause.pretty(cause)).pipe(Effect.asVoid),
							),
						),
					),
				),
			)
			const enqueueBackground = (action: Effect.Effect<void>) =>
				Queue.offer(backgroundActions, action)

			const switchToProject = (
				projectName: string,
				options?: {
					readonly focusTaskId?: string
					readonly openDetailTaskId?: string
				},
			): Effect.Effect<boolean> =>
				Effect.gen(function* () {
					const projects = yield* projectContext.getProjects()
					const project = projects.find((candidate) => candidate.name === projectName)
					if (project === undefined) {
						yield* toast.show("error", `Project not found: ${projectName}`)
						return false
					}

					const currentProject = yield* SubscriptionRef.get(projectContext.currentProject)
					if (currentProject?.path === project.path) {
						yield* overlay.pop()
						if (options?.focusTaskId !== undefined) {
							yield* nav.setFocusedTask(options.focusTaskId)
						}
						if (options?.openDetailTaskId !== undefined) {
							yield* overlay.push({ _tag: "detail", taskId: options.openDetailTaskId })
						}
						return true
					}

					if (currentProject !== undefined) {
						yield* projectState.saveCurrentProjectState(currentProject.path)
						yield* board.saveToCache(currentProject.path)
					}

					yield* overlay.pop()
					yield* Effect.logInfo(
						`[project-switch] selected=${project.name} path=${project.path} before-board-switch`,
					)
					const { cacheHit, refreshFailed } = yield* board.switchToProject(
						project.path,
						Effect.void,
					)
					const taskCountAfterSwitch = (yield* board.getTasks()).length
					yield* Effect.logInfo(
						`[project-switch] selected=${project.name} path=${project.path} cacheHit=${String(cacheHit)} after-board-switch taskCount=${taskCountAfterSwitch}`,
					)
					yield* projectContext.switchProject(project.name)
					yield* projectState.withPersistenceSuspended(
						Effect.gen(function* () {
							yield* projectState.restoreProjectState(project.path)
							if (options?.focusTaskId !== undefined) {
								yield* nav.setFocusedTask(options.focusTaskId)
							}
						}),
					)
					yield* projectState.saveCurrentProjectState(project.path)

					if (refreshFailed) {
						yield* toast.show("error", `Project switch refresh failed: ${project.name}`)
					} else if (taskCountAfterSwitch > 0) {
						yield* toast.show(
							cacheHit ? "success" : "info",
							`Loaded: ${project.name} (${taskCountAfterSwitch} tasks)`,
						)
					} else {
						yield* toast.show("warning", `Loaded: ${project.name} (0 tasks). Press r to refresh.`)
					}

					if (options?.openDetailTaskId !== undefined) {
						yield* overlay.push({ _tag: "detail", taskId: options.openDetailTaskId })
					}
					return true
				}).pipe(
					Effect.catchTag("TuiProjectContextError", (error) =>
						toast.show("error", `Project switch failed: ${error.message}`).pipe(Effect.as(false)),
					),
				)

			const getSelectableWaitingSessions = () =>
				Effect.gen(function* () {
					const sessions = yield* sessionAdapter
						.listActive()
						.pipe(Effect.catchTag("TuiSessionAdapterServiceError", () => Effect.succeed([])))
					const projects = yield* projectContext.getProjects()
					const currentProject = yield* SubscriptionRef.get(projectContext.currentProject)

					const waitingSessions = deriveWaitingSessionOptions(
						toWaitingSessionSourcesFromDaemonSnapshot(sessions),
						projects,
						currentProject?.path,
					)
					return waitingSessions.slice(0, MAX_WAITING_SESSION_SELECTIONS)
				})

			const handleEscape = (): Effect.Effect<void> =>
				Effect.gen(function* () {
					const hasOverlay = yield* overlay.isOpen()
					if (hasOverlay) {
						yield* overlay.pop()
						return
					}

					const mode = yield* editor.getMode()
					if (mode._tag !== "normal") {
						yield* editor.exitToNormal()
					}
				})

			const handleTextInput = (key: string): Effect.Effect<boolean> =>
				Effect.gen(function* () {
					const mode = yield* editor.getMode()
					if (mode._tag !== "search") {
						return false
					}

					if (key === "return") {
						yield* editor.exitToNormal()
						return true
					}
					if (key === "backspace") {
						if (mode.query.length > 0) {
							yield* editor.updateSearch(mode.query.slice(0, -1))
						}
						return true
					}
					if (key.length === 1 && !key.startsWith("C-")) {
						yield* editor.updateSearch(mode.query + key)
						return true
					}
					return false
				})

			const handleJumpInput = (key: string): Effect.Effect<void> =>
				Effect.gen(function* () {
					const mode = yield* editor.getMode()
					if (mode._tag !== "goto" || mode.gotoSubMode !== "jump") {
						return
					}

					if (mode.pendingJumpKey === null) {
						yield* editor.setPendingJumpKey(key)
						return
					}

					const label = mode.pendingJumpKey + key
					const target = mode.jumpLabels?.[label]
					if (target !== undefined) {
						yield* nav.jumpTo(target.columnIndex, target.taskIndex)
					}
					yield* editor.exitToNormal()
				})

			const handleConfirmInput = (key: string) =>
				Effect.gen(function* () {
					const currentOverlay = yield* overlay.current()
					if (currentOverlay?._tag !== "confirm" && currentOverlay?._tag !== "gitPull") {
						return false
					}

					if (key === "y" || key === "return") {
						yield* overlay.pop()
						yield* currentOverlay.onConfirm
						return true
					}
					if (key === "n" || key === "q" || key === "escape") {
						yield* overlay.pop()
						return true
					}
					return true
				})

			const handleMergeChoiceInput = (key: string) =>
				Effect.gen(function* () {
					const currentOverlay = yield* overlay.current()
					if (currentOverlay?._tag !== "mergeChoice") {
						return false
					}

					if (key === "m") {
						yield* overlay.pop()
						yield* currentOverlay.onMerge
						return true
					}
					if (key === "s") {
						yield* overlay.pop()
						yield* currentOverlay.onSkip
						return true
					}
					if (key === "escape" || key === "q") {
						yield* overlay.pop()
						return true
					}
					return true
				})

			const handleForkInput = (key: string): Effect.Effect<boolean> =>
				Effect.gen(function* () {
					const currentOverlay = yield* overlay.current()
					if (currentOverlay?._tag !== "fork") {
						return false
					}

					const blockedReason = currentOverlay.blockedReason
					if (key === "escape" || key === "q") {
						yield* overlay.pop()
						return true
					}

					if (key === "1" || key === "2" || key === "3") {
						if (blockedReason !== undefined) {
							yield* toast.show("warning", blockedReason)
							return true
						}

						yield* overlay.pop()
						if (key === "1") {
							yield* taskHandlers.forkFromCurrent(currentOverlay.sourceTaskId)
							return true
						}
						if (key === "2") {
							yield* taskHandlers.forkWithNewEpic(currentOverlay.sourceTaskId)
							return true
						}
						if (currentOverlay.parentEpicId === undefined) {
							yield* toast.show("warning", "No parent epic to fork under")
							return true
						}
						yield* taskHandlers.forkUnderParent(
							currentOverlay.sourceTaskId,
							currentOverlay.parentEpicId,
						)
						return true
					}

					return true
				})

			const handleBulkCleanupInput = (key: string) =>
				Effect.gen(function* () {
					const currentOverlay = yield* overlay.current()
					if (currentOverlay?._tag !== "bulkCleanup") {
						return false
					}

					if (key === "w") {
						yield* overlay.pop()
						yield* currentOverlay.onWorktreeOnly
						return true
					}
					if (key === "f") {
						yield* overlay.pop()
						yield* currentOverlay.onFullCleanup
						return true
					}
					if (key === "escape" || key === "q") {
						yield* overlay.pop()
						return true
					}
					return true
				})

			const handleDetailOverlayInput = (key: string): Effect.Effect<boolean> =>
				Effect.gen(function* () {
					const currentOverlay = yield* overlay.current()
					if (currentOverlay?._tag !== "detail") {
						return false
					}

					const taskId = currentOverlay.taskId
					if (key === "escape" || key === "q" || key === "return") {
						yield* overlay.pop()
						return true
					}
					if (key === "C-u") {
						yield* overlay.scroll("halfPage", -1, "detail")
						return true
					}
					if (key === "C-d") {
						yield* overlay.scroll("halfPage", 1, "detail")
						return true
					}
					if (key === "j" || key === "down") {
						yield* imageAttachment.selectNextAttachment()
						return true
					}
					if (key === "k" || key === "up") {
						yield* imageAttachment.selectPreviousAttachment()
						return true
					}
					if (key === "o") {
						yield* imageAttachment.openSelectedAttachment().pipe(
							Effect.tap(() => toast.show("success", "Opening image...")),
							Effect.catchAll((error) =>
								toast.show("error", `Open attachment failed: ${error.message}`),
							),
						)
						return true
					}
					if (key === "x") {
						yield* imageAttachment.removeSelectedAttachment().pipe(
							Effect.tap((removed) => toast.show("success", `Removed: ${removed.filename}`)),
							Effect.catchAll((error) =>
								toast.show("error", `Remove attachment failed: ${error.message}`),
							),
						)
						return true
					}
					if (key === "i") {
						yield* overlay.push({ _tag: "imageAttach", taskId })
						return true
					}
					if (key === "v") {
						const selected = yield* imageAttachment.getSelectedAttachment()
						if (selected === null) {
							yield* toast.show("info", "Select an attachment to preview (j/k)")
							return true
						}
						yield* imageAttachment
							.openPreview()
							.pipe(
								Effect.catchAll((error) => toast.show("error", `Preview failed: ${error.message}`)),
							)
						return true
					}
					return false
				})

			const handleDiagnosticsOverlayInput = (key: string): Effect.Effect<boolean> =>
				Effect.gen(function* () {
					const currentOverlay = yield* overlay.current()
					if (currentOverlay?._tag !== "diagnostics") {
						return false
					}

					if (key === "j" || key === "down") {
						yield* overlay.scroll("line", 1, "diagnostics")
						return true
					}
					if (key === "k" || key === "up") {
						yield* overlay.scroll("line", -1, "diagnostics")
						return true
					}
					if (key === "C-u") {
						yield* overlay.scroll("halfPage", -1, "diagnostics")
						return true
					}
					if (key === "C-d") {
						yield* overlay.scroll("halfPage", 1, "diagnostics")
						return true
					}
					if (key === "q") {
						yield* overlay.pop()
						return true
					}
					return false
				})

			const handleImageAttachInput = (key: string): Effect.Effect<boolean> =>
				Effect.gen(function* () {
					const currentOverlay = yield* overlay.current()
					if (currentOverlay?._tag !== "imageAttach") {
						return false
					}

					const overlayTaskId = currentOverlay.taskId
					let state = yield* SubscriptionRef.get(imageAttachment.overlayState)
					if (state.taskId === null) {
						yield* imageAttachment.openOverlay(overlayTaskId)
						state = yield* SubscriptionRef.get(imageAttachment.overlayState)
					}

					if (key === "escape" || (key === "q" && state.mode !== "path")) {
						if (state.mode === "path") {
							yield* imageAttachment.exitPathMode()
						} else {
							yield* imageAttachment.closeOverlay()
							yield* overlay.pop()
						}
						return true
					}

					if (state.mode === "path") {
						if (key === "return") {
							if (state.pathInput.trim().length > 0 && !state.isAttaching) {
								yield* imageAttachment.setAttaching(true)
								yield* imageAttachment.attachFile(overlayTaskId, state.pathInput.trim()).pipe(
									Effect.tap((attachment) =>
										toast.show("success", `Image attached: ${attachment.filename}`),
									),
									Effect.tap(() => imageAttachment.closeOverlay()),
									Effect.tap(() => overlay.pop()),
									Effect.catchTags({
										ImageAttachmentError: (error) =>
											toast.show("error", `Failed to attach: ${error.message}`),
										FileNotFoundError: (error) =>
											toast.show("error", `Failed to attach: ${error.path}`),
									}),
									Effect.ensuring(imageAttachment.setAttaching(false)),
								)
							}
							return true
						}
						if (key === "backspace") {
							if (state.pathInput.length > 0) {
								yield* imageAttachment.setPathInput(state.pathInput.slice(0, -1))
							}
							return true
						}
						if (key.length === 1 && !key.startsWith("C-")) {
							yield* imageAttachment.setPathInput(state.pathInput + key)
							return true
						}
						return true
					}

					if (key === "p" || key === "v") {
						if (!state.isAttaching) {
							const hasClipboard = yield* imageAttachment.hasClipboardSupport()
							if (!hasClipboard) {
								yield* toast.show(
									"error",
									process.platform === "darwin"
										? "Clipboard access not available"
										: "No clipboard tool found (install xclip or wl-clipboard)",
								)
								return true
							}
							yield* imageAttachment.setAttaching(true)
							yield* imageAttachment.attachFromClipboard(overlayTaskId).pipe(
								Effect.tap((attachment) =>
									toast.show("success", `Image attached: ${attachment.filename}`),
								),
								Effect.tap(() => imageAttachment.closeOverlay()),
								Effect.tap(() => overlay.pop()),
								Effect.catchTags({
									ImageAttachmentError: (error) =>
										toast.show("error", `Clipboard: ${error.message}`),
									ClipboardError: (error) => toast.show("error", `Clipboard: ${error.message}`),
								}),
								Effect.ensuring(imageAttachment.setAttaching(false)),
							)
						}
						return true
					}

					if (key === "f") {
						yield* imageAttachment.enterPathMode()
						return true
					}

					return true
				})

			const handleProjectSelectorInput = (key: string): Effect.Effect<boolean> =>
				Effect.gen(function* () {
					const num = Number.parseInt(key, 10)
					if (num >= 1 && num <= 9) {
						const projects = yield* projectContext.getProjects()
						if (num <= projects.length) {
							const project = projects[num - 1]
							if (project !== undefined) {
								// Keep background switch failures structured and readable.
								yield* enqueueBackground(
									switchToProject(project.name).pipe(
										Effect.asVoid,
										Effect.catchAllCause((cause) =>
											Effect.logWarning(Cause.pretty(cause)).pipe(Effect.asVoid),
										),
									),
								)
							}
						} else {
							yield* toast.show("error", `No project at position ${num}`)
						}
						return true
					}
					if (key === "q") {
						yield* overlay.pop()
						return true
					}
					if (key === "escape") {
						return false
					}
					return true
				})

			const handleWaitingSessionPickerInput = (key: string): Effect.Effect<boolean> =>
				Effect.gen(function* () {
					const num = Number.parseInt(key, 10)
					if (num >= 1 && num <= 9) {
						const waitingSessions = yield* getSelectableWaitingSessions()
						if (num > waitingSessions.length) {
							yield* toast.show("error", `No waiting session at position ${num}`)
							return true
						}

						const waitingSession = waitingSessions[num - 1]
						if (waitingSession === undefined) {
							yield* toast.show("error", `No waiting session at position ${num}`)
							return true
						}
						if (!waitingSession.isRegisteredProject) {
							yield* toast.show(
								"warning",
								`Project is not registered: ${waitingSession.projectName}`,
							)
							return true
						}

						yield* enqueueBackground(
							switchToProject(waitingSession.projectName, {
								focusTaskId: waitingSession.issueId,
								openDetailTaskId: waitingSession.issueId,
							}).pipe(
								Effect.asVoid,
								Effect.catchAllCause((cause) =>
									Effect.logWarning(Cause.pretty(cause)).pipe(Effect.asVoid),
								),
							),
						)
						return true
					}

					if (key === "q") {
						yield* overlay.pop()
						return true
					}
					if (key === "escape") {
						return false
					}
					return true
				})

			const handleImagePreviewInput = (key: string): Effect.Effect<boolean> =>
				Effect.gen(function* () {
					const currentOverlay = yield* overlay.current()
					if (currentOverlay?._tag !== "imagePreview") {
						return false
					}

					if (key === "escape" || key === "q") {
						yield* imageAttachment.closePreview()
						yield* overlay.pop()
						return true
					}
					if (key === "j" || key === "down") {
						yield* imageAttachment.previewNext()
						yield* imageAttachment
							.openPreview()
							.pipe(
								Effect.catchAll((error) => toast.show("error", `Preview failed: ${error.message}`)),
							)
						return true
					}
					if (key === "k" || key === "up") {
						yield* imageAttachment.previewPrevious()
						yield* imageAttachment
							.openPreview()
							.pipe(
								Effect.catchAll((error) => toast.show("error", `Preview failed: ${error.message}`)),
							)
						return true
					}
					if (key === "o") {
						yield* imageAttachment.openSelectedAttachment().pipe(
							Effect.tap(() => toast.show("success", "Opening image...")),
							Effect.catchAll((error) => toast.show("error", error.message)),
						)
						return true
					}
					return true
				})

			const handleSettingsInput = (key: string) =>
				Effect.gen(function* () {
					if (key === "escape" || key === "q") {
						yield* settings.close()
						yield* overlay.pop()
						return true
					}
					if (key === "j" || key === "down") {
						yield* settings.moveDown()
						return true
					}
					if (key === "k" || key === "up") {
						yield* settings.moveUp()
						return true
					}
					if (key === "space" || key === "return") {
						yield* settings.toggleCurrent()
						return true
					}
					if (key === "e") {
						const { configPath, backupContent } = yield* settings.openInEditor()
						yield* settings.validateAfterEdit(configPath, backupContent)
						return true
					}
					return true
				})

			const computeJumpLabels = (): Effect.Effect<Record.ReadonlyRecord<string, JumpTarget>> =>
				Effect.gen(function* () {
					const mode = yield* editor.getMode()
					const sortConfig = yield* editor.getSortConfig()
					const filterConfig = yield* editor.getFilterConfig()
					const searchQuery = mode._tag === "search" ? mode.query : ""

					const tasksByColumn = yield* board.getFilteredTasksByColumn(
						searchQuery,
						sortConfig,
						filterConfig,
					)
					const drillDownChildIds = yield* SubscriptionRef.get(nav.drillDownChildIds)
					const filteredTasksByColumn =
						drillDownChildIds.size === 0
							? tasksByColumn.map((column) =>
									column.filter((task) => task.parentEpicId === undefined),
								)
							: tasksByColumn.map((column) =>
									column.filter((task) => drillDownChildIds.has(task.id)),
								)

					const currentPos = yield* nav.getPosition()
					const chromeHeight = 9
					const taskCardHeight = 4
					const rows = process.stdout.rows || 24
					const maxVisible = Math.max(1, Math.floor((rows - chromeHeight) / taskCardHeight))

					const getVisibleWindow = (
						taskCount: number,
						selectedIndex: number,
					): { readonly startIndex: number; readonly endIndex: number } => {
						if (taskCount <= maxVisible) {
							return { startIndex: 0, endIndex: taskCount }
						}
						let startIndex = 0
						if (selectedIndex >= maxVisible - 1) {
							startIndex = Math.min(selectedIndex - maxVisible + 2, taskCount - maxVisible)
						}
						startIndex = Math.max(0, startIndex)
						return { startIndex, endIndex: startIndex + maxVisible }
					}

					const visibleTasks: Array<{
						readonly taskId: string
						readonly columnIndex: number
						readonly taskIndex: number
					}> = []

					filteredTasksByColumn.forEach((tasks, columnIndex) => {
						const selectedIndex = columnIndex === currentPos.columnIndex ? currentPos.taskIndex : 0
						const { startIndex, endIndex } = getVisibleWindow(tasks.length, selectedIndex)
						for (
							let taskIndex = startIndex;
							taskIndex < endIndex && taskIndex < tasks.length;
							taskIndex += 1
						) {
							const task = tasks[taskIndex]
							if (task !== undefined) {
								visibleTasks.push({ taskId: task.id, columnIndex, taskIndex })
							}
						}
					})

					const keyboardConfig = yield* appConfig.getKeyboardConfig()
					const labels = generateJumpLabels(visibleTasks.length, keyboardConfig.jumpLabelChars)
					const jumpTargets: Record<string, JumpTarget> = {}
					visibleTasks.forEach(({ taskId, columnIndex, taskIndex }, index) => {
						const label = labels[index]
						if (label !== undefined) {
							jumpTargets[label] = { taskId, columnIndex, taskIndex }
						}
					})
					return jumpTargets
				})

			const getEffectiveMode = (): Effect.Effect<KeyMode> =>
				Effect.gen(function* () {
					if (yield* overlay.isOpen()) {
						return "overlay"
					}

					const mode = yield* editor.getMode()
					switch (mode._tag) {
						case "normal":
							return "normal"
						case "select":
							return "select"
						case "action":
							return "action"
						case "goto":
							return mode.gotoSubMode === "pending" ? "goto-pending" : "goto-jump"
						case "search":
							return "search"
						case "sort":
							return "sort"
						case "filter":
							return "filter"
						case "spec":
							return "spec"
						case "orchestrate":
							return "orchestrate"
						case "mergeSelect":
							return "mergeSelect"
					}
				})

			const enterSpecWorkspace = (): Effect.Effect<void> =>
				Effect.gen(function* () {
					const specConfig = yield* appConfig.getSpecConfig()
					if (!specConfig.enabled) {
						yield* toast.show(
							"error",
							"Spec workspace is disabled. Run `az config set spec.enabled true` or enable `spec.enabled` in `.azedarach.json` to use it.",
						)
						return
					}
					yield* editor.enterSpecWorkspace()
				})

			return {
				handleEscape,
				handleTextInput,
				handleJumpInput,
				handleConfirmInput,
				handleMergeChoiceInput,
				handleForkInput,
				handleBulkCleanupInput,
				handleDetailOverlayInput,
				handleDiagnosticsOverlayInput,
				handleImageAttachInput,
				handleProjectSelectorInput,
				handleWaitingSessionPickerInput,
				handleImagePreviewInput,
				handleSettingsInput,
				computeJumpLabels,
				enterSpecWorkspace,
				getEffectiveMode,
			}
		}),
	},
) {}
