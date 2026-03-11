import { describe, expect, it } from "bun:test"
import { Effect, Schema } from "effect"
import {
	buildLinearFallbackSnapshots,
	buildLinearIssuesListPageQuery,
	collectLinearFallbackIssuesById,
	extractJsonPayload,
	getLinearCommandPerfMetadata,
	getSyncTargetForBackend,
	type Issue,
	isHiddenIssueStatus,
	isLocalFirstIssueBackend,
	resolveConfiguredIssueBackend,
	resolveSyncProjectPathValue,
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

		const parsed = Schema.decodeUnknownSync(
			Schema.parseJson(
				Schema.Array(
					Schema.Struct({
						id: Schema.String,
						title: Schema.String,
						state: Schema.Struct({ name: Schema.String }),
					}),
				),
			),
		)(extractJsonPayload(output))
		expect(parsed).toEqual([
			{
				id: "AZE-1",
				title: "Board load",
				state: { name: "Backlog" },
			},
		])
	})
})

describe("buildLinearIssuesListPageQuery", () => {
	it("omits after when cursor is undefined or null", () => {
		expect(buildLinearIssuesListPageQuery(undefined)).toEqual({ first: 250 })
		expect(buildLinearIssuesListPageQuery(null)).toEqual({ first: 250 })
	})

	it("includes after when cursor is present", () => {
		expect(buildLinearIssuesListPageQuery("cursor-1")).toEqual({
			first: 250,
			after: "cursor-1",
		})
	})
})

describe("isHiddenIssueStatus", () => {
	it("treats archived issues as hidden by default but allows explicit inclusion", () => {
		expect(isHiddenIssueStatus("archived")).toBe(true)
		expect(isHiddenIssueStatus("archived", { includeArchived: true })).toBe(false)
	})

	it("always hides tombstones and keeps board statuses visible", () => {
		expect(isHiddenIssueStatus("tombstone")).toBe(true)
		expect(isHiddenIssueStatus("closed")).toBe(false)
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

describe("resolveSyncProjectPathValue", () => {
	it("returns the selected project path when present", () => {
		expect(
			resolveSyncProjectPathValue({
				selectedPath: "  /tmp/project-a  ",
				fallbackProjectPath: "/tmp/fallback",
			}),
		).toBe("/tmp/project-a")
	})

	it("falls back when selected project path is empty", () => {
		expect(
			resolveSyncProjectPathValue({
				selectedPath: "   ",
				fallbackProjectPath: "/tmp/fallback",
			}),
		).toBe("/tmp/fallback")
	})

	it("captures a stable path value before later context changes", () => {
		let currentPath = "/tmp/project-a"
		const captured = resolveSyncProjectPathValue({
			selectedPath: currentPath,
			fallbackProjectPath: "/tmp/fallback",
		})
		currentPath = "/tmp/project-b"
		expect(currentPath).toBe("/tmp/project-b")
		expect(captured).toBe("/tmp/project-a")
	})
})

describe("buildLinearFallbackSnapshots", () => {
	const now = "2026-03-07T02:00:00.000Z"

	const makeIssue = (overrides: Partial<Issue>): Issue => ({
		id: "AZE-1",
		title: "Issue",
		status: "open",
		priority: 2,
		issue_type: "task",
		created_at: now,
		updated_at: now,
		implementations: ["default"],
		...overrides,
	})

	it("maps fallback issues into external snapshots with external refs and parent links", () => {
		const issue = makeIssue({
			id: "AZE-42",
			title: "Backfill me",
			labels: ["type:task", "backend"],
			dependencies: [
				{
					id: "AZE-1",
					dependency_type: "parent-child",
				},
				{
					id: "AZE-2",
					dependency_type: "related",
				},
			],
		})

		expect(
			buildLinearFallbackSnapshots(
				[issue],
				new Map([
					[
						"AZE-42",
						{
							externalId: "4f7b8f9d-2fcb-4d7e-92e2-2fbf4cedd54b",
							externalKey: "AZE-42",
						},
					],
				]),
			),
		).toEqual([
			{
				localId: "AZE-42",
				externalId: "4f7b8f9d-2fcb-4d7e-92e2-2fbf4cedd54b",
				externalKey: "AZE-42",
				title: "Backfill me",
				description: undefined,
				status: "open",
				priority: 2,
				issueType: "task",
				createdAt: now,
				updatedAt: now,
				closedAt: undefined,
				assignee: undefined,
				labels: ["type:task", "backend"],
				notes: undefined,
				design: undefined,
				acceptance: undefined,
				estimate: undefined,
				parentLocalId: "AZE-1",
			},
		])
	})

	it("drops issues whose external refs cannot be resolved", () => {
		const issue = makeIssue({ id: "AZE-999" })
		expect(buildLinearFallbackSnapshots([issue], new Map())).toEqual([])
	})
})

describe("collectLinearFallbackIssuesById", () => {
	const now = "2026-03-07T02:00:00.000Z"

	const makeIssue = (id: string): Issue => ({
		id,
		title: id,
		status: "open",
		priority: 2,
		issue_type: "task",
		created_at: now,
		updated_at: now,
		implementations: ["default"],
	})

	it("keeps successful fallback issues when some IDs fail and preserves requested order", async () => {
		const calls: string[] = []
		const fallbackById = (id: string) =>
			Effect.gen(function* () {
				calls.push(id)
				switch (id) {
					case "AZE-2":
						return yield* Effect.fail(new Error("boom"))
					case "AZE-1":
						return [makeIssue("AZE-1")]
					case "AZE-3":
						return [makeIssue("AZE-3")]
					default:
						return []
				}
			})

		const issues = await Effect.runPromise(
			collectLinearFallbackIssuesById(["AZE-1", "AZE-2", "AZE-3"], fallbackById),
		)

		expect(calls).toEqual(["AZE-1", "AZE-2", "AZE-3"])
		expect(issues.map((issue) => issue.id)).toEqual(["AZE-1", "AZE-3"])
	})
})
