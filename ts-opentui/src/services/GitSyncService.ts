/**
 * GitSyncService - Background git fetch and pull notification for origin mode
 *
 * In origin mode, this service:
 * - Polls `git fetch origin` every 30 seconds
 * - Checks if the local base branch is behind origin
 * - Automatically shows pull notification overlay when updates are available
 * - Provides pull action to update the local base branch
 *
 * Only active when:
 * - workflowMode === "origin"
 * - git.fetchEnabled === true
 * - Network is online
 *
 * All side effects (overlay notification) are handled internally -
 * React is only for rendering.
 */

import { Command, type CommandExecutor } from "@effect/platform"
import { Data, Effect, Ref, Schedule, Stream, SubscriptionRef } from "effect"
import { AppConfig } from "../config/AppConfig.js"
import { DiagnosticsService } from "./DiagnosticsService.js"
import { OfflineService } from "./OfflineService.js"
import { OverlayService } from "./OverlayService.js"
import { ProjectService } from "./ProjectService.js"
import { ToastService } from "./ToastService.js"

// ============================================================================
// Errors
// ============================================================================

export class GitSyncError extends Data.TaggedError("GitSyncError")<{
	readonly message: string
	readonly command?: string
}> {}

// ============================================================================
// Helper: Run git command
// ============================================================================

const runGit = (
	args: readonly string[],
	cwd: string,
): Effect.Effect<string, GitSyncError, CommandExecutor.CommandExecutor> =>
	Effect.gen(function* () {
		const command = Command.make("git", ...args).pipe(Command.workingDirectory(cwd))
		return yield* Command.string(command).pipe(
			Effect.mapError((error) => {
				const stderr = "stderr" in error ? String(error.stderr) : String(error)
				return new GitSyncError({
					message: stderr,
					command: `git ${args.join(" ")}`,
				})
			}),
		)
	})

// ============================================================================
// Service
// ============================================================================

export class GitSyncService extends Effect.Service<GitSyncService>()("GitSyncService", {
	dependencies: [
		DiagnosticsService.Default,
		AppConfig.Default,
		OfflineService.Default,
		ProjectService.Default,
		OverlayService.Default,
		ToastService.Default,
	],
	scoped: Effect.gen(function* () {
		const diagnostics = yield* DiagnosticsService
		const appConfig = yield* AppConfig
		const offlineService = yield* OfflineService
		const projectService = yield* ProjectService
		const overlay = yield* OverlayService
		const toast = yield* ToastService

		yield* diagnostics.trackService("GitSyncService", "Git fetch/pull sync for origin mode")

		// State
		const commitsBehind = yield* SubscriptionRef.make<number>(0)
		const isFetching = yield* SubscriptionRef.make<boolean>(false)
		// Track the count we've already notified about to avoid re-notifying
		const lastNotifiedCount = yield* Ref.make<number>(0)
		// Simple lock to prevent concurrent fetch operations
		const fetchLock = yield* Ref.make<boolean>(false)

		/**
		 * Fetch from origin and check if base branch is behind
		 *
		 * This is the core operation that runs periodically and on manual trigger.
		 */
		const fetchAndCheck = () =>
			Effect.gen(function* () {
				// Simple lock check
				const isLocked = yield* Ref.get(fetchLock)
				if (isLocked) return

				yield* Ref.set(fetchLock, true)

				yield* Effect.gen(function* () {
					// Check if we should run (origin mode + fetch enabled + online)
					const workflowMode = yield* appConfig.getWorkflowMode()
					if (workflowMode !== "origin") {
						return // Silent skip in local mode
					}

					const fetchStatus = yield* offlineService.isGitFetchEnabled()
					if (!fetchStatus.enabled) {
						return // Silent skip when fetch disabled or offline
					}

					const projectPath = yield* projectService.getCurrentPath()
					if (!projectPath) {
						return // No project open
					}

					const gitConfig = yield* appConfig.getGitConfig()
					const { baseBranch, remote } = gitConfig

					yield* SubscriptionRef.set(isFetching, true)

					// Step 1: Fetch from origin
					yield* runGit(["fetch", remote, baseBranch], projectPath).pipe(
						Effect.catchAll((e) => Effect.logWarning(`Git fetch failed: ${e.message}`)),
					)

					// Step 2: Check how many commits behind
					// Compare local baseBranch to remote-tracking branch (origin/baseBranch)
					const behindCount = yield* runGit(
						["rev-list", "--count", `${baseBranch}..${remote}/${baseBranch}`],
						projectPath,
					).pipe(
						Effect.map((output) => Number.parseInt(output.trim(), 10)),
						Effect.catchAll(() => Effect.succeed(0)),
					)

					yield* SubscriptionRef.set(commitsBehind, behindCount)

					yield* Effect.log(
						`Git sync: ${baseBranch} is ${behindCount} commits behind ${remote}/${baseBranch}`,
					)
				}).pipe(
					Effect.ensuring(
						Effect.all([SubscriptionRef.set(isFetching, false), Ref.set(fetchLock, false)]),
					),
				)
			})

		/**
		 * Pull updates to local base branch
		 *
		 * This pulls from origin into the local base branch.
		 * After pulling, resets the commitsBehind counter.
		 */
		const pull = () =>
			Effect.gen(function* () {
				const projectPath = yield* projectService.getCurrentPath()
				if (!projectPath) {
					return yield* Effect.fail(new GitSyncError({ message: "No project open" }))
				}

				const gitConfig = yield* appConfig.getGitConfig()
				const { baseBranch, remote } = gitConfig

				// Get current branch to check if we're on the base branch
				const currentBranch = yield* runGit(["branch", "--show-current"], projectPath).pipe(
					Effect.map((b) => b.trim()),
				)

				if (currentBranch === baseBranch) {
					// We're on the base branch - just pull
					yield* runGit(["pull", remote, baseBranch], projectPath)
				} else {
					// We're on a different branch - use fetch + merge-base update
					// This updates the local base branch without switching to it
					yield* runGit(["fetch", remote, `${baseBranch}:${baseBranch}`], projectPath)
				}

				// Reset counters after successful pull
				yield* SubscriptionRef.set(commitsBehind, 0)
				yield* Ref.set(lastNotifiedCount, 0)
			})

		/**
		 * Show notification overlay if we should notify
		 *
		 * Called internally when commitsBehind changes.
		 * If a gitPull overlay is already showing, update it with new count.
		 * If another overlay is open, skip (don't interrupt user).
		 */
		const maybeShowNotification = (currentBehind: number) =>
			Effect.gen(function* () {
				// Only notify in origin mode
				const workflowMode = yield* appConfig.getWorkflowMode()
				if (workflowMode !== "origin") {
					return
				}

				// Nothing to notify about
				if (currentBehind <= 0) {
					return
				}

				const gitConfig = yield* appConfig.getGitConfig()
				const { baseBranch, remote } = gitConfig

				// Create the pull effect (captures currentBehind at creation time)
				const createPullEffect = (count: number) =>
					Effect.gen(function* () {
						yield* pull()
						yield* toast.show("success", `Pulled ${count} commits from ${remote}/${baseBranch}`)
					}).pipe(Effect.catchAll((e) => toast.show("error", `Pull failed: ${e}`)))

				// Check current overlay state
				const currentOverlay = yield* overlay.current()

				if (currentOverlay?._tag === "gitPull") {
					// Already showing gitPull - update it if count changed
					if (currentOverlay.commitsBehind !== currentBehind) {
						yield* overlay.pop()
						yield* overlay.push({
							_tag: "gitPull",
							commitsBehind: currentBehind,
							baseBranch,
							remote,
							onConfirm: createPullEffect(currentBehind),
						})
					}
					return
				}

				if (currentOverlay) {
					// Some other overlay is open - don't interrupt
					return
				}

				// No overlay open - check if we should notify (more commits than last notified)
				const lastNotified = yield* Ref.get(lastNotifiedCount)
				if (currentBehind <= lastNotified) {
					return
				}

				// Mark as notified immediately to prevent duplicate popups
				yield* Ref.set(lastNotifiedCount, currentBehind)

				// Push the gitPull overlay
				yield* overlay.push({
					_tag: "gitPull",
					commitsBehind: currentBehind,
					baseBranch,
					remote,
					onConfirm: createPullEffect(currentBehind),
				})
			}).pipe(Effect.catchAll(Effect.logError))

		// Watch commitsBehind changes and trigger notification
		const notificationFiber = yield* Effect.forkScoped(
			Stream.runForEach(commitsBehind.changes, (behind) => maybeShowNotification(behind)),
		)

		yield* diagnostics.registerFiber({
			id: "git-sync-notification",
			name: "Git Sync Notification",
			description: "Watches for commits behind and shows pull notification",
			fiber: notificationFiber,
		})

		// Start background polling (30 seconds)
		const pollingFiber = yield* Effect.forkScoped(
			Effect.repeat(Schedule.spaced("30 seconds"))(
				fetchAndCheck().pipe(
					Effect.catchAll((e) => Effect.logWarning(`Git sync polling error: ${e}`)),
				),
			),
		)

		yield* diagnostics.registerFiber({
			id: "git-sync-polling",
			name: "Git Sync Polling",
			description: "Fetches from origin every 30 seconds to detect available updates",
			fiber: pollingFiber,
		})

		// Initial fetch on startup
		yield* fetchAndCheck().pipe(
			Effect.catchAll((e) => Effect.logWarning(`Initial git sync failed: ${e}`)),
		)

		return {
			// Reactive state
			commitsBehind,
			isFetching,

			// Actions
			fetchAndCheck,
			pull,
		}
	}),
}) {}
