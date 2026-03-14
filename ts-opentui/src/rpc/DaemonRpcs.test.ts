import { describe, expect, it } from "bun:test"
import { Effect, Schema } from "effect"
import {
	DAEMON_RPC_PROTOCOL_VERSION,
	DaemonAttachRequestSchema,
	DaemonHealthResultSchema,
	DaemonStatusRequestSchema,
} from "./DaemonRpcSchemas.js"
import { DaemonRpcGroup } from "./DaemonRpcs.js"

describe("DaemonRpcs", () => {
	it("registers all daemon rpc operations in a single group", () => {
		const keys = [...DaemonRpcGroup.requests.keys()].sort()
		expect(keys).toEqual([
			"daemonAttach",
			"daemonHealth",
			"daemonHeartbeat",
			"daemonLogs",
			"daemonReconnect",
			"daemonRestart",
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
})
