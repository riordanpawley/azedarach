import { describe, expect, it } from "bun:test"
import { Cause, Effect, Option } from "effect"
import { parseGitBehindCount } from "./GitSyncService.js"

describe("GitSyncService parseGitBehindCount", () => {
	it("parses valid integer output", async () => {
		const count = await Effect.runPromise(
			parseGitBehindCount("42\n", "git rev-list --count main..origin/main"),
		)
		expect(count).toBe(42)
	})

	it("fails with tagged parse error when output is not an integer", async () => {
		const exit = await Effect.runPromiseExit(
			parseGitBehindCount("fatal: not a git repository", "git rev-list --count main..origin/main"),
		)
		expect(exit._tag).toBe("Failure")
		if (exit._tag !== "Failure") {
			return
		}
		const failure = Cause.failureOption(exit.cause)
		expect(Option.isSome(failure)).toBe(true)
		if (Option.isNone(failure)) {
			return
		}
		const error = failure.value

		expect(error?._tag).toBe("GitBehindCountParseError")
		expect(error?.command).toBe("git rev-list --count main..origin/main")
		expect(error?.output).toContain("fatal:")
	})
})
