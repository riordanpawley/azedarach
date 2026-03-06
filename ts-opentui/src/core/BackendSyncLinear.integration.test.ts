import { BunContext } from "@effect/platform-bun"
import { describe, expect, it } from "bun:test"
import { Effect, Layer } from "effect"
import type { CommandExecutor } from "@effect/platform"
import { mkdtemp, rm, writeFile } from "node:fs/promises"
import { tmpdir } from "node:os"
import { join } from "node:path"
import { AppConfigConfig } from "../config/AppConfig.js"
import { IssueTrackerClient } from "./IssueTrackerClient.js"

const RUN_LINEAR_BACKEND_INTEGRATION =
	process.env.AZEDARACH_RUN_LINEAR_SYNC_BACKEND_INTEGRATION === "1"
const LINEAR_BACKEND_IT_TIMEOUT_MS = 180_000
const maybeIt = RUN_LINEAR_BACKEND_INTEGRATION ? it : it.skip

const requireLinearApiKey = (): void => {
	const apiKey = process.env.LINEAR_API_KEY?.trim()
	if (!apiKey) {
		throw new Error(
			"LINEAR_API_KEY must be set when AZEDARACH_RUN_LINEAR_SYNC_BACKEND_INTEGRATION=1",
		)
	}
}

const resolveLinearTeam = (): string => {
	const configuredTeam = process.env.AZEDARACH_LINEAR_TEST_TEAM?.trim()
	return configuredTeam && configuredTeam.length > 0 ? configuredTeam : "AZE"
}

const buildIntegrationConfig = (team: string): string =>
	`${JSON.stringify(
		{
			$schema: 4,
			issueTracker: {
				linear: {
					syncEnabled: true,
					team,
				},
			},
		},
		null,
		2,
	)}\n`

const runWithIssueClient = <A, E>(params: {
	readonly configPath: string
	readonly projectPath: string
	readonly program: Effect.Effect<A, E, IssueTrackerClient | CommandExecutor.CommandExecutor>
}): Promise<A> =>
	Effect.runPromise(
		Effect.scoped(
			params.program.pipe(
				Effect.provide(
					Layer.succeed(
						AppConfigConfig,
						AppConfigConfig.make({
							configPath: params.configPath,
							projectPath: params.projectPath,
						}),
					),
				),
				Effect.provide(IssueTrackerClient.Default),
				Effect.provide(BunContext.layer),
			),
		),
	)

describe("BackendSyncLinear integration", () => {
	maybeIt(
		"flushes linear create/close queue entries through IssueTrackerClient.sync",
		async () => {
			requireLinearApiKey()
			const team = resolveLinearTeam()
			const tempProjectPath = await mkdtemp(join(tmpdir(), "az-linear-backend-sync-it-"))
			const configPath = join(tempProjectPath, ".azedarach.integration.json")
			await writeFile(configPath, buildIntegrationConfig(team), "utf8")

			const runId = `${Date.now()}-${Math.random().toString(36).slice(2, 8)}`
			let createdIssueId: string | undefined

			try {
				await runWithIssueClient({
					configPath,
					projectPath: tempProjectPath,
					program: Effect.gen(function* () {
						const issueClient = yield* IssueTrackerClient

						const createdIssue = yield* issueClient.create({
							title: `[integration] linear backend sync ${runId}`,
							type: "task",
							labels: ["integration-test", "linear-sync-backend"],
							cwd: tempProjectPath,
						})
						createdIssueId = createdIssue.id

						const initialSync = yield* issueClient.sync(tempProjectPath)
						expect(initialSync.pushed).toBeGreaterThanOrEqual(1)
						expect(initialSync.pulled).toBe(0)

						yield* issueClient.close(createdIssue.id, "integration cleanup", tempProjectPath)

						const closeSync = yield* issueClient.sync(tempProjectPath)
						expect(closeSync.pushed).toBeGreaterThanOrEqual(1)
						expect(closeSync.pulled).toBe(0)

						const idleSync = yield* issueClient.sync(tempProjectPath)
						expect(idleSync).toEqual({ pushed: 0, pulled: 0 })
					}),
				})
			} finally {
				if (createdIssueId !== undefined) {
					const issueIdForCleanup = createdIssueId
					await runWithIssueClient({
						configPath,
						projectPath: tempProjectPath,
						program: Effect.gen(function* () {
							const issueClient = yield* IssueTrackerClient
							yield* issueClient
								.close(issueIdForCleanup, "integration cleanup", tempProjectPath)
								.pipe(Effect.catchAll(() => Effect.void))
							yield* issueClient
								.sync(tempProjectPath)
								.pipe(Effect.catchAll(() => Effect.succeed({ pushed: 0, pulled: 0 })))
						}),
					}).catch(() => undefined)
				}

				await rm(tempProjectPath, { recursive: true, force: true })
			}
		},
		LINEAR_BACKEND_IT_TIMEOUT_MS,
	)
})
