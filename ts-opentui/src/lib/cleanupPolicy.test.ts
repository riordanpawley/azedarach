import { describe, expect, it } from "bun:test"
import { createWorktreeOnlyCleanupOptions, isActionPaletteNetworkAction } from "./cleanupPolicy.js"

describe("cleanupPolicy", () => {
	it("keeps issues open for manual worktree cleanup", () => {
		expect(createWorktreeOnlyCleanupOptions("jt", "/tmp/project")).toEqual({
			issueId: "jt",
			projectPath: "/tmp/project",
			closeIssue: false,
		})
	})

	it("treats cleanup as a local palette action", () => {
		expect(isActionPaletteNetworkAction("d")).toBe(false)
		expect(isActionPaletteNetworkAction("P")).toBe(true)
		expect(isActionPaletteNetworkAction("O")).toBe(true)
	})
})
