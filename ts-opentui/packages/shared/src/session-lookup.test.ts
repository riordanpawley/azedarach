import { describe, expect, it } from "bun:test"
import {
	buildIssueSessionCandidatesFromSessionNames,
	buildIssueSessionCandidatesFromSnapshots,
	findIssueSessionNameByIssueId,
} from "./session-lookup.js"

describe("session lookup helpers", () => {
	it("finds snapshot sessions by issue id using stable lookup comparison", () => {
		const candidates = buildIssueSessionCandidatesFromSnapshots([
			{ issueId: "AZE-123", tmuxSessionName: "snap-a" },
			{ issueId: "aze-456", tmuxSessionName: "snap-b" },
		])

		expect(findIssueSessionNameByIssueId("aze-123", candidates)).toBe("snap-a")
		expect(findIssueSessionNameByIssueId("AZE-456", candidates)).toBe("snap-b")
	})

	it("builds tmux session candidates with project-aware fallback", () => {
		const candidates = buildIssueSessionCandidatesFromSessionNames(
			["az-b", "ch-b", "az_x2e_foo", "claude-az_x2e_foo"],
			"/Users/user/prog/azedarach",
		)

		expect(candidates).toEqual([
			{ issueId: "b", sessionName: "az-b" },
			{ issueId: "az.foo", sessionName: "az_x2e_foo" },
			{ issueId: "az.foo", sessionName: "claude-az_x2e_foo" },
		])
		expect(findIssueSessionNameByIssueId("b", candidates)).toBe("az-b")
		expect(findIssueSessionNameByIssueId("az.foo", candidates)).toBe("az_x2e_foo")
		expect(findIssueSessionNameByIssueId("missing", candidates)).toBeNull()
	})
})
