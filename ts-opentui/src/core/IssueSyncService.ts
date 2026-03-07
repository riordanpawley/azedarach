import { createHash, randomUUID } from "node:crypto"
import { FileSystem, Path, PlatformConfigProvider } from "@effect/platform"
import type { LinearClient, Issue as LinearSdkIssue } from "@linear/sdk"
import {
	Config,
	ConfigProvider,
	Data,
	Deferred,
	Effect,
	Option,
	Ref,
	Request,
	RequestResolver,
	Schedule,
} from "effect"
import { AppConfig } from "../config/AppConfig.js"
import {
	DiagnosticsService,
	type IssueSyncFailure,
	type IssueSyncQueueHealth,
	type IssueSyncRunHealth,
	type IssueSyncRuntimeHealth,
	type IssueSyncRuntimeReason,
	type IssueSyncHealth,
} from "../services/DiagnosticsService.js"
import { LinearSdk } from "./LinearSdk.js"
import {
	type ExternalIssueSnapshot,
	LocalIssueStore,
	type LocalIssueStoreError,
	type PendingSyncItem,
	type SyncQueueSummary,
	type SyncOperation,
	type SyncQueueClaim,
} from "./LocalIssueStore.js"

const LINEAR_SYNC_TARGET: "linear" = "linear"
const MAX_SYNC_BATCH = 500
const MAX_SYNC_ATTEMPTS = 5
const BASE_RETRY_SECONDS = 5
const LINEAR_WORKFLOW_STATES_PAGE_SIZE = 250
const LINEAR_LABELS_PAGE_SIZE = 250
const API_KEY_CACHE_TTL_MS = 30_000
const BOOTSTRAP_FETCH_RETRY_ATTEMPTS = 3
const BOOTSTRAP_FETCH_RETRY_DELAY = "500 millis"
const LINEAR_REMOTE_HYDRATION_MIN_INTERVAL_MS = 5_000

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
	readonly defaultTeam: string | undefined
	readonly defaultProject: string | undefined
}

type ApiKeySource = "config-provider" | "none"

interface ApiKeyCacheEntry {
	readonly apiKey: string | undefined
	readonly source: ApiKeySource
	readonly resolvedAtMs: number
}

type LinearIssuesQuery = NonNullable<Parameters<LinearClient["issues"]>[0]>
type LinearIssuesFilter = NonNullable<LinearIssuesQuery["filter"]>

type RuntimeUnavailableReason = Exclude<IssueSyncRuntimeReason, "ready">

interface SyncRunStart {
	readonly runId: string
	readonly operation: "bootstrap" | "flush"
	readonly startedAt: Date
}

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

interface LinearWorkflowState {
	readonly id: string
	readonly name: string
	readonly teamId: string | undefined
	readonly type: string
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

const formatSyncQueueSummary = (summary: SyncQueueSummary): string =>
	`queueTotal=${summary.total} pendingReady=${summary.pendingReady} pendingDelayed=${summary.pendingDelayed} processingActive=${summary.processingActive} processingStale=${summary.processingStale} failed=${summary.failed}`

const formatApiKeyFingerprint = (apiKey: string | undefined): string => {
	if (apiKey === undefined) {
		return "missing"
	}
	const digest = createHash("sha256").update(apiKey).digest("hex").slice(0, 10)
	return `set(len=${apiKey.length},sha256=${digest})`
}

export class IssueSyncService extends Effect.Service<IssueSyncService>()("IssueSyncService", {
	dependencies: [
		AppConfig.Default,
		LocalIssueStore.Default,
		LinearSdk.Default,
		DiagnosticsService.Default,
	],
	effect: Effect.gen(function* () {
		const appConfig = yield* AppConfig
		const localStore = yield* LocalIssueStore
		const linearSdk = yield* LinearSdk
		const diagnostics = yield* DiagnosticsService
		const fs = yield* FileSystem.FileSystem
		const pathService = yield* Path.Path
		const workflowStateCacheRef = yield* Ref.make<Map<string, string>>(new Map())
		const viewerIdCacheRef = yield* Ref.make<Map<string, string>>(new Map())
		const warnedMissingApiKeyRef = yield* Ref.make(false)
		const apiKeyCacheRef = yield* Ref.make<Map<string, ApiKeyCacheEntry>>(new Map())
		const projectIdCacheRef = yield* Ref.make<Map<string, string>>(new Map())
		const syncFailureCountRef = yield* Ref.make(0)
		const lastSyncedAtRef = yield* Ref.make<Date | undefined>(undefined)
		const lastFailureRef = yield* Ref.make<IssueSyncFailure | undefined>(undefined)
		const runtimeHealthRef = yield* Ref.make<IssueSyncRuntimeHealth | undefined>(undefined)
		const queueHealthRef = yield* Ref.make<IssueSyncQueueHealth | undefined>(undefined)
		const lastRunHealthRef = yield* Ref.make<IssueSyncRunHealth | undefined>(undefined)
		const bootstrapInFlightRef = yield* Ref.make<
			Map<string, Deferred.Deferred<number, IssueSyncError>>
		>(new Map())
		const flushInFlightRef = yield* Ref.make<
			Map<
				string,
				Deferred.Deferred<{ readonly pushed: number; readonly pulled: number }, IssueSyncError>
			>
		>(new Map())
		const lastRemoteHydrationAtRef = yield* Ref.make<Map<string, number>>(new Map())

		const mapLocalStoreError = (error: LocalIssueStoreError): IssueSyncError =>
			new IssueSyncError({
				message: error.message,
				cause: error.cause,
			})

		const fromStore = <A>(
			effect: Effect.Effect<A, LocalIssueStoreError>,
		): Effect.Effect<A, IssueSyncError> => effect.pipe(Effect.mapError(mapLocalStoreError))

		const createRunHealth = (params: {
			readonly run: SyncRunStart
			readonly status: Exclude<IssueSyncHealth["lastStatus"], "idle">
			readonly message: string
			readonly pushed: number
			readonly pulled: number
		}): IssueSyncRunHealth => ({
			runId: params.run.runId,
			operation: params.run.operation,
			status: params.status,
			startedAt: params.run.startedAt,
			finishedAt: new Date(),
			message: params.message,
			pushed: params.pushed,
			pulled: params.pulled,
		})

		const setRuntimeHealthReady = (params: {
			readonly projectPath: string
			readonly configuredTeam: string | undefined
			readonly configuredProject: string | undefined
			readonly apiKeySource: ApiKeySource
		}): Effect.Effect<void, never> =>
			Ref.set(runtimeHealthRef, {
				status: "ready",
				reason: "ready",
				projectPath: params.projectPath,
				configuredTeam: params.configuredTeam,
				configuredProject: params.configuredProject,
				apiKeySource: params.apiKeySource,
				updatedAt: new Date(),
			})

		const setRuntimeHealthUnavailable = (params: {
			readonly projectPath: string
			readonly configuredTeam: string | undefined
			readonly configuredProject: string | undefined
			readonly reason: RuntimeUnavailableReason
			readonly apiKeySource?: ApiKeySource
		}): Effect.Effect<void, never> =>
			Ref.set(runtimeHealthRef, {
				status: "unavailable",
				reason: params.reason,
				projectPath: params.projectPath,
				configuredTeam: params.configuredTeam,
				configuredProject: params.configuredProject,
				apiKeySource: params.apiKeySource ?? "unknown",
				updatedAt: new Date(),
			})

		const setQueueHealth = (summary: SyncQueueSummary): Effect.Effect<void, never> =>
			Ref.set(queueHealthRef, {
				total: summary.total,
				pendingReady: summary.pendingReady,
				pendingDelayed: summary.pendingDelayed,
				processingActive: summary.processingActive,
				processingStale: summary.processingStale,
				failed: summary.failed,
				updatedAt: new Date(),
			})

		const reportSyncHealth = (params: {
			readonly status: IssueSyncHealth["lastStatus"]
			readonly message: string
			readonly queueDepth: number
			readonly failure?: IssueSyncFailure
			readonly run?: IssueSyncRunHealth
		}): Effect.Effect<void, never> =>
			Effect.gen(function* () {
				const config = yield* appConfig.getIssueTrackerSyncConfig().pipe(
					Effect.tapError((error) =>
						Effect.logWarning(`Recovering from error before fallback: ${String(error)}`),
					),
					Effect.orElseSucceed(() => undefined),
				)
				const backend = config !== undefined && "linear" in config.issueTracker ? "linear" : "none"
				const syncEnabled =
					config !== undefined && "linear" in config.issueTracker
						? config.issueTracker.linear.syncEnabled
						: false

				if (params.status === "success") {
					yield* Ref.set(lastSyncedAtRef, new Date())
					yield* Ref.set(lastFailureRef, undefined)
				}
				if (params.failure !== undefined) {
					yield* Ref.update(syncFailureCountRef, (count) => count + 1)
					yield* Ref.set(lastFailureRef, params.failure)
				}
				if (params.run !== undefined) {
					yield* Ref.set(lastRunHealthRef, params.run)
				}

				const failedCount = yield* Ref.get(syncFailureCountRef)
				const lastSyncedAt = yield* Ref.get(lastSyncedAtRef)
				const lastFailure = yield* Ref.get(lastFailureRef)
				const runtime = yield* Ref.get(runtimeHealthRef)
				const queue = yield* Ref.get(queueHealthRef)
				const lastRun = yield* Ref.get(lastRunHealthRef)

				yield* diagnostics
					.setIssueSyncHealth({
						backend,
						syncEnabled,
						queueDepth: params.queueDepth,
						failedCount,
						lastSyncedAt,
						lastStatus: params.status,
						lastMessage: params.message,
						lastFailure,
						runtime,
						queue,
						lastRun,
					})
					.pipe(
						Effect.tapError((error) =>
							Effect.logWarning(`Recovering from error before fallback: ${String(error)}`),
						),
						Effect.orElseSucceed(() => void 0),
					)
			})

		const resolveDotEnvProvider = (
			path: string,
		): Effect.Effect<Option.Option<ConfigProvider.ConfigProvider>, never> =>
			Effect.gen(function* () {
				const exists = yield* fs
					.exists(path)
					.pipe(
						Effect.catchAll((error) =>
							Effect.logWarning(`Recovering after caught error: ${String(error)}`).pipe(
								Effect.zipRight(Effect.succeed(false)),
							),
						),
					)
				if (!exists) {
					return Option.none()
				}

				return yield* PlatformConfigProvider.fromDotEnv(path).pipe(
					Effect.provideService(FileSystem.FileSystem, fs),
					Effect.map(Option.some),
					Effect.catchAll((error) =>
						Effect.logWarning(
							`IssueSyncService: failed to load dotenv provider path=${path}; continuing with env fallback. cause=${String(error)}`,
						).pipe(Effect.as(Option.none<ConfigProvider.ConfigProvider>())),
					),
				)
			})

		const resolveProjectConfigProviders = (
			projectPath: string,
		): Effect.Effect<
			{
				readonly provider: ConfigProvider.ConfigProvider
				readonly dotEnvProviderOption: Option.Option<ConfigProvider.ConfigProvider>
				readonly dotEnvLocalProviderOption: Option.Option<ConfigProvider.ConfigProvider>
				readonly envProvider: ConfigProvider.ConfigProvider
			},
			never
		> =>
			Effect.gen(function* () {
				const dotEnvPath = pathService.join(projectPath, ".env")
				const dotEnvLocalPath = pathService.join(projectPath, ".env.local")
				const envProvider = ConfigProvider.fromEnv()
				const dotEnvProviderOption = yield* resolveDotEnvProvider(dotEnvPath)
				const dotEnvLocalProviderOption = yield* resolveDotEnvProvider(dotEnvLocalPath)

				const dotenvChain = Option.match(dotEnvProviderOption, {
					onNone: () => dotEnvLocalProviderOption,
					onSome: (dotEnvProvider) =>
						Option.match(dotEnvLocalProviderOption, {
							onNone: () => Option.some(dotEnvProvider),
							onSome: (dotEnvLocalProvider) =>
								Option.some(ConfigProvider.orElse(dotEnvLocalProvider, () => dotEnvProvider)),
						}),
				})

				const provider = Option.match(dotenvChain, {
					onNone: () => envProvider,
					onSome: (dotenvProvider) => ConfigProvider.orElse(dotenvProvider, () => envProvider),
				})

				yield* Effect.log(
					`IssueSyncService: resolved config provider for projectPath=${projectPath} usingDotEnv=${Option.isSome(dotenvChain)}`,
				)

				return {
					provider,
					dotEnvProviderOption,
					dotEnvLocalProviderOption,
					envProvider,
				}
			})

		const readLinearApiKeyFromProvider = (
			provider: ConfigProvider.ConfigProvider,
		): Effect.Effect<string | undefined, IssueSyncError> =>
			Config.option(Config.string("LINEAR_API_KEY")).pipe(
				Effect.withConfigProvider(provider),
				Effect.map((apiKeyOption) =>
					Option.isSome(apiKeyOption) ? apiKeyOption.value : undefined,
				),
				Effect.mapError(
					(cause) =>
						new IssueSyncError({
							message: "Failed to resolve LINEAR_API_KEY",
							cause,
						}),
				),
			)

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
		): Effect.Effect<
			Readonly<{ apiKey: string | undefined; source: ApiKeySource }>,
			IssueSyncError
		> =>
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

				const providers = yield* resolveProjectConfigProviders(projectPath)
				const resolvedApiKey = yield* readLinearApiKeyFromProvider(providers.provider)
				const dotEnvLocalApiKey = yield* Option.match(providers.dotEnvLocalProviderOption, {
					onNone: () => Effect.succeed(undefined),
					onSome: readLinearApiKeyFromProvider,
				})
				const dotEnvApiKey = yield* Option.match(providers.dotEnvProviderOption, {
					onNone: () => Effect.succeed(undefined),
					onSome: readLinearApiKeyFromProvider,
				})
				const envApiKey = yield* readLinearApiKeyFromProvider(providers.envProvider)
				const source: ApiKeySource = resolvedApiKey !== undefined ? "config-provider" : "none"
				yield* Effect.log(
					`IssueSyncService: LINEAR_API_KEY diagnostics projectPath=${projectPath} source=${source} .env.local=${formatApiKeyFingerprint(dotEnvLocalApiKey)} .env=${formatApiKeyFingerprint(dotEnvApiKey)} env=${formatApiKeyFingerprint(envApiKey)} selected=${formatApiKeyFingerprint(resolvedApiKey)}`,
				)
				yield* Ref.update(apiKeyCacheRef, (cache) => {
					const next = new Map(cache)
					next.set(projectPath, {
						apiKey: resolvedApiKey,
						source,
						resolvedAtMs: now,
					})
					return next
				})
				yield* Effect.log(
					`IssueSyncService: resolved LINEAR_API_KEY from ${source} for projectPath=${projectPath}`,
				)
				return {
					apiKey: resolvedApiKey,
					source,
				}
			})

		const getLinearRuntime = (
			cwd?: string,
		): Effect.Effect<Option.Option<LinearRuntime>, IssueSyncError> =>
			Effect.gen(function* () {
				const projectPath = getEffectiveProjectPath(cwd)
				const config = yield* appConfig.getIssueTrackerSyncConfig().pipe(
					Effect.tapError(() =>
						setRuntimeHealthUnavailable({
							projectPath,
							configuredTeam: undefined,
							configuredProject: undefined,
							reason: "config_error",
						}),
					),
				)
				if (!("linear" in config.issueTracker)) {
					yield* Effect.log(
						`Linear sync runtime unavailable: projectPath=${projectPath} reason=backend_not_linear`,
					)
					yield* setRuntimeHealthUnavailable({
						projectPath,
						configuredTeam: undefined,
						configuredProject: undefined,
						reason: "backend_not_linear",
					})
					return Option.none()
				}
				if (!config.issueTracker.linear.syncEnabled) {
					yield* Effect.log(
						`Linear sync runtime unavailable: projectPath=${projectPath} reason=sync_disabled`,
					)
					yield* setRuntimeHealthUnavailable({
						projectPath,
						configuredTeam: config.issueTracker.linear.team,
						configuredProject: config.issueTracker.linear.project,
						reason: "sync_disabled",
					})
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
							`LINEAR_API_KEY not set for projectPath=${projectPath} (source=${apiKeyResolution.source}); linear sync is disabled while local tracking remains active.`,
						)
					}
					yield* setRuntimeHealthUnavailable({
						projectPath,
						configuredTeam: config.issueTracker.linear.team,
						configuredProject: config.issueTracker.linear.project,
						reason: "missing_api_key",
						apiKeySource: apiKeyResolution.source,
					})
					return Option.none()
				}

				const runtime: LinearRuntime = {
					defaultTeam: config.issueTracker.linear.team,
					defaultProject: config.issueTracker.linear.project,
				}
				yield* setRuntimeHealthReady({
					projectPath,
					configuredTeam: runtime.defaultTeam,
					configuredProject: runtime.defaultProject,
					apiKeySource: apiKeyResolution.source,
				})
				yield* Effect.log(
					`Linear sync runtime ready: projectPath=${projectPath} team=${runtime.defaultTeam ?? "<none>"} project=${runtime.defaultProject ?? "<none>"}`,
				)
				return Option.some(runtime)
			})

		const resolveConfiguredProjectId = (
			configuredProject: string | undefined,
			issueId: string,
			cwd: string | undefined,
		): Effect.Effect<string | undefined, IssueSyncError> =>
			Effect.gen(function* () {
				const normalizedProject = normalizeScopeValue(configuredProject)
				if (normalizedProject === undefined) {
					return undefined
				}

				const cacheKey = normalizedProject.toLowerCase()
				const cachedProjectId = yield* Ref.get(projectIdCacheRef).pipe(
					Effect.map((cache) => cache.get(cacheKey)),
				)
				if (cachedProjectId !== undefined) {
					return cachedProjectId
				}

				const resolvedProjectId = yield* linearSdk.resolveProjectId(normalizedProject).pipe(
					Effect.mapError(
						(error) =>
							new IssueSyncError({
								message: `Failed to resolve configured Linear project '${normalizedProject}' for issue ${issueId}: ${error.message}`,
							}),
					),
				)
				yield* Ref.update(projectIdCacheRef, (cache) => {
					const next = new Map(cache)
					next.set(cacheKey, resolvedProjectId)
					return next
				})
				yield* Effect.log(
					`Linear sync project resolved: issue=${issueId} configuredProject=${normalizedProject} projectId=${resolvedProjectId} projectPath=${getEffectiveProjectPath(cwd)}`,
				)
				return resolvedProjectId
			})

		const resolveViewerIdForProject = (
			projectPath: string,
		): Effect.Effect<string, IssueSyncError> =>
			Effect.gen(function* () {
				const cachedViewerId = yield* Ref.get(viewerIdCacheRef).pipe(
					Effect.map((cache) => cache.get(projectPath)),
				)
				if (cachedViewerId !== undefined) {
					return cachedViewerId
				}

				const viewer = yield* linearSdk.viewer().pipe(
					Effect.mapError(
						(error) =>
							new IssueSyncError({
								message: `Failed to resolve Linear viewer for projectPath=${projectPath}: ${error.message}`,
							}),
					),
				)
				const viewerId = viewer.id.trim()
				if (viewerId.length === 0) {
					return yield* Effect.fail(
						new IssueSyncError({
							message: `Linear viewer id was empty for projectPath=${projectPath}`,
						}),
					)
				}

				yield* Ref.update(viewerIdCacheRef, (cache) => {
					const next = new Map(cache)
					next.set(projectPath, viewerId)
					return next
				})
				return viewerId
			})

		const fetchAllLinearIssues = (
			scope: LinearIssueScope,
		): Effect.Effect<readonly LinearSdkIssue[], IssueSyncError> =>
			Effect.gen(function* () {
				const filter = buildLinearIssueFilter(scope)
				const fetchIssuesPage = (afterCursor: string | undefined) =>
					linearSdk
						.issues({
							first: 250,
							after: afterCursor,
							filter,
						})
						.pipe(
							Effect.mapError(
								(error) =>
									new IssueSyncError({
										message: error.message,
									}),
							),
						)

				const collectPages = (
					afterCursor: string | undefined,
					accumulator: readonly LinearSdkIssue[],
				): Effect.Effect<readonly LinearSdkIssue[], IssueSyncError> =>
					fetchIssuesPage(afterCursor).pipe(
						Effect.flatMap((page) => {
							const nextAccumulator = [...accumulator, ...page.nodes]
							if (!page.pageInfo.hasNextPage || !page.pageInfo.endCursor) {
								return Effect.succeed(nextAccumulator)
							}
							return collectPages(page.pageInfo.endCursor, nextAccumulator)
						}),
					)

				return yield* collectPages(undefined, [])
			})

		const fetchLabelNameById = (): Effect.Effect<ReadonlyMap<string, string>, IssueSyncError> =>
			Effect.gen(function* () {
				const fetchLabelsPage = (afterCursor: string | undefined) =>
					linearSdk
						.issueLabels({
							first: LINEAR_LABELS_PAGE_SIZE,
							after: afterCursor,
						})
						.pipe(
							Effect.mapError(
								(error) =>
									new IssueSyncError({
										message: error.message,
									}),
							),
						)

				const collectPages = (
					afterCursor: string | undefined,
					accumulator: ReadonlyMap<string, string>,
				): Effect.Effect<ReadonlyMap<string, string>, IssueSyncError> =>
					fetchLabelsPage(afterCursor).pipe(
						Effect.flatMap((page) => {
							const nextAccumulator = new Map(accumulator)
							for (const label of page.nodes) {
								nextAccumulator.set(label.id, label.name)
							}

							if (!page.pageInfo.hasNextPage || !page.pageInfo.endCursor) {
								return Effect.succeed(nextAccumulator)
							}
							return collectPages(page.pageInfo.endCursor, nextAccumulator)
						}),
					)

				return yield* collectPages(undefined, new Map())
			})

		const fetchWorkflowStates = (): Effect.Effect<
			readonly LinearWorkflowState[],
			IssueSyncError
		> =>
			Effect.gen(function* () {
				const fetchWorkflowStatesPage = (afterCursor: string | undefined) =>
					linearSdk
						.workflowStates({
							first: LINEAR_WORKFLOW_STATES_PAGE_SIZE,
							after: afterCursor,
						})
						.pipe(
							Effect.mapError(
								(error) =>
									new IssueSyncError({
										message: error.message,
									}),
							),
						)

				const collectPages = (
					afterCursor: string | undefined,
					accumulator: readonly LinearWorkflowState[],
				): Effect.Effect<readonly LinearWorkflowState[], IssueSyncError> =>
					fetchWorkflowStatesPage(afterCursor).pipe(
						Effect.flatMap((page) => {
							const nextAccumulator: readonly LinearWorkflowState[] = [
								...accumulator,
								...page.nodes.map((state) => ({
									id: state.id,
									name: state.name,
									teamId: state.teamId,
									type: state.type,
								})),
							]
							if (!page.pageInfo.hasNextPage || !page.pageInfo.endCursor) {
								return Effect.succeed(nextAccumulator)
							}
							return collectPages(page.pageInfo.endCursor, nextAccumulator)
						}),
					)

				return yield* collectPages(undefined, [])
			})

		const fetchStateNameById = (): Effect.Effect<ReadonlyMap<string, string>, IssueSyncError> =>
			fetchWorkflowStates().pipe(
				Effect.map((workflowStates) =>
					new Map(workflowStates.map((state) => [state.id, state.name] as const)),
				),
			)

		const resolveLabelIds = (
			labels: readonly string[],
		): Effect.Effect<readonly string[], IssueSyncError> =>
			Effect.gen(function* () {
				if (labels.length === 0) {
					return []
				}
				const labelNameById = yield* fetchLabelNameById()
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

				const workflowStates = yield* fetchWorkflowStates()

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
				const projectPath = getEffectiveProjectPath(request.cwd)
				const configuredTeam = normalizeScopeValue(runtime.defaultTeam)
				const configuredProject = normalizeScopeValue(runtime.defaultProject)
				yield* Effect.log(
					`Linear sync upsert start: issue=${request.issueId} projectPath=${projectPath} configuredTeam=${configuredTeam ?? "<none>"} configuredProject=${configuredProject ?? "<none>"}`,
				)
				const issue = yield* fromStore(localStore.getIssueForSync(request.issueId, request.cwd))
				if (issue === undefined) {
					yield* Effect.logWarning(
						`Linear sync upsert skipped: issue=${request.issueId} projectPath=${projectPath} reason=local_issue_missing`,
					)
					return
				}
				const projectId = yield* resolveConfiguredProjectId(
					configuredProject,
					issue.id,
					request.cwd,
				)

				const externalRef = yield* fromStore(
					localStore.getExternalRef(issue.id, LINEAR_SYNC_TARGET, request.cwd),
				)
				const parentLocalId = issue.dependencies?.find(
					(dependency) => dependency.dependency_type === "parent-child",
				)?.id
				const parentId = yield* ensureParentExternalId(parentLocalId, request.cwd)
				const labelIds = yield* resolveLabelIds(issue.labels ?? [])
				const description = buildMergedDescription(issue)
				const assigneeId =
					issue.status === "in_progress"
						? yield* resolveViewerIdForProject(projectPath).pipe(
								Effect.tap((viewerId) =>
									Effect.log(
										`Linear sync upsert auto-assign resolved: issue=${issue.id} assigneeId=${viewerId} projectPath=${projectPath}`,
									),
								),
								Effect.catchAll((error) =>
									Effect.logWarning(
										`Linear sync upsert auto-assign skipped: issue=${issue.id} projectPath=${projectPath} reason=${error.message}`,
									).pipe(Effect.as(undefined)),
								),
							)
						: undefined

				const applyStateId = (
					externalIssueId: string,
					teamId: string,
				): Effect.Effect<void, IssueSyncError> =>
					issue.status === "open"
						? Effect.void
						: findStateIdForStatus(teamId, issue.status).pipe(
								Effect.flatMap((stateId) =>
									linearSdk.updateIssue(externalIssueId, { stateId }).pipe(
										Effect.asVoid,
										Effect.mapError(
											(error) =>
												new IssueSyncError({
													message: `Failed to apply status for ${issue.id}: ${error.message}`,
												}),
										),
									),
								),
							)

				if (externalRef !== undefined) {
					yield* Effect.log(
						`Linear sync upsert update: issue=${issue.id} externalId=${externalRef.externalId} projectPath=${projectPath} status=${issue.status} labels=${labelIds.length} parentLinked=${parentId !== undefined} projectId=${projectId ?? "<none>"}`,
					)
					yield* linearSdk
						.updateIssue(externalRef.externalId, {
							title: issue.title,
							description,
							priority: toLinearPriority(issue.priority),
							estimate: issue.estimate,
							labelIds: labelIds.length > 0 ? [...labelIds] : undefined,
							parentId,
							projectId,
							assigneeId,
						})
						.pipe(
							Effect.asVoid,
							Effect.mapError(
								(error) =>
									new IssueSyncError({
										message: `Failed to update Linear issue for ${issue.id}: ${error.message}`,
									}),
							),
						)

					const externalIssue = yield* linearSdk.issue(externalRef.externalId).pipe(
						Effect.mapError(
							(error) =>
								new IssueSyncError({
									message: `Failed to fetch Linear issue metadata for ${issue.id}: ${error.message}`,
								}),
						),
					)
					const teamId = yield* requireTeamId(
						externalIssue.teamId,
						`Linear issue ${externalRef.externalId} is missing team id`,
					)
					yield* applyStateId(externalRef.externalId, teamId)
					yield* Effect.log(
						`Linear sync upsert update complete: issue=${issue.id} externalId=${externalRef.externalId} projectPath=${projectPath}`,
					)
					return
				}

				if (!configuredTeam) {
					yield* Effect.logWarning(
						`Linear sync upsert failed: issue=${issue.id} projectPath=${projectPath} reason=missing_configured_team`,
					)
					return yield* Effect.fail(
						new IssueSyncError({
							message:
								"Linear sync requires issueTracker.linear.team when creating new external issues.",
						}),
					)
				}

				const createTeamId = yield* linearSdk.resolveTeamId(configuredTeam).pipe(
					Effect.mapError(
						(error) =>
							new IssueSyncError({
								message: error.message,
							}),
					),
				)
				const createExternalId = deterministicLinearCreateId(issue.id, request.payloadJson)
				yield* Effect.log(
					`Linear sync upsert create: issue=${issue.id} projectPath=${projectPath} configuredTeam=${configuredTeam} configuredProject=${configuredProject ?? "<none>"} projectId=${projectId ?? "<none>"} createExternalId=${createExternalId}`,
				)

				const createIssueId = yield* linearSdk
					.createIssue({
						id: createExternalId,
						teamId: createTeamId,
						title: issue.title,
						description,
						priority: toLinearPriority(issue.priority),
						estimate: issue.estimate,
						labelIds: labelIds.length > 0 ? [...labelIds] : undefined,
						parentId,
						projectId,
						assigneeId,
					})
					.pipe(
						Effect.mapError(
							(error) =>
								new IssueSyncError({
									message: `Failed to create Linear issue for ${issue.id}: ${error.message}`,
								}),
						),
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
							Effect.logWarning(createError).pipe(
								Effect.zipRight(
									linearSdk.issue(createExternalId).pipe(
										Effect.map((existingIssue) => existingIssue.id),
										Effect.mapError(() => createError),
									),
								),
							),
						),
					)
				yield* Effect.log(
					`Linear sync upsert create resolved id: issue=${issue.id} externalId=${createIssueId} projectPath=${projectPath}`,
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

				const createdLinearIssue = yield* linearSdk.issue(createIssueId).pipe(
					Effect.mapError(
						(error) =>
							new IssueSyncError({
								message: `Failed to fetch newly created Linear issue for ${issue.id}: ${error.message}`,
							}),
					),
				)

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
				yield* Effect.log(
					`Linear sync upsert create complete: issue=${issue.id} externalId=${createdLinearIssue.id} externalKey=${createdLinearIssue.identifier} projectPath=${projectPath}`,
				)
			})

		const syncCloseOrDelete = (
			runtime: LinearRuntime,
			request: LinearSyncRequest,
		): Effect.Effect<void, IssueSyncError> =>
			Effect.gen(function* () {
				void runtime
				const projectPath = getEffectiveProjectPath(request.cwd)
				yield* Effect.log(
					`Linear sync ${request.operation} start: issue=${request.issueId} projectPath=${projectPath}`,
				)
				const externalRef = yield* fromStore(
					localStore.getExternalRef(request.issueId, LINEAR_SYNC_TARGET, request.cwd),
				)
				if (externalRef === undefined) {
					yield* Effect.log(
						`Linear sync ${request.operation} skipped: issue=${request.issueId} projectPath=${projectPath} reason=no_external_ref`,
					)
					return
				}

				const externalIssue = yield* linearSdk.issue(externalRef.externalId).pipe(
					Effect.mapError(
						(error) =>
							new IssueSyncError({
								message: `Failed to fetch Linear issue metadata for ${request.issueId}: ${error.message}`,
							}),
					),
				)

				const teamId = yield* requireTeamId(
					externalIssue.teamId,
					`Linear issue ${externalRef.externalId} is missing team id`,
				)
				const closedStateId = yield* findStateIdForStatus(teamId, "closed")
				yield* linearSdk.updateIssue(externalRef.externalId, { stateId: closedStateId }).pipe(
					Effect.asVoid,
					Effect.mapError(
						(error) =>
							new IssueSyncError({
								message: `Failed to close Linear issue for ${request.issueId}: ${error.message}`,
							}),
					),
				)
				yield* Effect.log(
					`Linear sync ${request.operation} complete: issue=${request.issueId} externalId=${externalRef.externalId} projectPath=${projectPath}`,
				)
			})

		const linearResolver = (
			runtime: LinearRuntime,
		): RequestResolver.RequestResolver<LinearSyncRequest, never> =>
			RequestResolver.makeBatched<LinearSyncRequest, never>((requests) =>
				Effect.gen(function* () {
					const firstRequest = requests[0]
					const projectPath =
						firstRequest === undefined ? process.cwd() : getEffectiveProjectPath(firstRequest.cwd)
					yield* Effect.log(
						`Linear sync resolver batch start: size=${requests.length} projectPath=${projectPath}`,
					)
					yield* Effect.forEach(
						requests,
						(request) => {
							const runEffect =
								request.operation === "upsert"
									? syncUpsert(runtime, request)
									: syncCloseOrDelete(runtime, request)

							return runEffect.pipe(
								Effect.flatMap(() => Request.succeed(request, undefined)),
								Effect.catchAll((error) =>
									Effect.logWarning(`Recovering after caught error: ${String(error)}`).pipe(
										Effect.zipRight(Request.fail(request, error)),
									),
								),
							)
						},
						{ concurrency: "unbounded", discard: true },
					)
					yield* Effect.log(
						`Linear sync resolver batch complete: size=${requests.length} projectPath=${projectPath}`,
					)
				}).pipe(Effect.asVoid),
			)

		const markRequestFailure = (
			item: CollapsedSyncItem,
			error: IssueSyncError,
			cwd: string | undefined,
			runId: string,
		): Effect.Effect<void, IssueSyncError> =>
			Effect.gen(function* () {
				const nextAttempts = item.attempts + 1
				const failure: IssueSyncFailure = {
					issueId: item.issueId,
					operation: item.operation,
					error: error.message,
					attempts: nextAttempts,
					occurredAt: new Date(),
				}
				const projectPath = getEffectiveProjectPath(cwd)
				const shouldTerminal = nextAttempts >= MAX_SYNC_ATTEMPTS
				if (shouldTerminal) {
					yield* Effect.logWarning(
						`Linear sync dispatch terminal failure: run=${runId} issue=${item.issueId} operation=${item.operation} attempts=${nextAttempts}/${MAX_SYNC_ATTEMPTS} claims=${item.claims.length} projectPath=${projectPath} error=${error.message}`,
					)
					yield* fromStore(
						localStore.markSyncTerminalFailure(
							{
								claims: item.claims,
								errorMessage: error.message,
								nextAttempts,
							},
							cwd,
						),
					).pipe(
						Effect.tap(() =>
							reportSyncHealth({
								status: "failure",
								message: `sync failed for ${item.issueId}`,
								queueDepth: item.claims.length,
								failure,
							}),
						),
					)
					return
				}

				const delaySeconds = toRetryDelaySeconds(item.attempts)
				yield* Effect.logWarning(
					`Linear sync dispatch retry scheduled: run=${runId} issue=${item.issueId} operation=${item.operation} attempts=${nextAttempts}/${MAX_SYNC_ATTEMPTS} delaySeconds=${delaySeconds} claims=${item.claims.length} projectPath=${projectPath} error=${error.message}`,
				)
				yield* fromStore(
					localStore.markSyncRetriable(
						{
							claims: item.claims,
							errorMessage: error.message,
							delaySeconds,
							nextAttempts,
						},
						cwd,
					),
				).pipe(
					Effect.tap(() =>
						reportSyncHealth({
							status: "failure",
							message: `sync retry scheduled for ${item.issueId}`,
							queueDepth: item.claims.length,
							failure,
						}),
					),
				)
			})

		const buildBootstrapSnapshots = (
			scope: LinearIssueScope,
		): Effect.Effect<readonly ExternalIssueSnapshot[], IssueSyncError> =>
			Effect.gen(function* () {
				yield* Effect.log(
					`Linear bootstrap scope: team=${scope.team ?? "<none>"} project=${scope.project ?? "<none>"}`,
				)
				const emptyMetadataMap: ReadonlyMap<string, string> = new Map()
				const issues = yield* fetchAllLinearIssues(scope).pipe(
					Effect.tapError((error) =>
						Effect.logWarning(`Linear bootstrap: fetch issues failed (${error.message}); retrying`),
					),
					Effect.retry({
						schedule: Schedule.recurs(BOOTSTRAP_FETCH_RETRY_ATTEMPTS - 1).pipe(
							Schedule.addDelay(() => BOOTSTRAP_FETCH_RETRY_DELAY),
						),
						while: () => true,
					}),
				)
				yield* Effect.log(
					`Linear bootstrap: fetched ${issues.length} issues before metadata enrichment`,
				)
				const stateNameById = yield* fetchStateNameById().pipe(
					Effect.catchAll((error) =>
						Effect.logWarning(
							`Linear bootstrap: workflow states unavailable, continuing with fallback status mapping (${error.message})`,
						).pipe(Effect.as(emptyMetadataMap)),
					),
				)
				const labelNameById = yield* fetchLabelNameById().pipe(
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
						childCountByParentId.set(
							issue.parentId,
							(childCountByParentId.get(issue.parentId) ?? 0) + 1,
						)
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
							const stateName = issue.stateId ? stateNameById.get(issue.stateId) : undefined
							const status =
								stateName !== undefined
									? normalizeLinearStatus(stateName)
									: issue.completedAt != null || issue.canceledAt != null
										? "closed"
										: issue.startedAt != null
											? "in_progress"
											: "open"
							const parentLocalId = issue.parentId
								? issuesById.get(issue.parentId)?.identifier
								: undefined

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

		const pullRemoteSnapshots = (params: {
			readonly runtime: LinearRuntime
			readonly cwd: string | undefined
			readonly flushRun: SyncRunStart
			readonly pushedClaims: number
		}): Effect.Effect<number, IssueSyncError> =>
			Effect.gen(function* () {
				const projectPath = getEffectiveProjectPath(params.cwd)
				const nowMs = Date.now()
				const lastHydrationAt = yield* Ref.get(lastRemoteHydrationAtRef).pipe(
					Effect.map((entries) => entries.get(projectPath)),
				)
				const dueToPush = params.pushedClaims > 0
				const dueToInterval =
					lastHydrationAt === undefined ||
					nowMs - lastHydrationAt >= LINEAR_REMOTE_HYDRATION_MIN_INTERVAL_MS

				if (!dueToPush && !dueToInterval) {
					const ageMs = lastHydrationAt === undefined ? "none" : String(nowMs - lastHydrationAt)
					yield* Effect.log(
						`Linear remote hydration skipped: run=${params.flushRun.runId} projectPath=${projectPath} reason=cooldown ageMs=${ageMs} minIntervalMs=${LINEAR_REMOTE_HYDRATION_MIN_INTERVAL_MS}`,
					)
					return 0
				}

				const reason = dueToPush ? "post-push" : "interval"
				yield* Effect.log(
					`Linear remote hydration start: run=${params.flushRun.runId} projectPath=${projectPath} reason=${reason}`,
				)

				const snapshots = yield* buildBootstrapSnapshots({
					team: params.runtime.defaultTeam,
					project: params.runtime.defaultProject,
				})
				const pulled =
					snapshots.length === 0
						? 0
						: yield* fromStore(
								localStore.importExternalSnapshot(LINEAR_SYNC_TARGET, snapshots, params.cwd),
							)
				yield* fromStore(localStore.markBootstrapComplete(LINEAR_SYNC_TARGET, params.cwd))
				yield* Ref.update(lastRemoteHydrationAtRef, (entries) => {
					const next = new Map(entries)
					next.set(projectPath, nowMs)
					return next
				})
				yield* Effect.log(
					`Linear remote hydration complete: run=${params.flushRun.runId} projectPath=${projectPath} pulled=${pulled} snapshots=${snapshots.length}`,
				)
				return pulled
			})

		interface BootstrapGate {
			readonly join: Deferred.Deferred<number, IssueSyncError>
			readonly shouldRun: boolean
		}

		interface FlushGate {
			readonly join: Deferred.Deferred<
				{ readonly pushed: number; readonly pulled: number },
				IssueSyncError
			>
			readonly shouldRun: boolean
		}

		const beginSyncRun = (operation: "bootstrap" | "flush"): SyncRunStart => ({
			runId: randomUUID().slice(0, 8),
			operation,
			startedAt: new Date(),
		})

		return {
			bootstrapLinear: (cwd?: string): Effect.Effect<number, IssueSyncError> =>
				Effect.gen(function* () {
					const projectPath = getEffectiveProjectPath(cwd)
					const inFlight = yield* Ref.get(bootstrapInFlightRef).pipe(
						Effect.map((pending) => pending.get(projectPath)),
					)
					if (inFlight !== undefined) {
						yield* Effect.log(
							`Linear bootstrap: awaiting in-flight run for projectPath=${projectPath}`,
						)
						return yield* Deferred.await(inFlight)
					}

					const completion = yield* Deferred.make<number, IssueSyncError>()
					const gate: BootstrapGate = yield* Ref.modify(bootstrapInFlightRef, (pending) => {
						const existing = pending.get(projectPath)
						if (existing !== undefined) {
							const value: BootstrapGate = {
								join: existing,
								shouldRun: false,
							}
							return [value, pending] as const
						}
						const next = new Map(pending)
						next.set(projectPath, completion)
						const value: BootstrapGate = {
							join: completion,
							shouldRun: true,
						}
						return [value, next] as const
					})

					if (!gate.shouldRun) {
						yield* Effect.log(
							`Linear bootstrap: joined concurrent run for projectPath=${projectPath}`,
						)
						return yield* Deferred.await(gate.join)
					}

					const bootstrapRun = beginSyncRun("bootstrap")
					yield* Effect.log(
						`Linear bootstrap run start: run=${bootstrapRun.runId} projectPath=${projectPath}`,
					)

					const runBootstrap: Effect.Effect<number, IssueSyncError> = Effect.gen(function* () {
						const runtimeOption = yield* getLinearRuntime(cwd)
						if (Option.isNone(runtimeOption)) {
							const message = "bootstrap skipped (linear sync unavailable)"
							yield* reportSyncHealth({
								status: "skipped",
								message,
								queueDepth: 0,
								run: createRunHealth({
									run: bootstrapRun,
									status: "skipped",
									message,
									pushed: 0,
									pulled: 0,
								}),
							})
							return 0
						}

						const bootstrapComplete = yield* fromStore(
							localStore.isBootstrapComplete(LINEAR_SYNC_TARGET, cwd),
						)
						if (bootstrapComplete) {
							const message = "bootstrap skipped (already complete)"
							yield* reportSyncHealth({
								status: "skipped",
								message,
								queueDepth: 0,
								run: createRunHealth({
									run: bootstrapRun,
									status: "skipped",
									message,
									pushed: 0,
									pulled: 0,
								}),
							})
							return 0
						}

						const existingCount = yield* fromStore(localStore.countIssues(cwd))
						if (existingCount > 0) {
							yield* fromStore(localStore.markBootstrapComplete(LINEAR_SYNC_TARGET, cwd))
							const message = "bootstrap skipped (local issues already present)"
							yield* reportSyncHealth({
								status: "skipped",
								message,
								queueDepth: 0,
								run: createRunHealth({
									run: bootstrapRun,
									status: "skipped",
									message,
									pushed: 0,
									pulled: 0,
								}),
							})
							return 0
						}

						const snapshots = yield* buildBootstrapSnapshots({
							team: runtimeOption.value.defaultTeam,
							project: runtimeOption.value.defaultProject,
						})
						yield* Effect.log(
							`Linear bootstrap imported ${snapshots.length} issues (run=${bootstrapRun.runId} team=${runtimeOption.value.defaultTeam ?? "<none>"} project=${runtimeOption.value.defaultProject ?? "<none>"})`,
						)
						const imported = yield* fromStore(
							localStore.importExternalSnapshot(LINEAR_SYNC_TARGET, snapshots, cwd),
						)
						yield* fromStore(localStore.markBootstrapComplete(LINEAR_SYNC_TARGET, cwd))
						const totalIssues = yield* fromStore(localStore.countIssues(cwd))
						yield* Effect.log(
							`Linear bootstrap complete: run=${bootstrapRun.runId} projectPath=${projectPath} imported=${imported} totalIssues=${totalIssues}`,
						)
						const message = `bootstrap imported ${imported} issues`
						yield* reportSyncHealth({
							status: "success",
							message,
							queueDepth: 0,
							run: createRunHealth({
								run: bootstrapRun,
								status: "success",
								message,
								pushed: 0,
								pulled: imported,
							}),
						})
						return imported
					})

					return yield* runBootstrap.pipe(
						Effect.tap((result) => Deferred.succeed(completion, result)),
						Effect.tapError((error) =>
							reportSyncHealth({
								status: "failure",
								message: `bootstrap failed: ${error.message}`,
								queueDepth: 0,
								run: createRunHealth({
									run: bootstrapRun,
									status: "failure",
									message: `bootstrap failed: ${error.message}`,
									pushed: 0,
									pulled: 0,
								}),
								failure: {
									issueId: "bootstrap",
									operation: "bootstrap",
									error: error.message,
									attempts: 1,
									occurredAt: new Date(),
								},
							}),
						),
						Effect.tapError((error) => Deferred.fail(completion, error)),
						Effect.ensuring(
							Ref.update(bootstrapInFlightRef, (pending) => {
								const next = new Map(pending)
								if (next.get(projectPath) === completion) {
									next.delete(projectPath)
								}
								return next
							}),
						),
					)
				}),

			flushLinearQueue: (
				cwd?: string,
			): Effect.Effect<{ readonly pushed: number; readonly pulled: number }, IssueSyncError> =>
				Effect.gen(function* () {
					const projectPath = getEffectiveProjectPath(cwd)
					const inFlight = yield* Ref.get(flushInFlightRef).pipe(
						Effect.map((pending) => pending.get(projectPath)),
					)
					if (inFlight !== undefined) {
						yield* Effect.log(`Linear flush: awaiting in-flight run for projectPath=${projectPath}`)
						return yield* Deferred.await(inFlight)
					}

					const completion = yield* Deferred.make<
						{ readonly pushed: number; readonly pulled: number },
						IssueSyncError
					>()
					const gate: FlushGate = yield* Ref.modify(flushInFlightRef, (pending) => {
						const existing = pending.get(projectPath)
						if (existing !== undefined) {
							const value: FlushGate = {
								join: existing,
								shouldRun: false,
							}
							return [value, pending] as const
						}
						const next = new Map(pending)
						next.set(projectPath, completion)
						const value: FlushGate = {
							join: completion,
							shouldRun: true,
						}
						return [value, next] as const
					})

					if (!gate.shouldRun) {
						yield* Effect.log(`Linear flush: joined concurrent run for projectPath=${projectPath}`)
						return yield* Deferred.await(gate.join)
					}

					const flushRun = beginSyncRun("flush")
					yield* Effect.log(
						`Linear flush run start: run=${flushRun.runId} projectPath=${projectPath}`,
					)

					const runFlush: Effect.Effect<
						{ readonly pushed: number; readonly pulled: number },
						IssueSyncError
					> = Effect.gen(function* () {
						const runtimeOption = yield* getLinearRuntime(cwd)
						if (Option.isNone(runtimeOption)) {
							yield* Effect.log(
								`Linear flush skipped: run=${flushRun.runId} projectPath=${projectPath} reason=runtime_unavailable`,
							)
							const message = "flush skipped (linear sync unavailable)"
							yield* reportSyncHealth({
								status: "skipped",
								message,
								queueDepth: 0,
								run: createRunHealth({
									run: flushRun,
									status: "skipped",
									message,
									pushed: 0,
									pulled: 0,
								}),
							})
							return { pushed: 0, pulled: 0 }
						}

						const pendingItems = yield* fromStore(
							localStore.listPendingSync(LINEAR_SYNC_TARGET, MAX_SYNC_BATCH, cwd),
						)
						const queueSummaryBefore = yield* fromStore(
							localStore.getSyncQueueSummary(LINEAR_SYNC_TARGET, cwd),
						)
						yield* setQueueHealth(queueSummaryBefore)
						const collapsed = collapsePendingItems(pendingItems)
						let pushed = 0

						if (collapsed.length > 0) {
							yield* Effect.log(
								`Linear flush dispatching: run=${flushRun.runId} projectPath=${projectPath} pendingClaims=${pendingItems.length} collapsedItems=${collapsed.length} ${formatSyncQueueSummary(queueSummaryBefore)}`,
							)
							const resolver = linearResolver(runtimeOption.value)
							const pushedRef = yield* Ref.make(0)

							yield* Effect.forEach(
								collapsed,
								(item) => {
									const claimCount = item.claims.length
									const attemptCount = item.attempts + 1
									return Effect.log(
										`Linear sync dispatch start: run=${flushRun.runId} issue=${item.issueId} operation=${item.operation} claims=${claimCount} attempt=${attemptCount}/${MAX_SYNC_ATTEMPTS} projectPath=${projectPath}`,
									).pipe(
										Effect.zipRight(
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
															Ref.update(pushedRef, (count) => count + claimCount),
														),
														Effect.zipRight(
															Effect.log(
																`Linear sync dispatch success: run=${flushRun.runId} issue=${item.issueId} operation=${item.operation} claims=${claimCount} projectPath=${projectPath}`,
															),
														),
													),
												),
												Effect.catchAll((error) =>
													Effect.logWarning(error).pipe(
														Effect.zipRight(
															markRequestFailure(item, error, cwd, flushRun.runId),
														),
													),
												),
											),
										),
									)
								},
								{ concurrency: "unbounded" },
							)

							pushed = yield* Ref.get(pushedRef)
						} else {
							yield* Effect.log(
								`Linear flush dispatch skipped: run=${flushRun.runId} projectPath=${projectPath} reason=no_claimable_items ${formatSyncQueueSummary(queueSummaryBefore)}`,
							)
						}

						const pulled = yield* pullRemoteSnapshots({
							runtime: runtimeOption.value,
							cwd,
							flushRun,
							pushedClaims: pushed,
						})
						const queueSummaryAfter = yield* fromStore(
							localStore.getSyncQueueSummary(LINEAR_SYNC_TARGET, cwd),
						)
						yield* setQueueHealth(queueSummaryAfter)
						if (collapsed.length === 0 && pulled === 0) {
							const skippedMessage =
								queueSummaryAfter.total === 0
									? "flush skipped (no pending sync items; hydration not due)"
									: `flush skipped (no claimable items; delayed=${queueSummaryAfter.pendingDelayed} processingActive=${queueSummaryAfter.processingActive} processingStale=${queueSummaryAfter.processingStale} failed=${queueSummaryAfter.failed})`
							yield* reportSyncHealth({
								status: "skipped",
								message: skippedMessage,
								queueDepth: queueSummaryAfter.total,
								run: createRunHealth({
									run: flushRun,
									status: "skipped",
									message: skippedMessage,
									pushed,
									pulled,
								}),
							})
							return { pushed, pulled }
						}

						yield* Effect.log(
							`Linear flush complete: run=${flushRun.runId} projectPath=${projectPath} pendingClaims=${pendingItems.length} collapsedItems=${collapsed.length} pushedClaims=${pushed} pulledIssues=${pulled} ${formatSyncQueueSummary(queueSummaryAfter)}`,
						)
						const message = `flush processed ${collapsed.length} item(s), pushed ${pushed} claim(s), pulled ${pulled} issue snapshot(s), remaining=${queueSummaryAfter.total}`
						yield* reportSyncHealth({
							status: "success",
							message,
							queueDepth: queueSummaryAfter.total,
							run: createRunHealth({
								run: flushRun,
								status: "success",
								message,
								pushed,
								pulled,
							}),
						})
						return { pushed, pulled }
					})

					return yield* runFlush.pipe(
						Effect.tap((result) => Deferred.succeed(completion, result)),
						Effect.tapError((error) =>
							reportSyncHealth({
								status: "failure",
								message: `flush failed: ${error.message}`,
								queueDepth: 0,
								run: createRunHealth({
									run: flushRun,
									status: "failure",
									message: `flush failed: ${error.message}`,
									pushed: 0,
									pulled: 0,
								}),
								failure: {
									issueId: "flush",
									operation: "flush",
									error: error.message,
									attempts: 1,
									occurredAt: new Date(),
								},
							}),
						),
						Effect.tapError((error) => Deferred.fail(completion, error)),
						Effect.ensuring(
							Ref.update(flushInFlightRef, (pending) => {
								const next = new Map(pending)
								if (next.get(projectPath) === completion) {
									next.delete(projectPath)
								}
								return next
							}),
						),
					)
				}).pipe(Effect.withRequestBatching(true)),
		}
	}),
}) {}
