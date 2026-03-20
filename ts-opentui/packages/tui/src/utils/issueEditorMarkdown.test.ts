import { describe, expect, it } from "bun:test"
import { Effect } from "effect"
import type { Issue } from "../contracts.js"
import {
	createBlankIssueTemplate,
	ISSUE_EDITOR_ANCHORS,
	parseMarkdownToIssue,
	parseMarkdownToNewIssue,
	serializeIssueToMarkdown,
} from "./issueEditorMarkdown.js"

const issue: Issue = {
	id: "az-42",
	title: "Original title",
	status: "open",
	priority: 2,
	issue_type: "task",
	created_at: "2026-03-20T00:00:00.000Z",
	updated_at: "2026-03-20T00:00:00.000Z",
	assignee: "riordan",
	labels: ["one", "two"],
	description: "Original description",
	design: "Original design",
	notes: "Original notes",
	acceptance: "Original acceptance",
	estimate: 3,
	implementations: ["ts-opentui"],
}

describe("serializeIssueToMarkdown", () => {
	it("renders the issue metadata and sections", () => {
		const markdown = serializeIssueToMarkdown(issue)

		expect(markdown).toContain("# az-42: Original title")
		expect(markdown).toContain("Priority: P2")
		expect(markdown).toContain("Impl:     ts-opentui")
		expect(markdown).toContain("## Description")
		expect(markdown).toContain("Original acceptance")
	})
})

describe("parseMarkdownToIssue", () => {
	it("extracts only changed fields from edited markdown", async () => {
		const markdown = `# az-42: Updated title
───────────────────────────────────────────────────

Type:     task        (read-only - changing requires delete+create)
Priority: P1
Status:   blocked
Assignee: 
Labels:   alpha, beta
Impl:     tui, daemon
Estimate: 5

───────────────────────────────────────────────────
## Description

Updated description

───────────────────────────────────────────────────
## Design

Updated design

───────────────────────────────────────────────────
## Notes

Updated notes

───────────────────────────────────────────────────
## Acceptance Criteria

Updated acceptance
`

		const updates = await Effect.runPromise(parseMarkdownToIssue(markdown, issue))
		expect(updates).toEqual({
			title: "Updated title",
			priority: 1,
			status: "blocked",
			assignee: undefined,
			labels: ["alpha", "beta"],
			implementations: ["tui", "daemon"],
			estimate: 5,
			description: "Updated description",
			design: "Updated design",
			notes: "Updated notes",
			acceptance: "Updated acceptance",
		})
	})
})

describe("createBlankIssueTemplate", () => {
	it("includes the default implementation and anchors", () => {
		const markdown = createBlankIssueTemplate("ts-opentui", ["ts-opentui", "go-bubbletea"])

		expect(markdown).toContain("Impl:     ts-opentui        (ts-opentui | go-bubbletea)")
		expect(markdown).toContain(ISSUE_EDITOR_ANCHORS.DESCRIPTION)
	})
})

describe("parseMarkdownToNewIssue", () => {
	it("parses a new issue template into create fields", async () => {
		const markdown = `# New title
───────────────────────────────────────────────────

Type:     feature        (task | bug | feature | epic | chore)
Priority: P3          (P0 = highest, P4 = lowest)
Status:   in_progress        (open | in_progress | blocked | closed)
Assignee: riordan
Labels:   one, two
Impl:     tui, daemon        (tui | daemon)
Estimate: 8

───────────────────────────────────────────────────
## Description

Build the thing

───────────────────────────────────────────────────
## Design

Keep it package-local

───────────────────────────────────────────────────
## Notes

Tracker note

───────────────────────────────────────────────────
## Acceptance Criteria

Ship it
`

		const fields = await Effect.runPromise(parseMarkdownToNewIssue(markdown))
		expect(fields).toEqual({
			title: "New title",
			type: "feature",
			priority: 3,
			status: "in_progress",
			assignee: "riordan",
			labels: ["one", "two"],
			implementations: ["tui", "daemon"],
			estimate: 8,
			description: "Build the thing",
			design: "Keep it package-local",
			notes: "Tracker note",
			acceptance: "Ship it",
		})
	})
})
