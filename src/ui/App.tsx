/**
 * App component - root component with Helix-style modal keybindings
 *
 * Migrated to use atomic Effect services via custom hooks.
 */

import { Result } from "@effect-atom/atom"
import { useAtomSet, useAtomValue } from "@effect-atom/atom-react"
import { useKeyboard, useRenderer } from "@opentui/react"
import { useEffect, useMemo } from "react"
import { killActivePopup } from "../core/BeadEditorService.js"
import { ActionPalette } from "./ActionPalette.js"
import {
	activeSessionsCountAtom,
	boardIsLoadingAtom,
	claudeCreateSessionAtom,
	createTaskAtom,
	currentProjectAtom,
	drillDownEpicAtom,
	drillDownFilteredTasksAtom,
	drillDownPhasesAtom,
	focusedBeadPrimaryDevServerAtom,
	focusedTaskRunningOperationAtom,
	handleKeyAtom,
	isOnlineAtom,
	isRefreshingGitStatsAtom,
	maxVisibleTasksAtom,
	sessionMonitorStarterAtom,
	totalTasksCountAtom,
	viewModeAtom,
	workflowModeAtom,
} from "./atoms.js"
import { Board } from "./Board.js"
import { BulkCleanupOverlay } from "./BulkCleanupOverlay.js"
import { ClaudeCreatePrompt } from "./ClaudeCreatePrompt.js"
import { ConfirmOverlay } from "./ConfirmOverlay.js"
import { CreateTaskPrompt } from "./CreateTaskPrompt.js"
import { DetailPanel } from "./DetailPanel.js"
import { DevServerMenu } from "./DevServerMenu.js"
import { DiagnosticsOverlay } from "./DiagnosticsOverlay.js"
import { DiffViewer } from "./DiffViewer/index.js"
import { FilterMenu } from "./FilterMenu.js"
import { HelpOverlay } from "./HelpOverlay.js"
import { useEditorMode, useNavigation, useOverlays, useToasts } from "./hooks/index.js"
import { ImageAttachOverlay } from "./ImageAttachOverlay.js"
import { ImagePreviewOverlay } from "./ImagePreviewOverlay.js"
import { MergeChoiceOverlay } from "./MergeChoiceOverlay.js"
import { OrchestrationOverlay } from "./OrchestrationOverlay.js"
import { PlanningOverlay } from "./PlanningOverlay.js"
import { ProjectSelector } from "./ProjectSelector.js"
import { SearchInput } from "./SearchInput.js"
import { SettingsOverlay } from "./SettingsOverlay.js"
import { SortMenu } from "./SortMenu.js"
import { StatusBar } from "./StatusBar.js"
import { ToastContainer } from "./Toast.js"
import { theme } from "./theme.js"

// ============================================================================
// App Component
// ============================================================================

export const App = () => {
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
		showingClaudeCreate,
		showingSettings,
		showingImageAttach,
		showingImagePreview,
		showingConfirm,
		showingMergeChoice,
		showingBulkCleanup,
		showingDiagnostics,
		showingProjectSelector,
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
	const tasksByColumn = useAtomValue(drillDownFilteredTasksAtom)

	const projectName = useAtomValue(
		currentProjectAtom,
		Result.getOrElse(() => undefined),
	)?.name

	const handleKey = useAtomSet(handleKeyAtom, { mode: "promise" })

	const startSessionMonitor = useAtomSet(sessionMonitorStarterAtom, { mode: "promise" })
	useEffect(() => {
		startSessionMonitor()
	}, [startSessionMonitor])

	// Actions for prompts (these bypass keyboard handling)
	// Full orchestration (dismiss, create, navigate, toast) happens in the atoms
	const createTask = useAtomSet(createTaskAtom, { mode: "promise" })
	const claudeCreateSession = useAtomSet(claudeCreateSessionAtom, { mode: "promise" })

	const viewMode = useAtomValue(
		viewModeAtom,
		Result.getOrElse(() => "kanban" as const),
	)

	const displayDevServer = useAtomValue(focusedBeadPrimaryDevServerAtom)

	const runningOperation = useAtomValue(focusedTaskRunningOperationAtom)

	const isOnline = useAtomValue(
		isOnlineAtom,
		Result.getOrElse(() => true),
	)

	const workflowMode = useAtomValue(workflowModeAtom)

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

	// Terminal size
	const maxVisibleTasks = useAtomValue(maxVisibleTasksAtom)

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

	// Renderer access for manual redraw
	const renderer = useRenderer()

	useEffect(() => {
		const handleResize = () => {
			renderer.requestRender()
		}
		process.stdout.on("resize", handleResize)
		return () => {
			process.stdout.off("resize", handleResize)
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

		// Claude create prompt handling - ClaudeCreatePrompt handles its own keyboard input
		if (showingClaudeCreate) {
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

	const totalTasks = useAtomValue(totalTasksCountAtom)
	const activeSessions = useAtomValue(activeSessionsCountAtom)

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
				return `merge ${mode.sourceBeadId} into...`
		}
	}, [mode, searchQuery, selectedIds])

	// ═══════════════════════════════════════════════════════════════════════════
	// Render
	// ═══════════════════════════════════════════════════════════════════════════

	const renderContent = () => {
		// The derived atom returns an empty array if sources are loading/failed
		// This is handled gracefully - the board just shows empty columns

		return (
			<box flexGrow={1} flexDirection="column">
				{/* Epic header when in drill-down mode */}
				{/* drillDownEpicId && epicInfo && <EpicHeader epic={epicInfo} epicChildren={epicChildren} /> */}

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
					isActionMode={isAction}
					mergeSelectSourceId={mergeSelectSourceId}
					phases={phases}
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

			{showingDevServerMenu && currentOverlay?._tag === "devServerMenu" && (
				<DevServerMenu beadId={currentOverlay.beadId} />
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
				/>
			)}

			{/* Sort menu */}
			{isSort && <SortMenu currentSort={sortConfig} />}

			{/* Filter menu */}
			{isFilter && <FilterMenu config={filterConfig} activeField={activeFilterField} />}

			{/* Search input */}
			{isSearch && <SearchInput query={searchQuery} />}

			{/* Detail panel */}
			{showingDetail && selectedTask && <DetailPanel task={selectedTask} />}

			{/* Create task prompt */}
			{showingCreate && (
				<CreateTaskPrompt
					onSubmit={(params) => {
						createTask(params)
					}}
					onCancel={() => dismissOverlay()}
				/>
			)}

			{/* Claude create prompt */}
			{showingClaudeCreate && (
				<ClaudeCreatePrompt
					onSubmit={(description) => {
						claudeCreateSession(description)
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
