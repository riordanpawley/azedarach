import { LinearClient } from "@linear/sdk"
import { Config, Data, Effect, Option, RateLimiter, Ref } from "effect"

export class LinearSdkError extends Data.TaggedError("LinearSdkError")<{
	readonly message: string
}> {}

const LINEAR_REQUEST_LIMIT = 20
const LINEAR_REQUEST_INTERVAL = "1 minute"

export interface LinearSdkApi {
	readonly getClient: (apiKey: string) => Effect.Effect<LinearClient, LinearSdkError>
	readonly getClientFromEnv: Effect.Effect<LinearClient, LinearSdkError>
	readonly rateLimit: <A, E, R>(effect: Effect.Effect<A, E, R>) => Effect.Effect<A, E, R>
	readonly resolveTeamId: (
		client: LinearClient,
		reference: string,
	) => Effect.Effect<string, LinearSdkError>
}

export class LinearSdk extends Effect.Service<LinearSdk>()("LinearSdk", {
	scoped: Effect.gen(function* () {
		const clientCacheRef = yield* Ref.make<Map<string, LinearClient>>(new Map())
		const requestRateLimiter = yield* RateLimiter.make({
			limit: LINEAR_REQUEST_LIMIT,
			interval: LINEAR_REQUEST_INTERVAL,
			algorithm: "token-bucket",
		})

		const rateLimit = <A, E, R>(effect: Effect.Effect<A, E, R>): Effect.Effect<A, E, R> =>
			requestRateLimiter(effect)

		const runRateLimitedRequest = <A>(
			request: () => Promise<A>,
			message: string,
		): Effect.Effect<A, LinearSdkError> =>
			Effect.tryPromise({
				try: request,
				catch: (error) =>
					new LinearSdkError({
						message: error instanceof Error ? error.message : message,
					}),
			}).pipe(rateLimit)

		const getClient = (apiKey: string): Effect.Effect<LinearClient, LinearSdkError> =>
			Effect.gen(function* () {
				const trimmedApiKey = apiKey.trim()
				if (trimmedApiKey.length === 0) {
					return yield* Effect.fail(
						new LinearSdkError({
							message: "LINEAR_API_KEY is empty",
						}),
					)
				}

				const cachedClient = yield* Ref.get(clientCacheRef).pipe(
					Effect.map((cache) => cache.get(trimmedApiKey)),
				)
				if (cachedClient !== undefined) {
					return cachedClient
				}

				const client = new LinearClient({ apiKey: trimmedApiKey })
				yield* Ref.update(clientCacheRef, (cache) => {
					const nextCache = new Map(cache)
					nextCache.set(trimmedApiKey, client)
					return nextCache
				})
				return client
			})

		const getClientFromEnv: Effect.Effect<LinearClient, LinearSdkError> = Effect.gen(function* () {
			const apiKeyOption = yield* Config.option(Config.string("LINEAR_API_KEY")).pipe(
				Effect.mapError(
					() =>
						new LinearSdkError({
							message: "Failed to read LINEAR_API_KEY",
						}),
				),
			)
			if (Option.isNone(apiKeyOption)) {
				return yield* Effect.fail(
					new LinearSdkError({
						message: "LINEAR_API_KEY is required",
					}),
				)
			}
			return yield* getClient(apiKeyOption.value)
		})

		const resolveTeamId = (
			client: LinearClient,
			reference: string,
		): Effect.Effect<string, LinearSdkError> =>
			Effect.gen(function* () {
				const directTeamIdOption = yield* runRateLimitedRequest(
					() => client.team(reference),
					`Unable to resolve Linear team '${reference}'`,
				).pipe(
					Effect.map((team) => (team?.id ? Option.some(team.id) : Option.none<string>())),
					Effect.catchAll(() => Effect.succeed(Option.none<string>())),
				)
				if (Option.isSome(directTeamIdOption)) {
					return directTeamIdOption.value
				}

				const teams = yield* runRateLimitedRequest(
					() => client.teams({ first: 250 }),
					`Unable to resolve Linear team '${reference}'`,
				)
				const normalizedReference = reference.trim().toLowerCase()
				const matched = teams.nodes.find((team) => {
					const byId = team.id === reference
					const byKey =
						typeof team.key === "string" && team.key.trim().toLowerCase() === normalizedReference
					const byName =
						typeof team.name === "string" && team.name.trim().toLowerCase() === normalizedReference
					return byId || byKey || byName
				})
				if (!matched) {
					return yield* Effect.fail(
						new LinearSdkError({
							message: `Unable to resolve Linear team '${reference}'`,
						}),
					)
				}
				return matched.id
			})

		return {
			getClient,
			getClientFromEnv,
			rateLimit,
			resolveTeamId,
		} satisfies LinearSdkApi
	}),
}) {}
