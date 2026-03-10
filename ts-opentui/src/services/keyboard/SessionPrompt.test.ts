import { describe, expect, it } from "bun:test"
import { buildStartWorkPrompt } from "./SessionPrompt.js"

describe("session prompts", () => {
	it("uses a concise task prompt that boots through az prime", () => {
		const prompt = buildStartWorkPrompt({
			taskId: "jk",
			issueType: "task",
			title: "Register ts-opentui and go-bubbletea implementations",
		})

		expect(prompt).toContain(
			"work on issue jk (task): Register ts-opentui and go-bubbletea implementations",
		)
		expect(prompt).toContain("Start by running `az prime`.")
		expect(prompt).toContain("continue the task using the context it prints")
		expect(prompt).not.toContain("`AZEDARACH_ISSUE_ID` is already set")
		expect(prompt).not.toContain("`az issue get jk`")
		expect(prompt).not.toContain("`az spec` requirement/link records")
	})

	it("sanitizes and truncates long titles to keep the startup prompt short", () => {
		const prompt = buildStartWorkPrompt({
			taskId: "ez",
			issueType: "task",
			title: `Fix parser <image name=[Image #1]> ${"very long ".repeat(30)}`,
		})

		expect(prompt).toContain("Fix parser [image name=[Image #1]]")
		expect(prompt).not.toContain("<image")
		expect(prompt).toContain("...")
		expect(prompt.length).toBeLessThan(320)
	})
})
