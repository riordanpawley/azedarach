/**
 * Atoms for Azedarach UI state
 *
 * Uses effect-atom for reactive state management with Effect integration.
 */

import { BunContext } from "@effect/platform-bun"
import { Atom, Result } from "@effect-atom/atom"
import { Effect, Layer, pipe, Schedule, Stream, SubscriptionRef } from "effect"
import { ModeService } from "../atoms/runtime"
import { AppConfig } from "../config/index"
import { AttachmentService } from "../core/AttachmentService"
import { BeadsClient } from "../core/BeadsClient"
import { BeadEditorService } from "../core/EditorService"
import { PRWorkflow } from "../core/PRWorkflow"
import { SessionManager } from "../core/SessionManager"
import { TerminalService } from "../core/TerminalService"
import { TmuxService } from "../core/TmuxService"
import { type VCExecutorInfo, VCService } from "../core/VCService"
import { BoardService } from "../services/BoardService"
import type { SortField } from "../services/EditorService"
import { EditorService } from "../services/EditorService"
import { KeyboardService } from "../services/KeyboardService"
import { NavigationService } from "../services/NavigationService"
import { OverlayService } from "../services/OverlayService"
import { SessionService } from "../services/SessionService"
// New atomic Effect services
import { ToastService } from "../services/ToastService"
import type { TaskWithSession, ViewMode } from "./types"

const platformLayer = BunContext.layer

const appLayer = Layer.mergeAll(
	SessionService.Default,
	AttachmentService.Default,
	BoardService.Default,
	TmuxService.Default,
	BeadEditorService.Default,
	ModeService.Default,
	PRWorkflow.Default,
	TerminalService.Default,
	EditorService.Default,
	KeyboardService.Default,
	OverlayService.Default,
	ToastService.Default,
	KeyboardService.Default,
	NavigationService.Default,
	SessionService.Default,
	SessionManager.Default,
	BeadsClient.Default,
	AppConfig.Default,
	VCService.Default,
).pipe(Layer.provideMerge(platformLayer))

/**
 * Runtime atom that provides all services and platform dependencies
 *
 * This creates a runtime that all other async atoms can use.
 */
export const appRuntime = Atom.runtime(appLayer)

/**
 * Async atom that fetches all tasks from BeadsClient
 *
 * Uses the appRuntime to access BeadsClient service.
 * Returns Result.Result<TaskWithSession[], Error> for proper loading/error states.
 *
 * Note: Fetches ALL issues (not just ready) so we can display the full kanban board.
 * Merges session state from SessionManager for tasks with active sessions.
 */
export const tasksAtom = appRuntime.atom(
	Effect.gen(function* () {
		const client = yield* BeadsClient
		const sessionManager = yield* SessionManager

		// Fetch all issues (no status filter) to populate the full board
		const issues = yield* client.list()

		// Get active sessions to merge their state
		const activeSessions = yield* sessionManager.listActive()
		const sessionStateMap = new Map(
			activeSessions.map((session) => [session.beadId, session.state]),
		)

		// Map issues to TaskWithSession, using real session state if available
		const tasks: TaskWithSession[] = issues.map((issue) => ({
			...issue,
			sessionState: sessionStateMap.get(issue.id) ?? ("idle" as const),
		}))

		return tasks
	}),
	{ initialValue: [] },
)

// ============================================================================
// VC Status Atoms (scoped polling with SubscriptionRef)
// ============================================================================

const VC_STATUS_INITIAL: VCExecutorInfo = {
	status: "stopped",
	sessionName: "vc-autopilot",
}

/**
 * SubscriptionRef that holds the current VC status
 *
 * This is the single source of truth for VC status.
 * Updated by both the poller and toggle actions.
 */
export const vcStatusRefAtom = appRuntime.atom(
	SubscriptionRef.make<VCExecutorInfo>(VC_STATUS_INITIAL),
	{ initialValue: undefined },
)

/**
 * Scoped poller that updates vcStatusRefAtom every 5 seconds
 *
 * The polling fiber is automatically interrupted when the atom unmounts
 * because effect-atom provides the Scope.
 */
export const vcStatusPollerAtom = appRuntime.atom(
	(get) =>
		Effect.gen(function* () {
			const vc = yield* VCService
			const ref = yield* get.result(vcStatusRefAtom)

			// Get initial status immediately
			const initial = yield* vc.getStatus()
			yield* SubscriptionRef.set(ref, initial)

			// Fork polling loop - scoped by effect-atom, auto-interrupted on unmount
			yield* Effect.scheduleForked(Schedule.spaced("5 seconds"))(
				vc.getStatus().pipe(
					Effect.flatMap((status) => SubscriptionRef.set(ref, status)),
					Effect.catchAll(() => Effect.void), // Don't crash on transient errors
				),
			)
		}),
	{ initialValue: undefined },
)

/**
 * Read-only atom that subscribes to VC status changes
 *
 * Streams the SubscriptionRef's changes so UI updates reactively.
 *
 * Usage: const vcStatus = useAtomValue(vcStatusAtom)
 */
export const vcStatusAtom = appRuntime.atom(
	(get) =>
		pipe(
			get.result(vcStatusRefAtom),
			Effect.map((_) => _.changes),
			Stream.unwrap,
		),
	{ initialValue: VC_STATUS_INITIAL },
)

/**
 * Atom for currently selected task ID
 */
export const selectedTaskIdAtom = Atom.make<string | undefined>(undefined)

/**
 * Atom for UI error state
 */
export const errorAtom = Atom.make<string | undefined>(undefined)

/**
 * Atom for board view mode (kanban vs compact)
 *
 * - kanban: Traditional column-based view with task cards
 * - compact: Linear list view with minimal row height
 *
 * Usage: const [viewMode, setViewMode] = useAtom(viewModeAtom)
 */
export const viewModeAtom = Atom.make<ViewMode>("kanban")

// ============================================================================
// Action Atoms (using runtime.fn for proper effect-atom integration)
// ============================================================================

/**
 * Move a task to a new status
 *
 * Usage: const moveTask = useAtomSet(moveTaskAtom, { mode: "promise" })
 *        await moveTask({ taskId: "az-123", newStatus: "in_progress" })
 */
export const moveTaskAtom = appRuntime.fn(
	({ taskId, newStatus }: { taskId: string; newStatus: string }) =>
		Effect.gen(function* () {
			const client = yield* BeadsClient
			yield* client.update(taskId, { status: newStatus })
		}).pipe(Effect.catchAll(Effect.logError)),
)

/**
 * Move multiple tasks at once
 */
export const moveTasksAtom = appRuntime.fn(
	({ taskIds, newStatus }: { taskIds: string[]; newStatus: string }) =>
		Effect.gen(function* () {
			const client = yield* BeadsClient
			yield* Effect.all(
				taskIds.map((id) => client.update(id, { status: newStatus })),
				{ concurrency: "unbounded" },
			)
		}).pipe(Effect.catchAll(Effect.logError)),
)

/**
 * Attach to a session externally (opens new terminal window)
 *
 * Usage: const attachExternal = useAtomSet(attachExternalAtom, { mode: "promise" })
 *        await attachExternal(sessionId)
 */
export const attachExternalAtom = appRuntime.fn((sessionId: string) =>
	Effect.gen(function* () {
		const service = yield* AttachmentService
		yield* service.attachExternal(sessionId)
	}).pipe(Effect.catchAll(Effect.logError)),
)

/**
 * Attach to a session inline (future: replaces TUI)
 */
export const attachInlineAtom = appRuntime.fn((sessionId: string) =>
	Effect.gen(function* () {
		const service = yield* AttachmentService
		yield* service.attachInline(sessionId)
	}).pipe(Effect.catchAll(Effect.logError)),
)

/**
 * Start a Claude session (creates worktree + tmux + launches Claude)
 *
 * Usage: const startSession = useAtomSet(startSessionAtom, { mode: "promise" })
 *        await startSession(beadId)
 */
export const startSessionAtom = appRuntime.fn((beadId: string) =>
	Effect.gen(function* () {
		const manager = yield* SessionManager
		yield* manager.start({
			beadId,
			projectPath: process.cwd(),
		})
	}).pipe(Effect.catchAll(Effect.logError)),
)

/**
 * Pause a running session (Ctrl+C + WIP commit)
 */
export const pauseSessionAtom = appRuntime.fn((beadId: string) =>
	Effect.gen(function* () {
		const manager = yield* SessionManager
		yield* manager.pause(beadId)
	}).pipe(Effect.catchAll(Effect.logError)),
)

/**
 * Resume a paused session
 */
export const resumeSessionAtom = appRuntime.fn((beadId: string) =>
	Effect.gen(function* () {
		const manager = yield* SessionManager
		yield* manager.resume(beadId)
	}).pipe(Effect.catchAll(Effect.logError)),
)

/**
 * Stop a running session (kills tmux, marks as idle)
 */
export const stopSessionAtom = appRuntime.fn((beadId: string) =>
	Effect.gen(function* () {
		const manager = yield* SessionManager
		yield* manager.stop(beadId)
	}).pipe(Effect.catchAll(Effect.logError)),
)

/**
 * Create a new task
 *
 * Usage: const createTask = useAtomSet(createTaskAtom, { mode: "promise" })
 *        await createTask({ title: "New task", type: "task", priority: 2 })
 */
export const createTaskAtom = appRuntime.fn(
	(params: { title: string; type?: string; priority?: number; description?: string }) =>
		Effect.gen(function* () {
			const client = yield* BeadsClient
			return yield* client.create(params)
		}).pipe(Effect.tapError(Effect.logError)),
)

/**
 * Edit a bead in $EDITOR
 *
 * Opens the bead in $EDITOR as structured markdown, parses changes on save,
 * and applies updates via bd update.
 *
 * Usage: const editBead = useAtomSet(editBeadAtom, { mode: "promise" })
 *        await editBead(task)
 */
export const editBeadAtom = appRuntime.fn((bead: TaskWithSession) =>
	Effect.gen(function* () {
		const editor = yield* BeadEditorService
		yield* editor.editBead(bead)
	}).pipe(Effect.catchAll(Effect.logError)),
)

/**
 * Create a new bead via $EDITOR
 *
 * Opens a template in $EDITOR, parses the result, and creates a new bead.
 *
 * Usage: const createBead = useAtom(createBeadViaEditorAtom, { mode: "promise" })
 *        const { id, title } = await createBead()
 */
export const createBeadViaEditorAtom = appRuntime.fn(() =>
	Effect.gen(function* () {
		const editor = yield* BeadEditorService
		return yield* editor.createBead()
	}).pipe(Effect.catchAll(Effect.logError)),
)

/**
 * Create a Claude session to generate a bead from natural language
 *
 * Spawns a tmux session with Claude in the main project directory,
 * sends a prompt asking Claude to create a bead from the description,
 * and leaves the session open for the user to continue working.
 *
 * Returns the session name so the UI can inform the user.
 *
 * Usage: const claudeCreate = useAtom(claudeCreateSessionAtom, { mode: "promise" })
 *        const sessionName = await claudeCreate("Add dark mode toggle to settings")
 */
export const claudeCreateSessionAtom = appRuntime.fn((description: string) =>
	Effect.gen(function* () {
		const tmux = yield* TmuxService
		const { config } = yield* AppConfig

		// Generate unique session name with timestamp
		const timestamp = Date.now().toString(36)
		const sessionName = `claude-create-${timestamp}`
		const projectPath = process.cwd()

		// Build the Claude command with session settings
		const { command: claudeCommand, shell, tmuxPrefix, dangerouslySkipPermissions } = config.session
		const claudeArgs = dangerouslySkipPermissions ? " --dangerously-skip-permissions" : ""

		// Create the tmux session
		yield* tmux.newSession(sessionName, {
			cwd: projectPath,
			command: `${shell} -c '${claudeCommand}${claudeArgs}; exec ${shell}'`,
			prefix: tmuxPrefix,
		})

		// Wait briefly for Claude to start up
		yield* Effect.sleep("1500 millis")

		// Build the prompt for Claude with explicit bd create instructions
		const prompt = `Create a new bead issue for the following task using the \`bd\` CLI tool.

**Task description:**
${description}

**Instructions:**
1. Use \`bd create --title="..." --type=task|bug|feature --priority=1|2|3\`
2. Add a description if the task needs more detail
3. The issue ID will be returned - note it for the user
4. After creating, you may start working on it or wait for further instructions

Please create the bead now.`

		// Send the prompt to Claude via tmux
		yield* tmux.sendKeys(sessionName, prompt)
		yield* tmux.sendKeys(sessionName, "Enter")

		return sessionName
	}).pipe(Effect.tapError(Effect.logError)),
)

// ============================================================================
// PR Workflow Atoms
// ============================================================================

/**
 * Create a PR for a bead's worktree branch
 *
 * Usage: const createPR = useAtomSet(createPRAtom, { mode: "promise" })
 *        const pr = await createPR(beadId)
 */
export const createPRAtom = appRuntime.fn((beadId: string) =>
	Effect.gen(function* () {
		const prWorkflow = yield* PRWorkflow
		return yield* prWorkflow.createPR({
			beadId,
			projectPath: process.cwd(),
		})
	}).pipe(Effect.tapError(Effect.logError)),
)

/**
 * Cleanup worktree and branches after PR merge or abandonment
 *
 * Usage: const cleanup = useAtomSet(cleanupAtom, { mode: "promise" })
 *        await cleanup(beadId)
 */
export const cleanupAtom = appRuntime.fn((beadId: string) =>
	Effect.gen(function* () {
		const prWorkflow = yield* PRWorkflow
		yield* prWorkflow.cleanup({
			beadId,
			projectPath: process.cwd(),
		})
	}).pipe(Effect.catchAll(Effect.logError)),
)

/**
 * Delete a bead entirely
 *
 * Usage: const deleteBead = useAtom(deleteBeadAtom, { mode: "promise" })
 *        await deleteBead(beadId)
 */
export const deleteBeadAtom = appRuntime.fn((beadId: string) =>
	Effect.gen(function* () {
		const client = yield* BeadsClient
		yield* client.delete(beadId)
	}).pipe(Effect.catchAll(Effect.logError)),
)

/**
 * Check if gh CLI is available and authenticated
 *
 * Usage: const ghAvailable = useAtomValue(ghCLIAvailableAtom)
 */
export const ghCLIAvailableAtom = appRuntime.atom(
	Effect.gen(function* () {
		const prWorkflow = yield* PRWorkflow
		return yield* prWorkflow.checkGHCLI()
	}),
	{ initialValue: false },
)

// ============================================================================
// VC Auto-Pilot Action Atoms
// ============================================================================

/**
 * Toggle VC auto-pilot mode
 *
 * If running, stops it. If stopped, starts it.
 * Updates the vcStatusRefAtom immediately so UI reflects the change.
 *
 * Usage: const [, toggleVCAutoPilot] = useAtom(toggleVCAutoPilotAtom, { mode: "promise" })
 *        await toggleVCAutoPilot()
 */
export const toggleVCAutoPilotAtom = appRuntime.fn((_: undefined, get) =>
	Effect.gen(function* () {
		const vc = yield* VCService
		const newStatus = yield* vc.toggleAutoPilot()

		// Update the ref immediately so UI reflects the change
		const ref = yield* get.result(vcStatusRefAtom)
		yield* SubscriptionRef.set(ref, newStatus)

		return newStatus
	}).pipe(Effect.catchAll(Effect.logError)),
)

/**
 * Send a command to the VC REPL
 *
 * Usage: const sendVCCommand = useAtom(sendVCCommandAtom, { mode: "promise" })
 *        await sendVCCommand("What's ready to work on?")
 */
export const sendVCCommandAtom = appRuntime.fn((command: string) =>
	Effect.gen(function* () {
		const vcService = yield* VCService
		yield* vcService.sendCommand(command)
	}).pipe(Effect.catchAll(Effect.logError)),
)

// ============================================================================
// Atomic Service State Atoms (ModeService, NavigationService, etc.)
// ============================================================================

/**
 * Editor mode atom - subscribes to ModeService mode changes
 *
 * Uses appRuntime.subscriptionRef() for automatic reactive updates.
 *
 * Usage: const mode = useAtomValue(modeAtom)
 */
export const modeAtom = appRuntime.subscriptionRef(
	Effect.gen(function* () {
		const editor = yield* ModeService
		return editor.mode
	}),
)

/**
 * Selected task IDs atom - derived from modeAtom
 *
 * Usage: const selectedIds = useAtomValue(selectedIdsAtom)
 */
export const selectedIdsAtom = Atom.readable((get) => {
	const modeResult = get(modeAtom)
	if (!Result.isSuccess(modeResult)) return []
	const mode = modeResult.value
	return mode._tag === "select" ? mode.selectedIds : []
})

/**
 * Search query atom - derived from modeAtom
 *
 * Usage: const searchQuery = useAtomValue(searchQueryAtom)
 */
export const searchQueryAtom = Atom.readable((get) => {
	const modeResult = get(modeAtom)
	if (!Result.isSuccess(modeResult)) return ""
	const mode = modeResult.value
	return mode._tag === "search" ? mode.query : ""
})

/**
 * Command input atom - derived from modeAtom
 *
 * Usage: const commandInput = useAtomValue(commandInputAtom)
 */
export const commandInputAtom = Atom.readable((get) => {
	const modeResult = get(modeAtom)
	if (!Result.isSuccess(modeResult)) return ""
	const mode = modeResult.value
	return mode._tag === "command" ? mode.input : ""
})

/**
 * Sort configuration atom - subscribes to ModeService sortConfig changes
 *
 * Usage: const sortConfig = useAtomValue(sortConfigAtom)
 */
export const sortConfigAtom = appRuntime.subscriptionRef(
	Effect.gen(function* () {
		const editor = yield* ModeService
		return editor.sortConfig
	}),
)

/**
 * Navigation cursor atom - subscribes to NavigationService cursor changes
 *
 * Uses appRuntime.subscriptionRef() for automatic reactive updates.
 *
 * Usage: const cursor = useAtomValue(cursorAtom)
 */
export const cursorAtom = appRuntime.subscriptionRef(
	Effect.gen(function* () {
		const nav = yield* NavigationService
		return nav.cursor
	}),
)

/**
 * Toast notifications atom - subscribes to ToastService toasts changes
 *
 * Uses appRuntime.subscriptionRef() for automatic reactive updates.
 *
 * Usage: const toasts = useAtomValue(toastsAtom)
 */
export const toastsAtom = appRuntime.subscriptionRef(
	Effect.gen(function* () {
		const toast = yield* ToastService
		return toast.toasts
	}),
)

/**
 * Overlay stack atom - subscribes to OverlayService stack changes
 *
 * Uses appRuntime.subscriptionRef() for automatic reactive updates.
 *
 * Usage: const overlays = useAtomValue(overlaysAtom)
 */
export const overlaysAtom = appRuntime.subscriptionRef(
	Effect.gen(function* () {
		const overlay = yield* OverlayService
		return overlay.stack
	}),
)

/**
 * Board tasks atom - subscribes to BoardService tasks changes
 *
 * Uses appRuntime.subscriptionRef() for automatic reactive updates.
 *
 * Usage: const tasks = useAtomValue(boardTasksAtom)
 */
export const boardTasksAtom = appRuntime.subscriptionRef(
	Effect.gen(function* () {
		const board = yield* BoardService
		return board.tasks
	}),
)

/**
 * Board tasks by column atom - subscribes to BoardService tasksByColumn changes
 *
 * Uses appRuntime.subscriptionRef() for automatic reactive updates.
 *
 * Usage: const tasksByColumn = useAtomValue(boardTasksByColumnAtom)
 */
export const boardTasksByColumnAtom = appRuntime.subscriptionRef(
	Effect.gen(function* () {
		const board = yield* BoardService
		return board.tasksByColumn
	}),
)

/**
 * Current overlay atom - the top of the overlay stack
 *
 * Derived from overlaysAtom for automatic reactivity.
 *
 * Usage: const currentOverlay = useAtomValue(currentOverlayAtom)
 */
export const currentOverlayAtom = Atom.readable((get) => {
	const overlaysResult = get(overlaysAtom)
	if (!Result.isSuccess(overlaysResult)) return undefined
	const overlays = overlaysResult.value
	return overlays.length > 0 ? overlays[overlays.length - 1] : undefined
})

// ============================================================================
// Atomic Service Action Atoms
// ============================================================================

// --- ModeService Actions ---

/**
 * Enter select mode
 *
 * Usage: const [, enterSelect] = useAtom(enterSelectAtom, { mode: "promise" })
 *        await enterSelect()
 */
export const enterSelectAtom = appRuntime.fn(() =>
	Effect.gen(function* () {
		const editor = yield* ModeService
		yield* editor.enterSelect()
	}).pipe(Effect.catchAll(Effect.logError)),
)

/**
 * Exit select mode
 *
 * Usage: const [, exitSelect] = useAtom(exitSelectAtom, { mode: "promise" })
 *        await exitSelect(true) // clearSelections
 */
export const exitSelectAtom = appRuntime.fn((clearSelections: boolean | undefined) =>
	Effect.gen(function* () {
		const editor = yield* ModeService
		yield* editor.exitSelect(clearSelections ?? false)
	}).pipe(Effect.catchAll(Effect.logError)),
)

/**
 * Toggle selection of a task
 *
 * Usage: const [, toggleSelection] = useAtom(toggleSelectionAtom, { mode: "promise" })
 *        await toggleSelection(taskId)
 */
export const toggleSelectionAtom = appRuntime.fn((taskId: string) =>
	Effect.gen(function* () {
		const editor = yield* ModeService
		yield* editor.toggleSelection(taskId)
	}).pipe(Effect.catchAll(Effect.logError)),
)

/**
 * Enter goto mode
 *
 * Usage: const [, enterGoto] = useAtom(enterGotoAtom, { mode: "promise" })
 *        await enterGoto()
 */
export const enterGotoAtom = appRuntime.fn(() =>
	Effect.gen(function* () {
		const editor = yield* ModeService
		yield* editor.enterGoto()
	}).pipe(Effect.catchAll(Effect.logError)),
)

/**
 * Enter jump mode with labels
 *
 * Usage: const [, enterJump] = useAtom(enterJumpAtom, { mode: "promise" })
 *        await enterJump(labelsMap)
 */
export const enterJumpAtom = appRuntime.fn(
	(labels: Map<string, { taskId: string; columnIndex: number; taskIndex: number }>) =>
		Effect.gen(function* () {
			const editor = yield* ModeService
			yield* editor.enterJump(labels)
		}).pipe(Effect.catchAll(Effect.logError)),
)

/**
 * Set pending jump key
 *
 * Usage: const [, setPendingJumpKey] = useAtom(setPendingJumpKeyAtom, { mode: "promise" })
 *        await setPendingJumpKey("a")
 */
export const setPendingJumpKeyAtom = appRuntime.fn((key: string) =>
	Effect.gen(function* () {
		const editor = yield* ModeService
		yield* editor.setPendingJumpKey(key)
	}).pipe(Effect.catchAll(Effect.logError)),
)

/**
 * Enter action mode
 *
 * Usage: const [, enterAction] = useAtom(enterActionAtom, { mode: "promise" })
 *        await enterAction()
 */
export const enterActionAtom = appRuntime.fn(() =>
	Effect.gen(function* () {
		const editor = yield* ModeService
		yield* editor.enterAction()
	}).pipe(Effect.catchAll(Effect.logError)),
)

/**
 * Enter search mode
 *
 * Usage: const [, enterSearch] = useAtom(enterSearchAtom, { mode: "promise" })
 *        await enterSearch()
 */
export const enterSearchAtom = appRuntime.fn(() =>
	Effect.gen(function* () {
		const editor = yield* ModeService
		yield* editor.enterSearch()
	}).pipe(Effect.catchAll(Effect.logError)),
)

/**
 * Update search query
 *
 * Usage: const [, updateSearch] = useAtom(updateSearchAtom, { mode: "promise" })
 *        await updateSearch("new query")
 */
export const updateSearchAtom = appRuntime.fn((query: string) =>
	Effect.gen(function* () {
		const editor = yield* ModeService
		yield* editor.updateSearch(query)
	}).pipe(Effect.catchAll(Effect.logError)),
)

/**
 * Clear search and return to normal mode
 *
 * Usage: const [, clearSearch] = useAtom(clearSearchAtom, { mode: "promise" })
 *        await clearSearch()
 */
export const clearSearchAtom = appRuntime.fn(() =>
	Effect.gen(function* () {
		const editor = yield* ModeService
		yield* editor.clearSearch()
	}).pipe(Effect.catchAll(Effect.logError)),
)

/**
 * Enter command mode
 *
 * Usage: const [, enterCommand] = useAtom(enterCommandAtom, { mode: "promise" })
 *        await enterCommand()
 */
export const enterCommandAtom = appRuntime.fn(() =>
	Effect.gen(function* () {
		const editor = yield* ModeService
		yield* editor.enterCommand()
	}).pipe(Effect.catchAll(Effect.logError)),
)

/**
 * Update command input
 *
 * Usage: const [, updateCommand] = useAtom(updateCommandAtom, { mode: "promise" })
 *        await updateCommand("new command")
 */
export const updateCommandAtom = appRuntime.fn((input: string) =>
	Effect.gen(function* () {
		const editor = yield* ModeService
		yield* editor.updateCommand(input)
	}).pipe(Effect.catchAll(Effect.logError)),
)

/**
 * Clear command and return to normal mode
 *
 * Usage: const [, clearCommand] = useAtom(clearCommandAtom, { mode: "promise" })
 *        await clearCommand()
 */
export const clearCommandAtom = appRuntime.fn(() =>
	Effect.gen(function* () {
		const editor = yield* ModeService
		yield* editor.clearCommand()
	}).pipe(Effect.catchAll(Effect.logError)),
)

/**
 * Exit to normal mode
 *
 * Usage: const [, exitToNormal] = useAtom(exitToNormalAtom, { mode: "promise" })
 *        await exitToNormal()
 */
export const exitToNormalAtom = appRuntime.fn(() =>
	Effect.gen(function* () {
		const editor = yield* ModeService
		yield* editor.exitToNormal()
	}).pipe(Effect.catchAll(Effect.logError)),
)

/**
 * Enter sort mode
 *
 * Usage: const [, enterSort] = useAtom(enterSortAtom, { mode: "promise" })
 *        await enterSort()
 */
export const enterSortAtom = appRuntime.fn(() =>
	Effect.gen(function* () {
		const editor = yield* ModeService
		yield* editor.enterSort()
	}).pipe(Effect.catchAll(Effect.logError)),
)

/**
 * Cycle sort configuration for a field
 *
 * Usage: const [, cycleSort] = useAtom(cycleSortAtom, { mode: "promise" })
 *        await cycleSort("priority")
 */
export const cycleSortAtom = appRuntime.fn((field: SortField) =>
	Effect.gen(function* () {
		const editor = yield* ModeService
		yield* editor.cycleSort(field)
	}).pipe(Effect.catchAll(Effect.logError)),
)

// --- NavigationService Actions ---

/**
 * Navigate cursor atom - move cursor in a direction
 *
 * Usage: const [, navigate] = useAtom(navigateAtom, { mode: "promise" })
 *        await navigate("down")
 */
export const navigateAtom = appRuntime.fn((direction: "up" | "down" | "left" | "right") =>
	Effect.gen(function* () {
		const nav = yield* NavigationService
		yield* nav.move(direction)
	}).pipe(Effect.catchAll(Effect.logError)),
)

/**
 * Jump to position atom - jump cursor to specific column/task
 *
 * Usage: const [, jumpTo] = useAtom(jumpToAtom, { mode: "promise" })
 *        await jumpTo({ column: 0, task: 5 })
 */
export const jumpToAtom = appRuntime.fn(({ column, task }: { column: number; task: number }) =>
	Effect.gen(function* () {
		const nav = yield* NavigationService
		yield* nav.jumpTo(column, task)
	}).pipe(Effect.catchAll(Effect.logError)),
)

/**
 * Show toast atom - display a toast notification
 *
 * Usage: const [, showToast] = useAtom(showToastAtom, { mode: "promise" })
 *        await showToast({ type: "success", message: "Task completed!" })
 */
export const showToastAtom = appRuntime.fn(
	({ type, message }: { type: "success" | "error" | "info"; message: string }) =>
		Effect.gen(function* () {
			const toast = yield* ToastService
			yield* toast.show(type, message)
		}).pipe(Effect.catchAll(Effect.logError)),
)

/**
 * Dismiss toast atom - remove a toast by ID
 *
 * Usage: const [, dismissToast] = useAtom(dismissToastAtom, { mode: "promise" })
 *        await dismissToast(toastId)
 */
export const dismissToastAtom = appRuntime.fn((toastId: string) =>
	Effect.gen(function* () {
		const toast = yield* ToastService
		yield* toast.dismiss(toastId)
	}).pipe(Effect.catchAll(Effect.logError)),
)

/**
 * Push overlay atom - add overlay to stack
 *
 * Usage: const [, pushOverlay] = useAtom(pushOverlayAtom, { mode: "promise" })
 *        await pushOverlay({ _tag: "help" })
 */
export const pushOverlayAtom = appRuntime.fn(
	(
		overlay:
			| { readonly _tag: "help" }
			| { readonly _tag: "detail"; readonly taskId: string }
			| { readonly _tag: "create" }
			| { readonly _tag: "claudeCreate" }
			| { readonly _tag: "settings" }
			| {
					readonly _tag: "confirm"
					readonly message: string
					readonly onConfirm: Effect.Effect<void>
			  },
	) =>
		Effect.gen(function* () {
			const overlayService = yield* OverlayService
			yield* overlayService.push(overlay)
		}).pipe(Effect.catchAll(Effect.logError)),
)

/**
 * Pop overlay atom - remove top overlay from stack
 *
 * Usage: const [, popOverlay] = useAtom(popOverlayAtom, { mode: "promise" })
 *        await popOverlay()
 */
export const popOverlayAtom = appRuntime.fn(() =>
	Effect.gen(function* () {
		const overlay = yield* OverlayService
		yield* overlay.pop()
	}).pipe(Effect.catchAll(Effect.logError)),
)
