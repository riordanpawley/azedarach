import { describe, expect, it } from "bun:test"
import {
	buildFallbackEpicIssue,
	extractEpicChildren,
	resolveDrillDownScopeState,
} from "./navigation.js"

describe("navigation epic helpers", () => {
	it("filters epic children to parent-child dependents only", () => {
		expect(
			extractEpicChildren({
				id: "epic-1",
				title: "Epic",
				status: "open",
				priority: 2,
				issue_type: "epic",
				created_at: "2026-01-01T00:00:00.000Z",
				updated_at: "2026-01-01T00:00:00.000Z",
				implementations: [],
				dependents: [
					{ id: "child-1", dependency_type: "parent-child" },
					{ id: "blocked-1", dependency_type: "blocks" },
					{ id: "child-2", dependency_type: "parent-child" },
				],
			}),
		).toEqual([
			{ id: "child-1", dependency_type: "parent-child" },
			{ id: "child-2", dependency_type: "parent-child" },
		])
	})

	it("builds the fallback epic shape for missing daemon data", () => {
		expect(buildFallbackEpicIssue("epic-unknown")).toEqual({
			id: "epic-unknown",
			title: "Unknown Epic",
			status: "open",
			priority: 2,
			issue_type: "epic",
			created_at: "",
			updated_at: "",
			implementations: [],
		})
	})
})

describe("resolveDrillDownScopeState", () => {
	it("returns inactive when no drill-down epic is selected", () => {
		expect(
			resolveDrillDownScopeState({
				drillDownEpicId: null,
				drillDownEpicAvailable: "ready",
				childIds: new Set(["AZ-1"]),
				childIdsAvailable: "ready",
			}),
		).toEqual({ _tag: "inactive" })
	})

	it("returns loading when drill-down epic is active but child ids are still loading", () => {
		expect(
			resolveDrillDownScopeState({
				drillDownEpicId: "AZ-E1",
				drillDownEpicAvailable: "ready",
				childIds: undefined,
				childIdsAvailable: "loading",
			}),
		).toEqual({ _tag: "loading" })
	})

	it("returns error when drill-down epic is active but child ids fail", () => {
		expect(
			resolveDrillDownScopeState({
				drillDownEpicId: "AZ-E1",
				drillDownEpicAvailable: "ready",
				childIds: undefined,
				childIdsAvailable: "error",
			}),
		).toEqual({ _tag: "error" })
	})

	it("returns active with child ids when drill-down scope is available", () => {
		const childIds = new Set(["AZ-1", "AZ-2"])
		expect(
			resolveDrillDownScopeState({
				drillDownEpicId: "AZ-E1",
				drillDownEpicAvailable: "ready",
				childIds,
				childIdsAvailable: "ready",
			}),
		).toEqual({ _tag: "active", childIds })
	})
})
