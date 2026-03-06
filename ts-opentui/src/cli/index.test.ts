import { describe, expect, it } from "bun:test"
import {
	deriveWaitingAttentionPlan,
	formatIssueDetailSections,
	formatIssueSummaryLine,
	normalizeIssueJsonFlagOrder,
	resolveCliExecutionMode,
} from "./index.js"

describe("normalizeIssueJsonFlagOrder", () => {
	it("moves --json to the issue subcommand options position", () => {
		const argv = ["bun", "az", "issue", "create", "My issue", "--json"]
		const normalized = normalizeIssueJsonFlagOrder(argv)

		expect(normalized).toEqual(["bun", "az", "issue", "create", "--json", "My issue"])
	})

	it("keeps non-issue commands unchanged", () => {
		const argv = ["bun", "az", "status", "--json"]
		expect(normalizeIssueJsonFlagOrder(argv)).toEqual(argv)
	})

	it("moves issue update options ahead of issue-id when issue-id is first", () => {
		const argv = ["bun", "az", "issue", "update", "az-123", "--description", "why not"]
		const normalized = normalizeIssueJsonFlagOrder(argv)

		expect(normalized).toEqual([
			"bun",
			"az",
			"issue",
			"update",
			"--description",
			"why not",
			"az-123",
		])
	})

	it("keeps issue update argument order when options are already first", () => {
		const argv = ["bun", "az", "issue", "update", "--description", "why not", "az-123"]
		expect(normalizeIssueJsonFlagOrder(argv)).toEqual(argv)
	})

	it("moves issue create options ahead of title when title is first", () => {
		const argv = [
			"bun",
			"az",
			"issue",
			"create",
			"Child task",
			"--parent",
			"AZE-134",
		]
		const normalized = normalizeIssueJsonFlagOrder(argv)

		expect(normalized).toEqual([
			"bun",
			"az",
			"issue",
			"create",
			"--parent",
			"AZE-134",
			"Child task",
		])
	})
})

describe("resolveCliExecutionMode", () => {
	it("uses tui mode for bare az launch", () => {
		expect(resolveCliExecutionMode(["bun", "az"])).toBe("tui")
	})

	it("uses command mode for non-dev subcommands", () => {
		expect(resolveCliExecutionMode(["bun", "az", "issue", "create", "Title"])).toBe("command")
		expect(resolveCliExecutionMode(["bun", "az", "--config", "./.azedarach.json", "project", "list"])).toBe("command")
		expect(resolveCliExecutionMode(["bun", "az", "prime"])).toBe("command")
		expect(resolveCliExecutionMode(["bun", "az", "opencode", "plugin", "install"])).toBe("command")
	})

	it("uses dev-command mode for az dev", () => {
		expect(resolveCliExecutionMode(["bun", "az", "dev", "list"])).toBe("dev-command")
	})

	it("uses command mode for top-level help/version", () => {
		expect(resolveCliExecutionMode(["bun", "az", "--help"])).toBe("command")
		expect(resolveCliExecutionMode(["bun", "az", "--version"])).toBe("command")
	})
})

describe("deriveWaitingAttentionPlan", () => {
	it("rings bell once when entering waiting", () => {
		expect(deriveWaitingAttentionPlan("waiting", null)).toEqual({
			ringBell: true,
			nextFlag: "1",
		})
		expect(deriveWaitingAttentionPlan("waiting", "0")).toEqual({
			ringBell: true,
			nextFlag: "1",
		})
		expect(deriveWaitingAttentionPlan("waiting", "1")).toEqual({
			ringBell: false,
			nextFlag: "1",
		})
	})

	it("resets waiting flag when leaving waiting", () => {
		expect(deriveWaitingAttentionPlan("busy", "1")).toEqual({
			ringBell: false,
			nextFlag: "0",
		})
		expect(deriveWaitingAttentionPlan("idle", "1")).toEqual({
			ringBell: false,
			nextFlag: "0",
		})
	})
})

describe("formatIssueSummaryLine", () => {
	it("formats a single-line summary and compacts title whitespace", () => {
		const line = formatIssueSummaryLine({
			id: "az-123",
			title: "Fix\n  sqlite refresh  ",
			status: "in_progress",
			priority: 1,
			issue_type: "task",
			created_at: "2026-03-05T10:00:00.000Z",
			updated_at: "2026-03-05T11:00:00.000Z",
		})

		expect(line.includes("\n")).toBe(false)
		expect(line).toContain("az-123: Fix sqlite refresh")
		expect(line).toContain("status=in_progress")
		expect(line).toContain("priority=1")
		expect(line).toContain("type=task")
	})
})

describe("formatIssueDetailSections", () => {
	it("returns populated description/design/acceptance/notes sections", () => {
		const sections = formatIssueDetailSections({
			id: "az-123",
			title: "Title",
			status: "open",
			priority: 2,
			issue_type: "task",
			created_at: "2026-03-05T10:00:00.000Z",
			updated_at: "2026-03-05T11:00:00.000Z",
			description: "Investigate parser behavior",
			design: "Move options before positional args",
			acceptance: "description can be updated",
			notes: "manual repro completed",
			dependencies: [
				{ id: "AZE-11", dependency_type: "blocks" },
				{ id: "AZE-12", dependency_type: "related" },
				{ id: "AZE-13", dependency_type: "discovered-from" },
			],
		})

		expect(sections).toEqual([
			"Description:\nInvestigate parser behavior",
			"Design:\nMove options before positional args",
			"Acceptance:\ndescription can be updated",
			"Notes:\nmanual repro completed",
			"Dependency Counts: blockedBy: 1, related: 1, discoveredFrom: 1",
			"Dependencies:\nAZE-11, AZE-12, AZE-13",
		])
	})

	it("omits empty detail sections", () => {
		const sections = formatIssueDetailSections({
			id: "az-123",
			title: "Title",
			status: "open",
			priority: 2,
			issue_type: "task",
			created_at: "2026-03-05T10:00:00.000Z",
			updated_at: "2026-03-05T11:00:00.000Z",
			description: "   ",
		})

		expect(sections).toEqual([])
	})

	it("includes dependency and dependent ids in non-json detail sections", () => {
		const sections = formatIssueDetailSections({
			id: "az-123",
			title: "Title",
			status: "open",
			priority: 2,
			issue_type: "task",
			created_at: "2026-03-05T10:00:00.000Z",
			updated_at: "2026-03-05T11:00:00.000Z",
			dependencies: [
				{ id: "AZE-1", dependency_type: "blocks" },
				{ id: "AZE-1", dependency_type: "related" },
				{ id: "AZE-2", dependency_type: "related" },
			],
			dependents: [{ id: "AZE-9", dependency_type: "parent-child" }],
		})

		expect(sections).toEqual([
			"Dependency Counts: blockedBy: 1, children: 1, related: 2",
			"Dependencies:\nAZE-1, AZE-2",
			"Dependents:\nAZE-9",
		])
	})

	it("falls back to dependency counts when ids are unavailable", () => {
		const sections = formatIssueDetailSections({
			id: "az-123",
			title: "Title",
			status: "open",
			priority: 2,
			issue_type: "task",
			created_at: "2026-03-05T10:00:00.000Z",
			updated_at: "2026-03-05T11:00:00.000Z",
			dependency_count: 2,
			dependent_count: 1,
		})

		expect(sections).toEqual(["Dependencies: 2", "Dependents: 1"])
	})

	it("formats directional counts for blocks and parent-child relationships", () => {
		const sections = formatIssueDetailSections({
			id: "az-123",
			title: "Title",
			status: "open",
			priority: 2,
			issue_type: "task",
			created_at: "2026-03-05T10:00:00.000Z",
			updated_at: "2026-03-05T11:00:00.000Z",
			dependencies: [
				{ id: "AZE-11", dependency_type: "blocks" },
				{ id: "AZE-12", dependency_type: "blocks" },
				{ id: "AZE-10", dependency_type: "parent-child" },
			],
			dependents: [
				{ id: "AZE-90", dependency_type: "blocks" },
				{ id: "AZE-91", dependency_type: "parent-child" },
				{ id: "AZE-92", dependency_type: "parent-child" },
			],
		})

		expect(sections).toEqual([
			"Dependency Counts: blocking: 1, blockedBy: 2, children: 2, parent: 1",
			"Dependencies:\nAZE-11, AZE-12, AZE-10",
			"Dependents:\nAZE-90, AZE-91, AZE-92",
		])
	})
})
