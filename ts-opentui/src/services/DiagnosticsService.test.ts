import { describe, expect, it } from "bun:test"
import { Effect, Fiber, type Scope } from "effect"
import { DiagnosticsService } from "./DiagnosticsService.js"

const runWithDiagnostics = <A>(program: Effect.Effect<A, never, DiagnosticsService>): Promise<A> =>
	Effect.runPromise(program.pipe(Effect.provide(DiagnosticsService.Default)))

const runWithScopedDiagnostics = <A>(
	program: Effect.Effect<A, never, DiagnosticsService | Scope.Scope>,
): Promise<A> =>
	Effect.runPromise(program.pipe(Effect.provide(DiagnosticsService.Default), Effect.scoped))

describe("DiagnosticsService issue db perf tracking", () => {
	it("aggregates latency and failure stats", async () => {
		const stats = await runWithDiagnostics(
			Effect.gen(function* () {
				const diagnostics = yield* DiagnosticsService
				yield* diagnostics.recordIssueDbTiming({
					backend: "linear",
					operation: "i.list",
					kind: "read",
					durationMs: 100,
					success: true,
				})
				yield* diagnostics.recordIssueDbTiming({
					backend: "linear",
					operation: "i.list",
					kind: "read",
					durationMs: 200,
					success: true,
				})
				yield* diagnostics.recordIssueDbTiming({
					backend: "linear",
					operation: "i.list",
					kind: "read",
					durationMs: 400,
					success: false,
				})
				const snapshot = yield* diagnostics.getSnapshot()
				return snapshot.issueDbPerf[0]
			}),
		)

		expect(stats?.backend).toBe("linear")
		expect(stats?.operation).toBe("i.list")
		expect(stats?.kind).toBe("read")
		expect(stats?.count).toBe(3)
		expect(stats?.failureCount).toBe(1)
		expect(stats?.avgMs).toBe(233)
		expect(stats?.p50Ms).toBe(200)
		expect(stats?.p95Ms).toBe(400)
		expect(stats?.maxMs).toBe(400)
		expect(stats?.lastMs).toBe(400)
		expect(stats?.lastStatus).toBe("failure")
	})

	it("uses a bounded sample window for percentile metrics", async () => {
		const stats = await runWithDiagnostics(
			Effect.gen(function* () {
				const diagnostics = yield* DiagnosticsService
				for (let duration = 1; duration <= 210; duration += 1) {
					yield* diagnostics.recordIssueDbTiming({
						backend: "linear",
						operation: "i.get",
						kind: "read",
						durationMs: duration,
						success: true,
					})
				}
				const snapshot = yield* diagnostics.getSnapshot()
				return snapshot.issueDbPerf[0]
			}),
		)

		expect(stats?.count).toBe(210)
		expect(stats?.maxMs).toBe(210)
		// Percentiles come from the latest 200 samples: [11..210]
		expect(stats?.p50Ms).toBe(110)
		expect(stats?.p95Ms).toBe(200)
	})

	it("emits slow-call events at read/write thresholds", async () => {
		const events = await runWithDiagnostics(
			Effect.gen(function* () {
				const diagnostics = yield* DiagnosticsService
				yield* diagnostics.recordIssueDbTiming({
					backend: "linear",
					operation: "i.list",
					kind: "read",
					durationMs: 299,
					success: true,
				})
				yield* diagnostics.recordIssueDbTiming({
					backend: "linear",
					operation: "i.list",
					kind: "read",
					durationMs: 300,
					success: true,
				})
				yield* diagnostics.recordIssueDbTiming({
					backend: "linear",
					operation: "i.update",
					kind: "write",
					durationMs: 500,
					success: false,
				})
				const snapshot = yield* diagnostics.getSnapshot()
				return snapshot.events.filter((event) => event.source === "IssueDbPerf")
			}),
		)

		expect(events).toHaveLength(2)
		expect(events[0]?.message).toContain("i.list")
		expect(events[1]?.message).toContain("i.update")
	})
})

describe("DiagnosticsService issue sync health", () => {
	it("stores sync health snapshots", async () => {
		const health = await runWithDiagnostics(
			Effect.gen(function* () {
				const diagnostics = yield* DiagnosticsService
				yield* diagnostics.setIssueSyncHealth({
					backend: "linear",
					syncEnabled: true,
					queueDepth: 3,
					failedCount: 1,
					lastStatus: "failure",
					lastMessage: "flush failed",
					lastFailure: {
						issueId: "AZE-42",
						operation: "upsert",
						error: "boom",
						attempts: 2,
						occurredAt: new Date("2026-01-01T00:00:00.000Z"),
					},
				})
				const snapshot = yield* diagnostics.getSnapshot()
				return snapshot.issueSync
			}),
		)

		expect(health?.backend).toBe("linear")
		expect(health?.syncEnabled).toBe(true)
		expect(health?.queueDepth).toBe(3)
		expect(health?.failedCount).toBe(1)
		expect(health?.lastStatus).toBe("failure")
		expect(health?.lastMessage).toBe("flush failed")
		expect(health?.lastFailure?.issueId).toBe("AZE-42")
	})

	it("marks bootstrap completeness skips as unverified until a remote pull is observed", async () => {
		const health = await runWithDiagnostics(
			Effect.gen(function* () {
				const diagnostics = yield* DiagnosticsService
				yield* diagnostics.setIssueSyncHealth({
					backend: "linear",
					syncEnabled: true,
					queueDepth: 0,
					failedCount: 0,
					lastStatus: "skipped",
					lastMessage: "bootstrap skipped (already complete)",
					lastRun: {
						runId: "run-1",
						operation: "bootstrap",
						status: "skipped",
						startedAt: new Date("2026-03-07T00:00:00.000Z"),
						finishedAt: new Date("2026-03-07T00:00:01.000Z"),
						message: "bootstrap skipped (already complete)",
						pushed: 0,
						pulled: 0,
					},
				})
				const snapshot = yield* diagnostics.getSnapshot()
				return snapshot.issueSync
			}),
		)

		expect(health?.lastStatus).toBe("failure")
		expect(health?.lastMessage).toContain("remote completeness unverified")
		expect(health?.lastRun?.status).toBe("failure")
		expect(health?.lastRun?.message).toContain("remote completeness unverified")
	})

	it("keeps bootstrap skip status once a remote pull has been observed", async () => {
		const health = await runWithDiagnostics(
			Effect.gen(function* () {
				const diagnostics = yield* DiagnosticsService
				yield* diagnostics.setIssueSyncHealth({
					backend: "linear",
					syncEnabled: true,
					queueDepth: 0,
					failedCount: 0,
					lastStatus: "success",
					lastMessage:
						"flush processed 1 item(s), pushed 1 claim(s), pulled 3 issue snapshot(s), remaining=0",
					lastRun: {
						runId: "run-0",
						operation: "flush",
						status: "success",
						startedAt: new Date("2026-03-07T00:00:00.000Z"),
						finishedAt: new Date("2026-03-07T00:00:01.000Z"),
						message:
							"flush processed 1 item(s), pushed 1 claim(s), pulled 3 issue snapshot(s), remaining=0",
						pushed: 1,
						pulled: 3,
					},
				})
				yield* diagnostics.setIssueSyncHealth({
					backend: "linear",
					syncEnabled: true,
					queueDepth: 0,
					failedCount: 0,
					lastStatus: "skipped",
					lastMessage: "bootstrap skipped (already complete)",
					lastRun: {
						runId: "run-1",
						operation: "bootstrap",
						status: "skipped",
						startedAt: new Date("2026-03-07T00:00:02.000Z"),
						finishedAt: new Date("2026-03-07T00:00:03.000Z"),
						message: "bootstrap skipped (already complete)",
						pushed: 0,
						pulled: 0,
					},
				})
				const snapshot = yield* diagnostics.getSnapshot()
				return snapshot.issueSync
			}),
		)

		expect(health?.lastStatus).toBe("skipped")
		expect(health?.lastMessage).toBe("bootstrap skipped (already complete)")
		expect(health?.lastRun?.status).toBe("skipped")
		expect(health?.lastRun?.message).toBe("bootstrap skipped (already complete)")
	})
})

describe("DiagnosticsService linear webhook health", () => {
	it("stores linear webhook health snapshots", async () => {
		const health = await runWithDiagnostics(
			Effect.gen(function* () {
				const diagnostics = yield* DiagnosticsService
				yield* diagnostics.setLinearWebhookHealth({
					mode: "misconfigured",
					strategy: "polling-fallback",
					healthy: false,
					message: "Webhook URL missing; using polling.",
					updatedAt: new Date("2026-03-06T00:00:00.000Z"),
				})
				const snapshot = yield* diagnostics.getSnapshot()
				return snapshot.linearWebhook
			}),
		)

		expect(health?.mode).toBe("misconfigured")
		expect(health?.strategy).toBe("polling-fallback")
		expect(health?.healthy).toBe(false)
		expect(health?.message).toContain("using polling")
	})
})

describe("DiagnosticsService fiber tracking", () => {
	it("marks interrupted fibers as interrupted instead of failed", async () => {
		const fiber = await runWithScopedDiagnostics(
			Effect.gen(function* () {
				const diagnostics = yield* DiagnosticsService
				const fiber = yield* Effect.fork(Effect.never)
				yield* diagnostics.registerFiber({
					id: "board-background-polling",
					name: "Board Background Polling",
					description: "Refreshes board periodically",
					fiber,
				})
				yield* Fiber.interrupt(fiber)
				for (let attempt = 0; attempt < 20; attempt += 1) {
					const snapshot = yield* diagnostics.getSnapshot()
					const entry = snapshot.fibers.find((entry) => entry.id === "board-background-polling")
					if (entry?.status !== "running") {
						return entry
					}
					yield* Effect.sleep("1 millis")
				}
				const snapshot = yield* diagnostics.getSnapshot()
				return snapshot.fibers.find((entry) => entry.id === "board-background-polling")
			}),
		)

		expect(fiber?.status).toBe("interrupted")
		expect(fiber?.error).toBeUndefined()
		expect(fiber?.endedAt).toBeInstanceOf(Date)
	})
})
