import { describe, expect, it } from "bun:test"
import { inferLinearIssueType } from "./IssueTrackerClient.js"

describe("inferLinearIssueType", () => {
	it("maps native Linear Epic type to epic", () => {
		expect(inferLinearIssueType(undefined, false, "Epic")).toBe("epic")
	})

	it("maps native Linear Initiative type to epic", () => {
		expect(inferLinearIssueType(undefined, false, "Initiative")).toBe("epic")
	})

	it("normalizes labels with spaced type prefixes", () => {
		expect(inferLinearIssueType(["type: epic"], false, undefined)).toBe("epic")
	})

	it("uses children as epic fallback when type is missing", () => {
		expect(inferLinearIssueType(undefined, true, undefined)).toBe("epic")
	})

	it("defaults to task for unknown type metadata", () => {
		expect(inferLinearIssueType(["ops"], false, "unknown")).toBe("task")
	})

	it("prefers native Linear type over labels", () => {
		expect(inferLinearIssueType(["type:epic"], false, "Task")).toBe("task")
	})
})
