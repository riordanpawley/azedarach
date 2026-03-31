import { describe, expect, it } from "bun:test"
import {
	isPendingStatusCheckRollupState,
	resolvePRStateCacheTtlMs,
} from "./PRStateService.js"

describe("PRStateService polling windows", () => {
	it("uses a shorter cache window while CI checks are pending", () => {
		expect(resolvePRStateCacheTtlMs({ state: "open", statusCheckRollupState: "PENDING" })).toBe(
			10000,
		)
		expect(resolvePRStateCacheTtlMs({ state: "draft", statusCheckRollupState: "in_progress" })).toBe(
			10000,
		)
	})

	it("backs off after checks settle or the PR is terminal", () => {
		expect(resolvePRStateCacheTtlMs({ state: "open", statusCheckRollupState: "success" })).toBe(
			30000,
		)
		expect(resolvePRStateCacheTtlMs({ state: "merged", statusCheckRollupState: undefined })).toBe(
			120000,
		)
		expect(resolvePRStateCacheTtlMs({ state: "closed", statusCheckRollupState: "failure" })).toBe(
			120000,
		)
	})

	it("recognizes pending status check rollup states case-insensitively", () => {
		expect(isPendingStatusCheckRollupState("Queued")).toBe(true)
		expect(isPendingStatusCheckRollupState("success")).toBe(false)
		expect(isPendingStatusCheckRollupState(undefined)).toBe(false)
	})
})
