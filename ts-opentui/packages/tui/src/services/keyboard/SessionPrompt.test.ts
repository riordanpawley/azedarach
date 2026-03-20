import { describe, expect, it } from "bun:test"
import { buildStartWorkPrompt } from "./SessionPrompt.js"

describe("buildStartWorkPrompt", () => {
	it("builds the starter prompt from issue metadata", () => {
		expect(
			buildStartWorkPrompt({
				taskId: "az-123",
				issueType: "task",
				title: "Tighten daemon lifecycle boundary",
			}),
		).toBe(`work on issue az-123 (task): Tighten daemon lifecycle boundary

Start by running \`az prime\`. Then continue the task using the context it prints without waiting for further instruction.`)
	})

	it("sanitizes control characters and angle brackets", () => {
		expect(
			buildStartWorkPrompt({
				taskId: "az-123",
				issueType: "task\n",
				title: "Fix <keyboard>\u0000 bridge",
			}),
		).toContain("(task): Fix [keyboard] bridge")
	})
})
