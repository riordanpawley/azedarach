import { describe, expect, it } from "bun:test"
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
})
