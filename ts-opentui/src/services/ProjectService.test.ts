import { describe, expect, it } from "bun:test"
import path from "node:path"
import { isWorktreePathForProject, resolveConfigBasePath } from "./ProjectService.js"

describe("isWorktreePathForProject", () => {
	it("matches a sibling worktree root", () => {
		expect(
			isWorktreePathForProject(
				"/Users/riordan/prog/azedarach-az-1e6cc1",
				"/Users/riordan/prog/azedarach",
				path,
			),
		).toBe(true)
	})

	it("matches when cwd is inside a sibling worktree", () => {
		expect(
			isWorktreePathForProject(
				"/Users/riordan/prog/azedarach-az-1e6cc1/ts-opentui",
				"/Users/riordan/prog/azedarach",
				path,
			),
		).toBe(true)
	})

	it("does not match the primary project path", () => {
		expect(
			isWorktreePathForProject(
				"/Users/riordan/prog/azedarach/ts-opentui",
				"/Users/riordan/prog/azedarach",
				path,
			),
		).toBe(false)
	})

	it("does not match unrelated siblings", () => {
		expect(
			isWorktreePathForProject(
				"/Users/riordan/prog/another-az-1e6cc1/ts-opentui",
				"/Users/riordan/prog/azedarach",
				path,
			),
		).toBe(false)
	})
})

describe("resolveConfigBasePath", () => {
	it("uses worktree path when cwd is a worktree and has config", () => {
		expect(
			resolveConfigBasePath({
				cwdPath: "/Users/riordan/prog/azedarach-az-1e6cc1",
				projectPath: "/Users/riordan/prog/azedarach",
				pathOps: path,
				cwdHasConfig: true,
			}),
		).toBe("/Users/riordan/prog/azedarach-az-1e6cc1")
	})

	it("uses project path when cwd is a worktree but has no config", () => {
		expect(
			resolveConfigBasePath({
				cwdPath: "/Users/riordan/prog/azedarach-az-1e6cc1",
				projectPath: "/Users/riordan/prog/azedarach",
				pathOps: path,
				cwdHasConfig: false,
			}),
		).toBe("/Users/riordan/prog/azedarach")
	})
})
