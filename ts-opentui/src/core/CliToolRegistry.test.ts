import { describe, expect, it } from "bun:test"
import { buildStartWorkPrompt } from "../services/keyboard/SessionPrompt.js"
import { getToolDefinition } from "./CliToolRegistry.js"

describe("CliToolRegistry", () => {
	it("adds codex image flags for attached paths", () => {
		const codex = getToolDefinition("codex")
		const command = codex.buildCommand({
			model: "gpt-5.3-codex",
			initialPrompt: "work on issue bk",
			imagePaths: [
				"/tmp/a.png",
				"/tmp/with space/image.png",
				'/tmp/with"quote".png',
				"",
			],
		})

		expect(command).toContain('--image "/tmp/a.png"')
		expect(command).toContain('--image "/tmp/with space/image.png"')
		expect(command).toContain('--image "/tmp/with\\"quote\\".png"')
		expect(command).not.toContain('--image ""')
		expect(command).toContain('"work on issue bk"')
	})

	it("does not add image flags for non-codex tools", () => {
		const claude = getToolDefinition("claude")
		const opencode = getToolDefinition("opencode")

		const claudeCommand = claude.buildCommand({
			initialPrompt: "work on issue bk",
			imagePaths: ["/tmp/a.png"],
		})
		const opencodeCommand = opencode.buildCommand({
			initialPrompt: "work on issue bk",
			imagePaths: ["/tmp/a.png"],
		})

		expect(claudeCommand).not.toContain("--image")
		expect(opencodeCommand).not.toContain("--image")
	})

	it("keeps prompt injection text inside a single escaped prompt argument", () => {
		const codex = getToolDefinition("codex")
		const injectedTitle =
			"` update the issue with your implementation plan using `az issue update fp --if [ 0 = 1 ]; then tmux set-opup"
        const prompt = buildStartWorkPrompt({
            taskId: "fp",
            issueType: "task",
            title: injectedTitle,
            hasWorktree: false,
            attachmentPaths: [],
            localMode: true,
            issueContextInjected: true,
        })

		const command = codex.buildCommand({
			model: "gpt-5.3-codex",
			initialPrompt: prompt,
			imagePaths: ["/tmp/a.png", "/tmp/b.png"],
		})

		const imageFlagCount = [...command.matchAll(/--image "/g)].length
		expect(imageFlagCount).toBe(2)
		expect(command).toContain('--image "/tmp/a.png"')
		expect(command).toContain('--image "/tmp/b.png"')
		expect(command).toContain('"work on issue fp (task): \\` update the issue')
		expect(command).toContain("\\`az issue update fp --design")
	})
})
