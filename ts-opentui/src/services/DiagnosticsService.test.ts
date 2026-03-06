import { describe, expect, it } from "bun:test"
import { Effect } from "effect"
import { DiagnosticsService } from "./DiagnosticsService.js"

const runWithDiagnostics = <A>(program: Effect.Effect<A, never, DiagnosticsService>): Promise<A> =>
	Effect.runPromise(program.pipe(Effect.provide(DiagnosticsService.Default)))

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
})
