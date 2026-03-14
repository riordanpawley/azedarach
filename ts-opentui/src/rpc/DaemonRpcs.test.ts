import { describe, expect, it } from "bun:test"
import { Effect, Schema } from "effect"
import {
	DAEMON_RPC_PROTOCOL_VERSION,
	DaemonAttachRequestSchema,
	DaemonEventStreamResultSchema,
	DaemonHealthResultSchema,
	DaemonSessionSnapshotResultSchema,
	DaemonSessionStartRequestSchema,
	DaemonSessionUpdateStateRequestSchema,
	DaemonStatusRequestSchema,
} from "./DaemonRpcSchemas.js"
import { DaemonRpcGroup } from "./DaemonRpcs.js"

describe("DaemonRpcs", () => {
	it("registers all daemon rpc operations in a single group", () => {
		const keys = [...DaemonRpcGroup.requests.keys()].sort()
		expect(keys).toEqual([
			"daemonAttach",
			"daemonEventStream",
			"daemonHealth",
			"daemonHeartbeat",
			"daemonLogs",
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
})
