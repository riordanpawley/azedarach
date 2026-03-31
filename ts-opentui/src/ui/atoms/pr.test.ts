import { describe, expect, it } from "bun:test"
import { appendLinkedIssueAutoCloseFooter, buildIssuePRBody, buildIssuePRTitle } from "./pr.js"

describe("buildIssuePRTitle", () => {
	it("includes the issue type prefix and id", () => {
		expect(
			buildIssuePRTitle({
				id: "az-123",
				title: "Ship thin-client PR actions",
				issue_type: "task",
			}),
		).toBe("[task] Ship thin-client PR actions (az-123)")
	})
})

describe("buildIssuePRBody", () => {
	it("renders a structured PR body with base branch and issue details", () => {
		const body = buildIssuePRBody(
			{
				id: "az-123",
				title: "Ship thin-client PR actions",
				description: "Move PR atoms off PRWorkflow.",
				design: "Use daemon RPC issue updates and local git commands.",
			},
			{ baseBranch: "main" },
		)

		expect(body).toContain("Resolves az-123: Ship thin-client PR actions")
		expect(body).toContain("Base branch: `main`")
		expect(body).toContain("## Description")
		expect(body).toContain("## Design Notes")
		expect(body).toContain("Closes az-123")
		expect(body.endsWith("Closes az-123")).toBe(true)
	})

	it("keeps the auto-close footer idempotent", () => {
		const originalBody = [
			"## Summary",
			"",
			"Resolves az-123: Ship thin-client PR actions",
			"",
			"---",
			"🤖 Generated with [Azedarach](https://github.com/riordanpawley/azedarach)",
			"",
			"Closes az-123",
		].join("\n")

		expect(appendLinkedIssueAutoCloseFooter(originalBody, "az-123")).toBe(originalBody.trimEnd())
	})
})
