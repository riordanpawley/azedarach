import { describe, expect, it } from "bun:test"
import { createStaleLockRecoveryHint, extractGitRecoveryHint } from "./gitRecovery.js"

describe("gitRecovery", () => {
	it("extracts stale lock hint from git stderr", () => {
		const hint = extractGitRecoveryHint(
			"fatal: Unable to create '/tmp/project/.git/worktrees/feature/index.lock': File exists.",
		)

		expect(hint).toEqual({
			_tag: "stale-lock-file",
			lockFilePath: "/tmp/project/.git/worktrees/feature/index.lock",
		})
	})

	it("returns undefined when stderr does not include a lock-file path", () => {
		const hint = extractGitRecoveryHint("fatal: not a git repository")
		expect(hint).toBeUndefined()
	})

	it("creates stale lock hints from explicit lock paths", () => {
		const hint = createStaleLockRecoveryHint("/tmp/project/.git/index.lock")
		expect(hint).toEqual({
			_tag: "stale-lock-file",
			lockFilePath: "/tmp/project/.git/index.lock",
		})
	})
})
