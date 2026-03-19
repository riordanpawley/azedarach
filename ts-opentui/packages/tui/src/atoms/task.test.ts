import { describe, expect, it } from "bun:test"
import { resolveIssueType, toEpicChildDependencyRefs } from "./task.js"

describe("resolveIssueType", () => {
	it("keeps valid issue types", () => {
		expect(resolveIssueType("epic")).toBe("epic")
	})

	it("falls back to task for unexpected values", () => {
		expect(resolveIssueType("not-a-real-type")).toBe("task")
	})
})

describe("toEpicChildDependencyRefs", () => {
	it("filters to parent-child dependents and preserves child metadata", () => {
		const children = toEpicChildDependencyRefs([
			{
				id: "az-1",
				title: "Child one",
				status: "open",
				dependency_type: "parent-child",
				issue_type: "task",
			},
			{
				id: "az-2",
				title: "Blocked issue",
				status: "blocked",
				dependency_type: "blocks",
				issue_type: "bug",
			},
		])

		expect(children).toEqual([
			{
				id: "az-1",
				title: "Child one",
				status: "open",
				dependency_type: "parent-child",
				issue_type: "task",
			},
		])
	})
})
