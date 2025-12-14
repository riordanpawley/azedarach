/**
 * Atoms for Azedarach UI state
 *
 * Uses effect-atom for reactive state management with Effect integration.
 */
import { Atom } from "@effect-atom/atom"
import { Effect, Layer, pipe, Schedule, Stream, SubscriptionRef } from "effect"
import { AppConfigLiveWithPlatform } from "../config/index"
import { AttachmentService, AttachmentServiceLive } from "../core/AttachmentService"
import { BeadsClient, BeadsClientLiveWithPlatform } from "../core/BeadsClient"
import { EditorService, EditorServiceLive } from "../core/EditorService"
import { PRWorkflow, PRWorkflowLive } from "../core/PRWorkflow"
import { SessionManager, SessionManagerLive } from "../core/SessionManager"
import { TerminalServiceLive } from "../core/TerminalService"
import { TmuxServiceLive } from "../core/TmuxService"
import { type VCExecutorInfo, VCService, VCServiceLive } from "../core/VCService"
import type { TaskWithSession } from "./types"

// New atomic Effect services
import { ToastService } from "../services/ToastService"
import { OverlayService } from "../services/OverlayService"
import { NavigationService } from "../services/NavigationService"
import { EditorService as ModeService } from "../services/EditorService"
import { BoardService } from "../services/BoardService"
import { SessionService } from "../services/SessionService"
import { KeyboardService } from "../services/KeyboardService"

/**
 * Combined runtime layer with all services
 *
 * Merges BeadsClient, TmuxService, TerminalService, AttachmentService, SessionManager, and AppConfig.
 * AppConfig is provided so SessionManager can use custom init commands and session settings.
 */
const configLayer = AppConfigLiveWithPlatform(process.cwd())

const baseLayer = Layer.mergeAll(
	BeadsClientLiveWithPlatform,
	TmuxServiceLive,
	TerminalServiceLive,
	configLayer,
)

// Core services layer (existing)
const coreServicesLayer = baseLayer.pipe(
	Layer.merge(AttachmentServiceLive.pipe(Layer.provide(baseLayer))),
	Layer.merge(SessionManagerLive),
	Layer.merge(EditorServiceLive.pipe(Layer.provide(baseLayer))),
	Layer.merge(PRWorkflowLive),
	Layer.merge(VCServiceLive),
)

// Atomic services layer (new Effect.Service pattern)
// Independent services with no deps
const independentServicesLayer = Layer.mergeAll(
	ToastService.Default,
	OverlayService.Default,
	NavigationService.Default,
	ModeService.Default,
)

// BoardService needs BeadsClient & SessionManager from coreServicesLayer
// SessionService needs ToastService, NavigationService, BoardService
// KeyboardService needs ToastService, OverlayService, NavigationService, ModeService, BoardService
const dependentServicesLayer = Layer.mergeAll(
	BoardService.Default,
	SessionService.Default,
	KeyboardService.Default,
)

// Combine core and independent services first
const baseWithIndependent = coreServicesLayer.pipe(
	Layer.merge(independentServicesLayer),
)

// Dependent services need context from baseWithIndependent
// Use Layer.provide to satisfy their requirements, then merge outputs
const dependentServicesWithDeps = dependentServicesLayer.pipe(
	Layer.provide(baseWithIndependent),
)

const appLayer = baseWithIndependent.pipe(
	Layer.merge(dependentServicesWithDeps),
)

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
		}),
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
		}),
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
	}),
)

/**
 * Attach to a session inline (future: replaces TUI)
 */
export const attachInlineAtom = appRuntime.fn((sessionId: string) =>
	Effect.gen(function* () {
		const service = yield* AttachmentService
		yield* service.attachInline(sessionId)
	}),
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
	}),
)

/**
 * Pause a running session (Ctrl+C + WIP commit)
 */
export const pauseSessionAtom = appRuntime.fn((beadId: string) =>
	Effect.gen(function* () {
		const manager = yield* SessionManager
		yield* manager.pause(beadId)
	}),
)

/**
 * Resume a paused session
 */
export const resumeSessionAtom = appRuntime.fn((beadId: string) =>
	Effect.gen(function* () {
		const manager = yield* SessionManager
		yield* manager.resume(beadId)
	}),
)

/**
 * Stop a running session (kills tmux, marks as idle)
 */
export const stopSessionAtom = appRuntime.fn((beadId: string) =>
	Effect.gen(function* () {
		const manager = yield* SessionManager
		yield* manager.stop(beadId)
	}),
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
		}),
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
		const editor = yield* EditorService
		yield* editor.editBead(bead)
	}),
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
	}),
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
	}),
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
export const toggleVCAutoPilotAtom = appRuntime.fn((_: void, get) =>
	Effect.gen(function* () {
		const vc = yield* VCService
		const newStatus = yield* vc.toggleAutoPilot()

		// Update the ref immediately so UI reflects the change
		const ref = yield* get.result(vcStatusRefAtom)
		yield* SubscriptionRef.set(ref, newStatus)

		return newStatus
	}),
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
	}),
)
