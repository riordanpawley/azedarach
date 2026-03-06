import { LinearClient } from "@linear/sdk"
import { Config, Data, Effect, Option, Ref } from "effect"
import { LinearSyncThrottle } from "./LinearSyncThrottle.js"

export class LinearSdkError extends Data.TaggedError("LinearSdkError")<{
	readonly message: string
}> {}

type LinearIssuesArgs = Parameters<LinearClient["issues"]>[0]
type LinearIssuesResult = Awaited<ReturnType<LinearClient["issues"]>>
type LinearIssueResult = Awaited<ReturnType<LinearClient["issue"]>>
type LinearWorkflowStatesResult = Awaited<ReturnType<LinearClient["workflowStates"]>>
type LinearIssueLabelsResult = Awaited<ReturnType<LinearClient["issueLabels"]>>
type LinearUsersResult = Awaited<ReturnType<LinearClient["users"]>>
type LinearTeamsArgs = Parameters<LinearClient["teams"]>[0]
type LinearCreateIssueInput = Parameters<LinearClient["createIssue"]>[0]
type LinearCreateIssueResult = Awaited<ReturnType<LinearClient["createIssue"]>>
type LinearUpdateIssueInput = Parameters<LinearClient["updateIssue"]>[1]
type LinearUpdateIssueResult = Awaited<ReturnType<LinearClient["updateIssue"]>>
type LinearCreateWebhookInput = Parameters<LinearClient["createWebhook"]>[0]
type LinearCreateWebhookResult = Awaited<ReturnType<LinearClient["createWebhook"]>>
type LinearDeleteWebhookResult = Awaited<ReturnType<LinearClient["deleteWebhook"]>>
type LinearTeamResult = Awaited<ReturnType<LinearClient["team"]>>
type LinearTeamsResult = Awaited<ReturnType<LinearClient["teams"]>>
type LinearProjectResult = Awaited<ReturnType<LinearClient["project"]>>
type LinearProjectsResult = Awaited<ReturnType<LinearClient["projects"]>>

export interface LinearSdkApi {
	readonly issues: (
		args: LinearIssuesArgs,
		options?: { readonly maxWaitMs?: number },
	) => Effect.Effect<LinearIssuesResult, LinearSdkError>
	readonly issue: (
		id: string,
		options?: { readonly maxWaitMs?: number },
	) => Effect.Effect<LinearIssueResult, LinearSdkError>
	readonly workflowStates: (
		args: Parameters<LinearClient["workflowStates"]>[0],
		options?: { readonly maxWaitMs?: number },
	) => Effect.Effect<LinearWorkflowStatesResult, LinearSdkError>
	readonly issueLabels: (
		args: Parameters<LinearClient["issueLabels"]>[0],
		options?: { readonly maxWaitMs?: number },
	) => Effect.Effect<LinearIssueLabelsResult, LinearSdkError>
	readonly users: (
		args: Parameters<LinearClient["users"]>[0],
		options?: { readonly maxWaitMs?: number },
	) => Effect.Effect<LinearUsersResult, LinearSdkError>
	readonly teams: (
		args: LinearTeamsArgs,
		options?: { readonly maxWaitMs?: number },
	) => Effect.Effect<LinearTeamsResult, LinearSdkError>
	readonly createIssue: (
		input: LinearCreateIssueInput,
		options?: { readonly maxWaitMs?: number },
	) => Effect.Effect<LinearCreateIssueResult, LinearSdkError>
	readonly updateIssue: (
		id: string,
		input: LinearUpdateIssueInput,
		options?: { readonly maxWaitMs?: number },
	) => Effect.Effect<LinearUpdateIssueResult, LinearSdkError>
	readonly createWebhook: (
		input: LinearCreateWebhookInput,
		options?: { readonly maxWaitMs?: number },
	) => Effect.Effect<LinearCreateWebhookResult, LinearSdkError>
	readonly deleteWebhook: (
		id: string,
		options?: { readonly maxWaitMs?: number },
	) => Effect.Effect<LinearDeleteWebhookResult, LinearSdkError>
	readonly resolveTeamId: (
		reference: string,
		options?: { readonly maxWaitMs?: number },
	) => Effect.Effect<string, LinearSdkError>
	readonly resolveProjectId: (
		reference: string,
		options?: { readonly maxWaitMs?: number },
	) => Effect.Effect<string, LinearSdkError>
}

export class LinearSdk extends Effect.Service<LinearSdk>()("LinearSdk", {
	dependencies: [LinearSyncThrottle.Default],
	scoped: Effect.gen(function* () {
		const throttle = yield* LinearSyncThrottle
		const clientCacheRef = yield* Ref.make<Map<string, LinearClient>>(new Map())

		const getClient = (apiKey: string): Effect.Effect<LinearClient, LinearSdkError> =>
			Effect.gen(function* () {
				const trimmedApiKey = apiKey.trim()
				if (trimmedApiKey.length === 0) {
					return yield* Effect.fail(new LinearSdkError({ message: "LINEAR_API_KEY is empty" }))
				}

				const cachedClient = yield* Ref.get(clientCacheRef).pipe(
					Effect.map((cache) => cache.get(trimmedApiKey)),
				)
				if (cachedClient !== undefined) {
					return cachedClient
				}

				const client = new LinearClient({ apiKey: trimmedApiKey })
				yield* Ref.update(clientCacheRef, (cache) => {
					const next = new Map(cache)
					next.set(trimmedApiKey, client)
					return next
				})
				return client
			})

		const getClientFromEnv: Effect.Effect<LinearClient, LinearSdkError> = Effect.gen(function* () {
			const apiKeyOption = yield* Config.option(Config.string("LINEAR_API_KEY")).pipe(
				Effect.mapError(() => new LinearSdkError({ message: "Failed to read LINEAR_API_KEY" })),
			)
			if (Option.isNone(apiKeyOption)) {
				return yield* Effect.fail(new LinearSdkError({ message: "LINEAR_API_KEY is required" }))
			}
			return yield* getClient(apiKeyOption.value)
		})

		const runWithClient = <A>(params: {
			readonly operation: string
			readonly maxWaitMs?: number
			readonly request: (client: LinearClient) => Promise<A>
			readonly fallbackError: string
		}): Effect.Effect<A, LinearSdkError> =>
			Effect.gen(function* () {
				const client = yield* getClientFromEnv
				const executed = yield* throttle.enqueue({
					effect: Effect.tryPromise({
						try: () => params.request(client),
						catch: (cause) =>
							new LinearSdkError({
								message:
									cause instanceof Error
										? `${params.operation}: ${cause.message}`
										: `${params.operation}: ${params.fallbackError}`,
							}),
					}),
					maxWaitMs: params.maxWaitMs,
				})

				if (Option.isNone(executed)) {
					return yield* Effect.fail(
						new LinearSdkError({ message: `${params.operation}: throttled timeout` }),
					)
				}

				return executed.value
			})

		const issues: LinearSdkApi["issues"] = (args, options) =>
			runWithClient({
				operation: "issues",
				maxWaitMs: options?.maxWaitMs,
				request: (client) => client.issues(args),
				fallbackError: "Failed to fetch issues from Linear",
			})

		const issue: LinearSdkApi["issue"] = (id, options) =>
			runWithClient({
				operation: "issue",
				maxWaitMs: options?.maxWaitMs,
				request: (client) => client.issue(id),
				fallbackError: `Failed to fetch issue ${id} from Linear`,
			})

		const workflowStates: LinearSdkApi["workflowStates"] = (args, options) =>
			runWithClient({
				operation: "workflowStates",
				maxWaitMs: options?.maxWaitMs,
				request: (client) => client.workflowStates(args),
				fallbackError: "Failed to fetch workflow states from Linear",
			})

		const issueLabels: LinearSdkApi["issueLabels"] = (args, options) =>
			runWithClient({
				operation: "issueLabels",
				maxWaitMs: options?.maxWaitMs,
				request: (client) => client.issueLabels(args),
				fallbackError: "Failed to fetch issue labels from Linear",
			})

		const users: LinearSdkApi["users"] = (args, options) =>
			runWithClient({
				operation: "users",
				maxWaitMs: options?.maxWaitMs,
				request: (client) => client.users(args),
				fallbackError: "Failed to fetch users from Linear",
			})

		const teams: LinearSdkApi["teams"] = (args, options) =>
			runWithClient({
				operation: "teams",
				maxWaitMs: options?.maxWaitMs,
				request: (client) => client.teams(args),
				fallbackError: "Failed to fetch teams from Linear",
			})

		const createIssue: LinearSdkApi["createIssue"] = (input, options) =>
			runWithClient({
				operation: "createIssue",
				maxWaitMs: options?.maxWaitMs,
				request: (client) => client.createIssue(input),
				fallbackError: "Failed to create issue in Linear",
			})

		const updateIssue: LinearSdkApi["updateIssue"] = (id, input, options) =>
			runWithClient({
				operation: "updateIssue",
				maxWaitMs: options?.maxWaitMs,
				request: (client) => client.updateIssue(id, input),
				fallbackError: `Failed to update issue ${id} in Linear`,
			})

		const createWebhook: LinearSdkApi["createWebhook"] = (input, options) =>
			runWithClient({
				operation: "createWebhook",
				maxWaitMs: options?.maxWaitMs,
				request: (client) => client.createWebhook(input),
				fallbackError: "Failed to create webhook in Linear",
			})

		const deleteWebhook: LinearSdkApi["deleteWebhook"] = (id, options) =>
			runWithClient({
				operation: "deleteWebhook",
				maxWaitMs: options?.maxWaitMs,
				request: (client) => client.deleteWebhook(id),
				fallbackError: `Failed to delete webhook ${id} in Linear`,
			})

			const resolveTeamId: LinearSdkApi["resolveTeamId"] = (reference, options) =>
				Effect.gen(function* () {
				const directTeamIdOption = yield* runWithClient<LinearTeamResult>({
					operation: "team",
					maxWaitMs: options?.maxWaitMs,
					request: (client) => client.team(reference),
					fallbackError: `Unable to resolve Linear team '${reference}'`,
				}).pipe(
					Effect.map((team) => (team?.id ? Option.some(team.id) : Option.none<string>())),
					Effect.catchAll(() => Effect.succeed(Option.none<string>())),
				)

				if (Option.isSome(directTeamIdOption)) {
					return directTeamIdOption.value
				}

				const teams = yield* runWithClient<LinearTeamsResult>({
					operation: "teams",
					maxWaitMs: options?.maxWaitMs,
					request: (client) => client.teams({ first: 250 }),
					fallbackError: `Unable to resolve Linear team '${reference}'`,
				})

				const normalizedReference = reference.trim().toLowerCase()
				const matched = teams.nodes.find((team) => {
					const byId = team.id === reference
					const byKey =
						typeof team.key === "string" && team.key.trim().toLowerCase() === normalizedReference
					const byName =
						typeof team.name === "string" && team.name.trim().toLowerCase() === normalizedReference
					return byId || byKey || byName
				})

				if (matched === undefined) {
					return yield* Effect.fail(
						new LinearSdkError({
							message: `Unable to resolve Linear team '${reference}'`,
						}),
					)
				}

					return matched.id
				})

			const resolveProjectId: LinearSdkApi["resolveProjectId"] = (reference, options) =>
				Effect.gen(function* () {
					const normalizedReference = reference.trim().toLowerCase()
					if (normalizedReference.length === 0) {
						return yield* Effect.fail(
							new LinearSdkError({
								message: "Unable to resolve Linear project: reference is empty",
							}),
						)
					}

					const directProjectIdOption = yield* runWithClient<LinearProjectResult>({
						operation: "project",
						maxWaitMs: options?.maxWaitMs,
						request: (client) => client.project(reference),
						fallbackError: `Unable to resolve Linear project '${reference}'`,
					}).pipe(
						Effect.map((project) => (project?.id ? Option.some(project.id) : Option.none<string>())),
						Effect.catchAll(() => Effect.succeed(Option.none<string>())),
					)

					if (Option.isSome(directProjectIdOption)) {
						return directProjectIdOption.value
					}

					const projects = yield* runWithClient<LinearProjectsResult>({
						operation: "projects",
						maxWaitMs: options?.maxWaitMs,
						request: (client) => client.projects({ first: 250 }),
						fallbackError: `Unable to resolve Linear project '${reference}'`,
					})

					const matched = projects.nodes.find((project) => {
						const byId = project.id === reference
						const bySlugId = project.slugId.trim().toLowerCase() === normalizedReference
						const byName =
							typeof project.name === "string" &&
							project.name.trim().toLowerCase() === normalizedReference
						return byId || bySlugId || byName
					})

					if (matched === undefined) {
						return yield* Effect.fail(
							new LinearSdkError({
								message: `Unable to resolve Linear project '${reference}'`,
							}),
						)
					}

					return matched.id
				})

			return {
				issues,
				issue,
			workflowStates,
			issueLabels,
			users,
			teams,
			createIssue,
			updateIssue,
				createWebhook,
				deleteWebhook,
				resolveTeamId,
				resolveProjectId,
			} satisfies LinearSdkApi
		}),
	}) {}
