import { describe, expect, it } from "bun:test"
import { buildChatPrompt, buildStartWorkPrompt } from "./SessionPrompt.js"

describe("session prompts", () => {
	it("mentions injected az issue context in start prompt", () => {
		const prompt = buildStartWorkPrompt({
			taskId: "az-f4625d",
			issueType: "task",
			title: "Fix prompt backend",
			hasWorktree: false,
			attachmentPaths: [],
		})

		expect(prompt).toContain("work on issue az-f4625d")
		expect(prompt).toContain(
			"Context for this session is already injected (`az prime` + `az issue get az-f4625d`)",
		)
		expect(prompt).toContain("Only rerun `az issue get az-f4625d` if details are stale or missing")
		expect(prompt).toContain('`az issue update az-f4625d --design "..."`')
		expect(prompt).not.toContain("tracker show")
		expect(prompt).not.toContain("linear-cli")
	})

	it("mentions injected az issue context in chat prompt", () => {
		const prompt = buildChatPrompt({
			taskId: "az-f4625d",
			title: "Discuss scope",
			chatModel: "haiku",
		})

		expect(prompt).toContain("Let's chat about issue az-f4625d")
		expect(prompt).toContain(
			"Context for this session is already injected (`az prime` + `az issue get az-f4625d`)",
		)
		expect(prompt).toContain("Only rerun `az issue get az-f4625d` if details are stale or missing")
		expect(prompt).not.toContain("tracker show")
		expect(prompt).not.toContain("linear-cli")
	})
})
