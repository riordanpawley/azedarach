import { LinearClient } from "@linear/sdk"
import { Config, Data, Effect, Option, Ref } from "effect"

export class LinearSdkError extends Data.TaggedError("LinearSdkError")<{
	readonly message: string
}> {}

export interface LinearSdkApi {
	readonly getClient: (apiKey: string) => Effect.Effect<LinearClient, LinearSdkError>
	readonly getClientFromEnv: Effect.Effect<LinearClient, LinearSdkError>
	readonly resolveTeamId: (
		client: LinearClient,
		reference: string,
	) => Effect.Effect<string, LinearSdkError>
}

export class LinearSdk extends Effect.Service<LinearSdk>()("LinearSdk", {
	effect: Effect.gen(function* () {
		const clientCacheRef = yield* Ref.make<Map<string, LinearClient>>(new Map())

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
			Effect.tryPromise({
				try: async () => {
					try {
						const team = await client.team(reference)
						if (team?.id) return team.id
					} catch {
						// fall through to list matching
					}

					const teams = await client.teams({ first: 250 })
					const normalizedReference = reference.trim().toLowerCase()
					const matched = teams.nodes.find((team) => {
						const byId = team.id === reference
						const byKey =
							typeof team.key === "string" &&
							team.key.trim().toLowerCase() === normalizedReference
						const byName =
							typeof team.name === "string" &&
							team.name.trim().toLowerCase() === normalizedReference
						return byId || byKey || byName
					})
					if (!matched) {
						return await Promise.reject(
							new LinearSdkError({
								message: `Unable to resolve Linear team '${reference}'`,
							}),
						)
					}
					return matched.id
				},
				catch: (error) =>
					error instanceof LinearSdkError
						? error
						: new LinearSdkError({
								message:
									error instanceof Error
										? error.message
										: `Unable to resolve Linear team '${reference}'`,
							}),
			})

		return {
			getClient,
			getClientFromEnv,
			resolveTeamId,
		} satisfies LinearSdkApi
	}),
}) {}
