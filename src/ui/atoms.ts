/**
 * Atoms for Azedarach UI state
 *
 * Uses effect-atom for reactive state management with Effect integration.
 */
import { Atom } from "@effect-atom/atom"
import { Effect, Layer, pipe, Schedule, Stream, SubscriptionRef } from "effect"
import { AppConfig, AppConfigLiveWithPlatform } from "../config/index"
import { AttachmentService, AttachmentServiceLive } from "../core/AttachmentService"
import { BeadsClient, BeadsClientLiveWithPlatform } from "../core/BeadsClient"
import { EditorService, EditorServiceLive } from "../core/EditorService"
import { PRWorkflow, PRWorkflowLive } from "../core/PRWorkflow"
import { SessionManager } from "../core/SessionManager"
import { TerminalServiceLive } from "../core/TerminalService"
import { TmuxService, TmuxServiceLive } from "../core/TmuxService"
import { type VCExecutorInfo, VCService, VCServiceLive } from "../core/VCService"
import type { TaskWithSession } from "./types"

// ============================================================================
// Layer Composition
// ============================================================================

/**
 * AppConfig layer - contextual, needs project path
 */
const configLayer = AppConfigLiveWithPlatform(process.cwd())

/**
 * Leaf services - no dependencies on other app services
 */
const leafServices = Layer.mergeAll(
	BeadsClientLiveWithPlatform,
	TmuxServiceLive,
	TerminalServiceLive,
)

/**
 * SessionManager.Default bundles its deps but requires AppConfig
 * Provide AppConfig to satisfy that requirement
 */
const sessionManagerLayer = SessionManager.Default.pipe(
	Layer.provide(configLayer),
)

/**
 * Services that need TmuxService and TerminalService
 */
const attachmentLayer = AttachmentServiceLive.pipe(
	Layer.provide(Layer.mergeAll(TmuxServiceLive, TerminalServiceLive)),
)

/**
 * EditorService needs BeadsClient
 */
const editorLayer = EditorServiceLive.pipe(
	Layer.provide(BeadsClientLiveWithPlatform),
)

/**
 * PRWorkflowLive uses SessionManagerLive which requires AppConfig
 */
const prWorkflowLayer = PRWorkflowLive.pipe(
	Layer.provide(configLayer),
)

/**
 * Combined app layer - all services merged
 *
 * Layer composition pattern:
 * - Leaf layers (no app deps): merge directly
 * - Layers with deps: provide those deps, then merge
 */
const appLayer = Layer.mergeAll(
	leafServices,
	configLayer,
	sessionManagerLayer,
	attachmentLayer,
	editorLayer,
	prWorkflowLayer,
	VCServiceLive,
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

// ============================================================================
// Editor Create Atom (manual create via $EDITOR)
// ============================================================================

/**
 * Create a new bead via $EDITOR
 *
 * Opens $EDITOR with a blank template, parses the result, and creates the bead.
 * Returns the created bead info.
 *
 * Usage: const createBead = useAtom(createBeadViaEditorAtom, { mode: "promise" })
 *        const { id, title } = await createBead()
 */
export const createBeadViaEditorAtom = appRuntime.fn(() =>
	Effect.gen(function* () {
		const editor = yield* EditorService
		return yield* editor.createBead()
	}),
)

// ============================================================================
// Claude Create Session Atom
// ============================================================================

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

		// Warn if using dangerous permissions flag
		if (dangerouslySkipPermissions) {
			yield* Effect.logWarning(
				"Running Claude with --dangerously-skip-permissions. All permission prompts will be bypassed.",
			)
		}

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

**bd create syntax:**
\`\`\`
bd create --title="<title>" --type=<task|feature|bug> [--description="<description>"] [--priority=<1-5>]
\`\`\`

**Examples:**
- \`bd create --title="Add dark mode" --type=feature --description="Toggle in settings"\`
- \`bd create --title="Fix login bug" --type=bug --priority=1\`

**Your task:**
Based on the following description, create an appropriate bead:

${description}

After running \`bd create\`, tell me the bead ID that was created and ask if I'd like you to start working on it immediately.`

		// Send the prompt to Claude
		yield* tmux.sendKeys(sessionName, prompt)

		return sessionName
	}),
)

// ============================================================================
// Claude Edit Session Atom (AI edit mode)
// ============================================================================

/**
 * Create a Claude session to edit an existing bead
 *
 * Spawns a tmux session with Claude in the main project directory,
 * sends the bead details to Claude, and asks for editing guidance.
 *
 * Returns the session name so the UI can inform the user.
 *
 * Usage: const claudeEdit = useAtom(claudeEditSessionAtom, { mode: "promise" })
 *        const sessionName = await claudeEdit(task)
 */
export const claudeEditSessionAtom = appRuntime.fn((task: TaskWithSession) =>
	Effect.gen(function* () {
		const tmux = yield* TmuxService
		const { config } = yield* AppConfig

		// Generate unique session name with bead ID
		const sessionName = `claude-edit-${task.id}`
		const projectPath = process.cwd()

		// Build the Claude command with session settings
		const { command: claudeCommand, shell, tmuxPrefix, dangerouslySkipPermissions } = config.session
		const claudeArgs = dangerouslySkipPermissions ? " --dangerously-skip-permissions" : ""

		// Warn if using dangerous permissions flag
		if (dangerouslySkipPermissions) {
			yield* Effect.logWarning(
				"Running Claude with --dangerously-skip-permissions. All permission prompts will be bypassed.",
			)
		}

		// Create the tmux session
		yield* tmux.newSession(sessionName, {
			cwd: projectPath,
			command: `${shell} -c '${claudeCommand}${claudeArgs}; exec ${shell}'`,
			prefix: tmuxPrefix,
		})

		// Wait briefly for Claude to start up
		yield* Effect.sleep("1500 millis")

		// Build the bead details for context
		const beadDetails = `**Bead ID:** ${task.id}
**Title:** ${task.title}
**Type:** ${task.issue_type}
**Status:** ${task.status}
**Priority:** P${task.priority}
**Description:** ${task.description || "(none)"}
**Design:** ${task.design || "(none)"}
**Notes:** ${task.notes || "(none)"}
**Acceptance Criteria:** ${task.acceptance || "(none)"}`

		// Build the prompt for Claude with explicit bd update instructions
		const prompt = `I want to edit the following bead. Here are its current details:

${beadDetails}

**bd update syntax:**
\`\`\`
bd update <bead-id> [--title="<title>"] [--status=<status>] [--priority=<0-4>] [--description="<desc>"] [--notes="<notes>"] [--design="<design>"] [--acceptance="<criteria>"]
\`\`\`

**Available statuses:** backlog, ready, in_progress, review, done

What would you like to change? Describe the edits you want and I'll help you update the bead using \`bd update ${task.id}\`.`

		// Send the prompt to Claude
		yield* tmux.sendKeys(sessionName, prompt)

		return sessionName
	}),
)
