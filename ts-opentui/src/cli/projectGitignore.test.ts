import { describe, expect, it } from "bun:test"
import { Effect } from "effect"
import { AZEDARACH_GITIGNORE_CONTENT, ensureProjectAzedarachGitignore } from "./projectGitignore.js"

interface TestFileSystemState {
	readonly existingPaths: Set<string>
	readonly createdDirectories: string[]
	readonly writtenFiles: Array<{ readonly path: string; readonly data: string }>
}

const createTestFs = (state: TestFileSystemState) => ({
	exists: (path: string) => Effect.succeed(state.existingPaths.has(path)),
	makeDirectory: (path: string) =>
		Effect.sync(() => {
			state.createdDirectories.push(path)
			state.existingPaths.add(path)
		}),
	writeFileString: (path: string, data: string) =>
		Effect.sync(() => {
			state.writtenFiles.push({ path, data })
			state.existingPaths.add(path)
		}),
})

const testPathService = {
	join: (...parts: readonly string[]) => parts.join("/"),
}

describe("ensureProjectAzedarachGitignore", () => {
	it("creates .azedarach/.gitignore for newly added projects", async () => {
		const state: TestFileSystemState = {
			existingPaths: new Set<string>(),
			createdDirectories: [],
			writtenFiles: [],
		}

		await Effect.runPromise(
			ensureProjectAzedarachGitignore({
				projectPath: "/repo/project",
				pathService: testPathService,
				fs: createTestFs(state),
				verbose: false,
			}),
		)

		expect(state.createdDirectories).toEqual(["/repo/project/.azedarach"])
		expect(state.writtenFiles).toEqual([
			{
				path: "/repo/project/.azedarach/.gitignore",
				data: AZEDARACH_GITIGNORE_CONTENT,
			},
		])
	})

	it("does not overwrite existing .azedarach/.gitignore", async () => {
		const state: TestFileSystemState = {
			existingPaths: new Set<string>([
				"/repo/project/.azedarach",
				"/repo/project/.azedarach/.gitignore",
			]),
			createdDirectories: [],
			writtenFiles: [],
		}

		await Effect.runPromise(
			ensureProjectAzedarachGitignore({
				projectPath: "/repo/project",
				pathService: testPathService,
				fs: createTestFs(state),
				verbose: false,
			}),
		)

		expect(state.createdDirectories).toEqual([])
		expect(state.writtenFiles).toEqual([])
	})
})
