import { describe, expect, it } from "bun:test"
import {
	composeIssueBranchName,
	normalizeBranchSlugMaxLength,
	sanitizeIssueIdForBranchSegment,
	slugifyIssueTitleForBranch,
} from "./WorktreeManager.js"

describe("WorktreeManager branch naming helpers", () => {
	it("normalizes slug max length with defaults and minimum bound", () => {
		expect(normalizeBranchSlugMaxLength()).toBe(24)
		expect(normalizeBranchSlugMaxLength(Number.NaN)).toBe(24)
		expect(normalizeBranchSlugMaxLength(3)).toBe(24)
		expect(normalizeBranchSlugMaxLength(8.9)).toBe(8)
	})

	it("slugifies issue titles and falls back to task", () => {
		expect(slugifyIssueTitleForBranch("Fix login redirect", 24)).toBe("fix-login-redirect")
		expect(slugifyIssueTitleForBranch("Fix / login", 6)).toBe("fix-lo")
		expect(slugifyIssueTitleForBranch("!!!", 24)).toBe("task")
	})

	it("sanitizes issue id for branch segment", () => {
		expect(sanitizeIssueIdForBranchSegment("AZE-123")).toBe("aze-123")
		expect(sanitizeIssueIdForBranchSegment("  az.issue_7  ")).toBe("az.issue_7")
		expect(sanitizeIssueIdForBranchSegment("A/B C")).toBe("a-b-c")
		expect(sanitizeIssueIdForBranchSegment("///")).toBe("issue")
	})

	it("composes branch names with issue id as middle path segment", () => {
		expect(composeIssueBranchName("riordan", "AZE-123", "fix-login")).toBe(
			"riordan/aze-123/fix-login",
		)
	})
})
