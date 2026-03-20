import { describe, expect, it } from "bun:test"
import {
	DEFAULT_CLI_TOOL,
	deepMerge,
	generateHookConfig,
	getSupportedTools,
	getToolDefinition,
	isValidToolName,
	type JsonObject,
} from "./DaemonCliTooling.js"

describe("DaemonCliTooling", () => {
	it("builds shell commands for supported tools", () => {
		const claude = getToolDefinition("claude")
		expect(
			claude.buildCommand({
				issueId: "AZE-123",
				initialPrompt: 'Check "quotes"',
				model: "sonnet",
				sessionEnv: { FOO: "bar", AZEDARACH_ISSUE_ID: "ignored" },
			}),
		).toContain('AZEDARACH_ISSUE_ID="AZE-123"')
		expect(claude.buildCommand({ initialPrompt: "hello" })).toContain('claude "hello"')

		const codex = getToolDefinition("codex")
		expect(
			codex.buildCommand({
				issueId: "task-1",
				sessionName: "pr-task-1",
				initialPrompt: "continue",
				continueConversation: true,
				imagePaths: ["/tmp/image.png", "   "],
				dangerouslySkipPermissions: true,
				model: "gpt-5",
			}),
		).toContain("resume --last")
		expect(
			codex.buildCommand({
				issueId: "task-1",
				sessionName: "pr-task-1",
				initialPrompt: "continue",
			}),
		).toContain("hooks.SessionStart")
	})

	it("generates the expected hook config", () => {
		const config = generateHookConfig("task-1", { projectPath: "/tmp/project" })
		expect(config.permissions.allow).toEqual([
			"Read(//**/.azedarach/tmp/attachments/**)",
			"Bash(tracker:*)",
			"Bash(az:*)",
		])
		expect(config.hooks.PreCompact?.[0]?.hooks[0]?.command).toContain("az-pre-compact.sh")
		expect(config.hooks.UserPromptSubmit?.[0]?.hooks[0]?.command).toContain('"pr-task-1"')
	})

	it("merges JSON objects deeply", () => {
		const left: JsonObject = {
			a: { nested: 1, list: ["x"] },
			b: ["left"],
		}
		const right: JsonObject = {
			a: { extra: 2, list: ["y"] },
			b: ["right"],
		}

		expect(deepMerge(left, right)).toEqual({
			a: { nested: 1, extra: 2, list: ["x", "y"] },
			b: ["left", "right"],
		})
	})

	it("exposes supported tool metadata", () => {
		expect(DEFAULT_CLI_TOOL).toBe("claude")
		expect(getSupportedTools()).toEqual(["claude", "opencode", "codex"])
		expect(isValidToolName("claude")).toBe(true)
		expect(isValidToolName("unknown")).toBe(false)
	})
})
