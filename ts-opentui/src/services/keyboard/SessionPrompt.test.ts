import { describe, expect, it } from "bun:test"
import { buildChatPrompt, buildStartWorkPrompt } from "./SessionPrompt.js"

describe("session prompts", () => {
	it("uses az issue get/update in start prompt", () => {
		const prompt = buildStartWorkPrompt({
			taskId: "az-f4625d",
			issueType: "task",
			title: "Fix prompt backend",
			hasWorktree: false,
			attachmentPaths: [],
		})

		expect(prompt).toContain("work on issue az-f4625d")
		expect(prompt).toContain("Run `az issue get az-f4625d`")
		expect(prompt).toContain('`az issue update az-f4625d --design "..."`')
		expect(prompt).not.toContain("bd show")
		expect(prompt).not.toContain("linear-cli")
	})

	it("uses az issue get in chat prompt", () => {
		const prompt = buildChatPrompt({
			taskId: "az-f4625d",
			title: "Discuss scope",
			chatModel: "haiku",
		})

		expect(prompt).toContain("Let's chat about issue az-f4625d")
		expect(prompt).toContain("Run `az issue get az-f4625d`")
		expect(prompt).not.toContain("bd show")
		expect(prompt).not.toContain("linear-cli")
	})
})
