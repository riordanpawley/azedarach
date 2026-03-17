import { describe, expect, it } from "bun:test"
import { ParseResult, Schema } from "effect"
import {
	DAEMON_RPC_PROTOCOL_VERSION,
	DaemonControlStatusResultSchema,
	DaemonStatusRequestSchema,
} from "./DaemonRpcSchemas.js"

describe("DaemonRpc protocol version schemas", () => {
	it("defaults missing request protocol version to current literal", () => {
		const decoded = Schema.decodeUnknownSync(DaemonStatusRequestSchema)({})
		expect(decoded.rpcProtocolVersion).toBe(DAEMON_RPC_PROTOCOL_VERSION)
	})

	it("rejects response payloads with mismatched protocol version", () => {
		const result = Schema.decodeUnknownEither(DaemonControlStatusResultSchema)({
			rpcProtocolVersion: 999,
			checkedAtMs: 100,
			runtime: {
				protocolVersion: 1,
				runtimePhase: "ready",
				authoritativeRuntime: true,
				revision: 1,
				lifecycleGeneration: 1,
				lifecycleReason: "ok",
				recoveryGeneration: 0,
				capturedAtMs: 100,
				clients: {},
			},
			sync: {
				state: "running",
				generation: 1,
				projectPath: null,
				intervalMs: null,
				startedAtMs: null,
				runCount: 0,
				successCount: 0,
				failureCount: 0,
				failureStreak: 0,
				restartStreak: 0,
				lastBackoffMs: null,
				lastSuccessfulRunAtMs: null,
				lastRun: null,
				lastError: null,
			},
		})

		expect(result._tag).toBe("Left")
		if (result._tag !== "Left") {
			throw new Error("Expected decode failure")
		}
		expect(ParseResult.TreeFormatter.formatErrorSync(result.left)).toContain(
			String(DAEMON_RPC_PROTOCOL_VERSION),
		)
	})
})
