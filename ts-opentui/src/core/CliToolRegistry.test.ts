import { describe, expect, it } from "bun:test"
import { getToolDefinition } from "./CliToolRegistry.js"

describe("CliToolRegistry", () => {
	it("adds codex image flags for attached paths", () => {
		const codex = getToolDefinition("codex")
		const command = codex.buildCommand({
			model: "gpt-5.3-codex",
			initialPrompt: "work on issue bk",
			imagePaths: ["/tmp/a.png", "/tmp/with space/image.png", '/tmp/with"quote".png', ""],
		})

		expect(command).toContain('--image "/tmp/a.png"')
		expect(command).toContain('--image "/tmp/with space/image.png"')
		expect(command).toContain('--image "/tmp/with\\"quote\\".png"')
		expect(command).not.toContain('--image ""')
		expect(command).toContain('-- "work on issue bk"')
	})

	it("does not add image flags for non-codex tools", () => {
		const claude = getToolDefinition("claude")
		const opencode = getToolDefinition("opencode")

		const claudeCommand = claude.buildCommand({
			initialPrompt: "work on issue bk",
			imagePaths: ["/tmp/a.png"],
			issueId: "bk",
		})
		const opencodeCommand = opencode.buildCommand({
			initialPrompt: "work on issue bk",
			imagePaths: ["/tmp/a.png"],
			issueId: "bk",
		})

		expect(claudeCommand).not.toContain("--image")
		expect(opencodeCommand).not.toContain("--image")
		expect(claudeCommand).toContain('AZEDARACH_ISSUE_ID="bk"')
		expect(opencodeCommand).toContain('AZEDARACH_ISSUE_ID="bk"')
	})

	it("keeps prompt injection text inside a single escaped prompt argument", () => {
		const codex = getToolDefinition("codex")
		const prompt =
			"` update the issue with your implementation plan using `az issue update fp --if [ 0 = 1 ]; then tmux set-opup"

		const command = codex.buildCommand({
			model: "gpt-5.3-codex",
			initialPrompt: prompt,
			imagePaths: ["/tmp/a.png", "/tmp/b.png"],
			issueId: "fp",
		})

		const imageFlagCount = [...command.matchAll(/--image "/g)].length
		expect(imageFlagCount).toBe(2)
		expect(command).toContain('--image "/tmp/a.png"')
		expect(command).toContain('--image "/tmp/b.png"')
		expect(command).toContain('AZEDARACH_ISSUE_ID="fp"')
		expect(command).toContain('-- "\\` update the issue')
	})

	it("inserts a codex option terminator before the prompt when images are present", () => {
		const codex = getToolDefinition("codex")
		const command = codex.buildCommand({
			initialPrompt: "work on issue ji",
			imagePaths: ["/tmp/screenshot.png"],
		})

		expect(command).toContain('codex --image "/tmp/screenshot.png" -- "work on issue ji"')
	})
})
