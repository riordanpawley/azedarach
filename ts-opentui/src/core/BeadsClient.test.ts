import { describe, expect, it } from "bun:test"
import { Effect } from "effect"
import { getLinearCommandPerfMetadata, withIssueDbTiming } from "./BeadsClient.js"

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
