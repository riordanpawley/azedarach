import { DaemonRpcClient, type DaemonRpcClientApi } from "@azedarach/shared/rpc"
import type { CommandExecutor } from "@effect/platform"
import { Cause, Effect, Option, Ref } from "effect"
import { IssueTrackerClient } from "../core/IssueTrackerClient.js"
import type { ColumnStatus } from "../ui/types.js"
import { DiagnosticsService } from "./DiagnosticsService.js"
import { ToastService } from "./ToastService.js"

/**
 * Fields that can be updated on a bead - matches IssueTrackerClient.update signature
 */
export interface IssueUpdateFields {
	readonly status?: string
	readonly notes?: string
	readonly priority?: number
	readonly title?: string
	readonly description?: string
	readonly design?: string
	readonly acceptance?: string
	readonly assignee?: string
	readonly estimate?: number
	readonly labels?: readonly string[]
}

export type Mutation =
	| {
			_tag: "Move"
			id: string
			status: ColumnStatus
			cwd?: string
			rollback: Effect.Effect<void, never, CommandExecutor.CommandExecutor>
	  }
	| {
			_tag: "Delete"
			id: string
			cwd?: string
			rollback: Effect.Effect<void, never, CommandExecutor.CommandExecutor>
	  }
	| {
			_tag: "Update"
			id: string
			fields: IssueUpdateFields
			cwd?: string
			rollback: Effect.Effect<void, never, CommandExecutor.CommandExecutor>
	  }

export interface QueuedMutation {
	readonly mutation: Mutation
	readonly status: "pending" | "processing" | "success" | "failed"
	readonly timestamp: number
}

export interface OptimisticQueuedMutation {
	readonly mutation:
		| { readonly _tag: "Move"; readonly id: string; readonly status: ColumnStatus }
		| { readonly _tag: "Delete"; readonly id: string }
		| { readonly _tag: "Update"; readonly id: string; readonly fields: IssueUpdateFields }
	readonly status: "pending" | "processing"
	readonly timestamp: number
}

interface DaemonQueueRpc {
	readonly queueEnqueue: NonNullable<DaemonRpcClientApi["queueEnqueue"]>
	readonly queueQuery: NonNullable<DaemonRpcClientApi["queueQuery"]>
	readonly queueCancel: NonNullable<DaemonRpcClientApi["queueCancel"]>
}

const isRecord = (value: unknown): value is Record<string, unknown> =>
	typeof value === "object" && value !== null

const isColumnStatus = (value: string): value is ColumnStatus =>
	value === "open" || value === "in_progress" || value === "blocked" || value === "closed"

const toDaemonPayload = (mutation: Mutation): string => {
	switch (mutation._tag) {
		case "Move":
			return JSON.stringify({ tag: "Move", status: mutation.status })
		case "Delete":
			return JSON.stringify({ tag: "Delete" })
		case "Update":
			return JSON.stringify({ tag: "Update", fields: mutation.fields })
	}
}

const toOptimisticMutationFromDaemon = (
	issueId: string,
	payloadJson: string | null,
): Option.Option<OptimisticQueuedMutation["mutation"]> => {
	if (payloadJson === null) {
		return Option.none()
	}

	try {
		const parsed = JSON.parse(payloadJson)
		if (!isRecord(parsed)) {
			return Option.none()
		}
		const tag = parsed["tag"]
		if (tag === "Delete") {
			return Option.some({ _tag: "Delete", id: issueId })
		}
		if (tag === "Move") {
			const status = parsed["status"]
			return typeof status === "string" && isColumnStatus(status)
				? Option.some({ _tag: "Move", id: issueId, status })
				: Option.none()
		}
		if (tag === "Update") {
			const fields = parsed["fields"]
			return isRecord(fields)
				? Option.some({
						_tag: "Update",
						id: issueId,
						fields: {
							status: typeof fields["status"] === "string" ? fields["status"] : undefined,
							notes: typeof fields["notes"] === "string" ? fields["notes"] : undefined,
							priority: typeof fields["priority"] === "number" ? fields["priority"] : undefined,
							title: typeof fields["title"] === "string" ? fields["title"] : undefined,
							description:
								typeof fields["description"] === "string" ? fields["description"] : undefined,
							design: typeof fields["design"] === "string" ? fields["design"] : undefined,
							acceptance:
								typeof fields["acceptance"] === "string" ? fields["acceptance"] : undefined,
							assignee: typeof fields["assignee"] === "string" ? fields["assignee"] : undefined,
							estimate: typeof fields["estimate"] === "number" ? fields["estimate"] : undefined,
							labels:
								Array.isArray(fields["labels"]) &&
								fields["labels"].every((entry) => typeof entry === "string")
									? fields["labels"]
									: undefined,
						},
					})
				: Option.none()
		}
		return Option.none()
	} catch {
		return Option.none()
	}
}

const toDaemonPendingStatus = (
	state: "queued" | "running" | "done" | "failed" | "cancelled",
): Option.Option<OptimisticQueuedMutation["status"]> => {
	switch (state) {
		case "queued":
			return Option.some("pending")
		case "running":
			return Option.some("processing")
		case "done":
		case "failed":
		case "cancelled":
			return Option.none()
	}
}

export class MutationQueue extends Effect.Service<MutationQueue>()("MutationQueue", {
	dependencies: [IssueTrackerClient.Default, ToastService.Default, DiagnosticsService.Default],
	scoped: Effect.gen(function* () {
		const issueTrackerClient = yield* IssueTrackerClient
		const toast = yield* ToastService
		const diagnostics = yield* DiagnosticsService
		const daemonRpcClient = yield* Effect.serviceOption(DaemonRpcClient)

		yield* diagnostics.trackService("MutationQueue", "Optimistic mutation queue with rollback")

		const mutationsRef = yield* Ref.make<Map<string, QueuedMutation>>(new Map())

		const getDaemonQueueRpc = (): Option.Option<DaemonQueueRpc> => {
			if (Option.isNone(daemonRpcClient)) {
				return Option.none()
			}
			const queueEnqueue = daemonRpcClient.value.queueEnqueue
			const queueQuery = daemonRpcClient.value.queueQuery
			const queueCancel = daemonRpcClient.value.queueCancel
			if (queueEnqueue === undefined || queueQuery === undefined || queueCancel === undefined) {
				return Option.none()
			}
			return Option.some({
				queueEnqueue,
				queueQuery,
				queueCancel,
			})
		}

		const resolveDaemonProjectPath = (
			projectPath: string | undefined,
		): Effect.Effect<string, never, never> =>
			projectPath === undefined
				? Effect.succeed(globalThis.process.cwd())
				: Effect.succeed(projectPath)

		const clearDaemonMutationQueue = (
			taskId?: string,
			projectPath?: string,
		): Effect.Effect<void, never, never> =>
			Effect.gen(function* () {
				const daemonQueueRpc = getDaemonQueueRpc()
				if (Option.isNone(daemonQueueRpc)) {
					return
				}
				const daemonProjectPath = yield* resolveDaemonProjectPath(projectPath)
				yield* daemonQueueRpc.value
					.queueCancel(
						taskId === undefined
							? { domain: "mutation", projectPath: daemonProjectPath }
							: {
									domain: "mutation",
									issueId: taskId,
									projectPath: daemonProjectPath,
								},
					)
					.pipe(
						Effect.catchAll((error) =>
							Effect.logWarning(
								"Daemon mutation queue cancel failed; keeping local mutation state",
								{
									taskId,
									projectPath,
									error,
								},
							),
						),
						Effect.asVoid,
					)
			})

		const getOptimisticMutationsFromLocal = (): Effect.Effect<
			ReadonlyMap<string, OptimisticQueuedMutation>,
			never,
			never
		> =>
			Ref.get(mutationsRef).pipe(
				Effect.map((queue) => {
					const optimisticQueue = new Map<string, OptimisticQueuedMutation>()
					for (const [issueId, queued] of queue.entries()) {
						if (queued.status !== "pending" && queued.status !== "processing") {
							continue
						}
						switch (queued.mutation._tag) {
							case "Move":
								optimisticQueue.set(issueId, {
									mutation: {
										_tag: "Move",
										id: queued.mutation.id,
										status: queued.mutation.status,
									},
									status: queued.status,
									timestamp: queued.timestamp,
								})
								break
							case "Delete":
								optimisticQueue.set(issueId, {
									mutation: {
										_tag: "Delete",
										id: queued.mutation.id,
									},
									status: queued.status,
									timestamp: queued.timestamp,
								})
								break
							case "Update":
								optimisticQueue.set(issueId, {
									mutation: {
										_tag: "Update",
										id: queued.mutation.id,
										fields: queued.mutation.fields,
									},
									status: queued.status,
									timestamp: queued.timestamp,
								})
								break
						}
					}
					return optimisticQueue
				}),
			)

		const getOptimisticMutationsFromAdapter = (): Effect.Effect<
			ReadonlyMap<string, OptimisticQueuedMutation>,
			never,
			never
		> =>
			Effect.gen(function* () {
				const daemonQueueRpc = getDaemonQueueRpc()
				if (Option.isNone(daemonQueueRpc)) {
					return yield* getOptimisticMutationsFromLocal()
				}
				const daemonProjectPath = yield* resolveDaemonProjectPath(undefined)

				return yield* daemonQueueRpc.value
					.queueQuery({
						domain: "mutation",
						projectPath: daemonProjectPath,
					})
					.pipe(
						Effect.map((result) => {
							const optimisticQueue = new Map<string, OptimisticQueuedMutation>()
							for (const item of result.items) {
								if (item.issueId === null) {
									continue
								}
								const status = toDaemonPendingStatus(item.state)
								if (Option.isNone(status)) {
									continue
								}
								const mutation = toOptimisticMutationFromDaemon(item.issueId, item.payloadJson)
								if (Option.isNone(mutation)) {
									continue
								}
								optimisticQueue.set(item.issueId, {
									mutation: mutation.value,
									status: status.value,
									timestamp: item.enqueuedAtMs,
								})
							}
							return optimisticQueue
						}),
						Effect.catchAll((error) =>
							Effect.logWarning("Daemon mutation queue query failed; using local mutation state", {
								error,
							}).pipe(Effect.zipRight(getOptimisticMutationsFromLocal())),
						),
					)
			})

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

				const daemonQueueRpc = getDaemonQueueRpc()
				if (Option.isSome(daemonQueueRpc)) {
					const daemonProjectPath = yield* resolveDaemonProjectPath(mutation.cwd)
					yield* daemonQueueRpc.value
						.queueEnqueue({
							domain: "mutation",
							operation: mutation._tag,
							issueId: mutation.id,
							projectPath: daemonProjectPath,
							dedupeKey: `${mutation.id}:${mutation._tag}`,
							payloadJson: toDaemonPayload(mutation),
						})
						.pipe(
							Effect.catchAll((error) =>
								Effect.logWarning(
									"Daemon mutation queue enqueue failed; falling back to local queue",
									{
										taskId: mutation.id,
										tag: mutation._tag,
										error,
									},
								),
							),
							Effect.asVoid,
						)
				}
			})

		const executeMutation = (mutation: Mutation) => {
			switch (mutation._tag) {
				case "Update":
					// IssueUpdateFields is structurally compatible with IssueTrackerClient.update's fields parameter
					return issueTrackerClient.update(
						mutation.id,
						{
							status: mutation.fields.status,
							notes: mutation.fields.notes,
							priority: mutation.fields.priority,
							title: mutation.fields.title,
							description: mutation.fields.description,
							design: mutation.fields.design,
							acceptance: mutation.fields.acceptance,
							assignee: mutation.fields.assignee,
							estimate: mutation.fields.estimate,
							labels: mutation.fields.labels ? [...mutation.fields.labels] : undefined,
						},
						mutation.cwd,
					)
				case "Delete":
					return issueTrackerClient.delete(mutation.id, mutation.cwd)
				case "Move":
					return issueTrackerClient.update(mutation.id, { status: mutation.status }, mutation.cwd)
			}
		}

		const syncAfterMutation = (taskId: string, cwd?: string) =>
			issueTrackerClient.sync(cwd).pipe(
				Effect.catchAll((error) =>
					Effect.logWarning(
						`MutationQueue post-mutation sync failed for task ${taskId} (projectPath=${cwd ?? "<default>"}): ${String(error)}`,
					).pipe(Effect.asVoid),
				),
				Effect.asVoid,
			)

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
				yield* Effect.log(
					`Processing ${queued.mutation._tag} mutation for task ${taskId} (projectPath=${queued.mutation.cwd ?? "<default>"})`,
				)

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
							yield* syncAfterMutation(taskId, queued.mutation.cwd)
							yield* clearDaemonMutationQueue(taskId, queued.mutation.cwd)
							yield* Effect.log(
								`Successfully processed ${queued.mutation._tag} mutation for task ${taskId} (projectPath=${queued.mutation.cwd ?? "<default>"})`,
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
							yield* clearDaemonMutationQueue(taskId, queued.mutation.cwd)
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
			enqueue: (mutation: Mutation): Effect.Effect<void, never, CommandExecutor.CommandExecutor> =>
				add(mutation).pipe(Effect.zipRight(process(mutation.id))),
			process,
			rollback,
			clearAll: (): Effect.Effect<void> =>
				clearAll().pipe(Effect.zipRight(clearDaemonMutationQueue()), Effect.asVoid),

			hasPending: (taskId: string): Effect.Effect<boolean> =>
				Effect.gen(function* () {
					const getLocalPending = Ref.get(mutationsRef).pipe(
						Effect.map((queue) => {
							const queued = queue.get(taskId)
							return queued ? queued.status === "pending" || queued.status === "processing" : false
						}),
					)
					const daemonQueueRpc = getDaemonQueueRpc()
					if (Option.isNone(daemonQueueRpc)) {
						return yield* getLocalPending
					}
					const daemonProjectPath = yield* resolveDaemonProjectPath(undefined)

					return yield* daemonQueueRpc.value
						.queueQuery({
							domain: "mutation",
							issueId: taskId,
							projectPath: daemonProjectPath,
						})
						.pipe(
							Effect.map((result) =>
								result.items.some((item) => item.state === "queued" || item.state === "running"),
							),
							Effect.catchAll((error) =>
								Effect.logWarning("Daemon mutation queue query failed; using local pending state", {
									taskId,
									error,
								}).pipe(Effect.zipRight(getLocalPending)),
							),
						)
				}),

			getMutations: (): Effect.Effect<ReadonlyMap<string, QueuedMutation>> => Ref.get(mutationsRef),
			getOptimisticMutations: (): Effect.Effect<ReadonlyMap<string, OptimisticQueuedMutation>> =>
				getOptimisticMutationsFromAdapter(),
		}
	}),
}) {}
