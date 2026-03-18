import { describe, expect, it } from "bun:test"
import { buildIssuePRBody, buildIssuePRTitle } from "./pr.js"

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
		expect(
			buildIssuePRBody(
				{
					id: "az-123",
					title: "Ship thin-client PR actions",
					description: "Move PR atoms off PRWorkflow.",
					design: "Use daemon RPC issue updates and local git commands.",
				},
				{ baseBranch: "main" },
			),
		).toContain("Resolves az-123: Ship thin-client PR actions")
		expect(
			buildIssuePRBody(
				{
					id: "az-123",
					title: "Ship thin-client PR actions",
					description: "Move PR atoms off PRWorkflow.",
					design: "Use daemon RPC issue updates and local git commands.",
				},
				{ baseBranch: "main" },
			),
		).toContain("Base branch: `main`")
		expect(
			buildIssuePRBody(
				{
					id: "az-123",
					title: "Ship thin-client PR actions",
					description: "Move PR atoms off PRWorkflow.",
					design: "Use daemon RPC issue updates and local git commands.",
				},
				{ baseBranch: "main" },
			),
		).toContain("## Description")
		expect(
			buildIssuePRBody(
				{
					id: "az-123",
					title: "Ship thin-client PR actions",
					description: "Move PR atoms off PRWorkflow.",
					design: "Use daemon RPC issue updates and local git commands.",
				},
				{ baseBranch: "main" },
			),
		).toContain("## Design Notes")
	})
})
