/**
 * LinearWebhookService - SDK-backed Linear webhook runtime
 *
 * Runs a local webhook listener, verifies signatures, registers a temporary
 * webhook at startup, and emits typed Issue webhook events for consumers.
 */

import {
	LINEAR_WEBHOOK_SIGNATURE_HEADER,
	LINEAR_WEBHOOK_TS_HEADER,
	LinearWebhookClient,
} from "@linear/sdk/webhooks"
import { Command } from "@effect/platform"
import { Config, Data, Effect, Option, Queue, Schema, Stream, SubscriptionRef } from "effect"
import { AppConfig } from "../config/AppConfig.js"
import { LinearSdk } from "../core/LinearSdk.js"

const WEBHOOK_PATH = "/linear/webhook"
const DEFAULT_WEBHOOK_PORT = 9000
const DEFAULT_WEBHOOK_EVENTS: readonly string[] = ["Issue"]
const WEBHOOK_PUBLIC_URL_ENV = "LINEAR_WEBHOOK_PUBLIC_URL"
const LINEAR_WEBHOOK_STARTUP_TIMEOUT_MS = 5000
const LINEAR_WEBHOOK_TEAM_DISCOVERY_TIMEOUT_MS = 3000
const LINEAR_WEBHOOK_RESOLVE_TEAM_TIMEOUT_MS = 3000
const LINEAR_WEBHOOK_REGISTER_TIMEOUT_MS = 4000
const TAILSCALE_RESOLUTION_TIMEOUT_MS = 2000

export type LinearWebhookMode = "disabled" | "cli" | "misconfigured" | "sdk" | "failed"

class LinearWebhookRuntimeError extends Data.TaggedError("LinearWebhookRuntimeError")<{
	readonly message: string
}> {}

const hasMessage = (value: unknown): value is { readonly message: unknown } =>
	typeof value === "object" && value !== null && "message" in value

const formatErrorMessage = (error: unknown): string =>
	hasMessage(error) ? String(error.message) : String(error)

const failOnTimeout = <A, E>(params: {
	readonly effect: Effect.Effect<A, E>
	readonly timeoutMs: number
	readonly timeoutMessage: string
	readonly mapError: (error: E) => LinearWebhookRuntimeError
}): Effect.Effect<A, LinearWebhookRuntimeError> =>
	params.effect.pipe(
		Effect.mapError(params.mapError),
		Effect.timeoutFail({
			duration: `${params.timeoutMs} millis`,
			onTimeout: () => new LinearWebhookRuntimeError({ message: params.timeoutMessage }),
		}),
	)

const WorkflowStateChildSchema = Schema.Struct({
	id: Schema.String,
	name: Schema.String,
	type: Schema.String,
})

const IssueLabelChildSchema = Schema.Struct({
	id: Schema.String,
	name: Schema.String,
})

const IssueParentChildSchema = Schema.Struct({
	id: Schema.String,
	identifier: Schema.String,
})

const IssueWebhookDataSchema = Schema.Struct({
	id: Schema.String,
	identifier: Schema.String,
	title: Schema.String,
	description: Schema.NullOr(Schema.String).pipe(Schema.optional),
	priority: Schema.Number,
	createdAt: Schema.String,
	updatedAt: Schema.String,
	completedAt: Schema.NullOr(Schema.String).pipe(Schema.optional),
	canceledAt: Schema.NullOr(Schema.String).pipe(Schema.optional),
	parentId: Schema.NullOr(Schema.String).pipe(Schema.optional),
	parent: Schema.NullOr(IssueParentChildSchema).pipe(Schema.optional),
	teamId: Schema.String,
	state: WorkflowStateChildSchema,
	labels: Schema.Array(IssueLabelChildSchema),
	url: Schema.String,
})

const IssueWebhookEnvelopeSchema = Schema.Struct({
	type: Schema.Literal("Issue"),
	action: Schema.String,
	data: IssueWebhookDataSchema,
})

export type LinearIssueWebhookEvent = Schema.Schema.Type<typeof IssueWebhookEnvelopeSchema>

export const decodeLinearIssueWebhookEvent = (
	payload: unknown,
): Option.Option<LinearIssueWebhookEvent> => {
	const decoded = Schema.decodeUnknownEither(IssueWebhookEnvelopeSchema)(payload)
	return decoded._tag === "Right" ? Option.some(decoded.right) : Option.none()
}

export interface LinearWebhookServiceApi {
	readonly issueEvents: Stream.Stream<LinearIssueWebhookEvent>
	readonly healthy: SubscriptionRef.SubscriptionRef<boolean>
	readonly mode: SubscriptionRef.SubscriptionRef<LinearWebhookMode>
}

const parseWebhookUrl = (publicBaseUrl: string): string => {
	const trimmed = publicBaseUrl.trim().replace(/\/+$/, "")
	return `${trimmed}${WEBHOOK_PATH}`
}

const normalizeNonEmpty = (value: string | undefined | null): string | undefined => {
	if (value === undefined || value === null) return undefined
	const trimmed = value.trim()
	return trimmed.length > 0 ? trimmed : undefined
}

export const normalizePublicBaseUrl = (value: string | undefined | null): string | undefined =>
	normalizeNonEmpty(value)?.replace(/\/+$/, "")

const normalizeWebhookEvents = (events: readonly string[] | undefined): readonly string[] => {
	const configured = events
		?.map((eventType) => eventType.trim())
		.filter((eventType) => eventType.length > 0)
	return configured !== undefined && configured.length > 0 ? configured : DEFAULT_WEBHOOK_EVENTS
}

const TailscaleStatusSchema = Schema.Struct({
	Self: Schema.optional(
		Schema.Struct({
			DNSName: Schema.optional(Schema.NullOr(Schema.String)),
		}),
	),
})

export const parseTailscaleDnsName = (statusJson: string): Option.Option<string> => {
	let parsed: unknown
	try {
		parsed = JSON.parse(statusJson)
	} catch {
		return Option.none()
	}

	const decodedStatus = Schema.decodeUnknownEither(TailscaleStatusSchema)(parsed)
	if (decodedStatus._tag === "Left") {
		return Option.none()
	}

	const dnsName = normalizeNonEmpty(decodedStatus.right.Self?.DNSName)
	if (dnsName === undefined) {
		return Option.none()
	}

	const normalizedDnsName = dnsName.replace(/\.$/, "")
	return normalizedDnsName.length > 0 ? Option.some(normalizedDnsName) : Option.none()
}

const tryResolveTailscaleFunnelPublicUrl = (port: number) =>
	Effect.gen(function* () {
		yield* Effect.logDebug(
			`LinearWebhookService: attempting tailscale public URL resolution (port=${port})`,
		)
		const tailscaleStatus = yield* Command.string(
			Command.make("tailscale", "status", "--json"),
		).pipe(
			Effect.map((output) => output.trim()),
			Effect.catchAll((error) =>
				Effect.logWarning(`Recovering after caught error: ${String(error)}`).pipe(
					Effect.zipRight(Effect.succeed(undefined)),
				),
			),
		)
		if (tailscaleStatus === undefined) {
			yield* Effect.logDebug(
				"LinearWebhookService: tailscale status unavailable, cannot derive funnel URL",
			)
			return undefined
		}

		const dnsNameOption = parseTailscaleDnsName(tailscaleStatus)
		if (Option.isNone(dnsNameOption)) {
			yield* Effect.logDebug(
				"LinearWebhookService: tailscale status has no usable DNS name",
			)
			return undefined
		}

		yield* Effect.logDebug(
			`LinearWebhookService: enabling tailscale funnel for webhook port ${port}`,
		)
		const funnelExitCode = yield* Command.exitCode(
			Command.make("tailscale", "funnel", "--bg", "--yes", String(port)),
		).pipe(
			Effect.catchAll((error) =>
				Effect.logWarning(`Recovering after caught error: ${String(error)}`).pipe(
					Effect.zipRight(Effect.succeed(1)),
				),
			),
		)
		if (funnelExitCode !== 0) {
			yield* Effect.logDebug(
				`LinearWebhookService: tailscale funnel command failed (exit=${funnelExitCode})`,
			)
			return undefined
		}

		yield* Effect.logDebug(
			`LinearWebhookService: tailscale funnel URL resolved to https://${dnsNameOption.value}`,
		)
		return `https://${dnsNameOption.value}`
	})

type ResolvedWebhookTeamSource = "config" | "auto-single-team"

interface ResolvedWebhookTeamRef {
	readonly teamRef: string
	readonly source: ResolvedWebhookTeamSource
}

type ResolvedWebhookPublicUrlSource = "config" | "env" | "tailscale-funnel"

interface ResolvedWebhookPublicUrl {
	readonly publicBaseUrl: string
	readonly source: ResolvedWebhookPublicUrlSource
}

interface ResolvedSdkWebhookRuntimeConfig {
	readonly teamRef: string
	readonly teamSource: ResolvedWebhookTeamSource
	readonly publicBaseUrl: string
	readonly publicUrlSource: ResolvedWebhookPublicUrlSource
	readonly port: number
	readonly eventTypes: readonly string[]
	readonly webhookSecret: string
	readonly webhookSecretSource: "config" | "generated"
}

export class LinearWebhookService extends Effect.Service<LinearWebhookService>()(
	"LinearWebhookService",
	{
		dependencies: [AppConfig.Default, LinearSdk.Default],
		scoped: Effect.gen(function* () {
			const appConfig = yield* AppConfig
			const linearSdk = yield* LinearSdk

			const issueEventsQueue = yield* Queue.unbounded<LinearIssueWebhookEvent>()
			const healthy = yield* SubscriptionRef.make(false)
			const mode = yield* SubscriptionRef.make<LinearWebhookMode>("disabled")

			const config = yield* SubscriptionRef.get(appConfig.config)
			if (!("linear" in config.issueTracker)) {
				return {
					issueEvents: Stream.fromQueue(issueEventsQueue),
					healthy,
					mode,
				} satisfies LinearWebhookServiceApi
			}

			const linearConfig = config.issueTracker.linear
			const webhookConfig = linearConfig.webhooks
			const transport = webhookConfig.transport
			yield* Effect.logDebug(
				`LinearWebhookService: init backend=linear enabled=${String(webhookConfig.enabled)} transport=${transport}`,
			)
			if (webhookConfig.enabled === false) {
				yield* SubscriptionRef.set(mode, "disabled")
				yield* Effect.logDebug("LinearWebhookService: disabled in config")
				return {
					issueEvents: Stream.fromQueue(issueEventsQueue),
					healthy,
					mode,
				} satisfies LinearWebhookServiceApi
			}

			if (transport === "cli") {
				yield* SubscriptionRef.set(mode, "cli")
				yield* Effect.logDebug("LinearWebhookService: CLI transport selected; SDK runtime skipped")
				return {
					issueEvents: Stream.fromQueue(issueEventsQueue),
					healthy,
					mode,
				} satisfies LinearWebhookServiceApi
			}

			const resolveWebhookTeamRef = (): Effect.Effect<
				ResolvedWebhookTeamRef,
				LinearWebhookRuntimeError
				> =>
					Effect.gen(function* () {
						const configuredTeamRef = normalizeNonEmpty(linearConfig.team)
						if (configuredTeamRef !== undefined) {
							yield* Effect.logDebug(
								`LinearWebhookService: using configured team reference ${configuredTeamRef}`,
							)
							return {
								teamRef: configuredTeamRef,
								source: "config",
							} satisfies ResolvedWebhookTeamRef
						}

						yield* Effect.logDebug(
							"LinearWebhookService: no configured team, discovering via Linear API",
						)
						const teams = yield* failOnTimeout({
							effect: linearSdk.teams({ first: 50 }),
							timeoutMs: LINEAR_WEBHOOK_TEAM_DISCOVERY_TIMEOUT_MS,
							timeoutMessage: `Timed out discovering Linear teams after ${LINEAR_WEBHOOK_TEAM_DISCOVERY_TIMEOUT_MS}ms; set issueTracker.linear.team explicitly`,
							mapError: (error) =>
								new LinearWebhookRuntimeError({
									message: `${error.message}; set issueTracker.linear.team explicitly`,
								}),
						})

					const discoveredTeamRefs = teams.nodes
						.map((team) => normalizeNonEmpty(team.key) ?? normalizeNonEmpty(team.id))
						.filter((teamRef): teamRef is string => teamRef !== undefined)

					if (discoveredTeamRefs.length === 1) {
						return {
							teamRef: discoveredTeamRefs[0],
							source: "auto-single-team",
						} satisfies ResolvedWebhookTeamRef
					}

					if (discoveredTeamRefs.length === 0) {
						return yield* Effect.fail(
							new LinearWebhookRuntimeError({
								message:
									"Linear webhook SDK mode could not discover a default team; set issueTracker.linear.team",
							}),
						)
					}

					return yield* Effect.fail(
						new LinearWebhookRuntimeError({
							message: `Linear webhook SDK mode found multiple teams (${discoveredTeamRefs.join(", ")}); set issueTracker.linear.team`,
						}),
					)
				})

			const resolveWebhookPublicUrl = (port: number) =>
				Effect.gen(function* () {
					const configuredPublicBaseUrl = normalizePublicBaseUrl(webhookConfig.url)
					if (configuredPublicBaseUrl !== undefined) {
						yield* Effect.logDebug(
							`LinearWebhookService: using configured webhook URL ${configuredPublicBaseUrl}`,
						)
						return {
							publicBaseUrl: configuredPublicBaseUrl,
							source: "config",
						} satisfies ResolvedWebhookPublicUrl
					}

					const envPublicBaseUrl = yield* Config.option(Config.string(WEBHOOK_PUBLIC_URL_ENV)).pipe(
						Effect.map((option) =>
							Option.isSome(option) ? normalizePublicBaseUrl(option.value) : undefined,
						),
						Effect.mapError(
							() =>
								new LinearWebhookRuntimeError({
									message: `Failed to read ${WEBHOOK_PUBLIC_URL_ENV}`,
								}),
						),
					)
					if (envPublicBaseUrl !== undefined) {
						yield* Effect.logDebug(
							`LinearWebhookService: using ${WEBHOOK_PUBLIC_URL_ENV}=${envPublicBaseUrl}`,
						)
						return {
							publicBaseUrl: envPublicBaseUrl,
							source: "env",
						} satisfies ResolvedWebhookPublicUrl
					}

						const tailscalePublicBaseUrlResult = yield* tryResolveTailscaleFunnelPublicUrl(port).pipe(
							Effect.timeout(`${TAILSCALE_RESOLUTION_TIMEOUT_MS} millis`),
						)
						if (tailscalePublicBaseUrlResult === undefined) {
							yield* Effect.logDebug(
								`LinearWebhookService: tailscale URL resolution timed out after ${TAILSCALE_RESOLUTION_TIMEOUT_MS}ms`,
							)
						}
						const tailscalePublicBaseUrl = tailscalePublicBaseUrlResult
					if (tailscalePublicBaseUrl !== undefined) {
						yield* Effect.logDebug(
							`LinearWebhookService: using tailscale funnel URL ${tailscalePublicBaseUrl}`,
						)
						return {
							publicBaseUrl: tailscalePublicBaseUrl,
							source: "tailscale-funnel",
						} satisfies ResolvedWebhookPublicUrl
					}

					return yield* Effect.fail(
						new LinearWebhookRuntimeError({
							message: `Linear webhook SDK mode requires a public webhook URL. Set issueTracker.linear.webhooks.url, export ${WEBHOOK_PUBLIC_URL_ENV}, or run "tailscale funnel --bg --yes ${port}"`,
						}),
					)
				})

			const runtimeConfigResult = yield* Effect.either(
				Effect.gen(function* () {
					const port =
						Number.isInteger(webhookConfig.port) && webhookConfig.port > 0
							? webhookConfig.port
							: DEFAULT_WEBHOOK_PORT
					const eventTypes = normalizeWebhookEvents(webhookConfig.events)
					const team = yield* resolveWebhookTeamRef()
					const webhookPublicUrl = yield* resolveWebhookPublicUrl(port)
					const configuredSecret = normalizeNonEmpty(webhookConfig.secret)
					const webhookSecretSource: "config" | "generated" =
						configuredSecret !== undefined ? "config" : "generated"
					const webhookSecret = configuredSecret ?? `azw_${crypto.randomUUID().replaceAll("-", "")}`

					return {
						teamRef: team.teamRef,
						teamSource: team.source,
						publicBaseUrl: webhookPublicUrl.publicBaseUrl,
						publicUrlSource: webhookPublicUrl.source,
						port,
						eventTypes,
						webhookSecret,
						webhookSecretSource,
					} satisfies ResolvedSdkWebhookRuntimeConfig
				}),
			)
			if (runtimeConfigResult._tag === "Left") {
				yield* SubscriptionRef.set(mode, "misconfigured")
				yield* Effect.logDebug(
					"LinearWebhookService: runtime config resolution failed; entering misconfigured mode",
				)
				yield* Effect.logWarning(`${runtimeConfigResult.left.message}; falling back to polling`)
				return {
					issueEvents: Stream.fromQueue(issueEventsQueue),
					healthy,
					mode,
				} satisfies LinearWebhookServiceApi
			}

			const runtimeConfig = runtimeConfigResult.right
			const webhookClient = new LinearWebhookClient(runtimeConfig.webhookSecret)

				const resolveTeamId = (reference: string): Effect.Effect<string, LinearWebhookRuntimeError> =>
					failOnTimeout({
						effect: linearSdk.resolveTeamId(reference),
						timeoutMs: LINEAR_WEBHOOK_RESOLVE_TEAM_TIMEOUT_MS,
						timeoutMessage: `Timed out resolving Linear team '${reference}' after ${LINEAR_WEBHOOK_RESOLVE_TEAM_TIMEOUT_MS}ms`,
						mapError: (error) =>
							new LinearWebhookRuntimeError({
								message: error.message,
							}),
					})

			const startSdkWebhookRuntime = Effect.gen(function* () {
				yield* Effect.logDebug(
					`LinearWebhookService: starting SDK runtime (teamRef=${runtimeConfig.teamRef}, port=${runtimeConfig.port}, urlSource=${runtimeConfig.publicUrlSource})`,
				)
				const teamId = yield* resolveTeamId(runtimeConfig.teamRef)
				const webhookUrl = parseWebhookUrl(runtimeConfig.publicBaseUrl)
				const eventTypes = runtimeConfig.eventTypes
				const port = runtimeConfig.port

				const webhookIdRef = yield* Effect.sync((): { id: string | undefined } => ({
					id: undefined,
				}))
				const server = Bun.serve({
					port,
					fetch: async (request: Request): Promise<Response> => {
						const requestUrl = new URL(request.url)
						if (request.method !== "POST") {
							return new Response("Method not allowed", { status: 405 })
						}
						if (requestUrl.pathname !== WEBHOOK_PATH) {
							return new Response("Not found", { status: 404 })
						}

						const signature = request.headers.get(LINEAR_WEBHOOK_SIGNATURE_HEADER)
						if (!signature) {
							return new Response("Missing webhook signature", { status: 400 })
						}

						const rawBody = Buffer.from(await request.arrayBuffer())
						const timestamp = request.headers.get(LINEAR_WEBHOOK_TS_HEADER) ?? undefined
						try {
							const payload = webhookClient.parseData(rawBody, signature, timestamp)
							const decoded = decodeLinearIssueWebhookEvent(payload)
							if (Option.isSome(decoded)) {
								Queue.unsafeOffer(issueEventsQueue, decoded.value)
								Effect.runFork(SubscriptionRef.set(healthy, true))
							}
							return new Response("OK", { status: 200 })
						} catch (error) {
							void error
							return new Response("Invalid webhook", { status: 400 })
						}
					},
				})

				yield* Effect.addFinalizer(() =>
					Effect.gen(function* () {
						yield* Effect.sync(() => {
							server.stop(true)
						})
						const webhookId = webhookIdRef.id
						if (!webhookId) return
						yield* linearSdk.deleteWebhook(webhookId).pipe(
							Effect.mapError(
								() =>
									new LinearWebhookRuntimeError({
										message: "delete-webhook-failed",
									}),
							),
							Effect.catchAll((error) =>
								Effect.logWarning(error).pipe(
									Effect.zipRight(
										Effect.logWarning(`Linear webhook cleanup failed for webhook id ${webhookId}`),
									),
								),
							),
						)
					}),
				)

					const registrationPayload = yield* failOnTimeout({
						effect: linearSdk.createWebhook({
							teamId,
							url: webhookUrl,
							resourceTypes: [...eventTypes],
							secret: runtimeConfig.webhookSecret,
							enabled: true,
						}),
						timeoutMs: LINEAR_WEBHOOK_REGISTER_TIMEOUT_MS,
						timeoutMessage: `Timed out registering Linear webhook after ${LINEAR_WEBHOOK_REGISTER_TIMEOUT_MS}ms`,
						mapError: (error) =>
							new LinearWebhookRuntimeError({
								message: `Failed to register Linear webhook: ${formatErrorMessage(error)}`,
							}),
					})

				const registeredWebhookId = registrationPayload.webhookId
				if (registeredWebhookId) {
					webhookIdRef.id = registeredWebhookId
				}

				yield* SubscriptionRef.set(mode, "sdk")
				yield* SubscriptionRef.set(healthy, true)
				yield* Effect.log(
					`Linear SDK webhook runtime started on :${port} (team=${runtimeConfig.teamRef} via ${runtimeConfig.teamSource}, url=${webhookUrl} via ${runtimeConfig.publicUrlSource}, secret=${runtimeConfig.webhookSecretSource})`,
				)
			})

				yield* startSdkWebhookRuntime.pipe(
					Effect.timeout(`${LINEAR_WEBHOOK_STARTUP_TIMEOUT_MS} millis`),
					Effect.flatMap((result) =>
						result !== undefined
							? Effect.void
							: Effect.fail(
									new LinearWebhookRuntimeError({
										message: `Linear SDK webhook startup timed out after ${LINEAR_WEBHOOK_STARTUP_TIMEOUT_MS}ms`,
									}),
								),
					),
					Effect.catchAll((error) =>
						Effect.gen(function* () {
							yield* SubscriptionRef.set(mode, "failed")
							yield* SubscriptionRef.set(healthy, false)
							yield* Effect.logWarning(
								`Linear SDK webhook runtime failed: ${formatErrorMessage(error)}; falling back to polling`,
							)
						}),
					),
				)

			return {
				issueEvents: Stream.fromQueue(issueEventsQueue),
				healthy,
				mode,
			} satisfies LinearWebhookServiceApi
		}),
	},
) {}
