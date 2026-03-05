import { describe, expect, it } from "bun:test"
import {
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
})

describe("resolveCliExecutionMode", () => {
	it("uses tui mode for bare az launch", () => {
		expect(resolveCliExecutionMode(["bun", "az"])).toBe("tui")
	})

	it("uses command mode for non-dev subcommands", () => {
		expect(resolveCliExecutionMode(["bun", "az", "issue", "create", "Title"])).toBe("command")
		expect(resolveCliExecutionMode(["bun", "az", "--config", "./.azedarach.json", "project", "list"])).toBe("command")
	})

	it("uses dev-command mode for az dev", () => {
		expect(resolveCliExecutionMode(["bun", "az", "dev", "list"])).toBe("dev-command")
	})

	it("uses command mode for top-level help/version", () => {
		expect(resolveCliExecutionMode(["bun", "az", "--help"])).toBe("command")
		expect(resolveCliExecutionMode(["bun", "az", "--version"])).toBe("command")
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
