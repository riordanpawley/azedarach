import type { CommandExecutor } from "@effect/platform"
import { Data, Effect, Option, Ref } from "effect"
import { DaemonRpcClient, type DaemonRpcClientApi } from "../rpc/DaemonRpcClient.js"
import type { ColumnStatus } from "../ui/types.js"
import { DiagnosticsService } from "./DiagnosticsService.js"

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

export class MutationQueueUnavailableError extends Data.TaggedError(
	"MutationQueueUnavailableError",
)<{
	readonly operation: "queue-enqueue" | "queue-query" | "queue-cancel"
	readonly message: string
}> {}

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
	dependencies: [DiagnosticsService.Default],
	scoped: Effect.gen(function* () {
		const diagnostics = yield* DiagnosticsService
		const daemonRpcClient = yield* DaemonRpcClient

		yield* diagnostics.trackService("MutationQueue", "Optimistic mutation queue with rollback")

		const mutationsRef = yield* Ref.make<Map<string, QueuedMutation>>(new Map())

		const daemonQueueRpc = yield* Effect.gen(function* () {
			const queueEnqueue = daemonRpcClient.queueEnqueue
			const queueQuery = daemonRpcClient.queueQuery
			const queueCancel = daemonRpcClient.queueCancel
			if (queueEnqueue === undefined || queueQuery === undefined || queueCancel === undefined) {
				return yield* Effect.fail(
					new MutationQueueUnavailableError({
						operation: "queue-query",
						message:
							"Daemon mutation queue RPC is unavailable (queueEnqueue/queueQuery/queueCancel required).",
					}),
				)
			}
			return {
				queueEnqueue,
				queueQuery,
				queueCancel,
			}
		})

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
				const daemonProjectPath = yield* resolveDaemonProjectPath(projectPath)
				yield* daemonQueueRpc
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
								"Daemon mutation queue cancel failed; keeping local optimistic state",
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
				const daemonProjectPath = yield* resolveDaemonProjectPath(undefined)

				return yield* daemonQueueRpc
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
							Effect.logWarning("Daemon mutation queue query failed; returning empty state", {
								error,
							}).pipe(Effect.zipRight(Effect.succeed(new Map<string, OptimisticQueuedMutation>()))),
						),
					)
			})

		const add = (mutation: Mutation): Effect.Effect<void, MutationQueueUnavailableError> =>
			Effect.gen(function* () {
				const daemonProjectPath = yield* resolveDaemonProjectPath(mutation.cwd)
				yield* daemonQueueRpc
					.queueEnqueue({
						domain: "mutation",
						operation: mutation._tag,
						issueId: mutation.id,
						projectPath: daemonProjectPath,
						dedupeKey: `${mutation.id}:${mutation._tag}`,
						payloadJson: toDaemonPayload(mutation),
					})
					.pipe(
						Effect.asVoid,
						Effect.mapError(
							(error) =>
								new MutationQueueUnavailableError({
									operation: "queue-enqueue",
									message: String(error),
								}),
						),
					)
			})

		const process = (_taskId: string): Effect.Effect<void> => Effect.void

		const rollback = (_taskId: string): Effect.Effect<void> => Effect.void

		const clearAll = (): Effect.Effect<void> => Ref.set(mutationsRef, new Map())

		return {
			add,
			enqueue: (
				mutation: Mutation,
			): Effect.Effect<void, MutationQueueUnavailableError, CommandExecutor.CommandExecutor> =>
				add(mutation),
			process,
			rollback,
			clearAll: (): Effect.Effect<void> =>
				clearAll().pipe(Effect.zipRight(clearDaemonMutationQueue()), Effect.asVoid),

			hasPending: (taskId: string): Effect.Effect<boolean> =>
				Effect.gen(function* () {
					const daemonProjectPath = yield* resolveDaemonProjectPath(undefined)

					return yield* daemonQueueRpc
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
								Effect.logWarning("Daemon mutation queue query failed; assuming no pending state", {
									taskId,
									error,
								}).pipe(Effect.zipRight(Effect.succeed(false))),
							),
						)
				}),

			getMutations: (): Effect.Effect<ReadonlyMap<string, QueuedMutation>> => Ref.get(mutationsRef),
			getOptimisticMutations: (): Effect.Effect<ReadonlyMap<string, OptimisticQueuedMutation>> =>
				getOptimisticMutationsFromAdapter(),
		}
	}),
}) {}
