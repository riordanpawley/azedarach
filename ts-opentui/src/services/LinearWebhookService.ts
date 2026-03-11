/**
 * LinearWebhookService - SDK-backed Linear webhook runtime
 *
 * Runs a local webhook listener, verifies signatures, registers a temporary
 * webhook at startup, and emits typed Issue webhook events for consumers.
 */

import { Command, type CommandExecutor } from "@effect/platform"
import {
	LINEAR_WEBHOOK_SIGNATURE_HEADER,
	LINEAR_WEBHOOK_TS_HEADER,
	LinearWebhookClient,
} from "@linear/sdk/webhooks"
import { Config, Data, Effect, Option, Queue, Ref, Schema, Stream, SubscriptionRef } from "effect"
import { AppConfig } from "../config/AppConfig.js"
import type { ResolvedConfig } from "../config/defaults.js"
import { LinearSdk } from "../core/LinearSdk.js"
import { ProjectService } from "./ProjectService.js"

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

export interface LinearIssueWebhookMessage {
	readonly configKey: string
	readonly payload: LinearIssueWebhookEvent
}

export const decodeLinearIssueWebhookEvent = (
	payload: unknown,
): Option.Option<LinearIssueWebhookEvent> => {
	const decoded = Schema.decodeUnknownEither(IssueWebhookEnvelopeSchema)(payload)
	return decoded._tag === "Right" ? Option.some(decoded.right) : Option.none()
}

export interface LinearWebhookRuntimeStatus {
	readonly mode: LinearWebhookMode
	readonly healthy: boolean
	readonly reason: string | undefined
	readonly configKey: string | null
}

export interface LinearWebhookServiceApi {
	readonly issueEvents: Stream.Stream<LinearIssueWebhookMessage>
	readonly healthy: SubscriptionRef.SubscriptionRef<boolean>
	readonly mode: SubscriptionRef.SubscriptionRef<LinearWebhookMode>
	readonly status: SubscriptionRef.SubscriptionRef<LinearWebhookRuntimeStatus>
	readonly reconfigure: () => Effect.Effect<void, never, CommandExecutor.CommandExecutor>
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
	const decodedStatus = Schema.decodeUnknownEither(Schema.parseJson(TailscaleStatusSchema))(
		statusJson,
	)
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
			yield* Effect.logDebug("LinearWebhookService: tailscale status has no usable DNS name")
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

type ResolvedLinearConfig = Extract<
	ResolvedConfig["issueTracker"],
	{ readonly linear: unknown }
>["linear"]

interface ActiveLinearWebhookRuntime {
	readonly configKey: string
	readonly cleanup: Effect.Effect<void, never>
}

const normalizeWebhookProjectPath = (projectPath: string | undefined): string =>
	projectPath?.trim() ?? ""

const formatWebhookConfigSummary = (
	config: ResolvedConfig,
	projectPath: string | undefined,
): string => {
	if (!("linear" in config.issueTracker)) {
		if ("tracker" in config.issueTracker) return "backend=tracker"
		if ("legacy" in config.issueTracker) return "backend=legacy"
		return `backend=local projectPath=${normalizeWebhookProjectPath(projectPath) || "<none>"}`
	}

	const linearConfig = config.issueTracker.linear
	const webhookConfig = linearConfig.webhooks
	const configuredPort =
		Number.isInteger(webhookConfig.port) && webhookConfig.port > 0
			? webhookConfig.port
			: DEFAULT_WEBHOOK_PORT
	return [
		"backend=linear",
		`projectPath=${normalizeWebhookProjectPath(projectPath) || "<none>"}`,
		`enabled=${String(webhookConfig.enabled)}`,
		`transport=${webhookConfig.transport}`,
		`team=${normalizeNonEmpty(linearConfig.team) ?? "<auto>"}`,
		`configuredUrl=${normalizePublicBaseUrl(webhookConfig.url) ?? "<unset>"}`,
		`envUrl=${normalizePublicBaseUrl(process.env[WEBHOOK_PUBLIC_URL_ENV]) ?? "<unset>"}`,
		`port=${configuredPort}`,
		`events=${normalizeWebhookEvents(webhookConfig.events).join(",")}`,
		`secret=${normalizeNonEmpty(webhookConfig.secret) === undefined ? "generated" : "config"}`,
	].join(" ")
}

export const buildWebhookRuntimeConfigKey = (params: {
	readonly config: ResolvedConfig
	readonly projectPath: string | undefined
}): string => {
	const { config, projectPath } = params
	if (!("linear" in config.issueTracker)) {
		if ("tracker" in config.issueTracker) {
			return `backend=tracker|projectPath=${normalizeWebhookProjectPath(projectPath)}`
		}
		if ("legacy" in config.issueTracker) {
			return `backend=legacy|projectPath=${normalizeWebhookProjectPath(projectPath)}`
		}
		return `backend=local|projectPath=${normalizeWebhookProjectPath(projectPath)}`
	}

	const linearConfig = config.issueTracker.linear
	const webhookConfig = linearConfig.webhooks
	const configuredPort =
		Number.isInteger(webhookConfig.port) && webhookConfig.port > 0
			? webhookConfig.port
			: DEFAULT_WEBHOOK_PORT
	return [
		"backend=linear",
		`projectPath=${normalizeWebhookProjectPath(projectPath)}`,
		`enabled=${String(webhookConfig.enabled)}`,
		`transport=${webhookConfig.transport}`,
		`team=${normalizeNonEmpty(linearConfig.team) ?? ""}`,
		`configuredUrl=${normalizePublicBaseUrl(webhookConfig.url) ?? ""}`,
		`envUrl=${normalizePublicBaseUrl(process.env[WEBHOOK_PUBLIC_URL_ENV]) ?? ""}`,
		`port=${configuredPort}`,
		`events=${normalizeWebhookEvents(webhookConfig.events).join(",")}`,
		`secret=${normalizeNonEmpty(webhookConfig.secret) === undefined ? "generated" : "config"}`,
		`apiKeyPresent=${String(normalizeNonEmpty(process.env.LINEAR_API_KEY) !== undefined)}`,
	].join("|")
}

export class LinearWebhookService extends Effect.Service<LinearWebhookService>()(
	"LinearWebhookService",
	{
		dependencies: [AppConfig.Default, LinearSdk.Default, ProjectService.Default],
		scoped: Effect.gen(function* () {
			const appConfig = yield* AppConfig
			const linearSdk = yield* LinearSdk
			const projectService = yield* ProjectService

			const issueEventsQueue = yield* Queue.unbounded<LinearIssueWebhookMessage>()
			const healthy = yield* SubscriptionRef.make(false)
			const mode = yield* SubscriptionRef.make<LinearWebhookMode>("disabled")
			const status = yield* SubscriptionRef.make<LinearWebhookRuntimeStatus>({
				mode: "disabled",
				healthy: false,
				reason: "Linear webhook runtime not configured yet",
				configKey: null,
			})
			const activeRuntimeRef = yield* Ref.make<ActiveLinearWebhookRuntime | null>(null)
			const appliedConfigKeyRef = yield* Ref.make<string | null>(null)
			const setRuntimeStatus = (nextStatus: LinearWebhookRuntimeStatus) =>
				Effect.gen(function* () {
					yield* SubscriptionRef.set(mode, nextStatus.mode)
					yield* SubscriptionRef.set(healthy, nextStatus.healthy)
					yield* SubscriptionRef.set(status, nextStatus)
				})

			const stopActiveRuntime = (reason: string): Effect.Effect<void, never> =>
				Effect.gen(function* () {
					const activeRuntime = yield* Ref.get(activeRuntimeRef)
					if (activeRuntime === null) {
						return
					}

					yield* Ref.set(activeRuntimeRef, null)
					yield* Effect.logInfo(
						`LinearWebhookService: stopping SDK runtime (${reason}) configKey=${activeRuntime.configKey}`,
					)
					yield* activeRuntime.cleanup
				})

			const resolveWebhookTeamRef = (
				linearConfig: ResolvedLinearConfig,
			): Effect.Effect<ResolvedWebhookTeamRef, LinearWebhookRuntimeError> =>
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

			const resolveWebhookPublicUrl = (
				webhookConfig: ResolvedLinearConfig["webhooks"],
				port: number,
			) =>
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
						Effect.catchTag("TimeoutException", () => Effect.succeed(undefined)),
					)
					if (tailscalePublicBaseUrlResult === undefined) {
						yield* Effect.logDebug(
							`LinearWebhookService: tailscale URL resolution timed out after ${TAILSCALE_RESOLUTION_TIMEOUT_MS}ms`,
						)
					}

					if (tailscalePublicBaseUrlResult !== undefined) {
						yield* Effect.logDebug(
							`LinearWebhookService: using tailscale funnel URL ${tailscalePublicBaseUrlResult}`,
						)
						return {
							publicBaseUrl: tailscalePublicBaseUrlResult,
							source: "tailscale-funnel",
						} satisfies ResolvedWebhookPublicUrl
					}

					return yield* Effect.fail(
						new LinearWebhookRuntimeError({
							message: `Linear webhook SDK mode requires a public webhook URL. Set issueTracker.linear.webhooks.url, export ${WEBHOOK_PUBLIC_URL_ENV}, or run "tailscale funnel --bg --yes ${port}"`,
						}),
					)
				})

			const buildRuntimeConfig = (linearConfig: ResolvedLinearConfig) =>
				Effect.gen(function* () {
					const webhookConfig = linearConfig.webhooks
					const port =
						Number.isInteger(webhookConfig.port) && webhookConfig.port > 0
							? webhookConfig.port
							: DEFAULT_WEBHOOK_PORT
					const eventTypes = normalizeWebhookEvents(webhookConfig.events)
					const team = yield* resolveWebhookTeamRef(linearConfig)
					const webhookPublicUrl = yield* resolveWebhookPublicUrl(webhookConfig, port)
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
				})

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

			const startSdkWebhookRuntime = (params: {
				readonly configKey: string
				readonly linearConfig: ResolvedLinearConfig
			}) =>
				Effect.gen(function* () {
					const runtimeConfigResult = yield* Effect.either(buildRuntimeConfig(params.linearConfig))
					if (runtimeConfigResult._tag === "Left") {
						yield* setRuntimeStatus({
							mode: "misconfigured",
							healthy: false,
							reason: runtimeConfigResult.left.message,
							configKey: params.configKey,
						})
						yield* Effect.logWarning(
							`LinearWebhookService: runtime config invalid for configKey=${params.configKey}: ${runtimeConfigResult.left.message}; falling back to polling`,
						)
						return
					}

					const runtimeConfig = runtimeConfigResult.right
					const webhookClient = new LinearWebhookClient(runtimeConfig.webhookSecret)
					const runtimeStartResult = yield* Effect.either(
						Effect.gen(function* () {
							yield* Effect.logInfo(
								`LinearWebhookService: starting SDK runtime configKey=${params.configKey} team=${runtimeConfig.teamRef} port=${runtimeConfig.port} urlSource=${runtimeConfig.publicUrlSource}`,
							)
							const teamId = yield* resolveTeamId(runtimeConfig.teamRef)
							const webhookUrl = parseWebhookUrl(runtimeConfig.publicBaseUrl)
							const port = runtimeConfig.port
							const webhookIdRef: { id: string | undefined } = { id: undefined }
							const server = yield* Effect.try({
								try: () =>
									Bun.serve({
										port,
										fetch: async (request: Request): Promise<Response> => {
											const requestUrl = new URL(request.url)
											if (request.method !== "POST") {
												if (requestUrl.pathname === WEBHOOK_PATH) {
													void Effect.runFork(
														Effect.logInfo(
															`LinearWebhookService: rejected ${request.method} ${requestUrl.pathname} with 405`,
														),
													)
												}
												return new Response("Method not allowed", { status: 405 })
											}
											if (requestUrl.pathname !== WEBHOOK_PATH) {
												return new Response("Not found", { status: 404 })
											}

											const signature = request.headers.get(LINEAR_WEBHOOK_SIGNATURE_HEADER)
											if (!signature) {
												void Effect.runFork(
													Effect.logWarning(
														`LinearWebhookService: missing signature header for ${requestUrl.pathname}`,
													),
												)
												return new Response("Missing webhook signature", { status: 400 })
											}

											const rawBody = Buffer.from(await request.arrayBuffer())
											const timestamp = request.headers.get(LINEAR_WEBHOOK_TS_HEADER) ?? undefined
											try {
												const payload = webhookClient.parseData(rawBody, signature, timestamp)
												const decoded = decodeLinearIssueWebhookEvent(payload)
												if (Option.isSome(decoded)) {
													Queue.unsafeOffer(issueEventsQueue, {
														configKey: params.configKey,
														payload: decoded.value,
													})
													void Effect.runFork(SubscriptionRef.set(healthy, true))
													void Effect.runFork(
														Effect.logInfo(
															`LinearWebhookService: accepted issue webhook action=${decoded.value.action} identifier=${decoded.value.data.identifier}`,
														),
													)
												} else {
													void Effect.runFork(
														Effect.logInfo(
															"LinearWebhookService: ignored non-Issue webhook payload",
														),
													)
												}
												return new Response("OK", { status: 200 })
											} catch (error) {
												void Effect.runFork(
													Effect.logWarning(
														`LinearWebhookService: invalid webhook rejected: ${formatErrorMessage(error)}`,
													),
												)
												return new Response("Invalid webhook", { status: 400 })
											}
										},
									}),
								catch: (error) =>
									new LinearWebhookRuntimeError({
										message: `Failed to start local webhook listener on :${port}: ${formatErrorMessage(error)}`,
									}),
							})
							const cleanup = Effect.gen(function* () {
								yield* Effect.try({
									try: () => {
										server.stop(true)
									},
									catch: (error) =>
										new LinearWebhookRuntimeError({
											message: `Failed to stop local webhook listener on :${port}: ${formatErrorMessage(error)}`,
										}),
								}).pipe(
									Effect.catchAll((error) =>
										Effect.logWarning(`LinearWebhookService: ${error.message}`).pipe(Effect.asVoid),
									),
								)

								const webhookId = webhookIdRef.id
								if (webhookId === undefined) {
									return
								}

								yield* linearSdk
									.deleteWebhook(webhookId)
									.pipe(
										Effect.catchAll((error) =>
											Effect.logWarning(
												`Linear webhook cleanup failed for webhook id ${webhookId}: ${error.message}`,
											).pipe(Effect.asVoid),
										),
									)
							})

							const registrationResult = yield* Effect.either(
								failOnTimeout({
									effect: linearSdk.createWebhook({
										teamId,
										url: webhookUrl,
										resourceTypes: [...runtimeConfig.eventTypes],
										secret: runtimeConfig.webhookSecret,
										enabled: true,
									}),
									timeoutMs: LINEAR_WEBHOOK_REGISTER_TIMEOUT_MS,
									timeoutMessage: `Timed out registering Linear webhook after ${LINEAR_WEBHOOK_REGISTER_TIMEOUT_MS}ms`,
									mapError: (error) =>
										new LinearWebhookRuntimeError({
											message: `Failed to register Linear webhook: ${formatErrorMessage(error)}`,
										}),
								}),
							)
							if (registrationResult._tag === "Left") {
								yield* cleanup
								return yield* Effect.fail(registrationResult.left)
							}

							if (registrationResult.right.webhookId !== undefined) {
								webhookIdRef.id = registrationResult.right.webhookId
							}

							return {
								webhookUrl,
								cleanup,
							}
						}).pipe(
							Effect.timeout(`${LINEAR_WEBHOOK_STARTUP_TIMEOUT_MS} millis`),
							Effect.flatMap((result) =>
								result !== undefined
									? Effect.succeed(result)
									: Effect.fail(
											new LinearWebhookRuntimeError({
												message: `Linear SDK webhook startup timed out after ${LINEAR_WEBHOOK_STARTUP_TIMEOUT_MS}ms`,
											}),
										),
							),
						),
					)

					if (runtimeStartResult._tag === "Left") {
						const runtimeErrorMessage = formatErrorMessage(runtimeStartResult.left)
						yield* setRuntimeStatus({
							mode: "failed",
							healthy: false,
							reason: runtimeErrorMessage,
							configKey: params.configKey,
						})
						yield* Effect.logWarning(
							`Linear SDK webhook runtime failed for configKey=${params.configKey}: ${runtimeErrorMessage}; falling back to polling`,
						)
						return
					}

					yield* Ref.set(activeRuntimeRef, {
						configKey: params.configKey,
						cleanup: runtimeStartResult.right.cleanup,
					})
					yield* setRuntimeStatus({
						mode: "sdk",
						healthy: true,
						reason: undefined,
						configKey: params.configKey,
					})
					yield* Effect.logInfo(
						`Linear SDK webhook runtime started on :${runtimeConfig.port} (team=${runtimeConfig.teamRef} via ${runtimeConfig.teamSource}, url=${runtimeStartResult.right.webhookUrl} via ${runtimeConfig.publicUrlSource}, secret=${runtimeConfig.webhookSecretSource}, configKey=${params.configKey})`,
					)
				})

			const reconfigure = (): Effect.Effect<void, never, CommandExecutor.CommandExecutor> =>
				Effect.gen(function* () {
					const config = yield* SubscriptionRef.get(appConfig.config)
					const currentProjectPath = yield* projectService.getCurrentPath()
					const nextConfigKey = buildWebhookRuntimeConfigKey({
						config,
						projectPath: currentProjectPath,
					})
					const previousConfigKey = yield* Ref.get(appliedConfigKeyRef)
					if (previousConfigKey === nextConfigKey) {
						return
					}

					yield* Ref.set(appliedConfigKeyRef, nextConfigKey)
					yield* Effect.logInfo(
						`LinearWebhookService: applying config ${formatWebhookConfigSummary(config, currentProjectPath)} configKey=${nextConfigKey}`,
					)
					yield* stopActiveRuntime(
						previousConfigKey === null
							? "initial runtime configuration"
							: `config changed from ${previousConfigKey}`,
					)

					if (!("linear" in config.issueTracker)) {
						yield* setRuntimeStatus({
							mode: "disabled",
							healthy: false,
							reason: "Linear backend not active",
							configKey: nextConfigKey,
						})
						yield* Effect.logInfo(
							"LinearWebhookService: backend is not linear; SDK runtime disabled",
						)
						return
					}

					const linearConfig = config.issueTracker.linear
					const webhookConfig = linearConfig.webhooks
					if (webhookConfig.enabled === false) {
						yield* setRuntimeStatus({
							mode: "disabled",
							healthy: false,
							reason: "Linear webhooks disabled in config",
							configKey: nextConfigKey,
						})
						yield* Effect.logInfo(
							"LinearWebhookService: webhooks disabled in config; SDK runtime skipped",
						)
						return
					}

					if (webhookConfig.transport === "cli") {
						yield* setRuntimeStatus({
							mode: "cli",
							healthy: false,
							reason: "CLI webhook transport selected",
							configKey: nextConfigKey,
						})
						yield* Effect.logInfo(
							"LinearWebhookService: CLI transport selected; SDK runtime skipped",
						)
						return
					}

					yield* startSdkWebhookRuntime({
						configKey: nextConfigKey,
						linearConfig,
					})
				})

			yield* Effect.addFinalizer(() => stopActiveRuntime("service shutdown"))
			yield* reconfigure()

			return {
				issueEvents: Stream.fromQueue(issueEventsQueue),
				healthy,
				mode,
				status,
				reconfigure,
			} satisfies LinearWebhookServiceApi
		}),
	},
) {}
