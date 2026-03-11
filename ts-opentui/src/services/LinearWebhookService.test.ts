import { describe, expect, it } from "bun:test"
import * as crypto from "node:crypto"
import { mkdir, mkdtemp, rm, writeFile } from "node:fs/promises"
import { tmpdir } from "node:os"
import { join } from "node:path"
import type { CommandExecutor } from "@effect/platform"
import { BunContext } from "@effect/platform-bun"
import { LinearWebhookClient } from "@linear/sdk/webhooks"
import { Effect, Layer, Option, SubscriptionRef } from "effect"
import { AppConfig, AppConfigConfig } from "../config/AppConfig.js"
import {
	buildWebhookRuntimeConfigKey,
	decodeLinearIssueWebhookEvent,
	LinearWebhookService,
	normalizePublicBaseUrl,
	parseTailscaleDnsName,
} from "./LinearWebhookService.js"

const issueWebhookPayload = {
	type: "Issue",
	action: "create",
	data: {
		id: "7d9f958e-3700-4fdf-b82c-9e9b86276d40",
		identifier: "AZE-123",
		title: "Webhook test issue",
		description: "Test payload",
		priority: 2,
		createdAt: "2026-03-05T00:00:00.000Z",
		updatedAt: "2026-03-05T00:00:00.000Z",
		completedAt: null,
		canceledAt: null,
		parentId: null,
		teamId: "team-123",
		state: {
			id: "state-1",
			name: "Backlog",
			type: "unstarted",
		},
		labels: [
			{
				id: "label-1",
				name: "type:feature",
			},
		],
		url: "https://linear.app/example/issue/AZE-123",
	},
}

const cliTransportConfig = `${JSON.stringify(
	{
		$schema: 4,
		issueTracker: {
			linear: {
				syncEnabled: true,
				webhooks: {
					enabled: true,
					transport: "cli",
				},
			},
		},
	},
	null,
	2,
)}\n`

const buildLinearWebhookServiceTestLayer = (params: {
	readonly projectPath: string
	readonly configPath: string
}) => {
	const configOverrideLayer = Layer.succeed(
		AppConfigConfig,
		AppConfigConfig.make({
			projectPath: params.projectPath,
			configPath: params.configPath,
		}),
	)
	const baseLayer = Layer.merge(BunContext.layer, configOverrideLayer)
	const appConfigLayer = Layer.provide(AppConfig.Default, baseLayer)
	const webhookLayer = Layer.provide(
		LinearWebhookService.Default,
		Layer.merge(baseLayer, appConfigLayer),
	)
	return Layer.merge(baseLayer, Layer.merge(appConfigLayer, webhookLayer))
}

const runWithLinearWebhookService = <A, E>(params: {
	readonly projectPath: string
	readonly configPath: string
	readonly program: Effect.Effect<
		A,
		E,
		AppConfig | LinearWebhookService | CommandExecutor.CommandExecutor
	>
}): Promise<A> =>
	Effect.runPromise(
		Effect.scoped(
			params.program.pipe(
				Effect.provide(
					buildLinearWebhookServiceTestLayer({
						projectPath: params.projectPath,
						configPath: params.configPath,
					}),
				),
			),
		),
	)

describe("decodeLinearIssueWebhookEvent", () => {
	it("decodes Issue payloads", () => {
		const decoded = decodeLinearIssueWebhookEvent(issueWebhookPayload)
		expect(Option.isSome(decoded)).toBe(true)
		if (Option.isSome(decoded)) {
			expect(decoded.value.data.identifier).toBe("AZE-123")
		}
	})

	it("decodes Issue payloads with nested parent references", () => {
		const decoded = decodeLinearIssueWebhookEvent({
			...issueWebhookPayload,
			data: {
				...issueWebhookPayload.data,
				parentId: "parent-entity-id",
				parent: {
					id: "parent-entity-id",
					identifier: "AZE-42",
				},
			},
		})
		expect(Option.isSome(decoded)).toBe(true)
		if (Option.isSome(decoded)) {
			expect(decoded.value.data.parent?.identifier).toBe("AZE-42")
		}
	})

	it("filters non-Issue payloads", () => {
		const decoded = decodeLinearIssueWebhookEvent({
			...issueWebhookPayload,
			type: "Comment",
		})
		expect(Option.isNone(decoded)).toBe(true)
	})
})

describe("Linear webhook signature verification", () => {
	it("verifies a valid signature and decodes the issue event", () => {
		const secret = "lin_wh_secret_test"
		const webhookClient = new LinearWebhookClient(secret)
		const rawBody = Buffer.from(JSON.stringify(issueWebhookPayload))
		const timestamp = String(Date.now())
		const signature = crypto.createHmac("sha256", secret).update(rawBody).digest("hex")

		const parsedPayload = webhookClient.parseData(rawBody, signature, timestamp)
		const decoded = decodeLinearIssueWebhookEvent(parsedPayload)

		expect(Option.isSome(decoded)).toBe(true)
	})

	it("rejects invalid signatures", () => {
		const secret = "lin_wh_secret_test"
		const webhookClient = new LinearWebhookClient(secret)
		const rawBody = Buffer.from(JSON.stringify(issueWebhookPayload))
		const timestamp = String(Date.now())

		expect(() => webhookClient.parseData(rawBody, "deadbeef", timestamp)).toThrow(
			"Invalid webhook signature",
		)
	})
})

describe("Linear webhook URL resolution helpers", () => {
	it("normalizes configured public base URLs", () => {
		expect(normalizePublicBaseUrl("  https://demo.ngrok.app/// ")).toBe("https://demo.ngrok.app")
		expect(normalizePublicBaseUrl("   ")).toBeUndefined()
		expect(normalizePublicBaseUrl(undefined)).toBeUndefined()
	})

	it("parses tailscale dns names from status output", () => {
		const parsed = parseTailscaleDnsName(
			JSON.stringify({
				Self: {
					DNSName: "my-host.example.ts.net.",
				},
			}),
		)
		expect(Option.isSome(parsed)).toBe(true)
		if (Option.isSome(parsed)) {
			expect(parsed.value).toBe("my-host.example.ts.net")
		}
	})

	it("returns none for malformed tailscale status output", () => {
		expect(Option.isNone(parseTailscaleDnsName("{"))).toBe(true)
		expect(
			Option.isNone(
				parseTailscaleDnsName(
					JSON.stringify({
						Self: {},
					}),
				),
			),
		).toBe(true)
	})
})

describe("LinearWebhookService reconfiguration", () => {
	it("includes project path in the runtime config key", async () => {
		const projectPath = await mkdtemp(join(tmpdir(), "az-linear-webhook-config-key-"))
		const configDir = join(projectPath, ".azedarach")
		const configPath = join(configDir, "config.json")

		try {
			await mkdir(configDir, { recursive: true })
			await writeFile(configPath, cliTransportConfig, "utf8")

			await runWithLinearWebhookService({
				projectPath,
				configPath,
				program: Effect.gen(function* () {
					const appConfig = yield* AppConfig
					const config = yield* SubscriptionRef.get(appConfig.config)
					const keyA = buildWebhookRuntimeConfigKey({
						config,
						projectPath,
					})
					const keyB = buildWebhookRuntimeConfigKey({
						config,
						projectPath: `${projectPath}-other`,
					})

					expect(keyA).not.toBe(keyB)
					expect(keyA).toContain(`projectPath=${projectPath}`)
				}),
			})
		} finally {
			await rm(projectPath, { recursive: true, force: true })
		}
	})

	it("reloads from default local mode into configured linear webhook mode", async () => {
		const projectPath = await mkdtemp(join(tmpdir(), "az-linear-webhook-reload-"))
		const configDir = join(projectPath, ".azedarach")
		const configPath = join(configDir, "config.json")

		try {
			await runWithLinearWebhookService({
				projectPath,
				configPath,
				program: Effect.gen(function* () {
					const appConfig = yield* AppConfig
					const webhookService = yield* LinearWebhookService
					const initialStatus = yield* SubscriptionRef.get(webhookService.status)
					expect(initialStatus.mode).toBe("disabled")
					expect(initialStatus.reason).toBe("Linear backend not active")
					expect(initialStatus.configKey).not.toBeNull()

					yield* Effect.tryPromise(() => mkdir(configDir, { recursive: true }))
					yield* Effect.tryPromise(() => writeFile(configPath, cliTransportConfig, "utf8"))

					yield* appConfig.reload()
					yield* webhookService.reconfigure()

					const reconfiguredStatus = yield* SubscriptionRef.get(webhookService.status)
					expect(reconfiguredStatus.mode).toBe("cli")
					expect(reconfiguredStatus.healthy).toBe(false)
					expect(reconfiguredStatus.reason).toBe("CLI webhook transport selected")
					expect(reconfiguredStatus.configKey).not.toBeNull()
				}),
			})
		} finally {
			await rm(projectPath, { recursive: true, force: true })
		}
	})
})
