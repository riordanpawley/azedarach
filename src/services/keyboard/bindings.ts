/**
 * Default Keybindings
 *
 * Central registry of all keyboard shortcuts organized by mode.
 * Uses handler factories for domain-specific actions.
 */

import { Effect } from "effect"
import type { InputHandlers } from "./inputHandlers.js"
import type { PRHandlers } from "./prHandlers.js"
import type { SessionHandlers } from "./sessionHandlers.js"
import type { TaskHandlers } from "./taskHandlers.js"
import type { HandlerContext, Keybinding } from "./types.js"

// ============================================================================
// Binding Context
// ============================================================================

/**
 * Context for creating keybindings
 *
 * Contains all handler modules and services needed to define keybindings.
 */
export interface BindingContext {
	/** Session-related handlers */
	sessionHandlers: SessionHandlers
	/** Task/bead handlers */
	taskHandlers: TaskHandlers
	/** PR workflow handlers */
	prHandlers: PRHandlers
	/** Input processing handlers */
	inputHandlers: InputHandlers
	/** Full handler context for direct service access */
	ctx: HandlerContext
}

// ============================================================================
// Keybinding Factory
// ============================================================================

/**
 * Create the default keybinding array
 *
 * Keybindings are organized by mode:
 * - Normal mode: Navigation, mode transitions, actions
 * - Action mode: Space menu (session, task, PR actions)
 * - Goto mode: Jump navigation
 * - Select mode: Multi-selection
 * - Sort mode: Sort options
 * - Universal: Cross-mode bindings (escape)
 * - Overlay: Overlay-specific bindings
 *
 * @param bc - Binding context with all handlers and services
 */
export const createDefaultBindings = (bc: BindingContext): ReadonlyArray<Keybinding> => [
	// ========================================================================
	// Normal Mode - Navigation
	// ========================================================================
	{
		key: "j",
		mode: "normal",
		description: "Move down",
		action: bc.ctx.nav.move("down"),
	},
	{
		key: "k",
		mode: "normal",
		description: "Move up",
		action: bc.ctx.nav.move("up"),
	},
	{
		key: "h",
		mode: "normal",
		description: "Move left",
		action: bc.ctx.nav.move("left"),
	},
	{
		key: "l",
		mode: "normal",
		description: "Move right",
		action: bc.ctx.nav.move("right"),
	},
	{
		key: "down",
		mode: "normal",
		description: "Move down",
		action: bc.ctx.nav.move("down"),
	},
	{
		key: "up",
		mode: "normal",
		description: "Move up",
		action: bc.ctx.nav.move("up"),
	},
	{
		key: "left",
		mode: "normal",
		description: "Move left",
		action: bc.ctx.nav.move("left"),
	},
	{
		key: "right",
		mode: "normal",
		description: "Move right",
		action: bc.ctx.nav.move("right"),
	},
	{
		key: "CS-d",
		mode: "normal",
		description: "Half page down",
		action: bc.ctx.nav.halfPageDown(),
	},
	{
		key: "CS-u",
		mode: "normal",
		description: "Half page up",
		action: bc.ctx.nav.halfPageUp(),
	},

	// ========================================================================
	// Normal Mode - Mode Transitions
	// ========================================================================
	{
		key: "g",
		mode: "normal",
		description: "Enter goto mode",
		action: bc.ctx.editor.enterGoto(),
	},
	{
		key: "v",
		mode: "normal",
		description: "Enter select mode",
		action: bc.ctx.editor.enterSelect(),
	},
	{
		key: "space",
		mode: "normal",
		description: "Enter action mode",
		action: bc.ctx.editor.enterAction(),
	},
	{
		key: "/",
		mode: "normal",
		description: "Enter search mode",
		action: bc.ctx.editor.enterSearch(),
	},
	{
		key: ":",
		mode: "normal",
		description: "Enter command mode",
		action: bc.ctx.editor.enterCommand(),
	},
	{
		key: ",",
		mode: "normal",
		description: "Enter sort mode",
		action: bc.ctx.editor.enterSort(),
	},

	// ========================================================================
	// Normal Mode - Actions
	// ========================================================================
	{
		key: "q",
		mode: "normal",
		description: "Quit",
		action: Effect.sync(() => process.exit(0)),
	},
	{
		key: "?",
		mode: "normal",
		description: "Show help",
		action: bc.ctx.overlay.push({ _tag: "help" }),
	},
	{
		key: "d",
		mode: "normal",
		description: "Show diagnostics",
		action: bc.ctx.overlay.push({ _tag: "diagnostics" }),
	},
	{
		key: "return",
		mode: "normal",
		description: "View detail",
		action: Effect.suspend(() => bc.ctx.openCurrentDetail()),
	},
	{
		key: "c",
		mode: "normal",
		description: "Create bead via $EDITOR",
		action: Effect.suspend(() => bc.taskHandlers.createBead()),
	},
	{
		key: "S-c",
		mode: "normal",
		description: "Create bead via Claude",
		action: bc.ctx.overlay.push({ _tag: "claudeCreate" }),
	},
	{
		key: "a",
		mode: "normal",
		description: "Toggle VC auto-pilot",
		action: Effect.suspend(() => bc.taskHandlers.toggleVC()),
	},
	{
		key: "tab",
		mode: "normal",
		description: "Toggle view mode (kanban/compact)",
		action: bc.ctx.viewService.toggleViewMode(),
	},
	{
		key: "S-l",
		mode: "normal",
		description: "View logs in tmux popup",
		action: bc.ctx.tmux
			.displayPopup({
				command: `less +F ${process.cwd()}/az.log`,
				width: "90%",
				height: "90%",
				title: " az.log (Ctrl-C to scroll, q to quit) ",
			})
			.pipe(Effect.catchAll(Effect.logError)),
	},

	// ========================================================================
	// Action Mode (Space menu)
	// ========================================================================
	{
		key: "h",
		mode: "action",
		description: "Move task left",
		action: Effect.suspend(() =>
			bc.taskHandlers.moveTasksToColumn("left").pipe(Effect.catchAll(Effect.logError)),
		),
	},
	{
		key: "l",
		mode: "action",
		description: "Move task right",
		action: Effect.suspend(() =>
			bc.taskHandlers.moveTasksToColumn("right").pipe(Effect.catchAll(Effect.logError)),
		),
	},
	{
		key: "left",
		mode: "action",
		description: "Move task left",
		action: Effect.suspend(() =>
			bc.taskHandlers.moveTasksToColumn("left").pipe(Effect.catchAll(Effect.logError)),
		),
	},
	{
		key: "right",
		mode: "action",
		description: "Move task right",
		action: Effect.suspend(() =>
			bc.taskHandlers.moveTasksToColumn("right").pipe(Effect.catchAll(Effect.logError)),
		),
	},
	{
		key: "s",
		mode: "action",
		description: "Start session",
		action: Effect.suspend(() =>
			bc.ctx.editor.exitToNormal().pipe(Effect.tap(() => bc.sessionHandlers.startSession())),
		),
	},
	{
		key: "S-s",
		mode: "action",
		description: "Start+work (prompt Claude)",
		action: Effect.suspend(() =>
			bc.ctx.editor
				.exitToNormal()
				.pipe(Effect.tap(() => bc.sessionHandlers.startSessionWithPrompt())),
		),
	},
	{
		key: "!",
		mode: "action",
		description: "Start+work (skip permissions)",
		action: Effect.suspend(() =>
			bc.ctx.editor
				.exitToNormal()
				.pipe(Effect.tap(() => bc.sessionHandlers.startSessionDangerous())),
		),
	},
	{
		key: "c",
		mode: "action",
		description: "Chat (Haiku)",
		action: Effect.suspend(() =>
			bc.ctx.editor.exitToNormal().pipe(Effect.tap(() => bc.sessionHandlers.chatAboutTask())),
		),
	},
	{
		key: "a",
		mode: "action",
		description: "Attach to session",
		action: Effect.suspend(() =>
			bc.ctx.editor.exitToNormal().pipe(Effect.tap(() => bc.sessionHandlers.attachExternal())),
		),
	},
	{
		key: "S-a",
		mode: "action",
		description: "Attach inline",
		action: Effect.suspend(() =>
			bc.ctx.editor.exitToNormal().pipe(Effect.tap(() => bc.sessionHandlers.attachInline())),
		),
	},
	{
		key: "p",
		mode: "action",
		description: "Pause session",
		action: Effect.suspend(() =>
			bc.ctx.editor.exitToNormal().pipe(Effect.tap(() => bc.sessionHandlers.pauseSession())),
		),
	},
	{
		key: "r",
		mode: "action",
		description: "Resume session",
		action: Effect.suspend(() =>
			bc.ctx.editor.exitToNormal().pipe(Effect.tap(() => bc.sessionHandlers.resumeSession())),
		),
	},
	{
		key: "x",
		mode: "action",
		description: "Stop session",
		action: Effect.suspend(() =>
			bc.ctx.editor.exitToNormal().pipe(Effect.tap(() => bc.sessionHandlers.stopSession())),
		),
	},
	{
		key: "e",
		mode: "action",
		description: "Edit bead ($EDITOR)",
		action: Effect.suspend(() =>
			bc.ctx.editor.exitToNormal().pipe(Effect.tap(() => bc.taskHandlers.editBead())),
		),
	},
	{
		key: "S-e",
		mode: "action",
		description: "Edit bead (Claude)",
		action: Effect.suspend(() =>
			bc.ctx.editor
				.exitToNormal()
				.pipe(
					Effect.tap(() =>
						bc.ctx.toast.show("error", "Claude edit not yet implemented - use 'e' for $EDITOR"),
					),
				),
		),
	},
	{
		key: "S-p",
		mode: "action",
		description: "Create PR",
		action: Effect.suspend(() =>
			bc.ctx.editor.exitToNormal().pipe(Effect.tap(() => bc.prHandlers.createPR())),
		),
	},
	{
		key: "d",
		mode: "action",
		description: "Cleanup worktree",
		action: Effect.suspend(() =>
			bc.ctx.editor.exitToNormal().pipe(Effect.tap(() => bc.prHandlers.cleanup())),
		),
	},
	{
		key: "m",
		mode: "action",
		description: "Merge to main",
		action: Effect.suspend(() =>
			bc.ctx.editor.exitToNormal().pipe(Effect.tap(() => bc.prHandlers.mergeToMain())),
		),
	},
	{
		key: "S-m",
		mode: "action",
		description: "Abort merge",
		action: Effect.suspend(() =>
			bc.ctx.editor.exitToNormal().pipe(Effect.tap(() => bc.prHandlers.abortMerge())),
		),
	},
	{
		key: "S-d",
		mode: "action",
		description: "Delete bead",
		action: Effect.suspend(() =>
			bc.ctx.editor.exitToNormal().pipe(Effect.tap(() => bc.taskHandlers.deleteBead())),
		),
	},
	{
		key: "i",
		mode: "action",
		description: "Attach image",
		action: Effect.gen(function* () {
			const task = yield* bc.ctx.getSelectedTask()
			yield* bc.ctx.editor.exitToNormal()
			if (task) {
				yield* bc.ctx.overlay.push({ _tag: "imageAttach", taskId: task.id })
			}
		}),
	},

	// ========================================================================
	// Goto-Pending Mode (after pressing 'g')
	// ========================================================================
	{
		key: "g",
		mode: "goto-pending",
		description: "Go to top of column",
		action: bc.ctx.nav.goToFirst().pipe(Effect.tap(() => bc.ctx.editor.exitToNormal())),
	},
	{
		key: "e",
		mode: "goto-pending",
		description: "Go to bottom of column",
		action: bc.ctx.nav.goToLast().pipe(Effect.tap(() => bc.ctx.editor.exitToNormal())),
	},
	{
		key: "h",
		mode: "goto-pending",
		description: "Go to first column",
		action: bc.ctx.nav.goToFirstColumn().pipe(Effect.tap(() => bc.ctx.editor.exitToNormal())),
	},
	{
		key: "l",
		mode: "goto-pending",
		description: "Go to last column",
		action: bc.ctx.nav.goToLastColumn().pipe(Effect.tap(() => bc.ctx.editor.exitToNormal())),
	},
	{
		key: "w",
		mode: "goto-pending",
		description: "Enter jump mode",
		action: Effect.gen(function* () {
			const labels = yield* bc.inputHandlers.computeJumpLabels()
			yield* bc.ctx.editor.enterJump(labels)
		}),
	},
	{
		key: "p",
		mode: "goto-pending",
		description: "Open project selector",
		action: bc.ctx.overlay
			.push({ _tag: "projectSelector" })
			.pipe(Effect.tap(() => bc.ctx.editor.exitToNormal())),
	},

	// ========================================================================
	// Select Mode
	// ========================================================================
	{
		key: "j",
		mode: "select",
		description: "Move down",
		action: bc.ctx.nav.move("down"),
	},
	{
		key: "k",
		mode: "select",
		description: "Move up",
		action: bc.ctx.nav.move("up"),
	},
	{
		key: "h",
		mode: "select",
		description: "Move left",
		action: bc.ctx.nav.move("left"),
	},
	{
		key: "l",
		mode: "select",
		description: "Move right",
		action: bc.ctx.nav.move("right"),
	},
	{
		key: "down",
		mode: "select",
		description: "Move down",
		action: bc.ctx.nav.move("down"),
	},
	{
		key: "up",
		mode: "select",
		description: "Move up",
		action: bc.ctx.nav.move("up"),
	},
	{
		key: "left",
		mode: "select",
		description: "Move left",
		action: bc.ctx.nav.move("left"),
	},
	{
		key: "right",
		mode: "select",
		description: "Move right",
		action: bc.ctx.nav.move("right"),
	},
	{
		key: "space",
		mode: "select",
		description: "Toggle selection",
		action: Effect.suspend(() => bc.ctx.toggleCurrentSelection()),
	},
	{
		key: "v",
		mode: "select",
		description: "Exit select mode",
		action: bc.ctx.editor.exitSelect(),
	},

	// ========================================================================
	// Sort Mode
	// ========================================================================
	{
		key: "s",
		mode: "sort",
		description: "Sort by session status",
		action: bc.ctx.editor.cycleSort("session").pipe(
			Effect.tap(() => bc.ctx.editor.exitToNormal()),
			Effect.catchAll(Effect.logError),
		),
	},
	{
		key: "p",
		mode: "sort",
		description: "Sort by priority",
		action: bc.ctx.editor.cycleSort("priority").pipe(
			Effect.tap(() => bc.ctx.editor.exitToNormal()),
			Effect.catchAll(Effect.logError),
		),
	},
	{
		key: "u",
		mode: "sort",
		description: "Sort by updated at",
		action: bc.ctx.editor.cycleSort("updated").pipe(
			Effect.tap(() => bc.ctx.editor.exitToNormal()),
			Effect.catchAll(Effect.logError),
		),
	},

	// ========================================================================
	// Universal (*)
	// ========================================================================
	{
		key: "escape",
		mode: "*",
		description: "Exit/cancel",
		action: Effect.suspend(() => bc.inputHandlers.handleEscape()),
	},

	// ========================================================================
	// Overlay Mode
	// ========================================================================
	{
		key: "escape",
		mode: "overlay",
		description: "Close overlay",
		action: bc.ctx.overlay.pop().pipe(Effect.asVoid),
	},
]
