import { describe, expect, it } from "bun:test"
import { BunContext } from "@effect/platform-bun"
import { Effect } from "effect"
import { AppConfig } from "../config/AppConfig.js"
import type { Issue as TrackedIssue } from "../core/IssueTrackerClient.js"
import {
	buildCommandCliLayerForArgv,
	buildPrimeOutput,
	cliRunner,
	decodeIssueBulkCreatePayload,
	decodeIssueBulkUpdatePayload,
	deriveWaitingAttentionPlan,
	findLikelyParentChildTrackingMisses,
	formatIssueDetailSections,
	formatIssueSummaryLine,
	formatParentChildCheckOutput,
	normalizeCliAliases,
	normalizeIssueJsonFlagOrder,
	parseGitWorktreeListPaths,
	resolveCliExecutionMode,
	summarizeIssueBulkCreateResults,
	summarizeIssueBulkUpdateResults,
} from "./index.js"

const makePrimeIssue = (id: string, overrides: Partial<TrackedIssue> = {}): TrackedIssue => ({
	id,
	title: `Issue ${id}`,
	status: "open",
	priority: 3,
	issue_type: "task",
	created_at: "2026-03-08T00:00:00.000Z",
	updated_at: "2026-03-08T00:00:00.000Z",
	implementations: ["default"],
	...overrides,
})

describe("buildPrimeOutput", () => {
	it("includes issue-context guardrails and refresh instructions for active issues", () => {
		const output = buildPrimeOutput(
			"gq",
			{
				issue: makePrimeIssue("gq", {
					title: "Improve az prime",
					status: "in_progress",
					priority: 2,
					description: "Trim noisy guidance and keep the active issue block concise.",
				}),
				showImplementations: false,
			},
			undefined,
			true,
		)

		expect(output).toContain("Issue-context guardrails:")
		expect(output).toContain("AZEDARACH_ISSUE_ID` is set to `gq`")
		expect(output).toContain("refresh stale context with `az issue get gq`")
		expect(output).toContain(
			"Missing fields (for example description/design/acceptance/notes) are valid.",
		)
		expect(output).toContain("Do not go on history/log hunting tangents")
		expect(output).toContain(
			"When fanning out to subagents, tell each subagent to use `az issue` and create/maintain its own child issue under the active parent; reserve `az prime` for the orchestrator unless a subagent explicitly needs a fresh primer.",
		)
		expect(output).toContain(
			'`az issue create "Title"` defaults to the active parent context (including `AZEDARACH_ISSUE_ID`) unless `--deferred` is set.',
		)
		expect(output).toContain(
			"In this repo, when guidance says `spec`, it means `az spec` requirement/link records",
		)
		expect(output).toContain(
			"Treat `az spec link` records as required traceability for behavior work",
		)
		expect(output).toContain(
			"Before coding behavior changes, confirm the issue has the right requirement links",
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
		expect(output).toContain("For `az` commands, keep canonical CLI ordering")
		expect(output).toContain("prefer named flags over positional refs")
		expect(output).toContain("For `az spec link add`, use either explicit refs")
		expect(output).toContain("Avoid positional-first ordering like `az spec link add <issue-id>")
		expect(output).toContain("az config set spec.enabled false")
		expect(output).toContain('record "Spec impact: none" with concrete file-based rationale')
		expect(output).toContain(
			"Review flow policy: reviews target closed tasks, not in-progress tasks.",
		)
		expect(output).toContain("If review finds remaining work, move the issue back to in-progress")
		expect(output).toContain("Active issue context (AZEDARACH_ISSUE_ID=gq):")
		expect(output).toContain("Refresh with `az issue get gq` if this looks stale.")
		expect(output).toContain("gq: Improve az prime [status=in_progress priority=2 type=task")
		expect(output).toContain(
			"Description:\nTrim noisy guidance and keep the active issue block concise.",
		)
		expect(output).toContain("`az issue bulk-create --input issues.json --json`")
		expect(output).toContain('`[{"title":"Agent-created task"}]`')
		expect(output).toContain("`az issue bulk-update --input updates.json --json`")
		expect(output).toContain('`[{"id":"az-123","status":"blocked"}]`')
		expect(output).not.toContain("Start each session with: `az prime`")
		expect(output).not.toContain("Implementation guardrails:")
	})

	it("guides users to fetch an issue when no issue id is configured", () => {
		const output = buildPrimeOutput(undefined, undefined, undefined, true)

		expect(output).toContain("No active issue is preselected")
		expect(output).toContain("run `az issue get <issue-id>`")
		expect(output).not.toContain("Active issue context (AZEDARACH_ISSUE_ID=")
	})

	it("falls back to explicit refresh command when issue details fail to load", () => {
		const output = buildPrimeOutput("gq", undefined, undefined, true)

		expect(output).toContain("Active issue context (AZEDARACH_ISSUE_ID=gq):")
		expect(output).toContain("Could not load issue details automatically; run `az issue get gq`.")
	})

	it("keeps implementation guidance invisible while only one implementation exists", () => {
		const output = buildPrimeOutput(
			"gq",
			{
				issue: makePrimeIssue("gq", {
					title: "Improve az prime",
				}),
			},
			{
				implementations: ["default"],
			},
			true,
		)

		expect(output).not.toContain("Implementation guardrails:")
		expect(output).not.toContain("implicit `default` fallback")
	})

	it("warns explicitly when multiple implementations are configured", () => {
		const output = buildPrimeOutput(
			"gq",
			{
				issue: makePrimeIssue("gq", {
					title: "Improve az prime",
					implementations: ["default", "ts-opentui", "go-bubbletea"],
				}),
				showImplementations: true,
			},
			{
				implementations: ["default", "ts-opentui", "go-bubbletea"],
			},
			true,
		)

		expect(output).toContain("Implementation guardrails:")
		expect(output).toContain(
			"This project has multiple implementations configured: default, ts-opentui, go-bubbletea.",
		)
		expect(output).toContain(
			"New `az issue` and `az spec link` writes must include one or more `--impl <impl>` selections.",
		)
		expect(output).toContain(
			"The implicit `default` fallback only applies while the project has exactly one implementation configured.",
		)
		expect(output).toContain(
			"Repeated `--impl` flags mean intentionally shared work, for example `--impl ts-opentui --impl go-bubbletea`.",
		)
	})

	it("omits all spec guidance when spec workflows are disabled", () => {
		const output = buildPrimeOutput(
			"gq",
			{
				issue: makePrimeIssue("gq", {
					title: "Improve az prime",
					implementations: ["default", "ts-opentui"],
				}),
				showImplementations: true,
			},
			{
				implementations: ["default", "ts-opentui"],
			},
			false,
		)

		expect(output).not.toContain("`az spec`")
		expect(output).not.toContain("required traceability for behavior work")
		expect(output).toContain(
			"New `az issue` writes must include one or more `--impl <impl>` selections.",
		)
		expect(output).not.toContain(
			"New `az issue` and `az spec link` writes must include one or more `--impl <impl>` selections.",
		)
	})

	it("applies --config overrides to command-layer AppConfig reads", async () => {
		const configPath = `${process.env.TMPDIR ?? "/tmp"}/az-config-${crypto.randomUUID()}.json`
		await Bun.write(
			configPath,
			`${JSON.stringify({ $version: 7, spec: { enabled: false } }, null, 2)}\n`,
		)

		const specEnabled = await Effect.runPromise(
			Effect.gen(function* () {
				const appConfig = yield* AppConfig
				const specConfig = yield* appConfig.getSpecConfig()
				return specConfig.enabled
			}).pipe(
				Effect.provide(buildCommandCliLayerForArgv(["bun", "az", "--config", configPath, "prime"])),
			),
		)

		expect(specEnabled).toBe(false)
	})

	it("writes spec.enabled through az config set", async () => {
		const configPath = `${process.env.TMPDIR ?? "/tmp"}/az-config-set-${crypto.randomUUID()}.json`
		await Bun.write(
			configPath,
			`${JSON.stringify({ $version: 7, spec: { enabled: true } }, null, 2)}\n`,
		)

		await Effect.runPromise(
			cliRunner([
				"bun",
				"az",
				"--config",
				configPath,
				"config",
				"set",
				"spec.enabled",
				"false",
			]).pipe(Effect.provide(BunContext.layer)),
		)

		const updated = JSON.parse(await Bun.file(configPath).text()) as {
			spec?: { enabled?: boolean }
		}
		expect(updated.spec?.enabled).toBe(false)
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

	it("moves issue dep remove options ahead of positional ids when ids are first", () => {
		const argv = [
			"bun",
			"az",
			"issue",
			"dep",
			"remove",
			"AZE-200",
			"AZE-123",
			"--type",
			"parent-child",
		]

		expect(normalizeIssueJsonFlagOrder(argv)).toEqual([
			"bun",
			"az",
			"issue",
			"dep",
			"remove",
			"--type",
			"parent-child",
			"AZE-200",
			"AZE-123",
		])
	})
})

describe("decodeIssueBulkCreatePayload", () => {
	it("accepts a bare array payload", async () => {
		const decoded = await Effect.runPromise(
			decodeIssueBulkCreatePayload(
				JSON.stringify([
					{
						title: "Bulk-created task",
						type: "task",
						labels: ["agent", "json"],
					},
				]),
			),
		)

		expect(decoded).toEqual([
			{
				title: "Bulk-created task",
				type: "task",
				labels: ["agent", "json"],
			},
		])
	})

	it("accepts an object payload with issues", async () => {
		const decoded = await Effect.runPromise(
			decodeIssueBulkCreatePayload(
				JSON.stringify({
					issues: [
						{
							title: "First bulk-created task",
							priority: 2,
						},
						{
							title: "Second bulk-created task",
							description: "Created via JSON wrapper",
						},
					],
				}),
			),
		)

		expect(decoded).toEqual([
			{
				title: "First bulk-created task",
				priority: 2,
			},
			{
				title: "Second bulk-created task",
				description: "Created via JSON wrapper",
			},
		])
	})

	it("rejects an empty payload", async () => {
		await expect(
			Effect.runPromise(decodeIssueBulkCreatePayload(JSON.stringify([]))),
		).rejects.toThrow("Bulk create input must contain at least one issue item.")
	})
})

describe("decodeIssueBulkUpdatePayload", () => {
	it("accepts a bare array payload", async () => {
		const decoded = await Effect.runPromise(
			decodeIssueBulkUpdatePayload(
				JSON.stringify([
					{
						id: "dg",
						status: "blocked",
						labels: ["agent", "bulk"],
					},
				]),
			),
		)

		expect(decoded).toEqual([
			{
				id: "dg",
				status: "blocked",
				labels: ["agent", "bulk"],
			},
		])
	})

	it("accepts an object payload with updates", async () => {
		const decoded = await Effect.runPromise(
			decodeIssueBulkUpdatePayload(
				JSON.stringify({
					updates: [
						{
							id: "dg",
							notes: "bulk edit",
						},
						{
							id: "dh",
							parent: "dg",
						},
					],
				}),
			),
		)

		expect(decoded).toEqual([
			{
				id: "dg",
				notes: "bulk edit",
			},
			{
				id: "dh",
				parent: "dg",
			},
		])
	})

	it("rejects an empty payload", async () => {
		await expect(
			Effect.runPromise(decodeIssueBulkUpdatePayload(JSON.stringify([]))),
		).rejects.toThrow("Bulk update input must contain at least one update item.")
	})
})

describe("summarizeIssueBulkCreateResults", () => {
	it("computes created and failed counts from per-item results", () => {
		const summary = summarizeIssueBulkCreateResults([
			{
				index: 0,
				requestedTitle: "First task",
				issueId: "a",
				created: true,
			},
			{
				index: 1,
				created: false,
				error: "title is required",
			},
		])

		expect(summary.requestCount).toBe(2)
		expect(summary.createdCount).toBe(1)
		expect(summary.failedCount).toBe(1)
		expect(summary.results[1]?.error).toBe("title is required")
	})
})

describe("summarizeIssueBulkUpdateResults", () => {
	it("computes updated and failed counts from per-item results", () => {
		const summary = summarizeIssueBulkUpdateResults([
			{
				index: 0,
				requestedId: "dg",
				issueId: "dg",
				updated: true,
			},
			{
				index: 1,
				requestedId: "missing",
				issueId: "missing",
				updated: false,
				error: "Issue not found: missing",
			},
		])

		expect(summary.requestCount).toBe(2)
		expect(summary.updatedCount).toBe(1)
		expect(summary.failedCount).toBe(1)
		expect(summary.results[1]?.error).toBe("Issue not found: missing")
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
		expect(resolveCliExecutionMode(["bun", "az", "config", "set", "spec.enabled", "false"])).toBe(
			"command",
		)
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
		expect(normalizeCliAliases(["bun", "az", "issue", "d", "rm", "AZE-1", "AZE-2"])).toEqual([
			"bun",
			"az",
			"issue",
			"dep",
			"remove",
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

describe("parseGitWorktreeListPaths", () => {
	it("extracts worktree paths from git porcelain output", () => {
		const output = [
			"worktree /repo/main",
			"HEAD 1234567890",
			"branch refs/heads/main",
			"",
			"worktree /repo/feature-a",
			"HEAD abcdef0123",
			"branch refs/heads/feature-a",
			"locked",
			"",
		].join("\n")

		expect(parseGitWorktreeListPaths(output)).toEqual(["/repo/main", "/repo/feature-a"])
	})

	it("deduplicates repeated worktree entries and ignores unrelated lines", () => {
		const output = [
			"worktree /repo/main",
			"HEAD 1234567890",
			"worktree /repo/main",
			"prunable gitdir file points to non-existent location",
			"random text",
			"worktree   ",
		].join("\n")

		expect(parseGitWorktreeListPaths(output)).toEqual(["/repo/main"])
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
			implementations: ["default"],
		})

		expect(line.includes("\n")).toBe(false)
		expect(line).toContain("az-123: Fix sqlite refresh")
		expect(line).toContain("status=in_progress")
		expect(line).toContain("priority=1")
		expect(line).toContain("type=task")
	})

	it("includes implementation scope when a non-default assignment should be surfaced", () => {
		const line = formatIssueSummaryLine(
			{
				id: "az-124",
				title: "Ship ts-only work",
				status: "open",
				priority: 2,
				issue_type: "task",
				created_at: "2026-03-05T10:00:00.000Z",
				updated_at: "2026-03-05T11:00:00.000Z",
				implementations: ["ts-opentui"],
			},
			{ showImplementations: true },
		)

		expect(line).toContain("impl=ts-opentui")
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
			implementations: ["ts-opentui", "go-bubbletea"],
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
			"Implementations:\nts-opentui, go-bubbletea",
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
			implementations: ["default"],
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
			implementations: ["default"],
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
			implementations: ["default"],
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
			implementations: ["default"],
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
				implementations: ["default"],
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
						implementations: ["default"],
						fulfillment_status: "complete",
						fulfillment_percent: 100,
						evidence_note: null,
					},
					{
						id: "AZ-AT-2901",
						local_id: "at2901",
						external_code: "AZ-AT-2901",
						title: "Acceptance path is covered",
						kind: "acceptance",
						link_type: "tests",
						implementations: ["default"],
						fulfillment_status: "verified",
						fulfillment_percent: 100,
						evidence_note: null,
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
	implementations: ["default"],
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
