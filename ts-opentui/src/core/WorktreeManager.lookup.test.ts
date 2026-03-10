import { describe, expect, it } from "bun:test"
import { findWorktreeForIssue, type Worktree } from "./WorktreeManager.js"

const makeWorktree = (overrides: Partial<Worktree>): Worktree => ({
	path: "/tmp/ts-opentui-jt",
	issueId: "jt",
	branch: "riordan/jt/fix-cleanup",
	isLocked: false,
	head: "abc123",
	...overrides,
})

describe("findWorktreeForIssue", () => {
	it("matches by issue id when the cache key is correct", () => {
		const worktree = makeWorktree({})

		expect(
			findWorktreeForIssue([worktree], {
				issueId: "jt",
				expectedWorktreePath: "/tmp/ts-opentui-jt",
				pathMatches: (leftPath, rightPath) => leftPath === rightPath,
			}),
		).toEqual(worktree)
	})

	it("falls back to the expected worktree path when the parsed issue id differs", () => {
		const worktree = makeWorktree({
			issueId: "riordan/jt/fix-cleanup",
			path: "/tmp/ts-opentui-jt",
		})

		expect(
			findWorktreeForIssue([worktree], {
				issueId: "jt",
				expectedWorktreePath: "/tmp/ts-opentui-jt",
				pathMatches: (leftPath, rightPath) => leftPath === rightPath,
			}),
		).toEqual(worktree)
	})

	it("matches issue ids case-insensitively for linear-style keys", () => {
		const worktree = makeWorktree({
			issueId: "AZE-123",
			path: "/tmp/ts-opentui-AZE-123",
		})

		expect(
			findWorktreeForIssue([worktree], {
				issueId: "aze-123",
				expectedWorktreePath: "/tmp/ts-opentui-aze-123",
				pathMatches: (leftPath, rightPath) => leftPath.toLowerCase() === rightPath.toLowerCase(),
			}),
		).toEqual(worktree)
	})
})
