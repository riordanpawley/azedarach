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
import { Data, Effect, Option, Queue, Schema, Stream, SubscriptionRef } from "effect"
import { AppConfig } from "../config/AppConfig.js"
import { LinearSdk } from "../core/LinearSdk.js"

const WEBHOOK_PATH = "/linear/webhook"
const DEFAULT_WEBHOOK_PORT = 9000
const DEFAULT_WEBHOOK_EVENTS: readonly string[] = ["Issue"]

export type LinearWebhookMode = "disabled" | "cli" | "misconfigured" | "sdk" | "failed"

class LinearWebhookRuntimeError extends Data.TaggedError("LinearWebhookRuntimeError")<{
	readonly message: string
}> {}

const hasMessage = (value: unknown): value is { readonly message: unknown } =>
	typeof value === "object" && value !== null && "message" in value

const formatErrorMessage = (error: unknown): string =>
	hasMessage(error) ? String(error.message) : String(error)

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

const normalizeWebhookEvents = (
	events: readonly string[] | undefined,
): readonly string[] => {
	const configured = events
		?.map((eventType) => eventType.trim())
		.filter((eventType) => eventType.length > 0)
	return configured !== undefined && configured.length > 0 ? configured : DEFAULT_WEBHOOK_EVENTS
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

			const webhookConfig = config.issueTracker.linear.webhooks
			const transport = webhookConfig.transport
			if (webhookConfig.enabled === false) {
				yield* SubscriptionRef.set(mode, "disabled")
				return {
					issueEvents: Stream.fromQueue(issueEventsQueue),
					healthy,
					mode,
				} satisfies LinearWebhookServiceApi
			}

			if (transport === "cli") {
				yield* SubscriptionRef.set(mode, "cli")
				return {
					issueEvents: Stream.fromQueue(issueEventsQueue),
					healthy,
					mode,
				} satisfies LinearWebhookServiceApi
			}

			const teamRef = config.issueTracker.linear.team?.trim()
			const publicUrl = webhookConfig.url?.trim()
			const configuredSecret = webhookConfig.secret?.trim()
			if (!teamRef || !publicUrl) {
				yield* SubscriptionRef.set(mode, "misconfigured")
				const missing: string[] = []
				if (!teamRef) missing.push("issueTracker.linear.team")
				if (!publicUrl) missing.push("issueTracker.linear.webhooks.url")
				yield* Effect.logWarning(
					`Linear webhook SDK mode requires ${missing.join(", ")}; falling back to polling`,
				)
				return {
					issueEvents: Stream.fromQueue(issueEventsQueue),
					healthy,
					mode,
				} satisfies LinearWebhookServiceApi
			}

			const linearClientResult = yield* Effect.either(linearSdk.getClientFromEnv)
			if (linearClientResult._tag === "Left") {
				yield* SubscriptionRef.set(mode, "misconfigured")
				yield* Effect.logWarning(
					`${linearClientResult.left.message} for Linear SDK webhook registration; falling back to polling`,
				)
				return {
					issueEvents: Stream.fromQueue(issueEventsQueue),
					healthy,
					mode,
				} satisfies LinearWebhookServiceApi
			}

			const linearClient = linearClientResult.right
			const webhookSecret =
				configuredSecret && configuredSecret.length > 0
					? configuredSecret
					: `azw_${crypto.randomUUID().replaceAll("-", "")}`
			const webhookClient = new LinearWebhookClient(webhookSecret)

			const resolveTeamId = (
				reference: string,
			): Effect.Effect<string, LinearWebhookRuntimeError> =>
				linearSdk.resolveTeamId(linearClient, reference).pipe(
					Effect.mapError(
						(error) =>
							new LinearWebhookRuntimeError({
								message: error.message,
							}),
					),
				)

			const startSdkWebhookRuntime = Effect.gen(function* () {
				const teamId = yield* resolveTeamId(teamRef)
				const webhookUrl = parseWebhookUrl(publicUrl)
				const eventTypes = normalizeWebhookEvents(webhookConfig.events)
				const port =
					Number.isInteger(webhookConfig.port) && webhookConfig.port > 0
						? webhookConfig.port
						: DEFAULT_WEBHOOK_PORT

				const webhookIdRef = yield* Effect.sync(
					(): { id: string | undefined } => ({ id: undefined }),
				)
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
						yield* Effect.tryPromise({
							try: () => linearClient.deleteWebhook(webhookId),
							catch: () =>
								new LinearWebhookRuntimeError({
									message: "delete-webhook-failed",
								}),
						}).pipe(
							Effect.catchAll(() =>
								Effect.logWarning(
									`Linear webhook cleanup failed for webhook id ${webhookId}`,
								),
							),
						)
					}),
				)

				const registrationPayload = yield* Effect.tryPromise({
					try: () =>
						linearClient.createWebhook({
							teamId,
							url: webhookUrl,
							resourceTypes: [...eventTypes],
							secret: webhookSecret,
							enabled: true,
						}),
						catch: (error) =>
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
					`Linear SDK webhook runtime started on :${port} (team=${teamRef}, url=${webhookUrl}, secret=${configuredSecret ? "configured" : "generated"})`,
				)
			})

				yield* startSdkWebhookRuntime.pipe(
					Effect.catchAll((error) =>
						Effect.gen(function* () {
							yield* SubscriptionRef.set(mode, "failed")
							yield* SubscriptionRef.set(healthy, false)
							yield* Effect.logWarning(
								`Linear SDK webhook runtime failed: ${formatErrorMessage(error)}`,
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
