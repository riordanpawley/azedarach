import { describe, expect, it } from "bun:test"
import { mkdirSync, mkdtempSync, rmSync, writeFileSync } from "node:fs"
import { tmpdir } from "node:os"
import { join } from "node:path"
import { BunContext } from "@effect/platform-bun"
import { Effect, Layer, Stream } from "effect"
import {
	AppConfig,
	AppConfigConfig,
	AppConfigProjectContext,
	type AppConfigProjectContextApi,
} from "./AppConfig.js"

const withTempWorkspace = async <A>(run: (workspacePath: string) => Promise<A>): Promise<A> => {
	const workspacePath = mkdtempSync(join(tmpdir(), "azedarach-app-config-"))
	const previousCwd = process.cwd()
	try {
		process.chdir(workspacePath)
		return await run(workspacePath)
	} finally {
		process.chdir(previousCwd)
		rmSync(workspacePath, { force: true, recursive: true })
	}
}

const writeConfig = (projectPath: string, config: unknown): void => {
	mkdirSync(join(projectPath, ".azedarach"), { recursive: true })
	writeFileSync(
		join(projectPath, ".azedarach", "config.json"),
		`${JSON.stringify(config, null, 2)}\n`,
	)
}

describe("AppConfig", () => {
	it("loads explicit project-path issue tracker config without daemon-cwd fallback", async () => {
		await withTempWorkspace(async (workspacePath) => {
			const daemonCwdProject = join(workspacePath, "daemon-cwd")
			const targetProject = join(workspacePath, "target")
			mkdirSync(daemonCwdProject, { recursive: true })
			mkdirSync(targetProject, { recursive: true })

			writeConfig(daemonCwdProject, {
				issueTracker: {
					local: {
						syncEnabled: false,
					},
				},
			})
			writeConfig(targetProject, {
				issueTracker: {
					tracker: {
						syncEnabled: true,
					},
				},
			})

			process.chdir(daemonCwdProject)

			const projectContext: AppConfigProjectContextApi = {
				getCurrentPath: () => Effect.succeed(undefined),
				currentProjectPathChanges: Stream.empty,
			}

			const layer = AppConfig.Default.pipe(
				Layer.provide(
					Layer.mergeAll(
						BunContext.layer,
						Layer.succeed(AppConfigProjectContext, projectContext),
						Layer.succeed(
							AppConfigConfig,
							AppConfigConfig.make({
								projectPath: daemonCwdProject,
								configPath: null,
							}),
						),
					),
				),
			)

			const result = await Effect.runPromise(
				Effect.scoped(
					Effect.gen(function* () {
						const service = yield* AppConfig
						const currentProjectRuntime = yield* service.getIssueTrackerSyncConfig()
						const explicitProjectRuntime =
							yield* service.getIssueTrackerSyncConfigForProjectPath(targetProject)
						return { currentProjectRuntime, explicitProjectRuntime }
					}).pipe(Effect.provide(layer)),
				),
			)

			expect("local" in result.currentProjectRuntime.issueTracker).toBe(true)
			expect("tracker" in result.explicitProjectRuntime.issueTracker).toBe(true)
		})
	})
})
