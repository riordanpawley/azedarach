import { describe, expect, it } from "bun:test"
import { mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs"
import { tmpdir } from "node:os"
import { join } from "node:path"
import { BunContext } from "@effect/platform-bun"
import { Effect, Layer, Schema, SubscriptionRef } from "effect"
import { TuiProjectContextService } from "./TuiProjectContextService.js"

const makeLayer = TuiProjectContextService.Default.pipe(Layer.provideMerge(BunContext.layer))

const withTempWorkspace = async <A>(run: (workspacePath: string) => Promise<A>): Promise<A> => {
	const workspacePath = mkdtempSync(join(tmpdir(), "azedarach-tui-project-context-"))
	const previousCwd = process.cwd()
	try {
		process.chdir(workspacePath)
		return await run(workspacePath)
	} finally {
		process.chdir(previousCwd)
		rmSync(workspacePath, { force: true, recursive: true })
	}
}

describe("TuiProjectContextService", () => {
	it("loads projects from workspace config and selects the configured default", async () => {
		await withTempWorkspace(async (workspacePath) => {
			const alphaPath = join(workspacePath, "alpha")
			const betaPath = join(workspacePath, "beta")
			mkdirSync(alphaPath, { recursive: true })
			mkdirSync(betaPath, { recursive: true })
			mkdirSync(join(workspacePath, ".azedarach"), { recursive: true })
			writeFileSync(
				join(workspacePath, ".azedarach", "config.json"),
				JSON.stringify(
					{
						projects: [
							{ name: "alpha", path: alphaPath },
							{ name: "beta", path: betaPath },
						],
						defaultProject: "beta",
					},
					null,
					2,
				),
			)

			const result = await Effect.runPromise(
				Effect.scoped(
					Effect.gen(function* () {
						const service = yield* TuiProjectContextService
						const currentProject = yield* SubscriptionRef.get(service.currentProject)
						const projects = yield* SubscriptionRef.get(service.projects)
						return { currentProject, projects }
					}).pipe(Effect.provide(makeLayer)),
				),
			)

			expect(result.currentProject?.name).toBe("beta")
			expect(result.projects.map((project) => project.name)).toEqual(["alpha", "beta"])
		})
	})

	it("persists switched project selection back to workspace config", async () => {
		await withTempWorkspace(async (workspacePath) => {
			const alphaPath = join(workspacePath, "alpha")
			const betaPath = join(workspacePath, "beta")
			mkdirSync(alphaPath, { recursive: true })
			mkdirSync(betaPath, { recursive: true })
			mkdirSync(join(workspacePath, ".azedarach"), { recursive: true })
			const configPath = join(workspacePath, ".azedarach", "config.json")
			writeFileSync(
				configPath,
				JSON.stringify(
					{
						projects: [
							{ name: "alpha", path: alphaPath },
							{ name: "beta", path: betaPath },
						],
						defaultProject: "alpha",
					},
					null,
					2,
				),
			)

			await Effect.runPromise(
				Effect.scoped(
					Effect.gen(function* () {
						const service = yield* TuiProjectContextService
						yield* service.switchProject("beta")
					}).pipe(Effect.provide(makeLayer)),
				),
			)

			const nextConfig = Schema.decodeSync(
				Schema.Struct({
					defaultProject: Schema.optional(Schema.String),
				}),
			)(JSON.parse(readFileSync(configPath, "utf8")))
			expect(nextConfig.defaultProject).toBe("beta")
		})
	})

	it("provides getProjects and requireCurrentProject compatibility APIs", async () => {
		await withTempWorkspace(async (workspacePath) => {
			const alphaPath = join(workspacePath, "alpha")
			const betaPath = join(workspacePath, "beta")
			mkdirSync(alphaPath, { recursive: true })
			mkdirSync(betaPath, { recursive: true })
			mkdirSync(join(workspacePath, ".azedarach"), { recursive: true })
			writeFileSync(
				join(workspacePath, ".azedarach", "config.json"),
				JSON.stringify(
					{
						projects: [
							{ name: "alpha", path: alphaPath },
							{ name: "beta", path: betaPath },
						],
						defaultProject: "beta",
					},
					null,
					2,
				),
			)

			const result = await Effect.runPromise(
				Effect.scoped(
					Effect.gen(function* () {
						const service = yield* TuiProjectContextService
						const projects = yield* service.getProjects()
						const currentProject = yield* service.requireCurrentProject()
						return { projects, currentProject }
					}).pipe(Effect.provide(makeLayer)),
				),
			)

			expect(result.projects.map((project) => project.name)).toEqual(["alpha", "beta"])
			expect(result.currentProject.name).toBe("beta")
		})
	})

	it("fails requireCurrentProject when no configured projects exist", async () => {
		await withTempWorkspace(async (workspacePath) => {
			mkdirSync(join(workspacePath, ".azedarach"), { recursive: true })
			writeFileSync(join(workspacePath, ".azedarach", "config.json"), JSON.stringify({}, null, 2))

			const errorTag = await Effect.runPromise(
				Effect.scoped(
					Effect.gen(function* () {
						const service = yield* TuiProjectContextService
						return yield* service.requireCurrentProject().pipe(
							Effect.flip,
							Effect.map((error) => error._tag),
						)
					}).pipe(Effect.provide(makeLayer)),
				),
			)

			expect(errorTag).toBe("TuiProjectContextError")
		})
	})
})
