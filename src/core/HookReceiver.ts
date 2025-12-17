/**
 * HookReceiver - Effect service for receiving Claude Code hook notifications
 *
 * Watches for notification files written by `az notify` command and translates
 * hook events into session state changes. This enables authoritative state
 * detection from Claude Code's native hook system.
 *
 * Notification file format: /tmp/azedarach-notify-<bead-id>.json
 * {
 *   "event": "idle_prompt" | "stop" | "session_end",
 *   "beadId": "az-123",
 *   "timestamp": 1234567890
 * }
 *
 * Event to SessionState mapping:
 * - idle_prompt → "waiting" (Claude waiting for user input 60s+)
 * - stop → (no change) (Claude finished responding, but task continues)
 * - session_end → "idle" (Session terminated)
 */

import { FileSystem } from "@effect/platform"
import { Data, Effect, type Fiber, Ref, Schedule } from "effect"
import type { SessionState } from "../ui/types.js"

// ============================================================================
// Type Definitions
// ============================================================================

/**
 * Hook event types from Claude Code
 */
export type HookEventType = "idle_prompt" | "stop" | "session_end"

/**
 * Hook event payload (matches what `az notify` writes)
 */
export interface HookEvent {
	readonly event: HookEventType
	readonly beadId: string
	readonly timestamp: number
}

/**
 * Callback for processing hook events
 */
export type HookEventHandler = (event: HookEvent) => Effect.Effect<void, never>

// ============================================================================
// Error Types
// ============================================================================

/**
 * Error when hook receiver fails
 */
export class HookReceiverError extends Data.TaggedError("HookReceiverError")<{
	readonly message: string
	readonly cause?: unknown
}> {}

// ============================================================================
// Constants
// ============================================================================

/**
 * Notification file pattern
 */
const NOTIFY_DIR = "/tmp"
const NOTIFY_PREFIX = "azedarach-notify-"
const NOTIFY_SUFFIX = ".json"

/**
 * Polling interval for watching notification files
 */
const POLL_INTERVAL_MS = 500

// ============================================================================
// Event Mapping
// ============================================================================

/**
 * Map hook event type to SessionState
 *
 * Returns null if the event should not trigger a state change.
 */
export const mapEventToState = (event: HookEventType): SessionState | null => {
	switch (event) {
		case "idle_prompt":
			// Claude has been waiting for input for 60+ seconds
			return "waiting"
		case "session_end":
			// Session terminated - return to idle
			return "idle"
		case "stop":
			// Claude finished responding, but this doesn't change the session state
			// The session continues - it could be busy, waiting, or done
			return null
		default:
			return null
	}
}

// ============================================================================
// Service Definition
// ============================================================================

/**
 * HookReceiver service interface
 *
 * Provides hook event watching capabilities for receiving notifications
 * from Claude Code sessions running in worktrees.
 */
export interface HookReceiverService {
	/**
	 * Start watching for hook notifications
	 *
	 * Returns a Fiber that can be interrupted to stop watching.
	 * Events are pushed to the provided handler.
	 */
	readonly start: (handler: HookEventHandler) => Effect.Effect<Fiber.RuntimeFiber<number, never>>

	/**
	 * Process a single notification file
	 *
	 * Reads, parses, and deletes the notification file.
	 * Called internally by the watcher, but exposed for testing.
	 */
	readonly processNotification: (path: string) => Effect.Effect<HookEvent | null, never>

	/**
	 * Get list of pending notification files
	 */
	readonly listPendingNotifications: () => Effect.Effect<readonly string[], never>
}

// ============================================================================
// Service Implementation
// ============================================================================

/**
 * HookReceiver service
 *
 * Polls for notification files and processes them when found.
 * Uses a simple file-based IPC mechanism for reliability.
 *
 * @example
 * ```ts
 * const program = Effect.gen(function* () {
 *   const receiver = yield* HookReceiver
 *   const fiber = yield* receiver.start((event) =>
 *     Effect.gen(function* () {
 *       const newState = mapEventToState(event.event)
 *       if (newState) {
 *         yield* sessionManager.updateState(event.beadId, newState)
 *       }
 *     })
 *   )
 *   // Later: yield* Fiber.interrupt(fiber)
 * }).pipe(Effect.provide(HookReceiver.Default))
 * ```
 */
export class HookReceiver extends Effect.Service<HookReceiver>()("HookReceiver", {
	effect: Effect.gen(function* () {
		const fs = yield* FileSystem.FileSystem

		// Track processed files to avoid duplicates
		const processedRef = yield* Ref.make<Set<string>>(new Set())

		const listPendingNotifications = () =>
			Effect.gen(function* () {
				const entries = yield* fs
					.readDirectory(NOTIFY_DIR)
					.pipe(Effect.catchAll(() => Effect.succeed([] as readonly string[])))

				return entries.filter(
					(entry) => entry.startsWith(NOTIFY_PREFIX) && entry.endsWith(NOTIFY_SUFFIX),
				)
			})

		const processNotification = (filename: string) =>
			Effect.gen(function* () {
				const fullPath = `${NOTIFY_DIR}/${filename}`

				// Check if already processed
				const processed = yield* Ref.get(processedRef)
				if (processed.has(fullPath)) {
					return null
				}

				// Read file content
				const content = yield* fs
					.readFileString(fullPath)
					.pipe(Effect.catchAll(() => Effect.succeed(null)))

				if (content === null) {
					return null
				}

				// Parse JSON
				const event = yield* Effect.try({
					try: () => JSON.parse(content) as HookEvent,
					catch: () => null,
				}).pipe(Effect.catchAll(() => Effect.succeed(null)))

				if (event === null) {
					// Invalid JSON, delete the file anyway
					yield* fs.remove(fullPath).pipe(Effect.catchAll(() => Effect.void))
					return null
				}

				// Mark as processed
				yield* Ref.update(processedRef, (s) => new Set([...s, fullPath]))

				// Delete the notification file
				yield* fs.remove(fullPath).pipe(Effect.catchAll(() => Effect.void))

				// Clean up processed set (remove after 1 minute to prevent memory leak)
				yield* Effect.sleep("1 minute").pipe(
					Effect.flatMap(() =>
						Ref.update(processedRef, (s) => {
							const next = new Set(s)
							next.delete(fullPath)
							return next
						}),
					),
					Effect.fork,
				)

				return event
			})

		const start = (handler: HookEventHandler) =>
			Effect.gen(function* () {
				// Start a single polling fiber that processes notifications directly
				// This is simpler than queue-based approach and sufficient for our needs
				const pollerFiber = yield* Effect.gen(function* () {
					const files = yield* listPendingNotifications()

					for (const file of files) {
						const event = yield* processNotification(file)
						if (event) {
							yield* handler(event)
						}
					}
				}).pipe(
					// Catch errors inside the loop to prevent stopping
					Effect.catchAll(() => Effect.void),
					// Repeat indefinitely with 500ms interval
					Effect.repeat(Schedule.spaced(`${POLL_INTERVAL_MS} millis`)),
					Effect.fork,
				)

				return pollerFiber
			})

		return {
			start,
			processNotification,
			listPendingNotifications,
		}
	}),
}) {}

// ============================================================================
// Convenience Functions
// ============================================================================

/**
 * Start the hook receiver with a handler (convenience function)
 */
export const startHookReceiver = (
	handler: HookEventHandler,
): Effect.Effect<Fiber.RuntimeFiber<number, never>, never, HookReceiver> =>
	Effect.flatMap(HookReceiver, (receiver) => receiver.start(handler))

/**
 * List pending notification files (convenience function)
 */
export const listPendingNotifications = (): Effect.Effect<readonly string[], never, HookReceiver> =>
	Effect.flatMap(HookReceiver, (receiver) => receiver.listPendingNotifications())
