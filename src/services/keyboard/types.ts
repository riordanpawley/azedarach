/**
 * Keyboard Service Types
 *
 * Shared type definitions for keyboard handling, including:
 * - Mode types for keybinding matching
 * - Keybinding interface
 * - HandlerContext for dependency injection to handler modules
 */

import type { CommandExecutor } from "@effect/platform"
import type { Effect, SubscriptionRef } from "effect"
import type { ResolvedConfig } from "../../config/index"
import type { AttachmentService } from "../../core/AttachmentService"
import type { BeadsClient, BeadsError } from "../../core/BeadsClient"
import type { BeadEditorService } from "../../core/EditorService"
import type { ImageAttachmentService } from "../../core/ImageAttachmentService"
import type { PRWorkflow } from "../../core/PRWorkflow"
import type { SessionManager } from "../../core/SessionManager"
import type { TmuxService } from "../../core/TmuxService"
import type { VCService } from "../../core/VCService"
import type { TaskWithSession } from "../../ui/types"
import type { BoardService } from "../BoardService"
import type { CommandQueueService } from "../CommandQueueService"
import type { EditorService } from "../EditorService"
import type { NavigationService } from "../NavigationService"
import type { OverlayService } from "../OverlayService"
import type { ToastService } from "../ToastService"
import type { ViewService } from "../ViewService"

// ============================================================================
// Mode Types
// ============================================================================

/**
 * Keyboard mode for keybinding matching
 *
 * Modes map to EditorService modes:
 * - normal: Default navigation
 * - select: Multi-selection
 * - action: Action palette (Space menu)
 * - goto-pending: Waiting for second key after 'g'
 * - goto-jump: Jump label mode (2-char input)
 * - search: Search/filter with text input
 * - command: VC command with text input
 * - overlay: Any overlay is open
 * - sort: Sort menu
 * - *: Universal (matches any mode)
 */
export type KeyMode =
	| "normal"
	| "select"
	| "action"
	| "goto-pending"
	| "goto-jump"
	| "search"
	| "command"
	| "overlay"
	| "sort"
	| "*"

// ============================================================================
// Keybinding Types
// ============================================================================

/**
 * Platform dependencies that keybinding actions may require.
 * CommandExecutor is the one platform dependency allowed to leak through method return types.
 */
export type KeybindingDeps = CommandExecutor.CommandExecutor

/**
 * Keybinding definition with mode-specific action
 *
 * Actions may have platform requirements (CommandExecutor, FileSystem, BeadsClient)
 * which are satisfied by the runtime layer when KeyboardService is used.
 */
export interface Keybinding {
	readonly key: string
	readonly mode: KeyMode
	readonly description: string
	readonly action: Effect.Effect<void, BeadsError, KeybindingDeps>
}

// ============================================================================
// Handler Context
// ============================================================================

/**
 * Context object passed to handler factory functions
 *
 * Contains all injected services and pre-bound helper functions that handlers need.
 * This pattern allows handlers to be defined in separate modules while still
 * having access to services injected at KeyboardService construction time.
 */
export interface HandlerContext {
	// ========================================================================
	// Services
	// ========================================================================

	/** Show success/error/info toasts */
	readonly toast: ToastService

	/** Manage overlay stack (detail, help, confirm, imageAttach) */
	readonly overlay: OverlayService

	/** Cursor position and task focus management */
	readonly nav: NavigationService

	/** Mode FSM (normal/select/action/goto/search/command/sort) */
	readonly editor: EditorService

	/** Task data and board state */
	readonly board: BoardService

	/** Start/stop/pause/resume Claude sessions */
	readonly sessionManager: SessionManager

	/** Attach to tmux sessions (external/inline) */
	readonly attachment: AttachmentService

	/** Create PR, merge, cleanup, conflict check */
	readonly prWorkflow: PRWorkflow

	/** Update/delete/sync beads */
	readonly beadsClient: BeadsClient

	/** Edit/create beads via $EDITOR */
	readonly beadEditor: BeadEditorService

	/** Toggle kanban/compact view */
	readonly viewService: ViewService

	/** Attach images to tasks */
	readonly imageAttachment: ImageAttachmentService

	/** Display popups, run tmux commands */
	readonly tmux: TmuxService

	/** Start/stop VC auto-pilot, send commands */
	readonly vc: VCService

	/** Queue long-running operations per task */
	readonly commandQueue: CommandQueueService

	// ========================================================================
	// Configuration
	// ========================================================================

	/** Resolved application configuration (session.command, etc.) */
	readonly resolvedConfig: ResolvedConfig

	// ========================================================================
	// Shared Helpers (pre-bound to services)
	// ========================================================================

	/**
	 * Get the task currently at cursor position
	 * Resolves: cursor position → task ID → task object
	 */
	readonly getSelectedTask: () => Effect.Effect<TaskWithSession | undefined>

	/**
	 * Get current cursor column index (0-3)
	 */
	readonly getColumnIndex: () => Effect.Effect<number>

	/**
	 * Show error toast with consistent formatting
	 * Also logs the error via Effect.logError for debugging
	 *
	 * @param prefix - Error message prefix (e.g., "Failed to start")
	 */
	readonly showErrorToast: (prefix: string) => (error: unknown) => Effect.Effect<void>

	/**
	 * Open detail overlay for current cursor position
	 */
	readonly openCurrentDetail: () => Effect.Effect<void>

	/**
	 * Toggle selection for task at current cursor position
	 */
	readonly toggleCurrentSelection: () => Effect.Effect<void>

	/**
	 * Execute a queued operation for a task
	 *
	 * Operations like merge, cleanup, start session, etc. can conflict if run
	 * simultaneously on the same task. This helper ensures they run one at a time.
	 *
	 * @param taskId - The task to operate on
	 * @param label - Human-readable label for the operation (e.g., "merge", "cleanup")
	 * @param operation - The Effect to execute (return value is ignored)
	 */
	readonly withQueue: <A, E>(
		taskId: string,
		label: string,
		operation: Effect.Effect<A, E, CommandExecutor.CommandExecutor>,
	) => Effect.Effect<void, never, CommandExecutor.CommandExecutor>

	/**
	 * Check if a task has an operation in progress and show a toast if so.
	 *
	 * Returns true if busy (caller should abort), false if idle (caller can proceed).
	 * When busy, shows a toast like "az-123 is busy (merge in progress)"
	 *
	 * @param taskId - The task to check
	 * @returns Effect that resolves to true if busy, false if idle
	 */
	readonly checkBusy: (taskId: string) => Effect.Effect<boolean>

	// ========================================================================
	// Image Attachment State (needed by inputHandlers)
	// ========================================================================

	/**
	 * Image attachment overlay state ref
	 * Needed for handleImageAttachInput to read/write state
	 */
	readonly imageAttachmentOverlayState: SubscriptionRef.SubscriptionRef<{
		taskId: string | null
		mode: "menu" | "path"
		pathInput: string
		isAttaching: boolean
	}>
}
