import { describe, expect, it } from "bun:test"
import type { TrackedIssue } from "@azedarach/shared/rpc"
import { buildBoardTaskSnapshots } from "./BackendDaemonBoardProjection.js"

const epicIssue: TrackedIssue = {
	id: "epic-1",
	title: "Epic",
	status: "open",
	priority: 1,
	issue_type: "epic",
	created_at: "2026-03-20T00:00:00.000Z",
	updated_at: "2026-03-20T00:00:00.000Z",
	implementations: [],
}

const parentIssue: TrackedIssue = {
	id: "task-1",
	title: "Parent task",
	status: "in_progress",
	priority: 2,
	issue_type: "task",
	created_at: "2026-03-20T00:00:00.000Z",
	updated_at: "2026-03-20T01:00:00.000Z",
	implementations: ["ts-opentui"],
	dependencies: [{ id: "epic-1", dependency_type: "parent-child", issue_type: "epic" }],
	notes: "PR: https://github.com/acme/repo/pull/42",
}

const childIssue: TrackedIssue = {
	id: "task-2",
	title: "Child task",
	status: "open",
	priority: 3,
	issue_type: "task",
	created_at: "2026-03-20T00:00:00.000Z",
	updated_at: "2026-03-20T02:00:00.000Z",
	implementations: [],
	dependencies: [{ id: "task-1", dependency_type: "parent-child", issue_type: "task" }],
}

describe("buildBoardTaskSnapshots", () => {
	it("projects tracker issues into daemon board tasks", () => {
		const tasks = buildBoardTaskSnapshots({
			issues: [epicIssue, parentIssue, childIssue],
			sessionStateByIssueId: new Map([["task-1", "busy"]]),
			devServerIssueIds: new Set(["task-2"]),
		})

		const parent = tasks.find((task) => task.id === "task-1")
		const child = tasks.find((task) => task.id === "task-2")

		expect(parent?.sessionState).toBe("busy")
		expect(parent?.hasWorktree).toBe(true)
		expect(parent?.hasPR).toBe(true)
		expect(parent?.prUrl).toBe("https://github.com/acme/repo/pull/42")
		expect(parent?.prNumber).toBe(42)
		expect(parent?.parentEpicId).toBe("epic-1")

		expect(child?.sessionState).toBe("idle")
		expect(child?.hasWorktree).toBeUndefined()
		expect(child?.hasDevServer).toBe(true)
		expect(child?.parentEpicId).toBe("epic-1")
	})

	it("keeps the last matching PR reference from notes", () => {
		const tasks = buildBoardTaskSnapshots({
			issues: [
				{
					...parentIssue,
					id: "task-3",
					notes: [
						"PR: https://github.com/acme/repo/pull/41",
						"PR: https://github.com/acme/repo/pull/42",
					].join("\n"),
				},
			],
			sessionStateByIssueId: new Map(),
			devServerIssueIds: new Set(),
		})

		expect(tasks[0]?.hasPR).toBe(true)
		expect(tasks[0]?.prUrl).toBe("https://github.com/acme/repo/pull/42")
		expect(tasks[0]?.prNumber).toBe(42)
	})
})
