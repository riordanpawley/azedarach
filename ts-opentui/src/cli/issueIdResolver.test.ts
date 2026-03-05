import { describe, expect, it } from "bun:test"
import { inferLinearIssuePrefixFromIds } from "./issueIdResolver.js"

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
