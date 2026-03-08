import { describe, expect, it } from "bun:test"
import { GitError } from "../core/WorktreeManager.js"
import { format, formatForToast } from "./ErrorFormatter.js"

describe("ErrorFormatter GitError recovery hints", () => {
	it("formats stale git lock failures with targeted recovery steps", () => {
		const error = new GitError({
			message: "git commit failed",
			command: 'git commit -m "example"',
			stderr:
				"fatal: Unable to create '/Users/test/repo/.git/index.lock': File exists.\nremove the file manually to continue.",
			recovery: {
				_tag: "stale-lock-file",
				lockFilePath: "/Users/test/repo/.git/index.lock",
			},
		})

		const formatted = format(error)
		expect(formatted.message).toBe("Git lock file blocked the operation")
		expect(formatted.category).toBe("git")
		expect(formatted.suggestion).toContain("rm /Users/test/repo/.git/index.lock")

		const toastMessage = formatForToast(error)
		expect(toastMessage).toContain("Git lock file blocked the operation")
		expect(toastMessage).toContain("Ensure no other git command is running")
	})
})
