import {
	DaemonRpcClient,
	type DaemonRpcClientApi,
	type DaemonRpcClientError,
	type TrackedIssue,
} from "@azedarach/shared/rpc"
import { Effect, Stream, SubscriptionRef } from "effect"
import type { Issue, TaskPhaseInfo } from "../contracts.js"
import type { TaskWithSession } from "../types.js"
import { computeDependencyPhases } from "../utils/dependencyPhases.js"
import { DiagnosticsService } from "./DiagnosticsService.js"
import { EditorService } from "./EditorService.js"
import { TuiBoardStoreService } from "./TuiBoardStoreService.js"

export type Direction = "up" | "down" | "left" | "right"

export interface PerProjectNavigationState {
	readonly focusedTaskId: string | null
	readonly followTaskId: string | null
	readonly drillDownEpic: string | null
	readonly drillDownChildIds: ReadonlySet<string>
	readonly drillDownChildDetails: ReadonlyMap<string, Issue>
	readonly savedFocusedTaskId: string | null
}

export interface Position {
	readonly columnIndex: number
	readonly taskIndex: number
}

const EMPTY_CHILD_IDS: ReadonlySet<string> = new Set()
const EMPTY_CHILD_DETAILS: ReadonlyMap<string, Issue> = new Map()

const toNavigationIssue = (issue: TrackedIssue): Issue => ({
	id: issue.id,
	title: issue.title,
	description: issue.description,
	status: issue.status,
	priority: issue.priority,
	issue_type: issue.issue_type,
	created_at: issue.created_at,
	updated_at: issue.updated_at,
	closed_at: issue.closed_at,
	assignee: issue.assignee,
	labels: issue.labels,
	design: issue.design,
	notes: issue.notes,
	acceptance: issue.acceptance,
	estimate: issue.estimate,
	implementations: issue.implementations,
	dependent_count: issue.dependent_count,
	dependency_count: issue.dependency_count,
	dependents: issue.dependents,
	dependencies: issue.dependencies,
})

const loadDrillDownStateFromDaemon = (
	daemonRpcClient: DaemonRpcClientApi,
	epicId: string,
	projectPath: string | null,
): Effect.Effect<
	{
		readonly childIds: ReadonlySet<string>
		readonly childDetails: ReadonlyMap<string, Issue>
	},
	DaemonRpcClientError
> =>
	Effect.gen(function* () {
		const result = yield* daemonRpcClient.issueList({
			filters: { parent: epicId },
			projectPath: projectPath ?? undefined,
		})

		const childIds = new Set<string>()
		const childDetails = new Map<string, Issue>()
		for (const issue of result.issues) {
			childIds.add(issue.id)
			childDetails.set(issue.id, toNavigationIssue(issue))
		}

		return { childIds, childDetails }
	})

export class NavigationService extends Effect.Service<NavigationService>()("NavigationService", {
	dependencies: [DiagnosticsService.Default, EditorService.Default],
	scoped: Effect.gen(function* () {
		const daemonRpcClient = yield* DaemonRpcClient
		const board = yield* TuiBoardStoreService
		const diagnostics = yield* DiagnosticsService
		const editor = yield* EditorService

		yield* diagnostics.trackService(
			"NavigationService",
			"TUI cursor navigation and drill-down state",
		)

		const focusedTaskId = yield* SubscriptionRef.make<string | null>(null)
		const followTaskId = yield* SubscriptionRef.make<string | null>(null)
		const drillDownEpic = yield* SubscriptionRef.make<string | null>(null)
		const drillDownChildIds = yield* SubscriptionRef.make<ReadonlySet<string>>(EMPTY_CHILD_IDS)
		const drillDownChildDetails =
			yield* SubscriptionRef.make<ReadonlyMap<string, Issue>>(EMPTY_CHILD_DETAILS)
		const savedFocusedTaskId = yield* SubscriptionRef.make<string | null>(null)
		const perProjectState = yield* SubscriptionRef.make<Map<string, PerProjectNavigationState>>(
			new Map(),
		)
		const currentProjectPath = yield* SubscriptionRef.make<string | null>(null)

		const emptyProjectState = (): PerProjectNavigationState => ({
			focusedTaskId: null,
			followTaskId: null,
			drillDownEpic: null,
			drillDownChildIds: EMPTY_CHILD_IDS,
			drillDownChildDetails: EMPTY_CHILD_DETAILS,
			savedFocusedTaskId: null,
		})

		const getOrCreateProjectState = (projectPath: string) =>
			Effect.gen(function* () {
				const stateMap = yield* SubscriptionRef.get(perProjectState)
				const existing = stateMap.get(projectPath)
				if (existing !== undefined) {
					return existing
				}
				const nextState = emptyProjectState()
				yield* SubscriptionRef.update(perProjectState, (current) => {
					const next = new Map(current)
					next.set(projectPath, nextState)
					return next
				})
				return nextState
			})

		const saveCurrentToMap = () =>
			Effect.gen(function* () {
				const path = yield* SubscriptionRef.get(currentProjectPath)
				if (path === null) {
					return
				}
				const state: PerProjectNavigationState = {
					focusedTaskId: yield* SubscriptionRef.get(focusedTaskId),
					followTaskId: yield* SubscriptionRef.get(followTaskId),
					drillDownEpic: yield* SubscriptionRef.get(drillDownEpic),
					drillDownChildIds: yield* SubscriptionRef.get(drillDownChildIds),
					drillDownChildDetails: yield* SubscriptionRef.get(drillDownChildDetails),
					savedFocusedTaskId: yield* SubscriptionRef.get(savedFocusedTaskId),
				}
				yield* SubscriptionRef.update(perProjectState, (current) => {
					const next = new Map(current)
					next.set(path, state)
					return next
				})
			})

		const syncDerivedFromProject = (projectPath: string) =>
			Effect.gen(function* () {
				const state = yield* getOrCreateProjectState(projectPath)
				yield* SubscriptionRef.set(focusedTaskId, state.focusedTaskId)
				yield* SubscriptionRef.set(followTaskId, state.followTaskId)
				yield* SubscriptionRef.set(drillDownEpic, state.drillDownEpic)
				yield* SubscriptionRef.set(drillDownChildIds, state.drillDownChildIds)
				yield* SubscriptionRef.set(drillDownChildDetails, state.drillDownChildDetails)
				yield* SubscriptionRef.set(savedFocusedTaskId, state.savedFocusedTaskId)
			})

		const clearDerivedState = () =>
			Effect.gen(function* () {
				yield* SubscriptionRef.set(drillDownEpic, null)
				yield* SubscriptionRef.set(drillDownChildIds, EMPTY_CHILD_IDS)
				yield* SubscriptionRef.set(drillDownChildDetails, EMPTY_CHILD_DETAILS)
				yield* SubscriptionRef.set(savedFocusedTaskId, null)
				yield* SubscriptionRef.set(followTaskId, null)
				yield* SubscriptionRef.set(focusedTaskId, null)
			})

		const syncProject = (newProjectPath: string | null) =>
			Effect.gen(function* () {
				yield* saveCurrentToMap()
				yield* SubscriptionRef.set(currentProjectPath, newProjectPath)
				if (newProjectPath === null) {
					yield* clearDerivedState()
					return
				}
				yield* syncDerivedFromProject(newProjectPath)
			})

		const sortByPhase = (
			tasks: readonly TaskWithSession[],
			phases: ReadonlyMap<string, TaskPhaseInfo>,
		): readonly TaskWithSession[] =>
			[...tasks].sort((left, right) => {
				const leftPhase = phases.get(left.id)?.phase ?? 1
				const rightPhase = phases.get(right.id)?.phase ?? 1
				return leftPhase - rightPhase
			})

		const getFilteredTasksByColumn = () =>
			Effect.gen(function* () {
				const tasksByColumn = yield* SubscriptionRef.get(board.filteredTasksByColumn)
				const childIds = yield* SubscriptionRef.get(drillDownChildIds)
				if (childIds.size === 0) {
					return tasksByColumn.map((column) =>
						column.filter((task) => task.parentEpicId === undefined),
					)
				}

				const drillDownColumns = tasksByColumn.map((column) =>
					column.filter((task) => childIds.has(task.id)),
				)
				const childDetails = yield* SubscriptionRef.get(drillDownChildDetails)
				if (childDetails.size === 0) {
					return drillDownColumns
				}

				const phases = computeDependencyPhases(childIds, childDetails)
				return drillDownColumns.map((column) => sortByPhase(column, phases.phases))
			})

		const findTaskPosition = (taskId: string | null) =>
			Effect.gen(function* () {
				if (taskId === null) {
					return undefined
				}

				const tasksByColumn = yield* getFilteredTasksByColumn()
				for (let columnIndex = 0; columnIndex < tasksByColumn.length; columnIndex += 1) {
					const column = tasksByColumn[columnIndex]
					if (column === undefined) {
						continue
					}
					const taskIndex = column.findIndex((task) => task.id === taskId)
					if (taskIndex >= 0) {
						return { columnIndex, taskIndex } satisfies Position
					}
				}
				return undefined
			})

		const getTaskAtPosition = (columnIndex: number, taskIndex: number) =>
			Effect.gen(function* () {
				const tasksByColumn = yield* getFilteredTasksByColumn()
				const column = tasksByColumn[columnIndex]
				if (column === undefined) {
					return undefined
				}
				return column[taskIndex]
			})

		const getFirstTask = () =>
			Effect.gen(function* () {
				const tasksByColumn = yield* getFilteredTasksByColumn()
				for (const column of tasksByColumn) {
					if (column.length > 0) {
						return column[0]
					}
				}
				return undefined
			})

		const ensureValidFocus = () =>
			Effect.gen(function* () {
				const currentId = yield* SubscriptionRef.get(focusedTaskId)
				const position = yield* findTaskPosition(currentId)
				if (position !== undefined) {
					return
				}
				const firstTask = yield* getFirstTask()
				if (firstTask !== undefined) {
					yield* SubscriptionRef.set(focusedTaskId, firstTask.id)
				}
			})

		const loadPersistedDrillDownState = (epicId: string) =>
			Effect.gen(function* () {
				const projectPath = yield* SubscriptionRef.get(currentProjectPath)
				return yield* loadDrillDownStateFromDaemon(daemonRpcClient, epicId, projectPath).pipe(
					Effect.catchAll(() =>
						Effect.succeed({
							childIds: EMPTY_CHILD_IDS,
							childDetails: EMPTY_CHILD_DETAILS,
						}),
					),
				)
			})

		const refreshDrillDownCore = (epicId: string) =>
			Effect.gen(function* () {
				const restored = yield* loadPersistedDrillDownState(epicId)
				const existingChildIds = yield* SubscriptionRef.get(drillDownChildIds)
				const addedChildren = [...restored.childIds].filter(
					(childId) => !existingChildIds.has(childId),
				)
				if (addedChildren.length === 0) {
					return
				}

				yield* SubscriptionRef.set(drillDownChildIds, restored.childIds)
				const existingDetails = yield* SubscriptionRef.get(drillDownChildDetails)
				const mergedDetails = new Map(existingDetails)
				for (const childId of addedChildren) {
					const detail = restored.childDetails.get(childId)
					if (detail !== undefined) {
						mergedDetails.set(childId, detail)
					}
				}
				yield* SubscriptionRef.set(drillDownChildDetails, mergedDetails)
			})

		const followEditorFiber = yield* Effect.forkScoped(
			Stream.runForEach(editor.mode.changes, () =>
				ensureValidFocus().pipe(
					Effect.catchAllCause((cause) =>
						Effect.logDebug("NavigationService ensureValidFocus failed", { cause }).pipe(
							Effect.asVoid,
						),
					),
				),
			),
		)
		yield* diagnostics.registerFiber({
			id: "tui-nav-editor-focus-sync",
			name: "TUI Navigation Focus Sync",
			description: "Keeps TUI focus aligned with editor filters and search",
			fiber: followEditorFiber,
		})

		const drillDownRefreshFiber = yield* Effect.forkScoped(
			Stream.runForEach(board.tasks.changes, () =>
				Effect.gen(function* () {
					const epicId = yield* SubscriptionRef.get(drillDownEpic)
					if (epicId === null) {
						return
					}
					yield* refreshDrillDownCore(epicId)
				}).pipe(
					Effect.catchAllCause((cause) =>
						Effect.logDebug("NavigationService drill-down refresh failed", { cause }).pipe(
							Effect.asVoid,
						),
					),
				),
			),
		)
		yield* diagnostics.registerFiber({
			id: "tui-nav-drilldown-refresh",
			name: "TUI Drill-Down Refresh",
			description: "Refreshes drill-down children when board tasks change",
			fiber: drillDownRefreshFiber,
		})

		const projectSyncFiber = yield* Effect.forkScoped(
			Stream.runForEach(board.currentProjectPath.changes, (projectPath) =>
				syncProject(projectPath).pipe(
					Effect.catchAllCause((cause) =>
						Effect.logDebug("NavigationService project sync failed", { cause }).pipe(Effect.asVoid),
					),
				),
			),
		)
		yield* diagnostics.registerFiber({
			id: "tui-nav-project-sync",
			name: "TUI Navigation Project Sync",
			description: "Keeps per-project navigation state aligned with board project changes",
			fiber: projectSyncFiber,
		})

		yield* syncProject(yield* SubscriptionRef.get(board.currentProjectPath))

		return {
			focusedTaskId,
			followTaskId,
			drillDownEpic,
			drillDownChildIds,
			drillDownChildDetails,
			perProjectState,
			currentProjectPath,
			switchProject: (newProjectPath: string | null): Effect.Effect<void> =>
				syncProject(newProjectPath),
			getPosition: (): Effect.Effect<Position> =>
				Effect.gen(function* () {
					const position = yield* findTaskPosition(yield* SubscriptionRef.get(focusedTaskId))
					return position ?? { columnIndex: 0, taskIndex: 0 }
				}),
			getFocusedTaskId: (): Effect.Effect<string | null> => SubscriptionRef.get(focusedTaskId),
			getFocusedTask: () =>
				Effect.gen(function* () {
					const currentId = yield* SubscriptionRef.get(focusedTaskId)
					if (currentId === null) {
						return undefined
					}
					const tasks = yield* SubscriptionRef.get(board.tasks)
					return tasks.find((task) => task.id === currentId)
				}),
			move: (direction: Direction): Effect.Effect<void> =>
				Effect.gen(function* () {
					const tasksByColumn = yield* getFilteredTasksByColumn()
					const currentId = yield* SubscriptionRef.get(focusedTaskId)
					const currentPos = yield* findTaskPosition(currentId)
					if (currentPos === undefined) {
						yield* ensureValidFocus()
						return
					}

					const currentColumn = tasksByColumn[currentPos.columnIndex] ?? []
					let nextColumnIndex = currentPos.columnIndex
					let nextTaskIndex = currentPos.taskIndex

					switch (direction) {
						case "up":
							nextTaskIndex =
								currentPos.taskIndex > 0
									? currentPos.taskIndex - 1
									: Math.max(0, currentColumn.length - 1)
							break
						case "down":
							nextTaskIndex =
								currentPos.taskIndex < currentColumn.length - 1 ? currentPos.taskIndex + 1 : 0
							break
						case "left":
							for (
								let columnIndex = currentPos.columnIndex - 1;
								columnIndex >= 0;
								columnIndex -= 1
							) {
								const column = tasksByColumn[columnIndex] ?? []
								if (column.length > 0) {
									nextColumnIndex = columnIndex
									nextTaskIndex = 0
									break
								}
							}
							break
						case "right":
							for (
								let columnIndex = currentPos.columnIndex + 1;
								columnIndex < tasksByColumn.length;
								columnIndex += 1
							) {
								const column = tasksByColumn[columnIndex] ?? []
								if (column.length > 0) {
									nextColumnIndex = columnIndex
									nextTaskIndex = 0
									break
								}
							}
							break
					}

					const nextTask = yield* getTaskAtPosition(nextColumnIndex, nextTaskIndex)
					if (nextTask !== undefined) {
						yield* SubscriptionRef.set(focusedTaskId, nextTask.id)
					}
				}),
			jumpTo: (columnIndex: number, taskIndex: number): Effect.Effect<void> =>
				Effect.gen(function* () {
					const task = yield* getTaskAtPosition(columnIndex, taskIndex)
					if (task !== undefined) {
						yield* SubscriptionRef.set(focusedTaskId, task.id)
					}
				}),
			jumpToTask: (taskId: string): Effect.Effect<void> =>
				SubscriptionRef.set(focusedTaskId, taskId),
			setFocusedTask: (taskId: string | null): Effect.Effect<void> =>
				SubscriptionRef.set(focusedTaskId, taskId),
			jumpToEnd: (): Effect.Effect<void> =>
				Effect.gen(function* () {
					const tasksByColumn = yield* getFilteredTasksByColumn()
					for (let columnIndex = tasksByColumn.length - 1; columnIndex >= 0; columnIndex -= 1) {
						const column = tasksByColumn[columnIndex] ?? []
						const task = column[column.length - 1]
						if (task !== undefined) {
							yield* SubscriptionRef.set(focusedTaskId, task.id)
							return
						}
					}
				}),
			goToFirst: (): Effect.Effect<void> =>
				Effect.gen(function* () {
					const currentId = yield* SubscriptionRef.get(focusedTaskId)
					const currentPos = yield* findTaskPosition(currentId)
					if (currentPos === undefined) {
						yield* ensureValidFocus()
						return
					}
					const column = (yield* getFilteredTasksByColumn())[currentPos.columnIndex] ?? []
					const task = column[0]
					if (task !== undefined) {
						yield* SubscriptionRef.set(focusedTaskId, task.id)
					}
				}),
			goToLast: (): Effect.Effect<void> =>
				Effect.gen(function* () {
					const currentId = yield* SubscriptionRef.get(focusedTaskId)
					const currentPos = yield* findTaskPosition(currentId)
					if (currentPos === undefined) {
						yield* ensureValidFocus()
						return
					}
					const column = (yield* getFilteredTasksByColumn())[currentPos.columnIndex] ?? []
					const task = column[column.length - 1]
					if (task !== undefined) {
						yield* SubscriptionRef.set(focusedTaskId, task.id)
					}
				}),
			goToFirstColumn: (): Effect.Effect<void> =>
				Effect.gen(function* () {
					const currentId = yield* SubscriptionRef.get(focusedTaskId)
					const currentPos = yield* findTaskPosition(currentId)
					const firstColumn = (yield* getFilteredTasksByColumn())[0] ?? []
					if (firstColumn.length === 0) {
						return
					}
					const targetIndex =
						currentPos === undefined ? 0 : Math.min(currentPos.taskIndex, firstColumn.length - 1)
					const task = firstColumn[targetIndex]
					if (task !== undefined) {
						yield* SubscriptionRef.set(focusedTaskId, task.id)
					}
				}),
			goToLastColumn: (): Effect.Effect<void> =>
				Effect.gen(function* () {
					const currentId = yield* SubscriptionRef.get(focusedTaskId)
					const currentPos = yield* findTaskPosition(currentId)
					const tasksByColumn = yield* getFilteredTasksByColumn()
					const lastColumn = tasksByColumn[tasksByColumn.length - 1] ?? []
					if (lastColumn.length === 0) {
						return
					}
					const targetIndex =
						currentPos === undefined ? 0 : Math.min(currentPos.taskIndex, lastColumn.length - 1)
					const task = lastColumn[targetIndex]
					if (task !== undefined) {
						yield* SubscriptionRef.set(focusedTaskId, task.id)
					}
				}),
			halfPageDown: (): Effect.Effect<void> =>
				Effect.gen(function* () {
					const currentId = yield* SubscriptionRef.get(focusedTaskId)
					const currentPos = yield* findTaskPosition(currentId)
					if (currentPos === undefined) {
						yield* ensureValidFocus()
						return
					}
					const column = (yield* getFilteredTasksByColumn())[currentPos.columnIndex] ?? []
					const halfPage = Math.max(1, Math.floor(column.length / 2))
					const task =
						column[Math.min(currentPos.taskIndex + halfPage, Math.max(0, column.length - 1))]
					if (task !== undefined) {
						yield* SubscriptionRef.set(focusedTaskId, task.id)
					}
				}),
			halfPageUp: (): Effect.Effect<void> =>
				Effect.gen(function* () {
					const currentId = yield* SubscriptionRef.get(focusedTaskId)
					const currentPos = yield* findTaskPosition(currentId)
					if (currentPos === undefined) {
						yield* ensureValidFocus()
						return
					}
					const column = (yield* getFilteredTasksByColumn())[currentPos.columnIndex] ?? []
					const halfPage = Math.max(1, Math.floor(column.length / 2))
					const task = column[Math.max(0, currentPos.taskIndex - halfPage)]
					if (task !== undefined) {
						yield* SubscriptionRef.set(focusedTaskId, task.id)
					}
				}),
			setFollow: (taskId: string | null): Effect.Effect<void> =>
				SubscriptionRef.set(followTaskId, taskId),
			initialize: (): Effect.Effect<void> => ensureValidFocus(),
			getCursor: (): Effect.Effect<Position> =>
				Effect.gen(function* () {
					const position = yield* findTaskPosition(yield* SubscriptionRef.get(focusedTaskId))
					return position ?? { columnIndex: 0, taskIndex: 0 }
				}),
			enterDrillDown: (
				epicId: string,
				childIds: ReadonlySet<string>,
				childDetails?: ReadonlyMap<string, Issue>,
			): Effect.Effect<void> =>
				Effect.gen(function* () {
					yield* SubscriptionRef.set(savedFocusedTaskId, yield* SubscriptionRef.get(focusedTaskId))
					yield* SubscriptionRef.set(drillDownChildIds, childIds)
					yield* SubscriptionRef.set(drillDownChildDetails, childDetails ?? EMPTY_CHILD_DETAILS)
					yield* SubscriptionRef.set(drillDownEpic, epicId)
					yield* SubscriptionRef.set(focusedTaskId, null)
				}),
			exitDrillDown: (): Effect.Effect<void> =>
				Effect.gen(function* () {
					const savedId = yield* SubscriptionRef.get(savedFocusedTaskId)
					if (savedId !== null) {
						yield* SubscriptionRef.set(focusedTaskId, savedId)
					}
					yield* SubscriptionRef.set(savedFocusedTaskId, null)
					yield* SubscriptionRef.set(drillDownChildIds, EMPTY_CHILD_IDS)
					yield* SubscriptionRef.set(drillDownChildDetails, EMPTY_CHILD_DETAILS)
					yield* SubscriptionRef.set(drillDownEpic, null)
				}),
			isInDrillDown: (): Effect.Effect<boolean> =>
				Effect.map(SubscriptionRef.get(drillDownEpic), (epicId) => epicId !== null),
			getDrillDownEpic: (): Effect.Effect<string | null> => SubscriptionRef.get(drillDownEpic),
			restorePersistedState: (state: {
				readonly focusedTaskId: string | null
				readonly drillDownEpicId: string | null
				readonly savedFocusedTaskId?: string | null
			}) =>
				Effect.gen(function* () {
					if (state.drillDownEpicId === null) {
						yield* SubscriptionRef.set(drillDownEpic, null)
						yield* SubscriptionRef.set(drillDownChildIds, EMPTY_CHILD_IDS)
						yield* SubscriptionRef.set(drillDownChildDetails, EMPTY_CHILD_DETAILS)
						yield* SubscriptionRef.set(savedFocusedTaskId, null)
						yield* SubscriptionRef.set(focusedTaskId, state.focusedTaskId)
						return
					}

					const restored = yield* loadPersistedDrillDownState(state.drillDownEpicId)
					yield* SubscriptionRef.set(drillDownChildIds, restored.childIds)
					yield* SubscriptionRef.set(drillDownChildDetails, restored.childDetails)
					yield* SubscriptionRef.set(drillDownEpic, state.drillDownEpicId)
					yield* SubscriptionRef.set(savedFocusedTaskId, state.savedFocusedTaskId ?? null)
					yield* SubscriptionRef.set(focusedTaskId, state.focusedTaskId)
				}),
			refreshDrillDown: (epicId: string): Effect.Effect<void> =>
				Effect.gen(function* () {
					const currentEpic = yield* SubscriptionRef.get(drillDownEpic)
					if (currentEpic !== epicId) {
						return
					}
					yield* refreshDrillDownCore(epicId)
				}),
			getStateForSave: () =>
				Effect.gen(function* () {
					return {
						focusedTaskId: yield* SubscriptionRef.get(focusedTaskId),
						drillDownEpicId: yield* SubscriptionRef.get(drillDownEpic),
						savedFocusedTaskId: yield* SubscriptionRef.get(savedFocusedTaskId),
					}
				}),
		}
	}),
}) {}
