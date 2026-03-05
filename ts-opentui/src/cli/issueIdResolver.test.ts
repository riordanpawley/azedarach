import { describe, expect, it } from "bun:test"
import { inferLinearIssuePrefixFromIds, normalizeIssueIdInput } from "./issueIdResolver.js"

describe("normalizeIssueIdInput", () => {
	it("uppercases Linear identifier prefixes", () => {
		expect(normalizeIssueIdInput("aze-123")).toBe("AZE-123")
	})

	it("keeps non-Linear identifiers unchanged", () => {
		expect(normalizeIssueIdInput("az-2qy")).toBe("az-2qy")
	})

	it("trims surrounding whitespace", () => {
		expect(normalizeIssueIdInput("  AZE-321  ")).toBe("AZE-321")
		expect(normalizeIssueIdInput("  321  ")).toBe("321")
	})
})

describe("inferLinearIssuePrefixFromIds", () => {
	it("infers the dominant prefix from a mixed issue set", () => {
		expect(inferLinearIssuePrefixFromIds(["aze-1", "AZE-2", "AZE-3", "CHE-11"])).toBe("AZE")
	})

	it("returns undefined when there are no Linear identifiers", () => {
		expect(inferLinearIssuePrefixFromIds(["az-2qy", "task.12"])).toBeUndefined()
	})

	it("returns undefined when prefix frequencies tie", () => {
		expect(inferLinearIssuePrefixFromIds(["AZE-1", "CHE-1"])).toBeUndefined()
	})
})
