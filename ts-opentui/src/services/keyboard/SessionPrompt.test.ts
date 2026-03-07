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
			localMode: false,
		})

		expect(prompt).toContain("work on issue az-f4625d")
		expect(prompt).toContain(
			"Context for this session is already injected (`az prime` + `az issue get az-f4625d`)",
		)
		expect(prompt).toContain("Only rerun `az issue get az-f4625d` if details are stale or missing")
		expect(prompt).toContain("`az issue update az-f4625d --design '...'`")
		expect(prompt).toContain("avoid unescaped backticks in `--design` text")
		expect(prompt).toContain("Issue nesting rule:")
		expect(prompt).toContain("must be completed before closing `az-f4625d`")
		expect(prompt).toContain("child of `az-f4625d`")
		expect(prompt).toContain("`az issue update [new-id] --parent az-f4625d`")
		expect(prompt).toContain("intentionally deferred to a later session")
		expect(prompt).toContain("do NOT make it a child")
		expect(prompt).toContain("Close `az-f4625d` only after its child issues are completed")
		expect(prompt).toContain(
			"`az issue dep add --type discovered-from [new-id] az-f4625d`",
		)
		expect(prompt).not.toContain("tracker show")
		expect(prompt).not.toContain("linear-cli")
	})

	it("adds local workflow guardrails when local mode is enabled", () => {
		const prompt = buildStartWorkPrompt({
			taskId: "ar",
			issueType: "task",
			title: "Keep local workflow local",
			hasWorktree: false,
			attachmentPaths: [],
			localMode: true,
		})

		expect(prompt).toContain("Local workflow mode guardrails:")
		expect(prompt).toContain("Do not use `git -C [path]` unless intentionally targeting")
		expect(prompt).toContain("Do not run remote cleanup/sync commands unless explicitly asked.")
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
		expect(prompt).toContain("`/model [model]`")
		expect(prompt).not.toContain("tracker show")
		expect(prompt).not.toContain("linear-cli")
	})

	it("sanitizes tag-like and control-character input in prompt titles", () => {
		const prompt = buildStartWorkPrompt({
			taskId: "ez",
			issueType: "task",
			title: "Fix parser <image name=[Image #1]>\nwith weird chars",
			hasWorktree: false,
			attachmentPaths: [],
			localMode: false,
		})

		expect(prompt).toContain("Fix parser [image name=[Image #1]] with weird chars")
		expect(prompt).not.toContain("<image")
		expect(prompt).not.toContain("\nwith weird chars")
	})
})
