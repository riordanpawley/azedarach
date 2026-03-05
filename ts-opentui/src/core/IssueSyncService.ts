import { createHash } from "node:crypto"
import { execFileSync } from "node:child_process"
import type { Issue as LinearSdkIssue, LinearClient } from "@linear/sdk"
import { Config, Data, Effect, Option, Ref, Request, RequestResolver } from "effect"
import { AppConfig } from "../config/AppConfig.js"
import {
	LocalIssueStore,
	LocalIssueStoreError,
	type ExternalIssueSnapshot,
	type PendingSyncItem,
	type SyncOperation,
	type SyncQueueClaim,
} from "./LocalIssueStore.js"
import { LinearSdk } from "./LinearSdk.js"

const LINEAR_SYNC_TARGET: "linear" = "linear"
const MAX_SYNC_BATCH = 500
const MAX_SYNC_ATTEMPTS = 5
const BASE_RETRY_SECONDS = 5
const API_KEY_CACHE_TTL_MS = 30_000

type IssueStatus = "open" | "in_progress" | "blocked" | "closed" | "tombstone"
type IssueType = "bug" | "feature" | "task" | "epic" | "chore"

interface CollapsedSyncItem {
	readonly issueId: string
	readonly target: "linear"
	readonly operation: SyncOperation
	readonly claims: readonly SyncQueueClaim[]
	readonly maxQueueId: number
	readonly attempts: number
	readonly payloadJson: string | null
}

interface LinearRuntime {
	readonly client: LinearClient
	readonly defaultTeam: string | undefined
	readonly defaultProject: string | undefined
}

type ApiKeySource = "direnv" | "process-env" | "none"

interface ApiKeyCacheEntry {
	readonly apiKey: string | undefined
	readonly source: ApiKeySource
	readonly resolvedAtMs: number
}

type LinearIssuesQuery = NonNullable<Parameters<LinearClient["issues"]>[0]>
type LinearIssuesFilter = NonNullable<LinearIssuesQuery["filter"]>

class LinearSyncRequest extends Request.TaggedClass("LinearSyncRequest")<
	void,
	IssueSyncError,
	{
		readonly issueId: string
		readonly operation: SyncOperation
		readonly payloadJson: string | null
		readonly cwd: string | undefined
	}
> {}

export class IssueSyncError extends Data.TaggedError("IssueSyncError")<{
	readonly message: string
	readonly cause?: unknown
}> {}

const normalizeLinearStatus = (stateName: string | undefined): IssueStatus => {
	if (stateName === undefined) return "open"
	const normalized = stateName.trim().toLowerCase()

	if (
		normalized.includes("done") ||
		normalized.includes("complete") ||
		normalized.includes("cancel") ||
		normalized.includes("duplicate")
	) {
		return "closed"
	}

	if (normalized.includes("block")) {
		return "blocked"
	}

	if (
		normalized.includes("progress") ||
		normalized.includes("review") ||
		normalized.includes("started")
	) {
		return "in_progress"
	}

	return "open"
}

const normalizeLinearPriority = (priority: number | null | undefined): number => {
	if (priority == null) return 2
	if (priority <= 0) return 2
	if (priority === 1) return 0
	if (priority === 2) return 1
	if (priority === 3) return 2
	if (priority >= 4) return 3
	return 2
}

const toLinearPriority = (priority: number): number => {
	if (priority <= 0) return 1
	if (priority === 1) return 2
	if (priority === 2) return 3
	if (priority >= 3) return 4
	return 3
}

const inferIssueTypeFromLabels = (labels: readonly string[], hasChildren: boolean): IssueType => {
	const normalizedLabels = labels.map((label) => label.trim().toLowerCase())
	const hasLabel = (needle: string): boolean =>
		normalizedLabels.some((label) => label === needle || label === `type:${needle}`)

	if (hasLabel("bug")) return "bug"
	if (hasLabel("feature")) return "feature"
	if (hasLabel("chore")) return "chore"
	if (hasChildren || hasLabel("epic") || hasLabel("initiative")) return "epic"
	return "task"
}

const buildMergedDescription = (issue: {
	readonly description?: string
	readonly notes?: string
	readonly design?: string
	readonly acceptance?: string
}): string | undefined => {
	const sections: string[] = []
	if (issue.description && issue.description.trim().length > 0) {
		sections.push(issue.description)
	}
	if (issue.notes && issue.notes.trim().length > 0) {
		sections.push(`## Notes\n${issue.notes}`)
	}
	if (issue.design && issue.design.trim().length > 0) {
		sections.push(`## Design\n${issue.design}`)
	}
	if (issue.acceptance && issue.acceptance.trim().length > 0) {
		sections.push(`## Acceptance\n${issue.acceptance}`)
	}
	if (sections.length === 0) return undefined
	return sections.join("\n\n")
}

const isRecord = (value: unknown): value is Record<string, unknown> =>
	typeof value === "object" && value !== null

const parseSyncPayload = (payloadJson: string | null): { readonly idempotencyKey?: string } => {
	if (payloadJson === null) {
		return {}
	}

	try {
		const parsed = JSON.parse(payloadJson)
		if (!isRecord(parsed)) {
			return {}
		}
		const candidate = parsed.idempotencyKey
		if (typeof candidate !== "string") {
			return {}
		}
		const idempotencyKey = candidate.trim()
		return idempotencyKey.length > 0 ? { idempotencyKey } : {}
	} catch {
		return {}
	}
}

const formatUuidFromBytes = (bytes: Uint8Array): string => {
	const byteToHex = (value: number): string => value.toString(16).padStart(2, "0")
	const hex = Array.from(bytes, byteToHex).join("")
	return `${hex.slice(0, 8)}-${hex.slice(8, 12)}-${hex.slice(12, 16)}-${hex.slice(16, 20)}-${hex.slice(20, 32)}`
}

const deterministicLinearCreateId = (issueId: string, payloadJson: string | null): string => {
	const payload = parseSyncPayload(payloadJson)
	const seed = payload.idempotencyKey ?? `${LINEAR_SYNC_TARGET}:upsert:${issueId}`
	const digest = createHash("sha256").update(seed).digest()
	const bytes = Uint8Array.from(digest.subarray(0, 16))
	bytes[6] = (bytes[6] & 0x0f) | 0x40
	bytes[8] = (bytes[8] & 0x3f) | 0x80
	return formatUuidFromBytes(bytes)
}

const collapsePendingItems = (items: readonly PendingSyncItem[]): readonly CollapsedSyncItem[] => {
	const itemsByIssue = new Map<string, PendingSyncItem[]>()
	for (const item of items) {
		const grouped = itemsByIssue.get(item.issueId) ?? []
		grouped.push(item)
		itemsByIssue.set(item.issueId, grouped)
	}

	const collapsed: CollapsedSyncItem[] = []
	for (const [issueId, grouped] of itemsByIssue.entries()) {
		const sorted = [...grouped].sort((left, right) => left.id - right.id)
		const latest = sorted[sorted.length - 1]
		if (latest === undefined) {
			continue
		}
		let maxAttempts = 0
		const claims: SyncQueueClaim[] = []
		for (const item of sorted) {
			maxAttempts = Math.max(maxAttempts, item.attempts)
			claims.push({
				id: item.id,
				attemptToken: item.attemptToken,
			})
		}

		collapsed.push({
			issueId,
			target: latest.target,
			operation: latest.operation,
			claims,
			maxQueueId: latest.id,
			attempts: maxAttempts,
			payloadJson: latest.payloadJson,
		})
	}

	return collapsed
}

const toRetryDelaySeconds = (attempt: number): number => {
	const cappedAttempt = Math.max(0, Math.min(8, attempt))
	return BASE_RETRY_SECONDS * 2 ** cappedAttempt
}

const normalizeScopeValue = (value: string | undefined): string | undefined => {
	if (value === undefined) return undefined
	const trimmed = value.trim()
	return trimmed.length > 0 ? trimmed : undefined
}

interface LinearIssueScope {
	readonly team: string | undefined
	readonly project: string | undefined
}

const buildLinearIssueFilter = (scope: LinearIssueScope): LinearIssuesFilter | undefined => {
	const team = normalizeScopeValue(scope.team)
	const project = normalizeScopeValue(scope.project)
	const constraints: LinearIssuesFilter[] = []

	if (team !== undefined) {
		constraints.push({
			team: {
				or: [
					{ id: { eq: team } },
					{ key: { eqIgnoreCase: team } },
					{ name: { eqIgnoreCase: team } },
				],
			},
		})
	}

	if (project !== undefined) {
		constraints.push({
			project: {
				or: [
					{ id: { eq: project } },
					{ slugId: { eqIgnoreCase: project } },
					{ name: { eqIgnoreCase: project } },
				],
			},
		})
	}

	if (constraints.length === 0) return undefined
	if (constraints.length === 1) return constraints[0]
	return { and: constraints }
}

const getEffectiveProjectPath = (cwd: string | undefined): string => {
	if (cwd === undefined) return process.cwd()
	const trimmed = cwd.trim()
	return trimmed.length > 0 ? trimmed : process.cwd()
}

const readLinearApiKeyFromDirenv = (projectPath: string): string | undefined => {
	try {
		const envOutput = execFileSync("direnv", ["exec", projectPath, "env"], {
			encoding: "utf8",
			stdio: ["ignore", "pipe", "pipe"],
		})
		const keyPrefix = "LINEAR_API_KEY="
		const keyLine = envOutput
			.split(/\r?\n/)
			.find((line) => line.startsWith(keyPrefix))
		if (keyLine === undefined) {
			return undefined
		}
		const key = keyLine.slice(keyPrefix.length).trim()
		return key.length > 0 ? key : undefined
	} catch {
		return undefined
	}
}

export class IssueSyncService extends Effect.Service<IssueSyncService>()("IssueSyncService", {
	dependencies: [AppConfig.Default, LocalIssueStore.Default, LinearSdk.Default],
		effect: Effect.gen(function* () {
			const appConfig = yield* AppConfig
			const localStore = yield* LocalIssueStore
			const linearSdk = yield* LinearSdk
			const workflowStateCacheRef = yield* Ref.make<Map<string, string>>(new Map())
			const warnedMissingApiKeyRef = yield* Ref.make(false)
			const apiKeyCacheRef = yield* Ref.make<Map<string, ApiKeyCacheEntry>>(new Map())

		const mapLocalStoreError = (error: LocalIssueStoreError): IssueSyncError =>
			new IssueSyncError({
				message: error.message,
				cause: error.cause,
			})

		const fromStore = <A>(
			effect: Effect.Effect<A, LocalIssueStoreError>,
		): Effect.Effect<A, IssueSyncError> => effect.pipe(Effect.mapError(mapLocalStoreError))

			const requireTeamId = (
				teamId: string | undefined,
				message: string,
			): Effect.Effect<string, IssueSyncError> =>
				teamId !== undefined
					? Effect.succeed(teamId)
					: Effect.fail(
							new IssueSyncError({
								message,
							}),
						)

			const resolveLinearApiKey = (
				cwd: string | undefined,
			): Effect.Effect<Readonly<{ apiKey: string | undefined; source: ApiKeySource }>, IssueSyncError> =>
				Effect.gen(function* () {
					const projectPath = getEffectiveProjectPath(cwd)
					const now = Date.now()
					const cached = yield* Ref.get(apiKeyCacheRef).pipe(
						Effect.map((cache) => cache.get(projectPath)),
					)
					if (cached !== undefined && now - cached.resolvedAtMs < API_KEY_CACHE_TTL_MS) {
						return {
							apiKey: cached.apiKey,
							source: cached.source,
						}
					}

					const direnvApiKey = readLinearApiKeyFromDirenv(projectPath)
					if (direnvApiKey !== undefined) {
						yield* Ref.update(apiKeyCacheRef, (cache) => {
							const next = new Map(cache)
							next.set(projectPath, {
								apiKey: direnvApiKey,
								source: "direnv",
								resolvedAtMs: now,
							})
							return next
						})
						yield* Effect.log(
							`IssueSyncService: resolved LINEAR_API_KEY from direnv for projectPath=${projectPath}`,
						)
						return {
							apiKey: direnvApiKey,
							source: "direnv",
						}
					}

					const apiKeyFromEnv = yield* Config.option(Config.string("LINEAR_API_KEY")).pipe(
						Effect.mapError(
							(cause) =>
								new IssueSyncError({
									message: "Failed to resolve LINEAR_API_KEY",
								cause,
								}),
						),
					)
					const envApiKey = Option.isSome(apiKeyFromEnv) ? apiKeyFromEnv.value : undefined
					const source: ApiKeySource = envApiKey !== undefined ? "process-env" : "none"
					yield* Ref.update(apiKeyCacheRef, (cache) => {
						const next = new Map(cache)
						next.set(projectPath, {
							apiKey: envApiKey,
							source,
							resolvedAtMs: now,
						})
						return next
					})
					yield* Effect.log(
						`IssueSyncService: resolved LINEAR_API_KEY from ${source} for projectPath=${projectPath}`,
					)
					return {
						apiKey: envApiKey,
						source,
					}
				})

			const getLinearRuntime = (
				cwd?: string,
			): Effect.Effect<Option.Option<LinearRuntime>, IssueSyncError> =>
				Effect.gen(function* () {
					const config = yield* appConfig.getIssueTrackerSyncConfig()
					if (!("linear" in config.issueTracker)) {
						return Option.none()
					}

					const apiKeyResolution = yield* resolveLinearApiKey(cwd)
					const apiKeyOption =
						apiKeyResolution.apiKey !== undefined
							? Option.some(apiKeyResolution.apiKey)
							: Option.none<string>()

					if (Option.isNone(apiKeyOption)) {
						const warned = yield* Ref.get(warnedMissingApiKeyRef)
						if (!warned) {
							yield* Ref.set(warnedMissingApiKeyRef, true)
							yield* Effect.logWarning(
								`LINEAR_API_KEY not set for projectPath=${getEffectiveProjectPath(cwd)} (source=${apiKeyResolution.source}); linear sync is disabled while local tracking remains active.`,
							)
						}
						return Option.none()
					}

				const client = yield* linearSdk.getClient(apiKeyOption.value).pipe(
					Effect.mapError(
						(error) =>
							new IssueSyncError({
								message: error.message,
							}),
					),
				)
					return Option.some({
						client,
						defaultTeam: config.issueTracker.linear.team,
						defaultProject: config.issueTracker.linear.project,
					})
				})

			const fetchAllLinearIssues = (
				client: LinearClient,
				scope: LinearIssueScope,
			): Effect.Effect<readonly LinearSdkIssue[], IssueSyncError> =>
				Effect.tryPromise({
					try: async () => {
						const allIssues: LinearSdkIssue[] = []
						let afterCursor: string | undefined = undefined
						const filter = buildLinearIssueFilter(scope)
						while (true) {
							const page = await client.issues({
								first: 250,
								after: afterCursor,
								filter,
							})
							allIssues.push(...page.nodes)
							if (!page.pageInfo.hasNextPage || !page.pageInfo.endCursor) {
								break
						}
						afterCursor = page.pageInfo.endCursor
					}
					return allIssues
				},
				catch: (cause) =>
					new IssueSyncError({
						message: "Failed to fetch issues from Linear",
						cause,
					}),
			})

		const fetchLabelNameById = (
			client: LinearClient,
		): Effect.Effect<ReadonlyMap<string, string>, IssueSyncError> =>
			Effect.tryPromise({
				try: async () => {
					const labels = await client.issueLabels({ first: 500 })
					return new Map(labels.nodes.map((label) => [label.id, label.name] as const))
				},
				catch: (cause) =>
					new IssueSyncError({
						message: "Failed to fetch issue labels from Linear",
						cause,
					}),
			})

		const fetchStateNameById = (
			client: LinearClient,
		): Effect.Effect<ReadonlyMap<string, string>, IssueSyncError> =>
			Effect.tryPromise({
				try: async () => {
					const states = await client.workflowStates({ first: 500 })
					return new Map(states.nodes.map((state) => [state.id, state.name] as const))
				},
				catch: (cause) =>
					new IssueSyncError({
						message: "Failed to fetch workflow states from Linear",
						cause,
					}),
			})

		const resolveLabelIds = (
			client: LinearClient,
			labels: readonly string[],
		): Effect.Effect<readonly string[], IssueSyncError> =>
			Effect.gen(function* () {
				if (labels.length === 0) {
					return []
				}
				const labelNameById = yield* fetchLabelNameById(client)
				const normalized = labels.map((label) => label.trim().toLowerCase())
				const ids: string[] = []
				for (const [id, labelName] of labelNameById.entries()) {
					if (normalized.includes(labelName.trim().toLowerCase())) {
						ids.push(id)
					}
				}
				return ids
			})

		const findStateIdForStatus = (
			client: LinearClient,
			teamId: string,
			targetStatus: IssueStatus,
		): Effect.Effect<string, IssueSyncError> =>
			Effect.gen(function* () {
				const cacheKey = `${teamId}:${targetStatus}`
				const cached = yield* Ref.get(workflowStateCacheRef).pipe(
					Effect.map((cache) => cache.get(cacheKey)),
				)
				if (cached !== undefined) {
					return cached
				}

				const workflowStates = yield* Effect.tryPromise({
					try: async () => {
						const states = await client.workflowStates({ first: 500 })
						return states.nodes.map((state) => ({
							id: state.id,
							teamId: state.teamId,
							type: state.type,
							name: state.name,
						}))
					},
					catch: (cause) =>
						new IssueSyncError({
							message: "Failed to fetch workflow states from Linear",
							cause,
						}),
				})

				const teamStates = workflowStates.filter((state) => state.teamId === teamId)
				if (teamStates.length === 0) {
					return yield* Effect.fail(
						new IssueSyncError({
							message: `No workflow states available for team ${teamId}`,
						}),
					)
				}

				const findByType = (types: readonly string[]) =>
					teamStates.find((state) => types.includes(state.type))
				const findByName = (needle: string) =>
					teamStates.find((state) => state.name.toLowerCase().includes(needle))

				const selected =
					targetStatus === "closed"
						? (findByType(["completed"]) ?? findByType(["canceled"]) ?? teamStates[0])
						: targetStatus === "in_progress"
							? (findByType(["started"]) ?? findByName("progress") ?? teamStates[0])
							: targetStatus === "blocked"
								? (findByName("block") ?? findByType(["started"]) ?? teamStates[0])
								: (findByType(["unstarted", "backlog", "triage"]) ?? teamStates[0])

				yield* Ref.update(workflowStateCacheRef, (cache) => {
					const next = new Map(cache)
					next.set(cacheKey, selected.id)
					return next
				})

				return selected.id
			})

		const ensureParentExternalId = (
			parentLocalId: string | undefined,
			cwd: string | undefined,
		): Effect.Effect<string | undefined, IssueSyncError> =>
			Effect.gen(function* () {
				if (parentLocalId === undefined) return undefined
				const parentExternalRef = yield* fromStore(
					localStore.getExternalRef(parentLocalId, LINEAR_SYNC_TARGET, cwd),
				)
				return parentExternalRef?.externalId
			})

		const syncUpsert = (
			runtime: LinearRuntime,
			request: LinearSyncRequest,
		): Effect.Effect<void, IssueSyncError> =>
			Effect.gen(function* () {
				const issue = yield* fromStore(localStore.getIssueForSync(request.issueId, request.cwd))
				if (issue === undefined) {
					return
				}

				const externalRef = yield* fromStore(
					localStore.getExternalRef(issue.id, LINEAR_SYNC_TARGET, request.cwd),
				)
				const parentLocalId = issue.dependencies
					?.find((dependency) => dependency.dependency_type === "parent-child")
					?.id
				const parentId = yield* ensureParentExternalId(parentLocalId, request.cwd)
				const labelIds = yield* resolveLabelIds(runtime.client, issue.labels ?? [])
				const description = buildMergedDescription(issue)

				const applyStateId = (
					externalIssueId: string,
					teamId: string,
				): Effect.Effect<void, IssueSyncError> =>
					issue.status === "open"
						? Effect.void
						: findStateIdForStatus(runtime.client, teamId, issue.status).pipe(
								Effect.flatMap((stateId) =>
									Effect.tryPromise({
										try: () => runtime.client.updateIssue(externalIssueId, { stateId }),
										catch: (cause) =>
											new IssueSyncError({
												message: `Failed to apply status for ${issue.id}`,
												cause,
											}),
									}),
								),
							)

				if (externalRef !== undefined) {
					yield* Effect.tryPromise({
						try: () =>
							runtime.client.updateIssue(externalRef.externalId, {
								title: issue.title,
								description,
								priority: toLinearPriority(issue.priority),
								estimate: issue.estimate,
								labelIds: labelIds.length > 0 ? [...labelIds] : undefined,
								parentId,
							}),
						catch: (cause) =>
							new IssueSyncError({
								message: `Failed to update Linear issue for ${issue.id}`,
								cause,
							}),
					})

					const externalIssue = yield* Effect.tryPromise({
						try: () => runtime.client.issue(externalRef.externalId),
						catch: (cause) =>
							new IssueSyncError({
								message: `Failed to fetch Linear issue metadata for ${issue.id}`,
								cause,
							}),
					})
					const teamId = yield* requireTeamId(
						externalIssue.teamId,
						`Linear issue ${externalRef.externalId} is missing team id`,
					)
					yield* applyStateId(externalRef.externalId, teamId)
					return
				}

				const configuredTeam = runtime.defaultTeam?.trim()
				if (!configuredTeam) {
					return yield* Effect.fail(
						new IssueSyncError({
							message:
								"Linear sync requires issueTracker.linear.team when creating new external issues.",
						}),
					)
				}

				const createTeamId = yield* linearSdk.resolveTeamId(runtime.client, configuredTeam).pipe(
					Effect.mapError(
						(error) =>
							new IssueSyncError({
								message: error.message,
							}),
					),
				)
				const createExternalId = deterministicLinearCreateId(issue.id, request.payloadJson)

				const createIssueId = yield* Effect.tryPromise({
					try: () =>
						runtime.client.createIssue({
							id: createExternalId,
							teamId: createTeamId,
							title: issue.title,
							description,
							priority: toLinearPriority(issue.priority),
							estimate: issue.estimate,
							labelIds: labelIds.length > 0 ? [...labelIds] : undefined,
							parentId,
						}),
					catch: (cause) =>
						new IssueSyncError({
							message: `Failed to create Linear issue for ${issue.id}`,
							cause,
						}),
				}).pipe(
					Effect.flatMap((createPayload) =>
						createPayload.issueId
							? Effect.succeed(createPayload.issueId)
							: Effect.fail(
									new IssueSyncError({
										message: `Linear create returned no issue id for ${issue.id}`,
									}),
								),
					),
					Effect.catchAll((createError) =>
						Effect.tryPromise({
							try: () => runtime.client.issue(createExternalId),
							catch: () => createError,
						}).pipe(Effect.map((existingIssue) => existingIssue.id)),
					),
				)

				yield* fromStore(
					localStore.upsertExternalRef(
						{
							issueId: issue.id,
							target: LINEAR_SYNC_TARGET,
							externalId: createIssueId,
						},
						request.cwd,
					),
				)

				const createdLinearIssue = yield* Effect.tryPromise({
					try: () => runtime.client.issue(createIssueId),
					catch: (cause) =>
						new IssueSyncError({
							message: `Failed to fetch newly created Linear issue for ${issue.id}`,
							cause,
						}),
				})

				yield* fromStore(
					localStore.upsertExternalRef(
						{
							issueId: issue.id,
							target: LINEAR_SYNC_TARGET,
							externalId: createdLinearIssue.id,
							externalKey: createdLinearIssue.identifier,
						},
						request.cwd,
					),
				)
				const createdIssueTeamId = yield* requireTeamId(
					createdLinearIssue.teamId,
					`Linear issue ${createdLinearIssue.id} is missing team id`,
				)
				yield* applyStateId(createdLinearIssue.id, createdIssueTeamId)
			})

		const syncCloseOrDelete = (
			runtime: LinearRuntime,
			request: LinearSyncRequest,
		): Effect.Effect<void, IssueSyncError> =>
			Effect.gen(function* () {
				const externalRef = yield* fromStore(
					localStore.getExternalRef(request.issueId, LINEAR_SYNC_TARGET, request.cwd),
				)
				if (externalRef === undefined) {
					return
				}

				const externalIssue = yield* Effect.tryPromise({
					try: () => runtime.client.issue(externalRef.externalId),
					catch: (cause) =>
						new IssueSyncError({
							message: `Failed to fetch Linear issue metadata for ${request.issueId}`,
							cause,
						}),
				})

				const teamId = yield* requireTeamId(
					externalIssue.teamId,
					`Linear issue ${externalRef.externalId} is missing team id`,
				)
				const closedStateId = yield* findStateIdForStatus(runtime.client, teamId, "closed")
				yield* Effect.tryPromise({
					try: () => runtime.client.updateIssue(externalRef.externalId, { stateId: closedStateId }),
					catch: (cause) =>
						new IssueSyncError({
							message: `Failed to close Linear issue for ${request.issueId}`,
							cause,
						}),
				})
			})

		const linearResolver = (
			runtime: LinearRuntime,
		): RequestResolver.RequestResolver<LinearSyncRequest, never> =>
			RequestResolver.makeBatched<LinearSyncRequest, never>((requests) =>
				Effect.forEach(
					requests,
					(request) => {
						const runEffect =
							request.operation === "upsert"
								? syncUpsert(runtime, request)
								: syncCloseOrDelete(runtime, request)

						return runEffect.pipe(
							Effect.flatMap(() => Request.succeed(request, undefined)),
							Effect.catchAll((error) => Request.fail(request, error)),
						)
					},
					{ concurrency: "unbounded", discard: true },
				).pipe(Effect.asVoid),
			)

		const markRequestFailure = (
			item: CollapsedSyncItem,
			error: IssueSyncError,
			cwd: string | undefined,
		): Effect.Effect<void, IssueSyncError> => {
			const nextAttempts = item.attempts + 1
			const shouldTerminal = nextAttempts >= MAX_SYNC_ATTEMPTS
			if (shouldTerminal) {
				return fromStore(
					localStore.markSyncTerminalFailure(
						{
							claims: item.claims,
							errorMessage: error.message,
							nextAttempts,
						},
						cwd,
					),
				)
			}

			const delaySeconds = toRetryDelaySeconds(item.attempts)
			return fromStore(
				localStore.markSyncRetriable(
					{
						claims: item.claims,
						errorMessage: error.message,
						delaySeconds,
						nextAttempts,
					},
					cwd,
				),
			)
		}

			const buildBootstrapSnapshots = (
				client: LinearClient,
				scope: LinearIssueScope,
			): Effect.Effect<readonly ExternalIssueSnapshot[], IssueSyncError> =>
				Effect.gen(function* () {
					yield* Effect.log(
						`Linear bootstrap scope: team=${scope.team ?? "<none>"} project=${scope.project ?? "<none>"}`,
					)
					const emptyMetadataMap: ReadonlyMap<string, string> = new Map()
					const issues = yield* fetchAllLinearIssues(client, scope)
					const stateNameById = yield* fetchStateNameById(client).pipe(
						Effect.catchAll((error) =>
							Effect.logWarning(
								`Linear bootstrap: workflow states unavailable, continuing with fallback status mapping (${error.message})`,
							).pipe(Effect.as(emptyMetadataMap)),
						),
					)
					const labelNameById = yield* fetchLabelNameById(client).pipe(
						Effect.catchAll((error) =>
							Effect.logWarning(
								`Linear bootstrap: labels unavailable, continuing without label metadata (${error.message})`,
							).pipe(Effect.as(emptyMetadataMap)),
						),
					)

					const issuesById = new Map(issues.map((issue) => [issue.id, issue]))
					const childCountByParentId = new Map<string, number>()
					for (const issue of issues) {
						if (issue.parentId) {
							childCountByParentId.set(issue.parentId, (childCountByParentId.get(issue.parentId) ?? 0) + 1)
						}
					}

					return yield* Effect.forEach(
						issues,
						(issue) =>
							Effect.gen(function* () {
								const labels = issue.labelIds
									.map((labelId) => labelNameById.get(labelId))
									.filter((label): label is string => label !== undefined)
								const hasChildren = (childCountByParentId.get(issue.id) ?? 0) > 0
								const stateNameFromMap = issue.stateId ? stateNameById.get(issue.stateId) : undefined
								const issueState = issue.state
								const stateNameFromIssue =
									issueState === undefined
										? undefined
										: yield* Effect.tryPromise({
												try: () => issueState,
												catch: () => undefined,
											}).pipe(
												Effect.map((state) => state?.name),
												Effect.catchAll(() => Effect.succeed(undefined)),
											)
								const stateName =
									stateNameFromMap ?? stateNameFromIssue
								const status =
									stateName !== undefined
										? normalizeLinearStatus(stateName)
										: issue.completedAt != null || issue.canceledAt != null
											? "closed"
											: issue.startedAt != null
												? "in_progress"
											: "open"
								const parentLocalId = issue.parentId ? issuesById.get(issue.parentId)?.identifier : undefined

								return {
									localId: issue.identifier,
									externalId: issue.id,
									externalKey: issue.identifier,
									title: issue.title,
									description: issue.description ?? undefined,
									status,
									priority: normalizeLinearPriority(issue.priority),
									issueType: inferIssueTypeFromLabels(labels, hasChildren),
									createdAt: issue.createdAt.toISOString(),
									updatedAt: issue.updatedAt.toISOString(),
									closedAt:
										status === "closed"
											? (issue.completedAt ?? issue.canceledAt ?? issue.updatedAt).toISOString()
											: undefined,
									assignee: issue.assigneeId ?? null,
									labels,
									estimate: issue.estimate ?? undefined,
									parentLocalId,
								}
							}),
						{ concurrency: 16 },
					)
				})

			return {
				bootstrapLinear: (cwd?: string): Effect.Effect<number, IssueSyncError> =>
					Effect.gen(function* () {
						const runtimeOption = yield* getLinearRuntime(cwd)
						if (Option.isNone(runtimeOption)) {
							return 0
						}

					const bootstrapComplete = yield* fromStore(
						localStore.isBootstrapComplete(LINEAR_SYNC_TARGET, cwd),
					)
					if (bootstrapComplete) {
						return 0
					}

					const existingCount = yield* fromStore(localStore.countIssues(cwd))
					if (existingCount > 0) {
						yield* fromStore(localStore.markBootstrapComplete(LINEAR_SYNC_TARGET, cwd))
						return 0
					}

						const snapshots = yield* buildBootstrapSnapshots(runtimeOption.value.client, {
							team: runtimeOption.value.defaultTeam,
							project: runtimeOption.value.defaultProject,
						})
						yield* Effect.log(
							`Linear bootstrap imported ${snapshots.length} issues (team=${runtimeOption.value.defaultTeam ?? "<none>"} project=${runtimeOption.value.defaultProject ?? "<none>"})`,
						)
						const imported = yield* fromStore(
							localStore.importExternalSnapshot(LINEAR_SYNC_TARGET, snapshots, cwd),
						)
					yield* fromStore(localStore.markBootstrapComplete(LINEAR_SYNC_TARGET, cwd))
					return imported
				}),

				flushLinearQueue: (
					cwd?: string,
				): Effect.Effect<{ readonly pushed: number; readonly pulled: number }, IssueSyncError> =>
					Effect.gen(function* () {
						const runtimeOption = yield* getLinearRuntime(cwd)
						if (Option.isNone(runtimeOption)) {
							return { pushed: 0, pulled: 0 }
						}

					const pendingItems = yield* fromStore(
						localStore.listPendingSync(LINEAR_SYNC_TARGET, MAX_SYNC_BATCH, cwd),
					)
					if (pendingItems.length === 0) {
						return { pushed: 0, pulled: 0 }
					}

					const collapsed = collapsePendingItems(pendingItems)
					const resolver = linearResolver(runtimeOption.value)
					const pushedRef = yield* Ref.make(0)

					yield* Effect.forEach(
						collapsed,
						(item) =>
							Effect.request(
								new LinearSyncRequest({
									issueId: item.issueId,
									operation: item.operation,
									payloadJson: item.payloadJson,
									cwd,
								}),
								resolver,
							).pipe(
								Effect.flatMap(() =>
									fromStore(
										localStore.markSyncSucceeded(
											{
												issueId: item.issueId,
												target: item.target,
												maxQueueId: item.maxQueueId,
												claims: item.claims,
											},
											cwd,
										),
									).pipe(
										Effect.zipRight(
											Ref.update(pushedRef, (count) => count + item.claims.length),
										),
									),
								),
								Effect.catchAll((error) => markRequestFailure(item, error, cwd)),
							),
						{ concurrency: "unbounded" },
					)

					const pushed = yield* Ref.get(pushedRef)
					return { pushed, pulled: 0 }
				}).pipe(Effect.withRequestBatching(true)),
		}
	}),
}) {}
