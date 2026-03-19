/**
 * App component - root component with Helix-style modal keybindings
 *
 * Migrated to use atomic Effect services via custom hooks.
 */

import { Result } from "@effect-atom/atom"
import { useAtomSet, useAtomValue } from "@effect-atom/atom-react"
import { MouseButton, type MouseEvent } from "@opentui/core"
import { useKeyboard, useRenderer } from "@opentui/react"
import { useEffect, useMemo, useRef, useState } from "react"
import { ActionPalette } from "./ActionPalette.js"
import { AICreatePrompt } from "./AICreatePrompt.js"
import {
	activeSessionsCountAtom,
	aiCreateTaskAtom,
	appConfigAtom,
	boardIsLoadingAtom,
	boardRenderStateAtom,
	boardTasksAtom,
	createTaskAtom,
	currentProjectAtom,
	DEFAULT_SPEC_WORKSPACE_STATE,
	devServerSyncStarterAtom,
	drillDownEpicAtom,
	drillDownPhasesAtom,
	exitToNormalAtom,
	focusedIssuePrimaryDevServerAtom,
	focusedTaskRunningOperationAtom,
	forkCreateChildAtom,
	forkCreateEpicAtom,
	handleColumnPagerMouseInteractionAtom,
	handleKeyAtom,
	handleTaskMouseInteractionAtom,
	isOnlineAtom,
	isRefreshingGitStatsAtom,
	jumpToAtom,
	refreshSpecWorkspaceAtom,
	sessionMonitorStarterAtom,
	setVisibleTaskIdsAtom,
	specWorkspaceStateAtom,
	totalTasksCountAtom,
	viewModeAtom,
	waitingSessionOptionsAtom,
	workflowModeAtom,
} from "./atoms.js"
import { Board } from "./Board.js"
import { BulkCleanupOverlay } from "./BulkCleanupOverlay.js"
import { ConfirmOverlay } from "./ConfirmOverlay.js"
import { CreateTaskPrompt } from "./CreateTaskPrompt.js"
import { DetailPanel } from "./DetailPanel.js"
import { DevServerMenu } from "./DevServerMenu.js"
import { DiagnosticsOverlay } from "./DiagnosticsOverlay.js"
import { DiffViewer } from "./DiffViewer/index.js"
import { FilterMenu } from "./FilterMenu.js"
import { ForkOverlay } from "./ForkOverlay.js"
import { GitPullOverlay } from "./GitPullOverlay.js"
import { HelpOverlay } from "./HelpOverlay.js"
import { useEditorMode, useNavigation, useOverlays, useToasts } from "./hooks/index.js"
import { ImageAttachOverlay } from "./ImageAttachOverlay.js"
import { ImagePreviewOverlay } from "./ImagePreviewOverlay.js"
import { deriveCurrentProjectWaitingIssueIds } from "./lib/waitingSessions.js"
import { MergeChoiceOverlay } from "./MergeChoiceOverlay.js"
import { OrchestrationOverlay } from "./OrchestrationOverlay.js"
import { PlanningOverlay } from "./PlanningOverlay.js"
import { ProjectSelector } from "./ProjectSelector.js"
import { shouldRequestShutdownFromDirectQuitFallback } from "./quitFallbackPolicy.js"
import { isSmallScreen } from "./responsive.js"
import { requestShutdown } from "./runtimeControl.js"
import { SearchInput } from "./SearchInput.js"
import { SettingsOverlay } from "./SettingsOverlay.js"
import { SortMenu } from "./SortMenu.js"
import { SpecWorkspace } from "./SpecWorkspace.js"
import { StatusBar } from "./StatusBar.js"
import { TASK_CARD_HEIGHT } from "./TaskCard.js"
import { ToastContainer } from "./Toast.js"
import { theme } from "./theme.js"
import type { TaskWithSession } from "./types.js"
import { COLUMNS } from "./types.js"
import { killActivePopup } from "./utils/popupCleanup.js"
import { detectTmuxCapabilities } from "./utils/tmuxCapabilities.js"
import { WaitingSessionPicker } from "./WaitingSessionPicker.js"

// ============================================================================
// App Component
// ============================================================================

const computeKanbanVisibleTaskIds = (
	tasksByColumn: TaskWithSession[][],
	activeColumnIndex: number,
	activeTaskIndex: number,
	maxVisible: number,
): string[] => {
	const visibleIds: string[] = []
	const windowSize = Math.max(1, maxVisible)

	for (let colIndex = 0; colIndex < tasksByColumn.length; colIndex++) {
		const columnTasks = tasksByColumn[colIndex] ?? []
		if (columnTasks.length <= windowSize) {
			visibleIds.push(...columnTasks.map((task) => task.id))
			continue
		}

		const selectedIdx = colIndex === activeColumnIndex ? activeTaskIndex : 0
		const halfWindow = Math.floor(windowSize / 2)
		let startIdx = 0
		if (selectedIdx <= halfWindow) {
			startIdx = 0
		} else if (selectedIdx >= columnTasks.length - halfWindow) {
			startIdx = columnTasks.length - windowSize
		} else {
			startIdx = selectedIdx - halfWindow
		}
		startIdx = Math.max(0, startIdx)
		const endIdx = Math.min(startIdx + windowSize, columnTasks.length)
		for (let idx = startIdx; idx < endIdx; idx++) {
			const task = columnTasks[idx]
			if (task) visibleIds.push(task.id)
		}
	}

	return visibleIds
}

const sortCompactTasks = (tasks: readonly TaskWithSession[]): TaskWithSession[] => {
	const statusOrder = new Map<string, number>()
	COLUMNS.forEach((col, idx) => {
		statusOrder.set(col.status, idx)
	})

	return [...tasks].sort((a, b) => {
		const statusDiff = (statusOrder.get(a.status) ?? 99) - (statusOrder.get(b.status) ?? 99)
		if (statusDiff !== 0) return statusDiff
		return a.priority - b.priority
	})
}

const computeCompactVisibleTaskIds = (
	tasks: readonly TaskWithSession[],
	selectedTaskId: string | undefined,
	maxVisible: number,
): string[] => {
	const windowSize = Math.max(1, maxVisible)
	const sortedTasks = sortCompactTasks(tasks)

	let selectedIndex = 0
	if (selectedTaskId) {
		const index = sortedTasks.findIndex((task) => task.id === selectedTaskId)
		if (index >= 0) selectedIndex = index
	}

	let startIndex = 0
	if (sortedTasks.length > windowSize) {
		startIndex = Math.max(0, selectedIndex - Math.floor(windowSize / 2))
		startIndex = Math.min(startIndex, sortedTasks.length - windowSize)
	}

	const endIndex = Math.min(startIndex + windowSize, sortedTasks.length)
	return sortedTasks.slice(startIndex, endIndex).map((task) => task.id)
}

const HydratedApp = () => {
	// ═══════════════════════════════════════════════════════════════════════════
	// Hooks - Atomic State Management
	// ═══════════════════════════════════════════════════════════════════════════

	const { toasts, dismissToast } = useToasts()
	const {
		dismiss: dismissOverlay,
		currentOverlay,
		showingHelp,
		showingDetail,
		showingCreate,
		showingAICreate,
		showingSettings,
		showingImageAttach,
		showingImagePreview,
		showingConfirm,
		showingGitPull,
		showingMergeChoice,
		showingBulkCleanup,
		showingFork,
		showingDiagnostics,
		showingProjectSelector,
		showingWaitingSessionPicker,
		showingDiffViewer,
		showingDevServerMenu,
		showingPlanning,
	} = useOverlays()

	const {
		mode,
		selectedIds,
		searchQuery,
		pendingJumpKey,
		jumpLabels,
		sortConfig,
		filterConfig,
		activeFilterField,
		mergeSelectSourceId,
		isJump,
		isAction,
		isSearch,
		isSort,
		isFilter,
		isOrchestrate,
	} = useEditorMode()

	// ═══════════════════════════════════════════════════════════════════════════
	// Data Atoms
	// ═══════════════════════════════════════════════════════════════════════════
	// Use derived atom that handles both normal and drill-down filtering
	// All computation happens in atoms - React just renders
	const boardRenderState = useAtomValue(boardRenderStateAtom)
	const tasksByColumn = useMemo(
		() => boardRenderState.tasksByColumn.map((column) => [...column]),
		[boardRenderState],
	)
	const [startupCapabilities, setStartupCapabilities] = useState<{
		readonly boardReady: boolean
		readonly sessionMonitorReady: boolean
		readonly specReady: boolean
	}>({
		boardReady: false,
		sessionMonitorReady: false,
		specReady: false,
	})
	const boardTasks = useAtomValue(
		boardTasksAtom,
		Result.getOrElse(() => []),
	)
	const waitingSessions = useAtomValue(waitingSessionOptionsAtom)
	const waitingIssueIds = useMemo(
		() => deriveCurrentProjectWaitingIssueIds(waitingSessions),
		[waitingSessions],
	)

	const projectName = useAtomValue(
		currentProjectAtom,
		Result.getOrElse(() => undefined),
	)?.name

	const handleKey = useAtomSet(handleKeyAtom, { mode: "promise" })
	const handleTaskMouseInteraction = useAtomSet(handleTaskMouseInteractionAtom, { mode: "promise" })
	const handleColumnPagerMouseInteraction = useAtomSet(handleColumnPagerMouseInteractionAtom, {
		mode: "promise",
	})
	const jumpTo = useAtomSet(jumpToAtom, { mode: "promise" })
	const exitToNormal = useAtomSet(exitToNormalAtom, { mode: "promise" })

	const startSessionMonitor = useAtomSet(sessionMonitorStarterAtom, { mode: "promise" })
	useEffect(() => {
		if (!startupCapabilities.sessionMonitorReady) return
		startSessionMonitor()
	}, [startSessionMonitor, startupCapabilities.sessionMonitorReady])

	const startDevServerSync = useAtomSet(devServerSyncStarterAtom, { mode: "promise" })
	useEffect(() => {
		startDevServerSync(undefined)
	}, [startDevServerSync])

	// Actions for prompts (these bypass keyboard handling)
	// Full orchestration (dismiss, create, navigate, toast) happens in the atoms
	const createTask = useAtomSet(createTaskAtom, { mode: "promise" })
	const forkCreateChild = useAtomSet(forkCreateChildAtom, { mode: "promise" })
	const forkCreateEpic = useAtomSet(forkCreateEpicAtom, { mode: "promise" })
	const aiCreateTask = useAtomSet(aiCreateTaskAtom, { mode: "promise" })

	const viewMode = useAtomValue(
		viewModeAtom,
		Result.getOrElse(() => "kanban" as const),
	)

	const displayDevServer = useAtomValue(focusedIssuePrimaryDevServerAtom)

	const runningOperation = useAtomValue(focusedTaskRunningOperationAtom)

	const isOnline = useAtomValue(
		isOnlineAtom,
		Result.getOrElse(() => true),
	)

	const workflowMode = useAtomValue(workflowModeAtom)
	const tmuxCapabilities = useMemo(() => detectTmuxCapabilities(), [])
	const appConfigResult = useAtomValue(appConfigAtom)
	const specEnabled = Result.isSuccess(appConfigResult) ? appConfigResult.value.spec.enabled : false

	// Board loading state for status bar indicator
	const isLoading = useAtomValue(
		boardIsLoadingAtom,
		Result.getOrElse(() => false),
	)

	// Git stats refresh loading state
	const isRefreshingGitStats = useAtomValue(
		isRefreshingGitStatsAtom,
		Result.getOrElse(() => false),
	)
	const setVisibleTaskIds = useAtomSet(setVisibleTaskIdsAtom, { mode: "promise" })
	const refreshSpecWorkspace = useAtomSet(refreshSpecWorkspaceAtom, { mode: "promise" })

	useEffect(() => {
		if (startupCapabilities.boardReady) return
		if (boardRenderState._tag === "ready" || !isLoading) {
			setStartupCapabilities({
				boardReady: true,
				sessionMonitorReady: true,
				specReady: true,
			})
		}
	}, [boardRenderState._tag, isLoading, startupCapabilities.boardReady])

	// Navigation hook (needs tasksByColumn)
	const { columnIndex, taskIndex, selectedTask } = useNavigation(tasksByColumn)

	// Dependency phases for drill-down mode
	const phases = useAtomValue(drillDownPhasesAtom)

	// Drilldown epic ID (when viewing inside an epic)
	// Convert null to undefined for cleaner prop typing
	const drillDownEpicId =
		useAtomValue(
			drillDownEpicAtom,
			Result.getOrElse(() => null),
		) ?? undefined

	// Recalculate max visible tasks from live terminal dimensions.
	const CHROME_HEIGHT = 6
	const terminalRows = process.stdout.rows || 24
	const terminalColumns = process.stdout.columns || 80
	const baseMaxVisibleTasks = Math.max(
		1,
		Math.floor((terminalRows - CHROME_HEIGHT) / TASK_CARD_HEIGHT),
	)
	const maxVisibleTasks = drillDownEpicId ? baseMaxVisibleTasks - 1 : baseMaxVisibleTasks
	const useSingleKanbanColumn = viewMode === "kanban" && isSmallScreen(terminalColumns)

	const visibleTaskIds = useMemo(() => {
		if (viewMode === "compact") {
			return computeCompactVisibleTaskIds(tasksByColumn.flat(), selectedTask?.id, maxVisibleTasks)
		}
		return computeKanbanVisibleTaskIds(tasksByColumn, columnIndex, taskIndex, maxVisibleTasks)
	}, [viewMode, tasksByColumn, selectedTask?.id, columnIndex, taskIndex, maxVisibleTasks])

	useEffect(() => {
		if (!startupCapabilities.boardReady) return
		setVisibleTaskIds(visibleTaskIds)
	}, [setVisibleTaskIds, startupCapabilities.boardReady, visibleTaskIds])

	const specWorkspaceState = useAtomValue(
		specWorkspaceStateAtom,
		Result.getOrElse(() => DEFAULT_SPEC_WORKSPACE_STATE),
	)

	useEffect(() => {
		if (!startupCapabilities.specReady) {
			return
		}
		if (mode._tag !== "spec" || !specEnabled) {
			return
		}

		refreshSpecWorkspace(undefined)
		const interval = setInterval(() => {
			refreshSpecWorkspace(undefined)
		}, 4000)

		return () => {
			clearInterval(interval)
		}
	}, [mode, refreshSpecWorkspace, specEnabled, startupCapabilities.specReady])

	useEffect(() => {
		if (mode._tag === "spec" && !specEnabled) {
			exitToNormal(undefined)
		}
	}, [exitToNormal, mode, specEnabled])

	// Renderer access for manual redraw
	const renderer = useRenderer()

	useEffect(() => {
		let lastRows = process.stdout.rows || 24
		let lastColumns = process.stdout.columns || 80

		const handleResize = () => {
			lastRows = process.stdout.rows || 24
			lastColumns = process.stdout.columns || 80
			renderer.requestRender()
		}

		const reconcileTerminalSize = () => {
			const nextRows = process.stdout.rows || 24
			const nextColumns = process.stdout.columns || 80
			if (nextRows !== lastRows || nextColumns !== lastColumns) {
				lastRows = nextRows
				lastColumns = nextColumns
				renderer.requestRender()
			}
		}

		process.stdout.on("resize", handleResize)
		const interval = setInterval(reconcileTerminalSize, 500)
		return () => {
			process.stdout.off("resize", handleResize)
			clearInterval(interval)
		}
	}, [renderer])

	// ═══════════════════════════════════════════════════════════════════════════
	// Keyboard Handler - Delegates to KeyboardService
	// ═══════════════════════════════════════════════════════════════════════════

	useKeyboard((event) => {
		// Ctrl-C: Kill active editor popup (MUST be first - works in any state)
		if (event.ctrl && event.name === "c") {
			killActivePopup()
			return
		}

		// Direct quit fallback from normal mode.
		// This bypasses KeyboardService so quit still works if service dependencies
		// are degraded (for example, during board/runtime errors).
		if (
			shouldRequestShutdownFromDirectQuitFallback({
				key: event.name,
				ctrl: event.ctrl,
				meta: event.meta,
				shift: event.shift,
				modeTag: mode._tag,
				hasOverlay: currentOverlay !== undefined,
				inDrillDown: Boolean(drillDownEpicId),
			})
		) {
			requestShutdown()
			return
		}

		// Ctrl-L: Force redraw (classic Unix terminal refresh)
		// Useful when terminal resize corrupts the display
		if (event.ctrl && event.name === "l") {
			// Clear screen and move cursor to home position
			process.stdout.write("\x1b[2J\x1b[H")
			// Request a full re-render
			renderer.requestRender()
			return
		}

		// Create prompt handling - CreateTaskPrompt handles its own keyboard input
		if (showingCreate) {
			return
		}

		// AI create prompt handling - AICreatePrompt handles its own keyboard input
		if (showingAICreate) {
			return
		}

		// Note: imageAttach overlay keyboard is handled by KeyboardService

		// Build key sequence with modifiers (e.g., "C-d" for Ctrl+d, "S-c" for Shift+c, "CS-u" for Ctrl+Shift+u)
		let keySeq = event.name
		if (event.ctrl && event.shift) {
			keySeq = `CS-${event.name}`
		} else if (event.ctrl) {
			keySeq = `C-${event.name}`
		} else if (event.shift) {
			keySeq = `S-${event.name}`
		}

		// Delegate all keyboard handling to KeyboardService
		// KeyboardService handles: navigation, mode transitions, actions, overlays, escape, view toggle
		handleKey(keySeq)
	})

	// ═══════════════════════════════════════════════════════════════════════════
	// Mouse Handlers - First-pass task focus/open + wheel scroll
	// ═══════════════════════════════════════════════════════════════════════════

	const handleTaskMouseDown = (taskId: string, event: MouseEvent) => {
		// Left click = focus, right click = focus + open action menu
		if (event.button !== MouseButton.LEFT && event.button !== MouseButton.RIGHT) return

		event.preventDefault()
		event.stopPropagation()

		const button = event.button === MouseButton.RIGHT ? "right" : "left"
		void handleTaskMouseInteraction({
			taskId,
			button,
			selectedTaskId: selectedTask?.id,
		})
	}

	const handleColumnMouseScroll = (columnIdx: number, event: MouseEvent) => {
		if (currentOverlay) return
		const scroll = event.scroll
		if (!scroll) return
		if (scroll.direction !== "up" && scroll.direction !== "down") return

		event.preventDefault()
		event.stopPropagation()

		const columnTasks = tasksByColumn[columnIdx] ?? []
		if (columnTasks.length === 0) return

		const delta = Math.max(1, Math.trunc(Math.abs(scroll.delta)))
		const direction = scroll.direction === "down" ? 1 : -1
		const baseIndex = columnIdx === columnIndex ? taskIndex : 0
		const nextIndex = Math.max(0, Math.min(columnTasks.length - 1, baseIndex + delta * direction))

		void jumpTo({ column: columnIdx, task: nextIndex })
	}

	const handleColumnPagerMouseDown = (delta: -1 | 1, event: MouseEvent) => {
		if (event.button !== MouseButton.LEFT) return

		event.preventDefault()
		event.stopPropagation()

		void handleColumnPagerMouseInteraction({
			delta,
			columnIndex,
			taskIndex,
			tasksByColumn,
		})
	}

	const totalTasks = useAtomValue(totalTasksCountAtom)
	const activeSessions = useAtomValue(activeSessionsCountAtom)
	const detailTask =
		currentOverlay?._tag === "detail"
			? boardTasks.find((task) => task.id === currentOverlay.taskId)
			: undefined

	// Mode display text
	const modeDisplay = useMemo(() => {
		switch (mode._tag) {
			case "action":
				return "action"
			case "goto":
				if (mode.gotoSubMode === "pending") return "g..."
				if (mode.gotoSubMode === "jump")
					return mode.pendingJumpKey ? `g w ${mode.pendingJumpKey}_` : "g w ..."
				return "goto"
			case "normal":
				return searchQuery ? `filter: ${searchQuery}` : "normal"
			case "search":
				return "search"
			case "select":
				return `select (${selectedIds.length})`
			case "sort":
				return "sort"
			case "filter":
				return mode.activeField ? `filter: ${mode.activeField}` : "filter"
			case "orchestrate":
				return `orchestrate (${mode.selectedIds.length}/${mode.childTasks.length})`
			case "mergeSelect":
				return `merge ${mode.sourceIssueId} into...`
			case "spec":
				return `spec: ${mode.subview}`
		}
	}, [mode, searchQuery, selectedIds])

	// ═══════════════════════════════════════════════════════════════════════════
	// Render
	// ═══════════════════════════════════════════════════════════════════════════

	const renderContent = () => {
		if (mode._tag === "spec" && specEnabled) {
			return (
				<box flexGrow={1} flexDirection="column">
					<SpecWorkspace subview={mode.subview} state={specWorkspaceState} />
				</box>
			)
		}

		const activeColumn = COLUMNS[columnIndex] ?? COLUMNS[0]
		const canPageLeft = columnIndex > 0
		const canPageRight = columnIndex < COLUMNS.length - 1

		return (
			<box flexGrow={1} flexDirection="column">
				{boardRenderState._tag === "loading" && (
					<box paddingLeft={1} paddingBottom={1}>
						<text fg={theme.overlay1}>{"Loading board data..."}</text>
					</box>
				)}
				{boardRenderState._tag === "error" && (
					<box paddingLeft={1} paddingBottom={1}>
						<text fg={theme.red}>
							{`Board unavailable (${boardRenderState.reason.replaceAll("_", " ")})`}
						</text>
					</box>
				)}
				{/* Epic header when in drill-down mode */}
				{/* drillDownEpicId && epicInfo && <EpicHeader epic={epicInfo} epicChildren={epicChildren} /> */}

				{useSingleKanbanColumn && activeColumn && (
					<box
						flexDirection="row"
						justifyContent="space-between"
						paddingLeft={1}
						paddingRight={1}
						paddingBottom={1}
					>
						{/* biome-ignore lint/a11y/noStaticElementInteractions: OpenTUI uses <box> as the interactive mouse hit target. */}
						<box onMouseDown={(event) => handleColumnPagerMouseDown(-1, event)}>
							<text fg={canPageLeft ? theme.lavender : theme.overlay0}>{"← Prev"}</text>
						</box>
						<text fg={theme.text} attributes={ATTR_BOLD}>
							{`${activeColumn.title} (${columnIndex + 1}/${COLUMNS.length})`}
						</text>
						{/* biome-ignore lint/a11y/noStaticElementInteractions: OpenTUI uses <box> as the interactive mouse hit target. */}
						<box onMouseDown={(event) => handleColumnPagerMouseDown(1, event)}>
							<text fg={canPageRight ? theme.lavender : theme.overlay0}>{"Next →"}</text>
						</box>
					</box>
				)}

				<Board
					tasks={tasksByColumn.flat()}
					selectedTaskId={selectedTask?.id}
					activeColumnIndex={columnIndex}
					activeTaskIndex={taskIndex}
					selectedIds={new Set(selectedIds)}
					jumpLabels={isJump ? jumpLabels : null}
					pendingJumpKey={pendingJumpKey ?? null}
					// terminalHeight={drillDownEpicId ? maxVisibleTasks - 1 : maxVisibleTasks}
					terminalHeight={maxVisibleTasks}
					viewMode={viewMode}
					singleColumnMode={useSingleKanbanColumn}
					isActionMode={isAction}
					mergeSelectSourceId={mergeSelectSourceId}
					phases={phases}
					onTaskMouseDown={handleTaskMouseDown}
					onColumnMouseScroll={handleColumnMouseScroll}
				/>
			</box>
		)
	}

	return (
		<box flexDirection="column" width="100%" height="100%" backgroundColor={theme.base}>
			{renderContent()}

			{/* Status bar at bottom */}
			<StatusBar
				totalTasks={totalTasks}
				activeSessions={activeSessions}
				waitingIssueIds={waitingIssueIds}
				mode={mode._tag}
				modeDisplay={modeDisplay}
				selectedCount={selectedIds.length}
				// TODO: re-enable
				// vcStatus={vcStatus}
				viewMode={viewMode}
				isLoading={isLoading}
				isRefreshingGitStats={isRefreshingGitStats}
				devServerStatus={displayDevServer.status}
				devServerPort={displayDevServer.port}
				projectName={projectName}
			/>

			{/* Help overlay */}
			{showingHelp && <HelpOverlay />}

			{/* Settings overlay */}
			{showingSettings && <SettingsOverlay />}

			{showingProjectSelector && <ProjectSelector />}

			{showingWaitingSessionPicker && <WaitingSessionPicker />}

			{showingDevServerMenu && currentOverlay?._tag === "devServerMenu" && (
				<DevServerMenu issueId={currentOverlay.issueId} />
			)}

			{/* Diagnostics overlay */}
			{showingDiagnostics && <DiagnosticsOverlay />}

			{/* Diff viewer overlay */}
			{showingDiffViewer && currentOverlay?._tag === "diffViewer" && (
				<DiffViewer
					worktreePath={currentOverlay.worktreePath}
					baseBranch={currentOverlay.baseBranch}
					onClose={dismissOverlay}
				/>
			)}

			{/* Action palette */}
			{isAction && (
				<ActionPalette
					task={selectedTask}
					runningOperation={runningOperation}
					isOnline={isOnline}
					devServerStatus={displayDevServer.status}
					devServerPort={displayDevServer.port}
					workflowMode={workflowMode}
					drillDownEpicId={drillDownEpicId}
					tmuxCapabilities={tmuxCapabilities}
					compact={isSmallScreen(terminalColumns)}
					onActionSelect={(keySeq) => {
						void handleKey(keySeq)
					}}
				/>
			)}

			{/* Sort menu */}
			{isSort && <SortMenu currentSort={sortConfig} />}

			{/* Filter menu */}
			{isFilter && <FilterMenu config={filterConfig} activeField={activeFilterField} />}

			{/* Search input */}
			{isSearch && <SearchInput query={searchQuery} />}

			{/* Detail panel */}
			{showingDetail && detailTask && (
				<DetailPanel task={detailTask} forceSmallScreenLayout={isSmallScreen(terminalColumns)} />
			)}

			{/* Create task prompt */}
			{showingCreate && (
				<CreateTaskPrompt
					titleText={currentOverlay?._tag === "create" ? currentOverlay.title : undefined}
					initialTitle={
						currentOverlay?._tag === "create" ? currentOverlay.initial?.title : undefined
					}
					initialType={currentOverlay?._tag === "create" ? currentOverlay.initial?.type : undefined}
					initialPriority={
						currentOverlay?._tag === "create" ? currentOverlay.initial?.priority : undefined
					}
					lockType={currentOverlay?._tag === "create" ? currentOverlay.lockType : undefined}
					onSubmit={(params) => {
						const nextParams =
							currentOverlay?._tag === "create"
								? {
										...params,
										implementations: currentOverlay.initial?.implementations,
									}
								: params

						if (currentOverlay?._tag !== "create" || !currentOverlay.context) {
							createTask(nextParams)
							return
						}

						switch (currentOverlay.context._tag) {
							case "forkChild":
								forkCreateChild({
									parentEpicId: currentOverlay.context.parentEpicId,
									sourceTaskId: currentOverlay.context.sourceTaskId,
									params: nextParams,
								})
								return
							case "forkEpic":
								forkCreateEpic({
									sourceTaskId: currentOverlay.context.sourceTaskId,
									params: nextParams,
								})
								return
						}
					}}
					onCancel={() => dismissOverlay()}
				/>
			)}

			{/* Fork overlay */}
			{showingFork && <ForkOverlay />}

			{/* AI create prompt */}
			{showingAICreate && (
				<AICreatePrompt
					onSubmit={(description) => {
						aiCreateTask(description)
					}}
					onCancel={() => dismissOverlay()}
				/>
			)}

			{/* Image attach overlay */}
			{showingImageAttach && <ImageAttachOverlay />}

			{/* Image preview overlay */}
			{showingImagePreview && <ImagePreviewOverlay />}

			{/* Confirm overlay */}
			{showingConfirm && <ConfirmOverlay />}

			{/* Git pull notification overlay */}
			{showingGitPull && <GitPullOverlay />}

			{/* Merge choice overlay */}
			{showingMergeChoice && <MergeChoiceOverlay />}

			{/* Bulk cleanup overlay */}
			{showingBulkCleanup && <BulkCleanupOverlay />}

			{/* Planning overlay */}
			{showingPlanning && <PlanningOverlay onClose={dismissOverlay} />}

			{/* Orchestration overlay - rendered when in orchestrate mode */}
			{isOrchestrate && mode._tag === "orchestrate" && (
				<OrchestrationOverlay
					epicId={mode.epicId}
					epicTitle={mode.epicTitle}
					childTasks={mode.childTasks}
					selectedIds={mode.selectedIds}
					focusIndex={mode.focusIndex}
				/>
			)}

			{/* Toast notifications */}
			<ToastContainer toasts={toasts} onDismiss={dismissToast} />
		</box>
	)
}

const ATTR_BOLD = 1

const STARTUP_HYDRATION_DELAY_MS = 0
const DEFAULT_TTFP_BUDGET_MS = 120
const STARTUP_METRIC_PREFIX = "[startup-metric]"

const readStartupBudgetMs = (): number => {
	const raw = process.env.AZ_TUI_TTFP_BUDGET_MS
	if (raw === undefined) return DEFAULT_TTFP_BUDGET_MS
	const parsed = Number.parseInt(raw, 10)
	return Number.isFinite(parsed) && parsed > 0 ? parsed : DEFAULT_TTFP_BUDGET_MS
}

const maybeLogStartupMetric = (name: "ttfp_ms" | "hydrated_ms", value: number) => {
	if (process.env.AZ_STARTUP_METRICS === "1") {
		console.error(`${STARTUP_METRIC_PREFIX} ${JSON.stringify({ metric: name, value })}`)
	}
}

const StartupPlaceholder = () => (
	<box flexDirection="column" width="100%" height="100%" backgroundColor={theme.base}>
		<box flexGrow={1} justifyContent="center" alignItems="center">
			<text fg={theme.overlay1}>{"Bootstrapping board..."}</text>
		</box>
		<box
			border={true}
			borderColor={theme.surface1}
			paddingLeft={1}
			paddingRight={1}
			paddingTop={0}
			paddingBottom={0}
			height={1}
		>
			<text fg={theme.subtext0}>{"mode: startup"}</text>
		</box>
	</box>
)

type AppProps = {
	readonly launchStartedAtMs?: number
	readonly ttfpBudgetMs?: number
}

export const App = ({ launchStartedAtMs, ttfpBudgetMs = readStartupBudgetMs() }: AppProps) => {
	const [hydrated, setHydrated] = useState(false)
	const firstPaintLoggedRef = useRef(false)

	useEffect(() => {
		if (firstPaintLoggedRef.current) return
		firstPaintLoggedRef.current = true

		if (launchStartedAtMs !== undefined) {
			const ttfpMs = Date.now() - launchStartedAtMs
			maybeLogStartupMetric("ttfp_ms", ttfpMs)
			if (ttfpMs > ttfpBudgetMs) {
				const message = `[startup] ttfp budget exceeded: ${ttfpMs}ms > ${ttfpBudgetMs}ms`
				console.error(message)
				if (process.env.AZ_TUI_STRICT_STARTUP_BUDGET === "1") {
					// CI mode can opt in to a hard startup budget failure signal.
					queueMicrotask(() => {
						throw new Error(message)
					})
				}
			}
		}

		const hydrateTimer = setTimeout(() => {
			setHydrated(true)
		}, STARTUP_HYDRATION_DELAY_MS)

		return () => {
			clearTimeout(hydrateTimer)
		}
	}, [launchStartedAtMs, ttfpBudgetMs])

	useEffect(() => {
		if (!hydrated || launchStartedAtMs === undefined) return
		const hydratedMs = Date.now() - launchStartedAtMs
		maybeLogStartupMetric("hydrated_ms", hydratedMs)
	}, [hydrated, launchStartedAtMs])

	if (!hydrated) {
		return <StartupPlaceholder />
	}

	return <HydratedApp />
}
