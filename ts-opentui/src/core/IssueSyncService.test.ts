import { describe, expect, it } from "bun:test"
import {
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
