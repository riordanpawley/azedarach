import { Effect } from "effect"
import type { Issue } from "../../contracts.js"
import { requestShutdown } from "../../lib/runtimeControl.js"
import type { TmuxCapabilities } from "../../contracts.js"
import type { TuiBoardStoreService } from "../TuiBoardStoreService.js"
import type { EditorService } from "../EditorService.js"
import type { NavigationService } from "../NavigationService.js"
import type { OverlayService } from "../OverlayService.js"
import type { SettingsService } from "../SettingsService.js"
import type { ToastService } from "../ToastService.js"
import type { TmuxService } from "../TmuxService.js"
import type { TuiIssueAdapterService } from "../TuiIssueAdapterService.js"
import type { ViewService } from "../ViewService.js"
import type { DevServerHandlersService } from "./DevServerHandlersService.js"
import type { InputHandlersService } from "./InputHandlersService.js"
import type { KeyboardHelpersService } from "./KeyboardHelpersService.js"
import type { OrchestrateHandlersService } from "./OrchestrateHandlersService.js"
import type { PRHandlersService } from "./PRHandlersService.js"
import type { SessionHandlersService } from "./SessionHandlersService.js"
import type { TaskHandlersService } from "./TaskHandlersService.js"
import type { Keybinding } from "./types.js"

export interface BindingContext {
	sessionHandlers: SessionHandlersService
	taskHandlers: TaskHandlersService
	prHandlers: PRHandlersService
	inputHandlers: InputHandlersService
	orchestrateHandlers: OrchestrateHandlersService
	devServerHandlers: DevServerHandlersService
	helpers: KeyboardHelpersService
	issueAdapter: TuiIssueAdapterService
	nav: NavigationService
	editor: EditorService
	overlay: OverlayService
	settings: SettingsService
	toast: ToastService
	viewService: ViewService
	tmux: TmuxService
	tmuxCapabilities: TmuxCapabilities
	board: TuiBoardStoreService
}

const BOARD_NAV_MODES = ["normal", "select", "mergeSelect"] as const

const tmuxUnavailableMessage = (label: string): string => `${label} is unavailable outside tmux`

const guardTmuxAction = <E, R>(
	bc: BindingContext,
	label: string,
	action: Effect.Effect<void, E, R>,
): Effect.Effect<void, E, R> =>
	Effect.gen(function* () {
		if (!bc.tmuxCapabilities.tmuxActionsEnabled) {
			yield* bc.toast.show("info", tmuxUnavailableMessage(label))
			return
		}
		yield* action
	})

export const createDefaultBindings = (bc: BindingContext): ReadonlyArray<Keybinding> => [
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
			const selectableIds = allTasks.filter((task) => task.status !== "tombstone").map((task) => task.id)
			yield* bc.editor.selectAll(selectableIds)
			yield* bc.toast.show("info", `Selected ${selectableIds.length} tasks`)
		}),
	},
	{
		key: "space",
		mode: "normal",
		description: "Enter action mode",
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
	{
		key: "q",
		mode: "normal",
		description: "Quit (or exit drill-down)",
		action: Effect.gen(function* () {
			const inDrillDown = yield* bc.nav.isInDrillDown()
			if (inDrillDown) {
				yield* bc.nav.exitDrillDown()
				return
			}
			yield* Effect.sync(requestShutdown)
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
			const task = yield* bc.helpers.getSelectedTask()
			if (task && task.issue_type === "epic") {
				const children = yield* bc.issueAdapter.getEpicChildren(task.id).pipe(
					Effect.catchAll((error) =>
						Effect.logWarning(`Failed to load epic children: ${String(error)}`).pipe(
							Effect.zipRight(Effect.succeed([])),
						),
					),
				)
				const childIds = new Set(children.map((child) => child.id))
				const childDetailResults = yield* Effect.all(
					children.map((child) =>
						bc.issueAdapter.show(child.id).pipe(
							Effect.map((issue) => [child.id, issue] as const),
							Effect.catchAll((error) =>
								Effect.logWarning(`Failed to load child issue ${child.id}: ${String(error)}`).pipe(
									Effect.zipRight(Effect.succeed(null)),
								),
							),
						),
					),
					{ concurrency: "unbounded" },
				)

				const childDetails = new Map<string, Issue>()
				for (const result of childDetailResults) {
					if (result !== null) {
						childDetails.set(result[0], result[1])
					}
				}

				yield* bc.nav.enterDrillDown(task.id, childIds, childDetails)
				return
			}
			yield* bc.helpers.openCurrentDetail()
		}),
	},
	{
		key: "c",
		mode: "normal",
		description: "Create bead via $EDITOR",
		action: Effect.suspend(() => bc.taskHandlers.createIssue()),
	},
	{
		key: "S-c",
		mode: "normal",
		description: "Create bead via AI",
		action: bc.overlay.push({ _tag: "aiCreate" }),
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
		action: bc.board.refreshGitStats().pipe(
			Effect.catchAll((error) =>
				Effect.logError(`Failed to refresh git stats: ${String(error)}`).pipe(Effect.zipRight(Effect.void)),
			),
		),
	},
	{
		key: "S-r",
		mode: "normal",
		description: "Recover crashed session",
		action: Effect.suspend(() =>
			guardTmuxAction(bc, "Recover crashed session", bc.sessionHandlers.recoverCrashedSession()),
		),
	},
	{
		key: "C-r",
		mode: "normal",
		description: "Recover all crashed sessions",
		action: Effect.suspend(() =>
			guardTmuxAction(
				bc,
				"Recover crashed sessions",
				bc.sessionHandlers.recoverAllCrashedSessions(),
			),
		),
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
		action: guardTmuxAction(
			bc,
			"Log popup",
			Effect.gen(function* () {
				const projectPath = yield* bc.helpers.getProjectPath()
				const logFile = `${projectPath}/az.log`
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
		),
	},
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
			guardTmuxAction(
				bc,
				"Start session",
				bc.editor.exitToNormal().pipe(Effect.tap(() => bc.sessionHandlers.startSession())),
			),
		),
	},
	{
		key: "S-s",
		mode: "action",
		description: "Start+work (prompt AI)",
		action: Effect.suspend(() =>
			guardTmuxAction(
				bc,
				"Start session with prompt",
				bc.editor
					.exitToNormal()
					.pipe(Effect.tap(() => bc.sessionHandlers.startSessionWithPrompt())),
			),
		),
	},
	{
		key: "S-q",
		mode: "action",
		description: "Start+work (question-first)",
		action: Effect.suspend(() =>
			guardTmuxAction(
				bc,
				"Start session with question-first prompt",
				bc.editor
					.exitToNormal()
					.pipe(Effect.tap(() => bc.sessionHandlers.startSessionQuestionFirst())),
			),
		),
	},
	{
		key: "!",
		mode: "action",
		description: "Start+work (skip permissions)",
		action: Effect.suspend(() =>
			guardTmuxAction(
				bc,
				"Start session (skip permissions)",
				bc.editor.exitToNormal().pipe(Effect.tap(() => bc.sessionHandlers.startSessionDangerous())),
			),
		),
	},
	{
		key: "a",
		mode: "action",
		description: "Attach to session",
		action: Effect.suspend(() =>
			guardTmuxAction(
				bc,
				"Attach to session",
				bc.editor.exitToNormal().pipe(Effect.tap(() => bc.sessionHandlers.attachExternal())),
			),
		),
	},
	{
		key: "S-a",
		mode: "action",
		description: "Attach inline",
		action: Effect.suspend(() =>
			guardTmuxAction(
				bc,
				"Attach inline",
				bc.editor.exitToNormal().pipe(Effect.tap(() => bc.sessionHandlers.attachInline())),
			),
		),
	},
	{
		key: "p",
		mode: "action",
		description: "Pause session",
		action: Effect.suspend(() =>
			guardTmuxAction(
				bc,
				"Pause session",
				bc.editor.exitToNormal().pipe(Effect.tap(() => bc.sessionHandlers.pauseSession())),
			),
		),
	},
	{
		key: "r",
		mode: "action",
		description: "Toggle dev server",
		action: Effect.suspend(() =>
			guardTmuxAction(
				bc,
				"Dev server toggle",
				bc.editor.exitToNormal().pipe(
					Effect.tap(() => bc.devServerHandlers.toggleDevServer()),
					Effect.catchAll((error) =>
						Effect.gen(function* () {
							yield* Effect.logError("Dev server toggle failed", error)
							yield* bc.toast.show("error", `Dev server error: ${String(error)}`)
						}),
					),
				),
			),
		),
	},
	{
		key: "C-r",
		mode: "action",
		description: "Restart dev server",
		action: Effect.suspend(() =>
			guardTmuxAction(
				bc,
				"Dev server restart",
				bc.editor.exitToNormal().pipe(
					Effect.tap(() => bc.devServerHandlers.restartDevServer()),
					Effect.catchAll((error) =>
						Effect.gen(function* () {
							yield* Effect.logError("Dev server restart failed", error)
							yield* bc.toast.show("error", `Dev server error: ${String(error)}`)
						}),
					),
				),
			),
		),
	},
	{
		key: "S-r",
		mode: "action",
		description: "Resume session",
		action: Effect.suspend(() =>
			guardTmuxAction(
				bc,
				"Resume session",
				bc.editor.exitToNormal().pipe(Effect.tap(() => bc.sessionHandlers.resumeSession())),
			),
		),
	},
	{
		key: "x",
		mode: "action",
		description: "Stop session",
		action: Effect.suspend(() =>
			guardTmuxAction(
				bc,
				"Stop session",
				bc.editor.exitToNormal().pipe(Effect.tap(() => bc.sessionHandlers.stopSession())),
			),
		),
	},
	{
		key: "e",
		mode: "action",
		description: "Edit bead ($EDITOR)",
		action: Effect.suspend(() =>
			bc.editor.exitToNormal().pipe(Effect.tap(() => bc.taskHandlers.editIssue())),
		),
	},
	{
		key: "S-e",
		mode: "action",
		description: "Edit bead (AI)",
		action: Effect.suspend(() =>
			bc.editor
				.exitToNormal()
				.pipe(
					Effect.tap(() =>
						bc.toast.show("error", "AI edit not yet implemented - use 'e' for $EDITOR"),
					),
				),
		),
	},
	{
		key: "S-f",
		mode: "action",
		description: "Fork bead",
		action: Effect.suspend(() =>
			bc.editor.exitToNormal().pipe(Effect.tap(() => bc.taskHandlers.forkIssue())),
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
		key: "S-o",
		mode: "action",
		description: "Open PR",
		action: Effect.suspend(() =>
			bc.editor.exitToNormal().pipe(Effect.tap(() => bc.prHandlers.openPR())),
		),
	},
	{
		key: "d",
		mode: "action",
		description: "Cleanup worktree + branch",
		action: Effect.suspend(() => bc.prHandlers.cleanup().pipe(Effect.ensuring(bc.editor.exitToNormal()))),
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
		description: "Delete bead + cleanup",
		action: Effect.suspend(() =>
			bc.taskHandlers.deleteIssue().pipe(Effect.ensuring(bc.editor.exitToNormal())),
		),
	},
	{
		key: "S-t",
		mode: "action",
		description: "Tombstone bead",
		action: Effect.suspend(() =>
			bc.taskHandlers.tombstoneIssue().pipe(Effect.ensuring(bc.editor.exitToNormal())),
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
			guardTmuxAction(
				bc,
				"Helix session",
				bc.editor.exitToNormal().pipe(Effect.tap(() => bc.sessionHandlers.startHelixSession())),
			),
		),
	},
	{
		key: "b",
		mode: "action",
		description: "Merge bead into...",
		action: Effect.suspend(() => bc.prHandlers.enterMergeSelect()),
	},
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
		key: "s",
		mode: "goto-pending",
		description: "Enter spec workspace",
		action: Effect.suspend(() => bc.inputHandlers.enterSpecWorkspace()),
	},
	{
		key: "p",
		mode: "goto-pending",
		description: "Open project selector",
		action: bc.overlay.push({ _tag: "projectSelector" }).pipe(Effect.tap(() => bc.editor.exitToNormal())),
	},
	{
		key: "S-w",
		mode: "goto-pending",
		description: "Open waiting session picker",
		action: guardTmuxAction(
			bc,
			"Waiting session picker",
			bc.overlay
				.push({ _tag: "waitingSessionPicker" })
				.pipe(Effect.tap(() => bc.editor.exitToNormal())),
		),
	},
	{
		key: "space",
		mode: "select",
		description: "Enter action mode",
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
		action: bc.editor.exitSelect(true),
	},
	{
		key: "q",
		mode: "select",
		description: "Exit select mode",
		action: bc.editor.exitSelect(true),
	},
	{
		key: "S-5",
		mode: "select",
		description: "Select all tasks (excluding tombstoned)",
		action: Effect.gen(function* () {
			const allTasks = yield* bc.board.getTasks()
			const selectableIds = allTasks.filter((task) => task.status !== "tombstone").map((task) => task.id)
			yield* bc.editor.selectAll(selectableIds)
			yield* bc.toast.show("info", `Selected ${selectableIds.length} tasks`)
		}),
	},
	{
		key: "S-a",
		mode: "select",
		description: "Select all in column",
		action: Effect.gen(function* () {
			const pos = yield* bc.nav.getPosition()
			const mode = yield* bc.editor.getMode()
			const sortConfig = yield* bc.editor.getSortConfig()
			const filterConfig = yield* bc.editor.getFilterConfig()
			const searchQuery = mode._tag === "search" ? mode.query : ""
			const tasksByColumn = yield* bc.board.getFilteredTasksByColumn(searchQuery, sortConfig, filterConfig)
			const columnTasks = tasksByColumn[pos.columnIndex] ?? []
			const selectableIds = columnTasks
				.filter((task) => task.status !== "tombstone")
				.map((task) => task.id)
			yield* bc.editor.addToSelection(selectableIds)
			yield* bc.toast.show("info", `Added ${selectableIds.length} tasks to selection`)
		}),
	},
	{
		key: "s",
		mode: "sort",
		description: "Sort by session status",
		action: bc.editor
			.cycleSort("session")
			.pipe(Effect.tap(() => bc.editor.exitToNormal()), Effect.catchAll(Effect.logError)),
	},
	{
		key: "p",
		mode: "sort",
		description: "Sort by priority",
		action: bc.editor
			.cycleSort("priority")
			.pipe(Effect.tap(() => bc.editor.exitToNormal()), Effect.catchAll(Effect.logError)),
	},
	{
		key: "u",
		mode: "sort",
		description: "Sort by updated at",
		action: bc.editor
			.cycleSort("updated")
			.pipe(Effect.tap(() => bc.editor.exitToNormal()), Effect.catchAll(Effect.logError)),
	},
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
	{
		key: "c",
		mode: "filter",
		description: "Clear all filters",
		action: bc.editor.clearFilters().pipe(Effect.tap(() => bc.editor.exitToNormal())),
	},
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
	{
		key: "escape",
		mode: "*",
		description: "Exit/cancel",
		action: Effect.suspend(() => bc.inputHandlers.handleEscape()),
	},
	{
		key: "q",
		mode: ["action", "goto-pending", "sort", "filter", "spec"],
		description: "Exit/cancel",
		action: Effect.suspend(() => bc.inputHandlers.handleEscape()),
	},
	{
		key: "tab",
		mode: "spec",
		description: "Cycle spec subview",
		action: bc.editor.cycleSpecSubview(),
	},
	{
		key: "[",
		mode: "spec",
		description: "Previous parity implementation",
		action: Effect.gen(function* () {
			const subview = yield* bc.editor.getSpecSubview()
			if (subview === "parity") {
				yield* bc.editor.cycleSpecImplementation("previous")
			}
		}),
	},
	{
		key: "]",
		mode: "spec",
		description: "Next parity implementation",
		action: Effect.gen(function* () {
			const subview = yield* bc.editor.getSpecSubview()
			if (subview === "parity") {
				yield* bc.editor.cycleSpecImplementation("next")
			}
		}),
	},
	{
		key: "escape",
		mode: "overlay",
		description: "Close overlay",
		action: bc.overlay.pop().pipe(Effect.asVoid),
	},
	{
		key: "q",
		mode: "overlay",
		description: "Close help overlay",
		action: Effect.gen(function* () {
			const currentOverlay = yield* bc.overlay.current()
			if (currentOverlay?._tag === "help") {
				yield* bc.overlay.pop()
			}
		}),
	},
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
		action: Effect.suspend(() =>
			Effect.gen(function* () {
				const mode = yield* bc.editor.getMode()
				if (mode._tag !== "orchestrate") return
				const task = mode.childTasks[mode.focusIndex]
				if (task) {
					yield* bc.editor.orchestrateToggle(task.id)
				}
			}),
		),
	},
	{
		key: "a",
		mode: "orchestrate",
		description: "Select all spawnable tasks",
		action: bc.editor.orchestrateSelectAll(),
	},
	{
		key: "n",
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
		key: "q",
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
	{
		key: "q",
		mode: "mergeSelect",
		description: "Cancel",
		action: Effect.suspend(() => bc.prHandlers.cancelMergeSelect()),
	},
]
