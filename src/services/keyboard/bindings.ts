/**
 * Default Keybindings
 *
 * Central registry of all keyboard shortcuts organized by mode.
 * Uses Effect.Service layers for domain-specific actions.
 */

import { Effect } from "effect"
import type { BeadsClient, Issue } from "../../core/BeadsClient.js"
import type { TmuxService } from "../../core/TmuxService.js"
import type { BoardService } from "../BoardService.js"
import type { EditorService } from "../EditorService.js"
import type { NavigationService } from "../NavigationService.js"
import type { OverlayService } from "../OverlayService.js"
import type { SettingsService } from "../SettingsService.js"
import type { ToastService } from "../ToastService.js"
import type { ViewService } from "../ViewService.js"
import type { DevServerHandlersService } from "./DevServerHandlersService.js"
import type { InputHandlersService } from "./InputHandlersService.js"
import type { KeyboardHelpersService } from "./KeyboardHelpersService.js"
import type { OrchestrateHandlersService } from "./OrchestrateHandlersService.js"
import type { PRHandlersService } from "./PRHandlersService.js"
import type { SessionHandlersService } from "./SessionHandlersService.js"
import type { TaskHandlersService } from "./TaskHandlersService.js"
import type { Keybinding } from "./types.js"

// ============================================================================
// Binding Context
// ============================================================================

/**
 * Context for creating keybindings
 *
 * Contains all service instances needed to define keybindings.
 * Services are injected at KeyboardService construction time.
 */
export interface BindingContext {
	// Handler services
	sessionHandlers: SessionHandlersService
	taskHandlers: TaskHandlersService
	prHandlers: PRHandlersService
	inputHandlers: InputHandlersService
	orchestrateHandlers: OrchestrateHandlersService
	devServerHandlers: DevServerHandlersService
	helpers: KeyboardHelpersService

	// Core services for direct bindings
	nav: NavigationService
	editor: EditorService
	overlay: OverlayService
	settings: SettingsService
	toast: ToastService
	viewService: ViewService
	tmux: TmuxService
	beadsClient: BeadsClient
	board: BoardService
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
 * @param bc - Binding context with all services
 */
/**
 * Modes that support standard board navigation (hjkl/arrows)
 *
 * These modes use the same navigation bindings - moving cursor around the board.
 * Orchestrate mode is excluded because it has its own linear navigation.
 */
const BOARD_NAV_MODES = ["normal", "select", "mergeSelect"] as const

export const createDefaultBindings = (bc: BindingContext): ReadonlyArray<Keybinding> => [
	// ========================================================================
	// Board Navigation (shared across normal, select, mergeSelect modes)
	// ========================================================================
	{
		key: "j",
		mode: [...BOARD_NAV_MODES],
		description: "Move down",
		action: bc.nav.move("down"),
	},
	{
		key: "k",
		mode: [...BOARD_NAV_MODES],
		description: "Move up",
		action: bc.nav.move("up"),
	},
	{
		key: "h",
		mode: [...BOARD_NAV_MODES],
		description: "Move left",
		action: bc.nav.move("left"),
	},
	{
		key: "l",
		mode: [...BOARD_NAV_MODES],
		description: "Move right",
		action: bc.nav.move("right"),
	},
	{
		key: "down",
		mode: [...BOARD_NAV_MODES],
		description: "Move down",
		action: bc.nav.move("down"),
	},
	{
		key: "up",
		mode: [...BOARD_NAV_MODES],
		description: "Move up",
		action: bc.nav.move("up"),
	},
	{
		key: "left",
		mode: [...BOARD_NAV_MODES],
		description: "Move left",
		action: bc.nav.move("left"),
	},
	{
		key: "right",
		mode: [...BOARD_NAV_MODES],
		description: "Move right",
		action: bc.nav.move("right"),
	},
	{
		key: "C-d",
		mode: "normal",
		description: "Half page down",
		action: bc.nav.halfPageDown(),
	},
	{
		key: "C-u",
		mode: "normal",
		description: "Half page up",
		action: bc.nav.halfPageUp(),
	},

	// ========================================================================
	// Normal Mode - Mode Transitions
	// ========================================================================
	{
		key: "g",
		mode: "normal",
		description: "Enter goto mode",
		action: bc.editor.enterGoto(),
	},
	{
		key: "v",
		mode: "normal",
		description: "Enter select mode",
		action: bc.editor.enterSelect(),
	},
	{
		key: "S-5",
		mode: "normal",
		description: "Select all tasks (excluding tombstoned)",
		action: Effect.gen(function* () {
			const allTasks = yield* bc.board.getTasks()
			// Exclude tombstoned (deleted) tasks only - closed tasks are included
			const selectableIds = allTasks.filter((t) => t.status !== "tombstone").map((t) => t.id)
			yield* bc.editor.selectAll(selectableIds)
			yield* bc.toast.show("info", `Selected ${selectableIds.length} tasks`)
		}),
	},
	{
		key: "space",
		mode: "normal",
		description: "Enter action mode",
		// Capture task ID at Space press time to prevent race conditions
		// where cursor moves between Space and action key (fixes az-f3iw)
		action: Effect.gen(function* () {
			const taskId = yield* bc.nav.getFocusedTaskId()
			yield* bc.editor.enterAction(taskId)
		}),
	},
	{
		key: "/",
		mode: "normal",
		description: "Enter search mode",
		action: bc.editor.enterSearch(),
	},
	{
		key: ",",
		mode: "normal",
		description: "Enter sort mode",
		action: bc.editor.enterSort(),
	},
	{
		key: "f",
		mode: "normal",
		description: "Enter filter mode",
		action: bc.editor.enterFilter(),
	},

	// ========================================================================
	// Normal Mode - Actions
	// ========================================================================
	{
		key: "q",
		mode: "normal",
		description: "Quit (or exit drill-down)",
		action: Effect.gen(function* () {
			// If in drill-down mode, exit it instead of quitting
			const inDrillDown = yield* bc.nav.isInDrillDown()
			if (inDrillDown) {
				yield* bc.nav.exitDrillDown()
				return
			}

			// Check if any operations are running
			const busy = yield* bc.helpers.isAnyBusy()

			if (busy) {
				// Get running operation labels for the toast message
				const labels = yield* bc.helpers.getRunningOperationLabels()
				const labelStr = labels.length > 0 ? labels.join(", ") : "operation"
				yield* bc.toast.show("warning", `Cannot quit: ${labelStr} in progress`)
				return
			}

			process.exit(0)
		}),
	},
	{
		key: "?",
		mode: "normal",
		description: "Show help",
		action: bc.overlay.push({ _tag: "help" }),
	},
	{
		key: "d",
		mode: "normal",
		description: "Show diagnostics",
		action: bc.overlay.push({ _tag: "diagnostics" }),
	},
	{
		key: "s",
		mode: "normal",
		description: "Show settings",
		action: Effect.gen(function* () {
			yield* bc.overlay.push({ _tag: "settings" })
			yield* bc.settings.open()
		}),
	},
	{
		key: "return",
		mode: "normal",
		description: "View detail (or enter epic)",
		action: Effect.gen(function* () {
			// Get selected task to check if it's an epic
			const task = yield* bc.helpers.getSelectedTask()
			if (task && task.issue_type === "epic") {
				// Fetch epic children (DependencyRef array)
				const children = yield* bc.beadsClient
					.getEpicChildren(task.id)
					.pipe(Effect.catchAll(() => Effect.succeed([])))
				const childIds = new Set(children.map((c: { id: string }) => c.id))

				// Fetch full Issue objects for each child (needed for phase computation)
				// Parallel fetch with error tolerance
				const childDetailResults = yield* Effect.all(
					children.map((child: { id: string }) =>
						bc.beadsClient
							.show(child.id)
							.pipe(Effect.map((issue) => [child.id, issue] as const))
							.pipe(Effect.catchAll(() => Effect.succeed(null))),
					),
					{ concurrency: "unbounded" },
				)

				// Build map from successful fetches
				const childDetails = new Map<string, Issue>()
				for (const result of childDetailResults) {
					if (result !== null) {
						childDetails.set(result[0], result[1])
					}
				}

				// Enter drill-down mode for the epic with children and details
				yield* bc.nav.enterDrillDown(task.id, childIds, childDetails)
			} else {
				// Normal detail view for non-epics
				yield* bc.helpers.openCurrentDetail()
			}
		}),
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
		action: bc.overlay.push({ _tag: "claudeCreate" }),
	},
	{
		key: "tab",
		mode: "normal",
		description: "Toggle view mode (kanban/compact)",
		action: bc.viewService.toggleViewMode(),
	},
	{
		key: "r",
		mode: "normal",
		description: "Refresh git stats",
		action: bc.board.refreshGitStats(),
	},
	{
		key: "p",
		mode: "normal",
		description: "Open planning workflow",
		action: bc.overlay.push({ _tag: "planning" }),
	},
	{
		key: "S-l",
		mode: "normal",
		description: "View logs in tmux popup",
		action: Effect.gen(function* () {
			const projectPath = yield* bc.helpers.getProjectPath()
			const logFile = `${projectPath}/az.log`
			// Shell wrapper providing menu with view/edit/quit options
			const wrapperScript = `
while true; do
  clear
  echo ""
  echo "  az.log"
  echo ""
  echo "  [v] View logs (less +F)"
  echo "  [e] Edit in \\$EDITOR"
  echo "  [q] Quit"
  echo ""
  read -rsn1 key
  case "$key" in
    v|V|"") less +F "${logFile}" ;;
    e|E) \${EDITOR:-\${VISUAL:-vim}} "${logFile}"; exit ;;
    q|Q) exit ;;
  esac
done
`
			yield* bc.tmux.displayPopup({
				command: `bash -c '${wrapperScript.replace(/'/g, "'\\''")}'`,
				width: "90%",
				height: "90%",
				title: " az.log ",
				cwd: projectPath,
			})
		}).pipe(Effect.catchAll(Effect.logError)),
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
			bc.editor.exitToNormal().pipe(Effect.tap(() => bc.sessionHandlers.startSession())),
		),
	},
	{
		key: "S-s",
		mode: "action",
		description: "Start+work (prompt Claude)",
		action: Effect.suspend(() =>
			bc.editor.exitToNormal().pipe(Effect.tap(() => bc.sessionHandlers.startSessionWithPrompt())),
		),
	},
	{
		key: "!",
		mode: "action",
		description: "Start+work (skip permissions)",
		action: Effect.suspend(() =>
			bc.editor.exitToNormal().pipe(Effect.tap(() => bc.sessionHandlers.startSessionDangerous())),
		),
	},
	{
		key: "c",
		mode: "action",
		description: "Chat (Haiku)",
		action: Effect.suspend(() =>
			bc.editor.exitToNormal().pipe(Effect.tap(() => bc.sessionHandlers.chatAboutTask())),
		),
	},
	{
		key: "a",
		mode: "action",
		description: "Attach to session",
		action: Effect.suspend(() =>
			bc.editor.exitToNormal().pipe(Effect.tap(() => bc.sessionHandlers.attachExternal())),
		),
	},
	{
		key: "S-a",
		mode: "action",
		description: "Attach inline",
		action: Effect.suspend(() =>
			bc.editor.exitToNormal().pipe(Effect.tap(() => bc.sessionHandlers.attachInline())),
		),
	},
	{
		key: "p",
		mode: "action",
		description: "Pause session",
		action: Effect.suspend(() =>
			bc.editor.exitToNormal().pipe(Effect.tap(() => bc.sessionHandlers.pauseSession())),
		),
	},
	{
		key: "r",
		mode: "action",
		description: "Toggle dev server",
		action: Effect.suspend(() =>
			bc.editor.exitToNormal().pipe(
				Effect.tap(() => bc.devServerHandlers.toggleDevServer()),
				Effect.catchAll((e) =>
					Effect.gen(function* () {
						yield* Effect.logError("Dev server toggle failed", e)
						yield* bc.toast.show("error", `Dev server error: ${String(e)}`)
					}),
				),
			),
		),
	},
	{
		key: "C-r",
		mode: "action",
		description: "Restart dev server",
		action: Effect.suspend(() =>
			bc.editor.exitToNormal().pipe(
				Effect.tap(() => bc.devServerHandlers.restartDevServer()),
				Effect.catchAll((e) =>
					Effect.gen(function* () {
						yield* Effect.logError("Dev server restart failed", e)
						yield* bc.toast.show("error", `Dev server error: ${String(e)}`)
					}),
				),
			),
		),
	},
	{
		key: "S-r",
		mode: "action",
		description: "Resume session",
		action: Effect.suspend(() =>
			bc.editor.exitToNormal().pipe(Effect.tap(() => bc.sessionHandlers.resumeSession())),
		),
	},
	{
		key: "x",
		mode: "action",
		description: "Stop session",
		action: Effect.suspend(() =>
			bc.editor.exitToNormal().pipe(Effect.tap(() => bc.sessionHandlers.stopSession())),
		),
	},
	{
		key: "e",
		mode: "action",
		description: "Edit bead ($EDITOR)",
		action: Effect.suspend(() =>
			bc.editor.exitToNormal().pipe(Effect.tap(() => bc.taskHandlers.editBead())),
		),
	},
	{
		key: "S-e",
		mode: "action",
		description: "Edit bead (Claude)",
		action: Effect.suspend(() =>
			bc.editor
				.exitToNormal()
				.pipe(
					Effect.tap(() =>
						bc.toast.show("error", "Claude edit not yet implemented - use 'e' for $EDITOR"),
					),
				),
		),
	},
	{
		key: "S-p",
		mode: "action",
		description: "Create PR",
		action: Effect.suspend(() =>
			bc.editor.exitToNormal().pipe(Effect.tap(() => bc.prHandlers.createPR())),
		),
	},
	{
		key: "d",
		mode: "action",
		description: "Cleanup worktree",
		action: Effect.suspend(() =>
			bc.editor.exitToNormal().pipe(Effect.tap(() => bc.prHandlers.cleanup())),
		),
	},
	{
		key: "m",
		mode: "action",
		description: "Merge",
		action: Effect.suspend(() =>
			bc.editor.exitToNormal().pipe(Effect.tap(() => bc.prHandlers.merge())),
		),
	},
	{
		key: "S-m",
		mode: "action",
		description: "Abort merge",
		action: Effect.suspend(() =>
			bc.editor.exitToNormal().pipe(Effect.tap(() => bc.prHandlers.abortMerge())),
		),
	},
	{
		key: "f",
		mode: "action",
		description: "Diff menu",
		action: Effect.suspend(() =>
			bc.editor.exitToNormal().pipe(Effect.tap(() => bc.prHandlers.showDiff())),
		),
	},
	{
		key: "u",
		mode: "action",
		description: "Update from main",
		action: Effect.suspend(() =>
			bc.editor.exitToNormal().pipe(Effect.tap(() => bc.prHandlers.updateFromBase())),
		),
	},
	{
		key: "S-d",
		mode: "action",
		description: "Delete bead",
		action: Effect.suspend(() =>
			bc.editor.exitToNormal().pipe(Effect.tap(() => bc.taskHandlers.deleteBead())),
		),
	},
	{
		key: "i",
		mode: "action",
		description: "Attach image",
		action: Effect.gen(function* () {
			const task = yield* bc.helpers.getActionTargetTask()
			yield* bc.editor.exitToNormal()
			if (task) {
				yield* bc.overlay.push({ _tag: "imageAttach", taskId: task.id })
			}
		}),
	},
	{
		key: "S-h",
		mode: "action",
		description: "Open Helix editor",
		action: Effect.suspend(() =>
			bc.editor.exitToNormal().pipe(Effect.tap(() => bc.sessionHandlers.startHelixSession())),
		),
	},
	{
		key: "b",
		mode: "action",
		description: "Merge bead into...",
		action: Effect.suspend(() => bc.prHandlers.enterMergeSelect()),
	},

	// ========================================================================
	// Goto-Pending Mode (after pressing 'g')
	// ========================================================================
	{
		key: "g",
		mode: "goto-pending",
		description: "Go to top of column",
		action: bc.nav.goToFirst().pipe(Effect.tap(() => bc.editor.exitToNormal())),
	},
	{
		key: "e",
		mode: "goto-pending",
		description: "Go to bottom of column",
		action: bc.nav.goToLast().pipe(Effect.tap(() => bc.editor.exitToNormal())),
	},
	{
		key: "h",
		mode: "goto-pending",
		description: "Go to first column",
		action: bc.nav.goToFirstColumn().pipe(Effect.tap(() => bc.editor.exitToNormal())),
	},
	{
		key: "l",
		mode: "goto-pending",
		description: "Go to last column",
		action: bc.nav.goToLastColumn().pipe(Effect.tap(() => bc.editor.exitToNormal())),
	},
	{
		key: "w",
		mode: "goto-pending",
		description: "Enter jump mode",
		action: Effect.gen(function* () {
			const labels = yield* bc.inputHandlers.computeJumpLabels()
			yield* bc.editor.enterJump(labels)
		}),
	},
	{
		key: "p",
		mode: "goto-pending",
		description: "Open project selector",
		action: bc.overlay
			.push({ _tag: "projectSelector" })
			.pipe(Effect.tap(() => bc.editor.exitToNormal())),
	},

	// ========================================================================
	// Select Mode (navigation handled by BOARD_NAV_MODES)
	// ========================================================================
	{
		key: "space",
		mode: "select",
		description: "Enter action mode",
		// Same as normal mode: capture task ID and enter action mode
		// Actions will operate on all selected tasks via getActionTargetTasks()
		action: Effect.gen(function* () {
			const taskId = yield* bc.nav.getFocusedTaskId()
			yield* bc.editor.enterAction(taskId)
		}),
	},
	{
		key: "a",
		mode: "select",
		description: "Toggle selection",
		action: Effect.suspend(() => bc.helpers.toggleCurrentSelection()),
	},
	{
		key: "5",
		mode: "select",
		description: "Toggle selection (alt)",
		action: Effect.suspend(() => bc.helpers.toggleCurrentSelection()),
	},
	{
		key: "v",
		mode: "select",
		description: "Exit select mode",
		action: bc.editor.exitSelect(),
	},
	{
		key: "S-5",
		mode: "select",
		description: "Select all tasks (excluding tombstoned)",
		action: Effect.gen(function* () {
			const allTasks = yield* bc.board.getTasks()
			// Exclude tombstoned (deleted) tasks only - closed tasks are included
			const selectableIds = allTasks.filter((t) => t.status !== "tombstone").map((t) => t.id)
			yield* bc.editor.selectAll(selectableIds)
			yield* bc.toast.show("info", `Selected ${selectableIds.length} tasks`)
		}),
	},
	{
		key: "S-a",
		mode: "select",
		description: "Select all in column",
		action: Effect.gen(function* () {
			// Get current position and filtered tasks
			const pos = yield* bc.nav.getPosition()
			const mode = yield* bc.editor.getMode()
			const sortConfig = yield* bc.editor.getSortConfig()
			const filterConfig = yield* bc.editor.getFilterConfig()
			const searchQuery = mode._tag === "search" ? mode.query : ""

			const tasksByColumn = yield* bc.board.getFilteredTasksByColumn(
				searchQuery,
				sortConfig,
				filterConfig,
			)

			const columnTasks = tasksByColumn[pos.columnIndex] ?? []
			// Exclude tombstoned tasks, add to current selection
			const selectableIds = columnTasks.filter((t) => t.status !== "tombstone").map((t) => t.id)
			yield* bc.editor.addToSelection(selectableIds)
			yield* bc.toast.show("info", `Added ${selectableIds.length} tasks to selection`)
		}),
	},

	// ========================================================================
	// Sort Mode
	// ========================================================================
	{
		key: "s",
		mode: "sort",
		description: "Sort by session status",
		action: bc.editor.cycleSort("session").pipe(
			Effect.tap(() => bc.editor.exitToNormal()),
			Effect.catchAll(Effect.logError),
		),
	},
	{
		key: "p",
		mode: "sort",
		description: "Sort by priority",
		action: bc.editor.cycleSort("priority").pipe(
			Effect.tap(() => bc.editor.exitToNormal()),
			Effect.catchAll(Effect.logError),
		),
	},
	{
		key: "u",
		mode: "sort",
		description: "Sort by updated at",
		action: bc.editor.cycleSort("updated").pipe(
			Effect.tap(() => bc.editor.exitToNormal()),
			Effect.catchAll(Effect.logError),
		),
	},

	// ========================================================================
	// Filter Mode
	// ========================================================================
	// Sub-menu keys
	{
		key: "s",
		mode: "filter",
		description: "Status sub-menu",
		action: bc.editor.setActiveFilterField("status"),
	},
	{
		key: "p",
		mode: "filter",
		description: "Priority sub-menu",
		action: bc.editor.setActiveFilterField("priority"),
	},
	{
		key: "t",
		mode: "filter",
		description: "Type sub-menu",
		action: bc.editor.setActiveFilterField("type"),
	},
	{
		key: "S-s",
		mode: "filter",
		description: "Session sub-menu",
		action: bc.editor.setActiveFilterField("session"),
	},
	// Clear filters
	{
		key: "c",
		mode: "filter",
		description: "Clear all filters",
		action: bc.editor.clearFilters().pipe(Effect.tap(() => bc.editor.exitToNormal())),
	},
	// Priority toggles (0-4)
	{
		key: "0",
		mode: "filter",
		description: "Toggle P0 filter",
		action: bc.editor.toggleFilterPriority(0),
	},
	{
		key: "1",
		mode: "filter",
		description: "Toggle P1 filter",
		action: bc.editor.toggleFilterPriority(1),
	},
	{
		key: "2",
		mode: "filter",
		description: "Toggle P2 filter",
		action: bc.editor.toggleFilterPriority(2),
	},
	{
		key: "3",
		mode: "filter",
		description: "Toggle P3 filter",
		action: bc.editor.toggleFilterPriority(3),
	},
	{
		key: "4",
		mode: "filter",
		description: "Toggle P4 filter",
		action: bc.editor.toggleFilterPriority(4),
	},
	// Status toggles (o, i, b, d - first letter of each status except 'closed' uses 'd' for done)
	{
		key: "o",
		mode: "filter",
		description: "Toggle open status",
		action: bc.editor.toggleFilterStatus("open"),
	},
	{
		key: "i",
		mode: "filter",
		description: "Toggle in_progress status",
		action: bc.editor.toggleFilterStatus("in_progress"),
	},
	{
		key: "b",
		mode: "filter",
		description: "Toggle blocked status",
		action: bc.editor.toggleFilterStatus("blocked"),
	},
	{
		key: "d",
		mode: "filter",
		description: "Toggle closed status",
		action: bc.editor.toggleFilterStatus("closed"),
	},
	// Type toggles (B, F, T, E, C - uppercase to distinguish from status)
	{
		key: "S-b",
		mode: "filter",
		description: "Toggle bug type",
		action: bc.editor.toggleFilterType("bug"),
	},
	{
		key: "S-f",
		mode: "filter",
		description: "Toggle feature type",
		action: bc.editor.toggleFilterType("feature"),
	},
	{
		key: "S-t",
		mode: "filter",
		description: "Toggle task type",
		action: bc.editor.toggleFilterType("task"),
	},
	{
		key: "S-e",
		mode: "filter",
		description: "Toggle epic type",
		action: bc.editor.toggleFilterType("epic"),
	},
	{
		key: "S-c",
		mode: "filter",
		description: "Toggle chore type",
		action: bc.editor.toggleFilterType("chore"),
	},
	// Session toggles (lowercase when session sub-menu is active)
	{
		key: "S-i",
		mode: "filter",
		description: "Toggle idle session",
		action: bc.editor.toggleFilterSession("idle"),
	},
	{
		key: "S-u",
		mode: "filter",
		description: "Toggle busy session",
		action: bc.editor.toggleFilterSession("busy"),
	},
	{
		key: "S-w",
		mode: "filter",
		description: "Toggle waiting session",
		action: bc.editor.toggleFilterSession("waiting"),
	},
	{
		key: "S-d",
		mode: "filter",
		description: "Toggle done session",
		action: bc.editor.toggleFilterSession("done"),
	},
	{
		key: "S-x",
		mode: "filter",
		description: "Toggle error session",
		action: bc.editor.toggleFilterSession("error"),
	},
	{
		key: "S-p",
		mode: "filter",
		description: "Toggle paused session",
		action: bc.editor.toggleFilterSession("paused"),
	},

	// --- Age Filter (filter mode, 'a' submenu) ---
	{
		key: "1",
		mode: "filter",
		description: "Filter to tasks >1 day old",
		action: Effect.gen(function* () {
			yield* bc.editor.setAgeFilter(1)
			yield* bc.toast.show("info", "Filtering to tasks >1 day old")
		}),
	},
	{
		key: "7",
		mode: "filter",
		description: "Filter to tasks >7 days old",
		action: Effect.gen(function* () {
			yield* bc.editor.setAgeFilter(7)
			yield* bc.toast.show("info", "Filtering to tasks >7 days old")
		}),
	},
	{
		key: "3",
		mode: "filter",
		description: "Filter to tasks >30 days old",
		action: Effect.gen(function* () {
			yield* bc.editor.setAgeFilter(30)
			yield* bc.toast.show("info", "Filtering to tasks >30 days old")
		}),
	},
	{
		key: "0",
		mode: "filter",
		description: "Clear age filter",
		action: Effect.gen(function* () {
			yield* bc.editor.setAgeFilter(null)
			yield* bc.toast.show("info", "Age filter cleared")
		}),
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
		action: bc.overlay.pop().pipe(Effect.asVoid),
	},

	// ========================================================================
	// Orchestrate Mode - Epic child task management
	// ========================================================================
	{
		key: "j",
		mode: "orchestrate",
		description: "Move down",
		action: bc.editor.orchestrateMoveDown(),
	},
	{
		key: "k",
		mode: "orchestrate",
		description: "Move up",
		action: bc.editor.orchestrateMoveUp(),
	},
	{
		key: "down",
		mode: "orchestrate",
		description: "Move down",
		action: bc.editor.orchestrateMoveDown(),
	},
	{
		key: "up",
		mode: "orchestrate",
		description: "Move up",
		action: bc.editor.orchestrateMoveUp(),
	},
	{
		key: "space",
		mode: "orchestrate",
		description: "Toggle task selection",
		action: Effect.suspend(() => {
			return Effect.gen(function* () {
				const mode = yield* bc.editor.getMode()
				if (mode._tag !== "orchestrate") return
				const task = mode.childTasks[mode.focusIndex]
				if (task) {
					yield* bc.editor.orchestrateToggle(task.id)
				}
			})
		}),
	},
	{
		key: "a",
		mode: "orchestrate",
		description: "Select all spawnable tasks",
		action: bc.editor.orchestrateSelectAll(),
	},
	{
		key: "A",
		mode: "orchestrate",
		description: "Clear all selections",
		action: bc.editor.orchestrateSelectNone(),
	},
	{
		key: "return",
		mode: "orchestrate",
		description: "Confirm spawn selected tasks",
		action: bc.orchestrateHandlers.confirmSpawn(),
	},
	{
		key: "escape",
		mode: "orchestrate",
		description: "Exit orchestrate mode",
		action: bc.editor.exitOrchestrate(),
	},
	{
		key: "o",
		mode: "overlay",
		description: "Orchestrate epic (from detail)",
		action: bc.orchestrateHandlers
			.enterFromDetail()
			.pipe(Effect.catchAll(Effect.logError), Effect.asVoid),
	},

	// ========================================================================
	// Merge Select Mode (navigation handled by BOARD_NAV_MODES)
	// ========================================================================
	{
		key: "space",
		mode: "mergeSelect",
		description: "Confirm merge",
		action: Effect.suspend(() => bc.prHandlers.confirmMergeSelect()),
	},
	{
		key: "return",
		mode: "mergeSelect",
		description: "Confirm merge",
		action: Effect.suspend(() => bc.prHandlers.confirmMergeSelect()),
	},
	{
		key: "escape",
		mode: "mergeSelect",
		description: "Cancel",
		action: Effect.suspend(() => bc.prHandlers.cancelMergeSelect()),
	},
]
