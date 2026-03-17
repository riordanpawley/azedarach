import { describe, expect, it } from "bun:test"
import { mkdirSync, mkdtempSync, rmSync, writeFileSync } from "node:fs"
import { tmpdir } from "node:os"
import path from "node:path"
import { BunContext } from "@effect/platform-bun"
import { Effect, Layer } from "effect"
import {
	isWorktreeGitdirPointerForProject,
	isWorktreePathForProject,
	ProjectService,
	resolveConfigBasePath,
	resolveRegisteredProjectRootForWorktree,
} from "./ProjectService.js"

const canonicalTestPath = (value: string): string => path.normalize(value).replace(/^\/private/, "")

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
	it("uses project path when cwd is a worktree and has config", () => {
		expect(
			resolveConfigBasePath({
				cwdPath: "/Users/riordan/prog/azedarach-az-1e6cc1",
				projectPath: "/Users/riordan/prog/azedarach",
				pathOps: path,
				cwdHasConfig: true,
			}),
		).toBe("/Users/riordan/prog/azedarach")
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

describe("resolveRegisteredProjectRootForWorktree", () => {
	it("returns project root for sibling worktree paths", () => {
		expect(
			resolveRegisteredProjectRootForWorktree({
				cwdPath: "/Users/riordan/prog/azedarach-az-1e6cc1",
				projectPath: "/Users/riordan/prog/azedarach",
				pathOps: path,
				isTrackedGitWorktree: false,
			}),
		).toBe("/Users/riordan/prog/azedarach")
	})

	it("returns project root for tracked git worktrees", () => {
		expect(
			resolveRegisteredProjectRootForWorktree({
				cwdPath: "/any/path",
				projectPath: "/Users/riordan/prog/azedarach",
				pathOps: path,
				isTrackedGitWorktree: true,
			}),
		).toBe("/Users/riordan/prog/azedarach")
	})
})

describe("isWorktreeGitdirPointerForProject", () => {
	it("matches git worktree pointers under the project's .git/worktrees directory", () => {
		expect(
			isWorktreeGitdirPointerForProject(
				"/Users/riordan/prog/azedarach/.git/worktrees/az-1e6cc1",
				"/Users/riordan/prog/azedarach",
				path,
			),
		).toBe(true)
	})

	it("does not match sibling clone pointers", () => {
		expect(
			isWorktreeGitdirPointerForProject(
				"/Users/riordan/prog/azedarach-mv/.git",
				"/Users/riordan/prog/azedarach",
				path,
			),
		).toBe(false)
	})
})

describe("ProjectService initial project selection", () => {
	const withTempHomeAndCwd = async <A>(
		options: {
			readonly cwdPath: string
			readonly homePath: string
		},
		effect: Effect.Effect<A, never, ProjectService>,
	): Promise<A> => {
		const previousCwd = process.cwd()
		const previousHome = process.env.HOME
		process.chdir(options.cwdPath)
		process.env.HOME = options.homePath
		try {
			return await Effect.runPromise(
				effect.pipe(Effect.provide(Layer.provide(ProjectService.Default, BunContext.layer))),
			)
		} finally {
			process.chdir(previousCwd)
			if (previousHome === undefined) {
				delete process.env.HOME
			} else {
				process.env.HOME = previousHome
			}
		}
	}

	const writeProjectsConfig = (
		homePath: string,
		projects: ReadonlyArray<{ readonly name: string; readonly path: string }>,
		defaultProject?: string,
	) => {
		const configDir = path.join(homePath, ".config", "azedarach")
		mkdirSync(configDir, { recursive: true })
		writeFileSync(
			path.join(configDir, "projects.json"),
			JSON.stringify({ projects, defaultProject }, null, 2),
			"utf8",
		)
	}

	it("prefers cwd when it looks like a standalone azedarach workspace", async () => {
		const tempRoot = mkdtempSync(path.join(tmpdir(), "az-project-service-cwd-"))
		const registeredProject = path.join(tempRoot, "azedarach")
		const cwdClone = path.join(tempRoot, "azedarach-mv")
		const homePath = path.join(tempRoot, "home")
		mkdirSync(registeredProject, { recursive: true })
		mkdirSync(path.join(cwdClone, ".azedarach"), { recursive: true })
		mkdirSync(homePath, { recursive: true })
		writeProjectsConfig(homePath, [{ name: "azedarach", path: registeredProject }], "azedarach")

		try {
			const selectedPath = await withTempHomeAndCwd(
				{ cwdPath: cwdClone, homePath },
				Effect.gen(function* () {
					const projectService = yield* ProjectService
					return yield* projectService.getCurrentPath()
				}),
			)
			expect(canonicalTestPath(selectedPath ?? "")).toBe(canonicalTestPath(cwdClone))
		} finally {
			rmSync(tempRoot, { recursive: true, force: true })
		}
	})

	it("resolves the nearest ancestor workspace when launched from a nested subdirectory", async () => {
		const tempRoot = mkdtempSync(path.join(tmpdir(), "az-project-service-nested-"))
		const registeredProject = path.join(tempRoot, "azedarach")
		const workspaceRoot = path.join(tempRoot, "azedarach-mv")
		const nestedCwd = path.join(workspaceRoot, "ts-opentui")
		const homePath = path.join(tempRoot, "home")
		mkdirSync(registeredProject, { recursive: true })
		mkdirSync(path.join(workspaceRoot, ".azedarach"), { recursive: true })
		mkdirSync(nestedCwd, { recursive: true })
		mkdirSync(homePath, { recursive: true })
		writeProjectsConfig(homePath, [{ name: "azedarach", path: registeredProject }], "azedarach")

		try {
			const selectedPath = await withTempHomeAndCwd(
				{ cwdPath: nestedCwd, homePath },
				Effect.gen(function* () {
					const projectService = yield* ProjectService
					return yield* projectService.getCurrentPath()
				}),
			)
			expect(canonicalTestPath(selectedPath ?? "")).toBe(canonicalTestPath(workspaceRoot))
		} finally {
			rmSync(tempRoot, { recursive: true, force: true })
		}
	})

	it("prefers registered project root over local workspace root when cwd is a tracked worktree", async () => {
		const tempRoot = mkdtempSync(path.join(tmpdir(), "az-project-service-precedence-"))
		const registeredProject = path.join(tempRoot, "azedarach")
		const workspaceRoot = path.join(tempRoot, "azedarach-mv")
		const homePath = path.join(tempRoot, "home")
		mkdirSync(registeredProject, { recursive: true })
		mkdirSync(path.join(workspaceRoot, ".azedarach"), { recursive: true })
		mkdirSync(homePath, { recursive: true })
		writeProjectsConfig(homePath, [{ name: "azedarach", path: registeredProject }], "azedarach")
		writeFileSync(
			path.join(workspaceRoot, ".git"),
			`gitdir: ${path.join(registeredProject, ".git", "worktrees", "azedarach-mv")}`,
			"utf8",
		)

		try {
			const selectedPath = await withTempHomeAndCwd(
				{ cwdPath: workspaceRoot, homePath },
				Effect.gen(function* () {
					const projectService = yield* ProjectService
					return yield* projectService.getCurrentPath()
				}),
			)
			expect(canonicalTestPath(selectedPath ?? "")).toBe(canonicalTestPath(registeredProject))
		} finally {
			rmSync(tempRoot, { recursive: true, force: true })
		}
	})

	it("falls back to configured default when cwd has no local azedarach markers", async () => {
		const tempRoot = mkdtempSync(path.join(tmpdir(), "az-project-service-default-"))
		const registeredProject = path.join(tempRoot, "azedarach")
		const plainCwd = path.join(tempRoot, "scratch")
		const homePath = path.join(tempRoot, "home")
		mkdirSync(registeredProject, { recursive: true })
		mkdirSync(plainCwd, { recursive: true })
		mkdirSync(homePath, { recursive: true })
		writeProjectsConfig(homePath, [{ name: "azedarach", path: registeredProject }], "azedarach")

		try {
			const selectedPath = await withTempHomeAndCwd(
				{ cwdPath: plainCwd, homePath },
				Effect.gen(function* () {
					const projectService = yield* ProjectService
					return yield* projectService.getCurrentPath()
				}),
			)
			expect(selectedPath).toBe(registeredProject)
		} finally {
			rmSync(tempRoot, { recursive: true, force: true })
		}
	})
})
