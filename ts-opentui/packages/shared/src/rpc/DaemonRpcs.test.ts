import { describe, expect, it } from "bun:test"
import { Effect, Schema } from "effect"
import {
	DaemonImplementationCreateRequestSchema,
	DaemonImplementationGetRegistryResultSchema,
} from "./DaemonImplementationRpcSchemas.js"
import {
	DAEMON_RPC_PROTOCOL_VERSION,
	DaemonAttachRequestSchema,
	DaemonBoardReadModelRequestSchema,
	DaemonBoardReadModelResultSchema,
	DaemonDevServerListResultSchema,
	DaemonDevServerStartRequestSchema,
	DaemonDevServerStatusRequestSchema,
	DaemonEventStreamResultSchema,
	DaemonHealthResultSchema,
	DaemonQueueCancelResultSchema,
	DaemonQueueEnqueueRequestSchema,
	DaemonQueueQueryResultSchema,
	DaemonSessionSnapshotResultSchema,
	DaemonSessionStartRequestSchema,
	DaemonSessionUpdateStateRequestSchema,
	DaemonStatusRequestSchema,
} from "./DaemonRpcSchemas.js"
import { DaemonImplementationRpcGroup, DaemonRpcGroup } from "./DaemonRpcs.js"

describe("DaemonRpcs", () => {
	it("registers all daemon rpc operations in a single group", () => {
		const keys = [...DaemonRpcGroup.requests.keys()].sort()
		expect(keys).toEqual([
			"daemonAttach",
			"daemonBoardReadModel",
			"daemonDevServerList",
			"daemonDevServerStart",
			"daemonDevServerStatus",
			"daemonDevServerStop",
			"daemonEventStream",
			"daemonHealth",
			"daemonHeartbeat",
			"daemonIssueAddDependency",
			"daemonIssueClose",
			"daemonIssueCreate",
			"daemonIssueDelete",
			"daemonIssueGet",
			"daemonIssueList",
			"daemonIssueRemoveDependency",
			"daemonIssueSync",
			"daemonIssueUpdate",
			"daemonLogs",
			"daemonQueueCancel",
			"daemonQueueEnqueue",
			"daemonQueueQuery",
			"daemonReconnect",
			"daemonRestart",
			"daemonSessionPause",
			"daemonSessionRecover",
			"daemonSessionResume",
			"daemonSessionSnapshot",
			"daemonSessionStart",
			"daemonSessionStop",
			"daemonSessionUpdateState",
			"daemonStatus",
			"daemonStop",
		])
	})

	it("registers implementation registry rpc operations in a dedicated group", () => {
		const keys = [...DaemonImplementationRpcGroup.requests.keys()].sort()
		expect(keys).toEqual([
			"daemonImplementationCreate",
			"daemonImplementationDelete",
			"daemonImplementationGetRegistry",
			"daemonImplementationSetDefault",
			"daemonImplementationUpdate",
		])
	})

	it("validates request schemas for protocol versioned operations", async () => {
		const decodeStatus = Schema.decodeUnknown(DaemonStatusRequestSchema)
		const decodeAttach = Schema.decodeUnknown(DaemonAttachRequestSchema)

		const status = await Effect.runPromise(
			decodeStatus({
				rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
			}),
		)
		const attach = await Effect.runPromise(
			decodeAttach({
				rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
				clientId: "client-a",
			}),
		)

		expect(status.rpcProtocolVersion).toBe(DAEMON_RPC_PROTOCOL_VERSION)
		expect(attach.clientId).toBe("client-a")
	})

	it("validates session mutation request schemas", async () => {
		const decodeStart = Schema.decodeUnknown(DaemonSessionStartRequestSchema)
		const decodeUpdate = Schema.decodeUnknown(DaemonSessionUpdateStateRequestSchema)

		const start = await Effect.runPromise(
			decodeStart({
				rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
				issueId: "qc",
				projectPath: "/tmp/project",
			}),
		)
		const update = await Effect.runPromise(
			decodeUpdate({
				rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
				issueId: "qc",
				state: "waiting",
				projectPath: "/tmp/project",
			}),
		)

		expect(start.issueId).toBe("qc")
		expect(start.projectPath).toBe("/tmp/project")
		expect(update.state).toBe("waiting")
	})

	it("validates devserver request and result schemas", async () => {
		const decodeStatusRequest = Schema.decodeUnknown(DaemonDevServerStatusRequestSchema)
		const decodeStartRequest = Schema.decodeUnknown(DaemonDevServerStartRequestSchema)
		const decodeListResult = Schema.decodeUnknown(DaemonDevServerListResultSchema)

		const statusRequest = await Effect.runPromise(
			decodeStatusRequest({
				rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
				issueId: "qp",
				serverName: "default",
				projectPath: "/tmp/project",
			}),
		)
		const startRequest = await Effect.runPromise(
			decodeStartRequest({
				rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
				issueId: "qp",
				projectPath: "/tmp/project",
			}),
		)
		const listResult = await Effect.runPromise(
			decodeListResult({
				rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
				capturedAtMs: 500,
				servers: [
					{
						issueId: "qp",
						serverName: "default",
						status: "running",
						port: 3001,
						windowName: "dev-default",
						tmuxSession: "az-qp",
						worktreePath: "/tmp/project/.worktrees/qp",
						projectPath: "/tmp/project",
						startedAt: "2026-03-14T06:00:00.000Z",
						error: null,
					},
				],
			}),
		)

		expect(statusRequest.serverName).toBe("default")
		expect(startRequest.projectPath).toBe("/tmp/project")
		expect(listResult.servers[0]?.status).toBe("running")
	})

	it("validates daemon health response shape", async () => {
		const decode = Schema.decodeUnknown(DaemonHealthResultSchema)
		const decoded = await Effect.runPromise(
			decode({
				rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
				checkedAtMs: 10,
				state: "healthy",
				reason: "daemon runtime is healthy",
				status: {
					rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
					checkedAtMs: 10,
					runtime: {
						protocolVersion: 1,
						runtimePhase: "ready",
						authoritativeRuntime: true,
						revision: 2,
						lifecycleGeneration: 1,
						lifecycleReason: "daemon bootstrap completed",
						recoveryGeneration: 0,
						capturedAtMs: 10,
						clients: {},
					},
					sync: {
						state: "running",
						generation: 1,
						projectPath: "/tmp/project",
						intervalMs: 5000,
						startedAtMs: 9,
						runCount: 1,
						successCount: 1,
						failureCount: 0,
						failureStreak: 0,
						restartStreak: 0,
						lastBackoffMs: null,
						lastSuccessfulRunAtMs: 10,
						lastRun: {
							runAtMs: 10,
							result: "flushed",
							pushed: 1,
							pulled: 1,
							message: null,
						},
						lastError: null,
					},
				},
			}),
		)

		expect(decoded.state).toBe("healthy")
		expect(decoded.status.runtime.runtimePhase).toBe("ready")
	})

	it("validates daemon session snapshot response shape", async () => {
		const decode = Schema.decodeUnknown(DaemonSessionSnapshotResultSchema)
		const decoded = await Effect.runPromise(
			decode({
				rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
				capturedAtMs: 123,
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
			}),
		)

		expect(decoded.sessions).toHaveLength(1)
		expect(decoded.sessions[0]?.issueId).toBe("qc")
		expect(decoded.sessions[0]?.state).toBe("busy")
	})

	it("validates daemon board read model request and response shape", async () => {
		const decodeRequest = Schema.decodeUnknown(DaemonBoardReadModelRequestSchema)
		const decodeResult = Schema.decodeUnknown(DaemonBoardReadModelResultSchema)
		const request = await Effect.runPromise(
			decodeRequest({
				rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
				projectPath: "/tmp/project",
			}),
		)
		const result = await Effect.runPromise(
			decodeResult({
				rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
				capturedAtMs: 789,
				projectPath: "/tmp/project",
				tasks: [
					{
						id: "sm-1",
						title: "Daemon board projection task",
						status: "in_progress",
						priority: 1,
						issue_type: "task",
						created_at: "2026-03-14T06:00:00.000Z",
						updated_at: "2026-03-14T06:01:00.000Z",
						implementations: ["ts-opentui"],
						sessionState: "busy",
						hasWorktree: true,
						gitBehindCount: 2,
						hasUncommittedChanges: true,
						gitAdditions: 12,
						gitDeletions: 3,
					},
				],
			}),
		)

		expect(request.projectPath).toBe("/tmp/project")
		expect(result.tasks).toHaveLength(1)
		expect(result.tasks[0]?.id).toBe("sm-1")
		expect(result.tasks[0]?.gitBehindCount).toBe(2)
	})

	it("validates implementation registry request and result schemas", async () => {
		const decodeRequest = Schema.decodeUnknown(DaemonImplementationCreateRequestSchema)
		const decodeResult = Schema.decodeUnknown(DaemonImplementationGetRegistryResultSchema)

		const request = await Effect.runPromise(
			decodeRequest({
				rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
				projectPath: "/tmp/project",
				input: {
					name: "alpha",
					description: "Alpha implementation",
					directory: "packages/alpha",
					setDefault: true,
				},
			}),
		)
		const result = await Effect.runPromise(
			decodeResult({
				rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
				registry: {
					default_implementation: "alpha",
					implicit_default_allowed: false,
					implementations: [
						{
							name: "alpha",
							description: "Alpha implementation",
							directory: "packages/alpha",
							created_at: "2026-03-14T06:00:00.000Z",
							updated_at: "2026-03-14T06:00:00.000Z",
							is_default: true,
							is_builtin: false,
						},
					],
				},
			}),
		)

		expect(request.input.name).toBe("alpha")
		expect(request.input.setDefault).toBe(true)
		expect(result.registry.default_implementation).toBe("alpha")
		expect(result.registry.implementations[0]?.is_default).toBe(true)
	})

	it("validates daemon event stream response shape", async () => {
		const decode = Schema.decodeUnknown(DaemonEventStreamResultSchema)
		const decoded = await Effect.runPromise(
			decode({
				rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
				polledAtMs: 456,
				nextCursor: 22,
				events: [
					{
						cursor: 21,
						emittedAtMs: 455,
						event: {
							_tag: "DaemonEventStreamSessionSnapshotEvent",
							capturedAtMs: 455,
							sessions: [],
						},
					},
				],
			}),
		)

		expect(decoded.nextCursor).toBe(22)
		expect(decoded.events[0]?.event._tag).toBe("DaemonEventStreamSessionSnapshotEvent")
	})

	it("validates daemon queue request and result schemas", async () => {
		const decodeEnqueue = Schema.decodeUnknown(DaemonQueueEnqueueRequestSchema)
		const decodeQueryResult = Schema.decodeUnknown(DaemonQueueQueryResultSchema)
		const decodeCancelResult = Schema.decodeUnknown(DaemonQueueCancelResultSchema)

		const enqueue = await Effect.runPromise(
			decodeEnqueue({
				rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
				domain: "command",
				operation: "sessionStart",
				projectPath: "/tmp/project",
				issueId: "qm",
				payloadJson: '{"issueId":"qm"}',
			}),
		)
		const query = await Effect.runPromise(
			decodeQueryResult({
				rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
				queriedAtMs: 777,
				items: [
					{
						domain: "mutation",
						operationId: "queue-op-1",
						operation: "applyTaskMutation",
						projectPath: "/tmp/project",
						issueId: "qm",
						dedupeKey: "qm:apply",
						payloadJson: '{"state":"busy"}',
						state: "queued",
						enqueuedAtMs: 770,
						startedAtMs: null,
						finishedAtMs: null,
						error: null,
					},
				],
			}),
		)
		const cancel = await Effect.runPromise(
			decodeCancelResult({
				rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
				cancelledAtMs: 778,
				cancelledOperationIds: ["queue-op-1"],
			}),
		)

		expect(enqueue.operation).toBe("sessionStart")
		expect(query.items[0]?.operationId).toBe("queue-op-1")
		expect(cancel.cancelledOperationIds).toEqual(["queue-op-1"])
	})
})
