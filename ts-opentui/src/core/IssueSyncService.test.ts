import { describe, expect, it } from "bun:test"
import {
	buildLinearIssueFilter,
	buildLinearIssuesPageQuery,
	groupLinearSyncBatchByIdentity,
	resolveCollapsedSyncOperation,
	shouldRetryUpsertForMissingParent,
	shouldRunRemoteHydration,
} from "./IssueSyncService.js"

describe("resolveCollapsedSyncOperation", () => {
	it("keeps close when no prior upsert exists", () => {
		const operation = resolveCollapsedSyncOperation({
			latestOperation: "close",
			groupedOperations: ["close"],
		})
		expect(operation).toBe("close")
	})

	it("promotes create-then-close to upsert", () => {
		const operation = resolveCollapsedSyncOperation({
			latestOperation: "close",
			groupedOperations: ["upsert", "close"],
		})
		expect(operation).toBe("upsert")
	})

	it("keeps non-close operations unchanged", () => {
		expect(
			resolveCollapsedSyncOperation({
				latestOperation: "delete",
				groupedOperations: ["upsert", "delete"],
			}),
		).toBe("delete")
	})
})

describe("shouldRunRemoteHydration", () => {
	it("runs when hydration has never executed", () => {
		expect(
			shouldRunRemoteHydration({
				nowMs: 10_000,
				lastHydrationAtMs: undefined,
				minIntervalMs: 60_000,
			}),
		).toBe(true)
	})

	it("skips when within cooldown interval", () => {
		expect(
			shouldRunRemoteHydration({
				nowMs: 70_000,
				lastHydrationAtMs: 20_000,
				minIntervalMs: 60_000,
			}),
		).toBe(false)
	})

	it("runs once interval threshold is reached", () => {
		expect(
			shouldRunRemoteHydration({
				nowMs: 80_000,
				lastHydrationAtMs: 20_000,
				minIntervalMs: 60_000,
			}),
		).toBe(true)
	})
})

describe("shouldRetryUpsertForMissingParent", () => {
	it("requires retry when parent exists locally but external ref is missing", () => {
		expect(
			shouldRetryUpsertForMissingParent({
				parentLocalId: "AZE-101",
				parentExternalId: undefined,
			}),
		).toBe(true)
	})

	it("does not retry when parent mapping is available", () => {
		expect(
			shouldRetryUpsertForMissingParent({
				parentLocalId: "AZE-101",
				parentExternalId: "c1234",
			}),
		).toBe(false)
	})

	it("does not retry when issue has no parent", () => {
		expect(
			shouldRetryUpsertForMissingParent({
				parentLocalId: undefined,
				parentExternalId: undefined,
			}),
		).toBe(false)
	})
})

describe("groupLinearSyncBatchByIdentity", () => {
	it("dedupes identical requests into one execution group", () => {
		const groups = groupLinearSyncBatchByIdentity([
			{
				issueId: "AZE-101",
				operation: "upsert",
				payloadJson: '{"title":"same"}',
				projectPath: "/tmp/project",
			},
			{
				issueId: "AZE-101",
				operation: "upsert",
				payloadJson: '{"title":"same"}',
				projectPath: "/tmp/project",
			},
			{
				issueId: "AZE-101",
				operation: "close",
				payloadJson: null,
				projectPath: "/tmp/project",
			},
		])

		expect(groups).toHaveLength(2)
		expect(groups[0]?.length).toBe(2)
		expect(groups[1]?.length).toBe(1)
	})

	it("keeps requests distinct when payload or project path differs", () => {
		const groups = groupLinearSyncBatchByIdentity([
			{
				issueId: "AZE-200",
				operation: "upsert",
				payloadJson: '{"title":"first"}',
				projectPath: "/tmp/project-a",
			},
			{
				issueId: "AZE-200",
				operation: "upsert",
				payloadJson: '{"title":"second"}',
				projectPath: "/tmp/project-a",
			},
			{
				issueId: "AZE-200",
				operation: "upsert",
				payloadJson: '{"title":"first"}',
				projectPath: "/tmp/project-b",
			},
		])

		expect(groups).toHaveLength(3)
		expect(groups.every((group) => group.length === 1)).toBe(true)
	})
})

describe("buildLinearIssueFilter", () => {
	it("treats non-UUID team values as key/name filters only", () => {
		expect(buildLinearIssueFilter({ team: "AZE", project: undefined })).toEqual({
			team: {
				or: [{ key: { eqIgnoreCase: "AZE" } }, { name: { eqIgnoreCase: "AZE" } }],
			},
		})
	})

	it("treats non-UUID project values as slug/name filters only", () => {
		expect(buildLinearIssueFilter({ team: undefined, project: "core-platform" })).toEqual({
			project: {
				or: [
					{ slugId: { eqIgnoreCase: "core-platform" } },
					{ name: { eqIgnoreCase: "core-platform" } },
				],
			},
		})
	})

	it("includes id filter when a UUID scope value is provided", () => {
		expect(
			buildLinearIssueFilter({
				team: "3f8f18b7-1c70-4374-a327-c0a5f8faec2c",
				project: undefined,
			}),
		).toEqual({
			team: {
				or: [
					{ id: { eq: "3f8f18b7-1c70-4374-a327-c0a5f8faec2c" } },
					{ key: { eqIgnoreCase: "3f8f18b7-1c70-4374-a327-c0a5f8faec2c" } },
					{ name: { eqIgnoreCase: "3f8f18b7-1c70-4374-a327-c0a5f8faec2c" } },
				],
			},
		})
	})

	it("adds updatedAt filter for incremental pulls", () => {
		expect(
			buildLinearIssueFilter({
				team: "AZE",
				project: "core-platform",
				updatedAfterIso: "2026-03-12T00:00:00.000Z",
			}),
		).toEqual({
			and: [
				{
					team: {
						or: [{ key: { eqIgnoreCase: "AZE" } }, { name: { eqIgnoreCase: "AZE" } }],
					},
				},
				{
					project: {
						or: [
							{ slugId: { eqIgnoreCase: "core-platform" } },
							{ name: { eqIgnoreCase: "core-platform" } },
						],
					},
				},
				{
					updatedAt: {
						gt: "2026-03-12T00:00:00.000Z",
					},
				},
			],
		})
	})
})

describe("buildLinearIssuesPageQuery", () => {
	it("omits optional fields when cursor and filter are undefined", () => {
		expect(
			buildLinearIssuesPageQuery({
				afterCursor: undefined,
				filter: undefined,
			}),
		).toEqual({ first: 250 })
	})

	it("includes cursor and filter only when provided", () => {
		const filter = {
			team: {
				or: [{ key: { eqIgnoreCase: "AZE" } }],
			},
		}

		expect(
			buildLinearIssuesPageQuery({
				afterCursor: "cursor-1",
				filter,
			}),
		).toEqual({
			first: 250,
			after: "cursor-1",
			filter,
		})
	})
})
