/**
 * PRStateService - Cached PR state polling via gh CLI
 *
 * Provides accurate PR state (open/draft/merged/closed) by polling GitHub.
 * Cache windows adapt to check activity so active CI gets polled more often
 * while settled PRs back off.
 *
 * Used by BoardService to enrich TaskWithSession with prState field.
 */

import { Command } from "@effect/platform"
import { Effect, Ref, Schema } from "effect"
import type { PRState } from "../ui/types.js"
import { DiagnosticsService } from "./DiagnosticsService.js"

// ============================================================================
// Types
// ============================================================================

/** Baseline TTL for PR state cache (30 seconds) */
const PR_STATE_CACHE_TTL_MS = 30000

/** Faster polling window when checks are still pending */
const PR_STATE_PENDING_CACHE_TTL_MS = 10000

/** Slower polling window once checks have settled */
const PR_STATE_SETTLED_CACHE_TTL_MS = 120000

/** Cached PR state entry */
interface PRStateCacheEntry {
	readonly state: PRState
	readonly expiresAtMs: number
}

const GHPRViewSchema = Schema.Struct({
	state: Schema.Literal("OPEN", "CLOSED", "MERGED"),
	isDraft: Schema.Boolean,
	statusCheckRollup: Schema.optional(
		Schema.NullOr(
			Schema.Struct({
				state: Schema.String,
			}),
		),
	),
})

type GHPRView = Schema.Schema.Type<typeof GHPRViewSchema>

const pendingCheckRollupStates = new Set([
	"expected",
	"in_progress",
	"pending",
	"queued",
	"requested",
	"waiting",
])

export const isPendingStatusCheckRollupState = (state: string | undefined): boolean => {
	if (state === undefined) return false
	return pendingCheckRollupStates.has(state.trim().toLowerCase())
}

export const resolvePRStateCacheTtlMs = (params: {
	readonly state: PRState
	readonly statusCheckRollupState: string | undefined
}): number => {
	if (isPendingStatusCheckRollupState(params.statusCheckRollupState)) {
		return PR_STATE_PENDING_CACHE_TTL_MS
	}
	if (params.state === "merged" || params.state === "closed") {
		return PR_STATE_SETTLED_CACHE_TTL_MS
	}
	return PR_STATE_CACHE_TTL_MS
}

// ============================================================================
// Service Definition
// ============================================================================

export class PRStateService extends Effect.Service<PRStateService>()("PRStateService", {
	dependencies: [DiagnosticsService.Default],
	scoped: Effect.gen(function* () {
		const diagnostics = yield* DiagnosticsService
		yield* diagnostics.trackService(
			"PRStateService",
			"gh CLI polling for PR states (adaptive cache windows)",
		)

		// Cache keyed by PR URL
		const prStateCache = yield* Ref.make<Map<string, PRStateCacheEntry>>(new Map())

		// Track if gh CLI is available (checked once)
		const ghAvailable = yield* Ref.make<boolean | null>(null)

		/**
		 * Check if gh CLI is available and authenticated
		 */
		const checkGHCLI = () =>
			Effect.gen(function* () {
				const cached = yield* Ref.get(ghAvailable)
				if (cached !== null) return cached

				const command = Command.make("gh", "auth", "status")
				const exitCode = yield* Command.exitCode(command).pipe(
					Effect.catchAll((error) =>
						Effect.logWarning(`Recovering after caught error: ${String(error)}`).pipe(
							Effect.zipRight(Effect.succeed(1)),
						),
					),
				)
				const available = exitCode === 0
				yield* Ref.set(ghAvailable, available)
				return available
			})

		/**
		 * Get PR state from cache or fetch from gh CLI
		 *
		 * @param prUrl - Full GitHub PR URL (e.g., https://github.com/org/repo/pull/123)
		 * @param projectPath - Path to git repo (for running gh command)
		 * @returns PR state, or undefined if gh CLI unavailable/error
		 */
		const getPRState = (prUrl: string, projectPath: string) =>
			Effect.gen(function* () {
				const now = Date.now()

				// Check cache first
				const cache = yield* Ref.get(prStateCache)
				const cached = cache.get(prUrl)
				if (cached && now < cached.expiresAtMs) {
					return cached.state
				}

				// Check if gh CLI is available
				const available = yield* checkGHCLI()
				if (!available) {
					return undefined
				}

				// Extract PR number from URL
				const prNumberMatch = prUrl.match(/\/pull\/(\d+)/)
				if (!prNumberMatch) {
					return undefined
				}
				const prNumber = prNumberMatch[1]

				// Fetch from gh CLI
				const command = Command.make(
					"gh",
					"pr",
					"view",
					prNumber!,
					"--json",
					"state,isDraft,statusCheckRollup",
				).pipe(Command.workingDirectory(projectPath), Command.string)

				const decoded = yield* command.pipe(
					Effect.flatMap((output) => Schema.decode(Schema.parseJson(GHPRViewSchema))(output)),
					Effect.catchAll((error) =>
						Effect.logWarning(error).pipe(
							Effect.zipRight(Effect.succeed(undefined as GHPRView | undefined)),
						),
					),
				)

				if (decoded === undefined) {
					return undefined
				}

				const result = {
					state: decoded.isDraft
						? "draft"
						: (() => {
								switch (decoded.state) {
									case "OPEN":
										return "open"
									case "MERGED":
										return "merged"
									case "CLOSED":
										return "closed"
									default:
										return "open"
								}
							})(),
					checkState: decoded.statusCheckRollup?.state,
				} as const

				// Update cache if we got a result
				const cacheTtlMs = resolvePRStateCacheTtlMs({
					state: result.state,
					statusCheckRollupState: result.checkState,
				})
				yield* Ref.update(prStateCache, (c) => {
					const newCache = new Map(c)
					newCache.set(prUrl, { state: result.state, expiresAtMs: now + cacheTtlMs })
					return newCache
				})

				return result.state
			})

		/**
		 * Batch fetch PR states for multiple URLs
		 *
		 * Fetches in parallel with bounded concurrency to avoid overwhelming gh CLI.
		 *
		 * @param prInfos - Array of { prUrl, issueId } tuples
		 * @param projectPath - Path to git repo
		 * @returns Map of issueId -> PRState
		 */
		const getPRStates = (prInfos: { prUrl: string; issueId: string }[], projectPath: string) =>
			Effect.gen(function* () {
				if (prInfos.length === 0) {
					return new Map<string, PRState>()
				}

				// Check gh CLI availability first (single check for batch)
				const available = yield* checkGHCLI()
				if (!available) {
					return new Map<string, PRState>()
				}

				// Fetch all PR states in parallel (bounded concurrency)
				const results = yield* Effect.all(
					prInfos.map(({ prUrl, issueId }) =>
						getPRState(prUrl, projectPath).pipe(Effect.map((state) => [issueId, state] as const)),
					),
					{ concurrency: 5 },
				)

				// Build result map, filtering out undefined states
				const stateMap = new Map<string, PRState>()
				for (const [issueId, state] of results) {
					if (state !== undefined) {
						stateMap.set(issueId, state)
					}
				}

				return stateMap
			})

		/**
		 * Clear the cache (useful after creating/merging PRs)
		 */
		const clearCache = () => Ref.set(prStateCache, new Map())

		return {
			getPRState,
			getPRStates,
			checkGHCLI,
			clearCache,
		}
	}),
}) {}
