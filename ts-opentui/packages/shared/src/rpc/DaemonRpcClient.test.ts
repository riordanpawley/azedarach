import { describe, expect, it } from "bun:test"
import { RpcClientError } from "@effect/rpc/RpcClientError"
import { ParseResult, Schema } from "effect"
import {
	DaemonPlanningCreateIssuesRequestSchema,
	DaemonPlanningGenerateRequestSchema,
	DaemonPlanningRefineRequestSchema,
	DaemonPlanningReviewRequestSchema,
	type PlanningPlan,
	PlanningPlanSchema,
	type PlanningReviewFeedback,
	PlanningReviewFeedbackSchema,
} from "./DaemonPlanningRpcSchemas.js"
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

	it("defaults planning request protocol versions and round-trips planning codecs", () => {
		const generate = Schema.decodeUnknownSync(DaemonPlanningGenerateRequestSchema)({
			featureDescription: "Add dark mode",
		})
		const review = Schema.decodeUnknownSync(DaemonPlanningReviewRequestSchema)({
			plan: {
				epicTitle: "Dark mode",
				epicDescription: "Add dark mode support.",
				summary: "Split theme, persistence, and UI tasks.",
				tasks: [],
			},
		})
		const refine = Schema.decodeUnknownSync(DaemonPlanningRefineRequestSchema)({
			plan: {
				epicTitle: "Dark mode",
				epicDescription: "Add dark mode support.",
				summary: "Split theme, persistence, and UI tasks.",
				tasks: [],
			},
			feedback: {
				score: 90,
				issues: [],
				suggestions: [],
				parallelizationOpportunities: [],
				tasksTooLarge: [],
				missingDependencies: [],
				isApproved: true,
			},
		})
		const createIssues = Schema.decodeUnknownSync(DaemonPlanningCreateIssuesRequestSchema)({
			plan: {
				epicTitle: "Dark mode",
				epicDescription: "Add dark mode support.",
				summary: "Split theme, persistence, and UI tasks.",
				tasks: [],
			},
		})
		const plan: PlanningPlan = {
			epicTitle: "Dark mode",
			epicDescription: "Add dark mode support.",
			summary: "Split theme, persistence, and UI tasks.",
			tasks: [],
		}
		const encodedPlan = Schema.encodeSync(PlanningPlanSchema)(plan)
		const encodedFeedback = Schema.encodeSync(PlanningReviewFeedbackSchema)({
			score: 90,
			issues: [],
			suggestions: [],
			parallelizationOpportunities: [],
			tasksTooLarge: [],
			missingDependencies: [],
			isApproved: true,
		})

		expect(generate.rpcProtocolVersion).toBe(DAEMON_RPC_PROTOCOL_VERSION)
		expect(review.rpcProtocolVersion).toBe(DAEMON_RPC_PROTOCOL_VERSION)
		expect(refine.rpcProtocolVersion).toBe(DAEMON_RPC_PROTOCOL_VERSION)
		expect(createIssues.rpcProtocolVersion).toBe(DAEMON_RPC_PROTOCOL_VERSION)
		expect(Schema.decodeUnknownSync(PlanningPlanSchema)(encodedPlan)).toEqual(plan)
		const feedback: PlanningReviewFeedback = {
			score: 90,
			issues: [],
			suggestions: [],
			parallelizationOpportunities: [],
			tasksTooLarge: [],
			missingDependencies: [],
			isApproved: true,
		}

		expect(Schema.decodeUnknownSync(PlanningReviewFeedbackSchema)(encodedFeedback)).toEqual(
			feedback,
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
