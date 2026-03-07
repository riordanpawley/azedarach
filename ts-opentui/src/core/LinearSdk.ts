import { LinearClient } from "@linear/sdk"
import { Config, Data, Effect, Option, Ref } from "effect"
import { LinearSyncThrottle } from "./LinearSyncThrottle.js"

export class LinearSdkError extends Data.TaggedError("LinearSdkError")<{
	readonly message: string
}> {}

const isRecord = (value: unknown): value is Record<string, unknown> =>
	typeof value === "object" && value !== null

const extractErrorMessage = (value: unknown): string | undefined => {
	if (value instanceof Error) {
		return value.message
	}
	if (!isRecord(value)) {
		return undefined
	}
	const message = value.message
	if (typeof message !== "string") {
		return undefined
	}
	const trimmed = message.trim()
	return trimmed.length > 0 ? trimmed : undefined
}

const normalizePathSegment = (value: unknown): string | undefined => {
	if (typeof value === "string") {
		const trimmed = value.trim()
		return trimmed.length > 0 ? trimmed : undefined
	}
	if (typeof value === "number" && Number.isFinite(value)) {
		return String(value)
	}
	return undefined
}

const formatValidationValue = (value: unknown): string => {
	if (typeof value === "string") {
		return JSON.stringify(value)
	}
	const serialized = JSON.stringify(value)
	if (serialized === undefined) {
		return String(value)
	}
	return serialized.length <= 160 ? serialized : `${serialized.slice(0, 157)}...`
}

const findFirstValidationConstraint = (
	node: unknown,
	pathPrefix: readonly string[],
): { readonly path: string; readonly message: string; readonly value: unknown } | undefined => {
	if (!isRecord(node)) return undefined
	const propertySegment = normalizePathSegment(node.property)
	const nextPath = propertySegment === undefined ? [...pathPrefix] : [...pathPrefix, propertySegment]

	const constraints = node.constraints
	if (isRecord(constraints)) {
		for (const [constraintName, constraintMessage] of Object.entries(constraints)) {
			if (typeof constraintMessage !== "string") {
				continue
			}
			const trimmedConstraintMessage = constraintMessage.trim()
			if (trimmedConstraintMessage.length === 0) {
				continue
			}
			return {
				path: nextPath.length === 0 ? "<root>" : nextPath.join("."),
				message: `${constraintName}: ${trimmedConstraintMessage}`,
				value: node.value,
			}
		}
	}

	const children = node.children
	if (!Array.isArray(children)) {
		return undefined
	}
	for (const child of children) {
		const found = findFirstValidationConstraint(child, nextPath)
		if (found !== undefined) {
			return found
		}
	}
	return undefined
}

const extractLinearValidationDetails = (cause: unknown): string | undefined => {
	if (!isRecord(cause)) return undefined
	const raw = cause.raw
	if (!isRecord(raw)) return undefined
	const response = raw.response
	if (!isRecord(response)) return undefined
	const errors = response.errors
	if (!Array.isArray(errors) || errors.length === 0) return undefined
	const firstError = errors[0]
	if (!isRecord(firstError)) return undefined
	const extensions = firstError.extensions
	if (!isRecord(extensions)) return undefined
	const validationErrors = extensions.validationErrors
	if (!Array.isArray(validationErrors) || validationErrors.length === 0) return undefined

	const rootPathRaw = firstError.path
	const rootPath =
		Array.isArray(rootPathRaw) && rootPathRaw.length > 0
			? rootPathRaw
					.map((segment) => normalizePathSegment(segment))
					.filter((segment): segment is string => segment !== undefined)
			: []

	const renderedDetails: string[] = []
	for (const validationError of validationErrors) {
		const firstConstraint = findFirstValidationConstraint(validationError, rootPath)
		if (firstConstraint === undefined) continue
		renderedDetails.push(
			`${firstConstraint.path} -> ${firstConstraint.message} (value=${formatValidationValue(firstConstraint.value)})`,
		)
	}

	if (renderedDetails.length === 0) return undefined
	return renderedDetails.join(" | ")
}

export const formatLinearOperationError = (params: {
	readonly operation: string
	readonly fallbackError: string
	readonly cause: unknown
}): string => {
	const baseMessage = extractErrorMessage(params.cause) ?? params.fallbackError
	const validationDetails = extractLinearValidationDetails(params.cause)
	return validationDetails === undefined
		? `${params.operation}: ${baseMessage}`
		: `${params.operation}: ${baseMessage}; validation=${validationDetails}`
}

type LinearIssuesArgs = Parameters<LinearClient["issues"]>[0]
type LinearIssuesResult = Awaited<ReturnType<LinearClient["issues"]>>
type LinearIssueResult = Awaited<ReturnType<LinearClient["issue"]>>
type LinearDocumentsArgs = Parameters<LinearClient["documents"]>[0]
type LinearDocumentsResult = Awaited<ReturnType<LinearClient["documents"]>>
type LinearDocumentResult = Awaited<ReturnType<LinearClient["document"]>>
type LinearWorkflowStatesResult = Awaited<ReturnType<LinearClient["workflowStates"]>>
type LinearIssueLabelsResult = Awaited<ReturnType<LinearClient["issueLabels"]>>
type LinearUsersResult = Awaited<ReturnType<LinearClient["users"]>>
type LinearViewerResult = Awaited<LinearClient["viewer"]>
type LinearTeamsArgs = Parameters<LinearClient["teams"]>[0]
type LinearCreateIssueInput = Parameters<LinearClient["createIssue"]>[0]
type LinearCreateIssueResult = Awaited<ReturnType<LinearClient["createIssue"]>>
type LinearUpdateIssueInput = Parameters<LinearClient["updateIssue"]>[1]
type LinearUpdateIssueResult = Awaited<ReturnType<LinearClient["updateIssue"]>>
type LinearCreateDocumentInput = Parameters<LinearClient["createDocument"]>[0]
type LinearCreateDocumentResult = Awaited<ReturnType<LinearClient["createDocument"]>>
type LinearUpdateDocumentInput = Parameters<LinearClient["updateDocument"]>[1]
type LinearUpdateDocumentResult = Awaited<ReturnType<LinearClient["updateDocument"]>>
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
	readonly documents: (
		args?: LinearDocumentsArgs,
		options?: { readonly maxWaitMs?: number },
	) => Effect.Effect<LinearDocumentsResult, LinearSdkError>
	readonly document: (
		id: string,
		options?: { readonly maxWaitMs?: number },
	) => Effect.Effect<LinearDocumentResult, LinearSdkError>
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
	readonly viewer: (options?: {
		readonly maxWaitMs?: number
	}) => Effect.Effect<LinearViewerResult, LinearSdkError>
	readonly teams: (
		args: LinearTeamsArgs,
		options?: { readonly maxWaitMs?: number },
	) => Effect.Effect<LinearTeamsResult, LinearSdkError>
	readonly createIssue: (
		input: LinearCreateIssueInput,
		options?: { readonly maxWaitMs?: number },
	) => Effect.Effect<LinearCreateIssueResult, LinearSdkError>
	readonly createDocument: (
		input: LinearCreateDocumentInput,
		options?: { readonly maxWaitMs?: number },
	) => Effect.Effect<LinearCreateDocumentResult, LinearSdkError>
	readonly updateIssue: (
		id: string,
		input: LinearUpdateIssueInput,
		options?: { readonly maxWaitMs?: number },
	) => Effect.Effect<LinearUpdateIssueResult, LinearSdkError>
	readonly updateDocument: (
		id: string,
		input: LinearUpdateDocumentInput,
		options?: { readonly maxWaitMs?: number },
	) => Effect.Effect<LinearUpdateDocumentResult, LinearSdkError>
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
								message: formatLinearOperationError({
									operation: params.operation,
									fallbackError: params.fallbackError,
									cause,
								}),
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

		const documents: LinearSdkApi["documents"] = (args, options) =>
			runWithClient({
				operation: "documents",
				maxWaitMs: options?.maxWaitMs,
				request: (client) => client.documents(args),
				fallbackError: "Failed to fetch documents from Linear",
			})

		const document: LinearSdkApi["document"] = (id, options) =>
			runWithClient({
				operation: "document",
				maxWaitMs: options?.maxWaitMs,
				request: (client) => client.document(id),
				fallbackError: `Failed to fetch document ${id} from Linear`,
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

		const viewer: LinearSdkApi["viewer"] = (options) =>
			runWithClient({
				operation: "viewer",
				maxWaitMs: options?.maxWaitMs,
				request: (client) => client.viewer,
				fallbackError: "Failed to fetch current user from Linear",
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

		const createDocument: LinearSdkApi["createDocument"] = (input, options) =>
			runWithClient({
				operation: "createDocument",
				maxWaitMs: options?.maxWaitMs,
				request: (client) => client.createDocument(input),
				fallbackError: "Failed to create document in Linear",
			})

		const updateIssue: LinearSdkApi["updateIssue"] = (id, input, options) =>
			runWithClient({
				operation: "updateIssue",
				maxWaitMs: options?.maxWaitMs,
				request: (client) => client.updateIssue(id, input),
				fallbackError: `Failed to update issue ${id} in Linear`,
			})

		const updateDocument: LinearSdkApi["updateDocument"] = (id, input, options) =>
			runWithClient({
				operation: "updateDocument",
				maxWaitMs: options?.maxWaitMs,
				request: (client) => client.updateDocument(id, input),
				fallbackError: `Failed to update document ${id} in Linear`,
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
					Effect.catchAll((error) =>
						Effect.logWarning(`Recovering after caught error: ${String(error)}`).pipe(
							Effect.zipRight(Effect.succeed(Option.none<string>())),
						),
					),
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
					Effect.catchAll((error) =>
						Effect.logWarning(`Recovering after caught error: ${String(error)}`).pipe(
							Effect.zipRight(Effect.succeed(Option.none<string>())),
						),
					),
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
			documents,
			document,
			workflowStates,
			issueLabels,
			users,
			viewer,
			teams,
			createIssue,
			createDocument,
			updateIssue,
			updateDocument,
			createWebhook,
			deleteWebhook,
			resolveTeamId,
			resolveProjectId,
		} satisfies LinearSdkApi
	}),
}) {}
