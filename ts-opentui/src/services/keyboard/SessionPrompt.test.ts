import { describe, expect, it } from "bun:test"
import { buildStartWorkPrompt } from "./SessionPrompt.js"

describe("session prompts", () => {
	it("uses az prime bootstrap in start prompt", () => {
		const prompt = buildStartWorkPrompt({
			taskId: "az-f4625d",
			issueType: "task",
			title: "Fix prompt backend",
			hasWorktree: false,
			attachmentPaths: [],
			localMode: false,
			specEnabled: true,
		})

		expect(prompt).toContain("work on issue az-f4625d")
		expect(prompt).toContain("Start by running `az prime`.")
		expect(prompt).toContain("`AZEDARACH_ISSUE_ID` is already set for this session")
		expect(prompt).toContain("`az issue get az-f4625d`")
		expect(prompt).toContain(
			'when a prompt says "spec", it means `az spec` requirement/link records',
		)
		expect(prompt).toContain(
			"Before implementing behavior changes, inspect relevant `az spec` requirements/links.",
		)
		expect(prompt).toContain(
			"After implementing behavior changes, verify compliance against linked `az spec` requirements and update `az spec` requirement/link records when scope changes.",
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
			specEnabled: true,
		})

		expect(prompt).toContain("Local workflow mode guardrails:")
		expect(prompt).toContain("Do not use `git -C [path]` unless intentionally targeting")
		expect(prompt).toContain("Do not run remote cleanup/sync commands unless explicitly asked.")
	})

	it("sanitizes tag-like and control-character input in prompt titles", () => {
		const prompt = buildStartWorkPrompt({
			taskId: "ez",
			issueType: "task",
			title: "Fix parser <image name=[Image #1]>\nwith weird chars",
			hasWorktree: false,
			attachmentPaths: [],
			localMode: false,
			specEnabled: true,
		})

		expect(prompt).toContain("Fix parser [image name=[Image #1]] with weird chars")
		expect(prompt).not.toContain("<image")
		expect(prompt).not.toContain("\nwith weird chars")
	})

	it("omits spec guidance when spec workflows are disabled", () => {
		const prompt = buildStartWorkPrompt({
			taskId: "ja",
			issueType: "task",
			title: "Respect spec gating",
			hasWorktree: false,
			attachmentPaths: [],
			localMode: false,
			specEnabled: false,
		})

		expect(prompt).not.toContain("`az spec`")
		expect(prompt).toContain("Start by running `az prime`.")
		expect(prompt).toContain("If context looks stale, refresh with `az issue get ja`.")
	})
})
