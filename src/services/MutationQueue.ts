import type { CommandExecutor } from "@effect/platform"
import { Cause, Effect, Ref } from "effect"
import { BeadsClient } from "../core/BeadsClient.js"
import type { ColumnStatus, TaskWithSession } from "../ui/types.js"
import { DiagnosticsService } from "./DiagnosticsService.js"
import { ToastService } from "./ToastService.js"

export type Mutation =
	| {
			_tag: "Move"
			id: string
			status: ColumnStatus
			rollback: Effect.Effect<void, never, CommandExecutor.CommandExecutor>
	  }
	| {
			_tag: "Delete"
			id: string
			rollback: Effect.Effect<void, never, CommandExecutor.CommandExecutor>
	  }
	| {
			_tag: "Update"
			id: string
			fields: Partial<TaskWithSession>
			rollback: Effect.Effect<void, never, CommandExecutor.CommandExecutor>
	  }

export interface QueuedMutation {
	readonly mutation: Mutation
	readonly status: "pending" | "processing" | "success" | "failed"
	readonly timestamp: number
}

export class MutationQueue extends Effect.Service<MutationQueue>()("MutationQueue", {
	dependencies: [BeadsClient.Default, ToastService.Default, DiagnosticsService.Default],
	scoped: Effect.gen(function* () {
		const beadsClient = yield* BeadsClient
		const toast = yield* ToastService
		const diagnostics = yield* DiagnosticsService

		yield* diagnostics.trackService("MutationQueue", "Optimistic mutation queue with rollback")

		const mutationsRef = yield* Ref.make<Map<string, QueuedMutation>>(new Map())

		const add = (mutation: Mutation): Effect.Effect<void> =>
			Effect.gen(function* () {
				const timestamp = Date.now()

				yield* Ref.update(mutationsRef, (queue) => {
					const newQueue = new Map(queue)
					newQueue.set(mutation.id, {
						mutation,
						status: "pending",
						timestamp,
					})
					return newQueue
				})

				yield* Effect.log(`Queued ${mutation._tag} mutation for task ${mutation.id}`)
			})

		const executeMutation = (mutation: Mutation) => {
			switch (mutation._tag) {
				case "Update":
					return beadsClient.update(mutation.id, mutation.fields as Record<string, unknown>)
				case "Delete":
					return beadsClient.delete(mutation.id)
				case "Move":
					return beadsClient.update(mutation.id, { status: mutation.status })
			}
		}

		const process = (taskId: string) =>
			Effect.gen(function* () {
				const queue = yield* Ref.get(mutationsRef)
				const queued = queue.get(taskId)

				if (!queued) {
					yield* Effect.log(`No mutation found for task ${taskId}`)
					return
				}

				if (queued.status !== "pending") {
					yield* Effect.log(
						`Mutation ${queued.mutation._tag} for task ${taskId} is not pending, skipping`,
					)
					return
				}

				yield* Ref.update(mutationsRef, (queue) => {
					const newQueue = new Map(queue)
					const q = newQueue.get(taskId)
					if (q) {
						newQueue.set(taskId, { ...q, status: "processing" })
					}
					return newQueue
				})

				const execution = executeMutation(queued.mutation)

				yield* execution.pipe(
					Effect.tap(() =>
						Effect.gen(function* () {
							yield* Ref.update(mutationsRef, (queue) => {
								const newQueue = new Map(queue)
								newQueue.delete(taskId)
								return newQueue
							})
							yield* Effect.log(
								`Successfully processed ${queued.mutation._tag} mutation for task ${taskId}`,
							)
						}),
					),
					Effect.catchAllCause((cause) =>
						Effect.gen(function* () {
							yield* Ref.update(mutationsRef, (queue) => {
								const newQueue = new Map(queue)
								newQueue.delete(taskId)
								return newQueue
							})

							yield* queued.mutation.rollback.pipe(
								Effect.catchAllCause((rollbackCause) =>
									Effect.logError(
										`Rollback failed for ${queued.mutation._tag} on task ${taskId}: ${Cause.pretty(rollbackCause)}`,
									),
								),
							)
							yield* toast.show("error", `Failed to ${queued.mutation._tag} task ${taskId}`)
							yield* Effect.logError(
								`Failed to ${queued.mutation._tag} task ${taskId}: ${Cause.pretty(cause)}`,
							)
						}),
					),
				)
			})

		const rollback = (taskId: string) =>
			Effect.gen(function* () {
				const queue = yield* Ref.get(mutationsRef)
				const queued = queue.get(taskId)
				if (!queued) {
					yield* Effect.log(`No mutation to rollback for task ${taskId}`)
					return
				}
				yield* queued.mutation.rollback
				yield* Effect.log(`Rolled back mutation for task ${taskId}`)
			})

		const clearAll = (): Effect.Effect<void> => Ref.set(mutationsRef, new Map())

		return {
			add,
			process,
			rollback,
			clearAll,

			hasPending: (taskId: string): Effect.Effect<boolean> =>
				Ref.get(mutationsRef).pipe(
					Effect.map((queue) => {
						const queued = queue.get(taskId)
						return queued ? queued.status === "pending" || queued.status === "processing" : false
					}),
				),

			getMutations: (): Effect.Effect<ReadonlyMap<string, QueuedMutation>> => Ref.get(mutationsRef),
		}
	}),
}) {}
