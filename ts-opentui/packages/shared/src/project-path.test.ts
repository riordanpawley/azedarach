import { describe, expect, it } from "bun:test"
import { BunContext, BunFileSystem, BunPath } from "@effect/platform-bun"
import { Effect, Layer, Option } from "effect"
import { mkdirSync, rmSync, writeFileSync } from "fs"
import { join } from "path"
import { resolveBaseProjectPath, resolveProjectBasePath } from "./project-path.js"

const makeTempRoot = (): string => join(process.cwd(), `.tmp-project-path-${crypto.randomUUID()}`)
const platformLayer = Layer.mergeAll(BunContext.layer, BunFileSystem.layer, BunPath.layer)

describe("project path helpers", () => {
	it("resolves the repo root from a nested worktree .git gitdir marker", async () => {
		const tempRoot = makeTempRoot()
		const repoRoot = join(tempRoot, "repo")
		const worktreeRoot = join(tempRoot, "worktree")
		const gitDirPath = join(repoRoot, ".git", "worktrees", "feature")

		try {
			mkdirSync(join(gitDirPath, "objects"), { recursive: true })
			mkdirSync(join(worktreeRoot, "nested"), { recursive: true })
			writeFileSync(join(worktreeRoot, ".git"), `gitdir: ${gitDirPath}\n`)

			const resolved = await Effect.runPromise(
				resolveBaseProjectPath(join(worktreeRoot, "nested")).pipe(Effect.provide(platformLayer)),
			)

			expect(resolved).toBe(repoRoot)
		} finally {
			rmSync(tempRoot, { recursive: true, force: true })
		}
	})

	it("resolves a project base path from an optional project directory input", async () => {
		const tempRoot = makeTempRoot()
		const repoRoot = join(tempRoot, "repo")
		const childPath = join(repoRoot, "subdir")
		const gitDirPath = join(repoRoot, ".git")

		try {
			mkdirSync(childPath, { recursive: true })
			writeFileSync(gitDirPath, `gitdir: ${gitDirPath}\n`)

			const resolved = await Effect.runPromise(
				resolveProjectBasePath(Option.some(childPath)).pipe(Effect.provide(platformLayer)),
			)

			expect(resolved).toBe(repoRoot)
		} finally {
			rmSync(tempRoot, { recursive: true, force: true })
		}
	})
})
