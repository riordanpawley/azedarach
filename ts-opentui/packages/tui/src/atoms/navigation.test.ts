import { describe, expect, it } from "bun:test"
import { buildFallbackEpicIssue, extractEpicChildren } from "./navigation.js"

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
