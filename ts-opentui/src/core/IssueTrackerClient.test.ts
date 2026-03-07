import { describe, expect, it } from "bun:test"
import { Effect } from "effect"
import {
	extractJsonPayload,
	getLinearCommandPerfMetadata,
	getSyncTargetForBackend,
	isLocalFirstIssueBackend,
	resolveConfiguredIssueBackend,
	shouldUseLinearReadFallback,
	withIssueDbTiming,
} from "./IssueTrackerClient.js"

interface RecordedTiming {
	readonly backend: "linear"
	readonly operation: string
	readonly kind: "read" | "write"
	readonly durationMs: number
	readonly success: boolean
}

describe("getLinearCommandPerfMetadata", () => {
	it("classifies read operations", () => {
		expect(getLinearCommandPerfMetadata(["i", "list"])).toEqual({
			operation: "i.list",
			kind: "read",
		})
		expect(getLinearCommandPerfMetadata(["i", "get", "AZ-123"])).toEqual({
			operation: "i.get",
			kind: "read",
		})
	})

	it("classifies write operations and unknowns", () => {
		expect(getLinearCommandPerfMetadata(["i", "update", "AZ-123"])).toEqual({
			operation: "i.update",
			kind: "write",
		})
		expect(getLinearCommandPerfMetadata(["i", "close", "AZ-123"])).toEqual({
			operation: "i.close",
			kind: "write",
		})
		expect(getLinearCommandPerfMetadata(["foo"])).toEqual({
			operation: "other",
			kind: "write",
		})
	})
})

describe("withIssueDbTiming", () => {
	it("records a success measurement", async () => {
		const records: RecordedTiming[] = []
		const result = await Effect.runPromise(
			withIssueDbTiming(
				{
					recorder: {
						recordIssueDbTiming: (record) =>
							Effect.sync(() => {
								records.push(record)
							}),
					},
					backend: "linear",
					operation: "i.list",
					kind: "read",
				},
				Effect.succeed("ok"),
			),
		)

		expect(result).toBe("ok")
		expect(records).toHaveLength(1)
		expect(records[0]?.operation).toBe("i.list")
		expect(records[0]?.success).toBe(true)
		expect((records[0]?.durationMs ?? -1) >= 0).toBe(true)
	})

	it("records a failure measurement and preserves the failure", async () => {
		const records: RecordedTiming[] = []
		await expect(
			Effect.runPromise(
				withIssueDbTiming(
					{
						recorder: {
							recordIssueDbTiming: (record) =>
								Effect.sync(() => {
									records.push(record)
								}),
						},
						backend: "linear",
						operation: "i.update",
						kind: "write",
					},
					Effect.fail(new Error("boom")),
				),
			),
		).rejects.toThrow("boom")

		expect(records).toHaveLength(1)
		expect(records[0]?.operation).toBe("i.update")
		expect(records[0]?.success).toBe(false)
		expect((records[0]?.durationMs ?? -1) >= 0).toBe(true)
	})
})

describe("extractJsonPayload", () => {
	it("keeps already valid JSON unchanged", () => {
		const output = '[{"id":"AZE-1"}]'
		expect(extractJsonPayload(output)).toBe(output)
	})

	it("extracts JSON when linear-cli prepends process output", () => {
		const output =
			"process list refresh started\n" +
			'[{"id":"AZE-1","title":"Board load","state":{"name":"Backlog"}}]\n' +
			"process list refresh finished"

		expect(JSON.parse(extractJsonPayload(output))).toEqual([
			{
				id: "AZE-1",
				title: "Board load",
				state: { name: "Backlog" },
			},
		])
	})
})

describe("local-first backend selection", () => {
	it("resolves local backend without linear API assumptions", () => {
		const backend = resolveConfiguredIssueBackend({
			local: { syncEnabled: false },
		})
		expect(backend).toBe("local")
		expect(isLocalFirstIssueBackend(backend)).toBe(true)
		expect(getSyncTargetForBackend(backend)).toBeUndefined()
	})

	it("uses linear sync target for linear backend", () => {
		const backend = resolveConfiguredIssueBackend({
			linear: { syncEnabled: true },
		})
		expect(backend).toBe("linear")
		expect(isLocalFirstIssueBackend(backend)).toBe(true)
		expect(getSyncTargetForBackend(backend)).toBe("linear")
	})

	it("keeps legacy backends on non local-first path", () => {
		const bdBackend = resolveConfiguredIssueBackend({
			tracker: { syncEnabled: true },
		})
		const brBackend = resolveConfiguredIssueBackend({
			legacy: { syncEnabled: true },
		})

		expect(isLocalFirstIssueBackend(bdBackend)).toBe(false)
		expect(isLocalFirstIssueBackend(brBackend)).toBe(false)
		expect(getSyncTargetForBackend(bdBackend)).toBeUndefined()
		expect(getSyncTargetForBackend(brBackend)).toBeUndefined()
	})
})

describe("shouldUseLinearReadFallback", () => {
	it("enables fallback when linear read still misses requested issues after a zero-pull sync", () => {
		expect(
			shouldUseLinearReadFallback({
				backend: "linear",
				requestedCount: 1,
				localResultCount: 0,
				syncPulledCount: 0,
			}),
		).toBe(true)
	})

	it("does not fallback when local already satisfies the request", () => {
		expect(
			shouldUseLinearReadFallback({
				backend: "linear",
				requestedCount: 2,
				localResultCount: 2,
				syncPulledCount: 0,
			}),
		).toBe(false)
	})

	it("does not fallback for non-linear backends", () => {
		expect(
			shouldUseLinearReadFallback({
				backend: "local",
				requestedCount: 1,
				localResultCount: 0,
				syncPulledCount: 0,
			}),
		).toBe(false)
	})

	it("does not fallback when sync already pulled remote data", () => {
		expect(
			shouldUseLinearReadFallback({
				backend: "linear",
				requestedCount: 1,
				localResultCount: 0,
				syncPulledCount: 3,
			}),
		).toBe(false)
	})
})
