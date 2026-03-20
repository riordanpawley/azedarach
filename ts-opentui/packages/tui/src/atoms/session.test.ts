import { describe, expect, it } from "bun:test"
import { buildSessionUpdateStateRequest } from "./session.js"

describe("buildSessionUpdateStateRequest", () => {
	it("maps tmux update metadata into daemon sessionUpdateState payload", () => {
		const payload = buildSessionUpdateStateRequest(
			{
				issueId: "AZE-123",
				status: "busy",
				sessionName: "az-AZE-123",
				createdAt: 1_710_000_000,
				worktreePath: "/tmp/project/.worktrees/AZE-123",
				projectPath: "/tmp/project",
			},
			null,
		)

		expect(payload).toEqual({
			issueId: "AZE-123",
			state: "busy",
			projectPath: "/tmp/project",
			tmuxSessionName: "az-AZE-123",
			worktreePath: "/tmp/project/.worktrees/AZE-123",
			startedAt: "2024-03-09T16:00:00.000Z",
		})
	})

	it("falls back to current project path and null startedAt for synthetic disappearance", () => {
		const payload = buildSessionUpdateStateRequest(
			{
				issueId: "AZE-999",
				status: "idle",
				sessionName: "az-AZE-999",
				createdAt: 0,
				worktreePath: null,
				projectPath: null,
			},
			"/tmp/current-project",
		)

		expect(payload).toEqual({
			issueId: "AZE-999",
			state: "idle",
			projectPath: "/tmp/current-project",
			tmuxSessionName: "az-AZE-999",
			worktreePath: null,
			startedAt: null,
		})
	})
})
