import { describe, expect, it } from "bun:test"
import { RpcClientError } from "@effect/rpc/RpcClientError"
import { Cause, Effect, Exit, Option, Ref } from "effect"
import {
	DaemonRpcProtocolVersionMismatchError,
	DaemonRpcRemoteActionError,
	DaemonRpcTransportError,
	type DaemonRpcWireClient,
	makeDaemonRpcClientFromWire,
} from "./DaemonRpcClient.js"
import {
	DAEMON_RPC_PROTOCOL_VERSION,
	type DaemonControlStatusResult,
	type DaemonEventStreamResult,
	type DaemonHealthResult,
	type DaemonHeartbeatResult,
	type DaemonLogsResult,
	type DaemonQueueCancelResult,
	type DaemonQueueEnqueueResult,
	type DaemonQueueItem,
	type DaemonQueueQueryResult,
	type DaemonSessionMutationResult,
	type DaemonSessionSnapshotResult,
} from "./DaemonRpcSchemas.js"

const makeStatus = (
	overrides: Partial<DaemonControlStatusResult> = {},
): DaemonControlStatusResult => ({
	rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
	checkedAtMs: 100,
	runtime: {
		protocolVersion: 1,
		runtimePhase: "ready",
		authoritativeRuntime: true,
		revision: 2,
		lifecycleGeneration: 1,
		lifecycleReason: "daemon bootstrap completed",
		recoveryGeneration: 0,
		capturedAtMs: 100,
		clients: {},
	},
	sync: {
		state: "running",
		generation: 1,
		projectPath: "/tmp/project",
		intervalMs: 5_000,
		startedAtMs: 90,
		runCount: 1,
		successCount: 1,
		failureCount: 0,
		failureStreak: 0,
		restartStreak: 0,
		lastBackoffMs: null,
		lastSuccessfulRunAtMs: 100,
		lastRun: {
			runAtMs: 100,
			result: "flushed",
			pushed: 1,
			pulled: 1,
			message: null,
		},
		lastError: null,
	},
	...overrides,
})

const makeHealth = (): DaemonHealthResult => ({
	rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
	checkedAtMs: 100,
	state: "healthy",
	reason: "daemon runtime is healthy",
	status: makeStatus(),
})

const makeLogs = (): DaemonLogsResult => ({
	rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
	logPath: "/tmp/project/az-cli.log",
	totalLines: 2,
	lines: ["a", "b"],
})

const makeHeartbeat = (): DaemonHeartbeatResult => ({
	rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
	client: {
		clientId: "client-a",
		connectedAtMs: 1,
		lastHeartbeatAtMs: 2,
		lastReconnectAtMs: null,
		lastSeenRevision: null,
		lastSeenLifecycleGeneration: null,
		lastRecoveryGeneration: null,
	},
})

const makeSessionSnapshot = (): DaemonSessionSnapshotResult => ({
	rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
	capturedAtMs: 100,
	sessions: [
		{
			issueId: "qc",
			worktreePath: "/tmp/project/.worktrees/qc",
			tmuxSessionName: "az-qc",
			state: "busy",
			startedAt: "2026-03-14T06:00:00.000Z",
			projectPath: "/tmp/project",
		},
	],
})

const makeSessionMutation = (): DaemonSessionMutationResult => ({
	rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
	capturedAtMs: 105,
	session: {
		issueId: "qc",
		worktreePath: "/tmp/project/.worktrees/qc",
		tmuxSessionName: "az-qc",
		state: "busy",
		startedAt: "2026-03-14T06:00:00.000Z",
		projectPath: "/tmp/project",
	},
})

const makeEventStreamResult = (): DaemonEventStreamResult => ({
	rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
	polledAtMs: 110,
	nextCursor: 14,
	events: [
		{
			cursor: 13,
			emittedAtMs: 109,
			event: {
				_tag: "DaemonEventStreamSessionSnapshotEvent",
				capturedAtMs: 109,
				sessions: [],
			},
		},
	],
})

const makeQueueItem = (): DaemonQueueItem => ({
	domain: "command",
	operationId: "queue-op-1",
	operation: "sessionStart",
	projectPath: "/tmp/project",
	issueId: "qm",
	dedupeKey: "qm:sessionStart",
	payloadJson: '{"issueId":"qm"}',
	state: "queued",
	enqueuedAtMs: 120,
	startedAtMs: null,
	finishedAtMs: null,
	error: null,
})

const makeQueueEnqueueResult = (): DaemonQueueEnqueueResult => ({
	rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
	acceptedAtMs: 121,
	item: makeQueueItem(),
})

const makeQueueQueryResult = (): DaemonQueueQueryResult => ({
	rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
	queriedAtMs: 122,
	items: [makeQueueItem()],
})

const makeQueueCancelResult = (): DaemonQueueCancelResult => ({
	rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
	cancelledAtMs: 123,
	cancelledOperationIds: ["queue-op-1"],
})

const makeWire = (overrides: Partial<DaemonRpcWireClient>): DaemonRpcWireClient => ({
	daemonStatus: () => Effect.succeed(makeStatus()),
	daemonHealth: () => Effect.succeed(makeHealth()),
	daemonLogs: () => Effect.succeed(makeLogs()),
	daemonStop: () => Effect.succeed(makeStatus()),
	daemonRestart: () => Effect.succeed(makeStatus()),
	daemonAttach: () =>
		Effect.succeed({
			rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
			clientId: "client-a",
			acceptedAtMs: 100,
			resumeToken: "client-a:2",
			negotiatedCapabilities: {
				authoritativeRuntime: true,
				lifecycleGenerationTracking: true,
				recoveryGenerationTracking: true,
				resumeToken: true,
			},
			handshake: {
				operation: "attach",
				requestedAtMs: 100,
				negotiatedAtMs: 100,
				requestedProtocolVersion: 1,
				negotiatedProtocolVersion: 1,
				serverSupportedProtocolVersions: [1],
				compatibilityDecision: "exact-match",
			},
			snapshot: makeStatus().runtime,
		}),
	daemonReconnect: () =>
		Effect.succeed({
			rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
			clientId: "client-a",
			acceptedAtMs: 101,
			resumeToken: "client-a:3",
			negotiatedCapabilities: {
				authoritativeRuntime: true,
				lifecycleGenerationTracking: true,
				recoveryGenerationTracking: true,
				resumeToken: true,
			},
			handshake: {
				operation: "reconnect",
				requestedAtMs: 101,
				negotiatedAtMs: 101,
				requestedProtocolVersion: 1,
				negotiatedProtocolVersion: 1,
				serverSupportedProtocolVersions: [1],
				compatibilityDecision: "exact-match",
			},
			snapshot: makeStatus().runtime,
		}),
	daemonHeartbeat: () => Effect.succeed(makeHeartbeat()),
	daemonEventStream: () => Effect.succeed(makeEventStreamResult()),
	daemonSessionSnapshot: () => Effect.succeed(makeSessionSnapshot()),
	daemonSessionStart: () => Effect.succeed(makeSessionMutation()),
	daemonSessionStop: () => Effect.succeed(makeSessionMutation()),
	daemonSessionPause: () => Effect.succeed(makeSessionMutation()),
	daemonSessionResume: () => Effect.succeed(makeSessionMutation()),
	daemonSessionRecover: () => Effect.succeed(makeSessionMutation()),
	daemonSessionUpdateState: () => Effect.succeed(makeSessionMutation()),
	daemonQueueEnqueue: () => Effect.succeed(makeQueueEnqueueResult()),
	daemonQueueQuery: () => Effect.succeed(makeQueueQueryResult()),
	daemonQueueCancel: () => Effect.succeed(makeQueueCancelResult()),
	...overrides,
})

describe("DaemonRpcClient", () => {
	it("injects rpc protocol version into outgoing requests", async () => {
		const payloadRef = await Effect.runPromise(Ref.make<number | null>(null))
		const client = makeDaemonRpcClientFromWire(
			makeWire({
				daemonStatus: (payload) =>
					Effect.gen(function* () {
						yield* Ref.set(payloadRef, payload.rpcProtocolVersion)
						return makeStatus()
					}),
			}),
		)

		const status = await Effect.runPromise(client.status())
		const captured = await Effect.runPromise(Ref.get(payloadRef))
		expect(status.rpcProtocolVersion).toBe(DAEMON_RPC_PROTOCOL_VERSION)
		expect(captured).toBe(DAEMON_RPC_PROTOCOL_VERSION)
	})

	it("fails with typed protocol mismatch error", async () => {
		const client = makeDaemonRpcClientFromWire(
			makeWire({
				daemonHealth: () =>
					Effect.succeed({
						...makeHealth(),
						rpcProtocolVersion: 99,
					}),
			}),
		)

		const exit = await Effect.runPromiseExit(client.health())
		expect(Exit.isFailure(exit)).toBe(true)
		if (!Exit.isFailure(exit)) {
			throw new Error("Expected protocol mismatch failure")
		}
		const failure = Cause.failureOption(exit.cause)
		expect(Option.isSome(failure)).toBe(true)
		if (!Option.isSome(failure)) {
			throw new Error("Expected protocol mismatch failure cause")
		}
		expect(failure.value).toBeInstanceOf(DaemonRpcProtocolVersionMismatchError)
	})

	it("maps rpc transport failures to actionable transport errors", async () => {
		const client = makeDaemonRpcClientFromWire(
			makeWire({
				daemonLogs: () =>
					Effect.fail(
						new RpcClientError({
							reason: "Protocol",
							message: "socket unavailable",
						}),
					),
			}),
		)

		const exit = await Effect.runPromiseExit(client.logs())
		expect(Exit.isFailure(exit)).toBe(true)
		if (!Exit.isFailure(exit)) {
			throw new Error("Expected transport failure")
		}
		const failure = Cause.failureOption(exit.cause)
		expect(Option.isSome(failure)).toBe(true)
		if (!Option.isSome(failure)) {
			throw new Error("Expected transport failure cause")
		}
		expect(failure.value).toBeInstanceOf(DaemonRpcTransportError)
		if (!(failure.value instanceof DaemonRpcTransportError)) {
			throw new Error("Expected DaemonRpcTransportError")
		}
		expect(failure.value.operation).toBe("logs")
		expect(failure.value.suggestion).toContain("az daemon health")
	})

	it("maps remote action errors to typed rpc action errors", async () => {
		const client = makeDaemonRpcClientFromWire(
			makeWire({
				daemonRestart: () =>
					Effect.fail({
						_tag: "DaemonRpcActionError",
						code: "MISSING_PROJECT",
						message: "project path is required",
						action: "provide project path",
					}),
			}),
		)

		const exit = await Effect.runPromiseExit(client.restart({}))
		expect(Exit.isFailure(exit)).toBe(true)
		if (!Exit.isFailure(exit)) {
			throw new Error("Expected remote action failure")
		}
		const failure = Cause.failureOption(exit.cause)
		expect(Option.isSome(failure)).toBe(true)
		if (!Option.isSome(failure)) {
			throw new Error("Expected remote action failure cause")
		}
		expect(failure.value).toBeInstanceOf(DaemonRpcRemoteActionError)
		if (!(failure.value instanceof DaemonRpcRemoteActionError)) {
			throw new Error("Expected DaemonRpcRemoteActionError")
		}
		expect(failure.value.code).toBe("MISSING_PROJECT")
		expect(failure.value.operation).toBe("restart")
	})

	it("maps session snapshot requests and responses through the shared client", async () => {
		const payloadRef = await Effect.runPromise(
			Ref.make<{ readonly rpcProtocolVersion: number; readonly projectPath?: string } | null>(null),
		)
		const client = makeDaemonRpcClientFromWire(
			makeWire({
				daemonSessionSnapshot: (payload) =>
					Effect.gen(function* () {
						yield* Ref.set(payloadRef, payload)
						return makeSessionSnapshot()
					}),
			}),
		)

		const result = await Effect.runPromise(client.sessionSnapshot!({ projectPath: "/tmp/project" }))
		const captured = await Effect.runPromise(Ref.get(payloadRef))
		expect(result.sessions[0]?.issueId).toBe("qc")
		expect(captured).toEqual({
			rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
			projectPath: "/tmp/project",
		})
	})

	it("injects protocol version and payload for session lifecycle mutations", async () => {
		const payloadRef = await Effect.runPromise(
			Ref.make<{
				readonly rpcProtocolVersion: number
				readonly issueId: string
				readonly projectPath: string
			} | null>(null),
		)
		const client = makeDaemonRpcClientFromWire(
			makeWire({
				daemonSessionStart: (payload) =>
					Effect.gen(function* () {
						yield* Ref.set(payloadRef, payload)
						return makeSessionMutation()
					}),
			}),
		)
		if (client.sessionStart === undefined) {
			throw new Error("Expected sessionStart method to be available")
		}

		const result = await Effect.runPromise(
			client.sessionStart({
				issueId: "qc",
				projectPath: "/tmp/project",
			}),
		)
		const captured = await Effect.runPromise(Ref.get(payloadRef))
		expect(result.session.issueId).toBe("qc")
		expect(captured).toEqual({
			rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
			issueId: "qc",
			projectPath: "/tmp/project",
		})
	})

	it("maps daemon event stream requests through the shared client", async () => {
		const payloadRef = await Effect.runPromise(
			Ref.make<{
				readonly rpcProtocolVersion: number
				readonly clientId: string
				readonly cursor?: number
				readonly batchSize?: number
			} | null>(null),
		)
		const client = makeDaemonRpcClientFromWire(
			makeWire({
				daemonEventStream: (payload) =>
					Effect.gen(function* () {
						yield* Ref.set(payloadRef, payload)
						return makeEventStreamResult()
					}),
			}),
		)
		if (client.eventStream === undefined) {
			throw new Error("Expected eventStream method to be available")
		}

		const result = await Effect.runPromise(
			client.eventStream({
				clientId: "board-ui:test",
				cursor: 12,
				batchSize: 20,
			}),
		)
		const captured = await Effect.runPromise(Ref.get(payloadRef))
		expect(result.nextCursor).toBe(14)
		expect(captured).toEqual({
			rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
			clientId: "board-ui:test",
			cursor: 12,
			batchSize: 20,
		})
	})

	it("maps remote action errors for session mutation operations", async () => {
		const client = makeDaemonRpcClientFromWire(
			makeWire({
				daemonSessionUpdateState: () =>
					Effect.fail({
						_tag: "DaemonRpcActionError",
						code: "INVALID_STATE_TRANSITION",
						message: "cannot transition from done to busy",
						action: "resume from paused instead",
					}),
			}),
		)
		if (client.sessionUpdateState === undefined) {
			throw new Error("Expected sessionUpdateState method to be available")
		}

		const exit = await Effect.runPromiseExit(
			client.sessionUpdateState({
				issueId: "qc",
				state: "busy",
			}),
		)
		expect(Exit.isFailure(exit)).toBe(true)
		if (!Exit.isFailure(exit)) {
			throw new Error("Expected remote action failure for sessionUpdateState")
		}
		const failure = Cause.failureOption(exit.cause)
		expect(Option.isSome(failure)).toBe(true)
		if (!Option.isSome(failure)) {
			throw new Error("Expected sessionUpdateState failure cause")
		}
		expect(failure.value).toBeInstanceOf(DaemonRpcRemoteActionError)
		if (!(failure.value instanceof DaemonRpcRemoteActionError)) {
			throw new Error("Expected DaemonRpcRemoteActionError")
		}
		expect(failure.value.operation).toBe("sessionUpdateState")
		expect(failure.value.code).toBe("INVALID_STATE_TRANSITION")
	})

	it("maps daemon queue operations through the shared client", async () => {
		const enqueuePayloadRef = await Effect.runPromise(
			Ref.make<{
				readonly rpcProtocolVersion: number
				readonly domain: "command" | "mutation"
				readonly operation: string
				readonly projectPath?: string
				readonly issueId?: string
				readonly dedupeKey?: string
				readonly payloadJson?: string
			} | null>(null),
		)
		const queryPayloadRef = await Effect.runPromise(
			Ref.make<{
				readonly rpcProtocolVersion: number
				readonly domain?: "command" | "mutation"
				readonly projectPath?: string
				readonly limit?: number
			} | null>(null),
		)
		const cancelPayloadRef = await Effect.runPromise(
			Ref.make<{
				readonly rpcProtocolVersion: number
				readonly operationId?: string
			} | null>(null),
		)
		const client = makeDaemonRpcClientFromWire(
			makeWire({
				daemonQueueEnqueue: (payload) =>
					Effect.gen(function* () {
						yield* Ref.set(enqueuePayloadRef, payload)
						return makeQueueEnqueueResult()
					}),
				daemonQueueQuery: (payload) =>
					Effect.gen(function* () {
						yield* Ref.set(queryPayloadRef, payload)
						return makeQueueQueryResult()
					}),
				daemonQueueCancel: (payload) =>
					Effect.gen(function* () {
						yield* Ref.set(cancelPayloadRef, payload)
						return makeQueueCancelResult()
					}),
			}),
		)
		if (client.queueEnqueue === undefined) {
			throw new Error("Expected queueEnqueue method to be available")
		}
		if (client.queueQuery === undefined) {
			throw new Error("Expected queueQuery method to be available")
		}
		if (client.queueCancel === undefined) {
			throw new Error("Expected queueCancel method to be available")
		}

		const enqueue = await Effect.runPromise(
			client.queueEnqueue({
				domain: "command",
				operation: "sessionStart",
				projectPath: "/tmp/project",
				issueId: "qm",
				dedupeKey: "qm:sessionStart",
			}),
		)
		const query = await Effect.runPromise(
			client.queueQuery({
				domain: "command",
				projectPath: "/tmp/project",
				limit: 20,
			}),
		)
		const cancel = await Effect.runPromise(client.queueCancel({ operationId: "queue-op-1" }))
		const enqueuePayload = await Effect.runPromise(Ref.get(enqueuePayloadRef))
		const queryPayload = await Effect.runPromise(Ref.get(queryPayloadRef))
		const cancelPayload = await Effect.runPromise(Ref.get(cancelPayloadRef))

		expect(enqueue.item.operationId).toBe("queue-op-1")
		expect(query.items).toHaveLength(1)
		expect(cancel.cancelledOperationIds).toEqual(["queue-op-1"])
		expect(enqueuePayload).toEqual({
			rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
			domain: "command",
			operation: "sessionStart",
			projectPath: "/tmp/project",
			issueId: "qm",
			dedupeKey: "qm:sessionStart",
		})
		expect(queryPayload).toEqual({
			rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
			domain: "command",
			projectPath: "/tmp/project",
			limit: 20,
		})
		expect(cancelPayload).toEqual({
			rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
			operationId: "queue-op-1",
		})
	})

	it("maps queue remote action errors to queue operation tags", async () => {
		const client = makeDaemonRpcClientFromWire(
			makeWire({
				daemonQueueCancel: () =>
					Effect.fail({
						_tag: "DaemonRpcActionError",
						code: "QUEUE_CANCEL_REJECTED",
						message: "cannot cancel running operation",
						action: "retry with operation id",
					}),
			}),
		)
		if (client.queueCancel === undefined) {
			throw new Error("Expected queueCancel method to be available")
		}

		const exit = await Effect.runPromiseExit(client.queueCancel({}))
		expect(Exit.isFailure(exit)).toBe(true)
		if (!Exit.isFailure(exit)) {
			throw new Error("Expected queue cancel remote action failure")
		}
		const failure = Cause.failureOption(exit.cause)
		expect(Option.isSome(failure)).toBe(true)
		if (!Option.isSome(failure)) {
			throw new Error("Expected queue cancel failure cause")
		}
		expect(failure.value).toBeInstanceOf(DaemonRpcRemoteActionError)
		if (!(failure.value instanceof DaemonRpcRemoteActionError)) {
			throw new Error("Expected DaemonRpcRemoteActionError")
		}
		expect(failure.value.operation).toBe("queueCancel")
		expect(failure.value.code).toBe("QUEUE_CANCEL_REJECTED")
	})
})
