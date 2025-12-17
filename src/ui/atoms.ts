/**
 * Atoms for Azedarach UI state
 *
 * Uses effect-atom for reactive state management with Effect integration.
 */

import { Command, type CommandExecutor, PlatformLogger } from "@effect/platform"
import { BunContext } from "@effect/platform-bun"
import { Atom, Result } from "@effect-atom/atom"
import { Effect, Layer, Logger, pipe, type Record, Schedule, SubscriptionRef } from "effect"
import { ModeService } from "../atoms/runtime"
import { AppConfig } from "../config/index"
import { AttachmentService } from "../core/AttachmentService"
import { BeadsClient } from "../core/BeadsClient"
import { BeadEditorService } from "../core/EditorService"
import { HookReceiver, mapEventToState } from "../core/HookReceiver"
import { type ImageAttachment, ImageAttachmentService } from "../core/ImageAttachmentService"
import { PRWorkflow } from "../core/PRWorkflow"
import { SessionManager } from "../core/SessionManager"
import { TerminalService } from "../core/TerminalService"
import { TmuxService } from "../core/TmuxService"
import { type VCExecutorInfo, VCService } from "../core/VCService"
import { BoardService } from "../services/BoardService"
import { ClockService } from "../services/ClockService"
import type { SortField } from "../services/EditorService"
import { EditorService } from "../services/EditorService"
import { KeyboardService } from "../services/KeyboardService"
import { NavigationService } from "../services/NavigationService"
import { OverlayService } from "../services/OverlayService"
import { SessionService } from "../services/SessionService"
// New atomic Effect services
import { ToastService } from "../services/ToastService"
import { ViewService } from "../services/ViewService"
import type { TaskWithSession } from "./types"

const platformLayer = BunContext.layer

const fileLogger = Logger.logfmtLogger.pipe(PlatformLogger.toFile("az.log", { flag: "a" }))
const appLayer = Layer.mergeAll(
	SessionService.Default,
	AttachmentService.Default,
	ImageAttachmentService.Default,
	BoardService.Default,
	ClockService.Default,
	TmuxService.Default,
	BeadEditorService.Default,
	ModeService.Default,
	PRWorkflow.Default,
	TerminalService.Default,
	EditorService.Default,
	KeyboardService.Default,
	OverlayService.Default,
	ToastService.Default,
	NavigationService.Default,
	SessionManager.Default,
	BeadsClient.Default,
	AppConfig.Default,
	VCService.Default,
	ViewService.Default,
	HookReceiver.Default,
).pipe(
	Layer.provide(Logger.replaceScoped(Logger.defaultLogger, fileLogger)),
	Layer.provideMerge(platformLayer),
)

/**
 * Runtime atom that provides all services and platform dependencies
 *
 * This creates a runtime that all other async atoms can use.
 */
export const appRuntime = Atom.runtime(appLayer)

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
export const vcStatusAtom = appRuntime.subscriptionRef((get) => pipe(get.result(vcStatusRefAtom)))

// ============================================================================
// Hook Receiver (Claude Code native hooks integration)
// ============================================================================

/**
 * Hook receiver starter atom - starts the hook receiver on mount
 *
 * Watches for notification files from Claude Code hooks and updates
 * session state in SessionManager. The receiver is automatically stopped
 * when the atom unmounts.
 *
 * Usage: Simply subscribe to this atom in the app root to start the receiver.
 *        useAtomValue(hookReceiverStarterAtom)
 */
export const hookReceiverStarterAtom = appRuntime.atom(
	Effect.gen(function* () {
		const receiver = yield* HookReceiver
		const manager = yield* SessionManager

		// Handler that maps hook events to session state changes
		const handler = (event: { event: string; beadId: string }) =>
			Effect.gen(function* () {
				const newState = mapEventToState(event.event as "idle_prompt" | "stop" | "session_end")
				if (newState) {
					yield* manager
						.updateState(event.beadId, newState)
						.pipe(Effect.catchAll((e) => Effect.logWarning(`Failed to update session state: ${e}`)))
				}
			})

		// Start the receiver - it will be interrupted when this atom unmounts
		const fiber = yield* receiver.start(handler)

		yield* Effect.log("HookReceiver started - watching for Claude Code hook notifications")

		return fiber
	}),
	{ initialValue: undefined },
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
 * Uses ViewService for reactive state via SubscriptionRef.
 *
 * Usage: const viewMode = useAtomValue(viewModeAtom)
 */
export const viewModeAtom = appRuntime.subscriptionRef(
	Effect.gen(function* () {
		const viewService = yield* ViewService
		return viewService.viewMode
	}),
)

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
 * Create a new task with full orchestration
 *
 * Handles the complete create flow: dismiss overlay, create bead, refresh board,
 * navigate to new task, show toast. All logic in Effects, not React callbacks.
 *
 * Usage: const createTask = useAtomSet(createTaskAtom, { mode: "promise" })
 *        await createTask({ title: "New task", type: "task", priority: 2 })
 */
export const createTaskAtom = appRuntime.fn(
	(params: { title: string; type?: string; priority?: number; description?: string }) =>
		Effect.gen(function* () {
			const client = yield* BeadsClient
			const board = yield* BoardService
			const navigation = yield* NavigationService
			const toast = yield* ToastService
			const overlay = yield* OverlayService

			yield* overlay.pop()

			const issue = yield* client.create(params)

			yield* board.refresh()
			yield* navigation.jumpToTask(issue.id)
			yield* toast.show("success", `Created task: ${issue.id}`)

			return issue
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
 * Create a bead from natural language using Claude CLI
 *
 * Runs claude in non-interactive mode (-p) to create a bead based on
 * the user's description. Much lighter than a full interactive session.
 *
 * Handles full orchestration: dismiss overlay, show progress toast, create via Claude,
 * refresh board, navigate to new task, show success toast.
 *
 * Usage: const claudeCreate = useAtom(claudeCreateSessionAtom, { mode: "promise" })
 *        const beadId = await claudeCreate("Add dark mode toggle to settings")
 */
export const claudeCreateSessionAtom = appRuntime.fn((description: string) =>
	Effect.gen(function* () {
		const { config } = yield* AppConfig
		const board = yield* BoardService
		const navigation = yield* NavigationService
		const toast = yield* ToastService
		const overlay = yield* OverlayService

		// Dismiss overlay first
		yield* overlay.pop()
		yield* toast.show("info", "Creating task with Claude...")

		const projectPath = process.cwd()
		const { dangerouslySkipPermissions } = config.session

		// Build the prompt for Claude with explicit bd create instructions
		const prompt = `Create a new bead issue for the following task using the \`bd\` CLI tool.

**Task description:**
${description}

**Instructions:**
1. Use \`bd create --title="..." --type=task|bug|feature --priority=0|1|2|3|4\` (0=critical, 2=medium, 4=backlog)
2. Choose appropriate type: task for general work, feature for new functionality, bug for fixes
3. Add a description with --description="..." if the task needs more detail
4. Output ONLY the created issue ID on the final line (e.g., "az-123")

Create the bead now and output just the ID.`

		// Build command arguments for non-interactive print mode
		// Pre-approve bd CLI commands and beads MCP tools to avoid permission hang in non-interactive mode
		const args = [
			"-p",
			prompt,
			"--output-format",
			"text",
			"--allowedTools",
			"Bash(bd:*)",
			"mcp__plugin_beads_beads__create",
		]
		if (dangerouslySkipPermissions) {
			args.push("--dangerously-skip-permissions")
		}

		// Run claude CLI in non-interactive print mode
		const claudeCmd = Command.make("claude", ...args).pipe(Command.workingDirectory(projectPath))

		const result = yield* Command.string(claudeCmd).pipe(
			Effect.timeout("120 seconds"),
			Effect.mapError((e) => new Error(`Claude CLI failed: ${e}`)),
		)

		// Parse output to find created bead ID (pattern: az-xxx, beads-xxx, etc.)
		const beadIdPattern = /^([a-z]+-[a-z0-9]+)\s*$/im
		const lines = result.trim().split("\n")

		// Look for bead ID in the output, preferring the last line
		let beadId: string | null = null
		for (let i = lines.length - 1; i >= 0; i--) {
			const match = lines[i].trim().match(beadIdPattern)
			if (match) {
				beadId = match[1]
				break
			}
		}

		// If no ID found in strict format, try to find any bead-like ID in the output
		if (!beadId) {
			const broadMatch = result.match(/\b([a-z]+-[a-z0-9]{2,})\b/i)
			if (broadMatch) {
				beadId = broadMatch[1]
			}
		}

		// Refresh the board to show the new task
		yield* board.refresh()

		if (!beadId) {
			yield* Effect.logWarning(`Could not parse bead ID from Claude output: ${result}`)
			yield* toast.show("success", "Task created (check board for new task)")
			return "unknown"
		}

		// Navigate to the new task and show success
		yield* navigation.jumpToTask(beadId)
		yield* toast.show("success", `Created task: ${beadId}`)

		return beadId
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
 * Merge worktree branch to main and clean up
 *
 * Merges the worktree branch to main locally without creating a PR.
 * Ideal for completed work that doesn't need review.
 *
 * Usage: const mergeToMain = useAtomSet(mergeToMainAtom, { mode: "promise" })
 *        await mergeToMain(beadId)
 */
export const mergeToMainAtom = appRuntime.fn((beadId: string) =>
	Effect.gen(function* () {
		const prWorkflow = yield* PRWorkflow
		yield* prWorkflow.mergeToMain({
			beadId,
			projectPath: process.cwd(),
		})
	}).pipe(Effect.tapError(Effect.logError)),
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
// Keyboard Handling Atom
// ============================================================================

/**
 * Handle keyboard input via KeyboardService
 *
 * This is the main entry point for keyboard handling. It delegates to
 * KeyboardService which has all keybindings defined as data.
 *
 * Usage: const [, handleKey] = useAtom(handleKeyAtom, { mode: "promise" })
 *        handleKey(event.name)
 */
export const handleKeyAtom = appRuntime.fn((key: string) =>
	Effect.gen(function* () {
		const keyboard = yield* KeyboardService
		yield* keyboard.handleKey(key)
	}).pipe(Effect.catchAll(Effect.logError)),
)

// ============================================================================
// Atomic Service State Atoms (ModeService, NavigationService, etc.)
// ============================================================================

/**
 * Clock tick atom - current timestamp updated every second
 *
 * Used for elapsed timer displays on TaskCards. Subscribing to this atom
 * triggers re-renders every second, allowing components to derive elapsed
 * time from session start timestamps.
 *
 * Usage: const now = useAtomValue(clockTickAtom)
 *        const elapsed = now - sessionStartedAt
 */
export const clockTickAtom = appRuntime.subscriptionRef(
	Effect.gen(function* () {
		const clock = yield* ClockService
		return clock.now
	}),
)

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
 * Focused task ID atom - subscribes to NavigationService focusedTaskId
 *
 * This is the source of truth for which task is selected.
 * Position (columnIndex, taskIndex) is derived in useNavigation.
 *
 * Usage: const focusedTaskId = useAtomValue(focusedTaskIdAtom)
 */
export const focusedTaskIdAtom = appRuntime.subscriptionRef(
	Effect.gen(function* () {
		const nav = yield* NavigationService
		return nav.focusedTaskId
	}),
)

/**
 * Initialize navigation - ensures a task is focused
 *
 * Called when the app starts or when no task is focused.
 * Sets focusedTaskId to the first available task.
 *
 * Usage: const initNav = useAtomSet(initializeNavigationAtom, { mode: "promise" })
 *        initNav()
 */
export const initializeNavigationAtom = appRuntime.fn(() =>
	Effect.gen(function* () {
		const nav = yield* NavigationService
		yield* nav.initialize()
	}).pipe(Effect.catchAll(Effect.logError)),
)

/**
 * Refresh board data from BeadsClient
 *
 * Must be called before navigation can work, as NavigationService
 * depends on BoardService for filtered task data.
 *
 * Usage: const refreshBoard = useAtomSet(refreshBoardAtom, { mode: "promise" })
 *        refreshBoard()
 */
export const refreshBoardAtom = appRuntime.fn(() =>
	Effect.gen(function* () {
		const board = yield* BoardService
		yield* board.refresh()
	}).pipe(Effect.catchAll(Effect.logError)),
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
 *        await enterJump(labelsRecord)
 */
export const enterJumpAtom = appRuntime.fn(
	(
		labels: Record.ReadonlyRecord<
			string,
			{ taskId: string; columnIndex: number; taskIndex: number }
		>,
	) =>
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
 * Jump to task by ID - move cursor directly to a specific task
 *
 * Useful after creating a bead when you know the ID but not the position.
 *
 * Usage: const [, jumpToTask] = useAtom(jumpToTaskAtom, { mode: "promise" })
 *        await jumpToTask("az-123")
 */
export const jumpToTaskAtom = appRuntime.fn((taskId: string) =>
	Effect.gen(function* () {
		const nav = yield* NavigationService
		yield* nav.jumpToTask(taskId)
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
			| { readonly _tag: "imageAttach"; readonly taskId: string }
			| {
					readonly _tag: "confirm"
					readonly message: string
					// Exception: CommandExecutor is the only allowed leaked requirement
					readonly onConfirm: Effect.Effect<void, never, CommandExecutor.CommandExecutor>
			  },
	) =>
		Effect.gen(function* () {
			const overlayService = yield* OverlayService
			yield* overlayService.push(overlay)

			// Load attachments when opening detail overlay
			if (overlay._tag === "detail") {
				const imageService = yield* ImageAttachmentService
				yield* imageService.loadForTask(overlay.taskId)
			}

			// Initialize overlay state when opening imageAttach overlay
			if (overlay._tag === "imageAttach") {
				const imageService = yield* ImageAttachmentService
				yield* imageService.openOverlay(overlay.taskId)
			}
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
		const overlayService = yield* OverlayService
		const popped = yield* overlayService.pop()

		// Clear attachments when closing detail overlay
		if (popped?._tag === "detail") {
			const imageService = yield* ImageAttachmentService
			yield* imageService.clearCurrent()
		}

		// Close overlay state when closing imageAttach overlay
		if (popped?._tag === "imageAttach") {
			const imageService = yield* ImageAttachmentService
			yield* imageService.closeOverlay()
		}
	}).pipe(Effect.catchAll(Effect.logError)),
)

// ============================================================================
// Image Attachment Atoms
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

// Re-export ImageAttachment type for components
export type { ImageAttachment }
