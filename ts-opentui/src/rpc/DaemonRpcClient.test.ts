import { describe, expect, it } from "bun:test"
import { RpcClientError } from "@effect/rpc/RpcClientError"
import { ParseResult, Schema } from "effect"
import {
	classifyDaemonRpcClientError,
	isDaemonRpcClientProtocolMismatch,
	isDaemonRpcClientRetryableTransport,
} from "./DaemonRpcClient.js"
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

describe("DaemonRpcClient error classification", () => {
	it("classifies protocol failures as fail-fast mismatch errors", () => {
		const error = new RpcClientError({
			reason: "Protocol",
			message: "version mismatch",
		})

		expect(classifyDaemonRpcClientError(error)).toBe("protocol-mismatch")
		expect(isDaemonRpcClientProtocolMismatch(error)).toBe(true)
		expect(isDaemonRpcClientRetryableTransport(error)).toBe(false)
	})

	it("classifies unknown rpc client failures as retryable transport errors", () => {
		const error = new RpcClientError({
			reason: "Unknown",
			message: "socket unavailable",
		})

		expect(classifyDaemonRpcClientError(error)).toBe("transport")
		expect(isDaemonRpcClientProtocolMismatch(error)).toBe(false)
		expect(isDaemonRpcClientRetryableTransport(error)).toBe(true)
	})
})
