import { describe, expect, it } from "bun:test"
import type { TaskWithSession } from "../types.js"
import { findNextNonEmptyColumnIndex } from "./mouse.js"

const createTask = (id: string): TaskWithSession => ({
	id,
	title: id,
	status: "open",
	priority: 2,
	issue_type: "task",
	created_at: "2026-01-01T00:00:00.000Z",
	updated_at: "2026-01-01T00:00:00.000Z",
	implementations: [],
	sessionState: "idle",
})

describe("findNextNonEmptyColumnIndex", () => {
	it("skips empty columns when moving right", () => {
		const tasksByColumn: readonly (readonly TaskWithSession[])[] = [
			[createTask("a")],
			[],
			[],
			[createTask("b"), createTask("c")],
		]

		expect(findNextNonEmptyColumnIndex(tasksByColumn, 0, 1)).toBe(3)
	})

	it("skips empty columns when moving left", () => {
		const tasksByColumn: readonly (readonly TaskWithSession[])[] = [
			[createTask("a")],
			[],
			[],
			[createTask("b"), createTask("c")],
		]

		expect(findNextNonEmptyColumnIndex(tasksByColumn, 3, -1)).toBe(0)
	})

	it("returns undefined when no non-empty target exists", () => {
		const tasksByColumn: readonly (readonly TaskWithSession[])[] = [[createTask("a")], [], []]

		expect(findNextNonEmptyColumnIndex(tasksByColumn, 0, -1)).toBeUndefined()
		expect(findNextNonEmptyColumnIndex(tasksByColumn, 0, 1)).toBeUndefined()
	})
})
