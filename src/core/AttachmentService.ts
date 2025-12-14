/**
 * AttachmentService - Effect service for attaching to Claude Code sessions
 *
 * Uses tmux to attach to running Claude Code sessions for manual intervention.
 */

import { Context, Data, Effect, Layer } from "effect"
import { NotInsideTmuxError, TerminalService, TmuxCommandError } from "./TerminalService"
import { TmuxService } from "./TmuxService"

// ============================================================================
// Error Types
// ============================================================================

/**
 * Error when attachment operation fails
 */
export class AttachmentError extends Data.TaggedError("AttachmentError")<{
	readonly message: string
	readonly sessionId?: string
	readonly cause?: unknown
}> {}

/**
 * Error when session doesn't exist
 */
export class SessionNotFoundError extends Data.TaggedError("SessionNotFoundError")<{
	readonly sessionId: string
}> {}

/**
 * Error when terminal detection or command execution fails
 */
export class TerminalError extends Data.TaggedError("TerminalError")<{
	readonly message: string
	readonly terminalType?: string
}> {}

// ============================================================================
// Type Definitions
// ============================================================================

/**
 * Attachment mode
 */
export type AttachmentMode = "external" | "inline"

/**
 * Attachment event for tracking
 */
export interface AttachmentEvent {
	readonly sessionId: string
	readonly mode: AttachmentMode
	readonly timestamp: Date
}

// ============================================================================
// Service Definition
// ============================================================================

/**
 * AttachmentService interface
 *
 * Provides methods for attaching to tmux sessions running Claude Code.
 */
export interface AttachmentServiceI {
	/**
	 * Attach to a session in external terminal window
	 *
	 * Opens a new terminal window/tab with tmux attach command.
	 * Terminal type is auto-detected from environment.
	 *
	 * @example
	 * ```ts
	 * const service = yield* AttachmentService
	 * yield* service.attachExternal("claude-az-05y")
	 * ```
	 */
	readonly attachExternal: (
		sessionId: string,
	) => Effect.Effect<void, AttachmentError | SessionNotFoundError>

	/**
	 * Attach to a session inline (replace TUI)
	 *
	 * Future implementation: Will pause the TUI and attach to the session
	 * within the current terminal. Detaching returns to the TUI.
	 *
	 * @example
	 * ```ts
	 * const service = yield* AttachmentService
	 * yield* service.attachInline("claude-az-05y")
	 * ```
	 */
	readonly attachInline: (
		sessionId: string,
	) => Effect.Effect<void, AttachmentError | SessionNotFoundError>

	/**
	 * Get list of sessions user has attached to
	 *
	 * Returns array of attachment events for tracking which sessions
	 * have been manually inspected.
	 */
	readonly getAttachmentHistory: () => Effect.Effect<readonly AttachmentEvent[], never>

	/**
	 * Check if user has attached to a session
	 */
	readonly hasAttached: (sessionId: string) => Effect.Effect<boolean, never>
}

/**
 * AttachmentService tag
 */
export class AttachmentService extends Context.Tag("AttachmentService")<
	AttachmentService,
	AttachmentServiceI
>() {}

// ============================================================================
// Implementation
// ============================================================================

/**
 * Live AttachmentService implementation
 *
 * Depends on TmuxService and TerminalService for session and terminal operations.
 */
const AttachmentServiceImpl = Effect.gen(function* () {
	const tmux = yield* TmuxService
	const terminal = yield* TerminalService

	// Track attachment history
	const attachmentHistory: AttachmentEvent[] = []

	/**
	 * Record an attachment event
	 */
	const recordAttachment = (sessionId: string, mode: AttachmentMode): void => {
		attachmentHistory.push({
			sessionId,
			mode,
			timestamp: new Date(),
		})
	}

	return AttachmentService.of({
		attachExternal: (sessionId: string) =>
			Effect.gen(function* () {
				// Check if session exists
				const sessionExists = yield* tmux.hasSession(sessionId).pipe(
					Effect.mapError(
						(error) =>
							new AttachmentError({
								message: `Failed to check session existence: ${error.message}`,
								sessionId,
								cause: error,
							}),
					),
				)

				if (!sessionExists) {
					return yield* Effect.fail(new SessionNotFoundError({ sessionId }))
				}

				// Switch to the Claude session
				// Claude sessions use Ctrl-a prefix (not Ctrl-b) so users can switch back
				yield* tmux.switchClient(sessionId).pipe(
					Effect.mapError(
						(error) =>
							new AttachmentError({
								message: `Failed to switch to session: ${error}`,
								sessionId,
								cause: error,
							}),
					),
				)

				// Record the attachment
				yield* Effect.sync(() => recordAttachment(sessionId, "external"))
			}),

		attachInline: (sessionId: string) =>
			Effect.gen(function* () {
				// Check if session exists
				const sessionExists = yield* tmux.hasSession(sessionId).pipe(
					Effect.mapError(
						(error) =>
							new AttachmentError({
								message: `Failed to check session existence: ${error.message}`,
								sessionId,
								cause: error,
							}),
					),
				)

				if (!sessionExists) {
					return yield* Effect.fail(new SessionNotFoundError({ sessionId }))
				}

				// TODO: Implement inline attachment
				// This will require:
				// 1. Suspending the TUI renderer
				// 2. Attaching to the tmux session in the current terminal
				// 3. Handling detach (Ctrl-b d) to return to TUI
				// 4. Resuming the TUI renderer
				//
				// For now, return an error indicating it's not implemented
				return yield* Effect.fail(
					new AttachmentError({
						message:
							"Inline attachment not yet implemented. Use external attachment (key 'a') instead.",
						sessionId,
					}),
				)
			}),

		getAttachmentHistory: () => Effect.succeed([...attachmentHistory]),

		hasAttached: (sessionId: string) =>
			Effect.succeed(attachmentHistory.some((event) => event.sessionId === sessionId)),
	})
})

// ============================================================================
// Layers
// ============================================================================

/**
 * Live AttachmentService layer (without platform dependencies)
 *
 * Requires TmuxService and TerminalService to be provided.
 */
export const AttachmentServiceLive = Layer.effect(AttachmentService, AttachmentServiceImpl)

// ============================================================================
// Convenience Functions
// ============================================================================

/**
 * Attach to a session externally (convenience function)
 *
 * @example
 * ```ts
 * yield* attachExternal("claude-az-05y")
 * ```
 */
export const attachExternal = (
	sessionId: string,
): Effect.Effect<void, AttachmentError | SessionNotFoundError, AttachmentService> =>
	Effect.flatMap(AttachmentService, (service) => service.attachExternal(sessionId))

/**
 * Attach to a session inline (convenience function)
 *
 * @example
 * ```ts
 * yield* attachInline("claude-az-05y")
 * ```
 */
export const attachInline = (
	sessionId: string,
): Effect.Effect<void, AttachmentError | SessionNotFoundError, AttachmentService> =>
	Effect.flatMap(AttachmentService, (service) => service.attachInline(sessionId))

/**
 * Get attachment history (convenience function)
 */
export const getAttachmentHistory = (): Effect.Effect<
	readonly AttachmentEvent[],
	never,
	AttachmentService
> => Effect.flatMap(AttachmentService, (service) => service.getAttachmentHistory())

/**
 * Check if session has been attached (convenience function)
 */
export const hasAttached = (sessionId: string): Effect.Effect<boolean, never, AttachmentService> =>
	Effect.flatMap(AttachmentService, (service) => service.hasAttached(sessionId))
