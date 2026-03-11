import { describe, expect, it } from "bun:test"
import { createHmac } from "node:crypto"
import { mkdtemp, rm, writeFile } from "node:fs/promises"
import { tmpdir } from "node:os"
import { join } from "node:path"
import type { CommandExecutor } from "@effect/platform"
import { BunContext } from "@effect/platform-bun"
import { LINEAR_WEBHOOK_SIGNATURE_HEADER, LINEAR_WEBHOOK_TS_HEADER } from "@linear/sdk/webhooks"
import { Effect, Layer, Option, Stream, SubscriptionRef } from "effect"
import { AppConfigConfig } from "../config/AppConfig.js"
import { LinearWebhookService } from "./LinearWebhookService.js"

const RUN_LINEAR_WEBHOOK_INTEGRATION = process.env.AZEDARACH_RUN_LINEAR_WEBHOOK_INTEGRATION === "1"
const LINEAR_WEBHOOK_IT_TIMEOUT_MS = 120_000
const maybeIt = RUN_LINEAR_WEBHOOK_INTEGRATION ? it : it.skip

const requireLinearApiKey = (): void => {
	const apiKey = process.env.LINEAR_API_KEY?.trim()
	if (!apiKey) {
		throw new Error("LINEAR_API_KEY must be set when AZEDARACH_RUN_LINEAR_WEBHOOK_INTEGRATION=1")
	}
}

const resolveLinearTeam = (): string => {
	const configuredTeam = process.env.AZEDARACH_LINEAR_TEST_TEAM?.trim()
	return configuredTeam && configuredTeam.length > 0 ? configuredTeam : "AZE"
}

const resolveWebhookPublicBaseUrl = (): string => {
	const configuredUrl = process.env.AZEDARACH_LINEAR_TEST_WEBHOOK_PUBLIC_URL?.trim()
	return configuredUrl && configuredUrl.length > 0 ? configuredUrl : "https://example.com"
}

const resolveWebhookPort = (): number => 20000 + Math.floor(Math.random() * 20000)

const buildIntegrationConfig = (params: {
	readonly team: string
	readonly webhookPort: number
	readonly webhookPublicBaseUrl: string
	readonly webhookSecret: string
}): string =>
	`${JSON.stringify(
		{
			$schema: 4,
			issueTracker: {
				linear: {
					syncEnabled: true,
					team: params.team,
					webhooks: {
						enabled: true,
						transport: "sdk",
						url: params.webhookPublicBaseUrl,
						port: params.webhookPort,
						events: ["Issue"],
						secret: params.webhookSecret,
					},
				},
			},
		},
		null,
		2,
	)}\n`

const runWithLinearWebhookService = <A, E>(params: {
	readonly configPath: string
	readonly projectPath: string
	readonly program: Effect.Effect<A, E, LinearWebhookService | CommandExecutor.CommandExecutor>
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
				Effect.provide(LinearWebhookService.Default),
				Effect.provide(BunContext.layer),
			),
		),
	)

describe("LinearWebhookService integration", () => {
	maybeIt(
		"starts SDK webhook runtime and emits signed issue events",
		async () => {
			requireLinearApiKey()
			const team = resolveLinearTeam()
			const webhookPublicBaseUrl = resolveWebhookPublicBaseUrl()
			const webhookPort = resolveWebhookPort()
			const webhookSecret = `lin_wh_integration_${Date.now()}`

			const tempProjectPath = await mkdtemp(join(tmpdir(), "az-linear-webhook-it-"))
			const configPath = join(tempProjectPath, ".azedarach.integration.json")
			await writeFile(
				configPath,
				buildIntegrationConfig({
					team,
					webhookPort,
					webhookPublicBaseUrl,
					webhookSecret,
				}),
				"utf8",
			)

			try {
				await runWithLinearWebhookService({
					configPath,
					projectPath: tempProjectPath,
					program: Effect.gen(function* () {
						const webhookService = yield* LinearWebhookService
						const status = yield* SubscriptionRef.get(webhookService.status)
						expect(status.mode).toBe("sdk")
						expect(status.healthy).toBe(true)
						expect(status.reason).toBeUndefined()

						const payload = {
							type: "Issue",
							action: "create",
							data: {
								id: "54a3d2f1-9d55-4e7c-8c24-0fe8db5dadf9",
								identifier: "AZE-IT",
								title: "Integration webhook event",
								description: "integration test payload",
								priority: 2,
								createdAt: "2026-03-07T00:00:00.000Z",
								updatedAt: "2026-03-07T00:00:00.000Z",
								completedAt: null,
								canceledAt: null,
								parentId: null,
								teamId: "team-123",
								state: {
									id: "state-1",
									name: "Backlog",
									type: "unstarted",
								},
								labels: [],
								url: "https://linear.app/example/issue/AZE-IT",
							},
						}
						const rawPayload = JSON.stringify(payload)
						const signature = createHmac("sha256", webhookSecret).update(rawPayload).digest("hex")

						const response = yield* Effect.tryPromise(() =>
							fetch(`http://127.0.0.1:${webhookPort}/linear/webhook`, {
								method: "POST",
								headers: {
									"content-type": "application/json",
									[LINEAR_WEBHOOK_SIGNATURE_HEADER]: signature,
									[LINEAR_WEBHOOK_TS_HEADER]: String(Date.now()),
								},
								body: rawPayload,
							}),
						)

						expect(response.status).toBe(200)

						const nextEvent = yield* Stream.runHead(webhookService.issueEvents).pipe(
							Effect.timeoutFail({
								duration: "2 seconds",
								onTimeout: () => new Error("Timed out waiting for webhook issue event"),
							}),
						)
						expect(Option.isSome(nextEvent)).toBe(true)
						if (Option.isSome(nextEvent)) {
							expect(nextEvent.value.payload.data.identifier).toBe("AZE-IT")
						}
					}),
				})
			} finally {
				await rm(tempProjectPath, { recursive: true, force: true })
			}
		},
		LINEAR_WEBHOOK_IT_TIMEOUT_MS,
	)
})
