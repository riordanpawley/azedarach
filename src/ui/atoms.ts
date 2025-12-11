/**
 * Atoms for Azedarach UI state
 *
 * Uses effect-atom for reactive state management with Effect integration.
 */
import { Atom } from "@effect-atom/atom"
import { Effect } from "effect"
import type { TaskWithSession } from "./types"
import { BeadsClient, BeadsClientLiveWithPlatform } from "../core/BeadsClient"

/**
 * Runtime atom that provides BeadsClient and platform dependencies
 *
 * This creates a runtime that all other async atoms can use.
 */
export const appRuntime = Atom.runtime(BeadsClientLiveWithPlatform)

/**
 * Async atom that fetches all tasks from BeadsClient
 *
 * Uses the appRuntime to access BeadsClient service.
 * Returns Result.Result<TaskWithSession[], Error> for proper loading/error states.
 *
 * Note: Fetches ALL issues (not just ready) so we can display the full kanban board.
 */
export const tasksAtom = appRuntime.atom(
  Effect.gen(function* () {
    const client = yield* BeadsClient
    // Fetch all issues (no status filter) to populate the full board
    const issues = yield* client.list()

    // Map issues to TaskWithSession (all start as idle)
    const tasks: TaskWithSession[] = issues.map((issue) => ({
      ...issue,
      sessionState: "idle" as const,
    }))

    return tasks
  }),
  { initialValue: [] }
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
 * Effect to move a task to a new status
 *
 * Returns an Effect that updates the task's status via BeadsClient.
 * Call with Effect.runPromise and then refresh the tasks atom.
 */
export const moveTaskEffect = (taskId: string, newStatus: string) =>
  Effect.gen(function* () {
    const client = yield* BeadsClient
    yield* client.update(taskId, { status: newStatus })
  }).pipe(Effect.provide(BeadsClientLiveWithPlatform))

/**
 * Effect to move multiple tasks at once
 */
export const moveTasksEffect = (taskIds: string[], newStatus: string) =>
  Effect.gen(function* () {
    const client = yield* BeadsClient
    yield* Effect.all(
      taskIds.map((id) => client.update(id, { status: newStatus })),
      { concurrency: "unbounded" }
    )
  }).pipe(Effect.provide(BeadsClientLiveWithPlatform))
