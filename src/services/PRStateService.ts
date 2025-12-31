/**
 * PRStateService - Cached PR state polling via gh CLI
 *
 * Provides accurate PR state (open/draft/merged/closed) by polling GitHub.
 * Caches results for 30 seconds to minimize API calls.
 *
 * Used by BoardService to enrich TaskWithSession with prState field.
 */

import { Command } from "@effect/platform"
import { Effect, Ref } from "effect"
import type { PRState } from "../ui/types.js"
import { DiagnosticsService } from "./DiagnosticsService.js"

// ============================================================================
// Types
// ============================================================================

/** TTL for PR state cache (30 seconds) */
const PR_STATE_CACHE_TTL_MS = 30000

/** Cached PR state entry */
interface PRStateCacheEntry {
	readonly state: PRState
	readonly timestamp: number
}

/** gh pr view JSON output structure */
interface GHPRView {
	state: "OPEN" | "CLOSED" | "MERGED"
	isDraft: boolean
}

// ============================================================================
// Service Definition
// ============================================================================

export class PRStateService extends Effect.Service<PRStateService>()("PRStateService", {
	dependencies: [DiagnosticsService.Default],
	scoped: Effect.gen(function* () {
		const diagnostics = yield* DiagnosticsService
		yield* diagnostics.trackService("PRStateService", "gh CLI polling for PR states (30s cache)")

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
					Effect.catchAll(() => Effect.succeed(1)),
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
				if (cached && now - cached.timestamp < PR_STATE_CACHE_TTL_MS) {
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
				const command = Command.make("gh", "pr", "view", prNumber!, "--json", "state,isDraft").pipe(
					Command.workingDirectory(projectPath),
					Command.string,
				)

				const result = yield* command.pipe(
					Effect.map((output): PRState => {
						const data = JSON.parse(output) as GHPRView
						// Map gh CLI state to our PRState type
						if (data.isDraft) return "draft"
						switch (data.state) {
							case "OPEN":
								return "open"
							case "MERGED":
								return "merged"
							case "CLOSED":
								return "closed"
							default:
								return "open"
						}
					}),
					Effect.catchAll(() => Effect.succeed(undefined as PRState | undefined)),
				)

				// Update cache if we got a result
				if (result !== undefined) {
					yield* Ref.update(prStateCache, (c) => {
						const newCache = new Map(c)
						newCache.set(prUrl, { state: result, timestamp: now })
						return newCache
					})
				}

				return result
			})

		/**
		 * Batch fetch PR states for multiple URLs
		 *
		 * Fetches in parallel with bounded concurrency to avoid overwhelming gh CLI.
		 *
		 * @param prInfos - Array of { prUrl, beadId } tuples
		 * @param projectPath - Path to git repo
		 * @returns Map of beadId -> PRState
		 */
		const getPRStates = (prInfos: { prUrl: string; beadId: string }[], projectPath: string) =>
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
					prInfos.map(({ prUrl, beadId }) =>
						getPRState(prUrl, projectPath).pipe(Effect.map((state) => [beadId, state] as const)),
					),
					{ concurrency: 5 },
				)

				// Build result map, filtering out undefined states
				const stateMap = new Map<string, PRState>()
				for (const [beadId, state] of results) {
					if (state !== undefined) {
						stateMap.set(beadId, state)
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
