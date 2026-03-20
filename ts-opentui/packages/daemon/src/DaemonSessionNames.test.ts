import { describe, expect, it } from "bun:test"
import {
	decodeIssueSessionName,
	getIssueSessionName,
	getProjectSessionPrefix,
	issueIdsEqualForLookup,
	normalizeIssueIdForLookup,
	parseIssueSessionName,
} from "./DaemonSessionNames.js"

describe("DaemonSessionNames", () => {
	it("derives the same project-prefixed issue session names as the legacy helpers", () => {
		expect(getProjectSessionPrefix("/Users/riordan/prog/azedarach-te")).toBe("az")
		expect(getProjectSessionPrefix("/Users/user/prog/te")).toBe("te")
		expect(getIssueSessionName("AZE-123", "/tmp/project")).toBe("pr-AZE-123")
		expect(getIssueSessionName("az.foo")).toBe("az_x2e_foo")
	})

	it("decodes and parses current and legacy session names", () => {
		expect(decodeIssueSessionName("az_x2e_foo")).toBe("az.foo")
		expect(parseIssueSessionName("claude-pr-AZE-123", "/tmp/project")).toEqual({
			type: "issue",
			issueId: "AZE-123",
		})
		expect(parseIssueSessionName("az_x2e_foo")).toEqual({ type: "issue", issueId: "az.foo" })
		expect(parseIssueSessionName("az_foo")).toEqual({ type: "issue", issueId: "az.foo" })
	})

	it("normalizes linear issue ids for lookup", () => {
		expect(normalizeIssueIdForLookup("aze-123")).toBe("AZE-123")
		expect(issueIdsEqualForLookup("aze-123", "AZE-123")).toBe(true)
		expect(issueIdsEqualForLookup("a", "b")).toBe(false)
	})
})
