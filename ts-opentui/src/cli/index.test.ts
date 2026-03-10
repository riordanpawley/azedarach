import { describe, expect, it } from "bun:test"
import type { Issue as TrackedIssue } from "../core/IssueTrackerClient.js"
import {
	buildPrimeOutput,
	deriveWaitingAttentionPlan,
	findLikelyParentChildTrackingMisses,
	formatIssueDetailSections,
	formatIssueSummaryLine,
	formatParentChildCheckOutput,
	normalizeCliAliases,
	normalizeIssueJsonFlagOrder,
	resolveCliExecutionMode,
} from "./index.js"

describe("buildPrimeOutput", () => {
	it("includes issue-context guardrails and refresh instructions for active issues", () => {
		const output = buildPrimeOutput("gq", "gq: Improve az prime")

		expect(output).toContain("Issue-context guardrails:")
		expect(output).toContain("AZEDARACH_ISSUE_ID` is set to `gq`")
		expect(output).toContain("refresh stale context with `az issue get gq`")
		expect(output).toContain(
			"Missing fields (for example description/design/acceptance/notes) are valid.",
		)
		expect(output).toContain("Do not go on history/log hunting tangents")
		expect(output).toContain(
			"In this repo, when guidance says `spec`, it means `az spec` requirement/link records",
		)
		expect(output).toContain(
			"Before implementing behavior changes, inspect relevant `az spec` requirements/links",
		)
		expect(output).toContain("Spec boundary for `az spec` usage")
		expect(output).toContain("Use `az spec` only for product behavior changes")
		expect(output).toContain("Do NOT use `az spec` for infra-only work")
		expect(output).toContain('default to no spec link and note: "Spec impact: none (infra-only)."')
		expect(output).toContain(
			"After implementing behavior changes, run an `az spec` compliance pass",
		)
		expect(output).toContain("Spec sync discipline (ts-opentui behavior changes)")
		expect(output).toContain("For `az spec` commands, keep canonical Effect CLI ordering")
		expect(output).toContain('record "Spec impact: none" with concrete file-based rationale')
		expect(output).toContain(
			"Review flow policy: reviews target closed tasks, not in-progress tasks.",
		)
		expect(output).toContain("If review finds remaining work, move the issue back to in-progress")
		expect(output).toContain("Active issue context (AZEDARACH_ISSUE_ID=gq):")
	})

	it("guides users to fetch an issue when no issue id is configured", () => {
		const output = buildPrimeOutput(undefined, "")

		expect(output).toContain("No active issue is preselected")
		expect(output).toContain("run `az issue get <issue-id>`")
		expect(output).not.toContain("Active issue context (AZEDARACH_ISSUE_ID=")
	})

	it("falls back to explicit refresh command when issue details fail to load", () => {
		const output = buildPrimeOutput("gq", "")

		expect(output).toContain("Active issue from AZEDARACH_ISSUE_ID=gq.")
		expect(output).toContain("Could not load issue details automatically; run `az issue get gq`.")
	})
})

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
		const argv = ["bun", "az", "issue", "create", "Child task", "--parent", "AZE-134"]
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

	it("moves issue child options ahead of title when title is first", () => {
		const argv = ["bun", "az", "issue", "child", "Follow-up task", "--parent", "AZE-200"]
		const normalized = normalizeIssueJsonFlagOrder(argv)

		expect(normalized).toEqual([
			"bun",
			"az",
			"issue",
			"child",
			"--parent",
			"AZE-200",
			"Follow-up task",
		])
	})

	it("keeps issue list options unchanged", () => {
		const argv = ["bun", "az", "issue", "list", "--limit", "5", "--status", "open"]
		expect(normalizeIssueJsonFlagOrder(argv)).toEqual(argv)
	})

	it("keeps issue list parent filter order unchanged", () => {
		const argv = ["bun", "az", "issue", "list", "--parent", "AZE-200", "--limit", "5"]
		expect(normalizeIssueJsonFlagOrder(argv)).toEqual(argv)
	})

	it("moves issue check options ahead of issue-id when issue-id is first", () => {
		const argv = ["bun", "az", "issue", "check", "AZE-200", "--limit", "50"]
		const normalized = normalizeIssueJsonFlagOrder(argv)

		expect(normalized).toEqual(["bun", "az", "issue", "check", "--limit", "50", "AZE-200"])
	})

	it("moves issue dep add options ahead of positional ids when ids are first", () => {
		const argv = [
			"bun",
			"az",
			"issue",
			"dep",
			"add",
			"AZE-200",
			"AZE-123",
			"--type",
			"discovered-from",
		]

		expect(normalizeIssueJsonFlagOrder(argv)).toEqual([
			"bun",
			"az",
			"issue",
			"dep",
			"add",
			"--type",
			"discovered-from",
			"AZE-200",
			"AZE-123",
		])
	})
})

describe("resolveCliExecutionMode", () => {
	it("uses tui mode for bare az launch", () => {
		expect(resolveCliExecutionMode(["bun", "az"])).toBe("tui")
	})

	it("uses command mode for non-dev subcommands", () => {
		expect(resolveCliExecutionMode(["bun", "az", "issue", "create", "Title"])).toBe("command")
		expect(
			resolveCliExecutionMode([
				"bun",
				"az",
				"--config",
				"./.azedarach/config.json",
				"project",
				"list",
			]),
		).toBe("command")
		expect(resolveCliExecutionMode(["bun", "az", "prime"])).toBe("command")
		expect(resolveCliExecutionMode(["bun", "az", "spec", "req", "list"])).toBe("command")
		expect(resolveCliExecutionMode(["bun", "az", "opencode", "plugin", "install"])).toBe("command")
	})

	it("treats `az i` as `az issue` for mode resolution", () => {
		expect(resolveCliExecutionMode(["bun", "az", "i", "create", "Title"])).toBe("command")
	})

	it("treats `az ls` and `az st` as command aliases", () => {
		expect(resolveCliExecutionMode(["bun", "az", "ls", "show"])).toBe("command")
		expect(resolveCliExecutionMode(["bun", "az", "st", "a1"])).toBe("command")
	})

	it("uses dev-command mode for az dev", () => {
		expect(resolveCliExecutionMode(["bun", "az", "dev", "list"])).toBe("dev-command")
		expect(resolveCliExecutionMode(["bun", "az", "d", "list"])).toBe("dev-command")
	})

	it("uses command mode for top-level help/version", () => {
		expect(resolveCliExecutionMode(["bun", "az", "--help"])).toBe("command")
		expect(resolveCliExecutionMode(["bun", "az", "--version"])).toBe("command")
	})

	it("handles nested shorthand resolution while keeping command mode", () => {
		expect(resolveCliExecutionMode(["bun", "az", "a", "list"])).toBe("command")
		expect(resolveCliExecutionMode(["bun", "az", "i", "c", "Fix typo"])).toBe("command")
		expect(resolveCliExecutionMode(["bun", "az", "sp", "r", "ls"])).toBe("command")
		expect(resolveCliExecutionMode(["bun", "az", "i", "rm", "AZE-1"])).toBe("command")
		expect(resolveCliExecutionMode(["bun", "az", "p", "a", "myproject"])).toBe("command")
		expect(resolveCliExecutionMode(["bun", "az", "p", "sw", "myproject"])).toBe("command")
		expect(resolveCliExecutionMode(["bun", "az", "o", "i", "project"])).toBe("command")
		expect(resolveCliExecutionMode(["bun", "az", "o", "pl", "my-plugin"])).toBe("command")
		expect(resolveCliExecutionMode(["bun", "az", "h", "i", "hook-name"])).toBe("command")
		expect(resolveCliExecutionMode(["bun", "az", "d", "stp", "AZE-1"])).toBe("dev-command")
		expect(resolveCliExecutionMode(["bun", "az", "d", "ls"])).toBe("dev-command")
		expect(resolveCliExecutionMode(["bun", "az", "d", "s", "AZE-1"])).toBe("dev-command")
	})
})

describe("normalizeCliAliases", () => {
	it("normalizes top-level `i` to `issue`", () => {
		const argv = ["bun", "az", "i", "create", "Title", "--type", "task"]
		expect(normalizeCliAliases(argv)).toEqual([
			"bun",
			"az",
			"issue",
			"create",
			"Title",
			"--type",
			"task",
		])
	})

	it("normalizes common top-level shorthands to canonical commands", () => {
		expect(normalizeCliAliases(["bun", "az", "a", "list"])).toEqual(["bun", "az", "add", "list"])
		expect(normalizeCliAliases(["bun", "az", "ls", "list"])).toEqual(["bun", "az", "list"])
		expect(normalizeCliAliases(["bun", "az", "st", "a1"])).toEqual(["bun", "az", "start", "a1"])
		expect(normalizeCliAliases(["bun", "az", "p", "list"])).toEqual([
			"bun",
			"az",
			"project",
			"list",
		])
		expect(normalizeCliAliases(["bun", "az", "pr", "list"])).toEqual(["bun", "az", "prime", "list"])
		expect(normalizeCliAliases(["bun", "az", "at", "a1"])).toEqual(["bun", "az", "attach", "a1"])
		expect(normalizeCliAliases(["bun", "az", "pa", "a1"])).toEqual(["bun", "az", "pause", "a1"])
		expect(normalizeCliAliases(["bun", "az", "k", "a1"])).toEqual(["bun", "az", "kill", "a1"])
		expect(normalizeCliAliases(["bun", "az", "se", "status"])).toEqual([
			"bun",
			"az",
			"status",
			"status",
		])
	})

	it("does not rewrite short option aliases for issue operations", () => {
		const argv = [
			"bun",
			"az",
			"issue",
			"create",
			"-d",
			"Add missing alias support",
			"Create alias coverage",
		]
		expect(normalizeCliAliases(argv)).toEqual(argv)
	})

	it("normalizes issue subcommand shorthands", () => {
		expect(normalizeCliAliases(["bun", "az", "i", "c", "Fix typo"])).toEqual([
			"bun",
			"az",
			"issue",
			"create",
			"Fix typo",
		])
		expect(normalizeCliAliases(["bun", "az", "i", "ch", "Follow-up"])).toEqual([
			"bun",
			"az",
			"issue",
			"child",
			"Follow-up",
		])
		expect(normalizeCliAliases(["bun", "az", "i", "ck", "AZE-10"])).toEqual([
			"bun",
			"az",
			"issue",
			"check",
			"AZE-10",
		])
		expect(normalizeCliAliases(["bun", "az", "i", "dr", "AZE-10"])).toEqual([
			"bun",
			"az",
			"issue",
			"doctor",
			"AZE-10",
		])
		expect(normalizeCliAliases(["bun", "az", "issue", "d", "add", "AZE-1", "AZE-2"])).toEqual([
			"bun",
			"az",
			"issue",
			"dep",
			"add",
			"AZE-1",
			"AZE-2",
		])
		expect(normalizeCliAliases(["bun", "az", "i", "x", "AZE-1"])).toEqual([
			"bun",
			"az",
			"issue",
			"close",
			"AZE-1",
		])
		expect(normalizeCliAliases(["bun", "az", "i", "rm", "AZE-1"])).toEqual([
			"bun",
			"az",
			"issue",
			"delete",
			"AZE-1",
		])
		expect(normalizeCliAliases(["bun", "az", "i", "del", "AZE-1"])).toEqual([
			"bun",
			"az",
			"issue",
			"delete",
			"AZE-1",
		])
	})

	it("normalizes spec nested command shorthands", () => {
		expect(normalizeCliAliases(["bun", "az", "sp", "r", "ls"])).toEqual([
			"bun",
			"az",
			"spec",
			"req",
			"list",
		])
		expect(normalizeCliAliases(["bun", "az", "spec", "l", "a", "AZE-1"])).toEqual([
			"bun",
			"az",
			"spec",
			"link",
			"add",
			"AZE-1",
		])
		expect(normalizeCliAliases(["bun", "az", "spec", "p", "c"])).toEqual([
			"bun",
			"az",
			"spec",
			"sync",
			"c",
		])
		expect(normalizeCliAliases(["bun", "az", "spec", "publish", "c"])).toEqual([
			"bun",
			"az",
			"spec",
			"publish",
			"config",
		])
	})

	it("normalizes project, opencode, and hooks nested shorthands", () => {
		expect(normalizeCliAliases(["bun", "az", "p", "a", "myproject"])).toEqual([
			"bun",
			"az",
			"project",
			"add",
			"myproject",
		])
		expect(normalizeCliAliases(["bun", "az", "p", "l"])).toEqual(["bun", "az", "project", "list"])
		expect(normalizeCliAliases(["bun", "az", "p", "r", "myproject"])).toEqual([
			"bun",
			"az",
			"project",
			"remove",
			"myproject",
		])
		expect(normalizeCliAliases(["bun", "az", "p", "rm", "myproject"])).toEqual([
			"bun",
			"az",
			"project",
			"remove",
			"myproject",
		])
		expect(normalizeCliAliases(["bun", "az", "p", "s", "myproject"])).toEqual([
			"bun",
			"az",
			"project",
			"switch",
			"myproject",
		])
		expect(normalizeCliAliases(["bun", "az", "p", "sw", "myproject"])).toEqual([
			"bun",
			"az",
			"project",
			"switch",
			"myproject",
		])
		expect(normalizeCliAliases(["bun", "az", "o", "i", "project"])).toEqual([
			"bun",
			"az",
			"opencode",
			"init",
			"project",
		])
		expect(normalizeCliAliases(["bun", "az", "o", "p", "my-plugin"])).toEqual([
			"bun",
			"az",
			"opencode",
			"plugin",
			"my-plugin",
		])
		expect(normalizeCliAliases(["bun", "az", "o", "pl", "my-plugin"])).toEqual([
			"bun",
			"az",
			"opencode",
			"plugin",
			"my-plugin",
		])
		expect(normalizeCliAliases(["bun", "az", "h", "i", "hook-name"])).toEqual([
			"bun",
			"az",
			"hooks",
			"install",
			"hook-name",
		])
		expect(normalizeCliAliases(["bun", "az", "h", "ins", "hook-name"])).toEqual([
			"bun",
			"az",
			"hooks",
			"install",
			"hook-name",
		])
	})

	it("normalizes dev command subcommand shorthands", () => {
		expect(normalizeCliAliases(["bun", "az", "d", "s", "AZE-1"])).toEqual([
			"bun",
			"az",
			"dev",
			"start",
			"AZE-1",
		])
		expect(normalizeCliAliases(["bun", "az", "dev", "r", "AZE-1"])).toEqual([
			"bun",
			"az",
			"dev",
			"restart",
			"AZE-1",
		])
		expect(normalizeCliAliases(["bun", "az", "d", "stp", "AZE-1"])).toEqual([
			"bun",
			"az",
			"dev",
			"stop",
			"AZE-1",
		])
		expect(normalizeCliAliases(["bun", "az", "d", "ls"])).toEqual(["bun", "az", "dev", "list"])
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

	it("includes linked spec requirements when provided", () => {
		const sections = formatIssueDetailSections(
			{
				id: "az-123",
				title: "Title",
				status: "open",
				priority: 2,
				issue_type: "task",
				created_at: "2026-03-05T10:00:00.000Z",
				updated_at: "2026-03-05T11:00:00.000Z",
			},
			{
				linkedSpecRequirements: [
					{
						id: "AZ-FR-4201",
						local_id: "fr4201",
						external_code: "AZ-FR-4201",
						title: "Persist requirements and links",
						kind: "functional",
						link_type: "implements",
					},
					{
						id: "AZ-AT-2901",
						local_id: "at2901",
						external_code: "AZ-AT-2901",
						title: "Acceptance path is covered",
						kind: "acceptance",
						link_type: "tests",
					},
				],
			},
		)

		expect(sections).toEqual([
			"Linked Spec Requirements:\nfr4201 (AZ-FR-4201) [functional] (implements) Persist requirements and links\nat2901 (AZ-AT-2901) [acceptance] (tests) Acceptance path is covered",
		])
	})
})

const makeIssue = (id: string, overrides: Partial<TrackedIssue> = {}): TrackedIssue => ({
	id,
	title: `Issue ${id}`,
	status: "open",
	priority: 3,
	issue_type: "task",
	created_at: "2026-03-08T00:00:00.000Z",
	updated_at: "2026-03-08T00:00:00.000Z",
	...overrides,
})

describe("findLikelyParentChildTrackingMisses", () => {
	it("flags non-parent-child dependencies to the target parent", () => {
		const misses = findLikelyParentChildTrackingMisses("GX-1", [
			makeIssue("GX-2", {
				dependencies: [{ id: "GX-1", dependency_type: "blocks" }],
			}),
			makeIssue("GX-3", {
				dependencies: [{ id: "GX-1", dependency_type: "parent-child" }],
			}),
		])

		expect(misses).toHaveLength(1)
		expect(misses[0]?.issue.id).toBe("GX-2")
		expect(misses[0]?.reason).toContain("typed 'blocks' instead of 'parent-child'")
		expect(misses[0]?.remediateCommand).toBe("az issue update GX-2 --parent GX-1")
	})

	it("flags issue text references to parent when no parent-child link exists", () => {
		const misses = findLikelyParentChildTrackingMisses("GX-1", [
			makeIssue("GX-4", {
				title: "Follow-up for GX-1 parity fix",
			}),
		])

		expect(misses).toHaveLength(1)
		expect(misses[0]?.issue.id).toBe("GX-4")
		expect(misses[0]?.reason).toContain("references GX-1")
	})

	it("ignores closed candidates and unrelated issues", () => {
		const misses = findLikelyParentChildTrackingMisses("GX-1", [
			makeIssue("GX-5", {
				status: "closed",
				dependencies: [{ id: "GX-1", dependency_type: "blocks" }],
			}),
			makeIssue("GX-6", {
				title: "No mention",
			}),
		])

		expect(misses).toHaveLength(0)
	})
})

describe("formatParentChildCheckOutput", () => {
	it("formats no-miss output", () => {
		expect(formatParentChildCheckOutput("GX-1", [])).toBe(
			"No likely parent-child tracking misses found for GX-1.",
		)
	})
})
